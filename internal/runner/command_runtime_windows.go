//go:build windows

package runner

import (
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"cyberagent-workbench/internal/hostproxy"
	"cyberagent-workbench/internal/redact"

	"golang.org/x/sys/windows"
)

var (
	commandRuntimeWinHTTPCurrentUserProxy = windows.NewLazySystemDLL("winhttp.dll").
						NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	commandRuntimeGlobalFree = windows.NewLazySystemDLL("kernel32.dll").
					NewProc("GlobalFree")
)

type commandRuntimeCurrentUserProxyConfig struct {
	AutoDetect    int32
	AutoConfigURL *uint16
	Proxy         *uint16
	ProxyBypass   *uint16
}

func resolveCommandRuntimeShell(profile CommandRuntimeProfile) (string, error) {
	candidates := make([]string, 0, 12)
	switch profile {
	case CommandRuntimePowerShell:
		// Runtime selection belongs to the trusted host process, never the
		// per-command environment supplied by a model or project.
		if configured := os.Getenv("CYBERAGENT_POWERSHELL_PATH"); configured != "" {
			return resolveCommandRuntimeConfiguredPowerShell(configured)
		}
		if executable, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(executable), "runtimes", "powershell7", "pwsh.exe"))
		}
		for _, root := range controlledKnownFolders(windows.FOLDERID_ProgramFiles,
			windows.FOLDERID_ProgramFilesX64, windows.FOLDERID_ProgramFilesX86) {
			candidates = append(candidates, filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
		}
		if root, err := controlledWindowsDirectory(); err == nil {
			candidates = append(candidates, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
		}
	case CommandRuntimeBash:
		if gitPath, err := exec.LookPath("git.exe"); err == nil {
			if root, ok := commandRuntimeGitDistributionRoot(gitPath); ok {
				candidates = append(candidates, filepath.Join(root, "bin", "bash.exe"))
			}
		}
		roots := controlledKnownFolders(windows.FOLDERID_ProgramFiles,
			windows.FOLDERID_ProgramFilesX64, windows.FOLDERID_ProgramFilesX86,
			windows.FOLDERID_LocalAppData)
		for _, root := range roots {
			candidates = append(candidates, filepath.Join(root, "Git", "bin", "bash.exe"),
				filepath.Join(root, "Programs", "Git", "bin", "bash.exe"))
		}
	default:
		return "", ErrCommandRuntimeBoundary
	}
	for _, candidate := range candidates {
		path, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		pointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			continue
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err == nil && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
			// Resolve directory aliases before applying the common Workspace
			// exclusion and pinning the executable identity.
			return resolveCommandRuntimeProcess(path)
		}
	}
	return "", ErrCommandRuntimeUnavailable
}

func resolveCommandRuntimeConfiguredPowerShell(value string) (string, error) {
	value = strings.TrimSpace(value)
	volume := filepath.VolumeName(value)
	name := filepath.Base(value)
	if !validCommandRuntimeText(value, false) || len(value) > MaxCommandRuntimePathBytes ||
		!filepath.IsAbs(value) || len(volume) != 2 || volume[1] != ':' ||
		(!strings.EqualFold(name, "pwsh.exe") && !strings.EqualFold(name, "powershell.exe")) {
		return "", fmt.Errorf("%w: CYBERAGENT_POWERSHELL_PATH must name an absolute local pwsh.exe or powershell.exe", ErrCommandRuntimeBoundary)
	}
	path, err := resolveCommandRuntimeProcess(value)
	if err != nil {
		return "", fmt.Errorf("CYBERAGENT_POWERSHELL_PATH: %w", err)
	}
	if !commandRuntimePathEqual(value, path) {
		return "", fmt.Errorf("%w: CYBERAGENT_POWERSHELL_PATH cannot use a filesystem alias", ErrCommandRuntimeBoundary)
	}
	// NormalizeCommandRuntimeSpec performs the shared native-image, bounded
	// regular-file, Workspace exclusion, and SHA-256 checks on this exact path.
	return path, nil
}

