// Package announcement defines the exact, immutable payload of a local simulation.
// Native Sessions journal records, not these payloads, supply approval authority.
package announcement

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	HarnessName = "canvas.simulated-announcement.v1"
	ToolName    = "canvas.simulated-announcement"
	Kind        = "SIMULATED_ANNOUNCEMENT"
)

type Snapshot struct {
	Operation  string `json:"operation"`
	Definition string `json:"definition"`
	Action     string `json:"action"`
	Document   string `json:"document"`
	Path       string `json:"path"`
	Proposal   string `json:"proposal"`
	Revision   string `json:"revision"`
	Digest     string `json:"digest"`
	Content    string `json:"content"`
}

type Receipt struct {
	Kind     string   `json:"kind"`
	Snapshot Snapshot `json:"snapshot"`
}

var (
	identifier  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	declaredID  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex40       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	errSnapshot = errors.New("announcement: invalid snapshot")
)

func Validate(s Snapshot) error {
	if !identifier.MatchString(s.Operation) || !identifier.MatchString(s.Proposal) ||
		!declaredID.MatchString(s.Action) || !declaredID.MatchString(s.Document) ||
		!hex64.MatchString(s.Definition) || !hex40.MatchString(s.Revision) || !hex64.MatchString(s.Digest) ||
		!utf8.ValidString(s.Content) || len(s.Content) > 64*1024 ||
		!utf8.ValidString(s.Path) || len(s.Path) > 512 || s.Path == "." || !fs.ValidPath(s.Path) ||
		strings.ContainsAny(s.Path, "\\:*?[]{}") || strings.IndexFunc(s.Path, unicode.IsControl) >= 0 {
		return errSnapshot
	}
	digest := sha256.Sum256([]byte(s.Content))
	if hex.EncodeToString(digest[:]) != s.Digest {
		return errSnapshot
	}
	return nil
}

func Encode(s Snapshot) ([]byte, error) {
	if err := Validate(s); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// Decode deliberately refuses duplicate, case-folded, unknown and trailing fields.
func Decode(data []byte) (Snapshot, error) {
	if len(data) > 400*1024 || !utf8.Valid(data) {
		return Snapshot{}, errSnapshot
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return Snapshot{}, errSnapshot
	}
	args := map[string]any{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return Snapshot{}, errSnapshot
		}
		k, ok := key.(string)
		if !ok {
			return Snapshot{}, errSnapshot
		}
		if _, exists := args[k]; exists {
			return Snapshot{}, errSnapshot
		}
		// Decode into any so JSON null cannot silently become an empty string.
		var value any
		if dec.Decode(&value) != nil {
			return Snapshot{}, errSnapshot
		}
		if _, ok := value.(string); !ok {
			return Snapshot{}, errSnapshot
		}
		args[k] = value
	}
	if _, err = dec.Token(); err != nil {
		return Snapshot{}, errSnapshot
	}
	if _, err = dec.Token(); err != io.EOF {
		return Snapshot{}, errSnapshot
	}
	return FromArgs(args)
}

// Args uses flat string values to survive the Sessions protobuf map roundtrip.
func Args(s Snapshot) map[string]any {
	return map[string]any{"operation": s.Operation, "definition": s.Definition, "action": s.Action,
		"document": s.Document, "path": s.Path, "proposal": s.Proposal, "revision": s.Revision, "digest": s.Digest, "content": s.Content}
}

func FromArgs(args map[string]any) (Snapshot, error) {
	var s Snapshot
	fields := map[string]*string{"operation": &s.Operation, "definition": &s.Definition, "action": &s.Action,
		"document": &s.Document, "path": &s.Path, "proposal": &s.Proposal, "revision": &s.Revision, "digest": &s.Digest, "content": &s.Content}
	if len(args) != len(fields) {
		return Snapshot{}, errSnapshot
	}
	for key, dest := range fields {
		value, ok := args[key].(string)
		if !ok {
			return Snapshot{}, errSnapshot
		}
		*dest = value
	}
	if err := Validate(s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// DecodeReceipt validates only the typed output. Callers must separately verify
// the native record's session, call, approval sequences and successful status.
func DecodeReceipt(output map[string]any) (Receipt, error) {
	if len(output) != 2 || output["kind"] != Kind {
		return Receipt{}, errors.New("announcement: invalid receipt")
	}
	args, ok := output["snapshot"].(map[string]any)
	if !ok {
		return Receipt{}, errors.New("announcement: invalid receipt")
	}
	snapshot, err := FromArgs(args)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Kind: Kind, Snapshot: snapshot}, nil
}
