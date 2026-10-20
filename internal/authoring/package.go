package authoring

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/canvas/internal/canvas"
	"go.yaml.in/yaml/v3"
)

var ErrPackage = errors.New("invalid, unsafe or incomplete frozen package")

const maxPackageFiles = 1024
const maxPackageFile = 8 << 20
const maxPackageBytes = 32 << 20

// File describes canonical wrapper metadata, not modes embedded inside artifacts.
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}
type Package struct {
	ID               string `json:"id"`
	Definition       string `json:"definition"`
	DefinitionDigest string `json:"definitionDigest"`
	RuntimeMode      string `json:"runtimeMode"`
	Files            []File `json:"files"`
	SourceRoot       string `json:"-"`
}

type Packages struct{ privateRoot string }

// This canonical store record never contains its own hash or a source path.
type packageManifest struct {
	Definition       string `json:"definition"`
	DefinitionDigest string `json:"definitionDigest"`
	Files            []File `json:"files"`
	RuntimeMode      string `json:"runtimeMode"`
}

// OpenPackages opens private storage, not a draft or runtime. No source location
// is persisted: a retained package must remain usable after its draft is deleted.
func OpenPackages(root string) (*Packages, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrPackage
	}
	// Check existing ancestors BEFORE creating any missing suffix.
	for existing := absolute; ; existing = filepath.Dir(existing) {
		if _, e := os.Lstat(existing); errors.Is(e, os.ErrNotExist) {
			continue
		}
		if unlinked(existing) != nil {
			return nil, ErrPackage
		}
		break
	}
	if err = os.MkdirAll(absolute, 0700); err != nil || unlinked(absolute) != nil {
		return nil, ErrPackage
	}
	storage := filepath.Join(absolute, "packages")
	if err = os.MkdirAll(storage, 0700); err != nil || unlinked(storage) != nil {
		return nil, ErrPackage
	}
	return &Packages{privateRoot: storage}, nil
}

func unlinked(filename string) error {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return ErrPackage
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrPackage
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
func packagePath(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || len(part) > 255 {
			return false
		}
	}
	return true
}
func packageHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func validHash(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}

// A rooted, identity-checked reader is shared by capture and retained reads.
func packageReadFile(root *os.Root, name string, limit int64) ([]byte, error) {
	if !packagePath(name) {
		return nil, ErrPackage
	}
	for current := name; current != "."; current = path.Dir(current) {
		info, err := root.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrPackage
		}
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrPackage
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrPackage
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, ErrPackage
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) != info.Size() || int64(len(raw)) > limit {
		return nil, ErrPackage
	}
	return raw, nil
}

func definitionNode(raw []byte) (*yaml.Node, error) {
	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var extra any
	if dec.Decode(&node) != nil || dec.Decode(&extra) != io.EOF || !uniqueYAML(&node) {
		return nil, ErrPackage
	}
	return &node, nil
}
func uniqueYAML(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!null" {
		return false
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
				return false
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if !uniqueYAML(child) {
			return false
		}
	}
	return true
}

// Discover only declared filesystem dependencies. The actual compiler remains
// authoritative for syntax, schemas, pins, profiles and the complete definition.
func capturePackage(rootPath, definition string, replacement []byte) (map[string][]byte, error) {
	if !packagePath(definition) || unlinked(rootPath) != nil {
		return nil, ErrPackage
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, ErrPackage
	}
	defer root.Close()
	raw := replacement
	if raw == nil {
		raw, err = packageReadFile(root, definition, 65536)
		if err != nil {
			return nil, ErrPackage
		}
	}
	if len(raw) > 65536 {
		return nil, ErrPackage
	}
	node, err := definitionNode(raw)
	if err != nil {
		return nil, ErrPackage
	}
	var config struct {
		Spec struct {
			Documents map[string]canvas.DocumentDefinition `yaml:"documents"`
			Seed      map[string]string                    `yaml:"seed"`
			Agents    map[string]canvas.AgentDefinition    `yaml:"agents"`
			Flows     map[string]canvas.FlowDefinition     `yaml:"flows"`
		} `yaml:"spec"`
	}
	if node.Decode(&config) != nil {
		return nil, ErrPackage
	}
	files := map[string][]byte{}
	seen := map[string]string{}
	total := 0
	register := func(name string) error {
		if !packagePath(name) {
			return ErrPackage
		}
		for current := name; current != "."; current = path.Dir(current) {
			folded := strings.ToLower(current)
			if previous, ok := seen[folded]; ok && previous != current {
				return ErrPackage
			}
			seen[folded] = current
		}
		return nil
	}
	add := func(name string, data []byte) error {
		if register(name) != nil {
			return ErrPackage
		}
		if _, ok := files[name]; ok {
			return nil
		}
		if len(data) > maxPackageFile || len(files) >= maxPackageFiles || total+len(data) > maxPackageBytes {
			return ErrPackage
		}
		files[name] = data
		total += len(data)
		return nil
	}
	if add(definition, raw) != nil {
		return nil, ErrPackage
	}
	capture := func(value string, recursive bool) error {
		if !packagePath(value) {
			return ErrPackage
		}
		name := path.Join(path.Dir(definition), value)
		if register(name) != nil {
			return ErrPackage
		}
		// Check every ancestor before walking; OpenRoot also confines opens if names
		// change while capturing. Symlinks are never treated as portable assets.
		for current := name; current != "."; current = path.Dir(current) {
			info, e := root.Lstat(current)
			if e != nil || info.Mode()&os.ModeSymlink != 0 {
				return ErrPackage
			}
		}
		return fs.WalkDir(root.FS(), name, func(file string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || register(file) != nil {
				return ErrPackage
			}
			info, e := root.Lstat(file)
			if e != nil {
				return ErrPackage
			}
			if info.IsDir() {
				if !recursive {
					return ErrPackage
				}
				return nil
			}
			if _, ok := files[file]; ok {
				return nil
			}
			data, e := packageReadFile(root, file, maxPackageFile)
			if e != nil {
				return ErrPackage
			}
			return add(file, data)
		})
	}
	for _, doc := range config.Spec.Documents {
		if doc.Schema != "" && capture(doc.Schema, false) != nil {
			return nil, ErrPackage
		}
	}
	for _, seed := range config.Spec.Seed {
		if capture(seed, false) != nil {
			return nil, ErrPackage
		}
	}
	for _, flow := range config.Spec.Flows {
		if capture(flow.Inputs.Schema, false) != nil {
			return nil, ErrPackage
		}
	}
	for _, agent := range config.Spec.Agents {
		for _, eval := range []string{agent.Evaluation.Expected, agent.Evaluation.Evidence} {
			if eval != "" && capture(eval, false) != nil {
				return nil, ErrPackage
			}
		}
		ref := agent.Suite
		if ref.Ref != "" {
			if capture(ref.Layout, true) != nil {
				return nil, ErrPackage
			}
		} else {
			if capture(ref.Directory, true) != nil || capture(ref.Artifact, true) != nil {
				return nil, ErrPackage
			}
		}
	}
	return files, nil
}

