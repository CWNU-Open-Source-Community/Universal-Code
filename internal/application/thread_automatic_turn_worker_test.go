package application_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestAutomaticThreadTurnWaitsForReviewedWorkerToResume(t *testing.T) {
	for _, decision := range []application.FileEditReviewAction{application.FileEditApproveIntent, application.FileEditDeny} {
		t.Run(string(decision), func(t *testing.T) {
			st, run, workspace, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 10, MaxTokens: 20000, MaxToolCalls: 20})
			var editID string
			provider := &boundaryJourneyProvider{}
			provider.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 1, 6:
					return textResponse(rootActionResponse(domain.RootActionFinish, "Ready for review", "done", "")), nil
				case 2:
					return boundaryRead("worker-read", 1), nil
				case 3:
					return toolResponse("worker-create", "workspace_change", `{"version":"agent-code-tools.v1","action":"create","path":"worker.txt","expected_sha256":"missing","content":"worker result\n"}`), nil
				case 4:
					var receipt struct {
						Result struct {
							EditID string          `json:"edit_id"`
							Apply  json.RawMessage `json:"apply_arguments"`
						} `json:"result"`
					}
					last := req.Messages[len(req.Messages)-1]
					if len(last.ToolResults) != 1 {
						t.Fatalf("missing proposal receipt: %+v", last)
					}
					if err := json.Unmarshal([]byte(last.ToolResults[0].Content), &receipt); err != nil || receipt.Result.EditID == "" {
						t.Fatalf("worker proposal: %s, %v", last.ToolResults[0].Content, err)
					}
					editID = receipt.Result.EditID
					return toolResponse("worker-apply", "workspace_apply", string(receipt.Result.Apply)), nil
				case 5:
					return textResponse(specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Review decision observed", Report: &domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "The reviewed tool call is settled", WorkItemIDs: []string{}, NoteIDs: []string{}}})), nil
				default:
					t.Fatalf("unexpected model call %d", index)
					return nil, nil
				}
			}
			checker := executionWorkerReviewPolicy{Checker: policy.NewDefaultChecker(), review: true}
			turns := toolBoundaryServiceWithPolicy(st, st, provider, checker)
			first, err := turns.ExecuteAutomatic(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			root, found, err := st.GetRootAgent(t.Context(), run.ID)
			if err != nil || !found {
				t.Fatalf("root: %+v %v", root, err)
			}
			coord, err := coordinator.NewWithSpecialistAdmission(st, coordinator.SpecialistAdmissionPolicy{MaxChildren: 2, MaxTurnsPerChild: 3, MaxTokensPerChild: 4000})
			if err != nil {
				t.Fatal(err)
			}
			admit := func(key string, worker *domain.WorkerProfile) domain.AgentNode {
				t.Helper()
				result, err := coord.AdmitSpecialist(t.Context(), coordinator.AdmitSpecialistRequest{RunID: run.ID, ParentAgentID: root.ID, Title: "Worker review", Skills: []string{"model.chat"}, TurnLimit: 3, TokenLimit: 4000, ToolCallLimit: 10, Worker: worker, IdempotencyKey: key})
				if err != nil {
					t.Fatal(err)
				}
				return result.Agent
			}
			ordinary := admit("automatic-ready-child", nil)
			child := admit("automatic-worker-child", &domain.WorkerProfile{})
			if ordinary.Status != domain.AgentReady || child.Status != domain.AgentReady {
				t.Fatal("expected ready children")
			}
			if err := turns.CheckAutomaticTurn(t.Context(), request.ThreadID); err != nil {
				t.Fatalf("ready children must not block: %v", err)
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			runtime := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true})
			workers := application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(runtime)
			workerRequest := application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "automatic-worker-execution"}
			waiting, err := workers.ExecuteSpecialist(t.Context(), workerRequest)
			if err != nil || !waiting.WaitingApproval || len(provider.Requests()) != 4 {
				t.Fatalf("worker wait: %+v %v", waiting, err)
			}
			observe := func(want string) {
				t.Helper()
				got, err := turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, first.Submission.Message.ID)
				if err != nil || got.State != want || got.TerminalFailure {
					t.Fatalf("observation: %+v %v; want %s", got, err, want)
				}
				if want == "running" && !strings.Contains(got.Reason, "worker") {
					t.Fatalf("missing worker resume reason: %+v", got)
				}
			}
			observe("waiting_approval")
			if err := turns.CheckAutomaticTurn(t.Context(), request.ThreadID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("pending review admission: %v", err)
			}
			if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: editID, Action: decision}); err != nil {
				t.Fatal(err)
			}
			observe("running")
			if err := turns.CheckAutomaticTurn(t.Context(), request.ThreadID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "worker") {
				t.Fatalf("reviewed worker admission: %v", err)
			}
			next := request
			next.OperationKey = "automatic-after-worker"
			if _, err := turns.ExecuteAutomatic(t.Context(), next); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || len(provider.Requests()) != 4 {
				t.Fatalf("automatic execution crossed worker wait: %v", err)
			}
			if replay, err := turns.ExecuteAutomatic(t.Context(), request); err != nil || !replay.Replayed || len(provider.Requests()) != 4 {
				t.Fatalf("completed root replay must remain readable: %+v %v", replay, err)
			}
			if queue, err := st.GetOperatorSteeringQueueSummary(t.Context(), run.ID); err != nil || queue.Pending+queue.Prepared != 0 {
				t.Fatalf("rejected automatic input entered the queue: %+v %v", queue, err)
			}
			if _, err := os.Stat(filepath.Join(workspace, "worker.txt")); !os.IsNotExist(err) {
				t.Fatalf("observation dispatched the worker edit: %v", err)
			}
			resumed, err := workers.ExecuteSpecialist(t.Context(), workerRequest)
			if err != nil || resumed.AttemptID != waiting.AttemptID || resumed.AttemptStatus != domain.AgentAttemptFinished {
				t.Fatalf("worker resume: %+v %v", resumed, err)
			}
			contents, readErr := os.ReadFile(filepath.Join(workspace, "worker.txt"))
			if decision == application.FileEditApproveIntent && (readErr != nil || string(contents) != "worker result\n") {
				t.Fatalf("approved worker edit was not applied: %q %v", contents, readErr)
			}
			if decision == application.FileEditDeny && !os.IsNotExist(readErr) {
				t.Fatalf("denied worker edit was applied: %v", readErr)
			}
			observe("completed")
			if err := turns.CheckAutomaticTurn(t.Context(), request.ThreadID); err != nil {
				t.Fatalf("settled worker still blocks: %v", err)
			}
			if _, err := turns.ExecuteAutomatic(t.Context(), next); err != nil || len(provider.Requests()) != 6 {
				t.Fatalf("automatic execution after worker: %v", err)
			}
		})
	}
}
