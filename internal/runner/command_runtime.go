package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	CommandRuntimeProtocolVersion = "command-runtime.v2"
	CommandRuntimePolicyVersion   = "command-runtime-policy.v2"
	CommandRuntimeResultVersion   = "command-runtime-result.v2"
	// Empty identity kind retains the historical native executable SHA-256.
	CommandRuntimeExecutableTemplatePathSHA256 = "template_path_sha256"

	MaxCommandRuntimeArguments        = 128
	MaxCommandRuntimeArgumentBytes    = 16 * 1024
	MaxCommandRuntimeEnvironment      = 32
	MaxCommandRuntimeEnvironmentBytes = 64 * 1024
	MaxCommandRuntimePathBytes        = 4096
	MaxCommandRuntimePurposeRunes     = 1200
	MaxCommandRuntimeScriptBytes      = 64 * 1024
	MaxCommandRuntimeTimeout          = 30 * time.Minute
	MaxCommandRuntimeInlineBytes      = 512 * 1024
	MinCommandRuntimeInlineBytes      = 4 * 1024
	MaxCommandRuntimeArtifactBytes    = 4 * 1024 * 1024
	MaxCommandRuntimeStdinBytes       = 64 * 1024
)

var (
	ErrCommandRuntimeBoundary    = errors.New("command runtime boundary is invalid")
	ErrCommandRuntimeUnavailable = errors.New("command runtime profile is unavailable")
)

type CommandRuntimeProfile string

const (
	CommandRuntimePowerShell CommandRuntimeProfile = "powershell"
	CommandRuntimeBash       CommandRuntimeProfile = "bash"
	CommandRuntimeProcess    CommandRuntimeProfile = "process"
)

func (p CommandRuntimeProfile) Valid() bool {
	return p == CommandRuntimePowerShell || p == CommandRuntimeBash ||
		p == CommandRuntimeProcess
}

type CommandRuntimeStdinPolicy string

const (
	CommandRuntimeStdinClosed CommandRuntimeStdinPolicy = "closed"
	CommandRuntimeStdinPipe   CommandRuntimeStdinPolicy = "pipe"
)

func (p CommandRuntimeStdinPolicy) Valid() bool {
	return p == CommandRuntimeStdinClosed || p == CommandRuntimeStdinPipe
}

type CommandRuntimeNetwork string

const (
	CommandRuntimeNetworkDisabled CommandRuntimeNetwork = "disabled"
	// Host intent is executable only through a host-unsandboxed adapter with
	// Full Access (or Debug) authority. Sandbox adapters remain network-denied.
	CommandRuntimeNetworkHost CommandRuntimeNetwork = "host"
)

func (n CommandRuntimeNetwork) Valid() bool {
	return n == CommandRuntimeNetworkDisabled || n == CommandRuntimeNetworkHost
}

type CommandRuntimeCredentialPolicy string

const CommandRuntimeCredentialsNone CommandRuntimeCredentialPolicy = "none"

type CommandRuntimeEnvironment struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CommandRuntimeOutputPolicy struct {
	InlineBytes   int `json:"inline_bytes"`
	ArtifactBytes int `json:"artifact_bytes"`
}

// WorkspaceMode controls checkpoint lifetime, not process permissions. A shared
// service can keep running while other authorized operations edit the workspace.
// It is not a read-only process and retains the selected adapter's write access.
type CommandRuntimeWorkspaceMode string

const (
	CommandRuntimeWorkspaceExclusive     CommandRuntimeWorkspaceMode = "exclusive"
	CommandRuntimeWorkspaceSharedService CommandRuntimeWorkspaceMode = "shared_service"
)

func (m CommandRuntimeWorkspaceMode) Valid() bool {
	return m == "" || m == CommandRuntimeWorkspaceExclusive || m == CommandRuntimeWorkspaceSharedService
}

type CommandRuntimeSpec struct {
	Version             string                         `json:"version"`
	WorkspaceMode       CommandRuntimeWorkspaceMode    `json:"workspace_mode,omitempty"`
	Profile             CommandRuntimeProfile          `json:"profile"`
	Executable          string                         `json:"executable,omitempty"`
	Arguments           []string                       `json:"arguments,omitempty"`
	Script              string                         `json:"script,omitempty"`
	WorkingDirectory    string                         `json:"working_directory"`
	Environment         []CommandRuntimeEnvironment    `json:"environment"`
	StdinPolicy         CommandRuntimeStdinPolicy      `json:"stdin_policy"`
	InitialStdin        string                         `json:"initial_stdin,omitempty"`
	CloseInitialStdin   bool                           `json:"close_initial_stdin"`
	TimeoutMilliseconds int64                          `json:"timeout_milliseconds"`
	Output              CommandRuntimeOutputPolicy     `json:"output"`
	Network             CommandRuntimeNetwork          `json:"network"`
	Credentials         CommandRuntimeCredentialPolicy `json:"credentials"`
	Purpose             string                         `json:"purpose"`
}

