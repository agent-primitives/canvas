package sessionhost

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
)

const questionSchemaFixture = `{"json_schema":{"name":"draft_room_question","schema":{"additionalProperties":false,"properties":{"answer":{"maxLength":3000,"minLength":1,"type":"string"}},"required":["answer"],"type":"object"},"strict":true},"type":"json_schema"}`

func TestProfile3QuestionUsesSameSessionOriginalContextAndZeroCallReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	old := bundle(t, "Explain synthetic evidence only.", "synthetic-original-model")
	current := bundle(t, "Updated synthetic instructions.", "synthetic-current-model")
	originalInput := `{"mode":"scout","snapshot":{"board":"Synthetic accepted board"}}`
	originalOutput := `{"dossier":"Synthetic assessment with explicitly missing evidence.","rank":4,"tier":2,"rationale":"Synthetic unaccepted recommendation."}`
	questionInput := `{"mode":"question","request":"Why this synthetic recommendation?","acceptedContext":{"board":"Synthetic accepted board"},"proposalContext":{"status":"unaccepted"}}`
	answerOutput := `{"answer":"Synthetic explanation: the earlier recommendation is unaccepted; the accepted board is unchanged."}`
	requests := make(chan api.ModelRequest, 3)
	var calls atomic.Int64
	provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		requests <- req
		output := originalOutput
		if req.Messages[len(req.Messages)-1].Text() == questionInput {
			output = answerOutput
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", output)}, nil
	}
	h, err := Start(root, []contract.Bundle{old}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { h.Close() }()
	c := connect(t, h)
	// An omitted harness must pick profile 3, independently of LocalBinder.
	s, err := c.CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{}})
	if err != nil {
		t.Fatal(err)
	}
	if s.GetHarness() != "bundle-v3-"+old.Digest {
		t.Fatal("host default is not profile 3")
	}
	uid := s.GetMetadata().GetUid()
	exec(t, c, uid, originalInput)
	first := <-requests
	if first.Params["response_format"] != frozenScoutSchema {
		t.Fatal("profile 3 changed ordinary assessment schema")
	}
	before, err := sessionclient.Verify(ctx, h.Address(), uid)
	if err != nil || !before.ChainValid || !before.Reconstructed || !before.OutputsMatch || before.ModelCalls != 0 || calls.Load() != 1 {
		t.Fatal("original profile 3 proof failed")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Start(root, []contract.Bundle{current}, provider)
	if err != nil {
		t.Fatal(err)
	}
	c = connect(t, h)
	fresh, err := c.CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{}})
	if err != nil || fresh.GetHarness() != "bundle-v3-"+current.Digest {
		t.Fatal("restart did not default new sessions to the current profile 3 bundle")
	}
	records := exec(t, c, uid, questionInput)
	second := <-requests
	if second.Model != old.Model || len(second.Messages) != 4 || second.Messages[0].Text() != first.Messages[0].Text() || second.Messages[1].Text() != originalInput || second.Messages[2].Text() != originalOutput || second.Messages[3].Text() != questionInput {
		t.Fatal("question lost original immutable context or duplicated inputs")
	}
	if second.Params["response_format"] != questionSchemaFixture || second.Params["max_tokens"] != "700" || second.Params["temperature"] != "0.2" {
		t.Fatal("question does not request bounded answer-only JSON")
	}
	for _, guidance := range []string{"read-only", "accepted", "unaccepted", "Do not invent", "tools"} {
		if !strings.Contains(second.Messages[0].Text(), guidance) {
			t.Errorf("question instruction lacks %q", guidance)
		}
	}
	outputs := 0
	for _, r := range records {
		if r.GetEvent().GetKind() == v1.EventKind_EVENT_OUTPUT {
			outputs++
			if r.GetEvent().GetMessage().GetParts()[0].GetText().GetText() != answerOutput {
				t.Fatal("answer changed before journaling")
			}
		}
	}
	if outputs != 1 {
		t.Fatal("answer must be journaled once")
	}
	afterSession, err := c.GetSession(ctx, &v1.GetSessionRequest{Uid: uid})
	if err != nil || afterSession.GetHarness() != s.GetHarness() || afterSession.GetMetadata().GetUid() != uid || afterSession.GetLastSeq() <= before.Head {
		t.Fatal("question rebound or replaced the original session")
	}
	after, err := sessionclient.Verify(ctx, h.Address(), uid)
	if err != nil || !after.ChainValid || !after.Reconstructed || !after.OutputsMatch || !after.LiveUnchanged || after.ModelCalls != 0 || after.Head <= before.Head || calls.Load() != 2 {
		t.Fatal("question proof failed or called live provider")
	}
	fork, err := c.Fork(ctx, &v1.ForkRequest{Session: uid, AtSeq: after.Head, Count: 1})
	if err != nil || fork.GetChildren()[0].GetHarness() != s.GetHarness() {
		t.Fatal("profile 3 fork lost archived binding")
	}
	proof, err := sessionclient.Verify(ctx, h.Address(), fork.GetChildren()[0].GetMetadata().GetUid())
	if err != nil || !proof.Reconstructed || !proof.OutputsMatch || proof.ModelCalls != 0 || calls.Load() != 2 {
		t.Fatal("profile 3 fork replay called provider")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Start(root, nil, provider)
	if err != nil {
		t.Fatal(err)
	}
	archivedDefault, err := connect(t, h).CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{}})
	if err != nil || !strings.HasPrefix(archivedDefault.GetHarness(), "bundle-v3-") || calls.Load() != 2 {
		t.Fatal("archive-only startup selected a legacy default or called the provider")
	}
}

