package application

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

type workerCommandStore interface {
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
	GetSession(context.Context, string) (session.Session, error)
	GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	GetSpecialistToolCall(context.Context, domain.AgentAttemptRef, string) (domain.SpecialistToolCall, error)
	GetSpecialistApprovalCall(context.Context, string, string) (domain.SpecialistToolCall, bool, error)
	ValidateSpecialistToolSource(context.Context, domain.SpecialistToolSource) (domain.SpecialistToolCall, error)
	SetSpecialistToolApproval(context.Context, domain.AgentAttemptRef, string, string) (domain.SpecialistToolCall, error)
}

type commandJobActorStore interface {
	GetCommandRuntimeJobAttribution(context.Context, string) (domain.AgentAttribution, error)
}

// SpecialistCommandOperationKey is stable across approval waits and recovery.
// Provider call IDs are only unique within their recorded child attempt.
func SpecialistCommandOperationKey(call domain.SpecialistToolCall) string {
	return domain.SpecialistToolApprovalID(call.Ref, call.CallID)
}

func (s *CommandRuntimeService) BindSpecialistCommandToolAuthority(ctx context.Context,
	ref domain.AgentAttemptRef, name toolgateway.ToolName, raw, payload json.RawMessage,
) (json.RawMessage, error) {
	if ref.Validate() != nil || !toolgateway.IsCommandExecutionTool(name) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "worker command identity is invalid")
	}
	a, err := commandruntimeadapter.DecodeAuthority(raw)
	if err != nil || a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion || a.RunID != ref.RunID {
		return nil, apperror.New(apperror.CodePolicyDenied, "worker command requires current native operation authority")
	}
	st, ok := s.store.(workerCommandStore)
	if !ok {
		return nil, errors.New("worker command storage is unavailable")
	}
	child, err := st.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return nil, err
	}
	if !child.Worker.AllowsTool(string(name)) {
		return nil, apperror.New(apperror.CodePolicyDenied, "worker command is outside its delegated tool profile")
	}
	w := commandruntimeadapter.WorkerAuthority{AgentID: ref.AgentID, AttemptID: ref.AttemptID, SessionID: child.SessionID}
	if a.Worker != nil && *a.Worker != w {
		return nil, errors.New("worker command actor cannot be rebound")
	}
	a.Worker = &w
	encoded, err := commandruntimeadapter.EncodeAuthority(a)
	if err != nil {
		return nil, err
	}
	return s.BindCommandToolAuthority(ctx, name, encoded, payload)
}

func (m *CommandRuntimeMultiplexer) BindSpecialistCommandToolAuthority(ctx context.Context,
	ref domain.AgentAttemptRef, name toolgateway.ToolName, raw, payload json.RawMessage,
) (json.RawMessage, error) {
	a, err := commandruntimeadapter.DecodeAuthority(raw)
	if err != nil {
		return nil, err
	}
	for _, s := range m.adapters {
		if s.adapter.SameBackend(a.Adapter) {
			return s.BindSpecialistCommandToolAuthority(ctx, ref, name, raw, payload)
		}
	}
	return nil, apperror.New(apperror.CodeConflict, "worker command adapter changed")
}

func (s *CommandRuntimeService) bindWorkerCommandRuntime(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, b commandRuntimeBindings,
) (commandRuntimeBindings, error) {
	ref := domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}
	worker, err := readWorkerExecutionContext(ctx, s.store, ref, s.drydocks)
	if err != nil {
		return b, err
	}
	st, ok := s.store.(workerCommandStore)
	if !ok {
		return b, errors.New("worker command source store unavailable")
	}
	attempt, found, err := st.GetAgentAttempt(ctx, ref.AttemptID)
	if err != nil || !found || attempt.RunID != ref.RunID || attempt.AgentID != ref.AgentID ||
		attempt.ParentAgentID != b.root.ID || attempt.Status != domain.AgentAttemptRunning ||
		attempt.LeaseID != scope.LeaseID || attempt.LeaseGeneration != scope.LeaseGeneration ||
		worker.Run.ID != b.run.ID || worker.Mission.ID != b.mission.ID || worker.Child.ParentID != b.root.ID ||
		scope.RootAgentID != b.root.ID || scope.AgentID == scope.RootAgentID || scope.SessionID != worker.Child.SessionID ||
		worker.Binding.WorkspaceID != b.workspace.ID {
		return b, errors.Join(err, apperror.New(apperror.CodeConflict, "worker command actor, attempt or workspace changed"))
	}
	b.actor, b.worker, b.rootPath = worker.Child, &worker.Binding, worker.Files.Workspace.RootPath
	b.rootSHA256, err = runner.CommandRuntimeWorkspaceRootSHA256(b.rootPath)
	return b, err
}