// CommandRuntimeResolvedSpec is the canonical launch contract. The absolute
// cwd and environment values remain process-local; the executable digest and
// canonical argv make the exact launch reviewable and auditable.
type CommandRuntimeResolvedSpec struct {
	Spec                   CommandRuntimeSpec
	ExecutablePath         string
	ExecutableSHA256       string
	ExecutableIdentityKind string
	CanonicalArgv          []string
	AbsoluteDirectory      string
	WorkspaceRoot          string
	Environment            []string
	EnvironmentSHA256      string
	WorkspaceRootSHA256    string
	ExecutablePinned       bool
	ProfileStartupFiles    bool
	EnvironmentInherited   bool
	AttachmentInput        *CommandRuntimeAttachmentInput
}

const commandRuntimeProcessProfileRestriction = "process profile does not accept shells, system script hosts, script files, or blocked launchers; native development runtimes such as Node/Python require an absolute executable and literal arguments; for shell syntax use script with a powershell or bash profile only when supported by the current adapter and omit executable and arguments"

// NormalizeCommandRuntimeIntent validates every path-independent field before a
// Supervisor call can enter the durable replay ledger. Workspace identity and
// executable availability are deliberately resolved only at the launch boundary.
func NormalizeCommandRuntimeIntent(spec CommandRuntimeSpec) (CommandRuntimeSpec, error) {
	spec.Version = strings.TrimSpace(spec.Version)
	spec.Profile = CommandRuntimeProfile(strings.ToLower(strings.TrimSpace(string(spec.Profile))))
	spec.Executable = strings.TrimSpace(spec.Executable)
	spec.WorkingDirectory = filepath.ToSlash(strings.TrimSpace(spec.WorkingDirectory))
	purpose := strings.TrimSpace(spec.Purpose)
	if redact.String(purpose) != purpose {
		return CommandRuntimeSpec{}, fmt.Errorf(
			"%w: purpose contains secret-like material", ErrCommandRuntimeBoundary)
	}
	spec.Purpose = purpose
	if spec.Version != CommandRuntimeProtocolVersion || !spec.Profile.Valid() || !spec.WorkspaceMode.Valid() {
		return CommandRuntimeSpec{}, ErrCommandRuntimeBoundary
	}
	if spec.WorkingDirectory == "" ||
		len([]byte(spec.WorkingDirectory)) > MaxCommandRuntimePathBytes ||
		len([]byte(spec.Executable)) > MaxCommandRuntimePathBytes ||
		redact.String(spec.WorkingDirectory) != spec.WorkingDirectory ||
		redact.String(spec.Executable) != spec.Executable || spec.Environment == nil ||
		!spec.StdinPolicy.Valid() ||
		(spec.StdinPolicy == CommandRuntimeStdinClosed && !spec.CloseInitialStdin) ||
		!spec.Network.Valid() ||
		spec.Credentials != CommandRuntimeCredentialsNone ||
		spec.TimeoutMilliseconds < 1 ||
		spec.TimeoutMilliseconds > MaxCommandRuntimeTimeout.Milliseconds() ||
		spec.Output.InlineBytes < MinCommandRuntimeInlineBytes ||
		spec.Output.InlineBytes > MaxCommandRuntimeInlineBytes ||
		spec.Output.ArtifactBytes < spec.Output.InlineBytes ||
		spec.Output.ArtifactBytes > MaxCommandRuntimeArtifactBytes ||
		spec.Purpose == "" || !validCommandRuntimeText(spec.Purpose, false) ||
		utf8.RuneCountInString(spec.Purpose) > MaxCommandRuntimePurposeRunes {
		return CommandRuntimeSpec{}, ErrCommandRuntimeBoundary
	}
	if !validCommandRuntimeText(spec.InitialStdin, true) ||
		len([]byte(spec.InitialStdin)) > MaxCommandRuntimeStdinBytes ||
		redact.String(spec.InitialStdin) != spec.InitialStdin {
		return CommandRuntimeSpec{}, fmt.Errorf("%w: stdin is invalid or contains secret-like material", ErrCommandRuntimeBoundary)
	}
	if spec.StdinPolicy == CommandRuntimeStdinClosed && spec.InitialStdin != "" {
		return CommandRuntimeSpec{}, fmt.Errorf("%w: closed stdin cannot carry input", ErrCommandRuntimeBoundary)
	}
	cleanDirectory := filepath.Clean(filepath.FromSlash(spec.WorkingDirectory))
	if filepath.IsAbs(cleanDirectory) || cleanDirectory == ".." ||
		strings.HasPrefix(cleanDirectory, ".."+string(filepath.Separator)) ||
		!validCommandRuntimeText(spec.WorkingDirectory, false) {
		return CommandRuntimeSpec{}, fmt.Errorf(
			"%w: working directory must be Workspace-relative", ErrCommandRuntimeBoundary)
	}
	spec.WorkingDirectory = filepath.ToSlash(cleanDirectory)
	arguments, err := normalizeCommandRuntimeArguments(spec.Arguments)
	if err != nil {
		return CommandRuntimeSpec{}, err
	}
	spec.Arguments = arguments
	switch spec.Profile {
	case CommandRuntimePowerShell, CommandRuntimeBash:
		if spec.Executable != "" || spec.Arguments != nil || spec.Script == "" ||
			!validCommandRuntimeText(spec.Script, true) ||
			len([]byte(spec.Script)) > MaxCommandRuntimeScriptBytes ||
			redact.String(spec.Script) != spec.Script {
			return CommandRuntimeSpec{}, fmt.Errorf("%w: shell profile requires one bounded secret-free script", ErrCommandRuntimeBoundary)
		}
	case CommandRuntimeProcess:
		guestPath := path.IsAbs(spec.Executable) && path.Clean(spec.Executable) == spec.Executable &&
			!strings.HasPrefix(spec.Executable, "//") && !strings.Contains(spec.Executable, "\\")
		if spec.Executable == "" || (!filepath.IsAbs(spec.Executable) && !guestPath) ||
			spec.Arguments == nil || spec.Script != "" ||
			!validCommandRuntimeText(spec.Executable, false) {
			return CommandRuntimeSpec{}, fmt.Errorf("%w: process profile requires an absolute native executable and literal argv", ErrCommandRuntimeBoundary)
		}
		allowed := commandRuntimeNativeExecutableAllowed(spec.Executable)
		if guestPath {
			// The wire intent can address a POSIX microVM from Windows. Host
			// launches still resolve and validate an absolute native image in
			// normalizeCommandRuntimeSpec before acquiring process authority.
			allowed = commandRuntimePOSIXProcessAllowed(spec.Executable)
		}
		if !allowed {
			return CommandRuntimeSpec{}, fmt.Errorf("%w: %s", ErrCommandRuntimeBoundary, commandRuntimeProcessProfileRestriction)
		}
	}
	environment, _, _, err := normalizeCommandRuntimeEnvironment(spec.Environment,
		spec.Network)
	if err != nil {
		return CommandRuntimeSpec{}, err
	}
	spec.Environment = environment
	return spec, nil
}

