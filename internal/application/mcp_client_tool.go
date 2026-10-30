package application

import (
	"context"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

// AgentRunnerMCPClient is the narrow application boundary around the Go-owned
// MCP runtime. It deliberately excludes staging and review operations: model
// execution can observe only the capabilities already enabled by an operator.
type AgentRunnerMCPClient interface {
	Capabilities(context.Context, string, string) (mcp.ScopedCapabilities, error)
	Invoke(context.Context, mcp.InvokeRequest) (mcp.ClientCallResult, error)
}

type MCPClientToolExecutor struct {
	client       AgentRunnerMCPClient
	store        MCPExecutionPermissionStore
	capabilities domain.ExecutionPermissionRuntimeCapabilities
	drydocks     *RunWorktreeService
}

type MCPExecutionPermissionStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
}

func NewMCPClientToolExecutor(client AgentRunnerMCPClient,
	store MCPExecutionPermissionStore,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) (*MCPClientToolExecutor, error) {
	if client == nil || store == nil {
		return nil, errors.New("MCP client runtime and execution permission store are required")
	}
	if err := capabilities.Validate(); err != nil {
		return nil, err
	}
	return &MCPClientToolExecutor{client: client, store: store,
		capabilities: capabilities}, nil
}

func (e *MCPClientToolExecutor) ExecuteMCP(ctx context.Context,
	scope toolgateway.MCPExecutionScope, payload toolgateway.MCPToolCallPayload,
) (toolgateway.MCPExecutionResult, error) {
	if e == nil || e.client == nil || e.store == nil {
		return toolgateway.MCPExecutionResult{}, errors.New("MCP client runtime is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return toolgateway.MCPExecutionResult{}, err
	}
	if err := e.recheckExecutionScope(ctx, scope); err != nil {
		return toolgateway.MCPExecutionResult{}, err
	}
	subject, authorizer, recheck, err := e.operationAuthorizer(ctx, scope, payload)
	if err != nil {
		return toolgateway.MCPExecutionResult{}, err
	}
	var owned *mcp.InvocationWorkspace
	if scope.RequestedBy == "specialist_runner" {
		owned, err = workerMCPWorkspace(ctx, e.store, domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}, e.drydocks)
		if err != nil {
			return toolgateway.MCPExecutionResult{}, err
		}
	}
	result, err := e.client.Invoke(ctx, mcp.InvokeRequest{
		RunID: scope.RunID, WorkspaceID: scope.WorkspaceID,
		ServerID: payload.ServerID, ToolName: payload.ToolName, Method: payload.Method,
		CapabilityFingerprint: payload.CapabilityFingerprint,
		Arguments:             payload.Arguments,
		OperationID:           scope.InvocationID, Subject: subject, Authorizer: authorizer,
		Workspace:             owned,
	})
	if err != nil {
		return toolgateway.MCPExecutionResult{}, err
	}
	// Revocation during transport cannot undo a remote side effect. Reject its
	// result under stale authority without retrying the invocation.
	if err := recheck(ctx); err != nil {
		return toolgateway.MCPExecutionResult{}, mcp.NewInvocationError(toolcontract.Receipt{OperationID: scope.InvocationID,
			State: toolcontract.ReceiptResultReceived, ErrorCode: "authority_changed"}, err)
	}
	return toolgateway.MCPExecutionResult{
		Content: result.Content, FullContent: result.FullContent, IsError: result.IsError, Truncated: result.Truncated,
		Metadata: map[string]string{"trust": "untrusted", "source": "mcp_client", "execution_receipt": "result_received", "operation_id": scope.InvocationID},
	}, nil
}

// mcpRuntimeAuthorityCurrent binds a numeric Run fence to the authority
// instance that issued it. Historical unfenced calls are supported only by a
// legacy host with no runtime authority; they are never upgraded on replay.
func mcpRuntimeAuthorityCurrent(capabilities domain.ExecutionPermissionRuntimeCapabilities,
	runID string, fence uint64, epoch string,
) bool {
	if capabilities.RuntimeAuthority == nil {
		return fence == 0 && epoch == ""
	}
	return epoch != "" && epoch == capabilities.RuntimeAuthority.RuntimeEpoch() &&
		capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(runID, fence)
}

func (e *MCPClientToolExecutor) recheckExecutionScope(ctx context.Context,
	scope toolgateway.MCPExecutionScope,
) error {
	_, err := e.currentExecutionScope(ctx, scope)
	return err
}

func (e *MCPClientToolExecutor) currentExecutionScope(ctx context.Context,
	scope toolgateway.MCPExecutionScope,
) (domain.RunExecutionPermissionSnapshot, error) {
	run, err := e.store.GetRun(ctx, scope.RunID)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, apperror.Normalize(err)
	}
	mission, err := e.store.GetMission(ctx, scope.MissionID)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, apperror.Normalize(err)
	}
	permission, err := e.store.GetRunExecutionPermission(ctx, scope.RunID)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, apperror.Normalize(err)
	}
	lease, found, err := e.store.GetRunExecutionLease(ctx, scope.RunID)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, apperror.Normalize(err)
	}
	generation, live := e.capabilities.FullAccessGeneration(permission)
	fenceLive := mcpRuntimeAuthorityCurrent(e.capabilities, scope.RunID,
		scope.RunAuthorizationFence, scope.PermissionRuntimeEpoch)
	if run.ID != scope.RunID || run.MissionID != scope.MissionID || run.Status != domain.RunRunning || run.Terminal() ||
		mission.ID != scope.MissionID || mission.WorkspaceID != scope.WorkspaceID ||
		permission.ID != scope.PermissionSnapshotID ||
		permission.Revision != scope.PermissionRevision ||
		permission.Mode != scope.PermissionMode || !live || !fenceLive ||
		generation != scope.PermissionGeneration ||
		!found || lease.LeaseID != scope.LeaseID ||
		lease.Generation != scope.LeaseGeneration || !lease.ActiveAt(time.Now().UTC()) {
		return domain.RunExecutionPermissionSnapshot{}, apperror.New(
			apperror.CodeConflict,
			"MCP execution permission, activation generation, or Run lease is stale")
	}
	return permission, nil
}
