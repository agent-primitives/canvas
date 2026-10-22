package canvas

import (
	"context"
	"encoding/json"
	"maps"
	"path"
	"reflect"
	"slices"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// editReplayBindings accounts for both directions of the edit association. Edit
// producers intentionally have no FlowRun, so the ordinary generation scan is
// insufficient evidence of either coverage or the spent attempt budget.
func editReplayBindings(s flowReplayRecord) (map[string]editRecord, map[string]attempt, bool) {
	edits := map[string]editRecord{}
	attempts := map[string]attempt{}
	for _, e := range s.Edits {
		if e.ID == "" || edits[e.ID].ID != "" || e.Request.FlowRun != s.Flow.ID {
			return nil, nil, false
		}
		edits[e.ID] = e
	}
	for _, a := range s.EditAttempts {
		if a.ID == "" || attempts[a.ID].ID != "" || a.EditID == "" || a.FlowRun != "" {
			return nil, nil, false
		}
		attempts[a.ID] = a
	}
	if len(edits) != len(s.Flow.EditIDs) || len(attempts) != len(edits) {
		return nil, nil, false
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	var previous uint64
	requester := s.Flow.RequestedBy
	if requester.Role == "" {
		requester.Role = s.Flow.Role
	}
	for _, id := range s.Flow.EditIDs {
		e, found := edits[id]
		a, native := attempts[e.Attempt]
		if !found || seen[id] || !native || used[a.ID] || a.EditID != id || a.ExecutionOwner != s.Flow.ExecutionOwner || a.RequestedBy != requester || e.Generation == 0 || e.Generation > s.Flow.Generation || e.Generation < previous || a.Base != e.Request.BaseRevision || e.Digest != commandDigest(e.Request, a.RequestedBy) {
			return nil, nil, false
		}
		seen[id], used[a.ID], previous = true, true, e.Generation
	}
	return edits, attempts, true
}

// Validate the frozen input and scope against the accepted base, not the latest
// document. Local role attribution is not reconstructed authentication.
func (c *Canvas) inspectEditReplay(ctx context.Context, f flowRunRecord, e editRecord, a attempt, n flowNativeSnapshot, p sharedspace.Proposal) bool {
	agent, ok := c.definition.EditAgent(e.Request.Document)
	doc, exists := c.definition.documents[e.Request.Document]
	if !ok || !exists || doc.input || a.Agent != agent || a.EditID != e.ID || a.Session == "" || a.BundleDigest == "" || !n.Complete || !n.Settled || e.Request.Role != f.Role || a.RequestedBy.Role != e.Request.Role || a.Actor != e.Request.Role || a.Request != e.Request.Instruction || a.FlowGeneration != 0 || a.FlowStep != "" {
		return false
	}
	before, err := c.space.Read(e.Request.BaseRevision, doc.path)
	if err != nil || before != e.Before || editScopeHash(before) != e.Request.BaseDigest {
		return false
	}
	scope, err := resolveEditScope(before, e.Request.Section)
	if err != nil || !reflect.DeepEqual(scope, e.Scope) {
		return false
	}
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(a.Input), &input) != nil {
		return false
	}
	var bound struct {
		ID          string               `json:"id"`
		Document    string               `json:"document"`
		Scope       EditScope            `json:"scope"`
		Instruction string               `json:"instruction"`
		Comment     *ConversationMessage `json:"comment"`
	}
	if json.Unmarshal(input["edit"], &bound) != nil || bound.ID != e.ID || bound.Document != e.Request.Document || bound.Instruction != e.Request.Instruction || !reflect.DeepEqual(bound.Scope, e.Scope) || !reflect.DeepEqual(bound.Comment, e.Comment) {
		return false
	}
	var base string
	if json.Unmarshal(input["acceptedBase"], &base) != nil || base != a.Base {
		return false
	}
	reads := c.definition.editReads(agent)
	expectedInputs := []string{}
	for _, name := range reads {
		if d := c.definition.documents[name]; d.input {
			expectedInputs = append(expectedInputs, d.path)
		}
	}
	if !slices.Equal(e.ReadInputs, expectedInputs) {
		return false
	}
	paths, err := c.space.Paths(a.Base)
	if err != nil {
		return false
	}
	evidence, sources := map[string]string{}, map[string]string{}
	documents := map[string]any{}
	for _, name := range append(slices.Clone(reads), e.Request.Document) {
		if _, seen := documents[name]; seen {
			continue
		}
		d := c.definition.documents[name]
		spec := map[string]any{"path": d.path, "read": slices.Contains(reads, name), "propose": name == e.Request.Document}
		if d.schema != nil {
			spec["schema"] = d.schema.Schema()
		}
		documents[name] = spec
		if !slices.Contains(reads, name) {
			continue
		}
		for _, filename := range paths {
			matched, _ := path.Match(d.path, filename)
			if !matched {
				continue
			}
			text, err := c.space.Read(a.Base, filename)
			if err != nil || validateDocument(d, text) != nil {
				return false
			}
			evidence[filename], sources[filename] = text, a.Base
		}
	}
	if !maps.Equal(a.Sources, sources) || !editReplayInputMatches(input, "evidence", evidence) || !editReplayInputMatches(input, "documents", documents) || !editReplayInputMatches(input, "definition", map[string]string{"id": c.definition.ID, "digest": c.definition.Digest}) || !editReplayInputMatches(input, "request", e.Request.Instruction) || !editReplayInputMatches(input, "role", e.Request.Role) || !editReplayInputMatches(input, "agent", agent) || !editReplayInputMatches(input, "mode", "documents") {
		return false
	}
	changes, err := c.editChanges(e, a, n.Output)
	if e.State == "failed" && e.Proposal == "" && a.Proposal == "" && e.ReviewAction == "" {
		return err != nil && a.Status == "failed" && a.FlowStage == "completed"
	}
	if err != nil {
		return false
	}
	// Abandoned or natively unsettled outcomes remain incomplete; a local
	// tombstone never certifies native settlement.
	if e.Proposal == "" || a.Proposal != e.Proposal || p.ID != e.Proposal || !maps.Equal(changes, p.Changes) || !c.editProposalMatches(e, a, p) || p.Attribution.FlowRun != "" || p.Attribution.FlowStep != "" || !slices.Equal(p.Attribution.ReadInputs, e.ReadInputs) {
		return false
	}
	if e.State == "rejected" && e.ReviewAction == "reject" && e.ReviewApplied && p.Status == "rejected" && e.Reviewer.Role == e.Request.Role {
		if e.Reviewer.Issuer != "" {
			return p.Reviewer == e.Reviewer
		}
		return p.Reviewer == (sharedspace.ReviewActor{})
	}
	if e.State != "accepted" || e.ReviewAction != "accept" || p.Status != "accepted" || e.AcceptedCommit == "" || e.AcceptedCommit != p.AcceptedCommit || e.Reviewer.Role != e.Request.Role {
		return false
	}
	history, err := c.space.History()
	if err != nil || ctx.Err() != nil {
		return false
	}
	for _, h := range history {
		if h.Proposal != p.ID || h.Commit != e.AcceptedCommit {
			continue
		}
		if e.Reviewer.Issuer != "" && e.Reviewer.Subject != "" {
			return p.Reviewer == e.Reviewer && h.Reviewer == e.Reviewer
		}
		return e.Reviewer.Issuer == "" && e.Reviewer.Subject == "" && p.Reviewer == (sharedspace.ReviewActor{}) && h.Reviewer == (sharedspace.ReviewActor{}) && h.Actor == e.Reviewer.Role
	}
	return false
}

func editReplayInputMatches(input map[string]json.RawMessage, field string, expected any) bool {
	var actual, normalized any
	encoded, err := json.Marshal(expected)
	return err == nil && json.Unmarshal(input[field], &actual) == nil && json.Unmarshal(encoded, &normalized) == nil && reflect.DeepEqual(actual, normalized)
}

// Checkpoints are explicit base/position resets, never synthetic human reviews.
func editReplayCheckpoints(f flowRunRecord, edits map[string]editRecord, proven map[string]bool) (map[uint64]editCheckpoint, bool) {
	result := map[uint64]editCheckpoint{}
	used := map[string]bool{}
	var previous uint64
	for _, cp := range f.EditCheckpoints {
		e, ok := edits[cp.Edit]
		if !ok || !proven[cp.Edit] || used[cp.Edit] || cp.BeforeGeneration != e.Generation || cp.BeforeGeneration == 0 || cp.RestartGeneration != cp.BeforeGeneration+1 || cp.RestartGeneration > f.Generation || cp.RestartGeneration <= previous || cp.AcceptedCommit != e.AcceptedCommit {
			return nil, false
		}
		result[cp.RestartGeneration] = cp
		used[cp.Edit], previous = true, cp.RestartGeneration
	}
	// An accepted edit without a restart cannot justify a terminal flow chain.
	for id, e := range edits {
		if e.State == "accepted" && !used[id] {
			return nil, false
		}
	}
	return result, true
}
