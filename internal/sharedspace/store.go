// Package sharedspace keeps human-reviewed documents in a private scratch Git repository.
package sharedspace

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
)

var (
	ErrStale   = errors.New("proposal base or expected head is stale")
	ErrDecided = errors.New("proposal has already been decided")
	// ErrCommitted is wrapped into errors from a step that runs after a change
	// was already saved (reading back review state, or updating workspace
	// files). The change stands; the returned value describes it.
	ErrCommitted = errors.New("change was saved but a follow-up step failed")
)

func committed(e error) error { return fmt.Errorf("%w: %w", ErrCommitted, e) }

const acceptedRef = "refs/heads/main"

// ingestSummary is the first line of every checkpoint written by Ingest.
const ingestSummary = "Ingest data checkpoint"

var ErrMissingDocument = errors.New("document not present at revision")

// ErrProposalNotFound means no proposal has the requested ID.
var ErrProposalNotFound = errors.New("proposal not found")

type Attribution struct {
	FlowRun  string `json:"flowRun,omitempty"`
	FlowStep string `json:"flowStep,omitempty"`
	// ReadInputs freezes input collection scopes, including future matching files.
	ReadInputs           []string                `json:"readInputs,omitempty"`
	Suite                *contract.SuiteIdentity `json:"suite,omitempty"`
	Agent                string                  `json:"agent"`
	BundleDigest         string                  `json:"bundleDigest"`
	AuthoredBundleDigest string                  `json:"authoredBundleDigest,omitempty"`
	Session              string                  `json:"session"`
	Actor                string                  `json:"actor"`
	Run                  string                  `json:"run"`
	Sources              map[string]string       `json:"sources"`
	// Execution provenance is additive; zero values keep historical metadata
	// shapes. Actor remains an unverified label, never an execution owner.
	ExecutionOwner   api.IdentityRef `json:"executionOwner,omitzero"`
	RequestedBy      ReviewActor     `json:"requestedBy,omitzero"`
	TriggeredBy      ReviewActor     `json:"triggeredBy,omitzero"`
	TriggeringReview string          `json:"triggeringReview,omitempty"`
}

// ReviewActor keeps verified identity distinct from the selected workspace role.
// It is provenance, not a role-to-permission mapping.
type ReviewActor struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

func (r ReviewActor) validate() error {
	for _, value := range []string{r.Issuer, r.Subject, r.Role} {
		if strings.TrimSpace(value) == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid review identity")
		}
		for _, c := range value {
			if c < 32 || c == 127 {
				return errors.New("invalid review identity")
			}
		}
	}
	return validActor(r.Role)
}

type Proposal struct {
	Reviewer       ReviewActor       `json:"reviewer,omitzero"`
	ID             string            `json:"id"`
	Base           string            `json:"base"`
	Commit         string            `json:"commit"`
	Status         string            `json:"status"`
	Changes        map[string]string `json:"changes"`
	Attribution    Attribution       `json:"attribution"`
	Created        time.Time         `json:"created"`
	AcceptedCommit string            `json:"acceptedCommit,omitempty"`
}
type Checkpoint struct {
	Reviewer ReviewActor `json:"reviewer,omitzero"`
	Commit   string      `json:"commit"`
	Summary  string      `json:"summary"`
	Proposal string      `json:"proposal"`
	Actor    string      `json:"actor"`
}

// SharedSpace is the canvas-owned review boundary; Git is not the session journal.
type SharedSpace interface {
	Head() (string, error)
	Read(revision, path string) (string, error)
	Paths(revision string) ([]string, error)
	Propose(base string, changes map[string]string, attribution Attribution) (Proposal, error)
	Accept(id, expectedHead, actor string) (Proposal, error)
	Reject(id, actor string) (Proposal, error)
	Proposals() ([]Proposal, error)
	History() ([]Checkpoint, error)
	Ingest(path, content, actor string) (string, error)
	WriteInput(path, content string) error
	Root() string
	Close() error
}

