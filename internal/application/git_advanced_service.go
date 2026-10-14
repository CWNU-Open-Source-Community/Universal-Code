package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const GitAdvancedAPIProtocolVersion = "git-advanced-api.v1"

type GitAdvancedStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionProfile(context.Context, string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)

	CreateGitAdvancedOperation(context.Context, gitadvanced.OperationRecord) (
		gitadvanced.OperationRecord, bool, error)
	StartGitAdvancedOperation(context.Context, string, string, string, time.Time) (
		gitadvanced.OperationRecord, bool, error)
	CompleteGitAdvancedOperation(context.Context, string, gitadvanced.Receipt, time.Time) (
		gitadvanced.OperationRecord, bool, error)
	GetGitAdvancedOperation(context.Context, string) (gitadvanced.OperationRecord, bool, error)
	ListGitAdvancedOperations(context.Context, gitadvanced.OperationListFilter) (
		[]gitadvanced.OperationRecord, error)

	EnsureApproval(context.Context, approval.Proposal) (approval.Record, error)
	GetApproval(context.Context, string) (approval.Record, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)

	CreateGitAdvancedSequence(context.Context, gitadvanced.Sequence) (
		gitadvanced.Sequence, bool, error)
	AdvanceGitAdvancedSequence(context.Context, gitadvanced.Sequence, int64) (
		gitadvanced.Sequence, bool, error)
	GetGitAdvancedSequence(context.Context, string) (gitadvanced.Sequence, bool, error)
	GetActiveGitAdvancedSequence(context.Context, string) (gitadvanced.Sequence, bool, error)

	CreateManagedGitWorktree(context.Context, gitadvanced.ManagedWorktree) (
		gitadvanced.ManagedWorktree, bool, error)
	AdvanceManagedGitWorktree(context.Context, gitadvanced.ManagedWorktree, int64) (
		gitadvanced.ManagedWorktree, bool, error)
	GetManagedGitWorktree(context.Context, string) (gitadvanced.ManagedWorktree, bool, error)
	GetManagedGitWorktreeByName(context.Context, string, string) (
		gitadvanced.ManagedWorktree, bool, error)
	ListManagedGitWorktrees(context.Context, string, string, bool, int) (
		[]gitadvanced.ManagedWorktree, error)
}

type GitAdvancedService struct {
	store                  GitAdvancedStore
	executor               *repository.AdvancedExecutor
	permissionCapabilities domain.ExecutionPermissionRuntimeCapabilities
	checkpoints            *WorkspaceCheckpointService
	now                    func() time.Time
	// Installed only on a short-lived application-owned copy for an explicit
	// Thread operator action. The public advanced API keeps its running rule.
	operatorThreadID                 string
	operatorThreadPreviewFingerprint string
}

func NewGitAdvancedService(store GitAdvancedStore,
	executor *repository.AdvancedExecutor,
	permissionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
	checkpoints ...*WorkspaceCheckpointService,
) (*GitAdvancedService, error) {
	if store == nil || executor == nil || !executor.Available() ||
		permissionCapabilities.Validate() != nil ||
		!permissionCapabilities.OperatorApprovalEnabled {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced service requires enabled Git and operator-approval capabilities")
	}
	checkpointService := embeddedWorkspaceCheckpointService(store, permissionCapabilities)
	if len(checkpoints) != 0 {
		checkpointService = checkpoints[0]
	}
	if checkpointService == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced service requires Workspace Checkpoint storage")
	}
	return &GitAdvancedService{store: store, executor: executor,
		permissionCapabilities: permissionCapabilities, checkpoints: checkpointService,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

type GitAdvancedScope struct {
	CapabilityGeneration string `json:"capability_generation"`
	// LeaseID is an internal fencing identity. Public callers bind by the
	// non-secret generation and the service resolves the exact active identity
	// immediately before each review or execution.
	LeaseID         string `json:"-"`
	LeaseGeneration int64  `json:"lease_generation"`
}

type GitAdvancedReviewRequest struct {
	ProtocolVersion string           `json:"protocol_version"`
	RunID           string           `json:"run_id"`
	OperationKey    string           `json:"operation_key"`
	RequestedBy     string           `json:"requested_by"`
	Scope           GitAdvancedScope `json:"scope"`
	Spec            gitadvanced.Spec `json:"spec"`
}

type GitAdvancedReviewResult struct {
	ProtocolVersion string                       `json:"protocol_version"`
	RunID           string                       `json:"run_id"`
	WorkspaceID     string                       `json:"workspace_id"`
	Preview         gitadvanced.Preview          `json:"preview"`
	Operation       *gitadvanced.OperationRecord `json:"operation,omitempty"`
	Approval        *approval.Record             `json:"approval,omitempty"`
	Replayed        bool                         `json:"replayed"`
}

type GitAdvancedExecuteRequest struct {
	ProtocolVersion string           `json:"protocol_version"`
	RunID           string           `json:"run_id"`
	OperationID     string           `json:"operation_id"`
	ApprovalID      string           `json:"approval_id"`
	RequestedBy     string           `json:"requested_by"`
	Scope           GitAdvancedScope `json:"scope"`
}

type GitAdvancedExecuteResult struct {
	ProtocolVersion string                       `json:"protocol_version"`
	Operation       gitadvanced.OperationRecord  `json:"operation"`
	Receipt         gitadvanced.Receipt          `json:"receipt"`
	Sequence        *gitadvanced.Sequence        `json:"sequence,omitempty"`
	Worktree        *gitadvanced.ManagedWorktree `json:"worktree,omitempty"`
	Boundary        WorkspaceMutationBoundary    `json:"boundary"`
	Replayed        bool                         `json:"replayed"`
}

type GitAdvancedProjection struct {
	ProtocolVersion string                         `json:"protocol_version"`
	RunID           string                         `json:"run_id"`
	WorkspaceID     string                         `json:"workspace_id"`
	Authority       GitAdvancedAuthorityView       `json:"authority"`
	Capability      gitadvanced.CapabilitySnapshot `json:"capability"`
	Binding         gitadvanced.RepositoryBinding  `json:"binding"`
	Conflict        gitadvanced.ConflictState      `json:"conflict"`
	Stashes         []gitadvanced.StashEntry       `json:"stashes"`
	Sequence        *gitadvanced.Sequence          `json:"sequence,omitempty"`
	Worktrees       []gitadvanced.ManagedWorktree  `json:"worktrees"`
	Operations      []GitAdvancedOperationView     `json:"operations"`
}

// GitAdvancedAuthorityView exposes only the non-secret generation values a
// Desktop or API caller must echo during review and execution. The lease
// identity remains process-internal and is resolved immediately before use.
type GitAdvancedAuthorityView struct {
	ProtocolVersion      string           `json:"protocol_version"`
	Scope                GitAdvancedScope `json:"scope"`
	PermissionSnapshotID string           `json:"permission_snapshot_id"`
	PermissionRevision   int64            `json:"permission_revision"`
	LeaseActive          bool             `json:"lease_active"`
	LeaseExpiresAt       time.Time        `json:"lease_expires_at,omitempty"`
	Executable           bool             `json:"executable"`
}

// GitAdvancedOperationView is the public, parsed audit projection. Raw
// persistence JSON and the internal lease identity intentionally never cross
// the API boundary.
type GitAdvancedOperationView struct {
	ID                   string                      `json:"id"`
	ProtocolVersion      string                      `json:"protocol_version"`
	OperationKeySHA256   string                      `json:"operation_key_sha256"`
	RequestFingerprint   string                      `json:"request_fingerprint"`
	PreviewID            string                      `json:"preview_id"`
	ApprovalFingerprint  string                      `json:"approval_fingerprint"`
	ApprovalID           string                      `json:"approval_id,omitempty"`
	Operation            gitadvanced.Operation       `json:"operation"`
	Preview              gitadvanced.Preview         `json:"preview"`
	Receipt              *gitadvanced.Receipt        `json:"receipt,omitempty"`
	RepositorySHA256     string                      `json:"repository_sha256"`
	CommonDirSHA256      string                      `json:"common_dir_sha256"`
	PermissionSnapshotID string                      `json:"permission_snapshot_id"`
	PermissionRevision   int64                       `json:"permission_revision"`
	CapabilityGeneration string                      `json:"capability_generation"`
	LeaseGeneration      int64                       `json:"lease_generation"`
	Status               gitadvanced.OperationStatus `json:"status"`
	ErrorCode            gitadvanced.FailureCode     `json:"error_code,omitempty"`
	CreatedAt            time.Time                   `json:"created_at"`
	StartedAt            *time.Time                  `json:"started_at,omitempty"`
	CompletedAt          *time.Time                  `json:"completed_at,omitempty"`
}

type GitAdvancedReconcileResult struct {
	ProtocolVersion string   `json:"protocol_version"`
	Examined        int      `json:"examined"`
	Recovered       int      `json:"recovered"`
	Conflicted      int      `json:"conflicted"`
	Failed          int      `json:"failed"`
	OperationIDs    []string `json:"operation_ids"`
}

type gitAdvancedAuthority struct {
	run        domain.Run
	mission    domain.Mission
	session    session.Session
	workspace  session.WorkspaceRecord
	mode       domain.RunModeSnapshot
	profile    domain.RunExecutionProfileSnapshot
	permission domain.RunExecutionPermissionSnapshot
	lease      domain.RunExecutionLease
}

// DiscoverHunks is read-only and creates neither an Approval nor an operation.
// Execution still requires a second preview with explicit hunk identities.
func (s *GitAdvancedService) DiscoverHunks(ctx context.Context, runID string,
	spec gitadvanced.Spec,
) (GitAdvancedReviewResult, error) {
	if spec.Operation != gitadvanced.HunkStage && spec.Operation != gitadvanced.HunkUnstage &&
		spec.Operation != gitadvanced.HunkRevert || len(spec.HunkIDs) != 0 {
		return GitAdvancedReviewResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Git hunk discovery requires a hunk operation without selected identities")
	}
	authority, err := s.loadReadBinding(ctx, strings.TrimSpace(runID))
	if err != nil {
		return GitAdvancedReviewResult{}, err
	}
	preview, err := s.executor.ReviewAdvanced(ctx, authority.workspace.RootPath, spec)
	if err != nil {
		return GitAdvancedReviewResult{}, gitAdvancedApplicationError(err)
	}
	return GitAdvancedReviewResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: authority.run.ID, WorkspaceID: authority.workspace.ID, Preview: preview}, nil
}