func commandRuntimePOSIXProcessAllowed(executable string) bool {
	switch strings.ToLower(path.Base(executable)) {
	case "sh", "bash", "dash", "zsh", "fish", "ksh", "csh", "tcsh", "pwsh", "powershell", "env", "busybox", "xargs", "sudo", "su", "doas", "pkexec":
		return false
	}
	return true
}

func NormalizeCommandRuntimeSpec(spec CommandRuntimeSpec,
	workspaceRoot string,
) (CommandRuntimeResolvedSpec, error) {
	return normalizeCommandRuntimeSpec(spec, workspaceRoot, true)
}

func normalizeCommandRuntimeSpec(spec CommandRuntimeSpec,
	workspaceRoot string, systemProxyFallback bool,
) (CommandRuntimeResolvedSpec, error) {
	spec, err := NormalizeCommandRuntimeIntent(spec)
	if err != nil {
		return CommandRuntimeResolvedSpec{}, err
	}
	root, directory, relative, err := resolveCommandRuntimeDirectory(workspaceRoot,
		spec.WorkingDirectory)
	if err != nil {
		return CommandRuntimeResolvedSpec{}, err
	}
	spec.WorkingDirectory = relative

	var executablePath string
	var canonicalArgv []string
	switch spec.Profile {
	case CommandRuntimePowerShell, CommandRuntimeBash:
		executablePath, err = resolveCommandRuntimeShell(spec.Profile)
		if err != nil {
			return CommandRuntimeResolvedSpec{}, err
		}
		if spec.Profile == CommandRuntimePowerShell {
			canonicalArgv, err = commandRuntimePowerShellArguments(spec.Script)
			if err != nil {
				return CommandRuntimeResolvedSpec{}, err
			}
		} else {
			canonicalArgv = []string{"--noprofile", "--norc", "-c", spec.Script}
		}
	case CommandRuntimeProcess:
		executablePath, err = resolveCommandRuntimeProcess(spec.Executable)
		if err != nil {
			return CommandRuntimeResolvedSpec{}, err
		}
		if !commandRuntimeNativeExecutableAllowed(executablePath) {
			return CommandRuntimeResolvedSpec{}, fmt.Errorf("%w: %s", ErrCommandRuntimeBoundary, commandRuntimeProcessProfileRestriction)
		}
		canonicalArgv = cloneCommandRuntimeStrings(spec.Arguments)
	}
	outside, outsideErr := commandRuntimeExecutableOutsideWorkspace(executablePath, root)
	if outsideErr != nil || !outside {
		return CommandRuntimeResolvedSpec{}, fmt.Errorf("%w: runtime executable must be outside the workspace", ErrCommandRuntimeBoundary)
	}
	if err := commandRuntimeExecutableAttributes(executablePath); err != nil {
		return CommandRuntimeResolvedSpec{}, fmt.Errorf(
			"%w: executable is not a supported native image", ErrCommandRuntimeBoundary)
	}

	executableSHA, err := commandRuntimeFileSHA256(executablePath)
	if err != nil {
		return CommandRuntimeResolvedSpec{}, err
	}
	environmentSpec, environment, _, err :=
		normalizeCommandRuntimeEnvironment(spec.Environment, spec.Network)
	if err != nil {
		return CommandRuntimeResolvedSpec{}, err
	}
	if systemProxyFallback {
		environment = commandRuntimePlatformEnvironment(executablePath, root, environment,
			spec.Network, spec.Environment)
	} else {
		environment = commandRuntimePlatformEnvironmentWithoutProxy(executablePath,
			root, environment, spec.Network, spec.Environment)
	}
	sort.Slice(environment, func(left int, right int) bool {
		leftKey, _, _ := strings.Cut(environment[left], "=")
		rightKey, _, _ := strings.Cut(environment[right], "=")
		return strings.ToLower(leftKey) < strings.ToLower(rightKey)
	})
	encodedEnvironment, err := json.Marshal(environment)
	if err != nil {
		return CommandRuntimeResolvedSpec{}, ErrCommandRuntimeBoundary
	}
	environmentSHA := sha256.Sum256(encodedEnvironment)
	spec.Environment = environmentSpec
	rootDigest := sha256.Sum256([]byte(root))
	return CommandRuntimeResolvedSpec{
		Spec: spec, ExecutablePath: executablePath,
		ExecutableSHA256: executableSHA, CanonicalArgv: canonicalArgv,
		AbsoluteDirectory: directory, WorkspaceRoot: root, Environment: environment,
		EnvironmentSHA256:   hex.EncodeToString(environmentSHA[:]),
		WorkspaceRootSHA256: hex.EncodeToString(rootDigest[:]),
		ExecutablePinned:    true, ProfileStartupFiles: false,
		EnvironmentInherited: false,
	}, nil
}

