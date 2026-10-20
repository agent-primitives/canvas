package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestReleaseReviewConfiguredStartupWithoutInference(t *testing.T) {
	root := fixtureRoot(t)
	key := filepath.Join(root, "hosted.key")
	if err := os.WriteFile(key, []byte("test-only-not-a-provider-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"profile": "hosted", "hosted": map[string]string{"endpoint": "https://example.invalid/v1", "model": "gpt-5.6-terra", "keyFile": key, "auth": "bearer"}})
	if err := os.WriteFile(filepath.Join(root, "model.config.local.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	definition, err := filepath.Abs(filepath.Join("..", "..", "examples", "release-review", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := roomAddressWriter{ready: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"--root", root, "--definition", definition, "--state", filepath.Join(t.TempDir(), "private-state"), "--listen", "127.0.0.1:0", "--model-profile", "hosted"}, output)
	}()
	var address string
	select {
	case address = <-output.ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timed out")
	}
	response, err := http.Get(address + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var state canvas.State
	err = json.NewDecoder(response.Body).Decode(&state)
	response.Body.Close()
	if err != nil || state.Definition == nil || state.Definition.ID != "release-review" || len(state.Runs) != 0 || len(state.Agents) != 2 || state.Agents[0].Model != "gpt-5.6-terra" {
		t.Fatalf("release startup state: %+v %v", state.Definition, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
	// The reserved .invalid provider cannot serve inference. Successful startup
	// with no runs demonstrates that neither activation nor shutdown generates.
}
