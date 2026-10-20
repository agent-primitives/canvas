package canvas

import "testing"

func TestNorthwindAnnouncementDraftUsesSeparateNativeAction(t *testing.T) {
	d, err := LoadGenericDefinition(genericFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.actions["announce"].Document != "announcement" {
		t.Fatal("Northwind has no separate approval action")
	}
	if _, err := d.Changes("writer", changeEnvelope("announcement", "# Local draft\n")); err != nil {
		t.Fatal("writer cannot propose guarded announcement draft", err)
	}
	for _, b := range d.Bundles {
		if b.Suite == nil {
			t.Fatal("suite identity lost")
		}
	}
}
