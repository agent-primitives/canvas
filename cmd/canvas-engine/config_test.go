package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig(t *testing.T) config {
	t.Helper()
	root := t.TempDir()
	return config{Version: 1, AuthMode: "off", TrustedNetwork: true, InstanceID: "hosted-one", Listen: "127.0.0.1:0", Origin: "https://127.0.0.1:18443", ReadinessAddress: "127.0.0.1:0", StateDirectory: filepath.Join(root, "state"), DefinitionFile: filepath.Join(root, "canvas.yaml"), SessionsEndpoint: "agents-sessions.default.svc.cluster.local:8080", SessionsCAFile: filepath.Join(root, "ca.crt"), TLSCertFile: filepath.Join(root, "tls.crt"), TLSKeyFile: filepath.Join(root, "tls.key")}
}

func TestClosedConfig(t *testing.T) {
	cfg := validConfig(t)
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "engine.json")
	check := func(raw []byte, good bool) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readConfig(path)
		if (err == nil) != good {
			t.Fatalf("accepted=%v want %v: %s (%v)", err == nil, good, raw, err)
		}
	}
	check(raw, true)
	for _, bad := range []string{
		strings.Replace(string(raw), `"version":1`, `"version":null`, 1),
		strings.Replace(string(raw), `"version":1`, `"Version":1`, 1),
		strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(raw), `"version":1`, `"version":1,"credential":"secret"`, 1),
		strings.Replace(string(raw), `"trustedNetwork":true`, `"trustedNetwork":"true"`, 1),
		strings.Replace(string(raw), `"trustedNetwork":true,`, ``, 1),
		string(raw) + ` {}`, `null`, `[]`, strings.Repeat(" ", 65537),
	} {
		check([]byte(bad), false)
	}
	for _, mutate := range []func(*config){
		func(c *config) { c.Version = 2 }, func(c *config) { c.AuthMode = "" }, func(c *config) { c.TrustedNetwork = false }, func(c *config) { c.InstanceID = strings.Repeat("a", 64) }, func(c *config) { c.InstanceID = "UPPER" },
		func(c *config) { c.Listen = "0.0.0.0:8443" }, func(c *config) { c.Listen = "localhost:8443" }, func(c *config) { c.Listen = "127.0.0.1:http" }, func(c *config) { c.ReadinessAddress = "127.0.0.1:8443"; c.Listen = c.ReadinessAddress },
		func(c *config) { c.Origin = "http://127.0.0.1:8443" }, func(c *config) { c.Origin = "https://127.0.0.1:0" }, func(c *config) { c.Origin = "https://127.0.0.1:8443/" },
		func(c *config) { c.StateDirectory = "relative" },
		func(c *config) { c.StateDirectory += "/.." }, func(c *config) { c.SessionsEndpoint = "host.example:8080" }, func(c *config) { c.SessionsEndpoint = "127.0.0.1:0" }, func(c *config) { c.SessionsEndpoint = "dns:///agents.default.svc.cluster.local:8080" },
	} {
		c := cfg
		mutate(&c)
		b, _ := json.Marshal(c)
		check(b, false)
	}
	if _, err := readConfig(t.TempDir()); err == nil {
		t.Fatal("directory config accepted")
	}
}