func TestProfile3SchemaSelectionOnlyUsesCurrentQuestionEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		inputs      []string
		want        string
	}{
		{"scout", "scouting", []string{`{"mode":"scout"}`}, frozenScoutSchema},
		{"injury", "injury", []string{`{"mode":"injury"}`}, frozenInjurySchema},
		{"scenario", "scouting", []string{`{"mode":"scenario"}`}, frozenScenarioSchema},
		{"question-scout", "scouting", []string{`{"mode":"question"}`}, questionSchemaFixture},
		{"question-injury", "injury", []string{`{"mode":"question"}`}, questionSchemaFixture},
		{"last-input", "scouting", []string{`{"mode":"question"}`, `{"mode":"scenario"}`}, frozenScenarioSchema},
		{"last-question", "injury", []string{`{"mode":"injury"}`, `{"mode":"question"}`}, questionSchemaFixture},
		{"malformed", "injury", []string{`{"mode":"question"`}, frozenInjurySchema},
		{"unknown", "scouting", []string{`{"mode":"unknown"}`}, frozenScoutSchema},
		{"empty", "scouting", nil, frozenScoutSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := bundle(t, "Synthetic schema instructions.", "synthetic-model")
			b, err := contract.Parse([]byte(strings.Replace(string(b.Source), "name: scouting", "name: "+tc.agent, 1)))
			if err != nil {
				t.Fatal(err)
			}
			har, err := NewBundleHarness(b, 3)
			if err != nil {
				t.Fatal(err)
			}
			start := &api.Start{History: []api.Event{{Kind: api.EventInput, Message: api.TextMessage("user", `{"mode":"question"}`)}}}
			for _, text := range tc.inputs {
				start.Inputs = append(start.Inputs, *api.TextMessage("user", text))
			}
			sink := &requestCaptureSink{}
			if err := har.Run(context.Background(), start, sink); err != nil {
				t.Fatal(err)
			}
			if sink.request.Params["response_format"] != tc.want {
				t.Fatalf("schema = %s, want %s", sink.request.Params["response_format"], tc.want)
			}
			if sink.request.Model != b.Model {
				t.Fatal("question changed model")
			}
			var format map[string]any
			if json.Unmarshal([]byte(sink.request.Params["response_format"]), &format) != nil {
				t.Fatal("schema is not JSON")
			}
		})
	}
}

type requestCaptureSink struct {
	api.EventSink
	request api.ModelRequest
}

func (s *requestCaptureSink) Model(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
	s.request = req
	return api.ModelResponse{}, nil
}
