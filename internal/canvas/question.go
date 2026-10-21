package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/contract"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrQuestionsUnsupported = errors.New("questions are unsupported for this recorded session; start a new assessment explicitly")
var ErrQuestionBusy = errors.New("wait for the current session turn before asking another question")

func questionBinding(a *attempt) bool {
	return a.EditID == "" && a.Session != "" && a.BundleDigest != "" && (a.Harness == contract.HarnessName(a.BundleDigest, 3) || a.Mode == "documents" && a.Harness == contract.HarnessName(a.BundleDigest, 4))
}
func (c *Canvas) questionBusyLocked(session string) bool {
	return c.turnBusyLocked(session) || c.recoveryBusyLocked(session, "")
}
func (c *Canvas) askBusyLocked(session string) bool {
	return c.activeFlowLocked() || c.questionBusyLocked(session)
}
func (c *Canvas) turnBusyLocked(session string) bool {
	for _, a := range c.data.Attempts {
		if a.Session == session && (a.Status == "queued" || a.Status == "running") {
			return true
		}
	}
	for _, entry := range c.conversation.Entries {
		if entry.Session == session && (entry.Status == "queued" || entry.Status == "running") {
			return true
		}
	}
	return false
}
func (c *Canvas) questionVersionLocked(session string) int {
	if session == "" {
		return 0
	}
	n := 0
	for _, action := range c.data.Recoveries {
		if action.Inspection.Session == session {
			n++
		}
	}
	for _, entry := range c.conversation.Entries {
		if entry.Kind == "question" && entry.Session == session {
			n++
		}
	}
	return n
}

// Ask explains a recorded assessment on that exact session. Its intent is
// canvas-owned; only this explicit action sends a new input to Sessions.
func (c *Canvas) Ask(runID, text, role string) (string, error) {
	return c.AskContext(context.Background(), runID, text, role)
}

// AskContext captures the verified requester and performs its admission read
// within the request lifetime under the execution service. The queued turn
// then runs under the engine's lifetime, never the HTTP request's.
func (c *Canvas) AskContext(ctx context.Context, runID, text, role string) (string, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if err := c.competingFlow(); err != nil {
		return "", err
	}
	_, requestedBy, err := c.requester(ctx, role)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if !validRole(role) || text == "" || !safeText(text, 2000) {
		return "", errors.New("invalid question role or text")
	}
	c.mu.Lock()
	original := c.findLocked(runID)
	if original == nil {
		c.mu.Unlock()
		return "", errNotFound
	}
	if original.FlowRun != "" || !questionBinding(original) || !c.ownedLocked(original.ExecutionOwner) {
		c.mu.Unlock()
		return "", ErrQuestionsUnsupported
	}
	a := *original
	if c.askBusyLocked(a.Session) {
		c.mu.Unlock()
		return "", ErrQuestionBusy
	}
	c.mu.Unlock()
	serviceCtx, sessions, release, err := c.serviceContext(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	readCtx, cancel := context.WithTimeout(serviceCtx, 10*time.Second)
	defer cancel()
	session, err := sessions.GetSession(readCtx, a.Session)
	if err != nil {
		return "", internalError(errors.New("cannot inspect recorded question session"))
	}
	if session.GetHarness() != a.Harness || session.GetAnnotations()["bundle-digest"] != a.BundleDigest || !recordedOwner(session, a.ExecutionOwner) {
		return "", ErrQuestionsUnsupported
	}
	var input any
	if json.Unmarshal([]byte(a.Input), &input) != nil {
		return "", internalError(errors.New("recorded assessment input unavailable"))
	}
	reference := map[string]any{"run": a.ID, "originalInput": input, "notice": "Original input and earlier model output describe the recorded attempt, not later acceptance."}
	proposals, err := c.space.Proposals()
	if err != nil {
		return "", internalError(err)
	}
	for _, p := range proposals {
		if p.ID == a.Proposal {
			reference["proposal"] = map[string]any{"id": p.ID, "status": p.Status, "changes": p.Changes}
			break
		}
	}
	raw, err := json.Marshal(map[string]any{"mode": "question", "question": text, "role": role, "reference": reference, "notice": "Explain recorded evidence only. This turn is read-only and cannot update documents, accept or reject proposals."})
	if err != nil {
		return "", internalError(errors.New("cannot encode question context"))
	}
	entry := conversationRecord{ConversationMessage: ConversationMessage{ID: ID(), Kind: "question", Actor: role, Text: text, At: time.Now().UTC(), Run: a.ID, Session: a.Session, Agent: a.Agent, Status: "queued", RequestedBy: requestedBy, ExecutionOwner: a.ExecutionOwner}, Input: string(raw), ExpectedSeq: session.GetLastSeq(), Harness: a.Harness}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return "", errors.New("canvas stopping")
	}
	if c.askBusyLocked(a.Session) {
		return "", ErrQuestionBusy
	}
	if err := c.appendConversationLocked(entry); err != nil {
		if errors.Is(err, errCanvasPublicationUncertain) {
			c.markQuestionUncertainLocked(entry.ID, "Question intent was published but durability is uncertain. No execution launched; ask again explicitly.")
		}
		return "", err
	}
	// A previously verified head cannot cover this future turn. Persist the
	// invalidation before launching Exec, including other attempts on this UID.
	for i := range c.data.Attempts {
		if c.data.Attempts[i].Session == a.Session {
			c.data.Attempts[i].Proof = nil
		}
	}
	if err := c.saveLocked(); err != nil {
		c.persistenceFailureLocked(nil)
		c.markQuestionUncertainLocked(entry.ID, "Question could not start because proof invalidation was not saved. Ask again explicitly.")
		return "", err
	}
	c.wg.Add(1)
	go c.executeQuestion(entry.ID)
	return entry.ID, nil
}

