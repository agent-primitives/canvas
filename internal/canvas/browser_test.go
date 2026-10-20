package canvas

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/agent-primitives/canvas/web"
)

// TestBrowserFixture is a test-only real Git/gRPC backend with a deterministic
// provider, never a production model bypass. Its stdout contains only its URL.
func TestBrowserFixture(t *testing.T) {
	if os.Getenv("CANVAS_BROWSER_FIXTURE") != "1" {
		t.Skip("opt-in browser test fixture")
	}
	var c *Canvas
	var definition *CompiledDefinition
	var attempts atomic.Int32
	c, definition, _ = definedFixtureAt(t, filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"), func(input map[string]any) string {
		// Keep the existing question/reviewer fallback. Only the writer needs
		// whole-scope replacements, with fresh valid bytes on every attempt.
		if input["mode"] == "question" || input["agent"] != "writer" {
			return ""
		}
		attempt := attempts.Add(1)
		prd := strings.Replace(definition.Seed["prd.json"], "Fictional Northwind PRD", fmt.Sprintf("Proposed fictional pilot PRD %d", attempt), 1)
		announcement := fmt.Sprintf("# Fictional announcement draft %d\n\n", attempt) + strings.Repeat("Fictional pilot evidence remains subject to human review. No announcement has been sent.\n\n", 80)
		raw, err := json.Marshal(map[string]any{"changes": []map[string]string{
			{"document": "prd", "operation": "replace", "content": prd},
			{"document": "announcement", "operation": "replace", "content": announcement},
		}})
		if err != nil {
			panic(err)
		}
		return string(raw)
	})
	c.Watch()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	handler := c.Handler(lis.Addr().String(), web.Assets)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Canvas-Provider", "deterministic-fixture")
		handler.ServeHTTP(w, r)
	})}
	defer srv.Close()
	go srv.Serve(lis)
	fmt.Println("Test-only deterministic provider fixture: http://" + lis.Addr().String())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	<-sig
}
