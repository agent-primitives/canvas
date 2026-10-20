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
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/web"
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
	if options.flowAgentRuns > 0 && definitionPath == "" {
		return nil, errors.New("-flow-agent-runs requires an explicit -definition and separate -state")
	}
	host, _, e := net.SplitHostPort(address)
	if e != nil || host != "127.0.0.1" {
		return nil, errors.New("demo listener must use literal 127.0.0.1:port")
	}
	prepared, e := prepareRuntime(root, state, definitionPath, options, nil)
	if e != nil {
		return nil, e
	}
	engine, e := openRuntime(state, prepared)
	if e != nil {
		return nil, e
	}
	c := engine.canvas
	listener, e := net.Listen("tcp", address)
	if e != nil {
		engine.Close()
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
		info := engine.Metadata()
		info["boot"] = boot
		json.NewEncoder(w).Encode(info)
	})}
	if remote != nil {
		server.Handler = authenticatedHandler(c, listener.Addr().String(), boot, remote.reviewRole, remote.auth)
	}
	d := &demo{URL: origin, server: server, done: make(chan error, 1), close: engine.Close}
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
	draftRoot := flags.String("draft-root", "", "opt into inactive local authoring under this confined draft directory; requires separate -state")
	flowRuns := flags.Int("flow-agent-runs", 0, "opt into local declared flows with this positive operator attempt ceiling; requires -definition and separate -state")
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
	options := engineOptions{trustedEndpoint: *trusted, modelProfile: *profile, modelBase: *base, keyFile: *key, flowAgentRuns: *flowRuns}
	if err := options.validate(*sessionsConfig != ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stateExplicit, definitionExplicit := false, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "state" {
			stateExplicit = true
		}
		if f.Name == "definition" {
			definitionExplicit = true
		}
	})
	if err := validateAuthoringMode(*draftRoot, *definition, *state, stateExplicit, definitionExplicit, *sessionsConfig, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	var d *demo
	var err error
	if *draftRoot != "" {
		d, err = startAuthoring(*root, *state, *address, *draftRoot, options)
	} else {
		d, err = startWithOptions(*root, *state, *address, *definition, options)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer d.Close()
	switch {
	case *draftRoot != "":
		fmt.Println("Local frozen-package authoring engine (unverified attribution):", d.URL)
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
