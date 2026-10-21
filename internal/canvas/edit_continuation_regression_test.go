package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func editRecoveryFlow(t *testing.T) *Canvas {
	t.Helper()
	c, _ := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "review-copilot-recovery", 12)
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	return c
}
func rejectedRecoveryEdit(t *testing.T, c *Canvas, fid, key string) EditRun {
	t.Helper()
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, fid, key))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	if _, e = c.Review(r.Proposal, "reject", "GM"); e != nil {
		t.Fatal(e)
	}
	waitWorkflow(t, c, fid, "edit-paused")
	return r
}
func editRecoveryCommand(t *testing.T, c *Canvas, id, key string) EditMaintenance {
	t.Helper()
	return EditMaintenance{Edit: id, Role: "GM", RequestKey: key, ExpectedHead: mustHead(t, c), Confirmed: true}
}

func TestEditCompletedResumeDoesNotReleaseLaterPause(t *testing.T) {
	c := editRecoveryFlow(t)
	fid := c.data.Flows[0].ID
	a := rejectedRecoveryEdit(t, c, fid, "first-rejected-edit")
	q := editRecoveryCommand(t, c, a.ID, "first-completed-resume")
	if f, e := c.ResumeAfterEdit(context.Background(), q); e != nil || f.State != "review" {
		t.Fatal(f, e)
	}
	b := rejectedRecoveryEdit(t, c, fid, "second-rejected-edit")
	f, e := c.ResumeAfterEdit(context.Background(), q)
	if e != nil || f.State != "edit-paused" {
		t.Fatal("completed Resume released a later edit pause", f, e)
	}
	c.mu.Lock()
	bCommands := len(c.editLocked(b.ID).Commands)
	c.mu.Unlock()
	if bCommands != 0 {
		t.Fatal("later edit had its own command", bCommands)
	}

}

func TestEditCompletedResumeConfirmsAfterCancel(t *testing.T) {
	c := editRecoveryFlow(t)
	fid := c.data.Flows[0].ID
	a := rejectedRecoveryEdit(t, c, fid, "resume-then-cancel-edit")
	q := editRecoveryCommand(t, c, a.ID, "resume-before-cancel-key")
	if _, e := c.ResumeAfterEdit(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	if f, e := c.CancelFlow(context.Background(), FlowCancel{Run: fid, Role: "GM"}); e != nil || f.State != "cancelled" {
		t.Fatal(f, e)
	}
	if f, e := c.ResumeAfterEdit(context.Background(), q); e != nil || f.State != "cancelled" {
		t.Fatal("completed Resume cannot confirm after cancellation", f, e)
	}
}

func TestEditResumeFailedWriteKeepsPauseUntilDurableRetry(t *testing.T) {
	c := editRecoveryFlow(t)
	fid := c.data.Flows[0].ID
	a := rejectedRecoveryEdit(t, c, fid, "resume-write-failure-edit")
	q := editRecoveryCommand(t, c, a.ID, "resume-write-failure-key")
	blockStateWrites(t, c)
	defer os.Remove(filepath.Join(c.root, "attempts.json.tmp"))
	if _, e := c.ResumeAfterEdit(context.Background(), q); e == nil {
		t.Fatal("expected real blocked ledger write")
	}
	f, e := c.GetFlow(context.Background(), fid)
	if e != nil || f.State != "edit-paused" {
		t.Fatal("failed write exposed progress", f, e)
	}
	if disk := readPublishedLedger(t, c); disk.Flows[0].State != "edit-paused" {
		t.Fatal("failed prepublication write changed disk", disk.Flows[0])
	}
	if _, e = c.ResumeAfterEdit(context.Background(), q); e == nil {
		t.Fatal("exact retry acknowledged completion while writes remain blocked")
	}
	if e = os.Remove(filepath.Join(c.root, "attempts.json.tmp")); e != nil {
		t.Fatal(e)
	}
	if f, e = c.ResumeAfterEdit(context.Background(), q); e != nil || f.State != "review" {
		t.Fatal("exact durable retry did not restore review", f, e)
	}
	if readPublishedLedger(t, c).Flows[0].State != "review" {
		t.Fatal("successful Resume was not published")
	}
	c.Close()
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f, e = reopened.GetFlow(context.Background(), fid)
	if e != nil || f.State != "uncertain" {
		t.Fatal("reopen bypassed ordinary explicit reconciliation", f, e)
	}
	reopened.mu.Lock()
	durable := reopened.editLocked(a.ID)
	completed := durable.Commands["resume"] != "" && !durable.ResumePending
	reopened.mu.Unlock()
	if !completed {
		t.Fatal("reopen lost durable Resume completion")
	}

}

func TestEditCompletedRerunConfirmsAfterCancel(t *testing.T) {
	c := editRecoveryFlow(t)
	fid := c.data.Flows[0].ID
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, fid, "accepted-rerun-cancel-edit"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	q := editRecoveryCommand(t, c, r.ID, "rerun-then-cancel-command")
	if _, e = c.RerunAfterEdit(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	waitWorkflow(t, c, fid, "review")
	c.wg.Wait()
	if _, e = c.RerunAfterEdit(context.Background(), q); e != nil {
		t.Fatal("completed exact rerun did not initially confirm", e)
	}
	if f, e := c.CancelFlow(context.Background(), FlowCancel{Run: fid, Role: "GM"}); e != nil || f.State != "cancelled" {
		t.Fatal(f, e)
	}
	mux := http.NewServeMux()
	c.registerEditRoutes(mux)
	raw, _ := json.Marshal(q)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/edit/rerun", strings.NewReader(string(raw))))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"cancelled"`) {
		t.Fatal("completed rerun cannot confirm after cancellation", w.Code, w.Body.String())
	}
}

func TestEditResumeSuppressedExecDoesNotRequireExecution(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	created, release := make(chan struct{}), make(chan struct{})
	var creates atomic.Int32
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		e := next(ctx, m, req, reply, cc, opts...)
		if e == nil && m == v1.Sessions_CreateSession_FullMethodName && creates.Add(1) == 1 {
			close(created)
			<-release
		}
		return e
	})))
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c.client = cl
	f := startWorkflow(t, c, "create-flight-ordinary", 12)
	<-created
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "abandon-queued-editor"))
	if e != nil {
		close(release)
		t.Fatal(e)
	}
	if _, e = c.AbandonEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "queued-edit-abandon-key")); e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	c.wg.Wait()
	c.mu.Lock()
	a := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	if a.Session == "" {
		t.Fatal("not a created session with suppressed Exec", a)
	}
	native, e := cl.GetSession(context.Background(), a.Session)
	if e != nil || native.GetLastSeq() != 0 {
		t.Fatal("native Exec was not suppressed", native, e)
	}
	q := editRecoveryCommand(t, c, r.ID, "resume-suppressed-exec-key")
	for i := 0; i < 2; i++ {
		if out, err := c.ResumeAfterEdit(context.Background(), q); err != nil || out.State != "paused" {
			t.Fatal("proven suppressed Exec could not resume to an explicit retry gate", i, out, err)
		}
	}
	c.mu.Lock()
	pending := c.editLocked(r.ID).ResumePending
	c.mu.Unlock()
	if pending {
		t.Fatal("successful nondispatch Resume remained pending")
	}
	native, e = cl.GetSession(context.Background(), a.Session)
	if e != nil || native.GetLastSeq() != 0 {
		t.Fatal("Resume dispatched native work", native, e)
	}
	if _, e = c.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: a.ID, RequestKey: "retry-proven-suppression", Role: "GM"}); e != nil {
		t.Fatal("proven non-dispatch Resume stranded explicit Retry", e)
	}
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()

}

