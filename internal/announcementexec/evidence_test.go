package announcementexec

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEvidenceReadOnlyAndUnavailable(t *testing.T) {
	missing := newRoot(t)
	if _, e := ReadEvidence(t.Context(), missing); e == nil {
		t.Fatal("missing evidence invented")
	}
	if _, e := os.Lstat(missing); !os.IsNotExist(e) {
		t.Fatal("reader created state", e)
	}
	s := opened(t)
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
		t.Fatal(e)
	}
	before := tree(t, filepath.Dir(s.path))
	a, e := ReadEvidence(t.Context(), filepath.Dir(s.path))
	if e != nil {
		t.Fatal(e)
	}
	b, e := ReadEvidence(t.Context(), filepath.Dir(s.path))
	if e != nil {
		t.Fatal(e)
	}
	if a != b || a.StoreID != s.id || a.Version != 1 || a.Invocations != 1 || a.Outcomes != 1 || len(a.Digest) != 64 {
		t.Fatalf("bad evidence %+v %+v", a, b)
	}
	if tree(t, filepath.Dir(s.path)) != before {
		t.Fatal("reader modified live state")
	}
	for _, stmt := range []string{"UPDATE outcomes SET receipt=x'00'", "DELETE FROM invocations"} {
		root := newRoot(t)
		bad, e := Open(root)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = bad.Execute(t.Context(), scope, callFor("accepted")); e != nil {
			t.Fatal(e)
		}
		if _, e = bad.db.Exec(stmt); e != nil {
			t.Fatal(e)
		}
		bad.Close()
		before := tree(t, root)
		if _, e = ReadEvidence(t.Context(), root); e == nil {
			t.Fatal("inconsistent evidence accepted")
		}
		if tree(t, root) != before {
			t.Fatal("corrupt evidence repaired")
		}
		if reopened, e := Open(root); e == nil {
			reopened.Close()
			t.Fatal("inconsistent writer state accepted")
		}
	}
}

func TestEvidenceConcurrentWithExecute(t *testing.T) {
	s := opened(t)
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		defer close(done)
		for range 20 {
			if _, e := s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
				t.Error(e)
				return
			}
		}
	})
	for {
		select {
		case <-done:
			wg.Wait()
			counts(t, s, 20, 1)
			return
		default:
			ev, e := ReadEvidence(t.Context(), filepath.Dir(s.path))
			if e != nil && !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
			if e == nil && (ev.Outcomes > ev.Invocations || ev.Outcomes > 1) {
				t.Fatalf("torn evidence %+v", ev)
			}
		}
	}
}

func TestEvidenceReaderDoesNotPoisonCommit(t *testing.T) {
	s := opened(t)
	ro, e := database(s.path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	c, e := ro.Conn(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e = c.ExecContext(t.Context(), "BEGIN"); e != nil {
		t.Fatal(e)
	}
	var count int
	if c.QueryRowContext(t.Context(), "SELECT count(*) FROM invocations").Scan(&count) != nil {
		t.Fatal("reader lock unavailable")
	}
	started := make(chan struct{})
	s.commitFault = func(phase string, committed bool) error {
		if phase == "admission" && !committed {
			close(started)
		}
		return nil
	}
	result := make(chan error, 1)
	go func() { _, e := s.Execute(t.Context(), scope, callFor("accepted")); result <- e }()
	<-started
	if _, e = c.ExecContext(t.Context(), "COMMIT"); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e != nil {
		t.Fatal("reader poisoned writer", e)
	}
	counts(t, s, 1, 1)
}
