package application

import (
	"context"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/toolrun"
	"cyberagent-workbench/internal/tools"
)

const (
	ApprovalControlProtocolVersion = "approval_control.v1"
	ApprovalQueueProtocolVersion   = "approval_queue.v1"
	MaxApprovalQueueItems          = 100
)

type ApprovalControlAction string

const (
	ApprovalControlApproveOnce      ApprovalControlAction = "approve_once"
	ApprovalControlApproveForThread ApprovalControlAction = "approve_for_thread"
	ApprovalControlApproveForRun    ApprovalControlAction = "approve_for_run"
	ApprovalControlApproveForRunApp ApprovalControlAction = "approve_for_run_app"
	ApprovalControlDeny             ApprovalControlAction = "deny"
)

type ApprovalControlStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetApproval(context.Context, string) (approval.Record, error)
	ListApprovals(context.Context, approval.ListFilter) ([]approval.Record, error)
	GetToolRun(context.Context, string) (toolrun.ToolRun, error)
	GetScriptProcess(context.Context, string) (scriptprocess.Process, error)
}

type ApprovalReviewer interface {
	Review(context.Context, toolgateway.ReviewRequest) (toolgateway.Outcome, error)
}

type gitAdvancedApprovalControlStore interface {
	GetGitAdvancedOperation(context.Context, string) (gitadvanced.OperationRecord, bool, error)
	DecideApproval(context.Context, approval.DecisionRequest) (approval.DecisionResult, error)
}

type webFetchApprovalControlStore interface {
	GetWebFetchAuthorizationByApproval(context.Context, string) (
		domain.WebFetchAuthorization, error)
	DecideWebFetchAuthorization(context.Context, string,
		domain.WebFetchAuthorizationScope, bool, string, string, string) (
		domain.WebFetchAuthorization, bool, error)
}

type ApprovalControlService struct {
	store    ApprovalControlStore
	reviewer ApprovalReviewer
	checker  policy.Checker
}

type DecideApprovalControlRequest struct {
	Version         string
	RunID           string
	ApprovalID      string
	Action          ApprovalControlAction
	OperationKey    string
	ReviewedBy      string
	Reason          string
	GrantTTLSeconds int
	GrantMaxUses    int
}

type DecideApprovalControlResult struct {
	Approval     approval.Record
	Action       ApprovalControlAction
	Replayed     bool
	Grant        *approval.SessionGrant
	Consumption  *approval.GrantConsumption
	GrantCreated bool
}

func NewApprovalControlService(store ApprovalControlStore, reviewer ApprovalReviewer,
	checker policy.Checker,
) *ApprovalControlService {
	return &ApprovalControlService{store: store, reviewer: reviewer, checker: checker}
}

