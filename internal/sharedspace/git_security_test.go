package sharedspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitRevisionOptionsAreRefusedBeforeExecution(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	witness := filepath.Join(t.TempDir(), "must-not-be-written")
	oldHook := testGitHook
	t.Cleanup(func() { testGitHook = oldHook })
	calls := 0
	testGitHook = func([]string) error { calls++; return nil }
	for _, revision := range []string{
		"-", "--", "--all", "--help", "--filters", "--textconv",
		"--output=" + witness, "-c", "-c core.hooksPath=" + witness,
		"main", "refs/heads/main", "HEAD^{tree}", base + ":board.md",
		base + " --all", base + "\n--all", "$(touch " + witness + ")",
	} {
		t.Run(revision, func(t *testing.T) {
			before := calls
			if _, err := s.Read(revision, "board.md"); err == nil {
				t.Fatal("option/ref expression accepted as a read revision")
			}
			if _, err := s.Paths(revision); err == nil {
				t.Fatal("option/ref expression accepted as a tree revision")
			}
			if _, err := s.Propose(revision, map[string]string{"board.md": "attack"}, Attribution{}); err == nil {
				t.Fatal("option/ref expression accepted as a proposal base")
			}
			if calls != before {
				t.Fatal("invalid revision reached a Git subprocess")
			}
		})
	}
	if _, err := os.Lstat(witness); !os.IsNotExist(err) {
		t.Fatal("revision attack created an output file")
	}
	testGitHook = oldHook
	assertRead(t, s, base, "board.md", "original board\n")
}

func TestGitOptionShapedDocumentNamesRemainLiteral(t *testing.T) {
	root := filepath.Join(t.TempDir(), "-workspace")
	paths := []string{"-C.md", "--git-dir.md", "--exec-path.md", "docs/-branch.md"}
	seed := make(map[string]string)
	for _, path := range paths {
		seed[path] = "original"
	}
	s, err := OpenWithPolicy(root, seed, Policy{Documents: paths})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, path := range paths {
		base := head(t, s)
		content := "literal data: $(not-a-command); --output=not-an-option"
		p, err := s.Propose(base, map[string]string{path: content}, Attribution{Agent: "--all"})
		if err != nil {
			t.Fatal(err)
		}
		assertRead(t, s, p.Commit, path, content)
		if _, err := s.Accept(p.ID, base, "--help"); err != nil {
			t.Fatal(err)
		}
		assertRead(t, s, "", path, content)
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || string(got) != content {
			t.Fatal("option-shaped path was not projected as literal data")
		}
	}
	for _, path := range []string{":(top)*", ":(glob)**", "--output=escape", "docs/a\n--all"} {
		if _, err := s.Read("", path); err == nil || !strings.Contains(err.Error(), "path") {
			t.Fatalf("pathspec/control syntax accepted: %q (%v)", path, err)
		}
	}
}
