package application_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func newThreadPlanTurns(st *store.SQLiteStore, provider *scriptedToolProvider) *application.ThreadTurnService {
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
}

func planWait() *llm.ChatResponse {
	return textResponse(rootActionResponse(domain.RootActionWait, "Plan ready for review.", "", "operator confirmation required"))
}

func newThreadPlanFixture(t *testing.T, responses ...*llm.ChatResponse) (*store.SQLiteStore, domain.Run, *scriptedToolProvider, *application.ThreadTurnService) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "thread-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "workspace-thread-plan", Name: "Thread Plan", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "Original goal: preserve source and restrictions", Profile: "review", Surface: "code", Phase: "plan", Interactive: true, WorkspaceID: "workspace-thread-plan", ModelRoute: "tool-loop/model", Budget: domain.Budget{MaxTurns: 16, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 16)}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedToolProvider{responses: responses}
	return st, run, provider, newThreadPlanTurns(st, provider)
}

func TestThreadPlanModeSwitchAndReplanDoNotCopySelectedAuthority(t *testing.T) {
	st, run, provider, turns := newThreadPlanFixture(t,
		toolResponse("selected-plan", "plan_delivery_propose", planDeliveryTestPayload), planWait(),
		textResponse(rootActionResponse(domain.RootActionContinue, "Continue after selected plan", "", "")),
		toolResponse("replacement-plan", "plan_delivery_propose", planDeliveryTestPayload), planWait())
	direct := application.ThreadPlanControlRequest{Version: application.PlanDeliveryControlProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), RunID: run.ID, Action: "enter_deliver", OperationKey: "thread-plan-direct-without-proposal", RequestedBy: "operator"}
	result, err := turns.ControlPlan(t.Context(), direct)
	if err != nil || result.State != "completed" || result.ModelCalled || result.AppliedMode.Phase != domain.ExecutionPhaseDeliver {
		t.Fatalf("direct mode=%+v %v", result, err)
	}
	plan := direct
	plan.Action = "enter_plan"
	plan.OperationKey = "thread-plan-enter-before-proposal"
	if _, err := turns.ControlPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	sendThreadPlanInput(t, turns, run, "before-replan", "Original requirement: keep compatibility; original uncompleted constraint.")
	proposal := latestThreadPlan(t, st, run.ID)
	if _, err := turns.ControlPlan(t.Context(), confirmThreadPlan(run, proposal, "first-execution")); err != nil {
		t.Fatal(err)
	}
	oldEvents, _ := st.ListRunEvents(t.Context(), run.ID)
	replan := plan
	replan.OperationKey = "thread-plan-explicit-new-planning"
	result, err = turns.ControlPlan(t.Context(), replan)
	if err != nil || result.State != "completed" || result.RunID != run.ID || result.ModelCalled || result.AppliedMode.Phase != domain.ExecutionPhasePlan {
		t.Fatalf("replan=%+v %v", result, err)
	}
	thread, err := st.GetThread(t.Context(), plan.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.ActiveRunID == run.ID || thread.ActiveRunID == "" {
		t.Fatal("replan has no successor")
	}
	next, err := st.GetRun(t.Context(), thread.ActiveRunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.GetPlanDeliverySelectionByRun(t.Context(), next.ID); err != nil || found {
		t.Fatalf("old authorization carried=%t %v", found, err)
	}
	if values, err := st.ListPlanDeliveryProposals(t.Context(), next.ID, 10); err != nil || len(values) != 0 {
		t.Fatal("old proposal carried into fresh planning")
	}
	if items, err := st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: next.ID}); err != nil || len(items) != 0 {
		t.Fatal("old selected work items carried")
	}
	if len(provider.Requests()) != 3 {
		t.Fatal("mode change called model")
	}
	observed, err := newThreadPlanTurns(st, provider).InspectPlan(t.Context(), replan)
	if err != nil || observed.State != "completed" {
		t.Fatalf("observe successor=%+v %v", observed, err)
	}
	replay, err := newThreadPlanTurns(st, provider).ControlPlan(t.Context(), replan)
	if err != nil || replay.AppliedMode.ID != result.AppliedMode.ID {
		t.Fatalf("replay successor=%+v %v", replay, err)
	}
	bindings, _ := st.ListThreadRuns(t.Context(), plan.ThreadID)
	if len(bindings) != 2 {
		t.Fatalf("duplicate successor=%v", bindings)
	}
	_, err = turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: plan.ThreadID, Content: "Replan to retain compatibility and add a second requirement.", OperationKey: "thread-plan-new-requirements-turn", RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	var requestText strings.Builder
	for _, message := range provider.Requests()[3].Messages {
		requestText.WriteString(message.Content)
	}
	if !strings.Contains(requestText.String(), "original uncompleted constraint") {
		t.Fatal("successor lost original constraint")
	}
	afterEvents, _ := st.ListRunEvents(t.Context(), run.ID)
	if countEventType(afterEvents, events.PlanDeliveryDirectionSelectedEvent) != countEventType(oldEvents, events.PlanDeliveryDirectionSelectedEvent) {
		t.Fatal("old selection changed")
	}
}

