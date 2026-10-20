package canvas

import (
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/wire"
)

func resumeEvidence() []*v1.LogRecord {
	n := int64(1)
	return []*v1.LogRecord{
		{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventExecutionStart, ExecutionStart: &api.ExecutionStart{InputCount: &n, Harness: "known"}})},
		{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventInput, Message: api.TextMessage("user", `{"mode":"scout"}`)})},
		{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventModelCall, ModelCall: &api.ModelCall{ID: "call", Model: "fixture", InputHash: strings.Repeat("a", 64)}})},
		{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventError, Err: &api.Error{Description: "failed"}})},
	}
}

// Removing any of the corresponding production guards must authorize an unsafe journal.
func TestResumeClassificationFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		change  func([]*v1.LogRecord) []*v1.LogRecord
		allowed bool
	}{
		{"pending model with audit error", func(r []*v1.LogRecord) []*v1.LogRecord { return r }, true},
		{"lifecycle after pending", func(r []*v1.LogRecord) []*v1.LogRecord {
			return append(r, &v1.LogRecord{Event: wire.EventToProto(api.Event{Kind: api.EventLifecycle, Lifecycle: &api.Lifecycle{Kind: api.LifecycleSuspend}})})
		}, true},
		{"legacy", func(r []*v1.LogRecord) []*v1.LogRecord {
			for _, v := range r {
				v.Event.ExecutionId = ""
			}
			return r
		}, false},
		{"missing start", func(r []*v1.LogRecord) []*v1.LogRecord { return r[1:] }, false},
		{"incomplete inputs", func(r []*v1.LogRecord) []*v1.LogRecord {
			n := int64(2)
			r[0].Event.GetExecutionStart().InputCount = &n
			return r
		}, false},
		{"missing count", func(r []*v1.LogRecord) []*v1.LogRecord { r[0].Event.GetExecutionStart().InputCount = nil; return r }, false},
		{"wrong pending harness", func(r []*v1.LogRecord) []*v1.LogRecord { r[0].Event.GetExecutionStart().Harness = "imported"; return r }, false},
		{"opaque config", func(r []*v1.LogRecord) []*v1.LogRecord {
			r[0].Event.GetExecutionStart().Config = []byte("config")
			return r
		}, false},
		{"harness version", func(r []*v1.LogRecord) []*v1.LogRecord {
			r[0].Event.GetExecutionStart().HarnessVersion = "other"
			return r
		}, false},
		{"wrong ownership", func(r []*v1.LogRecord) []*v1.LogRecord {
			r[1].Event.GetMessage().Parts[0].GetText().Text = `{"mode":"question"}`
			return r
		}, false},
		{"input after effect", func(r []*v1.LogRecord) []*v1.LogRecord { r[1], r[2] = r[2], r[1]; return r }, false},
		{"duplicate model", func(r []*v1.LogRecord) []*v1.LogRecord { return append(r, r[2]) }, false},
		{"missing hash", func(r []*v1.LogRecord) []*v1.LogRecord { r[2].Event.GetModel().InputHash = ""; return r }, false},
		{"missing model identity", func(r []*v1.LogRecord) []*v1.LogRecord { r[2].Event.GetModel().Id = ""; return r }, false},
		{"wrong model", func(r []*v1.LogRecord) []*v1.LogRecord { r[2].Event.GetModel().Model = "different"; return r }, false},
		{"recorded output", func(r []*v1.LogRecord) []*v1.LogRecord {
			return append(r, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventOutput, Message: api.TextMessage("assistant", "done")})})
		}, false},
		{"end", func(r []*v1.LogRecord) []*v1.LogRecord {
			return append(r, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventEnd, End: &api.HarnessEnd{State: "COMPLETED"}})})
		}, false},
		{"keyed tool", func(r []*v1.LogRecord) []*v1.LogRecord {
			return append(r, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventToolCall, ToolCall: &api.ToolCall{ID: "tool", Tool: "write", IdempotencyKey: "durable-key", Mediation: api.MediationControllerMediated}})})
		}, false},
		{"approval", func(r []*v1.LogRecord) []*v1.LogRecord {
			return append(r, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "pending", Kind: api.EventApprovalRequest, Approval: &api.ApprovalRequest{ToolCallID: "tool"}})})
		}, false},
		{"tool definition", func(r []*v1.LogRecord) []*v1.LogRecord {
			r[2].Event.GetModel().Tools = []*v1.ToolDefinition{{Name: "write"}}
			return r
		}, false},
		{"tool message part", func(r []*v1.LogRecord) []*v1.LogRecord {
			r[1].Event.GetMessage().Parts = append(r[1].Event.GetMessage().Parts, &v1.Part{Part: &v1.Part_ToolCall{ToolCall: &v1.ToolCall{Tool: "write"}}})
			return r
		}, false},
		{"unknown event", func(r []*v1.LogRecord) []*v1.LogRecord { r[3].Event.Kind = v1.EventKind(199); return r }, false},
		{"malformed error", func(r []*v1.LogRecord) []*v1.LogRecord { r[3].Event.Body = nil; return r }, false},
		{"new execution steals tail", func(r []*v1.LogRecord) []*v1.LogRecord {
			n := int64(1)
			return append(r, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "other", Kind: api.EventExecutionStart, ExecutionStart: &api.ExecutionStart{InputCount: &n, Harness: "known"}})}, r[3])
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execution, reason := classifyResume(tt.change(resumeEvidence()), "known", `{"mode":"scout"}`, "fixture")
			if (execution != "") != tt.allowed {
				t.Fatalf("execution=%q reason=%q allowed=%v", execution, reason, tt.allowed)
			}
			if !tt.allowed && reason == "" {
				t.Fatal("refusal lacks bounded reason")
			}
		})
	}
}

func TestResumeSelectsLastFirstSeenExecutionNotLastAuditIdentity(t *testing.T) {
	old := resumeEvidence()
	for _, r := range old {
		r.Event.ExecutionId = "old"
	}
	old = append(old, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "old", Kind: api.EventOutput, Message: api.TextMessage("assistant", "old result")})}, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: "old", Kind: api.EventEnd, End: &api.HarnessEnd{State: "COMPLETED"}})})
	records := append(old, resumeEvidence()...)
	records = append(records, old[3])
	execution, reason := classifyResume(records, "known", `{"mode":"scout"}`, "fixture")
	if execution != "pending" {
		t.Fatalf("last first-seen execution not selected: %q %s", execution, reason)
	}
}
