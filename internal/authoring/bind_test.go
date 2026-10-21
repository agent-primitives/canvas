package authoring

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"go.yaml.in/yaml/v3"
)

func bindingSuite() contract.SuiteRef {
	return contract.SuiteRef{Ref: "sha256:6ea51f953d1d51be7316fb155cbce783e7010afb43169e479f44e0cce2783ccc", Layout: "supplied/reviewer", Member: "reviewer", Platform: "linux/amd64"}
}
func bindingAssets(t *testing.T, root string) {
	t.Helper()
	if err := os.CopyFS(filepath.Join(root, "supplied/reviewer"), os.DirFS(filepath.Join(sourceCheckout(t), "examples/northwind-released/layouts/reviewer"))); err != nil {
		t.Fatal(err)
	}
}
func bindingTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if err := filepath.WalkDir(root, func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			rel, err := filepath.Rel(root, name)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = packageRead(t, name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

// Catches accidental scope edits, stale evaluation carry-over, incomplete new
// OCI layouts, and retaining the no-longer-declared old writer assets.
func TestBindDraftSuppliedSuite(t *testing.T) {
	for _, example := range []string{"northwind-released", "northwind-prd"} {
		t.Run(example, func(t *testing.T) {
			root := packageFixture(t, example)
			bindingAssets(t, root)
			packageWrite(t, filepath.Join(root, "credentials.txt"), []byte("never copy"))
			filename := filepath.Join(root, "canvas.yaml")
			raw := packageRead(t, filename)
			raw = bytes.Replace(raw, []byte("  agents:\n"), []byte("  # Preserve this author comment.\n  agents:\n"), 1)
			packageWrite(t, filename, raw)
			before := bindingTree(t, root)
			output := filepath.Join(t.TempDir(), "bound")
			files, err := BindDraft(filename, output, "writer", bindingSuite())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, bindingTree(t, root)) {
				t.Fatal("binding changed source")
			}
			if !slices.IsSorted(files) || !slices.Contains(files, "supplied/reviewer/index.json") || slices.Contains(files, "credentials.txt") {
				t.Fatalf("wrong closure: %v", files)
			}
			for _, name := range files {
				if strings.HasPrefix(name, "layouts/writer/") || strings.HasPrefix(name, "suites/writer/") || strings.HasPrefix(name, "evidence/writer.") {
					t.Fatalf("unused old slot asset copied: %s", name)
				}
			}
			d, err := canvas.LoadGenericDefinition(filepath.Join(output, "canvas.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			got := d.Agents["writer"]
			if got.Suite.Ref != "sha256:6ea51f953d1d51be7316fb155cbce783e7010afb43169e479f44e0cce2783ccc" || got.Suite.Member != "reviewer" || got.Suite.Platform != "linux/amd64" || got.Suite.Layout != "supplied/reviewer" || got.Name != "PRD writer" || !reflect.DeepEqual(got.Read, []string{"brief", "feedback", "prd"}) || !reflect.DeepEqual(got.Propose, []string{"prd", "announcement"}) || got.Evaluation != (canvas.EvaluationReference{}) {
				t.Fatalf("wrong binding tuple: %+v", got)
			}
			// Hardcoded source identity, not computed with the binding implementation.
			if !bytes.Contains(before["canvas.yaml"], []byte("member: writer")) || !bytes.Contains(before["canvas.yaml"], []byte("propose: [prd, announcement]")) {
				t.Fatal("unexpected source tuple")
			}
			var sourceConfig struct {
				Spec struct {
					Agents map[string]canvas.AgentDefinition `yaml:"agents"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal(before["canvas.yaml"], &sourceConfig); err != nil {
				t.Fatal(err)
			}
			original := sourceConfig.Spec.Agents["writer"]
			if original.Suite.Member != "writer" || original.Name != "PRD writer" || !reflect.DeepEqual(original.Read, []string{"brief", "feedback", "prd"}) || !reflect.DeepEqual(original.Propose, []string{"prd", "announcement"}) {
				t.Fatalf("unexpected before tuple: %+v", original)
			}
			if example == "northwind-released" {
				if original.Suite.Ref != "sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544" || original.Suite.Layout != "layouts/writer" || original.Suite.Platform != "linux/amd64" {
					t.Fatalf("wrong released source tuple: %+v", original.Suite)
				}
			} else if original.Suite.Digest != "sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375" || original.Suite.Directory != "suites/writer" || original.Suite.Artifact != "layouts/writer" {
				t.Fatalf("wrong legacy source tuple: %+v", original.Suite)
			}
			if !reflect.DeepEqual(d.Agents["reviewer"], sourceConfig.Spec.Agents["reviewer"]) {
				t.Fatal("other slot changed")
			}
			if !bytes.Contains(packageRead(t, filepath.Join(output, "canvas.yaml")), []byte("# Preserve this author comment.")) {
				t.Fatal("unrelated comment removed")
			}
			newAssets := bindingTree(t, filepath.Join(root, "supplied/reviewer"))
			for name, want := range newAssets {
				if !bytes.Equal(packageRead(t, filepath.Join(output, "supplied/reviewer", name)), want) {
					t.Fatalf("OCI blob changed: %s", name)
				}
			}
			for _, name := range files {
				if name != "canvas.yaml" {
					if !bytes.Equal(packageRead(t, filepath.Join(output, name)), before[name]) {
						t.Fatalf("unrelated asset changed: %s", name)
					}
				}
			}
		})
	}
}

func TestBindDraftRefusalsAreReadOnly(t *testing.T) {
	for _, kind := range []string{"new-slot", "wrong-pin", "missing-layout", "missing-member", "missing-platform", "wrong-member", "wrong-platform", "parent-layout", "absolute-layout", "mixed-legacy", "existing", "linked-parent", "invalid-definition", "duplicate-yaml", "unsupported-profile", "missing-asset", "linked-asset"} {
		t.Run(kind, func(t *testing.T) {
			root := packageFixture(t, "northwind-released")
			bindingAssets(t, root)
			filename := filepath.Join(root, "canvas.yaml")
			suite := bindingSuite()
			agent := "writer"
			out := filepath.Join(t.TempDir(), "bound")
			switch kind {
			case "new-slot":
				agent = "new-agent"
			case "wrong-pin":
				suite.Ref = "sha256:" + strings.Repeat("0", 64)
			case "missing-layout":
				suite.Layout = ""
			case "missing-member":
				suite.Member = ""
			case "missing-platform":
				suite.Platform = ""
			case "wrong-member":
				suite.Member = "writer"
			case "wrong-platform":
				suite.Platform = "linux/riscv64"
			case "parent-layout":
				suite.Layout = "../supplied/reviewer"
			case "absolute-layout":
				suite.Layout = filepath.Join(root, "supplied/reviewer")
			case "mixed-legacy":
				suite.Directory = "suites/writer"
			case "unsupported-profile":
				suite.RuntimeProfile = "unqualified"
			case "missing-asset":
				if err := os.Remove(filepath.Join(root, "supplied/reviewer/index.json")); err != nil {
					t.Fatal(err)
				}
			case "linked-asset":
				asset := filepath.Join(root, "supplied/reviewer/index.json")
				if err := os.Remove(asset); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "layouts/reviewer/index.json"), asset); err != nil {
					t.Fatal(err)
				}
			case "existing":
				if err := os.Mkdir(out, 0755); err != nil {
					t.Fatal(err)
				}
				packageWrite(t, filepath.Join(out, "keep"), []byte("unchanged"))
			case "linked-parent":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(filepath.Dir(out), alias); err != nil {
					t.Fatal(err)
				}
				out = filepath.Join(alias, "bound")
			case "invalid-definition":
				packageWrite(t, filename, bytes.Replace(packageRead(t, filename), []byte("propose: [prd, announcement]"), []byte("propose: [unknown]"), 1))
			case "duplicate-yaml":
				packageWrite(t, filename, append(packageRead(t, filename), []byte("spec: {}\n")...))
			}
			before := bindingTree(t, root)
			if _, err := BindDraft(filename, out, agent, suite); !errors.Is(err, ErrBinding) || strings.Contains(err.Error(), root) {
				t.Fatalf("wrong binding refusal: %v", err)
			}
			if !reflect.DeepEqual(before, bindingTree(t, root)) {
				t.Fatal("refusal modified source")
			}
			if kind == "existing" {
				if string(packageRead(t, filepath.Join(out, "keep"))) != "unchanged" {
					t.Fatal("overwrote existing output")
				}
			} else if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal reserved destination: %v", err)
			}
		})
	}
}

func TestBindDraftUnchangedEvaluationIsRetained(t *testing.T) {
	root := packageFixture(t, "northwind-released")
	out := filepath.Join(t.TempDir(), "bound")
	same := contract.SuiteRef{Ref: "sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544", Layout: "layouts/writer", Member: "writer", Platform: "linux/amd64"}
	if _, err := BindDraft(filepath.Join(root, "canvas.yaml"), out, "writer", same); err != nil {
		t.Fatal(err)
	}
	d, err := canvas.LoadGenericDefinition(filepath.Join(out, "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Agents["writer"].Evaluation != (canvas.EvaluationReference{Expected: "evidence/writer.expected.json", Evidence: "evidence/writer.export.json"}) {
		t.Fatal("unchanged suite lost associated evidence")
	}
}
