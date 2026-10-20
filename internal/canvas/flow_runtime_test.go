package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/canvas/internal/contract"
	"github.com/kaimahi-agents/canvas/internal/sessionauth"
)

func workflowFixture(t *testing.T, action bool, output func(map[string]any) string) (*Canvas, *atomic.Int32) {
	t.Helper()
	filename := flowFixture(t)
	if !action {
		genericConfigEdit(t, filename, "        - {id: announce, action: announce}\n", "")
	}
	return workflowFixtureAt(t, filename, output)
}
func workflowFixtureAt(t *testing.T, filename string, output func(map[string]any) string) (*Canvas, *atomic.Int32) {
	t.Helper()
	d, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range d.Bundles {
		d.Bundles[i], err = contract.ForModel(b, "test-model")
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := new(atomic.Int32)
	c, close := nativeProviderFixture(t, d, func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		input, err := nativeRequestInput(r)
		if err != nil {
			return api.ModelResponse{}, err
		}
		text := ""
		if output != nil {
			text = output(input)
		}
		if text == "" {
			if input["agent"] == "reviewer" {
				text = changeEnvelope("review", d.Seed["review.json"])
			} else {
				text = completeWriterOutput()
			}
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", text)}, nil
	})
	t.Cleanup(close)
	return c, calls
}
func startWorkflow(t *testing.T, c *Canvas, key string, budget int) FlowRun {
	t.Helper()
	f, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: key, Role: "GM", Inputs: json.RawMessage(`{"topic":"Fictional pilot"}`), AgentRuns: budget})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func waitWorkflow(t *testing.T, c *Canvas, id, state string) FlowRun {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		f, err := c.GetFlow(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if f.State == state {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	f, _ := c.GetFlow(context.Background(), id)
	t.Fatalf("flow did not reach %s: %+v", state, f)
	return f
}
func TestWorkflowOrderedGatesAndManualBypasses(t *testing.T) {
	var observed []map[string]any
	c, calls := workflowFixture(t, false, func(input map[string]any) string { observed = append(observed, input); return "" })
	f := startWorkflow(t, c, "run-1", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	if calls.Load() != 1 || f.Step != "draft" || f.Attempts != 1 {
		t.Fatalf("first gate: %+v calls%d", f, calls.Load())
	}
	if _, err := c.Manual("writer", "", "Competing draft", "GM"); err == nil {
		t.Fatal("manual bypass admitted")
	}
	head, _ := c.space.Head()
	if err := c.WriteInput("feedback/initial.json", `{}`, "GM"); err == nil {
		t.Fatal("input mutation admitted")
	}
	if err := c.ScanInputs(); err == nil {
		t.Fatal("watch admission not fenced")
	}
	if _, err := c.RequestAnnouncement(context.Background(), "announce", f.Proposal, "GM"); err == nil {
		t.Fatal("early public action admitted")
	}
	if _, err := c.Review(f.Proposal, "accept", "GM"); err == nil {
		t.Fatal("local mode skipped compiled gate role")
	}
	if _, err := c.Ask(f.Attempt, "Explain", "GM"); err == nil {
		t.Fatal("question bypassed flow budget")
	}
	for _, ref := range []string{f.Attempt, f.Proposal} {
		if _, err := c.Regenerate(ref, "GM"); err == nil {
			t.Fatal("regenerate bypassed flow budget")
		}
	}
	if c.InspectResume(context.Background(), f.Attempt, "").Available {
		t.Fatal("generic resume available on flow")
	}
	if now, _ := c.space.Head(); now != head {
		t.Fatal("refused mutations changed accepted head")
	}
	first := f.Proposal
	for _, next := range []string{"critique", "revise", ""} {
		if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
			t.Fatal(err)
		}
		if next == "" {
			f = waitWorkflow(t, c, f.ID, "complete")
		} else {
			f = waitWorkflow(t, c, f.ID, "review")
			if f.Step != next {
				t.Fatalf("wrong step: %+v", f)
			}
		}
	}
	if calls.Load() != 3 || f.Attempts != 3 {
		t.Fatal("hidden dispatch", calls.Load(), f)
	}
	if _, err := c.Review(first, "accept", "reviewer"); err == nil {
		t.Fatal("old proposal advanced completed flow")
	}
	if len(observed) != 3 || !strings.Contains(observed[0]["request"].(string), "Fictional pilot") {
		t.Fatal("structured topic missing")
	}
	for _, in := range observed[1:] {
		if in["acceptedBase"] == head {
			t.Fatal("next step reused original base")
		}
	}
}
func TestWorkflowAdmissionIdempotencyBudgetAndFrozenSnapshots(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	for _, req := range []FlowStart{
		{Flow: "release", RequestKey: "x", Role: "GM", Inputs: json.RawMessage(`{}`), AgentRuns: 12},
		{Flow: "release", RequestKey: "x", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 13},
		{Flow: "release", RequestKey: "x", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 0},
	} {
		if _, err := c.StartFlow(context.Background(), req); err == nil {
			t.Fatal("invalid admission", req)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs dispatched")
	}
	f := startWorkflow(t, c, "same-key", 1)
	f = waitWorkflow(t, c, f.ID, "review")
	same := startWorkflow(t, c, "same-key", 1)
	if same.ID != f.ID || calls.Load() != 1 {
		t.Fatal("duplicate run dispatched")
	}
	if _, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "same-key", Role: "GM", Inputs: json.RawMessage(`{"topic":"changed"}`), AgentRuns: 1}); err == nil {
		t.Fatal("changed key payload accepted")
	}
	if _, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "other", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 1}); err == nil {
		t.Fatal("two active flows")
	}
	f.Inputs[0] = '['
	f.Accepted["forged"] = "forged"
	if _, err := c.Review(same.Proposal, "accept", "reviewer"); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "exhausted")
	if f.Attempts != 1 || calls.Load() != 1 || f.Accepted["forged"] != "" {
		t.Fatal("budget/snapshot changed", f, calls.Load())
	}
}
func TestWorkflowRequiredOutputsRetryAndRequestChanges(t *testing.T) {
	var partial atomic.Bool
	partial.Store(true)
	c, calls := workflowFixture(t, false, func(map[string]any) string {
		if partial.Load() {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "retry", 4)
	f = waitWorkflow(t, c, f.ID, "paused")
	proposals, _ := c.space.Proposals()
	if len(proposals) != 0 || f.Attempts != 1 {
		t.Fatal("partial output published", proposals, f)
	}
	partial.Store(false)
	r := FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "retry-1", Role: "GM"}
	if _, err := c.RetryFlow(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	if _, err := c.RetryFlow(context.Background(), r); err != nil {
		t.Fatal("idempotent retry", err)
	}
	if calls.Load() != 2 {
		t.Fatal("duplicate retry dispatched")
	}
	old := f.Proposal
	change := FlowChanges{Run: f.ID, Proposal: old, RequestKey: "feedback-1", Role: "reviewer", Feedback: "Please clarify the fictional scope."}
	if _, err := c.RequestFlowChanges(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	if f.Step != "draft" || f.Attempts != 3 || f.Proposal == old || calls.Load() != 3 {
		t.Fatal("request changes did not retry same step", f, calls.Load())
	}
	if _, err := c.Review(old, "accept", "reviewer"); err == nil {
		t.Fatal("rejected predecessor accepted")
	}
	if _, err := c.RequestFlowChanges(context.Background(), change); err != nil {
		t.Fatal("idempotent feedback", err)
	}
	if calls.Load() != 3 {
		t.Fatal("feedback repeated dispatch")
	}
	if _, err := c.Review(f.Proposal, "reject", "reviewer"); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "rejected")
	if calls.Load() != 3 {
		t.Fatal("reject dispatched", f)
	}
}
func TestWorkflowAuthenticatedRoleAndRequesterOwnerCancellation(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	reviewer := sessionauth.Identity{Issuer: testIssuer, Subject: "reviewer"}
	policy, err := NewFlowReviewerPolicy("reviewer", []sessionauth.Identity{reviewer})
	if err != nil {
		t.Fatal(err)
	}
	c.EnableAuthentication(policy)
	configureTestService(t, c, testServiceIdentity)
	ctx := humanContext(t, "starter")
	f, err := c.StartFlow(ctx, FlowStart{Flow: "release", RequestKey: "auth", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	if _, err := c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); !errors.Is(err, errDocumentReviewDenied) {
		t.Fatal("declaration granted membership", err)
	}
	if _, err := c.CancelFlow(humanContext(t, "outsider"), FlowCancel{Run: f.ID, Role: "owner"}); err == nil {
		t.Fatal("owner label granted cancellation")
	}
	if _, err := c.CancelFlow(ctx, FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReviewContext(humanContext(t, "reviewer"), f.Proposal, "accept", "reviewer"); err == nil {
		t.Fatal("canceled proposal accepted")
	}
}
