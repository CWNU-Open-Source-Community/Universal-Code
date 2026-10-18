package application

import (
	"context"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
)

type commandRuntimeDockerServiceStore interface {
	GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error)
	GetCommandRuntimeJobAttribution(context.Context, string) (domain.AgentAttribution, error)
	ListCommandRuntimeJobs(context.Context, runner.CommandRuntimeListFilter) ([]runner.CommandRuntimeJob, error)
}

type commandRuntimeDockerServiceBinding struct {
	job     runner.CommandRuntimeJob
	actor   domain.AgentAttribution
	binding sandbox.DockerStandardCodeRunnerBinding
}

func commandRuntimeDockerServiceKey(job runner.CommandRuntimeJob) string {
	return "command-runtime-docker-service-" + runmutation.Fingerprint(
		"command_runtime_docker_service.v1", job.OperationDigest,
		job.RequestFingerprint, job.SpecFingerprint)[:24]
}

// The key binds the immutable Job request, including its original actor and
// spec. It selects a record; the complete stored binding below is still checked.
func commandRuntimeDockerServiceAdmissionDigest(job runner.CommandRuntimeJob) string {
	return runmutation.Fingerprint("docker_sandbox_admission_operation.v1", job.RunID,
		standardCodeStageKey(commandRuntimeDockerServiceKey(job)+"-execute", "admit"))
}

func (e *DockerSandboxCommandRuntimeExecutor) sharedServiceJob(ctx context.Context,
	scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec,
) (runner.CommandRuntimeJob, error) {
	store, ok := e.service.store.(commandRuntimeDockerServiceStore)
	if !ok {
		return runner.CommandRuntimeJob{}, errors.New("Docker shared service Job reader is unavailable")
	}
	digest, id := runner.CommandRuntimeOperationIdentity(scope.RunID, scope.OperationKey)
	job, err := store.GetCommandRuntimeJob(ctx, id)
	if err != nil {
		return runner.CommandRuntimeJob{}, err
	}
	actor, err := store.GetCommandRuntimeJobAttribution(ctx, id)
	if err != nil {
		return runner.CommandRuntimeJob{}, err
	}
	if job.Validate() != nil || job.ID != id || job.OperationDigest != digest ||
		runner.CommandRuntimeJobWorkspaceMode(job) != runner.CommandRuntimeWorkspaceSharedService ||
		job.InvocationID != scope.InvocationID || job.RunID != scope.RunID ||
		job.MissionID != scope.MissionID || job.SessionID != scope.SessionID ||
		job.WorkspaceID != scope.WorkspaceID || job.RootAgentID != scope.RootAgentID ||
		job.WorkspaceRootSHA256 != scope.WorkspaceRootSHA256 ||
		job.SpecFingerprint != runner.CommandRuntimeSpecFingerprint(spec) ||
		!job.Adapter.SameBackend(scope.Adapter) || !job.Adapter.SameBackend(e.identity) ||
		job.ModeSnapshotID != scope.ModeSnapshotID || job.ModeRevision != scope.ModeRevision ||
		job.ProfileSnapshotID != scope.ProfileSnapshotID || job.ProfileRevision != scope.ProfileRevision ||
		job.PermissionSnapshotID != scope.PermissionSnapshotID || job.PermissionRevision != scope.PermissionRevision ||
		job.PermissionMode != scope.PermissionMode || job.PermissionRuntimeEpoch != scope.PermissionRuntimeEpoch ||
		job.PermissionGeneration != scope.PermissionGeneration || job.RunAuthorizationFence != scope.RunAuthorizationFence ||
		job.LeaseID != scope.LeaseID || job.LeaseGeneration != scope.LeaseGeneration || job.LeaseOwnerID != scope.LeaseOwnerID ||
		actor.AgentID != scope.AgentID || actor.AgentAttemptID != scope.AgentAttemptID || actor.Source != scope.AttributionSource ||
		actor.AgentID != job.RootAgentID {
		return runner.CommandRuntimeJob{}, runner.ErrCommandRuntimeBoundary
	}
	return job, nil
}

