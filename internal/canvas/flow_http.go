package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/suite/core"
)

// Polling omits exact historical inputs and private command/reservation journals.
// These are presentation copies, never authority to dispatch or make a decision.
type flowPanelRun struct {
	ID          string                  `json:"id"`
	Flow        string                  `json:"flow"`
	RequestKey  string                  `json:"requestKey"`
	Definition  string                  `json:"definition"`
	StartBase   string                  `json:"startBase"`
	Role        string                  `json:"role"`
	AgentRuns   int                     `json:"agentRuns"`
	Attempts    int                     `json:"attempts"`
	Position    int                     `json:"position"`
	Step        string                  `json:"step"`
	Attempt     string                  `json:"attempt,omitempty"`
	Proposal    string                  `json:"proposal,omitempty"`
	State       string                  `json:"state"`
	Reason      string                  `json:"reason,omitempty"`
	Accepted    map[string]string       `json:"accepted"`
	Action      string                  `json:"action,omitempty"`
	RequestedBy sharedspace.ReviewActor `json:"requestedBy,omitzero"`
	Inputs      json.RawMessage         `json:"inputs,omitempty"`
	Feedback    []FlowFeedback          `json:"feedback,omitempty"`
}

type flowPanelDefinition struct {
	ID         string          `json:"id"`
	Definition FlowDefinition  `json:"definition"`
	Schema     json.RawMessage `json:"schema"`
}
type flowPanelDocument struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}
type flowPanelState struct {
	Definition        *DefinitionInfo              `json:"definition"`
	Head              string                       `json:"head"`
	OperatorAgentRuns int                          `json:"operatorAgentRuns"`
	Busy              bool                         `json:"busy"`
	Documents         map[string]flowPanelDocument `json:"documents"`
	Flows             []flowPanelDefinition        `json:"flows"`
	Runs              []flowPanelRun               `json:"runs"`
}

func presentFlow(f FlowRun, detail bool) flowPanelRun {
	r := flowPanelRun{ID: f.ID, Flow: f.Flow, RequestKey: f.RequestKey, Definition: f.Definition, StartBase: f.StartBase, Role: f.Role, AgentRuns: f.AgentRuns, Attempts: f.Attempts, Position: f.Position, Step: f.Step, Attempt: f.Attempt, Proposal: f.Proposal, State: f.State, Reason: f.Reason, Accepted: f.Accepted, Action: f.Action, RequestedBy: f.RequestedBy}
	if detail {
		r.Inputs, r.Feedback = f.Inputs, f.Feedback
	}
	return r
}

func (c *Canvas) flowPanelSnapshot() (flowPanelState, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	p := flowPanelState{Definition: &DefinitionInfo{ID: c.definition.ID, Title: c.definition.Title, Digest: c.definition.Digest}, OperatorAgentRuns: c.flowOptions.MaxAgentRuns, Busy: c.flowAdmissionBusyLocked(), Documents: map[string]flowPanelDocument{}, Flows: []flowPanelDefinition{}, Runs: []flowPanelRun{}}
	var err error
	if p.Head, err = c.space.Head(); err != nil {
		return p, err
	}
	for id, d := range c.definition.documents {
		p.Documents[id] = flowPanelDocument{Path: d.path, Title: c.definition.Documents[id].Title}
	}
	ids := []string{}
	for id := range c.definition.flows {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		f := c.definition.flows[id]
		schema, err := json.Marshal(f.inputs.Schema())
		if err != nil {
			return p, err
		}
		p.Flows = append(p.Flows, flowPanelDefinition{ID: id, Definition: cloneFlow(f.definition), Schema: schema})
	}
	for _, f := range c.data.Flows {
		p.Runs = append(p.Runs, presentFlow(cloneFlowRun(f.FlowRun), false))
	}
	return p, nil
}

