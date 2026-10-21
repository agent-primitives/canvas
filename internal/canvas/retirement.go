package canvas

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// ErrRetired deliberately exposes neither the state path nor seal contents.
var ErrRetired = errors.New("canvas is retired and read-only")

// CheckWritable is a read-only, fail-closed check, including broken links and
// malformed/special seals. It is also safe before the state root exists.
func CheckWritable(root string) error {
	if _, err := os.Lstat(filepath.Join(root, "retired.json")); os.IsNotExist(err) {
		return nil
	}
	return ErrRetired
}

type ArchivedDocument struct {
	Content string `json:"content"`
	Digest  string `json:"digest"`
}

// ArchivedAnnouncement deliberately excludes actionable confirmations and the
// private operation journal. All nested snapshots contain only value fields.
type ArchivedAnnouncement struct {
	ID              string                  `json:"id"`
	Session         string                  `json:"session"`
	Status          string                  `json:"status"`
	Concluded       bool                    `json:"concluded"`
	Snapshot        announcement.Snapshot   `json:"snapshot"`
	Receipt         *announcement.Receipt   `json:"receipt,omitempty"`
	Reviewer        sharedspace.ReviewActor `json:"reviewer"`
	IdentitySource  string                  `json:"identitySource"`
	CurrentRevision string                  `json:"currentRevision"`
	Newer           bool                    `json:"newer"`
}

type Archive struct {
	Announcements []ArchivedAnnouncement      `json:"announcements,omitempty"`
	State         json.RawMessage             `json:"state"`
	Flows         json.RawMessage             `json:"flows"`
	Revision      string                      `json:"revision"`
	Documents     map[string]ArchivedDocument `json:"documents"`
}

var retirementID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var errRetirementBusy = errors.New("canvas cannot retire: finish, reject or reconcile outstanding work first")
var errRetirementCapture = errors.New("cannot capture coherent canvas history within 64 MiB")

const archiveLimit = 64 << 20

// retirementBindingLocked binds both private work journals without publishing
// their inputs, credentials or command reservations into the archive.
func (c *Canvas) retirementBindingLocked() ([32]byte, error) {
	raw, err := json.Marshal(struct {
		Work ledger
		Room conversationLedger
	}{c.data, c.conversation})
	return sha256.Sum256(raw), err
}

