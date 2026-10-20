package canvas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
)

func releaseDefinition(t *testing.T) *CompiledDefinition {
	t.Helper()
	d, err := LoadGenericDefinition(filepath.Join("..", "..", "examples", "release-review", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestReleaseReviewAcceptedSnapshot(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "release-review")
	load := func(member, doc string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, "evals", "recorded", member, doc))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	notes, announcement, review := load("writer", "notes.json"), load("writer", "announcement.md"), load("reviewer", "review.json")
	var evidence map[string]any
	c, _, _ := definedFixtureAt(t, filepath.Join(root, "canvas.yaml"), func(input map[string]any) string {
		changes := []map[string]string{{"document": "notes", "operation": "replace", "content": notes}, {"document": "announcement", "operation": "replace", "content": announcement}}
		if input["agent"] == "reviewer" {
			evidence, _ = input["evidence"].(map[string]any)
			changes = []map[string]string{{"document": "review", "operation": "replace", "content": review}}
		}
		raw, _ := json.Marshal(map[string]any{"changes": changes})
		return string(raw)
	})
	id, err := c.Manual("writer", "", "Draft release notes.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	writer := completed(t, c, id)
	if writer.Status != "proposed" {
		t.Fatalf("writer: %+v", writer)
	}
	if got, _ := c.space.Read("", "notes.json"); got == notes {
		t.Fatal("writer bypassed local reviewer")
	}
	if _, err := c.Review(writer.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	id, err = c.Manual("reviewer", "", "Review accepted documents.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	critique := completed(t, c, id)
	if critique.Status != "proposed" || evidence["notes.json"] != notes || evidence["announcement.md"] != announcement {
		t.Fatalf("reviewer lacks exact accepted documents: %+v", critique)
	}
	if critique.Evaluation != "not verified" {
		t.Fatal("local evals became suite evidence")
	}
}

func TestReleaseReviewPackage(t *testing.T) {
	d := releaseDefinition(t)
	for id, want := range map[string][]string{"writer": {"notes", "announcement"}, "reviewer": {"review"}} {
		a := d.Agents[id]
		if !reflect.DeepEqual(a.Propose, want) || !reflect.DeepEqual(a.Read, []string{"inputs", "notes", "review", "announcement"}) {
			t.Fatalf("%s scopes: %+v", id, a)
		}
		if a.Suite.Ref == "" || a.Suite.Platform != "linux/amd64" || a.Suite.Member != id {
			t.Fatalf("unpinned released suite: %+v", a.Suite)
		}
		for _, b := range d.Bundles {
			if b.ID != id {
				continue
			}
			if b.Model != "gpt-5.6-terra" || b.Suite == nil || b.Suite.Release == nil {
				t.Fatalf("suite identity: %+v", b)
			}
			if _, err := contract.ForModel(b, "qwen3:8b"); err == nil {
				t.Fatal("released model silently overridden")
			}
			for _, term := range []string{"sourceIds", "untrusted", "whole-document", "never approve", "announcement"} {
				if !strings.Contains(b.Instructions, term) {
					t.Fatalf("%s instruction missing %q", id, term)
				}
			}
		}
	}
	if d.Bundles[0].Instructions == d.Bundles[1].Instructions || len(d.Triggers) != 2 || d.Actions["announce"].Kind != "simulated-announcement" {
		t.Fatal("not separately instructed manual agents and receipt-only action")
	}
	if _, err := d.Changes("writer", changeEnvelope("notes", d.Seed["notes.json"])); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Changes("reviewer", changeEnvelope("review", d.Seed["review.json"])); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ agent, doc, content string }{
		{"writer", "inputs", d.Seed["inputs.json"]},
		{"writer", "review", d.Seed["review.json"]},
		{"reviewer", "notes", d.Seed["notes.json"]},
		{"reviewer", "announcement", "Not allowed"},
		{"writer", "announcement", strings.Repeat("x", 65537)},
	} {
		if _, err := d.Changes(tc.agent, changeEnvelope(tc.doc, tc.content)); err == nil {
			t.Fatalf("allowed invalid replacement %s/%s", tc.agent, tc.doc)
		}
	}
	for _, doc := range []string{"notes", "review"} {
		var seed map[string]any
		if err := json.Unmarshal([]byte(d.Seed[doc+".json"]), &seed); err != nil {
			t.Fatal(err)
		}
		seed["extra"] = true
		raw, _ := json.Marshal(seed)
		agent := "writer"
		if doc == "review" {
			agent = "reviewer"
		}
		if _, err := d.Changes(agent, changeEnvelope(doc, string(raw))); err == nil {
			t.Fatal("extra field allowed")
		}
		delete(seed, "extra")
		seed["status"] = strings.Repeat("x", 10000)
		raw, _ = json.Marshal(seed)
		if _, err := d.Changes(agent, changeEnvelope(doc, string(raw))); err == nil {
			t.Fatal("unbounded/unknown status allowed")
		}
	}
	if _, err := d.Changes("writer", changeEnvelope("announcement", strings.Repeat("x", 65536))); err != nil {
		t.Fatal("64KiB boundary rejected", err)
	}
}
