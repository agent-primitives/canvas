package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// A retired managed state must refuse even a missing definition before resolver,
// lease, Sessions or SharedSpace startup can write anything.
func TestRuntimeRetiredPreflightBeforeDefinitionAndStateWrites(t *testing.T) {
	for _, sealRoot := range []string{".", "canvas"} {
		t.Run(sealRoot, func(t *testing.T) {
			state := t.TempDir()
			seal := filepath.Join(state, sealRoot)
			if err := os.MkdirAll(seal, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(seal, "retired.json"), []byte("corrupt seal also refuses"), 0600); err != nil {
				t.Fatal(err)
			}
			d, err := startDefinition(t.TempDir(), state, "127.0.0.1:0", "missing.yaml")
			if d != nil {
				d.Close()
			}
			if !errors.Is(err, canvas.ErrRetired) {
				t.Fatalf("want retirement before definition resolution, got %v", err)
			}
			for _, name := range []string{"suite-cache", "sessions", "space", ".producer.lock"} {
				if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
					t.Fatalf("retired startup touched %s: %v", name, err)
				}
			}
		})
	}
}

func frozenRuntimeFixture(t *testing.T, definition string, mutate ...func(string)) (string, *authoring.Packages, authoring.Package, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.CopyFS(source, os.DirFS(filepath.Join("..", "..", "examples", "northwind-prd"))); err != nil {
		t.Fatal(err)
	}
	for _, apply := range mutate {
		apply(source)
	}
	packages, err := authoring.OpenPackages(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := packages.Prepare(source, definition)
	if err != nil {
		t.Fatal(err)
	}
	path, err := packages.DefinitionPath(pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	return source, packages, pkg, path
}

func TestFrozenRuntimeRefusesUnqualifiedPRDFixture(t *testing.T) {
	for _, change := range []string{"path", "schema", "scope"} {
		t.Run(change, func(t *testing.T) {
			_, _, pkg, path := frozenRuntimeFixture(t, "canvas.yaml", func(source string) {
				filename := filepath.Join(source, "canvas.yaml")
				raw, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "path":
					raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "path: prd.json", "path: other.json"), "    prd.json: seeds/prd.json", "    other.json: seeds/prd.json"))
				case "scope":
					raw = []byte(strings.Replace(string(raw), "propose: [prd, announcement]", "propose: [prd]", 1))
				case "schema":
					schema := filepath.Join(source, "schemas", "prd.json")
					data, err := os.ReadFile(schema)
					if err != nil {
						t.Fatal(err)
					}
					if os.WriteFile(schema, append(data, '\n'), 0644) != nil {
						t.Fatal("schema fixture")
					}
				}
				if os.WriteFile(filename, raw, 0644) != nil {
					t.Fatal("draft mutation")
				}
			})
			state := filepath.Join(t.TempDir(), "not-created")
			prepared, err := prepareRuntime(t.TempDir(), state, path, engineOptions{}, &pkg)
			if !errors.Is(err, authoring.ErrPackage) || prepared != nil {
				t.Fatalf("unqualified Northwind-shaped fixture accepted: %v", err)
			}
			if _, err = os.Lstat(state); !os.IsNotExist(err) {
				t.Fatal("fixture refusal created state", err)
			}
		})
	}
}

