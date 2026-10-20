package announcementexec

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

var errInconsistent = errors.New("announcement executor: inconsistent stored outcome")

func sqliteCode(err error) int {
	var e interface{ Code() int }
	if errors.As(err, &e) {
		return e.Code() & 0xff
	}
	return -1
}
func busy(err error) bool { return sqliteCode(err) == 5 }
func corrupt(err error) bool {
	return errors.Is(err, errInconsistent) || sqliteCode(err) == 11 || sqliteCode(err) == 26
}
func busyExec(ctx context.Context, c *sql.Conn, statement string) error {
	_, err := c.ExecContext(ctx, statement)
	if busy(err) && ctx.Err() == nil {
		_, err = c.ExecContext(ctx, statement)
	}
	return err
}
func discard(c *sql.Conn) {
	// A failed COMMIT must not leave an active transaction in database/sql's pool.
	_ = c.Raw(func(any) error { return driver.ErrBadConn })
	_ = c.Close()
}

func (s *Store) confirm(query string, args []any, want []byte) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := database(s.path, true)
	if err != nil {
		return false, err
	}
	defer db.Close()
	id, err := validateSchema(ctx, db)
	if err != nil || id != s.id {
		return false, ErrUnavailable
	}
	var got []byte
	err = db.QueryRowContext(ctx, query, args...).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || !bytes.Equal(got, want) {
		return false, ErrUnavailable
	}
	return true, nil
}

