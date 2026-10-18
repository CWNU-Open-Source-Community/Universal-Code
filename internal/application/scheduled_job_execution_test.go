package application_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type scheduledAutomationTurns struct {
	runID    string
	calls    int
	observed string
	deny     error
}

func (s *scheduledAutomationTurns) CheckAutomaticRun(context.Context, string, string) error {
	return s.deny
}
func (s *scheduledAutomationTurns) ExecuteAutomatic(context.Context, application.ExecuteThreadTurnRequest) (application.ExecuteThreadTurnResult, error) {
	s.calls++
	return application.ExecuteThreadTurnResult{Submission: application.SubmitThreadMessageResult{Run: domain.Run{ID: s.runID}, Message: domain.OperatorSteeringMessage{ID: "scheduled-test-message"}}}, s.deny
}

type droppedScheduledWorkflow struct {
	*application.WorkflowService
	drop  bool
	calls int
}

func (s *droppedScheduledWorkflow) Start(ctx context.Context, request application.StartWorkflowRequest) (domain.WorkflowExecution, error) {
	s.calls++
	execution, err := s.WorkflowService.Start(ctx, request)
	if err == nil && s.drop {
		s.drop = false
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeUnavailable, "simulate lost workflow response")
	}
	return execution, err
}

func scheduledWorkflowServices(st *store.SQLiteStore, p *lifecycleProvider) (*application.ThreadTurnService, *application.WorkflowService) {
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	return application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())), application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
}

