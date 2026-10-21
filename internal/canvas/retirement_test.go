package canvas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Retired mutations must be absent even for a valid local caller. A generic
// refusal from an active football handler is not retirement.
func TestRetiredFootballRoutesAreNotFoundWithoutEffects(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	server := newTestServer(t, c)
	before := readPublishedLedger(t, c)
	head := mustHead(t, c)
	history, _ := c.space.History()
	for _, route := range []struct{ path, body string }{
		{"/api/injury", `{"prospect":"tavon-reyes","injury":"Synthetic injury","recoveryWeeks":30,"role":"GM"}`},
		{"/api/scenarios", `{"run":"unavailable","role":"GM"}`},
	} {
		code, body := server.do("POST", route.path, route.body)
		if code != 404 {
			t.Errorf("retired %s returned %d, want 404: %s", route.path, code, body)
		}
	}
	after := readPublishedLedger(t, c)
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	if err != nil {
		t.Fatal(err)
	}
	currentHistory, _ := c.space.History()
	if !reflect.DeepEqual(before, after) || mustHead(t, c) != head || !reflect.DeepEqual(history, currentHistory) || calls.Load() != 0 || len(sessions) != 0 {
		t.Fatal("retired route changed state or launched work")
	}
	if _, err := os.Lstat(filepath.Join(c.space.Root(), "injuries")); !os.IsNotExist(err) {
		t.Fatalf("retired route created injuries: %v", err)
	}
}

func TestDefinedOrdinaryBoardPathDoesNotMigrateOrCreateInjuries(t *testing.T) {
	filename := genericFixture(t)
	config := `apiVersion: canvas.local/v1alpha2
kind: Canvas
metadata: {name: ordinary-documents, title: Ordinary documents}
spec:
  documents:
    notes: {path: board.md, title: Ordinary notes}
  agents:
    editor:
      name: Notes editor
      suite:
        directory: suites/writer
        artifact: layouts/writer
        digest: sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375
        member: writer
      read: [notes]
      propose: [notes]
  seed:
    board.md: seeds/announcement.md
  triggers:
    - {name: edit-notes, type: manual, agent: editor}
  people:
    - {id: GM, name: Morgan, title: Notes reviewer}
`
	if err := os.WriteFile(filename, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c, _, _ := definedFixtureAt(t, filename, nil)
	if got, err := c.space.Read("", "board.md"); err != nil || got != c.definition.Seed["board.md"] {
		t.Fatalf("ordinary declared document rewritten: %q %v", got, err)
	}
	for _, name := range []string{"injuries", "board.json"} {
		if _, err := os.Lstat(filepath.Join(c.space.Root(), name)); !os.IsNotExist(err) {
			t.Fatalf("constructor created football artifact %s: %v", name, err)
		}
	}
}

func TestDefinedExecutePersistsSessionIdentityBeforeExecRPC(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	observed := make(chan ledger, 1)
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if method == "/agentsessions.v1.Sessions/Exec" {
			raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
			if err != nil {
				return nil, err
			}
			var saved ledger
			if err := json.Unmarshal(raw, &saved); err != nil {
				return nil, err
			}
			observed <- saved
		}
		return streamer(ctx, desc, cc, method, opts...)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	if r.Status != "proposed" {
		t.Fatalf("native execution failed: %+v", r)
	}
	select {
	case saved := <-observed:
		if len(saved.Attempts) != 1 {
			t.Fatal("missing durable native attempt")
		}
		a := saved.Attempts[0]
		if a.ID != id || a.Session != r.Session || a.Status != "running" || a.ExpectedSeq == nil || a.BundleDigest != c.bundles["writer"].Digest || a.Harness != contract.HarnessName(a.BundleDigest, 4) {
			t.Fatalf("native Exec preceded durable identity: %+v", a)
		}
	default:
		t.Fatal("native Exec not observed")
	}
}

func TestDefinedFixedRoleDefaultsAndInvalidSavedFallback(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	state, err := c.State()
	if err != nil || state.Role != "GM" {
		t.Fatalf("default role: %s %v", state.Role, err)
	}
	for _, role := range []string{"GM", "coach", "owner"} {
		if err := c.SetRole(role); err != nil {
			t.Fatal(err)
		}
		state, _ := c.State()
		if state.Role != role {
			t.Fatalf("role not persisted: %s", role)
		}
	}
	if err := c.SetRole("intern"); err == nil {
		t.Fatal("unsupported role accepted")
	}
	c.Close()
	c.data.Role = "intern"
	if err := c.saveLocked(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, _ = reopened.State()
	if state.Role != "GM" || readPublishedLedger(t, reopened).Role != "GM" {
		t.Fatal("invalid persisted role did not fall back durably to GM")
	}
}

func TestRetiredCheckWritable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := CheckWritable(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("checker created missing root", err)
	}
	for _, kind := range []string{"valid", "corrupt", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "retired.json")
			var err error
			switch kind {
			case "valid":
				err = os.WriteFile(path, []byte(`{"nextID":"successor","definition":"digest","head":"revision"}`), 0600)
			case "corrupt":
				err = os.WriteFile(path, []byte("private malformed material"), 0600)
			case "symlink":
				err = os.Symlink(filepath.Join(root, "absent"), path)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckWritable(root); !errors.Is(err, ErrRetired) || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private") {
				t.Fatalf("seal did not refuse safely: %v", err)
			}
		})
	}
	root := t.TempDir()
	if err := os.WriteFile(root+"/retired.json", []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openCanvas(root, nil, nil, "", nil, nil, nil, nil); !errors.Is(err, ErrRetired) {
		t.Fatal("constructor proceeded past seal", err)
	}
}

