package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacySuiteArchiveStableAcrossModuleUpgrade(t *testing.T) {
	ref := northwindRef(t, "writer")
	root := t.TempDir()
	// The archive retains transport file modes. Pin the same input on every checkout,
	// rather than hashing the caller's umask-dependent artifact permissions.
	for name, source := range map[string]string{"directory": ref.Directory, "artifact": ref.Artifact} {
		target := filepath.Join(root, name)
		if err := os.CopyFS(target, os.DirFS(source)); err != nil {
			t.Fatal(err)
		}
		if err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			return os.Chmod(path, 0644)
		}); err != nil {
			t.Fatal(err)
		}
		if name == "directory" {
			ref.Directory = target
		} else {
			ref.Artifact = target
		}
	}
	b, err := LoadSuite("writer", ref)
	if err != nil {
		t.Fatal(err)
	}
	// Exact execution-v1 bytes from the v0.1.0 loader with these 0644 input views.
	if b.Digest != "e21b249fa2750b738d441a05abb17a402cd7ee72878aec0fb1b39297c298500f" {
		t.Fatalf("legacy archive identity changed: %s", b.Digest)
	}
}