func TestScheduledAutomaticWorkflowLostResponseObservesOriginalAcrossRestart(t *testing.T) {
	st, run, path := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second)
	clock := &scheduledStaticClock{now: now}
	p := &lifecycleProvider{responses: []string{workflowResponse(t, domain.CompletionSucceeded).Text}}
	turns, workflowService := scheduledWorkflowServices(st, p)
	workflows := &droppedScheduledWorkflow{WorkflowService: workflowService, drop: true}
	definition, err := workflows.Save(t.Context(), workflowSpec(workflowWorker("report")))
	if err != nil {
		t.Fatal(err)
	}
	request := scheduledAutomationRequest(run, now)
	request.Schedule.Kind = domain.ScheduledJobOnce
	request.Schedule.IntervalSeconds = 0
	request.MaxRounds = 1
	request.Retry.MaxAttempts = 2
	request.ExecutionTarget = &domain.ScheduledJobExecutionTarget{Kind: "workflow", WorkflowDefinitionID: definition.ID, Inputs: json.RawMessage(`{"topic":"saved"}`)}
	svc := application.NewScheduledJobService(st).WithClock(clock).WithExecution(turns, workflows)
	created, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunDueConfirmed(t.Context(), "workflow-worker", now); err == nil {
		t.Fatal("lost workflow response should be recorded as retry")
	}
	executions, err := workflows.List(t.Context(), run.ID)
	if err != nil || len(executions) != 1 || workflows.calls != 1 {
		t.Fatalf("executions=%+v calls=%d err=%v", executions, workflows.calls, err)
	}
	original := executions[0]
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, workflowService = scheduledWorkflowServices(reopened, p)
	workflows = &droppedScheduledWorkflow{WorkflowService: workflowService}
	// Recover the durable workflow before new-dispatch admission. Existing
	// executions remain observable when a new turn would be denied.
	denied := &scheduledAutomationTurns{deny: apperror.New(apperror.CodePolicyDenied, "new turn denied")}
	svc = application.NewScheduledJobService(reopened).WithClock(clock).WithExecution(denied, workflows)
	clock.Set(now.Add(2 * time.Second))
	if _, err := svc.RunDueConfirmed(t.Context(), "restarted-worker", clock.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil || len(snapshot.Executions) != 1 || snapshot.Executions[0].WorkflowExecutionID != original.ID || snapshot.Executions[0].State != "running" || workflows.calls != 0 {
		t.Fatalf("recovered=%+v calls=%d err=%v", snapshot, workflows.calls, err)
	}
	paused, err := workflows.Pause(t.Context(), original.ID, original.Version)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(6 * time.Second))
	if _, err := svc.RunDueConfirmed(t.Context(), "restarted-worker", clock.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil || snapshot.Executions[0].State != "blocked" || snapshot.Rounds[0].Attempt != 2 || workflows.calls != 0 {
		t.Fatalf("paused=%+v calls=%d err=%v", snapshot, workflows.calls, err)
	}
	execution, err := workflows.Resume(t.Context(), original.ID, paused.Version)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4 && execution.Status != "completed"; i++ {
		execution, err = workflows.Advance(t.Context(), original.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if execution.Status != "completed" {
		t.Fatalf("workflow=%+v", execution)
	}
	clock.Set(clock.Now().Add(6 * time.Second))
	if _, err := svc.RunDueConfirmed(t.Context(), "restarted-worker", clock.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil || snapshot.Job.Status != domain.ScheduledJobCompleted || snapshot.Job.RoundsCompleted != 1 || snapshot.Job.ModelCalls != 0 || snapshot.Executions[0].State != "completed" || p.calls != 1 || workflows.calls != 0 {
		t.Fatalf("completed=%+v model calls=%d starts=%d err=%v", snapshot, p.calls, workflows.calls, err)
	}
}

func TestScheduledAutomaticWorkflowRequiresOwnerToRemainCurrent(t *testing.T) {
	st, run, _ := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second)
	p := &lifecycleProvider{}
	turns, workflowService := scheduledWorkflowServices(st, p)
	workflows := &droppedScheduledWorkflow{WorkflowService: workflowService}
	definition, err := workflows.Save(t.Context(), workflowSpec(workflowWorker("report")))
	if err != nil {
		t.Fatal(err)
	}
	svc := application.NewScheduledJobService(st).WithClock(&scheduledStaticClock{now: now}).WithExecution(turns, workflows)
	request := scheduledAutomationRequest(run, now)
	request.ExecutionTarget = &domain.ScheduledJobExecutionTarget{Kind: "workflow", WorkflowDefinitionID: definition.ID}
	created, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Complete(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	next, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "Start the next task Run", OperationKey: "scheduled-workflow-new-run", RequestedBy: "operator"})
	if err != nil || next.Run.ID == run.ID {
		t.Fatalf("successor=%+v err=%v", next, err)
	}
	if err := turns.CheckAutomaticRun(t.Context(), next.Thread.ID, run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "selected Run") {
		t.Fatalf("owner admission=%v", err)
	}
	if _, err := svc.RunDueConfirmed(t.Context(), "workflow-worker", now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil || len(snapshot.Executions) != 1 || snapshot.Executions[0].State != "blocked" || workflows.calls != 0 || p.calls != 0 {
		t.Fatalf("owner drift=%+v starts=%d provider calls=%d err=%v", snapshot, workflows.calls, p.calls, err)
	}
}
func (s *scheduledAutomationTurns) ObserveAutomaticTurn(context.Context, string, string, string) (application.AutomaticThreadTurnObservation, error) {
	return application.AutomaticThreadTurnObservation{State: s.observed}, nil
}

func scheduledAutomationFixture(t *testing.T) (*store.SQLiteStore, domain.Run, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scheduled-automatic.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "scheduled-workspace", Name: "scheduled workspace", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "scheduled task", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: "scheduled-workspace", ModelRoute: "lifecycle-test/model", Interactive: true, Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 16, MaxTokens: 100000}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st, run, path
}
func scheduledAutomationRequest(run domain.Run, now time.Time) application.CreateScheduledJobRequest {
	r := scheduledReadOnlyRequest(run.ID, now, "scheduled-automatic-create")
	r.ExecutionMode = domain.ScheduledJobAutomatic
	r.ExecutionConsentVersion = 1
	r.MaxModelCalls = 0
	r.StopOnTargetTerminal = false
	r.Retry.MaxAttempts = 1
	r.ExecutionTarget = &domain.ScheduledJobExecutionTarget{Kind: "thread", ThreadID: domain.InitialThreadID(run.ID), Prompt: "Continue the saved task and report its result"}
	return r
}

func TestScheduledAutomaticConsentWaitRestartAndRoundBudget(t *testing.T) {
	st, run, path := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	clock := &scheduledStaticClock{now: now}
	turns := &scheduledAutomationTurns{runID: run.ID, observed: "waiting_approval"}
	svc := application.NewScheduledJobService(st).WithClock(clock).WithExecution(turns, nil)
	req := scheduledAutomationRequest(run, now)
	req.MaxRounds = 2
	missing := req
	missing.ExecutionConsentVersion = 0
	if _, err := svc.Create(t.Context(), missing); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("missing consent=%v", err)
	}
	created, err := svc.Create(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Job.ExecutionConsentVersion != 1 {
		t.Fatal("execution consent was not projected")
	}
	if _, err := svc.RunDueObservation(t.Context(), "old-observer", now); err != nil {
		t.Fatal(err)
	}
	if turns.calls != 0 {
		t.Fatal("observation worker executed an automatic job")
	}
	if _, err := svc.RunDueConfirmed(t.Context(), "automatic-worker", now); err != nil {
		t.Fatal(err)
	}
	if turns.calls != 1 {
		t.Fatalf("submissions=%d", turns.calls)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed := &scheduledAutomationTurns{runID: run.ID, observed: "waiting_approval"}
	svc = application.NewScheduledJobService(reopened).WithClock(clock).WithExecution(resumed, nil)
	for i := 1; i <= 12; i++ {
		clock.Set(now.Add(time.Duration(i) * 6 * time.Second))
		if _, err := svc.RunDueConfirmed(t.Context(), "restarted-worker", clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.calls != 0 || len(snapshot.Executions) != 1 || snapshot.Executions[0].State != "waiting_approval" || snapshot.Rounds[0].Attempt != 1 || snapshot.Job.RoundsCompleted != 0 {
		t.Fatalf("reopened calls=%d snapshot=%+v", resumed.calls, snapshot)
	}
	resumed.observed = "completed"
	clock.Set(clock.Now().Add(6 * time.Second))
	if _, err := svc.RunDueConfirmed(t.Context(), "restarted-worker", clock.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = svc.Get(t.Context(), created.Job.ID, 10, 10)
	if snapshot.Job.RoundsCompleted != 1 || snapshot.Executions[0].State != "completed" {
		t.Fatalf("completion=%+v", snapshot)
	}
	clock.Set(*snapshot.Job.NextWakeAt)
	if _, err := svc.RunDueConfirmed(t.Context(), "restarted-worker", clock.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = svc.Get(t.Context(), created.Job.ID, 10, 10)
	if resumed.calls != 1 || snapshot.Job.Status != domain.ScheduledJobExhausted || snapshot.Job.StopReason != domain.ScheduledJobStopRoundBudget || len(snapshot.Executions) != 2 {
		t.Fatalf("budget calls=%d snapshot=%+v", resumed.calls, snapshot)
	}
}

type droppedScheduledTurn struct {
	*application.ThreadTurnService
	drop  bool
	calls int
}

func (s *droppedScheduledTurn) ExecuteAutomatic(ctx context.Context, r application.ExecuteThreadTurnRequest) (application.ExecuteThreadTurnResult, error) {
	s.calls++
	result, err := s.ThreadTurnService.ExecuteAutomatic(ctx, r)
	if s.drop {
		s.drop = false
		return application.ExecuteThreadTurnResult{}, apperror.New(apperror.CodeUnavailable, "simulate lost submission response")
	}
	return result, err
}

func TestScheduledAutomaticLostResponseReplaysOriginalThreadInput(t *testing.T) {
	st, run, _ := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second)
	clock := &scheduledStaticClock{now: now}
	p := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionFinish, "finished scheduled task", "done", "")}}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	turns := &droppedScheduledTurn{ThreadTurnService: application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())), drop: true}
	svc := application.NewScheduledJobService(st).WithClock(clock).WithExecution(turns, nil)
	request := scheduledAutomationRequest(run, now)
	request.Retry.MaxAttempts = 2
	request.Schedule.Kind = domain.ScheduledJobOnce
	request.Schedule.IntervalSeconds = 0
	created, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunDueConfirmed(t.Context(), "worker", now); err == nil {
		t.Fatal("lost response should be recorded as retry")
	}
	clock.Set(now.Add(2 * time.Second))
	if _, err := svc.RunDueConfirmed(t.Context(), "worker-after-restart", clock.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Job.Status != domain.ScheduledJobCompleted || p.calls != 1 || turns.calls != 1 || len(snapshot.Executions) != 1 || snapshot.Executions[0].MessageID == "" {
		t.Fatalf("provider calls=%d dispatch attempts=%d snapshot=%+v", p.calls, turns.calls, snapshot)
	}
	messages, err := st.ListThreadMessagesPage(t.Context(), request.ExecutionTarget.ThreadID, false, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range messages {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("user messages=%d", users)
	}
}

func TestScheduledAutomaticPauseCancelAndWorkspaceDrift(t *testing.T) {
	st, run, _ := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second)
	clock := &scheduledStaticClock{now: now}
	turns := &scheduledAutomationTurns{runID: run.ID, observed: "completed"}
	svc := application.NewScheduledJobService(st).WithClock(clock).WithExecution(turns, nil)
	created, err := svc.Create(t.Context(), scheduledAutomationRequest(run, now))
	if err != nil {
		t.Fatal(err)
	}
	transition := func(action domain.ScheduledJobAction, key string) {
		t.Helper()
		job, _ := st.GetScheduledJob(t.Context(), created.Job.ID)
		if _, err := svc.Transition(t.Context(), application.TransitionScheduledJobRequest{Version: domain.ScheduledJobControlProtocolVersion, RunID: run.ID, JobID: job.ID, Action: action, ExpectedRevision: job.Revision, OperationKey: key, RequestedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	transition(domain.ScheduledJobPause, "pause-scheduled-auto")
	svc.RunDueConfirmed(t.Context(), "worker", now)
	if turns.calls != 0 {
		t.Fatal("paused job dispatched")
	}
	transition(domain.ScheduledJobResume, "resume-scheduled-auto")
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "scheduled-workspace", Name: "scheduled workspace", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunDueConfirmed(t.Context(), "worker", now); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := svc.Get(t.Context(), created.Job.ID, 10, 10)
	if turns.calls != 0 || len(snapshot.Executions) != 1 || snapshot.Executions[0].State != "blocked" {
		t.Fatalf("workspace drift=%+v calls=%d", snapshot, turns.calls)
	}
	transition(domain.ScheduledJobCancel, "cancel-scheduled-auto")
	clock.Set(now.Add(time.Minute))
	svc.RunDueConfirmed(t.Context(), "worker", clock.Now())
	if turns.calls != 0 {
		t.Fatal("cancelled job dispatched")
	}
}

func TestScheduledAutomaticPauseBeforeDispatchKeepsOriginalAttempt(t *testing.T) {
	st, run, _ := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second)
	turns := &scheduledAutomationTurns{runID: run.ID, observed: "completed"}
	svc := application.NewScheduledJobService(st).WithClock(&scheduledStaticClock{now: now}).WithExecution(turns, nil)
	request := scheduledAutomationRequest(run, now)
	request.Schedule.Kind = domain.ScheduledJobOnce
	request.Schedule.IntervalSeconds = 0
	created, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	job, lease, found, err := st.ClaimDueConfirmedScheduledJob(t.Context(), "before-pause", now)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, err := st.SaveScheduledJobExecution(t.Context(), lease, domain.ScheduledJobExecution{JobID: job.ID, OccurrenceAt: lease.OccurrenceAt, OperationKey: lease.OperationKey, State: "queued", CreatedAt: now, UpdatedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	paused, err := svc.Transition(t.Context(), application.TransitionScheduledJobRequest{Version: domain.ScheduledJobControlProtocolVersion, RunID: run.ID, JobID: job.ID, Action: domain.ScheduledJobPause, ExpectedRevision: job.Revision, OperationKey: "automatic-pause-before-dispatch", RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(t.Context(), application.TransitionScheduledJobRequest{Version: domain.ScheduledJobControlProtocolVersion, RunID: run.ID, JobID: job.ID, Action: domain.ScheduledJobResume, ExpectedRevision: paused.Job.Revision, OperationKey: "automatic-resume-before-dispatch", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunDueConfirmed(t.Context(), "after-resume", now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.Get(t.Context(), created.Job.ID, 10, 10)
	if err != nil || snapshot.Job.Status != domain.ScheduledJobCompleted || snapshot.Rounds[0].Attempt != 1 || turns.calls != 1 {
		t.Fatalf("resumed=%+v calls=%d err=%v", snapshot, turns.calls, err)
	}
}

func TestScheduledConfirmedSelectionOrdersBothConsentKindsAndSkipsLiveLease(t *testing.T) {
	st, run, _ := scheduledAutomationFixture(t)
	now := time.Now().UTC().Add(time.Second)
	svc := application.NewScheduledJobService(st).WithClock(&scheduledStaticClock{now: now}).WithExecution(&scheduledAutomationTurns{}, nil)
	automatic, err := svc.Create(t.Context(), scheduledAutomationRequest(run, now))
	if err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, planRun, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "observe task", Profile: "code", Surface: "code", Phase: "plan", WorkspaceID: "scheduled-workspace", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	planRun, err = runs.Start(t.Context(), planRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := scheduledReadOnlyRequest(planRun.ID, now.Add(-time.Second), "unconfirmed-older-observation")
	legacy.MaxModelCalls = 0
	if _, err := svc.Create(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	observation := scheduledReadOnlyRequest(planRun.ID, now.Add(time.Second), "confirmed-later-observation")
	observation.MaxModelCalls = 0
	observation.ObservationConsentVersion = 1
	confirmed, err := svc.Create(t.Context(), observation)
	if err != nil {
		t.Fatal(err)
	}
	first, _, found, err := st.ClaimDueConfirmedScheduledJob(t.Context(), "first-worker", now.Add(2*time.Second))
	if err != nil || !found || first.ID != automatic.Job.ID {
		t.Fatalf("oldest confirmed job=%s want=%s found=%v err=%v", first.ID, automatic.Job.ID, found, err)
	}
	second, _, found, err := st.ClaimDueConfirmedScheduledJob(t.Context(), "second-worker", now.Add(2*time.Second))
	if err != nil || !found || second.ID != confirmed.Job.ID {
		t.Fatalf("next confirmed job=%s want=%s found=%v err=%v", second.ID, confirmed.Job.ID, found, err)
	}
}
