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

	"github.com/agent-primitives/canvas/internal/activation"
	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
)

func authoringRequest(t *testing.T, client *http.Client, base, method, path, instance string, body any) (int, []byte) {
	t.Helper()
	var input io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, input)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", base)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Canvas-Instance", instance)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}
func TestAuthoringFreshLinkedNativeInstances(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	draft := filepath.Join(t.TempDir(), "draft")
	state := filepath.Join(t.TempDir(), "state")
	if _, err := authoring.Init(root, draft, "single-review", "fresh"); err != nil {
		t.Fatal(err)
	}
	d, err := startAuthoring(root, state, "127.0.0.1:0", draft, engineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d.Close() }()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	_, raw := authoringRequest(t, client, d.URL, "GET", "/api/demo", "", nil)
	if !bytes.Contains(raw, []byte(`"activeInstance":""`)) {
		t.Fatalf("not initially inert: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(state, "instances")); !os.IsNotExist(err) {
		t.Fatal("runtime before activation")
	}
	code, raw := authoringRequest(t, client, d.URL, "POST", "/api/authoring/prepare", "", map[string]string{"definition": "canvas.yaml"})
	if code != 200 {
		t.Fatalf("prepare: %d %s", code, raw)
	}
	var prepared struct {
		Candidate authoring.Package `json:"candidate"`
	}
	if json.Unmarshal(raw, &prepared) != nil || prepared.Candidate.ID == "" {
		t.Fatal("missing candidate", string(raw))
	}
	seed := filepath.Join(draft, "seeds", "draft.json")
	seedBytes, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(seed, bytes.ReplaceAll(seedBytes, []byte("Describe the document you want to review."), []byte("Working source changed")), 0644); err != nil {
		t.Fatal(err)
	}
	activate := func(candidate, current, key string) activation.Instance {
		t.Helper()
		code, raw := authoringRequest(t, client, d.URL, "POST", "/api/authoring/activate", "", activation.ActivationRequest{Candidate: candidate, ExpectedCurrent: current, RequestKey: key, Confirmed: true, Actor: "local person (unverified)"})
		if code != 200 {
			t.Fatalf("activate: %d %s", code, raw)
		}
		var result struct {
			Instance activation.Instance `json:"instance"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Instance.ID == "" {
			t.Fatal(string(raw))
		}
		return result.Instance
	}
	first := activate(prepared.Candidate.ID, "", "first-activation-key")
	code, raw = authoringRequest(t, client, d.URL, "GET", "/engine/api/document?path=draft.json", first.ID, nil)
	if code != 200 || bytes.Contains(raw, []byte("Working source changed")) {
		t.Fatalf("read mutable source: %d %s", code, raw)
	}
	code, raw = authoringRequest(t, client, d.URL, "POST", "/engine/api/run", first.ID, map[string]string{"agent": "writer", "request": "Produce a local proposal", "role": "GM"})
	if code != 202 {
		t.Fatalf("run: %d %s", code, raw)
	}
	var oldState canvas.State
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, raw = authoringRequest(t, client, d.URL, "GET", "/engine/api/state", first.ID, nil)
		if json.Unmarshal(raw, &oldState) != nil {
			t.Fatal(string(raw))
		}
		if len(oldState.Proposals) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(oldState.Proposals) != 1 {
		t.Fatalf("native proposal missing: %s", raw)
	}
	code, raw = authoringRequest(t, client, d.URL, "POST", "/engine/api/review", first.ID, map[string]string{"proposal": oldState.Proposals[0].ID, "action": "accept", "role": "GM"})
	if code != 200 {
		t.Fatalf("accept: %d %s", code, raw)
	}
	// Change the reviewed seed unambiguously without relying on template copy text.
	var seedObject map[string]any
	if json.Unmarshal(seedBytes, &seedObject) != nil {
		t.Fatal("seed not JSON")
	}
	seedObject["text"] = "Fresh second instance"
	newSeed, _ := json.Marshal(seedObject)
	if os.WriteFile(seed, newSeed, 0644) != nil {
		t.Fatal("seed write")
	}
	code, raw = authoringRequest(t, client, d.URL, "POST", "/api/authoring/prepare", "", map[string]string{"definition": "canvas.yaml"})
	if code != 200 || json.Unmarshal(raw, &prepared) != nil {
		t.Fatal("second prepare", string(raw))
	}
	second := activate(prepared.Candidate.ID, first.ID, "second-activation-key")
	code, raw = authoringRequest(t, client, d.URL, "GET", "/api/authoring/instance?id="+first.ID, "", nil)
	if code != 200 || !bytes.Contains(raw, []byte(second.ID)) || !bytes.Contains(raw, []byte(`"status":"read-only"`)) || !bytes.Contains(raw, []byte(`"revision"`)) {
		t.Fatalf("readonly archive/link lost: %d %s", code, raw)
	}
	code, _ = authoringRequest(t, client, d.URL, "POST", "/engine/api/review", first.ID, map[string]string{"proposal": oldState.Proposals[0].ID, "action": "accept", "role": "GM"})
	if code != 409 {
		t.Fatal("old instance mutation admitted", code)
	}
	code, raw = authoringRequest(t, client, d.URL, "GET", "/engine/api/state", second.ID, nil)
	var fresh canvas.State
	if code != 200 || json.Unmarshal(raw, &fresh) != nil || len(fresh.Proposals) != 0 || len(fresh.Runs) != 0 {
		t.Fatalf("inherited execution/decisions: %s", raw)
	}
	code, raw = authoringRequest(t, client, d.URL, "GET", "/engine/api/document?path=draft.json", second.ID, nil)
	if code != 200 || !bytes.Contains(raw, []byte("Fresh second instance")) {
		t.Fatalf("fresh reviewed seed absent: %s", raw)
	}
	if second.Previous != first.ID {
		t.Fatal("missing predecessor link")
	}
	d.Close()
	if err = os.RemoveAll(draft); err != nil {
		t.Fatal(err)
	}
	d, err = startAuthoring(root, state, "127.0.0.1:0", draft, engineOptions{})
	if err != nil {
		t.Fatal("restart depends on working source", err)
	}
	_, raw = authoringRequest(t, client, d.URL, "GET", "/api/demo", "", nil)
	if !bytes.Contains(raw, []byte(second.ID)) || !bytes.Contains(raw, []byte(`"providerCalls":0`)) {
		t.Fatalf("restart dispatched/changed identity: %s", raw)
	}
	code, raw = authoringRequest(t, client, d.URL, "GET", "/api/authoring/instance?id="+first.ID, "", nil)
	if code != 200 || !bytes.Contains(raw, []byte(second.ID)) {
		t.Fatal("restart lost links", string(raw))
	}
}

func TestAuthoringRefusesBeforeState(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	for _, options := range []engineOptions{{trustedEndpoint: "127.0.0.1:1"}, {remote: &remoteEngine{}}} {
		state := filepath.Join(t.TempDir(), "state")
		if d, err := startAuthoring(root, state, "127.0.0.1:0", root, options); err == nil {
			d.Close()
			t.Fatal("unsafe authoring combination accepted")
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("refusal wrote state")
		}
	}
}

func TestAuthoringStrictInputAndBrowserBoundary(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	d, err := startAuthoring(root, filepath.Join(t.TempDir(), "state"), "127.0.0.1:0", root, engineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	authoringRequest(t, client, d.URL, "GET", "/api/demo", "", nil)
	for _, body := range []string{`{"definition":"canvas.yaml","definition":"other.yaml"}`, `{"definition":"canvas.yaml","extra":true}`, `{"definition":"../canvas.yaml"}`, `{} {}`, strings.Repeat(" ", 4097)} {
		req, _ := http.NewRequest("POST", d.URL+"/api/authoring/prepare", strings.NewReader(body))
		req.Header.Set("Origin", d.URL)
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("invalid authoring input accepted", body)
		}
	}
	response, e := http.Post(d.URL+"/api/authoring/prepare", "application/json", strings.NewReader(`{"definition":"canvas.yaml"}`))
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cookie/origin-free mutation admitted", response.StatusCode)
	}
}
