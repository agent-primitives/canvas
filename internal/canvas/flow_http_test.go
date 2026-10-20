package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFlowHTTPReadOnlySnapshot(t *testing.T) {
	filename := flowFixture(t)
	c, calls := workflowFixtureAt(t, filename, nil)
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	// Mutating presentation and the source file must not alter frozen controls.
	c.definition.Flows["release"] = FlowDefinition{}
	if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "schemas/run.json"), []byte(`{"type":"object"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w := apiRequest(h, cookie, "GET", "/api/flows", "")
		var panel struct {
			OperatorAgentRuns int    `json:"operatorAgentRuns"`
			Head              string `json:"head"`
			Flows             []struct {
				ID         string         `json:"id"`
				Definition FlowDefinition `json:"definition"`
				Schema     map[string]any `json:"schema"`
			} `json:"flows"`
			Documents map[string]struct {
				Path string `json:"path"`
			} `json:"documents"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &panel) != nil {
			t.Fatalf("flow inspection: %d %s", w.Code, w.Body.String())
		}
		if panel.Head == "" || panel.OperatorAgentRuns != 12 || len(panel.Flows) != 1 || panel.Flows[0].ID != "release" || len(panel.Flows[0].Definition.Steps) != 4 || panel.Documents["announcement"].Path != "announcement.md" {
			t.Fatalf("missing bound flow controls: %+v", panel)
		}
		properties, _ := panel.Flows[0].Schema["properties"].(map[string]any)
		if properties["topic"] == nil || properties["urgent"] == nil {
			t.Fatal("inspection reread or lost the compiled schema", panel.Flows[0].Schema)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("inspection dispatched native model work")
	}
	f := startWorkflow(t, c, "inspection-key", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	before, _ := c.space.Head()
	w := apiRequest(h, cookie, "GET", "/api/flows", "")
	var panel struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &panel) != nil || len(panel.Runs) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	run := panel.Runs[0]
	if string(run["requestKey"]) != `"inspection-key"` || string(run["id"]) != `"`+f.ID+`"` {
		t.Fatal("lost-response lookup unavailable", w.Body.String())
	}
	if run["commands"] != nil || run["inputs"] != nil {
		t.Fatal("poll exposed command journal/full historical inputs")
	}
	w = apiRequest(h, cookie, "GET", "/api/flow?run="+f.ID, "")
	var detail map[string]json.RawMessage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail["inputs"] == nil || detail["commands"] != nil {
		t.Fatal("selected detail scope", w.Code, w.Body.String())
	}
	after, _ := c.space.Head()
	if before != after || calls.Load() != 1 {
		t.Fatal("reads advanced the gate")
	}
}

