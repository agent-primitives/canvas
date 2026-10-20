package canvas

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	v1 "github.com/agent-primitives/sessions/api/genpb"
)

func interruptedAnnouncement(t *testing.T, c *Canvas, p sharedspace.Proposal, knownSession bool) string {
	t.Helper()
	s := announcement.Snapshot{Operation: ID(), Definition: c.definition.Digest, Action: "announce", Document: "announcement", Path: "announcement.md", Proposal: p.ID, Revision: p.AcceptedCommit, Content: p.Changes["announcement.md"]}
	s.Digest = fmt.Sprintf("%x", sha256.Sum256([]byte(s.Content)))
	op := announcementOperation{Snapshot: s, Stage: "pending", ExecutionOwner: localExecutionOwner}
	if knownSession {
		session, err := c.client.CreateSession(context.Background(), &v1.Session{Metadata: &v1.ResourceMetadata{Name: "Interrupted local simulation", Project: c.project()}, Harness: announcement.HarnessName, Annotations: map[string]string{"canvas-operation": s.Operation, "canvas-definition": s.Definition}, Identity: &v1.IdentityRef{Principal: localExecutionOwner.Principal}})
		if err != nil {
			t.Fatal(err)
		}
		op.Session = session.GetMetadata().GetUid()
		op.InitialHead = session.GetLastSeq()
	}
	c.mu.Lock()
	c.data.Announcements = append(c.data.Announcements, op)
	err := c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return s.Operation
}
func TestAnnouncementRestartDoesNotDispatchAndRecoversSavedUIDBeforeExec(t *testing.T) {
	c, d, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Pre-exec saved UID cut\n")
	id := interruptedAnnouncement(t, c, p, true)
	op, _ := c.announcementOperation(id)
	before, err := c.client.GetSession(context.Background(), op.Session)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := c.client.GetSession(context.Background(), op.Session)
	if err != nil {
		t.Fatal(err)
	}
	if before.GetLastSeq() != after.GetLastSeq() {
		t.Fatal("startup dispatched native work")
	}
	saved, _ := reopened.announcementOperation(id)
	if saved.Stage != "uncertain" {
		t.Fatal("interrupted intent not uncertain")
	}
	v := confirmedAnnouncement(t, reopened, id)
	if v.Status != "recoverable" || v.Confirmation.ExecutionID != "" {
		t.Fatalf("preExec cut: %+v", v)
	}
	if err := reopened.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("undecided gate continued")
	}
	if err := reopened.RecoverAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	pending := confirmedAnnouncement(t, reopened, id)
	if pending.Status != "pending-approval" || pending.Confirmation.RequestSeq < 1 || pending.Concluded {
		t.Fatalf("not a recovered native request: %+v", pending)
	}
	if err := reopened.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); err == nil {
		t.Fatal("recovery reused pre-request confirmation")
	}
	if err := reopened.RecoverAnnouncement(context.Background(), id, "GM", pending.Confirmation); err == nil {
		t.Fatal("pending approval re-executed")
	}
	if calls.Load() != 0 {
		t.Fatal("recovery invoked model")
	}
}
func TestAnnouncementUnknownUIDRequiresExplicitAbandonBeforeNewRequest(t *testing.T) {
	c, d, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Unknown created UID cut\n")
	id := interruptedAnnouncement(t, c, p, false)
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	duplicate, err := reopened.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil || duplicate != id {
		t.Fatal("uncertain creation silently recreated")
	}
	v := confirmedAnnouncement(t, reopened, id)
	if v.Status != "uncertain" || v.Session != "" {
		t.Fatal("unknown UID fabricated native state")
	}
	if err := reopened.RecoverAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("unknown UID automatically dispatched")
	}
	if err := reopened.AbandonAnnouncement(context.Background(), id, "GM", AnnouncementConfirmation{Confirmed: true}); err == nil {
		t.Fatal("unknown UID abandoned without inspected snapshot evidence")
	}
	acceptedAnnouncement(t, reopened, "# Newer version while creation is uncertain\n")
	if err := reopened.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("stale unknown-UID confirmation abandoned request")
	}
	saved, _ := reopened.announcementOperation(id)
	if saved.Stage != "uncertain" {
		t.Fatal("refused abandonment changed dispatch intent")
	}
	v = confirmedAnnouncement(t, reopened, id)
	if err := reopened.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	next, err := reopened.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil || next == id {
		t.Fatal("explicit new request failed")
	}
	abandoned := confirmedAnnouncement(t, reopened, id)
	if abandoned.Status != "abandoned" {
		t.Fatal("abandon audit lost")
	}
	live := confirmedAnnouncement(t, reopened, next)
	if err := reopened.AbandonAnnouncement(context.Background(), next, "GM", live.Confirmation); err == nil {
		t.Fatal("known pending request abandoned")
	}
	if live.Status != "pending-approval" || calls.Load() != 0 {
		t.Fatal("new request inherited state or called model")
	}
}
func TestAnnouncementNativeDecisionSurvivesLocalUncertaintyAndExactRetry(t *testing.T) {
	c, d, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Decision before local outcome save\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	v := confirmedAnnouncement(t, c, id)
	if err := c.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	op, _ := c.announcementOperation(id)
	op.Stage = "pending"
	if err := c.saveAnnouncement(op); err != nil {
		t.Fatal(err)
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	v = confirmedAnnouncement(t, reopened, id)
	if v.Status != "approved" || v.Concluded {
		t.Fatal("native decision lost or automatically continued")
	}
	if err := reopened.DecideAnnouncement(context.Background(), id, "coach", true, v.Confirmation); err == nil {
		t.Fatal("retry changed native attribution")
	}
	if err := reopened.DecideAnnouncement(context.Background(), id, "GM", false, v.Confirmation); err == nil {
		t.Fatal("retry changed native decision")
	}
	if err := reopened.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); err != nil {
		t.Fatal("exact native retry refused", err)
	}
	v = confirmedAnnouncement(t, reopened, id)
	if err := reopened.RecoverAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("Recover bypassed Continue")
	}
	if err := reopened.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	op, _ = reopened.announcementOperation(id)
	op.Stage = "pending"
	if err := reopened.saveAnnouncement(op); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	last, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	proof := confirmedAnnouncement(t, last, id)
	if proof.Status != "simulated" || proof.Receipt == nil {
		t.Fatal("native receipt lost after local interruption")
	}
	if err := last.ContinueAnnouncement(context.Background(), id, "GM", proof.Confirmation); err == nil {
		t.Fatal("completed receipt re-executed")
	}
	if calls.Load() != 0 {
		t.Fatal("native recovery called model")
	}
}

