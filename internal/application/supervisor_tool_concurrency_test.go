package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type concTestProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	respond  func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
}

func (p *concTestProvider) Name() string { return "conc-test-provider" }

func (p *concTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: p.Name(), Capabilities: []string{"chat", "tools"}}}, nil
}

func (p *concTestProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	idx := len(p.requests) - 1
	respond := p.respond
	p.mu.Unlock()
	if respond == nil {
		return nil, errors.New("concTestProvider missing respond func")
	}
	return respond(ctx, request, idx)
}

func (p *concTestProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	resp, err := p.Chat(ctx, request)
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

func (*concTestProvider) SupportsTools(string) bool                  { return true }
func (*concTestProvider) SupportsVision(string) bool                 { return false }
func (*concTestProvider) SupportsJSONMode(string) bool               { return true }
func (*concTestProvider) RequiresPrivateAssistantHistory(string) bool { return false }

type concTestChecker struct {
	base           policy.Checker
	barrierTarget  int
	barrierTimeout time.Duration
	onEnter        func(tools.Call)
	holdIf         func(tools.Call) <-chan struct{}
	delayIf        func(tools.Call) time.Duration
	exclusiveSet   map[string]bool

	mu                  sync.Mutex
	inFlight            int
	maxInFlight         int
	barrierArrived      int
	barrierReady        chan struct{}
	exclusiveOverlapErr string
}

func newConcTestChecker(barrierTarget int) *concTestChecker {
	c := &concTestChecker{
		base:           policy.NewDefaultChecker(),
		barrierTarget:  barrierTarget,
		barrierTimeout: 400 * time.Millisecond,
		exclusiveSet: map[string]bool{
			"work_item_create":    true,
			"note_create":         true,
			"thread_send_message": true,
			"skill_read":          true,
			"workspace_change":    true,
			"workspace_apply":     true,
			"workspace_delete":    true,
		},
	}
	if barrierTarget > 1 {
		c.barrierReady = make(chan struct{})
	}
	return c
}

func (c *concTestChecker) CheckText(ctx, text string) policy.Decision {
	return c.base.CheckText(ctx, text)
}

func (c *concTestChecker) CheckToolCall(call tools.Call) policy.Decision {
	if c.onEnter != nil {
		c.onEnter(call)
	}
	if c.delayIf != nil {
		if d := c.delayIf(call); d > 0 {
			time.Sleep(d)
		}
	}
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	if c.exclusiveSet[call.Name] && c.inFlight > 1 {
		c.exclusiveOverlapErr = fmt.Sprintf("exclusive tool %s overlapped with %d other call(s)", call.Name, c.inFlight-1)
	}
	var waitCh chan struct{}
	if c.barrierTarget > 1 && isReadOnlyConcurrencySafeTool(call.Name) && c.barrierReady != nil {
		c.barrierArrived++
		if c.barrierArrived >= c.barrierTarget {
			select {
			case <-c.barrierReady:
			default:
				close(c.barrierReady)
			}
		}
		waitCh = c.barrierReady
	}
	c.mu.Unlock()

	if waitCh != nil {
		select {
		case <-waitCh:
		case <-time.After(c.barrierTimeout):
		}
	}
	if c.holdIf != nil {
		if hold := c.holdIf(call); hold != nil {
			<-hold
		}
	}

	defer func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
	}()
	return c.base.CheckToolCall(call)
}

func (c *concTestChecker) MaxInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInFlight
}

func (c *concTestChecker) ExclusiveOverlapError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exclusiveOverlapErr
}

type concTestHarness struct {
	st          *store.SQLiteStore
	root        string
	workspaceID string
	run         domain.Run
	threadID    string
	provider    *concTestProvider
	router      *llm.Router
	checker     policy.Checker
	caps        domain.ExecutionPermissionRuntimeCapabilities
	mcpClient   AgentRunnerMCPClient
}

