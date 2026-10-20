package canvas

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// Handler serves the existing browser routes and explicit assets behind one
// local boundary. The browser's legacy state and actions remain unchanged.
func (c *Canvas) Handler(host string, assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	headless := http.NewServeMux()
	c.RegisterAPIRoutes(headless)
	mux.Handle("GET /engine/", http.StripPrefix("/engine", headless))
	mux.Handle("POST /engine/", http.StripPrefix("/engine", headless))
	c.registerAPIRoutes(mux, false)
	mux.HandleFunc("GET /api/resume", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, http.StatusOK, c.InspectResume(r.Context(), r.URL.Query().Get("run"), r.URL.Query().Get("message")))
	})
	mux.HandleFunc("POST /api/", c.actionHTTP)
	mux.Handle("GET /", AssetHandler(assets))
	return LocalHandler(host, mux)
}

func sendJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Clients get fixed messages only. Internal failures are logged locally.
func failHTTP(w http.ResponseWriter, err error) {
	if errors.Is(err, errDocumentReviewDenied) {
		sendJSON(w, 403, map[string]string{"error": errDocumentReviewDenied.Error(), "code": "PERMISSION_DENIED"})
		return
	}
	if sendAuthError(w, err) {
		return
	}
	if errors.Is(err, errRemoteDiagnostics) {
		sendJSON(w, 409, map[string]string{"error": errRemoteDiagnostics.Error()})
		return
	}
	status := http.StatusBadRequest
	message := "The action could not be completed. Check the selected run, report or proposal."
	switch {
	case errors.Is(err, errSnapshotBound):
		message = "Accumulated context exceeds the supported model input bound. Replace large inputs with shorter valid content and inspect any new file-triggered runs before requesting another. No model was invoked for this request."
	case errors.Is(err, errResumeConflict):
		status = http.StatusConflict
		message = errResumeConflict.Error()
	case errors.Is(err, ErrQuestionsUnsupported):
		status = http.StatusConflict
		message = "Questions are unsupported for this recorded session. Start a new assessment explicitly."
	case errors.Is(err, ErrQuestionBusy):
		status = http.StatusConflict
		message = "Wait for the current session turn before asking or verifying."
	case errors.Is(err, sharedspace.ErrStale):
		status = http.StatusConflict
		message = "Proposal is stale. Regenerate on latest."
	case errors.Is(err, sharedspace.ErrDecided):
		status = http.StatusConflict
		message = "This proposal already has a review decision."
	case errors.Is(err, sharedspace.ErrCommitted):
		status = http.StatusInternalServerError
		message = "The change was saved, but the canvas could not finish updating. Refresh to see the latest state."
		log.Printf("canvas: %v", err)
	case errors.Is(err, errNotFound), errors.Is(err, sharedspace.ErrProposalNotFound):
		status = http.StatusNotFound
		message = "The selected run or proposal was not found."
	case errors.Is(err, errInternal):
		status = http.StatusInternalServerError
		message = "The canvas could not read or save its local state. Check the server log."
		log.Printf("canvas: %v", err)
	}
	sendJSON(w, status, map[string]string{"error": message})
}

