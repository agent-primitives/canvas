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
	if !ledgerTraceSynced(root, string(raw)) {
		t.Fatalf("ledger publication lacks successful directory fsync after rename:\n%s", raw)
	}
}

func ledgerTraceSynced(root, raw string) bool {
	type pendingCall struct {
		prefix    string
		published bool
	}
	pending := map[string]pendingCall{}
	published := false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		thread := fields[0]
		call := strings.TrimSpace(strings.TrimPrefix(line, thread))
		afterPublication := published
		if prefix, unfinished := strings.CutSuffix(call, "<unfinished ...>"); unfinished {
			pending[thread] = pendingCall{prefix, published}
			continue
		}
		if strings.HasPrefix(call, "<... ") {
			name, suffix, resumed := strings.Cut(strings.TrimPrefix(call, "<... "), " resumed>")
			saved, found := pending[thread]
			delete(pending, thread)
			if !resumed || !found || !strings.HasPrefix(saved.prefix, name+"(") {
				continue
			}
			call, afterPublication = saved.prefix+suffix, saved.published
		}
		if !strings.HasSuffix(strings.TrimSpace(call), "= 0") {
			continue
		}
		if strings.HasPrefix(call, "rename") && strings.Contains(call, `"`+filepath.Join(root, "attempts.json")+`"`) {
			published = true
		}
		if afterPublication && strings.HasPrefix(call, "fsync(") && strings.Contains(call, "<"+root+">") {
			return true
		}
	}
	return false
}

func TestLedgerTraceHandlesInterleavedSyscalls(t *testing.T) {
	const rename = `17 renameat(AT_FDCWD, "/ledger/attempts.json.tmp", AT_FDCWD, "/ledger/attempts.json") = 0` + "\n"
	for _, tc := range []struct {
		name, trace string
		want        bool
	}{
		{"split-directory-sync", rename + "21 fsync(3</ledger> <unfinished ...>\n17 --- SIGURG ---\n21 <... fsync resumed>) = 0\n", true},
		{"split-rename", "17 renameat(AT_FDCWD, \"/ledger/attempts.json.tmp\", AT_FDCWD, \"/ledger/attempts.json\" <unfinished ...>\n21 --- SIGURG ---\n17 <... renameat resumed>) = 0\n21 fsync(3</ledger>) = 0\n", true},
		{"failed-sync", rename + "21 fsync(3</ledger> <unfinished ...>\n21 <... fsync resumed>) = -1 EIO\n", false},
		{"unfinished-sync", rename + "21 fsync(3</ledger> <unfinished ...>\n", false},
		{"wrong-thread", rename + "21 fsync(3</ledger> <unfinished ...>\n22 <... fsync resumed>) = 0\n", false},
		{"sync-before-publication", "21 fsync(3</ledger>) = 0\n" + rename, false},
		{"sync-started-before-publication", "21 fsync(3</ledger> <unfinished ...>\n" + rename + "21 <... fsync resumed>) = 0\n", false},
		{"file-sync-only", rename + "21 fsync(3</ledger/attempts.json>) = 0\n", false},
		{"unrelated-publication", "17 renameat(AT_FDCWD, \"/other/attempts.json.tmp\", AT_FDCWD, \"/other/attempts.json\") = 0\n21 fsync(3</ledger>) = 0\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ledgerTraceSynced("/ledger", tc.trace); got != tc.want {
				t.Fatalf("durable trace = %v, want %v: %s", got, tc.want, tc.trace)
			}
		})
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
