package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRoot shares authored inputs, never the developer's model configuration.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := copyDefinitionFixture(filepath.Join(root, "examples", "northwind-prd"), filepath.Join(repo, "examples", "northwind-prd")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "model.lock.json"), filepath.Join(root, "model.lock.json")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCanvasRejectsSymlinkStateWithoutChangingForeignTarget(t *testing.T) {
	root := fixtureRoot(t)
	for _, hostOnly := range []bool{false, true} {
		mode := "producer"
		if hostOnly {
			mode = "sessions-only"
		}
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			foreign := filepath.Join(base, "foreign")
			if err := os.Mkdir(foreign, 0751); err != nil {
				t.Fatal("cannot create foreign directory")
			}
			if err := os.Chmod(foreign, 0751); err != nil {
				t.Fatal("cannot set foreign directory mode")
			}
			link := filepath.Join(base, "state-link")
			if err := os.Symlink(foreign, link); err != nil {
				t.Fatal("cannot create symlink fixture")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			args := []string{"-root", root, "-state", link}
			if hostOnly {
				args = append(args, "-sessions-only")
			}
			serveErr := serve(ctx, args, io.Discard)
			if serveErr == nil || !strings.Contains(serveErr.Error(), "symlink") {
				t.Errorf("symlink state directory must be rejected with a clear error, got %v", serveErr)
			}
			entries, err := os.ReadDir(foreign)
			if err != nil || len(entries) != 0 {
				t.Error("rejected state path wrote to foreign target")
			}
			if info, err := os.Stat(foreign); err != nil || info.Mode().Perm() != 0751 {
				t.Error("rejected state path changed foreign target permissions")
			}
		})
	}
}

// A symlink above the state directory, such as /home -> var/home on Fedora
// Silverblue, must not stop the app.
func TestCanvasAcceptsSymlinkedStateAncestor(t *testing.T) {
	root := fixtureRoot(t)
	model, _ := startupModelEndpoint(t, root)
	for _, layout := range []string{"existing", "missing"} {
		t.Run(layout, func(t *testing.T) {
			base := t.TempDir()
			real := filepath.Join(base, "real")
			if err := os.Mkdir(real, 0755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, "link")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			rel := "state"
			if layout == "missing" {
				rel = filepath.Join("new", "state")
			} else if err := os.Mkdir(filepath.Join(real, rel), 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := serve(ctx, []string{"-root", root, "-state", filepath.Join(link, rel), "-model-base", model.URL, "-model-profile", "offline", "-sessions-only"}, io.Discard)
			if err != nil {
				t.Fatalf("symlinked ancestor refused: %v", err)
			}
			info, err := os.Lstat(filepath.Join(real, rel))
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Fatal("state directory not created privately under the resolved path")
			}
		})
	}
}

// CopyFS applies the process umask; suite identities include file modes.
func copyDefinitionFixture(dst, src string) error {
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		return os.Chmod(filepath.Join(dst, rel), info.Mode().Perm())
	})
}

type readinessWriter struct{ ready chan struct{} }

func (w readinessWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "Canvas:") {
		close(w.ready)
	}
	return len(p), nil
}

func TestCanvasReportsActualStartupErrors(t *testing.T) {
	root := fixtureRoot(t)
	err := serve(context.Background(), []string{"-root", root, "-state", t.TempDir(), "-model-profile", "offline", "-key-file", "k"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "hosted") {
		t.Fatalf("key file with offline profile must be a clear error, got %v", err)
	}
	err = serve(context.Background(), []string{"-no-such-flag"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no-such-flag") {
		t.Fatalf("flag error must name the flag, got %v", err)
	}
	err = serve(context.Background(), []string{"-listen", "0.0.0.0:1"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "0.0.0.0:1") {
		t.Fatalf("listener error must name the address, got %v", err)
	}
}

func TestCanvasLocalStartsWithoutGenerationAndStopsOnCancellation(t *testing.T) {
	root := fixtureRoot(t)
	model, calls := startupModelEndpoint(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := t.TempDir()
	done := make(chan error, 1)
	output := readinessWriter{ready: make(chan struct{})}
	go func() {
		done <- serve(ctx, []string{"-root", root, "-state", state, "-listen", "127.0.0.1:0", "-model-profile", "offline", "-model-base", model.URL}, output)
	}()
	select {
	case <-output.ready:
		if calls.Load() != 1 {
			t.Fatalf("want metadata only: %d", calls.Load())
		}
	case <-done:
		t.Fatal("local canvas failed before readiness")
	case <-time.After(10 * time.Second):
		t.Fatal("local canvas startup timed out")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("local canvas shutdown failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canvas did not shut down")
	}
}