func commandRuntimeExecutableOutsideWorkspace(executablePath, workspaceRoot string) (
	bool, error,
) {
	executablePath, workspaceRoot = filepath.Clean(executablePath), filepath.Clean(workspaceRoot)
	executableVolume, workspaceVolume := filepath.VolumeName(executablePath),
		filepath.VolumeName(workspaceRoot)
	if executableVolume != "" && workspaceVolume != "" &&
		!strings.EqualFold(executableVolume, workspaceVolume) {
		// Windows volumes are disjoint namespaces. filepath.Rel reports an
		// error for this safe, common portable-layout case, but the executable
		// is conclusively outside the Workspace rather than unresolvable.
		return true, nil
	}
	within, err := pathWithin(executablePath, workspaceRoot)
	return !within, err
}

func CommandRuntimeSpecFingerprint(spec CommandRuntimeResolvedSpec) string {
	value := struct {
		Version                  string                         `json:"version"`
		WorkspaceMode            CommandRuntimeWorkspaceMode    `json:"workspace_mode,omitempty"`
		Profile                  CommandRuntimeProfile          `json:"profile"`
		ExecutablePath           string                         `json:"executable_path"`
		ExecutableSHA256         string                         `json:"executable_sha256"`
		ExecutableIdentityKind   string                         `json:"executable_identity_kind,omitempty"`
		InvocationPath           string                         `json:"invocation_path,omitempty"`
		Argv                     []string                       `json:"argv"`
		WorkingDirectory         string                         `json:"working_directory"`
		Environment              []CommandRuntimeEnvironment    `json:"environment"`
		EnvironmentSHA256        string                         `json:"environment_sha256"`
		StdinPolicy              CommandRuntimeStdinPolicy      `json:"stdin_policy"`
		InitialStdinSHA256       string                         `json:"initial_stdin_sha256"`
		CloseInitialStdin        bool                           `json:"close_initial_stdin"`
		TimeoutMilliseconds      int64                          `json:"timeout_milliseconds"`
		Output                   CommandRuntimeOutputPolicy     `json:"output"`
		Network                  CommandRuntimeNetwork          `json:"network"`
		Credentials              CommandRuntimeCredentialPolicy `json:"credentials"`
		Purpose                  string                         `json:"purpose"`
		WorkspaceRootSHA256      string                         `json:"workspace_root_sha256"`
		AttachmentManifestSHA256 string                         `json:"attachment_manifest_sha256,omitempty"`
	}{
		Version: spec.Spec.Version, WorkspaceMode: spec.Spec.WorkspaceMode, Profile: spec.Spec.Profile,
		ExecutablePath: spec.ExecutablePath, ExecutableSHA256: spec.ExecutableSHA256,
		ExecutableIdentityKind: spec.ExecutableIdentityKind,
		InvocationPath:         commandRuntimePythonInvocationPath(spec),
		Argv:                   cloneCommandRuntimeStrings(spec.CanonicalArgv),
		WorkingDirectory:       spec.Spec.WorkingDirectory,
		Environment:            cloneCommandRuntimeEnvironment(spec.Spec.Environment),
		EnvironmentSHA256:      spec.EnvironmentSHA256,
		StdinPolicy:            spec.Spec.StdinPolicy,
		InitialStdinSHA256:     commandRuntimeStringSHA256(spec.Spec.InitialStdin),
		CloseInitialStdin:      spec.Spec.CloseInitialStdin,
		TimeoutMilliseconds:    spec.Spec.TimeoutMilliseconds,
		Output:                 spec.Spec.Output, Network: spec.Spec.Network,
		Credentials: spec.Spec.Credentials,
		Purpose:     spec.Spec.Purpose, WorkspaceRootSHA256: spec.WorkspaceRootSHA256,
		AttachmentManifestSHA256: commandRuntimeAttachmentManifest(spec),
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func resolveCommandRuntimeDirectory(workspaceRoot string,
	relative string,
) (string, string, string, error) {
	root, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil || !filepath.IsAbs(root) {
		return "", "", "", ErrCommandRuntimeBoundary
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: workspace root is unavailable", ErrCommandRuntimeBoundary)
	}
	if filepath.IsAbs(relative) || !validCommandRuntimeText(relative, false) {
		return "", "", "", fmt.Errorf("%w: working directory must be workspace-relative", ErrCommandRuntimeBoundary)
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(relative))
	if cleanRelative == "" {
		cleanRelative = "."
	}
	if cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", "", "", fmt.Errorf("%w: working directory escapes the workspace", ErrCommandRuntimeBoundary)
	}
	directory, err := filepath.EvalSymlinks(filepath.Join(root, cleanRelative))
	if err != nil {
		return "", "", "", fmt.Errorf("%w: working directory is unavailable", ErrCommandRuntimeBoundary)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", "", fmt.Errorf("%w: working directory must be an existing non-link directory", ErrCommandRuntimeBoundary)
	}
	within, err := pathWithin(directory, root)
	if err != nil || !within {
		return "", "", "", fmt.Errorf("%w: working directory escapes the workspace", ErrCommandRuntimeBoundary)
	}
	relativePath, err := filepath.Rel(root, directory)
	if err != nil {
		return "", "", "", ErrCommandRuntimeBoundary
	}
	return filepath.Clean(root), filepath.Clean(directory), filepath.ToSlash(relativePath), nil
}

func normalizeCommandRuntimeArguments(values []string) ([]string, error) {
	if len(values) > MaxCommandRuntimeArguments {
		return nil, fmt.Errorf("%w: too many command arguments", ErrCommandRuntimeBoundary)
	}
	if values == nil {
		return nil, nil
	}
	result := make([]string, len(values))
	copy(result, values)
	total := 0
	for _, value := range result {
		if !validCommandRuntimeText(value, false) ||
			len([]byte(value)) > MaxCommandRuntimeArgumentBytes ||
			redact.String(value) != value {
			return nil, fmt.Errorf("%w: argument is invalid or contains secret-like material", ErrCommandRuntimeBoundary)
		}
		total += len([]byte(value))
	}
	if total > MaxCommandRuntimeEnvironmentBytes {
		return nil, fmt.Errorf("%w: command arguments are too large", ErrCommandRuntimeBoundary)
	}
	return result, nil
}

func cloneCommandRuntimeStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func cloneCommandRuntimeEnvironment(values []CommandRuntimeEnvironment) []CommandRuntimeEnvironment {
	if values == nil {
		return nil
	}
	result := make([]CommandRuntimeEnvironment, len(values))
	copy(result, values)
	return result
}

func normalizeCommandRuntimeEnvironment(values []CommandRuntimeEnvironment,
	network CommandRuntimeNetwork,
) (
	[]CommandRuntimeEnvironment, []string, [sha256.Size]byte, error,
) {
	if len(values) > MaxCommandRuntimeEnvironment {
		return nil, nil, [sha256.Size]byte{}, fmt.Errorf("%w: too many environment entries", ErrCommandRuntimeBoundary)
	}
	result := make([]CommandRuntimeEnvironment, len(values))
	copy(result, values)
	sort.Slice(result, func(left int, right int) bool {
		return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
	})
	seen := make(map[string]struct{}, len(result))
	total := 0
	for index := range result {
		entry := &result[index]
		entry.Name = strings.TrimSpace(entry.Name)
		if !validCommandRuntimeEnvironmentName(entry.Name, network) ||
			!validCommandRuntimeText(entry.Value, false) ||
			(commandRuntimeProxyEnvironmentName(entry.Name) &&
				!validCommandRuntimeProxyValue(entry.Name, entry.Value)) ||
			redact.String(entry.Name+"="+entry.Value) != entry.Name+"="+entry.Value {
			return nil, nil, [sha256.Size]byte{}, fmt.Errorf("%w: environment entry is invalid or secret-like", ErrCommandRuntimeBoundary)
		}
		key := strings.ToLower(entry.Name)
		if _, found := seen[key]; found {
			return nil, nil, [sha256.Size]byte{}, fmt.Errorf("%w: duplicate environment entry", ErrCommandRuntimeBoundary)
		}
		seen[key] = struct{}{}
		total += len(entry.Name) + len(entry.Value) + 1
	}
	if total > MaxCommandRuntimeEnvironmentBytes {
		return nil, nil, [sha256.Size]byte{}, fmt.Errorf("%w: environment is too large", ErrCommandRuntimeBoundary)
	}
	merged := commandRuntimeBaseEnvironment(network)
	for _, entry := range result {
		merged = replaceCommandRuntimeEnvironment(merged, entry.Name, entry.Value)
	}
	sort.Slice(merged, func(left int, right int) bool {
		leftKey, _, _ := strings.Cut(merged[left], "=")
		rightKey, _, _ := strings.Cut(merged[right], "=")
		return strings.ToLower(leftKey) < strings.ToLower(rightKey)
	})
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, nil, [sha256.Size]byte{}, ErrCommandRuntimeBoundary
	}
	return result, merged, sha256.Sum256(encoded), nil
}

