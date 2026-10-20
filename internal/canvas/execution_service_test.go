package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testIssuer = "https://issuer.example"

var testServiceIdentity = sessionauth.Identity{Issuer: testIssuer, Subject: "canvas-engine-service"}

// The Manager is real; only its token source and verifier are fake boundaries.
func serviceCredentials(verified sessionauth.Identity) *sessionauth.Manager {
	return sessionauth.New(sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) {
		return "service-credential-not-persisted", time.Now().Add(time.Hour), nil
	}), actionVerifier{verified})
}

// humanContext is a verified browser action that also carries outgoing gRPC
// metadata which must never reach service-owned RPCs.
func humanContext(t *testing.T, subject string) context.Context {
	t.Helper()
	return metadata.AppendToOutgoingContext(reviewerContext(t, subject), "x-human-marker", "human-only")
}

type observedRPC struct {
	Method string
	Caller sessionauth.Identity
	Human  bool
}
type rpcLog struct {
	mu       sync.Mutex
	calls    []observedRPC
	onCreate func()
}

func (l *rpcLog) add(ctx context.Context, method string) {
	caller, _ := sessionauth.Caller(ctx)
	md, _ := metadata.FromOutgoingContext(ctx)
	call := observedRPC{Method: method[strings.LastIndex(method, "/")+1:], Caller: caller, Human: len(md.Get("x-human-marker")) != 0}
	l.mu.Lock()
	hook := l.onCreate
	l.calls = append(l.calls, call)
	l.mu.Unlock()
	if call.Method == "CreateSession" && hook != nil {
		hook()
	}
}
func (l *rpcLog) snapshot() []observedRPC {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

func observedClient(t *testing.T, c *Canvas, log *rpcLog) *client.Client {
	t.Helper()
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			log.add(ctx, m)
			return next(ctx, m, req, reply, cc, opts...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, m string, next grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			log.add(ctx, m)
			return next(ctx, desc, cc, m, opts...)
		})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// configureTestService makes c.client the observed human decision client and a
// second observed client the service execution client.
func configureTestService(t *testing.T, c *Canvas, credentials sessionauth.Identity) (human, service *rpcLog) {
	t.Helper()
	human, service = &rpcLog{}, &rpcLog{}
	c.client = observedClient(t, c, human)
	if err := c.ConfigureExecutionService(ExecutionService{Client: observedClient(t, c, service), Credentials: serviceCredentials(credentials), Identity: testServiceIdentity}); err != nil {
		t.Fatal(err)
	}
	return human, service
}

func assertServiceCalls(t *testing.T, log *rpcLog, required ...string) {
	t.Helper()
	var methods []string
	for _, call := range log.snapshot() {
		methods = append(methods, call.Method)
		if call.Caller != testServiceIdentity || call.Human || call.Method == "Approve" || call.Method == "Resume" {
			t.Fatalf("service RPC used human authority: %+v", call)
		}
	}
	for _, method := range required {
		if !slices.Contains(methods, method) {
			t.Fatalf("service RPC %s missing from %v", method, methods)
		}
	}
}

func recordedSessionIdentity(t *testing.T, c *Canvas, uid string) api.IdentityRef {
	t.Helper()
	cl, err := client.Dial(c.address, client.WithProject(c.project()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, err := cl.GetSession(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return api.IdentityRef{Principal: s.GetIdentity().GetPrincipal(), Issuer: s.GetIdentity().GetIssuer(), Subject: s.GetIdentity().GetSubject()}
}

func proposalByID(t *testing.T, c *Canvas, id string) sharedspace.Proposal {
	t.Helper()
	proposals, err := c.space.Proposals()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range proposals {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("proposal %s missing", id)
	return sharedspace.Proposal{}
}

func savedAttempt(t *testing.T, c *Canvas, id string) attempt {
	t.Helper()
	for _, a := range readPublishedLedger(t, c).Attempts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("attempt %s not journaled", id)
	return attempt{}
}

func actor(subject, role string) sharedspace.ReviewActor {
	return sharedspace.ReviewActor{Issuer: testIssuer, Subject: subject, Role: role}
}

// useTestService configures the fixture's own client as the service client for
// existing authenticated tests that swap transports explicitly.
func useTestService(t *testing.T, c *Canvas) {
	t.Helper()
	if err := c.ConfigureExecutionService(ExecutionService{Client: c.client, Credentials: serviceCredentials(testServiceIdentity), Identity: testServiceIdentity}); err != nil {
		t.Fatal(err)
	}
}

// setClients swaps the human decision transport and, when configured, the
// service transport so fault-injection tests cover both paths.
func setClients(c *Canvas, cl *client.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = cl
	if c.service != nil {
		c.service.Client = cl
	}
}

func TestServiceContextRefusesAlreadyCanceledCaller(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, release, err := c.serviceContext(parent)
	if release != nil {
		release()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled caller was admitted: %v", err)
	}
}

func TestLegacyAnnouncementsKeepHistoryAndFenceButAllowFreshProposal(t *testing.T) {
	for _, tc := range []struct {
		owner        api.IdentityRef
		emptySession bool
	}{{}, {owner: api.IdentityRef{Principal: "another-owner"}}, {emptySession: true}, {owner: api.IdentityRef{Principal: "another-owner"}, emptySession: true}} {
		t.Run(fmt.Sprintf("%s/empty-session-%t", tc.owner.Principal, tc.emptySession), func(t *testing.T) {
			c, _, modelCalls := definedFixtureAt(t, actionDefinitionFixture(t), func(map[string]any) string {
				return changeEnvelope("announcement", "# Fresh reviewed announcement\n")
			})
			oldProposal := acceptedAnnouncement(t, c, "# Historical announcement\n")
			oldID, err := c.RequestAnnouncement(context.Background(), "announce", oldProposal.ID, "GM")
			if err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.data.Announcements[0].ExecutionOwner = tc.owner
			if tc.emptySession {
				c.data.Announcements[0].Session = ""
				c.data.Announcements[0].Stage = "uncertain"
			}
			c.mu.Unlock()
			policy, err := NewReviewerPolicy("GM", []sessionauth.Identity{{Issuer: testIssuer, Subject: "human"}})
			if err != nil {
				t.Fatal(err)
			}
			c.EnableAuthentication(policy)
			human, service := configureTestService(t, c, testServiceIdentity)
			ctx := humanContext(t, "human")
			view, err := c.InspectAnnouncement(ctx, oldID)
			if err != nil || view.Status != "historical" || !view.Confirmation.Unavailable || view.Snapshot.Content != "# Historical announcement\n" {
				t.Fatalf("legacy history unavailable: %+v %v", view, err)
			}
			view.Confirmation.Confirmed = true
			if c.ContinueAnnouncement(ctx, oldID, "GM", view.Confirmation) == nil || c.RecoverAnnouncement(ctx, oldID, "GM", view.Confirmation) == nil || c.AbandonAnnouncement(ctx, oldID, "GM", view.Confirmation) == nil {
				t.Fatal("legacy operation could mutate or clear its duplicate-effect fence")
			}
			if id, err := c.RequestAnnouncement(ctx, "announce", oldProposal.ID, "GM"); err != nil || id != oldID {
				t.Fatal("same-proposal legacy action was implicitly duplicated", id, err)
			}
			if len(human.snapshot()) != 0 || len(service.snapshot()) != 0 || modelCalls.Load() != 0 {
				t.Fatal("legacy history or deduplication contacted Sessions")
			}
			run, err := c.ManualContext(ctx, "writer", "", "Prepare a fresh fictional draft.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			fresh := completed(t, c, run)
			if _, err := c.ReviewContext(ctx, fresh.Proposal, "accept", "GM"); err != nil {
				t.Fatal(err)
			}
			id, err := c.RequestAnnouncement(ctx, "announce", fresh.Proposal, "GM")
			if err != nil || id == oldID {
				t.Fatal("fresh accepted proposal could not get a service-owned action", id, err)
			}
			op, err := c.announcementOperation(id)
			if err != nil || op.ExecutionOwner != testServiceIdentity.Ref() || op.RequestedBy != actor("human", "GM") {
				t.Fatal("fresh action attribution differs", op, err)
			}
		})
	}
}

func TestAnnouncementRefusesWrongCreateOwnerBeforeExec(t *testing.T) {
	c, _, modelCalls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	proposal := acceptedAnnouncement(t, c, "# Fixture announcement\n")
	var execs atomic.Int32
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			err := next(ctx, method, req, reply, cc, opts...)
			if err == nil && method == v1.Sessions_CreateSession_FullMethodName {
				reply.(*v1.Session).Identity = &v1.IdentityRef{Principal: "another-owner"}
			}
			return err
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, next grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			if method == v1.Sessions_Exec_FullMethodName {
				execs.Add(1)
			}
			return next(ctx, desc, cc, method, opts...)
		})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	c.client = cl
	if _, err := c.RequestAnnouncement(context.Background(), "announce", proposal.ID, "GM"); err == nil || execs.Load() != 0 || modelCalls.Load() != 0 {
		t.Fatalf("wrong-owner CreateSession reply reached execution: error=%v execs=%d models=%d", err, execs.Load(), modelCalls.Load())
	}
}

func TestLocalExecutionUsesStableEngineOwnerNotActor(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "coach")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	engine := api.IdentityRef{Principal: "demo:canvas-engine"}
	if r.Status != "proposed" || r.ExecutionOwner != engine || r.Actor != "coach" || r.RequestedBy != (sharedspace.ReviewActor{}) || r.Reviewer.Valid() {
		t.Fatalf("local run owner/attribution: %+v", r)
	}
	if got := recordedSessionIdentity(t, c, r.Session); got != engine {
		t.Fatalf("session owner %+v, want stable engine principal", got)
	}
	p := proposalByID(t, c, r.Proposal)
	if p.Attribution.ExecutionOwner != engine || p.Attribution.Actor != "coach" || p.Attribution.RequestedBy != (sharedspace.ReviewActor{}) {
		t.Fatalf("proposal attribution: %+v", p.Attribution)
	}
}

func TestServiceOwnsExecutionAndJournalsVerifiedRequesterBeforeDispatch(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	c.EnableAuthentication()
	human, service := configureTestService(t, c, testServiceIdentity)
	var journaled atomic.Bool
	service.onCreate = func() {
		raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
		var saved ledger
		if err == nil && json.Unmarshal(raw, &saved) == nil && saved.ExecutionOwner == testServiceIdentity.Ref() && len(saved.Attempts) == 1 && saved.Attempts[0].ExecutionOwner == testServiceIdentity.Ref() && saved.Attempts[0].RequestedBy == actor("requester", "GM") {
			journaled.Store(true)
		}
	}
	ctx, cancel := context.WithCancel(humanContext(t, "requester"))
	id, err := c.ManualContext(ctx, "writer", "", "Draft fictional requirements.", "GM")
	cancel() // Admission is over; the queued worker must not inherit the HTTP lifetime.
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	if r.Status != "proposed" || r.ExecutionOwner != testServiceIdentity.Ref() || r.RequestedBy != actor("requester", "GM") || r.Reviewer.Subject != "requester" || r.TriggeredBy != (sharedspace.ReviewActor{}) || calls.Load() != 1 {
		t.Fatalf("service run: %+v calls=%d", r, calls.Load())
	}
	if !journaled.Load() {
		t.Fatal("owner and requester were not durable before CreateSession")
	}
	if got := recordedSessionIdentity(t, c, r.Session); got != testServiceIdentity.Ref() {
		t.Fatalf("session identity %+v", got)
	}
	assertServiceCalls(t, service, "CreateSession", "GetSession", "Exec")
	if len(human.snapshot()) != 0 {
		t.Fatalf("human client executed: %+v", human.snapshot())
	}
	p := proposalByID(t, c, r.Proposal)
	if p.Attribution.ExecutionOwner != testServiceIdentity.Ref() || p.Attribution.RequestedBy != actor("requester", "GM") || p.Attribution.Actor != "GM" {
		t.Fatalf("proposal provenance: %+v", p.Attribution)
	}
}

func TestAuthenticatedExecutionWithoutServiceRefusesBeforeDispatch(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	c.EnableAuthentication()
	for _, incomplete := range []ExecutionService{
		{Credentials: serviceCredentials(testServiceIdentity), Identity: testServiceIdentity},
		{Client: c.client, Identity: testServiceIdentity},
		{Client: c.client, Credentials: serviceCredentials(testServiceIdentity)},
	} {
		if c.ConfigureExecutionService(incomplete) == nil {
			t.Fatalf("incomplete service accepted: %+v", incomplete)
		}
	}
	if _, err := c.ManualContext(humanContext(t, "requester"), "writer", "", "Draft fictional requirements.", "GM"); err == nil {
		t.Fatal("authenticated run fell back to the human owner")
	}
	state, _ := c.State()
	if len(state.Runs) != 0 || calls.Load() != 0 {
		t.Fatalf("refused run dispatched: %d runs, %d calls", len(state.Runs), calls.Load())
	}
}

func TestServiceOwnerRotationRequiresNewState(t *testing.T) {
	c, d, calls := definedFixture(t, nil)
	c.EnableAuthentication()
	configureTestService(t, c, testServiceIdentity)
	id, err := c.ManualContext(humanContext(t, "requester"), "writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	other := sessionauth.Identity{Issuer: testIssuer, Subject: "rotated-service"}
	if c.ConfigureExecutionService(ExecutionService{Client: c.client, Credentials: serviceCredentials(other), Identity: other}) == nil {
		t.Fatal("live service reconfiguration accepted")
	}
	c.Close()
	before := calls.Load()
	reopen := func() *Canvas {
		next, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{})
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	rotated := reopen()
	rotated.EnableAuthentication()
	if rotated.ConfigureExecutionService(ExecutionService{Client: c.client, Credentials: serviceCredentials(other), Identity: other}) == nil {
		t.Fatal("journaled service owner silently rotated")
	}
	if r, ok := rotated.GetRun(id); !ok || r.ExecutionOwner != testServiceIdentity.Ref() {
		t.Fatalf("history unreadable or relabelled: %+v", r)
	}
	rotated.Close()
	local := reopen()
	if _, err := local.Manual("writer", "", "Draft fictional requirements.", "GM"); err == nil {
		t.Fatal("service-bound journal dispatched under the local engine")
	}
	local.Close()
	same := reopen()
	defer same.Close()
	same.EnableAuthentication()
	if err := same.ConfigureExecutionService(ExecutionService{Client: c.client, Credentials: serviceCredentials(testServiceIdentity), Identity: testServiceIdentity}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("refused configuration invoked a model")
	}
}

func TestServiceRefusesLegacyAndMismatchedSessionsButAllowsFreshAttempt(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.EnableAuthentication()
	_, service := configureTestService(t, c, testServiceIdentity)
	before := calls.Load()
	ctx := humanContext(t, "asker")
	for _, owner := range []api.IdentityRef{{Principal: "demo:canvas-engine"}, {}, testServiceIdentity.Ref()} {
		c.mu.Lock()
		c.findLocked(id).ExecutionOwner = owner
		c.mu.Unlock()
		if _, err := c.AskContext(ctx, id, "Why this draft?", "GM"); err == nil {
			t.Fatalf("question on recorded owner %+v accepted", owner)
		}
		if c.InspectResume(ctx, id, "").Available {
			t.Fatalf("resume on recorded owner %+v available", owner)
		}
	}
	if calls.Load() != before {
		t.Fatal("legacy or mismatched session invoked a model")
	}
	if _, ok := c.GetRun(id); !ok {
		t.Fatal("legacy history unreadable")
	}
	fresh, err := c.ManualContext(ctx, "writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	if r := completed(t, c, fresh); r.Status != "proposed" || r.ExecutionOwner != testServiceIdentity.Ref() {
		t.Fatalf("fresh attempt: %+v", r)
	}
	assertServiceCalls(t, service)
}

func TestServiceContextDropsHumanAuthorityAndFollowsParent(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	humanCredentials := serviceCredentials(sessionauth.Identity{Issuer: testIssuer, Subject: "human"})
	parent, _, err := humanCredentials.Bind(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parent = metadata.AppendToOutgoingContext(parent, "x-human-marker", "human-only")
	deadline := time.Now().Add(time.Minute)
	parent, cancelParent := context.WithDeadline(parent, deadline)
	defer cancelParent()

	local, cl, cancel, err := c.serviceContext(parent)
	if err != nil || cl != c.client {
		t.Fatalf("local service context: %v", err)
	}
	if _, ok := sessionauth.Caller(local); ok {
		t.Fatal("local service context inherited the human caller")
	}
	cancel()

	c.EnableAuthentication()
	if _, _, _, err := c.serviceContext(parent); err == nil {
		t.Fatal("authenticated mode fell back without a service")
	}
	credentials := serviceCredentials(testServiceIdentity)
	serviceClient := observedClient(t, c, &rpcLog{})
	if err := c.ConfigureExecutionService(ExecutionService{Client: serviceClient, Credentials: credentials, Identity: testServiceIdentity}); err != nil {
		t.Fatal(err)
	}
	ctx, cl, cancel, err := c.serviceContext(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if caller, _ := sessionauth.Caller(ctx); caller != testServiceIdentity || cl != serviceClient {
		t.Fatalf("service caller %+v", caller)
	}
	if md, _ := metadata.FromOutgoingContext(ctx); len(md) != 0 {
		t.Fatalf("human outgoing metadata leaked: %v", md)
	}
	if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatalf("deadline %v %v", got, ok)
	}
	if _, err := humanCredentials.GetRequestMetadata(ctx); err == nil {
		t.Fatal("human credentials accepted a service-bound context")
	}
	if md, err := credentials.GetRequestMetadata(ctx); err != nil || md["authorization"] != "Bearer service-credential-not-persisted" {
		t.Fatalf("service token: %v", err)
	}
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation not forwarded")
	}
}

func TestServiceIdentityRefreshMismatchFailsWithoutModel(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	c.EnableAuthentication()
	human, _ := configureTestService(t, c, sessionauth.Identity{Issuer: testIssuer, Subject: "different-service"})
	if _, _, _, err := c.serviceContext(context.Background()); !sessionauth.IsIdentityChanged(err) {
		t.Fatalf("refresh mismatch: %v", err)
	}
	id, err := c.ManualContext(humanContext(t, "requester"), "writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	if r := completed(t, c, id); r.Status != "failed" || r.Session != "" || calls.Load() != 0 {
		t.Fatalf("mismatched service executed: %+v", r)
	}
	if len(human.snapshot()) != 0 {
		t.Fatal("human credential fallback")
	}
}

func TestQueuedAskAndRegenerateCaptureRequesterAndOutliveHTTPContext(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	c.EnableAuthentication()
	human, service := configureTestService(t, c, testServiceIdentity)
	id, err := c.ManualContext(humanContext(t, "requester"), "writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	ctx, cancel := context.WithCancel(humanContext(t, "asker"))
	qid, err := c.AskContext(ctx, id, "Explain the recorded draft.", "coach")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	q := awaitQuestion(t, c, qid)
	if q.Status != "answered" || q.RequestedBy != actor("asker", "coach") || q.ExecutionOwner != testServiceIdentity.Ref() {
		t.Fatalf("question: %+v", q)
	}
	ctx, cancel = context.WithCancel(humanContext(t, "regenerator"))
	nid, err := c.RegenerateContext(ctx, r.Proposal, "GM")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	next := completed(t, c, nid)
	if next.Status != "proposed" || next.PreviousAttempt != id || next.RequestedBy != actor("regenerator", "GM") || next.ExecutionOwner != testServiceIdentity.Ref() {
		t.Fatalf("regenerated: %+v", next)
	}
	assertServiceCalls(t, service, "GetSession", "Exec")
	if len(human.snapshot()) != 0 {
		t.Fatalf("human client used for execution: %+v", human.snapshot())
	}
}

func TestQueueAfterReviewBindsOnlyActualAcceptedReview(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	policy, err := NewReviewerPolicy("GM", []sessionauth.Identity{{Issuer: testIssuer, Subject: "acceptor"}})
	if err != nil {
		t.Fatal(err)
	}
	c.EnableAuthentication(policy)
	configureTestService(t, c, testServiceIdentity)
	id, err := c.ManualContext(humanContext(t, "requester"), "writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	first := completed(t, c, id)
	forged := actor("model-claimed", "GM")
	next := func() attempt {
		a, err := c.snapshotGeneric("reviewer", "Review the accepted PRD.", "GM")
		if err != nil {
			t.Fatal(err)
		}
		a.RequestedBy, a.TriggeredBy, a.TriggeringReview = actor("requester", "GM"), forged, "forged"
		return a
	}
	for _, proposal := range []string{first.Proposal, "missing"} {
		if _, err := c.queueAfterReview(next(), proposal); err == nil {
			t.Fatalf("unaccepted proposal %q queued", proposal)
		}
	}
	accepted, err := c.ReviewContext(humanContext(t, "acceptor"), first.Proposal, "accept", "GM")
	if err != nil {
		t.Fatal(err)
	}
	nid, err := c.queueAfterReview(next(), first.Proposal)
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, nid)
	if r.TriggeredBy != accepted.Reviewer || r.TriggeredBy != actor("acceptor", "GM") || r.TriggeringReview != first.Proposal || r.RequestedBy != actor("requester", "GM") || r.ExecutionOwner != testServiceIdentity.Ref() {
		t.Fatalf("follow-up provenance: %+v", r)
	}
	saved := savedAttempt(t, c, nid)
	if saved.TriggeredBy != r.TriggeredBy || saved.TriggeringReview != first.Proposal || saved.RequestedBy != r.RequestedBy {
		t.Fatalf("journaled provenance: %+v", saved.Run)
	}
	if r.Proposal != "" {
		p := proposalByID(t, c, r.Proposal)
		if p.Attribution.TriggeredBy != r.TriggeredBy || p.Attribution.TriggeringReview != first.Proposal || p.Attribution.RequestedBy != r.RequestedBy || p.Reviewer != (sharedspace.ReviewActor{}) {
			t.Fatalf("published provenance: %+v", p)
		}
	}
	if original, _ := c.GetRun(id); original.TriggeredBy != (sharedspace.ReviewActor{}) || original.Reviewer.Subject != "requester" {
		t.Fatalf("history reinterpreted: %+v", original)
	}
}

func TestFileTriggeredRunsAreServiceOwnedWithoutInventedHuman(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	c.EnableAuthentication()
	_, service := configureTestService(t, c, testServiceIdentity)
	if err := c.WriteInput("feedback/new.json", `{"team":"Northwind","feedback":"Fictional new observation; outcomes remain unmeasured."}`, "GM"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ := c.State()
	if len(state.Runs) != 1 {
		t.Fatalf("runs: %d", len(state.Runs))
	}
	r := completed(t, c, state.Runs[0].ID)
	if r.Status != "proposed" || r.ExecutionOwner != testServiceIdentity.Ref() || r.RequestedBy != (sharedspace.ReviewActor{}) || r.TriggeredBy != (sharedspace.ReviewActor{}) || r.Reviewer.Valid() || r.Actor != fileActor {
		t.Fatalf("file run: %+v", r)
	}
	assertServiceCalls(t, service, "CreateSession", "Exec")
}

func TestServicePrincipalCannotActAsHumanDecisionMaker(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Service cannot approve\n")
	head, _ := c.space.Head()
	pending, err := c.space.Propose(head, map[string]string{"announcement.md": "# Pending\n"}, sharedspace.Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	// Even an explicit external review grant does not let the engine act as a human.
	policy, err := NewReviewerPolicy("GM", []sessionauth.Identity{testServiceIdentity})
	if err != nil {
		t.Fatal(err)
	}
	c.EnableAuthentication(policy)
	human, _ := configureTestService(t, c, testServiceIdentity)
	serviceCtx, _, err := serviceCredentials(testServiceIdentity).Bind(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.RequestAnnouncement(humanContext(t, "requester"), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.InspectAnnouncement(humanContext(t, "requester"), id)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	denied := map[string]error{}
	_, denied["review"] = c.ReviewContext(serviceCtx, pending.ID, "accept", "GM")
	_, denied["manual"] = c.ManualContext(serviceCtx, "writer", "", "Draft.", "GM")
	_, denied["ask"] = c.AskContext(serviceCtx, "missing", "Why?", "GM")
	_, denied["regenerate"] = c.RegenerateContext(serviceCtx, pending.ID, "GM")
	_, denied["request"] = c.RequestAnnouncement(serviceCtx, "announce", p.ID, "GM")
	denied["approve"] = c.DecideAnnouncement(serviceCtx, id, "GM", true, v.Confirmation)
	denied["continue"] = c.ContinueAnnouncement(serviceCtx, id, "GM", v.Confirmation)
	denied["resume"] = c.ConfirmResume(serviceCtx, "missing", "", "GM", ResumeConfirmation{Confirmed: true, ActionID: ID() + ID()})
	for name, err := range denied {
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s by service principal: %v", name, err)
		}
	}
	if len(human.snapshot()) != 0 {
		t.Fatalf("decision RPCs sent: %+v", human.snapshot())
	}
}

func TestAnnouncementExecutesAsServiceAndDecidesAsHuman(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Service-owned announcement\n")
	c.EnableAuthentication()
	human, service := configureTestService(t, c, testServiceIdentity)
	ctx := humanContext(t, "approver")
	id, err := c.RequestAnnouncement(ctx, "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.announcementOperation(id)
	if err != nil || op.ExecutionOwner != testServiceIdentity.Ref() || op.RequestedBy != actor("approver", "GM") {
		t.Fatalf("operation provenance: %+v %v", op, err)
	}
	if got := recordedSessionIdentity(t, c, op.Session); got != testServiceIdentity.Ref() {
		t.Fatalf("announcement session owner %+v", got)
	}
	v, err := c.InspectAnnouncement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err := c.DecideAnnouncement(ctx, id, "GM", true, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v, err = c.InspectAnnouncement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err := c.ContinueAnnouncement(ctx, id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	if v, err = c.InspectAnnouncement(ctx, id); err != nil || v.Status != "simulated" {
		t.Fatalf("outcome %s %v", v.Status, err)
	}
	assertServiceCalls(t, service, "CreateSession", "Exec", "GetSession", "Replay")
	var methods []string
	for _, call := range human.snapshot() {
		methods = append(methods, call.Method)
		if call.Caller.Subject != "approver" {
			t.Fatalf("decision caller: %+v", call)
		}
	}
	if !slices.Equal(methods, []string{"Approve", "Resume"}) {
		t.Fatalf("human RPCs: %v", methods)
	}
}

func TestAuthenticatedGenericResumeUsesOriginalHumanRequestSynchronously(t *testing.T) {
	c, _, fail, close := resumeCanvas(t, false)
	defer close()
	c.EnableAuthentication()
	human, service := configureTestService(t, c, testServiceIdentity)
	ctx := humanContext(t, "operator")
	id, err := c.ManualContext(ctx, "writer", "", "Assess synthetic evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	if r := completed(t, c, id); r.Status != "failed" {
		t.Fatalf("fixture: %+v", r)
	}
	c.wg.Wait()
	fail.Store(false)
	i := c.InspectResume(ctx, id, "")
	if !i.Available {
		t.Fatalf("eligible: %+v", i)
	}
	if err := c.ConfirmResume(ctx, id, "", "GM", confirmationFor(i)); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	outcome := c.data.Recoveries[0].Status
	c.mu.Unlock()
	if outcome != "succeeded" {
		t.Fatalf("Resume was not completed within the original request: %s", outcome)
	}
	decisions := human.snapshot()
	if len(decisions) != 1 || decisions[0].Method != "Resume" || decisions[0].Caller.Subject != "operator" {
		t.Fatalf("human RPCs: %+v", decisions)
	}
	assertServiceCalls(t, service, "GetSession", "Replay")
	if r, _ := c.GetRun(id); r.Status != "proposed" {
		t.Fatalf("resumed run: %+v", r)
	}
}
