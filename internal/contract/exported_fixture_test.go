package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAbsentDetachedEvidenceIsNotVerifiedRatherThanLoadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	expected, evidence, err := EvaluationJSON(path, path)
	if err != nil || len(expected) != 0 || len(evidence) != 0 {
		t.Fatal("missing evidence should remain unverified", err)
	}
	b, err := LoadReleasedSuite("writer", releasedNorthwind(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err = WithReleasedEvaluation(b, expected, evidence)
	if err != nil || b.Suite.Release.Evaluation.Status != "not verified" || b.Suite.Release.Evaluation.Reason != "missing" {
		t.Fatal("missing evidence upgraded or refused suite", err)
	}
}

func TestGenuineKmxExportedReleaseEvidenceAndMismatch(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "northwind-released")
	pins := map[string]string{"writer": "sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544", "reviewer": "sha256:6ea51f953d1d51be7316fb155cbce783e7010afb43169e479f44e0cce2783ccc"}
	for id, pin := range pins {
		t.Run(id, func(t *testing.T) {
			b, err := LoadReleasedSuite(id, SuiteRef{Ref: pin, Layout: filepath.Join(root, "layouts", id), Member: id, Platform: "linux/amd64"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join(root, "evidence", id+".expected.json"))
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := os.ReadFile(filepath.Join(root, "evidence", id+".export.json"))
			if err != nil {
				t.Fatal(err)
			}
			evaluated, err := WithReleasedEvaluation(b, expected, evidence)
			if err != nil {
				t.Fatal(err)
			}
			got := evaluated.Suite.Release.Evaluation
			if got.Status != "evaluation consistent; unsigned" || !got.Unsigned || got.Cases != 2 || got.Runs != 2 || got.CompletedAt == "" || got.TestedExecution == nil || got.TestedExecution.Runtime.ID != "orka" {
				t.Fatalf("export not consistently verified: %+v", got)
			}
			clone := evaluated.Suite.Clone()
			clone.Release.Evaluation.TestedExecution.Runtime.ID = "forged"
			if evaluated.Suite.Release.Evaluation.TestedExecution.Runtime.ID != "orka" {
				t.Fatal("evaluation clone shares mutable authority")
			}
			if id == "writer" {
				mismatched, err := os.ReadFile(filepath.Join(root, "evidence", "writer.mismatched.export.json"))
				if err != nil {
					t.Fatal(err)
				}
				bad, err := WithReleasedEvaluation(b, expected, mismatched)
				if err != nil {
					t.Fatal(err)
				}
				if bad.Suite.Release.Evaluation.Status != "not verified" || bad.Suite.Release.Evaluation.Reason != "subject" {
					t.Fatal("mismatch not refused")
				}
				var e map[string]any
				json.Unmarshal(expected, &e)
				e["judges"] = nil
				nullExpected, _ := json.Marshal(e)
				bad, err = WithReleasedEvaluation(b, nullExpected, evidence)
				if err != nil || bad.Suite.Release.Evaluation.Reason != "malformed" {
					t.Fatal("null expected allowlist accepted", err)
				}
			}
		})
	}
}
