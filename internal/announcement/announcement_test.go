package announcement

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/wire"
)

func fixture() Snapshot {
	content := "Exact café\n<draft>&\n"
	digest := sha256.Sum256([]byte(content))
	return Snapshot{Operation: "42bd81ac01354b87a02f2d54aba543cb", Definition: strings.Repeat("a", 64), Action: "announce", Document: "announcement", Path: "documents/announcement.md", Proposal: "proposal_1.abc-2", Revision: strings.Repeat("b", 40), Digest: hex.EncodeToString(digest[:]), Content: content}
}

// Catches lossy content encoding, casing changes, and non-string protobuf args.
func TestSnapshotRoundTrip(t *testing.T) {
	s := fixture()
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\"Operation\"") || !strings.Contains(string(b), "\"operation\"") {
		t.Fatal("not lowerCamel JSON")
	}
	got, err := Decode(b)
	if err != nil || got != s {
		t.Fatalf("JSON roundtrip: %+v %v", got, err)
	}
	ev := api.Event{Kind: api.EventToolCall, ToolCall: &api.ToolCall{ID: s.Operation, Args: Args(s)}}
	decoded := wire.EventFromProto(wire.EventToProto(ev))
	got, err = FromArgs(decoded.ToolCall.Args)
	if err != nil || got != s {
		t.Fatalf("proto roundtrip: %+v %v", got, err)
	}
	receipt, err := DecodeReceipt(map[string]any{"kind": "SIMULATED_ANNOUNCEMENT", "snapshot": Args(s)})
	if err != nil || receipt.Kind != "SIMULATED_ANNOUNCEMENT" || receipt.Snapshot != s {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
}

func TestSnapshotRejectsMalformedBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Snapshot)
	}{
		{"empty operation", func(s *Snapshot) { s.Operation = "" }},
		{"operation delimiter", func(s *Snapshot) { s.Operation = "op/x" }},
		{"long ID", func(s *Snapshot) { s.Proposal = strings.Repeat("x", 129) }},
		{"definition", func(s *Snapshot) { s.Definition = strings.Repeat("A", 64) }},
		{"revision", func(s *Snapshot) { s.Revision = strings.Repeat("c", 39) }},
		{"digest", func(s *Snapshot) { s.Digest = strings.Repeat("0", 64) }},
		{"action", func(s *Snapshot) { s.Action = "Announce" }},
		{"document", func(s *Snapshot) { s.Document = "a/b" }},
		{"parent path", func(s *Snapshot) { s.Path = "../secret" }},
		{"absolute path", func(s *Snapshot) { s.Path = "/tmp/doc" }},
		{"unclean path", func(s *Snapshot) { s.Path = "docs/./doc" }},
		{"glob path", func(s *Snapshot) { s.Path = "docs/*.md" }},
		{"backslash", func(s *Snapshot) { s.Path = "docs\\x" }},
		{"control path", func(s *Snapshot) { s.Path = "docs/\nx" }},
		{"invalid UTF8", func(s *Snapshot) { s.Content = string([]byte{255}) }},
		{"content limit", func(s *Snapshot) { s.Content = strings.Repeat("x", 65537) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			tc.change(&s)
			if Validate(s) == nil {
				t.Fatal("accepted invalid snapshot")
			}
			if _, err := Encode(s); err == nil {
				t.Fatal("encoded invalid snapshot")
			}
		})
	}
}

func TestSnapshotStrictJSONAndArgs(t *testing.T) {
	b, err := Encode(fixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		string(b) + " {}", strings.Replace(string(b), "{", "{\"unknown\":\"x\",", 1),
		strings.Replace(string(b), "{", "{\"operation\":\"other\",", 1),
		strings.Replace(string(b), "\"operation\"", "\"Operation\"", 1),
		strings.Replace(string(b), "\"content\":", "\"content\":null,\"unused\":", 1),
		"{}", "null", "[]",
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted JSON %s", raw)
		}
	}
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["extra"] = "x" }, func(m map[string]any) { delete(m, "content") },
		func(m map[string]any) { m["operation"] = 42 }, func(m map[string]any) { m["content"] = nil },
	} {
		args := Args(fixture())
		change(args)
		if _, err := FromArgs(args); err == nil {
			t.Fatal("accepted malformed args")
		}
	}
	for _, output := range []map[string]any{
		nil, {"kind": "SUCCESS", "snapshot": Args(fixture())}, {"kind": "SIMULATED_ANNOUNCEMENT", "snapshot": "not-map"},
		{"kind": "SIMULATED_ANNOUNCEMENT", "snapshot": Args(fixture()), "approvalRequestSeq": 1},
	} {
		if _, err := DecodeReceipt(output); err == nil {
			t.Fatal("accepted malformed receipt")
		}
	}
}
