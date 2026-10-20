package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelpin"
)

func TestGenericSubstrateRefusesBeforeProviderOrControl(t *testing.T) {
	err := serve(context.Background(), []string{"-definition", "/missing/definition.yaml", "-runtime", "substrate", "-model-profile", "hosted", "-key-file", "/missing/key", "-control-address", "example.invalid:443"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "local-only") || !strings.Contains(err.Error(), "Substrate") {
		t.Fatalf("generic substrate must explicitly refuse before provisioning: %v", err)
	}
}

func TestGenericStandaloneLocalArchivesWithoutGeneration(t *testing.T) {
	root := hostedRoot(t, `{"profile":"hosted","hosted":{"endpoint":"https://example.invalid/openai/v1","model":"example-deployment"}}`)
	key := filepath.Join(t.TempDir(), "hosted.key")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	definition, err := filepath.Abs(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := readinessWriter{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"-root", root, "-definition", definition, "-state", state, "-listen", "127.0.0.1:0", "-runtime", "local", "-key-file", key}, output)
	}()
	select {
	case <-output.ready:
	case err := <-done:
		t.Fatalf("generic local host startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("generic local host startup timed out")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("generic local host shutdown timed out")
	}
	paths, err := filepath.Glob(filepath.Join(state, "sessions", "contracts", "*", "agent.yaml"))
	if err != nil || len(paths) != 2 {
		t.Fatal("native archive missing")
	}
	for _, filename := range paths {
		b, err := contract.Load(filename)
		if err != nil || b.Suite == nil || b.Model != "example-deployment" {
			t.Fatalf("selected native identity not archived: %v", err)
		}
	}
}

// A default host must archive native contracts in its own child, and reopen
// without changing any legacy sibling or dispatching inference.
func TestDefaultHostPreservesLegacyParentAndReopens(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "startup must not generate", 500)
	}))
	defer model.Close()
	root := hostedRoot(t, fmt.Sprintf(`{"profile":"hosted","hosted":{"endpoint":%q,"model":"example-deployment"}}`, model.URL))
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	legacy := ".state"
	writeHostFixture(t, filepath.Join(legacy, "canvas", "attempts.json"), `{"attempts":[]}`)
	if err := os.Chmod(legacy, 0751); err != nil {
		t.Fatal(err)
	}
	before := hostTree(t, legacy)
	state := filepath.Join(legacy, "northwind-host")
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cancel()
		if err := serve(ctx, []string{"--root", root, "--listen", "127.0.0.1:0", "--key-file", key}, io.Discard); err != nil {
			t.Fatal(err)
		}
		paths, err := filepath.Glob(filepath.Join(state, "sessions", "contracts", "*", "agent.yaml"))
		if err != nil || len(paths) != 2 {
			t.Fatalf("native archives: %v %v", paths, err)
		}
		for _, path := range paths {
			bundle, err := contract.Load(path)
			if err != nil || bundle.Suite == nil || bundle.Model != "example-deployment" {
				t.Fatalf("native archive: %+v %v", bundle.Suite, err)
			}
		}
	}
	after := hostTree(t, legacy)
	for name, entry := range before {
		if after[name] != entry {
			t.Errorf("changed legacy entry %s", name)
		}
	}
	for name := range after {
		if _, existed := before[name]; !existed && name != "northwind-host" && !strings.HasPrefix(name, "northwind-host/") {
			t.Errorf("unexpected state %s", name)
		}
	}
	for _, name := range []string{"producer-current.json", ".producer.lock", "canvas", "space"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Fatalf("host-only startup created %s", name)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("startup dispatched inference")
	}
}