// GitStore owns an exclusive root lock until Close. All methods except Root
// serialize on mu; returned maps are reconstructed, never aliases of store state.
type GitStore struct {
	mu     sync.Mutex
	root   string
	state  string
	repo   string
	lock   *os.File
	closed bool
	policy *Policy
	// Instance-local input publication boundary; nil uses the real directory sync.
	syncInputDirectory func(string) error
	ledger             reviewLedger
	// A failed publication remains dirty even when reconstructed memory is unchanged.
	ledgerDirty bool
	// Commits never change, so parsed proposals are kept by commit ID and the
	// accepted history by the accepted head it was read from. Review status is
	// not cached; reconcile works it out from the ledger on every call.
	proposalCache map[string]Proposal
	historyHead   string
	historyCache  []historyEntry
	ingestPaths   map[string][]string
}

var _ SharedSpace = (*GitStore)(nil)

type decision struct {
	Reviewer       ReviewActor `json:"reviewer,omitzero"`
	Status         string      `json:"status"`
	Actor          string      `json:"actor"`
	AcceptedCommit string      `json:"acceptedCommit,omitempty"`
}
type reviewIntent struct {
	Reviewer ReviewActor `json:"reviewer,omitzero"`
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	Commit   string      `json:"commit,omitempty"`
	Actor    string      `json:"actor"`
}
type reviewLedger struct {
	Decisions map[string]decision `json:"decisions"`
	Intent    *reviewIntent       `json:"intent,omitempty"`
}
type proposalMetadata struct {
	ID          string      `json:"id"`
	Base        string      `json:"base"`
	Paths       []string    `json:"paths"`
	Attribution Attribution `json:"attribution"`
	Created     time.Time   `json:"created"`
}

// Open initializes an owned workspace or reopens it without applying seed again.
// The runtime should locate root beneath its private, ignored .state directory.
func Open(root string, seed map[string]string) (*GitStore, error) {
	return open(root, seed, nil)
}

func open(root string, seed map[string]string, policy *Policy) (_ *GitStore, err error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("invalid workspace root")
	}
	root, err = resolveRoot(root)
	if err != nil {
		return nil, errors.New("invalid workspace root")
	}
	if err = ensureDirectory(root, root); err != nil {
		return nil, err
	}
	if err = os.Chmod(root, 0700); err != nil {
		return nil, errors.New("cannot secure workspace root")
	}
	state := filepath.Join(root, ".sharedspace")
	if err = ensureDirectory(root, state); err != nil {
		return nil, err
	}
	if err = os.Chmod(state, 0700); err != nil {
		return nil, errors.New("cannot secure private state")
	}
	fd, err := syscall.Open(filepath.Join(state, "lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("cannot open workspace lock")
	}
	lock := os.NewFile(uintptr(fd), "sharedspace lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("workspace is already open")
	}
	s := &GitStore{root: root, state: state, policy: policy, repo: filepath.Join(state, "git"), lock: lock, ledgerDirty: true, proposalCache: make(map[string]Proposal), ingestPaths: make(map[string][]string)}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	if err = checkNoSymlinks(root, s.repo); err != nil {
		return nil, err
	}
	if err = s.bindPolicy(); err != nil {
		return nil, err
	}
	if err = s.removeUnfinishedInit(); err != nil {
		return nil, err
	}
	if _, e := os.Lstat(s.repo); errors.Is(e, os.ErrNotExist) {
		if err = s.initRepository(seed); err != nil {
			return nil, err
		}
	} else if e != nil {
		return nil, errors.New("cannot inspect scratch repository")
	}
	refs, err := s.git("", nil, "for-each-ref", "--format=%(refname)", acceptedRef)
	if err != nil {
		return nil, err
	}
	hasAcceptedRef := false
	for _, ref := range strings.Fields(refs) {
		if ref == acceptedRef {
			hasAcceptedRef = true
		}
	}
	// The repository only appears once it holds the seed commit, so a missing
	// ref here means real damage, not an interrupted first run. Never reseed.
	if !hasAcceptedRef {
		return nil, errors.New("existing scratch repository is missing accepted ref")
	}
	s.ledger.Decisions = make(map[string]decision)
	ledgerPath := filepath.Join(state, "reviews.json")
	if err = checkNoSymlinks(root, ledgerPath); err != nil {
		return nil, err
	}
	data, e := os.ReadFile(ledgerPath)
	if e == nil {
		if json.Unmarshal(data, &s.ledger) != nil {
			return nil, errors.New("invalid review ledger")
		}
		s.ledgerDirty = false
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, errors.New("cannot read review ledger")
	}
	if s.ledger.Decisions == nil {
		s.ledger.Decisions = make(map[string]decision)
	}
	if _, err = s.reconcile(); err != nil {
		return nil, err
	}
	h, err := s.head()
	if err != nil {
		return nil, err
	}
	if err = s.project(h); err != nil {
		return nil, err
	}
	return s, nil
}

