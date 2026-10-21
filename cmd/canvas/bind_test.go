package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestBindCLIProducesOnlyInertDraft(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	source := filepath.Join(t.TempDir(), "source")
	if err := serve(context.Background(), []string{"init", source, "--fixture", "--template", "writer-review", "--root", root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(source, "canvas.yaml")
	before, _ := os.ReadFile(filename)
	compiled, err := canvas.LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	suite := compiled.Agents["reviewer"].Suite
	output := filepath.Join(t.TempDir(), "bound")
	var stdout bytes.Buffer
	args := []string{"bind", filename, "--out", output, "--agent", "writer", "--ref", suite.Ref, "--layout", "layouts/reviewer", "--member", suite.Member, "--platform", suite.Platform, "--json"}
	if err := serve(context.Background(), args, &stdout); err != nil {
		t.Fatalf("bind refused: %v: %s", err, &stdout)
	}
	var result cliResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result.OK || result.Operation != "bind" || result.Status != "created" {
		t.Fatalf("bad bind result: %s (%v)", &stdout, err)
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("bind changed source definition")
	}
	bound, err := canvas.LoadGenericDefinition(filepath.Join(output, "canvas.yaml"))
	if err != nil || bound.Agents["writer"].Suite.Ref != suite.Ref {
		t.Fatalf("bound identity not validated: %v", err)
	}
	for _, path := range []string{".state", "canvas", "sessions", "space"} {
		if _, err := os.Stat(filepath.Join(output, path)); !os.IsNotExist(err) {
			t.Fatalf("bind created runtime state %s: %v", path, err)
		}
	}
	stdout.Reset()
	if err := serve(context.Background(), args, &stdout); commandExitCode(err) != 1 {
		t.Fatalf("existing destination not refused: %v", err)
	}
}

func TestBindCLIRequiresCompleteIdentity(t *testing.T) {
	var stdout bytes.Buffer
	err := serve(context.Background(), []string{"bind", "canvas.yaml", "--out", "unused", "--agent", "writer", "--json"}, &stdout)
	var result cliResult
	if commandExitCode(err) != 2 || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Status != "usage-error" || result.OK {
		t.Fatalf("incomplete bind not bounded usage refusal: %v %s", err, &stdout)
	}
}
