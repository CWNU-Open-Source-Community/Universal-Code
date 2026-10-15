package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const DrydockAPIProtocolVersion = "drydock-api.v1"

type RunWorktreeStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)

	CreateDrydockTrust(context.Context, runworktree.Trust) (runworktree.Trust, bool, error)
	GetDrydockTrustByRun(context.Context, string) (runworktree.Trust, bool, error)
	PrepareDrydock(context.Context, runworktree.Workspace) (runworktree.Workspace, bool, error)
	AdvanceDrydock(context.Context, runworktree.Workspace, int64, runworktree.Receipt) (
		runworktree.Workspace, bool, error)
	CreateDrydockDelivery(context.Context, runworktree.DeliveryProposal, runworktree.Workspace,
		int64, runworktree.Receipt) (runworktree.DeliveryProposal, runworktree.Workspace, bool, error)
	GetDrydockByRun(context.Context, string) (runworktree.Workspace, bool, error)
	GetDrydock(context.Context, string) (runworktree.Workspace, bool, error)
	ListDrydocks(context.Context, runworktree.ListFilter) ([]runworktree.Workspace, error)
	GetDrydockReceiptByOperation(context.Context, string) (runworktree.Receipt, bool, error)
	ListDrydockReceipts(context.Context, string, int) ([]runworktree.Receipt, error)
	GetDrydockDelivery(context.Context, string) (runworktree.DeliveryProposal, bool, error)

	CreateWorkspaceCheckpoint(context.Context, workspacecheckpoint.Snapshot) (
		workspacecheckpoint.Checkpoint, bool, error)
	GetWorkspaceCheckpointSnapshot(context.Context, string) (workspacecheckpoint.Snapshot, error)
}

type RunWorktreeService struct {
	store       RunWorktreeStore
	executor    *repository.RunWorktreeExecutor
	checkpoints *WorkspaceCheckpointService
	now         func() time.Time
}

func NewRunWorktreeService(store RunWorktreeStore,
	executor *repository.RunWorktreeExecutor,
) (*RunWorktreeService, error) {
	if store == nil || executor == nil || !executor.Available() {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock requires durable storage and an available product-managed Git root")
	}
	return &RunWorktreeService{store: store, executor: executor,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *RunWorktreeService) WithCheckpointService(
	checkpoints *WorkspaceCheckpointService,
) *RunWorktreeService {
	if s == nil {
		return s
	}
	s.checkpoints = nil
	if checkpoints != nil {
		// Keep the caller's ordinary source-Workspace checkpoint service intact.
		// Drydock receives a private projection with the owned Workspace resolver.
		owned := *checkpoints
		s.checkpoints = &owned
		s.checkpoints.withRunWorkspaceResolver(func(ctx context.Context,
			runID string,
		) (session.WorkspaceInfo, bool, error) {
			workspace, found, err := readRunFileDrydock(ctx, s.store, runID)
			if err != nil || !found {
				return session.WorkspaceInfo{}, false, err
			}
			if workspace.State != runworktree.StateReady &&
				workspace.State != runworktree.StateDelivered {
				return session.WorkspaceInfo{}, false, nil
			}
			return session.WorkspaceInfo{ID: workspace.WorkspaceID,
				Name: workspace.Name, RootPath: workspace.Path}, true, nil
		})
	}
	return s
}

// ReconcileWorkspaceCheckpoints restores each transaction in its recorded
// workspace. Source history keeps its own cursor even after a Run owns a Drydock.
func (s *RunWorktreeService) ReconcileWorkspaceCheckpoints(ctx context.Context) (int, error) {
	if s == nil || s.checkpoints == nil {
		return 0, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock Workspace checkpoint recovery is unavailable")
	}
	source := *s.checkpoints
	source.runWorkspace = nil
	return source.reconcile(ctx, func(ctx context.Context, transaction workspacecheckpoint.Transaction) (*WorkspaceCheckpointService, error) {
		if (transaction.Kind == workspacecheckpoint.TransactionRewind || transaction.Kind == workspacecheckpoint.TransactionUndo) &&
			transaction.TriggerReceiptID == drydockReceiptID(transaction.OperationKeyDigest) {
			// This lifecycle owns its physical cursor. A restart must not turn
			// its unresolved request into a different restore or source cursor.
			if receipt, found, err := s.store.GetDrydockReceiptByOperation(ctx, transaction.OperationKeyDigest); err != nil {
				return nil, apperror.Normalize(err)
			} else if found {
				return nil, s.finishDrydockRestoreJournal(ctx, transaction.OperationKeyDigest, receipt)
			}
			return nil, nil
		}
		run, err := s.store.GetRun(ctx, transaction.RunID)
		if err != nil {
			return nil, err
		}
		mission, err := s.store.GetMission(ctx, run.MissionID)
		if err != nil {
			return nil, err
		}
		if transaction.WorkspaceID == mission.WorkspaceID {
			return &source, nil
		}
		resolved, err := ResolveRunFileWorkspace(ctx, s.checkpoints.store, run, mission, s)
		if err != nil {
			return nil, err
		}
		if resolved.Drydock == nil || transaction.WorkspaceID != resolved.Workspace.ID {
			return nil, apperror.New(apperror.CodeConflict,
				"checkpoint recovery transaction workspace does not match its owner")
		}
		// Older Drydock boundaries used the ordinary cursor table. Preserve that
		// recorded identity when it still points at this transaction; never adopt
		// a source cursor or a different Drydock boundary into the new file cursor.
		cursor, found, err := s.checkpoints.store.GetWorkspaceCheckpointRunState(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		if found && cursor.WorkspaceID == transaction.WorkspaceID &&
			(cursor.CurrentCheckpointID == transaction.BeforeCheckpointID ||
				cursor.CurrentCheckpointID == transaction.ExpectedCurrentCheckpointID) {
			legacy := *s.checkpoints
			legacy.withRunWorkspaceResolver(func(ctx context.Context, runID string) (session.WorkspaceInfo, bool, error) {
				if runID != run.ID {
					return session.WorkspaceInfo{}, false, apperror.New(apperror.CodeConflict,
						"legacy checkpoint recovery Run changed")
				}
				return resolved.Workspace, true, nil
			})
			return &legacy, nil
		}
		if transaction.Kind == workspacecheckpoint.TransactionFork {
			return s.checkpoints, nil
		}
		if transaction.Kind == workspacecheckpoint.TransactionGitMutation {
			return s.gitMutationCheckpointService(), nil
		}
		if transaction.Kind != workspacecheckpoint.TransactionFileTool {
			return nil, apperror.New(apperror.CodeConflict,
				"Drydock checkpoint recovery cursor is unavailable")
		}
		return s.FileEditCheckpointService(), nil
	})
}

type DrydockCreateRequest struct {
	RunID                 string `json:"run_id"`
	OperationKey          string `json:"operation_key"`
	RequestedBy           string `json:"requested_by"`
	ConfirmWorkspaceTrust bool   `json:"confirm_workspace_trust"`
	ExpectedTrustDigest   string `json:"expected_trust_digest,omitempty"`
}

type DrydockCreateResult struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Source          runworktree.SourceIdentity      `json:"source"`
	SourceRoot      string                          `json:"-"`
	SourceState     runworktree.SourceState         `json:"source_state"`
	TrustDigest     string                          `json:"trust_digest"`
	TrustRequired   bool                            `json:"trust_required"`
	Trust           *runworktree.Trust              `json:"trust,omitempty"`
	Workspace       *runworktree.Workspace          `json:"workspace,omitempty"`
	Receipt         *runworktree.Receipt            `json:"receipt,omitempty"`
	Checkpoint      *workspacecheckpoint.Checkpoint `json:"checkpoint,omitempty"`
	Replayed        bool                            `json:"replayed"`
}

// DrydockWorkspaceInspection is a read-only source observation used by
// composite product operations before they create a Run-owned Drydock. It is
// internal control-plane evidence and must not be exposed without a narrowed
// view because SourceIdentity contains host paths.
type DrydockWorkspaceInspection struct {
	WorkspaceID string
	Source      runworktree.SourceIdentity
	SourceState runworktree.SourceState
	TrustDigest string
}

func (s *RunWorktreeService) InspectWorkspace(ctx context.Context,
	workspaceID string,
) (DrydockWorkspaceInspection, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if s == nil || s.store == nil || s.executor == nil || workspaceID == "" {
		return DrydockWorkspaceInspection{}, apperror.New(
			apperror.CodeInvalidArgument, "Drydock Workspace inspection is invalid")
	}
	workspace, err := s.store.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return DrydockWorkspaceInspection{}, apperror.Normalize(err)
	}
	source, err := s.executor.InspectSource(ctx, workspace.ID, workspace.RootPath)
	if err != nil {
		return DrydockWorkspaceInspection{}, apperror.Normalize(err)
	}
	result := DrydockWorkspaceInspection{WorkspaceID: workspace.ID,
		Source: source.Identity, SourceState: source.State,
		TrustDigest: runworktree.TrustConfirmationDigest(source.Identity, source.State)}
	if source.State.SymlinkEntries != 0 || source.State.SubmoduleEntries != 0 {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock rejects source symlink/reparse and submodule entries")
	}
	return result, nil
}

type DrydockUseRequest struct {
	RunID              string `json:"run_id"`
	ExpectedGeneration int64  `json:"expected_generation"`
	OperationKey       string `json:"operation_key"`
	RequestedBy        string `json:"requested_by"`
}

type DrydockUseResult struct {
	ProtocolVersion        string                `json:"protocol_version"`
	Workspace              runworktree.Workspace `json:"workspace"`
	Receipt                runworktree.Receipt   `json:"receipt"`
	RootPath               string                `json:"-"`
	BindingFingerprint     string                `json:"binding_fingerprint"`
	GrantsProcessAuthority bool                  `json:"grants_process_authority"`
	Replayed               bool                  `json:"replayed"`
}

// ResolveDrydockExecutionBinding performs a read-only ownership check for the
// exact Standard Code runner binding. It grants no process authority and does
// not advance the Drydock generation. When requireUnchanged is false, it still
// proves the owned root/branch/base identity but permits changes made by an
// already-owned container so cancellation and recovery can converge cleanup.
func (s *RunWorktreeService) ResolveDrydockExecutionBinding(ctx context.Context,
	binding sandbox.DockerStandardCodeRunnerBinding, requireUnchanged bool,
) (sandbox.WorkspaceBinding, error) {
	if s == nil || binding.Validate() != nil {
		return sandbox.WorkspaceBinding{}, apperror.New(apperror.CodeInvalidArgument,
			"Standard Code Drydock binding is invalid")
	}
	var workspace runworktree.Workspace
	var observed repository.DrydockObservation
	var err error
	if proof := gitBisectRuntimeFromContext(ctx); proof != nil {
		if proof.runID != binding.RunID || proof.check == nil || proof.check(ctx) != nil {
			return sandbox.WorkspaceBinding{}, runner.ErrCommandRuntimeBoundary
		}
		workspace, observed, err = s.loadGitBisectDrydock(ctx, binding.RunID, binding.DrydockGeneration, proof.root)
	} else if source, ok := ctx.Value(gitBisectCleanupRootKey{}).(sandbox.WorkspaceBinding); ok {
		if requireUnchanged || source.ID != binding.WorkspaceID {
			return sandbox.WorkspaceBinding{}, runner.ErrCommandRuntimeBoundary
		}
		workspace, observed, err = s.loadGitBisectDrydock(ctx, binding.RunID, binding.DrydockGeneration, source.RootPath)
	} else {
		workspace, _, observed, err = s.loadExactDrydock(ctx, binding.RunID, binding.DrydockGeneration, false)
	}
	if err != nil {
		return sandbox.WorkspaceBinding{}, err
	}
	if workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered {
		return sandbox.WorkspaceBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"Standard Code Drydock is not ready")
	}
	if workspace.ID != binding.DrydockID ||
		workspace.RunID != binding.RunID || workspace.MissionID != binding.MissionID ||
		workspace.SessionID != binding.SessionID ||
		workspace.SourceWorkspaceID != binding.WorkspaceID ||
		workspace.WorkspaceID != binding.DrydockWorkspaceID ||
		workspace.LastCheckpointID != binding.CheckpointID ||
		workspace.ExpectedBindingFingerprint != binding.DrydockBindingSHA256 {
		return sandbox.WorkspaceBinding{}, apperror.New(apperror.CodeConflict,
			"Standard Code Drydock authority binding changed")
	}
	if requireUnchanged && observed.Binding.Fingerprint() != binding.DrydockBindingSHA256 {
		return sandbox.WorkspaceBinding{}, apperror.New(apperror.CodeConflict,
			"Standard Code Drydock content changed before container start")
	}
	bound, err := batchValidationDockerWorkspace(ctx, binding, sandbox.WorkspaceBinding{ID: workspace.SourceWorkspaceID,
		RootPath: workspace.Path})
	if err != nil {
		return sandbox.WorkspaceBinding{}, err
	}
	return gitBisectDockerWorkspace(ctx, binding, bound)
}

