package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const (
	WorkspaceCheckpointAPIProtocolVersion = "workspace-checkpoint-api.v1"
	workspaceCheckpointListLimit          = 2_000
)

type WorkspaceCheckpointStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	GetWorkspaceCheckpointInvocationAttempt(context.Context, string, string) (string, bool, error)

	CreateWorkspaceCheckpoint(context.Context, workspacecheckpoint.Snapshot) (
		workspacecheckpoint.Checkpoint, bool, error)
	GetWorkspaceCheckpoint(context.Context, string) (workspacecheckpoint.Checkpoint, error)
	GetWorkspaceCheckpointSnapshot(context.Context, string) (workspacecheckpoint.Snapshot, error)
	ListWorkspaceCheckpoints(context.Context, string, int) ([]workspacecheckpoint.Checkpoint, error)
	WorkspaceCheckpointStorageUsage(context.Context) (workspacecheckpoint.StorageUsage, error)

	CreateWorkspaceCheckpointTransaction(context.Context, workspacecheckpoint.Transaction) (
		workspacecheckpoint.Transaction, bool, error)
	UpdateWorkspaceCheckpointTransaction(context.Context, workspacecheckpoint.Transaction) (
		workspacecheckpoint.Transaction, bool, error)
	GetWorkspaceCheckpointTransaction(context.Context, string) (
		workspacecheckpoint.Transaction, bool, error)
	GetWorkspaceCheckpointTransactionByOperation(context.Context, string) (
		workspacecheckpoint.Transaction, bool, error)
	ListWorkspaceCheckpointTransactions(context.Context, string, int) (
		[]workspacecheckpoint.Transaction, error)
	ListOpenWorkspaceCheckpointTransactions(context.Context, int) (
		[]workspacecheckpoint.Transaction, error)
	ListWorkspaceCheckpointTransactionsPendingCursor(context.Context, int) (
		[]workspacecheckpoint.Transaction, error)

	GetWorkspaceCheckpointRunState(context.Context, string) (
		workspacecheckpoint.RunState, bool, error)
	AdvanceWorkspaceCheckpointRunState(context.Context, workspacecheckpoint.RunState, string) (
		workspacecheckpoint.RunState, bool, error)
}

// workspaceCheckpointScopedTimelineStore is an optional exact projection used
// by durable stores that can query one Run/Workspace timeline directly. Test
// doubles and older adapters keep the bounded in-memory fallback below.
type workspaceCheckpointScopedTimelineStore interface {
	ListWorkspaceCheckpointsByWorkspace(context.Context, string, string, int) (
		[]workspacecheckpoint.Checkpoint, error)
	ListWorkspaceCheckpointTransactionsByWorkspace(context.Context, string, string, int) (
		[]workspacecheckpoint.Transaction, error)
}

type WorkspaceCheckpointForkStore interface {
	WorkspaceCheckpointStore
	ContextContinuityStore
	CreateWorkspaceMissionRunWithContinuity(context.Context, session.WorkspaceRecord,
		domain.Mission, domain.Run, domain.RunModeSnapshot, session.Session, bool,
		[]events.Event, contextmgr.ContinuityNode) error
}

type WorkspaceCheckpointService struct {
	store                WorkspaceCheckpointStore
	capabilities         domain.ExecutionPermissionRuntimeCapabilities
	now                  func() time.Time
	lifecycleHooks       *hooks.Engine
	runWorkspace         func(context.Context, string) (session.WorkspaceInfo, bool, error)
	strictMutationReplay bool
	operatorGitThreadID  string
	boundaryCompletionMu *sync.Mutex
}

func (s *WorkspaceCheckpointService) WithLifecycleHooks(
	engine *hooks.Engine,
) *WorkspaceCheckpointService {
	if s != nil {
		s.lifecycleHooks = engine
	}
	return s
}

// withRunWorkspaceResolver permits an Application-internal, identity-verifying
// owner such as Drydock to substitute its Run-owned Workspace. Keeping this
// hook package-private prevents an external caller from replacing Run scope.
func (s *WorkspaceCheckpointService) withRunWorkspaceResolver(
	resolver func(context.Context, string) (session.WorkspaceInfo, bool, error),
) *WorkspaceCheckpointService {
	if s != nil {
		s.runWorkspace = resolver
	}
	return s
}

func NewWorkspaceCheckpointService(store WorkspaceCheckpointStore,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) (*WorkspaceCheckpointService, error) {
	if store == nil || capabilities.Validate() != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace checkpoint service dependencies are invalid")
	}
	return &WorkspaceCheckpointService{store: store, capabilities: capabilities,
		now: func() time.Time { return time.Now().UTC() }, boundaryCompletionMu: &sync.Mutex{}}, nil
}

type WorkspaceCheckpointCaptureRequest struct {
	RunID        string
	OperationKey string
	RequestedBy  string
	Title        string
}

type WorkspaceMutationBoundaryRequest struct {
	RunID                string
	Kind                 workspacecheckpoint.TransactionKind
	OperationKey         string
	TriggerReceiptID     string
	InvocationID         string
	AttemptID            string
	CapabilityGeneration string
	LeaseID              string
	LeaseGeneration      int64
	IncompleteReasons    []string
}

type WorkspaceMutationBoundary struct {
	Transaction workspacecheckpoint.Transaction `json:"transaction"`
	Before      workspacecheckpoint.Checkpoint  `json:"before"`
	After       *workspacecheckpoint.Checkpoint `json:"after,omitempty"`
	Replayed    bool                            `json:"replayed"`
}

type WorkspaceCheckpointTimeline struct {
	ProtocolVersion string                            `json:"protocol_version"`
	RunID           string                            `json:"run_id"`
	WorkspaceID     string                            `json:"workspace_id"`
	Current         *workspacecheckpoint.RunState     `json:"current,omitempty"`
	Checkpoints     []workspacecheckpoint.Checkpoint  `json:"checkpoints"`
	Transactions    []workspacecheckpoint.Transaction `json:"transactions"`
	StorageUsage    workspacecheckpoint.StorageUsage  `json:"storage_usage"`
}

type WorkspaceRestoreRequest struct {
	RunID                       string
	TargetCheckpointID          string
	ExpectedCurrentCheckpointID string
	OperationKey                string
	RequestedBy                 string
	Kind                        workspacecheckpoint.TransactionKind
	TriggerReceiptID            string
	Confirm                     bool
}

type WorkspaceRestoreResult struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Preview         workspacecheckpoint.Preview      `json:"preview"`
	Transaction     *workspacecheckpoint.Transaction `json:"transaction,omitempty"`
	Before          workspacecheckpoint.Checkpoint   `json:"before"`
	After           *workspacecheckpoint.Checkpoint  `json:"after,omitempty"`
	Confirmed       bool                             `json:"confirmed"`
	Replayed        bool                             `json:"replayed"`
}

type WorkspaceForkRequest struct {
	RunID                       string
	TargetCheckpointID          string
	ExpectedCurrentCheckpointID string
	OperationKey                string
	RequestedBy                 string
	WorkspaceName               string
	WorkspaceRoot               string
	Branch                      string
	Goal                        string
	Confirm                     bool
}

type WorkspaceForkResult struct {
	ProtocolVersion string                          `json:"protocol_version"`
	SourceRunID     string                          `json:"source_run_id"`
	Target          workspacecheckpoint.Checkpoint  `json:"target"`
	Workspace       session.WorkspaceRecord         `json:"workspace"`
	Mission         domain.Mission                  `json:"mission"`
	Run             domain.Run                      `json:"run"`
	Node            contextmgr.ContinuityNode       `json:"continuity_node"`
	Checkpoint      workspacecheckpoint.Checkpoint  `json:"checkpoint"`
	Transaction     workspacecheckpoint.Transaction `json:"transaction"`
	NotInherited    []string                        `json:"not_inherited"`
	Replayed        bool                            `json:"replayed"`
}

type workspaceCheckpointBinding struct {
	run       domain.Run
	mission   domain.Mission
	session   session.Session
	workspace session.WorkspaceInfo
}

func (s *WorkspaceCheckpointService) Capture(ctx context.Context,
	request WorkspaceCheckpointCaptureRequest,
) (workspacecheckpoint.Checkpoint, bool, error) {
	if s == nil || s.store == nil || s.now == nil {
		return workspacecheckpoint.Checkpoint{}, false, apperror.New(
			apperror.CodeFailedPrecondition, "workspace checkpoint service is unavailable")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeWorkspaceCheckpointOperator(request.RequestedBy)
	request.Title = strings.TrimSpace(request.Title)
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" {
		return workspacecheckpoint.Checkpoint{}, false, apperror.New(
			apperror.CodeInvalidArgument, "workspace checkpoint capture request is invalid")
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	openTransactions, err := s.store.ListOpenWorkspaceCheckpointTransactions(ctx,
		workspaceCheckpointListLimit)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, apperror.Normalize(err)
	}
	if len(openTransactions) == workspaceCheckpointListLimit {
		return workspacecheckpoint.Checkpoint{}, false, apperror.New(
			apperror.CodeResourceExhausted,
			"workspace checkpoint open-transaction backlog reached its safe scan limit")
	}
	for _, transaction := range openTransactions {
		if transaction.RunID == binding.run.ID {
			return workspacecheckpoint.Checkpoint{}, false, apperror.New(
				apperror.CodeConflict,
				"a workspace mutation boundary is still open for this Run")
		}
	}
	operationDigest := runmutation.OperationKeyDigest("workspace_checkpoint_manual.v1",
		binding.run.ID, request.OperationKey)
	checkpointID := workspaceCheckpointID(operationDigest, "manual")
	if existing, getErr := s.store.GetWorkspaceCheckpoint(ctx, checkpointID); getErr == nil {
		if existing.RunID != binding.run.ID || existing.MissionID != binding.mission.ID ||
			existing.SessionID != binding.session.ID ||
			existing.WorkspaceID != binding.workspace.ID ||
			existing.Trigger != workspacecheckpoint.TriggerManual ||
			existing.Phase != workspacecheckpoint.PhaseStandalone ||
			existing.TriggerReceiptID != request.OperationKey ||
			existing.RequestedBy != request.RequestedBy || existing.Title != request.Title {
			return workspacecheckpoint.Checkpoint{}, false, apperror.New(
				apperror.CodeConflict, "workspace checkpoint operation key was reused")
		}
		if err := s.advanceManualCheckpointCursor(ctx, binding, existing); err != nil {
			return workspacecheckpoint.Checkpoint{}, false, err
		}
		return existing, true, nil
	} else if apperror.CodeOf(apperror.Normalize(getErr)) != apperror.CodeNotFound {
		return workspacecheckpoint.Checkpoint{}, false, apperror.Normalize(getErr)
	}
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, apperror.Normalize(err)
	}
	parentID := ""
	if found {
		parentID = state.CurrentCheckpointID
	}
	if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.Checkpoint,
		binding.run.ID, binding.workspace.ID, map[string]any{
			"session_id": binding.session.ID, "checkpoint_id": checkpointID,
			"kind": workspacecheckpoint.TriggerManual, "source": "workspace_checkpoint",
		}); err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	snapshot, replayed, err := s.capture(ctx, binding, workspacecheckpoint.CaptureRequest{
		ID:      checkpointID,
		Trigger: workspacecheckpoint.TriggerManual, Phase: workspacecheckpoint.PhaseStandalone,
		TriggerReceiptID: request.OperationKey, RequestedBy: request.RequestedBy,
		Title: request.Title, ParentCheckpointID: parentID,
		CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	_, cursorReplay, err := s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			CurrentCheckpointID: snapshot.Checkpoint.ID,
			LastTransactionID:   "", UpdatedAt: s.now().UTC()}, parentID)
	if err != nil {
		// A retry after the cursor commit is successful if it still points at
		// this exact immutable checkpoint.
		current, currentFound, currentErr := s.store.GetWorkspaceCheckpointRunState(ctx,
			binding.run.ID)
		if currentErr != nil || !currentFound || current.CurrentCheckpointID != snapshot.Checkpoint.ID {
			return workspacecheckpoint.Checkpoint{}, false, apperror.Normalize(err)
		}
		cursorReplay = true
	}
	return snapshot.Checkpoint, replayed || cursorReplay, nil
}

func (s *WorkspaceCheckpointService) advanceManualCheckpointCursor(ctx context.Context,
	binding workspaceCheckpointBinding, checkpoint workspacecheckpoint.Checkpoint,
) error {
	_, _, err := s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID,
			WorkspaceID: binding.workspace.ID, CurrentCheckpointID: checkpoint.ID,
			LastTransactionID: "", UpdatedAt: s.now().UTC()}, checkpoint.ParentCheckpointID)
	if err == nil {
		return nil
	}
	current, found, getErr := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	if getErr == nil && found && current.WorkspaceID == binding.workspace.ID &&
		current.CurrentCheckpointID == checkpoint.ID {
		return nil
	}
	if getErr != nil {
		return apperror.Normalize(getErr)
	}
	return apperror.Normalize(err)
}

