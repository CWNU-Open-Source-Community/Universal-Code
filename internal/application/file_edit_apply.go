package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type FileEditApplyStore interface {
	fileedit.Store
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	RecordPolicyDecision(context.Context, policy.DecisionRecord) error
	GetFileEditApplyOperation(context.Context, string) (
		fileedit.ApplyOperation, *fileedit.ApplyResult, bool, error)
	PrepareFileEditApply(context.Context, fileedit.ApplyOperation) (
		fileedit.ApplyOperation, *fileedit.ApplyResult, bool, error)
	CompleteFileEditApply(context.Context, fileedit.ApplyResult) (
		fileedit.ApplyResult, bool, error)
}

type FileEditApplyService struct {
	store                 FileEditApplyStore
	manager               *fileedit.Manager
	checker               policy.Checker
	checkpoints           *WorkspaceCheckpointService
	drydocks              *RunWorktreeService
	now                   func() time.Time
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities
}

func (s *FileEditApplyService) WithExecutionPermissionCapabilities(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *FileEditApplyService {
	if s != nil {
		s.executionCapabilities = capabilities
	}
	return s
}

func (s *FileEditApplyService) WithRunWorktree(drydocks *RunWorktreeService) *FileEditApplyService {
	if s != nil {
		s.drydocks = drydocks
	}
	return s
}

type ApplyFileEditRequest struct {
	Version                string
	RunID                  string
	EditID                 string
	OperationKey           string
	AppliedBy              string
	InvocationID           string
	CapabilityGeneration   string
	LeaseID                string
	LeaseGeneration        int64
	PermissionSnapshotID   string
	PermissionGeneration   uint64
	PermissionRuntimeEpoch string
	RunAuthorizationFence  uint64
}

type ApplyFileEditResult struct {
	Operation      fileedit.ApplyOperation
	Result         fileedit.ApplyResult
	Edit           fileedit.Edit
	StagingCleanup fileedit.StagingCleanupResult
	Replayed       bool
	FileWritten    bool
}

func currentApplyFileHash(root, path, operation string) (string, error) {
	if operation == fileedit.OperationCreate {
		return fileedit.CurrentCreateHash(root, path)
	}
	return fileedit.CurrentHash(root, path)
}

func NewFileEditApplyService(store FileEditApplyStore,
	checker policy.Checker, checkpoints ...*WorkspaceCheckpointService,
) *FileEditApplyService {
	checkpointService := embeddedWorkspaceCheckpointService(store,
		domain.ExecutionPermissionRuntimeCapabilities{})
	if len(checkpoints) != 0 {
		checkpointService = checkpoints[0]
	}
	return &FileEditApplyService{store: store, manager: fileedit.NewManager(store),
		checkpoints: checkpointService,
		checker:     checker, now: func() time.Time { return time.Now().UTC() }}
}

func (s *FileEditApplyService) Apply(ctx context.Context,
	request ApplyFileEditRequest,
) (value ApplyFileEditResult, returnErr error) {
	if s == nil || s.store == nil {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"FileEdit apply dependencies are required")
	}
	normalized, err := normalizeFileEditApplyRequest(request)
	if err != nil {
		return value, err
	}
	if normalized.LeaseID != "" {
		return s.applyWithLease(ctx, normalized)
	}
	// Completed operation replay is read-only and remains available after the
	// Run ends. Every manual attempt that could write must first claim the same
	// durable execution lease used by model and tool execution.
	_, result, found, err := s.store.GetFileEditApplyOperation(ctx,
		runmutation.FileEditApplyOperationDigest(normalized.RunID, normalized.EditID,
			normalized.OperationKey))
	if err != nil {
		return value, apperror.Normalize(err)
	}
	if found && result != nil {
		return s.applyWithLease(ctx, normalized)
	}
	leaseStore, ok := s.store.(RunExecutionLeaseStore)
	if !ok {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"manual FileEdit apply requires durable execution lease storage")
	}
	err = withRunExecutionLease(ctx, leaseStore, normalized.RunID,
		idgen.New("file-edit-operator"), DefaultRunExecutionLeasePolicy(),
		func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
			normalized.LeaseID = lease.LeaseID
			normalized.LeaseGeneration = lease.Generation
			var applyErr error
			value, applyErr = s.applyWithLease(leaseCtx, normalized)
			return applyErr
		})
	return value, err
}