func (s *CommandRuntimeService) authorizeCommandJobActor(ctx context.Context,
	job runner.CommandRuntimeJob, b commandRuntimeBindings,
) error {
	actors, ok := s.store.(commandJobActorStore)
	if !ok {
		if b.worker == nil {
			return nil // Legacy embedders have no child execution capability.
		}
		return errors.New("worker command Job actor reader is unavailable")
	}
	actor, err := actors.GetCommandRuntimeJobAttribution(ctx, job.ID)
	if err != nil {
		return err
	}
	if b.worker == nil && actor.Source == domain.AgentAttributionLegacyUnknown {
		return nil
	}
	if actor.AgentID != b.actor.ID || (b.worker != nil && actor.Source != domain.AgentAttributionRecorded) {
		return apperror.New(apperror.CodeNotFound, "command Job does not belong to this Agent")
	}
	if b.worker != nil {
		current, err := readWorkerExecutionContext(ctx, s.store, b.worker.Ref, s.drydocks)
		if err != nil || current.Binding != *b.worker {
			return apperror.New(apperror.CodeNotFound, "command Job requires the current worker attempt")
		}
		origin := domain.AgentAttemptRef{RunID: job.RunID, AgentID: actor.AgentID, AttemptID: actor.AgentAttemptID}
		owned, err := readWorkerResourceContext(ctx, s.store, origin, s.drydocks)
		if err != nil || workerResourceFingerprint(owned.Binding) != workerResourceFingerprint(*b.worker) ||
			job.RunID != b.run.ID || job.SessionID != b.actor.SessionID || job.WorkspaceRootSHA256 != b.rootSHA256 {
			return apperror.New(apperror.CodeNotFound, "command Job does not belong to this worker resource scope")
		}
	}
	return nil
}

// nil means this is an ordinary root Job. A concrete false rejects a child
// Job whose worker, selected worktree or permission no longer lives.
func (s *CommandRuntimeService) workerCommandJobBindingsCurrent(ctx context.Context,
	job runner.CommandRuntimeJob,
) (*bool, error) {
	actors, ok := s.store.(commandJobActorStore)
	if !ok {
		return nil, nil
	}
	actor, err := actors.GetCommandRuntimeJobAttribution(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if actor.AgentID == "" || actor.AgentID == job.RootAgentID {
		return nil, nil
	}
	current := false
	if actor.Source != domain.AgentAttributionRecorded || actor.AgentAttemptID == "" {
		return &current, nil
	}
	b, err := s.loadCommandRuntimeBindings(ctx, job.RunID)
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodePolicyDenied {
			return &current, nil
		}
		return &current, err
	}
	ref := domain.AgentAttemptRef{RunID: job.RunID, AgentID: actor.AgentID, AttemptID: actor.AgentAttemptID}
	worker, err := readWorkerResourceContext(ctx, s.store, ref, s.drydocks)
	if err != nil {
		// A stale child is a revoked lifetime, not a reconciler failure.
		if apperror.CodeOf(err) == apperror.CodeConflict || apperror.CodeOf(err) == apperror.CodePolicyDenied ||
			apperror.CodeOf(err) == apperror.CodeFailedPrecondition {
			return &current, nil
		}
		return &current, err
	}
	rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(worker.Files.Workspace.RootPath)
	if err != nil {
		return &current, nil
	}
	// Releasing a foreground Run lease to await review does not transfer or
	// revoke an already admitted native Job. Its original attempt and target
	// remain immutable; every new dispatch/stdin still needs the current lease.
	current = b.rootFound && worker.Child.ParentID == b.root.ID && b.root.ID == job.RootAgentID &&
		worker.Child.SessionID == job.SessionID && worker.Binding.WorkspaceID == job.WorkspaceID &&
		(worker.Run.Status == domain.RunRunning || worker.Run.Status == domain.RunWaitingApproval) &&
		worker.Mission.ID == job.MissionID && worker.Run.MissionID == job.MissionID &&
		rootSHA == job.WorkspaceRootSHA256 && b.mode.ID == job.ModeSnapshotID && b.mode.Revision == job.ModeRevision &&
		b.mode.Surface == domain.ExecutionSurfaceCode && (b.mode.Phase == domain.ExecutionPhaseDeliver ||
		b.mode.Phase == domain.ExecutionPhasePlan && s.projectTestJobCurrent(job)) &&
		b.profile.ID == job.ProfileSnapshotID && b.profile.Revision == job.ProfileRevision &&
		b.profile.Profile == commandRuntimeExecutionProfile(s.adapter) && b.profile.NetworkScope == domain.ExecutionNetworkDisabled &&
		b.permission.ID == job.PermissionSnapshotID && b.permission.Revision == job.PermissionRevision && b.permission.Mode == job.PermissionMode &&
		s.capabilities.AllowsSnapshot(b.permission) && commandRuntimeJobGrantMatches(s.capabilities, b.permission, job) &&
		s.commandRuntimeAdapterCurrent() && job.Adapter.SameBackend(s.adapter) && s.adapter.AllowsPermission(job.PermissionMode)
	return &current, nil
}