type DrydockCheckpointRequest struct {
	RunID                  string `json:"run_id"`
	ExpectedGeneration     int64  `json:"expected_generation"`
	OperationKey           string `json:"operation_key"`
	RequestedBy            string `json:"requested_by"`
	Title                  string `json:"title,omitempty"`
	ConfirmObservedChanges bool   `json:"confirm_observed_changes"`
}

type DrydockCheckpointResult struct {
	ProtocolVersion string                         `json:"protocol_version"`
	Workspace       runworktree.Workspace          `json:"workspace"`
	Checkpoint      workspacecheckpoint.Checkpoint `json:"checkpoint"`
	Receipt         runworktree.Receipt            `json:"receipt"`
	Replayed        bool                           `json:"replayed"`
}

type DrydockRewindRequest struct {
	RunID                  string `json:"run_id"`
	TargetCheckpointID     string `json:"target_checkpoint_id"`
	ExpectedGeneration     int64  `json:"expected_generation"`
	OperationKey           string `json:"operation_key"`
	RequestedBy            string `json:"requested_by"`
	Confirm                bool   `json:"confirm"`
	ConfirmObservedChanges bool   `json:"confirm_observed_changes"`
}

type DrydockUndoRequest struct {
	RunID                  string `json:"run_id"`
	ExpectedGeneration     int64  `json:"expected_generation"`
	OperationKey           string `json:"operation_key"`
	RequestedBy            string `json:"requested_by"`
	Confirm                bool   `json:"confirm"`
	ConfirmObservedChanges bool   `json:"confirm_observed_changes"`
}

type DrydockRewindResult struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Workspace       runworktree.Workspace           `json:"workspace"`
	Target          workspacecheckpoint.Checkpoint  `json:"target"`
	Before          workspacecheckpoint.Checkpoint  `json:"before"`
	Preview         workspacecheckpoint.Preview     `json:"preview"`
	After           *workspacecheckpoint.Checkpoint `json:"after,omitempty"`
	Receipt         *runworktree.Receipt            `json:"receipt,omitempty"`
	Confirmed       bool                            `json:"confirmed"`
	Replayed        bool                            `json:"replayed"`
}

type DrydockForkRequest struct {
	RunID                       string `json:"run_id"`
	TargetCheckpointID          string `json:"target_checkpoint_id"`
	ExpectedCurrentCheckpointID string `json:"expected_current_checkpoint_id"`
	ExpectedGeneration          int64  `json:"expected_generation"`
	OperationKey                string `json:"operation_key"`
	RequestedBy                 string `json:"requested_by"`
	WorkspaceName               string `json:"workspace_name"`
	WorkspaceRoot               string `json:"workspace_root"`
	Branch                      string `json:"branch"`
	Goal                        string `json:"goal"`
	Confirm                     bool   `json:"confirm"`
}

type DrydockForkResult struct {
	ProtocolVersion string                `json:"protocol_version"`
	Workspace       runworktree.Workspace `json:"workspace"`
	Fork            WorkspaceForkResult   `json:"fork"`
	Receipt         runworktree.Receipt   `json:"receipt"`
	Replayed        bool                  `json:"replayed"`
}

type DrydockDeliveryRequest struct {
	RunID              string `json:"run_id"`
	ExpectedGeneration int64  `json:"expected_generation"`
	OperationKey       string `json:"operation_key"`
	RequestedBy        string `json:"requested_by"`
	Confirm            bool   `json:"confirm"`
}

type DrydockDeliveryResult struct {
	ProtocolVersion string                `json:"protocol_version"`
	Workspace       runworktree.Workspace `json:"workspace"`
	Review          runworktree.Review    `json:"review"`
	Receipt         runworktree.Receipt   `json:"receipt"`
	Replayed        bool                  `json:"replayed"`
}

type DrydockCleanupRequest struct {
	RunID              string `json:"run_id"`
	ExpectedGeneration int64  `json:"expected_generation"`
	OperationKey       string `json:"operation_key"`
	RequestedBy        string `json:"requested_by"`
	Confirm            bool   `json:"confirm"`
}

type DrydockCleanupResult struct {
	ProtocolVersion string                `json:"protocol_version"`
	Workspace       runworktree.Workspace `json:"workspace"`
	Receipt         runworktree.Receipt   `json:"receipt"`
	Preserved       bool                  `json:"preserved"`
	Replayed        bool                  `json:"replayed"`
}

type DrydockProjection struct {
	ProtocolVersion string                        `json:"protocol_version"`
	RunID           string                        `json:"run_id"`
	Trust           *runworktree.Trust            `json:"trust,omitempty"`
	Workspace       *runworktree.Workspace        `json:"workspace,omitempty"`
	Delivery        *runworktree.DeliveryProposal `json:"delivery,omitempty"`
	Receipts        []runworktree.Receipt         `json:"receipts"`
}

type DrydockReconcileResult struct {
	ProtocolVersion  string   `json:"protocol_version"`
	Examined         int      `json:"examined"`
	Recovered        int      `json:"recovered"`
	RecoveryRequired int      `json:"recovery_required"`
	Unchanged        int      `json:"unchanged"`
	DrydockIDs       []string `json:"drydock_ids"`
}

type DrydockGCResult struct {
	ProtocolVersion string   `json:"protocol_version"`
	Examined        int      `json:"examined"`
	Cleaned         int      `json:"cleaned"`
	Preserved       int      `json:"preserved"`
	DrydockIDs      []string `json:"drydock_ids"`
}

type drydockRunBinding struct {
	run       domain.Run
	mission   domain.Mission
	session   session.Session
	workspace session.WorkspaceRecord
}

func (s *RunWorktreeService) Create(ctx context.Context,
	request DrydockCreateRequest,
) (DrydockCreateResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	request.ExpectedTrustDigest = strings.TrimSpace(request.ExpectedTrustDigest)
	if s == nil || s.store == nil || s.executor == nil || request.RunID == "" ||
		request.OperationKey == "" || request.RequestedBy == "" {
		return DrydockCreateResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock create request is invalid")
	}
	binding, err := s.loadRunBinding(ctx, request.RunID, false)
	if err != nil {
		return DrydockCreateResult{}, err
	}
	source, err := s.executor.InspectSource(ctx, binding.workspace.ID,
		binding.workspace.RootPath)
	if err != nil {
		return DrydockCreateResult{}, apperror.Normalize(err)
	}
	result := DrydockCreateResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Source: source.Identity, SourceRoot: source.Identity.RootPath,
		SourceState: source.State,
		TrustDigest: runworktree.TrustConfirmationDigest(source.Identity, source.State)}
	if source.State.SymlinkEntries != 0 || source.State.SubmoduleEntries != 0 {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock v1 rejects source symlink/reparse and submodule entries instead of following them implicitly")
	}
	if existing, found, getErr := readRunFileDrydock(ctx, s.store, binding.run.ID); getErr != nil {
		return result, apperror.Normalize(getErr)
	} else if found {
		if err := requireCurrentRunFileDrydock(ctx, s.store, binding.run.ID, existing); err != nil {
			return result, err
		}
		if existing.Source.Fingerprint() != source.Identity.Fingerprint() {
			return result, apperror.New(apperror.CodeConflict,
				"Drydock source root, branch, or base commit drifted")
		}
		if existing.State == runworktree.StatePreparing {
			reconciled, _, reconcileErr := s.reconcileOne(ctx, existing)
			if reconcileErr != nil {
				return result, apperror.Normalize(reconcileErr)
			}
			existing = reconciled
		}
		trust, trustFound, trustErr := s.store.GetDrydockTrustByRun(ctx, binding.run.ID)
		if trustErr != nil {
			return result, apperror.Normalize(trustErr)
		}
		if trustFound {
			result.Trust = &trust
		}
		createDigest := drydockOperationDigest(runworktree.OperationCreate,
			binding.run.ID, request.OperationKey)
		if receipt, receiptFound, receiptErr := s.store.GetDrydockReceiptByOperation(ctx,
			createDigest); receiptErr != nil {
			return result, apperror.Normalize(receiptErr)
		} else if receiptFound {
			expectedFingerprint := runmutation.Fingerprint("drydock-create-request.v1",
				binding.run.ID, source.Identity.Fingerprint(), request.RequestedBy)
			if receipt.Operation != runworktree.OperationCreate ||
				receipt.RequestFingerprint != expectedFingerprint {
				return result, apperror.New(apperror.CodeConflict,
					"Drydock create operation key was reused for different intent")
			}
			result.Receipt = &receipt
			if receipt.CheckpointID != "" {
				snapshot, snapshotErr := s.store.GetWorkspaceCheckpointSnapshot(ctx,
					receipt.CheckpointID)
				if snapshotErr != nil {
					return result, apperror.Normalize(snapshotErr)
				}
				result.Checkpoint = &snapshot.Checkpoint
			}
		}
		result.Workspace, result.Replayed = &existing, true
		return result, nil
	}

	trust, trustFound, err := s.store.GetDrydockTrustByRun(ctx, binding.run.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if trustFound && (trust.Source.Fingerprint() != source.Identity.Fingerprint() ||
		trust.SourceState.Fingerprint() != source.State.Fingerprint()) {
		return result, apperror.New(apperror.CodeConflict,
			"stored Workspace Trust does not match the current source identity and state")
	}
	if !trustFound {
		if !request.ConfirmWorkspaceTrust {
			result.TrustRequired = true
			return result, nil
		}
		if request.ExpectedTrustDigest == "" ||
			request.ExpectedTrustDigest != result.TrustDigest {
			return result, apperror.New(apperror.CodeConflict,
				"Workspace Trust confirmation does not match the exact reviewed source state")
		}
		now := s.now().UTC()
		trust = runworktree.Trust{ID: drydockTrustID(binding.run.ID, source.Identity),
			ProtocolVersion: runworktree.TrustProtocolVersion, RunID: binding.run.ID,
			WorkspaceID: binding.workspace.ID, Source: source.Identity,
			SourceState: source.State, ConfirmedBy: request.RequestedBy,
			GrantsProcessAuthority: false, ConfirmedAt: now}
		trust, _, err = s.store.CreateDrydockTrust(ctx, trust)
		if err != nil {
			return result, apperror.Normalize(err)
		}
	}
	result.Trust = &trust

	identityDigest := runworktree.Fingerprint("drydock", binding.run.ID,
		source.Identity.Fingerprint())
	name := "drydock-" + identityDigest[:24]
	branch := "codex/drydock/" + identityDigest[:24]
	plan, err := s.executor.PlanCreate(ctx, source.Identity.RootPath, name, branch,
		source.Identity.BaseCommit)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if !plan.Preview.Executable() {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock create preview is blocked: "+strings.Join(plan.Preview.BlockedReasons, "; "))
	}
	preCreateSource, err := s.executor.InspectSource(ctx, binding.workspace.ID,
		binding.workspace.RootPath)
	if err != nil || preCreateSource.Identity.Fingerprint() != trust.Source.Fingerprint() ||
		preCreateSource.State.Fingerprint() != trust.SourceState.Fingerprint() {
		return result, apperror.Wrap(apperror.CodeConflict,
			"source identity or dirty/index state changed after Workspace Trust confirmation", err)
	}
	now := s.now().UTC()
	workspace := runworktree.Workspace{ID: "drydock-" + identityDigest[:32],
		ProtocolVersion: runworktree.WorkspaceProtocolVersion, RunID: binding.run.ID,
		MissionID: binding.mission.ID, SessionID: binding.session.ID,
		SourceWorkspaceID: binding.workspace.ID,
		WorkspaceID:       "drydock-ws-" + identityDigest[:32], TrustID: trust.ID,
		Source: source.Identity, Name: name, Path: filepath.Clean(plan.Path),
		PathSHA256: runworktree.FingerprintBytes([]byte(filepath.ToSlash(filepath.Clean(plan.Path)))),
		Branch:     branch, BaseCommit: source.Identity.BaseCommit,
		CreatePreviewID: plan.Preview.ID, State: runworktree.StatePreparing,
		Generation: 1, ExpiresAt: now.Add(runworktree.DefaultLifetime),
		CreatedAt: now, UpdatedAt: now}
	workspace, replayed, err := s.store.PrepareDrydock(ctx, workspace)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if replayed {
		result.Workspace, result.Replayed = &workspace, true
		return result, nil
	}
	gitReceipt, executionErr := s.executor.ExecuteCreate(ctx, source.Identity.RootPath, plan)
	receiptErr := gitReceipt.Validate()
	if executionErr != nil || receiptErr != nil ||
		gitReceipt.Status != gitadvanced.ReceiptSucceeded {
		cause := errors.Join(executionErr, receiptErr)
		if cause == nil {
			cause = errors.New("Git advanced create receipt did not succeed")
		}
		failed, receipt, transitionErr := s.failCreate(ctx, workspace, source, request,
			gitReceipt, cause)
		result.Workspace, result.Receipt = &failed, &receipt
		return result, errors.Join(apperror.Normalize(cause), transitionErr)
	}
	postCreateSource, postSourceErr := s.executor.InspectSource(ctx, binding.workspace.ID,
		binding.workspace.RootPath)
	if postSourceErr != nil ||
		postCreateSource.Identity.Fingerprint() != trust.Source.Fingerprint() ||
		postCreateSource.State.Fingerprint() != trust.SourceState.Fingerprint() {
		cause := errors.Join(postSourceErr,
			errors.New("source identity or dirty/index state changed while the Drydock was created"))
		failed, receipt, transitionErr := s.failCreate(ctx, workspace, source, request,
			gitReceipt, cause)
		result.Workspace, result.Receipt = &failed, &receipt
		return result, errors.Join(apperror.Normalize(cause), transitionErr)
	}
	observed, err := s.executor.Inspect(ctx, source.Identity.RootPath, source.Binding,
		workspace.Name)
	if err != nil || !observed.Present || !observed.Clean ||
		observed.Path != workspace.Path || observed.Branch != workspace.Branch ||
		observed.Head != workspace.BaseCommit {
		failed, receipt, transitionErr := s.failCreate(ctx, workspace, source, request,
			gitReceipt, errors.Join(err, errors.New("created Drydock identity was not exact and clean")))
		result.Workspace, result.Receipt = &failed, &receipt
		return result, errors.Join(err, transitionErr,
			apperror.New(apperror.CodeConflict, "created Drydock requires recovery"))
	}
	if err := s.executor.VerifyBaseAncestry(ctx, observed.Path, workspace.BaseCommit,
		observed.Binding.Head); err != nil {
		failed, receipt, transitionErr := s.failCreate(ctx, workspace, source, request,
			gitReceipt, err)
		result.Workspace, result.Receipt = &failed, &receipt
		return result, errors.Join(err, transitionErr)
	}
	createDigest := drydockOperationDigest(runworktree.OperationCreate, binding.run.ID,
		request.OperationKey)
	receiptID := drydockReceiptID(createDigest)
	checkpoint, err := s.captureCheckpoint(ctx, workspace, receiptID,
		"Drydock baseline", request.RequestedBy, "create", "")
	if err != nil {
		failed, receipt, transitionErr := s.failCreate(ctx, workspace, source, request,
			gitReceipt, err)
		result.Workspace, result.Receipt = &failed, &receipt
		return result, errors.Join(err, transitionErr)
	}
	beforeGeneration := workspace.Generation
	workspace.RootFingerprint = observed.RootFingerprint
	workspace.ExpectedHead = observed.Binding.Head
	workspace.ExpectedBindingFingerprint = observed.Binding.Fingerprint()
	workspace.CreateGitReceiptID = gitReceipt.ID
	workspace.ManagedWorktreeID = gitReceipt.WorktreeID
	workspace.State = runworktree.StateReady
	workspace.Generation++
	workspace.LastCheckpointID = checkpoint.ID
	workspace.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(workspace, beforeGeneration, runworktree.OperationCreate,
		createDigest, runmutation.Fingerprint("drydock-create-request.v1", binding.run.ID,
			source.Identity.Fingerprint(), request.RequestedBy), runworktree.OutcomeSucceeded,
		"", "created an exact Run-owned Drydock without granting process authority",
		"", observed.Binding.Fingerprint(), gitReceipt.ID, checkpoint.ID, "")
	workspace, replayed, err = s.store.AdvanceDrydock(ctx, workspace, beforeGeneration, receipt)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.Workspace, result.Receipt, result.Checkpoint = &workspace, &receipt, &checkpoint
	result.Replayed = replayed
	return result, nil
}

