package authoring

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func sourceCheckout(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInitPortableTemplates(t *testing.T) {
	for _, tc := range []struct {
		template string
		count    int
	}{{"single-review", 1}, {"writer-review", 2}} {
		t.Run(tc.template, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "checkout")
			layouts := filepath.Join("examples", "northwind-released", "layouts")
			if err := os.CopyFS(filepath.Join(source, layouts), os.DirFS(filepath.Join(sourceCheckout(t), layouts))); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "my-review")
			files, err := Init(source, dest, tc.template, "my-review")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(source); err != nil {
				t.Fatal(err)
			}
			definition, err := canvas.LoadGenericDefinition(filepath.Join(dest, "canvas.yaml"))
			if err != nil {
				t.Fatalf("generated package depends on original checkout: %v", err)
			}
			if definition.ID != "my-review" || len(definition.Documents) != tc.count || len(definition.Agents) != tc.count || len(definition.Actions) != 0 {
				t.Fatalf("wrong template shape: %+v", definition)
			}
			if len(definition.Triggers) != tc.count {
				t.Fatal("every template agent needs a manual trigger")
			}
			for _, trigger := range definition.Triggers {
				if trigger.Type != "manual" {
					t.Fatal("automatic trigger in starter")
				}
			}
			for id, agent := range definition.Agents {
				want := "sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544"
				if id == "reviewer" {
					want = "sha256:6ea51f953d1d51be7316fb155cbce783e7010afb43169e479f44e0cce2783ccc"
				}
				if agent.Suite.Ref != want || agent.Suite.Member != id || agent.Suite.Layout != "layouts/"+id || agent.Suite.Platform != "linux/amd64" {
					t.Fatalf("wrong released binding: %+v", agent.Suite)
				}
			}
			if len(files) == 0 || files[0] != "canvas.yaml" {
				t.Fatalf("missing sorted package inventory: %v", files)
			}
			for i, file := range files {
				if filepath.IsAbs(file) || strings.Contains(file, "..") || i > 0 && file <= files[i-1] {
					t.Fatal("non-portable or unsorted file inventory")
				}
				info, err := os.Stat(filepath.Join(dest, filepath.FromSlash(file)))
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatalf("output file modes: %s %v %v", file, info, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dest, ".state")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("init created runtime state")
			}
		})
	}
}

func TestInitNamesRemainYAMLStrings(t *testing.T) {
	for _, name := range []string{"null", "true", "false"} {
		dest := filepath.Join(t.TempDir(), name)
		if _, err := Init(sourceCheckout(t), dest, "single-review", name); err != nil {
			t.Fatalf("accepted name %q did not initialize: %v", name, err)
		}
		d, err := canvas.LoadGenericDefinition(filepath.Join(dest, "canvas.yaml"))
		if err != nil || d.ID != name {
			t.Fatalf("name changed YAML type: %v", err)
		}
	}
}

type swappedDestinationFS struct {
	fs.FS
	once sync.Once
	swap func()
}

func (s *swappedDestinationFS) Open(name string) (fs.File, error) {
	s.once.Do(s.swap)
	return s.FS.Open(name)
}

func TestPublishRefusesSwappedDestinationWithoutTouchingTarget(t *testing.T) {
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "document.json"), []byte("package bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dest, parked := filepath.Join(parent, "output"), filepath.Join(parent, "parked")
	foreign := t.TempDir()
	before, err := os.Stat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(foreign, before.Mode().Perm()) // Restore our temp directory even on a regression.
	source := &swappedDestinationFS{FS: os.DirFS(stage), swap: func() {
		if err := os.Rename(dest, parked); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(foreign, dest); err != nil {
			t.Fatal(err)
		}
	}}
	if err := publish(source, dest); !errors.Is(err, ErrPublish) {
		t.Errorf("publication reported success after path swap: %v", err)
	}
	after, err := os.Stat(foreign)
	if err != nil || before.Mode() != after.Mode() {
		t.Fatalf("publication chmodded unrelated target: %v", err)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatal("publication wrote into unrelated target")
	}
	if info, err := os.Lstat(dest); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("cleanup removed swapped target")
	}
}