func (s *WorkspaceCheckpointService) BeginBoundary(ctx context.Context,
	request WorkspaceMutationBoundaryRequest,
) (WorkspaceMutationBoundary, error) {
	request = normalizeWorkspaceMutationBoundaryRequest(request)
	if s == nil || s.store == nil || s.now == nil || request.RunID == "" ||
		request.OperationKey == "" || request.TriggerReceiptID == "" ||
		!workspaceBoundaryKind(request.Kind) {
		return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace mutation boundary request is invalid")
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return WorkspaceMutationBoundary{}, err
	}
	operationDigest := workspaceBoundaryOperationDigest(binding.run.ID, request.Kind,
		request.OperationKey)
	fingerprint := workspaceBoundaryFingerprint(binding, request)
	if existing, found, err := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx,
		operationDigest); err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	} else if found {
		if existing.RequestFingerprint != fingerprint || existing.RunID != binding.run.ID ||
			existing.WorkspaceID != binding.workspace.ID || existing.Kind != request.Kind ||
			existing.TriggerReceiptID != request.TriggerReceiptID {
			return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeConflict,
				"workspace mutation operation key was reused for different intent")
		}
		return s.replayBoundary(ctx, binding, existing)
	}
	openTransactions, err := s.store.ListOpenWorkspaceCheckpointTransactions(ctx,
		workspaceCheckpointListLimit)
	if err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	if len(openTransactions) == workspaceCheckpointListLimit {
		return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeResourceExhausted,
			"workspace checkpoint open-transaction backlog reached its safe scan limit")
	}
	for _, openTransaction := range openTransactions {
		if openTransaction.RunID == binding.run.ID {
			return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeConflict,
				"another workspace mutation boundary is still open for this Run")
		}
	}
	incomplete := append([]string{}, request.IncompleteReasons...)
	attemptID := request.AttemptID
	if attemptID == "" && request.InvocationID != "" {
		resolved, found, resolveErr := s.store.GetWorkspaceCheckpointInvocationAttempt(ctx,
			binding.run.ID, request.InvocationID)
		if resolveErr != nil {
			return WorkspaceMutationBoundary{}, apperror.Normalize(resolveErr)
		}
		if found {
			attemptID = resolved
		} else {
			incomplete = append(incomplete,
				"triggering supervisor attempt could not be attributed")
		}
	}
	if request.CapabilityGeneration == "" && request.InvocationID != "" {
		incomplete = append(incomplete, "triggering capability generation is unavailable")
	}
	if request.LeaseID != "" {
		if err := s.requireBoundaryLease(ctx, binding, request); err != nil {
			return WorkspaceMutationBoundary{}, err
		}
	} else if request.InvocationID != "" {
		incomplete = append(incomplete, "triggering execution lease is unavailable")
	}
	state, stateFound, err := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	if err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	previousID := ""
	if stateFound {
		previousID = state.CurrentCheckpointID
	}
	before, _, err := s.capture(ctx, binding, workspacecheckpoint.CaptureRequest{
		ID: workspaceCheckpointID(operationDigest, "before"), AttemptID: attemptID,
		CapabilityGeneration: request.CapabilityGeneration,
		Trigger:              workspaceBoundaryTrigger(request.Kind), Phase: workspacecheckpoint.PhaseBefore,
		TriggerReceiptID: request.TriggerReceiptID, ParentCheckpointID: previousID,
		IncompleteReasons: incomplete, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return WorkspaceMutationBoundary{}, err
	}
	now := s.now().UTC()
	transaction := workspacecheckpoint.Transaction{ID: workspaceTransactionID(operationDigest),
		ProtocolVersion:    workspacecheckpoint.ProtocolVersion,
		OperationKeyDigest: operationDigest, RequestFingerprint: fingerprint,
		RunID: binding.run.ID, WorkspaceID: binding.workspace.ID, Kind: request.Kind,
		TriggerReceiptID:            request.TriggerReceiptID,
		BeforeCheckpointID:          before.Checkpoint.ID,
		ExpectedCurrentCheckpointID: previousID,
		Status:                      workspacecheckpoint.TransactionPrepared,
		RecoveryLevel:               before.Checkpoint.RecoveryLevel, ConflictJSON: "[]",
		CreatedAt: now, UpdatedAt: now}
	transaction, replayed, err := s.store.CreateWorkspaceCheckpointTransaction(ctx,
		transaction)
	if err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	_, cursorReplay, err := s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			CurrentCheckpointID: before.Checkpoint.ID,
			LastTransactionID:   "", UpdatedAt: s.now().UTC()}, previousID)
	if err != nil {
		current, found, getErr := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
		if getErr != nil || !found || current.CurrentCheckpointID != before.Checkpoint.ID {
			if s.strictMutationReplay {
				// Keep the prepared journal recoverable. No mutation was authorized
				// before its owning cursor could be advanced.
				return WorkspaceMutationBoundary{}, apperror.Normalize(err)
			}
			completedAt := s.now().UTC()
			transaction.Status = workspacecheckpoint.TransactionFailed
			transaction.AfterCheckpointID = before.Checkpoint.ID
			transaction.ErrorCode = "workspace_cursor_race"
			transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
			_, _, _ = s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
			return WorkspaceMutationBoundary{}, apperror.Normalize(err)
		}
		cursorReplay = true
	}
	return WorkspaceMutationBoundary{Transaction: transaction, Before: before.Checkpoint,
		Replayed: replayed || cursorReplay}, nil
}

func (s *WorkspaceCheckpointService) CompleteBoundary(ctx context.Context,
	request WorkspaceMutationBoundaryRequest, mutationErr error,
) (WorkspaceMutationBoundary, error) {
	request = normalizeWorkspaceMutationBoundaryRequest(request)
	if s == nil || s.store == nil || request.RunID == "" || request.OperationKey == "" ||
		!workspaceBoundaryKind(request.Kind) {
		return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace mutation completion request is invalid")
	}
	// Foreground completion and recovery can observe the same prepared row.
	// Serialize capture through cursor advancement so the second caller reads
	// the durable completion instead of producing a competing timestamp/CAS.
	// The pointer keeps Drydock's scoped service copies on the same lock.
	s.boundaryCompletionMu.Lock()
	defer s.boundaryCompletionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return WorkspaceMutationBoundary{}, err
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return WorkspaceMutationBoundary{}, err
	}
	digest := workspaceBoundaryOperationDigest(binding.run.ID, request.Kind,
		request.OperationKey)
	transaction, found, err := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx, digest)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition,
				"workspace mutation boundary was not prepared")
		}
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	if transaction.RequestFingerprint != workspaceBoundaryFingerprint(binding, request) ||
		transaction.Kind != request.Kind || transaction.TriggerReceiptID != request.TriggerReceiptID {
		return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeConflict,
			"workspace mutation completion does not match its prepared boundary")
	}
	if transaction.Status.Terminal() {
		return s.replayBoundary(ctx, binding, transaction)
	}
	before, err := s.store.GetWorkspaceCheckpointSnapshot(ctx,
		transaction.BeforeCheckpointID)
	if err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	after, _, err := s.capture(ctx, binding, workspacecheckpoint.CaptureRequest{
		ID: workspaceCheckpointID(digest, "after"), AttemptID: before.Checkpoint.AttemptID,
		CapabilityGeneration: before.Checkpoint.CapabilityGeneration,
		Trigger:              workspaceBoundaryTrigger(request.Kind), Phase: workspacecheckpoint.PhaseAfter,
		TriggerReceiptID:   request.TriggerReceiptID,
		ParentCheckpointID: before.Checkpoint.ID, CreatedAt: s.now().UTC(),
		IncompleteReasons: append([]string{}, before.Checkpoint.IncompleteReasons...),
	})
	if err != nil {
		return WorkspaceMutationBoundary{}, err
	}
	completedAt := s.now().UTC()
	transaction.AfterCheckpointID = after.Checkpoint.ID
	transaction.RecoveryLevel = weakestWorkspaceRecovery(before.Checkpoint.RecoveryLevel,
		after.Checkpoint.RecoveryLevel)
	transaction.Status = workspacecheckpoint.TransactionCompleted
	transaction.ErrorCode = ""
	if mutationErr != nil {
		transaction.Status = workspacecheckpoint.TransactionFailed
		transaction.ErrorCode = strings.ToLower(string(apperror.CodeOf(
			apperror.Normalize(mutationErr))))
		if transaction.ErrorCode == "" {
			transaction.ErrorCode = "workspace_mutation_failed"
		}
	}
	transaction.UpdatedAt = completedAt
	transaction.CompletedAt = &completedAt
	transaction, replayed, err := s.store.UpdateWorkspaceCheckpointTransaction(ctx,
		transaction)
	if err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	_, cursorReplay, err := s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			CurrentCheckpointID: after.Checkpoint.ID, LastTransactionID: transaction.ID,
			UpdatedAt: s.now().UTC()}, before.Checkpoint.ID)
	if err != nil {
		current, currentFound, getErr := s.store.GetWorkspaceCheckpointRunState(ctx,
			binding.run.ID)
		if getErr != nil || !currentFound || current.CurrentCheckpointID != after.Checkpoint.ID ||
			current.LastTransactionID != transaction.ID {
			return WorkspaceMutationBoundary{}, apperror.Normalize(err)
		}
		cursorReplay = true
	}
	checkpoint := after.Checkpoint
	return WorkspaceMutationBoundary{Transaction: transaction, Before: before.Checkpoint,
		After: &checkpoint, Replayed: replayed || cursorReplay}, nil
}

func (s *WorkspaceCheckpointService) Timeline(ctx context.Context, runID string,
	limit int,
) (WorkspaceCheckpointTimeline, error) {
	binding, err := s.loadBinding(ctx, strings.TrimSpace(runID))
	if err != nil {
		return WorkspaceCheckpointTimeline{}, err
	}
	if limit < 1 || limit > 2_000 {
		return WorkspaceCheckpointTimeline{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint timeline limit is invalid")
	}
	var checkpoints []workspacecheckpoint.Checkpoint
	var transactions []workspacecheckpoint.Transaction
	if scoped, ok := s.store.(workspaceCheckpointScopedTimelineStore); ok {
		checkpoints, err = scoped.ListWorkspaceCheckpointsByWorkspace(ctx,
			binding.run.ID, binding.workspace.ID, limit)
		if err == nil {
			transactions, err = scoped.ListWorkspaceCheckpointTransactionsByWorkspace(ctx,
				binding.run.ID, binding.workspace.ID, limit)
		}
	} else {
		// A Run may also own a Drydock checkpoint timeline. Adapters without an
		// exact query receive a bounded superset and are projected in memory.
		checkpoints, err = s.store.ListWorkspaceCheckpoints(ctx, binding.run.ID,
			workspaceCheckpointListLimit)
		if err == nil {
			checkpoints = filterWorkspaceCheckpoints(checkpoints, binding.workspace.ID, limit)
			transactions, err = s.store.ListWorkspaceCheckpointTransactions(ctx,
				binding.run.ID, workspaceCheckpointListLimit)
		}
		if err == nil {
			transactions = filterWorkspaceCheckpointTransactions(transactions,
				binding.workspace.ID, limit)
		}
	}
	if err != nil {
		return WorkspaceCheckpointTimeline{}, apperror.Normalize(err)
	}
	usage, err := s.store.WorkspaceCheckpointStorageUsage(ctx)
	if err != nil {
		return WorkspaceCheckpointTimeline{}, apperror.Normalize(err)
	}
	value := WorkspaceCheckpointTimeline{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
		RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
		Checkpoints: checkpoints, Transactions: transactions, StorageUsage: usage}
	if state, found, stateErr := s.store.GetWorkspaceCheckpointRunState(ctx,
		binding.run.ID); stateErr != nil {
		return WorkspaceCheckpointTimeline{}, apperror.Normalize(stateErr)
	} else if found {
		value.Current = &state
	}
	return value, nil
}

func filterWorkspaceCheckpoints(values []workspacecheckpoint.Checkpoint,
	workspaceID string, limit int,
) []workspacecheckpoint.Checkpoint {
	filtered := make([]workspacecheckpoint.Checkpoint, 0, min(len(values), limit))
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			filtered = append(filtered, value)
			if len(filtered) == limit {
				break
			}
		}
	}
	return filtered
}

func filterWorkspaceCheckpointTransactions(values []workspacecheckpoint.Transaction,
	workspaceID string, limit int,
) []workspacecheckpoint.Transaction {
	filtered := make([]workspacecheckpoint.Transaction, 0, min(len(values), limit))
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			filtered = append(filtered, value)
			if len(filtered) == limit {
				break
			}
		}
	}
	return filtered
}

