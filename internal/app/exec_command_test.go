package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func TestClipExecGoalPreservesUTF8Boundaries(t *testing.T) {
	prompt := strings.Repeat("长", 3000) // 9000 bytes of multi-byte content
	clipped := clipExecGoal(prompt)
	if len(clipped) > domain.MaxRunCreationGoalBytes {
		t.Fatalf("clipped goal exceeded the limit: %d", len(clipped))
	}
	if !utf8.ValidString(clipped) {
		t.Fatal("clipped goal is not valid UTF-8")
	}
	if !strings.HasPrefix(prompt, clipped) {
		t.Fatal("clipped goal is not a prefix of the prompt")
	}
	short := clipExecGoal("short prompt")
	if short != "short prompt" {
		t.Fatalf("short prompt was modified: %q", short)
	}
}

type execTestProvider struct {
	response string
}

func (p *execTestProvider) Name() string { return "exec-test-provider" }
func (p *execTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model"}}, nil
}
func (p *execTestProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Text:     p.response,
		Provider: p.Name(),
		Model:    "model",
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}
func (p *execTestProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 2)
	ch <- llm.ChatChunk{Text: resp.Text}
	ch <- llm.FinalChatChunk(resp)
	close(ch)
	return ch, nil
}
func (p *execTestProvider) SupportsTools(string) bool    { return false }
func (p *execTestProvider) SupportsVision(string) bool   { return false }
func (p *execTestProvider) SupportsJSONMode(string) bool { return true }

func TestExecJSONOutputWithUnfinishedTurnExitsNonZero(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID:        "exec-ws",
		Name:      "exec-ws",
		RootPath:  t.TempDir(),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(domain.RootAction{
		Version: domain.RootLifecycleVersion,
		Kind:    domain.RootActionContinue,
		Message: "Incomplete task message",
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := &execTestProvider{response: string(encoded)}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)

	var out, errOut bytes.Buffer
	checker := policy.NewDefaultChecker()
	a := &App{
		home:    home,
		out:     &out,
		errOut:  &errOut,
		store:   st,
		router:  router,
		checker: checker,
	}

	// Run exec with --json and --max-turns 1 so the supervisor stops with root_wait or pause
	err = a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws",
		"--route", provider.Name() + "/model",
		"--max-turns", "1",
		"--json",
		"Do something incomplete",
	})
	if err == nil {
		t.Fatal("execCommand with unfinished turn returned nil error for --json mode")
	}

	if !strings.Contains(err.Error(), "exec turn did not finish") {
		t.Fatalf("expected 'exec turn did not finish' error, got: %v", err)
	}

	// Ensure JSON was still emitted to output
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse emitted JSON output: %v, raw output: %s", err, out.String())
	}
	if result["stop_reason"] == "turn_finish" {
		t.Fatalf("stop_reason should not be turn_finish, got: %v", result["stop_reason"])
	}
	if fmt.Sprint(result["reply"]) != "Incomplete task message" {
		t.Fatalf("expected reply 'Incomplete task message', got: %v", result["reply"])
	}
}

// execFinishResponse builds a supervisor finish response whose public reply
// text is unique per call, so a follow-up turn's reply can be distinguished
// from the first turn's.
func execFinishResponse(message string) string {
	encoded, err := json.Marshal(domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: message, Summary: "Done"})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func newExecContinueTestApp(t *testing.T) (*App, *execTestProvider, *store.SQLiteStore) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-continue-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID:        "exec-ws",
		Name:      "exec-ws",
		RootPath:  t.TempDir(),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	provider := &execTestProvider{response: execFinishResponse("first reply")}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a := &App{home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, store: st,
		router: router, checker: policy.NewDefaultChecker()}
	return a, provider, st
}

