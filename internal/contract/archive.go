package contract

import (
	"encoding/hex"
	"path/filepath"
	"strings"
)

// ArchiveResolver reads exact retained execution archives, never the current alias or registry.
type ArchiveResolver struct{ Directory string }

func (r ArchiveResolver) Load(digest string) (Bundle, error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
		return Bundle{}, errSuite
	}
	filename := filepath.Join(r.Directory, digest, "agent.yaml")
	if _, err := noLinkedAncestors(filename); err != nil {
		return Bundle{}, errSuite
	}
	b, err := Load(filename)
	if err != nil || b.Digest != digest {
		return Bundle{}, errSuite
	}
	return b, nil
}
