package contract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/suite/core"
	"github.com/agent-primitives/suite/oci"
)

var ErrReleaseProfile = errors.New("unsupported released runtime profile; evaluation not verified (export unavailable for this profile)")

// ReleaseIdentity describes a selected release, not qualification of Canvas's local harness.
type ReleaseIdentity struct {
	AuthoredModel      string         `json:"authoredModel"`
	SchemaVersion      string         `json:"schemaVersion"`
	Platform           string         `json:"platform"`
	RuntimeProfile     string         `json:"runtimeProfile,omitempty"`
	CompositionDigest  string         `json:"compositionDigest"`
	BuildProfile       string         `json:"buildProfile"`
	BuildProfileDigest string         `json:"buildProfileDigest"`
	RuntimeImage       string         `json:"runtimeImage,omitempty"`
	HarnessImage       string         `json:"harnessImage,omitempty"`
	CanvasRuntime      string         `json:"canvasRuntime"`
	Evaluation         EvaluationInfo `json:"evaluation"`
}

type releasedArchive struct {
	Version        string                  `json:"apiVersion"`
	Kind           string                  `json:"kind"`
	ID             string                  `json:"id"`
	Pin            string                  `json:"pin"`
	Member         string                  `json:"member"`
	Platform       string                  `json:"platform"`
	RuntimeProfile string                  `json:"runtimeProfile,omitempty"`
	Artifact       map[string]retainedFile `json:"artifact"`
	Expected       []byte                  `json:"expected,omitempty"`
	Evidence       []byte                  `json:"evidence,omitempty"`
}

func LoadReleasedSuite(id string, ref SuiteRef, resolver *ReleaseResolver) (Bundle, error) {
	pin, _, err := releaseReference(ref.Ref)
	if err != nil || !namePattern.MatchString(id) || ref.Directory != "" || ref.Artifact != "" || ref.Digest != "" || ref.Member == "" || ref.Platform == "" {
		return Bundle{}, errSuite
	}
	var files map[string]retainedFile
	if resolver != nil {
		files, err = resolver.resolve(ref, pin)
	} else if ref.Layout != "" {
		files, err = capture(ref.Layout)
	} else {
		return Bundle{}, errSuite
	}
	if err != nil {
		return Bundle{}, errSuite
	}
	// Transport file permissions are not part of S; content-layer modes remain intact.
	for name, file := range files {
		file.Mode = 0600
		files[name] = file
	}
	return releasedBundle(releasedArchive{Version: "canvas.local/release-execution-v1", Kind: "ReleasedSuiteExecution", ID: id, Pin: pin, Member: ref.Member, Platform: ref.Platform, RuntimeProfile: ref.RuntimeProfile, Artifact: files})
}

func WithReleasedEvaluation(b Bundle, expected, evidence []byte) (Bundle, error) {
	parsed, err := Parse(b.Source)
	if err != nil || parsed.Suite == nil || parsed.Suite.Release == nil || !reflect.DeepEqual(parsed, b) {
		return Bundle{}, errSuite
	}
	var a releasedArchive
	if json.Unmarshal(b.Source, &a) != nil {
		return Bundle{}, errSuite
	}
	a.Expected = bytes.Clone(expected)
	a.Evidence = bytes.Clone(evidence)
	return releasedBundle(a)
}

func parseReleasedArchive(data []byte) (Bundle, error) {
	if len(data) > maxArchive {
		return Bundle{}, errSuite
	}
	var a releasedArchive
	if decodeReleaseJSON(data, &a) != nil {
		return Bundle{}, errSuite
	}
	canonical, err := json.Marshal(a)
	if err != nil || !bytes.Equal(canonical, data) {
		return Bundle{}, errSuite
	}
	return releasedBundle(a)
}

