package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
)

type BatchDeliveryStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionProfile(context.Context, string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	ListCommandRuntimeJobs(context.Context, runner.CommandRuntimeListFilter) ([]runner.CommandRuntimeJob, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
	GetChildTaskProposal(context.Context, string) (domain.ChildTaskProposal, bool, error)
	ListChildTaskAssignments(context.Context, string) ([]domain.ChildTaskAssignment, error)
	CreateBatchDeliveryPlan(context.Context, domain.BatchDeliveryPlan,
		[]domain.BatchDeliveryWorkspace) (domain.BatchDeliveryPlan,
		[]domain.BatchDeliveryWorkspace, bool, error)
	GetBatchDeliveryPlan(context.Context, string) (domain.BatchDeliveryPlan, bool, error)
	GetBatchDeliveryPlanByProposal(context.Context, string) (domain.BatchDeliveryPlan, bool, error)
	ListBatchDeliveryPlans(context.Context, string, int) ([]domain.BatchDeliveryPlan, error)
	ListBatchDeliveryWorkspaces(context.Context, string) ([]domain.BatchDeliveryWorkspace, error)
	GetBatchDeliveryWorkspace(context.Context, string, int) (domain.BatchDeliveryWorkspace, bool, error)
	ActivateBatchDeliveryWorkspace(context.Context, domain.BatchDeliveryMailboxMessage,
		string) (domain.BatchDeliveryWorkspace, domain.BatchDeliveryMailboxMessage, bool, error)
	AppendBatchDeliveryMailbox(context.Context, domain.BatchDeliveryMailboxMessage,
		string, time.Time) (domain.BatchDeliveryWorkspace,
		domain.BatchDeliveryMailboxMessage, bool, error)
	AbortBatchDeliveryWorkspace(context.Context, domain.BatchDeliveryMailboxMessage,
		domain.BatchDeliveryWorkspaceStatus, string) (domain.BatchDeliveryWorkspace,
		domain.BatchDeliveryMailboxMessage, bool, error)
	ListBatchDeliveryMailbox(context.Context, string, int, int) (
		[]domain.BatchDeliveryMailboxMessage, error)
	GetBatchDeliveryMailboxByOperationDigest(context.Context, string) (
		domain.BatchDeliveryMailboxMessage, bool, error)
	RecordBatchDeliveryReceipt(context.Context, domain.BatchDeliveryReceipt, string,
		domain.BatchDeliveryMailboxMessage) (domain.BatchDeliveryReceipt, bool, error)
	GetBatchDeliveryReceipt(context.Context, string, int, int64) (
		domain.BatchDeliveryReceipt, bool, error)
	RecordBatchDeliveryReview(context.Context, domain.BatchDeliveryReview,
		domain.BatchDeliveryMailboxMessage) (domain.BatchDeliveryReview, bool, error)
	GetBatchDeliveryReview(context.Context, string) (domain.BatchDeliveryReview, bool, error)
	RetryBatchDeliveryWorkspace(context.Context, string, int, int64, string,
		time.Time, time.Time) (domain.BatchDeliveryWorkspace, error)
	RotateBatchDeliveryWorkspaceOwner(context.Context, string, int, int64, string,
		time.Time, time.Time) (domain.BatchDeliveryWorkspace, error)
	SetBatchDeliveryWorkspaceStatus(context.Context, string, int, int64,
		domain.BatchDeliveryWorkspaceStatus, domain.BatchDeliveryWorkspaceStatus,
		string, time.Time) error
	SetBatchDeliveryPlanStatus(context.Context, string, []domain.BatchDeliveryStatus,
		domain.BatchDeliveryStatus, time.Time) error
	CreateBatchDeliveryMergeQueue(context.Context, domain.BatchDeliveryMergeQueue) (
		domain.BatchDeliveryMergeQueue, bool, error)
	GetBatchDeliveryMergeQueue(context.Context, string) (domain.BatchDeliveryMergeQueue, bool, error)
	MarkBatchDeliveryMergeQueueRunning(context.Context, string, string, time.Time) error
	BlockBatchDeliveryMergeQueue(context.Context, string, domain.BatchDeliveryMergeQueueStatus,
		string, string, string, time.Time) error
	CompleteBatchDeliveryMergeStep(context.Context, domain.BatchDeliveryMergeStep, int,
		string, domain.BatchDeliveryMergeQueueStatus, string, string) error
	ListBatchDeliveryMergeSteps(context.Context, string) ([]domain.BatchDeliveryMergeStep, error)
	GetBatchDeliveryMergeQueueByPlan(context.Context, string) (
		domain.BatchDeliveryMergeQueue, bool, error)
	AbortBatchDeliveryMergeQueue(context.Context, string,
		domain.BatchDeliveryMergeQueueStatus, string, string, time.Time) error
	ListRecoverableBatchDeliveryPlans(context.Context, int) ([]domain.BatchDeliveryPlan, error)
	CloseTerminalBatchWorkers(context.Context, string) ([]domain.AgentNode, error)
}

type BatchDeliveryService struct {
	store                           BatchDeliveryStore
	now                             func() time.Time
	hostValidationExecutionEnabled  bool
	executionPermissionCapabilities domain.ExecutionPermissionRuntimeCapabilities
	validationRuntime               ProjectActionRuntime
	validationMu                    sync.Mutex
	workerResources                 batchWorkerResourceCloser
}

func NewBatchDeliveryService(store BatchDeliveryStore) *BatchDeliveryService {
	return &BatchDeliveryService{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// WithValidationCommandRuntime installs the process-owned adapter selector.
// Requests and child-authored validation declarations cannot choose a backend.
func (s *BatchDeliveryService) WithValidationCommandRuntime(runtime any) *BatchDeliveryService {
	if s != nil {
		s.validationRuntime, _ = runtime.(ProjectActionRuntime)
	}
	return s
}

// WithHostValidationExecution enables fixed go/npm validation processes. It
// must only be set by trusted process configuration after the operator has
// enabled the ordinary host-execution boundary; a batch child cannot toggle it.
func (s *BatchDeliveryService) WithHostValidationExecution(enabled bool,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *BatchDeliveryService {
	if s != nil {
		s.hostValidationExecutionEnabled = enabled
		s.executionPermissionCapabilities = capabilities
	}
	return s
}

type PrepareBatchDeliveryRequest struct {
	RunID          string
	ProposalID     string
	Spec           domain.BatchDeliverySpec
	OperationKey   string
	RequestedBy    string
	WorktreeParent string // internal/test override; HTTP never accepts a root path
	Confirm        bool
}

type BatchDeliveryAuthority struct {
	Ordinal        int
	AgentID        string
	Generation     int64
	OwnerToken     string
	Branch         string
	LeaseExpiresAt time.Time
	ToolProfile    domain.BatchDeliveryToolProfile
}

type PrepareBatchDeliveryResult struct {
	Plan        domain.BatchDeliveryPlan
	Workspaces  []domain.BatchDeliveryWorkspace
	Authorities []BatchDeliveryAuthority
	Replayed    bool
}

type BatchDeliverySnapshot struct {
	Plan       domain.BatchDeliveryPlan
	Workspaces []domain.BatchDeliveryWorkspace
	Receipts   []domain.BatchDeliveryReceipt
	Reviews    []domain.BatchDeliveryReview
	Mailbox    map[int][]domain.BatchDeliveryMailboxMessage
	MergeQueue *domain.BatchDeliveryMergeQueue
	MergeSteps []domain.BatchDeliveryMergeStep
}

func (s *BatchDeliveryService) Prepare(ctx context.Context,
	request PrepareBatchDeliveryRequest,
) (PrepareBatchDeliveryResult, error) {
	var result PrepareBatchDeliveryResult
	if s == nil || s.store == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.ProposalID = strings.TrimSpace(request.ProposalID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	operationKey, err := domain.NormalizeAgentOperationKey(strings.TrimSpace(request.OperationKey))
	if err != nil || request.RunID == "" || request.ProposalID == "" ||
		request.RequestedBy == "" || !request.Confirm {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery preparation requires scope, confirmation, requester, and operation key")
	}
	spec, err := domain.NormalizeBatchDeliverySpec(request.Spec)
	if err != nil {
		return result, apperror.Wrap(apperror.CodeInvalidArgument,
			"batch delivery specification is invalid", err)
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery requires a running Run")
	}
	if err := s.requireBatchValidationAuthority(ctx, run, spec); err != nil {
		return result, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	proposal, found, err := s.store.GetChildTaskProposal(ctx, request.ProposalID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "child task proposal was not found")
		}
		return result, apperror.Normalize(err)
	}
	assignments, err := s.store.ListChildTaskAssignments(ctx, proposal.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if err := validateBatchPreparationBinding(run, mission, workspace, proposal,
		assignments, spec); err != nil {
		return result, err
	}
	operationDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryProtocolVersion,
		run.ID, operationKey)
	if existing, exists, loadErr := s.store.GetBatchDeliveryPlanByProposal(ctx,
		proposal.ID); loadErr != nil {
		return result, apperror.Normalize(loadErr)
	} else if exists {
		existingSpecJSON, _ := json.Marshal(existing.Spec)
		requestedSpecJSON, _ := json.Marshal(spec)
		if existing.OperationDigest != operationDigest || existing.RunID != run.ID ||
			existing.WorkspaceID != workspace.ID || existing.CreatedBy != request.RequestedBy ||
			!slices.Equal(existingSpecJSON, requestedSpecJSON) {
			return result, apperror.New(apperror.CodeConflict,
				"child task proposal is already bound to a different batch delivery")
		}
		storedWorkspaces, listErr := s.store.ListBatchDeliveryWorkspaces(ctx, existing.ID)
		if listErr != nil {
			return result, apperror.Normalize(listErr)
		}
		result = PrepareBatchDeliveryResult{Plan: existing, Workspaces: storedWorkspaces,
			Replayed: true}
		if existing.Status.Terminal() {
			return result, nil
		}
		result, reconcileErr := s.reconcileMaterialization(ctx, workspace.RootPath, result)
		return result, apperror.Normalize(reconcileErr)
	}
	repositoryState, err := repository.Inspect(ctx, workspace.RootPath, workspace.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if !repositoryState.Available || !repositoryState.Clean || repositoryState.Detached ||
		repositoryState.FullHead == "" {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery source must be a clean branch-backed Git workspace")
	}
	baseCommit, err := repository.CurrentFullHead(ctx, workspace.RootPath)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	sourceBranch, err := repository.CurrentBranch(ctx, workspace.RootPath)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	specJSON, _ := json.Marshal(spec)
	requestFingerprint := runmutation.Fingerprint("batch-delivery-request.v1", run.ID,
		proposal.ID, workspace.ID, baseCommit, sourceBranch, string(specJSON), request.RequestedBy)
	now := s.now().UTC()
	planID := "batch-" + operationDigest[:24]
	parent, err := prepareBatchWorktreeParent(workspace.RootPath, request.WorktreeParent,
		operationDigest)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	plan := domain.BatchDeliveryPlan{ID: planID, RunID: run.ID, ProposalID: proposal.ID,
		RootAgentID: proposal.RootAgentID, WorkspaceID: workspace.ID,
		Status: domain.BatchDeliveryPreparing, Spec: spec, BaseCommit: baseCommit,
		SourceBranch: sourceBranch, OperationDigest: operationDigest,
		RequestFingerprint: requestFingerprint, CreatedBy: request.RequestedBy,
		CreatedAt: now, UpdatedAt: now}
	profile := domain.DefaultBatchDeliveryToolProfile()
	workspaces := make([]domain.BatchDeliveryWorkspace, len(spec.Tasks))
	authorities := make([]BatchDeliveryAuthority, len(spec.Tasks))
	for index, task := range spec.Tasks {
		token, digest, tokenErr := newBatchDeliveryOwnerToken()
		if tokenErr != nil {
			return result, apperror.Wrap(apperror.CodeInternal,
				"batch delivery owner token could not be generated", tokenErr)
		}
		branch := fmt.Sprintf("codex/batch-%s/task-%d-g1", operationDigest[:12], task.Ordinal)
		destination := filepath.Join(parent, fmt.Sprintf("task-%d-g1", task.Ordinal))
		destination, err = repository.NormalizeWorktreeDestination(workspace.RootPath, destination)
		if err != nil {
			return result, apperror.Normalize(err)
		}
		deadline := now.Add(time.Duration(task.Budget.TimeoutMillis) * time.Millisecond)
		workspaces[index] = domain.BatchDeliveryWorkspace{PlanID: plan.ID,
			Ordinal: task.Ordinal, AgentID: assignments[index].AdmittedAgentID,
			Generation: 1, Status: domain.BatchWorkspacePreparing, Branch: branch,
			WorktreeRoot: destination, BaseCommit: baseCommit, OwnerTokenDigest: digest,
			ToolProfile: profile, ToolProfileFingerprint: profile.Fingerprint(),
			LeaseExpiresAt: deadline, LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now}
		authorities[index] = BatchDeliveryAuthority{Ordinal: task.Ordinal,
			AgentID: assignments[index].AdmittedAgentID, Generation: 1, OwnerToken: token,
			Branch: branch, LeaseExpiresAt: deadline, ToolProfile: profile}
	}
	storedPlan, storedWorkspaces, replayed, err := s.store.CreateBatchDeliveryPlan(ctx,
		plan, workspaces)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result = PrepareBatchDeliveryResult{Plan: storedPlan, Workspaces: storedWorkspaces,
		Replayed: replayed}
	if replayed {
		// Raw owner tokens are deliberately not durable. A caller replaying after
		// losing them must explicitly rotate generation through RotateOwner.
		result, err = s.reconcileMaterialization(ctx, workspace.RootPath, result)
		return result, apperror.Normalize(err)
	}
	result.Authorities = authorities
	result, err = s.reconcileMaterialization(ctx, workspace.RootPath, result)
	if err != nil {
		fresh, found, _ := s.store.GetBatchDeliveryPlan(ctx, plan.ID)
		if found && fresh.Status != domain.BatchDeliveryBlocked && !fresh.Status.Terminal() {
			_ = s.store.SetBatchDeliveryPlanStatus(context.WithoutCancel(ctx), fresh.ID,
				[]domain.BatchDeliveryStatus{fresh.Status}, domain.BatchDeliveryBlocked,
				s.now().UTC())
		}
		return result, apperror.Normalize(err)
	}
	return result, nil
}

func validateBatchPreparationBinding(run domain.Run, mission domain.Mission,
	workspace session.WorkspaceInfo, proposal domain.ChildTaskProposal,
	assignments []domain.ChildTaskAssignment, spec domain.BatchDeliverySpec,
) error {
	if proposal.RunID != run.ID || proposal.WorkspaceID != workspace.ID ||
		mission.WorkspaceID != workspace.ID || proposal.Status != domain.ChildTaskProposalApproved ||
		proposal.Surface != domain.ChildTaskSurfaceCore || len(assignments) != len(spec.Tasks) ||
		len(proposal.Spec.Tasks) != len(spec.Tasks) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery must bind the approved, admitted core proposal in this workspace")
	}
	for index, task := range spec.Tasks {
		child := proposal.Spec.Tasks[index]
		assignment := assignments[index]
		if task.Ordinal != index+1 || assignment.Ordinal != task.Ordinal ||
			assignment.Status != domain.ChildTaskAssignmentAdmitted ||
			assignment.AdmittedAgentID == "" || task.Budget.TurnLimit != child.TurnLimit ||
			task.Budget.TokenLimit != child.TokenLimit ||
			task.Budget.TimeoutMillis != child.TimeoutMillis ||
			!slices.Equal(task.DependencyOrdinals, child.DependencyOrdinals) ||
			!slices.Equal(task.ExpectedArtifacts, child.ExpectedArtifacts) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"batch delivery task, DAG, budget, or artifact contract drifted from admission")
		}
	}
	return nil
}

func prepareBatchWorktreeParent(sourceRoot, override, operationDigest string) (string, error) {
	parent := strings.TrimSpace(override)
	if parent == "" {
		parent = filepath.Join(filepath.Dir(sourceRoot),
			"."+filepath.Base(sourceRoot)+"-cyberagent-worktrees", operationDigest[:24])
	} else {
		parent = filepath.Join(parent, operationDigest[:24])
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create batch delivery worktree parent: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve batch delivery worktree parent: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func (s *BatchDeliveryService) reconcileMaterialization(ctx context.Context,
	sourceRoot string, result PrepareBatchDeliveryResult,
) (PrepareBatchDeliveryResult, error) {
	var joined error
	for index, workspace := range result.Workspaces {
		if workspace.Status != domain.BatchWorkspacePreparing {
			continue
		}
		info, statErr := os.Lstat(workspace.WorktreeRoot)
		switch {
		case statErr == nil:
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				joined = errors.Join(joined, fmt.Errorf("task %d worktree identity is unsafe", workspace.Ordinal))
				continue
			}
			if err := repository.VerifyBatchWorktree(ctx, workspace.WorktreeRoot,
				workspace.Branch, workspace.BaseCommit); err != nil {
				joined = errors.Join(joined, fmt.Errorf("task %d worktree recovery: %w", workspace.Ordinal, err))
				continue
			}
		case errors.Is(statErr, os.ErrNotExist):
			// A crash may leave the exact base-only branch without its directory.
			// The repository helper verifies that identity before deleting it.
			if err := repository.CleanupInterruptedWorktree(ctx, sourceRoot,
				workspace.WorktreeRoot, workspace.Branch, workspace.BaseCommit); err != nil {
				joined = errors.Join(joined, fmt.Errorf("task %d interrupted branch: %w", workspace.Ordinal, err))
				continue
			}
			if _, err := repository.CreateWorktree(ctx, sourceRoot, workspace.WorktreeRoot,
				workspace.Branch, workspace.BaseCommit); err != nil {
				joined = errors.Join(joined, fmt.Errorf("task %d worktree creation: %w", workspace.Ordinal, err))
				continue
			}
		default:
			joined = errors.Join(joined, fmt.Errorf("task %d worktree inspection: %w", workspace.Ordinal, statErr))
			continue
		}
		now := s.now().UTC()
		message := batchDeliveryMessage(result.Plan.ID, workspace.Ordinal,
			workspace.Generation, domain.BatchMailboxDispatch, result.Plan.RootAgentID,
			"child worktree and narrowed tool profile dispatched", nil,
			result.Plan.OperationDigest+fmt.Sprintf("-dispatch-%d", workspace.Ordinal), now)
		activated, _, _, err := s.store.ActivateBatchDeliveryWorkspace(ctx, message,
			workspace.BaseCommit)
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("task %d durable dispatch: %w", workspace.Ordinal, err))
			continue
		}
		result.Workspaces[index] = activated
	}
	fresh, found, err := s.store.GetBatchDeliveryPlan(ctx, result.Plan.ID)
	if err != nil {
		return result, errors.Join(joined, err)
	}
	if found {
		result.Plan = fresh
	}
	if joined == nil && result.Plan.Status == domain.BatchDeliveryBlocked {
		if err := s.store.SetBatchDeliveryPlanStatus(ctx, result.Plan.ID,
			[]domain.BatchDeliveryStatus{domain.BatchDeliveryBlocked}, domain.BatchDeliveryActive,
			s.now().UTC()); err == nil {
			result.Plan.Status = domain.BatchDeliveryActive
		}
	}
	return result, joined
}

func newBatchDeliveryOwnerToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(digest[:]), nil
}

