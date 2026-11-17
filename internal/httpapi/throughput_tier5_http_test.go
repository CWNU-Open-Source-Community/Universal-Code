package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type tier5HTTPProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	respond  func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
}

func (*tier5HTTPProvider) Name() string { return "http-tier5" }
func (*tier5HTTPProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "mock-code", Provider: "http-tier5", Capabilities: []string{"chat", "tools"}}}, nil
}
func (p *tier5HTTPProvider) Chat(ctx context.Context, r llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	idx := len(p.requests) - 1
	fn := p.respond
	p.mu.Unlock()
	if fn == nil {
		return nil, errors.New("tier5HTTPProvider respond is nil")
	}
	return fn(ctx, r, idx)
}
func (p *tier5HTTPProvider) StreamChat(ctx context.Context, r llm.ChatRequest) (<-chan llm.ChatChunk, error) {
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
func (*tier5HTTPProvider) SupportsTools(string) bool    { return true }
func (*tier5HTTPProvider) SupportsVision(string) bool   { return false }
func (*tier5HTTPProvider) SupportsJSONMode(string) bool { return true }
func (p *tier5HTTPProvider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

type tier5HTTPFixture struct {
	st       *store.SQLiteStore
	root     string
	ws       store.WorkspaceRecord
	run      domain.Run
	threadID string
	provider *tier5HTTPProvider
	turns    *application.ThreadTurnService
	api      *API
}

func newTier5HTTPFixture(t *testing.T, mode domain.RunExecutionPermissionMode, checker policy.Checker, provider *tier5HTTPProvider) *tier5HTTPFixture {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "tier5-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := store.WorkspaceRecord{ID: "ws-tier5-http", Name: "tier5-http", RootPath: root, CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if provider == nil {
		provider = &tier5HTTPProvider{}
	}
	if checker == nil {
		checker = httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
	}
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "Tier 5 HTTP R3 verification", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: ws.ID, ModelRoute: provider.Name() + "/mock-code", Interactive: true,
		Budget: domain.Budget{MaxTurns: 10, MaxToolCalls: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	if mode != "" && mode != domain.RunExecutionPermissionAsk {
		if _, err := application.NewThreadExecutionPermissionService(st, caps).Change(ctx, application.ChangeThreadExecutionPermissionRequest{
			ThreadID: threadID, Mode: string(mode), OperationKey: "tier5-http-perm-" + string(mode),
			RequestedBy: "test_operator", Reason: "tier 5 setup",
			ConfirmFull: mode == domain.RunExecutionPermissionFull,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ref := llm.ModelRef{Provider: provider.Name(), Model: "mock-code"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(provider)
	router.SetRoute("code", ref)
	lifecycle := application.NewRunLifecycleControlService(st)
	execution := application.NewRunExecutionHandoffService(st, router, checker).
		WithExecutionPermissionCapabilities(caps).
		WithGeneratedContextCompaction(false)
	turns := application.NewThreadTurnServiceWithExecutionCapabilities(st, lifecycle, execution, caps)
	api, err := New(st, Config{
		AccessToken:                       testAccessToken,
		ControlToken:                      testControlToken,
		RunControlEnabled:                 true,
		ExecutionPermissionControlEnabled: true,
		FileEditReviewEnabled:             true,
		FileEditReviewController:          application.NewFileEditReviewService(st),
		FileEditApplyEnabled:              true,
		FileEditApplyController:           application.NewFileEditApplyService(st, checker),
		RunExecutionEnabled:               true,
		RunExecutionController:            execution,
		ThreadTurnController:              turns,
		ExecutionPermissionCapabilities:   caps,
		AppVersion:                        "tier5-http-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &tier5HTTPFixture{
		st: st, root: root, ws: ws, run: run, threadID: threadID,
		provider: provider, turns: turns, api: api,
	}
}

func tier5ToolResp(calls ...llm.ToolCall) *llm.ChatResponse {
	return &llm.ChatResponse{Provider: "http-tier5", Model: "mock-code", ToolCalls: calls}
}

func tier5ActionResp(kind domain.RootActionKind, msg, summary, reason string) *llm.ChatResponse {
	raw, _ := json.Marshal(domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: kind, Message: msg, Summary: summary, Reason: reason,
	})
	return &llm.ChatResponse{Provider: "http-tier5", Model: "mock-code", Text: string(raw)}
}

func TestThroughputTier5_HTTP_WorkspaceDeleteApproveDirectApplyAndReplay(t *testing.T) {
	ctx := t.Context()
	p := &tier5HTTPProvider{}
	f := newTier5HTTPFixture(t, domain.RunExecutionPermissionAuto, httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}, p)
	target := filepath.Join(f.root, "obsolete.txt")
	if err := os.WriteFile(target, []byte("drop me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	delSHA := fileedit.HashText("drop me\n")

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			args, _ := json.Marshal(toolgateway.WorkspaceDeletePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "propose",
				Path: "obsolete.txt", ConfirmPath: "obsolete.txt", ExpectedSHA256: delSHA,
			})
			return tier5ToolResp(llm.ToolCall{ID: "del-1", Name: "workspace_delete", Arguments: args}), nil
		case 1:
			return tier5ActionResp(domain.RootActionWait, "Confirm deletion of obsolete.txt", "", "delete requires approval"), nil
		case 2:
			var all strings.Builder
			for _, m := range req.Messages {
				all.WriteString(m.Content)
			}
			for _, want := range []string{
				"System-recorded direct file apply result after operator approval",
				"APPLIED",
				"file_written=true",
			} {
				if !strings.Contains(all.String(), want) {
					return nil, fmt.Errorf("resuming model missed %q in messages: %s", want, all.String())
				}
			}
			return tier5ActionResp(domain.RootActionFinish, "Deleted obsolete.txt via direct Go apply", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}

	if _, err := f.turns.Execute(ctx, application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: f.threadID,
		Content: "Delete obsolete.txt", OperationKey: "tier5-http-del-turn-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("obsolete.txt deleted before approval: %v", err)
	}
	edits, err := f.st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 1 || edits[0].Operation != fileedit.OperationDelete || edits[0].Status != fileedit.StatusProposed {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	editID := edits[0].ID

	path := "/api/v1/runs/" + f.run.ID + "/file-edits/" + editID + "/review"
	resp := performSessionMessageRequest(t, f.api, http.MethodPost, path, testControlToken, "", "application/json",
		strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
	var env struct {
		Data FileEditReviewView `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
		t.Fatalf("status=%d body=%s err=%v", resp.Code, resp.Body.String(), err)
	}
	v := env.Data
	if resp.Code != http.StatusAccepted || v.Replayed || !v.FileWritten ||
		v.Edit.Status != fileedit.StatusApplied || v.Edit.Operation != fileedit.OperationDelete ||
		v.Continuation == nil || v.Continuation.State != "completed" ||
		!v.Continuation.ModelCalled || v.Continuation.ToolCalled {
		t.Fatalf("unexpected delete review response: status=%d view=%+v", resp.Code, v)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete.txt still on disk after direct Go apply: %v", err)
	}
	// Ensure no workspace_apply or workspace_delete(apply) tool call was emitted.
	rounds, _ := f.st.ListRunSupervisorToolRoundsPage(ctx, f.run.ID, 0, 10)
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.ToolName == "workspace_apply" || (c.ToolName == "workspace_delete" && strings.Contains(c.PayloadJSON, `"apply"`)) {
				t.Fatalf("unexpected secondary apply call: %#v", c)
			}
		}
	}

	// Replay POST .../review: must return file_written=false, status="applied", replayed=true, and no double-delete error or extra model call!
	callsBefore := len(p.Requests())
	replayResp := performSessionMessageRequest(t, f.api, http.MethodPost, path, testControlToken, "", "application/json",
		strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
	var replayEnv struct {
		Data FileEditReviewView `json:"data"`
	}
	if err := json.Unmarshal(replayResp.Body.Bytes(), &replayEnv); err != nil {
		t.Fatalf("replay status=%d body=%s err=%v", replayResp.Code, replayResp.Body.String(), err)
	}
	rv := replayEnv.Data
	if replayResp.Code != http.StatusAccepted || !rv.Replayed || rv.FileWritten ||
		rv.Edit.Status != fileedit.StatusApplied || rv.Continuation == nil ||
		!rv.Continuation.Replayed || len(p.Requests()) != callsBefore {
		t.Fatalf("unexpected delete review replay: status=%d view=%+v calls=%d/%d",
			replayResp.Code, rv, len(p.Requests()), callsBefore)
	}
}

func TestThroughputTier5_HTTP_ReviewCASConflictPreservesFailureReasonAndModelRecovers(t *testing.T) {
	ctx := t.Context()
	p := &tier5HTTPProvider{}
	f := newTier5HTTPFixture(t, domain.RunExecutionPermissionAuto, httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}, p)
	target := filepath.Join(f.root, "shared.txt")
	if err := os.WriteFile(target, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha1 := fileedit.HashText("v1\n")
	externalText := "v1\nexternal-change\n"
	sha2 := fileedit.HashText(externalText)

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			args, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "replace",
				Path: "shared.txt", ExpectedSHA256: sha1, Content: "v1-model\n",
			})
			return tier5ToolResp(llm.ToolCall{ID: "c-1", Name: "workspace_change", Arguments: args}), nil
		case 1:
			return tier5ActionResp(domain.RootActionWait, "Review shared.txt update", "", "review required"), nil
		case 2:
			// Resuming model after CAS conflict MUST receive failure evidence and NOT call workspace_apply.
			var all strings.Builder
			for _, m := range req.Messages {
				all.WriteString(m.Content)
			}
			for _, want := range []string{
				"System-recorded direct file apply result after operator approval",
				"FAILED (CONFLICT)",
				"Do not call workspace_apply",
			} {
				if !strings.Contains(all.String(), want) {
					return nil, fmt.Errorf("resuming model missed conflict evidence %q", want)
				}
			}
			args, _ := json.Marshal(toolgateway.WorkspaceReadPayload{
				Version: toolgateway.AgentCodeRegistryVersion, Path: "shared.txt", StartLine: 1, EndLine: 10,
			})
			return tier5ToolResp(llm.ToolCall{ID: "r-recover", Name: "workspace_read", Arguments: args}), nil
		case 3:
			args, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "replace",
				Path: "shared.txt", ExpectedSHA256: sha2, Content: "v1-model\nexternal-change\n",
			})
			return tier5ToolResp(llm.ToolCall{ID: "c-2", Name: "workspace_change", Arguments: args}), nil
		case 4:
			return tier5ActionResp(domain.RootActionWait, "Review merged recovery edit", "", "review required"), nil
		case 5:
			return tier5ActionResp(domain.RootActionFinish, "Recovered cleanly from CAS conflict", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}

	if _, err := f.turns.Execute(ctx, application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: f.threadID,
		Content: "Update shared.txt", OperationKey: "tier5-http-cas-turn-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	edits, err := f.st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	firstID := edits[0].ID

	// Mutate file externally before approval!
	if err := os.WriteFile(target, []byte(externalText), 0o600); err != nil {
		t.Fatal(err)
	}

	firstPath := "/api/v1/runs/" + f.run.ID + "/file-edits/" + firstID + "/review"
	resp1 := performSessionMessageRequest(t, f.api, http.MethodPost, firstPath, testControlToken, "", "application/json",
		strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
	var env1 struct {
		Data FileEditReviewView `json:"data"`
	}
	if err := json.Unmarshal(resp1.Body.Bytes(), &env1); err != nil {
		t.Fatalf("status=%d body=%s err=%v", resp1.Code, resp1.Body.String(), err)
	}
	v1 := env1.Data
	if resp1.Code != http.StatusAccepted || v1.FileWritten || v1.Edit.Status != fileedit.StatusFailed ||
		!strings.Contains(v1.Edit.Reason, "changed") || v1.Continuation == nil ||
		v1.Continuation.State != "completed" || !v1.Continuation.ToolCalled {
		t.Fatalf("CAS conflict review response=%+v", v1)
	}
	if got, _ := os.ReadFile(target); string(got) != externalText {
		t.Fatalf("CAS conflict overwrote external file: %q", string(got))
	}

	// Verify model never called workspace_apply during recovery.
	rounds, _ := f.st.ListRunSupervisorToolRoundsPage(ctx, f.run.ID, 0, 20)
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.ToolName == "workspace_apply" {
				t.Fatalf("model unexpectedly called workspace_apply on recovery: %#v", c)
			}
		}
	}

	// Replay /review on conflicted edit must preserve failed status + reason and file_written=false.
	replay1 := performSessionMessageRequest(t, f.api, http.MethodPost, firstPath, testControlToken, "", "application/json",
		strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
	var replayEnv1 struct {
		Data FileEditReviewView `json:"data"`
	}
	_ = json.Unmarshal(replay1.Body.Bytes(), &replayEnv1)
	if !replayEnv1.Data.Replayed || replayEnv1.Data.FileWritten ||
		replayEnv1.Data.Edit.Status != fileedit.StatusFailed || replayEnv1.Data.Edit.Reason == "" {
		t.Fatalf("CAS conflict replay mismatch: %+v", replayEnv1.Data)
	}

	// Approve the recovery proposal and verify it applies directly cleanly!
	edits, err = f.st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 2 {
		t.Fatalf("expected 2 edits after model recovery, got %+v err=%v", edits, err)
	}
	var secondID string
	for _, ed := range edits {
		if ed.ID != firstID {
			secondID = ed.ID
		}
	}
	secondPath := "/api/v1/runs/" + f.run.ID + "/file-edits/" + secondID + "/review"
	resp2 := performSessionMessageRequest(t, f.api, http.MethodPost, secondPath, testControlToken, "", "application/json",
		strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
	var env2 struct {
		Data FileEditReviewView `json:"data"`
	}
	_ = json.Unmarshal(resp2.Body.Bytes(), &env2)
	if !env2.Data.FileWritten || env2.Data.Edit.Status != fileedit.StatusApplied ||
		env2.Data.Continuation == nil || env2.Data.Continuation.State != "completed" {
		t.Fatalf("recovery edit review failed: %+v", env2.Data)
	}
	if got, _ := os.ReadFile(target); string(got) != "v1-model\nexternal-change\n" {
		t.Fatalf("recovered content=%q", string(got))
	}
}

func TestThroughputTier5_HTTP_ManualProposalAndNonWaitingRunReviewDoesNotApply(t *testing.T) {
	t.Run("manual_proposal_on_idle_run", func(t *testing.T) {
		ctx := t.Context()
		p := &tier5HTTPProvider{}
		f := newTier5HTTPFixture(t, domain.RunExecutionPermissionAsk, nil, p)
		target := filepath.Join(f.root, "manual.txt")
		if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index == 0 {
				return tier5ActionResp(domain.RootActionFinish, "Turn finished idle", "done", ""), nil
			}
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
		// Run finishes a turn -> RunRunning + SupervisorIdle (NOT SupervisorWaiting).
		if _, err := f.turns.Execute(ctx, application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: f.threadID,
			Content: "Say hello", OperationKey: "tier5-http-idle-turn-0001", RequestedBy: "operator",
		}); err != nil {
			t.Fatal(err)
		}
		edit, err := fileedit.NewManager(f.st).Propose(ctx, fileedit.Proposal{
			SessionID: f.run.SessionID, WorkspaceID: f.ws.ID, WorkspaceRoot: f.root,
			Path: "manual.txt", ProposedText: "manual change\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/runs/" + f.run.ID + "/file-edits/" + edit.ID + "/review"
		resp := performSessionMessageRequest(t, f.api, http.MethodPost, path, testControlToken, "", "application/json",
			strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
		var env struct {
			Data FileEditReviewView `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
			t.Fatalf("status=%d body=%s err=%v", resp.Code, resp.Body.String(), err)
		}
		v := env.Data
		if resp.Code != http.StatusAccepted || v.FileWritten || v.Edit.Status != fileedit.StatusApproved ||
			v.Continuation == nil || v.Continuation.State != "not_started" {
			t.Fatalf("manual proposal review unexpectedly applied or started continuation: %+v", v)
		}
		if got, _ := os.ReadFile(target); string(got) != "original\n" {
			t.Fatalf("manual proposal review modified disk: %q", string(got))
		}
		if len(p.Requests()) != 1 {
			t.Fatalf("manual proposal review woke model unexpectedly: calls=%d", len(p.Requests()))
		}
	})

	t.Run("model_proposal_on_non_waiting_idle_run", func(t *testing.T) {
		ctx := t.Context()
		p := &tier5HTTPProvider{}
		f := newTier5HTTPFixture(t, domain.RunExecutionPermissionAuto, httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}, p)
		target := filepath.Join(f.root, "non_wait.txt")
		if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sha := fileedit.HashText("before\n")
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch index {
			case 0:
				args, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
					Version: toolgateway.AgentCodeRegistryVersion, Action: "replace",
					Path: "non_wait.txt", ExpectedSHA256: sha, Content: "after\n",
				})
				return tier5ToolResp(llm.ToolCall{ID: "nw-1", Name: "workspace_change", Arguments: args}), nil
			case 1:
				// Model finishes instead of waiting -> checkpoint.Phase == SupervisorIdle!
				return tier5ActionResp(domain.RootActionFinish, "Proposed only, finished idle", "done", ""), nil
			default:
				return nil, fmt.Errorf("unexpected model call %d", index)
			}
		}
		if _, err := f.turns.Execute(ctx, application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: f.threadID,
			Content: "Propose without waiting", OperationKey: "tier5-http-nonwait-turn-0001", RequestedBy: "operator",
		}); err != nil {
			t.Fatal(err)
		}
		edits, err := f.st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
		if err != nil || len(edits) != 1 {
			t.Fatalf("edits=%+v err=%v", edits, err)
		}
		path := "/api/v1/runs/" + f.run.ID + "/file-edits/" + edits[0].ID + "/review"
		resp := performSessionMessageRequest(t, f.api, http.MethodPost, path, testControlToken, "", "application/json",
			strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
		var env struct {
			Data FileEditReviewView `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
			t.Fatalf("status=%d body=%s err=%v", resp.Code, resp.Body.String(), err)
		}
		v := env.Data
		if resp.Code != http.StatusAccepted || v.FileWritten || v.Edit.Status != fileedit.StatusApproved ||
			v.Continuation == nil || v.Continuation.State != "not_started" {
			t.Fatalf("non-waiting run review unexpectedly applied or started continuation: %+v", v)
		}
		if got, _ := os.ReadFile(target); string(got) != "before\n" {
			t.Fatalf("non-waiting run review modified disk: %q", string(got))
		}
	})
}

func TestThroughputTier5_HTTP_StandardCodeDirectApplyAdvancesEpochAndContinuation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := t.Context()
	sourceRoot := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", sourceRoot}, args...)...)
		hideSubprocessWindow(cmd)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	runGit("config", "user.email", "tier5-sc@example.invalid")
	runGit("config", "user.name", "Tier5 SC")
	runGit("config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(sourceRoot, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-qm", "baseline")

	st, err := store.Open(filepath.Join(t.TempDir(), "tier5-http-sc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws := store.WorkspaceRecord{ID: "ws-tier5-sc-http", Name: "tier5-sc-http", RootPath: sourceRoot, CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true,
		OperatorApprovalEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
	}
	executor, err := repository.NewRunWorktreeExecutor(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	drydocks, err := application.NewRunWorktreeService(st, executor)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := application.NewWorkspaceCheckpointService(st, caps)
	if err != nil {
		t.Fatal(err)
	}
	drydocks.WithCheckpointService(checkpoints)

	p := &tier5HTTPProvider{}
	ref := llm.ModelRef{Provider: p.Name(), Model: "mock-code"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	router.SetRoute("code", ref)

	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "Standard Code HTTP direct apply + continuation", Profile: "code", Surface: "code", Phase: "plan",
		WorkspaceID: ws.ID, ModelRoute: p.Name() + "/mock-code", Interactive: true,
		Budget: domain.Budget{MaxTurns: 12, MaxToolCalls: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := application.CapabilityReadinessRuntime{
		RunControlEnabled: true, RunExecutionEnabled: true, ExecutionPermissionControlEnabled: true, StandardCodePresetEnabled: true,
		ExecutionPermissionCapabilities: caps,
		LocalSandboxInstalled: true, LocalSandboxProven: true, LocalBackendReady: true,
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{commandruntimeadapter.SandboxedWorkspace(
			application.CommandRuntimeLocalSandboxBackend, "tier5-sc-http", "tier5-sc-http-gen")},
	}
	preset, err := application.NewStandardCodePresetService(st, drydocks, runtime)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := preset.Configure(ctx, application.ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: run.ID, Action: "configure",
		BackendIntent: "local", OperationKey: "tier5-sc-http-preview", RequestedBy: "operator",
	})
	if err != nil || !preview.TrustRequired {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := preset.Configure(ctx, application.ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: run.ID, Action: "configure",
		BackendIntent: "local", OperationKey: "tier5-sc-http-cfg", RequestedBy: "operator",
		ConfirmWorkspaceTrust: true, ExpectedTrustDigest: preview.TrustDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	checker := httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}
	lifecycle := application.NewRunLifecycleControlService(st)
	execution := application.NewRunExecutionHandoffService(st, router, checker).
		WithRunWorktree(drydocks).
		WithExecutionPermissionCapabilities(caps).
		WithGeneratedContextCompaction(false)
	turns := application.NewThreadTurnServiceWithExecutionCapabilities(st, lifecycle, execution, caps)
	api, err := New(st, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		FileWorkspaceDrydocks: drydocks,
		FileEditReviewEnabled: true, FileEditReviewController: application.NewFileEditReviewService(st).WithRunWorktree(drydocks),
		RunExecutionEnabled: true, RunExecutionController: execution, ThreadTurnController: turns,
		AppVersion: "tier5-http-sc-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	plan := `{"version":"plan_delivery.v1","directions":[
 {"title":"Patch tracked","summary":"Patch tracked.txt","tradeoffs":["Small"],"modules":[{"title":"Patch","objective":"Patch tracked","acceptance_criteria":["Updated"],"dependencies":[]}]},
 {"title":"Option B","summary":"Alt","tradeoffs":["Alt"],"modules":[{"title":"Alt","objective":"Alt","acceptance_criteria":["Alt"],"dependencies":[]}]},
 {"title":"Option C","summary":"Alt","tradeoffs":["Alt"],"modules":[{"title":"Alt","objective":"Alt","acceptance_criteria":["Alt"],"dependencies":[]}]}]}`
	readArgs, _ := json.Marshal(toolgateway.WorkspaceReadPayload{Version: toolgateway.AgentCodeRegistryVersion, Path: "tracked.txt", StartLine: 1, EndLine: 2})
	patchArgs, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "tracked.txt",
		ExpectedSHA256: fileedit.HashText("base\n"),
		Replacements:   []toolgateway.WorkspaceReplacement{{OldText: "base", NewText: "http-sc-applied", ExpectedOccurrences: 1}},
	})

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return tier5ToolResp(llm.ToolCall{ID: "r1", Name: "workspace_read", Arguments: readArgs}), nil
		case 1:
			return tier5ToolResp(llm.ToolCall{ID: "r2", Name: "workspace_read", Arguments: readArgs}), nil
		case 2:
			return tier5ToolResp(llm.ToolCall{ID: "p1", Name: "plan_delivery_propose", Arguments: json.RawMessage(plan)}), nil
		case 3:
			return tier5ActionResp(domain.RootActionWait, "Select direction", "", "plan selection"), nil
		case 4:
			return tier5ToolResp(llm.ToolCall{ID: "wc1", Name: "workspace_change", Arguments: patchArgs}), nil
		case 5:
			return tier5ActionResp(domain.RootActionWait, "Approve patch", "", "operator review"), nil
		case 6:
			return tier5ActionResp(domain.RootActionWait, "Applied directly via HTTP", "", "ready to verify"), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}

	threadID := domain.InitialThreadID(run.ID)
	if _, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID,
		Content: "Inspect and plan", OperationKey: "tier5-sc-http-plan", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	proposals, err := st.ListPlanDeliveryProposals(ctx, run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
	planCtl := application.NewPlanDeliveryControlService(st)
	if _, err := planCtl.SelectDirection(ctx, application.ControlPlanDirectionRequest{
		Version: application.PlanDeliveryControlProtocolVersion, RunID: run.ID, ProposalID: proposals[0].ID,
		Direction: 1, OperationKey: "tier5-sc-http-sel", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := planCtl.EnterDelivery(ctx, application.ControlPlanDeliveryTransitionRequest{
		Version: application.PlanDeliveryControlProtocolVersion, RunID: run.ID,
		OperationKey: "tier5-sc-http-del", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID,
		Content: "Propose patch", OperationKey: "tier5-sc-http-deliver", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}

	edits, err := st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	reviewPath := "/api/v1/runs/" + run.ID + "/file-edits/" + edits[0].ID + "/review"
	resp := performSessionMessageRequest(t, api, http.MethodPost, reviewPath, testControlToken, "", "application/json",
		strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
	var env struct {
		Data FileEditReviewView `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
		t.Fatalf("status=%d body=%s err=%v", resp.Code, resp.Body.String(), err)
	}
	if resp.Code != http.StatusAccepted || !env.Data.FileWritten || env.Data.Edit.Status != fileedit.StatusApplied ||
		env.Data.Continuation == nil || env.Data.Continuation.State != "completed" {
		t.Fatalf("StandardCode HTTP direct apply failed: %+v", env.Data)
	}
	snap, found, err := st.GetStandardCodeSupervisorSnapshot(ctx, run.ID)
	if err != nil || !found || snap.MutationEpoch != 1 || snap.State != domain.StandardCodeSupervisorExecute {
		t.Fatalf("StandardCode supervisor did not advance MutationEpoch via HTTP review: found=%t snap=%+v err=%v", found, snap, err)
	}
	owned, found, err := st.GetDrydockByRun(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("drydock missing: %t %v", found, err)
	}
	if got, _ := os.ReadFile(filepath.Join(owned.Path, "tracked.txt")); string(got) != "http-sc-applied\n" {
		t.Fatalf("drydock file=%q", string(got))
	}

	// Cancel Run 1 and submit a successor Run 2 -> GetStandardCodeContinuation must succeed!
	if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	nextRun, err := application.NewThreadServiceWithExecutionCapabilities(st, caps).
		WithRunWorktree(drydocks).
		Submit(ctx, application.SubmitThreadMessageRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID,
			Content: "Verify applied patch", OperationKey: "tier5-sc-http-succ", RequestedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := st.GetStandardCodeContinuation(ctx, nextRun.Run.ID)
	if err != nil || cont == nil || cont.MutationEpoch != 1 || cont.MutationRunID != run.ID || cont.MutationCheckpointID == "" {
		t.Fatalf("GetStandardCodeContinuation failed after HTTP direct apply: cont=%+v err=%v", cont, err)
	}
}
