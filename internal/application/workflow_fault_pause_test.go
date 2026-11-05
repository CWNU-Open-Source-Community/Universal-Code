package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
)

// SQLite, services, tool gateway, approvals, claims, Run leases and the saved
// wall-clock deadline are real; only model replies use a deterministic provider.
func TestWorkflowFaultPauseDeadlineCleansPendingApproval(t *testing.T) {
	for _, mode := range []string{"unpaused", "run_pause", "workflow_pause"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			p := &specialistTestProvider{respond: func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				var call llm.ToolCall
				switch index {
				case 0:
					call = llm.ToolCall{ID: "pause-proposal", Name: "workspace_change", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"output.txt","expected_sha256":"missing","content":"pending approval\n"}`)}
				case 1:
					var receipt struct {
						Result struct {
							EditID string          `json:"edit_id"`
							Apply  json.RawMessage `json:"apply_arguments"`
						} `json:"result"`
					}
					if err := json.Unmarshal([]byte(scopedLatestResult(t, request)), &receipt); err != nil || receipt.Result.EditID == "" {
						return llm.ChatResponse{}, fmt.Errorf("missing real file proposal: %v", err)
					}
					call = llm.ToolCall{ID: "pause-apply", Name: "workspace_apply", Arguments: receipt.Result.Apply}
				default:
					return llm.ChatResponse{}, fmt.Errorf("unexpected model dispatch after approval stop: call=%d", index)
				}
				return llm.ChatResponse{ToolCalls: []llm.ToolCall{call}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
			}}
			st, run, service := pauseFaultFixture(t, ctx, p, root)
			step := workflowWorker("write")
			step.Task.Worker, step.Task.ToolCallLimit = &domain.WorkerProfile{}, 4
			spec := workflowSpec(step)
			spec.Limits.TimeoutMillis = 5000
			e := startHostWorkflow(t, service, run.ID, "fault-pause-"+mode, spec)
			deadline := e.CreatedAt.Add(5 * time.Second)
			for range 2 {
				pauseFaultTick(t, ctx, service)
			}
			e, err := service.Get(ctx, e.ID)
			if err != nil || e.Status != "waiting_approval" || p.calls != 2 || !time.Now().Before(deadline) {
				t.Fatalf("did not establish real approval wait before deadline: status=%s calls=%d err=%v", e.Status, p.calls, err)
			}
			var stepID string
			for _, group := range e.State.Maps {
				for _, item := range group.Items {
					stepID = item.StepExecutionID
				}
			}
			job, found, err := st.GetWorkflowStep(ctx, stepID)
			if err != nil || !found {
				t.Fatalf("missing real workflow step: found=%t err=%v", found, err)
			}
			child, err := st.GetAgentNode(ctx, job.AgentID)
			if err != nil || child.ActiveAttemptID == "" {
				t.Fatalf("missing live approval attempt: child=%+v err=%v", child, err)
			}
			ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: child.ActiveAttemptID}
			before := pauseFaultSnapshot(t, ctx, st, service, e.ID, stepID, ref, "approval_stop")
			if !before.ApprovalWaiting || !before.ApprovalPending || before.UnusedReservedTokens == 0 {
				t.Fatal("approval wait did not hold its child allocation")
			}
			switch mode {
			case "run_pause":
				_, err = application.NewRunService(st).Pause(ctx, run.ID)
			case "workflow_pause":
				e, err = service.Pause(ctx, e.ID, e.Version)
			}
			if err != nil {
				t.Fatal(err)
			}
			paused := pauseFaultSnapshot(t, ctx, st, service, e.ID, stepID, ref, "after_control")
			if mode != "unpaused" {
				pauseFaultTick(t, ctx, service)
				current := pauseFaultSnapshot(t, ctx, st, service, e.ID, stepID, ref, "before_deadline_tick")
				if current.ChildStatus != paused.ChildStatus || current.ActiveAttemptID != paused.ActiveAttemptID || current.AttemptStatus != paused.AttemptStatus || current.StepStatus != "pending" || p.calls != 2 {
					t.Fatal("unexpired pause changed or dispatched its child")
				}
			}
			// Wait for the persisted deadline itself. RunDue's now parameter alone
			// cannot advance time inside advanceClaimed; no clock/SQL injection.
			timer := time.NewTimer(time.Until(deadline) + 25*time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			t.Logf("deadline=%s now=%s provider=deterministic test provider (no real model)", deadline.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
			candidates, err := st.ListWorkflowCandidates(ctx, time.Time{}, "", time.Now().UTC(), 128)
			if err != nil {
				t.Fatal(err)
			}
			_, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "pause-fault-diagnostic")
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				if err := st.ReleaseWorkflowExecution(ctx, e.ID, "pause-fault-diagnostic"); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("expired candidate_count=%d diagnostic_claim_acquired=%t", len(candidates), claimed)
			for range 3 {
				if err := pauseFaultObserveTick(t, ctx, service); err != nil {
					t.Errorf("deadline cleanup tick must succeed: %v", err)
				}
			}
			after := pauseFaultSnapshot(t, ctx, st, service, e.ID, stepID, ref, "after_deadline_ticks")
			if after.Usage.TotalTokens != before.Usage.TotalTokens {
				t.Errorf("expiry changed charged token usage: before=%d after=%d", before.Usage.TotalTokens, after.Usage.TotalTokens)
			}
			if after.WorkflowStatus != "failed" || after.WorkflowReason != "workflow deadline exceeded" {
				t.Errorf("expired workflow must fail while %s: status=%s reason=%q", mode, after.WorkflowStatus, after.WorkflowReason)
			}
			pauseFaultAssertRetired(t, after)
			if p.calls != 2 {
				t.Errorf("expiry or resume dispatched model work: calls=%d want=2", p.calls)
			}
			if _, err := os.Stat(filepath.Join(root, "output.txt")); !os.IsNotExist(err) {
				t.Errorf("unapproved file must remain absent: %v", err)
			}
		})
	}
}

func pauseFaultFixture(t *testing.T, ctx context.Context, p *specialistTestProvider, root string) (*store.SQLiteStore, domain.Run, *application.WorkflowService) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pause.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: "pause-workspace", Name: "pause-workspace", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "deadline cleanup of a paused workflow", Profile: "code", ModelRoute: p.Name() + "/model", WorkspaceID: "pause-workspace", Budget: domain.Budget{MaxTurns: 20, MaxTokens: 128, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewSkillSelectionService(st, registry).Select(ctx, application.SelectSkillsRequest{RunID: run.ID, Names: []string{"code", "plan-delivery"}, OperationKey: "pause-fault-skills-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	checker := executionWorkerReviewPolicy{Checker: policy.NewDefaultChecker(), review: true}
	caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	runtime := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(caps)
	service := application.NewWorkflowService(&lifecycleFenceStore{SQLiteStore: st, t: t}, application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(runtime))
	return st, run, service
}

type pauseFaultState struct {
	WorkflowID, WorkflowStatus, WorkflowReason string
	Revision                                   int64
	Steps, Jobs, AllocatedJobs                 int
	StepID, StepStatus, ChildID                string
	ChildStatus                                domain.AgentStatus
	ActiveAttemptID, AttemptID                 string
	AttemptStatus                              domain.AgentAttemptStatus
	AttemptFailure                             domain.AgentAttemptFailure
	RunStatus                                  domain.RunStatus
	Budget                                     domain.Budget
	Usage                                      domain.RunAgentUsage
	ChildTurnLimit, ChildTurnsUsed             int64
	ChildTokenLimit, ChildTokensUsed           int64
	UnusedReservedTokens                       int64
	ApprovalWaiting, ApprovalPending           bool
	ApprovalRecords                            []string
}

func pauseFaultSnapshot(t *testing.T, ctx context.Context, st *store.SQLiteStore, service *application.WorkflowService, id, stepID string, ref domain.AgentAttemptRef, phase string) pauseFaultState {
	t.Helper()
	e, err := service.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := st.GetWorkflowStep(ctx, stepID)
	if err != nil || !found {
		t.Fatalf("read step: found=%t err=%v", found, err)
	}
	child, err := st.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, found, err := st.GetAgentAttempt(ctx, ref.AttemptID)
	if err != nil || !found {
		t.Fatalf("read attempt: found=%t err=%v", found, err)
	}
	run, err := st.GetRun(ctx, ref.RunID)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := st.GetRunAgentUsage(ctx, ref.RunID)
	if err != nil {
		t.Fatal(err)
	}
	waiting, pending, err := st.SpecialistToolApprovalState(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	records, err := st.ListApprovals(ctx, approval.ListFilter{RunID: ref.RunID})
	if err != nil {
		t.Fatal(err)
	}
	s := pauseFaultState{WorkflowID: id, WorkflowStatus: e.Status, WorkflowReason: e.Reason, Revision: e.Version, Steps: e.State.Steps, Jobs: e.State.Jobs, AllocatedJobs: e.State.AllocatedJobs, StepID: job.ID, StepStatus: job.Status, ChildID: child.ID, ChildStatus: child.Status, ActiveAttemptID: child.ActiveAttemptID, AttemptID: attempt.ID, AttemptStatus: attempt.Status, AttemptFailure: attempt.Failure, RunStatus: run.Status, Budget: run.Budget, Usage: usage, ChildTurnLimit: child.TurnLimit, ChildTurnsUsed: child.TurnsUsed, ChildTokenLimit: child.TokenLimit, ChildTokensUsed: child.TokensUsed, ApprovalWaiting: waiting, ApprovalPending: pending}
	// Derived from the same status/limit/usage policy as specialistReservationsTx;
	// this is unused reserved capacity, distinct from charged Run token usage.
	if !child.Terminal() {
		s.UnusedReservedTokens = max(0, child.TokenLimit-child.TokensUsed)
	}
	for _, record := range records {
		s.ApprovalRecords = append(s.ApprovalRecords, record.ID+":"+string(record.Status))
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", phase, raw)
	return s
}

func pauseFaultTick(t *testing.T, ctx context.Context, service *application.WorkflowService) {
	t.Helper()
	if err := pauseFaultObserveTick(t, ctx, service); err != nil {
		t.Fatal(err)
	}
}

func pauseFaultObserveTick(t *testing.T, ctx context.Context, service *application.WorkflowService) error {
	t.Helper()
	ran, err := service.RunDue(ctx, "pause-fault-host", time.Now().UTC())
	t.Logf("host RunDue ran=%t err=%v", ran, err)
	var detail *apperror.Error
	if errors.As(err, &detail) {
		t.Logf("host error code=%s cause=%v", detail.Code, detail.Cause)
	}
	return err
}

func pauseFaultAssertRetired(t *testing.T, state pauseFaultState) {
	t.Helper()
	if state.StepStatus != "failed" || state.ChildStatus != domain.AgentCancelled || state.ActiveAttemptID != "" || state.AttemptStatus != domain.AgentAttemptInterrupted || state.UnusedReservedTokens != 0 || state.ApprovalWaiting || state.ApprovalPending {
		t.Errorf("deadline must retire pending step/child/attempt, release unused reservation and end active approval wait: step=%s child=%s active_attempt=%q attempt=%s unused_reserved_tokens=%d approval_waiting=%t approval_pending=%t", state.StepStatus, state.ChildStatus, state.ActiveAttemptID, state.AttemptStatus, state.UnusedReservedTokens, state.ApprovalWaiting, state.ApprovalPending)
	}
}