func validCommandRuntimeEnvironmentName(value string,
	network CommandRuntimeNetwork,
) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "=\x00") {
		return false
	}
	for index, current := range []byte(value) {
		if current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' ||
			index > 0 && current >= '0' && current <= '9' || current == '_' {
			continue
		}
		return false
	}
	lower := strings.ToLower(value)
	if commandRuntimeProxyEnvironmentName(value) {
		return network == CommandRuntimeNetworkHost
	}
	if strings.HasPrefix(lower, "git_") || strings.HasPrefix(lower, "ssh_") {
		return false
	}
	for _, fixed := range commandRuntimeFixedEnvironment() {
		name, _, _ := strings.Cut(fixed, "=")
		if strings.EqualFold(name, value) {
			return false
		}
	}
	for _, fragment := range []string{
		"api_key", "apikey", "auth", "cookie", "credential", "password",
		"passwd", "private_key", "proxy", "secret", "token", "askpass",
	} {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	switch lower {
	case "path", "pathext", "comspec", "systemroot", "windir", "home",
		"userprofile", "bash_env", "env", "shellopts", "prompt_command",
		"ld_preload", "ld_library_path", "dyld_insert_libraries",
		"node_options", "pythonstartup", "git_config_global",
		"git_config_system", "git_config_count", "git_ssh", "git_ssh_command",
		"ssh_command", "cargo_home", "uv_config_file", "poetry_config_dir",
		"cyberagent_powershell_path", "traverse_attachments_dir":
		return false
	case "aws_access_key_id", "aws_profile", "aws_default_profile", "azure_config_dir",
		"cloudsdk_config", "docker_config", "gh_config_dir", "kubeconfig",
		"netrc", "npm_config_userconfig", "pip_config_file":
		return false
	}
	return !strings.HasPrefix(lower, "dyld_")
}

