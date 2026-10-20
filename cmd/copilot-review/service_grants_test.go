package main

// FAKE CONTRACT. The pinned sessions SDK and authn fork expose verified RPC
// callers but no production session owner or per-session grant API. These
// fixtures model the intended host contract locally: an opaque engine service
// credential (not an Entra app token) and an authority-independent owner/grant
// table administered by the test. Nothing here qualifies hosted credentials.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const serviceIssuer = "https://canvas-engine.fixture.invalid"

var fixtureServiceIdentity = sessionauth.Identity{Issuer: serviceIssuer, Subject: "canvas-engine"}

// opaqueServiceVerifier is a fixture-only verifier for one opaque engine
// credential. It deliberately bypasses the user-only Entra verifier.
type opaqueServiceVerifier struct {
	token    string
	identity sessionauth.Identity
}

func (v opaqueServiceVerifier) Verify(_ context.Context, raw string) (sessionauth.Identity, time.Time, error) {
	if v.token == "" || subtle.ConstantTimeCompare([]byte(raw), []byte(v.token)) != 1 {
		return sessionauth.Identity{}, time.Time{}, errors.New("unknown service credential")
	}
	return v.identity, time.Now().Add(time.Hour), nil
}

func newServiceToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "opaque-engine-" + hex.EncodeToString(b)
}

// serviceManager is the trusted Go injection a host adapter would perform.
func serviceManager(token string) *sessionauth.Manager {
	return sessionauth.New(sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) {
		return token, time.Now().Add(time.Hour), nil
	}), opaqueServiceVerifier{token: token, identity: fixtureServiceIdentity})
}

// compositeVerifier accepts the opaque service credential or a real human JWT.
type compositeVerifier struct {
	service opaqueServiceVerifier
	human   hostVerifier
}

func (v compositeVerifier) Verify(ctx context.Context) (authn.VerifiedCaller, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") {
		if id, exp, err := v.service.Verify(ctx, strings.TrimPrefix(values[0], "Bearer ")); err == nil {
			return authn.VerifiedCaller{Identity: api.CallerIdentity{Issuer: id.Issuer, Subject: id.Subject}, ValidUntil: exp}, nil
		}
	}
	return v.human.Verify(ctx)
}

type grantKey struct{ Issuer, Subject, Session, Method string }
type fixtureCall struct {
	Method  string
	Caller  api.CallerIdentity
	Allowed bool
}

// grantFixture owns authority independently of Session.Identity provenance:
// owners come from the verified CreateSession caller and human grants are
// added only by the test administrator.
type grantFixture struct {
	mu      sync.Mutex
	service api.CallerIdentity
	owners  map[string]api.CallerIdentity
	grants  map[grantKey]bool
	calls   []fixtureCall
}

func newGrantFixture() *grantFixture {
	return &grantFixture{service: api.CallerIdentity{Issuer: fixtureServiceIdentity.Issuer, Subject: fixtureServiceIdentity.Subject}, owners: map[string]api.CallerIdentity{}, grants: map[grantKey]bool{}}
}

func (f *grantFixture) set(id sessionauth.Identity, session, method string, granted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[grantKey{id.Issuer, id.Subject, session, method}] = granted
}

func (f *grantFixture) snapshot() []fixtureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fixtureCall(nil), f.calls...)
}

