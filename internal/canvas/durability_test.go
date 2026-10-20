package canvas

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanvasLedgerRenameSyncsDirectory(t *testing.T) {
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("strace unavailable")
	}
	root := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	cmd := exec.Command(strace, "-f", "-yy", "-e", "trace=fsync,rename,renameat,renameat2", "-o", trace, os.Args[0], "-test.run=^TestCanvasLedgerSyncChild$")
	cmd.Env = append(os.Environ(), "CANVAS_LEDGER_SYNC_TEST_ROOT="+root)
	if err := cmd.Run(); err != nil {
		t.Fatal("ledger syscall exercise failed")
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	published := false
	directorySynced := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "rename") && strings.Contains(line, "attempts.json") && strings.Contains(line, "= 0") {
			published = true
		}
		if published && strings.Contains(line, "fsync(") && strings.Contains(line, "<"+root+">") && strings.Contains(line, "= 0") {
			directorySynced = true
		}
	}
	if !published || !directorySynced {
		t.Fatal("ledger publication lacks successful directory fsync after rename")
	}
}

func TestCanvasLedgerSyncChild(t *testing.T) {
	root := os.Getenv("CANVAS_LEDGER_SYNC_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	c := &Canvas{root: root, data: ledger{Role: "GM"}}
	if err := c.saveLocked(); err != nil {
		t.Fatal("ledger save failed")
	}
}