func TestActivationPartialStartupSeeds(t *testing.T) {
	for _, phase := range []string{"space-only", "canvas-published"} {
		t.Run(phase, func(t *testing.T) {
			_, _, pkg, path := frozenRuntimeFixture(t, "canvas.yaml", func(source string) {
				filename := filepath.Join(source, "canvas.yaml")
				raw, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.Replace(string(raw), "  seed:\n", "  seed:\n    feedback/seed.json: seeds/feedback-seed.json\n", 1))
				if os.WriteFile(filename, raw, 0644) != nil || os.WriteFile(filepath.Join(source, "seeds", "feedback-seed.json"), []byte(`{"team":"fixture","feedback":"Reviewed starting context"}`), 0644) != nil {
					t.Fatal("seed fixture")
				}
			})
			state := filepath.Join(t.TempDir(), "partial")
			prepared, err := prepareRuntime(t.TempDir(), state, path, engineOptions{}, &pkg)
			if err != nil {
				t.Fatal(err)
			}
			space, err := sharedspace.OpenWithPolicy(filepath.Join(state, "space"), prepared.definition.Seed, prepared.definition.Policy)
			if err != nil {
				t.Fatal(err)
			}
			initialHead, err := space.Head()
			if err != nil {
				t.Fatal(err)
			}
			space.Close()
			if phase == "canvas-published" {
				opened, err := openRuntime(state, prepared)
				if err != nil {
					t.Fatal(err)
				}
				opened.Close()
			}
			again, err := prepareRuntime(t.TempDir(), state, path, engineOptions{}, &pkg)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := openRuntime(state, again)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if err = engine.canvas.ScanInputs(); err != nil {
				t.Fatal(err)
			}
			snapshot, err := engine.canvas.State()
			if err != nil || snapshot.Head != initialHead || len(snapshot.Runs) != 0 || engine.Metadata()["providerCalls"] != int64(0) {
				t.Fatalf("partial startup replayed or skipped seed intent: %+v %v", snapshot, err)
			}
		})
	}
}

