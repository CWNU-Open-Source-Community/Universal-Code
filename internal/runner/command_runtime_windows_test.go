//go:build windows

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf16"

	"cyberagent-workbench/internal/hostproxy"
	"cyberagent-workbench/internal/rootfs"

	"golang.org/x/sys/windows"
)

func TestCommandRuntimeDirectoryRejectsWindowsPathAliasing(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "project")
	exact, err := rootfs.ExactAbsolutePath(plain + " ")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{plain, exact} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Remove(exact) })
	if _, _, _, err := resolveCommandRuntimeDirectory(plain, "."); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveCommandRuntimeDirectory(plain+" ", "."); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("unsafe working directory should be rejected before dispatch: %v", err)
	}
	if _, _, _, err := resolveCommandRuntimeDirectory(root, "project "); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("unsafe relative working directory should be rejected before dispatch: %v", err)
	}
	spec := commandRuntimeTestPowerShellSpec()
	spec.WorkingDirectory = "project "
	if _, err := NormalizeCommandRuntimeSpec(spec, root); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("intent silently selected the neighboring directory: %v", err)
	}
}

func TestCommandRuntimeWindowsExplicitPowerShellPinsHostSelection(t *testing.T) {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		t.Run(name, func(t *testing.T) {
			executable := commandRuntimeTestPowerShellImage(t, t.TempDir(), name)
			t.Setenv("CYBERAGENT_POWERSHELL_PATH", executable)
			resolved, err := NormalizeCommandRuntimeSpec(commandRuntimeTestPowerShellSpec(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			wantSHA, err := commandRuntimeFileSHA256(executable)
			if err != nil {
				t.Fatal(err)
			}
			if !commandRuntimePathEqual(resolved.ExecutablePath, executable) ||
				resolved.ExecutableSHA256 != wantSHA || !resolved.ExecutablePinned ||
				len(resolved.CanonicalArgv) != 8 ||
				strings.Join(resolved.CanonicalArgv[:6], "|") != "-NoLogo|-NoProfile|-NonInteractive|-OutputFormat|Text|-Command" ||
				resolved.CanonicalArgv[6] != hostPowerShellUTF8Bootstrap ||
				resolved.CanonicalArgv[7] != "Write-Output runtime-selection" {
				t.Fatalf("explicit host runtime was not pinned: path=%q sha=%q argv=%q",
					resolved.ExecutablePath, resolved.ExecutableSHA256, resolved.CanonicalArgv)
			}
			for _, entry := range resolved.Environment {
				if strings.HasPrefix(strings.ToUpper(entry), "CYBERAGENT_POWERSHELL_PATH=") {
					t.Fatal("host runtime selection leaked into the child environment")
				}
			}
			if name == "powershell.exe" {
				var profile string
				for _, entry := range resolved.Environment {
					key, value, _ := strings.Cut(entry, "=")
					if strings.EqualFold(key, "USERPROFILE") {
						profile = value
					}
				}
				if !commandRuntimePathEqual(profile, resolved.WorkspaceRoot) {
					t.Fatalf("Windows PowerShell 5 USERPROFILE=%q want Workspace %q",
						profile, resolved.WorkspaceRoot)
				}
				_, _, unboundSHA, err := normalizeCommandRuntimeEnvironment(
					resolved.Spec.Environment, resolved.Spec.Network)
				if err != nil || resolved.EnvironmentSHA256 == hex.EncodeToString(unboundSHA[:]) {
					t.Fatalf("profile binding was not included in environment SHA: %v", err)
				}
			}
			spec := commandRuntimeTestPowerShellSpec()
			spec.Environment = []CommandRuntimeEnvironment{{Name: "CYBERAGENT_POWERSHELL_PATH", Value: executable}}
			if _, err := NormalizeCommandRuntimeSpec(spec, t.TempDir()); !errors.Is(err, ErrCommandRuntimeBoundary) {
				t.Fatalf("per-command host runtime configuration was accepted: %v", err)
			}
		})
	}
}

func TestCommandRuntimeWindowsStaticProxyFallbackIsHostOnly(t *testing.T) {
	for _, tc := range []struct {
		pac        string
		autoDetect bool
	}{
		{pac: "http://127.0.0.1/proxy.pac"},
		{autoDetect: true},
	} {
		if server, bypass, ok := commandRuntimeWindowsProxySnapshot(
			"127.0.0.1:7890", "localhost", tc.pac, tc.autoDetect); ok ||
			server != "" || bypass != "" {
			t.Fatalf("active automatic route was treated as static: pac=%t auto=%t",
				tc.pac != "", tc.autoDetect)
		}
	}
	for _, tc := range []struct {
		pac        string
		autoDetect bool
	}{
		{pac: "http://127.0.0.1/proxy.pac"},
		{autoDetect: true},
		{pac: "  http://127.0.0.1/proxy.pac  ", autoDetect: true},
	} {
		if proxy, bypass, ok := commandRuntimeWindowsStaticProxyWithoutAutoConfig(
			"127.0.0.1:7890", "localhost", tc.pac, tc.autoDetect); ok ||
			proxy != "" || bypass != "" {
			t.Fatalf("static fallback ignored automatic route pac=%t autodetect=%t",
				tc.pac != "", tc.autoDetect)
		}
	}
	proxy, bypass, ok := commandRuntimeWindowsStaticProxy(
		"127.0.0.1:7890", "localhost;127.0.0.1")
	if !ok || proxy != "http://127.0.0.1:7890" ||
		bypass != "localhost,127.0.0.1" {
		t.Fatalf("static proxy=%q bypass=%q available=%t", proxy, bypass, ok)
	}
	for _, server := range []string{
		"http=127.0.0.1:7890;https=127.0.0.1:7891",
		"http://user:pass@127.0.0.1:7890", "http://127.0.0.1:7890/path",
	} {
		if _, _, ok := commandRuntimeWindowsStaticProxy(server, ""); ok {
			t.Fatalf("ambiguous or credentialed system proxy %q accepted", server)
		}
	}
	for _, bypass := range []string{
		"<local>", "*.corp", "10.*", "localhost;<local>",
		"localhost;*.corp", "localhost;10.*", "localhost;*.corp;10.*;<local>",
	} {
		if gotProxy, gotBypass, ok := commandRuntimeWindowsStaticProxy(
			"127.0.0.1:7890", bypass); ok || gotProxy != "" || gotBypass != "" {
			t.Fatalf("unrepresentable bypass %q enabled proxy=%q no_proxy=%q available=%t",
				bypass, gotProxy, gotBypass, ok)
		}
	}
	base := []string{"HOME=", "USERPROFILE=", "GIT_TERMINAL_PROMPT=0"}
	host := commandRuntimeApplyHostProxyEnvironment(append([]string{}, base...),
		nil, proxy, bypass)
	joined := strings.Join(host, "\n")
	for _, want := range []string{"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy,
		"ALL_PROXY=" + proxy, "NO_PROXY=" + bypass} {
		if !strings.Contains(joined, want) {
			t.Fatalf("host fallback missing %q: %q", want, joined)
		}
	}
	explicit := []CommandRuntimeEnvironment{{Name: "HTTPS_PROXY",
		Value: "http://localhost:9000"}}
	if got := commandRuntimeApplyHostProxyEnvironment(append([]string{}, base...),
		explicit, proxy, bypass); len(got) != len(base) {
		t.Fatalf("system fallback overrode explicit proxy: %q", got)
	}
	if got := commandRuntimeApplyHostProxyEnvironment(append([]string{}, base...),
		[]CommandRuntimeEnvironment{{Name: "NO_PROXY", Value: "localhost"}},
		proxy, bypass); len(got) != len(base) {
		t.Fatalf("system fallback ignored explicit bypass: %q", got)
	}
	disabled := commandRuntimePlatformEnvironment("not-powershell.exe", t.TempDir(),
		append([]string{}, base...), CommandRuntimeNetworkDisabled, nil)
	if len(disabled) != len(base) {
		t.Fatalf("disabled intent received system proxy: %q", disabled)
	}
}

func TestCommandRuntimeManagerBindsComplexSystemProxyOnlyForHost(t *testing.T) {
	executable := commandRuntimeTestPowerShellImage(t, t.TempDir(), "powershell.exe")
	t.Setenv("CYBERAGENT_POWERSHELL_PATH", executable)
	workspace := t.TempDir()
	config := hostproxy.Config{UpstreamURL: "http://127.0.0.1:7890",
		Bypass: "localhost;<local>;10.*;*.corp"}
	manager := &CommandRuntimeManager{hostProxy: &commandRuntimeHostProxySet{
		readConfig: func() (hostproxy.Config, bool) { return config, true },
	}}
	t.Cleanup(func() { _ = manager.hostProxy.close() })
	spec := commandRuntimeTestPowerShellSpec()
	spec.Network = CommandRuntimeNetworkHost
	first, err := manager.NormalizeCommandRuntimeSpec(spec, workspace)
	if err != nil {
		t.Fatal(err)
	}
	get := func(environment []string, name string) string {
		for _, entry := range environment {
			key, value, _ := strings.Cut(entry, "=")
			if strings.EqualFold(key, name) {
				return value
			}
		}
		return ""
	}
	proxy := get(first.Environment, "HTTPS_PROXY")
	if !strings.HasPrefix(proxy, "http://127.0.0.1:") ||
		get(first.Environment, "HTTP_PROXY") != proxy ||
		get(first.Environment, "ALL_PROXY") != proxy ||
		get(first.Environment, "NO_PROXY") != "" ||
		get(first.Environment, "NODE_USE_ENV_PROXY") != "1" {
		t.Fatalf("host bridge environment was not bound: %q", first.Environment)
	}
	wantRoute, err := hostproxy.Fingerprint(config)
	if err != nil || get(first.Environment, "CYBERAGENT_HOST_PROXY_ROUTE_SHA256") != wantRoute {
		t.Fatal("host route fingerprint was not bound")
	}
	second, err := manager.NormalizeCommandRuntimeSpec(spec, workspace)
	if err != nil || first.EnvironmentSHA256 != second.EnvironmentSHA256 ||
		CommandRuntimeSpecFingerprint(first) != CommandRuntimeSpecFingerprint(second) {
		t.Fatalf("same process route changed identity: %v", err)
	}
	config.UpstreamURL = "http://127.0.0.1:7891"
	changed, err := manager.NormalizeCommandRuntimeSpec(spec, workspace)
	if err != nil || CommandRuntimeSpecFingerprint(first) == CommandRuntimeSpecFingerprint(changed) {
		t.Fatalf("changed upstream reused command identity: %v", err)
	}
	spec.Environment = []CommandRuntimeEnvironment{{Name: "HTTPS_PROXY",
		Value: "http://127.0.0.1:9000"}}
	explicit, err := manager.NormalizeCommandRuntimeSpec(spec, workspace)
	if err != nil || get(explicit.Environment, "HTTPS_PROXY") != "http://127.0.0.1:9000" ||
		get(explicit.Environment, "CYBERAGENT_HOST_PROXY_ROUTE_SHA256") != "" {
		t.Fatalf("explicit proxy did not win: %v", err)
	}
	spec.Environment = []CommandRuntimeEnvironment{}
	spec.Network = CommandRuntimeNetworkDisabled
	disabled, err := manager.NormalizeCommandRuntimeSpec(spec, workspace)
	if err != nil || get(disabled.Environment, "HTTPS_PROXY") != "" ||
		get(disabled.Environment, "CYBERAGENT_HOST_PROXY_ROUTE_SHA256") != "" {
		t.Fatalf("disabled network received bridge: %v", err)
	}
}

func TestCommandRuntimeWindowsProxyFallbackChangesFinalFingerprintOnlyWhenSafe(t *testing.T) {
	base := []string{"HOME=", "USERPROFILE=", "GIT_TERMINAL_PROMPT=0"}
	proxy, bypass, ok := commandRuntimeWindowsStaticProxy(
		"127.0.0.1:7890", "localhost;127.0.0.1")
	if !ok {
		t.Fatal("simple static proxy was rejected")
	}
	plain := commandRuntimePlatformEnvironmentWithProxy("native.exe", `D:\workspace`,
		append([]string{}, base...), CommandRuntimeNetworkHost, nil, "", "", false)
	proxied := commandRuntimePlatformEnvironmentWithProxy("native.exe", `D:\workspace`,
		append([]string{}, base...), CommandRuntimeNetworkHost, nil, proxy, bypass, true)
	unsafeProxy, unsafeBypass, unsafeAvailable := commandRuntimeWindowsStaticProxy(
		"127.0.0.1:7890", "localhost;*.corp;10.*;<local>")
	unsafe := commandRuntimePlatformEnvironmentWithProxy("native.exe", `D:\workspace`,
		append([]string{}, base...), CommandRuntimeNetworkHost, nil,
		unsafeProxy, unsafeBypass, unsafeAvailable)
	if strings.Join(unsafe, "\n") != strings.Join(plain, "\n") {
		t.Fatalf("unsafe bypass leaked fallback proxy: %q", unsafe)
	}
	fingerprint := func(environment []string) string {
		finalEnvironment := append([]string{}, environment...)
		sort.Slice(finalEnvironment, func(left, right int) bool {
			leftKey, _, _ := strings.Cut(finalEnvironment[left], "=")
			rightKey, _, _ := strings.Cut(finalEnvironment[right], "=")
			return strings.ToLower(leftKey) < strings.ToLower(rightKey)
		})
		encoded, err := json.Marshal(finalEnvironment)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		return CommandRuntimeSpecFingerprint(CommandRuntimeResolvedSpec{
			Spec: CommandRuntimeSpec{Version: CommandRuntimeProtocolVersion,
				Network: CommandRuntimeNetworkHost},
			EnvironmentSHA256: hex.EncodeToString(digest[:]),
		})
	}
	if fingerprint(plain) == fingerprint(proxied) ||
		fingerprint(plain) != fingerprint(unsafe) {
		t.Fatal("safe proxy or unsafe bypass did not bind the expected fingerprint")
	}
}

func TestCommandRuntimeWindowsExplicitPowerShellInvalidDoesNotFallback(t *testing.T) {
	root := t.TempDir()
	textImage := filepath.Join(t.TempDir(), "pwsh.exe")
	if err := os.WriteFile(textImage, []byte("not a native image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path string
		want       error
	}{
		{"relative", `pwsh.exe`, ErrCommandRuntimeBoundary},
		{"blank", "  ", ErrCommandRuntimeBoundary},
		{"quoted", `"C:\Tools\PowerShell\pwsh.exe"`, ErrCommandRuntimeBoundary},
		{"wrong name", filepath.Join(t.TempDir(), "cmd.exe"), ErrCommandRuntimeBoundary},
		{"network", `\\server\share\pwsh.exe`, ErrCommandRuntimeBoundary},
		{"device", `\\?\C:\Tools\pwsh.exe`, ErrCommandRuntimeBoundary},
		{"missing", filepath.Join(t.TempDir(), "pwsh.exe"), ErrCommandRuntimeUnavailable},
		{"not native", textImage, ErrCommandRuntimeBoundary},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CYBERAGENT_POWERSHELL_PATH", test.path)
			if _, err := NormalizeCommandRuntimeSpec(commandRuntimeTestPowerShellSpec(), root); !errors.Is(err, test.want) {
				t.Fatalf("invalid explicit runtime fell back or returned wrong error: %v", err)
			}
		})
	}
}

