// Package authoring builds inert, portable Canvas definition packages.
package authoring

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/agent-primitives/canvas/internal/canvas"
)

var (
	ErrOptions     = errors.New("choose single-review or writer-review and a name of 1–63 lowercase letters, digits or hyphens, starting with a letter")
	ErrSource      = errors.New("fixture assets unavailable or invalid; --root must select a Canvas source checkout containing examples/northwind-released/layouts")
	ErrDestination = errors.New("destination must be a new directory under an existing parent; existing files, directories and links are never overwritten")
	ErrPublish     = errors.New("cannot publish the complete package; inspect the destination before retrying")
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// Init copies reviewed fixture assets from a checkout into a standalone package.
// It validates staged bytes before reserving a new destination; it never runs Canvas.
func Init(sourceRoot, destination, template, name string) ([]string, error) {
	if !namePattern.MatchString(name) || template != "single-review" && template != "writer-review" {
		return nil, ErrOptions
	}
	if destination == "" {
		return nil, ErrDestination
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return nil, ErrDestination
	}
	parent := filepath.Dir(absolute)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		// The strict definition loader must be able to reopen the published path.
		return nil, ErrDestination
	}
	destination = absolute
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrDestination
	}
	files := templateFiles(template, name)
	agents := []string{"writer"}
	if template == "writer-review" {
		agents = append(agents, "reviewer")
	}
	for _, agent := range agents {
		source := filepath.Join(sourceRoot, "examples", "northwind-released", "layouts", agent)
		if err := readLayout(source, "layouts/"+agent, files); err != nil {
			return nil, ErrSource
		}
	}
	// The strict loader rejects symlinked ancestors, including system temp aliases.
	staging, err := os.MkdirTemp(parent, ".canvas-init-")
	if err != nil {
		return nil, ErrPublish
	}
	defer os.RemoveAll(staging)
	for path, data := range files {
		filename := filepath.Join(staging, filepath.FromSlash(path))
		if os.MkdirAll(filepath.Dir(filename), 0755) != nil || os.WriteFile(filename, data, 0644) != nil {
			return nil, ErrPublish
		}
	}
	if _, err := canvas.LoadGenericDefinition(filepath.Join(staging, "canvas.yaml")); err != nil {
		return nil, ErrSource
	}
	if err := publish(os.DirFS(staging), destination); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func readLayout(source, prefix string, files map[string][]byte) error {
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() {
		return ErrSource
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return ErrSource
	}
	defer root.Close()
	count, total := 0, 0
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return ErrSource
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return ErrSource
		}
		count++
		total += int(info.Size())
		if count > 128 || total > 4<<20 {
			return ErrSource
		}
		f, err := root.Open(path)
		if err != nil {
			return ErrSource
		}
		data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(data) > 1<<20 {
			return ErrSource
		}
		files[prefix+"/"+path] = data
		return nil
	})
}

func publish(source fs.FS, destination string) (err error) {
	if os.Mkdir(destination, 0755) != nil {
		return ErrDestination
	}
	owned, statErr := os.Lstat(destination)
	if statErr != nil {
		return ErrPublish
	}
	defer func() {
		if err != nil {
			// Never remove an existing or swapped destination on failure.
			if current, e := os.Lstat(destination); e == nil && current.IsDir() && os.SameFile(owned, current) {
				_ = os.RemoveAll(destination)
			}
		}
	}()
	output, openErr := os.OpenRoot(destination)
	if openErr != nil {
		return ErrPublish
	}
	defer output.Close()
	opened, statErr := output.Stat(".")
	if statErr != nil || !os.SameFile(owned, opened) {
		return ErrPublish
	}
	// Anchor writes and permissions to the directory we reserved, not its name.
	if fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return ErrPublish
		}
		if entry.IsDir() {
			if err := output.MkdirAll(path, 0755); err != nil {
				return err
			}
			return output.Chmod(path, 0755)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrPublish
		}
		input, err := source.Open(path)
		if err != nil {
			return err
		}
		file, err := output.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(file, input)
		modeErr := file.Chmod(0644)
		inputErr, outputErr := input.Close(), file.Close()
		return errors.Join(copyErr, modeErr, inputErr, outputErr)
	}) != nil {
		return ErrPublish
	}
	current, statErr := os.Lstat(destination)
	if statErr != nil || !current.IsDir() || !os.SameFile(owned, current) {
		return ErrPublish
	}
	return nil
}
