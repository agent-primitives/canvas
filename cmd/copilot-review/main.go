// copilot-review serves the Canvas review engine. Real providers and remote
// sessions are explicit opt-ins; the default remains deterministic Northwind.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/demostate"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/canvas/web"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
)

type demo struct {
	URL    string
	server *http.Server
	done   chan error
	close  func()
}

func (d *demo) Close() { d.server.Close(); d.close() }

func start(root, state, address string) (*demo, error) {
	return startDefinition(root, state, address, "")
}

func startDefinition(root, state, address, definitionPath string) (*demo, error) {
	return startConfigured(root, state, address, definitionPath, nil)
}

func startConfigured(root, state, address, definitionPath string, remote *remoteEngine) (*demo, error) {
	return startWithOptions(root, state, address, definitionPath, engineOptions{remote: remote})
}

func startWithOptions(root, state, address, definitionPath string, options engineOptions) (_ *demo, err error) {
	if err = options.validate(options.remote != nil); err != nil {
		return nil, err
	}
	remote := options.remote
	host, _, e := net.SplitHostPort(address)
	if e != nil || host != "127.0.0.1" {
		return nil, errors.New("demo listener must use literal 127.0.0.1:port")
	}
	selectedDefinition := definitionPath
	if selectedDefinition == "" {
		selectedDefinition = filepath.Join(root, "examples", "northwind-prd", "canvas.yaml")
	} else if !filepath.IsAbs(selectedDefinition) {
		selectedDefinition = filepath.Join(root, selectedDefinition)
	}
	definition, e := canvas.LoadManualDefinitionWithResolver(selectedDefinition, contract.NewReleaseResolver(filepath.Join(state, "suite-cache")))
	if e != nil {
		return nil, e
	}
	// Refuse before state, providers or sessions RPCs: the human credential is
	// never relabelled as the execution owner.
	if remote != nil && !remote.hasExecutionBinding() {
		return nil, errExecutionBinding
	}
	selected, e := options.configuredProvider(root, definition)
	if e != nil {
		return nil, e
	}
	release, e := demostate.Acquire(state)
	if e != nil {
		return nil, e
	}
	cleanups := []func(){release}
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	var calls atomic.Int64
	provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		var input struct {
			Mode  string `json:"mode"`
			Agent string `json:"agent"`
		}
		if len(req.Messages) == 0 || json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Text()), &input) != nil || input.Mode != "documents" {
			return api.ModelResponse{}, errors.New("demo accepts document requests only")
		}
		changes := []map[string]string{}
		switch input.Agent {
		case "writer":
			changes = append(changes, map[string]string{"document": "prd", "operation": "replace", "content": strings.Replace(definition.Seed["prd.json"], "Fictional Northwind PRD", "Proposed fictional Northwind pilot", 1)}, map[string]string{"document": "announcement", "operation": "replace", "content": "# Northwind pilot\n\nInvite three fictional reviewers to a local preview.\nNothing is sent or published.\n"})
		case "reviewer":
			changes = append(changes, map[string]string{"document": "review", "operation": "replace", "content": definition.Seed["review.json"]})
		default:
			return api.ModelResponse{}, errors.New("unsupported demo agent")
		}
		raw, e := json.Marshal(map[string]any{"changes": changes})
		if e != nil {
			return api.ModelResponse{}, e
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", string(raw))}, nil
	}
	if selected != nil {
		provider = func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
			calls.Add(1)
			return selected(ctx, req)
		}
	}
	var cl, execution *client.Client
	var sessionsAddress string
	var archives *contract.ArchiveResolver
	if remote == nil && options.trustedEndpoint == "" {
		h, e := sessionhost.Start(filepath.Join(state, "sessions"), definition.Bundles, provider)
		if e != nil {
			return nil, e
		}
		cleanups = append(cleanups, func() { h.Close() })
		sessionsAddress = h.Address()
		archives = &contract.ArchiveResolver{Directory: filepath.Join(state, "sessions", "contracts")}
		cl, e = client.Dial(sessionsAddress, client.WithProject(definition.ID))
	} else if remote != nil {
		sessionsAddress = remote.endpoint
		cl, e = remote.dial(definition.ID)
	} else {
		sessionsAddress = options.trustedEndpoint
		cl, e = options.dialTrusted(definition.ID)
	}
	if e != nil {
		return nil, e
	}
	cleanups = append(cleanups, func() { cl.Close() })
	if remote != nil {
		// Same endpoint and TLS trust, separate service credential. Registered
		// before OpenDefined so Canvas.Close runs before either connection closes.
		if execution, e = remote.dialExecution(definition.ID); e != nil {
			return nil, e
		}
		cleanups = append(cleanups, func() { execution.Close() })
	}
	space, e := sharedspace.OpenWithPolicy(filepath.Join(state, "space"), definition.Seed, definition.Policy)
	if e != nil {
		return nil, e
	}
	cleanups = append(cleanups, func() { space.Close() })
	c, e := canvas.OpenDefined(filepath.Join(state, "canvas"), space, cl, sessionsAddress, definition, contract.LocalBinder{}, archives)
	if e != nil {
		return nil, e
	}
	cleanups = append(cleanups, func() { c.Close() })
	if remote != nil {
		c.EnableAuthentication(remote.reviewPolicy)
		if e = c.ConfigureExecutionService(canvas.ExecutionService{Client: execution, Credentials: remote.executionAuth, Identity: remote.executionIdentity}); e != nil {
			return nil, e
		}
	} else if options.trustedEndpoint != "" {
		c.EnableRemoteSessions()
	}
	listener, e := net.Listen("tcp", address)
	if e != nil {
		return nil, e
	}
	origin := "http://" + listener.Addr().String()
	handler := c.Handler(listener.Addr().String(), web.Assets)
	boot := canvas.ID()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/demo" {
			handler.ServeHTTP(w, r)
			return
		}
		if r.Host != listener.Addr().String() || r.Method != "GET" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Local demo read required.", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		info := map[string]any{"mode": "deterministic-northwind", "identity": "unauthenticated (prototype)", "boot": boot, "providerCalls": calls.Load(), "realModelCalls": 0, "headlessPrefix": "/engine"}
		if options.modelProfile != "" {
			info = map[string]any{"mode": options.modelProfile + "-model", "identity": "not exercised (local unauthenticated engine)", "boot": boot, "providerInvocations": calls.Load(), "completionTokenCeiling": 4096, "headlessPrefix": "/engine"}
		} else if options.trustedEndpoint != "" {
			info = map[string]any{"mode": "trusted-loopback-sessions", "identity": "not exercised (unauthenticated trusted connection)", "boot": boot, "modelCalls": nil, "executorCalls": nil, "headlessPrefix": "/engine"}
		}
		json.NewEncoder(w).Encode(info)
	})}
	if remote != nil {
		server.Handler = authenticatedHandler(c, listener.Addr().String(), boot, remote.reviewRole, remote.auth)
	}
	d := &demo{URL: origin, server: server, done: make(chan error, 1), close: cleanup}
	go func() { d.done <- server.Serve(listener) }()
	return d, nil
}