const initPrefix = "git-init-"

// initRepository builds the bare repository and its seed commit in a temporary
// sibling directory and renames it into place, so a run killed part way (for
// example Ctrl-C during the first start) leaves nothing that looks initialized.
func (s *GitStore) initRepository(seed map[string]string) (err error) {
	if err = s.validateChanges(seed, false); err != nil {
		return err
	}
	final := s.repo
	temp, err := os.MkdirTemp(s.state, initPrefix)
	if err != nil {
		return errors.New("cannot create scratch repository")
	}
	s.repo = temp
	defer func() {
		s.repo = final
		if err != nil {
			os.RemoveAll(temp)
		}
	}()
	if _, err = s.git("", nil, "init", "--bare", "--initial-branch=main", "--object-format=sha1", temp); err != nil {
		return err
	}
	tree, err := s.buildTree("", seed)
	if err != nil {
		return err
	}
	commit, err := s.commit(tree, nil, "Initial accepted baseline\n")
	if err != nil {
		return err
	}
	if err = s.updateRef(acceptedRef, commit, ""); err != nil {
		return err
	}
	if err = checkNoSymlinks(s.root, final); err != nil {
		return err
	}
	if err = os.Rename(temp, final); err != nil {
		return errors.New("cannot publish scratch repository")
	}
	return syncDirectory(s.state)
}

// removeUnfinishedInit deletes temporary repositories left by an interrupted
// first run. They live in private state and are never used.
func (s *GitStore) removeUnfinishedInit() error {
	entries, err := os.ReadDir(s.state)
	if err != nil {
		return errors.New("cannot inspect private state")
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), initPrefix) {
			if os.RemoveAll(filepath.Join(s.state, entry.Name())) != nil {
				return errors.New("cannot remove unfinished scratch repository")
			}
		}
	}
	return nil
}

func (s *GitStore) ready() error {
	if s.closed {
		return errors.New("sharedspace is closed")
	}
	return nil
}
func (s *GitStore) Root() string { return s.root }
func (s *GitStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.lock == nil {
		return nil
	}
	// Closing the descriptor also releases flock, including after process exit.
	if err := s.lock.Close(); err != nil {
		return errors.New("cannot close workspace lock")
	}
	return nil
}
func (s *GitStore) Head() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return "", e
	}
	return s.head()
}
func (s *GitStore) head() (string, error) {
	out, e := s.git("", nil, "rev-parse", "--verify", acceptedRef)
	if e != nil {
		return "", e
	}
	h := strings.TrimSpace(out)
	if !validOID(h) {
		return "", errors.New("invalid accepted ref")
	}
	return h, nil
}
func (s *GitStore) Read(revision, path string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return "", e
	}
	if e := s.validatePath(path); e != nil {
		return "", e
	}
	if revision == "" {
		h, e := s.head()
		if e != nil {
			return "", e
		}
		revision = h
	}
	if e := s.validateCommit(revision); e != nil {
		return "", e
	}
	return s.read(revision, path)
}
func (s *GitStore) read(revision, path string) (string, error) {
	// A tree entry must be a regular blob, not a Git symlink or submodule.
	entry, e := s.git("", nil, "ls-tree", revision, "--", path)
	if e != nil {
		return "", e
	}
	fields := strings.Fields(strings.TrimSpace(entry))
	if len(fields) == 0 {
		return "", ErrMissingDocument
	}
	if len(fields) != 4 || fields[0] != "100644" || fields[1] != "blob" || fields[3] != path {
		return "", errors.New("document is missing or not regular")
	}
	return s.git("", nil, "cat-file", "blob", fields[2])
}

