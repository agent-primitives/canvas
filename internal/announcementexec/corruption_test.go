package announcementexec

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestDetectedCorruptionFencesOtherSessionKeys(t *testing.T) {
	s := opened(t)
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
		t.Fatal(e)
	}
	// Model silent receipt bit-rot after Open: SQLite's page structure remains
	// valid, but the stored bytes no longer represent this accepted snapshot.
	if _, e := s.db.Exec(`UPDATE outcomes SET receipt=x'00'`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
		t.Fatalf("corruption not fenced: %v", e)
	}
	other := scope
	other.SessionUID = "unrelated-session"
	if _, e := s.Execute(t.Context(), other, callFor("accepted")); !errors.Is(e, ErrUncertain) {
		t.Fatal("known-corrupt storage accepted another key", e)
	}
	counts(t, s, 2, 1)
}

func TestSQLiteCorruptionCodesFenceStore(t *testing.T) {
	for _, kind := range []string{"not-database", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			s := opened(t)
			s.db.SetMaxIdleConns(0)
			original, e := os.ReadFile(s.path)
			if e != nil {
				t.Fatal(e)
			}
			damaged := []byte("not a database")
			if kind == "truncated" {
				damaged = original[:100]
			}
			if e = os.WriteFile(s.path, damaged, 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
				t.Fatal("SQLite corruption not fenced", e)
			}
			if e = os.WriteFile(s.path, original, 0600); e != nil {
				t.Fatal(e)
			}
			other := scope
			other.SessionUID = "different-session"
			if _, e = s.Execute(t.Context(), other, callFor("accepted")); !errors.Is(e, ErrUncertain) {
				t.Fatal("corruption fence lost", e)
			}
		})
	}
}

func TestAdmissionCommitReconciliation(t *testing.T) {
	for _, kind := range []string{"present", "absent", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			s := opened(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.afterAdmission = cancel
			s.commitFault = func(phase string, committed bool) error {
				if phase != "admission" {
					return nil
				}
				if kind == "absent" && !committed {
					return errors.New("admission not committed")
				}
				if committed {
					if kind == "unknown" {
						if e := os.Rename(s.path, s.path+".saved"); e != nil {
							t.Fatal(e)
						}
					}
					return errors.New("lost admission acknowledgement")
				}
				return nil
			}
			_, e := s.Execute(ctx, scope, callFor("accepted"))
			switch kind {
			case "present":
				if !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
				counts(t, s, 1, 0)
			case "absent":
				if e == nil || errors.Is(e, ErrUncertain) {
					t.Fatal(e)
				}
				counts(t, s, 0, 0)
			case "unknown":
				if !errors.Is(e, ErrUncertain) {
					t.Fatal(e)
				}
				if e = os.Rename(s.path+".saved", s.path); e != nil {
					t.Fatal(e)
				}
				counts(t, s, 1, 0)
				if _, e = s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
					t.Fatal(e)
				}
				return
			}
			s.afterAdmission = nil
			s.commitFault = nil
			if _, e = s.Execute(t.Context(), scope, callFor("accepted")); e != nil {
				t.Fatal(e)
			}
			want := 1
			if kind == "present" {
				want = 2
			}
			counts(t, s, want, 1)
		})
	}
}
