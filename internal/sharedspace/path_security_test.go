package sharedspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInputRefusesTraversalAndSymlinkEscapes(t *testing.T) {
	for _, kind := range []string{"paths", "parent-link", "leaf-link", "dangling-link", "directory-leaf"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root, outside := filepath.Join(base, "space"), filepath.Join(base, "space-sibling")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			witness := filepath.Join(outside, "x.json")
			if err := os.WriteFile(witness, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := OpenWithPolicy(root, map[string]string{"docs/prd.json": `{}`}, declaredPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			target := filepath.Join(root, "feedback", "x.json")
			paths := []string{"feedback/x.json"}
			if kind == "paths" {
				paths = []string{"../feedback/x.json", witness, "feedback/../x.json", "feedback/./x.json", "feedback//x.json", "feedback/.x.json", "feedback/x..json", `feedback\x.json`, "feedback/x.json\x00", "feedback/C:x.json", "Feedback/x.json"}
			} else if kind == "parent-link" {
				if err := os.Symlink(outside, filepath.Dir(target)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "leaf-link":
					err = os.Symlink(witness, target)
				case "dangling-link":
					err = os.Symlink(filepath.Join(outside, "new.json"), target)
				case "directory-leaf":
					err = os.Mkdir(target, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before := head(t, s)
			for _, path := range paths {
				if err := s.WriteInput(path, `{"attack":true}`); err == nil {
					t.Fatalf("unsafe input accepted: %q", path)
				}
			}
			got, err := os.ReadFile(witness)
			entries, listErr := os.ReadDir(outside)
			if err != nil || string(got) != "outside" || listErr != nil || len(entries) != 1 || head(t, s) != before {
				t.Fatal("refused input changed outside files or accepted state")
			}
			if kind == "paths" {
				if err := s.WriteInput("feedback/safe.json", `{"safe":true}`); err != nil {
					t.Fatal("normal declared input refused", err)
				}
			}
		})
	}
}

func TestFileHelpersRefuseSiblingPrefixEscape(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "space"), filepath.Join(base, "space-sibling")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	witness := filepath.Join(outside, "keep")
	if err := os.WriteFile(witness, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkNoSymlinks(root, outside); err == nil {
		t.Fatal("sibling with matching root prefix passed containment")
	}
	if err := ensureDirectory(root, filepath.Join(outside, "new")); err == nil {
		t.Fatal("created an outside directory")
	}
	if err := atomicWrite(root, witness, []byte("attack")); err == nil {
		t.Fatal("published an outside file")
	}
	entries, err := os.ReadDir(outside)
	got, readErr := os.ReadFile(witness)
	if err != nil || len(entries) != 1 || readErr != nil || string(got) != "outside" {
		t.Fatal("refused helper operation changed the outside directory")
	}
}