func TestSealQuiescenceAndReopen(t *testing.T) {
	c, d, calls := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Draft a fictional pilot.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	if _, err := c.Review(run.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	beforeLedger := retirementLedger(t, c)
	next := ID()
	var saved Archive
	if err := c.SealForHistory(next, func(a Archive) error {
		if err := CheckWritable(c.root); err != nil {
			t.Fatal("sealed before archive persistence", err)
		}
		saved = a
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != state.Head || len(saved.Documents) != len(state.Documents) {
		t.Fatal("archive lost accepted revision/documents")
	}
	for path, doc := range saved.Documents {
		content, err := c.space.Read(state.Head, path)
		if err != nil || doc.Content != content || doc.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) {
			t.Fatalf("accepted bytes/digest mismatch: %s %v", path, err)
		}
	}
	if !strings.Contains(saved.Documents["prd.json"].Content, "Proposed fictional pilot PRD") {
		t.Fatal("archive substituted seeds for accepted bytes")
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(saved.State, &public); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"spacePath", "conversation", "role", "prospects", "input", "request", "preferences"} {
		if _, exists := public[key]; exists {
			t.Fatal("private state field archived", key)
		}
	}
	var archived apiState
	if err := json.Unmarshal(saved.State, &archived); err != nil || len(archived.Proposals) != 1 || len(archived.History) == 0 || len(archived.Runs) != 1 || archived.Runs[0].Proof == nil || archived.Runs[0].Proof.ModelCalls != 0 {
		t.Fatal("recorded audit/proof metadata lost", err)
	}
	var seal struct{ NextID, Definition, Head string }
	raw, err := os.ReadFile(filepath.Join(c.root, "retired.json"))
	if err != nil || json.Unmarshal(raw, &seal) != nil || seal.NextID != next || seal.Definition != d.Digest || seal.Head != state.Head {
		t.Fatal("seal binding missing", string(raw), err)
	}
	beforeCalls := calls.Load()
	if _, err := c.Manual("writer", "", "Do not dispatch.", "GM"); err == nil {
		t.Fatal("retired runtime dispatched")
	}
	if _, err := c.Review(run.Proposal, "reject", "GM"); err == nil {
		t.Fatal("retired runtime reviewed")
	}
	if _, err := c.RequestAnnouncement(context.Background(), "announce", run.Proposal, "GM"); err == nil {
		t.Fatal("retired runtime reserved native action")
	}
	if _, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{}); !errors.Is(err, ErrRetired) {
		t.Fatal("writable reopen permitted", err)
	}
	if _, err := c.State(); err != nil {
		t.Fatal("retired state cannot be read before resource close", err)
	}
	if head, _ := c.space.Head(); head != state.Head || calls.Load() != beforeCalls || !bytes.Equal(beforeLedger, retirementLedger(t, c)) {
		t.Fatal("retired mutation changed Git/native calls/journal")
	}
}

func retirementLedger(t *testing.T, c *Canvas) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSealFailureLeavesRuntimeUsable(t *testing.T) {
	for _, kind := range []string{"capture", "persist", "invalid-id"} {
		t.Run(kind, func(t *testing.T) {
			c, _, calls := definedFixture(t, nil)
			before := retirementLedger(t, c)
			original := c.space
			next, invoked := ID(), false
			if kind == "capture" {
				c.space = retirementReadFailure{SharedSpace: original}
			}
			if kind == "invalid-id" {
				next = "../escape"
			}
			err := c.SealForHistory(next, func(Archive) error { invoked = true; return errors.New("archive unavailable") })
			c.space = original
			if err == nil || invoked != (kind == "persist") || CheckWritable(c.root) != nil || c.ctx.Err() != nil || calls.Load() != 0 || !bytes.Equal(before, retirementLedger(t, c)) {
				t.Fatal("failure consumed predecessor", err, invoked)
			}
			id, err := c.Manual("writer", "", "Still usable.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			completed(t, c, id)
			if calls.Load() != 1 {
				t.Fatal("failure prevented subsequent native dispatch")
			}
		})
	}
}

type retirementReadFailure struct{ sharedspace.SharedSpace }

func (retirementReadFailure) Read(string, string) (string, error) {
	return "", errors.New("capture unavailable")
}

func TestSealBusyWorkJournals(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	for _, status := range []string{"queued", "running", "uncertain"} {
		t.Run("attempt-"+status, func(t *testing.T) {
			c.data.Attempts = []attempt{{Run: Run{Status: status}}}
			assertSealBusy(t, c)
			c.data.Attempts = nil
		})
		t.Run("question-"+status, func(t *testing.T) {
			c.conversation.Entries = []conversationRecord{{ConversationMessage: ConversationMessage{Kind: "question", Status: status}}}
			assertSealBusy(t, c)
			c.conversation.Entries = nil
		})
	}
	for _, status := range []string{"pending", "uncertain"} {
		c.data.Recoveries = []resumeAction{{Status: status}}
		assertSealBusy(t, c)
	}
	c.data.Recoveries = nil
	for _, op := range []announcementOperation{{Stage: "pending", Phase: "create"}, {Stage: "uncertain", Phase: "approve"}, {Stage: "idle", Phase: "continue", EffectReserved: true}, {Stage: "idle", Phase: "park"}} {
		c.data.Announcements = []announcementOperation{op}
		assertSealBusy(t, c)
	}
	c.data.Announcements = nil
	if calls.Load() != 0 {
		t.Fatal("busy inspection dispatched model")
	}
}

func assertSealBusy(t *testing.T, c *Canvas) {
	t.Helper()
	before := retirementLedger(t, c)
	head, _ := c.space.Head()
	called := false
	if err := c.SealForHistory(ID(), func(Archive) error { called = true; return nil }); err == nil || called || c.ctx.Err() != nil || CheckWritable(c.root) != nil {
		t.Fatal("busy runtime sealed/persisted/stopped", err)
	}
	if now, _ := c.space.Head(); now != head || !bytes.Equal(before, retirementLedger(t, c)) {
		t.Fatal("refusal changed predecessor")
	}
}

func TestSealUndecidedAndStaleProposals(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Proposal requiring a decision.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	assertSealBusy(t, c)
	if _, err := c.space.Ingest("feedback/initial.json", `{"team":"Northwind","feedback":"New evidence"}`, "GM"); err != nil {
		t.Fatal(err)
	}
	assertSealBusy(t, c)
	if _, err := c.Review(run.Proposal, "reject", "GM"); err != nil {
		t.Fatal(err)
	}
	if err := c.SealForHistory(ID(), func(Archive) error { return nil }); err != nil {
		t.Fatal("rejected proposal prevented retirement", err)
	}
}

func TestSealFlowReviewAndTerminalSnapshot(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "retirement", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	// Review publication precedes finish; settle the worker before comparing
	// ledger bytes across the read-only retirement refusal.
	c.wg.Wait()
	assertSealBusy(t, c)
	for i := 0; i < 3; i++ {
		completed(t, c, f.Attempt)
		if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			f = waitWorkflow(t, c, f.ID, "review")
		}
	}
	waitWorkflow(t, c, f.ID, "complete")
	var saved Archive
	if err := c.SealForHistory(ID(), func(a Archive) error { saved = a; return nil }); err != nil {
		t.Fatal(err)
	}
	var panel flowPanelState
	if json.Unmarshal(saved.Flows, &panel) != nil || len(panel.Runs) != 1 || panel.Runs[0].State != "complete" || len(panel.Runs[0].Inputs) != 0 || panel.Head != saved.Revision {
		t.Fatal("terminal flow lost/slim projection violated", string(saved.Flows))
	}
	var fields struct{ Runs []map[string]json.RawMessage }
	if err := json.Unmarshal(saved.Flows, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"commands", "inputBytes", "executionOwner", "inputs", "feedback"} {
		if _, exists := fields.Runs[0][key]; exists {
			t.Fatal("private flow journal archived", key)
		}
	}
	before := retirementLedger(t, c)
	if _, err := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "after", Role: "GM", Inputs: json.RawMessage(`{"topic":"no dispatch"}`), AgentRuns: 12}); err == nil {
		t.Fatal("retired runtime started flow")
	}
	if _, err := c.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "after", Role: "GM"}); err == nil {
		t.Fatal("retired runtime retried flow")
	}
	if calls.Load() != 3 || !bytes.Equal(before, retirementLedger(t, c)) {
		t.Fatal("retired flow mutated")
	}
}

