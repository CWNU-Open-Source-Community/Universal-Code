package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const (
	FanoutExecutionsPathTemplate        = "/api/v1/runs/{run_id}/fanout-executions"
	FanoutExecutionCancelPathTemplate   = "/api/v1/runs/{run_id}/fanout-executions/{execution_id}/cancel"
	FanoutExecutionsListProtocolVersion = "readonly_fanout_executions.v1"
)

// FanoutExecutionController is the bounded read/list + cancel surface for
// read-only fan-out executions. Cancellation carries the addressable Run so the
// Go service can reject a foreign binding before it writes; the list is plan
// keyed, so the route verifies every returned execution against its own Run.
type FanoutExecutionController interface {
	ListReadOnlyFanoutExecutions(context.Context, string, int) ([]domain.ReadOnlyFanoutExecution, error)
	CancelReadOnlyFanoutExecution(context.Context, string, string, string, string) (domain.ReadOnlyFanoutExecution, error)
}

// FanoutExecutionCancelRequestView is the explicit-confirmation control body.
type FanoutExecutionCancelRequestView struct {
	Version       string `json:"version"`
	ConfirmCancel bool   `json:"confirm_cancel"`
}

// FanoutExecutionsListView projects one plan's execution history.
type FanoutExecutionsListView struct {
	ProtocolVersion string                `json:"protocol_version"`
	PlanID          string                `json:"plan_id"`
	Items           []FanoutExecutionView `json:"items"`
}

func (a *API) listFanoutExecutions(request *http.Request, runID string) (any, *Page, error) {
	if err := validateSingleQueryValues(request.URL.Query(), "plan_id", "limit"); err != nil {
		return nil, nil, err
	}
	planID := strings.TrimSpace(request.URL.Query().Get("plan_id"))
	if planID == "" || !domain.ValidAgentID(planID) {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"fan-out execution list requires a valid plan_id")
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"fan-out execution limit must be between 1 and 100")
		}
		limit = parsed
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	executions, err := a.fanoutExecutionController.ListReadOnlyFanoutExecutions(
		request.Context(), planID, limit)
	if err != nil {
		return nil, nil, err
	}
	view := FanoutExecutionsListView{ProtocolVersion: FanoutExecutionsListProtocolVersion,
		PlanID: planID, Items: make([]FanoutExecutionView, 0, len(executions))}
	for _, execution := range executions {
		// The collection is addressable only through its own Run; plan_id alone
		// must not surface another Run's shard history. An absent plan keeps its
		// existing empty projection, so this cannot become an existence oracle.
		if execution.RunID != runID {
			return nil, nil, apperror.New(apperror.CodeNotFound,
				"fan-out execution list belongs to another run")
		}
		view.Items = append(view.Items, fanoutExecutionFullView(execution))
	}
	return view, nil, nil
}

func matchFanoutExecutionCancelPath(requestPath string) (string, string, bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", false
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(segments) != 4 || segments[0] == "" || segments[1] != "fanout-executions" ||
		segments[2] == "" || segments[3] != "cancel" {
		return "", "", false
	}
	return segments[0], segments[2], true
}

func (a *API) serveFanoutExecutionCancel(writer http.ResponseWriter, request *http.Request,
	requestID, runID, executionID string,
) {
	if a.fanoutExecutionController == nil {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if !a.controlEnabled {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if !a.authorized(request, a.controlTokenHash) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
		a.writeError(writer, requestID,
			apperror.New(apperror.CodePolicyDenied, "valid control bearer authorization is required"),
			http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeInvalidArgument,
				"fan-out execution cancellation only supports POST"),
			http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validatePathIdentity(executionID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var body struct {
		Version       string `json:"version"`
		ConfirmCancel bool   `json:"confirm_cancel"`
	}
	strictBody, err := readStrictControlBody(request, "fan-out execution cancellation")
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := decodeStrictRunOperation(strictBody, &body, "fan-out execution cancellation"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if body.Version != "readonly_fanout_cancel.v1" || !body.ConfirmCancel {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"fan-out execution cancellation requires explicit confirmation"),
			http.StatusBadRequest)
		return
	}
	execution, err := a.fanoutExecutionController.CancelReadOnlyFanoutExecution(
		request.Context(), runID, executionID, "web_operator", requestID)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if execution.RunID != runID {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
			"fan-out execution is not part of the run"), http.StatusNotFound)
		return
	}
	a.writeSuccessStatus(writer, requestID, fanoutExecutionFullView(execution), nil, http.StatusOK)
}

func fanoutExecutionFullView(value domain.ReadOnlyFanoutExecution) FanoutExecutionView {
	view := FanoutExecutionView{ID: value.ID, Status: string(value.Status),
		Parallelism: value.Parallelism, MaxOutputTokensPerShard: value.MaxOutputTokensPerShard,
		RequestedBy: value.RequestedBy, StopCode: value.StopCode,
		StartedAt: value.StartedAt, UpdatedAt: value.UpdatedAt,
		FinishedAt: value.FinishedAt, Shards: make([]FanoutExecutionShardView, 0, len(value.Shards))}
	for _, shard := range value.Shards {
		view.Shards = append(view.Shards, FanoutExecutionShardView{
			Ordinal: shard.Ordinal, Status: string(shard.Status),
			AttemptCount: shard.AttemptCount, CurrentAttempt: shard.CurrentAttempt,
			Provider: shard.Provider, Model: shard.Model,
			InputTokens: shard.InputTokens, OutputTokens: shard.OutputTokens,
			TotalTokens: shard.TotalTokens, ElapsedMillis: shard.ElapsedMillis,
			FindingCount: shard.FindingCount, ErrorCode: shard.ErrorCode,
			StartedAt: shard.StartedAt, FinishedAt: shard.FinishedAt,
		})
	}
	return view
}
