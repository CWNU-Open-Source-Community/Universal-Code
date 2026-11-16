package rootfs

import (
	"io/fs"
	"os"
	"path/filepath"
)

// EvalExactSymlinks resolves a path without losing the Windows spelling used
// to address a name that ends with a space or a dot.
func EvalExactSymlinks(path string) (string, error) {
	native, err := ExactAbsolutePath(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(native)
	if err != nil {
		return "", err
	}
	return ExactAbsolutePath(resolved)
}

// ExactRoot keeps file operations rooted without treating spaces as path syntax.
// Windows needs handle-relative operations for names that Win32 would normalize.
type ExactRoot struct{ *os.Root }

// FS exposes slash-separated paths and keeps directory traversal exact on Windows.
func (r *ExactRoot) FS() fs.FS {
	return exactRootFS{root: r}
}

type exactRootFS struct{ root *ExactRoot }

func (f exactRootFS) Open(name string) (fs.File, error) {
	local, err := filepath.Localize(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return f.root.OpenReadOnly(local)
}

func OpenExactRoot(path string) (*ExactRoot, error) {
	native, err := ExactAbsolutePath(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(native)
	if err != nil {
		return nil, err
	}
	return &ExactRoot{root}, nil
}
