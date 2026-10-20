package sessionhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/modelconfig"
)

const probeSecret = "synthetic-probe-secret"

func probeProfile(t *testing.T, base, auth string) modelconfig.Profile {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte(probeSecret), 0600); err != nil {
		t.Fatal(err)
	}
	return modelconfig.Profile{Name: "hosted", Model: "custom-deployment", Hosted: modelconfig.Hosted{Endpoint: base, Model: "custom-deployment", KeyFile: key, Auth: auth}}
}

func TestCheckModelHostedOnlyGetsMetadataWithSelectedAuth(t *testing.T) {
	for _, auth := range []string{"api-key", "bearer"} {
		for _, path := range []string{"", "/", "/custom/v1/", "/custom%2Fv1/"} {
			t.Run(auth+path, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					wantPath := "/v1/models"
					if path == "/custom/v1/" || path == "/custom%2Fv1/" {
						wantPath = strings.TrimSuffix(path, "/") + "/models"
					}
					if r.Method != http.MethodGet || r.URL.EscapedPath() != wantPath || r.ContentLength != 0 {
						t.Error("probe must only GET configured metadata route without request body")
					}
					if auth == "api-key" {
						if r.Header.Get("api-key") != probeSecret || r.Header.Get("Authorization") != "" {
							t.Error("incorrect api-key auth")
						}
					} else if r.Header.Get("Authorization") != "Bearer "+probeSecret || r.Header.Get("api-key") != "" {
						t.Error("incorrect bearer auth")
					}
					fmt.Fprint(w, `{"object":"list","data":[{"id":"different-deployment"}]}`)
				}))
				defer server.Close()
				report, err := checkModel(context.Background(), probeProfile(t, server.URL+path, auth), "http://127.0.0.1:1", server.Client().Transport)
				if err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 || !strings.Contains(report, "custom-deployment") || !strings.Contains(report, "hosted") || !strings.Contains(report, "inference and model availability not verified") {
					t.Fatalf("incorrect metadata-only report: %q", report)
				}
				if strings.Contains(report, probeSecret) || strings.Contains(report, server.URL) || strings.Contains(report, "different-deployment") {
					t.Fatal("response or endpoint leaked")
				}
			})
		}
	}
}

func TestCheckModelAcceptsBoundedLargeCatalogWithoutLoggingEntries(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Error("diagnostic must remain metadata-only")
		}
		fmt.Fprint(w, `{"data":[{"id":"`+probeSecret+`","description":"`+strings.Repeat("x", 256<<10)+`"}]}`)
	}))
	defer server.Close()
	report, err := checkModel(context.Background(), probeProfile(t, server.URL, "api-key"), "", server.Client().Transport)
	if err != nil || !strings.Contains(report, "Metadata only") || strings.Contains(report, probeSecret) {
		t.Fatalf("bounded catalog rejected or leaked: report=%q err=%v", report, err)
	}
}

func TestCheckModelSafeMetadataFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"unauthorized", 401, probeSecret, "credentials rejected"},
		{"forbidden", 403, probeSecret, "credentials rejected"},
		{"unsupported", 404, probeSecret, "unsupported"},
		{"server", 503, probeSecret, "HTTP 503"},
		{"html", 200, "<html>" + probeSecret + "</html>", "metadata"},
		{"missing", 200, `{"secret":"` + probeSecret + `"}`, "metadata"},
		{"null", 200, `{"data":null}`, "metadata"},
		{"wrong", 200, `{"data":{}}`, "metadata"},
		{"trailing", 200, `{"data":[]} ` + probeSecret, "metadata"},
		{"too-large", 200, `{"data":[],"padding":"` + strings.Repeat("x", 1<<20) + `"}`, "1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/v1/models" {
					t.Error("generation or unexpected route")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			report, err := checkModel(context.Background(), probeProfile(t, server.URL, "api-key"), "", server.Client().Transport)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), probeSecret) || strings.Contains(err.Error(), server.URL) || report != "" || calls.Load() != 1 {
				t.Fatalf("unsafe or incorrect failure: report=%q err=%v calls=%d", report, err, calls.Load())
			}
		})
	}
}

func TestCheckModelOfflineMetadataDoesNotVerifyPinOrAvailability(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/tags" || r.Header.Get("Authorization") != "" || r.Header.Get("api-key") != "" {
			t.Error("offline probe must only GET origin tags without credentials")
		}
		fmt.Fprint(w, `{"models":[{"name":"different-model","digest":"not-the-pin"}]}`)
	}))
	defer server.Close()
	report, err := CheckModel(context.Background(), modelconfig.Profile{Name: "offline", Model: "qwen3:8b"}, server.URL+"/v1")
	if err != nil || calls.Load() != 1 || !strings.Contains(report, "qwen3:8b") || !strings.Contains(report, "not verified") {
		t.Fatalf("report=%q err=%v", report, err)
	}
}

