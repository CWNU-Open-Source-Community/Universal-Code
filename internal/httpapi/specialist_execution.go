package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const SpecialistExecutionPathTemplate = "/api/v1/runs/{run_id}/agents/{agent_id}/execute"
const SpecialistContinuationPathTemplate = "/api/v1/runs/{run_id}/agents/{agent_id}/continue"
const SpecialistToolReceiptsPathTemplate = "/api/v1/runs/{run_id}/agents/{agent_id}/attempts/{attempt_id}/tool-receipts"

type SpecialistExecutionController interface {
	ExecuteSpecialist(context.Context, application.ExecuteSpecialistRequest) (application.SpecialistTurnResult, error)
}
type SpecialistExecutionRequestView struct {
	Version        string `json:"version"`
	OperationKey   string `json:"operation_key"`
	WorkspaceTools bool   `json:"workspace_tools"`
	ConfirmExecute bool   `json:"confirm_execute"`
}

type SpecialistContinuationRequestView struct {
	Version        string `json:"version"`
	OperationKey   string `json:"operation_key"`
	ConfirmExecute bool   `json:"confirm_execute"`
}

type SpecialistContinuationView struct {
	Version          string   `json:"version"`
	RunID            string   `json:"run_id"`
	AgentID          string   `json:"agent_id"`
	ThreadID         string   `json:"thread_id"`
	ScheduleID       string   `json:"schedule_id"`
	AttemptID        string   `json:"attempt_id,omitempty"`
	AttemptIDs       []string `json:"attempt_ids"`
	Status           string   `json:"status"`
	StopReason       string   `json:"stop_reason"`
	ErrorCode        string   `json:"error_code,omitempty"`
	RoundsCompleted  int      `json:"rounds_completed"`
	TurnsStarted     int      `json:"turns_started"`
	TotalTokens      int64    `json:"total_tokens"`
	ExecutionMillis  int64    `json:"execution_millis"`
	SummaryStatus    string   `json:"summary_status"`
	SummaryErrorCode string   `json:"summary_error_code,omitempty"`
	WakeID           string   `json:"wake_id,omitempty"`
	Replayed         bool     `json:"replayed"`
}
type SpecialistExecutionView struct {
	Version       string                   `json:"version"`
	RunID         string                   `json:"run_id"`
	AgentID       string                   `json:"agent_id"`
	AttemptID     string                   `json:"attempt_id"`
	Status        string                   `json:"status"`
	Usage         domain.AgentAttemptUsage `json:"usage"`
	ModelAttempts int                      `json:"model_attempts"`
	ToolCalls     int                      `json:"tool_calls"`
	ToolRounds    int                      `json:"tool_rounds"`
	Replayed      bool                     `json:"replayed"`
}
type SpecialistToolReceiptsView struct {
	Version string                    `json:"version"`
	Rounds  []SpecialistToolRoundView `json:"rounds"`
}

type SpecialistToolRoundView struct {
	Round        int                      `json:"round"`
	ModelAttempt int                      `json:"model_attempt"`
	Calls        []SpecialistToolCallView `json:"calls"`
}
type SpecialistToolCallView struct {
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ErrorCode  string `json:"error_code,omitempty"`
	ResultJSON string `json:"result_json,omitempty"`
}

func matchSpecialistExecutionPath(path string) (runID, agentID, attemptID string, matched bool) {
	if !strings.HasPrefix(path, "/api/v1/runs/") {
		return
	}
	p := strings.Split(strings.TrimPrefix(path, "/api/v1/runs/"), "/")
	if len(p) == 4 && p[0] != "" && p[1] == "agents" && p[2] != "" && (p[3] == "execute" || p[3] == "continue") {
		return p[0], p[2], "", true
	}
	if len(p) == 6 && p[0] != "" && p[1] == "agents" && p[2] != "" && p[3] == "attempts" && p[4] != "" && p[5] == "tool-receipts" {
		return p[0], p[2], p[4], true
	}
	return
}

