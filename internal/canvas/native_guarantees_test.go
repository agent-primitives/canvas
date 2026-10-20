package canvas

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
)

func TestDefinedProposalRecoveryUsesDurableAttributionRun(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	c.space = publishHook{c.space, func() { blockStateWrites(t, c) }}
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	original := completed(t, c, id)
	c.wg.Wait()
	c.Close()
	if original.Proposal == "" {
		t.Fatal("missing durable Git proposal fixture")
	}
	// The ledger predates proposal bookkeeping, but Git proves the attributed run.
	saved := readPublishedLedger(t, c)
	if saved.Attempts[0].Proposal != "" {
		t.Fatal("fixture persisted producer bookkeeping")
	}
	decoy := saved.Attempts[0]
	decoy.ID, decoy.Status = "same-session-decoy", "failed"
	saved.Attempts = append(saved.Attempts, decoy)
	c.data = saved
	if err := os.Remove(filepath.Join(c.root, "attempts.json.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := c.saveLocked(); err != nil {
		t.Fatal(err)
	}
	n := calls.Load()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, _ := reopened.GetRun(id)
	unrelated, _ := reopened.GetRun(decoy.ID)
	if recovered.Status != "proposed" || recovered.Proposal != original.Proposal || recovered.Session != original.Session || unrelated.Status != "failed" || unrelated.Proposal != "" || calls.Load() != n {
		t.Fatalf("recovery guessed from session/base instead of attribution: %+v %+v", recovered, unrelated)
	}
	if readPublishedLedger(t, reopened).Attempts[0].Proposal != original.Proposal {
		t.Fatal("recovery was not durable")
	}
}

func TestInputActivityNativeBindingBeforeExecAndStrippedWire(t *testing.T) {
	d := nativeData(t)
	providerObserved := make(chan ledger, 1)
	var c *Canvas
	var close func()
	c, close = nativeProviderFixture(t, d, func(_ context.Context, request api.ModelRequest) (api.ModelResponse, error) {
		providerObserved <- readPublishedLedger(t, c)
		if request.Model != "test-model" || len(request.Messages) == 0 || request.Messages[0].Role != "system" || request.Messages[0].Text() == "" {
			t.Error("native request lost actual model or instructions")
		}
		input, err := nativeRequestInput(request)
		if err != nil {
			return api.ModelResponse{}, err
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", nativeOutput(d, input))}, nil
	})
	defer close()
	messageCounts := make(chan int, 4)
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}
		return policyBoundaryStream{ClientStream: stream, messageCounts: messageCounts}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	if r.Status != "proposed" {
		t.Fatalf("native fixture failed: %+v", r)
	}
	select {
	case count := <-messageCounts:
		if count != 0 {
			t.Fatal("normal wire exposed private request messages")
		}
	default:
		t.Fatal("model request not observed on real stream")
	}
	saved := <-providerObserved
	a := saved.Attempts[0]
	if a.Session != r.Session || a.BundleDigest != c.bundles["writer"].Digest || a.Harness != contract.HarnessName(a.BundleDigest, 4) || a.Status != "running" {
		t.Fatal("native binding not durable before provider invocation")
	}
	if a.Model != "" {
		t.Fatal("model metadata inferred before returned observations")
	}
	want := []string{"snapshot", "input-read", "input-read", "input-read", "execution-started", "model-requested", "output-received", "validated", "proposed", "finished"}
	if !reflect.DeepEqual(activityKinds(decisionMetadata(t, r)), want) {
		t.Fatal("stripped wire lost producer milestones or invented policy activity")
	}
}

func TestWatchScansOnlyDeclaredInputsAndStopsCleanly(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	if err := os.Mkdir(filepath.Join(c.space.Root(), "injuries"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.space.Root(), "injuries", "retired.json"), []byte(`{"id":"retired"}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeFeedback(t, c, "watched.json")
	c.Watch()
	deadline := time.Now().Add(5 * time.Second)
	var runs []Run
	for time.Now().Before(deadline) {
		state, err := c.State()
		if err != nil {
			t.Fatal(err)
		}
		runs = state.Runs
		if len(runs) != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(runs) != 1 {
		t.Fatalf("watcher did not discover declared input: %+v", runs)
	}
	r := completed(t, c, runs[0].ID)
	c.Close()
	if r.Trigger != "new-feedback" || r.Actor != fileActor || r.Status != "proposed" || calls.Load() != 1 {
		t.Fatalf("watcher dispatched undeclared source or lost actor: %+v", r)
	}
	// No Sessions record was inferred for a retired input.
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	if err != nil || len(sessions) != 1 || sessions[0].GetExecState() != v1.ExecState_EXEC_COMPLETED {
		t.Fatalf("watcher lifecycle: %v", err)
	}
}