type workerCommandRuntimeKey struct{}
type workerCommandRuntimeProof struct {
	service       *CommandRuntimeService
	scope         toolgateway.CommandRuntimeContext
	binding       commandRuntimeBindings
	nativeScope   runner.CommandRuntimeScope
	commandSHA256 string
}

func workerCommandRuntimeFromContext(ctx context.Context) *workerCommandRuntimeProof {
	proof, _ := ctx.Value(workerCommandRuntimeKey{}).(*workerCommandRuntimeProof)
	return proof
}

func (s *CommandRuntimeService) withWorkerCommandRuntime(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, b commandRuntimeBindings,
) context.Context {
	return context.WithValue(ctx, workerCommandRuntimeKey{}, &workerCommandRuntimeProof{service: s, scope: scope, binding: b})
}

func (p *workerCommandRuntimeProof) check(ctx context.Context, scope runner.CommandRuntimeScope,
	root string,
) error {
	if p == nil || p.service == nil || p.binding.worker == nil ||
		scope.RunID != p.scope.RunID || scope.AgentID != p.scope.AgentID || scope.AgentAttemptID != p.scope.AgentAttemptID ||
		scope.SessionID != p.scope.SessionID || scope.RootAgentID != p.scope.RootAgentID || root != p.binding.rootPath ||
		scope.WorkspaceRootSHA256 != p.binding.rootSHA256 {
		return fmt.Errorf("%w: worker runtime target does not match its captured actor and workspace", runner.ErrCommandRuntimeBoundary)
	}
	current, err := p.service.loadAuthorizedBindings(ctx, p.scope, false)
	if err != nil || current.worker == nil || *current.worker != *p.binding.worker || current.rootPath != root || current.rootSHA256 != p.binding.rootSHA256 {
		return errors.Join(err, fmt.Errorf("%w: worker runtime workspace or capability binding changed", runner.ErrCommandRuntimeBoundary))
	}
	return nil
}

// The Job records the child Session. The sandbox's mount/lease resource still
// belongs to the parent Run Session and is never relabelled as child-owned.
func workerCommandSandboxWorkspace(ctx context.Context, owned runworktree.Workspace,
	scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec,
) (runworktree.Workspace, string, error) {
	proof := workerCommandRuntimeFromContext(ctx)
	if proof == nil {
		return owned, scope.SessionID, nil
	}
	if err := proof.check(ctx, scope, spec.WorkspaceRoot); err != nil {
		return runworktree.Workspace{}, "", err
	}
	owned.Path = proof.binding.rootPath
	owned.RootFingerprint = proof.binding.worker.RootFingerprint
	return owned, proof.binding.run.SessionID, nil
}