func TestCommandRuntimeWindowsExplicitPowerShellRejectsWorkspaceAndAlias(t *testing.T) {
	root := t.TempDir()
	executable := commandRuntimeTestPowerShellImage(t, root, "pwsh.exe")
	t.Setenv("CYBERAGENT_POWERSHELL_PATH", executable)
	if _, err := NormalizeCommandRuntimeSpec(commandRuntimeTestPowerShellSpec(), root); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("project executable selected as runtime: %v", err)
	}
	t.Run("ancestor alias", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "runtime-link")
		if err := os.Symlink(root, link); err != nil {
			windowsRoot, rootErr := controlledWindowsDirectory()
			if rootErr != nil {
				t.Fatal(rootErr)
			}
			command := exec.Command(filepath.Join(windowsRoot, "System32", "cmd.exe"),
				"/d", "/c", "mklink", "/J", link, root)
			if output, junctionErr := command.CombinedOutput(); junctionErr != nil {
				t.Skipf("host cannot create an ancestor alias: symlink=%v junction=%v output=%s", err, junctionErr, output)
			}
		}
		if _, err := os.Stat(filepath.Join(link, "pwsh.exe")); err != nil {
			t.Fatalf("ancestor alias does not reach the test executable: %v", err)
		}
		t.Setenv("CYBERAGENT_POWERSHELL_PATH", filepath.Join(link, "pwsh.exe"))
		if _, err := NormalizeCommandRuntimeSpec(commandRuntimeTestPowerShellSpec(), root); !errors.Is(err, ErrCommandRuntimeBoundary) && !errors.Is(err, ErrCommandRuntimeUnavailable) {
			t.Fatalf("runtime alias bypassed the project boundary: %v", err)
		}
	})
}

