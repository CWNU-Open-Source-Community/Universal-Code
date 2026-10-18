package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

type workerCommandTestActor struct {
	store   *store.SQLiteStore
	service *CommandRuntimeService
	run     domain.Run
	root    domain.AgentNode
	child   domain.AgentNode
	lease   domain.RunExecutionLease
	ref     domain.AgentAttemptRef
	round   int
}

func newWorkerCommandTestActor(t *testing.T, st *store.SQLiteStore, service *CommandRuntimeService,
	run domain.Run, root domain.AgentNode, lease domain.RunExecutionLease, profile *domain.WorkerProfile,
) *workerCommandTestActor {
	return newWorkerCommandTestActorTurns(t, st, service, run, root, lease, profile, 1)
}

func newWorkerCommandTestActorTurns(t *testing.T, st *store.SQLiteStore, service *CommandRuntimeService,
	run domain.Run, root domain.AgentNode, lease domain.RunExecutionLease, profile *domain.WorkerProfile, turns int64,
) *workerCommandTestActor {
	t.Helper()
	skill := ""
	for _, candidate := range root.Skills {
		if domain.DelegableAgentSkill(candidate) {
			skill = candidate
			break
		}
	}
	if skill == "" {
		t.Fatal("worker fixture has no delegated skill")
	}
	child, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{
		Worker: profile, AgentID: idgen.New("worker"), SessionID: idgen.New("worker-session"),
		RunID: run.ID, ParentAgentID: root.ID, Title: "independent command worker", Skills: []string{skill},
		TurnLimit: turns, TokenLimit: 100, MaxChildren: 2, CreatedAt: time.Now().UTC(),
	}, idgen.New("admit-command-worker"))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := st.BeginSpecialistAttempt(t.Context(), domain.AgentAttemptStart{
		AttemptID: idgen.New("worker-attempt"), RunID: run.ID, AgentID: child.ID, ParentAgentID: root.ID,
		Lease: lease, StartedAt: time.Now().UTC(),
	}, idgen.New("start-command-worker"))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: attempt.ID}
	brief, err := st.PrepareSpecialistContext(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	files, err := ResolveRunFileWorkspace(t.Context(), st, run, mission, service.drydocks)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := workspace.AgentCodeRootFingerprint(files.Workspace.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	profileJSON, err := domain.WorkerProfileJSON(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindSpecialistTools(t.Context(), domain.SpecialistToolBinding{
		Ref: ref, SessionID: child.SessionID, WorkspaceID: mission.WorkspaceID,
		RootFingerprint: fingerprint, BriefFingerprint: brief.TaskBrief.Fingerprint, Mode: "worker", WorkerProfileJSON: profileJSON,
	}); err != nil {
		t.Fatal(err)
	}
	child, err = st.GetAgentNode(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &workerCommandTestActor{store: st, service: service, run: run, root: root, child: child, lease: lease, ref: ref}
}

func (f *workerCommandTestActor) prepare(t *testing.T, input toolgateway.CommandRuntimeInput) domain.SpecialistToolCall {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := toolgateway.NormalizeCommandRuntimePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := f.store.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	permissionID, generation, epoch, fence, live := bindAgentCodeRuntime(f.service.capabilities, permission)
	if !live {
		t.Fatal("worker fixture has no live authority")
	}
	base, err := commandruntimeadapter.EncodeAuthority(commandruntimeadapter.Authority{
		ProtocolVersion: commandruntimeadapter.OperationAuthorityVersion, RunID: f.run.ID, Adapter: f.service.adapter,
		PermissionSnapshotID: permissionID, PermissionMode: permission.Mode, PermissionRevision: permission.Revision,
		PermissionGeneration: generation, PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := f.service.BindSpecialistCommandToolAuthority(t.Context(), f.ref, toolgateway.CommandRuntimeTool, base, payload)
	if err != nil {
		t.Fatal(err)
	}
	model := llm.ModelAttempt{Number: f.round + 1, ToolRound: f.round, TransportAttempt: 1, MaxAttempts: 1, Provider: "worker-fixture", Model: "fixture"}
	if _, err := f.store.RecordSpecialistModelStarted(t.Context(), f.ref, model); err != nil {
		t.Fatal(err)
	}
	model.Elapsed, model.Outcome = time.Millisecond, llm.OutcomeSuccess
	callID := "provider-call-" + idgen.New("next")
	response := llm.ChatResponse{Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.CommandRuntimeTool), Arguments: payload}}}
	if _, err := f.store.RecordSpecialistToolModelCompleted(t.Context(), f.ref, model, response, "execute the authenticated worker brief",
		policy.Decision{Allowed: true, Risk: "low", Reason: "fixture model output"}); err != nil {
		t.Fatal(err)
	}
	f.round++
	call, err := f.store.BindSpecialistToolAuthority(t.Context(), f.ref, callID, string(authority))
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func (f *workerCommandTestActor) charge(t *testing.T, call domain.SpecialistToolCall, start bool) toolgateway.CommandRuntimeContext {
	t.Helper()
	binding, err := f.store.GetSpecialistToolBinding(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := f.store.ChargeToolCall(t.Context(), toolbudget.ChargeRequest{RunID: f.run.ID, SessionID: f.child.SessionID,
		WorkspaceID: binding.WorkspaceID, AgentID: f.child.ID, AgentAttemptID: f.ref.AttemptID, ToolCallID: call.CallID, ToolName: call.Name,
		ActionClass: "process", RequestedBy: "specialist_runner", LeaseID: f.lease.LeaseID, LeaseGeneration: f.lease.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if start {
		if _, err := f.store.MarkSpecialistToolStarted(t.Context(), f.ref, call.CallID, call.AuthorityJSON); err != nil {
			t.Fatal(err)
		}
	}
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil {
		t.Fatal(err)
	}
	mode, err := f.store.GetRunMode(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return toolgateway.CommandRuntimeContext{InvocationID: usage.LastCharge, OperationKey: SpecialistCommandOperationKey(call),
		RunID: f.run.ID, MissionID: f.run.MissionID, RootAgentID: f.root.ID, AgentID: f.child.ID, AgentAttemptID: f.ref.AttemptID,
		SessionID: f.child.SessionID, WorkspaceID: binding.WorkspaceID, SpecialistToolCallID: call.CallID,
		Surface: mode.Surface, Phase: mode.Phase, Role: domain.AgentRoleSpecialist, Profile: mode.Profile, ModeRevision: mode.Revision,
		PermissionMode: a.PermissionMode, PermissionSnapshotID: a.PermissionSnapshotID, PermissionRevision: a.PermissionRevision,
		PermissionGeneration: a.PermissionGeneration, PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence,
		CapabilityGeneration: a.Adapter.Generation, Adapter: a.Adapter, LeaseID: f.lease.LeaseID, LeaseGeneration: f.lease.Generation,
		RequestedBy: "specialist_runner", PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "high", Reason: "fixture dispatch"}}
}

func (f *workerCommandTestActor) complete(t *testing.T, call domain.SpecialistToolCall) {
	t.Helper()
	call.Status, call.ResultJSON = domain.SupervisorToolCompleted, `{"status":"completed"}`
	if err := f.store.CompleteSpecialistToolCall(t.Context(), call); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCommandRuntimeUsesRealActorAndIsolatesJobs(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-command-host")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	service, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	a := newWorkerCommandTestActor(t, st, service, run, root, lease, &domain.WorkerProfile{})
	b := newWorkerCommandTestActor(t, st, service, run, root, lease, &domain.WorkerProfile{})
	input := commandApprovalNativeInput(t, true)
	input.Commands[0].TimeoutMilliseconds = 60000
	call := a.prepare(t, input)
	if waiting, result, err := service.PreflightSpecialistCommandApproval(t.Context(), a.ref, call.CallID); err != nil || waiting || result != nil {
		t.Fatalf("live Full worker preflight waiting=%v result=%+v err=%v", waiting, result, err)
	}
	scope := a.charge(t, call, true)
	result, err := service.ExecuteCommandRuntime(t.Context(), scope, input)
	if err != nil || len(result.Jobs) != 1 {
		t.Fatalf("worker process: %+v %v", result, err)
	}
	job, err := st.GetCommandRuntimeJob(t.Context(), result.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := st.GetCommandRuntimeJobAttribution(t.Context(), job.ID)
	if err != nil || job.SessionID != a.child.SessionID || job.RootAgentID != root.ID || actor.AgentID != a.child.ID || actor.AgentAttemptID != a.ref.AttemptID || actor.Source != domain.AgentAttributionRecorded {
		t.Fatalf("child was relabelled as root: job=%+v actor=%+v err=%v", job, actor, err)
	}
	a.complete(t, call)
	if current, err := service.commandRuntimeJobBindingsCurrent(t.Context(), job); err != nil || !current {
		t.Fatalf("completed start lost active worker owner: current=%v err=%v", current, err)
	}
	list := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionList}
	listCall := b.prepare(t, list)
	listed, err := service.ExecuteCommandRuntime(t.Context(), b.charge(t, listCall, true), list)
	if err != nil || len(listed.Jobs) != 0 {
		t.Fatalf("sibling Job leaked: %+v %v", listed, err)
	}
	b.complete(t, listCall)
	otherCall := b.prepare(t, input)
	otherResult, err := service.ExecuteCommandRuntime(t.Context(), b.charge(t, otherCall, true), input)
	if err != nil || len(otherResult.Jobs) != 1 {
		t.Fatalf("second worker process: %+v %v", otherResult, err)
	}
	b.complete(t, otherCall)
	sibling, err := st.GetCommandRuntimeJob(t.Context(), otherResult.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := service.loadAuthorizedBindings(t.Context(), scope, false)
	if err != nil {
		t.Fatal(err)
	}
	otherScope := scope
	otherScope.AgentID, otherScope.AgentAttemptID, otherScope.SessionID = b.child.ID, b.ref.AttemptID, b.child.SessionID
	other, err := service.loadAuthorizedBindings(t.Context(), otherScope, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.authorizeJob(t.Context(), job.ID, other); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("sibling controlled foreign Job: %v", err)
	}
	if _, err := service.authorizeJob(t.Context(), job.ID, bindings); err != nil {
		t.Fatal(err)
	}
	rebound := bindings
	foreignAttempt := *bindings.worker
	foreignAttempt.Ref.AttemptID = "replacement-worker-attempt"
	rebound.worker = &foreignAttempt
	if _, err := service.authorizeJob(t.Context(), job.ID, rebound); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("replacement attempt controlled the original worker Job: %v", err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(t.Context(), job); err != nil || !current {
		t.Fatalf("review wait revoked the admitted worker Job: current=%v err=%v", current, err)
	}
	if stopped, err := service.Reconcile(t.Context()); err != nil || stopped != 0 {
		t.Fatalf("review wait stopped the worker process: stopped=%d err=%v", stopped, err)
	}
	if stopped, err := service.CloseSpecialistCommands(t.Context(), a.ref); err != nil || stopped != 1 {
		t.Fatalf("worker cleanup stopped=%d err=%v", stopped, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err = st.GetCommandRuntimeJob(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() && job.TreeReaped {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !job.State.Terminal() || !job.TreeReaped {
		t.Fatalf("closed worker retained its process: %+v", job)
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(t.Context(), sibling); err != nil || !current || !manager.OwnsActiveJob(sibling) {
		t.Fatalf("worker cleanup revoked the sibling Job: current=%v err=%v", current, err)
	}
	if _, err := st.RevokeSpecialistReadTools(t.Context(), run.ID, b.child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	job = sibling
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err = st.GetCommandRuntimeJob(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() && job.TreeReaped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("revoked worker retained its process: %+v", job)
}

func TestWorkerCommandRuntimeRequiresStartedExactIntent(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-command-exact")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	service, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	f := newWorkerCommandTestActor(t, st, service, run, root, lease, &domain.WorkerProfile{})
	input := commandApprovalNativeInput(t, false)
	call := f.prepare(t, input)
	scope := f.charge(t, call, false)
	if _, err = service.ExecuteCommandRuntime(t.Context(), scope, input); err == nil {
		t.Fatal("undispatched child intent executed")
	}
	if _, err = st.MarkSpecialistToolStarted(t.Context(), f.ref, call.CallID, call.AuthorityJSON); err != nil {
		t.Fatal(err)
	}
	changed := scope
	changed.SessionID = run.SessionID
	if _, err = service.ExecuteCommandRuntime(t.Context(), changed, input); err == nil {
		t.Fatal("root Session replaced child actor")
	}
	changed = scope
	changed.AgentAttemptID = root.ActiveAttemptID
	if _, err = service.ExecuteCommandRuntime(t.Context(), changed, input); err == nil {
		t.Fatal("foreign attempt replaced child attempt")
	}
	jobs, err := st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: run.ID, Limit: 10})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("invalid child scopes created jobs: %+v %v", jobs, err)
	}
	result, err := service.ExecuteCommandRuntime(t.Context(), scope, input)
	if err != nil || len(result.Jobs) != 1 || result.Jobs[0].State != runner.CommandRuntimeJobCompleted {
		t.Fatalf("exact child command failed: %+v %v", result, err)
	}
	info, err := st.GetWorkspaceInfo(t.Context(), scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(info.RootPath, "count.txt"))
	if err != nil || len(marker) == 0 {
		t.Fatalf("real child process did not write its marker: %q %v", marker, err)
	}
}

func TestWorkerCommandRuntimePreservesAskAutoFullSandbox(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, mode, domain.RunExecutionProfileDocker, true, true)
			if mode == domain.RunExecutionPermissionAsk {
				f.service.SetCommandRuntimePolicy(&commandApprovalChecker{Checker: policy.NewDefaultChecker(), require: true})
			}
			lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
			if err != nil || !found {
				t.Fatalf("fixture lease missing: %v", err)
			}
			worker := newWorkerCommandTestActor(t, f.st, f.service, f.turn.Run, f.turn.Agent, lease, &domain.WorkerProfile{})
			input := commandApprovalSandboxInput(t)
			call := worker.prepare(t, input)
			waiting, terminal, err := f.service.PreflightSpecialistCommandApproval(t.Context(), worker.ref, call.CallID)
			if err != nil || terminal != nil || waiting != (mode == domain.RunExecutionPermissionAsk) {
				t.Fatalf("worker preflight waiting=%v terminal=%+v err=%v", waiting, terminal, err)
			}
			if waiting {
				record, err := f.st.GetApprovalByProposal(t.Context(), domain.SpecialistToolApprovalID(worker.ref, call.CallID))
				if err != nil || record.RunID != worker.run.ID || record.SessionID != worker.child.SessionID || record.RequestFingerprint != domain.SpecialistToolApprovalFingerprint(call) {
					t.Fatalf("child exact review identity: %+v %v", record, err)
				}
				jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: worker.run.ID, Limit: 10})
				if err != nil || len(jobs) != 0 {
					t.Fatalf("Ask dispatched before review: %+v %v", jobs, err)
				}
				if err := RecheckCommandApproval(t.Context(), f.st, record); err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.DecideApproval(t.Context(), approval.DecisionRequest{ProposalID: record.ProposalID, IdempotencyKey: idgen.New("approve-worker"), Action: approval.ActionApprove, ReviewedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
				if waiting, terminal, err = f.service.PreflightSpecialistCommandApproval(t.Context(), worker.ref, call.CallID); err != nil || waiting || terminal != nil {
					t.Fatalf("approved child did not resume: %v %+v %v", waiting, terminal, err)
				}
			}
			result, err := f.service.ExecuteCommandRuntime(t.Context(), worker.charge(t, call, true), input)
			if err != nil || len(result.Jobs) != 1 || result.Jobs[0].State != runner.CommandRuntimeJobCompleted {
				t.Fatalf("worker sandbox execution: %+v %v (cause: %v)", result, err, errors.Unwrap(err))
			}
			job, err := f.st.GetCommandRuntimeJob(t.Context(), result.Jobs[0].ID)
			if err != nil || job.SessionID != worker.child.SessionID || !strings.Contains(job.Stdout, "controlled sandbox output") {
				t.Fatalf("worker sandbox receipt: %+v %v", job, err)
			}
			if f.hostTripwire.attempts.Load() != 0 {
				t.Fatal("worker fell back to host execution")
			}
		})
	}
}

type workerCommandWaitingDockerLifecycle struct {
	sandbox.DockerContainerLifecycleTransport
	entered chan struct{}
	finish  chan struct{}
}

func (x *workerCommandWaitingDockerLifecycle) Wait(ctx context.Context, request sandbox.DockerContainerLifecycleRequest,
	fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, error) {
	close(x.entered)
	select {
	case <-ctx.Done():
		return sandbox.DockerContainerLifecycleObservation{}, ctx.Err()
	case <-x.finish:
		return x.DockerContainerLifecycleTransport.Wait(ctx, request, fence)
	}
}

func TestWorkerCommandRuntimeSandboxTerminalReceiptSurvivesReviewLeaseRelease(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("worker sandbox lease: %v", err)
	}
	worker := newWorkerCommandTestActor(t, f.st, f.service, f.turn.Run, f.turn.Agent, lease, &domain.WorkerProfile{})
	docker := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor).service.docker
	paused := &workerCommandWaitingDockerLifecycle{DockerContainerLifecycleTransport: docker.lifecycleTransport,
		entered: make(chan struct{}), finish: make(chan struct{})}
	docker.lifecycleTransport = paused
	input := commandApprovalSandboxInput(t)
	input.Action, input.FailurePolicy, input.MaxBytes = toolgateway.CommandRuntimeActionStart, "", nil
	input.Commands[0].TimeoutMilliseconds = 60000
	call := worker.prepare(t, input)
	if waiting, result, err := f.service.PreflightSpecialistCommandApproval(t.Context(), worker.ref, call.CallID); err != nil || waiting || result != nil {
		t.Fatalf("worker background preflight: waiting=%v result=%+v err=%v", waiting, result, err)
	}
	started, err := f.service.ExecuteCommandRuntime(t.Context(), worker.charge(t, call, true), input)
	if err != nil || len(started.Jobs) != 1 {
		t.Fatalf("worker background start: %+v %v", started, err)
	}
	worker.complete(t, call)
	select {
	case <-paused.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sandbox never reached its admitted process lifetime")
	}
	if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	job, err := f.st.GetCommandRuntimeJob(t.Context(), started.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := f.service.commandRuntimeJobBindingsCurrent(t.Context(), job); err != nil || !current {
		t.Fatalf("foreground lease release revoked sandbox lifetime: current=%v err=%v", current, err)
	}
	close(paused.finish)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, err = f.st.GetCommandRuntimeJob(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() && job.TreeReaped {
			if job.State != runner.CommandRuntimeJobCompleted || !strings.Contains(job.Stdout, "controlled sandbox output") {
				t.Fatalf("sandbox lost its admitted terminal receipt: %+v", job)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("sandbox did not persist its terminal receipt: %+v", job)
}
