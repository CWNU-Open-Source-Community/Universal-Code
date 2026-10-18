package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
)

const CommandRuntimeDockerSandboxBackend = "docker_standard_code"

// DockerSandboxCommandRuntimeExecutor projects command-runtime.v2's native
// process profile onto the fixed Standard Code Docker toolchains. Image,
// endpoint, mounts, network, credentials, and Docker flags remain Go-owned.
type DockerSandboxCommandRuntimeExecutor struct {
	service  *StandardCodeDockerService
	identity commandruntimeadapter.Identity
}

type dockerCommandRuntimeAdmissionKey struct{}
type dockerCommandRuntimeAdmission struct {
	runID   string
	confirm func(context.Context) error
}

func confirmDockerCommandRuntimeAdmission(ctx context.Context, runID string) error {
	if admission, ok := ctx.Value(dockerCommandRuntimeAdmissionKey{}).(dockerCommandRuntimeAdmission); ok {
		if admission.runID != runID || admission.confirm == nil {
			return runner.ErrCommandRuntimeBoundary
		}
		return admission.confirm(ctx)
	}
	return nil
}

func (*DockerSandboxCommandRuntimeExecutor) CommandRuntimeAdmissionRequired() bool { return true }

func NewDockerSandboxCommandRuntimeExecutor(service *StandardCodeDockerService) (
	*DockerSandboxCommandRuntimeExecutor, error,
) {
	if service == nil || service.docker == nil {
		return nil, errors.New("Docker Standard Code Command Runtime is unavailable")
	}
	generation, err := service.docker.StandardCodeCapabilityGeneration()
	if err != nil {
		return nil, err
	}
	identity := commandruntimeadapter.SandboxedWorkspace(
		CommandRuntimeDockerSandboxBackend, "docker-standard-code-guest.v1", generation)
	if !identity.Executable() {
		return nil, errors.New("Docker Standard Code Command Runtime identity is invalid")
	}
	return &DockerSandboxCommandRuntimeExecutor{service: service,
		identity: identity}, nil
}

func (e *DockerSandboxCommandRuntimeExecutor) Identity() commandruntimeadapter.Identity {
	if e == nil {
		return commandruntimeadapter.Identity{}
	}
	return e.identity
}

func (e *DockerSandboxCommandRuntimeExecutor) Available() bool {
	if e == nil || e.service == nil || e.service.docker == nil ||
		!e.identity.Executable() || !e.service.docker.commandRuntimeStdinAvailable() {
		return false
	}
	generation, err := e.service.docker.StandardCodeCapabilityGeneration()
	return err == nil && generation == e.identity.Generation
}

func (e *DockerSandboxCommandRuntimeExecutor) Ready(ctx context.Context,
	runID string,
) (bool, error) {
	if !e.Available() {
		return false, nil
	}
	workspace, found, err := readRunFileDrydock(ctx, e.service.store, runID)
	if err != nil || !found {
		return false, err
	}
	command := standardcode.Command{ProtocolVersion: standardcode.CommandProtocolVersion,
		Toolchain: sandbox.DockerStandardCodeToolchainGo,
		Arguments: []string{"version"}, WorkingDirectory: ".", TimeoutSeconds: 30,
		Purpose: "probe the installed Command Runtime Docker adapter"}
	readiness, err := e.service.Readiness(ctx, StandardCodeDockerReadinessRequest{
		RunID: runID, ExpectedGeneration: workspace.Generation,
		ExpectedCheckpoint: workspace.LastCheckpointID, Command: command})
	return err == nil && readiness.Status == standardcode.ReadinessReady &&
		len(readiness.BlockedBy) == 0, err
}

