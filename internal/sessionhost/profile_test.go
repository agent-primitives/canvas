package sessionhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/sessions/api"
	"golang.org/x/sys/unix"
)

const testHostedModel = "example-deployment"

func hostedProfile(endpoint, keyFile, auth string) modelconfig.Profile {
	return modelconfig.Profile{Name: "hosted", Model: testHostedModel, Hosted: modelconfig.Hosted{Endpoint: endpoint, Model: testHostedModel, KeyFile: keyFile, Auth: auth}}
}

func writeKey(t *testing.T, mode os.FileMode) string {
	t.Helper()
	key := filepath.Join(t.TempDir(), "hosted.key")
	if err := os.WriteFile(key, []byte("synthetic-file-key\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, mode); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestHostedEnvelopeUsesFileKeyOnly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ignored-environment-fixture")
	key := writeKey(t, 0600)
	for _, auth := range []string{"api-key", "bearer"} {
		t.Run(auth, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/openai/v1/chat/completions" {
					t.Error("incorrect path")
				}
				if auth == "api-key" && (r.Header.Get("api-key") != "synthetic-file-key" || r.Header.Get("Authorization") != "") {
					t.Error("incorrect api-key credential header")
				}
				if auth == "bearer" && (r.Header.Get("Authorization") != "Bearer synthetic-file-key" || r.Header.Get("api-key") != "") {
					t.Error("incorrect bearer credential header")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request")
				}
				if body["model"] != testHostedModel || body["max_completion_tokens"] != float64(700) {
					t.Error("incorrect journaled model or token budget")
				}
				if _, ok := body["temperature"]; ok {
					t.Error("hosted temperature must be omitted")
				}
				if _, ok := body["max_tokens"]; ok {
					t.Error("hosted max_tokens must be omitted")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"synthetic unit result"}}]}`))
			}))
			defer server.Close()
			// The https requirement is covered by the ConfiguredProvider test.
			p, err := compatible(endpoint{label: "hosted", base: server.URL + "/openai/v1", model: testHostedModel, keyFile: key, auth: auth, reasoning: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p(context.Background(), api.ModelRequest{Model: testHostedModel, Messages: []api.Message{*api.TextMessage("user", "synthetic input")}}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("expected exactly one hosted request")
			}
		})
	}
}

func TestHostedProfileRequiresKeyFileWithoutOllamaOrEnvironmentFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ignored-environment-fixture")
	b := bundle(t, "synthetic instructions", testHostedModel)
	missing := filepath.Join(t.TempDir(), "hosted.key")
	_, err := ConfiguredProvider(hostedProfile("https://example.invalid/openai/v1", missing, "api-key"), "nonexistent-lock", "http://127.0.0.1:1", []contract.Bundle{b})
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing key file must be a clear error, got %v", err)
	}
	provider, err := ConfiguredProvider(hostedProfile("https://example.invalid/openai/v1", writeKey(t, 0600), "api-key"), "nonexistent-lock", "http://127.0.0.1:1", []contract.Bundle{b})
	if err != nil {
		t.Fatal("hosted startup must not load a model lock or contact Ollama")
	}
	_, err = provider(context.Background(), api.ModelRequest{Model: modelconfig.OfflineModel})
	if !errors.Is(err, ErrModelProfileMismatch) || !strings.Contains(err.Error(), "regenerate") {
		t.Fatal("inherited offline model must fail visibly before hosted generation")
	}
	if _, err := ConfiguredProvider(hostedProfile("http://example.invalid/v1", writeKey(t, 0600), "api-key"), "", "", []contract.Bundle{b}); err == nil {
		t.Fatal("plain http hosted endpoint accepted")
	}
	other := hostedProfile("https://example.invalid/v1", writeKey(t, 0600), "api-key")
	other.Hosted.Model = "different"
	if _, err := ConfiguredProvider(other, "", "", []contract.Bundle{b}); !errors.Is(err, ErrModelProfileMismatch) {
		t.Fatal("configured model must match the profile model")
	}
}

func TestKeyFileMustBePrivateRegularFile(t *testing.T) {
	if _, err := readKeyFile("hosted", writeKey(t, 0600)); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0640, 0604, 0644} {
		_, err := readKeyFile("hosted", writeKey(t, mode))
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("mode %o accepted or unclear: %v", mode, err)
		}
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(writeKey(t, 0600), link); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyFile("hosted", link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked key file accepted: %v", err)
	}
	if _, err := readKeyFile("hosted", dir); err == nil {
		t.Fatal("directory accepted as key file")
	}
	fifo := filepath.Join(dir, "fifo.key")
	if err := unix.Mkfifo(fifo, 0600); err == nil {
		if _, err := readKeyFile("hosted", fifo); err == nil {
			t.Fatal("fifo accepted as key file")
		}
	}
	empty := filepath.Join(dir, "empty.key")
	if err := os.WriteFile(empty, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyFile("hosted", empty); err == nil {
		t.Fatal("empty key accepted")
	}
	big := filepath.Join(dir, "big.key")
	if err := os.WriteFile(big, []byte(strings.Repeat("k", maxKeySize+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyFile("hosted", big); err == nil {
		t.Fatal("oversized key accepted")
	}
	secret := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(secret, []byte("line-one-secret\nline-two-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyFile("hosted", secret); err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatal("multi-line key accepted or key contents disclosed")
	}
}
