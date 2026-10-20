package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type tlsFixture struct {
	config              config
	certificate         tls.Certificate
	roots               *x509.CertPool
	calls, exec, resume atomic.Int64
	blockModel          atomic.Bool
	host                *sessionhost.Host
}

func certificate(t *testing.T, root string, ip bool) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true, DNSNames: []string{"agents.default.svc.cluster.local"}}
	if ip {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	for name, data := range map[string][]byte{"ca.crt": certPEM, "tls.crt": certPEM, "tls.key": keyPEM} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return cert, pool
}
func fixture(t *testing.T, block <-chan struct{}, ip, plain bool) *tlsFixture {
	t.Helper()
	f := &tlsFixture{config: validConfig(t)}
	root := filepath.Dir(f.config.StateDirectory)
	f.certificate, f.roots = certificate(t, root, ip)
	if !ip {
		// Isolate host hostname verification from the engine's own IP certificate.
		apiRoot := t.TempDir()
		_, f.roots = certificate(t, apiRoot, true)
		f.config.TLSCertFile = filepath.Join(apiRoot, "tls.crt")
		f.config.TLSKeyFile = filepath.Join(apiRoot, "tls.key")
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	f.config.DefinitionFile = path
	d, err := canvas.LoadManualDefinitionWithResolver(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opts := []grpc.ServerOption{grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if block != nil && strings.HasSuffix(info.FullMethod, "/ListSessions") {
			select {
			case <-block:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return next(ctx, req)
	}), grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		if strings.HasSuffix(info.FullMethod, "/Exec") {
			f.exec.Add(1)
		}
		if strings.HasSuffix(info.FullMethod, "/Resume") {
			f.resume.Add(1)
		}
		return next(srv, ss)
	})}
	if !plain {
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{f.certificate}})))
	}
	f.host, err = sessionhost.StartWithOptions(filepath.Join(t.TempDir(), "host"), d.Bundles, func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		f.calls.Add(1)
		if f.blockModel.Load() {
			<-ctx.Done()
			return api.ModelResponse{}, ctx.Err()
		}
		if req.Params["max_tokens"] != "16384" || req.Params["response_format"] == "" || len(req.Messages) < 2 {
			return api.ModelResponse{}, fmt.Errorf("unexpected canonical ModelRequest: %+v", req.Params)
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"changes":[{"document":"announcement","operation":"replace","content":"# Hosted proposal\n"}]}`)}, nil
	}, sessionhost.Options{Listener: listener, ServerOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.host.Close() })
	f.config.SessionsEndpoint = f.host.Address()
	return f
}
func request(t *testing.T, e *engine, hc *http.Client, method, path string, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, "https://"+e.apiAddress+path, bytes.NewReader(raw))
	u, _ := url.Parse(e.config.Origin)
	req.Host = u.Host
	req.Header.Set("Origin", e.config.Origin)
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && json.Unmarshal(data, out) != nil {
		t.Fatalf("%d: %s", res.StatusCode, data)
	}
	return res.StatusCode
}
func httpClient(f *tlsFixture) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.roots, MinVersion: tls.VersionTLS12}}}
}

func TestHostedTLSRunReviewReopen(t *testing.T) {
	f := fixture(t, nil, true, false)
	e, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
		t.Fatal("startup dispatched")
	}
	if err := healthProbe(e.readinessAddress); err != nil {
		t.Fatal(err)
	}
	hc := httpClient(f)
	if code := request(t, e, hc, "GET", "/", nil, nil); code != 200 {
		t.Fatal(code)
	}
	var meta map[string]any
	request(t, e, hc, "GET", "/api/demo", nil, &meta)
	if meta["mode"] != "hosted-sessions" || meta["modelCalls"] != nil || meta["executorCalls"] != nil || meta["boot"] == "" {
		t.Fatal(meta)
	}
	boot := meta["boot"]
	for _, prefix := range []string{"", "/engine"} {
		for _, suffix := range []string{"request", "decision", "continue", "verify", "recover", "abandon", "unknown"} {
			var out map[string]any
			if code := request(t, e, hc, "POST", prefix+"/api/announcement/"+suffix, map[string]string{}, &out); code != 409 || out["error"] == nil {
				t.Fatalf("announcement %s %s: %d %+v", prefix, suffix, code, out)
			}
		}
		for _, name := range []string{"resume", "ask", "input", "comment", "role", "regenerate", "other"} {
			code := request(t, e, hc, "POST", prefix+"/api/"+name, map[string]string{}, nil)
			if code != 404 && code != 405 {
				t.Fatalf("legacy %s/%s: %d", prefix, name, code)
			}
		}
	}
	if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
		t.Fatal("denied routes dispatched")
	}
	var run struct {
		Run string `json:"run"`
	}
	if code := request(t, e, hc, "POST", "/engine/api/run", map[string]string{"agent": "writer", "role": "GM", "request": "Draft a proposal"}, &run); code != 202 {
		t.Fatal(code)
	}
	var proposal string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var state canvas.State
		request(t, e, hc, "GET", "/engine/api/state", nil, &state)
		for _, r := range state.Runs {
			if r.ID == run.Run {
				if r.Status == "failed" {
					t.Fatalf("run failed: %+v", r)
				}
				proposal = r.Proposal
			}
		}
		if proposal != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if proposal == "" {
		t.Fatal("no producer proposal")
	}
	if code := request(t, e, hc, "POST", "/engine/api/review", map[string]string{"proposal": proposal, "action": "accept", "role": "GM"}, nil); code != 200 {
		t.Fatal(code)
	}
	if code := request(t, e, hc, "POST", "/engine/api/verify", map[string]string{"run": run.Run}, nil); code != 409 {
		t.Fatal(code)
	}
	sessions, err := e.client.ListSessions(t.Context(), "")
	if err != nil || len(sessions) != 1 || sessions[0].Metadata.Project != f.config.InstanceID {
		t.Fatalf("instance project: %+v %v", sessions, err)
	}
	if _, err := os.Stat(filepath.Join(f.config.StateDirectory, "sessions")); !os.IsNotExist(err) {
		t.Fatal("embedded Sessions state exists")
	}
	if _, err := startEngine(t.Context(), f.config); err == nil {
		t.Fatal("second writer allowed")
	}
	calls, exec, resume := f.calls.Load(), f.exec.Load(), f.resume.Load()
	e.Close()
	e2, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	request(t, e2, hc, "GET", "/api/demo", nil, &meta)
	if meta["boot"] == boot {
		t.Fatal("boot retained")
	}
	var state canvas.State
	request(t, e2, hc, "GET", "/engine/api/state", nil, &state)
	if len(state.History) < 2 {
		t.Fatalf("accepted history lost: %+v", state.History)
	}
	if f.calls.Load() != calls || f.exec.Load() != exec || f.resume.Load() != resume {
		t.Fatal("restart dispatched")
	}
	e2.Close()
	// hosting.json deliberately cannot identify a replacement journal at the
	// same endpoint. Missing old UIDs must stay unavailable, never create/Exec.
	if err := f.host.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := canvas.LoadManualDefinitionWithResolver(f.config.DefinitionFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", f.config.SessionsEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	var replacementMutations atomic.Int64
	replacement, err := sessionhost.StartWithOptions(filepath.Join(t.TempDir(), "replacement"), d.Bundles,
		func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
			f.calls.Add(1)
			return api.ModelResponse{}, fmt.Errorf("replacement must not run")
		},
		sessionhost.Options{Listener: listener, ServerOptions: []grpc.ServerOption{
			grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{f.certificate}})),
			grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				if strings.HasSuffix(info.FullMethod, "/CreateSession") {
					replacementMutations.Add(1)
				}
				return next(ctx, req)
			}),
			grpc.ChainStreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
				if strings.HasSuffix(info.FullMethod, "/Exec") || strings.HasSuffix(info.FullMethod, "/Resume") {
					replacementMutations.Add(1)
				}
				return next(srv, stream)
			}),
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	e3, err := startEngine(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e3.client.GetSession(t.Context(), sessions[0].Metadata.Uid); err == nil {
		t.Fatal("replacement retained old UID")
	}
	e3.Close()
	binding := hostingRecord{Version: 1, InstanceID: f.config.InstanceID, DefinitionDigest: d.Digest, SessionsEndpoint: f.config.SessionsEndpoint}
	release, err := acquireState(f.config.StateDirectory, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	space, err := sharedspace.OpenWithPolicy(filepath.Join(f.config.StateDirectory, "space"), d.Seed, d.Policy)
	if err != nil {
		t.Fatal(err)
	}
	defer space.Close()
	cl, err := client.Dial(f.config.SessionsEndpoint, client.WithProject(f.config.InstanceID), client.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots, ServerName: "127.0.0.1"})), grpc.WithNoProxy(), grpc.WithDisableRetry()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c, err := canvas.OpenDefinedInstance(filepath.Join(f.config.StateDirectory, "canvas"), space, cl, f.config.SessionsEndpoint, d, f.config.InstanceID, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.EnableRemoteSessions()
	if inspection := c.InspectResume(t.Context(), run.Run, ""); inspection.Available {
		t.Fatal("missing UID offered recovery")
	}
	if err := c.ConfirmResume(t.Context(), run.Run, "", "GM", canvas.ResumeConfirmation{ActionID: strings.Repeat("a", 32), Confirmed: true}); err == nil {
		t.Fatal("missing UID resumed")
	}
	if replacementMutations.Load() != 0 || f.calls.Load() != calls {
		t.Fatal("replacement caused fallback or new execution")
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}
func TestReservedReadinessIsNotReady(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	f := fixture(t, block, true, false)
	f.config.ReadinessAddress = freeAddress(t)
	f.config.Listen = freeAddress(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		e, err := startEngine(ctx, f.config)
		if e != nil {
			e.Close()
		}
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", f.config.ReadinessAddress, 50*time.Millisecond)
		if err == nil {
			c.Close()
			connected = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !connected {
		t.Fatal("readiness not reserved")
	}
	if err := healthProbe(f.config.ReadinessAddress); err == nil {
		t.Fatal("unserved reserved socket passed readiness")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("blocked startup unexpectedly succeeded")
	}
	if _, err := os.Stat(f.config.StateDirectory); !os.IsNotExist(err) {
		t.Fatalf("state touched before connectivity: %v", err)
	}
}

func TestTLSFailuresDoNotCreateState(t *testing.T) {
	for _, kind := range []string{"wrong-san", "plaintext", "untrusted-ca"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t, nil, kind != "wrong-san", kind == "plaintext")
			if kind == "untrusted-ca" {
				dir := t.TempDir()
				certificate(t, dir, true)
				f.config.SessionsCAFile = filepath.Join(dir, "ca.crt")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
			defer cancel()
			if e, err := startEngine(ctx, f.config); err == nil {
				e.Close()
				t.Fatal("bad TLS accepted")
			} else if !strings.Contains(err.Error(), "sessions read-only TLS connectivity probe") {
				t.Fatalf("did not exercise host TLS: %v", err)
			}
			if _, err := os.Stat(f.config.StateDirectory); !os.IsNotExist(err) {
				t.Fatalf("state touched: %v", err)
			}
			if f.exec.Load() != 0 || f.calls.Load() != 0 {
				t.Fatal("TLS failure dispatched")
			}
		})
	}
}
