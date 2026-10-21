package contract

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/agent-primitives/suite/core"
	"github.com/agent-primitives/suite/oci"
)

const releaseManifestType = "application/vnd.oci.image.manifest.v1+json"

type releaseDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Platform     *core.Platform    `json:"platform,omitempty"`
	URLs         json.RawMessage   `json:"urls,omitempty"`
	Data         json.RawMessage   `json:"data,omitempty"`
}
type releaseIndex struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType,omitempty"`
	Manifests     []releaseDescriptor `json:"manifests"`
	Annotations   map[string]string   `json:"annotations,omitempty"`
}
type releaseManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType,omitempty"`
	ArtifactType  string              `json:"artifactType"`
	Config        releaseDescriptor   `json:"config"`
	Layers        []releaseDescriptor `json:"layers"`
	Subject       *releaseDescriptor  `json:"subject,omitempty"`
	Annotations   map[string]string   `json:"annotations,omitempty"`
}

// ReleaseResolver caches validated OCI bytes by manifest digest, never mutable tags.
// Root validation is lazy so legacy definition failures have no new state effects.
type ReleaseResolver struct {
	cacheDir string
	client   *http.Client
}

func NewReleaseResolver(cacheDir string) *ReleaseResolver {
	return &ReleaseResolver{cacheDir: cacheDir}
}

var releaseRepositoryPart = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

func releaseReference(ref string) (string, string, error) {
	if len(ref) > 1024 {
		return "", "", errSuite
	}
	if core.IsValidDigest(ref) {
		return ref, "", nil
	}
	if strings.Count(ref, "@") != 1 {
		return "", "", errSuite
	}
	location, pin, _ := strings.Cut(ref, "@")
	host, repository, ok := strings.Cut(location, "/")
	if !ok || host == "" || !core.IsValidDigest(pin) {
		return "", "", errSuite
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errSuite
	}
	for _, part := range strings.Split(repository, "/") {
		if !releaseRepositoryPart.MatchString(part) {
			return "", "", errSuite
		}
	}
	return pin, "https://" + host + "/v2/" + repository, nil
}

func (r *ReleaseResolver) resolve(ref SuiteRef, pin string) (map[string]retainedFile, error) {
	root, err := privateReleaseRoot(r.cacheDir)
	if err != nil {
		return nil, errSuite
	}
	defer root.Close()
	key := strings.TrimPrefix(pin, "sha256:") + ".json"
	cached, exists, err := readReleaseCache(root, key, pin)
	if err != nil {
		return nil, errSuite
	}
	if exists && ref.Layout == "" {
		return cached, nil
	}
	var files map[string]retainedFile
	if ref.Layout != "" {
		files, err = capture(ref.Layout)
	} else {
		files, err = r.fetch(ref.Ref)
	}
	if err != nil {
		return nil, errSuite
	}
	for name, file := range files {
		file.Mode = 0600
		files[name] = file
	}
	if validateReleaseLayout(files, pin) != nil {
		return nil, errSuite
	}
	data, err := json.Marshal(files)
	if err != nil || len(data) > maxArchive {
		return nil, errSuite
	}
	if exists {
		// Both layouts validated the same raw manifest and referenced digests. Index
		// annotations/unused transport files are not S; retain the established cache bytes.
		return cached, nil
	}
	staging := "staging-" + rand.Text()
	f, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errSuite
	}
	defer root.Remove(staging)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return nil, errSuite
	}
	if err := root.Link(staging, key); err != nil {
		prior, ok, readErr := readReleaseCache(root, key, pin)
		if readErr != nil || !ok {
			return nil, errSuite
		}
		files = prior
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, errSuite
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return nil, errSuite
	}
	return files, nil
}

// Walk anchored directories before creating anything below an existing private state root.
// Never repair permissions or follow a link into a caller-supplied foreign path.
func privateReleaseRoot(value string) (*os.Root, error) {
	if value == "" {
		return nil, errSuite
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return nil, errSuite
	}
	parts := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 2 {
		return nil, errSuite
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, errSuite
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			root.Close()
			return nil, errSuite
		}
		info, err := root.Lstat(part)
		if os.IsNotExist(err) {
			if err = root.Mkdir(part, 0700); err == nil || os.IsExist(err) {
				info, err = root.Lstat(part)
			}
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, errSuite
		}
		if i >= len(parts)-2 {
			owner, ok := info.Sys().(*syscall.Stat_t)
			if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
				root.Close()
				return nil, errSuite
			}
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, errSuite
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			root.Close()
			return nil, errSuite
		}
		root.Close()
		root = next
	}
	return root, nil
}