func (c *Canvas) actionHTTP(w http.ResponseWriter, r *http.Request) {
	send, fail := sendJSON, failHTTP
	if r.URL.Path == "/api/injury" || r.URL.Path == "/api/scenarios" {
		send(w, http.StatusNotFound, map[string]string{"error": "Unknown canvas action."})
		return
	}
	var in struct {
		Path         string             `json:"path"`
		Content      string             `json:"content"`
		Agent        string             `json:"agent"`
		Prospect     string             `json:"prospect"`
		Request      string             `json:"request"`
		Role         string             `json:"role"`
		Run          string             `json:"run"`
		Message      string             `json:"message"`
		Confirmation ResumeConfirmation `json:"confirmation"`
		Proposal     string             `json:"proposal"`
		Action       string             `json:"action"`
	}
	limit := int64(8192)
	if r.URL.Path == "/api/input" {
		limit = 512 << 10
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		fail(w, errors.New("invalid action"))
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, errors.New("trailing action"))
		return
	}
	switch r.URL.Path {
	case "/api/resume":
		if err := c.ConfirmResume(r.Context(), in.Run, in.Message, in.Role, in.Confirmation); err != nil {
			fail(w, err)
			return
		}
		out := map[string]string{"run": in.Run}
		if in.Message != "" {
			out["message"] = in.Message
		}
		send(w, http.StatusAccepted, out)
	case "/api/comment":
		id, err := c.Comment(in.Run, in.Request, in.Role)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, http.StatusOK, map[string]string{"message": id})
	case "/api/ask":
		id, err := c.AskContext(r.Context(), in.Run, in.Request, in.Role)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, http.StatusAccepted, map[string]string{"message": id})
	case "/api/role":
		if err := c.SetRole(in.Role); err != nil {
			fail(w, err)
			return
		}
		send(w, 200, map[string]bool{"ok": true})
	case "/api/run":
		id, err := c.ManualContext(r.Context(), in.Agent, in.Prospect, in.Request, in.Role)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, 202, map[string]string{"run": id})
	case "/api/review":
		p, err := c.ReviewContext(r.Context(), in.Proposal, in.Action, in.Role)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, 200, p)
	case "/api/regenerate":
		id, err := c.RegenerateContext(r.Context(), in.Proposal, in.Role)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, 202, map[string]string{"run": id})
	case "/api/verify":
		proof, err := c.Verify(r.Context(), in.Run)
		if errors.Is(err, errInternal) {
			// The proof ran, but the canvas could not save it.
			log.Printf("canvas: %v", err)
			send(w, http.StatusInternalServerError, map[string]string{"error": "Reconstruction succeeded, but the canvas could not save the result. Check the server log."})
			return
		}
		if err != nil {
			if sendAuthError(w, err) {
				return
			}
			if errors.Is(err, errRemoteDiagnostics) {
				fail(w, err)
				return
			}
			if errors.Is(err, ErrQuestionBusy) {
				fail(w, err)
				return
			}
			send(w, 409, map[string]string{"error": "Reconstruction evidence missing: diagnostic unavailable or session incomplete."})
			return
		}
		send(w, 200, proof)
	case "/api/input":
		if err := c.WriteInput(in.Path, in.Content, in.Role); err != nil {
			if errors.Is(err, sharedspace.ErrCommitted) {
				send(w, http.StatusInternalServerError, map[string]string{"error": "The source file was published but its durability is uncertain. Its watcher may still start a chargeable run. Inspect the latest state before retrying."})
				return
			}
			fail(w, err)
			return
		}
		send(w, http.StatusAccepted, map[string]bool{"saved": true})
	default:
		send(w, 404, map[string]string{"error": "Unknown canvas action."})
	}
}

// AssetHandler serves only the browser's explicit public files. Wrap the composed
// endpoint in LocalHandler; this route handler does not mint its own cookie.
func AssetHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
			return
		}
		if assets == nil {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Path
		switch path {
		case "/":
			path = "index.html"
		case "/app.js":
			path = "app.js"
		case "/style.css":
			path = "style.css"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch path {
		case "app.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case "style.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		_, _ = w.Write(data)
	})
}

// LocalHandler applies the browser's Host, Origin, cookie and response-header
// boundary once to a composed endpoint. The cookie is local CSRF protection,
// not authentication or a multi-user identity. Do not nest guarded handlers.
func LocalHandler(host string, next http.Handler) http.Handler {
	return browserGuard(host, "http://"+host, false, next)
}

// TLSLoopbackHandler guards an explicitly configured HTTPS loopback browser
// origin, independently of the server's listen port. The caller serves TLS;
// forwarded headers never supply trust. This is CSRF protection, not identity.
func TLSLoopbackHandler(origin string, next http.Handler) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() != "127.0.0.1" {
		return nil, errors.New("canonical HTTPS loopback origin required")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || origin != "https://127.0.0.1:"+strconv.Itoa(port) {
		return nil, errors.New("canonical HTTPS loopback origin required")
	}
	return browserGuard(u.Host, origin, true, next), nil
}

func browserGuard(host, origin string, secure bool, next http.Handler) http.Handler {
	token := ID() + ID()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Local canvas host required.", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			cookie, err := r.Cookie("canvas-local")
			if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 || r.Header.Get("Origin") != origin {
				http.Error(w, "Same-origin canvas action required.", http.StatusForbidden)
				return
			}
		} else {
			http.SetCookie(w, &http.Cookie{Name: "canvas-local", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
		}
		next.ServeHTTP(w, r)
	})
}
