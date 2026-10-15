package application

import (
	"context"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/workspace"
)

// This fingerprint extends the existing exact approval, not the plugin contract
// or database. Runtime epochs are issued only for a new explicit review. Cold
// recovery and execution can observe an existing epoch but cannot recreate it.
func (s *GitAdvancedService) operationApprovalFingerprint(ctx context.Context,
	value gitAdvancedAuthority, preview gitadvanced.Preview, reviewing bool,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runtime := s.permissionCapabilities.RuntimeAuthority
	if runtime == nil {
		return "", apperror.New(apperror.CodePolicyDenied, "Git runtime authorization is unavailable")
	}
	root, err := workspace.AgentCodeRootFingerprint(value.workspace.RootPath)
	if err != nil {
		return "", err
	}
	generation, live := s.permissionCapabilities.FullAccessGeneration(value.permission)
	if !live {
		return "", apperror.New(apperror.CodePolicyDenied, "Git runtime permission is inactive")
	}
	epoch := runtime.RuntimeEpoch()
	var fence uint64
	if reviewing {
		fence, err = runtime.IssueRunAuthorizationFence(value.run.ID)
		if err != nil {
			return "", err
		}
	} else {
		var found bool
		fence, found = runtime.RunAuthorizationFence(value.run.ID)
		if !found {
			return "", apperror.New(apperror.CodePolicyDenied, "Git review authority was revoked")
		}
	}
	raw, err := json.Marshal(struct {
		PreviewID, Root, RunID, MissionID, SessionID, WorkspaceID string
		Mode                                                      domain.RunModeSnapshot
		Profile                                                   domain.RunExecutionProfileSnapshot
		Permission                                                domain.RunExecutionPermissionSnapshot
		LeaseID, OwnerID, OperatorThreadID                        string
		LeaseGeneration                                           int64
		RuntimeEpoch                                              string
		RuntimeFence, FullGeneration                              uint64
	}{preview.ID, root, value.run.ID, value.mission.ID, value.session.ID, value.workspace.ID,
		value.mode, value.profile, value.permission, value.lease.LeaseID, value.lease.OwnerID,
		s.operatorThreadID, value.lease.Generation, epoch, fence, generation})
	if err != nil {
		return "", err
	}
	base := gitadvanced.Fingerprint("authorized-native-git.v2", string(raw))
	if preview.Spec.Operation != gitadvanced.BisectRun {
		return base, nil
	}
	if reviewing {
		if err := s.requireBisectRuntime(ctx, value.run.ID, value.workspace.RootPath, preview.Spec); err != nil {
			return "", err
		}
	}
	selected, err := s.selectBisectCommandRuntime(ctx, value.run.ID)
	if err != nil {
		return "", err
	}
	backend, err := json.Marshal(struct {
		Adapter commandruntimeadapter.Identity
	}{selected.adapter})
	if err != nil {
		return "", err
	}
	return gitadvanced.Fingerprint("authorized-native-git-bisect.v1", base, string(backend)), nil
}

func gitAdvancedApprovalMatches(record gitadvanced.OperationRecord, proof approval.Record) bool {
	return proof.ProposalID == record.ID && proof.RunID == record.RunID &&
		proof.SessionID == record.SessionID && proof.WorkspaceID == record.WorkspaceID &&
		proof.ToolName == gitadvanced.ApprovalToolName && proof.ActionClass == gitadvanced.ApprovalActionClass &&
		proof.Mode == "per_call" && proof.GrantID == "" && proof.RequestFingerprint == record.ApprovalFingerprint
}

func (s *GitAdvancedService) operationDispatchGuard(ctx context.Context,
	request GitAdvancedExecuteRequest, record gitadvanced.OperationRecord, preview gitadvanced.Preview,
) (toolcontract.DispatchGuard, error) {
	operation, err := repository.AdvancedOperation(preview)
	if err != nil {
		return nil, err
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return nil, err
	}
	// This identity is host-issued for the native operator route; RequestedBy
	// from a request body never becomes actor authority or an approval proof.
	subject := executionauth.SubjectRef{RunID: record.RunID, ActorID: "native-git-operator"}
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context,
		actual executionauth.SubjectRef, _ toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		if err := ctx.Err(); err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if actual != subject || approvalRef != request.ApprovalID {
			return executionauth.OperationAuthority{}, errors.New("Git operation subject or approval changed")
		}
		current, err := s.loadMutationAuthority(checkCtx, request.RunID, request.Scope)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		binding, err := s.operationApprovalFingerprint(checkCtx, current, preview, false)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if binding != record.ApprovalFingerprint {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeConflict,
				"Git native authority changed after review")
		}
		stored, found, err := s.store.GetGitAdvancedOperation(checkCtx, record.ID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if !found || stored.RequestFingerprint != record.RequestFingerprint ||
			stored.PreviewJSON != record.PreviewJSON || stored.SpecJSON != record.SpecJSON ||
			(stored.Status != gitadvanced.OperationProposed && stored.Status != gitadvanced.OperationRunning) {
			return executionauth.OperationAuthority{}, errors.New("Git immutable operation changed or already completed")
		}
		proof, err := s.store.GetApproval(checkCtx, approvalRef)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if !gitAdvancedApprovalMatches(stored, proof) {
			return executionauth.OperationAuthority{}, errors.New("Git exact one-time approval binding changed")
		}
		projection, err := domain.ExecutionPermissionApproval(current.permission)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		return executionauth.OperationAuthority{Mode: projection.Mode, BindingFingerprint: binding,
			RuntimeAvailable: true, FullActivated: projection.Mode == domain.ExecutionApprovalFull,
			EffectsVerified: false, Approval: &executionauth.BoundApproval{Ref: proof.ID, Subject: subject,
				OperationFingerprint: fingerprint, Status: string(proof.Status)}}, nil
	})
	decision, err := authorizer.Authorize(ctx, subject, operation, request.ApprovalID)
	if err != nil {
		return nil, err
	}
	if decision.Outcome != "allow" || decision.Validate() != nil {
		return nil, apperror.New(apperror.CodePolicyDenied, "Git requires exact operator approval")
	}
	return executionauth.NewRecheckingDispatchGuard(fingerprint, decision.BeforeDispatch, func(checkCtx context.Context) error {
		return authorizer.Recheck(checkCtx, subject, operation, request.ApprovalID, decision.AuthorizationRef)
	}, "Git dispatch authority was denied or its inputs changed"), nil
}
