package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type hostingRecord struct {
	Version          int    `json:"version"`
	InstanceID       string `json:"instanceID"`
	DefinitionDigest string `json:"definitionDigest"`
	SessionsEndpoint string `json:"sessionsEndpoint"`
}

var errHosting = errors.New("private hosted state is invalid or incompatible; use separate prepared state, never reset/adopt existing history")

// Only bounded, private staging files in the nonce format we generate are
// disposable. A broad prefix rule could hide unmarked operator/history data.
func hostingScratch(entry fs.DirEntry) bool {
	nonce, ok := strings.CutPrefix(entry.Name(), ".hosting-")
	if !ok || len(nonce) < 26 || len(nonce) > 128 || strings.Trim(nonce, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" || !entry.Type().IsRegular() {
		return false
	}
	info, err := entry.Info()
	return err == nil && info.Mode() == 0600 && info.Size() <= 4096
}

// This read-only check precedes connections, locks and all state creation. A
// matching marker with no ledgers is a recoverable partial initialization.
func preflightState(root string, want hostingRecord) error {
	parent, err := os.Stat(filepath.Dir(root))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0222 == 0 {
		return errors.New("operator-prepared writable parent required")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSetuid|os.ModeSticky) != 0700 {
		return errHosting
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return errHosting
		}
		return nil
	}); err != nil {
		return err
	}
	marker := filepath.Join(root, "hosting.json")
	info, err = os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		entries, e := os.ReadDir(root)
		if e != nil {
			return errHosting
		}
		for _, entry := range entries {
			if !hostingScratch(entry) && (entry.Name() != ".producer.lock" || !entry.Type().IsRegular()) {
				return errHosting
			}
		}
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errHosting
	}
	raw, err := readRegular(marker, 4096)
	if err != nil {
		return errHosting
	}
	var got hostingRecord
	if decodeClosed(raw, &got) != nil || got != want {
		return errHosting
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "hosting.json", ".producer.lock":
			if !entry.Type().IsRegular() {
				return errHosting
			}
		case "canvas", "space":
			if !entry.IsDir() {
				return errHosting
			}
		default:
			if !hostingScratch(entry) {
				return errHosting
			}
		}
	}
	return nil
}

func acquireState(root string, want hostingRecord) (func(), error) {
	if err := preflightState(root, want); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0700); err == nil {
		// A setgid mount parent propagates setgid even with mkdir(0700). Secure
		// only the child we just created; existing paths are never repaired.
		if err = os.Chmod(root, 0700); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := preflightState(root, want); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(root, ".producer.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("producer lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "producer lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errHosting
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("producer is active; fence/stop the old writer before recovery")
	}
	if err = preflightState(root, want); err != nil {
		file.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { file.Close() }) }, nil
}

// Publish without replacement, after fsync, under the stable lifecycle lock.
// Endpoint binding is configuration identity, NOT Sessions backend identity.
func bindHosting(root string, want hostingRecord) error {
	if err := preflightState(root, want); err != nil {
		return err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		if hostingScratch(entry) {
			if err := dir.Remove(entry.Name()); err != nil {
				return err
			}
			removed = true
		}
	}
	if removed {
		if err := syncHostingDirectory(dir); err != nil {
			return err
		}
	}
	if _, err := dir.Lstat("hosting.json"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	name := ".hosting-" + rand.Text()
	file, err := dir.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(name)
	raw, err := json.Marshal(want)
	if err != nil {
		file.Close()
		return err
	}
	_, writeErr := file.Write(append(raw, '\n'))
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.Join(writeErr, syncErr, closeErr)
	}
	if err = dir.Link(name, "hosting.json"); err != nil {
		return err
	}
	if err = dir.Remove(name); err != nil {
		return err
	}
	return syncHostingDirectory(dir)
}

func syncHostingDirectory(dir *os.Root) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