// Preview is a non-authorizing inspection path for CLI/Desktop. It performs
// the same repository and durable-target checks as Review, but creates no
// operation and no Approval. Mutation always re-renders this evidence later.
func (s *GitAdvancedService) Preview(ctx context.Context, runID string,
	spec gitadvanced.Spec,
) (GitAdvancedReviewResult, error) {
	if s == nil || s.executor == nil || spec.Validate() != nil {
		return GitAdvancedReviewResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Git advanced preview request is invalid")
	}
	authority, err := s.loadReadBinding(ctx, strings.TrimSpace(runID))
	if err != nil {
		return GitAdvancedReviewResult{}, err
	}
	preview, err := s.executor.ReviewAdvanced(ctx, authority.workspace.RootPath, spec)
	if err != nil {
		return GitAdvancedReviewResult{}, gitAdvancedApplicationError(err)
	}
	if err := s.requireDurableTarget(ctx, authority, preview); err != nil {
		return GitAdvancedReviewResult{}, err
	}
	return GitAdvancedReviewResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: authority.run.ID, WorkspaceID: authority.workspace.ID,
		Preview: preview}, nil
}

func (s *GitAdvancedService) Review(ctx context.Context,
	request GitAdvancedReviewRequest,
) (GitAdvancedReviewResult, error) {
	if s == nil || s.store == nil || s.executor == nil || s.now == nil ||
		request.ProtocolVersion != GitAdvancedAPIProtocolVersion {
		return GitAdvancedReviewResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Git advanced review request is invalid")
	}
	normalizeGitAdvancedReviewRequest(&request)
	if err := s.resolveGitAdvancedLeaseID(ctx, request.RunID, &request.Scope); err != nil {
		return GitAdvancedReviewResult{}, err
	}
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" ||
		request.Scope.CapabilityGeneration == "" || request.Scope.LeaseID == "" ||
		request.Scope.LeaseGeneration <= 0 || request.Spec.Validate() != nil {
		return GitAdvancedReviewResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Git advanced review fields are invalid")
	}
	if isHunkOperation(request.Spec.Operation) && len(request.Spec.HunkIDs) == 0 {
		return GitAdvancedReviewResult{}, apperror.New(apperror.CodeInvalidArgument,
			"select exact hunk identities from discovery before requesting approval")
	}
	authority, err := s.loadMutationAuthority(ctx, request.RunID, request.Scope)
	if err != nil {
		return GitAdvancedReviewResult{}, err
	}
	preview, err := s.executor.ReviewAdvanced(ctx, authority.workspace.RootPath,
		request.Spec)
	if err != nil {
		return GitAdvancedReviewResult{}, gitAdvancedApplicationError(err)
	}
	if err := s.requireDurableTarget(ctx, authority, preview); err != nil {
		return GitAdvancedReviewResult{}, err
	}
	preview.PermissionSnapshotID = authority.permission.ID
	preview.PermissionRevision = authority.permission.Revision
	preview.LeaseID = authority.lease.LeaseID
	preview.LeaseGeneration = authority.lease.Generation
	preview.ApprovalFingerprint, err = s.operationApprovalFingerprint(ctx, authority, preview, true)
	if err != nil {
		return GitAdvancedReviewResult{}, err
	}
	if !preview.Executable() {
		return GitAdvancedReviewResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
			RunID: authority.run.ID, WorkspaceID: authority.workspace.ID,
			Preview: preview}, nil
	}
	specJSON, _ := json.Marshal(request.Spec)
	previewJSON, err := json.Marshal(preview)
	if s.operatorThreadID != "" && s.operatorThreadPreviewFingerprint != "" {
		previewJSON, err = json.Marshal(threadGitWorktreeIntent{Preview: preview, ThreadPreviewFingerprint: s.operatorThreadPreviewFingerprint})
	}
	if err != nil {
		return GitAdvancedReviewResult{}, err
	}
	operationKey := runmutation.OperationKeyDigest("git_advanced_operation.v1",
		authority.run.ID, request.OperationKey)
	record := gitadvanced.OperationRecord{ID: idgen.New("git-advanced"),
		ProtocolVersion: gitadvanced.ProtocolVersion, OperationKeySHA256: operationKey,
		RequestFingerprint: gitadvanced.Fingerprint("operation-request", authority.run.ID,
			authority.workspace.ID, preview.ID, preview.ApprovalFingerprint,
			request.RequestedBy),
		PreviewID: preview.ID, ApprovalFingerprint: preview.ApprovalFingerprint,
		RunID: authority.run.ID, SessionID: authority.session.ID,
		WorkspaceID: authority.workspace.ID, Operation: request.Spec.Operation,
		SpecJSON: string(specJSON), PreviewJSON: string(previewJSON),
		RepositorySHA256:     preview.Binding.RepositorySHA256,
		CommonDirSHA256:      preview.Binding.CommonDirSHA256,
		PermissionSnapshotID: authority.permission.ID,
		PermissionRevision:   authority.permission.Revision,
		CapabilityGeneration: preview.Capability.Generation,
		LeaseID:              authority.lease.LeaseID, LeaseGeneration: authority.lease.Generation,
		Status: gitadvanced.OperationProposed, ReceiptJSON: "{}", CreatedAt: s.now().UTC()}
	if s.operatorThreadID != "" {
		record.ID = "thread-git-worktree-" + request.OperationKey
	}
	record, replayed, err := s.store.CreateGitAdvancedOperation(ctx, record)
	if err != nil {
		return GitAdvancedReviewResult{}, apperror.Normalize(err)
	}
	approvalRecord, err := s.store.EnsureApproval(ctx, approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey(gitadvanced.ApprovalToolName,
			record.ID),
		ProposalID: record.ID, SessionID: authority.session.ID,
		WorkspaceID: authority.workspace.ID, ToolName: gitadvanced.ApprovalToolName,
		ActionClass: gitadvanced.ApprovalActionClass, Mode: "per_call",
		Status: approval.StatusPending, RequestFingerprint: record.ApprovalFingerprint,
		RequestedBy: request.RequestedBy, CreatedAt: record.CreatedAt,
		UpdatedAt: record.CreatedAt,
	})
	if err != nil {
		return GitAdvancedReviewResult{}, apperror.Normalize(err)
	}
	if approvalRecord.ProposalID != record.ID || approvalRecord.RunID != authority.run.ID ||
		approvalRecord.Status == approval.StatusDenied ||
		approvalRecord.RequestFingerprint != record.ApprovalFingerprint {
		return GitAdvancedReviewResult{}, apperror.New(apperror.CodeConflict,
			"Git advanced approval binding is invalid or denied")
	}
	return GitAdvancedReviewResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: authority.run.ID, WorkspaceID: authority.workspace.ID,
		Preview: preview, Operation: &record, Approval: &approvalRecord,
		Replayed: replayed}, nil
}

