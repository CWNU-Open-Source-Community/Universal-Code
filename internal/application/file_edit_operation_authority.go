package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/workspace"
)

// fileEditDispatchCheck binds the existing prepared apply transaction to the
// common authorizer. It consumes one dispatch grant before the first mutation,
// then rechecks that same authority before later native publication/removal.
// No new approval or Run/Session ledger is created by this bridge.
func (s *FileEditApplyService) fileEditDispatchCheck(ctx context.Context,
	prepared fileedit.ApplyOperation, binding fileEditApplyBinding, request ApplyFileEditRequest,
) (func() error, error) {
	subject := executionauth.SubjectRef{RunID: prepared.RunID, ActorID: request.AppliedBy}
	rootFingerprint, err := workspace.AgentCodeRootFingerprint(binding.workspace.RootPath)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	operation, err := fileEditOperation(prepared, binding.edit, rootFingerprint)
	if err != nil {
		return nil, err
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	var runtimeFence uint64
	runtimeEpoch := ""
	if runtime := s.executionCapabilities.RuntimeAuthority; runtime != nil {
		runtimeEpoch = runtime.RuntimeEpoch()
		runtimeFence, err = runtime.IssueRunAuthorizationFence(prepared.RunID)
		if err != nil {
			return nil, err
		}
	}
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context,
		actualSubject executionauth.SubjectRef, _ toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		current, err := s.loadOperationBinding(checkCtx, prepared)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		currentRoot, err := workspace.AgentCodeRootFingerprint(current.workspace.RootPath)
		if err != nil {
			return executionauth.OperationAuthority{}, apperror.Normalize(err)
		}
		expectedApprovalRef := current.approval.ID
		if current.approval.Mode == "automatic" {
			expectedApprovalRef = ""
		}
		if actualSubject != subject || current.run.Status != domain.RunRunning ||
			current.session.Status != session.StatusActive ||
			current.edit.Status != fileedit.StatusApproved ||
			current.workspace.RootPath != binding.workspace.RootPath || currentRoot != rootFingerprint ||
			current.approval.ID != binding.approval.ID || approvalRef != expectedApprovalRef || current.approval.RequestFingerprint !=
			fileedit.ApprovalFingerprint(current.edit.SessionID, current.edit.WorkspaceID, current.edit) {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied,
				"FileEdit execution authority changed before filesystem mutation")
		}
		finalOperation, err := fileEditOperation(prepared, current.edit, currentRoot)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		finalFingerprint, err := toolcontract.FingerprintOperation(finalOperation)
		if err != nil || finalFingerprint != fingerprint {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeConflict,
				"FileEdit final inputs changed before filesystem mutation")
		}
		if err := s.checkCurrentPolicy(checkCtx, current); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if err := s.checkWorkerFileEditSource(checkCtx, current, request); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if err := s.checkAutomaticFileEditAuthorization(checkCtx, current, request); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		permissions, ok := s.store.(interface {
			GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
		})
		if !ok {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeFailedPrecondition, "file permission reader unavailable")
		}
		permission, err := permissions.GetRunExecutionPermission(checkCtx, prepared.RunID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		projection, err := domain.ExecutionPermissionApproval(permission)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		generation, live := s.executionCapabilities.FullAccessGeneration(permission)
		if !live || (runtimeEpoch != "" && !mcpRuntimeAuthorityCurrent(s.executionCapabilities, prepared.RunID, runtimeFence, runtimeEpoch)) {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "file runtime authority was revoked")
		}
		leases, ok := s.store.(RunExecutionLeaseStore)
		if !ok {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeFailedPrecondition,
				"FileEdit execution lease reader is unavailable")
		}
		lease, found, err := leases.GetRunExecutionLease(checkCtx, prepared.RunID)
		if err != nil {
			return executionauth.OperationAuthority{}, apperror.Normalize(err)
		}
		if !found || !lease.ActiveAt(s.now().UTC()) || lease.LeaseID != request.LeaseID ||
			lease.Generation != request.LeaseGeneration {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied,
				"FileEdit execution lease changed before filesystem mutation")
		}
		raw, err := json.Marshal(struct {
			Root         string
			Request      ApplyFileEditRequest
			Approval     approval.Record
			Permission   domain.RunExecutionPermissionSnapshot
			Generation   uint64
			RuntimeEpoch string
			RuntimeFence uint64
		}{currentRoot, request, current.approval, permission, generation, runtimeEpoch, runtimeFence})
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		value := executionauth.OperationAuthority{Mode: projection.Mode, BindingFingerprint: fileedit.HashText(string(raw)),
			RuntimeAvailable: true, FullActivated: projection.Mode == domain.ExecutionApprovalFull, EffectsVerified: true}
		// A persisted automatic decision is provenance, not an operator consent
		// that could bypass the current common policy on a later attempt.
		if current.approval.Mode != "automatic" {
			value.Approval = &executionauth.BoundApproval{Ref: current.approval.ID, Subject: subject,
				OperationFingerprint: finalFingerprint, Status: string(current.approval.Status)}
		}
		return value, nil
	})
	var decision executionauth.Decision
	started := false
	return func() error {
		// The manager rechecks the proposal's actual hashes and rooted paths on
		// each invocation. Reload the immutable native transaction too, rather
		// than trusting a stale application-level permission check.
		current, err := s.loadOperationBinding(ctx, prepared)
		if err != nil {
			return err
		}
		actual, err := fileEditOperation(prepared, current.edit, rootFingerprint)
		if err != nil {
			return err
		}
		actualFingerprint, err := toolcontract.FingerprintOperation(actual)
		if err != nil {
			return err
		}
		if !started {
			started = true
			// Defer activation until an actual mutation is needed. A manager
			// recovering already-published bytes must remain a read-only replay.
			approvalRef := binding.approval.ID
			if binding.approval.Mode == "automatic" {
				approvalRef = ""
			}
			decision, err = authorizer.Authorize(ctx, subject, operation, approvalRef)
			if err != nil {
				return err
			}
			if decision.Outcome != "allow" || decision.Validate() != nil {
				return apperror.New(apperror.CodePolicyDenied, "FileEdit operation was not authorized")
			}
			return decision.BeforeDispatch(ctx, actualFingerprint)
		}
		approvalRef := binding.approval.ID
		if binding.approval.Mode == "automatic" {
			approvalRef = ""
		}
		return authorizer.Recheck(ctx, subject, actual, approvalRef, decision.AuthorizationRef)
	}, nil
}

