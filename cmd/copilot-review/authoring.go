package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/activation"
	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/web"
)

func validateAuthoringMode(draft, definition, state string, stateExplicit, definitionExplicit bool, remote string, options engineOptions) error {
	if draft == "" {
		return nil
	}
	if definitionExplicit || definition != "" || remote != "" || options.remote != nil || options.trustedEndpoint != "" {
		return errors.New("-draft-root cannot combine with -definition or remote/trusted Sessions")
	}
	if err := validateDefinitionState("frozen-authoring", state, stateExplicit); err != nil {
		return errors.New("-draft-root requires an explicit separate -state directory")
	}
	return nil
}

type managedRuntime struct{ engine *runtimeEngine }

func (r *managedRuntime) RegisterAPIRoutes(m *http.ServeMux) { r.engine.canvas.RegisterAPIRoutes(m) }
func (r *managedRuntime) Metadata() map[string]any           { return r.engine.Metadata() }
func (r *managedRuntime) Seal(next string, save func(canvas.Archive) error) error {
	return r.engine.canvas.SealForHistory(next, save)
}
func (r *managedRuntime) Close() { r.engine.Close() }

func startAuthoring(root, state, address, draftRoot string, options engineOptions) (_ *demo, err error) {
	if options.remote != nil || options.trustedEndpoint != "" || draftRoot == "" || state == "" {
		return nil, activation.ErrRefused
	}
	if err = options.validate(false); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return nil, activation.ErrRefused
	}
	for _, path := range []string{state, filepath.Join(state, "canvas")} {
		if err = canvas.CheckWritable(path); err != nil {
			return nil, err
		}
	}
	draftRoot, err = filepath.Abs(draftRoot)
	if err != nil {
		return nil, activation.ErrRefused
	}
	if actual, e := filepath.EvalSymlinks(draftRoot); e == nil {
		if actual != draftRoot {
			return nil, activation.ErrRefused
		}
	} else if _, e = os.Lstat(filepath.Join(state, "catalog.json")); e != nil {
		return nil, activation.ErrRefused
	}
	// Never adopt an operator instance into the authoring catalog.
	for _, name := range []string{"canvas", "space", "sessions", "producers"} {
		if _, e := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(e) {
			return nil, activation.ErrRefused
		}
	}
	packages, err := authoring.OpenPackages(filepath.Join(state, "authoring"))
	if err != nil {
		return nil, err
	}
	factory := activation.Factory{
		Preflight: func(instance activation.Instance, pkg authoring.Package, path string) (any, error) {
			private := filepath.Join(state, "instances", instance.ID, "runtime")
			return prepareRuntime(root, private, path, options, &pkg)
		},
		Start: func(instance activation.Instance, value any) (activation.Runtime, error) {
			prepared, ok := value.(*preparedRuntime)
			if !ok {
				return nil, activation.ErrRefused
			}
			engine, e := openRuntime(filepath.Join(state, "instances", instance.ID, "runtime"), prepared)
			if e != nil {
				return nil, e
			}
			return &managedRuntime{engine}, nil
		},
	}
	manager, err := activation.Open(state, packages, factory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			manager.Close()
		}
	}()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	origin := "http://" + listener.Addr().String()
	authoringMux := http.NewServeMux()
	send := func(w http.ResponseWriter, status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(value)
	}
	fail := func(w http.ResponseWriter, e error, instance activation.Instance) {
		status, code := 400, "ACTIVATION_REFUSED"
		if errors.Is(e, activation.ErrConflict) {
			status, code = 409, "ACTIVATION_CONFLICT"
		} else if errors.Is(e, activation.ErrUncertain) {
			status, code = 503, "ACTIVATION_PENDING"
		}
		send(w, status, map[string]any{"code": code, "error": code, "instance": instance})
	}
	authoringMux.HandleFunc("GET /api/authoring", func(w http.ResponseWriter, r *http.Request) { send(w, 200, manager.Snapshot()) })
	authoringMux.HandleFunc("POST /api/authoring/prepare", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Definition string `json:"definition"`
		}
		if decodeAuthoring(w, r, &input) != nil || input.Definition == "" {
			fail(w, activation.ErrRefused, activation.Instance{})
			return
		}
		pkg, e := manager.Prepare(draftRoot, input.Definition)
		if e != nil {
			fail(w, e, activation.Instance{})
			return
		}
		send(w, 200, map[string]any{"candidate": pkg})
	})
	authoringMux.HandleFunc("GET /api/authoring/candidate", func(w http.ResponseWriter, r *http.Request) {
		pkg, e := packages.Load(r.URL.Query().Get("id"))
		if e != nil {
			fail(w, e, activation.Instance{})
			return
		}
		send(w, 200, pkg)
	})
	authoringMux.HandleFunc("GET /api/authoring/file", func(w http.ResponseWriter, r *http.Request) {
		id, path := r.URL.Query().Get("candidate"), r.URL.Query().Get("path")
		pkg, e := packages.Load(id)
		if e != nil {
			fail(w, e, activation.Instance{})
			return
		}
		raw, e := packages.File(id, path)
		if e != nil {
			fail(w, e, activation.Instance{})
			return
		}
		encoding, data := "utf8", string(raw)
		if !utf8.Valid(raw) {
			encoding, data = "base64", base64.StdEncoding.EncodeToString(raw)
		}
		hash := ""
		for _, file := range pkg.Files {
			if file.Path == path {
				hash = file.SHA256
				break
			}
		}
		send(w, 200, map[string]any{"path": path, "sha256": hash, "encoding": encoding, "data": data})
	})
	authoringMux.HandleFunc("POST /api/authoring/activate", func(w http.ResponseWriter, r *http.Request) {
		var input activation.ActivationRequest
		if decodeAuthoring(w, r, &input) != nil {
			fail(w, activation.ErrRefused, activation.Instance{})
			return
		}
		instance, e := manager.Activate(input)
		if e != nil {
			log.Printf("local activation: %v", e)
			fail(w, e, instance)
			return
		}
		send(w, 200, map[string]any{"instance": instance})
	})
	authoringMux.HandleFunc("GET /api/authoring/instance", func(w http.ResponseWriter, r *http.Request) {
		instance, archive, e := manager.History(r.URL.Query().Get("id"))
		if e != nil {
			fail(w, e, activation.Instance{})
			return
		}
		send(w, 200, map[string]any{"instance": instance, "archive": archive})
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/demo", func(w http.ResponseWriter, r *http.Request) { send(w, 200, manager.Metadata()) })
	runtime := http.HandlerFunc(manager.ServeRuntime)
	for _, method := range []string{"GET", "POST"} {
		mux.Handle(method+" /engine/", http.StripPrefix("/engine", runtime))
		mux.Handle(method+" /api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/authoring" || strings.HasPrefix(r.URL.Path, "/api/authoring/") {
				authoringMux.ServeHTTP(w, r)
				return
			}
			runtime.ServeHTTP(w, r)
		}))
	}
	mux.Handle("GET /", canvas.AssetHandler(web.Assets))
	server := &http.Server{Handler: canvas.LocalHandler(listener.Addr().String(), mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	d := &demo{URL: origin, server: server, done: make(chan error, 1), close: manager.Close}
	go func() { d.done <- server.Serve(listener) }()
	return d, nil
}

// Strict, small objects only. Duplicate/case aliases cannot create a second
// interpretation of an activation payload across the bridge and engine.
func decodeAuthoring(w http.ResponseWriter, r *http.Request, out any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return activation.ErrRefused
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return activation.ErrRefused
	}
	fields := map[string]bool{}
	switch out.(type) {
	case *activation.ActivationRequest:
		for _, k := range []string{"candidate", "expectedCurrent", "requestKey", "confirmed", "actor"} {
			fields[k] = true
		}
	default:
		fields["definition"] = true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if token, e := dec.Token(); e != nil || token != json.Delim('{') {
		return activation.ErrRefused
	}
	seen := map[string]bool{}
	for dec.More() {
		token, e := dec.Token()
		key, ok := token.(string)
		if e != nil || !ok || seen[key] || !fields[key] {
			return activation.ErrRefused
		}
		seen[key] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return activation.ErrRefused
		}
	}
	if _, err = dec.Token(); err != nil {
		return activation.ErrRefused
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return activation.ErrRefused
	}
	if json.Unmarshal(raw, out) != nil {
		return activation.ErrRefused
	}
	return nil
}