func commandRuntimeGitDistributionRoot(gitPath string) (string, bool) {
	path, err := filepath.Abs(strings.TrimSpace(gitPath))
	if err != nil || !strings.EqualFold(filepath.Base(path), "git.exe") {
		return "", false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	directory := filepath.Dir(path)
	if !strings.EqualFold(filepath.Base(directory), "cmd") &&
		!strings.EqualFold(filepath.Base(directory), "bin") {
		return "", false
	}
	root := filepath.Clean(filepath.Join(directory, ".."))
	if root == filepath.Clean(filepath.VolumeName(root)+string(filepath.Separator)) {
		return "", false
	}
	return root, true
}

func commandRuntimeNativeExecutableAllowed(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if filepath.Ext(base) != ".exe" && filepath.Ext(base) != ".com" {
		return false
	}
	// Native development runtimes use the same pinned process boundary as Go
	// and other compiled programs. Shells and system/privilege brokers retain
	// their dedicated profile or remain unavailable through this path.
	blocked := map[string]struct{}{
		"cmd.exe": {}, "powershell.exe": {}, "pwsh.exe": {}, "bash.exe": {},
		"sh.exe": {}, "wscript.exe": {}, "cscript.exe": {}, "mshta.exe": {},
		"rundll32.exe": {}, "regsvr32.exe": {}, "py.exe": {},
		"busybox.exe": {}, "wsl.exe": {}, "runas.exe": {},
	}
	_, found := blocked[base]
	return !found
}

func commandRuntimeInheritedEnvironmentNames() []string {
	return []string{
		"SystemRoot", "WINDIR", "SystemDrive", "ComSpec", "Path", "PATHEXT",
		"TEMP", "TMP", "ProgramData", "ProgramFiles", "ProgramW6432",
		"CommonProgramFiles", "CommonProgramW6432", "LOCALAPPDATA",
		"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS", "GOROOT",
		"GOPATH", "GOCACHE", "GOMODCACHE", "RUSTUP_HOME",
	}
}

func commandRuntimeFixedEnvironment() []string {
	return []string{
		"CI=1", "NO_COLOR=1", "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=NUL", "GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=NUL",
		"GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=diff.external", "GIT_CONFIG_VALUE_3=",
		"GIT_CONFIG_KEY_4=core.pager", "GIT_CONFIG_VALUE_4=cat",
		"GIT_LFS_SKIP_SMUDGE=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_ALLOW_PROTOCOL=file", "GIT_ASKPASS=false", "SSH_ASKPASS=false",
		"GIT_EDITOR=false", "GIT_SEQUENCE_EDITOR=false", "GIT_EXTERNAL_DIFF=",
		"GOPROXY=off", "GOSUMDB=off", "CARGO_NET_OFFLINE=true",
		"NPM_CONFIG_OFFLINE=true", "PIP_NO_INDEX=1", "UV_OFFLINE=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1", "POWERSHELL_TELEMETRY_OPTOUT=1",
		"HOME=", "USERPROFILE=", "SSH_AUTH_SOCK=",
	}
}

func commandRuntimePlatformEnvironment(executablePath, workspaceRoot string,
	environment []string, network CommandRuntimeNetwork,
	explicit []CommandRuntimeEnvironment,
) []string {
	var server, bypass string
	var available bool
	if network == CommandRuntimeNetworkHost {
		server, bypass, available = commandRuntimeWindowsSystemProxy()
	}
	return commandRuntimePlatformEnvironmentWithProxy(executablePath, workspaceRoot,
		environment, network, explicit, server, bypass, available)
}

func commandRuntimePlatformEnvironmentWithoutProxy(executablePath, workspaceRoot string,
	environment []string, network CommandRuntimeNetwork,
	explicit []CommandRuntimeEnvironment,
) []string {
	return commandRuntimePlatformEnvironmentWithProxy(executablePath, workspaceRoot,
		environment, network, explicit, "", "", false)
}

func commandRuntimePlatformEnvironmentWithProxy(executablePath, workspaceRoot string,
	environment []string, network CommandRuntimeNetwork,
	explicit []CommandRuntimeEnvironment, server, bypass string, available bool,
) []string {
	if network == CommandRuntimeNetworkHost && available {
		environment = commandRuntimeApplyHostProxyEnvironment(environment,
			explicit, server, bypass)
	}
	if strings.EqualFold(filepath.Base(executablePath), "powershell.exe") {
		// Windows PowerShell 5 needs a valid USERPROFILE before script startup.
		// Bind it to the canonical Workspace root, never the operator home.
		environment = replaceCommandRuntimeEnvironment(environment, "USERPROFILE", workspaceRoot)
	}
	return environment
}

func commandRuntimeWindowsSystemProxy() (string, string, bool) {
	server, bypass, available := commandRuntimeWindowsProxySettings()
	if !available {
		return "", "", false
	}
	return commandRuntimeWindowsStaticProxy(server, bypass)
}

func commandRuntimePlatformHostProxy() (hostproxy.Config, bool) {
	server, bypass, available := commandRuntimeWindowsProxySettings()
	if !available {
		return hostproxy.Config{}, false
	}
	server, ok := commandRuntimeWindowsStaticProxyServer(server)
	if !ok {
		return hostproxy.Config{}, false
	}
	config := hostproxy.Config{UpstreamURL: server, Bypass: bypass}
	if _, err := hostproxy.Fingerprint(config); err != nil {
		return hostproxy.Config{}, false
	}
	return config, true
}

func commandRuntimeWindowsProxySettings() (string, string, bool) {
	// WinHTTP returns the active connection's settings, including WPAD and
	// PAC. The top-level Internet Settings registry values can be stale when
	// the current connection is a VPN or dial-up connection.
	if commandRuntimeWinHTTPCurrentUserProxy.Find() != nil ||
		commandRuntimeGlobalFree.Find() != nil {
		return "", "", false
	}
	var config commandRuntimeCurrentUserProxyConfig
	result, _, _ := commandRuntimeWinHTTPCurrentUserProxy.Call(
		uintptr(unsafe.Pointer(&config)))
	if result == 0 {
		return "", "", false
	}
	defer func() {
		for _, value := range []*uint16{config.AutoConfigURL, config.Proxy,
			config.ProxyBypass} {
			if value != nil {
				_, _, _ = commandRuntimeGlobalFree.Call(uintptr(unsafe.Pointer(value)))
			}
		}
	}()
	return commandRuntimeWindowsProxySnapshot(
		windows.UTF16PtrToString(config.Proxy),
		windows.UTF16PtrToString(config.ProxyBypass),
		windows.UTF16PtrToString(config.AutoConfigURL), config.AutoDetect != 0)
}

func commandRuntimeWindowsProxySnapshot(server, bypass, autoConfigURL string,
	autoDetect bool,
) (string, string, bool) {
	if autoDetect || strings.TrimSpace(autoConfigURL) != "" {
		return "", "", false
	}
	if len(server) > 2048 || len(bypass) > 8192 || strings.TrimSpace(server) == "" {
		return "", "", false
	}
	return server, bypass, true
}

func commandRuntimeWindowsStaticProxyWithoutAutoConfig(server, bypass,
	autoConfigURL string, autoDetect bool,
) (string, string, bool) {
	// A PAC or auto-detected route can vary per destination. A static proxy
	// value beside it cannot be applied to every request by environment vars.
	if strings.TrimSpace(autoConfigURL) != "" || autoDetect {
		return "", "", false
	}
	return commandRuntimeWindowsStaticProxy(server, bypass)
}

func commandRuntimeWindowsStaticProxy(server, bypass string) (string, string, bool) {
	server, ok := commandRuntimeWindowsStaticProxyServer(server)
	if !ok {
		return "", "", false
	}
	var domains []string
	for _, item := range strings.Split(bypass, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// <local> and wildcard masks have no portable NO_PROXY equivalent
		// across PowerShell, curl, Go, Git, npm and Python. Never turn on the
		// proxy while silently dropping any bypass rule.
		if strings.EqualFold(item, "<local>") ||
			strings.ContainsAny(item, "*=@?#\\/ ,<>") {
			return "", "", false
		}
		domains = append(domains, item)
	}
	noProxy := strings.Join(domains, ",")
	if !validCommandRuntimeProxyValue("NO_PROXY", noProxy) ||
		redact.String("NO_PROXY="+noProxy) != "NO_PROXY="+noProxy {
		return "", "", false
	}
	return server, noProxy, true
}

func commandRuntimeWindowsStaticProxyServer(server string) (string, bool) {
	server = strings.TrimSpace(server)
	if server == "" || strings.ContainsAny(server, ";=, \t\r\n") {
		// Windows protocol maps and PAC settings have no single portable env value.
		return "", false
	}
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	if !validCommandRuntimeProxyValue("HTTPS_PROXY", server) ||
		redact.String("HTTPS_PROXY="+server) != "HTTPS_PROXY="+server {
		return "", false
	}
	return server, true
}

func commandRuntimeApplyHostProxyEnvironment(environment []string,
	explicit []CommandRuntimeEnvironment, server, bypass string,
) []string {
	for _, entry := range explicit {
		if commandRuntimeProxyEnvironmentName(entry.Name) {
			return environment
		}
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		environment = replaceCommandRuntimeEnvironment(environment, name, server)
	}
	if bypass != "" {
		environment = replaceCommandRuntimeEnvironment(environment, "NO_PROXY", bypass)
	}
	return environment
}

func commandRuntimeExecutableAttributes(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("runtime executable is a reparse point")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	parsed, err := pe.NewFile(file)
	if err != nil {
		return errors.New("runtime executable is not a PE image")
	}
	return parsed.Close()
}

func commandRuntimePathEqual(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