func TestHostStateRefusalsAreReadOnly(t *testing.T) {
	root := hostedRoot(t, `{"profile":"offline"}`)
	lock, err := modelpin.Load(filepath.Join(root, "model.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": lock.Model, "digest": lock.ManifestDigest}}})
	}))
	defer model.Close()
	for _, kind := range []string{"ledger", "workspace", "journal", "legacy-archive", "mixed-archives", "symlink-root", "symlink-journal", "lock-directory", "blank-definition"} {
		t.Run(kind, func(t *testing.T) {
			calls.Store(0)
			state := t.TempDir()
			selected := state
			args := []string{"--root", root, "--state", selected, "--listen", "127.0.0.1:0", "--model-base", model.URL}
			switch kind {
			case "ledger":
				writeHostFixture(t, filepath.Join(state, "canvas", "attempts.json"), `{"attempts":[]}`)
			case "workspace":
				writeHostFixture(t, filepath.Join(state, "space", ".sharedspace", "git", "HEAD"), "legacy workspace")
			case "journal":
				writeHostFixture(t, filepath.Join(state, "sessions", "journal.db"), "ambiguous journal")
			case "legacy-archive", "mixed-archives":
				old, err := contract.Parse([]byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: old}\nspec:\n  instructions: old instruction\n  model: {name: synthetic-model}\n"))
				if err != nil {
					t.Fatal(err)
				}
				writeHostFixture(t, filepath.Join(state, "sessions", "contracts", old.Digest, "agent.yaml"), string(old.Source))
				if kind == "mixed-archives" {
					def, err := canvas.LoadGenericDefinition(filepath.Join(root, "examples", "northwind-prd", "canvas.yaml"))
					if err != nil {
						t.Fatal(err)
					}
					b := def.Bundles[0]
					writeHostFixture(t, filepath.Join(state, "sessions", "contracts", b.Digest, "agent.yaml"), string(b.Source))
				}
			case "symlink-root":
				selected = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(state, selected); err != nil {
					t.Fatal(err)
				}
				args[3] = selected
			case "symlink-journal":
				if err := os.Mkdir(filepath.Join(state, "sessions"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "foreign"), filepath.Join(state, "sessions", "journal.db")); err != nil {
					t.Fatal(err)
				}
			case "lock-directory":
				if err := os.Mkdir(filepath.Join(state, ".producer.lock"), 0700); err != nil {
					t.Fatal(err)
				}
			case "blank-definition":
				args = append(args, "--definition", "")
			}
			writeHostFixture(t, filepath.Join(state, "northwind", "keep"), "unrelated child")
			writeHostFixture(t, filepath.Join(state, "northwind-host", "keep"), "unrelated host child")
			if err := os.Chmod(state, 0751); err != nil {
				t.Fatal(err)
			}
			before := hostTree(t, state)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := serve(ctx, args, io.Discard)
			if err == nil {
				t.Fatal("unsafe/legacy state accepted")
			}
			if calls.Load() != 0 {
				t.Fatal("refusal contacted provider")
			}
			if !reflect.DeepEqual(before, hostTree(t, state)) {
				t.Fatal("refusal changed bytes, modes or entries")
			}
		})
	}
}

func TestHostAcceptsSymlinkedStateAncestor(t *testing.T) {
	root := hostedRoot(t, `{"profile":"hosted","hosted":{"endpoint":"https://example.invalid/v1","model":"example-deployment"}}`)
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, existing := range []bool{false, true} {
		parent := t.TempDir()
		link := filepath.Join(t.TempDir(), "ancestor")
		if err := os.Symlink(parent, link); err != nil {
			t.Fatal(err)
		}
		if existing {
			if err := os.Mkdir(filepath.Join(parent, "state"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := serve(ctx, []string{"--root", root, "--state", filepath.Join(link, "state"), "--listen", "127.0.0.1:0", "--key-file", key}, io.Discard); err != nil {
			t.Fatalf("ancestor refused: %v", err)
		}
		if _, err := os.Stat(filepath.Join(parent, "state", "sessions", "journal.db")); err != nil {
			t.Fatal(err)
		}
	}
}

func writeHostFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

type hostEntry struct {
	mode  fs.FileMode
	bytes string
}

func hostTree(t *testing.T, root string) map[string]hostEntry {
	t.Helper()
	tree := map[string]hostEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := hostEntry{mode: info.Mode()}
		if info.Mode()&os.ModeSymlink != 0 {
			value.bytes, err = os.Readlink(path)
		} else if info.Mode().IsRegular() {
			var data []byte
			data, err = os.ReadFile(path)
			value.bytes = string(data)
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		tree[rel] = value
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
