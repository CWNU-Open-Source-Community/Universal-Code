package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

type agentBrowserApprovalStore interface {
	GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
	EnsureApproval(context.Context, approval.Proposal) (approval.Record, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	DecideApproval(context.Context, approval.DecisionRequest) (approval.DecisionResult, error)
}

func recheckAgentBrowserApproval(ctx context.Context, base ApprovalControlStore, record approval.Record) error {
	if strings.HasPrefix(record.ProposalID, "specialist-tool-") {
		return recheckWorkerBrowserApproval(ctx, base, record)
	}
	st, ok := base.(agentBrowserApprovalStore)
	if !ok {
		return agentBrowserUnavailable("browser approval source unavailable")
	}
	call, started, e := st.GetSupervisorApprovalCall(ctx, record.RunID, record.ProposalID)
	if e != nil {
		return e
	}
	a, e := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(call.AuthorityJSON))
	if e != nil {
		return e
	}
	var p toolgateway.AgentBrowserPayload
	if json.Unmarshal([]byte(call.PayloadJSON), &p) != nil || p.Sensitive == nil || started || call.Status != domain.SupervisorToolPending || record.ToolName != toolgateway.AgentBrowserApprovalTool || record.Mode != "per_call" || record.SessionID != a.SessionID || record.WorkspaceID != a.WorkspaceID || record.ActionClass != "browser_"+p.Sensitive.Effect || record.RequestFingerprint != toolgateway.AgentBrowserApprovalFingerprint(call) {
		return agentBrowserUnavailable("browser approval source changed or was already dispatched")
	}
	return nil
}

// Preflight happens before the durable execution-started event. Waiting is a
// pending tool in the existing turn; it is not a completed model wait.
func (s *AgentRunner) preflightAgentBrowserApproval(ctx context.Context, call domain.SupervisorToolCall) (bool, *domain.SupervisorToolResult, error) {
	var p toolgateway.AgentBrowserPayload
	if json.Unmarshal([]byte(call.PayloadJSON), &p) != nil {
		return false, nil, agentBrowserUnavailable("invalid browser payload")
	}
	if p.Sensitive == nil {
		return false, nil, nil
	}
	a, e := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(call.AuthorityJSON))
	if e != nil {
		return false, nil, e
	}
	st, ok := s.store.(agentBrowserApprovalStore)
	if !ok {
		return false, nil, agentBrowserUnavailable("browser approval storage unavailable")
	}
	stored, started, e := st.GetSupervisorApprovalCall(ctx, call.RunID, call.CallID)
	if e != nil {
		return false, nil, e
	}
	if toolgateway.AgentBrowserApprovalFingerprint(stored) != toolgateway.AgentBrowserApprovalFingerprint(call) {
		return false, nil, agentBrowserUnavailable("browser approval call changed")
	}
	if started {
		return false, nil, nil
	} // only the fresh gate may handle this unknown outcome
	record, loadErr := st.GetApprovalByProposal(ctx, call.CallID)
	exists := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return false, nil, loadErr
	}
	if exists && (record.RunID != call.RunID || record.ToolName != toolgateway.AgentBrowserApprovalTool || record.SessionID != a.SessionID || record.WorkspaceID != a.WorkspaceID || record.Mode != "per_call" || record.GrantID != "" || record.RequestFingerprint != toolgateway.AgentBrowserApprovalFingerprint(call)) {
		return false, nil, agentBrowserUnavailable("browser approval decision identity changed")
	}
	if exists && record.Status == approval.StatusDenied {
		r := agentBrowserPreflightResult(call, "policy_denied", "The operator denied this exact browser action. It was not dispatched.", domain.SupervisorToolDenied)
		return false, &r, nil
	}
	valid := s.agentBrowser != nil && s.agentBrowser.check(ctx, a) == nil
	if valid {
		s.agentBrowser.mu.Lock()
		slot := s.agentBrowser.slots[call.RunID]
		valid = slot != nil && slot.runtime != nil
		if valid {
			status := slot.runtime.Status()
			valid = status.DocumentEpoch == p.Sensitive.DocumentEpoch && status.CanonicalURL == p.Sensitive.Target && status.State != "closed" && status.State != "closing" && status.State != "cleanup_pending"
		}
		s.agentBrowser.mu.Unlock()
	}
	if !valid {
		r := agentBrowserPreflightResult(call, "browser_authority_expired", "The browser session or document changed before dispatch. This exact action was not sent. Obtain a current snapshot and reassess the task before proposing a new action.", domain.SupervisorToolFailed)
		return false, &r, nil
	}
	if !exists {
		now := time.Now().UTC()
		record, e = st.EnsureApproval(ctx, approval.Proposal{IdempotencyKey: approval.ProposalIdempotencyKey(toolgateway.AgentBrowserApprovalTool, call.CallID), ProposalID: call.CallID, SessionID: a.SessionID, WorkspaceID: a.WorkspaceID, ToolName: toolgateway.AgentBrowserApprovalTool, ActionClass: "browser_" + p.Sensitive.Effect, Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: toolgateway.AgentBrowserApprovalFingerprint(call), DecisionReason: p.Sensitive.Description, RequestedBy: "run_supervisor", CreatedAt: now, UpdatedAt: now})
		if e != nil {
			return false, nil, e
		}
	}
	return record.Status != approval.StatusApproved, nil, nil
}
func agentBrowserPreflightResult(call domain.SupervisorToolCall, code, message string, status domain.SupervisorToolCallStatus) domain.SupervisorToolResult {
	b, _ := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion, Tool: call.ToolName, Status: string(status), Code: code, Message: message, Metadata: map[string]string{"browser_preflight": "not_dispatched", "browser_source_fingerprint": toolgateway.AgentBrowserApprovalFingerprint(call)}})
	return domain.SupervisorToolResult{CallID: call.CallID, Status: status, ErrorCode: code, ResultJSON: string(b), CompletedAt: time.Now().UTC()}
}