func batchDeliveryOwnerTokenDigest(token string) (string, error) {
	token = strings.TrimSpace(token)
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", errors.New("batch delivery owner token is invalid")
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:]), nil
}

func batchDeliveryMessage(planID string, ordinal int, generation int64,
	kind domain.BatchDeliveryMailboxKind, actor, summary string, evidence []string,
	operationKey string, now time.Time,
) domain.BatchDeliveryMailboxMessage {
	evidence = append([]string(nil), evidence...)
	if evidence == nil {
		evidence = []string{}
	}
	fingerprintJSON, _ := json.Marshal(evidence)
	return domain.BatchDeliveryMailboxMessage{ID: idgen.New("batchmsg"), PlanID: planID,
		Ordinal: ordinal, Generation: generation, Kind: kind, Actor: strings.TrimSpace(actor),
		Summary: strings.TrimSpace(summary), EvidenceRefs: evidence,
		OperationDigest: runmutation.OperationKeyDigest(domain.BatchDeliveryMailboxVersion,
			planID, operationKey),
		RequestFingerprint: runmutation.Fingerprint("batch-delivery-mailbox-request.v1",
			planID, fmt.Sprint(ordinal), fmt.Sprint(generation), string(kind),
			strings.TrimSpace(actor), strings.TrimSpace(summary), string(fingerprintJSON)),
		CreatedAt: now}
}

