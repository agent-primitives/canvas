package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func localFlowRequest(t *testing.T, d *demo) func(string, string, any, any) int {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	res, err := client.Get(d.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return func(method, path string, body, out any) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, d.URL+path, bytes.NewReader(raw))
		req.Header.Set("Origin", d.URL)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if out != nil && res.StatusCode < 300 && json.Unmarshal(data, out) != nil {
			t.Fatal("invalid response", string(data))
		}
		return res.StatusCode
	}
}

func TestLocalFlowStartupOptIn(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	for _, tc := range []struct {
		name, definition string
		options          engineOptions
		valid            bool
	}{
		{"explicit local", "examples/northwind-prd/flow.yaml", engineOptions{flowAgentRuns: 6}, true},
		{"manual path still refuses", "examples/northwind-prd/flow.yaml", engineOptions{}, false},
		{"negative ceiling", "examples/northwind-prd/flow.yaml", engineOptions{flowAgentRuns: -1}, false},
		{"missing definition", "", engineOptions{flowAgentRuns: 12}, false},
		{"no declared flow", "examples/northwind-prd/canvas.yaml", engineOptions{flowAgentRuns: 12}, false},
		{"trusted remote", "examples/northwind-prd/flow.yaml", engineOptions{flowAgentRuns: 12, trustedEndpoint: "127.0.0.1:9"}, false},
		{"authenticated remote", "examples/northwind-prd/flow.yaml", engineOptions{flowAgentRuns: 12, remote: &remoteEngine{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "not-created")
			d, err := startWithOptions(root, state, "127.0.0.1:0", tc.definition, tc.options)
			if d != nil {
				defer d.Close()
			}
			if !tc.valid {
				if err == nil {
					t.Fatal("unsupported flow startup succeeded")
				}
				if _, e := os.Stat(state); !os.IsNotExist(e) {
					t.Fatal("refusal initialized state/cache", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			request := localFlowRequest(t, d)
			var panel struct {
				OperatorAgentRuns int `json:"operatorAgentRuns"`
			}
			if request("GET", "/engine/api/flows", nil, &panel) != 200 || panel.OperatorAgentRuns != 6 {
				t.Fatal("operator ceiling lost", panel)
			}
			var info struct {
				FlowRuntime   bool   `json:"flowRuntime"`
				RequesterRole string `json:"requesterRole"`
			}
			if request("GET", "/api/demo", nil, &info) != 200 || !info.FlowRuntime || info.RequesterRole != "GM" {
				t.Fatal("missing explicit local-flow metadata", info)
			}
		})
	}
}

func TestLocalFlowTopicFixture(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	state := t.TempDir()
	d, err := startWithOptions(root, state, "127.0.0.1:0", "examples/northwind-prd/flow.yaml", engineOptions{flowAgentRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	request := localFlowRequest(t, d)
	var f canvas.FlowRun
	if request("POST", "/engine/api/flow/start", map[string]any{"flow": "release", "requestKey": "topic-http", "role": "GM", "inputs": map[string]any{"topic": "Safer local setup", "audience": "engineering", "includeRisks": false}, "agentRuns": 12}, &f) != 202 || f.ID == "" {
		t.Fatal("start", f)
	}
	wait := func(state string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if request("GET", "/engine/api/flow?run="+f.ID, nil, &f) != 200 {
				t.Fatal("inspect flow")
			}
			if f.State == state {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("flow failed to reach", state, f)
	}
	var draft, critique string
	for i, step := range []string{"draft", "critique", "revise"} {
		wait("review")
		if f.Step != step {
			t.Fatal("wrong step", f)
		}
		var s canvas.State
		if request("GET", "/api/state", nil, &s) != 200 {
			t.Fatal("state")
		}
		var changed map[string]string
		for _, p := range s.Proposals {
			if p.ID == f.Proposal {
				changed = p.Changes
			}
		}
		switch i {
		case 0:
			draft = changed["prd.json"]
			if !strings.Contains(draft, "Safer local setup") || !strings.Contains(changed["announcement.md"], "Safer local setup") {
				t.Fatal("fixture ignored topic from native request", changed)
			}
		case 1:
			critique = changed["review.json"]
			if !strings.Contains(critique, "Safer local setup") {
				t.Fatal("critique ignored actual topic", changed)
			}
		case 2:
			if changed["prd.json"] == draft {
				t.Fatal("revision fixture repeated draft bytes")
			}
			// This durable exact input is the one dispatched to the real native session.
			raw, e := os.ReadFile(filepath.Join(state, "canvas", "attempts.json"))
			if e != nil {
				t.Fatal(e)
			}
			var journal struct {
				Attempts []struct {
					ID    string
					Input string
				}
			}
			if json.Unmarshal(raw, &journal) != nil {
				t.Fatal("ledger input")
			}
			found := false
			for _, a := range journal.Attempts {
				if a.ID != f.Attempt {
					continue
				}
				found = true
				var input struct {
					Evidence map[string]string `json:"evidence"`
				}
				if json.Unmarshal([]byte(a.Input), &input) != nil || input.Evidence["review.json"] != critique || input.Evidence["prd.json"] != draft {
					t.Fatal("revision did not consume accepted draft AND critique", input)
				}
			}
			if !found {
				t.Fatal("native attempt not recorded")
			}
		}
		if request("POST", "/engine/api/review", map[string]string{"proposal": f.Proposal, "action": "accept", "role": "reviewer"}, nil) != 200 {
			t.Fatal("gate", step)
		}
	}
	wait("action")
}

func TestLocalFlowFixtureRefusesNonNorthwindShape(t *testing.T) {
	for _, tc := range []struct{ name, agent, prd, review string }{
		{"null PRD", "writer", `null`, `{"summary":"advice"}`},
		{"nontext problem", "writer", `{"title":"seed","problem":12}`, `{"summary":"advice"}`},
		{"null review", "reviewer", `{"title":"seed","problem":"problem"}`, `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &canvas.CompiledDefinition{Seed: map[string]string{"prd.json": tc.prd, "review.json": tc.review}}
			_, err := flowFixtureChanges(d, tc.agent, `{"inputs":{"topic":"Topic"}}`, map[string]string{"prd.json": `{"title":"Topic — draft"}`, "review.json": `{"summary":"advice"}`})
			if err == nil {
				t.Fatal("unsupported fixture shape accepted")
			}
		})
	}
}