func (s *RunWorktreeService) Use(ctx context.Context,
	request DrydockUseRequest,
) (DrydockUseResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" ||
		request.ExpectedGeneration < 1 {
		return DrydockUseResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock use request is invalid")
	}
	digest := drydockOperationDigest(runworktree.OperationUse, request.RunID,
		request.OperationKey)
	if replay, found, err := s.replayUse(ctx, request, digest); found || err != nil {
		return replay, err
	}
	workspace, source, observed, err := s.loadExactDrydock(ctx, request.RunID,
		request.ExpectedGeneration, false)
	if err != nil {
		return DrydockUseResult{}, err
	}
	if workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered {
		return DrydockUseResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock is not ready for use")
	}
	if observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint {
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace,
			runworktree.OperationUse, digest, request.RequestedBy,
			drydockUseRequestFingerprint(workspace.ID, request), "unattributed_workspace_change",
			"Drydock content changed outside its last attributed generation", &observed)
		return DrydockUseResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: preserved, Receipt: receipt}, errors.Join(preserveErr,
				apperror.New(apperror.CodeConflict, "Drydock content requires recovery review"))
	}
	beforeGeneration := workspace.Generation
	workspace.Generation++
	workspace.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(workspace, beforeGeneration, runworktree.OperationUse,
		digest, drydockUseRequestFingerprint(workspace.ID, request),
		runworktree.OutcomeSucceeded, "",
		"attested the exact Drydock binding; no process authority was granted",
		observed.Binding.Fingerprint(), observed.Binding.Fingerprint(), "", "", "")
	receipt.RunID = request.RunID
	workspace, replayed, err := s.store.AdvanceDrydock(ctx, workspace,
		beforeGeneration, receipt)
	if err != nil {
		return DrydockUseResult{}, apperror.Normalize(err)
	}
	_ = source
	return DrydockUseResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: workspace, Receipt: receipt, RootPath: workspace.Path,
		BindingFingerprint:     observed.Binding.Fingerprint(),
		GrantsProcessAuthority: false, Replayed: replayed}, nil
}

func drydockUseRequestFingerprint(workspaceID string, request DrydockUseRequest) string {
	return runmutation.Fingerprint("drydock-use-request.v1", workspaceID,
		strconv.FormatInt(request.ExpectedGeneration, 10), request.RequestedBy)
}

func (s *RunWorktreeService) Checkpoint(ctx context.Context,
	request DrydockCheckpointRequest,
) (DrydockCheckpointResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	request.Title = strings.TrimSpace(request.Title)
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" ||
		request.ExpectedGeneration < 1 {
		return DrydockCheckpointResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock checkpoint request is invalid")
	}
	checkpointDigest := drydockOperationDigest(runworktree.OperationCheckpoint, request.RunID,
		request.OperationKey)
	recoverDigest := drydockOperationDigest(runworktree.OperationRecover, request.RunID,
		request.OperationKey)
	for _, replayDigest := range []string{checkpointDigest, recoverDigest} {
		receipt, found, replayErr := s.store.GetDrydockReceiptByOperation(ctx, replayDigest)
		if replayErr != nil {
			return DrydockCheckpointResult{}, apperror.Normalize(replayErr)
		}
		if !found {
			continue
		}
		workspace, workspaceFound, getErr := readRunFileDrydock(ctx, s.store, request.RunID)
		if getErr != nil || !workspaceFound {
			return DrydockCheckpointResult{}, apperror.Normalize(getErr)
		}
		expectedFingerprint := drydockCheckpointRequestFingerprint(workspace.ID, request)
		if !drydockReceiptMatchesRequest(receipt, workspace.ID, request.ExpectedGeneration,
			expectedFingerprint) {
			return DrydockCheckpointResult{}, apperror.New(apperror.CodeConflict,
				"Drydock checkpoint operation key was reused for different intent")
		}
		result := DrydockCheckpointResult{ProtocolVersion: DrydockAPIProtocolVersion,
			Workspace: workspace, Receipt: receipt, Replayed: true}
		if receipt.Outcome != runworktree.OutcomeSucceeded || receipt.CheckpointID == "" {
			return result, apperror.New(apperror.CodeConflict,
				"the previous checkpoint request preserved the Drydock for recovery")
		}
		snapshot, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx, receipt.CheckpointID)
		result.Checkpoint = snapshot.Checkpoint
		return result, apperror.Normalize(getErr)
	}
	workspace, _, observed, err := s.loadExactDrydock(ctx, request.RunID,
		request.ExpectedGeneration, true)
	if err != nil {
		return DrydockCheckpointResult{}, err
	}
	if workspace.State == runworktree.StateCleaned || workspace.State == runworktree.StatePreparing {
		return DrydockCheckpointResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock cannot be checkpointed in its current state")
	}
	operation := runworktree.OperationCheckpoint
	digest := checkpointDigest
	if workspace.State == runworktree.StateRecoveryRequired {
		operation = runworktree.OperationRecover
		digest = recoverDigest
	}
	changed := observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint
	if (changed || workspace.State == runworktree.StateRecoveryRequired) &&
		!request.ConfirmObservedChanges {
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace,
			operation, digest, request.RequestedBy,
			drydockCheckpointRequestFingerprint(workspace.ID, request),
			"unconfirmed_workspace_change",
			"Drydock changed after its last receipt and was preserved for recovery", &observed)
		return DrydockCheckpointResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: preserved, Receipt: receipt}, errors.Join(preserveErr,
				apperror.New(apperror.CodeConflict, "observed changes require explicit confirmation"))
	}
	receiptID := drydockReceiptID(digest)
	title := request.Title
	if title == "" {
		title = "Drydock checkpoint"
	}
	checkpoint, err := s.captureCheckpointForRun(ctx, request.RunID, workspace, receiptID, title,
		request.RequestedBy, string(operation), workspace.LastCheckpointID)
	if err != nil {
		return DrydockCheckpointResult{}, apperror.Normalize(err)
	}
	beforeGeneration := workspace.Generation
	previousBinding := workspace.ExpectedBindingFingerprint
	workspace.RootFingerprint = observed.RootFingerprint
	workspace.ExpectedHead = observed.Binding.Head
	workspace.ExpectedBindingFingerprint = observed.Binding.Fingerprint()
	workspace.State = runworktree.StateReady
	workspace.RecoveryReason = ""
	workspace.Generation++
	workspace.LastCheckpointID = checkpoint.ID
	workspace.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(workspace, beforeGeneration, operation, digest,
		drydockCheckpointRequestFingerprint(workspace.ID, request),
		runworktree.OutcomeSucceeded, "", "captured tracked, untracked, and raw Git index state",
		previousBinding, observed.Binding.Fingerprint(), "", checkpoint.ID, "")
	receipt.RunID = request.RunID
	workspace, replayed, err := s.store.AdvanceDrydock(ctx, workspace,
		beforeGeneration, receipt)
	if err != nil {
		return DrydockCheckpointResult{}, apperror.Normalize(err)
	}
	return DrydockCheckpointResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: workspace, Checkpoint: checkpoint, Receipt: receipt,
		Replayed: replayed}, nil
}

func drydockCheckpointRequestFingerprint(workspaceID string,
	request DrydockCheckpointRequest,
) string {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = "Drydock checkpoint"
	}
	return runmutation.Fingerprint("drydock-checkpoint-request.v1", workspaceID,
		strconv.FormatInt(request.ExpectedGeneration, 10), request.RequestedBy,
		fmt.Sprintf("%t", request.ConfirmObservedChanges), title)
}

func drydockReceiptMatchesRequest(receipt runworktree.Receipt, workspaceID string,
	expectedGeneration int64, expectedFingerprint string,
) bool {
	if receipt.DrydockID != workspaceID || receipt.GenerationBefore != expectedGeneration {
		return false
	}
	return receipt.RequestFingerprint == expectedFingerprint
}

func (s *RunWorktreeService) Rewind(ctx context.Context,
	request DrydockRewindRequest,
) (DrydockRewindResult, error) {
	return s.rewind(ctx, request, runworktree.OperationRewind)
}

