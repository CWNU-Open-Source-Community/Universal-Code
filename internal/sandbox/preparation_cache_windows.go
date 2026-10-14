//go:build windows

package sandbox

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

func localPreparedGoEnvironment(environment []uint16, moduleCache string) ([]uint16, error) {
	if !validLocalHostRoot(moduleCache) {
		return nil, ErrLocalSandboxBoundary
	}
	values := map[string]string{}
	for _, entry := range strings.Split(string(utf16.Decode(environment)), "\x00") {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	// Go runs vet with each package directory as cwd. Windows child creation
	// rejects cwd longer than MAX_PATH even with an extended path prefix.
	// Keep module extraction in the existing private scratch GOMODCACHE and
	// read verified persistent artifacts through Go's offline file proxy.
	// This one source has no HTTP/VCS fallback or network capability.
	proxy := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(filepath.Join(moduleCache, "cache", "download"))}).String()
	// Commas delimit GOPROXY sources even though they are valid URL paths.
	values["GOPROXY"] = strings.ReplaceAll(proxy, ",", "%2C")
	return localEnvironment(values)
}
