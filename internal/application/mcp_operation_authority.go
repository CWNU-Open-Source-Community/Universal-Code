package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

// Only the host supplies this resolver. MCP adapters describe actual operations;
// neither a tool annotation nor a plugin declaration supplies execution authority.
func (e *MCPClientToolExecutor) operationAuthorizer(ctx context.Context, scope toolgateway.MCPExecutionScope, payload toolgateway.MCPToolCallPayload) (executionauth.SubjectRef, *executionauth.PolicyAuthorizer, func(context.Context) error, error) {
	serverID := payload.ServerID
	store, ok := e.store.(interface {
		GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	})
	if !ok {
		return executionauth.SubjectRef{}, nil, nil, errors.New("MCP host actor/mode reader unavailable")
	}
	readActor := func(ctx context.Context) (domain.AgentNode, error) {
		if scope.AgentID != "" {
			actors, ok := e.store.(interface {
				GetAgentNode(context.Context, string) (domain.AgentNode, error)
			})
			if !ok {
				return domain.AgentNode{}, errors.New("MCP actor reader unavailable")
			}
			return actors.GetAgentNode(ctx, scope.AgentID)
		}
		// Compatibility for trusted host callers predating durable attribution.
		// The model-facing gateway supplies a concrete actor and attempt.
		roots, ok := e.store.(interface {
			GetRootAgent(context.Context, string) (domain.AgentNode, bool, error)
		})
		if !ok {
			return domain.AgentNode{}, errors.New("MCP host actor reader unavailable")
		}
		actor, found, err := roots.GetRootAgent(ctx, scope.RunID)
		if err == nil && !found {
			err = errors.New("MCP host actor unavailable")
		}
		return actor, err
	}
	actor, err := readActor(ctx)
	if err != nil {
		return executionauth.SubjectRef{}, nil, nil, err
	}
	subject := executionauth.SubjectRef{RunID: scope.RunID, ActorID: actor.ID}
	component := toolcontract.ComponentRef{PackageID: mcp.ClientProtocolVersion, ComponentID: serverID}
	readBinding := func(ctx context.Context) (string, domain.ExecutionApprovalMode, error) {
		permission, err := e.currentExecutionScope(ctx, scope)
		if err != nil {
			return "", "", err
		}
		current, err := readActor(ctx)
		if err != nil || current.ID != actor.ID || current.RunID != scope.RunID || current.Role != scope.Role ||
			(scope.AgentID != "" && (scope.AgentID != current.ID || scope.AgentAttemptID != current.ActiveAttemptID || current.Status != domain.AgentRunning)) {
			return "", "", apperror.New(apperror.CodeConflict, "MCP host actor or attempt changed")
		}
		mode, err := store.GetRunMode(ctx, scope.RunID)
		if err != nil {
			return "", "", err
		}
		if mode.Surface != scope.Surface || mode.Phase != scope.Phase || mode.MissionID != scope.MissionID {
			return "", "", apperror.New(apperror.CodeConflict, "MCP execution mode changed")
		}
		raw, err := json.Marshal(struct {
			Scope            toolgateway.MCPExecutionScope
			Mode             domain.RunModeSnapshot
			ActorID, Attempt string
			Status           domain.AgentStatus
		}{scope, mode, current.ID, current.ActiveAttemptID, current.Status})
		if err != nil {
			return "", "", err
		}
		sum := sha256.Sum256(raw)
		projection, err := domain.ExecutionPermissionApproval(permission)
		return hex.EncodeToString(sum[:]), projection.Mode, err
	}
	expected, _, err := readBinding(ctx)
	if err != nil {
		return subject, nil, nil, err
	}
	// Capture the initial host binding before resolving component metadata.
	// Revocation during that lookup is then observed by Manager's actual guard,
	// preserving its not-dispatched receipt and existing call audit boundary.
	if records, ok := e.store.(interface {
		GetMCPClientServer(context.Context, string) (mcp.ServerRecord, error)
	}); ok {
		record, err := records.GetMCPClientServer(ctx, serverID)
		if err != nil && apperror.CodeOf(err) != apperror.CodeNotFound {
			return subject, nil, nil, err
		}
		if err == nil {
			component.PackageID = record.Descriptor.ProtocolVersion
		}
		if err == nil && record.Descriptor.NativeSource != nil {
			source := *record.Descriptor.NativeSource
			if source.Validate() != nil || source.Surface != string(scope.Surface) {
				return subject, nil, nil, apperror.New(apperror.CodePolicyDenied, "native MCP surface does not match its host subject")
			}
			component = source.Component
		}
	}
	recheck := func(ctx context.Context) error {
		binding, _, err := readBinding(ctx)
		if err == nil && binding != expected {
			err = apperror.New(apperror.CodeConflict, "MCP operation authority changed")
		}
		if err == nil {
			record, proofErr := e.mcpDispatchApproval(ctx, scope, payload)
			err = proofErr
			if err == nil && record != nil && record.Status != approval.StatusApproved {
				err = mcpApprovalUnavailable("MCP operation no longer has its exact approval")
			}
		}
		return err
	}
	authorizer := executionauth.NewPolicyAuthorizer(func(ctx context.Context, actual executionauth.SubjectRef, operation toolcontract.Operation, approvalRef string) (executionauth.OperationAuthority, error) {
		if actual != subject || approvalRef != "" || operation.AdapterID != mcp.RuntimeAdapterID || operation.AdapterRevision != mcp.RuntimeAdapterRevision ||
			operation.Component != component ||
			(operation.Kind != toolcontract.OperationConnect && operation.Kind != toolcontract.OperationDiscovery && operation.Kind != toolcontract.OperationToolCall) {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "MCP host operation does not match its runtime")
		}
		binding, mode, err := readBinding(ctx)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if binding != expected {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeConflict, "MCP operation authority changed")
		}
		// Current Full activation was checked above. Server claims never turn
		// unknown remote/host effects into proven safe auto operations.
		record, err := e.mcpDispatchApproval(ctx, scope, payload)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		value := executionauth.OperationAuthority{Mode: mode, BindingFingerprint: binding, RuntimeAvailable: true,
			FullActivated: mode == domain.ExecutionApprovalFull, EffectsVerified: false}
		if record != nil {
			fingerprint, err := toolcontract.FingerprintOperation(operation)
			if err != nil {
				return executionauth.OperationAuthority{}, err
			}
			value.Approval = &executionauth.BoundApproval{Ref: record.ID, Subject: subject, OperationFingerprint: fingerprint, Status: string(record.Status)}
		}
		return value, nil
	})
	return subject, authorizer, recheck, nil
}