func (s *RunWorktreeService) Undo(ctx context.Context,
	request DrydockUndoRequest,
) (DrydockRewindResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" ||
		request.ExpectedGeneration < 1 {
		return DrydockRewindResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock undo request is invalid")
	}
	digest := drydockOperationDigest(runworktree.OperationUndo, request.RunID,
		request.OperationKey)
	if journal, err := s.drydockRestoreJournal(ctx, digest); err != nil {
		return DrydockRewindResult{}, err
	} else if journal != nil {
		return s.rewind(ctx, DrydockRewindRequest{RunID: request.RunID,
			TargetCheckpointID: journal.TargetCheckpointID, ExpectedGeneration: request.ExpectedGeneration,
			OperationKey: request.OperationKey, RequestedBy: request.RequestedBy,
			Confirm: request.Confirm, ConfirmObservedChanges: request.ConfirmObservedChanges}, runworktree.OperationUndo)
	}
	if receipt, receiptFound, receiptErr := s.store.GetDrydockReceiptByOperation(ctx,
		digest); receiptErr != nil {
		return DrydockRewindResult{}, apperror.Normalize(receiptErr)
	} else if receiptFound && receipt.Outcome == runworktree.OutcomeSucceeded {
		if receipt.Operation != runworktree.OperationUndo || receipt.CheckpointID == "" {
			return DrydockRewindResult{}, apperror.New(apperror.CodeConflict,
				"stored Drydock undo receipt is invalid")
		}
		after, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx, receipt.CheckpointID)
		if getErr != nil || after.Checkpoint.ParentCheckpointID == "" {
			return DrydockRewindResult{}, apperror.Normalize(errors.Join(getErr,
				errors.New("stored Drydock undo after-checkpoint is incomplete")))
		}
		before, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx,
			after.Checkpoint.ParentCheckpointID)
		if getErr != nil || before.Checkpoint.ParentCheckpointID == "" {
			return DrydockRewindResult{}, apperror.Normalize(errors.Join(getErr,
				errors.New("stored Drydock undo before-checkpoint is incomplete")))
		}
		return s.rewind(ctx, DrydockRewindRequest{RunID: request.RunID,
			TargetCheckpointID: before.Checkpoint.ParentCheckpointID,
			ExpectedGeneration: request.ExpectedGeneration,
			OperationKey:       request.OperationKey, RequestedBy: request.RequestedBy,
			Confirm:                request.Confirm,
			ConfirmObservedChanges: request.ConfirmObservedChanges}, runworktree.OperationUndo)
	}
	workspace, found, err := readRunFileDrydock(ctx, s.store, request.RunID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "Drydock was not found for this Run")
		}
		return DrydockRewindResult{}, apperror.Normalize(err)
	}
	if workspace.LastCheckpointID == "" {
		return DrydockRewindResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock has no checkpoint to undo")
	}
	current, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, workspace.LastCheckpointID)
	if err != nil {
		return DrydockRewindResult{}, apperror.Normalize(err)
	}
	if current.Checkpoint.ParentCheckpointID == "" {
		return DrydockRewindResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock checkpoint has no previous state to undo to")
	}
	return s.rewind(ctx, DrydockRewindRequest{RunID: request.RunID,
		TargetCheckpointID: current.Checkpoint.ParentCheckpointID,
		ExpectedGeneration: request.ExpectedGeneration, OperationKey: request.OperationKey,
		RequestedBy: request.RequestedBy, Confirm: request.Confirm,
		ConfirmObservedChanges: request.ConfirmObservedChanges}, runworktree.OperationUndo)
}

func (s *RunWorktreeService) rewind(ctx context.Context, request DrydockRewindRequest,
	operation runworktree.Operation,
) (DrydockRewindResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.TargetCheckpointID = strings.TrimSpace(request.TargetCheckpointID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	if (operation != runworktree.OperationRewind && operation != runworktree.OperationUndo) ||
		request.RunID == "" || request.TargetCheckpointID == "" ||
		request.OperationKey == "" || request.RequestedBy == "" ||
		request.ExpectedGeneration < 1 {
		return DrydockRewindResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock rewind request is invalid")
	}
	digest := drydockOperationDigest(operation, request.RunID, request.OperationKey)
	if request.Confirm {
		if stored, found, err := s.store.GetDrydockReceiptByOperation(ctx, digest); err != nil {
			return DrydockRewindResult{}, apperror.Normalize(err)
		} else if found {
			workspace, workspaceFound, getErr := readRunFileDrydock(ctx, s.store, request.RunID)
			if getErr != nil || !workspaceFound {
				return DrydockRewindResult{}, apperror.Normalize(getErr)
			}
			fingerprint := drydockRewindRequestFingerprint(workspace.ID, operation, request)
			if !drydockReceiptMatchesRequest(stored, workspace.ID,
				request.ExpectedGeneration, fingerprint) {
				return DrydockRewindResult{}, apperror.New(apperror.CodeConflict,
					"Drydock rewind operation key was reused for different intent")
			}
			if err := s.finishDrydockRestoreJournal(ctx, digest, stored); err != nil {
				return DrydockRewindResult{}, err
			}
			if stored.Outcome != runworktree.OutcomeSucceeded || stored.CheckpointID == "" {
				return DrydockRewindResult{ProtocolVersion: DrydockAPIProtocolVersion,
						Workspace: workspace, Receipt: &stored, Confirmed: true, Replayed: true},
					apperror.New(apperror.CodeConflict,
						"the previous rewind request preserved the Drydock for recovery")
			}
			after, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx, stored.CheckpointID)
			if getErr != nil {
				return DrydockRewindResult{}, apperror.Normalize(getErr)
			}
			before, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx,
				after.Checkpoint.ParentCheckpointID)
			if getErr != nil {
				return DrydockRewindResult{}, apperror.Normalize(getErr)
			}
			target, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx,
				request.TargetCheckpointID)
			return DrydockRewindResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: workspace, Target: target.Checkpoint, Before: before.Checkpoint,
				After: &after.Checkpoint, Receipt: &stored, Confirmed: true,
				Replayed: true}, apperror.Normalize(getErr)
		}
	}
	workspace, source, observed, err := s.loadExactDrydock(ctx, request.RunID,
		request.ExpectedGeneration, false)
	if err != nil {
		return DrydockRewindResult{}, err
	}
	if err := requireCurrentRunFileDrydock(ctx, s.store, request.RunID, workspace); err != nil {
		return DrydockRewindResult{}, err
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return DrydockRewindResult{}, apperror.Normalize(err)
	}
	journal, err := s.drydockRestoreJournal(ctx, digest)
	if err != nil {
		return DrydockRewindResult{}, err
	}
	if journal != nil && (journal.RunID != request.RunID || journal.WorkspaceID != workspace.WorkspaceID ||
		journal.RequestFingerprint != drydockRewindRequestFingerprint(workspace.ID, operation, request) ||
		journal.TargetCheckpointID != request.TargetCheckpointID || journal.ExpectedCurrentCheckpointID != workspace.LastCheckpointID || journal.Status.Terminal()) {
		return DrydockRewindResult{}, apperror.New(apperror.CodeConflict, "Drydock restore request or its physical cursor changed")
	}
	if workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered {
		return DrydockRewindResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock is not ready for rewind")
	}
	current, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, workspace.LastCheckpointID)
	if err != nil {
		return DrydockRewindResult{}, apperror.Normalize(err)
	}
	target, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, request.TargetCheckpointID)
	if err != nil {
		return DrydockRewindResult{}, apperror.Normalize(err)
	}
	if err := errors.Join(s.requireDrydockCheckpoint(ctx, workspace, current.Checkpoint),
		s.requireDrydockCheckpoint(ctx, workspace, target.Checkpoint)); err != nil {
		return DrydockRewindResult{}, err
	}
	if current.Checkpoint.BaseCommit != observed.Binding.Head ||
		target.Checkpoint.BaseCommit != current.Checkpoint.BaseCommit ||
		current.Checkpoint.Branch != workspace.Branch ||
		target.Checkpoint.Branch != workspace.Branch ||
		target.Checkpoint.RecoveryLevel == workspacecheckpoint.RecoveryUnavailable {
		return DrydockRewindResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"target checkpoint is not materializable in this exact Drydock")
	}
	if journal == nil && observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint &&
		!request.ConfirmObservedChanges {
		if !request.Confirm {
			return DrydockRewindResult{ProtocolVersion: DrydockAPIProtocolVersion,
					Workspace: workspace, Target: target.Checkpoint,
					Before: current.Checkpoint}, apperror.New(apperror.CodeConflict,
					"Drydock changed after its last attributed checkpoint")
		}
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace, operation,
			digest, request.RequestedBy,
			drydockRewindRequestFingerprint(workspace.ID, operation, request),
			"unattributed_workspace_change",
			"rewind refused content not attributed to the last Drydock checkpoint", &observed)
		return DrydockRewindResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: preserved, Target: target.Checkpoint, Before: current.Checkpoint,
				Receipt: &receipt, Confirmed: true}, errors.Join(preserveErr,
				apperror.New(apperror.CodeConflict,
					"Drydock rewind requires explicit attribution of observed changes"))
	}
	observedSnapshot, err := workspacecheckpoint.Capture(ctx,
		workspacecheckpoint.CaptureRequest{ID: "drydock-rewind-observed-" + digest[:24],
			RunID: run.ID, MissionID: run.MissionID,
			SessionID: run.SessionID, WorkspaceID: workspace.WorkspaceID,
			WorkspaceRoot: workspace.Path, Trigger: workspacecheckpoint.TriggerRewindPreflight,
			Phase:              workspacecheckpoint.PhasePreflight,
			TriggerReceiptID:   drydockReceiptID(digest),
			ParentCheckpointID: current.Checkpoint.ID, RequestedBy: request.RequestedBy,
			CreatedAt: s.now().UTC()})
	if err != nil {
		return DrydockRewindResult{}, apperror.Normalize(err)
	}
	expectedSnapshot := current
	if journal != nil {
		expectedSnapshot, err = s.store.GetWorkspaceCheckpointSnapshot(ctx, journal.BeforeCheckpointID)
		if err != nil {
			return DrydockRewindResult{}, apperror.Normalize(err)
		}
		if err := s.requireDrydockCheckpoint(ctx, workspace, expectedSnapshot.Checkpoint); err != nil {
			return DrydockRewindResult{}, err
		}
	}
	if expectedSnapshot.Checkpoint.IndexSHA256 != observedSnapshot.Checkpoint.IndexSHA256 &&
		drydockIndexProjectionEqual(expectedSnapshot, observedSnapshot) {
		// Git may rewrite stat-cache-only bytes in an otherwise identical index.
		// Use the freshly observed raw index as the CAS input only when every
		// projected path/OID/mode/staged value is still exact.
		expectedSnapshot = drydockRestoreObservedIndex(expectedSnapshot, observedSnapshot)
	}
	preview, previewErr := workspacecheckpoint.PreviewWorkspaceRestore(expectedSnapshot, target,
		observedSnapshot)
	result := DrydockRewindResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: workspace, Target: target.Checkpoint, Before: current.Checkpoint,
		Preview: preview}
	if !request.Confirm {
		// Preview conflicts are data for the operator; no write has occurred.
		return result, nil
	}
	if previewErr != nil || len(preview.Conflicts) != 0 {
		if previewErr == nil {
			previewErr = &workspacecheckpoint.ConflictError{Conflicts: preview.Conflicts}
		}
		return result, apperror.Normalize(previewErr)
	}
	if s.checkpoints == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition, "Drydock restore requires current workspace restore authority")
	}
	binding, err := s.checkpoints.loadBinding(ctx, request.RunID)
	if err != nil {
		return result, err
	}
	if err := s.checkpoints.requireRestoreAuthority(ctx, binding, request.RequestedBy); err != nil {
		return result, err
	}
	if journal == nil {
		journal, err = s.prepareDrydockRestore(ctx, request, operation, digest, workspace, observedSnapshot, preview)
		if err != nil {
			return result, err
		}
	}
	// The prepared transaction excludes execution, cleanup and successor
	// publication before the first filesystem write; retries retain it.
	_, applyErr := workspacecheckpoint.ApplyWorkspaceRestore(ctx, workspace.Path, expectedSnapshot,
		target, observedSnapshot)
	if applyErr != nil {
		post, inspectErr := s.executor.Inspect(ctx, source.Identity.RootPath,
			source.Binding, workspace.Name)
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace, operation,
			digest, request.RequestedBy,
			drydockRewindRequestFingerprint(workspace.ID, operation, request),
			"rewind_apply_failed",
			"a partial or conflicted rewind was preserved for operator recovery", &post)
		result.Workspace, result.Receipt, result.Confirmed = preserved, &receipt, true
		return result, s.preserveDrydockRestore(ctx, digest, receipt, errors.Join(apperror.Normalize(applyErr), apperror.Normalize(inspectErr),
			preserveErr))
	}
	receiptID := drydockReceiptID(digest)
	afterSnapshot, captureErr := workspacecheckpoint.Capture(ctx,
		workspacecheckpoint.CaptureRequest{ID: "drydock-checkpoint-" + digest[:32],
			RunID: run.ID, MissionID: run.MissionID,
			SessionID: run.SessionID, WorkspaceID: workspace.WorkspaceID,
			WorkspaceRoot: workspace.Path, Trigger: workspacecheckpoint.TriggerRewindResult,
			Phase: workspacecheckpoint.PhaseAfter, TriggerReceiptID: receiptID,
			ParentCheckpointID: journal.BeforeCheckpointID, RequestedBy: request.RequestedBy,
			Title: "Drydock " + string(operation), CreatedAt: s.now().UTC()})
	if captureErr == nil && (afterSnapshot.Checkpoint.ManifestSHA256 !=
		target.Checkpoint.ManifestSHA256 || afterSnapshot.Checkpoint.IndexSHA256 !=
		target.Checkpoint.IndexSHA256) {
		captureErr = errors.New("Drydock rewind final checkpoint does not match its target")
	}
	if captureErr != nil {
		post, inspectErr := s.executor.Inspect(ctx, source.Identity.RootPath,
			source.Binding, workspace.Name)
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace, operation,
			digest, request.RequestedBy,
			drydockRewindRequestFingerprint(workspace.ID, operation, request),
			"rewind_verification_failed",
			"rewind output was preserved because exact checkpoint verification failed", &post)
		result.Workspace, result.Receipt, result.Confirmed = preserved, &receipt, true
		return result, s.preserveDrydockRestore(ctx, digest, receipt, errors.Join(apperror.Normalize(captureErr),
			apperror.Normalize(inspectErr), preserveErr))
	}
	if storedAfter, getErr := s.store.GetWorkspaceCheckpointSnapshot(ctx, afterSnapshot.Checkpoint.ID); getErr == nil {
		previous, actual := storedAfter.Checkpoint, afterSnapshot.Checkpoint
		if previous.RunID != actual.RunID || previous.SessionID != actual.SessionID || previous.MissionID != actual.MissionID ||
			previous.WorkspaceID != actual.WorkspaceID || previous.TriggerReceiptID != actual.TriggerReceiptID ||
			previous.ParentCheckpointID != actual.ParentCheckpointID || previous.RootFingerprint != actual.RootFingerprint ||
			previous.RootPathSHA256 != actual.RootPathSHA256 || previous.BaseCommit != actual.BaseCommit ||
			previous.Branch != actual.Branch || previous.IndexSHA256 != actual.IndexSHA256 || previous.ManifestSHA256 != actual.ManifestSHA256 {
			return result, apperror.New(apperror.CodeConflict, "Drydock restore changed after its result was captured")
		}
		afterSnapshot = storedAfter
	} else if apperror.CodeOf(apperror.Normalize(getErr)) != apperror.CodeNotFound {
		return result, apperror.Normalize(getErr)
	}
	afterCheckpoint, _, err := s.store.CreateWorkspaceCheckpoint(ctx, afterSnapshot)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	post, err := s.executor.Inspect(ctx, source.Identity.RootPath, source.Binding,
		workspace.Name)
	validationErr := s.validateObservation(ctx, workspace, post)
	if err != nil || validationErr != nil {
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace, operation,
			digest, request.RequestedBy,
			drydockRewindRequestFingerprint(workspace.ID, operation, request),
			"rewind_identity_failed",
			"rewind output was preserved because its root identity could not be reverified", &post)
		result.Workspace, result.Receipt, result.Confirmed = preserved, &receipt, true
		return result, s.preserveDrydockRestore(ctx, digest, receipt, errors.Join(apperror.Normalize(err),
			apperror.Normalize(validationErr), preserveErr))
	}
	beforeGeneration := workspace.Generation
	previousBinding := workspace.ExpectedBindingFingerprint
	workspace.State = runworktree.StateReady
	workspace.Generation++
	workspace.LastCheckpointID = afterCheckpoint.ID
	workspace.RootFingerprint = post.RootFingerprint
	workspace.ExpectedHead = post.Binding.Head
	workspace.ExpectedBindingFingerprint = post.Binding.Fingerprint()
	workspace.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(workspace, beforeGeneration, operation, digest,
		drydockRewindRequestFingerprint(workspace.ID, operation, request),
		runworktree.OutcomeSucceeded, "", "restored a reviewed Drydock checkpoint with exact tracked, untracked, and index verification",
		previousBinding, post.Binding.Fingerprint(), "", afterCheckpoint.ID, "")
	receipt.RunID = request.RunID
	workspace, replayed, err := s.store.AdvanceDrydock(ctx, workspace,
		beforeGeneration, receipt)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.Workspace, result.After, result.Receipt = workspace, &afterCheckpoint, &receipt
	result.Confirmed, result.Replayed = true, replayed
	return result, s.finishDrydockRestoreJournal(ctx, digest, receipt)
}

