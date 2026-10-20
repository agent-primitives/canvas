package canvas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func genericFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "definition")
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "examples", "northwind-prd"))); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "canvas.yaml")
}
func genericConfigEdit(t *testing.T, p, old, value string) {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("fixture lacks %q", old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(data), old, value, 1)), 0600); err != nil {
		t.Fatal(err)
	}
}
func changeEnvelope(id, content string) string {
	b, _ := json.Marshal(map[string]any{"changes": []map[string]string{{"document": id, "operation": "replace", "content": content}}})
	return string(b)
}

const validPRD = `{"title":"Fictional pilot","problem":"Slow setup reported, not measured","proposedSuccessCriterion":"Proposed, not achieved","risks":["tenant isolation","rollback"],"openQuestions":["pilot criteria"],"fictional":true}`

func TestRestrictedWriterScopeRejectsAnnouncementAtomically(t *testing.T) {
	p := genericFixture(t)
	genericConfigEdit(t, p, "propose: [prd, announcement]", "propose: [prd]")
	d, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Changes("writer", changeEnvelope("announcement", "Simulated announcement")); err == nil {
		t.Fatal("announcement allowed before approval adoption")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(changeEnvelope("prd", validPRD)), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["changes"] = append(envelope["changes"].([]any), map[string]string{"document": "announcement", "operation": "replace", "content": "Simulated announcement"})
	raw, _ := json.Marshal(envelope)
	if changes, err := d.Changes("writer", string(raw)); err == nil || changes != nil {
		t.Fatal("mixed unauthorized announcement published partial PRD")
	}
}

func TestGenericDefinitionCompilesWithoutFootballAndValidatesChanges(t *testing.T) {
	p := genericFixture(t)
	d, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "northwind-prd" || d.Title != "Northwind fictional PRD" || len(d.Digest) != 64 || len(d.Bundles) != 2 || len(d.Documents) != 5 || len(d.People) != 3 {
		t.Fatalf("bad compiled definition: %+v", d)
	}
	if d.Policy.Validate() != nil || !d.Policy.IsInput("feedback/initial.json") || d.Policy.IsInput("prd.json") {
		t.Fatal("input policy incorrect")
	}
	if d.Bundles[0].ID != "reviewer" || d.Bundles[1].ID != "writer" {
		t.Fatal("nondeterministic bundle ordering")
	}
	changes, err := d.Changes("writer", changeEnvelope("prd", validPRD))
	if err != nil || changes["prd.json"] != validPRD || len(changes) != 1 {
		t.Fatal("document ID was not mapped to declared path", err)
	}
	// Public presentation maps are not the validated proposal authority.
	d.Documents["prd"] = DocumentDefinition{Path: "secrets.txt"}
	a := d.Agents["writer"]
	a.Propose = []string{"review"}
	d.Agents["writer"] = a
	changes, err = d.Changes("writer", changeEnvelope("prd", validPRD))
	if err != nil || changes["prd.json"] != validPRD {
		t.Fatal("public mutation altered compiled authority", err)
	}
	if _, err = d.Changes("writer", changeEnvelope("review", `{}`)); err == nil {
		t.Fatal("mutated public scope authorized write")
	}
	other, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	if other.Digest != d.Digest {
		t.Fatal("nondeterministic definition digest")
	}
	genericConfigEdit(t, p, "northwind-prd", "lab-notes")
	genericConfigEdit(t, p, "Northwind fictional PRD", "Unrelated laboratory notes")
	unrelated, err := LoadGenericDefinition(p)
	if err != nil || unrelated.ID != "lab-notes" || unrelated.Digest == d.Digest {
		t.Fatal("hardcoded project or unbound config", err)
	}
}

func TestGenericDefinitionRefusalMatrix(t *testing.T) {
	cases := []struct{ name, old, value string }{
		{"version", "canvas.local/v1alpha2", "canvas.local/v1alpha1"},
		{"unknown", "kind: Canvas", "kind: Canvas\nsurprise: credential-canary"},
		{"duplicate", "kind: Canvas", "kind: Canvas\nkind: Canvas"},
		{"alias", "kind: Canvas", "kind: &alias Canvas"},
		{"multidoc", "kind: Canvas", "kind: Canvas\n---\nkind: Canvas"},
		{"private-path", "path: prd.json", "path: .private/prd.json"},
		{"traversal", "path: prd.json", "path: ../prd.json"},
		{"double-glob", "feedback/*.json", "feedback/**.json"},
		{"unknown-read", "read: [brief, feedback, prd]", "read: [missing]"},
		{"unknown-propose", "propose: [prd, announcement]", "propose: [missing]"},
		{"glob-propose", "propose: [prd, announcement]", "propose: [feedback]"},
		{"suite-escape", "directory: suites/writer", "directory: ../writer"},
		{"seed-escape", "prd.json: seeds/prd.json", "prd.json: ../prd.json"},
		{"seed-target", "prd.json: seeds/prd.json", "unknown.json: seeds/prd.json"},
		{"missing-seed", "prd.json: seeds/prd.json\n", ""},
		{"trigger-agent", "agent: writer", "agent: missing"},
		{"trigger-path", "path: feedback/*.json", "path: unknown/*.json"},
		{"no-manual", "type: manual", "type: automatic"},
		{"unknown-person", "id: GM", "id: admin"},
		{"wrong-case-person", "id: GM", "id: gm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := genericFixture(t)
			genericConfigEdit(t, p, tc.old, tc.value)
			_, err := LoadGenericDefinition(p)
			if err == nil || strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("unsafe definition not safely refused", err)
			}
		})
	}
}

