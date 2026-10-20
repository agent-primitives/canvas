package sessionclient

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func validReply(t *testing.T) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(map[string]any{"chainValid": true, "reconstructed": true, "outputsMatch": true, "liveUnchanged": true, "modelCalls": 0, "records": 4, "head": "9007199254740993", "requestedModels": []any{"fixture-model"}})
	if err != nil {
		t.Fatal("proof fixture failed")
	}
	return s
}
func TestProofPreservesExactHeadAndJSONNames(t *testing.T) {
	p, err := decodeProof(validReply(t))
	if err != nil || p.Head != 9007199254740993 || p.Records != 4 || p.ModelCalls != 0 || !p.ChainValid || !p.Reconstructed || !p.OutputsMatch || !p.LiveUnchanged || len(p.RequestedModels) != 1 || p.RequestedModels[0] != "fixture-model" {
		t.Fatal("proof decoding lost evidence")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal("proof JSON encoding failed")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("proof JSON malformed")
	}
	for _, name := range []string{"chainValid", "reconstructed", "outputsMatch", "liveUnchanged", "modelCalls", "records", "head", "requestedModels"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("proof JSON field must be lower camel case")
		}
	}
	if len(fields) != 8 {
		t.Fatal("unexpected proof JSON field")
	}
}
func TestMalformedProofCannotBecomePassingEvidence(t *testing.T) {
	cases := []struct {
		name, field string
		value       *structpb.Value
	}{
		{"missing chain", "chainValid", nil},
		{"untyped flag", "outputsMatch", &structpb.Value{}},
		{"string flag", "reconstructed", structpb.NewStringValue("true")},
		{"negative count", "modelCalls", structpb.NewNumberValue(-1)},
		{"fractional count", "records", structpb.NewNumberValue(1.5)},
		{"unbounded count", "records", structpb.NewNumberValue(20001)},
		{"inexact head", "head", structpb.NewNumberValue(1)},
		{"negative head", "head", structpb.NewStringValue("-1")},
		{"overflow head", "head", structpb.NewStringValue("9223372036854775808")},
		{"wrong models", "requestedModels", structpb.NewStringValue("fixture-model")},
		{"invalid model", "requestedModels", structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{structpb.NewNumberValue(4)}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validReply(t)
			if tc.value == nil {
				delete(s.Fields, tc.field)
			} else {
				s.Fields[tc.field] = tc.value
			}
			if _, err := decodeProof(s); err == nil {
				t.Fatal("malformed remote evidence accepted")
			}
		})
	}
}