func releasedBundle(a releasedArchive) (Bundle, error) {
	if a.Version != "canvas.local/release-execution-v1" || a.Kind != "ReleasedSuiteExecution" || !namePattern.MatchString(a.ID) || !core.IsValidDigest(a.Pin) || len(a.Expected) > 1<<20 || len(a.Evidence) > 1<<20 {
		return Bundle{}, errSuite
	}
	content, manifest, err := releaseContent(a.Artifact, a.Pin)
	if err != nil {
		return Bundle{}, errSuite
	}
	temp, err := os.MkdirTemp("", "canvas-release-")
	if err != nil {
		return Bundle{}, errSuite
	}
	defer os.RemoveAll(temp)
	layout, directory := filepath.Join(temp, "layout"), filepath.Join(temp, "content")
	if copiedView(layout, a.Artifact) != nil || copiedView(directory, content) != nil {
		return Bundle{}, errSuite
	}
	report, err := oci.ValidatePath(layout)
	if err != nil {
		return Bundle{}, errSuite
	}
	diff, err := oci.DiffPaths(directory, layout)
	if err != nil || diff.After.ArtifactDigest != a.Pin || diff.Before.ContentDigest != diff.After.ContentDigest {
		return Bundle{}, errSuite
	}
	var suite core.Suite
	raw := content["agentsuite.json"].Data
	if core.Unmarshal(raw, &suite) != nil || len(suite.Extensions) != 0 || len(suite.Capabilities) != 0 {
		return Bundle{}, errSuite
	}
	var selected core.CompositionSelection
	count := 0
	for _, s := range report.CompositionSelections {
		if s.Agent == a.Member && s.Platform.String() == a.Platform && s.RuntimeProfile == a.RuntimeProfile {
			selected = s
			count++
		}
	}
	if count != 1 {
		return Bundle{}, errSuite
	}
	// No new public-profile runtime is qualified here. Refuse before a Bundle can register.
	if selected.RuntimeProfile != "" {
		return Bundle{}, ErrReleaseProfile
	}
	var member core.ManifestRef
	count = 0
	for _, m := range suite.Agents {
		if m.ID == a.Member {
			member = m
			count++
		}
	}
	var agent core.Agent
	if count != 1 || core.Unmarshal(content[member.Path].Data, &agent) != nil || !modelOnlyAgent(agent) || agent.Policy != nil {
		return Bundle{}, errSuite
	}
	var composition core.Composition
	for _, c := range suite.Compositions {
		if c.Descriptor.Digest == selected.Digest {
			if core.Unmarshal(content[c.Path].Data, &composition) != nil {
				return Bundle{}, errSuite
			}
		}
	}
	if composition.Agent != a.Member || composition.Platform.String() != a.Platform || len(composition.ToolProviders) != 0 {
		return Bundle{}, errSuite
	}
	var build core.BuildProfile
	var buildDigest string
	for _, r := range suite.BuildProfiles {
		if r.ID == selected.BuildProfile {
			buildDigest = r.Descriptor.Digest
			if core.Unmarshal(content[r.Path].Data, &build) != nil {
				return Bundle{}, errSuite
			}
		}
	}
	if buildDigest == "" || build.Execution != nil {
		return Bundle{}, errSuite
	}
	image := func(images []core.PlatformImage) (string, error) {
		found := ""
		for _, im := range images {
			if im.Platform == selected.Platform {
				if found != "" {
					return "", errSuite
				}
				found = im.ImageRef
			}
		}
		if found == "" {
			return "", errSuite
		}
		return found, nil
	}
	runtimeImage, err := image(build.RuntimeBase)
	if err != nil {
		return Bundle{}, errSuite
	}
	harnessImage, err := image(build.Harness)
	if err != nil {
		return Bundle{}, errSuite
	}
	instruction := content[agent.Instructions.Path].Data
	if len(instruction) == 0 || len(instruction) > 65536 || !utf8.Valid(instruction) {
		return Bundle{}, errSuite
	}
	graph, err := core.Digest(raw)
	if err != nil {
		return Bundle{}, errSuite
	}
	identity := &SuiteIdentity{Digest: a.Pin, ContentDigest: diff.After.ContentDigest, GraphDigest: graph, Member: member.ID, MemberDigest: member.Descriptor.Digest, Compositions: []core.CompositionSelection{selected}, Release: &ReleaseIdentity{AuthoredModel: agent.Model.Model, SchemaVersion: suite.SchemaVersion, Platform: a.Platform, RuntimeProfile: a.RuntimeProfile, CompositionDigest: selected.Digest, BuildProfile: selected.BuildProfile, BuildProfileDigest: buildDigest, RuntimeImage: runtimeImage, HarnessImage: harnessImage, CanvasRuntime: "canvas-local/bundle-v4; execution not qualified by release evaluation"}}
	identity.Release.Evaluation = verifyReleaseEvaluation(layout, a, manifest, identity, agent)
	data, err := json.Marshal(a)
	if err != nil || len(data) > maxArchive {
		return Bundle{}, errSuite
	}
	sum := sha256.Sum256(data)
	return Bundle{ID: a.ID, Digest: fmt.Sprintf("%x", sum), Model: agent.Model.Model, Instructions: string(instruction), Source: data, Suite: identity}, nil
}

