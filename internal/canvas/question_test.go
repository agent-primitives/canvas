package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
)

func questionCanvas(t *testing.T, answer string, calls *atomic.Int32) (*Canvas, func()) {
	t.Helper()
	d := nativeData(t)
	return nativeProviderFixture(t, d, func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		var envelope struct {
			Mode      string `json:"mode"`
			Question  string `json:"question"`
			Reference struct {
				OriginalInput json.RawMessage `json:"originalInput"`
			} `json:"reference"`
		}
		if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Text()), &envelope); err != nil {
			return api.ModelResponse{}, err
		}
		if envelope.Mode == "question" {
			if envelope.Question == "" || len(envelope.Reference.OriginalInput) == 0 || len(req.Messages) < 4 {
				t.Error("question lost actual session history or recorded snapshot")
			}
			if answer == "provider-fails" {
				return api.ModelResponse{}, errors.New("synthetic unsafe provider detail")
			}
			if answer == "profile-mismatch" {
				return api.ModelResponse{}, sessionhost.ErrModelProfileMismatch
			}
			return api.ModelResponse{Message: *api.TextMessage("assistant", answer)}, nil
		}
		input, err := nativeRequestInput(req)
		if err != nil {
			return api.ModelResponse{}, err
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", nativeOutput(d, input))}, nil
	})
}

