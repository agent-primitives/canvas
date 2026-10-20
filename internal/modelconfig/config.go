// Package modelconfig reads the local or shipped model configuration: which
// provider profile to use and, for hosted, where to reach it. Credentials never
// live in these files.
package modelconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-primitives/canvas/internal/contract"
)

const OfflineModel = "qwen3:8b"

// DefaultKeyFile is where the hosted API key is read from unless
// hosted.keyFile or -key-file names another file.
const DefaultKeyFile = "~/.config/canvas/hosted.key"

const maxConfigSize = 16384

// Hosted describes an OpenAI-compatible Chat Completions endpoint, such as an
// Azure OpenAI resource. There is no built-in default.
type Hosted struct {
	Endpoint string // https base URL, e.g. https://<resource>.openai.azure.com/openai/v1
	Model    string // model or deployment name
	KeyFile  string // absolute path, with ~ already expanded
	Auth     string // "api-key" (Azure header) or "bearer"
}

type Profile struct {
	Name   string
	Model  string
	Hosted Hosted // set only for the hosted profile
}

// Load reads root/model.config.local.json, or root/model.config.json only if
// the local file is absent. The local file replaces the whole configuration.
// If both files are absent, offline is selected. A non-empty override replaces
// the configured profile name, but does not bypass file validation.
func Load(root, override string) (Profile, error) {
	name := "offline"
	var hosted Hosted
	path := filepath.Join(root, "model.config.local.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		path = filepath.Join(root, "model.config.json")
		info, err = os.Lstat(path)
	}
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > maxConfigSize {
			return Profile{}, fmt.Errorf("%s must be a regular file of at most 16 KiB", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Profile{}, fmt.Errorf("cannot read %s: %w", path, err)
		}
		if len(data) > maxConfigSize {
			return Profile{}, fmt.Errorf("%s must be at most 16 KiB", path)
		}
		name, hosted, err = parse(data)
		if err != nil {
			return Profile{}, fmt.Errorf("%s: %w", path, err)
		}
		// An unknown profile name is an error even with an override. Hosted
		// settings are only checked when hosted is actually selected.
		if name != "hosted" && name != "offline" {
			return Profile{}, fmt.Errorf("%s: profile must be \"hosted\" or \"offline\", not %q", path, name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Profile{}, fmt.Errorf("cannot inspect %s: %w", path, err)
	}
	if override != "" {
		name = override
	}
	p, err := profile(name, hosted)
	if err != nil {
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// parse accepts {"profile": string, "hosted": {...}} and nothing else:
// unknown or duplicate fields, nulls and trailing data are errors.
func parse(data []byte) (string, Hosted, error) {
	var name string
	var hosted Hosted
	dec := json.NewDecoder(bytes.NewReader(data))
	seenProfile := false
	err := object(dec, map[string]func() error{
		"profile": func() error {
			seenProfile = true
			return str(dec, &name)
		},
		"hosted": func() error {
			return object(dec, map[string]func() error{
				"endpoint": func() error { return str(dec, &hosted.Endpoint) },
				"model":    func() error { return str(dec, &hosted.Model) },
				"keyFile":  func() error { return str(dec, &hosted.KeyFile) },
				"auth":     func() error { return str(dec, &hosted.Auth) },
			})
		},
	})
	if err != nil {
		return "", Hosted{}, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return "", Hosted{}, errors.New("unexpected data after the JSON object")
	}
	if !seenProfile {
		return "", Hosted{}, errors.New(`"profile" is required`)
	}
	return name, hosted, nil
}

func object(dec *json.Decoder, fields map[string]func() error) error {
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid JSON: expected an object")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("invalid JSON")
		}
		read, known := fields[key]
		if !known {
			return fmt.Errorf("unknown field %q", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		if err := read(); err != nil {
			return fmt.Errorf("field %q: %w", key, err)
		}
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return errors.New("invalid JSON")
	}
	return nil
}

func str(dec *json.Decoder, out *string) error {
	var value *string
	if dec.Decode(&value) != nil || value == nil {
		return errors.New("must be a string")
	}
	*out = *value
	return nil
}

// Contracts applies selection before Sessions binding, so the journal and HTTP
// provider agree on the model. Original authored bundles remain unchanged.
func (p Profile) Contracts(bundles []contract.Bundle) ([]contract.Bundle, error) {
	selected := make([]contract.Bundle, 0, len(bundles))
	for _, b := range bundles {
		derived, err := contract.ForModel(b, p.Model)
		if err != nil {
			return nil, err
		}
		selected = append(selected, derived)
	}
	return selected, nil
}

// WithKeyFile returns p with the hosted key file replaced, e.g. from -key-file.
// A relative path is taken relative to the current directory.
func (p Profile) WithKeyFile(path string) (Profile, error) {
	if p.Name != "hosted" {
		return Profile{}, errors.New("-key-file only applies to the hosted profile")
	}
	expanded, err := ExpandHome(path)
	if err != nil {
		return Profile{}, err
	}
	if p.Hosted.KeyFile, err = filepath.Abs(expanded); err != nil {
		return Profile{}, errors.New("invalid key file path")
	}
	return p, nil
}

// ExpandHome replaces a leading ~ with the current user's home directory.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("cannot find the home directory to expand ~")
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

func profile(name string, hosted Hosted) (Profile, error) {
	switch name {
	case "offline":
		return Profile{Name: name, Model: OfflineModel}, nil
	case "hosted":
		h, err := checkHosted(hosted)
		if err != nil {
			return Profile{}, err
		}
		return Profile{Name: name, Model: h.Model, Hosted: h}, nil
	default:
		return Profile{}, fmt.Errorf("model profile must be \"hosted\" or \"offline\", not %q", name)
	}
}

func checkHosted(h Hosted) (Hosted, error) {
	if h.Endpoint == "" || h.Model == "" {
		return Hosted{}, errors.New(`the hosted profile needs "hosted": {"endpoint": ..., "model": ...}`)
	}
	u, err := url.Parse(h.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Hosted{}, errors.New("hosted.endpoint must be an https URL without credentials, query or fragment")
	}
	if len(h.Model) > 160 || strings.ContainsAny(h.Model, "\r\n\t ") {
		return Hosted{}, errors.New("hosted.model must be a short name without whitespace")
	}
	switch h.Auth {
	case "":
		h.Auth = "api-key"
	case "api-key", "bearer":
	default:
		return Hosted{}, errors.New(`hosted.auth must be "api-key" or "bearer"`)
	}
	if h.KeyFile == "" {
		h.KeyFile = DefaultKeyFile
	}
	keyFile, err := ExpandHome(h.KeyFile)
	if err != nil {
		return Hosted{}, err
	}
	if !filepath.IsAbs(keyFile) {
		return Hosted{}, errors.New("hosted.keyFile must be an absolute path or start with ~/")
	}
	h.KeyFile = filepath.Clean(keyFile)
	return h, nil
}
