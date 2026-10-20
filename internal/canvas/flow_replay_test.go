package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

// Real Git and native profile-4 executions: removing any coverage or binding
// check must turn one of these complete histories into a false positive.
func replayCompleteFlow(t *testing.T) (*Canvas, FlowRun, *atomic.Int32) {
	t.Helper()
	c, calls := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "replay", 12)
	for range 3 {
		f = waitWorkflow(t, c, f.ID, "review")
		if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
			t.Fatal(err)
		}
	}
	return c, waitWorkflow(t, c, f.ID, "complete"), calls
}

func replayRefs(t *testing.T, c *Canvas) []byte {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+filepath.Join(c.space.Root(), ".sharedspace", "git"), "show-ref")
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFlowReplayCompleteReadOnlyAndDetached(t *testing.T) {
	c, f, calls := replayCompleteFlow(t)
	before, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	refs := replayRefs(t, c)
	count := calls.Load()
	p, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || !p.Complete || p.Digest == "" || len(p.Sessions) != 3 || len(p.Reviews) != 3 {
		t.Fatalf("aggregate: %+v %v", p, err)
	}
	for _, s := range p.Sessions {
		if s.Execution == "" || !s.Proof.ChainValid || !s.Proof.Reconstructed || !s.Proof.OutputsMatch || !s.Proof.LiveUnchanged || s.Proof.ModelCalls != 0 || s.Proof.ExecutorCalls == nil || *s.Proof.ExecutorCalls != 0 || s.Head != s.Proof.Head || len(s.Proof.ExecutionIDs) != 1 || s.Proof.ExecutionIDs[0] != s.Execution {
			t.Fatalf("missing exact native coverage: %+v", s)
		}
	}
	for _, r := range p.Reviews {
		if r.State != "accepted" || r.Reviewer != (sharedspace.ReviewActor{}) || r.LocalRole != "reviewer" {
			t.Fatalf("invented human identity: %+v", r)
		}
	}
	digest := p.Digest
	p.Sessions[0].Proof.ExecutionIDs[0] = "mutated"
	*p.Sessions[0].Proof.ExecutorCalls = 42
	p.Reviews[0].LocalRole = "mutated"
	again, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || !again.Complete || again.Digest != digest || again.Sessions[0].Proof.ExecutionIDs[0] == "mutated" {
		t.Fatalf("aliased/unstable proof: %+v %v", again, err)
	}
	after, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if !bytes.Equal(before, after) || !bytes.Equal(refs, replayRefs(t, c)) || calls.Load() != count {
		t.Fatal("replay mutated ledger/refs or invoked provider")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.data.Attempts {
		if a.Proof != nil {
			t.Fatal("persisted native proof")
		}
	}
}

func TestFlowReplayMalformedHistoryIsIncomplete(t *testing.T) {
	c, f, _ := replayCompleteFlow(t)
	c.mu.Lock()
	original := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	attempts, _ := json.Marshal(c.data.Attempts)
	c.mu.Unlock()
	tests := []struct {
		name   string
		change func(*flowRunRecord)
	}{
		{"legacy", func(r *flowRunRecord) { r.Generation = 0; r.Generations = nil; r.Reviews = nil }},
		{"generation gap", func(r *flowRunRecord) { r.Generations[1].Number = 9 }},
		{"wrong predecessor", func(r *flowRunRecord) { r.Generations[1].Previous = 0 }},
		{"duplicate attempt", func(r *flowRunRecord) { r.Generations[1].Attempt = r.Generations[0].Attempt }},
		{"budget", func(r *flowRunRecord) { r.AgentRuns = 2 }},
		{"counter", func(r *flowRunRecord) { r.Attempts++ }},
		{"review generation", func(r *flowRunRecord) { r.Reviews[0].Generation = 3 }},
		{"review order", func(r *flowRunRecord) { r.Reviews[0], r.Reviews[1] = r.Reviews[1], r.Reviews[0] }},
		{"review wrong base", func(r *flowRunRecord) { r.Reviews[0].Base = r.StartBase }},
		{"missing decision", func(r *flowRunRecord) { r.Reviews = r.Reviews[:2] }},
		{"unapplied intent", func(r *flowRunRecord) { r.Reviews[0].Applied = false }},
		{"wrong accepted binding", func(r *flowRunRecord) { r.Accepted["prd"] = r.Reviews[0].Proposal }},
		{"wrong accepted checkpoint", func(r *flowRunRecord) { r.Reviews[0].AcceptedCommit = r.StartBase }},
		{"incomplete position", func(r *flowRunRecord) { r.Position = 2 }},
		{"unlisted attempt", func(r *flowRunRecord) {
			a := c.data.Attempts[0]
			a.ID = "extra"
			c.data.Attempts = append(c.data.Attempts, a)
		}},
		{"wrong native input", func(r *flowRunRecord) { c.data.Attempts[0].Input += "forged" }},
		{"cached proof not authority", func(r *flowRunRecord) {
			c.data.Attempts[0].Session = "missing"
			zero := 0
			c.data.Attempts[0].Proof = &sessionclient.Proof{ChainValid: true, Reconstructed: true, OutputsMatch: true, LiveUnchanged: true, ExecutorCalls: &zero}
		}},
		{"unknown create", func(r *flowRunRecord) {
			c.data.Attempts[0].Session = ""
			c.data.Attempts[0].FlowStage = "create-intent"
		}},
	}
	// The first attempt's base really is StartBase; corrupt a later base instead.
	tests[8].change = func(r *flowRunRecord) { r.Reviews[1].Base = r.StartBase }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.mu.Lock()
			*c.flowRecordLocked(f.ID) = cloneFlowRecord(original)
			if err := json.Unmarshal(attempts, &c.data.Attempts); err != nil {
				t.Fatal(err)
			}
			tt.change(c.flowRecordLocked(f.ID))
			c.mu.Unlock()
			p, err := c.VerifyFlow(context.Background(), f.ID)
			if err != nil || p.Complete || p.Reason == "" {
				t.Fatalf("malformed history passed: %+v %v", p, err)
			}
		})
	}
}

