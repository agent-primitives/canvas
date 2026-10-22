package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

type editPanelDocument struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Title string `json:"title"`
	Agent string `json:"agent"`
}

func (c *Canvas) registerEditRoutes(mux *http.ServeMux) {
	handle := func(pattern string, next http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if c.definition == nil || c.authenticated || c.remoteSessions {
				http.NotFound(w, r)
				return
			}
			next(w, r)
		})
	}
	handle("GET /api/edits", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		edits := make([]EditRun, 0, len(c.data.Edits))
		for _, e := range c.data.Edits {
			edits = append(edits, cloneEdit(e.EditRun))
		}
		slot := c.data.EditSlot
		c.mu.Unlock()
		slices.SortFunc(edits, func(a, b EditRun) int { return bytes.Compare([]byte(a.ID), []byte(b.ID)) })
		docs := []editPanelDocument{}
		for id, d := range c.definition.documents {
			if !d.input && d.editAgent != "" {
				docs = append(docs, editPanelDocument{ID: id, Path: d.path, Title: id, Agent: d.editAgent})
			}
		}
		slices.SortFunc(docs, func(a, b editPanelDocument) int { return bytes.Compare([]byte(a.ID), []byte(b.ID)) })
		sendJSON(w, 200, map[string]any{"edits": edits, "slot": slot, "documents": docs})
	})
	handle("GET /api/edit", func(w http.ResponseWriter, r *http.Request) {
		e, err := c.GetEdit(r.Context(), r.URL.Query().Get("id"))
		sendEditResult(w, e, err, 200)
	})
	handle("GET /api/edit/context", c.editContextHTTP)
	handle("POST /api/edit/request", func(w http.ResponseWriter, r *http.Request) {
		var q EditRequest
		if !decodeEditAction(w, r, &q) {
			return
		}
		e, err := c.RequestEdit(r.Context(), q)
		sendEditResult(w, e, err, 202)
	})
	handle("POST /api/edit/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var q EditMaintenance
		if !decodeEditAction(w, r, &q) {
			return
		}
		e, err := c.ReconcileEdit(r.Context(), q)
		sendEditResult(w, e, c.editMaintenanceError(q, "reconcile", err), 200)
	})
	handle("POST /api/edit/abandon", func(w http.ResponseWriter, r *http.Request) {
		var q EditMaintenance
		if !decodeEditAction(w, r, &q) {
			return
		}
		e, err := c.AbandonEdit(r.Context(), q)
		sendEditResult(w, e, c.editMaintenanceError(q, "abandon", err), 200)
	})
	handle("POST /api/edit/resume", func(w http.ResponseWriter, r *http.Request) {
		var q EditMaintenance
		if !decodeEditAction(w, r, &q) {
			return
		}
		f, err := c.ResumeAfterEdit(r.Context(), q)
		if err != nil {
			sendEditResult(w, EditRun{}, c.editMaintenanceError(q, "resume", err), 200)
			return
		}
		sendJSON(w, 200, presentFlow(f, false))
	})
	handle("POST /api/edit/rerun", func(w http.ResponseWriter, r *http.Request) {
		var q EditMaintenance
		if !decodeEditAction(w, r, &q) {
			return
		}
		f, err := c.RerunAfterEdit(r.Context(), q)
		if err != nil {
			sendEditResult(w, EditRun{}, c.editMaintenanceError(q, "rerun", err), 200)
			return
		}
		sendJSON(w, 200, presentFlow(f, false))
	})
}

func (c *Canvas) editContextHTTP(w http.ResponseWriter, r *http.Request) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	id, flow := r.URL.Query().Get("document"), r.URL.Query().Get("flowRun")
	doc, ok := c.definition.documents[id]
	if !ok || doc.input || doc.editAgent == "" {
		sendEditResult(w, EditRun{}, ErrEditConflict, 200)
		return
	}
	if flow != "" {
		c.mu.Lock()
		f := c.flowLocked(flow)
		valid := f != nil && !terminalFlow(f.State)
		c.mu.Unlock()
		if !valid {
			sendEditResult(w, EditRun{}, ErrEditConflict, 200)
			return
		}
	}
	head, err := c.space.Head()
	if err != nil {
		sendEditResult(w, EditRun{}, internalError(err), 200)
		return
	}
	content, err := c.space.Read(head, doc.path)
	if err != nil {
		sendEditResult(w, EditRun{}, internalError(err), 200)
		return
	}
	sendJSON(w, 200, map[string]string{"document": id, "path": doc.path, "agent": doc.editAgent, "baseRevision": head, "baseDigest": editScopeHash(content), "content": content, "flowRun": flow})
}

