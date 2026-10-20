package announcementexec

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func newRoot(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "executor") }

// Losing the lease, adopting an existing empty database, or repairing permissions
// would all make these refusals silently destroy the counter provenance.
func TestStorePrivateLifecycleAndRefusals(t *testing.T) {
	root := newRoot(t)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	id := s.id
	if len(id) != 32 {
		t.Fatalf("missing store identity: %q", id)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("second writer admitted")
	}
	for _, name := range []string{"", "executor.db", ".executor.lock"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if name == "" {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s permissions %o", name, info.Mode().Perm())
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.id != id {
		t.Fatal("store identity changed")
	}
}

func TestStoreRefusesExistingArtifactsUnchanged(t *testing.T) {
	for _, kind := range []string{"nonprivate-root", "unrelated", "empty-db", "corrupt-db", "symlink-db", "fifo-db", "directory-db", "bad-lock", "sidecar"} {
		t.Run(kind, func(t *testing.T) {
			root := newRoot(t)
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(root, "executor.db")
			switch kind {
			case "nonprivate-root":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			case "unrelated":
				if err := os.WriteFile(filepath.Join(root, "history"), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty-db":
				if err := os.WriteFile(db, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt-db":
				if err := os.WriteFile(db, []byte("not sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-db":
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), db); err != nil {
					t.Fatal(err)
				}
			case "fifo-db":
				if err := unix.Mkfifo(db, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory-db":
				if err := os.Mkdir(db, 0700); err != nil {
					t.Fatal(err)
				}
			case "bad-lock":
				if err := os.WriteFile(filepath.Join(root, ".executor.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(root, ".executor.lock"), 0644); err != nil {
					t.Fatal(err)
				}
			case "sidecar":
				if err := os.WriteFile(db+"-journal", []byte("unresolved"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := tree(t, root)
			if s, err := Open(root); err == nil {
				s.Close()
				t.Fatal("unsafe store accepted")
			}
			if after := tree(t, root); after != before {
				t.Fatalf("refusal changed files\nbefore%s\nafter%s", before, after)
			}
		})
	}
}

func tree(t *testing.T, root string) string {
	t.Helper()
	out := ""
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out += rel + ":" + info.Mode().String() + ":"
		if info.Mode().IsRegular() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			out += string(b)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			b, e := os.Readlink(path)
			if e != nil {
				return e
			}
			out += b
		}
		out += "\n"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStoreRejectsWrongVersionAndSchema(t *testing.T) {
	for _, statement := range []string{"UPDATE metadata SET version=2", "DROP TABLE outcomes"} {
		root := newRoot(t)
		s, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		s.Close()
		before := tree(t, root)
		if other, err := Open(root); err == nil {
			other.Close()
			t.Fatal("incompatible schema accepted")
		}
		if tree(t, root) != before {
			t.Fatal("incompatible state modified")
		}
	}
}
