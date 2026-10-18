package application

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

const BatchDeliveryWorkbenchVersion = "batch-delivery-workbench.v1"

// BatchDeliveryWorker is a trusted Go runtime bridge. Implementations must use
// the ordinary accounted model lifecycle and the narrowed Batch tools. This is
// deliberately not a renderer-supplied executor or a general command surface.
type BatchDeliveryWorker interface {
	ExecuteBatchChild(context.Context, BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error)
}

type BatchDeliveryWorkRequest struct {
	Plan      domain.BatchDeliveryPlan
	Workspace domain.BatchDeliveryWorkspace
	Task      domain.ChildTask
	Feedback  string
	Authority BatchDeliveryToolAuthority
}

type BatchDeliveryWorkResult struct {
	EvidenceRefs    []string
	Limitations     []string
	WaitingApproval bool
}

type BatchDeliveryWorkbenchTask struct {
	Ordinal        int
	OwnershipHints []domain.BatchDeliveryOwnershipHint
	Validations    []domain.BatchDeliveryValidationRequirement
}

type PrepareBatchDeliveryWorkbenchRequest struct {
	RunID, ProposalID, OperationKey, RequestedBy string
	Tasks                                        []BatchDeliveryWorkbenchTask
	Confirm                                      bool
}

type BatchDeliveryWorkbenchChild struct {
	Ordinal, Generation int64
	OwnerAvailable      bool
	Executing           bool
	OutcomeUnresolved   bool
	WaitingApproval     bool
}

type BatchDeliveryWorkbenchSnapshot struct {
	Snapshot        BatchDeliverySnapshot
	WorkerAvailable bool
	Children        []BatchDeliveryWorkbenchChild
	Replayed        bool
}

type BatchDeliveryWorkbenchOwnerRequest struct {
	PlanID, OperationKey, RequestedBy string
	Ordinal                           int
	ExpectedGeneration                int64
	Retry, Confirm                    bool
}

type ExecuteBatchDeliveryWorkbenchRequest struct {
	PlanID, OperationKey string
	Ordinal              int
	ExpectedGeneration   int64
	Confirm              bool
}

// The adapter retains raw owners only in this process. SQLite's existing digest
// remains the final fence. After restart the operator explicitly rotates the
// observed generation; reading a snapshot never creates execution authority.
type BatchDeliveryWorkbenchService struct {
	*BatchDeliveryService
	mu     sync.Mutex
	owners map[string]BatchDeliveryAuthority
	busy   map[string]bool
	worker BatchDeliveryWorker
}

func NewBatchDeliveryWorkbenchService(kernel *BatchDeliveryService) *BatchDeliveryWorkbenchService {
	return &BatchDeliveryWorkbenchService{BatchDeliveryService: kernel,
		owners: make(map[string]BatchDeliveryAuthority), busy: make(map[string]bool)}
}

func (s *BatchDeliveryWorkbenchService) WithWorker(worker BatchDeliveryWorker) *BatchDeliveryWorkbenchService {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.worker = worker
	s.BatchDeliveryService.validationMu.Lock()
	s.BatchDeliveryService.workerResources, _ = worker.(batchWorkerResourceCloser)
	s.BatchDeliveryService.validationMu.Unlock()
	return s
}

func (s *BatchDeliveryWorkbenchService) Reconcile(ctx context.Context, planID string) (BatchDeliveryReconcileResult, error) {
	result, err := s.BatchDeliveryService.Reconcile(ctx, planID)
	if err != nil {
		return result, err
	}
	settler, ok := s.store.(interface {
		SettleBatchDeliveryDependencies(context.Context, string, int, int64, string, string) ([]domain.DependencyWake, error)
	})
	if !ok {
		return result, nil
	}
	snapshot, err := s.Snapshot(ctx, planID)
	if err != nil {
		return result, err
	}
	for _, review := range snapshot.Reviews {
		if review.Verdict != domain.BatchReviewAccepted {
			continue
		}
		if _, err := settler.SettleBatchDeliveryDependencies(ctx, planID, review.Ordinal, review.Generation, review.ReceiptID, review.ID); err != nil {
			return result, apperror.Normalize(err)
		}
	}
	return result, nil
}

