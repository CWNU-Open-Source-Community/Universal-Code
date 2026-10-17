package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

// Actual process and stdin effects use the common authorizer. Native immutable
// input pins and existing approval/job ledgers supply the host's evidence.
func (s *CommandRuntimeService) authorizedCommandStart(scope toolgateway.CommandRuntimeContext,
	bindings commandRuntimeBindings, operationKey string, spec runner.CommandRuntimeResolvedSpec,
) (runner.CommandRuntimeStartRequest, error) {
	runnerScope := s.runnerScope(scope, bindings, operationKey)
	_, operationID := runner.CommandRuntimeOperationIdentity(scope.RunID, operationKey)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return runner.CommandRuntimeStartRequest{}, err
	}
	prepare := func(actual runner.CommandRuntimeResolvedSpec) (toolcontract.Operation, error) {
		return commandOperation(key, operationID, s.adapter, struct {
			Scope runner.CommandRuntimeScope
			Spec  runner.CommandRuntimeResolvedSpec
		}{runnerScope, actual}, actual.Spec.Network,
			[]toolcontract.Target{{Kind: "process", Locator: actual.ExecutablePath},
				{Kind: "directory", Locator: actual.AbsoluteDirectory}})
	}
	operation, err := prepare(spec)
	if err != nil {
		return runner.CommandRuntimeStartRequest{}, err
	}
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionStart, Commands: []runner.CommandRuntimeSpec{spec.Spec}}
	check, err := s.commandOperationCheck(scope, bindings, operation, spec.Spec.Network, "", input, runner.CommandRuntimeSpecFingerprint(spec), func(source commandApprovalSource) bool {
		index := 0
		if source.input.Action == toolgateway.CommandRuntimeActionRun {
			index = -1
			for i := range source.input.Commands {
				if commandRuntimeBatchOperationKey(scope.OperationKey, i) == operationKey {
					index = i
					break
				}
			}
		} else if source.input.Action != toolgateway.CommandRuntimeActionStart || operationKey != scope.OperationKey {
			return false
		}
		return index >= 0 && index < len(source.authority.CommandFingerprints) && source.authority.CommandFingerprints[index] == runner.CommandRuntimeSpecFingerprint(spec)
	})
	if err != nil {
		return runner.CommandRuntimeStartRequest{}, err
	}
	return runner.CommandRuntimeStartRequest{Scope: runnerScope, Spec: spec,
		DispatchCheck: func(ctx context.Context, actual runner.CommandRuntimeResolvedSpec) error {
			prepared, err := prepare(actual)
			if err != nil {
				return err
			}
			return check(ctx, prepared, false)
		}, OwnershipCheck: func(ctx context.Context) error {
			return check(ctx, operation, true)
		}}, nil
}

func (s *CommandRuntimeService) commandStdinDispatchCheck(scope toolgateway.CommandRuntimeContext,
	bindings commandRuntimeBindings, input toolgateway.CommandRuntimeInput,
) (func(context.Context, string, []byte, bool) error, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	_, operationID := runner.CommandRuntimeOperationIdentity(scope.RunID, scope.OperationKey)
	prepare := func(jobID string, data []byte, closeAfter bool) (toolcontract.Operation, error) {
		return commandOperation(key, operationID, s.adapter, struct {
			JobID string
			Data  []byte
			Close bool
		}{jobID, data, closeAfter}, runner.CommandRuntimeNetworkDisabled,
			[]toolcontract.Target{{Kind: "process", Locator: jobID}})
	}
	operation, err := prepare(input.JobID, []byte(*input.Stdin), *input.CloseStdin)
	if err != nil {
		return nil, err
	}
	check, err := s.commandOperationCheck(scope, bindings, operation,
		runner.CommandRuntimeNetworkDisabled, input.JobID, input, "", func(source commandApprovalSource) bool {
			return source.input.Action == input.Action && source.input.JobID == input.JobID && source.input.Stdin != nil && source.input.CloseStdin != nil &&
				*source.input.Stdin == *input.Stdin && *source.input.CloseStdin == *input.CloseStdin
		})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, jobID string, data []byte, closeAfter bool) error {
		actual, err := prepare(jobID, data, closeAfter)
		if err != nil {
			return err
		}
		return check(ctx, actual, false)
	}, nil
}

