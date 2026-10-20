package canvas

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/wire"
)

const olderAnnouncementWarning = "A newer version exists; approving publishes this older version"

var (
	errAnnouncement         = errors.New("Announcement evidence is unavailable or changed. Inspect again before acting.")
	errAnnouncementAccepted = errors.New("An accepted proposal changing this action's document is required.")
	errAnnouncementLimit    = errors.New("The local announcement request limit has been reached.")
	errAnnouncementHost     = errors.New("Local simulated announcements are unavailable on this host; inspect before retrying.")
)

type AnnouncementConfirmation struct {
	Confirmed       bool   `json:"confirmed"`
	Session         string `json:"session"`
	ExecutionID     string `json:"executionID"`
	ToolCallID      string `json:"toolCallID"`
	RequestSeq      int64  `json:"requestSeq"`
	Head            int64  `json:"head"`
	Revision        string `json:"revision"`
	Digest          string `json:"digest"`
	CurrentRevision string `json:"currentRevision"`
	Newer           bool   `json:"newer"`
	Unavailable     bool   `json:"unavailable,omitempty"`
}
type AnnouncementView struct {
	Reviewer        sharedspace.ReviewActor  `json:"reviewer,omitzero"`
	IdentitySource  string                   `json:"identitySource,omitempty"`
	ID              string                   `json:"id"`
	Session         string                   `json:"session"`
	Status          string                   `json:"status"`
	Concluded       bool                     `json:"concluded"`
	Snapshot        announcement.Snapshot    `json:"snapshot"`
	Warning         string                   `json:"warning,omitempty"`
	Newer           bool                     `json:"newer"`
	CurrentRevision string                   `json:"currentRevision"`
	Confirmation    AnnouncementConfirmation `json:"confirmation"`
	Receipt         *announcement.Receipt    `json:"receipt,omitempty"`
}
type announcementDecision struct {
	Identity sessionauth.Identity `json:"identity,omitzero"`
	Approved bool
	Role     string
	Reason   string
}
type announcementOperation struct {
	FlowRun     string                  `json:"flowRun,omitempty"`
	TriggeredBy sharedspace.ReviewActor `json:"triggeredBy,omitzero"`
	Snapshot    announcement.Snapshot   `json:"snapshot"`
	Session     string                  `json:"session,omitempty"`
	InitialHead int64                   `json:"initialHead"`
	Stage       string                  `json:"stage"`
	Decision    *announcementDecision   `json:"decision,omitempty"`
	// The engine owns the session; the requester is attribution only.
	ExecutionOwner api.IdentityRef         `json:"executionOwner,omitzero"`
	RequestedBy    sharedspace.ReviewActor `json:"requestedBy,omitzero"`
}
type AnnouncementSummary struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	Proposal string `json:"proposal"`
	Stage    string `json:"stage"`
}
type DeclaredAction struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Document string `json:"document"`
	Path     string `json:"path"`
}