func (s *GitAdvancedService) Execute(ctx context.Context,
	request GitAdvancedExecuteRequest,
) (GitAdvancedExecuteResult, error) {
	if s == nil || s.store == nil || s.executor == nil || s.checkpoints == nil ||
		request.ProtocolVersion != GitAdvancedAPIProtocolVersion {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Git advanced execution request is invalid")
	}
	normalizeGitAdvancedExecuteRequest(&request)
	if err := s.resolveGitAdvancedLeaseID(ctx, request.RunID, &request.Scope); err != nil {
		return GitAdvancedExecuteResult{}, err
	}
	if request.RunID == "" || request.OperationID == "" || request.ApprovalID == "" ||
		request.RequestedBy == "" || request.Scope.CapabilityGeneration == "" ||
		request.Scope.LeaseID == "" || request.Scope.LeaseGeneration <= 0 {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Git advanced execution fields are invalid")
	}
	record, found, err := s.store.GetGitAdvancedOperation(ctx, request.OperationID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "Git advanced operation was not found")
		}
		return GitAdvancedExecuteResult{}, apperror.Normalize(err)
	}
	if record.RunID != request.RunID || record.ApprovalID != "" &&
		record.ApprovalID != request.ApprovalID {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced operation does not belong to this exact request")
	}
	if record.Status.Terminal() {
		return s.replayGitAdvancedExecution(ctx, request, record)
	}
	if record.Status == gitadvanced.OperationRunning {
		return GitAdvancedExecuteResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
				Operation: record, Replayed: true}, apperror.New(apperror.CodeFailedPrecondition,
				"Git advanced operation was interrupted after execution began; reconcile or recover it instead of replaying")
	}
	authority, err := s.loadMutationAuthority(ctx, request.RunID, request.Scope)
	if err != nil {
		return GitAdvancedExecuteResult{}, err
	}
	if record.SessionID != authority.session.ID || record.WorkspaceID != authority.workspace.ID ||
		record.PermissionSnapshotID != authority.permission.ID ||
		record.PermissionRevision != authority.permission.Revision ||
		record.CapabilityGeneration != request.Scope.CapabilityGeneration ||
		record.LeaseID != authority.lease.LeaseID ||
		record.LeaseGeneration != authority.lease.Generation {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeConflict,
			"Git advanced authority changed after review")
	}
	approvalRecord, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return GitAdvancedExecuteResult{}, apperror.Normalize(err)
	}
	if approvalRecord.Status != approval.StatusApproved || !gitAdvancedApprovalMatches(record, approvalRecord) {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced execution requires exact one-time approval")
	}
	var preview gitadvanced.Preview
	if json.Unmarshal([]byte(record.PreviewJSON), &preview) != nil ||
		preview.ID != record.PreviewID || preview.ApprovalFingerprint != record.ApprovalFingerprint {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"stored Git advanced preview is invalid")
	}
	if err := s.requireDurableTarget(ctx, authority, preview); err != nil {
		return GitAdvancedExecuteResult{}, err
	}
	guard, err := s.operationDispatchGuard(ctx, request, record, preview)
	if err != nil {
		return GitAdvancedExecuteResult{}, err
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: authority.run.ID,
		Kind:         workspacecheckpoint.TransactionGitMutation,
		OperationKey: record.OperationKeySHA256, TriggerReceiptID: record.ID,
		CapabilityGeneration: record.CapabilityGeneration,
		LeaseID:              record.LeaseID, LeaseGeneration: record.LeaseGeneration,
		IncompleteReasons: advancedCheckpointLimitations(record.Operation)}
	boundary, err := s.checkpoints.BeginBoundary(ctx, boundaryRequest)
	if err != nil {
		return GitAdvancedExecuteResult{}, err
	}
	var startReplayed bool
	record, startReplayed, err = s.store.StartGitAdvancedOperation(ctx, record.ID,
		approvalRecord.ID, record.ApprovalFingerprint, s.now().UTC())
	if err != nil {
		boundary, boundaryErr := s.checkpoints.CompleteBoundary(context.WithoutCancel(ctx),
			boundaryRequest, err)
		return GitAdvancedExecuteResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
				Operation: record, Boundary: boundary}, errors.Join(err,
				apperror.Normalize(boundaryErr))
	}
	if startReplayed {
		if record.Status.Terminal() {
			return s.replayGitAdvancedExecution(ctx, request, record)
		}
		return GitAdvancedExecuteResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
				Operation: record, Boundary: boundary, Replayed: true},
			apperror.New(apperror.CodeFailedPrecondition,
				"Git advanced operation already began; reconcile or await its terminal receipt instead of replaying it")
	}
	receipt, executeErr := s.executor.ExecuteAdvanced(ctx,
		authority.workspace.RootPath, preview, guard)
	receipt.CheckpointID = boundary.Before.ID
	sequence, worktree, stateErr := s.persistGitAdvancedState(context.WithoutCancel(ctx),
		authority, record, preview, receipt)
	if stateErr != nil {
		if executeErr == nil {
			executeErr = stateErr
		}
		receipt.Status = gitadvanced.ReceiptFailed
		receipt.ErrorCode = gitadvanced.FailureGit
		receipt.ErrorSummary = "Git changed but durable recovery state failed: " + stateErr.Error()
		if receipt.CompletedAt.IsZero() {
			receipt.CompletedAt = s.now().UTC()
		}
	}
	completed, _, completeErr := s.store.CompleteGitAdvancedOperation(
		context.WithoutCancel(ctx), record.ID, receipt, s.now().UTC())
	boundary, boundaryErr := s.checkpoints.CompleteBoundary(context.WithoutCancel(ctx),
		boundaryRequest, executeErr)
	if worktree != nil {
		worktree.Path = ""
	}
	result := GitAdvancedExecuteResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		Operation: completed, Receipt: receipt, Sequence: sequence, Worktree: worktree,
		Boundary: boundary}
	if executeErr != nil || completeErr != nil || boundaryErr != nil {
		return result, errors.Join(gitAdvancedApplicationError(executeErr),
			apperror.Normalize(completeErr), apperror.Normalize(boundaryErr))
	}
	return result, nil
}

func (s *GitAdvancedService) Projection(ctx context.Context, runID string,
	limit int,
) (GitAdvancedProjection, error) {
	authority, err := s.loadReadBinding(ctx, strings.TrimSpace(runID))
	if err != nil {
		return GitAdvancedProjection{}, err
	}
	authorityView, err := s.gitAdvancedAuthorityView(ctx, authority)
	if err != nil {
		return GitAdvancedProjection{}, err
	}
	observation, err := s.executor.InspectAdvancedSequence(ctx, authority.workspace.RootPath)
	if err != nil {
		return GitAdvancedProjection{}, gitAdvancedApplicationError(err)
	}
	sequence, found, err := s.store.GetActiveGitAdvancedSequence(ctx,
		observation.Binding.RepositorySHA256)
	if err != nil {
		return GitAdvancedProjection{}, apperror.Normalize(err)
	}
	var sequencePointer *gitadvanced.Sequence
	if found && sequence.RunID == authority.run.ID &&
		sequence.WorkspaceID == authority.workspace.ID {
		sequencePointer = &sequence
	}
	worktrees, err := s.store.ListManagedGitWorktrees(ctx, authority.run.ID,
		observation.Binding.RepositorySHA256, true, limit)
	if err != nil {
		return GitAdvancedProjection{}, apperror.Normalize(err)
	}
	if worktrees == nil {
		worktrees = []gitadvanced.ManagedWorktree{}
	}
	stashes, err := s.executor.ListAdvancedStashes(ctx, authority.workspace.RootPath, 100)
	if err != nil {
		return GitAdvancedProjection{}, gitAdvancedApplicationError(err)
	}
	if stashes == nil {
		stashes = []gitadvanced.StashEntry{}
	}
	for index := range worktrees {
		worktrees[index].Path = "" // raw host paths never cross the projection boundary.
	}
	records, err := s.store.ListGitAdvancedOperations(ctx,
		gitadvanced.OperationListFilter{RunID: authority.run.ID, Limit: limit})
	if err != nil {
		return GitAdvancedProjection{}, apperror.Normalize(err)
	}
	operations := make([]GitAdvancedOperationView, 0, len(records))
	for _, record := range records {
		view, viewErr := gitAdvancedOperationView(record)
		if viewErr != nil {
			return GitAdvancedProjection{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"Git advanced audit evidence is invalid", viewErr)
		}
		operations = append(operations, view)
	}
	return GitAdvancedProjection{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: authority.run.ID, WorkspaceID: authority.workspace.ID,
		Authority:  authorityView,
		Capability: s.executor.Capability(), Binding: observation.Binding,
		Conflict: observation.Conflict, Stashes: stashes, Sequence: sequencePointer,
		Worktrees: worktrees, Operations: operations}, nil
}