func TestExecContinueDeliversFollowUpTurnOnRunningRun(t *testing.T) {
	a, provider, st := newExecContinueTestApp(t)

	var first, second bytes.Buffer
	a.out = &first
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--json", "First task"}); err != nil {
		t.Fatalf("fresh exec failed: %v", err)
	}
	var created struct {
		RunID    string `json:"run_id"`
		ThreadID string `json:"thread_id"`
		Reply    string `json:"reply"`
	}
	if err := json.Unmarshal(first.Bytes(), &created); err != nil {
		t.Fatalf("failed to parse fresh exec JSON: %v, raw: %s", err, first.String())
	}
	if created.ThreadID == "" || created.Reply != "first reply" {
		t.Fatalf("fresh exec output mismatch: %+v", created)
	}

	provider.response = execFinishResponse("second reply")
	a.out = &second
	if err := a.execCommand(t.Context(), []string{
		"--continue", created.ThreadID, "--route", provider.Name() + "/model",
		"--json", "Second task"}); err != nil {
		t.Fatalf("continue exec failed: %v", err)
	}
	var continued struct {
		RunID string `json:"run_id"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(second.Bytes(), &continued); err != nil {
		t.Fatalf("failed to parse continue JSON: %v, raw: %s", err, second.String())
	}
	if continued.Reply != "second reply" {
		t.Fatalf("continue reply mismatch: %+v", continued)
	}
	if continued.RunID != created.RunID {
		t.Fatalf("follow-up turn on a running Run must reuse the Run: got successor %s",
			continued.RunID)
	}
	runs, err := st.ListRuns(t.Context(), domain.RunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected exactly one Run after steering-style continue, got %d err=%v",
			len(runs), err)
	}
}

func TestExecContinueCreatesSuccessorForTerminalRun(t *testing.T) {
	a, provider, st := newExecContinueTestApp(t)

	var first, second bytes.Buffer
	a.out = &first
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--json", "First task"}); err != nil {
		t.Fatalf("fresh exec failed: %v", err)
	}
	var created struct {
		RunID    string `json:"run_id"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(first.Bytes(), &created); err != nil {
		t.Fatalf("failed to parse fresh exec JSON: %v", err)
	}
	if _, err := application.NewRunService(st).Cancel(t.Context(), created.RunID); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}

	provider.response = execFinishResponse("successor reply")
	a.out = &second
	if err := a.execCommand(t.Context(), []string{
		"--continue", created.ThreadID, "--route", provider.Name() + "/model",
		"--json", "Second task"}); err != nil {
		t.Fatalf("continue exec on a terminal Run failed: %v", err)
	}
	var continued struct {
		RunID string `json:"run_id"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(second.Bytes(), &continued); err != nil {
		t.Fatalf("failed to parse continue JSON: %v, raw: %s", err, second.String())
	}
	if continued.Reply != "successor reply" || continued.RunID == created.RunID {
		t.Fatalf("terminal continue must create a successor Run: run=%s reply=%q",
			continued.RunID, continued.Reply)
	}
}

func TestExecContinueUnknownThreadFailsClearly(t *testing.T) {
	a, provider, _ := newExecContinueTestApp(t)
	err := a.execCommand(t.Context(), []string{
		"--continue", "thread-does-not-exist", "--route", provider.Name() + "/model",
		"Any task"})
	if err == nil {
		t.Fatal("continuing an unknown Thread must fail")
	}
	message := err.Error()
	if !strings.Contains(message, "not found") {
		t.Fatalf("unknown-thread error must be a clear not-found, got: %v", err)
	}
	if strings.Contains(message, "sql: no rows") {
		t.Fatalf("raw SQL error leaked to the operator: %v", err)
	}
}

func TestExecContinueArchivedThreadFailsClearly(t *testing.T) {
	a, provider, st := newExecContinueTestApp(t)

	var first bytes.Buffer
	a.out = &first
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--json", "First task"}); err != nil {
		t.Fatalf("fresh exec failed: %v", err)
	}
	var created struct {
		RunID    string `json:"run_id"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(first.Bytes(), &created); err != nil {
		t.Fatalf("failed to parse fresh exec JSON: %v", err)
	}

	// Archive the Thread via the same store the exec used.
	threadRecord, threadErr := st.GetThread(t.Context(), created.ThreadID)
	if threadErr != nil {
		t.Fatal(threadErr)
	}
	_, transitionErr := application.NewThreadService(st).Transition(t.Context(), created.ThreadID,
		domain.ThreadLifecycleAction("archive"), threadRecord.Version, "test_operator", "test-archive-0001")
	if transitionErr != nil {
		t.Fatalf("archive failed: %v", transitionErr)
	}

	continueErr := a.execCommand(t.Context(), []string{
		"--continue", created.ThreadID, "--route", provider.Name() + "/model",
		"Any task"})
	if continueErr == nil {
		t.Fatal("continuing an archived Thread must fail")
	}
	if !strings.Contains(continueErr.Error(), "archived") {
		t.Fatalf("archived Thread error must mention the status, got: %v", continueErr)
	}
	if strings.Contains(continueErr.Error(), "sql: no rows") {
		t.Fatalf("raw SQL error leaked: %v", continueErr)
	}
}