// releaseContent bounds decompression before the shared validator sees untrusted OCI bytes.
func releaseContent(files map[string]retainedFile, pin string) (map[string]retainedFile, releaseDescriptor, error) {
	var zero releaseDescriptor
	if len(files) == 0 || len(files) > 128 {
		return nil, zero, errSuite
	}
	total := 0
	for name, file := range files {
		total += len(file.Data)
		if !retainedPath(name) || len(file.Data) > 1<<20 || total > 4<<20 || file.Mode > 0777 || file.Mode&0022 != 0 {
			return nil, zero, errSuite
		}
	}
	var index releaseIndex
	if core.Unmarshal(files["index.json"].Data, &index) != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return nil, zero, errSuite
	}
	desc := index.Manifests[0]
	if desc.Digest != pin {
		return nil, zero, errSuite
	}
	raw, err := releaseBlob(files, desc)
	if err != nil {
		return nil, zero, errSuite
	}
	var manifest releaseManifest
	if core.Unmarshal(raw, &manifest) != nil {
		return nil, zero, errSuite
	}
	var layer []byte
	for _, d := range manifest.Layers {
		if d.MediaType == core.MediaTypeContent {
			if layer != nil {
				return nil, zero, errSuite
			}
			layer, err = releaseBlob(files, d)
			if err != nil {
				return nil, zero, errSuite
			}
		}
	}
	if layer == nil {
		return nil, zero, errSuite
	}
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		return nil, zero, errSuite
	}
	expanded, err := io.ReadAll(io.LimitReader(gz, (8<<20)+1))
	closeErr := gz.Close()
	if err != nil || closeErr != nil || len(expanded) > 8<<20 {
		return nil, zero, errSuite
	}
	tr := tar.NewReader(bytes.NewReader(expanded))
	content := map[string]retainedFile{}
	entries := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		entries++
		if err != nil || entries > 256 {
			return nil, zero, errSuite
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !retainedPath(name) || h.Mode&^0777 != 0 || h.Mode&07000 != 0 {
			return nil, zero, errSuite
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 1<<20 || len(content) >= 128 {
			return nil, zero, errSuite
		}
		if _, ok := content[name]; ok {
			return nil, zero, errSuite
		}
		data, err := io.ReadAll(io.LimitReader(tr, (1<<20)+1))
		if err != nil || int64(len(data)) != h.Size {
			return nil, zero, errSuite
		}
		content[name] = retainedFile{Data: data, Mode: uint32(h.Mode) &^ 0022}
	}
	return content, desc, nil
}

func releaseBlob(files map[string]retainedFile, d releaseDescriptor) ([]byte, error) {
	if !core.IsValidDigest(d.Digest) || d.Size < 0 || d.Size > 1<<20 || len(d.URLs) != 0 || len(d.Data) != 0 {
		return nil, errSuite
	}
	raw, ok := files["blobs/sha256/"+strings.TrimPrefix(d.Digest, "sha256:")]
	sum := sha256.Sum256(raw.Data)
	if !ok || int64(len(raw.Data)) != d.Size || fmt.Sprintf("sha256:%x", sum) != d.Digest {
		return nil, errSuite
	}
	return raw.Data, nil
}
