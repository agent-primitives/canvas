package contract

import (
	"bytes"
	"testing"
)

func TestSelectedModelDerivesANewExactContractWithoutEditingAuthoredBytes(t *testing.T) {
	authored, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(authored.Source)
	selected, err := ForModel(authored, "example-hosted-model")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Model != "example-hosted-model" || selected.Digest == authored.Digest || selected.AuthoredDigest != authored.Digest {
		t.Fatal("selection must have a distinct exact-source identity and authored provenance")
	}
	parsed, err := Parse(selected.Source)
	if err != nil || parsed.Digest != selected.Digest || parsed.Model != selected.Model || parsed.Instructions != authored.Instructions || parsed.ID != authored.ID {
		t.Fatal("derived bytes must describe exactly the selected execution contract")
	}
	if !bytes.Equal(authored.Source, before) || authored.Model != "qwen3:8b" {
		t.Fatal("selection edited the authored contract")
	}
	again, err := ForModel(authored, "example-hosted-model")
	if err != nil || !bytes.Equal(again.Source, selected.Source) || again.Digest != selected.Digest {
		t.Fatal("selection must be deterministic")
	}
	unchanged, err := ForModel(authored, authored.Model)
	if err != nil || !bytes.Equal(unchanged.Source, before) || unchanged.Digest != authored.Digest || unchanged.AuthoredDigest != authored.Digest {
		t.Fatal("matching selection must retain exact authored bytes")
	}
}

func TestSelectedModelRejectsMalformedDocumentShapesBeforeTraversal(t *testing.T) {
	for _, source := range []string{"", "null", "[]", "scalar", "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: scout}\nspec: []\n", "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: scout}\nspec: {instructions: synthetic, model: {name: []}}\n"} {
		if _, err := ForModel(Bundle{Source: []byte(source)}, "example-hosted-model"); err == nil {
			t.Fatal("malformed shape bypassed initial strict Parse")
		}
	}
}

func TestSelectedModelRejectsForgedBundleFields(t *testing.T) {
	for _, field := range []string{"model", "digest", "instructions", "id"} {
		b, _ := Parse([]byte(valid))
		switch field {
		case "model":
			b.Model = "forged"
		case "digest":
			b.Digest = "forged"
		case "instructions":
			b.Instructions = "forged"
		case "id":
			b.ID = "forged"
		}
		if _, err := ForModel(b, "example-hosted-model"); err == nil {
			t.Fatalf("accepted forged %s", field)
		}
	}
}
