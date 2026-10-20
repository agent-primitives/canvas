package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
)

// ConversationMessage is a canvas-owned room post or an explicit question turn.
// Human room posts never become accepted documents or Sessions log records.
type ConversationMessage struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Actor         string    `json:"actor"`
	Text          string    `json:"text"`
	At            time.Time `json:"at"`
	Run           string    `json:"run,omitempty"`
	Session       string    `json:"session,omitempty"`
	Agent         string    `json:"agent,omitempty"`
	Status        string    `json:"status,omitempty"`
	Answer        string    `json:"answer,omitempty"`
	Model         string    `json:"model,omitempty"`
	Error         string    `json:"error,omitempty"`
	Seconds       float64   `json:"seconds,omitempty"`
	Started       time.Time `json:"started,omitempty"`
	Completed     time.Time `json:"completed,omitempty"`
	LastSeq       int64     `json:"lastSeq,omitempty"`
	ResumeStatus  string    `json:"resumeStatus,omitempty"`
	RecoveryError string    `json:"recoveryError,omitempty"`
	// Requester and owner stay separate from the unverified Actor label.
	RequestedBy    sharedspace.ReviewActor `json:"requestedBy,omitzero"`
	ExecutionOwner api.IdentityRef         `json:"executionOwner,omitzero"`
}

type conversationRecord struct {
	ConversationMessage
	Input       string `json:"input,omitempty"` // private journaled question intent, omitted from State
	ExpectedSeq int64  `json:"expectedSeq,omitempty"`
	Harness     string `json:"harness,omitempty"`
}
type conversationLedger struct {
	Entries []conversationRecord `json:"entries"`
}

func (c *Canvas) openConversation() error {
	path := filepath.Join(c.root, "conversation.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return internalError(errors.New("cannot read regular canvas conversation store"))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return internalError(errors.New("invalid canvas conversation store"))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return internalError(errors.New("invalid canvas conversation store"))
	}
	key, err := dec.Token()
	if err != nil || key != "entries" || dec.Decode(&c.conversation.Entries) != nil || c.conversation.Entries == nil || dec.More() {
		return internalError(errors.New("invalid canvas conversation store"))
	}
	end, err := dec.Token()
	var extra any
	if err != nil || end != json.Delim('}') || dec.Decode(&extra) != io.EOF {
		return internalError(errors.New("invalid canvas conversation store"))
	}
	ids := map[string]bool{}
	for i := range c.conversation.Entries {
		entry := &c.conversation.Entries[i]
		if entry.ID == "" || ids[entry.ID] || !validRole(entry.Actor) || strings.TrimSpace(entry.Text) == "" || !safeText(entry.Text, 2000) || entry.At.IsZero() {
			return internalError(errors.New("invalid stored room attribution"))
		}
		ids[entry.ID] = true
		switch entry.Kind {
		case "comment":
			if entry.Session != "" || entry.Agent != "" || entry.Answer != "" || entry.Input != "" || entry.Status != "" {
				return internalError(errors.New("room comment contains agent execution data"))
			}
		case "question":
			if entry.Session == "" || entry.Run == "" || !c.recordedAgent(entry.Agent) || entry.Input == "" || entry.Harness == "" || entry.ExpectedSeq < 0 {
				return internalError(errors.New("invalid recorded question intent"))
			}
			switch entry.Status {
			case "queued", "running":
				entry.Status = "uncertain"
				entry.Error = "Question interrupted. No automatic retry; ask again explicitly."
				entry.Completed = time.Now().UTC()
			case "answered":
				if !validAnswer(entry.Answer) {
					return internalError(errors.New("invalid stored agent answer"))
				}
			case "failed", "uncertain":
			default:
				return internalError(errors.New("invalid stored question state"))
			}
		default:
			return internalError(errors.New("invalid stored conversation entry"))
		}
	}
	return c.saveConversationLocked()
}

func (c *Canvas) saveConversationLocked() error {
	raw, err := json.MarshalIndent(c.conversation, "", "  ")
	if err != nil {
		return internalError(errors.New("cannot encode canvas conversation"))
	}
	path := filepath.Join(c.root, "conversation.json")
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return internalError(errors.New("cannot persist canvas conversation"))
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return internalError(errors.New("cannot persist canvas conversation"))
	}
	if os.Rename(path+".tmp", path) != nil {
		return internalError(errors.New("cannot publish canvas conversation"))
	}
	syncDirectory := c.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncCanvasDirectory
	}
	if syncDirectory(c.root) != nil {
		return errCanvasPublicationUncertain
	}
	return nil
}

func (c *Canvas) Comment(runID, text, role string) (string, error) {
	text = strings.TrimSpace(text)
	if !validRole(role) || text == "" || !safeText(text, 2000) {
		return "", errors.New("invalid room role or text")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return "", errors.New("canvas stopping")
	}
	if runID != "" && c.findLocked(runID) == nil {
		return "", errNotFound
	}
	entry := conversationRecord{ConversationMessage: ConversationMessage{ID: ID(), Kind: "comment", Actor: role, Text: text, At: time.Now().UTC(), Run: runID}}
	if err := c.appendConversationLocked(entry); err != nil {
		return "", err
	}
	return entry.ID, nil
}

func (c *Canvas) appendConversationLocked(entry conversationRecord) error {
	n := len(c.conversation.Entries)
	c.conversation.Entries = append(c.conversation.Entries, entry)
	if err := c.saveConversationLocked(); err != nil {
		if !errors.Is(err, errCanvasPublicationUncertain) {
			c.conversation.Entries = c.conversation.Entries[:n]
		}
		c.persistenceFailureLocked(nil)
		return internalError(err)
	}
	return nil
}

func (c *Canvas) conversationStateLocked() []ConversationMessage {
	messages := make([]ConversationMessage, 0, len(c.conversation.Entries))
	for _, entry := range c.conversation.Entries {
		message := entry.ConversationMessage
		message.ResumeStatus, message.RecoveryError = c.recoveryMetadataLocked(entry.Session, entry.ID)
		messages = append(messages, message)
	}
	return messages
}
