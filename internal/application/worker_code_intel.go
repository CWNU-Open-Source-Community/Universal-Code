package application

import (
	"context"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func (s *AgentRunner) workerCodeIntelCapabilities(ctx context.Context, ref domain.AgentAttemptRef,
	turn domain.SupervisorTurn,
) (toolgateway.CodeIntelCapabilitySnapshot, error) {
	empty := toolgateway.CodeIntelCapabilitySnapshot{ProtocolVersion: toolgateway.CodeIntelProtocolVersion,
		Servers: []toolgateway.CodeIntelServerCapability{}, Refusals: map[string]string{}}
	available, _ := toolgateway.CodeIntelScopeEligibility(toolgateway.AgentCodeCapabilityContext{
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
		Profile: turn.Mode.Profile})
	if s.codeIntel == nil || !available {
		return empty, nil
	}
	w, err := readWorkerExecutionContext(ctx, s.store, ref, s.drydocks)
	if err != nil {
		return empty, err
	}
	if turn.Run.ID != ref.RunID || turn.Agent.ID != w.Child.ParentID ||
		turn.Mission.ID != w.Mission.ID || turn.Mission.WorkspaceID != w.Mission.WorkspaceID {
		return empty, errors.New("worker code-intel parent scope changed")
	}
	return s.codeIntelWorkspaceCapabilities(ctx, w.Files.Workspace.ID, w.Files.Workspace.RootPath), nil
}

func (e *CodeIntelToolExecutor) WithExecutionPermissionCapabilities(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *CodeIntelToolExecutor {
	if e != nil && e.authority != nil {
		e.authority.WithExecutionPermissionCapabilities(capabilities)
	}
	return e
}

func (e *CodeIntelToolExecutor) validateWorkerPayload(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, name toolgateway.ToolName, payload json.RawMessage,
) error {
	if scope.RequestedBy != "specialist_runner" {
		return nil
	}
	call, err := e.authority.workerAgentCodeCall(ctx, scope, name, true)
	if err != nil {
		return err
	}
	if call.PayloadJSON != string(payload) {
		return apperror.New(apperror.CodeConflict, "worker code-intel payload differs from its durable call")
	}
	return nil
}
