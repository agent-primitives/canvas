package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/announcementexec"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func executorPaths(t *testing.T) (string, string) {
	t.Helper()
	root, err := os.MkdirTemp("", "canvas-exec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	sockets := filepath.Join(root, "sockets")
	if err = os.Mkdir(sockets, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(sockets, "executor.sock"), filepath.Join(root, "state")
}

func startExecutor(t *testing.T, socket, state string) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveAnnouncementExecutor(ctx, socket, state) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-done:
			cancel()
			t.Fatalf("executor startup: %v", e)
		case <-deadline.C:
			cancel()
			t.Fatal("executor startup timeout")
		case <-tick.C:
			// Observe the owned listener without occupying a worker slot;
			// a connect-and-close probe can race the connection-limit test.
			info, e := os.Lstat(socket)
			if e == nil && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0600 {
				t.Cleanup(func() { cancel() })
				return cancel, done
			}
		}
	}
}
func stopExecutor(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not drain")
	}
}
func executorFrame(t *testing.T, content string) []byte {
	t.Helper()
	hash := sha256.Sum256([]byte(content))
	// Match ADA socketexec's seven-field v1 request and alphabetically marshaled Args map.
	req := struct {
		Version int            `json:"version"`
		Harness string         `json:"harness"`
		Session string         `json:"session_uid"`
		Call    string         `json:"call_id"`
		Tool    string         `json:"tool"`
		Args    map[string]any `json:"arguments"`
		Key     string         `json:"idempotency_key"`
	}{1, "canvas.simulated-announcement.v1", "session-1", "operation-1", "canvas.simulated-announcement", map[string]any{
		"operation": "operation-1", "definition": strings.Repeat("a", 64), "action": "announce", "document": "announcement", "path": "announcement.md", "proposal": "proposal-1", "revision": strings.Repeat("b", 40), "digest": hex.EncodeToString(hash[:]), "content": content,
	}, "operation-1"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

type wireResponse struct {
	Version int            `json:"version"`
	Call    string         `json:"call_id"`
	IsError bool           `json:"is_error"`
	Output  map[string]any `json:"output"`
	Error   string         `json:"error"`
}

func sendExecutor(t *testing.T, socket string, frame []byte) (wireResponse, error) {
	t.Helper()
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return wireResponse{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = conn.Write(frame); err != nil {
		return wireResponse{}, err
	}
	var response wireResponse
	err = json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&response)
	return response, err
}
func executorEvidence(t *testing.T, state string) announcementexec.Evidence {
	t.Helper()
	e, err := announcementexec.ReadEvidence(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Catches a transport-only opt-in accidentally treated as signed-in authority.
func TestExecutorRequiresExplicitSimulation(t *testing.T) {
	for _, args := range [][]string{
		{}, {"-auth-mode", "required", "-trusted-network=true"}, {"-auth-mode", "off"}, {"-trusted-network=true"}, {"-auth-mode", "off", "-trusted-network=false"}, {"-auth-mode", "off", "-trusted-network=true", "extra"},
		{"-auth-mode", "off", "-trusted-network=true", "-socket", ""},
		{"-auth-mode", "off", "-trusted-network=true", "-state", ""},
		{"-auth-mode", "off", "-trusted-network=true", "-socket", "relative.sock"},
	} {
		socket, state := executorPaths(t)
		flags := append([]string{"executor", "-socket", socket, "-state", state}, args...)
		err := run(t.Context(), flags, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "simulation") {
			t.Fatalf("opt-in %v: %v", args, err)
		}
		for _, p := range []string{socket, state} {
			if _, e := os.Lstat(p); !os.IsNotExist(e) {
				t.Fatalf("refusal changed %s: %v", p, e)
			}
		}
	}
}

// Catches a second receipt, broken ADA map key ordering, lost binding or lease on restart.
func TestExecutorWireLifecycle(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	initial := executorEvidence(t, state)
	if initial.Invocations != 0 || initial.Outcomes != 0 {
		t.Fatal(initial)
	}
	raw := executorFrame(t, "Fictional release\n")
	var first wireResponse
	for i := 0; i < 2; i++ {
		response, err := sendExecutor(t, socket, raw)
		if err != nil || response.Version != 1 || response.Call != "operation-1" || response.IsError {
			t.Fatalf("response: %+v %v", response, err)
		}
		receipt, err := announcement.DecodeReceipt(response.Output)
		if err != nil || receipt.Snapshot.Content != "Fictional release\n" || receipt.Snapshot.Operation != "operation-1" {
			t.Fatalf("receipt: %+v %v", receipt, err)
		}
		if i == 0 {
			first = response
		} else if !reflect.DeepEqual(first, response) {
			t.Fatal("duplicate receipt changed")
		}
	}
	conflict, err := sendExecutor(t, socket, executorFrame(t, "Changed release\n"))
	if err != nil || !conflict.IsError || conflict.Output != nil || !strings.Contains(conflict.Error, "reconcile") {
		t.Fatalf("conflict: %+v %v", conflict, err)
	}
	evidence := executorEvidence(t, state)
	if evidence.Invocations != 3 || evidence.Outcomes != 1 || evidence.StoreID != initial.StoreID {
		t.Fatal(evidence)
	}
	stopExecutor(t, cancel, done)
	if _, err = os.Lstat(socket); err != nil {
		t.Fatal("stale socket must remain for operator verification", err)
	}
	socket2 := filepath.Join(filepath.Dir(socket), "restart.sock")
	cancel, done = startExecutor(t, socket2, state)
	response, err := sendExecutor(t, socket2, raw)
	if err != nil || !reflect.DeepEqual(response, first) {
		t.Fatalf("restart receipt: %+v %v", response, err)
	}
	evidence = executorEvidence(t, state)
	if evidence.Invocations != 4 || evidence.Outcomes != 1 || evidence.StoreID != initial.StoreID {
		t.Fatal(evidence)
	}
	stopExecutor(t, cancel, done)
}

// Catches per-connection executors losing the shared durable operation namespace.
func TestExecutorConcurrentDuplicates(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	defer stopExecutor(t, cancel, done)
	type reply struct {
		response wireResponse
		err      error
	}
	results := make(chan reply, 8)
	for i := 0; i < 8; i++ {
		go func() {
			response, err := sendExecutor(t, socket, executorFrame(t, "concurrent"))
			results <- reply{response, err}
		}()
	}
	for i := 0; i < 8; i++ {
		result := <-results
		if result.err != nil || result.response.IsError {
			t.Fatalf("duplicate transport: %+v %v", result.response, result.err)
		}
	}
	if evidence := executorEvidence(t, state); evidence.Invocations != 8 || evidence.Outcomes != 1 {
		t.Fatal(evidence)
	}
}

// Catches permissive decoding that would admit untyped or differently bound frames.
func TestExecutorFrameRefusals(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	defer stopExecutor(t, cancel, done)
	valid := string(executorFrame(t, "private payload"))
	cases := map[string]string{
		"unknown":          strings.Replace(valid, `"version":1`, `"version":1,"token":"do-not-leak"`, 1),
		"duplicate":        strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		"case":             strings.Replace(valid, `"version"`, `"Version"`, 1),
		"null":             strings.Replace(valid, `"version":1`, `"version":null`, 1),
		"float":            strings.Replace(valid, `"version":1`, `"version":1.0`, 1),
		"harness":          strings.Replace(valid, "canvas.simulated-announcement.v1", "another-harness", 1),
		"tool":             strings.Replace(valid, `"tool":"canvas.simulated-announcement"`, `"tool":"publish"`, 1),
		"session":          strings.Replace(valid, `"session_uid":"session-1"`, `"session_uid":""`, 1),
		"key":              strings.Replace(valid, `"idempotency_key":"operation-1"`, `"idempotency_key":"other"`, 1),
		"nested-duplicate": strings.Replace(valid, `"action":"announce"`, `"action":"announce","action":"announce"`, 1),
		"nested-null":      strings.Replace(valid, `"content":"private payload"`, `"content":null`, 1),
		"nested-object":    strings.Replace(valid, `"content":"private payload"`, `"content":{}`, 1),
		"missing":          strings.Replace(valid, `"tool":"canvas.simulated-announcement",`, "", 1),
		"invalid-utf8":     strings.Replace(valid, "private payload", string([]byte{0xff}), 1),
		"trailing":         strings.TrimSpace(valid) + " {}\n",
		"oversize":         strings.Repeat("x", (1<<20)+1) + "\n",
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			response, err := sendExecutor(t, socket, []byte(frame))
			if err == nil {
				t.Fatalf("invalid request got response: %+v", response)
			}
		})
	}
	// EOF before newline is refused rather than interpreting a partial frame.
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte(strings.TrimSpace(valid)))
	conn.(*net.UnixConn).CloseWrite()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = bufio.NewReader(conn).ReadByte(); err == nil {
		t.Fatal("truncated frame answered")
	}
	conn.Close()
	evidence := executorEvidence(t, state)
	if evidence.Invocations != 0 || evidence.Outcomes != 0 {
		t.Fatal("invalid request admitted", evidence)
	}
}

// Catches deleting/repairing collisions, permissive modes and overlapping socket/state roots.
func TestExecutorSocketRefusals(t *testing.T) {
	for _, mode := range []string{"file", "symlink", "socket", "public-parent", "state-overlap", "state-parent"} {
		t.Run(mode, func(t *testing.T) {
			socket, state := executorPaths(t)
			var existing net.Listener
			switch mode {
			case "file":
				os.WriteFile(socket, []byte("unchanged"), 0600)
			case "symlink":
				os.Symlink("missing", socket)
			case "socket":
				var err error
				existing, err = net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer existing.Close()
			case "public-parent":
				os.Chmod(filepath.Dir(socket), 0755)
			case "state-overlap":
				state = filepath.Dir(socket)
			case "state-parent":
				state = filepath.Dir(filepath.Dir(socket))
			}
			before, _ := os.Lstat(socket)
			err := serveAnnouncementExecutor(t.Context(), socket, state)
			if err == nil {
				t.Fatal("unsafe socket/state accepted")
			}
			after, _ := os.Lstat(socket)
			if before != nil && (after == nil || !os.SameFile(before, after)) {
				t.Fatal("collision replaced")
			}
			if mode != "state-overlap" && mode != "state-parent" {
				if _, err = os.Stat(state); !os.IsNotExist(err) {
					t.Fatal("refusal initialized store", err)
				}
			}
		})
	}
}

// Catches a shutdown blocked on stalled reads or accidental unlink of another socket.
func TestExecutorShutdownAndLease(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	other := filepath.Join(filepath.Dir(socket), "other.sock")
	if err := serveAnnouncementExecutor(t.Context(), other, state); err == nil {
		t.Fatal("second writer accepted")
	}
	if _, err := os.Lstat(other); !os.IsNotExist(err) {
		t.Fatal("second writer opened socket")
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"version":`))
	// Same-user/operator replacement is outside the socket's trust boundary; never unlink it.
	if err = os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer replacement.Close()
	replacementInfo, _ := os.Lstat(socket)
	stopExecutor(t, cancel, done)
	after, err := os.Lstat(socket)
	if err != nil || !os.SameFile(replacementInfo, after) {
		t.Fatal("shutdown removed replacement", err)
	}
}

// Catches an unbounded worker pool admitting work while all bounded slots stall.
func TestExecutorConnectionLimit(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	defer stopExecutor(t, cancel, done)
	for i := 0; i < 16; i++ {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.Write([]byte(`{"version":`))
	}
	response, err := sendExecutor(t, socket, executorFrame(t, "overloaded"))
	if err == nil {
		t.Fatalf("overloaded server admitted a request: %+v", response)
	}
	if evidence := executorEvidence(t, state); evidence.Invocations != 0 {
		t.Fatal(evidence)
	}
}

// Catches a peer holding a worker forever without finishing the first frame.
func TestExecutorStalledFrameDeadline(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	defer stopExecutor(t, cancel, done)
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"version":`))
	conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	_, err = bufio.NewReader(conn).ReadByte()
	var networkErr net.Error
	if err == nil || errors.As(err, &networkErr) && networkErr.Timeout() {
		t.Fatalf("server did not bound stalled frame: %v", err)
	}
	if evidence := executorEvidence(t, state); evidence.Invocations != 0 {
		t.Fatal(evidence)
	}
}

// Catches keeping a corrupted, fenced store online to consume further approvals.
func TestExecutorUncertainStops(t *testing.T) {
	socket, state := executorPaths(t)
	_, done := startExecutor(t, socket, state)
	response, err := sendExecutor(t, socket, executorFrame(t, "before"))
	if err != nil || response.IsError {
		t.Fatal(response, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(state, "executor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Break the first admitted binding's mandatory invocation linkage, as an external corruptor.
	if _, err = db.Exec(`DELETE FROM invocations`); err != nil {
		t.Fatal(err)
	}
	_, _ = sendExecutor(t, socket, executorFrame(t, "before"))
	select {
	case err = <-done:
		if !errors.Is(err, announcementexec.ErrUncertain) {
			t.Fatalf("want uncertain exit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain server kept accepting")
	}
}

// Catches confusing transport loss with native pending/uncertain approval: the native result is final.
func TestExecutorLostReplyNativeFailureWithDurableReceipt(t *testing.T) {
	socket, state := executorPaths(t)
	cancel, done := startExecutor(t, socket, state)
	defer stopExecutor(t, cancel, done)
	log := eventlog.AsStore(eventlog.New())
	newNative := func() *controller.Controller {
		native, err := controller.New(log, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
			t.Error("model called")
			return api.ModelResponse{}, errors.New("forbidden")
		}, controller.WithSessionUID("session-1"), controller.WithHarness("canvas.simulated-announcement.v1"), controller.WithToolExecutor(func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
			conn, e := net.DialTimeout("unix", socket, time.Second)
			if e != nil {
				return api.ToolResult{}, e
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(time.Second))
			if _, e = conn.Write(executorFrame(t, "native receipt")); e != nil {
				return api.ToolResult{}, e
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				// A live transaction's rollback journal deliberately makes this
				// read-only evidence API unavailable; it must not invent zero.
				evidence, readErr := announcementexec.ReadEvidence(t.Context(), state)
				if readErr == nil && evidence.Outcomes == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("no durable outcome: %+v %v", evidence, readErr)
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			// Discard the actual socket reply even though the independent ledger committed.
			return api.ToolResult{}, errors.New("reply lost")
		}))
		if err != nil {
			t.Fatal(err)
		}
		return native
	}
	native := newNative()
	var err error
	var frame struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err = json.Unmarshal(executorFrame(t, "native receipt"), &frame); err != nil {
		t.Fatal(err)
	}
	if err = native.Exec(t.Context(), sessionhost.NewAnnouncementHarness(), []api.Message{*api.TextMessage("user", string(frame.Arguments))}, 0); !errors.Is(err, api.ErrApprovalParked) {
		t.Fatal(err)
	}
	pending, err := controller.InspectApproval(log, 0)
	if err != nil || pending == nil || pending.Request == nil {
		t.Fatal("no pending approval", err)
	}
	if evidence := executorEvidence(t, state); evidence.Invocations != 0 {
		t.Fatal("park invoked executor", evidence)
	}
	if _, err = controller.Approve(log, api.ApprovalDecision{ExecutionID: pending.ExecutionID, ToolCallID: pending.Call.ID, RequestSeq: pending.Request.Seq, Approved: true}); err != nil {
		t.Fatal(err)
	}
	if evidence := executorEvidence(t, state); evidence.Invocations != 0 {
		t.Fatal("approve invoked executor", evidence)
	}
	_, resumeErr := newNative().Resume(t.Context(), sessionhost.NewAnnouncementHarness())
	if resumeErr == nil {
		t.Fatal("lost reply reported native success")
	}
	records, err := log.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	var results []*api.ToolResult
	for _, record := range records {
		if record.Event.Result != nil {
			results = append(results, record.Event.Result)
		}
	}
	if len(results) != 1 || !results[0].IsError || results[0].Code != api.ToolResultCodeExecutorError || results[0].Output != nil {
		t.Fatalf("lost reply not terminal native error: %+v, resume: %v", results, resumeErr)
	}
	evidence := executorEvidence(t, state)
	if evidence.Invocations != 1 || evidence.Outcomes != 1 {
		t.Fatal("lost reply hid receipt", evidence)
	}
	response, err := sendExecutor(t, socket, executorFrame(t, "native receipt"))
	if err != nil || response.IsError {
		t.Fatal("explicit transport reconciliation", response, err)
	}
	if evidence = executorEvidence(t, state); evidence.Invocations != 2 || evidence.Outcomes != 1 {
		t.Fatal(evidence)
	}
}