func (s *GitAdvancedService) gitAdvancedAuthorityView(ctx context.Context,
	value gitAdvancedAuthority,
) (GitAdvancedAuthorityView, error) {
	view := GitAdvancedAuthorityView{ProtocolVersion: "git-advanced-authority.v1",
		Scope: GitAdvancedScope{CapabilityGeneration: s.executor.Capability().Generation}}
	var err error
	if value.mode, err = s.store.GetRunMode(ctx, value.run.ID); err != nil {
		return view, apperror.Normalize(err)
	}
	if value.profile, err = s.store.GetRunExecutionProfile(ctx, value.run.ID); err != nil {
		return view, apperror.Normalize(err)
	}
	if value.permission, err = s.store.GetRunExecutionPermission(ctx, value.run.ID); err != nil {
		return view, apperror.Normalize(err)
	}
	var found bool
	if value.lease, found, err = s.store.GetRunExecutionLease(ctx, value.run.ID); err != nil {
		return view, apperror.Normalize(err)
	}
	view.PermissionSnapshotID = value.permission.ID
	view.PermissionRevision = value.permission.Revision
	if found {
		view.Scope.LeaseGeneration = value.lease.Generation
		view.LeaseExpiresAt = value.lease.ExpiresAt
		view.LeaseActive = value.lease.Status == domain.RunExecutionLeaseActive &&
			value.lease.ExpiresAt.After(s.now().UTC())
	}
	permissionAvailable := value.permission.Mode.IsApprovalMode() && value.permission.Validate() == nil &&
		s.permissionCapabilities.OperatorApprovalEnabled && s.permissionCapabilities.RuntimeAuthority != nil
	view.Executable = permissionAvailable && view.LeaseActive && value.run.Status == domain.RunRunning &&
		value.mode.RunID == value.run.ID && value.mode.MissionID == value.mission.ID &&
		value.mode.Surface == domain.ExecutionSurfaceCode &&
		value.mode.Phase == domain.ExecutionPhaseDeliver &&
		value.profile.RunID == value.run.ID && value.profile.MissionID == value.mission.ID &&
		value.profile.Profile == domain.RunExecutionProfileLocal &&
		value.profile.NetworkScope == domain.ExecutionNetworkDisabled &&
		value.permission.RunID == value.run.ID && value.permission.MissionID == value.mission.ID &&
		s.permissionCapabilities.AllowsSnapshot(value.permission)
	return view, nil
}

func gitAdvancedOperationView(record gitadvanced.OperationRecord) (GitAdvancedOperationView, error) {
	var preview gitadvanced.Preview
	if err := json.Unmarshal([]byte(record.PreviewJSON), &preview); err != nil ||
		preview.ID != record.PreviewID || preview.Operation != record.Operation {
		if err == nil {
			err = errors.New("preview binding does not match its operation")
		}
		return GitAdvancedOperationView{}, err
	}
	view := GitAdvancedOperationView{ID: record.ID, ProtocolVersion: record.ProtocolVersion,
		OperationKeySHA256: record.OperationKeySHA256,
		RequestFingerprint: record.RequestFingerprint, PreviewID: record.PreviewID,
		ApprovalFingerprint: record.ApprovalFingerprint, ApprovalID: record.ApprovalID,
		Operation: record.Operation, Preview: preview,
		RepositorySHA256:     record.RepositorySHA256,
		CommonDirSHA256:      record.CommonDirSHA256,
		PermissionSnapshotID: record.PermissionSnapshotID,
		PermissionRevision:   record.PermissionRevision,
		CapabilityGeneration: record.CapabilityGeneration,
		LeaseGeneration:      record.LeaseGeneration, Status: record.Status,
		ErrorCode: record.ErrorCode, CreatedAt: record.CreatedAt,
		StartedAt: record.StartedAt, CompletedAt: record.CompletedAt}
	if record.Status.Terminal() {
		var receipt gitadvanced.Receipt
		if err := json.Unmarshal([]byte(record.ReceiptJSON), &receipt); err != nil ||
			receipt.PreviewID != record.PreviewID || receipt.Operation != record.Operation {
			if err == nil {
				err = errors.New("receipt binding does not match its operation")
			}
			return GitAdvancedOperationView{}, err
		}
		view.Receipt = &receipt
	}
	return view, nil
}

// ReconcileStartup terminalizes every operation whose Git process may have
// been interrupted. It never replays a command. Observable sequencer state is
// persisted first, the prepared Workspace checkpoint boundary is completed,
// and an explicit interrupted/conflicted receipt prevents duplicate commits.
func (s *GitAdvancedService) ReconcileStartup(ctx context.Context,
	limit int,
) (GitAdvancedReconcileResult, error) {
	result := GitAdvancedReconcileResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		OperationIDs: []string{}}
	if s == nil || s.store == nil || s.executor == nil || s.checkpoints == nil ||
		limit < 1 || limit > 500 {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"Git advanced startup reconciliation limit is invalid")
	}
	records, err := s.store.ListGitAdvancedOperations(ctx,
		gitadvanced.OperationListFilter{Status: gitadvanced.OperationRunning, Limit: limit})
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.Examined = len(records)
	for _, record := range records {
		if err := s.reconcileInterruptedGitAdvancedOperation(ctx, record, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *GitAdvancedService) reconcileInterruptedGitAdvancedOperation(ctx context.Context,
	record gitadvanced.OperationRecord, result *GitAdvancedReconcileResult,
) error {
	workspace, err := s.store.GetWorkspaceByID(ctx, record.WorkspaceID)
	if err != nil {
		return apperror.Normalize(err)
	}
	var preview gitadvanced.Preview
	if json.Unmarshal([]byte(record.PreviewJSON), &preview) != nil ||
		preview.ID != record.PreviewID || preview.Operation != record.Operation ||
		preview.Binding.RepositorySHA256 != record.RepositorySHA256 ||
		preview.Binding.CommonDirSHA256 != record.CommonDirSHA256 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"interrupted Git advanced operation has invalid preview evidence")
	}
	observation, err := s.executor.InspectAdvancedSequence(ctx, workspace.RootPath)
	if err != nil {
		return gitAdvancedApplicationError(err)
	}
	now := s.now().UTC()
	started := record.CreatedAt
	if record.StartedAt != nil {
		started = *record.StartedAt
	}
	receipt := gitadvanced.Receipt{ProtocolVersion: gitadvanced.ReceiptProtocolVersion,
		ID:        "gar-" + gitadvanced.Fingerprint("receipt", preview.ID)[:32],
		PreviewID: preview.ID, Operation: record.Operation,
		Status: gitadvanced.ReceiptFailed, PreBinding: preview.Binding,
		PostBinding: observation.Binding, Conflict: observation.Conflict,
		ErrorCode:    gitadvanced.FailureInterrupted,
		ErrorSummary: "process restart interrupted the exact Git operation; it was not replayed",
		StartedAt:    started, CompletedAt: now}
	if kind, sequenceID, sequenceOperation := interruptedSequenceIdentity(record.Operation,
		preview); sequenceOperation {
		receipt.SequenceID = sequenceID
		if observation.Binding.RepositorySHA256 == record.RepositorySHA256 &&
			(!observation.Active || observation.Kind == kind) {
			if observation.Active {
				if observation.Conflict.Active {
					receipt.Status = gitadvanced.ReceiptConflicted
					receipt.ErrorCode = gitadvanced.FailureConflict
					receipt.ErrorSummary = "process restart found the exact Git sequence paused with conflicts; continue, skip, or abort remains available"
					result.Conflicted++
				} else {
					receipt.ErrorSummary = "process restart found the exact Git sequence still active; explicit continue, skip, or abort is required"
				}
			} else {
				receipt.ErrorSummary = "process restart found no active Git sequencer; the command outcome cannot be proven and the durable sequence was terminalized as failed"
			}
			if err := s.persistInterruptedGitAdvancedSequence(ctx, record, preview,
				observation, kind, sequenceID); err != nil {
				return apperror.Wrap(apperror.CodeUnavailable,
					"persist interrupted Git sequence", err)
			}
		}
	}
	if strings.HasPrefix(string(record.Operation), "worktree_") {
		if err := s.persistInterruptedManagedGitWorktree(ctx, workspace.RootPath,
			record, preview, &receipt); err != nil {
			return apperror.Wrap(apperror.CodeUnavailable,
				"persist interrupted managed Git worktree", err)
		}
		if receipt.WorktreeID != "" {
			receipt.ErrorSummary += "; exact managed worktree state was recovered into the durable registry"
		}
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: record.RunID,
		Kind:         workspacecheckpoint.TransactionGitMutation,
		OperationKey: record.OperationKeySHA256, TriggerReceiptID: record.ID,
		CapabilityGeneration: record.CapabilityGeneration,
		LeaseID:              record.LeaseID, LeaseGeneration: record.LeaseGeneration,
		IncompleteReasons: advancedCheckpointLimitations(record.Operation)}
	boundary, boundaryErr := s.checkpoints.CompleteBoundary(ctx, boundaryRequest,
		apperror.New(apperror.CodeUnavailable, receipt.ErrorSummary))
	if boundaryErr != nil {
		return apperror.Wrap(apperror.CodeUnavailable,
			"complete interrupted Git checkpoint boundary", boundaryErr)
	}
	receipt.CheckpointID = boundary.Before.ID
	if _, _, err := s.store.CompleteGitAdvancedOperation(ctx, record.ID, receipt, now); err != nil {
		return apperror.Normalize(err)
	}
	result.Recovered++
	if receipt.Status == gitadvanced.ReceiptFailed {
		result.Failed++
	}
	result.OperationIDs = append(result.OperationIDs, record.ID)
	return nil
}

