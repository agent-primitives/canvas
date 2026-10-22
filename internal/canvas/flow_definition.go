package canvas

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/agent-primitives/suite/core"
	"github.com/google/jsonschema-go/jsonschema"
	"go.yaml.in/yaml/v3"
)

var ErrFlowRuntime = errors.New("declared flows are compile-only; flow execution is not supported")

// RequireManualRuntime keeps validated flow syntax out of runtimes that cannot
// enforce its gates. Validation itself does not call this activation guard.
func (d *CompiledDefinition) RequireManualRuntime() error {
	if len(d.flows) != 0 {
		return ErrFlowRuntime
	}
	return nil
}

// FlowDefinition describes ordered human-gated work, not a running workflow.
type FlowDefinition struct {
	Inputs FlowInputs `yaml:"inputs" json:"inputs"`
	Limits FlowLimits `yaml:"limits" json:"limits"`
	Steps  []FlowStep `yaml:"steps" json:"steps"`
}

type FlowInputs struct {
	Schema string `yaml:"schema" json:"schema"`
}

type FlowLimits struct {
	AgentRuns int `yaml:"agentRuns" json:"agentRuns"`
}

type FlowStep struct {
	ID     string    `yaml:"id" json:"id"`
	Agent  string    `yaml:"agent,omitempty" json:"agent,omitempty"`
	Gate   *FlowGate `yaml:"gate,omitempty" json:"gate,omitempty"`
	Action string    `yaml:"action,omitempty" json:"action,omitempty"`
}

type FlowGate struct {
	Accept []string `yaml:"accept" json:"accept"`
	Role   string   `yaml:"role" json:"role"`
}

type compiledFlow struct {
	definition FlowDefinition
	inputs     *jsonschema.Resolved
}

func cloneFlow(flow FlowDefinition) FlowDefinition {
	flow.Steps = slices.Clone(flow.Steps)
	for i := range flow.Steps {
		if gate := flow.Steps[i].Gate; gate != nil {
			flow.Steps[i].Gate = &FlowGate{Accept: slices.Clone(gate.Accept), Role: gate.Role}
		}
	}
	return flow
}

// Flow returns an independent view of validated controls, not mutable authority.
func (d *CompiledDefinition) Flow(id string) (FlowDefinition, bool) {
	flow, ok := d.flows[id]
	return cloneFlow(flow.definition), ok
}

func (d *CompiledDefinition) EditAgent(document string) (string, bool) {
	doc, ok := d.documents[document]
	return doc.editAgent, ok && doc.editAgent != ""
}

// ValidateFlowInputs checks the loaded schema snapshot without applying defaults,
// rereading source files, contacting a provider or activating a flow.
func (d *CompiledDefinition) ValidateFlowInputs(id string, raw []byte) error {
	flow, ok := d.flows[id]
	if !ok || len(raw) > 65536 {
		return errDefinition
	}
	var value any
	// Preserve strict JSON checks, then give schema validation native numbers.
	if core.Unmarshal(raw, &value) != nil || !preservesJSONNumbers(value) || json.Unmarshal(raw, &value) != nil {
		return errDefinition
	}
	if _, ok := value.(map[string]any); !ok || flow.inputs.Validate(value) != nil {
		return errDefinition
	}
	return nil
}

// jsonschema-go validates native float64 values, not json.Number. Refuse any
// decimal value that changes when converted, including fractions rounded into
// integers and nonzero values underflowing to zero.
func preservesJSONNumbers(value any) bool {
	switch v := value.(type) {
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return false
		}
		digits, exponent, ok := decimalParts(string(v))
		converted, convertedExponent, convertedOK := decimalParts(strconv.FormatFloat(number, 'g', -1, 64))
		return ok && convertedOK && digits == converted && exponent == convertedExponent
	case map[string]any:
		for _, child := range v {
			if !preservesJSONNumbers(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !preservesJSONNumbers(child) {
				return false
			}
		}
	}
	return true
}

