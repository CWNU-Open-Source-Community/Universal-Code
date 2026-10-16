package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const (
	BatchDeliveriesPathTemplate      = "/api/v1/runs/{run_id}/batch-deliveries"
	BatchDeliveryPathTemplate        = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}"
	BatchDeliveryReviewPathTemplate  = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/children/{ordinal}/review"
	BatchDeliveryRenewPathTemplate   = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/children/{ordinal}/renew-owner"
	BatchDeliveryMergePathTemplate   = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/merge"
	BatchDeliveryCancelPathTemplate  = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/cancel"
	BatchDeliveryRecoverPathTemplate = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/reconcile"
	BatchDeliveriesListVersion       = "batch-deliveries-list.v1"
)

type BatchDeliveryController interface {
	Prepare(context.Context, application.PrepareBatchDeliveryRequest) (
		application.PrepareBatchDeliveryResult, error)
	List(context.Context, string, int) ([]domain.BatchDeliveryPlan, error)
	Snapshot(context.Context, string) (application.BatchDeliverySnapshot, error)
	Review(context.Context, application.ReviewBatchDeliveryRequest) (
		domain.BatchDeliveryReview, bool, error)
	RenewOwner(context.Context, application.RenewBatchDeliveryOwnerRequest) (
		domain.BatchDeliveryWorkspace, application.BatchDeliveryAuthority, error)
	Merge(context.Context, application.MergeBatchDeliveryRequest) (
		application.MergeBatchDeliveryResult, error)
	Cancel(context.Context, application.CancelBatchDeliveryRequest) (
		application.CancelBatchDeliveryResult, error)
	Reconcile(context.Context, string) (application.BatchDeliveryReconcileResult, error)
}

