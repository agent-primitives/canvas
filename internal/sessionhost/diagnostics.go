package sessionhost

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// The local Verify service reuses structpb, so it needs no upstream patch or
// generated proto, and never touches the live controller.
type diagnosticsServer interface {
	Verify(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

type diagnostics struct{ host *Host }

func registerDiagnostics(s *grpc.Server, h *Host) {
	s.RegisterService(&grpc.ServiceDesc{ServiceName: "canvas.local.Diagnostics", HandlerType: (*diagnosticsServer)(nil), Methods: []grpc.MethodDesc{{MethodName: "Verify", Handler: verifyHandler}}}, &diagnostics{host: h})
}
func verifyHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := new(structpb.Struct)
	if err := dec(req); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(diagnosticsServer).Verify(ctx, req)
	}
	return interceptor(ctx, req, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/canvas.local.Diagnostics/Verify"}, func(ctx context.Context, req any) (any, error) {
		return srv.(diagnosticsServer).Verify(ctx, req.(*structpb.Struct))
	})
}

func (d *diagnostics) Verify(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	h := d.host
	uid := req.GetFields()["session"].GetStringValue()
	if uid == "" || len(uid) > 256 {
		return nil, status.Error(codes.InvalidArgument, "bounded session required")
	}
	info, err := h.store.SessionInfo(uid)
	if err != nil {
		return nil, status.Error(codes.NotFound, "session unavailable")
	}
	var har api.Harness
	if bundle, ok := h.harnesses[info.Harness]; ok {
		har = bundle
	} else if info.Harness == announcement.HarnessName {
		har = announcementHarness{}
	}
	if har == nil {
		return nil, status.Error(codes.FailedPrecondition, "original contract unavailable")
	}
	// The store has no limited read, so Read loads the whole journal before
	// copyRecords applies its caps. The journal only grows, so a head already
	// past the record cap can be refused without reading it. The size cap still
	// needs a limited read upstream.
	log := h.store.Session(uid)
	if head, err := log.Head(); err != nil {
		return nil, status.Error(codes.Internal, "journal unavailable")
	} else if head > maxDiagnosticRecords {
		return nil, status.Error(codes.FailedPrecondition, "snapshot reconstruction unavailable")
	}
	// SQLite Read executes one ordered SELECT and holds its rows/connection until
	// fully consumed: all records belong to the same read snapshot. Head above is
	// only an early refusal; never combine it with this snapshot or mint a fence
	// on the live store.
	records, err := log.Read(1)
	if err != nil {
		return nil, status.Error(codes.Internal, "journal snapshot unavailable")
	}
	proof, err := verifyRecords(ctx, records, har)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "snapshot reconstruction unavailable")
	}
	models := make([]any, len(proof.RequestedModels))
	for i, m := range proof.RequestedModels {
		models[i] = m
	}
	fields := map[string]any{"chainValid": proof.ChainValid, "reconstructed": proof.Reconstructed, "outputsMatch": proof.OutputsMatch, "liveUnchanged": proof.LiveUnchanged, "modelCalls": proof.ModelCalls, "records": proof.Records, "head": strconv.FormatInt(proof.Head, 10), "requestedModels": models}
	if proof.ExecutorCalls != nil {
		fields["executorCalls"] = *proof.ExecutorCalls
		ids := make([]any, len(proof.ExecutionIDs))
		for i, id := range proof.ExecutionIDs {
			ids[i] = id
		}
		fields["executionIDs"] = ids
	}
	return structpb.NewStruct(fields)
}

const maxDiagnosticRecords = 20000

func copyRecords(source []eventlog.Record) ([]eventlog.Record, error) {
	if len(source) > maxDiagnosticRecords {
		return nil, errors.New("diagnostics: snapshot too large")
	}
	out := make([]eventlog.Record, len(source))
	total := 0
	for i, r := range source {
		pb := wire.EventToProto(r.Event)
		total += proto.Size(pb)
		if total > 64<<20 {
			return nil, errors.New("diagnostics: snapshot too large")
		}
		// NewFrom copies only the outer list, not nested event pointers. A protobuf
		// round trip isolates all messages, maps, opaque reasoning and byte buffers.
		blob, err := proto.Marshal(pb)
		if err != nil {
			return nil, errors.New("diagnostics: cannot copy event")
		}
		isolated := new(v1.Event)
		if err := proto.Unmarshal(blob, isolated); err != nil {
			return nil, errors.New("diagnostics: cannot copy event")
		}
		out[i] = r
		out[i].Event = wire.EventFromProto(isolated)
	}
	return out, nil
}

