package canvas

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericUnrelatedDocumentsAndChangeSetLimits(t *testing.T) {
	p := genericFixture(t)
	// A separate project shape, local agent alias, document IDs and paths. No
	// Northwind/football identifiers are consulted by the compiler or validator.
	var config strings.Builder
	config.WriteString("apiVersion: canvas.local/v1alpha2\nkind: Canvas\nmetadata: {name: laboratory, title: Laboratory journal}\nspec:\n  documents:\n")
	var ids []string
	for i := 0; i < 33; i++ {
		id := fmt.Sprintf("journal-%02d", i)
		ids = append(ids, id)
		fmt.Fprintf(&config, "    %s: {path: journals/page-%02d.txt}\n", id, i)
	}
	config.WriteString("  agents:\n    editor:\n      name: Journal editor\n      suite:\n        directory: suites/writer\n        artifact: layouts/writer\n        digest: sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375\n        member: writer\n      read: [journal-00]\n      propose: [" + strings.Join(ids, ", ") + "]\n  triggers:\n    - {name: edit-journal, type: manual, agent: editor}\n  seed:\n")
	for i := 0; i < 33; i++ {
		fmt.Fprintf(&config, "    journals/page-%02d.txt: seeds/announcement.md\n", i)
	}
	if err := os.WriteFile(p, []byte(config.String()), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Bundles[0].ID != "editor" || d.Bundles[0].Suite.Member != "writer" || d.ID != "laboratory" {
		t.Fatal("local/canonical identities conflated")
	}
	makeOutput := func(count, size int) string {
		changes := []map[string]string{}
		for i := 0; i < count; i++ {
			changes = append(changes, map[string]string{"document": ids[i], "operation": "replace", "content": strings.Repeat("x", size)})
		}
		data, _ := json.Marshal(map[string]any{"changes": changes})
		return string(data)
	}
	for _, tc := range []struct {
		count, size int
		valid       bool
	}{{1, 65536, true}, {1, 65537, false}, {4, 65536, true}, {5, 65536, false}, {32, 1, true}, {33, 1, false}} {
		t.Run(fmt.Sprintf("%dx%d", tc.count, tc.size), func(t *testing.T) {
			changes, err := d.Changes("editor", makeOutput(tc.count, tc.size))
			if tc.valid {
				if err != nil || len(changes) != tc.count {
					t.Fatal("valid boundary refused", err)
				}
			} else if err == nil || changes != nil {
				t.Fatal("invalid aggregate escaped atomically", err)
			}
		})
	}
	changes, err := d.Changes("editor", changeEnvelope("journal-00", "whole journal"))
	if err != nil || changes["journals/page-00.txt"] != "whole journal" {
		t.Fatal("non-JSON documents unsupported", err)
	}
	// File-backed schemas are compiled at load time, not read from mutable state
	// on each answer. Replacing an asset cannot relax an already opened compiler.
	original := genericFixture(t)
	sealed, err := LoadGenericDefinition(original)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(filepath.Dir(original), "schemas/prd.json"), []byte(`{}`), 0600)
	if _, err := sealed.Changes("writer", changeEnvelope("prd", `{}`)); err == nil {
		t.Fatal("mutable schema relaxed compiled authority")
	}
}
