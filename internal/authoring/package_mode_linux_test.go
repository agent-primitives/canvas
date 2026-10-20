package authoring

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

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
