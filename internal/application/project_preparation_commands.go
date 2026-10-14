package application

import (
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/workspaceidentity"
)

// Rebuild is separate from the default script-free installation so its exact
// operator consent cannot be inferred from download or install authorization.
func PrepareProjectPreparationRebuildCommand(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string) (runner.CommandRuntimeSpec, error) {
	if plan.Ecosystem != "npm" {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "install scripts are only supported for npm preparation")
	}
	spec, err := PrepareProjectPreparationCommand(plan, adapter, workspaceRoot, true)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	if len(spec.Arguments) < 2 || spec.Arguments[1] != "ci" {
		return runner.CommandRuntimeSpec{}, runner.ErrCommandRuntimeBoundary
	}
	spec.Arguments[1] = "rebuild"
	spec.Purpose = strings.Replace(spec.Purpose, "install.npm", "rebuild.npm", 1)
	return runner.NormalizeCommandRuntimeIntent(spec)
}

// PrepareProjectPreparationCommand translates a reviewed plan to fixed argv.
// The operator's scripts choice is explicit. Package contents never supply a
// host command, executable, network permission or environment binding.
func PrepareProjectPreparationCommand(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string, runInstallScripts bool) (runner.CommandRuntimeSpec, error) {
	if !plan.Ready || plan.Version != projectprep.ProtocolVersion || len(plan.Fingerprint) != 64 {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "dependency preparation plan is unavailable")
	}
	return prepareProjectPreparationSpec(plan, adapter, workspaceRoot, false, runInstallScripts)
}

func PrepareProjectPreparationTestCommand(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string) (runner.CommandRuntimeSpec, error) {
	if plan.Version != projectprep.ProtocolVersion || len(plan.Fingerprint) != 64 || plan.Test.ID == "" {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "no project test was detected")
	}
	return prepareProjectPreparationSpec(plan, adapter, workspaceRoot, true, false)
}

func prepareProjectPreparationSpec(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string, testing, runInstallScripts bool) (runner.CommandRuntimeSpec, error) {
	if adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace ||
		(adapter.Backend != CommandRuntimeLocalSandboxBackend && adapter.Backend != CommandRuntimeDockerSandboxBackend && adapter.Backend != CommandRuntimeSBXBackend) {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodePolicyDenied, "project preparation requires the selected sandbox runtime")
	}
	if err := (workspaceidentity.PathPolicy{}).Check(plan.Target); err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	id := "test.go"
	if plan.Ecosystem == "npm" {
		id = "test.npm"
	} else if plan.Ecosystem != "go" {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "dependency toolchain is unsupported")
	}
	executable, launcher, err := resolveProjectCommandToolchain(id, adapter, workspaceRoot)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	arguments := []string{"mod", "download"}
	actionID := "install.go"
	if testing {
		arguments, actionID = []string{"test", "-mod=readonly", "./..."}, "test.go"
	}
	if plan.Ecosystem == "npm" {
		cache, err := filepath.Rel(filepath.FromSlash(plan.Target), filepath.FromSlash(projectprep.NPMCacheDirectory(plan.Target)))
		if err != nil {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "project cache path is unavailable")
		}
		scripts := "--ignore-scripts"
		if runInstallScripts {
			scripts = "--ignore-scripts=false"
		}
		arguments, actionID = []string{"ci", "--offline", scripts, "--bin-links=false", "--global=false", "--prefix=.", "--no-audit", "--no-fund", "--cache=" + filepath.ToSlash(cache)}, "install.npm"
		if testing {
			arguments, actionID = []string{"test", "--offline", "--global=false", "--prefix=.", "--no-audit", "--no-fund", "--cache=" + filepath.ToSlash(cache)}, "test.npm"
		}
		arguments = append(append([]string{}, launcher...), arguments...)
	}
	return runner.NormalizeCommandRuntimeIntent(runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: executable, Arguments: arguments, WorkingDirectory: plan.Target,
		Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 300000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288},
		Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "Project preparation " + actionID + " " + plan.Fingerprint,
	})
}
