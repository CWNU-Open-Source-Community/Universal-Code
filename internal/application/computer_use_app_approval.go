package application

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
)

type ComputerUseApprovalTarget = approval.ComputerUseTarget

type computerUseAppApprovalStore interface {
	DecideMCPComputerUseAppApproval(context.Context, approval.DecisionRequest) (approval.DecisionResult, approval.SessionGrant, bool, error)
	TryAuthorizeMCPComputerUseAppApproval(context.Context, string) (approval.Record, bool, error)
	CheckMCPComputerUseAppGrant(context.Context, string) error
}

func mcpComputerUseTarget(source mcpApprovalSource) (*ComputerUseApprovalTarget, error) {
	if !mcp.IsComputerUseServer(source.server.Descriptor) {
		return nil, nil
	}
	if source.payload.Method != "" && source.payload.Method != "tools/call" {
		return nil, mcpApprovalUnavailable("Computer Use only exposes its pinned tools")
	}
	target, err := approval.ParseComputerUseTarget(source.payload.ToolName, source.payload.Arguments)
	if err != nil {
		return nil, mcpApprovalUnavailable("Computer Use target or key chord is invalid")
	}
	return &target, nil
}

// MCPComputerUseApprovalPreview reads the same immutable target used at
// dispatch. Executable is a display path; the complete AppID is the identity.
func MCPComputerUseApprovalPreview(ctx context.Context, base ApprovalControlStore, record approval.Record) (*ComputerUseApprovalTarget, error) {
	st, ok := base.(mcpApprovalStore)
	if !ok {
		return nil, mcpApprovalUnavailable("MCP approval source reader unavailable")
	}
	source, err := readMCPApprovalSource(ctx, st, record.RunID, record.ProposalID)
	if err != nil {
		return nil, err
	}
	if source.started || !mcpApprovalMatches(record, source) {
		return nil, mcpApprovalUnavailable("MCP approval source changed or was already dispatched")
	}
	return mcpComputerUseTarget(source)
}

func MCPApprovalDecisionActions(ctx context.Context, base ApprovalControlStore, record approval.Record, runTerminal bool) []ApprovalControlAction {
	actions := ApprovalDecisionActions(record, runTerminal)
	if len(actions) == 0 || record.ToolName != mcp.OperationApprovalTool {
		return actions
	}
	target, err := MCPComputerUseApprovalPreview(ctx, base, record)
	if err != nil || target == nil || target.AppID == "" || target.SystemShortcut {
		return actions
	}
	permission, err := base.(mcpApprovalStore).GetRunExecutionPermission(ctx, record.RunID)
	if err != nil || permission.Mode != domain.RunExecutionPermissionAsk {
		return actions
	}
	return []ApprovalControlAction{ApprovalControlApproveOnce, ApprovalControlApproveForRunApp, ApprovalControlDeny}
}

func (s *ApprovalControlService) decideComputerUseApp(ctx context.Context, run domain.Run, record approval.Record, request DecideApprovalControlRequest) (DecideApprovalControlResult, error) {
	var result DecideApprovalControlResult
	st, ok := s.store.(computerUseAppApprovalStore)
	if !ok || request.Action != ApprovalControlApproveForRunApp {
		return result, mcpApprovalUnavailable("this decision requires its exact Computer Use application approval action")
	}
	if record.Status == approval.StatusPending {
		if run.Terminal() {
			return result, mcpApprovalUnavailable("terminal Run approvals cannot be changed")
		}
		if err := s.recheckApprovalSource(ctx, record); err != nil {
			return result, err
		}
		target, err := MCPComputerUseApprovalPreview(ctx, s.store, record)
		if err != nil || target == nil || target.AppID == "" {
			return result, mcpApprovalUnavailable("task application approval requires the exact built-in application target")
		}
		if target.SystemShortcut {
			return result, mcpApprovalUnavailable("system shortcuts require an exact single-call approval")
		}
		permission, err := s.store.(mcpApprovalStore).GetRunExecutionPermission(ctx, run.ID)
		if err != nil {
			return result, err
		}
		if permission.Mode != domain.RunExecutionPermissionAsk {
			return result, mcpApprovalUnavailable("task application approval is available only in Ask mode")
		}
	}
	decision, grant, created, err := st.DecideMCPComputerUseAppApproval(ctx, approval.DecisionRequest{
		ProposalID: record.ProposalID, IdempotencyKey: request.OperationKey, Action: approval.ActionApprove, ReviewedBy: request.ReviewedBy})
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if decision.Approval.ID != record.ID || decision.Approval.Status != approval.StatusApproved || decision.Approval.GrantID != grant.ID {
		return result, apperror.New(apperror.CodeInternal, "Computer Use approval lost its application grant binding")
	}
	return DecideApprovalControlResult{Approval: decision.Approval, Action: request.Action, Replayed: decision.Replayed,
		Grant: &grant, GrantCreated: created}, nil
}

func checkMCPComputerUseAppGrant(ctx context.Context, base any, record approval.Record) error {
	if record.GrantID == "" {
		return nil
	}
	st, ok := base.(computerUseAppApprovalStore)
	if !ok {
		return errors.New("Computer Use application grant verifier unavailable")
	}
	return st.CheckMCPComputerUseAppGrant(ctx, record.ProposalID)
}
