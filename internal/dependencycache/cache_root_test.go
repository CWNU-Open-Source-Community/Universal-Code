package dependencycache

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewCacheRejectsLinkedApplicationAndCacheDirectories(t *testing.T) {
	for _, child := range []bool{false, true} {
		name := "linked application directory"
		if child {
			name = "linked cache directory"
		}
		t.Run(name, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(parent, "real")
			if err := os.Mkdir(real, 0700); err != nil {
				t.Fatal(err)
			}
			linked := filepath.Join(parent, "linked")
			if runtime.GOOS == "windows" {
				// Directory junctions need no symbolic-link privilege and exercise
				// the same rejection on ordinary Windows developer accounts.
				command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"),
					"/d", "/c", "mklink", "/J", linked, real)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("create directory junction: %v %s", err, output)
				}
			} else if err := os.Symlink(real, linked); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}
			requested := filepath.Join(linked, "dependency-cache")
			if child {
				requested = linked
			}
			if _, err := New(requested); err == nil {
				t.Fatal("application-controlled directory link was accepted")
			}
		})
	}
}
