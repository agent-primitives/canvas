package sessionhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
)

func TestPinnedOllamaChecksManifestBeforeEveryCall(t *testing.T) {
	matching := true
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			calls++
			w.WriteHeader(500)
			return
		}
		if matching {
			w.Write([]byte(`{"models":[{"name":"test-model","digest":"abc"}]}`))
		} else {
			w.Write([]byte(`{"models":[]}`))
		}
	}))
	defer server.Close()
	lock := filepath.Join(t.TempDir(), "lock.json")
	if err := os.WriteFile(lock, []byte(`{"model":"test-model","manifestDigest":"sha256:abc"}`), 0600); err != nil {
		t.Fatal("cannot write lock fixture")
	}
	b := bundle(t, "Assess fictional evidence.", "test-model")
	provider, err := PinnedOllama(lock, server.URL, []contract.Bundle{b})
	if err != nil || provider == nil {
		t.Fatal("provider startup failed")
	}
	matching = false
	if _, err := provider(context.Background(), api.ModelRequest{}); err == nil {
		t.Fatal("mutated model manifest accepted")
	}
	if calls != 0 {
		t.Fatal("model called before manifest validation")
	}
}
