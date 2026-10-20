package contract

import (
	"os"
	"path/filepath"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
)

func TestReleasedArchiveRefusesPathEscapeBeforeExtraction(t *testing.T) {
	jail := t.TempDir()
	t.Setenv("TMPDIR", jail)
	witness := filepath.Join(jail, "escape")
	if err := os.WriteFile(witness, []byte("outside content"), 0600); err != nil {
		t.Fatal(err)
	}
	control := repackedRelease(t, func(map[string]retainedFile, *agentsuite.Suite) {})
	if _, err := LoadReleasedSuite("control", control, nil); err != nil {
		t.Fatal("valid repacked release refused", err)
	}
	for _, name := range []string{"../../escape", "dir/../../../escape", witness, `..\..\escape`, ".hidden", "dir/../escape"} {
		t.Run(name, func(t *testing.T) {
			ref := repackedRelease(t, func(content map[string]retainedFile, _ *agentsuite.Suite) {
				content[name] = retainedFile{Data: []byte("attack"), Mode: 0644}
			})
			// The helper recomputes layer/manifest digests, so a bad digest
			// cannot conceal a missing archive-name guard.
			files, err := capture(ref.Layout)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := releaseContent(files, ref.Ref); err == nil {
				t.Fatal("unsafe archive entry reached retained content")
			}
			if _, err := LoadReleasedSuite("attack", ref, nil); err == nil {
				t.Fatal("unsafe released suite accepted")
			}
			got, err := os.ReadFile(witness)
			if err != nil || string(got) != "outside content" {
				t.Fatal("archive extraction escaped its private content directory")
			}
		})
	}
}
