package announcementexec

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestCommitBusyRollsBackAdmission(t *testing.T) {
	s := opened(t)
	ro, e := database(s.path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	conn, e := ro.Conn(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(t.Context(), "BEGIN"); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM invocations").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrBusy) || errors.Is(e, ErrUncertain) {
		t.Fatal("read contention misclassified", e)
	}
	if _, e = conn.ExecContext(t.Context(), "COMMIT"); e != nil {
		t.Fatal(e)
	}
	counts(t, s, 0, 0)
	if _, e = s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
		t.Fatal("definite BUSY poisoned store", e)
	}
	counts(t, s, 1, 1)
}

func TestCanceledDiscardedConnectionBeforeCommit(t *testing.T) {
	s := opened(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.transaction(ctx, "admission", func(c *sql.Conn) error {
		cancel()
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		return ctx.Err()
	}, func() (bool, error) { t.Fatal("no COMMIT occurred"); return false, nil })
	if !errors.Is(err, ctx.Err()) || errors.Is(err, ErrUncertain) {
		t.Fatalf("uncommitted interruption became uncertain: %v", err)
	}
	if _, err = s.Execute(t.Context(), scope, callFor("accepted")); err != nil {
		t.Fatal("discard poisoned store", err)
	}
}

func TestCanceledInFlightSQLiteBodyIsNotAmbiguousCommit(t *testing.T) {
	s := opened(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err := s.transaction(ctx, "admission", func(c *sql.Conn) error {
		var sum int64
		return c.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n`).Scan(&sum)
	}, func() (bool, error) { t.Fatal("body failure cannot have committed"); return false, nil })
	if !errors.Is(err, ctx.Err()) || errors.Is(err, ErrUncertain) {
		t.Fatalf("lost actual run cancellation: %v", err)
	}
	if _, err = s.Execute(t.Context(), scope, callFor("accepted")); err != nil {
		t.Fatal("canceled transaction poisoned store", err)
	}
}
