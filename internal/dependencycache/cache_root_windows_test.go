//go:build windows

package dependencycache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNewCacheAcceptsWindowsPathSpellings(t *testing.T) {
	home := filepath.Join(t.TempDir(), "RunnerAdmin Long Cache Home")
	cache := filepath.Join(home, "dependency-cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(cache)
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, requested string) {
		t.Helper()
		t.Logf("requested=%q canonical=%q", requested, canonical)
		broker, err := New(requested)
		if err != nil {
			t.Fatalf("ordinary cache path spelling rejected: %v", err)
		}
		if broker.root != canonical {
			t.Fatalf("cache not bound to canonical directory: root=%q want=%q", broker.root, canonical)
		}
	}
	t.Run("case spelling", func(t *testing.T) {
		check(t, strings.ToLower(cache))
	})
	t.Run("8.3 parent spelling", func(t *testing.T) {
		path, err := windows.UTF16PtrFromString(home)
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]uint16, 32768)
		n, err := windows.GetShortPathName(path, &buffer[0], uint32(len(buffer)))
		if err != nil || n == 0 || n >= uint32(len(buffer)) {
			t.Fatalf("read native short path: length=%d err=%v", n, err)
		}
		short := windows.UTF16ToString(buffer[:n])
		if strings.EqualFold(short, home) {
			t.Skip("this volume does not generate 8.3 directory names")
		}
		check(t, filepath.Join(short, "dependency-cache"))
	})
}
