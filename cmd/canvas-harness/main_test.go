package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harnesswire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type testInventory struct {
	Version int         `json:"version"`
	Entries []testEntry `json:"entries"`
}
type testEntry struct {
	Bundle  string `json:"bundle"`
	Address string `json:"address"`
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a := l.Addr().String()
	l.Close()
	return a
}
func fixture(t *testing.T) (string, testInventory) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "prepared")
	if err := run(t.Context(), []string{"prepare", "-definition", "../../examples/northwind-prd/canvas.yaml", "-out", out}, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, e := os.ReadFile(filepath.Join(out, "inventory.json"))
	if e != nil {
		t.Fatal(e)
	}
	var inv testInventory
	if e = json.Unmarshal(data, &inv); e != nil {
		t.Fatal(e)
	}
	return out, inv
}
func saveInventory(t *testing.T, root string, inv testInventory) string {
	t.Helper()
	data, e := json.Marshal(inv)
	if e != nil {
		t.Fatal(e)
	}
	name := filepath.Join(root, "inventory.json")
	if e = os.WriteFile(name, data, 0600); e != nil {
		t.Fatal(e)
	}
	return name
}
func assertClosed(t *testing.T, addresses ...string) {
	t.Helper()
	for _, a := range addresses {
		l, e := net.Listen("tcp", a)
		if e != nil {
			t.Errorf("listener leaked at %s: %v", a, e)
		} else {
			l.Close()
		}
	}
}

type scriptSink struct {
	api.EventSink
	Requests []api.ModelRequest
}

func (s *scriptSink) Model(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
	s.Requests = append(s.Requests, r)
	return api.ModelResponse{Message: *api.TextMessage("assistant", `{"changes":[{"document":"prd","operation":"replace","content":"script draft"}]}`)}, nil
}