func sendThreadPlanInput(t *testing.T, turns *application.ThreadTurnService, run domain.Run, key, content string) {
	t.Helper()
	_, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: content, OperationKey: "thread-plan-input-" + key, RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
}

func latestThreadPlan(t *testing.T, st *store.SQLiteStore, runID string) domain.PlanDeliveryProposal {
	t.Helper()
	values, err := st.ListPlanDeliveryProposals(t.Context(), runID, 10)
	if err != nil || len(values) == 0 {
		t.Fatalf("proposals=%v err=%v", values, err)
	}
	return values[0]
}

func confirmThreadPlan(run domain.Run, proposal domain.PlanDeliveryProposal, key string) application.ThreadPlanControlRequest {
	return application.ThreadPlanControlRequest{Version: application.PlanDeliveryControlProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), RunID: run.ID, Action: "confirm", ProposalID: proposal.ID, Direction: 2, ManualAcceptance: domain.PlanDeliveryManualAcceptanceOnDemand, Content: "Confirm the revised Balanced plan and execute within the existing restrictions.", OperationKey: "thread-plan-confirm-" + key, RequestedBy: "operator"}
}

func TestThreadPlanConfirmLatestProposalSameThreadAndExactReplay(t *testing.T) {
	st, run, provider, turns := newThreadPlanFixture(t, toolResponse("plan-one", "plan_delivery_propose", planDeliveryTestPayload), planWait(), toolResponse("plan-two", "plan_delivery_propose", strings.ReplaceAll(planDeliveryTestPayload, "bounded core path", "corrected bounded core path")), planWait(), textResponse(rootActionResponse(domain.RootActionContinue, "Execution started within the revised scope.", "", "")))
	sendThreadPlanInput(t, turns, run, "initial-plan", "Plan first; preserve source and restrictions.")
	first := latestThreadPlan(t, st, run.ID)
	sendThreadPlanInput(t, turns, run, "revise-plan", "Correction: retain the old format and avoid changing public APIs.")
	latest := latestThreadPlan(t, st, run.ID)
	if latest.ID == first.ID {
		t.Fatal("no revised proposal")
	}
	if _, err := turns.ControlPlan(t.Context(), confirmThreadPlan(run, first, "stale-plan")); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("old proposal: %v", err)
	}
	request := confirmThreadPlan(run, latest, "confirm-latest")
	result, err := turns.ControlPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || result.RunID != run.ID || result.ThreadID != request.ThreadID || !result.ModelCalled || !result.ExecutionStarted || result.AppliedMode == nil || result.AppliedMode.Phase != domain.ExecutionPhaseDeliver {
		t.Fatalf("confirmation=%+v", result)
	}
	requests := provider.Requests()
	if len(requests) != 5 {
		t.Fatalf("model calls=%d", len(requests))
	}
	var delivered strings.Builder
	for _, m := range requests[4].Messages {
		delivered.WriteString(m.Content)
	}
	for _, fact := range []string{"preserve source and restrictions", "Correction: retain the old format", request.Content, "Balanced"} {
		if !strings.Contains(delivered.String(), fact) {
			t.Errorf("missing actual model context %q", fact)
		}
	}
	bindings, _ := st.ListThreadRuns(t.Context(), request.ThreadID)
	if len(bindings) != 1 {
		t.Fatalf("unexpected successor: %v", bindings)
	}
	beforeEvents, _ := st.ListRunEvents(t.Context(), run.ID)
	observed, err := newThreadPlanTurns(st, provider).InspectPlan(t.Context(), request)
	if err != nil || observed.State != "completed" || observed.ModelCalled || observed.TurnRequest == nil {
		t.Fatalf("pure observation=%+v %v", observed, err)
	}
	replay, err := newThreadPlanTurns(st, provider).ControlPlan(t.Context(), request)
	if err != nil || replay.State != "completed" || len(provider.Requests()) != 5 {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	afterEvents, _ := st.ListRunEvents(t.Context(), run.ID)
	if len(afterEvents) != len(beforeEvents) {
		t.Fatal("GET/replay wrote events")
	}
	other := request
	other.OperationKey = "another-window-confirm"
	if _, err := turns.ControlPlan(t.Context(), other); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("different key=%v", err)
	}
	changed := request
	changed.Content = "Different later instruction"
	if _, err := turns.ControlPlan(t.Context(), changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed body=%v", err)
	}
	if countEventType(afterEvents, events.PlanDeliveryDirectionSelectedEvent) != 1 {
		t.Fatal("duplicate selection")
	}
}