func TestCommandRuntimeWindowsPowerShellDefaultDoesNotUsePath(t *testing.T) {
	t.Setenv("CYBERAGENT_POWERSHELL_PATH", "")
	before, beforeErr := resolveCommandRuntimeShell(CommandRuntimePowerShell)
	pathRoot := t.TempDir()
	commandRuntimeTestPowerShellImage(t, pathRoot, "pwsh.exe")
	t.Setenv("PATH", pathRoot)
	after, afterErr := resolveCommandRuntimeShell(CommandRuntimePowerShell)
	if !commandRuntimePathEqual(before, after) || !errors.Is(afterErr, beforeErr) {
		t.Fatalf("PATH changed default PowerShell: before=%q %v after=%q %v", before, beforeErr, after, afterErr)
	}
}

func commandRuntimeTestPowerShellSpec() CommandRuntimeSpec {
	return CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimePowerShell,
		Script: "Write-Output runtime-selection", WorkingDirectory: ".",
		Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "validate trusted PowerShell selection without starting a process",
	}
}

func commandRuntimeTestPowerShellImage(t *testing.T, root, name string) string {
	t.Helper()
	// This PE fixture exercises resolution and pinning only; it is never launched
	// as PowerShell and is not evidence of shell or LPAC compatibility.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, value, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommandRuntimeProcessExecutableOnAnotherVolumeIsOutsideWorkspace(t *testing.T) {
	outside, err := commandRuntimeExecutableOutsideWorkspace(
		`C:\Program Files\Go\bin\go.exe`, `D:\standard-code-attack-181\runtime`)
	if err != nil || !outside {
		t.Fatalf("cross-volume executable outside=%t err=%v", outside, err)
	}
	outside, err = commandRuntimeExecutableOutsideWorkspace(
		`D:\standard-code-attack-181\runtime\tool.exe`,
		`D:\standard-code-attack-181\runtime`)
	if err != nil || outside {
		t.Fatalf("workspace executable outside=%t err=%v", outside, err)
	}
}