// Catches archive drift, profile substitution, multiplexing identities, and lost host callbacks.
func TestPrepareServeNativeWire(t *testing.T) {
	root, inv := fixture(t)
	if inv.Version != 1 || len(inv.Entries) != 2 {
		t.Fatalf("inventory: %+v", inv)
	}
	members := map[string]bool{}
	for i, e := range inv.Entries {
		if e.Address != "127.0.0.1:"+strconv.Itoa(9000+i) {
			t.Fatalf("default address %s", e.Address)
		}
		b, err := contract.Load(filepath.Join(root, e.Bundle))
		if err != nil {
			t.Fatal(err)
		}
		if e.Bundle != "contracts/"+b.Digest+"/agent.yaml" || b.Suite == nil {
			t.Fatal("archive identity not bound")
		}
		members[b.Suite.Member] = true
		for _, p := range []string{root, filepath.Join(root, e.Bundle)} {
			info, _ := os.Stat(p)
			if info.Mode().Perm()&0077 != 0 {
				t.Fatalf("not private: %s", p)
			}
		}
		inv.Entries[i].Address = freeAddress(t)
	}
	if !members["writer"] || !members["reviewer"] {
		t.Fatal("missing separate members")
	}
	ready := freeAddress(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	file := saveInventory(t, root, inv)
	go func() {
		done <- run(ctx, []string{"serve", "-inventory", file, "-ready-listen", ready}, io.Discard)
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, e := client.Get("http://" + ready + "/readyz")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, p := range []string{"/", "/readyz/extra"} {
		resp, e := client.Get("http://" + ready + p)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("unintended health path")
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+ready+"/readyz", nil)
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("POST ready")
	}
	for _, e := range inv.Entries {
		b, err := contract.Load(filepath.Join(root, e.Bundle))
		if err != nil {
			t.Fatal(err)
		}
		conn, err := grpc.NewClient(e.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
		callCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		desc, err := remote.Describe(callCtx)
		if err != nil {
			t.Fatal(err)
		}
		if desc.ID != "bundle-v4-"+b.Digest {
			t.Fatal("wrong exact descriptor")
		}
		local, err := sessionhost.NewBundleHarness(b, 4)
		if err != nil {
			t.Fatal(err)
		}
		start := &api.Start{ExecutionID: "script-execution", SessionUID: "script-session", Inputs: []api.Message{*api.TextMessage("user", `{"mode":"documents"}`)}}
		want, got := &scriptSink{}, &scriptSink{}
		if err = local.Run(callCtx, start, want); err != nil {
			t.Fatal(err)
		}
		if err = remote.Run(callCtx, start, got); err != nil {
			t.Fatal(err)
		}
		if len(got.Requests) != 1 || !reflect.DeepEqual(want.Requests, got.Requests) {
			t.Fatal("wire changed profile-4 request")
		}
		stop()
		conn.Close()
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked")
	}
	assertClosed(t, ready, inv.Entries[0].Address, inv.Entries[1].Address)
}

// Catches permissive JSON, noncanonical paths/addresses, ambiguous identities, and partial validation.
func TestInventoryRefusalsBeforeListen(t *testing.T) {
	root, inv := fixture(t)
	a := freeAddress(t)
	ready := freeAddress(t)
	inv.Entries[0].Address = a
	inv.Entries[1].Address = freeAddress(t)
	raw, _ := json.Marshal(inv)
	cases := map[string]string{
		"empty": "", "null": "null", "empty-list": `{"version":1,"entries":[]}`, "null-list": `{"version":1,"entries":null}`,
		"null-version": `{"version":null,"entries":[]}`, "null-entry": `{"version":1,"entries":[null]}`, "null-address": strings.Replace(string(raw), `"address":"`+a+`"`, `"address":null`, 1),
		"unknown":         strings.Replace(string(raw), `"version":1`, `"extra":true,"version":1`, 1),
		"duplicate":       strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		"case":            strings.Replace(string(raw), `"version"`, `"Version"`, 1),
		"version":         strings.Replace(string(raw), `"version":1`, `"version":2`, 1),
		"entry-duplicate": strings.Replace(string(raw), `"bundle":`, `"address":"127.0.0.1:5555","bundle":`, 1),
		"entry-null":      strings.Replace(string(raw), `"bundle":"`+inv.Entries[0].Bundle+`"`, `"bundle":null`, 1),
		"entry-unknown":   strings.Replace(string(raw), `"bundle":`, `"profile":4,"bundle":`, 1),
		"trailing":        string(raw) + ` {}`, "oversize": strings.Repeat(" ", 65537),
	}
	for _, path := range []string{"../agent.yaml", "/agent.yaml", "contracts/../agent.yaml", strings.Replace(inv.Entries[0].Bundle, "contracts/", "contracts//", 1), "contracts/" + strings.Repeat("a", 64) + "/agent.yaml"} {
		cases["path-"+path] = strings.Replace(string(raw), inv.Entries[0].Bundle, path, 1)
	}
	for _, address := range []string{"localhost:9000", "[::1]:9000", "127.0.0.2:9000", "127.0.0.1:0", "127.0.0.1:09000", "127.0.0.1:http", "127.0.0.1:65536"} {
		cases[address] = strings.Replace(string(raw), a, address, 1)
	}
	dup := inv
	dup.Entries = append([]testEntry(nil), inv.Entries...)
	dup.Entries[1].Address = a
	d, _ := json.Marshal(dup)
	cases["same-address"] = string(d)
	dup.Entries[1] = dup.Entries[0]
	dup.Entries[1].Address = freeAddress(t)
	d, _ = json.Marshal(dup)
	cases["same-identity"] = string(d)
	many := inv
	many.Entries = make([]testEntry, 33)
	for i := range many.Entries {
		many.Entries[i] = inv.Entries[0]
	}
	d, _ = json.Marshal(many)
	cases["count"] = string(d)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(root, "invalid.json")
			if e := os.WriteFile(file, []byte(data), 0600); e != nil {
				t.Fatal(e)
			}
			if e := run(t.Context(), []string{"serve", "-inventory", file, "-ready-listen", ready}, io.Discard); e == nil {
				t.Fatal("accepted invalid inventory")
			}
			assertClosed(t, a, ready)
		})
	}
}

