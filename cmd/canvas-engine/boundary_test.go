package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestProjectedSecretsAndActualPartialBootstrap(t *testing.T) {
	f := fixture(t, nil, true, false)
	projected := t.TempDir()
	version := filepath.Join(projected, "..2026")
	if err := os.Mkdir(version, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.crt", "tls.crt", "tls.key"} {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.config.SessionsCAFile), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(version, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("..data/"+name, filepath.Join(projected, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("..2026", filepath.Join(projected, "..data")); err != nil {
		t.Fatal(err)
	}
	f.config.SessionsCAFile = filepath.Join(projected, "ca.crt")
	f.config.TLSCertFile = filepath.Join(projected, "tls.crt")
	f.config.TLSKeyFile = filepath.Join(projected, "tls.key")
	d, err := canvas.LoadManualDefinitionWithResolver(f.config.DefinitionFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := hostingRecord{Version: 1, InstanceID: f.config.InstanceID, DefinitionDigest: d.Digest, SessionsEndpoint: f.config.SessionsEndpoint}
	release, err := acquireState(f.config.StateDirectory, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindHosting(f.config.StateDirectory, binding); err != nil {
		t.Fatal(err)
	}
	release()
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := healthProbe(e.readinessAddress); err != nil {
		t.Fatal(err)
	}
	hc := httpClient(f)
	if code := request(t, e, hc, "GET", "/", nil, nil); code != 200 {
		t.Fatal(code)
	}
	if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
		t.Fatal("partial bootstrap dispatched")
	}
}

func TestInvalidEngineCertificateRefusesBeforeState(t *testing.T) {
	for _, kind := range []string{"wrong-san", "expired", "future"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t, nil, true, false)
			root := t.TempDir()
			cert, _ := certificate(t, root, kind != "wrong-san")
			if kind != "wrong-san" {
				leaf, err := x509.ParseCertificate(cert.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				if kind == "expired" {
					leaf.NotBefore = time.Now().Add(-2 * time.Hour)
					leaf.NotAfter = time.Now().Add(-time.Hour)
				} else {
					leaf.NotBefore = time.Now().Add(time.Hour)
					leaf.NotAfter = time.Now().Add(2 * time.Hour)
				}
				key := cert.PrivateKey.(*ecdsa.PrivateKey)
				der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.config.TLSCertFile = filepath.Join(root, "tls.crt")
			f.config.TLSKeyFile = filepath.Join(root, "tls.key")
			if e, err := startEngine(t.Context(), f.config); err == nil {
				e.Close()
				t.Fatal("invalid engine certificate accepted")
			}
			if _, err := os.Stat(f.config.StateDirectory); !os.IsNotExist(err) {
				t.Fatal("invalid engine certificate touched state")
			}
		})
	}
}

