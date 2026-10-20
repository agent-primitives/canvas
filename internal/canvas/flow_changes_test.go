package canvas

import (
	"errors"
	"fmt"
	"testing"
)

var _ interface {
	ChangesForStep(string, string, string) (map[string]string, error)
} = (*CompiledDefinition)(nil)

func completeWriterOutput() string {
	return fmt.Sprintf(`{"changes":[{"document":"prd","operation":"replace","content":%q},{"document":"announcement","operation":"replace","content":"# Draft announcement\n"}]}`, validPRD)
}

func TestFlowStepRequiresEveryGateOutput(t *testing.T) {
	d, err := LoadGenericDefinition(flowFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	v := d
	for _, output := range []string{changeEnvelope("prd", validPRD), changeEnvelope("announcement", "# Draft\n")} {
		if changes, err := v.ChangesForStep("release", "draft", output); !errors.Is(err, ErrMissingGateOutputs) || changes != nil {
			t.Fatalf("partial gate accepted: %v %v", changes, err)
		}
	}
	changes, err := v.ChangesForStep("release", "draft", completeWriterOutput())
	if err != nil || len(changes) != 2 || changes["prd.json"] != validPRD || changes["announcement.md"] != "# Draft announcement\n" {
		t.Fatalf("whole gate refused or paths not compiled: %v %v", changes, err)
	}
	for _, output := range []string{changeEnvelope("prd", "{}"), changeEnvelope("brief", "{}"), `{"changes":[]}`} {
		if changes, err := v.ChangesForStep("release", "draft", output); err == nil || changes != nil {
			t.Fatal("step weakened schema, scope or envelope validation", changes, err)
		}
	}
	if changes, err := d.Changes("writer", changeEnvelope("prd", validPRD)); err != nil || len(changes) != 1 {
		t.Fatal("manual subset behavior changed", changes, err)
	}
}

func TestFlowStepAllowsEveryExtraScopedReplacement(t *testing.T) {
	filename := flowFixture(t)
	genericConfigEdit(t, filename,
		"id: draft, agent: writer, gate: {accept: [prd, announcement]",
		"id: draft, agent: writer, gate: {accept: [prd]")
	d, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := d.ChangesForStep("release", "draft", completeWriterOutput())
	if err != nil || len(changes) != 2 || changes["announcement.md"] == "" {
		t.Fatal("extra scoped replacement must remain in the whole proposal", changes, err)
	}
}

func TestFlowStepUsesFrozenBindingsAndRefusesUnknownOrActionSteps(t *testing.T) {
	d, err := LoadGenericDefinition(flowFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	d.Flows["release"].Steps[0].Gate.Accept = nil
	d.Flows["release"].Steps[0].Agent = "reviewer"
	copy, _ := d.Flow("release")
	copy.Steps[0].Gate.Accept = nil
	v := d
	if changes, err := v.ChangesForStep("release", "draft", changeEnvelope("prd", validPRD)); err == nil || changes != nil {
		t.Fatal("presentation mutation removed required outputs", changes, err)
	}
	if changes, err := v.ChangesForStep("release", "draft", completeWriterOutput()); err != nil || len(changes) != 2 {
		t.Fatal("presentation mutation changed bound agent", changes, err)
	}
	for _, refs := range [][2]string{{"unknown", "draft"}, {"release", "unknown"}, {"release", "announce"}} {
		if changes, err := v.ChangesForStep(refs[0], refs[1], completeWriterOutput()); err == nil || changes != nil {
			t.Fatal("unknown or non-agent step accepted", refs, changes, err)
		}
	}
	legacy, err := LoadGenericDefinition(genericFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if changes, err := legacy.ChangesForStep("release", "draft", completeWriterOutput()); err == nil || changes != nil {
		t.Fatal("legacy definition acquired a flow", changes, err)
	}
	var absent *CompiledDefinition
	if changes, err := absent.ChangesForStep("release", "draft", completeWriterOutput()); !errors.Is(err, errDefinition) || changes != nil {
		t.Fatal("nil definition acquired gate outputs", changes, err)
	}
	if !errors.Is(d.RequireManualRuntime(), ErrFlowRuntime) {
		t.Fatal("output validation activated flow runtime")
	}
}
