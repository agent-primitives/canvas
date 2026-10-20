package sharedspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Policy declares portable document paths. Globs are input-only; models never
// choose a concrete target beneath a glob through the proposal boundary.
type Policy struct {
	Documents []string `json:"documents"`
	Inputs    []string `json:"inputs"`
}

func portableDocument(value string, glob bool) bool {
	if value == "" || len(value) > 1024 || path.Clean(value) != value || strings.HasPrefix(value, "/") {
		return false
	}
	for i, part := range strings.Split(value, "/") {
		if part == "" || len(part) > 255 || strings.HasPrefix(part, ".") || strings.Contains(part, "..") {
			return false
		}
		for j, c := range part {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
				continue
			}
			if c == '*' && glob && j == 0 && i == strings.Count(value, "/") && strings.HasPrefix(part, "*.") && len(part) > 2 {
				continue
			}
			return false
		}
	}
	return true
}

func (p Policy) Validate() error {
	if len(p.Documents) == 0 || len(p.Documents) > 128 || len(p.Inputs) > 128 {
		return errors.New("declared document policy must be bounded and nonempty")
	}
	seen := map[string]bool{}
	for _, document := range p.Documents {
		folded := strings.ToLower(document)
		if !portableDocument(document, true) || seen[folded] {
			return errors.New("invalid or duplicate declared document path")
		}
		seen[folded] = true
		if strings.Contains(document, "*") && !slices.Contains(p.Inputs, document) {
			return errors.New("document globs must be declared inputs")
		}
	}
	for i, document := range p.Documents {
		for _, other := range p.Documents[:i] {
			if documentOverlap(document, other) || documentOverlap(other, document) {
				return errors.New("declared document paths overlap")
			}
		}
	}
	seen = map[string]bool{}
	for _, input := range p.Inputs {
		if !slices.Contains(p.Documents, input) || seen[input] {
			return errors.New("input must name a unique declared document pattern")
		}
		seen[input] = true
	}
	return nil
}

// Compare ancestors too: an input glob may name a file another path treats as
// a directory. Fold case so portable declarations are safe on either filesystem.
func documentOverlap(pattern, candidate string) bool {
	pattern, candidate = strings.ToLower(pattern), strings.ToLower(candidate)
	for candidate != "." {
		if matched, _ := path.Match(pattern, candidate); matched {
			return true
		}
		candidate = path.Dir(candidate)
	}
	return false
}

func matchesDocument(patterns []string, candidate string) bool {
	if !portableDocument(candidate, false) {
		return false
	}
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, candidate); matched {
			return true
		}
	}
	return false
}
func (p Policy) Allows(candidate string) bool  { return matchesDocument(p.Documents, candidate) }
func (p Policy) IsInput(candidate string) bool { return matchesDocument(p.Inputs, candidate) }

func preparedPolicy(policy Policy) (*Policy, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	policy.Documents = append([]string{}, policy.Documents...)
	policy.Inputs = append([]string{}, policy.Inputs...)
	slices.Sort(policy.Documents)
	slices.Sort(policy.Inputs)
	return &policy, nil
}

func OpenWithPolicy(root string, seed map[string]string, policy Policy) (*GitStore, error) {
	p, err := preparedPolicy(policy)
	if err != nil {
		return nil, err
	}
	return open(root, seed, p)
}

// PreflightPolicy checks binding without initializing a new workspace. The
// producer lifecycle lock covers startup; OpenWithPolicy checks again on open.
func PreflightPolicy(root string, policy Policy) error {
	p, err := preparedPolicy(policy)
	if err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" {
		return errors.New("invalid workspace root")
	}
	root, err = resolveRoot(root)
	if err != nil {
		return errors.New("invalid workspace root")
	}
	state := filepath.Join(root, ".sharedspace")
	s := GitStore{root: root, state: state, repo: filepath.Join(state, "git"), policy: p}
	return s.checkPolicy(false)
}

// bindPolicy prevents a broader declaration from silently adopting old state.
// Legacy workspaces have no policy file; neither mode can adopt the other.
func (s *GitStore) bindPolicy() error { return s.checkPolicy(true) }

func (s *GitStore) checkPolicy(create bool) error {
	name := filepath.Join(s.state, "policy.json")
	if err := checkNoSymlinks(s.root, name); err != nil {
		return err
	}
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		if s.policy == nil {
			return nil
		}
		if _, err := os.Lstat(s.repo); !errors.Is(err, os.ErrNotExist) {
			return errors.New("existing workspace has no declared policy; use separate state")
		}
		if !create {
			return nil
		}
		data, err := json.Marshal(s.policy)
		if err != nil {
			return err
		}
		return atomicWrite(s.root, name, data)
	}
	if err != nil {
		return errors.New("cannot inspect workspace policy")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<10 {
		return errors.New("workspace policy is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (512<<10)+1))
	expected, encodeErr := json.Marshal(s.policy)
	if err != nil || encodeErr != nil || s.policy == nil || !bytes.Equal(data, expected) {
		return errors.New("workspace policy changed; use separate state")
	}
	return nil
}

func (s *GitStore) Paths(revision string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return nil, err
	}
	if revision == "" {
		var err error
		revision, err = s.head()
		if err != nil {
			return nil, err
		}
	}
	if err := s.validateCommit(revision); err != nil {
		return nil, err
	}
	return s.treePaths(revision)
}

// WriteInput publishes operator-supplied source bytes, never accepted outputs.
// The watcher separately checkpoints and queues a durable, deduplicated intent.
func (s *GitStore) WriteInput(filename, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if s.policy == nil || !s.policy.IsInput(filename) || len(content) > 64<<10 {
		return errors.New("only bounded declared inputs may be written")
	}
	if err := s.validatePath(filename); err != nil {
		return err
	}
	target := filepath.Join(s.root, filepath.FromSlash(filename))
	if err := ensureDirectory(s.root, filepath.Dir(target)); err != nil {
		return err
	}
	syncDir := s.syncInputDirectory
	if syncDir == nil {
		syncDir = syncDirectory
	}
	published, err := publishFile(s.root, target, []byte(content), syncDir)
	if published && err != nil {
		return committed(err)
	}
	return err
}

func (s *GitStore) inputPath(candidate string) bool {
	if s.policy != nil {
		return s.policy.IsInput(candidate)
	}
	return strings.HasPrefix(candidate, "injuries/")
}
