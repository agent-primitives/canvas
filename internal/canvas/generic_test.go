package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
)

func TestDefinedPeoplePreserveExistingPersonaIDs(t *testing.T) {
	d, err := LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, person := range d.People {
		if person.ID == "GM" {
			found = true
		}
		if !validRole(person.ID) {
			t.Fatalf("declared persona cannot be attributed through existing API: %q", person.ID)
		}
	}
	if !found {
		t.Fatal("generic definition lost the existing GM attribution identity")
	}
}

func definedFixture(t *testing.T, override func(map[string]any) string) (*Canvas, *CompiledDefinition, *atomic.Int32) {
	return definedFixtureAt(t, filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"), override)
}

func definedFixtureAt(t *testing.T, filename string, override func(map[string]any) string, fail ...*atomic.Bool) (*Canvas, *CompiledDefinition, *atomic.Int32) {
	t.Helper()
	d, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int32)
	c, cleanup := nativeProviderFixture(t, d, func(_ context.Context, request api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		if len(fail) > 0 && fail[0].Swap(false) {
			return api.ModelResponse{}, errors.New("synthetic provider interruption")
		}
		var input map[string]any
		if len(request.Messages) == 0 || json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Text()), &input) != nil {
			return api.ModelResponse{}, errors.New("typed input missing")
		}
		output := ""
		if override != nil {
			output = override(input)
		}
		if output == "" {
			if input["mode"] == "question" {
				output = `{"answer":"This explains the recorded fictional documents, not an evaluation result."}`
			} else {
				id, content := "prd", strings.Replace(d.Seed["prd.json"], "Fictional Northwind PRD", "Proposed fictional pilot PRD", 1)
				if input["agent"] == "reviewer" {
					id, content = "review", d.Seed["review.json"]
				}
				raw, _ := json.Marshal(map[string]any{"changes": []map[string]string{{"document": id, "operation": "replace", "content": content}}})
				output = string(raw)
			}
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", output)}, nil
	})
	t.Cleanup(cleanup)
	return c, d, calls
}

