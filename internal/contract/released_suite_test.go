package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func releasedNorthwind(t *testing.T) SuiteRef {
	t.Helper()
	old := northwindRef(t, "writer")
	return SuiteRef{Ref: old.Digest, Layout: old.Artifact, Member: "writer", Platform: "linux/amd64"}
}

func TestReleasedSuiteExactIdentityAndArchive(t *testing.T) {
	ref := releasedNorthwind(t)
	b, err := LoadReleasedSuite("alias", ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Model != "qwen3:8b" || b.Suite.Digest != ref.Ref || b.Suite.Member != "writer" || b.Suite.Release == nil {
		t.Fatalf("wrong release: %+v", b)
	}
	s := b.Suite.Release
	if s.Platform != "linux/amd64" || s.BuildProfile != "canvas-provenance" || s.CompositionDigest == "" || s.BuildProfileDigest == "" || s.SchemaVersion != "1.0.0-draft" {
		t.Fatalf("wrong selection: %+v", s)
	}
	if len(b.Suite.Compositions) != 1 {
		t.Fatal("selected more than one composition")
	}
	if s.Evaluation.Status != "not verified" || s.Evaluation.Reason != "missing" || s.CanvasRuntime != "canvas-local/bundle-v4; execution not qualified by release evaluation" {
		t.Fatalf("unverified claim: %+v", s)
	}
	parsed, err := Parse(b.Source)
	if err != nil || !reflect.DeepEqual(b, parsed) {
		t.Fatal("release archive differs", err)
	}
	if _, err := ForModel(b, "other-model"); err == nil {
		t.Fatal("release model overridden")
	}
	same, err := ForModel(b, b.Model)
	if err != nil || !reflect.DeepEqual(b, same) {
		t.Fatal("exact model refused", err)
	}
	clone := b.Suite.Clone()
	clone.Release.Evaluation.Reason = "forged"
	if b.Suite.Release.Evaluation.Reason != "missing" {
		t.Fatal("shared mutable release identity")
	}
}

func TestReleaseCacheAcceptsEquivalentTransportIndex(t *testing.T) {
	ref := releasedNorthwind(t)
	resolver := NewReleaseResolver(filepath.Join(t.TempDir(), "private", "suite-cache"))
	original, err := LoadReleasedSuite("alias", ref, resolver)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "layout")
	if err := os.CopyFS(layout, os.DirFS(ref.Layout)); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(layout, "index.json")
	raw, _ := os.ReadFile(indexPath)
	var index map[string]any
	if json.Unmarshal(raw, &index) != nil {
		t.Fatal("index")
	}
	index["annotations"] = map[string]string{"org.opencontainers.image.ref.name": "same-release-other-layout"}
	modified, _ := json.Marshal(index)
	if err := os.WriteFile(indexPath, modified, 0600); err != nil {
		t.Fatal(err)
	}
	ref.Layout = layout
	loaded, err := LoadReleasedSuite("alias", ref, resolver)
	if err != nil || !reflect.DeepEqual(original, loaded) {
		t.Fatal("same S was confused with transport index bytes", err)
	}
}

func TestReleasedSuiteRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*SuiteRef)
	}{
		{"pin", func(r *SuiteRef) { r.Ref = "sha256:" + strings.Repeat("0", 64) }},
		{"member", func(r *SuiteRef) { r.Member = "missing" }},
		{"platform", func(r *SuiteRef) { r.Platform = "linux/riscv64" }},
		{"no-platform", func(r *SuiteRef) { r.Platform = "" }},
		{"foreign-profile", func(r *SuiteRef) { r.RuntimeProfile = "sessions-chat" }},
		{"mixed-directory", func(r *SuiteRef) { r.Directory = "ignored" }},
		{"mixed-artifact", func(r *SuiteRef) { r.Artifact = "ignored" }},
		{"mixed-digest", func(r *SuiteRef) { r.Digest = r.Ref }},
		{"tag", func(r *SuiteRef) { r.Ref = "registry.example.invalid/team/writer:latest" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := releasedNorthwind(t)
			tc.mutate(&r)
			if _, err := LoadReleasedSuite("alias", r, nil); err == nil {
				t.Fatal("unsafe or ambiguous release accepted")
			}
		})
	}
	ref := releasedNorthwind(t)
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(ref.Layout)); err != nil {
		t.Fatal(err)
	}
	ref.Layout = root
	index, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var i struct {
		Manifests []struct {
			Digest string `json:"digest"`
		}
	}
	if json.Unmarshal(index, &i) != nil {
		t.Fatal("index")
	}
	manifest := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(i.Manifests[0].Digest, "sha256:"))
	if err := os.WriteFile(manifest, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReleasedSuite("alias", ref, nil); err == nil {
		t.Fatal("tampered OCI accepted")
	}
}

