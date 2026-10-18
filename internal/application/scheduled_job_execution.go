package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
)

type scheduledJobThreadExecutor interface {
	ExecuteAutomatic(context.Context, ExecuteThreadTurnRequest) (ExecuteThreadTurnResult, error)
	CheckAutomaticRun(context.Context, string, string) error
	ObserveAutomaticTurn(context.Context, string, string, string) (AutomaticThreadTurnObservation, error)
}
type scheduledJobWorkflowExecutor interface {
	Start(context.Context, StartWorkflowRequest) (domain.WorkflowExecution, error)
	Get(context.Context, string) (domain.WorkflowExecution, error)
	Definition(context.Context, string) (domain.WorkflowDefinition, error)
}
type scheduledJobExecutionStore interface {
	CreateScheduledJobWithExecutionConsent(context.Context, domain.ScheduledJob, domain.ScheduledJobExecutionConsent, domain.ScheduledJobOperation) (domain.ScheduledJob, bool, error)
	GetScheduledJobExecutionConsent(context.Context, string) (domain.ScheduledJobExecutionConsent, error)
	GetScheduledJobExecution(context.Context, string, time.Time) (domain.ScheduledJobExecution, bool, error)
	ListScheduledJobExecutions(context.Context, string, int) ([]domain.ScheduledJobExecution, error)
	SaveScheduledJobExecution(context.Context, domain.ScheduledJobLease, domain.ScheduledJobExecution, time.Time) (domain.ScheduledJobExecution, error)
	RenewScheduledJobLease(context.Context, domain.ScheduledJobLease, time.Time) (bool, error)
	YieldScheduledJobExecution(context.Context, domain.ScheduledJobLease, string, time.Time) error
	ClaimDueConfirmedScheduledJob(context.Context, string, time.Time) (domain.ScheduledJob, domain.ScheduledJobLease, bool, error)
	ReconcileConfirmedScheduledJobs(context.Context, time.Time, int) (int, error)
}
type scheduledTargetStore interface {
	RunFileWorkspaceStore
	GetThread(context.Context, string) (domain.Thread, error)
	GetThreadByRun(context.Context, string) (domain.Thread, error)
	GetMission(context.Context, string) (domain.Mission, error)
	ListThreadRuns(context.Context, string) ([]domain.ThreadRun, error)
}
type scheduledExecutionBinding struct{ WorkspaceID, RootPathSHA256, Branch, ThreadID, RunID string }

func (s *ScheduledJobService) WithExecution(turns scheduledJobThreadExecutor, workflows scheduledJobWorkflowExecutor) *ScheduledJobService {
	if s != nil {
		s.turns = turns
		s.workflows = workflows
		if concrete, ok := turns.(*ThreadTurnService); ok && concrete != nil && concrete.threads != nil {
			s.drydocks = concrete.threads.drydocks
		}
	}
	return s
}

// RunDueConfirmed shares a single oldest-due selection between observation and
// execution consent, so a large observation queue cannot starve executions.
func (s *ScheduledJobService) RunDueConfirmed(ctx context.Context, owner string, now time.Time) (bool, error) {
	return s.runDue(ctx, owner, now, 2)
}

