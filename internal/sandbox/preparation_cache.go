package sandbox

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/projectprep"
)

// PreparedGoModuleCache selects only a cache beneath the already granted
// workspace. The nearest prepared project ancestor supports tests in a package
// subdirectory. This changes filesystem inputs, never network authority.
func PreparedGoModuleCache(root, directory string) (string, error) {
	if !filepath.IsAbs(root) || path.IsAbs(directory) || strings.Contains(directory, `\`) || path.Clean(directory) != directory || directory == ".." || strings.HasPrefix(directory, "../") {
		return "", errors.New("prepared module cache location is invalid")
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer opened.Close()
	for current := directory; ; current = path.Dir(current) {
		relative := projectprep.GoModuleCacheDirectory(current)
		if info, err := opened.Lstat(relative); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("prepared module cache is linked")
			}
			for ancestor := path.Dir(relative); ancestor != "."; ancestor = path.Dir(ancestor) {
				info, err := opened.Lstat(ancestor)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return "", errors.New("prepared module cache parent is linked")
				}
			}
			return relative, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if current == "." {
			return "", nil
		}
	}
}
