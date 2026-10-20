package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
)

func TestActorRefusesNativeArchiveAndProfile4BeforeBootstrap(t *testing.T) {
	b, err := contract.LoadSuite("writer", contract.SuiteRef{Directory: "../../examples/northwind-prd/suites/writer", Artifact: "../../examples/northwind-prd/layouts/writer", Digest: "sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375", Member: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, b.Source, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--bundle", path},
		{"--bundle", path, "--profile", "4"},
		{"--bundle", path, "--profile", "1", "--describe"},
		{"--bundle", portableAgentFixture(t), "--profile", "4"},
	} {
		args = append(args, "--listen", "invalid-listener", "--ready-listen", "invalid-listener")
		err := serve(context.Background(), args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "local-only") {
			t.Fatalf("actor did not refuse native before bootstrap: %v", err)
		}
	}
}
