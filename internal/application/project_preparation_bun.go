package application

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/workspaceidentity"
)

const bunRuntimeReason = "Bun preparation requires the Standard Code Docker image with Bun " + projectprep.BunVersion

func (s *CommandRuntimeService) bunPreparationReady(ctx context.Context) error {
	if s.adapter.Backend != CommandRuntimeDockerSandboxBackend {
		return apperror.New(apperror.CodeFailedPrecondition, bunRuntimeReason)
	}
	executor, ok := s.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Docker Bun preparation is unavailable")
	}
	probe, ok := executor.service.docker.readiness.(interface {
		BunPreparationAvailable(context.Context, string) (bool, error)
	})
	if ok {
		ready, err := probe.BunPreparationAvailable(ctx, executor.service.imageDigest)
		if err != nil {
			return apperror.New(apperror.CodeUnavailable, "Docker Bun image inspection failed")
		}
		if ready {
			return nil
		}
	}
	return apperror.New(apperror.CodeFailedPrecondition, "rebuild the Standard Code Docker image with Bun "+projectprep.BunVersion+" to enable Bun preparation")
}

func prepareBunPreparationSpec(plan projectprep.Plan, adapter commandruntimeadapter.Identity, testing, scripts bool) (runner.CommandRuntimeSpec, error) {
	if !plan.Ready || plan.Version != projectprep.ProtocolVersion || len(plan.Fingerprint) != 64 {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "reviewed Bun preparation plan is unavailable")
	}
	if adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace || adapter.Backend != CommandRuntimeDockerSandboxBackend {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, bunRuntimeReason)
	}
	if scripts {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "Bun preparation always ignores lifecycle scripts")
	}
	if err := (workspaceidentity.PathPolicy{}).Check(plan.Target); err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	actionID := "install.bun"
	args := []string{"install", "--offline", "--frozen-lockfile", "--ignore-scripts", "--force", "--backend=copyfile", "--cache-dir=" + sandbox.DockerStandardCodeWorkspaceTarget + "/" + projectprep.BunCacheDirectory(plan.Target)}
	if testing {
		if plan.Test.ID != "test.bun" {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "no Bun test script was detected")
		}
		actionID, args = "test.bun", []string{"run", "--no-install", "test"}
	}
	return runner.NormalizeCommandRuntimeIntent(runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: sandbox.DockerStandardCodeBunExecutable, Arguments: args, WorkingDirectory: plan.Target, Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 300000,
		Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288}, Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "Project preparation " + actionID + " " + plan.Fingerprint})
}
