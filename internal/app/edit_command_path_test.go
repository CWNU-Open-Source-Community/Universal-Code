package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEditContentRejectsSurroundingWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{" " + path, path + " ", "\t" + path} {
		if content, err := readEditContent(requested); err == nil {
			t.Errorf("readEditContent(%q) silently read %q", requested, content)
		}
	}
}