func readWorkerCommandSource(ctx context.Context, base commandApprovalStore,
	runID, sourceID string, allowCompletedStart bool,
) (commandApprovalSource, error) {
	var out commandApprovalSource
	st, ok := base.(workerCommandStore)
	if !ok {
		return out, pendingToolApprovalUnavailable("command approval source was not found")
	}
	c, started, err := st.GetSpecialistApprovalCall(ctx, runID, sourceID)
	if err != nil {
		return out, err
	}
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil || a.Worker == nil || a.Worker.AgentID != c.Ref.AgentID || a.Worker.AttemptID != c.Ref.AttemptID ||
		c.Ref.RunID != runID || c.ApprovalSourceID != sourceID || sourceID != domain.SpecialistToolApprovalID(c.Ref, c.CallID) {
		return out, pendingToolApprovalUnavailable("worker command approval actor or intent changed")
	}
	input, canonical, err := toolgateway.CommandToolInput(toolgateway.ToolName(c.Name), json.RawMessage(c.PayloadJSON), a)
	if err != nil || string(canonical) != c.PayloadJSON || a.ScopeFingerprint == "" ||
		len(a.CommandFingerprints) != len(input.Commands) || ((input.JobID == "") != (a.JobFingerprint == "")) {
		return out, pendingToolApprovalUnavailable("worker command input is not its pinned intent")
	}
	completed := allowCompletedStart && started && c.Status == domain.SupervisorToolCompleted &&
		c.CompletedAt != nil && c.ErrorCode == "" && input.Action == toolgateway.CommandRuntimeActionStart && len(input.Commands) == 1
	if !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(c.Name)) || (c.Status != domain.SupervisorToolPending && !completed) {
		return out, pendingToolApprovalUnavailable("worker command call is not pending")
	}
	run, err := base.GetRun(ctx, runID)
	if err != nil {
		return out, err
	}
	mission, err := base.GetMission(ctx, run.MissionID)
	if err != nil {
		return out, err
	}
	child, err := st.GetAgentNode(ctx, c.Ref.AgentID)
	if err != nil {
		return out, err
	}
	binding, err := st.GetSpecialistToolBinding(ctx, c.Ref)
	if err != nil {
		return out, err
	}
	attempt, found, err := st.GetAgentAttempt(ctx, c.Ref.AttemptID)
	if err != nil || !found || attempt.RunID != runID || attempt.AgentID != child.ID || attempt.Status != domain.AgentAttemptRunning ||
		child.RunID != runID || child.Role != domain.AgentRoleSpecialist || child.Worker == nil || child.Terminal() ||
		child.ActiveAttemptID != c.Ref.AttemptID || (child.Status != domain.AgentRunning && child.Status != domain.AgentWaiting) ||
		binding.Ref != c.Ref || binding.Mode != "worker" || binding.SessionID != child.SessionID || binding.WorkspaceID != mission.WorkspaceID ||
		a.Worker.SessionID != child.SessionID || run.Terminal() {
		return out, pendingToolApprovalUnavailable("worker command active actor or binding changed")
	}
	permission, err := base.GetRunExecutionPermission(ctx, runID)
	if err != nil {
		return out, err
	}
	if permission.Validate() != nil || permission.ID != a.PermissionSnapshotID || permission.Revision != a.PermissionRevision || permission.Mode != a.PermissionMode {
		return out, pendingToolApprovalUnavailable("worker command permission changed")
	}
	if !child.Worker.AllowsTool(c.Name) {
		return out, apperror.New(apperror.CodePolicyDenied, "worker command is outside its delegated tool profile")
	}
	out = commandApprovalSource{specialist: &c, authority: a, input: input, permission: permission,
		run: run, workspaceID: mission.WorkspaceID, sessionID: child.SessionID, started: started,
		call: commandCallIntent{RunID: runID, CallID: sourceID, AgentID: child.ID, AgentAttemptID: c.Ref.AttemptID,
			AttemptID: c.Ref.AttemptID, ToolName: c.Name, PayloadJSON: c.PayloadJSON, AuthorityJSON: c.AuthorityJSON,
			Turn: int(attempt.Turn), Status: c.Status, AgentAttribution: domain.AgentAttributionRecorded, CompletedAt: c.CompletedAt}}
	return out, nil
}

