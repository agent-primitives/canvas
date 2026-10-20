package canvas

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

const feedbackJSON = `{"team":"Northwind","feedback":"Fictional observation; outcomes remain unmeasured."}`

func writeFeedback(t *testing.T, c *Canvas, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.space.Root(), "feedback", name), []byte(feedbackJSON), 0600); err != nil {
		t.Fatal(err)
	}
}

func hasError(c *Canvas, prefix string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.errors {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// failingIngest fails checkpoints for one input path and counts the tries.
type failingIngest struct {
	sharedspace.SharedSpace
	mu    sync.Mutex
	path  string
	tries int
}

func (f *failingIngest) Ingest(path, content, actor string) (string, error) {
	if path == f.path {
		f.mu.Lock()
		f.tries++
		f.mu.Unlock()
		return "", errors.New("synthetic checkpoint failure")
	}
	return f.SharedSpace.Ingest(path, content, actor)
}
func (f *failingIngest) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries
}

func TestInputScanRefusesSymlinksFIFOsAndOversizeFiles(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	dir := filepath.Join(c.space.Root(), "feedback")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(feedbackJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.json"), 0600); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat(" ", (64<<10)+1)
	if err := os.WriteFile(filepath.Join(dir, "big.json"), []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.ScanInputs() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan blocked on a FIFO")
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 0 {
		t.Fatal("unsafe declared input queued a run")
	}
	if !hasError(c, "A declared input is not a bounded regular file; no model was invoked.") {
		t.Fatal("unsafe declared input not reported")
	}
	for _, name := range []string{"link.json", "pipe.json", "big.json"} {
		if _, err := c.readInput("feedback/" + name); err == nil {
			t.Fatalf("%s read as a report", name)
		}
	}
}

func TestInputScanContinuesPastAFailingFileAndBacksOff(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	failing := &failingIngest{SharedSpace: c.space, path: "feedback/a-bad.json"}
	c.space = failing
	writeFeedback(t, c, "a-bad.json")
	writeFeedback(t, c, "b-good.json")
	if err := c.ScanInputs(); err != nil {
		t.Fatalf("one failing file stopped the scan: %v", err)
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 {
		t.Fatalf("later report not queued: %d runs", len(state.Runs))
	}
	completed(t, c, state.Runs[0].ID)
	if !hasError(c, "A declared input could not be checkpointed or queued") {
		t.Fatal("failing report not reported")
	}
	for i := 0; i < 3; i++ {
		if err := c.ScanInputs(); err != nil {
			t.Fatal(err)
		}
	}
	if failing.count() != 1 {
		t.Fatalf("failing report retried every scan: %d tries", failing.count())
	}
	c.scanMu.Lock()
	for key := range c.reportRetry {
		c.reportRetry[key] = time.Now().Add(-time.Second)
	}
	c.scanMu.Unlock()
	failing.path = ""
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ = c.State()
	if len(state.Runs) != 2 {
		t.Fatalf("report not retried after the delay: %d runs", len(state.Runs))
	}
	completed(t, c, state.Runs[1].ID)
}

func TestInputFileActorIsNotAnInventedPersona(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	writeFeedback(t, c, "dropped.json")
	if err := c.WriteInput("feedback/submitted.json", feedbackJSON, "coach"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, err := c.State()
	if err != nil || len(state.Runs) != 2 {
		t.Fatalf("input runs: %v %+v", err, state.Runs)
	}
	for _, r := range state.Runs {
		r = completed(t, c, r.ID)
		if r.Actor != fileActor || r.Status != "proposed" {
			t.Fatalf("discovery invented persona: %+v", r)
		}
	}
	state, _ = c.State()
	for _, proposal := range state.Proposals {
		if proposal.Attribution.Actor != fileActor {
			t.Fatal("proposal invented a submitter authority")
		}
	}
	if len(state.History) != 3 {
		t.Fatalf("expected seed and two input checkpoints: %+v", state.History)
	}
	for _, checkpoint := range state.History {
		if checkpoint.Summary == "Ingest data checkpoint" && checkpoint.Actor != fileActor {
			t.Fatal("checkpoint invented a persona")
		}
	}
}
