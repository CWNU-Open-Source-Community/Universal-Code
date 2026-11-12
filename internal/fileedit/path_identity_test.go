package fileedit

import "testing"

func TestNormalizePathRejectsSurroundingWhitespace(t *testing.T) {
	for _, path := range []string{" file.txt", "file.txt ", "\tfile.txt"} {
		if got, err := normalizePath(path); err == nil {
			t.Errorf("normalizePath(%q) redirected to %q instead of rejecting it", path, got)
		}
	}
}