func fileEditOperation(prepared fileedit.ApplyOperation, edit fileedit.Edit,
	rootFingerprint string,
) (toolcontract.Operation, error) {
	raw, err := json.Marshal(struct {
		Prepared fileedit.ApplyOperation
		Root     string
	}{prepared, rootFingerprint})
	if err != nil {
		return toolcontract.Operation{}, err
	}
	effects := []toolcontract.Effect{toolcontract.EffectReversibleWrite}
	if edit.Operation == fileedit.OperationDelete {
		effects = []toolcontract.Effect{toolcontract.EffectDestructive}
	}
	targets := []toolcontract.Target{{Kind: "file", Locator: edit.WorkspaceID + ":" + edit.Path}}
	if edit.DestinationPath != "" {
		targets = append(targets, toolcontract.Target{Kind: "file", Locator: edit.WorkspaceID + ":" + edit.DestinationPath})
	}
	operation := toolcontract.Operation{ID: "file-edit:" + prepared.KeyDigest,
		Kind: toolcontract.OperationFileWrite, ToolID: fileedit.ApprovalToolName(edit),
		Component: toolcontract.ComponentRef{PackageID: "traverse-board", ComponentID: "workspace-files"},
		AdapterID: "host-rooted-files", AdapterRevision: "1",
		InputFingerprint: fileedit.HashText(string(raw)), Targets: targets, Effects: effects}
	return operation, operation.Validate()
}
