package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// OperatorCommandRequest carries the operator's exact command. Lease, actor,
// process epoch and native input pins are resolved by the host, never by input.
type OperatorCommandRequest struct {
	RunID, OperationKey, RequestedBy string
	Command                          runner.CommandRuntimeSpec
	ConfirmExecution                 bool
}

type OperatorCommandResult struct {
	Job      runner.CommandRuntimeJob
	Replayed bool
}

type operatorCommandApprovalKey struct{}
type operatorCommandApproval struct {
	confirmed                                                       bool
	input                                                           toolgateway.CommandRuntimeInput
	host                                                            *CommandRuntimeService
	invocationID, operationKey, bindingFingerprint, specFingerprint string
	runStatus                                                       domain.RunStatus
	lease                                                           domain.RunExecutionLease
}

func (s *CommandRuntimeService) operatorCompletesCommandBoundary(ctx context.Context, scope toolgateway.CommandRuntimeContext) bool {
	proof, ok := ctx.Value(operatorCommandApprovalKey{}).(operatorCommandApproval)
	return ok && proof.host == s && proof.invocationID == scope.InvocationID &&
		proof.operationKey == scope.OperationKey && scope.RequestedBy == toolgateway.CommandRuntimeRequestedByOperator
}

// Only the foreground operator entry can create this evidence. A requested_by
// string, persisted actor row or Job discriminator cannot substitute for it.
func operatorCommandOwnsStoppedRun(ctx context.Context, run domain.Run, lease domain.RunExecutionLease) bool {
	proof, ok := ctx.Value(operatorCommandApprovalKey{}).(operatorCommandApproval)
	return ok && proof.host != nil && proof.confirmed && proof.runStatus == run.Status &&
		(run.Status == domain.RunCreated || run.Status == domain.RunPaused) &&
		lease.RunID == run.ID && proof.lease.RunID == run.ID &&
		proof.lease.LeaseID == lease.LeaseID && proof.lease.Generation == lease.Generation &&
		proof.lease.OwnerID == lease.OwnerID && ctx.Err() == nil
}

// RunOperatorCommand is a foreground adapter to the existing command runtime.
// A terminal receipt is readable without fresh authority. Any previous intent
// without a conclusive result is never turned into another native start.
func (s *CommandRuntimeService) RunOperatorCommand(ctx context.Context, request OperatorCommandRequest) (OperatorCommandResult, error) {
	return s.runOperatorCommand(ctx, request, nil)
}

func (s *CommandRuntimeService) runOperatorCommand(ctx context.Context, request OperatorCommandRequest, started func(OperatorCommandResult)) (OperatorCommandResult, error) {
	return s.runPreparedOperatorCommand(ctx, request, started, nil, nil)
}