func newConcTestHarness(t *testing.T, mode domain.RunExecutionPermissionMode, budget domain.Budget, provider *concTestProvider, checker policy.Checker) *concTestHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "conc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wsID := "ws-conc-test"
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID: wsID, Name: "conc-test", RootPath: root, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        authority,
	}
	if checker == nil {
		checker = policy.NewDefaultChecker()
	}
	_, run, err := NewRunService(st).Create(t.Context(), CreateRunRequest{
		Goal: "Concurrency verification", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: wsID, ModelRoute: provider.Name() + "/model", Interactive: true, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" && mode != domain.RunExecutionPermissionAsk {
		if _, err := NewThreadExecutionPermissionService(st, caps).Change(t.Context(), ChangeThreadExecutionPermissionRequest{
			ThreadID: domain.InitialThreadID(run.ID), Mode: string(mode), OperationKey: "conc-perm-setup-key-" + string(mode),
			RequestedBy: "test_operator", Reason: "conc test setup", ConfirmFull: mode == domain.RunExecutionPermissionFull,
		}); err != nil {
			t.Fatal(err)
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return &concTestHarness{
		st: st, root: root, workspaceID: wsID, run: run,
		threadID: domain.InitialThreadID(run.ID), provider: provider,
		router: router, checker: checker, caps: caps,
	}
}

func (h *concTestHarness) runner() *AgentRunner {
	r := NewAgentRunner(h.st, h.router, h.checker).
		WithExecutionPermissionCapabilities(h.caps).
		WithGeneratedContextCompaction(false)
	if h.mcpClient != nil {
		r.WithMCPClient(h.mcpClient)
	}
	return r
}

func (h *concTestHarness) turns() *ThreadTurnService {
	exec := NewRunExecutionHandoffService(h.st, h.router, h.checker).
		WithExecutionPermissionCapabilities(h.caps).
		WithGeneratedContextCompaction(false)
	lifecycle := NewRunLifecycleControlService(h.st)
	return NewThreadTurnServiceWithExecutionCapabilities(h.st, lifecycle, exec, h.caps)
}

func concWriteFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func concReadPayload(rel string, startLine, endLine int) string {
	b, _ := json.Marshal(toolgateway.WorkspaceReadPayload{
		Version: toolgateway.AgentCodeRegistryVersion, Path: rel, StartLine: startLine, EndLine: endLine,
	})
	return string(b)
}

func concActionResponse(kind domain.RootActionKind, msg, summary string) *llm.ChatResponse {
	raw, _ := json.Marshal(map[string]any{
		"version": domain.RootLifecycleVersion,
		"action":  kind, "message": msg, "summary": summary, "reason": "",
	})
	return &llm.ChatResponse{
		Text: string(raw), Provider: "conc-test-provider", Model: "model",
		Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
	}
}

func TestSupervisorToolConcurrencyClassification(t *testing.T) {
	if SupervisorReadOnlyPoolSize != 8 {
		t.Fatalf("expected SupervisorReadOnlyPoolSize=8, got %d", SupervisorReadOnlyPoolSize)
	}
	expectedReadOnly := map[toolgateway.ToolName]bool{
		toolgateway.WorkspaceListTool:      true,
		toolgateway.WorkspaceReadTool:      true,
		toolgateway.WorkspaceGlobTool:      true,
		toolgateway.WorkspaceGrepTool:      true,
		toolgateway.WorkspaceViewImageTool: true,
		toolgateway.GitHubEvidenceListTool: true,
		toolgateway.GitHubEvidenceReadTool: true,
		toolgateway.CodeWorkspaceSymbolsTool: true,
		toolgateway.CodeDocumentSymbolsTool:  true,
		toolgateway.CodeDefinitionTool:       true,
		toolgateway.CodeReferencesTool:       true,
		toolgateway.CodeImplementationTool:   true,
		toolgateway.CodeHoverTool:            true,
		toolgateway.CodeSignatureHelpTool:    true,
		toolgateway.CodeDiagnosticsTool:      true,
		toolgateway.CodeCallHierarchyTool:    true,
		toolgateway.CodeTypeHierarchyTool:    true,
		toolgateway.HistorySearchTool:        true,
		toolgateway.HistoryReadTool:          true,
		toolgateway.ThreadListTool:           true,
		toolgateway.MCPToolSearchTool:        true,
	}

	var catalog []toolgateway.ToolDefinition
	catalog = append(catalog, toolgateway.SupervisorToolDefinitions()...)
	catalog = append(catalog, toolgateway.AgentCodeToolDefinitions()...)
	catalog = append(catalog, toolgateway.CodeIntelToolDefinitions()...)
	for _, b := range []toolgateway.ToolName{
		toolgateway.BrowserScrollTool, toolgateway.BrowserKeyTool,
		toolgateway.BrowserUploadTool, toolgateway.BrowserMoveTool,
	} {
		if def, ok := toolgateway.AgentBrowserToolDefinition(b); ok {
			catalog = append(catalog, def)
		}
	}

	seen := make(map[toolgateway.ToolName]bool)
	for _, def := range catalog {
		seen[def.Name] = true
		got := isReadOnlyConcurrencySafeTool(string(def.Name))
		want := expectedReadOnly[def.Name]
		if got != want {
			t.Fatalf("tool %q concurrency classification mismatch: got=%v want=%v", def.Name, got, want)
		}
	}
	for name := range expectedReadOnly {
		if !seen[name] {
			t.Fatalf("expected read-only tool %q missing from inspected tool catalog", name)
		}
	}
	for _, failClosed := range []string{"", "unknown_tool", "host_command_propose", "workspace_change", "workspace_apply", "workspace_delete", "skill_read", "web_fetch", "mcp_tool_call", "command_runtime", "run_tests"} {
		if isReadOnlyConcurrencySafeTool(failClosed) {
			t.Fatalf("expected fail-closed exclusive classification for %q", failClosed)
		}
	}
}

func TestPartitionSupervisorToolSubBatches(t *testing.T) {
	calls := []domain.SupervisorToolCall{
		{Position: 1, ToolName: "workspace_read", Status: domain.SupervisorToolPending},
		{Position: 2, ToolName: "code_references", Status: domain.SupervisorToolPending},
		{Position: 3, ToolName: "workspace_read", Status: domain.SupervisorToolCompleted},
		{Position: 4, ToolName: "workspace_grep", Status: domain.SupervisorToolPending},
		{Position: 5, ToolName: "mcp_tool_search", Status: domain.SupervisorToolPending},
		{Position: 6, ToolName: "work_item_create", Status: domain.SupervisorToolPending},
		{Position: 7, ToolName: "workspace_list", Status: domain.SupervisorToolPending},
		{Position: 8, ToolName: "thread_list", Status: domain.SupervisorToolPending},
	}
	got := partitionSupervisorToolSubBatches(calls)
	want := []supervisorToolSubBatch{
		{start: 0, end: 2, readOnly: true},
		{start: 2, end: 3, readOnly: false},
		{start: 3, end: 5, readOnly: true},
		{start: 5, end: 6, readOnly: false},
		{start: 6, end: 8, readOnly: true},
	}
	if len(got) != len(want) {
		t.Fatalf("sub-batch count mismatch: got=%+v want=%+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sub-batch %d mismatch: got=%+v want=%+v", i, got[i], want[i])
		}
	}
}

func TestSupervisorToolConcurrencyEightReadsPoolAndOrderedSequence(t *testing.T) {
	checker := newConcTestChecker(8)
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	for i := 1; i <= 8; i++ {
		concWriteFile(t, h.root, fmt.Sprintf("r%d.txt", i), fmt.Sprintf("payload-token-%02d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, llm.ToolCall{
					ID: fmt.Sprintf("c-%d", i), Name: "workspace_read",
					Arguments: json.RawMessage(concReadPayload(fmt.Sprintf("r%d.txt", i), 1, 2)),
				})
			}
			return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: calls}, nil
		}
		return concActionResponse(domain.RootActionFinish, "all 8 reads complete", "done"), nil
	}

	if _, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
		Content: "Read 8 files concurrently", OperationKey: "conc-8-reads-op-key", RequestedBy: "test_operator",
	}); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if checker.MaxInFlight() != 8 {
		t.Fatalf("expected maxInFlight=8, got %d", checker.MaxInFlight())
	}

	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 8 {
		t.Fatalf("unexpected rounds: %+v err=%v", rounds, err)
	}
	for i, c := range rounds[0].Calls {
		want := fmt.Sprintf("payload-token-%02d", i+1)
		if c.Position != i+1 || c.Status != domain.SupervisorToolCompleted || !strings.Contains(c.ResultJSON, want) {
			t.Fatalf("call %d out of order or missing %q: %+v", i, want, c)
		}
	}
	evts, err := h.st.ListRunEvents(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	chargedSeq := 0
	resultPos := 0
	for _, ev := range evts {
		if ev.Type == events.ToolBudgetChargedEvent {
			chargedSeq++
			if !strings.Contains(ev.PayloadJSON, fmt.Sprintf(`"consumed":%d`, chargedSeq)) {
				t.Fatalf("ToolBudgetChargedEvent %d out of sequence: %s", chargedSeq, ev.PayloadJSON)
			}
		}
		if ev.Type == events.SupervisorToolResultEvent {
			resultPos++
			if !strings.Contains(ev.PayloadJSON, fmt.Sprintf(`"position":%d`, resultPos)) {
				t.Fatalf("SupervisorToolResultEvent %d out of position order: %s", resultPos, ev.PayloadJSON)
			}
		}
	}
	if chargedSeq != 8 || resultPos != 8 {
		t.Fatalf("expected 8 budget charges and 8 result events, got charged=%d results=%d", chargedSeq, resultPos)
	}
}

