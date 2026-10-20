package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	agentsuite "github.com/agent-primitives/suite"
)

type announcementRequest struct {
	ID           string                   `json:"id"`
	Action       string                   `json:"action"`
	Proposal     string                   `json:"proposal"`
	Role         string                   `json:"role"`
	Approved     *bool                    `json:"approved"`
	Confirmation AnnouncementConfirmation `json:"confirmation"`
}

func (c *Canvas) announcementRoutes(mux *http.ServeMux, send func(http.ResponseWriter, int, any)) {
	mux.HandleFunc("GET /api/announcement", func(w http.ResponseWriter, r *http.Request) {
		v, err := c.InspectAnnouncement(r.Context(), r.URL.Query().Get("id"))
		if sendAuthError(w, err) {
			return
		}
		if err != nil {
			send(w, 409, map[string]string{"error": errAnnouncement.Error()})
			return
		}
		send(w, 200, v)
	})
	mux.HandleFunc("POST /api/announcement/", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		var object map[string]json.RawMessage
		if err != nil || agentsuite.Unmarshal(raw, &object) != nil || object == nil {
			send(w, 400, map[string]string{"error": "Invalid local announcement action."})
			return
		}
		allowed := map[string]bool{"id": true, "action": true, "proposal": true, "role": true, "approved": true, "confirmation": true}
		for key := range object {
			if !allowed[key] {
				send(w, 400, map[string]string{"error": "Invalid local announcement action."})
				return
			}
		}
		if conf, ok := object["confirmation"]; ok {
			var fields map[string]json.RawMessage
			allowed := map[string]bool{"confirmed": true, "session": true, "executionID": true, "toolCallID": true, "requestSeq": true, "head": true, "revision": true, "digest": true, "currentRevision": true, "newer": true, "unavailable": true}
			if json.Unmarshal(conf, &fields) != nil || fields == nil {
				send(w, 400, map[string]string{"error": "Invalid snapshot confirmation."})
				return
			}
			for key := range fields {
				if !allowed[key] {
					send(w, 400, map[string]string{"error": "Invalid snapshot confirmation."})
					return
				}
			}
		}
		var in announcementRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			send(w, 400, map[string]string{"error": "Invalid local announcement action."})
			return
		}
		switch r.URL.Path {
		case "/api/announcement/verify":
			if c.remoteSessions {
				send(w, 409, map[string]string{"error": errRemoteDiagnostics.Error()})
				return
			}
			view, e := c.InspectAnnouncement(r.Context(), in.ID)
			if e != nil || !view.Concluded || view.Status != "simulated" {
				send(w, 409, map[string]string{"error": "A completed simulated announcement is required."})
				return
			}
			proof, e := sessionclient.Verify(r.Context(), c.address, view.Session)
			if e != nil || proof.Head != view.Confirmation.Head {
				send(w, 409, map[string]string{"error": "Reconstruction unavailable or snapshot changed. Inspect again."})
				return
			}
			send(w, 200, proof)
			return
		case "/api/announcement/request":
			id, e := c.RequestAnnouncement(r.Context(), in.Action, in.Proposal, in.Role)
			if sendAuthError(w, e) {
				return
			}
			if e != nil {
				message := errAnnouncement.Error()
				for _, known := range []error{errAnnouncementAccepted, errAnnouncementLimit, errAnnouncementHost} {
					if errors.Is(e, known) {
						message = known.Error()
						break
					}
				}
				send(w, 409, map[string]string{"error": message, "id": id})
				return
			}
			send(w, 200, map[string]string{"id": id})
			return
		case "/api/announcement/decision":
			if in.Approved == nil {
				send(w, 400, map[string]string{"error": "An explicit approval or denial is required."})
				return
			}
			err = c.DecideAnnouncement(r.Context(), in.ID, in.Role, *in.Approved, in.Confirmation)
		case "/api/announcement/continue":
			err = c.ContinueAnnouncement(r.Context(), in.ID, in.Role, in.Confirmation)
		case "/api/announcement/recover":
			err = c.RecoverAnnouncement(r.Context(), in.ID, in.Role, in.Confirmation)
		case "/api/announcement/abandon":
			if !in.Confirmation.Confirmed {
				err = errAnnouncement
			} else {
				err = c.AbandonAnnouncement(r.Context(), in.ID, in.Role, in.Confirmation)
			}
		default:
			send(w, 404, map[string]string{"error": "Unknown local announcement action."})
			return
		}
		if sendAuthError(w, err) {
			return
		}
		if err != nil {
			send(w, 409, map[string]string{"error": errAnnouncement.Error()})
			return
		}
		send(w, 200, map[string]bool{"recorded": true})
	})
}
