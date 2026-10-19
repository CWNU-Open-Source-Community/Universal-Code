//go:build windows

package workspace

import "golang.org/x/sys/windows"

func userDocumentsDirectory() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Documents, windows.KF_FLAG_DEFAULT)
}