// Normalize decimal spelling without allocating powers of attacker-selected
// exponents. Callers supply strict JSON numbers bounded by the 64 KiB input.
func decimalParts(number string) (string, int64, bool) {
	mantissa, exponent := number, ""
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		mantissa, exponent = number[:i], number[i+1:]
	}
	offset := int64(0)
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		offset = -int64(len(mantissa) - i - 1)
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	negative := strings.HasPrefix(mantissa, "-")
	digits := strings.TrimLeft(strings.TrimPrefix(mantissa, "-"), "0")
	if digits == "" {
		return "0", 0, true
	}
	trimmed := strings.TrimRight(digits, "0")
	offset += int64(len(digits) - len(trimmed))
	if negative {
		trimmed = "-" + trimmed
	}
	if exponent != "" {
		// Larger exponents cannot yield a finite nonzero float64 within the
		// bounded mantissa, and restricting them keeps offset arithmetic safe.
		power, err := strconv.ParseInt(exponent, 10, 32)
		if err != nil {
			return "", 0, false
		}
		offset += power
	}
	return trimmed, offset, true
}

func (d *CompiledDefinition) compileFlows(roles []string, flows map[string]FlowDefinition, loadSchema func(string) (*jsonschema.Resolved, error)) error {
	if len(roles) > 32 || len(flows) > 128 {
		return errDefinition
	}
	declaredRoles := map[string]bool{}
	for _, role := range roles {
		if !definitionName.MatchString(role) || declaredRoles[role] {
			return errDefinition
		}
		declaredRoles[role] = true
	}
	for id, doc := range d.Documents {
		if doc.EditAgent == "" {
			continue
		}
		agent, ok := d.Agents[doc.EditAgent]
		if !ok || d.documents[id].input || !slices.Contains(agent.Read, id) || !d.proposals[doc.EditAgent][id] {
			return errDefinition
		}
		compiled := d.documents[id]
		compiled.editAgent = doc.EditAgent
		d.documents[id] = compiled
	}
	d.ReviewRoles = slices.Clone(roles)
	d.Flows = map[string]FlowDefinition{}
	d.flows = map[string]compiledFlow{}
	for id, flow := range flows {
		if !definitionName.MatchString(id) || flow.Limits.AgentRuns <= 0 || len(flow.Steps) == 0 || len(flow.Steps) > 32 {
			return errDefinition
		}
		inputs, err := loadSchema(flow.Inputs.Schema)
		if err != nil || inputs.Schema().Type != "object" {
			return errDefinition
		}
		steps, accepted := map[string]bool{}, map[string]bool{}
		agentSteps := 0
		for i, step := range flow.Steps {
			if !definitionName.MatchString(step.ID) || steps[step.ID] || (step.Agent == "") == (step.Action == "") {
				return errDefinition
			}
			steps[step.ID] = true
			if step.Action != "" {
				action, ok := d.actions[step.Action]
				if !ok || i != len(flow.Steps)-1 || step.Gate != nil || !accepted[action.Document] {
					return errDefinition
				}
				continue
			}
			if _, ok := d.Agents[step.Agent]; !ok || step.Gate == nil || !declaredRoles[step.Gate.Role] || len(step.Gate.Accept) == 0 || len(step.Gate.Accept) > maxProposalChanges {
				return errDefinition
			}
			seen := map[string]bool{}
			for _, document := range step.Gate.Accept {
				if !d.proposals[step.Agent][document] || seen[document] {
					return errDefinition
				}
				seen[document], accepted[document] = true, true
			}
			agentSteps++
		}
		if agentSteps == 0 {
			return errDefinition
		}
		d.Flows[id] = cloneFlow(flow)
		d.flows[id] = compiledFlow{definition: cloneFlow(flow), inputs: inputs}
	}
	return nil
}

// yaml's integer decoder otherwise truncates fractional scalars into int.
func integerFlowBudgets(node *yaml.Node) bool {
	flows := yamlField(yamlField(node, "spec"), "flows")
	if flows != nil {
		for i := 1; i < len(flows.Content); i += 2 {
			budget := yamlField(yamlField(flows.Content[i], "limits"), "agentRuns")
			if budget != nil && budget.Tag != "!!int" {
				return false
			}
		}
	}
	return true
}

// Version gating checks key presence, including empty maps/lists/strings that
// would otherwise disappear in zero-valued decoded structs.
func hasFlowSyntax(node *yaml.Node) bool {
	spec := yamlField(node, "spec")
	if yamlField(spec, "flows") != nil || yamlField(spec, "reviewRoles") != nil {
		return true
	}
	documents := yamlField(spec, "documents")
	if documents != nil {
		for i := 1; i < len(documents.Content); i += 2 {
			if yamlField(documents.Content[i], "editAgent") != nil {
				return true
			}
		}
	}
	return false
}

func yamlField(node *yaml.Node, name string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == name {
				return node.Content[i+1]
			}
		}
	}
	return nil
}
