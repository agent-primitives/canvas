package authoring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/sessions/api"
)

func fixturePackage(t *testing.T, template string) (string, *canvas.CompiledDefinition) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "package")
	if _, err := Init(sourceCheckout(t), dest, template, "my-review"); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dest, "canvas.yaml")
	d, err := canvas.LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	return filename, d
}

func fixtureSnapshot(d *canvas.CompiledDefinition, agent, text string) map[string]any {
	target := "draft"
	if agent == "reviewer" {
		target = "review"
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	return map[string]any{
		"mode": "documents", "agent": agent,
		"definition": map[string]string{"id": d.ID, "digest": d.Digest},
		"evidence":   map[string]string{target + ".json": string(content)},
		"documents":  map[string]any{target: map[string]any{"path": target + ".json", "read": true, "propose": true}},
	}
}

func fixtureRequest(d *canvas.CompiledDefinition, agent string, snapshot map[string]any) api.ModelRequest {
	raw, _ := json.Marshal(snapshot)
	model := ""
	for _, b := range d.Bundles {
		if b.ID == agent {
			model = b.Model
		}
	}
	return api.ModelRequest{Model: model, Messages: []api.Message{*api.TextMessage("system", "fixture instructions"), *api.TextMessage("user", string(raw))}}
}

// Catches using startup seeds, echoing current evidence, or returning path IDs.
func TestFixtureCurrentEvidenceAndRepeatedChanges(t *testing.T) {
	for _, template := range []string{"single-review", "writer-review"} {
		t.Run(template, func(t *testing.T) {
			filename, _ := fixturePackage(t, template)
			raw, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.NewReplacer("my-review", "edited-name", "Your draft", "Edited title", "Writer — deterministic fixture", "My writer").Replace(string(raw)))
			if err := os.WriteFile(filename, raw, 0644); err != nil {
				t.Fatal(err)
			}
			seed, _ := json.Marshal(map[string]string{"text": strings.Repeat("界", 4096)})
			if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "seeds/draft.json"), seed, 0644); err != nil {
				t.Fatal(err)
			}
			d, err := canvas.LoadGenericDefinition(filename)
			if err != nil {
				t.Fatal(err)
			}
			provider, err := NewFixtureProvider(filename, d)
			if err != nil {
				t.Fatal(err)
			}
			for agent := range d.Agents {
				for _, initial := range []string{"accepted content, not startup seed", strings.Repeat("界", 4096)} {
					current := initial
					for range 4 {
						req := fixtureRequest(d, agent, fixtureSnapshot(d, agent, current))
						response, err := provider(context.Background(), req)
						if err != nil {
							t.Fatal(err)
						}
						changes, err := d.Changes(agent, response.Message.Text())
						target := "draft.json"
						if agent == "reviewer" {
							target = "review.json"
						}
						var doc struct{ Text string }
						if err != nil || len(changes) != 1 || json.Unmarshal([]byte(changes[target]), &doc) != nil || doc.Text == current || utf8.RuneCountInString(doc.Text) < 1 || utf8.RuneCountInString(doc.Text) > 4096 {
							t.Fatalf("invalid/non-changing fixture proposal: %v %v", changes, err)
						}
						current = doc.Text
					}
				}
			}
		})
	}
}

