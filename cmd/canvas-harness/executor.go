package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/announcementexec"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

const executorFrameLimit = 1 << 20

var errExecutorRequest = errors.New("invalid simulated announcement request")

func runAnnouncementExecutor(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("canvas-harness executor", flag.ContinueOnError)
	flags.SetOutput(out)
	socket := flags.String("socket", "", "new absolute socket path in an existing owner-private directory")
	state := flags.String("state", "", "absolute retained executor subtree, separate from socket directory")
	mode := flags.String("auth-mode", "", "explicit off only; identity is not exercised")
	trusted := flags.Bool("trusted-network", false, "explicitly trust local/private socket callers")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errors.New("simulation executor flags are invalid")
	}
	if flags.NArg() != 0 || *mode != "off" || !*trusted || *socket == "" || *state == "" {
		return errors.New("simulation requires -socket, -state, -auth-mode off and -trusted-network=true; signed-in operation is unavailable")
	}
	fmt.Fprintln(out, "Starting unauthenticated simulated executor; identity not exercised. Hosted operation is incomplete pending host-authoritative dispatch bindings.")
	return serveAnnouncementExecutor(ctx, *socket, *state)
}

// The private socket is the trust boundary. This fixed adapter supplies typing,
// not an approval grant: anyone able to connect can request a simulated receipt.
func decodeAnnouncementRequest(raw []byte) (controller.ToolCallContext, api.ToolCall, error) {
	var scope controller.ToolCallContext
	var call api.ToolCall
	if len(raw) > executorFrameLimit || !utf8.Valid(raw) {
		return scope, call, errExecutorRequest
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return scope, call, errExecutorRequest
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return scope, call, errExecutorRequest
		}
		key, ok := token.(string)
		if !ok {
			return scope, call, errExecutorRequest
		}
		switch key {
		case "version", "harness", "session_uid", "call_id", "tool", "arguments", "idempotency_key":
		default:
			return scope, call, errExecutorRequest
		}
		if _, exists := fields[key]; exists {
			return scope, call, errExecutorRequest
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return scope, call, errExecutorRequest
		}
		fields[key] = value
	}
	if _, err = dec.Token(); err != nil || len(fields) != 7 {
		return scope, call, errExecutorRequest
	}
	if _, err = dec.Token(); err != io.EOF {
		return scope, call, errExecutorRequest
	}
	var version int
	var harness string
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 || json.Unmarshal(fields["harness"], &harness) != nil || harness != announcement.HarnessName {
		return scope, call, errExecutorRequest
	}
	if json.Unmarshal(fields["session_uid"], &scope.SessionUID) != nil || scope.SessionUID == "" || len(scope.SessionUID) > 4096 {
		return scope, call, errExecutorRequest
	}
	if json.Unmarshal(fields["call_id"], &call.ID) != nil || json.Unmarshal(fields["tool"], &call.Tool) != nil || call.Tool != announcement.ToolName || json.Unmarshal(fields["idempotency_key"], &call.IdempotencyKey) != nil {
		return scope, call, errExecutorRequest
	}
	snapshot, err := announcement.Decode(fields["arguments"])
	if err != nil || call.ID != snapshot.Operation || call.IdempotencyKey != snapshot.Operation {
		return scope, call, errExecutorRequest
	}
	call.Args = announcement.Args(snapshot)
	call.Mediation = api.MediationRequiresApproval
	return scope, call, nil
}

func overlappingDirectories(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

// Refuse collisions before opening the store. Never remove stale sockets, even
// on clean exit: an inode check followed by unlink cannot fence same-user swaps.
func executorSocketParent(socket, state string) (*os.Root, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || len(socket) > 103 || !filepath.IsAbs(state) || filepath.Clean(state) != state || filepath.Dir(state) == state || overlappingDirectories(filepath.Dir(socket), state) {
		return nil, errors.New("simulation requires separate absolute private socket and state paths")
	}
	root, err := directory(filepath.Dir(socket))
	if err != nil {
		return nil, errors.New("simulation requires a private socket directory")
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, errors.New("simulation socket directory unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0700 || !ok || owner.Uid != uint32(os.Geteuid()) {
		root.Close()
		return nil, errors.New("simulation socket directory must be owner-private")
	}
	if _, err = root.Lstat(filepath.Base(socket)); !errors.Is(err, os.ErrNotExist) {
		root.Close()
		return nil, errors.New("simulation socket exists; verify stopped owner before operator removal")
	}
	return root, nil
}

// serveAnnouncementExecutor runs separately from producer serve. Shutdown waits
// for all admitted work before releasing the store's single-writer lease.
func serveAnnouncementExecutor(ctx context.Context, socket, state string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := executorSocketParent(socket, state)
	if err != nil {
		return err
	}
	defer parent.Close()
	store, err := announcementexec.Open(state)
	if err != nil {
		return err
	}
	defer store.Close()
	// A different writer may have bound the path while the store was opening.
	if _, err = parent.Lstat(filepath.Base(socket)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("simulation socket exists; verify stopped owner before operator removal")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return errors.New("simulation socket unavailable")
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		return errors.New("simulation socket permissions unavailable")
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopped := context.AfterFunc(runCtx, func() { listener.Close() })
	defer stopped()
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, 16)
	for {
		conn, e := listener.Accept()
		if e != nil {
			if errors.Is(context.Cause(runCtx), announcementexec.ErrUncertain) {
				return announcementexec.ErrUncertain
			}
			if runCtx.Err() != nil {
				return nil
			}
			return errors.New("simulation listener stopped")
		}
		select {
		case slots <- struct{}{}:
			workers.Go(func() {
				defer func() { <-slots }()
				defer conn.Close()
				callCtx, stop := context.WithTimeout(runCtx, 5*time.Second)
				defer stop()
				closeOnCancel := context.AfterFunc(callCtx, func() { conn.Close() })
				defer closeOnCancel()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				if err := serveAnnouncementConn(callCtx, conn, store); errors.Is(err, announcementexec.ErrUncertain) {
					cancel(announcementexec.ErrUncertain)
				}
			})
		default:
			conn.Close()
		}
	}
}

func serveAnnouncementConn(ctx context.Context, conn net.Conn, store *announcementexec.Store) error {
	reader := bufio.NewReader(io.LimitReader(conn, executorFrameLimit+1))
	raw, err := reader.ReadBytes('\n')
	if err != nil || len(raw) > executorFrameLimit || reader.Buffered() != 0 {
		return nil
	}
	scope, call, err := decodeAnnouncementRequest(raw)
	if err != nil {
		return nil
	}
	result, err := store.Execute(ctx, scope, call)
	response := struct {
		Version int            `json:"version"`
		CallID  string         `json:"call_id"`
		IsError bool           `json:"is_error"`
		Output  map[string]any `json:"output,omitempty"`
		Error   string         `json:"error,omitempty"`
	}{Version: 1, CallID: call.ID, IsError: err != nil, Output: result.Output}
	if err != nil {
		// Native Sessions can stamp EXECUTOR_ERROR even if the ledger committed a
		// receipt. This response never claims absence or permission to retry.
		response.Output = nil
		response.Error = "simulation outcome unavailable; reconcile durable state before retry"
	}
	json.NewEncoder(conn).Encode(response)
	return err
}