func (s *BatchDeliveryService) List(ctx context.Context, runID string,
	limit int,
) ([]domain.BatchDeliveryPlan, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "batch delivery store is unavailable")
	}
	items, err := s.store.ListBatchDeliveryPlans(ctx, strings.TrimSpace(runID), limit)
	return items, apperror.Normalize(err)
}

func (s *BatchDeliveryService) Snapshot(ctx context.Context,
	planID string,
) (BatchDeliverySnapshot, error) {
	var snapshot BatchDeliverySnapshot
	if s == nil || s.store == nil {
		return snapshot, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	plan, found, err := s.store.GetBatchDeliveryPlan(ctx, strings.TrimSpace(planID))
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery plan was not found")
		}
		return snapshot, apperror.Normalize(err)
	}
	workspaces, err := s.store.ListBatchDeliveryWorkspaces(ctx, plan.ID)
	if err != nil {
		return snapshot, apperror.Normalize(err)
	}
	snapshot = BatchDeliverySnapshot{Plan: plan, Workspaces: workspaces,
		Mailbox: make(map[int][]domain.BatchDeliveryMailboxMessage, len(workspaces))}
	for _, workspace := range workspaces {
		messages, listErr := s.store.ListBatchDeliveryMailbox(ctx, plan.ID,
			workspace.Ordinal, 512)
		if listErr != nil {
			return BatchDeliverySnapshot{}, apperror.Normalize(listErr)
		}
		snapshot.Mailbox[workspace.Ordinal] = messages
		receipt, exists, getErr := s.store.GetBatchDeliveryReceipt(ctx, plan.ID,
			workspace.Ordinal, workspace.Generation)
		if getErr != nil {
			return BatchDeliverySnapshot{}, apperror.Normalize(getErr)
		}
		if exists {
			snapshot.Receipts = append(snapshot.Receipts, receipt)
			review, reviewed, reviewErr := s.store.GetBatchDeliveryReview(ctx, receipt.ID)
			if reviewErr != nil {
				return BatchDeliverySnapshot{}, apperror.Normalize(reviewErr)
			}
			if reviewed {
				snapshot.Reviews = append(snapshot.Reviews, review)
			}
		}
	}
	queue, queued, err := s.store.GetBatchDeliveryMergeQueueByPlan(ctx, plan.ID)
	if err != nil {
		return BatchDeliverySnapshot{}, apperror.Normalize(err)
	}
	if queued {
		snapshot.MergeQueue = &queue
		snapshot.MergeSteps, err = s.store.ListBatchDeliveryMergeSteps(ctx, queue.ID)
		if err != nil {
			return BatchDeliverySnapshot{}, apperror.Normalize(err)
		}
	}
	return snapshot, nil
}