func (e *DockerSandboxCommandRuntimeExecutor) ExecuteSandboxCommand(ctx context.Context,
	scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec,
	stdin io.ReadCloser,
) (runner.CommandRuntimeSandboxResult, error) {
	// Preparation and admission review create no container. After Start is
	// attempted, only its actual terminal receipt can prove the tree is reaped.
	noProcess := runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}
	if ctx == nil || ctx.Err() != nil {
		return noProcess, fmt.Errorf(
			"%w: execution context is unavailable", runner.ErrCommandRuntimeBoundary)
	}
	if !e.Available() {
		return noProcess, fmt.Errorf(
			"%w: Docker Standard Code adapter is unavailable or stale",
			runner.ErrCommandRuntimeBoundary)
	}
	if scope.Validate() != nil || !scope.Adapter.SameBackend(e.identity) {
		return noProcess, fmt.Errorf(
			"%w: execution scope does not match the Docker Standard Code adapter",
			runner.ErrCommandRuntimeBoundary)
	}
	if !e.identity.AllowsPermission(scope.PermissionMode) ||
		spec.Spec.Profile != runner.CommandRuntimeProcess ||
		(spec.Spec.StdinPolicy == runner.CommandRuntimeStdinClosed &&
			(stdin != nil || !spec.Spec.CloseInitialStdin || spec.Spec.InitialStdin != "")) ||
		(spec.Spec.StdinPolicy == runner.CommandRuntimeStdinPipe && stdin == nil) ||
		(spec.Spec.StdinPolicy != runner.CommandRuntimeStdinClosed &&
			spec.Spec.StdinPolicy != runner.CommandRuntimeStdinPipe) {
		return noProcess, fmt.Errorf(
			"%w: command or stdin policy is unsupported by Docker Standard Code",
			runner.ErrCommandRuntimeBoundary)
	}
	command, err := commandRuntimeDockerCommand(spec)
	if err != nil {
		return noProcess, err
	}
	ctx, err = workerCommandDockerContext(ctx, scope, spec, command)
	if err != nil {
		return noProcess, err
	}
	requestedBy := scope.AgentID
	if proof := workerCommandRuntimeFromContext(ctx); proof != nil {
		requestedBy = workerCommandDockerRequester(proof)
	}
	if proof := batchValidationRuntimeFromContext(ctx); proof != nil {
		if err := proof.check(ctx); err != nil {
			return noProcess, err
		}
		requestedBy = batchValidationDockerRequester(proof.target)
		if _, _, err := parseBatchValidationDockerRequester(requestedBy); err != nil {
			return noProcess, err
		}
	}
	if proof := gitBisectRuntimeFromContext(ctx); proof != nil {
		if batchValidationRuntimeFromContext(ctx) != nil || proof.runID != scope.RunID || proof.specFingerprint != runner.CommandRuntimeSpecFingerprint(spec) || proof.check == nil || proof.check(ctx) != nil {
			return noProcess, runner.ErrCommandRuntimeBoundary
		}
		requestedBy = gitBisectDockerRequester(proof)
		if _, _, err := parseGitBisectDockerRequester(requestedBy); err != nil {
			return noProcess, err
		}
	}
	workspace, found, err := readRunFileDrydock(ctx, e.service.store, scope.RunID)
	if err != nil || !found {
		return noProcess, errors.Join(err,
			runner.ErrCommandRuntimeBoundary)
	}
	baseKey := "command-runtime-docker-" + runmutation.Fingerprint(
		"command_runtime_docker_adapter_operation.v1", scope.RunID,
		scope.OperationKey)[:24]
	if spec.Spec.WorkspaceMode == runner.CommandRuntimeWorkspaceSharedService &&
		workerCommandRuntimeFromContext(ctx) == nil && batchValidationRuntimeFromContext(ctx) == nil && gitBisectRuntimeFromContext(ctx) == nil {
		job, err := e.sharedServiceJob(ctx, scope, spec)
		if err != nil {
			return noProcess, err
		}
		baseKey = commandRuntimeDockerServiceKey(job)
		requestedBy = commandRuntimeDockerServiceRequester(job)
		commandSHA256, err := command.Fingerprint()
		if err != nil {
			return noProcess, err
		}
		ctx = context.WithValue(ctx, commandRuntimeDockerServiceProofKey{}, &commandRuntimeDockerServiceProof{
			job: job, commandSHA256: commandSHA256, check: func(current context.Context) error {
				if _, err := e.sharedServiceJob(current, scope, spec); err != nil {
					return err
				}
				return runner.CheckCommandRuntimeDispatch(current, spec)
			},
		})
	}
	stdinPolicy := sandbox.DockerStandardCodeStdinClosed
	if spec.Spec.StdinPolicy == runner.CommandRuntimeStdinPipe {
		stdinPolicy = sandbox.DockerStandardCodeStdinPipe
	}
	prepared, err := e.service.prepareCommandRuntime(ctx, StandardCodeDockerPrepareRequest{
		RunID: scope.RunID, ExpectedGeneration: workspace.Generation,
		ExpectedCheckpoint: workspace.LastCheckpointID,
		OperationKey:       baseKey + "-prepare", RequestedBy: requestedBy,
		Command: command}, stdinPolicy)
	if err != nil {
		return noProcess, err
	}
	if prepared.Blocked || prepared.Preparation == nil || prepared.Approval == nil {
		return noProcess, errors.New(
			"Docker Standard Code adapter is not ready for this command")
	}
	decision, err := e.service.manifests.ReviewApproval(ctx,
		prepared.Preparation.Preparation.ID, approval.ActionApprove,
		baseKey+"-approve", "command_runtime_adapter", "")
	if err != nil {
		return noProcess, err
	}
	lease, found, err := e.service.store.GetRunExecutionLease(ctx, scope.RunID)
	if err != nil || !found || lease.LeaseID != scope.LeaseID ||
		lease.Generation != scope.LeaseGeneration || lease.OwnerID != scope.LeaseOwnerID ||
		lease.Status != domain.RunExecutionLeaseActive {
		return noProcess, errors.Join(err,
			fmt.Errorf("%w: Run execution lease changed during Docker admission",
				runner.ErrCommandRuntimeBoundary))
	}
	executionCtx, output := withDockerCommandRuntimeOutput(ctx, scope.RunID,
		spec.Spec.Output.ArtifactBytes)
	executionCtx = context.WithValue(executionCtx, dockerCommandRuntimeAdmissionKey{}, dockerCommandRuntimeAdmission{
		runID: scope.RunID, confirm: func(current context.Context) error { return runner.ConfirmCommandRuntimeDispatch(current, spec) },
	})
	if err := runner.CheckCommandRuntimeDispatch(executionCtx, spec); err != nil {
		return noProcess, err
	}
	executed, err := e.service.executeCommandRuntime(executionCtx,
		StandardCodeDockerExecuteRequest{
			RunID: scope.RunID, ExpectedGeneration: workspace.Generation,
			ExpectedCheckpoint: workspace.LastCheckpointID,
			PreparationID:      prepared.Preparation.Preparation.ID,
			ApprovalID:         decision.Approval.ID, OperationKey: baseKey + "-execute",
			RequestedBy: requestedBy, Command: command}, lease, stdinPolicy, stdin)
	if executed.batchRecord != nil {
		record := executed.batchRecord
		if !executed.Executed || record.Validate() != nil || record.Receipt == nil || record.Receipt.ExitCode == nil || !record.Receipt.CleanupComplete {
			return runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: output.noOwnedTree()}, errors.New("native Docker terminal receipt is unavailable")
		}
		if record.Receipt.Outcome != domain.DockerSandboxOutcomeSucceeded &&
			!(gitBisectRuntimeFromContext(ctx) != nil && gitBisectDockerProcessExit(record.Receipt, err)) {
			err = errors.Join(err, errors.New("native Docker command did not succeed"))
		}
		return output.terminal(*record.Receipt.ExitCode), err
	}
	if !executed.Executed || executed.Result == nil ||
		executed.Result.Validate() != nil || executed.Result.ExitCode == nil {
		return runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: output.noOwnedTree()}, errors.Join(err, errors.New(
			"Docker Standard Code adapter did not return a terminal receipt"))
	}
	// An actual terminal failure still carries its bounded captured diagnostics.
	// No terminal/cleanup receipt above means no success-shaped output is exposed.
	return output.terminalResult(*executed.Result, err)
}

