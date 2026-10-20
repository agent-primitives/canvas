package modelconfig

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const hostedConfig = `{"profile":"hosted","hosted":{"endpoint":"https://example-resource.openai.azure.com/openai/v1","model":"example-deployment"}}`

func writeConfig(t *testing.T, root, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "model.config.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultProfileIsOffline(t *testing.T) {
	root := t.TempDir()
	p, err := Load(root, "")
	if err != nil || p.Name != "offline" || p.Model != "qwen3:8b" {
		t.Fatal("missing configuration must default to offline")
	}
	writeConfig(t, root, `{"profile":"offline"}`)
	p, err = Load(root, "")
	if err != nil || p.Name != "offline" || p.Model != "qwen3:8b" {
		t.Fatal("explicit config must select offline")
	}
	if _, err = Load(root, "hosted"); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("hosted without endpoint and model must be a clear error, got %v", err)
	}
}

func TestShippedConfigIsOffline(t *testing.T) {
	// Check the shipped file, not a developer's ignored full-file override.
	data, err := os.ReadFile(filepath.Join("..", "..", "model.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeConfig(t, root, string(data))
	p, err := Load(root, "")
	if err != nil || p.Name != "offline" {
		t.Fatalf("shipped model.config.json must select offline: %v", err)
	}
}

func TestLocalConfigReplacesShippedFile(t *testing.T) {
	for _, shipped := range []string{"", `{broken`, `{"profile":"offline"}`, hostedConfig} {
		t.Run(shipped, func(t *testing.T) {
			root := t.TempDir()
			if shipped != "" {
				writeConfig(t, root, shipped)
			}
			if err := os.WriteFile(filepath.Join(root, "model.config.local.json"), []byte(hostedConfig), 0600); err != nil {
				t.Fatal(err)
			}
			p, err := Load(root, "")
			if err != nil || p.Name != "hosted" || p.Model != "example-deployment" {
				t.Fatalf("local file must replace shipped file: %+v %v", p, err)
			}
			p, err = Load(root, "offline")
			if err != nil || p.Name != "offline" || p.Hosted != (Hosted{}) {
				t.Fatalf("CLI profile override must still win: %+v %v", p, err)
			}
		})
	}
}

func TestLocalConfigDoesNotMergeHostedSettings(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, hostedConfig)
	local := filepath.Join(root, "model.config.local.json")
	for _, data := range []string{`{"profile":"hosted"}`, `{"profile":"offline"}`} {
		if err := os.WriteFile(local, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "hosted"); err == nil || !strings.Contains(err.Error(), local) || !strings.Contains(err.Error(), "endpoint") {
			t.Fatalf("local file must not inherit hosted settings, and error must name it: %v", err)
		}
		if p, err := Load(root, "offline"); err != nil || p.Name != "offline" {
			t.Fatalf("offline override must not require hosted settings: %+v %v", p, err)
		}
	}
	if p, err := Load(root, ""); err != nil || p.Name != "offline" {
		t.Fatalf("local offline must replace shipped hosted: %+v %v", p, err)
	}
}

func TestLocalConfigCLIHostedOverride(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `{"profile":"offline"}`)
	if err := os.WriteFile(filepath.Join(root, "model.config.local.json"), []byte(`{"profile":"offline","hosted":{"endpoint":"https://api.example.com/v1","model":"local-model","keyFile":"/synthetic/key","auth":"bearer"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(root, "hosted")
	if err != nil || p.Model != "local-model" || p.Hosted.KeyFile != "/synthetic/key" || p.Hosted.Auth != "bearer" {
		t.Fatalf("CLI hosted override must use local settings: %+v %v", p, err)
	}
}

func TestLocalConfigUnreadableFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of permission bits")
	}
	root := t.TempDir()
	writeConfig(t, root, `{"profile":"offline"}`)
	local := filepath.Join(root, "model.config.local.json")
	if err := os.WriteFile(local, []byte(`{"profile":"offline"}`), 0000); err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{"", "offline"} {
		if _, err := Load(root, override); err == nil || !strings.Contains(err.Error(), local) {
			t.Fatalf("unreadable local file must not fall back, even with override: %v", err)
		}
	}
}

func TestLocalConfigNonregularFailsClosed(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `{"profile":"offline"}`)
			local := filepath.Join(root, "model.config.local.json")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(filepath.Join(root, "model.config.json"), local)
			case "dangling":
				err = os.Symlink(filepath.Join(root, "absent"), local)
			case "directory":
				err = os.Mkdir(local, 0700)
			case "fifo":
				err = syscall.Mkfifo(local, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, override := range []string{"", "offline"} {
				if _, err := Load(root, override); err == nil || !strings.Contains(err.Error(), local) {
					t.Fatalf("nonregular local file must not fall back, even with override: %v", err)
				}
			}
		})
	}
}

func TestLocalConfigInvalidFailsClosedWithOverride(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `{"profile":"offline"}`)
	local := filepath.Join(root, "model.config.local.json")
	if err := os.WriteFile(local, []byte(`{"profile":"offline","unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "offline"); err == nil || !strings.Contains(err.Error(), local) || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("invalid local config must fail before CLI override, not fall back: %v", err)
	}
}

func TestHostedConfigDefaultsAndOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	writeConfig(t, root, hostedConfig)
	p, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Hosted{Endpoint: "https://example-resource.openai.azure.com/openai/v1", Model: "example-deployment", KeyFile: filepath.Join(home, ".config", "canvas", "hosted.key"), Auth: "api-key"}
	if p.Name != "hosted" || p.Model != "example-deployment" || p.Hosted != want {
		t.Fatalf("unexpected hosted profile %+v", p)
	}
	p, err = Load(root, "offline")
	if err != nil || p.Name != "offline" || p.Hosted != (Hosted{}) {
		t.Fatal("explicit CLI selection must override config")
	}
	writeConfig(t, root, `{"profile":"hosted","hosted":{"endpoint":"https://api.example.com/v1","model":"m","keyFile":"~/keys/k","auth":"bearer"}}`)
	p, err = Load(root, "")
	if err != nil || p.Hosted.KeyFile != filepath.Join(home, "keys", "k") || p.Hosted.Auth != "bearer" {
		t.Fatalf("keyFile and auth not applied: %+v %v", p, err)
	}
	p, err = p.WithKeyFile("/elsewhere/key")
	if err != nil || p.Hosted.KeyFile != "/elsewhere/key" {
		t.Fatal("key file override not applied")
	}
	offline, _ := Load(t.TempDir(), "")
	if _, err := offline.WithKeyFile("/k"); err == nil {
		t.Fatal("key file override accepted for offline")
	}
	// Incomplete hosted settings do not block an offline override.
	writeConfig(t, root, `{"profile":"hosted"}`)
	if p, err = Load(root, "offline"); err != nil || p.Name != "offline" {
		t.Fatal("offline override must not need hosted settings")
	}
}

func TestProfileConfigFailsClosed(t *testing.T) {
	for _, data := range []string{
		`{broken`, `{"profile":"unknown"}`, `{"profile":""}`, `{"profile":null}`, `{"profile":false}`, `{"profile":8}`, `{"profile":[]}`, `{"profile":{}}`,
		`{"profile":"hosted","key":"not-allowed"}`, `{"profile":"hosted","profile":"offline"}`, `{"profile":"hosted"} {"profile":"offline"}`, `{}`, `{"hosted":{}}`,
		`{"profile":"hosted"}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1"}}`,
		`{"profile":"hosted","hosted":{"model":"m"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"http://x.example/v1","model":"m"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://user:pw@x.example/v1","model":"m"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1?a=b","model":"m"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1","model":"m","apiKey":"secret"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1","model":"m","auth":"basic"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1","model":"m","keyFile":"relative/key"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1","model":"m","model":"n"}}`,
		`{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1","model":null}}`,
		`{"profile":"offline","hosted":null}`,
		`{"profile":"offline","hosted":{"endpoint":8}}`,
		`{"profile":"offline"}` + strings.Repeat(" ", 16384),
	} {
		for _, filename := range []string{"model.config.json", "model.config.local.json"} {
			root := t.TempDir()
			writeConfig(t, root, `{"profile":"offline"}`)
			path := filepath.Join(root, filename)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root, ""); err == nil || !strings.Contains(err.Error(), path) {
				t.Errorf("accepted invalid config %.80s or failed to name %s: %v", data, path, err)
			}
		}
	}
	if _, err := Load(t.TempDir(), "automatic-fallback"); err == nil {
		t.Fatal("accepted unknown override")
	}
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "model.config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, ""); err == nil {
		t.Fatal("accepted symlinked config")
	}
}