func verifyRecords(ctx context.Context, source []eventlog.Record, har api.Harness) (sessionclient.Proof, error) {
	var p sessionclient.Proof
	records, err := copyRecords(source)
	if err != nil {
		return p, err
	}
	log := eventlog.NewFrom(records)
	if err := log.Verify(); err != nil {
		return p, errors.New("diagnostics: invalid copied chain")
	}
	// Verify also contiguous sequence numbers: the upstream in-memory verifier
	// checks the hash chain but does not independently enforce this invariant.
	for i, r := range records {
		if r.Seq != int64(i+1) {
			return p, errors.New("diagnostics: invalid copied sequence")
		}
	}
	p.ChainValid = true
	p.Records = len(records)
	p.Head = log.Head()
	completed := map[string]bool{}
	modern := map[string]bool{}
	models := map[string]bool{}
	for _, r := range records {
		if r.Event.Kind == api.EventExecutionStart {
			modern[r.Event.ExecutionID] = true
		}
		if r.Event.Kind == api.EventEnd && r.Event.End != nil && r.Event.End.State == "COMPLETED" {
			completed[r.Event.ExecutionID] = true
		}
		if mc := r.Event.ModelCall; mc != nil {
			if len(mc.Model) == 0 || len(mc.Model) > 256 {
				return p, errors.New("diagnostics: invalid model identity")
			}
			models[mc.Model] = true
		}
	}
	if len(completed) == 0 {
		return p, errors.New("diagnostics: no completed execution")
	}
	if len(models) > 128 {
		return p, errors.New("diagnostics: too many model identities")
	}
	for m := range models {
		p.RequestedModels = append(p.RequestedModels, m)
	}
	sort.Strings(p.RequestedModels)
	var recorded [][]byte
	var texts []string
	for _, r := range records {
		if r.Event.Kind == api.EventOutput && completed[r.Event.ExecutionID] {
			if r.Event.Message == nil {
				return p, errors.New("diagnostics: output missing")
			}
			b, err := canonicalOutput(*r.Event.Message)
			if err != nil {
				return p, errors.New("diagnostics: invalid output")
			}
			recorded = append(recorded, b)
			texts = append(texts, r.Event.Message.Text())
		}
	}
	modelCalls := 0
	failOnCall := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		modelCalls++
		return api.ModelResponse{}, errors.New("diagnostics: live model forbidden")
	}
	// This constructor mints only the COPY'S fence. No live store/controller is
	// passed to reconstruction, so an ongoing live writer cannot be superseded.
	executorCalls := 0
	c, err := controller.New(eventlog.AsStore(log), failOnCall, controller.WithSessionUID("diagnostics-copy"), controller.WithToolExecutor(func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		executorCalls++
		return api.ToolResult{}, errors.New("diagnostics: live executor forbidden")
	}))
	if err != nil {
		return p, errors.New("diagnostics: cannot reconstruct")
	}
	capture := &captureHarness{inner: har}
	outputs, err := c.Replay(ctx, capture)
	p.ModelCalls = modelCalls + c.ModelInvocations()
	if err != nil || p.ModelCalls != 0 || executorCalls != 0 {
		return p, errors.New("diagnostics: reconstruction failed")
	}
	if len(outputs) != len(texts) || len(recorded) != len(capture.outputs) {
		return p, errors.New("diagnostics: reconstructed output count mismatch")
	}
	for i, b := range recorded {
		if outputs[i] != texts[i] || !bytes.Equal(b, capture.outputs[i]) {
			return p, errors.New("diagnostics: reconstructed output mismatch")
		}
	}
	// Older journals retain their existing proof without claiming modern execution coverage.
	expected := make([]string, 0, len(completed))
	allModern := true
	for id := range completed {
		expected = append(expected, id)
		allModern = allModern && modern[id]
	}
	if allModern {
		sort.Strings(expected)
		sort.Strings(capture.executions)
		if !reflect.DeepEqual(expected, capture.executions) {
			return p, errors.New("diagnostics: execution coverage mismatch")
		}
		p.ExecutorCalls = &executorCalls
		p.ExecutionIDs = expected
	}
	p.Reconstructed = true
	p.OutputsMatch = true
	p.LiveUnchanged = true
	return p, nil
}

func canonicalOutput(msg api.Message) ([]byte, error) {
	return canon.Event(api.Event{Kind: api.EventOutput, Message: &msg})
}

type captureHarness struct {
	inner      api.Harness
	outputs    [][]byte
	executions []string
}

func (h *captureHarness) Describe(ctx context.Context) (api.Descriptor, error) {
	return h.inner.Describe(ctx)
}
func (h *captureHarness) Run(ctx context.Context, s *api.Start, sink api.EventSink) error {
	h.executions = append(h.executions, s.ExecutionID)
	return h.inner.Run(ctx, s, &captureSink{EventSink: sink, harness: h})
}

type captureSink struct {
	api.EventSink
	harness *captureHarness
}

func (s *captureSink) Model(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
	resp, err := s.EventSink.Model(ctx, req)
	if err != nil {
		return resp, err
	}
	b, err := canonicalOutput(resp.Message)
	if err != nil {
		return api.ModelResponse{}, err
	}
	s.harness.outputs = append(s.harness.outputs, b)
	return resp, nil
}
