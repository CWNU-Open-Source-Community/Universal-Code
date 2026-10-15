package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type supervisorMCPReferenceStore interface {
	ListSupervisorMCPReferences(context.Context, domain.SupervisorCheckpoint) ([]domain.ThreadMCPReference, error)
	ClaimThreadMCPReference(context.Context, domain.SupervisorCheckpoint, string) (domain.ThreadMCPReference, bool, error)
	RecheckThreadMCPReference(context.Context, domain.SupervisorCheckpoint, string) error
	CompleteThreadMCPReference(context.Context, string, string, string, string, string) (domain.ThreadMCPReference, error)
	GetMCPClientServer(context.Context, string) (mcp.ServerRecord, error)
}

type mcpReferenceResult struct {
	Version          string                     `json:"version"`
	ServerID         string                     `json:"server_id"`
	Method           string                     `json:"method"`
	Receipt          string                     `json:"receipt"`
	Content          string                     `json:"content,omitempty"`
	IsError          bool                       `json:"is_error,omitempty"`
	Truncated        bool                       `json:"truncated,omitempty"`
	Error            string                     `json:"error,omitempty"`
	FullResultRead   *domain.HistoryReadRequest `json:"full_result_read,omitempty"`
	FullResultNotice string                     `json:"full_result_notice,omitempty"`
	FullContent      string                     `json:"-"`
}

// The message selection itself is this one operation's exact consent. The
// ordinary model MCP executor and its Ask/Auto/Full decisions are unchanged.
func (s *AgentRunner) materializeSupervisorMCPReferences(ctx context.Context, turn domain.SupervisorTurn, vision bool) ([]llm.Message, []int64, error) {
	store, ok := s.store.(supervisorMCPReferenceStore)
	if !ok {
		return nil, nil, nil
	}
	refs, err := store.ListSupervisorMCPReferences(ctx, turn.Checkpoint)
	if err != nil {
		return nil, nil, err
	}
	var messages []llm.Message
	var evidenceIDs []int64
	for _, ref := range refs {
		value, fresh, err := store.ClaimThreadMCPReference(ctx, turn.Checkpoint, ref.OperationID)
		if err != nil {
			return nil, nil, err
		}
		if value.State != "completed" {
			result := mcpReferenceResult{Version: "mcp_reference_result.v1", ServerID: ref.Reference.ServerID, Method: ref.Reference.Method,
				Receipt: string(toolcontract.ReceiptOutcomeUnknown), IsError: true, Error: "A previous read started without a saved result. Its outcome is unknown; it was not repeated. Ask the operator to select it again if another read is needed."}
			if fresh {
				result = s.invokeSelectedMCPReference(ctx, store, turn, value)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return nil, nil, err
			}
			_, evidence, err := projectMCPReferenceEvidence(turn.Run.SessionID, ref.OperationID, result, false)
			if err != nil {
				return nil, nil, err
			}
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			value, err = store.CompleteThreadMCPReference(persistCtx, ref.OperationID, result.Receipt, string(raw), evidence, result.FullContent)
			cancel()
			if err != nil {
				return nil, nil, err
			}
		}
		var result mcpReferenceResult
		if json.Unmarshal([]byte(value.ResultJSON), &result) != nil {
			return nil, nil, errors.New("saved MCP reference result is invalid")
		}
		message, _, err := projectMCPReferenceEvidence(turn.Run.SessionID, ref.OperationID, result, vision)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, message)
		evidenceIDs = append(evidenceIDs, value.EvidenceMessageID)
	}
	return messages, evidenceIDs, nil
}

