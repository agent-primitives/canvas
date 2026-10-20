package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStatePreflightNoAdoptionOrPermissionRepair(t *testing.T) {
	for _, kind := range []string{"unmarked", "symlink", "public", "setgid-existing", "readonly", "bad-hosting", "wrong-instance", "wrong-definition", "wrong-endpoint"} {
		t.Run(kind, func(t *testing.T) {
			cfg := validConfig(t)
			root := cfg.StateDirectory
			want := hostingRecord{Version: 1, InstanceID: cfg.InstanceID, DefinitionDigest: "digest", SessionsEndpoint: cfg.SessionsEndpoint}
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			got := want
			switch kind {
			case "unmarked":
				os.WriteFile(filepath.Join(root, "old"), []byte("history"), 0600)
			case "symlink":
				os.Symlink(t.TempDir(), filepath.Join(root, "canvas"))
			case "public":
				os.Chmod(root, 0755)
			case "setgid-existing":
				os.Chmod(root, os.ModeSetgid|0700)
			case "readonly":
				os.Chmod(root, 0500)
			case "bad-hosting":
				os.WriteFile(filepath.Join(root, "hosting.json"), []byte(`{"version":null}`), 0600)
			case "wrong-instance":
				got.InstanceID = "other"
			case "wrong-definition":
				got.DefinitionDigest = "other"
			case "wrong-endpoint":
				got.SessionsEndpoint = "127.0.0.1:4321"
			}
			if kind == "wrong-instance" || kind == "wrong-definition" || kind == "wrong-endpoint" {
				raw, _ := json.Marshal(got)
				os.WriteFile(filepath.Join(root, "hosting.json"), raw, 0600)
			}
			before, _ := os.Stat(root)
			entries, _ := os.ReadDir(root)
			if err := preflightState(root, want); err == nil {
				t.Fatal("unsafe state accepted")
			}
			after, _ := os.Stat(root)
			newEntries, _ := os.ReadDir(root)
			if before.Mode() != after.Mode() || len(entries) != len(newEntries) {
				t.Fatal("preflight changed state")
			}
		})
	}
}

func TestHostingBootstrapScratchRecovery(t *testing.T) {
	for _, phase := range []string{"empty", "partial", "linked"} {
		t.Run(phase, func(t *testing.T) {
			cfg := validConfig(t)
			root := cfg.StateDirectory
			want := hostingRecord{Version: 1, InstanceID: cfg.InstanceID, DefinitionDigest: "digest", SessionsEndpoint: cfg.SessionsEndpoint}
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			raw := []byte{}
			if phase == "partial" {
				raw = []byte(`{"version":`)
			}
			if phase == "linked" {
				raw, _ = json.Marshal(want)
				if err := os.WriteFile(filepath.Join(root, "hosting.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			scratch := filepath.Join(root, ".hosting-"+rand.Text())
			if err := os.WriteFile(scratch, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := preflightState(root, want); err != nil {
				t.Fatalf("crashed bootstrap refused: %v", err)
			}
			if _, err := os.Stat(scratch); err != nil {
				t.Fatal("read-only preflight removed scratch")
			}
			release, err := acquireState(root, want)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err := acquireState(root, want); err == nil {
				t.Fatal("concurrent bootstrap accepted")
			}
			if _, err := os.Stat(scratch); err != nil {
				t.Fatal("scratch removed before locked publication")
			}
			if err := bindHosting(root, want); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scratch remains: %v", err)
			}
			if err := preflightState(root, want); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostingBootstrapRejectsUnknownScratch(t *testing.T) {
	for _, marked := range []bool{false, true} {
		for _, kind := range []string{"malformed", "symlink", "directory", "nonprivate", "oversize", "history"} {
			t.Run(kind+map[bool]string{false: "-unmarked", true: "-marked"}[marked], func(t *testing.T) {
				cfg := validConfig(t)
				root := cfg.StateDirectory
				want := hostingRecord{Version: 1, InstanceID: cfg.InstanceID, DefinitionDigest: "digest", SessionsEndpoint: cfg.SessionsEndpoint}
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				if marked {
					raw, _ := json.Marshal(want)
					if err := os.WriteFile(filepath.Join(root, "hosting.json"), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				name := ".hosting-" + rand.Text()
				if kind == "malformed" {
					name = ".hosting-x"
				}
				scratch := filepath.Join(root, name)
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(t.TempDir(), scratch)
				case "directory":
					err = os.Mkdir(scratch, 0700)
				default:
					data := []byte("partial")
					if kind == "oversize" {
						data = make([]byte, 4097)
					}
					err = os.WriteFile(scratch, data, 0600)
					if err == nil && kind == "nonprivate" {
						err = os.Chmod(scratch, 0644)
					}
					if err == nil && kind == "history" {
						err = os.WriteFile(filepath.Join(root, "old-history"), []byte("retain"), 0600)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := preflightState(root, want); !errors.Is(err, errHosting) {
					t.Fatalf("want hosting refusal, got %v", err)
				}
				if _, err := os.Lstat(scratch); err != nil {
					t.Fatal("refusal removed unknown data")
				}
			})
		}
	}
}

func TestPrivateChildSetgidParentAndPartialBootstrap(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, os.ModeSetgid|0775); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(parent)
	cfg := validConfig(t)
	cfg.StateDirectory = filepath.Join(parent, "state")
	want := hostingRecord{Version: 1, InstanceID: cfg.InstanceID, DefinitionDigest: "digest", SessionsEndpoint: cfg.SessionsEndpoint}
	if err := preflightState(cfg.StateDirectory, want); err != nil {
		t.Fatal(err)
	}
	release, err := acquireState(cfg.StateDirectory, want)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(cfg.StateDirectory)
	if info.Mode()&os.ModePerm != 0700 || info.Mode()&os.ModeSetgid != 0 {
		t.Fatalf("child mode %v", info.Mode())
	}
	if err := bindHosting(cfg.StateDirectory, want); err != nil {
		t.Fatal(err)
	}
	release()
	after, _ := os.Stat(parent)
	a, b := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if before.Mode() != after.Mode() || a.Uid != b.Uid || a.Gid != b.Gid {
		t.Fatal("parent changed")
	}
	release, err = acquireState(cfg.StateDirectory, want)
	if err != nil {
		t.Fatalf("partial bootstrap: %v", err)
	}
	defer release()
	if err := bindHosting(cfg.StateDirectory, want); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireState(cfg.StateDirectory, want); err == nil {
		t.Fatal("concurrent producer accepted")
	}
}
