package canvas

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

// A Verify snapshot predating Resume may arrive after Resume has fully finished,
// when busy=false again. Polling from another tab must not resurrect that proof.
func TestResumeRejectsDelayedVerifyAcrossFullRecoveryAndPolling(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "question"}[question], func(t *testing.T) {
			c, _, fail, cleanup := resumeCanvas(t, question)
			defer cleanup()
			id, err := c.Manual("writer", "", "Assess synthetic evidence.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			completed(t, c, id)
			message := ""
			if question {
				fail.Store(true)
				message, err = c.Ask(id, "Why this rank?", "coach")
				if err != nil {
					t.Fatal(err)
				}
				awaitQuestion(t, c, message)
			}
			c.wg.Wait()
			if err := c.update(id, func(a *attempt) { a.Proof = &sessionclient.Proof{ChainValid: true} }); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				request := new(structpb.Struct)
				if err := stream.RecvMsg(request); err != nil {
					return err
				}
				close(started)
				<-release
				proof, _ := structpb.NewStruct(map[string]any{"chainValid": true, "reconstructed": true, "outputsMatch": true, "liveUnchanged": true, "modelCalls": 0, "records": 4, "head": "4", "requestedModels": []any{"fixture"}})
				return stream.SendMsg(proof)
			}))
			go server.Serve(listener)
			defer server.Stop()
			c.address = listener.Addr().String()
			result := make(chan error, 1)
			go func() { _, err := c.Verify(context.Background(), id); result <- err }()
			<-started
			inspection := c.InspectResume(context.Background(), id, message)
			if !inspection.Available {
				t.Fatalf("inspection: %+v", inspection)
			}
			fail.Store(false)
			if err := c.ConfirmResume(context.Background(), id, message, "GM", confirmationFor(inspection)); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			for n := 0; n < 2; n++ {
				state, err := c.State()
				if err != nil {
					t.Fatal(err)
				}
				for _, run := range state.Runs {
					if run.ID == id && (run.SessionVersion <= inspection.SessionVersion || run.Proof != nil) {
						t.Fatalf("poll observed stale version/proof: %+v", run)
					}
				}
			}
			close(release)
			if err := <-result; !errors.Is(err, ErrQuestionBusy) {
				t.Fatalf("delayed pre-Resume proof accepted: %v", err)
			}
			run, _ := c.GetRun(id)
			if run.Proof != nil {
				t.Fatal("late Verify resurrected proof")
			}
		})
	}
}
