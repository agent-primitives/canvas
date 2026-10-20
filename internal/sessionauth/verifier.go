package sessionauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

var guidLower = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// VerificationConfig pins one Entra tenant and the accepted access-token shape.
type VerificationConfig struct {
	TenantID         string   `json:"tenantID"`
	Audience         string   `json:"audience"`
	AllowedClientIDs []string `json:"allowedClientIDs"`
	RequiredScope    string   `json:"requiredScope"`
	// SubjectClaim is "oid" (default) or "sub".
	SubjectClaim string `json:"subjectClaim,omitempty"`
	// HTTPClient is a code-only transport override for offline tests. Only its
	// Transport is used; redirects, plain HTTP, and oversized bodies stay refused.
	HTTPClient *http.Client `json:"-"`
}

type verifierImpl struct {
	issuer, tenantID, audience, scope, subject string
	clients                                    []string
	now                                        func() time.Time
	idv                                        *oidc.IDTokenVerifier
}

// NewVerifier discovers the pinned tenant's v2.0 issuer and signing keys.
func NewVerifier(ctx context.Context, c VerificationConfig) (Verifier, error) {
	issuer, subject, err := validateVerificationConfig(c)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, trustedClient(c.HTTPClient)), issuer)
	if err != nil {
		return nil, errors.New("sessionauth: verifier discovery failed")
	}
	// Expiry is checked below without go-oidc's leeway; go-oidc still checks
	// signature, algorithm, issuer, and audience membership.
	idv := provider.Verifier(&oidc.Config{ClientID: c.Audience, SkipExpiryCheck: true, SupportedSigningAlgs: []string{oidc.RS256}})
	return &verifierImpl{issuer: issuer, tenantID: c.TenantID, audience: c.Audience, scope: c.RequiredScope, subject: subject,
		clients: slices.Clone(c.AllowedClientIDs), now: time.Now, idv: idv}, nil
}

func (v *verifierImpl) Verify(ctx context.Context, raw string) (Identity, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, time.Time{}, err
	}
	tok, err := v.idv.Verify(ctx, raw)
	if err != nil {
		return Identity{}, time.Time{}, unauthenticated()
	}
	var c map[string]json.RawMessage
	if tok.Claims(&c) != nil {
		return Identity{}, time.Time{}, unauthenticated()
	}
	now := v.now()
	exp, expOK := unixClaim(c, "exp")
	ok := expOK && exp.After(now) &&
		str(c, "iss") == v.issuer && str(c, "tid") == v.tenantID && str(c, "aud") == v.audience &&
		slices.Contains(v.clients, str(c, "azp")) && str(c, "ver") == "2.0" &&
		slices.Contains(strings.Fields(str(c, "scp")), v.scope)
	if _, present := c["idtyp"]; present && str(c, "idtyp") != "user" {
		ok = false
	}
	nbf, nbfOK := unixClaim(c, "nbf")
	ok = ok && nbfOK && !nbf.After(now)
	subject := str(c, v.subject)
	if !ok || !plainWord(subject) {
		return Identity{}, time.Time{}, unauthenticated()
	}
	return Identity{Issuer: v.issuer, Subject: subject}, exp, nil
}

func validateVerificationConfig(c VerificationConfig) (issuer, subject string, err error) {
	invalid := errors.New("sessionauth: invalid verification config")
	if !guidLower.MatchString(c.TenantID) || !guidLower.MatchString(c.Audience) || len(c.AllowedClientIDs) == 0 ||
		!plainWord(c.RequiredScope) ||
		slices.ContainsFunc(c.AllowedClientIDs, func(id string) bool { return !guidLower.MatchString(id) }) {
		return "", "", invalid
	}
	switch subject = c.SubjectClaim; subject {
	case "":
		subject = "oid"
	case "oid", "sub":
	default:
		return "", "", invalid
	}
	return "https://login.microsoftonline.com/" + c.TenantID + "/v2.0", subject, nil
}

// Keep the delegated access-token claim rules aligned with the host's entraauth verifier.
func plainWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r <= 32 || r == 127 {
			return false
		}
	}
	return true
}

// str returns a string claim, or "" when absent or not a JSON string.
func str(c map[string]json.RawMessage, name string) string {
	var s string
	if json.Unmarshal(c[name], &s) != nil {
		return ""
	}
	return s
}

func unixClaim(c map[string]json.RawMessage, name string) (time.Time, bool) {
	var n int64
	if json.Unmarshal(c[name], &n) != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}

// trustedClient wraps only the supplied transport: HTTPS only, no redirects,
// bounded request time, and bounded response bodies for discovery and JWKS.
func trustedClient(c *http.Client) *http.Client {
	base := http.DefaultTransport
	if c != nil && c.Transport != nil {
		base = c.Transport
	}
	return &http.Client{
		Transport: boundedTransport{base},
		Timeout:   httpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("sessionauth: redirects are not allowed")
		},
	}
}

type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, errors.New("sessionauth: https is required")
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil {
		// Reads past the limit fail with *http.MaxBytesError.
		resp.Body = http.MaxBytesReader(nil, resp.Body, maxHTTPResponse)
	}
	return resp, err
}