type BatchDeliveryPlanView struct {
	ID           string                   `json:"id"`
	RunID        string                   `json:"run_id"`
	ProposalID   string                   `json:"proposal_id"`
	RootAgentID  string                   `json:"root_agent_id"`
	WorkspaceID  string                   `json:"workspace_id"`
	Status       string                   `json:"status"`
	Spec         domain.BatchDeliverySpec `json:"spec"`
	BaseCommit   string                   `json:"base_commit"`
	SourceBranch string                   `json:"source_branch"`
	CreatedBy    string                   `json:"created_by"`
	CreatedAt    time.Time                `json:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
}

type BatchDeliveryWorkspaceView struct {
	PlanID          string                          `json:"plan_id"`
	Ordinal         int                             `json:"ordinal"`
	AgentID         string                          `json:"agent_id"`
	Generation      int64                           `json:"generation"`
	Status          string                          `json:"status"`
	Branch          string                          `json:"branch"`
	BaseCommit      string                          `json:"base_commit"`
	HeadCommit      string                          `json:"head_commit,omitempty"`
	ToolProfile     domain.BatchDeliveryToolProfile `json:"tool_profile"`
	LeaseExpiresAt  time.Time                       `json:"lease_expires_at"`
	LastHeartbeatAt time.Time                       `json:"last_heartbeat_at"`
	CreatedAt       time.Time                       `json:"created_at"`
	UpdatedAt       time.Time                       `json:"updated_at"`
}

type BatchDeliveryMailboxView struct {
	ID           string    `json:"id"`
	Ordinal      int       `json:"ordinal"`
	Generation   int64     `json:"generation"`
	Sequence     int64     `json:"sequence"`
	Kind         string    `json:"kind"`
	Actor        string    `json:"actor"`
	Summary      string    `json:"summary"`
	EvidenceRefs []string  `json:"evidence_refs"`
	CreatedAt    time.Time `json:"created_at"`
}

type BatchDeliveryReceiptView struct {
	ID              string                            `json:"id"`
	Ordinal         int                               `json:"ordinal"`
	Generation      int64                             `json:"generation"`
	ProtocolVersion string                            `json:"protocol_version"`
	BaseCommit      string                            `json:"base_commit"`
	HeadCommit      string                            `json:"head_commit"`
	DiffSHA256      string                            `json:"diff_sha256"`
	CallChainSHA256 string                            `json:"call_chain_sha256"`
	DiffBytes       int64                             `json:"diff_bytes"`
	DiffStat        string                            `json:"diff_stat"`
	ChangedFiles    []string                          `json:"changed_files"`
	TestReceipts    []domain.BatchDeliveryTestReceipt `json:"test_receipts"`
	EvidenceRefs    []string                          `json:"evidence_refs"`
	Limitations     []string                          `json:"limitations"`
	CreatedAt       time.Time                         `json:"created_at"`
}

type BatchDeliveryReviewView struct {
	ID                string    `json:"id"`
	Ordinal           int       `json:"ordinal"`
	Generation        int64     `json:"generation"`
	ProtocolVersion   string    `json:"protocol_version"`
	ReceiptID         string    `json:"receipt_id"`
	Reviewer          string    `json:"reviewer"`
	Verdict           string    `json:"verdict"`
	Summary           string    `json:"summary"`
	BaseCommit        string    `json:"base_commit"`
	HeadCommit        string    `json:"head_commit"`
	DiffSHA256        string    `json:"diff_sha256"`
	CallChainSHA256   string    `json:"call_chain_sha256"`
	FullDiffReviewed  bool      `json:"full_diff_reviewed"`
	CallChainReviewed bool      `json:"call_chain_reviewed"`
	TestsReviewed     bool      `json:"tests_reviewed"`
	CreatedAt         time.Time `json:"created_at"`
}

type BatchDeliveryChildView struct {
	Workspace BatchDeliveryWorkspaceView `json:"workspace"`
	Mailbox   []BatchDeliveryMailboxView `json:"mailbox"`
	Receipt   *BatchDeliveryReceiptView  `json:"receipt,omitempty"`
	Review    *BatchDeliveryReviewView   `json:"review,omitempty"`
}

type BatchDeliveryMergeQueueView struct {
	ID                string    `json:"id"`
	PlanID            string    `json:"plan_id"`
	ProtocolVersion   string    `json:"protocol_version"`
	Status            string    `json:"status"`
	BaseCommit        string    `json:"base_commit"`
	LatestBaseCommit  string    `json:"latest_base_commit"`
	IntegrationBranch string    `json:"integration_branch"`
	IntegrationHead   string    `json:"integration_head,omitempty"`
	OrderedOrdinals   []int     `json:"ordered_ordinals"`
	NextIndex         int       `json:"next_index"`
	FailureCode       string    `json:"failure_code,omitempty"`
	FailureSummary    string    `json:"failure_summary,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type BatchDeliveryMergeStepView struct {
	StepIndex     int        `json:"step_index"`
	Ordinal       int        `json:"ordinal"`
	InputHead     string     `json:"input_head"`
	PreMergeHead  string     `json:"pre_merge_head"`
	PostMergeHead string     `json:"post_merge_head,omitempty"`
	Status        string     `json:"status"`
	FailureCode   string     `json:"failure_code,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type BatchDeliverySnapshotView struct {
	ProtocolVersion string                       `json:"protocol_version"`
	Plan            BatchDeliveryPlanView        `json:"plan"`
	Children        []BatchDeliveryChildView     `json:"children"`
	MergeQueue      *BatchDeliveryMergeQueueView `json:"merge_queue,omitempty"`
	MergeSteps      []BatchDeliveryMergeStepView `json:"merge_steps"`
}

type BatchDeliveriesListView struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Items           []BatchDeliveryPlanView `json:"items"`
}

type BatchDeliveryAuthorityView struct {
	Ordinal        int                             `json:"ordinal"`
	AgentID        string                          `json:"agent_id"`
	Generation     int64                           `json:"generation"`
	OwnerToken     string                          `json:"owner_token"`
	Branch         string                          `json:"branch"`
	LeaseExpiresAt time.Time                       `json:"lease_expires_at"`
	ToolProfile    domain.BatchDeliveryToolProfile `json:"tool_profile"`
}

type BatchDeliveryPrepareRequestView struct {
	Version    string                   `json:"version"`
	ProposalID string                   `json:"proposal_id"`
	Spec       domain.BatchDeliverySpec `json:"spec"`
	Confirm    bool                     `json:"confirm"`
}

type BatchDeliveryPrepareView struct {
	Snapshot    BatchDeliverySnapshotView    `json:"snapshot"`
	Authorities []BatchDeliveryAuthorityView `json:"authorities"`
	Replayed    bool                         `json:"replayed"`
}

type BatchDeliveryReviewRequestView struct {
	Version           string `json:"version"`
	Generation        int64  `json:"generation"`
	Reviewer          string `json:"reviewer"`
	Verdict           string `json:"verdict"`
	Summary           string `json:"summary"`
	FullDiffReviewed  bool   `json:"full_diff_reviewed"`
	CallChainReviewed bool   `json:"call_chain_reviewed"`
	TestsReviewed     bool   `json:"tests_reviewed"`
}

type BatchDeliveryReviewControlView struct {
	Review   BatchDeliveryReviewView `json:"review"`
	Replayed bool                    `json:"replayed"`
}

type BatchDeliveryRenewRequestView struct {
	Version            string `json:"version"`
	ExpectedGeneration int64  `json:"expected_generation"`
	Retry              bool   `json:"retry"`
	Confirm            bool   `json:"confirm"`
}

type BatchDeliveryRenewView struct {
	Workspace BatchDeliveryWorkspaceView `json:"workspace"`
	Authority BatchDeliveryAuthorityView `json:"authority"`
}

type BatchDeliveryMergeRequestView struct {
	Version         string `json:"version"`
	OrderedOrdinals []int  `json:"ordered_ordinals"`
	ConfirmReplay   bool   `json:"confirm_replay"`
	Confirm         bool   `json:"confirm"`
}

type BatchDeliveryMergeControlView struct {
	Queue       BatchDeliveryMergeQueueView  `json:"queue"`
	Steps       []BatchDeliveryMergeStepView `json:"steps"`
	BaseDrifted bool                         `json:"base_drifted"`
	Replayed    bool                         `json:"replayed"`
}

type BatchDeliveryCancelRequestView struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
	Confirm bool   `json:"confirm"`
}

type BatchDeliveryCancelView struct {
	Snapshot             BatchDeliverySnapshotView `json:"snapshot"`
	PreservedOrdinals    []int                     `json:"preserved_ordinals"`
	IntegrationPreserved bool                      `json:"integration_preserved"`
	Replayed             bool                      `json:"replayed"`
}

type BatchDeliveryReconcileRequestView struct {
	Version string `json:"version"`
	Confirm bool   `json:"confirm"`
}

type BatchDeliveryReconcileView struct {
	ProtocolVersion        string `json:"protocol_version"`
	PlanID                 string `json:"plan_id"`
	MaterializedWorktrees  int    `json:"materialized_worktrees"`
	RecoveredWorktrees     int    `json:"recovered_worktrees"`
	Expired                bool   `json:"expired"`
	MergeResumed           bool   `json:"merge_resumed"`
	MergeCompleted         bool   `json:"merge_completed"`
	NeedsOperatorAttention bool   `json:"needs_operator_attention"`
}

func (a *API) listBatchDeliveries(request *http.Request, runID string) (any, *Page, error) {
	if a.batchDeliveryController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found")
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	if err := validateSingleQueryValues(request.URL.Query(), "limit"); err != nil {
		return nil, nil, err
	}
	limit := 50
	if values, ok := request.URL.Query()["limit"]; ok {
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > 64 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery limit must be between 1 and 64")
		}
		limit = parsed
	}
	plans, err := a.batchDeliveryController.List(request.Context(), runID, limit)
	if err != nil {
		return nil, nil, err
	}
	items := make([]BatchDeliveryPlanView, len(plans))
	for index, plan := range plans {
		items[index] = batchDeliveryPlanView(plan)
	}
	return BatchDeliveriesListView{ProtocolVersion: BatchDeliveriesListVersion,
		Items: items}, nil, nil
}

func (a *API) getBatchDelivery(request *http.Request, runID, planID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	snapshot, err := a.batchDeliverySnapshotForRun(request.Context(), runID, planID)
	if err != nil {
		return nil, nil, err
	}
	return batchDeliverySnapshotView(snapshot), nil, nil
}

func matchBatchDeliveryMutationPath(requestPath string) (runID, planID, action string,
	ordinal int, matched bool,
) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] == "batch-deliveries" {
		return parts[0], "", "prepare", 0, true
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "batch-deliveries" && parts[2] == "prepare-workbench" {
		return parts[0], "", "prepare-workbench", 0, true
	}
	if len(parts) == 4 && parts[0] != "" && parts[1] == "batch-deliveries" &&
		parts[2] != "" && (parts[3] == "merge" || parts[3] == "cancel" ||
		parts[3] == "reconcile") {
		return parts[0], parts[2], parts[3], 0, true
	}
	if len(parts) == 6 && parts[0] != "" && parts[1] == "batch-deliveries" &&
		parts[2] != "" && parts[3] == "children" &&
		(parts[5] == "review" || parts[5] == "renew-owner" || parts[5] == "workbench-owner" || parts[5] == "workbench-execute") {
		parsed, err := strconv.Atoi(parts[4])
		if err != nil || parsed < 1 || parsed > domain.MaxBatchDeliveryTasks {
			return "", "", "", 0, false
		}
		return parts[0], parts[2], parts[5], parsed, true
	}
	return "", "", "", 0, false
}

func (a *API) serveBatchDeliveryControl(writer http.ResponseWriter, request *http.Request,
	requestID, runID, planID, action string, ordinal int,
) {
	if !a.authorizeRunOperation(writer, request, requestID,
		a.batchDeliveryControlEnabled, "Batch delivery "+action) {
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	if action != "prepare" && action != "prepare-workbench" {
		if _, err := a.batchDeliverySnapshotForRun(request.Context(), runID, planID); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
	}
	if action == "prepare-workbench" || action == "workbench-owner" || action == "workbench-execute" {
		a.serveBatchWorkbenchControl(writer, request, requestID, runID, planID, action, ordinal)
		return
	}
	if action == "renew-owner" || action == "reconcile" {
		a.serveNonIdempotentBatchDeliveryControl(writer, request, requestID,
			runID, planID, action, ordinal)
		return
	}
	operationKey, body, err := a.readRunOperationRequest(request, "Batch delivery "+action)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	switch action {
	case "prepare":
		var view BatchDeliveryPrepareRequestView
		if err := decodeStrictRunOperation(body, &view, "Batch delivery preparation"); err != nil ||
			view.Version != "batch_delivery_prepare.v1" || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery preparation requires batch_delivery_prepare.v1 and confirmation"), 0)
			return
		}
		result, err := a.batchDeliveryController.Prepare(request.Context(),
			application.PrepareBatchDeliveryRequest{RunID: runID, ProposalID: view.ProposalID,
				Spec: view.Spec, OperationKey: operationKey,
				RequestedBy: "http_batch_delivery_operator", Confirm: true})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		snapshot, err := a.batchDeliveryController.Snapshot(request.Context(), result.Plan.ID)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		authorities := make([]BatchDeliveryAuthorityView, len(result.Authorities))
		for index, authority := range result.Authorities {
			authorities[index] = batchDeliveryAuthorityView(authority)
		}
		a.writeSuccessStatus(writer, requestID, BatchDeliveryPrepareView{
			Snapshot: batchDeliverySnapshotView(snapshot), Authorities: authorities,
			Replayed: result.Replayed}, nil, http.StatusCreated)
	case "review":
		var view BatchDeliveryReviewRequestView
		if err := decodeStrictRunOperation(body, &view, "Batch delivery review"); err != nil ||
			view.Version != "batch_delivery_review_control.v1" {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery review version is invalid"), 0)
			return
		}
		review, replayed, err := a.batchDeliveryController.Review(request.Context(),
			application.ReviewBatchDeliveryRequest{PlanID: planID, Ordinal: ordinal,
				Generation: view.Generation, Reviewer: view.Reviewer,
				Verdict: domain.BatchDeliveryReviewVerdict(view.Verdict), Summary: view.Summary,
				FullDiffReviewed:  view.FullDiffReviewed,
				CallChainReviewed: view.CallChainReviewed, TestsReviewed: view.TestsReviewed,
				OperationKey: operationKey})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, BatchDeliveryReviewControlView{
			Review: batchDeliveryReviewView(review), Replayed: replayed}, nil, http.StatusOK)
	case "merge":
		var view BatchDeliveryMergeRequestView
		if err := decodeStrictRunOperation(body, &view, "Batch delivery merge"); err != nil ||
			view.Version != "batch_delivery_merge.v1" || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery merge requires batch_delivery_merge.v1 and confirmation"), 0)
			return
		}
		result, err := a.batchDeliveryController.Merge(request.Context(),
			application.MergeBatchDeliveryRequest{PlanID: planID,
				OrderedOrdinals: view.OrderedOrdinals, ConfirmReplay: view.ConfirmReplay,
				OperationKey: operationKey, RequestedBy: "http_batch_delivery_operator",
				Confirm: true})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, batchDeliveryMergeControlView(result),
			nil, http.StatusOK)
	case "cancel":
		var view BatchDeliveryCancelRequestView
		if err := decodeStrictRunOperation(body, &view, "Batch delivery cancellation"); err != nil ||
			view.Version != "batch_delivery_cancel.v1" || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery cancellation requires batch_delivery_cancel.v1 and confirmation"), 0)
			return
		}
		result, err := a.batchDeliveryController.Cancel(request.Context(),
			application.CancelBatchDeliveryRequest{PlanID: planID, OperationKey: operationKey,
				RequestedBy: "http_batch_delivery_operator", Reason: view.Reason, Confirm: true})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, BatchDeliveryCancelView{
			Snapshot:             batchDeliverySnapshotView(result.Snapshot),
			PreservedOrdinals:    result.PreservedOrdinals,
			IntegrationPreserved: result.IntegrationPreserved, Replayed: result.Replayed},
			nil, http.StatusOK)
	default:
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"), 0)
	}
}

func (a *API) serveNonIdempotentBatchDeliveryControl(writer http.ResponseWriter,
	request *http.Request, requestID, runID, planID, action string, ordinal int,
) {
	body, err := readStrictControlBody(request, "Batch delivery "+action)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if action == "renew-owner" {
		var view BatchDeliveryRenewRequestView
		if err := decodeStrictRunOperation(body, &view, "Batch delivery owner renewal"); err != nil ||
			view.Version != "batch_delivery_renew_owner.v1" || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"batch delivery owner renewal requires its version and confirmation"), 0)
			return
		}
		workspace, authority, err := a.batchDeliveryController.RenewOwner(request.Context(),
			application.RenewBatchDeliveryOwnerRequest{PlanID: planID, Ordinal: ordinal,
				ExpectedGeneration: view.ExpectedGeneration, Retry: view.Retry,
				RequestedBy: "http_batch_delivery_operator", Confirm: true})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, BatchDeliveryRenewView{
			Workspace: batchDeliveryWorkspaceView(workspace),
			Authority: batchDeliveryAuthorityView(authority)}, nil, http.StatusOK)
		return
	}
	var view BatchDeliveryReconcileRequestView
	if err := decodeStrictRunOperation(body, &view, "Batch delivery reconciliation"); err != nil ||
		view.Version != "batch_delivery_reconcile.v1" || !view.Confirm {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery reconciliation requires its version and confirmation"), 0)
		return
	}
	result, err := a.batchDeliveryController.Reconcile(request.Context(), planID)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, batchDeliveryReconcileView(result), nil, http.StatusOK)
}

func (a *API) batchDeliverySnapshotForRun(ctx context.Context, runID, planID string) (
	application.BatchDeliverySnapshot, error,
) {
	if a.batchDeliveryController == nil {
		return application.BatchDeliverySnapshot{},
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found")
	}
	snapshot, err := a.batchDeliveryController.Snapshot(ctx, planID)
	if err != nil {
		return application.BatchDeliverySnapshot{}, err
	}
	if snapshot.Plan.RunID != runID {
		return application.BatchDeliverySnapshot{},
			apperror.New(apperror.CodeNotFound, "batch delivery is not part of the Run")
	}
	return snapshot, nil
}

func batchDeliverySnapshotView(snapshot application.BatchDeliverySnapshot) BatchDeliverySnapshotView {
	view := BatchDeliverySnapshotView{ProtocolVersion: domain.BatchDeliveryProtocolVersion,
		Plan:       batchDeliveryPlanView(snapshot.Plan),
		Children:   make([]BatchDeliveryChildView, len(snapshot.Workspaces)),
		MergeSteps: make([]BatchDeliveryMergeStepView, len(snapshot.MergeSteps))}
	receipts := make(map[int]domain.BatchDeliveryReceipt, len(snapshot.Receipts))
	reviews := make(map[int]domain.BatchDeliveryReview, len(snapshot.Reviews))
	for _, receipt := range snapshot.Receipts {
		receipts[receipt.Ordinal] = receipt
	}
	for _, review := range snapshot.Reviews {
		reviews[review.Ordinal] = review
	}
	for index, workspace := range snapshot.Workspaces {
		child := BatchDeliveryChildView{Workspace: batchDeliveryWorkspaceView(workspace),
			Mailbox: make([]BatchDeliveryMailboxView, len(snapshot.Mailbox[workspace.Ordinal]))}
		for messageIndex, message := range snapshot.Mailbox[workspace.Ordinal] {
			child.Mailbox[messageIndex] = batchDeliveryMailboxView(message)
		}
		if receipt, ok := receipts[workspace.Ordinal]; ok {
			projected := batchDeliveryReceiptView(receipt)
			child.Receipt = &projected
		}
		if review, ok := reviews[workspace.Ordinal]; ok {
			projected := batchDeliveryReviewView(review)
			child.Review = &projected
		}
		view.Children[index] = child
	}
	if snapshot.MergeQueue != nil {
		projected := batchDeliveryMergeQueueView(*snapshot.MergeQueue)
		view.MergeQueue = &projected
	}
	for index, step := range snapshot.MergeSteps {
		view.MergeSteps[index] = batchDeliveryMergeStepView(step)
	}
	return view
}

func batchDeliveryPlanView(value domain.BatchDeliveryPlan) BatchDeliveryPlanView {
	return BatchDeliveryPlanView{ID: value.ID, RunID: value.RunID,
		ProposalID: value.ProposalID, RootAgentID: value.RootAgentID,
		WorkspaceID: value.WorkspaceID, Status: string(value.Status), Spec: value.Spec,
		BaseCommit: value.BaseCommit, SourceBranch: value.SourceBranch,
		CreatedBy: value.CreatedBy, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func batchDeliveryWorkspaceView(value domain.BatchDeliveryWorkspace) BatchDeliveryWorkspaceView {
	return BatchDeliveryWorkspaceView{PlanID: value.PlanID, Ordinal: value.Ordinal,
		AgentID: value.AgentID, Generation: value.Generation, Status: string(value.Status),
		Branch: value.Branch, BaseCommit: value.BaseCommit, HeadCommit: value.HeadCommit,
		ToolProfile: value.ToolProfile, LeaseExpiresAt: value.LeaseExpiresAt,
		LastHeartbeatAt: value.LastHeartbeatAt, CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt}
}

func batchDeliveryMailboxView(value domain.BatchDeliveryMailboxMessage) BatchDeliveryMailboxView {
	return BatchDeliveryMailboxView{ID: value.ID, Ordinal: value.Ordinal,
		Generation: value.Generation, Sequence: value.Sequence, Kind: string(value.Kind),
		Actor: value.Actor, Summary: value.Summary,
		EvidenceRefs: append([]string{}, value.EvidenceRefs...), CreatedAt: value.CreatedAt}
}

func batchDeliveryReceiptView(value domain.BatchDeliveryReceipt) BatchDeliveryReceiptView {
	return BatchDeliveryReceiptView{ID: value.ID, Ordinal: value.Ordinal,
		Generation: value.Generation, ProtocolVersion: value.ProtocolVersion,
		BaseCommit: value.BaseCommit, HeadCommit: value.HeadCommit,
		DiffSHA256: value.DiffSHA256, CallChainSHA256: value.CallChainSHA256,
		DiffBytes: value.DiffBytes, DiffStat: value.DiffStat,
		ChangedFiles: append([]string(nil), value.ChangedFiles...),
		TestReceipts: append([]domain.BatchDeliveryTestReceipt(nil), value.TestReceipts...),
		EvidenceRefs: append([]string(nil), value.EvidenceRefs...),
		Limitations:  append([]string(nil), value.Limitations...), CreatedAt: value.CreatedAt}
}

func batchDeliveryReviewView(value domain.BatchDeliveryReview) BatchDeliveryReviewView {
	return BatchDeliveryReviewView{ID: value.ID, Ordinal: value.Ordinal,
		Generation: value.Generation, ProtocolVersion: value.ProtocolVersion,
		ReceiptID: value.ReceiptID, Reviewer: value.Reviewer, Verdict: string(value.Verdict),
		Summary: value.Summary, BaseCommit: value.BaseCommit, HeadCommit: value.HeadCommit,
		DiffSHA256: value.DiffSHA256, CallChainSHA256: value.CallChainSHA256,
		FullDiffReviewed: value.FullDiffReviewed, CallChainReviewed: value.CallChainReviewed,
		TestsReviewed: value.TestsReviewed, CreatedAt: value.CreatedAt}
}

func batchDeliveryAuthorityView(value application.BatchDeliveryAuthority) BatchDeliveryAuthorityView {
	return BatchDeliveryAuthorityView{Ordinal: value.Ordinal, AgentID: value.AgentID,
		Generation: value.Generation, OwnerToken: value.OwnerToken, Branch: value.Branch,
		LeaseExpiresAt: value.LeaseExpiresAt, ToolProfile: value.ToolProfile}
}

func batchDeliveryMergeQueueView(value domain.BatchDeliveryMergeQueue) BatchDeliveryMergeQueueView {
	return BatchDeliveryMergeQueueView{ID: value.ID, PlanID: value.PlanID,
		ProtocolVersion: value.ProtocolVersion, Status: string(value.Status),
		BaseCommit: value.BaseCommit, LatestBaseCommit: value.LatestBaseCommit,
		IntegrationBranch: value.IntegrationBranch, IntegrationHead: value.IntegrationHead,
		OrderedOrdinals: append([]int(nil), value.OrderedOrdinals...), NextIndex: value.NextIndex,
		FailureCode: value.FailureCode, FailureSummary: value.FailureSummary,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func batchDeliveryMergeStepView(value domain.BatchDeliveryMergeStep) BatchDeliveryMergeStepView {
	return BatchDeliveryMergeStepView{StepIndex: value.StepIndex, Ordinal: value.Ordinal,
		InputHead: value.InputHead, PreMergeHead: value.PreMergeHead,
		PostMergeHead: value.PostMergeHead, Status: string(value.Status),
		FailureCode: value.FailureCode, CreatedAt: value.CreatedAt,
		CompletedAt: value.CompletedAt}
}

func batchDeliveryMergeControlView(value application.MergeBatchDeliveryResult) BatchDeliveryMergeControlView {
	steps := make([]BatchDeliveryMergeStepView, len(value.Steps))
	for index, step := range value.Steps {
		steps[index] = batchDeliveryMergeStepView(step)
	}
	return BatchDeliveryMergeControlView{Queue: batchDeliveryMergeQueueView(value.Queue),
		Steps: steps, BaseDrifted: value.BaseDrifted, Replayed: value.Replayed}
}

func batchDeliveryReconcileView(value application.BatchDeliveryReconcileResult) BatchDeliveryReconcileView {
	return BatchDeliveryReconcileView{ProtocolVersion: domain.BatchDeliveryProtocolVersion,
		PlanID: value.PlanID, MaterializedWorktrees: value.MaterializedWorktrees,
		RecoveredWorktrees: value.RecoveredWorktrees, Expired: value.Expired,
		MergeResumed: value.MergeResumed, MergeCompleted: value.MergeCompleted,
		NeedsOperatorAttention: value.NeedsOperatorAttention}
}
