package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestValidateDefinitionWithoutStartingRuntime(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// An occupied port and missing credentials must not affect offline validation.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "validation must not contact a provider", http.StatusInternalServerError)
	}))
	defer model.Close()
	for _, tc := range []struct {
		name, definition, wantError string
	}{
		{"valid", "examples/northwind-released/canvas.yaml", ""},
		{"invalid", "go.mod", "invalid or unsupported generic canvas definition"},
		{"missing file", "missing.yaml", "invalid or unsupported generic canvas definition"},
		{"no definition", "", "--validate requires --definition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "not-created")
			args := []string{"--validate", "--root", root, "--state", state, "--listen", listener.Addr().String(), "--model-profile", "hosted", "--key-file", "/missing/key", "--model-base", model.URL}
			if tc.definition != "" {
				args = append(args, "--definition", tc.definition)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var output bytes.Buffer
			err := serve(ctx, args, &output)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("want %q, got %v", tc.wantError, err)
				}
			} else if err != nil || !strings.Contains(output.String(), "not activated") {
				t.Fatalf("validation: %v; output: %s", err, &output)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("validation touched runtime state: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("validation made %d provider requests", calls.Load())
	}
}

func TestValidateReleasedDefinitionRequiresOfflineLayout(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join("..", "..", "examples", "northwind-released")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "canvas.yaml")
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	definition := strings.ReplaceAll(string(raw), "        layout: layouts/writer\n", "")
	if err := os.WriteFile(filename, []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "not-created")
	err = serve(context.Background(), []string{"--validate", "--definition", filename, "--state", state}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("released pin without layout must refuse offline: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("offline refusal touched runtime state/cache: %v", err)
	}
}

func TestAuthoringStarterCompilesManualOnly(t *testing.T) {
	filename := filepath.Join("..", "..", "examples", "northwind-released", "canvas-starter.yaml")
	d, err := canvas.LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Triggers) == 0 || len(d.Actions) != 0 {
		t.Fatal("starter must have manual triggers and no actions")
	}
	for _, trigger := range d.Triggers {
		if trigger.Type != "manual" {
			t.Fatalf("starter has automatic trigger: %+v", trigger)
		}
	}
}