// SealForHistory persists immutable public history before permanently retiring
// scheduling. The caller owns SharedSpace/client closure and HTTP exclusion.
// persist must only save the archive: it runs under the reservation boundary.
func (c *Canvas) SealForHistory(nextID string, persist func(Archive) error) error {
	if !retirementID.MatchString(nextID) || !safeText(nextID, 128) || persist == nil {
		return errors.New("invalid retirement request")
	}
	if err := CheckWritable(c.root); err != nil {
		return err
	}
	c.mu.Lock()
	binding, bindErr := c.retirementBindingLocked()
	busy := c.flowAdmissionBusyLocked() || c.activeExecutions != 0
	attempts := append([]attempt(nil), c.data.Attempts...)
	flowIDs := make(map[string]string, len(c.data.Flows))
	for _, f := range c.data.Flows {
		flowIDs[f.ID] = f.Flow
	}
	ops := append([]announcementOperation(nil), c.data.Announcements...)
	c.mu.Unlock()
	if busy || bindErr != nil {
		return errRetirementBusy
	}
	// The existing panel snapshot owns publicationMu itself. Bind its result to
	// unchanged journals below rather than recursively acquiring that mutex.
	flows := json.RawMessage("null")
	if c.flowOptions != nil {
		panel, err := c.flowPanelSnapshot()
		if err != nil {
			return errRetirementCapture
		}
		flows, err = json.Marshal(panel)
		if err != nil {
			return errRetirementCapture
		}
	}
	c.publicationMu.Lock()
	err := func() error {
		state, err := c.State()
		if err != nil {
			return errRetirementCapture
		}
		for _, p := range state.Proposals {
			if p.Status != "accepted" && p.Status != "rejected" {
				return errRetirementBusy
			}
		}
		// Publication recovered after a crash is not proof of clean execution.
		// Read the exact native execution outside mu, then bind it again below.
		for _, a := range attempts {
			if !a.Completed.IsZero() {
				continue
			}
			if a.Proposal == "" {
				return errRetirementBusy
			}
			native, err := c.readFlowExecution(c.ctx, a)
			if err != nil || !native.Settled || !native.Complete {
				return errRetirementBusy
			}
			var changes map[string]string
			if a.FlowRun != "" {
				changes, err = c.definition.ChangesForStep(flowIDs[a.FlowRun], a.FlowStep, native.Output)
			} else {
				changes, err = c.definition.Changes(a.Agent, native.Output)
			}
			if err != nil {
				return errRetirementBusy
			}
			matched := false
			for _, p := range state.Proposals {
				at := p.Attribution
				if p.ID == a.Proposal && p.Base == a.Base && maps.Equal(p.Changes, changes) &&
					at.Run == a.ID && at.Session == a.Session && at.Agent == a.Agent &&
					at.BundleDigest == a.BundleDigest && at.AuthoredBundleDigest == a.AuthoredBundleDigest &&
					at.ExecutionOwner == a.ExecutionOwner && at.RequestedBy == a.RequestedBy &&
					at.TriggeredBy == a.TriggeredBy && at.TriggeringReview == a.TriggeringReview &&
					at.FlowRun == a.FlowRun && at.FlowStep == a.FlowStep && maps.Equal(at.Sources, a.Sources) {
					matched = true
				}
			}
			if !matched {
				return errRetirementBusy
			}
		}
		var announcements []ArchivedAnnouncement
		for _, op := range ops {
			if op.Stage == "pending" || op.Stage == "uncertain" || op.EffectReserved && !op.EffectSettled {
				return errRetirementBusy
			}
			view, err := c.inspectAnnouncement(c.ctx, op)
			if err != nil || !view.Concluded {
				return errRetirementBusy
			}
			archived := ArchivedAnnouncement{
				ID: view.ID, Session: view.Session, Status: view.Status, Concluded: view.Concluded,
				Snapshot: view.Snapshot, Reviewer: view.Reviewer, IdentitySource: view.IdentitySource,
				CurrentRevision: view.CurrentRevision, Newer: view.Newer,
			}
			if view.Receipt != nil {
				receipt := *view.Receipt
				archived.Receipt = &receipt
			}
			announcements = append(announcements, archived)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		now, err := c.retirementBindingLocked()
		if err != nil || now != binding || c.flowAdmissionBusyLocked() || c.activeExecutions != 0 {
			return errRetirementBusy
		}
		if c.flowOptions != nil {
			var panel flowPanelState
			if json.Unmarshal(flows, &panel) != nil || panel.Head != state.Head {
				return errRetirementCapture
			}
		}
		public, err := json.Marshal(projectAPIState(state))
		if err != nil {
			return errRetirementCapture
		}
		archive := Archive{Announcements: announcements, State: public, Flows: flows, Revision: state.Head, Documents: map[string]ArchivedDocument{}}
		paths, err := c.space.Paths(state.Head)
		if err != nil {
			return errRetirementCapture
		}
		size := len(public) + len(flows)
		for _, path := range paths {
			content, err := c.space.Read(state.Head, path)
			size += len(content)
			if err != nil || size > archiveLimit || !utf8.ValidString(content) {
				return errRetirementCapture
			}
			archive.Documents[path] = ArchivedDocument{Content: content, Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}
		}
		raw, err := json.Marshal(archive)
		if err != nil || len(raw) > archiveLimit {
			return errRetirementCapture
		}
		if head, err := c.space.Head(); err != nil || head != state.Head {
			return errRetirementCapture
		}
		if err := persist(archive); err != nil {
			return err
		}
		if head, err := c.space.Head(); err != nil || head != state.Head || c.flowAdmissionBusyLocked() || c.activeExecutions != 0 {
			return errRetirementCapture
		}
		if err := c.writeRetirementSeal(nextID, state.Head); err != nil {
			return err
		}
		c.cancel()
		return nil
	}()
	c.publicationMu.Unlock()
	if err == nil {
		// Finishing workers can need publicationMu; never join while holding it.
		c.wg.Wait()
	}
	return err
}

func (c *Canvas) writeRetirementSeal(nextID, head string) error {
	raw, err := json.Marshal(struct {
		NextID     string `json:"nextID"`
		Definition string `json:"definition"`
		Head       string `json:"head"`
	}{nextID, c.data.DefinitionDigest, head})
	if err != nil {
		return errRetirementCapture
	}
	f, err := os.CreateTemp(c.root, ".retired-*")
	if err != nil {
		return errors.New("cannot persist retirement seal")
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err != nil || closeErr != nil {
		return errors.New("cannot sync retirement seal")
	}
	if err := os.Rename(f.Name(), filepath.Join(c.root, "retired.json")); err != nil {
		return errors.New("cannot publish retirement seal")
	}
	syncDirectory := c.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncCanvasDirectory
	}
	if err := syncDirectory(c.root); err != nil {
		return errCanvasPublicationUncertain
	}
	return nil
}
