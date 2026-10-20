package canvas

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	agentsuite "github.com/agent-primitives/suite"
	"github.com/google/jsonschema-go/jsonschema"
	"go.yaml.in/yaml/v3"
)

type DocumentDefinition struct {
	Path      string `yaml:"path" json:"path"`
	Schema    string `yaml:"schema,omitempty" json:"schema,omitempty"`
	Title     string `yaml:"title,omitempty" json:"title,omitempty"`
	EditAgent string `yaml:"editAgent,omitempty" json:"editAgent,omitempty"`
}

// ActionDefinition declares a local simulated publication capability, never a model tool.
type ActionDefinition struct {
	Name     string `yaml:"name" json:"name"`
	Kind     string `yaml:"kind" json:"kind"`
	Document string `yaml:"document" json:"document"`
}

type EvaluationReference struct {
	Expected string `yaml:"expected,omitempty" json:"expected,omitempty"`
	Evidence string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

type AgentDefinition struct {
	Evaluation EvaluationReference `yaml:"evaluation,omitempty" json:"evaluation,omitempty"`
	Name       string              `yaml:"name" json:"name"`
	Suite      contract.SuiteRef   `yaml:"suite" json:"suite"`
	Read       []string            `yaml:"read" json:"read"`
	Propose    []string            `yaml:"propose" json:"propose"`
}

// CompiledDefinition exposes presentation/configuration data. Changes uses a
// private compiled scope/schema snapshot, never the caller-mutable maps.
type CompiledDefinition struct {
	archives       *contract.ArchiveResolver
	verifiedSuites map[string]*contract.SuiteIdentity
	APIVersion     string
	ReviewRoles    []string
	Flows          map[string]FlowDefinition
	flows          map[string]compiledFlow
	ID             string
	Title          string
	Digest         string
	Documents      map[string]DocumentDefinition
	Agents         map[string]AgentDefinition
	Bundles        []contract.Bundle
	Seed           map[string]string
	Triggers       []Trigger
	People         []Person
	Policy         sharedspace.Policy
	Actions        map[string]ActionDefinition
	actions        map[string]ActionDefinition
	documents      map[string]compiledDocument
	proposals      map[string]map[string]bool
}
type compiledDocument struct {
	path      string
	input     bool
	schema    *jsonschema.Resolved
	editAgent string
}
type genericDefinition struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name  string `yaml:"name"`
		Title string `yaml:"title"`
	} `yaml:"metadata"`
	Spec struct {
		Documents   map[string]DocumentDefinition `yaml:"documents"`
		Agents      map[string]AgentDefinition    `yaml:"agents"`
		Seed        map[string]string             `yaml:"seed"`
		Triggers    []Trigger                     `yaml:"triggers"`
		People      []Person                      `yaml:"people"`
		Actions     map[string]ActionDefinition   `yaml:"actions"`
		ReviewRoles []string                      `yaml:"reviewRoles"`
		Flows       map[string]FlowDefinition     `yaml:"flows"`
	} `yaml:"spec"`
}

var definitionName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var errDefinition = errors.New("invalid or unsupported generic canvas definition")

