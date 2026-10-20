package canvas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrdinaryHyphenatedWordsAllowedInSnapshotsAndInputs(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	for _, word := range []string{"risk-reward", "high-risk-high-reward", "task-based", "high-risk-high-reward-synthetic-task-based-assessment"} {
		if _, err := c.snapshotGeneric("writer", "Assess "+word+" synthetic evidence.", "GM"); err != nil {
			t.Error("ordinary hyphenated request was rejected")
		}
		raw, _ := json.Marshal(map[string]string{"team": "Northwind", "feedback": "Synthetic " + word + " assessment"})
		if err := c.WriteInput("feedback/"+word+".json", string(raw), "coach"); err != nil {
			t.Error("ordinary hyphenated input was rejected")
		}

	}
}

func TestCredentialShapedTextRejectedAtSnapshotAndInputBoundaries(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	// Synthetic repeated-character fixtures only; never observed credentials.
	fixtures := []string{
		"sk-" + strings.Repeat("A", 48),
		"sk-proj-" + strings.Repeat("B", 140),
		"sk-svcacct-" + strings.Repeat("C", 140),
		"Bearer deterministic-fixture",
		"-----BEGIN PRIVATE KEY-----",
	}
	for _, fixture := range fixtures {
		if _, err := c.snapshotGeneric("writer", "Assess ("+fixture+").", "GM"); err == nil {
			t.Error("credential-shaped request accepted")
		}
		for _, field := range []string{"team", "feedback"} {
			input := map[string]string{"team": "Northwind", "feedback": "Fictional report"}
			input[field] = fixture
			raw, _ := json.Marshal(input)
			if err := c.WriteInput("feedback/synthetic.json", string(raw), "GM"); err == nil {
				t.Errorf("credential-shaped input %s accepted", field)
			}
		}
	}
}