func (s *GitAdvancedService) persistInterruptedGitAdvancedSequence(ctx context.Context,
	record gitadvanced.OperationRecord, preview gitadvanced.Preview,
	observation repository.AdvancedSequenceObservation, kind gitadvanced.SequenceKind,
	sequenceID string,
) error {
	now := s.now().UTC()
	conflictJSON, _ := json.Marshal(observation.Conflict)
	status := gitadvanced.SequenceFailed
	completedAt := &now
	if observation.Active {
		status = sequenceStatusFromObservation(observation)
		completedAt = nil
	}
	if isSequenceStart(record.Operation) {
		createdAt := record.CreatedAt
		if record.StartedAt != nil {
			createdAt = *record.StartedAt
		}
		_, _, err := s.store.CreateGitAdvancedSequence(ctx, gitadvanced.Sequence{
			ID: sequenceID, ProtocolVersion: gitadvanced.SequenceProtocolVersion,
			RunID: record.RunID, WorkspaceID: record.WorkspaceID, Kind: kind,
			Status:           status,
			RepositorySHA256: record.RepositorySHA256,
			OriginalHead:     preview.Binding.Head, OriginalBranch: preview.Binding.Branch,
			TargetJSON: record.SpecJSON, SequencerSHA256: observation.Binding.SequenceSHA256,
			CurrentHead: observation.Binding.Head, ConflictJSON: string(conflictJSON),
			Generation: 1, StartedOperationID: record.ID, LastOperationID: record.ID,
			CreatedAt: createdAt, UpdatedAt: now, CompletedAt: completedAt,
		})
		return err
	}
	value, found, err := s.store.GetGitAdvancedSequence(ctx, sequenceID)
	if err != nil {
		return err
	}
	if !found || value.RepositorySHA256 != record.RepositorySHA256 || value.Kind != kind {
		return errors.New("interrupted Git sequence has no matching durable state")
	}
	if value.LastOperationID == record.ID {
		expectedStatus := gitadvanced.SequenceFailed
		if observation.Active {
			expectedStatus = sequenceStatusFromObservation(observation)
		}
		if value.CurrentHead != observation.Binding.Head ||
			value.SequencerSHA256 != observation.Binding.SequenceSHA256 ||
			(observation.Active && value.Status != expectedStatus) ||
			(!observation.Active && !value.Status.Terminal()) {
			return errors.New("already-persisted interrupted Git sequence changed before reconciliation")
		}
		return nil
	}
	if value.Status.Terminal() {
		return errors.New("interrupted Git sequence has no matching active durable state")
	}
	value.Status = status
	value.SequencerSHA256 = observation.Binding.SequenceSHA256
	value.CurrentHead = observation.Binding.Head
	value.ConflictJSON = string(conflictJSON)
	value.Generation++
	value.LastOperationID = record.ID
	value.UpdatedAt = now
	value.CompletedAt = completedAt
	_, _, err = s.store.AdvanceGitAdvancedSequence(ctx, value, value.Generation-1)
	return err
}

func (s *GitAdvancedService) persistInterruptedManagedGitWorktree(ctx context.Context,
	root string, record gitadvanced.OperationRecord, preview gitadvanced.Preview,
	receipt *gitadvanced.Receipt,
) error {
	spec, now := preview.Spec, s.now().UTC()
	if record.Operation == gitadvanced.WorktreeCreate {
		observation, err := s.executor.InspectManagedWorktree(ctx, root,
			preview.Binding, spec.WorktreeName)
		if err != nil {
			return err
		}
		if !observation.Found || !observation.Present || observation.Prunable ||
			observation.Detached || observation.Locked || !observation.Clean ||
			observation.Head != spec.Commit || observation.Binding.Head != spec.Commit ||
			observation.Branch != spec.Branch || observation.Binding.Branch != spec.Branch ||
			observation.Binding.CommonDirSHA256 != preview.Binding.CommonDirSHA256 {
			return nil
		}
		worktreeID := "gwt-" + gitadvanced.Fingerprint("worktree", preview.ID)[:32]
		createdAt := record.CreatedAt
		if record.StartedAt != nil {
			createdAt = *record.StartedAt
		}
		value := gitadvanced.ManagedWorktree{ID: worktreeID,
			ProtocolVersion: gitadvanced.WorktreeProtocolVersion,
			RunID:           record.RunID, WorkspaceID: record.WorkspaceID,
			RepositorySHA256: record.RepositorySHA256,
			CommonDirSHA256:  preview.Binding.CommonDirSHA256, Name: spec.WorktreeName,
			Path:       observation.Path,
			PathSHA256: gitadvanced.Fingerprint("managed-worktree-path", observation.Path),
			Branch:     spec.Branch, Head: spec.Commit, Present: true, Generation: 1,
			CreatedOperationID: record.ID, LastOperationID: record.ID,
			CreatedAt: createdAt, UpdatedAt: now}
		stored, _, err := s.store.CreateManagedGitWorktree(ctx, value)
		if err != nil {
			return err
		}
		if stored.RunID != value.RunID || stored.WorkspaceID != value.WorkspaceID ||
			stored.RepositorySHA256 != value.RepositorySHA256 ||
			stored.CommonDirSHA256 != value.CommonDirSHA256 || stored.Name != value.Name ||
			stored.PathSHA256 != value.PathSHA256 || stored.Branch != value.Branch ||
			stored.Head != value.Head || !stored.Present {
			return errors.New("recovered managed Git worktree registry binding changed")
		}
		receipt.WorktreeID = worktreeID
		return nil
	}

	if record.Operation == gitadvanced.WorktreePrune {
		values, err := s.store.ListManagedGitWorktrees(ctx, record.RunID,
			record.RepositorySHA256, false, 500)
		if err != nil {
			return err
		}
		for _, value := range values {
			if value.CommonDirSHA256 != preview.Binding.CommonDirSHA256 {
				continue
			}
			observation, inspectErr := s.executor.InspectManagedWorktree(ctx, root,
				preview.Binding, value.Name)
			if inspectErr != nil {
				return inspectErr
			}
			if observation.Found {
				continue
			}
			value.Generation++
			value.Present, value.Locked, value.LockReason = false, false, ""
			value.LastOperationID, value.UpdatedAt, value.RemovedAt = record.ID, now, &now
			if _, _, err := s.store.AdvanceManagedGitWorktree(ctx, value,
				value.Generation-1); err != nil {
				return err
			}
		}
		return nil
	}

	value, found, err := s.store.GetManagedGitWorktree(ctx, spec.WorktreeID)
	if err != nil {
		return err
	}
	persistedRemoval := record.Operation == gitadvanced.WorktreeRemove &&
		value.LastOperationID == record.ID && !value.Present
	if !found || (!value.Present && !persistedRemoval) || value.ID != spec.WorktreeID ||
		value.RunID != record.RunID ||
		value.WorkspaceID != record.WorkspaceID ||
		value.RepositorySHA256 != record.RepositorySHA256 ||
		value.CommonDirSHA256 != preview.Binding.CommonDirSHA256 ||
		value.Name != spec.WorktreeName {
		return errors.New("interrupted managed Git worktree has no matching durable state")
	}
	observation, err := s.executor.InspectManagedWorktree(ctx, root,
		preview.Binding, spec.WorktreeName)
	if err != nil {
		return err
	}
	if observation.Path != value.Path || gitadvanced.Fingerprint("managed-worktree-path",
		observation.Path) != value.PathSHA256 {
		return errors.New("interrupted managed Git worktree target changed")
	}
	desired := false
	switch record.Operation {
	case gitadvanced.WorktreeLock:
		desired = observation.Present && observation.Locked &&
			observation.LockReason == spec.LockReason
	case gitadvanced.WorktreeUnlock:
		desired = observation.Present && !observation.Locked
	case gitadvanced.WorktreeRemove:
		desired = !observation.Found
	}
	if !desired {
		return nil
	}
	if observation.Present && (observation.Prunable || observation.Detached ||
		observation.Head != value.Head || observation.Binding.Head != value.Head ||
		observation.Branch != value.Branch || observation.Binding.Branch != value.Branch ||
		observation.Binding.CommonDirSHA256 != value.CommonDirSHA256 ||
		observation.Path != value.Path || gitadvanced.Fingerprint("managed-worktree-path",
		observation.Path) != value.PathSHA256) {
		return nil
	}
	if value.LastOperationID == record.ID {
		receipt.WorktreeID = value.ID
		return nil
	}
	value.Generation++
	value.LastOperationID, value.UpdatedAt = record.ID, now
	switch record.Operation {
	case gitadvanced.WorktreeLock:
		value.Locked, value.LockReason = true, spec.LockReason
	case gitadvanced.WorktreeUnlock:
		value.Locked, value.LockReason = false, ""
	case gitadvanced.WorktreeRemove:
		value.Present, value.Locked, value.LockReason, value.RemovedAt =
			false, false, "", &now
	default:
		return nil
	}
	if _, _, err := s.store.AdvanceManagedGitWorktree(ctx, value,
		value.Generation-1); err != nil {
		return err
	}
	receipt.WorktreeID = value.ID
	return nil
}

