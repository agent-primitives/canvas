package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/demostate"
	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/canvas/web"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/controller"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		// Provider errors are already sanitized; key contents and request or
		// response bodies never reach these messages.
		fmt.Fprintln(os.Stderr, "canvas:", err)
		os.Exit(commandExitCode(err))
	}
}

// privateStateDir returns the state directory with symlinks in its existing
// ancestors resolved, so system links such as /home -> var/home work. The state
// directory itself must not be a symlink: it is created and chmodded here.
func privateStateDir(path string) (string, error) {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err == nil && !info.IsDir() {
		return "", fmt.Errorf("state path %s must be a real directory, not a file or symlink", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot inspect state path %s: %w", path, err)
	}
	missing := []string{filepath.Base(path)}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(parent) == parent {
			return "", fmt.Errorf("cannot resolve state path %s: %w", path, err)
		}
		missing = append([]string{filepath.Base(parent)}, missing...)
	}
}

func serve(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 && (args[0] == "init" || args[0] == "validate" || args[0] == "bind") {
		return authoringCommand(args, output)
	}
	flags := flag.NewFlagSet("canvas", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // parse errors are returned and printed by main
	root := flags.String("root", ".", "repository root")
	definitionPath := flags.String("definition", "examples/northwind-prd/canvas.yaml", "generic canvas definition relative to root; local embedded runtime only")
	validate := flags.Bool("validate", false, "validate --definition offline and exit without starting Canvas; released suites require local layouts")
	fixture := flags.Bool("fixture", false, "explicit deterministic provider for generated templates; no real inference")
	state := flags.String("state", ".state/northwind", "private ignored state directory")
	listen := flags.String("listen", "127.0.0.1:8765", "loopback HTTP listener")
	remote := flags.String("sessions-address", "", "separately provisioned Sessions gRPC address")
	hostOnly := flags.Bool("sessions-only", false, "legacy embedded-host mode; prefer sessions-host")
	profileName := flags.String("model-profile", "", "hosted or offline; defaults to model.config.local.json, then model.config.json (offline if both absent)")
	base := flags.String("model-base", "http://127.0.0.1:11434", "offline Ollama endpoint; hosted uses hosted.endpoint from the selected model config")
	keyFile := flags.String("key-file", "", "hosted API key file; overrides hosted.keyFile (default "+modelconfig.DefaultKeyFile+")")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: canvas [flags]")
		flags.SetOutput(output)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", flags.Args())
	}
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if *fixture {
		if !explicit["definition"] || *definitionPath == "" || !explicit["state"] || *state == "" {
			return errors.New("fixture requires --definition and explicit --state")
		}
		for _, name := range []string{"model-profile", "model-base", "key-file", "sessions-address", "sessions-only", "validate"} {
			if explicit[name] {
				return errors.New("fixture cannot combine with provider, remote, host-only or validation flags")
			}
		}
	}
	if *validate {
		if !explicit["definition"] || *definitionPath == "" {
			return errors.New("--validate requires --definition")
		}
		filename := *definitionPath
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(*root, filename)
		}
		// A nil resolver permits local layouts only: no registry or runtime cache.
		if _, err := canvas.LoadGenericDefinition(filename); err != nil {
			return fmt.Errorf("offline definition validation (released suites require suite.layout): %w", err)
		}
		fmt.Fprintln(output, "Definition valid (offline); not activated. Validation is not authorization or runtime qualification.")
		return nil
	}
	if *remote != "" {
		return errors.New("generic definitions are local-only; --sessions-address is unsupported; Substrate templates are a separate follow-up")
	}
	if *definitionPath == "" {
		return errors.New("generic definition required; --definition cannot be empty")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("HTTP listener must bind 127.0.0.1, got %q", *listen)
	}
	if *root == "" || *state == "" {
		return errors.New("root and state required")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("invalid repository root: %w", err)
	}
	private, err := filepath.Abs(*state)
	if err != nil {
		return fmt.Errorf("invalid state path: %w", err)
	}
	if private, err = privateStateDir(private); err != nil {
		return err
	}
	modeBinding, err := checkRuntimeMode(private, *fixture)
	if err != nil {
		return err
	}
	if err := demostate.PreflightGeneric(private); err != nil {
		return err
	}
	var configured *canvas.CompiledDefinition
	var provider controller.ModelFunc
	filename := *definitionPath
	if !filepath.IsAbs(filename) {
		filename = filepath.Join(abs, filename)
	}
	if *fixture {
		configured, err = canvas.LoadManualDefinitionWithResolver(filename, nil)
		if err == nil {
			provider, err = authoring.NewFixtureProvider(filename, configured)
		}
	} else {
		configured, err = canvas.LoadManualDefinitionWithResolver(filename, contract.NewReleaseResolver(filepath.Join(private, "suite-cache")))
	}
	if err != nil {
		return err
	}
	bundles := configured.Bundles
	if !*hostOnly {
		// Refuse known binding mismatches before securing state or creating a lock.
		// Re-resolve and recheck under the lifecycle lock below.
		selected, err := demostate.Resolve(private)
		if err != nil {
			return err
		}
		if err := canvas.PreflightDefinition(filepath.Join(selected, "canvas"), configured); err != nil {
			return err
		}
		if err := sharedspace.PreflightPolicy(filepath.Join(selected, "space"), configured.Policy); err != nil {
			return err
		}
	}
	// Released resolution validates the private cache/state root before any chmod.
	if err := os.MkdirAll(private, 0700); err != nil {
		return fmt.Errorf("cannot create state directory: %w", err)
	}
	if err := os.Chmod(private, 0700); err != nil {
		return fmt.Errorf("cannot set permissions on state directory: %w", err)
	}
	var profile modelconfig.Profile
	if !*fixture {
		profile, err = modelconfig.Load(abs, *profileName)
		if err != nil {
			return err
		}
		if *keyFile != "" {
			if profile, err = profile.WithKeyFile(*keyFile); err != nil {
				return err
			}
		}
		bundles, err = profile.Contracts(bundles)
		if err != nil {
			return err
		}
		configured.Bundles = bundles
	}
	producerRoot := private
	if !*hostOnly {
		release, err := demostate.Acquire(private)
		if err != nil {
			return err
		}
		defer release()
		producerRoot, err = demostate.Resolve(private)
		if err != nil {
			return err
		}
	}
	if !*hostOnly {
		// Read-only binding checks precede all workspace/provider/host effects.
		// The real opens recheck the same bindings under the producer lock.
		if err := canvas.PreflightDefinition(filepath.Join(producerRoot, "canvas"), configured); err != nil {
			return err
		}
		if err := sharedspace.PreflightPolicy(filepath.Join(producerRoot, "space"), configured.Policy); err != nil {
			return err
		}
	}
	if !*fixture {
		provider, err = sessionhost.ConfiguredProvider(profile, filepath.Join(abs, "model.lock.json"), *base, bundles)
		if err != nil {
			return err
		}
	}
	if err := modeBinding.Bind(); err != nil {
		return err
	}
	h, err := sessionhost.Start(filepath.Join(private, "sessions"), bundles, provider)
	if err != nil {
		return err
	}
	defer h.Close()
	address := h.Address()
	if *hostOnly {
		fmt.Fprintln(output, "Sessions gRPC:", address)
		<-ctx.Done()
		return nil
	}
	project := configured.ID
	cl, err := client.Dial(address, client.WithProject(project))
	if err != nil {
		return err
	}
	defer cl.Close()
	ping, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = cl.ListSessions(ping, project)
	pingCancel()
	if err != nil {
		return fmt.Errorf("Sessions gRPC unavailable at %s; is sessions-host running?", address)
	}
	space, err := sharedspace.OpenWithPolicy(filepath.Join(producerRoot, "space"), configured.Seed, configured.Policy)
	if err != nil {
		return err
	}
	defer space.Close()
	c, err := canvas.OpenDefined(filepath.Join(producerRoot, "canvas"), space, cl, address, configured, contract.LocalBinder{}, &contract.ArchiveResolver{Directory: filepath.Join(private, "sessions", "contracts")})
	if err != nil {
		return err
	}
	defer c.Close()
	c.Watch()
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("cannot open HTTP listener: %w", err)
	}
	defer lis.Close()
	// WriteTimeout stays well above the longest request handler (fork, 30s).
	engineHandler := c.Handler(lis.Addr().String(), web.Assets)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := "configured"
		if *fixture {
			mode = "deterministic-fixture"
		}
		w.Header().Set("X-Canvas-Provider", mode)
		engineHandler.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(lis) }()
	fmt.Fprintln(output, "Canvas:", "http://"+lis.Addr().String())
	if *fixture {
		fmt.Fprintln(output, "Provider: deterministic fixture; no real inference, not runtime qualification.")
	}
	fmt.Fprintln(output, "Sessions gRPC:", address)
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server stopped: %w", err)
		}
		return nil
	}
}
