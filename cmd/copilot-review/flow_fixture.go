package main

import (
	"encoding/json"
	"errors"
	"path"
	"slices"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
)

// The frozen authoring path must qualify its deterministic provider before it
// retires another instance. Keep the reviewed Northwind execution shape pinned,
// while allowing metadata, valid seeds, gates and budgets to be authored. These
// hashes are the checked-in schema bytes, not execution/evaluation evidence.
func qualifiedNorthwindFixture(d *canvas.CompiledDefinition, frozen *authoring.Package) bool {
	type document struct{ path, schema, hash string }
	documents := map[string]document{
		"brief":        {"brief.json", "schemas/brief.json", "6ad5e09d39f6718149f29ea3032cba5134cb814c32592072ff90068a6966a815"},
		"feedback":     {"feedback/*.json", "schemas/feedback.json", "93ce6c023d9c0e4a95248cbd91e3d5d766642f89bb3a43f207534990116f2a5f"},
		"prd":          {"prd.json", "schemas/prd.json", "777d20828f57d9b51c74b08a59ae8708d79b866d246c4ae49db78a6bb5c4857f"},
		"review":       {"review.json", "schemas/review.json", "02d4ff612a8c85d51a8d85a551502a8b179db16dd06722fedc30fc1542cf481b"},
		"announcement": {"announcement.md", "", ""},
	}
	files := map[string]string{}
	for _, file := range frozen.Files {
		files[file.Path] = file.SHA256
	}
	if len(d.Documents) != len(documents) || len(d.Agents) != 2 {
		return false
	}
	for id, expected := range documents {
		actual, ok := d.Documents[id]
		if !ok || actual.Path != expected.path || actual.Schema != expected.schema {
			return false
		}
		if expected.schema != "" && files[path.Join(path.Dir(frozen.Definition), expected.schema)] != expected.hash {
			return false
		}
	}
	same := func(actual, expected []string) bool {
		actual = slices.Clone(actual)
		expected = slices.Clone(expected)
		slices.Sort(actual)
		slices.Sort(expected)
		return slices.Equal(actual, expected)
	}
	writer, reviewer := d.Agents["writer"], d.Agents["reviewer"]
	if writer.Suite.Ref != "" || reviewer.Suite.Ref != "" || writer.Suite.Member != "writer" || reviewer.Suite.Member != "reviewer" || writer.Suite.Digest != "sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375" || reviewer.Suite.Digest != "sha256:101df94e7cfd582d3032850a5945570bbf813e6334b0708a751cfc7199b98513" {
		return false
	}
	if !same(writer.Propose, []string{"prd", "announcement"}) || !same(reviewer.Propose, []string{"review"}) || !same(reviewer.Read, []string{"brief", "feedback", "prd"}) {
		return false
	}
	if !same(writer.Read, []string{"brief", "feedback", "prd"}) && !same(writer.Read, []string{"brief", "feedback", "prd", "review"}) {
		return false
	}
	// Flow revisions consume accepted critique; the manual fixture does not.
	if len(d.Flows) > 0 && !slices.Contains(writer.Read, "review") {
		return false
	}
	for _, flow := range d.Flows {
		if flow.Inputs.Schema != "schemas/run.json" || files[path.Join(path.Dir(frozen.Definition), flow.Inputs.Schema)] != "9537c6049433a5cb4aa86164dd91d2829543f0ad210d4e722d41acde42a0cc33" {
			return false
		}
	}
	return true
}

// Deterministic topic-aware output for the local Northwind example, not a model
// or evaluation. Read the actual native request and accepted evidence only.
func flowFixtureChanges(d *canvas.CompiledDefinition, agent, request string, evidence map[string]string) ([]map[string]string, error) {
	var workflow struct {
		Inputs struct {
			Topic string `json:"topic"`
		} `json:"inputs"`
	}
	if json.Unmarshal([]byte(request), &workflow) != nil || workflow.Inputs.Topic == "" {
		return nil, errors.New("Northwind flow fixture requires a topic")
	}
	topic := workflow.Inputs.Topic
	encode := func(document, content string) map[string]string {
		return map[string]string{"document": document, "operation": "replace", "content": content}
	}
	switch agent {
	case "writer":
		var prd, accepted map[string]any
		if json.Unmarshal([]byte(d.Seed["prd.json"]), &prd) != nil || prd == nil || json.Unmarshal([]byte(evidence["prd.json"]), &accepted) != nil || accepted == nil {
			return nil, errors.New("Northwind fixture requires the accepted PRD")
		}
		problem, ok := prd["problem"].(string)
		if !ok {
			return nil, errors.New("Northwind fixture requires a text problem")
		}
		prd["title"] = topic + " — draft"
		if accepted["title"] == topic+" — draft" {
			var review struct {
				Summary string `json:"summary"`
			}
			if json.Unmarshal([]byte(evidence["review.json"]), &review) != nil || review.Summary == "" {
				return nil, errors.New("Northwind revision requires accepted critique")
			}
			prd["title"] = topic + " — revised"
			prd["problem"] = problem + " Accepted critique: " + review.Summary
		}
		raw, err := json.Marshal(prd)
		if err != nil {
			return nil, err
		}
		return []map[string]string{encode("prd", string(raw)), encode("announcement", "# "+topic+"\n\nFictional local preview. Nothing is sent or published.\n")}, nil
	case "reviewer":
		var review map[string]any
		if json.Unmarshal([]byte(d.Seed["review.json"]), &review) != nil || review == nil {
			return nil, errors.New("Northwind fixture review unavailable")
		}
		review["summary"] = "Critique for " + topic + ": clarify the fictional pilot scope. Advice, not sign-off."
		raw, err := json.Marshal(review)
		if err != nil {
			return nil, err
		}
		return []map[string]string{encode("review", string(raw))}, nil
	}
	return nil, errors.New("unsupported Northwind flow fixture agent")
}
