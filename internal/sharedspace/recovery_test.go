package sharedspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcceptedMetadataWithoutReachableCheckpointIsNotAcceptance(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "unaccepted")
	root := s.Root()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	overwriteLedger(t, root, map[string]any{"decisions": map[string]any{p.ID: map[string]string{"status": "accepted", "actor": "GM", "acceptedCommit": p.Commit}}})
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	ps, e := reopened.Proposals()
	if e != nil || len(ps) != 1 || ps[0].Status != "pending" || ps[0].AcceptedCommit != "" {
		t.Fatal("ledger forged an acceptance absent from accepted ref")
	}
	if _, e = reopened.Accept(p.ID, p.Base, "GM"); e != nil {
		t.Fatalf("unaccepted proposal not reviewable: %v", e)
	}
}

func TestReopenCompletesDurableRejectIntent(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "unaccepted")
	root := s.Root()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	overwriteLedger(t, root, map[string]any{"intent": map[string]string{"id": p.ID, "kind": "reject", "actor": "coach"}})
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	ps, e := reopened.Proposals()
	if e != nil || len(ps) != 1 || ps[0].Status != "rejected" {
		t.Fatal("durable rejection intent did not reconcile")
	}
	if _, e = reopened.Accept(p.ID, p.Base, "GM"); !errors.Is(e, ErrDecided) {
		t.Fatalf("rejected proposal accepted: %v", e)
	}
}

func TestReopenRecoversAcceptedProposalWithoutBranch(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "accepted")
	if _, e := s.Accept(p.ID, p.Base, "GM"); e != nil {
		t.Fatal(e)
	}
	scratchGit(t, s.Root(), "", "update-ref", "-d", "refs/heads/proposals/"+p.ID, p.Commit)
	root := s.Root()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(root, ".sharedspace", "reviews.json")); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, map[string]string{"board.md": "must not reseed"})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	ps, e := reopened.Proposals()
	if e != nil || len(ps) != 1 || ps[0].Status != "accepted" {
		t.Fatal("reachable accepted proposal not reconstructed")
	}
	assertRead(t, reopened, "", "board.md", "accepted")
}

func TestReopenMissingAcceptedRefDoesNotReseedOrChangeReviewEvidence(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "accepted fixture\n")
	accepted, err := s.Accept(p.ID, p.Base, "GM")
	if err != nil {
		t.Fatal(err)
	}
	root := s.Root()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	paths := []string{"board.md", "strategy.md", "stats/ada.json", ".sharedspace/reviews.json"}
	before := make(map[string]string)
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		before[path] = string(raw)
	}
	scratchGit(t, root, "", "update-ref", "-d", acceptedRef, accepted.AcceptedCommit)
	for _, seed := range []map[string]string{{"board.md": "must not overwrite accepted fixture"}, nil} {
		reopened, err := Open(root, seed)
		if reopened != nil {
			reopened.Close()
		}
		if err == nil {
			t.Error("existing repository with missing accepted ref reopened and reseeded")
		} else if err.Error() != "existing scratch repository is missing accepted ref" {
			t.Error("missing accepted ref did not return bounded operational error")
		}
		for _, path := range paths {
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil || string(raw) != before[path] {
				t.Errorf("failed reopen changed accepted document or decision metadata: %s", path)
			}
		}
		if got := scratchGit(t, root, "", "for-each-ref", "--format=%(objectname)", acceptedRef); got != "" {
			t.Error("failed reopen recreated accepted ref")
		}
	}
}