func (s *WorkspaceCheckpointService) Restore(ctx context.Context,
	request WorkspaceRestoreRequest,
) (WorkspaceRestoreResult, error) {
	request = normalizeWorkspaceRestoreRequest(request)
	if s == nil || s.store == nil || request.RunID == "" ||
		request.TargetCheckpointID == "" ||
		(request.Confirm && (request.OperationKey == "" ||
			request.ExpectedCurrentCheckpointID == "")) || !workspaceRestoreKind(request.Kind) {
		return WorkspaceRestoreResult{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace restore request is invalid")
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return WorkspaceRestoreResult{}, err
	}
	target, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, request.TargetCheckpointID)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	if target.Checkpoint.RunID != binding.run.ID ||
		target.Checkpoint.WorkspaceID != binding.workspace.ID {
		return WorkspaceRestoreResult{}, apperror.New(apperror.CodeNotFound,
			"target checkpoint was not found in this Run")
	}
	digest := ""
	if request.Confirm {
		digest = runmutation.OperationKeyDigest("workspace_restore_operation.v1."+
			string(request.Kind), binding.run.ID, request.OperationKey)
		existing, exists, getErr := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx,
			digest)
		if getErr != nil {
			return WorkspaceRestoreResult{}, apperror.Normalize(getErr)
		}
		if exists {
			if request.ExpectedCurrentCheckpointID == "" {
				request.ExpectedCurrentCheckpointID = existing.ExpectedCurrentCheckpointID
			}
			fingerprint := runmutation.Fingerprint("workspace_restore_request.v1",
				binding.run.ID, binding.workspace.ID, request.TargetCheckpointID,
				request.ExpectedCurrentCheckpointID, string(request.Kind), request.RequestedBy)
			if existing.RequestFingerprint != fingerprint ||
				existing.TargetCheckpointID != request.TargetCheckpointID ||
				existing.ExpectedCurrentCheckpointID != request.ExpectedCurrentCheckpointID ||
				existing.Kind != request.Kind {
				return WorkspaceRestoreResult{}, apperror.New(apperror.CodeConflict,
					"workspace restore operation key was reused for different intent")
			}
			if existing.Status.Terminal() {
				return s.replayRestore(ctx, existing)
			}
			state, stateFound, stateErr := s.store.GetWorkspaceCheckpointRunState(ctx,
				binding.run.ID)
			if stateErr != nil {
				return WorkspaceRestoreResult{}, apperror.Normalize(stateErr)
			}
			if !stateFound {
				return WorkspaceRestoreResult{}, apperror.New(apperror.CodeConflict,
					"workspace restore cursor disappeared")
			}
			if state.CurrentCheckpointID != existing.ExpectedCurrentCheckpointID &&
				state.CurrentCheckpointID != existing.BeforeCheckpointID &&
				state.CurrentCheckpointID != existing.AfterCheckpointID {
				return WorkspaceRestoreResult{}, apperror.New(apperror.CodeConflict,
					"workspace restore cursor moved to another operation")
			}
			if err := s.requireRestoreAuthority(ctx, binding, request.RequestedBy); err != nil {
				return WorkspaceRestoreResult{}, err
			}
			if state.CurrentCheckpointID == existing.ExpectedCurrentCheckpointID &&
				state.CurrentCheckpointID != existing.BeforeCheckpointID {
				if _, _, stateErr = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
					workspacecheckpoint.RunState{RunID: binding.run.ID,
						WorkspaceID:         binding.workspace.ID,
						CurrentCheckpointID: existing.BeforeCheckpointID,
						LastTransactionID:   "",
						UpdatedAt:           s.now().UTC()}, state.CurrentCheckpointID); stateErr != nil {
					return WorkspaceRestoreResult{}, apperror.Normalize(stateErr)
				}
			}
			return s.resumeRestore(ctx, binding, request, existing, target)
		}
	}
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition,
				"workspace checkpoint cursor is not initialized")
		}
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	if request.ExpectedCurrentCheckpointID != "" &&
		request.ExpectedCurrentCheckpointID != state.CurrentCheckpointID {
		return WorkspaceRestoreResult{}, apperror.New(apperror.CodeConflict,
			"workspace checkpoint cursor changed after preview")
	}
	request.ExpectedCurrentCheckpointID = state.CurrentCheckpointID
	current, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, state.CurrentCheckpointID)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	if !request.Confirm {
		observed, captureErr := workspacecheckpoint.Capture(ctx,
			s.workspaceCaptureRequest(binding, workspacecheckpoint.CaptureRequest{
				ID: idgen.New("workspace-preview"), Trigger: workspacecheckpoint.TriggerRewindPreflight,
				Phase:              workspacecheckpoint.PhasePreflight,
				TriggerReceiptID:   request.TargetCheckpointID,
				ParentCheckpointID: state.CurrentCheckpointID, CreatedAt: s.now().UTC()}))
		if captureErr != nil {
			return WorkspaceRestoreResult{}, apperror.Normalize(captureErr)
		}
		preview, previewErr := workspacecheckpoint.PreviewRestore(current, target, observed)
		return WorkspaceRestoreResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
			Preview: preview, Before: current.Checkpoint}, apperror.Normalize(previewErr)
	}
	if err := s.requireRestoreAuthority(ctx, binding, request.RequestedBy); err != nil {
		return WorkspaceRestoreResult{}, err
	}
	fingerprint := runmutation.Fingerprint("workspace_restore_request.v1", binding.run.ID,
		binding.workspace.ID, request.TargetCheckpointID,
		request.ExpectedCurrentCheckpointID, string(request.Kind), request.RequestedBy)
	preflight, _, err := s.capture(ctx, binding, workspacecheckpoint.CaptureRequest{
		ID:      workspaceCheckpointID(digest, "preflight"),
		Trigger: workspacecheckpoint.TriggerRewindPreflight,
		Phase:   workspacecheckpoint.PhasePreflight, TriggerReceiptID: request.TriggerReceiptID,
		ParentCheckpointID: state.CurrentCheckpointID, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return WorkspaceRestoreResult{}, err
	}
	preview, err := workspacecheckpoint.PreviewRestore(current, target, preflight)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	now := s.now().UTC()
	transaction := workspacecheckpoint.Transaction{ID: workspaceTransactionID(digest),
		ProtocolVersion: workspacecheckpoint.ProtocolVersion, OperationKeyDigest: digest,
		RequestFingerprint: fingerprint, RunID: binding.run.ID,
		WorkspaceID: binding.workspace.ID, Kind: request.Kind,
		TriggerReceiptID:            request.TriggerReceiptID,
		BeforeCheckpointID:          preflight.Checkpoint.ID,
		ExpectedCurrentCheckpointID: state.CurrentCheckpointID,
		TargetCheckpointID:          target.Checkpoint.ID,
		Status:                      workspacecheckpoint.TransactionPrepared,
		RecoveryLevel:               preview.RecoveryLevel,
		ConflictJSON:                workspaceConflictJSON(preview.Conflicts), CreatedAt: now, UpdatedAt: now}
	transaction, _, err = s.store.CreateWorkspaceCheckpointTransaction(ctx, transaction)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	_, _, err = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			CurrentCheckpointID: preflight.Checkpoint.ID,
			LastTransactionID:   "", UpdatedAt: s.now().UTC()},
		state.CurrentCheckpointID)
	if err != nil {
		currentState, currentFound, getErr := s.store.GetWorkspaceCheckpointRunState(ctx,
			binding.run.ID)
		if getErr != nil || !currentFound ||
			currentState.CurrentCheckpointID != preflight.Checkpoint.ID {
			completedAt := s.now().UTC()
			transaction.Status = workspacecheckpoint.TransactionFailed
			transaction.AfterCheckpointID = preflight.Checkpoint.ID
			transaction.ErrorCode = "workspace_cursor_race"
			transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
			_, _, updateErr := s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
			return WorkspaceRestoreResult{}, errors.Join(apperror.Normalize(err),
				apperror.Normalize(getErr), apperror.Normalize(updateErr))
		}
	}
	if len(preview.Conflicts) != 0 {
		completedAt := s.now().UTC()
		transaction.Status = workspacecheckpoint.TransactionFailed
		transaction.AfterCheckpointID = preflight.Checkpoint.ID
		transaction.ErrorCode = "restore_conflict"
		transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
		transaction, _, _ = s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
		_, _, _ = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
			workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
				CurrentCheckpointID: preflight.Checkpoint.ID,
				LastTransactionID:   transaction.ID, UpdatedAt: s.now().UTC()},
			preflight.Checkpoint.ID)
		return WorkspaceRestoreResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
			Preview: preview, Transaction: &transaction, Before: preflight.Checkpoint,
			Confirmed: true}, &workspacecheckpoint.ConflictError{Conflicts: preview.Conflicts}
	}
	return s.resumeRestore(ctx, binding, request, transaction, target)
}

func (s *WorkspaceCheckpointService) Undo(ctx context.Context, runID,
	expectedCurrentCheckpointID, operationKey, requestedBy string, confirm bool,
) (WorkspaceRestoreResult, error) {
	if result, found, err := s.replayRestoreOperation(ctx, runID, operationKey,
		requestedBy, workspacecheckpoint.TransactionUndo); found || err != nil {
		return result, err
	}
	transaction, err := s.findUndoSource(ctx, strings.TrimSpace(runID))
	if err != nil {
		return WorkspaceRestoreResult{}, err
	}
	return s.Restore(ctx, WorkspaceRestoreRequest{RunID: runID,
		TargetCheckpointID:          transaction.BeforeCheckpointID,
		ExpectedCurrentCheckpointID: expectedCurrentCheckpointID,
		OperationKey:                operationKey, RequestedBy: requestedBy,
		Kind:             workspacecheckpoint.TransactionUndo,
		TriggerReceiptID: transaction.ID, Confirm: confirm})
}

func (s *WorkspaceCheckpointService) Redo(ctx context.Context, runID,
	expectedCurrentCheckpointID, operationKey, requestedBy string, confirm bool,
) (WorkspaceRestoreResult, error) {
	if result, found, err := s.replayRestoreOperation(ctx, runID, operationKey,
		requestedBy, workspacecheckpoint.TransactionRedo); found || err != nil {
		return result, err
	}
	undo, source, err := s.findRedoSource(ctx, strings.TrimSpace(runID))
	if err != nil {
		return WorkspaceRestoreResult{}, err
	}
	return s.Restore(ctx, WorkspaceRestoreRequest{RunID: runID,
		TargetCheckpointID:          source.AfterCheckpointID,
		ExpectedCurrentCheckpointID: expectedCurrentCheckpointID,
		OperationKey:                operationKey, RequestedBy: requestedBy,
		Kind:             workspacecheckpoint.TransactionRedo,
		TriggerReceiptID: undo.ID, Confirm: confirm})
}

// Fork materializes a historical checkpoint into a new branch-backed
// worktree, registers a distinct Workspace, and creates an authority-reset Run.
// The source Run cursor and all historical checkpoints remain unchanged.
func (s *WorkspaceCheckpointService) Fork(ctx context.Context,
	request WorkspaceForkRequest,
) (WorkspaceForkResult, error) {
	return s.fork(ctx, request, nil)
}

// forkFromOwnedCheckpoint reuses the authority-reset Fork implementation with
// a package-internal cursor owned by a higher-level lifecycle such as Drydock.
// It never replaces the source Workspace cursor stored for the Run.
func (s *WorkspaceCheckpointService) forkFromOwnedCheckpoint(ctx context.Context,
	request WorkspaceForkRequest, ownedState workspacecheckpoint.RunState,
) (WorkspaceForkResult, error) {
	return s.fork(ctx, request, &ownedState)
}