// decide records and evaluates one call. Service-only methods need the
// verified owner of a known session; Approve/Resume need an exact human grant
// and grant the owner nothing.
func (f *grantFixture) decide(ctx context.Context, method, session string) bool {
	caller, ok := authn.Caller(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := false
	if ok {
		switch method {
		case "CreateSession":
			allowed = caller == f.service
		case "GetSession", "Exec", "Replay":
			owner, known := f.owners[session]
			allowed = known && caller == f.service && owner == caller
		case "Approve", "Resume":
			allowed = caller.Issuer == testIssuer && caller != f.service && f.grants[grantKey{caller.Issuer, caller.Subject, session, method}]
		}
	}
	f.calls = append(f.calls, fixtureCall{Method: method, Caller: caller, Allowed: allowed})
	return allowed
}

func shortMethod(full string) string { return full[strings.LastIndex(full, "/")+1:] }

func (f *grantFixture) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	session := ""
	switch r := req.(type) {
	case *v1.GetSessionRequest:
		session = r.GetUid()
	case *v1.ApproveRequest:
		session = r.GetSession()
	case *v1.ResumeRequest:
		session = r.GetSession()
	}
	method := shortMethod(info.FullMethod)
	switch info.FullMethod {
	case v1.Sessions_CreateSession_FullMethodName, v1.Sessions_GetSession_FullMethodName, v1.Sessions_Approve_FullMethodName, v1.Sessions_Resume_FullMethodName:
	default:
		method = "denied:" + method
	}
	if !f.decide(ctx, method, session) {
		return nil, status.Error(codes.PermissionDenied, "private fixture grant rejection")
	}
	resp, err := next(ctx, req)
	if s, ok := resp.(*v1.Session); err == nil && ok && info.FullMethod == v1.Sessions_CreateSession_FullMethodName {
		caller, _ := authn.Caller(ctx)
		f.mu.Lock()
		f.owners[s.GetMetadata().GetUid()] = caller
		f.mu.Unlock()
	}
	return resp, err
}

func (f *grantFixture) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	if info.FullMethod != v1.Sessions_Exec_FullMethodName && info.FullMethod != v1.Sessions_Replay_FullMethodName {
		f.decide(ss.Context(), "denied:"+shortMethod(info.FullMethod), "")
		return status.Error(codes.PermissionDenied, "private fixture grant rejection")
	}
	return next(srv, &guardedStream{ServerStream: ss, fixture: f})
}

// guardedStream authorizes every received payload's session, so stream
// authority is never inferred from connection metadata alone.
type guardedStream struct {
	grpc.ServerStream
	fixture *grantFixture
}

func (s *guardedStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}
	method, session := "denied:unknown", ""
	switch r := m.(type) {
	case *v1.ExecRequest:
		method, session = "Exec", r.GetSession()
	case *v1.ReplayRequest:
		method, session = "Replay", r.GetSession()
	}
	if !s.fixture.decide(s.Context(), method, session) {
		return status.Error(codes.PermissionDenied, "private fixture grant rejection")
	}
	return nil
}

func TestAuthenticatedEngineRequiresSeparateServiceBinding(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepts atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			conn.Close()
		}
	}()
	var sourceCalls atomic.Int64
	source := sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) {
		sourceCalls.Add(1)
		return "", time.Time{}, errors.New("unused")
	})
	human := sessionauth.New(source, nil)
	policy, err := canvas.NewReviewerPolicy("GM", []sessionauth.Identity{{Issuer: testIssuer, Subject: "approver"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, binding := range map[string]struct {
		manager  *sessionauth.Manager
		identity sessionauth.Identity
	}{
		"missing":                {},
		"identity without creds": {identity: fixtureServiceIdentity},
		"creds without identity": {manager: sessionauth.New(source, nil)},
		"relabelled human creds": {manager: human, identity: fixtureServiceIdentity},
	} {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			remote := &remoteEngine{endpoint: listener.Addr().String(), tls: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "sessions.test"}, auth: human, executionAuth: binding.manager, executionIdentity: binding.identity, reviewPolicy: policy, reviewRole: "GM"}
			d, err := startConfigured(filepath.Join("..", ".."), state, "127.0.0.1:0", "", remote)
			if err == nil {
				d.Close()
				t.Fatal("authenticated engine started without a separately verified service binding")
			}
			if !strings.Contains(err.Error(), "authenticated execution requires a separately verified service binding; hosted service credentials are not yet qualified") {
				t.Fatalf("unclear refusal: %v", err)
			}
			entries, _ := os.ReadDir(state)
			for _, entry := range entries {
				if entry.Name() != "suite-cache" {
					t.Fatalf("state touched before refusal: %s", entry.Name())
				}
			}
		})
	}
	if sourceCalls.Load() != 0 || accepts.Load() != 0 {
		t.Fatalf("credential or sessions contacted before refusal: source=%d connections=%d", sourceCalls.Load(), accepts.Load())
	}
}

