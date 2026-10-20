package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/agent-primitives/sessions/api"
)

// Real host records the unanswered model intent; only the external provider is scripted.
func resumeCanvas(t *testing.T, question bool, outputs ...string) (*Canvas, *atomic.Int32, *atomic.Bool, func()) {
	t.Helper()
	d := nativeData(t)
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(!question)
	c, cleanup := nativeProviderFixture(t, d, func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		if fail.Load() {
			return api.ModelResponse{}, errors.New("private provider failure")
		}
		input, err := nativeRequestInput(req)
		if err != nil {
			return api.ModelResponse{}, err
		}
		output := nativeOutput(d, input)
		if strings.Contains(req.Messages[len(req.Messages)-1].Text(), `"mode":"question"`) {
			output = `{"answer":"The recorded synthetic evidence supported the rank."}`
		}
		if len(outputs) != 0 && (!question || strings.Contains(req.Messages[len(req.Messages)-1].Text(), `"mode":"question"`)) {
			output = outputs[0]
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", output)}, nil
	})
	return c, &calls, &fail, cleanup
}

func TestResumeHTTPRequiresConfirmationThenRecoversOriginalTarget(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "question"}[question], func(t *testing.T) {
			c, calls, fail, close := resumeCanvas(t, question)
			defer close()
			id, err := c.Manual("writer", "", "Assess synthetic evidence.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			original := completed(t, c, id)
			message := ""
			if question {
				fail.Store(true)
				message, err = c.Ask(id, "Why this rank?", "coach")
				if err != nil {
					t.Fatal(err)
				}
				awaitQuestion(t, c, message)
			}
			c.wg.Wait()
			before, _ := c.State()
			host := "127.0.0.1:8765"
			h := c.Handler(host, fstest.MapFS{"index.html": {Data: []byte("room")}})
			get := httptest.NewRecorder()
			h.ServeHTTP(get, httptest.NewRequest("GET", "http://"+host+"/", nil))
			cookie := get.Result().Cookies()[0]
			post := func(body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://"+host+"/api/resume", strings.NewReader(body))
				r.Header.Set("Origin", "http://"+host)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			body := map[string]any{"run": id, "role": "owner"}
			if message != "" {
				body["message"] = message
			}
			raw, _ := json.Marshal(body)
			if w := post(string(raw)); w.Code != 400 && w.Code != 409 {
				t.Fatalf("unconfirmed status=%d %s", w.Code, w.Body.String())
			}
			n := calls.Load()
			url := "http://" + host + "/api/resume?run=" + id
			if message != "" {
				url += "&message=" + message
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
			var inspection map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &inspection) != nil || inspection["available"] != true {
				t.Fatalf("inspection=%d %s", w.Code, w.Body.String())
			}
			if calls.Load() != n {
				t.Fatal("inspection invoked provider")
			}
			confirmation := map[string]any{"confirmed": true, "actionId": "0123456789abcdef0123456789abcdef"}
			for _, key := range []string{"session", "execution", "lastSeq", "head", "sessionVersion"} {
				confirmation[key] = inspection[key]
			}
			body["confirmation"] = confirmation
			raw, _ = json.Marshal(body)
			fail.Store(false)
			w = post(string(raw))
			if w.Code != 202 {
				t.Fatalf("confirmed status=%d %s", w.Code, w.Body.String())
			}
			c.wg.Wait()
			if calls.Load() != n+1 {
				t.Fatalf("provider calls=%d want %d", calls.Load(), n+1)
			}
			after, _ := c.State()
			run, _ := c.GetRun(id)
			if question {
				entry := awaitQuestion(t, c, message)
				if entry.Status != "answered" || entry.Answer == "" || run.Proposal != original.Proposal || len(after.Proposals) != len(before.Proposals) {
					t.Fatalf("question recovery became proposal or lost answer: %+v", entry)
				}
			} else if run.Status != "proposed" || run.Proposal == "" || run.Base != original.Base {
				t.Fatalf("run did not publish on original base: %+v", run)
			}
			if after.Head != before.Head {
				t.Fatal("Resume accepted a proposal")
			}
			resumedCount := func(steps []Activity) int {
				n := 0
				for _, step := range steps {
					if step.Kind == "resumed" && !step.At.IsZero() {
						n++
					}
				}
				return n
			}
			var saved ledger
			rawLedger, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
			if err != nil || json.Unmarshal(rawLedger, &saved) != nil || len(saved.Attempts) != 1 {
				t.Fatal("cannot read durable recovered attempt")
			}
			if resumedCount(run.Activity) != 1 || resumedCount(saved.Attempts[0].Activity) != 1 {
				t.Fatal("successful recovery did not record one durable resumed activity")
			}
			if w := post(string(raw)); w.Code != 409 {
				t.Fatalf("duplicate status=%d", w.Code)
			}
			c.wg.Wait()
			if calls.Load() != n+1 {
				t.Fatal("duplicate resent request")
			}
		})
	}
}
