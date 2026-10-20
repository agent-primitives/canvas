package sharedspace

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func openTestStore(t *testing.T) *GitStore {
	t.Helper()
	s, err := Open(t.TempDir(), map[string]string{"board.md": "original board\n", "strategy.md": "original strategy\n", "stats/ada.json": "{}\n"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}
func head(t *testing.T, s *GitStore) string {
	t.Helper()
	h, e := s.Head()
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func proposal(t *testing.T, s *GitStore, base, path, content string) Proposal {
	t.Helper()
	p, e := s.Propose(base, map[string]string{path: content}, Attribution{Agent: "scouting", BundleDigest: "digest", Session: "session-1", Run: "run-1", Sources: map[string]string{"board.md": base}})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func assertRead(t *testing.T, s *GitStore, rev, path, want string) {
	t.Helper()
	got, e := s.Read(rev, path)
	if e != nil {
		t.Fatal(e)
	}
	if got != want {
		t.Fatal("unexpected document content")
	}
}
func scratchGit(t *testing.T, root, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + filepath.Join(root, ".sharedspace", "git")}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=SharedSpace", "GIT_AUTHOR_EMAIL=sharedspace@localhost", "GIT_COMMITTER_NAME=SharedSpace", "GIT_COMMITTER_EMAIL=sharedspace@localhost")
	cmd.Stdin = strings.NewReader(input)
	out, e := cmd.Output()
	if e != nil {
		t.Fatalf("scratch git command failed: %v", e)
	}
	return strings.TrimSpace(string(out))
}

func TestProposalDoesNotChangeAccepted(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	p := proposal(t, s, base, "board.md", "new board\n")
	if p.ID == "" || p.Base != base || p.Commit == base || p.Status != "pending" || p.Created.IsZero() {
		t.Fatal("proposal missing identity or pending state")
	}
	if head(t, s) != base {
		t.Fatal("proposal advanced accepted ref")
	}
	assertRead(t, s, "", "board.md", "original board\n")
	assertRead(t, s, p.Commit, "board.md", "new board\n")
	raw, e := os.ReadFile(filepath.Join(s.Root(), "board.md"))
	if e != nil || string(raw) != "original board\n" {
		t.Fatal("proposal changed workspace")
	}
	if scratchGit(t, s.Root(), "", "rev-parse", "refs/heads/proposals/"+p.ID) != p.Commit {
		t.Fatal("proposal branch missing")
	}
}

func TestAcceptMergeHistoryAndProvenance(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	p := proposal(t, s, base, "board.md", "accepted board\n")
	accepted, e := s.Accept(p.ID, base, "GM")
	if e != nil {
		t.Fatal(e)
	}
	h := head(t, s)
	if accepted.Status != "accepted" || accepted.AcceptedCommit != h {
		t.Fatal("missing accepted checkpoint")
	}
	parents := strings.Fields(scratchGit(t, s.Root(), "", "rev-list", "--parents", "-n", "1", h))
	if len(parents) != 3 || parents[1] != base || parents[2] != p.Commit {
		t.Fatal("checkpoint is not a two-parent merge")
	}
	assertRead(t, s, "", "board.md", "accepted board\n")
	raw, e := os.ReadFile(filepath.Join(s.Root(), "board.md"))
	if e != nil || string(raw) != "accepted board\n" {
		t.Fatal("workspace not projected")
	}
	history, e := s.History()
	if e != nil {
		t.Fatal(e)
	}
	if len(history) != 2 || history[0].Proposal != p.ID || history[0].Actor != "GM" || history[0].Commit != h {
		t.Fatal("history missing review provenance")
	}
	entries, e := s.Proposals()
	if e != nil || len(entries) != 1 || entries[0].Attribution.Session != "session-1" || entries[0].Attribution.Sources["board.md"] != base {
		t.Fatal("lost proposal provenance")
	}
	if _, e = s.Accept(p.ID, h, "owner"); !errors.Is(e, ErrDecided) {
		t.Fatalf("second accept: %v", e)
	}
	if _, e = s.Reject(p.ID, "owner"); !errors.Is(e, ErrDecided) {
		t.Fatalf("accept then reject: %v", e)
	}
}

func TestRejectLeavesHeadAndIsTerminal(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	p := proposal(t, s, base, "strategy.md", "new strategy")
	got, e := s.Reject(p.ID, "coach")
	if e != nil || got.Status != "rejected" {
		t.Fatalf("Reject: %v", e)
	}
	if head(t, s) != base {
		t.Fatal("reject advanced accepted")
	}
	if _, e = s.Accept(p.ID, base, "GM"); !errors.Is(e, ErrDecided) {
		t.Fatalf("reject then accept: %v", e)
	}
	if _, e = s.Reject(p.ID, "GM"); !errors.Is(e, ErrDecided) {
		t.Fatalf("second reject: %v", e)
	}
	root := s.Root()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if _, e = reopened.Accept(p.ID, base, "GM"); !errors.Is(e, ErrDecided) {
		t.Fatalf("reopened reject: %v", e)
	}
}

func TestStaleDerivedAndRejectAllowed(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	a := proposal(t, s, base, "board.md", "A")
	b := proposal(t, s, base, "strategy.md", "B")
	if _, e := s.Accept(a.ID, base, "GM"); e != nil {
		t.Fatal(e)
	}
	h := head(t, s)
	if _, e := s.Accept(b.ID, base, "coach"); !errors.Is(e, ErrStale) {
		t.Fatalf("old expected head: %v", e)
	}
	if _, e := s.Accept(b.ID, h, "coach"); !errors.Is(e, ErrStale) {
		t.Fatalf("stale base: %v", e)
	}
	ps, e := s.Proposals()
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range ps {
		if p.ID == b.ID {
			found = p.Status == "stale"
		}
	}
	if !found {
		t.Fatal("stale status not derived")
	}
	if _, e = s.Reject(b.ID, "coach"); e != nil {
		t.Fatal(e)
	}
	if head(t, s) != h {
		t.Fatal("stale rejection advanced ref")
	}
}

func TestReviewRaces(t *testing.T) {
	t.Run("competing acceptance", func(t *testing.T) {
		s := openTestStore(t)
		base := head(t, s)
		a := proposal(t, s, base, "board.md", "A")
		b := proposal(t, s, base, "board.md", "B")
		errs := make(chan error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, id := range []string{a.ID, b.ID} {
			wg.Add(1)
			go func(id string) { defer wg.Done(); <-start; _, e := s.Accept(id, base, "GM"); errs <- e }(id)
		}
		close(start)
		wg.Wait()
		close(errs)
		wins, stale := 0, 0
		for e := range errs {
			if e == nil {
				wins++
			} else if errors.Is(e, ErrStale) {
				stale++
			} else {
				t.Fatal(e)
			}
		}
		if wins != 1 || stale != 1 {
			t.Fatal("accept CAS did not resolve once")
		}
	})
	t.Run("accept versus reject", func(t *testing.T) {
		s := openTestStore(t)
		base := head(t, s)
		p := proposal(t, s, base, "board.md", "A")
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() { <-start; _, e := s.Accept(p.ID, base, "GM"); errs <- e }()
		go func() { <-start; _, e := s.Reject(p.ID, "coach"); errs <- e }()
		close(start)
		wins, decided := 0, 0
		for range 2 {
			e := <-errs
			if e == nil {
				wins++
			} else if errors.Is(e, ErrDecided) {
				decided++
			} else {
				t.Fatal(e)
			}
		}
		if wins != 1 || decided != 1 {
			t.Fatal("review decision did not resolve once")
		}
	})
}

// Crash simulation belongs only in tests: publish the checkpoint as Git would,
// without finalizing the review ledger or updating the visible workspace.
func simulateAcceptedCheckpoint(t *testing.T, s *GitStore, p Proposal) string {
	t.Helper()
	tree := scratchGit(t, s.Root(), "", "rev-parse", p.Commit+"^{tree}")
	merge := scratchGit(t, s.Root(), "Accept proposal\n\nSharedSpace-Proposal: "+p.ID+"\nSharedSpace-Actor: GM\n", "commit-tree", tree, "-p", p.Base, "-p", p.Commit)
	scratchGit(t, s.Root(), "", "update-ref", "refs/heads/main", merge, p.Base)
	return merge
}
func overwriteLedger(t *testing.T, root string, value any) {
	t.Helper()
	b, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, ".sharedspace", "reviews.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestReopenReconcilesAuthoritativeAcceptedRef(t *testing.T) {
	for _, mode := range []string{"missing metadata", "contradictory decision", "crashed accept"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			p := proposal(t, s, head(t, s), "board.md", "crash accepted\n")
			merge := simulateAcceptedCheckpoint(t, s, p)
			root := s.Root()
			if e := s.Close(); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "missing metadata":
				if e := os.Remove(filepath.Join(root, ".sharedspace", "reviews.json")); e != nil {
					t.Fatal(e)
				}
			case "contradictory decision":
				overwriteLedger(t, root, map[string]any{"decisions": map[string]any{p.ID: map[string]string{"status": "rejected", "actor": "coach"}}})
			case "crashed accept":
				overwriteLedger(t, root, map[string]any{"intent": map[string]string{"id": p.ID, "kind": "accept", "commit": merge, "actor": "GM"}})
			}
			reopened, e := Open(root, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			entries, e := reopened.Proposals()
			if e != nil || len(entries) != 1 || entries[0].Status != "accepted" || entries[0].AcceptedCommit != merge || entries[0].Attribution.Session != "session-1" {
				t.Fatal("accepted checkpoint did not repair metadata")
			}
			assertRead(t, reopened, "", "board.md", "crash accepted\n")
			raw, e := os.ReadFile(filepath.Join(root, "board.md"))
			if e != nil || string(raw) != "crash accepted\n" {
				t.Fatal("reopen did not repair projection")
			}
			if _, e = reopened.Reject(p.ID, "coach"); !errors.Is(e, ErrDecided) {
				t.Fatalf("reopened accepted review: %v", e)
			}
		})
	}
}

func TestUnpublishedAcceptIntentDoesNotAccept(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "not accepted")
	root := s.Root()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	overwriteLedger(t, root, map[string]any{"intent": map[string]string{"id": p.ID, "kind": "accept", "commit": p.Commit, "actor": "GM"}})
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	ps, e := reopened.Proposals()
	if e != nil || len(ps) != 1 || ps[0].Status != "pending" {
		t.Fatal("unpublished intent became acceptance")
	}
	assertRead(t, reopened, "", "board.md", "original board\n")
}

func TestSafePathsAndSymlinks(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	for _, path := range []string{"", "../board.md", "/board.md", "prospects/../board.md", "prospects/a/b.md", "prospects/.hidden.md", "stats/a.md", "other.md", "board.md\x00x", "stats/a\\b.json", "injuries/x.json\n"} {
		if _, e := s.Propose(base, map[string]string{path: "private content"}, Attribution{}); e == nil {
			t.Fatalf("Propose accepted unsafe path %q", path)
		}
		if _, e := s.Read("", path); e == nil {
			t.Fatalf("Read accepted unsafe path %q", path)
		}
		if _, e := s.Ingest(path, "private content", "GM"); e == nil {
			t.Fatalf("Ingest accepted unsafe path %q", path)
		}
	}
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(s.Root(), "prospects")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Propose(base, map[string]string{"prospects/a.md": "sensitive fixture"}, Attribution{}); e == nil {
		t.Fatal("proposal through symlink allowed")
	}
	if _, e := s.Ingest("prospects/a.md", "sensitive fixture", "GM"); e == nil {
		t.Fatal("ingest through symlink allowed")
	}
	if _, e := s.Read("", "prospects/a.md"); e == nil {
		t.Fatal("read through symlink allowed")
	}
	if e := os.Symlink(filepath.Join(outside, "target"), filepath.Join(s.Root(), "injuries")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Ingest("injuries/a.json", "{}", "GM"); e == nil {
		t.Fatal("dangling symlink allowed")
	}
	for _, revision := range []string{"--all", "HEAD", "main", "../x", base + ":board.md"} {
		if _, e := s.Read(revision, "board.md"); e == nil {
			t.Fatalf("unsafe revision %q allowed", revision)
		}
	}
}

func TestIngestCheckpointIdempotentAndPreservesUntracked(t *testing.T) {
	s := openTestStore(t)
	root := s.Root()
	if e := os.MkdirAll(filepath.Join(root, "injuries"), 0700); e != nil {
		t.Fatal(e)
	}
	for path, content := range map[string]string{"injuries/untouched.json": "untracked report", "notes.txt": "private note"} {
		if e := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	base := head(t, s)
	h, e := s.Ingest("injuries/report.json", "{\"reportId\":\"r1\"}\n", "watcher")
	if e != nil || h == base {
		t.Fatalf("Ingest: %v", e)
	}
	same, e := s.Ingest("injuries/report.json", "{\"reportId\":\"r1\"}\n", "watcher")
	if e != nil || same != h {
		t.Fatal("unchanged ingest was not idempotent")
	}
	assertRead(t, s, "", "injuries/report.json", "{\"reportId\":\"r1\"}\n")
	p := proposal(t, s, h, "board.md", "later board")
	if _, e = s.Accept(p.ID, h, "GM"); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	for path, want := range map[string]string{"injuries/untouched.json": "untracked report", "notes.txt": "private note"} {
		got, e := os.ReadFile(filepath.Join(root, path))
		if e != nil || string(got) != want {
			t.Fatal("operation destroyed unrelated workspace file")
		}
	}
	history, e := reopened.History()
	if e != nil || len(history) != 3 || history[1].Actor != "watcher" || history[1].Proposal != "" {
		t.Fatal("missing data checkpoint provenance")
	}
}

func TestReopenPreservesTrackedInjuryRevision(t *testing.T) {
	s := openTestStore(t)
	const path = "injuries/report.json"
	const original = "{\"reportId\":\"r1\",\"injury\":\"original\"}\n"
	const revised = "{\"reportId\":\"r1\",\"injury\":\"revised\"}\n"
	checkpoint, e := s.Ingest(path, original, "watcher")
	if e != nil {
		t.Fatal(e)
	}
	root := s.Root()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, path), []byte(revised), 0600); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	got, e := os.ReadFile(filepath.Join(root, path))
	if e != nil || string(got) != revised {
		t.Fatalf("reopen overwrote incoming injury revision: got %q, error %v", got, e)
	}
	if head(t, reopened) != checkpoint {
		t.Fatal("reopen advanced accepted head")
	}
	assertRead(t, reopened, "", path, original)
}

func TestIngestPreservesOtherTrackedInjuryRevision(t *testing.T) {
	for _, mode := range []string{"new checkpoint", "idempotent checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			const original = "{\"reportId\":\"b\",\"injury\":\"original\"}\n"
			const revised = "{\"reportId\":\"b\",\"injury\":\"revised\"}\n"
			const reportA = "{\"reportId\":\"a\"}\n"
			if _, e := s.Ingest("injuries/b.json", original, "watcher"); e != nil {
				t.Fatal(e)
			}
			if mode == "idempotent checkpoint" {
				if _, e := s.Ingest("injuries/a.json", reportA, "watcher"); e != nil {
					t.Fatal(e)
				}
			}
			before := head(t, s)
			target := filepath.Join(s.Root(), "injuries/b.json")
			if e := os.WriteFile(target, []byte(revised), 0600); e != nil {
				t.Fatal(e)
			}
			checkpoint, e := s.Ingest("injuries/a.json", reportA, "watcher")
			if e != nil {
				t.Fatal(e)
			}
			got, e := os.ReadFile(target)
			if e != nil || string(got) != revised {
				t.Fatalf("ingesting A overwrote incoming B revision: got %q, error %v", got, e)
			}
			if (checkpoint == before) != (mode == "idempotent checkpoint") {
				t.Fatal("unexpected ingest checkpoint identity")
			}
			assertRead(t, s, "", "injuries/a.json", reportA)
			assertRead(t, s, "", "injuries/b.json", original)
		})
	}
}

