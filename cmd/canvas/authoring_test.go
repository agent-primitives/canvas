package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/canvas"
)

type cliResult struct {
	SchemaVersion string                                          `json:"schemaVersion"`
	Operation     string                                          `json:"operation"`
	OK            bool                                            `json:"ok"`
	Status        string                                          `json:"status"`
	Files         []string                                        `json:"files"`
	Diagnostics   []struct{ Code, Path, Pointer, Message string } `json:"diagnostics"`
}

func TestAuthoringCLIInitializeThenValidate(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, template := range []string{"single-review", "writer-review"} {
		t.Run(template, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "my-review")
			for _, args := range [][]string{
				{"init", dest, "--fixture", "--root", root, "--template", template, "--json"},
				{"validate", "canvas.yaml", "--root", dest, "--json"},
				{"validate", "--json", filepath.Join(dest, "canvas.yaml")},
			} {
				var output bytes.Buffer
				if err := serve(context.Background(), args, &output); err != nil {
					t.Fatal(err)
				}
				dec := json.NewDecoder(&output)
				var result cliResult
				if err := dec.Decode(&result); err != nil {
					t.Fatal(err)
				}
				var extra any
				if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
					t.Fatal("stdout isn't one JSON document")
				}
				if result.SchemaVersion != "canvas.authoring/v1" || !result.OK || result.Operation != args[0] || result.Files == nil || result.Diagnostics == nil {
					t.Fatalf("wrong JSON contract: %+v", result)
				}
				want := "valid-offline"
				if args[0] == "init" {
					want = "created"
				}
				if result.Status != want || len(result.Diagnostics) != 0 {
					t.Fatalf("wrong success: %+v", result)
				}
			}
			if _, err := os.Stat(filepath.Join(dest, ".state")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("authoring started state")
			}
		})
	}
}

