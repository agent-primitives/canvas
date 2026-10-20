package sessionhost

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func bundle(t *testing.T, instructions, model string) contract.Bundle {
	t.Helper()
	b, err := contract.Parse([]byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: scouting\nspec:\n  instructions: '" + instructions + "'\n  model:\n    name: " + model + "\n"))
	if err != nil {
		t.Fatal("fixture bundle parse failed")
	}
	return b
}
func connect(t *testing.T, h *Host) v1.SessionsClient {
	t.Helper()
	c, err := grpc.NewClient(h.Address(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal("connect failed")
	}
	t.Cleanup(func() { c.Close() })
	return v1.NewSessionsClient(c)
}
func create(t *testing.T, c v1.SessionsClient, b contract.Bundle) *v1.Session {
	t.Helper()
	s, err := c.CreateSession(context.Background(), &v1.CreateSessionRequest{Session: &v1.Session{Harness: "bundle-" + b.Digest, Model: "metadata-not-routing", Metadata: &v1.ResourceMetadata{Project: "draft", Name: "fixture"}, Labels: map[string]string{"kind": "scout"}}})
	if err != nil {
		t.Fatal("create failed")
	}
	return s
}
func exec(t *testing.T, c v1.SessionsClient, uid, text string) []*v1.LogRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	records, err := run(ctx, c, uid, text)
	if err != nil {
		t.Fatal("exec failed")
	}
	return records
}
func run(ctx context.Context, c v1.SessionsClient, uid, text string) ([]*v1.LogRecord, error) {
	s, err := c.Exec(ctx, &v1.ExecRequest{Session: uid, Inputs: []*v1.Message{wire.MessageToProto(api.TextMessage("user", text))}, Config: []byte("not-journaled")})
	if err != nil {
		return nil, err
	}
	var records []*v1.LogRecord
	for {
		u, err := s.Recv()
		if err == io.EOF {
			return records, nil
		}
		if err != nil {
			return records, err
		}
		if r := u.GetRecord(); r != nil {
			records = append(records, r)
		}
	}
}
func TestGRPCConversationForkAndProof(t *testing.T) {
	b := bundle(t, "immutable instructions", "fixture-model")
	var mu sync.Mutex
	var requests []api.ModelRequest
	provider := func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		mu.Lock()
		requests = append(requests, r)
		mu.Unlock()
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"dossier":"fictional","rank":4,"tier":2,"rationale":"fixture"}`)}, nil
	}
	root := t.TempDir()
	h, err := Start(root, []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal("start failed")
	}
	t.Cleanup(func() { h.Close() })
	if !strings.HasPrefix(h.Address(), "127.0.0.1:") {
		t.Fatal("host not loopback")
	}
	c := connect(t, h)
	s := create(t, c, b)
	uid := s.GetMetadata().GetUid()
	records := exec(t, c, uid, `{"mode":"scout"}`)
	outputs := 0
	for _, r := range records {
		if r.GetEvent().GetKind() == v1.EventKind_EVENT_OUTPUT {
			outputs++
		}
	}
	if outputs != 1 {
		t.Fatal("completion must be recorded exactly once")
	}
	got, err := c.GetSession(context.Background(), &v1.GetSessionRequest{Uid: uid})
	if err != nil || got.GetHarness() != "bundle-"+b.Digest || got.GetLabels()["kind"] != "scout" {
		t.Fatal("metadata did not round trip")
	}
	fork, err := c.Fork(context.Background(), &v1.ForkRequest{Session: uid, AtSeq: got.GetLastSeq(), Count: 2, ChildNames: []string{"up", "stay"}, Labels: map[string]string{"kind": "scenario"}})
	if err != nil {
		t.Fatal("fork failed")
	}
	child := fork.GetChildren()[0]
	if child.GetParentUid() != uid || child.GetForkSeq() != got.GetLastSeq() || child.GetHarness() != s.GetHarness() {
		t.Fatal("fork lineage lost")
	}
	exec(t, c, child.GetMetadata().GetUid(), `{"mode":"scenario","scenario":"trade up"}`)
	mu.Lock()
	if len(requests) != 2 {
		t.Fatal("wrong model invocation count")
	}
	for i, r := range requests {
		if r.Model != b.Model || r.Params["max_tokens"] != "700" || r.Params["temperature"] != "0.2" {
			t.Fatal("routing or generation bounds lost")
		}
		if r.Messages[0].Role != "system" || !strings.Contains(r.Messages[0].Text(), b.Instructions) {
			t.Fatal("contract instructions missing")
		}
		if !strings.Contains(r.Messages[0].Text(), "/no_think") {
			t.Fatal("qwen non-thinking directive missing")
		}
		if i == 0 && len(r.Messages) != 2 || i == 1 && len(r.Messages) != 4 {
			t.Fatal("history or current inputs duplicated")
		}
		current := r.Messages[len(r.Messages)-1]
		if current.Role != "user" || i == 0 && current.Text() != `{"mode":"scout"}` || i == 1 && current.Text() != `{"mode":"scenario","scenario":"trade up"}` {
			t.Fatal("current journaled input was changed")
		}
	}
	mu.Unlock()
	p, err := sessionclient.Verify(context.Background(), h.Address(), child.GetMetadata().GetUid())
	if err != nil {
		t.Fatal("verify failed")
	}
	if !p.ChainValid || !p.Reconstructed || !p.OutputsMatch || !p.LiveUnchanged || p.ModelCalls != 0 || p.Records == 0 || p.Head == 0 || len(p.RequestedModels) != 1 || p.RequestedModels[0] != b.Model {
		t.Fatal("proof incomplete")
	}
	fi, err := os.Stat(filepath.Join(root, "contracts", b.Digest, "agent.yaml"))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("archive not private")
	}
}
func TestRestartUsesArchivedOriginalContract(t *testing.T) {
	root := t.TempDir()
	old := bundle(t, "original instructions", "original-model")
	newer := bundle(t, "changed instructions", "changed-model")
	provider := func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"fixture"}`)}, nil
	}
	h, err := Start(root, []contract.Bundle{old}, provider)
	if err != nil {
		t.Fatal("start failed")
	}
	c := connect(t, h)
	s := create(t, c, old)
	uid := s.GetMetadata().GetUid()
	exec(t, c, uid, `{"mode":"scout"}`)
	if h.Close() != nil {
		t.Fatal("close failed")
	}
	h, err = Start(root, []contract.Bundle{newer}, provider)
	if err != nil {
		t.Fatal("restart failed")
	}
	restarted := h
	t.Cleanup(func() { restarted.Close() })
	p, err := sessionclient.Verify(context.Background(), h.Address(), uid)
	if err != nil || !p.Reconstructed || !p.OutputsMatch || len(p.RequestedModels) != 1 || p.RequestedModels[0] != old.Model {
		t.Fatal("old contract was not preserved")
	}
	c = connect(t, h)
	exec(t, c, uid, `{"mode":"scenario"}`)
	if err := h.Close(); err != nil {
		t.Fatal("close failed")
	}
	path := filepath.Join(root, "contracts", old.Digest, "agent.yaml")
	if os.WriteFile(path, newer.Source, 0600) != nil {
		t.Fatal("tamper fixture failed")
	}
	if h, err = Start(root, []contract.Bundle{newer}, provider); err == nil {
		h.Close()
		t.Fatal("archive digest tampering accepted")
	}
}
func TestVerifyDoesNotFenceBlockedLiveTurn(t *testing.T) {
	b := bundle(t, "fixture instructions", "fixture-model")
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int
	provider := func(ctx context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		calls++
		if calls == 2 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return api.ModelResponse{}, ctx.Err()
			}
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"fixture"}`)}, nil
	}
	h, err := Start(t.TempDir(), []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal("start failed")
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	uid := create(t, c, b).GetMetadata().GetUid()
	exec(t, c, uid, `{"mode":"scout"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := run(ctx, c, uid, `{"mode":"scenario"}`); done <- e }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("live provider did not block")
	}
	p, err := sessionclient.Verify(ctx, h.Address(), uid)
	close(release)
	if err != nil || !p.Reconstructed || !p.LiveUnchanged || p.ModelCalls != 0 {
		t.Fatal("completed previous turn not verifiable")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("verifier fenced the live writer")
		}
	case <-ctx.Done():
		t.Fatal("writer did not finish")
	}
}
func TestIncompleteModelIntentRequiresExplicitResumeAndSanitizesErrors(t *testing.T) {
	b := bundle(t, "fixture instructions", "fixture-model")
	var calls int
	provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{}, errors.New("sensitive provider payload")
	}
	h, err := Start(t.TempDir(), []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal("start failed")
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	uid := create(t, c, b).GetMetadata().GetUid()
	if _, err := run(context.Background(), c, uid, `{"mode":"scout"}`); err == nil {
		t.Fatal("provider failure accepted")
	}
	if _, err := sessionclient.Verify(context.Background(), h.Address(), uid); err == nil {
		t.Fatal("incomplete intent claimed reconstruction")
	}
	if calls != 1 {
		t.Fatal("read-only verification re-invoked the model")
	}
	for wantCalls := 2; wantCalls <= 3; wantCalls++ {
		if _, err := c.Resume(context.Background(), &v1.ResumeRequest{Session: uid}); err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("explicit Resume must return a sanitized provider failure: %v", err)
		}
		if calls != wantCalls {
			t.Fatalf("explicit Resume model calls = %d, want %d", calls, wantCalls)
		}
	}
	records, err := h.store.Session(uid).Read(1)
	if err != nil {
		t.Fatal("read failed")
	}
	for _, r := range records {
		if r.Event.Err != nil && strings.Contains(r.Event.Err.Description, "sensitive") {
			t.Fatal("provider payload leaked into ERROR")
		}
	}
}
func TestExplicitResumeAfterRestartReissuesOnlyUnansweredModel(t *testing.T) {
	b := bundle(t, "fixture instructions", "fixture-model")
	var requests []api.ModelRequest
	provider := func(_ context.Context, request api.ModelRequest) (api.ModelResponse, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return api.ModelResponse{}, errors.New("sensitive provider payload")
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"recovered"}`)}, nil
	}
	root := t.TempDir()
	h, err := Start(root, []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal(err)
	}
	c := connect(t, h)
	uid := create(t, c, b).GetMetadata().GetUid()
	if _, err := run(t.Context(), c, uid, `{"mode":"scout"}`); err == nil {
		t.Fatal("fixture must leave an unanswered model intent")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Start(root, []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	c = connect(t, h)
	if _, err := sessionclient.Verify(t.Context(), h.Address(), uid); err == nil {
		t.Fatal("incomplete intent claimed reconstruction")
	}
	if len(requests) != 1 {
		t.Fatal("restart or read-only verification reissued model request")
	}
	if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: uid}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("explicit Resume did not reissue exactly the original request")
	}
	records, err := h.store.Session(uid).Read(1)
	if err != nil {
		t.Fatal(err)
	}
	models, outputs, ends := 0, 0, 0
	for _, record := range records {
		switch record.Event.Kind {
		case api.EventModelCall:
			models++
		case api.EventOutput:
			outputs++
			if record.Event.Message.Text() != `{"strategy":"recovered"}` {
				t.Fatal("recovered output lost")
			}
		case api.EventEnd:
			ends++
		}
	}
	if models != 1 || outputs != 1 || ends != 1 {
		t.Fatalf("recovery duplicated intent or completion: models=%d outputs=%d ends=%d", models, outputs, ends)
	}
	if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: uid}); err != nil {
		t.Fatal(err)
	}
	proof, err := sessionclient.Verify(t.Context(), h.Address(), uid)
	if err != nil || !proof.Reconstructed || !proof.OutputsMatch || proof.ModelCalls != 0 || len(requests) != 2 {
		t.Fatalf("completed Resume/reconstruction invoked provider or failed: %+v %v", proof, err)
	}
}

func TestCopiedRecordTamperAndAliasing(t *testing.T) {
	b := bundle(t, "fixture instructions", "fixture-model")
	provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"fixture"}`)}, nil
	}
	h, err := Start(t.TempDir(), []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal("start failed")
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	uid := create(t, c, b).GetMetadata().GetUid()
	exec(t, c, uid, `{"mode":"scout"}`)
	records, err := h.store.Session(uid).Read(1)
	if err != nil {
		t.Fatal("read failed")
	}
	cp, err := copyRecords(records)
	if err != nil {
		t.Fatal("copy failed")
	}
	input := -1
	for i, record := range cp {
		if record.Event.Kind == api.EventInput {
			input = i
			break
		}
	}
	if input < 0 {
		t.Fatal("fixture has no INPUT record")
	}
	cp[input].Event.Message.Parts[0].Text.Text = "tampered"
	if records[input].Event.Message.Text() == "tampered" {
		t.Fatal("snapshot aliases source")
	}
	if eventlog.NewFrom(cp).Verify() == nil {
		t.Fatal("copy chain tampering accepted")
	}
	if _, err := verifyRecords(context.Background(), cp, bundleHarness{bundle: b}); err == nil {
		t.Fatal("diagnostic accepted tampered chain")
	}
	// A complete chain with changed instructions still must fail deterministic reconstruction.
	wrong := bundle(t, "different instructions", "fixture-model")
	if _, err := verifyRecords(context.Background(), records, bundleHarness{bundle: wrong}); err == nil {
		t.Fatal("instruction divergence accepted")
	}
}
