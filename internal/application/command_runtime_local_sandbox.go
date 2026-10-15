package application

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
)

const (
	CommandRuntimeLocalSandboxBackend = "local_windows_lpac"
	commandRuntimeLocalToolchainRoot  = "/toolchains/runtime"
)

type LocalSandboxCommandRuntimeStore interface {
	GetRunExecutionProfile(context.Context, string) (
		domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionInteraction(context.Context, string) (
		domain.RunExecutionInteractionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (
		domain.RunExecutionLease, bool, error)
	GetDrydockByRun(context.Context, string) (runworktree.Workspace, bool, error)
}

// LocalSandboxCommandRuntimeExecutor compiles the backend-neutral
// command-runtime.v2 process shape into the already-proven Windows
// AppContainer/LPAC LocalRunRequest. It never accepts a source Workspace root
// or restores authority from a persisted Job row.
type LocalSandboxCommandRuntimeExecutor struct {
	store    LocalSandboxCommandRuntimeStore
	backend  sandbox.LocalBackend
	identity commandruntimeadapter.Identity
}

func NewLocalSandboxCommandRuntimeExecutor(store LocalSandboxCommandRuntimeStore,
	backend sandbox.LocalBackend, readiness sandbox.LocalReadiness,
) (*LocalSandboxCommandRuntimeExecutor, error) {
	if store == nil || backend == nil || readiness.Validate() != nil ||
		!readiness.Ready || readiness.Status != sandbox.LocalReadinessReady ||
		readiness.RuntimeGeneration != backend.Generation() {
		return nil, errors.New("Local Sandbox Command Runtime readiness is invalid")
	}
	identity := commandruntimeadapter.SandboxedWorkspace(
		CommandRuntimeLocalSandboxBackend,
		sandbox.LocalBackendName+"."+sandbox.LocalBackendPolicyVersion,
		backend.Generation())
	if !identity.Executable() {
		return nil, errors.New("Local Sandbox Command Runtime identity is invalid")
	}
	return &LocalSandboxCommandRuntimeExecutor{store: store, backend: backend,
		identity: identity}, nil
}

func (e *LocalSandboxCommandRuntimeExecutor) Identity() commandruntimeadapter.Identity {
	if e == nil {
		return commandruntimeadapter.Identity{}
	}
	return e.identity
}

func (e *LocalSandboxCommandRuntimeExecutor) Available() bool {
	return e != nil && e.store != nil && e.backend != nil &&
		e.backend.Generation() == e.identity.Generation && e.identity.Executable()
}

func (e *LocalSandboxCommandRuntimeExecutor) Ready(ctx context.Context,
	runID string,
) (bool, error) {
	if ctx == nil || ctx.Err() != nil || !e.Available() ||
		!domain.ValidAgentID(runID) {
		return false, nil
	}
	readiness, err := e.backend.Readiness(ctx,
		sandbox.LocalRuntimeCapabilities{Enabled: true})
	if err != nil {
		return false, err
	}
	if readiness.Validate() != nil || !readiness.Ready ||
		readiness.Status != sandbox.LocalReadinessReady ||
		readiness.RuntimeGeneration != e.identity.Generation {
		return false, nil
	}
	interaction, err := e.store.GetRunExecutionInteraction(ctx, runID)
	if err != nil {
		return false, err
	}
	return interaction.Validate() == nil && interaction.RunID == runID &&
		interaction.Mode == domain.RunExecutionInteractionControlled &&
		interaction.ExecutionProfile == domain.RunExecutionProfileLocal &&
		interaction.NetworkScope == domain.ExecutionNetworkDisabled &&
		readiness.Ready &&
		readiness.Status == sandbox.LocalReadinessReady &&
		readiness.RuntimeGeneration == e.identity.Generation, nil
}

func (e *LocalSandboxCommandRuntimeExecutor) ExecuteSandboxCommand(ctx context.Context,
	scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec,
	stdin io.ReadCloser,
) (runner.CommandRuntimeSandboxResult, error) {
	// All returns before Run/RunWithStdin precede native process dispatch.
	noProcess := runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}
	if ctx == nil || ctx.Err() != nil || !e.Available() ||
		scope.Validate() != nil || !scope.Adapter.SameBackend(e.identity) ||
		!e.identity.AllowsPermission(scope.PermissionMode) ||
		(spec.Spec.StdinPolicy == runner.CommandRuntimeStdinClosed &&
			(stdin != nil || !spec.Spec.CloseInitialStdin || spec.Spec.InitialStdin != "")) ||
		(spec.Spec.StdinPolicy == runner.CommandRuntimeStdinPipe && stdin == nil) ||
		(spec.Spec.StdinPolicy != runner.CommandRuntimeStdinClosed &&
			spec.Spec.StdinPolicy != runner.CommandRuntimeStdinPipe) {
		return noProcess, runner.ErrCommandRuntimeBoundary
	}
	if spec.Spec.Profile == runner.CommandRuntimePowerShell &&
		!strings.EqualFold(filepath.Base(spec.ExecutablePath), "pwsh.exe") {
		return noProcess, runner.ErrCommandRuntimeLocalPowerShell
	}
	workspace, found, err := readRunFileDrydock(ctx, e.store, scope.RunID)
	if err != nil || !found {
		return noProcess, errors.Join(err,
			runner.ErrCommandRuntimeBoundary)
	}
	workspace, err = batchValidationExecutionWorkspace(ctx, workspace, scope, spec)
	if err != nil {
		return noProcess, err
	}
	profile, err := e.store.GetRunExecutionProfile(ctx, scope.RunID)
	if err != nil {
		return noProcess, err
	}
	permission, err := e.store.GetRunExecutionPermission(ctx, scope.RunID)
	if err != nil {
		return noProcess, err
	}
	interaction, err := e.store.GetRunExecutionInteraction(ctx, scope.RunID)
	if err != nil {
		return noProcess, err
	}
	lease, leaseFound, err := e.store.GetRunExecutionLease(ctx, scope.RunID)
	if err != nil {
		return noProcess, err
	}
	rootSHA256, err := runner.CommandRuntimeWorkspaceRootSHA256(workspace.Path)
	if err != nil {
		return noProcess, err
	}
	bound, bindErr := commandRuntimeDrydockBound(ctx, e.store, workspace, scope.RunID, scope.MissionID, scope.SessionID, scope.WorkspaceID)
	if bindErr != nil {
		return noProcess, bindErr
	}
	if !leaseFound || !bound ||
		(workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered) ||
		rootSHA256 != scope.WorkspaceRootSHA256 || rootSHA256 != spec.WorkspaceRootSHA256 ||
		profile.ID != scope.ProfileSnapshotID || profile.Revision != scope.ProfileRevision ||
		profile.Profile != domain.RunExecutionProfileLocal ||
		permission.ID != scope.PermissionSnapshotID ||
		permission.Revision != scope.PermissionRevision ||
		permission.Mode != scope.PermissionMode || !e.identity.AllowsPermission(permission.Mode) ||
		interaction.Mode != domain.RunExecutionInteractionControlled ||
		lease.LeaseID != scope.LeaseID || lease.Generation != scope.LeaseGeneration ||
		lease.OwnerID != scope.LeaseOwnerID || lease.Status != domain.RunExecutionLeaseActive {
		return noProcess, runner.ErrCommandRuntimeBoundary
	}

	request, err := e.compile(scope, spec, workspace, profile, permission,
		interaction, lease)
	if err != nil {
		return noProcess, err
	}
	var value sandbox.LocalExecutionResult
	var runErr error
	if err := runner.CheckCommandRuntimeDispatch(ctx, spec); err != nil {
		return noProcess, err
	}
	if spec.Spec.StdinPolicy == runner.CommandRuntimeStdinPipe {
		value, runErr = e.backend.RunWithStdin(ctx, request, stdin)
	} else {
		value, runErr = e.backend.Run(ctx, request)
	}
	result := runner.CommandRuntimeSandboxResult{ExitCode: value.ExitCode,
		Stdout:     append([]byte(nil), value.Stdout.Data...),
		Stderr:     append([]byte(nil), value.Stderr.Data...),
		TreeReaped: value.TreeReaped}
	if validateErr := value.Validate(request); validateErr != nil {
		// Validation can fail after dispatch or while cleanup is unresolved.
		// Preserve its tree proof without exposing an unvalidated output body.
		return runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: value.TreeReaped}, errors.Join(runErr, validateErr)
	}
	return result, runErr
}