func commandRuntimeProxyEnvironmentName(name string) bool {
	switch strings.ToUpper(name) {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	default:
		return false
	}
}

func validCommandRuntimeProxyValue(name, value string) bool {
	if strings.EqualFold(name, "NO_PROXY") {
		if strings.Contains(value, "://") || strings.ContainsAny(value, "@=?#;\\") {
			return false
		}
		for _, part := range strings.Split(value, ",") {
			if strings.TrimSpace(part) != part || strings.ContainsAny(part, " \t\r\n") {
				return false
			}
		}
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" ||
		parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return false
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return false
		}
	}
	return !strings.ContainsAny(value, "@?#\\")
}

func validCommandRuntimeText(value string, allowLineWhitespace bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current == 0 || unicode.In(current, unicode.Cf) {
			return false
		}
		if unicode.IsControl(current) &&
			!(allowLineWhitespace && (current == '\n' || current == '\r' || current == '\t')) {
			return false
		}
	}
	return true
}

func replaceCommandRuntimeEnvironment(values []string, name string, value string) []string {
	key := strings.ToLower(name)
	for index, entry := range values {
		current, _, _ := strings.Cut(entry, "=")
		if strings.ToLower(current) == key {
			values[index] = name + "=" + value
			return values
		}
	}
	return append(values, name+"="+value)
}