// The opt-in constructor is necessary but not hosted browser qualification.
// Remote/authenticated runtimes retain their Go API without this local UI port.
func (c *Canvas) registerFlowRoutes(mux *http.ServeMux) {
	handle := func(pattern string, next http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if c.flowOptions == nil || c.authenticated || c.remoteSessions {
				http.NotFound(w, r)
				return
			}
			next(w, r)
		})
	}
	handle("GET /api/flows", func(w http.ResponseWriter, r *http.Request) {
		p, err := c.flowPanelSnapshot()
		if err != nil {
			failHTTP(w, internalError(err))
			return
		}
		sendJSON(w, 200, p)
	})
	handle("GET /api/flow", func(w http.ResponseWriter, r *http.Request) {
		f, err := c.GetFlow(r.Context(), r.URL.Query().Get("run"))
		if err != nil {
			failHTTP(w, err)
			return
		}
		sendJSON(w, 200, presentFlow(f, true))
	})
	// Exact registrations, not a generic model-callable method dispatcher.
	handle("POST /api/flow/start", func(w http.ResponseWriter, r *http.Request) {
		var q FlowStart
		if !decodeFlowAction(w, r, 72<<10, &q) {
			return
		}
		f, err := c.StartFlow(r.Context(), q)
		sendFlowResult(w, f, err, http.StatusAccepted)
	})
	handle("POST /api/flow/changes", func(w http.ResponseWriter, r *http.Request) {
		var q FlowChanges
		if !decodeFlowAction(w, r, 8192, &q) {
			return
		}
		f, err := c.RequestFlowChanges(r.Context(), q)
		sendFlowResult(w, f, err, http.StatusAccepted)
	})
	handle("POST /api/flow/retry", func(w http.ResponseWriter, r *http.Request) {
		var q FlowRetry
		if !decodeFlowAction(w, r, 8192, &q) {
			return
		}
		f, err := c.RetryFlow(r.Context(), q)
		sendFlowResult(w, f, err, http.StatusAccepted)
	})
	handle("POST /api/flow/cancel", func(w http.ResponseWriter, r *http.Request) {
		var q FlowCancel
		if !decodeFlowAction(w, r, 8192, &q) {
			return
		}
		f, err := c.CancelFlow(r.Context(), q)
		sendFlowResult(w, f, err, http.StatusOK)
	})
	handle("POST /api/flow/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var q FlowMaintenance
		if !decodeFlowAction(w, r, 8192, &q) {
			return
		}
		f, err := c.ReconcileFlow(r.Context(), q)
		sendFlowResult(w, f, err, http.StatusOK)
	})
	handle("POST /api/flow/advance", func(w http.ResponseWriter, r *http.Request) {
		var q FlowAdvance
		if !decodeFlowAction(w, r, 8192, &q) {
			return
		}
		f, err := c.AdvanceFlow(r.Context(), q)
		sendFlowResult(w, f, err, http.StatusAccepted)
	})
	handle("POST /api/flow/verify", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Run string `json:"run"`
		}
		if !decodeFlowAction(w, r, 8192, &q) {
			return
		}
		p, err := c.VerifyFlow(r.Context(), q.Run)
		if err != nil {
			failHTTP(w, err)
			return
		}
		sendJSON(w, http.StatusOK, p)
	})
}

func decodeFlowAction(w http.ResponseWriter, r *http.Request, limit int64, q any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var object map[string]json.RawMessage
	if err != nil || core.Unmarshal(raw, &object) != nil || object == nil {
		sendJSON(w, 400, map[string]string{"error": "A bounded JSON flow request is required.", "code": "FLOW_INVALID"})
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(q) != nil || d.Decode(&extra) != io.EOF {
		sendJSON(w, 400, map[string]string{"error": "Invalid flow request fields.", "code": "FLOW_INVALID"})
		return false
	}
	return true
}

func sendFlowResult(w http.ResponseWriter, f FlowRun, err error, success int) {
	if err == nil {
		sendJSON(w, success, presentFlow(f, false))
		return
	}
	status, code, message := http.StatusInternalServerError, "FLOW_UNCERTAIN", "The flow outcome could not be confirmed. Inspect its current record; nothing was automatically retried."
	switch {
	case errors.Is(err, ErrFlowCancelPending) || f.State == "cancelling":
		status, code, message = 409, "FLOW_CANCEL_PENDING", "Cancellation is durably fenced, but native settlement is still pending. Inspect and reconcile explicitly."
	case errors.Is(err, ErrFlowBusy), errors.Is(err, ErrFlowConflict), errors.Is(err, sharedspace.ErrStale), errors.Is(err, sharedspace.ErrDecided):
		status, code, message = 409, "FLOW_CONFLICT", "The flow command conflicts with its current state, authority, inputs or budget. Refresh and inspect before another command."
	case errors.Is(err, errDefinition):
		status, code, message = 400, "FLOW_INVALID", "Inputs do not match the frozen flow schema or supported bounds."
	case errors.Is(err, errNotFound):
		status, code, message = 404, "FLOW_NOT_FOUND", "The selected flow was not found."
	default:
		log.Printf("canvas flow: %v", err)
	}
	out := map[string]any{"error": message, "code": code}
	if f.ID != "" {
		out["run"] = presentFlow(f, false)
	}
	sendJSON(w, status, out)
}