// Check the token tree without float conversion: neither duplicate keys nor
// encoding/json's case-insensitive struct matching may widen a selector.
func editHTTPJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrEditScope
			}
			seen[s] = true
			if err := editHTTPJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := editHTTPJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return ErrEditScope
	}
	_, err = d.Token()
	return err
}
func editFields(raw []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	for k := range obj {
		if !slices.Contains(allowed, k) {
			return nil, false
		}
	}
	return obj, true
}
func decodeEditAction(w http.ResponseWriter, r *http.Request, q any) bool {
	invalid := func() bool {
		sendJSON(w, 400, map[string]string{"error": "A bounded, exact JSON edit request is required.", "code": "EDIT_INVALID"})
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil || !utf8.Valid(raw) {
		return invalid()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if editHTTPJSONValue(d) != nil {
		return invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid()
	}
	switch q.(type) {
	case *EditRequest:
		obj, ok := editFields(raw, "flowRun", "document", "baseRevision", "baseDigest", "section", "instruction", "requestKey", "role", "commentRef")
		if !ok {
			return invalid()
		}
		if section, present := obj["section"]; present {
			if bytes.Equal(bytes.TrimSpace(section), []byte("null")) {
				return invalid()
			}
			sec, ok := editFields(section, "kind", "pointer", "headingPath", "occurrence")
			if !ok {
				return invalid()
			}
			var kind string
			if json.Unmarshal(sec["kind"], &kind) != nil {
				return invalid()
			}
			_, pointer := sec["pointer"]
			_, headings := sec["headingPath"]
			_, occurrence := sec["occurrence"]
			if kind == "json-pointer" {
				if !pointer || headings || occurrence || bytes.Equal(bytes.TrimSpace(sec["pointer"]), []byte("null")) {
					return invalid()
				}
			} else if kind == "markdown-heading" {
				if pointer || !headings || !occurrence {
					return invalid()
				}
			} else {
				return invalid()
			}
		}
	case *EditMaintenance:
		if _, ok := editFields(raw, "edit", "role", "requestKey", "expectedHead", "confirmed"); !ok {
			return invalid()
		}
	default:
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(q) != nil {
		return invalid()
	}
	return true
}

type editRefusedError struct{ error }

func (e *editRefusedError) Unwrap() error { return e.error }

// Only absence of this exact command's durable intent permits discarding its
// client transport key. A failed/uncertain save retains its in-memory claim too.
func (c *Canvas) editMaintenanceError(q EditMaintenance, kind string, err error) error {
	if err == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.editLocked(q.Edit)
	if r != nil && ((kind == "reconcile" && r.ReconcileKeys[q.RequestKey] == commandDigest(q, rRequestActor(c, r))) || (kind != "reconcile" && r.Commands[kind] == commandDigest(q, rRequestActor(c, r)))) {
		return err
	}
	return &editRefusedError{err}
}
func sendEditResult(w http.ResponseWriter, e EditRun, err error, success int) {
	if err == nil {
		sendJSON(w, success, e)
		return
	}
	var refused *editRefusedError
	status, code, message := 500, "EDIT_UNCERTAIN", "The edit outcome could not be confirmed. Inspect its record; nothing was automatically retried."
	switch {
	case errors.As(err, &refused):
		status, code, message = 409, "EDIT_REFUSED", "No command was reserved. Inspect refreshed evidence and confirm a new request."
	case errors.Is(err, ErrEditConflict), errors.Is(err, ErrEditScope), errors.Is(err, ErrFlowConflict), errors.Is(err, ErrFlowBusy), errors.Is(err, sharedspace.ErrStale), errors.Is(err, sharedspace.ErrDecided):
		status, code, message = 409, "EDIT_CONFLICT", "The edit conflicts with the current state or frozen scope. Refresh and inspect before acting."
	case errors.Is(err, errNotFound):
		status, code, message = 404, "EDIT_NOT_FOUND", "The selected edit was not found."
	}
	out := map[string]any{"error": message, "code": code}
	if e.ID != "" {
		out["edit"] = e
	}
	sendJSON(w, status, out)
}