func (c *Canvas) questionLocked(id string) *conversationRecord {
	for i := range c.conversation.Entries {
		if c.conversation.Entries[i].ID == id {
			return &c.conversation.Entries[i]
		}
	}
	return nil
}
func (c *Canvas) markQuestionUncertainLocked(id, message string) {
	entry := c.questionLocked(id)
	if entry == nil {
		return
	}
	entry.Status = "uncertain"
	entry.Answer = ""
	entry.Error = message
	entry.Completed = time.Now().UTC()
	if c.saveConversationLocked() != nil {
		c.persistenceFailureLocked(nil)
	}
}
func (c *Canvas) updateQuestion(id string, change func(*conversationRecord)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.questionLocked(id)
	if entry == nil {
		return internalError(errors.New("recorded question missing"))
	}
	change(entry)
	if err := c.saveConversationLocked(); err != nil {
		entry.Status = "uncertain"
		entry.Answer = ""
		entry.Error = "Question state could not be saved. No automatic retry; inspect evidence before asking again."
		entry.Completed = time.Now().UTC()
		// The first rename may already have published a terminal answer. Best-effort
		// publish its durability warning too; never re-drive the model turn.
		if saveErr := c.saveConversationLocked(); saveErr != nil {
			c.persistenceFailureLocked(nil)
		}
		c.persistenceFailureLocked(nil)
		return internalError(err)
	}
	return nil
}

func validAnswer(answer string) bool {
	return utf8.ValidString(answer) && utf8.RuneCountInString(answer) <= 3000 && strings.TrimSpace(answer) != "" && safeText(answer, 12000)
}

func decodeAnswer(output string) (string, error) {
	failure := errors.New("agent answer did not match the read-only answer schema")
	if len(output) > 24000 {
		return "", failure
	}
	dec := json.NewDecoder(strings.NewReader(output))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return "", failure
	}
	key, err := dec.Token()
	if err != nil || key != "answer" {
		return "", failure
	}
	var answer string
	if dec.Decode(&answer) != nil || !validAnswer(answer) || dec.More() {
		return "", failure
	}
	end, err := dec.Token()
	var extra any
	if err != nil || end != json.Delim('}') || dec.Decode(&extra) != io.EOF {
		return "", failure
	}
	return answer, nil
}