func (s *GitStore) Propose(base string, changes map[string]string, attribution Attribution) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return Proposal{}, e
	}
	if e := s.validateCommit(base); e != nil {
		return Proposal{}, e
	}
	if e := s.validateChanges(changes, true); e != nil {
		return Proposal{}, e
	}
	if s.policy != nil {
		for _, pattern := range attribution.ReadInputs {
			if !slices.Contains(s.policy.Inputs, pattern) {
				return Proposal{}, errors.New("invalid proposal input scope")
			}
		}
		for path := range changes {
			if s.policy.IsInput(path) {
				return Proposal{}, errors.New("input documents cannot be proposed")
			}
		}
	}
	var random [16]byte
	if _, e := rand.Read(random[:]); e != nil {
		return Proposal{}, errors.New("cannot create proposal identity")
	}
	meta := proposalMetadata{ID: hex.EncodeToString(random[:]), Base: base, Paths: sortedPaths(changes), Attribution: attribution, Created: time.Now().UTC()}
	data, e := json.Marshal(meta)
	if e != nil {
		return Proposal{}, errors.New("cannot encode proposal metadata")
	}
	tree, e := s.buildTree(base, changes)
	if e != nil {
		return Proposal{}, e
	}
	commit, e := s.commit(tree, []string{base}, "Propose documents\n\nSharedSpace-Metadata: "+base64.RawStdEncoding.EncodeToString(data)+"\n")
	if e != nil {
		return Proposal{}, e
	}
	// Publish only after the entire proposal and its provenance are durable Git objects.
	if e = s.updateRef("refs/heads/proposals/"+meta.ID, commit, ""); e != nil {
		return Proposal{}, e
	}
	ps, e := s.reconcile()
	if e != nil {
		// The proposal is already saved. Return its identity with the error so
		// the producer does not report that no proposal exists.
		attribution.Suite = attribution.Suite.Clone()
		attribution.Sources = maps.Clone(attribution.Sources)
		attribution.ReadInputs = slices.Clone(attribution.ReadInputs)
		return Proposal{ID: meta.ID, Base: base, Commit: commit, Status: "unknown", Changes: maps.Clone(changes), Attribution: attribution, Created: meta.Created}, committed(e)
	}
	return ps[meta.ID], nil
}
func (s *GitStore) Accept(id, expectedHead, actor string) (Proposal, error) {
	return s.accept(id, expectedHead, actor, ReviewActor{})
}

func (s *GitStore) AcceptReviewed(id, expectedHead string, reviewer ReviewActor) (Proposal, error) {
	if err := reviewer.validate(); err != nil {
		return Proposal{}, err
	}
	return s.accept(id, expectedHead, reviewer.Role, reviewer)
}

