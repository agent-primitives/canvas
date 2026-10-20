package canvas

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoomCommentsPersistAttributionWithoutSessionsOrSharedSpaceChanges(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	before, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	id, err := c.Comment("", "Could the owner explain our cap constraint?", "coach")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRole("owner"); err != nil {
		t.Fatal(err)
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Conversation) != 1 {
		t.Fatal("room comment missing")
	}
	entry := state.Conversation[0]
	if entry.ID != id || entry.Kind != "comment" || entry.Actor != "coach" || entry.Text != "Could the owner explain our cap constraint?" || entry.At.Before(start) || entry.At.After(time.Now().UTC()) {
		t.Fatal("room attribution or actual timestamp missing")
	}
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	if err != nil || len(sessions) != 0 || state.Head != before.Head || len(state.History) != len(before.History) || len(state.Proposals) != 0 || len(state.Runs) != 0 {
		t.Fatal("posting a room question spent a call or wrote shared/session history")
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.State()
	if err != nil || len(restored.Conversation) != 1 || restored.Conversation[0] != entry {
		t.Fatal("stored comment lost identity, actor, text or timestamp on reopen")
	}
	raw, err := os.ReadFile(filepath.Join(c.root, "conversation.json"))
	if err != nil || !strings.Contains(string(raw), "cap constraint") {
		t.Fatal("comments must live in canvas's separate private store")
	}
	attempts, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil || strings.Contains(string(attempts), "cap constraint") {
		t.Fatal("room text must not enter attempt ledger")
	}
}

func TestRoomCommentValidationAndPersistenceFailureDoNotInventPosts(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	for _, item := range []struct{ text, actor string }{{" ", "GM"}, {strings.Repeat("x", 2001), "GM"}, {"ordinary comment", "unknown"}, {"sk-proj-" + strings.Repeat("z", 25), "GM"}} {
		if _, err := c.Comment("", item.text, item.actor); err == nil {
			t.Fatal("invalid room post accepted")
		}
	}
	if _, err := c.Comment("missing-run", "ordinary comment", "GM"); err == nil {
		t.Fatal("unrecorded run reference accepted")
	}
	if err := os.Mkdir(filepath.Join(c.root, "conversation.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Comment("", "not saved", "GM"); err == nil {
		t.Fatal("unsaved post reported success")
	}
	state, err := c.State()
	if err != nil || len(state.Conversation) != 0 {
		t.Fatal("failed write left a phantom comment")
	}
}

func TestRoomStoreCorruptionFailsClosed(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.Close()
	for _, raw := range []string{`{"entries": [{"kind":"comment","text":"unattributed"}]}`, `null`, `{}`, `{"entries":null}`, `{"entries":[],"entries":[]}`} {
		if err := os.WriteFile(filepath.Join(c.root, "conversation.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder); err == nil {
			reopened.Close()
			t.Errorf("invalid stored conversation did not fail closed: %s", raw)
		}
	}
}

func TestPublishedRoomPostRetainsIdentityOnDirectorySyncFailure(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	failDirectorySync(c)
	if _, err := c.Comment("", "published but durability uncertain", "owner"); err == nil {
		t.Fatal("directory sync failure hidden")
	}
	raw, err := os.ReadFile(filepath.Join(c.root, "conversation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored conversationLedger
	if json.Unmarshal(raw, &stored) != nil || len(stored.Entries) != 1 {
		t.Fatal("published room identity was rolled back")
	}
	state, err := c.State()
	if err != nil || len(state.Conversation) != 1 || state.Conversation[0].ID != stored.Entries[0].ID || len(state.Errors) == 0 {
		t.Fatal("published room post or persistence warning lost")
	}
}