func TestAuthenticatedServiceOwnershipAndHumanGrants(t *testing.T) {
	verifier, key := integrationVerifier(t)
	cert, pool := sessionsCertificate(t)
	serviceToken := newServiceToken(t)
	fixture := newGrantFixture()
	unary, stream, err := authn.NewInterceptors(authn.Required, compositeVerifier{service: opaqueServiceVerifier{token: serviceToken, identity: fixtureServiceIdentity}, human: hostVerifier{verifier}})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := canvas.LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostRoot := filepath.Join(t.TempDir(), "host")
	host, err := sessionhost.StartWithOptions(hostRoot, definition.Bundles, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"changes":[{"document":"announcement","operation":"replace","content":"# Reviewed announcement\n"}]}`)}, nil
	}, sessionhost.Options{Listener: listener, ServerOptions: []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})), grpc.ChainUnaryInterceptor(unary, fixture.unary), grpc.ChainStreamInterceptor(stream, fixture.stream)}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	human := func(subject string) sessionauth.Identity {
		return sessionauth.Identity{Issuer: testIssuer, Subject: subject}
	}
	requester, acceptor, decider := human("requester"), human("acceptor"), human("decider")
	policy, err := canvas.NewReviewerPolicy("GM", []sessionauth.Identity{requester, acceptor, decider})
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	tokens := map[string]string{}
	for _, id := range []sessionauth.Identity{requester, acceptor, decider} {
		tokens[id.Subject] = signedToken(t, key, id.Subject, expiry)
	}
	secrets := [][]byte{[]byte(serviceToken)}
	for _, token := range tokens {
		secrets = append(secrets, []byte(token))
	}
	var current atomic.Pointer[string]
	act := func(id sessionauth.Identity) { token := tokens[id.Subject]; current.Store(&token) }
	act(requester)
	// A 25s source expiry is inside the 30s refresh skew, so every HTTP action
	// binds a freshly sourced human credential rather than a cached one.
	humanAuth := sessionauth.New(sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) {
		return *current.Load(), time.Now().Add(25 * time.Second), nil
	}), verifier)
	serviceAuth := serviceManager(serviceToken)
	newRemote := func() *remoteEngine {
		return &remoteEngine{endpoint: host.Address(), tls: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "sessions.test"}, auth: humanAuth, executionAuth: serviceAuth, executionIdentity: fixtureServiceIdentity, reviewPolicy: policy, reviewRole: "GM"}
	}
	state := t.TempDir()
	engine, err := startConfigured(filepath.Join("..", ".."), state, "127.0.0.1:0", "", newRemote())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if engine != nil {
			engine.Close()
		}
	}()
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	request := func(method, path string, body any, out any) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, engine.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", engine.URL)
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		for _, secret := range secrets {
			if bytes.Contains(data, secret) {
				t.Fatal("credential leaked into HTTP response")
			}
		}
		if bytes.Contains(data, []byte("private fixture grant rejection")) {
			t.Fatal("private host failure leaked")
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatalf("response %d: %s", res.StatusCode, data)
			}
		}
		return res.StatusCode
	}
	open := func() {
		t.Helper()
		res, err := httpClient.Get(engine.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	open()
	getState := func() canvas.State {
		t.Helper()
		var s canvas.State
		if code := request("GET", "/engine/api/state", nil, &s); code != 200 {
			t.Fatalf("state %d", code)
		}
		return s
	}

	// Model or browser JSON can never supply execution provenance.
	for _, field := range []string{"executionOwner", "requestedBy", "triggeredBy", "owner"} {
		body := map[string]any{"agent": "writer", "role": "GM", "request": "Prepare review", field: map[string]string{"issuer": testIssuer, "subject": "forged"}}
		if code := request("POST", "/engine/api/run", body, nil); code != 400 {
			t.Fatalf("forged %s accepted: %d", field, code)
		}
	}
	var queued struct {
		Run string `json:"run"`
	}
	if code := request("POST", "/engine/api/run", map[string]string{"agent": "writer", "role": "GM", "request": "Prepare review"}, &queued); code != 202 {
		t.Fatalf("manual run %d", code)
	}
	var proposal string
	for deadline := time.Now().Add(10 * time.Second); proposal == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, r := range getState().Runs {
			if r.ID == queued.Run {
				if r.Status == "failed" {
					t.Fatalf("run failed: %+v", r)
				}
				proposal = r.Proposal
			}
		}
	}
	if proposal == "" {
		t.Fatal("no proposal")
	}
	act(acceptor)
	var accepted map[string]any
	if code := request("POST", "/engine/api/review", map[string]string{"proposal": proposal, "action": "accept", "role": "GM"}, &accepted); code != 200 {
		t.Fatalf("acceptor review %d %+v", code, accepted)
	}

	// Separate attribution survives a restart over the same state.
	engine.Close()
	engine, err = startConfigured(filepath.Join("..", ".."), state, "127.0.0.1:0", "", newRemote())
	if err != nil {
		t.Fatal(err)
	}
	jar, _ = cookiejar.New(nil)
	httpClient.Jar = jar
	open()
	restarted := getState()
	wantRequester := sharedspace.ReviewActor{Issuer: requester.Issuer, Subject: requester.Subject, Role: "GM"}
	wantAcceptor := sharedspace.ReviewActor{Issuer: acceptor.Issuer, Subject: acceptor.Subject, Role: "GM"}
	found := false
	for _, r := range restarted.Runs {
		if r.ID == queued.Run {
			found = true
			if r.ExecutionOwner != fixtureServiceIdentity.Ref() || r.RequestedBy != wantRequester || r.TriggeredBy != (sharedspace.ReviewActor{}) {
				t.Fatalf("run provenance: owner=%+v requestedBy=%+v triggeredBy=%+v", r.ExecutionOwner, r.RequestedBy, r.TriggeredBy)
			}
		}
	}
	if !found {
		t.Fatal("run lost on restart")
	}
	found = false
	for _, p := range restarted.Proposals {
		if p.ID == proposal {
			found = true
			if p.Attribution.ExecutionOwner != fixtureServiceIdentity.Ref() || p.Attribution.RequestedBy != wantRequester || p.Reviewer != wantAcceptor {
				t.Fatalf("proposal provenance: %+v reviewer=%+v", p.Attribution, p.Reviewer)
			}
		}
	}
	if !found {
		t.Fatal("proposal lost on restart")
	}

	var op struct {
		ID string `json:"id"`
	}
	if code := request("POST", "/engine/api/announcement/request", map[string]string{"proposal": proposal, "action": "announce", "role": "GM"}, &op); code != 200 {
		t.Fatalf("announcement request %d", code)
	}
	inspect := func() canvas.AnnouncementView {
		t.Helper()
		var v canvas.AnnouncementView
		if code := request("GET", "/engine/api/announcement?id="+op.ID, nil, &v); code != 200 {
			t.Fatalf("inspect %d %+v", code, v)
		}
		v.Confirmation.Confirmed = true
		return v
	}
	view := inspect()
	if view.Status != "pending-approval" || view.Session == "" {
		t.Fatalf("not parked: %+v", view)
	}
	session := view.Session
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The owning service gets no approval or resume privilege, even with its
	// own identity and even when an administrator wrongly grants it.
	r := newRemote()
	serviceClient, err := r.dialExecution(definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer serviceClient.Close()
	humanClient, err := r.dial(definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer humanClient.Close()
	fixture.set(fixtureServiceIdentity, session, "Approve", true)
	fixture.set(fixtureServiceIdentity, session, "Resume", true)
	if _, err := serviceClient.Approve(ctx, session, api.ApprovalDecision{ExecutionID: view.Confirmation.ExecutionID, ToolCallID: view.Confirmation.ToolCallID, RequestSeq: view.Confirmation.RequestSeq, Approved: true, Reason: "service self-approval", Identity: fixtureServiceIdentity.Ref()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service owner approved: %v", err)
	}
	head := view.Confirmation.Head
	if _, err := serviceClient.Sessions().Resume(ctx, &v1.ResumeRequest{Session: session, ExpectedLastSeq: &head}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service owner resumed: %v", err)
	}
	// Human callers never read or replay service-owned sessions directly.
	act(decider)
	if _, err := humanClient.Replay(ctx, session, 0, 0); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("human replay: %v", err)
	}
	if _, err := humanClient.GetSession(ctx, session); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("human read: %v", err)
	}
	// A human-bound context cannot select the service credential.
	before := len(fixture.snapshot())
	bound, _, err := humanAuth.Bind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceClient.GetSession(bound, session); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("credential crossover: %v", err)
	}
	if len(fixture.snapshot()) != before {
		t.Fatal("crossover reached sessions")
	}

	decide := func(approved bool) (int, map[string]any) {
		var result map[string]any
		code := request("POST", "/engine/api/announcement/decision", map[string]any{"id": op.ID, "role": "GM", "approved": approved, "confirmation": inspect().Confirmation}, &result)
		return code, result
	}
	resume := func() (int, map[string]any) {
		var result map[string]any
		code := request("POST", "/engine/api/announcement/continue", map[string]any{"id": op.ID, "role": "GM", "confirmation": inspect().Confirmation}, &result)
		return code, result
	}
	if code, result := decide(true); code != 403 || result["code"] != "PERMISSION_DENIED" {
		t.Fatalf("ungranted approval %d %+v", code, result)
	}
	fixture.set(decider, session, "Approve", true)
	if code, result := decide(true); code != 200 {
		t.Fatalf("granted approval %d %+v", code, result)
	}
	if v := inspect(); v.Status != "approved" || v.Receipt != nil || v.Concluded {
		t.Fatalf("approval resumed or concluded: %+v", v)
	}
	for _, c := range fixture.snapshot() {
		if c.Method == "Resume" && c.Allowed {
			t.Fatal("approval triggered automatic resume")
		}
	}
	denials := []func(){
		func() {}, // Approve grant is the wrong method.
		func() { fixture.set(decider, "another-session", "Resume", true) },
		func() { fixture.set(acceptor, session, "Resume", true) },
		func() { fixture.set(decider, session, "Resume", true); fixture.set(decider, session, "Resume", false) },
	}
	for i, setup := range denials {
		setup()
		if code, result := resume(); code != 403 || result["code"] != "PERMISSION_DENIED" {
			t.Fatalf("resume denial %d: %d %+v", i, code, result)
		}
		if v := inspect(); v.Status != "approved" || v.Receipt != nil {
			t.Fatalf("denied resume changed state: %+v", v)
		}
	}
	fixture.set(decider, session, "Resume", true)
	if code, result := resume(); code != 200 {
		t.Fatalf("granted resume %d %+v", code, result)
	}
	if v := inspect(); v.Status != "simulated" || v.Receipt == nil {
		t.Fatalf("no simulated receipt: %+v", v)
	}

	service := api.CallerIdentity{Issuer: fixtureServiceIdentity.Issuer, Subject: fixtureServiceIdentity.Subject}
	deciderCaller := api.CallerIdentity{Issuer: decider.Issuer, Subject: decider.Subject}
	seen := map[string]bool{}
	for _, c := range fixture.snapshot() {
		if strings.HasPrefix(c.Method, "denied:") && c.Caller == service {
			t.Fatalf("engine used unsupported method %s", c.Method)
		}
		if !c.Allowed {
			continue
		}
		seen[c.Method] = true
		switch c.Method {
		case "CreateSession", "GetSession", "Exec", "Replay":
			if c.Caller != service {
				t.Fatalf("%s by %+v", c.Method, c.Caller)
			}
		case "Approve", "Resume":
			if c.Caller != deciderCaller {
				t.Fatalf("%s by %+v", c.Method, c.Caller)
			}
		}
	}
	for _, method := range []string{"CreateSession", "Exec", "Replay", "Approve", "Resume"} {
		if !seen[method] {
			t.Fatalf("no authorized %s observed", method)
		}
	}
	for _, root := range []string{state, hostRoot} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range secrets {
				if bytes.Contains(data, secret) {
					t.Fatalf("credential persisted in %s", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
