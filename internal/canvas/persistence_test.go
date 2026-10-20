package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
)

func blockStateWrites(t *testing.T, c *Canvas) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(c.root, "attempts.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
}
func failDirectorySync(c *Canvas) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncDirectory = func(string) error { return errors.New("synthetic directory sync failure") }
}

func readPublishedLedger(t *testing.T, c *Canvas) ledger {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved ledger
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal("published ledger invalid")
	}
	return saved
}

func TestDirectorySyncFailureRetainsQueuedIntentWithoutCreatingSession(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	failDirectorySync(c)
	if _, err := c.Manual("writer", "", "Assess evidence.", "GM"); err == nil {
		t.Fatal("directory sync failure did not propagate")
	}
	c.wg.Wait()
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	if err != nil || len(sessions) != 0 {
		t.Fatal("non-durable queue launched session creation")
	}
	saved := readPublishedLedger(t, c)
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Attempts) != 1 || len(state.Runs) != 1 || state.Runs[0].ID != saved.Attempts[0].ID || state.Runs[0].Status != "uncertain" || len(state.Errors) == 0 {
		t.Fatal("published queue intent rolled back or persistence failure hidden")
	}
}

func TestDirectorySyncFailureAtRunningTransitionBlocksSessionCreation(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	a, err := c.snapshotGeneric("writer", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.data.Attempts = append(c.data.Attempts, a)
	err = c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	failDirectorySync(c)
	c.wg.Add(1)
	c.execute(a.ID)
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	r, _ := c.GetRun(a.ID)
	if err != nil || len(sessions) != 0 || r.Status != "uncertain" || r.Session != "" {
		t.Fatal("running transition sync failure launched session or left phantom worker")
	}
}

func syncFailureAfterRPC(t *testing.T, c *Canvas, method string) *client.Client {
	t.Helper()
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, fullMethod string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := invoke(ctx, fullMethod, req, reply, cc, opts...)
		if err == nil && fullMethod == "/agentsessions.v1.Sessions/"+method {
			failDirectorySync(c)
		}
		return err
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func TestDirectorySyncFailureAfterSessionCreationBlocksExec(t *testing.T) {
	for _, method := range []string{"CreateSession", "GetSession"} {
		t.Run(method, func(t *testing.T) {
			c, close := setup(t, false)
			defer close()
			c.client = syncFailureAfterRPC(t, c, method)
			id, err := c.Manual("writer", "", "Assess evidence.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			r := completed(t, c, id)
			c.wg.Wait()
			sessions, err := c.client.ListSessions(context.Background(), c.project())
			if err != nil || len(sessions) != 1 || sessions[0].GetLastSeq() != 0 || r.Status != "uncertain" || r.Proposal != "" {
				t.Fatal("session mapping or provenance sync failure launched Exec")
			}
		})
	}
}

func TestFailedRunningStateWriteDoesNotLeavePhantomWorker(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	a, err := c.snapshotGeneric("writer", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.data.Attempts = append(c.data.Attempts, a)
	err = c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	blockStateWrites(t, c)
	c.wg.Add(1)
	c.execute(a.ID)
	r, _ := c.GetRun(a.ID)
	state, _ := c.State()
	if r.Status != "uncertain" || r.Session != "" || len(state.Errors) == 0 {
		t.Fatal("failed persistence left phantom running attempt or hidden error")
	}
}

type publishHook struct {
	sharedspace.SharedSpace
	after func()
}

func (s publishHook) Propose(base string, changes map[string]string, a sharedspace.Attribution) (sharedspace.Proposal, error) {
	p, err := s.SharedSpace.Propose(base, changes, a)
	if err == nil {
		s.after()
	}
	return p, err
}
func TestPublishedProposalSurvivesBookkeepingFailure(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.space = publishHook{c.space, func() { blockStateWrites(t, c) }}
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	state, _ := c.State()
	if r.Proposal == "" || r.Status != "proposed" || len(state.Proposals) != 1 || len(state.Errors) == 0 || strings.Contains(r.Summary, "No proposal") {
		t.Fatal("durable Git proposal was misrepresented after bookkeeping failure")
	}
	c.wg.Wait()
	r, _ = c.GetRun(id)
	for _, activity := range r.Activity {
		if activity.Kind == "finished" {
			t.Fatal("bookkeeping failure falsely marked clean completion")
		}
	}
	if r.Error == "" {
		t.Fatal("bookkeeping failure hidden")
	}
}
