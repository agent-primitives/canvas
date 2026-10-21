package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harnesswire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type typedTestInventory struct {
	Version int              `json:"version"`
	Entries []map[string]any `json:"entries"`
}

// This sink records the boundary's pending call and parks it; it never executes an effect.
type announcementWireSink struct {
	api.EventSink
	calls  []api.ToolCall
	models int
	ref    api.ApprovalRef
}

func (s *announcementWireSink) Model(context.Context, api.ModelRequest) (api.ModelResponse, error) {
	s.models++
	return api.ModelResponse{}, errors.New("unexpected model request")
}
func (s *announcementWireSink) ToolCall(_ context.Context, call api.ToolCall) (api.ToolResult, error) {
	s.calls = append(s.calls, call)
	return api.ToolResult{}, &api.ApprovalParkedError{Ref: s.ref}
}

// Catches an ignored flag, archive drift, wrong deterministic identity/tool, model calls,
// lost pending-call correlation, and listeners surviving cancellation.
func TestPrepareServeSimulatedInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	if err := run(t.Context(), []string{"prepare", "-definition", "../../examples/northwind-prd/canvas.yaml", "-out", root, "-simulated-announcement"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "inventory.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var inv typedTestInventory
	if err = json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Version != 2 || len(inv.Entries) != 3 {
		t.Fatalf("typed inventory: %s", raw)
	}
	_, original := fixture(t)
	for i, entry := range inv.Entries {
		if entry["address"] != "127.0.0.1:"+strconv.Itoa(9000+i) {
			t.Fatalf("address: %v", entry)
		}
		if i < 2 {
			if len(entry) != 3 || entry["kind"] != "bundle" || entry["bundle"] != original.Entries[i].Bundle {
				t.Fatalf("bundle shape: %v", entry)
			}
			if _, err = contract.Load(filepath.Join(root, entry["bundle"].(string))); err != nil {
				t.Fatal(err)
			}
		} else if len(entry) != 2 || entry["kind"] != "simulated-announcement" {
			t.Fatalf("announcement shape: %v", entry)
		}
		entry["address"] = freeAddress(t)
	}
	raw, err = json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ready := freeAddress(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve", "-inventory", file, "-ready-listen", ready}, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("shutdown blocked")
		}
		addresses := []string{ready}
		for _, entry := range inv.Entries {
			addresses = append(addresses, entry["address"].(string))
		}
		assertClosed(t, addresses...)
	})
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, e := client.Get("http://" + ready + "/readyz")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i, entry := range inv.Entries {
		conn, e := grpc.NewClient(entry["address"].(string), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { conn.Close() })
		remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
		callCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		desc, e := remote.Describe(callCtx)
		if e != nil {
			t.Fatal(e)
		}
		if i < 2 {
			parts := strings.Split(entry["bundle"].(string), "/")
			if desc.ID != "bundle-v4-"+parts[1] {
				t.Fatalf("bundle descriptor: %+v", desc)
			}
			continue
		}
		if desc.ID != "canvas.simulated-announcement.v1" || desc.Version != "1" || len(desc.Tools) != 1 || desc.Tools[0].Name != "canvas.simulated-announcement" || desc.Tools[0].Mediation != api.MediationRequiresApproval {
			t.Fatalf("fixed descriptor: %+v", desc)
		}
		content := "Pinned café\nNothing is sent.\n"
		digest := sha256.Sum256([]byte(content))
		snapshot := announcement.Snapshot{Operation: "operation_1", Definition: strings.Repeat("a", 64), Action: "announce", Document: "announcement", Path: "documents/announcement.md", Proposal: "proposal_1", Revision: strings.Repeat("b", 40), Digest: hex.EncodeToString(digest[:]), Content: content}
		encoded, e := announcement.Encode(snapshot)
		if e != nil {
			t.Fatal(e)
		}
		sink := &announcementWireSink{ref: api.ApprovalRef{ExecutionID: "wire-execution", ToolCallID: "operation_1", RequestSeq: 7}}
		e = remote.Run(callCtx, &api.Start{SessionUID: "wire-session", ExecutionID: "wire-execution", Inputs: []api.Message{*api.TextMessage("user", string(encoded))}}, sink)
		var parked *api.ApprovalParkedError
		if !errors.As(e, &parked) || parked.Ref != sink.ref {
			t.Fatalf("pending call not preserved: %v", e)
		}
		if sink.models != 0 || len(sink.calls) != 1 {
			t.Fatalf("models=%d calls=%d", sink.models, len(sink.calls))
		}
		call := sink.calls[0]
		got, e := announcement.FromArgs(call.Args)
		if e != nil || got != snapshot || call.ID != "operation_1" || call.IdempotencyKey != "operation_1" || call.Tool != "canvas.simulated-announcement" || call.Mediation != api.MediationRequiresApproval {
			t.Fatalf("wrong pending call: %+v %v", call, e)
		}
	}
}