func TestIngestPreservesInjuryNewerThanCheckpointInput(t *testing.T) {
	s := openTestStore(t)
	const path = "injuries/report.json"
	const snapshot = "{\"reportId\":\"r1\",\"injury\":\"snapshot\"}\n"
	const revised = "{\"reportId\":\"r1\",\"injury\":\"revised\"}\n"
	if e := os.Mkdir(filepath.Join(s.Root(), "injuries"), 0700); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(s.Root(), path)
	if e := os.WriteFile(target, []byte(revised), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Ingest(path, snapshot, "watcher"); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(target)
	if e != nil || string(got) != revised {
		t.Fatalf("ingest overwrote injury newer than its input: got %q, error %v", got, e)
	}
	assertRead(t, s, "", path, snapshot)
}

func TestAcceptProjectsBoardWithoutOverwritingTrackedInjuryRevision(t *testing.T) {
	s := openTestStore(t)
	const path = "injuries/report.json"
	const original = "{\"reportId\":\"r1\",\"injury\":\"original\"}\n"
	const revised = "{\"reportId\":\"r1\",\"injury\":\"revised\"}\n"
	base, e := s.Ingest(path, original, "watcher")
	if e != nil {
		t.Fatal(e)
	}
	p := proposal(t, s, base, "board.md", "accepted board\n")
	if e = os.WriteFile(filepath.Join(s.Root(), path), []byte(revised), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Accept(p.ID, base, "GM"); e != nil {
		t.Fatal(e)
	}
	board, e := os.ReadFile(filepath.Join(s.Root(), "board.md"))
	if e != nil || string(board) != "accepted board\n" {
		t.Fatal("accepted board not projected")
	}
	got, e := os.ReadFile(filepath.Join(s.Root(), path))
	if e != nil || string(got) != revised {
		t.Fatalf("accept overwrote incoming injury revision: got %q, error %v", got, e)
	}
	assertRead(t, s, "", "board.md", "accepted board\n")
	assertRead(t, s, "", path, original)
}

func TestMissingCheckpointedInjuryRestoredWithoutNewCheckpoint(t *testing.T) {
	for _, mode := range []string{"reopen", "idempotent ingest"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			const path = "injuries/report.json"
			const content = "{\"reportId\":\"r1\"}\n"
			checkpoint, e := s.Ingest(path, content, "watcher")
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(filepath.Join(s.Root(), path)); e != nil {
				t.Fatal(e)
			}
			if mode == "reopen" {
				root := s.Root()
				if e = s.Close(); e != nil {
					t.Fatal(e)
				}
				s, e = Open(root, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer s.Close()
				got, e := os.ReadFile(filepath.Join(root, path))
				if e != nil || string(got) != content {
					t.Fatal("reopen did not restore missing injury report")
				}
			}
			same, e := s.Ingest(path, content, "watcher")
			if e != nil || same != checkpoint {
				t.Fatalf("restored report created a new checkpoint: %v", e)
			}
			got, e := os.ReadFile(filepath.Join(s.Root(), path))
			if e != nil || string(got) != content {
				t.Fatal("missing injury report not restored")
			}
			history, e := s.History()
			if e != nil || len(history) != 2 {
				t.Fatal("restoration added checkpoint history")
			}
		})
	}
}

func TestReopenRejectsUnsafeTrackedInjuryPaths(t *testing.T) {
	for _, kind := range []string{"file symlink", "dangling symlink", "directory", "parent symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			const path = "injuries/report.json"
			if _, e := s.Ingest(path, "{}\n", "watcher"); e != nil {
				t.Fatal(e)
			}
			root := s.Root()
			if e := s.Close(); e != nil {
				t.Fatal(e)
			}
			target := filepath.Join(root, path)
			if e := os.Remove(target); e != nil {
				t.Fatal(e)
			}
			outside := t.TempDir()
			external := filepath.Join(outside, "report.json")
			if kind != "dangling symlink" {
				if e := os.WriteFile(external, []byte("untouched"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "directory":
				if e := os.Mkdir(target, 0700); e != nil {
					t.Fatal(e)
				}
			case "parent symlink":
				target = filepath.Join(root, "injuries")
				if e := os.Remove(target); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(outside, target); e != nil {
					t.Fatal(e)
				}
			default:
				if e := os.Symlink(external, target); e != nil {
					t.Fatal(e)
				}
			}
			if reopened, e := Open(root, nil); e == nil {
				reopened.Close()
				t.Fatal("reopen accepted unsafe tracked injury path")
			}
			got, e := os.ReadFile(external)
			if kind == "dangling symlink" {
				if !errors.Is(e, os.ErrNotExist) {
					t.Fatal("reopen created symlink target")
				}
			} else if e != nil || string(got) != "untouched" {
				t.Fatal("reopen changed external injury file")
			}
		})
	}
}

func TestRootLockAndClose(t *testing.T) {
	s := openTestStore(t)
	root := s.Root()
	if _, e := Open(root, nil); e == nil {
		t.Fatal("second owner acquired workspace")
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Head(); e == nil {
		t.Fatal("closed store allowed operation")
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e := os.Symlink(root, filepath.Join(t.TempDir(), "workspace")); e != nil {
		t.Fatal(e)
	}
}

func TestPrivateStateAndContentNotInErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "space")
	s, e := Open(root, map[string]string{"board.md": "secret seed"})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, path := range []string{root, filepath.Join(root, ".sharedspace"), filepath.Join(root, ".sharedspace", "reviews.json")} {
		info, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("private state accessible to group or others")
		}
	}
	_, e = s.Propose("invalid", map[string]string{"board.md": "DO_NOT_LEAK_PRIVATE_TEXT"}, Attribution{})
	if e == nil || strings.Contains(e.Error(), "DO_NOT_LEAK_PRIVATE_TEXT") {
		t.Fatal("unsafe error or missing base validation")
	}
}