func readReleaseCache(root *os.Root, key, pin string) (map[string]retainedFile, bool, error) {
	info, err := root.Lstat(key)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxArchive {
		return nil, false, errSuite
	}
	f, err := root.OpenFile(key, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, errSuite
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, false, errSuite
	}
	data, err := io.ReadAll(io.LimitReader(f, maxArchive+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(data) > maxArchive {
		return nil, false, errSuite
	}
	var files map[string]retainedFile
	if decodeReleaseJSON(data, &files) != nil || validateReleaseLayout(files, pin) != nil {
		return nil, false, errSuite
	}
	return files, true, nil
}

// Shared strict decoding rejects duplicates/invalid values; stdlib decoding retains
// the existing archive format's base64 byte fields, which suite's typed codec omits.
func decodeReleaseJSON(data []byte, out any) error {
	var value any
	if core.Unmarshal(data, &value) != nil {
		return errSuite
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return errSuite
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errSuite
	}
	return nil
}

func validateReleaseLayout(files map[string]retainedFile, pin string) error {
	if _, _, err := releaseContent(files, pin); err != nil {
		return errSuite
	}
	temp, err := os.MkdirTemp("", "canvas-release-cache-")
	if err != nil {
		return errSuite
	}
	defer os.RemoveAll(temp)
	layout := filepath.Join(temp, "layout")
	if copiedView(layout, files) != nil {
		return errSuite
	}
	if _, err := oci.ValidatePath(layout); err != nil {
		return errSuite
	}
	return nil
}

func (r *ReleaseResolver) fetch(ref string) (map[string]retainedFile, error) {
	pin, base, err := releaseReference(ref)
	if err != nil || base == "" {
		return nil, errSuite
	}
	client := r.client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	}
	// A test-injected TLS transport may trust its fixture, but cannot relax redirects.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errSuite }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	total := 0
	get := func(path, digest string, size int64) ([]byte, error) {
		if size > 1<<20 || size < -1 {
			return nil, errSuite
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, errSuite
		}
		req.Header.Set("Accept", releaseManifestType)
		response, err := copyClient.Do(req)
		if err != nil {
			return nil, errSuite
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || response.ContentLength > 1<<20 {
			return nil, errSuite
		}
		if strings.HasPrefix(path, "/manifests/") && strings.Split(response.Header.Get("Content-Type"), ";")[0] != releaseManifestType {
			return nil, errSuite
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		total += len(data)
		sum := sha256.Sum256(data)
		if err != nil || len(data) > 1<<20 || total > 4<<20 || (size >= 0 && int64(len(data)) != size) || fmt.Sprintf("sha256:%x", sum) != digest {
			return nil, errSuite
		}
		return data, nil
	}
	raw, err := get("/manifests/"+pin, pin, -1)
	if err != nil {
		return nil, errSuite
	}
	var manifest releaseManifest
	if core.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.ArtifactType != core.MediaTypeArtifact || manifest.Subject != nil || len(manifest.Layers) > 32 {
		return nil, errSuite
	}
	descriptor := releaseDescriptor{MediaType: releaseManifestType, Digest: pin, Size: int64(len(raw))}
	index, _ := json.Marshal(releaseIndex{SchemaVersion: 2, Manifests: []releaseDescriptor{descriptor}})
	files := map[string]retainedFile{"oci-layout": {Data: []byte(`{"imageLayoutVersion":"1.0.0"}`), Mode: 0600}, "index.json": {Data: index, Mode: 0600}, "blobs/sha256/" + strings.TrimPrefix(pin, "sha256:"): {Data: raw, Mode: 0600}}
	for _, d := range append([]releaseDescriptor{manifest.Config}, manifest.Layers...) {
		if !core.IsValidDigest(d.Digest) || d.Size < 0 || len(d.URLs) != 0 || len(d.Data) != 0 {
			return nil, errSuite
		}
		key := "blobs/sha256/" + strings.TrimPrefix(d.Digest, "sha256:")
		if prior, ok := files[key]; ok {
			if int64(len(prior.Data)) != d.Size {
				return nil, errSuite
			}
			continue
		}
		data, err := get("/blobs/"+d.Digest, d.Digest, d.Size)
		if err != nil {
			return nil, errSuite
		}
		files[key] = retainedFile{Data: data, Mode: 0600}
	}
	return files, nil
}