func TestEditRerunRefusesUnknownExecOutcome(t *testing.T) {
	c, calls := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	execEntered, release := make(chan struct{}), make(chan struct{})
	var execs atomic.Int32
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, m string, next grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if m == v1.Sessions_Exec_FullMethodName && execs.Add(1) == 1 {
			close(execEntered)
			<-release
			return nil, status.Error(codes.Unavailable, "unknown native Exec transport outcome")
		}
		return next(ctx, desc, cc, m, opts...)
	})))
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c.client = cl
	f := startWorkflow(t, c, "old-unknown-exec-flow", 12)
	<-execEntered
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "edit-during-unknown-exec"))
	if e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	c.mu.Lock()
	old := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	proof, e := c.readFlowExecution(context.Background(), old)
	if e == nil && proof.Settled {
		t.Fatal("old execution unexpectedly proved settled")
	}
	if old.FlowStage != "exec-intent" || old.Session == "" {
		t.Fatal("not unknown recorded Exec", old)
	}
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	before := readPublishedLedger(t, c)
	count, head := calls.Load(), mustHead(t, c)
	if _, e = c.RerunAfterEdit(context.Background(), editRecoveryCommand(t, c, r.ID, "rerun-without-old-proof")); !errors.Is(e, ErrEditConflict) {
		t.Fatal("rerun did not retain the unknown ordinary Exec fence", e)
	}
	c.wg.Wait()
	after := readPublishedLedger(t, c)
	if before.Flows[0].Generation != after.Flows[0].Generation || before.Flows[0].Attempts != after.Flows[0].Attempts || len(before.Flows[0].EditCheckpoints) != len(after.Flows[0].EditCheckpoints) || len(after.Attempts) != len(before.Attempts) || calls.Load() != count || mustHead(t, c) != head {
		t.Fatal("refused rerun reserved a generation, work, checkpoint or accepted mutation")
	}
}