func TestAcceptAfterUnrelatedIngest(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	p := proposal(t, s, base, "prospects/a.md", "scouting for A\n")
	other := proposal(t, s, base, "board.md", "new board\n")
	// Two injury reports for another prospect land before the GM reviews.
	if _, e := s.Ingest("injuries/b.json", "{\"b\":1}\n", "coach"); e != nil {
		t.Fatal(e)
	}
	ingested, e := s.Ingest("injuries/c.json", "{\"c\":1}\n", "coach")
	if e != nil {
		t.Fatal(e)
	}
	if statusOf(t, s, p.ID) != "pending" || statusOf(t, s, other.ID) != "pending" {
		t.Fatal("unrelated ingest made proposals stale")
	}
	if _, e = s.Accept(p.ID, base, "GM"); !errors.Is(e, ErrStale) {
		t.Fatalf("old expected head accepted: %v", e)
	}
	accepted, e := s.Accept(p.ID, ingested, "GM")
	if e != nil {
		t.Fatalf("accept after unrelated ingest: %v", e)
	}
	h := head(t, s)
	if accepted.Status != "accepted" || accepted.AcceptedCommit != h {
		t.Fatal("acceptance not reported")
	}
	parents := strings.Fields(scratchGit(t, s.Root(), "", "rev-list", "--parents", "-n", "1", h))
	if len(parents) != 3 || parents[1] != ingested || parents[2] != p.Commit {
		t.Fatal("accepted checkpoint not built on the current head")
	}
	assertRead(t, s, "", "prospects/a.md", "scouting for A\n")
	assertRead(t, s, "", "injuries/b.json", "{\"b\":1}\n")
	assertRead(t, s, "", "injuries/c.json", "{\"c\":1}\n")
	assertRead(t, s, "", "board.md", "original board\n")
	// A real acceptance still makes the other proposal stale.
	if statusOf(t, s, other.ID) != "stale" {
		t.Fatal("proposal not stale after an acceptance")
	}
	if _, e = s.Accept(other.ID, h, "GM"); !errors.Is(e, ErrStale) {
		t.Fatalf("accept after an acceptance: %v", e)
	}
	// Reopening checks the accepted checkpoint's provenance from scratch.
	root := s.Root()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, ".sharedspace", "reviews.json")); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if statusOf(t, reopened, p.ID) != "accepted" || statusOf(t, reopened, other.ID) != "stale" {
		t.Fatal("reopen lost acceptance after ingest")
	}
}