// Catches silently adding v2 fields or changing the default's serialized v1 shape.
func TestTypedInventoryDefaultV1Compatibility(t *testing.T) {
	root, inv := fixture(t)
	raw, err := os.ReadFile(filepath.Join(root, "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, append(want, '\n')) {
		t.Fatalf("default v1 bytes changed: %s", raw)
	}
}

// Catches a v2 parser accepting a field union instead of the exact discriminated shapes.
// An occupied first port makes binding-before-validation observable, not merely leak-free.
func TestTypedInventoryRefusalsBeforeListen(t *testing.T) {
	root, inv := fixture(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	a := occupied.Addr().String()
	b, c, ready := freeAddress(t), freeAddress(t), freeAddress(t)
	bundle := func(path, address string) string {
		raw, _ := json.Marshal(map[string]string{"kind": "bundle", "bundle": path, "address": address})
		return string(raw)
	}
	ann := `{"kind":"simulated-announcement","address":"` + c + `"}`
	first, second := bundle(inv.Entries[0].Bundle, a), bundle(inv.Entries[1].Bundle, b)
	wrap := func(entries ...string) string { return `{"version":2,"entries":[` + strings.Join(entries, ",") + `]}` }
	valid := wrap(first, second, ann)
	cases := map[string]string{
		"unknown-version":           strings.Replace(valid, `"version":2`, `"version":3`, 1),
		"v1-with-kind":              strings.Replace(valid, `"version":2`, `"version":1`, 1),
		"v2-with-v1-entry":          wrap(`{"bundle":"`+inv.Entries[0].Bundle+`","address":"`+a+`"}`, second, ann),
		"unknown-kind":              wrap(first, `{"kind":"other","address":"`+b+`"}`),
		"null-kind":                 wrap(first, `{"kind":null,"address":"`+b+`"}`),
		"missing-kind":              wrap(first, `{"address":"`+b+`"}`),
		"null-entry":                wrap(first, "null"),
		"null-entries":              `{"version":2,"entries":null}`,
		"missing-entries":           `{"version":2}`,
		"null-root":                 `null`,
		"empty-entries":             wrap(),
		"null-version":              strings.Replace(valid, `"version":2`, `"version":null`, 1),
		"missing-version":           strings.Replace(valid, `"version":2,`, "", 1),
		"duplicate-root":            strings.Replace(valid, `"version":2`, `"version":2,"version":2`, 1),
		"extra-root":                strings.Replace(valid, `"version":2`, `"version":2,"extra":true`, 1),
		"case-kind":                 wrap(first, strings.Replace(ann, `"kind"`, `"Kind"`, 1)),
		"duplicate-kind":            wrap(first, strings.Replace(ann, `"kind":`, `"kind":"bundle","kind":`, 1)),
		"null-address":              wrap(first, strings.Replace(ann, `"`+c+`"`, `null`, 1)),
		"missing-address":           wrap(first, `{"kind":"simulated-announcement"}`),
		"announcement-bundle":       wrap(first, strings.Replace(ann, `{`, `{"bundle":"`+inv.Entries[1].Bundle+`",`, 1)),
		"announcement-empty-bundle": wrap(first, strings.Replace(ann, `{`, `{"bundle":"",`, 1)),
		"announcement-null-bundle":  wrap(first, strings.Replace(ann, `{`, `{"bundle":null,`, 1)),
		"announcement-id":           wrap(first, strings.Replace(ann, `{`, `{"id":"forged",`, 1)),
		"announcement-version":      wrap(first, strings.Replace(ann, `{`, `{"version":"1",`, 1)),
		"announcement-tool":         wrap(first, strings.Replace(ann, `{`, `{"tool":"forged",`, 1)),
		"bundle-missing-path":       wrap(first, `{"kind":"bundle","address":"`+b+`"}`),
		"bundle-null-path":          wrap(first, `{"kind":"bundle","bundle":null,"address":"`+b+`"}`),
		"duplicate-announcement":    wrap(first, ann, strings.Replace(ann, c, b, 1)),
		"duplicate-address":         wrap(first, strings.Replace(ann, c, a, 1)),
		"wildcard-overlap":          wrap(first, strings.Replace(ann, c, strings.Replace(a, "127.0.0.1", "0.0.0.0", 1), 1)),
		"ready-overlap":             wrap(first, strings.Replace(ann, c, ready, 1)),
		"duplicate-bundle":          wrap(first, bundle(inv.Entries[0].Bundle, b), ann),
		"later-invalid-bundle":      wrap(first, ann, bundle("contracts/"+strings.Repeat("f", 64)+"/agent.yaml", b)),
		"trailing":                  valid + ` {}`,
		"invalid-utf8":              strings.Replace(valid, "bundle", "bund\xffle", 1),
	}
	for _, address := range []string{"localhost:9000", "127.0.0.1:0", "127.0.0.1:09000", "127.0.0.1:65536", "[::1]:9000"} {
		cases[address] = wrap(first, strings.Replace(ann, c, address, 1))
	}
	many := []string{first}
	for range 32 {
		many = append(many, ann)
	}
	cases["too-many"] = wrap(many...)
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(root, "invalid-typed.json")
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err := run(ctx, []string{"serve", "-inventory", file, "-ready-listen", ready}, io.Discard)
			if err == nil || strings.Contains(err.Error(), "producer listener") || strings.Contains(err.Error(), "readiness listener") {
				t.Fatalf("not refused before bind: %v", err)
			}
			assertClosed(t, b, c, ready)
		})
	}
}

