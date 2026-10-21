package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
)

var ErrEditConflict = errors.New("edit request, scope or workspace state conflicts; inspect before acting")

type EditRequest struct {
	FlowRun      string       `json:"flowRun,omitempty"`
	Document     string       `json:"document"`
	BaseRevision string       `json:"baseRevision"`
	BaseDigest   string       `json:"baseDigest"`
	Section      *EditSection `json:"section,omitempty"`
	Instruction  string       `json:"instruction"`
	RequestKey   string       `json:"requestKey"`
	Role         string       `json:"role"`
	CommentRef   string       `json:"commentRef,omitempty"`
}
type EditMaintenance struct {
	Edit         string `json:"edit"`
	Role         string `json:"role"`
	RequestKey   string `json:"requestKey"`
	ExpectedHead string `json:"expectedHead"`
	Confirmed    bool   `json:"confirmed"`
}
type EditRun struct {
	ID             string      `json:"id"`
	Request        EditRequest `json:"request"`
	State          string      `json:"state"`
	Attempt        string      `json:"attempt"`
	Proposal       string      `json:"proposal,omitempty"`
	AcceptedCommit string      `json:"acceptedCommit,omitempty"`
	NativeSettled  bool        `json:"nativeSettled,omitempty"`
	Scope          EditScope   `json:"scope"`
}
type editReviewIntent struct {
	Action       string                  `json:"action"`
	Reviewer     sharedspace.ReviewActor `json:"reviewer"`
	ObservedHead string                  `json:"observedHead"`
}

type editSettlement struct {
	Session   string `json:"session"`
	Execution string `json:"execution"`
	Head      int64  `json:"head"`
	Hash      string `json:"hash"`
}

type editRecord struct {
	EditRun
	Digest                string                  `json:"digest"`
	Before                string                  `json:"before"`
	ReadInputs            []string                `json:"readInputs"`
	Generation            uint64                  `json:"generation"`
	PreviousState         string                  `json:"previousState"`
	Settlement            *editSettlement         `json:"settlement,omitempty"`
	PublicationPending    bool                    `json:"publicationPending,omitempty"`
	ResumePending         bool                    `json:"resumePending,omitempty"`
	RerunPending          bool                    `json:"rerunPending,omitempty"`
	ReviewResetCount      uint64                  `json:"reviewResetCount,omitempty"`
	LastUncommittedReview *editReviewIntent       `json:"lastUncommittedReview,omitempty"`
	ReviewApplied         bool                    `json:"reviewApplied,omitempty"`
	ReviewAction          string                  `json:"reviewAction,omitempty"`
	Reviewer              sharedspace.ReviewActor `json:"reviewer"`
	ReconcileKeys         map[string]string       `json:"reconcileKeys,omitempty"`
	ReconciledKeys        map[string]bool         `json:"reconciledKeys,omitempty"`
	Commands              map[string]string       `json:"commands,omitempty"`
	Comment               *ConversationMessage    `json:"comment,omitempty"`
}