func (s *Store) transaction(ctx context.Context, phase string, body func(*sql.Conn) error, confirm func() (bool, error)) error {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := s.db.Conn(op)
	if err != nil {
		if corrupt(err) {
			s.uncertain = true
			return ErrUncertain
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	if err = busyExec(op, conn, "BEGIN IMMEDIATE"); err != nil {
		discard(conn)
		if corrupt(err) {
			s.uncertain = true
			return ErrUncertain
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if busy(err) {
			return ErrBusy
		}
		return ErrUnavailable
	}
	err = body(conn)
	if err != nil {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		_, rollback := conn.ExecContext(cleanup, "ROLLBACK")
		done()
		discard(conn)
		if corrupt(err) {
			s.uncertain = true
			return ErrUncertain
		}
		// No COMMIT was attempted. Discarding the connection rolls back any
		// pending work even when cancellation already invalidated the driver.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if rollback != nil {
			s.uncertain = true
			return ErrUncertain
		}
		if busy(err) {
			return ErrBusy
		}
		return ErrUnavailable
	}
	commit, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	if s.commitFault != nil {
		err = s.commitFault(phase, false)
	}
	if err == nil {
		err = busyExec(commit, conn, "COMMIT")
	}
	if err == nil && s.commitFault != nil {
		err = s.commitFault(phase, true)
	}
	done()
	if err == nil {
		conn.Close()
		return nil
	}
	// Confirm durable facts only after terminating the connection that could own
	// the old transaction. Never equate a driver error with receipt absence.
	cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
	_, rollback := conn.ExecContext(cleanup, "ROLLBACK")
	finish()
	discard(conn)
	if corrupt(err) {
		s.uncertain = true
		return ErrUncertain
	}
	present, checkErr := confirm()
	if checkErr == nil && present {
		return nil
	}
	if checkErr != nil || (busy(err) && rollback != nil) {
		s.uncertain = true
		return ErrUncertain
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if busy(err) {
		return ErrBusy
	}
	return ErrUnavailable
}

// Execute records each admitted invocation before deduplicating a simulated
// receipt. It does not supply approval authority or perform an external effect.
// ErrUncertain deliberately never wraps a context error: the controller may
// record EXECUTOR_ERROR while storage has a receipt, requiring operator review.
func (s *Store) Execute(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
	if scope.SessionUID == "" || call.Tool != announcement.ToolName || call.Mediation != api.MediationRequiresApproval {
		return api.ToolResult{}, errors.New("announcement executor: invalid host context")
	}
	snapshot, err := announcement.FromArgs(call.Args)
	if err != nil || call.ID != snapshot.Operation || call.IdempotencyKey != snapshot.Operation {
		return api.ToolResult{}, errors.New("announcement executor: invalid operation binding")
	}
	canonical, err := announcement.Encode(snapshot)
	if err != nil {
		return api.ToolResult{}, err
	}
	select {
	case <-ctx.Done():
		return api.ToolResult{}, ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if ctx.Err() != nil {
		return api.ToolResult{}, ctx.Err()
	}
	if s.uncertain {
		return api.ToolResult{}, ErrUncertain
	}
	if s.closed {
		return api.ToolResult{}, ErrUnavailable
	}
	var invocation int64
	conflict := false
	err = s.transaction(ctx, "admission", func(c *sql.Conn) error {
		var bound []byte
		e := c.QueryRowContext(ctx, `SELECT CASE WHEN typeof(snapshot)='blob' AND length(snapshot)<=409600 THEN snapshot ELSE NULL END FROM bindings WHERE session_uid=? AND operation=?`, scope.SessionUID, snapshot.Operation).Scan(&bound)
		if errors.Is(e, sql.ErrNoRows) {
			_, e = c.ExecContext(ctx, `INSERT INTO bindings VALUES(?,?,?)`, scope.SessionUID, snapshot.Operation, canonical)
		} else if e == nil {
			if !validSnapshot(bound, snapshot.Operation) {
				return errInconsistent
			}
			var first []byte
			e = c.QueryRowContext(ctx, `SELECT CASE WHEN typeof(snapshot)='blob' AND length(snapshot)<=409600 THEN snapshot ELSE NULL END FROM invocations WHERE session_uid=? AND operation=? ORDER BY id LIMIT 1`, scope.SessionUID, snapshot.Operation).Scan(&first)
			if errors.Is(e, sql.ErrNoRows) {
				return errInconsistent
			}
			if e != nil {
				return e
			}
			if !bytes.Equal(first, bound) {
				return errInconsistent
			}
			conflict = !bytes.Equal(bound, canonical)
		}
		if e != nil {
			return e
		}
		result, e := c.ExecContext(ctx, `INSERT INTO invocations(session_uid,operation,snapshot) VALUES(?,?,?)`, scope.SessionUID, snapshot.Operation, canonical)
		if e != nil {
			return e
		}
		invocation, e = result.LastInsertId()
		return e
	}, func() (bool, error) {
		return s.confirm(`SELECT CASE WHEN typeof(snapshot)='blob' AND length(snapshot)<=409600 THEN snapshot ELSE NULL END FROM invocations WHERE id=? AND session_uid=? AND operation=?`, []any{invocation, scope.SessionUID, snapshot.Operation}, canonical)
	})
	if err != nil {
		return api.ToolResult{}, err
	}
	if conflict {
		return api.ToolResult{}, ErrConflict
	}
	if s.afterAdmission != nil {
		s.afterAdmission()
	}
	receipt, _ := json.Marshal(announcement.Receipt{Kind: announcement.Kind, Snapshot: snapshot})
	err = s.transaction(ctx, "outcome", func(c *sql.Conn) error {
		var existing []byte
		e := c.QueryRowContext(ctx, `SELECT CASE WHEN typeof(receipt)='blob' AND length(receipt)<=409600 THEN receipt ELSE NULL END FROM outcomes WHERE session_uid=? AND operation=?`, scope.SessionUID, snapshot.Operation).Scan(&existing)
		if e == nil {
			if !bytes.Equal(existing, receipt) {
				return errInconsistent
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = c.ExecContext(ctx, `INSERT INTO outcomes VALUES(?,?,?)`, scope.SessionUID, snapshot.Operation, receipt)
		return e
	}, func() (bool, error) {
		return s.confirm(`SELECT CASE WHEN typeof(receipt)='blob' AND length(receipt)<=409600 THEN receipt ELSE NULL END FROM outcomes WHERE session_uid=? AND operation=?`, []any{scope.SessionUID, snapshot.Operation}, receipt)
	})
	if err != nil {
		return api.ToolResult{}, err
	}
	return api.ToolResult{Output: map[string]any{"kind": announcement.Kind, "snapshot": announcement.Args(snapshot)}}, nil
}