func (s *ApprovalControlService) Decide(ctx context.Context,
	request DecideApprovalControlRequest,
) (DecideApprovalControlResult, error) {
	if s == nil || s.store == nil || s.reviewer == nil || s.checker == nil {
		return DecideApprovalControlResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "approval control dependencies are required")
	}
	if err := normalizeApprovalControlRequest(&request); err != nil {
		return DecideApprovalControlResult{}, err
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return DecideApprovalControlResult{}, apperror.Normalize(err)
	}
	record, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return DecideApprovalControlResult{}, apperror.Normalize(err)
	}
	if record.RunID != run.ID {
		return DecideApprovalControlResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"approval is not an ungranted request for the requested Run")
	}
	if record.Mode == string(toolgateway.ApprovalNever) {
		return DecideApprovalControlResult{}, apperror.New(
			apperror.CodePolicyDenied, "permanent Policy denial cannot be overridden")
	}
	if record.ToolName == string(toolgateway.CommandRuntimeTool) && (request.Action == ApprovalControlApproveForRun || record.GrantID != "") {
		return s.decideBoundedCommand(ctx, run, record, request)
	}
	if record.ToolName == mcp.OperationApprovalTool && (request.Action == ApprovalControlApproveForRunApp || record.GrantID != "") {
		return s.decideComputerUseApp(ctx, run, record, request)
	}
	if record.GrantID != "" {
		return DecideApprovalControlResult{}, apperror.New(apperror.CodeFailedPrecondition, "approval already belongs to a session grant")
	}
	expected := approval.StatusDenied
	reviewAction := toolgateway.ReviewDeny
	approve := request.Action == ApprovalControlApproveOnce ||
		request.Action == ApprovalControlApproveForThread
	if approve {
		expected = approval.StatusApproved
		reviewAction = toolgateway.ReviewApprove
	}
	if record.Status != approval.StatusPending && record.Status != expected {
		return DecideApprovalControlResult{}, apperror.New(apperror.CodeConflict,
			"approval was already decided with a different outcome")
	}
	if record.Status == approval.StatusPending && run.Terminal() {
		return DecideApprovalControlResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "terminal Run approvals cannot be changed")
	}
	if approve && record.Status == approval.StatusPending {
		if err := s.recheckApprovalSource(ctx, record); err != nil {
			return DecideApprovalControlResult{}, err
		}
	}
	if !approvalActionSupported(record, request.Action) {
		return DecideApprovalControlResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"approval action is not available through this control surface")
	}
	replayed := record.Status == expected
	if record.ToolName == string(toolgateway.WebFetchTool) {
		webStore, ok := s.store.(webFetchApprovalControlStore)
		if !ok {
			return DecideApprovalControlResult{}, apperror.New(
				apperror.CodeFailedPrecondition, "Web fetch approval control is unavailable")
		}
		value, loadErr := webStore.GetWebFetchAuthorizationByApproval(ctx, record.ID)
		if loadErr != nil {
			return DecideApprovalControlResult{}, apperror.Normalize(loadErr)
		}
		scope := domain.WebFetchAuthorizationOnce
		if request.Action == ApprovalControlApproveForThread {
			scope = domain.WebFetchAuthorizationThread
		}
		_, _, err = webStore.DecideWebFetchAuthorization(ctx, value.ID, scope, approve,
			request.OperationKey, request.ReviewedBy, request.Reason)
	} else if record.ToolName == toolgateway.AgentBrowserApprovalTool || record.ToolName == mcp.OperationApprovalTool || record.ToolName == string(toolgateway.CommandRuntimeTool) {
		st, ok := s.store.(interface {
			DecideApproval(context.Context, approval.DecisionRequest) (approval.DecisionResult, error)
		})
		if !ok {
			return DecideApprovalControlResult{}, mcpApprovalUnavailable("tool approval store unavailable")
		}
		action := approval.ActionDeny
		if request.Action == ApprovalControlApproveOnce {
			action = approval.ActionApprove
		}
		_, err = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: record.ProposalID, IdempotencyKey: request.OperationKey, Action: action, Reason: request.Reason, ReviewedBy: request.ReviewedBy})
	} else if record.ToolName == ThreadPullRequestApprovalTool {
		prStore, ok := s.store.(threadPullRequestApprovalStore)
		if !ok {
			return DecideApprovalControlResult{}, apperror.New(apperror.CodeFailedPrecondition, "task pull request approval store is unavailable")
		}
		action := approval.ActionDeny
		if request.Action == ApprovalControlApproveOnce {
			action = approval.ActionApprove
		}
		_, err = prStore.DecideApproval(ctx, approval.DecisionRequest{ProposalID: record.ProposalID, IdempotencyKey: request.OperationKey, Action: action, Reason: request.Reason, ReviewedBy: request.ReviewedBy})
	} else if record.ToolName == gitadvanced.ApprovalToolName {
		advancedStore, ok := s.store.(gitAdvancedApprovalControlStore)
		if !ok {
			return DecideApprovalControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"Git advanced approval control is unavailable")
		}
		action := approval.ActionDeny
		if request.Action == ApprovalControlApproveOnce {
			action = approval.ActionApprove
		}
		_, err = advancedStore.DecideApproval(ctx, approval.DecisionRequest{
			ProposalID: record.ProposalID, IdempotencyKey: request.OperationKey,
			Action: action, Reason: request.Reason, ReviewedBy: request.ReviewedBy,
		})
	} else {
		_, err = s.reviewer.Review(ctx, toolgateway.ReviewRequest{
			Action: reviewAction, Tool: toolgateway.ToolName(record.ToolName),
			ProposalID: record.ProposalID, IdempotencyKey: request.OperationKey,
			ReviewedBy: request.ReviewedBy, Reason: request.Reason,
		})
	}
	if err != nil {
		return DecideApprovalControlResult{}, apperror.Normalize(err)
	}
	stored, err := s.store.GetApproval(ctx, record.ID)
	if err != nil {
		return DecideApprovalControlResult{}, apperror.Normalize(err)
	}
	if stored.RunID != run.ID || stored.ProposalID != record.ProposalID ||
		stored.ToolName != record.ToolName || stored.ActionClass != record.ActionClass ||
		stored.Status != expected || stored.GrantID != "" {
		return DecideApprovalControlResult{}, apperror.New(apperror.CodeInternal,
			"approval decision result violated its exact binding")
	}
	return DecideApprovalControlResult{Approval: stored, Action: request.Action,
		Replayed: replayed}, nil
}

