package sessionhost

import (
	"context"
	"errors"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

// This harness has no provider, file access, or external effect. The native
// TOOL_RESULT is the only receipt; successful replay emits no duplicate output.
type announcementHarness struct{}

// NewAnnouncementHarness exposes the existing fixed, model-free simulation
// contract. Approval authority still belongs to the native controller.
func NewAnnouncementHarness() api.Harness { return announcementHarness{} }

func (announcementHarness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{
		ID: announcement.HarnessName, Version: "1",
		Tools:        []api.ToolSpec{{Name: announcement.ToolName, Description: "Record a local simulated announcement; nothing is sent or published", Mediation: api.MediationRequiresApproval}},
		Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay, ForkSafe: false},
	}, nil
}

func (announcementHarness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	if start == nil || len(start.Inputs) == 0 {
		return errors.New("announcement: snapshot input required")
	}
	snapshot, err := announcement.Decode([]byte(start.Inputs[len(start.Inputs)-1].Text()))
	if err != nil {
		return err
	}
	result, err := sink.ToolCall(ctx, api.ToolCall{
		ID: snapshot.Operation, Tool: announcement.ToolName, IdempotencyKey: snapshot.Operation,
		Mediation: api.MediationRequiresApproval, Args: announcement.Args(snapshot),
	})
	// In particular, preserve the SDK's typed park signal.
	if err != nil {
		return err
	}
	if result.IsError || result.Code != api.ToolResultCodeUnspecified {
		return errors.New("announcement: simulation did not succeed")
	}
	receipt, err := announcement.DecodeReceipt(result.Output)
	if err != nil || receipt.Snapshot != snapshot {
		return errors.New("announcement: invalid simulation receipt")
	}
	return nil
}

// Approval and its sequence bindings are enforced and stamped by the controller,
// not manufactured by this pure, safely repeatable executor.
func executeAnnouncement(_ context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
	if scope.SessionUID == "" || call.Tool != announcement.ToolName || call.Mediation != api.MediationRequiresApproval {
		return api.ToolResult{}, errors.New("announcement: invalid host tool context")
	}
	snapshot, err := announcement.FromArgs(call.Args)
	if err != nil {
		return api.ToolResult{}, err
	}
	if call.ID != snapshot.Operation || call.IdempotencyKey != snapshot.Operation {
		return api.ToolResult{}, errors.New("announcement: invalid operation binding")
	}
	return api.ToolResult{Output: map[string]any{"kind": announcement.Kind, "snapshot": announcement.Args(snapshot)}}, nil
}

func refuseAnnouncementModel(context.Context, api.ModelRequest) (api.ModelResponse, error) {
	return api.ModelResponse{}, errors.New("announcement: model calls are unavailable")
}