type SendBatchDeliveryMessageRequest struct {
	PlanID       string
	Ordinal      int
	Generation   int64
	OwnerToken   string
	Kind         domain.BatchDeliveryMailboxKind
	Summary      string
	EvidenceRefs []string
	OperationKey string
}

func (s *BatchDeliveryService) SendMessage(ctx context.Context,
	request SendBatchDeliveryMessageRequest,
) (domain.BatchDeliveryWorkspace, domain.BatchDeliveryMailboxMessage, bool, error) {
	var emptyWorkspace domain.BatchDeliveryWorkspace
	var emptyMessage domain.BatchDeliveryMailboxMessage
	if s == nil || s.store == nil {
		return emptyWorkspace, emptyMessage, false,
			apperror.New(apperror.CodeFailedPrecondition, "batch delivery store is unavailable")
	}
	request.PlanID = strings.TrimSpace(request.PlanID)
	operationKey, err := domain.NormalizeAgentOperationKey(strings.TrimSpace(request.OperationKey))
	if err != nil || request.PlanID == "" || (request.Kind != domain.BatchMailboxAck &&
		request.Kind != domain.BatchMailboxProgress && request.Kind != domain.BatchMailboxQuestion &&
		request.Kind != domain.BatchMailboxEvidence) {
		return emptyWorkspace, emptyMessage, false,
			apperror.New(apperror.CodeInvalidArgument, "batch delivery child message is invalid")
	}
	tokenDigest, err := batchDeliveryOwnerTokenDigest(request.OwnerToken)
	if err != nil {
		return emptyWorkspace, emptyMessage, false,
			apperror.Wrap(apperror.CodePolicyDenied, "batch delivery owner token was rejected", err)
	}
	plan, workspace, task, err := s.loadBatchTask(ctx, request.PlanID, request.Ordinal)
	if err != nil {
		return emptyWorkspace, emptyMessage, false, err
	}
	if workspace.Generation != request.Generation {
		return emptyWorkspace, emptyMessage, false,
			apperror.New(apperror.CodeConflict, "stale batch delivery generation")
	}
	if request.Kind == domain.BatchMailboxProgress || request.Kind == domain.BatchMailboxEvidence {
		if err := s.requireBatchDependencies(ctx, plan, task); err != nil {
			return emptyWorkspace, emptyMessage, false, err
		}
	}
	now := s.now().UTC()
	deadline := workspace.LeaseExpiresAt
	if !now.Before(deadline) {
		return emptyWorkspace, emptyMessage, false,
			apperror.New(apperror.CodeDeadlineExceeded, "batch delivery task deadline expired")
	}
	message := batchDeliveryMessage(plan.ID, workspace.Ordinal, workspace.Generation,
		request.Kind, workspace.AgentID, request.Summary, request.EvidenceRefs, operationKey, now)
	updated, stored, replayed, err := s.store.AppendBatchDeliveryMailbox(ctx, message,
		tokenDigest, deadline)
	return updated, stored, replayed, apperror.Normalize(err)
}

