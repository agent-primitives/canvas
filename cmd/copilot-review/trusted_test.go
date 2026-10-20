package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
)

func TestTrustedLoopbackProducerUsesRemoteSessionsAndUnknownCounters(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	def, err := canvas.LoadGenericDefinition(filepath.Join(root, "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		content := strings.Replace(def.Seed["prd.json"], "Fictional Northwind PRD", "Remote harness proposal", 1)
		raw, _ := json.Marshal(map[string]any{"changes": []any{map[string]string{"document": "prd", "operation": "replace", "content": content}}})
		return api.ModelResponse{Message: *api.TextMessage("assistant", string(raw))}, nil
	}
	h, err := sessionhost.Start(t.TempDir(), def.Bundles, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	d, err := startWithOptions(root, t.TempDir(), "127.0.0.1:0", "", engineOptions{trustedEndpoint: h.Address()})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	browser := producerBrowser(t, d.URL)
	run := producerDraft(t, browser, d.URL)
	if calls.Load() != 1 {
		t.Fatalf("remote provider calls%d", calls.Load())
	}
	var info map[string]any
	producerRequest(t, browser, d.URL, "GET", "/api/demo", nil, &info)
	if info["mode"] != "trusted-loopback-sessions" || !strings.Contains(info["identity"].(string), "not exercised") || info["modelCalls"] != nil {
		t.Fatalf("misleading remote metadata: %+v", info)
	}
	if value, ok := info["realModelCalls"]; ok && value == float64(0) {
		t.Fatal("unknown remote model counter reported zero")
	}
	if status := producerRequest(t, browser, d.URL, "POST", "/api/verify", map[string]string{"run": run.ID}, nil); status != 409 {
		t.Fatalf("remote plaintext Diagnostics enabled: %d", status)
	}
}

func TestTrustedAndConfiguredRequireSeparateState(t *testing.T) {
	for _, o := range []engineOptions{{trustedEndpoint: "127.0.0.1:123"}, {modelProfile: "hosted"}} {
		if err := validateProducerState(o, ".state/copilot-northwind", true); err == nil {
			t.Fatal("default state accepted")
		}
		if err := validateProducerState(o, filepath.Join(t.TempDir(), "new"), false); err == nil {
			t.Fatal("implicit separate state accepted")
		}
		if err := validateProducerState(o, filepath.Join(t.TempDir(), "new"), true); err != nil {
			t.Fatal(err)
		}
	}
}
