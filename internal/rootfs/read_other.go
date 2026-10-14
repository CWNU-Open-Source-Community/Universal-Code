//go:build !unix

package rootfs

import "os"

func OpenReadOnly(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
