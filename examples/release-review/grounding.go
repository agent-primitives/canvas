// Package releasereview contains example-local evaluation helpers, not engine
// policy or verified AgentSuite evaluation evidence.
package releasereview

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var citation = regexp.MustCompile(`\[([^\[\]\n]+)\]`)

// CheckCitations checks reference existence, not whether prose follows from a
// source. Apply the semantic rubric in evals/README.md separately. The engine
// validates document shape and scope before any proposal is recorded.
func CheckCitations(inputs []byte, document string, content []byte) error {
	var in struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(inputs, &in); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, source := range in.Sources {
		if source.ID == "" || known[source.ID] {
			return errors.New("missing or duplicate source ID")
		}
		known[source.ID] = true
	}
	if len(known) == 0 {
		return errors.New("no sources")
	}
	check := func(ids []string) error {
		if len(ids) == 0 {
			return errors.New("claim without sourceIds")
		}
		for _, id := range ids {
			if !known[id] {
				return fmt.Errorf("unknown source ID %q", id)
			}
		}
		return nil
	}
	if document == "announcement" {
		matches := citation.FindAllSubmatch(content, -1)
		if len(matches) == 0 {
			return errors.New("announcement without citations")
		}
		for _, match := range matches {
			if err := check([]string{string(match[1])}); err != nil {
				return err
			}
		}
		return nil
	}
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(content, &groups); err != nil {
		return err
	}
	var fields []string
	switch document {
	case "notes":
		fields = []string{"changes", "upgradeSteps", "unknowns"}
	case "review":
		fields = []string{"findings"}
	default:
		return errors.New("unknown document")
	}
	for _, field := range fields {
		var claims []struct {
			SourceIDs []string `json:"sourceIds"`
		}
		if raw, ok := groups[field]; ok {
			if err := json.Unmarshal(raw, &claims); err != nil {
				return err
			}
		}
		for _, claim := range claims {
			if err := check(claim.SourceIDs); err != nil {
				return err
			}
		}
	}
	return nil
}