func TestIngestOfProposedDocumentMakesProposalStale(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	p := proposal(t, s, base, "stats/ada.json", "{\"proposed\":true}\n")
	h, e := s.Ingest("stats/ada.json", "{\"ingested\":true}\n", "coach")
	if e != nil {
		t.Fatal(e)
	}
	if statusOf(t, s, p.ID) != "stale" {
		t.Fatal("ingest of a proposed document did not make it stale")
	}
	if _, e = s.Accept(p.ID, h, "GM"); !errors.Is(e, ErrStale) {
		t.Fatalf("accept over ingested document: %v", e)
	}
	assertRead(t, s, "", "stats/ada.json", "{\"ingested\":true}\n")
}

func TestAcceptAfterAcceptanceAndIngestIsStale(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	a := proposal(t, s, base, "board.md", "A")
	b := proposal(t, s, base, "strategy.md", "B")
	if _, e := s.Accept(a.ID, base, "GM"); e != nil {
		t.Fatal(e)
	}
	h, e := s.Ingest("injuries/r.json", "{}", "coach")
	if e != nil {
		t.Fatal(e)
	}
	if statusOf(t, s, b.ID) != "stale" {
		t.Fatal("proposal behind an acceptance not stale")
	}
	if _, e = s.Accept(b.ID, h, "GM"); !errors.Is(e, ErrStale) {
		t.Fatalf("accept behind an acceptance: %v", e)
	}
}