func (s *GitStore) accept(id, expectedHead, actor string, reviewer ReviewActor) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return Proposal{}, e
	}
	if e := validActor(actor); e != nil {
		return Proposal{}, e
	}
	ps, e := s.reconcile()
	if e != nil {
		return Proposal{}, e
	}
	p, ok := ps[id]
	if !ok {
		return Proposal{}, ErrProposalNotFound
	}
	if terminal(p.Status) {
		return Proposal{}, ErrDecided
	}
	h, e := s.head()
	if e != nil {
		return Proposal{}, e
	}
	if expectedHead != h {
		return Proposal{}, ErrStale
	}
	entries, e := s.history(h)
	if e != nil {
		return Proposal{}, e
	}
	current, e := s.stillCurrent(entries, 0, p)
	if e != nil {
		return Proposal{}, e
	}
	if !current {
		return Proposal{}, ErrStale
	}
	if e = s.validateTree(p.Commit); e != nil {
		return Proposal{}, e
	}
	var tree string
	if p.Base == h {
		tree, e = s.git("", nil, "rev-parse", p.Commit+"^{tree}")
		tree = strings.TrimSpace(tree)
	} else {
		// Only unrelated ingests landed since the proposal was made: apply its
		// documents on top of the current head so those ingests are kept.
		tree, e = s.buildTree(h, p.Changes)
		if e == nil {
			e = s.validateTree(tree)
		}
	}
	if e != nil {
		return Proposal{}, e
	}
	message := "Accept proposal\n\nSharedSpace-Proposal: " + p.ID + "\nSharedSpace-Actor: " + actor + "\n"
	if reviewer != (ReviewActor{}) {
		raw, _ := json.Marshal(reviewer)
		message += "SharedSpace-Reviewer: " + string(raw) + "\n"
	}
	merge, e := s.commit(tree, []string{h, p.Commit}, message)
	if e != nil {
		return Proposal{}, e
	}
	s.ledger.Intent = &reviewIntent{ID: p.ID, Kind: "accept", Commit: merge, Actor: actor, Reviewer: reviewer}
	if e = s.saveLedger(); e != nil {
		return Proposal{}, e
	}
	// The explicit old value prevents a stale check-then-write, even if another
	// writer bypasses this store's root lock. The ref is the acceptance commit point.
	if e = s.updateRef(acceptedRef, merge, h); e != nil {
		actual, headErr := s.head()
		if headErr == nil && actual != h {
			return Proposal{}, ErrStale
		}
		return Proposal{}, e
	}
	// Acceptance is now final; later failures still report it as accepted.
	p.Status, p.AcceptedCommit = "accepted", merge
	ps, e = s.reconcile()
	if e != nil {
		return p, committed(e)
	}
	p = ps[id]
	if e = s.project(merge); e != nil {
		return p, committed(e)
	}
	return p, nil
}
func (s *GitStore) Reject(id, actor string) (Proposal, error) {
	return s.reject(id, actor, ReviewActor{})
}

func (s *GitStore) RejectReviewed(id string, reviewer ReviewActor) (Proposal, error) {
	if err := reviewer.validate(); err != nil {
		return Proposal{}, err
	}
	return s.reject(id, reviewer.Role, reviewer)
}

func (s *GitStore) reject(id, actor string, reviewer ReviewActor) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return Proposal{}, e
	}
	if e := validActor(actor); e != nil {
		return Proposal{}, e
	}
	ps, e := s.reconcile()
	if e != nil {
		return Proposal{}, e
	}
	p, ok := ps[id]
	if !ok {
		return Proposal{}, ErrProposalNotFound
	}
	if terminal(p.Status) {
		return Proposal{}, ErrDecided
	}
	// The durable reject intent is itself a decision; reconciliation completes it.
	s.ledger.Intent = &reviewIntent{ID: id, Kind: "reject", Actor: actor, Reviewer: reviewer}
	if e = s.saveLedger(); e != nil {
		return Proposal{}, e
	}
	p.Status = "rejected"
	ps, e = s.reconcile()
	if e != nil {
		return p, committed(e)
	}
	return ps[id], nil
}
func (s *GitStore) Proposals() ([]Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return nil, e
	}
	ps, e := s.reconcile()
	if e != nil {
		return nil, e
	}
	out := make([]Proposal, 0, len(ps))
	for _, p := range ps {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out, nil
}
func (s *GitStore) History() ([]Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return nil, e
	}
	h, e := s.head()
	if e != nil {
		return nil, e
	}
	entries, e := s.history(h)
	if e != nil {
		return nil, e
	}
	out := make([]Checkpoint, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Checkpoint)
	}
	return out, nil
}
func (s *GitStore) Ingest(path, content, actor string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ready(); e != nil {
		return "", e
	}
	if e := validActor(actor); e != nil {
		return "", e
	}
	if e := s.validatePath(path); e != nil {
		return "", e
	}
	if s.policy != nil && !s.policy.IsInput(path) {
		return "", errors.New("ingest only accepts declared input documents")
	}
	if _, e := s.reconcile(); e != nil {
		return "", e
	}
	h, e := s.head()
	if e != nil {
		return "", e
	}
	files, e := s.treePaths(h)
	if e != nil {
		return "", e
	}
	for _, file := range files {
		if file == path {
			existing, e := s.read(h, path)
			if e != nil {
				return "", e
			}
			if existing == content {
				return h, s.project(h)
			}
			break
		}
	}
	tree, e := s.buildTree(h, map[string]string{path: content})
	if e != nil {
		return "", e
	}
	commit, e := s.commit(tree, []string{h}, ingestSummary+"\n\nSharedSpace-Actor: "+actor+"\n")
	if e != nil {
		return "", e
	}
	if e = s.validateTree(commit); e != nil {
		return "", e
	}
	if e = s.updateRef(acceptedRef, commit, h); e != nil {
		actual, headErr := s.head()
		if headErr == nil && actual != h {
			return "", ErrStale
		}
		return "", e
	}
	if _, e = s.reconcile(); e != nil {
		return commit, committed(e)
	}
	if e = s.project(commit); e != nil {
		return commit, committed(e)
	}
	return commit, nil
}

