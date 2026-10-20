package authoring

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/agent-primitives/canvas/internal/contract"
	"go.yaml.in/yaml/v3"
)

var ErrBinding = errors.New("binding requires an existing agent slot, a complete matching local released suite and an unused safe destination")

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// BindDraft edits an inert copy only. Supplied assets must already be local to
// the definition; no resolver, suite creation, evaluation or runtime is invoked.
func BindDraft(definition, destination, agent string, suite contract.SuiteRef) ([]string, error) {
	if suite.Ref == "" || !packagePath(suite.Layout) || suite.Member == "" || suite.Platform == "" || suite.Directory != "" || suite.Artifact != "" || suite.Digest != "" || destination == "" {
		return nil, ErrBinding
	}
	filename, err := filepath.Abs(definition)
	if err != nil || unlinked(filename) != nil {
		return nil, ErrBinding
	}
	sourceRoot := filepath.Dir(filename)
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nil, ErrBinding
	}
	raw, err := packageReadFile(root, filepath.Base(filename), 65536)
	root.Close()
	if err != nil {
		return nil, ErrBinding
	}
	node, err := definitionNode(raw)
	if err != nil || len(node.Content) != 1 {
		return nil, ErrBinding
	}
	agents := mappingValue(mappingValue(node.Content[0], "spec"), "agents")
	slot := mappingValue(agents, agent)
	oldSuite := mappingValue(slot, "suite")
	if slot == nil || oldSuite == nil {
		return nil, ErrBinding
	}
	var previous contract.SuiteRef
	if oldSuite.Decode(&previous) != nil {
		return nil, ErrBinding
	}
	if previous != suite {
		var replacement yaml.Node
		if replacement.Encode(suite) != nil {
			return nil, ErrBinding
		}
		replacement.HeadComment = oldSuite.HeadComment
		replacement.LineComment = oldSuite.LineComment
		replacement.FootComment = oldSuite.FootComment
		*oldSuite = replacement
		for i := 0; i < len(slot.Content); i += 2 {
			if slot.Content[i].Value == "evaluation" {
				slot.Content = append(slot.Content[:i], slot.Content[i+2:]...)
				break
			}
		}
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if encoder.Encode(node) != nil || encoder.Close() != nil {
		return nil, ErrBinding
	}
	// Discover the UPDATED closure: unused old suite/evaluation assets must not
	// leak into the output merely because they remain in the source workspace.
	files, err := capturePackage(sourceRoot, filepath.Base(filename), encoded.Bytes())
	if err != nil {
		return nil, ErrBinding
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return nil, ErrBinding
	}
	parent := filepath.Dir(absolute)
	if unlinked(parent) != nil {
		return nil, ErrBinding
	}
	if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrBinding
	}
	staging, err := os.MkdirTemp(parent, ".canvas-bind-")
	if err != nil {
		return nil, ErrBinding
	}
	defer os.RemoveAll(staging)
	if writePackageTree(staging, files) != nil {
		return nil, ErrBinding
	}
	if _, _, err := compilePackage(staging, filepath.Base(filename)); err != nil {
		return nil, ErrBinding
	}
	// Reuse Init's exclusive reservation and inode-checked publication cleanup.
	if publish(os.DirFS(staging), absolute) != nil {
		return nil, ErrBinding
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	return paths, nil
}
