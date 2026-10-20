package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
)

func nativeData(t *testing.T) *CompiledDefinition {
	t.Helper()
	d, err := LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range d.Bundles {
		d.Bundles[i], err = contract.ForModel(b, "test-model")
		if err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// Only the external model is scripted. Git, native contract binding, Sessions,
// project routing, publication, and reopen validation remain real.
func nativeProviderFixture(t *testing.T, d *CompiledDefinition, provider func(context.Context, api.ModelRequest) (api.ModelResponse, error)) (*Canvas, func()) {
	t.Helper()
	root := t.TempDir()
	space, err := sharedspace.OpenWithPolicy(filepath.Join(root, "space"), d.Seed, d.Policy)
	if err != nil {
		t.Fatal(err)
	}
	h, err := sessionhost.Start(filepath.Join(root, "host"), d.Bundles, provider)
	if err != nil {
		space.Close()
		t.Fatal(err)
	}
	cl, err := client.Dial(h.Address(), client.WithProject(d.ID))
	if err != nil {
		h.Close()
		space.Close()
		t.Fatal(err)
	}
	c, err := OpenDefined(filepath.Join(root, "canvas"), space, cl, h.Address(), d, contract.LocalBinder{})
	if err != nil {
		cl.Close()
		h.Close()
		space.Close()
		t.Fatal(err)
	}
	return c, func() { c.Close(); cl.Close(); h.Close(); space.Close() }
}

func nativeOutput(d *CompiledDefinition, input map[string]any) string {
	if input["mode"] == "question" {
		return `{"answer":"This explains the recorded fictional documents, not an evaluation result."}`
	}
	id, content := "prd", strings.Replace(d.Seed["prd.json"], "Fictional Northwind PRD", "Proposed fictional pilot PRD", 1)
	if input["agent"] == "reviewer" {
		id, content = "review", d.Seed["review.json"]
	}
	return changeEnvelope(id, content)
}

func nativeRequestInput(request api.ModelRequest) (map[string]any, error) {
	var input map[string]any
	if len(request.Messages) == 0 || json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Text()), &input) != nil {
		return nil, errors.New("typed input missing")
	}
	return input, nil
}
