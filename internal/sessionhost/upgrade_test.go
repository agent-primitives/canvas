package sessionhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/eventlog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// ce018b2-native.db was generated, not assembled or rewritten, in a detached
// Canvas worktree at 58be923 with umask 022 and GOWORK=off. Its go.mod replaces
// Sessions with github.com/gambtho/agentsessions v0.1.3-0.20261018160920-ce018b2daa0f
// and the Substrate integration with v0.0.0-20261018160920-ce018b2daa0f.
// To reproduce: add a temporary TestGenerateCe018b2Journal in that checkout using
// nativeBundle(t), Start(t.TempDir(), []contract.Bundle{b}, scriptedProvider), and
// connect(t, h). Create two sessions with contract.HarnessName(b.Digest, 4), named
// fork-pin-completed and fork-pin-unanswered; run each with {"mode":"question"}
// using run(t.Context(), c, uid, input). The first provider call returns assistant
// {"answer":"ce018b2-fictional"}; the second returns errors.New("fictional
// interrupted provider"), leaving a native model intent without a response.
// Run GOWORK=off go test ./internal/sessionhost -run '^TestGenerateCe018b2Journal$'
// -count=1 -v; after h.Close(), copy root/journal.db here (never a live main file).
// Generation made exactly two scripted calls, used no real model/operator data,
// and confirmed the fork replacement in debug.ReadBuildInfo(). Random session
// and execution IDs will differ on regeneration. This catches request/hash or
// harness changes that would break reconstruction across the fork/upstream boundary.
func TestForkPinNativeJournalVerifiesAndResumes(t *testing.T) {
	const completed = "sess-9c7ee38a3e6848bdf72f6008"
	const unanswered = "sess-4d24aba402d7b3aefa027e41"
	root := t.TempDir()
	path := filepath.Join(root, "journal.db")
	fixture, err := os.ReadFile("testdata/ce018b2-native.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	beforeVersion, before := upgradeEvidence(t, path)
	if beforeVersion != 5 || len(before) != 9 {
		t.Fatalf("fork fixture shape: schema=%d records=%d", beforeVersion, len(before))
	}
	b := nativeBundle(t)
	var calls atomic.Int32
	requests := make(chan api.ModelRequest, 2)
	h, err := Start(root, []contract.Bundle{b}, func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		requests <- r
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"answer":"ce018b2-recovered"}`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	version, after := upgradeEvidence(t, path)
	if version != 5 || !reflect.DeepEqual(before, after) {
		t.Fatal("opening schema-5 fork journal changed recorded evidence")
	}
	c := connect(t, h)
	for _, uid := range []string{completed, unanswered} {
		s, err := c.GetSession(t.Context(), &v1.GetSessionRequest{Uid: uid})
		if err != nil || s.GetHarness() != "bundle-v4-"+b.Digest {
			t.Fatalf("native fork session binding lost: %s %v", uid, err)
		}
		records, err := h.store.Session(uid).Read(1)
		if err != nil {
			t.Fatal(err)
		}
		if err := eventlog.NewFrom(records).Verify(); err != nil {
			t.Fatalf("fork journal chain invalid: %s %v", uid, err)
		}
		if len(records) == 0 || records[0].Event.Kind != api.EventExecutionStart {
			t.Fatal("fixture is not a native execution")
		}
		models, outputs, ends := 0, 0, 0
		for _, record := range records {
			switch record.Event.Kind {
			case api.EventModelCall:
				models++
			case api.EventOutput:
				outputs++
			case api.EventEnd:
				ends++
			}
		}
		wantCompleted := 0
		if uid == completed {
			wantCompleted = 1
		}
		if models != 1 || outputs != wantCompleted || ends != wantCompleted {
			t.Fatalf("fork fixture turn shape: %s models=%d outputs=%d ends=%d", uid, models, outputs, ends)
		}
	}
	proof, err := sessionclient.Verify(t.Context(), h.Address(), completed)
	if err != nil || !proof.ChainValid || !proof.Reconstructed || !proof.OutputsMatch || !proof.LiveUnchanged || proof.ModelCalls != 0 {
		t.Fatalf("completed fork turn failed zero-call reconstruction: %+v %v", proof, err)
	}
	if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: completed}); err != nil {
		t.Fatalf("completed fork Resume: %v", err)
	}
	if _, err := sessionclient.Verify(t.Context(), h.Address(), unanswered); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unanswered fork intent verification = %v, want FailedPrecondition", err)
	}
	if calls.Load() != 0 {
		t.Fatal("startup, verification or completed Resume reissued model work")
	}
	// An unanswered intent is not a zero-call completion: only explicit Resume
	// may reissue it, exactly once. Subsequent Resume and replay remain zero-call.
	if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: unanswered}); err != nil {
		t.Fatalf("unanswered fork Resume: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unanswered Resume calls = %d, want 1", calls.Load())
	}
	r := <-requests
	if r.Model != b.Model || r.Params["max_tokens"] != "16384" || r.Params["response_format"] != questionSchemaFixture || len(r.Messages) != 2 || r.Messages[1].Text() != `{"mode":"question"}` {
		t.Fatal("unanswered Resume changed the recorded native request")
	}
	if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: unanswered}); err != nil {
		t.Fatal(err)
	}
	proof, err = sessionclient.Verify(t.Context(), h.Address(), unanswered)
	if err != nil || !proof.ChainValid || !proof.Reconstructed || !proof.OutputsMatch || !proof.LiveUnchanged || proof.ModelCalls != 0 || calls.Load() != 1 {
		t.Fatalf("recovered fork turn failed zero-extra-call reconstruction: %+v %v", proof, err)
	}
	_, after = upgradeEvidence(t, path)
	// Resume may append evidence, but must never rewrite the historical prefix.
	for _, original := range before {
		unchanged := false
		for _, current := range after {
			if current.Session == original.Session && current.Seq == original.Seq {
				unchanged = reflect.DeepEqual(original, current)
				break
			}
		}
		if !unchanged {
			t.Fatal("Resume rewrote historical fork evidence")
		}
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
