package contract

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const valid = "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: scout\nspec:\n  instructions: Assess fictional evidence only.\n  model:\n    name: qwen3:8b\n"

func TestExactPortableIdentity(t *testing.T) {
	b, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	frame := fmt.Sprintf("portable-agent.yaml %d\n%s\n", len(valid), valid)
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(frame)))
	if b.Digest != want || b.ID != "scout" || b.Model != "qwen3:8b" {
		t.Fatal("bundle contract or framed identity lost")
	}
	other, err := Parse([]byte(valid + "# different authored bytes\n"))
	if err != nil {
		t.Fatal(err)
	}
	if other.Digest == b.Digest {
		t.Fatal("authored-byte identity was normalized")
	}
	binding, err := (LocalBinder{}).Resolve(b)
	if err != nil || binding.Harness != "bundle-v3-"+want {
		t.Fatal("binding is not immutable")
	}
}

func TestRefuseUnsupportedContractInsteadOfDropping(t *testing.T) {
	cases := []string{valid + "surprise: true\n", valid + "extensions:\n  vendor: {}\n", valid + "extensions: null\n", valid + "---\n{}\n", valid + "metadata: {name: other}\n", valid + "extra: &x {a: b}\n"}
	for i, s := range cases {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("accepted unsupported document %d", i)
		}
	}
}

func TestLoadRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yaml")
	if err := os.WriteFile(filepath.Join(root, "source"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source", path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("symlink accepted")
	}
}
