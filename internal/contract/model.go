package contract

import (
	"bytes"
	"errors"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ForModel derives an execution contract, never edits the authored source. The
// exact derived bytes get their own digest and are archived like any contract;
// existing harness profiles and old request hashes remain unchanged.
func ForModel(b Bundle, model string) (Bundle, error) {
	if b.Suite != nil || isSuiteArchive(b.Source) {
		return forSuiteModel(b, model)
	}
	parsed, err := Parse(b.Source)
	if err != nil || parsed.ID != b.ID || parsed.Digest != b.Digest || parsed.Model != b.Model || parsed.Instructions != b.Instructions {
		return Bundle{}, errors.New("invalid authored bundle identity")
	}
	if model == "" || len(model) > 160 || strings.ContainsAny(model, "\r\n") {
		return Bundle{}, errors.New("invalid selected model")
	}
	if model == b.Model {
		if b.AuthoredDigest == "" {
			b.AuthoredDigest = b.Digest
		}
		return b, nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(b.Source, &doc) != nil {
		return Bundle{}, errors.New("cannot decode execution contract")
	}
	node := doc.Content[0]
	for _, key := range []string{"spec", "model", "name"} {
		var child *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				child = node.Content[i+1]
				break
			}
		}
		if child == nil {
			return Bundle{}, errors.New("execution contract model missing")
		}
		node = child
	}
	node.Value = model
	node.Style = 0
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if encoder.Encode(&doc) != nil || encoder.Close() != nil {
		return Bundle{}, errors.New("cannot encode execution contract")
	}
	selected, err := Parse(buffer.Bytes())
	if err != nil {
		return Bundle{}, err
	}
	selected.AuthoredDigest = b.AuthoredDigest
	if selected.AuthoredDigest == "" {
		selected.AuthoredDigest = b.Digest
	}
	return selected, nil
}
