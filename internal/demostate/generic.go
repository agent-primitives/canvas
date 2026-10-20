package demostate

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// PreflightGeneric refuses legacy or ambiguous state without creating, securing,
// or opening a journal. It is an early observation, not a replacement for the
// producer lifecycle lock, exact definition/policy bindings, or host validation.
func PreflightGeneric(private string) error {
	root, err := privateRoot(private)
	if err != nil {
		return err
	}
	lock := filepath.Join(root, ".producer.lock")
	if err := checkPath(root, lock); err != nil {
		return err
	}
	if info, err := os.Lstat(lock); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("producer lock is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect producer lock")
	}
	selected, err := Resolve(root)
	if err != nil {
		return err
	}
	// A reset pointer must not hide legacy evidence in the original producer.
	if err := validateProducer(root, root, false); err != nil {
		return err
	}
	for _, producer := range []string{root, selected} {
		if err := genericProducer(root, producer); err != nil {
			return err
		}
		if selected == root {
			break
		}
	}
	return genericSessions(root)
}

// readGenericFile uses the same no-link boundary as producer metadata, but
// ledgers are not pointer files: a normal attempts history may exceed 4 KiB.
func readGenericFile(root, path string, limit int64) ([]byte, error) {
	if err := checkPath(root, path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, errors.New("cannot inspect generic state file")
	}
	file := os.NewFile(uintptr(fd), "generic state")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("generic state metadata must be a regular file")
	}
	if limit > 0 {
		if info.Size() > limit {
			return nil, errors.New("workspace policy is not a bounded regular file")
		}
		return io.ReadAll(io.LimitReader(file, limit+1))
	}
	return io.ReadAll(file)
}
func genericProducer(root, producer string) error {
	data, err := readGenericFile(root, filepath.Join(producer, "canvas", "attempts.json"), 0)
	if err == nil {
		var ledger struct {
			DefinitionDigest string `json:"definitionDigest"`
		}
		if json.Unmarshal(data, &ledger) != nil || ledger.DefinitionDigest == "" {
			return errors.New("legacy or invalid canvas ledger; use separate state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := filepath.Join(producer, "space", ".sharedspace")
	data, err = readGenericFile(root, filepath.Join(state, "policy.json"), 512<<10)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(state, "git")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("existing workspace has no declared policy; use separate state")
		}
		return nil
	}
	if err != nil {
		return err
	}
	var policy sharedspace.Policy
	if json.Unmarshal(data, &policy) != nil || policy.Validate() != nil {
		return errors.New("invalid workspace policy; use separate state")
	}
	return nil
}
func genericSessions(root string) error {
	sessions := filepath.Join(root, "sessions")
	if err := directory(root, sessions, false); err != nil {
		return err
	}
	// Never follow metadata links or inspect unpublished staging contents.
	if _, err := os.Lstat(sessions); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	contracts := filepath.Join(sessions, "contracts")
	if err := filepath.WalkDir(sessions, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("cannot inspect sessions state")
		}
		if err := safeEntry(entry); err != nil {
			return err
		}
		if path == filepath.Join(contracts, ".staging") && entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}); err != nil {
		return err
	}
	if err := directory(root, contracts, false); err != nil {
		return err
	}
	entries, err := os.ReadDir(contracts)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect contract archive")
	}
	count := 0
	for _, entry := range entries {
		if entry.Name() == ".staging" && entry.IsDir() {
			continue
		}
		if !entry.IsDir() {
			return errors.New("invalid contract archive entry; use separate state")
		}
		bundle, err := contract.Load(filepath.Join(contracts, entry.Name(), "agent.yaml"))
		if err != nil || bundle.Suite == nil || bundle.Digest != entry.Name() {
			return errors.New("legacy or invalid contract archive; use separate state")
		}
		count++
	}
	for _, name := range []string{"journal.db", "journal.db-wal", "journal.db-shm"} {
		info, err := os.Lstat(filepath.Join(sessions, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("invalid sessions journal path")
		}
		if count == 0 {
			return errors.New("ambiguous sessions journal without native archives; use separate state")
		}
	}
	return nil
}
