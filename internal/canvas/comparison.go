package canvas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// These presentation limits do not truncate or replace the exact raw diff.
const comparisonDocumentLimit = 64 << 10
const comparisonDisplayLimit = 128 << 10

func documentComparison(path, before, after string) (string, string) {
	fallback := func(reason string) (string, string) {
		return "Raw line fallback: " + reason + ".\n" + documentDiff(path, before, after), "raw-lines"
	}
	if len(before) > comparisonDocumentLimit || len(after) > comparisonDocumentLimit {
		return fallback("document exceeds readable comparison size limits")
	}
	a, b := before, after
	kind, label := "text-lines", "Text line comparison."
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		kind, label = "yaml-lines", "YAML line comparison (not YAML validation)."
	default:
		if strings.EqualFold(filepath.Ext(path), ".json") || json.Valid([]byte(before)) || json.Valid([]byte(after)) {
			var okA, okB bool
			a, okA = comparisonJSON(before)
			b, okB = comparisonJSON(after)
			if !okA || !okB {
				return fallback("JSON is invalid, has duplicate keys, or exceeds nesting limits")
			}
			kind, label = "json-lines", "JSON field/line comparison (presentation only; see raw diff for exact bytes)."
		}
	}
	if len(a) > comparisonDocumentLimit || len(b) > comparisonDocumentLimit {
		return fallback("expanded document exceeds readable comparison size limits")
	}
	body := comparisonLines(path, a, b)
	if kind == "json-lines" && a == b && before != after {
		body = "No readable JSON changes (formatting differs; see raw diff).\n"
	}
	out := label + "\n" + body
	if len(out) > comparisonDisplayLimit {
		return fallback("expanded comparison exceeds display size limits")
	}
	return out, kind
}

func comparisonJSON(text string) (string, bool) {
	// Empty content represents a missing document, not an invented JSON value.
	if text == "" {
		return "", true
	}
	if !utf8.ValidString(text) || !json.Valid([]byte(text)) {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if !comparisonJSONValue(dec, 0) {
		return "", false
	}
	// Compact/Indent retain number and string lexemes rather than decoding into
	// float64 or reserializing values. Only presentation whitespace is changed.
	var compact, pretty bytes.Buffer
	if json.Compact(&compact, []byte(text)) != nil || json.Indent(&pretty, compact.Bytes(), "", "  ") != nil {
		return "", false
	}
	if strings.HasSuffix(text, "\n") {
		pretty.WriteByte('\n')
	}
	return pretty.String(), true
}

// Check keys before formatting: silently collapsing duplicates would present
// an ambiguous document as a trustworthy field comparison. Depth also bounds
// indentation expansion and recursion independently of the input byte limit.
func comparisonJSONValue(dec *json.Decoder, depth int) bool {
	token, err := dec.Token()
	if err != nil {
		return false
	}
	delim, container := token.(json.Delim)
	if !container {
		return true
	}
	if depth >= 100 {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		if delim == '{' {
			keyToken, err := dec.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return false
			}
			seen[key] = true
		}
		if !comparisonJSONValue(dec, depth+1) {
			return false
		}
	}
	_, err = dec.Token()
	return err == nil
}

// A linear, single-hunk replacement with two unchanged lines of context keeps
// nested field names visible without a quadratic diff algorithm.
func comparisonLines(path, before, after string) string {
	if before == after {
		return "No content changes.\n"
	}
	a, b := diffLines(before), diffLines(after)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	endA, endB := len(a), len(b)
	for endA > prefix && endB > prefix && a[endA-1] == b[endB-1] {
		endA--
		endB--
	}
	start, context := max(0, prefix-2), min(2, len(a)-endA)
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (proposal base)\n+++ %s (proposed)\n@@ -%s +%s @@\n", path, path, hunkRange(start, endA+context-start), hunkRange(start, endB+context-start))
	for _, line := range a[start:prefix] {
		writeDiffLine(&out, " ", line)
	}
	for _, line := range a[prefix:endA] {
		writeDiffLine(&out, "-", line)
	}
	for _, line := range b[prefix:endB] {
		writeDiffLine(&out, "+", line)
	}
	for _, line := range a[endA : endA+context] {
		writeDiffLine(&out, " ", line)
	}
	return out.String()
}
