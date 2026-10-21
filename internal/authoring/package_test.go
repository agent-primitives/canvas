package authoring

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func packageFixture(t *testing.T, example string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join(sourceCheckout(t), "examples", example))); err != nil {
		t.Fatal(err)
	}
	return root
}
func packageWrite(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0644); err != nil {
		t.Fatal(err)
	}
}
func packageRead(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func packageStore(t *testing.T) *Packages {
	t.Helper()
	s, err := OpenPackages(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Catches copying arbitrary workspace files, mutable-source fallback, lost OCI
// blobs, and confusing the package inventory identity with the compiled digest.
func TestPackageFreezeClosure(t *testing.T) {
	for _, tc := range []struct{ example, definition, mode string }{
		{"northwind-released", "canvas.yaml", "manual"},
		{"northwind-prd", "flow.yaml", "flow"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			root := packageFixture(t, tc.example)
			packageWrite(t, filepath.Join(root, "credentials.txt"), []byte("credential-canary"))
			packageWrite(t, filepath.Join(root, ".git", "config"), []byte("private"))
			if err := os.Symlink("/unrelated-secret", filepath.Join(root, "unrelated-link")); err != nil {
				t.Fatal(err)
			}
			store := packageStore(t)
			p, err := store.Prepare(root, tc.definition)
			if err != nil {
				t.Fatal(err)
			}
			again, err := store.Prepare(root, tc.definition)
			if err != nil || again.ID != p.ID {
				t.Fatalf("unstable snapshot: %v", err)
			}
			if p.RuntimeMode != tc.mode || len(p.ID) != 64 || p.ID == p.DefinitionDigest || p.Definition != tc.definition {
				t.Fatalf("bad identities: %+v", p)
			}
			if !filepath.IsAbs(p.SourceRoot) || strings.HasPrefix(p.SourceRoot, root+"/") {
				t.Fatalf("not private: %s", p.SourceRoot)
			}
			public, _ := json.Marshal(p)
			if bytes.Contains(public, []byte(p.SourceRoot)) || bytes.Contains(public, []byte("SourceRoot")) {
				t.Fatal("private root serialized")
			}
			wrapper, err := os.Stat(filepath.Dir(p.SourceRoot))
			if err != nil || wrapper.Mode().Perm() != 0755 {
				t.Fatalf("package wrapper directory mode: %v %v", wrapper, err)
			}
			path, err := store.DefinitionPath(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := canvas.LoadGenericDefinition(path)
			if err != nil || compiled.Digest != p.DefinitionDigest {
				t.Fatalf("compile frozen: %v", err)
			}
			if compiled.Seed["prd.json"] != string(packageRead(t, filepath.Join(root, "seeds", "prd.json"))) {
				t.Fatal("wrong frozen seeds")
			}
			hasBlob, hasEval, hasInput := false, false, false
			original := map[string][]byte{}
			for i, f := range p.Files {
				if f.Path == "credentials.txt" || strings.HasPrefix(f.Path, ".git/") || i > 0 && p.Files[i-1].Path >= f.Path {
					t.Fatalf("unclosed/unsorted inventory: %s", f.Path)
				}
				original[f.Path] = packageRead(t, filepath.Join(root, filepath.FromSlash(f.Path)))
				got, err := store.File(p.ID, f.Path)
				if err != nil || !bytes.Equal(got, original[f.Path]) {
					t.Fatalf("inner bytes changed %s: %v", f.Path, err)
				}
				if f.Mode != 0644 || f.Size != int64(len(got)) {
					t.Fatalf("wrapper metadata: %+v", f)
				}
				info, err := os.Stat(filepath.Join(p.SourceRoot, filepath.FromSlash(f.Path)))
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatalf("wrapper mode: %v", err)
				}
				hasBlob = hasBlob || strings.Contains(f.Path, "/blobs/sha256/")
				hasEval = hasEval || f.Path == "evidence/writer.expected.json"
				hasInput = hasInput || f.Path == "schemas/run.json"
			}
			if !hasBlob || tc.mode == "manual" && !hasEval || tc.mode == "flow" && !hasInput {
				t.Fatal("incomplete declared closure")
			}
			// Each source edit and deletion must leave the frozen package compilable,
			// with the identical mode/digest and all captured bytes.
			for _, f := range p.Files {
				source := filepath.Join(root, filepath.FromSlash(f.Path))
				packageWrite(t, source, []byte("mutated source"))
				for _, remove := range []bool{false, true} {
					if remove {
						if err := os.Remove(source); err != nil {
							t.Fatal(err)
						}
					}
					frozen, err := store.Load(p.ID)
					if err != nil || frozen.DefinitionDigest != p.DefinitionDigest || frozen.RuntimeMode != tc.mode {
						t.Fatalf("source change affected %s: %v", f.Path, err)
					}
					got, err := store.File(p.ID, f.Path)
					if err != nil || !bytes.Equal(got, original[f.Path]) {
						t.Fatalf("source fallback %s: %v", f.Path, err)
					}
				}
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DefinitionPath(p.ID); err != nil {
				t.Fatalf("deleted draft blocked read: %v", err)
			}
		})
	}
}

// Catches an identity that ignores exact byte changes or source wrapper modes.
func TestPackageIdentityAndNestedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "nested")
	if _, err := Init(sourceCheckout(t), root, "single-review", "frozen"); err != nil {
		t.Fatal(err)
	}
	store := packageStore(t)
	p, err := store.Prepare(parent, "nested/canvas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Definition != "nested/canvas.yaml" {
		t.Fatal("definition root lost")
	}
	packageWrite(t, filepath.Join(root, "seeds/draft.json"), []byte("{\"text\":\"A different reviewed seed.\"}\n"))
	q, err := store.Prepare(parent, "nested/canvas.yaml")
	if err != nil || q.ID == p.ID || q.DefinitionDigest == p.DefinitionDigest {
		t.Fatalf("changed bytes not bound: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, "seeds/draft.json"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := store.Prepare(parent, "nested/canvas.yaml")
	if err != nil || r.ID != q.ID {
		t.Fatalf("noncanonical wrapper mode: %v", err)
	}
}

// Each mutation must be rejected before any final package is published.
func TestPackageIntegrityAndLimits(t *testing.T) {
	for _, kind := range []string{"absolute", "parent", "dot", "hidden", "backslash", "case", "link", "ancestor", "fifo", "file-limit", "total-limit", "count-limit", "duplicate-yaml", "unknown-yaml", "external-schema"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "draft")
			if _, err := Init(sourceCheckout(t), root, "single-review", "frozen"); err != nil {
				t.Fatal(err)
			}
			def := "canvas.yaml"
			text := string(packageRead(t, filepath.Join(root, def)))
			switch kind {
			case "absolute":
				def = filepath.Join(root, def)
			case "parent":
				def = "../draft/canvas.yaml"
			case "dot":
				def = "./canvas.yaml"
			case "hidden":
				text = strings.ReplaceAll(text, "seeds/draft.json", ".secret")
			case "backslash":
				text = strings.ReplaceAll(text, "seeds/draft.json", `seeds\draft.json`)
			case "case":
				packageWrite(t, filepath.Join(root, "layouts/writer/INDEX.json"), []byte("{}"))
			case "link":
				name := filepath.Join(root, "seeds/draft.json")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../schemas/document.json", name); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				root = alias
			case "fifo":
				name := filepath.Join(root, "layouts/writer/pipe")
				if err := syscall.Mkfifo(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "file-limit":
				packageWrite(t, filepath.Join(root, "layouts/writer/large"), make([]byte, (8<<20)+1))
			case "total-limit":
				for i := 0; i < 5; i++ {
					packageWrite(t, filepath.Join(root, fmt.Sprintf("layouts/writer/large%d", i)), make([]byte, 7<<20))
				}
			case "count-limit":
				for i := 0; i < 1025; i++ {
					packageWrite(t, filepath.Join(root, fmt.Sprintf("layouts/writer/file%04d", i)), nil)
				}
			case "duplicate-yaml":
				text += "spec: {}\n"
			case "unknown-yaml":
				text += "unknown: true\n"
			case "external-schema":
				packageWrite(t, filepath.Join(root, "schemas/document.json"), []byte(`{"$ref":"https://credential-canary.invalid/schema"}`))
			}
			if kind != "ancestor" {
				packageWrite(t, filepath.Join(root, "canvas.yaml"), []byte(text))
			}
			storage := t.TempDir()
			store, err := OpenPackages(storage)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Prepare(root, def); !errors.Is(err, ErrPackage) || strings.Contains(err.Error(), "credential-canary") {
				t.Fatalf("unsafe input accepted/leaked: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(storage, "packages"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial publication: %v %v", entries, err)
			}
		})
	}
	for _, kind := range []string{"tamper", "missing", "extra", "symlink", "unknown-manifest", "duplicate-manifest", "mode-manifest", "hash-manifest", "unsorted-manifest", "digest-manifest", "mode-runtime", "trailing-manifest", "missing-manifest", "corrupt-manifest", "linked-manifest", "extra-wrapper", "file-mode", "directory-mode", "wrapper-mode"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "draft")
			if _, err := Init(sourceCheckout(t), root, "single-review", "frozen"); err != nil {
				t.Fatal(err)
			}
			storage := t.TempDir()
			store, err := OpenPackages(storage)
			if err != nil {
				t.Fatal(err)
			}
			p, err := store.Prepare(root, "canvas.yaml")
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(p.SourceRoot, "seeds/draft.json")
			manifest := filepath.Join(storage, "packages", p.ID, "manifest.json")
			raw := packageRead(t, manifest)
			switch kind {
			case "tamper":
				packageWrite(t, file, []byte("changed"))
			case "missing":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "extra":
				packageWrite(t, filepath.Join(p.SourceRoot, "unlisted"), []byte("unreviewed"))
			case "missing-manifest":
				if err := os.Remove(manifest); err != nil {
					t.Fatal(err)
				}
			case "corrupt-manifest":
				packageWrite(t, manifest, []byte(`{"files":`))
			case "linked-manifest":
				if err := os.Remove(manifest); err != nil {
					t.Fatal(err)
				}
				foreign := filepath.Join(t.TempDir(), "manifest.json")
				packageWrite(t, foreign, raw)
				if err := os.Symlink(foreign, manifest); err != nil {
					t.Fatal(err)
				}
			case "extra-wrapper":
				packageWrite(t, filepath.Join(storage, "packages", p.ID, "unlisted"), []byte("extra"))
			case "file-mode":
				if err := os.Chmod(file, 0600); err != nil {
					t.Fatal(err)
				}
			case "wrapper-mode":
				if err := os.Chmod(filepath.Dir(p.SourceRoot), 0700); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				if err := os.Chmod(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "seeds/draft.json"), file); err != nil {
					t.Fatal(err)
				}
			default:
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "unknown-manifest":
					m["unknown"] = true
				case "duplicate-manifest":
					raw = append([]byte(`{"definition":"other.yaml",`), raw[1:]...)
				case "mode-manifest":
					m["files"].([]any)[0].(map[string]any)["mode"] = 0600
				case "hash-manifest":
					m["files"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64)
				case "unsorted-manifest":
					f := m["files"].([]any)
					f[0], f[1] = f[1], f[0]
				case "digest-manifest":
					m["definitionDigest"] = strings.Repeat("0", 64)
				case "mode-runtime":
					m["runtimeMode"] = "flow"
				case "trailing-manifest":
					raw = append(raw, []byte(` {}`)...)
				}
				if kind != "duplicate-manifest" && kind != "trailing-manifest" {
					raw, err = json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
				}
				packageWrite(t, manifest, raw)
			}
			if _, err := store.Load(p.ID); !errors.Is(err, ErrPackage) {
				t.Fatalf("corrupt package accepted: %v", err)
			}
			if _, err := store.File(p.ID, "canvas.yaml"); !errors.Is(err, ErrPackage) {
				t.Fatal("file skipped package verification")
			}
			if _, err := store.DefinitionPath(p.ID); !errors.Is(err, ErrPackage) {
				t.Fatal("definition path skipped verification")
			}
		})
	}
}