func TestSupervisorToolConcurrencyPartialMaxToolCallsExhaustionDeterministic(t *testing.T) {
	checker := newConcTestChecker(1)
	// Delay position 1 (f1.txt) so later positions enter Invoke earlier;
	// the ordered budget turnstile must still charge positions 1..3 first!
	checker.delayIf = func(call tools.Call) time.Duration {
		if strings.Contains(call.Args["payload"], "f1.txt") {
			return 20 * time.Millisecond
		}
		return 0
	}
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 3}, p, checker)
	for i := 1; i <= 8; i++ {
		concWriteFile(t, h.root, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("file-%02d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, llm.ToolCall{
					ID: fmt.Sprintf("pb-%d", i), Name: "workspace_read",
					Arguments: json.RawMessage(concReadPayload(fmt.Sprintf("f%d.txt", i), 1, 2)),
				})
			}
			return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: calls}, nil
		}
		return concActionResponse(domain.RootActionFinish, "partial budget handled", "done"), nil
	}

	if _, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
		Content: "8 reads with budget 3", OperationKey: "conc-partial-budget-op-key", RequestedBy: "test_operator",
	}); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 8 {
		t.Fatalf("unexpected rounds: %+v err=%v", rounds, err)
	}
	for i, c := range rounds[0].Calls {
		if i < 3 {
			if c.Status != domain.SupervisorToolCompleted || !strings.Contains(c.ResultJSON, fmt.Sprintf("file-%02d", i+1)) {
				t.Fatalf("expected position %d completed with file-%02d, got %+v", i+1, i+1, c)
			}
		} else {
			if c.Status != domain.SupervisorToolFailed || c.ErrorCode != string(apperror.CodeResourceExhausted) {
				t.Fatalf("expected position %d failed with RESOURCE_EXHAUSTED, got %+v", i+1, c)
			}
		}
	}
}

