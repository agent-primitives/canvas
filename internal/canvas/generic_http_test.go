package canvas

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

func TestDefinedHTTPInputBoundary(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	s := newTestServer(t, c)
	for _, body := range []string{
		`{"path":"../escape.json","content":"{}","role":"GM"}`,
		`{"path":"prd.json","content":"{}","role":"GM"}`,
		`{"path":"feedback/invalid.json","content":"{}","role":"GM"}`,
		`{"path":"feedback/new.json","content":"{}","role":"file"}`,
		`{"path":"feedback/new.json","content":"{}","role":"GM","extra":true}`,
	} {
		if code, _ := s.do("POST", "/api/input", body); code != http.StatusBadRequest {
			t.Fatalf("invalid input response: %d", code)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8765/api/input", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://example.invalid")
	r.AddCookie(s.cookie)
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("input route bypassed browser boundary or invoked a model")
	}
	content := `{"team":"Northwind","feedback":"Fictional new feedback; no outcome measured."}`
	body, _ := json.Marshal(map[string]string{"path": "feedback/new.json", "content": content, "role": "GM"})
	if code, message := s.do("POST", "/api/input", string(body)); code != http.StatusAccepted {
		t.Fatalf("save response: %d %s", code, message)
	}
	if calls.Load() != 0 {
		t.Fatal("save response claimed or performed a direct model run")
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ := c.State()
	if len(state.Runs) != 1 {
		t.Fatal("saved source did not become one declared trigger intent")
	}
	completed(t, c, state.Runs[0].ID)
}

type publishedInputSpace struct{ sharedspace.SharedSpace }

func (s publishedInputSpace) WriteInput(path, content string) error {
	if err := s.SharedSpace.WriteInput(path, content); err != nil {
		return err
	}
	return errors.Join(sharedspace.ErrCommitted, errors.New("synthetic post-publication sync failure"))
}

func TestDefinedHTTPWarnsPublishedInputMayStillTrigger(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	c.space = publishedInputSpace{c.space}
	s := newTestServer(t, c)
	content := `{"team":"Northwind","feedback":"Fictional new feedback."}`
	body, _ := json.Marshal(map[string]string{"path": "feedback/published.json", "content": content, "role": "GM"})
	code, message := s.do("POST", "/api/input", string(body))
	if code != http.StatusInternalServerError || !strings.Contains(message, "may still start") || !strings.Contains(message, "before retrying") {
		t.Fatalf("publication warning: %d %s", code, message)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ := c.State()
	if len(state.Runs) != 1 {
		t.Fatal("fixture did not preserve a visible producer event")
	}
	completed(t, c, state.Runs[0].ID)
	if calls.Load() != 1 {
		t.Fatal("published upload must remain exactly one possible trigger")
	}
}

func TestDefinedPresentationAndProposalCopiesOwnSuiteMetadata(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	state, _ := c.State()
	pin := state.Agents[0].Suite.Digest
	state.Agents[0].Suite.Digest = "forged"
	state.Agents[0].Suite.Compositions[0].Digest = "forged"
	next, _ := c.State()
	if next.Agents[0].Suite.Digest != pin || next.Agents[0].Suite.Compositions[0].Digest == "forged" {
		t.Fatal("presentation mutation changed active suite")
	}
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	run.Suite.Digest = "forged"
	fresh, _ := c.GetRun(id)
	if fresh.Suite.Digest == "forged" {
		t.Fatal("run metadata aliases stored authority")
	}
	proposals, _ := c.space.Proposals()
	proposals[0].Attribution.Suite.Compositions[0].Digest = "forged"
	proposals, _ = c.space.Proposals()
	if proposals[0].Attribution.Suite.Compositions[0].Digest == "forged" {
		t.Fatal("proposal presentation aliases cached provenance")
	}
}
