package sessionhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/model/openai"
	"golang.org/x/sys/unix"
)

// Ollama uses the upstream OpenAI-compatible provider at an explicitly named,
// keyless endpoint. An origin-only URL gets Ollama's /v1 base path.
func Ollama(baseURL, model string) (controller.ModelFunc, error) {
	if model == "" {
		model = "qwen3:8b"
	}
	return compatible(endpoint{label: "offline", base: baseURL, model: model})
}

// endpoint describes one OpenAI-compatible Chat Completions provider.
type endpoint struct {
	label     string // "hosted" or "offline", used in error messages
	base      string
	model     string
	keyFile   string // empty for keyless endpoints
	auth      string // "api-key" header or "bearer"; only used with keyFile
	https     bool   // require https
	reasoning bool   // GPT-5-style request shaping
}

func compatible(e endpoint) (controller.ModelFunc, error) {
	u, err := url.Parse(e.base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%s: endpoint must be an http(s) URL without credentials, query or fragment", e.label)
	}
	if e.https && u.Scheme != "https" {
		return nil, fmt.Errorf("%s: endpoint must use https", e.label)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1"
	}
	var key string
	if e.keyFile != "" {
		if key, err = readKeyFile(e.label, e.keyFile); err != nil {
			return nil, err
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// Model attempts must not be replayed implicitly on a reused connection.
	transport.DisableKeepAlives = true
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	client := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: boundedGenerationTransport{next: transport, reasoning: e.reasoning},
		// Never forward the key to a redirected endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	base := strings.TrimSuffix(u.String(), "/")
	return safeProvider(func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		selected := req.Model
		if selected == "" {
			selected = e.model
		}
		// Upstream treats the sample model "echo" as a placeholder. Binding its
		// fallback to the selected model preserves even that explicit model name.
		options := []openai.Option{openai.WithBaseURL(base), openai.WithModel(selected), openai.WithHTTPClient(client)}
		if e.auth == "api-key" {
			options = append(options, openai.WithHeader("api-key", key))
		} else {
			options = append(options, openai.WithAPIKey(key))
		}
		provider, err := openai.New(options...)
		if err != nil {
			return api.ModelResponse{}, fmt.Errorf("%s: cannot configure provider", e.label)
		}
		ctx = context.WithValue(ctx, responseFormatKey{}, req.Params["response_format"])
		ctx = context.WithValue(ctx, maxTokensKey{}, req.Params["max_tokens"])
		return provider.Model(ctx, req)
	}), nil
}

const maxKeySize = 16384

// readKeyFile reads an API key from a private regular file. The final path
// component must not be a symlink, and the file must not be readable by
// group or others.
func readKeyFile(label, path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return "", fmt.Errorf("%s: key file %s not found; put your API key there (chmod 600)", label, path)
		}
		if errors.Is(err, unix.ELOOP) {
			return "", fmt.Errorf("%s: key file %s is a symlink; point to the real file instead", label, path)
		}
		return "", fmt.Errorf("%s: cannot open key file %s", label, path)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: key file %s must be a regular file", label, path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("%s: key file %s is accessible by other users; run chmod 600 %s", label, path, path)
	}
	if info.Size() > maxKeySize {
		return "", fmt.Errorf("%s: key file %s is too large", label, path)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxKeySize+1))
	if err != nil {
		return "", fmt.Errorf("%s: cannot read key file %s", label, path)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || len(raw) > maxKeySize || strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("%s: key file %s must contain a single line with the API key", label, path)
	}
	return key, nil
}

// The upstream provider does not encode ModelRequest.Params, so the generation
// limits are applied to the outgoing HTTP request here. The harness hashes the
// same fixed Params on live runs and on replay.
type responseFormatKey struct{}
type maxTokensKey struct{}
type boundedGenerationTransport struct {
	next      http.RoundTripper
	reasoning bool
}

func (t boundedGenerationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, 16<<20))
	req.Body.Close()
	if err != nil {
		return nil, errors.New("model request: cannot read request body")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, errors.New("model request: invalid request body")
	}
	// Recorded profile-4 Params stay immutable for replay, while the live
	// provider uses the same stricter completion ceiling as the remote host.
	native := req.Context().Value(maxTokensKey{}) == "16384"
	budget := "700"
	if native {
		budget = "4096"
	}
	if t.reasoning {
		// GPT-5-style models take a completion-token budget and only their
		// default temperature.
		delete(body, "max_tokens")
		body["max_completion_tokens"] = json.RawMessage(budget)
		delete(body, "temperature")
	} else {
		body["max_tokens"] = json.RawMessage(budget)
		body["temperature"] = json.RawMessage("0.2")
	}
	format, _ := req.Context().Value(responseFormatKey{}).(string)
	if native {
		if err := validateDocumentFormat(format); err != nil {
			return nil, err
		}
	}
	body["response_format"] = json.RawMessage(`{"type":"json_object"}`)
	if format != "" {
		if len(format) > 16384 || !json.Valid([]byte(format)) {
			return nil, errors.New("model request: invalid recorded response format")
		}
		body["response_format"] = json.RawMessage(format)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("model request: cannot encode request body")
	}
	cp := req.Clone(req.Context())
	cp.Body = io.NopCloser(bytes.NewReader(encoded))
	cp.ContentLength = int64(len(encoded))
	cp.GetBody = nil
	resp, err := t.next.RoundTrip(cp)
	if native && err == nil {
		return bufferDocumentCompletion(resp)
	}
	return resp, err
}
