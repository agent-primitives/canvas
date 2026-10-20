package canvas

import (
	"os"
	"strings"
	"testing"
)

const announcementDeclaration = "  actions:\n    announce:\n      name: Simulated announcement\n      kind: simulated-announcement\n      document: announcement\n"

func actionDefinitionFixture(t *testing.T) string {
	t.Helper()
	p := genericFixture(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "  actions:") {
		genericConfigEdit(t, p, "  triggers:", announcementDeclaration+"  triggers:")
	}
	return p
}

func TestAnnouncementDefinitionCompilesPrivateActionAuthority(t *testing.T) {
	p := actionDefinitionFixture(t)
	d, err := LoadGenericDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	a := d.Actions["announce"]
	if a.Name != "Simulated announcement" || a.Kind != "simulated-announcement" || a.Document != "announcement" {
		t.Fatalf("declaration: %+v", a)
	}
	d.Actions["announce"] = ActionDefinition{Document: "feedback"}
	if d.actions["announce"].Document != "announcement" {
		t.Fatal("public action map changed execution authority")
	}
}

func TestAnnouncementDefinitionRefusesUnsupportedActions(t *testing.T) {
	for _, entry := range []struct{ name, old, value string }{
		{"input", "document: announcement", "document: feedback"},
		{"missing", "document: announcement", "document: undeclared"},
		{"kind", "kind: simulated-announcement", "kind: external-send"},
		{"empty-name", "name: Simulated announcement", "name: ''"},
		{"unknown-field", "kind: simulated-announcement", "kind: simulated-announcement\n      destination: external"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			p := actionDefinitionFixture(t)
			genericConfigEdit(t, p, entry.old, entry.value)
			if _, err := LoadGenericDefinition(p); err == nil {
				t.Fatal("unsupported action admitted")
			}
		})
	}
}