func TestAuthoringCLIValidatesFlowsWithoutActivating(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	dest := filepath.Join(t.TempDir(), "flow-draft")
	if err := serve(context.Background(), []string{"init", dest, "--fixture", "--root", root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dest, "canvas.yaml")
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	definition := strings.Replace(string(raw), "canvas.local/v1alpha2", "canvas.local/v1alpha3", 1)
	definition = strings.Replace(definition, "spec:\n", `spec:
  reviewRoles: [reviewer]
  flows:
    draft:
      inputs: {schema: schemas/document.json}
      limits: {agentRuns: 2}
      steps:
        - {id: draft, agent: writer, gate: {accept: [draft], role: reviewer}}
`, 1)
	if err := os.WriteFile(filename, []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := serve(context.Background(), []string{"validate", filename, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var result cliResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.OK || result.Status != "valid-offline" {
		t.Fatalf("flow validation failed: %s, %v", &output, err)
	}
	state := filepath.Join(t.TempDir(), "not-created")
	if err := serve(context.Background(), []string{"--validate", "--definition", filename, "--state", state}, io.Discard); err != nil {
		t.Fatal("flag-only validation refused flow", err)
	}
	// Even an explicit fixture choice cannot silently run past declared gates.
	err = serve(context.Background(), []string{"--fixture", "--root", root, "--definition", filename, "--state", state}, io.Discard)
	if !errors.Is(err, canvas.ErrFlowRuntime) {
		t.Fatal("flow activation must fail with specific unsupported-runtime error", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("flow refusal created runtime state", err)
	}
	if err := os.WriteFile(filename, []byte(strings.Replace(definition, "agentRuns: 2", "agentRuns: 0", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err = serve(context.Background(), []string{"validate", filename, "--json"}, &output)
	if commandExitCode(err) != 1 || json.Unmarshal(output.Bytes(), &result) != nil || result.OK || result.Status != "invalid" {
		t.Fatalf("invalid flow passed CLI validation: %v %s", err, &output)
	}
}

func TestAuthoringCLIUsageAndHelpAreMachineReadable(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"init", "--json"}, "usage-error", 2},
		{[]string{"init", "example", "--json"}, "usage-error", 2},
		{[]string{"validate", "--unknown", "--json"}, "usage-error", 2},
		{[]string{"validate", "example.yaml", "--name", "--json"}, "usage-error", 2},
		{[]string{"validate", "--json"}, "usage-error", 2},
		{[]string{"validate", "example.yaml", "extra", "--json"}, "usage-error", 2},
		{[]string{"init", "example", "--fixture", "--name", "credential-canary!", "--json"}, "usage-error", 2},
		{[]string{"validate", "--help", "--json"}, "help", 0},
		{[]string{"validate", "root", "--json", "--help"}, "help", 0},
		{[]string{"validate", "root", "--json", "--unknown"}, "usage-error", 2},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := serve(context.Background(), tc.args, &output)
			if commandExitCode(err) != tc.code {
				t.Fatalf("exit %d, want %d: %v", commandExitCode(err), tc.code, err)
			}
			if strings.Contains(output.String(), "credential-canary") || err != nil && strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("raw input leaked")
			}
			var result cliResult
			if e := json.Unmarshal(output.Bytes(), &result); e != nil || result.Status != tc.want {
				t.Fatalf("wrong failure/help JSON: %s %v", &output, e)
			}
			if tc.code == 2 {
				wantCode := "usage"
				if len(tc.args) == 3 && tc.args[0] == "init" {
					wantCode = "fixture-required"
				}
				if strings.Contains(strings.Join(tc.args, " "), "credential-canary") {
					wantCode = "template-options"
				}
				if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != wantCode {
					t.Fatalf("wrong diagnostic: %+v", result.Diagnostics)
				}
			}
		})
	}
	if commandExitCode(flag.ErrHelp) != 0 || commandExitCode(errors.New("legacy parse error")) != 1 {
		t.Fatal("legacy exit contract changed")
	}
	if err := serve(context.Background(), []string{"extra"}, io.Discard); err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatal("legacy positional arguments no longer refuse")
	}
}

func TestAuthoringCLIValidateStrictOfflineRefusals(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	for _, tc := range []struct{ name, file, old, replacement string }{
		{"unknown field", "canvas.yaml", "kind: Canvas", "kind: Canvas\nunknown: credential-canary"},
		{"bad scope", "canvas.yaml", "propose: [draft]", "propose: [missing]"},
		{"missing layout", "canvas.yaml", "layout: layouts/writer", "layout: layouts/missing"},
		{"bad schema", "schemas/document.json", `"type":"object"`, `"type":"not-a-type"`},
		{"external schema", "schemas/document.json", `"type":"object"`, `"$ref":"https://example.invalid/credential-canary"`},
		{"bad seed", "seeds/draft.json", `"text":"Describe the document you want to review."`, `"text":42`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "my-review")
			if err := serve(context.Background(), []string{"init", dest, "--fixture", "--root", root}, io.Discard); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dest, filepath.FromSlash(tc.file))
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tc.old) {
				t.Fatal("bad test mutation")
			}
			if err := os.WriteFile(p, []byte(strings.Replace(string(raw), tc.old, tc.replacement, 1)), 0644); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = serve(context.Background(), []string{"validate", filepath.Join(dest, "canvas.yaml"), "--json"}, &output)
			var result cliResult
			if commandExitCode(err) != 1 || json.Unmarshal(output.Bytes(), &result) != nil || result.OK || result.Status != "invalid" || len(result.Diagnostics) != 1 {
				t.Fatalf("strict refusal: %v %s", err, &output)
			}
			if result.Diagnostics[0].Code != "invalid-definition" {
				t.Fatalf("wrong definition refusal: %+v", result.Diagnostics)
			}
			if strings.Contains(output.String(), "credential-canary") || strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("validation echoed content")
			}
		})
	}
}

func TestAuthoringCLIJSONRespectsValuesAndTerminator(t *testing.T) {
	for _, tc := range []struct {
		args []string
		json bool
	}{
		{[]string{"validate", "--json", "--", "--json=false"}, true},
		{[]string{"validate", "--", "--json"}, false},
		{[]string{"validate", "--root", "--json", "/nonexistent/canvas.yaml"}, false},
	} {
		var output bytes.Buffer
		err := serve(context.Background(), tc.args, &output)
		if commandExitCode(err) != 1 {
			t.Fatalf("expected invalid definition, got %v", err)
		}
		if tc.json {
			var result cliResult
			if json.Unmarshal(output.Bytes(), &result) != nil || result.Status != "invalid" {
				t.Fatalf("operand overrode JSON option: %s", &output)
			}
		} else if output.Len() != 0 {
			t.Fatalf("value/operand became an option: %s", &output)
		}
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAuthoringCLIReportsOutputFailure(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	err := serve(context.Background(), []string{"validate", "examples/northwind-released/canvas-starter.yaml", "--root", root, "--json"}, brokenOutput{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure swallowed: %v", err)
	}
}
