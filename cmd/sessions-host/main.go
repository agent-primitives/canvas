// sessions-host runs the Sessions service: agent contracts, the SQLite journal,
// model calls and replay. Canvas talks to it over local gRPC.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/demostate"
	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/substrateruntime"
)

// Opt-in integration builds must qualify suspend retention before enabling it.
// Ordinary builds keep retained actors disabled.
var retainedActorsBuild = "false"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		// Provider errors are already sanitized; key contents and request or
		// response bodies never reach these messages.
		fmt.Fprintln(os.Stderr, "sessions-host:", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("sessions-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // parse errors are returned and printed by main
	root := flags.String("root", ".", "repository root")
	definitionPath := flags.String("definition", "examples/northwind-prd/canvas.yaml", "generic definition relative to root; host-only local runtime support")
	state := flags.String("state", ".state/northwind-host", "private ignored state directory; host owns its sessions subdirectory")
	listen := flags.String("listen", "127.0.0.1:8766", "Sessions listener; generic definitions require loopback")
	runtime := flags.String("runtime", "local", "runtime: local; substrate is unsupported for generic definitions")
	retainActors := flags.Bool("retain-suspended-actors", false, "release workers while retaining actors; requires substrate and the Sessions suspend-retention fix")
	control := flags.String("control-address", "", "explicit Substrate Control gRPC address")
	serverName := flags.String("control-server-name", "", "verified Control TLS server name")
	caFile := flags.String("control-ca-file", "", "projected Control CA bundle file")
	tokenFile := flags.String("control-token-file", "", "rotating projected Control token file")
	namespace := flags.String("template-namespace", "canvas", "ActorTemplate atespace (legacy flag name)")
	profileName := flags.String("model-profile", "", "hosted or offline; defaults to model.config.local.json, then model.config.json (offline if both absent)")
	base := flags.String("model-base", "http://127.0.0.1:11434", "offline Ollama endpoint; hosted uses hosted.endpoint from the selected model config")
	keyFile := flags.String("key-file", "", "hosted API key file; overrides hosted.keyFile (default "+modelconfig.DefaultKeyFile+")")
	checkModel := flags.Bool("check-model", false, "check selected model endpoint metadata without generation or starting Sessions")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: sessions-host [flags]")
		flags.SetOutput(output)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" || (!*checkModel && *state == "") {
		return errors.New("root and state required; no positional arguments")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("invalid repository root: %w", err)
	}
	if !*checkModel {
		if *definitionPath == "" && *runtime != "substrate" {
			return errors.New("generic definition required; --definition cannot be empty")
		}
		if *runtime == "substrate" {
			return errors.New("generic definitions are local-only; Substrate template/archive delivery is a separate follow-up")
		}
		if *retainActors && *runtime != "substrate" {
			return errors.New("retained actors require substrate runtime")
		}
		if *retainActors && retainedActorsBuild != "true" {
			return errors.New("retaining actors requires a suspend-retention Sessions build")
		}
		if *runtime == "substrate" && retainedActorsBuild == "true" && !*retainActors {
			return errors.New("integration build requires retained actors for substrate runtime")
		}
		if (*runtime != "local" && *runtime != "substrate") || (*runtime == "local" && (*control != "" || *serverName != "" || *caFile != "" || *tokenFile != "")) || (*runtime == "substrate" && (*control == "" || *serverName == "" || *caFile == "" || *tokenFile == "" || *namespace == "")) {
			return errors.New("invalid runtime configuration: explicit verified Control required for substrate only")
		}
		listenHost, port, err := net.SplitHostPort(*listen)
		n, portErr := strconv.Atoi(port)
		if err != nil || (listenHost != "127.0.0.1" && !(*runtime == "substrate" && listenHost == "0.0.0.0")) || portErr != nil || n < 0 || n > 65535 {
			return fmt.Errorf("Sessions listener must bind 127.0.0.1 with a numeric port, got %q", *listen)
		}
	}
	profile, err := modelconfig.Load(abs, *profileName)
	if err != nil {
		return err
	}
	if *keyFile != "" {
		if profile, err = profile.WithKeyFile(*keyFile); err != nil {
			return err
		}
	}
	if *checkModel {
		report, err := sessionhost.CheckModel(ctx, profile, *base)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, report)
		return err
	}
	private, err := filepath.Abs(*state)
	if err != nil {
		return fmt.Errorf("invalid state path: %w", err)
	}
	if err := demostate.PreflightGeneric(private); err != nil {
		return err
	}
	filename := *definitionPath
	if !filepath.IsAbs(filename) {
		filename = filepath.Join(abs, filename)
	}
	definition, err := canvas.LoadManualDefinitionWithResolver(filename, contract.NewReleaseResolver(filepath.Join(private, "suite-cache")))
	if err != nil {
		return err
	}
	bundles := definition.Bundles
	bundles, err = profile.Contracts(bundles)
	if err != nil {
		return err
	}
	provider, err := sessionhost.ConfiguredProvider(profile, filepath.Join(abs, "model.lock.json"), *base, bundles)
	if err != nil {
		return err
	}
	var host *sessionhost.Host
	if *runtime == "local" {
		host, err = sessionhost.StartAt(filepath.Join(private, "sessions"), *listen, bundles, provider)
	} else {
		conn, dialErr := substrateruntime.DialControl(substrateruntime.ControlConfig{Address: *control, ServerName: *serverName, CAFile: *caFile, TokenFile: *tokenFile})
		if dialErr != nil {
			return dialErr
		}
		defer conn.Close()
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		controlErr := substrateruntime.EnsureAtespace(startup, conn)
		cancel()
		if controlErr != nil {
			return controlErr
		}
		lis, listenErr := net.Listen("tcp", *listen)
		if listenErr != nil {
			return fmt.Errorf("cannot open Sessions listener: %w", listenErr)
		}
		factory := substrateruntime.Factory(conn, *namespace)
		if *retainActors {
			factory = substrateruntime.RetainedFactory(conn, *namespace)
		}
		host, err = sessionhost.StartWithOptions(filepath.Join(private, "sessions"), bundles, provider, sessionhost.Options{Listener: lis, BackendFactory: factory})
	}
	if err != nil {
		return err
	}
	defer host.Close()
	fmt.Fprintln(output, "Sessions gRPC:", host.Address())
	select {
	case <-ctx.Done():
		return nil
	case err := <-host.Done():
		return fmt.Errorf("Sessions gRPC server stopped: %v", err)
	}
}
