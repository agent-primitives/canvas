package canvas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Synthetic compatibility data only: this does not claim that a historical
// workspace can be activated by OpenDefined or that lost fork RPCs can recover.
func TestHistoricalLedgerUnmarshalSaveAndCloneWithoutLoss(t *testing.T) {
	const historical = `{
 "attempts":[{
  "id":"historical-child","created":"2026-01-02T03:04:05Z","order":3,
  "agent":"injury","mode":"scenario","prospect":"tavon-reyes","scenario":"Stay at #14",
  "comparison":"comparison-id","previousAttempt":"previous-id","parentSession":"parent-session","forkSeq":17,
  "requestSummary":"Recorded request","actor":"coach","session":"child-session","model":"archived-model",
  "authoredBundleDigest":"authored-digest","executionBundleDigest":"execution-digest",
  "base":"accepted-base","status":"uncertain","summary":"Recorded uncertainty","error":"Lost response",
  "seconds":1.5,"started":"2026-01-02T03:04:05Z","completed":"2026-01-02T03:04:06Z","proposal":"proposal-id",
  "boardChange":{"prospect":"tavon-reyes","oldRank":4,"newRank":22,"oldTier":1,"newTier":3,"reason":"Recorded recommendation"},
  "report":{"id":"report-id","prospect":"tavon-reyes","injury":"Synthetic historical injury","recoveryWeeks":30,"source":"fictional report","actor":"coach"},
  "activity":[{"kind":"input-read","at":"2026-01-02T03:04:05Z","document":"board.json"}],
  "proof":{"chainValid":true,"reconstructed":true,"outputsMatch":true,"liveUnchanged":true,"modelCalls":0,"records":12,"head":12,"requestedModels":["archived-model"]},
  "suite":{"digest":"suite-digest","contentDigest":"content-digest","graphDigest":"graph-digest","member":"archived-member","memberDigest":"member-digest","compositions":[{"runtimeProfile":"native","agent":"archived-member","platform":{"os":"linux","architecture":"amd64"},"buildProfile":"default","digest":"composition-digest"}]},
  "input":"{\"mode\":\"scenario\"}","request":"Recorded request","sources":{"board.json":"accepted-base"},
  "bundleDigest":"archived-digest","harness":"archived-harness","expectedSeq":17,
  "questionsSupported":false,"sessionVersion":2,"resumeStatus":"uncertain","recoveryError":"No automatic dispatch"
 }],
 "reports":{"report-key":"historical-child"},"role":"coach",
 "fanouts":{"comparison-id":{"id":"comparison-id","parent":"parent-session","forkSeq":17,"attempts":["historical-child","lost-child"],"status":"uncertain"}}
 }`
	var saved ledger
	if err := json.Unmarshal([]byte(historical), &saved); err != nil {
		t.Fatal(err)
	}
	c := &Canvas{root: t.TempDir(), data: saved}
	if err := c.saveLocked(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(historical), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reports", "role", "fanouts"} {
		if !reflect.DeepEqual(got[key], want[key]) {
			t.Fatalf("historical %s changed: %v", key, got[key])
		}
	}
	originalFields := want["attempts"].([]any)[0].(map[string]any)
	storedFields := got["attempts"].([]any)[0].(map[string]any)
	for key, value := range originalFields {
		if !reflect.DeepEqual(value, storedFields[key]) {
			t.Errorf("historical field %s lost or changed: got %v want %v", key, storedFields[key], value)
		}
	}
	var roundtrip ledger
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	original := roundtrip.Attempts[0].Run
	clone := cloneRun(original)
	if !reflect.DeepEqual(original, clone) {
		t.Fatal("clone lost historical fields")
	}
	clone.BoardChange.Reason = "mutated"
	clone.Activity[0].Document = "mutated"
	clone.Proof.RequestedModels[0] = "mutated"
	clone.Suite.Member = "mutated"
	clone.Suite.Compositions[0].Agent = "mutated"
	if original.Suite.Compositions[0].Agent != "archived-member" {
		t.Fatal("clone aliases historical composition selections")
	}
	if original.BoardChange.Reason != "Recorded recommendation" || original.Activity[0].Document != "board.json" || original.Proof.RequestedModels[0] != "archived-model" || original.Suite.Member != "archived-member" {
		t.Fatal("clone aliases historical data")
	}
	// Missing child identity remains missing; data save is not correlation.
	if len(roundtrip.Attempts) != 1 || roundtrip.Fanouts["comparison-id"].Status != "uncertain" {
		t.Fatal("compatibility save invented fork identity")
	}
}
