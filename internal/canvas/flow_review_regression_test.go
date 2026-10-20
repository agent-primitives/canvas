package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

func TestWorkflowReopenedStartPreservesExactInputBytes(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	r := FlowStart{Flow: "release", RequestKey: "raw-reopen", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12}
	f, e := c.StartFlow(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	waitWorkflow(t, c, f.ID, "review")
	c.Close()
	before := calls.Load()
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, c.binder, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	got, e := reopened.StartFlow(context.Background(), r)
	if e != nil || got.ID != f.ID || !bytes.Equal(got.Inputs, r.Inputs) {
		t.Fatal("same exact request changed across ledger encoding", got, e)
	}
	r.Inputs = json.RawMessage(`{ "topic": "x" }`)
	if _, e = reopened.StartFlow(context.Background(), r); !errors.Is(e, ErrFlowConflict) {
		t.Fatal("different raw bytes reused key", e)
	}
	if calls.Load() != before {
		t.Fatal("idempotent replay dispatched")
	}
}

func TestWorkflowInvalidReviewActionLeavesGateReviewable(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "demo", true: "verified"}[authenticated], func(t *testing.T) {
			c, _ := workflowFixture(t, false, nil)
			ctx := context.Background()
			if authenticated {
				policy, e := NewFlowReviewerPolicy("reviewer", []sessionauth.Identity{{Issuer: testIssuer, Subject: "acceptor"}})
				if e != nil {
					t.Fatal(e)
				}
				c.EnableAuthentication(policy)
				configureTestService(t, c, testServiceIdentity)
				ctx = humanContext(t, "acceptor")
			}
			startCtx := context.Background()
			if authenticated {
				startCtx = humanContext(t, "requester")
			}
			f, e := c.StartFlow(startCtx, FlowStart{Flow: "release", RequestKey: "invalid-action", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
			if e != nil {
				t.Fatal(e)
			}
			f = waitWorkflow(t, c, f.ID, "review")
			if _, e := c.ReviewContext(ctx, f.Proposal, "approve", "reviewer"); e == nil {
				t.Fatal("invalid action accepted")
			}
			got, _ := c.GetFlow(context.Background(), f.ID)
			if got.State != "review" || got.Proposal != f.Proposal {
				t.Fatal("invalid action consumed gate", got)
			}
			if _, e := c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); e != nil {
				t.Fatal("valid review blocked after invalid action", e)
			}
		})
	}
}

func TestWorkflowLateAttemptFinishCannotRestoreConsumedGate(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(map[bool]string{false: "parked", true: "approved"}[approved], func(t *testing.T) {
			c, f := readyFlowAction(t)
			want := "action"
			if approved {
				v, e := c.InspectAnnouncement(context.Background(), f.Action)
				if e != nil {
					t.Fatal(e)
				}
				v.Confirmation.Confirmed = true
				if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); e != nil {
					t.Fatal(e)
				}
				want = "approved-not-executed"
			}
			c.update(f.Attempt, func(a *attempt) { a.Seconds = 1 })
			got, _ := c.GetFlow(context.Background(), f.ID)
			if got.State != want || got.Proposal != "" {
				t.Fatal("late worker finish restored consumed gate", got)
			}
		})
	}
}

func TestWorkflowQuestionReservationObservesActiveFlow(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "question-race", 12)
	waitWorkflow(t, c, f.ID, "review")
	c.mu.Lock()
	busy := c.askBusyLocked("already-inspected-session")
	c.mu.Unlock()
	if !busy {
		t.Fatal("question final reservation ignored newly inserted flow")
	}
}

func TestWorkflowActiveGateStillAllowsNativeVerify(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "verify-active", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	before := calls.Load()
	if _, e := c.Verify(context.Background(), f.Attempt); e != nil {
		t.Fatal("active flow blocked read-only native verification", e)
	}
	if calls.Load() != before {
		t.Fatal("verification dispatched model")
	}
}

type flowAdmissionHeadHook struct {
	sharedspace.SharedSpace
	hook func()
}

func (s flowAdmissionHeadHook) Head() (string, error) { s.hook(); return s.SharedSpace.Head() }
func TestWorkflowAdmissionChecksAllWorkJournalsAtReservation(t *testing.T) {
	for _, kind := range []string{"pending-recovery", "queued-question", "running-question", "recovery-during-head"} {
		t.Run(kind, func(t *testing.T) {
			c, calls := workflowFixture(t, false, nil)
			add := func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if kind == "pending-recovery" || kind == "recovery-during-head" {
					c.data.Recoveries = append(c.data.Recoveries, resumeAction{ID: "admitted-recovery", Status: "pending"})
				} else {
					status := "queued"
					if kind == "running-question" {
						status = "running"
					}
					c.conversation.Entries = append(c.conversation.Entries, conversationRecord{ConversationMessage: ConversationMessage{ID: "admitted-question", Kind: "question", Status: status}})
				}
			}
			if kind == "recovery-during-head" {
				c.space = flowAdmissionHeadHook{c.space, add}
			} else {
				add()
			}
			_, e := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: kind, Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
			if !errors.Is(e, ErrFlowBusy) {
				t.Fatal("flow admitted over competing journal", e)
			}
			c.mu.Lock()
			n := len(c.data.Flows)
			c.mu.Unlock()
			if n != 0 || calls.Load() != 0 {
				t.Fatal("refused admission reserved/dispatched", n, calls.Load())
			}
		})
	}
}
