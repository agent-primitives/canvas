package canvas

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type decisionRun struct {
	Model    string `json:"model"`
	Activity []struct {
		Kind     string    `json:"kind"`
		At       time.Time `json:"at"`
		Document string    `json:"document,omitempty"`
	} `json:"activity"`
}

func decisionMetadata(t *testing.T, r Run) decisionRun {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var d decisionRun
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func activityKinds(d decisionRun) []string {
	var kinds []string
	for _, a := range d.Activity {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}
func TestDecisionObservedModelAndSuccessfulActivity(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	r, _ := c.GetRun(id)
	d := decisionMetadata(t, r)
	if d.Model != "test-model" {
		t.Fatalf("observed requested model missing: %q", d.Model)
	}
	want := []string{"snapshot", "input-read", "input-read", "input-read", "execution-started", "model-requested", "output-received", "validated", "proposed", "finished"}
	if !reflect.DeepEqual(activityKinds(d), want) {
		t.Fatalf("activity = %v, want %v", activityKinds(d), want)
	}
	if !d.Activity[0].At.Equal(r.Created) {
		t.Fatal("snapshot timestamp differs from capture")
	}
	for _, a := range d.Activity {
		if a.At.IsZero() {
			t.Fatal("activity timestamp absent")
		}
	}
	saved := readPublishedLedger(t, c)
	if !reflect.DeepEqual(decisionMetadata(t, saved.Attempts[0].Run), d) {
		t.Fatal("decision metadata not persisted")
	}
}
func TestDecisionFailedTurnRetainsObservedModelWithoutCleanFinish(t *testing.T) {
	c, close := setup(t, true)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	r, _ := c.GetRun(id)
	d := decisionMetadata(t, r)
	if d.Model != "test-model" {
		t.Fatalf("partial streamed model not retained: %q", d.Model)
	}
	want := []string{"snapshot", "input-read", "input-read", "input-read", "execution-started", "model-requested", "failed"}
	if !reflect.DeepEqual(activityKinds(d), want) {
		t.Fatalf("failed activity = %v", activityKinds(d))
	}
}
func TestDecisionLegacyInterruptedAndRecoveredMetadata(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	c.Close()
	c.mu.Lock()
	// Simulate pre-decision-view ledgers, including interrupted and lost bookkeeping.
	raw, _ := json.Marshal(c.data)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	for _, item := range data["attempts"].([]any) {
		a := item.(map[string]any)
		delete(a, "model")
		delete(a, "activity")
		a["status"] = "running"
		delete(a, "proposal")
	}
	raw, _ = json.Marshal(data)
	c.data = ledger{}
	if err := json.Unmarshal(raw, &c.data); err != nil {
		t.Fatal(err)
	}
	interrupted := attempt{Run: Run{ID: "legacy-interrupted", Status: "running", Agent: "writer"}}
	untouched := attempt{Run: Run{ID: "legacy-failed", Status: "failed", Agent: "writer"}}
	c.data.Attempts = append(c.data.Attempts, interrupted, untouched)
	if err := c.saveLocked(); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	before := time.Now().UTC()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b := reopened.bundles["writer"]
	b.Model = "today-not-historical"
	reopened.bundles["writer"] = b
	recovered, _ := reopened.GetRun(id)
	d := decisionMetadata(t, recovered)
	if d.Model != "" || !reflect.DeepEqual(activityKinds(d), []string{"recovered"}) || d.Activity[0].At.Before(before) {
		t.Fatalf("legacy recovery invents model or history: %+v", d)
	}
	uncertain, _ := reopened.GetRun(interrupted.ID)
	d = decisionMetadata(t, uncertain)
	if uncertain.Status != "uncertain" || !reflect.DeepEqual(activityKinds(d), []string{"interrupted"}) {
		t.Fatalf("interruption mislabeled: %+v %+v", uncertain, d)
	}
	old, _ := reopened.GetRun(untouched.ID)
	d = decisionMetadata(t, old)
	if d.Model != "" || len(d.Activity) != 0 {
		t.Fatal("old completed ledger backfilled")
	}
	reopened.Close()
	reopened2, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened2.Close()
	again, _ := reopened2.GetRun(id)
	if len(decisionMetadata(t, again).Activity) != 1 {
		t.Fatal("recovery activity duplicates on each restart")
	}
}

func TestDecisionReturnedRunSnapshotsCannotMutateLedger(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.mu.Lock()
	c.data.Attempts = append(c.data.Attempts, attempt{Run: Run{ID: "snapshot", Activity: []Activity{{Kind: "snapshot", At: time.Now()}}, BoardChange: &BoardChangeDTO{OldRank: 4}, Proof: &sessionclient.Proof{RequestedModels: []string{"recorded-model"}}}})
	c.mu.Unlock()
	r, _ := c.GetRun("snapshot")
	r.Activity[0].Kind = "mutated"
	r.BoardChange.OldRank = 99
	r.Proof.RequestedModels[0] = "mutated"
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs[0].Activity[0].Kind != "snapshot" || state.Runs[0].BoardChange.OldRank != 4 || state.Runs[0].Proof.RequestedModels[0] != "recorded-model" {
		t.Fatal("GetRun aliases producer ledger")
	}
	state.Runs[0].Activity[0].Kind = "mutated-state"
	r, _ = c.GetRun("snapshot")
	if r.Activity[0].Kind != "snapshot" {
		t.Fatal("State aliases producer ledger")
	}
}

func TestDecisionObservedMetadataIsBoundedAndTimestamped(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, model := range []string{"actual-requested-model", "Bearer secret", "unsafe\nmodel", "sk-123456789012345678901234567890"} {
		t.Run(model, func(t *testing.T) {
			a := attempt{}
			turn := &client.TurnResult{Output: "private output", Records: []*v1.LogRecord{{Event: &v1.Event{Kind: v1.EventKind_EVENT_MODEL_CALL, Ts: timestamppb.New(at), Body: &v1.Event_Model{Model: &v1.ModelCall{Model: model, Params: map[string]string{"prompt": "private prompt"}}}}}}}
			observeTurn(&a, turn, false)
			observeTurn(&a, turn, false)
			if len(a.Activity) != 1 || a.Activity[0].Kind != "model-requested" || !a.Activity[0].At.Equal(at) {
				t.Fatal("model milestone duplicate or timestamp invented")
			}
			want := ""
			if model == "actual-requested-model" {
				want = model
			}
			if a.Model != want {
				t.Fatalf("model metadata not bounded: %q", a.Model)
			}
			raw, _ := json.Marshal(a.Run)
			var exposed map[string]any
			if err := json.Unmarshal(raw, &exposed); err != nil {
				t.Fatal(err)
			}
			if _, ok := exposed["output"]; ok {
				t.Fatal("raw turn leaked")
			}
			if strings.Contains(string(raw), "private prompt") || strings.Contains(string(raw), "private output") {
				t.Fatal("private session payload leaked into run metadata")
			}
		})
	}
}

type observedModelStream struct{ grpc.ClientStream }

func (s observedModelStream) RecvMsg(message any) error {
	err := s.ClientStream.RecvMsg(message)
	if err == nil {
		if update, ok := message.(*v1.ExecUpdate); ok && update.GetRecord().GetEvent().GetKind() == v1.EventKind_EVENT_MODEL_CALL {
			update.GetRecord().GetEvent().GetModel().Model = "stream-requested-model"
		}
	}
	return err
}
func TestDecisionModelComesFromStreamNotNominalSession(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}
		return observedModelStream{stream}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	r, _ := c.GetRun(id)
	if r.Model != "stream-requested-model" {
		t.Fatalf("model inferred from nominal session or bundle: %q", r.Model)
	}
}

type interruptedStream struct {
	grpc.ClientStream
	observedModel bool
}

func (s *interruptedStream) RecvMsg(message any) error {
	if s.observedModel {
		return status.Error(codes.Canceled, "private transport failure")
	}
	err := s.ClientStream.RecvMsg(message)
	if err == nil {
		if update, ok := message.(*v1.ExecUpdate); ok && update.GetRecord().GetEvent().GetKind() == v1.EventKind_EVENT_MODEL_CALL {
			s.observedModel = true
		}
	}
	return err
}
func TestDecisionInterruptedStreamRetainsOnlyObservedModelMetadata(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}
		return &interruptedStream{ClientStream: stream}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	r, _ := c.GetRun(id)
	if r.Status != "uncertain" || r.Model != "test-model" || r.Proposal != "" || !reflect.DeepEqual(activityKinds(decisionMetadata(t, r)), []string{"snapshot", "input-read", "input-read", "input-read", "execution-started", "model-requested", "interrupted"}) {
		t.Fatalf("interruption invented completion or lost observed model: %+v", r)
	}
}

func TestDecisionLaterBookkeepingFailureDoesNotEraseObservedCompletion(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	blockStateWrites(t, c)
	if err := c.update(id, func(a *attempt) { a.Summary = "Later bookkeeping." }); err == nil {
		t.Fatal("failure fixture did not block write")
	}
	r, _ := c.GetRun(id)
	found := false
	for _, activity := range r.Activity {
		if activity.Kind == "finished" {
			found = true
		}
	}
	if !found {
		t.Fatal("later bookkeeping failure erased an already-observed clean finish")
	}
}

func TestDecisionCancelledQueuedRunHasNoInventedModelActivity(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.sem <- struct{}{}
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	r, _ := c.GetRun(id)
	if r.Status != "uncertain" || r.Model != "" || !reflect.DeepEqual(activityKinds(decisionMetadata(t, r)), []string{"snapshot", "input-read", "input-read", "input-read", "interrupted"}) {
		t.Fatalf("cancelled queue invented execution: %+v", r)
	}
}

func mustHead(t *testing.T, c *Canvas) string {
	t.Helper()
	head, err := c.space.Head()
	if err != nil {
		t.Fatal(err)
	}
	return head
}
