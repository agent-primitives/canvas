package sessionhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	_ "modernc.org/sqlite"
)

// This fictional database was written through the old pinned Canvas host, not
// assembled using the new schema. Opening it must migrate without rewriting any
// recorded evidence, and completed legacy turns must remain zero-call.
func TestOldPinJournalMigratesAndReconstructsCompletedFork(t *testing.T) {
	const parent = "sess-c57c81f0cff45e7d901a9620"
	const child = "sess-f6270999faf93f2b7eb1f0ff"
	root := t.TempDir()
	path := filepath.Join(root, "journal.db")
	fixture, err := os.ReadFile("testdata/9a3d755-completed.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	beforeVersion, before := upgradeEvidence(t, path)
	if beforeVersion != 1 || len(before) != 13 {
		t.Fatalf("old fixture shape: schema=%d records=%d", beforeVersion, len(before))
	}
	b := bundle(t, "old pin fixture instructions", "fixture-model")
	calls := 0
	h, err := Start(root, []contract.Bundle{b}, func(_ context.Context, _ api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{}, errors.New("upgrade must never call a provider")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	version, after := upgradeEvidence(t, path)
	if version != 5 {
		t.Fatalf("journal schema after host startup = %d, want 5", version)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote recorded evidence")
	}
	c := connect(t, h)
	for _, uid := range []string{parent, child} {
		session, err := c.GetSession(t.Context(), &v1.GetSessionRequest{Uid: uid})
		if err != nil || session.GetHarness() != "bundle-"+b.Digest || uid == parent && session.GetLabels()["fixture"] != "old-pin" {
			t.Fatalf("old session binding lost: %s %v", uid, err)
		}
		if uid == child && (session.GetParentUid() != parent || session.GetForkSeq() != 4 || session.GetMetadata().GetName() != "old-child") {
			t.Fatal("old fork lineage changed")
		}
		proof, err := sessionclient.Verify(t.Context(), h.Address(), uid)
		if err != nil || !proof.ChainValid || !proof.Reconstructed || !proof.OutputsMatch || !proof.LiveUnchanged || proof.ModelCalls != 0 {
			t.Fatalf("old completed journal failed zero-call reconstruction: %+v %v", proof, err)
		}
		if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: uid}); err != nil {
			t.Fatalf("completed old session Resume: %v", err)
		}
	}
	if calls != 0 {
		t.Fatal("upgrade, verification or completed Resume called provider")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	// Startup after the migration is also safe; it is not a rollback test.
	reopened, err := Start(root, []contract.Bundle{b}, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{}, errors.New("reopen must never call a provider")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if calls != 0 {
		t.Fatal("migrated startup reissued model work")
	}
}

type upgradeRecord struct {
	Session, PrevHash, Hash string
	Seq, Fence              int64
	Event                   []byte
}

func upgradeEvidence(t *testing.T, path string) (int, []upgradeRecord) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT session, seq, fence, prev_hash, hash, event FROM events ORDER BY session, seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var records []upgradeRecord
	for rows.Next() {
		var r upgradeRecord
		if err := rows.Scan(&r.Session, &r.Seq, &r.Fence, &r.PrevHash, &r.Hash, &r.Event); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return version, records
}