func resolveCommandRuntimeProcess(value string) (string, error) {
	if !validCommandRuntimeText(value, false) {
		return "", ErrCommandRuntimeBoundary
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%w: process executable must be an absolute path", ErrCommandRuntimeBoundary)
	}
	path := value
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return "", ErrCommandRuntimeBoundary
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: executable is unavailable", ErrCommandRuntimeUnavailable)
	}
	return filepath.Clean(path), nil
}

func commandRuntimeFileSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > 1<<30 {
		return "", fmt.Errorf("%w: executable must be a bounded regular non-link file", ErrCommandRuntimeBoundary)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: executable cannot be opened", ErrCommandRuntimeUnavailable)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("%w: executable cannot be hashed", ErrCommandRuntimeUnavailable)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func commandRuntimeStringSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func CommandRuntimeWorkspaceRootSHA256(workspaceRoot string) (string, error) {
	root, _, _, err := resolveCommandRuntimeDirectory(workspaceRoot, ".")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(root))
	return hex.EncodeToString(digest[:]), nil
}

func validateCommandRuntimeLaunchDirectory(spec CommandRuntimeResolvedSpec) error {
	if err := validateCommandRuntimeAttachmentInput(spec); err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(spec.WorkspaceRoot)
	if err != nil {
		return ErrCommandRuntimeBoundary
	}
	root, err = filepath.Abs(root)
	if err != nil || !commandRuntimePathEqual(root, spec.WorkspaceRoot) {
		return ErrCommandRuntimeBoundary
	}
	directory, err := filepath.EvalSymlinks(spec.AbsoluteDirectory)
	if err != nil {
		return ErrCommandRuntimeBoundary
	}
	directory, err = filepath.Abs(directory)
	if err != nil || !commandRuntimePathEqual(directory, spec.AbsoluteDirectory) {
		return ErrCommandRuntimeBoundary
	}
	for _, path := range []string{root, directory} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrCommandRuntimeBoundary
		}
	}
	within, err := pathWithin(directory, root)
	if err != nil || !within {
		return ErrCommandRuntimeBoundary
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || filepath.ToSlash(relative) != spec.Spec.WorkingDirectory {
		return ErrCommandRuntimeBoundary
	}
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	if hex.EncodeToString(digest[:]) != spec.WorkspaceRootSHA256 {
		return ErrCommandRuntimeBoundary
	}
	return nil
}

