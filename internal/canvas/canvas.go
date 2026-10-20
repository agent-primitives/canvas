// Package canvas is a producer. Its only session dependency is the gRPC client;
// controller, Runtime and the session journal are deliberately outside this package.
package canvas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Run struct {
	FlowRun  string `json:"flowRun,omitempty"`
	FlowStep string `json:"flowStep,omitempty"`
	// Reviewer is the legacy alias for the verified requester, never an owner.
	Reviewer sessionauth.Identity `json:"reviewer,omitzero"`
	// ExecutionOwner is the Sessions owner journaled before dispatch. Actor stays
	// an unverified persona or source label.
	ExecutionOwner       api.IdentityRef         `json:"executionOwner,omitzero"`
	RequestedBy          sharedspace.ReviewActor `json:"requestedBy,omitzero"`
	TriggeredBy          sharedspace.ReviewActor `json:"triggeredBy,omitzero"`
	TriggeringReview     string                  `json:"triggeringReview,omitempty"`
	Trigger              string                  `json:"trigger,omitempty"`
	Suite                *contract.SuiteIdentity `json:"suite,omitempty"`
	Evaluation           string                  `json:"evaluation,omitempty"`
	ExecutionDigest      string                  `json:"executionBundleDigest,omitempty"`
	ID                   string                  `json:"id"`
	Created              time.Time               `json:"created"`
	Order                uint64                  `json:"order"`
	Comparison           string                  `json:"comparison,omitempty"`
	PreviousAttempt      string                  `json:"previousAttempt,omitempty"`
	BoardChange          *BoardChangeDTO         `json:"boardChange,omitempty"`
	Agent                string                  `json:"agent"`
	Mode                 string                  `json:"mode"`
	Prospect             string                  `json:"prospect,omitempty"`
	Scenario             string                  `json:"scenario,omitempty"`
	RequestSummary       string                  `json:"requestSummary"`
	Actor                string                  `json:"actor"`
	Session              string                  `json:"session,omitempty"`
	Model                string                  `json:"model,omitempty"`
	AuthoredBundleDigest string                  `json:"authoredBundleDigest,omitempty"`
	Activity             []Activity              `json:"activity,omitempty"`
	ParentSession        string                  `json:"parentSession,omitempty"`
	ForkSeq              int64                   `json:"forkSeq,omitempty"`
	Base                 string                  `json:"base"`
	Status               string                  `json:"status"`
	Summary              string                  `json:"summary"`
	Error                string                  `json:"error,omitempty"`
	Seconds              float64                 `json:"seconds"`
	Started              time.Time               `json:"started"`
	Completed            time.Time               `json:"completed,omitempty"`
	Proposal             string                  `json:"proposal,omitempty"`
	Proof                *sessionclient.Proof    `json:"proof,omitempty"`
	QuestionsSupported   bool                    `json:"questionsSupported"`
	SessionVersion       int                     `json:"sessionVersion"`
	ResumeStatus         string                  `json:"resumeStatus,omitempty"`
	RecoveryError        string                  `json:"recoveryError,omitempty"`
}
type attempt struct {
	Run
	Input        string            `json:"input"`
	Request      string            `json:"request"`
	Report       *InjuryDTO        `json:"report,omitempty"`
	Sources      map[string]string `json:"sources"`
	BundleDigest string            `json:"bundleDigest,omitempty"`
	Harness      string            `json:"harness,omitempty"`
	ExpectedSeq  *int64            `json:"expectedSeq,omitempty"`
	FlowStage    string            `json:"flowStage,omitempty"`
}
type ledger struct {
	Flows []flowRunRecord `json:"flows,omitempty"`
	// ExecutionOwner binds this state to one configured service identity once a
	// service-owned attempt is journaled; rotation requires new state.
	ExecutionOwner   api.IdentityRef         `json:"executionOwner,omitzero"`
	DefinitionDigest string                  `json:"definitionDigest,omitempty"`
	Attempts         []attempt               `json:"attempts"`
	Reports          map[string]string       `json:"reports"`
	Role             string                  `json:"role"`
	Fanouts          map[string]fanoutIntent `json:"fanouts,omitempty"`
	Recoveries       []resumeAction          `json:"recoveries,omitempty"`
	Announcements    []announcementOperation `json:"announcements,omitempty"`
}
type Agent struct {
	Suite          *contract.SuiteIdentity `json:"suite,omitempty"`
	Evaluation     string                  `json:"evaluation,omitempty"`
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Model          string                  `json:"model"`
	Digest         string                  `json:"digest"`
	AuthoredDigest string                  `json:"authoredDigest,omitempty"`
}
type Person struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
}
type State struct {
	Actions       []DeclaredAction         `json:"actions,omitempty"`
	Announcements []AnnouncementSummary    `json:"announcements,omitempty"`
	Definition    *DefinitionInfo          `json:"definition,omitempty"`
	Conversation  []ConversationMessage    `json:"conversation"`
	Agents        []Agent                  `json:"agents"`
	People        []Person                 `json:"people"`
	Role          string                   `json:"role"`
	Documents     []string                 `json:"documents"`
	Head          string                   `json:"head"`
	Runs          []Run                    `json:"runs"`
	Proposals     []sharedspace.Proposal   `json:"proposals"`
	History       []sharedspace.Checkpoint `json:"history"`
	Triggers      []Trigger                `json:"triggers"`
	Prospects     []struct{}               `json:"prospects"`
	SpacePath     string                   `json:"spacePath"`
	Errors        []string                 `json:"errors"`
}
type Canvas struct {
	authenticated    bool
	remoteSessions   bool
	reviewAuthorizer ReviewAuthorizer
	definition       *CompiledDefinition
	mu               sync.Mutex
	publicationMu    sync.Mutex
	// inputHeads is scanMu-owned and deliberately empty after restart: validated
	// source checkpoints must reconcile independently of durable intent dedupe.
	inputHeads map[string]string
	scanMu     sync.Mutex
	// Reports whose checkpoint or queue step failed, keyed like data.Reports.
	// Guarded by scanMu.
	reportRetry  map[string]time.Time
	wg           sync.WaitGroup
	sem          chan struct{}
	root         string
	space        sharedspace.SharedSpace
	client       *client.Client
	address      string
	bundles      map[string]contract.Bundle
	binder       contract.ContractBinder
	triggers     []Trigger
	ctx          context.Context
	cancel       context.CancelFunc
	data         ledger
	conversation conversationLedger
	// service is configured before serving; nil means the local default client.
	service     *ExecutionService
	flowOptions *FlowRuntimeOptions
	errors      []string
	// Instance-local filesystem boundary; nil uses the real directory sync.
	syncDirectory func(string) error
}

