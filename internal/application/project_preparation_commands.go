package application

import (
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/workspaceidentity"
)

// Rebuild is separate from the default script-free installation so its exact
// operator consent cannot be inferred from download or install authorization.
func PrepareProjectPreparationRebuildCommand(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string) (runner.CommandRuntimeSpec, error) {
	if plan.Ecosystem == "pnpm" {
		return preparePNPMPreparationSpec(plan, adapter, "rebuild")
	}
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

// Fetch fills pnpm's private native store from broker-verified local tarballs.
func PrepareProjectPreparationFetchCommand(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string) (runner.CommandRuntimeSpec, error) {
	if plan.Ecosystem != "pnpm" {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "store fetch is only supported for pnpm preparation")
	}
	return preparePNPMPreparationSpec(plan, adapter, "fetch")
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
	if plan.Ecosystem == "pnpm" {
		action := "install"
		if testing {
			action = "test"
		}
		// The installation always ignores scripts. The separate rebuild Job
		// carries the explicit scripts authorization.
		return preparePNPMPreparationSpec(plan, adapter, action)
	}
	// Test preparation emits an intent; execution still needs the selected
	// backend and exact command approval. Dependency installation stays sandboxed.
	if !(testing && adapter.Kind == commandruntimeadapter.KindHostUnsandboxed) &&
		(adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace ||
			(adapter.Backend != CommandRuntimeLocalSandboxBackend && adapter.Backend != CommandRuntimeDockerSandboxBackend && adapter.Backend != CommandRuntimeSBXBackend)) {
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

func preparePNPMPreparationSpec(plan projectprep.Plan, adapter commandruntimeadapter.Identity, action string) (runner.CommandRuntimeSpec, error) {
	if plan.Version != projectprep.ProtocolVersion || len(plan.Fingerprint) != 64 || (action != "test" && !plan.Ready) {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "dependency preparation plan is unavailable")
	}
	executable, launcher, err := resolveProjectCommandToolchain("test.pnpm", adapter)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	policy := workspaceidentity.PathPolicy{}
	if err := policy.Check(plan.Target); err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	if plan.WorkspaceRoot == "" {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "pnpm workspace root is unavailable")
	}
	if err := policy.Check(plan.WorkspaceRoot); err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	if plan.WorkspaceRoot != "." && plan.Target != plan.WorkspaceRoot && !strings.HasPrefix(plan.Target, plan.WorkspaceRoot+"/") {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "pnpm test target is outside its workspace")
	}
	workingDirectory := plan.WorkspaceRoot
	common := []string{"--offline", "--config.ignore-pnpmfile=true", "--config.manage-package-manager-versions=false", "--reporter=append-only", "--store-dir=" + sandbox.DockerStandardCodeWorkspaceTarget + "/" + projectprep.PNPMStoreDirectory(plan.Target)}
	arguments := []string{}
	switch action {
	case "fetch":
		workingDirectory = projectprep.PNPMSeedDirectory(plan.Target)
		arguments = append([]string{"fetch", "--frozen-lockfile", "--ignore-scripts"}, common...)
	case "install":
		arguments = append([]string{"install", "--frozen-lockfile", "--ignore-scripts"}, common...)
	case "rebuild":
		arguments = append([]string{"--recursive", "rebuild", "--pending", "--ignore-scripts=false"}, common...)
	case "test":
		workingDirectory = plan.Target
		arguments = append(common, "--config.verify-deps-before-run=false", "test")
	default:
		return runner.CommandRuntimeSpec{}, runner.ErrCommandRuntimeBoundary
	}
	return runner.NormalizeCommandRuntimeIntent(runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: executable, Arguments: append(launcher, arguments...), WorkingDirectory: workingDirectory,
		Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 300000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288},
		Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "Project preparation " + action + ".pnpm " + plan.Fingerprint,
	})
}
