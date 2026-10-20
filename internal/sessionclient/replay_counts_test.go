package sessionclient

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestReplayCallAndCoverageEvidence(t *testing.T) {
	s := validReply(t)
	s.Fields["executorCalls"] = structpb.NewNumberValue(0)
	s.Fields["executionIDs"] = structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{structpb.NewStringValue("exec-1")}})
	p, err := decodeProof(s)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || string(fields["executorCalls"]) != "0" || string(fields["executionIDs"]) != `["exec-1"]` {
		t.Fatalf("missing 0/0 coverage evidence: %s", data)
	}
	for name, value := range map[string]*structpb.Value{
		"wrong executor":     structpb.NewStringValue("0"),
		"negative executor":  structpb.NewNumberValue(-1),
		"empty coverage":     structpb.NewListValue(&structpb.ListValue{}),
		"duplicate coverage": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{structpb.NewStringValue("exec-1"), structpb.NewStringValue("exec-1")}}),
	} {
		copy := validReply(t)
		copy.Fields["executorCalls"] = s.Fields["executorCalls"]
		copy.Fields["executionIDs"] = s.Fields["executionIDs"]
		key := "executionIDs"
		if name == "wrong executor" || name == "negative executor" {
			key = "executorCalls"
		}
		copy.Fields[key] = value
		if _, err := decodeProof(copy); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	for _, key := range []string{"executorCalls", "executionIDs"} {
		copy := validReply(t)
		copy.Fields[key] = s.Fields[key]
		if _, err := decodeProof(copy); err == nil {
			t.Fatalf("partial %s evidence accepted", key)
		}
	}
}
