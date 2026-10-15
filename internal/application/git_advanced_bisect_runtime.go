package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/workspace"
)

// Only the application installs this selector. Recipes never choose an adapter
// or supply process arguments.
func (s *GitAdvancedService) WithBisectCommandRuntime(runtime any) *GitAdvancedService {
	if s != nil {
		s.bisectRuntime, _ = runtime.(ProjectActionRuntime)
	}
	return s
}

type gitBisectRuntimeKey struct{}
type gitBisectRuntimeProof struct {
	runID, root, rootIdentity, operationID, commandSHA256, specFingerprint string
	check                                                                  func(context.Context) error
}
type gitBisectJobBinding struct {
	proof                            *gitBisectRuntimeProof
	operationDigest, specFingerprint string
}

func gitBisectRuntimeFromContext(ctx context.Context) *gitBisectRuntimeProof {
	proof, _ := ctx.Value(gitBisectRuntimeKey{}).(*gitBisectRuntimeProof)
	return proof
}

type gitBisectRecipeExecutor struct {
	service     *GitAdvancedService
	runtime     *CommandRuntimeService
	authority   gitAdvancedAuthority
	record      gitadvanced.OperationRecord
	preview     gitadvanced.Preview
	guard       toolcontract.DispatchGuard
	fingerprint string
}

func (s *GitAdvancedService) bisectRecipeExecutor(ctx context.Context, authority gitAdvancedAuthority,
	record gitadvanced.OperationRecord, preview gitadvanced.Preview, guard toolcontract.DispatchGuard,
) (*gitBisectRecipeExecutor, error) {
	if s.bisectRuntime == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "selected Git bisect command runtime is unavailable")
	}
	runtime, err := s.selectBisectCommandRuntime(ctx, authority.run.ID)
	if err != nil {
		return nil, err
	}
	operation, err := repository.AdvancedOperation(preview)
	if err != nil {
		return nil, err
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return nil, err
	}
	return &gitBisectRecipeExecutor{service: s, runtime: runtime, authority: authority,
		record: record, preview: preview, guard: guard, fingerprint: fingerprint}, nil
}

