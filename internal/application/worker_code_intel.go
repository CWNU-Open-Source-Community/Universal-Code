package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
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

func (e *AgentCodeToolExecutor) bindWorkerCodeIntelWorkspace(ctx context.Context, ref domain.AgentAttemptRef,
	a toolgateway.AgentCodeCallAuthority,
) (toolgateway.AgentCodeCallAuthority, error) {
	w, err := readWorkerExecutionContext(ctx, e.store, ref, e.drydocks)
	if err != nil || w.Binding.BatchPlanID == "" {
		return a, err
	}
	parent, err := ResolveRunFileWorkspace(ctx, e.store, w.Run, w.Mission, e.drydocks)
	if err != nil {
		return a, err
	}
	fingerprint, err := workspace.AgentCodeRootFingerprint(parent.Workspace.RootPath)
	if err != nil {
		return a, err
	}
	if a.RootFingerprint != fingerprint {
		return a, apperror.New(apperror.CodeConflict, "worker code-intel parent workspace changed")
	}
	// Preserve the parent's capability and permission ceiling, changing only
	// the physical root to the child's current admitted Batch generation.
	return toolgateway.NewAgentCodeCallAuthority(toolgateway.AgentCodeCapabilityContext{
		RunID: a.RunID, MissionID: a.MissionID, RootAgentID: a.RootAgentID, WorkspaceID: a.WorkspaceID,
		RootFingerprint: w.Binding.RootFingerprint, Surface: a.Surface, Phase: a.Phase,
		Role: a.Role, Profile: a.Profile, PermissionMode: a.PermissionMode,
		PermissionSnapshotID: a.PermissionSnapshotID, PermissionGeneration: a.PermissionGeneration,
		PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence,
		ModeRevision: a.ModeRevision, PermissionRevision: a.PermissionRevision,
	}, a.SessionID)
}

func (s *AgentRunner) closeWorkerCodeIntelResources(ctx context.Context, ref domain.AgentAttemptRef) error {
	if s == nil || s.codeIntel == nil {
		return nil
	}
	st, ok := s.store.(interface {
		GetAgentNode(context.Context, string) (domain.AgentNode, error)
		ListAgentAttempts(context.Context, string) ([]domain.AgentAttempt, error)
		GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	})
	if !ok {
		return errors.New("worker code-intel cleanup binding reader is unavailable")
	}
	child, err := st.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return err
	}
	if child.RunID != ref.RunID || child.Role != domain.AgentRoleSpecialist {
		return apperror.New(apperror.CodePolicyDenied, "worker code-intel cleanup actor changed")
	}
	if child.Worker == nil {
		return nil // Legacy Batch specialists never acquire worker LSP resources.
	}
	attempts, err := st.ListAgentAttempts(ctx, child.ID)
	if err != nil {
		return err
	}
	closed := map[string]bool{}
	var result error
	for _, attempt := range attempts {
		if attempt.RunID != ref.RunID || attempt.AgentID != child.ID {
			result = errors.Join(result, errors.New("worker code-intel cleanup attempt changed"))
			continue
		}
		attemptRef := specialistAttemptRef(attempt)
		binding, err := st.GetSpecialistToolBinding(ctx, attemptRef)
		if errors.Is(err, sql.ErrNoRows) {
			continue // Interrupted attempts may not have reached tool admission.
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if binding.BatchPlanID == "" {
			continue
		}
		if binding.Ref != attemptRef || binding.SessionID != child.SessionID || binding.Mode != "worker" {
			result = errors.Join(result, errors.New("worker code-intel cleanup binding changed"))
			continue
		}
		key := binding.WorkspaceID + "\x00" + binding.RootFingerprint
		if closed[key] {
			continue
		}
		if err := s.codeIntel.CloseWorkspaceRoot(ctx, binding.WorkspaceID, binding.RootFingerprint); err != nil {
			result = errors.Join(result, err)
			continue
		}
		closed[key] = true
	}
	return result
}

func (e *CodeIntelToolExecutor) ResolveWorkerCodeIntelWorkspace(ctx context.Context,
	call toolgateway.ToolCall,
) (string, string, error) {
	if e == nil || e.authority == nil || call.RequestedBy != "specialist_runner" || !toolgateway.IsCodeIntelTool(call.Name) {
		return "", "", apperror.New(apperror.CodePolicyDenied, "worker code-intel requires its durable child call")
	}
	st, ok := e.authority.store.(agentCodeWorkerStore)
	if !ok {
		return "", "", errors.New("worker code-intel source reader is unavailable")
	}
	ref := domain.AgentAttemptRef{RunID: call.RunID, AgentID: call.AgentID, AttemptID: call.AgentAttemptID}
	stored, err := st.GetSpecialistToolCall(ctx, ref, call.SpecialistToolCallID)
	if err != nil {
		return "", "", err
	}
	a, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(stored.AuthorityJSON))
	if err != nil {
		return "", "", err
	}
	files, err := e.authority.validateWorkerAgentCodeAuthority(ctx, ref, a, call.Name)
	if err != nil {
		return "", "", err
	}
	if provided := strings.TrimSpace(call.WorkspaceRoot); provided != "" {
		expectedInfo, expectedErr := os.Stat(files.Workspace.RootPath)
		providedInfo, providedErr := os.Stat(provided)
		if expectedErr != nil || providedErr != nil || !expectedInfo.IsDir() || !providedInfo.IsDir() || !os.SameFile(expectedInfo, providedInfo) {
			return "", "", apperror.New(apperror.CodeConflict, "provided code-intel root differs from the worker workspace")
		}
	}
	return files.Workspace.ID, files.Workspace.RootPath, nil
}

func (e *CodeIntelToolExecutor) bindWorkerCodeIntelReadScope(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, request *codeintel.Request,
) error {
	if scope.RequestedBy != "specialist_runner" {
		return nil
	}
	w, err := readWorkerExecutionContext(ctx, e.authority.store, domain.AgentAttemptRef{
		RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID,
	}, e.authority.drydocks)
	if err != nil || w.Binding.BatchPlanID == "" {
		return err
	}
	raw, err := json.Marshal(struct {
		Binding domain.SpecialistToolBinding
		Hints   []domain.BatchDeliveryOwnershipHint
	}{w.Binding, w.BatchReadHints})
	if err != nil {
		return err
	}
	request.ReadScopeFingerprint = session.ContentSHA256(string(raw))
	request.ReadPathAllowed = func(path string) bool {
		return domain.BatchOwnershipAllows(w.BatchReadHints, path)
	}
	return nil
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