func drydockRewindRequestFingerprint(workspaceID string, operation runworktree.Operation,
	request DrydockRewindRequest,
) string {
	return runmutation.Fingerprint("drydock-rewind-request.v1", workspaceID,
		string(operation), request.TargetCheckpointID,
		strconv.FormatInt(request.ExpectedGeneration, 10), request.RequestedBy,
		fmt.Sprintf("%t", request.ConfirmObservedChanges))
}

func drydockIndexProjectionEqual(left, right workspacecheckpoint.Snapshot) bool {
	type indexEntry struct {
		OID    string
		Mode   string
		Staged bool
	}
	project := func(snapshot workspacecheckpoint.Snapshot) map[string]indexEntry {
		values := make(map[string]indexEntry)
		for _, entry := range snapshot.Entries {
			if !entry.Tracked && entry.IndexOID == "" && entry.IndexMode == "" {
				continue
			}
			values[entry.Path] = indexEntry{OID: entry.IndexOID,
				Mode: entry.IndexMode, Staged: entry.Staged}
		}
		return values
	}
	first, second := project(left), project(right)
	if len(first) != len(second) {
		return false
	}
	for path, value := range first {
		if second[path] != value {
			return false
		}
	}
	return true
}

func (s *RunWorktreeService) Fork(ctx context.Context,
	request DrydockForkRequest,
) (DrydockForkResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.TargetCheckpointID = strings.TrimSpace(request.TargetCheckpointID)
	request.ExpectedCurrentCheckpointID = strings.TrimSpace(
		request.ExpectedCurrentCheckpointID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	request.WorkspaceName = strings.TrimSpace(request.WorkspaceName)
	request.WorkspaceRoot = strings.TrimSpace(request.WorkspaceRoot)
	request.Branch = strings.TrimSpace(request.Branch)
	request.Goal = strings.TrimSpace(request.Goal)
	if s == nil || s.checkpoints == nil || request.RunID == "" ||
		request.TargetCheckpointID == "" || request.ExpectedCurrentCheckpointID == "" ||
		request.ExpectedGeneration < 1 || request.OperationKey == "" ||
		request.RequestedBy == "" || request.WorkspaceName == "" ||
		request.WorkspaceRoot == "" || request.Branch == "" || !request.Confirm {
		return DrydockForkResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock fork request is invalid or its checkpoint integration is unavailable")
	}
	digest := drydockOperationDigest(runworktree.OperationFork, request.RunID,
		request.OperationKey)
	checkpointRequest := WorkspaceForkRequest{RunID: request.RunID,
		TargetCheckpointID:          request.TargetCheckpointID,
		ExpectedCurrentCheckpointID: request.ExpectedCurrentCheckpointID,
		OperationKey:                request.OperationKey, RequestedBy: request.RequestedBy,
		WorkspaceName: request.WorkspaceName, WorkspaceRoot: request.WorkspaceRoot,
		Branch: request.Branch, Goal: request.Goal, Confirm: true}
	if receipt, found, err := s.store.GetDrydockReceiptByOperation(ctx, digest); err != nil {
		return DrydockForkResult{}, apperror.Normalize(err)
	} else if found {
		workspace, workspaceFound, getErr := readRunFileDrydock(ctx, s.store, request.RunID)
		if getErr != nil || !workspaceFound {
			return DrydockForkResult{}, apperror.Normalize(getErr)
		}
		if receipt.RequestFingerprint != drydockForkRequestFingerprint(workspace.ID,
			request) {
			return DrydockForkResult{}, apperror.New(apperror.CodeConflict,
				"Drydock fork operation key was reused for different intent")
		}
		forked, forkErr := s.forkOwnedCheckpoint(ctx, workspace, checkpointRequest)
		return DrydockForkResult{ProtocolVersion: DrydockAPIProtocolVersion,
			Workspace: workspace, Fork: forked, Receipt: receipt,
			Replayed: true}, forkErr
	}
	workspace, _, observed, err := s.loadExactDrydock(ctx, request.RunID,
		request.ExpectedGeneration, false)
	if err != nil {
		return DrydockForkResult{}, err
	}
	if err := requireCurrentRunFileDrydock(ctx, s.store, request.RunID, workspace); err != nil {
		return DrydockForkResult{}, err
	}
	if workspace.LastCheckpointID != request.ExpectedCurrentCheckpointID ||
		observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint {
		return DrydockForkResult{}, apperror.New(apperror.CodeConflict,
			"Drydock changed after the fork checkpoint was reviewed")
	}
	target, err := s.store.GetWorkspaceCheckpointSnapshot(ctx,
		request.TargetCheckpointID)
	if err != nil {
		return DrydockForkResult{}, apperror.Normalize(err)
	}
	if err := s.requireDrydockCheckpoint(ctx, workspace, target.Checkpoint); err != nil {
		return DrydockForkResult{}, err
	}
	if target.Checkpoint.RecoveryLevel == workspacecheckpoint.RecoveryUnavailable {
		return DrydockForkResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"fork checkpoint is not materializable in this exact Drydock")
	}
	if err := s.executor.VerifyBaseAncestry(ctx, workspace.Path, workspace.BaseCommit,
		target.Checkpoint.BaseCommit); err != nil {
		return DrydockForkResult{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"fork checkpoint no longer descends from the fixed Drydock base", err)
	}
	forked, err := s.forkOwnedCheckpoint(ctx, workspace, checkpointRequest)
	if err != nil {
		return DrydockForkResult{}, err
	}
	verified, _, after, err := s.loadExactDrydock(ctx, request.RunID,
		request.ExpectedGeneration, false)
	if err != nil || after.Binding.Fingerprint() != observed.Binding.Fingerprint() {
		return DrydockForkResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: verified, Fork: forked}, errors.Join(err,
				apperror.New(apperror.CodeConflict,
					"Drydock changed while the independent fork was materialized"))
	}
	beforeGeneration := verified.Generation
	verified.Generation++
	verified.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(verified, beforeGeneration, runworktree.OperationFork,
		digest, drydockForkRequestFingerprint(verified.ID, request),
		runworktree.OutcomeSucceeded, "",
		"materialized a checkpoint into a distinct authority-reset Run and branch without changing the source Drydock",
		observed.Binding.Fingerprint(), after.Binding.Fingerprint(), "",
		request.TargetCheckpointID, "")
	receipt.RunID = request.RunID
	verified, replayed, err := s.store.AdvanceDrydock(ctx, verified,
		beforeGeneration, receipt)
	if err != nil {
		return DrydockForkResult{ProtocolVersion: DrydockAPIProtocolVersion,
			Workspace: verified, Fork: forked}, apperror.Normalize(err)
	}
	return DrydockForkResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: verified, Fork: forked, Receipt: receipt,
		Replayed: replayed || forked.Replayed}, nil
}

