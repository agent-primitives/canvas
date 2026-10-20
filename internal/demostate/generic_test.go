package demostate_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/demostate"
)

type genericEntry struct {
	Mode  fs.FileMode
	Bytes string
}

func genericTree(t *testing.T, root string) map[string]genericEntry {
	t.Helper()
	tree := map[string]genericEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := genericEntry{Mode: info.Mode()}
		if info.Mode()&os.ModeSymlink != 0 {
			value.Bytes, err = os.Readlink(path)
		} else if info.Mode().IsRegular() {
			var raw []byte
			raw, err = os.ReadFile(path)
			value.Bytes = string(raw)
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		tree[rel] = value
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
func nativeBundle(t *testing.T) contract.Bundle {
	t.Helper()
	d, err := canvas.LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return d.Bundles[0]
}
func archiveNative(t *testing.T, root string, b contract.Bundle) {
	t.Helper()
	put(t, filepath.Join(root, "sessions", "contracts", b.Digest, "agent.yaml"), string(b.Source))
}

// Missing this boundary must reject before any mutation, even when old and native archives mix.
func TestGenericPreflightRejectsUnsafeOrLegacyStateReadOnly(t *testing.T) {
	native := nativeBundle(t)
	legacy, err := contract.Parse([]byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: old}\nspec:\n  instructions: synthetic old instruction\n  model: {name: synthetic-model}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"lock-directory", func(t *testing.T, r string) {
			if err := os.Mkdir(filepath.Join(r, ".producer.lock"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"legacy-ledger", func(t *testing.T, r string) { put(t, filepath.Join(r, "canvas", "attempts.json"), `{"attempts":[]}`) }},
		{"malformed-ledger", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "canvas", "attempts.json"), `{"definitionDigest":42}`)
		}},
		{"truncated-ledger", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "canvas", "attempts.json"), `{"definitionDigest":"bound"`)
		}},
		{"legacy-workspace", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "space", ".sharedspace", "git", "HEAD"), "synthetic")
		}},
		{"bad-policy", func(t *testing.T, r string) { put(t, filepath.Join(r, "space", ".sharedspace", "policy.json"), "null") }},
		{"ambiguous-journal", func(t *testing.T, r string) { put(t, filepath.Join(r, "sessions", "journal.db"), "synthetic journal") }},
		{"legacy-archive", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "sessions", "contracts", legacy.Digest, "agent.yaml"), string(legacy.Source))
		}},
		{"mixed-archives", func(t *testing.T, r string) {
			archiveNative(t, r, native)
			put(t, filepath.Join(r, "sessions", "contracts", legacy.Digest, "agent.yaml"), string(legacy.Source))
			put(t, filepath.Join(r, "sessions", "journal.db"), "synthetic journal")
		}},
		{"wrong-digest", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "sessions", "contracts", strings.Repeat("a", 64), "agent.yaml"), string(native.Source))
		}},
		{"incomplete-archive", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "sessions", "contracts", native.Digest, "other"), "incomplete")
		}},
		{"staging-file", func(t *testing.T, r string) {
			put(t, filepath.Join(r, "sessions", "contracts", ".staging"), "not a directory")
		}},
	}
	for _, name := range []string{".producer.lock", "sessions", "sessions/contracts", "sessions/journal.db", "sessions/contracts/.staging", "canvas/attempts.json", "space/.sharedspace/policy.json", "producer-current.json"} {
		name := name
		cases = append(cases, struct {
			name  string
			setup func(*testing.T, string)
		}{"symlink-" + name, func(t *testing.T, r string) {
			path := filepath.Join(r, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			if err := os.Chmod(root, 0751); err != nil {
				t.Fatal(err)
			}
			before := genericTree(t, root)
			if err := demostate.PreflightGeneric(root); err == nil {
				t.Fatal("unsafe or legacy state accepted")
			}
			if !reflect.DeepEqual(before, genericTree(t, root)) {
				t.Fatal("refusal changed state")
			}
		})
	}
}
func TestGenericPreflightChecksOriginalAndSelectedProducer(t *testing.T) {
	for _, tc := range []struct{ producer, evidence string }{{"original", "ledger"}, {"selected", "ledger"}, {"original", "policy"}, {"selected", "policy"}} {
		t.Run(tc.producer+"/"+tc.evidence, func(t *testing.T) {
			root := t.TempDir()
			rel := "demo-generations/0123456789abcdef0123456789abcdef"
			selected := filepath.Join(root, rel)
			for _, r := range []string{root, selected} {
				put(t, filepath.Join(r, "canvas", "attempts.json"), `{"definitionDigest":"generic","attempts":[]}`)
				put(t, filepath.Join(r, "space", ".sharedspace", "policy.json"), `{"documents":["doc.md"],"inputs":[]}`)
			}
			put(t, filepath.Join(selected, "archive.json"), `{"previous":".","space":"space","canvas":"canvas"}`)
			put(t, filepath.Join(root, "producer-current.json"), `{"generation":"`+rel+`"}`)
			if err := demostate.PreflightGeneric(root); err != nil {
				t.Fatalf("native generation refused: %v", err)
			}
			target := root
			if tc.producer == "selected" {
				target = selected
			}
			if tc.evidence == "ledger" {
				put(t, filepath.Join(target, "canvas", "attempts.json"), `{"attempts":[]}`)
			} else if err := os.Remove(filepath.Join(target, "space", ".sharedspace", "policy.json")); err != nil {
				t.Fatal(err)
			} else {
				put(t, filepath.Join(target, "space", ".sharedspace", "git", "HEAD"), "legacy workspace")
			}
			before := genericTree(t, root)
			if err := demostate.PreflightGeneric(root); err == nil {
				t.Fatal("legacy producer accepted")
			}
			if !reflect.DeepEqual(before, genericTree(t, root)) {
				t.Fatal("refusal changed producer state")
			}
		})
	}
}
func TestGenericPreflightAllowsFreshNativeAndLargeLedger(t *testing.T) {
	root := t.TempDir()
	if err := demostate.PreflightGeneric(filepath.Join(root, "missing")); err != nil {
		t.Fatal(err)
	}
	archiveNative(t, root, nativeBundle(t))
	put(t, filepath.Join(root, "sessions", "journal.db"), "journal content is not opened")
	put(t, filepath.Join(root, "sessions", "contracts", ".staging", "unfinished"), "unpublished")
	raw, err := json.Marshal(map[string]any{"definitionDigest": "generic", "attempts": []any{}, "reports": map[string]string{"large": strings.Repeat("x", 2<<20)}})
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "canvas", "attempts.json"), string(raw))
	before := genericTree(t, root)
	if err := demostate.PreflightGeneric(root); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, genericTree(t, root)) {
		t.Fatal("preflight changed state")
	}
	ancestor := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(root), ancestor); err != nil {
		t.Fatal(err)
	}
	if err := demostate.PreflightGeneric(filepath.Join(ancestor, filepath.Base(root))); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(root, actual); err != nil {
		t.Fatal(err)
	}
	if err := demostate.PreflightGeneric(actual); err == nil {
		t.Fatal("symlink state accepted")
	}
}