func TestAnnouncementIncompleteNativeInvocationCanOnlyBeAbandonedLocally(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Partial input crash\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	op, _ := c.announcementOperation(id)
	original, err := c.client.GetSession(context.Background(), op.Session)
	if err != nil {
		t.Fatal(err)
	}
	records, err := sessionclient.ReadRecovery(context.Background(), c.client.Sessions(), op.Session, original.GetLastSeq())
	if err != nil {
		t.Fatal(err)
	}
	partial := proto.Clone(original).(*v1.Session)
	partial.LastSeq = 1
	partial.PendingApproval = nil
	partial.ExecState = v1.ExecState_EXEC_PENDING
	read := &resumeReadServer{session: partial, records: records[:1]}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, read)
	go server.Serve(listener)
	defer server.Stop()
	fake, err := client.Dial(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	real := c.client
	c.client = fake
	defer func() { c.client = real }()
	v := confirmedAnnouncement(t, c, id)
	if v.Status != "incomplete" {
		t.Fatal("partial invocation incorrectly recoverable", v.Status)
	}
	if err := c.RecoverAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil || read.resumes.Load() != 0 {
		t.Fatal("incomplete input sent to Resume")
	}
	stale := v.Confirmation
	stale.Head++
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", stale); err == nil {
		t.Fatal("stale partial-prefix confirmation accepted")
	}
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	// A late original writer may finish parking; abandoning never cancels it.
	read.session = original
	read.records = records
	proof := confirmedAnnouncement(t, c, id)
	if proof.Status != "abandoned" {
		t.Fatal("late native progress revived abandoned operation")
	}
	if err := c.DecideAnnouncement(context.Background(), id, "GM", true, proof.Confirmation); err == nil {
		t.Fatal("abandoned operation approved")
	}
	if err := c.ContinueAnnouncement(context.Background(), id, "GM", proof.Confirmation); err == nil {
		t.Fatal("abandoned operation continued")
	}
	if err := c.RecoverAnnouncement(context.Background(), id, "GM", proof.Confirmation); err == nil {
		t.Fatal("abandoned operation recovered")
	}
	c.client = real
	next, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil || next == id {
		t.Fatal("fresh explicit gate unavailable", err)
	}
	fresh := confirmedAnnouncement(t, c, next)
	if fresh.Session == op.Session || fresh.Status != "pending-approval" || calls.Load() != 0 {
		t.Fatal("new gate inherited native state or invoked model")
	}
}

type emptyAnnouncementUIDServer struct{ v1.UnimplementedSessionsServer }

func (emptyAnnouncementUIDServer) CreateSession(context.Context, *v1.CreateSessionRequest) (*v1.Session, error) {
	return &v1.Session{}, nil
}
func TestAnnouncementEmptyCreatedUIDCanBeExplicitlyAbandoned(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Empty UID response\n")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, emptyAnnouncementUIDServer{})
	go server.Serve(listener)
	defer server.Stop()
	fake, err := client.Dial(listener.Addr().String(), client.WithProject(c.project()))
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	c.client = fake
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != errAnnouncement || id == "" {
		t.Fatal("expected empty created UID refusal", err)
	}
	v := confirmedAnnouncement(t, c, id)
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal("empty UID stranded local request", err)
	}
}
func TestAnnouncementRefusedRecoveryDoesNotLeavePendingIntent(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Cursor refusal\n")
	id := interruptedAnnouncement(t, c, p, true)
	op, _ := c.announcementOperation(id)
	op.Stage = "uncertain"
	op.InitialHead++
	if err := c.saveAnnouncement(op); err != nil {
		t.Fatal(err)
	}
	v := confirmedAnnouncement(t, c, id)
	if err := c.RecoverAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("stale original cursor accepted")
	}
	saved, _ := c.announcementOperation(id)
	if saved.Stage != "uncertain" {
		t.Fatal("refusal changed intent stage to", saved.Stage)
	}
}
