package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type workflowReviewDisconnectStore struct {
	*store.SQLiteStore
	cancel     context.CancelFunc
	oldCommits int
	newCommits int
}

func (s *workflowReviewDisconnectStore) DecideApproval(ctx context.Context, request approval.DecisionRequest) (approval.DecisionResult, error) {
	result, err := s.SQLiteStore.DecideApproval(ctx, request)
	if err == nil {
		s.oldCommits++
		s.cancel()
	}
	return result, err
}

func (s *workflowReviewDisconnectStore) CommitFileEditReview(ctx context.Context, expected fileedit.Edit, observed approval.Record, request approval.DecisionRequest) (fileedit.Edit, bool, error) {
	edit, replayed, err := s.SQLiteStore.CommitFileEditReview(ctx, expected, observed, request)
	if err == nil {
		s.newCommits++
		s.cancel()
	}
	return edit, replayed, err
}

func TestWorkflowReviewDecisionCommitDisconnectBeforeEditStateSurvivesReopen(t *testing.T) {
	for _, scenario := range []struct {
		action  application.FileEditReviewAction
		control string
	}{
		{action: application.FileEditApproveIntent},
		{action: application.FileEditDeny},
		{action: application.FileEditApproveIntent, control: "workflow_pause"},
		{action: application.FileEditDeny, control: "workflow_pause"},
		{action: application.FileEditApproveIntent, control: "run_cancel"},
		{action: application.FileEditDeny, control: "run_cancel"},
	} {
		action := scenario.action
		t.Run(string(action)+"/"+scenario.control, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			var editID, resumedToolResult string
			p := &specialistTestProvider{respond: func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				call := func(name, id string, payload json.RawMessage) llm.ChatResponse {
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{{Name: name, ID: id, Arguments: payload}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
				}
				switch index {
				case 0:
					return call("workspace_change", "restart-proposal", json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"output.txt","expected_sha256":"missing","content":"approved restart output\n"}`)), nil
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
					editID = receipt.Result.EditID
					return call("workspace_apply", "restart-apply", receipt.Result.Apply), nil
				case 2:
					resumedToolResult = scopedLatestResult(t, request)
					return workflowResponse(t, domain.CompletionSucceeded), nil
				default:
					return llm.ChatResponse{}, errors.New("unexpected repeated model call")
				}
			}}
			st, run, _, _ := newScopedSubagentFixture(t, p, domain.Budget{MaxTurns: 20, MaxTokens: 10000, MaxToolCalls: 20}, 2, 128, false, domain.SpecialistAdmission{MaxChildren: domain.MaxAgentChildren}, home, root)
			router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
			router.RegisterProvider(p)
			checker := executionWorkerReviewPolicy{Checker: policy.NewDefaultChecker(), review: true}
			capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			makeService := func(st *store.SQLiteStore) *application.WorkflowService {
				runtime := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(capabilities)
				return application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(runtime))
			}
			svc := makeService(st)
			step := workflowWorker("write")
			step.Task.Worker, step.Task.ToolCallLimit = &domain.WorkerProfile{}, 4
			e := startHostWorkflow(t, svc, run.ID, "review-commit-disconnect-"+string(action), workflowSpec(step))
			workflowTick(t, svc)
			workflowTick(t, svc)
			e, err := svc.Get(t.Context(), e.ID)
			if err != nil || e.Status != "waiting_approval" || p.calls != 2 {
				t.Fatalf("real approval wait was not reached: execution=%+v calls=%d err=%v", e, p.calls, err)
			}
			var stepID string
			for _, group := range e.State.Maps {
				for _, item := range group.Items {
					stepID = item.StepExecutionID
				}
			}
			originalStep, found, err := st.GetWorkflowStep(t.Context(), stepID)
			if err != nil || !found {
				t.Fatalf("missing real step: %+v found=%t err=%v", originalStep, found, err)
			}
			originalChild, err := st.GetAgentNode(t.Context(), originalStep.AgentID)
			if err != nil || originalChild.ActiveAttemptID == "" {
				t.Fatalf("missing approval attempt: %+v err=%v", originalChild, err)
			}
			beforeUsage, err := st.GetRunAgentUsage(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.control == "workflow_pause" {
				e, err = svc.Pause(t.Context(), e.ID, e.Version)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: editID, Action: action}
			disconnect := &workflowReviewDisconnectStore{SQLiteStore: st, cancel: cancel}
			_, err = application.NewFileEditReviewService(disconnect).Review(ctx, request)
			if disconnect.oldCommits+disconnect.newCommits != 1 || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("post-commit disconnect was not injected once: old=%d atomic=%d ctx=%v err=%v", disconnect.oldCommits, disconnect.newCommits, ctx.Err(), err)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("post-commit review error changed: %v", err)
			}
			decision, err := st.GetApprovalByProposal(t.Context(), editID)
			if err != nil || decision.Status == approval.StatusPending {
				t.Fatalf("decision did not commit before disconnect: %+v err=%v", decision, err)
			}
			edit, err := st.GetFileEdit(t.Context(), editID)
			if disconnect.newCommits != 1 || err != nil || (edit.Status == fileedit.StatusApproved) != (action == application.FileEditApproveIntent) || (edit.Status != fileedit.StatusApproved && edit.Status != fileedit.StatusDenied) {
				t.Errorf("decision and edit did not commit together: atomic_commits=%d edit=%+v err=%v", disconnect.newCommits, edit, err)
			}
			if scenario.control == "run_cancel" {
				if _, err := application.NewRunService(st).Cancel(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			svc = makeService(st)
			for range 3 {
				workflowTick(t, svc)
			}
			e, err = svc.Get(t.Context(), e.ID)
			if scenario.control == "workflow_pause" {
				pausedChild, childErr := st.GetAgentNode(t.Context(), originalStep.AgentID)
				pausedUsage, usageErr := st.GetRunAgentUsage(t.Context(), run.ID)
				if err != nil || e.Status != "paused" || p.calls != 2 || childErr != nil || pausedChild.ActiveAttemptID != originalChild.ActiveAttemptID || usageErr != nil || pausedUsage.TotalTokens != beforeUsage.TotalTokens {
					t.Fatalf("reopen or review resumed paused work: execution=%+v calls=%d child=%+v usage=%+v errors=%v/%v/%v", e, p.calls, pausedChild, pausedUsage, err, childErr, usageErr)
				}
				if _, err := svc.Resume(t.Context(), e.ID, e.Version); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					workflowTick(t, svc)
				}
				e, err = svc.Get(t.Context(), e.ID)
			}
			wantStatus, wantCalls, addedTokens := "completed", 3, int64(5)
			if scenario.control == "run_cancel" {
				wantStatus, wantCalls, addedTokens = "failed", 2, 0
			}
			if err != nil || e.Status != wantStatus || p.calls != wantCalls {
				t.Errorf("resumed original workflow did not finish once: execution=%+v calls=%d err=%v", e, p.calls, err)
			}
			afterStep, found, err := st.GetWorkflowStep(t.Context(), stepID)
			if err != nil || !found || afterStep.ID != originalStep.ID || afterStep.AgentID != originalStep.AgentID || afterStep.SessionID != originalStep.SessionID {
				t.Errorf("step identity changed: before=%+v after=%+v found=%t err=%v", originalStep, afterStep, found, err)
			}
			attempts, err := st.ListAgentAttempts(t.Context(), originalStep.AgentID)
			if err != nil || len(attempts) != 1 || attempts[0].ID != originalChild.ActiveAttemptID {
				t.Errorf("approval created a replacement attempt: %+v err=%v", attempts, err)
			}
			afterUsage, err := st.GetRunAgentUsage(t.Context(), run.ID)
			if err != nil || afterUsage.TotalTokens != beforeUsage.TotalTokens+addedTokens {
				t.Errorf("resumption repeated model cost: before=%+v after=%+v err=%v", beforeUsage, afterUsage, err)
			}
			edit, err = st.GetFileEdit(t.Context(), editID)
			content, readErr := os.ReadFile(filepath.Join(root, "output.txt"))
			if scenario.control == "run_cancel" {
				storedRun, runErr := st.GetRun(t.Context(), run.ID)
				if err != nil || runErr != nil || storedRun.Status != domain.RunCancelled || !os.IsNotExist(readErr) || afterStep.Status != "failed" || len(attempts) != 1 || attempts[0].Status != domain.AgentAttemptInterrupted {
					t.Errorf("reopen resumed cancelled work: run=%s step=%s attempts=%+v read_err=%v errors=%v/%v", storedRun.Status, afterStep.Status, attempts, readErr, err, runErr)
				}
			} else if action == application.FileEditApproveIntent {
				if err != nil || edit.Status != fileedit.StatusApplied || readErr != nil || string(content) != "approved restart output\n" || !strings.Contains(resumedToolResult, `"status":"applied"`) {
					t.Errorf("approved intent did not recover: edit_status=%s content=%q read_err=%v store_err=%v receipt=%s", edit.Status, content, readErr, err, resumedToolResult)
				}
			} else if err != nil || edit.Status != fileedit.StatusDenied || !os.IsNotExist(readErr) || !strings.Contains(resumedToolResult, `"status":"denied"`) {
				t.Errorf("denied intent did not recover: edit_status=%s content=%q read_err=%v store_err=%v receipt=%s", edit.Status, content, readErr, err, resumedToolResult)
			}
			t.Logf("atomic_commits=%d model_calls=%d tokens=%d->%d workflow=%s original_step=%s original_attempt=%s", disconnect.newCommits, p.calls, beforeUsage.TotalTokens, afterUsage.TotalTokens, e.Status, originalStep.ID, originalChild.ActiveAttemptID)
		})
	}
}
