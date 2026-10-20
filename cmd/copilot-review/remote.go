package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Configuration contains locations and trust policy only, never a bearer token.
type remoteConfig struct {
	Endpoint     string                         `json:"endpoint"`
	CAFile       string                         `json:"caFile"`
	ServerName   string                         `json:"serverName,omitempty"`
	TokenSource  sessionauth.SourceConfig       `json:"tokenSource"`
	Identity     sessionauth.VerificationConfig `json:"identity"`
	ReviewPolicy struct {
		Role      string                 `json:"role"`
		Reviewers []sessionauth.Identity `json:"reviewers"`
	} `json:"reviewPolicy"`
}

type remoteEngine struct {
	endpoint          string
	tls               *tls.Config
	auth              *sessionauth.Manager
	executionAuth     *sessionauth.Manager
	executionIdentity sessionauth.Identity
	reviewPolicy      canvas.ReviewAuthorizer
	reviewRole        string
}

func readRemoteConfig(path string) (remoteConfig, error) {
	var c remoteConfig
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("cannot read sessions configuration")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return c, errors.New("sessions configuration must be a bounded regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, (64<<10)+1))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return remoteConfig{}, errors.New("invalid sessions configuration; bearer tokens do not belong in configuration")
	}
	host, port, err := net.SplitHostPort(c.Endpoint)
	number, e := strconv.Atoi(port)
	if err != nil || e != nil || host == "" || strings.ContainsAny(host, "/\\@?#") || number < 1 || number > 65535 || c.CAFile == "" {
		return remoteConfig{}, errors.New("sessions requires a host:port endpoint and a CA file; plaintext is unsupported")
	}
	return c, nil
}

func openRemote(ctx context.Context, c remoteConfig) (*remoteEngine, error) {
	policy, err := canvas.NewReviewerPolicy(c.ReviewPolicy.Role, c.ReviewPolicy.Reviewers)
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, errors.New("cannot read sessions CA")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("sessions CA must contain a trusted PEM certificate")
	}
	source, err := c.TokenSource.Open()
	if err != nil {
		return nil, err
	}
	verifier, err := sessionauth.NewVerifier(ctx, c.Identity)
	if err != nil {
		return nil, err
	}
	return &remoteEngine{endpoint: c.Endpoint, tls: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: c.ServerName}, auth: sessionauth.New(source, verifier), reviewPolicy: policy, reviewRole: c.ReviewPolicy.Role}, nil
}

func (r *remoteEngine) dial(project string) (*client.Client, error) {
	return client.Dial(r.endpoint, client.WithProject(project), client.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(r.tls)), grpc.WithPerRPCCredentials(r.auth), grpc.WithDisableRetry(), grpc.WithNoProxy()))
}

var errExecutionBinding = errors.New("authenticated execution requires a separately verified service binding; hosted service credentials are not yet qualified")

// The current Entra verifier is user-only, so -sessions-config cannot supply
// this binding. A trusted Go host adapter must inject a separately verified
// service manager; never relabel or reuse the human credential.
func (r *remoteEngine) hasExecutionBinding() bool {
	return r.executionAuth != nil && r.executionAuth != r.auth && r.executionIdentity.Valid()
}

func (r *remoteEngine) dialExecution(project string) (*client.Client, error) {
	if !r.hasExecutionBinding() {
		return nil, errExecutionBinding
	}
	return client.Dial(r.endpoint, client.WithProject(project), client.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(r.tls)), grpc.WithPerRPCCredentials(r.executionAuth), grpc.WithDisableRetry(), grpc.WithNoProxy()))
}

func validateRemoteState(config, state string, explicit bool) error {
	if config == "" {
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
		return errors.New("-sessions-config requires an explicit separate -state directory")
	}
	return nil
}

// Keep the authenticated engine on the generic action surface. Legacy routes
// cannot turn a browser-selected persona into an authenticated approval.
func authenticatedHandler(c *canvas.Canvas, host, boot, role string, auth *sessionauth.Manager) http.Handler {
	routes := http.NewServeMux()
	api := http.NewServeMux()
	c.RegisterAPIRoutes(api)
	routes.Handle("GET /engine/", http.StripPrefix("/engine", api))
	routes.Handle("POST /engine/", http.StripPrefix("/engine", api))
	routes.HandleFunc("GET /api/demo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"mode": "authenticated-sessions", "identity": "engine-verified per action; sessions authorizes", "boot": boot, "headlessPrefix": "/engine", "reviewerRole": role})
	})
	routes.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "Open the Copilot review canvas. Sessions credentials remain in the engine.\n")
	})
	return canvas.LocalHandler(host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/engine/") {
			ctx, _, err := auth.Bind(r.Context())
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				message := "Sign in again: sessions credentials are missing, invalid or expired."
				if sessionauth.IsIdentityChanged(err) {
					message = "Credential identity changed. Reinspect this action with the intended reviewer."
				}
				json.NewEncoder(w).Encode(map[string]string{"error": message, "code": "UNAUTHENTICATED"})
				return
			}
			r = r.WithContext(ctx)
		}
		routes.ServeHTTP(w, r)
	}))
}