func (s *ScheduledJobService) executionBinding(ctx context.Context, spec domain.ScheduledJobSpec) (scheduledExecutionBinding, error) {
	var b scheduledExecutionBinding
	st, ok := s.store.(scheduledTargetStore)
	if !ok {
		return b, apperror.New(apperror.CodeFailedPrecondition, "scheduled execution workspace reader is required")
	}
	target := spec.ExecutionTarget
	if target == nil {
		return b, apperror.New(apperror.CodeInvalidArgument, "automatic execution target is required")
	}
	var thread domain.Thread
	var err error
	if target.Kind == "thread" {
		thread, err = st.GetThread(ctx, target.ThreadID)
	} else {
		thread, err = st.GetThreadByRun(ctx, spec.TargetRunID)
	}
	if err != nil {
		return b, apperror.Normalize(err)
	}
	if thread.Status != domain.ThreadActive {
		return b, apperror.New(apperror.CodeFailedPrecondition, "automatic execution requires an active task")
	}
	if target.Kind == "workflow" && thread.ActiveRunID != spec.TargetRunID {
		return b, apperror.New(apperror.CodeFailedPrecondition, "scheduled workflow requires its selected Run to remain the task's active Run")
	}
	bindings, err := st.ListThreadRuns(ctx, thread.ID)
	if err != nil {
		return b, err
	}
	owns := false
	for _, v := range bindings {
		if v.RunID == spec.TargetRunID {
			owns = true
			break
		}
	}
	if !owns {
		return b, apperror.New(apperror.CodeConflict, "scheduled target Run does not belong to the selected task")
	}
	runID := spec.TargetRunID
	if target.Kind == "thread" {
		runID = thread.ActiveRunID
		if runID == "" {
			runID = thread.LastRunID
		}
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return b, err
	}
	mission, err := st.GetMission(ctx, run.MissionID)
	if err != nil {
		return b, err
	}
	files, err := ResolveRunFileWorkspace(ctx, st, run, mission, s.drydocks)
	if err != nil {
		return b, err
	}
	if files.Drydock != nil {
		if err := requireCurrentRunFileDrydock(ctx, st, run.ID, *files.Drydock); err != nil {
			return b, err
		}
	}
	root, err := filepath.EvalSymlinks(files.Workspace.RootPath)
	if err != nil {
		return b, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return b, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return b, err
	}
	if !info.IsDir() {
		return b, apperror.New(apperror.CodeFailedPrecondition, "scheduled workspace is not a directory")
	}
	b = scheduledExecutionBinding{WorkspaceID: files.Workspace.ID, RootPathSHA256: runmutation.Fingerprint("scheduled_execution_workspace.v1", filepath.Clean(root)), ThreadID: thread.ID, RunID: run.ID}
	if _, err = os.Lstat(filepath.Join(root, ".git")); err == nil {
		b.Branch, err = repository.CurrentBranch(ctx, root)
		if err != nil {
			return b, apperror.Normalize(err)
		}
	} else if !os.IsNotExist(err) {
		return b, err
	}
	return b, nil
}

func (s *ScheduledJobService) validateExecutionTarget(ctx context.Context, spec domain.ScheduledJobSpec) (scheduledExecutionBinding, error) {
	b, err := s.executionBinding(ctx, spec)
	if err != nil {
		return b, err
	}
	if s.turns == nil {
		return b, apperror.New(apperror.CodeFailedPrecondition, "automatic task execution service is unavailable")
	}
	if spec.ExecutionTarget.Kind == "workflow" {
		if s.workflows == nil {
			return b, apperror.New(apperror.CodeFailedPrecondition, "automatic workflow execution service is unavailable")
		}
		if _, err := s.workflows.Definition(ctx, spec.ExecutionTarget.WorkflowDefinitionID); err != nil {
			return b, err
		}
		run, err := s.store.GetRun(ctx, spec.TargetRunID)
		if err != nil {
			return b, err
		}
		if run.Status != domain.RunRunning {
			return b, apperror.New(apperror.CodeFailedPrecondition, "scheduled workflow requires its selected Run to remain running")
		}
	}
	return b, nil
}