func terminal(status string) bool { return status == "accepted" || status == "rejected" }
func (s *GitStore) saveLedger() error {
	s.ledgerDirty = true
	data, e := json.Marshal(s.ledger)
	if e != nil {
		return errors.New("cannot encode review ledger")
	}
	if e = atomicWrite(s.root, filepath.Join(s.state, "reviews.json"), data); e != nil {
		return e
	}
	s.ledgerDirty = false
	return nil
}

// Rebuild proposals from Git; only terminal reviews live in the ledger. A
// reachable accepted merge always overrides a stale or contradictory ledger.
func (s *GitStore) reconcile() (map[string]Proposal, error) {
	// Accepted ledger entries are merely a cache. Only reachable checkpoints
	// may establish acceptance, including when metadata was edited or reverted.
	next := reviewLedger{Decisions: maps.Clone(s.ledger.Decisions), Intent: s.ledger.Intent}
	for id, d := range next.Decisions {
		if d.Status == "accepted" {
			delete(next.Decisions, id)
		}
	}
	ps := make(map[string]Proposal)
	used := make(map[string]bool)
	refs, e := s.git("", nil, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/proposals/")
	if e != nil {
		return nil, e
	}
	for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, errors.New("invalid proposal reference")
		}
		id := strings.TrimPrefix(parts[0], "refs/heads/proposals/")
		p, e := s.loadProposal(parts[1])
		if e != nil {
			return nil, e
		}
		used[parts[1]] = true
		if p.ID != id {
			return nil, errors.New("proposal reference identity mismatch")
		}
		ps[id] = p
	}
	h, e := s.head()
	if e != nil {
		return nil, e
	}
	entries, e := s.history(h)
	if e != nil {
		return nil, e
	}
	for i, entry := range entries {
		if entry.Proposal == "" {
			continue
		}
		if len(entry.parents) != 2 {
			return nil, errors.New("accepted proposal checkpoint must have two parents")
		}
		p, e := s.loadProposal(entry.parents[1])
		if e != nil {
			return nil, e
		}
		used[entry.parents[1]] = true
		if p.ID != entry.Proposal {
			return nil, errors.New("accepted checkpoint provenance mismatch")
		}
		// The first parent is the head at acceptance: the proposal's base, or a
		// later head reached from it through unrelated ingests only.
		if current, e := s.stillCurrent(entries, i+1, p); e != nil {
			return nil, e
		} else if !current {
			return nil, errors.New("accepted checkpoint provenance mismatch")
		}
		ps[p.ID] = p
		next.Decisions[p.ID] = decision{Status: "accepted", Actor: entry.Actor, AcceptedCommit: entry.Commit, Reviewer: entry.Reviewer}
	}
	if intent := next.Intent; intent != nil {
		if intent.Kind == "reject" {
			if d := next.Decisions[intent.ID]; d.Status != "accepted" {
				if _, ok := ps[intent.ID]; !ok {
					return nil, errors.New("review intent proposal not found")
				}
				next.Decisions[intent.ID] = decision{Status: "rejected", Actor: intent.Actor, Reviewer: intent.Reviewer}
			}
		}
		// An accept intent without a reachable merge did not commit; do not accept it.
		next.Intent = nil
	}
	for commit := range s.proposalCache {
		if !used[commit] {
			delete(s.proposalCache, commit)
		}
	}
	for id, p := range ps {
		if d, ok := next.Decisions[id]; ok {
			if !terminal(d.Status) {
				return nil, errors.New("invalid terminal review decision")
			}
			p.Status = d.Status
			p.AcceptedCommit = d.AcceptedCommit
			p.Reviewer = d.Reviewer
		} else if current, e := s.stillCurrent(entries, 0, p); e != nil {
			return nil, e
		} else if !current {
			p.Status = "stale"
		} else {
			p.Status = "pending"
		}
		ps[id] = p
	}
	if !maps.Equal(s.ledger.Decisions, next.Decisions) || s.ledger.Intent != next.Intent {
		s.ledger = next
		s.ledgerDirty = true
	}
	if s.ledgerDirty {
		if e = s.saveLedger(); e != nil {
			return nil, e
		}
	}
	return ps, nil
}

