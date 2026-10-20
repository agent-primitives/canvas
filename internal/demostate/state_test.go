package demostate_test

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/agent-primitives/canvas/internal/demostate"
)

func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func snapshot(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// selectedGeneration writes only a historical reader fixture, not a runnable
// workspace or a replacement generation producer.
func selectedGeneration(t *testing.T, private string) string {
	t.Helper()
	generation := filepath.Join(private, "demo-generations", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	put(t, filepath.Join(generation, "space", "doc.md"), "synthetic accepted document")
	put(t, filepath.Join(generation, "canvas", "ledger.json"), "synthetic previous review")
	put(t, filepath.Join(generation, "archive.json"), `{"previous":".","space":"space","canvas":"canvas"}`)
	put(t, filepath.Join(private, "producer-current.json"), `{"generation":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	return generation
}

func TestActiveProducerRefusesWithoutChangingState(t *testing.T) {
	private := filepath.Join(t.TempDir(), ".state")
	put(t, filepath.Join(private, "space", "doc.md"), "synthetic prior document")
	release, err := demostate.Acquire(private)
	if err != nil {
		t.Fatal(err)
	}
	before := genericTree(t, private)
	if _, err := demostate.Acquire(private); !errors.Is(err, demostate.ErrActive) {
		t.Fatalf("second producer: %v", err)
	}
	if after := genericTree(t, private); !reflect.DeepEqual(before, after) {
		t.Fatal("refused producer changed state")
	}
	release()
	release() // shutdown cleanup may call release more than once
	next, err := demostate.Acquire(private)
	if err != nil {
		t.Fatal(err)
	}
	next()
}

// Keep the cross-process exclusion formerly exercised through reset-demo at
// the surviving producer lifecycle boundary.
func TestAcquireLockAcrossProcess(t *testing.T) {
	if private := os.Getenv("DEMOSTATE_TEST_ROOT"); private != "" {
		if release, err := demostate.Acquire(private); !errors.Is(err, demostate.ErrActive) {
			if release != nil {
				release()
			}
			t.Fatalf("child acquire did not refuse producer: %v", err)
		}
		return
	}
	private := filepath.Join(t.TempDir(), ".state")
	release, err := demostate.Acquire(private)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := genericTree(t, private)
	child := exec.Command(os.Args[0], "-test.run=^TestAcquireLockAcrossProcess$")
	child.Env = append(os.Environ(), "DEMOSTATE_TEST_ROOT="+private)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("process lock test failed: %v %s", err, output)
	}
	if !reflect.DeepEqual(before, genericTree(t, private)) {
		t.Fatal("refused child changed private state")
	}
}

func TestResolvePreservesHistoricalGenerationsReadOnly(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, ".state")
	put(t, filepath.Join(private, "space", "doc.md"), "synthetic old document")
	put(t, filepath.Join(private, "canvas", "ledger.json"), "synthetic previous review")
	put(t, filepath.Join(private, "canvas", "sessions", "store.sqlite"), "synthetic embedded journal")
	put(t, filepath.Join(private, "sessions", "store.sqlite"), "synthetic live journal")
	put(t, filepath.Join(private, "sessions", "logs", "run.log"), "synthetic live log")
	put(t, filepath.Join(root, "model-storage", "sentinel"), "synthetic model storage")
	before := genericTree(t, root)
	resolved, err := demostate.Resolve(private)
	if err != nil || resolved != private {
		t.Fatalf("legacy resolve: %q %v", resolved, err)
	}
	if !reflect.DeepEqual(before, genericTree(t, root)) {
		t.Fatal("legacy resolution changed state")
	}

	first := selectedGeneration(t, private)
	before = genericTree(t, root)
	resolved, err = demostate.Resolve(private)
	if err != nil || resolved != first {
		t.Fatalf("generation resolve: %q %v", resolved, err)
	}
	if !reflect.DeepEqual(before, genericTree(t, root)) {
		t.Fatal("generation resolution changed state")
	}
	firstBefore := snapshot(t, first)
	second := filepath.Join(private, "demo-generations", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	put(t, filepath.Join(second, "space", "doc.md"), "synthetic later document")
	put(t, filepath.Join(second, "canvas", "ledger.json"), "synthetic later review")
	put(t, filepath.Join(second, "archive.json"), `{"previous":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","space":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/space","canvas":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/canvas"}`)
	put(t, filepath.Join(private, "producer-current.json"), `{"generation":"demo-generations/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	// Readers must not secure or rewrite existing private data as a side effect.
	if err := os.Chmod(private, 0751); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(second, "archive.json"), 0640); err != nil {
		t.Fatal(err)
	}
	before = genericTree(t, root)
	resolved, err = demostate.Resolve(private)
	if err != nil || resolved != second {
		t.Fatalf("later generation resolve: %q %v", resolved, err)
	}
	if !reflect.DeepEqual(before, genericTree(t, root)) || !reflect.DeepEqual(firstBefore, snapshot(t, first)) {
		t.Fatal("resolution altered paths, metadata, modes, or archived bytes")
	}
}

func TestUnsafePathsRefused(t *testing.T) {
	for _, target := range []string{"private", "lock", "pointer", "generations", "space", "canvas"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			private := filepath.Join(root, ".state")
			outside := filepath.Join(root, "outside")
			put(t, filepath.Join(outside, "sentinel"), "synthetic outside")
			selectedGeneration(t, private)
			paths := map[string]string{"private": private, "lock": filepath.Join(private, ".producer.lock"), "pointer": filepath.Join(private, "producer-current.json"), "generations": filepath.Join(private, "demo-generations"), "space": filepath.Join(private, "space"), "canvas": filepath.Join(private, "canvas")}
			if target == "space" || target == "canvas" {
				// Without a pointer, the original producer is the selected tree.
				if err := os.Remove(filepath.Join(private, "producer-current.json")); err != nil {
					t.Fatal(err)
				}
			}
			// Test-only removal constructs hostile paths beneath t.TempDir.
			if err := os.RemoveAll(paths[target]); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, paths[target]); err != nil {
				t.Fatal(err)
			}
			before := genericTree(t, root)
			if target == "lock" {
				if release, err := demostate.Acquire(private); err == nil {
					release()
					t.Fatal("acquire accepted symlink")
				}
			} else if _, err := demostate.Resolve(private); err == nil {
				t.Fatal("resolve accepted symlink")
			}
			if !reflect.DeepEqual(before, genericTree(t, root)) {
				t.Fatal("refusal changed private or outside state")
			}
		})
	}
	for _, pointer := range []string{`{"generation":"../outside"}`, `{"generation":"/tmp/outside"}`, `{"generation":"demo-generations/../outside"}`, `{"generation":"demo-generations/missing"}`, `{"generation":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","extra":true}`, `null`, `{broken`, `{"generation":""}`, `{"generation":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {}`, `{"generation":"demo-generations/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` + strings.Repeat(" ", 4097)} {
		t.Run(pointer, func(t *testing.T) {
			private := filepath.Join(t.TempDir(), ".state")
			selectedGeneration(t, private)
			put(t, filepath.Join(private, "producer-current.json"), pointer)
			before := genericTree(t, private)
			if _, err := demostate.Resolve(private); err == nil {
				t.Fatal("resolve accepted invalid pointer")
			}
			if !reflect.DeepEqual(before, genericTree(t, private)) {
				t.Fatal("invalid pointer refusal changed state")
			}
		})
	}
}

func TestNonRegularAndSelectedSymlinkPathsRefused(t *testing.T) {
	for _, target := range []string{"pointer-directory", "pointer-fifo", "lock-fifo", "space-file", "selected-space", "selected-canvas", "selected-document", "selected-archive", "selected-generation"} {
		t.Run(target, func(t *testing.T) {
			private := filepath.Join(t.TempDir(), ".state")
			generation := selectedGeneration(t, private)
			var path string
			switch target {
			case "pointer-directory", "pointer-fifo":
				path = filepath.Join(private, "producer-current.json")
			case "lock-fifo":
				path = filepath.Join(private, ".producer.lock")
			case "space-file", "selected-space":
				path = filepath.Join(generation, "space")
			case "selected-canvas":
				path = filepath.Join(generation, "canvas")
			case "selected-document":
				path = filepath.Join(generation, "space", "doc.md")
			case "selected-archive":
				path = filepath.Join(generation, "archive.json")
			case "selected-generation":
				path = generation
			}
			// Test-only removal constructs hostile paths beneath t.TempDir.
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			switch target {
			case "pointer-directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "pointer-fifo", "lock-fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "space-file":
				put(t, path, "synthetic not a directory")
			default:
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			}
			before := genericTree(t, private)
			if target == "lock-fifo" {
				if release, err := demostate.Acquire(private); err == nil {
					release()
					t.Fatal("acquire accepted FIFO lock")
				}
			} else if _, err := demostate.Resolve(private); err == nil {
				t.Fatal("resolved unsafe path")
			}
			if !reflect.DeepEqual(before, genericTree(t, private)) {
				t.Fatal("unsafe path refusal changed state")
			}
		})
	}
}

func TestResolveRefusesInvalidArchiveReadOnly(t *testing.T) {
	for _, manifest := range []string{`null`, `{broken`, `{"previous":"../outside","space":"../outside/space","canvas":"../outside/canvas"}`, `{"previous":".","space":"wrong","canvas":"canvas"}`, `{"previous":".","space":"space","canvas":"wrong"}`, `{"previous":".","space":"space","canvas":"canvas","extra":true}`, `{"previous":".","space":"space","canvas":"canvas"} {}`, `{"previous":".","space":"space","canvas":"canvas"}` + strings.Repeat(" ", 4097)} {
		t.Run(manifest, func(t *testing.T) {
			private := filepath.Join(t.TempDir(), ".state")
			generation := selectedGeneration(t, private)
			put(t, filepath.Join(generation, "archive.json"), manifest)
			before := genericTree(t, private)
			if _, err := demostate.Resolve(private); err == nil {
				t.Fatal("invalid archive accepted")
			}
			if !reflect.DeepEqual(before, genericTree(t, private)) {
				t.Fatal("invalid archive refusal changed state")
			}
		})
	}
}

func TestResolveRefusesIncompleteSelectedGeneration(t *testing.T) {
	for _, missing := range []string{"space", "canvas", "archive.json"} {
		t.Run(missing, func(t *testing.T) {
			private := filepath.Join(t.TempDir(), ".state")
			generation := selectedGeneration(t, private)
			if err := os.RemoveAll(filepath.Join(generation, missing)); err != nil {
				t.Fatal(err)
			}
			before := genericTree(t, private)
			if _, err := demostate.Resolve(private); err == nil {
				t.Fatal("incomplete selected generation accepted")
			}
			if !reflect.DeepEqual(before, genericTree(t, private)) {
				t.Fatal("incomplete generation refusal changed state")
			}
		})
	}
}

func TestInterruptedStagingDoesNotChangeResolution(t *testing.T) {
	private := filepath.Join(t.TempDir(), ".state")
	generation := selectedGeneration(t, private)
	put(t, filepath.Join(private, "demo-generations", "incomplete", "space", "doc.md"), "synthetic partial document")
	put(t, filepath.Join(private, ".producer-pointer-interrupted"), "broken unpublished pointer")
	before := genericTree(t, private)
	resolved, err := demostate.Resolve(private)
	if err != nil || resolved != generation {
		t.Fatalf("staging was selected: %q %v", resolved, err)
	}
	if !reflect.DeepEqual(before, genericTree(t, private)) {
		t.Fatal("resolution deleted or modified staging")
	}
}

// A checkout reached through a linked directory must work; the state tree
// itself still refuses links (see TestUnsafePathsRefused).
func TestResolveThroughSymlinkedAncestor(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(link, "checkout", ".state")
	release, err := demostate.Acquire(private)
	if err != nil {
		t.Fatalf("acquire through linked ancestor: %v", err)
	}
	release()
	selectedGeneration(t, private)
	real, err = filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(real, "checkout", ".state", "demo-generations", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	before := genericTree(t, private)
	resolved, err := demostate.Resolve(private)
	if err != nil || resolved != want {
		t.Fatalf("resolve through linked ancestor: %q %v", resolved, err)
	}
	if !reflect.DeepEqual(before, genericTree(t, private)) {
		t.Fatal("resolution through ancestor changed state")
	}
}