func TestSupervisorToolConcurrencyExclusiveBarriersBetweenReadSubBatches(t *testing.T) {
	checker := newConcTestChecker(3)
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	for i := 1; i <= 6; i++ {
		concWriteFile(t, h.root, fmt.Sprintf("b%d.txt", i), fmt.Sprintf("b%d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{
					{ID: "r1", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("b1.txt", 1, 2))},
					{ID: "r2", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("b2.txt", 1, 2))},
					{ID: "r3", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("b3.txt", 1, 2))},
					{ID: "w1", Name: "work_item_create", Arguments: json.RawMessage(`{"title":"Barrier"}`)},
					{ID: "w2", Name: "note_create", Arguments: json.RawMessage(`{"title":"Note","content":"serial"}`)},
					{ID: "r4", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("b4.txt", 1, 2))},
					{ID: "r5", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("b5.txt", 1, 2))},
					{ID: "r6", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("b6.txt", 1, 2))},
				},
			}, nil
		}
		return concActionResponse(domain.RootActionFinish, "barrier ok", "done"), nil
	}

	if _, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
		Content: "Mixed reads with exclusive barrier", OperationKey: "conc-barrier-op-key", RequestedBy: "test_operator",
	}); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if checker.MaxInFlight() != 3 {
		t.Fatalf("expected maxInFlight=3 for 3-call read waves, got %d", checker.MaxInFlight())
	}
	if msg := checker.ExclusiveOverlapError(); msg != "" {
		t.Fatalf("exclusive barrier violated: %s", msg)
	}
}

