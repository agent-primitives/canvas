package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"go.yaml.in/yaml/v3"
)

var errFixtureDefinition = errors.New("fixture provider requires an unchanged generated template structure and schema; metadata and valid seed edits are allowed")
var errFixtureRequest = errors.New("fixture provider requires a matching documents request with readable current target evidence and the authored model")

// NewFixtureProvider supplies deterministic document proposals, not inference or
// evaluation evidence. The caller must load d with the strict offline loader.
func NewFixtureProvider(filename string, d *canvas.CompiledDefinition) (controller.ModelFunc, error) {
	if d == nil || d.ID == "" || d.Digest == "" || len(d.Actions) != 0 || len(d.People) != 0 {
		return nil, errFixtureDefinition
	}
	template := "single-review"
	if len(d.Agents) == 2 {
		template = "writer-review"
	}
	files := templateFiles(template, "fixture")
	var controls struct {
		Spec struct {
			Documents map[string]canvas.DocumentDefinition `yaml:"documents"`
			Agents    map[string]canvas.AgentDefinition    `yaml:"agents"`
			Triggers  []canvas.Trigger                     `yaml:"triggers"`
		} `yaml:"spec"`
	}
	if yaml.Unmarshal(files["canvas.yaml"], &controls) != nil || len(d.Documents) != len(controls.Spec.Documents) || len(d.Agents) != len(controls.Spec.Agents) || !reflect.DeepEqual(d.Triggers, controls.Spec.Triggers) || len(d.Bundles) != len(d.Agents) {
		return nil, errFixtureDefinition
	}
	for id, want := range controls.Spec.Documents {
		got, ok := d.Documents[id]
		got.Title = want.Title // Presentation labels are not execution controls.
		if !ok || got != want {
			return nil, errFixtureDefinition
		}
	}
	for id, want := range controls.Spec.Agents {
		got, ok := d.Agents[id]
		got.Name = want.Name
		if !ok || !reflect.DeepEqual(got, want) {
			return nil, errFixtureDefinition
		}
	}
	models := map[string]string{}
	for _, b := range d.Bundles {
		a, ok := d.Agents[b.ID]
		if !ok || models[b.ID] != "" || b.Model == "" || b.Suite == nil || b.Suite.Digest != a.Suite.Ref || b.Suite.Member != a.Suite.Member || b.Suite.Release == nil || b.Model != b.Suite.Release.AuthoredModel {
			return nil, errFixtureDefinition
		}
		models[b.ID] = b.Model
	}
	// Only the fixed schema needs rereading: compiled definitions deliberately do
	// not expose source schema bytes. Never resolve a registry or load bundles here.
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return nil, errFixtureDefinition
	}
	defer root.Close()
	info, err := root.Lstat("schemas/document.json")
	if err != nil || !info.Mode().IsRegular() {
		return nil, errFixtureDefinition
	}
	f, err := root.Open("schemas/document.json")
	if err != nil {
		return nil, errFixtureDefinition
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errFixtureDefinition
	}
	wantSchema := files["schemas/document.json"]
	raw, err := io.ReadAll(io.LimitReader(f, int64(len(wantSchema)+1)))
	if err != nil || !bytes.Equal(raw, wantSchema) {
		return nil, errFixtureDefinition
	}
	// Copy public scalar controls; Changes itself uses private immutable scopes
	// and schemas, so later presentation edits cannot redirect this provider.
	compiled, id, digest := *d, d.ID, d.Digest
	return func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		var snapshot struct {
			Mode       string `json:"mode"`
			Agent      string `json:"agent"`
			Definition struct {
				ID     string `json:"id"`
				Digest string `json:"digest"`
			} `json:"definition"`
			Evidence  map[string]string `json:"evidence"`
			Documents map[string]struct {
				Path    string `json:"path"`
				Read    bool   `json:"read"`
				Propose bool   `json:"propose"`
			} `json:"documents"`
		}
		if len(req.Messages) == 0 || json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Text()), &snapshot) != nil || snapshot.Mode != "documents" || snapshot.Definition.ID != id || snapshot.Definition.Digest != digest || models[snapshot.Agent] == "" || req.Model != models[snapshot.Agent] {
			return api.ModelResponse{}, errFixtureRequest
		}
		target := "draft"
		if snapshot.Agent == "reviewer" {
			target = "review"
		}
		doc, ok := snapshot.Documents[target]
		var current struct {
			Text string `json:"text"`
		}
		if !ok || doc.Path != target+".json" || !doc.Read || !doc.Propose || json.Unmarshal([]byte(snapshot.Evidence[doc.Path]), &current) != nil || current.Text == "" {
			return api.ModelResponse{}, errFixtureRequest
		}
		text := "Deterministic fixture proposal. Review this change before accepting."
		if current.Text == text {
			text = "Another deterministic fixture proposal. Review this change before accepting."
		}
		content, _ := json.Marshal(map[string]string{"text": text})
		output, _ := json.Marshal(map[string]any{"changes": []map[string]string{{"document": target, "operation": "replace", "content": string(content)}}})
		if _, err := compiled.Changes(snapshot.Agent, string(output)); err != nil {
			return api.ModelResponse{}, err
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", string(output))}, nil
	}, nil
}
