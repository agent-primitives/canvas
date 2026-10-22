package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/agent-primitives/suite/core"
)

var errRuntimeMode = errors.New("runtime mode binding is invalid or incompatible; use separate state without changing existing history")

const runtimeModeFile = "runtime-mode.json"

type runtimeModeRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	Mode          string `json:"mode"`
}

type runtimeModeBinding struct {
	private string
	mode    string
	fresh   bool
}

// Check before cache, lifecycle-lock or provider effects. Only missing/empty
// state may become a new fixture. Unmarked real state passes this mode check;
// the separate generic preflight still refuses legacy or ambiguous runtime state.
func checkRuntimeMode(private string, fixture bool) (*runtimeModeBinding, error) {
	mode := "real"
	if fixture {
		mode = "fixture"
	}
	b := &runtimeModeBinding{private: private, mode: mode}
	info, err := os.Lstat(private)
	if errors.Is(err, os.ErrNotExist) {
		b.fresh = true
		return b, nil
	}
	if err != nil || !info.IsDir() {
		return nil, errRuntimeMode
	}
	root, err := os.OpenRoot(private)
	if err != nil {
		return nil, errRuntimeMode
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errRuntimeMode
	}
	recorded, exists, err := readRuntimeMode(root)
	if err != nil || exists && recorded != mode {
		return nil, errRuntimeMode
	}
	if exists {
		return b, nil
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, errRuntimeMode
	}
	b.fresh = len(entries) == 0
	if fixture && !b.fresh {
		return nil, errRuntimeMode
	}
	return b, nil
}

func readRuntimeMode(root *os.Root) (string, bool, error) {
	info, err := root.Lstat(runtimeModeFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512 {
		return "", false, errRuntimeMode
	}
	file, err := root.OpenFile(runtimeModeFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false, errRuntimeMode
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", false, errRuntimeMode
	}
	raw, err := io.ReadAll(io.LimitReader(file, 513))
	var record runtimeModeRecord
	if err != nil || len(raw) > 512 || core.Unmarshal(raw, &record) != nil || record.SchemaVersion != "canvas.runtime-mode/v1" || record.Mode != "fixture" && record.Mode != "real" {
		return "", false, errRuntimeMode
	}
	return record.Mode, true, nil
}

// Bind publishes atomically before any host/journal activation. It rechecks
// under the normal lifecycle lock; sessions-only uses the same atomic binding
// without taking a producer lock or touching producer generations.
func (b *runtimeModeBinding) Bind() error {
	root, err := os.OpenRoot(b.private)
	if err != nil {
		return errRuntimeMode
	}
	defer root.Close()
	mode, exists, err := readRuntimeMode(root)
	if err != nil || exists && mode != b.mode {
		return errRuntimeMode
	}
	if exists {
		return nil
	}
	if b.mode == "fixture" {
		if !b.fresh {
			return errRuntimeMode
		}
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			return errRuntimeMode
		}
		for _, entry := range entries {
			if entry.Name() != ".producer.lock" || !entry.Type().IsRegular() {
				return errRuntimeMode
			}
		}
	}
	name := ".runtime-mode-" + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errRuntimeMode
	}
	defer root.Remove(name)
	raw, err := json.Marshal(runtimeModeRecord{SchemaVersion: "canvas.runtime-mode/v1", Mode: b.mode})
	if err != nil {
		file.Close()
		return errRuntimeMode
	}
	_, writeErr := file.Write(append(raw, '\n'))
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errRuntimeMode
	}
	if err := root.Link(name, runtimeModeFile); err != nil {
		mode, exists, readErr := readRuntimeMode(root)
		if readErr != nil || !exists || mode != b.mode {
			return errRuntimeMode
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return errRuntimeMode
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return errRuntimeMode
	}
	return nil
}