func (s *RunWorktreeService) forkOwnedCheckpoint(ctx context.Context,
	workspace runworktree.Workspace, request WorkspaceForkRequest,
) (WorkspaceForkResult, error) {
	target, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, request.TargetCheckpointID)
	if err != nil {
		return WorkspaceForkResult{}, apperror.Normalize(err)
	}
	if err := s.requireDrydockCheckpoint(ctx, workspace, target.Checkpoint); err != nil {
		return WorkspaceForkResult{}, err
	}
	return s.checkpoints.forkFromOwnedCheckpoint(ctx, request,
		workspacecheckpoint.RunState{RunID: request.RunID,
			WorkspaceID: workspace.WorkspaceID, CurrentCheckpointID: workspace.LastCheckpointID,
			LastTransactionID: "", UpdatedAt: s.now().UTC()})
}

func drydockForkRequestFingerprint(workspaceID string, request DrydockForkRequest) string {
	return runmutation.Fingerprint("drydock-fork-request.v1", workspaceID,
		request.TargetCheckpointID, request.ExpectedCurrentCheckpointID,
		strconv.FormatInt(request.ExpectedGeneration, 10), request.RequestedBy,
		request.WorkspaceName, filepath.Clean(request.WorkspaceRoot), request.Branch,
		request.Goal)
}

func (s *RunWorktreeService) Deliver(ctx context.Context,
	request DrydockDeliveryRequest,
) (DrydockDeliveryResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" ||
		request.ExpectedGeneration < 1 || !request.Confirm {
		return DrydockDeliveryResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock delivery requires an exact generation and explicit review confirmation")
	}
	digest := drydockOperationDigest(runworktree.OperationDeliver, request.RunID,
		request.OperationKey)
	if receipt, found, err := s.store.GetDrydockReceiptByOperation(ctx, digest); err != nil {
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	} else if found {
		return s.replayDelivery(ctx, request, receipt)
	}
	workspace, _, observed, err := s.loadExactDrydock(ctx, request.RunID,
		request.ExpectedGeneration, false)
	if err != nil {
		return DrydockDeliveryResult{}, err
	}
	if workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered {
		return DrydockDeliveryResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock is not ready for delivery")
	}
	if observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint {
		preserved, receipt, preserveErr := s.markRecovery(ctx, request.RunID, workspace,
			runworktree.OperationDeliver, digest, request.RequestedBy,
			drydockDeliveryRequestFingerprint(workspace.ID, request),
			"unattributed_workspace_change",
			"delivery refused content not covered by the last Drydock checkpoint", &observed)
		return DrydockDeliveryResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: preserved, Receipt: receipt}, errors.Join(preserveErr,
				apperror.New(apperror.CodeConflict, "delivery requires a fresh attributed checkpoint"))
	}
	receiptID := drydockReceiptID(digest)
	checkpoint, err := s.captureCheckpointForRun(ctx, request.RunID, workspace, receiptID,
		"Drydock delivery", request.RequestedBy, "deliver", workspace.LastCheckpointID)
	if err != nil {
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	}
	evidence, err := s.executor.CaptureDelivery(ctx, workspace.Path, workspace.BaseCommit)
	if err != nil {
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	}
	if evidence.Binding.Fingerprint() != observed.Binding.Fingerprint() {
		return DrydockDeliveryResult{}, apperror.New(apperror.CodeConflict,
			"Drydock changed after its delivery checkpoint")
	}
	proposalID := "drydock-delivery-" + digest[:32]
	proposal := runworktree.DeliveryProposal{ID: proposalID,
		ProtocolVersion:    runworktree.DeliveryProtocolVersion,
		OperationKeySHA256: digest,
		RequestFingerprint: drydockDeliveryRequestFingerprint(workspace.ID, request),
		DrydockID:          workspace.ID, RunID: request.RunID,
		Generation:           workspace.Generation + 1,
		SourceIdentitySHA256: workspace.Source.Fingerprint(),
		RootFingerprint:      workspace.RootFingerprint, BaseCommit: workspace.BaseCommit,
		HeadCommit: evidence.HeadCommit, MergeBaseCommit: evidence.MergeBaseCommit,
		BindingFingerprint: evidence.Binding.Fingerprint(),
		DiffSHA256:         runworktree.FingerprintBytes([]byte(evidence.Patch)),
		DiffBytes:          len([]byte(evidence.Patch)), DiffStat: evidence.DiffStat,
		ChangedPaths: evidence.ChangedPaths, CheckpointID: checkpoint.ID,
		CreatedBy: request.RequestedBy, AutomaticMerge: false,
		PushAuthorized: false, ForceAuthorized: false,
		SourceOverwriteAllowed: false, CreatedAt: s.now().UTC()}
	review := runworktree.Review{Proposal: proposal, Patch: evidence.Patch}
	if err := review.Validate(); err != nil {
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	}
	beforeGeneration := workspace.Generation
	workspace.State = runworktree.StateDelivered
	workspace.Generation++
	workspace.LastCheckpointID = checkpoint.ID
	workspace.LastDeliveryID = proposal.ID
	workspace.ExpectedHead = evidence.Binding.Head
	workspace.ExpectedBindingFingerprint = evidence.Binding.Fingerprint()
	workspace.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(workspace, beforeGeneration, runworktree.OperationDeliver,
		digest, proposal.RequestFingerprint, runworktree.OutcomeSucceeded, "",
		"recorded a review-only Diff proposal without push, force, merge, or source overwrite authority",
		observed.Binding.Fingerprint(), evidence.Binding.Fingerprint(), "",
		checkpoint.ID, proposal.ID)
	receipt.RunID = request.RunID
	proposal, workspace, replayed, err := s.store.CreateDrydockDelivery(ctx,
		proposal, workspace, beforeGeneration, receipt)
	if err != nil {
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	}
	review.Proposal = proposal
	return DrydockDeliveryResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: workspace, Review: review, Receipt: receipt, Replayed: replayed}, nil
}

func drydockDeliveryRequestFingerprint(workspaceID string,
	request DrydockDeliveryRequest,
) string {
	return runmutation.Fingerprint("drydock-delivery-request.v1", workspaceID,
		strconv.FormatInt(request.ExpectedGeneration, 10), request.RequestedBy)
}

func (s *RunWorktreeService) Cleanup(ctx context.Context,
	request DrydockCleanupRequest,
) (DrydockCleanupResult, error) {
	return s.cleanup(ctx, request, s.executor)
}

func (s *RunWorktreeService) cleanup(ctx context.Context,
	request DrydockCleanupRequest, executor drydockCleanupExecutor,
) (result DrydockCleanupResult, err error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = normalizeDrydockActor(request.RequestedBy)
	if request.RunID == "" || request.OperationKey == "" || request.RequestedBy == "" ||
		request.ExpectedGeneration < 1 || !request.Confirm {
		return DrydockCleanupResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock cleanup requires an exact generation and explicit confirmation")
	}
	digest := drydockOperationDigest(runworktree.OperationCleanup, request.RunID,
		request.OperationKey)
	// Another process confirming this exact request can finish while this
	// caller is inspecting or committing. Return its sealed result, never our
	// losing locally constructed receipt. Unresolved errors remain unknown.
	defer func() {
		if err == nil {
			return
		}
		result = DrydockCleanupResult{}
		if replay, found, replayErr := s.replayDrydockCleanup(ctx, request, digest); replayErr == nil && found {
			result, err = replay, nil
		}
	}()
	if replay, found, replayErr := s.replayDrydockCleanup(ctx, request, digest); replayErr != nil || found {
		return replay, replayErr
	}
	if err := s.beginDrydockCleanup(ctx, request, digest); err != nil {
		return DrydockCleanupResult{}, err
	}
	workspace, _, observed, err := s.loadDrydockForCleanup(ctx, request.RunID,
		request.ExpectedGeneration)
	if err != nil {
		return DrydockCleanupResult{}, err
	}
	if workspace.State == runworktree.StateCleaned {
		return DrydockCleanupResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock is already cleaned")
	}
	if !observed.Found && !observed.Present {
		return s.completeDrydockCleanup(ctx, request, digest, workspace,
			"closed an exactly absent Drydock registration after explicit confirmation; no filesystem entry was deleted",
			"", "")
	}
	if !observed.Present {
		return s.confirmDrydockCleanupFailure(ctx, request, digest, "", nil)
	}
	if workspace.State == runworktree.StatePreparing || workspace.State == runworktree.StateRecoveryRequired ||
		!observed.Clean || observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint {
		// Same-key callers share a reservation; another one may already be
		// removing this directory. A dirty observation cannot establish a
		// final preserved outcome or release that caller's mutation fence.
		return DrydockCleanupResult{}, apperror.New(apperror.CodeConflict,
			"Drydock content or identity changed; removal is not confirmed. Keep the original cleanup request to inspect its outcome")
	}
	preview, err := executor.PlanRemove(ctx, workspace.Source.RootPath,
		workspace.Name, workspace.ManagedWorktreeID)
	if err != nil || !preview.Executable() {
		return s.confirmDrydockCleanupFailure(ctx, request, digest, "", err)
	}
	gitReceipt, err := executor.ExecuteRemove(ctx, workspace.Source.RootPath, preview)
	receiptErr := gitReceipt.Validate()
	if err != nil || receiptErr != nil || gitReceipt.Status != gitadvanced.ReceiptSucceeded {
		gitErr := errors.Join(err, receiptErr)
		if gitErr == nil {
			gitErr = errors.New("Git advanced cleanup receipt did not succeed")
		}
		return s.confirmDrydockCleanupFailure(ctx, request, digest, gitReceipt.ID, gitErr)
	}
	return s.completeDrydockCleanup(ctx, request, digest, workspace,
		"removed only the exact clean Git worktree; the local branch and source Workspace were retained",
		observed.Binding.Fingerprint(), gitReceipt.ID)
}

func drydockCleanupRequestFingerprint(workspaceID string,
	request DrydockCleanupRequest,
) string {
	return runmutation.Fingerprint("drydock-cleanup-request.v1", workspaceID,
		strconv.FormatInt(request.ExpectedGeneration, 10), request.RequestedBy)
}

func (s *RunWorktreeService) Projection(ctx context.Context, runID string,
	limit int,
) (DrydockProjection, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || limit < 1 || limit > runworktree.MaxList {
		return DrydockProjection{}, apperror.New(apperror.CodeInvalidArgument,
			"Drydock projection request is invalid")
	}
	if _, err := s.loadRunBinding(ctx, runID, true); err != nil {
		return DrydockProjection{}, err
	}
	value := DrydockProjection{ProtocolVersion: DrydockAPIProtocolVersion,
		RunID: runID, Receipts: []runworktree.Receipt{}}
	if trust, found, err := s.store.GetDrydockTrustByRun(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	} else if found {
		value.Trust = &trust
	}
	workspace, found, err := readRunFileDrydock(ctx, s.store, runID)
	if err != nil || !found {
		return value, apperror.Normalize(err)
	}
	value.Workspace = &workspace
	value.Receipts, err = s.store.ListDrydockReceipts(ctx, workspace.ID, limit)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	if workspace.LastDeliveryID != "" {
		if proposal, proposalFound, proposalErr := s.store.GetDrydockDelivery(ctx,
			workspace.LastDeliveryID); proposalErr != nil {
			return value, apperror.Normalize(proposalErr)
		} else if proposalFound {
			value.Delivery = &proposal
		}
	}
	return value, nil
}

func (s *RunWorktreeService) Reconcile(ctx context.Context) (DrydockReconcileResult, error) {
	value := DrydockReconcileResult{ProtocolVersion: DrydockAPIProtocolVersion,
		DrydockIDs: []string{}}
	workspaces, err := s.store.ListDrydocks(ctx, runworktree.ListFilter{
		IncludeCleaned: false, Limit: runworktree.MaxList})
	if err != nil {
		return value, apperror.Normalize(err)
	}
	for _, workspace := range workspaces {
		if err := ctx.Err(); err != nil {
			return value, err
		}
		value.Examined++
		updated, changed, reconcileErr := s.reconcileOne(ctx, workspace)
		if reconcileErr != nil {
			return value, reconcileErr
		}
		if !changed {
			value.Unchanged++
			continue
		}
		value.DrydockIDs = append(value.DrydockIDs, updated.ID)
		if updated.State == runworktree.StateReady {
			value.Recovered++
		} else if updated.State == runworktree.StateRecoveryRequired {
			value.RecoveryRequired++
		}
	}
	return value, nil
}

