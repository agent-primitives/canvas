package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flowDeclarations = `  reviewRoles: [reviewer]
  flows:
    release:
      inputs: {schema: schemas/run.json}
      limits: {agentRuns: 12}
      steps:
        - {id: draft, agent: writer, gate: {accept: [prd, announcement], role: reviewer}}
        - {id: critique, agent: reviewer, gate: {accept: [review], role: reviewer}}
        - {id: revise, agent: writer, gate: {accept: [prd, announcement], role: reviewer}}
        - {id: announce, action: announce}
`

const flowInputSchema = `{"type":"object","properties":{"topic":{"type":"string","minLength":1,"maxLength":256},"audience":{"type":"string","enum":["engineer","manager"],"default":"engineer"},"urgent":{"type":"boolean","default":false},"count":{"type":"integer","minimum":1}},"required":["topic"],"additionalProperties":false}`

func flowFixture(t *testing.T) string {
	t.Helper()
	filename := genericFixture(t)
	genericConfigEdit(t, filename, "canvas.local/v1alpha2", "canvas.local/v1alpha3")
	genericConfigEdit(t, filename, "spec:\n", "spec:\n"+flowDeclarations)
	genericConfigEdit(t, filename, "path: prd.json\n", "path: prd.json\n      editAgent: writer\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "schemas", "run.json"), []byte(flowInputSchema), 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestFlowDefinitionCompiles(t *testing.T) {
	filename := flowFixture(t)
	d, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatalf("valid ordered flow refused: %v", err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		APIVersion  string
		ReviewRoles []string
		Flows       map[string]struct {
			Inputs struct{ Schema string }
			Limits struct{ AgentRuns int }
			Steps  []struct {
				ID, Agent, Action string
				Gate              *struct {
					Accept []string
					Role   string
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	f := view.Flows["release"]
	if view.APIVersion != "canvas.local/v1alpha3" || len(view.ReviewRoles) != 1 || view.ReviewRoles[0] != "reviewer" || f.Inputs.Schema != "schemas/run.json" || f.Limits.AgentRuns != 12 || len(f.Steps) != 4 {
		t.Fatalf("compiled flow controls missing: %s", raw)
	}
	if f.Steps[0].ID != "draft" || f.Steps[0].Agent != "writer" || f.Steps[0].Gate == nil || len(f.Steps[0].Gate.Accept) != 2 || f.Steps[0].Gate.Role != "reviewer" || f.Steps[3].Action != "announce" {
		t.Fatalf("order or gates lost: %+v", f.Steps)
	}
	if len(d.Triggers) != 3 || d.Triggers[0].Type != "manual" || d.Triggers[2].Type != "file-added" {
		t.Fatal("existing manual/event triggers changed")
	}
}

func TestFlowOnlyAndManualVersions(t *testing.T) {
	for _, version := range []string{"canvas.local/v1alpha2", "canvas.local/v1alpha3"} {
		t.Run(version, func(t *testing.T) {
			filename := genericFixture(t)
			genericConfigEdit(t, filename, "canvas.local/v1alpha2", version)
			if _, err := LoadGenericDefinition(filename); err != nil {
				t.Fatalf("manual definition refused: %v", err)
			}
			genericConfigEdit(t, filename, "type: manual", "type: file-added\n      path: feedback/*.json")
			// The second manual trigger is still a valid entry point.
			genericConfigEdit(t, filename, "type: manual", "type: file-added\n      path: feedback/*.json")
			if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
				t.Fatalf("no manual trigger or flow must refuse: %v", err)
			}
		})
	}
	filename := flowFixture(t)
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	start, end := strings.Index(string(raw), "  triggers:\n"), strings.Index(string(raw), "  people:\n")
	if start < 0 || end <= start {
		t.Fatal("missing trigger block")
	}
	if err := os.WriteFile(filename, append(raw[:start], raw[end:]...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGenericDefinition(filename); err != nil {
		t.Fatalf("flow-only definition refused: %v", err)
	}
}

func TestFlowDeclarationsRefuseInvalidControls(t *testing.T) {
	cases := []struct{ name, old, replacement string }{
		{"missing input schema", "inputs: {schema: schemas/run.json}", "inputs: {}"},
		{"zero budget", "agentRuns: 12", "agentRuns: 0"},
		{"negative budget", "agentRuns: 12", "agentRuns: -1"},
		{"fraction budget", "agentRuns: 12", "agentRuns: 1.5"},
		{"string budget", "agentRuns: 12", "agentRuns: '12'"},
		{"missing budget", "limits: {agentRuns: 12}", "limits: {}"},
		{"unknown flow field", "limits: {agentRuns: 12}", "limits: {agentRuns: 12}\n      autoApprove: true"},
		{"unknown step field", "id: draft, agent: writer", "id: draft, next: revise, agent: writer"},
		{"missing gate", ", gate: {accept: [prd, announcement], role: reviewer}", ""},
		{"no gate bypass", "gate: {accept: [prd, announcement], role: reviewer}", "gate: {accept: [prd, announcement], role: reviewer, optional: true}"},
		{"empty accept", "accept: [prd, announcement]", "accept: []"},
		{"duplicate accept", "accept: [prd, announcement]", "accept: [prd, prd]"},
		{"unknown document", "accept: [prd, announcement]", "accept: [missing]"},
		{"wrong propose scope", "accept: [prd, announcement]", "accept: [review]"},
		{"input gate", "accept: [prd, announcement]", "accept: [feedback]"},
		{"unknown agent", "id: draft, agent: writer", "id: draft, agent: missing"},
		{"unknown role", "role: reviewer", "role: missing"},
		{"duplicate roles", "reviewRoles: [reviewer]", "reviewRoles: [reviewer, reviewer]"},
		{"invalid role", "reviewRoles: [reviewer]", "reviewRoles: [bad/role]"},
		{"missing roles", "reviewRoles: [reviewer]", "reviewRoles: []"},
		{"duplicate steps", "id: critique", "id: draft"},
		{"invalid step", "id: draft", "id: ../draft"},
		{"two step kinds", "id: draft, agent: writer", "id: draft, action: announce, agent: writer"},
		{"no step kind", "id: draft, agent: writer", "id: draft"},
		{"unknown action", "id: announce, action: announce", "id: announce, action: missing"},
		{"action gate", "id: announce, action: announce", "id: announce, action: announce, gate: {accept: [prd], role: reviewer}"},
		{"action before last", "id: critique, agent: reviewer, gate: {accept: [review], role: reviewer}", "id: critique, action: announce"},
		{"unreviewed action document", "accept: [prd, announcement]", "accept: [prd]"},
		{"unknown edit agent", "editAgent: writer", "editAgent: missing"},
		{"unwritable edit agent", "editAgent: writer", "editAgent: reviewer"},
		{"unreadable edit agent", "read: [brief, feedback, prd]", "read: [brief, feedback]"},
		{"input edit binding", "path: feedback/*.json", "editAgent: writer\n      path: feedback/*.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filename := flowFixture(t)
			if tc.name == "unreviewed action document" {
				// Remove it from both writer gates, not only the first.
				genericConfigEdit(t, filename, tc.old, tc.replacement)
			}
			genericConfigEdit(t, filename, tc.old, tc.replacement)
			if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
				t.Fatalf("invalid flow must return bounded definition refusal: %v", err)
			}
		})
	}
}

func TestFlowStepBound(t *testing.T) {
	for _, count := range []int{0, 32, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			filename := flowFixture(t)
			var steps strings.Builder
			for i := range count {
				fmt.Fprintf(&steps, "        - {id: step-%d, agent: writer, gate: {accept: [prd], role: reviewer}}\n", i)
			}
			old := strings.SplitN(flowDeclarations, "      steps:\n", 2)[1]
			replacement := steps.String()
			if count == 0 {
				genericConfigEdit(t, filename, "      steps:\n"+old, "      steps: []\n")
			} else {
				genericConfigEdit(t, filename, old, replacement)
			}
			_, err := LoadGenericDefinition(filename)
			if count == 32 && err != nil || count != 32 && !errors.Is(err, errDefinition) {
				t.Fatalf("%d steps: %v", count, err)
			}
		})
	}
}

func TestFlowGateFitsOneProposal(t *testing.T) {
	for _, count := range []int{32, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			filename := flowFixture(t)
			var documents, seeds strings.Builder
			ids := make([]string, count)
			for i := range count {
				ids[i] = fmt.Sprintf("extra-%d", i)
				fmt.Fprintf(&documents, "    %s: {path: %s.md}\n", ids[i], ids[i])
				fmt.Fprintf(&seeds, "    %s.md: seeds/announcement.md\n", ids[i])
			}
			genericConfigEdit(t, filename, "  documents:\n", "  documents:\n"+documents.String())
			genericConfigEdit(t, filename, "  seed:\n", "  seed:\n"+seeds.String())
			genericConfigEdit(t, filename, "propose: [prd, announcement]", "propose: [prd, announcement, "+strings.Join(ids, ", ")+"]")
			genericConfigEdit(t, filename, "accept: [prd, announcement]", "accept: ["+strings.Join(ids, ", ")+"]")
			_, err := LoadGenericDefinition(filename)
			if count == 32 && err != nil || count == 33 && !errors.Is(err, errDefinition) {
				t.Fatalf("gate requiring %d replacements: %v", count, err)
			}
		})
	}
}