func (e *gitBisectRecipeExecutor) ExecuteBisectRecipe(ctx context.Context, step repository.BisectRecipeStep) (runner.OnceStartResult, gitadvanced.BisectExecutionReceipt, error) {
	var result runner.OnceStartResult
	receipt := gitadvanced.BisectExecutionReceipt{Commit: step.Commit, Ordinal: step.Ordinal}
	if step.Root != e.authority.workspace.RootPath || step.SequenceID != e.preview.Spec.SequenceID ||
		e.preview.Spec.Recipe == nil || step.RecipeName != e.preview.Spec.Recipe.Name || step.Ordinal < 0 ||
		step.Ordinal >= e.preview.Spec.Recipe.MaxSteps || step.TimeoutSeconds != e.preview.Spec.Recipe.TimeoutSeconds {
		return result, receipt, runner.ErrCommandRuntimeBoundary
	}
	rootIdentity, err := workspace.AgentCodeRootFingerprint(step.Root)
	if err != nil {
		return result, receipt, err
	}
	check := func(current context.Context) error {
		if err := e.guard(current, e.fingerprint); err != nil {
			return err
		}
		if !e.runtime.commandRuntimeAdapterCurrent() {
			return runner.ErrCommandRuntimeBoundary
		}
		identity, err := workspace.AgentCodeRootFingerprint(step.Root)
		if err != nil || identity != rootIdentity {
			return runner.ErrCommandRuntimeBoundary
		}
		return nil
	}
	checkRevision := func(current context.Context) error {
		if err := check(current); err != nil {
			return err
		}
		binding, err := e.service.executor.CaptureAdvancedBinding(current, step.Root)
		if err != nil {
			return err
		}
		if binding.Head != step.Commit || binding.RepositorySHA256 != e.preview.Binding.RepositorySHA256 ||
			binding.CommonDirSHA256 != e.preview.Binding.CommonDirSHA256 {
			return runner.ErrCommandRuntimeBoundary
		}
		return nil
	}
	if err := checkRevision(ctx); err != nil {
		return result, receipt, err
	}
	id := "test.go"
	if step.RecipeName == gitadvanced.RecipeNPMTest {
		id = "test.npm"
	}
	executable, launcher, err := resolveProjectCommandToolchain(id, e.runtime.adapter, step.Root)
	if err != nil {
		return result, receipt, err
	}
	args := []string{"test", "-count=1", "./..."}
	if step.RecipeName == gitadvanced.RecipeNPMTest {
		args = append(launcher, "test", "--", "--runInBand")
	}
	intent := runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: executable, Arguments: args, WorkingDirectory: ".",
		Environment: []runner.CommandRuntimeEnvironment{{Name: "npm_config_audit", Value: "false"}, {Name: "npm_config_fund", Value: "false"}},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: int64(step.TimeoutSeconds) * 1000,
		Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MinCommandRuntimeInlineBytes, ArtifactBytes: runner.MaxCommandRuntimeArtifactBytes},
		Network:             runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "git-bisect:" + e.record.ID + ":" + step.Commit}
	resolved, err := e.runtime.normalizeCommandRuntimeSpec(intent, step.Root)
	if err != nil {
		return result, receipt, err
	}
	if e.runtime.adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		if err := check(ctx); err != nil {
			return result, receipt, err
		}
		if err := gitBisectHostEnvironment(&resolved); err != nil {
			return result, receipt, err
		}
	}
	root, found, err := e.runtime.store.GetRootAgent(ctx, e.authority.run.ID)
	if err != nil || !found {
		return result, receipt, apperror.New(apperror.CodeFailedPrecondition, "Git bisect Root anchor is unavailable")
	}
	permission := e.authority.permission
	generation, live := e.service.permissionCapabilities.FullAccessGeneration(permission)
	authority := e.service.permissionCapabilities.RuntimeAuthority
	if authority == nil || !live {
		return result, receipt, runner.ErrCommandRuntimeBoundary
	}
	fence, found := authority.RunAuthorizationFence(e.authority.run.ID)
	if !found {
		return result, receipt, runner.ErrCommandRuntimeBoundary
	}
	key := "git-bisect-" + gitadvanced.Fingerprint("git-bisect-job.v1", e.record.ID, step.RequestFingerprint)
	scope := runner.CommandRuntimeScope{InvocationID: key, OperationKey: key, RunID: e.authority.run.ID,
		MissionID: e.authority.mission.ID, RootAgentID: root.ID, AgentID: root.ID, AttributionSource: domain.AgentAttributionOperatorRoot,
		SessionID: e.authority.session.ID, WorkspaceID: e.authority.workspace.ID, WorkspaceRootSHA256: resolved.WorkspaceRootSHA256,
		ModeSnapshotID: e.authority.mode.ID, ModeRevision: e.authority.mode.Revision,
		ProfileSnapshotID: e.authority.profile.ID, ProfileRevision: e.authority.profile.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
		PermissionRuntimeEpoch: authority.RuntimeEpoch(), PermissionGeneration: generation, RunAuthorizationFence: fence,
		LeaseID: e.authority.lease.LeaseID, LeaseGeneration: e.authority.lease.Generation, LeaseOwnerID: e.authority.lease.OwnerID,
		Adapter: e.runtime.adapter}
	proof := &gitBisectRuntimeProof{runID: scope.RunID, root: step.Root, rootIdentity: rootIdentity, operationID: e.record.ID, check: check,
		specFingerprint: runner.CommandRuntimeSpecFingerprint(resolved)}
	if e.runtime.adapter.Backend == CommandRuntimeDockerSandboxBackend {
		command, err := commandRuntimeDockerCommand(resolved)
		if err != nil {
			return result, receipt, err
		}
		proof.commandSHA256, err = command.Fingerprint()
		if err != nil {
			return result, receipt, err
		}
	}
	ctx = context.WithValue(ctx, gitBisectRuntimeKey{}, proof)
	checkedAt, err := e.runtime.gitBisectReadiness(ctx, scope.RunID)
	if err != nil {
		return result, receipt, err
	}
	operationDigest, jobID := runner.CommandRuntimeOperationIdentity(scope.RunID, key)
	binding := &gitBisectJobBinding{proof: proof, operationDigest: operationDigest, specFingerprint: runner.CommandRuntimeSpecFingerprint(resolved)}
	if _, loaded := e.runtime.gitBisectJobs.LoadOrStore(jobID, binding); loaded {
		return result, receipt, runner.ErrCommandRuntimeUncertain
	}
	defer func() {
		stored, err := e.runtime.store.GetCommandRuntimeJob(context.WithoutCancel(ctx), jobID)
		if err == nil && stored.State.Terminal() && stored.TreeReaped {
			e.runtime.gitBisectJobs.CompareAndDelete(jobID, binding)
		}
	}()
	receipt.Execution = *batchValidationExecutionReceipt(e.runtime.adapter, resolved.WorkspaceRootSHA256)
	receipt.Execution.ReadinessCheckedAt = checkedAt
	receipt.Execution.JobID = jobID
	if docker, ok := e.runtime.sandbox.(*DockerSandboxCommandRuntimeExecutor); ok {
		receipt.Execution.ImageDigest = docker.service.imageDigest
	}
	if sbx, ok := e.runtime.sandbox.(*SBXCommandRuntimeExecutor); ok {
		receipt.Execution.TemplateReference = sbx.backend.TemplateReference()
	}
	job, replayed, startErr := e.runtime.manager.Start(ctx, runner.CommandRuntimeStartRequest{Scope: scope, Spec: resolved,
		DiscardOutput: true, OwnershipCheck: check, DispatchCheck: func(current context.Context, actual runner.CommandRuntimeResolvedSpec) error {
			if runner.CommandRuntimeSpecFingerprint(actual) != binding.specFingerprint {
				return runner.ErrCommandRuntimeBoundary
			}
			return checkRevision(current)
		}})
	if job.ID == "" {
		e.runtime.gitBisectJobs.CompareAndDelete(jobID, binding)
		receipt.Execution.JobID = ""
		return result, receipt, gitBisectExecutionError(startErr)
	}
	if replayed {
		return result, receipt, runner.ErrCommandRuntimeUncertain
	}
	waitErr := startErr
	if waitErr == nil {
		waitErr = e.runtime.waitForOperatorCommand(ctx, job.ID)
	}
	stored, readErr := e.runtime.store.GetCommandRuntimeJob(context.WithoutCancel(ctx), job.ID)
	if readErr != nil {
		return result, receipt, errors.New("Git bisect command completion record is unavailable")
	}
	receipt.Execution.TreeReaped, receipt.Execution.StdinClosed = stored.TreeReaped, stored.StdinClosed
	if stored.ExitCode == nil || stored.StartedAt == nil || stored.CompletedAt == nil || !stored.TreeReaped || !stored.StdinClosed {
		return result, receipt, runner.ErrCommandRuntimeUncertain
	}
	result = runner.OnceStartResult{StartedAt: *stored.StartedAt, CompletedAt: *stored.CompletedAt, ExitCode: *stored.ExitCode,
		TreeReaped: stored.TreeReaped, StdinClosed: stored.StdinClosed, TimedOut: stored.TimedOut, Cancelled: stored.Cancelled,
		Stdout: batchValidationJobCapture(stored.StdoutObservedBytes, stored.StdoutSHA256), Stderr: batchValidationJobCapture(stored.StderrObservedBytes, stored.StderrSHA256)}
	if waitErr != nil {
		return result, receipt, gitBisectExecutionError(waitErr)
	}
	if result.ExitCode < 0 || result.ExitCode > 127 {
		return result, receipt, errors.New("Git bisect command has no confirmed recipe exit status")
	}
	if stored.State != runner.CommandRuntimeJobCompleted && stored.State != runner.CommandRuntimeJobFailed {
		return result, receipt, fmt.Errorf("Git bisect Job ended in %s", stored.State)
	}
	return result, receipt, checkRevision(ctx)
}

