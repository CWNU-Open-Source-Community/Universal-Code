//go:build !windows

package rootfs

import (
	"os"
	"path/filepath"
)

func ExactAbsolutePath(path string) (string, error) { return filepath.Abs(path) }

func OrdinaryPath(path string) string { return path }

func CheckExternalDirectory(string) error { return nil }

func (r *ExactRoot) CreateExclusive(name string, mode os.FileMode) (*os.File, error) {
	return r.Root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}
