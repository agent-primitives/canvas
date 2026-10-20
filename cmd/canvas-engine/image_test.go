package main

import (
	"os"
	"strings"
	"testing"
)

func TestEngineImageIsSeparateNonrootRuntime(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	_, engine, ok := strings.Cut(source, " AS engine\n")
	if !ok {
		t.Fatal("missing standalone engine image target")
	}
	if next := strings.Index(engine, "\nFROM "); next >= 0 {
		engine = engine[:next]
	}
	for _, required := range []string{"git ca-certificates", "USER 65532:65532", "/out/canvas-engine /usr/local/bin/", "HOME=/data/home", "TMPDIR=/data/tmp", `ENTRYPOINT ["canvas-engine"]`} {
		if !strings.Contains(engine, required) {
			t.Fatalf("engine missing %q", required)
		}
	}
	for _, forbidden := range []string{"sessions-host", "model.config", "model.lock", "examples", "provider", "COPY .", "curl"} {
		if strings.Contains(engine, forbidden) {
			t.Fatalf("engine contains %q", forbidden)
		}
	}
	if !strings.Contains(source, "-o /out/canvas-engine ./cmd/canvas-engine") {
		t.Fatal("engine binary not built")
	}
}
