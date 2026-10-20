package canvas

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/protobuf/proto"
)

func TestResumeUsesOriginalBaseAndIndependentStaleReview(t *testing.T) {
	c, _, fail, cleanup := resumeCanvas(t, false)
	defer cleanup()
	id := failedResumeRun(t, c)
	original, _ := c.GetRun(id)
	if _, err := c.space.Ingest("feedback/later.json", `{"team":"Northwind","feedback":"A later accepted fictional observation."}`, "GM"); err != nil {
		t.Fatal(err)
	}
	head, err := c.space.Head()
	if err != nil || head == original.Base {
		t.Fatal("accepted base did not advance")
	}
	fail.Store(false)
	inspection := c.InspectResume(context.Background(), id, "")
	if err := c.ConfirmResume(context.Background(), id, "", "owner", confirmationFor(inspection)); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	run, _ := c.GetRun(id)
	proposals, err := c.space.Proposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 || run.Base != original.Base || proposals[0].Base != original.Base || proposals[0].Attribution.Session != run.Session || proposals[0].Attribution.Run != id {
		t.Fatalf("recovery moved base/ownership: %+v", proposals)
	}
	if _, err := c.Review(run.Proposal, "accept", "GM"); !errors.Is(err, sharedspace.ErrStale) {
		t.Fatalf("recovery bypassed stale review: %v", err)
	}
	after, _ := c.space.Head()
	if after != head {
		t.Fatal("recovery accepted documents")
	}
}

func TestResumeInvalidOutputUsesExistingRunAndQuestionValidators(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "question"}[question], func(t *testing.T) {
			c, calls, fail, cleanup := resumeCanvas(t, question, `{"rank":1,"tier":1,"strategy":"unexpected"}`)
			defer cleanup()
			id, err := c.Manual("writer", "", "Assess synthetic evidence.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			original := completed(t, c, id)
			message := ""
			if question {
				fail.Store(true)
				message, err = c.Ask(id, "Why?", "coach")
				if err != nil {
					t.Fatal(err)
				}
				awaitQuestion(t, c, message)
			}
			c.wg.Wait()
			if !question {
				if err := c.update(id, func(a *attempt) { a.Status = "uncertain" }); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := c.State()
			n := calls.Load()
			fail.Store(false)
			inspection := c.InspectResume(context.Background(), id, message)
			if err := c.ConfirmResume(context.Background(), id, message, "GM", confirmationFor(inspection)); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			after, _ := c.State()
			run, _ := c.GetRun(id)
			if after.Head != before.Head || len(after.Proposals) != len(before.Proposals) || calls.Load() != n+1 {
				t.Fatal("invalid output published or retried")
			}
			if question {
				entry := awaitQuestion(t, c, message)
				if entry.Status != "failed" || entry.Answer != "" || entry.Error == "" || entry.ResumeStatus != "refused" || run.Proposal != original.Proposal {
					t.Fatalf("invalid question output used: %+v", entry)
				}
			} else if run.Status != "failed" || run.Proposal != "" || run.Error == "" || run.ResumeStatus != "refused" {
				t.Fatalf("invalid resumed run lost validation diagnostic: %+v", run)
			}
			if c.InspectResume(context.Background(), id, message).Available {
				t.Fatal("completed invalid output eligible to resend")
			}
		})
	}
}

func TestResumedTurnRejectsChangedOrUnexpectedCompletion(t *testing.T) {
	for _, kind := range []string{"valid", "extra-model", "wrong-execution", "wrong-order", "missing-end", "changed-prefix", "tool-output"} {
		t.Run(kind, func(t *testing.T) {
			records := resumeEvidence()
			rechainResume(t, records)
			inspection := ResumeInspection{Available: true, Session: "session", Execution: "pending", LastSeq: 4, Head: records[3].ContentHash}
			output := &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventOutput, Message: api.TextMessage("assistant", `{"answer":"read only"}`)})}
			end := &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventEnd, End: &api.HarnessEnd{State: "COMPLETED"}})}
			records = append(records, output, end)
			switch kind {
			case "extra-model":
				records = append(records, proto.Clone(records[2]).(*v1.LogRecord))
			case "wrong-execution":
				output.Event.ExecutionId = "other"
			case "wrong-order":
				records[4], records[5] = records[5], records[4]
			case "missing-end":
				records = records[:5]
			case "changed-prefix":
				records[3].Event.GetError().Description = "changed"
			case "tool-output":
				output.Event.GetMessage().Parts = append(output.Event.GetMessage().Parts, &v1.Part{Part: &v1.Part_ToolCall{ToolCall: &v1.ToolCall{Tool: "write"}}})
			}
			rechainResume(t, records)
			turn, err := resumedTurn(records, inspection, &v1.Session{Metadata: &v1.ResourceMetadata{Uid: "session"}, LastSeq: int64(len(records))})
			if kind == "valid" {
				if err != nil || turn.Output != `{"answer":"read only"}` {
					t.Fatalf("valid completion: %+v %v", turn, err)
				}
			} else if err == nil {
				t.Fatalf("unexpected completion consumed: %+v", turn)
			}
		})
	}
}