func (s *BatchDeliveryService) loadBatchTask(ctx context.Context, planID string,
	ordinal int,
) (domain.BatchDeliveryPlan, domain.BatchDeliveryWorkspace,
	domain.BatchDeliveryTaskSpec, error) {
	plan, found, err := s.store.GetBatchDeliveryPlan(ctx, strings.TrimSpace(planID))
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery plan was not found")
		}
		return domain.BatchDeliveryPlan{}, domain.BatchDeliveryWorkspace{},
			domain.BatchDeliveryTaskSpec{}, apperror.Normalize(err)
	}
	if ordinal < 1 || ordinal > len(plan.Spec.Tasks) {
		return domain.BatchDeliveryPlan{}, domain.BatchDeliveryWorkspace{},
			domain.BatchDeliveryTaskSpec{}, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery task ordinal is invalid")
	}
	if _, err := s.activeBatchRun(ctx, plan.RunID); err != nil {
		return domain.BatchDeliveryPlan{}, domain.BatchDeliveryWorkspace{},
			domain.BatchDeliveryTaskSpec{}, err
	}
	workspace, found, err := s.store.GetBatchDeliveryWorkspace(ctx, plan.ID, ordinal)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery workspace was not found")
		}
		return domain.BatchDeliveryPlan{}, domain.BatchDeliveryWorkspace{},
			domain.BatchDeliveryTaskSpec{}, apperror.Normalize(err)
	}
	return plan, workspace, plan.Spec.Tasks[ordinal-1], nil
}

func (s *BatchDeliveryService) requireBatchDependencies(ctx context.Context,
	plan domain.BatchDeliveryPlan, task domain.BatchDeliveryTaskSpec,
) error {
	for _, dependency := range task.DependencyOrdinals {
		workspace, found, err := s.store.GetBatchDeliveryWorkspace(ctx, plan.ID, dependency)
		if err != nil || !found {
			return apperror.Normalize(err)
		}
		if workspace.Status != domain.BatchWorkspaceAccepted &&
			workspace.Status != domain.BatchWorkspaceMerged {
			return apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("batch delivery task %d waits for accepted dependency %d",
					task.Ordinal, dependency))
		}
	}
	return nil
}

type SubmitBatchDeliveryRequest struct {
	PlanID       string
	Ordinal      int
	Generation   int64
	OwnerToken   string
	EvidenceRefs []string
	Limitations  []string
	OperationKey string
}

