//go:build windows

package application

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeMCPStateCreationRejectsPreexistingJunction(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "redirected")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside)
	hideSubprocessWindow(command)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("controlled temporary fixture junction: %v %s", err, output)
	}
	if err := prepareNativeMCPDirectory(filepath.Join(link, "must-not-create")); err == nil {
		t.Fatal("existing junction accepted as native state directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "must-not-create")); !os.IsNotExist(err) {
		t.Fatal("directory creation escaped into junction target", err)
	}
}