func (s *WorkspaceCheckpointService) fork(ctx context.Context,
	request WorkspaceForkRequest, ownedState *workspacecheckpoint.RunState,
) (WorkspaceForkResult, error) {
	request = normalizeWorkspaceForkRequest(request)
	if s == nil || s.store == nil {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace checkpoint fork store is unavailable")
	}
	forkStore, ok := s.store.(WorkspaceCheckpointForkStore)
	if !ok {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace checkpoint fork store is unavailable")
	}
	if request.RunID == "" || request.TargetCheckpointID == "" ||
		request.ExpectedCurrentCheckpointID == "" || request.OperationKey == "" ||
		request.RequestedBy == "" || request.WorkspaceName == "" ||
		!request.Confirm || repository.ValidateBranchName(request.Branch) != nil {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint fork request is invalid")
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return WorkspaceForkResult{}, err
	}
	target, err := s.store.GetWorkspaceCheckpointSnapshot(ctx,
		request.TargetCheckpointID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	if (ownedState == nil && target.Checkpoint.RunID != binding.run.ID) ||
		target.Checkpoint.WorkspaceID != binding.workspace.ID ||
		target.Checkpoint.RecoveryLevel == workspacecheckpoint.RecoveryUnavailable {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"target checkpoint is not materializable in this Run")
	}
	if target.Checkpoint.BaseCommit == "unborn" {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace fork requires a committed Git base")
	}
	var state workspacecheckpoint.RunState
	found := false
	if ownedState != nil {
		state = *ownedState
		found = state.Validate() == nil && state.RunID == binding.run.ID &&
			state.WorkspaceID == binding.workspace.ID && state.LastTransactionID == ""
	} else {
		state, found, err = s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	}
	if err != nil || !found || state.CurrentCheckpointID != request.ExpectedCurrentCheckpointID {
		if err == nil {
			err = apperror.New(apperror.CodeConflict,
				"workspace checkpoint cursor changed before fork")
		}
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	digest := runmutation.OperationKeyDigest("workspace_fork_operation.v1",
		binding.run.ID, request.OperationKey)
	if request.WorkspaceRoot == "" {
		request.WorkspaceRoot, err = defaultWorkspaceForkRoot(binding.workspace.RootPath, digest)
		if err != nil {
			return WorkspaceForkResult{}, err
		}
	}
	request.WorkspaceRoot, err = repository.NormalizeWorktreeDestination(
		binding.workspace.RootPath, request.WorkspaceRoot)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	fingerprint := runmutation.Fingerprint("workspace_fork_request.v1", binding.run.ID,
		binding.workspace.ID, request.TargetCheckpointID,
		request.ExpectedCurrentCheckpointID, request.WorkspaceName,
		request.WorkspaceRoot, request.Branch, request.Goal, request.RequestedBy)
	transaction, exists, err := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx,
		digest)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	if exists {
		if transaction.RequestFingerprint != fingerprint ||
			transaction.RunID != binding.run.ID ||
			transaction.WorkspaceID != binding.workspace.ID ||
			transaction.Kind != workspacecheckpoint.TransactionFork ||
			transaction.TargetCheckpointID != target.Checkpoint.ID ||
			transaction.ExpectedCurrentCheckpointID != request.ExpectedCurrentCheckpointID ||
			transaction.ForkWorkspaceRoot != request.WorkspaceRoot ||
			transaction.ForkBranch != request.Branch {
			return WorkspaceForkResult{}, apperror.New(apperror.CodeConflict,
				"workspace fork operation key was reused for different intent")
		}
		if transaction.Status.Terminal() {
			return s.replayFork(ctx, forkStore, transaction, target.Checkpoint)
		}
	}
	if err := s.requireRestoreAuthority(ctx, binding, request.RequestedBy); err != nil {
		return WorkspaceForkResult{}, err
	}
	openTransactions, err := s.store.ListOpenWorkspaceCheckpointTransactions(ctx,
		workspaceCheckpointListLimit)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	if len(openTransactions) == workspaceCheckpointListLimit {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeResourceExhausted,
			"workspace checkpoint open-transaction backlog reached its safe scan limit")
	}
	for _, openTransaction := range openTransactions {
		if openTransaction.RunID == binding.run.ID &&
			openTransaction.OperationKeyDigest != digest {
			return WorkspaceForkResult{}, apperror.New(apperror.CodeConflict,
				"another workspace mutation boundary is still open for this Run")
		}
	}
	sourceNodeID, err := workspaceForkSourceNode(ctx, forkStore, binding.run)
	if err != nil {
		return WorkspaceForkResult{}, err
	}
	workspaceID := "workspace-fork-" + digest[:20]
	continuity := NewContextContinuityService(forkStore)
	prepared, node, continuityResult, err := continuity.prepareBranch(ctx,
		BranchContinuityRequest{SourceNodeID: sourceNodeID,
			Kind: contextmgr.ContinuityNodeFork, Goal: request.Goal,
			RequestedBy: request.RequestedBy, WorkspaceID: workspaceID,
			GitBranch: request.Branch, GitHead: target.Checkpoint.BaseCommit})
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	if exists {
		prepared, node, err = rekeyWorkspaceForkPreparation(prepared, node,
			transaction.TriggerReceiptID)
		if err != nil {
			return WorkspaceForkResult{}, err
		}
	} else {
		now := s.now().UTC()
		transaction = workspacecheckpoint.Transaction{ID: workspaceTransactionID(digest),
			ProtocolVersion:    workspacecheckpoint.ProtocolVersion,
			OperationKeyDigest: digest, RequestFingerprint: fingerprint,
			RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			Kind:                        workspacecheckpoint.TransactionFork,
			TriggerReceiptID:            prepared.Run.ID,
			BeforeCheckpointID:          state.CurrentCheckpointID,
			ExpectedCurrentCheckpointID: state.CurrentCheckpointID,
			TargetCheckpointID:          target.Checkpoint.ID,
			ForkWorkspaceRoot:           request.WorkspaceRoot,
			ForkBranch:                  request.Branch,
			Status:                      workspacecheckpoint.TransactionPrepared,
			RecoveryLevel:               target.Checkpoint.RecoveryLevel,
			ConflictJSON:                "[]", CreatedAt: now, UpdatedAt: now}
		transaction, _, err = s.store.CreateWorkspaceCheckpointTransaction(ctx,
			transaction)
		if err != nil {
			return WorkspaceForkResult{}, apperror.Normalize(err)
		}
	}
	if existingRun, getErr := forkStore.GetRun(ctx, transaction.TriggerReceiptID); getErr == nil {
		return s.completeExistingFork(ctx, forkStore, transaction, target.Checkpoint,
			existingRun)
	} else if apperror.CodeOf(apperror.Normalize(getErr)) != apperror.CodeNotFound {
		return WorkspaceForkResult{}, apperror.Normalize(getErr)
	}
	createdRoot, err := materializeWorkspaceFork(ctx, binding.workspace.RootPath,
		request.WorkspaceRoot, request.Branch, target)
	if err != nil {
		return WorkspaceForkResult{}, s.failForkTransaction(ctx, transaction,
			"fork_materialization_failed", err, false)
	}
	workspaceRecord := session.WorkspaceRecord{ID: workspaceID, Name: request.WorkspaceName,
		RootPath: createdRoot, CreatedAt: s.now().UTC()}
	if err := forkStore.CreateWorkspaceMissionRunWithContinuity(ctx, workspaceRecord,
		prepared.Mission, prepared.Run, prepared.Mode, prepared.Session,
		prepared.CreateSession, prepared.InitialEvents, node); err != nil {
		cleanupErr := s.cleanupInterruptedWorkspaceFork(context.WithoutCancel(ctx),
			binding.workspace.RootPath, createdRoot, request.Branch, target.Checkpoint)
		return WorkspaceForkResult{}, s.failForkTransaction(ctx, transaction,
			"fork_registration_failed", errors.Join(apperror.Normalize(err), cleanupErr),
			cleanupErr != nil)
	}
	newBinding, err := s.loadBinding(ctx, prepared.Run.ID)
	if err != nil {
		return WorkspaceForkResult{}, err
	}
	forkSnapshot, _, err := s.capture(ctx, newBinding, workspacecheckpoint.CaptureRequest{
		ID: workspaceCheckpointID(digest, "fork"), Trigger: workspacecheckpoint.TriggerFork,
		Phase: workspacecheckpoint.PhaseStandalone, TriggerReceiptID: transaction.ID,
		CreatedAt: s.now().UTC()})
	if err != nil {
		// The new Run and worktree are already durable. Keep the prepared
		// transaction replayable so a restart or identical retry can finish
		// capture without recreating either resource.
		return WorkspaceForkResult{}, err
	}
	if forkSnapshot.Checkpoint.ManifestSHA256 != target.Checkpoint.ManifestSHA256 ||
		forkSnapshot.Checkpoint.IndexSHA256 != target.Checkpoint.IndexSHA256 ||
		forkSnapshot.Checkpoint.BaseCommit != target.Checkpoint.BaseCommit ||
		forkSnapshot.Checkpoint.Branch != request.Branch {
		err = errors.New("forked Workspace failed final checkpoint verification")
		return WorkspaceForkResult{}, s.failForkTransaction(ctx, transaction,
			"fork_checkpoint_failed", err, true)
	}
	if _, _, err = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: prepared.Run.ID, WorkspaceID: workspaceID,
			CurrentCheckpointID: forkSnapshot.Checkpoint.ID, LastTransactionID: "",
			UpdatedAt: s.now().UTC()}, ""); err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	transaction, err = s.completeForkTransaction(ctx, transaction)
	if err != nil {
		return WorkspaceForkResult{}, err
	}
	continuityResult.Run = prepared.Run
	continuityResult.Mission = prepared.Mission
	continuityResult.Node = node
	return WorkspaceForkResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
		SourceRunID: binding.run.ID, Target: target.Checkpoint, Workspace: workspaceRecord,
		Mission: prepared.Mission, Run: prepared.Run, Node: node,
		Checkpoint: forkSnapshot.Checkpoint, Transaction: transaction,
		NotInherited: continuityResult.NotInherited}, nil
}

func (s *WorkspaceCheckpointService) replayRestoreOperation(ctx context.Context, runID,
	operationKey, requestedBy string, kind workspacecheckpoint.TransactionKind,
) (WorkspaceRestoreResult, bool, error) {
	runID, operationKey = strings.TrimSpace(runID), strings.TrimSpace(operationKey)
	if !workspaceRestoreKind(kind) || runID == "" || operationKey == "" {
		return WorkspaceRestoreResult{}, false, nil
	}
	binding, err := s.loadBinding(ctx, runID)
	if err != nil {
		return WorkspaceRestoreResult{}, false, err
	}
	digest := runmutation.OperationKeyDigest("workspace_restore_operation.v1."+
		string(kind), binding.run.ID, operationKey)
	transaction, found, err := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx,
		digest)
	if err != nil || !found {
		return WorkspaceRestoreResult{}, false, apperror.Normalize(err)
	}
	result, err := s.Restore(ctx, WorkspaceRestoreRequest{RunID: runID,
		TargetCheckpointID:          transaction.TargetCheckpointID,
		ExpectedCurrentCheckpointID: transaction.ExpectedCurrentCheckpointID,
		OperationKey:                operationKey,
		RequestedBy:                 requestedBy,
		Kind:                        kind,
		TriggerReceiptID:            transaction.TriggerReceiptID,
		Confirm:                     true})
	return result, true, err
}

func (s *WorkspaceCheckpointService) Reconcile(ctx context.Context) (int, error) {
	return s.reconcile(ctx, nil)
}

