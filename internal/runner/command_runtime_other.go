//go:build !windows

package runner

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/hostproxy"
)

func resolveCommandRuntimeShell(profile CommandRuntimeProfile) (string, error) {
	var candidates []string
	switch profile {
	case CommandRuntimeBash:
		candidates = []string{"/bin/bash", "/usr/bin/bash", "/opt/homebrew/bin/bash"}
	case CommandRuntimePowerShell:
		candidates = []string{"/usr/bin/pwsh", "/usr/local/bin/pwsh", "/opt/homebrew/bin/pwsh"}
	default:
		return "", ErrCommandRuntimeBoundary
	}
	for _, path := range candidates {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return filepath.Clean(path), nil
		}
	}
	return "", ErrCommandRuntimeUnavailable
}

func commandRuntimeNativeExecutableAllowed(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	blocked := map[string]struct{}{
		"sh": {}, "bash": {}, "dash": {}, "zsh": {}, "fish": {}, "ksh": {},
		"csh": {}, "tcsh": {}, "pwsh": {}, "powershell": {}, "env": {},
		"busybox": {},
		"xargs":   {}, "sudo": {}, "su": {}, "doas": {}, "pkexec": {},
	}
	_, found := blocked[base]
	return !found
}

func commandRuntimeInheritedEnvironmentNames() []string {
	return []string{
		"PATH", "TMPDIR", "LANG", "LC_ALL", "GOROOT", "GOPATH", "GOCACHE",
		"GOMODCACHE", "RUSTUP_HOME",
	}
}

func commandRuntimeFixedEnvironment() []string {
	return []string{
		"CI=1", "NO_COLOR=1", "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null",
		"GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=diff.external", "GIT_CONFIG_VALUE_3=",
		"GIT_CONFIG_KEY_4=core.pager", "GIT_CONFIG_VALUE_4=cat",
		"GIT_LFS_SKIP_SMUDGE=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_ALLOW_PROTOCOL=file", "GIT_ASKPASS=false", "SSH_ASKPASS=false",
		"GIT_EDITOR=false", "GIT_SEQUENCE_EDITOR=false", "GIT_EXTERNAL_DIFF=",
		"GOPROXY=off", "GOSUMDB=off", "CARGO_NET_OFFLINE=true",
		"NPM_CONFIG_OFFLINE=true", "PIP_NO_INDEX=1", "UV_OFFLINE=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1", "POWERSHELL_TELEMETRY_OPTOUT=1",
		"HOME=", "SSH_AUTH_SOCK=",
	}
}

func commandRuntimePlatformEnvironment(_ string, _ string,
	environment []string, _ CommandRuntimeNetwork, _ []CommandRuntimeEnvironment,
) []string {
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if name == "GOCACHE" && value != "" {
			return environment
		}
	}
	// HOME is cleared in the child. Preserve Go's default build cache by
	// resolving its path here, without exposing the operator's home config.
	if cacheRoot, err := os.UserCacheDir(); err == nil {
		environment = replaceCommandRuntimeEnvironment(environment, "GOCACHE",
			filepath.Join(cacheRoot, "go-build"))
	}
	return environment
}

func commandRuntimePlatformEnvironmentWithoutProxy(_ string, _ string,
	environment []string, _ CommandRuntimeNetwork, _ []CommandRuntimeEnvironment,
) []string {
	return commandRuntimePlatformEnvironment("", "", environment, "", nil)
}

func commandRuntimePlatformHostProxy() (hostproxy.Config, bool) {
	return hostproxy.Config{}, false
}

func commandRuntimeExecutableAttributes(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ErrCommandRuntimeBoundary
	}
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return ErrCommandRuntimeBoundary
	}
	magic := binary.BigEndian.Uint32(header[:])
	switch magic {
	case 0x7f454c46, // ELF
		0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, // Mach-O
		0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca: // universal Mach-O
		return nil
	default:
		return ErrCommandRuntimeBoundary
	}
}

func commandRuntimePathEqual(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
