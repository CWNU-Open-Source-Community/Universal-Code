package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const (
	RunLifecycleControlPathTemplate = "/api/v1/runs/{run_id}/lifecycle"
	RunExecutionControlPathTemplate = "/api/v1/runs/{run_id}/execute"
	MaxRunOperationControlBodyBytes = 4 * 1024
)

type RunLifecycleController interface {
	Apply(context.Context, application.ControlRunLifecycleRequest) (
		application.ControlRunLifecycleResult, error)
}

type RunExecutionController interface {
	Execute(context.Context, application.ExecuteRunHandoffRequest) (
		application.ExecuteRunHandoffResult, error)
}

type RunLifecycleControlRequestView struct {
	Version string                    `json:"version"`
	Action  domain.RunLifecycleAction `json:"action"`
}

type RunLifecycleControlView struct {
	Version            string  `json:"version"`
	Run                RunView `json:"run"`
	Action             string  `json:"action"`
	ExpectedStatus     string  `json:"expected_status"`
	AppliedStatus      string  `json:"applied_status"`
	EventSequenceStart int64   `json:"event_sequence_start"`
	EventSequenceEnd   int64   `json:"event_sequence_end"`
	Replayed           bool    `json:"replayed"`
	ExecutionStarted   bool    `json:"execution_started"`
	ModelCalled        bool    `json:"model_called"`
	ToolCalled         bool    `json:"tool_called"`
	CapabilityGrant    bool    `json:"capability_grant"`
}

type RunExecutionControlRequestView struct {
	Version  string `json:"version"`
	MaxSteps int    `json:"max_steps"`
}

type RunExecutionControlView struct {
	Version                 string `json:"version"`
	OperationID             string `json:"operation_id"`
	RunID                   string `json:"run_id"`
	SessionID               string `json:"session_id"`
	MaxSteps                int    `json:"max_steps"`
	SelectedCount           int    `json:"selected_count"`
	Status                  string `json:"status"`
	RunStatus               string `json:"run_status"`
	StopReason              string `json:"stop_reason"`
	ErrorCode               string `json:"error_code,omitempty"`
	StepsCompleted          int    `json:"steps_completed"`
	PendingCount            int    `json:"pending_count"`
	PreparedCount           int    `json:"prepared_count"`
	CommittedCount          int    `json:"committed_count"`
	CancelledCount          int    `json:"cancelled_count"`
	CompletionEventSequence int64  `json:"completion_event_sequence"`
	Replayed                bool   `json:"replayed"`
	ExecutionStarted        bool   `json:"execution_started"`
	ModelCalled             bool   `json:"model_called"`
	ToolCalled              bool   `json:"tool_called"`
	CapabilityGrant         bool   `json:"capability_grant"`
}

func matchRunLifecycleControlPath(requestPath string) (string, bool) {
	return matchRunOperationControlPath(requestPath, "/lifecycle")
}

func matchRunExecutionControlPath(requestPath string) (string, bool) {
	return matchRunOperationControlPath(requestPath, "/execute")
}

func matchRunOperationControlPath(requestPath string, suffix string) (string, bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	if runID == "" || strings.Contains(runID, "/") {
		return "", false
	}
	return runID, true
}

func (a *API) serveRunLifecycleControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	if !a.authorizeRunOperation(writer, request, requestID,
		a.runLifecycleEnabled, "Run lifecycle") {
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
	operationKey, body, err := a.readRunOperationRequest(request,
		"Run lifecycle control")
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view RunLifecycleControlRequestView
	if err := decodeStrictRunOperation(body, &view, "Run lifecycle control"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := a.runLifecycleController.Apply(request.Context(),
		application.ControlRunLifecycleRequest{
			Version: view.Version, RunID: runID, Action: view.Action,
			OperationKey: operationKey, RequestedBy: "http_run_operator",
		})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, RunLifecycleControlView{
		Version: domain.RunLifecycleControlProtocolVersion, Run: runView(result.Run),
		Action:             string(result.Operation.Action),
		ExpectedStatus:     string(result.Operation.ExpectedStatus),
		AppliedStatus:      string(result.Operation.AppliedStatus),
		EventSequenceStart: result.Operation.EventSequenceStart,
		EventSequenceEnd:   result.Operation.EventSequenceEnd, Replayed: result.Replayed,
	}, nil, http.StatusAccepted)
}

