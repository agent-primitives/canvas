package canvas

import (
	"context"
	"errors"
	"testing"

	"github.com/kaimahi-agents/canvas/internal/sessionauth"
	"github.com/kaimahi-agents/canvas/internal/sharedspace"
)

func TestWorkflowKnownUndispatchedRefusalCanBeCanceled(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	c.EnableAuthentication()
	configureTestService(t, c, sessionauth.Identity{Issuer: testIssuer, Subject: "wrong-service"})
	ctx := humanContext(t, "starter")
	f, err := c.StartFlow(ctx, FlowStart{Flow: "release", RequestKey: "refused", Role: "GM", Inputs: []byte(`{"topic":"x"}`), AgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "paused")
	if calls.Load() != 0 {
		t.Fatal("refused credential dispatched")
	}
	if _, err := c.CancelFlow(ctx, FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
		t.Fatal("known no-dispatch refusal holds an irreversible slot", err)
	}
}
func TestWorkflowInitialSnapshotRefusalCanBeCanceled(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	bundle := c.bundles["writer"]
	bundle.Suite = nil
	c.bundles["writer"] = bundle
	f, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "no-snapshot", Role: "GM", Inputs: []byte(`{"topic":"x"}`), AgentRuns: 12})
	if err == nil || f.State != "paused" || calls.Load() != 0 {
		t.Fatal("snapshot was not refused before dispatch", f, err)
	}
	if _, err := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
		t.Fatal("unstarted flow cannot be canceled", err)
	}
}

func TestWorkflowRunningCancellationAndDuplicateStartAreFenced(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c, calls := workflowFixture(t, false, func(map[string]any) string { close(entered); <-release; return "" })
	f := startWorkflow(t, c, "running", 12)
	<-entered
	if _, err := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "owner"}); err == nil {
		t.Error("running cancellation implied revocation")
	}
	if _, err := c.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "too-soon", Role: "GM"}); err == nil {
		t.Error("live generation was retried")
	}
	if duplicate := startWorkflow(t, c, "running", 12); duplicate.ID != f.ID {
		t.Error("running Run key duplicated")
	}
	close(release)
	f = waitWorkflow(t, c, f.ID, "review")
	if calls.Load() != 1 {
		t.Fatal("running commands dispatched another model", calls.Load())
	}
}

func TestWorkflowUnrelatedAndStaleReviewsNeverAdvance(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	head, _ := c.space.Head()
	unrelated, err := c.space.Propose(head, map[string]string{"prd.json": validPRD}, sharedspace.Attribution{Actor: "GM", Agent: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	f := startWorkflow(t, c, "strict", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	if _, err := c.Review(unrelated.ID, "accept", "GM"); err == nil {
		t.Fatal("unrelated manual acceptance raced a flow")
	}
	// Emulate an external writer bypassing the engine, rather than relaxing CAS.
	if _, err := c.space.Accept(unrelated.ID, head, "GM"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(f.Proposal, "accept", "reviewer"); err == nil {
		t.Fatal("stale flow gate merged instead of refusing")
	}
	after, _ := c.GetFlow(context.Background(), f.ID)
	if after.Attempts != 1 || calls.Load() != 1 {
		t.Fatal("unrelated/stale acceptance advanced", after)
	}
}

type flowReviewError struct{ sharedspace.SharedSpace }

func (s flowReviewError) Accept(id, base, actor string) (sharedspace.Proposal, error) {
	p, e := s.SharedSpace.Accept(id, base, actor)
	if e != nil {
		return p, e
	}
	return p, errors.New("synthetic post-commit review failure")
}
func TestWorkflowCommittedReviewFailurePausesWithoutDispatch(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "review-fault", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	c.space = flowReviewError{c.space}
	if _, err := c.Review(f.Proposal, "accept", "reviewer"); err == nil {
		t.Fatal("postcommit error hidden")
	}
	state, _ := c.GetFlow(context.Background(), f.ID)
	if state.State != "uncertain" || state.Attempts != 1 || calls.Load() != 1 {
		t.Fatal("ambiguous committed review dispatched", state)
	}
}
