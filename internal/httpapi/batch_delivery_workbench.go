package httpapi

import (
	"context"
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const (
	BatchWorkbenchPreparePathTemplate = "/api/v1/runs/{run_id}/batch-deliveries/prepare-workbench"
	BatchWorkbenchPathTemplate        = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/workbench"
	BatchWorkbenchOwnerPathTemplate   = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/children/{ordinal}/workbench-owner"
	BatchWorkbenchExecutePathTemplate = "/api/v1/runs/{run_id}/batch-deliveries/{batch_delivery_id}/children/{ordinal}/workbench-execute"
)

type BatchDeliveryWorkbenchController interface {
	PrepareWorkbench(context.Context, application.PrepareBatchDeliveryWorkbenchRequest) (application.BatchDeliveryWorkbenchSnapshot, error)
	WorkbenchSnapshot(context.Context, string) (application.BatchDeliveryWorkbenchSnapshot, error)
	RenewWorkbenchOwner(context.Context, application.BatchDeliveryWorkbenchOwnerRequest) (application.BatchDeliveryWorkbenchSnapshot, error)
	ExecuteWorkbench(context.Context, application.ExecuteBatchDeliveryWorkbenchRequest) (application.BatchDeliveryWorkbenchSnapshot, error)
}

type BatchWorkbenchTaskInputView struct {
	Ordinal        int                                         `json:"ordinal"`
	OwnershipHints []domain.BatchDeliveryOwnershipHint         `json:"ownership_hints"`
	Validations    []domain.BatchDeliveryValidationRequirement `json:"validations"`
}

type BatchWorkbenchPrepareRequestView struct {
	Version    string                        `json:"version"`
	ProposalID string                        `json:"proposal_id"`
	Tasks      []BatchWorkbenchTaskInputView `json:"tasks"`
	Confirm    bool                          `json:"confirm"`
}

type BatchWorkbenchChildView struct {
	Ordinal           int64 `json:"ordinal"`
	Generation        int64 `json:"generation"`
	OwnerAvailable    bool  `json:"owner_available"`
	Executing         bool  `json:"executing"`
	OutcomeUnresolved bool  `json:"outcome_unresolved"`
	WaitingApproval   bool  `json:"waiting_approval,omitempty"`
}

type BatchWorkbenchView struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Snapshot        BatchDeliverySnapshotView `json:"snapshot"`
	WorkerAvailable bool                      `json:"worker_available"`
	Children        []BatchWorkbenchChildView `json:"children"`
	Replayed        bool                      `json:"replayed"`
}

type BatchWorkbenchOwnerRequestView struct {
	Version            string `json:"version"`
	ExpectedGeneration int64  `json:"expected_generation"`
	Retry              bool   `json:"retry"`
	Confirm            bool   `json:"confirm"`
}

type BatchWorkbenchExecuteRequestView struct {
	Version            string `json:"version"`
	ExpectedGeneration int64  `json:"expected_generation"`
	Confirm            bool   `json:"confirm"`
}

func batchWorkbenchView(result application.BatchDeliveryWorkbenchSnapshot) BatchWorkbenchView {
	view := BatchWorkbenchView{ProtocolVersion: application.BatchDeliveryWorkbenchVersion,
		Snapshot: batchDeliverySnapshotView(result.Snapshot), WorkerAvailable: result.WorkerAvailable,
		Children: make([]BatchWorkbenchChildView, len(result.Children)), Replayed: result.Replayed}
	for index, child := range result.Children {
		view.Children[index] = BatchWorkbenchChildView{Ordinal: child.Ordinal, Generation: child.Generation,
			OwnerAvailable: child.OwnerAvailable, Executing: child.Executing, OutcomeUnresolved: child.OutcomeUnresolved, WaitingApproval: child.WaitingApproval}
	}
	return view
}

func (a *API) getBatchWorkbench(request *http.Request, runID, planID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if _, err := a.batchDeliverySnapshotForRun(request.Context(), runID, planID); err != nil {
		return nil, nil, err
	}
	controller, ok := a.batchDeliveryController.(BatchDeliveryWorkbenchController)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeNotFound, "batch workbench is unavailable")
	}
	result, err := controller.WorkbenchSnapshot(request.Context(), planID)
	if err != nil {
		return nil, nil, err
	}
	return batchWorkbenchView(result), nil, nil
}

func (a *API) serveBatchWorkbenchControl(writer http.ResponseWriter, request *http.Request,
	requestID, runID, planID, action string, ordinal int) {
	controller, ok := a.batchDeliveryController.(BatchDeliveryWorkbenchController)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "batch workbench is unavailable"), 0)
		return
	}
	operationKey, body, err := a.readRunOperationRequest(request, "Batch workbench "+action)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var result application.BatchDeliveryWorkbenchSnapshot
	status := http.StatusOK
	switch action {
	case "prepare-workbench":
		var view BatchWorkbenchPrepareRequestView
		if decodeStrictRunOperation(body, &view, "Batch workbench preparation") != nil ||
			view.Version != application.BatchDeliveryWorkbenchVersion || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "batch workbench preparation requires its version and confirmation"), 0)
			return
		}
		tasks := make([]application.BatchDeliveryWorkbenchTask, len(view.Tasks))
		for index, task := range view.Tasks {
			tasks[index] = application.BatchDeliveryWorkbenchTask{
				Ordinal: task.Ordinal, OwnershipHints: task.OwnershipHints, Validations: task.Validations}
		}
		result, err = controller.PrepareWorkbench(request.Context(), application.PrepareBatchDeliveryWorkbenchRequest{
			RunID: runID, ProposalID: view.ProposalID, Tasks: tasks, OperationKey: operationKey,
			RequestedBy: "http_batch_workbench_operator", Confirm: true})
		status = http.StatusCreated
	case "workbench-owner":
		var view BatchWorkbenchOwnerRequestView
		if decodeStrictRunOperation(body, &view, "Batch workbench owner recovery") != nil ||
			view.Version != application.BatchDeliveryWorkbenchVersion || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "batch workbench owner recovery requires its version and confirmation"), 0)
			return
		}
		result, err = controller.RenewWorkbenchOwner(request.Context(), application.BatchDeliveryWorkbenchOwnerRequest{
			PlanID: planID, Ordinal: ordinal, ExpectedGeneration: view.ExpectedGeneration, Retry: view.Retry,
			OperationKey: operationKey, RequestedBy: "http_batch_workbench_operator", Confirm: true})
	case "workbench-execute":
		var view BatchWorkbenchExecuteRequestView
		if decodeStrictRunOperation(body, &view, "Batch workbench child execution") != nil ||
			view.Version != application.BatchDeliveryWorkbenchVersion || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "batch workbench child execution requires its version and confirmation"), 0)
			return
		}
		result, err = controller.ExecuteWorkbench(request.Context(), application.ExecuteBatchDeliveryWorkbenchRequest{
			PlanID: planID, Ordinal: ordinal, ExpectedGeneration: view.ExpectedGeneration, OperationKey: operationKey, Confirm: true})
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, batchWorkbenchView(result), nil, status)
}