// failNthGit fails the nth run of the named Git subcommand from now on.
func failNthGit(t *testing.T, name string, n int) {
	t.Helper()
	count := 0
	testGitHook = func(args []string) error {
		if args[0] == name {
			count++
			if count == n {
				return errors.New("injected failure")
			}
		}
		return nil
	}
	t.Cleanup(func() { testGitHook = nil })
}

func TestDecisionSavedBeforeFollowUpFailureIsReported(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		s := openTestStore(t)
		base := head(t, s)
		p := proposal(t, s, base, "board.md", "accepted board\n")
		// Accept lists proposal refs once before and once after moving main.
		failNthGit(t, "for-each-ref", 2)
		got, e := s.Accept(p.ID, base, "GM")
		testGitHook = nil
		if !errors.Is(e, ErrCommitted) {
			t.Fatalf("follow-up failure not marked as committed: %v", e)
		}
		h := head(t, s)
		if h == base || got.ID != p.ID || got.Status != "accepted" || got.AcceptedCommit != h {
			t.Fatalf("committed acceptance not reported: %+v", got)
		}
		if statusOf(t, s, p.ID) != "accepted" {
			t.Fatal("acceptance lost")
		}
		if _, e = s.Accept(p.ID, h, "GM"); !errors.Is(e, ErrDecided) {
			t.Fatalf("retry: %v", e)
		}
	})
	t.Run("reject", func(t *testing.T) {
		s := openTestStore(t)
		base := head(t, s)
		p := proposal(t, s, base, "board.md", "rejected board\n")
		failNthGit(t, "for-each-ref", 2)
		got, e := s.Reject(p.ID, "coach")
		testGitHook = nil
		if !errors.Is(e, ErrCommitted) {
			t.Fatalf("follow-up failure not marked as committed: %v", e)
		}
		if got.ID != p.ID || got.Status != "rejected" {
			t.Fatalf("committed rejection not reported: %+v", got)
		}
		if statusOf(t, s, p.ID) != "rejected" || head(t, s) != base {
			t.Fatal("rejection lost or head moved")
		}
	})
	t.Run("decided before commit is plain failure", func(t *testing.T) {
		s := openTestStore(t)
		base := head(t, s)
		p := proposal(t, s, base, "board.md", "board\n")
		failNthGit(t, "update-ref", 1)
		got, e := s.Accept(p.ID, base, "GM")
		testGitHook = nil
		if e == nil || errors.Is(e, ErrCommitted) || got.ID != "" {
			t.Fatalf("failed accept reported as committed: %+v %v", got, e)
		}
		if statusOf(t, s, p.ID) != "pending" || head(t, s) != base {
			t.Fatal("failed accept changed state")
		}
	})
}