func gitBisectExecutionError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("Git bisect command execution did not return a confirmed result")
}

func (s *GitAdvancedService) gitAdvancedProfileAllowed(profile domain.RunExecutionProfile) bool {
	return profile == domain.RunExecutionProfileLocal || s.bisectRuntime != nil &&
		(profile == domain.RunExecutionProfileDocker || profile == domain.RunExecutionProfileSBX)
}

func gitBisectHostEnvironment(resolved *runner.CommandRuntimeResolvedSpec) error {
	cacheRoot := filepath.Join(os.TempDir(), "cyberagent-git-bisect-cache")
	if err := os.MkdirAll(cacheRoot, 0700); err != nil {
		return errors.New("Git bisect recipe cache is unavailable")
	}
	resolved.Environment = repository.ProjectValidationEnvironment(cacheRoot)
	raw, _ := json.Marshal(resolved.Environment)
	digest := sha256.Sum256(raw)
	resolved.EnvironmentSHA256 = hex.EncodeToString(digest[:])
	return nil
}

func (s *GitAdvancedService) requireBisectRuntime(ctx context.Context, runID, root string, spec gitadvanced.Spec) error {
	if spec.Operation != gitadvanced.BisectRun {
		return nil
	}
	if s.bisectRuntime == nil || spec.Recipe == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "selected Git bisect command runtime is unavailable")
	}
	selected, err := s.selectBisectCommandRuntime(ctx, runID)
	if err != nil {
		return err
	}
	id := "test.go"
	if spec.Recipe.Name == gitadvanced.RecipeNPMTest {
		id = "test.npm"
	}
	_, _, err = resolveProjectCommandToolchain(id, selected.adapter, root)
	if err != nil {
		return err
	}
	if err := selected.projectPresetReady(ctx, id); err != nil {
		return err
	}
	_, err = selected.gitBisectReadiness(ctx, runID)
	return err
}

