package canvas

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
)

func TestNativeSessionIdentityMissingOrMismatchedBlocksExecution(t *testing.T) {
	for _, kind := range []string{"missing", "mismatched"} {
		t.Run(kind, func(t *testing.T) {
			c, close := setup(t, false)
			defer close()
			hooked, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				err := invoke(ctx, method, req, reply, cc, opts...)
				if err == nil && method == "/agentsessions.v1.Sessions/GetSession" {
					s := reply.(*v1.Session)
					if kind == "missing" {
						delete(s.Annotations, "bundle-digest")
					} else {
						s.Annotations["bundle-digest"] = strings.Repeat("a", 64)
					}
				}
				return err
			})))
			if err != nil {
				t.Fatal(err)
			}
			defer hooked.Close()
			c.client = hooked
			id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			r := completed(t, c, id)
			if r.Status != "failed" || r.Proposal != "" || !strings.Contains(r.Error, "bundle identity") {
				t.Fatal("unproven identity did not fail closed")
			}
			s, err := hooked.GetSession(context.Background(), r.Session)
			if err != nil || s.GetLastSeq() != 0 {
				t.Fatal("unproven identity launched execution")
			}
		})
	}
}

// Historical contracts are exercised directly through Sessions, never activated
// as a current Canvas workspace. Native Canvas cannot construct legacy fanouts.
func TestArchivedSessionForksKeepProfileAndReplayWithoutModelCalls(t *testing.T) {
	for _, profile := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			root := t.TempDir()
			parse := func(model, instructions string) contract.Bundle {
				b, err := contract.Parse([]byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: compatibility-reader}\nspec:\n  instructions: " + instructions + "\n  model: {name: " + model + "}\n"))
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			old := parse("old-test-model", "Archived synthetic instructions.")
			current := parse("new-test-model", "Current synthetic instructions.")
			var calls atomic.Int32
			provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
				calls.Add(1)
				if req.Model != old.Model || !strings.HasPrefix(req.Messages[0].Text(), old.Instructions) || (req.Params["response_format"] != "") != (profile >= 2) {
					t.Error("archived execution lost model/instructions/profile")
				}
				return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"Recorded synthetic response; no document producer."}`)}, nil
			}
			h, err := sessionhost.Start(filepath.Join(root, "host"), []contract.Bundle{old}, provider)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { h.Close() }()
			cl, err := client.Dial(h.Address(), client.WithProject("compatibility-reader"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { cl.Close() }()
			s, err := cl.CreateSession(context.Background(), &v1.Session{Harness: contract.HarnessName(old.Digest, profile), Model: old.Model, Annotations: map[string]string{"bundle-digest": old.Digest}})
			if err != nil {
				t.Fatal(err)
			}
			turn, err := cl.Exec(context.Background(), client.ExecOptions{Session: s.GetMetadata().GetUid(), Inputs: []string{`{"mode":"scout"}`}})
			if err != nil || turn.Output == "" {
				t.Fatalf("historical fixture: %v", err)
			}
			s, err = cl.GetSession(context.Background(), s.GetMetadata().GetUid())
			if err != nil {
				t.Fatal(err)
			}
			cl.Close()
			h.Close()
			h, err = sessionhost.Start(filepath.Join(root, "host"), []contract.Bundle{current}, provider)
			if err != nil {
				t.Fatal(err)
			}
			cl, err = client.Dial(h.Address(), client.WithProject("compatibility-reader"))
			if err != nil {
				t.Fatal(err)
			}
			children, err := cl.Sessions().Fork(context.Background(), &v1.ForkRequest{Session: s.GetMetadata().GetUid(), AtSeq: s.GetLastSeq(), Count: 2, ChildNames: []string{"first", "second"}})
			if err != nil || len(children.GetChildren()) != 2 {
				t.Fatalf("archived fork: %v", err)
			}
			for _, child := range children.GetChildren() {
				if child.GetHarness() != contract.HarnessName(old.Digest, profile) || child.GetAnnotations()["bundle-digest"] != old.Digest || child.GetParentUid() != s.GetMetadata().GetUid() || child.GetForkSeq() != s.GetLastSeq() {
					t.Fatal("fork inherited current identity or lost checkpoint")
				}
				if _, err := cl.Exec(context.Background(), client.ExecOptions{Session: child.GetMetadata().GetUid(), Inputs: []string{`{"mode":"scenario","scenario":"synthetic"}`}}); err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				proof, err := sessionclient.Verify(context.Background(), h.Address(), child.GetMetadata().GetUid())
				if err != nil || !proof.Reconstructed || !proof.OutputsMatch || !proof.ChainValid || proof.ModelCalls != 0 || calls.Load() != before {
					t.Fatalf("historical zero-call reconstruction: %+v %v", proof, err)
				}
			}
			if calls.Load() != 3 {
				t.Fatal("fork/replay dispatched extra model work")
			}
		})
	}
}
