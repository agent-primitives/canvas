package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func producerRequest(t *testing.T, c *http.Client, base, method, path string, value, out any) int {
	t.Helper()
	var body io.Reader
	if value != nil {
		raw, _ := json.Marshal(value)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", base)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && resp.StatusCode < 300 {
		if err = json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode
}
func producerBrowser(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	producerRequest(t, c, base, "GET", "/", nil, nil)
	return c
}
func producerDraft(t *testing.T, c *http.Client, base string) canvas.Run {
	t.Helper()
	var run struct {
		Run string `json:"run"`
	}
	if status := producerRequest(t, c, base, "POST", "/api/run", map[string]string{"agent": "writer", "request": "Draft a grounded PRD.", "role": "GM"}, &run); status != 202 {
		t.Fatalf("run status%d", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var s canvas.State
		producerRequest(t, c, base, "GET", "/api/state", nil, &s)
		for _, r := range s.Runs {
			if r.ID == run.Run {
				if r.Status == "failed" {
					t.Fatalf("run failed: %+v", r)
				}
				if r.Proposal != "" {
					return r
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("proposal missing")
	return canvas.Run{}
}

func TestConfiguredHostedProducerUsesRealAdapter(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(root, "examples", "northwind-prd", "canvas.yaml")
	compiled, err := canvas.LoadGenericDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(compiled.Seed["prd.json"], "Fictional Northwind PRD", "Model-authored fictional pilot", 1)
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid provider body")
		}
		if req["max_completion_tokens"] != float64(4096) || req["model"] != "example-deployment" {
			t.Errorf("provider config changed: %+v", req)
		}
		if req["response_format"].(map[string]any)["type"] != "json_schema" || r.Header.Get("api-key") != "fixture-key" {
			t.Error("strict format/file credential missing")
		}
		text, _ := json.Marshal(map[string]any{"changes": []any{map[string]string{"document": "prd", "operation": "replace", "content": content}}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(text)}}}})
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	old := http.DefaultTransport
	transport := old.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old; transport.CloseIdleConnections() })
	configRoot := t.TempDir()
	key := filepath.Join(configRoot, "key")
	if err = os.WriteFile(key, []byte("fixture-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"profile": "hosted", "hosted": map[string]string{"endpoint": srv.URL + "/v1", "model": "example-deployment", "keyFile": key}})
	if err = os.WriteFile(filepath.Join(configRoot, "model.config.local.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := startWithOptions(configRoot, t.TempDir(), "127.0.0.1:0", definition, engineOptions{modelProfile: "hosted"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	browser := producerBrowser(t, d.URL)
	run := producerDraft(t, browser, d.URL)
	if run.Proposal == "" || run.Model != "example-deployment" || calls.Load() != 1 {
		t.Fatalf("actual proposal/dispatch missing: %+v calls%d", run, calls.Load())
	}
	var info map[string]any
	producerRequest(t, browser, d.URL, "GET", "/api/demo", nil, &info)
	if info["mode"] != "hosted-model" || info["providerInvocations"] != float64(1) || info["completionTokenCeiling"] != float64(4096) {
		t.Fatalf("misleading provider metadata: %+v", info)
	}
	if _, exists := info["realModelCalls"]; exists {
		t.Fatal("provider invocations must not be labelled an HTTP dispatch ledger")
	}
}

func TestConfiguredOptionsRefuseBeforeState(t *testing.T) {
	for _, options := range []engineOptions{
		{modelProfile: "unknown"}, {trustedEndpoint: "localhost:123"}, {trustedEndpoint: "127.0.0.1:0"},
		{trustedEndpoint: "127.0.0.1:123", modelProfile: "hosted"}, {remote: &remoteEngine{}, trustedEndpoint: "127.0.0.1:123"},
		{modelBase: "http://127.0.0.1:1"}, {keyFile: "private-file"},
	} {
		state := filepath.Join(t.TempDir(), "state")
		d, err := startWithOptions(t.TempDir(), state, "127.0.0.1:0", "", options)
		if !errors.Is(err, errEngineOptions) {
			if d != nil {
				d.Close()
			}
			t.Fatalf("options must fail before even loading a definition: %v", err)
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("unsafe options created state")
		}
	}
}
