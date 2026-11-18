package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type ApprovalContinuationRequest struct {
	RunID      string
	Kind       string
	ProposalID string
}

// The review is already durable when this result is returned. Failed refers
// only to its subsequent model continuation, never to the operator decision.
type ApprovalContinuationResult struct {
	State       string `json:"state"`
	HandoffID   string `json:"handoff_id,omitempty"`
	Replayed    bool   `json:"replayed"`
	ModelCalled bool   `json:"model_called"`
	ToolCalled  bool   `json:"tool_called"`
	ErrorCode   string `json:"error_code,omitempty"`
	Message     string `json:"message,omitempty"`
}

type approvalContinuationStore interface {
	PrepareApprovalContinuation(context.Context, string, string, string) (domain.ApprovalContinuation, bool, error)
	BeginSupervisorApprovalContinuation(context.Context, domain.RunExecutionLease, string) (domain.SupervisorTurn, error)
	CloseFailedApprovalContinuation(context.Context, string) error
}

// ResumeApproval shares the Thread's live owner and stop button. It never
// approves or executes a proposal itself, nor manufactures a user message.
func (s *ThreadTurnService) ResumeApproval(ctx context.Context, request ApprovalContinuationRequest) ApprovalContinuationResult {
	if s == nil || s.threads == nil || s.execution == nil {
		return approvalContinuationFailed(apperror.New(apperror.CodeUnavailable, "Approval continuation is unavailable"))
	}
	if !validControlIdentity(request.RunID) || !validControlIdentity(request.ProposalID) || (request.Kind != "file_edit" && request.Kind != "host_command" && request.Kind != "agent_browser" && request.Kind != "mcp" && request.Kind != "command_runtime") {
		return approvalContinuationFailed(apperror.New(apperror.CodeInvalidArgument, "Approval continuation identity is invalid"))
	}
	thread, err := s.threads.store.GetThreadByRun(ctx, request.RunID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	if thread.Status != domain.ThreadActive || thread.ActiveRunID != request.RunID {
		return ApprovalContinuationResult{State: "not_started"}
	}
	s.turnMu.Lock()
	if active := s.activeTurns[thread.ID]; active != nil {
		defer s.turnMu.Unlock()
		if active.stopping {
			return ApprovalContinuationResult{State: "not_started"}
		}
		for _, pending := range active.approvals {
			if pending == request {
				return ApprovalContinuationResult{State: "queued", Replayed: true}
			}
		}
		if len(active.approvals) >= 32 {
			return approvalContinuationFailed(apperror.New(apperror.CodeResourceExhausted, "Pending review continuation queue is full"))
		}
		active.approvals = append(active.approvals, request)
		return ApprovalContinuationResult{State: "queued"}
	}
	turnCtx, cancel := context.WithCancel(ctx)
	active := &activeThreadTurn{id: idgen.New("thread-execution"), operationKey: "approval:" + request.Kind + ":" + request.ProposalID, cancel: cancel}
	if s.activeTurns == nil {
		s.activeTurns = make(map[string]*activeThreadTurn)
	}
	s.activeTurns[thread.ID] = active
	s.turnMu.Unlock()
	defer func() {
		cancel()
		s.turnMu.Lock()
		if s.activeTurns[thread.ID] == active {
			active.stopping = true
			delete(s.activeTurns, thread.ID)
		}
		s.turnMu.Unlock()
	}()
	result := s.resumeApprovalWithOwner(turnCtx, request)
	// Accepted new messages retain their ordinary ownership and priority. They
	// are consumed only after this approval continuation reaches its boundary.
	for result.State != "failed" && turnCtx.Err() == nil {
		s.turnMu.Lock()
		if len(active.queue) > 0 {
			next := active.queue[0]
			active.queue = active.queue[1:]
			s.turnMu.Unlock()
			var err error
			next.result.Submission.Message, err = s.execution.store.GetOperatorSteering(turnCtx, next.result.Submission.Message.ID)
			if err == nil && next.result.Submission.Message.Status != domain.OperatorSteeringPending {
				continue
			}
			if err == nil {
				next.result.Submission.Run, err = s.threads.store.GetRun(turnCtx, next.result.Submission.Run.ID)
			}
			if err == nil {
				_, err = s.executeSubmission(turnCtx, next.request, next.result)
			}
			if err != nil {
				break
			}
			continue
		}
		if len(active.approvals) == 0 {
			active.stopping = true
			if s.activeTurns[thread.ID] == active {
				delete(s.activeTurns, thread.ID)
			}
			s.turnMu.Unlock()
			break
		}
		next := active.approvals[0]
		active.approvals = active.approvals[1:]
		s.turnMu.Unlock()
		if resumed := s.resumeApprovalWithOwner(turnCtx, next); resumed.State == "failed" {
			break
		}
	}
	return result
}

func approvalContinuationFailed(err error) ApprovalContinuationResult {
	return ApprovalContinuationResult{State: "failed", ErrorCode: string(apperror.CodeOf(apperror.Normalize(err))), Message: "审批已保存，后续执行未完成。请先查看执行记录；结果未确认的写入或命令不会自动重试。"}
}

func (s *ThreadTurnService) resumeApprovalWithOwner(ctx context.Context, request ApprovalContinuationRequest) ApprovalContinuationResult {
	if request.Kind == "agent_browser" || request.Kind == "mcp" || request.Kind == "command_runtime" {
		return s.resumePendingToolApproval(ctx, request)
	}
	store, ok := s.execution.store.(approvalContinuationStore)
	if !ok {
		return ApprovalContinuationResult{State: "not_started"}
	}
	thread, err := s.threads.store.GetThreadByRun(ctx, request.RunID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	run, err := s.threads.store.GetRun(ctx, request.RunID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	if configStore, ok := s.threads.store.(threadEpochTransitionStore); ok {
		pending, err := s.pendingEpochConfiguration(ctx, configStore, thread, run)
		if err != nil {
			return approvalContinuationFailed(err)
		}
		if pending {
			return ApprovalContinuationResult{State: "not_started"}
		}
	}
	value, found, err := store.PrepareApprovalContinuation(ctx, request.RunID, request.Kind, request.ProposalID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	if !found {
		return ApprovalContinuationResult{State: "not_started"}
	}
	if value.Handoff.Result != nil {
		return approvalContinuationProjection(value.Handoff, true)
	}
	var execution LifecycleResult
	err = s.execution.supervisor.withRunExecutionLease(ctx, request.RunID, func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
		turn, stepErr := store.BeginSupervisorApprovalContinuation(leaseCtx, lease, value.Handoff.Operation.ID)
		if stepErr == nil {
			editIDs := value.ApprovedFileEditIDs
			if len(editIDs) == 0 && request.Kind == "file_edit" && request.ProposalID != "" {
				editIDs = []string{request.ProposalID}
			}
			for _, editID := range editIDs {
				if stepErr = s.execution.supervisor.applyApprovedFileEditForContinuation(leaseCtx, lease, turn, value.Handoff.Operation.ID, editID); stepErr != nil {
					break
				}
			}
		}
		if stepErr == nil {
			execution, stepErr = s.execution.supervisor.stepWithLease(leaseCtx, lease, value.Input)
		}
		status, code, reason := domain.RunExecutionHandoffCompleted, "", "approval_continuation"
		if stepErr != nil {
			status = domain.RunExecutionHandoffFailed
			code = strings.ToLower(string(apperror.CodeOf(apperror.Normalize(stepErr))))
			reason = code
		}
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(leaseCtx), 2*time.Second)
		defer cancel()
		steps := 0
		if execution.Turn > 0 {
			steps = 1
		}
		result, _, recordErr := s.execution.store.CompleteRunExecutionHandoff(settleCtx, value.Handoff.Operation.ID, lease, status, reason, code, steps, execution.ModelAttempts > 0, execution.ToolCalls > 0)
		if recordErr == nil {
			value.Handoff.Result = &result
		}
		return errors.Join(stepErr, recordErr)
	})
	if err != nil {
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if value.Handoff.Result != nil {
			err = errors.Join(err, store.CloseFailedApprovalContinuation(settleCtx, value.Handoff.Operation.ID))
		}
		result := approvalContinuationFailed(err)
		result.HandoffID = value.Handoff.Operation.ID
		result.ModelCalled = execution.ModelAttempts > 0
		result.ToolCalled = execution.ToolCalls > 0
		return result
	}
	return approvalContinuationProjection(value.Handoff, false)
}

func approvalContinuationProjection(handoff domain.RunExecutionHandoff, replayed bool) ApprovalContinuationResult {
	result := ApprovalContinuationResult{State: "completed", HandoffID: handoff.Operation.ID, Replayed: replayed}
	if handoff.Result == nil {
		return approvalContinuationFailed(apperror.New(apperror.CodeUnavailable, "Approval continuation outcome is unknown"))
	}
	result.ModelCalled, result.ToolCalled = handoff.Result.ModelCalled, handoff.Result.ToolCalled
	if handoff.Result.Status == domain.RunExecutionHandoffFailed {
		result.State = "failed"
		result.ErrorCode = strings.ToUpper(handoff.Result.ErrorCode)
		result.Message = "审批已保存，后续执行未完成。请先查看执行记录；结果未确认的写入或命令不会自动重试。"
	}
	return result
}

func (s *AgentRunner) applyApprovedFileEditForContinuation(ctx context.Context,
	lease domain.RunExecutionLease, turn domain.SupervisorTurn,
	handoffID, proposalID string,
) error {
	if s == nil || s.store == nil || s.checker == nil {
		return nil
	}
	applyStore, ok := s.store.(FileEditApplyStore)
	if !ok {
		return nil
	}
	edit, err := applyStore.GetFileEdit(ctx, proposalID)
	if err != nil {
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound {
			return nil
		}
		return apperror.Normalize(err)
	}
	if edit.Status != fileedit.StatusApproved || edit.SessionID != turn.Run.SessionID {
		return nil
	}
	approvalRecord, err := applyStore.GetApprovalByProposal(ctx, edit.ID)
	if err != nil {
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound {
			return nil
		}
		return apperror.Normalize(err)
	}
	if approvalRecord.Status != approval.StatusApproved {
		return nil
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	_, authorityJSON, err := s.supervisorAgentCodeCapabilities(ctx, turn, permission)
	if err != nil {
		return err
	}
	capabilityGeneration := ""
	permissionSnapshotID := ""
	var permissionGeneration uint64
	permissionRuntimeEpoch := ""
	var runAuthorizationFence uint64
	if len(authorityJSON) > 0 {
		if authority, decodeErr := toolgateway.DecodeAgentCodeCallAuthority(authorityJSON); decodeErr == nil {
			capabilityGeneration = authority.CapabilityGeneration
			permissionSnapshotID = authority.PermissionSnapshotID
			permissionGeneration = authority.PermissionGeneration
			permissionRuntimeEpoch = authority.PermissionRuntimeEpoch
			runAuthorizationFence = authority.RunAuthorizationFence
		}
	}
	checkpoints := embeddedWorkspaceCheckpointService(applyStore, s.executionCapabilities)
	applyService := NewFileEditApplyService(applyStore, s.checker, checkpoints).
		WithExecutionPermissionCapabilities(s.executionCapabilities).
		WithRunWorktree(s.drydocks)
	applied, applyErr := applyService.Apply(ctx, ApplyFileEditRequest{
		Version:                fileedit.FileEditApplyProtocolVersion,
		RunID:                  turn.Run.ID,
		EditID:                 edit.ID,
		OperationKey:           runmutation.Fingerprint("file_edit_approval_apply_v1", turn.Run.ID, edit.ID),
		AppliedBy:              "approval_continuation",
		AttemptID:              turn.Checkpoint.AttemptID,
		CapabilityGeneration:   capabilityGeneration,
		LeaseID:                lease.LeaseID,
		LeaseGeneration:        lease.Generation,
		PermissionSnapshotID:   permissionSnapshotID,
		PermissionGeneration:   permissionGeneration,
		PermissionRuntimeEpoch: permissionRuntimeEpoch,
		RunAuthorizationFence:  runAuthorizationFence,
	})
	var evidenceText string
	if applyErr != nil {
		code := apperror.CodeOf(apperror.Normalize(applyErr))
		if code != apperror.CodeConflict && code != apperror.CodePolicyDenied {
			return applyErr
		}
		if latest, lookupErr := applyStore.GetFileEdit(ctx, edit.ID); lookupErr == nil && latest.Status == fileedit.StatusApproved {
			latest.Status = fileedit.StatusFailed
			latest.Reason = applyErr.Error()
			latest.UpdatedAt = time.Now().UTC()
			if saved, saveErr := applyStore.SaveFileEdit(ctx, latest); saveErr == nil {
				edit = saved
			}
		}
		evidenceText = fmt.Sprintf(
			"System-recorded direct file apply result after operator approval: File proposal %s (%s) FAILED (%s) for path %q: %s. The file was NOT applied. Do not call workspace_apply; inspect the current workspace state before proposing a replacement.",
			edit.ID, edit.Operation, code, edit.Path, applyErr.Error(),
		)
	} else {
		evidenceText = fmt.Sprintf(
			"System-recorded direct file apply result after operator approval: File proposal %s (%s) is APPLIED for path %q (original SHA256 %s; proposed SHA256 %s; file_written=%t; replayed=%t). The approved change is already written to the workspace; do not call workspace_apply or repeat this edit.",
			applied.Edit.ID, applied.Edit.Operation, applied.Edit.Path, applied.Edit.OriginalHash, applied.Edit.ProposedHash, applied.FileWritten, applied.Replayed,
		)
	}
	if msgStore, ok := s.store.(interface {
		SaveSessionMessage(context.Context, session.Message) (session.Message, error)
	}); ok {
		sourceRef := handoffID + "-apply-" + edit.ID
		existing, listErr := s.store.ListSessionMessages(ctx, turn.Run.SessionID, true)
		alreadyRecorded := false
		if listErr == nil {
			for _, msg := range existing {
				if msg.Provenance.SourceKind == session.SourceToolResult && msg.Provenance.SourceRef == sourceRef {
					alreadyRecorded = true
					break
				}
			}
		}
		if !alreadyRecorded {
			if _, saveErr := msgStore.SaveSessionMessage(ctx, session.NewEvidenceMessage(
				turn.Run.SessionID, session.SourceToolResult, sourceRef, evidenceText)); saveErr != nil {
				return apperror.Normalize(saveErr)
			}
		}
	}
	return nil
}