func commandRuntimeDockerCommand(spec runner.CommandRuntimeResolvedSpec) (
	standardcode.Command, error,
) {
	base := strings.ToLower(filepath.Base(spec.ExecutablePath))
	toolchain := ""
	switch base {
	case "go", "go.exe":
		toolchain = sandbox.DockerStandardCodeToolchainGo
	case "gofmt", "gofmt.exe":
		toolchain = sandbox.DockerStandardCodeToolchainGofmt
	case "node", "node.exe":
		toolchain = sandbox.DockerStandardCodeToolchainNode
	case "python", "python.exe", "python3", "python3.exe":
		toolchain = sandbox.DockerStandardCodeToolchainPython
	case "cargo", "cargo.exe":
		toolchain = sandbox.DockerStandardCodeToolchainRust
	default:
		return standardcode.Command{}, errors.New(
			"Docker Standard Code accepts only the fixed Go, Node, Python, or Rust toolchain")
	}
	if toolchain == sandbox.DockerStandardCodeToolchainGo && len(spec.CanonicalArgv) == 2 && spec.CanonicalArgv[0] == "mod" && spec.CanonicalArgv[1] == "download" {
		toolchain = sandbox.DockerStandardCodeToolchainGoPrepare
	}
	if toolchain == sandbox.DockerStandardCodeToolchainNode && len(spec.CanonicalArgv) > 1 && spec.CanonicalArgv[0] == "/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js" && (spec.CanonicalArgv[1] == "ci" || spec.CanonicalArgv[1] == "rebuild") {
		toolchain = sandbox.DockerStandardCodeToolchainNodePrepare
	}
	command := standardcode.Command{ProtocolVersion: standardcode.CommandProtocolVersion,
		Toolchain: toolchain, Arguments: append([]string(nil), spec.CanonicalArgv...),
		WorkingDirectory: filepath.ToSlash(spec.Spec.WorkingDirectory),
		TimeoutSeconds:   int((spec.Spec.TimeoutMilliseconds + 999) / 1000),
		Purpose:          spec.Spec.Purpose}
	if err := command.Validate(); err != nil {
		return standardcode.Command{}, err
	}
	return command, nil
}

var _ runner.CommandRuntimeSandboxExecutor = (*DockerSandboxCommandRuntimeExecutor)(nil)

func (*DockerSandboxCommandRuntimeExecutor) OwnsWorkspaceCheckpoint() bool { return true }
