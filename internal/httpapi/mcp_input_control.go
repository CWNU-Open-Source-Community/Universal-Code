package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/mcp"
)

const MCPInputsPathTemplate = "/api/v1/runs/{run_id}/mcp-inputs"
const MCPInputResponsePathTemplate = "/api/v1/runs/{run_id}/mcp-inputs/{input_id}"

type MCPInputController interface {
	ListMCPInputs(context.Context, string) ([]mcp.InputView, error)
	RespondMCPInput(context.Context, string, string, mcp.InputResponse) error
}
type MCPInputListView struct {
	Version string          `json:"version"`
	Items   []mcp.InputView `json:"items"`
}
type MCPInputResponseView struct {
	Accepted bool `json:"accepted"`
}

func matchMCPInputPath(path string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/runs/"), "/")
	if !strings.HasPrefix(path, "/api/v1/runs/") || (len(parts) != 2 && len(parts) != 3) || parts[1] != "mcp-inputs" {
		return "", "", false
	}
	if len(parts) == 3 {
		return parts[0], parts[2], parts[2] != ""
	}
	return parts[0], "", true
}
func (a *API) serveMCPInput(w http.ResponseWriter, r *http.Request, requestID, runID, inputID string) {
	w.Header().Set("Cache-Control", "no-store")
	if inputID != "" {
		if !a.authorizeRunOperation(w, r, requestID, a.extensionControlEnabled, "MCP input response") {
			return
		}
	} else {
		if !a.authorized(r, a.tokenHash) {
			a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "MCP input listing requires GET"), http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "MCP input listing cannot contain a body"), 0)
			return
		}
	}
	controller, ok := a.extensionController.(MCPInputController)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "MCP input interaction is unavailable"), 0)
		return
	}
	if validatePathIdentity(runID) != nil || (inputID != "" && validatePathIdentity(inputID) != nil) || r.URL.RawQuery != "" {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "invalid MCP input path or query"), 0)
		return
	}
	if inputID == "" {
		items, err := controller.ListMCPInputs(r.Context(), runID)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, MCPInputListView{Version: mcp.InputProtocolVersion, Items: items}, nil)
		return
	}
	raw, err := readExtensionOnboardingBody(r, 36*1024)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var response mcp.InputResponse
	if err = decodeStrictRunOperation(raw, &response, "MCP input response"); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if err = controller.RespondMCPInput(r.Context(), runID, inputID, response); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, MCPInputResponseView{Accepted: true}, nil)
}
