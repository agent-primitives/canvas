package canvas

import (
	"regexp"
	"time"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
)

// Activity contains only producer milestones and scoped input labels, never session payloads.
type Activity struct {
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Document string    `json:"document,omitempty"`
}

func addActivity(a *attempt, kind string, at time.Time) {
	for _, activity := range a.Activity {
		if activity.Kind == kind {
			return
		}
	}
	a.Activity = append(a.Activity, Activity{Kind: kind, At: at.UTC()})
}

var modelIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)

// Use only metadata actually returned by Exec, including partial failed turns.
// Event timestamps are authoritative when present; otherwise At is the time
// this producer observed the returned records, not an invented model-call time.
func observeTurn(a *attempt, turn *client.TurnResult, successful bool) {
	if turn == nil {
		return
	}
	observed := time.Now().UTC()
	for _, record := range turn.Records {
		event := record.GetEvent()
		at := observed
		if ts := event.GetTs(); ts != nil && ts.IsValid() {
			at = ts.AsTime()
		}
		switch event.GetKind() {
		case v1.EventKind_EVENT_MODEL_CALL:
			if event.GetModel() == nil {
				continue
			}
			addActivity(a, "model-requested", at)
			model := event.GetModel().GetModel()
			if a.Model == "" && modelIdentifier.MatchString(model) && safeText(model, 200) {
				a.Model = model
			}
		case v1.EventKind_EVENT_OUTPUT:
			if successful {
				addActivity(a, "output-received", at)
			}
		}
	}
}

func cloneRun(r Run) Run {
	r.Suite = r.Suite.Clone()
	r.Activity = append([]Activity(nil), r.Activity...)
	if r.BoardChange != nil {
		change := *r.BoardChange
		r.BoardChange = &change
	}
	if r.Proof != nil {
		proof := *r.Proof
		proof.RequestedModels = append([]string(nil), proof.RequestedModels...)
		r.Proof = &proof
	}
	return r
}
