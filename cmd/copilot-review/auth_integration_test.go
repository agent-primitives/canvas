package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testTenant = "11111111-1111-1111-1111-111111111111"
const testAudience = "22222222-2222-2222-2222-222222222222"
const testClientID = "33333333-3333-3333-3333-333333333333"
const testIssuer = "https://login.microsoftonline.com/" + testTenant + "/v2.0"

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type hostVerifier struct{ sessionauth.Verifier }

func (v hostVerifier) Verify(ctx context.Context) (authn.VerifiedCaller, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return authn.VerifiedCaller{}, fmt.Errorf("missing credential")
	}
	id, exp, err := v.Verifier.Verify(ctx, strings.TrimPrefix(values[0], "Bearer "))
	return authn.VerifiedCaller{Identity: api.CallerIdentity{Issuer: id.Issuer, Subject: id.Subject}, ValidUntil: exp}, err
}

func signedToken(t *testing.T, key *rsa.PrivateKey, subject string, expiry time.Time) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": testIssuer, "tid": testTenant, "aud": testAudience, "azp": testClientID, "ver": "2.0", "idtyp": "user", "scp": "sessions.access", "oid": subject, "sub": "pairwise-" + subject, "iat": time.Now().Unix(), "nbf": time.Now().Add(-time.Minute).Unix(), "exp": expiry.Unix()})
	encode := base64.RawURLEncoding.EncodeToString
	raw := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(raw))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return raw + "." + encode(signature)
}

func integrationVerifier(t *testing.T) (sessionauth.Verifier, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwksURL := "https://login.microsoftonline.com/" + testTenant + "/discovery/v2.0/keys"
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		var data any
		switch r.URL.String() {
		case testIssuer + "/.well-known/openid-configuration":
			data = map[string]any{"issuer": testIssuer, "jwks_uri": jwksURL}
		case jwksURL:
			data = map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test-key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}}
		default:
			return nil, fmt.Errorf("unexpected identity endpoint")
		}
		raw, _ := json.Marshal(data)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
	})
	v, err := sessionauth.NewVerifier(context.Background(), sessionauth.VerificationConfig{TenantID: testTenant, Audience: testAudience, AllowedClientIDs: []string{testClientID}, RequiredScope: "sessions.access", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return v, key
}

func sessionsCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sessions.test"}, DNSNames: []string{"sessions.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return cert, pool
}

