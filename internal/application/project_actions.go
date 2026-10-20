package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
)

const ProjectActionsProtocolVersion = "project-actions.v1"

type ProjectActionsStore interface {
	RunFileWorkspaceStore
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error)
}

type ProjectActionRuntime interface {
	ThreadApplicationServiceCommandRuntime
	projectCommandService(context.Context, string) (*CommandRuntimeService, error)
}

type ProjectActionView struct {
	Slot       string `json:"slot"`
	CommandID  string `json:"command_id"`
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	TargetKind string `json:"target_kind"`
	Reason     string `json:"reason"`
}

type ProjectActionsView struct {
	ProtocolVersion string              `json:"protocol_version"`
	RunID           string              `json:"run_id"`
	Target          string              `json:"target"`
	Backend         string              `json:"backend"`
	Actions         []ProjectActionView `json:"actions"`
	CapabilityGrant bool                `json:"capability_grant"`
}

type ProjectActionJobView struct {
	ID              string                        `json:"id"`
	State           runner.CommandRuntimeJobState `json:"state"`
	ExitCode        *int                          `json:"exit_code,omitempty"`
	Stdout          string                        `json:"stdout"`
	Stderr          string                        `json:"stderr"`
	StdoutTruncated bool                          `json:"stdout_truncated"`
	StderrTruncated bool                          `json:"stderr_truncated"`
	CanStop         bool                          `json:"can_stop"`
}

type ProjectActionResultView struct {
	ProtocolVersion string               `json:"protocol_version"`
	RunID           string               `json:"run_id"`
	Slot            string               `json:"slot"`
	CommandID       string               `json:"command_id"`
	Backend         string               `json:"backend"`
	Replayed        bool                 `json:"replayed"`
	Job             ProjectActionJobView `json:"job"`
}

type ProjectActionRequest struct {
	RunID, Slot, Target, OperationKey, RequestedBy string
	ConfirmExecution                               bool
}

type projectActionPending struct {
	ready   chan struct{}
	purpose string
	result  ProjectActionResultView
	err     error
}

type ProjectActionsService struct {
	store    ProjectActionsStore
	runtime  ProjectActionRuntime
	drydocks *RunWorktreeService
	mu       sync.Mutex
	pending  map[string]*projectActionPending
}

func NewProjectActionsService(store ProjectActionsStore, runtime any, drydocks *RunWorktreeService) *ProjectActionsService {
	commands, _ := runtime.(ProjectActionRuntime)
	return &ProjectActionsService{store: store, runtime: commands, drydocks: drydocks, pending: make(map[string]*projectActionPending)}
}

// The selection is a read of current preferences/readiness. It creates no
// lease or permission grant, and never substitutes Host for an owned sandbox.
func (s *CommandRuntimeService) projectCommandService(ctx context.Context, runID string) (*CommandRuntimeService, error) {
	return s.projectCommandServiceWithReadiness(ctx, runID, true)
}

func (s *CommandRuntimeService) projectCommandServiceWithReadiness(ctx context.Context, runID string, probeReadiness bool) (*CommandRuntimeService, error) {
	if s == nil || s.store == nil || !s.adapter.Executable() || !s.commandRuntimeAdapterCurrent() {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "command runtime is unavailable")
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != domain.RunRunning && run.Status != domain.RunPaused && run.Status != domain.RunCreated {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Run is not executable")
	}
	mode, err := s.store.GetRunMode(ctx, runID)
	if err != nil {
		return nil, err
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, runID)
	if err != nil {
		return nil, err
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, runID)
	if err != nil {
		return nil, err
	}
	if mode.Surface != domain.ExecutionSurfaceCode || mode.Phase != domain.ExecutionPhaseDeliver {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "project actions require Code Deliver phase")
	}
	if profile.Profile != commandRuntimeExecutionProfile(s.adapter) {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "command adapter does not match the selected execution profile")
	}
	if !permission.Mode.IsApprovalMode() || !s.adapter.AllowsPermission(permission.Mode) || !s.capabilities.AllowsSnapshot(permission) {
		return nil, apperror.New(apperror.CodePolicyDenied, "current Run permission is unavailable for project actions")
	}
	owned, found, err := readRunFileDrydock(ctx, s.store, runID)
	if err != nil {
		return nil, err
	}
	if s.adapter.Kind == commandruntimeadapter.KindHostUnsandboxed && found {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "owned workspace requires its sandbox command adapter")
	}
	if s.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		if !found || owned.Path == "" {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "sandbox workspace is unavailable")
		}
		if probe, ok := s.sandbox.(commandRuntimeSandboxReadiness); ok && probeReadiness {
			ready, err := probe.Ready(ctx, runID)
			if err != nil {
				return nil, err
			}
			if !ready {
				return nil, apperror.New(apperror.CodeFailedPrecondition, "selected sandbox command runtime is not ready")
			}
		}
	}
	return s, nil
}

