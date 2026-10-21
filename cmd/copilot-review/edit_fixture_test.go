package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestFixtureScopedEdit(t *testing.T) {
	d, e := canvas.LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "flow.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	changes, e := fixtureEditChanges(d, "prd", map[string]string{"prd.json": d.Seed["prd.json"]})
	if e != nil || len(changes) != 1 || changes[0]["document"] != "prd" {
		t.Fatal("fixture did not produce one edit", changes, e)
	}
	var before, after map[string]any
	_ = json.Unmarshal([]byte(d.Seed["prd.json"]), &before)
	_ = json.Unmarshal([]byte(changes[0]["content"]), &after)
	if after["title"] == before["title"] || !strings.Contains(after["title"].(string), "fixture") {
		t.Fatal("fixture edit not explicit", after)
	}
	delete(before, "title")
	delete(after, "title")
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("fixture altered outside title")
	}
	if _, e := fixtureEditChanges(d, "announcement", map[string]string{}); e == nil {
		t.Fatal("unsupported fixture editor broadened")
	}
}