func (s *FileEditApplyService) applyWithLease(ctx context.Context,
	request ApplyFileEditRequest,
) (value ApplyFileEditResult, returnErr error) {
	if s == nil || s.store == nil || s.manager == nil || s.checker == nil || s.now == nil {
		return ApplyFileEditResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"FileEdit apply dependencies are required")
	}
	normalized, err := normalizeFileEditApplyRequest(request)
	if err != nil {
		return ApplyFileEditResult{}, err
	}
	keyDigest := runmutation.FileEditApplyOperationDigest(normalized.RunID,
		normalized.EditID, normalized.OperationKey)
	fingerprint := runmutation.FileEditApplyRequestFingerprint(normalized.RunID,
		normalized.EditID, normalized.AppliedBy)
	operation, storedResult, found, err := s.store.GetFileEditApplyOperation(ctx,
		keyDigest)
	if err != nil {
		return ApplyFileEditResult{}, apperror.Normalize(err)
	}
	preparedReplay := found
	if found {
		if operation.ProtocolVersion != fileedit.FileEditApplyProtocolVersion ||
			operation.KeyDigest != keyDigest || operation.RequestFingerprint != fingerprint ||
			operation.RunID != normalized.RunID || operation.EditID != normalized.EditID ||
			operation.AppliedBy != normalized.AppliedBy {
			return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
				"FileEdit apply operation key was used for different intent")
		}
		edit, lookupErr := s.store.GetFileEdit(ctx, operation.EditID)
		if lookupErr != nil {
			return ApplyFileEditResult{}, apperror.Normalize(lookupErr)
		}
		if storedResult != nil {
			return ApplyFileEditResult{Operation: operation, Result: *storedResult,
				Edit: edit, Replayed: true}, nil
		}
	} else {
		binding, bindingErr := s.loadBinding(ctx, normalized.RunID, normalized.EditID,
			true)
		if bindingErr != nil {
			return ApplyFileEditResult{}, bindingErr
		}
		if err := s.checkAutomaticFileEditAuthorization(ctx, binding, normalized); err != nil {
			return ApplyFileEditResult{}, err
		}
		if policyErr := s.checkCurrentPolicy(ctx, binding); policyErr != nil {
			return ApplyFileEditResult{}, policyErr
		}
		observedHash, hashErr := currentApplyFileHash(binding.workspace.RootPath,
			binding.edit.Path, binding.edit.Operation)
		if hashErr != nil {
			return ApplyFileEditResult{}, apperror.Normalize(hashErr)
		}
		if observedHash != binding.edit.OriginalHash &&
			observedHash != binding.edit.ProposedHash {
			return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
				"workspace file changed after review; refusing to apply")
		}
		destinationObservedHash := ""
		if binding.edit.Operation == fileedit.OperationMove {
			destinationObservedHash, hashErr = fileedit.CurrentHash(binding.workspace.RootPath,
				binding.edit.DestinationPath)
			if hashErr != nil {
				return ApplyFileEditResult{}, apperror.Normalize(hashErr)
			}
			if destinationObservedHash != binding.edit.DestinationOriginalHash &&
				destinationObservedHash != binding.edit.DestinationProposedHash {
				return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
					"workspace move destination changed after review; refusing to apply")
			}
		}
		operation = fileedit.ApplyOperation{
			ProtocolVersion: fileedit.FileEditApplyProtocolVersion,
			KeyDigest:       keyDigest, RequestFingerprint: fingerprint,
			RunID: binding.run.ID, SessionID: binding.run.SessionID,
			WorkspaceID: binding.edit.WorkspaceID, EditID: binding.edit.ID,
			Operation: binding.edit.Operation, Path: binding.edit.Path,
			DestinationPath: binding.edit.DestinationPath,
			OriginalHash:    binding.edit.OriginalHash,
			ProposedHash:    binding.edit.ProposedHash, ObservedHash: observedHash,
			DestinationOriginalHash: binding.edit.DestinationOriginalHash,
			DestinationProposedHash: binding.edit.DestinationProposedHash,
			DestinationObservedHash: destinationObservedHash,
			AppliedBy:               normalized.AppliedBy, CreatedAt: s.now().UTC(),
		}
		operation, storedResult, preparedReplay, err = s.store.PrepareFileEditApply(ctx,
			operation)
		if err != nil {
			return ApplyFileEditResult{}, apperror.Normalize(err)
		}
		if storedResult != nil {
			edit, lookupErr := s.store.GetFileEdit(ctx, operation.EditID)
			return ApplyFileEditResult{Operation: operation, Result: *storedResult,
				Edit: edit, Replayed: true}, apperror.Normalize(lookupErr)
		}
	}

	binding, err := s.loadOperationBinding(ctx, operation)
	if err != nil {
		return ApplyFileEditResult{}, err
	}
	currentHash, err := currentApplyFileHash(binding.workspace.RootPath, operation.Path, operation.Operation)
	if err != nil {
		return ApplyFileEditResult{}, apperror.Normalize(err)
	}
	if currentHash != operation.OriginalHash && currentHash != operation.ProposedHash {
		return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
			"workspace file no longer matches the prepared FileEdit apply operation")
	}
	if operation.Operation == fileedit.OperationMove {
		destinationHash, destinationErr := fileedit.CurrentHash(binding.workspace.RootPath,
			operation.DestinationPath)
		if destinationErr != nil || (destinationHash != operation.DestinationOriginalHash &&
			destinationHash != operation.DestinationProposedHash) {
			return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
				"workspace move destination no longer matches the prepared operation")
		}
	}
	fileWritten := binding.edit.Status == fileedit.StatusApproved &&
		currentHash == operation.OriginalHash
	if fileWritten {
		if binding.run.Status != domain.RunRunning ||
			binding.session.Status != session.StatusActive {
			return ApplyFileEditResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"FileEdit apply recovery requires a running Run and active Session before writing")
		}
		// Preparation is durable, but execution authority is not. Recheck Policy at
		// the final write boundary so a restart cannot revive stale permission.
		if policyErr := s.checkCurrentPolicy(ctx, binding); policyErr != nil {
			return ApplyFileEditResult{}, policyErr
		}
		if err := s.checkAutomaticFileEditAuthorization(ctx, binding, normalized); err != nil {
			return ApplyFileEditResult{}, err
		}
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: operation.RunID,
		Kind: workspacecheckpoint.TransactionFileTool, OperationKey: operation.KeyDigest,
		TriggerReceiptID: operation.EditID, InvocationID: normalized.InvocationID,
		CapabilityGeneration: normalized.CapabilityGeneration,
		LeaseID:              normalized.LeaseID, LeaseGeneration: normalized.LeaseGeneration}
	checkpoints := s.checkpoints
	if binding.target.Drydock != nil {
		checkpoints = s.drydocks.FileEditCheckpointService()
		if checkpoints == nil {
			return ApplyFileEditResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"Drydock file edit checkpoint boundary is unavailable")
		}
	}
	boundaryCompleted := false
	if checkpoints != nil {
		if _, err := checkpoints.BeginBoundary(ctx, boundaryRequest); err != nil {
			return ApplyFileEditResult{}, err
		}
		defer func() {
			if boundaryCompleted {
				return
			}
			completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
				30*time.Second)
			defer cancel()
			_, boundaryErr := checkpoints.CompleteBoundary(completionCtx,
				boundaryRequest, returnErr)
			returnErr = errors.Join(returnErr, boundaryErr)
		}()
	}
	applied := binding.edit
	var applyErr error
	switch binding.edit.Status {
	case fileedit.StatusApproved:
		preWrite, err := s.fileEditDispatchCheck(ctx, operation, binding, normalized)
		if err != nil {
			return ApplyFileEditResult{}, err
		}
		applied, applyErr = s.manager.ApproveWithPreWriteCheck(ctx, binding.edit.ID,
			binding.workspace.RootPath, preWrite)
	case fileedit.StatusApplied, fileedit.StatusFailed:
		// Recover the durable result after a process interruption.
	default:
		return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
			"FileEdit is not in an applicable state")
	}
	status := fileedit.ApplyCompleted
	reasonCode := ""
	if applyErr != nil || applied.Status == fileedit.StatusFailed {
		status = fileedit.ApplyFailed
		reasonCode = "file_edit_apply_failed"
	}
	if status == fileedit.ApplyCompleted {
		writtenHash, hashErr := currentApplyFileHash(binding.workspace.RootPath,
			operation.Path, operation.Operation)
		if hashErr != nil || writtenHash != operation.ProposedHash ||
			applied.Status != fileedit.StatusApplied {
			if hashErr == nil {
				hashErr = errors.New("applied FileEdit failed final hash verification")
			}
			return ApplyFileEditResult{}, apperror.Normalize(hashErr)
		}
		if operation.Operation == fileedit.OperationMove {
			destinationHash, destinationErr := fileedit.CurrentHash(binding.workspace.RootPath,
				operation.DestinationPath)
			if destinationErr != nil || destinationHash != operation.DestinationProposedHash {
				return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
					"applied move failed destination hash verification")
			}
		}
	}
	if binding.target.Drydock != nil {
		// Seal the owned Workspace cursor and metadata before the durable Apply
		// result. A completed receipt can then replay without touching either root.
		completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_, boundaryErr := checkpoints.CompleteBoundary(completionCtx, boundaryRequest, applyErr)
		cancel()
		if boundaryErr != nil {
			return ApplyFileEditResult{}, boundaryErr
		}
		boundaryCompleted = true
	}
	result, completionReplay, completionErr := s.store.CompleteFileEditApply(ctx,
		fileedit.ApplyResult{OperationKeyDigest: operation.KeyDigest, Status: status,
			ReasonCode: reasonCode, CompletedAt: s.now().UTC()})
	if completionErr != nil {
		if applyErr != nil {
			return ApplyFileEditResult{}, apperror.Normalize(errors.Join(applyErr,
				completionErr))
		}
		return ApplyFileEditResult{}, apperror.Normalize(completionErr)
	}
	cleanup := s.cleanupStaging(ctx, binding.workspace.RootPath, operation)
	value = ApplyFileEditResult{Operation: operation, Result: result, Edit: applied,
		StagingCleanup: cleanup, Replayed: preparedReplay || completionReplay,
		FileWritten: fileWritten}
	if applyErr != nil {
		return value, apperror.Normalize(applyErr)
	}
	return value, nil
}