func commandRuntimeBaseEnvironment(network CommandRuntimeNetwork) []string {
	allowed := commandRuntimeInheritedEnvironmentNames()
	values := make([]string, 0, len(allowed)+16)
	for _, name := range allowed {
		if value, found := os.LookupEnv(name); found && value != "" &&
			validCommandRuntimeText(value, false) &&
			redact.String(name+"="+value) == name+"="+value {
			values = append(values, name+"="+value)
		}
	}
	fixed := commandRuntimeFixedEnvironment()
	for _, entry := range fixed {
		name, value, _ := strings.Cut(entry, "=")
		if network == CommandRuntimeNetworkHost && commandRuntimeOfflineEnvironmentName(name) {
			continue
		}
		values = replaceCommandRuntimeEnvironment(values, name, value)
	}
	return values
}

func commandRuntimeOfflineEnvironmentName(name string) bool {
	switch strings.ToUpper(name) {
	case "GIT_ALLOW_PROTOCOL", "GOPROXY", "GOSUMDB", "CARGO_NET_OFFLINE",
		"NPM_CONFIG_OFFLINE", "PIP_NO_INDEX", "UV_OFFLINE":
		return true
	default:
		return false
	}
}

func commandRuntimeIntentJSON(spec CommandRuntimeResolvedSpec) string {
	value := struct {
		Version                  string                         `json:"version"`
		WorkspaceMode            CommandRuntimeWorkspaceMode    `json:"workspace_mode,omitempty"`
		PolicyVersion            string                         `json:"policy_version"`
		Profile                  CommandRuntimeProfile          `json:"profile"`
		ExecutablePath           string                         `json:"executable_path"`
		ExecutableSHA256         string                         `json:"executable_sha256"`
		ExecutableIdentityKind   string                         `json:"executable_identity_kind,omitempty"`
		InvocationPath           string                         `json:"invocation_path,omitempty"`
		Argv                     []string                       `json:"argv"`
		WorkingDirectory         string                         `json:"working_directory"`
		Environment              []CommandRuntimeEnvironment    `json:"environment"`
		EnvironmentSHA256        string                         `json:"environment_sha256"`
		StdinPolicy              CommandRuntimeStdinPolicy      `json:"stdin_policy"`
		InitialStdinBytes        int                            `json:"initial_stdin_bytes"`
		InitialStdinSHA256       string                         `json:"initial_stdin_sha256"`
		CloseInitialStdin        bool                           `json:"close_initial_stdin"`
		TimeoutMilliseconds      int64                          `json:"timeout_milliseconds"`
		Output                   CommandRuntimeOutputPolicy     `json:"output"`
		Network                  CommandRuntimeNetwork          `json:"network"`
		Credentials              CommandRuntimeCredentialPolicy `json:"credentials"`
		Purpose                  string                         `json:"purpose"`
		AttachmentManifestSHA256 string                         `json:"attachment_manifest_sha256,omitempty"`
	}{
		Version: spec.Spec.Version, WorkspaceMode: spec.Spec.WorkspaceMode, PolicyVersion: CommandRuntimePolicyVersion,
		Profile: spec.Spec.Profile, ExecutablePath: spec.ExecutablePath,
		ExecutableSHA256:       spec.ExecutableSHA256,
		ExecutableIdentityKind: spec.ExecutableIdentityKind,
		InvocationPath:         commandRuntimePythonInvocationPath(spec),
		Argv:                   cloneCommandRuntimeStrings(spec.CanonicalArgv),
		WorkingDirectory:       spec.Spec.WorkingDirectory,
		Environment:            cloneCommandRuntimeEnvironment(spec.Spec.Environment),
		EnvironmentSHA256:      spec.EnvironmentSHA256,
		StdinPolicy:            spec.Spec.StdinPolicy,
		InitialStdinBytes:      len([]byte(spec.Spec.InitialStdin)),
		InitialStdinSHA256:     commandRuntimeStringSHA256(spec.Spec.InitialStdin),
		CloseInitialStdin:      spec.Spec.CloseInitialStdin,
		TimeoutMilliseconds:    spec.Spec.TimeoutMilliseconds,
		Output:                 spec.Spec.Output, Network: spec.Spec.Network,
		Credentials: spec.Spec.Credentials, Purpose: spec.Spec.Purpose,
		AttachmentManifestSHA256: commandRuntimeAttachmentManifest(spec),
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
