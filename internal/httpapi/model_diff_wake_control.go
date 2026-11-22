package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/redact"
)

type ModelControlController interface {
	SelectRoute(context.Context, application.SelectModelRouteRequest) (
		modelregistry.RouteAvailability, error)
	Diagnose(context.Context, application.DiagnoseProviderRequest) (
		modelregistry.DiagnosticResult, error)
	QualifyHarness(context.Context, application.QualifyModelHarnessRequest) (
		modelregistry.HarnessQualificationResult, error)
}

type FileEditReviewController interface {
	Review(context.Context, application.ReviewFileEditRequest) (
		application.ReviewFileEditResult, error)
}

type FileEditApplyController interface {
	Apply(context.Context, application.ApplyFileEditRequest) (
		application.ApplyFileEditResult, error)
}

type RunWakeExecutionController interface {
	Consume(context.Context, application.ConsumeRunWakeRequest) (
		application.ConsumeRunWakeResult, error)
}

type RunWakeController interface {
	Schedule(context.Context, application.ScheduleRunWakeRequest) (
		application.RunWakeControlResult, error)
	Cancel(context.Context, application.CancelRunWakeRequest) (
		application.RunWakeControlResult, error)
	Get(context.Context, string) (domain.RunWakeIntent, bool, error)
}

const (
	ModelRouteControlPathTemplate   = "/api/v1/models/routes/{route}"
	ProviderDiagnosticPath          = "/api/v1/models/diagnostics"
	ModelHarnessQualificationPath   = "/api/v1/models/harness-qualifications"
	FileEditQueuePathTemplate       = "/api/v1/runs/{run_id}/file-edits"
	FileEditChangeSetPathTemplate   = "/api/v1/runs/{run_id}/file-edit-change-set"
	FileEditReviewPathTemplate      = "/api/v1/runs/{run_id}/file-edits/{edit_id}/review"
	FileEditApplyPathTemplate       = "/api/v1/runs/{run_id}/file-edits/{edit_id}/apply"
	RunWakeIntentPathTemplate       = "/api/v1/runs/{run_id}/wake-intent"
	RunWakeCancellationPathTemplate = "/api/v1/runs/{run_id}/wake-intent/cancel"
	RunWakeExecutionPathTemplate    = "/api/v1/runs/{run_id}/wake-intent/consume"
)

type ModelRouteControlRequestView struct {
	Version  string `json:"version"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ProviderDiagnosticRequestView struct {
	Version           string `json:"version"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	ConfirmDiagnostic bool   `json:"confirm_diagnostic"`
}