func TestFlowReplayActiveAndLocalRejectionRemainIncomplete(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "active", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	p, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete || !p.RecordOnly {
		t.Fatalf("active claimed complete: %+v %v", p, err)
	}
	if _, err = c.Review(f.Proposal, "reject", "reviewer"); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "rejected")
	p, err = c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete {
		t.Fatalf("unattributed rejection passed: %+v %v", p, err)
	}
}

func TestFlowReplayRemoteRefusesBeforePlaintext(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	c.remoteSessions = true
	c.address = "not-a-diagnostics-address"
	if _, err := c.VerifyFlow(context.Background(), "anything"); !errors.Is(err, errRemoteDiagnostics) {
		t.Fatal("remote attempted plaintext fallback", err)
	}
}

func TestFlowReplayTerminalActionNeedsActualConclusionAndCoverage(t *testing.T) {
	c, f := readyFlowAction(t)
	p, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete {
		t.Fatalf("pending action passed: %+v %v", p, err)
	}
	v, err := c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v, err = c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "complete")
	before, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	p, err = c.VerifyFlow(context.Background(), f.ID)
	if err != nil || !p.Complete || len(p.Sessions) != 4 || p.Sessions[3].Kind != "action" || p.Sessions[3].Proof.ExecutorCalls == nil || *p.Sessions[3].Proof.ExecutorCalls != 0 {
		t.Fatalf("action native coverage missing: %+v %v", p, err)
	}
	after, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("action replay saved state")
	}
	c.mu.Lock()
	c.data.Announcements[0].FlowGeneration++
	c.mu.Unlock()
	p, err = c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete {
		t.Fatalf("foreign action generation passed: %+v %v", p, err)
	}
}

func TestFlowReplayForeignAcceptedHeadIsIncomplete(t *testing.T) {
	c, f, _ := replayCompleteFlow(t)
	// Move the accepted ref to a real ancestor; proposal objects still exist.
	cmd := exec.Command("git", "--git-dir="+filepath.Join(c.space.Root(), ".sharedspace", "git"), "update-ref", "refs/heads/main", f.StartBase)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	p, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete {
		t.Fatalf("unreachable checkpoint passed: %+v %v", p, err)
	}
}

// Only the external diagnostics boundary is intercepted; the response starts as
// a real native reconstruction and all Git/native execution reads remain real.
func replayDiagnosticProxy(t *testing.T, c *Canvas, fault func(*structpb.Struct) error) {
	t.Helper()
	address := c.address
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		req, reply := new(structpb.Struct), new(structpb.Struct)
		if err := stream.RecvMsg(req); err != nil {
			return err
		}
		if err := conn.Invoke(stream.Context(), "/canvas.local.Diagnostics/Verify", req, reply); err != nil {
			return err
		}
		if err := fault(reply); err != nil {
			return err
		}
		return stream.SendMsg(reply)
	}))
	go server.Serve(listener)
	c.address = listener.Addr().String()
	t.Cleanup(func() { c.address = address; server.Stop(); listener.Close(); conn.Close() })
}

