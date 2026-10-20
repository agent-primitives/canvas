package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

type reviewFaultSpace struct {
	sharedspace.SharedSpace
	before    func()
	refuse    error
	after     bool
	transform func([]sharedspace.Proposal) []sharedspace.Proposal
	order     *[]string
}

func (s reviewFaultSpace) Accept(id, base, role string) (sharedspace.Proposal, error) {
	if s.before != nil {
		s.before()
	}
	if s.refuse != nil {
		return sharedspace.Proposal{}, s.refuse
	}
	p, e := s.SharedSpace.Accept(id, base, role)
	if e == nil && s.after {
		e = sharedspace.ErrCommitted
	}
	return p, e
}
func (s reviewFaultSpace) AcceptReviewed(id, base string, who sharedspace.ReviewActor) (sharedspace.Proposal, error) {
	if s.before != nil {
		s.before()
	}
	if s.refuse != nil {
		return sharedspace.Proposal{}, s.refuse
	}
	p, e := s.SharedSpace.(reviewedSpace).AcceptReviewed(id, base, who)
	if e == nil && s.after {
		e = sharedspace.ErrCommitted
	}
	return p, e
}
func (s reviewFaultSpace) RejectReviewed(id string, who sharedspace.ReviewActor) (sharedspace.Proposal, error) {
	return s.SharedSpace.(reviewedSpace).RejectReviewed(id, who)
}
func (s reviewFaultSpace) Proposals() ([]sharedspace.Proposal, error) {
	if s.order != nil {
		*s.order = append(*s.order, "proposals")
	}
	p, e := s.SharedSpace.Proposals()
	if e == nil && s.transform != nil {
		p = s.transform(p)
	}
	return p, e
}
func (s reviewFaultSpace) History() ([]sharedspace.Checkpoint, error) {
	if s.order != nil {
		*s.order = append(*s.order, "history")
	}
	return s.SharedSpace.History()
}
func (s reviewFaultSpace) Head() (string, error) {
	if s.order != nil {
		*s.order = append(*s.order, "head")
	}
	return s.SharedSpace.Head()
}
func savedReviews(t *testing.T, c *Canvas) []map[string]any {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if e != nil {
		t.Fatal(e)
	}
	var j struct {
		Flows []struct{ Reviews []map[string]any }
	}
	if e = json.Unmarshal(b, &j); e != nil {
		t.Fatal(e)
	}
	if len(j.Flows) != 1 {
		return nil
	}
	return j.Flows[0].Reviews
}
func configureReviewIdentity(t *testing.T, c *Canvas) context.Context {
	t.Helper()
	p, e := NewFlowReviewerPolicy("reviewer", []sessionauth.Identity{{Issuer: testIssuer, Subject: "acceptor"}})
	if e != nil {
		t.Fatal(e)
	}
	c.EnableAuthentication(p)
	configureTestService(t, c, testServiceIdentity)
	return humanContext(t, "acceptor")
}
func startReviewFlow(t *testing.T, c *Canvas, verified bool) FlowRun {
	t.Helper()
	ctx := context.Background()
	if verified {
		ctx = humanContext(t, "requester")
	}
	f, e := c.StartFlow(ctx, FlowStart{Flow: "release", RequestKey: "review-evidence", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	return waitWorkflow(t, c, f.ID, "review")
}
func TestFlowReviewIntentBeforeCommitAndRecoveredEvidence(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "verified"}[verified], func(t *testing.T) {
			c, calls := workflowFixture(t, false, nil)
			ctx := context.Background()
			if verified {
				ctx = configureReviewIdentity(t, c)
			}
			f := startReviewFlow(t, c, verified)
			c.space = reviewFaultSpace{SharedSpace: c.space, after: true, before: func() {
				rs := savedReviews(t, c)
				if len(rs) != 1 || rs[0]["proposal"] != f.Proposal || rs[0]["attempt"] != f.Attempt || rs[0]["action"] != "accept" || rs[0]["applied"] == true {
					t.Fatal("typed intent missing before store decision", rs)
				}
			}}
			if _, e := c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); !errors.Is(e, sharedspace.ErrCommitted) {
				t.Fatal("fixture failed to commit ambiguously", e)
			}
			c.Close()
			reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, c.binder, FlowRuntimeOptions{MaxAgentRuns: 12})
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			before, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
			head, _ := c.space.Head()
			n := calls.Load()
			for range 2 {
				v, e := reopened.InspectFlowReview(context.Background(), f.ID)
				if e != nil || v.State != "accepted" || v.AcceptedCommit == "" || v.Proposal != f.Proposal || v.Applied {
					t.Fatal("durable accepted evidence lost", v, e)
				}
				if verified {
					if v.Reviewer.Subject != "acceptor" || v.LocalRole != "" {
						t.Fatal("verified reviewer lost", v)
					}
				} else if v.Reviewer != (sharedspace.ReviewActor{}) || v.LocalRole != "reviewer" {
					t.Fatal("local persona fabricated identity", v)
				}
			}
			after, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
			now, _ := c.space.Head()
			state, _ := reopened.GetFlow(context.Background(), f.ID)
			if !bytes.Equal(before, after) || now != head || n != calls.Load() || state.State != "uncertain" {
				t.Fatal("inspection changed flow/documents or dispatched", state)
			}
		})
	}
}
func TestFlowReviewRefusalAndUnreachableCommitNeverBecomeDecisions(t *testing.T) {
	for _, refusal := range []error{sharedspace.ErrStale, sharedspace.ErrDecided, sharedspace.ErrProposalNotFound} {
		t.Run(refusal.Error(), func(t *testing.T) {
			c, _ := workflowFixture(t, false, nil)
			f := startReviewFlow(t, c, false)
			c.space = reviewFaultSpace{SharedSpace: c.space, refuse: refusal}
			if _, e := c.Review(f.Proposal, "accept", "reviewer"); !errors.Is(e, refusal) {
				t.Fatal(e)
			}
			v, e := c.InspectFlowReview(context.Background(), f.ID)
			if e != nil || v.State != "pending" {
				t.Fatal("intent treated as decision", v, e)
			}
			c.space = reviewFaultSpace{SharedSpace: c.space, transform: func(ps []sharedspace.Proposal) []sharedspace.Proposal {
				for i := range ps {
					if ps[i].ID == f.Proposal {
						ps[i].Status = "accepted"
						ps[i].AcceptedCommit = ps[i].Commit
					}
				}
				return ps
			}}
			v, e = c.InspectFlowReview(context.Background(), f.ID)
			if e != nil || v.State != "uncertain" {
				t.Fatal("unreachable accepted checkpoint trusted", v, e)
			}
		})
	}
}
func TestFlowReviewRejectEvidenceAndAppliedHistory(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "verified"}[verified], func(t *testing.T) {
			c, _ := workflowFixture(t, false, nil)
			ctx := context.Background()
			if verified {
				ctx = configureReviewIdentity(t, c)
			}
			f := startReviewFlow(t, c, verified)
			if _, e := c.ReviewContext(ctx, f.Proposal, "reject", "reviewer"); e != nil {
				t.Fatal(e)
			}
			v, e := c.InspectFlowReview(context.Background(), f.ID)
			want := "uncertain"
			if verified {
				want = "rejected"
			}
			if e != nil || v.State != want || !v.Applied {
				t.Fatal("reject identity evidence invented/lost", v, e)
			}
			if !verified && v.Reviewer != (sharedspace.ReviewActor{}) {
				t.Fatal("unattributed reject gained identity", v)
			}
		})
	}
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	first := f.Proposal
	if _, e := c.Review(first, "accept", "reviewer"); e != nil {
		t.Fatal(e)
	}
	waitWorkflow(t, c, f.ID, "review")
	v, e := c.InspectFlowReview(context.Background(), f.ID)
	if e != nil || v.State != "accepted" || v.Proposal != first || !v.Applied {
		t.Fatal("advance overwrote previous applied decision", v, e)
	}
}
func TestFlowReviewIntentFailureNeverCallsStoreOrBecomesAuthority(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	if _, e := c.Review(f.Proposal, "approve", "reviewer"); e == nil {
		t.Fatal("invalid review action accepted")
	}
	if len(savedReviews(t, c)) != 0 {
		t.Fatal("invalid action recorded intent")
	}
	if _, e := c.Review(f.Proposal, "accept", "GM"); e == nil {
		t.Fatal("wrong gate role accepted")
	}
	if len(savedReviews(t, c)) != 0 {
		t.Fatal("wrong role recorded intent")
	}
	calls := 0
	c.space = reviewFaultSpace{SharedSpace: c.space, before: func() { calls++ }}
	oldSync := c.syncDirectory
	failDirectorySync(c)
	if _, e := c.Review(f.Proposal, "accept", "reviewer"); e == nil {
		t.Fatal("failed intent save hidden")
	}
	if calls != 0 {
		t.Fatal("store called despite failed intent save")
	}
	c.mu.Lock()
	c.syncDirectory = oldSync
	e := c.saveLocked()
	c.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.InspectFlowReview(context.Background(), f.ID)
	if e != nil || v.State != "pending" || v.Applied {
		t.Fatal("later persisted failed intent became decision", v, e)
	}
}
func TestFlowReviewRequestChangesRetainsFeedbackEvidence(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	ctx := configureReviewIdentity(t, c)
	f := startReviewFlow(t, c, true)
	if _, e := c.RequestFlowChanges(ctx, FlowChanges{Run: f.ID, Proposal: f.Proposal, RequestKey: "feedback-1", Role: "reviewer", Feedback: "Clarify the pilot."}); e != nil {
		t.Fatal(e)
	}
	waitWorkflow(t, c, f.ID, "review")
	v, e := c.InspectFlowReview(context.Background(), f.ID)
	if e != nil || v.State != "rejected" || !v.Applied || v.Reviewer.Subject != "acceptor" {
		t.Fatal(v, e)
	}
	rs := savedReviews(t, c)
	if len(rs) != 1 || rs[0]["requestKey"] != "feedback-1" || rs[0]["feedback"] != "Clarify the pilot." || rs[0]["digest"] == "" {
		t.Fatal("request changes cause lost", rs)
	}
}

