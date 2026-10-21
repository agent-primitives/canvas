package canvas

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/wire"
)

const resumeUnavailable = "Resume is unavailable for this recorded evidence. Start a new attempt explicitly."
const resumeToolRefusal = "The last tool call has no recorded result. It may already have changed something; repeating it could repeat the action."
const resumeUncertain = "Resume outcome is uncertain. No automatic retry; inspect again and confirm a new action explicitly."

var errResumeConflict = errors.New("Resume evidence changed, is busy, or this action was already submitted; inspect and confirm again")

// ResumeInspection is read-only evidence, not executable authority. Every field is
// rechecked when accepting confirmation and immediately before the single RPC.
type ResumeInspection struct {
	Available      bool   `json:"available"`
	Reason         string `json:"reason"`
	Session        string `json:"session"`
	Execution      string `json:"execution"`
	LastSeq        int64  `json:"lastSeq"`
	Head           string `json:"head"`
	SessionVersion int    `json:"sessionVersion"`
}
type ResumeConfirmation struct {
	Confirmed      bool   `json:"confirmed"`
	ActionID       string `json:"actionId"`
	Session        string `json:"session"`
	Execution      string `json:"execution"`
	LastSeq        int64  `json:"lastSeq"`
	Head           string `json:"head"`
	SessionVersion int    `json:"sessionVersion"`
}
type resumeAction struct {
	ID         string           `json:"id"`
	Run        string           `json:"run"`
	Message    string           `json:"message,omitempty"`
	Role       string           `json:"role"`
	Inspection ResumeInspection `json:"inspection"`
	Status     string           `json:"status"`
	Error      string           `json:"error,omitempty"`
}

// Each allowlisted Canvas harness makes exactly one model call and no tools.
// Require the actual invocation's identity, not merely the session default. Any
// legacy/incomplete/foreign execution or tool/approval evidence fails closed.
func classifyResume(records []*v1.LogRecord, harness, input, model string) (string, string) {
	type execution struct {
		inputs int
		stage  int
	}
	seen := map[string]*execution{}
	last := ""
	for _, record := range records {
		event := record.GetEvent()
		if event == nil {
			return "", resumeUnavailable
		}
		switch event.GetKind() {
		case v1.EventKind_EVENT_TOOL_CALL, v1.EventKind_EVENT_TOOL_RESULT, v1.EventKind_EVENT_APPROVAL_REQUEST, v1.EventKind_EVENT_APPROVAL_RESULT:
			return "", resumeToolRefusal
		}
		if event.GetKind() == v1.EventKind_EVENT_LIFECYCLE {
			life := event.GetLifecycle()
			if life == nil || life.GetKind() < v1.Lifecycle_LIFECYCLE_SUSPEND || life.GetKind() > v1.Lifecycle_LIFECYCLE_CANCEL || life.GetKind() == v1.Lifecycle_LIFECYCLE_BASELINE || event.GetExecutionId() != "" {
				return "", resumeUnavailable
			}
			continue
		}
		id := event.GetExecutionId()
		if id == "" || len(id) > 256 {
			return "", resumeUnavailable
		}
		e := seen[id]
		if e == nil {
			start := event.GetExecutionStart()
			if event.GetKind() != v1.EventKind_EVENT_EXECUTION_START || start == nil || start.InputCount == nil || start.GetInputCount() != 1 || start.GetHarness() != harness || start.GetHarnessVersion() != "" || len(start.GetConfig()) != 0 || start.GetResumeFromSeq() != 0 {
				return "", resumeUnavailable
			}
			e = &execution{}
			seen[id] = e
			last = id
			continue
		}
		switch event.GetKind() {
		case v1.EventKind_EVENT_INPUT:
			if e.stage != 0 || e.inputs != 0 || !plainMessage(event.GetMessage(), "user") {
				return "", resumeUnavailable
			}
			e.inputs++
		case v1.EventKind_EVENT_MODEL_CALL:
			call := event.GetModel()
			if e.stage != 0 || e.inputs != 1 || call == nil || call.GetId() == "" || len(call.GetId()) > 256 || call.GetModel() != model || !hashIdentity(call.GetInputHash()) || len(call.GetTools()) != 0 || call.GetToolChoice() != nil {
				return "", resumeUnavailable
			}
			for _, message := range call.GetMessages() {
				if !plainMessage(message, message.GetRole()) {
					return "", resumeToolRefusal
				}
			}
			e.stage = 1
		case v1.EventKind_EVENT_OUTPUT:
			if e.stage != 1 || !plainMessage(event.GetMessage(), "assistant") {
				return "", resumeUnavailable
			}
			e.stage = 2
		case v1.EventKind_EVENT_USAGE:
			if e.stage != 2 || event.GetUsage() == nil {
				return "", resumeUnavailable
			}
		case v1.EventKind_EVENT_END:
			if e.stage != 2 || event.GetEnd() == nil || event.GetEnd().GetState() != "COMPLETED" {
				return "", resumeUnavailable
			}
			e.stage = 3
		case v1.EventKind_EVENT_ERROR:
			if event.GetError() == nil {
				return "", resumeUnavailable
			}
		default:
			return "", resumeUnavailable
		}
	}
	for id, execution := range seen {
		if id != last && execution.stage != 3 {
			return "", resumeUnavailable
		}
	}
	pending := seen[last]
	if pending == nil || pending.inputs != 1 || pending.stage != 1 {
		return "", resumeUnavailable
	}
	// Exactly one complete producer input binds the pending execution to its owner.
	for _, r := range records {
		if r.GetEvent().GetExecutionId() == last && r.GetEvent().GetKind() == v1.EventKind_EVENT_INPUT {
			if wire.MessageFromProto(r.GetEvent().GetMessage()).Text() != input {
				return "", resumeUnavailable
			}
		}
	}
	return last, ""
}
func plainMessage(message *v1.Message, role string) bool {
	if message == nil || message.GetRole() != role || role == "tool" || len(message.GetParts()) == 0 {
		return false
	}
	for _, part := range message.GetParts() {
		if part.GetText() == nil {
			return false
		}
	}
	return true
}
func hashIdentity(hash string) bool {
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == 32 && hash == strings.ToLower(hash)
}