func openCanvas(root string, space sharedspace.SharedSpace, cl *client.Client, address string, bundles []contract.Bundle, binder contract.ContractBinder, triggers []Trigger, definition *CompiledDefinition) (*Canvas, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("cannot create private canvas state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Canvas{definition: definition, root: root, space: space, client: cl, address: address, binder: binder, bundles: map[string]contract.Bundle{}, triggers: triggers, ctx: ctx, cancel: cancel, sem: make(chan struct{}, 1)}
	for _, b := range bundles {
		c.bundles[b.ID] = b
	}
	c.data = ledger{Reports: map[string]string{}, Role: "GM"}
	existing := false
	if raw, err := os.ReadFile(filepath.Join(root, "attempts.json")); err == nil {
		existing = true
		if json.Unmarshal(raw, &c.data) != nil {
			cancel()
			return nil, errors.New("invalid canvas attempt ledger")
		}
	} else if !os.IsNotExist(err) {
		cancel()
		return nil, errors.New("cannot read canvas ledger")
	}
	if c.data.Reports == nil {
		c.data.Reports = map[string]string{}
	}
	if c.data.Fanouts == nil {
		c.data.Fanouts = map[string]fanoutIntent{}
	}
	if err := c.bindDefinition(existing); err != nil {
		cancel()
		return nil, err
	}
	if err := c.openConversation(); err != nil {
		cancel()
		return nil, err
	}
	for i := range c.data.Announcements {
		if c.data.Announcements[i].Stage == "pending" {
			c.data.Announcements[i].Stage = "uncertain"
		}
	}
	for i := range c.data.Recoveries {
		if c.data.Recoveries[i].Status == "pending" {
			c.data.Recoveries[i].Status = "uncertain"
			c.data.Recoveries[i].Error = resumeUncertain
		}
	}
	proposals, err := space.Proposals()
	if err != nil {
		cancel()
		return nil, err
	}
	for i := range c.data.Attempts {
		a := &c.data.Attempts[i]
		if a.Order == 0 {
			a.Order = uint64(i + 1)
		}
		if a.Created.IsZero() && !a.Started.IsZero() {
			a.Created = a.Started
		}
		interrupted := a.Status == "queued" || a.Status == "running"
		recovered := false
		for _, p := range proposals {
			if p.Attribution.Run == a.ID {
				recovered = true
				if a.Proposal != p.ID || a.Status != "proposed" || a.Error != "" {
					addActivity(a, "recovered", time.Now().UTC())
					a.Summary = "Proposal recovered from durable Git evidence."
				}
				a.Proposal = p.ID
				a.Status = "proposed"
				// Recovery proves publication, not clean execution completion.
				// Keep a recorded bookkeeping failure visible.
			}
		}
		if interrupted && !recovered {
			a.Status = "uncertain"
			a.Error = "Execution interrupted. Regenerate explicitly; a recorded model intent may lack its completion."
			a.Completed = time.Now().UTC()
			addActivity(a, "interrupted", a.Completed)
		}
	}
	c.reopenFlowsLocked()
	if !validRole(c.data.Role) {
		c.data.Role = "GM"
	}
	if err := c.saveLocked(); err != nil {
		cancel()
		return nil, err
	}
	return c, nil
}
func (c *Canvas) Close() { c.cancel(); c.wg.Wait() }
func ID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random identity unavailable")
	}
	return hex.EncodeToString(b[:])
}
func validRole(role string) bool { return role == "GM" || role == "coach" || role == "owner" }