func interruptedSequenceIdentity(operation gitadvanced.Operation,
	preview gitadvanced.Preview,
) (gitadvanced.SequenceKind, string, bool) {
	kind := gitadvanced.SequenceRebase
	switch {
	case operation == gitadvanced.RebaseStart || strings.HasPrefix(string(operation), "rebase_"):
		kind = gitadvanced.SequenceRebase
	case operation == gitadvanced.CherryPickStart || strings.HasPrefix(string(operation), "cherry_pick_"):
		kind = gitadvanced.SequenceCherryPick
	case operation == gitadvanced.BisectStart || strings.HasPrefix(string(operation), "bisect_"):
		kind = gitadvanced.SequenceBisect
	default:
		return "", "", false
	}
	if isSequenceStart(operation) {
		return kind, gitadvanced.SequenceIDForPreview(preview.ID, kind), true
	}
	return kind, preview.Spec.SequenceID, true
}

func sequenceStatusFromObservation(observation repository.AdvancedSequenceObservation) gitadvanced.SequenceStatus {
	if observation.Conflict.Active {
		return gitadvanced.SequenceConflicted
	}
	return gitadvanced.SequenceActive
}

func (s *GitAdvancedService) loadReadBinding(ctx context.Context,
	runID string,
) (gitAdvancedAuthority, error) {
	if s == nil || s.store == nil || s.executor == nil || !s.executor.Available() {
		return gitAdvancedAuthority{}, apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced capability is unavailable")
	}
	var value gitAdvancedAuthority
	var err error
	if runID == "" {
		return value, apperror.New(apperror.CodeInvalidArgument, "Run id is required")
	}
	if value.run, err = s.store.GetRun(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.mission, err = s.store.GetMission(ctx, value.run.MissionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.session, err = s.store.GetSession(ctx, value.run.SessionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.workspace, err = s.store.GetWorkspaceByID(ctx,
		value.mission.WorkspaceID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.run.MissionID != value.mission.ID || value.run.SessionID != value.session.ID ||
		value.mission.WorkspaceID == "" || value.mission.WorkspaceID != value.workspace.ID ||
		value.session.WorkspaceID != value.workspace.ID || value.session.Status != session.StatusActive ||
		strings.TrimSpace(value.workspace.RootPath) == "" {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced Run and Workspace binding is invalid")
	}
	return value, nil
}

func (s *GitAdvancedService) loadMutationAuthority(ctx context.Context, runID string,
	scope GitAdvancedScope,
) (gitAdvancedAuthority, error) {
	value, err := s.loadReadBinding(ctx, runID)
	if err != nil {
		return value, err
	}
	if value.mode, err = s.store.GetRunMode(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.profile, err = s.store.GetRunExecutionProfile(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.permission, err = s.store.GetRunExecutionPermission(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	}
	var found bool
	if value.lease, found, err = s.store.GetRunExecutionLease(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	}
	capability := s.executor.Capability()
	operatorIdle := false
	if s.operatorThreadID != "" {
		checker, ok := s.store.(interface {
			CheckThreadGitIdle(context.Context, string, string, *domain.RunExecutionLease) error
		})
		if !ok || !strings.HasPrefix(value.lease.OwnerID, "thread-git:") {
			return value, apperror.New(apperror.CodeConflict, "operator Git lease is unavailable")
		}
		if err := checker.CheckThreadGitIdle(ctx, s.operatorThreadID, value.run.ID, &value.lease); err != nil {
			return value, err
		}
		operatorIdle = true
	}
	if !found || (!operatorIdle && value.run.Status != domain.RunRunning) ||
		value.mode.RunID != value.run.ID || value.mode.MissionID != value.mission.ID ||
		value.mode.Surface != domain.ExecutionSurfaceCode ||
		value.mode.Phase != domain.ExecutionPhaseDeliver ||
		value.profile.RunID != value.run.ID || value.profile.MissionID != value.mission.ID ||
		value.profile.Profile != domain.RunExecutionProfileLocal ||
		value.profile.NetworkScope != domain.ExecutionNetworkDisabled ||
		value.permission.RunID != value.run.ID ||
		value.permission.MissionID != value.mission.ID ||
		!value.permission.Mode.IsApprovalMode() || value.permission.Validate() != nil ||
		!s.permissionCapabilities.AllowsSnapshot(value.permission) ||
		capability.Generation != scope.CapabilityGeneration ||
		value.lease.Status != domain.RunExecutionLeaseActive ||
		value.lease.LeaseID != scope.LeaseID || value.lease.Generation != scope.LeaseGeneration ||
		!value.lease.ExpiresAt.After(s.now().UTC()) {
		return value, apperror.New(apperror.CodeConflict,
			"Git advanced permission, capability, or Workspace lease binding is stale")
	}
	// This is readiness to request a native preview, never permission to
	// mutate. Execute resolves the exact approval through the common policy
	// and the adapter rechecks it immediately before every native effect.
	return value, nil
}

func (s *GitAdvancedService) resolveGitAdvancedLeaseID(ctx context.Context, runID string,
	scope *GitAdvancedScope,
) error {
	if scope == nil || strings.TrimSpace(scope.LeaseID) != "" {
		return nil
	}
	lease, found, err := s.store.GetRunExecutionLease(ctx, strings.TrimSpace(runID))
	if err != nil {
		return apperror.Normalize(err)
	}
	if !found || lease.Status != domain.RunExecutionLeaseActive ||
		lease.Generation != scope.LeaseGeneration || !lease.ExpiresAt.After(s.now().UTC()) {
		return apperror.New(apperror.CodeConflict,
			"Git advanced Workspace lease binding is stale")
	}
	scope.LeaseID = lease.LeaseID
	return nil
}

func (s *GitAdvancedService) requireDurableTarget(ctx context.Context,
	authority gitAdvancedAuthority, preview gitadvanced.Preview,
) error {
	spec := preview.Spec
	if isSequenceControl(spec.Operation) {
		sequence, found, err := s.store.GetGitAdvancedSequence(ctx, spec.SequenceID)
		if err != nil {
			return apperror.Normalize(err)
		}
		expectedKind := gitadvanced.SequenceRebase
		if strings.HasPrefix(string(spec.Operation), "cherry_pick_") {
			expectedKind = gitadvanced.SequenceCherryPick
		} else if strings.HasPrefix(string(spec.Operation), "bisect_") {
			expectedKind = gitadvanced.SequenceBisect
		}
		if !found || sequence.RunID != authority.run.ID ||
			sequence.WorkspaceID != authority.workspace.ID || sequence.Kind != expectedKind ||
			sequence.RepositorySHA256 != preview.Binding.RepositorySHA256 ||
			sequence.SequencerSHA256 != preview.Binding.SequenceSHA256 ||
			sequence.CurrentHead != preview.Binding.Head || sequence.Status.Terminal() ||
			(sequence.Kind != gitadvanced.SequenceBisect &&
				protectedAdvancedBranch(sequence.OriginalBranch)) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Git sequence does not match an active durable Run binding")
		}
	}
	if spec.Operation == gitadvanced.WorktreeCreate {
		if _, found, err := s.store.GetManagedGitWorktreeByName(ctx,
			preview.Binding.CommonDirSHA256, spec.WorktreeName); err != nil {
			return apperror.Normalize(err)
		} else if found {
			return apperror.New(apperror.CodeConflict,
				"managed worktree names are never reused, including after removal")
		}
	}
	if spec.Operation == gitadvanced.WorktreeLock || spec.Operation == gitadvanced.WorktreeUnlock ||
		spec.Operation == gitadvanced.WorktreeRemove {
		worktree, found, err := s.store.GetManagedGitWorktree(ctx, spec.WorktreeID)
		if err != nil {
			return apperror.Normalize(err)
		}
		path, pathErr := s.executor.ManagedWorktreePath(preview.Binding, spec.WorktreeName)
		if pathErr != nil {
			return apperror.New(apperror.CodeFailedPrecondition,
				"managed worktree registry binding changed")
		}
		targetBinding, bindingErr := s.executor.CaptureAdvancedBinding(ctx, path)
		if !found || !worktree.Present ||
			bindingErr != nil || targetBinding.Head != worktree.Head ||
			targetBinding.Branch != worktree.Branch ||
			targetBinding.CommonDirSHA256 != worktree.CommonDirSHA256 ||
			worktree.RunID != authority.run.ID || worktree.WorkspaceID != authority.workspace.ID ||
			worktree.RepositorySHA256 != preview.Binding.RepositorySHA256 ||
			worktree.CommonDirSHA256 != preview.Binding.CommonDirSHA256 ||
			worktree.Name != spec.WorktreeName || worktree.Path != path ||
			worktree.PathSHA256 != gitadvanced.Fingerprint("managed-worktree-path", path) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"managed worktree registry binding changed")
		}
	}
	if spec.Operation == gitadvanced.WorktreePrune {
		candidates, err := s.executor.PrunableManagedWorktreePathSHA256(ctx,
			authority.workspace.RootPath, preview.Binding)
		if err != nil {
			return gitAdvancedApplicationError(err)
		}
		registered, err := s.store.ListManagedGitWorktrees(ctx, authority.run.ID,
			preview.Binding.RepositorySHA256, false, 500)
		if err != nil {
			return apperror.Normalize(err)
		}
		byPath := make(map[string]gitadvanced.ManagedWorktree, len(registered))
		for _, worktree := range registered {
			byPath[worktree.PathSHA256] = worktree
		}
		for _, pathSHA256 := range candidates {
			worktree, found := byPath[pathSHA256]
			if !found || !worktree.Present || worktree.CommonDirSHA256 !=
				preview.Binding.CommonDirSHA256 {
				return apperror.New(apperror.CodeFailedPrecondition,
					"worktree prune candidate is not in the exact product registry")
			}
		}
	}
	return nil
}

func (s *GitAdvancedService) persistGitAdvancedState(ctx context.Context,
	authority gitAdvancedAuthority, operation gitadvanced.OperationRecord,
	preview gitadvanced.Preview, receipt gitadvanced.Receipt,
) (*gitadvanced.Sequence, *gitadvanced.ManagedWorktree, error) {
	if isSequenceStart(operation.Operation) || isSequenceControl(operation.Operation) {
		// A missing sequence id proves the executor never entered the closed Git
		// command template (for example, exact preflight evidence drifted). There
		// is no sequencer state to create or advance in that case.
		if receipt.SequenceID == "" {
			return nil, nil, nil
		}
		value, err := s.persistGitAdvancedSequence(ctx, authority, operation, preview, receipt)
		return value, nil, err
	}
	if strings.HasPrefix(string(operation.Operation), "worktree_") {
		value, err := s.persistManagedGitWorktree(ctx, authority, operation, preview, receipt)
		return nil, value, err
	}
	return nil, nil, nil
}

func (s *GitAdvancedService) persistGitAdvancedSequence(ctx context.Context,
	authority gitAdvancedAuthority, operation gitadvanced.OperationRecord,
	preview gitadvanced.Preview, receipt gitadvanced.Receipt,
) (*gitadvanced.Sequence, error) {
	observation, err := s.executor.InspectAdvancedSequence(ctx, authority.workspace.RootPath)
	if err != nil {
		return nil, err
	}
	conflictJSON, _ := json.Marshal(observation.Conflict)
	now := s.now().UTC()
	if isSequenceStart(operation.Operation) {
		kind := gitadvanced.SequenceRebase
		if operation.Operation == gitadvanced.CherryPickStart {
			kind = gitadvanced.SequenceCherryPick
		} else if operation.Operation == gitadvanced.BisectStart {
			kind = gitadvanced.SequenceBisect
		}
		if receipt.SequenceID == "" {
			return nil, errors.New("Git sequence start produced no durable identity")
		}
		status := gitadvanced.SequenceCompleted
		var completedAt *time.Time
		if observation.Active {
			status = gitadvanced.SequenceActive
			if observation.Conflict.Active {
				status = gitadvanced.SequenceConflicted
			}
		} else {
			completedAt = &now
		}
		if receipt.Status == gitadvanced.ReceiptFailed && !observation.Active {
			status = gitadvanced.SequenceFailed
		}
		value := gitadvanced.Sequence{ID: receipt.SequenceID,
			ProtocolVersion: gitadvanced.SequenceProtocolVersion,
			RunID:           authority.run.ID, WorkspaceID: authority.workspace.ID, Kind: kind,
			Status: status, RepositorySHA256: preview.Binding.RepositorySHA256,
			OriginalHead: preview.Binding.Head, OriginalBranch: preview.Binding.Branch,
			TargetJSON: operation.SpecJSON, SequencerSHA256: observation.Binding.SequenceSHA256,
			CurrentHead: observation.Binding.Head, ConflictJSON: string(conflictJSON),
			Generation: 1, StartedOperationID: operation.ID, LastOperationID: operation.ID,
			CreatedAt: now, UpdatedAt: now, CompletedAt: completedAt}
		stored, _, err := s.store.CreateGitAdvancedSequence(ctx, value)
		return &stored, err
	}
	value, found, err := s.store.GetGitAdvancedSequence(ctx, preview.Spec.SequenceID)
	if err != nil || !found {
		return nil, errors.New("durable Git sequence disappeared during execution")
	}
	value.Generation++
	value.LastOperationID = operation.ID
	value.SequencerSHA256 = observation.Binding.SequenceSHA256
	value.CurrentHead = observation.Binding.Head
	value.ConflictJSON = string(conflictJSON)
	value.UpdatedAt = now
	if receipt.Status == gitadvanced.ReceiptConflicted || observation.Conflict.Active {
		value.Status = gitadvanced.SequenceConflicted
	} else if operation.Operation == gitadvanced.RebaseAbort ||
		operation.Operation == gitadvanced.CherryPickAbort ||
		operation.Operation == gitadvanced.BisectReset {
		value.Status, value.CompletedAt = gitadvanced.SequenceAborted, &now
	} else if !observation.Active {
		value.Status, value.CompletedAt = gitadvanced.SequenceCompleted, &now
	} else {
		value.Status = gitadvanced.SequenceActive
	}
	stored, _, err := s.store.AdvanceGitAdvancedSequence(ctx, value, value.Generation-1)
	return &stored, err
}

func (s *GitAdvancedService) persistManagedGitWorktree(ctx context.Context,
	authority gitAdvancedAuthority, operation gitadvanced.OperationRecord,
	preview gitadvanced.Preview, receipt gitadvanced.Receipt,
) (*gitadvanced.ManagedWorktree, error) {
	spec := preview.Spec
	if receipt.Status != gitadvanced.ReceiptSucceeded {
		return nil, nil
	}
	now := s.now().UTC()
	if operation.Operation == gitadvanced.WorktreeCreate {
		observation, err := s.executor.InspectManagedWorktree(ctx,
			authority.workspace.RootPath, preview.Binding, spec.WorktreeName)
		if err != nil {
			return nil, err
		}
		if !observation.Found || !observation.Present || observation.Prunable ||
			observation.Detached || observation.Locked || !observation.Clean ||
			observation.Head != spec.Commit || observation.Binding.Head != spec.Commit ||
			observation.Branch != spec.Branch || observation.Binding.Branch != spec.Branch ||
			observation.Binding.CommonDirSHA256 != preview.Binding.CommonDirSHA256 {
			return nil, errors.New("created managed Git worktree changed before durable registration")
		}
		value := gitadvanced.ManagedWorktree{ID: receipt.WorktreeID,
			ProtocolVersion: gitadvanced.WorktreeProtocolVersion,
			RunID:           authority.run.ID, WorkspaceID: authority.workspace.ID,
			RepositorySHA256: preview.Binding.RepositorySHA256,
			CommonDirSHA256:  preview.Binding.CommonDirSHA256, Name: spec.WorktreeName,
			Path: observation.Path, PathSHA256: gitadvanced.Fingerprint("managed-worktree-path",
				observation.Path),
			Branch: spec.Branch, Head: observation.Binding.Head, Present: true, Generation: 1,
			CreatedOperationID: operation.ID, LastOperationID: operation.ID,
			CreatedAt: now, UpdatedAt: now}
		stored, _, err := s.store.CreateManagedGitWorktree(ctx, value)
		return &stored, err
	}
	if operation.Operation == gitadvanced.WorktreePrune {
		values, err := s.store.ListManagedGitWorktrees(ctx, authority.run.ID,
			preview.Binding.RepositorySHA256, false, 500)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			if value.CommonDirSHA256 != preview.Binding.CommonDirSHA256 {
				continue
			}
			observation, inspectErr := s.executor.InspectManagedWorktree(ctx,
				authority.workspace.RootPath, preview.Binding, value.Name)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if observation.Found {
				continue
			}
			value.Generation++
			value.Present, value.Locked, value.LockReason = false, false, ""
			value.LastOperationID, value.UpdatedAt, value.RemovedAt = operation.ID, now, &now
			if _, _, err := s.store.AdvanceManagedGitWorktree(ctx, value,
				value.Generation-1); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	value, found, err := s.store.GetManagedGitWorktree(ctx, spec.WorktreeID)
	if err != nil || !found {
		return nil, errors.New("managed Git worktree disappeared during execution")
	}
	observation, err := s.executor.InspectManagedWorktree(ctx,
		authority.workspace.RootPath, preview.Binding, spec.WorktreeName)
	if err != nil {
		return nil, err
	}
	desired := false
	switch operation.Operation {
	case gitadvanced.WorktreeLock:
		desired = observation.Present && observation.Locked &&
			observation.LockReason == spec.LockReason
	case gitadvanced.WorktreeUnlock:
		desired = observation.Present && !observation.Locked
	case gitadvanced.WorktreeRemove:
		desired = !observation.Found
	}
	if !desired {
		return nil, errors.New("managed Git worktree changed before durable state persistence")
	}
	if observation.Present && (observation.Prunable || observation.Detached ||
		observation.Head != value.Head || observation.Binding.Head != value.Head ||
		observation.Branch != value.Branch || observation.Binding.Branch != value.Branch ||
		observation.Binding.CommonDirSHA256 != value.CommonDirSHA256 ||
		observation.Path != value.Path || gitadvanced.Fingerprint("managed-worktree-path",
		observation.Path) != value.PathSHA256) {
		return nil, errors.New("managed Git worktree identity drifted after execution")
	}
	value.Generation++
	value.LastOperationID, value.UpdatedAt = operation.ID, now
	switch operation.Operation {
	case gitadvanced.WorktreeLock:
		value.Locked, value.LockReason = true, spec.LockReason
	case gitadvanced.WorktreeUnlock:
		value.Locked, value.LockReason = false, ""
	case gitadvanced.WorktreeRemove:
		value.Present, value.Locked, value.LockReason, value.RemovedAt = false, false, "", &now
	default:
		return nil, errors.New("unsupported managed Git worktree transition")
	}
	if value.Present {
		value.Head = observation.Binding.Head
	}
	stored, _, err := s.store.AdvanceManagedGitWorktree(ctx, value, value.Generation-1)
	return &stored, err
}

func (s *GitAdvancedService) replayGitAdvancedExecution(ctx context.Context,
	request GitAdvancedExecuteRequest, record gitadvanced.OperationRecord,
) (GitAdvancedExecuteResult, error) {
	if record.ApprovalID != request.ApprovalID || record.ReceiptJSON == "" ||
		!json.Valid([]byte(record.ReceiptJSON)) {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeConflict,
			"Git advanced replay binding is invalid")
	}
	var receipt gitadvanced.Receipt
	if json.Unmarshal([]byte(record.ReceiptJSON), &receipt) != nil {
		return GitAdvancedExecuteResult{}, apperror.New(apperror.CodeInternal,
			"Git advanced receipt cannot be decoded")
	}
	result := GitAdvancedExecuteResult{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		Operation: record, Receipt: receipt, Replayed: true}
	if receipt.SequenceID != "" {
		if value, found, err := s.store.GetGitAdvancedSequence(ctx,
			receipt.SequenceID); err == nil && found {
			result.Sequence = &value
		}
	}
	if receipt.WorktreeID != "" {
		if value, found, err := s.store.GetManagedGitWorktree(ctx,
			receipt.WorktreeID); err == nil && found {
			value.Path = ""
			result.Worktree = &value
		}
	}
	return result, nil
}

func normalizeGitAdvancedReviewRequest(request *GitAdvancedReviewRequest) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.Scope.CapabilityGeneration = strings.TrimSpace(request.Scope.CapabilityGeneration)
	request.Scope.LeaseID = strings.TrimSpace(request.Scope.LeaseID)
}

func normalizeGitAdvancedExecuteRequest(request *GitAdvancedExecuteRequest) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.ApprovalID = strings.TrimSpace(request.ApprovalID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.Scope.CapabilityGeneration = strings.TrimSpace(request.Scope.CapabilityGeneration)
	request.Scope.LeaseID = strings.TrimSpace(request.Scope.LeaseID)
}

func isHunkOperation(operation gitadvanced.Operation) bool {
	return operation == gitadvanced.HunkStage || operation == gitadvanced.HunkUnstage ||
		operation == gitadvanced.HunkRevert
}

func isSequenceStart(operation gitadvanced.Operation) bool {
	return operation == gitadvanced.RebaseStart || operation == gitadvanced.CherryPickStart ||
		operation == gitadvanced.BisectStart
}

func isSequenceControl(operation gitadvanced.Operation) bool {
	return (strings.HasPrefix(string(operation), "rebase_") ||
		strings.HasPrefix(string(operation), "cherry_pick_") ||
		strings.HasPrefix(string(operation), "bisect_")) && !isSequenceStart(operation)
}

func protectedAdvancedBranch(branch string) bool {
	branch = strings.ToLower(strings.TrimSpace(branch))
	return branch == "main" || branch == "master" || branch == "trunk" ||
		branch == "production" || branch == "prod" || branch == "release" ||
		strings.HasPrefix(branch, "release/")
}

func advancedCheckpointLimitations(operation gitadvanced.Operation) []string {
	var reasons []string
	switch operation {
	case gitadvanced.StashDrop:
		reasons = append(reasons, "Workspace Checkpoint does not archive deleted refs/stash objects")
	case gitadvanced.WorktreeRemove, gitadvanced.WorktreePrune:
		reasons = append(reasons,
			"Workspace Checkpoint covers the registered Workspace, not an external managed worktree root")
	case gitadvanced.RebaseStart, gitadvanced.RebaseContinue, gitadvanced.RebaseSkip,
		gitadvanced.RebaseAbort, gitadvanced.CherryPickStart,
		gitadvanced.CherryPickContinue, gitadvanced.CherryPickSkip,
		gitadvanced.CherryPickAbort, gitadvanced.BisectStart, gitadvanced.BisectGood,
		gitadvanced.BisectBad, gitadvanced.BisectSkip, gitadvanced.BisectRun,
		gitadvanced.BisectReset:
		reasons = append(reasons,
			"Workspace Checkpoint restores files and index; Git ref/sequencer recovery also requires the durable Git receipt")
	}
	return reasons
}

func gitAdvancedApplicationError(err error) error {
	if err == nil {
		return nil
	}
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) {
		return apperror.Normalize(err)
	}
	code := apperror.CodeFailedPrecondition
	switch advancedErr.Code {
	case gitadvanced.FailureCapabilityDisabled, gitadvanced.FailureApprovalRequired,
		gitadvanced.FailureBranchProtected, gitadvanced.FailureUnsafeRepository,
		gitadvanced.FailureOutsideManagedRoot:
		code = apperror.CodePolicyDenied
	case gitadvanced.FailureTimeout:
		code = apperror.CodeDeadlineExceeded
	case gitadvanced.FailureBudgetExceeded:
		code = apperror.CodeResourceExhausted
	case gitadvanced.FailureStalePreview, gitadvanced.FailureRepositoryDrift,
		gitadvanced.FailureRemoteDrift, gitadvanced.FailurePermissionDrift,
		gitadvanced.FailureLeaseDrift, gitadvanced.FailureConflict,
		gitadvanced.FailureDirtyWorktree:
		code = apperror.CodeConflict
	}
	return apperror.New(code, advancedErr.Error())
}