func (m *CommandRuntimeMultiplexer) projectCommandService(ctx context.Context, runID string) (*CommandRuntimeService, error) {
	var selected *CommandRuntimeService
	for _, service := range m.adapters {
		value, err := service.projectCommandService(ctx, runID)
		if err != nil {
			continue
		}
		if selected != nil {
			return nil, apperror.New(apperror.CodeConflict, "project command adapter selection is ambiguous")
		}
		selected = value
	}
	if selected == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "selected project command runtime is unavailable")
	}
	return selected, nil
}

func (s *CommandRuntimeService) projectPresetReady(ctx context.Context, id string) error {
	if id == "test.yarn" {
		return s.projectPreparationReady(ctx, "yarn")
	}
	if id == "test.pnpm" {
		return s.projectPreparationReady(ctx, "pnpm")
	}
	if s.adapter.Backend != CommandRuntimeDockerSandboxBackend || (id != "test.npm" && id != "format.go") {
		return nil
	}
	executor, ok := s.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Docker project tools are unavailable")
	}
	probe, ok := executor.service.docker.readiness.(interface {
		ProjectActionsAvailable(context.Context, string) (bool, error)
	})
	if ok {
		ready, err := probe.ProjectActionsAvailable(ctx, executor.service.imageDigest)
		if err != nil {
			return apperror.New(apperror.CodeUnavailable, "Docker project tool inspection failed")
		}
		if ready {
			return nil
		}
	}
	return apperror.New(apperror.CodeFailedPrecondition, "rebuild the Standard Code Docker image to enable npm tests and gofmt")
}

func (s *ProjectActionsService) List(ctx context.Context, runID string, targets ...string) (ProjectActionsView, error) {
	target := "."
	if len(targets) > 1 {
		return ProjectActionsView{}, apperror.New(apperror.CodeInvalidArgument, "project actions accept one target directory")
	}
	if len(targets) == 1 && targets[0] != "" {
		target = targets[0]
	}
	view := ProjectActionsView{ProtocolVersion: ProjectActionsProtocolVersion, RunID: runID, Target: target, Actions: []ProjectActionView{}}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return view, err
	}
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return view, err
	}
	policy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return view, err
	}
	if err := policy.Check(target); err != nil {
		return view, err
	}
	var service *CommandRuntimeService
	if s.runtime != nil {
		service, err = s.runtime.projectCommandService(ctx, runID)
	} else {
		err = errors.New("command runtime is unavailable")
	}
	reason := ""
	if err != nil {
		reason = redact.String(err.Error())
	}
	root := ""
	if service != nil {
		view.Backend = service.adapter.Backend
	}
	mission, readErr := s.store.GetMission(ctx, run.MissionID)
	if readErr != nil {
		return view, readErr
	}
	files, resolveErr := ResolveRunFileWorkspace(ctx, s.store, run, mission, s.drydocks)
	if resolveErr != nil {
		reason = "Run workspace is unavailable"
		service = nil
	} else {
		root = files.Workspace.RootPath
		if service != nil {
			if lease, found, leaseErr := service.store.GetRunExecutionLease(ctx, run.ID); leaseErr != nil {
				return view, leaseErr
			} else if found && lease.ActiveAt(time.Now().UTC()) {
				reason = "Run is executing another operation"
				service = nil
			}
		}
	}
	for _, slot := range []string{"test", "format"} {
		action := ProjectActionView{Slot: slot, TargetKind: "directory", Reason: reason}
		var detected *projectprep.Plan
		if slot == "format" {
			action.TargetKind = "go_file"
		}
		if project != nil {
			if slot == "test" {
				action.CommandID = project.TestCommandID
			} else {
				action.CommandID = project.FormatCommandID
			}
		}
		action.Configured = action.CommandID != ""
		if slot == "test" && action.CommandID == "" && root != "" {
			plan, detectErr := projectprep.Detect(ctx, root, target)
			if detectErr == nil {
				detectErr = checkDetectedProjectPaths(run, plan)
			}
			if detectErr == nil && plan.Test.ID != "" {
				action.CommandID, detected = plan.Test.ID, &plan
			}
		}
		if action.CommandID == "" {
			if action.Reason == "" {
				action.Reason = "No project action is configured or detected"
			}
		} else if service != nil {
			_, _, prepareErr := resolveProjectCommandToolchain(action.CommandID, service.adapter, root)
			if prepareErr == nil && detected != nil {
				_, prepareErr = PrepareProjectPreparationTestCommand(*detected, service.adapter, root)
			}
			if prepareErr == nil {
				prepareErr = service.projectPresetReady(ctx, action.CommandID)
			}
			if (slot == "format") != (action.CommandID == "format.go") {
				prepareErr = errors.New("configured command preset does not match its project action")
			}
			if slot == "format" && project != nil && project.ReadOnly {
				prepareErr = errors.New("read-only project cannot be formatted")
			}
			if prepareErr != nil {
				action.Reason = redact.String(prepareErr.Error())
			} else {
				action.Available = true
				action.Reason = ""
			}
		}
		view.Actions = append(view.Actions, action)
	}
	return view, nil
}

