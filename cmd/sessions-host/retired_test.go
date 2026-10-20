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
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	state := t.TempDir()
	if os.MkdirAll(filepath.Join(state, "canvas"), 0700) != nil || os.WriteFile(filepath.Join(state, "canvas", "retired.json"), []byte("damaged seal"), 0600) != nil {
		t.Fatal("seal")
	}
	err := serve(context.Background(), []string{"--root", root, "--definition", "missing.yaml", "--state", state, "--listen", "127.0.0.1:0"}, io.Discard)
	if !errors.Is(err, canvas.ErrRetired) {
		t.Fatalf("retired state reached definition/cache startup: %v", err)
	}
	for _, name := range []string{"suite-cache", "sessions", "space"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Fatal("refusal wrote", name, err)
		}
	}
}
