package contract

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func northwindRef(t *testing.T, member string) SuiteRef {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "northwind-prd")
	data, err := os.ReadFile(filepath.Join(root, "layouts", member, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &index); err != nil || len(index.Manifests) != 1 {
		t.Fatal("invalid fixture index", err)
	}
	return SuiteRef{Directory: filepath.Join(root, "suites", member), Artifact: filepath.Join(root, "layouts", member), Digest: index.Manifests[0].Digest, Member: member}
}

func TestSuiteArchiveIdentityAndReopen(t *testing.T) {
	ref := northwindRef(t, "writer")
	b, err := LoadSuite("draft-writer", ref)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != "draft-writer" || b.Suite.Member != "writer" || b.Suite.Digest != ref.Digest || len(b.Digest) != 64 || b.Model != "qwen3:8b" || !strings.Contains(b.Instructions, "fictional Northwind") {
		t.Fatalf("incorrect execution identity: %+v", b)
	}
	if b.Suite.Digest == b.Suite.ContentDigest || b.Suite.Digest == b.Suite.GraphDigest || b.Suite.GraphDigest == b.Suite.MemberDigest {
		t.Fatal("conflated identity domains")
	}
	if len(b.Suite.Compositions) != 2 || b.Suite.Compositions[0].Platform.Architecture != "amd64" || b.Suite.Compositions[1].Platform.Architecture != "arm64" || b.Suite.Compositions[0].BuildProfile != "canvas-provenance" {
		t.Fatalf("incomplete composition identity: %+v", b.Suite.Compositions)
	}
	parsed, err := Parse(b.Source)
	if err != nil || !reflect.DeepEqual(b, parsed) {
		t.Fatal("archive reopen differs", err)
	}
	selected, err := ForModel(b, "local-other")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Digest == b.Digest || selected.AuthoredDigest != b.Digest || selected.Model != "local-other" || !reflect.DeepEqual(selected.Suite, b.Suite) {
		t.Fatal("selection lost authored or suite identity")
	}
	reopened, err := Parse(selected.Source)
	if err != nil || reopened.Model != "local-other" || reopened.AuthoredDigest != b.Digest {
		t.Fatal("selection archive not durable", err)
	}
	if Profile(b) != 4 || !reflect.DeepEqual(Profiles(b), []int{4}) {
		t.Fatal("generic profile is not isolated")
	}
	binding, err := (LocalBinder{}).Resolve(b)
	if err != nil || binding.Harness != "bundle-v4-"+b.Digest {
		t.Fatal("wrong binding", err)
	}
	b.Suite.Member = "forged"
	if _, err := ForModel(b, b.Model); err == nil {
		t.Fatal("caller suite metadata accepted")
	}
}

func TestSuiteNormalizesWritableCheckoutModes(t *testing.T) {
	ref := northwindRef(t, "writer")
	root := t.TempDir()
	for name, source := range map[string]string{"content": ref.Directory, "layout": ref.Artifact} {
		if err := os.CopyFS(filepath.Join(root, name), os.DirFS(source)); err != nil {
			t.Fatal(err)
		}
	}
	ref.Directory = filepath.Join(root, "content")
	ref.Artifact = filepath.Join(root, "layout")
	setModes := func(mode fs.FileMode) {
		t.Helper()
		for _, view := range []string{ref.Directory, ref.Artifact} {
			if err := filepath.WalkDir(view, func(name string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				return os.Chmod(name, mode)
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	setModes(0644)
	baseline, err := LoadSuite("local-writer", ref)
	if err != nil {
		t.Fatal("0644 checkout refused", err)
	}
	setModes(0664)
	loaded, err := LoadSuite("local-writer", ref)
	if err != nil {
		t.Fatal("0664 checkout refused", err)
	}
	if !reflect.DeepEqual(loaded, baseline) {
		t.Fatal("checkout write permissions changed suite or execution identity")
	}
	var archive executionArchive
	if err := json.Unmarshal(loaded.Source, &archive); err != nil {
		t.Fatal(err)
	}
	for view, files := range map[string]map[string]retainedFile{ref.Directory: archive.Directory, ref.Artifact: archive.Artifact} {
		if len(files) == 0 {
			t.Fatal("empty retained view", view)
		}
		for name, file := range files {
			if file.Mode != 0644 {
				t.Fatalf("retained %s/%s mode = %04o, want 0644", view, name, file.Mode)
			}
			info, err := os.Stat(filepath.Join(view, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0664 {
				t.Fatalf("source %s/%s mode = %04o, want unchanged 0664", view, name, info.Mode().Perm())
			}
		}
	}
	reopened, err := Parse(loaded.Source)
	if err != nil || !reflect.DeepEqual(loaded, reopened) {
		t.Fatal("normalized archive reopen differs", err)
	}
}

func TestSuiteRefusesPinsMembersAndChangedCopy(t *testing.T) {
	ref := northwindRef(t, "writer")
	for _, change := range []func(*SuiteRef){func(r *SuiteRef) { r.Digest = "sha256:" + strings.Repeat("0", 64) }, func(r *SuiteRef) { r.Member = "reviewer" }, func(r *SuiteRef) { r.Artifact = northwindRef(t, "reviewer").Artifact }} {
		r := ref
		change(&r)
		if _, err := LoadSuite("writer", r); err == nil {
			t.Fatal("accepted mismatched suite")
		}
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(ref.Directory)); err != nil {
		t.Fatal(err)
	}
	ref.Directory = root
	p := filepath.Join(root, "instructions", "writer.md")
	if err := os.WriteFile(p, []byte("credential-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSuite("writer", ref)
	if err == nil || strings.Contains(err.Error(), "credential-canary") {
		t.Fatal("tamper not safely refused", err)
	}
}

func TestSuiteSnapshotRefusesUnsafeAndBoundedInputs(t *testing.T) {
	ref := northwindRef(t, "writer")
	for _, mode := range []string{"root-link", "ancestor-link", "entry-link", "oversize", "count", "directory-layout"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "content")
			if err := os.CopyFS(root, os.DirFS(ref.Directory)); err != nil {
				t.Fatal(err)
			}
			r := ref
			r.Directory = root
			switch mode {
			case "root-link":
				os.Symlink(root, filepath.Join(base, "linked"))
				r.Directory = filepath.Join(base, "linked")
			case "ancestor-link":
				os.Symlink(base, filepath.Join(base, "linked"))
				r.Directory = filepath.Join(base, "linked", "content")
			case "entry-link":
				os.Symlink("instructions/writer.md", filepath.Join(root, "link"))
			case "oversize":
				os.WriteFile(filepath.Join(root, "large"), bytes.Repeat([]byte("x"), (1<<20)+1), 0600)
			case "count":
				for i := 0; i < 129; i++ {
					os.WriteFile(filepath.Join(root, strings.Repeat("x", i+1)), nil, 0600)
				}
			case "directory-layout":
				os.WriteFile(filepath.Join(root, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0600)
			}
			if _, err := LoadSuite("writer", r); err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
		})
	}
}