func writePackageTree(root string, files map[string][]byte) error {
	for name, raw := range files {
		filename := filepath.Join(root, filepath.FromSlash(name))
		directory := filepath.Dir(filename)
		if os.MkdirAll(directory, 0755) != nil {
			return ErrPackage
		}
		// These are exclusively owned staging directories. Set portable wrapper
		// modes explicitly rather than inheriting the operator's umask.
		for dir := directory; ; dir = filepath.Dir(dir) {
			if os.Chmod(dir, 0755) != nil {
				return ErrPackage
			}
			if dir == root {
				break
			}
		}
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return ErrPackage
		}
		_, writeErr := file.Write(raw)
		modeErr := file.Chmod(0644)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil {
			return ErrPackage
		}
	}
	return nil
}
func compilePackage(root, definition string) (string, string, error) {
	compiled, err := canvas.LoadGenericDefinition(filepath.Join(root, filepath.FromSlash(definition)))
	if err != nil {
		return "", "", ErrPackage
	}
	mode := "manual"
	if len(compiled.Flows) > 0 {
		mode = "flow"
	}
	return compiled.Digest, mode, nil
}

// Identity deliberately excludes ID and compiled metadata: no self-reference,
// and the compiler digest remains a separate, checked execution identity.
func packageID(p Package) string {
	raw, _ := json.Marshal(struct {
		Definition string `json:"definition"`
		Files      []File `json:"files"`
	}{p.Definition, p.Files})
	return packageHash(raw)
}
func (s *Packages) Prepare(draftRoot, definition string) (Package, error) {
	if unlinked(s.privateRoot) != nil {
		return Package{}, ErrPackage
	}
	files, err := capturePackage(draftRoot, definition, nil)
	if err != nil {
		return Package{}, ErrPackage
	}
	staging, err := os.MkdirTemp(s.privateRoot, ".prepare-")
	if err != nil {
		return Package{}, ErrPackage
	}
	defer os.RemoveAll(staging)
	source := filepath.Join(staging, "files")
	if os.Mkdir(source, 0755) != nil || writePackageTree(source, files) != nil {
		return Package{}, ErrPackage
	}
	digest, mode, err := compilePackage(source, definition)
	if err != nil {
		return Package{}, ErrPackage
	}
	p := Package{Definition: definition, DefinitionDigest: digest, RuntimeMode: mode, Files: []File{}}
	for name, raw := range files {
		p.Files = append(p.Files, File{Path: name, SHA256: packageHash(raw), Size: int64(len(raw)), Mode: 0644})
	}
	slices.SortFunc(p.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	p.ID = packageID(p)
	// The manifest has no ID field: the directory name is checked against the
	// canonical definition/inventory material on every load.
	raw, err := json.Marshal(packageManifest{p.Definition, p.DefinitionDigest, p.Files, p.RuntimeMode})
	if err != nil || writePackageTree(staging, map[string][]byte{"manifest.json": raw}) != nil || os.Chmod(staging, 0755) != nil || syncPackageDirs(staging) != nil {
		return Package{}, ErrPackage
	}
	destination := filepath.Join(s.privateRoot, p.ID)
	if _, err := os.Lstat(destination); err == nil {
		return s.Load(p.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Package{}, ErrPackage
	}
	if os.Rename(staging, destination) != nil {
		return s.Load(p.ID)
	}
	parent, err := os.Open(s.privateRoot)
	if err != nil {
		return Package{}, ErrPackage
	}
	syncErr, closeErr := parent.Sync(), parent.Close()
	if syncErr != nil || closeErr != nil {
		return Package{}, ErrPackage
	}
	return s.Load(p.ID)
}
func syncPackageDirs(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return ErrPackage
		}
		if !entry.IsDir() {
			return nil
		}
		f, err := os.Open(name)
		if err != nil {
			return ErrPackage
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		return errors.Join(syncErr, closeErr)
	})
}