func (c *Canvas) executeQuestion(id string) {
	defer c.wg.Done()
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-c.ctx.Done():
		_ = c.updateQuestion(id, func(entry *conversationRecord) {
			entry.Status = "uncertain"
			entry.Error = "Question interrupted before execution. Ask again explicitly."
			entry.Completed = time.Now().UTC()
		})
		return
	}
	started := time.Now()
	if err := c.updateQuestion(id, func(entry *conversationRecord) { entry.Status = "running"; entry.Started = started.UTC() }); err != nil {
		return
	}
	c.mu.Lock()
	entry := *c.questionLocked(id)
	owned := c.ownedLocked(entry.ExecutionOwner)
	c.mu.Unlock()
	if !owned {
		_ = c.completeQuestion(id, nil, ErrQuestionsUnsupported, started)
		return
	}
	serviceCtx, sessions, release, err := c.serviceContext(context.Background())
	if err != nil {
		_ = c.completeQuestion(id, nil, err, started)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(serviceCtx, 8*time.Minute)
	defer cancel()
	session, err := sessions.GetSession(ctx, entry.Session)
	if err == nil && !recordedOwner(session, entry.ExecutionOwner) {
		err = ErrQuestionsUnsupported
	}
	if err == nil && (session.GetHarness() != entry.Harness || session.GetLastSeq() != entry.ExpectedSeq) {
		err = ErrQuestionBusy
	}
	var turn *client.TurnResult
	if err == nil {
		turn, err = sessions.Exec(ctx, client.ExecOptions{Session: entry.Session, Inputs: []string{entry.Input}, ExpectedLastSeq: &entry.ExpectedSeq})
	}
	_ = c.completeQuestion(id, turn, err, started)
}

// Both Exec and explicitly confirmed Resume use the same read-only decoder.
func (c *Canvas) completeQuestion(id string, turn *client.TurnResult, err error, started time.Time) error {
	answer := ""
	if err == nil && turn != nil {
		answer, err = decodeAnswer(turn.Output)
		if err != nil {
			err = errors.Join(errOutputRejected, err)
		}
	}
	saveErr := c.updateQuestion(id, func(saved *conversationRecord) {
		saved.Error = ""
		saved.Completed = time.Now().UTC()
		saved.Seconds = time.Since(started).Seconds()
		if turn != nil {
			observed := attempt{}
			observeTurn(&observed, turn, err == nil)
			saved.Model = observed.Model
			saved.LastSeq = turn.LastSeq
		}
		if err == nil && turn != nil {
			saved.Status = "answered"
			saved.Answer = answer
			return
		}
		saved.Status = "failed"
		saved.Error = "The question failed; no documents were changed. Ask again explicitly."
		// Only this already-public, fixed diagnostic may escape generic sanitization.
		if turn != nil {
			for _, record := range turn.Records {
				if record.GetEvent().GetKind() == v1.EventKind_EVENT_ERROR && record.GetEvent().GetError().GetDescription() == "recorded model differs from selected profile; select the matching profile or regenerate on latest" {
					saved.Error = "The recorded model differs from the selected profile. Select the matching profile or start a new assessment explicitly; no documents were changed."
				}
			}
		}
		if err == ErrQuestionBusy || status.Code(err) == codes.Aborted {
			saved.Error = "The session changed before this question could run. Ask again explicitly."
		}
		if c.ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.Canceled || status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Unavailable {
			saved.Status = "uncertain"
			saved.Error = "Question outcome is uncertain. No automatic retry; inspect the session before asking again."
		}
	})
	if saveErr != nil {
		return saveErr
	}
	return err
}
