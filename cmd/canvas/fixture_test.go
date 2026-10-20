package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
)

func TestFixtureFlagsRefuseBeforeEffects(t *testing.T) {
	for _, extra := range [][]string{
		{"--model-profile", "offline"}, {"--model-base", "http://127.0.0.1:1"}, {"--key-file", "credential-canary"},
		{"--sessions-address", "127.0.0.1:1"}, {"--sessions-only=false"}, {"--validate"},
	} {
		state := filepath.Join(t.TempDir(), "not-created")
		args := append([]string{"--fixture", "--definition", "missing.yaml", "--state", state}, extra...)
		err := serve(context.Background(), args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "credential-canary") {
			t.Fatalf("wrong fixture flag refusal: %v", err)
		}
		if _, err := os.Lstat(state); !os.IsNotExist(err) {
			t.Fatal("flag refusal created state")
		}
	}
	implicitState := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{{"--fixture"}, {"--fixture", "--definition", "missing.yaml"}, {"--fixture", "--root", fixtureRoot(t), "--state", implicitState}} {
		if err := serve(context.Background(), args, io.Discard); err == nil || !strings.Contains(err.Error(), "fixture requires") {
			t.Fatalf("implicit fixture startup: %v", err)
		}
		if _, err := os.Lstat(implicitState); !os.IsNotExist(err) {
			t.Fatal("implicit fixture created state")
		}
	}
}

func startGeneratedFixture(t *testing.T, definition, state string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	output := roomAddressWriter{ready: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"--fixture", "--definition", definition, "--state", state, "--root", t.TempDir(), "--listen", "127.0.0.1:0"}, output)
	}()
	var address string
	select {
	case address = <-output.ready:
	case err := <-done:
		cancel()
		t.Fatalf("generated fixture startup: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("fixture startup timed out")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("fixture shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("fixture shutdown timed out")
			}
		})
	}
	t.Cleanup(stop)
	return address, stop
}

func TestFixtureGeneratedCanvasReviewAndRestart(t *testing.T) {
	source, _ := filepath.Abs(filepath.Join("..", ".."))
	for _, template := range []string{"single-review", "writer-review"} {
		t.Run(template, func(t *testing.T) {
			packageDir := filepath.Join(t.TempDir(), "my-review")
			if _, err := authoring.Init(source, packageDir, template, "my-review"); err != nil {
				t.Fatal(err)
			}
			seed := `{"text":"A newcomer edited this seed."}`
			if err := os.WriteFile(filepath.Join(packageDir, "seeds", "draft.json"), []byte(seed), 0644); err != nil {
				t.Fatal(err)
			}
			definition := filepath.Join(packageDir, "canvas.yaml")
			if _, err := canvas.LoadGenericDefinition(definition); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(t.TempDir(), "new-state")
			address, stop := startGeneratedFixture(t, definition, statePath)
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
			request := func(method, path string, body any, out any) {
				t.Helper()
				raw, _ := json.Marshal(body)
				req, _ := http.NewRequest(method, address+path, bytes.NewReader(raw))
				req.Header.Set("Origin", address)
				req.Header.Set("Content-Type", "application/json")
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				if res.Header.Get("X-Canvas-Provider") != "deterministic-fixture" {
					t.Fatal("actual provider mode not labelled")
				}
				data, err := io.ReadAll(res.Body)
				if err != nil || res.StatusCode >= 300 {
					t.Fatalf("engine request %s: %d %s %v", path, res.StatusCode, data, err)
				}
				if out != nil && json.Unmarshal(data, out) != nil {
					t.Fatal("invalid engine JSON")
				}
			}
			var initial canvas.State
			request("GET", "/api/state", nil, &initial)
			if len(initial.Runs) != 0 || len(initial.Actions) != 0 {
				t.Fatal("startup ran a proposal or enabled actions")
			}
			draft := func() string {
				var answer struct {
					Run string `json:"run"`
				}
				request("POST", "/api/run", map[string]string{"agent": "writer", "request": "Propose a local fixture change.", "role": "GM"}, &answer)
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
								t.Fatalf("fixture run failed: %s", run.Error)
							}
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("fixture proposal did not complete")
				return ""
			}
			first := draft()
			var proposed map[string]string
			request("GET", "/api/document?path=draft.json&proposal="+first, nil, &proposed)
			if proposed["base"] != seed || proposed["proposed"] == seed {
				t.Fatal("edited seed did not produce visible diff")
			}
			request("POST", "/api/review", map[string]string{"proposal": first, "action": "accept", "role": "GM"}, nil)
			accepted := proposed["proposed"]
			stop()
			before := startupTree(t, statePath)
			if err := serve(context.Background(), []string{"--definition", definition, "--state", statePath, "--model-profile", "offline"}, io.Discard); !errors.Is(err, errRuntimeMode) {
				t.Fatalf("real startup ignored fixture binding: %v", err)
			}
			if !reflect.DeepEqual(before, startupTree(t, statePath)) {
				t.Fatal("opposite-mode startup changed history")
			}
			address, stop = startGeneratedFixture(t, definition, statePath)
			defer stop()
			jar, _ = cookiejar.New(nil)
			client.Jar = jar
			var reopened canvas.State
			request("GET", "/api/state", nil, &reopened)
			if len(reopened.Runs) != 1 {
				t.Fatal("restart lost history or dispatched new work")
			}
			second := draft()
			request("GET", "/api/document?path=draft.json&proposal="+second, nil, &proposed)
			if proposed["base"] != accepted || proposed["proposed"] == accepted {
				t.Fatal("fixture reused startup seed instead of current accepted snapshot")
			}
			request("POST", "/api/review", map[string]string{"proposal": second, "action": "reject", "role": "GM"}, nil)
			var document map[string]string
			request("GET", "/api/document?path=draft.json", nil, &document)
			if document["accepted"] != accepted {
				t.Fatal("rejection changed accepted document")
			}
		})
	}
}

// Mode refusals must win even when a later generic preflight would also refuse.
func TestFixtureOppositeModesPrecedeGenericPreflight(t *testing.T) {
	for _, fixture := range []bool{false, true} {
		state := t.TempDir()
		binding, err := checkRuntimeMode(state, !fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := binding.Bind(); err != nil {
			t.Fatal(err)
		}
		writeStartupFixture(t, filepath.Join(state, "canvas", "attempts.json"), `{"attempts":[]}`)
		before := startupTree(t, state)
		args := []string{"--definition", "missing.yaml", "--state", state}
		if fixture {
			args = append(args, "--fixture")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := serve(ctx, args, io.Discard); !errors.Is(err, errRuntimeMode) {
			t.Fatalf("opposite mode precedence: %v", err)
		}
		if !reflect.DeepEqual(before, startupTree(t, state)) {
			t.Fatal("opposite mode changed state")
		}
	}
}