func awaitQuestion(t *testing.T, c *Canvas, id string) ConversationMessage {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, err := c.State()
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range state.Conversation {
			if entry.ID == id && entry.Status != "queued" && entry.Status != "running" {
				return entry
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("question did not finish")
	return ConversationMessage{}
}

func TestQuestionUsesSameNativeSessionWithoutPublishingAnyDocument(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"The original assessment retained the rank because only synthetic production was supplied; medical evidence was missing."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess synthetic production.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	if run.Status != "proposed" || !run.QuestionsSupported {
		t.Fatal("native document run not available for questions")
	}
	if _, err := c.Verify(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	before, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Comment(id, "Could the owner clarify our priorities?", "coach"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("room question called the agent")
	}
	qid, err := c.Ask(id, "Why did you retain his rank?", "owner")
	if err != nil {
		t.Fatal(err)
	}
	question := awaitQuestion(t, c, qid)
	if question.Status != "answered" || question.Answer == "" || question.Actor != "owner" || question.Session != run.Session || question.Agent != "writer" || question.Model == "" || question.LastSeq <= 0 || calls.Load() != 2 {
		t.Fatal("question answer lost actual identity/model/sequence or retried")
	}
	after, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if after.Head != before.Head || len(after.Proposals) != len(before.Proposals) || len(after.History) != len(before.History) || len(after.Runs) != len(before.Runs) {
		t.Fatal("read-only question changed document or proposal history")
	}
	original, _ := c.GetRun(id)
	if original.Proof != nil || original.Proposal != run.Proposal || original.Model != run.Model {
		t.Fatal("question retained stale proof or overwrote original assessment")
	}
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	if err != nil || len(sessions) != 1 {
		t.Fatal("question silently created another session")
	}
	proof, err := c.Verify(context.Background(), id)
	if err != nil || proof.ModelCalls != 0 || !proof.Reconstructed || !proof.OutputsMatch || calls.Load() != 2 {
		t.Fatal("whole-session replay changed after answer")
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := awaitQuestion(t, reopened, qid)
	if restored != question || calls.Load() != 2 {
		t.Fatal("reopen lost answer metadata or replayed provider")
	}
}

// Synthetic historical records test refusal, not old workspace activation.
func TestLegacyQuestionsUnsupportedWithoutAnyNewSessionOrModelCall(t *testing.T) {
	for _, profile := range []int{1, 2} {
		t.Run(contract.HarnessName("fixture", profile), func(t *testing.T) {
			c, _, calls := definedFixture(t, nil)
			b := c.bundles["writer"]
			c.data.Attempts = append(c.data.Attempts, attempt{Run: Run{ID: "historical", Agent: "writer", Session: "unavailable-historical-session", Status: "proposed"}, BundleDigest: b.Digest, Harness: contract.HarnessName(b.Digest, profile)})
			run, _ := c.GetRun("historical")
			if run.QuestionsSupported {
				t.Fatal("legacy record advertised questions")
			}
			if _, err := c.Ask(run.ID, "Why?", "GM"); !errors.Is(err, ErrQuestionsUnsupported) {
				t.Fatalf("unsupported question: %v", err)
			}
			state, err := c.State()
			if err != nil || len(state.Conversation) != 0 || calls.Load() != 0 {
				t.Fatal("unsupported question invented a turn or dispatched")
			}
			sessions, err := c.client.ListSessions(context.Background(), c.project())
			if err != nil || len(sessions) != 0 {
				t.Fatal("unsupported question created session")
			}
		})
	}
}

func TestQuestionProfileMismatchDisplaysOnlyFixedActionableGuidance(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, "profile-mismatch", &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	qid, err := c.Ask(id, "Why?", "coach")
	if err != nil {
		t.Fatal(err)
	}
	entry := awaitQuestion(t, c, qid)
	if entry.Status != "failed" || !strings.Contains(entry.Error, "matching profile") || entry.Answer != "" || calls.Load() != 2 {
		t.Fatal("model-profile mismatch lost actionable fixed diagnostic or retried")
	}
}

func TestQuestionsSerializeAndRejectStaleSessionSequenceWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"Synthetic answer."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	c.wg.Wait()
	c.sem <- struct{}{}
	released := false
	defer func() {
		if !released {
			<-c.sem
		}
	}()
	qid, err := c.Ask(id, "Why?", "coach")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ask(id, "Another why?", "owner"); !errors.Is(err, ErrQuestionBusy) {
		t.Fatal("same-session questions were interleaved")
	}
	if _, err := c.Verify(context.Background(), id); !errors.Is(err, ErrQuestionBusy) {
		t.Fatal("in-flight answer used an old proof")
	}
	if _, err := c.Comment(id, "Human room post while the question is queued.", "owner"); err != nil {
		t.Fatal(err)
	}
	session, err := c.client.GetSession(context.Background(), run.Session)
	if err != nil {
		t.Fatal(err)
	}
	seq := session.GetLastSeq()
	if _, err := c.client.Exec(context.Background(), client.ExecOptions{Session: run.Session, Inputs: []string{`{"mode":"documents","agent":"writer","request":"Advance the session without publishing."}`}, ExpectedLastSeq: &seq}); err != nil {
		t.Fatal(err)
	}
	<-c.sem
	released = true
	entry := awaitQuestion(t, c, qid)
	if entry.Status != "failed" || entry.Answer != "" || calls.Load() != 2 {
		t.Fatal("stale question ran at a changed head or retried")
	}
}

func TestAnswerValidationAndFailureNeverPublishOrRetry(t *testing.T) {
	for _, answer := range []string{`{"rank":1,"tier":1}`, `{"answer":"","strategy":"unexpected"}`, `{"answer":""}`, "provider-fails"} {
		t.Run(answer, func(t *testing.T) {
			var calls atomic.Int32
			c, close := questionCanvas(t, answer, &calls)
			defer close()
			id, err := c.Manual("writer", "", "Assess synthetic evidence.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			completed(t, c, id)
			before, err := c.State()
			if err != nil {
				t.Fatal(err)
			}
			qid, err := c.Ask(id, "Explain the recommendation.", "coach")
			if err != nil {
				t.Fatal(err)
			}
			entry := awaitQuestion(t, c, qid)
			if entry.Status != "failed" || entry.Answer != "" || entry.Error == "" || calls.Load() != 2 {
				t.Fatal("invalid answer was exposed, hidden or retried")
			}
			after, err := c.State()
			if err != nil || after.Head != before.Head || len(after.Proposals) != len(before.Proposals) {
				t.Fatal("invalid answer changed documents")
			}
		})
	}
}