func batchWorkbenchChildKey(planID string, ordinal int) string {
	return planID + "/" + strconv.Itoa(ordinal)
}

func normalizeBatchWorkbenchOperationKey(value string) (string, error) {
	key, err := domain.NormalizeAgentOperationKey(value)
	// Leave room for the durable sub-operations before any generation changes.
	if err != nil || len([]byte(key)) > domain.MaxAgentOperationKeyBytes-32 {
		return "", apperror.New(apperror.CodeInvalidArgument, "batch workbench operation key is invalid or too long")
	}
	return key, nil
}

func (s *BatchDeliveryWorkbenchService) WorkbenchSnapshot(ctx context.Context, planID string) (BatchDeliveryWorkbenchSnapshot, error) {
	snapshot, err := s.Snapshot(ctx, planID)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := BatchDeliveryWorkbenchSnapshot{Snapshot: snapshot, WorkerAvailable: s.worker != nil,
		Children: make([]BatchDeliveryWorkbenchChild, len(snapshot.Workspaces))}
	for index, child := range snapshot.Workspaces {
		key := batchWorkbenchChildKey(planID, child.Ordinal)
		owner, ok := s.owners[key]
		digest, _ := batchDeliveryOwnerTokenDigest(owner.OwnerToken)
		result.Children[index] = BatchDeliveryWorkbenchChild{Ordinal: int64(child.Ordinal), Generation: child.Generation,
			OwnerAvailable: ok && owner.Generation == child.Generation && digest == child.OwnerTokenDigest &&
				s.now().UTC().Before(child.LeaseExpiresAt), Executing: s.busy[key]}
		if reader, ok := s.store.(interface {
			GetAgentNode(context.Context, string) (domain.AgentNode, error)
		}); ok {
			node, err := reader.GetAgentNode(ctx, child.AgentID)
			if err != nil {
				return BatchDeliveryWorkbenchSnapshot{}, err
			}
			waiting, _, err := batchWorkerApprovalWait(ctx, s.store, node)
			if err != nil {
				return BatchDeliveryWorkbenchSnapshot{}, err
			}
			result.Children[index].WaitingApproval = waiting
		}
		hasReceipt := false
		for _, receipt := range snapshot.Receipts {
			if receipt.Ordinal == child.Ordinal && receipt.Generation == child.Generation {
				hasReceipt = true
			}
		}
		for _, message := range snapshot.Mailbox[child.Ordinal] {
			if message.Generation == child.Generation && message.Summary == "explicit child execution started" &&
				message.Kind == domain.BatchMailboxProgress && !hasReceipt && !s.busy[key] && !result.Children[index].WaitingApproval {
				result.Children[index].OutcomeUnresolved = true
			}
		}
	}
	return result, nil
}

func (s *BatchDeliveryWorkbenchService) PrepareWorkbench(ctx context.Context, request PrepareBatchDeliveryWorkbenchRequest) (BatchDeliveryWorkbenchSnapshot, error) {
	if s == nil || s.BatchDeliveryService == nil || s.store == nil {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "batch workbench is unavailable")
	}
	if _, err := normalizeBatchWorkbenchOperationKey(request.OperationKey); err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	proposal, found, err := s.store.GetChildTaskProposal(ctx, request.ProposalID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "child task proposal was not found")
		}
		return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
	}
	if len(request.Tasks) != len(proposal.Spec.Tasks) || len(request.Tasks) > domain.MaxBatchDeliveryTasks {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeInvalidArgument, "batch preparation must include the complete admitted task set")
	}
	spec := domain.BatchDeliverySpec{Version: domain.BatchDeliveryProtocolVersion,
		Tasks: make([]domain.BatchDeliveryTaskSpec, len(request.Tasks)),
		Contract: domain.BatchDeliveryContract{RequireClean: true, RequireIndependentReview: true,
			RequireAllValidations: true, MaxChangedFiles: 128, MaxDiffBytes: 8 * 1024 * 1024}}
	for index, input := range request.Tasks {
		child := proposal.Spec.Tasks[index]
		if input.Ordinal != index+1 {
			return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeInvalidArgument, "batch preparation ordinals must match admission")
		}
		spec.Tasks[index] = domain.BatchDeliveryTaskSpec{Ordinal: input.Ordinal,
			OwnershipHints: input.OwnershipHints, Validations: input.Validations,
			DependencyOrdinals: child.DependencyOrdinals, ExpectedArtifacts: child.ExpectedArtifacts,
			Budget: domain.BatchDeliveryBudget{TurnLimit: child.TurnLimit, TokenLimit: child.TokenLimit, TimeoutMillis: child.TimeoutMillis}}
	}
	prepared, err := s.Prepare(ctx, PrepareBatchDeliveryRequest{RunID: request.RunID, ProposalID: request.ProposalID,
		Spec: spec, OperationKey: request.OperationKey, RequestedBy: request.RequestedBy, Confirm: request.Confirm})
	// Keep issued owners even if materialization later reports an error. They are
	// still unusable until the durable kernel state permits a child operation.
	s.mu.Lock()
	for _, owner := range prepared.Authorities {
		s.owners[batchWorkbenchChildKey(prepared.Plan.ID, owner.Ordinal)] = owner
	}
	s.mu.Unlock()
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	result, err := s.WorkbenchSnapshot(ctx, prepared.Plan.ID)
	result.Replayed = prepared.Replayed
	return result, err
}

