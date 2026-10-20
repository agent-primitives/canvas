package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestHostingMarkerBindsTransportMode(t *testing.T) {
	f := fixture(t, nil, true, false)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	path := filepath.Join(f.config.StateDirectory, "hosting.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(original, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["authMode"] != "off" || fields["trustedNetwork"] != true {
		t.Errorf("marker lacks explicit mode binding: %s", original)
	}
	for _, kind := range []string{"auth-mode", "trusted-network", "missing-auth", "missing-trusted"} {
		t.Run(kind, func(t *testing.T) {
			var changed map[string]any
			if err := json.Unmarshal(original, &changed); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "auth-mode":
				changed["authMode"] = "required"
			case "trusted-network":
				changed["trustedNetwork"] = false
			case "missing-auth":
				delete(changed, "authMode")
			case "missing-trusted":
				delete(changed, "trustedNetwork")
			}
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if reopened, err := startEngine(t.Context(), f.config); err == nil {
				reopened.Close()
				t.Error("incompatible marker reopened")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatal("marker refusal modified history", err)
			}
		})
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal("matching mode cannot reopen", err)
	}
	reopened.Close()
	if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
		t.Fatal("marker checks dispatched work")
	}
}

func TestHostedRejectsRawPathBeforeRouting(t *testing.T) {
	f := fixture(t, nil, true, false)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	hc := httpClient(f)
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if code := request(t, e, hc, "GET", "/", nil, nil); code != 200 {
		t.Fatal(code)
	}
	for _, item := range []struct{ method, path string }{
		{"GET", "/engine%2Fapi%2Fstate"}, {"POST", "/engine%2Fapi%2Frun"},
		{"GET", "/%61pi/demo"}, {"GET", "/%65ngine/api/state"}, {"POST", "/api/%61nnouncement/request"},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			if code := request(t, e, hc, item.method, item.path, map[string]string{}, nil); code != 404 {
				t.Fatalf("encoded path routed/redirected: %d", code)
			}
		})
	}
	if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
		t.Fatal("encoded request dispatched work")
	}
}
