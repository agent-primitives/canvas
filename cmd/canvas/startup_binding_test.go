package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelpin"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// Moving either binding check after provider/host startup must fail this test:
// refusal must preserve the existing tree and never contact the model endpoint.
func TestGenericBindingRefusalPrecedesRuntimeStartup(t *testing.T) {
	for _, layout := range []string{"original", "generation"} {
		for _, mismatch := range []string{"definition", "policy", "missing-policy", "oversize-policy"} {
			t.Run(layout+"/"+mismatch, func(t *testing.T) {
				root := fixtureRoot(t)
				definitionDir := filepath.Join(t.TempDir(), "definition")
				source, err := filepath.Abs(filepath.Join("..", "..", "examples", "northwind-prd"))
				if err != nil {
					t.Fatal(err)
				}
				if err := copyDefinitionFixture(definitionDir, source); err != nil {
					t.Fatal(err)
				}
				definitionPath := filepath.Join(definitionDir, "canvas.yaml")
				d, err := canvas.LoadGenericDefinition(definitionPath)
				if err != nil {
					t.Fatal(err)
				}
				state := t.TempDir()
				producer := state
				if layout == "generation" {
					generation := "demo-generations/0123456789abcdef0123456789abcdef"
					producer = filepath.Join(state, filepath.FromSlash(generation))
					if err := os.MkdirAll(producer, 0700); err != nil {
						t.Fatal(err)
					}
					writeStartupFixture(t, filepath.Join(producer, "archive.json"), `{"previous":".","space":"space","canvas":"canvas"}`)
					writeStartupFixture(t, filepath.Join(state, "producer-current.json"), `{"generation":"`+generation+`"}`)
				}
				space, err := sharedspace.OpenWithPolicy(filepath.Join(producer, "space"), d.Seed, d.Policy)
				if err != nil {
					t.Fatal(err)
				}
				c, err := canvas.OpenDefined(filepath.Join(producer, "canvas"), space, nil, "", d, contract.LocalBinder{})
				if err != nil {
					space.Close()
					t.Fatal(err)
				}
				c.Close()
				if err := space.Close(); err != nil {
					t.Fatal(err)
				}
				policyPath := filepath.Join(producer, "space", ".sharedspace", "policy.json")
				wantError := ""
				switch mismatch {
				case "definition":
					raw, err := os.ReadFile(definitionPath)
					if err != nil {
						t.Fatal(err)
					}
					writeStartupFixture(t, definitionPath, strings.Replace(string(raw), "Northwind fictional PRD", "Changed fictional PRD", 1))
					// Opening SharedSpace would project the accepted document over
					// these bytes, so definition refusal must precede that open too.
					writeStartupFixture(t, filepath.Join(producer, "space", "announcement.md"), "Uncommitted operator draft must survive refusal.\n")
					wantError = "canvas definition changed"
				case "policy":
					policy := d.Policy
					policy.Documents = append(append([]string{}, policy.Documents...), "other.md")
					raw, err := json.Marshal(policy)
					if err != nil {
						t.Fatal(err)
					}
					writeStartupFixture(t, policyPath, string(raw))
					wantError = "workspace policy changed"
				case "missing-policy":
					if err := os.Remove(policyPath); err != nil {
						t.Fatal(err)
					}
					wantError = "existing workspace has no declared policy"
				case "oversize-policy":
					writeStartupFixture(t, policyPath, strings.Repeat(" ", (512<<10)+1))
					wantError = "workspace policy is not a bounded regular file"
				}
				model, calls := startupModelEndpoint(t, root)
				if err := os.Chmod(state, 0751); err != nil {
					t.Fatal(err)
				}
				before := startupTree(t, state)
				err = serve(context.Background(), []string{"-root", root, "-definition", definitionPath, "-state", state, "-listen", "127.0.0.1:0", "-model-profile", "offline", "-model-base", model.URL}, io.Discard)
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("want binding refusal %q, got %v", wantError, err)
				}
				if calls.Load() != 0 {
					t.Errorf("binding refusal configured/contacted the model provider: %d requests", calls.Load())
				}
				if after := startupTree(t, state); !reflect.DeepEqual(before, after) {
					for name, entry := range after {
						if prior, ok := before[name]; !ok || prior != entry {
							t.Errorf("refused startup created or changed %s", name)
						}
					}
					for name := range before {
						if _, ok := after[name]; !ok {
							t.Errorf("refused startup removed %s", name)
						}
					}
				}
			})
		}
	}
}

func TestFreshGenericProviderRefusalDoesNotInitializeWorkspace(t *testing.T) {
	root := fixtureRoot(t)
	definition, err := filepath.Abs(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "model.lock.json")); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	err = serve(context.Background(), []string{"-root", root, "-definition", definition, "-state", state, "-listen", "127.0.0.1:0", "-model-profile", "offline"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "model lock") {
		t.Fatalf("expected missing model lock, got %v", err)
	}
	for _, name := range []string{"space", "canvas", "sessions"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Errorf("refused fresh start created %s: %v", name, err)
		}
	}
}

func TestGenericSessionsOnlyDoesNotBindProducerState(t *testing.T) {
	root := fixtureRoot(t)
	definition, err := filepath.Abs(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	model, calls := startupModelEndpoint(t, root)
	state := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx, []string{"-root", root, "-definition", definition, "-state", state, "-sessions-only", "-model-profile", "offline", "-model-base", model.URL}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("host-only startup did not perform its normal model pin probe: %d requests", calls.Load())
	}
	for _, name := range []string{".producer.lock", "canvas", "space"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Errorf("host-only startup touched producer state %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "sessions", "journal.db")); err != nil {
		t.Fatalf("host-only startup did not initialize its journal: %v", err)
	}
}

func startupModelEndpoint(t *testing.T, root string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	lock, err := modelpin.Load(filepath.Join(root, "model.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/tags" {
			http.Error(w, "inference is not permitted", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": lock.Model, "digest": lock.ManifestDigest}}})
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func writeStartupFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

type startupEntry struct {
	mode   fs.FileMode
	digest [sha256.Size]byte
}

func startupTree(t *testing.T, root string) map[string]startupEntry {
	t.Helper()
	tree := map[string]startupEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := startupEntry{mode: info.Mode()}
		if !entry.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.digest = sha256.Sum256(raw)
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		tree[name] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
