package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
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
	mu       sync.Mutex
	requests []llm.ChatRequest
}

// recordChat keeps the model-visible context of every request so tests can
// assert that fixed instructions were actually delivered to the model.
func (p *execTestProvider) recordChat(_ context.Context,
	request llm.ChatRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request.Tools = append([]llm.ToolSpec(nil), request.Tools...)
	request.Messages = append([]llm.Message(nil), request.Messages...)
	p.requests = append(p.requests, request)
}

func (p *execTestProvider) deliveredText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var visible strings.Builder
	for _, request := range p.requests {
		for _, message := range request.Messages {
			visible.WriteString(message.Role)
			visible.WriteString("\n")
			visible.WriteString(message.Content)
			visible.WriteString("\n")
		}
	}
	return visible.String()
}

func (p *execTestProvider) Name() string { return "exec-test-provider" }
func (p *execTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model"}}, nil
}
func (p *execTestProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.recordChat(ctx, request)
	return &llm.ChatResponse{
		Text:     p.response,
		Provider: p.Name(),
		Model:    "model",
		Usage:    llm.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
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

func TestNormalizeExecInstructions(t *testing.T) {
	long := strings.Repeat("说", domain.MaxThreadContextPinRunes+1)
	for _, item := range []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "unset flag", raw: "", want: ""},
		{name: "trims surrounding space", raw: "  Always answer in JSON.  ", want: "Always answer in JSON."},
		{name: "whitespace only fails loudly", raw: "   ", wantErr: "requires nonempty text"},
		{name: "rune cap mirrors context pins", raw: long, wantErr: "context pin text exceeds"},
		{name: "NUL rejected", raw: "answer\x00now", wantErr: "context pin text"},
		{name: "protected secrets rejected", raw: "use key sk-abcdef1234567890abcdef", wantErr: "protected secrets"},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, err := normalizeExecInstructions(item.raw)
			if item.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), item.wantErr) {
					t.Fatalf("normalizeExecInstructions(%q) error=%v, want containing %q", item.raw, err, item.wantErr)
				}
				return
			}
			if err != nil || got != item.want {
				t.Fatalf("normalizeExecInstructions(%q) = (%q, %v), want %q", item.raw, got, err, item.want)
			}
		})
	}
}

func newExecTestApp(t *testing.T) (*App, *execTestProvider, *store.SQLiteStore) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-test.db"))
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
	encoded, err := json.Marshal(domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: "Task complete under the fixed instructions.", Summary: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &execTestProvider{response: string(encoded)}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a := &App{home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, store: st,
		router: router, checker: policy.NewDefaultChecker()}
	return a, provider, st
}

func TestExecInstructionsFixesThreadContextPinAndDeliversIt(t *testing.T) {
	a, provider, st := newExecTestApp(t)
	var out bytes.Buffer
	a.out = &out
	const fixed = "Always answer in JSON."

	err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--instructions", fixed, "--json", "Summarize the workspace README"})
	if err != nil {
		t.Fatalf("exec with fixed instructions failed: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse exec JSON output: %v, raw: %s", err, out.String())
	}
	runID := fmt.Sprint(result["run_id"])
	if runID == "" {
		t.Fatalf("run_id missing from exec JSON output: %s", out.String())
	}

	state, err := st.ThreadContextPinsForRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Pins) != 1 {
		t.Fatalf("expected exactly one fixed context pin, got %d", len(state.Pins))
	}
	pin := state.Pins[0]
	if pin.Status != "active" || pin.Text != fixed || pin.CreatedBy != "cli_exec_operator" ||
		pin.SourceKind != "operator_input" {
		t.Fatalf("fixed pin mismatch: status=%s created_by=%s source=%s text=%q",
			pin.Status, pin.CreatedBy, pin.SourceKind, pin.Text)
	}
	if pin.ContentSHA256 == "" || pin.Version < 1 {
		t.Fatalf("fixed pin integrity fields missing: version=%d sha=%q", pin.Version, pin.ContentSHA256)
	}

	// The pin text must reach the model context, not only the durable state.
	if !strings.Contains(provider.deliveredText(), fixed) {
		t.Fatal("fixed instructions were never delivered to the model context")
	}
}

func TestExecInstructionsWhitespaceOnlyFailsBeforeRunCreation(t *testing.T) {
	a, provider, _ := newExecTestApp(t)
	err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--instructions", "   ", "Any task"})
	if err == nil || !strings.Contains(err.Error(), "requires nonempty text") {
		t.Fatalf("whitespace-only --instructions must fail loudly, got: %v", err)
	}
}

// cancellingExecProvider blocks inside the model call until the invocation
// context is cancelled, mimicking an interruption arriving mid-turn. Both
// model entry points block: the turn machinery streams by default, and the
// promoted StreamChat of the embedded fixture would otherwise dispatch to the
// embedded fixture's own immediate response.
type cancellingExecProvider struct {
	execTestProvider
	started     chan struct{}
	arriveConce sync.Once
}

// arrive reports the first model entry exactly once, even if the machinery
// ever retries or races the two entry points.
func (p *cancellingExecProvider) arrive() {
	p.arriveConce.Do(func() { close(p.started) })
}

