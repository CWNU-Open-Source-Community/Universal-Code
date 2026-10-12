//go:build windows

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestWindowsLocalToolchainPreflightGoBinAvailableToNestedProcesses(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "go.exe"), []byte("PATH inspection does not execute this file"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := buildLocalEnvironment(filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "tmp"), []localPinnedRoot{{path: root}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range strings.Split(string(utf16.Decode(values)), "\x00") {
		if strings.HasPrefix(value, "PATH=") {
			if !strings.Contains(value, filepath.Join(root, "bin")) {
				t.Fatalf("nested go process cannot find its pinned toolchain: %s", value)
			}
			return
		}
	}
	t.Fatal("isolated PATH is missing")
}