func (s *BatchDeliveryWorkbenchService) RenewWorkbenchOwner(ctx context.Context, request BatchDeliveryWorkbenchOwnerRequest) (BatchDeliveryWorkbenchSnapshot, error) {
	if !request.Confirm {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeInvalidArgument, "owner recovery requires confirmation")
	}
	operationKey, err := normalizeBatchWorkbenchOperationKey(request.OperationKey)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeInvalidArgument, "owner recovery operation key is invalid")
	}
	key := batchWorkbenchChildKey(request.PlanID, request.Ordinal)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[key] {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeConflict, "child execution is in progress; check its result before recovering ownership")
	}
	plan, child, _, err := s.loadBatchTask(ctx, request.PlanID, request.Ordinal)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	markerKey := operationKey + "-owner"
	digest := runmutation.OperationKeyDigest(domain.BatchDeliveryMailboxVersion, plan.ID, markerKey)
	marker, exists, err := s.store.GetBatchDeliveryMailboxByOperationDigest(ctx, digest)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
	}
	expectedSummary := fmt.Sprintf("owner recovery from generation %d; retry=%t", request.ExpectedGeneration, request.Retry)
	if exists {
		if marker.Ordinal != request.Ordinal || marker.Generation != request.ExpectedGeneration+1 || marker.Summary != expectedSummary {
			return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeConflict, "owner recovery operation key was reused")
		}
		return s.workbenchSnapshotLocked(ctx, plan.ID, true)
	}
	// A rotation may have committed before its mailbox marker or process state.
	// Never mint another owner on replay against a newer generation.
	if child.Generation != request.ExpectedGeneration {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeConflict, "owner generation changed; inspect the current generation before starting a new recovery")
	}
	updated, owner, err := s.RenewOwner(ctx, RenewBatchDeliveryOwnerRequest{PlanID: plan.ID, Ordinal: request.Ordinal,
		ExpectedGeneration: request.ExpectedGeneration, Retry: request.Retry, RequestedBy: request.RequestedBy, Confirm: true})
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	s.owners[key] = owner
	markerKind := domain.BatchMailboxProgress
	if updated.Status == domain.BatchWorkspaceDispatched {
		markerKind = domain.BatchMailboxAck
	}
	_, _, _, err = s.SendMessage(ctx, SendBatchDeliveryMessageRequest{PlanID: plan.ID, Ordinal: updated.Ordinal,
		Generation: updated.Generation, OwnerToken: owner.OwnerToken, Kind: markerKind,
		Summary: expectedSummary, OperationKey: markerKey})
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	return s.workbenchSnapshotLocked(ctx, plan.ID, false)
}

