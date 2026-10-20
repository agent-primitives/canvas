// bundle-harness runs one agent contract as a remote actor. It holds no model
// key: every model call goes back to the Sessions host through harnesswire.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/substrateruntime"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harnesswire"
	"google.golang.org/grpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "bundle-harness:", err)
		os.Exit(1)
	}
}
func serve(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("bundle-harness", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("bundle", "", "baked immutable agent.yaml path")
	profile := flags.Int("profile", 3, "execution profile: 1 (legacy), 2, or 3 (read-only questions)")
	model := flags.String("model", "", "execution model name (empty preserves the authored contract)")
	describe := flags.Bool("describe", false, "emit execution identity as JSON without opening listeners")
	listen := flags.String("listen", "0.0.0.0:80", "private actor harnesswire listener")
	readiness := flags.String("ready-listen", "0.0.0.0:8081", "private actor readiness listener")
	flags.Usage = func() {
		fmt.Fprintln(out, "Usage: bundle-harness [flags]")
		flags.SetOutput(out)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	b, err := contract.Load(*path)
	if err != nil {
		return err
	}
	if b.Suite != nil || *profile == 4 {
		return errors.New("bundle-harness: native suite/profile 4 is local-only; remote actor bootstrap unsupported")
	}
	if *model != "" {
		b, err = contract.ForModel(b, *model)
		if err != nil {
			return err
		}
	}
	har, err := sessionhost.NewBundleHarness(b, *profile)
	if err != nil {
		return err
	}
	if *describe {
		descriptor, err := har.Describe(ctx)
		if err != nil {
			return err
		}
		template, err := substrateruntime.TemplateName(b, *profile)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			Template string `json:"template"`
			Harness  string `json:"harness"`
			Model    string `json:"model"`
		}{Template: template, Harness: descriptor.ID, Model: b.Model})
	}
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("cannot listen: %w", err)
	}
	defer lis.Close()
	ready, err := net.Listen("tcp", *readiness)
	if err != nil {
		return fmt.Errorf("cannot listen for readiness: %w", err)
	}
	defer ready.Close()
	server := grpc.NewServer()
	v1.RegisterHarnessServer(server, harnesswire.NewServer(har))
	defer server.Stop()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	health := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer health.Close()
	done := make(chan error, 2)
	go func() { done <- server.Serve(lis) }()
	go func() { done <- health.Serve(ready) }()
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return errors.New("actor listener stopped")
	}
}