func agentBrowserStoppedResult(call domain.SupervisorToolCall, code, message string, status domain.SupervisorToolCallStatus) domain.SupervisorToolResult {
	b, _ := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion, Tool: call.ToolName, Status: string(status), Code: code, Message: message})
	return domain.SupervisorToolResult{CallID: call.CallID, Status: status, ResultJSON: string(b), ErrorCode: code, CompletedAt: time.Now().UTC()}
}
func (s *AgentBrowserService) checkDurableDispatch(ctx context.Context, scope toolgateway.AgentBrowserExecutionScope, canonical json.RawMessage) error {
	if scope.Call.RequestedBy == "specialist_runner" {
		return s.checkWorkerDurableBrowserDispatch(ctx, scope, canonical)
	}
	st, ok := s.store.(agentBrowserApprovalStore)
	if !ok {
		return agentBrowserUnavailable("durable browser call storage unavailable")
	}
	leases, ok := s.store.(RunExecutionLeaseStore)
	if !ok {
		return agentBrowserUnavailable("browser dispatch requires live Run lease storage")
	}
	lease, found, e := leases.GetRunExecutionLease(ctx, scope.Authority.RunID)
	if e != nil {
		return e
	}
	if !found || lease.Status != domain.RunExecutionLeaseActive || lease.LeaseID != scope.Call.LeaseID || lease.Generation != scope.Call.LeaseGeneration || !lease.ExpiresAt.After(time.Now()) {
		return agentBrowserUnavailable("browser dispatch Run lease is no longer current")
	}
	c, started, e := st.GetSupervisorApprovalCall(ctx, scope.Authority.RunID, scope.Call.SupervisorToolCallID)
	if e != nil {
		return e
	}
	encoded, _ := json.Marshal(scope.Authority)
	if !started || c.Status != domain.SupervisorToolPending || c.AgentID != scope.Authority.RootAgentID || c.AgentAttemptID != scope.Call.AgentAttemptID || c.Turn != scope.Call.SupervisorTurn || c.ToolName != string(scope.Call.Name) || c.PayloadJSON != string(canonical) || c.AuthorityJSON != string(encoded) {
		return agentBrowserUnavailable("browser dispatch requires its exact started durable call")
	}
	var p toolgateway.AgentBrowserPayload
	_ = json.Unmarshal(canonical, &p)
	if p.Sensitive != nil {
		record, e := st.GetApprovalByProposal(ctx, c.CallID)
		if e != nil {
			return e
		}
		if record.Status != approval.StatusApproved || record.ToolName != toolgateway.AgentBrowserApprovalTool || record.RequestFingerprint != toolgateway.AgentBrowserApprovalFingerprint(c) || record.RunID != c.RunID || record.GrantID != "" {
			return agentBrowserUnavailable("sensitive browser action lacks its exact approval")
		}
	}
	return nil
}