func (a *API) serveSpecialistExecution(w http.ResponseWriter, r *http.Request, requestID, runID, agentID, attemptID string) {
	if attemptID != "" {
		if r.Method != http.MethodGet {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "child receipts require GET"), http.StatusMethodNotAllowed)
			return
		}
		if !a.authorized(r, a.tokenHash) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
			a.writeError(w, requestID,
				apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"),
				http.StatusUnauthorized)
			return
		}
		if err := rejectQuery(r.URL.Query()); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		st, ok := a.store.(interface {
			GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
			ListSpecialistToolRounds(context.Context, domain.AgentAttemptRef) ([]domain.SpecialistToolRound, error)
		})
		if !ok {
			a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "child receipts are unavailable"), 0)
			return
		}
		attempt, found, err := st.GetAgentAttempt(r.Context(), attemptID)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		if !found || attempt.RunID != runID || attempt.AgentID != agentID {
			a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "child attempt does not belong to this Run and Agent"), 0)
			return
		}
		rounds, err := st.ListSpecialistToolRounds(r.Context(), domain.AgentAttemptRef{RunID: runID, AgentID: agentID, AttemptID: attemptID})
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		view := SpecialistToolReceiptsView{Version: domain.SpecialistToolsVersion, Rounds: []SpecialistToolRoundView{}}
		for _, round := range rounds {
			item := SpecialistToolRoundView{Round: round.Round, ModelAttempt: round.ModelAttempt, Calls: []SpecialistToolCallView{}}
			for _, call := range round.Calls {
				item.Calls = append(item.Calls, SpecialistToolCallView{CallID: call.CallID, Name: call.Name, Status: string(call.Status), ErrorCode: call.ErrorCode, ResultJSON: call.ResultJSON})
			}
			view.Rounds = append(view.Rounds, item)
		}
		a.writeSuccessStatus(w, requestID, view, nil, http.StatusOK)
		return
	}
	if a.specialistExecutionController == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "child execution is unavailable"), 0)
		return
	}
	if !a.authorizeRunOperation(w, r, requestID, a.modelControlEnabled, "Specialist execution") {
		return
	}
	if strings.HasSuffix(r.URL.Path, "/continue") {
		a.serveSpecialistContinuation(w, r, requestID, runID, agentID)
		return
	}
	var body SpecialistExecutionRequestView
	raw, err := readStrictControlBody(r, "Specialist execution")
	if err == nil {
		err = decodeStrictRunOperation(raw, &body, "Specialist execution")
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if body.Version != domain.SpecialistToolsVersion || !body.ConfirmExecute {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "child execution requires its version and explicit confirmation"), 0)
		return
	}
	result, err := a.specialistExecutionController.ExecuteSpecialist(r.Context(), application.ExecuteSpecialistRequest{RunID: runID, AgentID: agentID, OperationKey: body.OperationKey, WorkspaceTools: body.WorkspaceTools})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(w, requestID, SpecialistExecutionView{Version: domain.SpecialistToolsVersion, RunID: result.RunID, AgentID: result.AgentID, AttemptID: result.AttemptID, Status: string(result.AttemptStatus), Usage: result.Usage, ModelAttempts: result.ModelAttempts, ToolCalls: result.ToolCalls, ToolRounds: result.ToolRounds, Replayed: result.Replayed}, nil, http.StatusOK)
}

func (a *API) serveSpecialistContinuation(w http.ResponseWriter, r *http.Request, requestID, runID, agentID string) {
	controller, ok := a.specialistExecutionController.(interface {
		ContinueSpecialist(context.Context, application.ContinueSpecialistRequest) (application.SpecialistContinuationResult, error)
	})
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "continuous child execution is unavailable"), 0)
		return
	}
	var body SpecialistContinuationRequestView
	raw, err := readStrictControlBody(r, "Specialist continuation")
	if err == nil {
		err = decodeStrictRunOperation(raw, &body, "Specialist continuation")
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if body.Version != application.SpecialistContinuationVersion || !body.ConfirmExecute {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "continuous child execution and completion summary require explicit confirmation"), 0)
		return
	}
	// Multi-round child work can exceed the ordinary HTTP write timeout.
	// Request cancellation and the durable Agent/Run budgets still bound work.
	responseControl := http.NewResponseController(w)
	if err := responseControl.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.writeError(w, requestID, apperror.Wrap(apperror.CodeUnavailable, "child response deadline could not be configured", err), 0)
		return
	}
	result, err := controller.ContinueSpecialist(r.Context(), application.ContinueSpecialistRequest{RunID: runID, AgentID: agentID, OperationKey: body.OperationKey})
	_ = responseControl.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	before, after := result.Schedule.UsageBefore, result.Schedule.UsageAfter
	a.writeSuccessStatus(w, requestID, SpecialistContinuationView{
		Version: application.SpecialistContinuationVersion, RunID: result.RunID, AgentID: result.AgentID, ThreadID: result.ThreadID,
		ScheduleID: result.Schedule.ID, AttemptID: result.AttemptID, Status: string(result.Schedule.Status), StopReason: result.Schedule.StopReason,
		ErrorCode:       result.Schedule.ErrorCode,
		AttemptIDs:      result.AttemptIDs,
		RoundsCompleted: result.Schedule.RoundsCompleted, TurnsStarted: result.Schedule.TurnsStarted,
		TotalTokens: after.SpecialistTokens - before.SpecialistTokens, ExecutionMillis: after.SpecialistExecutionMillis - before.SpecialistExecutionMillis,
		SummaryStatus: result.SummaryStatus, SummaryErrorCode: result.SummaryErrorCode, WakeID: result.WakeID, Replayed: result.Replayed,
	}, nil, http.StatusOK)
}
