package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func acceptedContinuationEdit(t *testing.T, c *Canvas, fid, key string) EditRun {
	t.Helper()
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, fid, key))
	if err != nil {
		t.Fatal(err)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	if _, err = c.Review(r.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	return r
}

type blockedFirstBinder struct {
	once             sync.Once
	entered, release chan struct{}
}

func (b *blockedFirstBinder) Resolve(bundle contract.Bundle) (contract.Binding, error) {
	first := false
	b.once.Do(func() { first = true; close(b.entered); <-b.release })
	if first {
		return contract.Binding{}, errors.New("local binding failed before Create")
	}
	return (contract.LocalBinder{}).Resolve(bundle)
}

// Refusing all old attempts would over-fence both owned END and real local
// failure before Create. Neither case needs another native execution to settle.
func TestEditBoundaryRerunAllowsSettledOrFinalQueued(t *testing.T) {
	for _, kind := range []string{"owned-end", "final-queued"} {
		t.Run(kind, func(t *testing.T) {
			c, calls := workflowFixture(t, false, func(in map[string]any) string {
				if in["edit"] != nil {
					return changeEnvelope("prd", validPRD)
				}
				return ""
			})
			var f FlowRun
			if kind == "final-queued" {
				b := &blockedFirstBinder{entered: make(chan struct{}), release: make(chan struct{})}
				c.binder = b
				f = startWorkflow(t, c, "boundary-queued-flow", 12)
				<-b.entered
				r, err := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "boundary-queued-edit"))
				close(b.release)
				if err != nil {
					t.Fatal(err)
				}
				r = waitEdit(t, c, r.ID, "proposal")
				c.wg.Wait()
				old := readPublishedLedger(t, c).Attempts[0]
				if old.Status != "failed" || old.FlowStage != "queued" || old.Session != "" {
					t.Fatal("not final local nondispatch", old)
				}
				if _, err = c.Review(r.Proposal, "accept", "GM"); err != nil {
					t.Fatal(err)
				}
				count := calls.Load()
				if _, err = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "boundary-queued-rerun")); err != nil {
					t.Fatal(err)
				}
				waitWorkflow(t, c, f.ID, "review")
				c.wg.Wait()
				if calls.Load() != count+1 {
					t.Fatal("rerun did not execute exactly once", calls.Load(), count)
				}
			} else {
				f = startWorkflow(t, c, "boundary-settled-flow", 12)
				waitWorkflow(t, c, f.ID, "review")
				c.wg.Wait()
				old := readPublishedLedger(t, c).Attempts[0]
				n, err := c.readFlowExecution(context.Background(), old)
				if err != nil || !n.Settled {
					t.Fatal("no owned END", n, err)
				}
				r := acceptedContinuationEdit(t, c, f.ID, "boundary-settled-edit")
				if _, err = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "boundary-settled-rerun")); err != nil {
					t.Fatal(err)
				}
				waitWorkflow(t, c, f.ID, "review")
				c.wg.Wait()
			}
		})
	}
}