func TestSocketCollisionsRefuseBeforeState(t *testing.T) {
	f := fixture(t, nil, true, false)
	for _, which := range []string{"API", "readiness"} {
		t.Run(which, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			cfg := f.config
			if which == "API" {
				cfg.Listen = l.Addr().String()
			} else {
				cfg.ReadinessAddress = l.Addr().String()
			}
			if e, err := startEngine(t.Context(), cfg); err == nil {
				e.Close()
				t.Fatal("port collision accepted")
			}
			if _, err := os.Stat(cfg.StateDirectory); !os.IsNotExist(err) {
				t.Fatal("port collision touched state")
			}
		})
	}
}
func TestRuntimeBindingMismatchDoesNotAlterHistory(t *testing.T) {
	f := fixture(t, nil, true, false)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	original, err := os.ReadFile(filepath.Join(f.config.StateDirectory, "hosting.json"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := os.ReadFile(filepath.Join(f.config.StateDirectory, "canvas", "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"instance", "endpoint", "definition", "unmarked-root"} {
		t.Run(kind, func(t *testing.T) {
			cfg := f.config
			switch kind {
			case "instance":
				cfg.InstanceID = "different-instance"
			case "endpoint":
				cfg.SessionsEndpoint = "other.default.svc.cluster.local:8080"
			case "definition":
				root := t.TempDir()
				if err := os.CopyFS(root, os.DirFS(filepath.Dir(cfg.DefinitionFile))); err != nil {
					t.Fatal(err)
				}
				cfg.DefinitionFile = filepath.Join(root, "canvas.yaml")
				raw, err := os.ReadFile(cfg.DefinitionFile)
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte("Northwind fictional PRD"), []byte("Another fictional PRD"), 1)
				if err := os.WriteFile(cfg.DefinitionFile, raw, 0644); err != nil {
					t.Fatal(err)
				}
			case "unmarked-root":
				cfg.StateDirectory = filepath.Join(t.TempDir(), "state")
				os.Mkdir(cfg.StateDirectory, 0700)
				os.WriteFile(filepath.Join(cfg.StateDirectory, "old-data"), []byte("old"), 0600)
			}
			if e, err := startEngine(t.Context(), cfg); err == nil {
				e.Close()
				t.Fatal("binding mismatch accepted")
			}
			after, _ := os.ReadFile(filepath.Join(f.config.StateDirectory, "hosting.json"))
			afterLedger, _ := os.ReadFile(filepath.Join(f.config.StateDirectory, "canvas", "attempts.json"))
			if !bytes.Equal(after, original) || !bytes.Equal(afterLedger, ledger) {
				t.Fatal("history altered")
			}
		})
	}
}

func TestHostedForgedHeadersCannotSupplyTrust(t *testing.T) {
	f := fixture(t, nil, true, false)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	hc := httpClient(f)
	request(t, e, hc, "GET", "/", nil, nil)
	u, _ := url.Parse(e.config.Origin)
	for _, kind := range []string{"host", "origin", "cookie", "cross-site"} {
		req, _ := http.NewRequest("POST", "https://"+e.apiAddress+"/engine/api/run", strings.NewReader(`{"agent":"writer","role":"GM"}`))
		req.Host = u.Host
		req.Header.Set("Origin", e.config.Origin)
		req.Header.Set("X-Forwarded-Host", u.Host)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Forwarded", "host="+u.Host+";proto=https")
		for _, cookie := range hc.Jar.Cookies(req.URL) {
			req.AddCookie(cookie)
		}
		transport := hc.Transport
		switch kind {
		case "host":
			req.Host = "evil.invalid"
		case "origin":
			req.Header.Set("Origin", "https://evil.invalid")
		case "cookie":
			req.Header.Del("Cookie")
		case "cross-site":
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		res, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatalf("%s trust bypass %d", kind, res.StatusCode)
		}
	}
	if f.calls.Load() != 0 || f.exec.Load() != 0 {
		t.Fatal("forged headers dispatched")
	}
	// A plain HTTP API connection cannot bootstrap or obtain a cookie.
	plain := &http.Client{Timeout: time.Second}
	res, err := plain.Get("http://" + e.apiAddress + "/")
	if err == nil {
		defer res.Body.Close()
		if res.StatusCode == 200 || len(res.Cookies()) != 0 {
			t.Fatal("API served plaintext")
		}
	}
	// TLS 1.1 is not a permitted downgrade.
	conn, err := tls.Dial("tcp", e.apiAddress, &tls.Config{RootCAs: f.roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
	if err == nil {
		conn.Close()
		t.Fatal("TLS downgrade accepted")
	}
}
func TestHealthProbeRequiresOwnedHTTPResponse(t *testing.T) {
	for _, kind := range []string{"wrong-body", "status", "redirect", "good"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/readyz" {
					t.Error("wrong readiness request")
				}
				switch kind {
				case "wrong-body":
					io.WriteString(w, "ready")
				case "status":
					w.WriteHeader(503)
					io.WriteString(w, readyBody)
				case "redirect":
					http.Redirect(w, r, "/elsewhere", http.StatusFound)
				case "good":
					io.WriteString(w, readyBody)
				}
			}))
			defer server.Close()
			err := healthProbe(strings.TrimPrefix(server.URL, "http://"))
			if (err == nil) != (kind == "good") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
	for _, args := range [][]string{nil, {"health-probe"}, {"health-probe", "localhost:8081"}, {"health-probe", "127.0.0.1:0"}, {"-model-profile", "hosted"}} {
		if err := run(args); err == nil {
			t.Fatalf("unsafe CLI accepted %v", args)
		}
	}
}

func TestShutdownCancelsActiveRunAndReleasesLockOnce(t *testing.T) {
	f := fixture(t, nil, true, false)
	f.blockModel.Store(true)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	hc := httpClient(f)
	request(t, e, hc, "GET", "/", nil, nil)
	if code := request(t, e, hc, "POST", "/engine/api/run", map[string]string{"agent": "writer", "role": "GM", "request": "Block until cancellation"}, nil); code != 202 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for f.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.calls.Load() == 0 {
		t.Fatal("model did not begin")
	}
	started := time.Now()
	e.Close()
	e.Close()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("shutdown unbounded: %v", elapsed)
	}
	e2, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatalf("lock retained after cleanup: %v", err)
	}
	defer e2.Close()
	if f.calls.Load() != 1 || f.resume.Load() != 0 {
		t.Fatal("shutdown/restart dispatched again")
	}
}

func TestInterruptedLedgerReopenDoesNotDispatch(t *testing.T) {
	f := fixture(t, nil, true, false)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	path := filepath.Join(f.config.StateDirectory, "canvas", "attempts.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	ledger["attempts"] = []any{map[string]any{"id": "interrupted", "agent": "writer", "status": "running", "mode": "documents"}}
	raw, _ = json.Marshal(ledger)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	e2, err := startEngine(context.Background(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	hc := httpClient(f)
	var state canvas.State
	request(t, e2, hc, "GET", "/engine/api/state", nil, &state)
	if len(state.Runs) != 1 || state.Runs[0].Status != "uncertain" {
		t.Fatalf("not uncertain: %+v", state.Runs)
	}
	if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
		t.Fatal("interrupted restart dispatched")
	}
}