func (s *FileEditApplyService) cleanupStaging(ctx context.Context,
	workspaceRoot string, operation fileedit.ApplyOperation,
) fileedit.StagingCleanupResult {
	if operation.Operation == fileedit.OperationMove ||
		operation.Operation == fileedit.OperationDelete {
		return fileedit.StagingCleanupResult{}
	}
	// The apply already resolved and verified this exact root. Cleanup only
	// removes old internal staging files with the approved bytes; resolving the
	// whole Drydock again after committing the receipt adds no write authority.
	if ctx.Err() != nil {
		return fileedit.StagingCleanupResult{Pending: true}
	}
	result, err := fileedit.CleanupStaleStaging(workspaceRoot,
		operation.Path, operation.ProposedHash, s.now().UTC())
	if err != nil {
		return fileedit.StagingCleanupResult{Pending: true}
	}
	return result
}

type fileEditApplyBinding struct {
	run       domain.Run
	mission   domain.Mission
	session   session.Session
	workspace session.WorkspaceInfo
	edit      fileedit.Edit
	approval  approval.Record
	target    RunFileWorkspace
}

func (s *FileEditApplyService) loadBinding(ctx context.Context, runID string,
	editID string, requireRunning bool,
) (fileEditApplyBinding, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return fileEditApplyBinding{}, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return fileEditApplyBinding{}, apperror.Normalize(err)
	}
	linkedSession, err := s.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return fileEditApplyBinding{}, apperror.Normalize(err)
	}
	edit, err := s.store.GetFileEdit(ctx, editID)
	if err != nil {
		return fileEditApplyBinding{}, apperror.Normalize(err)
	}
	if err := checkRunProjectPaths(run, edit.Path, edit.DestinationPath); err != nil {
		return fileEditApplyBinding{}, err
	}
	target, err := ResolveRunFileWorkspace(ctx, s.store, run, mission, s.drydocks)
	if err != nil {
		return fileEditApplyBinding{}, apperror.Normalize(err)
	}
	record, err := s.store.GetApprovalByProposal(ctx, edit.ID)
	if err != nil {
		return fileEditApplyBinding{}, apperror.Normalize(err)
	}
	if run.SessionID == "" || mission.WorkspaceID == "" ||
		linkedSession.ID != run.SessionID ||
		linkedSession.WorkspaceID != mission.WorkspaceID ||
		edit.SessionID != run.SessionID || edit.WorkspaceID != target.Workspace.ID ||
		record.RunID != run.ID || record.SessionID != run.SessionID ||
		record.WorkspaceID != target.Workspace.ID ||
		record.ProposalID != edit.ID || record.ToolName != fileedit.ApprovalToolName(edit) ||
		record.ActionClass != "workspace_write" || record.Status != approval.StatusApproved {
		return fileEditApplyBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"FileEdit apply binding or approval is invalid")
	}
	if requireRunning && (run.Status != domain.RunRunning ||
		linkedSession.Status != session.StatusActive ||
		edit.Status != fileedit.StatusApproved) {
		return fileEditApplyBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"new FileEdit apply requires a running Run, active Session, and approved edit")
	}
	return fileEditApplyBinding{run: run, mission: mission, session: linkedSession,
		workspace: target.Workspace, edit: edit, approval: record, target: target}, nil
}