func (s *RunWorktreeService) GarbageCollect(ctx context.Context, limit int) (DrydockGCResult, error) {
	value := DrydockGCResult{ProtocolVersion: DrydockAPIProtocolVersion,
		DrydockIDs: []string{}}
	if limit < 1 || limit > 100 {
		return value, apperror.New(apperror.CodeInvalidArgument,
			"Drydock GC limit must be between 1 and 100")
	}
	now := s.now().UTC()
	workspaces, err := s.store.ListDrydocks(ctx, runworktree.ListFilter{
		ExpiredBefore: &now, IncludeCleaned: false, Limit: limit})
	if err != nil {
		return value, apperror.Normalize(err)
	}
	for _, workspace := range workspaces {
		value.Examined++
		if workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered {
			value.Preserved++
			continue
		}
		retained, retainErr := s.threadRetainsDrydock(ctx, workspace)
		if retainErr != nil {
			return value, retainErr
		}
		if retained {
			value.Preserved++
			continue
		}
		result, cleanupErr := s.Cleanup(ctx, DrydockCleanupRequest{RunID: workspace.RunID,
			ExpectedGeneration: workspace.Generation,
			OperationKey:       "gc-" + workspace.ID + "-" + strconv.FormatInt(workspace.Generation, 10),
			RequestedBy:        "drydock_gc", Confirm: true})
		if cleanupErr != nil && result.Workspace.ID == "" {
			return value, cleanupErr
		}
		value.DrydockIDs = append(value.DrydockIDs, workspace.ID)
		if result.Workspace.State == runworktree.StateCleaned {
			value.Cleaned++
		} else {
			value.Preserved++
		}
	}
	return value, nil
}

func (s *RunWorktreeService) replayUse(ctx context.Context, request DrydockUseRequest,
	digest string,
) (DrydockUseResult, bool, error) {
	receipt, found, err := s.store.GetDrydockReceiptByOperation(ctx, digest)
	if err != nil || !found {
		return DrydockUseResult{}, false, apperror.Normalize(err)
	}
	workspace, workspaceFound, err := readRunFileDrydock(ctx, s.store, request.RunID)
	if err != nil || !workspaceFound {
		return DrydockUseResult{}, true, apperror.Normalize(err)
	}
	if receipt.Operation != runworktree.OperationUse ||
		!drydockReceiptMatchesRequest(receipt, workspace.ID,
			request.ExpectedGeneration,
			drydockUseRequestFingerprint(workspace.ID, request)) {
		return DrydockUseResult{}, true, apperror.New(apperror.CodeConflict,
			"Drydock use operation key was reused for different intent")
	}
	result := DrydockUseResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: workspace, Receipt: receipt, RootPath: workspace.Path,
		BindingFingerprint:     receipt.BindingAfterSHA256,
		GrantsProcessAuthority: false, Replayed: true}
	if receipt.Outcome != runworktree.OutcomeSucceeded {
		return result, true, apperror.New(apperror.CodeConflict,
			"the previous use request preserved the Drydock for recovery")
	}
	return result, true, nil
}

func (s *RunWorktreeService) replayDelivery(ctx context.Context,
	request DrydockDeliveryRequest,
	receipt runworktree.Receipt,
) (DrydockDeliveryResult, error) {
	workspace, found, err := readRunFileDrydock(ctx, s.store, request.RunID)
	if err != nil || !found {
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	}
	if receipt.Operation != runworktree.OperationDeliver ||
		!drydockReceiptMatchesRequest(receipt, workspace.ID,
			request.ExpectedGeneration,
			drydockDeliveryRequestFingerprint(workspace.ID, request)) {
		return DrydockDeliveryResult{}, apperror.New(apperror.CodeConflict,
			"Drydock delivery operation key was reused for different intent")
	}
	if receipt.Outcome != runworktree.OutcomeSucceeded || receipt.DeliveryID == "" {
		return DrydockDeliveryResult{ProtocolVersion: DrydockAPIProtocolVersion,
				Workspace: workspace, Receipt: receipt, Replayed: true},
			apperror.New(apperror.CodeConflict,
				"the previous delivery request preserved the Drydock for recovery")
	}
	proposal, found, err := s.store.GetDrydockDelivery(ctx, receipt.DeliveryID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("stored Drydock delivery proposal is unavailable")
		}
		return DrydockDeliveryResult{}, apperror.Normalize(err)
	}
	evidence, err := s.executor.CaptureDelivery(ctx, workspace.Path, workspace.BaseCommit)
	if err != nil || runworktree.FingerprintBytes([]byte(evidence.Patch)) != proposal.DiffSHA256 ||
		evidence.Binding.Fingerprint() != proposal.BindingFingerprint {
		return DrydockDeliveryResult{}, apperror.New(apperror.CodeConflict,
			"stored delivery proposal no longer matches the live Drydock")
	}
	return DrydockDeliveryResult{ProtocolVersion: DrydockAPIProtocolVersion,
		Workspace: workspace, Review: runworktree.Review{Proposal: proposal, Patch: evidence.Patch},
		Receipt: receipt, Replayed: true}, nil
}

func (s *RunWorktreeService) loadExactDrydock(ctx context.Context, runID string,
	expectedGeneration int64, allowRecovery bool,
) (runworktree.Workspace, repository.DrydockSourceObservation,
	repository.DrydockObservation, error,
) {
	binding, err := s.loadRunBinding(ctx, runID, true)
	if err != nil {
		return runworktree.Workspace{}, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, err
	}
	workspace, found, err := readRunFileDrydock(ctx, s.store, runID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "Drydock was not found for this Run")
		}
		return runworktree.Workspace{}, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, apperror.Normalize(err)
	}
	if workspace.Generation != expectedGeneration {
		return workspace, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, apperror.New(apperror.CodeConflict,
				"Drydock ownership generation changed")
	}
	if workspace.State == runworktree.StateRecoveryRequired && !allowRecovery {
		return workspace, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, apperror.New(apperror.CodeFailedPrecondition,
				"Drydock requires operator recovery")
	}
	// These are independent read-only observations of the source and its owned
	// worktree. The stored common-directory digest is only a lookup coordinate,
	// not an authority result: both repositories are inspected afresh below and
	// all source, root, branch and ancestry checks still have to succeed. Keeping
	// results local to this call also prevents a later execution fence from using
	// a previous inspection after files or authority have changed.
	observationCtx, cancelObservation := context.WithCancel(ctx)
	defer cancelObservation()
	var observed repository.DrydockObservation
	var observationErr error
	observedDone := make(chan struct{})
	go func() {
		defer close(observedDone)
		observed, observationErr = s.executor.Inspect(observationCtx,
			binding.workspace.RootPath,
			gitadvanced.RepositoryBinding{CommonDirSHA256: workspace.Source.CommonDirSHA256},
			workspace.Name)
	}()
	source, err := s.executor.InspectSource(observationCtx, binding.workspace.ID,
		binding.workspace.RootPath)
	if err != nil || source.Identity.Fingerprint() != workspace.Source.Fingerprint() {
		cancelObservation()
		<-observedDone
		return workspace, source, repository.DrydockObservation{},
			apperror.New(apperror.CodeConflict,
				"Drydock source root, repository, branch, or base commit drifted")
	}
	<-observedDone
	if observationErr != nil {
		return workspace, source, observed, apperror.Normalize(observationErr)
	}
	if err := s.validateObservation(ctx, workspace, observed); err != nil {
		return workspace, source, observed, apperror.Normalize(err)
	}
	return workspace, source, observed, nil
}

func (s *RunWorktreeService) loadDrydockForCleanup(ctx context.Context, runID string,
	expectedGeneration int64,
) (runworktree.Workspace, repository.DrydockSourceObservation,
	repository.DrydockObservation, error,
) {
	binding, err := s.loadRunBinding(ctx, runID, true)
	if err != nil {
		return runworktree.Workspace{}, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, err
	}
	workspace, found, err := s.cleanupWorkspace(ctx, runID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "Drydock was not found for this Run")
		}
		return runworktree.Workspace{}, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, apperror.Normalize(err)
	}
	if workspace.Generation != expectedGeneration {
		return workspace, repository.DrydockSourceObservation{},
			repository.DrydockObservation{}, apperror.New(apperror.CodeConflict,
				"Drydock ownership generation changed")
	}
	if err := s.requireCurrentDrydockCleanupHolder(ctx, runID, workspace); err != nil {
		return workspace, repository.DrydockSourceObservation{}, repository.DrydockObservation{}, err
	}
	source, err := s.executor.InspectSource(ctx, binding.workspace.ID,
		binding.workspace.RootPath)
	if err != nil || source.Identity.Fingerprint() != workspace.Source.Fingerprint() {
		return workspace, source, repository.DrydockObservation{},
			apperror.New(apperror.CodeConflict,
				"Drydock source root, repository, branch, or base commit drifted")
	}
	observed, err := s.executor.Inspect(ctx, source.Identity.RootPath, source.Binding,
		workspace.Name)
	if err != nil {
		return workspace, source, observed, apperror.Normalize(err)
	}
	if observed.Present {
		if err := s.validateObservation(ctx, workspace, observed); err != nil {
			return workspace, source, observed, apperror.Normalize(err)
		}
	}
	return workspace, source, observed, nil
}

func (s *RunWorktreeService) validateObservation(ctx context.Context,
	workspace runworktree.Workspace, observed repository.DrydockObservation,
) error {
	if !observed.Found || !observed.Present || observed.Prunable || observed.Locked ||
		observed.Path != workspace.Path || observed.Branch != workspace.Branch ||
		observed.Binding.CommonDirSHA256 != workspace.Source.CommonDirSHA256 {
		return apperror.New(apperror.CodeConflict,
			"Drydock root or branch identity changed")
	}
	if workspace.RootFingerprint != "" && observed.RootFingerprint != workspace.RootFingerprint {
		return apperror.New(apperror.CodeConflict, "Drydock root fingerprint changed")
	}
	if err := s.executor.VerifyBaseAncestry(ctx, observed.Path, workspace.BaseCommit,
		observed.Binding.Head); err != nil {
		return apperror.Wrap(apperror.CodeConflict, "Drydock base ancestry changed", err)
	}
	return nil
}

func (s *RunWorktreeService) loadRunBinding(ctx context.Context, runID string,
	allowTerminal bool,
) (drydockRunBinding, error) {
	var value drydockRunBinding
	var err error
	if value.run, err = s.store.GetRun(ctx, strings.TrimSpace(runID)); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.run.Terminal() && !allowTerminal {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"terminal Run cannot create a Drydock")
	}
	if value.mission, err = s.store.GetMission(ctx, value.run.MissionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.mission.Profile != domain.ProfileCode || value.mission.WorkspaceID == "" ||
		value.run.SessionID == "" {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"Drydock requires a Code Run with an exact Workspace and Session")
	}
	if value.session, err = s.store.GetSession(ctx, value.run.SessionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.workspace, err = s.store.GetWorkspaceByID(ctx,
		value.mission.WorkspaceID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.session.WorkspaceID != value.workspace.ID ||
		value.mission.Scope.WorkspaceID != value.workspace.ID {
		return value, apperror.New(apperror.CodeConflict,
			"Run, Mission, Session, and source Workspace binding changed")
	}
	return value, nil
}

func (s *RunWorktreeService) captureCheckpoint(ctx context.Context,
	workspace runworktree.Workspace, triggerReceiptID, title, requestedBy, phase,
	parentID string,
) (workspacecheckpoint.Checkpoint, error) {
	return s.captureCheckpointForRun(ctx, workspace.RunID, workspace, triggerReceiptID, title, requestedBy, phase, parentID)
}

func (s *RunWorktreeService) captureCheckpointForRun(ctx context.Context, runID string,
	workspace runworktree.Workspace, triggerReceiptID, title, requestedBy, phase, parentID string,
) (workspacecheckpoint.Checkpoint, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, apperror.Normalize(err)
	}
	if run.MissionID != workspace.MissionID || run.SessionID == "" {
		return workspacecheckpoint.Checkpoint{}, apperror.New(apperror.CodeConflict, "Checkpoint execution identity does not match its working directory")
	}
	digest := runmutation.Fingerprint("drydock-checkpoint.v1", workspace.ID,
		triggerReceiptID, phase, parentID)
	snapshot, err := workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{
		ID: "drydock-checkpoint-" + digest[:32], RunID: run.ID,
		MissionID: workspace.MissionID, SessionID: run.SessionID,
		WorkspaceID: workspace.WorkspaceID, WorkspaceRoot: workspace.Path,
		CapabilityGeneration: runworktree.Fingerprint("drydock-generation", workspace.ID,
			strconv.FormatInt(workspace.Generation, 10)),
		Trigger: workspacecheckpoint.TriggerManual, Phase: workspacecheckpoint.PhaseStandalone,
		TriggerReceiptID: triggerReceiptID, RequestedBy: requestedBy, Title: title,
		ParentCheckpointID: parentID, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, err
	}
	checkpoint, _, err := s.store.CreateWorkspaceCheckpoint(ctx, snapshot)
	return checkpoint, err
}

