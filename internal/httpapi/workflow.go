package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

type WorkflowController interface {
	Save(context.Context, domain.WorkflowSpec) (domain.WorkflowDefinition, error)
	Definitions(context.Context) ([]domain.WorkflowDefinition, error)
	Definition(context.Context, string) (domain.WorkflowDefinition, error)
	Start(context.Context, application.StartWorkflowRequest) (domain.WorkflowExecution, error)
	Get(context.Context, string) (domain.WorkflowExecution, error)
	List(context.Context, string) ([]domain.WorkflowExecution, error)
	AdvanceAtRevision(context.Context, string, int64) (domain.WorkflowExecution, error)
	Pause(context.Context, string, int64) (domain.WorkflowExecution, error)
	Resume(context.Context, string, int64) (domain.WorkflowExecution, error)
	Rerun(context.Context, string, string, string) (domain.WorkflowExecution, error)
}
type WorkflowDefinitionsView struct {
	Items []domain.WorkflowDefinition `json:"items"`
}
type WorkflowExecutionsView struct {
	Items []domain.WorkflowExecution `json:"items"`
}
type WorkflowStartView struct {
	Version      string          `json:"version"`
	DefinitionID string          `json:"definition_id"`
	Inputs       json.RawMessage `json:"inputs"`
	ConfirmStart bool            `json:"confirm_start"`
}
type WorkflowControlView struct {
	Version      string `json:"version"`
	Revision     int64  `json:"revision"`
	ConfirmStart bool   `json:"confirm_start,omitempty"`
}

func matchWorkflowPath(path string) (runID, id, action string, matched bool) {
	if path == "/api/v1/workflows" {
		return "", "", "definitions", true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if !strings.HasPrefix(path, "/api/v1/") {
		return
	}
	if len(parts) == 2 && parts[0] == "workflows" && domain.ValidAgentID(parts[1]) {
		return "", parts[1], "definition", true
	}
	if len(parts) < 3 || len(parts) > 5 || parts[0] != "runs" || parts[2] != "workflows" || !domain.ValidAgentID(parts[1]) {
		return
	}
	runID = parts[1]
	if len(parts) == 3 {
		return runID, "", "executions", true
	}
	if !domain.ValidAgentID(parts[3]) {
		return "", "", "", false
	}
	id = parts[3]
	if len(parts) == 4 {
		return runID, id, "execution", true
	}
	switch parts[4] {
	case "advance", "pause", "resume", "rerun":
		return runID, id, parts[4], true
	}
	return "", "", "", false
}
func (a *API) serveWorkflow(w http.ResponseWriter, r *http.Request, requestID, runID, id, action string) {
	if a.workflowController == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "workflow control is unavailable"), http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		if !a.authorized(r, a.tokenHash) {
			a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if err := rejectQuery(r.URL.Query()); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
	} else if !a.authorizeRunOperation(w, r, requestID, a.modelControlEnabled, "workflow control") {
		return
	}
	if runID != "" {
		if _, err := a.store.GetRun(r.Context(), runID); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
	}
	if id != "" && runID != "" {
		e, err := a.workflowController.Get(r.Context(), id)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		if e.RunID != runID {
			a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "workflow is not part of this Run"), http.StatusNotFound)
			return
		}
	}
	var value any
	var err error
	if r.Method == http.MethodGet {
		switch action {
		case "definitions":
			var items []domain.WorkflowDefinition
			items, err = a.workflowController.Definitions(r.Context())
			value = WorkflowDefinitionsView{Items: items}
		case "definition":
			value, err = a.workflowController.Definition(r.Context(), id)
		case "executions":
			var items []domain.WorkflowExecution
			items, err = a.workflowController.List(r.Context(), runID)
			value = WorkflowExecutionsView{Items: items}
		case "execution":
			value, err = a.workflowController.Get(r.Context(), id)
		default:
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "workflow mutation requires POST"), http.StatusMethodNotAllowed)
			return
		}
	} else {
		key, raw, readErr := a.readRunOperationRequestWithLimit(r, "workflow control", domain.MaxWorkflowJSONBytes+4096)
		if readErr != nil {
			a.writeError(w, requestID, readErr, 0)
			return
		}
		switch action {
		case "definitions":
			var spec domain.WorkflowSpec
			if err = decodeStrictRunOperation(raw, &spec, "workflow definition"); err == nil {
				value, err = a.workflowController.Save(r.Context(), spec)
			}
		case "executions":
			var body WorkflowStartView
			if err = decodeStrictRunOperation(raw, &body, "workflow start"); err == nil {
				if body.Version != "workflow_start.v1" || !body.ConfirmStart {
					err = apperror.New(apperror.CodeInvalidArgument, "workflow start requires explicit confirmation")
				} else {
					value, err = a.workflowController.Start(r.Context(), application.StartWorkflowRequest{RunID: runID, DefinitionID: body.DefinitionID, Inputs: body.Inputs, OperationKey: key})
				}
			}
		case "advance", "pause", "resume", "rerun":
			var body WorkflowControlView
			if err = decodeStrictRunOperation(raw, &body, "workflow control"); err == nil {
				if body.Version != "workflow_control.v1" {
					err = apperror.New(apperror.CodeInvalidArgument, "unsupported workflow control version")
				} else {
					switch action {
					case "advance":
						value, err = a.workflowController.AdvanceAtRevision(r.Context(), id, body.Revision)
					case "pause":
						value, err = a.workflowController.Pause(r.Context(), id, body.Revision)
					case "resume":
						value, err = a.workflowController.Resume(r.Context(), id, body.Revision)
					case "rerun":
						if !body.ConfirmStart {
							err = apperror.New(apperror.CodeInvalidArgument, "workflow rerun requires confirmation")
						} else {
							value, err = a.workflowController.Rerun(r.Context(), id, runID, key)
						}
					}
				}
			}
		default:
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "workflow definition and execution reads require GET"), http.StatusMethodNotAllowed)
			return
		}
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(w, requestID, value, nil, http.StatusOK)
}

