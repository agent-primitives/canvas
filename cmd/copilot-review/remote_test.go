package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteConfigRejectsSecretOrInsecureShapes(t *testing.T) {
	for _, raw := range []string{
		`{"endpoint":"http://localhost:8080","caFile":"ca.pem","tokenSource":{"file":"token.json"}}`,
		`{"endpoint":"127.0.0.1:8080","tokenSource":{"file":"token.json"}}`,
		`{"endpoint":"127.0.0.1:8080","caFile":"ca.pem","insecureSkipVerify":true}`,
		`{"endpoint":"127.0.0.1:8080","caFile":"ca.pem","token":"super-secret-token"}`,
		`{} {}`,
	} {
		p := filepath.Join(t.TempDir(), "remote.json")
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readRemoteConfig(p)
		if err == nil {
			t.Fatalf("unsafe config accepted: %s", raw)
		}
		if strings.Contains(err.Error(), "super-secret-token") {
			t.Fatal("config error leaked secret")
		}
	}
}

func TestRemoteConfigRequiresSeparateState(t *testing.T) {
	if err := validateRemoteState("remote.json", ".state/copilot-northwind", true); err == nil {
		t.Fatal("remote mixed with unauthenticated demo state")
	}
	if err := validateRemoteState("remote.json", filepath.Join(t.TempDir(), "remote"), false); err == nil {
		t.Fatal("implicit remote state accepted")
	}
	if err := validateRemoteState("remote.json", filepath.Join(t.TempDir(), "remote"), true); err != nil {
		t.Fatal(err)
	}
}