func (s *FileEditApplyService) checkCurrentPolicy(ctx context.Context,
	binding fileEditApplyBinding,
) error {
	decision, err := s.fileEditPolicyDecision(ctx, binding)
	if err != nil {
		return err
	}
	if !decision.Allowed || (binding.approval.Mode == "automatic" && decision.NeedsApproval) {
		return apperror.New(apperror.CodePolicyDenied, "FileEdit apply requires the current host policy and review")
	}
	return nil
}

func (s *FileEditApplyService) fileEditPolicyDecision(ctx context.Context, binding fileEditApplyBinding) (policy.Decision, error) {
	decision := s.checker.CheckToolCall(tools.Call{Name: fileedit.ApprovalToolName(binding.edit),
		Args: map[string]string{
			"run_id": binding.run.ID, "workspace_id": binding.edit.WorkspaceID,
			"path": binding.edit.Path, "original_hash": binding.edit.OriginalHash,
			"proposed_hash":             binding.edit.ProposedHash,
			"destination_path":          binding.edit.DestinationPath,
			"destination_original_hash": binding.edit.DestinationOriginalHash,
			"destination_proposed_hash": binding.edit.DestinationProposedHash,
		}})
	if err := s.store.RecordPolicyDecision(ctx, policy.DecisionRecord{
		SessionID: binding.run.SessionID, SubjectID: binding.edit.ID,
		Context: "file_edit_apply", Decision: decision,
	}); err != nil {
		return decision, apperror.Normalize(err)
	}
	if !decision.Allowed {
		return decision, apperror.New(apperror.CodePolicyDenied,
			"FileEdit apply was denied by current Policy")
	}
	return decision, nil
}