func (e *LocalSandboxCommandRuntimeExecutor) compile(scope runner.CommandRuntimeScope,
	spec runner.CommandRuntimeResolvedSpec, workspace runworktree.Workspace,
	profile domain.RunExecutionProfileSnapshot,
	permission domain.RunExecutionPermissionSnapshot,
	interaction domain.RunExecutionInteractionSnapshot, lease domain.RunExecutionLease,
) (sandbox.LocalRunRequest, error) {
	if err := runner.ValidateCommandRuntimeLaunchSpec(spec); err != nil {
		return sandbox.LocalRunRequest{}, err
	}
	toolchainRoot := filepath.Clean(filepath.Dir(spec.ExecutablePath))
	// Go needs its read-only standard library and compiler alongside bin/go.
	// Select only the installed Go distribution, never arbitrary host ancestors.
	base := strings.ToLower(filepath.Base(spec.ExecutablePath))
	if base == "go.exe" || base == "go" || base == "gofmt.exe" || base == "gofmt" {
		candidate := filepath.Dir(toolchainRoot)
		if filepath.Base(toolchainRoot) == "bin" {
			if info, err := os.Stat(filepath.Join(candidate, "src", "runtime")); err == nil && info.IsDir() {
				toolchainRoot = candidate
			}
		}
	}
	toolchainSHA256, err := sandbox.LocalHostPathDigest(toolchainRoot)
	if err != nil {
		return sandbox.LocalRunRequest{}, err
	}
	drydockSHA256, err := sandbox.LocalHostPathDigest(workspace.Path)
	if err != nil {
		return sandbox.LocalRunRequest{}, err
	}
	relativeExecutable, err := filepath.Rel(toolchainRoot, spec.ExecutablePath)
	if err != nil || relativeExecutable == "." || strings.HasPrefix(relativeExecutable, "..") {
		return sandbox.LocalRunRequest{}, runner.ErrCommandRuntimeBoundary
	}
	virtualExecutable := path.Join(commandRuntimeLocalToolchainRoot,
		filepath.ToSlash(relativeExecutable))
	toolchainInputs := []sandbox.LocalToolchainInput{{ID: "command-runtime-toolchain", Root: toolchainRoot,
		VirtualRoot: commandRuntimeLocalToolchainRoot, RootSHA256: toolchainSHA256}}
	// An installed npm update can live outside the Node distribution. Bind
	// that exact package read-only instead of leaking an inaccessible host CLI
	// path into the isolated process or mounting its user-profile ancestors.
	if (base == "node.exe" || base == "node") && len(spec.CanonicalArgv) > 0 && filepath.Base(spec.CanonicalArgv[0]) == "npm-cli.js" && filepath.IsAbs(spec.CanonicalArgv[0]) {
		launcher := spec.CanonicalArgv[0]
		if relative, err := filepath.Rel(toolchainRoot, launcher); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			npmRoot := filepath.Dir(filepath.Dir(launcher))
			digest, err := sandbox.LocalHostPathDigest(npmRoot)
			if err != nil {
				return sandbox.LocalRunRequest{}, err
			}
			toolchainInputs = append(toolchainInputs, sandbox.LocalToolchainInput{ID: "command-runtime-npm", Root: npmRoot, VirtualRoot: "/toolchains/npm", RootSHA256: digest})
		}
	}
	workingDirectory := "/workspace"
	if spec.Spec.WorkingDirectory != "." {
		workingDirectory = path.Join(workingDirectory,
			filepath.ToSlash(spec.Spec.WorkingDirectory))
	}
	arguments := make([]string, len(spec.CanonicalArgv))
	for index, argument := range spec.CanonicalArgv {
		arguments[index] = commandRuntimeLocalVirtualArgument(argument,
			workspace.Path, toolchainRoot)
		if len(toolchainInputs) == 2 {
			if relative, err := filepath.Rel(toolchainInputs[1].Root, argument); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				arguments[index] = path.Join(toolchainInputs[1].VirtualRoot, filepath.ToSlash(relative))
			}
		}
	}
	environment := make([]sandbox.EnvironmentBinding, len(spec.Spec.Environment))
	for index, binding := range spec.Spec.Environment {
		environment[index] = sandbox.EnvironmentBinding{Name: binding.Name,
			Source: sandbox.EnvironmentLiteral, Value: binding.Value}
	}
	timeoutSeconds := int((spec.Spec.TimeoutMilliseconds + 999) / 1000)
	request := sandbox.LocalRunRequest{Manifest: sandbox.Manifest{
		ProtocolVersion: sandbox.ManifestProtocolVersion, Backend: sandbox.BackendLocal,
		Command: sandbox.CommandSpec{Executable: virtualExecutable,
			Arguments: arguments, WorkingDirectory: workingDirectory},
		Mounts: []sandbox.Mount{{Source: ".", Target: "/workspace",
			Access: sandbox.MountReadWrite}},
		Environment: environment, Network: sandbox.NetworkScope{Mode: "disabled"},
		Resources: sandbox.ResourceLimits{CPUQuotaMillis: 2_000,
			MemoryBytes: 2 * 1024 * 1024 * 1024, PIDs: 128,
			MaxOutputBytes: int64(spec.Spec.Output.ArtifactBytes)},
		Output:         sandbox.OutputSpec{CaptureStdout: true, CaptureStderr: true},
		TimeoutSeconds: timeoutSeconds,
		Cancellation:   sandbox.CancellationSpec{GracePeriodMillis: 2_000}},
		Binding: sandbox.LocalExecutionBinding{RunID: scope.RunID,
			MissionID: scope.MissionID, SessionID: scope.SessionID,
			WorkspaceID: scope.WorkspaceID, DrydockID: workspace.ID,
			DrydockRoot: workspace.Path, DrydockPathSHA256: drydockSHA256,
			DrydockRootFingerprint:    workspace.RootFingerprint,
			DrydockBindingFingerprint: workspace.ExpectedBindingFingerprint,
			DrydockGeneration:         workspace.Generation,
			PermissionSnapshotID:      permission.ID,
			PermissionRevision:        permission.Revision,
			ProfileSnapshotID:         profile.ID, ProfileRevision: profile.Revision,
			InteractionSnapshotID: interaction.ID,
			InteractionRevision:   interaction.Revision,
			CapabilityGeneration:  e.identity.Generation,
			LeaseID:               lease.LeaseID, LeaseGeneration: lease.Generation,
			OperationKeySHA256: runmutation.Fingerprint(
				"command_runtime_local_sandbox_operation.v1", scope.RunID,
				scope.OperationKey),
			RuntimeGeneration: e.backend.Generation()},
		ToolchainInputs: toolchainInputs,
		// PowerShell registers its ETW provider before running the requested
		// script. The pinned runtime selects this named capability, never the
		// command text. Local PowerShell normalization accepts only PowerShell 7.
		Instrumentation: spec.Spec.Profile == runner.CommandRuntimePowerShell,
		// Local accounts for the entire Job's writes, including compiler cache
		// and scratch. Docker's workspace-only growth limit is not this budget.
		MaxDiskWriteBytes: sandbox.DefaultLocalDiskWriteLimit,
		StdinPipe:         spec.Spec.StdinPolicy == runner.CommandRuntimeStdinPipe}
	if spec.AttachmentInput != nil {
		inputSHA, err := sandbox.LocalHostPathDigest(spec.AttachmentInput.Root)
		if err != nil {
			return sandbox.LocalRunRequest{}, err
		}
		request.ToolchainInputs = append(request.ToolchainInputs, sandbox.LocalToolchainInput{
			ID: "attachments-" + spec.AttachmentInput.ManifestSHA256, Root: spec.AttachmentInput.Root,
			VirtualRoot: sandbox.LocalAttachmentVirtualRoot, RootSHA256: inputSHA, DataOnly: true})
	}
	return sandbox.NormalizeLocalRunRequest(request)
}

func commandRuntimeLocalVirtualArgument(value, workspaceRoot,
	toolchainRoot string,
) string {
	for _, binding := range []struct {
		root    string
		virtual string
	}{{workspaceRoot, "/workspace"}, {toolchainRoot, commandRuntimeLocalToolchainRoot}} {
		if relative, err := filepath.Rel(binding.root, value); err == nil &&
			relative != "." && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return path.Join(binding.virtual, filepath.ToSlash(relative))
		}
	}
	return value
}

var _ runner.CommandRuntimeSandboxExecutor = (*LocalSandboxCommandRuntimeExecutor)(nil)