func (p *cancellingExecProvider) Chat(ctx context.Context,
	request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.arrive()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *cancellingExecProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	p.arrive()
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestExecCancellationKeepsRunRecoverable(t *testing.T) {
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
	provider := &cancellingExecProvider{started: make(chan struct{})}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a := &App{home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, store: st,
		router: router, checker: policy.NewDefaultChecker()}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.execCommand(ctx, []string{
			"--workspace", "exec-ws", "--route", provider.Name() + "/model",
			"Long running task"})
	}()

	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("exec never reached the model call")
	}
	cancel()
	var execErr error
	select {
	case execErr = <-errCh:
	case <-time.After(60 * time.Second):
		t.Fatal("exec did not return after cancellation")
	}
	if execErr == nil || apperror.CodeOf(execErr) != apperror.CodeCancelled {
		t.Fatalf("cancelled exec must surface the cancelled code, got: %v", execErr)
	}

	// The durable Run is recoverable, not orphaned: the handoff failure was
	// recorded through a detached context and the Run keeps its lifecycle.
	runs, err := st.ListRuns(t.Context(), domain.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly one Run after cancellation, got %d", len(runs))
	}
	if runs[0].Terminal() {
		t.Fatalf("cancelled Run must stay recoverable, got status %s", runs[0].Status)
	}
}

// execFinishResponse builds a supervisor finish response with the given
// public reply text.
func execFinishResponse(message string) string {
	encoded, err := json.Marshal(domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: message, Summary: "Done"})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func newExecLastMessageTestApp(t *testing.T) (*App, *execTestProvider, string) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-last-message-test.db"))
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
	return a, provider, home
}

func TestExecOutputLastMessageWritesReply(t *testing.T) {
	a, provider, _ := newExecLastMessageTestApp(t)
	target := filepath.Join(t.TempDir(), "last-message.txt")

	var out bytes.Buffer
	a.out = &out
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--output-last-message", target, "--json", "Any task"}); err != nil {
		t.Fatalf("exec with --output-last-message failed: %v", err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "first reply" {
		t.Fatalf("last-message file mismatch: %q", string(written))
	}
	// The standard output contract is unchanged: --json still carries the
	// reply alongside the file.
	var result struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse exec JSON: %v, raw: %s", err, out.String())
	}
	if result.Reply != "first reply" {
		t.Fatalf("stdout reply mismatch: %+v", result)
	}
}

func TestExecOutputLastMessageRejectsSurroundingWhitespaceBeforeExecution(t *testing.T) {
	a, provider, _ := newExecLastMessageTestApp(t)
	target := filepath.Join(t.TempDir(), "reply.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{" " + target, target + " ", "\t" + target, " "} {
		err := a.execCommand(t.Context(), []string{
			"--workspace", "exec-ws", "--route", provider.Name() + "/model",
			"--output-last-message", requested, "Any task"})
		if err == nil || !strings.Contains(err.Error(), "surrounding whitespace") {
			t.Fatalf("output path %q must fail before execution, got %v", requested, err)
		}
	}
	written, err := os.ReadFile(target)
	if err != nil || string(written) != "original" {
		t.Fatalf("existing output was changed: %q, error=%v", written, err)
	}
	if delivered := provider.deliveredText(); delivered != "" {
		t.Fatal("invalid output path executed the model")
	}
}

func TestExecOutputLastMessageWriteFailureFailsBeforeOutput(t *testing.T) {
	a, provider, _ := newExecLastMessageTestApp(t)
	missingDir := filepath.Join(t.TempDir(), "does-not-exist", "last-message.txt")

	var out, errOut bytes.Buffer
	a.out = &out
	a.errOut = &errOut
	err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--output-last-message", missingDir, "Any task"})
	if err == nil || !strings.Contains(err.Error(), "--output-last-message") {
		t.Fatalf("failed write must fail the command with the flag named, got: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no reply may reach stdout when the write failed, got: %s", out.String())
	}
}

func TestExecOutputLastMessageRecordsPauseReply(t *testing.T) {
	a, provider, _ := newExecLastMessageTestApp(t)
	// A continue action never reports finish, so the turn pauses with no
	// committed reply for this invocation.
	provider.response = `{"version":"root_lifecycle.v1","action":"continue","message":"Incomplete task message"}`
	target := filepath.Join(t.TempDir(), "last-message.txt")

	var out bytes.Buffer
	a.out = &out
	err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--max-turns", "1", "--output-last-message", target, "Any task"})
	if err == nil || !strings.Contains(err.Error(), "exec turn did not finish") {
		t.Fatalf("paused turn must exit non-zero, got: %v", err)
	}
	written, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("paused turn must still record the file: %v", readErr)
	}
	// The wait action commits its public message as the reply before the
	// pause, so the file records that committed text — the same text stdout
	// would carry.
	if string(written) != "Incomplete task message" {
		t.Fatalf("paused turn must record its committed reply, got %q", string(written))
	}
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

func TestExecJSONOutputIncludesUsage(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID: "exec-usage-ws", Name: "exec-usage-ws",
		RootPath:  t.TempDir(),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: "Task completed successfully.", Summary: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &execTestProvider{response: string(encoded)}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)

	var out, errOut bytes.Buffer
	a := &App{home: home, out: &out, errOut: &errOut, store: st, router: router,
		checker: policy.NewDefaultChecker()}

	execErr := a.execCommand(t.Context(), []string{
		"--workspace", "exec-usage-ws",
		"--route", provider.Name() + "/model",
		"--json",
		"Complete the task",
	})
	if execErr != nil {
		t.Fatalf("exec with --json failed: %v", execErr)
	}
	_ = provider

	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v, raw: %s", err, out.String())
	}

	usage, ok := result["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage object missing from JSON output: %v", result)
	}
	if usage["total_tokens"] == nil {
		t.Fatal("usage.total_tokens is missing")
	}
	if usage["duration_ms"] == nil {
		t.Fatal("usage.duration_ms is missing")
	}
	if usage["turns"] == nil {
		t.Fatal("usage.turns is missing")
	}
	if result["reply"] != "Task completed successfully." {
		t.Fatalf("reply mismatch: %v", result["reply"])
	}
	if result["stop_reason"] != "turn_finish" {
		t.Fatalf("a finished turn must report turn_finish, got: %v", result["stop_reason"])
	}
}
