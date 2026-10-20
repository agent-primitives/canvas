package canvas

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func confirmationFor(i ResumeInspection) ResumeConfirmation {
	return ResumeConfirmation{Confirmed: true, ActionID: ID() + ID(), Session: i.Session, Execution: i.Execution, LastSeq: i.LastSeq, Head: i.Head, SessionVersion: i.SessionVersion}
}
func failedResumeRun(t *testing.T, c *Canvas) string {
	t.Helper()
	id, err := c.Manual("writer", "", "Assess synthetic evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	c.wg.Wait()
	if r.Status != "failed" {
		t.Fatalf("fixture not failed: %+v", r)
	}
	return id
}
func countRecoveryRPCs(t *testing.T, c *Canvas) (*atomic.Int32, func()) {
	t.Helper()
	var resumes atomic.Int32
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if strings.HasSuffix(method, "/Resume") {
			resumes.Add(1)
		}
		if strings.HasSuffix(method, "/CreateSession") {
			t.Error("recovery created a session")
		}
		return invoke(ctx, method, req, reply, cc, opts...)
	}), grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if strings.HasSuffix(method, "/Exec") {
			t.Error("recovery executed new input")
		}
		return streamer(ctx, desc, cc, method, opts...)
	})))
	if err != nil {
		t.Fatal(err)
	}
	previous := c.client
	c.client = cl
	return &resumes, func() { c.wg.Wait(); c.client = previous; cl.Close() }
}

func TestResumeRejectsMissingStaleBusyAndReusedAuthorityWithoutRPC(t *testing.T) {
	c, calls, _, close := resumeCanvas(t, false)
	defer close()
	id := failedResumeRun(t, c)
	resumes, restore := countRecoveryRPCs(t, c)
	defer restore()
	i := c.InspectResume(context.Background(), id, "")
	if !i.Available {
		t.Fatalf("eligible: %+v", i)
	}
	for _, kind := range []string{"confirmation", "session", "execution", "seq", "head", "version", "action", "role"} {
		t.Run(kind, func(t *testing.T) {
			confirm := confirmationFor(i)
			role := "GM"
			switch kind {
			case "confirmation":
				confirm.Confirmed = false
			case "session":
				confirm.Session = "other"
			case "execution":
				confirm.Execution = "other"
			case "seq":
				confirm.LastSeq++
			case "head":
				confirm.Head = strings.Repeat("b", 64)
			case "version":
				confirm.SessionVersion++
			case "action":
				confirm.ActionID = "short"
			case "role":
				role = "system"
			}
			if err := c.ConfirmResume(context.Background(), id, "", role, confirm); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	c.mu.Lock()
	c.findLocked(id).Status = "running"
	c.mu.Unlock()
	if c.InspectResume(context.Background(), id, "").Available {
		t.Fatal("busy session eligible")
	}
	if err := c.ConfirmResume(context.Background(), id, "", "GM", confirmationFor(i)); err == nil {
		t.Fatal("busy confirmation accepted")
	}
	c.mu.Lock()
	c.findLocked(id).Status = "failed"
	c.mu.Unlock()
	if calls.Load() != 1 || resumes.Load() != 0 {
		t.Fatal("invalid authority performed work")
	}
}

func TestResumeConcurrentActionIntentIsDurableBeforeSingleRPC(t *testing.T) {
	c, calls, fail, close := resumeCanvas(t, false)
	defer close()
	id := failedResumeRun(t, c)
	resumes, restore := countRecoveryRPCs(t, c)
	defer restore()
	i := c.InspectResume(context.Background(), id, "")
	confirm := confirmationFor(i)
	c.sem <- struct{}{}
	var group sync.WaitGroup
	var accepted atomic.Int32
	for n := 0; n < 8; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if c.ConfirmResume(context.Background(), id, "", "GM", confirm) == nil {
				accepted.Add(1)
			}
		}()
	}
	group.Wait()
	raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), confirm.ActionID) || !strings.Contains(string(raw), `"status": "pending"`) {
		t.Fatal("intent not durable before RPC")
	}
	run, _ := c.GetRun(id)
	if run.SessionVersion != i.SessionVersion+1 || run.ResumeStatus != "pending" || run.Proof != nil || accepted.Load() != 1 || resumes.Load() != 0 {
		t.Fatalf("claim/generation: %+v accepted=%d RPC=%d", run, accepted.Load(), resumes.Load())
	}
	fail.Store(false)
	<-c.sem
	c.wg.Wait()
	if calls.Load() != 2 || resumes.Load() != 1 {
		t.Fatalf("single submission calls=%d rpc=%d", calls.Load(), resumes.Load())
	}
}

