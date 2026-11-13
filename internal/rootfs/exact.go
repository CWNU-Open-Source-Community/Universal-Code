package rootfs

import (
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