func (s *CommandRuntimeService) validateWorkerCommandDispatch(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, source commandApprovalSource,
) error {
	if source.specialist == nil || source.specialist.Status != domain.SupervisorToolPending {
		return errors.New("worker command dispatch requires its pending child intent")
	}
	st, ok := s.store.(workerCommandStore)
	if !ok {
		return errors.New("worker command dispatch source unavailable")
	}
	_, err := st.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{
		Ref: source.specialist.Ref, CallID: scope.SpecialistToolCallID, SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID,
		Name: source.specialist.Name, PayloadJSON: source.specialist.PayloadJSON, AuthorityJSON: source.specialist.AuthorityJSON,
		ChargeID: scope.InvocationID, LeaseID: scope.LeaseID, LeaseGeneration: scope.LeaseGeneration, RequireStarted: true})
	return err
}

func (s *CommandRuntimeService) PreflightSpecialistCommandApproval(ctx context.Context,
	ref domain.AgentAttemptRef, callID string,
) (bool, *domain.SupervisorToolResult, error) {
	st, ok := s.store.(commandApprovalStore)
	if !ok {
		return false, nil, pendingToolApprovalUnavailable("worker command approval store unavailable")
	}
	source, err := readWorkerCommandSource(ctx, st, ref.RunID, domain.SpecialistToolApprovalID(ref, callID), false)
	if err != nil {
		return false, nil, err
	}
	if source.specialist == nil || source.specialist.Ref != ref || source.specialist.CallID != callID {
		return false, nil, pendingToolApprovalUnavailable("worker command approval source changed")
	}
	stop := func(code, message string, status domain.SupervisorToolCallStatus) (bool, *domain.SupervisorToolResult, error) {
		raw, _ := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion,
			Tool: source.call.ToolName, Status: string(status), Code: code, Message: message})
		return false, &domain.SupervisorToolResult{CallID: callID, Status: status, ErrorCode: code,
			ResultJSON: string(raw), CompletedAt: time.Now().UTC()}, nil
	}
	if source.started {
		return stop("outcome_unknown", "This child command was already dispatched; its native outcome must be recovered before continuing.", domain.SupervisorToolFailed)
	}
	if _, err = s.BindCommandToolAuthority(ctx, toolgateway.ToolName(source.call.ToolName), json.RawMessage(source.call.AuthorityJSON), json.RawMessage(source.call.PayloadJSON)); err != nil {
		return stop("command_authority_expired", "The pinned child command inputs or execution authority changed. No command was dispatched.", domain.SupervisorToolFailed)
	}
	policy := toolgateway.CommandRuntimePolicyDecision(s.commandRuntimePolicy(), source.input)
	if !policy.Allowed {
		return stop("policy_denied", "Current policy denied this exact child command.", domain.SupervisorToolDenied)
	}
	value, err := st.GetApprovalByProposal(ctx, source.call.CallID)
	var record *approval.Record
	if err == nil {
		if !commandApprovalMatches(value, source) {
			return false, nil, pendingToolApprovalUnavailable("worker command approval identity changed")
		}
		record = &value
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if !commandRuntimeMutates(source.input) && !policy.NeedsApproval && record == nil {
		return false, nil, nil
	}
	ensure := func() error {
		now := time.Now().UTC()
		value, err = st.EnsureApproval(ctx, approval.Proposal{
			IdempotencyKey: approval.ProposalIdempotencyKey(string(toolgateway.CommandRuntimeTool), source.call.CallID),
			ProposalID:     source.call.CallID, SessionID: source.sessionID, WorkspaceID: source.workspaceID,
			ToolName: string(toolgateway.CommandRuntimeTool), ActionClass: "command_process", Mode: "per_call", Status: approval.StatusPending,
			RequestFingerprint: source.approvalFingerprint(), RequestedBy: "specialist_runner",
			DecisionReason: "Approve only this child's pinned command or stdin bytes under the current Run permission.", CreatedAt: now, UpdatedAt: now})
		if err == nil {
			workers := s.store.(workerCommandStore)
			_, err = workers.SetSpecialistToolApproval(ctx, ref, callID, value.ID)
			if err == nil {
				record = &value
			}
		}
		return err
	}
	if record != nil {
		workers := s.store.(workerCommandStore)
		if _, err := workers.SetSpecialistToolApproval(ctx, ref, callID, record.ID); err != nil {
			return false, nil, err
		}
		if err := checkCommandGrant(ctx, st, *record); err != nil {
			return stop("policy_denied", "The child command authorization is no longer valid.", domain.SupervisorToolDenied)
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return false, nil, err
	}
	network := runner.CommandRuntimeNetworkDisabled
	for _, c := range source.input.Commands {
		if c.Network == runner.CommandRuntimeNetworkHost {
			network = runner.CommandRuntimeNetworkHost
		}
	}
	operation, err := commandOperation(key, source.call.CallID, source.authority.Adapter, source.call.PayloadJSON, network,
		[]toolcontract.Target{{Kind: "process", Locator: source.call.CallID}})
	if err != nil {
		return false, nil, err
	}
	waiting, allowed, err := decidePendingOperation(ctx, source.permission,
		executionauth.SubjectRef{RunID: ref.RunID, ActorID: ref.AgentID}, operation, source.approvalFingerprint(), &record,
		ensure, policy.NeedsApproval || source.input.ReviewScope != nil, source.authority.Adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace)
	if err != nil || waiting {
		return waiting, nil, err
	}
	if !allowed {
		return stop("policy_denied", "The operator denied this exact child command.", domain.SupervisorToolDenied)
	}
	return false, nil, nil
}

func (m *CommandRuntimeMultiplexer) PreflightSpecialistCommandApproval(ctx context.Context,
	ref domain.AgentAttemptRef, callID string,
) (bool, *domain.SupervisorToolResult, error) {
	for _, s := range m.adapters {
		st, ok := s.store.(workerCommandStore)
		if !ok {
			continue
		}
		c, err := st.GetSpecialistToolCall(ctx, ref, callID)
		if err != nil {
			return false, nil, err
		}
		a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(c.AuthorityJSON))
		if err != nil {
			return false, nil, err
		}
		if s.adapter.SameBackend(a.Adapter) {
			return s.PreflightSpecialistCommandApproval(ctx, ref, callID)
		}
	}
	return false, nil, pendingToolApprovalUnavailable("worker command approval backend changed")
}