func TestFlowHTTPStrictCommands(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	base := `{"flow":"release","requestKey":"http-key","role":"GM","inputs":{"topic":"From HTTP"},"agentRuns":12}`
	for _, body := range []string{`null`, base + ` {}`, strings.Replace(base, `"role":"GM"`, `"role":"GM","role":"owner"`, 1), strings.Replace(base, `"role":"GM"`, `"role":"GM","actor":"trusted"`, 1), strings.Replace(base, `"From HTTP"`, `"`+strings.Repeat("x", 65537)+`"`, 1)} {
		w := apiRequest(h, cookie, "POST", "/api/flow/start", body)
		if w.Code != 400 {
			t.Errorf("invalid request: %d %s", w.Code, w.Body.String())
		}
	}
	w := apiRequest(h, cookie, "POST", "/api/flow/start", strings.Replace(base, `"agentRuns":12`, `"agentRuns":13`, 1))
	if w.Code != 409 {
		t.Fatal("budget ceiling not refused", w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("invalid start dispatched")
	}
	r := httptest.NewRequest("POST", "http://"+apiTestHost+"/api/flow/start", strings.NewReader(base))
	r.AddCookie(cookie) // No Origin.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("flow bypassed browser boundary", w.Code)
	}
	w = apiRequest(h, cookie, "POST", "/api/flow/start", base)
	var started struct {
		ID string `json:"id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &started) != nil || started.ID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	waitWorkflow(t, c, started.ID, "review")
	w = apiRequest(h, cookie, "POST", "/api/flow/start", base)
	if w.Code != 202 || !strings.Contains(w.Body.String(), started.ID) {
		t.Fatal("duplicate key did not return existing flow", w.Code, w.Body.String())
	}
	w = apiRequest(h, cookie, "POST", "/api/flow/start", strings.Replace(base, "From HTTP", "Changed HTTP", 1))
	if w.Code != 409 || calls.Load() != 1 {
		t.Fatal("changed key payload dispatched", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ path, body string }{
		{"/api/run", `{"agent":"writer","request":"Unrelated","role":"GM"}`},
		{"/api/announcement/request", `{"proposal":"unrelated","action":"announce","role":"GM"}`},
	} {
		w = apiRequest(h, cookie, "POST", tc.path, tc.body)
		if w.Code < 400 {
			t.Fatal("manual work entered active flow", tc.path, w.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("manual bypass dispatched")
	}
	manual, _, _ := definedFixture(t, nil)
	w = apiRequest(manual.APIHandler(apiTestHost), nil, "GET", "/api/flows", "")
	if w.Code != 404 {
		t.Fatal("manual runtime exposed flow transport", w.Code)
	}
	c.EnableRemoteSessions()
	w = apiRequest(h, cookie, "GET", "/api/flows", "")
	if w.Code != 404 {
		t.Fatal("remote flow UI silently qualified", w.Code)
	}
}

func TestFlowHTTPCustomGateRole(t *testing.T) {
	filename := flowFixture(t)
	declarations := strings.Replace(flowDeclarations, "reviewRoles: [reviewer]", "reviewRoles: [editor]", 1)
	genericConfigEdit(t, filename, flowDeclarations, strings.ReplaceAll(declarations, "role: reviewer", "role: editor"))
	c, calls := workflowFixtureAt(t, filename, nil)
	f := startWorkflow(t, c, "custom-role", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	body, _ := json.Marshal(map[string]string{"proposal": f.Proposal, "action": "accept", "role": "editor"})
	w := apiRequest(h, cookie, "POST", "/api/review", string(body))
	if w.Code != 200 {
		t.Fatal("compiled gate role cannot use human HTTP review", w.Code, w.Body.String())
	}
	f = waitWorkflow(t, c, f.ID, "review")
	if f.Step != "critique" || calls.Load() != 2 {
		t.Fatal("human gate did not advance exactly once", f, calls.Load())
	}
}

func TestFlowHTTPOrderedHumanGates(t *testing.T) {
	filename := flowFixture(t)
	// An additional allowed replacement still belongs to the complete gate.
	genericConfigEdit(t, filename, "id: draft, agent: writer, gate: {accept: [prd, announcement]", "id: draft, agent: writer, gate: {accept: [prd]")
	observed := make(chan map[string]any, 3)
	c, calls := workflowFixtureAt(t, filename, func(in map[string]any) string { observed <- in; return "" })
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	w := apiRequest(h, cookie, "POST", "/api/flow/start", `{"flow":"release","requestKey":"ordered-http","role":"GM","inputs":{"topic":"HTTP topic"},"agentRuns":12}`)
	var f FlowRun
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &f) != nil || f.ID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, step := range []string{"draft", "critique", "revise"} {
		f = waitWorkflow(t, c, f.ID, "review")
		if f.Step != step || calls.Load() != int32(i+1) {
			t.Fatal("wrong gate", f, calls.Load())
		}
		in := <-observed
		var request struct {
			Inputs map[string]any `json:"inputs"`
		}
		if raw, ok := in["request"].(string); !ok || json.Unmarshal([]byte(raw), &request) != nil || request.Inputs["topic"] != "HTTP topic" {
			t.Fatal("topic missing from actual native input", in)
		}
		if i == 0 {
			proposals, err := c.space.Proposals()
			if err != nil || len(proposals) != 1 || proposals[0].ID != f.Proposal || len(proposals[0].Changes) != 2 {
				t.Fatal("extra replacement escaped the whole proposal", proposals, err)
			}
		}
		body, _ := json.Marshal(map[string]string{"proposal": f.Proposal, "action": "accept", "role": "reviewer"})
		w = apiRequest(h, cookie, "POST", "/api/review", string(body))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	f = waitWorkflow(t, c, f.ID, "action")
	v, err := c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil || v.Status != "pending-approval" {
		t.Fatal(v, err)
	}
	v.Confirmation.Confirmed = true
	body, _ := json.Marshal(map[string]any{"id": f.Action, "role": "reviewer", "approved": true, "confirmation": v.Confirmation})
	w = apiRequest(h, cookie, "POST", "/api/announcement/decision", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v, err = c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil || v.Status != "approved" || v.Concluded {
		t.Fatal("approval ran simulation", v, err)
	}
	// Reads and incomplete aggregate proof cannot perform Continue.
	w = apiRequest(h, cookie, "POST", "/api/flow/verify", `{"run":"`+f.ID+`"}`)
	var proof FlowProof
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &proof) != nil || proof.Complete {
		t.Fatal("unconcluded action falsely verified", w.Code, w.Body.String())
	}
	v.Confirmation.Confirmed = true
	body, _ = json.Marshal(map[string]any{"id": f.Action, "role": "reviewer", "confirmation": v.Confirmation})
	w = apiRequest(h, cookie, "POST", "/api/announcement/continue", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitWorkflow(t, c, f.ID, "complete")
	ledger, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	w = apiRequest(h, cookie, "POST", "/api/flow/verify", `{"run":"`+f.ID+`"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &proof) != nil || !proof.Complete || len(proof.Sessions) != 4 {
		t.Fatal("aggregate incomplete", w.Code, w.Body.String())
	}
	for _, session := range proof.Sessions {
		if !session.Proof.Reconstructed || !session.Proof.LiveUnchanged || session.Proof.ModelCalls != 0 || session.Proof.ExecutorCalls == nil || *session.Proof.ExecutorCalls != 0 {
			t.Fatal("unproven native replay", session)
		}
	}
	after, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if !bytes.Equal(ledger, after) || calls.Load() != 3 {
		t.Fatal("diagnostic mutated records or invoked model")
	}
}

func TestFlowHTTPRecoveryIsExplicit(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	c.space = reviewFaultSpace{SharedSpace: c.space, after: true}
	if _, err := c.Review(f.Proposal, "accept", "reviewer"); err == nil {
		t.Fatal("crash fixture did not lose committed review reply")
	}
	c = reopenTestFlow(t, c)
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	for range 2 {
		w := apiRequest(h, cookie, "GET", "/api/flows", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	f, _ = c.GetFlow(context.Background(), f.ID)
	if f.State != "uncertain" || calls.Load() != 1 {
		t.Fatal("GET reconciled or dispatched", f, calls.Load())
	}
	w := apiRequest(h, cookie, "POST", "/api/flow/reconcile", `{"run":"`+f.ID+`","role":"GM"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &f) != nil || f.State != "ready" || calls.Load() != 1 {
		t.Fatal("reconcile dispatched", w.Code, w.Body.String())
	}
	w = apiRequest(h, cookie, "POST", "/api/flow/advance", `{"run":"`+f.ID+`","role":"GM","requestKey":"advance-http"}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f = waitWorkflow(t, c, f.ID, "review")
	if f.Step != "critique" || calls.Load() != 2 {
		t.Fatal("explicit advance failed", f, calls.Load())
	}
}

func TestFlowHTTPCancelRetainsPendingIdentity(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once, released sync.Once
	c, _ := workflowFixture(t, false, func(map[string]any) string { once.Do(func() { close(entered) }); <-release; return "" })
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	f := startWorkflow(t, c, "http-cancel", 12)
	<-entered
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	w := apiRequest(h, cookie, "POST", "/api/flow/cancel", `{"run":"`+f.ID+`","role":"GM"}`)
	var outcome struct {
		Code string  `json:"code"`
		Run  FlowRun `json:"run"`
	}
	if w.Code != 409 || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || outcome.Code != "FLOW_CANCEL_PENDING" || outcome.Run.ID != f.ID || outcome.Run.State != "cancelling" {
		t.Fatal("persisted cancellation lost its identity or claimed settlement", w.Code, w.Body.String())
	}
	released.Do(func() { close(release) })
}

func TestFlowHTTPRetryAndFeedbackKeepCountedBudget(t *testing.T) {
	first := true
	c, calls := workflowFixture(t, false, func(map[string]any) string {
		if first {
			first = false
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "http-retry", 2)
	f = waitWorkflow(t, c, f.ID, "paused")
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	body := `{"run":"` + f.ID + `","attempt":"` + f.Attempt + `","requestKey":"retry-http","role":"GM"}`
	w := apiRequest(h, cookie, "POST", "/api/flow/retry", body)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f = waitWorkflow(t, c, f.ID, "review")
	w = apiRequest(h, cookie, "POST", "/api/flow/retry", body)
	if w.Code != 202 || calls.Load() != 2 {
		t.Fatal("retry key repeated counted work", w.Code, w.Body.String())
	}
	body = `{"run":"` + f.ID + `","proposal":"` + f.Proposal + `","requestKey":"changes-http","role":"reviewer","feedback":"Make the scope clearer."}`
	w = apiRequest(h, cookie, "POST", "/api/flow/changes", body)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f = waitWorkflow(t, c, f.ID, "exhausted")
	if f.Attempts != 2 || calls.Load() != 2 || len(f.Feedback) != 1 || f.Feedback[0].Text != "Make the scope clearer." {
		t.Fatal("feedback silently extended budget or lost attribution", f, calls.Load())
	}
}