// countGit records the Git subcommands run until the returned stop is called.
func countGit(t *testing.T) (calls func() []string, stop func()) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	testGitHook = func(args []string) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, args[0])
		return nil
	}
	stop = func() { testGitHook = nil }
	t.Cleanup(stop)
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }, stop
}

func statusOf(t *testing.T, s *GitStore, id string) string {
	t.Helper()
	ps, e := s.Proposals()
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range ps {
		if p.ID == id {
			return p.Status
		}
	}
	return ""
}

func TestRepeatedPollDoesNotRereadCommits(t *testing.T) {
	s := openTestStore(t)
	base := head(t, s)
	var ids []string
	for i := range 5 {
		p := proposal(t, s, base, "prospects/p"+string(rune('a'+i))+".md", "dossier")
		ids = append(ids, p.ID)
	}
	first, e := s.Proposals()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.History(); e != nil {
		t.Fatal(e)
	}
	calls, stop := countGit(t)
	second, e := s.Proposals()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.History(); e != nil {
		t.Fatal(e)
	}
	stop()
	for _, call := range calls() {
		if call != "for-each-ref" && call != "rev-parse" {
			t.Fatalf("unchanged poll re-read Git objects: %v", calls())
		}
	}
	if len(calls()) > 3 {
		t.Fatalf("unchanged poll ran %d Git commands: %v", len(calls()), calls())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("cached poll changed proposals")
	}
	// Returned maps are copies, not the cache.
	second[0].Changes["board.md"] = "changed by caller"
	if again, _ := s.Proposals(); again[0].Changes["board.md"] != "" {
		t.Fatal("caller changed cached proposal")
	}

	// Every write shows up on the next poll.
	extra := proposal(t, s, base, "strategy.md", "new strategy")
	if statusOf(t, s, extra.ID) != "pending" {
		t.Fatal("new proposal missing after propose")
	}
	if _, e = s.Reject(ids[0], "coach"); e != nil {
		t.Fatal(e)
	}
	if statusOf(t, s, ids[0]) != "rejected" {
		t.Fatal("reject not visible")
	}
	accepted, e := s.Accept(ids[1], base, "GM")
	if e != nil {
		t.Fatal(e)
	}
	if statusOf(t, s, ids[1]) != "accepted" || statusOf(t, s, ids[2]) != "stale" {
		t.Fatal("accept not visible")
	}
	history, e := s.History()
	if e != nil || len(history) != 2 || history[0].Commit != accepted.AcceptedCommit {
		t.Fatal("history not refreshed after accept")
	}
	ingested, e := s.Ingest("injuries/r.json", "{}", "coach")
	if e != nil {
		t.Fatal(e)
	}
	history, e = s.History()
	if e != nil || len(history) != 3 || history[0].Commit != ingested || history[0].Actor != "coach" {
		t.Fatal("history not refreshed after ingest")
	}
}