// Recovery selects a service from the transaction's durable workspace identity,
// rather than reinterpreting historical transactions through today's Run target.
func (s *WorkspaceCheckpointService) reconcile(ctx context.Context,
	resolve func(context.Context, workspacecheckpoint.Transaction) (*WorkspaceCheckpointService, error),
) (int, error) {
	if s == nil || s.store == nil {
		return 0, apperror.New(apperror.CodeFailedPrecondition,
			"workspace checkpoint service is unavailable")
	}
	reconciled := 0
	for {
		transactions, err := s.store.ListOpenWorkspaceCheckpointTransactions(ctx,
			workspaceCheckpointListLimit)
		if err != nil {
			return reconciled, apperror.Normalize(err)
		}
		if len(transactions) == 0 {
			break
		}
		processed := 0
		for _, transaction := range transactions {
			scoped := s
			if resolve != nil {
				var err error
				scoped, err = resolve(ctx, transaction)
				if err != nil {
					return reconciled, err
				}
			}
			if scoped == nil {
				continue // The exact lifecycle retains this unresolved operation.
			}
			processed++
			if err := scoped.requireWorkspaceCheckpointReconciliationQuiescence(ctx,
				transaction.RunID); err != nil {
				return reconciled, err
			}
			if transaction.Kind == workspacecheckpoint.TransactionFork {
				if err := scoped.reconcileForkTransaction(ctx, transaction); err != nil {
					return reconciled, err
				}
				reconciled++
				continue
			}
			binding, bindErr := scoped.loadBinding(ctx, transaction.RunID)
			if bindErr != nil {
				return reconciled, bindErr
			}
			if binding.workspace.ID != transaction.WorkspaceID {
				return reconciled, apperror.New(apperror.CodeConflict,
					"checkpoint recovery cannot change the recorded workspace")
			}
			before, getErr := scoped.store.GetWorkspaceCheckpointSnapshot(ctx,
				transaction.BeforeCheckpointID)
			if getErr != nil {
				return reconciled, apperror.Normalize(getErr)
			}
			state, stateFound, stateErr := scoped.store.GetWorkspaceCheckpointRunState(ctx,
				binding.run.ID)
			if stateErr != nil {
				return reconciled, apperror.Normalize(stateErr)
			}
			if !stateFound {
				return reconciled, apperror.New(apperror.CodeConflict,
					"workspace checkpoint cursor disappeared before restart reconciliation")
			}
			if state.CurrentCheckpointID == transaction.ExpectedCurrentCheckpointID &&
				state.CurrentCheckpointID != transaction.BeforeCheckpointID {
				state, _, stateErr = scoped.store.AdvanceWorkspaceCheckpointRunState(ctx,
					workspacecheckpoint.RunState{RunID: binding.run.ID,
						WorkspaceID:         binding.workspace.ID,
						CurrentCheckpointID: transaction.BeforeCheckpointID,
						LastTransactionID:   "", UpdatedAt: scoped.now().UTC()},
					state.CurrentCheckpointID)
				if stateErr != nil {
					return reconciled, apperror.Normalize(stateErr)
				}
			}
			if state.CurrentCheckpointID != transaction.BeforeCheckpointID {
				return reconciled, apperror.New(apperror.CodeConflict,
					"workspace checkpoint cursor moved before restart reconciliation")
			}
			incompleteReasons := append([]string{}, before.Checkpoint.IncompleteReasons...)
			incompleteReasons = append(incompleteReasons,
				"process restart interrupted the mutation boundary")
			observed, _, captureErr := scoped.capture(ctx, binding, workspacecheckpoint.CaptureRequest{
				ID:                   workspaceCheckpointID(transaction.OperationKeyDigest, "reconciled"),
				AttemptID:            before.Checkpoint.AttemptID,
				CapabilityGeneration: before.Checkpoint.CapabilityGeneration,
				Trigger:              workspacecheckpoint.TriggerRewindResult,
				Phase:                workspacecheckpoint.PhaseAfter,
				TriggerReceiptID:     transaction.ID,
				ParentCheckpointID:   before.Checkpoint.ID,
				IncompleteReasons:    incompleteReasons,
				CreatedAt:            scoped.now().UTC()})
			if captureErr != nil {
				return reconciled, captureErr
			}
			completedAt := scoped.now().UTC()
			transaction.Status = workspacecheckpoint.TransactionInterrupted
			transaction.AfterCheckpointID = observed.Checkpoint.ID
			transaction.RecoveryLevel = weakestWorkspaceRecovery(transaction.RecoveryLevel,
				observed.Checkpoint.RecoveryLevel)
			transaction.ErrorCode = "process_restart_reconciliation"
			transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
			transaction, _, updateErr := scoped.store.UpdateWorkspaceCheckpointTransaction(ctx,
				transaction)
			if updateErr != nil {
				return reconciled, apperror.Normalize(updateErr)
			}
			if state.CurrentCheckpointID != observed.Checkpoint.ID {
				if _, _, stateErr = scoped.store.AdvanceWorkspaceCheckpointRunState(ctx,
					workspacecheckpoint.RunState{RunID: binding.run.ID,
						WorkspaceID:         binding.workspace.ID,
						CurrentCheckpointID: observed.Checkpoint.ID,
						LastTransactionID:   transaction.ID, UpdatedAt: scoped.now().UTC()},
					transaction.BeforeCheckpointID); stateErr != nil {
					return reconciled, apperror.Normalize(stateErr)
				}
			}
			reconciled++
		}
		if processed == 0 || len(transactions) < workspaceCheckpointListLimit {
			break
		}
	}
	for {
		transactions, err := s.store.ListWorkspaceCheckpointTransactionsPendingCursor(ctx,
			workspaceCheckpointListLimit)
		if err != nil {
			return reconciled, apperror.Normalize(err)
		}
		if len(transactions) == 0 {
			return reconciled, nil
		}
		processed := 0
		for _, transaction := range transactions {
			scoped := s
			if resolve != nil {
				var err error
				scoped, err = resolve(ctx, transaction)
				if err != nil {
					return reconciled, err
				}
			}
			if scoped == nil {
				continue
			}
			processed++
			if err := scoped.requireWorkspaceCheckpointReconciliationQuiescence(ctx,
				transaction.RunID); err != nil {
				return reconciled, err
			}
			if _, _, err := scoped.store.AdvanceWorkspaceCheckpointRunState(ctx,
				workspacecheckpoint.RunState{RunID: transaction.RunID,
					WorkspaceID:         transaction.WorkspaceID,
					CurrentCheckpointID: transaction.AfterCheckpointID,
					LastTransactionID:   transaction.ID, UpdatedAt: scoped.now().UTC()},
				transaction.BeforeCheckpointID); err != nil {
				return reconciled, apperror.Normalize(err)
			}
			reconciled++
		}
		if processed == 0 || len(transactions) < workspaceCheckpointListLimit {
			return reconciled, nil
		}
	}
}

func (s *WorkspaceCheckpointService) requireWorkspaceCheckpointReconciliationQuiescence(
	ctx context.Context, runID string,
) error {
	lease, found, err := s.store.GetRunExecutionLease(ctx, runID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if found && lease.ActiveAt(s.now().UTC()) {
		return apperror.New(apperror.CodeConflict,
			"workspace checkpoint reconciliation requires an expired or released execution lease")
	}
	return nil
}

func (s *WorkspaceCheckpointService) reconcileForkTransaction(ctx context.Context,
	transaction workspacecheckpoint.Transaction,
) error {
	forkStore, ok := s.store.(WorkspaceCheckpointForkStore)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition,
			"workspace checkpoint fork store is unavailable")
	}
	target, err := s.store.GetWorkspaceCheckpoint(ctx, transaction.TargetCheckpointID)
	if err != nil {
		return apperror.Normalize(err)
	}
	run, err := forkStore.GetRun(ctx, transaction.TriggerReceiptID)
	if err == nil {
		_, err = s.completeExistingFork(ctx, forkStore, transaction, target, run)
		return err
	}
	if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		return apperror.Normalize(err)
	}
	binding, err := s.loadBinding(ctx, transaction.RunID)
	if err != nil {
		return err
	}
	if err = s.cleanupInterruptedWorkspaceFork(ctx, binding.workspace.RootPath,
		transaction.ForkWorkspaceRoot, transaction.ForkBranch, target); err != nil {
		// Keep the transaction open so a later startup can retry. A drifted
		// worktree or branch is never deleted speculatively.
		return apperror.Normalize(err)
	}
	return s.markForkTransactionFailure(ctx, transaction,
		"process_restart_reconciliation", true)
}

func (s *WorkspaceCheckpointService) cleanupInterruptedWorkspaceFork(ctx context.Context,
	sourceRoot, destinationRoot, branch string, target workspacecheckpoint.Checkpoint,
) error {
	if _, err := os.Lstat(destinationRoot); err == nil {
		observed, captureErr := workspacecheckpoint.Capture(ctx,
			workspacecheckpoint.CaptureRequest{ID: idgen.New("workspace-fork-cleanup"),
				RunID: target.RunID, MissionID: target.MissionID,
				SessionID: target.SessionID, WorkspaceID: target.WorkspaceID,
				WorkspaceRoot: destinationRoot, Trigger: workspacecheckpoint.TriggerFork,
				Phase:            workspacecheckpoint.PhasePreflight,
				TriggerReceiptID: target.ID, CreatedAt: s.now().UTC()})
		if captureErr != nil {
			return apperror.Normalize(captureErr)
		}
		if observed.Checkpoint.ManifestSHA256 != target.ManifestSHA256 ||
			observed.Checkpoint.IndexSHA256 != target.IndexSHA256 ||
			observed.Checkpoint.BaseCommit != target.BaseCommit ||
			observed.Checkpoint.Branch != branch {
			return fmt.Errorf("interrupted fork Workspace changed before cleanup: "+
				"manifest %s/%s index %s/%s commit %s/%s branch %s/%s",
				observed.Checkpoint.ManifestSHA256, target.ManifestSHA256,
				observed.Checkpoint.IndexSHA256, target.IndexSHA256,
				observed.Checkpoint.BaseCommit, target.BaseCommit,
				observed.Checkpoint.Branch, branch)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect interrupted fork Workspace: %w", err)
	}
	return repository.CleanupInterruptedWorktree(ctx, sourceRoot, destinationRoot,
		branch, target.BaseCommit)
}

func (s *WorkspaceCheckpointService) completeForkTransaction(ctx context.Context,
	transaction workspacecheckpoint.Transaction,
) (workspacecheckpoint.Transaction, error) {
	completedAt := s.now().UTC()
	transaction.Status = workspacecheckpoint.TransactionCompleted
	transaction.AfterCheckpointID = transaction.TargetCheckpointID
	transaction.ErrorCode = ""
	transaction.ConflictJSON = "[]"
	transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
	value, _, err := s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
	return value, apperror.Normalize(err)
}

func (s *WorkspaceCheckpointService) failForkTransaction(ctx context.Context,
	transaction workspacecheckpoint.Transaction, code string, cause error, interrupted bool,
) error {
	return errors.Join(apperror.Normalize(cause),
		s.markForkTransactionFailure(ctx, transaction, code, interrupted))
}

func (s *WorkspaceCheckpointService) markForkTransactionFailure(ctx context.Context,
	transaction workspacecheckpoint.Transaction, code string, interrupted bool,
) error {
	completedAt := s.now().UTC()
	transaction.Status = workspacecheckpoint.TransactionFailed
	if interrupted {
		transaction.Status = workspacecheckpoint.TransactionInterrupted
	}
	transaction.AfterCheckpointID = transaction.BeforeCheckpointID
	transaction.RecoveryLevel = workspacecheckpoint.RecoveryUnavailable
	transaction.ErrorCode = code
	transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
	_, _, updateErr := s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
	return apperror.Normalize(updateErr)
}

func (s *WorkspaceCheckpointService) completeExistingFork(ctx context.Context,
	store WorkspaceCheckpointForkStore, transaction workspacecheckpoint.Transaction,
	target workspacecheckpoint.Checkpoint, run domain.Run,
) (WorkspaceForkResult, error) {
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, run.ID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	if !found {
		binding, bindErr := s.loadBinding(ctx, run.ID)
		if bindErr != nil {
			return WorkspaceForkResult{}, bindErr
		}
		checkpoint, _, captureErr := s.capture(ctx, binding,
			workspacecheckpoint.CaptureRequest{
				ID:               workspaceCheckpointID(transaction.OperationKeyDigest, "fork"),
				Trigger:          workspacecheckpoint.TriggerFork,
				Phase:            workspacecheckpoint.PhaseStandalone,
				TriggerReceiptID: transaction.ID, CreatedAt: s.now().UTC()})
		if captureErr != nil {
			return WorkspaceForkResult{}, captureErr
		}
		if verifyErr := verifyWorkspaceForkCheckpoint(ctx, store, transaction,
			target, run, checkpoint.Checkpoint); verifyErr != nil {
			return WorkspaceForkResult{}, s.failForkTransaction(ctx, transaction,
				"fork_checkpoint_failed", verifyErr, true)
		}
		transaction.RecoveryLevel = weakestWorkspaceRecovery(transaction.RecoveryLevel,
			checkpoint.Checkpoint.RecoveryLevel)
		state = workspacecheckpoint.RunState{RunID: run.ID,
			WorkspaceID:         binding.workspace.ID,
			CurrentCheckpointID: checkpoint.Checkpoint.ID,
			LastTransactionID:   "", UpdatedAt: s.now().UTC()}
		if _, _, err = s.store.AdvanceWorkspaceCheckpointRunState(ctx, state, ""); err != nil {
			return WorkspaceForkResult{}, apperror.Normalize(err)
		}
	} else {
		checkpoint, getErr := s.store.GetWorkspaceCheckpoint(ctx, state.CurrentCheckpointID)
		if getErr != nil {
			return WorkspaceForkResult{}, apperror.Normalize(getErr)
		}
		if verifyErr := verifyWorkspaceForkCheckpoint(ctx, store, transaction,
			target, run, checkpoint); verifyErr != nil {
			return WorkspaceForkResult{}, s.failForkTransaction(ctx, transaction,
				"fork_checkpoint_failed", verifyErr, true)
		}
		transaction.RecoveryLevel = weakestWorkspaceRecovery(transaction.RecoveryLevel,
			checkpoint.RecoveryLevel)
	}
	transaction, err = s.completeForkTransaction(ctx, transaction)
	if err != nil {
		return WorkspaceForkResult{}, err
	}
	return s.workspaceForkResult(ctx, store, transaction, target, run, state, false)
}

func verifyWorkspaceForkCheckpoint(ctx context.Context, store WorkspaceCheckpointForkStore,
	transaction workspacecheckpoint.Transaction, target workspacecheckpoint.Checkpoint,
	run domain.Run, checkpoint workspacecheckpoint.Checkpoint,
) error {
	if checkpoint.RunID != run.ID || checkpoint.Trigger != workspacecheckpoint.TriggerFork ||
		checkpoint.TriggerReceiptID != transaction.ID ||
		checkpoint.ManifestSHA256 != target.ManifestSHA256 ||
		checkpoint.IndexSHA256 != target.IndexSHA256 ||
		checkpoint.BaseCommit != target.BaseCommit {
		return errors.New("forked Workspace checkpoint does not match the historical target")
	}
	nodes, err := store.ListSessionContinuityNodes(ctx, run.SessionID, 2_000)
	if err != nil {
		return apperror.Normalize(err)
	}
	for _, node := range nodes {
		if node.RunID == run.ID && node.Kind == contextmgr.ContinuityNodeFork {
			if node.WorkspaceID != checkpoint.WorkspaceID || node.GitBranch == "" ||
				node.GitBranch != checkpoint.Branch || node.GitHead != checkpoint.BaseCommit {
				return errors.New("forked Workspace drifted from its continuity identity")
			}
			return nil
		}
	}
	return errors.New("forked Run continuity identity is unavailable")
}

func (s *WorkspaceCheckpointService) replayFork(ctx context.Context,
	store WorkspaceCheckpointForkStore, transaction workspacecheckpoint.Transaction,
	target workspacecheckpoint.Checkpoint,
) (WorkspaceForkResult, error) {
	if transaction.Status != workspacecheckpoint.TransactionCompleted {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace fork operation is terminal but incomplete")
	}
	run, err := store.GetRun(ctx, transaction.TriggerReceiptID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, run.ID)
	if err != nil || !found {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeConflict,
			"completed workspace fork lost its checkpoint cursor")
	}
	return s.workspaceForkResult(ctx, store, transaction, target, run, state, true)
}