func commandOperation(key []byte, id string, adapter commandruntimeadapter.Identity,
	input any, network runner.CommandRuntimeNetwork, targets []toolcontract.Target,
) (toolcontract.Operation, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return toolcontract.Operation{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("command-operation-input\x00"))
	_, _ = mac.Write(raw)
	effects := []toolcontract.Effect{toolcontract.EffectProcess, toolcontract.EffectUnknown}
	if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace &&
		adapter.IsolationGrade == commandruntimeadapter.IsolationWorkspaceSandbox &&
		adapter.NetworkPolicy == commandruntimeadapter.NetworkDenied &&
		adapter.CredentialPolicy == commandruntimeadapter.CredentialsNone &&
		network == runner.CommandRuntimeNetworkDisabled {
		effects = []toolcontract.Effect{toolcontract.EffectProcess, toolcontract.EffectWorkspaceRead,
			toolcontract.EffectReversibleWrite}
	}
	// A host cwd, network intent, or executable name is not isolation evidence.
	operation := toolcontract.Operation{ID: id, Kind: toolcontract.OperationProcess,
		ToolID: "command_runtime", Component: toolcontract.ComponentRef{PackageID: "builtin", ComponentID: "command_runtime"},
		AdapterID: adapter.BackendIdentity, AdapterRevision: adapter.Generation,
		InputFingerprint: hex.EncodeToString(mac.Sum(nil)), Targets: targets, Effects: effects}
	return operation, operation.Validate()
}