func (s *BatchDeliveryService) Submit(ctx context.Context,
	request SubmitBatchDeliveryRequest,
) (domain.BatchDeliveryReceipt, bool, error) {
	var empty domain.BatchDeliveryReceipt
	if s == nil || s.store == nil {
		return empty, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	operationKey, err := domain.NormalizeAgentOperationKey(strings.TrimSpace(request.OperationKey))
	if err != nil {
		return empty, false, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery submission operation key is invalid")
	}
	tokenDigest, err := batchDeliveryOwnerTokenDigest(request.OwnerToken)
	if err != nil {
		return empty, false, apperror.Wrap(apperror.CodePolicyDenied,
			"batch delivery owner token was rejected", err)
	}
	plan, workspace, task, err := s.loadBatchTask(ctx, request.PlanID, request.Ordinal)
	if err != nil {
		return empty, false, err
	}
	if workspace.Generation != request.Generation {
		return empty, false, apperror.New(apperror.CodeConflict,
			"stale batch delivery generation")
	}
	if workspace.OwnerTokenDigest != tokenDigest {
		return empty, false, apperror.New(apperror.CodePolicyDenied,
			"batch delivery owner token does not match")
	}
	if !s.now().UTC().Before(workspace.LeaseExpiresAt) {
		return empty, false, apperror.New(apperror.CodeDeadlineExceeded,
			"batch delivery submission lease expired")
	}
	if err := s.requireBatchDependencies(ctx, plan, task); err != nil {
		return empty, false, err
	}
	evidence, limitations, err := normalizeBatchDeliveryClaims(request.EvidenceRefs,
		request.Limitations)
	if err != nil {
		return empty, false, err
	}
	if existing, exists, loadErr := s.store.GetBatchDeliveryReceipt(ctx, plan.ID,
		workspace.Ordinal, workspace.Generation); loadErr != nil {
		return empty, false, apperror.Normalize(loadErr)
	} else if exists {
		testsJSON, _ := json.Marshal(existing.TestReceipts)
		claimsJSON, _ := json.Marshal([]any{evidence, limitations})
		operationDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryReceiptVersion,
			plan.ID, operationKey)
		requestFingerprint := runmutation.Fingerprint("batch-delivery-receipt-request.v1",
			plan.ID, fmt.Sprint(workspace.Ordinal), fmt.Sprint(workspace.Generation),
			existing.BaseCommit, existing.HeadCommit, existing.DiffSHA256,
			existing.CallChainSHA256, string(testsJSON), string(claimsJSON))
		if existing.OperationDigest != operationDigest ||
			existing.RequestFingerprint != requestFingerprint {
			return empty, false, apperror.New(apperror.CodeConflict,
				"batch delivery generation already has a different receipt")
		}
		return existing, true, nil
	}
	inspection, err := repository.InspectBatchDelivery(ctx, workspace.WorktreeRoot,
		workspace.Branch, workspace.BaseCommit, plan.Spec.Contract.MaxChangedFiles,
		plan.Spec.Contract.MaxDiffBytes)
	if err != nil {
		return empty, false, apperror.Wrap(apperror.CodeFailedPrecondition,
			"batch delivery requires a clean, branch-bound, committed worktree", err)
	}
	for _, changed := range inspection.ChangedFiles {
		if !domain.BatchOwnershipAllows(task.OwnershipHints, changed) {
			return empty, false, apperror.New(apperror.CodePolicyDenied,
				fmt.Sprintf("batch delivery changed unowned path %q", changed))
		}
	}
	tests, reused, err := s.reuseSpecialistBatchVerification(ctx, plan, workspace, task, inspection.HeadCommit)
	if err == nil && !reused {
		tests, err = s.runBatchDeliveryValidations(ctx, plan.RunID, workspace.WorktreeRoot, workspace.BaseCommit, task.Validations,
			batchValidationTargetRef{PlanID: plan.ID, Ordinal: workspace.Ordinal, Generation: workspace.Generation, OperationKey: "submit:" + operationKey})
	}
	if err != nil {
		return empty, false, err
	}
	postValidation, err := repository.InspectBatchDelivery(ctx, workspace.WorktreeRoot,
		workspace.Branch, workspace.BaseCommit, plan.Spec.Contract.MaxChangedFiles,
		plan.Spec.Contract.MaxDiffBytes)
	if err != nil || !sameBatchDeliveryInspection(inspection, postValidation) {
		return empty, false, apperror.Wrap(apperror.CodeConflict,
			"batch delivery validation changed the submitted worktree; commit and validate a stable generation",
			err)
	}
	now := s.now().UTC()
	testsJSON, _ := json.Marshal(tests)
	claimsJSON, _ := json.Marshal([]any{evidence, limitations})
	receipt := domain.BatchDeliveryReceipt{ID: idgen.New("batchreceipt"), PlanID: plan.ID,
		Ordinal: workspace.Ordinal, Generation: workspace.Generation,
		ProtocolVersion: domain.BatchDeliveryReceiptVersion,
		BaseCommit:      inspection.BaseCommit, HeadCommit: inspection.HeadCommit,
		DiffSHA256: inspection.DiffSHA256, CallChainSHA256: inspection.CallChainSHA256,
		DiffBytes: inspection.DiffBytes, DiffStat: inspection.DiffStat,
		ChangedFiles: inspection.ChangedFiles, TestReceipts: tests,
		EvidenceRefs: evidence, Limitations: limitations,
		OperationDigest: runmutation.OperationKeyDigest(domain.BatchDeliveryReceiptVersion,
			plan.ID, operationKey),
		RequestFingerprint: runmutation.Fingerprint("batch-delivery-receipt-request.v1",
			plan.ID, fmt.Sprint(workspace.Ordinal), fmt.Sprint(workspace.Generation),
			inspection.BaseCommit, inspection.HeadCommit, inspection.DiffSHA256,
			inspection.CallChainSHA256, string(testsJSON), string(claimsJSON)), CreatedAt: now}
	ready := batchDeliveryMessage(plan.ID, workspace.Ordinal, workspace.Generation,
		domain.BatchMailboxReadyForReview, workspace.AgentID,
		"committed delivery is ready for independent review", evidence,
		operationKey+"-ready", now)
	stored, replayed, err := s.store.RecordBatchDeliveryReceipt(ctx, receipt,
		tokenDigest, ready)
	return stored, replayed, apperror.Normalize(err)
}

