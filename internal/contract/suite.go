package contract

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/suite/core"
	"github.com/agent-primitives/suite/oci"
)

const maxArchive = 8 << 20

// SuiteRef names two local views of the same pinned suite. The caller resolves
// paths relative to its definition; Member is canonical, not the local alias.
type SuiteRef struct {
	Ref            string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Layout         string `yaml:"layout,omitempty" json:"layout,omitempty"`
	Platform       string `yaml:"platform,omitempty" json:"platform,omitempty"`
	RuntimeProfile string `yaml:"runtimeProfile,omitempty" json:"runtimeProfile,omitempty"`
	Directory      string `yaml:"directory" json:"directory"`
	Artifact       string `yaml:"artifact" json:"artifact"`
	Digest         string `yaml:"digest" json:"digest"`
	Member         string `yaml:"member" json:"member"`
}
type SuiteIdentity struct {
	Release       *ReleaseIdentity            `json:"release,omitempty"`
	Digest        string                      `json:"digest"`
	ContentDigest string                      `json:"contentDigest"`
	GraphDigest   string                      `json:"graphDigest"`
	Member        string                      `json:"member"`
	MemberDigest  string                      `json:"memberDigest"`
	Compositions  []core.CompositionSelection `json:"compositions"`
}

// Clone keeps presentation and historical metadata from sharing mutable selections.
func (s *SuiteIdentity) Clone() *SuiteIdentity {
	if s == nil {
		return nil
	}
	copy := *s
	if s.Release != nil {
		release := *s.Release
		if release.Evaluation.TestedExecution != nil {
			execution := *release.Evaluation.TestedExecution
			release.Evaluation.TestedExecution = &execution
		}
		copy.Release = &release
	}
	copy.Compositions = append([]core.CompositionSelection(nil), s.Compositions...)
	return &copy
}

type retainedFile struct {
	Data []byte `json:"data"`
	Mode uint32 `json:"mode"`
}
type executionArchive struct {
	Version       string                  `json:"apiVersion"`
	Kind          string                  `json:"kind"`
	ID            string                  `json:"id"`
	Model         string                  `json:"model"`
	AuthoredModel string                  `json:"authoredModel"`
	Pin           string                  `json:"pin"`
	Member        string                  `json:"member"`
	Directory     map[string]retainedFile `json:"directory"`
	Artifact      map[string]retainedFile `json:"artifact"`
}

var errSuite = errors.New("unsupported, unsafe or mismatched local suite")

func Profile(b Bundle) int {
	if b.Suite != nil {
		return 4
	}
	return 3
}
func Profiles(b Bundle) []int {
	if b.Suite != nil {
		return []int{4}
	}
	return []int{1, 2, 3}
}

func LoadSuite(id string, ref SuiteRef) (Bundle, error) {
	if ref.Ref != "" {
		return LoadReleasedSuite(id, ref, nil)
	}
	if ref.Layout != "" || ref.Platform != "" || ref.RuntimeProfile != "" {
		return Bundle{}, errSuite
	}
	a := executionArchive{Version: "canvas.local/execution-v1", Kind: "SuiteExecution", ID: id, Pin: ref.Digest, Member: ref.Member}
	var err error
	if a.Directory, err = capture(ref.Directory); err != nil {
		return Bundle{}, errSuite
	}
	if a.Artifact, err = capture(ref.Artifact); err != nil {
		return Bundle{}, errSuite
	}
	identity, agent, instructions, err := validateRetained(a)
	if err != nil {
		return Bundle{}, errSuite
	}
	a.Model = agent.Model.Model
	a.AuthoredModel = a.Model
	return archiveBundle(a, identity, instructions)
}

