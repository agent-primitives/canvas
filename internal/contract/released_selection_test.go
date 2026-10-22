package contract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agent-primitives/suite/core"
)

func repackedRelease(t *testing.T, mutate func(map[string]retainedFile, *core.Suite)) SuiteRef {
	t.Helper()
	ref := releasedNorthwind(t)
	files, err := capture(ref.Layout)
	if err != nil {
		t.Fatal(err)
	}
	content, _, err := releaseContent(files, ref.Ref)
	if err != nil {
		t.Fatal(err)
	}
	var suite core.Suite
	if core.Unmarshal(content["agentsuite.json"].Data, &suite) != nil {
		t.Fatal("suite")
	}
	mutate(content, &suite)
	raw, err := core.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	content["agentsuite.json"] = retainedFile{Data: raw, Mode: 0644}
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(content))
	for name := range content {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file := content[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(file.Mode), Size: int64(len(file.Data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(file.Data); err != nil {
			t.Fatal(err)
		}
	}
	if tw.Close() != nil || gz.Close() != nil {
		t.Fatal("tar close")
	}
	descriptor := func(raw []byte, media string) releaseDescriptor {
		sum := sha256.Sum256(raw)
		return releaseDescriptor{MediaType: media, Digest: fmt.Sprintf("sha256:%x", sum), Size: int64(len(raw))}
	}
	config := []byte(`{}`)
	layer := buffer.Bytes()
	cfgDesc, layerDesc := descriptor(config, core.MediaTypeEmptyConfig), descriptor(layer, core.MediaTypeContent)
	manifest, _ := json.Marshal(releaseManifest{SchemaVersion: 2, MediaType: releaseManifestType, ArtifactType: core.MediaTypeArtifact, Config: cfgDesc, Layers: []releaseDescriptor{layerDesc}})
	manifestDesc := descriptor(manifest, releaseManifestType)
	index, _ := json.Marshal(releaseIndex{SchemaVersion: 2, Manifests: []releaseDescriptor{manifestDesc}})
	artifact := map[string]retainedFile{"oci-layout": {Data: []byte(`{"imageLayoutVersion":"1.0.0"}`), Mode: 0600}, "index.json": {Data: index, Mode: 0600}}
	for digest, raw := range map[string][]byte{cfgDesc.Digest: config, layerDesc.Digest: layer, manifestDesc.Digest: manifest} {
		artifact["blobs/sha256/"+strings.TrimPrefix(digest, "sha256:")] = retainedFile{Data: raw, Mode: 0600}
	}
	layout := filepath.Join(t.TempDir(), "layout")
	if copiedView(layout, artifact) != nil {
		t.Fatal("copy")
	}
	return SuiteRef{Ref: manifestDesc.Digest, Layout: layout, Member: "writer", Platform: "linux/amd64"}
}

func TestReleasedMultiMemberSelectionAndAmbiguityRefusal(t *testing.T) {
	ref := repackedRelease(t, func(content map[string]retainedFile, suite *core.Suite) {
		other := northwindRef(t, "reviewer")
		files, err := capture(other.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		reviewer, _, err := releaseContent(files, other.Digest)
		if err != nil {
			t.Fatal(err)
		}
		var rs core.Suite
		core.Unmarshal(reviewer["agentsuite.json"].Data, &rs)
		for name, file := range reviewer {
			if name != "agentsuite.json" {
				content[name] = file
			}
		}
		suite.Agents = append(suite.Agents, rs.Agents...)
		suite.Compositions = append(suite.Compositions, rs.Compositions...)
	})
	ref.Member = "reviewer"
	ref.Platform = "linux/arm64"
	b, err := LoadReleasedSuite("scoped-alias", ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Suite.Member != "reviewer" || b.Suite.Release.Platform != "linux/arm64" || len(b.Suite.Compositions) != 1 {
		t.Fatal("selected wrong member or platform")
	}
	ambiguous := repackedRelease(t, func(_ map[string]retainedFile, suite *core.Suite) {
		suite.Compositions = append(suite.Compositions, suite.Compositions[0])
	})
	if _, err := LoadReleasedSuite("alias", ambiguous, nil); err == nil {
		t.Fatal("duplicate exact tuple accepted")
	}
}

func TestReleasedForeignProfileRefusesWithExportUnavailable(t *testing.T) {
	ref := repackedRelease(t, func(content map[string]retainedFile, suite *core.Suite) {
		implementation := core.EvidenceImplementation{ID: "foreign-runtime", Version: "1.0.0", Digest: "sha256:" + strings.Repeat("a", 64)}
		profile := core.RuntimeProfile{SchemaVersion: core.SpecVersion, MediaType: core.MediaTypeRuntimeProfile, ID: "foreign", Runtime: implementation, Harness: implementation, Renderer: implementation, ConfigurationABI: "foreign-abi", ImageRequired: true}
		raw, _ := core.Marshal(profile)
		digest, _ := core.Digest(raw)
		name := "runtime-profiles/foreign.json"
		content[name] = retainedFile{Data: raw, Mode: 0644}
		suite.RuntimeProfiles = []core.ManifestRef{{ID: "foreign", Path: name, Descriptor: core.Descriptor{MediaType: core.MediaTypeRuntimeProfile, Digest: digest, Size: int64(len(raw))}}}
		suite.Compositions = suite.Compositions[:1]
		r := &suite.Compositions[0]
		var c core.Composition
		core.Unmarshal(content[r.Path].Data, &c)
		c.RuntimeProfile = "foreign"
		r.RuntimeProfile = "foreign"
		raw, _ = core.Marshal(c)
		digest, _ = core.Digest(raw)
		content[r.Path] = retainedFile{Data: raw, Mode: 0644}
		r.Descriptor.Digest = digest
		r.Descriptor.Size = int64(len(raw))
	})
	ref.RuntimeProfile = "foreign"
	b, err := LoadReleasedSuite("alias", ref, nil)
	if err == nil || b.Suite != nil || !strings.Contains(err.Error(), "not verified (export unavailable for this profile)") {
		t.Fatal("foreign ABI/profile did not refuse honestly", err)
	}
}

func TestReleasedSelectedSecretAndMissingImageRefused(t *testing.T) {
	for _, kind := range []string{"secret", "image"} {
		t.Run(kind, func(t *testing.T) {
			ref := repackedRelease(t, func(content map[string]retainedFile, suite *core.Suite) {
				if kind == "secret" {
					var agent core.Agent
					core.Unmarshal(content[suite.Agents[0].Path].Data, &agent)
					agent.Model.SecretRefs = []string{"unsupported-secret"}
					raw, _ := core.Marshal(agent)
					content[suite.Agents[0].Path] = retainedFile{Data: raw, Mode: 0644}
					sum := sha256.Sum256(raw)
					suite.Agents[0].Descriptor.Digest = fmt.Sprintf("sha256:%x", sum)
					suite.Agents[0].Descriptor.Size = int64(len(raw))
				} else {
					var build core.BuildProfile
					ref := &suite.BuildProfiles[0]
					core.Unmarshal(content[ref.Path].Data, &build)
					build.Harness = build.Harness[1:]
					raw, _ := core.Marshal(build)
					content[ref.Path] = retainedFile{Data: raw, Mode: 0644}
					sum := sha256.Sum256(raw)
					ref.Descriptor.Digest = fmt.Sprintf("sha256:%x", sum)
					ref.Descriptor.Size = int64(len(raw))
				}
			})
			if _, err := LoadReleasedSuite("alias", ref, nil); err == nil {
				t.Fatal("unsupported selected requirement accepted")
			}
		})
	}
}
