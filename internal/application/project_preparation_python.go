package application

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"path/filepath"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/workspaceidentity"
)

func preparePythonPreparationSpec(plan projectprep.Plan, adapter commandruntimeadapter.Identity, workspaceRoot string, testing bool, module string) (runner.CommandRuntimeSpec, error) {
	if !plan.Ready || len(plan.PythonSeed) == 0 {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "a reviewed Python wheel plan is required")
	}
	if !(testing && adapter.Kind == commandruntimeadapter.KindHostUnsandboxed) && (adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace || adapter.Backend != CommandRuntimeDockerSandboxBackend) {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "Python dependency preparation requires the Standard Code Docker image with pip >=25.3")
	}
	if err := (workspaceidentity.PathPolicy{}).Check(plan.Target); err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	executable, _, err := resolveProjectCommandToolchain("test.python", adapter, workspaceRoot)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	root := filepath.ToSlash(workspaceRoot)
	if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		root = sandbox.DockerStandardCodeWorkspaceTarget + "/"
	}
	sum := sha256.Sum256(plan.PythonSeed)
	common := []string{root, path.Join(root, projectprep.PythonPlanFile(plan.Target)), hex.EncodeToString(sum[:])}
	arguments := append(projectprep.PythonPreparationProgram(false), common...)
	arguments = append(arguments, path.Join(root, projectprep.PythonWheelDirectory(plan.Target)), path.Join(root, projectprep.PythonSiteDirectory(plan.Target)))
	action := "install.python"
	if testing {
		arguments = append(projectprep.PythonPreparationProgram(true), common...)
		arguments = append(arguments, path.Join(root, projectprep.PythonSiteDirectory(plan.Target)), module)
		if module == "unittest" {
			arguments = append(arguments, "discover", "-v")
		}
		action = "test.python"
	}
	return runner.NormalizeCommandRuntimeIntent(runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess, Executable: executable, Arguments: arguments, WorkingDirectory: plan.Target,
		Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 300000,
		Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288}, Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "Project preparation " + action + " " + plan.Fingerprint,
	})
}