func TestSupervisorToolConcurrencyParentCancelStopsSiblingsAndPersistsNoCompleted(t *testing.T) {
	entered := make(chan struct{})
	var enteredCount atomic.Int32
	hold := make(chan struct{})
	checker := newConcTestChecker(0)
	checker.onEnter = func(c tools.Call) {
		if c.Name == "workspace_read" && enteredCount.Add(1) == 4 {
			select {
			case <-entered:
			default:
				close(entered)
			}
		}
	}
	checker.holdIf = func(c tools.Call) <-chan struct{} {
		if c.Name == "workspace_read" {
			return hold
		}
		return nil
	}
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	run, err := NewRunService(h.st).Start(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.run = run
	for i := 1; i <= 8; i++ {
		concWriteFile(t, h.root, fmt.Sprintf("c%d.txt", i), fmt.Sprintf("c%d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, _ int) (*llm.ChatResponse, error) {
		calls := make([]llm.ToolCall, 0, 8)
		for i := 1; i <= 8; i++ {
			calls = append(calls, llm.ToolCall{
				ID: fmt.Sprintf("cancel-%d", i), Name: "workspace_read",
				Arguments: json.RawMessage(concReadPayload(fmt.Sprintf("c%d.txt", i), 1, 2)),
			})
		}
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: calls}, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, stepErr := h.runner().Step(ctx, h.run.ID)
		done <- stepErr
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(hold)
		t.Fatal("read batch did not enter in-flight barrier")
	}
	cancel()
	close(hold)

	var stepErr error
	select {
	case stepErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Step hung after parent cancellation")
	}
	if apperror.CodeOf(stepErr) != apperror.CodeCancelled {
		t.Fatalf("expected CodeCancelled, got %v", stepErr)
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.Status == domain.SupervisorToolCompleted {
				t.Fatalf("cancelled batch committed completed call: %+v", c)
			}
		}
	}
}

func TestSupervisorToolConcurrencyPerCallAuthorityMismatchFailsCleanly(t *testing.T) {
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, nil)
	concWriteFile(t, h.root, "a1.txt", "alpha\n")
	concWriteFile(t, h.root, "a2.txt", "beta\n")
	run, err := NewRunService(h.st).Start(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	runner := h.runner()
	ctx := t.Context()
	lease, err := h.st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "test-owner", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := h.st.BeginSupervisorTurn(ctx, lease.Lease, "")
	if err != nil {
		t.Fatal(err)
	}
	permission, err := h.st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap, auth, err := runner.supervisorAgentCodeCapabilities(ctx, turn, permission)
	if err != nil {
		t.Fatal(err)
	}
	options := supervisorToolOptions{AgentCode: supervisorAgentCodeTools{Capabilities: snap, Authority: auth}}
	calls, err := prepareSupervisorToolCalls([]llm.ToolCall{
		{ID: "k1", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("a1.txt", 1, 2))},
		{ID: "k2", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("a2.txt", 1, 2))},
	}, turn.Run.ID, turn.Checkpoint.NextTurn, 1, turn.Mode.Surface, turn.Mode.Phase,
		domain.RunExecutionPermissionAuto, false, false, options)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{
		SupervisorAttemptID: turn.Checkpoint.AttemptID,
		Number:              1,
		TransportAttempt:    1,
		MaxAttempts:         1,
		ToolRound:           0,
		Provider:            p.Name(),
		Model:               "model",
	}
	if _, err := h.st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	if _, err := h.st.RecordSupervisorModelCompletedForAgent(ctx, turn.Checkpoint, attempt, llm.ChatResponse{
		Provider: p.Name(), Model: "model", ToolCalls: calls,
		Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
	}, domain.AgentAttribution{
		AgentID: turn.Agent.ID, AgentAttemptID: turn.Checkpoint.AttemptID, Source: domain.AgentAttributionRecorded,
	}); err != nil {
		t.Fatal(err)
	}
	rounds, err := h.st.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper position 2's AuthorityJSON before dispatch so position 2 fails per-call authority verification.
	rounds[0].Calls[1].AuthorityJSON = `{"run_id":"wrong-run"}`
	_, waiting, resumeErr := runner.resumeSupervisorToolsRaw(ctx, turn, rounds)
	if waiting || apperror.CodeOf(resumeErr) != apperror.CodeFailedPrecondition {
		t.Fatalf("expected CodeFailedPrecondition on tampered authority in parallel sub-batch, got waiting=%v err=%v", waiting, resumeErr)
	}
	// Position 1 succeeded before position 2's fatal error and was committed in Position order;
	// Position 2 was NOT committed as completed.
	updatedRounds, err := h.st.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil || len(updatedRounds) != 1 || len(updatedRounds[0].Calls) != 2 {
		t.Fatalf("unexpected updatedRounds: %+v err=%v", updatedRounds, err)
	}
	if updatedRounds[0].Calls[1].Status == domain.SupervisorToolCompleted {
		t.Fatalf("tampered call 2 must not be committed completed: %+v", updatedRounds[0].Calls[1])
	}
}