func projectActionCommandIdentity(run domain.Run, request ProjectActionRequest) (string, string, string, error) {
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return "", "", "", err
	}
	id := ""
	if project != nil {
		id = project.TestCommandID
		if request.Slot == "format" {
			id = project.FormatCommandID
		}
	}
	if id == "" && request.Slot != "test" {
		return "", "", "", apperror.New(apperror.CodeFailedPrecondition, "project action is not configured")
	}
	key := "project-action-" + runmutation.Fingerprint("project_action_operation", request.RunID, request.OperationKey)
	// Stored IntentJSON retains this immutable task/action binding, so retries
	// can read the original Job even after a toolchain or adapter disappears.
	purpose := "Configured project action " + id + " " + projectActionRequestFingerprint(run, request)
	return id, key, purpose, nil
}

func projectActionRequestFingerprint(run domain.Run, request ProjectActionRequest) string {
	return runmutation.Fingerprint("project_action_request", run.ID, run.Config.ProjectConfigFingerprint, request.Slot, request.Target, request.RequestedBy)
}

func detectedProjectActionPurpose(purpose, requestFingerprint string) (string, bool) {
	parts := strings.Fields(purpose)
	if len(parts) != 6 || strings.Join(parts[:3], " ") != "Detected project action" ||
		parts[4] != requestFingerprint || len(parts[5]) != 64 || (parts[3] != "test.go" && parts[3] != "test.npm" && parts[3] != "test.pnpm" && parts[3] != "test.yarn" && parts[3] != "test.rust") {
		return "", false
	}
	return parts[3], true
}

func operatorProjectJobID(runID, key string) string {
	operation := "operator-command-" + runmutation.Fingerprint("operator_command_key.v1", runID, key)
	_, jobID := runner.CommandRuntimeOperationIdentity(runID, commandRuntimeBatchOperationKey(operation, 0))
	return jobID
}

