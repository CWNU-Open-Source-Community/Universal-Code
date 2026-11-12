package fileedit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/tools"
)

// Called only inside the authorized apply boundary. Mkdir is rooted, each
// existing component is revalidated. Failure may retain new empty parents:
// pathname-based rollback cannot safely delete by the original directory's
// identity during external renames. The file still uses no-clobber Link.
func prepareCreateDirectories(root *rootfs.ExactRoot, workspaceRoot, target string) error {
	parent := filepath.Dir(target)
	if parent == "." {
		return nil
	}
	fs := tools.NewWorkspaceFS(workspaceRoot)
	current := ""
	for _, component := range strings.Split(parent, string(os.PathSeparator)) {
		if _, err := fs.ResolveForCreate(target); err != nil {
			return err
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			mkdirErr := root.Mkdir(current, 0o755)
			if mkdirErr != nil && !os.IsExist(mkdirErr) {
				return mkdirErr
			}
			info, err = root.Lstat(current)
			if mkdirErr == nil && err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				syncRootParentDirectory(root, current)
			}
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("create parent is not a real workspace directory")
		}
	}
	_, err := fs.ResolveForWrite(target)
	return err
}