// Match credential-shaped tokens, not the suffix in ordinary words like risk-reward.
var credentialToken = regexp.MustCompile(`(^|[^A-Za-z0-9_])sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}($|[^A-Za-z0-9_-])`)

func safeText(s string, max int) bool {
	return len(s) <= max && !strings.Contains(s, "\x00") && !credentialToken.MatchString(s) && !strings.Contains(s, "Bearer ") && !strings.Contains(s, "PRIVATE KEY")
}

var errCanvasPublicationUncertain = errors.New("cannot persist canvas state publication")

// errInternal marks a local storage or Git failure, as opposed to a bad request.
// errNotFound marks a request that names a run or proposal that does not exist.
var (
	errInternal       = errors.New("internal canvas failure")
	errNotFound       = errors.New("not found")
	errOutputRejected = errors.New("model output failed validation")
)

func internalError(err error) error {
	if err == nil || errors.Is(err, errInternal) {
		return err
	}
	return fmt.Errorf("%w: %w", errInternal, err)
}

func syncCanvasDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func (c *Canvas) saveLocked() error { return internalError(c.writeLedgerLocked()) }
func (c *Canvas) writeLedgerLocked() error {
	raw, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return errors.New("cannot encode canvas state")
	}
	path := filepath.Join(c.root, "attempts.json")
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot persist canvas state")
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot persist canvas state")
	}
	if os.Rename(path+".tmp", path) != nil {
		return errors.New("cannot commit canvas state")
	}
	syncDirectory := c.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncCanvasDirectory
	}
	if syncDirectory(c.root) != nil {
		// The new ledger is already visible; callers must retain its intent even
		// though crash durability is uncertain, and must not launch external work.
		return errCanvasPublicationUncertain
	}
	return nil
}
func (c *Canvas) queue(a attempt, reportKey string) (string, error) {
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return "", errors.New("canvas stopping")
	}
	owner, err := c.executionOwnerLocked()
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	if reportKey != "" {
		if _, done := c.data.Reports[reportKey]; done {
			c.mu.Unlock()
			return "", nil
		}
		c.data.Reports[reportKey] = a.ID
	}
	// The expected owner is journaled with the attempt before any dispatch;
	// only a verified service binds the whole state against later rotation.
	previousOwner := c.data.ExecutionOwner
	if c.service != nil {
		c.data.ExecutionOwner = owner
	}
	a.ExecutionOwner = owner
	a.Order = uint64(len(c.data.Attempts) + 1)
	c.data.Attempts = append(c.data.Attempts, a)
	if err := c.saveLocked(); err != nil {
		if errors.Is(err, errCanvasPublicationUncertain) {
			c.persistenceFailureLocked(c.findLocked(a.ID))
		} else {
			c.data.Attempts = c.data.Attempts[:len(c.data.Attempts)-1]
			delete(c.data.Reports, reportKey)
			c.data.ExecutionOwner = previousOwner
		}
		c.mu.Unlock()
		return "", err
	}
	c.wg.Add(1)
	c.mu.Unlock()
	go c.execute(a.ID)
	return a.ID, nil
}
func (c *Canvas) Manual(agent, slug, request, role string) (string, error) {
	return c.ManualContext(context.Background(), agent, slug, request, role)
}
func (c *Canvas) ManualContext(ctx context.Context, agent, slug, request, role string) (string, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if err := c.competingFlow(); err != nil {
		return "", err
	}
	identity, requestedBy, err := c.requester(ctx, role)
	if err != nil {
		return "", err
	}
	selectedTrigger := ""
	for _, trigger := range c.triggers {
		if trigger.Type == "manual" && trigger.Agent == agent && selectedTrigger == "" {
			selectedTrigger = trigger.Name
		}
	}
	if selectedTrigger == "" || !validRole(role) || slug != "" {
		return "", errors.New("unsupported manual definition trigger")
	}
	a, err := c.snapshotGeneric(agent, request, role)
	if err != nil {
		return "", err
	}
	a.Trigger = selectedTrigger
	a.Reviewer, a.RequestedBy = identity, requestedBy
	return c.queue(a, "")
}
func (c *Canvas) findLocked(id string) *attempt {
	for i := range c.data.Attempts {
		if c.data.Attempts[i].ID == id {
			return &c.data.Attempts[i]
		}
	}
	return nil
}
func (c *Canvas) update(id string, f func(*attempt)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.findLocked(id)
	if a == nil {
		return errors.New("attempt missing")
	}
	previousActivity := len(a.Activity)
	f(a)
	c.observeFlowAttemptLocked(a)
	if err := c.saveLocked(); err != nil {
		// Only an unpersisted finish from this update is uncertain. A later
		// bookkeeping failure must not erase an already-recorded completion.
		for i := previousActivity; i < len(a.Activity); i++ {
			if a.Activity[i].Kind == "finished" {
				a.Activity = append(a.Activity[:i:i], a.Activity[i+1:]...)
				break
			}
		}
		c.persistenceFailureLocked(a)
		return err
	}
	return nil
}
func (c *Canvas) persistenceFailureLocked(a *attempt) {
	for i := range c.data.Flows {
		if !terminalFlow(c.data.Flows[i].State) {
			c.data.Flows[i].State = "uncertain"
			c.data.Flows[i].Reason = "Canvas persistence failed; no automatic dispatch."
		}
	}
	message := "Canvas state persistence failed. Restore writable storage; no automatic retry will occur."
	seen := false
	for _, current := range c.errors {
		if current == message {
			seen = true
			break
		}
	}
	if !seen {
		c.errors = append(c.errors, message)
	}
	if a != nil {
		a.Error = message
		a.Completed = time.Now().UTC()
		addActivity(a, "failed", a.Completed)
		if a.Proposal != "" {
			a.Status = "proposed"
			a.Summary = "Proposal is durable in Git; producer bookkeeping could not be persisted."
		} else {
			a.Status = "uncertain"
			a.Summary = "Attempt stopped because its state could not be persisted."
		}
	}
}
func (c *Canvas) execute(id string) {
	defer c.wg.Done()
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-c.ctx.Done():
		_ = c.update(id, func(a *attempt) {
			a.Status = "uncertain"
			a.Completed = time.Now().UTC()
			a.Error = "Execution interrupted before it started. Regenerate explicitly."
			addActivity(a, "interrupted", a.Completed)
		}) // update exposes a bounded persistence failure if saving fails.
		return
	}
	c.mu.Lock()
	a := *c.findLocked(id)
	c.mu.Unlock()
	started := time.Now()
	if err := c.update(id, func(a *attempt) {
		a.Status = "running"
		a.Started = started.UTC()
		addActivity(a, "execution-started", a.Started)
		a.Summary = "Agent running with checkpointed declared evidence."
	}); err != nil {
		return
	}
	failureReason := "Agent attempt failed or was interrupted; no documents accepted. Regenerate explicitly."
	finish := func(err error) {
		if saveErr := c.update(id, func(a *attempt) {
			a.Completed = time.Now().UTC()
			a.Seconds = time.Since(started).Seconds()
			if err == nil && a.Error == "" {
				addActivity(a, "finished", a.Completed)
			} else if err != nil {
				kind := "failed"
				if c.ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.Canceled || status.Code(err) == codes.DeadlineExceeded {
					kind = "interrupted"
				}
				addActivity(a, kind, a.Completed)
				if a.Proposal != "" {
					a.Status = "proposed"
					a.Summary = "Proposal is durable in Git; producer bookkeeping is incomplete."
				} else {
					a.Status = "failed"
					if kind == "interrupted" {
						a.Status = "uncertain"
					}
					a.Summary = "No proposal produced."
				}
				a.Error = failureReason
				if auth := sessionsAuthError(err); auth != nil {
					a.Error = status.Convert(auth).Message()
				}
			}
		}); saveErr != nil {
			// update already exposes an honest recoverable state and a bounded
			// storage error in the UI; a background worker cannot return it to HTTP.
			return
		}
	}
	c.mu.Lock()
	owned := a.ExecutionOwner != (api.IdentityRef{}) && c.ownedLocked(a.ExecutionOwner)
	c.mu.Unlock()
	if !owned {
		failureReason = "Execution owner is missing or differs from the configured canvas service; no execution launched. Start a new attempt explicitly."
		finish(errExecutionOwnerChanged)
		return
	}
	// Queued work runs under the engine's own lifetime and service identity,
	// never the admitting HTTP request or its credential.
	serviceCtx, sessions, release, err := c.serviceContext(context.Background())
	if err != nil {
		finish(err)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(serviceCtx, 8*time.Minute)
	defer cancel()
	principal := a.ExecutionOwner
	uid := a.Session
	if uid == "" {
		b, ok := c.bundles[a.Agent]
		if !ok {
			finish(errors.New("bundle unavailable"))
			return
		}
		binding, err := c.binder.Resolve(b)
		if err != nil {
			finish(err)
			return
		}
		// Exec sends no per-turn config, so refuse a binder that needs one.
		if len(binding.Config) != 0 {
			finish(errors.New("bundle execution config unsupported"))
			return
		}
		if a.FlowRun != "" {
			if err := c.update(id, func(saved *attempt) { saved.FlowStage = "create-intent" }); err != nil {
				return
			}
		}
		sess, err := sessions.CreateSession(ctx, &v1.Session{Metadata: &v1.ResourceMetadata{Name: a.RequestSummary, Project: c.project()}, Harness: binding.Harness, Model: b.Model, Labels: map[string]string{"canvas": c.project(), "agent": a.Agent, "mode": a.Mode}, Annotations: map[string]string{"bundle-digest": b.Digest, "authored-bundle-digest": b.AuthoredDigest, "accepted-base": a.Base, "attempt": a.ID}, Identity: &v1.IdentityRef{Principal: principal.Principal, Issuer: principal.Issuer, Subject: principal.Subject}})
		if err != nil {
			finish(err)
			return
		}
		uid = sess.GetMetadata().GetUid()
		if err := c.update(id, func(a *attempt) { a.Session = uid }); err != nil {
			finish(err)
			return
		}
	}
	session, err := sessions.GetSession(ctx, uid)
	if err != nil {
		finish(err)
		return
	}
	if !recordedOwner(session, a.ExecutionOwner) {
		failureReason = "Session owner differs from the recorded execution owner; no execution launched. Start a new attempt explicitly."
		finish(errExecutionOwnerChanged)
		return
	}
	digest := session.GetAnnotations()["bundle-digest"]
	decoded, identityErr := hex.DecodeString(digest)
	if identityErr != nil || len(decoded) != 32 || digest != strings.ToLower(digest) || session.GetHarness() != contract.HarnessName(digest, 4) || digest != c.bundles[a.Agent].Digest || session.GetMetadata().GetProject() != c.project() {
		failureReason = "Session bundle identity missing or inconsistent; no execution launched. Regenerate explicitly."
		finish(errors.New("session bundle identity unavailable"))
		return
	}
	// Checkpoint the actual session identity before any model call or proposal
	// publication; a nominal bundle alone is not execution evidence.
	if err := c.update(id, func(a *attempt) {
		a.BundleDigest = digest
		a.AuthoredBundleDigest = session.GetAnnotations()["authored-bundle-digest"]
		a.Harness = session.GetHarness()
		seq := session.GetLastSeq()
		a.ExpectedSeq = &seq
		if a.FlowRun != "" {
			a.FlowStage = "exec-intent"
		}
	}); err != nil {
		finish(err)
		return
	}
	seq := session.GetLastSeq()
	turn, err := sessions.Exec(ctx, client.ExecOptions{Session: uid, Inputs: []string{a.Input}, ExpectedLastSeq: &seq})
	if turn != nil {
		if saveErr := c.update(id, func(a *attempt) { observeTurn(a, turn, err == nil) }); saveErr != nil {
			finish(saveErr)
			return
		}
	}
	if err != nil {
		finish(err)
		return
	}
	if a.FlowRun != "" {
		if err := c.update(id, func(saved *attempt) { saved.FlowStage = "completed" }); err != nil {
			return
		}
	}
	if reason, err := c.publishRunOutput(a, turn, session); err != nil {
		if reason != "" {
			failureReason = reason
		}
		finish(err)
		return
	}
	finish(nil)
}