func (s *CommandRuntimeService) commandOperationCheck(scope toolgateway.CommandRuntimeContext,
	bindings commandRuntimeBindings, operation toolcontract.Operation, network runner.CommandRuntimeNetwork, jobID string,
	input toolgateway.CommandRuntimeInput, operatorSpecFingerprint string, pinned func(commandApprovalSource) bool,
) (func(context.Context, toolcontract.Operation, bool) error, error) {
	actor := scope.AgentID
	if actor == "" {
		actor = bindings.root.ID
	}
	subject := executionauth.SubjectRef{RunID: scope.RunID, ActorID: actor}
	expectedScope := s.runnerScope(scope, bindings, scope.OperationKey)
	expectedBinding, err := commandOperationBindingFingerprint(expectedScope)
	if err != nil {
		return nil, err
	}
	// Set only under the same lock that guards this operation's decision.
	// Ownership and dispatch keep the original AuthorizationRef; the caller
	// cannot select this phase through a tool payload or persisted receipt.
	ownershipContinuation := false
	authorizer := executionauth.NewPolicyAuthorizer(func(ctx context.Context, actualSubject executionauth.SubjectRef,
		_ toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		if actualSubject != subject || approvalRef != "" || !s.commandRuntimeAdapterCurrent() {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "command operation host authority changed")
		}
		var source commandApprovalSource
		var st commandApprovalStore
		var err error
		if bindings.permission.Mode.IsApprovalMode() && (scope.RequestedBy == "run_supervisor" || scope.RequestedBy == "specialist_runner") {
			var ok bool
			st, ok = s.store.(commandApprovalStore)
			if !ok || (scope.SupervisorToolCallID == "" && scope.SpecialistToolCallID == "") {
				return executionauth.OperationAuthority{}, errors.New("command operation source is unavailable")
			}
			sourceID := scope.SupervisorToolCallID
			if scope.RequestedBy == "specialist_runner" {
				sourceID = domain.SpecialistToolApprovalID(domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}, scope.SpecialistToolCallID)
			}
			source, err = readCommandSource(ctx, st, scope.RunID, sourceID, jobID == "" && input.Action == toolgateway.CommandRuntimeActionStart)
			if err != nil {
				return executionauth.OperationAuthority{}, err
			}
		}
		// An admitted background Job owns its lifetime independently of the
		// completed model turn. Keep its original immutable scope, then prove
		// current Run/permission/workspace and exact live process ownership below.
		// Every pending dispatch and later write_stdin still requires the active
		// attempt and current Run lease.
		current := bindings
		if !ownershipContinuation || source.call.Status != domain.SupervisorToolCompleted {
			current, err = s.loadAuthorizedBindings(ctx, scope, network == runner.CommandRuntimeNetworkHost)
			if err != nil {
				return executionauth.OperationAuthority{}, err
			}
			if (scope.RequestedBy == "run_supervisor" || scope.RequestedBy == "specialist_runner") && (scope.AgentID != current.actor.ID ||
				current.actor.Status != domain.AgentRunning || current.actor.ActiveAttemptID != scope.AgentAttemptID) {
				return executionauth.OperationAuthority{}, apperror.New(apperror.CodeConflict,
					"command operation Agent attempt changed before dispatch")
			}
		}
		binding, err := commandOperationBindingFingerprint(s.runnerScope(scope, current, scope.OperationKey))
		if err != nil || binding != expectedBinding {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodeConflict, "command operation scope changed before dispatch")
		}
		var activeJob runner.CommandRuntimeJob
		if jobID != "" {
			if _, err := s.authorizeActiveJob(ctx, jobID, current); err != nil {
				return executionauth.OperationAuthority{}, err
			}
			activeJob, err = s.store.GetCommandRuntimeJob(ctx, jobID)
			if err != nil {
				return executionauth.OperationAuthority{}, err
			}
		}
		var proof *approval.Record
		policyInput := input
		if current.permission.Mode.IsApprovalMode() && (scope.RequestedBy == "run_supervisor" || scope.RequestedBy == "specialist_runner") {
			if !commandSourceMatchesScope(source, scope, current, s.adapter) || pinned == nil || !pinned(source) ||
				(jobID != "" && source.authority.JobFingerprint != commandRuntimeJobFingerprint(activeJob)) {
				return executionauth.OperationAuthority{}, errors.New("command operation input or native source changed")
			}
			if source.specialist != nil && source.call.Status == domain.SupervisorToolPending {
				if err := s.validateWorkerCommandDispatch(ctx, scope, source); err != nil {
					return executionauth.OperationAuthority{}, err
				}
			}
			if source.call.Status == domain.SupervisorToolCompleted {
				// Returning action=start completes the tool call, not its owned
				// process. This is a continuation of the same admitted Job; a
				// completed call alone never authorizes another native dispatch.
				digest, ownedID := runner.CommandRuntimeOperationIdentity(scope.RunID, scope.OperationKey)
				owned, err := s.authorizeActiveJob(ctx, ownedID, current)
				if err != nil || operation.ID != ownedID || owned.State != runner.CommandRuntimeJobRunning ||
					owned.OperationDigest != digest || owned.InvocationID != scope.InvocationID ||
					owned.SpecFingerprint != source.authority.CommandFingerprints[0] ||
					owned.LeaseID != scope.LeaseID || owned.LeaseGeneration != scope.LeaseGeneration ||
					owned.LeaseOwnerID != expectedScope.LeaseOwnerID {
					return executionauth.OperationAuthority{}, errors.New("completed command start no longer owns its exact active Job")
				}
				if current, err := s.commandRuntimeJobBindingsCurrent(ctx, owned); err != nil || !current {
					return executionauth.OperationAuthority{}, errors.Join(err, errors.New("completed command start lost its current Run authority"))
				}
				// Stop revokes continuation in memory before the terminal record is
				// persisted. Ownership alone also permits stopping Jobs for cleanup.
				live, err := s.manager.Get(ctx, ownedID)
				if err != nil || live.State != runner.CommandRuntimeJobRunning {
					return executionauth.OperationAuthority{}, errors.New("completed command start no longer owns its exact active Job")
				}
			}
			policyInput = source.input
			record, err := st.GetApprovalByProposal(ctx, source.call.CallID)
			if err == nil {
				if !commandApprovalMatches(record, source) {
					return executionauth.OperationAuthority{}, errors.New("command operation approval identity changed")
				}
				if err := checkCommandGrant(ctx, st, record); err != nil {
					return executionauth.OperationAuthority{}, err
				}
				proof = &record
			} else if !errors.Is(err, sql.ErrNoRows) {
				return executionauth.OperationAuthority{}, err
			}
		}
		operatorProof := false
		if scope.RequestedBy == toolgateway.CommandRuntimeRequestedByOperator {
			consent, exists := ctx.Value(operatorCommandApprovalKey{}).(operatorCommandApproval)
			if !exists || consent.host != s || consent.invocationID != scope.InvocationID ||
				consent.operationKey != scope.OperationKey || consent.bindingFingerprint != binding ||
				operatorSpecFingerprint == "" || consent.specFingerprint != operatorSpecFingerprint {
				return executionauth.OperationAuthority{}, errors.New("operator command is missing its exact host invocation")
			}
			policyInput = consent.input
			operatorProof = consent.confirmed
		}
		policy := toolgateway.CommandRuntimePolicyDecision(s.commandRuntimePolicy(), policyInput)
		if !policy.Allowed || ((policy.NeedsApproval || policyInput.ReviewScope != nil) && proof == nil && !operatorProof) {
			return executionauth.OperationAuthority{}, apperror.New(apperror.CodePolicyDenied, "current command host policy requires denial or exact review")
		}
		projection, err := domain.ExecutionPermissionApproval(current.permission)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		value := executionauth.OperationAuthority{Mode: projection.Mode, BindingFingerprint: binding,
			RuntimeAvailable: true, FullActivated: projection.Mode == domain.ExecutionApprovalFull,
			EffectsVerified: s.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace}
		if proof != nil || operatorProof {
			fingerprint, err := toolcontract.FingerprintOperation(operation)
			if err != nil {
				return executionauth.OperationAuthority{}, err
			}
			ref, status := scope.InvocationID, approval.StatusApproved
			if proof != nil {
				ref, status = proof.ID, proof.Status
			}
			value.Approval = &executionauth.BoundApproval{Ref: ref, Subject: subject, OperationFingerprint: fingerprint, Status: string(status)}
		}
		return value, nil
	})
	var mu sync.Mutex
	var decision executionauth.Decision
	started := false
	denied := false
	return func(ctx context.Context, actual toolcontract.Operation, ownership bool) (err error) {
		mu.Lock()
		defer mu.Unlock()
		ownershipContinuation = ownership
		if denied {
			return errors.New("command operation dispatch was already denied")
		}
		defer func() { denied = err != nil }()
		if !started {
			started = true
			var err error
			decision, err = authorizer.Authorize(ctx, subject, operation, "")
			if err != nil {
				return err
			}
			if decision.Outcome != "allow" || decision.Validate() != nil {
				return apperror.New(apperror.CodePolicyDenied, "command operation was not authorized")
			}
			fingerprint, err := toolcontract.FingerprintOperation(actual)
			if err != nil {
				return err
			}
			return decision.BeforeDispatch(ctx, fingerprint)
		}
		if decision.AuthorizationRef == "" {
			return errors.New("command operation dispatch was already denied")
		}
		return authorizer.Recheck(ctx, subject, actual, "", decision.AuthorizationRef)
	}, nil
}

func commandOperationBindingFingerprint(value runner.CommandRuntimeScope) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