// The extra deterministic endpoint consumes a real port before inventory publication.
func TestTypedInventoryPreparePortRange(t *testing.T) {
	for _, simulated := range []bool{false, true} {
		out := filepath.Join(t.TempDir(), "prepared")
		args := []string{"prepare", "-definition", "../../examples/northwind-prd/canvas.yaml", "-out", out, "-base-port", "65534"}
		if simulated {
			args = append(args, "-simulated-announcement")
		}
		err := run(t.Context(), args, io.Discard)
		if !simulated && err != nil {
			t.Fatal(err)
		}
		if simulated {
			if err == nil || !strings.Contains(err.Error(), "count or port range") {
				t.Fatalf("expected range refusal: %v", err)
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("failed preparation left output")
			}
		}
	}
}

// A v2 inventory with only the deterministic entry needs no bundle archive.
func TestTypedInventoryAnnouncementOnly(t *testing.T) {
	root := t.TempDir()
	a, ready := freeAddress(t), freeAddress(t)
	raw := `{"version":2,"entries":[{"kind":"simulated-announcement","address":"` + a + `"}]}`
	file := filepath.Join(root, "inventory.json")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, file, ready) }()
	conn, err := grpc.NewClient(a, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.WaitForReady(true)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	callCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	desc, err := harnesswire.NewClientHarness(v1.NewHarnessClient(conn)).Describe(callCtx)
	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Error(serveErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked")
	}
	if err != nil || !reflect.DeepEqual([]string{desc.ID, desc.Version}, []string{"canvas.simulated-announcement.v1", "1"}) {
		t.Fatalf("announcement-only: %+v %v", desc, err)
	}
	assertClosed(t, a, ready)
}