func TestMissingAcceptedRefIsNotConfusedWithNestedBranch(t *testing.T) {
	s := openTestStore(t)
	root, commit := s.Root(), head(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	scratchGit(t, root, "", "update-ref", "-d", acceptedRef, commit)
	scratchGit(t, root, "", "update-ref", acceptedRef+"/unrelated", commit)
	reopened, err := Open(root, nil)
	if reopened != nil {
		reopened.Close()
	}
	if err == nil || err.Error() != "existing scratch repository is missing accepted ref" {
		t.Fatal("nested branch hid the missing accepted ref diagnostic")
	}
}

func TestUnchangedReconciliationDoesNotRewriteReviewLedger(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "accepted fixture")
	if _, err := s.Accept(p.ID, p.Base, "GM"); err != nil {
		t.Fatal(err)
	}
	p = proposal(t, s, head(t, s), "strategy.md", "rejected fixture")
	if _, err := s.Reject(p.ID, "coach"); err != nil {
		t.Fatal(err)
	}
	root := s.Root()
	path := filepath.Join(root, ".sharedspace", "reviews.json")
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.Proposals(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Proposals(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatal("unchanged reconciliation rewrote the review ledger")
	}
}

func TestChangedReviewLedgerRemainsDirtyAfterWriteFailure(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "strategy.md", "pending fixture")
	path := filepath.Join(s.Root(), ".sharedspace", "reviews.json")
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	// A recovered review intent changes the reconstructed decisions. Failed
	// publication must remain retryable even after memory reflects that change.
	s.mu.Lock()
	s.ledger.Intent = &reviewIntent{ID: p.ID, Kind: "reject", Actor: "coach"}
	s.mu.Unlock()
	for range 2 {
		if _, err := s.Proposals(); err == nil {
			t.Fatal("dirty reconciliation skipped failed ledger persistence")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".backup", path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals(); err != nil {
		t.Fatal(err)
	}
	root := s.Root()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Accept(p.ID, p.Base, "GM"); !errors.Is(err, ErrDecided) {
		t.Fatal("repaired storage lost retried terminal decision")
	}
}

func TestActorCannotInjectCheckpointTrailers(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "candidate")
	for _, actor := range []string{"", "GM\nSharedSpace-Proposal: other", "GM\x00", strings.Repeat("x", 129)} {
		if _, e := s.Accept(p.ID, p.Base, actor); e == nil {
			t.Fatal("unsafe actor accepted")
		}
		if _, e := s.Reject(p.ID, actor); e == nil {
			t.Fatal("unsafe actor rejected")
		}
		if _, e := s.Ingest("injuries/r.json", "{}", actor); e == nil {
			t.Fatal("unsafe actor ingested")
		}
	}
	if head(t, s) != p.Base {
		t.Fatal("actor validation changed head")
	}
}

func TestEmptyRootRejectedWithoutFilesystemChanges(t *testing.T) {
	// Keep even a broken implementation confined to a disposable directory.
	t.Chdir(t.TempDir())
	if s, e := Open("", nil); e == nil {
		s.Close()
		t.Fatal("empty root opened working directory")
	}
	entries, e := os.ReadDir(".")
	if e != nil || len(entries) != 0 {
		t.Fatal("invalid root changed filesystem")
	}
}

func TestOpenRejectsSymlinkRootsAndPrivateState(t *testing.T) {
	for _, kind := range []string{"root", "state", "git", "ledger"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "root")
			if e := os.Mkdir(root, 0700); e != nil {
				t.Fatal(e)
			}
			external := t.TempDir()
			target := root
			switch kind {
			case "root":
				if e := os.Remove(root); e != nil {
					t.Fatal(e)
				}
			case "state":
				target = filepath.Join(root, ".sharedspace")
			case "git", "ledger":
				s, e := Open(root, map[string]string{"board.md": "seed"})
				if e != nil {
					t.Fatal(e)
				}
				s.Close()
				if kind == "git" {
					target = filepath.Join(root, ".sharedspace", "git")
				} else {
					target = filepath.Join(root, ".sharedspace", "reviews.json")
				}
				if e = os.RemoveAll(target); e != nil {
					t.Fatal(e)
				}
			}
			if e := os.Symlink(external, target); e != nil {
				t.Fatal(e)
			}
			if s, e := Open(root, map[string]string{"board.md": "seed"}); e == nil {
				s.Close()
				t.Fatal("Open followed symlink")
			}
			entries, e := os.ReadDir(external)
			if e != nil || len(entries) != 0 {
				t.Fatal("Open wrote through symlink")
			}
		})
	}
}

func TestLeagueScaffoldAndDataCheckpoint(t *testing.T) {
	s, e := Open(t.TempDir(), map[string]string{"board.md": "board", "league.md": "fictional league"})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	assertRead(t, s, "", "league.md", "fictional league")
	base := head(t, s)
	checkpoint, e := s.Ingest("league.md", "updated fictional league", "scaffold")
	if e != nil || checkpoint == base {
		t.Fatalf("league data checkpoint: %v", e)
	}
	same, e := s.Ingest("league.md", "updated fictional league", "scaffold")
	if e != nil || same != checkpoint {
		t.Fatal("league checkpoint was not idempotent")
	}
	assertRead(t, s, "", "league.md", "updated fictional league")
}

func TestNonRegularDocumentRefusedBeforeCheckpoint(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "candidate")
	target := filepath.Join(s.Root(), "board.md")
	if e := os.Remove(target); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(target, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Propose(p.Base, map[string]string{"board.md": "candidate"}, Attribution{}); e == nil {
		t.Fatal("directory accepted as proposal document")
	}
	if _, e := s.Ingest("board.md", "candidate", "GM"); e == nil {
		t.Fatal("directory accepted as ingest document")
	}
	if _, e := s.Accept(p.ID, p.Base, "GM"); e == nil {
		t.Fatal("directory accepted as checkpoint document")
	}
	if head(t, s) != p.Base {
		t.Fatal("invalid projection advanced accepted ref")
	}
}

func TestFileSymlinkRefusedBeforeCheckpoint(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "candidate")
	target := filepath.Join(s.Root(), "board.md")
	if e := os.Remove(target); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "document")
	if e := os.WriteFile(outside, []byte("untouched"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, target); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Accept(p.ID, p.Base, "GM"); e == nil {
		t.Fatal("checkpoint followed document symlink")
	}
	if _, e := s.Read("", "board.md"); e == nil {
		t.Fatal("read followed document symlink")
	}
	if head(t, s) != p.Base {
		t.Fatal("symlink refusal advanced accepted ref")
	}
	content, e := os.ReadFile(outside)
	if e != nil || string(content) != "untouched" {
		t.Fatal("checkpoint changed symlink target")
	}
}

