package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// Registered tests use the selected backend and existing approval preferences
// in both phases. Test code can write files/caches even during Plan.
func projectTestPhaseAllowed(phase domain.ExecutionPhase) bool {
	return phase == domain.ExecutionPhaseDeliver || phase == domain.ExecutionPhasePlan
}

func supervisorProjectTestsAvailable(surface domain.ExecutionSurface, phase domain.ExecutionPhase,
	permission domain.RunExecutionPermissionMode, adapter commandruntimeadapter.Identity) bool {
	return surface == domain.ExecutionSurfaceCode && adapter.Executable() && permission.IsApprovalMode() &&
		adapter.AllowsPermission(permission) && projectTestPhaseAllowed(phase)
}

func (s *CommandRuntimeService) prepareProjectTest(ctx context.Context, a commandruntimeadapter.Authority,
	i toolgateway.RunTestsInput) (toolgateway.CommandRuntimeInput, error) {
	if _, _, err := toolgateway.NormalizeRunTestsPayload(mustProjectTestJSON(i)); err != nil {
		return toolgateway.CommandRuntimeInput{}, err
	}
	_, b, err := s.commandRuntimeAuthorityBindingsForTest(ctx, a, true)
	if err != nil {
		return toolgateway.CommandRuntimeInput{}, err
	}
	spec, err := PrepareProjectCommandForAdapter(b.run, "test", i.Target, s.adapter, b.rootPath)
	if err != nil {
		return toolgateway.CommandRuntimeInput{}, err
	}
	// The preset determines all effects. Model input only narrows its timeout.
	spec.TimeoutMilliseconds = i.TimeoutMilliseconds
	spec.Output.InlineBytes = runner.MinCommandRuntimeInlineBytes
	maxBytes := toolgateway.MaxCommandRuntimePageBytes
	return toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionRun, Commands: []runner.CommandRuntimeSpec{spec},
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes}, nil
}

func mustProjectTestJSON(i toolgateway.RunTestsInput) json.RawMessage {
	raw, _ := json.Marshal(i)
	return raw
}

func (s *CommandRuntimeService) PrepareProjectTest(ctx context.Context, call toolgateway.ToolCall,
	i toolgateway.RunTestsInput) (toolgateway.CommandRuntimeInput, error) {
	root := call.RequestedBy == "run_supervisor" && call.Role == domain.AgentRoleRoot
	worker := call.RequestedBy == "specialist_runner" && call.Role == domain.AgentRoleSpecialist && call.SpecialistToolCallID != ""
	if call.Name != toolgateway.RunTestsTool || (!root && !worker) {
		return toolgateway.CommandRuntimeInput{}, apperror.New(apperror.CodePolicyDenied, "project tests require recorded Supervisor or worker execution")
	}
	a := commandruntimeadapter.Authority{ProtocolVersion: commandruntimeadapter.OperationAuthorityVersion,
		RunID: call.RunID, Adapter: call.CommandRuntimeAdapter, PermissionSnapshotID: call.PermissionSnapshotID,
		PermissionGeneration: call.PermissionGeneration, PermissionRuntimeEpoch: call.PermissionRuntimeEpoch,
		PermissionMode: call.PermissionMode, PermissionRevision: call.PermissionRevision, RunAuthorizationFence: call.RunAuthorizationFence}
	if worker {
		a.Worker = &commandruntimeadapter.WorkerAuthority{AgentID: call.AgentID, AttemptID: call.AgentAttemptID, SessionID: call.SessionID}
	}
	return s.prepareProjectTest(ctx, a, i)
}

func (m *CommandRuntimeMultiplexer) PrepareProjectTest(ctx context.Context, call toolgateway.ToolCall,
	i toolgateway.RunTestsInput) (toolgateway.CommandRuntimeInput, error) {
	for _, s := range m.adapters {
		if s.adapter.SameBackend(call.CommandRuntimeAdapter) {
			return s.PrepareProjectTest(ctx, call, i)
		}
	}
	return toolgateway.CommandRuntimeInput{}, apperror.New(apperror.CodePolicyDenied, "project test backend changed")
}
