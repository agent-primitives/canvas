package sharedspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (s *GitStore) validatePath(path string) error {
	if s.policy != nil {
		if !s.policy.Allows(path) {
			return errors.New("document path is not declared")
		}
	} else if path != "board.md" && path != "board.json" && path != "strategy.md" && path != "league.md" {
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			return errors.New("invalid document path")
		}
		suffix := ""
		switch parts[0] {
		case "prospects":
			suffix = ".md"
		case "stats", "injuries":
			suffix = ".json"
		default:
			return errors.New("unknown document path")
		}
		name := parts[1]
		if !strings.HasSuffix(name, suffix) || len(name) <= len(suffix) || len(name) > 255 || name[0] == '.' || strings.Contains(name, "..") {
			return errors.New("invalid document path")
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return errors.New("invalid document path")
			}
		}
	}
	target := filepath.Join(s.root, filepath.FromSlash(path))
	if e := checkNoSymlinks(s.root, target); e != nil {
		return e
	}
	info, e := os.Lstat(target)
	if e == nil && !info.Mode().IsRegular() {
		return errors.New("document path is not a regular file")
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return errors.New("cannot inspect document path")
	}
	return nil
}

// resolveRoot makes root absolute and follows symlinks in the directories above
// it, so a checkout reached through a linked directory (for example /home ->
// var/home) still works. The root itself is not followed: it and everything
// below it belong to this program and must not be symlinks.
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dir, tail := filepath.Dir(abs), []string{filepath.Base(abs)}
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		}
		// Only a missing directory may be skipped; a dangling link is refused.
		if _, statErr := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) || !errors.Is(statErr, os.ErrNotExist) {
			return "", errors.New("cannot resolve workspace root")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("cannot resolve workspace root")
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
	}
}

// checkNoSymlinks inspects path and each parent up to and including root,
// including dangling symlinks. Missing tail components are fine until they are
// created. Directories above root were resolved once by resolveRoot and are not
// checked again. The workspace is private to this process; a same-user process
// racing to swap paths is outside what this local demo defends against.
func checkNoSymlinks(root, path string) error {
	path = filepath.Clean(path)
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path is outside the workspace")
	}
	for {
		info, e := os.Lstat(path)
		if e == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("symlink path is not allowed")
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return errors.New("non-regular path is not allowed")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return errors.New("cannot inspect private path")
		}
		if path == root {
			return nil
		}
		path = filepath.Dir(path)
	}
}
func ensureDirectory(root, path string) error {
	if e := checkNoSymlinks(root, path); e != nil {
		return e
	}
	if e := os.MkdirAll(path, 0700); e != nil {
		return errors.New("cannot create private directory")
	}
	info, e := os.Stat(path)
	if e != nil || !info.IsDir() {
		return errors.New("private directory is not a directory")
	}
	return nil
}
func atomicWrite(root, path string, data []byte) error {
	_, err := publishFile(root, path, data, syncDirectory)
	return err
}

// Publication and crash durability are separate: a visible input can trigger
// work even if the directory sync fails after rename.
func publishFile(root, path string, data []byte, syncDir func(string) error) (bool, error) {
	if e := checkNoSymlinks(root, path); e != nil {
		return false, e
	}
	file, e := os.CreateTemp(filepath.Dir(path), ".sharedspace-write-")
	if e != nil {
		return false, errors.New("cannot create private temporary file")
	}
	name := file.Name()
	defer os.Remove(name)
	if _, e = file.Write(data); e != nil {
		file.Close()
		return false, errors.New("cannot write private file")
	}
	if e = file.Sync(); e != nil {
		file.Close()
		return false, errors.New("cannot sync private file")
	}
	if e = file.Close(); e != nil {
		return false, errors.New("cannot close private file")
	}
	if e = checkNoSymlinks(root, path); e != nil {
		return false, e
	}
	if e = os.Rename(name, path); e != nil {
		return false, errors.New("cannot publish private file")
	}
	return true, syncDir(filepath.Dir(path))
}
func syncDirectory(path string) error {
	dir, e := os.Open(path)
	if e != nil {
		return errors.New("cannot open private directory")
	}
	defer dir.Close()
	if e = dir.Sync(); e != nil {
		return errors.New("cannot sync private directory")
	}
	return nil
}
