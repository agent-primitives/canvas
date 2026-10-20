// Package demostate resolves historical producer generations beneath a private
// state root and guards active producers with a stable lifecycle lock.
package demostate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var ErrActive = errors.New("producer is active; stop it normally before opening this state")

const pointerName = "producer-current.json"

type pointer struct {
	Generation string `json:"generation"`
}

type archive struct {
	Previous string `json:"previous"`
	Space    string `json:"space"`
	Canvas   string `json:"canvas"`
}

// Acquire takes the stable, nonblocking exclusive producer lifecycle lock.
// Producers must hold this during startup binding checks and while using the
// resolved producer, until canvas and SharedSpace have closed. Read-only refusal
// preflights may inspect Resolve before acquiring it. Sessions-only hosts must not use it.
// The lock file is never removed: replacing it would break mutual exclusion.
func Acquire(private string) (func(), error) {
	root, err := privateRoot(private)
	if err != nil {
		return nil, err
	}
	if err = makeDirectory(root, root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".producer.lock")
	if err = checkPath(root, path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("cannot open producer lock")
	}
	file := os.NewFile(uintptr(fd), "producer lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("producer lock is not a regular file")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrActive
		}
		return nil, errors.New("cannot acquire producer lock")
	}
	if err = os.Chmod(root, 0700); err != nil {
		file.Close()
		return nil, errors.New("cannot protect private state")
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, errors.New("cannot protect producer lock")
	}
	var once sync.Once
	return func() { once.Do(func() { _ = file.Close() }) }, nil
}

// Resolve returns the active producer root, or private for the unchanged legacy
// layout when no pointer exists. It does not create state and may be inspected
// by read-only preflights. A producer must re-resolve under Acquire before using
// the path for startup and hold that lock for its lifetime; Resolve does not lock.
func Resolve(private string) (string, error) {
	root, err := privateRoot(private)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, pointerName)
	data, err := readMetadata(root, path)
	if errors.Is(err, os.ErrNotExist) {
		if err = validateProducer(root, root, false); err != nil {
			return "", err
		}
		return root, nil
	}
	if err != nil {
		return "", err
	}
	var p pointer
	if err = decodeMetadata(data, &p); err != nil || !validGeneration(p.Generation) {
		return "", errors.New("invalid producer pointer")
	}
	generation := filepath.Join(root, filepath.FromSlash(p.Generation))
	if err = validateProducer(root, generation, true); err != nil {
		return "", err
	}
	data, err = readMetadata(root, filepath.Join(generation, "archive.json"))
	if err != nil {
		return "", errors.New("generation has no valid archive manifest")
	}
	var a archive
	if err = decodeMetadata(data, &a); err != nil || (a.Previous != "." && !validGeneration(a.Previous)) || a.Space != filepath.ToSlash(filepath.Join(a.Previous, "space")) || a.Canvas != filepath.ToSlash(filepath.Join(a.Previous, "canvas")) {
		return "", errors.New("invalid archive manifest")
	}
	return generation, nil
}

func validGeneration(path string) bool {
	const prefix = "demo-generations/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	id := strings.TrimPrefix(path, prefix)
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// privateRoot makes private absolute and follows symlinks in the directories
// above it, so a checkout reached through a linked directory still works. The
// state directory itself and everything below it must not be symlinks.
func privateRoot(private string) (string, error) {
	if strings.TrimSpace(private) == "" {
		return "", errors.New("invalid private state root")
	}
	root, err := filepath.Abs(private)
	if err != nil || root == string(filepath.Separator) {
		return "", errors.New("invalid private state root")
	}
	root, err = resolveParents(root)
	if err != nil {
		return "", err
	}
	if err = directory(root, root, false); err != nil {
		return "", err
	}
	return root, nil
}

// resolveParents follows symlinks in the existing directories above path and
// keeps any missing ones as written. A dangling link above path is refused.
func resolveParents(path string) (string, error) {
	dir, tail := filepath.Dir(path), []string{filepath.Base(path)}
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		}
		if _, statErr := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) || !errors.Is(statErr, os.ErrNotExist) {
			return "", errors.New("cannot resolve private state path")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("cannot resolve private state path")
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
	}
}

// checkPath checks path and each parent up to and including root, including
// dangling links. Directories above root were resolved by privateRoot. As in
// SharedSpace, a same-user process racing to swap paths is out of scope here.
func checkPath(root, path string) error {
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("unsafe private state path")
	}
	for path = filepath.Clean(path); ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errors.New("unsafe private state path")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect private state path")
		}
		if path == root {
			return nil
		}
	}
}

// safeEntry checks a directory entry found by WalkDir, whose parents were
// already checked. WalkDir does not follow links, so the entry type is enough.
func safeEntry(entry fs.DirEntry) error {
	if entry.Type()&fs.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
		return errors.New("unsafe private state path")
	}
	return nil
}

func directory(root, path string, required bool) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if !required && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.New("private state directory is missing or invalid")
	}
	return nil
}

func makeDirectory(root, path string) error {
	if err := directory(root, path, false); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("cannot create private state directory")
	}
	return nil
}

func validateProducer(private, root string, required bool) error {
	if err := directory(private, root, required); err != nil {
		return err
	}
	for _, name := range []string{"space", "canvas"} {
		path := filepath.Join(root, name)
		if err := directory(private, path, required); err != nil {
			return err
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return errors.New("cannot inspect producer state")
			}
			return safeEntry(entry)
		}); err != nil {
			return err
		}
	}
	return nil
}

func readMetadata(root, path string) ([]byte, error) {
	if err := checkPath(root, path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, errors.New("cannot read producer metadata")
	}
	file := os.NewFile(uintptr(fd), "producer metadata")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("invalid producer metadata file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return nil, errors.New("cannot read producer metadata")
	}
	return data, nil
}

func decodeMetadata(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errors.New("invalid producer metadata")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid producer metadata")
	}
	return nil
}
