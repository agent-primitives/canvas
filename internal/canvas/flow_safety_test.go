package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

func TestWorkflowEarlierGateActionAndSeparateHumanContinue(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approved], func(t *testing.T) {
			filename := flowFixture(t)
			genericConfigEdit(t, filename, "        - {id: revise, agent: writer, gate: {accept: [prd, announcement], role: reviewer}}\n", "")
			c, calls := workflowFixtureAt(t, filename, nil)
			policy, err := NewFlowReviewerPolicy("reviewer", []sessionauth.Identity{{Issuer: testIssuer, Subject: "acceptor"}})
			if err != nil {
				t.Fatal(err)
			}
			c.EnableAuthentication(policy)
			human, service := configureTestService(t, c, testServiceIdentity)
			ctx := humanContext(t, "acceptor")
			f, err := c.StartFlow(humanContext(t, "requester"), FlowStart{Flow: "release", RequestKey: "action", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
			if err != nil {
				t.Fatal(err)
			}
			f = waitWorkflow(t, c, f.ID, "review")
			first := f.Proposal
			if _, err := c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); err != nil {
				t.Fatal(err)
			}
			f = waitWorkflow(t, c, f.ID, "review")
			if _, err := c.RequestAnnouncement(ctx, "announce", first, "reviewer"); err == nil {
				t.Fatal("manual action bypassed critique")
			}
			if _, err := c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); err != nil {
				t.Fatal(err)
			}
			f = waitWorkflow(t, c, f.ID, "action")
			op, err := c.announcementOperation(f.Action)
			if err != nil || op.Snapshot.Proposal != first || op.RequestedBy.Subject != "requester" || op.TriggeredBy.Subject != "acceptor" || op.FlowRun != f.ID {
				t.Fatal("terminal action lost earlier gate/provenance", op, err)
			}
			v, err := c.InspectAnnouncement(ctx, f.Action)
			if err != nil || v.Receipt != nil || v.Status != "pending-approval" {
				t.Fatal("unapproved effect", v, err)
			}
			v.Confirmation.Confirmed = true
			if err := c.DecideAnnouncement(ctx, f.Action, "reviewer", approved, v.Confirmation); err != nil {
				t.Fatal(err)
			}
			state := "denied"
			if approved {
				state = "approved-not-executed"
			}
			f = waitWorkflow(t, c, f.ID, state)
			v, err = c.InspectAnnouncement(ctx, f.Action)
			if err != nil || v.Receipt != nil {
				t.Fatal("approval produced receipt", v, err)
			}
			if _, err := c.CancelFlow(humanContext(t, "requester"), FlowCancel{Run: f.ID, Role: "GM"}); err == nil {
				t.Fatal("action-stage cancel bypassed fences")
			}
			if approved {
				v.Confirmation.Confirmed = true
				if err := c.ContinueAnnouncement(ctx, f.Action, "reviewer", v.Confirmation); err != nil {
					t.Fatal(err)
				}
				waitWorkflow(t, c, f.ID, "complete")
			}
			if calls.Load() != 2 {
				t.Fatal("action used extra model calls", calls.Load())
			}
			if !approved {
				if err := c.RecoverAnnouncement(ctx, f.Action, "reviewer", v.Confirmation); err == nil {
					t.Fatal("denied cleanup exposed Recover")
				}
				head, _ := c.space.Head()
				newer, e := c.space.Propose(head, map[string]string{"announcement.md": "# New accepted announcement\n"}, sharedspace.Attribution{Actor: "GM", Agent: "writer"})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = c.ReviewContext(ctx, newer.ID, "accept", "reviewer"); e != nil {
					t.Fatal(e)
				}
				v, e = c.InspectAnnouncement(ctx, f.Action)
				if e != nil || !v.Newer {
					t.Fatal("newer revision fixture missing", v, e)
				}
				v.Confirmation.Confirmed = true
				if e = c.ContinueAnnouncement(ctx, f.Action, "reviewer", v.Confirmation); e != nil {
					t.Fatal("explicit denied cleanup blocked", e)
				}
				v, e = c.InspectAnnouncement(ctx, f.Action)
				if e != nil || !v.Concluded || v.Receipt != nil {
					t.Fatal("denied cleanup produced effect or did not conclude", v, e)
				}
				if state, _ := c.GetFlow(context.Background(), f.ID); state.State != "denied" {
					t.Fatal("cleanup changed denied decision", state)
				}
				next, e := c.StartFlow(humanContext(t, "requester"), FlowStart{Flow: "release", RequestKey: "after-denial", Role: "GM", Inputs: json.RawMessage(`{"topic":"next"}`), AgentRuns: 12})
				if e != nil {
					t.Fatal("denial permanently locked serial flow", e)
				}
				next = waitWorkflow(t, c, next.ID, "review")
				if _, e = c.CancelFlow(humanContext(t, "requester"), FlowCancel{Run: next.ID, Role: "GM"}); e != nil {
					t.Fatal(e)
				}
			}
			for _, call := range human.snapshot() {
				if call.Method != "Approve" && call.Method != "Resume" {
					t.Fatal("human used execution path", call)
				}
			}
			assertServiceCalls(t, service, "CreateSession", "Exec", "GetSession", "Replay")
		})
	}
}
func TestWorkflowOwnerGrantAndNoLabelOrServiceAuthority(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	policy, err := NewFlowReviewerPolicy("reviewer", []sessionauth.Identity{{Issuer: testIssuer, Subject: "reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewFlowReviewerPolicy("owner", []sessionauth.Identity{{Issuer: testIssuer, Subject: "canvas-owner"}})
	if err != nil {
		t.Fatal(err)
	}
	c.flowOptions.OwnerAuthorizer = owner
	c.EnableAuthentication(policy)
	configureTestService(t, c, testServiceIdentity)
	f, err := c.StartFlow(humanContext(t, "requester"), FlowStart{Flow: "release", RequestKey: "owner", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	for _, subject := range []string{"outsider", testServiceIdentity.Subject} {
		if _, err := c.CancelFlow(humanContext(t, subject), FlowCancel{Run: f.ID, Role: "owner"}); err == nil {
			t.Fatal("ungranted owner canceled", subject)
		}
	}
	if _, err := c.CancelFlow(humanContext(t, "canvas-owner"), FlowCancel{Run: f.ID, Role: "owner"}); err != nil {
		t.Fatal("explicit owner grant refused", err)
	}
}
func TestWorkflowIntentVisibleBeforeProviderAndReopenNeverDispatches(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	c.EnableAuthentication()
	_, service := configureTestService(t, c, testServiceIdentity)
	service.onCreate = func() {
		saved := readPublishedLedger(t, c)
		if len(saved.Flows) != 1 || len(saved.Attempts) != 1 || saved.Flows[0].Attempt != saved.Attempts[0].ID || saved.Flows[0].Attempts != 1 || saved.Attempts[0].FlowRun != saved.Flows[0].ID || saved.Attempts[0].FlowStage != "create-intent" {
			t.Errorf("dispatch before durable binding: %+v", saved)
		}
	}
	f, err := c.StartFlow(humanContext(t, "requester"), FlowStart{Flow: "release", RequestKey: "durable", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	c.Close()
	before := calls.Load()
	reopened, err := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, c.binder, FlowRuntimeOptions{MaxAgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.GetFlow(context.Background(), f.ID)
	if err != nil || restored.State != "uncertain" || restored.Attempts != 1 {
		t.Fatal("restart inferred permission to dispatch", restored, err)
	}
	if _, err := reopened.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "ambiguous", Role: "GM"}); err == nil {
		t.Fatal("uncertain retry admitted")
	}
	if _, err := reopened.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); err == nil {
		t.Fatal("uncertain slot cleared")
	}
	if calls.Load() != before {
		t.Fatal("reopen/status retried execution")
	}
}
func TestWorkflowLargeStructuredInputsWithoutManualLimitRelaxation(t *testing.T) {
	filename := flowFixture(t)
	schema := filepath.Join(filepath.Dir(filename), "schemas", "run.json")
	raw, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schema, []byte(strings.Replace(string(raw), `"maxLength":256`, `"maxLength":65500`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var request string
	c, _ := workflowFixtureAt(t, filename, func(in map[string]any) string { request = in["request"].(string); return "" })
	topic := strings.Repeat("x", 3000)
	inputs, _ := json.Marshal(map[string]any{"topic": topic})
	f, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "large", Role: "GM", Inputs: inputs, AgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	if !strings.Contains(request, topic) || strings.Contains(request, `"audience"`) {
		t.Fatal("inputs truncated or defaults injected")
	}
	if _, err := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Manual("writer", "", topic, "GM"); err == nil {
		t.Fatal("manual request limit silently relaxed")
	}
}

// A real Git proposal can be durable while its secondary receipt reports error.
type flowPublicationError struct{ sharedspace.SharedSpace }

func (s flowPublicationError) Propose(base string, changes map[string]string, a sharedspace.Attribution) (sharedspace.Proposal, error) {
	p, e := s.SharedSpace.Propose(base, changes, a)
	if e != nil {
		return p, e
	}
	return p, errors.New("synthetic post-commit publication failure")
}
func TestWorkflowAmbiguousPublishedProposalCannotBecomeReviewable(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	original := c.space
	c.space = flowPublicationError{original}
	f := startWorkflow(t, c, "published-ambiguous", 12)
	c.wg.Wait()
	f, err := c.GetFlow(context.Background(), f.ID)
	if err != nil || f.State != "uncertain" {
		t.Fatal("ambiguous proposal acquired review authority", f, err)
	}
	c.space = original
	ps, err := c.space.Proposals()
	if err != nil || len(ps) != 1 {
		t.Fatal("durable proposal missing", ps, err)
	}
	if _, err := c.Review(ps[0].ID, "accept", "reviewer"); err == nil {
		t.Fatal("orphan flow proposal accepted through manual route")
	}
}
