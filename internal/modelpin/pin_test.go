package modelpin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineProvisioningDiagnosticNamesExplicitSetup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"models":[]}`)) }))
	err := Check(context.Background(), server.URL, "qwen3:8b", "sha256:expected")
	if err == nil || !strings.Contains(err.Error(), "make setup-offline") {
		t.Fatal("missing model must name explicit offline provisioning")
	}
	server.Close()
	err = Check(context.Background(), server.URL, "qwen3:8b", "sha256:expected")
	if err == nil || !strings.Contains(err.Error(), "make setup-offline") {
		t.Fatal("unavailable model must name explicit offline provisioning")
	}
}

func TestRefusesDifferentInstalledManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"qwen3:8b","digest":"different"}]}`))
	}))
	defer srv.Close()
	if err := Check(context.Background(), srv.URL, "qwen3:8b", "sha256:expected"); err == nil {
		t.Fatal("mutable model tag accepted")
	}
}

func TestMatchesModelAndDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"qwen3:8b","digest":"expected"}]}`))
	}))
	defer srv.Close()
	if err := Check(context.Background(), srv.URL, "qwen3:8b", "sha256:expected"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckHonorsContext(t *testing.T) {
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("check must not use a proxy")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"qwen3:8b","digest":"expected"}]}`))
	}))
	defer srv.Close()
	if err := Check(context.Background(), srv.URL, "qwen3:8b", "sha256:expected"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Check(ctx, srv.URL, "qwen3:8b", "sha256:expected"); err == nil {
		t.Fatal("cancelled check succeeded")
	}
}

func TestLoadParsesRepositoryLockStrictly(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "model.lock.json")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lock.json")
	for _, data := range []string{`{"model":"m","manifestDigest":"sha256:a","surprise":1}`, `{"model":"m","manifestDigest":"a"}`, `{"model":"m","manifestDigest":"sha256:a"} {}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted invalid lock %s", data)
		}
	}
}
