package canvas

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
)

func abandonedNativeEdit(t *testing.T) (*Canvas, *atomic.Int32, EditRun) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	c, calls := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			close(entered)
			<-release
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	q := editRequest(t, c, "", "abandoned-native-editor")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	if _, e = c.AbandonEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "abandon-native-editor", ExpectedHead: q.BaseRevision, Confirmed: true}); e != nil {
		close(release)
		t.Fatal(e)
	}
	// No native END exists yet. A local tombstone must not release admission.
	if _, e = c.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "inspect-live-abandoned", ExpectedHead: q.BaseRevision, Confirmed: true}); !errors.Is(e, ErrEditConflict) {
		close(release)
		t.Fatal("live editor falsely settled", e)
	}
	cut := retirementLedger(t, c)
	close(release)
	c.wg.Wait()
	c.Close()
	// Actual native END persists, but Canvas crashed before its final bookkeeping.
	if e = os.WriteFile(filepath.Join(c.root, "attempts.json"), cut, 0600); e != nil {
		t.Fatal(e)
	}
	next, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(next.Close)
	return next, calls, r
}
func TestEditAbandonedSettlementUsesOnlyExactNativeReadback(t *testing.T) {
	c, calls, r := abandonedNativeEdit(t)
	ctx := context.Background()
	head := mustHead(t, c)
	refs := replayRefs(t, c)
	count := calls.Load()
	if _, e := c.StartFlow(ctx, FlowStart{Flow: "release", RequestKey: "blocked-before-native-proof", Inputs: []byte(`{"topic":"Fictional pilot"}`), AgentRuns: 12, Role: "GM"}); !errors.Is(e, ErrFlowBusy) {
		t.Fatal("abandoned uncertain attempt did not retain admission fence", e)
	}
	c.mu.Lock()
	saved := cloneEdit(*c.findLocked(r.Attempt))
	c.mu.Unlock()
	for _, tc := range []struct {
		name   string
		mutate func(*attempt)
	}{
		{"missing UID", func(a *attempt) { a.Session = "" }},
		{"unknown execution", func(a *attempt) { a.ExpectedSeq = nil }},
		{"foreign owner", func(a *attempt) { a.ExecutionOwner.Subject = "foreign" }},
		{"wrong harness", func(a *attempt) { a.Harness = "foreign" }},
		{"wrong bundle", func(a *attempt) { a.BundleDigest = "foreign" }},
		{"wrong input", func(a *attempt) { a.Input += " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.mu.Lock()
			a := c.findLocked(r.Attempt)
			*a = cloneEdit(saved)
			tc.mutate(a)
			c.mu.Unlock()
			if _, e := c.ReconcileEdit(ctx, EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "proof-refusal-" + tc.name, ExpectedHead: head, Confirmed: true}); !errors.Is(e, ErrEditConflict) {
				t.Fatal("mismatched native binding cleared fence", e)
			}
			c.mu.Lock()
			status := c.findLocked(r.Attempt).Status
			c.mu.Unlock()
			if status != "uncertain" {
				t.Fatal("refusal changed admission", status)
			}
		})
	}
	c.mu.Lock()
	*c.findLocked(r.Attempt) = saved
	c.mu.Unlock()
	originalClient := c.client
	var writes atomic.Int32
	readClient, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		switch method {
		case v1.Sessions_CreateSession_FullMethodName, v1.Sessions_Resume_FullMethodName, v1.Sessions_Approve_FullMethodName, v1.Sessions_Cancel_FullMethodName:
			writes.Add(1)
		}
		return next(ctx, method, req, reply, cc, opts...)
	}), grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, next grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if method == v1.Sessions_Exec_FullMethodName {
			writes.Add(1)
		}
		return next(ctx, desc, cc, method, opts...)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer readClient.Close()
	c.client = readClient
	command := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "prove-native-abandoned-settlement", ExpectedHead: head, Confirmed: true}
	out, e := c.ReconcileEdit(ctx, command)
	if e != nil || out.State != "abandoned" || !out.NativeSettled {
		t.Fatal("known settled abandoned editor could not reconcile", out, e)
	}
	c.mu.Lock()
	status := c.findLocked(r.Attempt).Status
	stage := c.findLocked(r.Attempt).FlowStage
	completed := c.findLocked(r.Attempt).Completed
	witness := cloneEdit(c.editLocked(r.ID).Settlement)
	c.mu.Unlock()
	if !completed.Equal(saved.Completed) || witness == nil || witness.Session != saved.Session || witness.Execution == "" || witness.Head == 0 || witness.Hash == "" {
		t.Fatal("settlement fabricated completion or omitted exact native witness", completed, witness)
	}
	if status != "abandoned" || stage != "completed" || c.data.EditSlot != "" {
		t.Fatal("admission bookkeeping not cleared", status, stage)
	}
	if writes.Load() != 0 || calls.Load() != count || mustHead(t, c) != head || string(replayRefs(t, c)) != string(refs) {
		t.Fatal("native read-back dispatched, published or changed Git")
	}
	ps, e := c.space.Proposals()
	if e != nil || len(ps) != 0 || out.Proposal != "" || out.AcceptedCommit != "" {
		t.Fatal("abandoned output became a proposal", out, ps, e)
	}
	if _, e = c.ReconcileEdit(ctx, command); e != nil || calls.Load() != count {
		t.Fatal("settled exact-key retry changed execution", e)
	}
	c.client = originalClient
	c.Close()
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(reopened.Close)
	c = reopened
	out, e = c.GetEdit(ctx, r.ID)
	if e != nil || out.State != "abandoned" || !out.NativeSettled || out.Proposal != "" || out.AcceptedCommit != "" {
		t.Fatal("reopen revived abandoned output", out, e)
	}
	if again, e := c.RequestEdit(ctx, out.Request); e != nil || again.ID != r.ID || again.State != "abandoned" || calls.Load() != count {
		t.Fatal("exact request replay revived tombstone", again, e)
	}
	command.RequestKey = "reopened-abandoned-native-proof"
	if out, e = c.ReconcileEdit(ctx, command); e != nil || out.State != "abandoned" || !out.NativeSettled || calls.Load() != count {
		t.Fatal("reopened reconciliation revived tombstone", out, e)
	}
	ps, e = c.space.Proposals()
	if e != nil || len(ps) != 0 || mustHead(t, c) != head || string(replayRefs(t, c)) != string(refs) {
		t.Fatal("reopened tombstone published or changed Git", ps, e)
	}
	f, e := c.StartFlow(ctx, FlowStart{Flow: "release", RequestKey: "new-explicit-flow-after-proof", Inputs: []byte(`{"topic":"Fictional pilot"}`), AgentRuns: 12, Role: "GM"})
	if e != nil {
		t.Fatal("settled abandoned attempt still blocked admission", e)
	}
	waitWorkflow(t, c, f.ID, "review")
}
