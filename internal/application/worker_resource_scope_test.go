package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func continueResourceWorker(t *testing.T, actor *workerCommandTestActor) {
	t.Helper()
	attempt, found, err := actor.store.GetAgentAttempt(t.Context(), actor.ref.AttemptID)
	if err != nil || !found {
		t.Fatalf("attempt missing: %v", err)
	}
	if attempt.UsageRecordedAt == nil {
		if _, _, err := actor.store.RecordSpecialistAttemptUsage(t.Context(), actor.ref, domain.AgentAttemptUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, idgen.New("resource-usage")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := actor.store.ContinueSpecialistAttempt(t.Context(), actor.ref, idgen.New("resource-continue")); err != nil {
		t.Fatal(err)
	}
}

func resumeResourceWorker(t *testing.T, actor *workerCommandTestActor) {
	t.Helper()
	binding, err := actor.store.GetSpecialistToolBinding(t.Context(), actor.ref)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := actor.store.BeginSpecialistAttempt(t.Context(), domain.AgentAttemptStart{
		AttemptID: idgen.New("resource-attempt"), RunID: actor.run.ID, AgentID: actor.child.ID,
		ParentAgentID: actor.root.ID, Lease: actor.lease, StartedAt: time.Now().UTC(),
	}, idgen.New("resource-start"))
	if err != nil {
		t.Fatal(err)
	}
	actor.ref.AttemptID, actor.round = attempt.ID, 0
	brief, err := actor.store.PrepareSpecialistContext(t.Context(), actor.ref)
	if err != nil {
		t.Fatal(err)
	}
	binding.Ref, binding.BriefFingerprint = actor.ref, brief.TaskBrief.Fingerprint
	if err = actor.store.BindSpecialistTools(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCommandResourceSurvivesContinueAndClosesWhileIdle(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context(), 6)
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-resource-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	service, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	a := newWorkerCommandTestActorTurns(t, st, service, run, root, lease, &domain.WorkerProfile{}, 3)
	b := newWorkerCommandTestActor(t, st, service, run, root, lease, &domain.WorkerProfile{})
	input := commandApprovalNativeInput(t, true)
	input.Commands[0].Arguments = []string{"-e", "setInterval(()=>{},1000)"}
	input.Commands[0].TimeoutMilliseconds = 60000
	call := a.prepare(t, input)
	result, err := service.ExecuteCommandRuntime(t.Context(), a.charge(t, call, true), input)
	if err != nil || len(result.Jobs) != 1 {
		t.Fatalf("start process: %+v %v", result, err)
	}
	a.complete(t, call)
	job, err := st.GetCommandRuntimeJob(t.Context(), result.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	origin := a.ref
	continueResourceWorker(t, a)
	if current, err := service.commandRuntimeJobBindingsCurrent(t.Context(), job); err != nil || !current {
		t.Fatalf("continued resource lost owner: current=%v err=%v", current, err)
	}
	if _, err := readWorkerExecutionContext(t.Context(), st, origin, nil); err == nil {
		t.Fatal("continued attempt retained dispatch authority")
	}
	resumeResourceWorker(t, a)
	list := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionList}
	for _, actor := range []*workerCommandTestActor{a, b} {
		call := actor.prepare(t, list)
		listed, err := service.ExecuteCommandRuntime(t.Context(), actor.charge(t, call, true), list)
		want := 0
		if actor == a {
			want = 1
		}
		if err != nil || len(listed.Jobs) != want {
			t.Fatalf("owned job list actor=%s result=%+v err=%v", actor.child.ID, listed, err)
		}
		actor.complete(t, call)
	}
	actor, err := st.GetCommandRuntimeJobAttribution(t.Context(), job.ID)
	if err != nil || actor.AgentAttemptID != origin.AttemptID {
		t.Fatalf("resource origin attribution changed: %+v %v", actor, err)
	}
	continueResourceWorker(t, a)
	execution := NewSpecialistExecutionService(st, nil, nil).WithWorkerRuntime(&AgentRunner{commandRuntime: service})
	if _, err := execution.RevokeSpecialistReadTools(t.Context(), run.ID, a.child.ID); err != nil {
		t.Fatal(err)
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(t.Context(), job); err != nil || current {
		t.Fatalf("revoked resource stayed current: current=%v err=%v", current, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err = st.GetCommandRuntimeJob(t.Context(), job.ID)
		if err != nil || job.State.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || !job.State.Terminal() {
		t.Fatalf("closed worker process survived: %+v %v", job, err)
	}
}

func TestWorkerBrowserResourceKeepsSessionAndRejectsOldAttempt(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	service := &CommandRuntimeService{}
	a := newWorkerCommandTestActorTurns(t, st, service, run, root, lease, &domain.WorkerProfile{}, 3)
	browser := NewAgentBrowserService(st, AgentBrowserOptions{HomePath: t.TempDir(), Capabilities: caps})
	browser.available = true
	t.Cleanup(func() { _ = browser.Shutdown(context.Background()) })
	raw, err := browser.WorkerToolAuthority(t.Context(), a.ref)
	if err != nil {
		t.Fatal(err)
	}
	var first toolgateway.AgentBrowserCallAuthority
	if err = json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	runtime := &agentBrowserTestRuntime{done: make(chan struct{}), status: browserruntime.AgentBrowserStatus{SessionID: first.BrowserSessionID, State: "ready"}}
	browser.slots[agentBrowserSlotKey(first)].runtime = runtime
	continueResourceWorker(t, a)
	if err := browser.checkResource(t.Context(), first); err != nil {
		t.Fatalf("idle browser resource: %v", err)
	}
	if err := browser.check(t.Context(), first); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("old attempt retained browser authority: %v", err)
	}
	resumeResourceWorker(t, a)
	raw, err = browser.WorkerToolAuthority(t.Context(), a.ref)
	if err != nil {
		t.Fatal(err)
	}
	var second toolgateway.AgentBrowserCallAuthority
	if err = json.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	if first.BrowserSessionID != second.BrowserSessionID || first.Generation == second.Generation || first.AgentAttemptID == second.AgentAttemptID {
		t.Fatalf("resource/call identity was not separated: first=%+v second=%+v", first, second)
	}
	if err = browser.check(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err = browser.checkResource(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err = browser.CloseWorker(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: a.child.ID}); err != nil {
		t.Fatal(err)
	}
	if !runtime.cancelled {
		t.Fatal("close did not cancel the retained browser")
	}
	if err = browser.checkResource(t.Context(), first); err == nil {
		t.Fatal("closed browser retained resource authority")
	}
}

func TestWorkerMCPResourceScopeSurvivesContinueWithFreshDispatchGuard(t *testing.T) {
	st, run, root, lease, _ := newCommandRuntimeTestRuntime(t, t.Context(), 6)
	a := newWorkerCommandTestActorTurns(t, st, &CommandRuntimeService{}, run, root, lease, &domain.WorkerProfile{}, 3)
	b := newWorkerCommandTestActor(t, st, &CommandRuntimeService{}, run, root, lease, &domain.WorkerProfile{})
	first, err := workerMCPWorkspace(t.Context(), st, a.ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore, err := first.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	continueResourceWorker(t, a)
	if _, err = first.Resolve(t.Context()); err == nil {
		t.Fatal("old MCP invocation retained roots authority")
	}
	resumeResourceWorker(t, a)
	second, err := workerMCPWorkspace(t.Context(), st, a.ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	rootAfter, err := second.Resolve(t.Context())
	if err != nil || rootAfter != rootBefore || first.ScopeID != second.ScopeID {
		t.Fatalf("same worker changed MCP resource: %+v %+v %v", rootBefore, rootAfter, err)
	}
	sibling, err := workerMCPWorkspace(t.Context(), st, b.ref, nil)
	if err != nil || sibling.ScopeID == first.ScopeID {
		t.Fatalf("siblings share MCP resource: %v", err)
	}
	if _, err := st.RevokeSpecialistReadTools(t.Context(), run.ID, a.child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resolve(t.Context()); err == nil {
		t.Fatal("revoked worker retained MCP invocation authority")
	}
}
