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
