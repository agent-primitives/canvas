package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestLocalNorthwindDocumentAndActionLoop(t *testing.T) {
	root := filepath.Join("..", "..")
	d, err := start(root, t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	res, err := client.Get(d.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	request := func(method, path string, body any, out any) int {
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
		if out != nil && res.StatusCode < 300 {
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode
	}
	draft := func() string {
		t.Helper()
		var answer struct {
			Run string `json:"run"`
		}
		if code := request("POST", "/api/run", map[string]string{"agent": "writer", "request": "Draft the fictional pilot.", "role": "GM"}, &answer); code != 202 {
			t.Fatal(code)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var state canvas.State
			request("GET", "/api/state", nil, &state)
			for _, run := range state.Runs {
				if run.ID == answer.Run {
					if run.Proposal != "" {
						return run.Proposal
					}
					if run.Status == "failed" {
						t.Fatalf("draft failed: %+v", run)
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("draft did not complete")
		return ""
	}
	rejected := draft()
	if request("POST", "/api/review", map[string]string{"proposal": rejected, "action": "reject", "role": "GM"}, nil) != 200 {
		t.Fatal("reject")
	}
	accepted := draft()
	stale := draft()
	if request("POST", "/api/review", map[string]string{"proposal": accepted, "action": "accept", "role": "GM"}, nil) != 200 {
		t.Fatal("accept")
	}
	if request("POST", "/api/review", map[string]string{"proposal": stale, "action": "accept", "role": "GM"}, nil) != 409 {
		t.Fatal("stale accepted")
	}
	var state canvas.State
	request("GET", "/api/state", nil, &state)
	if len(state.Announcements) != 0 {
		t.Fatal("document decision created an action")
	}
	for _, run := range state.Runs {
		if run.Proposal == accepted {
			var proof map[string]any
			if request("POST", "/api/verify", map[string]string{"run": run.ID}, &proof) != 200 || proof["modelCalls"] != float64(0) || proof["executorCalls"] != float64(0) {
				t.Fatalf("proposal replay: %+v", proof)
			}
		}
	}
	var op struct {
		ID string `json:"id"`
	}
	if request("POST", "/api/announcement/request", map[string]string{"proposal": accepted, "action": "announce", "role": "GM"}, &op) != 200 {
		t.Fatal("request gate")
	}
	inspect := func() canvas.AnnouncementView {
		var v canvas.AnnouncementView
		if request("GET", "/api/announcement?id="+op.ID, nil, &v) != 200 {
			t.Fatal("inspect")
		}
		v.Confirmation.Confirmed = true
		return v
	}
	v := inspect()
	if v.Status != "pending-approval" {
		t.Fatalf("not parked: %+v", v)
	}
	if request("POST", "/api/announcement/decision", map[string]any{"id": op.ID, "role": "GM", "approved": true, "confirmation": v.Confirmation}, nil) != 200 {
		t.Fatal("approve")
	}
	v = inspect()
	if v.Status != "approved" || v.Receipt != nil {
		t.Fatal("approval executed action")
	}
	if request("POST", "/api/announcement/continue", map[string]any{"id": op.ID, "role": "GM", "confirmation": v.Confirmation}, nil) != 200 {
		t.Fatal("continue")
	}
	v = inspect()
	if v.Status != "simulated" || v.Receipt == nil {
		t.Fatal("missing native receipt")
	}
	var proof map[string]any
	if request("POST", "/api/announcement/verify", map[string]string{"id": op.ID}, &proof) != 200 || proof["modelCalls"] != float64(0) || proof["executorCalls"] != float64(0) {
		t.Fatalf("action replay: %+v", proof)
	}
	var meta map[string]any
	if request("GET", "/api/demo", nil, &meta) != 200 || meta["providerCalls"] != float64(3) || meta["mode"] != "deterministic-northwind" {
		t.Fatalf("demo counters: %+v", meta)
	}
}

func TestRefuseNonLoopbackBeforeCreatingState(t *testing.T) {
	if d, err := start(filepath.Join("..", ".."), filepath.Join(t.TempDir(), "unused"), "0.0.0.0:18878"); err == nil {
		d.Close()
		t.Fatal("wildcard listen accepted")
	}
}
