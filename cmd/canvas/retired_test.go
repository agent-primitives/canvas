package main

import (
	"context"
	"errors"
	"github.com/agent-primitives/canvas/internal/canvas"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredManagedStateRefusesBeforeResolution(t *testing.T) {
	state := t.TempDir()
	if os.MkdirAll(filepath.Join(state, "canvas"), 0700) != nil {
		t.Fatal("state")
	}
	if os.WriteFile(filepath.Join(state, "canvas", "retired.json"), []byte("even a damaged seal refuses"), 0600) != nil {
		t.Fatal("seal")
	}
	err := serve(context.Background(), []string{"--definition", "missing.yaml", "--state", state}, io.Discard)
	if !errors.Is(err, canvas.ErrRetired) {
		t.Fatalf("retired state reached definition/cache startup: %v", err)
	}
	for _, name := range []string{"suite-cache", "sessions", "space", "runtime-mode.json"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Fatal("refusal wrote", name, err)
		}
	}
}