func TestFlowReplayDiagnosticsMustCoverExactExecutionWithoutCalls(t *testing.T) {
	c, f, _ := replayCompleteFlow(t)
	cases := []struct {
		name  string
		fault func(*structpb.Struct) error
	}{
		{"unavailable", func(p *structpb.Struct) error { return status.Error(codes.Unimplemented, "diagnostics disabled") }},
		{"legacy", func(p *structpb.Struct) error {
			delete(p.Fields, "executorCalls")
			delete(p.Fields, "executionIDs")
			return nil
		}},
		{"executor invoked", func(p *structpb.Struct) error { p.Fields["executorCalls"] = structpb.NewNumberValue(1); return nil }},
		{"model invoked", func(p *structpb.Struct) error { p.Fields["modelCalls"] = structpb.NewNumberValue(1); return nil }},
		{"wrong execution", func(p *structpb.Struct) error {
			p.Fields["executionIDs"] = structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{structpb.NewStringValue("foreign")}})
			return nil
		}},
		{"foreign extra execution", func(p *structpb.Struct) error {
			p.Fields["executionIDs"].GetListValue().Values = append(p.Fields["executionIDs"].GetListValue().Values, structpb.NewStringValue("foreign"))
			return nil
		}},
		{"wrong proof head", func(p *structpb.Struct) error { p.Fields["head"] = structpb.NewStringValue("1"); return nil }},
		{"reconstruction false", func(p *structpb.Struct) error { p.Fields["reconstructed"] = structpb.NewBoolValue(false); return nil }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			replayDiagnosticProxy(t, c, tt.fault)
			p, err := c.VerifyFlow(context.Background(), f.ID)
			if err != nil || p.Complete || p.Reason == "" {
				t.Fatalf("diagnostic fault passed: %+v %v", p, err)
			}
		})
	}
}

func TestFlowReplayMovingNativeHeadAndWorkerRecordAreIncomplete(t *testing.T) {
	for _, which := range []string{"native head", "worker record", "git refs"} {
		t.Run(which, func(t *testing.T) {
			c, f, _ := replayCompleteFlow(t)
			var changed atomic.Bool
			var forged atomic.Bool
			if which == "native head" {
				// Intercept a real Get response only after real diagnostics has run.
				cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
					e := next(ctx, m, req, reply, cc, opts...)
					if e == nil && changed.Load() {
						if s, ok := reply.(interface{ GetLastSeq() int64 }); ok && s.GetLastSeq() > 0 {
							// Set through protobuf reflection, leaving the service-owned
							// original and all other metadata untouched.
							if msg, ok := reply.(interface{ ProtoReflect() protoreflect.Message }); ok {
								fields := msg.ProtoReflect().Descriptor().Fields()
								if field := fields.ByName("last_seq"); field != nil {
									msg.ProtoReflect().Set(field, protoreflect.ValueOfInt64(s.GetLastSeq()+1))
									forged.Store(true)
								}
							}
						}
					}
					return e
				})))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cl.Close() })
				c.client = cl
			}
			replayDiagnosticProxy(t, c, func(p *structpb.Struct) error {
				if changed.CompareAndSwap(false, true) {
					if which == "worker record" {
						c.mu.Lock()
						c.findLocked(f.Attempt).FlowStage = "changed"
						c.mu.Unlock()
					}
					if which == "git refs" {
						cmd := exec.Command("git", "--git-dir="+filepath.Join(c.space.Root(), ".sharedspace", "git"), "update-ref", "refs/heads/foreign", f.StartBase)
						if b, e := cmd.CombinedOutput(); e != nil {
							return errors.New(string(b))
						}
					}
				}
				return nil
			})
			p, err := c.VerifyFlow(context.Background(), f.ID)
			if which == "native head" && !forged.Load() {
				t.Fatal("native head interception did not run")
			}
			if err != nil || p.Complete {
				t.Fatalf("moving evidence passed: %+v %v", p, err)
			}
		})
	}
}

func TestFlowReplayVerifiedRejectionAndExhaustedHistory(t *testing.T) {
	t.Run("verified rejection", func(t *testing.T) {
		c, _ := workflowFixture(t, false, nil)
		ctx := configureReviewIdentity(t, c)
		f := startReviewFlow(t, c, true)
		if _, err := c.ReviewContext(ctx, f.Proposal, "reject", "reviewer"); err != nil {
			t.Fatal(err)
		}
		f = waitWorkflow(t, c, f.ID, "rejected")
		// Authentication defaults to a remote host; this fixture owns a local native host.
		c.remoteSessions = false
		p, err := c.VerifyFlow(context.Background(), f.ID)
		if err != nil || !p.Complete || len(p.Sessions) != 1 || len(p.Reviews) != 1 || p.Reviews[0].Reviewer.Subject == "" {
			t.Fatalf("real rejection coverage: %+v %v", p, err)
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		c, _ := workflowFixture(t, false, nil)
		f := startWorkflow(t, c, "budget", 1)
		f = waitWorkflow(t, c, f.ID, "review")
		if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
			t.Fatal(err)
		}
		f = waitWorkflow(t, c, f.ID, "exhausted")
		p, err := c.VerifyFlow(context.Background(), f.ID)
		if err != nil || !p.Complete || len(p.Sessions) != 1 {
			t.Fatalf("budget coverage: %+v %v", p, err)
		}
	})
}

