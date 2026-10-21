package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// APIHandler serves the bounded generic JSON surface without browser assets or
// legacy actions. Each handler owns one local CSRF cookie.
func (c *Canvas) APIHandler(host string) http.Handler {
	mux := http.NewServeMux()
	c.RegisterAPIRoutes(mux)
	return LocalHandler(host, mux)
}

// RegisterAPIRoutes registers routes only, without security middleware. Commands
// may add their own bounded reads and AssetHandler fallback to the same mux,
// then wrap it once in LocalHandler. Do not compose separately guarded handlers.
func (c *Canvas) RegisterAPIRoutes(mux *http.ServeMux) {
	c.registerAPIRoutes(mux, true)
}

// apiState excludes browser preferences, room chat, football data and local
// filesystem paths. Declared people remain attribution metadata, not authority.
type apiState struct {
	Actions       []DeclaredAction         `json:"actions,omitempty"`
	Announcements []AnnouncementSummary    `json:"announcements,omitempty"`
	Definition    *DefinitionInfo          `json:"definition"`
	Agents        []Agent                  `json:"agents"`
	People        []Person                 `json:"people"`
	Documents     []string                 `json:"documents"`
	Head          string                   `json:"head"`
	Runs          []Run                    `json:"runs"`
	Proposals     []sharedspace.Proposal   `json:"proposals"`
	History       []sharedspace.Checkpoint `json:"history"`
	Triggers      []Trigger                `json:"triggers"`
	Errors        []string                 `json:"errors"`
}

func projectAPIState(state State) apiState {
	return apiState{
		Actions: state.Actions, Announcements: state.Announcements,
		Definition: state.Definition, Agents: state.Agents, People: state.People,
		Documents: state.Documents, Head: state.Head, Runs: state.Runs,
		Proposals: state.Proposals, History: state.History,
		Triggers: state.Triggers, Errors: state.Errors,
	}
}

func (c *Canvas) registerAPIRoutes(mux *http.ServeMux, generic bool) {
	c.registerFlowRoutes(mux)
	c.registerEditRoutes(mux)
	handle := func(pattern string, next http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if generic && c.definition == nil {
				http.NotFound(w, r)
				return
			}
			next(w, r)
		})
	}
	handle("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		state, err := c.State()
		if err != nil {
			failHTTP(w, err)
			return
		}
		if !generic {
			sendJSON(w, 200, state)
			return
		}
		sendJSON(w, 200, projectAPIState(state))
	})
	handle("GET /api/document", func(w http.ResponseWriter, r *http.Request) {
		doc, err := c.Document(r.URL.Query().Get("path"), r.URL.Query().Get("proposal"))
		if err != nil {
			failHTTP(w, err)
			return
		}
		sendJSON(w, 200, doc)
	})
	handle("GET /api/forks", func(w http.ResponseWriter, r *http.Request) {
		view, err := c.Forks(r.URL.Query().Get("run"))
		if err != nil {
			failHTTP(w, err)
			return
		}
		sendJSON(w, 200, view)
	})
	for _, path := range []string{"/api/run", "/api/review", "/api/verify"} {
		handle("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			if generic {
				c.apiActionHTTP(w, r)
				return
			}
			c.actionHTTP(w, r)
		})
	}
	announcements := http.NewServeMux()
	c.announcementRoutes(announcements, sendJSON)
	handle("GET /api/announcement", announcements.ServeHTTP)
	handle("POST /api/announcement/", announcements.ServeHTTP)
}

var errInvalidAPIAction = errors.New("invalid generic action")

// Keep the existing action implementation, but admit only the fields belonging
// to each headless route. Legacy action fields cannot enter through this surface.
func (c *Canvas) apiActionHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var object map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &object) != nil || object == nil {
		failHTTP(w, errInvalidAPIAction)
		return
	}
	allowed := map[string]bool{}
	switch r.URL.Path {
	case "/api/run":
		allowed = map[string]bool{"agent": true, "request": true, "role": true}
	case "/api/review":
		allowed = map[string]bool{"proposal": true, "action": true, "role": true}
	case "/api/verify":
		allowed = map[string]bool{"run": true}
	}
	for key := range object {
		if !allowed[key] {
			failHTTP(w, errInvalidAPIAction)
			return
		}
	}
	copy := r.Clone(r.Context())
	copy.Body = io.NopCloser(bytes.NewReader(raw))
	c.actionHTTP(w, copy)
}
