package sharedspace

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// testGitHook lets tests see, count or fail Git commands. Always nil outside tests.
var testGitHook func(args []string) error

// git uses argv only for validated identifiers/paths. Document bytes and commit
// messages always go through stdin, and stderr is deliberately never surfaced.
func (s *GitStore) git(index string, input io.Reader, args ...string) (string, error) {
	if testGitHook != nil && testGitHook(args) != nil {
		return "", errors.New("scratch Git operation failed")
	}
	commandArgs := []string{"--git-dir=" + s.repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false", "-c", "core.logAllRefUpdates=false", "-c", "core.fsync=committed"}
	cmd := exec.Command("git", append(commandArgs, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=SharedSpace", "GIT_AUTHOR_EMAIL=sharedspace@localhost", "GIT_COMMITTER_NAME=SharedSpace", "GIT_COMMITTER_EMAIL=sharedspace@localhost", "GIT_TERMINAL_PROMPT=0")
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.Stdin = input
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
		return "", errors.New("scratch Git operation failed")
	}
	return out.String(), nil
}
func (s *GitStore) commit(tree string, parents []string, message string) (string, error) {
	args := []string{"commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	out, e := s.git("", strings.NewReader(message), args...)
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(out), nil
}
func (s *GitStore) updateRef(ref, value, old string) error {
	if old == "" {
		old = strings.Repeat("0", 40)
	}
	_, e := s.git("", nil, "update-ref", ref, value, old)
	return e
}
func (s *GitStore) buildTree(base string, changes map[string]string) (string, error) {
	file, e := os.CreateTemp(s.state, "index-")
	if e != nil {
		return "", errors.New("cannot create private Git index")
	}
	index := file.Name()
	file.Close()
	if e = os.Remove(index); e != nil {
		return "", errors.New("cannot initialize private Git index")
	}
	defer os.Remove(index)
	defer os.Remove(index + ".lock")
	if base == "" {
		_, e = s.git(index, nil, "read-tree", "--empty")
	} else {
		_, e = s.git(index, nil, "read-tree", base)
	}
	if e != nil {
		return "", e
	}
	// Index entries contain only file names and object IDs, never document contents.
	var entries strings.Builder
	for _, path := range sortedPaths(changes) {
		blob, e := s.git("", strings.NewReader(changes[path]), "hash-object", "-w", "--stdin")
		if e != nil {
			return "", e
		}
		entries.WriteString("100644 " + strings.TrimSpace(blob) + "\t" + path + "\x00")
	}
	if _, e = s.git(index, strings.NewReader(entries.String()), "update-index", "-z", "--index-info"); e != nil {
		return "", e
	}
	tree, e := s.git(index, nil, "write-tree")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(tree), nil
}
func (s *GitStore) treePaths(revision string) ([]string, error) {
	out, e := s.git("", nil, "ls-tree", "-r", "-z", revision)
	if e != nil {
		return nil, e
	}
	paths := make([]string, 0)
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid document tree")
		}
		meta := strings.Fields(parts[0])
		if len(meta) != 3 || meta[0] != "100644" || meta[1] != "blob" {
			return nil, errors.New("document tree contains a non-regular file")
		}
		if e := s.validatePath(parts[1]); e != nil {
			return nil, e
		}
		paths = append(paths, parts[1])
	}
	return paths, nil
}
func (s *GitStore) validateTree(revision string) error { _, e := s.treePaths(revision); return e }
func (s *GitStore) project(revision string) error {
	paths, e := s.treePaths(revision)
	if e != nil {
		return e
	}
	for _, path := range paths {
		content, e := s.read(revision, path)
		if e != nil {
			return e
		}
		target := filepath.Join(s.root, filepath.FromSlash(path))
		if e = ensureDirectory(s.root, filepath.Dir(target)); e != nil {
			return e
		}
		current, e := os.ReadFile(target)
		// Restore missing sources, but never rewind existing declared inputs
		// (legacy injuries in the default policy) to checkpointed bytes.
		if e == nil && (s.inputPath(path) || string(current) == content) {
			continue
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return errors.New("cannot read workspace document")
		}
		if e = atomicWrite(s.root, target, []byte(content)); e != nil {
			return e
		}
	}
	// No checkout/reset/clean: projection updates accepted documents and restores
	// missing injury sources; unrelated files and existing reports stay untouched.
	return nil
}
