package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolgateway"
)

type workerDispatchStore interface {
	specialistToolStore
	BindSpecialistToolAuthority(context.Context, domain.AgentAttemptRef, string, string) (domain.SpecialistToolCall, error)
	SetSpecialistToolApproval(context.Context, domain.AgentAttemptRef, string, string) (domain.SpecialistToolCall, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
}

func (r *SubagentRunner) resolveWorkerTools(ctx context.Context, result *SpecialistTurnResult, run domain.Run, mission domain.Mission, child domain.AgentNode, attempt domain.AgentAttempt, rounds []domain.SpecialistToolRound, executor *specialistWorkspaceExecutor, gateway *toolgateway.Gateway) (bool, error) {
	st, ok := r.store.(workerDispatchStore)
	if !ok {
		return false, errors.New("worker durable tool storage is unavailable")
	}
	result.ToolRounds, result.ToolCalls = len(rounds), 0
	for _, round := range rounds {
		for _, c := range round.Calls {
			if c.Status.Terminal() {
				result.ToolCalls++
				continue
			}
			if c.Name == domain.SpecialistResultReadName {
				if err := r.resolveSpecialistReceiptRead(ctx, c, run, mission, child, attempt, executor, gateway); err != nil {
					return false, err
				}
				result.ToolCalls++
				continue
			}
			if executor.batch != nil && isBatchWorkerTool(c.Name) {
				if err := r.resolveBatchWorkerTool(ctx, c, run, mission, child, attempt, executor, gateway); err != nil {
					return false, err
				}
				result.ToolCalls++
				continue
			}
			var callErr error
			var terminal *domain.SupervisorToolResult
			if c.StartedAt != nil {
				callErr = errors.New("outcome_unknown: this worker call was dispatched without a durable result and will not be repeated")
			}
			if callErr == nil && c.AuthorityJSON == "" {
				catalogue, err := r.workerRuntime.workerToolCatalogue(ctx, c.Ref)
				if err != nil {
					callErr = err
				} else {
					authority, err := r.workerRuntime.bindWorkerToolAuthority(ctx, c, r.batchWorkerCatalogue(catalogue, child))
					if err != nil {
						callErr = err
					} else {
						var bound domain.SpecialistToolCall
						bound, callErr = st.BindSpecialistToolAuthority(ctx, c.Ref, c.CallID, string(authority))
						if callErr == nil {
							c = bound
						}
					}
				}
			}
			if callErr == nil {
				var waiting bool
				waiting, terminal, callErr = r.workerRuntime.preflightWorkerApproval(ctx, c)
				if callErr == nil && waiting {
					// File edits attach their existing edit approval in their own
					// preflight; other services use the global child-call identity.
					if !toolgateway.IsAgentCodeTool(toolgateway.ToolName(c.Name)) {
						record, err := st.GetApprovalByProposal(ctx, c.ApprovalSourceID)
						if err != nil {
							return false, err
						}
						if _, err = st.SetSpecialistToolApproval(ctx, c.Ref, c.CallID, record.ID); err != nil {
							return false, err
						}
					}
					return true, nil
				}
			}
			var outcome toolgateway.Outcome
			if callErr == nil && terminal == nil {
				call, err := workerGatewayCall(c, run, mission, child, attempt)
				if err != nil {
					callErr = err
				} else {
					// Mode is current host state, never selected by child payloads.
					mode, err := r.store.GetRunMode(ctx, run.ID)
					if err != nil {
						callErr = err
					} else {
						call.Surface, call.Phase, call.Profile, call.ModeRevision = mode.Surface, mode.Phase, mode.Profile, mode.Revision
						toolCtx, cancel := context.WithTimeout(ctx, supervisorToolExecutionTimeout(call.Name))
						outcome, callErr = r.workerRuntime.tools.Invoke(toolCtx, call)
						cancel()
					}
				}
			}
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			c.Status = domain.SupervisorToolCompleted
			var receipt any
			switch {
			case callErr != nil:
				c.Status, c.ErrorCode = domain.SupervisorToolFailed, string(apperror.CodeOf(apperror.Normalize(callErr)))
				receipt = map[string]any{"status": c.Status, "error_code": c.ErrorCode, "message": redact.String(callErr.Error())}
			case terminal != nil:
				c.Status, c.ErrorCode = terminal.Status, terminal.ErrorCode
				receipt = json.RawMessage(terminal.ResultJSON)
			case !outcome.Decision.Allowed || outcome.Result != nil && outcome.Result.Status == toolgateway.StatusDenied:
				c.Status, c.ErrorCode = domain.SupervisorToolDenied, "policy_denied"
				receipt = map[string]any{"status": c.Status, "error_code": c.ErrorCode, "message": outcome.Decision.Reason}
			case outcome.Result == nil:
				c.Status, c.ErrorCode = domain.SupervisorToolFailed, "failed_precondition"
				receipt = map[string]any{"status": c.Status, "message": "worker tool returned no result"}
			default:
				if outcome.Result.Status == toolgateway.StatusFailed {
					c.Status, c.ErrorCode = domain.SupervisorToolFailed, "tool_failed"
					if c.Name == string(toolgateway.MCPToolCallTool) {
						c.ErrorCode = "remote_tool_error"
					}
				}
				var content any = outcome.Result.Stdout
				if json.Valid([]byte(outcome.Result.Stdout)) {
					content = json.RawMessage(outcome.Result.Stdout)
				}
				receipt = map[string]any{"status": c.Status, "result": content,
					"error_code": c.ErrorCode, "stderr": outcome.Result.Stderr,
					"truncated": outcome.Result.Truncated, "metadata": outcome.Result.Metadata}
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				return false, err
			}
			c.ResultJSON = string(raw)
			eventCtx, cancel := specialistEventContext(ctx)
			err = st.CompleteSpecialistToolCall(eventCtx, c)
			cancel()
			if err != nil {
				return false, err
			}
			result.ToolCalls++
		}
	}
	return false, nil
}

func (s *AgentRunner) preflightWorkerApproval(ctx context.Context, c domain.SpecialistToolCall) (bool, *domain.SupervisorToolResult, error) {
	name := toolgateway.ToolName(c.Name)
	if toolgateway.IsCommandExecutionTool(name) {
		p, ok := s.commandRuntime.(interface {
			PreflightSpecialistCommandApproval(context.Context, domain.AgentAttemptRef, string) (bool, *domain.SupervisorToolResult, error)
		})
		if !ok {
			return false, nil, errors.New("worker command approval is unavailable")
		}
		return p.PreflightSpecialistCommandApproval(ctx, c.Ref, c.CallID)
	}
	if toolgateway.IsAgentCodeTool(name) {
		e, err := s.workerCodeExecutor()
		if err != nil {
			return false, nil, err
		}
		return e.PreflightSpecialistFileEditApproval(ctx, c.Ref, c.CallID)
	}
	if name == toolgateway.MCPToolCallTool {
		return s.preflightWorkerMCPApproval(ctx, c)
	}
	if toolgateway.IsBrowserActionTool(name) {
		return s.preflightWorkerBrowserApproval(ctx, c)
	}
	return false, nil, nil
}

func workerGatewayCall(c domain.SpecialistToolCall, run domain.Run, mission domain.Mission, child domain.AgentNode, attempt domain.AgentAttempt) (toolgateway.ToolCall, error) {
	call := toolgateway.ToolCall{Name: toolgateway.ToolName(c.Name), Payload: json.RawMessage(c.PayloadJSON), OperationKey: domain.SpecialistToolApprovalID(c.Ref, c.CallID),
		RunID: run.ID, MissionID: mission.ID, WorkspaceID: mission.WorkspaceID, SessionID: child.SessionID, RootAgentID: child.ParentID,
		AgentID: child.ID, AgentAttemptID: attempt.ID, SpecialistToolCallID: c.CallID, Role: domain.AgentRoleSpecialist, RequestedBy: "specialist_runner", LeaseID: attempt.LeaseID, LeaseGeneration: attempt.LeaseGeneration}
	if c.Ref != specialistAttemptRef(attempt) || attempt.AgentID != child.ID {
		return call, errors.New("worker call attempt changed")
	}
	raw := json.RawMessage(c.AuthorityJSON)
	switch {
	case toolgateway.IsAgentCodeTool(call.Name) || toolgateway.IsCodeIntelTool(call.Name):
		a, err := toolgateway.DecodeAgentCodeCallAuthority(raw)
		if err != nil {
			return call, err
		}
		call.RootFingerprint, call.CapabilityGeneration = a.RootFingerprint, a.CapabilityGeneration
		call.PermissionMode, call.PermissionRevision = a.PermissionMode, a.PermissionRevision
		call.PermissionSnapshotID, call.PermissionGeneration, call.PermissionRuntimeEpoch, call.RunAuthorizationFence = a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence
	case toolgateway.IsCommandExecutionTool(call.Name):
		a, err := commandruntimeadapter.DecodeAuthority(raw)
		if err != nil {
			return call, err
		}
		call.CommandRuntimeAdapter, call.CapabilityGeneration = a.Adapter, a.Adapter.Generation
		call.PermissionMode, call.PermissionRevision = a.PermissionMode, a.PermissionRevision
		call.PermissionSnapshotID, call.PermissionGeneration, call.PermissionRuntimeEpoch, call.RunAuthorizationFence = a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence
	case call.Name == toolgateway.MCPToolCallTool || call.Name == toolgateway.MCPToolSearchTool:
		a, err := mcp.DecodeSupervisorCallAuthority(raw)
		if err != nil {
			return call, err
		}
		call.PermissionMode, call.PermissionRevision = a.PermissionMode, a.PermissionRevision
		call.PermissionSnapshotID, call.PermissionGeneration, call.PermissionRuntimeEpoch, call.RunAuthorizationFence = a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence
	case toolgateway.IsBrowserActionTool(call.Name):
		a, err := toolgateway.DecodeAgentBrowserAuthority(raw)
		if err != nil {
			return call, err
		}
		call.AgentBrowserAuthority, call.CapabilityGeneration = raw, a.Generation
		call.PermissionMode, call.PermissionRevision = a.PermissionMode, a.PermissionRevision
		call.PermissionSnapshotID, call.PermissionGeneration, call.RunAuthorizationFence = a.PermissionSnapshotID, a.PermissionActivation, a.RunAuthorizationFence
	case call.Name == toolgateway.SkillReadTool:
		if err := validateWorkerSkillAuthority(call, raw); err != nil {
			return call, err
		}
	case toolgateway.IsHistoryRecallTool(call.Name):
		a, err := toolgateway.DecodeWorkerHistoryAuthority(raw)
		if err != nil {
			return call, err
		}
		call.PermissionMode, call.PermissionRevision = a.PermissionMode, a.PermissionRevision
		call.PermissionSnapshotID, call.PermissionGeneration, call.PermissionRuntimeEpoch, call.RunAuthorizationFence = a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence
	default:
		return call, errors.New("worker tool runtime is unavailable")
	}
	return call, nil
}

// A pending approval is an intentional suspension, not an interrupted tool.
// Do not consume a turn or create a new intent while the operator is deciding.
func (r *SubagentRunner) resumeWorkerAttempt(ctx context.Context, child domain.AgentNode, lease domain.RunExecutionLease, result *SpecialistTurnResult) (domain.AgentAttempt, bool, error) {
	st, ok := r.store.(interface {
		ResumeSpecialistToolAttempt(context.Context, domain.AgentAttemptRef, domain.RunExecutionLease) (domain.AgentAttempt, error)
		GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
		ListSpecialistToolRounds(context.Context, domain.AgentAttemptRef) ([]domain.SpecialistToolRound, error)
		GetApprovalByProposal(context.Context, string) (approval.Record, error)
	})
	if !ok {
		return domain.AgentAttempt{}, false, errors.New("worker approval recovery is unavailable")
	}
	a, found, err := st.GetAgentAttempt(ctx, child.ActiveAttemptID)
	if err != nil || !found {
		return a, false, errors.Join(err, errors.New("worker active attempt is missing"))
	}
	result.AttemptID, result.AttemptStatus, result.Turn, result.Usage = a.ID, a.Status, a.Turn, a.Usage
	rounds, err := st.ListSpecialistToolRounds(ctx, specialistAttemptRef(a))
	if err != nil {
		return a, false, err
	}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.Status != domain.SupervisorToolPending || call.ApprovalID == "" || call.StartedAt != nil {
				continue
			}
			proposal := call.ApprovalSourceID
			if toolgateway.IsAgentCodeTool(toolgateway.ToolName(call.Name)) {
				var p struct {
					EditID string `json:"edit_id"`
				}
				_ = json.Unmarshal([]byte(call.PayloadJSON), &p)
				proposal = p.EditID
			}
			p, err := st.GetApprovalByProposal(ctx, proposal)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return a, false, err
			}
			if err == nil && p.Status == approval.StatusPending {
				result.WaitingApproval = true
				return a, true, nil
			}
		}
	}
	a, err = st.ResumeSpecialistToolAttempt(ctx, specialistAttemptRef(a), lease)
	return a, false, err
}
