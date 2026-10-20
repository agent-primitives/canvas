package canvas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadOnlyAnswerEnforcesExactSchemaKeysAndUnicodeLimit(t *testing.T) {
	for _, output := range []string{`{"Answer":"case variant"}`, `{"answer":"first","answer":"second"}`, `{"answer":"first","answer":null}`, `{"answer":null}`, `{"answer":"safe","rank":1}`, `{"answer":"safe"} {}`, `["safe"]`} {
		if _, err := decodeAnswer(output); err == nil {
			t.Errorf("non-schema answer accepted: %s", output)
		}
	}
	answer := strings.Repeat("界", 3000)
	raw, _ := json.Marshal(map[string]string{"answer": answer})
	got, err := decodeAnswer(string(raw))
	if err != nil || got != answer {
		t.Fatal("schema-valid Unicode explanation rejected as overlong bytes")
	}
	raw, _ = json.Marshal(map[string]string{"answer": answer + "界"})
	if _, err := decodeAnswer(string(raw)); err == nil {
		t.Fatal("more than 3000 Unicode characters accepted")
	}
}