func (s *RunWorktreeService) transitionReceipt(workspace runworktree.Workspace,
	beforeGeneration int64, operation runworktree.Operation, operationDigest,
	requestFingerprint string, outcome runworktree.Outcome, reason, summary,
	bindingBefore, bindingAfter, gitReceiptID, checkpointID, deliveryID string,
) runworktree.Receipt {
	return runworktree.Receipt{ID: drydockReceiptID(operationDigest),
		ProtocolVersion:    runworktree.ReceiptProtocolVersion,
		OperationKeySHA256: operationDigest, RequestFingerprint: requestFingerprint,
		DrydockID: workspace.ID, RunID: workspace.RunID, Operation: operation,
		Outcome: outcome, GenerationBefore: beforeGeneration,
		GenerationAfter:      workspace.Generation,
		SourceIdentitySHA256: workspace.Source.Fingerprint(),
		RootFingerprint:      workspace.RootFingerprint,
		BindingBeforeSHA256:  bindingBefore, BindingAfterSHA256: bindingAfter,
		GitReceiptID: gitReceiptID, CheckpointID: checkpointID, DeliveryID: deliveryID,
		ReasonCode: reason, Summary: summary, GrantsProcessAuthority: false,
		CreatedAt: s.now().UTC()}
}

func (s *RunWorktreeService) failCreate(ctx context.Context, workspace runworktree.Workspace,
	source repository.DrydockSourceObservation, request DrydockCreateRequest,
	gitReceipt gitadvanced.Receipt, cause error,
) (runworktree.Workspace, runworktree.Receipt, error) {
	observed, inspectErr := s.executor.Inspect(ctx, source.Identity.RootPath,
		source.Binding, workspace.Name)
	beforeGeneration := workspace.Generation
	now := s.now().UTC()
	reason := "create_failed"
	if inspectErr == nil && observed.Present && observed.Path == workspace.Path &&
		observed.Branch == workspace.Branch && observed.Binding.Head == workspace.BaseCommit {
		workspace.RootFingerprint = observed.RootFingerprint
		workspace.ExpectedHead = observed.Binding.Head
		workspace.ExpectedBindingFingerprint = observed.Binding.Fingerprint()
		workspace.ManagedWorktreeID = gitReceipt.WorktreeID
		if workspace.ManagedWorktreeID == "" {
			workspace.ManagedWorktreeID = "gwt-" + gitadvanced.Fingerprint("worktree",
				workspace.CreatePreviewID)[:32]
		}
		workspace.State = runworktree.StateRecoveryRequired
		workspace.RecoveryReason = reason
	} else if !observed.Found && inspectErr == nil {
		workspace.State = runworktree.StateCleaned
		workspace.CleanedAt = &now
	} else {
		workspace.State = runworktree.StateRecoveryRequired
		workspace.RecoveryReason = reason
	}
	workspace.Generation++
	workspace.UpdatedAt = now
	digest := drydockOperationDigest(runworktree.OperationCreate, workspace.RunID,
		request.OperationKey)
	summary := "Drydock creation failed; uncertain or changed content was preserved"
	receipt := s.transitionReceipt(workspace, beforeGeneration, runworktree.OperationCreate,
		digest, runmutation.Fingerprint("drydock-create-request.v1", workspace.RunID,
			workspace.Source.Fingerprint(), request.RequestedBy), runworktree.OutcomeFailed,
		reason, summary, "", workspace.ExpectedBindingFingerprint, gitReceipt.ID, "", "")
	advanced, _, transitionErr := s.store.AdvanceDrydock(ctx, workspace,
		beforeGeneration, receipt)
	return advanced, receipt, errors.Join(apperror.Normalize(cause),
		apperror.Normalize(inspectErr), apperror.Normalize(transitionErr))
}

func (s *RunWorktreeService) markRecovery(ctx context.Context, runID string, workspace runworktree.Workspace,
	operation runworktree.Operation, digest, requestedBy, requestFingerprint, reason, summary string,
	observed *repository.DrydockObservation,
) (runworktree.Workspace, runworktree.Receipt, error) {
	return s.markRecoveryWithGit(ctx, runID, workspace, operation, digest, requestedBy,
		requestFingerprint, reason, summary, observed, "")
}

func (s *RunWorktreeService) markRecoveryWithGit(ctx context.Context, runID string,
	workspace runworktree.Workspace, operation runworktree.Operation, digest, requestedBy,
	requestFingerprint, reason, summary string, observed *repository.DrydockObservation,
	gitReceiptID string,
) (runworktree.Workspace, runworktree.Receipt, error) {
	if strings.TrimSpace(requestedBy) == "" || !runworktree.ValidDigest(requestFingerprint) {
		return runworktree.Workspace{}, runworktree.Receipt{}, apperror.New(
			apperror.CodeInvalidArgument, "Drydock recovery attribution is invalid")
	}
	beforeGeneration := workspace.Generation
	bindingBefore := workspace.ExpectedBindingFingerprint
	bindingAfter := ""
	if observed != nil && observed.Present && observed.Path == workspace.Path &&
		observed.Branch == workspace.Branch {
		// Never promote changed or uncertain content into the expected binding.
		// Only a preparing owner with no prior materialization evidence may adopt
		// an exact registered observation for later operator recovery.
		if workspace.RootFingerprint == "" && workspace.ExpectedHead == "" &&
			workspace.ExpectedBindingFingerprint == "" && workspace.ManagedWorktreeID == "" &&
			observed.Binding.CommonDirSHA256 == workspace.Source.CommonDirSHA256 {
			workspace.RootFingerprint = observed.RootFingerprint
			workspace.ExpectedHead = observed.Binding.Head
			workspace.ExpectedBindingFingerprint = observed.Binding.Fingerprint()
			workspace.ManagedWorktreeID = "gwt-" + gitadvanced.Fingerprint("worktree",
				workspace.CreatePreviewID)[:32]
		}
		bindingAfter = observed.Binding.Fingerprint()
	}
	workspace.State = runworktree.StateRecoveryRequired
	workspace.RecoveryReason = reason
	workspace.Generation++
	workspace.UpdatedAt = s.now().UTC()
	receipt := s.transitionReceipt(workspace, beforeGeneration, operation, digest,
		requestFingerprint,
		runworktree.OutcomePreserved, reason, summary, bindingBefore, bindingAfter,
		gitReceiptID, "", "")
	receipt.RunID = runID
	advanced, _, err := s.advanceDrydockTransition(ctx, workspace, beforeGeneration, receipt)
	return advanced, receipt, apperror.Normalize(err)
}

func (s *RunWorktreeService) reconcileOne(ctx context.Context,
	workspace runworktree.Workspace,
) (runworktree.Workspace, bool, error) {
	if workspace.State == runworktree.StateCleaned || workspace.State == runworktree.StateRecoveryRequired {
		return workspace, false, nil
	}
	if skip, err := s.skipTransferredDrydockReconciliation(ctx, workspace); err != nil || skip {
		return workspace, false, err
	}
	binding, err := s.loadRunBinding(ctx, workspace.RunID, true)
	if err != nil {
		return s.reconcilePreserve(ctx, workspace, "run_binding_drift")
	}
	source, err := s.executor.InspectSource(ctx, binding.workspace.ID,
		binding.workspace.RootPath)
	if err != nil || source.Identity.Fingerprint() != workspace.Source.Fingerprint() {
		return s.reconcilePreserve(ctx, workspace, "source_identity_drift")
	}
	observed, inspectErr := s.executor.Inspect(ctx, source.Identity.RootPath,
		source.Binding, workspace.Name)
	if inspectErr != nil || !observed.Present {
		return s.reconcilePreserve(ctx, workspace, "worktree_identity_unavailable")
	}
	if err := s.validateObservation(ctx, workspace, observed); err != nil {
		return s.reconcilePreserveObserved(ctx, workspace, "worktree_identity_drift", observed)
	}
	if workspace.State == runworktree.StatePreparing {
		if !observed.Clean || observed.Binding.Head != workspace.BaseCommit {
			return s.reconcilePreserveObserved(ctx, workspace,
				"interrupted_create_has_user_changes", observed)
		}
		digest := drydockOperationDigest(runworktree.OperationRecover, workspace.RunID,
			"startup-create-"+strconv.FormatInt(workspace.Generation, 10))
		receiptID := drydockReceiptID(digest)
		checkpoint, captureErr := s.captureCheckpoint(ctx, workspace, receiptID,
			"Recovered Drydock baseline", "drydock_reconciler", "recover", "")
		if captureErr != nil {
			return s.reconcilePreserveObserved(ctx, workspace,
				"recovery_checkpoint_failed", observed)
		}
		beforeGeneration := workspace.Generation
		workspace.RootFingerprint = observed.RootFingerprint
		workspace.ExpectedHead = observed.Binding.Head
		workspace.ExpectedBindingFingerprint = observed.Binding.Fingerprint()
		workspace.ManagedWorktreeID = "gwt-" + gitadvanced.Fingerprint("worktree",
			workspace.CreatePreviewID)[:32]
		workspace.CreateGitReceiptID = "gar-" + gitadvanced.Fingerprint("receipt",
			workspace.CreatePreviewID)[:32]
		workspace.State = runworktree.StateReady
		workspace.Generation++
		workspace.LastCheckpointID = checkpoint.ID
		workspace.UpdatedAt = s.now().UTC()
		receipt := s.transitionReceipt(workspace, beforeGeneration, runworktree.OperationRecover,
			digest, runmutation.Fingerprint("drydock-reconcile.v1", workspace.ID,
				strconv.FormatInt(beforeGeneration, 10)), runworktree.OutcomeSucceeded, "",
			"recovered an interrupted create only after exact clean identity verification",
			"", observed.Binding.Fingerprint(), workspace.CreateGitReceiptID,
			checkpoint.ID, "")
		workspace, _, err = s.store.AdvanceDrydock(ctx, workspace,
			beforeGeneration, receipt)
		return workspace, true, apperror.Normalize(err)
	}
	if observed.Binding.Fingerprint() != workspace.ExpectedBindingFingerprint {
		return s.reconcilePreserveObserved(ctx, workspace,
			"post_crash_workspace_change", observed)
	}
	return workspace, false, nil
}

func (s *RunWorktreeService) reconcilePreserve(ctx context.Context,
	workspace runworktree.Workspace, reason string,
) (runworktree.Workspace, bool, error) {
	digest := drydockOperationDigest(runworktree.OperationRecover, workspace.RunID,
		"startup-preserve-"+strconv.FormatInt(workspace.Generation, 10))
	updated, _, err := s.markRecovery(ctx, workspace.RunID, workspace, runworktree.OperationRecover,
		digest, "drydock_reconciler", runmutation.Fingerprint("drydock-reconcile.v1",
			workspace.ID, strconv.FormatInt(workspace.Generation, 10), reason), reason,
		"startup recovery preserved a Drydock whose exact ownership could not be proved", nil)
	return updated, true, err
}

func (s *RunWorktreeService) reconcilePreserveObserved(ctx context.Context,
	workspace runworktree.Workspace, reason string, observed repository.DrydockObservation,
) (runworktree.Workspace, bool, error) {
	digest := drydockOperationDigest(runworktree.OperationRecover, workspace.RunID,
		"startup-preserve-"+strconv.FormatInt(workspace.Generation, 10))
	updated, _, err := s.markRecovery(ctx, workspace.RunID, workspace, runworktree.OperationRecover,
		digest, "drydock_reconciler", runmutation.Fingerprint("drydock-reconcile.v1",
			workspace.ID, strconv.FormatInt(workspace.Generation, 10), reason), reason,
		"startup recovery preserved observed content instead of deleting it", &observed)
	return updated, true, err
}

func drydockTrustID(runID string, identity runworktree.SourceIdentity) string {
	return "drydock-trust-" + runworktree.Fingerprint("trust", runID,
		identity.Fingerprint())[:32]
}

func drydockOperationDigest(operation runworktree.Operation, runID, operationKey string) string {
	return runmutation.OperationKeyDigest("drydock_lifecycle.v1."+string(operation),
		runID, operationKey)
}

func drydockReceiptID(operationDigest string) string {
	return "drydock-receipt-" + operationDigest[:32]
}

func normalizeDrydockActor(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "operator"
	}
	if len([]rune(value)) > 256 || strings.ContainsRune(value, 0) {
		return ""
	}
	return value
}
