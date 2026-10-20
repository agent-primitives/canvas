// canvas-engine is an explicit trusted-network, headless TLS Canvas runtime.
// It never starts an embedded Sessions host or accepts provider/identity keys.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const readyBody = "canvas-engine ready\n"

type engine struct {
	config                       config
	apiAddress, readinessAddress string
	client                       *client.Client
	done                         chan error
	close                        func()
}

func (e *engine) Close() { e.close() }

func startEngine(ctx context.Context, cfg config) (_ *engine, err error) {
	if err = cfg.validate(); err != nil {
		return nil, err
	}
	definition, err := canvas.LoadManualDefinitionWithResolver(cfg.DefinitionFile, nil)
	if err != nil {
		return nil, fmt.Errorf("prepared definition: %w", err)
	}
	if err = definition.RequireManualRuntime(); err != nil {
		return nil, err
	}
	binding := hostingRecord{Version: 1, InstanceID: cfg.InstanceID, DefinitionDigest: definition.Digest, SessionsEndpoint: cfg.SessionsEndpoint}
	if err = preflightState(cfg.StateDirectory, binding); err != nil {
		return nil, err
	}
	ca, err := readRegular(cfg.SessionsCAFile, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("sessions CA file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("sessions CA file has no certificates")
	}
	certPEM, err := readRegular(cfg.TLSCertFile, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("engine certificate file: %w", err)
	}
	keyPEM, err := readRegular(cfg.TLSKeyFile, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("engine key file: %w", err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("engine TLS certificate/key invalid")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || leaf.VerifyHostname("127.0.0.1") != nil {
		return nil, errors.New("engine TLS certificate requires IP SAN 127.0.0.1")
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, errors.New("engine TLS certificate is outside its validity period")
	}
	cleanups := []func(){}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		})
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	apiListener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("API listener: %w", err)
	}
	cleanups = append(cleanups, func() { apiListener.Close() })
	readyListener, err := net.Listen("tcp", cfg.ReadinessAddress)
	if err != nil {
		return nil, fmt.Errorf("readiness listener: %w", err)
	}
	cleanups = append(cleanups, func() { readyListener.Close() })
	host, _, _ := net.SplitHostPort(cfg.SessionsEndpoint)
	cl, err := client.Dial(cfg.SessionsEndpoint, client.WithProject(cfg.InstanceID), client.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: host})), grpc.WithDisableRetry(), grpc.WithNoProxy()))
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { cl.Close() })
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = cl.ListSessionsPage(probeCtx, "", 1, "")
	cancel()
	if err != nil {
		return nil, fmt.Errorf("sessions read-only TLS connectivity probe: %w", err)
	}
	release, err := acquireState(cfg.StateDirectory, binding)
	if err != nil {
		return nil, err
	}
	// The connection must close before the lock, not merely before process exit.
	cleanups = cleanups[:len(cleanups)-1]
	cleanups = append(cleanups, release, func() { cl.Close() })
	if err = bindHosting(cfg.StateDirectory, binding); err != nil {
		return nil, err
	}
	space, err := sharedspace.OpenWithPolicy(filepath.Join(cfg.StateDirectory, "space"), definition.Seed, definition.Policy)
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { space.Close() })
	c, err := canvas.OpenDefinedInstance(filepath.Join(cfg.StateDirectory, "canvas"), space, cl, cfg.SessionsEndpoint, definition, cfg.InstanceID, contract.LocalBinder{})
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, c.Close)
	c.EnableRemoteSessions()
	handler, err := hostedHandler(c, cfg.Origin, canvas.ID())
	if err != nil {
		return nil, err
	}
	apiServer := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}}
	readyMux := http.NewServeMux()
	readyMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, readyBody)
	})
	readyServer := &http.Server{Handler: readyMux, ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second}
	cleanups = append(cleanups, func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		readyServer.Shutdown(stopCtx)
		apiServer.Shutdown(stopCtx)
		readyServer.Close()
		apiServer.Close()
	})
	e := &engine{config: cfg, apiAddress: apiListener.Addr().String(), readinessAddress: readyListener.Addr().String(), client: cl, done: make(chan error, 2), close: cleanup}
	go func() { e.done <- apiServer.Serve(tls.NewListener(apiListener, apiServer.TLSConfig)) }()
	// Starting this only now is intentional: listen backlog is not readiness.
	go func() { e.done <- readyServer.Serve(readyListener) }()
	return e, nil
}

func hostedHandler(c *canvas.Canvas, origin, boot string) (http.Handler, error) {
	routes := http.NewServeMux()
	c.RegisterAPIRoutes(routes)
	api := http.StripPrefix("/engine", routes)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if r.Method == http.MethodPost && (path == "/api/announcement" || strings.HasPrefix(path, "/api/announcement/") || path == "/engine/api/announcement" || strings.HasPrefix(path, "/engine/api/announcement/")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "Hosted announcement execution and replay are unavailable until the remote executor is implemented."})
			return
		}
		if path == "/" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "Canvas hosted engine; identity not exercised.\n")
			return
		}
		if path == "/api/demo" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"mode": "hosted-sessions", "boot": boot, "identity": "not exercised (unauthenticated trusted network)", "headlessPrefix": "/engine", "modelCalls": nil, "executorCalls": nil})
			return
		}
		allowed := false
		switch r.Method {
		case http.MethodGet:
			switch path {
			case "/engine/api/state", "/engine/api/document", "/engine/api/forks", "/engine/api/announcement":
				allowed = true
			}
		case http.MethodPost:
			switch path {
			case "/engine/api/run", "/engine/api/review", "/engine/api/verify":
				allowed = true
			}
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
	return canvas.TLSLoopbackHandler(origin, handler)
}

func healthProbe(address string) error {
	if err := loopbackAddress(address, false); err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("readiness redirects refused") }}
	res, err := hc.Get("http://" + address + "/readyz")
	if err != nil {
		return fmt.Errorf("engine not ready: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(len(readyBody)+1)))
	if err != nil || res.StatusCode != 200 || string(body) != readyBody {
		return errors.New("engine readiness response invalid")
	}
	return nil
}
func run(args []string) error {
	if len(args) > 0 && args[0] == "health-probe" {
		if len(args) != 2 {
			return errors.New("usage: canvas-engine health-probe 127.0.0.1:PORT")
		}
		return healthProbe(args[1])
	}
	flags := flag.NewFlagSet("canvas-engine", flag.ContinueOnError)
	path := flags.String("config", "", "closed version-1 operator JSON configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("usage: canvas-engine -config FILE")
	}
	cfg, err := readConfig(*path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	e, err := startEngine(ctx, cfg)
	if err != nil {
		return err
	}
	defer e.Close()
	fmt.Println("Canvas hosted engine ready; identity not exercised:", cfg.Origin)
	select {
	case <-ctx.Done():
		return nil
	case err := <-e.done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