func (s *WorkspaceCheckpointService) workspaceForkResult(ctx context.Context,
	store WorkspaceCheckpointForkStore, transaction workspacecheckpoint.Transaction,
	target workspacecheckpoint.Checkpoint, run domain.Run,
	state workspacecheckpoint.RunState, replayed bool,
) (WorkspaceForkResult, error) {
	mission, err := store.GetMission(ctx, run.MissionID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	workspace, err := store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	checkpoint, err := s.store.GetWorkspaceCheckpoint(ctx, state.CurrentCheckpointID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	nodes, err := store.ListSessionContinuityNodes(ctx, run.SessionID, 2_000)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	var node contextmgr.ContinuityNode
	for _, value := range nodes {
		if value.RunID == run.ID && value.Kind == contextmgr.ContinuityNodeFork {
			node = value
			break
		}
	}
	if node.ID == "" {
		return WorkspaceForkResult{}, apperror.New(apperror.CodeConflict,
			"forked Run continuity node is unavailable")
	}
	return WorkspaceForkResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
		SourceRunID: transaction.RunID, Target: target,
		Workspace: session.WorkspaceRecord{ID: workspace.ID, Name: workspace.Name,
			RootPath: workspace.RootPath}, Mission: mission, Run: run, Node: node,
		Checkpoint: checkpoint, Transaction: transaction,
		NotInherited: []string{"approvals", "capability grants", "credentials",
			"debug sessions", "execution leases", "network authorization", "processes",
			"terminal leases", "execution profiles"}, Replayed: replayed}, nil
}

func (s *WorkspaceCheckpointService) resumeRestore(ctx context.Context,
	binding workspaceCheckpointBinding, request WorkspaceRestoreRequest,
	transaction workspacecheckpoint.Transaction, target workspacecheckpoint.Snapshot,
) (WorkspaceRestoreResult, error) {
	before, err := s.store.GetWorkspaceCheckpointSnapshot(ctx,
		transaction.BeforeCheckpointID)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	observedRequest := s.workspaceCaptureRequest(binding, workspacecheckpoint.CaptureRequest{
		ID:               idgen.New("workspace-restore-observed"),
		Trigger:          workspacecheckpoint.TriggerRewindPreflight,
		Phase:            workspacecheckpoint.PhasePreflight,
		TriggerReceiptID: transaction.ID, ParentCheckpointID: before.Checkpoint.ID,
		CreatedAt: s.now().UTC()})
	observed, err := workspacecheckpoint.Capture(ctx, observedRequest)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	preview, err := workspacecheckpoint.PreviewRestore(before, target, observed)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	if len(preview.Conflicts) != 0 {
		persisted, _, captureErr := s.capture(ctx, binding,
			workspacecheckpoint.CaptureRequest{
				ID:                 workspaceCheckpointID(transaction.OperationKeyDigest, "conflict"),
				Trigger:            workspacecheckpoint.TriggerRewindResult,
				Phase:              workspacecheckpoint.PhaseAfter,
				TriggerReceiptID:   transaction.ID,
				ParentCheckpointID: before.Checkpoint.ID,
				IncompleteReasons:  []string{"external change blocked Workspace restore"},
				CreatedAt:          s.now().UTC()})
		if captureErr != nil {
			return WorkspaceRestoreResult{}, captureErr
		}
		return s.finishRestoreFailureWithAfter(ctx, binding, transaction, before,
			persisted, preview,
			&workspacecheckpoint.ConflictError{Conflicts: preview.Conflicts})
	}
	if err := s.requireRestoreAuthority(ctx, binding, request.RequestedBy); err != nil {
		return WorkspaceRestoreResult{}, err
	}
	if transaction.Status == workspacecheckpoint.TransactionPrepared {
		transaction.Status = workspacecheckpoint.TransactionApplying
		transaction.UpdatedAt = s.now().UTC()
		transaction, _, err = s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
		if err != nil {
			return WorkspaceRestoreResult{}, apperror.Normalize(err)
		}
	}
	_, applyErr := workspacecheckpoint.ApplyRestore(ctx, binding.workspace.RootPath,
		before, target, observed)
	resultSnapshot, _, captureErr := s.capture(ctx, binding,
		workspacecheckpoint.CaptureRequest{
			ID:               workspaceCheckpointID(transaction.OperationKeyDigest, "result"),
			Trigger:          workspacecheckpoint.TriggerRewindResult,
			Phase:            workspacecheckpoint.PhaseAfter,
			TriggerReceiptID: transaction.ID, ParentCheckpointID: before.Checkpoint.ID,
			CreatedAt: s.now().UTC()})
	if captureErr != nil {
		return WorkspaceRestoreResult{}, errors.Join(apperror.Normalize(applyErr), captureErr)
	}
	if applyErr != nil {
		previewAfter, _ := workspacecheckpoint.PreviewRestore(before, target, resultSnapshot)
		return s.finishRestoreFailureWithAfter(ctx, binding, transaction, before,
			resultSnapshot, previewAfter, applyErr)
	}
	verification, verifyErr := workspacecheckpoint.PreviewRestore(target, target,
		resultSnapshot)
	if verifyErr != nil || len(verification.Conflicts) != 0 ||
		resultSnapshot.Checkpoint.IndexSHA256 != target.Checkpoint.IndexSHA256 ||
		resultSnapshot.Checkpoint.ManifestSHA256 != target.Checkpoint.ManifestSHA256 {
		if verifyErr == nil {
			verifyErr = &workspacecheckpoint.ConflictError{Conflicts: verification.Conflicts}
		}
		return s.finishRestoreFailureWithAfter(ctx, binding, transaction, before,
			resultSnapshot, verification, errors.Join(
				errors.New("workspace restore final verification failed"), verifyErr))
	}
	completedAt := s.now().UTC()
	transaction.Status = workspacecheckpoint.TransactionCompleted
	transaction.AfterCheckpointID = resultSnapshot.Checkpoint.ID
	transaction.RecoveryLevel = weakestWorkspaceRecovery(target.Checkpoint.RecoveryLevel,
		resultSnapshot.Checkpoint.RecoveryLevel)
	transaction.ErrorCode = ""
	transaction.ConflictJSON = "[]"
	transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
	transaction, replayed, err := s.store.UpdateWorkspaceCheckpointTransaction(ctx,
		transaction)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	if err := s.advanceRestoreCursor(ctx, binding, transaction, resultSnapshot.Checkpoint.ID); err != nil {
		return WorkspaceRestoreResult{}, err
	}
	after := resultSnapshot.Checkpoint
	return WorkspaceRestoreResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
		Preview: preview, Transaction: &transaction, Before: before.Checkpoint,
		After: &after, Confirmed: true, Replayed: replayed}, nil
}

func (s *WorkspaceCheckpointService) finishRestoreFailureWithAfter(ctx context.Context,
	binding workspaceCheckpointBinding, transaction workspacecheckpoint.Transaction,
	before, after workspacecheckpoint.Snapshot, preview workspacecheckpoint.Preview, cause error,
) (WorkspaceRestoreResult, error) {
	wasApplying := transaction.Status == workspacecheckpoint.TransactionApplying
	completedAt := s.now().UTC()
	transaction.Status = workspacecheckpoint.TransactionFailed
	if wasApplying ||
		after.Checkpoint.ManifestSHA256 != before.Checkpoint.ManifestSHA256 ||
		after.Checkpoint.IndexSHA256 != before.Checkpoint.IndexSHA256 {
		transaction.Status = workspacecheckpoint.TransactionInterrupted
	}
	transaction.AfterCheckpointID = after.Checkpoint.ID
	transaction.RecoveryLevel = workspacecheckpoint.RecoveryUnavailable
	transaction.ErrorCode = "restore_failed"
	transaction.ConflictJSON = workspaceConflictJSON(preview.Conflicts)
	transaction.UpdatedAt, transaction.CompletedAt = completedAt, &completedAt
	transaction, _, updateErr := s.store.UpdateWorkspaceCheckpointTransaction(ctx, transaction)
	if updateErr == nil {
		updateErr = s.advanceRestoreCursor(ctx, binding, transaction, after.Checkpoint.ID)
	}
	checkpoint := after.Checkpoint
	result := WorkspaceRestoreResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
		Preview: preview, Transaction: &transaction, Before: before.Checkpoint,
		After: &checkpoint, Confirmed: true}
	return result, errors.Join(apperror.Normalize(cause), apperror.Normalize(updateErr))
}

func (s *WorkspaceCheckpointService) advanceRestoreCursor(ctx context.Context,
	binding workspaceCheckpointBinding, transaction workspacecheckpoint.Transaction,
	afterCheckpointID string,
) error {
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("workspace restore cursor disappeared")
		}
		return apperror.Normalize(err)
	}
	if state.CurrentCheckpointID == afterCheckpointID &&
		state.LastTransactionID == transaction.ID {
		return nil
	}
	if state.CurrentCheckpointID != transaction.BeforeCheckpointID {
		return apperror.New(apperror.CodeConflict,
			"workspace restore cursor changed while applying")
	}
	_, _, err = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			CurrentCheckpointID: afterCheckpointID, LastTransactionID: transaction.ID,
			UpdatedAt: s.now().UTC()}, transaction.BeforeCheckpointID)
	return apperror.Normalize(err)
}

func (s *WorkspaceCheckpointService) replayBoundary(ctx context.Context,
	binding workspaceCheckpointBinding, transaction workspacecheckpoint.Transaction,
) (WorkspaceMutationBoundary, error) {
	before, err := s.store.GetWorkspaceCheckpoint(ctx, transaction.BeforeCheckpointID)
	if err != nil {
		return WorkspaceMutationBoundary{}, apperror.Normalize(err)
	}
	result := WorkspaceMutationBoundary{Transaction: transaction, Before: before, Replayed: true}
	if s.strictMutationReplay && transaction.Status.Terminal() &&
		(transaction.Status != workspacecheckpoint.TransactionCompleted ||
			transaction.AfterCheckpointID == transaction.BeforeCheckpointID) {
		return WorkspaceMutationBoundary{}, apperror.New(apperror.CodeConflict,
			"an unsuccessful mutation boundary cannot authorize another file write")
	}
	if !transaction.Status.Terminal() {
		state, stateFound, stateErr := s.store.GetWorkspaceCheckpointRunState(ctx,
			binding.run.ID)
		if stateErr != nil {
			return WorkspaceMutationBoundary{}, apperror.Normalize(stateErr)
		}
		if !stateFound || state.CurrentCheckpointID != before.ID {
			expected := transaction.ExpectedCurrentCheckpointID
			if _, _, stateErr = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
				workspacecheckpoint.RunState{RunID: binding.run.ID,
					WorkspaceID: binding.workspace.ID, CurrentCheckpointID: before.ID,
					LastTransactionID: "", UpdatedAt: s.now().UTC()},
				expected); stateErr != nil {
				return WorkspaceMutationBoundary{}, apperror.Normalize(stateErr)
			}
		}
	}
	if transaction.AfterCheckpointID != "" {
		after, getErr := s.store.GetWorkspaceCheckpoint(ctx, transaction.AfterCheckpointID)
		if getErr != nil {
			return WorkspaceMutationBoundary{}, apperror.Normalize(getErr)
		}
		result.After = &after
		if transaction.Status.Terminal() {
			if s.strictMutationReplay {
				// The owning lifecycle can replay its immutable cursor receipt even
				// after later edits. A failed attribution is never a successful apply.
				if _, _, err := s.store.AdvanceWorkspaceCheckpointRunState(ctx,
					workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
						CurrentCheckpointID: after.ID, LastTransactionID: transaction.ID,
						UpdatedAt: s.now().UTC()}, transaction.BeforeCheckpointID); err != nil {
					return WorkspaceMutationBoundary{}, apperror.Normalize(err)
				}
			} else {
				_ = s.advanceBoundaryReplayCursor(ctx, binding, transaction, after.ID)
			}
		}
	}
	return result, nil
}