// Each mutation escapes a generated control while remaining a real compiled fixture.
func TestFixtureEligibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*canvas.CompiledDefinition)
	}{
		{"read scope", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Read = []string{"review"}
			d.Agents["writer"] = a
		}},
		{"propose scope", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Propose = []string{"review"}
			d.Agents["writer"] = a
		}},
		{"pin", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Suite.Ref = reviewerPin
			d.Agents["writer"] = a
		}},
		{"layout", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Suite.Layout = "layouts/other"
			d.Agents["writer"] = a
		}},
		{"member", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Suite.Member = "reviewer"
			d.Agents["writer"] = a
		}},
		{"platform", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Suite.Platform = "linux/arm64"
			d.Agents["writer"] = a
		}},
		{"evaluation", func(d *canvas.CompiledDefinition) {
			a := d.Agents["writer"]
			a.Evaluation.Expected = "expected.json"
			d.Agents["writer"] = a
		}},
		{"extra agent", func(d *canvas.CompiledDefinition) { d.Agents["other"] = d.Agents["writer"] }},
		{"document path", func(d *canvas.CompiledDefinition) {
			a := d.Documents["draft"]
			a.Path = "other.json"
			d.Documents["draft"] = a
		}},
		{"schema path", func(d *canvas.CompiledDefinition) {
			a := d.Documents["draft"]
			a.Schema = "other.json"
			d.Documents["draft"] = a
		}},
		{"extra document", func(d *canvas.CompiledDefinition) { d.Documents["other"] = d.Documents["draft"] }},
		{"event trigger", func(d *canvas.CompiledDefinition) {
			d.Triggers[0].Type = "file-added"
			d.Triggers[0].Path = "draft.json"
		}},
		{"missing trigger", func(d *canvas.CompiledDefinition) { d.Triggers = d.Triggers[1:] }},
		{"action", func(d *canvas.CompiledDefinition) {
			d.Actions["publish"] = canvas.ActionDefinition{Name: "Publish", Kind: "simulated-announcement", Document: "draft"}
		}},
		{"missing model", func(d *canvas.CompiledDefinition) { d.Bundles[0].Model = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename, d := fixturePackage(t, "writer-review")
			tc.edit(d)
			if p, err := NewFixtureProvider(filename, d); err == nil || p != nil {
				t.Fatal("unsupported control accepted")
			}
		})
	}
	if p, err := NewFixtureProvider("", nil); err == nil || p != nil {
		t.Fatal("nil definition accepted")
	}
}

func TestFixtureRejectsChangedSchema(t *testing.T) {
	filename, _ := fixturePackage(t, "single-review")
	path := filepath.Join(filepath.Dir(filename), "schemas/document.json")
	for _, schema := range []string{`{"type":"object"}`, string(templateFiles("single-review", "test")["schemas/document.json"]) + "\n"} {
		if err := os.WriteFile(path, []byte(schema), 0644); err != nil {
			t.Fatal(err)
		}
		d, err := canvas.LoadGenericDefinition(filename)
		if err != nil {
			t.Fatal(err)
		}
		if p, err := NewFixtureProvider(filename, d); err == nil || p != nil {
			t.Fatal("changed schema bytes accepted")
		}
	}
}

func TestFixtureInvalidRequests(t *testing.T) {
	filename, d := fixturePackage(t, "single-review")
	provider, err := NewFixtureProvider(filename, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mode", "agent", "definition", "digest", "target", "read", "propose", "path", "evidence", "malformed evidence", "model", "empty messages", "malformed snapshot"} {
		t.Run(name, func(t *testing.T) {
			s := fixtureSnapshot(d, "writer", "current")
			switch name {
			case "mode":
				s["mode"] = "question"
			case "agent":
				s["agent"] = "unknown"
			case "definition":
				s["definition"].(map[string]string)["id"] = "other"
			case "digest":
				s["definition"].(map[string]string)["digest"] = "other"
			case "target":
				s["documents"] = map[string]any{}
			case "read", "propose":
				s["documents"].(map[string]any)["draft"].(map[string]any)[name] = false
			case "path":
				s["documents"].(map[string]any)["draft"].(map[string]any)["path"] = "other.json"
			case "evidence":
				s["evidence"] = map[string]string{}
			case "malformed evidence":
				s["evidence"] = map[string]string{"draft.json": "not JSON"}
			}
			req := fixtureRequest(d, "writer", s)
			switch name {
			case "model":
				req.Model = "other"
			case "empty messages":
				req.Messages = nil
			case "malformed snapshot":
				req.Messages = []api.Message{*api.TextMessage("user", "not JSON")}
			}
			if response, err := provider(context.Background(), req); err == nil || response.Message.Text() != "" {
				t.Fatal("invalid request produced a proposal")
			}
		})
	}
}