func TestGenericSchemasRejectExternalReferencesAndInvalidSeeds(t *testing.T) {
	for _, schema := range []string{`{"$ref":"../x.json"}`, `{"$ref":"file:///tmp/private.json"}`, `{"$ref":"https://example.invalid/x.json"}`, `{"$ref":"http://127.0.0.1:1/x.json"}`, `{"type":"not-a-type"}`, `{"type":"object","required":["absent"]}`, `{"type":"object","type":"string"}`} {
		t.Run(schema, func(t *testing.T) {
			p := genericFixture(t)
			if err := os.WriteFile(filepath.Join(filepath.Dir(p), "schemas", "prd.json"), []byte(schema), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadGenericDefinition(p); err == nil {
				t.Fatal("unsafe schema/seed accepted")
			}
		})
	}
	p := genericFixture(t)
	schema := `{"$defs":{"title":{"type":"string"}},"type":"object","properties":{"title":{"$ref":"#/$defs/title"}}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "schemas", "prd.json"), []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal("embedded refs refused", err)
	}
	if _, err = d.Changes("writer", changeEnvelope("prd", `{"title":42}`)); err == nil {
		t.Fatal("embedded ref was not enforced")
	}
}

func TestGenericChangesAreBoundedStrictAndAtomic(t *testing.T) {
	d, err := LoadGenericDefinition(genericFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := changeEnvelope("prd", validPRD)
	cases := []string{`{"changes":[{"document":"announcement","operation":"replace"}]}`, `{"changes":[{"document":"announcement","operation":"replace","content":null}]}`, `{"changes":[]}`, `{"changes":null}`, `{"changes":[],"extra":"credential-canary"}`, `{"changes":[],"changes":[]}`, valid + `{}`, strings.Replace(valid, `"replace"`, `"append"`, 1), strings.Replace(valid, `"document"`, `"path"`, 1), changeEnvelope("prd.json", validPRD), changeEnvelope("review", `{}`), changeEnvelope("feedback", `{}`), changeEnvelope("prd", `{"title":"credential-canary"}`), changeEnvelope("announcement", strings.Repeat("x", 65537)), string([]byte{'{', 0xff, '}'}), strings.Replace(valid, `"operation":"replace"`, `"operation":"replace","operation":"replace"`, 1)}
	for _, output := range cases {
		changes, err := d.Changes("writer", output)
		if err == nil || changes != nil || strings.Contains(err.Error(), "credential-canary") {
			t.Fatal("invalid set did not return zero map and bounded error", err)
		}
	}
	if _, err = d.Changes("unknown", valid); err == nil {
		t.Fatal("unknown actor accepted")
	}
	var envelope struct {
		Changes []map[string]string `json:"changes"`
	}
	json.Unmarshal([]byte(valid), &envelope)
	envelope.Changes = append(envelope.Changes, map[string]string{"document": "review", "operation": "replace", "content": "{}"})
	mixed, _ := json.Marshal(envelope)
	if changes, err := d.Changes("writer", string(mixed)); err == nil || changes != nil {
		t.Fatal("partial write set escaped")
	}
	envelope.Changes[1] = envelope.Changes[0]
	duplicates, _ := json.Marshal(envelope)
	if changes, err := d.Changes("writer", string(duplicates)); err == nil || changes != nil {
		t.Fatal("duplicate document accepted")
	}
}

func TestGenericSourcesRefuseSymlinksAndBindLoadedBytes(t *testing.T) {
	for _, name := range []string{"canvas.yaml", "seeds/prd.json", "schemas/prd.json", "suites/writer"} {
		t.Run(name, func(t *testing.T) {
			p := genericFixture(t)
			root := filepath.Dir(p)
			old := filepath.Join(root, filepath.FromSlash(name))
			target := old + "-original"
			if err := os.Rename(old, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, old); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadGenericDefinition(p); err == nil {
				t.Fatal("linked source accepted")
			}
		})
	}
	p := genericFixture(t)
	a, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	genericConfigEdit(t, filepath.Join(filepath.Dir(p), "seeds/prd.json"), "Fictional Northwind PRD", "Other fictional PRD")
	b, err := LoadGenericDefinition(p)
	if err != nil || a.Digest == b.Digest {
		t.Fatal("seed bytes not bound", err)
	}
	schema := filepath.Join(filepath.Dir(p), "schemas/prd.json")
	data, _ := os.ReadFile(schema)
	os.WriteFile(schema, append(data, '\n'), 0600)
	c, err := LoadGenericDefinition(p)
	if err != nil || b.Digest == c.Digest {
		t.Fatal("schema exact bytes not bound", err)
	}
}