func (s *ProjectActionsService) Run(ctx context.Context, request ProjectActionRequest) (ProjectActionResultView, error) {
	if !domain.ValidAgentID(request.RunID) || !domain.ValidAgentID(request.RequestedBy) || (request.Slot != "test" && request.Slot != "format") || !request.ConfirmExecution || request.Target != strings.TrimSpace(request.Target) {
		return ProjectActionResultView{}, apperror.New(apperror.CodeInvalidArgument, "a confirmed project action with normalized target is required")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey {
		return ProjectActionResultView{}, apperror.New(apperror.CodeInvalidArgument, "project action operation key is invalid")
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return ProjectActionResultView{}, err
	}
	id, operationKey, purpose, err := projectActionCommandIdentity(run, request)
	if err != nil {
		return ProjectActionResultView{}, err
	}
	jobID := operatorProjectJobID(run.ID, operationKey)
	if job, readErr := s.store.GetCommandRuntimeJob(ctx, jobID); readErr == nil {
		var intent runner.CommandRuntimeSpec
		valid := job.Validate() == nil && json.Unmarshal([]byte(job.IntentJSON), &intent) == nil && job.RunID == run.ID
		if valid && id == "" {
			id, valid = detectedProjectActionPurpose(intent.Purpose, projectActionRequestFingerprint(run, request))
		} else {
			valid = valid && intent.Purpose == purpose
		}
		if !valid {
			return ProjectActionResultView{}, apperror.New(apperror.CodeConflict, "project action key was already used for another action or target")
		}
		return s.projectResult(ctx, request.Slot, id, job, true), nil
	} else if !errors.Is(readErr, runner.ErrCommandRuntimeJobNotFound) && apperror.CodeOf(apperror.Normalize(readErr)) != apperror.CodeNotFound {
		return ProjectActionResultView{}, readErr
	}
	if s.runtime == nil {
		return ProjectActionResultView{}, apperror.New(apperror.CodeFailedPrecondition, "project command runtime is unavailable")
	}
	service, err := s.runtime.projectCommandService(ctx, run.ID)
	if err != nil {
		return ProjectActionResultView{}, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return ProjectActionResultView{}, err
	}
	files, err := ResolveRunFileWorkspace(ctx, s.store, run, mission, s.drydocks)
	if err != nil {
		return ProjectActionResultView{}, err
	}
	var detectedPlan *projectprep.Plan
	if id == "" {
		policy, err := projectconfig.PathPolicy(run.Config)
		if err != nil {
			return ProjectActionResultView{}, err
		}
		if err := policy.Check(request.Target); err != nil {
			return ProjectActionResultView{}, err
		}
		plan, err := projectprep.Detect(ctx, files.Workspace.RootPath, request.Target)
		if err != nil {
			return ProjectActionResultView{}, err
		}
		if err := checkDetectedProjectPaths(run, plan); err != nil {
			return ProjectActionResultView{}, err
		}
		if plan.Test.ID == "" {
			return ProjectActionResultView{}, apperror.New(apperror.CodeFailedPrecondition, "no project test was detected in the selected directory")
		}
		id = plan.Test.ID
		detectedPlan = &plan
		purpose = "Detected project action " + id + " " + projectActionRequestFingerprint(run, request) + " " + plan.Fingerprint
	}
	if err := service.projectPresetReady(ctx, id); err != nil {
		return ProjectActionResultView{}, err
	}
	var spec runner.CommandRuntimeSpec
	if detectedPlan != nil {
		spec, err = PrepareProjectPreparationTestCommand(*detectedPlan, service.adapter, files.Workspace.RootPath)
	} else {
		spec, err = PrepareProjectCommandForAdapter(run, request.Slot, request.Target, service.adapter, files.Workspace.RootPath)
	}
	if err != nil {
		return ProjectActionResultView{}, err
	}
	spec.Purpose = purpose
	if err = ctx.Err(); err != nil {
		return ProjectActionResultView{}, err
	}
	s.mu.Lock()
	pending := s.pending[jobID]
	if pending != nil && pending.purpose != purpose {
		s.mu.Unlock()
		return ProjectActionResultView{}, apperror.New(apperror.CodeConflict, "project action key was already used for another action or target")
	}
	if pending == nil {
		pending = &projectActionPending{ready: make(chan struct{}), purpose: purpose}
		s.pending[jobID] = pending
		owned := context.WithoutCancel(ctx)
		go func() {
			var once sync.Once
			publish := func(value OperatorCommandResult, err error) {
				once.Do(func() {
					if value.Job.ID != "" {
						pending.result = s.projectResult(owned, request.Slot, id, value.Job, value.Replayed)
						err = nil
					}
					pending.err = err
					close(pending.ready)
					s.mu.Lock()
					delete(s.pending, jobID)
					s.mu.Unlock()
				})
			}
			var before func(context.Context, string) error
			if detectedPlan != nil {
				before = func(ctx context.Context, root string) error {
					current, err := projectprep.Detect(ctx, root, request.Target)
					if err != nil {
						return err
					}
					if current.Fingerprint != detectedPlan.Fingerprint {
						return apperror.New(apperror.CodeConflict, "detected project sources changed before test execution; inspect the project again")
					}
					return checkDetectedProjectPaths(run, current)
				}
			}
			value, executeErr := service.runPreparedOperatorCommand(owned, OperatorCommandRequest{RunID: run.ID, OperationKey: operationKey, RequestedBy: request.RequestedBy, Command: spec, ConfirmExecution: true}, func(value OperatorCommandResult) { publish(value, nil) }, before, nil)
			publish(value, executeErr)
		}()
	}
	s.mu.Unlock()
	select {
	case <-pending.ready:
		return pending.result, pending.err
	case <-ctx.Done():
		return ProjectActionResultView{}, ctx.Err()
	}
}

func (s *ProjectActionsService) GetJob(ctx context.Context, runID, jobID string) (ProjectActionResultView, error) {
	job, err := s.store.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		return ProjectActionResultView{}, err
	}
	var intent runner.CommandRuntimeSpec
	if job.Validate() != nil || job.RunID != runID || !strings.HasPrefix(job.InvocationID, "operator-command-") || json.Unmarshal([]byte(job.IntentJSON), &intent) != nil {
		return ProjectActionResultView{}, apperror.New(apperror.CodeNotFound, "project action Job was not found")
	}
	parts := strings.Fields(intent.Purpose)
	configured := len(parts) == 5 && strings.Join(parts[:3], " ") == "Configured project action" && len(parts[4]) == 64
	detected := len(parts) == 6 && strings.Join(parts[:3], " ") == "Detected project action" && len(parts[4]) == 64 && len(parts[5]) == 64 && (parts[3] == "test.go" || parts[3] == "test.npm" || parts[3] == "test.pnpm" || parts[3] == "test.yarn" || parts[3] == "test.rust")
	if !configured && !detected {
		return ProjectActionResultView{}, apperror.New(apperror.CodeNotFound, "Job is not a configured project action")
	}
	slot := "test"
	if parts[3] == "format.go" {
		slot = "format"
	}
	return s.projectResult(ctx, slot, parts[3], job, true), nil
}