type ModelHarnessQualificationRequestView struct {
	Version              string `json:"version"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	ConfirmQualification bool   `json:"confirm_qualification"`
}

type FileEditReviewRequestView struct {
	Version string                           `json:"version"`
	Action  application.FileEditReviewAction `json:"action"`
}

type FileEditApplyRequestView struct {
	Version string `json:"version"`
}

type RunWakeScheduleRequestView struct {
	Version             string `json:"version"`
	MaxAttempts         int    `json:"max_attempts"`
	InitialDelaySeconds int    `json:"initial_delay_seconds"`
	BaseBackoffSeconds  int    `json:"base_backoff_seconds"`
	MaxBackoffSeconds   int    `json:"max_backoff_seconds"`
	MaxElapsedSeconds   int    `json:"max_elapsed_seconds"`
}

type RunWakeCancelRequestView struct {
	Version string `json:"version"`
}

type RunWakeExecutionRequestView struct {
	Version  string `json:"version"`
	MaxSteps int    `json:"max_steps"`
}

func matchModelControlPath(requestPath string) (string, string, bool) {
	if requestPath == "/api/v1/models/diagnostics" {
		return "", "diagnostic", true
	}
	if requestPath == ModelHarnessQualificationPath {
		return "", "harness_qualification", true
	}
	const prefix = "/api/v1/models/routes/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", false
	}
	route := strings.TrimPrefix(requestPath, prefix)
	return route, "route", route != "" && !strings.Contains(route, "/")
}

func matchFileEditReviewControlPath(requestPath string) (string, string, bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", false
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(segments) != 4 || segments[0] == "" || segments[1] != "file-edits" ||
		segments[2] == "" || segments[3] != "review" {
		return "", "", false
	}
	return segments[0], segments[2], true
}

func matchFileEditApplyControlPath(requestPath string) (string, string, bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", false
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(segments) != 4 || segments[0] == "" || segments[1] != "file-edits" ||
		segments[2] == "" || segments[3] != "apply" {
		return "", "", false
	}
	return segments[0], segments[2], true
}

func matchRunWakeControlPath(requestPath string) (string, bool, bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", false, false
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(segments) == 2 && segments[0] != "" && segments[1] == "wake-intent" {
		return segments[0], false, true
	}
	if len(segments) == 3 && segments[0] != "" && segments[1] == "wake-intent" &&
		segments[2] == "cancel" {
		return segments[0], true, true
	}
	return "", false, false
}

func matchRunWakeExecutionPath(requestPath string) (string, bool) {
	const prefix = "/api/v1/runs/"
	const suffix = "/wake-intent/consume"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	return runID, runID != "" && !strings.Contains(runID, "/")
}

func (a *API) serveModelControl(writer http.ResponseWriter, request *http.Request,
	requestID string, route string, kind string,
) {
	label := "Model route control"
	if kind == "diagnostic" {
		label = "Provider diagnostic"
	} else if kind == "harness_qualification" {
		label = "Model Harness qualification"
	}
	if !a.authorizeRunOperation(writer, request, requestID,
		a.modelControlEnabled, label) {
		return
	}
	body, err := readStrictControlBody(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if kind == "diagnostic" {
		var view ProviderDiagnosticRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		result, err := a.modelControlController.Diagnose(request.Context(),
			application.DiagnoseProviderRequest{Version: view.Version,
				Provider: view.Provider, Model: view.Model,
				ConfirmDiagnostic: view.ConfirmDiagnostic})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, providerDiagnosticView(result), nil,
			http.StatusAccepted)
		return
	}
	if kind == "harness_qualification" {
		var view ModelHarnessQualificationRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		result, err := a.modelControlController.QualifyHarness(request.Context(),
			application.QualifyModelHarnessRequest{
				Version: view.Version, Provider: view.Provider, Model: view.Model,
				ConfirmQualification: view.ConfirmQualification,
			})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, modelHarnessQualificationView(result),
			nil, http.StatusAccepted)
		return
	}
	if err := validatePathIdentity(route); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view ModelRouteControlRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	selected, err := a.modelControlController.SelectRoute(request.Context(),
		application.SelectModelRouteRequest{Version: view.Version, Route: route,
			Provider: view.Provider, Model: view.Model})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, ModelRouteAvailabilityView{Name: selected.Name,
		Provider: selected.Provider, Model: selected.Model, Available: selected.Available,
		HarnessReady: selected.HarnessReady}, nil,
		http.StatusAccepted)
}

func (a *API) serveFileEditReviewControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string, editID string,
) {
	const label = "File edit review"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.fileEditReviewEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validatePathIdentity(editID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readStrictControlBody(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view FileEditReviewRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := a.fileEditReviewController.Review(request.Context(),
		application.ReviewFileEditRequest{Version: view.Version, RunID: runID,
			EditID: editID, Action: view.Action})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	continuation := a.resumeReviewedProposal(request.Context(), runID, "file_edit", editID)
	edit := result.Edit
	if reader, ok := a.store.(fileEditDetailReader); ok {
		if latest, lookupErr := reader.GetFileEdit(request.Context(), editID); lookupErr == nil {
			edit = latest
		}
	}
	fileWritten := !result.Replayed && result.Action == application.FileEditApproveIntent && edit.Status == fileedit.StatusApplied
	a.writeSuccessStatus(writer, requestID, FileEditReviewView{
		Continuation:    continuation,
		ProtocolVersion: application.FileEditReviewProtocolVersion, RunID: runID,
		Action: string(result.Action), Edit: fileEditView(edit, false),
		Replayed: result.Replayed, FileWritten: fileWritten,
	}, nil, http.StatusAccepted)
}

func (a *API) serveFileEditApplyControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string, editID string,
) {
	const label = "File edit apply"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.fileEditApplyEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validatePathIdentity(editID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, body, err := a.readRunOperationRequest(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view FileEditApplyRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := a.fileEditApplyController.Apply(request.Context(),
		application.ApplyFileEditRequest{Version: view.Version, RunID: runID,
			EditID: editID, OperationKey: operationKey, AppliedBy: "http_operator"})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, FileEditApplyView{
		ProtocolVersion: fileedit.FileEditApplyProtocolVersion, RunID: runID,
		Edit: fileEditView(result.Edit, false), Status: string(result.Result.Status),
		Replayed: result.Replayed, FileWritten: result.FileWritten,
		PolicyRechecked: true,
		Receipt: operationReceiptView(operationreceipt.FileEditApply(
			string(result.Result.Status), result.Replayed,
			result.StagingCleanup.Pending)),
	}, nil, http.StatusAccepted)
}

func (a *API) serveRunWakeControl(writer http.ResponseWriter, request *http.Request,
	requestID string, runID string, cancel bool,
) {
	const label = "Run wake control"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.runWakeControlEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, body, err := a.readRunOperationRequest(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var result application.RunWakeControlResult
	if cancel {
		var view RunWakeCancelRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		result, err = a.runWakeController.Cancel(request.Context(),
			application.CancelRunWakeRequest{Version: view.Version, RunID: runID,
				OperationKey: operationKey, RequestedBy: "http_control"})
	} else {
		var view RunWakeScheduleRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		result, err = a.runWakeController.Schedule(request.Context(),
			application.ScheduleRunWakeRequest{Version: view.Version, RunID: runID,
				OperationKey: operationKey, RequestedBy: "http_control",
				MaxAttempts: view.MaxAttempts, InitialDelaySeconds: view.InitialDelaySeconds,
				BaseBackoffSeconds: view.BaseBackoffSeconds,
				MaxBackoffSeconds:  view.MaxBackoffSeconds,
				MaxElapsedSeconds:  view.MaxElapsedSeconds})
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	action := domain.RunWakeSchedule
	if cancel {
		action = domain.RunWakeCancel
	}
	a.writeSuccessStatus(writer, requestID, RunWakeControlView{
		ProtocolVersion: domain.RunWakeControlProtocolVersion, Action: string(action),
		Intent: *runWakeIntentView(result.Intent, true), Replayed: result.Replayed,
		ExecutionStarted: false, ModelCalled: false, ToolCalled: false,
	}, nil, http.StatusAccepted)
}

func (a *API) serveRunWakeExecutionControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	const label = "Foreground Run wake execution"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.runWakeExecutionEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readStrictControlBody(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view RunWakeExecutionRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := a.runWakeExecutionController.Consume(request.Context(),
		application.ConsumeRunWakeRequest{Version: view.Version, RunID: runID,
			OwnerID: "http_wake_foreground", MaxSteps: view.MaxSteps})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	modelCalled, toolCalled, stopReason := false, false, ""
	if result.Handoff.Result != nil {
		modelCalled = result.Handoff.Result.ModelCalled
		toolCalled = result.Handoff.Result.ToolCalled
		stopReason = result.Handoff.Result.StopReason
	}
	a.writeSuccessStatus(writer, requestID, RunWakeExecutionView{
		ProtocolVersion: domain.RunWakeConsumerProtocolVersion, RunID: runID,
		Intent:            *runWakeIntentView(result.Intent, true),
		ConsumptionStatus: string(result.Consumption.Status),
		StopReason:        stopReason, Replayed: result.Replayed,
		ExecutionStarted: result.Consumption.HandoffOperationID != "" ||
			result.Handoff.Operation.ID != "",
		ModelCalled: modelCalled, ToolCalled: toolCalled,
		BackgroundLoopEnabled: false,
		Receipt: operationReceiptView(operationreceipt.Settled(
			operationreceipt.KindRunWakeConsume, result.Replayed, false)),
	}, nil, http.StatusAccepted)
}

func operationReceiptView(value operationreceipt.Receipt) OperationReceiptView {
	return OperationReceiptView{
		ProtocolVersion: value.ProtocolVersion, Kind: value.Kind, Outcome: value.Outcome,
		Durable: value.Durable, Replayed: value.Replayed, RetrySafe: value.RetrySafe,
		RetryStrategy: value.RetryStrategy, RecoveryAction: value.RecoveryAction,
		CleanupState: value.CleanupState,
	}
}

func readStrictControlBody(request *http.Request, label string) ([]byte, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, err
	}
	if err := validateJSONContentType(request.Header); err != nil {
		return nil, err
	}
	body, err := readBoundedControlBody(request)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		return nil, err
	}
	return body, nil
}

func providerDiagnosticView(result modelregistry.DiagnosticResult) ProviderDiagnosticView {
	return ProviderDiagnosticView{ProtocolVersion: result.ProtocolVersion,
		Provider: result.Provider, Model: result.Model, Status: result.Status,
		Outcome: result.Outcome, FailureReason: string(result.FailureReason),
		Retryable:               result.Retryable,
		NetworkRequestAttempted: result.NetworkRequestAttempted,
		ModelCalled:             result.ModelCalled, ToolCalled: result.ToolCalled,
		ResponseContentReturned: result.ResponseContentReturned,
		QualificationStatus:     result.QualificationStatus,
		DurationMillis:          result.DurationMillis}
}

func modelHarnessAvailabilityView(
	value modelregistry.HarnessAvailability,
) ModelHarnessAvailabilityView {
	return ModelHarnessAvailabilityView{
		ProtocolVersion: value.ProtocolVersion, Model: value.Model,
		TransportProtocol: value.TransportProtocol, ToolStrategy: value.ToolStrategy,
		JSONStrategy: value.JSONStrategy, QualificationStatus: value.QualificationStatus,
		ToolCallsQualified:        value.ToolCallsQualified,
		ToolResultsQualified:      value.ToolResultsQualified,
		StrictJSONQualified:       value.StrictJSONQualified,
		StreamingQualified:        value.StreamingQualified,
		RootEligible:              value.RootEligible,
		StructuredJSONEligible:    value.StructuredJSONEligible,
		LatestQualificationStatus: value.LatestQualificationStatus,
		QualificationCheckedAt:    value.QualificationCheckedAt,
		QualificationSource:       value.QualificationSource,
		QualifiedAt:               value.QualifiedAt, ExpiresAt: value.ExpiresAt,
	}
}

func modelHarnessQualificationView(
	result modelregistry.HarnessQualificationResult,
) ModelHarnessQualificationView {
	return ModelHarnessQualificationView{
		ProtocolVersion: result.ProtocolVersion, Provider: result.Provider,
		Model: result.Model, Status: result.Status, Outcome: result.Outcome,
		FailureReason:           string(result.FailureReason),
		Retryable:               result.Retryable,
		NetworkRequestAttempted: result.NetworkRequestAttempted,
		ModelCalls:              result.ModelCalls, SyntheticToolCalls: result.SyntheticToolCalls,
		ToolExecuted:            result.ToolExecuted,
		ResponseContentReturned: result.ResponseContentReturned,
		QualificationStatus:     result.QualificationStatus,
		DurationMillis:          result.DurationMillis,
		Harness:                 modelHarnessAvailabilityView(result.Harness),
	}
}

func fileEditPreviewView(value fileedit.Preview, terminal bool) FileEditPreviewView {
	actions := []application.FileEditReviewAction{}
	if value.Status == fileedit.StatusProposed && !terminal {
		actions = []application.FileEditReviewAction{
			application.FileEditApproveIntent, application.FileEditDeny,
		}
	}
	return FileEditPreviewView{ID: value.ID, SessionID: value.SessionID,
		WorkspaceID: value.WorkspaceID, Path: value.Path, Operation: value.Operation,
		DestinationPath: value.DestinationPath, Status: value.Status,
		Diff: value.Diff, OriginalHash: value.OriginalHash, ProposedHash: value.ProposedHash,
		DestinationOriginalHash: value.DestinationOriginalHash,
		DestinationProposedHash: value.DestinationProposedHash,
		Reason:                  value.Reason, SecretsRedacted: value.SecretsRedacted,
		AllowedActions: actions, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		ApplyEnabled: false}
}

func fileEditView(value fileedit.Edit, terminal bool) FileEditPreviewView {
	// Delete receipts historically persist only operation/hash/size metadata.
	// A complete verified body can enrich the read-only view without changing
	// the stored Diff or any proposal/apply identity. Never infer missing bytes.
	if value.Operation == fileedit.OperationDelete && !value.SecretsRedacted &&
		value.DestinationPath == "" && value.DestinationOriginalHash == "" &&
		value.DestinationProposedHash == "" && value.ProposedHash == "missing" &&
		value.ProposedText == "" && len(value.OriginalText) <= fileedit.MaxContentBytes &&
		utf8.ValidString(value.OriginalText) && !strings.ContainsRune(value.OriginalText, 0) &&
		redact.String(value.OriginalText) == value.OriginalText &&
		fileedit.HashText(value.OriginalText) == value.OriginalHash {
		value.Diff = fmt.Sprintf("delete %s\nexpected_sha256 %s\nsize_bytes %d\n",
			value.Path, value.OriginalHash, len(value.OriginalText))
		if value.OriginalText != "" {
			value.Diff += fileedit.UnifiedDiff(value.Path, value.OriginalText, "")
		}
	}
	return fileEditPreviewView(fileedit.Preview{ID: value.ID, SessionID: value.SessionID,
		WorkspaceID: value.WorkspaceID, Path: value.Path, Operation: value.Operation,
		DestinationPath: value.DestinationPath, Status: value.Status,
		Diff: value.Diff, OriginalHash: value.OriginalHash, ProposedHash: value.ProposedHash,
		DestinationOriginalHash: value.DestinationOriginalHash,
		DestinationProposedHash: value.DestinationProposedHash,
		Reason:                  value.Reason, SecretsRedacted: value.SecretsRedacted,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}, terminal)
}

type fileEditDetailReader interface {
	GetFileEdit(context.Context, string) (fileedit.Edit, error)
}

func runWakeIntentView(value domain.RunWakeIntent, found bool) *RunWakeIntentView {
	if !found {
		return nil
	}
	return &RunWakeIntentView{ID: value.ID, ProtocolVersion: value.ProtocolVersion,
		RunID: value.RunID, SessionID: value.SessionID, Status: string(value.Status),
		MaxAttempts: value.MaxAttempts, AttemptCount: value.AttemptCount,
		InitialDelaySeconds: value.InitialDelaySeconds,
		BaseBackoffSeconds:  value.BaseBackoffSeconds,
		MaxBackoffSeconds:   value.MaxBackoffSeconds,
		MaxElapsedSeconds:   value.MaxElapsedSeconds, NextWakeAt: value.NextWakeAt,
		DeadlineAt: value.DeadlineAt, ExecutionEnabled: false,
		BackgroundLoopEnabled: false, CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, CancelledAt: value.CancelledAt}
}