// Automatic approval records an exact operation-policy decision, not a
// perpetual permission to write. Recheck the exact source and live authority on
// every attempt that can still publish file bytes.
func (s *FileEditApplyService) checkAutomaticFileEditAuthorization(ctx context.Context,
	binding fileEditApplyBinding, request ApplyFileEditRequest,
) error {
	if binding.approval.Mode != "automatic" {
		return nil
	}
	reader, ok := s.store.(interface {
		GetFileEditAutoAuthorization(context.Context, string) (fileedit.AutoAuthorization, bool, error)
		GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
		GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
		GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	})
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition,
			"automatic FileEdit authorization reader is unavailable")
	}
	source, found, err := reader.GetFileEditAutoAuthorization(ctx, binding.edit.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !found || binding.edit.Operation != source.Operation ||
		binding.edit.Path != source.Path || binding.edit.OriginalHash != source.OriginalHash ||
		binding.edit.ProposedHash != source.ProposedHash ||
		binding.edit.DestinationPath != source.DestinationPath ||
		binding.edit.DestinationOriginalHash != source.DestinationOriginalHash ||
		binding.edit.DestinationProposedHash != source.DestinationProposedHash ||
		binding.edit.SessionID != source.SessionID || binding.edit.WorkspaceID != source.WorkspaceID ||
		binding.run.ID != source.RunID || request.AppliedBy != source.AgentID ||
		request.RunAuthorizationFence != source.RunAuthorizationFence {
		return apperror.New(apperror.CodeFailedPrecondition,
			"automatic FileEdit source no longer matches the exact proposal")
	}
	currentRun, err := s.store.GetRun(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	permission, err := reader.GetRunExecutionPermission(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	mode, err := reader.GetRunMode(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	lease, leaseFound, err := reader.GetRunExecutionLease(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	generation, live := s.executionCapabilities.FullAccessGeneration(permission)
	epoch := ""
	if s.executionCapabilities.RuntimeAuthority != nil {
		epoch = s.executionCapabilities.RuntimeAuthority.RuntimeEpoch()
	}
	if currentRun.Status != domain.RunRunning ||
		!live || !agentCodeRuntimeCurrent(s.executionCapabilities, permission, source.PermissionSnapshotID, source.RuntimeGeneration, source.RuntimeEpoch, source.RunAuthorizationFence) ||
		permission.ID != source.PermissionSnapshotID ||
		permission.Revision != source.PermissionRevision ||
		mode.Revision != source.ModeRevision ||
		request.PermissionSnapshotID != permission.ID ||
		request.PermissionGeneration != generation ||
		request.PermissionRuntimeEpoch != epoch ||
		source.RuntimeGeneration != generation || source.RuntimeEpoch != epoch ||
		request.CapabilityGeneration != source.CapabilityGeneration ||
		!leaseFound || !lease.ActiveAt(s.now().UTC()) ||
		lease.LeaseID != request.LeaseID || lease.Generation != request.LeaseGeneration {
		return apperror.New(apperror.CodePolicyDenied,
			"automatic FileEdit apply requires its current operation policy, runtime fence and lease")
	}
	return nil
}

func (s *FileEditApplyService) loadOperationBinding(ctx context.Context,
	operation fileedit.ApplyOperation,
) (fileEditApplyBinding, error) {
	binding, err := s.loadBinding(ctx, operation.RunID, operation.EditID, false)
	if err != nil {
		return fileEditApplyBinding{}, err
	}
	if binding.run.SessionID != operation.SessionID ||
		binding.edit.WorkspaceID != operation.WorkspaceID ||
		binding.edit.Operation != operation.Operation ||
		binding.edit.Path != operation.Path ||
		binding.edit.DestinationPath != operation.DestinationPath ||
		binding.edit.OriginalHash != operation.OriginalHash ||
		binding.edit.ProposedHash != operation.ProposedHash ||
		binding.edit.DestinationOriginalHash != operation.DestinationOriginalHash ||
		binding.edit.DestinationProposedHash != operation.DestinationProposedHash {
		return fileEditApplyBinding{}, apperror.New(apperror.CodeConflict,
			"prepared FileEdit apply binding changed")
	}
	return binding, nil
}

func normalizeFileEditApplyRequest(request ApplyFileEditRequest) (
	ApplyFileEditRequest, error,
) {
	if request.Version != fileedit.FileEditApplyProtocolVersion ||
		!validControlIdentity(request.RunID) || !validControlIdentity(request.EditID) ||
		!validControlIdentity(request.AppliedBy) {
		return ApplyFileEditRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"FileEdit apply request is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return ApplyFileEditRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"FileEdit apply idempotency key is invalid")
	}
	request.OperationKey = key
	request.AppliedBy = strings.TrimSpace(request.AppliedBy)
	request.InvocationID = strings.TrimSpace(request.InvocationID)
	request.CapabilityGeneration = strings.TrimSpace(request.CapabilityGeneration)
	request.LeaseID = strings.TrimSpace(request.LeaseID)
	request.PermissionSnapshotID = strings.TrimSpace(request.PermissionSnapshotID)
	request.PermissionRuntimeEpoch = strings.TrimSpace(request.PermissionRuntimeEpoch)
	if (request.LeaseID == "") != (request.LeaseGeneration == 0) ||
		request.LeaseGeneration < 0 ||
		(request.PermissionGeneration != 0 && (request.PermissionSnapshotID == "" || request.PermissionRuntimeEpoch == "")) ||
		(request.RunAuthorizationFence != 0 && request.PermissionRuntimeEpoch == "") {
		return ApplyFileEditRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"FileEdit apply execution lease is invalid")
	}
	return request, nil
}
