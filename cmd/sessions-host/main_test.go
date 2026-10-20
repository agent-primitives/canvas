package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelpin"
)

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
	if strings.HasPrefix(string(p), "Sessions gRPC:") {
		close(w.ready)
	}
	return len(p), nil
}

func TestStandaloneHostUsesSeparateListenerWithoutGeneration(t *testing.T) {
	root := hostedRoot(t, `{"profile":"offline"}`)
	lock, err := modelpin.Load(filepath.Join(root, "model.lock.json"))
	if err != nil {
		t.Fatal("cannot load model fixture")
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Error("startup called generation endpoint")
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": lock.Model, "digest": lock.ManifestDigest}}})
	}))
	defer model.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := readinessWriter{ready: make(chan struct{})}
	state := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"-root", root, "-state", state, "-listen", "127.0.0.1:0", "-model-profile", "offline", "-model-base", model.URL}, output)
	}()
	select {
	case <-output.ready:
	case <-done:
		t.Fatal("host failed before readiness")
	case <-time.After(10 * time.Second):
		t.Fatal("host startup timed out")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("host shutdown failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("host did not shut down")
	}
}

// hostedRoot shares authored examples and the model lock, but isolates config
// so a developer's ignored local override cannot affect CLI tests.
func hostedRoot(t *testing.T, config string) string {
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
	if err := os.WriteFile(filepath.Join(root, "model.config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHostedConfigArchivesSelectedContractsWithoutOllama(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := hostedRoot(t, `{"profile":"hosted","hosted":{"endpoint":"https://example.invalid/openai/v1","model":"example-deployment"}}`)
	key := filepath.Join(t.TempDir(), "hosted.key")
	if err := os.WriteFile(key, []byte("synthetic-file-key"), 0600); err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("hosted startup contacted offline model endpoint")
		w.WriteHeader(500)
	}))
	defer model.Close()
	state := t.TempDir()
	args := []string{"-root", root, "-state", state, "-listen", "127.0.0.1:0", "-model-base", model.URL}
	// Without -key-file the default key file is used, and it does not exist.
	err := serve(context.Background(), args, readinessWriter{})
	defaultKey := filepath.Join(home, ".config", "canvas", "hosted.key")
	if err == nil || !strings.Contains(err.Error(), defaultKey) {
		t.Fatalf("missing default key file must be named in the error, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := readinessWriter{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, append(args, "-key-file", key), output)
	}()
	select {
	case <-output.ready:
	case err := <-done:
		t.Fatalf("hosted startup failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("hosted startup timed out")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(state, "sessions", "contracts", "*", "agent.yaml"))
	if err != nil || len(paths) != 2 {
		t.Fatal("execution contracts were not archived")
	}
	for _, path := range paths {
		b, err := contract.Load(path)
		if err != nil || b.Model != "example-deployment" || b.Suite == nil {
			t.Fatal("archive must contain the configured hosted model")
		}
	}
}

func TestStandaloneReportsMissingHostedSettings(t *testing.T) {
	root := hostedRoot(t, `{"profile":"hosted"}`)
	err := serve(context.Background(), []string{"-root", root, "-state", t.TempDir(), "-listen", "127.0.0.1:0"}, readinessWriter{})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("hosted without endpoint must be a clear error, got %v", err)
	}
}

func TestStandaloneReportsBusyListener(t *testing.T) {
	root := hostedRoot(t, `{"profile":"offline"}`)
	lock, err := modelpin.Load(filepath.Join(root, "model.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": lock.Model, "digest": lock.ManifestDigest}}})
	}))
	defer model.Close()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	err = serve(context.Background(), []string{"-root", root, "-state", t.TempDir(), "-listen", busy.Addr().String(), "-model-profile", "offline", "-model-base", model.URL}, readinessWriter{})
	if err == nil || !strings.Contains(err.Error(), busy.Addr().String()) || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("busy listener must be reported, got %v", err)
	}
}

