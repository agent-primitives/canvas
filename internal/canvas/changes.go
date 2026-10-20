package canvas

import (
	"errors"
	"unicode/utf8"

	agentsuite "github.com/agent-primitives/suite"
)

const maxProposalChanges = 32

// ErrMissingGateOutputs means a flow proposal omitted a required gate document.
var ErrMissingGateOutputs = errors.New("flow proposal is missing required gate outputs")

// ChangesForStep validates a whole flow-bound proposal, without activating or
// advancing a workflow. The private compiled step supplies agent and gate scope.
func (d *CompiledDefinition) ChangesForStep(flowID, stepID, output string) (map[string]string, error) {
	if d == nil {
		return nil, errDefinition
	}
	flow, ok := d.flows[flowID]
	if !ok {
		return nil, errDefinition
	}
	for _, step := range flow.definition.Steps {
		if step.ID != stepID {
			continue
		}
		if step.Agent == "" || step.Gate == nil {
			return nil, errDefinition
		}
		changes, err := d.Changes(step.Agent, output)
		if err != nil {
			return nil, err
		}
		for _, name := range step.Gate.Accept {
			if _, present := changes[d.documents[name].path]; !present {
				return nil, ErrMissingGateOutputs
			}
		}
		return changes, nil
	}
	return nil, errDefinition
}

// Changes validates the entire set before returning any path-addressed writes.
// Paths come only from compiled declarations, never from model output.
func (d *CompiledDefinition) Changes(agent, output string) (map[string]string, error) {
	if d == nil || len(output) > 2<<20 || !utf8.ValidString(output) {
		return nil, errors.New("invalid bounded change envelope")
	}
	scope, ok := d.proposals[agent]
	if !ok {
		return nil, errors.New("unknown proposal agent")
	}
	var envelope struct {
		Changes []struct {
			Document  string  `json:"document"`
			Operation string  `json:"operation"`
			Content   *string `json:"content"`
		} `json:"changes"`
	}
	if agentsuite.Unmarshal([]byte(output), &envelope) != nil || len(envelope.Changes) == 0 || len(envelope.Changes) > maxProposalChanges {
		return nil, errors.New("invalid bounded change envelope")
	}
	result := map[string]string{}
	total := 0
	for _, change := range envelope.Changes {
		doc, ok := d.documents[change.Document]
		if !ok || !scope[change.Document] || doc.input || change.Operation != "replace" || change.Content == nil {
			return nil, errors.New("change is outside declared proposal scope")
		}
		if _, exists := result[doc.path]; exists {
			return nil, errors.New("duplicate change document")
		}
		total += len(*change.Content)
		if total > 256<<10 {
			return nil, errors.New("change set exceeds content limits")
		}
		if err := validateDocument(doc, *change.Content); err != nil {
			return nil, err
		}
		result[doc.path] = *change.Content
	}
	return result, nil
}
