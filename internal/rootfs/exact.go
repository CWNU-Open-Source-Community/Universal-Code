package rootfs

import "os"

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
