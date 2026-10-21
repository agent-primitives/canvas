package authoring

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// Catches portable file modes exposing captured assets before validation refuses
// a binding whose destination parent is shared with other users.
func TestBindDraftValidationStagingStaysPrivate(t *testing.T) {
	if source := os.Getenv("CANVAS_BIND_PRIVACY_TEST_SOURCE"); source != "" {
		output := os.Getenv("CANVAS_BIND_PRIVACY_TEST_OUTPUT")
		if _, err := BindDraft(filepath.Join(source, "canvas.yaml"), output, "writer", bindingSuite()); !errors.Is(err, ErrBinding) {
			t.Fatalf("wrong binding refusal: %v", err)
		}
		if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refusal reserved destination: %v", err)
		}
		return
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("strace unavailable")
	}
	source := packageFixture(t, "northwind-released")
	bindingAssets(t, source)
	filename := filepath.Join(source, "canvas.yaml")
	raw := packageRead(t, filename)
	invalid := bytes.Replace(raw, []byte("propose: [prd, announcement]"), []byte("propose: [unknown]"), 1)
	if bytes.Equal(raw, invalid) {
		t.Fatal("fixture lacks expected proposal scope")
	}
	packageWrite(t, filename, invalid)
	// A direct /tmp child avoids t.TempDir's private ancestor hiding the bug.
	parent, err := os.MkdirTemp("/tmp", "canvas-bind-privacy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(parent); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	cmd := exec.Command(strace, "-f", "-yy", "-e", "trace=mkdir,mkdirat,chmod,fchmod,fchmodat,rename,renameat,renameat2", "-o", trace, os.Args[0], "-test.run=^TestBindDraftValidationStagingStaysPrivate$")
	cmd.Env = append(os.Environ(), "CANVAS_BIND_PRIVACY_TEST_SOURCE="+source, "CANVAS_BIND_PRIVACY_TEST_OUTPUT="+filepath.Join(parent, "bound"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("binding syscall exercise failed: %v\n%s", err, output)
	}
	raw, err = os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	creation := regexp.MustCompile(`(?m)\bmkdir(?:at)?\([^"\n]*"(` + regexp.QuoteMeta(filepath.Join(parent, ".canvas-bind-")) + `[^"/]+)", 0700\)\s+= 0(?:\s|$)`)
	roots := creation.FindAllSubmatch(raw, -1)
	if len(roots) != 1 {
		t.Fatalf("expected one successful private staging root creation, got %d\n%s", len(roots), raw)
	}
	root := regexp.QuoteMeta(string(roots[0][1]))
	// Match the exact outer root, not canonical-mode child directories/files.
	exposure := regexp.MustCompile(`\b(?:chmod|fchmod|fchmodat)\([^\n]*(?:"` + root + `"|<` + root + `>), 0755[,)]`)
	for _, line := range strings.Split(string(raw), "\n") {
		if exposure.MatchString(line) {
			t.Fatalf("validation staging root became accessible to other users: %s", line)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refusal left staging or output behind: %v, %v", entries, err)
	}
}

func TestPackageNormalizesOwnedFilesUnderRestrictiveUmask(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	draft := filepath.Join(t.TempDir(), "draft")
	if _, err := Init(root, draft, "single-review", "portable"); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPackages(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0077)
	defer syscall.Umask(old)
	candidate, err := store.Prepare(draft, "canvas.yaml")
	if err != nil {
		t.Fatal("owned frozen wrapper modes depended on host umask", err)
	}
	if _, err = store.Load(candidate.ID); err != nil {
		t.Fatal(err)
	}
	path, err := store.DefinitionPath(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("not canonical mode", err)
	}
}