// Call only with mu held; release it for the normal read projection.
func (s *BatchDeliveryWorkbenchService) workbenchSnapshotLocked(ctx context.Context, planID string, replayed bool) (BatchDeliveryWorkbenchSnapshot, error) {
	s.mu.Unlock()
	result, err := s.WorkbenchSnapshot(ctx, planID)
	s.mu.Lock()
	result.Replayed = replayed
	return result, err
}

func (s *BatchDeliveryWorkbenchService) ExecuteWorkbench(ctx context.Context, request ExecuteBatchDeliveryWorkbenchRequest) (BatchDeliveryWorkbenchSnapshot, error) {
	operationKey, err := normalizeBatchWorkbenchOperationKey(request.OperationKey)
	if err != nil || !request.Confirm || request.ExpectedGeneration < 1 {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeInvalidArgument, "child execution requires an operation key, current generation and confirmation")
	}
	key := batchWorkbenchChildKey(request.PlanID, request.Ordinal)
	s.mu.Lock()
	owner, hasOwner := s.owners[key]
	worker := s.worker
	if s.busy[key] {
		s.mu.Unlock()
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeConflict, "child execution is already in progress")
	}
	s.busy[key] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.busy, key); s.mu.Unlock() }()
	plan, child, task, err := s.loadBatchTask(ctx, request.PlanID, request.Ordinal)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	if child.Generation != request.ExpectedGeneration {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeConflict, "child execution generation changed")
	}
	intentKey := operationKey + "-execute"
	intentDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryMailboxVersion, plan.ID, intentKey)
	intent, exists, err := s.store.GetBatchDeliveryMailboxByOperationDigest(ctx, intentDigest)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
	}
	resuming := false
	if exists {
		if intent.Ordinal != request.Ordinal || intent.Generation != request.ExpectedGeneration {
			return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeConflict, "child execution operation key was reused")
		}
		if reader, ok := s.store.(interface {
			GetAgentNode(context.Context, string) (domain.AgentNode, error)
		}); ok {
			node, err := reader.GetAgentNode(ctx, child.AgentID)
			if err != nil {
				return BatchDeliveryWorkbenchSnapshot{}, err
			}
			waiting, pending, err := batchWorkerApprovalWait(ctx, s.store, node)
			if err != nil {
				return BatchDeliveryWorkbenchSnapshot{}, err
			}
			resuming = waiting && !pending
		}
		// An exact approval wait can resume the existing attempt. All other
		// model dispatch outcomes may be unknown. Replay reads receipt/state
		// and may retry submitted-resource cleanup, without repeating work.
		if !resuming {
			if _, submitted, err := s.store.GetBatchDeliveryReceipt(ctx, plan.ID, child.Ordinal, child.Generation); err != nil {
				return BatchDeliveryWorkbenchSnapshot{}, err
			} else if submitted {
				if closer, ok := worker.(batchWorkerResourceCloser); ok {
					if err := closer.CloseBatchWorkerResources(domain.AgentAttemptRef{RunID: plan.RunID, AgentID: child.AgentID}); err != nil {
						return BatchDeliveryWorkbenchSnapshot{}, err
					}
				}
			}
			s.mu.Lock()
			delete(s.busy, key)
			s.mu.Unlock()
			result, err := s.WorkbenchSnapshot(ctx, plan.ID)
			result.Replayed = true
			return result, err
		}
	}
	messages, err := s.store.ListBatchDeliveryMailbox(ctx, plan.ID, child.Ordinal, 512)
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
	}
	for _, message := range messages {
		if !resuming && message.Generation == child.Generation && message.Summary == "explicit child execution started" && message.Kind == domain.BatchMailboxProgress {
			return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "this generation already has an execution intent; inspect its result and recover a new generation before retrying")
		}
	}
	if worker == nil {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "the accounted batch child runtime is unavailable")
	}
	if !hasOwner || owner.Generation != child.Generation {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "child ownership needs explicit recovery")
	}
	if child.Status == domain.BatchWorkspaceDispatched {
		_, _, _, err = s.SendMessage(ctx, SendBatchDeliveryMessageRequest{PlanID: plan.ID, Ordinal: child.Ordinal,
			Generation: child.Generation, OwnerToken: owner.OwnerToken, Kind: domain.BatchMailboxAck,
			Summary: "child accepted the isolated task", OperationKey: operationKey + "-ack"})
		if err != nil {
			return BatchDeliveryWorkbenchSnapshot{}, err
		}
	}
	authority := BatchDeliveryToolAuthority{PlanID: plan.ID, Ordinal: child.Ordinal, Generation: child.Generation,
		OwnerToken: owner.OwnerToken, OperationKey: operationKey}
	if _, err := s.bindBatchDeliveryTool(ctx, authority); err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	if err := s.requireBatchDependencies(ctx, plan, task); err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	proposal, found, err := s.store.GetChildTaskProposal(ctx, plan.ProposalID)
	if err != nil || !found {
		return BatchDeliveryWorkbenchSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "admitted child task is unavailable")
	}
	feedback := ""
	for generation := child.Generation - 1; generation > 0; generation-- {
		previous, found, err := s.store.GetBatchDeliveryReceipt(ctx, plan.ID, child.Ordinal, generation)
		if err != nil {
			return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
		}
		if found {
			review, found, err := s.store.GetBatchDeliveryReview(ctx, previous.ID)
			if err != nil {
				return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
			}
			if found && review.Verdict == domain.BatchReviewChangesRequested {
				feedback = review.Summary
				break
			}
		}
	}
	_, _, _, err = s.SendMessage(ctx, SendBatchDeliveryMessageRequest{PlanID: plan.ID, Ordinal: child.Ordinal,
		Generation: child.Generation, OwnerToken: owner.OwnerToken, Kind: domain.BatchMailboxProgress,
		Summary: "explicit child execution started", OperationKey: intentKey})
	if err != nil {
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	workCtx, cancel := context.WithDeadline(ctx, child.LeaseExpiresAt)
	defer cancel()
	work, err := worker.ExecuteBatchChild(workCtx, BatchDeliveryWorkRequest{Plan: plan, Workspace: child,
		Task: proposal.Spec.Tasks[child.Ordinal-1], Feedback: feedback, Authority: authority})
	if err != nil {
		s.recordWorkbenchFailure(ctx, authority, operationKey, err)
		return BatchDeliveryWorkbenchSnapshot{}, apperror.Normalize(err)
	}
	if work.WaitingApproval {
		s.mu.Lock()
		delete(s.busy, key)
		s.mu.Unlock()
		return s.WorkbenchSnapshot(ctx, plan.ID)
	}
	_, _, err = s.Submit(ctx, SubmitBatchDeliveryRequest{PlanID: plan.ID, Ordinal: child.Ordinal,
		Generation: child.Generation, OwnerToken: owner.OwnerToken, EvidenceRefs: work.EvidenceRefs,
		Limitations: work.Limitations, OperationKey: operationKey + "-submit"})
	if err != nil {
		s.recordWorkbenchFailure(ctx, authority, operationKey, err)
		return BatchDeliveryWorkbenchSnapshot{}, err
	}
	if closer, ok := worker.(batchWorkerResourceCloser); ok {
		if err := closer.CloseBatchWorkerResources(domain.AgentAttemptRef{RunID: plan.RunID, AgentID: child.AgentID}); err != nil {
			return BatchDeliveryWorkbenchSnapshot{}, err
		}
	}
	s.mu.Lock()
	delete(s.busy, key)
	s.mu.Unlock()
	return s.WorkbenchSnapshot(ctx, plan.ID)
}

// Persist a stable action and code, never arbitrary provider response text. A
// cancelled caller can still leave a diagnostic while the owner lease is valid.
func (s *BatchDeliveryWorkbenchService) recordWorkbenchFailure(ctx context.Context, authority BatchDeliveryToolAuthority, operationKey string, cause error) {
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _, _, _ = s.SendMessage(diagnosticCtx, SendBatchDeliveryMessageRequest{PlanID: authority.PlanID,
		Ordinal: authority.Ordinal, Generation: authority.Generation, OwnerToken: authority.OwnerToken,
		Kind: domain.BatchMailboxQuestion, OperationKey: operationKey + "-result",
		Summary: "child execution stopped: " + string(apperror.CodeOf(apperror.Normalize(cause))) +
			"; inspect the child delivery and recover ownership before another attempt"})
}