// The durable intent authorizes only this invocation. The runtime separately
// binds the actual launch, discovery budget and wire input with its keyed
// fingerprints before each side effect; server declarations grant no authority.
func (e *MCPClientToolExecutor) mcpDispatchApproval(ctx context.Context, scope toolgateway.MCPExecutionScope, payload toolgateway.MCPToolCallPayload) (*approval.Record, error) {
	if scope.SupervisorToolCallID == "" {
		if scope.PolicyDecision.Approval == toolgateway.ApprovalPerCall {
			return nil, mcpApprovalUnavailable("MCP per-call approval requires a durable intent")
		}
		return nil, nil
	}
	st, ok := e.store.(mcpApprovalStore)
	if !ok {
		return nil, mcpApprovalUnavailable("MCP dispatch source reader unavailable")
	}
	source, err := readMCPApprovalSource(ctx, st, scope.RunID, scope.SupervisorToolCallID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	_, canonical, err := toolgateway.NormalizeMCPToolPayload(raw)
	if err != nil {
		return nil, err
	}
	a, c := source.authority, source.call
	if !source.started || c.Turn != scope.SupervisorTurn || c.AgentID != scope.AgentID ||
		c.AgentAttemptID != scope.AgentAttemptID || c.PayloadJSON != string(canonical) ||
		a.MissionID != scope.MissionID || a.WorkspaceID != scope.WorkspaceID || a.PermissionSnapshotID != scope.PermissionSnapshotID ||
		a.PermissionRevision != scope.PermissionRevision || a.PermissionMode != scope.PermissionMode ||
		a.PermissionGeneration != scope.PermissionGeneration || a.PermissionRuntimeEpoch != scope.PermissionRuntimeEpoch ||
		a.RunAuthorizationFence != scope.RunAuthorizationFence {
		return nil, mcpApprovalUnavailable("MCP dispatch requires its exact started durable call")
	}
	record, err := st.GetApprovalByProposal(ctx, c.CallID)
	if errors.Is(err, sql.ErrNoRows) && scope.PolicyDecision.Approval != toolgateway.ApprovalPerCall {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !mcpApprovalMatches(record, source) {
		return nil, mcpApprovalUnavailable("MCP dispatch approval identity changed")
	}
	if record.GrantID != "" && record.ReviewedBy == "session_grant" &&
		(scope.PolicyDecision.Approval == toolgateway.ApprovalPerCall || strings.EqualFold(scope.PolicyDecision.Risk, "critical")) {
		return nil, mcpApprovalUnavailable("current policy requires exact review of this Computer Use call")
	}
	if err := checkMCPComputerUseAppGrant(ctx, st, record); err != nil {
		return nil, err
	}
	return &record, nil
}