// CloseSpecialistCommands revokes only process-local Jobs admitted by this
// child, including earlier continued attempts. Cleanup needs no active attempt
// or new execution lease, so an idle worker can also be revoked.
func (s *CommandRuntimeService) CloseSpecialistCommands(ctx context.Context, ref domain.AgentAttemptRef) (int, error) {
	if s == nil || s.store == nil || s.manager == nil || ctx == nil || ref.RunID == "" || ref.AgentID == "" {
		return 0, apperror.New(apperror.CodeInvalidArgument, "worker command cleanup identity is invalid")
	}
	actors, ok := s.store.(commandJobActorStore)
	if !ok {
		return 0, errors.New("worker command Job actor reader is unavailable")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), runner.MaxCommandRuntimeCancelGrace+time.Second)
	defer cancel()
	jobs, err := s.store.ListCommandRuntimeJobs(cleanup, runner.CommandRuntimeListFilter{RunID: ref.RunID, ActiveOnly: true, Limit: 500})
	if err != nil {
		return 0, err
	}
	stopped := 0
	var result error
	for _, job := range jobs {
		if !job.Adapter.SameBackend(s.adapter) || !s.manager.OwnsActiveJob(job) {
			continue
		}
		actor, err := actors.GetCommandRuntimeJobAttribution(cleanup, job.ID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if actor.Source != domain.AgentAttributionRecorded || actor.AgentID != ref.AgentID {
			continue
		}
		if _, err := s.manager.Stop(cleanup, job.ID, true, 0); err != nil {
			result = errors.Join(result, err)
			continue
		}
		stopped++
	}
	return stopped, result
}

func (m *CommandRuntimeMultiplexer) CloseSpecialistCommands(ctx context.Context, ref domain.AgentAttemptRef) (int, error) {
	if m == nil {
		return 0, nil
	}
	total := 0
	var result error
	for _, adapter := range m.adapters {
		count, err := adapter.CloseSpecialistCommands(ctx, ref)
		total += count
		result = errors.Join(result, err)
	}
	return total, result
}