func (s *ProjectActionsService) projectResult(ctx context.Context, slot, id string, job runner.CommandRuntimeJob, replayed bool) ProjectActionResultView {
	value := ProjectActionJobView{ID: job.ID, State: job.State, ExitCode: job.ExitCode, Stdout: redact.String(job.Stdout), Stderr: redact.String(job.Stderr), StdoutTruncated: job.StdoutObservedBytes > int64(len(job.Stdout)), StderrTruncated: job.StderrObservedBytes > int64(len(job.Stderr))}
	// Operator Jobs are observed/stopped by the same owner bridge as Thread
	// services. Source-message attribution is optional for operator invocation.
	if s.runtime != nil && !job.State.Terminal() {
		snapshot, page, owned, err := s.runtime.ReadCommandRuntimeActivityTail(ctx, job, runner.MaxCommandRuntimeOutputRead)
		if err == nil && owned {
			value.State, value.ExitCode = snapshot.State, snapshot.ExitCode
			output := projectThreadServiceOutput(page, "", nil)
			value.Stdout, value.Stderr = output.Stdout, output.Stderr
			value.StdoutTruncated = output.Dropped || snapshot.StdoutObservedBytes > int64(len(output.Stdout))
			value.StderrTruncated = output.Dropped || snapshot.StderrObservedBytes > int64(len(output.Stderr))
			value.CanStop = !snapshot.State.Terminal() && snapshot.State != runner.CommandRuntimeJobStopping
		}
	}
	// Local paths are not part of the public receipt.
	if mission, err := s.store.GetMission(ctx, job.MissionID); err == nil {
		if files, err := resolveRunFileWorkspaceControl(ctx, s.store, domain.Run{ID: job.RunID, MissionID: job.MissionID, SessionID: job.SessionID}, mission, s.drydocks); err == nil {
			for _, root := range []string{files.Source.RootPath, files.Workspace.RootPath} {
				if root != "" {
					value.Stdout = strings.ReplaceAll(value.Stdout, root, "[workspace]")
					value.Stderr = strings.ReplaceAll(value.Stderr, root, "[workspace]")
				}
			}
		}
	}
	return ProjectActionResultView{ProtocolVersion: ProjectActionsProtocolVersion, RunID: job.RunID, Slot: slot, CommandID: id, Backend: job.Adapter.Backend, Replayed: replayed, Job: value}
}
