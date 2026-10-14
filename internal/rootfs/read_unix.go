//go:build unix

// Package rootfs contains file operations confined by an existing os.Root.
package rootfs

import (
	"os"
	"syscall"
)

// OpenReadOnly must be followed by a handle-based file type check. O_NONBLOCK
// prevents a FIFO replacement from blocking before that check can run.
func OpenReadOnly(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
