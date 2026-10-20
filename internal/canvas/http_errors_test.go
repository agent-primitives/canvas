package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// brokenHead stands in for a shared space whose Git storage cannot be read.
type brokenHead struct{ sharedspace.SharedSpace }

func (brokenHead) Head() (string, error) {
	return "", errors.New("synthetic git failure /private/path")
}

type testServer struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func newTestServer(t *testing.T, c *Canvas) testServer {
	t.Helper()
	h := c.Handler("127.0.0.1:8765", fstest.MapFS{"index.html": {Data: []byte("Draft Room")}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8765/", nil))
	return testServer{t: t, h: h, cookie: w.Result().Cookies()[0]}
}
func (s testServer) do(method, path, body string) (int, string) {
	s.t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
	if method == "POST" {
		r.Header.Set("Origin", "http://127.0.0.1:8765")
		r.AddCookie(s.cookie)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.Error
}

func TestRegenerateRejectsEmptyAndUnknownIDs(t *testing.T) {
	c, close := setup(t, true)
	defer close()
	// A failed run has no proposal, so an empty ID used to match it.
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	if _, err := c.Regenerate("", "GM"); err == nil {
		t.Fatal("empty proposal ID regenerated an attempt")
	}
	if _, err := c.Regenerate("missing", "GM"); !errors.Is(err, errNotFound) {
		t.Fatalf("unknown proposal ID: %v", err)
	}
	s := newTestServer(t, c)
	if code, _ := s.do("POST", "/api/regenerate", `{"proposal":"","role":"GM"}`); code != http.StatusBadRequest {
		t.Fatalf("empty proposal over HTTP: %d", code)
	}
	if code, _ := s.do("POST", "/api/regenerate", `{"role":"GM"}`); code != http.StatusBadRequest {
		t.Fatalf("missing proposal over HTTP: %d", code)
	}
	if code, _ := s.do("POST", "/api/regenerate", `{"proposal":"missing","role":"GM"}`); code != http.StatusNotFound {
		t.Fatalf("unknown proposal over HTTP: %d", code)
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 {
		t.Fatalf("rejected regenerate queued runs: %d", len(state.Runs))
	}
}

func TestDocumentUnknownProposalIsNotFound(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	if _, err := c.Document("prd.json", "missing"); !errors.Is(err, errNotFound) {
		t.Fatalf("unknown proposal: %v", err)
	}
	if doc, err := c.Document("prd.json", ""); err != nil || doc["accepted"] == "" {
		t.Fatal("accepted document missing without a proposal")
	}
	s := newTestServer(t, c)
	if code, _ := s.do("GET", "/api/document?path=prd.json&proposal=missing", ""); code != http.StatusNotFound {
		t.Fatalf("unknown proposal over HTTP: %d", code)
	}
	if code, _ := s.do("GET", "/api/document?path=prd.json", ""); code != http.StatusOK {
		t.Fatalf("accepted document over HTTP: %d", code)
	}
}

func TestInternalFailuresAreServerErrorsWithoutDetail(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	real := c.space
	c.space = brokenHead{real}
	defer func() { c.space = real }()
	s := newTestServer(t, c)
	code, message := s.do("GET", "/api/state", "")
	if code != http.StatusInternalServerError || message == "" || strings.Contains(message, "private") || strings.Contains(message, "synthetic") {
		t.Fatalf("state failure: %d %q", code, message)
	}
	if code, _ := s.do("POST", "/api/run", `{"agent":"writer","request":"Assess.","role":"GM"}`); code != http.StatusInternalServerError {
		t.Fatalf("run snapshot failure: %d", code)
	}
	// Bad input is still the caller's problem.
	if code, _ := s.do("POST", "/api/role", `{"role":"intern"}`); code != http.StatusBadRequest {
		t.Fatalf("bad role: %d", code)
	}
}

func TestVerifySaveFailureIsNotMissingEvidence(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	s := newTestServer(t, c)
	if code, _ := s.do("POST", "/api/verify", `{"run":"missing"}`); code != http.StatusConflict {
		t.Fatalf("missing evidence: %d", code)
	}
	blockStateWrites(t, c)
	code, message := s.do("POST", "/api/verify", `{"run":"`+id+`"}`)
	if code != http.StatusInternalServerError || strings.Contains(message, "evidence missing") {
		t.Fatalf("save failure after proof: %d %q", code, message)
	}
	if _, err := c.Verify(context.Background(), id); !errors.Is(err, errInternal) {
		t.Fatalf("direct verify: %v", err)
	}
}
