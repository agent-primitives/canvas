package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestQuestionIntentFailureDoesNotLaunchExecOrLeavePhantomWorker(t *testing.T) {
	for _, failure := range []string{"before-publication", "after-publication"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			c, close := questionCanvas(t, `{"answer":"Synthetic answer."}`, &calls)
			defer close()
			id, err := c.Manual("writer", "", "Assess evidence.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			run := completed(t, c, id)
			c.wg.Wait()
			session, err := c.client.GetSession(context.Background(), run.Session)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "before-publication" {
				if err := os.Mkdir(filepath.Join(c.root, "conversation.json.tmp"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				failDirectorySync(c)
			}
			if _, err := c.Ask(id, "Why?", "coach"); err == nil {
				t.Fatal("question intent storage failure hidden")
			}
			c.wg.Wait()
			after, err := c.client.GetSession(context.Background(), run.Session)
			if err != nil || after.GetLastSeq() != session.GetLastSeq() || calls.Load() != 1 {
				t.Fatal("unpersisted question intent launched Exec")
			}
			state, err := c.State()
			if err != nil {
				t.Fatal(err)
			}
			if failure == "before-publication" {
				if len(state.Conversation) != 0 {
					t.Fatal("pre-publication failure invented question")
				}
			} else {
				if len(state.Conversation) != 1 || state.Conversation[0].Status != "uncertain" {
					t.Fatal("published intent left a phantom queued worker")
				}
			}
		})
	}
}

func TestQuestionFinalSyncFailureRetainsUncertaintyAfterReopen(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"Synthetic explanation."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	failed := false
	c.mu.Lock()
	c.syncDirectory = func(dir string) error {
		raw, err := os.ReadFile(filepath.Join(dir, "conversation.json"))
		if err != nil {
			return err
		}
		var ledger conversationLedger
		if json.Unmarshal(raw, &ledger) != nil {
			return errors.New("fixture unreadable")
		}
		if !failed && len(ledger.Entries) > 0 && ledger.Entries[len(ledger.Entries)-1].Status == "answered" {
			failed = true
			return errors.New("synthetic final answer sync failure")
		}
		return syncCanvasDirectory(dir)
	}
	c.mu.Unlock()
	qid, err := c.Ask(id, "Why?", "coach")
	if err != nil {
		t.Fatal(err)
	}
	entry := awaitQuestion(t, c, qid)
	if entry.Status != "uncertain" || entry.Answer != "" {
		t.Fatal("final durability failure hidden")
	}
	c.wg.Wait()
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := awaitQuestion(t, reopened, qid)
	if restored.Status != "uncertain" || restored.Error == "" || restored.Answer != "" || calls.Load() != 2 {
		t.Fatal("restart forgot question persistence uncertainty or retried provider")
	}
}

func TestQuestionRestartKeepsUncertainIntentWithoutModelRetry(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"Synthetic answer."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	c.wg.Wait()
	// Persist an interrupted intent as though the producer stopped after its durable
	// queue checkpoint, before any RPC; no synthetic session activity is inserted.
	session, err := c.client.GetSession(context.Background(), run.Session)
	if err != nil {
		t.Fatal(err)
	}
	a := c.findLocked(id)
	entry := conversationRecord{ConversationMessage: ConversationMessage{ID: "interrupted-question", Kind: "question", Actor: "owner", Text: "Why?", At: run.Created, Run: id, Session: run.Session, Agent: "writer", Status: "running"}, Input: `{"mode":"question","question":"Why?"}`, ExpectedSeq: session.GetLastSeq(), Harness: a.Harness}
	c.mu.Lock()
	err = c.appendConversationLocked(entry)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := reopened.State()
	if err != nil || len(state.Conversation) != 1 || state.Conversation[0].Status != "uncertain" || state.Conversation[0].Answer != "" || calls.Load() != 1 {
		t.Fatal("restart guessed answer or retried model")
	}
	after, err := c.client.GetSession(context.Background(), run.Session)
	if err != nil || after.GetLastSeq() != session.GetLastSeq() {
		t.Fatal("restart changed session log")
	}
}

func TestQuestionProofInvalidationMustPersistBeforeModelCall(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"Synthetic answer."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	c.wg.Wait()
	if _, err := c.Verify(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	blockStateWrites(t, c)
	if _, err := c.Ask(id, "Why?", "coach"); err == nil {
		t.Fatal("failed proof invalidation did not prevent question")
	}
	state, err := c.State()
	if err != nil || len(state.Conversation) != 1 || state.Conversation[0].Status != "uncertain" || calls.Load() != 1 {
		t.Fatal("question ignored proof persistence failure")
	}
	current, _ := c.GetRun(id)
	if current.Proof != nil || current.Session != run.Session {
		t.Fatal("stale proof or wrong session retained")
	}
}

func TestRoomStoreRefusesSymlinkAndInvalidQuestionStates(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.Close()
	target := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(target, []byte(`{"entries":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(c.root, "conversation.json")); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder); err == nil {
		opened.Close()
		t.Fatal("foreign room store accepted")
	}
	if err := os.Remove(filepath.Join(c.root, "conversation.json")); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(conversationLedger{Entries: []conversationRecord{{ConversationMessage: ConversationMessage{ID: "bad", Kind: "question", Actor: "GM", Text: "Why?"}}}})
	if err := os.WriteFile(filepath.Join(c.root, "conversation.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder); err == nil {
		opened.Close()
		t.Fatal("incomplete question state accepted")
	}
}