func (s *AgentRunner) invokeSelectedMCPReference(ctx context.Context, store supervisorMCPReferenceStore, turn domain.SupervisorTurn, ref domain.ThreadMCPReference) mcpReferenceResult {
	result := mcpReferenceResult{Version: "mcp_reference_result.v1", ServerID: ref.Reference.ServerID, Method: ref.Reference.Method, Receipt: string(toolcontract.ReceiptNotDispatched)}
	fail := func(err error) mcpReferenceResult {
		result.IsError = true
		result.Error = boundedSupervisorToolMessage(redact.String(err.Error()))
		return result
	}
	if s.mcpClient == nil {
		return fail(errors.New("MCP runtime is unavailable"))
	}
	server, err := store.GetMCPClientServer(ctx, ref.Reference.ServerID)
	if err != nil {
		return fail(err)
	}
	if server.State != mcp.TrustEnabled || server.Health != mcp.HealthHealthy || server.ApprovedCapabilityFingerprint != ref.Reference.CapabilityFingerprint || server.Capabilities.Fingerprint != ref.Reference.CapabilityFingerprint {
		return fail(apperror.New(apperror.CodeConflict, "MCP selection is stale; select this resource or Prompt again from the current catalog"))
	}
	toolName := "resources.read"
	if ref.Reference.Method == "prompts/get" {
		toolName = "prompts.get"
	}
	policyCall := tools.Call{Name: mcp.OperationApprovalTool, Args: map[string]string{"server_id": ref.Reference.ServerID, "tool_name": toolName, "method": ref.Reference.Method,
		"capability_fingerprint": ref.Reference.CapabilityFingerprint, "arguments": string(ref.Reference.Arguments)}}
	if decision := s.checker.CheckToolCall(policyCall); !decision.Allowed {
		return fail(apperror.New(apperror.CodePolicyDenied, decision.Reason))
	}
	if s.lifecycleHooks != nil {
		payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPContentToolProtocolVersion, Method: ref.Reference.Method, ServerID: ref.Reference.ServerID,
			ToolName: toolName, CapabilityFingerprint: ref.Reference.CapabilityFingerprint, Arguments: ref.Reference.Arguments})
		checked, err := s.lifecycleHooks.Execute(ctx, hooks.Input{Event: hooks.PreTool, RunID: turn.Run.ID, WorkspaceID: turn.Mission.WorkspaceID, ToolName: mcp.OperationApprovalTool, Payload: payload})
		if err != nil {
			return fail(err)
		}
		if checked.Denied || !bytes.Equal(checked.Payload, payload) {
			return fail(apperror.New(apperror.CodePolicyDenied, "A lifecycle hook rejected or changed this exact MCP selection"))
		}
		defer func() {
			_, _ = s.lifecycleHooks.Execute(context.WithoutCancel(ctx), hooks.Input{Event: hooks.PostTool, RunID: turn.Run.ID, WorkspaceID: turn.Mission.WorkspaceID, ToolName: mcp.OperationApprovalTool, Payload: json.RawMessage(`{"operator_selected":true}`)})
		}()
	}
	if _, err := mcp.ValidateInvocation(server.Capabilities, ref.Reference.Method, toolName, ref.Reference.Arguments); err != nil {
		return fail(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
	if err != nil {
		return fail(err)
	}
	fence := uint64(0)
	epoch := ""
	if runtime := s.executionCapabilities.RuntimeAuthority; runtime != nil {
		epoch = runtime.RuntimeEpoch()
		fence, err = runtime.IssueRunAuthorizationFence(turn.Run.ID)
		if err != nil {
			return fail(err)
		}
	}
	subject := executionauth.SubjectRef{RunID: turn.Run.ID, ActorID: ref.RequestedBy}
	component := toolcontract.ComponentRef{PackageID: server.Descriptor.ProtocolVersion, ComponentID: server.Descriptor.ID}
	if source := server.Descriptor.NativeSource; source != nil {
		if source.Surface != string(turn.Mode.Surface) {
			return fail(errors.New("MCP server surface differs from the selected task"))
		}
		component = source.Component
	}
	bindingJSON, _ := json.Marshal(struct {
		Ref        domain.ThreadMCPReference
		Checkpoint domain.SupervisorCheckpoint
		Permission domain.RunExecutionPermissionSnapshot
		Descriptor string
		Generation int64
		Fence      uint64
		Epoch      string
	}{
		ref, turn.Checkpoint, permission, server.DescriptorFingerprint, server.Generation, fence, epoch})
	digest := sha256.Sum256(bindingJSON)
	binding := hex.EncodeToString(digest[:])
	recheck := func(checkCtx context.Context) error {
		if decision := s.checker.CheckToolCall(policyCall); !decision.Allowed {
			return apperror.New(apperror.CodePolicyDenied, decision.Reason)
		}
		if err := store.RecheckThreadMCPReference(checkCtx, turn.Checkpoint, ref.OperationID); err != nil {
			return err
		}
		currentServer, err := store.GetMCPClientServer(checkCtx, ref.Reference.ServerID)
		if err != nil {
			return err
		}
		if currentServer.Generation != server.Generation || currentServer.DescriptorFingerprint != server.DescriptorFingerprint || currentServer.State != mcp.TrustEnabled || currentServer.Health != mcp.HealthHealthy || currentServer.ApprovedCapabilityFingerprint != ref.Reference.CapabilityFingerprint || currentServer.Capabilities.Fingerprint != ref.Reference.CapabilityFingerprint {
			return apperror.New(apperror.CodeConflict, "MCP server selection changed before dispatch")
		}
		current, err := s.store.GetRunExecutionPermission(checkCtx, turn.Run.ID)
		if err != nil {
			return err
		}
		if current.ID != permission.ID || current.Revision != permission.Revision || !mcpRuntimeAuthorityCurrent(s.executionCapabilities, turn.Run.ID, fence, epoch) {
			return apperror.New(apperror.CodeConflict, "MCP selection permission changed before dispatch")
		}
		return nil
	}
	operationHash := sha256.Sum256([]byte(ref.OperationID))
	operationSuffix := hex.EncodeToString(operationHash[:])
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context, actual executionauth.SubjectRef, operation toolcontract.Operation, approvalRef string) (executionauth.OperationAuthority, error) {
		if actual != subject || approvalRef != "" || operation.Component != component || operation.AdapterID != mcp.RuntimeAdapterID || operation.AdapterRevision != mcp.RuntimeAdapterRevision || operation.ToolID != "mcp_tool_call" {
			return executionauth.OperationAuthority{}, errors.New("MCP operation differs from this message selection")
		}
		expectedID := ref.OperationID
		switch operation.Kind {
		case toolcontract.OperationConnect:
			expectedID = "mcp-connect-" + operationSuffix
		case toolcontract.OperationDiscovery:
			expectedID = "mcp-discovery-" + operationSuffix
		case toolcontract.OperationToolCall:
		default:
			return executionauth.OperationAuthority{}, errors.New("MCP selection cannot authorize this operation")
		}
		if operation.ID != expectedID {
			return executionauth.OperationAuthority{}, errors.New("MCP operation identity differs from this message selection")
		}
		if err := recheck(checkCtx); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		fingerprint, err := toolcontract.FingerprintOperation(operation)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalAsk, BindingFingerprint: binding, RuntimeAvailable: true,
			Approval: &executionauth.BoundApproval{Ref: ref.OperationID, Subject: subject, OperationFingerprint: fingerprint, Status: "approved"}}, nil
	})
	response, err := s.mcpClient.Invoke(ctx, mcp.InvokeRequest{RunID: turn.Run.ID, WorkspaceID: turn.Mission.WorkspaceID,
		ServerID: ref.Reference.ServerID, CapabilityFingerprint: ref.Reference.CapabilityFingerprint, Method: ref.Reference.Method,
		ToolName: toolName, Arguments: slices.Clone(ref.Reference.Arguments), OperationID: ref.OperationID, Subject: subject, Authorizer: authorizer})
	if err != nil {
		result.Receipt = string(toolcontract.ReceiptOutcomeUnknown)
		if receipt, found := mcp.InvocationReceipt(err); found {
			result.Receipt = string(receipt.State)
		}
		return fail(err)
	}
	result.Receipt = string(toolcontract.ReceiptResultReceived)
	if err := recheck(ctx); err != nil {
		return fail(err)
	}
	result.Content, result.IsError, result.Truncated = response.Content, response.IsError, response.Truncated
	result.FullContent = response.FullContent
	return result
}