func (s *GitAdvancedService) selectBisectCommandRuntime(ctx context.Context, runID string) (*CommandRuntimeService, error) {
	var candidates []*CommandRuntimeService
	switch runtime := s.bisectRuntime.(type) {
	case *CommandRuntimeService:
		candidates = []*CommandRuntimeService{runtime}
	case *CommandRuntimeMultiplexer:
		candidates = runtime.adapters
	}
	var selected *CommandRuntimeService
	for _, candidate := range candidates {
		value, err := candidate.projectCommandServiceWithReadiness(ctx, runID, false)
		if err != nil {
			continue
		}
		if selected != nil {
			return nil, apperror.New(apperror.CodeConflict, "Git bisect command adapter is ambiguous")
		}
		selected = value
	}
	if selected == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "selected Git bisect command runtime is unavailable")
	}
	return selected, nil
}

// Source bisect deliberately moves its branch and HEAD. Readiness probes the
// installed backend; the native proof separately checks its executable root.
func (s *CommandRuntimeService) gitBisectReadiness(ctx context.Context, runID string) (time.Time, error) {
	if docker, ok := s.sandbox.(*DockerSandboxCommandRuntimeExecutor); ok {
		value, err := s.loadCommandRuntimeBindings(ctx, runID)
		if err != nil {
			return time.Time{}, err
		}
		owned, found, err := readRunFileDrydock(ctx, s.store, runID)
		if err != nil || !found {
			return time.Time{}, runner.ErrCommandRuntimeBoundary
		}
		bound, err := commandRuntimeDrydockBound(ctx, s.store, owned, runID, value.mission.ID, value.run.SessionID, value.workspace.ID)
		if err != nil || !bound {
			return time.Time{}, runner.ErrCommandRuntimeBoundary
		}
		scope := standardcode.ExecutionContext{RunID: runID, MissionID: value.mission.ID, SessionID: value.run.SessionID, WorkspaceID: value.workspace.ID,
			DrydockID: owned.ID, DrydockWorkspaceID: owned.WorkspaceID, DrydockGeneration: owned.Generation,
			CheckpointID: owned.LastCheckpointID, DrydockBindingSHA256: owned.ExpectedBindingFingerprint,
			ProfileSnapshotID: value.profile.ID, ProfileRevision: value.profile.Revision,
			PermissionSnapshotID: value.permission.ID, PermissionRevision: value.permission.Revision, CapabilityGeneration: s.adapter.Generation}
		manifest, err := standardcode.CompileDockerManifestWithStdin(scope, standardcode.Command{ProtocolVersion: standardcode.CommandProtocolVersion,
			Toolchain: sandbox.DockerStandardCodeToolchainGo, Arguments: []string{"version"}, WorkingDirectory: ".", TimeoutSeconds: 30,
			Purpose: "probe the Git bisect command adapter"}, sandbox.DockerStandardCodeStdinClosed)
		if err != nil {
			return time.Time{}, err
		}
		ready, err := docker.service.docker.StandardCodeReadiness(ctx, manifest)
		if err != nil {
			return time.Time{}, err
		}
		if !ready.Ready {
			return time.Time{}, apperror.New(apperror.CodeFailedPrecondition, "Git bisect Docker backend is not ready: "+ready.ReasonCode)
		}
		return ready.CheckedAt, nil
	}
	if probe, ok := s.sandbox.(commandRuntimeSandboxReadiness); ok {
		ready, err := probe.Ready(ctx, runID)
		if err != nil {
			return time.Time{}, err
		}
		if !ready {
			return time.Time{}, apperror.New(apperror.CodeFailedPrecondition, "Git bisect backend is not ready")
		}
	}
	return time.Now().UTC(), nil
}