func TestRetainedActorsRequireSubstrateBeforeModelProvisioning(t *testing.T) {
	err := serve(context.Background(), []string{"-retain-suspended-actors", "-runtime", "local"}, readinessWriter{})
	if err == nil || !strings.Contains(err.Error(), "retained actors require substrate") {
		t.Fatalf("invalid retention mode was not rejected at runtime boundary: %v", err)
	}
}

func TestGenericRuntimeRefusalsBeforeProvisioning(t *testing.T) {
	for _, definition := range [][]string{nil, {"-definition", "/missing/canvas.yaml"}, {"-definition", ""}} {
		for _, build := range []string{"false", "true"} {
			old := retainedActorsBuild
			retainedActorsBuild = build
			state := filepath.Join(t.TempDir(), "absent")
			err := serve(context.Background(), append([]string{"-runtime", "substrate", "-state", state}, definition...), readinessWriter{})
			retainedActorsBuild = old
			if err == nil || !strings.Contains(err.Error(), "local-only") {
				t.Fatalf("generic substrate not refused: %v", err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("refusal touched state")
			}
		}
	}
	for _, args := range [][]string{{"-runtime", "unknown"}, {"-runtime", "local", "-control-address", "api.invalid:443"}} {
		if err := serve(context.Background(), args, readinessWriter{}); err == nil || !strings.Contains(err.Error(), "runtime configuration") {
			t.Fatalf("invalid runtime: %v", err)
		}
	}
}

func TestCheckModelReturnsBeforeStateListenerOrProvider(t *testing.T) {
	for _, status := range []int{200, 401, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/tags" {
					t.Error("check-model performed generation")
				}
				w.WriteHeader(status)
				if status == 200 {
					w.Write([]byte(`{"models":[]}`))
				} else {
					w.Write([]byte("synthetic-secret-response"))
				}
			}))
			defer model.Close()
			// No definitions or lock file: provider construction must not be reached.
			root := t.TempDir()
			state := filepath.Join(root, "absent-state")
			busy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer busy.Close()
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = serve(ctx, []string{"-check-model", "-definition", "", "-runtime", "unknown", "-root", root, "-state", "", "-listen", "not-a-listener", "-model-profile", "offline", "-model-base", model.URL}, &output)
			if (err == nil) != (status == 200) || calls.Load() != 1 {
				t.Fatalf("unexpected probe result: %v calls=%d", err, calls.Load())
			}
			if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
				t.Fatal("check-model created state")
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("check-model created repository artifacts")
			}
			if status == 200 && (!strings.Contains(output.String(), "qwen3:8b") || !strings.Contains(output.String(), "not verified")) {
				t.Fatalf("missing metadata-only report: %q", output.String())
			}
			if strings.Contains(output.String(), "Sessions gRPC:") || strings.Contains(output.String(), "synthetic-secret-response") || (err != nil && strings.Contains(err.Error(), "synthetic-secret-response")) {
				t.Fatal("unsafe probe output")
			}
		})
	}
}

func TestCheckModelHostedKeyOverrideFailsWithoutRuntimeArtifacts(t *testing.T) {
	root := hostedRoot(t, `{"profile":"hosted","hosted":{"endpoint":"https://example.invalid/custom/v1","model":"custom-model","keyFile":"/unused.key","auth":"bearer"}}`)
	state := filepath.Join(root, "absent-state")
	key := filepath.Join(root, "missing-override.key")
	var output bytes.Buffer
	err := serve(context.Background(), []string{"-check-model", "-root", root, "-state", state, "-key-file", key}, &output)
	if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "/unused.key") {
		t.Fatalf("override not respected: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) || output.Len() != 0 {
		t.Fatal("failed probe entered runtime")
	}
}

func TestStandaloneHostRefusesPublicListener(t *testing.T) {
	root := t.TempDir()
	// No listening socket may be opened even if provisioning would otherwise fail.
	err := serve(context.Background(), []string{"-root", root, "-state", t.TempDir(), "-listen", "0.0.0.0:8766", "-model-base", "http://127.0.0.1:1"}, readinessWriter{})
	if err == nil || !strings.Contains(err.Error(), "listener must bind") {
		t.Fatal("unsafe listener was not refused before model provisioning")
	}
}