func TestAuthenticatedEngineWithSessionsInterceptors(t *testing.T) {
	verifier, key := integrationVerifier(t)
	cert, pool := sessionsCertificate(t)
	serviceToken := newServiceToken(t)
	unary, stream, err := authn.NewInterceptors(authn.Required, compositeVerifier{service: opaqueServiceVerifier{token: serviceToken, identity: fixtureServiceIdentity}, human: hostVerifier{verifier}})
	if err != nil {
		t.Fatal(err)
	}
	// This test gate checks service execution and human approval separately.
	gate := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if info.FullMethod == v1.Sessions_CreateSession_FullMethodName {
			caller, ok := authn.Caller(ctx)
			if !ok || caller.Issuer != serviceIssuer || caller.Subject != "canvas-engine" {
				return nil, status.Error(codes.PermissionDenied, "execution requires engine service identity")
			}
		}
		if info.FullMethod == v1.Sessions_Approve_FullMethodName {
			caller, ok := authn.Caller(ctx)
			if !ok || caller.Issuer != testIssuer || caller.Subject != "approver" {
				return nil, status.Error(codes.PermissionDenied, "private host rejection text")
			}
		}
		return next(ctx, req)
	}
	definition, err := canvas.LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostRoot := filepath.Join(t.TempDir(), "host")
	host, err := sessionhost.StartWithOptions(hostRoot, definition.Bundles, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"changes":[{"document":"announcement","operation":"replace","content":"# Reviewed announcement\n"}]}`)}, nil
	}, sessionhost.Options{Listener: listener, ServerOptions: []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})), grpc.ChainUnaryInterceptor(unary, gate), grpc.ChainStreamInterceptor(stream)}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	policy, err := canvas.NewReviewerPolicy("GM", []sessionauth.Identity{{Issuer: testIssuer, Subject: "approver"}, {Issuer: testIssuer, Subject: "non-approver"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"approver", "non-approver", "signed-in-outsider"} {
		t.Run(subject, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour)
			token := signedToken(t, key, subject, expiry)
			source := sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) { return token, expiry, nil })
			remote := &remoteEngine{endpoint: host.Address(), tls: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "sessions.test"}, auth: sessionauth.New(source, verifier), executionAuth: serviceManager(serviceToken), executionIdentity: fixtureServiceIdentity, reviewPolicy: policy}
			state := t.TempDir()
			engine, err := startConfigured(filepath.Join("..", ".."), state, "127.0.0.1:0", "", remote)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			jar, _ := cookiejar.New(nil)
			httpClient := &http.Client{Jar: jar, Timeout: 15 * time.Second}
			res, err := httpClient.Get(engine.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			request := func(method, path string, body any, out any) int {
				t.Helper()
				raw, _ := json.Marshal(body)
				req, _ := http.NewRequest(method, engine.URL+path, bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", engine.URL)
				res, err := httpClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				data, _ := io.ReadAll(res.Body)
				if bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(serviceToken)) || bytes.Contains(data, []byte("private host rejection text")) {
					t.Fatal("credential or private failure leaked")
				}
				if out != nil {
					if err := json.Unmarshal(data, out); err != nil {
						t.Fatalf("response %d: %s", res.StatusCode, data)
					}
				}
				return res.StatusCode
			}
			if code := request("POST", "/api/review", map[string]any{"role": "GM"}, nil); code != 404 {
				t.Fatalf("legacy route active: %d", code)
			}
			var queued struct {
				Run string `json:"run"`
			}
			if code := request("POST", "/engine/api/run", map[string]string{"agent": "writer", "role": "GM", "request": "Prepare review"}, &queued); code != 202 {
				t.Fatal(code)
			}
			var proposal string
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				var state canvas.State
				request("GET", "/engine/api/state", nil, &state)
				for _, r := range state.Runs {
					if r.ID == queued.Run {
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
				t.Fatal("no proposal")
			}
			var denied map[string]any
			if code := request("POST", "/engine/api/review", map[string]string{"proposal": proposal, "action": "accept", "role": "owner"}, &denied); code != 403 || denied["code"] != "PERMISSION_DENIED" {
				t.Fatalf("role spoof: %d %+v", code, denied)
			}
			if subject == "signed-in-outsider" {
				if code := request("POST", "/engine/api/review", map[string]string{"proposal": proposal, "action": "accept", "role": "GM"}, &denied); code != 403 {
					t.Fatalf("outsider review: %d %+v", code, denied)
				}
				return
			}
			var accepted map[string]any
			if code := request("POST", "/engine/api/review", map[string]string{"proposal": proposal, "action": "accept", "role": "GM"}, &accepted); code != 200 {
				t.Fatalf("document review: %d %+v", code, accepted)
			}
			reviewer, _ := accepted["reviewer"].(map[string]any)
			if reviewer["subject"] != subject || reviewer["role"] != "GM" {
				t.Fatalf("document review lost identity: %+v", accepted)
			}
			var op struct {
				ID string `json:"id"`
			}
			if code := request("POST", "/engine/api/announcement/request", map[string]string{"proposal": proposal, "action": "announce", "role": "GM"}, &op); code != 200 {
				t.Fatal(code)
			}
			var view canvas.AnnouncementView
			if code := request("GET", "/engine/api/announcement?id="+op.ID, nil, &view); code != 200 || view.Status != "pending-approval" {
				t.Fatalf("inspect %d %+v", code, view)
			}
			view.Confirmation.Confirmed = true
			var result map[string]any
			code := request("POST", "/engine/api/announcement/decision", map[string]any{"id": op.ID, "role": "GM", "approved": true, "confirmation": view.Confirmation}, &result)
			if subject == "approver" {
				if code != 200 {
					t.Fatalf("approver %d %+v", code, result)
				}
			} else {
				if code != 403 || result["code"] != "PERMISSION_DENIED" {
					t.Fatalf("non-approver %d %+v", code, result)
				}
			}
			// Credentials must never reach Canvas ledgers, Git content or the session journal.
			for _, root := range []string{state, hostRoot} {
				err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(serviceToken)) {
						t.Fatalf("token persisted in %s", path)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("background credential identity switch is a clear authentication failure", func(t *testing.T) {
		expiry := time.Now().Add(time.Hour)
		token := signedToken(t, key, "another-reviewer", expiry)
		manager := sessionauth.New(sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) { return token, expiry, nil }), verifier)
		r := &remoteEngine{endpoint: host.Address(), tls: &tls.Config{RootCAs: pool, ServerName: "sessions.test"}, auth: manager, executionAuth: serviceManager(serviceToken), executionIdentity: fixtureServiceIdentity}
		cl, err := r.dial("test")
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ctx = sessionauth.Expect(ctx, sessionauth.Identity{Issuer: testIssuer, Subject: "original-reviewer"})
		_, err = cl.GetSession(ctx, "missing")
		if status.Code(err) != codes.Unauthenticated || !strings.Contains(status.Convert(err).Message(), "identity changed") {
			t.Fatalf("identity switch: %v", err)
		}
	})
	t.Run("expired token refused by host", func(t *testing.T) {
		conn, err := grpc.NewClient(host.Address(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "sessions.test"})))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+signedToken(t, key, "approver", time.Now().Add(-time.Minute)))
		_, err = v1.NewSessionsClient(conn).GetSession(ctx, &v1.GetSessionRequest{Uid: "missing"})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expired: %v", err)
		}
	})
	t.Run("TLS required and server validated", func(t *testing.T) {
		for _, transport := range []credentials.TransportCredentials{insecure.NewCredentials(), credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "wrong.test"}), credentials.NewTLS(&tls.Config{RootCAs: x509.NewCertPool(), ServerName: "sessions.test"})} {
			conn, err := grpc.NewClient(host.Address(), grpc.WithTransportCredentials(transport))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			_, err = v1.NewSessionsClient(conn).GetSession(ctx, &v1.GetSessionRequest{Uid: "missing"})
			cancel()
			conn.Close()
			if err == nil {
				t.Fatal("untrusted or plaintext connection accepted")
			}
		}
	})
}