func TestThreadPlanRejectsNewRequirementsWithoutUpdatedProposal(t *testing.T) {
	st, run, _, turns := newThreadPlanFixture(t, toolResponse("old-plan", "plan_delivery_propose", planDeliveryTestPayload), planWait(), planWait())
	sendThreadPlanInput(t, turns, run, "first", "Plan this task")
	proposal := latestThreadPlan(t, st, run.ID)
	sendThreadPlanInput(t, turns, run, "new-requirements", "Do not modify the original file format.")
	if _, err := turns.ControlPlan(t.Context(), confirmThreadPlan(run, proposal, "outdated-confirm")); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale input accepted: %v", err)
	}
	if value, err := turns.InspectPlan(t.Context(), confirmThreadPlan(run, proposal, "outdated-confirm")); err != nil || value.State != "not_received" {
		t.Fatalf("pre-admission rejection left an unresolved request: %+v %v", value, err)
	}
	_, found, err := st.GetPlanDeliverySelectionByRun(context.Background(), run.ID)
	if err != nil || found {
		t.Fatal("stale plan selected")
	}
}

func TestThreadPlanModeRevisionChangeRejectsBeforeReservingConfirmation(t *testing.T) {
	st, run, _, turns := newThreadPlanFixture(t, toolResponse("versioned-plan", "plan_delivery_propose", planDeliveryTestPayload), planWait())
	sendThreadPlanInput(t, turns, run, "plan-version", "Plan this task")
	request := confirmThreadPlan(run, latestThreadPlan(t, st, run.ID), "stale-mode-revision")
	mode := application.ThreadPlanControlRequest{Version: request.Version, ThreadID: request.ThreadID, RunID: run.ID, Action: "enter_deliver", OperationKey: "switch-direct-before-new-plan", RequestedBy: "operator"}
	if _, err := turns.ControlPlan(t.Context(), mode); err != nil {
		t.Fatal(err)
	}
	mode.Action = "enter_plan"
	mode.OperationKey = "switch-plan-new-revision-no-proposal"
	if _, err := turns.ControlPlan(t.Context(), mode); err != nil {
		t.Fatal(err)
	}
	if _, err := turns.ControlPlan(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale mode confirmation=%v", err)
	}
	if value, err := turns.InspectPlan(t.Context(), request); err != nil || value.State != "not_received" {
		t.Fatalf("stale mode left unresolved preparation=%+v %v", value, err)
	}
}

func TestThreadPlanTerminalModeContinuationCreatesNoUserMessageOrSelection(t *testing.T) {
	st, run, p, turns := newThreadPlanFixture(t)
	runs := application.NewRunService(st)
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(t.Context(), run.ID, "terminal fixture before explicit replanning"); err != nil {
		t.Fatal(err)
	}
	request := application.ThreadPlanControlRequest{Version: application.PlanDeliveryControlProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), RunID: run.ID, Action: "enter_plan", OperationKey: "terminal-run-explicit-plan-operation", RequestedBy: "operator"}
	result, err := turns.ControlPlan(t.Context(), request)
	if err != nil || result.State != "completed" || result.ModelCalled || result.ExecutionStarted {
		t.Fatalf("terminal replan=%+v %v", result, err)
	}
	thread, err := st.GetThread(t.Context(), request.ThreadID)
	if err != nil || thread.ActiveRunID == run.ID || thread.ActiveRunID == "" {
		t.Fatalf("thread=%+v %v", thread, err)
	}
	if _, selected, err := st.GetPlanDeliverySelectionByRun(t.Context(), thread.ActiveRunID); err != nil || selected {
		t.Fatalf("selection=%t %v", selected, err)
	}
	next, _ := st.GetRun(t.Context(), thread.ActiveRunID)
	messages, err := st.ListSessionMessages(t.Context(), next.SessionID, true)
	if err != nil || len(messages) != 0 || len(p.Requests()) != 0 {
		t.Fatal("mode change invented a user message or called model")
	}
}
