package sessionauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// SourceConfig selects exactly one token source: a private JSON file or an
// argv command (never a shell) that prints the same JSON envelope.
type SourceConfig struct {
	File    string   `json:"file,omitempty"`
	Command []string `json:"command,omitempty"`
}

func (c SourceConfig) Open() (Source, error) {
	if (c.File != "") == (len(c.Command) != 0) {
		return nil, errors.New("sessionauth: exactly one token source is required")
	}
	if c.File != "" {
		f, err := openPrivate(c.File)
		if err != nil {
			return nil, err
		}
		f.Close()
		return fileSource(c.File), nil
	}
	for i, arg := range c.Command {
		if arg == "" {
			return nil, errors.New("sessionauth: command argv must be non-empty")
		}
		if i > 0 && strings.HasPrefix(strings.ToLower(arg), "bearer ") {
			return nil, errors.New("sessionauth: command argv must not include bearer tokens")
		}
	}
	return commandSource(append([]string(nil), c.Command...)), nil
}

type fileSource string

func (s fileSource) Token(ctx context.Context) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	f, err := openPrivate(string(s))
	if err != nil {
		return "", time.Time{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSourceJSON+1))
	if err != nil || len(data) > maxSourceJSON {
		return "", time.Time{}, errSourceInvalid
	}
	return decodeSourceJSON(data)
}

// openPrivate refuses symlinks, FIFOs/devices, and files readable by anyone
// other than the owning current user. Size is bounded by the caller's read.
func openPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errSourceInvalid
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, errSourceInvalid
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || int(st.Uid) != os.Geteuid() {
		f.Close()
		return nil, errSourceInvalid
	}
	return f, nil
}

type commandSource []string

func (s commandSource) Token(ctx context.Context) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s[0], s[1:]...)
	// Stdin/Stderr stay nil (null device): stderr may echo secrets and is discarded.
	var out limitBuffer
	cmd.Stdout = &out
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Run(); err != nil || out.overflow {
		return "", time.Time{}, errSourceInvalid
	}
	return decodeSourceJSON(out.buf.Bytes())
}

// limitBuffer keeps at most maxSourceJSON bytes and drains the rest so a
// chatty command exits promptly instead of blocking on a full pipe.
type limitBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (w *limitBuffer) Write(p []byte) (int, error) {
	if w.overflow || w.buf.Len()+len(p) > maxSourceJSON {
		w.overflow = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func decodeSourceJSON(data []byte) (string, time.Time, error) {
	var env struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   string `json:"expiresAt"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&env) != nil || d.Decode(&struct{}{}) != io.EOF {
		return "", time.Time{}, errSourceInvalid
	}
	tok := env.AccessToken
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") {
		return "", time.Time{}, errSourceInvalid
	}
	expires, err := time.Parse(time.RFC3339, env.ExpiresAt)
	if err != nil {
		return "", time.Time{}, errSourceInvalid
	}
	return tok, expires, nil
}
