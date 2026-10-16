package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/toolrun"
)

// The queue stays metadata-only. This bounded projection is read on demand and
// contains only the proposal bound to this approval, never raw tool output.
type ApprovalPreviewView struct {
	ProtocolVersion  string                     `json:"protocol_version"`
	RunID            string                     `json:"run_id"`
	ApprovalID       string                     `json:"approval_id"`
	ProposalID       string                     `json:"proposal_id"`
	ToolName         string                     `json:"tool_name"`
	WorkspaceID      string                     `json:"workspace_id"`
	Effect           string                     `json:"effect"`
	WorkingDirectory string                     `json:"working_directory"`
	Fields           []ApprovalPreviewFieldView `json:"fields"`
	SourceCurrent    bool                       `json:"source_current"`
	Redacted         bool                       `json:"redacted"`
	Truncated        bool                       `json:"truncated"`
}

type ApprovalPreviewFieldView struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (a *API) runApprovalPreview(request *http.Request, runID, approvalID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if err := validatePathIdentity(approvalID); err != nil {
		return nil, nil, err
	}
	ctx := request.Context()
	run, err := a.store.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	record, err := a.store.GetApproval(ctx, approvalID)
	if err != nil {
		return nil, nil, err
	}
	if record.RunID != run.ID {
		return nil, nil, apperror.New(apperror.CodeNotFound, "approval does not belong to this Run")
	}
	view := ApprovalPreviewView{ProtocolVersion: application.ApprovalQueueProtocolVersion,
		RunID: run.ID, ApprovalID: record.ID, ProposalID: record.ProposalID,
		ToolName: record.ToolName, WorkspaceID: record.WorkspaceID, Effect: "unavailable",
		Fields: []ApprovalPreviewFieldView{}}
	stale := func() (any, *Page, error) {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
			"approval proposal is unavailable or its exact binding has changed; refresh approvals")
	}
	add := func(name, raw string) {
		value := redact.String(raw)
		view.Redacted = view.Redacted || value != raw || strings.Contains(value, "[REDACTED:")
		runes := []rune(value)
		if len(runes) > 8192 {
			value = string(runes[:8192])
			view.Truncated = true
		}
		view.Fields = append(view.Fields, ApprovalPreviewFieldView{Name: name, Value: value})
	}
	view.SourceCurrent = !run.Terminal() && record.Status == approval.StatusPending &&
		record.GrantID == "" && record.Mode != "never"
	switch record.ToolName {
	case string(toolgateway.CommandRuntimeTool):
		source, ok := a.store.(interface {
			GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
		})
		if !ok {
			return stale()
		}
		call, started, err := source.GetSupervisorApprovalCall(ctx, run.ID, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		input, _, err := toolgateway.NormalizeCommandRuntimePayload(json.RawMessage(call.PayloadJSON))
		if err != nil || record.RequestFingerprint != commandruntimeadapter.OperationApprovalFingerprint(call) || call.ToolName != string(toolgateway.CommandRuntimeTool) {
			return stale()
		}
		base, ok := a.store.(application.ApprovalControlStore)
		view.SourceCurrent = view.SourceCurrent && !started && ok
		if view.SourceCurrent {
			view.SourceCurrent = application.RecheckCommandApproval(ctx, base, record) == nil
		}
		view.Effect = "command_process"
		add("action", input.Action)
		add("intent", call.PayloadJSON)
		if input.ReviewScope != nil {
			risk, _ := json.Marshal(input.ReviewScope)
			add("review_scope", string(risk))
			add("bounded_review", "Optional bounded Run review groups this purpose/risk scope for at most 8 uses and 900 seconds. Each exact new command still needs explicit review; declarations do not enforce isolation.")
			if ledger, ok := a.store.(interface {
				GetCommandApprovalGrantScope(context.Context, string) (approval.GrantQuery, error)
				FindActiveSessionGrant(context.Context, approval.GrantQuery) (approval.SessionGrant, bool, error)
			}); ok && view.SourceCurrent {
				query, queryErr := ledger.GetCommandApprovalGrantScope(ctx, record.ProposalID)
				if queryErr != nil {
					return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "bounded review limits are unavailable; refresh before approving")
				}
				grant, found, grantErr := ledger.FindActiveSessionGrant(ctx, query)
				if grantErr != nil {
					return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "bounded review limits are unavailable; refresh before approving")
				}
				if found && approval.MatchesBoundedGrantScope(grant, query) && grant.Status == approval.GrantActive && grant.ExpiresAt != nil && time.Now().UTC().Before(*grant.ExpiresAt) && grant.UsesRemaining > 0 {
					add("grant_ttl_seconds", strconv.Itoa(int(grant.ExpiresAt.Sub(grant.CreatedAt)/time.Second)))
					add("grant_max_uses", strconv.Itoa(grant.MaxUses))
					add("grant_uses_remaining", strconv.Itoa(grant.UsesRemaining))
					add("grant_expires_at", grant.ExpiresAt.Format(time.RFC3339))
				}
			}
		}
		add("summary", "Approve only this pinned command batch or these stdin bytes for the original Run-owned process. A host working directory and network declaration do not enforce isolation; changed inputs require a new proposal.")
	case mcp.OperationApprovalTool:
		source, ok := a.store.(interface {
			GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
		})
		if !ok {
			return stale()
		}
		call, started, err := source.GetSupervisorApprovalCall(ctx, run.ID, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		payload, _, err := toolgateway.NormalizeMCPToolPayload(json.RawMessage(call.PayloadJSON))
		if err != nil || record.RequestFingerprint != mcp.OperationApprovalFingerprint(call) || call.ToolName != mcp.OperationApprovalTool {
			return stale()
		}
		base, ok := a.store.(application.ApprovalControlStore)
		view.SourceCurrent = view.SourceCurrent && !started && ok
		if view.SourceCurrent {
			view.SourceCurrent = application.RecheckMCPApproval(ctx, base, record) == nil
		}
		view.Effect = "mcp_server_and_tool"
		add("server", payload.ServerID)
		add("tool", payload.ToolName)
		add("arguments", string(payload.Arguments))
		add("summary", "Start/connect to this reviewed MCP server, discover its capabilities and invoke this exact tool. External effects are unverified.")
		if ok {
			target, targetErr := application.MCPComputerUseApprovalPreview(ctx, base, record)
			if targetErr != nil {
				return stale()
			}
			if target != nil {
				view.Effect = "computer_use_app"
				view.Fields = nil
				add("app_path", target.Executable)
				add("app_id", target.AppID)
				add("window_id", target.WindowID)
				add("tool_name", target.ToolName)
				add("arguments", string(payload.Arguments))
				if target.SystemShortcut {
					add("authorization_scope", "仅本次系统快捷键；可能影响桌面或其他应用（"+run.ID+"）")
				} else {
					add("authorization_scope", "当前任务（"+run.ID+"）")
				}
			}
		}
	case "shell":
		source, ok := a.store.(interface {
			GetToolRun(context.Context, string) (toolrun.ToolRun, error)
		})
		if !ok {
			return stale()
		}
		value, err := source.GetToolRun(ctx, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		if value.ID != record.ProposalID || value.SessionID != record.SessionID ||
			value.WorkspaceID != record.WorkspaceID || record.RequestFingerprint !=
			approval.ShellFingerprint(value.SessionID, value.WorkspaceID, value.Command) {
			return stale()
		}
		view.Effect, view.WorkingDirectory = "dry_run", "."
		view.SourceCurrent = view.SourceCurrent && value.Status == toolrun.StatusProposed
		add("command", value.Command)
	case "script_process":
		source, ok := a.store.(interface {
			GetScriptProcess(context.Context, string) (scriptprocess.Process, error)
		})
		if !ok {
			return stale()
		}
		value, err := source.GetScriptProcess(ctx, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		if value.ID != record.ProposalID || value.RunID != record.RunID ||
			value.SessionID != record.SessionID || value.WorkspaceID != record.WorkspaceID ||
			value.ApprovalFingerprint != record.RequestFingerprint || value.Validate() != nil {
			return stale()
		}
		view.Effect, view.WorkingDirectory = "dry_run", value.WorkingDirectory
		view.SourceCurrent = view.SourceCurrent && value.Status == scriptprocess.StatusProposed &&
			value.ExecutionMode == scriptprocess.ExecutionDisabled
		add("executable", value.Executable)
		arguments, _ := json.Marshal(value.Arguments)
		add("arguments", string(arguments))
		add("requested_backend", value.RequestedBackend)
	case gitadvanced.ApprovalToolName:
		source, ok := a.store.(interface {
			GetGitAdvancedOperation(context.Context, string) (gitadvanced.OperationRecord, bool, error)
		})
		if !ok {
			return stale()
		}
		value, found, err := source.GetGitAdvancedOperation(ctx, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		var preview gitadvanced.Preview
		if !found || value.RunID != record.RunID || value.SessionID != record.SessionID ||
			value.WorkspaceID != record.WorkspaceID || value.ApprovalFingerprint != record.RequestFingerprint ||
			json.Unmarshal([]byte(value.PreviewJSON), &preview) != nil ||
			preview.ID != value.PreviewID || preview.ApprovalFingerprint != record.RequestFingerprint ||
			preview.Spec.Validate() != nil || preview.Operation != value.Operation {
			return stale()
		}
		view.Effect, view.WorkingDirectory = "record_git_approval", "."
		view.SourceCurrent = view.SourceCurrent && value.Status == gitadvanced.OperationProposed &&
			value.ApprovalID == "" && preview.Executable()
		add("operation", string(value.Operation))
		spec, _ := json.MarshalIndent(preview.Spec, "", "  ")
		add("parameters", string(spec))
		add("summary", preview.Summary)
	case "replace_file", "create_file", "move_file", "delete_file":
		value, err := a.store.GetFileEditPreview(ctx, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		belongs, err := a.store.FileEditWorkspaceBelongsToRun(ctx, run.ID, value.SessionID, value.WorkspaceID)
		if err != nil {
			return nil, nil, err
		}
		if value.ID != record.ProposalID || value.SessionID != run.SessionID ||
			value.SessionID != record.SessionID || value.WorkspaceID != record.WorkspaceID || !belongs {
			return stale()
		}
		mission, err := a.store.GetMission(ctx, run.MissionID)
		if err != nil {
			return nil, nil, err
		}
		target, targetErr := application.ResolveRunFileWorkspace(ctx, a.store, run, mission, a.fileWorkspaceDrydocks)
		view.SourceCurrent = view.SourceCurrent && value.Status == "proposed" &&
			targetErr == nil && value.WorkspaceID == target.Workspace.ID
		view.Effect, view.WorkingDirectory = "file_review_required", "."
		add("operation", value.Operation)
		add("path", value.Path)
		if value.DestinationPath != "" {
			add("destination_path", value.DestinationPath)
		}
		view.Redacted = view.Redacted || value.SecretsRedacted
	case toolgateway.AgentBrowserApprovalTool:
		source, ok := a.store.(interface {
			GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
		})
		if !ok {
			return stale()
		}
		call, started, err := source.GetSupervisorApprovalCall(ctx, run.ID, record.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		authority, err := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(call.AuthorityJSON))
		var payload toolgateway.AgentBrowserPayload
		if err != nil || json.Unmarshal([]byte(call.PayloadJSON), &payload) != nil || payload.Sensitive == nil ||
			call.RunID != run.ID || call.CallID != record.ProposalID || authority.RunID != run.ID ||
			authority.SessionID != record.SessionID || authority.WorkspaceID != record.WorkspaceID ||
			record.RequestFingerprint != toolgateway.AgentBrowserApprovalFingerprint(call) ||
			record.ActionClass != "browser_"+payload.Sensitive.Effect || record.Mode != "per_call" {
			return stale()
		}
		view.Effect = "browser_sensitive_action"
		view.SourceCurrent = view.SourceCurrent && !started && call.Status == domain.SupervisorToolPending
		if a.agentBrowserController == nil {
			view.SourceCurrent = false
		} else {
			current, statusErr := a.agentBrowserController.GetStatus(ctx, run.ID)
			view.SourceCurrent = view.SourceCurrent && statusErr == nil && current.SessionID == authority.BrowserSessionID &&
				current.Capabilities.Available && current.DocumentEpoch == payload.Sensitive.DocumentEpoch &&
				current.CanonicalURL == payload.Sensitive.Target
		}
		add("operation", call.ToolName)
		add("url", payload.Sensitive.Target)
		add("summary", payload.Sensitive.Description)
		add("effect", payload.Sensitive.Effect)
	case "web_fetch":
		source, ok := a.store.(interface {
			GetWebFetchAuthorizationByApproval(context.Context, string) (domain.WebFetchAuthorization, error)
		})
		if !ok {
			return stale()
		}
		value, err := source.GetWebFetchAuthorizationByApproval(ctx, record.ID)
		if err != nil {
			return nil, nil, err
		}
		if value.ApprovalID != record.ID || value.RunID != record.RunID ||
			value.SessionID != record.SessionID || value.WorkspaceID != record.WorkspaceID ||
			value.RequestFingerprint != record.RequestFingerprint {
			return stale()
		}
		view.Effect = "fetch_public_https"
		view.SourceCurrent = view.SourceCurrent || (!run.Terminal() &&
			len(recoverableWebFetchApprovalActions(value)) > 0)
		add("url", value.CanonicalURL)
		add("host", value.ExactTarget)
	default:
		view.SourceCurrent = false
	}
	if view.Truncated {
		view.SourceCurrent = false
	}
	return view, nil, nil
}