func TestInitRefusesExistingTargets(t *testing.T) {
	for _, kind := range []string{"file", "empty-directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "existing")
			switch kind {
			case "file":
				if err := os.WriteFile(dest, []byte("keep me"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty-directory":
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), dest); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.Lstat(dest)
			if _, err := Init(sourceCheckout(t), dest, "single-review", "my-review"); !errors.Is(err, ErrDestination) {
				t.Fatalf("wrong existing destination refusal: %v", err)
			}
			after, err := os.Lstat(dest)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("init replaced existing target")
			}
			if kind == "file" {
				raw, _ := os.ReadFile(dest)
				if string(raw) != "keep me" {
					t.Fatal("existing bytes changed")
				}
			}
		})
	}
}

func TestInitWorksWithLinkedSystemTempDirectory(t *testing.T) {
	root := t.TempDir()
	temp := filepath.Join(root, "temp")
	if err := os.Mkdir(temp, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "temp-alias")
	if err := os.Symlink(temp, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	dest := filepath.Join(root, "review")
	if _, err := Init(sourceCheckout(t), dest, "single-review", "review"); err != nil {
		t.Fatalf("system temporary path prevented portable initialization: %v", err)
	}
	if _, err := canvas.LoadGenericDefinition(filepath.Join(dest, "canvas.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestInitRefusesLinkedDestinationParent(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(sourceCheckout(t), filepath.Join(alias, "review"), "single-review", "review"); !errors.Is(err, ErrDestination) {
		t.Fatalf("init reported an output that the strict loader cannot reopen: %v", err)
	}
	entries, err := os.ReadDir(real)
	if err != nil || len(entries) != 0 {
		t.Fatal("refusal wrote through a linked parent")
	}
}

func TestInitRefusesMissingDestinationParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "absent")
	if _, err := Init(sourceCheckout(t), filepath.Join(parent, "review"), "single-review", "review"); !errors.Is(err, ErrDestination) {
		t.Fatalf("wrong missing-parent refusal: %v", err)
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init created a missing parent")
	}
}

func TestPublishRemovesNewDestinationOnCopyFailure(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(stage, []byte("not a package tree"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "review")
	if err := publish(os.DirFS(stage), dest); !errors.Is(err, ErrPublish) {
		t.Fatalf("wrong publication error: %v", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed copy left its owned destination")
	}
}

func TestInitRefusesUnsafeSourcesAndOptions(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "symlink", "unknown-template", "invalid-name"} {
		t.Run(kind, func(t *testing.T) {
			source := t.TempDir()
			template, name := "single-review", "my-review"
			if kind != "missing" {
				if err := os.CopyFS(filepath.Join(source, "examples", "northwind-released", "layouts"), os.DirFS(filepath.Join(sourceCheckout(t), "examples", "northwind-released", "layouts"))); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "corrupt":
				if err := os.WriteFile(filepath.Join(source, "examples", "northwind-released", "layouts", "writer", "index.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				p := filepath.Join(source, "examples", "northwind-released", "layouts", "writer", "index.json")
				if err := os.Rename(p, p+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+"-original", p); err != nil {
					t.Fatal(err)
				}
			case "unknown-template":
				template = "unknown"
			case "invalid-name":
				name = "../credential-canary"
			}
			dest := filepath.Join(t.TempDir(), "output")
			_, err := Init(source, dest, template, name)
			want := ErrSource
			if kind == "unknown-template" || kind == "invalid-name" {
				want = ErrOptions
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "credential-canary") {
				t.Fatalf("wrong unsafe init refusal/leaked input: %v", err)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal published a package")
			}
		})
	}
}