// The intent save precedes the generation save. A late old worker's real
// claims must still fail at that ready-state boundary, without relying on a
// worker counter or on a guessed native outcome.
func TestEditBoundaryLateClaimAtReadyGap(t *testing.T) {
	c, _ := workflowFixture(t, false, func(in map[string]any) string {
		if in["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	b := &blockedFirstBinder{entered: make(chan struct{}), release: make(chan struct{})}
	c.binder = b
	f := startWorkflow(t, c, "boundary-late-flow", 12)
	<-b.entered
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "boundary-late-edit"))
	close(b.release)
	if err != nil {
		t.Fatal(err)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	if _, err = c.Review(r.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	checked := false
	c.space = flowAdmissionHeadHook{c.space, func() {
		c.mu.Lock()
		ready := c.flowLocked(f.ID).State == "ready"
		c.mu.Unlock()
		if !ready || checked {
			return
		}
		checked = true
		for _, stage := range []string{"create-intent", "exec-intent"} {
			allowed := false
			if err := c.update(f.Attempt, func(a *attempt) { allowed = c.claimFlowDispatchStageLocked(a, stage) }); err != nil {
				t.Fatal(err)
			}
			if allowed {
				t.Fatal("late old claim dispatched in ready gap", stage)
			}
		}
	}}
	if _, err = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "boundary-late-rerun")); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if !checked {
		t.Fatal("ready-before-generation boundary not exercised")
	}
}

func TestEditBoundaryResumeReleaseSyncFailure(t *testing.T) {
	c := editRecoveryFlow(t)
	fid := c.data.Flows[0].ID
	r := rejectedRecoveryEdit(t, c, fid, "boundary-resume-sync-edit")
	q := editRecoveryCommand(t, c, r.ID, "boundary-resume-sync-key")
	c.syncDirectory = func(root string) error {
		disk := readPublishedLedger(t, c)
		if disk.Edits[0].Commands["resume"] != "" && !disk.Edits[0].ResumePending {
			return errors.New("release rename visible but directory fsync uncertain")
		}
		return syncCanvasDirectory(root)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.ResumeAfterEdit(context.Background(), q); err == nil {
			t.Fatal("uncertain release acknowledged", i)
		}
		c.mu.Lock()
		pending := c.editLocked(r.ID).ResumePending
		pause := c.flowEditPauseLocked(c.flowRecordLocked(fid))
		state := c.flowLocked(fid).State
		c.mu.Unlock()
		if !pending || pause != "edit-paused" || state != "edit-paused" {
			t.Fatal("uncertain release lost effective pause", pending, pause, state)
		}
		if readPublishedLedger(t, c).Edits[0].Commands["resume"] == "" {
			t.Fatal("visible intent lost")
		}
	}
	c.syncDirectory = nil
	if out, err := c.ResumeAfterEdit(context.Background(), q); err != nil || out.State != "review" {
		t.Fatal(out, err)
	}
	c.Close()
	reopened, err := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	out, err := reopened.GetFlow(context.Background(), fid)
	if err != nil || out.State != "uncertain" || reopened.editLocked(r.ID).ResumePending || reopened.editLocked(r.ID).Commands["resume"] == "" {
		t.Fatal("release not durable", out, err)
	}
}

func TestEditBoundaryCompletedCommandsRejectChangedBinding(t *testing.T) {
	for _, kind := range []string{"resume", "rerun"} {
		t.Run(kind, func(t *testing.T) {
			c := editRecoveryFlow(t)
			fid := c.data.Flows[0].ID
			var r EditRun
			invoke := c.ResumeAfterEdit
			if kind == "resume" {
				r = rejectedRecoveryEdit(t, c, fid, "boundary-binding-edit")
			} else {
				r = acceptedContinuationEdit(t, c, fid, "boundary-binding-edit")
				invoke = c.RerunAfterEdit
			}
			q := editRecoveryCommand(t, c, r.ID, "boundary-binding-command")
			if _, err := invoke(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			before := readPublishedLedger(t, c)
			head := mustHead(t, c)
			for _, change := range []func(*EditMaintenance){
				func(q *EditMaintenance) { q.Edit = "foreign-edit" },
				func(q *EditMaintenance) { q.RequestKey = "changed-request-key" },
				func(q *EditMaintenance) { q.ExpectedHead = "changed-payload" },
				func(q *EditMaintenance) { q.Role = "owner" },
				func(q *EditMaintenance) { q.Confirmed = false },
			} {
				other := q
				change(&other)
				if _, err := invoke(context.Background(), other); err == nil {
					t.Fatal("changed completed command acknowledged", other)
				}
			}
			if !reflect.DeepEqual(before, readPublishedLedger(t, c)) || mustHead(t, c) != head {
				t.Fatal("refused command mutated state")
			}
		})
	}
}

func TestEditBoundaryRerunReservationFailureRetainsPending(t *testing.T) {
	c, calls := workflowFixture(t, false, func(in map[string]any) string {
		if in["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "boundary-reservation-flow", 12)
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	fid := f.ID
	r := acceptedContinuationEdit(t, c, fid, "boundary-reservation-edit")
	q := editRecoveryCommand(t, c, r.ID, "boundary-reservation-rerun")
	count := calls.Load()
	// The real intent rename succeeds. Block only the following reservation's
	// temp-file open, leaving its failed RAM mutations observable to exact retry.
	c.syncDirectory = func(root string) error {
		raw, err := os.ReadFile(filepath.Join(root, "attempts.json"))
		if err != nil {
			return err
		}
		var disk ledger
		if err = json.Unmarshal(raw, &disk); err != nil {
			return err
		}
		if disk.Edits[0].RerunPending && disk.Flows[0].State == "ready" {
			if err = os.Mkdir(filepath.Join(root, "attempts.json.tmp"), 0700); err != nil {
				return err
			}
		}
		return syncCanvasDirectory(root)
	}
	defer os.Remove(filepath.Join(c.root, "attempts.json.tmp"))
	if _, err := c.RerunAfterEdit(context.Background(), q); err == nil {
		t.Fatal("reservation unexpectedly succeeded")
	}
	c.mu.Lock()
	pending := c.editLocked(r.ID).RerunPending
	state := c.flowLocked(fid).State
	attempts := len(c.data.Attempts)
	c.mu.Unlock()
	if !pending || state != "uncertain" {
		t.Fatal("failed reservation looked completed", pending, state)
	}
	if _, err := c.RerunAfterEdit(context.Background(), q); err == nil {
		t.Fatal("exact retry falsely acknowledged failed reservation")
	}
	c.wg.Wait()
	c.mu.Lock()
	after := len(c.data.Attempts)
	c.mu.Unlock()
	if attempts != after || calls.Load() != count {
		t.Fatal("failed continuation reserved or dispatched more work", attempts, after, count, calls.Load())
	}
}

// The native session exists, but the lost Create reply leaves no UID with
// which Canvas can prove the old RPC outcome.
func TestEditBoundaryRerunRefusesUnknownCreate(t *testing.T) {
	c, calls := workflowFixture(t, false, func(in map[string]any) string {
		if in["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var creates atomic.Int32
	var nativeUID string
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := next(ctx, m, req, reply, cc, opts...)
		if err == nil && m == v1.Sessions_CreateSession_FullMethodName && creates.Add(1) == 1 {
			nativeUID = reply.(*v1.Session).GetMetadata().GetUid()
			close(entered)
			<-release
			return status.Error(codes.Unavailable, "Create committed; reply lost")
		}
		return err
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	f := startWorkflow(t, c, "boundary-unknown-create", 12)
	<-entered
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "boundary-unknown-create-edit"))
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	old := readPublishedLedger(t, c).Attempts[0]
	if old.Session != "" || old.FlowStage != "create-intent" || old.Status != "failed" {
		t.Fatal("not unknown Create", old)
	}
	if s, err := cl.GetSession(context.Background(), nativeUID); err != nil || s.GetMetadata().GetUid() != nativeUID {
		t.Fatal("Create did not commit", s, err)
	}
	if _, err = c.Review(r.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	before, count, head := readPublishedLedger(t, c), calls.Load(), mustHead(t, c)
	if _, err = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "boundary-unknown-create-rerun")); !errors.Is(err, ErrEditConflict) {
		t.Fatal("unknown Create crossed settlement guard", err)
	}
	c.wg.Wait()
	if !reflect.DeepEqual(before, readPublishedLedger(t, c)) || calls.Load() != count || mustHead(t, c) != head {
		t.Fatal("unknown Create refusal mutated or dispatched")
	}
}

func suppressedContinuationFlow(t *testing.T) (*Canvas, *atomic.Int32, FlowRun, EditRun) {
	t.Helper()
	c, calls := workflowFixture(t, false, func(in map[string]any) string {
		if in["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var creates atomic.Int32
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := next(ctx, m, req, reply, cc, opts...)
		if err == nil && m == v1.Sessions_CreateSession_FullMethodName && creates.Add(1) == 1 {
			close(entered)
			<-release
		}
		return err
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	c.client = cl
	f := startWorkflow(t, c, "boundary-suppressed-sync-flow", 12)
	<-entered
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "boundary-suppressed-sync-edit"))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err = c.AbandonEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "boundary-suppressed-abandon")); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	c.wg.Wait()
	old := readPublishedLedger(t, c).Attempts[0]
	if old.FlowStage != "edit-suppressed" || old.Status != "failed" {
		t.Fatal("not proven pre-Exec suppression", old)
	}
	return c, calls, f, r
}

func TestEditBoundarySuppressedResumeReleaseSyncRetry(t *testing.T) {
	c, calls, f, r := suppressedContinuationFlow(t)
	q := editRecoveryCommand(t, c, r.ID, "boundary-suppressed-sync-resume")
	c.syncDirectory = func(root string) error {
		disk := readPublishedLedger(t, c)
		if disk.Edits[0].Commands["resume"] != "" && !disk.Edits[0].ResumePending {
			return errors.New("release postrename fsync uncertainty")
		}
		return syncCanvasDirectory(root)
	}
	if _, err := c.ResumeAfterEdit(context.Background(), q); err == nil {
		t.Fatal("uncertain release acknowledged")
	}
	c.mu.Lock()
	pending := c.editLocked(r.ID).ResumePending
	state := c.flowLocked(f.ID).State
	c.mu.Unlock()
	if !pending || state != "edit-paused" {
		t.Fatal("effective pause lost", pending, state)
	}
	c.syncDirectory = nil
	if out, err := c.ResumeAfterEdit(context.Background(), q); err != nil || out.State != "paused" {
		t.Fatal("exact nondispatch retry stranded", out, err)
	}
	disk := readPublishedLedger(t, c)
	if disk.Edits[0].ResumePending || disk.Attempts[0].Status != "failed" || disk.Attempts[0].FlowStage != "completed" || calls.Load() != 0 {
		t.Fatal("retry did not durably finalize without native work", disk, calls.Load())
	}
}

type lostEndReplyStream struct {
	grpc.ClientStream
	entered, release chan struct{}
}

func (s *lostEndReplyStream) RecvMsg(message any) error {
	err := s.ClientStream.RecvMsg(message)
	if update, ok := message.(*v1.ExecUpdate); err == nil && ok && update.GetRecord().GetEvent().GetKind() == v1.EventKind_EVENT_END {
		close(s.entered)
		<-s.release
		return status.Error(codes.Canceled, "native END committed; reply lost")
	}
	return err
}
func TestEditBoundaryRerunSettlesLocallyUncertainOwnedEND(t *testing.T) {
	c, _ := workflowFixture(t, false, func(in map[string]any) string {
		if in["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var execs atomic.Int32
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, m string, next grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := next(ctx, desc, cc, m, opts...)
		if err == nil && m == v1.Sessions_Exec_FullMethodName && execs.Add(1) == 1 {
			return &lostEndReplyStream{stream, entered, release}, nil
		}
		return stream, err
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	f := startWorkflow(t, c, "boundary-lost-end-flow", 12)
	<-entered
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "boundary-lost-end-edit"))
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	old := readPublishedLedger(t, c).Attempts[0]
	if old.Status != "uncertain" || old.FlowStage != "exec-intent" {
		t.Fatal("not locally uncertain Exec", old)
	}
	if n, err := c.readFlowExecution(context.Background(), old); err != nil || !n.Settled {
		t.Fatal("no real owned END", n, err)
	}
	if _, err = c.Review(r.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "boundary-lost-end-rerun")); err != nil {
		t.Fatal(err)
	}
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	settled := readPublishedLedger(t, c).Attempts[0]
	if settled.Status != "failed" || settled.FlowStage != "completed" || settled.Proposal != "" {
		t.Fatal("owned END left old admission journal unresolved", settled)
	}
	if _, err = c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); err != nil {
		t.Fatal(err)
	}
	next, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "boundary-after-lost-end", Role: "GM", Inputs: json.RawMessage(`{"topic":"Next fictional pilot"}`), AgentRuns: 12})
	if err != nil {
		t.Fatal("old uncertain journal stranded future admission", err)
	}
	waitWorkflow(t, c, next.ID, "review")
	c.wg.Wait()
}

func TestEditBoundaryResumeThenNewEditRerunRetainsNondispatchSettlement(t *testing.T) {
	c, calls, f, r := suppressedContinuationFlow(t)
	q := editRecoveryCommand(t, c, r.ID, "review-resume-before-next-edit")
	if out, err := c.ResumeAfterEdit(context.Background(), q); err != nil || out.State != "paused" {
		t.Fatal(out, err)
	}
	old := readPublishedLedger(t, c).Attempts[0]
	if old.Status != "failed" || old.FlowStage != "completed" {
		t.Fatal("Resume did not settle suppression", old)
	}
	native, err := c.client.GetSession(context.Background(), old.Session)
	if err != nil || native.GetLastSeq() != 0 {
		t.Fatal("unexpected native execution", native, err)
	}
	next := acceptedContinuationEdit(t, c, f.ID, "review-next-accepted-edit")
	count := calls.Load()
	if _, err = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, next.ID, "review-next-edit-rerun")); err != nil {
		t.Fatal("completed nondispatch settlement stranded later rerun", err)
	}
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	disk := readPublishedLedger(t, c)
	if calls.Load() != count+1 || len(disk.Flows[0].EditCheckpoints) != 1 || disk.Flows[0].Generation != 2 {
		t.Fatal("rerun did not reserve exactly one restart", calls.Load(), count, disk.Flows[0])
	}
}

