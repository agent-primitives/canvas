package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
)

// EvaluationInfo is a checked release claim, never proof of the current Canvas run.
type EvaluationInfo struct {
	Status          string                        `json:"status"`
	Reason          string                        `json:"reason,omitempty"`
	EvidenceDigest  string                        `json:"evidenceDigest,omitempty"`
	ExpectedDigest  string                        `json:"expectedDigest,omitempty"`
	Cases           int                           `json:"cases,omitempty"`
	Runs            int                           `json:"runs,omitempty"`
	CompletedAt     string                        `json:"completedAt,omitempty"`
	Unsigned        bool                          `json:"unsigned"`
	TestedExecution *agentsuite.EvidenceExecution `json:"testedExecution,omitempty"`
}

func verifyReleaseEvaluation(layout string, a releasedArchive, manifest releaseDescriptor, identity *SuiteIdentity, agent agentsuite.Agent) EvaluationInfo {
	result := EvaluationInfo{Status: "not verified", Reason: "missing"}
	digest := func(data []byte) string { sum := sha256.Sum256(data); return fmt.Sprintf("sha256:%x", sum) }
	if len(a.Expected) > 0 {
		result.ExpectedDigest = digest(a.Expected)
	}
	if len(a.Evidence) > 0 {
		result.EvidenceDigest = digest(a.Evidence)
	}
	if a.RuntimeProfile != "" && len(a.Evidence) == 0 {
		result.Reason = "export unavailable for this profile"
		return result
	}
	if len(a.Expected) == 0 || len(a.Evidence) == 0 {
		return result
	}
	var expected agentsuite.ExpectedEvaluation
	if !validExpectedShape(a.Expected) || agentsuite.Unmarshal(a.Expected, &expected) != nil || expected.Judges == nil {
		result.Reason = "malformed"
		return result
	}
	parts := strings.Split(a.Platform, "/")
	platform := agentsuite.EvidencePlatform{}
	if len(parts) >= 2 {
		platform.OS = parts[0]
		platform.Architecture = parts[1]
	}
	if len(parts) == 3 {
		platform.Variant = parts[2]
	}
	selected := agentsuite.EvidenceSubject{Manifest: agentsuite.EvidenceDescriptor{MediaType: manifest.MediaType, Digest: a.Pin, Size: manifest.Size}, MemberID: a.Member, MemberDigest: identity.MemberDigest, CompositionDigest: identity.Release.CompositionDigest, BuildProfileDigest: identity.Release.BuildProfileDigest, Platform: platform}
	if expected.Subject != selected || expected.Execution.Model.ID != agent.Model.Model || expected.Execution.Model.Protocol != agent.Model.Protocol {
		result.Reason = "subject"
		return result
	}
	verified, err := agentsuite.VerifyEvaluation(layout, expected, bytes.NewReader(a.Evidence))
	if err != nil {
		var refused *agentsuite.EvidenceRefusal
		if errors.As(err, &refused) {
			result.Reason = refused.Code
		} else {
			result.Reason = "malformed"
		}
		return result
	}
	// Only verified summaries and independently configured execution identities are published.
	result.Status = "evaluation consistent; unsigned"
	result.Reason = ""
	result.Cases = verified.Cases
	result.Runs = verified.Runs
	result.CompletedAt = verified.CompletedAt
	result.Unsigned = verified.Unsigned
	execution := expected.Execution
	result.TestedExecution = &execution
	return result
}

// EvaluationJSON keeps expected identity independent from evidence and bounds local files.
func EvaluationJSON(expected, evidence string) ([]byte, []byte, error) {
	read := func(value string) ([]byte, error) {
		if value == "" {
			return nil, nil
		}
		if _, err := os.Lstat(value); os.IsNotExist(err) {
			return nil, nil
		}
		abs, err := noLinkedAncestors(value)
		if err != nil {
			return nil, errSuite
		}
		files, err := readEvaluationFile(abs)
		if err != nil {
			return nil, errSuite
		}
		return files, nil
	}
	e, err := read(expected)
	if err != nil {
		return nil, nil, err
	}
	b, err := read(evidence)
	return e, b, err
}

func readEvaluationFile(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errSuite
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, errSuite
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, errSuite
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(data) > 1<<20 {
		return nil, errSuite
	}
	return data, nil
}

// Strictly decode an independent expectation's outer shape, including explicit judges[].
func validExpectedShape(data []byte) bool {
	var fields map[string]json.RawMessage
	if agentsuite.Unmarshal(data, &fields) != nil || len(fields) != 4 {
		return false
	}
	for _, name := range []string{"subject", "execution", "evaluator", "judges"} {
		raw, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	return true
}