// loadProposal returns the proposal stored in commit, without review status.
// The result is a copy the caller may change.
func (s *GitStore) loadProposal(commit string) (Proposal, error) {
	if p, ok := s.proposalCache[commit]; ok {
		return cloneProposal(p), nil
	}
	if e := s.validateCommit(commit); e != nil {
		return Proposal{}, e
	}
	raw, e := s.git("", nil, "show", "-s", "--format=%B", commit)
	if e != nil {
		return Proposal{}, e
	}
	encoded := trailer(raw, "SharedSpace-Metadata")
	data, e := base64.RawStdEncoding.DecodeString(encoded)
	if e != nil || encoded == "" {
		return Proposal{}, errors.New("invalid proposal metadata")
	}
	var meta proposalMetadata
	if json.Unmarshal(data, &meta) != nil || len(meta.ID) != 32 || !validHex(meta.ID) || !validOID(meta.Base) || meta.Created.IsZero() || len(meta.Paths) == 0 {
		return Proposal{}, errors.New("invalid proposal metadata")
	}
	parents, e := s.git("", nil, "rev-list", "--parents", "-n", "1", commit)
	if e != nil {
		return Proposal{}, e
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[1] != meta.Base {
		return Proposal{}, errors.New("proposal base mismatch")
	}
	p := Proposal{ID: meta.ID, Base: meta.Base, Commit: commit, Attribution: meta.Attribution, Created: meta.Created, Changes: make(map[string]string)}
	for _, path := range meta.Paths {
		if e := s.validatePath(path); e != nil {
			return Proposal{}, e
		}
		content, e := s.read(commit, path)
		if e != nil {
			return Proposal{}, e
		}
		p.Changes[path] = content
	}
	s.proposalCache[commit] = p
	return cloneProposal(p), nil
}
func cloneProposal(p Proposal) Proposal {
	p.Attribution.Suite = p.Attribution.Suite.Clone()
	p.Changes = maps.Clone(p.Changes)
	p.Attribution.Sources = maps.Clone(p.Attribution.Sources)
	p.Attribution.ReadInputs = slices.Clone(p.Attribution.ReadInputs)
	return p
}

type historyEntry struct {
	Checkpoint
	parents []string
}

// history lists the first-parent chain from the accepted head h, newest first.
// The result is shared with the cache and must not be changed.
func (s *GitStore) history(h string) ([]historyEntry, error) {
	if h == s.historyHead && s.historyCache != nil {
		return s.historyCache, nil
	}
	out, e := s.git("", nil, "log", "-z", "--first-parent", "--format=%H %P%n%B", h)
	if e != nil {
		return nil, e
	}
	entries := make([]historyEntry, 0)
	for _, record := range strings.Split(out, "\x00") {
		header, message, _ := strings.Cut(record, "\n")
		fields := strings.Fields(header)
		if len(fields) == 0 {
			continue
		}
		if !validOID(fields[0]) {
			return nil, errors.New("invalid accepted history")
		}
		summary := strings.SplitN(message, "\n", 2)[0]
		var reviewer ReviewActor
		if raw := trailer(message, "SharedSpace-Reviewer"); raw != "" {
			if json.Unmarshal([]byte(raw), &reviewer) != nil || reviewer.validate() != nil || reviewer.Role != trailer(message, "SharedSpace-Actor") {
				return nil, errors.New("invalid recorded reviewer")
			}
		}
		entries = append(entries, historyEntry{Checkpoint: Checkpoint{Commit: fields[0], Summary: summary, Proposal: trailer(message, "SharedSpace-Proposal"), Actor: trailer(message, "SharedSpace-Actor"), Reviewer: reviewer}, parents: fields[1:]})
	}
	s.historyHead, s.historyCache = h, entries
	return entries, nil
}

// stillCurrent reports whether proposal p can be applied at entries[from]: its
// base is that commit, or every checkpoint between them is an ingest that
// changed none of the proposed documents or recorded read-input scopes. Any
// acceptance in between, or ingest affecting that premise, makes it stale.
func (s *GitStore) stillCurrent(entries []historyEntry, from int, p Proposal) (bool, error) {
	for i := from; i < len(entries); i++ {
		entry := entries[i]
		if entry.Commit == p.Base {
			return true, nil
		}
		if entry.Summary != ingestSummary || entry.Proposal != "" || len(entry.parents) != 1 {
			return false, nil
		}
		paths, e := s.changedPaths(entry.Commit)
		if e != nil {
			return false, e
		}
		for _, filename := range paths {
			if _, ok := p.Changes[filename]; ok {
				return false, nil
			}
			if s.policy != nil && (from == 0 || len(p.Attribution.ReadInputs) > 0) {
				// Pending older generic proposals still guard their exact recorded reads.
				// Historical acceptances without new scopes retain their original rules.
				if _, read := p.Attribution.Sources[filename]; read && s.policy.IsInput(filename) {
					return false, nil
				}
				for _, pattern := range p.Attribution.ReadInputs {
					if matched, _ := path.Match(pattern, filename); matched {
						return false, nil
					}
				}
			}
		}
	}
	return false, nil
}

// changedPaths lists the documents a single-parent commit changed. Commits
// never change, so the answer is kept.
func (s *GitStore) changedPaths(commit string) ([]string, error) {
	if paths, ok := s.ingestPaths[commit]; ok {
		return paths, nil
	}
	out, e := s.git("", nil, "diff-tree", "--no-commit-id", "--no-renames", "--name-only", "-r", "-z", commit)
	if e != nil {
		return nil, e
	}
	paths := make([]string, 0, 1)
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	s.ingestPaths[commit] = paths
	return paths, nil
}
func trailer(message, key string) string {
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, key+": ") {
			return strings.TrimPrefix(line, key+": ")
		}
	}
	return ""
}
func sortedPaths(changes map[string]string) []string {
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
func validOID(value string) bool { return len(value) == 40 && validHex(value) }
func validHex(value string) bool {
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return value != ""
}
func validActor(actor string) error {
	if strings.TrimSpace(actor) == "" || len(actor) > 128 {
		return errors.New("invalid review actor")
	}
	for _, c := range actor {
		if c < 32 || c == 127 {
			return errors.New("invalid review actor")
		}
	}
	return nil
}
func (s *GitStore) validateCommit(revision string) error {
	if !validOID(revision) {
		return errors.New("revision must be a full commit ID")
	}
	kind, e := s.git("", nil, "cat-file", "-t", revision)
	if e != nil {
		return e
	}
	if strings.TrimSpace(kind) != "commit" {
		return errors.New("revision is not a commit")
	}
	return nil
}
func (s *GitStore) validateChanges(changes map[string]string, nonempty bool) error {
	if nonempty && len(changes) == 0 {
		return errors.New("proposal has no documents")
	}
	for path := range changes {
		if e := s.validatePath(path); e != nil {
			return e
		}
	}
	return nil
}
