package contract

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
)

func suiteArchive(t *testing.T) (Bundle, executionArchive) {
	t.Helper()
	b, err := LoadSuite("local-writer", northwindRef(t, "writer"))
	if err != nil {
		t.Fatal(err)
	}
	var a executionArchive
	if err := json.Unmarshal(b.Source, &a); err != nil {
		t.Fatal(err)
	}
	return b, a
}
func TestSuiteArchiveTamperingNeverTrusted(t *testing.T) {
	b, a := suiteArchive(t)
	for _, field := range []string{"digest", "content", "graph", "member", "memberDigest", "composition", "instructions", "model", "source", "authored"} {
		t.Run(field, func(t *testing.T) {
			candidate := b
			identity := *b.Suite
			identity.Compositions = append([]agentsuite.CompositionSelection{}, identity.Compositions...)
			candidate.Suite = &identity
			switch field {
			case "digest":
				candidate.Suite.Digest = "sha256:" + strings.Repeat("0", 64)
			case "content":
				candidate.Suite.ContentDigest = "forged"
			case "graph":
				candidate.Suite.GraphDigest = "forged"
			case "member":
				candidate.Suite.Member = "forged"
			case "memberDigest":
				candidate.Suite.MemberDigest = "forged"
			case "composition":
				candidate.Suite.Compositions[0].BuildProfile = "forged"
			case "instructions":
				candidate.Instructions = "credential-canary"
			case "model":
				candidate.Model = "forged"
			case "source":
				candidate.Source = append(append([]byte{}, b.Source...), '\n')
			case "authored":
				candidate.AuthoredDigest = strings.Repeat("0", 64)
			}
			_, err := ForModel(candidate, b.Model)
			if err == nil || strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("tampering accepted/leaked", err)
			}
		})
	}
	original := a.Directory["instructions/writer.md"]
	original.Data = []byte("credential-canary")
	a.Directory["instructions/writer.md"] = original
	data, _ := json.Marshal(a)
	if _, err := Parse(data); err == nil || strings.Contains(err.Error(), "credential-canary") {
		t.Fatal("archive material not revalidated", err)
	}
	data = bytes.Replace(b.Source, []byte(`"id":"local-writer"`), []byte(`"id":"local-writer","id":"other"`), 1)
	if _, err := Parse(data); err == nil {
		t.Fatal("duplicate archive field accepted")
	}
	if _, err := Parse(append(b.Source, bytes.Repeat([]byte(" "), maxArchive)...)); err == nil {
		t.Fatal("oversize archive accepted")
	}
}

func TestSuiteArchiveRefusesWritableRetainedModes(t *testing.T) {
	b, _ := suiteArchive(t)
	for _, view := range []string{"directory", "artifact"} {
		for _, mode := range []uint32{0664, 0646} {
			t.Run(fmt.Sprintf("%s/%04o", view, mode), func(t *testing.T) {
				var archive executionArchive
				if err := json.Unmarshal(b.Source, &archive); err != nil {
					t.Fatal(err)
				}
				files := archive.Directory
				name := "agentsuite.json"
				if view == "artifact" {
					files = archive.Artifact
					name = "index.json"
				}
				file, ok := files[name]
				if !ok {
					t.Fatal("missing fixture file", name)
				}
				file.Mode = mode
				files[name] = file
				data, err := json.Marshal(archive)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Parse(data); err == nil {
					t.Fatal("archive with writable retained mode accepted")
				}
			})
		}
	}
}