// Preparation hooks are private Go-owned work under the same execution lease.
// They are never supplied by an HTTP body, repository or model tool call.
func (s *CommandRuntimeService) runPreparedOperatorCommand(ctx context.Context, request OperatorCommandRequest, started func(OperatorCommandResult), before func(context.Context, string) error, after func(context.Context, string) error) (OperatorCommandResult, error) {
	var result OperatorCommandResult
	if s == nil || s.store == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition, "operator command store is unavailable")
	}
	prepared, err := normalizeOperatorCommandRequest(request)
	if err != nil {
		return result, err
	}
	if receipt, found, err := readOperatorCommandReceipt(ctx, s.store, prepared); found || err != nil {
		return receipt, err
	}
	// Each operator invocation owns one Job, including commands whose budget
	// or retained output exceeds the model-facing foreground batch limits.
	spec, invocationID := prepared.spec, prepared.invocationID
	operationKey := commandRuntimeBatchOperationKey(prepared.operationKey, 0)
	leaseStore, ok := s.store.(RunExecutionLeaseStore)
	if !ok || !s.adapter.Executable() || s.capabilities.RuntimeAuthority == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition, "operator command requires an installed command runtime and Run lease service")
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return result, err
	}
	if run.Status != domain.RunRunning && run.Status != domain.RunCreated && run.Status != domain.RunPaused {
		return result, apperror.New(apperror.CodeFailedPrecondition, "operator commands require a Running, Created or Paused Run; no Run state was changed")
	}
	if run.Status != domain.RunRunning && !request.ConfirmExecution {
		return result, apperror.New(apperror.CodePolicyDenied, "commands on a stopped Run require exact operator confirmation")
	}
	err = withRunExecutionLease(ctx, leaseStore, run.ID, idgen.New("operator-command"), DefaultRunExecutionLeasePolicy(), func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
		mission, err := s.store.GetMission(leaseCtx, run.MissionID)
		if err != nil {
			return err
		}
		root, found, err := s.store.GetRootAgent(leaseCtx, run.ID)
		if err != nil || !found {
			return errors.Join(err, errors.New("operator command Root authority anchor is unavailable"))
		}
		permission, err := s.store.GetRunExecutionPermission(leaseCtx, run.ID)
		if err != nil {
			return err
		}
		if !permission.Mode.IsApprovalMode() {
			return apperror.New(apperror.CodePolicyDenied, "new operator commands require ask, auto, or full")
		}
		snapshot, generation, epoch, fence, live := bindAgentCodeRuntime(s.capabilities, permission)
		if !live {
			return apperror.New(apperror.CodePolicyDenied, "operator command runtime authority is unavailable")
		}
		scope := toolgateway.CommandRuntimeContext{InvocationID: invocationID, OperationKey: operationKey,
			RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID,
			RootAgentID: root.ID, AgentID: root.ID, PermissionMode: permission.Mode, PermissionRevision: permission.Revision,
			PermissionSnapshotID: snapshot, PermissionGeneration: generation, PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence,
			LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, CapabilityGeneration: s.adapter.Generation,
			RequestedBy: toolgateway.CommandRuntimeRequestedByOperator, Adapter: s.adapter,
			PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "high", Reason: "operator command still requires native operation authorization"}}
		mode, err := s.store.GetRunMode(leaseCtx, run.ID)
		if err != nil {
			return err
		}
		scope.Surface, scope.Phase, scope.Profile, scope.Role, scope.ModeRevision = mode.Surface, mode.Phase, mode.Profile, root.Role, mode.Revision
		consent := operatorCommandApproval{host: s, invocationID: invocationID, operationKey: operationKey,
			confirmed: request.ConfirmExecution, runStatus: run.Status, lease: lease}
		leaseCtx = context.WithValue(leaseCtx, operatorCommandApprovalKey{}, consent)
		bindings, err := s.loadAuthorizedBindings(leaseCtx, scope, spec.Network == runner.CommandRuntimeNetworkHost)
		if err != nil {
			return err
		}
		if before != nil {
			if err := before(leaseCtx, bindings.rootPath); err != nil {
				return err
			}
		}
		resolved, err := s.normalizeCommandRuntimeSpec(spec, bindings.rootPath)
		if err != nil {
			return err
		}
		input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
			Action: toolgateway.CommandRuntimeActionStart, Commands: []runner.CommandRuntimeSpec{spec}}
		binding, err := commandOperationBindingFingerprint(s.runnerScope(scope, bindings, scope.OperationKey))
		if err != nil {
			return err
		}
		// Host evidence is private to this invocation and binds the original
		// action as well as native input pins. Public scopes cannot fabricate it.
		consent.bindingFingerprint, consent.specFingerprint, consent.input = binding, runner.CommandRuntimeSpecFingerprint(resolved), input
		leaseCtx = context.WithValue(leaseCtx, operatorCommandApprovalKey{}, consent)
		// Reuse the same operation check before preparing a Job as at the native
		// sink. A missing operator review must not consume the operation key.
		start, err := s.authorizedCommandStart(scope, bindings, operationKey, resolved)
		if err != nil {
			return err
		}
		if err = start.DispatchCheck(leaseCtx, resolved); err != nil {
			return err
		}
		value, err := s.ExecuteCommandRuntime(leaseCtx, scope, input)
		if len(value.Jobs) == 1 {
			if started != nil {
				job, readErr := s.store.GetCommandRuntimeJob(context.WithoutCancel(leaseCtx), value.Jobs[0].ID)
				if readErr == nil {
					started(OperatorCommandResult{Job: job, Replayed: value.Replayed})
				}
			}
			if err == nil {
				err = s.waitForOperatorCommand(leaseCtx, value.Jobs[0].ID)
			} else {
				// Receipt assembly can fail after Start has committed an owned Job.
				err = errors.Join(err, s.cancelForegroundJob(value.Jobs[0].ID))
			}
			job, readErr := s.store.GetCommandRuntimeJob(context.WithoutCancel(leaseCtx), value.Jobs[0].ID)
			result = OperatorCommandResult{Job: job, Replayed: value.Replayed}
			err = errors.Join(err, readErr)
			if err == nil && job.State == runner.CommandRuntimeJobCompleted && job.TreeReaped && job.ExitCode != nil && *job.ExitCode == 0 && after != nil {
				err = after(leaseCtx, bindings.rootPath)
			}
			if readErr == nil && job.State.Terminal() {
				err = errors.Join(err, s.completeCommandRuntimeJobBoundary(context.WithoutCancel(leaseCtx), job))
			}
		}
		return err
	})
	return result, err
}

