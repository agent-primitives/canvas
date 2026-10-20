package announcementexec

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/announcement"
)

// Delegate to the real SQLite database, but observe whether malformed storage
// reaches the payload SELECT before its storage class/byte bound is checked.
type payloadObserver struct {
	*sql.DB
	table    string
	selected bool
}

func (q *payloadObserver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM "+q.table+" ") {
		q.selected = true
	}
	return q.DB.QueryContext(ctx, query, args...)
}
func TestTextPayloadRejectedBeforeScan(t *testing.T) {
	for _, item := range []struct{ table, column string }{{"bindings", "snapshot"}, {"invocations", "snapshot"}, {"outcomes", "receipt"}} {
		t.Run(item.table, func(t *testing.T) {
			s := opened(t)
			if _, e := s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
				t.Fatal(e)
			}
			text := "\x00" + strings.Repeat("x", 500000)
			if _, e := s.db.Exec("UPDATE "+item.table+" SET "+item.column+"=?", text); e != nil {
				t.Fatal(e)
			}
			observer := &payloadObserver{DB: s.db, table: item.table}
			if _, e := validateRecords(t.Context(), observer, s.id); !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
			if observer.selected {
				t.Fatal("TEXT payload reached allocating SELECT")
			}
		})
	}
}
func TestFirstInvocationMustEstablishBinding(t *testing.T) {
	for _, kind := range []string{"replaced-binding", "deleted-original"} {
		t.Run(kind, func(t *testing.T) {
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
			s.afterAdmission = nil
			if kind == "replaced-binding" {
				snapshot, _ := announcement.FromArgs(callFor("different").Args)
				raw, _ := announcement.Encode(snapshot)
				if _, e = s.db.Exec("UPDATE bindings SET snapshot=?", raw); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e = s.Execute(t.Context(), scope, callFor("different")); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
				if _, e = s.db.Exec("DELETE FROM invocations WHERE id=1"); e != nil {
					t.Fatal(e)
				}
			}
			s.Close()
			before := tree(t, root)
			if _, e = ReadEvidence(t.Context(), root); !errors.Is(e, ErrUnavailable) {
				t.Fatal("changed binding evidence accepted", e)
			}
			if other, e := Open(root); e == nil {
				other.Close()
				t.Fatal("changed binding reopened")
			}
			if tree(t, root) != before {
				t.Fatal("refusal changed source")
			}
		})
	}
}
func TestInvalidLiveBindingFencesStore(t *testing.T) {
	s := opened(t)
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("UPDATE bindings SET snapshot=x'00'"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
		t.Fatal("invalid binding treated as ordinary conflict", e)
	}
	other := scope
	other.SessionUID = "other"
	if _, e := s.Execute(t.Context(), other, callFor("accepted")); !errors.Is(e, ErrUncertain) {
		t.Fatal("invalid binding did not fence other keys", e)
	}
	counts(t, s, 1, 1)
}
func TestChangedCanonicalLiveBindingFencesStore(t *testing.T) {
	s := opened(t)
	ctx, cancel := context.WithCancel(t.Context())
	s.afterAdmission = cancel
	if _, e := s.Execute(ctx, scope, callFor("accepted")); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s.afterAdmission = nil
	snapshot, _ := announcement.FromArgs(callFor("different").Args)
	raw, _ := announcement.Encode(snapshot)
	if _, e := s.db.Exec("UPDATE bindings SET snapshot=?", raw); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
		t.Fatal("changed canonical binding not fenced", e)
	}
	counts(t, s, 1, 0)
}

func TestCleanWALRefusedWithoutSidecars(t *testing.T) {
	for _, route := range []string{"evidence", "open"} {
		t.Run(route, func(t *testing.T) {
			root := newRoot(t)
			s, e := Open(root)
			if e != nil {
				t.Fatal(e)
			}
			s.Close()
			db, e := database(filepath.Join(root, "executor.db"), false)
			if e != nil {
				t.Fatal(e)
			}
			var mode string
			if e = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); e != nil || mode != "wal" {
				t.Fatal(mode, e)
			}
			if e = db.Close(); e != nil {
				t.Fatal(e)
			}
			before := tree(t, root)
			if route == "evidence" {
				if _, e = ReadEvidence(t.Context(), root); !errors.Is(e, ErrUnavailable) {
					t.Fatal(e)
				}
			} else {
				if other, e := Open(root); e == nil {
					other.Close()
					t.Fatal("WAL accepted")
				}
			}
			if tree(t, root) != before {
				t.Fatal("readonly refusal created WAL sidecars")
			}
		})
	}
}
func TestUnexpectedSqlitexTriggerRefused(t *testing.T) {
	root := newRoot(t)
	s, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("CREATE TRIGGER sqlitex AFTER INSERT ON invocations BEGIN DELETE FROM outcomes; END"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	before := tree(t, root)
	if _, e = ReadEvidence(t.Context(), root); !errors.Is(e, ErrUnavailable) {
		t.Fatal("unexpected trigger evidence accepted", e)
	}
	if other, e := Open(root); e == nil {
		other.Close()
		t.Fatal("unexpected trigger accepted")
	}
	if tree(t, root) != before {
		t.Fatal("trigger refusal changed state")
	}
}
