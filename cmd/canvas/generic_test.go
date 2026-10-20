package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelpin"
	"reflect"
	"slices"
)

type roomAddressWriter struct{ ready chan string }

func (w roomAddressWriter) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	for _, prefix := range []string{"Canvas: ", "Draft Room: "} {
		if strings.HasPrefix(text, prefix) {
			w.ready <- strings.TrimPrefix(text, prefix)
		}
	}
	return len(p), nil
}

func TestGenericRemoteRefusesBeforeStateOrProvider(t *testing.T) {
	for _, definition := range [][]string{nil, {"-definition", "/missing/definition.yaml"}, {"-definition", ""}} {
		state := filepath.Join(t.TempDir(), "not-created")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		args := append([]string{"-sessions-address", "example.invalid:443", "-state", state, "-model-profile", "hosted", "-key-file", "/missing/key"}, definition...)
		err := serve(ctx, args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "local-only") {
			t.Fatalf("generic remote must explicitly refuse: %v", err)
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("remote refusal touched state")
		}
	}
}
func TestEmptyDefinitionRefusesLocalWithoutEffects(t *testing.T) {
	state := filepath.Join(t.TempDir(), "absent")
	err := serve(context.Background(), []string{"-definition", "", "-state", state}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("blank definition accepted: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("blank refusal touched state")
	}
}

// A fallback to football, shared default state, or inference on reopen breaks this test.
func TestDefaultCanvasUsesNorthwindAndPreservesLegacyParent(t *testing.T) {
	root := fixtureRoot(t)
	model, calls := startupModelEndpoint(t, root)
	cwd := t.TempDir()
	t.Chdir(cwd)
	legacy := filepath.Join(cwd, ".state")
	writeStartupFixture(t, filepath.Join(legacy, "canvas", "attempts.json"), `{"attempts":[]}`)
	if err := os.Chmod(legacy, 0751); err != nil {
		t.Fatal(err)
	}
	before := startupTree(t, legacy)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		output := roomAddressWriter{ready: make(chan string, 1)}
		done := make(chan error, 1)
		go func() {
			done <- serve(ctx, []string{"-root", root, "-listen", "127.0.0.1:0", "-model-profile", "offline", "-model-base", model.URL}, output)
		}()
		var address string
		select {
		case address = <-output.ready:
		case err := <-done:
			cancel()
			t.Fatalf("default startup: %v", err)
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("startup timed out")
		}
		response, err := http.Get(address + "/api/state")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var state canvas.State
		err = json.NewDecoder(response.Body).Decode(&state)
		response.Body.Close()
		cancel()
		if stopErr := <-done; stopErr != nil {
			t.Fatal(stopErr)
		}
		if err != nil || state.Definition == nil || state.Definition.ID != "northwind-prd" || len(state.Prospects) != 0 || len(state.Runs) != 0 {
			t.Fatalf("default generic state: %+v %v", state.Definition, err)
		}
		if len(state.Agents) != 2 || state.Agents[0].Suite == nil || !slices.Contains(state.Documents, "announcement.md") {
			t.Fatalf("declared generic agents/documents missing: %+v %+v", state.Agents, state.Documents)
		}
	}
	after := startupTree(t, legacy)
	for name, entry := range before {
		if after[name] != entry {
			t.Errorf("default startup changed legacy entry %s", name)
		}
	}
	for name := range after {
		if _, existed := before[name]; !existed && name != "northwind" && !strings.HasPrefix(name, "northwind/") {
			t.Errorf("unexpected default state: %s", name)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("want two metadata-only probes, got %d", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(legacy, "northwind", "canvas", "attempts.json")); err != nil {
		t.Fatal(err)
	}
}

// Legacy producer and host-only state must refuse before cache, chmod, lock, or provider.
func TestLegacyStateRefusesWithoutEffects(t *testing.T) {
	for _, hostOnly := range []bool{false, true} {
		for _, evidence := range []string{"ledger", "workspace", "journal", "archive", "mixed-archives", "lock-directory", "lock-symlink"} {
			t.Run(fmt.Sprintf("%t/%s", hostOnly, evidence), func(t *testing.T) {
				root := fixtureRoot(t)
				model, calls := startupModelEndpoint(t, root)
				state := t.TempDir()
				switch evidence {
				case "ledger":
					writeStartupFixture(t, filepath.Join(state, "canvas", "attempts.json"), `{"attempts":[]}`)
				case "workspace":
					if err := os.MkdirAll(filepath.Join(state, "space", ".sharedspace", "git"), 0700); err != nil {
						t.Fatal(err)
					}
				case "journal":
					writeStartupFixture(t, filepath.Join(state, "sessions", "journal.db"), "synthetic old journal")
				case "archive":
					writeStartupFixture(t, filepath.Join(state, "sessions", "contracts", "old", "agent.yaml"), "invalid old archive")
				case "mixed-archives":
					d, err := canvas.LoadGenericDefinition(filepath.Join(root, "examples", "northwind-prd", "canvas.yaml"))
					if err != nil {
						t.Fatal(err)
					}
					b := d.Bundles[0]
					writeStartupFixture(t, filepath.Join(state, "sessions", "contracts", b.Digest, "agent.yaml"), string(b.Source))
					old, err := contract.Parse([]byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: old}\nspec:\n  instructions: old instruction\n  model: {name: synthetic-model}\n"))
					if err != nil {
						t.Fatal(err)
					}
					writeStartupFixture(t, filepath.Join(state, "sessions", "contracts", old.Digest, "agent.yaml"), string(old.Source))
				case "lock-directory":
					if err := os.Mkdir(filepath.Join(state, ".producer.lock"), 0700); err != nil {
						t.Fatal(err)
					}
				case "lock-symlink":
					target := filepath.Join(t.TempDir(), "lock")
					writeStartupFixture(t, target, "foreign lock")
					if err := os.Symlink(target, filepath.Join(state, ".producer.lock")); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Chmod(state, 0751); err != nil {
					t.Fatal(err)
				}
				writeStartupFixture(t, filepath.Join(state, "northwind", "canvas", "attempts.json"), "unrelated child")
				writeStartupFixture(t, filepath.Join(state, "northwind-host", "keep"), "unrelated host child")
				before := startupTree(t, state)
				args := []string{"-root", root, "-state", state, "-model-profile", "offline", "-model-base", model.URL}
				if hostOnly {
					args = append(args, "-sessions-only")
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				err := serve(ctx, args, io.Discard)
				if err == nil {
					t.Fatal("legacy state accepted")
				}
				if calls.Load() != 0 {
					t.Errorf("refusal contacted model: %d", calls.Load())
				}
				if !reflect.DeepEqual(before, startupTree(t, state)) {
					t.Fatal("refusal changed existing state")
				}
			})
		}
	}
}

func TestGenericExplicitLocalStartupWithoutInference(t *testing.T) {
	root := fixtureRoot(t)
	definition, err := filepath.Abs(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := modelpin.Load(filepath.Join(root, "model.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int32)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			calls.Add(1)
			http.Error(w, "generation is not permitted in startup fixture", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": lock.Model, "digest": lock.ManifestDigest}}})
	}))
	defer model.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := roomAddressWriter{ready: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"-root", root, "-definition", definition, "-state", t.TempDir(), "-listen", "127.0.0.1:0", "-model-profile", "offline", "-model-base", model.URL}, output)
	}()
	var address string
	select {
	case address = <-output.ready:
	case err := <-done:
		t.Fatalf("startup failed: %v", err)
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
	if err != nil || state.Definition == nil || state.Definition.ID != "northwind-prd" || len(state.Prospects) != 0 || len(state.Runs) != 0 {
		t.Fatalf("generic state: %+v %v", state.Definition, err)
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
	if calls.Load() != 0 {
		t.Fatal("startup or shutdown dispatched inference")
	}
}
