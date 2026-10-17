//go:build !windows

package runner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandRuntimePOSIXGoBuildCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(home, "ssh-agent"))
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-credential")
	for _, source := range []string{"home", "xdg", "inherited", "explicit"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("GOCACHE", "")
			t.Setenv("XDG_CACHE_HOME", "")
			want := filepath.Join(home, ".cache", "go-build")
			if runtime.GOOS == "darwin" {
				want = filepath.Join(home, "Library", "Caches", "go-build")
			}
			var explicit []CommandRuntimeEnvironment
			switch source {
			case "xdg":
				cache := t.TempDir()
				t.Setenv("XDG_CACHE_HOME", cache)
				if runtime.GOOS != "darwin" {
					want = filepath.Join(cache, "go-build")
				}
			case "inherited":
				want = t.TempDir()
				t.Setenv("GOCACHE", want)
			case "explicit":
				t.Setenv("GOCACHE", t.TempDir())
				want = t.TempDir()
				explicit = []CommandRuntimeEnvironment{{Name: "GOCACHE", Value: want}}
			}
			_, environment, _, err := normalizeCommandRuntimeEnvironment(explicit, CommandRuntimeNetworkDisabled)
			if err != nil {
				t.Fatal(err)
			}
			for _, withoutProxy := range []bool{false, true} {
				platform := commandRuntimePlatformEnvironment
				if withoutProxy {
					platform = commandRuntimePlatformEnvironmentWithoutProxy
				}
				resolved := platform("", "", append([]string(nil), environment...), CommandRuntimeNetworkDisabled, explicit)
				values := make(map[string]string)
				for _, entry := range resolved {
					name, value, _ := strings.Cut(entry, "=")
					values[name] = value
				}
				if values["GOCACHE"] != want {
					t.Fatalf("GOCACHE=%q, want %q", values["GOCACHE"], want)
				}
				for _, name := range []string{"HOME", "XDG_CACHE_HOME", "SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY"} {
					if values[name] != "" {
						t.Fatalf("ambient %s leaked into child environment", name)
					}
				}
			}
		})
	}
}

func TestCommandRuntimePOSIXGoBuildCacheUnavailable(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, err := os.UserCacheDir(); err == nil {
		t.Skip("platform has another default cache location")
	}
	environment := commandRuntimePlatformEnvironment("", "", []string{"HOME="}, CommandRuntimeNetworkDisabled, nil)
	if len(environment) != 1 || environment[0] != "HOME=" {
		t.Fatalf("unavailable cache produced a fabricated path: %v", environment)
	}
}
