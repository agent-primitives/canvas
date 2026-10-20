package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/agent-primitives/canvas/internal/canvas"
)

type config struct {
	Version          int    `json:"version"`
	AuthMode         string `json:"authMode"`
	TrustedNetwork   bool   `json:"trustedNetwork"`
	InstanceID       string `json:"instanceID"`
	Listen           string `json:"listen"`
	Origin           string `json:"origin"`
	ReadinessAddress string `json:"readinessAddress"`
	StateDirectory   string `json:"stateDirectory"`
	DefinitionFile   string `json:"definitionFile"`
	SessionsEndpoint string `json:"sessionsEndpoint"`
	SessionsCAFile   string `json:"sessionsCAFile"`
	TLSCertFile      string `json:"tlsCertFile"`
	TLSKeyFile       string `json:"tlsKeyFile"`
}

var projectName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var serviceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.svc\.cluster\.local$`)

// Secrets may be kubelet projected symlinks. Validate the opened target, not
// the link spelling; O_NONBLOCK prevents a FIFO from blocking before fstat.
func readRegular(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("expected bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file too large")
	}
	return b, nil
}

// All keys are mandatory and exact. encoding/json's case-insensitive struct
// matching and last-duplicate-wins behaviour are not a configuration contract.
func decodeClosed(raw []byte, target any) error {
	v := reflect.ValueOf(target).Elem()
	typ := v.Type()
	fields := map[string]int{}
	for i := 0; i < typ.NumField(); i++ {
		fields[typ.Field(i).Tag.Get("json")] = i
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("expected JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		index, known := fields[name]
		if !ok || !known || seen[name] {
			return errors.New("unknown or duplicate JSON key")
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("null JSON value")
		}
		if err := json.Unmarshal(value, v.Field(index).Addr().Interface()); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || len(seen) != len(fields) {
		return errors.New("missing keys or trailing JSON")
	}
	return nil
}

func readConfig(path string) (config, error) {
	var c config
	raw, err := readRegular(path, 64<<10)
	if err != nil {
		return c, fmt.Errorf("config file: %w", err)
	}
	if err = decodeClosed(raw, &c); err != nil {
		return c, fmt.Errorf("config JSON: %w", err)
	}
	return c, c.validate()
}
func numericAddress(address string, zero bool) (string, error) {
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || n < 0 || n > 65535 || (!zero && n == 0) || strconv.Itoa(n) != port {
		return "", errors.New("numeric port required")
	}
	return host, nil
}
func loopbackAddress(address string, zero bool) error {
	host, err := numericAddress(address, zero)
	if err != nil || host != "127.0.0.1" {
		return errors.New("listener must use literal 127.0.0.1:numeric-port")
	}
	return nil
}
func (c config) validate() error {
	if c.Version != 1 || c.AuthMode != "off" || !c.TrustedNetwork || !projectName.MatchString(c.InstanceID) {
		return errors.New("version 1, explicit authMode off/trustedNetwork true and bounded instanceID required")
	}
	if err := loopbackAddress(c.Listen, true); err != nil {
		return err
	}
	if err := loopbackAddress(c.ReadinessAddress, true); err != nil {
		return err
	}
	if c.Listen == c.ReadinessAddress && c.Listen != "127.0.0.1:0" {
		return errors.New("API and readiness ports must differ")
	}
	if _, err := canvas.TLSLoopbackHandler(c.Origin, nil); err != nil {
		return err
	}
	for _, path := range []string{c.StateDirectory, c.DefinitionFile, c.SessionsCAFile, c.TLSCertFile, c.TLSKeyFile} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || filepath.Clean(path) != path || path == "/" {
			return errors.New("absolute operator file/child paths required")
		}
	}
	host, err := numericAddress(c.SessionsEndpoint, false)
	if err != nil || (host != "127.0.0.1" && !serviceName.MatchString(host)) {
		return errors.New("sessions endpoint must be literal loopback or exact service.namespace.svc.cluster.local:numeric-port")
	}
	return nil
}
