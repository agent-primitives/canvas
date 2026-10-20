package sessionauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sourceSecret = "never-echo-bearer-secret"

func TestSourceConfigFileAndCommand(t *testing.T) {
	root := t.TempDir()
	expires := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(root, "token.json")
	raw, _ := json.Marshal(map[string]string{"accessToken": "fixture.token", "expiresAt": expires.Format(time.RFC3339)})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	src, err := (SourceConfig{File: path}).Open()
	if err != nil {
		t.Fatal(err)
	}
	token, until, err := src.Token(context.Background())
	if err != nil || token != "fixture.token" || !until.Equal(expires) {
		t.Fatalf("file Token() = %q %v %v", token, until, err)
	}
	argv := helperCommand(t, "ok")
	t.Setenv("SESSIONAUTH_HELPER", "1")
	src, err = (SourceConfig{Command: argv}).Open()
	if err != nil {
		t.Fatal(err)
	}
	token, until, err = src.Token(context.Background())
	if err != nil || token != "command.token" || !until.Equal(expires) {
		t.Fatalf("command Token() = %q %v %v", token, until, err)
	}
}

func TestSourceConfigRejectsBadInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "token.json")
	if err := os.WriteFile(path, []byte(`{"accessToken":"fixture.token","expiresAt":"2026-10-17T12:00:00Z"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil { // independent of the process umask
		t.Fatal(err)
	}
	for _, tc := range []SourceConfig{
		{},
		{File: path, Command: []string{"cmd"}},
		{Command: []string{}},
		{Command: []string{"", "arg"}},
		{Command: []string{"cmd", "Bearer raw-token"}},
	} {
		if _, err := tc.Open(); err == nil {
			t.Fatalf("accepted invalid config: %+v", tc)
		}
	}
	if _, err := (SourceConfig{File: path}).Open(); err == nil {
		t.Fatal("accepted file with loose permissions")
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (SourceConfig{File: link}).Open(); err == nil {
		t.Fatal("accepted symlink source file")
	}
}

func TestCommandSourceTimeoutBoundsAndRedacts(t *testing.T) {
	t.Setenv("SESSIONAUTH_HELPER", "1")
	for _, name := range []string{"stderr", "timeout", "huge", "malformed"} {
		t.Run(name, func(t *testing.T) {
			src, err := (SourceConfig{Command: helperCommand(t, name)}).Open()
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = src.Token(context.Background())
			if err == nil {
				t.Fatal("invalid command accepted")
			}
			if strings.Contains(err.Error(), sourceSecret) {
				t.Fatalf("stderr leaked secret: %v", err)
			}
		})
	}
}

func helperCommand(t *testing.T, scenario string) []string {
	t.Helper()
	return []string{os.Args[0], "-test.run=TestSessionauthHelperProcess", "--", scenario}
}

func TestSessionauthHelperProcess(t *testing.T) {
	if os.Getenv("SESSIONAUTH_HELPER") != "1" {
		return
	}
	args := os.Args
	for i := range args {
		if args[i] == "--" && i+1 < len(args) {
			switch args[i+1] {
			case "ok":
				fmt.Fprint(os.Stdout, `{"accessToken":"command.token","expiresAt":"2026-10-17T12:00:00Z"}`)
				os.Exit(0)
			case "stderr":
				fmt.Fprint(os.Stderr, sourceSecret)
				os.Exit(1)
			case "timeout":
				time.Sleep(commandTimeout + 200*time.Millisecond)
				os.Exit(0)
			case "huge":
				fmt.Fprint(os.Stdout, strings.Repeat("x", maxSourceJSON+1))
				os.Exit(0)
			case "malformed":
				fmt.Fprint(os.Stdout, `{"accessToken":true}`)
				os.Exit(0)
			}
		}
	}
	_, _ = fmt.Fprint(os.Stderr, "missing helper scenario")
	os.Exit(2)
}

func TestFileSourceRejectsMalformedJSON(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "token.json")
	for _, raw := range []string{
		`{"accessToken":true,"expiresAt":"2026-10-17T12:00:00Z"}`,
		`{"accessToken":"Bearer raw","expiresAt":"2026-10-17T12:00:00Z"}`,
		`{"accessToken":"fixture.token"}`,
		`{"accessToken":"fixture.token","expiresAt":"not-a-time"}`,
		`{"accessToken":"fixture.token","expiresAt":"2026-10-17T12:00:00Z","extra":true}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		src, err := (SourceConfig{File: path}).Open()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = src.Token(context.Background())
		if err == nil {
			t.Fatalf("accepted malformed source JSON: %s", raw)
		}
	}
	big := strings.Repeat("x", maxSourceJSON+1)
	if err := os.WriteFile(path, []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := (SourceConfig{File: path}).Open()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = src.Token(context.Background())
	if err == nil {
		t.Fatal("accepted oversized file")
	}
	if !errors.Is(err, errSourceInvalid) {
		t.Fatalf("want invalid source classification, got %v", err)
	}
}