// Catches accepting mismatched reviewed identity, remote authority, or a missing
// operator flow ceiling before the old runtime can be sealed by the caller.
func TestFrozenRuntimeRefusalsBeforeStateWrites(t *testing.T) {
	_, _, manual, manualPath := frozenRuntimeFixture(t, "canvas.yaml")
	_, _, flow, flowPath := frozenRuntimeFixture(t, "flow.yaml")
	for _, name := range []string{"digest", "mode", "unknown mode", "flow zero", "manual flow ceiling", "trusted", "authenticated", "options first", "provider config", "digest before provider"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(t.TempDir(), "unused")
			pkg, path, options := manual, manualPath, engineOptions{}
			want := authoring.ErrPackage
			switch name {
			case "digest":
				pkg.DefinitionDigest = strings.Repeat("0", 64)
			case "mode":
				pkg.RuntimeMode = "flow"
			case "unknown mode":
				pkg.RuntimeMode = "unknown"
			case "flow zero":
				pkg, path, want = flow, flowPath, canvas.ErrFlowRuntime
			case "manual flow ceiling":
				options.flowAgentRuns, want = 1, canvas.ErrFlowRuntime
			case "trusted":
				options.trustedEndpoint, want = "127.0.0.1:9", errEngineOptions
			case "authenticated":
				options.remote, want = &remoteEngine{}, errEngineOptions
			case "options first":
				options.modelProfile, path, want = "unknown", "missing.yaml", errEngineOptions
			case "provider config", "digest before provider":
				options.modelProfile = "hosted"
				if err := os.WriteFile(filepath.Join(root, "model.config.local.json"), []byte(`{"profile":"invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if name == "digest before provider" {
					pkg.DefinitionDigest = strings.Repeat("0", 64)
				} else {
					want = nil // Operator configuration errors preserve their useful context.
				}
			}
			prepared, err := prepareRuntime(root, state, path, options, &pkg)
			if err == nil || prepared != nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("preflight accepted or wrong refusal: prepared=%v err=%v want=%v", prepared != nil, err, want)
			}
			if name == "provider config" && !strings.Contains(err.Error(), "profile") {
				t.Fatal("provider error lost context", err)
			}
			if _, err := os.Lstat(state); !os.IsNotExist(err) {
				t.Fatal("preflight created state/cache/Sessions", err)
			}
		})
	}
}

// Catches source/cache fallback, runtime recompilation, premature provider calls,
// shared metadata maps, lost native review scope and leaked lifecycle leases.
func TestFrozenRuntimeSnapshotAndNativeDispatch(t *testing.T) {
	for _, definition := range []string{"canvas.yaml", "flow.yaml"} {
		t.Run(definition, func(t *testing.T) {
			source, packages, pkg, path := frozenRuntimeFixture(t, definition)
			seed, err := packages.File(pkg.ID, "seeds/prd.json")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "seeds/prd.json"), []byte("mutated working source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(source); err != nil {
				t.Fatal(err)
			}
			alternateRoot := t.TempDir()
			if err := os.RemoveAll(alternateRoot); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "new-instance")
			options := engineOptions{}
			if pkg.RuntimeMode == "flow" {
				options.flowAgentRuns = 12
			}
			prepared, err := prepareRuntime(alternateRoot, state, path, options, &pkg)
			if err != nil {
				t.Fatal("retained package requires working source", err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("prepare created state", err)
			}
			if prepared.definition.Digest != pkg.DefinitionDigest || prepared.calls.Load() != 0 {
				t.Fatal("preflight identity/calls changed")
			}
			// No later YAML loading is permitted; native contracts are already compiled.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			engine, err := openRuntime(state, prepared)
			if err != nil {
				t.Fatal("runtime reloaded the definition", err)
			}
			defer engine.Close()
			snapshot, err := engine.canvas.State()
			if err != nil || snapshot.Definition == nil || snapshot.Definition.Digest != pkg.DefinitionDigest || len(snapshot.Runs) != 0 || len(snapshot.Proposals) != 0 {
				t.Fatalf("unexpected initial runtime state: %+v %v", snapshot, err)
			}
			doc, err := engine.canvas.Document("prd.json", "")
			if err != nil || doc["accepted"] != string(seed) {
				t.Fatal("runtime did not use reviewed seed", doc, err)
			}
			info := engine.Metadata()
			if info["providerCalls"] != int64(0) || info["realModelCalls"] != 0 || info["mode"] != "deterministic-northwind" {
				t.Fatal("startup dispatched a model or changed metadata", info)
			}
			info["mode"] = "caller mutation"
			if engine.Metadata()["mode"] != "deterministic-northwind" {
				t.Fatal("metadata map shared")
			}
			var runID string
			if pkg.RuntimeMode == "flow" {
				if info["flowRuntime"] != true || info["requesterRole"] != "GM" {
					t.Fatal("flow authority metadata lost", info)
				}
				flow, err := engine.canvas.StartFlow(context.Background(), canvas.FlowStart{Flow: "release", RequestKey: "frozen-flow", Role: "GM", Inputs: json.RawMessage(`{"topic":"Frozen topic","audience":"product","includeRisks":false}`), AgentRuns: 12})
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					flow, err = engine.canvas.GetFlow(context.Background(), flow.ID)
					if err != nil {
						t.Fatal(err)
					}
					if flow.State == "review" {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if flow.State != "review" || flow.Proposal == "" {
					t.Fatal("native flow did not reach review", flow)
				}
				runID = flow.Attempt
			} else {
				runID, err = engine.canvas.Manual("writer", "", "Draft the fictional pilot.", "GM")
				if err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			var run canvas.Run
			for time.Now().Before(deadline) {
				run, _ = engine.canvas.GetRun(runID)
				if run.Proposal != "" || run.Status == "failed" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if run.Proposal == "" || engine.Metadata()["providerCalls"] != int64(1) {
				t.Fatalf("native dispatch lost: %+v %+v", run, engine.Metadata())
			}
			proposed, err := engine.canvas.Document("prd.json", run.Proposal)
			if err != nil || proposed["accepted"] != string(seed) || proposed["status"] != "pending" || !strings.Contains(proposed["proposed"], "pilot") && pkg.RuntimeMode == "manual" || pkg.RuntimeMode == "flow" && !strings.Contains(proposed["proposed"], "Frozen topic") {
				t.Fatal("native proposal scope/fixture changed", proposed, err)
			}
			engine.Close()
			engine.Close()
			if err := os.WriteFile(path, raw, 0644); err != nil {
				t.Fatal(err)
			}
			restart, err := prepareRuntime(alternateRoot, state, path, options, &pkg)
			if err != nil {
				t.Fatal("same owned state continuation refused", err)
			}
			reopened, err := openRuntime(state, restart)
			if err != nil {
				t.Fatal("runtime cleanup leaked native resources/lease", err)
			}
			defer reopened.Close()
			if reopened.Metadata()["providerCalls"] != int64(0) {
				t.Fatal("restart dispatched a provider")
			}
			if _, err := os.Stat(filepath.Join(state, "suite-cache")); !os.IsNotExist(err) {
				t.Fatal("frozen runtime created suite-cache", err)
			}
		})
	}
}
