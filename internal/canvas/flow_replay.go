package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// FlowProof is diagnostic evidence, never permission to advance or repeat work.
// RecordOnly distinguishes an active journal from a proven terminal history.
type FlowProof struct {
	Complete   bool                 `json:"complete"`
	RecordOnly bool                 `json:"recordOnly"`
	Reason     string               `json:"reason,omitempty"`
	Digest     string               `json:"digest"`
	Run        string               `json:"run"`
	Head       string               `json:"head,omitempty"`
	Sessions   []FlowSessionProof   `json:"sessions,omitempty"`
	Reviews    []FlowReviewEvidence `json:"reviews,omitempty"`
}

// Coverage is either reconstructed or not-dispatched. An empty coverage never
// claims that diagnostics reconstructed an unfinished or unknown execution.
type FlowSessionProof struct {
	Kind, Attempt, Session, Execution, Hash, Coverage string
	Head                                              int64
	Proof                                             sessionclient.Proof
}

type flowReplayRecord struct {
	Flow     flowRunRecord
	Attempts []attempt
	Actions  []announcementOperation
}

// JSON copying includes nested native proof pointers and private attempt DTOs;
// none of the frozen records alias worker-owned memory.
func (c *Canvas) freezeFlowReplayLocked(run string) (flowReplayRecord, error) {
	r := c.flowRecordLocked(run)
	if r == nil {
		return flowReplayRecord{}, errNotFound
	}
	s := flowReplayRecord{Flow: cloneFlowRecord(*r)}
	for _, a := range c.data.Attempts {
		if a.FlowRun == run {
			s.Attempts = append(s.Attempts, a)
		}
	}
	for _, op := range c.data.Announcements {
		if op.FlowRun == run {
			s.Actions = append(s.Actions, op)
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return flowReplayRecord{}, err
	}
	var frozen flowReplayRecord
	err = json.Unmarshal(raw, &frozen)
	return frozen, err
}

// Include proposal refs as well as the accepted head; merely retaining an old
// commit object is not proof that its review is still reachable.
func (c *Canvas) flowReplayRefs(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+filepath.Join(c.space.Root(), ".sharedspace", "git"), "-c", "core.fsmonitor=false", "for-each-ref", "--format=%(refname) %(objectname)")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return string(out), err
}

