package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// attachRecordingProvider records every model request so the attachment
// delivery can be asserted on the actual model input. It is deliberately a
// separate type from execTestProvider: the sibling exec branches extend that
// fixture independently, and keeping this recording local avoids stacked
// merge conflicts.
type attachRecordingProvider struct {
	execTestProvider
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (p *attachRecordingProvider) record(ctx context.Context, request llm.ChatRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request.Messages = append([]llm.Message(nil), request.Messages...)
	p.requests = append(p.requests, request)
}

func (p *attachRecordingProvider) deliveredText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var visible strings.Builder
	for _, request := range p.requests {
		for _, message := range request.Messages {
			visible.WriteString(message.Content)
			visible.WriteString("\n")
		}
	}
	return visible.String()
}

func (p *attachRecordingProvider) Chat(ctx context.Context,
	request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.record(ctx, request)
	return p.execTestProvider.Chat(ctx, request)
}

func (p *attachRecordingProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	p.record(ctx, request)
	return p.execTestProvider.StreamChat(ctx, request)
}

func TestExecAttachDeliversAttachmentToModelContext(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-attach-test.db"))
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
	logFile := filepath.Join(t.TempDir(), "build-output.txt")
	const logBody = "FAILED: TestParserHandlesUnicode expected 42 got 41"
	if err := os.WriteFile(logFile, []byte(logBody), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &attachRecordingProvider{execTestProvider: execTestProvider{
		response: execFinishResponse("attachment reply")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a := &App{home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, store: st,
		router: router, checker: policy.NewDefaultChecker()}

	var out bytes.Buffer
	a.out = &out
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--attach", logFile, "--json", "Diagnose the attached build log"}); err != nil {
		t.Fatalf("exec with attachment failed: %v", err)
	}
	var result struct {
		RunID string `json:"run_id"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse exec JSON: %v, raw: %s", err, out.String())
	}
	if result.Reply != "attachment reply" {
		t.Fatalf("unexpected reply: %+v", result)
	}
	// The attachment's readable text must reach the model context as
	// untrusted evidence, not only sit in workspace storage.
	if !strings.Contains(provider.deliveredText(), logBody) {
		t.Fatal("attached file content was never delivered to the model context")
	}
}

func TestExecAttachContinueResolvesWorkspaceFromRun(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-attach-cont-test.db"))
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
	provider := &attachRecordingProvider{execTestProvider: execTestProvider{
		response: execFinishResponse("first reply")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a := &App{home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, store: st,
		router: router, checker: policy.NewDefaultChecker()}

	var first, second bytes.Buffer
	a.out = &first
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--json", "First task"}); err != nil {
		t.Fatalf("fresh exec failed: %v", err)
	}
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(first.Bytes(), &created); err != nil {
		t.Fatalf("failed to parse fresh exec JSON: %v", err)
	}

	notesFile := filepath.Join(t.TempDir(), "notes.md")
	const notesBody = "Retry matrix: run the parser suite three times before concluding flakiness."
	if err := os.WriteFile(notesFile, []byte(notesBody), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.response = execFinishResponse("second reply")
	a.out = &second
	if err := a.execCommand(t.Context(), []string{
		"--continue", created.ThreadID, "--route", provider.Name() + "/model",
		"--attach", notesFile, "--json", "Second task with notes attached"}); err != nil {
		t.Fatalf("continue exec with attachment failed: %v", err)
	}
	if !strings.Contains(provider.deliveredText(), notesBody) {
		t.Fatal("attachment on a continued Thread was never delivered to the model context")
	}
}

func TestExecAttachRejectsBadInput(t *testing.T) {
	a, provider, _ := newExecContinueTestApp(t)
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--attach", filepath.Join(t.TempDir(), "missing.txt"), "Any task"}); err == nil ||
		!strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("missing attachment must fail with the path named, got: %v", err)
	}
	tooMany := []string{"--workspace", "exec-ws", "--route", provider.Name() + "/model", "Any task"}
	for i := 0; i < domain.MaxThreadMessageAttachments+1; i++ {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		tooMany = append(tooMany, "--attach", path)
	}
	err := a.execCommand(t.Context(), tooMany)
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("more than the attachment cap must fail loudly, got: %v", err)
	}

	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--attach", "", "Any task"}); err == nil ||
		!strings.Contains(err.Error(), "requires a file path") {
		t.Fatalf("empty --attach value must fail loudly, got: %v", err)
	}

	oversized := filepath.Join(t.TempDir(), "huge.bin")
	if err := os.WriteFile(oversized, make([]byte, 5*1024*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}
	err = a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--attach", oversized, "Any task"})
	if err == nil || !strings.Contains(err.Error(), "5 MiB") {
		t.Fatalf("oversized attachment must fail with the size cap, got: %v", err)
	}
}

// imageRecordingProvider records model requests and claims vision support,
// so the image evidence delivery can be asserted end to end.
type imageRecordingProvider struct {
	execTestProvider
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (p *imageRecordingProvider) record(ctx context.Context, request llm.ChatRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request.Messages = append([]llm.Message(nil), request.Messages...)
	p.requests = append(p.requests, request)
}

func (p *imageRecordingProvider) imageParts() []llm.ImagePart {
	p.mu.Lock()
	defer p.mu.Unlock()
	var parts []llm.ImagePart
	for _, request := range p.requests {
		for _, message := range request.Messages {
			parts = append(parts, message.Images...)
		}
	}
	return parts
}

func (p *imageRecordingProvider) SupportsVision(string) bool { return true }

// DescribeVision satisfies the VisionDescriber contract: protocol support
// alone is not evidence of model vision support, so the capability check
// consults this exact-provenance describer rather than SupportsVision.
func (p *imageRecordingProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: llm.VisionSupported, Source: "provider_metadata"}
}

func (p *imageRecordingProvider) Chat(ctx context.Context,
	request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.record(ctx, request)
	return p.execTestProvider.Chat(ctx, request)
}

func (p *imageRecordingProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	p.record(ctx, request)
	return p.execTestProvider.StreamChat(ctx, request)
}

func writeTinyPNG(t *testing.T) ([]byte, string) {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			picture.Set(x, y, color.RGBA{R: uint8(x * 31), G: uint8(y * 31), B: 200, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes(), "image/png"
}

func newExecImageTestApp(t *testing.T) (*App, *imageRecordingProvider, *store.SQLiteStore) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "exec-image-test.db"))
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
	provider := &imageRecordingProvider{execTestProvider: execTestProvider{
		response: execFinishResponse("image reply")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a := &App{home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, store: st,
		router: router, checker: policy.NewDefaultChecker()}
	return a, provider, st
}

func TestExecImageDeliversBytesToVisionModel(t *testing.T) {
	a, provider, _ := newExecImageTestApp(t)
	pngBytes, _ := writeTinyPNG(t)
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(imagePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	a.out = &out
	if err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--image", imagePath, "--json", "Read the diagram and describe it"}); err != nil {
		t.Fatalf("exec with image failed: %v", err)
	}
	var result struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse exec JSON: %v, raw: %s", err, out.String())
	}
	if result.Reply != "image reply" {
		t.Fatalf("unexpected reply: %+v", result)
	}
	digest := sha256.Sum256(pngBytes)
	found := false
	for _, part := range provider.imageParts() {
		if part.SHA256 == hex.EncodeToString(digest[:]) && bytes.Equal(part.Data, pngBytes) &&
			part.MediaType == "image/png" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the attached image bytes were never delivered to the vision model")
	}
}

func TestExecImageRejectsUnsupportedFormat(t *testing.T) {
	a, provider, _ := newExecImageTestApp(t)
	plain := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(plain, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--image", plain, "Any task"})
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("non-image attachment must fail with a clear format error, got: %v", err)
	}
}

func TestExecImageRequiresVisionCapability(t *testing.T) {
	// The recording provider does NOT implement DescribeVision, so the
	// machinery classifies the model as vision-unsupported; the recording
	// also turns "refused before any model call" into an enforced assertion.
	provider := &attachRecordingProvider{execTestProvider: execTestProvider{
		response: execFinishResponse("should never be reached")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	a, _, _ := newExecContinueTestApp(t)
	a.router = router
	pngBytes, _ := writeTinyPNG(t)
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(imagePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	err := a.execCommand(t.Context(), []string{
		"--workspace", "exec-ws", "--route", provider.Name() + "/model",
		"--image", imagePath, "Any task"})
	if err == nil || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("image without a vision-capable model must fail loudly, got: %v", err)
	}
	provider.mu.Lock()
	calls := len(provider.requests)
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("the vision gate must refuse before any model call, got %d", calls)
	}
}