func (s *ScheduledJobService) runExecution(ctx context.Context, job domain.ScheduledJob, lease domain.ScheduledJobLease) error {
	st, ok := s.store.(scheduledJobExecutionStore)
	if !ok {
		return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, apperror.New(apperror.CodeFailedPrecondition, "scheduled execution store is required"))
	}
	deadline := job.Spec.DeadlineAt
	elapsed := job.CreatedAt.Add(time.Duration(job.Spec.MaxElapsedSeconds) * time.Second)
	if elapsed.Before(deadline) {
		deadline = elapsed
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	s.executionMu.Lock()
	if s.activeExecutions == nil {
		s.activeExecutions = map[string]context.CancelFunc{}
	}
	s.activeExecutions[job.ID] = cancel
	s.executionMu.Unlock()
	defer func() { s.executionMu.Lock(); delete(s.activeExecutions, job.ID); s.executionMu.Unlock() }()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				renewed, err := st.RenewScheduledJobLease(runCtx, lease, s.clock.Now().UTC())
				if err != nil || !renewed {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-stopped }()
	consent, err := st.GetScheduledJobExecutionConsent(runCtx, job.ID)
	if err != nil {
		return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, err)
	}
	receipt, found, err := st.GetScheduledJobExecution(runCtx, job.ID, lease.OccurrenceAt)
	if err != nil {
		return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, err)
	}
	if !found {
		now := s.clock.Now().UTC()
		receipt = domain.ScheduledJobExecution{JobID: job.ID, OccurrenceAt: lease.OccurrenceAt, OperationKey: lease.OperationKey, State: "queued", CreatedAt: now, UpdatedAt: now}
		if _, err = st.SaveScheduledJobExecution(runCtx, lease, receipt, now); err != nil {
			return err
		}
	}
	save := func() error {
		receipt.UpdatedAt = s.clock.Now().UTC()
		_, err := st.SaveScheduledJobExecution(runCtx, lease, receipt, receipt.UpdatedAt)
		return err
	}
	wait := func(state, summary string) error {
		receipt.State = state
		receipt.Result = summary
		if err := save(); err != nil {
			return err
		}
		return st.YieldScheduledJobExecution(runCtx, lease, summary, s.clock.Now().UTC())
	}
	fail := func(cause error) error {
		receipt.State = "failed"
		receipt.ErrorCode = scheduledJobFailureCode(cause)
		receipt.Result = "Scheduled execution failed (" + receipt.ErrorCode + ")"
		if err := save(); err != nil {
			return err
		}
		return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, cause)
	}
	target := job.Spec.ExecutionTarget
	if receipt.State == "failed" {
		return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, apperror.New(apperror.CodeFailedPrecondition, "original scheduled execution failed"))
	}
	if target.Kind == "thread" && receipt.MessageID == "" && s.turns != nil {
		// A response can be lost after the Thread's own journal has committed.
		// Recover an approval/completed input before considering a new dispatch.
		if reader, ok := s.store.(interface {
			InspectOperatorSteeringOperation(context.Context, string, string) (domain.OperatorSteeringMessage, bool, error)
		}); ok {
			if targets, ok := s.store.(scheduledTargetStore); ok {
				bindings, err := targets.ListThreadRuns(runCtx, target.ThreadID)
				if err != nil {
					return err
				}
				for _, binding := range bindings {
					message, found, err := reader.InspectOperatorSteeringOperation(runCtx, binding.SessionID, receipt.OperationKey)
					if err != nil {
						return err
					}
					if !found {
						continue
					}
					observed, err := s.turns.ObserveAutomaticTurn(runCtx, target.ThreadID, binding.RunID, message.ID)
					if err != nil {
						return err
					}
					if message.Status != domain.OperatorSteeringPending || observed.State == "waiting_approval" || observed.TerminalFailure {
						receipt.RunID = binding.RunID
						receipt.MessageID = message.ID
						receipt.State = "running"
						if err := save(); err != nil {
							return err
						}
					}
					break
				}
			}
		}
	}
	// Workflow.Start derives this exact durable ID from the operation key. A
	// crash after Start but before saving the returned receipt is reconciled
	// before admission checks for a fresh execution.
	if target.Kind == "workflow" && receipt.WorkflowExecutionID == "" && s.workflows != nil {
		id := "workflowrun-" + runmutation.Fingerprint("workflow_start.v1", job.Spec.TargetRunID, receipt.OperationKey)
		if execution, lookupErr := s.workflows.Get(runCtx, id); lookupErr == nil {
			receipt.RunID = execution.RunID
			receipt.WorkflowExecutionID = execution.ID
			receipt.State = "running"
			if err := save(); err != nil {
				return err
			}
		} else if apperror.CodeOf(apperror.Normalize(lookupErr)) != apperror.CodeNotFound {
			return lookupErr
		}
	}
	if receipt.ResumeSubmission && s.turns != nil {
		observed, err := s.turns.ObserveAutomaticTurn(runCtx, target.ThreadID, receipt.RunID, receipt.MessageID)
		if err != nil {
			return err
		}
		if observed.TerminalFailure {
			return fail(apperror.New(apperror.CodeFailedPrecondition, "original scheduled task execution failed"))
		}
		if observed.State == "waiting_approval" {
			receipt.ResumeSubmission = false
			receipt.ErrorCode = ""
			return wait("waiting_approval", scheduledExecutionSummary("waiting_approval"))
		}
		if observed.State == "completed" {
			receipt.ResumeSubmission = false
			receipt.ErrorCode = ""
		}
	}
	if receipt.MessageID == "" && receipt.WorkflowExecutionID == "" || receipt.ResumeSubmission {
		binding, err := s.validateExecutionTarget(runCtx, job.Spec)
		if err != nil {
			return wait("blocked", "Automatic execution requires the selected task and workspace to be available")
		}
		if binding.WorkspaceID != consent.WorkspaceID || binding.RootPathSHA256 != consent.RootPathSHA256 || binding.Branch != consent.Branch {
			return wait("blocked", "Task workspace or branch changed; restore the selected workspace before continuing")
		}
		if receipt.ResumeSubmission && binding.RunID != receipt.RunID {
			return wait("blocked", "The original pending input requires its task Run to remain current")
		}
		if target.Kind == "workflow" {
			if err := s.turns.CheckAutomaticRun(runCtx, binding.ThreadID, job.Spec.TargetRunID); err != nil {
				return wait("blocked", scheduledBlockedSummary(err))
			}
		}
		// Recheck the schedule's fenced lease after target admission and directly
		// before dispatch. A concurrent pause/cancel prevents a new submission.
		if renewed, err := st.RenewScheduledJobLease(runCtx, lease, s.clock.Now().UTC()); err != nil || !renewed {
			if err == nil {
				err = apperror.New(apperror.CodeConflict, "scheduled execution lease was revoked")
			}
			return err
		}
		if target.Kind == "thread" {
			result, execErr := s.turns.ExecuteAutomatic(runCtx, ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: target.ThreadID, Content: target.Prompt, OperationKey: receipt.OperationKey, RequestedBy: "scheduled_job"})
			if result.Submission.Message.ID != "" {
				receipt.RunID = result.Submission.Run.ID
				receipt.MessageID = result.Submission.Message.ID
				receipt.ResumeSubmission = execErr != nil && result.Execution == nil && result.Submission.Message.Status == domain.OperatorSteeringPending
			}
			if receipt.MessageID != "" {
				receipt.State = "running"
				receipt.ErrorCode = ""
				if execErr != nil {
					receipt.State = "blocked"
					receipt.ErrorCode = scheduledJobFailureCode(execErr)
					receipt.Result = scheduledBlockedSummary(execErr)
				}
				if err := save(); err != nil {
					return err
				}
			}
			if execErr != nil && receipt.MessageID == "" {
				code := apperror.CodeOf(apperror.Normalize(execErr))
				if code == apperror.CodeFailedPrecondition || code == apperror.CodeResourceExhausted || code == apperror.CodePolicyDenied || code == apperror.CodeConflict {
					return wait("blocked", scheduledBlockedSummary(execErr))
				}
				return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, execErr)
			}
		} else {
			execution, execErr := s.workflows.Start(runCtx, StartWorkflowRequest{RunID: job.Spec.TargetRunID, DefinitionID: target.WorkflowDefinitionID, Inputs: target.Inputs, OperationKey: receipt.OperationKey})
			if execErr != nil {
				code := apperror.CodeOf(apperror.Normalize(execErr))
				if code == apperror.CodeFailedPrecondition || code == apperror.CodeResourceExhausted || code == apperror.CodePolicyDenied || code == apperror.CodeConflict {
					return wait("blocked", scheduledBlockedSummary(execErr))
				}
				return s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, execErr)
			}
			receipt.RunID = execution.RunID
			receipt.WorkflowExecutionID = execution.ID
			receipt.State = "running"
			if err := save(); err != nil {
				return err
			}
		}
	}
	if target.Kind == "thread" {
		if s.turns == nil {
			return wait("blocked", "Task execution service is unavailable")
		}
		observed, err := s.turns.ObserveAutomaticTurn(runCtx, target.ThreadID, receipt.RunID, receipt.MessageID)
		if err != nil {
			return err
		}
		if observed.TerminalFailure {
			return fail(apperror.New(apperror.CodeFailedPrecondition, "original scheduled task execution failed"))
		}
		if observed.State != "completed" {
			if receipt.State == "blocked" && receipt.ErrorCode != "" && observed.State != "waiting_approval" {
				return wait("blocked", receipt.Result)
			}
			state := observed.State
			if state == "waiting_approval" {
				receipt.ErrorCode = ""
			}
			if state != "waiting_approval" && state != "blocked" {
				state = "running"
			}
			return wait(state, scheduledExecutionSummary(state))
		}
	} else {
		if s.workflows == nil {
			return wait("blocked", "Workflow execution service is unavailable")
		}
		execution, err := s.workflows.Get(runCtx, receipt.WorkflowExecutionID)
		if err != nil {
			return err
		}
		switch execution.Status {
		case "completed":
		case "failed", "cancelled":
			return fail(apperror.New(apperror.CodeFailedPrecondition, "original scheduled workflow execution failed"))
		case "waiting_approval":
			return wait("waiting_approval", scheduledExecutionSummary("waiting_approval"))
		case "paused":
			return wait("blocked", "The original workflow is paused; resume it through its workflow controls")
		default:
			return wait("running", scheduledExecutionSummary("running"))
		}
	}
	receipt.State = "completed"
	receipt.ResumeSubmission = false
	receipt.ErrorCode = ""
	receipt.Result = "Scheduled execution completed"
	if err := save(); err != nil {
		return err
	}
	run, err := s.store.GetRun(runCtx, job.OwnerRunID)
	if err != nil {
		return err
	}
	_, _, _, err = s.store.CompleteScheduledJobRound(runCtx, lease, domain.ScheduledJobRoundOutcome{EventSequence: job.LastEventSequence, ObservationSHA256: runmutation.Fingerprint("scheduled_execution_result.v1", job.ID, lease.OperationKey), TargetStatus: run.Status, TargetTerminal: run.Terminal(), Changed: true, Result: receipt.Result}, s.clock.Now().UTC())
	return apperror.Normalize(err)
}

func scheduledExecutionSummary(state string) string {
	switch state {
	case "waiting_approval":
		return "Scheduled execution is waiting for its existing approval"
	case "blocked":
		return "Scheduled execution requires attention in its original task"
	default:
		return "Scheduled execution is running"
	}
}
func scheduledBlockedSummary(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "lifecycle hook"):
		return "A lifecycle hook blocked task startup; review the hook before continuing"
	case strings.Contains(text, "full access"):
		return "Reactivate Full Access explicitly before automatic execution can continue"
	case strings.Contains(text, "approval"):
		return "The task is waiting for its existing approval"
	case strings.Contains(text, "budget"):
		return "The task has exhausted its Run budget; continue it explicitly"
	default:
		return "The task requires attention before automatic execution can start"
	}
}