// Catches storage creation through an existing linked ancestor before validation.
func TestPackageOpenRefusesLinkedMissingAncestorReadOnly(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPackages(filepath.Join(alias, "must-not-create")); !errors.Is(err, ErrPackage) {
		t.Fatalf("linked store: %v", err)
	}
	entries, err := os.ReadDir(real)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refusal wrote through linked ancestor: %v %v", entries, err)
	}
}

// A self-consistent inventory still cannot introduce undeclared workspace files.
func TestPackageLoadRejectsForgedClosure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	if _, err := Init(sourceCheckout(t), root, "single-review", "frozen"); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	store, err := OpenPackages(storage)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Prepare(root, "canvas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	extra := []byte("private credential")
	sum := sha256.Sum256(extra)
	files := append(append([]File(nil), p.Files...), File{Path: "unrelated.txt", SHA256: fmt.Sprintf("%x", sum), Size: int64(len(extra)), Mode: 0644})
	inventory, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	identity := append([]byte(`{"definition":"canvas.yaml","files":`), inventory...)
	identity = append(identity, '}')
	idSum := sha256.Sum256(identity)
	id := fmt.Sprintf("%x", idSum)
	original := filepath.Join(storage, "packages", p.ID)
	forged := filepath.Join(storage, "packages", id)
	if err := os.Rename(original, forged); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(forged, "manifest.json")
	var m map[string]any
	if err := json.Unmarshal(packageRead(t, manifest), &m); err != nil {
		t.Fatal(err)
	}
	m["files"] = files
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	packageWrite(t, manifest, raw)
	packageWrite(t, filepath.Join(forged, "files/unrelated.txt"), extra)
	if _, err := store.Load(id); !errors.Is(err, ErrPackage) {
		t.Fatalf("forged undeclared closure accepted: %v", err)
	}
}

