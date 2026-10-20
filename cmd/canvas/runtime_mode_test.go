package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRuntimeModeFreshFixtureAndReopen(t *testing.T) {
	for _, missing := range []bool{false, true} {
		root := t.TempDir()
		if missing {
			root = filepath.Join(root, "new-state")
		}
		binding, err := checkRuntimeMode(root, true)
		if err != nil {
			t.Fatal(err)
		}
		if missing {
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatal("read-only mode check created state")
			}
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
		}
		// The regular producer acquires its lock after the cold-state check.
		if err := os.WriteFile(filepath.Join(root, ".producer.lock"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := binding.Bind(); err != nil {
			t.Fatal(err)
		}
		before := startupTree(t, root)
		reopen, err := checkRuntimeMode(root, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := reopen.Bind(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, startupTree(t, root)) {
			t.Fatal("reopen rewrote state")
		}
	}
}

func TestRuntimeModeUnmarkedExistingRealOnly(t *testing.T) {
	root := t.TempDir()
	history := filepath.Join(root, "history.txt")
	if err := os.WriteFile(history, []byte("existing history"), 0600); err != nil {
		t.Fatal(err)
	}
	before := startupTree(t, root)
	if _, err := checkRuntimeMode(root, true); !errors.Is(err, errRuntimeMode) {
		t.Fatalf("fixture adopted unmarked history: %v", err)
	}
	if !reflect.DeepEqual(before, startupTree(t, root)) {
		t.Fatal("fixture refusal changed history")
	}
	binding, err := checkRuntimeMode(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Bind(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(history); err != nil || string(raw) != "existing history" {
		t.Fatal("real mode changed historical bytes")
	}
	if _, err := os.Stat(filepath.Join(root, "runtime-mode.json")); err != nil {
		t.Fatal("real mode not persisted")
	}
}

func TestRuntimeModeSwitchesRefuseUntouched(t *testing.T) {
	for _, fixture := range []bool{false, true} {
		root := t.TempDir()
		binding, err := checkRuntimeMode(root, fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := binding.Bind(); err != nil {
			t.Fatal(err)
		}
		before := startupTree(t, root)
		if _, err := checkRuntimeMode(root, !fixture); !errors.Is(err, errRuntimeMode) {
			t.Fatalf("mode switch accepted: %v", err)
		}
		if !reflect.DeepEqual(before, startupTree(t, root)) {
			t.Fatal("switch refusal changed state")
		}
	}
}

func TestRuntimeModeCorruptBindingsRefuseUntouched(t *testing.T) {
	for _, value := range []string{"broken-json", `{"schemaVersion":"wrong","mode":"fixture"}`, `{"schemaVersion":"canvas.runtime-mode/v1","mode":"other"}`, `{"schemaVersion":"canvas.runtime-mode/v1","mode":"fixture","mode":"real"}`, "symlink"} {
		for _, fixture := range []bool{false, true} {
			root := t.TempDir()
			name := filepath.Join(root, "runtime-mode.json")
			if value == "symlink" {
				target := filepath.Join(t.TempDir(), "outside-binding.json")
				if err := os.WriteFile(target, []byte(`{"schemaVersion":"canvas.runtime-mode/v1","mode":"fixture"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(name, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			before := startupTree(t, root)
			if _, err := checkRuntimeMode(root, fixture); !errors.Is(err, errRuntimeMode) {
				t.Fatalf("corrupt binding accepted: %v", err)
			}
			if !reflect.DeepEqual(before, startupTree(t, root)) {
				t.Fatal("corrupt binding was repaired/mutated")
			}
		}
	}
}

func TestRuntimeModeConcurrentPublishChoosesOneMode(t *testing.T) {
	root := t.TempDir()
	a, err := checkRuntimeMode(root, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := checkRuntimeMode(root, false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, binding := range []*runtimeModeBinding{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); results <- binding.Bind() }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, errRuntimeMode) {
			t.Fatalf("unexpected binding failure: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("admitted %d modes", success)
	}
}
