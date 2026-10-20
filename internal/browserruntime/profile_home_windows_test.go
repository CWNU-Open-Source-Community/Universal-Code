package browserruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareFullCDPProfileRuntimeRootAcceptsWindowsSeparators(t *testing.T) {
	for _, separator := range []string{"native", "forward", "mixed"} {
		t.Run(separator, func(t *testing.T) {
			home := directTestPath(t, t.TempDir())
			input := home
			switch separator {
			case "forward":
				input = filepath.ToSlash(home)
			case "mixed":
				input = strings.Replace(home, `\`, "/", 1)
			}

			root, err := PrepareFullCDPProfileRuntimeRoot(input)
			if err != nil {
				t.Fatalf("prepare home %q: %v", input, err)
			}
			want := filepath.Join(home, "runtime", "full-cdp", ProfileRuntimeRootName)
			if root != want || !profilePathHasNoIndirection(root) {
				t.Fatalf("root = %q, want direct path %q", root, want)
			}
		})
	}
}

func TestPrepareFullCDPProfileRuntimeRootRejectsUncleanWindowsHome(t *testing.T) {
	for _, suffix := range []string{"/.", "/child/..", "//"} {
		t.Run(suffix, func(t *testing.T) {
			home := directTestPath(t, t.TempDir())
			if err := os.Mkdir(filepath.Join(home, "child"), 0o700); err != nil {
				t.Fatal(err)
			}
			input := filepath.ToSlash(home) + suffix
			if _, err := PrepareFullCDPProfileRuntimeRoot(input); err == nil {
				t.Fatalf("accepted unclean home %q", input)
			}
			if _, err := os.Stat(filepath.Join(home, "runtime")); !os.IsNotExist(err) {
				t.Fatalf("invalid home created a runtime directory: %v", err)
			}
		})
	}
}
