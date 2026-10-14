//go:build windows

package dependencycache

import (
	"os"
	"path/filepath"
)

func cacheRootPathMatches(requested, canonical string) bool {
	// EvalSymlinks also expands native 8.3 names and corrects case on Windows.
	// Check every actual directory first so accepting those spelling changes
	// never permits a parent or leaf junction or symbolic link.
	for current := requested; ; {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	requestedInfo, err := os.Stat(requested)
	if err != nil {
		return false
	}
	canonicalInfo, err := os.Stat(canonical)
	return err == nil && os.SameFile(requestedInfo, canonicalInfo)
}