func workflowOperationSpecs() []openAPIOperationSpec {
	runID := pathIdentityParameter("run_id", "Run owning the execution")
	definitionID := pathIdentityParameter("definition_id", "Immutable saved definition")
	executionID := pathIdentityParameter("execution_id", "Workflow execution")
	key := openAPIParameter{Name: "Idempotency-Key", In: "header", Required: true, Schema: map[string]any{"type": "string", "minLength": 16, "maxLength": 256}}
	specs := []openAPIOperationSpec{
		{Path: "/api/v1/workflows", OperationID: "listWorkflowDefinitions", Summary: "List saved workflow definitions", Tag: "Analysis", DataType: reflect.TypeOf(WorkflowDefinitionsView{}), NotFound: true},
		{Path: "/api/v1/workflows", Method: http.MethodPost, OperationID: "saveWorkflowDefinition", Summary: "Save an immutable bounded workflow", Tag: "Control", DataType: reflect.TypeOf(domain.WorkflowDefinition{}), RequestType: reflect.TypeOf(domain.WorkflowSpec{}), Control: true, NotFound: true, Parameters: []openAPIParameter{key}, SuccessStatus: 200},
		{Path: "/api/v1/workflows/{definition_id}", OperationID: "getWorkflowDefinition", Summary: "Read a saved workflow", Tag: "Analysis", DataType: reflect.TypeOf(domain.WorkflowDefinition{}), NotFound: true, Parameters: []openAPIParameter{definitionID}},
		{Path: "/api/v1/runs/{run_id}/workflows", OperationID: "listRunWorkflowExecutions", Summary: "List Run workflow executions", Tag: "Analysis", DataType: reflect.TypeOf(WorkflowExecutionsView{}), NotFound: true, Parameters: []openAPIParameter{runID}},
		{Path: "/api/v1/runs/{run_id}/workflows", Method: http.MethodPost, OperationID: "startRunWorkflow", Summary: "Start a saved workflow with inputs", Tag: "Control", DataType: reflect.TypeOf(domain.WorkflowExecution{}), RequestType: reflect.TypeOf(WorkflowStartView{}), Control: true, NotFound: true, Parameters: []openAPIParameter{runID, key}, SuccessStatus: 200},
		{Path: "/api/v1/runs/{run_id}/workflows/{execution_id}", OperationID: "getRunWorkflow", Summary: "Read workflow state and committed outputs", Tag: "Analysis", DataType: reflect.TypeOf(domain.WorkflowExecution{}), NotFound: true, Parameters: []openAPIParameter{runID, executionID}},
	}
	for _, action := range []string{"advance", "pause", "resume", "rerun"} {
		specs = append(specs, openAPIOperationSpec{Path: "/api/v1/runs/{run_id}/workflows/{execution_id}/" + action, Method: http.MethodPost, OperationID: action + "RunWorkflow", Summary: action + " a saved workflow execution", Description: "Advance executes at most one real worker turn. Its observed revision fences replay. Pause takes effect before the next turn; resume preserves committed work; confirmed rerun starts fresh.", Tag: "Control", DataType: reflect.TypeOf(domain.WorkflowExecution{}), RequestType: reflect.TypeOf(WorkflowControlView{}), Control: true, NotFound: true, Parameters: []openAPIParameter{runID, executionID, key}, SuccessStatus: 200})
	}
	return specs
}
