package canvas

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
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
