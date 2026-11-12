package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
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