func TestCommandRuntimeWindowsPowerShell5PowerShell7AndGitBashSmoke(t *testing.T) {
	root := t.TempDir()
	var powershell5 string
	if windowsRoot, err := controlledWindowsDirectory(); err == nil {
		powershell5 = filepath.Join(windowsRoot, "System32", "WindowsPowerShell", "v1.0",
			"powershell.exe")
	}
	var powershell7 string
	for _, programFiles := range controlledKnownFolders(windows.FOLDERID_ProgramFiles,
		windows.FOLDERID_ProgramFilesX64, windows.FOLDERID_ProgramFilesX86) {
		candidate := filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe")
		if commandRuntimeRegularFile(candidate) {
			powershell7 = candidate
			break
		}
	}
	gitBash, _ := resolveCommandRuntimeShell(CommandRuntimeBash)
	for _, test := range []struct {
		name       string
		profile    CommandRuntimeProfile
		executable string
		script     string
	}{
		{name: "Windows PowerShell 5", profile: CommandRuntimePowerShell,
			executable: powershell5, script: "[Console]::Out.WriteLine('powershell-5-smoke')"},
		{name: "PowerShell 7", profile: CommandRuntimePowerShell,
			executable: powershell7, script: "[Console]::Out.WriteLine('powershell-7-smoke')"},
		{name: "Git Bash", profile: CommandRuntimeBash,
			executable: gitBash, script: "printf 'git-bash-smoke\\n'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !commandRuntimeRegularFile(test.executable) {
				t.Skipf("%s is unavailable", test.name)
			}
			if test.profile == CommandRuntimePowerShell {
				// Normalize the selected shell's actual launch environment instead
				// of replacing a different shell's executable after fingerprinting.
				t.Setenv("CYBERAGENT_POWERSHELL_PATH", test.executable)
			}
			resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
				Version: CommandRuntimeProtocolVersion, Profile: test.profile,
				Script: test.script, WorkingDirectory: ".",
				Environment: []CommandRuntimeEnvironment{},
				StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
				TimeoutMilliseconds: 5000,
				Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
					ArtifactBytes: MinCommandRuntimeInlineBytes},
				Network:     CommandRuntimeNetworkDisabled,
				Credentials: CommandRuntimeCredentialsNone, Purpose: test.name + " smoke",
			}, root)
			if err != nil {
				t.Fatal(err)
			}
			process, err := newPlatformCommandRuntimeStarter().Start(
				context.Background(), CommandRuntimeScope{}, resolved)
			if err != nil {
				t.Fatal(err)
			}
			stdoutDone := make(chan []byte, 1)
			stderrDone := make(chan []byte, 1)
			go func() { value, _ := io.ReadAll(process.Stdout()); stdoutDone <- value }()
			go func() { value, _ := io.ReadAll(process.Stderr()); stderrDone <- value }()
			exitCode, waitErr := process.Wait()
			stdout, stderr := <-stdoutDone, <-stderrDone
			_ = process.Close()
			decodedStdout := commandRuntimeWindowsTestOutput(stdout)
			decodedStderr := commandRuntimeWindowsTestOutput(stderr)
			if waitErr != nil || exitCode != 0 || !strings.Contains(decodedStdout, "smoke") {
				t.Fatalf("stdout=%q stderr=%q exit=%d err=%v",
					decodedStdout, decodedStderr, exitCode, waitErr)
			}
		})
	}
}

func commandRuntimeWindowsTestOutput(value []byte) string {
	if len(value) < 2 || len(value)%2 != 0 {
		return string(value)
	}
	zeroHighBytes := 0
	for index := 1; index < len(value); index += 2 {
		if value[index] == 0 {
			zeroHighBytes++
		}
	}
	if zeroHighBytes*4 < (len(value)/2)*3 {
		return string(value)
	}
	units := make([]uint16, len(value)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(value[index*2:])
	}
	return string(utf16.Decode(units))
}

func TestCommandRuntimeWindowsTestOutput(t *testing.T) {
	utf16LE := []byte{'s', 0, 'm', 0, 'o', 0, 'k', 0, 'e', 0}
	if got := commandRuntimeWindowsTestOutput(utf16LE); got != "smoke" {
		t.Fatalf("UTF-16LE output = %q", got)
	}
	if got := commandRuntimeWindowsTestOutput([]byte("smoke")); got != "smoke" {
		t.Fatalf("UTF-8 output = %q", got)
	}
}

func commandRuntimeRegularFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