func TestFlowReviewBindingsReadOrderAndLegacy(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	v, e := c.InspectFlowReview(context.Background(), f.ID)
	if e != nil || v.State != "unavailable" {
		t.Fatal(v, e)
	}
	if _, e = c.InspectFlowReview(context.Background(), "unknown"); e == nil {
		t.Fatal("unknown flow accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.InspectFlowReview(ctx, f.ID); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	c.space = reviewFaultSpace{SharedSpace: c.space, after: true}
	if _, e = c.Review(f.Proposal, "accept", "reviewer"); e == nil {
		t.Fatal("fault not observed")
	}
	base := c.space
	for _, field := range []string{"base", "session", "reviewer", "changes"} {
		t.Run(field, func(t *testing.T) {
			c.space = reviewFaultSpace{SharedSpace: base, transform: func(ps []sharedspace.Proposal) []sharedspace.Proposal {
				for i := range ps {
					if ps[i].ID != f.Proposal {
						continue
					}
					switch field {
					case "base":
						ps[i].Base = "foreign"
					case "session":
						ps[i].Attribution.Session = "foreign"
					case "reviewer":
						ps[i].Reviewer = sharedspace.ReviewActor{Issuer: "foreign", Subject: "foreign", Role: "reviewer"}
					case "changes":
						ps[i].Changes = map[string]string{"prd.json": "not json"}
					}
				}
				return ps
			}}
			v, e := c.InspectFlowReview(context.Background(), f.ID)
			if e != nil || v.State != "uncertain" {
				t.Fatal("foreign evidence trusted", v, e)
			}
		})
	}
	var order []string
	c.space = reviewFaultSpace{SharedSpace: base, order: &order}
	v, e = c.InspectFlowReview(context.Background(), f.ID)
	if e != nil || v.State != "accepted" || len(order) != 3 || order[0] != "proposals" || order[1] != "history" || order[2] != "head" {
		t.Fatal("inconsistent observation order", v, order, e)
	}
}