func TestResumePersistenceFailureCannotLaunchRPC(t *testing.T) {
	for _, kind := range []string{"before-rename", "directory-sync"} {
		t.Run(kind, func(t *testing.T) {
			c, calls, _, close := resumeCanvas(t, false)
			defer close()
			id := failedResumeRun(t, c)
			resumes, restore := countRecoveryRPCs(t, c)
			defer restore()
			i := c.InspectResume(context.Background(), id, "")
			confirm := confirmationFor(i)
			if kind == "before-rename" {
				if err := os.Mkdir(filepath.Join(c.root, "attempts.json.tmp"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				c.syncDirectory = func(string) error { return errors.New("sync failed") }
			}
			if err := c.ConfirmResume(context.Background(), id, "", "GM", confirm); !errors.Is(err, errInternal) {
				t.Fatalf("persistence error=%v", err)
			}
			c.wg.Wait()
			if calls.Load() != 1 || resumes.Load() != 0 {
				t.Fatal("persistence failure launched recovery")
			}
			if kind == "before-rename" {
				os.Remove(filepath.Join(c.root, "attempts.json.tmp"))
			} else {
				c.syncDirectory = nil
			}
			if err := c.ConfirmResume(context.Background(), id, "", "GM", confirm); err == nil {
				t.Fatal("failed persistence reused old authority")
			}
		})
	}
}

func TestResumeUncertaintySurvivesRestartOnlyFreshConfirmationCanResend(t *testing.T) {
	c, calls, fail, close := resumeCanvas(t, false)
	defer close()
	id := failedResumeRun(t, c)
	i := c.InspectResume(context.Background(), id, "")
	confirm := confirmationFor(i)
	// A real Resume whose provider fails leaves an audit ERROR and the same pending effect.
	if err := c.ConfirmResume(context.Background(), id, "", "GM", confirm); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	run, _ := c.GetRun(id)
	if calls.Load() != 2 || run.ResumeStatus != "uncertain" || run.RecoveryError == "" {
		t.Fatalf("uncertain outcome hidden: %+v calls=%d", run, calls.Load())
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for n := 0; n < 3; n++ {
		reopened.State()
		reopened.InspectResume(context.Background(), id, "")
	}
	if calls.Load() != 2 {
		t.Fatal("restart/poll auto resent")
	}
	if err := reopened.ConfirmResume(context.Background(), id, "", "GM", confirm); err == nil {
		t.Fatal("old confirmation resent after restart")
	}
	fresh := reopened.InspectResume(context.Background(), id, "")
	if !fresh.Available {
		t.Fatalf("fresh inspection after uncertainty: %+v", fresh)
	}
	sameID := confirmationFor(fresh)
	sameID.ActionID = confirm.ActionID
	if err := reopened.ConfirmResume(context.Background(), id, "", "GM", sameID); err == nil {
		t.Fatal("consumed action ID reused with new evidence")
	}
	// A head change refuses an intervening confirmed action without resolving the earlier request.
	reopened.sem <- struct{}{}
	if err := reopened.ConfirmResume(context.Background(), id, "", "GM", confirmationFor(fresh)); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.client.Suspend(context.Background(), fresh.Session); err != nil {
		t.Fatal(err)
	}
	<-reopened.sem
	reopened.wg.Wait()
	run, _ = reopened.GetRun(id)
	if run.ResumeStatus != "refused" || calls.Load() != 2 {
		t.Fatalf("intervening action did not refuse before provider: %+v", run)
	}
	fresh = reopened.InspectResume(context.Background(), id, "")
	if !fresh.Available {
		t.Fatalf("fresh inspection after refusal: %+v", fresh)
	}
	fail.Store(false)
	if err := reopened.ConfirmResume(context.Background(), id, "", "owner", confirmationFor(fresh)); err != nil {
		t.Fatal(err)
	}
	reopened.wg.Wait()
	run, _ = reopened.GetRun(id)
	if calls.Load() != 3 || run.Status != "proposed" || run.ResumeStatus != "succeeded" || !strings.Contains(run.RecoveryError, "earlier Resume outcome was uncertain") {
		t.Fatalf("fresh deliberate resume/history: %+v calls=%d", run, calls.Load())
	}
	if reopened.InspectResume(context.Background(), id, "").Available {
		t.Fatal("completed output eligible to resend")
	}
}

func TestResumeChangedEvidenceAtDispatchRefusesWithoutRPC(t *testing.T) {
	c, calls, fail, close := resumeCanvas(t, false)
	defer close()
	id := failedResumeRun(t, c)
	resumes, restore := countRecoveryRPCs(t, c)
	defer restore()
	inspection := c.InspectResume(context.Background(), id, "")
	confirm := confirmationFor(inspection)
	c.sem <- struct{}{}
	if err := c.ConfirmResume(context.Background(), id, "", "GM", confirm); err != nil {
		t.Fatal(err)
	}
	if _, err := c.client.Suspend(context.Background(), inspection.Session); err != nil {
		t.Fatal(err)
	}
	<-c.sem
	c.wg.Wait()
	run, _ := c.GetRun(id)
	if run.ResumeStatus != "refused" || calls.Load() != 1 || resumes.Load() != 0 {
		t.Fatalf("changed head dispatched: %+v calls=%d rpc=%d", run, calls.Load(), resumes.Load())
	}
	// With no uncertain action in history, fresh success must clear the obsolete refusal.
	fresh := c.InspectResume(context.Background(), id, "")
	if !fresh.Available {
		t.Fatalf("fresh inspection after refusal: %+v", fresh)
	}
	fail.Store(false)
	if err := c.ConfirmResume(context.Background(), id, "", "owner", confirmationFor(fresh)); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	run, _ = c.GetRun(id)
	if run.ResumeStatus != "succeeded" || run.RecoveryError != "" || calls.Load() != 2 || resumes.Load() != 1 {
		t.Fatalf("successful recovery retained obsolete refusal: %+v", run)
	}
}

func TestResumeTransportUncertaintyNeverRetriesEvenWhenHostCompleted(t *testing.T) {
	c, calls, fail, close := resumeCanvas(t, false)
	defer close()
	id := failedResumeRun(t, c)
	var resumes atomic.Int32
	hooked, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := invoke(ctx, method, req, reply, cc, opts...)
		if strings.HasSuffix(method, "/Resume") {
			resumes.Add(1)
			return status.Error(codes.Unavailable, "private response lost")
		}
		return err
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer hooked.Close()
	c.client = hooked
	fail.Store(false)
	i := c.InspectResume(context.Background(), id, "")
	if err := c.ConfirmResume(context.Background(), id, "", "GM", confirmationFor(i)); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	run, _ := c.GetRun(id)
	if run.ResumeStatus != "uncertain" || run.Proposal != "" || calls.Load() != 2 || resumes.Load() != 1 {
		t.Fatalf("uncertainty invented completion: %+v", run)
	}
	if c.InspectResume(context.Background(), id, "").Available {
		t.Fatal("recorded completed output re-invoked")
	}
	if err := c.ConfirmResume(context.Background(), id, "", "GM", confirmationFor(i)); err == nil {
		t.Fatal("uncertain RPC auto retried")
	}
	if resumes.Load() != 1 {
		t.Fatal("extra Resume RPC")
	}
}