func flowReplayDigest(v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func flowReplayCovered(p sessionclient.Proof, execution string, head int64) bool {
	return execution != "" && p.ChainValid && p.Reconstructed && p.OutputsMatch &&
		p.LiveUnchanged && p.ModelCalls == 0 && p.ExecutorCalls != nil &&
		*p.ExecutorCalls == 0 && p.Head == head && p.Records == int(head) &&
		len(p.ExecutionIDs) == 1 && p.ExecutionIDs[0] == execution
}

// VerifyFlow reads native execution journals and asks diagnostics to reconstruct
// copies. It deliberately does not call Canvas.Verify, which saves cached proof.
func (c *Canvas) VerifyFlow(ctx context.Context, run string) (FlowProof, error) {
	if c.remoteSessions {
		return FlowProof{}, errRemoteDiagnostics
	}
	if err := ctx.Err(); err != nil {
		return FlowProof{}, err
	}
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()

	c.mu.Lock()
	frozen, err := c.freezeFlowReplayLocked(run)
	owned := err == nil && c.ownedLocked(frozen.Flow.ExecutionOwner)
	c.mu.Unlock()
	if err != nil {
		return FlowProof{}, err
	}
	r := frozen.Flow
	p := FlowProof{Run: run, Complete: true, RecordOnly: !terminalFlow(r.State)}
	fail := func(reason string) {
		p.Complete = false
		if p.Reason == "" {
			p.Reason = reason
		}
	}
	var refs string
	var proposals []sharedspace.Proposal
	deferDigest := func() (FlowProof, error) {
		p.Digest = flowReplayDigest(struct {
			Frozen    flowReplayRecord
			Refs      string
			Proposals []sharedspace.Proposal
			Proof     FlowProof
		}{frozen, refs, proposals, p})
		if e := ctx.Err(); e != nil {
			return p, e
		}
		return p, nil
	}
	if p.RecordOnly {
		fail("Active flow: record-only evidence, not terminal reconstruction.")
	}
	if !owned {
		fail("Recorded execution owner is unavailable.")
		return deferDigest()
	}
	if c.definition == nil || r.Definition != c.definition.Digest {
		fail("Compiled definition binding is unavailable.")
		return deferDigest()
	}
	d, ok := c.definition.flows[r.Flow]
	if !ok {
		fail("Compiled flow is unavailable.")
		return deferDigest()
	}
	steps := d.definition.Steps
	n := len(r.Generations)
	if n == 0 || r.Generation != uint64(n) || r.Attempts != n ||
		n != len(frozen.Attempts) || r.AgentRuns < 1 || n > r.AgentRuns ||
		r.AgentRuns > d.definition.Limits.AgentRuns || c.flowOptions == nil ||
		r.AgentRuns > c.flowOptions.MaxAgentRuns {
		fail("Legacy, incomplete or inconsistent generation/budget history.")
		return deferDigest()
	}
	attempts := make(map[string]attempt, n)
	for _, a := range frozen.Attempts {
		if a.ID == "" {
			fail("Missing attempt identity.")
			return deferDigest()
		}
		if _, exists := attempts[a.ID]; exists {
			fail("Duplicate attempt identity.")
			return deferDigest()
		}
		attempts[a.ID] = a
	}
	seen := map[string]bool{}
	for i, g := range r.Generations {
		a, found := attempts[g.Attempt]
		if !found || seen[g.Attempt] || g.Number != uint64(i+1) || g.Previous != uint64(i) ||
			a.FlowGeneration != g.Number || a.FlowRun != run || a.FlowStep != g.Step ||
			a.ExecutionOwner != r.ExecutionOwner || a.RequestedBy != r.RequestedBy {
			fail("Generation chain or attempt binding is inconsistent.")
			return deferDigest()
		}
		seen[g.Attempt] = true
	}
	last := r.Generations[n-1]
	if r.Attempt != last.Attempt || r.Step != last.Step {
		fail("Current attempt does not match the generation chain.")
		return deferDigest()
	}

	// Proposals may reconcile their private review ledger; freeze refs afterwards.
	proposals, err = c.space.Proposals()
	if err != nil {
		fail("Git proposals are unavailable.")
		return deferDigest()
	}
	p.Head, err = c.space.Head()
	if err != nil {
		fail("Accepted head is unavailable.")
		return deferDigest()
	}
	refs, err = c.flowReplayRefs(ctx)
	if err != nil {
		fail("Git refs are unavailable.")
		return deferDigest()
	}
	ps := map[string]sharedspace.Proposal{}
	for _, proposal := range proposals {
		ps[proposal.ID] = proposal
	}

	// Freeze every session head before calling reconstruction on any session.
	native := make(map[string]flowNativeSnapshot, n)
	versions := map[string]int{}
	for _, g := range r.Generations {
		a := attempts[g.Attempt]
		s := FlowSessionProof{Kind: "agent", Attempt: a.ID, Session: a.Session}
		if a.Session == "" && a.FlowStage == "queued" && a.ExpectedSeq == nil && a.Proposal == "" {
			s.Coverage = "not-dispatched"
			p.Sessions = append(p.Sessions, s)
			continue
		}
		c.mu.Lock()
		busy := c.questionBusyLocked(a.Session)
		versions[a.Session] = c.questionVersionLocked(a.Session)
		c.mu.Unlock()
		if busy {
			fail("Native session has pending work.")
			p.Sessions = append(p.Sessions, s)
			continue
		}
		snapshot, e := c.readFlowExecution(ctx, a)
		native[a.ID] = snapshot
		s.Execution, s.Head, s.Hash = snapshot.Execution, snapshot.Head, snapshot.Hash
		p.Sessions = append(p.Sessions, s)
		if e != nil || !snapshot.Complete || !snapshot.Settled {
			fail("Native agent execution is unavailable, failed or unfinished.")
		}
	}

	// Validate every decision, in generation order, not merely the last gate.
	reviews := map[uint64]flowReviewIntent{}
	evidence := map[uint64]FlowReviewEvidence{}
	var previous uint64
	for i, intent := range r.Reviews {
		if intent.Generation <= previous || intent.Generation > uint64(n) ||
			intent.Attempt != r.Generations[intent.Generation-1].Attempt ||
			intent.Step != r.Generations[intent.Generation-1].Step {
			fail("Review history is unordered or bound to another generation.")
			continue
		}
		previous = intent.Generation
		reviews[intent.Generation] = intent
		v, e := c.inspectFlowReviewLocked(ctx, run, i)
		p.Reviews = append(p.Reviews, v)
		evidence[intent.Generation] = v
		if e != nil || !intent.Applied || !v.Applied || v.CurrentHead != p.Head ||
			(v.State != "accepted" && v.State != "rejected") ||
			(v.State == "accepted" && (v.AcceptedCommit == "" || intent.AcceptedCommit != v.AcceptedCommit)) {
			fail("A typed review lacks an applied, reachable, bound human decision.")
		}
	}
	if len(r.Reviews) == 0 {
		fail("No typed review history is available.")
	}

	position := 0
	accepted := map[string]string{}
	base := r.StartBase
	rejected := false
	for _, g := range r.Generations {
		a := attempts[g.Attempt]
		if rejected || position >= len(steps) || steps[position].Agent == "" ||
			steps[position].ID != g.Step || steps[position].Agent != a.Agent || a.Base != base {
			fail("Attempt order, step or accepted base does not match the compiled flow.")
			continue
		}
		snapshot, hasNative := native[a.ID]
		proposal, hasProposal := ps[a.Proposal]
		if a.Proposal != "" {
			changes, e := c.definition.ChangesForStep(r.Flow, g.Step, snapshot.Output)
			if !hasNative || !hasProposal || e != nil || !maps.Equal(changes, proposal.Changes) ||
				proposal.Base != a.Base || proposal.Attribution.Run != a.ID ||
				proposal.Attribution.FlowRun != run || proposal.Attribution.FlowStep != g.Step ||
				proposal.Attribution.Session != a.Session || proposal.Attribution.Agent != a.Agent ||
				proposal.Attribution.BundleDigest != a.BundleDigest ||
				proposal.Attribution.ExecutionOwner != a.ExecutionOwner ||
				proposal.Attribution.RequestedBy != a.RequestedBy ||
				!maps.Equal(proposal.Attribution.Sources, a.Sources) {
				fail("Actual native output differs from the bound whole proposal.")
			}
		}
		intent, decided := reviews[g.Number]
		if !decided {
			if g.Number != r.Generation {
				// A completed native response can fail document validation
				// before any proposal exists. That counted failure permits an
				// explicit same-step retry, never advancement without a gate.
				_, validation := c.definition.ChangesForStep(r.Flow, g.Step, snapshot.Output)
				rejectedOutput := hasNative && snapshot.Complete && snapshot.Settled && a.FlowStage == "completed" && a.Status == "failed" && a.Proposal == "" && validation != nil
				if !rejectedOutput {
					fail("Superseded attempt has no proven review or output-validation transition.")
				}
			}
			continue
		}
		v := evidence[g.Number]
		if intent.Action == "accept" && v.State == "accepted" {
			for name, doc := range c.definition.documents {
				if _, changed := proposal.Changes[doc.path]; changed {
					accepted[name] = proposal.ID
				}
			}
			base = v.AcceptedCommit
			position++
		} else if intent.Action == "reject" && v.State == "rejected" {
			rejected = intent.RequestKey == ""
		}
	}
	if r.Position != position || !maps.Equal(accepted, r.Accepted) || base != p.Head {
		fail("Accepted bindings, position or current Git head do not match ordered decisions.")
	}
	if r.State == "rejected" && !rejected {
		fail("Terminal rejection lacks attributable native-bound review.")
	}
	if r.State == "exhausted" && (r.Attempts != r.AgentRuns || position >= len(steps)) {
		fail("Exhaustion does not match the counted budget.")
	}
	if r.State == "complete" && (r.CancelRequested || r.Revocation != 0 || rejected) {
		fail("Completed flow has a cancellation or rejection fence.")
	}

	var action *announcementOperation
	var actionView AnnouncementView
	if r.Action != "" || len(frozen.Actions) != 0 {
		if len(frozen.Actions) != 1 || r.Action != frozen.Actions[0].Snapshot.Operation {
			fail("Terminal action history is ambiguous.")
		} else {
			op := frozen.Actions[0]
			action = &op
			if op.FlowGeneration != r.Generation || op.Revoked || r.CancelRequested || r.Revocation != 0 ||
				op.ExecutionOwner != r.ExecutionOwner || op.RequestedBy != r.RequestedBy ||
				position != len(steps)-1 || steps[position].Action != op.Snapshot.Action ||
				r.Accepted[op.Snapshot.Document] != op.Snapshot.Proposal {
				fail("Terminal action is not bound to the current accepted generation.")
			}
			actionView, err = c.inspectAnnouncement(ctx, op)
			s := FlowSessionProof{Kind: "action", Session: op.Session, Execution: actionView.Confirmation.ExecutionID, Head: actionView.Confirmation.Head}
			p.Sessions = append(p.Sessions, s)
			if err != nil || !actionView.Concluded || actionView.Confirmation.Unavailable ||
				(actionView.Receipt == nil && actionView.Status != "denied") ||
				(r.State == "complete" && actionView.Receipt == nil) ||
				(r.State == "denied" && (actionView.Status != "denied" || actionView.Receipt != nil)) {
				fail("Terminal action lacks an actual native receipt or concluded denial.")
			}
		}
	}
	if r.State == "complete" && action == nil && position != len(steps) {
		fail("Completed flow has unaccepted required steps.")
	}
	if r.State == "denied" && action == nil {
		fail("Denied flow has no native action.")
	}

	for i := range p.Sessions {
		s := &p.Sessions[i]
		if s.Coverage == "not-dispatched" {
			continue
		}
		if s.Session == "" || s.Execution == "" || s.Head == 0 {
			fail("Native execution/head binding is missing.")
			continue
		}
		if s.Kind == "agent" && !native[s.Attempt].Complete {
			continue
		}
		if s.Kind == "action" && !actionView.Concluded {
			continue
		}
		s.Proof, err = sessionclient.Verify(ctx, c.address, s.Session)
		if err != nil || !flowReplayCovered(s.Proof, s.Execution, s.Head) {
			fail("Native reconstruction is unavailable or lacks exact zero-call execution coverage.")
		} else {
			s.Coverage = "reconstructed"
		}
	}

	// LiveUnchanged concerns the verifier's own writes, not concurrent writers.
	// Recheck heads, questions, refs and private worker state independently.
	for _, s := range p.Sessions {
		if s.Session == "" {
			continue
		}
		service, sessions, release, e := c.serviceContext(ctx)
		if e == nil {
			latest, getErr := sessions.GetSession(service, s.Session)
			if getErr != nil || latest.GetLastSeq() != s.Head || latest.GetMetadata().GetUid() != s.Session ||
				latest.GetMetadata().GetProject() != c.project() || !recordedOwner(latest, r.ExecutionOwner) {
				fail("Native head or session binding changed during reconstruction.")
			}
			release()
		} else {
			fail("Fresh native head is unavailable.")
		}
		c.mu.Lock()
		if c.questionBusyLocked(s.Session) || c.questionVersionLocked(s.Session) != versions[s.Session] {
			// Action sessions have no question entries; their version is zero.
			fail("Native session acquired another turn during reconstruction.")
		}
		c.mu.Unlock()
	}
	head, e := c.space.Head()
	afterRefs, refErr := c.flowReplayRefs(ctx)
	if e != nil || refErr != nil || head != p.Head || afterRefs != refs {
		fail("Git head or refs changed during reconstruction.")
	}
	c.mu.Lock()
	current, currentErr := c.freezeFlowReplayLocked(run)
	stillOwned := c.ownedLocked(r.ExecutionOwner)
	c.mu.Unlock()
	if currentErr != nil || !stillOwned || flowReplayDigest(current) != flowReplayDigest(frozen) {
		fail("Flow or native worker records changed during reconstruction.")
	}
	return deferDigest()
}