func cloneEdit[T any](value T) T {
	raw, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (d *CompiledDefinition) editReads(agent string) []string {
	return slices.Clone(d.editReadScopes[agent])
}

// Only edits get the extra lossless numeric boundary; legacy seeds and manual
// proposals deliberately keep their existing validation behavior.
func validateEditDocument(doc compiledDocument, content string) error {
	if doc.schema != nil {
		value, err := editScopeJSON(content)
		if err != nil || !preservesJSONNumbers(value) {
			return ErrEditScope
		}
	}
	return validateDocument(doc, content)
}
func (c *Canvas) editLocked(id string) *editRecord {
	for i := range c.data.Edits {
		if c.data.Edits[i].ID == id {
			return &c.data.Edits[i]
		}
	}
	return nil
}
func (c *Canvas) editFence() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data.EditSlot != "" {
		return ErrEditConflict
	}
	return nil
}
func (c *Canvas) GetEdit(ctx context.Context, id string) (EditRun, error) {
	if err := ctx.Err(); err != nil {
		return EditRun{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.editLocked(id)
	if r == nil {
		return EditRun{}, errNotFound
	}
	return cloneEdit(r.EditRun), nil
}
func (c *Canvas) RequestEdit(ctx context.Context, q EditRequest) (EditRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return EditRun{}, err
	}
	if q.Section != nil {
		if q.Section.Pointer != nil && !utf8.ValidString(*q.Section.Pointer) {
			return EditRun{}, ErrEditScope
		}
		for _, title := range q.Section.HeadingPath {
			if !utf8.ValidString(title) {
				return EditRun{}, ErrEditScope
			}
		}
	}
	if !utf8.ValidString(q.RequestKey) || !safeText(q.CommentRef, 128) {
		return EditRun{}, ErrEditConflict
	}
	if c.definition == nil || c.authenticated || c.remoteSessions || !c.flowRole(q.Role) || !safeText(q.Instruction, 2000) || strings.TrimSpace(q.Instruction) == "" || !utf8.ValidString(q.Instruction) || !safeText(q.RequestKey, 128) || len(q.RequestKey) < 8 {
		return EditRun{}, ErrEditConflict
	}
	_, who, err := c.requester(ctx, q.Role)
	if err != nil {
		return EditRun{}, err
	}
	who.Role = q.Role
	q = cloneEdit(q)
	digest := commandDigest(q, who)
	c.mu.Lock()
	for _, r := range c.data.Edits {
		if r.Request.RequestKey == q.RequestKey {
			c.mu.Unlock()
			if r.Digest != digest {
				return EditRun{}, ErrEditConflict
			}
			return cloneEdit(r.EditRun), nil
		}
	}
	if len(c.data.Edits) >= 128 || c.data.EditSlot != "" || c.ctx.Err() != nil {
		c.mu.Unlock()
		return EditRun{}, ErrEditConflict
	}
	previous := ""
	var generation uint64
	if q.FlowRun != "" {
		f := c.flowLocked(q.FlowRun)
		if f == nil || f.Attempt == "" || c.flowRecordLocked(f.ID).Generation == 0 || terminalFlow(f.State) || f.State == "uncertain" || f.State == "edited" || f.State == "edit-paused" || f.State == "cancelling" || f.State == "decision" || !c.flowRequester(*f, who, q.Role) || f.Attempts >= f.AgentRuns {
			c.mu.Unlock()
			return EditRun{}, ErrEditConflict
		}
		if c.flowEditPauseLocked(c.flowRecordLocked(f.ID)) != "" {
			c.mu.Unlock()
			return EditRun{}, ErrEditConflict
		}
		for _, edit := range c.data.Edits {
			if edit.Request.FlowRun == f.ID && (edit.RerunPending || edit.ResumePending) {
				c.mu.Unlock()
				return EditRun{}, ErrEditConflict
			}
		}
		previous = f.State
		generation = c.flowRecordLocked(f.ID).Generation
	} else if c.activeFlowLocked() {
		c.mu.Unlock()
		return EditRun{}, ErrEditConflict
	}
	for _, op := range c.data.Announcements {
		if op.Stage == "pending" || op.Stage == "uncertain" || op.EffectReserved && (!op.EffectSettled || op.FlowRun == q.FlowRun && q.FlowRun != "") {
			c.mu.Unlock()
			return EditRun{}, ErrEditConflict
		}
	}
	var comment *ConversationMessage
	if q.CommentRef != "" {
		for _, entry := range c.conversation.Entries {
			if entry.ID == q.CommentRef && entry.Kind == "comment" {
				copy := entry.ConversationMessage
				comment = &copy
			}
		}
		if comment == nil {
			c.mu.Unlock()
			return EditRun{}, ErrEditConflict
		}
	}
	operations := cloneEdit(c.data.Announcements)
	c.mu.Unlock()
	// A document-only edit cannot retire a parked manual action: native Cancel
	// is unavailable, so refuse before reserving an edit or spending its budget.
	if q.FlowRun == "" {
		for _, op := range operations {
			if op.FlowRun != "" || op.Snapshot.Document != q.Document {
				continue
			}
			view, err := c.inspectAnnouncement(ctx, op)
			if err != nil || !view.Concluded || view.Confirmation.Unavailable {
				return EditRun{}, ErrEditConflict
			}
		}
	}
	agent, ok := c.definition.EditAgent(q.Document)
	doc := c.definition.documents[q.Document]
	if !ok || doc.input {
		return EditRun{}, ErrEditConflict
	}
	head, err := c.space.Head()
	if err != nil || head != q.BaseRevision {
		return EditRun{}, ErrEditConflict
	}
	before, err := c.space.Read(head, doc.path)
	if err != nil || editScopeHash(before) != q.BaseDigest {
		return EditRun{}, ErrEditConflict
	}
	scope, err := resolveEditScope(before, q.Section)
	if err != nil {
		return EditRun{}, err
	}
	if comment != nil && comment.Run != "" {
		run, found := c.GetRun(comment.Run)
		if !found {
			return EditRun{}, ErrEditConflict
		}
		ps, e := c.space.Proposals()
		if e != nil {
			return EditRun{}, e
		}
		matched := false
		for _, p := range ps {
			if p.ID == run.Proposal {
				_, matched = p.Changes[doc.path]
			}
		}
		if !matched {
			return EditRun{}, ErrEditConflict
		}
	}
	// The mutable presentation agent map never selects evidence or output scope.
	declared := AgentDefinition{Read: c.definition.editReads(agent), Propose: []string{q.Document}}
	a, err := c.snapshotAgentRequest(agent, q.Instruction, q.Role, 2000, declared)
	if err != nil || a.Base != head {
		return EditRun{}, ErrEditConflict
	}
	id := ID()
	a.EditID = id
	a.RequestedBy = who
	a.FlowStage = "queued"
	var input map[string]any
	if json.Unmarshal([]byte(a.Input), &input) != nil {
		return EditRun{}, ErrEditConflict
	}
	input["edit"] = map[string]any{"id": id, "document": q.Document, "scope": scope, "instruction": q.Instruction, "comment": comment}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 256<<10 {
		return EditRun{}, ErrEditConflict
	}
	a.Input = string(raw)
	r := editRecord{EditRun: EditRun{ID: id, Request: q, State: "queued", Attempt: a.ID, Scope: scope}, Digest: digest, Before: before, Generation: generation, PreviousState: previous, Comment: comment, Commands: map[string]string{}}
	for _, name := range declared.Read {
		if d := c.definition.documents[name]; d.input {
			r.ReadInputs = append(r.ReadInputs, d.path)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owner, e := c.executionOwnerLocked()
	if e != nil {
		return EditRun{}, e
	}
	a.ExecutionOwner = owner
	if q.FlowRun != "" {
		f := c.flowRecordLocked(q.FlowRun)
		if f == nil || f.State != previous || f.Generation != generation || f.Attempts >= f.AgentRuns || f.ExecutionOwner != owner {
			return EditRun{}, ErrEditConflict
		}
		f.Attempts++
		f.EditIDs = append(f.EditIDs, id)
		f.State = "editing"
	}
	if c.service != nil {
		c.data.ExecutionOwner = owner
	}
	a.Order = uint64(len(c.data.Attempts) + 1)
	c.data.Attempts = append(c.data.Attempts, a)
	c.data.Edits = append(c.data.Edits, r)
	c.data.EditSlot = id
	if err := c.saveLocked(); err != nil {
		c.editLocked(id).State = "uncertain"
		return cloneEdit(r.EditRun), err
	}
	c.wg.Add(1)
	go c.execute(a.ID)
	return cloneEdit(r.EditRun), nil
}

// Called with mu held at the existing pre-Create/pre-Exec native claim boundary.
func (c *Canvas) claimEditDispatchLocked(a *attempt, stage string) bool {
	r := c.editLocked(a.EditID)
	if r == nil || c.data.EditSlot != r.ID || r.State == "abandoned" || r.State == "stale" {
		return false
	}
	a.FlowStage = stage
	r.State = "running"
	return true
}

// Called by update under mu. Native ambiguity retains the one durable slot.
func (c *Canvas) observeEditAttemptLocked(a *attempt) {
	if a.EditID == "" {
		return
	}
	r := c.editLocked(a.EditID)
	if r == nil || c.data.EditSlot != r.ID || r.ReviewAction != "" {
		return
	}
	if a.Proposal != "" {
		r.Proposal = a.Proposal
		r.State = "proposal"
		return
	}
	if a.Status == "failed" || a.Status == "uncertain" {
		if r.PublicationPending || a.FlowStage == "create-intent" || a.FlowStage == "exec-intent" {
			r.State = "uncertain"
		} else {
			r.State = "failed"
			c.releaseEditLocked(r)
		}
	}
}
func (c *Canvas) releaseEditLocked(r *editRecord) {
	held := c.data.EditSlot == r.ID
	if held {
		c.data.EditSlot = ""
	}
	if f := c.flowLocked(r.Request.FlowRun); held && f != nil && c.flowRecordLocked(f.ID).Generation == r.Generation && !terminalFlow(f.State) && f.State != "cancelling" {
		f.State = "edit-paused"
		if r.State == "accepted" {
			f.State = "edited"
		}
		f.Reason = "Explicit edit resume, rerun or cancellation is required."
	}
}
func (c *Canvas) editChanges(r editRecord, a attempt, output string) (map[string]string, error) {
	changes, e := c.definition.Changes(a.Agent, output)
	doc := c.definition.documents[r.Request.Document]
	if e != nil || len(changes) != 1 {
		return nil, ErrEditScope
	}
	text, ok := changes[doc.path]
	if !ok || validateEditDocument(doc, text) != nil || validateEditScope(r.Before, text, r.Scope) != nil {
		return nil, ErrEditScope
	}
	return changes, nil
}
func (c *Canvas) publishEditOutput(a attempt, turn *client.TurnResult, session *v1.Session) (string, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	return c.publishEditLocked(a, turn.Output, session)
}
func (c *Canvas) publishEditLocked(a attempt, output string, session *v1.Session) (string, error) {
	a.Session = session.GetMetadata().GetUid()
	c.mu.Lock()
	saved := c.editLocked(a.EditID)
	if saved == nil || c.data.EditSlot != a.EditID || saved.State == "abandoned" {
		c.mu.Unlock()
		return "Edit output is retained as history only.", errOutputRejected
	}
	r := cloneEdit(*saved)
	c.mu.Unlock()
	head, e := c.space.Head()
	if e != nil || head != r.Request.BaseRevision {
		return "Edit base changed; output is not eligible.", ErrEditConflict
	}
	changes, e := c.editChanges(r, a, output)
	if e != nil {
		return "Editor output failed scope or whole-document validation.", e
	}
	ps, e := c.space.Proposals()
	if e != nil {
		return "", e
	}
	var p sharedspace.Proposal
	for _, candidate := range ps {
		if candidate.Attribution.Run == a.ID {
			if p.ID != "" || !maps.Equal(candidate.Changes, changes) || candidate.Base != a.Base || !c.editProposalMatches(r, a, candidate) || !slices.Equal(candidate.Attribution.ReadInputs, r.ReadInputs) {
				return "", ErrEditConflict
			}
			p = candidate
		}
	}
	if p.ID == "" {
		if err := c.update(a.ID, func(*attempt) { c.editLocked(a.EditID).PublicationPending = true }); err != nil {
			return "", err
		}
		p, e = c.space.Propose(a.Base, changes, sharedspace.Attribution{ReadInputs: slices.Clone(r.ReadInputs), Suite: a.Suite.Clone(), Agent: a.Agent, BundleDigest: session.GetAnnotations()["bundle-digest"], AuthoredBundleDigest: session.GetAnnotations()["authored-bundle-digest"], Session: a.Session, Actor: a.Actor, Run: a.ID, Sources: a.Sources, ExecutionOwner: a.ExecutionOwner, RequestedBy: a.RequestedBy})
	}
	if e != nil {
		return "Edit publication is uncertain; reconcile explicitly.", e
	}
	return "", c.update(a.ID, func(saved *attempt) {
		c.editLocked(a.EditID).PublicationPending = false
		saved.Proposal = p.ID
		saved.Status = "proposed"
		saved.FlowStage = "completed"
		saved.Summary = "Scoped edit proposal; accepted bytes are unchanged."
	})
}
func (c *Canvas) reopenEditsLocked() error {
	seen := map[string]bool{}
	for i := range c.data.Edits {
		r := &c.data.Edits[i]
		if r.ID == "" || seen[r.ID] || r.Digest != commandDigest(r.Request, rRequestActor(c, r)) || c.findLocked(r.Attempt) == nil || c.findLocked(r.Attempt).EditID != r.ID {
			return ErrEditConflict
		}
		seen[r.ID] = true
		if r.Commands == nil {
			r.Commands = map[string]string{}
		}
		if r.ID == c.data.EditSlot {
			r.State = "uncertain"
			if f := c.flowLocked(r.Request.FlowRun); f != nil {
				f.State = "editing"
			}
		}
	}
	if len(c.data.Edits) > 128 || c.data.EditSlot != "" && !seen[c.data.EditSlot] {
		return ErrEditConflict
	}
	return nil
}
func rRequestActor(c *Canvas, r *editRecord) sharedspace.ReviewActor {
	if a := c.findLocked(r.Attempt); a != nil {
		return a.RequestedBy
	}
	return sharedspace.ReviewActor{}
}

// A released edit pause is durable, not evidence to reconstruct from an old
// ordinary attempt. Only its explicit resume/rerun command may consume it.
func (c *Canvas) flowEditPauseLocked(f *flowRunRecord) string {
	if f == nil || f.CancelRequested {
		return ""
	}
	for i := len(f.EditIDs) - 1; i >= 0; i-- {
		e := c.editLocked(f.EditIDs[i])
		if e == nil || e.Generation != f.Generation {
			continue
		}
		if e.State == "accepted" && !e.RerunPending {
			for _, cp := range f.EditCheckpoints {
				if cp.Edit == e.ID {
					return ""
				}
			}
			return "edited"
		}
		if (e.State == "rejected" || e.State == "failed" || e.State == "abandoned" || e.State == "stale") && e.Commands["resume"] == "" {
			return "edit-paused"
		}
		return ""
	}
	return ""
}

func (c *Canvas) editCommandLocked(r *editRecord, q EditMaintenance, kind string) error {
	if !q.Confirmed || q.Role != r.Request.Role || len(q.RequestKey) < 8 || !safeText(q.RequestKey, 128) {
		return ErrEditConflict
	}
	digest := commandDigest(q, rRequestActor(c, r))
	if q.RequestKey == r.Request.RequestKey {
		return ErrEditConflict
	}
	if kind == "reconcile" {
		if old, ok := r.ReconcileKeys[q.RequestKey]; ok && old != digest {
			return ErrEditConflict
		}
		return nil
	}
	if old, ok := r.Commands[kind]; ok && old != digest {
		return ErrEditConflict
	}
	return nil
}
func (c *Canvas) AbandonEdit(ctx context.Context, q EditMaintenance) (EditRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.editLocked(q.Edit)
	if r == nil || c.ctx.Err() != nil || c.editCommandLocked(r, q, "abandon") != nil {
		return EditRun{}, ErrEditConflict
	}
	if r.State == "abandoned" && r.Commands["abandon"] != "" {
		return cloneEdit(r.EditRun), nil
	}
	if r.ReviewAction != "" || c.data.EditSlot != r.ID || (r.State != "queued" && r.State != "running" && r.State != "uncertain" && r.State != "proposal") {
		return EditRun{}, ErrEditConflict
	}
	if r.Request.FlowRun != "" {
		f := c.flowRecordLocked(r.Request.FlowRun)
		if f == nil || f.Generation != r.Generation || f.CancelRequested {
			return EditRun{}, ErrEditConflict
		}
	}
	head, e := c.space.Head()
	if e != nil || head != q.ExpectedHead || c.editCommandLocked(r, q, "abandon") != nil {
		return EditRun{}, ErrEditConflict
	}
	r.Commands["abandon"] = commandDigest(q, rRequestActor(c, r))
	r.State = "abandoned"
	c.releaseEditLocked(r)
	e = c.saveLocked()
	return cloneEdit(r.EditRun), e
}
func (c *Canvas) ReconcileEdit(ctx context.Context, q EditMaintenance) (result EditRun, resultErr error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	defer func() {
		if resultErr == nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			r := c.editLocked(q.Edit)
			if r != nil && r.ReconcileKeys[q.RequestKey] == commandDigest(q, rRequestActor(c, r)) && !r.ReconciledKeys[q.RequestKey] {
				if r.ReconciledKeys == nil {
					r.ReconciledKeys = map[string]bool{}
				}
				r.ReconciledKeys[q.RequestKey] = true
				resultErr = c.saveLocked()
			}
		}
	}()
	c.mu.Lock()
	r := c.editLocked(q.Edit)
	if r == nil || c.editCommandLocked(r, q, "reconcile") != nil {
		c.mu.Unlock()
		return EditRun{}, ErrEditConflict
	}
	if r.ReconciledKeys[q.RequestKey] {
		out := cloneEdit(r.EditRun)
		c.mu.Unlock()
		return out, nil
	}
	if r.State == "abandoned" && c.activeExecutions != 0 {
		c.mu.Unlock()
		return EditRun{}, ErrEditConflict
	}
	copy := cloneEdit(*r)
	a := *c.findLocked(r.Attempt)
	head, headErr := c.space.Head()
	if headErr != nil || head != q.ExpectedHead {
		c.mu.Unlock()
		return EditRun{}, ErrEditConflict
	}
	r.Commands["reconcile"] = commandDigest(q, rRequestActor(c, r))
	if r.ReconcileKeys == nil {
		r.ReconcileKeys = map[string]string{}
	}
	r.ReconcileKeys[q.RequestKey] = r.Commands["reconcile"]
	if err := c.saveLocked(); err != nil {
		c.mu.Unlock()
		return EditRun{}, err
	}
	c.mu.Unlock()
	n, e := c.readFlowExecution(ctx, a)
	if e != nil || !n.Settled {
		return copy.EditRun, ErrEditConflict
	}
	if copy.State == "abandoned" {
		// No output processing is reachable here. The exact owned native chain
		// is settled; only the tombstoned attempt's admission status may change.
		c.mu.Lock()
		live := c.editLocked(copy.ID)
		if live == nil || live.State != "abandoned" || c.activeExecutions != 0 || !c.ownedLocked(a.ExecutionOwner) {
			c.mu.Unlock()
			return copy.EditRun, ErrEditConflict
		}
		live.NativeSettled = true
		live.Settlement = &editSettlement{Session: n.Session, Execution: n.Execution, Head: n.Head, Hash: n.Hash}
		saved := c.findLocked(a.ID)
		saved.Status = "abandoned"
		saved.FlowStage = "completed"
		err := c.saveLocked()
		out := cloneEdit(live.EditRun)
		c.mu.Unlock()
		return out, err
	}
	if copy.ReviewAction == "" {
		_, invalid := c.editChanges(copy, a, n.Output)
		if !n.Complete || invalid != nil {
			if err := c.update(a.ID, func(saved *attempt) {
				saved.Status = "failed"
				saved.FlowStage = "completed"
				saved.Error = "The settled edit ended without a valid scoped replacement."
			}); err != nil {
				return copy.EditRun, err
			}
			return c.GetEdit(ctx, copy.ID)
		}
	}
	if !n.Complete {
		return copy.EditRun, ErrEditConflict
	}
	if copy.ReviewAction != "" {
		return c.reconcileEditReviewLocked(copy, a, n.Output)
	}
	service, sessions, release, e := c.serviceContext(ctx)
	if e != nil {
		return copy.EditRun, e
	}
	defer release()
	s, e := sessions.GetSession(service, a.Session)
	if e != nil {
		return copy.EditRun, e
	}
	_, e = c.publishEditLocked(a, n.Output, s)
	out, _ := c.GetEdit(ctx, q.Edit)
	return out, e
}