func (s *BatchDeliveryService) runBatchDeliveryValidations(ctx context.Context,
	runID, root, baseCommit string,
	requirements []domain.BatchDeliveryValidationRequirement,
	refs ...batchValidationTargetRef,
) ([]domain.BatchDeliveryTestReceipt, error) {
	results := make([]domain.BatchDeliveryTestReceipt, 0, len(requirements))
	for _, requirement := range requirements {
		starter := runner.NewPlatformOnceProcessStarter()
		var runtimeStarter *batchRuntimeValidationStarter
		if requirement.Kind != domain.BatchValidationGitDiffCheck {
			if err := s.authorizeBatchHostValidation(ctx, runID); err != nil {
				return results, err
			}
			if s.validationRuntime != nil {
				if len(refs) != 1 {
					return results, apperror.New(apperror.CodeFailedPrecondition, "batch validation target is unavailable")
				}
				selected, err := s.validationRuntime.projectCommandService(ctx, runID)
				if err != nil {
					return results, err
				}
				target, err := s.bindBatchValidationTarget(ctx, runID, root, baseCommit, refs[0])
				if err != nil {
					return results, err
				}
				runtimeStarter = &batchRuntimeValidationStarter{service: s, runtime: selected, target: target, requirement: requirement, readinessCheckedAt: s.now().UTC()}
				starter = runtimeStarter
			} else {
				if err := s.requireBatchLocalHostValidation(ctx, runID); err != nil {
					return results, err
				}
				starter = &batchValidationStarter{OnceStarter: starter, service: s, runID: runID}
			}
		}
		result, err := repository.RunBatchValidation(ctx, root, baseCommit, requirement, starter)
		if err != nil && (apperror.CodeOf(err) == apperror.CodePolicyDenied || apperror.CodeOf(err) == apperror.CodeConflict) {
			return results, err
		}
		receipt := domain.BatchDeliveryTestReceipt{RequirementID: result.RequirementID,
			Kind: result.Kind, Scope: result.Scope, ExitCode: result.ExitCode,
			OutputSHA256: result.OutputSHA256, DurationMillis: result.DurationMillis,
			CompletedAt: result.CompletedAt}
		if runtimeStarter != nil {
			receipt.Execution = runtimeStarter.execution
		} else if !result.CompletedAt.IsZero() {
			rootSHA, _ := runner.CommandRuntimeWorkspaceRootSHA256(root)
			receipt.Execution = batchValidationExecutionReceipt(commandruntimeadapter.HostUnsandboxed(runner.OnceExecutionProtocolVersion), rootSHA)
			receipt.Execution.TreeReaped, receipt.Execution.StdinClosed = true, true
		}
		results = append(results, receipt)
		if err != nil {
			return results, apperror.Wrap(apperror.CodeFailedPrecondition,
				"batch delivery validation failed", err)
		}
	}
	return results, nil
}

func (s *BatchDeliveryService) requireBatchValidationAuthority(ctx context.Context,
	run domain.Run, spec domain.BatchDeliverySpec,
) error {
	for _, task := range spec.Tasks {
		for _, validation := range task.Validations {
			if validation.Kind != domain.BatchValidationGitDiffCheck {
				return s.authorizeBatchHostValidation(ctx, run.ID)
			}
		}
	}
	return nil
}

func (s *BatchDeliveryService) activeBatchRun(ctx context.Context,
	runID string,
) (domain.Run, error) {
	if s == nil || s.store == nil {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	run, err := s.store.GetRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.Run{}, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery authority requires a running Run")
	}
	return run, nil
}

func normalizeBatchDeliveryClaims(evidence, limitations []string) ([]string, []string, error) {
	if len(evidence) > domain.MaxBatchMailboxEvidenceRefs ||
		len(limitations) > domain.MaxBatchDeliveryLimitations {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery claims exceed their bounded limits")
	}
	evidence = append([]string(nil), evidence...)
	limitations = append([]string(nil), limitations...)
	for index := range evidence {
		evidence[index] = strings.TrimSpace(evidence[index])
		if evidence[index] == "" || len([]byte(evidence[index])) > 2048 ||
			strings.ContainsRune(evidence[index], 0) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery evidence reference is invalid")
		}
	}
	for index := range limitations {
		limitations[index] = strings.TrimSpace(limitations[index])
		if limitations[index] == "" ||
			len([]rune(limitations[index])) > domain.MaxBatchDeliveryLimitationRunes {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery limitation is invalid")
		}
	}
	sort.Strings(evidence)
	sort.Strings(limitations)
	return evidence, limitations, nil
}

type ReviewBatchDeliveryRequest struct {
	PlanID            string
	Ordinal           int
	Generation        int64
	Reviewer          string
	Verdict           domain.BatchDeliveryReviewVerdict
	Summary           string
	FullDiffReviewed  bool
	CallChainReviewed bool
	TestsReviewed     bool
	OperationKey      string
}