func (s *WorkspaceCheckpointService) advanceBoundaryReplayCursor(ctx context.Context,
	binding workspaceCheckpointBinding, transaction workspacecheckpoint.Transaction,
	afterCheckpointID string,
) error {
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, binding.run.ID)
	if err != nil || !found {
		return apperror.Normalize(err)
	}
	if state.CurrentCheckpointID == afterCheckpointID {
		return nil
	}
	if state.CurrentCheckpointID != transaction.BeforeCheckpointID {
		return apperror.New(apperror.CodeConflict,
			"workspace mutation cursor changed after transaction completion")
	}
	_, _, err = s.store.AdvanceWorkspaceCheckpointRunState(ctx,
		workspacecheckpoint.RunState{RunID: binding.run.ID, WorkspaceID: binding.workspace.ID,
			CurrentCheckpointID: afterCheckpointID, LastTransactionID: transaction.ID,
			UpdatedAt: s.now().UTC()}, transaction.BeforeCheckpointID)
	return apperror.Normalize(err)
}

func (s *WorkspaceCheckpointService) replayRestore(ctx context.Context,
	transaction workspacecheckpoint.Transaction,
) (WorkspaceRestoreResult, error) {
	binding, err := s.loadBinding(ctx, transaction.RunID)
	if err != nil {
		return WorkspaceRestoreResult{}, err
	}
	before, err := s.store.GetWorkspaceCheckpointSnapshot(ctx,
		transaction.BeforeCheckpointID)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	target, err := s.store.GetWorkspaceCheckpointSnapshot(ctx,
		transaction.TargetCheckpointID)
	if err != nil {
		return WorkspaceRestoreResult{}, apperror.Normalize(err)
	}
	after := before
	if transaction.AfterCheckpointID != "" {
		after, err = s.store.GetWorkspaceCheckpointSnapshot(ctx,
			transaction.AfterCheckpointID)
		if err != nil {
			return WorkspaceRestoreResult{}, apperror.Normalize(err)
		}
		state, stateFound, stateErr := s.store.GetWorkspaceCheckpointRunState(ctx,
			binding.run.ID)
		if stateErr != nil {
			return WorkspaceRestoreResult{}, apperror.Normalize(stateErr)
		}
		if stateFound && state.CurrentCheckpointID == transaction.BeforeCheckpointID {
			if err := s.advanceRestoreCursor(ctx, binding, transaction,
				transaction.AfterCheckpointID); err != nil {
				return WorkspaceRestoreResult{}, err
			}
		}
	}
	preview, _ := workspacecheckpoint.PreviewRestore(before, target, after)
	checkpoint := after.Checkpoint
	return WorkspaceRestoreResult{ProtocolVersion: WorkspaceCheckpointAPIProtocolVersion,
		Preview: preview, Transaction: &transaction, Before: before.Checkpoint,
		After: &checkpoint, Confirmed: true, Replayed: true}, nil
}

func (s *WorkspaceCheckpointService) findUndoSource(ctx context.Context,
	runID string,
) (workspacecheckpoint.Transaction, error) {
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, runID)
	if err != nil || !found {
		return workspacecheckpoint.Transaction{}, apperror.New(
			apperror.CodeFailedPrecondition, "workspace checkpoint cursor is not initialized")
	}
	// A final delivery checkpoint is a read-only recovery anchor. Follow its
	// bounded manual-parent chain so it does not hide the most recent reversible
	// mutation from Undo.
	cursorID := state.CurrentCheckpointID
	for depth := 0; depth < 64; depth++ {
		checkpoint, checkpointErr := s.store.GetWorkspaceCheckpoint(ctx, cursorID)
		if checkpointErr != nil {
			return workspacecheckpoint.Transaction{}, apperror.Normalize(checkpointErr)
		}
		if checkpoint.RunID != runID || checkpoint.WorkspaceID != state.WorkspaceID {
			return workspacecheckpoint.Transaction{}, apperror.New(
				apperror.CodeConflict, "workspace checkpoint cursor escaped its Run binding")
		}
		if checkpoint.Trigger != workspacecheckpoint.TriggerManual ||
			checkpoint.Phase != workspacecheckpoint.PhaseStandalone ||
			checkpoint.ParentCheckpointID == "" {
			break
		}
		cursorID = checkpoint.ParentCheckpointID
	}
	transactions, err := s.store.ListWorkspaceCheckpointTransactions(ctx, runID, 2_000)
	if err != nil {
		return workspacecheckpoint.Transaction{}, apperror.Normalize(err)
	}
	for _, transaction := range transactions {
		if transaction.AfterCheckpointID == cursorID &&
			(transaction.Kind == workspacecheckpoint.TransactionFileTool ||
				transaction.Kind == workspacecheckpoint.TransactionCommandBatch ||
				transaction.Kind == workspacecheckpoint.TransactionGitMutation ||
				transaction.Kind == workspacecheckpoint.TransactionAgentMerge) {
			return transaction, nil
		}
	}
	return workspacecheckpoint.Transaction{}, apperror.New(apperror.CodeFailedPrecondition,
		"no reversible workspace mutation is at the current cursor")
}

func (s *WorkspaceCheckpointService) findRedoSource(ctx context.Context,
	runID string,
) (workspacecheckpoint.Transaction, workspacecheckpoint.Transaction, error) {
	state, found, err := s.store.GetWorkspaceCheckpointRunState(ctx, runID)
	if err != nil || !found {
		return workspacecheckpoint.Transaction{}, workspacecheckpoint.Transaction{},
			apperror.New(apperror.CodeFailedPrecondition,
				"workspace checkpoint cursor is not initialized")
	}
	transactions, err := s.store.ListWorkspaceCheckpointTransactions(ctx, runID, 2_000)
	if err != nil {
		return workspacecheckpoint.Transaction{}, workspacecheckpoint.Transaction{},
			apperror.Normalize(err)
	}
	for _, undo := range transactions {
		if undo.Kind != workspacecheckpoint.TransactionUndo ||
			undo.Status != workspacecheckpoint.TransactionCompleted ||
			undo.AfterCheckpointID != state.CurrentCheckpointID {
			continue
		}
		source, sourceFound, getErr := s.store.GetWorkspaceCheckpointTransaction(ctx,
			undo.TriggerReceiptID)
		if getErr != nil || !sourceFound || source.AfterCheckpointID == "" {
			return workspacecheckpoint.Transaction{}, workspacecheckpoint.Transaction{},
				apperror.New(apperror.CodeFailedPrecondition,
					"workspace redo source transaction is unavailable")
		}
		return undo, source, nil
	}
	return workspacecheckpoint.Transaction{}, workspacecheckpoint.Transaction{},
		apperror.New(apperror.CodeFailedPrecondition,
			"no workspace undo is available to redo")
}

func (s *WorkspaceCheckpointService) capture(ctx context.Context,
	binding workspaceCheckpointBinding, request workspacecheckpoint.CaptureRequest,
) (workspacecheckpoint.Snapshot, bool, error) {
	request = s.workspaceCaptureRequest(binding, request)
	snapshot, err := workspacecheckpoint.Capture(ctx, request)
	if err != nil {
		return workspacecheckpoint.Snapshot{}, false, apperror.Normalize(err)
	}
	_, replayed, err := s.store.CreateWorkspaceCheckpoint(ctx, snapshot)
	return snapshot, replayed, apperror.Normalize(err)
}

func (s *WorkspaceCheckpointService) workspaceCaptureRequest(
	binding workspaceCheckpointBinding, request workspacecheckpoint.CaptureRequest,
) workspacecheckpoint.CaptureRequest {
	request.RunID = binding.run.ID
	request.MissionID = binding.mission.ID
	request.SessionID = binding.session.ID
	request.WorkspaceID = binding.workspace.ID
	request.WorkspaceRoot = binding.workspace.RootPath
	return request
}

func (s *WorkspaceCheckpointService) loadBinding(ctx context.Context,
	runID string,
) (workspaceCheckpointBinding, error) {
	var value workspaceCheckpointBinding
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
	if s.runWorkspace != nil {
		resolved, found, resolveErr := s.runWorkspace(ctx, value.run.ID)
		if resolveErr != nil {
			return value, apperror.Normalize(resolveErr)
		}
		if found {
			value.workspace = resolved
			if value.run.MissionID != value.mission.ID ||
				value.run.SessionID != value.session.ID || value.mission.WorkspaceID == "" ||
				value.mission.WorkspaceID != value.session.WorkspaceID ||
				strings.TrimSpace(value.workspace.ID) == "" ||
				strings.TrimSpace(value.workspace.RootPath) == "" {
				return value, apperror.New(apperror.CodeFailedPrecondition,
					"resolved workspace checkpoint Run binding is invalid")
			}
			return value, nil
		}
	}
	if value.workspace, err = s.store.GetWorkspaceInfo(ctx,
		value.mission.WorkspaceID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.run.MissionID != value.mission.ID || value.run.SessionID != value.session.ID ||
		value.mission.WorkspaceID == "" ||
		value.mission.WorkspaceID != value.session.WorkspaceID ||
		value.mission.WorkspaceID != value.workspace.ID ||
		strings.TrimSpace(value.workspace.RootPath) == "" {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"workspace checkpoint Run binding is invalid")
	}
	return value, nil
}

func workspaceForkSourceNode(ctx context.Context, store WorkspaceCheckpointForkStore,
	run domain.Run,
) (string, error) {
	nodes, err := store.ListSessionContinuityNodes(ctx, run.SessionID, 2_000)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	for _, node := range nodes {
		if node.RunID == run.ID && node.Kind == contextmgr.ContinuityNodeRoot {
			return node.ID, nil
		}
	}
	return "", apperror.New(apperror.CodeFailedPrecondition,
		"source Run continuity root is unavailable")
}

func rekeyWorkspaceForkPreparation(prepared preparedRun, node contextmgr.ContinuityNode,
	runID string,
) (preparedRun, contextmgr.ContinuityNode, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return preparedRun{}, contextmgr.ContinuityNode{}, errors.New(
			"fork Run identity is invalid")
	}
	oldRunID := prepared.Run.ID
	prepared.Run.ID = runID
	prepared.Mode.RunID = runID
	for index := range prepared.InitialEvents {
		prepared.InitialEvents[index].RunID = runID
		if prepared.InitialEvents[index].SubjectID == oldRunID {
			prepared.InitialEvents[index].SubjectID = runID
		}
	}
	if err := prepared.Run.Validate(); err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, err
	}
	if err := prepared.Mode.Validate(); err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, err
	}
	for _, event := range prepared.InitialEvents {
		if err := event.Validate(); err != nil {
			return preparedRun{}, contextmgr.ContinuityNode{}, err
		}
	}
	rebound, err := contextmgr.NewContinuityNode(node.ID, node.Kind,
		prepared.Run.SessionID, prepared.Run.ID, prepared.Mission.WorkspaceID,
		node.ParentID, node.SourceNodeID, node.Title, node.Summary, node.CreatedBy,
		node.Snapshot, node.CreatedAt)
	return prepared, rebound, err
}