func TestRejectedCorruptLedgerFailsClosedAndReleasesLock(t *testing.T) {
	s := openTestStore(t)
	p := proposal(t, s, head(t, s), "board.md", "candidate")
	if _, e := s.Reject(p.ID, "coach"); e != nil {
		t.Fatal(e)
	}
	root := s.Root()
	s.Close()
	path := filepath.Join(root, ".sharedspace", "reviews.json")
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("not-json"), 0600); e != nil {
		t.Fatal(e)
	}
	if reopened, e := Open(root, nil); e == nil {
		reopened.Close()
		t.Fatal("corrupt terminal review ledger accepted")
	}
	if e = os.WriteFile(path, original, 0600); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatal("failed Open retained root lock")
	}
	defer reopened.Close()
	if _, e = reopened.Accept(p.ID, p.Base, "GM"); !errors.Is(e, ErrDecided) {
		t.Fatal("terminal reject lost after repaired ledger")
	}
}

func TestRootLockAcrossProcesses(t *testing.T) {
	s := openTestStore(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRootLockChild$")
	cmd.Env = append(os.Environ(), "SHAREDSPACE_LOCK_TEST_ROOT="+s.Root())
	if e := cmd.Run(); e != nil {
		t.Fatal("second process acquired root lock")
	}
}
func TestRootLockChild(t *testing.T) {
	root := os.Getenv("SHAREDSPACE_LOCK_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	if s, e := Open(root, nil); e == nil {
		s.Close()
		t.Fatal("second process acquired root")
	}
}

// A checkout reached through a linked directory (Fedora Silverblue's /home, or
// ~/code -> /mnt/data/code) must work. Links inside the workspace stay refused.
func TestOpenThroughSymlinkedAncestor(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if e := os.Mkdir(real, 0700); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(t.TempDir(), "link")
	if e := os.Symlink(real, link); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(link, "checkout", ".state", "space")
	s, e := Open(root, map[string]string{"board.md": "seed\n"})
	if e != nil {
		t.Fatalf("Open through linked ancestor: %v", e)
	}
	real, e = filepath.EvalSymlinks(real)
	if e != nil {
		t.Fatal(e)
	}
	if want := filepath.Join(real, "checkout", ".state", "space"); s.Root() != want {
		t.Fatalf("root not resolved: %s", s.Root())
	}
	p := proposal(t, s, head(t, s), "prospects/a.md", "dossier\n")
	if _, e = s.Accept(p.ID, p.Base, "GM"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Ingest("injuries/a.json", "{}\n", "coach"); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, nil)
	if e != nil {
		t.Fatalf("reopen through linked ancestor: %v", e)
	}
	assertRead(t, reopened, "", "prospects/a.md", "dossier\n")
	outside := t.TempDir()
	if e = os.Symlink(outside, filepath.Join(reopened.Root(), "stats")); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.Ingest("stats/a.json", "{}", "GM"); e == nil {
		t.Fatal("ingest followed a link inside the workspace")
	}
	reopened.Close()
	if e = os.Remove(filepath.Join(real, "checkout", ".state", "space", "stats")); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(filepath.Join(real, "checkout", ".state", "space"), filepath.Join(real, "moved")); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(real, "moved"), filepath.Join(real, "checkout", ".state", "space")); e != nil {
		t.Fatal(e)
	}
	if s, e := Open(root, nil); e == nil {
		s.Close()
		t.Fatal("Open followed a linked workspace root")
	}
}

func TestInterruptedFirstOpenCanBeRetried(t *testing.T) {
	root := t.TempDir()
	// Fail just before the seed commit is published, as if the first start
	// was killed between creating the repository and writing its ref.
	testGitHook = func(args []string) error {
		if len(args) > 0 && args[0] == "update-ref" {
			return errors.New("interrupted")
		}
		return nil
	}
	s, err := Open(root, map[string]string{"board.md": "seed\n"})
	testGitHook = nil
	if err == nil {
		s.Close()
		t.Fatal("interrupted first open reported success")
	}
	if _, err := os.Lstat(filepath.Join(root, ".sharedspace", "git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted first open left a repository in place")
	}
	// A run killed outright leaves its temporary repository behind.
	leftover := filepath.Join(root, ".sharedspace", initPrefix+"killed")
	if out, err := exec.Command("git", "init", "-q", "--bare", leftover).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	s, err = Open(root, map[string]string{"board.md": "seed\n"})
	if err != nil {
		t.Fatalf("retry after interrupted first open: %v", err)
	}
	defer s.Close()
	assertRead(t, s, "", "board.md", "seed\n")
	if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unfinished repository was not cleaned up")
	}
}