func (s *ApprovalControlService) recheckApprovalSource(ctx context.Context,
	record approval.Record,
) error {
	var decision policy.Decision
	switch record.ToolName {
	case string(toolgateway.CommandRuntimeTool):
		if err := RecheckCommandApproval(ctx, s.store, record); err != nil {
			return err
		}
		source, err := readCommandApprovalSource(ctx, s.store.(commandApprovalStore), record.RunID, record.ProposalID)
		if err != nil {
			return err
		}
		decision = toolgateway.CommandRuntimePolicyDecision(s.checker, source.input)
	case mcp.OperationApprovalTool:
		if err := RecheckMCPApproval(ctx, s.store, record); err != nil {
			return err
		}
		st := s.store.(mcpApprovalStore)
		source, err := readMCPApprovalSource(ctx, st, record.RunID, record.ProposalID)
		if err != nil {
			return err
		}
		p := source.payload
		decision = s.checker.CheckToolCall(tools.Call{Name: mcp.OperationApprovalTool, Args: map[string]string{
			"server_id": p.ServerID, "tool_name": p.ToolName, "capability_fingerprint": p.CapabilityFingerprint, "arguments": string(p.Arguments)}})
	case toolgateway.AgentBrowserApprovalTool:
		return recheckAgentBrowserApproval(ctx, s.store, record)
	case ThreadPullRequestApprovalTool:
		return recheckThreadPullRequestApproval(ctx, s.store, record)
	case string(toolgateway.ShellTool):
		proposal, err := s.store.GetToolRun(ctx, record.ProposalID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if proposal.ID != record.ProposalID || proposal.SessionID != record.SessionID ||
			proposal.WorkspaceID != record.WorkspaceID || proposal.Status != toolrun.StatusProposed ||
			record.RequestFingerprint != approval.ShellFingerprint(proposal.SessionID, proposal.WorkspaceID, proposal.Command) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Shell approval source changed or is no longer pending")
		}
		decision = s.checker.CheckToolCall(tools.Call{Name: string(toolgateway.ShellTool),
			Args: map[string]string{"command": proposal.Command}})
	case string(toolgateway.ScriptProcessTool):
		proposal, err := s.store.GetScriptProcess(ctx, record.ProposalID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if proposal.ID != record.ProposalID || proposal.RunID != record.RunID ||
			proposal.SessionID != record.SessionID || proposal.WorkspaceID != record.WorkspaceID ||
			proposal.Status != scriptprocess.StatusProposed ||
			proposal.ExecutionMode != scriptprocess.ExecutionDisabled ||
			proposal.ApprovalFingerprint != record.RequestFingerprint {
			return apperror.New(apperror.CodeFailedPrecondition,
				"ScriptProcess approval source changed or is not process-disabled")
		}
		payload, err := scriptprocess.EncodeProposal(proposal.Proposal())
		if err != nil {
			return apperror.Wrap(apperror.CodeFailedPrecondition,
				"ScriptProcess approval source is invalid", err)
		}
		decision = s.checker.CheckToolCall(tools.Call{Name: string(toolgateway.ScriptProcessTool),
			Args: map[string]string{"proposal": payload}})
	case gitadvanced.ApprovalToolName:
		advancedStore, ok := s.store.(gitAdvancedApprovalControlStore)
		if !ok {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Git advanced approval source is unavailable")
		}
		proposal, found, err := advancedStore.GetGitAdvancedOperation(ctx, record.ProposalID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if !found || proposal.ID != record.ProposalID || proposal.RunID != record.RunID ||
			proposal.SessionID != record.SessionID || proposal.WorkspaceID != record.WorkspaceID ||
			proposal.Status != gitadvanced.OperationProposed ||
			proposal.ApprovalFingerprint != record.RequestFingerprint ||
			proposal.ApprovalID != "" {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Git advanced approval source changed or is no longer pending")
		}
		return nil // Execution rechecks capability, permission, lease, and repository drift.
	case string(toolgateway.WebFetchTool):
		webStore, ok := s.store.(webFetchApprovalControlStore)
		if !ok {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Web fetch approval source is unavailable")
		}
		value, err := webStore.GetWebFetchAuthorizationByApproval(ctx, record.ID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if value.ApprovalID != record.ID || value.RunID != record.RunID ||
			value.SessionID != record.SessionID || value.WorkspaceID != record.WorkspaceID ||
			value.Status != domain.WebFetchAuthorizationPending ||
			value.RequestFingerprint != record.RequestFingerprint {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Web fetch approval source changed or is no longer pending")
		}
		return nil // Execution rechecks public HTTPS, DNS and redirects after approval.
	default:
		return apperror.New(apperror.CodeFailedPrecondition,
			"approve-once is limited to dry-run Shell and ScriptProcess proposals")
	}
	if !decision.Allowed {
		return apperror.New(apperror.CodePolicyDenied,
			"approval source is permanently denied by current Policy")
	}
	return nil
}

func ApprovalDecisionActions(record approval.Record, runTerminal bool) []ApprovalControlAction {
	if runTerminal || record.Status != approval.StatusPending || record.GrantID != "" ||
		record.Mode == string(toolgateway.ApprovalNever) {
		return []ApprovalControlAction{}
	}
	switch record.ToolName {
	case string(toolgateway.CommandRuntimeTool):
		// Bounded Run review is advertised only after reading the durable source
		// and verifying that it contains a declared risk scope.
		return []ApprovalControlAction{ApprovalControlApproveOnce, ApprovalControlDeny}
	case string(toolgateway.ShellTool), string(toolgateway.ScriptProcessTool),
		gitadvanced.ApprovalToolName, ThreadPullRequestApprovalTool, toolgateway.AgentBrowserApprovalTool, mcp.OperationApprovalTool:
		return []ApprovalControlAction{ApprovalControlApproveOnce, ApprovalControlDeny}
	case string(toolgateway.WebFetchTool):
		return []ApprovalControlAction{ApprovalControlApproveOnce,
			ApprovalControlApproveForThread, ApprovalControlDeny}
	case string(toolgateway.ReplaceFileTool):
		return []ApprovalControlAction{ApprovalControlDeny}
	default:
		return []ApprovalControlAction{}
	}
}

func approvalActionSupported(record approval.Record, action ApprovalControlAction) bool {
	for _, supported := range ApprovalDecisionActions(record, false) {
		if supported == action {
			return true
		}
	}
	// Idempotent replay is still constrained to the same tool classes.
	if record.Status != approval.StatusPending {
		switch record.ToolName {
		case string(toolgateway.ShellTool), string(toolgateway.ScriptProcessTool),
			gitadvanced.ApprovalToolName, ThreadPullRequestApprovalTool, toolgateway.AgentBrowserApprovalTool, mcp.OperationApprovalTool, string(toolgateway.CommandRuntimeTool):
			return action == ApprovalControlApproveOnce || action == ApprovalControlDeny
		case string(toolgateway.ReplaceFileTool):
			return action == ApprovalControlDeny
		case string(toolgateway.WebFetchTool):
			return action == ApprovalControlApproveOnce ||
				action == ApprovalControlApproveForThread || action == ApprovalControlDeny
		}
	}
	return false
}

func normalizeApprovalControlRequest(request *DecideApprovalControlRequest) error {
	if request == nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"approval control request is required")
	}
	if request.Version != ApprovalControlProtocolVersion {
		return apperror.New(apperror.CodeInvalidArgument,
			"unsupported approval control version")
	}
	for label, value := range map[string]string{
		"Run id": request.RunID, "approval id": request.ApprovalID,
		"reviewer": request.ReviewedBy,
	} {
		if value != strings.TrimSpace(value) || !domain.ValidAgentID(value) ||
			strings.ContainsRune(value, 0) {
			return apperror.New(apperror.CodeInvalidArgument,
				"approval control "+label+" is invalid")
		}
	}
	operationKey, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || operationKey != request.OperationKey || containsSpaceOrControl(operationKey) {
		return apperror.New(apperror.CodeInvalidArgument,
			"approval control idempotency key is invalid")
	}
	if request.Action != ApprovalControlApproveOnce &&
		request.Action != ApprovalControlApproveForThread &&
		request.Action != ApprovalControlApproveForRun &&
		request.Action != ApprovalControlApproveForRunApp &&
		request.Action != ApprovalControlDeny {
		return apperror.New(apperror.CodeInvalidArgument,
			"approval control action must be approve_once, approve_for_thread, approve_for_run, approve_for_run_app, or deny")
	}
	if request.Action == ApprovalControlApproveForRun {
		if request.GrantTTLSeconds < 1 || request.GrantTTLSeconds > 900 || request.GrantMaxUses < 1 || request.GrantMaxUses > 8 {
			return apperror.New(apperror.CodeInvalidArgument, "bounded Run approval requires explicit TTL (1..900 seconds) and uses (1..8)")
		}
	} else if request.GrantTTLSeconds != 0 || request.GrantMaxUses != 0 {
		return apperror.New(apperror.CodeInvalidArgument, "only bounded Run approval can include grant limits")
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if (request.Action == ApprovalControlApproveOnce ||
		request.Action == ApprovalControlApproveForThread || request.Action == ApprovalControlApproveForRunApp) && request.Reason != "" {
		return apperror.New(apperror.CodeInvalidArgument,
			"approve-once cannot include a denial reason")
	}
	if request.Action == ApprovalControlDeny && request.Reason == "" {
		request.Reason = "denied by operator"
	}
	if _, err := (approval.DecisionRequest{ProposalID: request.ApprovalID,
		IdempotencyKey: operationKey, Action: approval.ActionDeny,
		Reason: request.Reason, ReviewedBy: request.ReviewedBy}).Normalize(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"approval control request is invalid", err)
	}
	request.OperationKey = operationKey
	return nil
}
