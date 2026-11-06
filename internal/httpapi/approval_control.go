package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

const ApprovalDecisionControlPathTemplate = "/api/v1/runs/{run_id}/approvals/{approval_id}/decision"

type ApprovalController interface {
	Decide(context.Context, application.DecideApprovalControlRequest) (
		application.DecideApprovalControlResult, error)
}

type WebFetchAuthorizationResumeController interface {
	ResumeWebFetchAuthorization(context.Context, string, string) (
		application.LifecycleResult, bool, error)
}

type ApprovalDecisionControlRequestView struct {
	Version         string                            `json:"version"`
	Action          application.ApprovalControlAction `json:"action"`
	Reason          string                            `json:"reason,omitempty"`
	GrantTTLSeconds int                               `json:"grant_ttl_seconds,omitempty"`
	GrantMaxUses    int                               `json:"grant_max_uses,omitempty"`
}

// This is an audit projection of the consumed grant, not execution authority.
type BoundedCommandGrantView struct {
	ID                        string    `json:"id"`
	ScopeFingerprint          string    `json:"scope_fingerprint"`
	TTLSeconds                int       `json:"ttl_seconds"`
	MaxUses                   int       `json:"max_uses"`
	UsesRemaining             int       `json:"uses_remaining"`
	UseOrdinal                int       `json:"use_ordinal"`
	ExpiresAt                 time.Time `json:"expires_at"`
	EachCommandRequiresReview bool      `json:"each_command_requires_review"`
}

type ApprovalDecisionControlView struct {
	Version                 string                                  `json:"version"`
	RunID                   string                                  `json:"run_id"`
	ApprovalID              string                                  `json:"approval_id"`
	ProposalID              string                                  `json:"proposal_id"`
	ToolName                string                                  `json:"tool_name"`
	Action                  application.ApprovalControlAction       `json:"action"`
	Status                  string                                  `json:"status"`
	Replayed                bool                                    `json:"replayed"`
	ProcessExecutionEnabled bool                                    `json:"process_execution_enabled"`
	ShellExecutionEnabled   bool                                    `json:"shell_execution_enabled"`
	DockerExecutionEnabled  bool                                    `json:"docker_execution_enabled"`
	WorkspaceWriteApplied   bool                                    `json:"workspace_write_applied"`
	SessionGrantCreated     bool                                    `json:"session_grant_created"`
	CapabilityGrant         bool                                    `json:"capability_grant"`
	ExecutionResumed        bool                                    `json:"execution_resumed"`
	RetryCompleted          bool                                    `json:"retry_completed"`
	RetryScheduled          bool                                    `json:"retry_scheduled"`
	Continuation            *application.ApprovalContinuationResult `json:"continuation,omitempty"`
	BoundedGrant            *BoundedCommandGrantView                `json:"bounded_grant,omitempty"`
}

func matchApprovalDecisionControlPath(requestPath string) (string, string, bool) {
	const prefix = "/api/v1/runs/"
	const middle = "/approvals/"
	const suffix = "/decision"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", "", false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	parts := strings.Split(value, middle)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.Contains(parts[0], "/") || strings.Contains(parts[1], "/") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (a *API) serveApprovalDecisionControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string, approvalID string,
) {
	if !a.authorizeRunOperation(writer, request, requestID,
		a.approvalControlEnabled, "Approval decision") {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validatePathIdentity(approvalID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, body, err := a.readRunOperationRequest(request, "Approval decision control")
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view ApprovalDecisionControlRequestView
	if err := decodeStrictRunOperation(body, &view, "Approval decision control"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := a.approvalController.Decide(request.Context(),
		application.DecideApprovalControlRequest{
			Version: view.Version, RunID: runID, ApprovalID: approvalID,
			Action: view.Action, OperationKey: operationKey,
			ReviewedBy: "http_approval_operator", Reason: view.Reason,
			GrantTTLSeconds: view.GrantTTLSeconds, GrantMaxUses: view.GrantMaxUses,
		})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	executionResumed, retryCompleted, retryScheduled := false, false, false
	var continuation *application.ApprovalContinuationResult
	var boundedGrant *BoundedCommandGrantView
	if result.Grant != nil && result.Consumption != nil && result.Grant.ExpiresAt != nil {
		boundedGrant = &BoundedCommandGrantView{ID: result.Grant.ID, ScopeFingerprint: result.Grant.ScopeFingerprint,
			TTLSeconds: int(result.Grant.ExpiresAt.Sub(result.Grant.CreatedAt) / time.Second), MaxUses: result.Grant.MaxUses,
			UsesRemaining: result.Grant.UsesRemaining, UseOrdinal: result.Consumption.UseOrdinal,
			ExpiresAt: *result.Grant.ExpiresAt, EachCommandRequiresReview: true}
	}
	if result.Approval.ToolName == toolgateway.AgentBrowserApprovalTool && !strings.HasPrefix(result.Approval.ProposalID, domain.BrowserNetworkRequestPrefix) {
		continuation = a.resumeReviewedProposal(request.Context(), result.Approval.RunID, "agent_browser", result.Approval.ProposalID)
	}
	if result.Approval.ToolName == string(toolgateway.MCPToolCallTool) {
		continuation = a.resumeReviewedProposal(request.Context(), result.Approval.RunID, "mcp", result.Approval.ProposalID)
	}
	if result.Approval.ToolName == string(toolgateway.CommandRuntimeTool) {
		continuation = a.resumeReviewedProposal(request.Context(), result.Approval.RunID, "command_runtime", result.Approval.ProposalID)
	}
	if result.Approval.ToolName == "web_fetch" &&
		a.webFetchAuthorizationSchedulerEnabled {
		_, ok := a.runExecutionController.(WebFetchAuthorizationResumeController)
		if !ok {
			a.writeError(writer, requestID, applicationWebFetchResumeUnavailable(), 0)
			return
		}
		// The decision is already durable. The process-owned reconciler observes
		// this queue and resumes it independently of the HTTP client. Never start
		// an untracked request-handler goroutine: desktop shutdown must be able to
		// cancel and join every continuation before SQLite closes.
		retryScheduled = true
	}
	a.writeSuccessStatus(writer, requestID, ApprovalDecisionControlView{
		Version: application.ApprovalControlProtocolVersion,
		RunID:   result.Approval.RunID, ApprovalID: result.Approval.ID,
		ProposalID: result.Approval.ProposalID, ToolName: result.Approval.ToolName,
		Action: result.Action, Status: string(result.Approval.Status),
		Replayed: result.Replayed, ExecutionResumed: executionResumed,
		RetryCompleted: retryCompleted, RetryScheduled: retryScheduled,
		Continuation:        continuation,
		SessionGrantCreated: result.GrantCreated, BoundedGrant: boundedGrant,
	}, nil, http.StatusAccepted)
}

func applicationWebFetchResumeUnavailable() error {
	return apperror.New(apperror.CodeFailedPrecondition,
		"web fetch authorization resume controller is unavailable")
}