func (c *Canvas) announcementOperation(id string) (announcementOperation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, op := range c.data.Announcements {
		if op.Snapshot.Operation == id {
			return op, nil
		}
	}
	return announcementOperation{}, errAnnouncement
}
func (c *Canvas) saveAnnouncement(op announcementOperation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.data.Announcements {
		if c.data.Announcements[i].Snapshot.Operation == op.Snapshot.Operation {
			c.data.Announcements[i] = op
			c.observeFlowActionLocked(op)
			err := c.saveLocked()
			if err != nil {
				c.data.Announcements[i].Stage = "uncertain"
				if f := c.flowLocked(op.FlowRun); f != nil {
					f.State = "uncertain"
					f.Reason = "Action bookkeeping could not be persisted."
				}
			}
			return err
		}
	}
	return errAnnouncement
}
func (c *Canvas) RequestAnnouncement(ctx context.Context, action, proposal, role string) (string, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if err := c.competingFlow(); err != nil {
		return "", err
	}
	ps, err := c.space.Proposals()
	if err != nil {
		return "", err
	}
	for _, p := range ps {
		if p.ID == proposal && p.Attribution.FlowRun != "" {
			return "", ErrFlowConflict
		}
	}
	return c.requestAnnouncementLocked(ctx, action, proposal, role, "")
}
func (c *Canvas) requestAnnouncementLocked(ctx context.Context, action, proposal, role, flowID string) (string, error) {
	_, requestedBy, authErr := c.requester(ctx, role)
	if authErr != nil {
		return "", authErr
	}
	if c.definition == nil || !c.flowRole(role) {
		return "", errAnnouncement
	}
	a, ok := c.definition.actions[action]
	if !ok {
		return "", errAnnouncement
	}
	doc := c.definition.documents[a.Document]
	proposals, err := c.space.Proposals()
	if err != nil {
		return "", errAnnouncement
	}
	var revision string
	for _, p := range proposals {
		if p.ID == proposal && p.Status == "accepted" && p.AcceptedCommit != "" {
			if _, ok := p.Changes[doc.path]; ok {
				revision = p.AcceptedCommit
			}
		}
	}
	if revision == "" {
		return "", errAnnouncementAccepted
	}
	content, err := c.space.Read(revision, doc.path)
	if err != nil {
		return "", errAnnouncement
	}
	c.mu.Lock()
	for _, op := range c.data.Announcements {
		if op.Stage != "abandoned" && op.Snapshot.Action == action && op.Snapshot.Proposal == proposal {
			c.mu.Unlock()
			return op.Snapshot.Operation, nil
		}
	}
	if len(c.data.Announcements) >= 128 {
		c.mu.Unlock()
		return "", errAnnouncementLimit
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		c.mu.Unlock()
		return "", errAnnouncement
	}
	snapshot := announcement.Snapshot{Operation: fmt.Sprintf("%x", nonce), Definition: c.definition.Digest, Action: action, Document: a.Document, Path: doc.path, Proposal: proposal, Revision: revision, Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(content))), Content: content}
	if announcement.Validate(snapshot) != nil {
		c.mu.Unlock()
		return "", errAnnouncement
	}
	// The expected engine owner is journaled before CreateSession; the
	// requester is attribution only.
	owner, err := c.executionOwnerLocked()
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	if c.service != nil {
		c.data.ExecutionOwner = owner
	}
	op := announcementOperation{Snapshot: snapshot, Stage: "pending", ExecutionOwner: owner, RequestedBy: requestedBy, FlowRun: flowID}
	if flowID != "" {
		f := c.flowLocked(flowID)
		if f == nil || f.Accepted[a.Document] != proposal {
			c.mu.Unlock()
			return "", ErrFlowConflict
		}
		f.Action = snapshot.Operation
		f.State = "action"
		op.RequestedBy = f.RequestedBy
		op.TriggeredBy = requestedBy
	}
	c.data.Announcements = append(c.data.Announcements, op)
	err = c.saveLocked()
	if err != nil {
		c.data.Announcements[len(c.data.Announcements)-1].Stage = "uncertain"
	}
	c.mu.Unlock()
	if err != nil {
		return snapshot.Operation, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	serviceCtx, sessions, release, err := c.serviceContext(ctx)
	var sess *v1.Session
	if err == nil {
		sess, err = sessions.CreateSession(serviceCtx, &v1.Session{Metadata: &v1.ResourceMetadata{Name: "Simulated announcement", Project: c.project()}, Harness: announcement.HarnessName, Annotations: map[string]string{"canvas-operation": snapshot.Operation, "canvas-definition": snapshot.Definition}, Identity: &v1.IdentityRef{Principal: owner.Principal, Issuer: owner.Issuer, Subject: owner.Subject}})
		release()
	}
	if err != nil {
		op.Stage = "uncertain"
		if sessionsAuthError(err) != nil {
			op.Stage = "abandoned"
		}
		if save := c.saveAnnouncement(op); save != nil {
			return snapshot.Operation, save
		}
		return snapshot.Operation, sessionsFailure(err, errAnnouncementHost)
	}
	op.Session = sess.GetMetadata().GetUid()
	op.InitialHead = sess.GetLastSeq()
	if op.Session == "" || !recordedOwner(sess, owner) {
		op.Stage = "uncertain"
		_ = c.saveAnnouncement(op)
		return snapshot.Operation, errAnnouncement
	}
	if c.saveAnnouncement(op) != nil {
		return snapshot.Operation, errAnnouncement
	}
	err = c.execAnnouncement(ctx, op)
	op.Stage = "idle"
	if err != nil {
		op.Stage = "uncertain"
	}
	if save := c.saveAnnouncement(op); save != nil {
		return snapshot.Operation, save
	}
	if err != nil {
		return snapshot.Operation, sessionsFailure(err, errAnnouncement)
	}
	return snapshot.Operation, nil
}
func (c *Canvas) execAnnouncement(ctx context.Context, op announcementOperation) error {
	raw, err := announcement.Encode(op.Snapshot)
	if err != nil {
		return errAnnouncement
	}
	// Only the native pending request proves parking, regardless of Exec's transport outcome.
	serviceCtx, sessions, release, err := c.serviceContext(ctx)
	if err != nil {
		return sessionsFailure(err, errAnnouncement)
	}
	_, execErr := sessions.Exec(serviceCtx, client.ExecOptions{Session: op.Session, Inputs: []string{string(raw)}, ExpectedLastSeq: &op.InitialHead})
	release()
	if auth := sessionsAuthError(execErr); auth != nil {
		return auth
	}
	if v, proof := c.inspectAnnouncement(ctx, op); proof != nil || v.Status != "pending-approval" {
		return sessionsFailure(proof, errAnnouncement)
	}
	return nil
}
func (c *Canvas) InspectAnnouncement(ctx context.Context, id string) (AnnouncementView, error) {
	op, err := c.announcementOperation(id)
	if err != nil {
		return AnnouncementView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.inspectAnnouncement(ctx, op)
}
func (c *Canvas) inspectAnnouncement(ctx context.Context, op announcementOperation) (AnnouncementView, error) {
	s := op.Snapshot
	if c.definition == nil || s.Definition != c.definition.Digest || announcement.Validate(s) != nil {
		return AnnouncementView{}, errAnnouncement
	}
	action, ok := c.definition.actions[s.Action]
	if !ok || action.Document != s.Document || c.definition.documents[s.Document].path != s.Path {
		return AnnouncementView{}, errAnnouncement
	}
	content, err := c.space.Read(s.Revision, s.Path)
	if err != nil || content != s.Content {
		return AnnouncementView{}, errAnnouncement
	}
	v := AnnouncementView{ID: s.Operation, Session: op.Session, Status: "uncertain", Snapshot: s, CurrentRevision: s.Revision}
	history, err := c.space.History()
	if err != nil {
		return v, errAnnouncement
	}
	ps, err := c.space.Proposals()
	if err != nil {
		return v, errAnnouncement
	}
	accepted := false
	for _, p := range ps {
		if p.ID == s.Proposal && p.Status == "accepted" && p.AcceptedCommit == s.Revision && p.Changes[s.Path] == s.Content {
			accepted = true
		}
	}
	if !accepted {
		return v, errAnnouncement
	}
	// History is newest first. Unrelated checkpoint changes are not document revisions.
	found := false
	for _, h := range history {
		if h.Commit == s.Revision {
			found = true
			break
		}
		for _, p := range ps {
			if p.ID == h.Proposal && p.AcceptedCommit == h.Commit {
				if _, touch := p.Changes[s.Path]; touch && v.CurrentRevision == s.Revision {
					v.CurrentRevision = h.Commit
				}
			}
		}
	}
	if !found {
		return v, errAnnouncement
	}
	v.Newer = v.CurrentRevision != s.Revision
	if v.Newer {
		v.Warning = olderAnnouncementWarning
	}
	v.Confirmation = AnnouncementConfirmation{Session: op.Session, Revision: s.Revision, Digest: s.Digest, CurrentRevision: v.CurrentRevision, Newer: v.Newer}
	if op.Stage == "abandoned" {
		v.Status = "abandoned"
		return v, nil
	}
	// Preserve the local snapshot, but never read, resume or retire an unknown
	// owner's action. Keeping its deduplication fence avoids repeating an effect
	// whose outcome cannot be established under the current service binding.
	c.mu.Lock()
	owned := c.ownedLocked(op.ExecutionOwner)
	c.mu.Unlock()
	if !owned {
		v.Status, v.Confirmation.Unavailable = "historical", true
		v.Warning = "Historical execution owner; no decisions or continuation are available. Start a fresh document attempt for a new proposal."
		return v, nil
	}
	if op.Session == "" {
		return v, nil
	}
	serviceCtx, sessions, release, err := c.serviceContext(ctx)
	if err != nil {
		return v, sessionsFailure(err, errAnnouncement)
	}
	defer release()
	sess, err := sessions.GetSession(serviceCtx, op.Session)
	if err != nil {
		if auth := sessionsAuthError(err); auth != nil {
			return v, auth
		}
		if serviceCtx.Err() != nil {
			return v, errAnnouncement
		}
		// This proves only local eligibility to abandon, never absence of native work.
		v.Status, v.Confirmation.Unavailable = "unavailable", true
		return v, nil
	}
	if sess.GetMetadata().GetUid() != op.Session || !recordedOwner(sess, op.ExecutionOwner) || sess.GetMetadata().GetProject() != c.project() || sess.GetHarness() != announcement.HarnessName || sess.GetAnnotations()["canvas-operation"] != s.Operation || sess.GetAnnotations()["canvas-definition"] != s.Definition {
		return v, errAnnouncement
	}
	v.Confirmation.Head = sess.GetLastSeq()
	var records []*v1.LogRecord
	if sess.GetLastSeq() > 0 {
		records, err = sessionclient.ReadRecovery(serviceCtx, sessions.Sessions(), op.Session, sess.GetLastSeq())
		if err != nil {
			return v, sessionsFailure(err, errAnnouncement)
		}
	}
	var execution string
	inputs, calls, requests, decisions, results := 0, 0, 0, 0, 0
	var requestSeq, decisionSeq int64
	approved := false
	for _, r := range records {
		e := wire.EventFromProto(r.GetEvent())
		switch e.Kind {
		case api.EventExecutionStart:
			if e.ExecutionStart == nil || execution != "" || e.ExecutionStart.Harness != announcement.HarnessName || e.ExecutionStart.HarnessVersion != "1" || e.ExecutionStart.InputCount == nil || *e.ExecutionStart.InputCount != 1 {
				return v, errAnnouncement
			}
			execution = e.ExecutionID
		case api.EventInput:
			if e.ExecutionID != execution || e.Message == nil {
				return v, errAnnouncement
			}
			got, err := announcement.Decode([]byte(e.Message.Text()))
			if err != nil || got != s {
				return v, errAnnouncement
			}
			inputs++
		case api.EventModelCall:
			return v, errAnnouncement
		case api.EventToolCall:
			if inputs != 1 || e.ExecutionID != execution || e.ToolCall == nil || e.ToolCall.ID != s.Operation || e.ToolCall.IdempotencyKey != s.Operation || e.ToolCall.Tool != announcement.ToolName || e.ToolCall.Mediation != api.MediationRequiresApproval {
				return v, errAnnouncement
			}
			got, err := announcement.FromArgs(e.ToolCall.Args)
			if err != nil || got != s {
				return v, errAnnouncement
			}
			calls++
		case api.EventApprovalRequest:
			if calls != 1 || e.ExecutionID != execution || e.Approval == nil || e.Approval.ToolCallID != s.Operation {
				return v, errAnnouncement
			}
			requests++
			requestSeq = r.GetSeq()
		case api.EventApprovalResult:
			if requests != 1 || e.ExecutionID != execution || e.ApprovalResult == nil || e.ApprovalResult.ToolCallID != s.Operation || e.ApprovalResult.RequestSeq != requestSeq {
				return v, errAnnouncement
			}
			decisions++
			decisionSeq = r.GetSeq()
			approved = e.ApprovalResult.Approved
			if op.Decision != nil && op.Decision.Identity.Valid() && e.Actor == op.Decision.Identity.Ref() && approved == op.Decision.Approved && e.ApprovalResult.Reason == op.Decision.Reason {
				v.Reviewer = sharedspace.ReviewActor{Issuer: op.Decision.Identity.Issuer, Subject: op.Decision.Identity.Subject, Role: op.Decision.Role}
				v.IdentitySource = "engine-verified"
			}
		case api.EventToolResult:
			result := e.Result
			if decisions != 1 || e.ExecutionID != execution || result == nil || result.ID != s.Operation || result.ApprovalRequestSeq != requestSeq || result.ApprovalDecisionSeq != decisionSeq {
				return v, errAnnouncement
			}
			results++
			if result.IsError {
				if !approved && result.Code != api.ToolResultCodeApprovalDenied {
					return v, errAnnouncement
				}
				v.Status = "failed"
				if !approved {
					v.Status = "denied"
				}
			} else {
				if !approved || result.Code != api.ToolResultCodeUnspecified {
					return v, errAnnouncement
				}
				receipt, err := announcement.DecodeReceipt(result.Output)
				if err != nil || receipt.Snapshot != s {
					return v, errAnnouncement
				}
				v.Receipt = &receipt
				v.Status = "simulated"
			}
		}
	}
	v.Concluded = results == 1
	if inputs > 1 || calls > 1 || requests > 1 || decisions > 1 || results > 1 {
		return v, errAnnouncement
	}
	if pending := sess.GetPendingApproval(); pending != nil {
		if results != 0 || pending.GetExecutionId() != execution || pending.GetToolCallId() != s.Operation || pending.GetRequestSeq() != requestSeq {
			return v, errAnnouncement
		}
	}
	if results == 0 {
		switch {
		case decisions == 1:
			v.Status = "denied"
			if approved {
				v.Status = "approved"
			}
		case requests == 1:
			v.Status = "pending-approval"
			if sess.GetPendingApproval() == nil {
				v.Status, v.Confirmation.Unavailable = "unavailable", true
			}
		default:
			v.Status = "recoverable"
			if execution != "" && inputs == 0 && calls == 0 && requests == 0 && decisions == 0 && results == 0 && sess.GetPendingApproval() == nil {
				v.Status = "incomplete"
			}
		}
	}
	v.Confirmation.ExecutionID = execution
	v.Confirmation.ToolCallID = s.Operation
	v.Confirmation.RequestSeq = requestSeq
	latest, err := sessions.GetSession(serviceCtx, op.Session)
	if err != nil || latest.GetLastSeq() != sess.GetLastSeq() {
		return v, sessionsFailure(err, errAnnouncement)
	}
	return v, nil
}
func (c *Canvas) confirmedAnnouncement(ctx context.Context, id string, confirmation AnnouncementConfirmation) (announcementOperation, AnnouncementView, error) {
	op, err := c.announcementOperation(id)
	if err != nil {
		return op, AnnouncementView{}, err
	}
	v, err := c.inspectAnnouncement(ctx, op)
	if err != nil {
		return op, v, err
	}
	confirmation.Confirmed = false
	if confirmation != v.Confirmation {
		return op, v, errAnnouncement
	}
	return op, v, nil
}
func (c *Canvas) DecideAnnouncement(ctx context.Context, id, role string, approved bool, confirmation AnnouncementConfirmation) error {
	principal, authErr := c.actionIdentity(ctx, role)
	if authErr != nil {
		return authErr
	}
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if !c.flowRole(role) || !confirmation.Confirmed {
		return errAnnouncement
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	op, v, err := c.confirmedAnnouncement(ctx, id, confirmation)
	if err != nil {
		return err
	}
	c.mu.Lock()
	allowed := c.flowActionAllowedLocked(op)
	c.mu.Unlock()
	if !allowed || op.FlowRun != "" && v.Newer {
		return ErrFlowConflict
	}
	if v.Status != "pending-approval" && v.Status != "approved" && v.Status != "denied" || v.Confirmation.RequestSeq < 1 || v.Concluded {
		return errAnnouncement
	}
	reason := "Exact accepted snapshot reviewed."
	if !approved {
		reason = "Exact accepted snapshot denied."
	}
	identity, _ := sessionauth.Caller(ctx)
	decision := announcementDecision{Approved: approved, Role: role, Reason: reason, Identity: identity}
	if op.Decision != nil && *op.Decision != decision {
		return errAnnouncement
	}
	previousDecision := op.Decision
	op.Decision = &decision
	op.Stage = "pending"
	if err := c.saveAnnouncement(op); err != nil {
		return err
	}
	_, err = c.client.Approve(ctx, op.Session, api.ApprovalDecision{ExecutionID: v.Confirmation.ExecutionID, ToolCallID: v.Confirmation.ToolCallID, RequestSeq: v.Confirmation.RequestSeq, Approved: approved, Reason: decision.Reason, Identity: principal})
	op.Stage = "idle"
	if sessionsAuthError(err) != nil {
		// Rejection precedes mutation; another reviewer may explicitly inspect and decide.
		op.Decision = previousDecision
	} else if err != nil {
		op.Stage = "uncertain"
	}
	if save := c.saveAnnouncement(op); save != nil {
		return save
	}
	if err != nil {
		return sessionsFailure(err, errAnnouncement)
	}
	return nil
}
func (c *Canvas) ContinueAnnouncement(ctx context.Context, id, role string, confirmation AnnouncementConfirmation) error {
	return c.resumeAnnouncement(ctx, id, role, confirmation, false)
}
func (c *Canvas) RecoverAnnouncement(ctx context.Context, id, role string, confirmation AnnouncementConfirmation) error {
	return c.resumeAnnouncement(ctx, id, role, confirmation, true)
}
func (c *Canvas) resumeAnnouncement(ctx context.Context, id, role string, confirmation AnnouncementConfirmation, recover bool) error {
	if _, err := c.actionIdentity(ctx, role); err != nil {
		return err
	}
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if !c.flowRole(role) || !confirmation.Confirmed {
		return errAnnouncement
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	op, v, err := c.confirmedAnnouncement(ctx, id, confirmation)
	if err != nil {
		return err
	}
	c.mu.Lock()
	cleanup := !recover && c.flowDeniedCleanupLocked(op)
	allowed := c.flowActionAllowedLocked(op) || cleanup
	c.mu.Unlock()
	if !allowed || !cleanup && op.FlowRun != "" && v.Newer || cleanup && v.Status != "denied" {
		return ErrFlowConflict
	}
	if recover {
		if v.Status != "recoverable" {
			return errAnnouncement
		}
	} else if v.Concluded || v.Confirmation.RequestSeq < 1 || v.Status != "approved" && v.Status != "denied" {
		return errAnnouncement
	}
	if recover && v.Confirmation.ExecutionID == "" && v.Confirmation.Head != op.InitialHead {
		return errAnnouncement
	}
	op.Stage = "pending"
	if err := c.saveAnnouncement(op); err != nil {
		return err
	}
	if recover && v.Confirmation.ExecutionID == "" {
		err = c.execAnnouncement(ctx, op)
	} else {
		_, err = c.client.Sessions().Resume(ctx, &v1.ResumeRequest{Session: op.Session, ExpectedLastSeq: &v.Confirmation.Head})
	}
	if err != nil && sessionsAuthError(err) == nil && !recover {
		// A denied tool ends the native turn with an error. Only its bound native
		// receipt (or a committed successful receipt) establishes a known outcome.
		if proof, inspectErr := c.inspectAnnouncement(ctx, op); inspectErr == nil && proof.Concluded && (proof.Status == "denied" || proof.Receipt != nil) {
			err = nil
		}
	}
	op.Stage = "idle"
	if err != nil && sessionsAuthError(err) == nil {
		op.Stage = "uncertain"
	}
	if save := c.saveAnnouncement(op); save != nil {
		return save
	}
	if err != nil {
		return sessionsFailure(err, errAnnouncement)
	}
	return c.finishFlowAction(ctx, op)
}
func (c *Canvas) AbandonAnnouncement(ctx context.Context, id, role string, confirmation AnnouncementConfirmation) error {
	if _, err := c.actionIdentity(ctx, role); err != nil {
		return err
	}
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if !c.flowRole(role) || !confirmation.Confirmed {
		return errAnnouncement
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	op, view, err := c.confirmedAnnouncement(ctx, id, confirmation)
	if err != nil {
		return err
	}
	if op.FlowRun != "" || view.Status == "historical" {
		return errAnnouncement
	}
	if op.Session == "" {
		if op.Stage != "uncertain" {
			return errAnnouncement
		}
	} else if view.Status != "incomplete" && view.Status != "recoverable" && view.Status != "unavailable" {
		return errAnnouncement
	}
	// Local abandonment is not cancellation. An orphan can still finish parking,
	// but this operation can never record a decision or continue it afterward.
	op.Stage = "abandoned"
	return c.saveAnnouncement(op)
}