func projectMCPReferenceEvidence(sessionID, operationID string, result mcpReferenceResult, vision bool) (llm.Message, string, error) {
	var images []llm.ImagePart
	if result.Content != "" {
		var native map[string]any
		decoder := json.NewDecoder(bytes.NewReader([]byte(result.Content)))
		decoder.UseNumber()
		if decoder.Decode(&native) == nil && native != nil {
			projectMCPContent(native, &images, vision, operationID)
			raw, err := json.Marshal(native)
			if err != nil {
				return llm.Message{}, "", err
			}
			result.Content = string(raw)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return llm.Message{}, "", err
	}
	evidence := session.NewEvidenceMessage(sessionID, session.SourceToolResult, operationID, string(raw))
	projected := session.ProjectContextMessage(evidence)
	message := llm.Message{Role: projected.Role, Content: projected.Content, Images: images}
	if err := llm.ValidateMessageImages(message); err != nil {
		return llm.Message{}, "", fmt.Errorf("MCP selection image projection: %w", err)
	}
	return message, string(raw), nil
}

func withoutCurrentMCPReferenceEvidence(history []session.Message, ids []int64) []session.Message {
	if len(ids) == 0 {
		return history
	}
	return slices.DeleteFunc(slices.Clone(history), func(message session.Message) bool { return slices.Contains(ids, message.ID) })
}