func (a *API) serveRunExecutionControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	if !a.authorizeRunOperation(writer, request, requestID,
		a.runExecutionEnabled, "Run execution") {
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
	operationKey, body, err := a.readRunOperationRequest(request,
		"Run execution control")
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view RunExecutionControlRequestView
	if err := decodeStrictRunOperation(body, &view, "Run execution control"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	// Synchronous execution can outlast the ordinary HTTP write timeout.
	// Cancellation and Run budgets continue to bound the work itself.
	responseControl := http.NewResponseController(writer)
	if err := responseControl.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.writeError(writer, requestID, apperror.Wrap(apperror.CodeUnavailable, "Run response deadline could not be configured", err), 0)
		return
	}
	result, err := a.runExecutionController.Execute(request.Context(),
		application.ExecuteRunHandoffRequest{
			Version: view.Version, RunID: runID, MaxSteps: view.MaxSteps,
			OperationKey: operationKey, RequestedBy: "http_run_operator",
		})
	_ = responseControl.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	operation := result.Handoff.Operation
	completed := result.Handoff.Result
	if completed == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInternal,
			"Run execution handoff has no durable result"), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, RunExecutionControlView{
		Version:     domain.RunExecutionHandoffProtocolVersion,
		OperationID: operation.ID, RunID: operation.RunID, SessionID: operation.SessionID,
		MaxSteps: operation.MaxSteps, SelectedCount: operation.SelectedCount,
		Status: string(completed.Status), RunStatus: string(completed.RunStatus),
		StopReason: completed.StopReason, ErrorCode: completed.ErrorCode,
		StepsCompleted: completed.StepsCompleted, PendingCount: completed.PendingCount,
		PreparedCount: completed.PreparedCount, CommittedCount: completed.CommittedCount,
		CancelledCount:          completed.CancelledCount,
		CompletionEventSequence: completed.CompletionEventSequence,
		Replayed:                result.Replayed, ExecutionStarted: completed.LeaseID != "",
		ModelCalled: completed.ModelCalled, ToolCalled: completed.ToolCalled,
	}, nil, http.StatusAccepted)
}

func (a *API) authorizeRunOperation(writer http.ResponseWriter,
	request *http.Request, requestID string, enabled bool, label string,
) bool {
	if !enabled {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return false
	}
	if !a.authorized(request, a.controlTokenHash) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
		a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied,
			"valid control bearer authorization is required"), http.StatusUnauthorized)
		return false
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			label+" endpoint only supports POST"), http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func (a *API) readRunOperationRequest(request *http.Request,
	label string,
) (string, []byte, error) {
	return a.readRunOperationRequestWithLimit(request, label, MaxRunOperationControlBodyBytes)
}

func (a *API) readRunOperationRequestWithLimit(request *http.Request, label string, limit int64) (string, []byte, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return "", nil, err
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		return "", nil, err
	}
	body, err := readBoundedRequestBody(request, limit)
	if err != nil {
		return "", nil, err
	}
	if !utf8.Valid(body) {
		return "", nil, apperror.New(apperror.CodeInvalidArgument,
			label+" body must be valid UTF-8 JSON")
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		return "", nil, err
	}
	return operationKey, body, nil
}

func decodeStrictRunOperation(body []byte, destination any, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			label+" body must be one JSON object", err)
	}
	return ensureJSONEOF(decoder)
}

func runOperationErrorStatus(err error) int {
	if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeResourceExhausted {
		return http.StatusRequestEntityTooLarge
	}
	return 0
}
