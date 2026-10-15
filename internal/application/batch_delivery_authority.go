package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/workspace"
)

// Batch validators use the existing process-tree primitive and command operation
// contract. Package-manager offline settings and a child worktree cwd do not
// isolate child-authored Go/npm code from the host.
type batchValidationStarter struct {
	runner.OnceStarter
	service *BatchDeliveryService
	runID   string
}

func (s *batchValidationStarter) Start(ctx context.Context, spec runner.OnceStartSpec) (runner.OnceStartResult, error) {
	if err := s.service.requireBatchLocalHostValidation(ctx, s.runID); err != nil {
		return runner.OnceStartResult{}, err
	}
	// Freeze all executable inputs before the resolver can call external stores.
	spec.Argv = slices.Clone(spec.Argv)
	spec.Environment = slices.Clone(spec.Environment)
	operation, err := batchValidationOperation(spec.RequestFingerprint, spec,
		[]toolcontract.Target{{Kind: "process", Locator: spec.ExecutablePath},
			{Kind: "directory", Locator: spec.WorkingDirectory}})
	if err != nil {
		return runner.OnceStartResult{}, err
	}
	decision, err := s.service.batchValidationAuthorization(ctx, s.runID, spec.WorkingDirectory, operation)
	if err != nil {
		return runner.OnceStartResult{}, err
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return runner.OnceStartResult{}, err
	}
	// Authorize pins the exact current binding; BeforeDispatch rereads it after
	// command resolution, immediately before the shared native process starter.
	if err := decision.BeforeDispatch(ctx, fingerprint); err != nil {
		return runner.OnceStartResult{}, apperror.Wrap(apperror.CodePolicyDenied,
			"batch delivery validation authority changed before dispatch", err)
	}
	return s.OnceStarter.Start(ctx, spec)
}

func batchValidationOperation(id string, input any, targets []toolcontract.Target) (toolcontract.Operation, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return toolcontract.Operation{}, err
	}
	return commandOperation(key, id, commandruntimeadapter.HostUnsandboxed(runner.OnceExecutionProtocolVersion),
		input, runner.CommandRuntimeNetworkHost, targets)
}

func (s *BatchDeliveryService) authorizeBatchHostValidation(ctx context.Context, runID string) error {
	operation, err := batchValidationOperation("batch-validation-admission", runID,
		[]toolcontract.Target{{Kind: "process", Locator: "batch-host-validation"}})
	if err != nil {
		return err
	}
	_, err = s.batchValidationAuthorization(ctx, runID, "", operation)
	return err
}

func (s *BatchDeliveryService) batchValidationAuthorization(ctx context.Context,
	runID, workingRoot string, operation toolcontract.Operation,
) (executionauth.Decision, error) {
	if s == nil || s.store == nil || !s.hostValidationExecutionEnabled {
		return executionauth.Decision{}, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery go/npm validation requires explicitly enabled host execution")
	}
	if err := s.executionPermissionCapabilities.Validate(); err != nil {
		return executionauth.Decision{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"batch delivery execution capabilities are invalid", err)
	}
	// The actor is issued by the native route, never by the child or request.
	subject := executionauth.SubjectRef{RunID: runID, ActorID: "native-batch-validator"}
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context,
		actual executionauth.SubjectRef, _ toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		if actual != subject || approvalRef != "" {
			return executionauth.OperationAuthority{}, errors.New("batch validation subject changed")
		}
		run, err := s.activeBatchRun(checkCtx, runID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		permission, err := s.store.GetRunExecutionPermission(checkCtx, run.ID)
		if err != nil {
			return executionauth.OperationAuthority{}, apperror.Normalize(err)
		}
		profile, err := s.store.GetRunExecutionProfile(checkCtx, run.ID)
		if err != nil || profile.RunID != run.ID || profile.MissionID != run.MissionID || profile.Validate() != nil {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "batch validation execution profile changed")
		}
		if permission.RunID != run.ID || permission.MissionID != run.MissionID {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeFailedPrecondition,
				"batch delivery execution permission binding changed")
		}
		projection, err := domain.ExecutionPermissionApproval(permission)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		mission, err := s.store.GetMission(checkCtx, run.MissionID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		info, err := s.store.GetWorkspaceInfo(checkCtx, mission.WorkspaceID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if mission.ID != run.MissionID || info.ID != mission.WorkspaceID {
			return executionauth.OperationAuthority{}, errors.New("batch validation workspace binding changed")
		}
		sourceRoot, err := workspace.AgentCodeRootFingerprint(info.RootPath)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		var executionRoot string
		if workingRoot != "" {
			executionRoot, err = workspace.AgentCodeRootFingerprint(workingRoot)
			if err != nil {
				return executionauth.OperationAuthority{}, err
			}
		}
		caps := s.executionPermissionCapabilities
		generation, live := caps.FullAccessGeneration(permission)
		// Observe existing authority only. Recovery cannot issue or restore a
		// runtime grant/fence, even when a persisted preference still says Full.
		epoch := caps.RuntimeAuthority.RuntimeEpoch()
		fence, _ := caps.RuntimeAuthority.RunAuthorizationFence(run.ID)
		raw, err := json.Marshal(struct {
			Run                                               domain.Run
			MissionID, WorkspaceID, SourceRoot, ExecutionRoot string
			Permission                                        domain.RunExecutionPermissionSnapshot
			Profile                                           domain.RunExecutionProfileSnapshot
			RuntimeEpoch                                      string
			RuntimeFence, FullGeneration                      uint64
		}{run, mission.ID, info.ID, sourceRoot, executionRoot, permission, profile, epoch, fence, generation})
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		digest := sha256.Sum256(raw)
		return executionauth.OperationAuthority{Mode: projection.Mode,
			BindingFingerprint: hex.EncodeToString(digest[:]),
			RuntimeAvailable:   s.hostValidationExecutionEnabled && caps.AllowsSnapshot(permission),
			FullActivated:      live && generation != 0,
			EffectsVerified:    false}, nil
	})
	decision, err := authorizer.Authorize(ctx, subject, operation, "")
	if err != nil {
		return executionauth.Decision{}, err
	}
	if decision.Outcome != "allow" || decision.Validate() != nil {
		return executionauth.Decision{}, apperror.New(apperror.CodePolicyDenied,
			"batch delivery host validation denied: "+decision.ReasonCode)
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return executionauth.Decision{}, err
	}
	decision.BeforeDispatch = executionauth.NewRecheckingDispatchGuard(fingerprint, decision.BeforeDispatch, func(checkCtx context.Context) error {
		return authorizer.Recheck(checkCtx, subject, operation, "", decision.AuthorizationRef)
	}, "batch validation dispatch authority changed")
	return decision, nil
}
