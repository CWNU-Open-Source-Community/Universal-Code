package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

func (e *AgentCodeToolExecutor) authorizeAgentCodeRead(ctx context.Context, scope toolgateway.AgentCodeExecutionScope,
	name toolgateway.ToolName, payload json.RawMessage,
) (func() error, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	operation := toolcontract.Operation{ID: scope.InvocationID, Kind: toolcontract.OperationFileRead, ToolID: string(name),
		Component: toolcontract.ComponentRef{PackageID: "traverse-board", ComponentID: "workspace-files"},
		AdapterID: "host-rooted-files", AdapterRevision: "1", InputFingerprint: hex.EncodeToString(mac.Sum(nil)),
		Targets: []toolcontract.Target{{Kind: "directory", Locator: scope.WorkspaceID + ":" + scope.RootFingerprint}},
		Effects: []toolcontract.Effect{toolcontract.EffectWorkspaceRead}}
	subject := executionauth.SubjectRef{RunID: scope.RunID, ActorID: scope.ActorID()}
	raw, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context, actual executionauth.SubjectRef, _ toolcontract.Operation, ref string) (executionauth.OperationAuthority, error) {
		if actual != subject || ref != "" {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "file read authority mismatch")
		}
		if err := e.validateScope(checkCtx, scope, name); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if e.apply.checker == nil {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeFailedPrecondition, "file read policy is unavailable")
		}
		policy := e.apply.checker.CheckToolCall(tools.Call{Name: string(name),
			Args: map[string]string{"payload": string(payload)}, WorkingDir: scope.WorkspaceRoot})
		if !policy.Allowed || policy.NeedsApproval {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "file read is blocked by the current host policy")
		}
		permission, err := e.store.GetRunExecutionPermission(checkCtx, scope.RunID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		projection, err := domain.ExecutionPermissionApproval(permission)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		return executionauth.OperationAuthority{Mode: projection.Mode, BindingFingerprint: fileedit.HashText(string(raw)),
			RuntimeAvailable: true, FullActivated: projection.Mode == domain.ExecutionApprovalFull, EffectsVerified: true}, nil
	})
	decision, err := authorizer.Authorize(ctx, subject, operation, "")
	if err != nil {
		return nil, err
	}
	if decision.Outcome != "allow" {
		return nil, apperror.New(apperror.CodePolicyDenied, "file read was not authorized")
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return nil, err
	}
	if err := decision.BeforeDispatch(ctx, fingerprint); err != nil {
		return nil, err
	}
	return func() error { return authorizer.Recheck(ctx, subject, operation, "", decision.AuthorizationRef) }, nil
}

// Native file bindings carry host fences; the shared plugin Operation remains
// independent of Run leases and process-local activation internals.
func agentCodeRuntimeCurrent(capabilities domain.ExecutionPermissionRuntimeCapabilities,
	permission domain.RunExecutionPermissionSnapshot, snapshotID string, generation uint64,
	epoch string, fence uint64,
) bool {
	expected, live := capabilities.FullAccessGeneration(permission)
	if !live || snapshotID != permission.ID || generation != expected {
		return false
	}
	if capabilities.RuntimeAuthority == nil {
		return !permission.Mode.IsFullPreference() && epoch == "" && fence == 0
	}
	return epoch != "" && epoch == capabilities.RuntimeAuthority.RuntimeEpoch() &&
		capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(permission.RunID, fence)
}

func bindAgentCodeRuntime(capabilities domain.ExecutionPermissionRuntimeCapabilities,
	permission domain.RunExecutionPermissionSnapshot,
) (snapshotID string, generation uint64, epoch string, fence uint64, live bool) {
	generation, live = capabilities.FullAccessGeneration(permission)
	if !live {
		return
	}
	snapshotID = permission.ID
	if runtime := capabilities.RuntimeAuthority; runtime != nil {
		var err error
		epoch = runtime.RuntimeEpoch()
		fence, err = runtime.IssueRunAuthorizationFence(permission.RunID)
		live = err == nil && epoch != ""
	}
	return
}

// The native manager has already prepared rooted paths, original/proposed
// hashes and recoverable bytes. Decide only after that evidence exists; a tool
// declaration cannot claim a reversible write. Existing proposals are replayed
// before reaching this function and are never silently upgraded.
func (e *AgentCodeToolExecutor) automaticallyAuthorizePreparedFileEdit(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, edit fileedit.Edit,
) (bool, error) {
	proposalTool := toolgateway.WorkspaceChangeTool
	if edit.Operation == fileedit.OperationDelete {
		proposalTool = toolgateway.WorkspaceDeleteTool
	}
	if err := e.validateScope(ctx, scope, proposalTool); err != nil {
		return false, err
	}
	permission, err := e.store.GetRunExecutionPermission(ctx, scope.RunID)
	if err != nil {
		return false, err
	}
	run, err := e.store.GetRun(ctx, scope.RunID)
	if err != nil {
		return false, err
	}
	policy, err := e.apply.fileEditPolicyDecision(ctx, fileEditApplyBinding{run: run, edit: edit})
	if err != nil {
		return false, err
	}
	if policy.NeedsApproval {
		return false, nil
	}
	prepared := fileedit.ApplyOperation{KeyDigest: runmutation.Fingerprint("file-operation-proposal.v1", scope.RunID, scope.OperationKey),
		RunID: scope.RunID, SessionID: scope.SessionID, WorkspaceID: edit.WorkspaceID, EditID: edit.ID,
		Operation: edit.Operation, Path: edit.Path, DestinationPath: edit.DestinationPath,
		OriginalHash: edit.OriginalHash, ProposedHash: edit.ProposedHash,
		DestinationOriginalHash: edit.DestinationOriginalHash, DestinationProposedHash: edit.DestinationProposedHash}
	operation, err := fileEditOperation(prepared, edit, scope.RootFingerprint)
	if err != nil {
		return false, err
	}
	projection, err := domain.ExecutionPermissionApproval(permission)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return false, err
	}
	subject := executionauth.SubjectRef{RunID: scope.RunID, ActorID: scope.ActorID()}
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context, actual executionauth.SubjectRef,
		_ toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		if actual != subject || approvalRef != "" {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "file proposal authority changed")
		}
		if err := e.validateScope(checkCtx, scope, proposalTool); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		return executionauth.OperationAuthority{Mode: projection.Mode, BindingFingerprint: fileedit.HashText(string(raw)),
			RuntimeAvailable: true, FullActivated: projection.Mode == domain.ExecutionApprovalFull, EffectsVerified: true}, nil
	})
	decision, err := authorizer.Authorize(ctx, subject, operation, "")
	if err != nil {
		return false, err
	}
	if decision.ReasonCode == "approval_proposal_required" || decision.Outcome == "require_approval" {
		return false, nil
	}
	if decision.Outcome != "allow" {
		return false, apperror.New(apperror.CodePolicyDenied, "file operation denied by current policy")
	}
	return true, nil
}