// A historical Job only identifies which terminal lifecycle must not create a
// new checkpoint. It never restores create/start, stdin or process authority.
func commandRuntimeDockerServiceForAdmission(ctx context.Context, value any,
	admission domain.DockerSandboxAdmission,
) (*commandRuntimeDockerServiceBinding, error) {
	store, ok := value.(commandRuntimeDockerServiceStore)
	if !ok {
		return nil, nil
	}
	jobs, err := store.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: admission.RunID, Limit: 500})
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if commandRuntimeDockerServiceAdmissionDigest(job) != admission.OperationKeyDigest {
			continue
		}
		if admission.Validate() != nil || job.Validate() != nil ||
			runner.CommandRuntimeJobWorkspaceMode(job) != runner.CommandRuntimeWorkspaceSharedService ||
			job.Adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace ||
			job.Adapter.Backend != CommandRuntimeDockerSandboxBackend {
			return nil, runner.ErrCommandRuntimeBoundary
		}
		actor, err := store.GetCommandRuntimeJobAttribution(ctx, job.ID)
		if err != nil {
			return nil, err
		}
		manifest, err := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
		if err != nil {
			return nil, err
		}
		binding, exact := sandbox.ParseDockerStandardCodeManifest(manifest)
		if !exact || actor.Validate() != nil ||
			(actor.Source != domain.AgentAttributionRecorded && actor.Source != domain.AgentAttributionOperatorRoot) ||
			actor.AgentID != job.RootAgentID || actor.AgentID != admission.RequestedBy ||
			job.RunID != admission.RunID || job.MissionID != admission.MissionID || job.WorkspaceID != admission.WorkspaceID ||
			job.RunID != binding.RunID || job.MissionID != binding.MissionID || job.SessionID != binding.SessionID ||
			job.WorkspaceID != binding.WorkspaceID || job.Adapter.Generation != binding.CapabilityGeneration ||
			job.ProfileSnapshotID != binding.ProfileSnapshotID || job.ProfileRevision != binding.ProfileRevision ||
			job.ProfileSnapshotID != admission.ProfileSnapshotID || job.ProfileRevision != admission.ProfileRevision ||
			job.PermissionSnapshotID != binding.PermissionSnapshotID || job.PermissionRevision != binding.PermissionRevision ||
			job.PermissionSnapshotID != admission.PermissionSnapshotID || job.PermissionRevision != admission.PermissionRevision ||
			job.PermissionMode != admission.PermissionMode {
			return nil, runner.ErrCommandRuntimeBoundary
		}
		var intent struct {
			Version          string                             `json:"version"`
			WorkspaceMode    runner.CommandRuntimeWorkspaceMode `json:"workspace_mode"`
			Profile          runner.CommandRuntimeProfile       `json:"profile"`
			ExecutablePath   string                             `json:"executable_path"`
			ExecutableSHA256 string                             `json:"executable_sha256"`
			Arguments        []string                           `json:"argv"`
			WorkingDirectory string                             `json:"working_directory"`
			StdinPolicy      runner.CommandRuntimeStdinPolicy   `json:"stdin_policy"`
			Timeout          int64                              `json:"timeout_milliseconds"`
			Purpose          string                             `json:"purpose"`
		}
		if json.Unmarshal([]byte(job.IntentJSON), &intent) != nil ||
			intent.Version != runner.CommandRuntimeProtocolVersion || intent.WorkspaceMode != runner.CommandRuntimeWorkspaceSharedService ||
			intent.Profile != runner.CommandRuntimeProcess || intent.Profile != job.Profile ||
			intent.ExecutablePath != job.ExecutablePath || intent.ExecutableSHA256 != job.ExecutableSHA256 ||
			intent.WorkingDirectory != job.WorkingDirectory || intent.StdinPolicy != job.StdinPolicy || intent.Timeout != job.TimeoutMilliseconds {
			return nil, runner.ErrCommandRuntimeBoundary
		}
		command, err := commandRuntimeDockerCommand(runner.CommandRuntimeResolvedSpec{
			ExecutablePath: intent.ExecutablePath, CanonicalArgv: intent.Arguments,
			Spec: runner.CommandRuntimeSpec{WorkingDirectory: intent.WorkingDirectory,
				TimeoutMilliseconds: intent.Timeout, Purpose: intent.Purpose},
		})
		if err != nil || !standardCodeCommandMatchesBinding(command, binding) || string(intent.StdinPolicy) != binding.StdinPolicy {
			return nil, runner.ErrCommandRuntimeBoundary
		}
		return &commandRuntimeDockerServiceBinding{job: job, actor: actor, binding: binding}, nil
	}
	if len(jobs) == 500 {
		return nil, errors.New("Docker shared service Job scan reached its safe limit")
	}
	return nil, nil
}

func (b *commandRuntimeDockerServiceBinding) matchesWorkspace(workspace runworktree.Workspace) bool {
	if b == nil || workspace.ID != b.binding.DrydockID || workspace.WorkspaceID != b.binding.DrydockWorkspaceID ||
		workspace.SourceWorkspaceID != b.job.WorkspaceID || workspace.MissionID != b.job.MissionID {
		return false
	}
	rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(workspace.Path)
	return err == nil && rootSHA == b.job.WorkspaceRootSHA256
}

// Reconstruct only the same physical mount for cleanup after ordinary edits.
// The original immutable manifest, plan and container ownership still govern
// cleanup; this path is never consulted by a create/start authority check.
func (s *DockerSandboxService) sharedServiceCleanupWorkspace(ctx context.Context,
	admission domain.DockerSandboxAdmission,
) (sandbox.WorkspaceBinding, bool, error) {
	service, err := commandRuntimeDockerServiceForAdmission(ctx, s.store, admission)
	if err != nil || service == nil {
		return sandbox.WorkspaceBinding{}, false, err
	}
	if s.standardCodeDrydock == nil {
		return sandbox.WorkspaceBinding{}, true, runner.ErrCommandRuntimeBoundary
	}
	workspace, found, err := readRunFileDrydock(ctx, s.standardCodeDrydock.store, admission.RunID)
	if err != nil || !found || !service.matchesWorkspace(workspace) {
		return sandbox.WorkspaceBinding{}, true, errors.Join(err, runner.ErrCommandRuntimeBoundary)
	}
	verified, _, _, err := s.standardCodeDrydock.loadExactDrydock(ctx, admission.RunID, workspace.Generation, false)
	if err != nil || !service.matchesWorkspace(verified) {
		return sandbox.WorkspaceBinding{}, true, errors.Join(err, runner.ErrCommandRuntimeBoundary)
	}
	return sandbox.WorkspaceBinding{ID: admission.WorkspaceID, RootPath: verified.Path}, true, nil
}
