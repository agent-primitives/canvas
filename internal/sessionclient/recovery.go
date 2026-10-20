package sessionclient

import (
	"context"
	"errors"
	"io"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// ReadRecovery reads exactly the inspected head, never a moving/unbounded tail.
// The limits apply before retaining records or interpreting recovery authority.
func ReadRecovery(ctx context.Context, sessions v1.SessionsClient, uid string, head int64) ([]*v1.LogRecord, error) {
	bad := errors.New("recovery evidence unavailable or invalid")
	if uid == "" || head < 1 || head > 4096 {
		return nil, bad
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := sessions.Replay(ctx, &v1.ReplayRequest{Session: uid, FromSeq: 1, ToSeq: head}, grpc.MaxCallRecvMsgSize(8<<20))
	if err != nil {
		return nil, bad
	}
	var records []*v1.LogRecord
	size := 0
	previous := ""
	for {
		r, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if int64(len(records)) != head {
				return nil, bad
			}
			return records, nil
		}
		if err != nil || r == nil || len(records) >= 4096 {
			return nil, bad
		}
		size += proto.Size(r)
		if size > 8<<20 || r.GetSeq() != int64(len(records)+1) || r.GetSeq() > head || r.GetPrevHash() != previous || r.GetFence() < 0 || len(r.ProtoReflect().GetUnknown()) != 0 {
			return nil, bad
		}
		event := r.GetEvent()
		if event == nil || !proto.Equal(event, wire.EventToProto(wire.EventFromProto(event))) {
			return nil, bad
		}
		hash, err := canon.HashRecord(previous, r.GetSeq(), wire.EventFromProto(event))
		if err != nil || hash != r.GetContentHash() {
			return nil, bad
		}
		previous = hash
		records = append(records, r)
	}
}