func (s *CommandRuntimeService) waitForOperatorCommand(ctx context.Context, jobID string) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, s.cancelForegroundJob(jobID))
		}
	}()
	var cursor uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, page, err := s.manager.Wait(ctx, jobID, runner.MaxCommandRuntimeWait, cursor, toolgateway.MaxCommandRuntimePageBytes)
		if err != nil {
			return err
		}
		if page.NextCursor < cursor || page.NextCursor > page.EndCursor ||
			(job.State.Terminal() && page.NextCursor < page.EndCursor && page.NextCursor == cursor) {
			return runner.ErrCommandRuntimeUncertain
		}
		cursor = page.NextCursor
		// Drain every retained page, including pages after process exit. Output
		// assembly stays in the manager's existing bounded per-stream capture;
		// the returned Job carries those prefixes and any truncation reason.
		if job.State.Terminal() && cursor == page.EndCursor {
			return nil
		}
	}
}

type operatorCommandReceiptStore interface {
	GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error)
}

type preparedOperatorCommand struct {
	spec                                                      runner.CommandRuntimeSpec
	runID, operationKey, invocationID, operationDigest, jobID string
}

func normalizeOperatorCommandRequest(request OperatorCommandRequest) (preparedOperatorCommand, error) {
	var prepared preparedOperatorCommand
	if !domain.ValidAgentID(request.RunID) || !domain.ValidAgentID(request.OperationKey) || !domain.ValidAgentID(request.RequestedBy) {
		return prepared, apperror.New(apperror.CodeInvalidArgument, "operator command identity is invalid")
	}
	if request.Command.StdinPolicy != runner.CommandRuntimeStdinClosed || request.Command.InitialStdin != "" {
		return prepared, apperror.New(apperror.CodeInvalidArgument, "operator command requires closed stdin")
	}
	var err error
	prepared.spec, err = runner.NormalizeCommandRuntimeIntent(request.Command)
	if err != nil {
		return prepared, err
	}
	raw, err := json.Marshal(prepared.spec)
	if err != nil {
		return prepared, err
	}
	prepared.runID = request.RunID
	prepared.operationKey = "operator-command-" + runmutation.Fingerprint("operator_command_key.v1", request.RunID, request.OperationKey)
	prepared.invocationID = "operator-command-" + runmutation.Fingerprint("operator_command_request.v1", request.RunID, request.OperationKey, request.RequestedBy, string(raw))
	prepared.operationDigest, prepared.jobID = runner.CommandRuntimeOperationIdentity(request.RunID, commandRuntimeBatchOperationKey(prepared.operationKey, 0))
	return prepared, nil
}

// ReadOperatorCommand reads an exact saved command without a runtime adapter,
// lease, executable file or fresh permission activation. It grants no authority.
func ReadOperatorCommand(ctx context.Context, store operatorCommandReceiptStore, request OperatorCommandRequest) (OperatorCommandResult, bool, error) {
	prepared, err := normalizeOperatorCommandRequest(request)
	if err != nil {
		return OperatorCommandResult{}, false, err
	}
	return readOperatorCommandReceipt(ctx, store, prepared)
}

func readOperatorCommandReceipt(ctx context.Context, store operatorCommandReceiptStore, request preparedOperatorCommand) (OperatorCommandResult, bool, error) {
	if ctx == nil || store == nil {
		return OperatorCommandResult{}, false, apperror.New(apperror.CodeInvalidArgument, "operator command receipt reader is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return OperatorCommandResult{}, false, err
	}
	job, err := store.GetCommandRuntimeJob(ctx, request.jobID)
	if errors.Is(err, runner.ErrCommandRuntimeJobNotFound) || errors.Is(err, sql.ErrNoRows) || apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound {
		return OperatorCommandResult{}, false, nil
	}
	if err != nil {
		return OperatorCommandResult{}, false, err
	}
	if job.RunID != request.runID || job.OperationDigest != request.operationDigest || job.InvocationID != request.invocationID || job.Validate() != nil {
		return OperatorCommandResult{}, true, apperror.New(apperror.CodeConflict, "operator command key was already used for another request")
	}
	result := OperatorCommandResult{Job: job, Replayed: true}
	if !job.State.Terminal() || job.State == runner.CommandRuntimeJobInterrupted {
		return result, true, runner.ErrCommandRuntimeUncertain
	}
	return result, true, nil
}