// publishRunOutput preserves the original snapshot, validation and independent
// proposal review path for both fresh execution and confirmed recovery.
func (c *Canvas) publishRunOutput(a attempt, turn *client.TurnResult, session *v1.Session) (string, error) {
	id, uid, digest := a.ID, session.GetMetadata().GetUid(), session.GetAnnotations()["bundle-digest"]
	failureReason := ""
	var changes map[string]string
	var err error
	if a.FlowRun != "" {
		c.publicationMu.Lock()
		defer c.publicationMu.Unlock()
		if !c.flowPublishAllowed(a) {
			return "Workflow binding is no longer publishable.", errOutputRejected
		}
		c.mu.Lock()
		f := c.flowLocked(a.FlowRun)
		flowID := f.Flow
		c.mu.Unlock()
		changes, err = c.definition.ChangesForStep(flowID, a.FlowStep, turn.Output)
	} else {
		changes, err = c.definition.Changes(a.Agent, turn.Output)
	}
	if err != nil {
		return err.Error(), errors.Join(errOutputRejected, err)
	}
	if err := c.update(id, func(a *attempt) { addActivity(a, "validated", time.Now().UTC()) }); err != nil {
		return failureReason, err
	}
	var readInputs []string
	if c.definition != nil {
		for _, id := range c.definition.Agents[a.Agent].Read {
			if doc := c.definition.documents[id]; doc.input {
				readInputs = append(readInputs, doc.path)
			}
		}
	}
	proposal, err := c.space.Propose(a.Base, changes, sharedspace.Attribution{ReadInputs: readInputs, Suite: a.Suite.Clone(), Agent: a.Agent, BundleDigest: digest, AuthoredBundleDigest: session.GetAnnotations()["authored-bundle-digest"], Session: uid, Actor: a.Actor, Run: a.ID, Sources: a.Sources, ExecutionOwner: a.ExecutionOwner, RequestedBy: a.RequestedBy, TriggeredBy: a.TriggeredBy, TriggeringReview: a.TriggeringReview, FlowRun: a.FlowRun, FlowStep: a.FlowStep})
	if err != nil {
		if a.FlowRun != "" && proposal.ID != "" {
			_ = c.setFlow(a.FlowRun, "uncertain", "Proposal is durable but publication bookkeeping is ambiguous; no review or automatic dispatch.")
		}
		if proposal.ID != "" {
			failureReason = "Proposal published to Git; review metadata persistence failed. Restore writable storage and refresh."
			if saveErr := c.update(id, func(a *attempt) {
				a.Proposal = proposal.ID
				addActivity(a, "proposed", time.Now().UTC())
				a.Status = "proposed"
				a.Summary = "Proposal is durable in Git; review metadata needs reconciliation."
			}); saveErr != nil {
				return failureReason, saveErr
			}
		}
		return failureReason, err
	}
	if err := c.update(id, func(a *attempt) {
		a.Proposal = proposal.ID
		addActivity(a, "proposed", time.Now().UTC())
		a.Status = "proposed"
		a.Summary = "Agent proposed a document change for human review."
	}); err != nil {
		return failureReason, err
	}
	return "", nil
}
func (c *Canvas) GetRun(id string) (Run, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.findLocked(id)
	if a == nil {
		return Run{}, false
	}
	r := cloneRun(a.Run)
	r.ExecutionDigest = a.BundleDigest
	if c.definition != nil {
		r.Suite = c.recordedSuite(a)
		r.Evaluation = suiteEvaluation(r.Suite)
	}
	r.QuestionsSupported = questionBinding(a)
	r.SessionVersion = c.questionVersionLocked(a.Session)
	r.ResumeStatus, r.RecoveryError = c.recoveryMetadataLocked(a.Session, "")
	return r, true
}
func (c *Canvas) State() (State, error) {
	c.mu.Lock()
	runs := make([]Run, 0, len(c.data.Attempts))
	for _, a := range c.data.Attempts {
		r := cloneRun(a.Run)
		r.ExecutionDigest = a.BundleDigest
		if c.definition != nil {
			r.Suite = c.recordedSuite(&a)
			r.Evaluation = suiteEvaluation(r.Suite)
		}
		r.QuestionsSupported = questionBinding(&a)
		r.SessionVersion = c.questionVersionLocked(a.Session)
		r.ResumeStatus, r.RecoveryError = c.recoveryMetadataLocked(a.Session, "")
		runs = append(runs, r)
	}
	announcements := make([]AnnouncementSummary, 0, len(c.data.Announcements))
	for _, op := range c.data.Announcements {
		announcements = append(announcements, AnnouncementSummary{ID: op.Snapshot.Operation, Action: op.Snapshot.Action, Proposal: op.Snapshot.Proposal, Stage: op.Stage})
	}
	conversation := c.conversationStateLocked()
	role := c.data.Role
	errs := append([]string{}, c.errors...)
	c.mu.Unlock()
	head, err := c.space.Head()
	if err != nil {
		return State{}, internalError(err)
	}
	proposals, err := c.space.Proposals()
	if err != nil {
		return State{}, internalError(err)
	}
	if c.definition != nil {
		c.mu.Lock()
		for i := range proposals {
			if a := c.findLocked(proposals[i].Attribution.Run); a != nil && a.BundleDigest == proposals[i].Attribution.BundleDigest {
				proposals[i].Attribution.Suite = c.recordedSuite(a)
			} else if proposals[i].Attribution.Suite != nil {
				proposals[i].Attribution.Suite = proposals[i].Attribution.Suite.Clone()
				proposals[i].Attribution.Suite.Release = nil
			}
		}
		c.mu.Unlock()
	}
	history, err := c.space.History()
	if err != nil {
		return State{}, internalError(err)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Order < runs[j].Order })
	if proposals == nil {
		proposals = []sharedspace.Proposal{}
	}
	if history == nil {
		history = []sharedspace.Checkpoint{}
	}
	state := State{Announcements: announcements, Conversation: conversation, Role: role, Head: head, Runs: runs, Proposals: proposals, History: history, Triggers: c.triggers, Prospects: []struct{}{}, SpacePath: c.space.Root(), Errors: errs}
	return c.genericState(state)
}
func (c *Canvas) SetRole(role string) error {
	if !validRole(role) {
		return errors.New("invalid demo role")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.Role = role
	return c.saveLocked()
}
func (c *Canvas) Review(id, action, role string) (sharedspace.Proposal, error) {
	return c.ReviewContext(context.Background(), id, action, role)
}
func (c *Canvas) review(id, action, role string) (sharedspace.Proposal, error) {
	if !c.flowRole(role) {
		return sharedspace.Proposal{}, errors.New("invalid demo role")
	}
	if action == "reject" {
		return c.space.Reject(id, role)
	}
	if action != "accept" {
		return sharedspace.Proposal{}, errors.New("unknown review action")
	}
	head, err := c.space.Head()
	if err != nil {
		return sharedspace.Proposal{}, internalError(err)
	}
	return c.space.Accept(id, head, role)
}
func (c *Canvas) Regenerate(proposal, role string) (string, error) {
	return c.RegenerateContext(context.Background(), proposal, role)
}

// RegenerateContext captures the verified requester during admission; the
// queued attempt itself runs under the execution service.
func (c *Canvas) RegenerateContext(ctx context.Context, proposal, role string) (string, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if err := c.competingFlow(); err != nil {
		return "", err
	}
	identity, requestedBy, err := c.requester(ctx, role)
	if err != nil {
		return "", err
	}
	// An empty ID would match the first attempt that has no proposal.
	if proposal == "" {
		return "", errors.New("proposal ID required")
	}
	c.mu.Lock()
	var previous *attempt
	for _, a := range c.data.Attempts {
		if a.Proposal == proposal || a.ID == proposal {
			copy := a
			previous = &copy
			break
		}
	}
	c.mu.Unlock()
	if previous != nil && previous.FlowRun != "" {
		return "", ErrFlowConflict
	}
	if previous == nil {
		return "", fmt.Errorf("proposal attempt missing: %w", errNotFound)
	}
	a, err := c.snapshotGeneric(previous.Agent, previous.Request, role)
	if err != nil {
		return "", err
	}
	a.Comparison = previous.Comparison
	a.PreviousAttempt = previous.ID
	a.Trigger = previous.Trigger
	a.Reviewer, a.RequestedBy = identity, requestedBy
	return c.queue(a, "")
}
func (c *Canvas) Verify(ctx context.Context, id string) (sessionclient.Proof, error) {
	if c.remoteSessions {
		return sessionclient.Proof{}, errRemoteDiagnostics
	}
	r, ok := c.GetRun(id)
	if !ok || r.Session == "" {
		return sessionclient.Proof{}, errors.New("session evidence missing")
	}
	c.mu.Lock()
	version := c.questionVersionLocked(r.Session)
	busy := c.questionBusyLocked(r.Session)
	c.mu.Unlock()
	if busy {
		return sessionclient.Proof{}, ErrQuestionBusy
	}
	proof, err := sessionclient.Verify(ctx, c.address, r.Session)
	if err != nil {
		return sessionclient.Proof{}, errors.New("reconstruction diagnostic unavailable or session incomplete")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.questionVersionLocked(r.Session) != version || c.questionBusyLocked(r.Session) {
		return sessionclient.Proof{}, ErrQuestionBusy
	}
	a := c.findLocked(id)
	if a == nil {
		return sessionclient.Proof{}, errNotFound
	}
	a.Proof = &proof
	if err := c.saveLocked(); err != nil {
		a.Proof = nil
		c.persistenceFailureLocked(a)
		return sessionclient.Proof{}, err
	}
	return proof, nil
}
func (c *Canvas) Document(path, proposal string) (map[string]string, error) {
	allowed := c.definition.Policy.Allows(path)
	if !allowed {
		return nil, errors.New("document outside canvas scope")
	}
	text, err := c.space.Read("", path)
	if c.definition != nil && errors.Is(err, sharedspace.ErrMissingDocument) {
		text, err = "", nil
	}
	if err != nil {
		return nil, internalError(err)
	}
	out := map[string]string{"path": path, "accepted": text}
	if proposal != "" {
		all, err := c.space.Proposals()
		if err != nil {
			return nil, internalError(err)
		}
		found := false
		for _, p := range all {
			if p.ID == proposal {
				found = true
				proposed, changed := p.Changes[path]
				if !changed {
					return nil, errors.New("proposal does not change this document")
				}
				base, err := c.space.Read(p.Base, path)
				if c.definition != nil && errors.Is(err, sharedspace.ErrMissingDocument) {
					base, err = "", nil
				}
				if err != nil {
					return nil, internalError(err)
				}
				out["base"] = base
				out["baseCommit"] = p.Base
				out["proposed"] = proposed
				out["status"] = p.Status
				out["diff"] = documentDiff(path, base, proposed)
				out["comparison"], out["comparisonKind"] = documentComparison(path, base, proposed)
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("proposal missing: %w", errNotFound)
		}
	}
	return out, nil
}