func (c *Canvas) resumeTargetLocked(run, message string) (attempt, string, bool) {
	a := c.findLocked(run)
	if a == nil || a.Session == "" || a.EditID != "" || c.data.EditSlot != "" {
		return attempt{}, "", false
	}
	target := *a
	input := a.Input
	if message == "" {
		if a.Proposal != "" || (a.Status != "failed" && a.Status != "uncertain") {
			return attempt{}, "", false
		}
	} else {
		q := c.questionLocked(message)
		if q == nil || q.Kind != "question" || q.Run != run || q.Session != a.Session || q.Harness != a.Harness || (q.Status != "failed" && q.Status != "uncertain") {
			return attempt{}, "", false
		}
		input = q.Input
		seq := q.ExpectedSeq
		target.ExpectedSeq = &seq
	}
	var envelope struct {
		Mode      string `json:"mode"`
		Reference struct {
			Run string `json:"run"`
		} `json:"reference"`
	}
	if json.Unmarshal([]byte(input), &envelope) != nil || !strings.HasPrefix(strings.TrimSpace(input), "{") {
		return attempt{}, "", false
	}
	if message != "" {
		if envelope.Mode != "question" || envelope.Reference.Run != a.ID {
			return attempt{}, "", false
		}
	} else if envelope.Mode != a.Mode {
		return attempt{}, "", false
	}
	bundle, ok := c.bundles[a.Agent]
	if !ok || bundle.Digest != a.BundleDigest || !hashIdentity(bundle.Digest) || !c.ownedLocked(a.ExecutionOwner) {
		return attempt{}, "", false
	}
	known := false
	for _, profile := range contract.Profiles(bundle) {
		if a.Harness == contract.HarnessName(bundle.Digest, profile) {
			known = true
		}
	}
	if !known || message != "" && !questionBinding(a) {
		return attempt{}, "", false
	}
	if target.ExpectedSeq == nil || *target.ExpectedSeq < 0 {
		return attempt{}, "", false
	}
	return target, input, true
}
func (c *Canvas) recoveryBusyLocked(session, ignore string) bool {
	for _, action := range c.data.Recoveries {
		if action.ID != ignore && action.Inspection.Session == session && action.Status == "pending" {
			return true
		}
	}
	return false
}
func (c *Canvas) inspectResume(ctx context.Context, run, message, ignore string) ResumeInspection {
	unavailable := ResumeInspection{Reason: resumeUnavailable}
	c.mu.Lock()
	a, input, ok := c.resumeTargetLocked(run, message)
	if !ok || a.FlowRun != "" || c.activeFlowLocked() {
		c.mu.Unlock()
		return unavailable
	}
	version := c.questionVersionLocked(a.Session)
	busy := c.turnBusyLocked(a.Session) || c.recoveryBusyLocked(a.Session, ignore)
	c.mu.Unlock()
	if busy {
		return ResumeInspection{Reason: "The session is busy. Wait for the current session turn."}
	}
	// Eligibility reads are service-owned and follow the caller's lifetime.
	serviceCtx, sessions, release, err := c.serviceContext(ctx)
	if err != nil {
		return unavailable
	}
	defer release()
	ctx, cancel := context.WithTimeout(serviceCtx, 10*time.Second)
	defer cancel()
	session, err := sessions.GetSession(ctx, a.Session)
	if err != nil || !recordedOwner(session, a.ExecutionOwner) || session.GetMetadata().GetUid() != a.Session || session.GetMetadata().GetProject() != c.project() || session.GetHarness() != a.Harness || session.GetAnnotations()["bundle-digest"] != a.BundleDigest || !terminalResumeState(session.GetExecState()) {
		return unavailable
	}
	records, err := sessionclient.ReadRecovery(ctx, sessions.Sessions(), a.Session, session.GetLastSeq())
	if err != nil {
		return unavailable
	}
	execution, reason := classifyResume(records, a.Harness, input, c.bundles[a.Agent].Model)
	if execution == "" {
		return ResumeInspection{Reason: reason}
	}
	owned := false
	for _, record := range records {
		if record.GetEvent().GetExecutionId() == execution && record.GetEvent().GetKind() == v1.EventKind_EVENT_EXECUTION_START {
			owned = record.GetSeq() == *a.ExpectedSeq+1
		}
	}
	if !owned {
		return unavailable
	}
	latest, err := sessions.GetSession(ctx, a.Session)
	if err != nil || !sameSessionEvidence(session, latest) {
		return unavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.questionVersionLocked(a.Session) != version || c.turnBusyLocked(a.Session) || c.recoveryBusyLocked(a.Session, ignore) {
		return unavailable
	}
	return ResumeInspection{Available: true, Session: a.Session, Execution: execution, LastSeq: session.GetLastSeq(), Head: records[len(records)-1].GetContentHash(), SessionVersion: version}
}
func terminalResumeState(state v1.ExecState) bool {
	return state == v1.ExecState_EXEC_COMPLETED || state == v1.ExecState_EXEC_FAILED || state == v1.ExecState_EXEC_CANCELED
}
func sameSessionEvidence(a, b *v1.Session) bool {
	return a.GetLastSeq() == b.GetLastSeq() && a.GetMetadata().GetUid() == b.GetMetadata().GetUid() && a.GetHarness() == b.GetHarness() && a.GetAnnotations()["bundle-digest"] == b.GetAnnotations()["bundle-digest"] && terminalResumeState(b.GetExecState())
}
func (c *Canvas) InspectResume(ctx context.Context, run, message string) ResumeInspection {
	return c.inspectResume(ctx, run, message, "")
}
func matchingResume(i ResumeInspection, confirmation ResumeConfirmation) bool {
	return i.Available && i.Session == confirmation.Session && i.Execution == confirmation.Execution && i.LastSeq == confirmation.LastSeq && i.Head == confirmation.Head && i.SessionVersion == confirmation.SessionVersion
}

// ConfirmResume durably claims this unique action and invalidates proofs before
// scheduling work. Neither reload nor polling launches a recovery worker.
func (c *Canvas) ConfirmResume(ctx context.Context, run, message, role string, confirmation ResumeConfirmation) error {
	if _, err := c.actionIdentity(ctx, role); err != nil {
		return err
	}
	actionID, err := hex.DecodeString(confirmation.ActionID)
	if !validRole(role) || !confirmation.Confirmed || err != nil || len(actionID) < 16 || len(actionID) > 32 || confirmation.ActionID != strings.ToLower(confirmation.ActionID) {
		return errors.New("explicit bounded Resume confirmation required")
	}
	inspection := c.InspectResume(ctx, run, message)
	if !matchingResume(inspection, confirmation) {
		return errResumeConflict
	}
	action, err := c.claimResume(run, message, role, inspection, confirmation)
	if err != nil {
		return err
	}
	c.wg.Add(1)
	if !c.authenticated {
		go c.executeResume(c.ctx, action)
		return nil
	}
	// Authenticated Resume completes within the original verified request; a
	// background worker never reacquires or outlives the human credential.
	return c.executeResume(ctx, action)
}
func (c *Canvas) claimResume(run, message, role string, inspection ResumeInspection, confirmation ResumeConfirmation) (resumeAction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data.EditSlot != "" || c.activeFlowLocked() || c.ctx.Err() != nil || c.questionVersionLocked(inspection.Session) != inspection.SessionVersion || c.questionBusyLocked(inspection.Session) {
		return resumeAction{}, errResumeConflict
	}
	for _, previous := range c.data.Recoveries {
		if previous.ID == confirmation.ActionID {
			return resumeAction{}, errResumeConflict
		}
	}
	action := resumeAction{ID: confirmation.ActionID, Run: run, Message: message, Role: role, Inspection: inspection, Status: "pending"}
	c.data.Recoveries = append(c.data.Recoveries, action)
	for i := range c.data.Attempts {
		if c.data.Attempts[i].Session == inspection.Session {
			c.data.Attempts[i].Proof = nil
		}
	}
	if err := c.saveLocked(); err != nil {
		// Keep a consumed in-memory ID even when publication failed. On disk either
		// nothing launched, or a pending action will become uncertain after restart.
		c.data.Recoveries[len(c.data.Recoveries)-1].Status = "uncertain"
		c.data.Recoveries[len(c.data.Recoveries)-1].Error = resumeUncertain
		c.persistenceFailureLocked(nil)
		return resumeAction{}, err
	}
	return action, nil
}
func (c *Canvas) finishResume(id, status, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.data.Recoveries {
		if c.data.Recoveries[i].ID == id {
			if status == "succeeded" && c.data.Recoveries[i].Status != "succeeded" {
				if a := c.findLocked(c.data.Recoveries[i].Run); a != nil {
					a.Activity = append(a.Activity, Activity{Kind: "resumed", At: time.Now().UTC()})
				}
			}
			c.data.Recoveries[i].Status = status
			c.data.Recoveries[i].Error = message
		}
	}
	if c.saveLocked() != nil {
		c.persistenceFailureLocked(nil)
	}
}

// executeResume performs one claimed Resume. human is the original verified
// request for authenticated mode, or the engine root for unauthenticated local
// mode. Only the Resume RPC itself uses it directly; reads are service-owned.
func (c *Canvas) executeResume(human context.Context, action resumeAction) error {
	defer c.wg.Done()
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-c.ctx.Done():
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return nil
	case <-human.Done():
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return human.Err()
	}
	inspection := c.inspectResume(human, action.Run, action.Message, action.ID)
	expected := action.Inspection
	expected.SessionVersion++
	if !inspection.Available || inspection != expected {
		c.finishResume(action.ID, "refused", resumeUnavailable)
		return nil
	}
	c.mu.Lock()
	a := *c.findLocked(action.Run)
	c.mu.Unlock()
	started := time.Now()
	ctx, cancel := context.WithTimeout(human, 8*time.Minute)
	defer cancel()
	// Keep the original human values, but Close must still stop this RPC.
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	session, err := c.client.Resume(ctx, inspection.Session, false)
	if auth := sessionsAuthError(err); auth != nil {
		// Authorization is rejected before mutation; a later explicit action may retry.
		c.finishResume(action.ID, "refused", resumeUnavailable)
		return auth
	}
	if err != nil || session.GetMetadata().GetUid() != inspection.Session || session.GetMetadata().GetProject() != c.project() || session.GetHarness() != a.Harness || session.GetAnnotations()["bundle-digest"] != a.BundleDigest || !recordedOwner(session, a.ExecutionOwner) {
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return nil
	}
	serviceCtx, sessions, release, err := c.serviceContext(ctx)
	if err != nil {
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return nil
	}
	defer release()
	records, err := sessionclient.ReadRecovery(serviceCtx, sessions.Sessions(), inspection.Session, session.GetLastSeq())
	if err != nil {
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return nil
	}
	turn, err := resumedTurn(records, inspection, session)
	if err != nil {
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return nil
	}
	if action.Message != "" {
		err = c.completeQuestion(action.Message, turn, nil, started)
	} else {
		err = c.update(a.ID, func(saved *attempt) { saved.Error = ""; observeTurn(saved, turn, true) })
		if err == nil {
			var reason string
			reason, err = c.publishRunOutput(a, turn, session)
			if err != nil && reason != "" {
				// Keep the same bounded validator/publication diagnostic as Exec.
				if saveErr := c.update(a.ID, func(saved *attempt) { saved.Error = reason; saved.Completed = time.Now().UTC() }); saveErr != nil {
					err = saveErr
				}
			}
		}
		if err == nil {
			err = c.update(a.ID, func(saved *attempt) {
				saved.Completed = time.Now().UTC()
				addActivity(saved, "finished", saved.Completed)
			})
		}
	}
	if errors.Is(err, errOutputRejected) {
		if action.Message == "" {
			if saveErr := c.update(a.ID, func(saved *attempt) { saved.Status = "failed" }); saveErr != nil {
				c.finishResume(action.ID, "uncertain", resumeUncertain)
				return nil
			}
		}
		c.finishResume(action.ID, "refused", "The recorded model output failed validation. Resume cannot re-send a completed request; ask again or start a new attempt explicitly.")
		return nil
	}
	if err != nil {
		c.finishResume(action.ID, "uncertain", resumeUncertain)
		return nil
	}
	c.finishResume(action.ID, "succeeded", "")
	return nil
}
func resumedTurn(records []*v1.LogRecord, inspection ResumeInspection, session *v1.Session) (*client.TurnResult, error) {
	turn := &client.TurnResult{Session: session, LastSeq: session.GetLastSeq()}
	outputs, ends := 0, 0
	if inspection.LastSeq < 1 || len(records) < int(inspection.LastSeq) || records[inspection.LastSeq-1].GetContentHash() != inspection.Head || session.GetLastSeq() != int64(len(records)) || session.GetMetadata().GetUid() != inspection.Session {
		return nil, errResumeConflict
	}
	for _, record := range records {
		event := record.GetEvent()
		if event.GetKind() == v1.EventKind_EVENT_TOOL_CALL || event.GetKind() == v1.EventKind_EVENT_TOOL_RESULT || event.GetKind() == v1.EventKind_EVENT_APPROVAL_REQUEST || event.GetKind() == v1.EventKind_EVENT_APPROVAL_RESULT {
			return nil, errResumeConflict
		}
		if record.GetSeq() <= inspection.LastSeq {
			if event.GetExecutionId() == inspection.Execution {
				turn.Records = append(turn.Records, record)
			}
			continue
		}
		if event.GetKind() == v1.EventKind_EVENT_LIFECYCLE {
			life := event.GetLifecycle()
			if event.GetExecutionId() != "" || life == nil || (life.GetKind() != v1.Lifecycle_LIFECYCLE_RESUME && life.GetKind() != v1.Lifecycle_LIFECYCLE_SUSPEND) {
				return nil, errResumeConflict
			}
			continue
		}
		if event.GetExecutionId() != inspection.Execution {
			return nil, errResumeConflict
		}
		turn.Records = append(turn.Records, record)
		switch event.GetKind() {
		case v1.EventKind_EVENT_OUTPUT:
			if outputs != 0 || ends != 0 || !plainMessage(event.GetMessage(), "assistant") {
				return nil, errResumeConflict
			}
			outputs++
			turn.Output = wire.MessageFromProto(event.GetMessage()).Text()
		case v1.EventKind_EVENT_END:
			if outputs != 1 || ends != 0 || event.GetEnd() == nil || event.GetEnd().GetState() != "COMPLETED" {
				return nil, errResumeConflict
			}
			ends++
		case v1.EventKind_EVENT_USAGE:
			if outputs != 1 || ends != 0 || event.GetUsage() == nil {
				return nil, errResumeConflict
			}
		default:
			return nil, errResumeConflict
		}
	}
	if outputs != 1 || ends != 1 {
		return nil, errResumeConflict
	}
	return turn, nil
}

func (c *Canvas) recoveryMetadataLocked(session, message string) (string, string) {
	status, reason := "", ""
	uncertain := false
	for _, action := range c.data.Recoveries {
		if action.Inspection.Session == session && action.Message == message {
			uncertain = uncertain || action.Status == "uncertain" || action.Error == resumeUncertain
			status, reason = action.Status, action.Error
		}
	}
	if uncertain && status != "uncertain" {
		reason = "An earlier Resume outcome was uncertain. The latest action does not establish what that earlier request did."
	}
	return status, reason
}
