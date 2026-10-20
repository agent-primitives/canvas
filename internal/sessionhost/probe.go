package sessionhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agent-primitives/canvas/internal/modelconfig"
)

const probeTimeout = 10 * time.Second
const maxProbeBody = 1 << 20

var probeModelIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var probeCredentialToken = regexp.MustCompile(`(^|[^A-Za-z0-9_])sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}($|[^A-Za-z0-9_-])`)
var probeHostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)

// CheckModel checks only the selected provider's metadata endpoint. It never
// constructs a generation provider, verifies the model pin, or creates state.
func CheckModel(ctx context.Context, profile modelconfig.Profile, offlineBase string) (string, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	return checkModel(ctx, profile, offlineBase, transport)
}

func checkModel(ctx context.Context, profile modelconfig.Profile, offlineBase string, transport http.RoundTripper) (string, error) {
	base, field := offlineBase, "models"
	if profile.Name == "hosted" {
		base, field = profile.Hosted.Endpoint, "data"
	} else if profile.Name != "offline" {
		return "", errors.New("unsupported model profile; no automatic fallback")
	}
	u, err := probeURL(base, profile.Name == "hosted")
	if err != nil {
		return "", fmt.Errorf("%s: %w", profile.Name, err)
	}
	if profile.Name == "hosted" {
		// Match compatible's origin-only /v1 normalization without constructing it.
		if u.Path == "" || u.Path == "/" {
			u.Path = "/v1"
		}
		u.RawPath = strings.TrimSuffix(u.EscapedPath(), "/") + "/models"
		u.Path = strings.TrimSuffix(u.Path, "/") + "/models"
	} else {
		u.Path = "/api/tags"
		u.RawPath = ""
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%s: cannot configure endpoint check", profile.Name)
	}
	if profile.Name == "hosted" {
		key, err := readKeyFile("hosted", profile.Hosted.KeyFile)
		if err != nil {
			return "", err
		}
		for _, ch := range key {
			if ch < 33 || ch > 126 {
				return "", errors.New("hosted: key file must contain a printable API key without whitespace")
			}
		}
		if profile.Hosted.Auth == "api-key" {
			req.Header.Set("api-key", key)
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(req)
	if err != nil {
		return "", probeFailure(ctx, profile.Name)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", fmt.Errorf("%s: metadata endpoint HTTP %d; credentials rejected", profile.Name, response.StatusCode)
	case http.StatusNotFound:
		return "", fmt.Errorf("%s: metadata endpoint HTTP 404; route unsupported, endpoint unverified", profile.Name)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("%s: metadata endpoint HTTP %d; endpoint unverified", profile.Name, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBody+1))
	if err != nil {
		return "", probeFailure(ctx, profile.Name)
	}
	if len(body) > maxProbeBody {
		return "", fmt.Errorf("%s: metadata response exceeds 1 MiB; endpoint unverified", profile.Name)
	}
	var envelope map[string]json.RawMessage
	var entries []json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || json.Unmarshal(envelope[field], &entries) != nil || entries == nil {
		return "", fmt.Errorf("%s: invalid metadata JSON; endpoint unverified", profile.Name)
	}
	model := profile.Model
	if len(model) > 200 || !probeModelIdentifier.MatchString(model) || probeCredentialToken.MatchString(model) {
		model = "(configured)"
	}
	return fmt.Sprintf("Model endpoint reachable (profile=%s, selected model=%s). Metadata only; inference and model availability not verified.", profile.Name, model), nil
}

func probeFailure(ctx context.Context, label string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s: endpoint check timed out; endpoint unverified", label)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s: endpoint check canceled; endpoint unverified", label)
	}
	return fmt.Errorf("%s: endpoint unreachable (transport or TLS failure); endpoint unverified", label)
}

// Reject credential-bearing and malformed URLs before reading a key or sending
// a request. No URL or transport/body detail is included in operational errors.
func probeURL(base string, hosted bool) (*url.URL, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(base, "#") {
		return nil, errors.New("endpoint must be an http(s) URL without credentials, query or fragment")
	}
	if hosted && u.Scheme != "https" {
		return nil, errors.New("endpoint must use https")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("endpoint has an invalid port")
		}
	}
	if net.ParseIP(u.Hostname()) == nil {
		for _, label := range strings.Split(strings.TrimSuffix(u.Hostname(), "."), ".") {
			if len(label) > 63 || !probeHostLabel.MatchString(label) {
				return nil, errors.New("endpoint has an invalid host")
			}
		}
	}
	return u, nil
}