func validateDefinitionState(definition, state string, explicit bool) error {
	if definition == "" {
		return nil
	}
	selected, err := filepath.Abs(state)
	if err != nil {
		return err
	}
	original, err := filepath.Abs(".state/copilot-northwind")
	if err != nil {
		return err
	}
	if !explicit || selected == original {
		return errors.New("-definition requires an explicit separate -state directory")
	}
	return nil
}

func main() {
	flags := flag.NewFlagSet("copilot-review", flag.ExitOnError)
	root := flags.String("root", ".", "Canvas repository root")
	state := flags.String("state", ".state/copilot-northwind", "separate private demo state")
	address := flags.String("listen", "127.0.0.1:18878", "literal loopback HTTP listener")
	definition := flags.String("definition", "", "opt-in definition; requires explicit separate -state")
	sessionsConfig := flags.String("sessions-config", "", "human sessions TLS/token-source configuration; requires separate -state and a verified service binding (not supplied by CLI)")
	trusted := flags.String("sessions-trusted-loopback", "", "explicit unauthenticated 127.0.0.1:port sessions; identity not exercised; requires separate -state")
	profile := flags.String("model-profile", "", "explicit embedded hosted or offline real provider; requires separate -state")
	base := flags.String("model-base", "", "offline Ollama origin override; requires -model-profile offline")
	key := flags.String("key-file", "", "hosted API key file override; never an inline key")
	flags.Parse(os.Args[1:])
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	options := engineOptions{trustedEndpoint: *trusted, modelProfile: *profile, modelBase: *base, keyFile: *key}
	if err := options.validate(*sessionsConfig != ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stateExplicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "state" {
			stateExplicit = true
		}
	})
	if err := validateDefinitionState(*definition, *state, stateExplicit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := validateRemoteState(*sessionsConfig, *state, stateExplicit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := validateProducerState(options, *state, stateExplicit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var remote *remoteEngine
	if *sessionsConfig != "" {
		config, err := readRemoteConfig(*sessionsConfig)
		if err == nil {
			remote, err = openRemote(ctx, config)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	options.remote = remote
	d, err := startWithOptions(*root, *state, *address, *definition, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer d.Close()
	switch {
	case remote != nil:
		fmt.Println("Authenticated sessions review engine:", d.URL)
	case options.trustedEndpoint != "":
		fmt.Println("Trusted-loopback sessions engine (identity not exercised):", d.URL)
	case options.modelProfile != "":
		fmt.Println("Real-model review engine (4096 completion-token ceiling):", d.URL)
	default:
		fmt.Println("Fictional deterministic Northwind engine:", d.URL)
	}
	select {
	case <-ctx.Done():
	case err := <-d.done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "demo HTTP server stopped")
			os.Exit(1)
		}
	}
}
