package application

import (
	"encoding/json"
	"slices"

	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

// Project the same Go-issued snapshot used to advertise tools into actual
// provider messages. ChatRequest.Metadata is internal and cannot teach a model
// whether preparing a proposal or applying it requires operator review.
func supervisorFileEditGuidance(request llm.ChatRequest, snapshot toolgateway.AgentCodeCapabilitySnapshot, historyStart int) llm.ChatRequest {
	offered := map[string]bool{}
	for _, tool := range request.Tools {
		offered[tool.Name] = true
	}
	if !offered[string(toolgateway.WorkspaceChangeTool)] {
		return request
	}
	view := struct {
		PermissionMode string `json:"permission_mode"`
		Change         string `json:"workspace_change"`
		Apply          string `json:"workspace_apply,omitempty"`
	}{PermissionMode: snapshot.PermissionMode}
	for _, tool := range snapshot.Tools {
		if !tool.Available || !offered[string(tool.Name)] {
			continue
		}
		switch tool.Name {
		case toolgateway.WorkspaceChangeTool:
			view.Change = tool.Approval
		case toolgateway.WorkspaceApplyTool:
			view.Apply = tool.Approval
		}
	}
	if view.Change == "" {
		return request
	}
	encoded, _ := json.Marshal(view)
	text := "Go-issued file-edit capability snapshot advertised for this segment/request: " + string(encoded) + ". This describes eligibility when the tools were advertised, not an approval or a guarantee that authority remains live. Go rechecks the current scope, permission, lease and file hashes at execution; a newer denial takes precedence over this snapshot. If the user requested a file change, workspace_change prepares and—when automatically authorized—atomically applies the change in one step; when review is required, workspace_change records a proposal without writing bytes, and operator approval triggers direct host apply. Do not ask the user to approve a proposal you have not prepared. Read the resulting status, file_written, apply_authorized, and review_required flags (apply_arguments are retained only for compatibility or recovery of unapplied approved edits). Historical failed or unknown effects describe those earlier calls; they establish neither current permission nor revocation. They do not by themselves prevent preparing a new proposal. Read-only tasks still require no edits."
	if view.Change == "operation_policy_then_existing_proposal" {
		text += " Eligible prepared operations can receive recorded automatic authorization under the current operation policy while their runtime authority remains current. Obtain that result through workspace_change; do not infer that an operator review is missing merely because no new proposal has been made."
	}
	request.Messages = slices.Insert(slices.Clone(request.Messages), historyStart, llm.Message{Role: "system", Content: text})
	return request
}