func TestSealNativeAnnouncementMustConclude(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Local simulated announcement\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	assertSealBusy(t, c)
	v := confirmedAnnouncement(t, c, id)
	if err := c.DecideAnnouncement(context.Background(), id, "GM", false, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	assertSealBusy(t, c) // A decision alone is not a native conclusion.
	v = confirmedAnnouncement(t, c, id)
	if err := c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v = confirmedAnnouncement(t, c, id)
	if !v.Concluded || v.Status != "denied" {
		t.Fatal("fixture lacks real native conclusion", v)
	}
	if err := c.SealForHistory(ID(), func(Archive) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("retirement invoked model")
	}
}

func TestSealArchiveBoundAndFinishingWorker(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	original := c.space
	c.space = retirementOversize{SharedSpace: original}
	assertSealBusy(t, c)
	c.space = original
	// A published proposal can still have a finishing worker. Status alone
	// must not let retirement race that worker's final journal update.
	c.data.Attempts = []attempt{{Run: Run{Status: "proposed"}}}
	assertSealBusy(t, c)
	c.data.Attempts = nil
	if calls.Load() != 0 {
		t.Fatal("capture/refusal invoked native model")
	}
}

func TestSealLivePublishedWorker(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	published, release := make(chan sharedspace.Proposal, 1), make(chan struct{})
	c.space = retirementHeldPublication{SharedSpace: c.space, published: published, release: release}
	defer close(release)
	id, err := c.Manual("writer", "", "Hold the live publishing worker.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	var p sharedspace.Proposal
	select {
	case p = <-published:
	case <-time.After(8 * time.Second):
		t.Fatal("worker did not publish")
	}
	// Reproduce the post-publication/pre-finish journal while the actual
	// execute goroutine remains held. Only the runtime counter distinguishes
	// this from the recovered, fully evidenced publication above.
	if err := c.update(id, func(a *attempt) { a.Status, a.Proposal = "proposed", p.ID }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(p.ID, "reject", "GM"); err != nil {
		t.Fatal(err)
	}
	native, err := c.readFlowExecution(context.Background(), c.data.Attempts[0])
	if err != nil || !native.Complete || !native.Settled {
		t.Fatalf("native execution incomplete: %+v %v", native, err)
	}
	assertSealBusy(t, c)
	if calls.Load() != 1 {
		t.Fatal("retirement launched more native work")
	}
}

type retirementHeldPublication struct {
	sharedspace.SharedSpace
	published chan<- sharedspace.Proposal
	release   <-chan struct{}
}

func (s retirementHeldPublication) Propose(base string, changes map[string]string, at sharedspace.Attribution) (sharedspace.Proposal, error) {
	p, err := s.SharedSpace.Propose(base, changes, at)
	if err == nil {
		s.published <- p
		<-s.release
	}
	return p, err
}

type retirementOversize struct{ sharedspace.SharedSpace }

func (retirementOversize) Read(string, string) (string, error) {
	return strings.Repeat("x", 64<<20), nil
}

// A durable proposal is not a completion timestamp. Recovery may retire only
// when exact, read-only native evidence proves the published output.
func TestSealRecoveredPublication(t *testing.T) {
	for _, kind := range []string{"accepted", "rejected", "wrong-input", "wrong-changes", "wrong-base", "wrong-session", "wrong-digest", "incomplete-native"} {
		t.Run(kind, func(t *testing.T) {
			c, _, calls := definedFixture(t, nil)
			id, err := c.Manual("writer", "", "Recover this exact publication.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			run := completed(t, c, id)
			c.wg.Wait()
			native, err := c.readFlowExecution(context.Background(), c.data.Attempts[0])
			if err != nil || !native.Complete || !native.Settled {
				t.Fatalf("fixture lacks proven completed native execution: %+v %v", native, err)
			}
			c.Close()
			a := &c.data.Attempts[0]
			a.Completed, a.Proposal, a.Status = time.Time{}, "", "running"
			if kind == "wrong-input" {
				a.Input += " different input"
			}
			if err := c.saveLocked(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			recovered, _ := reopened.GetRun(id)
			if recovered.Proposal != run.Proposal || !recovered.Completed.IsZero() {
				t.Fatalf("recovery invented completion or lost publication: %+v", recovered)
			}
			action := "reject"
			if kind == "accepted" {
				action = "accept"
			}
			if _, err := reopened.Review(run.Proposal, action, "GM"); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "wrong-") && kind != "wrong-input" {
				reopened.space = retirementProposalMismatch{SharedSpace: reopened.space, kind: kind}
			}
			if kind == "incomplete-native" {
				s, err := reopened.client.GetSession(context.Background(), run.Session)
				if err != nil {
					t.Fatal(err)
				}
				records, err := sessionclient.ReadRecovery(context.Background(), reopened.client.Sessions(), run.Session, s.GetLastSeq())
				if err != nil {
					t.Fatal(err)
				}
				// A valid hash-chained native prefix, not malformed JSON/protobuf.
				for len(records) > 0 && records[len(records)-1].GetEvent().GetKind() != v1.EventKind_EVENT_END {
					records = records[:len(records)-1]
				}
				if len(records) == 0 {
					t.Fatal("native fixture has no END")
				}
				records = records[:len(records)-1]
				s = proto.Clone(s).(*v1.Session)
				s.LastSeq = int64(len(records))
				read := &resumeReadServer{session: s, records: records}
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer()
				v1.RegisterSessionsServer(server, read)
				go server.Serve(listener)
				defer server.Stop()
				cl, err := client.Dial(listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer cl.Close()
				reopened.client = cl
				defer func() {
					if read.resumes.Load() != 0 {
						t.Error("retirement resumed native execution")
					}
				}()
			}
			beforeCalls, before := calls.Load(), retirementLedger(t, reopened)
			if kind == "accepted" || kind == "rejected" {
				if err := reopened.SealForHistory(ID(), func(Archive) error { return nil }); err != nil {
					t.Fatal("reviewed recovered publication remained busy", err)
				}
			} else {
				assertSealBusy(t, reopened)
			}
			if calls.Load() != beforeCalls || !bytes.Equal(before, retirementLedger(t, reopened)) || !reopened.data.Attempts[0].Completed.IsZero() {
				t.Fatal("retirement changed native work, ledger, or invented completion")
			}
		})
	}
}

type retirementProposalMismatch struct {
	sharedspace.SharedSpace
	kind string
}

func (s retirementProposalMismatch) Proposals() ([]sharedspace.Proposal, error) {
	proposals, err := s.SharedSpace.Proposals()
	for i := range proposals {
		switch s.kind {
		case "wrong-changes":
			proposals[i].Changes["prd.json"] += "\n"
		case "wrong-base":
			proposals[i].Base = strings.Repeat("1", 40)
		case "wrong-session":
			proposals[i].Attribution.Session = "another-native-session"
		case "wrong-digest":
			proposals[i].Attribution.BundleDigest = strings.Repeat("1", 64)
		}
	}
	return proposals, err
}

// Denial and simulation both have an idle operation journal; the retained
// public history must preserve their distinct native outcomes after closure.
func TestSealArchivedAnnouncements(t *testing.T) {
	for _, kind := range []string{"denied", "simulated", "simulated-older"} {
		t.Run(kind, func(t *testing.T) {
			c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
			p := acceptedAnnouncement(t, c, "# Exact retained announcement\n")
			id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "simulated-older" {
				acceptedAnnouncement(t, c, "# Newer revision\n")
			}
			v := confirmedAnnouncement(t, c, id)
			if err := c.DecideAnnouncement(context.Background(), id, "GM", kind != "denied", v.Confirmation); err != nil {
				t.Fatal(err)
			}
			v = confirmedAnnouncement(t, c, id)
			if err := c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
				t.Fatal(err)
			}
			v = confirmedAnnouncement(t, c, id)
			if !v.Concluded || v.Snapshot.Proposal != p.ID || v.Snapshot.Revision != p.AcceptedCommit || (v.Receipt != nil) != (kind != "denied") {
				t.Fatalf("fixture lacks exact concluded native outcome: %+v", v)
			}
			var stored []byte
			if err := c.SealForHistory(ID(), func(a Archive) error {
				var err error
				stored, err = json.Marshal(a)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			c.Close()
			if err := c.client.Close(); err != nil {
				t.Fatal(err)
			}
			c.space = retirementReadFailure{SharedSpace: c.space}
			var archive map[string]json.RawMessage
			if err := json.Unmarshal(stored, &archive); err != nil {
				t.Fatal(err)
			}
			var announcements []map[string]json.RawMessage
			if err := json.Unmarshal(archive["announcements"], &announcements); err != nil || len(announcements) != 1 {
				t.Fatalf("retained native announcement missing: %s %v", archive["announcements"], err)
			}
			want := map[string]any{"id": v.ID, "session": v.Session, "status": v.Status, "concluded": true,
				"snapshot": v.Snapshot, "reviewer": v.Reviewer, "identitySource": v.IdentitySource,
				"currentRevision": v.CurrentRevision, "newer": v.Newer}
			if v.Receipt != nil {
				want["receipt"] = v.Receipt
			}
			for key, value := range want {
				expected, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(announcements[0][key], expected) {
					t.Errorf("retained %s = %s, want %s", key, announcements[0][key], expected)
				}
			}
			for key := range announcements[0] {
				if _, ok := want[key]; !ok {
					t.Error("unexpected/actionable archived field", key)
				}
			}
			if calls.Load() != 0 {
				t.Fatal("retirement dispatched a model")
			}
		})
	}
}

func TestSealSerializesDispatch(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	entered, release, sealed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		sealed <- c.SealForHistory(ID(), func(Archive) error { close(entered); <-release; return nil })
	}()
	<-entered
	dispatched := make(chan error, 1)
	go func() { _, err := c.Manual("writer", "", "Race with seal.", "GM"); dispatched <- err }()
	select {
	case err := <-dispatched:
		t.Fatal("dispatch crossed archive persistence boundary", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-sealed; err != nil {
		t.Fatal(err)
	}
	if err := <-dispatched; err == nil || calls.Load() != 0 {
		t.Fatal("seal lost dispatch race", err)
	}
}
