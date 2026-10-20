package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasedDefinitionRequiresDistinctExplicitState(t *testing.T) {
	for _, state := range []string{".state/copilot-northwind", ".state/../.state/copilot-northwind"} {
		if validateDefinitionState("released.yaml", state, true) == nil {
			t.Fatal("default demo state reused")
		}
	}
	if validateDefinitionState("released.yaml", ".state/released", false) == nil {
		t.Fatal("implicit state accepted")
	}
	if err := validateDefinitionState("released.yaml", ".state/released", true); err != nil {
		t.Fatal(err)
	}
	if err := validateDefinitionState("", ".state/copilot-northwind", false); err != nil {
		t.Fatal("legacy default changed", err)
	}
}

func TestOptInReleasedDefinitionUsesSameGuardedEngine(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	if err := os.CopyFS(fixture, os.DirFS(filepath.Join(root, "examples", "northwind-prd"))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(fixture, "canvas.yaml")
	raw, _ := os.ReadFile(filename)
	text := strings.Replace(string(raw), "name: northwind-prd", "name: released-command-test", 1)
	for _, id := range []string{"writer", "reviewer"} {
		text = strings.Replace(text, "        directory: suites/"+id+"\n        artifact: layouts/"+id+"\n        digest:", "        layout: layouts/"+id+"\n        platform: linux/amd64\n        ref:", 1)
	}
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "released-state")
	demo, err := startDefinition(root, state, "127.0.0.1:0", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer demo.Close()
	response, err := http.Get(demo.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var view struct {
		Definition struct {
			ID string `json:"id"`
		} `json:"definition"`
		Agents []struct {
			Suite struct {
				Release any `json:"release"`
			} `json:"suite"`
		} `json:"agents"`
	}
	if json.NewDecoder(response.Body).Decode(&view) != nil || view.Definition.ID != "released-command-test" || len(view.Agents) != 2 || view.Agents[0].Suite.Release == nil {
		t.Fatal("released command did not project selected suite")
	}
	for _, path := range []string{"/engine/api/scenarios", "/engine/api/resume", "/engine/api/role"} {
		r, err := http.Get(demo.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 404 && r.StatusCode != 405 {
			t.Fatalf("headless legacy route %s admitted: %d", path, r.StatusCode)
		}
	}
	assets, err := http.Get(demo.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	assets.Body.Close()
	if assets.StatusCode != 200 || len(assets.Cookies()) != 1 {
		t.Fatal("browser bootstrap/guard missing")
	}
}