func TestCheckModelDisablesProxyAndRedirect(t *testing.T) {
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trapped.Add(1)
		t.Error("credential forwarded to proxy or redirect")
	}))
	defer trap.Close()
	trapURL, _ := url.Parse(trap.URL)
	old := http.DefaultTransport
	transport := old.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) { return trapURL, nil }
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("non-GET probe")
		}
		http.Redirect(w, r, trap.URL+"/"+probeSecret, http.StatusFound)
	}))
	defer server.Close()
	_, err := CheckModel(context.Background(), modelconfig.Profile{Name: "offline", Model: "qwen3:8b"}, server.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") || strings.Contains(err.Error(), probeSecret) || trapped.Load() != 0 {
		t.Fatalf("unsafe redirect/proxy: %v", err)
	}
}

type probeTransportFunc func(*http.Request) (*http.Response, error)

func (f probeTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheckModelRejectsUnsafeURLsBeforeRequests(t *testing.T) {
	for _, base := range []string{"https://user:" + probeSecret + "@example.invalid", "https://example.invalid?secret=" + probeSecret, "https://example.invalid#" + probeSecret, "https://:443", "https://[bad]", "https://bad..host", "https://example.invalid:99999", "http://example.invalid", "https://example.invalid?", "https://example.invalid#"} {
		t.Run(base, func(t *testing.T) {
			_, err := checkModel(context.Background(), probeProfile(t, base, "api-key"), "", probeTransportFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unsafe endpoint requested")
				return nil, errors.New(probeSecret)
			}))
			if err == nil || strings.Contains(err.Error(), probeSecret) || strings.Contains(err.Error(), base) {
				t.Fatalf("unsafe URL error: %v", err)
			}
		})
	}
}

func TestCheckModelSanitizesTransportErrorAndUnsafeModel(t *testing.T) {
	p := probeProfile(t, "https://example.invalid/v1", "api-key")
	_, err := checkModel(context.Background(), p, "", probeTransportFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("probe lacks ten-second bound")
		}
		return nil, errors.New(probeSecret)
	}))
	if err == nil || !strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), probeSecret) {
		t.Fatalf("unsafe transport error: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"models":[]}`) }))
	defer server.Close()
	for _, model := range []string{"unsafe\x1b[31m", strings.Repeat("x", 201)} {
		report, err := CheckModel(context.Background(), modelconfig.Profile{Name: "offline", Model: model}, server.URL)
		if err != nil || strings.Contains(report, model) || !strings.Contains(report, "not verified") {
			t.Fatalf("unsafe model output: %q %v", report, err)
		}
	}
}

func TestCheckModelCancellationTimeoutAndRefusedConnection(t *testing.T) {
	for _, stage := range []string{"headers", "body", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("non-GET probe")
				}
				if stage == "body" {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			want := "timed out"
			if stage == "canceled" {
				cancel()
				want = "canceled"
			}
			_, err := CheckModel(ctx, modelconfig.Profile{Name: "offline", Model: "qwen3:8b"}, server.URL)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("incorrect bounded failure: %v", err)
			}
		})
	}
	server := httptest.NewServer(http.NotFoundHandler())
	base := server.URL
	server.Close()
	_, err := CheckModel(context.Background(), modelconfig.Profile{Name: "offline", Model: "qwen3:8b"}, base)
	if err == nil || !strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), base) {
		t.Fatalf("unsafe refused transport error: %v", err)
	}
}

func TestCheckModelDoesNotPrintCredentialShapedConfiguredModels(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()
	for _, model := range []string{"sk-" + strings.Repeat("A", 48), "sk-proj-" + strings.Repeat("B", 48), "vendor/sk-svcacct-" + strings.Repeat("C", 48)} {
		p := probeProfile(t, server.URL, "api-key")
		p.Model, p.Hosted.Model = model, model
		report, err := checkModel(context.Background(), p, "", server.Client().Transport)
		if err != nil || strings.Contains(report, model) || !strings.Contains(report, "selected model=(configured)") {
			t.Fatalf("credential-shaped configured model was not redacted: %q %v", report, err)
		}
	}
	p := probeProfile(t, server.URL, "api-key")
	p.Model, p.Hosted.Model = "high-risk-high-reward-deployment", "high-risk-high-reward-deployment"
	report, err := checkModel(context.Background(), p, "", server.Client().Transport)
	if err != nil || !strings.Contains(report, p.Model) {
		t.Fatalf("ordinary hyphenated model was incorrectly redacted: %q %v", report, err)
	}
}

func TestCheckModelKeepsPrivateKeyFilePolicy(t *testing.T) {
	p := probeProfile(t, "https://example.invalid/v1", "api-key")
	transport := probeTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unsafe key requested")
		return nil, errors.New(probeSecret)
	})
	if err := os.Chmod(p.Hosted.KeyFile, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := checkModel(context.Background(), p, "", transport)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), p.Hosted.KeyFile) || strings.Contains(err.Error(), probeSecret) {
		t.Fatalf("key security bypassed: %v", err)
	}
}
