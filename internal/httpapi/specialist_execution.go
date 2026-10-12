package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const SpecialistExecutionPathTemplate = "/api/v1/runs/{run_id}/agents/{agent_id}/execute"
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
	if len(p) == 4 && p[0] != "" && p[1] == "agents" && p[2] != "" && p[3] == "execute" {
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
