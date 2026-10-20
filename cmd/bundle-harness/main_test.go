package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/substrateruntime"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// portableAgentFixture keeps legacy actor compatibility independent of product examples.
func portableAgentFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: replay-fixture}\nspec:\n  instructions: Summarize the supplied synthetic document.\n  model: {name: qwen3:8b}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestActorServesExactDescriptorAndReleasesListeners(t *testing.T) {
	bundlePath := portableAgentFixture(t)
	for _, tc := range []struct{ name, profile, prefix, model string }{
		{"legacy", "1", "bundle-", ""},
		{"profile2", "2", "bundle-v2-", ""},
		{"profile3", "3", "bundle-v3-", ""},
		{"default", "", "bundle-v3-", ""},
		{"empty-model", "", "bundle-v3-", ""},
		{"offline", "3", "bundle-v3-", "qwen3:8b"},
		{"hosted-legacy", "1", "bundle-", "example-hosted-model"},
		{"hosted-profile2", "2", "bundle-v2-", "example-hosted-model"},
		{"hosted-profile3", "3", "bundle-v3-", "example-hosted-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := contract.Load(bundlePath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.model != "" {
				b, err = contract.ForModel(b, tc.model)
				if err != nil {
					t.Fatal(err)
				}
			}
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := lis.Addr().String()
			lis.Close()
			ready, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			readyAddr := ready.Addr().String()
			ready.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				args := []string{"-bundle", bundlePath, "-listen", address, "-ready-listen", readyAddr}
				if tc.profile != "" {
					args = append(args, "-profile", tc.profile)
				}
				if tc.model != "" || tc.name == "empty-model" {
					args = append(args, "--model", tc.model)
				}
				done <- serve(ctx, args, io.Discard)
			}()
			conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			deadline := time.Now().Add(5 * time.Second)
			for {
				call, stop := context.WithTimeout(ctx, 200*time.Millisecond)
				d, e := v1.NewHarnessClient(conn).Describe(call, &v1.DescribeRequest{})
				stop()
				if e == nil {
					if d.GetId() != tc.prefix+b.Digest || strings.Join(d.GetModels(), ",") != b.Model || d.GetCapabilities().GetResumability() != v1.Resumability_RESUMABILITY_STATELESS_REPLAY {
						t.Fatal("immutable descriptor lost")
					}
					break
				}
				select {
				case err := <-done:
					t.Fatalf("actor stopped before ready: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("actor not ready")
				}
				time.Sleep(20 * time.Millisecond)
			}
			response, err := http.Get("http://" + readyAddr + "/readyz")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("Substrate readiness requires HTTP 200, got %d", response.StatusCode)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			for _, a := range []string{address, readyAddr} {
				l, err := net.Listen("tcp", a)
				if err != nil {
					t.Fatal("listener leaked")
				}
				l.Close()
			}
		})
	}
}
func TestDescribeEmitsExactIdentityWithoutListening(t *testing.T) {
	bundlePath := portableAgentFixture(t)
	// Both addresses are occupied: description must never try to bind either.
	actor, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer actor.Close()
	ready, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	for _, profile := range []int{1, 2, 3} {
		for _, model := range []string{"", "qwen3:8b", "example-hosted-model"} {
			t.Run(fmt.Sprintf("profile%d/model=%s", profile, model), func(t *testing.T) {
				b, err := contract.Load(bundlePath)
				if err != nil {
					t.Fatal(err)
				}
				if model != "" {
					b, err = contract.ForModel(b, model)
					if err != nil {
						t.Fatal(err)
					}
				}
				descriptor, err := sessionhost.BundleDescriptor(b, profile)
				if err != nil {
					t.Fatal(err)
				}
				template, err := substrateruntime.TemplateName(b, profile)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				args := []string{"--bundle", bundlePath, "--profile", fmt.Sprint(profile), "--model", model, "--describe", "--listen", actor.Addr().String(), "--ready-listen", ready.Addr().String()}
				if err := serve(context.Background(), args, &out); err != nil {
					t.Fatal(err)
				}
				var got map[string]string
				decoder := json.NewDecoder(&out)
				if err := decoder.Decode(&got); err != nil {
					t.Fatal(err)
				}
				if len(got) != 3 || got["template"] != template || got["harness"] != descriptor.ID || got["model"] != b.Model {
					t.Fatalf("identity mismatch or unsafe extra fields: %v", got)
				}
				if err := decoder.Decode(new(any)); err != io.EOF {
					t.Fatalf("unexpected trailing output: %v", err)
				}
			})
		}
	}
}

func TestDescriptionRejectsInvalidModelProfileAndBundleWithoutOutput(t *testing.T) {
	bundlePath := portableAgentFixture(t)
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "model contains newline",
			args:    []string{"--bundle", bundlePath, "--model", "bad\nmodel"},
			wantErr: "invalid selected model",
		},
		{
			name:    "model exceeds length limit",
			args:    []string{"--bundle", bundlePath, "--model", strings.Repeat("x", 161)},
			wantErr: "invalid selected model",
		},
		{
			name:    "unsupported profile",
			args:    []string{"--bundle", bundlePath, "--profile", "4"},
			wantErr: "bundle-harness: native suite/profile 4 is local-only; remote actor bootstrap unsupported",
		},
		{
			name:    "missing bundle",
			args:    []string{"--bundle", filepath.Join(t.TempDir(), "missing.yaml")},
			wantErr: "bundle must be a bounded regular file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := serve(context.Background(), append(tc.args, "--describe"), &out)
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("error = %v, want %q", err, tc.wantErr)
			}
			if out.Len() != 0 {
				t.Errorf("invalid contract produced a deployment identity: %q", out.String())
			}
		})
	}
}

func TestActorRejectsUnknownProfileBeforeListening(t *testing.T) {
	if err := serve(context.Background(), []string{"-bundle", portableAgentFixture(t), "-profile", "4"}, io.Discard); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
