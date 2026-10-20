// Package sessionclient exposes optional reconstruction evidence over gRPC only.
package sessionclient

import (
	"context"
	"errors"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// Proof refers to a copied journal snapshot. LiveUnchanged means the verifier
// never mutated the live journal or its fence, not that another writer stood still.
// Reconstruction covers completed executions only; Head and Records describe
// the entire copied source snapshot, including any ongoing execution.
type Proof struct {
	ChainValid    bool `json:"chainValid"`
	Reconstructed bool `json:"reconstructed"`
	OutputsMatch  bool `json:"outputsMatch"`
	LiveUnchanged bool `json:"liveUnchanged"`
	ModelCalls    int  `json:"modelCalls"`
	// Nil means the older host supplied no executor or execution-coverage evidence.
	ExecutorCalls   *int     `json:"executorCalls,omitempty"`
	ExecutionIDs    []string `json:"executionIDs,omitempty"`
	Records         int      `json:"records"`
	Head            int64    `json:"head"`
	RequestedModels []string `json:"requestedModels"`
}

// Verify asks the host's optional diagnostics service for bounded evidence.
// An absent diagnostics service is an error, never synthetic passing evidence.
func Verify(ctx context.Context, address, uid string) (Proof, error) {
	if address == "" || uid == "" || len(uid) > 256 {
		return Proof{}, errors.New("diagnostics: address and bounded session are required")
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return Proof{}, err
	}
	defer conn.Close()
	request := &structpb.Struct{Fields: map[string]*structpb.Value{"session": structpb.NewStringValue(uid)}}
	reply := new(structpb.Struct)
	if err := conn.Invoke(ctx, "/canvas.local.Diagnostics/Verify", request, reply, grpc.MaxCallRecvMsgSize(64<<10)); err != nil {
		return Proof{}, err
	}
	return decodeProof(reply)
}

func decodeProof(reply *structpb.Struct) (Proof, error) {
	var p Proof
	bad := errors.New("diagnostics: malformed proof")
	fields := reply.GetFields()
	flags := []struct {
		name   string
		target *bool
	}{{"chainValid", &p.ChainValid}, {"reconstructed", &p.Reconstructed}, {"outputsMatch", &p.OutputsMatch}, {"liveUnchanged", &p.LiveUnchanged}}
	for _, f := range flags {
		v, ok := fields[f.name]
		if !ok {
			return Proof{}, bad
		}
		b, ok := v.Kind.(*structpb.Value_BoolValue)
		if !ok {
			return Proof{}, bad
		}
		*f.target = b.BoolValue
	}
	for name, target := range map[string]*int{"modelCalls": &p.ModelCalls, "records": &p.Records} {
		v, ok := fields[name]
		if !ok {
			return Proof{}, bad
		}
		n, ok := v.Kind.(*structpb.Value_NumberValue)
		if !ok || n.NumberValue < 0 || n.NumberValue > 20000 || n.NumberValue != float64(int(n.NumberValue)) {
			return Proof{}, bad
		}
		*target = int(n.NumberValue)
	}
	// int64 is a string on the wire, avoiding Struct's float64 precision loss.
	hv, ok := fields["head"]
	if !ok {
		return Proof{}, bad
	}
	hs, ok := hv.Kind.(*structpb.Value_StringValue)
	if !ok {
		return Proof{}, bad
	}
	head, err := strconv.ParseInt(hs.StringValue, 10, 64)
	if err != nil || head < 0 {
		return Proof{}, bad
	}
	p.Head = head
	models, ok := fields["requestedModels"]
	if !ok {
		return Proof{}, bad
	}
	list, ok := models.Kind.(*structpb.Value_ListValue)
	if !ok || len(list.ListValue.Values) > 128 {
		return Proof{}, bad
	}
	for _, v := range list.ListValue.Values {
		s, ok := v.Kind.(*structpb.Value_StringValue)
		if !ok || len(s.StringValue) == 0 || len(s.StringValue) > 256 {
			return Proof{}, bad
		}
		p.RequestedModels = append(p.RequestedModels, s.StringValue)
	}
	executor, hasExecutor := fields["executorCalls"]
	ids, hasIDs := fields["executionIDs"]
	if hasExecutor != hasIDs {
		return Proof{}, bad
	}
	if hasExecutor {
		n, ok := executor.Kind.(*structpb.Value_NumberValue)
		if !ok || n.NumberValue < 0 || n.NumberValue > 20000 || n.NumberValue != float64(int(n.NumberValue)) {
			return Proof{}, bad
		}
		count := int(n.NumberValue)
		p.ExecutorCalls = &count
		list, ok := ids.Kind.(*structpb.Value_ListValue)
		if !ok || len(list.ListValue.Values) == 0 || len(list.ListValue.Values) > 20000 {
			return Proof{}, bad
		}
		seen := map[string]bool{}
		for _, value := range list.ListValue.Values {
			s, ok := value.Kind.(*structpb.Value_StringValue)
			if !ok || len(s.StringValue) == 0 || len(s.StringValue) > 256 || seen[s.StringValue] {
				return Proof{}, bad
			}
			seen[s.StringValue] = true
			p.ExecutionIDs = append(p.ExecutionIDs, s.StringValue)
		}
	}
	return p, nil
}
