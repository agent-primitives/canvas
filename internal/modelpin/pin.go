// Package modelpin reads model.lock.json and checks that the local Ollama
// serves exactly the pinned model manifest.
package modelpin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Lock mirrors model.lock.json. Only Model and ManifestDigest are used here;
// the other fields are read by scripts/ollama.py.
type Lock struct {
	Image          string `json:"image"`
	ImageVersion   string `json:"imageVersion"`
	Model          string `json:"model"`
	ManifestDigest string `json:"manifestDigest"`
	BaseURL        string `json:"baseURL"`
	Container      string `json:"container"`
	Volume         string `json:"volume"`
	OwnerLabel     string `json:"ownerLabel"`
}

func Load(path string) (Lock, error) {
	var l Lock
	raw, err := os.ReadFile(path)
	if err != nil {
		return l, errors.New("cannot read model lock " + path)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&l) != nil || dec.Decode(&extra) != io.EOF || l.Model == "" || !strings.HasPrefix(l.ManifestDigest, "sha256:") {
		return Lock{}, errors.New("invalid model lock " + path)
	}
	return l, nil
}

// The check talks only to the local Ollama: never through a proxy, never
// following redirects.
var client = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Timeout:       5 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}()

func Check(ctx context.Context, base, model, digest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/api/tags", nil)
	if err != nil {
		return errors.New("invalid offline model endpoint")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("offline Ollama unavailable at " + base + "; run make setup-offline")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("cannot list models from offline Ollama at " + base)
	}
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&tags) != nil {
		return errors.New("invalid model list from offline Ollama")
	}
	for _, m := range tags.Models {
		if m.Name == model && strings.TrimPrefix(m.Digest, "sha256:") == strings.TrimPrefix(digest, "sha256:") {
			return nil
		}
	}
	return errors.New("pinned model " + model + " missing or different in offline Ollama; run make setup-offline")
}