func TestSupervisorToolConcurrencyMCPSearchConcurrent(t *testing.T) {
	checker := newConcTestChecker(4)
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	run, err := NewRunService(h.st).Start(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.run = run
	client := &exactPermissionMCPClient{
		capabilities: mcp.ScopedCapabilities{
			ProtocolVersion: mcp.ClientProtocolVersion,
			Generation:      strings.Repeat("a", 64),
			Servers: []mcp.ScopedServerCapability{{
				ServerID: "srv-1", Name: "Docs", DescriptorFingerprint: strings.Repeat("b", 64),
				CapabilityFingerprint: strings.Repeat("c", 64), RegistrationGeneration: 1,
				Tools: []mcp.RemoteTool{{Name: "lookup", Description: "Lookup docs", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			}},
		},
	}
	h.mcpClient = client
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{
					{ID: "ms-1", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"lookup"}`)},
					{ID: "ms-2", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"docs"}`)},
					{ID: "ms-3", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"srv"}`)},
					{ID: "ms-4", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":""}`)},
				},
			}, nil
		}
		return concActionResponse(domain.RootActionFinish, "4 mcp searches ran concurrently", "done"), nil
	}

	if _, err := h.runner().Step(t.Context(), h.run.ID); err != nil {
		t.Fatalf("step failed: %v", err)
	}
	if checker.MaxInFlight() != 4 {
		t.Fatalf("expected 4 concurrent mcp_tool_search invocations, got maxInFlight=%d", checker.MaxInFlight())
	}
}