func (s *BatchDeliveryService) Review(ctx context.Context,
	request ReviewBatchDeliveryRequest,
) (domain.BatchDeliveryReview, bool, error) {
	var empty domain.BatchDeliveryReview
	operationKey, err := domain.NormalizeAgentOperationKey(strings.TrimSpace(request.OperationKey))
	request.Reviewer = strings.TrimSpace(request.Reviewer)
	request.Summary = strings.TrimSpace(request.Summary)
	if s == nil || s.store == nil {
		return empty, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	if err != nil || request.Reviewer == "" || request.Summary == "" ||
		(request.Verdict != domain.BatchReviewAccepted &&
			request.Verdict != domain.BatchReviewChangesRequested) ||
		!request.FullDiffReviewed || !request.CallChainReviewed || !request.TestsReviewed {
		return empty, false, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery review requires explicit full-diff, call-chain, and test confirmation")
	}
	plan, workspace, task, err := s.loadBatchTask(ctx, request.PlanID, request.Ordinal)
	if err != nil {
		return empty, false, err
	}
	if workspace.Generation != request.Generation || request.Reviewer == workspace.AgentID {
		return empty, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery review must be independent and target the current generation")
	}
	receipt, found, err := s.store.GetBatchDeliveryReceipt(ctx, plan.ID,
		workspace.Ordinal, workspace.Generation)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery receipt was not found")
		}
		return empty, false, apperror.Normalize(err)
	}
	operationDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryReviewVersion,
		plan.ID, operationKey)
	requestFingerprint := runmutation.Fingerprint("batch-delivery-review-request.v1",
		receipt.ID, request.Reviewer, string(request.Verdict), request.Summary)
	if existing, reviewed, loadErr := s.store.GetBatchDeliveryReview(ctx,
		receipt.ID); loadErr != nil {
		return empty, false, apperror.Normalize(loadErr)
	} else if reviewed {
		if existing.OperationDigest != operationDigest ||
			existing.RequestFingerprint != requestFingerprint {
			return empty, false, apperror.New(apperror.CodeConflict,
				"batch delivery receipt already has a different review")
		}
		return existing, true, nil
	}
	if workspace.Status != domain.BatchWorkspaceReadyForReview {
		return empty, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery review requires the current ready generation")
	}
	inspection, err := repository.InspectBatchDelivery(ctx, workspace.WorktreeRoot,
		workspace.Branch, workspace.BaseCommit, plan.Spec.Contract.MaxChangedFiles,
		plan.Spec.Contract.MaxDiffBytes)
	if err != nil {
		return empty, false, apperror.Wrap(apperror.CodeFailedPrecondition,
			"independent batch delivery diff inspection failed", err)
	}
	if inspection.BaseCommit != receipt.BaseCommit || inspection.HeadCommit != receipt.HeadCommit ||
		inspection.DiffSHA256 != receipt.DiffSHA256 ||
		inspection.CallChainSHA256 != receipt.CallChainSHA256 ||
		!slices.Equal(inspection.ChangedFiles, receipt.ChangedFiles) {
		return empty, false, apperror.New(apperror.CodeConflict,
			"batch delivery changed after receipt; submit and review a new generation")
	}
	for _, changed := range inspection.ChangedFiles {
		if !domain.BatchOwnershipAllows(task.OwnershipHints, changed) {
			return empty, false, apperror.New(apperror.CodePolicyDenied,
				"independent review found a change outside child ownership")
		}
	}
	if _, err := s.runBatchDeliveryValidations(ctx, plan.RunID,
		workspace.WorktreeRoot, workspace.BaseCommit, task.Validations,
		batchValidationTargetRef{PlanID: plan.ID, Ordinal: workspace.Ordinal, Generation: workspace.Generation, OperationKey: "review:" + operationKey}); err != nil {
		return empty, false, err
	}
	postValidation, err := repository.InspectBatchDelivery(ctx, workspace.WorktreeRoot,
		workspace.Branch, workspace.BaseCommit, plan.Spec.Contract.MaxChangedFiles,
		plan.Spec.Contract.MaxDiffBytes)
	if err != nil || !sameBatchDeliveryInspection(inspection, postValidation) {
		return empty, false, apperror.Wrap(apperror.CodeConflict,
			"batch delivery validation changed the reviewed worktree; submit and review a stable generation",
			err)
	}
	now := s.now().UTC()
	review := domain.BatchDeliveryReview{ID: idgen.New("batchreview"), PlanID: plan.ID,
		Ordinal: workspace.Ordinal, Generation: workspace.Generation,
		ProtocolVersion: domain.BatchDeliveryReviewVersion, ReceiptID: receipt.ID,
		Reviewer: request.Reviewer, Verdict: request.Verdict, Summary: request.Summary,
		BaseCommit: receipt.BaseCommit, HeadCommit: receipt.HeadCommit,
		DiffSHA256: receipt.DiffSHA256, CallChainSHA256: receipt.CallChainSHA256,
		FullDiffReviewed:  request.FullDiffReviewed,
		CallChainReviewed: request.CallChainReviewed, TestsReviewed: request.TestsReviewed,
		OperationDigest: operationDigest, RequestFingerprint: requestFingerprint,
		CreatedAt: now}
	kind := domain.BatchMailboxChangesRequested
	if request.Verdict == domain.BatchReviewAccepted {
		kind = domain.BatchMailboxAccepted
	}
	message := batchDeliveryMessage(plan.ID, workspace.Ordinal, workspace.Generation,
		kind, request.Reviewer, request.Summary, nil, operationKey+"-mailbox", now)
	stored, replayed, err := s.store.RecordBatchDeliveryReview(ctx, review, message)
	return stored, replayed, apperror.Normalize(err)
}

func sameBatchDeliveryInspection(left, right repository.BatchDeliveryInspection) bool {
	return left.BaseCommit == right.BaseCommit &&
		left.HeadCommit == right.HeadCommit &&
		left.Branch == right.Branch &&
		left.DiffSHA256 == right.DiffSHA256 &&
		left.CallChainSHA256 == right.CallChainSHA256 &&
		left.DiffBytes == right.DiffBytes &&
		left.DiffStat == right.DiffStat &&
		left.Clean && right.Clean &&
		slices.Equal(left.ChangedFiles, right.ChangedFiles)
}

type RenewBatchDeliveryOwnerRequest struct {
	PlanID             string
	Ordinal            int
	ExpectedGeneration int64
	Retry              bool
	RequestedBy        string
	Confirm            bool
}

func (s *BatchDeliveryService) RenewOwner(ctx context.Context,
	request RenewBatchDeliveryOwnerRequest,
) (domain.BatchDeliveryWorkspace, BatchDeliveryAuthority, error) {
	var workspace domain.BatchDeliveryWorkspace
	var authority BatchDeliveryAuthority
	if s == nil || s.store == nil || strings.TrimSpace(request.RequestedBy) == "" ||
		!request.Confirm || request.ExpectedGeneration <= 0 ||
		request.ExpectedGeneration >= domain.MaxBatchDeliveryGenerations {
		return workspace, authority, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery owner renewal request is invalid")
	}
	plan, current, task, err := s.loadBatchTask(ctx, request.PlanID, request.Ordinal)
	if err != nil {
		return workspace, authority, err
	}
	if current.Generation != request.ExpectedGeneration || plan.Status.Terminal() {
		return workspace, authority, apperror.New(apperror.CodeConflict,
			"batch delivery owner renewal generation is stale")
	}
	token, digest, err := newBatchDeliveryOwnerToken()
	if err != nil {
		return workspace, authority, apperror.Normalize(err)
	}
	now := s.now().UTC()
	deadline := now.Add(time.Duration(task.Budget.TimeoutMillis) * time.Millisecond)
	if request.Retry {
		workspace, err = s.store.RetryBatchDeliveryWorkspace(ctx, plan.ID, current.Ordinal,
			current.Generation, digest, deadline, now)
	} else {
		workspace, err = s.store.RotateBatchDeliveryWorkspaceOwner(ctx, plan.ID,
			current.Ordinal, current.Generation, digest, deadline, now)
	}
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, authority, apperror.Normalize(err)
	}
	authority = BatchDeliveryAuthority{Ordinal: workspace.Ordinal, AgentID: workspace.AgentID,
		Generation: workspace.Generation, OwnerToken: token, Branch: workspace.Branch,
		LeaseExpiresAt: workspace.LeaseExpiresAt, ToolProfile: workspace.ToolProfile}
	return workspace, authority, nil
}