func TestRetainedLimitsAggregateAcrossBothViewsAndGzipStreams(t *testing.T) {
	makeArchive := func() executionArchive {
		return executionArchive{Directory: map[string]retainedFile{"one": {Mode: 0600}}, Artifact: map[string]retainedFile{"two": {Mode: 0600}}}
	}
	a := makeArchive()
	if err := checkRetained(a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		a.Directory[fmt.Sprintf("raw%d", i)] = retainedFile{Mode: 0600, Data: bytes.Repeat([]byte("r"), 1<<20)}
	}
	if err := checkRetained(a); err != nil {
		t.Fatal("exact raw limit refused", err)
	}
	a.Artifact["two"] = retainedFile{Mode: 0600, Data: []byte("x")}
	if checkRetained(a) == nil {
		t.Fatal("combined raw cap ignored")
	}
	a = makeArchive()
	for i := 0; i < 127; i++ {
		a.Directory[fmt.Sprintf("file%d", i)] = retainedFile{Mode: 0600}
	}
	if checkRetained(a) == nil {
		t.Fatal("combined file count ignored")
	}
	gzipBytes := func(size int) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(bytes.Repeat([]byte("x"), size)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	a = makeArchive()
	a.Directory["one"] = retainedFile{Mode: 0600, Data: gzipBytes(4 << 20)}
	a.Artifact["two"] = retainedFile{Mode: 0600, Data: gzipBytes(4 << 20)}
	if err := checkRetained(a); err != nil {
		t.Fatal("exact aggregate gzip cap refused", err)
	}
	a.Artifact["two"] = retainedFile{Mode: 0600, Data: gzipBytes((4 << 20) + 1)}
	if checkRetained(a) == nil {
		t.Fatal("combined expanded cap ignored")
	}
	a = makeArchive()
	a.Directory["one"] = retainedFile{Mode: 0600, Data: append(gzipBytes(4<<20), gzipBytes((4<<20)+1)...)}
	if checkRetained(a) == nil {
		t.Fatal("concatenated gzip escaped cap")
	}
	a.Directory["one"] = retainedFile{Mode: 0600, Data: []byte{0x1f, 0x8b, 0x00}}
	if checkRetained(a) == nil {
		t.Fatal("malformed gzip ignored")
	}
}

func TestDirectorySupplementAndSpecialFileRefusals(t *testing.T) {
	ref := northwindRef(t, "writer")
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(ref.Directory)); err != nil {
		t.Fatal(err)
	}
	ref.Directory = root
	if err := os.WriteFile(filepath.Join(root, "extra.json"), []byte(`{"fictional":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentsuite.ValidateDirectory(root); err != nil {
		t.Fatal("supplement must be otherwise conformant", err)
	}
	if _, err := LoadSuite("writer", ref); err == nil {
		t.Fatal("full inventory mismatch accepted")
	}
	os.Remove(filepath.Join(root, "extra.json"))
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite("writer", ref); err == nil {
		t.Fatal("special file accepted")
	}
}

func TestSuiteModelOnlyPolicyRefusesUnsupportedFeatures(t *testing.T) {
	_, archive := suiteArchive(t)
	var agent agentsuite.Agent
	if err := agentsuite.Unmarshal(archive.Directory["agents/writer.json"].Data, &agent); err != nil {
		t.Fatal(err)
	}
	if !modelOnlyAgent(agent) {
		t.Fatal("fixture is not model-only")
	}
	for _, mutate := range []func(*agentsuite.Agent){
		func(a *agentsuite.Agent) { a.Model.Protocol = "other" },
		func(a *agentsuite.Agent) { a.Model.Model = "" },
		func(a *agentsuite.Agent) { a.Model.Model = strings.Repeat("x", 161) },
		func(a *agentsuite.Agent) { a.Model.Model = "model\ncredential-canary" },
		func(a *agentsuite.Agent) { a.Model.EndpointEnv = "CREDENTIAL_CANARY" },
		func(a *agentsuite.Agent) { a.Model.SecretRefs = []string{"credential-canary"} },
		func(a *agentsuite.Agent) { a.ToolProviders = []agentsuite.ToolProviderRequirement{{ID: "tool"}} },
		func(a *agentsuite.Agent) { a.Invokes = []agentsuite.AgentInvoke{{Agent: "other"}} },
		func(a *agentsuite.Agent) { a.Extensions = []agentsuite.Extension{{Name: "extra"}} },
	} {
		a := agent
		mutate(&a)
		if modelOnlyAgent(a) {
			t.Fatal("unsupported model policy accepted")
		}
	}
	raw := bytes.Replace(archive.Directory["agents/writer.json"].Data, []byte(`"id":"writer"`), []byte(`"id":"writer","environment":{"SECRET":"credential-canary"}`), 1)
	if agentsuite.Unmarshal(raw, &agent) == nil {
		t.Fatal("unknown executable fields accepted")
	}
}

func TestLegacyJSONAndHarnessProfilesRemainUnchanged(t *testing.T) {
	data := []byte(`{"apiVersion":"kmx.kaimahi.dev/v1alpha1","kind":"PortableAgent","metadata":{"name":"scout"},"spec":{"instructions":"SuiteExecution","model":{"name":"qwen3:8b"}}}`)
	b, err := Parse(data)
	if err != nil {
		t.Fatal("legacy JSON mistaken for archive", err)
	}
	if Profile(b) != 3 || !reflect.DeepEqual(Profiles(b), []int{1, 2, 3}) {
		t.Fatal("legacy profiles lost")
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, append([]byte(valid), bytes.Repeat([]byte("#x\n"), 22000)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("legacy size limit widened")
	}
}
