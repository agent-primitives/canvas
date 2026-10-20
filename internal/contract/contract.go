// Package contract loads agent bundles and binds them to harnesses. It supports
// only the core subset Canvas uses, never runtime extensions.
package contract

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

type Bundle struct {
	Suite          *SuiteIdentity `json:"suite,omitempty"`
	ID             string         `json:"id"`
	Digest         string         `json:"digest"`
	AuthoredDigest string         `json:"authoredDigest,omitempty"`
	Instructions   string         `json:"-"`
	Model          string         `json:"model"`
	Source         []byte         `json:"-"`
}

type Binding struct {
	Harness string
	Config  []byte
}

// ContractBinder can be replaced by a durable-chat-config adapter or an
// addressed harness deployment adapter without changing canvas orchestration.
type ContractBinder interface{ Resolve(Bundle) (Binding, error) }
type LocalBinder struct{}

func (LocalBinder) Resolve(b Bundle) (Binding, error) {
	if len(b.Digest) != 64 {
		return Binding{}, errors.New("invalid portable identity")
	}
	return Binding{Harness: HarnessName(b.Digest, Profile(b))}, nil
}

// HarnessName keeps binding versions explicit: old session request hashes must
// not change when generation constraints improve.
func HarnessName(digest string, profile int) string {
	if profile <= 1 {
		return "bundle-" + digest
	}
	return fmt.Sprintf("bundle-v%d-%s", profile, digest)
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func Load(path string) (Bundle, error) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxArchive {
		return Bundle{}, errors.New("bundle must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Bundle{}, errors.New("cannot read bundle")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return Bundle{}, errors.New("bundle changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxArchive+1))
	if err != nil {
		return Bundle{}, errors.New("cannot read bundle")
	}
	return Parse(data)
}
func Parse(data []byte) (Bundle, error) {
	if isSuiteArchive(data) {
		return parseArchive(data)
	}
	if len(data) > 65536 {
		return Bundle{}, errors.New("bundle too large")
	}
	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&node) != nil {
		return Bundle{}, errors.New("invalid bundle YAML")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return Bundle{}, errors.New("bundle must contain one document")
	}
	if err := checkNode(&node); err != nil {
		return Bundle{}, err
	}
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Instructions string `yaml:"instructions"`
			Description  string `yaml:"description"`
			Model        struct {
				Name string `yaml:"name"`
			} `yaml:"model"`
		} `yaml:"spec"`
		Extensions map[string]any `yaml:"extensions"`
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	if strict.Decode(&doc) != nil {
		return Bundle{}, errors.New("unsupported bundle fields")
	}
	if doc.APIVersion != "kmx.kaimahi.dev/v1alpha1" || doc.Kind != "PortableAgent" || !namePattern.MatchString(doc.Metadata.Name) || doc.Spec.Instructions == "" || doc.Spec.Model.Name == "" || len(doc.Extensions) > 0 {
		return Bundle{}, errors.New("unsupported or incomplete core-only bundle")
	}
	if len(node.Content) > 0 {
		for i := 0; i+1 < len(node.Content[0].Content); i += 2 {
			if node.Content[0].Content[i].Value == "extensions" && node.Content[0].Content[i+1].Tag == "!!null" {
				return Bundle{}, errors.New("extensions cannot be null")
			}
		}
	}
	frame := fmt.Sprintf("portable-agent.yaml %d\n", len(data))
	sum := sha256.Sum256(append(append([]byte(frame), data...), '\n'))
	return Bundle{ID: doc.Metadata.Name, Digest: fmt.Sprintf("%x", sum), Instructions: doc.Spec.Instructions, Model: doc.Spec.Model.Name, Source: append([]byte(nil), data...)}, nil
}
func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("aliases and anchors unsupported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if k == "<<" || seen[k] {
				return errors.New("duplicate or merge keys unsupported")
			}
			seen[k] = true
		}
	}
	for _, child := range n.Content {
		if err := checkNode(child); err != nil {
			return err
		}
	}
	return nil
}
