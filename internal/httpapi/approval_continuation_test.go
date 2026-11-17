package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/tools"
)

type reviewedProposalContinuationStub struct {
	threadTurnControllerStub
	request application.ApprovalContinuationRequest
}

func (s *reviewedProposalContinuationStub) ResumeApproval(ctx context.Context, r application.ApprovalContinuationRequest) application.ApprovalContinuationResult {
	s.request = r
	return application.ApprovalContinuationResult{State: "failed", HandoffID: "run-handoff-test", ErrorCode: "UNAVAILABLE", Message: "审批已保存，后续执行未完成。"}
}

func TestFileReviewHTTPRetainsDurableDecisionWhenContinuationFails(t *testing.T) {
	f := newAPIFixture(t)
	edit, err := fileedit.NewManager(f.store).Propose(t.Context(), fileedit.Proposal{SessionID: f.run.SessionID, WorkspaceID: f.workspace.ID, WorkspaceRoot: f.workspace.RootPath, Path: "reviewed.txt", ProposedText: "approved intent only\n"})
	if err != nil {
		t.Fatal(err)
	}
	controller := &reviewedProposalContinuationStub{}
	f.api.fileEditReviewEnabled = true
	f.api.fileEditReviewController = application.NewFileEditReviewService(f.store)
	f.api.runExecutionEnabled = true
	f.api.threadTurnController = controller
	path := "/api/v1/runs/" + f.run.ID + "/file-edits/" + edit.ID + "/review"
	for _, replay := range []bool{false, true} {
		response := performSessionMessageRequest(t, f.api, http.MethodPost, path, testControlToken, "", "application/json", strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
		var envelope struct {
			Data FileEditReviewView `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		v := envelope.Data
		if response.Code != http.StatusAccepted || v.Edit.Status != "approved" || v.FileWritten || v.Replayed != replay || v.Continuation == nil || v.Continuation.State != "failed" {
			t.Fatalf("review %d: %s", response.Code, response.Body.String())
		}
		if controller.request.RunID != f.run.ID || controller.request.Kind != "file_edit" || controller.request.ProposalID != edit.ID {
			t.Fatalf("wrong continuation identity: %#v", controller.request)
		}
	}
	if _, err := os.Stat(filepath.Join(f.workspace.RootPath, "reviewed.txt")); !os.IsNotExist(err) {
		t.Fatalf("review wrote file: %v", err)
	}
	stored, err := f.store.GetFileEdit(t.Context(), edit.ID)
	if err != nil || stored.Status != fileedit.StatusApproved {
		t.Fatalf("decision not preserved: %#v %v", stored, err)
	}
}

type httpDirectApplyProvider struct {
	calls   int
	respond func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
}

func (p *httpDirectApplyProvider) Name() string { return "http-direct-apply" }
func (p *httpDirectApplyProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "mock-code", Provider: "http-direct-apply", Capabilities: []string{"chat", "tools"}}}, nil
}
func (p *httpDirectApplyProvider) Chat(ctx context.Context, r llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	return p.respond(ctx, r, p.calls)
}
func (p *httpDirectApplyProvider) StreamChat(ctx context.Context, r llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	resp, err := p.Chat(ctx, r)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 2)
	if resp.Text != "" {
		ch <- llm.ChatChunk{Text: resp.Text}
	}
	ch <- llm.FinalChatChunk(resp)
	close(ch)
	return ch, nil
}
func (p *httpDirectApplyProvider) SupportsTools(string) bool    { return true }
func (p *httpDirectApplyProvider) SupportsVision(string) bool   { return false }
func (p *httpDirectApplyProvider) SupportsJSONMode(string) bool { return true }

type httpAlwaysReviewPolicy struct {
	base policy.Checker
}

func (p httpAlwaysReviewPolicy) CheckText(ctx, text string) policy.Decision {
	return p.base.CheckText(ctx, text)
}

func (p httpAlwaysReviewPolicy) CheckToolCall(call tools.Call) policy.Decision {
	d := p.base.CheckToolCall(call)
	if call.Name == "create_file" || call.Name == "replace_file" || call.Name == "move_file" || call.Name == "delete_file" {
		d.NeedsApproval = true
	}
	return d
}

func TestFileReviewHTTPAppliesDirectlyAndHandlesCASConflict(t *testing.T) {
	for _, casConflict := range []bool{false, true} {
		name := "direct_apply_success"
		if casConflict {
			name = "cas_conflict_failed"
		}
		t.Run(name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "http-direct-apply.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ws := store.WorkspaceRecord{ID: "ws-http-direct-apply", Name: "http-direct-apply", RootPath: root, CreatedAt: time.Now().UTC()}
			if err := st.SaveWorkspace(t.Context(), ws); err != nil {
				t.Fatal(err)
			}
			p := &httpDirectApplyProvider{}
			p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 1:
					args := fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"propose_patch","path":"reviewed.txt","expected_sha256":%q,"replacements":[{"old_text":"original\n","new_text":"reviewed\n","expected_occurrences":1}]}`, fileedit.HashText("original\n"))
					return &llm.ChatResponse{Provider: p.Name(), Model: "mock-code", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "workspace_change", Arguments: json.RawMessage(args)}}}, nil
				case 2:
					raw, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait, Message: "Await review", Reason: "operator review"})
					return &llm.ChatResponse{Provider: p.Name(), Model: "mock-code", Text: string(raw)}, nil
				case 3:
					raw, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish, Message: "Continued after HTTP review", Summary: "done"})
					return &llm.ChatResponse{Provider: p.Name(), Model: "mock-code", Text: string(raw)}, nil
				default:
					return nil, fmt.Errorf("unexpected model call %d", index)
				}
			}
			caps := domain.ExecutionPermissionRuntimeCapabilities{
				OperatorApprovalEnabled: true,
				RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
			}
			_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
				Goal: "HTTP direct apply review", Profile: "code", Surface: "code", Phase: "deliver",
				WorkspaceID: ws.ID, ModelRoute: p.Name() + "/mock-code", Interactive: true,
				Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20},
			})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := st.GetThreadByRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "reviewed.txt")
			if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			ref := llm.ModelRef{Provider: p.Name(), Model: "mock-code"}
			router := llm.NewRouter(ref)
			router.RegisterProvider(p)
			router.SetRoute("code", ref)
			checker := httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}
			lifecycle := application.NewRunLifecycleControlService(st)
			execution := application.NewRunExecutionHandoffService(st, router, checker).
				WithExecutionPermissionCapabilities(caps)
			turns := application.NewThreadTurnServiceWithExecutionCapabilities(st, lifecycle, execution, caps)
			api, err := New(st, Config{
				AccessToken:                     testAccessToken,
				ControlToken:                    testControlToken,
				FileEditReviewEnabled:           true,
				FileEditReviewController:        application.NewFileEditReviewService(st),
				RunExecutionEnabled:      true,
				RunExecutionController:   execution,
				ThreadTurnController:     turns,
				AppVersion:               "http-direct-apply-test",
			})
			if err != nil {
				t.Fatal(err)
			}
			execRes, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
				Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "Update reviewed.txt", OperationKey: "http-direct-apply-turn", RequestedBy: "operator",
			})
			if err != nil {
				t.Fatal(err)
			}
			edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
			if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
				rounds, _ := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 10)
				t.Fatalf("edits=%+v err=%v exec=%+v rounds=%+v", edits, err, execRes, rounds)
			}
			if casConflict {
				if err := os.WriteFile(target, []byte("external drift\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			path := "/api/v1/runs/" + run.ID + "/file-edits/" + edits[0].ID + "/review"
			resp := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "", "application/json",
				strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
			var env struct {
				Data FileEditReviewView `json:"data"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
				t.Fatalf("status=%d body=%s err=%v", resp.Code, resp.Body.String(), err)
			}
			v := env.Data
			if resp.Code != http.StatusAccepted || v.Replayed || v.Continuation == nil || v.Continuation.State != "completed" {
				t.Fatalf("unexpected review response: status=%d view=%+v", resp.Code, v)
			}
			if casConflict {
				if v.FileWritten || v.Edit.Status != fileedit.StatusFailed || strings.TrimSpace(v.Edit.Reason) == "" {
					t.Fatalf("CAS conflict review must report failed+reason without file_written: %+v", v)
				}
				if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "external drift\n" {
					t.Fatalf("CAS conflict overwrote external file: %q err=%v", got, readErr)
				}
			} else {
				if !v.FileWritten || v.Edit.Status != fileedit.StatusApplied {
					t.Fatalf("direct apply review must report file_written=true and applied: %+v", v)
				}
				if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "reviewed\n" {
					t.Fatalf("direct apply did not write disk: %q err=%v", got, readErr)
				}
			}
			replayResp := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "", "application/json",
				strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
			var replayEnv struct {
				Data FileEditReviewView `json:"data"`
			}
			if err := json.Unmarshal(replayResp.Body.Bytes(), &replayEnv); err != nil {
				t.Fatal(err)
			}
			rv := replayEnv.Data
			if replayResp.Code != http.StatusAccepted || !rv.Replayed || rv.FileWritten || rv.Edit.Status != v.Edit.Status {
				t.Fatalf("replay review response mismatch: %+v", rv)
			}
		})
	}
}