func TestFlowFieldsRequireV1alpha3EvenWhenEmpty(t *testing.T) {
	for _, field := range []string{"  flows: {}\n", "  reviewRoles: []\n", "      editAgent: ''\n"} {
		t.Run(strings.TrimSpace(field), func(t *testing.T) {
			filename := genericFixture(t)
			old, replacement := "spec:\n", "spec:\n"+field
			if strings.Contains(field, "editAgent") {
				old, replacement = "path: prd.json\n", "path: prd.json\n"+field
			}
			genericConfigEdit(t, filename, old, replacement)
			if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
				t.Fatalf("v1alpha2 accepted new syntax: %v", err)
			}
		})
	}
}

func TestFlowCompiledControlsIgnorePresentationMutation(t *testing.T) {
	d, err := LoadGenericDefinition(flowFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	flow, ok := d.Flow("release")
	if !ok {
		t.Fatal("compiled flow missing")
	}
	flow.Steps[0].Gate.Accept[0], flow.Steps[0].Agent = "review", "reviewer"
	public := d.Flows["release"]
	public.Steps[0].Gate.Role, public.Steps[0].Gate.Accept[0] = "missing", "review"
	delete(d.Flows, "release")
	d.ReviewRoles[0] = "missing"
	d.Documents["prd"] = DocumentDefinition{Path: "prd.json", EditAgent: "reviewer"}
	fresh, ok := d.Flow("release")
	if !ok || fresh.Steps[0].Agent != "writer" || fresh.Steps[0].Gate.Role != "reviewer" || fresh.Steps[0].Gate.Accept[0] != "prd" {
		t.Fatal("presentation mutation changed compiled flow")
	}
	if id, ok := d.EditAgent("prd"); !ok || id != "writer" {
		t.Fatal("presentation mutation changed edit binding")
	}
	if _, ok := d.Flow("missing"); ok {
		t.Fatal("unknown flow exists")
	}
	if _, ok := d.EditAgent("missing"); ok {
		t.Fatal("unknown edit binding exists")
	}
	guard, ok := any(d).(interface{ RequireManualRuntime() error })
	if !ok {
		t.Fatal("flow definitions have no pre-start runtime guard")
	}
	if guard.RequireManualRuntime() == nil {
		t.Fatal("mutable presentation data bypassed flow runtime refusal")
	}
	// This must refuse before touching either injected dependency or state.
	state := filepath.Join(t.TempDir(), "not-created")
	if _, err := OpenDefined(state, nil, nil, "", d, nil); !errors.Is(err, ErrFlowRuntime) {
		t.Fatal("library did not specifically refuse unsupported flow runtime", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime refusal touched state", err)
	}
}

func TestFlowInputsUseStrictFrozenSchema(t *testing.T) {
	filename := flowFixture(t)
	d, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	validator, ok := any(d).(interface {
		ValidateFlowInputs(string, []byte) error
	})
	if !ok {
		t.Fatal("compiled flow has no input validator")
	}
	for _, tc := range []struct {
		name, flow, input string
		valid             bool
	}{
		{"valid", "release", `{"topic":"Upgrade","count":2}`, true},
		{"enum and boolean", "release", `{"topic":"Upgrade","audience":"manager","urgent":true}`, true},
		{"required", "release", `{}`, false},
		{"empty", "release", `{"topic":""}`, false},
		{"length", "release", `{"topic":"` + strings.Repeat("x", 257) + `"}`, false},
		{"enum", "release", `{"topic":"Upgrade","audience":"unknown"}`, false},
		{"number", "release", `{"topic":"Upgrade","count":0}`, false},
		{"duplicate key", "release", `{"topic":"A","topic":"B"}`, false},
		{"unknown property", "release", `{"topic":"Upgrade","unexpected":true}`, false},
		{"null", "release", `null`, false},
		{"array", "release", `[]`, false},
		{"trailing JSON", "release", `{"topic":"Upgrade"}{}`, false},
		{"invalid UTF-8", "release", "{\"topic\":\"\xff\"}", false},
		{"unknown flow", "missing", `{"topic":"Upgrade"}`, false},
		{"oversize", "release", `{"topic":"` + strings.Repeat("x", 65536) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validator.ValidateFlowInputs(tc.flow, []byte(tc.input))
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, errDefinition) {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	// Changing source bytes after load cannot relax the already-compiled schema.
	path := filepath.Join(filepath.Dir(filename), "schemas", "run.json")
	if err := os.WriteFile(path, []byte(`{"type":"object"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validator.ValidateFlowInputs("release", []byte(`{}`)); !errors.Is(err, errDefinition) {
		t.Fatal("source mutation changed compiled input policy", err)
	}
	changed, err := LoadGenericDefinition(filename)
	if err != nil || changed.Digest == d.Digest {
		t.Fatal("input schema bytes are not bound to definition digest", err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"object"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	whitespace, err := LoadGenericDefinition(filename)
	if err != nil || whitespace.Digest == changed.Digest {
		t.Fatal("exact input schema bytes not bound", err)
	}
	reloaded, err := LoadGenericDefinition(filename)
	if err != nil || reloaded.Digest != whitespace.Digest {
		t.Fatal("same input schema has nondeterministic digest", err)
	}
}

func TestFlowInputsRejectPrecisionLosingNumbers(t *testing.T) {
	filename := flowFixture(t)
	schema := strings.Replace(flowInputSchema, `"minimum":1`, `"minimum":1,"maximum":9007199254740992`, 1)
	schema = strings.Replace(schema, `"properties":{`, `"properties":{"samples":{"type":"array","items":{"type":"number","maximum":1}},`, 1)
	if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "schemas", "run.json"), []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fields string
		valid  bool
	}{
		{`"count":2`, true},
		{`"count":2.0`, true},
		{`"count":2e0`, true},
		{`"count":9007199254740992`, true},
		{`"count":2.0000000000000001`, false},
		{`"count":0.99999999999999999`, false},
		{`"count":9007199254740993`, false},
		{`"samples":[0.1,1.0,1e-3,0,-0,0e999999999]`, true},
		{`"samples":[1.0000000000000001]`, false},
		{`"samples":[1e-999999999]`, false},
	} {
		t.Run(tc.fields, func(t *testing.T) {
			err := d.ValidateFlowInputs("release", []byte(`{"topic":"Upgrade",`+tc.fields+`}`))
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, errDefinition) {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestFlowInputSchemasRejectPrecisionLosingConstraints(t *testing.T) {
	for _, field := range []string{
		`"minimum":9007199254740993`,
		`"maximum":1.0000000000000001`,
		`"enum":[9007199254740993]`,
		`"default":2.0000000000000001`,
	} {
		t.Run(field, func(t *testing.T) {
			filename := flowFixture(t)
			schema := `{"type":"object","properties":{"count":{"type":"number",` + field + `}}}`
			if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "schemas", "run.json"), []byte(schema), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
				t.Fatal("precision-losing input schema constraint accepted", err)
			}
		})
	}
}

func TestFlowInputSchemasRefuseUnsafeSources(t *testing.T) {
	for _, schema := range []string{
		`{"type":"string"}`, `true`, `{"type":"object","$ref":"https://example.invalid/private"}`,
		`{"type":"object","$ref":"file:///tmp/private"}`, `{"type":"object","$ref":"../other.json"}`,
		`{"type":"object","properties":{"topic":{"type":"string","default":42}}}`,
		`{"type":"object","type":"string"}`, `{"type":"not-a-type"}`,
	} {
		t.Run(schema, func(t *testing.T) {
			filename := flowFixture(t)
			if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "schemas", "run.json"), []byte(schema), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
				t.Fatal("unsafe input schema not refused", err)
			}
		})
	}
	for _, source := range []string{"../run.json", "/tmp/run.json", ".private/run.json", "schemas/missing.json"} {
		t.Run(source, func(t *testing.T) {
			filename := flowFixture(t)
			genericConfigEdit(t, filename, "schema: schemas/run.json", "schema: "+source)
			if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
				t.Fatal("unsafe input schema source not refused", err)
			}
		})
	}
	filename := flowFixture(t)
	schema := filepath.Join(filepath.Dir(filename), "schemas", "run.json")
	if err := os.Rename(schema, schema+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(schema+"-real", schema); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGenericDefinition(filename); !errors.Is(err, errDefinition) {
		t.Fatal("linked input schema accepted", err)
	}
}

func TestFlowCompilerPreservesV1alpha2Digest(t *testing.T) {
	for _, tc := range []struct{ filename, digest string }{
		{"northwind-prd/canvas.yaml", "7af5e7407f0f099805dceb43cadeda59b92aa4cbb5b14de2f128070e6543c223"},
		{"release-review/canvas.yaml", "65251f3150fe30e3e3d150d84c28c22b02269e3d0737b5afff8cbacf1c162f2b"},
	} {
		d, err := LoadGenericDefinition(filepath.Join("..", "..", "examples", tc.filename))
		if err != nil || d.Digest != tc.digest {
			t.Fatalf("existing state digest changed for %s: %v", tc.filename, err)
		}
	}
}
