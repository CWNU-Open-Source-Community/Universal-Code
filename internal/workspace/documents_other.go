//go:build !windows

package workspace

import (
	"os"
	"path/filepath"
)

func userDocumentsDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}
