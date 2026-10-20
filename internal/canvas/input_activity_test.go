package canvas

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"google.golang.org/grpc"
)

// Normal Exec records strip ModelCall.Messages. Observe only their presence,
// without changing the wire record or retaining any private request payload.
type policyBoundaryStream struct {
	grpc.ClientStream
	messageCounts chan int
}

func (s policyBoundaryStream) RecvMsg(message any) error {
	err := s.ClientStream.RecvMsg(message)
	if err == nil {
		if update, ok := message.(*v1.ExecUpdate); ok && update.GetRecord().GetEvent().GetKind() == v1.EventKind_EVENT_MODEL_CALL {
			s.messageCounts <- len(update.GetRecord().GetEvent().GetModel().GetMessages())
		}
	}
	return err
}

type inputReadWindow struct {
	path          string
	before, after time.Time
}

type observedInputSpace struct {
	sharedspace.SharedSpace
	reads []inputReadWindow
	fail  map[string]error
}

func (s *observedInputSpace) Read(revision, path string) (string, error) {
	before := time.Now().UTC()
	if err := s.fail[path]; err != nil {
		return "", err
	}
	text, err := s.SharedSpace.Read(revision, path)
	if err == nil {
		s.reads = append(s.reads, inputReadWindow{path: path, before: before, after: time.Now().UTC()})
	}
	return text, err
}

func TestInputActivityReportIsLoadedWithoutInventingPolicy(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	before := time.Now().UTC()
	if err := c.WriteInput("feedback/observed.json", feedbackJSON, "coach"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, err := c.State()
	if err != nil || len(state.Runs) != 1 {
		t.Fatalf("input run unavailable: %v", err)
	}
	r := completed(t, c, state.Runs[0].ID)
	c.wg.Wait()
	d := decisionMetadata(t, r)
	wantKinds := []string{"snapshot", "input-read", "input-read", "input-read", "input-read", "execution-started", "model-requested", "output-received", "validated", "proposed", "finished"}
	if !reflect.DeepEqual(activityKinds(d), wantKinds) {
		t.Fatalf("input activity = %v", activityKinds(d))
	}
	for i, path := range []string{"brief.json", "feedback/initial.json", "feedback/observed.json", "prd.json"} {
		activity := d.Activity[i+1]
		if activity.Document != path || activity.At.Before(before) || activity.At.After(r.Created) {
			t.Fatalf("input %d = %+v", i, activity)
		}
	}
	saved := readPublishedLedger(t, c)
	if !reflect.DeepEqual(saved.Attempts[0].Activity, r.Activity) {
		t.Fatal("input metadata not persisted")
	}
	r.Activity[1].Document = "mutated"
	state, err = c.State()
	if err != nil || state.Runs[0].Activity[1].Document != "brief.json" {
		t.Fatal("GetRun aliases ledger")
	}
	state.Runs[0].Activity[1].Document = "mutated-state"
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, ok := reopened.GetRun(r.ID)
	if !ok || !reflect.DeepEqual(again.Activity, saved.Attempts[0].Activity) {
		t.Fatal("restart changed observed metadata")
	}
}

func TestInputActivityReviewerOnlyRecordsItsDeclaredInputs(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	a, err := c.snapshotGeneric("reviewer", "Review accepted requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Evidence map[string]string `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(a.Input), &in); err != nil || len(in.Evidence) != 3 {
		t.Fatalf("reviewer evidence changed: %v", err)
	}
	for _, path := range []string{"brief.json", "feedback/initial.json", "prd.json"} {
		if in.Evidence[path] == "" {
			t.Fatalf("missing declared input %s", path)
		}
	}
	for _, activity := range a.Activity {
		if activity.Kind == "input-read" && in.Evidence[activity.Document] == "" {
			t.Fatal("fabricated input activity")
		}
	}
	if len(a.Activity) != 4 {
		t.Fatal("reviewer read an undeclared document")
	}
}

func TestInputActivityRequiredReadFailureDoesNotQueue(t *testing.T) {
	for _, path := range []string{"brief.json", "feedback/initial.json", "prd.json"} {
		t.Run(path, func(t *testing.T) {
			c, close := setup(t, false)
			defer close()
			c.space = &observedInputSpace{SharedSpace: c.space, fail: map[string]error{path: errors.New("private read failure")}}
			id, err := c.Manual("writer", "", "Assess evidence.", "GM")
			if id != "" || err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("required read failure not bounded: %q %v", id, err)
			}
			if len(c.data.Attempts) != 0 || len(readPublishedLedger(t, c).Attempts) != 0 {
				t.Fatal("failed snapshot queued a run")
			}
		})
	}
}

func TestInputActivitySnapshotRecordsActualSourceReads(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	space := &observedInputSpace{SharedSpace: c.space}
	c.space = space
	before := time.Now().UTC()
	a, err := c.snapshotGeneric("writer", "Assess evidence.", "GM")
	after := time.Now().UTC()
	if err != nil {
		t.Fatal(err)
	}
	d := decisionMetadata(t, a.Run)
	want := []string{"brief.json", "feedback/initial.json", "prd.json"}
	if len(d.Activity) != 4 {
		t.Fatalf("snapshot activity = %+v; want snapshot and three actual input reads", d.Activity)
	}
	if d.Activity[0].Kind != "snapshot" || !d.Activity[0].At.Equal(a.Created) {
		t.Fatal("legacy snapshot milestone changed")
	}
	if len(space.reads) != len(want) {
		t.Fatalf("source reads = %+v; want exactly one read per input", space.reads)
	}
	for i, path := range want {
		activity, read := d.Activity[i+1], space.reads[i]
		if activity.Kind != "input-read" || activity.Document != path || read.path != path {
			t.Fatalf("input %d: activity = %+v, read = %+v", i, activity, read)
		}
		if activity.At.Before(read.before) || activity.At.Before(read.after) || activity.At.After(after) || activity.At.Before(before) {
			t.Fatalf("input %d timestamp not observed after actual read: %+v", i, activity)
		}
		if i > 0 && activity.At.Before(d.Activity[i].At) {
			t.Fatal("input reads out of capture order")
		}
	}
}
