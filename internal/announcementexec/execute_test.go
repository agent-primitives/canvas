package announcementexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func callFor(content string) api.ToolCall {
	h := sha256.Sum256([]byte(content))
	snap := announcement.Snapshot{Operation: "op-1", Definition: strings.Repeat("a", 64), Action: "announce", Document: "announcement", Path: "announcement.md", Proposal: "proposal-1", Revision: strings.Repeat("b", 40), Digest: hex.EncodeToString(h[:]), Content: content}
	return api.ToolCall{ID: snap.Operation, Tool: announcement.ToolName, IdempotencyKey: snap.Operation, Mediation: api.MediationRequiresApproval, Args: announcement.Args(snap)}
}
func opened(t *testing.T) *Store {
	t.Helper()
	s, e := Open(newRoot(t))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func counts(t *testing.T, s *Store, wantInv, wantOut int) {
	t.Helper()
	var i, o int
	if s.db.QueryRow("SELECT count(*) FROM invocations").Scan(&i) != nil || s.db.QueryRow("SELECT count(*) FROM outcomes").Scan(&o) != nil {
		t.Fatal("counts unavailable")
	}
	if i != wantInv || o != wantOut {
		t.Fatalf("counts=%d/%d want%d/%d", i, o, wantInv, wantOut)
	}
}

var scope = controller.ToolCallContext{SessionUID: "session-1"}

func TestExecuteExactNativeBinding(t *testing.T) {
	s := opened(t)
	for _, name := range []string{"session", "tool", "mediation", "call", "key", "args"} {
		c := callFor("accepted")
		sc := scope
		switch name {
		case "session":
			sc.SessionUID = ""
		case "tool":
			c.Tool = "other"
		case "mediation":
			c.Mediation = api.MediationControllerMediated
		case "call":
			c.ID = "other"
		case "key":
			c.IdempotencyKey = "other"
		case "args":
			c.Args["extra"] = "bad"
		}
		if _, e := s.Execute(t.Context(), sc, c); e == nil {
			t.Fatal("invalid context admitted", name)
		}
	}
	counts(t, s, 0, 0)
}
func TestExecuteMatchingDedupAndConflict(t *testing.T) {
	s := opened(t)
	for range 2 {
		r, e := s.Execute(t.Context(), scope, callFor("accepted"))
		if e != nil {
			t.Fatal(e)
		}
		receipt, e := announcement.DecodeReceipt(r.Output)
		if e != nil || receipt.Snapshot.Content != "accepted" || r.IsError {
			t.Fatalf("receipt %+v %v", r, e)
		}
	}
	counts(t, s, 2, 1)
	if _, e := s.Execute(t.Context(), scope, callFor("different")); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	counts(t, s, 3, 1)
	sc := scope
	sc.SessionUID = "session-2"
	if _, e := s.Execute(t.Context(), sc, callFor("accepted")); e != nil {
		t.Fatal(e)
	}
	counts(t, s, 4, 2)
}
func TestExecuteConcurrentDuplicates(t *testing.T) {
	s := opened(t)
	var wg sync.WaitGroup
	fail := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			_, e := s.Execute(t.Context(), scope, callFor("accepted"))
			if e != nil {
				fail <- e
			}
		})
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		t.Error(e)
	}
	counts(t, s, 16, 1)
}
func TestExecuteAdmissionCutPreservesBinding(t *testing.T) {
	root := newRoot(t)
	s, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	s.afterAdmission = cancel
	if _, e = s.Execute(ctx, scope, callFor("accepted")); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	counts(t, s, 1, 0)
	s.Close()
	s, e = Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Execute(t.Context(), scope, callFor("different")); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	counts(t, s, 2, 0)
	if _, e = s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
		t.Fatal(e)
	}
	counts(t, s, 3, 1)
}
func TestExecuteCommitErrorReconciliation(t *testing.T) {
	for _, kind := range []string{"committed", "absent", "unknown", "canceled-commit", "cancel-before-commit"} {
		t.Run(kind, func(t *testing.T) {
			s := opened(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.commitFault = func(phase string, committed bool) error {
				if phase != "outcome" {
					return nil
				}
				if kind == "cancel-before-commit" {
					if !committed {
						cancel()
					}
					return nil
				}
				if kind == "absent" && !committed {
					return errors.New("before commit")
				}
				if committed {
					if kind == "unknown" {
						if e := os.Rename(s.path, s.path+".saved"); e != nil {
							t.Fatal(e)
						}
					}
					if kind == "canceled-commit" {
						cancel()
						return ctx.Err()
					}
					return errors.New("lost commit acknowledgement")
				}
				return nil
			}
			r, e := s.Execute(ctx, scope, callFor("accepted"))
			switch kind {
			case "committed", "canceled-commit", "cancel-before-commit":
				if e != nil || r.Output == nil {
					t.Fatalf("known committed receipt lost: %+v %v", r, e)
				}
				counts(t, s, 1, 1)
			case "absent":
				if e == nil || errors.Is(e, ErrUncertain) {
					t.Fatal(e)
				}
				counts(t, s, 1, 0)
			case "unknown":
				if !errors.Is(e, ErrUncertain) || errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
					t.Fatal(e)
				}
				if e = os.Rename(s.path+".saved", s.path); e != nil {
					t.Fatal(e)
				}
				if _, e = s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
					t.Fatal("uncertain store not fenced", e)
				}
				counts(t, s, 1, 1)
			}
		})
	}
}
func TestExecuteCanceledSQLiteOperationWrapsContext(t *testing.T) {
	s := opened(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := s.Execute(ctx, scope, callFor("accepted")); !errors.Is(e, ctx.Err()) {
		t.Fatal(e)
	}
	counts(t, s, 0, 0)
}
func TestExecuteBusyIsNotUncertain(t *testing.T) {
	s := opened(t)
	other, e := database(s.path, false)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	conn, e := other.Conn(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	_, e = s.Execute(t.Context(), scope, callFor("accepted"))
	if !errors.Is(e, ErrBusy) || errors.Is(e, ErrUncertain) {
		t.Fatal(e)
	}
	conn.ExecContext(t.Context(), "ROLLBACK")
	counts(t, s, 0, 0)
	if _, e = s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
		t.Fatal(e)
	}
	counts(t, s, 1, 1)
}