func TestEditBoundaryNondispatchRerunIntentSyncRetry(t *testing.T) {
	c, calls := workflowFixture(t, false, func(in map[string]any) string {
		if in["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	b := &blockedFirstBinder{entered: make(chan struct{}), release: make(chan struct{})}
	c.binder = b
	f := startWorkflow(t, c, "review-nondispatch-rerun-flow", 12)
	<-b.entered
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "review-nondispatch-rerun-edit"))
	close(b.release)
	if err != nil {
		t.Fatal(err)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	old := readPublishedLedger(t, c).Attempts[0]
	if old.Status != "failed" || old.FlowStage != "queued" || old.Session != "" {
		t.Fatal("not final nondispatch", old)
	}
	if _, err = c.Review(r.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	q := editRecoveryCommand(t, c, r.ID, "review-nondispatch-rerun-command")
	count := calls.Load()
	c.syncDirectory = func(root string) error {
		disk := readPublishedLedger(t, c)
		if disk.Edits[0].RerunPending && disk.Flows[0].Generation == 1 {
			return errors.New("rerun intent renamed; directory fsync uncertain")
		}
		return syncCanvasDirectory(root)
	}
	if _, err = c.RerunAfterEdit(context.Background(), q); err == nil {
		t.Fatal("uncertain rerun intent acknowledged")
	}
	before := readPublishedLedger(t, c)
	if !before.Edits[0].RerunPending || before.Attempts[0].FlowStage != "completed" || calls.Load() != count || before.Flows[0].Generation != 1 {
		t.Fatal("intent did not retain settled old attempt without dispatch", before, calls.Load())
	}
	c.syncDirectory = nil
	if _, err = c.RerunAfterEdit(context.Background(), q); err != nil {
		t.Fatal("exact pending rerun lost nondispatch settlement", err)
	}
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	disk := readPublishedLedger(t, c)
	if calls.Load() != count+1 || disk.Edits[0].RerunPending || len(disk.Flows[0].EditCheckpoints) != 1 || disk.Flows[0].Generation != 2 || disk.Flows[0].Attempts != 3 || disk.Attempts[0].Error != old.Error {
		t.Fatal("retry duplicated work or lost original failure context", disk.Flows[0], disk.Attempts[0], calls.Load(), count)
	}
	if _, err = c.RerunAfterEdit(context.Background(), q); err != nil || calls.Load() != count+1 {
		t.Fatal("completed retry redispatched", err, calls.Load())
	}
}

func TestEditBoundaryResumeCommittedReleaseIgnoresRequestCancellation(t *testing.T) {
	c := editRecoveryFlow(t)
	fid := c.data.Flows[0].ID
	r := rejectedRecoveryEdit(t, c, fid, "review-resume-cancel-edit")
	q := editRecoveryCommand(t, c, r.ID, "review-resume-cancel-command")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := false
	c.syncDirectory = func(root string) error {
		err := syncCanvasDirectory(root)
		disk := readPublishedLedger(t, c)
		if err == nil && disk.Edits[0].Commands["resume"] != "" && !disk.Edits[0].ResumePending {
			committed = true
			cancel()
		}
		return err
	}
	out, err := c.ResumeAfterEdit(ctx, q)
	if !committed || ctx.Err() == nil {
		t.Fatal("cancel did not occur after committed release")
	}
	if err != nil || out.State != "review" {
		t.Fatal("request cancellation undid committed release", out, err)
	}
	disk := readPublishedLedger(t, c)
	if disk.Edits[0].ResumePending || disk.Flows[0].State != "review" || disk.Edits[0].Commands["resume"] == "" {
		t.Fatal("durable completion rolled back", disk.Flows[0], disk.Edits[0])
	}
	c.syncDirectory = nil
	if replay, err := c.ResumeAfterEdit(context.Background(), q); err != nil || replay.State != "review" {
		t.Fatal("exact completed replay failed", replay, err)
	}
}
