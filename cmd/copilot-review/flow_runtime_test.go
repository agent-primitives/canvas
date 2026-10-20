package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestFlowRuntimeRefusesBeforeReleasedCacheOrBootstrap(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	fixture := t.TempDir()
	if err := os.CopyFS(fixture, os.DirFS(filepath.Join(root, "examples", "northwind-released"))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(fixture, "canvas.yaml")
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	definition := strings.Replace(string(raw), "canvas.local/v1alpha2", "canvas.local/v1alpha3", 1)
	definition = strings.Replace(definition, "spec:\n", "spec:\n  reviewRoles: [reviewer]\n  flows:\n    draft:\n      inputs: {schema: schemas/prd.json}\n      limits: {agentRuns: 2}\n      steps:\n        - {id: draft, agent: writer, gate: {accept: [prd], role: reviewer}}\n", 1)
	if err := os.WriteFile(filename, []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "not-created")
	runtime, err := startDefinition(root, state, "127.0.0.1:0", filename)
	if runtime != nil {
		runtime.Close()
	}
	if !errors.Is(err, canvas.ErrFlowRuntime) {
		t.Fatalf("flow runtime must refuse with specific unsupported-runtime error: %v", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("flow refusal touched runtime state/cache", err)
	}
}