// Refuse symlinks in every ancestor, not just the final component. OpenRoot
// then keeps entry opens confined while bytes are copied into the archive.
func noLinkedAncestors(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", errSuite
	}
	for current := abs; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errSuite
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return abs, nil
}
func retainedPath(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
func capture(value string) (map[string]retainedFile, error) {
	abs, err := noLinkedAncestors(value)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, errSuite
	}
	defer root.Close()
	files := map[string]retainedFile{}
	total := 0
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errSuite
		}
		if name == "." {
			return nil
		}
		entries++
		if entries > 256 || !retainedPath(name) {
			return errSuite
		}
		info, err := root.Lstat(name)
		if err != nil {
			return errSuite
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errSuite
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 || len(files) >= 128 {
			return errSuite
		}
		f, err := root.Open(name)
		if err != nil {
			return errSuite
		}
		opened, err := f.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			f.Close()
			return errSuite
		}
		data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		closeErr := f.Close()
		total += len(data)
		if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() || len(data) > 1<<20 || total > 4<<20 {
			return errSuite
		}
		files[name] = retainedFile{Data: data, Mode: uint32(info.Mode().Perm() &^ 0022)}
		return nil
	})
	if err != nil {
		return nil, errSuite
	}
	return files, nil
}
func checkRetained(a executionArchive) error {
	count, total, expanded := 0, 0, int64(0)
	for _, files := range []map[string]retainedFile{a.Directory, a.Artifact} {
		if len(files) == 0 {
			return errSuite
		}
		for name, file := range files {
			count++
			total += len(file.Data)
			if !retainedPath(name) || file.Mode > 0777 || file.Mode&0022 != 0 || count > 128 || len(file.Data) > 1<<20 || total > 4<<20 {
				return errSuite
			}
			if len(file.Data) >= 2 && file.Data[0] == 0x1f && file.Data[1] == 0x8b {
				r, err := gzip.NewReader(bytes.NewReader(file.Data))
				if err != nil {
					return errSuite
				}
				n, err := io.Copy(io.Discard, io.LimitReader(r, (8<<20)-expanded+1))
				r.Close()
				expanded += n
				if err != nil || expanded > 8<<20 {
					return errSuite
				}
			}
		}
	}
	if _, ok := a.Directory["oci-layout"]; ok {
		return errSuite
	}
	return nil
}
func copiedView(root string, files map[string]retainedFile) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return errSuite
	}
	for name, file := range files {
		dest := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return errSuite
		}
		if err := os.WriteFile(dest, file.Data, fs.FileMode(file.Mode)); err != nil {
			return errSuite
		}
		if err := os.Chmod(dest, fs.FileMode(file.Mode)); err != nil {
			return errSuite
		}
	}
	return nil
}
func validateRetained(a executionArchive) (*SuiteIdentity, core.Agent, string, error) {
	var agent core.Agent
	if !namePattern.MatchString(a.ID) || !core.IsValidDigest(a.Pin) || checkRetained(a) != nil {
		return nil, agent, "", errSuite
	}
	temp, err := os.MkdirTemp("", "canvas-suite-")
	if err != nil {
		return nil, agent, "", errSuite
	}
	defer os.RemoveAll(temp)
	before, after := filepath.Join(temp, "content"), filepath.Join(temp, "layout")
	if copiedView(before, a.Directory) != nil || copiedView(after, a.Artifact) != nil {
		return nil, agent, "", errSuite
	}
	report, err := oci.ValidateDirectory(before)
	if err != nil {
		return nil, agent, "", errSuite
	}
	diff, err := oci.DiffPaths(before, after)
	if err != nil || diff.Before.SourceType != "directory" || diff.Before.ArtifactDigest != "" || diff.After.SourceType != "oci-layout" || diff.After.ArtifactDigest != a.Pin || diff.Before.ContentDigest != diff.After.ContentDigest {
		return nil, agent, "", errSuite
	}
	var suite core.Suite
	raw := a.Directory["agentsuite.json"].Data
	if core.Unmarshal(raw, &suite) != nil || len(suite.Agents) != 1 || len(suite.Extensions) != 0 || len(suite.Capabilities) != 0 || report.ToolProviders != 0 || report.ToolProviderCompositions != 0 {
		return nil, agent, "", errSuite
	}
	member := suite.Agents[0]
	if member.ID != a.Member || !core.MatchesCanonicalDescriptor(a.Directory[member.Path].Data, member.Descriptor) || core.Unmarshal(a.Directory[member.Path].Data, &agent) != nil {
		return nil, agent, "", errSuite
	}
	if agent.ID != member.ID || !modelOnlyAgent(agent) {
		return nil, agent, "", errSuite
	}
	instruction := a.Directory[agent.Instructions.Path].Data
	if agent.Instructions.Descriptor.MediaType != core.MediaTypeInstructions || !core.DescriptorSizeAndDigestMatch(instruction, agent.Instructions.Descriptor) || len(instruction) == 0 || len(instruction) > 65536 || !utf8.Valid(instruction) {
		return nil, agent, "", errSuite
	}
	graph, err := core.Digest(raw)
	if err != nil {
		return nil, agent, "", errSuite
	}
	selected := make([]core.CompositionSelection, 0, len(report.CompositionSelections))
	for _, s := range report.CompositionSelections {
		if s.Agent == member.ID {
			selected = append(selected, s)
		}
	}
	if len(selected) == 0 {
		return nil, agent, "", errSuite
	}
	return &SuiteIdentity{Digest: a.Pin, ContentDigest: diff.Before.ContentDigest, GraphDigest: graph, Member: member.ID, MemberDigest: member.Descriptor.Digest, Compositions: selected}, agent, string(instruction), nil
}
func modelOnlyAgent(agent core.Agent) bool {
	return agent.Model.Protocol == "openai-compatible" && boundedModel(agent.Model.Model) && len(agent.Model.SecretRefs) == 0 && agent.Model.EndpointEnv == "" && len(agent.ToolProviders) == 0 && len(agent.Invokes) == 0 && len(agent.Extensions) == 0
}
func boundedModel(model string) bool {
	return strings.TrimSpace(model) != "" && len(model) <= 160 && utf8.ValidString(model) && !strings.ContainsAny(model, "\r\n\x00")
}
func archiveBytes(a executionArchive) ([]byte, string, error) {
	data, err := json.Marshal(a)
	if err != nil || len(data) > maxArchive {
		return nil, "", errSuite
	}
	sum := sha256.Sum256(data)
	return data, fmt.Sprintf("%x", sum), nil
}
func archiveBundle(a executionArchive, identity *SuiteIdentity, instructions string) (Bundle, error) {
	data, digest, err := archiveBytes(a)
	if err != nil {
		return Bundle{}, err
	}
	b := Bundle{ID: a.ID, Digest: digest, Model: a.Model, Instructions: instructions, Source: data, Suite: identity}
	if a.Model != a.AuthoredModel {
		a.Model = a.AuthoredModel
		_, b.AuthoredDigest, err = archiveBytes(a)
	}
	return b, err
}
func isSuiteArchive(data []byte) bool {
	if len(data) > maxArchive || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return false
	}
	var head struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(data, &head) == nil && (head.Kind == "SuiteExecution" || head.Kind == "ReleasedSuiteExecution")
}
func parseArchive(data []byte) (Bundle, error) {
	if len(data) > maxArchive {
		return Bundle{}, errSuite
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(data, &header) == nil && header.Kind == "ReleasedSuiteExecution" {
		return parseReleasedArchive(data)
	}
	var a executionArchive
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&a) != nil || a.Version != "canvas.local/execution-v1" || a.Kind != "SuiteExecution" || !boundedModel(a.Model) {
		return Bundle{}, errSuite
	}
	canonical, _, err := archiveBytes(a)
	if err != nil || !bytes.Equal(canonical, data) {
		return Bundle{}, errSuite
	}
	identity, agent, instructions, err := validateRetained(a)
	if err != nil || a.AuthoredModel != agent.Model.Model {
		return Bundle{}, errSuite
	}
	return archiveBundle(a, identity, instructions)
}
func forSuiteModel(b Bundle, model string) (Bundle, error) {
	parsed, err := Parse(b.Source)
	if err != nil || parsed.ID != b.ID || parsed.Digest != b.Digest || parsed.Model != b.Model || parsed.Instructions != b.Instructions || parsed.AuthoredDigest != b.AuthoredDigest || !reflect.DeepEqual(parsed.Suite, b.Suite) || !boundedModel(model) {
		return Bundle{}, errSuite
	}
	if model == b.Model {
		return parsed, nil
	}
	if parsed.Suite.Release != nil {
		return Bundle{}, errSuite
	}
	var a executionArchive
	if json.Unmarshal(b.Source, &a) != nil {
		return Bundle{}, errSuite
	}
	a.Model = model
	return archiveBundle(a, parsed.Suite, parsed.Instructions)
}