func (s *Packages) verified(id string) (Package, map[string][]byte, error) {
	if !validHash(id) || unlinked(s.privateRoot) != nil {
		return Package{}, nil, ErrPackage
	}
	directory := filepath.Join(s.privateRoot, id)
	if unlinked(directory) != nil {
		return Package{}, nil, ErrPackage
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Package{}, nil, ErrPackage
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm() != 0755 {
		return Package{}, nil, ErrPackage
	}
	raw, err := packageReadFile(root, "manifest.json", 1<<20)
	if err != nil {
		return Package{}, nil, ErrPackage
	}
	// Canonical encoding rejects duplicates, unknown keys, alternate field types,
	// trailing JSON and unexpected nulls in addition to ordinary decoder checks.
	var manifest packageManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&manifest) != nil {
		return Package{}, nil, ErrPackage
	}
	canonical, _ := json.Marshal(manifest)
	if !bytes.Equal(raw, canonical) {
		return Package{}, nil, ErrPackage
	}
	p := Package{ID: id, Definition: manifest.Definition, DefinitionDigest: manifest.DefinitionDigest, RuntimeMode: manifest.RuntimeMode, Files: manifest.Files, SourceRoot: filepath.Join(directory, "files")}
	if !packagePath(p.Definition) || !validHash(p.DefinitionDigest) || (p.RuntimeMode != "manual" && p.RuntimeMode != "flow") || len(p.Files) == 0 || len(p.Files) > maxPackageFiles || packageID(p) != id {
		return Package{}, nil, ErrPackage
	}
	files := map[string][]byte{}
	total := int64(0)
	allowed := map[string]bool{".": true, "files": true, "manifest.json": true}
	for i, f := range p.Files {
		if !packagePath(f.Path) || !validHash(f.SHA256) || f.Mode != 0644 || f.Size < 0 || f.Size > maxPackageFile || i > 0 && p.Files[i-1].Path >= f.Path {
			return Package{}, nil, ErrPackage
		}
		total += f.Size
		if total > maxPackageBytes {
			return Package{}, nil, ErrPackage
		}
		name := "files/" + f.Path
		data, e := packageReadFile(root, name, maxPackageFile)
		if e != nil || int64(len(data)) != f.Size || packageHash(data) != f.SHA256 {
			return Package{}, nil, ErrPackage
		}
		info, e := root.Stat(name)
		if e != nil || info.Mode().Perm() != 0644 {
			return Package{}, nil, ErrPackage
		}
		files[f.Path] = data
		for current := name; current != "."; current = path.Dir(current) {
			allowed[current] = true
		}
	}
	if _, ok := files[p.Definition]; !ok {
		return Package{}, nil, ErrPackage
	}
	if fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, e error) error {
		if e != nil || !allowed[name] || entry.Type()&os.ModeSymlink != 0 {
			return ErrPackage
		}
		if name != "." && entry.IsDir() {
			info, e := root.Stat(name)
			if e != nil || info.Mode().Perm() != 0755 {
				return ErrPackage
			}
		}
		return nil
	}) != nil {
		return Package{}, nil, ErrPackage
	}
	closure, err := capturePackage(p.SourceRoot, p.Definition, nil)
	if err != nil || len(closure) != len(files) {
		return Package{}, nil, ErrPackage
	}
	for name, data := range closure {
		if !bytes.Equal(data, files[name]) {
			return Package{}, nil, ErrPackage
		}
	}
	digest, mode, err := compilePackage(p.SourceRoot, p.Definition)
	if err != nil || digest != p.DefinitionDigest || mode != p.RuntimeMode {
		return Package{}, nil, ErrPackage
	}
	return p, files, nil
}
func (s *Packages) Load(id string) (Package, error) { p, _, err := s.verified(id); return p, err }
func (s *Packages) File(id, name string) ([]byte, error) {
	if !packagePath(name) {
		return nil, ErrPackage
	}
	_, files, err := s.verified(id)
	if err != nil {
		return nil, ErrPackage
	}
	data, ok := files[name]
	if !ok {
		return nil, ErrPackage
	}
	return data, nil
}
func (s *Packages) DefinitionPath(id string) (string, error) {
	p, err := s.Load(id)
	if err != nil {
		return "", ErrPackage
	}
	return filepath.Join(p.SourceRoot, filepath.FromSlash(p.Definition)), nil
}