func strictDefinitionNode(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!null" {
		return false
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
				return false
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if !strictDefinitionNode(child) {
			return false
		}
	}
	return true
}
func boundedLabel(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
func portableSource(value string) bool {
	if value == "" || len(value) > 512 || path.Clean(value) != value || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasPrefix(part, ".") || strings.Contains(part, "..") || len(part) > 255 {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}
func noSourceLinks(filename string) error {
	for current := filename; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errDefinition
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
func readDefinitionFile(filename string) ([]byte, error) {
	if noSourceLinks(filename) != nil {
		return nil, errDefinition
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errDefinition
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, errDefinition
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errDefinition
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 || !utf8.Valid(data) {
		return nil, errDefinition
	}
	return data, nil
}
func (d *CompiledDefinition) matchingDocument(candidate string) (compiledDocument, bool) {
	if !portableSource(candidate) {
		return compiledDocument{}, false
	}
	for _, doc := range d.documents {
		if matches, _ := path.Match(doc.path, candidate); matches {
			return doc, true
		}
	}
	return compiledDocument{}, false
}

func LoadGenericDefinition(filename string) (*CompiledDefinition, error) {
	return LoadGenericDefinitionWithResolver(filename, nil)
}

func LoadGenericDefinitionWithResolver(filename string, resolver *contract.ReleaseResolver) (*CompiledDefinition, error) {
	return loadGenericDefinition(filename, resolver, false)
}

// LoadManualDefinitionWithResolver refuses unsupported flow activation before
// loading dependencies, including any registry/cache resolution side effects.
func LoadManualDefinitionWithResolver(filename string, resolver *contract.ReleaseResolver) (*CompiledDefinition, error) {
	return loadGenericDefinition(filename, resolver, true)
}

func loadGenericDefinition(filename string, resolver *contract.ReleaseResolver, manualOnly bool) (*CompiledDefinition, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, errDefinition
	}
	data, err := readDefinitionFile(filename)
	if err != nil {
		return nil, errDefinition
	}
	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var extra any
	if dec.Decode(&node) != nil || !strictDefinitionNode(&node) || dec.Decode(&extra) != io.EOF {
		return nil, errDefinition
	}
	var config genericDefinition
	dec = yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if dec.Decode(&config) != nil || (config.APIVersion != "canvas.local/v1alpha2" && config.APIVersion != "canvas.local/v1alpha3") || config.Kind != "Canvas" || !definitionName.MatchString(config.Metadata.Name) || !boundedLabel(config.Metadata.Title) {
		return nil, errDefinition
	}
	if !integerFlowBudgets(&node) || config.APIVersion == "canvas.local/v1alpha2" && hasFlowSyntax(&node) {
		return nil, errDefinition
	}
	s := config.Spec
	if manualOnly && len(s.Flows) != 0 {
		return nil, ErrFlowRuntime
	}
	if len(s.Documents) == 0 || len(s.Documents) > 128 || len(s.Agents) == 0 || len(s.Agents) > 32 || len(s.Seed) > 128 || len(s.Triggers) > 128 || len(s.People) > 3 {
		return nil, errDefinition
	}
	d := &CompiledDefinition{APIVersion: config.APIVersion, ID: config.Metadata.Name, Title: config.Metadata.Title, Documents: s.Documents, Agents: s.Agents, Triggers: s.Triggers, People: s.People, Seed: map[string]string{}, documents: map[string]compiledDocument{}, proposals: map[string]map[string]bool{}}
	inputs := map[string]bool{}
	triggers := map[string]bool{}
	manual := false
	for _, doc := range s.Documents {
		if strings.Contains(doc.Path, "*") {
			inputs[doc.Path] = true
		}
	}
	for _, trigger := range s.Triggers {
		if !definitionName.MatchString(trigger.Name) || triggers[trigger.Name] {
			return nil, errDefinition
		}
		triggers[trigger.Name] = true
		if _, ok := s.Agents[trigger.Agent]; !ok {
			return nil, errDefinition
		}
		switch trigger.Type {
		case "manual":
			if trigger.Path != "" {
				return nil, errDefinition
			}
			manual = true
		case "file-added":
			found := false
			for _, doc := range s.Documents {
				if doc.Path == trigger.Path {
					found = true
					inputs[doc.Path] = true
				}
			}
			if !found {
				return nil, errDefinition
			}
		default:
			return nil, errDefinition
		}
	}
	if !manual && len(s.Flows) == 0 {
		return nil, errDefinition
	}
	root := filepath.Dir(filename)
	schemas := map[string][]byte{}
	compiledSchemas := map[string]*jsonschema.Resolved{}
	loadSchema := func(source string) (*jsonschema.Resolved, error) {
		if !portableSource(source) {
			return nil, errDefinition
		}
		if resolved, ok := compiledSchemas[source]; ok {
			return resolved, nil
		}
		raw, err := readDefinitionFile(filepath.Join(root, filepath.FromSlash(source)))
		if err != nil {
			return nil, errDefinition
		}
		resolved, err := compileDocumentSchema(raw)
		if err != nil {
			return nil, errDefinition
		}
		schemas[source], compiledSchemas[source] = raw, resolved
		return resolved, nil
	}
	for id, doc := range s.Documents {
		if !definitionName.MatchString(id) || doc.Title != "" && !boundedLabel(doc.Title) {
			return nil, errDefinition
		}
		d.Policy.Documents = append(d.Policy.Documents, doc.Path)
		if inputs[doc.Path] {
			d.Policy.Inputs = append(d.Policy.Inputs, doc.Path)
		}
		compiled := compiledDocument{path: doc.Path, input: inputs[doc.Path]}
		if doc.Schema != "" {
			resolved, err := loadSchema(doc.Schema)
			if err != nil {
				return nil, errDefinition
			}
			compiled.schema = resolved
		}
		d.documents[id] = compiled
	}
	slices.Sort(d.Policy.Documents)
	slices.Sort(d.Policy.Inputs)
	if d.Policy.Validate() != nil {
		return nil, errDefinition
	}
	if len(s.Actions) > 16 {
		return nil, errDefinition
	}
	d.Actions, d.actions = map[string]ActionDefinition{}, map[string]ActionDefinition{}
	for id, action := range s.Actions {
		doc, ok := d.documents[action.Document]
		if !definitionName.MatchString(id) || !boundedLabel(action.Name) || action.Kind != "simulated-announcement" || !ok || doc.input || strings.Contains(doc.path, "*") {
			return nil, errDefinition
		}
		d.Actions[id], d.actions[id] = action, action
	}
	for target, source := range s.Seed {
		doc, ok := d.matchingDocument(target)
		if !ok || !portableSource(source) {
			return nil, errDefinition
		}
		raw, err := readDefinitionFile(filepath.Join(root, filepath.FromSlash(source)))
		if err != nil || validateDocument(doc, string(raw)) != nil {
			return nil, errDefinition
		}
		d.Seed[target] = string(raw)
	}
	for _, doc := range d.documents {
		if !strings.Contains(doc.path, "*") {
			if _, ok := d.Seed[doc.path]; !ok {
				return nil, errDefinition
			}
		}
	}
	ids := make([]string, 0, len(s.Agents))
	for id := range s.Agents {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		agent := s.Agents[id]
		if !definitionName.MatchString(id) || !boundedLabel(agent.Name) || len(agent.Read) == 0 || len(agent.Propose) == 0 || len(agent.Read) > 128 || len(agent.Propose) > 128 {
			return nil, errDefinition
		}
		for _, scope := range [][]string{agent.Read, agent.Propose} {
			seen := map[string]bool{}
			for _, docID := range scope {
				if _, ok := d.documents[docID]; !ok || seen[docID] {
					return nil, errDefinition
				}
				seen[docID] = true
			}
		}
		d.proposals[id] = map[string]bool{}
		for _, docID := range agent.Propose {
			if d.documents[docID].input {
				return nil, errDefinition
			}
			d.proposals[id][docID] = true
		}
		ref := agent.Suite
		var b contract.Bundle
		if ref.Ref == "" {
			if !portableSource(ref.Directory) || !portableSource(ref.Artifact) || agent.Evaluation != (EvaluationReference{}) {
				return nil, errDefinition
			}
			ref.Directory = filepath.Join(root, filepath.FromSlash(ref.Directory))
			ref.Artifact = filepath.Join(root, filepath.FromSlash(ref.Artifact))
			b, err = contract.LoadSuite(id, ref)
		} else {
			if ref.Layout != "" {
				if !portableSource(ref.Layout) {
					return nil, errDefinition
				}
				ref.Layout = filepath.Join(root, filepath.FromSlash(ref.Layout))
			}
			b, err = contract.LoadReleasedSuite(id, ref, resolver)
			if err == nil && agent.Evaluation != (EvaluationReference{}) {
				evalPath := func(value string) (string, error) {
					if value == "" {
						return "", nil
					}
					if !portableSource(value) {
						return "", errDefinition
					}
					return filepath.Join(root, filepath.FromSlash(value)), nil
				}
				expectedPath, e := evalPath(agent.Evaluation.Expected)
				if e != nil {
					return nil, errDefinition
				}
				evidencePath, e := evalPath(agent.Evaluation.Evidence)
				if e != nil {
					return nil, errDefinition
				}
				expected, evidence, e := contract.EvaluationJSON(expectedPath, evidencePath)
				if e != nil {
					return nil, errDefinition
				}
				b, err = contract.WithReleasedEvaluation(b, expected, evidence)
			}
		}
		if err != nil {
			if errors.Is(err, contract.ErrReleaseProfile) {
				return nil, fmt.Errorf("%w: %w", errDefinition, err)
			}
			return nil, errDefinition
		}
		d.Bundles = append(d.Bundles, b)
	}
	loadFlowSchema := func(source string) (*jsonschema.Resolved, error) {
		resolved, err := loadSchema(source)
		var value any
		if err != nil || agentsuite.Unmarshal(schemas[source], &value) != nil || !preservesJSONNumbers(value) {
			return nil, errDefinition
		}
		return resolved, nil
	}
	if err := d.compileFlows(s.ReviewRoles, s.Flows, loadFlowSchema); err != nil {
		return nil, errDefinition
	}
	people := map[string]bool{}
	for _, person := range d.People {
		if !validRole(person.ID) || people[person.ID] || !boundedLabel(person.Name) || !boundedLabel(person.Title) {
			return nil, errDefinition
		}
		people[person.ID] = true
	}
	material := struct {
		Config  []byte
		Schemas map[string][]byte
		Seed    map[string]string
		Bundles []contract.Bundle
	}{data, schemas, d.Seed, d.Bundles}
	encoded, err := json.Marshal(material)
	if err != nil {
		return nil, errDefinition
	}
	sum := sha256.Sum256(encoded)
	d.Digest = fmt.Sprintf("%x", sum)
	return d, nil
}
func compileDocumentSchema(raw []byte) (*jsonschema.Resolved, error) {
	var value any
	if agentsuite.Unmarshal(raw, &value) != nil || !localSchemaRefs(value) {
		return nil, errDefinition
	}
	var schema jsonschema.Schema
	if json.Unmarshal(raw, &schema) != nil {
		return nil, errDefinition
	}
	// Never permit the schema resolver to read a file or contact a network.
	return schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true, Loader: func(*url.URL) (*jsonschema.Schema, error) { return nil, errDefinition }})
}
func localSchemaRefs(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "$ref" || key == "$dynamicRef" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return false
				}
			}
			if !localSchemaRefs(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !localSchemaRefs(child) {
				return false
			}
		}
	}
	return true
}
func validateDocument(doc compiledDocument, content string) error {
	if len(content) > 65536 || !utf8.ValidString(content) {
		return errors.New("document exceeds content limits")
	}
	if doc.schema != nil {
		var instance any
		// Public strict decoding uses json.Number, which jsonschema-go v0.4.3
		// classifies as a string. After strict/lossless validation, decode into
		// standard JSON values so numeric types reach schema validation intact.
		if agentsuite.Unmarshal([]byte(content), &instance) != nil || json.Unmarshal([]byte(content), &instance) != nil || doc.schema.Validate(instance) != nil {
			return errors.New("document does not satisfy schema")
		}
	}
	return nil
}
