package sessionhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
)

func archiveProvider(context.Context, api.ModelRequest) (api.ModelResponse, error) {
	return api.ModelResponse{}, errors.New("unexpected model call")
}

// Construct interrupted filesystem states without production failpoints.
func archivedFixture(t *testing.T, root, name string, source []byte) string {
	t.Helper()
	dir := filepath.Join(root, "contracts", name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent.yaml")
	if source != nil {
		if err := os.WriteFile(path, source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRenameNoReplacePreservesExistingTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(source, target); err == nil {
		t.Fatal("rename replaced an existing target")
	}
	for _, path := range []string{source, target} {
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			t.Fatalf("rename changed %s: %v", path, err)
		}
	}
}

func TestStartIgnoresUnpublishedContractStaging(t *testing.T) {
	for _, state := range []string{"missing", "truncated", "complete", "symlink"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			old := bundle(t, "original instructions", "original-model")
			current := bundle(t, "current instructions", "current-model")
			orphan := bundle(t, "unpublished instructions", "unpublished-model")
			oldPath := archivedFixture(t, root, old.Digest, old.Source)
			before, err := os.Stat(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(".staging", current.Digest)
			switch state {
			case "missing":
				archivedFixture(t, root, stage, nil)
			case "truncated":
				archivedFixture(t, root, stage, current.Source[:len(current.Source)/2])
			case "complete":
				archivedFixture(t, root, filepath.Join(".staging", orphan.Digest), orphan.Source)
			case "symlink":
				outside := t.TempDir()
				archivedFixture(t, outside, orphan.Digest, orphan.Source)
				if err := os.MkdirAll(filepath.Join(root, "contracts", ".staging"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "contracts", stage)); err != nil {
					t.Fatal(err)
				}
			}

			h, err := Start(root, []contract.Bundle{current}, archiveProvider)
			if err != nil {
				t.Fatalf("unpublished %s staging blocked startup: %v", state, err)
			}
			t.Cleanup(func() { h.Close() })
			for _, harness := range h.harnesses {
				if harness.bundle.Digest == orphan.Digest {
					t.Fatal("unpublished staging loaded as a harness")
				}
			}
			for _, b := range []contract.Bundle{old, current} {
				path := filepath.Join(root, "contracts", b.Digest, "agent.yaml")
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, b.Source) {
					t.Fatalf("archive did not preserve exact source for %s: %v", b.Digest, err)
				}
				fi, err := os.Stat(path)
				if err != nil || fi.Mode().Perm() != 0600 {
					t.Fatalf("archive not private: %v", err)
				}
				if b.Digest == old.Digest && !os.SameFile(before, fi) {
					t.Fatal("existing archive was replaced")
				}
				create(t, connect(t, h), b)
			}
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Start(root, nil, archiveProvider)
			if err != nil {
				t.Fatalf("published archive could not reopen: %v", err)
			}
			t.Cleanup(func() { reopened.Close() })
			for _, b := range []contract.Bundle{old, current} {
				if _, ok := reopened.harnesses[contract.HarnessName(b.Digest, 1)]; !ok {
					t.Fatal("reopen lost a published identity")
				}
			}
			for _, harness := range reopened.harnesses {
				if harness.bundle.Digest == orphan.Digest {
					t.Fatal("reopen loaded unpublished staging")
				}
			}
		})
	}
}

func TestStartRefusesCorruptPublishedContracts(t *testing.T) {
	for _, state := range []string{"missing", "truncated", "tampered", "staging-prefix"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			old := bundle(t, "original instructions", "original-model")
			current := bundle(t, "current instructions", "current-model")
			name := old.Digest
			var source []byte
			switch state {
			case "truncated":
				source = old.Source[:len(old.Source)/2]
			case "tampered":
				source = current.Source
			case "staging-prefix":
				name = ".staging-" + old.Digest
			}
			path := archivedFixture(t, root, name, source)
			// Only the exact reserved directory may be ignored, never a digest
			// directory whose bytes might belong to an existing session.
			h, err := Start(root, []contract.Bundle{current}, archiveProvider)
			if err == nil {
				h.Close()
				t.Fatal("corrupted published identity accepted")
			}
			if source != nil {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, source) {
					t.Fatal("corrupted published identity was overwritten")
				}
			} else if _, err := os.Lstat(filepath.Dir(path)); err != nil {
				t.Fatal("corrupted published directory was removed")
			}
			if _, err := os.Lstat(filepath.Join(root, "contracts", current.Digest)); !os.IsNotExist(err) {
				t.Fatal("current bundle published despite archive corruption")
			}
		})
	}
}

// A symlink above the host's own directory (e.g. /home -> var/home) is fine.
func TestStartAcceptsSymlinkedAncestor(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	b := bundle(t, "current instructions", "current-model")
	for _, root := range []string{filepath.Join(link, "host"), filepath.Join(link, "missing", "host")} {
		h, err := Start(root, []contract.Bundle{b}, archiveProvider)
		if err != nil {
			t.Fatalf("symlinked ancestor refused: %v", err)
		}
		h.Close()
		resolved := filepath.Join(real, strings.TrimPrefix(root, link))
		if _, err := os.Stat(filepath.Join(resolved, "contracts", b.Digest, "agent.yaml")); err != nil {
			t.Fatal("contract not archived under the resolved path")
		}
	}
}

func TestStartRefusesContractArchiveSymlinks(t *testing.T) {
	for _, component := range []string{"root", "contracts", "digest", "source", "staging"} {
		t.Run(component, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "host")
			outside := t.TempDir()
			old := bundle(t, "original instructions", "original-model")
			current := bundle(t, "current instructions", "current-model")
			outsidePath := archivedFixture(t, outside, old.Digest, old.Source)
			link, target := root, outside
			switch component {
			case "contracts":
				link, target = filepath.Join(root, "contracts"), filepath.Join(outside, "contracts")
			case "digest":
				link, target = filepath.Join(root, "contracts", old.Digest), filepath.Dir(outsidePath)
			case "source":
				link, target = filepath.Join(root, "contracts", old.Digest, "agent.yaml"), outsidePath
			case "staging":
				archivedFixture(t, root, old.Digest, old.Source)
				link, target = filepath.Join(root, "contracts", ".staging"), filepath.Join(outside, "contracts")
			}
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			h, err := Start(root, []contract.Bundle{current}, archiveProvider)
			if err == nil {
				h.Close()
				t.Fatalf("%s symlink accepted", component)
			}
			got, err := os.ReadFile(outsidePath)
			if err != nil || !bytes.Equal(got, old.Source) {
				t.Fatal("symlink target was changed")
			}
			if _, err := os.Lstat(filepath.Join(outside, "contracts", current.Digest)); !os.IsNotExist(err) {
				t.Fatal("contract published through symlink")
			}
		})
	}
}
