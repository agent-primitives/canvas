package sessionauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tenantID = "11111111-2222-3333-4444-5555555abcde"
	audience = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	clientID = "12345678-1234-1234-1234-123456789abc"
	issuer   = "https://login.microsoftonline.com/" + tenantID + "/v2.0"
)

type trustFixture struct {
	key    *rsa.PrivateKey
	other  *rsa.PrivateKey
	client *http.Client
	now    time.Time
}

func TestNewVerifierOfflineDiscoveryAndClaimChecks(t *testing.T) {
	f := newTrustFixture(t)
	cfg := VerificationConfig{TenantID: tenantID, Audience: audience, AllowedClientIDs: []string{clientID}, RequiredScope: "sessions.access", HTTPClient: f.client}
	v, err := NewVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, exp, err := v.Verify(context.Background(), f.signed(claims(f.now), f.key))
	if err != nil || id != (Identity{Issuer: issuer, Subject: "object-id-a"}) || !exp.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("Verify() = %+v %v %v", id, exp, err)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong tenant", func(c map[string]any) { c["tid"] = audience }},
		{"missing nbf", func(c map[string]any) { delete(c, "nbf") }},
		{"subject contains whitespace", func(c map[string]any) { c["oid"] = "identity with space" }},
		{"subject contains control", func(c map[string]any) { c["oid"] = "identity\nforged" }},
		{"wrong audience", func(c map[string]any) { c["aud"] = clientID }},
		{"wrong client", func(c map[string]any) { c["azp"] = audience }},
		{"wrong scope", func(c map[string]any) { c["scp"] = "other.scope" }},
		{"expired", func(c map[string]any) { c["exp"] = f.now.Unix() }},
		{"missing exp", func(c map[string]any) { delete(c, "exp") }},
		{"not yet valid", func(c map[string]any) { c["nbf"] = f.now.Add(time.Minute).Unix() }},
		{"wrong version", func(c map[string]any) { c["ver"] = "1.0" }},
		{"missing oid", func(c map[string]any) { delete(c, "oid") }},
		{"app token", func(c map[string]any) { c["idtyp"] = "app" }},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://attacker.invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := claims(f.now)
			tc.edit(c)
			_, _, err := v.Verify(context.Background(), f.signed(c, f.key))
			if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage {
				t.Fatalf("status = %v", err)
			}
		})
	}
	unsigned := rawJWT(map[string]any{"alg": "none", "typ": "JWT"}, claims(f.now), nil)
	if _, _, err := v.Verify(context.Background(), unsigned); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unsigned status = %v", err)
	}
	if _, _, err := v.Verify(context.Background(), f.signed(claims(f.now), f.other)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("signature status = %v", err)
	}
	v, err = NewVerifier(context.Background(), VerificationConfig{TenantID: tenantID, Audience: audience, AllowedClientIDs: []string{clientID}, RequiredScope: "sessions.access", SubjectClaim: "sub", HTTPClient: f.client})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err = v.Verify(context.Background(), f.signed(claims(f.now), f.key))
	if err != nil || id.Subject != "pairwise-subject" {
		t.Fatalf("subject claim = %+v %v", id, err)
	}
}

func TestNewVerifierRejectsInvalidConfig(t *testing.T) {
	f := newTrustFixture(t)
	for _, cfg := range []VerificationConfig{
		{TenantID: strings.ToUpper(tenantID), Audience: audience, AllowedClientIDs: []string{clientID}, RequiredScope: "sessions.access", HTTPClient: f.client},
		{TenantID: tenantID, Audience: strings.ToUpper(audience), AllowedClientIDs: []string{clientID}, RequiredScope: "sessions.access", HTTPClient: f.client},
		{TenantID: tenantID, Audience: audience, AllowedClientIDs: []string{strings.ToUpper(clientID)}, RequiredScope: "sessions.access", HTTPClient: f.client},
		{TenantID: tenantID, Audience: audience, AllowedClientIDs: []string{clientID}, RequiredScope: "sessions access", HTTPClient: f.client},
	} {
		if _, err := NewVerifier(context.Background(), cfg); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}

func newTrustFixture(t *testing.T) *trustFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &trustFixture{key: key, other: other, now: time.Now().UTC().Truncate(time.Second)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/" + tenantID + "/v2.0/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": "https://login.microsoftonline.com/" + tenantID + "/discovery/v2.0/keys"})
		case "/" + tenantID + "/discovery/v2.0/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{jwk(key, "test-key")}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	local, _ := url.Parse(server.URL)
	base := server.Client().Transport
	f.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "login.microsoftonline.com" {
			return nil, context.Canceled
		}
		clone := r.Clone(r.Context())
		clone.URL.Host = local.Host
		return base.RoundTrip(clone)
	})}
	return f
}

func TestNewVerifierRejectsRedirectsAndOversizedResponses(t *testing.T) {
	f := newTrustFixture(t)
	discovery := "https://login.microsoftonline.com/" + tenantID + "/v2.0/.well-known/openid-configuration"
	cfg := VerificationConfig{TenantID: tenantID, Audience: audience, AllowedClientIDs: []string{clientID}, RequiredScope: "sessions.access"}
	cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery == "" {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {discovery + "?moved=1"}}, Body: http.NoBody, Request: r}, nil
		}
		return f.client.Transport.RoundTrip(r)
	})}
	if _, err := NewVerifier(context.Background(), cfg); err == nil {
		t.Fatal("followed discovery redirect")
	}
	pad := strings.Repeat("x", maxHTTPResponse)
	cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"issuer": issuer, "jwks_uri": "https://login.microsoftonline.com/" + tenantID + "/discovery/v2.0/keys", "pad": pad})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: -1, Request: r}, nil
	})}
	if _, err := NewVerifier(context.Background(), cfg); err == nil {
		t.Fatal("accepted oversized discovery response")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func claims(now time.Time) map[string]any {
	return map[string]any{"iss": issuer, "tid": tenantID, "aud": audience, "azp": clientID, "ver": "2.0", "scp": "other sessions.access extra", "oid": "object-id-a", "sub": "pairwise-subject", "exp": now.Add(time.Hour).Unix(), "nbf": now.Add(-time.Minute).Unix()}
}

func jwk(key *rsa.PrivateKey, kid string) map[string]any {
	return map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}

func (f *trustFixture) signed(claims map[string]any, key *rsa.PrivateKey) string {
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"}
	return rawJWT(header, claims, func(input string) []byte {
		sum := sha256.Sum256([]byte(input))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		if err != nil {
			panic(err)
		}
		return sig
	})
}

func rawJWT(header, payload map[string]any, sign func(string) []byte) string {
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(payload)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sig := ""
	if sign != nil {
		sig = base64.RawURLEncoding.EncodeToString(sign(input))
	}
	return input + "." + sig
}