func TestDefinedProposalIsStaleAfterReadInputChanges(t *testing.T) {
	for _, filename := range []string{"feedback/initial.json", "feedback/added-after-run.json"} {
		t.Run(filename, func(t *testing.T) {
			c, _, _ := definedFixture(t, nil)
			id, err := c.Manual("writer", "", "Review the accepted feedback.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			run := completed(t, c, id)
			if run.Status != "proposed" {
				t.Fatalf("fixture: %+v", run)
			}
			if _, err := c.space.Ingest(filename, `{"team":"Northwind","feedback":"Fictional new evidence changes the premise."}`, "GM"); err != nil {
				t.Fatal(err)
			}
			state, err := c.State()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, p := range state.Proposals {
				if p.ID == run.Proposal {
					found = true
					if p.Status != "stale" {
						t.Fatal("review UI still offers a proposal based on superseded read inputs")
					}
				}
			}
			if !found {
				t.Fatal("proposal missing")
			}
			if _, err := c.Review(run.Proposal, "accept", "GM"); !errors.Is(err, sharedspace.ErrStale) {
				t.Fatalf("accepted outdated input premise: %v", err)
			}
		})
	}
}

func TestDefinedWriterReviewAndRecordedIdentity(t *testing.T) {
	var reviewerEvidence map[string]any
	c, d, calls := definedFixture(t, func(input map[string]any) string {
		if input["agent"] == "reviewer" {
			reviewerEvidence, _ = input["evidence"].(map[string]any)
		}
		return ""
	})
	base, err := c.space.Head()
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Manual("writer", "", "Draft a fictional pilot proposal.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	writer := completed(t, c, id)
	if writer.Status != "proposed" || writer.Trigger != "draft-prd" || writer.Base != base || writer.Suite == nil || writer.Suite.Digest != d.Bundles[1].Suite.Digest || writer.Evaluation != "not verified" {
		t.Fatalf("writer result/provenance: %+v", writer)
	}
	accepted, err := c.space.Read("", "prd.json")
	if err != nil || accepted != d.Seed["prd.json"] {
		t.Fatal("model bypassed independent review")
	}
	if _, err := c.Review(writer.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	acceptedBase, _ := c.space.Head()
	reviewerID, err := c.Manual("reviewer", "", "Review the accepted draft, keeping unsupported claims explicit.", "coach")
	if err != nil {
		t.Fatal(err)
	}
	reviewer := completed(t, c, reviewerID)
	if reviewer.Status != "proposed" || reviewer.Base != acceptedBase || reviewer.Suite.Member != "reviewer" {
		t.Fatalf("reviewer result: %+v", reviewer)
	}
	if text, ok := reviewerEvidence["prd.json"].(string); !ok || !strings.Contains(text, "Proposed fictional pilot PRD") {
		t.Fatal("reviewer did not read the accepted writer revision")
	}
	if text, ok := reviewerEvidence["feedback/initial.json"].(string); !ok || text != c.definition.Seed["feedback/initial.json"] {
		t.Fatal("reviewer lacks accepted original feedback for its independent claims check")
	}
	before := calls.Load()
	proof, err := c.Verify(context.Background(), id)
	if err != nil || !proof.ChainValid || !proof.Reconstructed || proof.ModelCalls != 0 || calls.Load() != before {
		t.Fatalf("zero-call integrity proof: %+v %v", proof, err)
	}
	recorded, _ := c.GetRun(id)
	if recorded.Evaluation != "not verified" {
		t.Fatal("Replay integrity was relabelled evaluation evidence")
	}
	proposals, _ := c.space.Proposals()
	if proposals[0].Attribution.Suite == nil || proposals[0].Attribution.Suite.Digest != writer.Suite.Digest {
		t.Fatal("proposal lost historical suite identity")
	}
	doc, err := c.Document("prd.json", writer.Proposal)
	if err != nil || doc["baseCommit"] != base || doc["base"] != d.Seed["prd.json"] {
		t.Fatalf("original-base review lost: %v", err)
	}
}

func TestDefinedInputTriggerIgnoresSeedAndDeduplicates(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ := c.State()
	if len(state.Runs) != 0 || calls.Load() != 0 {
		t.Fatal("startup replayed seeded input")
	}
	if err := c.WriteInput("feedback/new.json", `{"team":"Northwind","feedback":"Fictional new observation; outcomes remain unmeasured."}`, "GM"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ = c.State()
	if len(state.Runs) != 1 {
		t.Fatalf("expected one queued input run, got %d", len(state.Runs))
	}
	run := completed(t, c, state.Runs[0].ID)
	if run.Status != "proposed" || run.Trigger != "new-feedback" {
		t.Fatalf("input run: %+v", run)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ = c.State()
	if len(state.Runs) != 1 || calls.Load() != 1 {
		t.Fatal("polling dispatched an input twice")
	}
	if err := c.WriteInput("prd.json", `{}`, "GM"); err == nil {
		t.Fatal("input API overwrote output")
	}
	if err := c.WriteInput("feedback/invalid.json", `{"feedback":7}`, "GM"); err == nil {
		t.Fatal("invalid input reached the watcher")
	}
}

func TestDefinedInvalidChangeSetPublishesNothing(t *testing.T) {
	c, _, _ := definedFixture(t, func(map[string]any) string {
		return `{"changes":[{"document":"announcement","operation":"replace","content":"Simulated only"},{"document":"review","operation":"replace","content":"{}"}]}`
	})
	base, _ := c.space.Head()
	id, err := c.Manual("writer", "", "Propose a fictional update.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	state, _ := c.State()
	if run.Status != "failed" || run.Proposal != "" || len(state.Proposals) != 0 || state.Head != base {
		t.Fatalf("partially published invalid set: %+v", run)
	}
}

func TestDefinedStateFreezesPresentationAndBindsReopen(t *testing.T) {
	c, d, _ := definedFixture(t, nil)
	d.Title = "Changed by caller"
	doc := d.Documents["prd"]
	doc.Path = "escape.md"
	d.Documents["prd"] = doc
	d.Agents["writer"].Propose[0] = "review"
	state, err := c.State()
	if err != nil || state.Definition.Title == d.Title || state.Definition.DefaultDocument != "prd.json" {
		t.Fatalf("mutable definition changed active state: %v", err)
	}
	c.Close()
	fresh, err := LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fresh.Digest = strings.Repeat("a", 64)
	if changed, err := OpenDefined(c.root, c.space, c.client, c.address, fresh, contract.LocalBinder{}); err == nil {
		changed.Close()
		t.Fatal("new definition reinterpreted saved state")
	}
	fresh, _ = LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, fresh, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ = reopened.State()
	if len(state.Runs) != 0 {
		t.Fatal("restart dispatched seeded or uncertain work")
	}
}

func TestDefinedQuestionAndConfirmedModelResume(t *testing.T) {
	fail := new(atomic.Bool)
	fail.Store(true)
	c, d, calls := definedFixtureAt(t, filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"), nil, fail)
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	interrupted := completed(t, c, id)
	if interrupted.Status != "failed" || calls.Load() != 1 {
		t.Fatalf("interruption fixture: %+v", interrupted)
	}
	inspection := c.InspectResume(context.Background(), id, "")
	if !inspection.Available {
		t.Fatalf("modern native model-only pending request unavailable: %s", inspection.Reason)
	}
	if err := c.ConfirmResume(context.Background(), id, "", "GM", confirmationFor(inspection)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		r, _ := c.GetRun(id)
		if r.ResumeStatus == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := c.GetRun(id)
	if run.Status != "proposed" || run.ResumeStatus != "succeeded" || calls.Load() != 2 || run.Evaluation != "not verified" {
		t.Fatalf("native resume: %+v", run)
	}
	head, _ := c.space.Head()
	question, err := c.Ask(id, "What remains unmeasured?", "coach")
	if err != nil {
		t.Fatal(err)
	}
	answer := awaitQuestion(t, c, question)
	if answer.Status != "answered" || answer.Session != run.Session || answer.Agent != "writer" {
		t.Fatalf("native read-only question: %+v", answer)
	}
	if current, _ := c.space.Head(); current != head {
		t.Fatal("question changed accepted documents")
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, d, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state, err := reopened.State(); err != nil || len(state.Conversation) != 1 || state.Conversation[0].Status != "answered" {
		t.Fatalf("native question history failed reopen: %v", err)
	}
}

func TestDefinedSnapshotBoundRefusesBeforeModel(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	for i := 0; i < 6; i++ {
		raw, _ := json.Marshal(map[string]string{"team": "Northwind", "feedback": strings.Repeat("x", 60000)})
		if _, err := c.space.Ingest(fmt.Sprintf("feedback/large-%d.json", i), string(raw), "GM"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Manual("writer", "", "Read the accepted feedback.", "GM"); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("oversized context refusal: %v", err)
	}
	server := newTestServer(t, c)
	code, message := server.do("POST", "/api/run", `{"agent":"writer","request":"Read accepted feedback.","role":"GM"}`)
	if code != 400 || !strings.Contains(message, "context exceeds") || !strings.Contains(message, "No model") {
		t.Fatalf("capacity guidance missing: %d %s", code, message)
	}
	state, _ := c.State()
	if calls.Load() != 0 || len(state.Runs) != 0 {
		t.Fatal("oversized context created model work")
	}
}

func TestDefinedSeenInputRestoresCheckpointWithoutResending(t *testing.T) {
	for _, seeded := range []bool{true, false} {
		t.Run(fmt.Sprint("seeded=", seeded), func(t *testing.T) {
			c, _, calls := definedFixture(t, nil)
			filename, a := "feedback/initial.json", c.definition.Seed["feedback/initial.json"]
			if !seeded {
				filename = "feedback/restored.json"
				if err := c.WriteInput(filename, a, "GM"); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.ScanInputs(); err != nil {
				t.Fatal(err)
			}
			state, _ := c.State()
			for _, run := range state.Runs {
				completed(t, c, run.ID)
			}
			baseline := len(state.Runs)
			b := `{"team":"Northwind","feedback":"Fictional revised feedback."}`
			if err := c.WriteInput(filename, b, "GM"); err != nil {
				t.Fatal(err)
			}
			if err := c.ScanInputs(); err != nil {
				t.Fatal(err)
			}
			state, _ = c.State()
			if len(state.Runs) != baseline+1 {
				t.Fatal("new input was not recorded")
			}
			for _, run := range state.Runs {
				completed(t, c, run.ID)
			}
			if err := c.WriteInput(filename, a, "GM"); err != nil {
				t.Fatal(err)
			}
			if err := c.ScanInputs(); err != nil {
				t.Fatal(err)
			}
			current, err := c.space.Read("", filename)
			if err != nil || current != a {
				t.Fatal("seen A bytes left B in the accepted checkpoint")
			}
			if calls.Load() != int32(baseline+1) {
				t.Fatal("restoration resent recorded model work")
			}
			// Restore B while down; restart must checkpoint it, not repeat its old intent.
			definition := c.definition
			c.Close()
			if err := c.space.WriteInput(filename, b); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenDefined(c.root, c.space, c.client, c.address, definition, contract.LocalBinder{})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.ScanInputs(); err != nil {
				t.Fatal(err)
			}
			current, err = c.space.Read("", filename)
			if err != nil || current != b || calls.Load() != int32(baseline+1) {
				t.Fatal("restart did not checkpoint seen B without model resend")
			}
			id, err := reopened.Manual("writer", "", "Review current accepted input.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			completed(t, reopened, id)
			reopened.mu.Lock()
			recordedInput := reopened.findLocked(id).Input
			reopened.mu.Unlock()
			var input struct {
				Evidence map[string]any `json:"evidence"`
			}
			if json.Unmarshal([]byte(recordedInput), &input) != nil || input.Evidence[filename] != b {
				t.Fatal("manual snapshot ignored restored checkpoint")
			}
		})
	}
}

func TestDefinedCapacityRefusalIsTerminalAndInputReplacementRecovers(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	for i := 0; i < 6; i++ {
		raw, _ := json.Marshal(map[string]string{"team": "Northwind", "feedback": strings.Repeat("x", 60000)})
		if _, err := c.space.Ingest(fmt.Sprintf("feedback/large-%d.json", i), string(raw), "GM"); err != nil {
			t.Fatal(err)
		}
	}
	filename, content := "feedback/extra.json", `{"team":"Northwind","feedback":"Fictional observation."}`
	if err := c.WriteInput(filename, content, "GM"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	key := inputKey("new-feedback", filename, []byte(content))
	c.mu.Lock()
	refused := c.data.Reports[key]
	c.mu.Unlock()
	if refused != "refused-input-bound" || !c.reportRetry[key].IsZero() || calls.Load() != 0 {
		t.Fatal("known input-capacity refusal was left in a retry loop")
	}
	before, _ := c.space.History()
	for i := 0; i < 6; i++ {
		filename := fmt.Sprintf("feedback/large-%d.json", i)
		if err := c.WriteInput(filename, content, "GM"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.space.Ingest(filename, content, "GM"); err != nil {
			t.Fatal(err)
		}
	}
	id, err := c.Manual("writer", "", "Review the shortened, accepted fictional inputs.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	if run := completed(t, c, id); run.Status != "proposed" {
		t.Fatalf("replacement did not recover capacity: %+v", run)
	}
	after, _ := c.space.History()
	if len(after) <= len(before) {
		t.Fatal("recovery deleted historical input evidence")
	}
}

func TestDefinedStoredLabelsCannotClaimEvaluation(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.mu.Lock()
	c.findLocked(id).Evaluation = "verified"
	c.mu.Unlock()
	run, _ := c.GetRun(id)
	state, err := c.State()
	if err != nil || run.Evaluation != "not verified" || state.Runs[0].Evaluation != "not verified" {
		t.Fatal("a stored label was treated as a verifier result")
	}
}

func TestDefinedEmptyInputDirectoryAndUnrelatedDefinition(t *testing.T) {
	filename := genericFixture(t)
	genericConfigEdit(t, filename, "    feedback/initial.json: seeds/feedback-initial.json\n", "")
	c, _, calls := definedFixtureAt(t, filename, nil)
	if err := c.ScanInputs(); err != nil || calls.Load() != 0 {
		t.Fatalf("empty input collection must be idle: %v", err)
	}
	filename = genericFixture(t)
	config := `apiVersion: canvas.local/v1alpha2
kind: Canvas
metadata: {name: laboratory, title: Laboratory notes}
spec:
  documents:
    notes: {path: notes.md, title: Session notes}
  agents:
    editor:
      name: Notes editor
      suite:
        directory: suites/writer
        artifact: layouts/writer
        digest: sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375
        member: writer
      read: [notes]
      propose: [notes]
  seed:
    notes.md: seeds/announcement.md
  triggers:
    - {name: edit-notes, type: manual, agent: editor}
  people:
    - {id: GM, name: Morgan, title: Notes reviewer}
`
	if err := os.WriteFile(filename, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	other, d, _ := definedFixtureAt(t, filename, func(map[string]any) string { return changeEnvelope("notes", "# Reviewed fictional notes") })
	id, err := other.Manual("editor", "", "Update the notes.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, other, id)
	if run.Status != "proposed" || run.Suite.Member != "writer" || run.Agent != "editor" || other.project() != "laboratory" {
		t.Fatalf("unrelated definition: %+v", run)
	}
	if _, err := other.Review(run.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	if got, _ := other.space.Read("", "notes.md"); got != "# Reviewed fictional notes" {
		t.Fatal("generic target was not accepted")
	}
	state, err := other.State()
	if err != nil || state.Definition.ID != d.ID || len(state.Documents) != 1 || state.Documents[0] != "notes.md" {
		t.Fatalf("hardcoded domain leaked: %+v", state)
	}
}
