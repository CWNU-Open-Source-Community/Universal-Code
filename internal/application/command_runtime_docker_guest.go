package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

// Docker resolves fixed image executables without consulting the host PATH.
// The digest binds the installed image generation and its guest executable;
// historical v1 Jobs retain their native-binary digest discriminator.
func (e *DockerSandboxCommandRuntimeExecutor) NormalizeCommandRuntimeSpec(spec runner.CommandRuntimeSpec, root string) (runner.CommandRuntimeResolvedSpec, error) {
	if !e.Available() || spec.Profile != runner.CommandRuntimeProcess || spec.Network != runner.CommandRuntimeNetworkDisabled {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	intent, err := runner.NormalizeCommandRuntimeIntent(spec)
	if err != nil {
		return runner.CommandRuntimeResolvedSpec{}, err
	}
	guest := ""
	switch strings.ToLower(filepath.Base(intent.Executable)) {
	case "go", "go.exe":
		guest = "/opt/toolchains/go/bin/go"
	case "gofmt", "gofmt.exe":
		guest = "/opt/toolchains/go/bin/gofmt"
	case "node", "node.exe":
		guest = "/opt/toolchains/node/bin/node"
	case "python", "python.exe", "python3", "python3.exe":
		guest = "/opt/toolchains/python/usr-local/bin/python3"
	case "cargo", "cargo.exe":
		guest = "/usr/local/cargo/bin/cargo"
	default:
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	// Historical callers use a native absolute toolchain path as the fixed
	// toolchain selector, including POSIX hosts. It is never the launched path
	// or the executable pin; new presets use the canonical guest path directly.
	if (strings.HasPrefix(intent.Executable, "/opt/toolchains/") || strings.HasPrefix(intent.Executable, "/usr/local/cargo/")) && intent.Executable != guest {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	intent.Executable = guest
	for _, argument := range intent.Arguments {
		if strings.HasPrefix(argument, `\\`) || (len(argument) > 1 && argument[1] == ':') ||
			(path.IsAbs(argument) && !strings.HasPrefix(argument, "/workspace/") && argument != "/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js" && argument != sandbox.DockerStandardCodePNPMCLI) {
			return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
		}
	}
	// The fixed runner owns its environment. These two npm preferences match
	// its fixed Node environment; callers cannot supply ignored environment.
	for _, binding := range intent.Environment {
		if (binding.Name != "npm_config_audit" && binding.Name != "npm_config_fund") || binding.Value != "false" {
			return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
		}
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || !filepath.IsAbs(root) {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	directory := filepath.Join(root, filepath.FromSlash(intent.WorkingDirectory))
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(root)
	if err != nil {
		return runner.CommandRuntimeResolvedSpec{}, err
	}
	identity, _ := json.Marshal([]string{e.identity.BackendIdentity, e.identity.Generation, guest})
	executableSHA := sha256.Sum256(identity)
	environment, _ := json.Marshal([]any{e.identity.BackendIdentity, e.identity.Generation, "fixed-runner-environment", intent.Environment})
	environmentSHA := sha256.Sum256(environment)
	return runner.CommandRuntimeResolvedSpec{Spec: intent, ExecutablePath: guest,
		ExecutableIdentityKind: runner.CommandRuntimeExecutableTemplatePathSHA256,
		ExecutableSHA256:       hex.EncodeToString(executableSHA[:]), ExecutablePinned: true,
		CanonicalArgv: append([]string{}, intent.Arguments...), AbsoluteDirectory: directory, WorkspaceRoot: root,
		EnvironmentSHA256: hex.EncodeToString(environmentSHA[:]), WorkspaceRootSHA256: rootSHA}, nil
}
