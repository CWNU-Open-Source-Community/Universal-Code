package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func workerBrowserApprovalMatches(record approval.Record, c domain.SpecialistToolCall, a toolgateway.AgentBrowserCallAuthority, p toolgateway.AgentBrowserPayload) bool {
	return p.Sensitive != nil && record.RunID == c.Ref.RunID && record.ProposalID == domain.SpecialistToolApprovalID(c.Ref, c.CallID) &&
		record.SessionID == a.SessionID && record.WorkspaceID == a.WorkspaceID && record.ToolName == toolgateway.AgentBrowserApprovalTool &&
		record.ActionClass == "browser_"+p.Sensitive.Effect && record.Mode == "per_call" && record.GrantID == "" &&
		record.RequestFingerprint == domain.SpecialistToolApprovalFingerprint(c)
}

func recheckWorkerBrowserApproval(ctx context.Context, base any, record approval.Record) error {
	st, ok := base.(workerToolSourceStore)
	if !ok {
		return agentBrowserUnavailable("worker browser approval source unavailable")
	}
	c, started, err := st.GetSpecialistApprovalCall(ctx, record.RunID, record.ProposalID)
	if err != nil {
		return err
	}
	a, err := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return err
	}
	var p toolgateway.AgentBrowserPayload
	if json.Unmarshal([]byte(c.PayloadJSON), &p) != nil || started || c.Status != domain.SupervisorToolPending || !workerBrowserApprovalMatches(record, c, a, p) {
		return agentBrowserUnavailable("worker browser approval source changed or was dispatched")
	}
	return nil
}

func (s *AgentRunner) preflightWorkerBrowserApproval(ctx context.Context, c domain.SpecialistToolCall) (bool, *domain.SupervisorToolResult, error) {
	var p toolgateway.AgentBrowserPayload
	if json.Unmarshal([]byte(c.PayloadJSON), &p) != nil {
		return false, nil, agentBrowserUnavailable("invalid worker browser payload")
	}
	if p.Sensitive == nil || c.StartedAt != nil {
		return false, nil, nil
	}
	a, err := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return false, nil, err
	}
	st, ok := s.store.(agentBrowserApprovalStore)
	if !ok || s.agentBrowser == nil {
		return false, nil, agentBrowserUnavailable("worker browser approval unavailable")
	}
	stop := func(code, message string, status domain.SupervisorToolCallStatus) (bool, *domain.SupervisorToolResult, error) {
		raw, _ := json.Marshal(map[string]string{"status": string(status), "code": code, "message": message})
		return false, &domain.SupervisorToolResult{CallID: c.CallID, Status: status, ErrorCode: code, ResultJSON: string(raw), CompletedAt: time.Now().UTC()}, nil
	}
	if err := s.agentBrowser.check(ctx, a); err != nil {
		return stop("browser_authority_expired", "Worker browser authority changed before dispatch.", domain.SupervisorToolFailed)
	}
	s.agentBrowser.mu.Lock()
	slot := s.agentBrowser.slots[agentBrowserSlotKey(a)]
	valid := slot != nil && slot.runtime != nil
	if valid {
		status := slot.runtime.Status()
		valid = status.DocumentEpoch == p.Sensitive.DocumentEpoch && status.CanonicalURL == p.Sensitive.Target && status.State != "closed" && status.State != "closing" && status.State != "cleanup_pending"
	}
	s.agentBrowser.mu.Unlock()
	if !valid {
		return stop("browser_document_changed", "The worker browser document changed; obtain a current snapshot before proposing an action.", domain.SupervisorToolFailed)
	}
	proposalID := domain.SpecialistToolApprovalID(c.Ref, c.CallID)
	record, err := st.GetApprovalByProposal(ctx, proposalID)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC()
		record, err = st.EnsureApproval(ctx, approval.Proposal{IdempotencyKey: approval.ProposalIdempotencyKey(toolgateway.AgentBrowserApprovalTool, proposalID), ProposalID: proposalID,
			SessionID: a.SessionID, WorkspaceID: a.WorkspaceID, ToolName: toolgateway.AgentBrowserApprovalTool, ActionClass: "browser_" + p.Sensitive.Effect,
			Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: domain.SpecialistToolApprovalFingerprint(c), DecisionReason: p.Sensitive.Description,
			RequestedBy: "specialist_runner", CreatedAt: now, UpdatedAt: now})
	}
	if err != nil {
		return false, nil, err
	}
	if !workerBrowserApprovalMatches(record, c, a, p) {
		return false, nil, agentBrowserUnavailable("worker browser approval identity changed")
	}
	if record.Status == approval.StatusDenied {
		return stop("policy_denied", "The operator denied this worker browser action; it was not dispatched.", domain.SupervisorToolDenied)
	}
	return record.Status != approval.StatusApproved, nil, nil
}

func (s *AgentBrowserService) checkWorkerDurableBrowserDispatch(ctx context.Context, scope toolgateway.AgentBrowserExecutionScope, canonical json.RawMessage) error {
	st, ok := s.store.(workerToolSourceStore)
	if !ok {
		return agentBrowserUnavailable("worker browser intent storage unavailable")
	}
	a, call := scope.Authority, scope.Call
	raw, _ := json.Marshal(a)
	c, err := st.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{Ref: domain.AgentAttemptRef{RunID: call.RunID, AgentID: call.AgentID, AttemptID: call.AgentAttemptID},
		CallID: call.SpecialistToolCallID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID, Name: string(call.Name), PayloadJSON: string(canonical), AuthorityJSON: string(raw),
		ChargeID: call.InvocationID, LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration, RequireStarted: true})
	if err != nil {
		return err
	}
	worker, err := readWorkerExecutionContext(ctx, s.store, c.Ref, s.drydocks)
	if err != nil {
		return err
	}
	if !worker.Child.Worker.AllowsTool(c.Name) || workerResourceFingerprint(worker.Binding) != a.WorkspaceFingerprint {
		return agentBrowserUnavailable("worker browser capability changed")
	}
	var p toolgateway.AgentBrowserPayload
	_ = json.Unmarshal(canonical, &p)
	if p.Sensitive != nil {
		approvals, ok := s.store.(agentBrowserApprovalStore)
		if !ok {
			return agentBrowserUnavailable("worker browser approval storage unavailable")
		}
		record, err := approvals.GetApprovalByProposal(ctx, domain.SpecialistToolApprovalID(c.Ref, c.CallID))
		if err != nil {
			return err
		}
		if record.Status != approval.StatusApproved || !workerBrowserApprovalMatches(record, c, a, p) {
			return agentBrowserUnavailable("worker browser action lacks its exact approval")
		}
	}
	return nil
}