// Empty directories consume traversal work even though they are not published.
func TestPackageBoundsEmptyDirectoryTraversal(t *testing.T) {
	for _, operation := range []string{"prepare", "bind"} {
		t.Run(operation, func(t *testing.T) {
			root := packageFixture(t, "northwind-released")
			layout := "layouts/writer"
			if operation == "bind" {
				bindingAssets(t, root)
				layout = "supplied/reviewer"
			}
			for i := 0; i <= 4*maxPackageFiles; i++ {
				if err := os.Mkdir(filepath.Join(root, layout, fmt.Sprintf("empty-%04d", i)), 0755); err != nil {
					t.Fatal(err)
				}
			}
			before := bindingTree(t, root)
			if operation == "prepare" {
				store := packageStore(t)
				if _, err := store.Prepare(root, "canvas.yaml"); !errors.Is(err, ErrPackage) {
					t.Fatalf("unbounded directory capture accepted: %v", err)
				}
				entries, err := os.ReadDir(store.privateRoot)
				if err != nil || len(entries) != 0 {
					t.Fatalf("refused capture published files: %v %v", entries, err)
				}
			} else {
				out := filepath.Join(t.TempDir(), "bound")
				if _, err := BindDraft(filepath.Join(root, "canvas.yaml"), out, "writer", bindingSuite()); !errors.Is(err, ErrBinding) {
					t.Fatalf("unbounded directory binding accepted: %v", err)
				}
				if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refused binding reserved output: %v", err)
				}
			}
			if !reflect.DeepEqual(before, bindingTree(t, root)) {
				t.Fatal("refused traversal changed source bytes")
			}
		})
	}
}

func TestPackageRefusesDisjointAndUnsafeReads(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	if _, err := Init(sourceCheckout(t), root, "single-review", "frozen"); err != nil {
		t.Fatal(err)
	}
	store := packageStore(t)
	p, err := store.Prepare(root, "canvas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../canvas.yaml", "/canvas.yaml", "./canvas.yaml", ".git/config", `seeds\draft.json`, "absent"} {
		if _, err := store.File(p.ID, name); !errors.Is(err, ErrPackage) {
			t.Fatalf("unsafe read %q: %v", name, err)
		}
	}
	other := packageStore(t)
	if _, err := other.Load(p.ID); !errors.Is(err, ErrPackage) {
		t.Fatal("cross-store fallback")
	}
	for _, id := range []string{"../" + p.ID, strings.ToUpper(p.ID), ""} {
		if _, err := store.Load(id); !errors.Is(err, ErrPackage) {
			t.Fatal("unsafe id accepted")
		}
	}
	alias := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPackages(alias); !errors.Is(err, ErrPackage) {
		t.Fatal("linked store accepted")
	}
	loaded, err := store.Load(p.ID)
	if err != nil || !reflect.DeepEqual(p, loaded) {
		t.Fatalf("reload mismatch: %v", err)
	}
}
