package sessionclient

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
)

type recoveryServer struct {
	v1.UnimplementedSessionsServer
	records []*v1.LogRecord
	t       *testing.T
	head    int64
}

func (s *recoveryServer) Replay(r *v1.ReplayRequest, stream grpc.ServerStreamingServer[v1.LogRecord]) error {
	if r.FromSeq != 1 || r.ToSeq != s.head {
		s.t.Errorf("unbounded/wrong read: %+v", r)
	}
	for _, record := range s.records {
		if err := stream.Send(record); err != nil {
			return err
		}
	}
	return nil
}
func recoveryRecords(t *testing.T, n int) []*v1.LogRecord {
	t.Helper()
	log := eventlog.New()
	out := make([]*v1.LogRecord, 0, n)
	for i := 0; i < n; i++ {
		r, err := log.Append(int64(i), 0, api.Event{ExecutionID: "e", Kind: api.EventError, Err: &api.Error{Description: "failure"}})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &v1.LogRecord{Seq: r.Seq, PrevHash: r.PrevHash, ContentHash: r.Hash, Fence: r.Fence, Event: wire.EventToProto(r.Event)})
	}
	return out
}
func TestRecoveryReadBoundsAndVerifiesEvidence(t *testing.T) {
	for _, kind := range []string{"valid", "truncated", "gap", "hash", "unknown", "oversize", "aggregate", "records", "record-limit"} {
		t.Run(kind, func(t *testing.T) {
			records := recoveryRecords(t, 3)
			head := int64(3)
			switch kind {
			case "truncated":
				records = records[:2]
			case "gap":
				records[1].Seq = 3
			case "hash":
				records[1].ContentHash = strings.Repeat("b", 64)
			case "unknown":
				records[1].Event.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 1})
			case "oversize":
				records[1].Event.GetError().Description = strings.Repeat("x", 8<<20)
			case "aggregate":
				for _, r := range records {
					r.Event.GetError().Description = strings.Repeat("x", 3<<20)
				}
			case "records":
				head = 4097
				records = nil
			case "record-limit":
				head = 4096
				records = recoveryRecords(t, 4096)
			}
			if kind == "aggregate" || kind == "oversize" {
				previous := ""
				for _, r := range records {
					r.PrevHash = previous
					r.ContentHash, _ = canon.HashRecord(previous, r.Seq, wire.EventFromProto(r.Event))
					previous = r.ContentHash
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			v1.RegisterSessionsServer(server, &recoveryServer{records: records, t: t, head: head})
			go server.Serve(listener)
			defer server.Stop()
			cl, err := client.Dial(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			got, err := ReadRecovery(context.Background(), cl.Sessions(), "session", head)
			if kind == "valid" || kind == "record-limit" {
				if err != nil || int64(len(got)) != head {
					t.Fatalf("valid read: %d %v", len(got), err)
				}
			} else if err == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
}