// A bad later archive must win over an already occupied first port: no binds yet.
func TestAllArchivesValidateBeforeBind(t *testing.T) {
	root, inv := fixture(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	inv.Entries[0].Address = occupied.Addr().String()
	inv.Entries[1].Address = freeAddress(t)
	if err = os.WriteFile(filepath.Join(root, inv.Entries[1].Bundle), []byte("invalid archive"), 0600); err != nil {
		t.Fatal(err)
	}
	ready := freeAddress(t)
	err = run(t.Context(), []string{"serve", "-inventory", saveInventory(t, root, inv), "-ready-listen", ready}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "inventory entry 1") {
		t.Fatalf("bind happened before complete validation: %v", err)
	}
	assertClosed(t, ready, inv.Entries[1].Address)
}

func TestUnsafeArchivesAndStartupRollback(t *testing.T) {
	for _, mode := range []string{"file-link", "directory-link", "inventory-link", "tampered", "oversize", "legacy", "later-listener-busy", "ready-busy", "ready-collision", "bad-ready"} {
		t.Run(mode, func(t *testing.T) {
			root, inv := fixture(t)
			for i := range inv.Entries {
				inv.Entries[i].Address = freeAddress(t)
			}
			ready := freeAddress(t)
			file := saveInventory(t, root, inv)
			archive := filepath.Join(root, inv.Entries[1].Bundle)
			switch mode {
			case "file-link":
				data, _ := os.ReadFile(archive)
				outside := filepath.Join(t.TempDir(), "agent.yaml")
				os.WriteFile(outside, data, 0600)
				os.Remove(archive)
				os.Symlink(outside, archive)
			case "directory-link":
				dir := filepath.Dir(archive)
				dest := filepath.Join(t.TempDir(), "moved")
				os.Rename(dir, dest)
				os.Symlink(dest, dir)
			case "inventory-link":
				other := filepath.Join(root, "link.json")
				os.Symlink(file, other)
				file = other
			case "tampered":
				os.WriteFile(archive, []byte(`{"kind":"forged"}`), 0600)
			case "oversize":
				if err := os.WriteFile(archive, []byte(strings.Repeat(" ", (16<<20)+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				data := []byte(`{"apiVersion":"kmx.kaimahi.dev/v1alpha1","kind":"PortableAgent","metadata":{"name":"legacy"},"spec":{"instructions":"Fixture only.","model":{"name":"fixture"}}}`)
				b, e := contract.Parse(data)
				if e != nil {
					t.Fatal(e)
				}
				rel := "contracts/" + b.Digest + "/agent.yaml"
				os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0700)
				os.WriteFile(filepath.Join(root, rel), data, 0600)
				inv.Entries[1].Bundle = rel
				file = saveInventory(t, root, inv)
			case "later-listener-busy", "ready-busy":
				target := ready
				if mode == "later-listener-busy" {
					target = inv.Entries[1].Address
				}
				l, e := net.Listen("tcp", target)
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			case "ready-collision":
				ready = inv.Entries[0].Address
			case "bad-ready":
				ready = "localhost:8081"
			}
			err := run(t.Context(), []string{"serve", "-inventory", file, "-ready-listen", ready}, io.Discard)
			if err == nil {
				t.Fatal("unsafe startup succeeded")
			}
			if mode == "legacy" && !strings.Contains(err.Error(), "invalid bundle identity or profile") {
				t.Fatalf("legacy profile boundary not exercised: %v", err)
			}
			if mode == "oversize" && !strings.Contains(err.Error(), "bounded regular archive file") {
				t.Fatalf("archive size boundary not exercised: %v", err)
			}
			assertClosed(t, inv.Entries[0].Address)
		})
	}
}

func TestPrepareInvalidModelLayoutAndOutputLinks(t *testing.T) {
	for _, mode := range []string{"model", "layout", "output-link", "parent-link"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.CopyFS(source, os.DirFS("../../examples/northwind-prd")); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "new")
			switch mode {
			case "model":
				file := filepath.Join(source, "suites/writer/agents/writer.json")
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(file, []byte(strings.Replace(string(data), `"model":"qwen3:8b"`, `"model":""`, 1)), 0644); err != nil {
					t.Fatal(err)
				}
			case "layout":
				if err := os.WriteFile(filepath.Join(source, "layouts/writer/index.json"), []byte(`{}`), 0644); err != nil {
					t.Fatal(err)
				}
			case "output-link":
				if err := os.Symlink(source, output); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				link := filepath.Join(root, "link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				output = filepath.Join(link, "new")
			}
			if err := run(t.Context(), []string{"prepare", "-definition", filepath.Join(source, "canvas.yaml"), "-out", output}, io.Discard); err == nil {
				t.Fatal("invalid prepare succeeded")
			}
			if mode != "output-link" {
				if _, err := os.Lstat(filepath.Join(root, "new")); !os.IsNotExist(err) {
					t.Fatal("failed prepare left output")
				}
			}
			if _, err := os.Stat(filepath.Join(source, "canvas.yaml")); err != nil {
				t.Fatal("existing source removed")
			}
		})
	}
}

func TestPrepareReleaseReviewPreservesPinnedContractBytes(t *testing.T) {
	definition := "../../examples/release-review/canvas.yaml"
	compiled, err := canvas.LoadGenericDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "release-producers")
	if err = run(t.Context(), []string{"prepare", "-definition", definition, "-out", out}, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inv testInventory
	if err = json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Version != 1 || len(inv.Entries) != 2 {
		t.Fatalf("showcase inventory: %+v", inv)
	}
	addresses := map[string]bool{}
	for _, e := range inv.Entries {
		archived, err := os.ReadFile(filepath.Join(out, e.Bundle))
		if err != nil {
			t.Fatal(err)
		}
		b, err := contract.Parse(archived)
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, original := range compiled.Bundles {
			if original.ID == b.ID {
				matched = original.Digest == b.Digest && bytes.Equal(original.Source, archived)
			}
		}
		if !matched || b.Model != "gpt-5.6-terra" || b.Suite == nil || b.Suite.Release == nil || e.Bundle != "contracts/"+b.Digest+"/agent.yaml" {
			t.Fatal("showcase model, pin or archive bytes changed")
		}
		h, err := sessionhost.NewBundleHarness(b, 4)
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := h.Describe(t.Context())
		if err != nil || descriptor.ID != "bundle-v4-"+b.Digest {
			t.Fatalf("showcase descriptor: %+v %v", descriptor, err)
		}
		if addresses[e.Address] {
			t.Fatal("showcase members share an endpoint")
		}
		addresses[e.Address] = true
	}
}

func TestPrepareFreshOnlyAndFlags(t *testing.T) {
	root, inv := fixture(t)
	old, _ := os.ReadFile(filepath.Join(root, inv.Entries[0].Bundle))
	if e := run(t.Context(), []string{"prepare", "-definition", "../../examples/northwind-prd/canvas.yaml", "-out", root}, io.Discard); e == nil {
		t.Fatal("overwrote output")
	}
	now, _ := os.ReadFile(filepath.Join(root, inv.Entries[0].Bundle))
	if string(old) != string(now) {
		t.Fatal("old contract changed")
	}
	for _, extra := range [][]string{{"-base-port", "0"}, {"-base-port", "65535"}, {"-base-port", "09000"}, {"-bind", "localhost"}, {"-definition", "missing"}} {
		out := filepath.Join(t.TempDir(), "new")
		args := append([]string{"prepare", "-definition", "../../examples/northwind-prd/canvas.yaml", "-out", out}, extra...)
		if e := run(t.Context(), args, io.Discard); e == nil {
			t.Fatal("accepted bad prepare")
		}
		if _, e := os.Lstat(out); !os.IsNotExist(e) {
			t.Fatal("failed prepare left output")
		}
	}
	out := filepath.Join(t.TempDir(), "released")
	if e := run(t.Context(), []string{"prepare", "-definition", "../../examples/northwind-released/canvas.yaml", "-out", out, "-bind", "0.0.0.0", "-base-port", "19000"}, io.Discard); e != nil {
		t.Fatal("released layout:", e)
	}
	for _, args := range [][]string{nil, {"unknown"}, {"prepare"}, {"serve"}, {"prepare", "-bogus"}, {"serve", "extra"}} {
		if e := run(t.Context(), args, io.Discard); e == nil {
			t.Fatal("accepted incomplete command")
		}
	}
}