func materializeWorkspaceFork(ctx context.Context, sourceRoot, destinationRoot,
	branch string, target workspacecheckpoint.Snapshot,
) (string, error) {
	createdRoot, err := repository.CreateWorktree(ctx, sourceRoot, destinationRoot,
		branch, target.Checkpoint.BaseCommit)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	cleanupOnError := func(cause error) (string, error) {
		cleanupErr := repository.RemoveCreatedWorktree(context.WithoutCancel(ctx),
			sourceRoot, createdRoot, branch)
		return "", errors.Join(cause, cleanupErr)
	}
	now := time.Now().UTC()
	observed, err := workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{
		ID: idgen.New("workspace-fork-observed"), RunID: target.Checkpoint.RunID,
		MissionID: target.Checkpoint.MissionID, SessionID: target.Checkpoint.SessionID,
		WorkspaceID: target.Checkpoint.WorkspaceID, WorkspaceRoot: createdRoot,
		Trigger: workspacecheckpoint.TriggerFork, Phase: workspacecheckpoint.PhasePreflight,
		TriggerReceiptID: target.Checkpoint.ID, CreatedAt: now})
	if err != nil {
		return cleanupOnError(apperror.Normalize(err))
	}
	if observed.Checkpoint.BaseCommit != target.Checkpoint.BaseCommit ||
		observed.Checkpoint.Branch != branch {
		return cleanupOnError(errors.New("new Git worktree identity drifted before materialization"))
	}
	rebound := rebindWorkspaceForkTarget(target, observed.Checkpoint)
	if err := rebound.Validate(); err != nil {
		return cleanupOnError(err)
	}
	preview, err := workspacecheckpoint.PreviewRestore(observed, rebound, observed)
	if err != nil || len(preview.Conflicts) != 0 {
		return cleanupOnError(errors.Join(err,
			&workspacecheckpoint.ConflictError{Conflicts: preview.Conflicts}))
	}
	if _, err := workspacecheckpoint.ApplyRestore(ctx, createdRoot, observed, rebound,
		observed); err != nil {
		return cleanupOnError(err)
	}
	verified, err := workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{
		ID: idgen.New("workspace-fork-verified"), RunID: target.Checkpoint.RunID,
		MissionID: target.Checkpoint.MissionID, SessionID: target.Checkpoint.SessionID,
		WorkspaceID: target.Checkpoint.WorkspaceID, WorkspaceRoot: createdRoot,
		Trigger: workspacecheckpoint.TriggerFork, Phase: workspacecheckpoint.PhaseStandalone,
		TriggerReceiptID: target.Checkpoint.ID, CreatedAt: time.Now().UTC()})
	if err != nil || verified.Checkpoint.ManifestSHA256 != rebound.Checkpoint.ManifestSHA256 ||
		verified.Checkpoint.IndexSHA256 != rebound.Checkpoint.IndexSHA256 {
		if err == nil {
			err = errors.New("new Git worktree content failed exact verification")
		}
		return cleanupOnError(err)
	}
	return createdRoot, nil
}

func rebindWorkspaceForkTarget(target workspacecheckpoint.Snapshot,
	identity workspacecheckpoint.Checkpoint,
) workspacecheckpoint.Snapshot {
	checkpoint := identity
	checkpoint.ID = idgen.New("workspace-fork-target")
	checkpoint.IndexSHA256 = target.Checkpoint.IndexSHA256
	checkpoint.IndexBlobSHA256 = target.Checkpoint.IndexBlobSHA256
	checkpoint.ManifestSHA256 = target.Checkpoint.ManifestSHA256
	checkpoint.RecoveryLevel = target.Checkpoint.RecoveryLevel
	checkpoint.IncompleteReasons = append([]string{}, target.Checkpoint.IncompleteReasons...)
	checkpoint.EntryCount = target.Checkpoint.EntryCount
	checkpoint.StoredBytes = target.Checkpoint.StoredBytes
	return workspacecheckpoint.Snapshot{Checkpoint: checkpoint,
		Entries: append([]workspacecheckpoint.Entry{}, target.Entries...),
		Blobs:   append([]workspacecheckpoint.Blob{}, target.Blobs...)}
}

func (s *WorkspaceCheckpointService) requireBoundaryLease(ctx context.Context,
	binding workspaceCheckpointBinding, request WorkspaceMutationBoundaryRequest,
) error {
	lease, found, err := s.store.GetRunExecutionLease(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	operatorIdle := false
	if request.Kind == workspacecheckpoint.TransactionCommandBatch {
		operatorIdle = operatorCommandOwnsStoppedRun(ctx, binding.run, lease)
	}
	if s.operatorGitThreadID != "" {
		checker, ok := s.store.(interface {
			CheckThreadGitIdle(context.Context, string, string, *domain.RunExecutionLease) error
		})
		if !ok || request.Kind != workspacecheckpoint.TransactionGitMutation || !strings.HasPrefix(lease.OwnerID, "thread-git:") {
			return apperror.New(apperror.CodeConflict, "operator Git checkpoint authority is unavailable")
		}
		if err := checker.CheckThreadGitIdle(ctx, s.operatorGitThreadID, binding.run.ID, &lease); err != nil {
			return err
		}
		operatorIdle = true
	}
	if !found || (!operatorIdle && binding.run.Status != domain.RunRunning) ||
		binding.session.Status != session.StatusActive || lease.Status != domain.RunExecutionLeaseActive ||
		lease.LeaseID != request.LeaseID || lease.Generation != request.LeaseGeneration ||
		!lease.ExpiresAt.After(s.now().UTC()) {
		return apperror.New(apperror.CodeConflict,
			"workspace mutation execution lease is stale")
	}
	return nil
}

func (s *WorkspaceCheckpointService) requireRestoreAuthority(ctx context.Context,
	binding workspaceCheckpointBinding, requestedBy string,
) error {
	if normalizeWorkspaceCheckpointOperator(requestedBy) == "" {
		return apperror.New(apperror.CodePolicyDenied,
			"workspace restore requires an explicit operator")
	}
	if binding.run.Status != domain.RunPaused || binding.session.Status != session.StatusActive {
		return apperror.New(apperror.CodeFailedPrecondition,
			"workspace restore requires a paused Run and active Session")
	}
	mode, err := s.store.GetRunMode(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	lease, found, err := s.store.GetRunExecutionLease(ctx, binding.run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if found && lease.Status == domain.RunExecutionLeaseActive &&
		lease.ExpiresAt.After(s.now().UTC()) {
		return apperror.New(apperror.CodeConflict,
			"workspace restore requires a quiescent Run with no active execution lease")
	}
	if mode.RunID != binding.run.ID || mode.MissionID != binding.mission.ID ||
		mode.Surface != domain.ExecutionSurfaceCode || mode.Phase != domain.ExecutionPhaseDeliver ||
		permission.RunID != binding.run.ID || permission.MissionID != binding.mission.ID ||
		(permission.Mode.IsApprovalMode() && !s.capabilities.OperatorApprovalEnabled) ||
		!s.capabilities.AllowsSnapshot(permission) {
		return apperror.New(apperror.CodePolicyDenied,
			"workspace restore is not authorized by the current execution permission")
	}
	return nil
}

func normalizeWorkspaceMutationBoundaryRequest(
	request WorkspaceMutationBoundaryRequest,
) WorkspaceMutationBoundaryRequest {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.TriggerReceiptID = strings.TrimSpace(request.TriggerReceiptID)
	request.InvocationID = strings.TrimSpace(request.InvocationID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	request.CapabilityGeneration = strings.TrimSpace(request.CapabilityGeneration)
	request.LeaseID = strings.TrimSpace(request.LeaseID)
	for index := range request.IncompleteReasons {
		request.IncompleteReasons[index] = strings.TrimSpace(request.IncompleteReasons[index])
	}
	return request
}

func normalizeWorkspaceRestoreRequest(request WorkspaceRestoreRequest) WorkspaceRestoreRequest {
	request.RunID = strings.TrimSpace(request.RunID)
	request.TargetCheckpointID = strings.TrimSpace(request.TargetCheckpointID)
	request.ExpectedCurrentCheckpointID = strings.TrimSpace(request.ExpectedCurrentCheckpointID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeWorkspaceCheckpointOperator(request.RequestedBy)
	request.TriggerReceiptID = strings.TrimSpace(request.TriggerReceiptID)
	if request.Kind == "" {
		request.Kind = workspacecheckpoint.TransactionRewind
	}
	if request.TriggerReceiptID == "" {
		request.TriggerReceiptID = request.TargetCheckpointID
	}
	return request
}

func normalizeWorkspaceForkRequest(request WorkspaceForkRequest) WorkspaceForkRequest {
	request.RunID = strings.TrimSpace(request.RunID)
	request.TargetCheckpointID = strings.TrimSpace(request.TargetCheckpointID)
	request.ExpectedCurrentCheckpointID = strings.TrimSpace(
		request.ExpectedCurrentCheckpointID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeWorkspaceCheckpointOperator(request.RequestedBy)
	request.WorkspaceName = strings.TrimSpace(request.WorkspaceName)
	request.WorkspaceRoot = strings.TrimSpace(request.WorkspaceRoot)
	request.Branch = strings.TrimSpace(request.Branch)
	request.Goal = strings.TrimSpace(request.Goal)
	return request
}

func defaultWorkspaceForkRoot(sourceRoot, operationDigest string) (string, error) {
	if len(operationDigest) < 20 {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace fork operation identity is invalid")
	}
	source, err := filepath.Abs(strings.TrimSpace(sourceRoot))
	if err != nil || strings.TrimSpace(sourceRoot) == "" {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"source Workspace root is unavailable")
	}
	return filepath.Join(filepath.Dir(filepath.Clean(source)),
		"prayu-fork-"+operationDigest[:20]), nil
}

func normalizeWorkspaceCheckpointOperator(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "cli_operator", "desktop_operator", "api_operator", "run_supervisor":
		return value
	default:
		return ""
	}
}

func workspaceBoundaryKind(kind workspacecheckpoint.TransactionKind) bool {
	return kind == workspacecheckpoint.TransactionFileTool ||
		kind == workspacecheckpoint.TransactionCommandBatch ||
		kind == workspacecheckpoint.TransactionGitMutation ||
		kind == workspacecheckpoint.TransactionAgentMerge
}

func workspaceRestoreKind(kind workspacecheckpoint.TransactionKind) bool {
	return kind == workspacecheckpoint.TransactionRewind ||
		kind == workspacecheckpoint.TransactionUndo ||
		kind == workspacecheckpoint.TransactionRedo
}

func workspaceBoundaryTrigger(kind workspacecheckpoint.TransactionKind) workspacecheckpoint.TriggerKind {
	switch kind {
	case workspacecheckpoint.TransactionFileTool:
		return workspacecheckpoint.TriggerFileTool
	case workspacecheckpoint.TransactionCommandBatch:
		return workspacecheckpoint.TriggerCommandBatch
	case workspacecheckpoint.TransactionGitMutation:
		return workspacecheckpoint.TriggerGitMutation
	case workspacecheckpoint.TransactionAgentMerge:
		return workspacecheckpoint.TriggerAgentMerge
	default:
		return workspacecheckpoint.TriggerManual
	}
}

func workspaceBoundaryOperationDigest(runID string,
	kind workspacecheckpoint.TransactionKind, operationKey string,
) string {
	return runmutation.OperationKeyDigest("workspace_mutation_boundary.v1."+string(kind),
		runID, operationKey)
}

func workspaceBoundaryFingerprint(binding workspaceCheckpointBinding,
	request WorkspaceMutationBoundaryRequest,
) string {
	return runmutation.Fingerprint("workspace_mutation_boundary_request.v1",
		binding.run.ID, binding.mission.ID, binding.session.ID, binding.workspace.ID,
		string(request.Kind), request.TriggerReceiptID, request.InvocationID)
}

func embeddedWorkspaceCheckpointService(store any,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *WorkspaceCheckpointService {
	checkpointStore, ok := store.(WorkspaceCheckpointStore)
	if !ok {
		return nil
	}
	service, err := NewWorkspaceCheckpointService(checkpointStore, capabilities)
	if err != nil {
		return nil
	}
	return service
}

func workspaceCheckpointID(digest, phase string) string {
	sum := sha256.Sum256([]byte("workspace-checkpoint.v1\x00" + digest + "\x00" + phase))
	return "wcp-" + hex.EncodeToString(sum[:16])
}

func workspaceTransactionID(digest string) string {
	sum := sha256.Sum256([]byte("workspace-checkpoint-transaction.v1\x00" + digest))
	return "wctx-" + hex.EncodeToString(sum[:16])
}

func workspaceConflictJSON(conflicts []workspacecheckpoint.Conflict) string {
	value := (&workspacecheckpoint.ConflictError{Conflicts: conflicts}).ConflictJSON()
	if value == "" {
		return "[]"
	}
	return value
}

func weakestWorkspaceRecovery(left,
	right workspacecheckpoint.RecoveryLevel,
) workspacecheckpoint.RecoveryLevel {
	if left == workspacecheckpoint.RecoveryUnavailable ||
		right == workspacecheckpoint.RecoveryUnavailable {
		return workspacecheckpoint.RecoveryUnavailable
	}
	if left == workspacecheckpoint.RecoveryPartial ||
		right == workspacecheckpoint.RecoveryPartial {
		return workspacecheckpoint.RecoveryPartial
	}
	return workspacecheckpoint.RecoveryComplete
}