func TestReleasedEvaluationRetentionAndRefusal(t *testing.T) {
	b, err := LoadReleasedSuite("alias", releasedNorthwind(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := WithReleasedEvaluation(b, []byte(`{"untrusted":"not expectations"}`), []byte(`{"forged":"evaluation consistent; unsigned"}`))
	if err != nil {
		t.Fatal(err)
	}
	e := evaluated.Suite.Release.Evaluation
	if e.Status != "not verified" || e.Reason != "malformed" || e.EvidenceDigest == "" || e.ExpectedDigest == "" {
		t.Fatalf("false evaluation: %+v", e)
	}
	if evaluated.Digest == b.Digest || evaluated.Suite.Digest != b.Suite.Digest {
		t.Fatal("archive/release domains conflated")
	}
	reopened, err := Parse(evaluated.Source)
	if err != nil || !reflect.DeepEqual(reopened, evaluated) {
		t.Fatal("evidence not retained", err)
	}
	var a releasedArchive
	if json.Unmarshal(evaluated.Source, &a) != nil {
		t.Fatal("archive")
	}
	a.Evidence = bytes.Repeat([]byte("x"), (1<<20)+1)
	raw, _ := json.Marshal(a)
	if _, err := Parse(raw); err == nil {
		t.Fatal("oversized evidence retained")
	}
}

func TestReleaseCachePrivateRootAndReuse(t *testing.T) {
	state := filepath.Join(t.TempDir(), "private")
	resolver := NewReleaseResolver(filepath.Join(state, "suite-cache"))
	ref := releasedNorthwind(t)
	b, err := LoadReleasedSuite("alias", ref, resolver)
	if err != nil {
		t.Fatal(err)
	}
	ref.Layout = ""
	ref.Ref = "registry.example.invalid/team/writer@" + ref.Ref
	reopened, err := LoadReleasedSuite("alias", ref, NewReleaseResolver(filepath.Join(state, "suite-cache")))
	if err != nil || !reflect.DeepEqual(b, reopened) {
		t.Fatal("retained bytes unavailable offline", err)
	}
	for _, name := range []string{"state-link", "cache-link", "public-state", "public-cache", "entry-link", "corrupt"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			private := filepath.Join(root, "state")
			cache := filepath.Join(private, "suite-cache")
			if err := os.Mkdir(private, 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			switch name {
			case "state-link":
				os.Remove(private)
				os.Symlink(outside, private)
			case "cache-link":
				os.Symlink(outside, cache)
			case "public-state":
				os.Chmod(private, 0755)
			case "public-cache":
				os.Mkdir(cache, 0755)
				os.Chmod(cache, 0755)
			default:
				os.Mkdir(cache, 0700)
				key := strings.TrimPrefix(releasedNorthwind(t).Ref, "sha256:") + ".json"
				if name == "entry-link" {
					os.Symlink(filepath.Join(outside, "absent"), filepath.Join(cache, key))
				} else {
					os.WriteFile(filepath.Join(cache, key), []byte("corrupt"), 0600)
				}
			}
			if _, err := LoadReleasedSuite("alias", releasedNorthwind(t), NewReleaseResolver(cache)); err == nil {
				t.Fatal("unsafe cache root/content accepted")
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("wrote through foreign/link path")
			}
		})
	}
}