func TestFlowReplayCancelledHistoryAndKnownNoDispatchCoverage(t *testing.T) {
	t.Run("completed native cancellation", func(t *testing.T) {
		c, _ := workflowFixture(t, false, nil)
		f := startWorkflow(t, c, "cancel", 12)
		f = waitWorkflow(t, c, f.ID, "review")
		if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
			t.Fatal(err)
		}
		f = waitWorkflow(t, c, f.ID, "review")
		if _, err := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
			t.Fatal(err)
		}
		p, err := c.VerifyFlow(context.Background(), f.ID)
		if err != nil || !p.Complete || len(p.Sessions) != 2 {
			t.Fatalf("cancelled native coverage: %+v %v", p, err)
		}
	})
	t.Run("known no dispatch", func(t *testing.T) {
		c, _ := workflowFixture(t, false, nil)
		c.EnableAuthentication()
		configureTestService(t, c, sessionauth.Identity{Issuer: testIssuer, Subject: "wrong-service"})
		ctx := humanContext(t, "starter")
		f, err := c.StartFlow(ctx, FlowStart{Flow: "release", RequestKey: "no-dispatch", Role: "GM", Inputs: []byte(`{"topic":"x"}`), AgentRuns: 12})
		if err != nil {
			t.Fatal(err)
		}
		f = waitWorkflow(t, c, f.ID, "paused")
		if _, err := c.CancelFlow(ctx, FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
			t.Fatal(err)
		}
		// Explicitly enable diagnostics only for the fixture-owned local host.
		c.remoteSessions = false
		p, err := c.VerifyFlow(ctx, f.ID)
		if err != nil || p.Complete || len(p.Sessions) != 1 || p.Sessions[0].Coverage != "not-dispatched" {
			t.Fatalf("no dispatch invented native coverage: %+v %v", p, err)
		}
	})
}

func TestFlowReplayActualNativeOutputMustMatchAcceptedWholeProposal(t *testing.T) {
	c, f, _ := replayCompleteFlow(t)
	c.mu.Lock()
	record := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	last := record.Reviews[2]
	c.mu.Unlock()
	ps, err := c.space.Proposals()
	if err != nil {
		t.Fatal(err)
	}
	var old sharedspace.Proposal
	for _, p := range ps {
		if p.ID == last.Proposal {
			old = p
		}
	}
	if old.ID == "" {
		t.Fatal("last proposal missing")
	}
	// Build a different, schema-valid whole proposal at the exact native base.
	// Every Git review and private binding agrees; only native output differs.
	cmd := exec.Command("git", "--git-dir="+filepath.Join(c.space.Root(), ".sharedspace", "git"), "update-ref", "refs/heads/main", old.Base)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("%s: %v", b, e)
	}
	changes := maps.Clone(old.Changes)
	for path, text := range changes {
		changes[path] = text + "\n"
	}
	replacement, err := c.space.Propose(old.Base, changes, old.Attribution)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err = c.space.Accept(replacement.ID, old.Base, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	r := c.flowRecordLocked(f.ID)
	r.Reviews[2].Proposal = replacement.ID
	r.Reviews[2].AcceptedCommit = replacement.AcceptedCommit
	for name, proposal := range r.Accepted {
		if proposal == old.ID {
			r.Accepted[name] = replacement.ID
		}
	}
	c.findLocked(last.Attempt).Proposal = replacement.ID
	c.mu.Unlock()
	v, err := c.InspectFlowReview(context.Background(), f.ID)
	if err != nil || v.State != "accepted" {
		t.Fatalf("fixture did not retain valid Git evidence: %+v %v", v, err)
	}
	p, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete {
		t.Fatalf("accepted substituted output passed: %+v %v", p, err)
	}
}

func TestFlowReplayDeniedActionNeedsConcludedNativeDenial(t *testing.T) {
	c, f := readyFlowAction(t)
	v, err := c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", false, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "denied")
	p, err := c.VerifyFlow(context.Background(), f.ID)
	if err != nil || p.Complete {
		t.Fatalf("denial intent claimed concluded action: %+v %v", p, err)
	}
	v, err = c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	p, err = c.VerifyFlow(context.Background(), f.ID)
	// The pinned native host concludes denied cleanup without a completed
	// execution that diagnostics can reconstruct. Preserve that limitation;
	// the receipt/denial inspector alone cannot manufacture a replay proof.
	if err != nil || p.Complete || p.Reason == "" || len(p.Sessions) != 4 || p.Sessions[3].Coverage == "reconstructed" {
		t.Fatalf("unsupported denied replay claimed complete: %+v %v", p, err)
	}
}
