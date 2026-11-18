package application_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

// --- Shared Opaque-Box E2E Test Helpers ---

type e2eScriptedProvider struct {
	mu             sync.Mutex
	name           string
	privateHistory bool
	vision         llm.VisionSupport
	requests       []llm.ChatRequest
	respond        func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
}

func (p *e2eScriptedProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "e2e-provider"
}

func (p *e2eScriptedProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: p.Name(), Capabilities: []string{"chat", "tools"}}}, nil
}

func (p *e2eScriptedProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	index := len(p.requests) - 1
	respond := p.respond
	p.mu.Unlock()
	if respond == nil {
		return nil, errors.New("e2e scripted provider has no respond handler")
	}
	return respond(ctx, request, index)
}

func (p *e2eScriptedProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	resp, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if resp.Text != "" {
		chunks <- llm.ChatChunk{Text: resp.Text}
	}
	chunks <- llm.FinalChatChunk(resp)
	close(chunks)
	return chunks, nil
}

func (*e2eScriptedProvider) SupportsTools(string) bool    { return true }
func (p *e2eScriptedProvider) SupportsVision(string) bool { return p.vision == llm.VisionSupported }
func (*e2eScriptedProvider) SupportsJSONMode(string) bool { return true }
func (p *e2eScriptedProvider) DescribeVision(string) llm.VisionCapability {
	state := p.vision
	if state == "" {
		state = llm.VisionUnsupported
	}
	return llm.VisionCapability{State: state, Source: "operator_declared"}
}
func (p *e2eScriptedProvider) RequiresPrivateAssistantHistory(string) bool {
	return p.privateHistory
}

func (p *e2eScriptedProvider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

// e2eConcurrencyChecker wraps policy.Checker to observe real in-flight concurrency,
// enforce rendezvous barriers for read-only batches, and verify serial barriers for
// exclusive tools without touching production internals.
type e2eConcurrencyChecker struct {
	base           policy.Checker
	needsReview    func(tools.Call) bool
	denyCall       func(tools.Call) bool
	barrierTarget  int
	barrierTimeout time.Duration
	onEnter        func(tools.Call)
	holdIf         func(tools.Call) <-chan struct{}

	mu                  sync.Mutex
	inFlight            int
	maxInFlight         int
	exclusiveOverlapErr string
	barrierArrived      int
	barrierReady        chan struct{}
	callOrder           []string
	activeNames         map[string]int
}

func newE2EConcurrencyChecker(barrierTarget int) *e2eConcurrencyChecker {
	c := &e2eConcurrencyChecker{
		base:           policy.NewDefaultChecker(),
		barrierTarget:  barrierTarget,
		barrierTimeout: 300 * time.Millisecond,
		activeNames:    make(map[string]int),
	}
	if barrierTarget > 1 {
		c.barrierReady = make(chan struct{})
	}
	return c
}

func (c *e2eConcurrencyChecker) CheckText(ctx, text string) policy.Decision {
	return c.base.CheckText(ctx, text)
}

func (c *e2eConcurrencyChecker) isBarrierEligibleRead(name string) bool {
	switch name {
	case "workspace_read", "workspace_list", "workspace_glob", "workspace_grep",
		"workspace_view_image", "history_search", "history_read", "thread_list",
		"code_workspace_symbols", "code_document_symbols", "code_definition",
		"code_references", "code_implementation", "code_hover",
		"code_signature_help", "code_diagnostics", "code_call_hierarchy",
		"code_type_hierarchy":
		return true
	default:
		return false
	}
}

func (c *e2eConcurrencyChecker) isExclusiveTool(name string) bool {
	switch name {
	case "work_item_create", "note_create", "thread_send_message", "skill_read",
		"workspace_change", "workspace_apply", "workspace_delete",
		"create_file", "replace_file", "move_file", "delete_file":
		return true
	default:
		return false
	}
}

func (c *e2eConcurrencyChecker) CheckToolCall(call tools.Call) policy.Decision {
	if c.onEnter != nil {
		c.onEnter(call)
	}
	c.mu.Lock()
	c.callOrder = append(c.callOrder, call.Name)
	c.inFlight++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	c.activeNames[call.Name]++
	if c.isExclusiveTool(call.Name) && c.inFlight > 1 {
		c.exclusiveOverlapErr = fmt.Sprintf("exclusive tool %s overlapped with %d other in-flight calls (%v)",
			call.Name, c.inFlight-1, c.activeNames)
	}
	for active := range c.activeNames {
		if active != call.Name && c.isExclusiveTool(active) {
			c.exclusiveOverlapErr = fmt.Sprintf("tool %s ran concurrently with exclusive tool %s", call.Name, active)
		}
	}
	var waitCh chan struct{}
	if c.barrierTarget > 1 && c.isBarrierEligibleRead(call.Name) && c.barrierReady != nil {
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
		c.activeNames[call.Name]--
		if c.activeNames[call.Name] <= 0 {
			delete(c.activeNames, call.Name)
		}
		c.mu.Unlock()
	}()

	d := c.base.CheckToolCall(call)
	if c.denyCall != nil && c.denyCall(call) {
		d.Allowed = false
		d.Reason = "e2e policy denied"
		return d
	}
	if c.needsReview != nil && c.needsReview(call) {
		d.NeedsApproval = true
	}
	return d
}

func (c *e2eConcurrencyChecker) MaxInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInFlight
}

func (c *e2eConcurrencyChecker) ExclusiveOverlapError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exclusiveOverlapErr
}

type e2eHarness struct {
	st           *store.SQLiteStore
	root         string
	workspaceID  string
	mission      domain.Mission
	run          domain.Run
	threadID     string
	provider     *e2eScriptedProvider
	router       *llm.Router
	checker      policy.Checker
	caps         domain.ExecutionPermissionRuntimeCapabilities
	authority    *domain.ExecutionPermissionRuntimeAuthority
	codeIntel    *codeintel.Manager
	peerMessages *application.ThreadPeerMessageService
}

func newE2EHarness(t *testing.T, mode domain.RunExecutionPermissionMode, budget domain.Budget, provider *e2eScriptedProvider, checker policy.Checker) *e2eHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wsID := "ws-e2e-throughput"
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID: wsID, Name: "e2e-throughput", RootPath: root, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        authority,
	}
	if provider == nil {
		provider = &e2eScriptedProvider{}
	}
	if checker == nil {
		checker = policy.NewDefaultChecker()
	}
	mission, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Throughput E2E verification", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: wsID, ModelRoute: provider.Name() + "/model", Interactive: true, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" && mode != domain.RunExecutionPermissionAsk {
		if _, err := application.NewThreadExecutionPermissionService(st, caps).Change(t.Context(), application.ChangeThreadExecutionPermissionRequest{
			ThreadID: domain.InitialThreadID(run.ID), Mode: string(mode), OperationKey: "e2e-permission-setup-" + string(mode),
			RequestedBy: "test_operator", Reason: "e2e permission setup",
			ConfirmFull: mode == domain.RunExecutionPermissionFull,
		}); err != nil {
			t.Fatal(err)
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return &e2eHarness{
		st: st, root: root, workspaceID: wsID, mission: mission, run: run,
		threadID: domain.InitialThreadID(run.ID), provider: provider, router: router,
		checker: checker, caps: caps, authority: authority,
	}
}

func (h *e2eHarness) startRun(t *testing.T) domain.Run {
	t.Helper()
	run, err := application.NewRunService(h.st).Start(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.run = run
	perm, err := h.st.GetRunExecutionPermission(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if perm.Mode == domain.RunExecutionPermissionFull {
		if _, err := h.authority.ActivateRunFullAccess(perm); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func (h *e2eHarness) runner() *application.AgentRunner {
	r := application.NewAgentRunner(h.st, h.router, h.checker).
		WithExecutionPermissionCapabilities(h.caps).
		WithGeneratedContextCompaction(false)
	if h.codeIntel != nil {
		r.WithCodeIntel(h.codeIntel)
	}
	if h.peerMessages != nil {
		r.WithThreadPeerMessages(h.peerMessages)
	}
	return r
}

func (h *e2eHarness) turns() *application.ThreadTurnService {
	exec := application.NewRunExecutionHandoffService(h.st, h.router, h.checker).
		WithExecutionPermissionCapabilities(h.caps).
		WithGeneratedContextCompaction(false)
	if h.codeIntel != nil {
		exec.WithCodeIntel(h.codeIntel)
	}
	if h.peerMessages != nil {
		exec.WithThreadPeerMessages(h.peerMessages)
	}
	lifecycle := application.NewRunLifecycleControlService(h.st)
	return application.NewThreadTurnServiceWithExecutionCapabilities(h.st, lifecycle, exec, h.caps)
}

func (h *e2eHarness) turnRequest(opKey, content string) application.ExecuteThreadTurnRequest {
	if len(opKey) < 16 {
		opKey = "e2e-operation-key-" + opKey
	}
	return application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
		Content: content, OperationKey: opKey, RequestedBy: "test_operator",
	}
}

func e2eWriteFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return fileedit.HashText(content)
}

func e2eMakePNG(t *testing.T, seed byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: seed + byte(x*10), G: seed + byte(y*10), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func e2eToolCall(id, name, argsJSON string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(argsJSON)}
}

func e2eToolResponse(calls ...llm.ToolCall) *llm.ChatResponse {
	return &llm.ChatResponse{
		Provider: "e2e-provider", Model: "model",
		Usage:     llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
		ToolCalls: calls,
	}
}

func e2eActionResponse(kind domain.RootActionKind, msg, summary, reason string) *llm.ChatResponse {
	payload, _ := json.Marshal(map[string]any{
		"version": domain.RootLifecycleVersion,
		"action":  kind, "message": msg, "summary": summary, "reason": reason,
	})
	return &llm.ChatResponse{
		Text: string(payload), Provider: "e2e-provider", Model: "model",
		Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
	}
}

func e2eReadPayload(rel string, startLine, endLine int) string {
	b, _ := json.Marshal(toolgateway.WorkspaceReadPayload{
		Version: toolgateway.AgentCodeRegistryVersion, Path: rel, StartLine: startLine, EndLine: endLine,
	})
	return string(b)
}

func e2eChangeCreatePayload(rel, content string) string {
	b, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: rel, ExpectedSHA256: "missing", Content: content,
	})
	return string(b)
}

func e2eChangeReplacePayload(rel, expectedSHA, content string) string {
	b, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "replace", Path: rel, ExpectedSHA256: expectedSHA, Content: content,
	})
	return string(b)
}

func e2eChangePatchPayload(rel, expectedSHA, oldText, newText string) string {
	b, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: rel, ExpectedSHA256: expectedSHA,
		Replacements: []toolgateway.WorkspaceReplacement{{OldText: oldText, NewText: newText, ExpectedOccurrences: 1}},
	})
	return string(b)
}

func e2eChangeMovePayload(src, srcSHA, dst string) string {
	b, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "move", Path: src, ExpectedSHA256: srcSHA,
		DestinationPath: dst, DestinationExpectedSHA256: "missing",
	})
	return string(b)
}

func e2eExtractResultEnvelopeStdout(t *testing.T, resultJSON string) map[string]any {
	t.Helper()
	var env struct {
		Status string `json:"status"`
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &env); err != nil {
		t.Fatalf("unmarshal envelope %q: %v", resultJSON, err)
	}
	var out map[string]any
	if env.Stdout != "" {
		if err := json.Unmarshal([]byte(env.Stdout), &out); err != nil {
			t.Fatalf("unmarshal stdout %q: %v", env.Stdout, err)
		}
	}
	return out
}

func e2eCountBoundaryRollovers(t *testing.T, st *store.SQLiteStore, runID string) int {
	t.Helper()
	evts, err := st.ListRunEvents(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range evts {
		if strings.Contains(ev.PayloadJSON, `"tool_round_boundary":true`) {
			count++
		}
	}
	return count
}

func e2eHasBoundaryPrompt(requests []llm.ChatRequest) bool {
	for _, req := range requests {
		if len(req.Tools) == 0 && len(req.Messages) > 0 &&
			strings.Contains(req.Messages[len(req.Messages)-1].Content, "scheduling boundary") {
			return true
		}
	}
	return false
}

// --- CodeIntel LSP Helper Process (reused via codeintel.Manager) ---

func TestThroughputE2ELSPHelper(t *testing.T) {
	mode := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.outputdir=e2e-lsp-") {
			mode = strings.TrimPrefix(arg, "-test.outputdir=e2e-lsp-")
			break
		}
	}
	if mode == "" {
		return
	}
	os.Exit(runE2ELSPHelper(mode, os.Stdin, os.Stdout))
}

func runE2ELSPHelper(mode string, input io.Reader, output io.Writer) int {
	reader := bufio.NewReader(input)
	var writeMu sync.Mutex
	send := func(msg any) error {
		raw, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = fmt.Fprintf(output, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
		return err
	}
	var rootURI string
	for {
		var length int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return 0
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				v, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
				length = v
			}
		}
		if length <= 0 {
			continue
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			return 0
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &req) != nil {
			return 2
		}
		if req.Method == "exit" {
			return 0
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			var p struct {
				RootURI string `json:"rootUri"`
			}
			_ = json.Unmarshal(req.Params, &p)
			rootURI = p.RootURI
			result = map[string]any{
				"serverInfo": map[string]any{"name": "e2e-lsp", "version": mode},
				"capabilities": map[string]any{
					"workspaceSymbolProvider":  true,
					"documentSymbolProvider":   true,
					"definitionProvider":       true,
					"referencesProvider":       true,
					"implementationProvider":   true,
					"hoverProvider":            true,
					"signatureHelpProvider":    map[string]any{"triggerCharacters": []string{"("}},
					"diagnosticProvider":       map[string]any{"interFileDependencies": true, "workspaceDiagnostics": false},
					"callHierarchyProvider":    true,
					"typeHierarchyProvider":    true,
					"textDocumentSync":         map[string]any{"openClose": true, "change": 1},
				},
			}
		case "shutdown":
			result = nil
		case "workspace/symbol":
			result = []any{map[string]any{
				"name": "Compute", "kind": 12,
				"location": map[string]any{"uri": rootURI + "/main.go", "range": e2eLSPRange()},
			}}
		case "textDocument/documentSymbol":
			result = []any{map[string]any{
				"name": "Compute", "kind": 12, "range": e2eLSPRange(), "selectionRange": e2eLSPRange(),
			}}
		case "textDocument/definition", "textDocument/references", "textDocument/implementation":
			result = []any{map[string]any{"uri": rootURI + "/main.go", "range": e2eLSPRange()}}
		case "textDocument/hover":
			result = map[string]any{"contents": map[string]string{"kind": "markdown", "value": "func Compute() int"}, "range": e2eLSPRange()}
		case "textDocument/diagnostic":
			result = map[string]any{"items": []any{}}
		default:
			result = []any{}
		}
		if send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			return 3
		}
	}
}

func e2eLSPRange() map[string]any {
	return map[string]any{
		"start": map[string]int{"line": 1, "character": 0},
		"end":   map[string]int{"line": 1, "character": 12},
	}
}

func newE2ECodeIntelManager(t *testing.T, workspaceID string) *codeintel.Manager {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := codeintel.NewManager([]codeintel.ServerDescriptor{{
		ProtocolVersion: codeintel.ProtocolVersion,
		ID:              "e2e-lsp",
		Name:            "E2E Test LSP",
		WorkspaceID:     workspaceID,
		Languages:       []codeintel.Language{{ID: "go", Extensions: []string{".go"}}},
		Executable:      exe,
		ExecutableSHA256: hex.EncodeToString(h.Sum(nil)),
		Arguments: []string{
			"-test.run=^TestThroughputE2ELSPHelper$",
			"-test.outputdir=e2e-lsp-normal",
		},
		RequestTimeoutMillis: 4000,
		ReviewedBy:           "e2e-test",
		ReviewedAt:           time.Now().UTC(),
		Source:               codeintel.Source{Kind: "operator_config", Label: "e2e-lsp.json", SHA256: strings.Repeat("a", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = mgr.Close(ctx)
		cancel()
	})
	return mgr
}

// ============================================================================
// TIER 1: FEATURE COVERAGE (R1, R2, R3)
// ============================================================================

func TestThroughputE2E_Tier1_R1_EightCallReadBatchExecutesConcurrently(t *testing.T) {
	checker := newE2EConcurrencyChecker(8)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	for i := 1; i <= 8; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("line-%d\n", i))
	}
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("r-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("f%d.txt", i), 1, 5)))
			}
			return e2eToolResponse(calls...), nil
		case 1:
			return e2eActionResponse(domain.RootActionFinish, "Read all 8 files concurrently", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	res, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r1-8-reads", "Read f1..f8 in parallel"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 1 || step.ToolCalls != 8 {
		t.Fatalf("expected 1 round / 8 calls, got rounds=%d calls=%d", step.ToolRounds, step.ToolCalls)
	}
	if checker.MaxInFlight() != 8 {
		t.Fatalf("expected 8 concurrent in-flight read calls, got maxInFlight=%d", checker.MaxInFlight())
	}
}

func TestThroughputE2E_Tier1_R1_MixedReadOnlyToolFamiliesConcurrent(t *testing.T) {
	checker := newE2EConcurrencyChecker(8)
	p := &e2eScriptedProvider{vision: llm.VisionSupported}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	e2eWriteFile(t, h.root, "alpha.go", "package alpha\n// needle\n")
	if err := os.WriteFile(filepath.Join(h.root, "diagram.png"), e2eMakePNG(t, 11), 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed peer thread so thread_list returns a real peer target, and wire ThreadPeerMessages.
	_, peerRun, err := application.NewRunService(h.st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Peer target", Profile: "review", WorkspaceID: h.workspaceID, Interactive: true,
		ModelRoute: p.Name() + "/model", Budget: domain.Budget{MaxTurns: 2, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = peerRun
	h.peerMessages = application.NewThreadPeerMessageService(h.st, h.turns())
	// Create a real note so history_search finds something concrete.
	if _, err := application.NewNoteService(h.st).Create(t.Context(), application.CreateNoteRequest{
		RunID: h.run.ID, Title: "Seeded history note", Content: "prior exploration needle", Visibility: "root",
	}); err != nil {
		t.Fatal(err)
	}

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return e2eToolResponse(
				e2eToolCall("m1", "workspace_list", `{"version":"agent-code-tools.v1","path":".","limit":20}`),
				e2eToolCall("m2", "workspace_read", e2eReadPayload("alpha.go", 1, 10)),
				e2eToolCall("m3", "workspace_glob", `{"version":"agent-code-tools.v1","pattern":"*.go","limit":20}`),
				e2eToolCall("m4", "workspace_grep", `{"version":"agent-code-tools.v1","query":"needle","pattern":"*.go","limit":20,"case_sensitive":false}`),
				e2eToolCall("m5", "workspace_view_image", `{"version":"agent-code-tools.v1","path":"diagram.png"}`),
				e2eToolCall("m6", "history_search", `{"query":"needle","limit":5}`),
				e2eToolCall("m7", "history_search", `{"query":"exploration","limit":5}`),
				e2eToolCall("m8", "thread_list", `{}`),
			), nil
		}
		if index == 1 {
			// Verify image pixels were attached and all 8 tool results were delivered.
			resultCount := 0
			imageCount := 0
			for _, msg := range req.Messages {
				resultCount += len(msg.ToolResults)
				imageCount += len(msg.Images)
			}
			if resultCount != 8 || imageCount != 1 {
				return nil, fmt.Errorf("expected 8 tool results & 1 image, got results=%d images=%d", resultCount, imageCount)
			}
			return e2eActionResponse(domain.RootActionFinish, "Mixed read-only batch complete", "done", ""), nil
		}
		return nil, fmt.Errorf("unexpected call %d", index)
	}

	res, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r1-mixed", "Run mixed read-only batch"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.Execution.Execution.Steps[0].ToolCalls != 8 {
		t.Fatalf("expected 8 calls, got %#v", res.Execution.Execution.Steps[0])
	}
	if checker.MaxInFlight() < 6 {
		t.Fatalf("expected concurrent execution across mixed read-only families, got maxInFlight=%d", checker.MaxInFlight())
	}
}

func TestThroughputE2E_Tier1_R1_CodeIntelReadOnlyToolsConcurrent(t *testing.T) {
	checker := newE2EConcurrencyChecker(6)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	e2eWriteFile(t, h.root, "main.go", "package main\nfunc Compute() int { return 42 }\n")
	mgr := newE2ECodeIntelManager(t, h.workspaceID)
	snap, err := mgr.InitializeServer(t.Context(), h.workspaceID, h.root, "e2e-lsp")
	if err != nil {
		t.Fatalf("init lsp: %v", err)
	}
	h.codeIntel = mgr

	mkPayload := func(tool toolgateway.ToolName) string {
		in := toolgateway.CodeIntelPayload{
			Version: toolgateway.CodeIntelProtocolVersion, ServerID: snap.ServerID,
			ServerGeneration: snap.Generation, CapabilityFingerprint: snap.CapabilityFingerprint,
			Path: "main.go", Limit: 10,
		}
		switch tool {
		case toolgateway.CodeWorkspaceSymbolsTool:
			in.Path, in.Query = "", "Compute"
		case toolgateway.CodeDefinitionTool, toolgateway.CodeReferencesTool, toolgateway.CodeHoverTool:
			in.Line, in.Character = 1, 5
		}
		b, _ := json.Marshal(in)
		return string(b)
	}

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return e2eToolResponse(
				e2eToolCall("ci-1", string(toolgateway.CodeWorkspaceSymbolsTool), mkPayload(toolgateway.CodeWorkspaceSymbolsTool)),
				e2eToolCall("ci-2", string(toolgateway.CodeDocumentSymbolsTool), mkPayload(toolgateway.CodeDocumentSymbolsTool)),
				e2eToolCall("ci-3", string(toolgateway.CodeDefinitionTool), mkPayload(toolgateway.CodeDefinitionTool)),
				e2eToolCall("ci-4", string(toolgateway.CodeReferencesTool), mkPayload(toolgateway.CodeReferencesTool)),
				e2eToolCall("ci-5", string(toolgateway.CodeHoverTool), mkPayload(toolgateway.CodeHoverTool)),
				e2eToolCall("ci-6", string(toolgateway.CodeDiagnosticsTool), mkPayload(toolgateway.CodeDiagnosticsTool)),
			), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "Code intel concurrent batch complete", "done", ""), nil
	}

	res, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r1-codeintel", "Query 6 code_* tools concurrently"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.Execution.Execution.Steps[0].ToolCalls != 6 {
		t.Fatalf("expected 6 code_* calls, got %d", res.Execution.Execution.Steps[0].ToolCalls)
	}
	if checker.MaxInFlight() != 6 {
		t.Fatalf("expected 6 concurrent code_* calls, got maxInFlight=%d", checker.MaxInFlight())
	}
}

func TestThroughputE2E_Tier1_R1_OrderedPersistenceAndEventsAcrossBatch(t *testing.T) {
	checker := newE2EConcurrencyChecker(8)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	for i := 1; i <= 8; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("order-%d.txt", i), fmt.Sprintf("marker-token-%02d\n", i))
	}
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("ord-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("order-%d.txt", i), 1, 2)))
			}
			return e2eToolResponse(calls...), nil
		}
		// Verify model sees results in exact position order 1..8.
		var results []llm.ToolResult
		for _, m := range req.Messages {
			results = append(results, m.ToolResults...)
		}
		if len(results) != 8 {
			return nil, fmt.Errorf("expected 8 tool results, got %d", len(results))
		}
		for i, r := range results {
			want := fmt.Sprintf("marker-token-%02d", i+1)
			if !strings.Contains(r.Content, want) {
				return nil, fmt.Errorf("position %d result missing %q: %s", i+1, want, r.Content)
			}
		}
		return e2eActionResponse(domain.RootActionFinish, "Ordered results verified", "done", ""), nil
	}
	if _, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r1-ordered", "Verify ordered persistence")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 8 {
		t.Fatalf("rounds=%#v err=%v", rounds, err)
	}
	for i, c := range rounds[0].Calls {
		if c.Position != i+1 || c.Status != domain.SupervisorToolCompleted ||
			!strings.Contains(c.ResultJSON, fmt.Sprintf("marker-token-%02d", i+1)) {
			t.Fatalf("persisted call at index %d out of order: %#v", i, c)
		}
	}
	evts, err := h.st.ListRunEvents(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	resIdx := 0
	for _, ev := range evts {
		if ev.Type == events.SupervisorToolResultEvent {
			resIdx++
			if !strings.Contains(ev.PayloadJSON, fmt.Sprintf(`"position":%d`, resIdx)) {
				t.Fatalf("SupervisorToolResultEvent %d out of position order: %s", resIdx, ev.PayloadJSON)
			}
		}
	}
	if resIdx != 8 {
		t.Fatalf("expected 8 SupervisorToolResultEvent records, got %d", resIdx)
	}
}

func TestThroughputE2E_Tier1_R1_WriteToolFormsSerialBarrierBetweenReadBatches(t *testing.T) {
	checker := newE2EConcurrencyChecker(3)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	for i := 1; i <= 6; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("b%d.txt", i), fmt.Sprintf("body-%d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return e2eToolResponse(
				e2eToolCall("r1", "workspace_read", e2eReadPayload("b1.txt", 1, 2)),
				e2eToolCall("r2", "workspace_read", e2eReadPayload("b2.txt", 1, 2)),
				e2eToolCall("r3", "workspace_read", e2eReadPayload("b3.txt", 1, 2)),
				e2eToolCall("w1", "work_item_create", `{"title":"Barrier work item","priority":"normal"}`),
				e2eToolCall("w2", "note_create", `{"title":"Barrier note","content":"serial barrier"}`),
				e2eToolCall("r4", "workspace_read", e2eReadPayload("b4.txt", 1, 2)),
				e2eToolCall("r5", "workspace_read", e2eReadPayload("b5.txt", 1, 2)),
				e2eToolCall("r6", "workspace_read", e2eReadPayload("b6.txt", 1, 2)),
			), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "Serial barrier respected", "done", ""), nil
	}
	if _, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r1-barrier", "Reads with write barrier")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if checker.MaxInFlight() != 3 {
		t.Fatalf("expected read wave concurrency=3, got %d", checker.MaxInFlight())
	}
	if errMsg := checker.ExclusiveOverlapError(); errMsg != "" {
		t.Fatalf("serial barrier violated: %s", errMsg)
	}
}

func TestThroughputE2E_Tier1_R1_ExclusiveToolsRemainSerial(t *testing.T) {
	checker := newE2EConcurrencyChecker(0)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return e2eToolResponse(
				e2eToolCall("e1", "work_item_create", `{"title":"Item 1"}`),
				e2eToolCall("e2", "work_item_create", `{"title":"Item 2"}`),
				e2eToolCall("e3", "note_create", `{"title":"Note 1","content":"c1"}`),
				e2eToolCall("e4", "note_create", `{"title":"Note 2","content":"c2"}`),
			), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "Exclusive tools ran serially", "done", ""), nil
	}
	if _, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r1-exclusive", "Run 4 exclusive tools")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if checker.MaxInFlight() != 1 {
		t.Fatalf("exclusive tools ran concurrently: maxInFlight=%d", checker.MaxInFlight())
	}
	if errMsg := checker.ExclusiveOverlapError(); errMsg != "" {
		t.Fatalf("exclusive overlap: %s", errMsg)
	}
}

func TestThroughputE2E_Tier1_R2_LongNativeLoopExceedsFourRoundsUninterrupted(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 30}, p, nil)
	e2eWriteFile(t, h.root, "target.txt", "line0\n")
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index < 10 {
			if len(req.Tools) == 0 {
				return nil, fmt.Errorf("tools withdrawn prematurely at round %d", index+1)
			}
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("rd-%d", index), "workspace_read", e2eReadPayload("target.txt", 1, 2))), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "Completed 10 native rounds in one segment", "done", ""), nil
	}
	res, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r2-10-rounds", "Execute 10 continuous rounds"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 10 || step.ModelAttempts != 11 || step.ToolBoundary {
		t.Fatalf("unexpected step summary: %#v", step)
	}
	if rollovers := e2eCountBoundaryRollovers(t, h.st, h.run.ID); rollovers != 0 {
		t.Fatalf("expected 0 boundary rollovers for 10 rounds, got %d", rollovers)
	}
	if e2eHasBoundaryPrompt(p.Requests()) {
		t.Fatal("observed artificial scheduling boundary prompt inside 10-round loop")
	}
}

func TestThroughputE2E_Tier1_R2_BudgetLimitsEnforcedAcrossLongSegment(t *testing.T) {
	t.Run("MaxToolCalls_stops_excess_calls", func(t *testing.T) {
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 6}, p, nil)
		e2eWriteFile(t, h.root, "a.txt", "hello\n")
		p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index < 6 {
				return e2eToolResponse(e2eToolCall(fmt.Sprintf("tc-%d", index), "workspace_read", e2eReadPayload("a.txt", 1, 2))), nil
			}
			// On 7th model call (after 6 tool calls consumed), tools should be withdrawn or boundary reached cleanly.
			if len(req.Tools) == 0 {
				return e2eActionResponse(domain.RootActionFinish, "Tool budget reached cleanly", "done", ""), nil
			}
			if index == 6 {
				return e2eToolResponse(e2eToolCall("tc-over", "workspace_read", e2eReadPayload("a.txt", 1, 2))), nil
			}
			return e2eActionResponse(domain.RootActionFinish, "Observed budget exhaustion", "done", ""), nil
		}
		if _, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r2-maxcalls", "Enforce MaxToolCalls=6")); err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		usage, err := h.st.GetToolCallUsage(t.Context(), h.run.ID)
		if err != nil || usage.Consumed != 6 {
			t.Fatalf("tool call budget breached: usage=%#v err=%v", usage, err)
		}
	})

	t.Run("MaxTokens_enforced_past_round_4", func(t *testing.T) {
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxTokens: 56, MaxToolCalls: 20}, p, nil)
		e2eWriteFile(t, h.root, "a.txt", "hello\n")
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("tk-%d", index), "workspace_read", e2eReadPayload("a.txt", 1, 2))), nil
		}
		_, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r2-maxtokens", "Enforce MaxTokens"))
		if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			t.Fatalf("expected CodeResourceExhausted on token budget, got %v", err)
		}
		if len(p.Requests()) <= 4 {
			t.Fatalf("expected budget check to run across >4 rounds before hitting 56 tokens (8 per call = 7 calls), got %d", len(p.Requests()))
		}
	})

	t.Run("MaxTurns_enforced", func(t *testing.T) {
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 1, MaxToolCalls: 20}, p, nil)
		h.startRun(t)
		e2eWriteFile(t, h.root, "a.txt", "hello\n")
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index < 5 {
				return e2eToolResponse(e2eToolCall(fmt.Sprintf("mt-%d", index), "workspace_read", e2eReadPayload("a.txt", 1, 2))), nil
			}
			return e2eActionResponse(domain.RootActionContinue, "First turn finished", "", ""), nil
		}
		if _, err := h.runner().Step(t.Context(), h.run.ID); err != nil {
			t.Fatalf("turn 1 failed: %v", err)
		}
		_, err := h.runner().Step(t.Context(), h.run.ID)
		if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			t.Fatalf("expected CodeResourceExhausted on Turn 2, got %v", err)
		}
	})

	t.Run("TimeoutSeconds_enforced_mid_segment", func(t *testing.T) {
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, TimeoutSeconds: 1, MaxToolCalls: 20}, p, nil)
		e2eWriteFile(t, h.root, "a.txt", "hello\n")
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index == 5 {
				time.Sleep(1150 * time.Millisecond)
			}
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("to-%d", index), "workspace_read", e2eReadPayload("a.txt", 1, 2))), nil
		}
		_, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r2-timeout", "Timeout mid-segment"))
		if apperror.CodeOf(err) != apperror.CodeDeadlineExceeded {
			t.Fatalf("expected CodeDeadlineExceeded, got %v", err)
		}
	})

	t.Run("MaxCostUSD_enforced", func(t *testing.T) {
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxCostUSD: 0.1, MaxToolCalls: 20}, p, nil)
		now := time.Now().UTC()
		snap := pricing.Snapshot{
			ProtocolVersion: pricing.ProtocolVersion, ID: "e2e-test-price-table",
			Source: pricing.SourceOperatorImport, Currency: pricing.CurrencyUSD,
			ImportedBy: "e2e_test", ImportedAt: now,
			ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			Entries: []pricing.Entry{{
				Provider: p.Name(), Model: "model",
				InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000,
			}},
		}
		snap.Fingerprint = pricing.Fingerprint(snap)
		if _, _, err := h.st.ImportPriceSnapshot(t.Context(), snap); err != nil {
			t.Fatal(err)
		}
		e2eWriteFile(t, h.root, "a.txt", "hello\n")
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index < 6 {
				return e2eToolResponse(e2eToolCall(fmt.Sprintf("mc-%d", index), "workspace_read", e2eReadPayload("a.txt", 1, 2))), nil
			}
			return e2eActionResponse(domain.RootActionFinish, "Completed within cost cap", "done", ""), nil
		}
		if _, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r2-cost", "Monetary check across 6 rounds")); err != nil {
			t.Fatalf("execute failed: %v", err)
		}
	})
}

func TestThroughputE2E_Tier1_R2_MidTurnSteeringFenceAcrossLongSegment(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 30}, p, nil)
	e2eWriteFile(t, h.root, "old.txt", "old\n")
	e2eWriteFile(t, h.root, "new.txt", "new\n")
	round6Started := make(chan struct{})
	releaseRound6 := make(chan struct{})

	p.respond = func(ctx context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index < 5:
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("s-%d", index), "workspace_read", e2eReadPayload("old.txt", 1, 2))), nil
		case index == 5:
			close(round6Started)
			select {
			case <-releaseRound6:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return e2eToolResponse(e2eToolCall("s-stale", "workspace_read", e2eReadPayload("old.txt", 1, 2))), nil
		case index == 6:
			sawCorrection := false
			for _, m := range req.Messages {
				if strings.Contains(m.Content, "Switch to new.txt") {
					sawCorrection = true
				}
			}
			if !sawCorrection {
				return nil, errors.New("model did not receive mid-turn steering at round 6")
			}
			return e2eToolResponse(e2eToolCall("s-new", "workspace_read", e2eReadPayload("new.txt", 1, 2))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "Switched cleanly to new.txt", "done", ""), nil
		}
	}

	turns := h.turns()
	done := make(chan error, 1)
	go func() {
		_, err := turns.Execute(context.Background(), h.turnRequest("t1-r2-steer", "Inspect old.txt"))
		done <- err
	}()
	select {
	case <-round6Started:
	case <-time.After(5 * time.Second):
		t.Fatal("round 6 did not start")
	}
	if _, err := application.NewSessionMessageSubmissionService(h.st).Submit(t.Context(), application.SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: h.run.SessionID,
		Content: "Switch to new.txt", OperationKey: "e2e-t1-r2-steer-msg-0001", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	}); err != nil {
		close(releaseRound6)
		t.Fatal(err)
	}
	close(releaseRound6)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("steered turn failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("steered turn timed out")
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	superseded := 0
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.ErrorCode == "steering_superseded" {
				superseded++
			}
		}
	}
	if superseded == 0 {
		t.Fatalf("expected steering_superseded tool call at round 6, rounds=%#v", rounds)
	}
}

func TestThroughputE2E_Tier1_R2_PendingApprovalPauseAndResumeAtRoundSix(t *testing.T) {
	checker := newE2EConcurrencyChecker(0)
	checker.needsReview = func(c tools.Call) bool {
		return c.Name == "replace_file"
	}
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 30}, p, checker)
	sha := e2eWriteFile(t, h.root, "guarded.txt", "v1\n")

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index < 5:
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("r6-%d", index), "workspace_read", e2eReadPayload("guarded.txt", 1, 2))), nil
		case index == 5:
			return e2eToolResponse(e2eToolCall("r6-edit", "workspace_change", e2eChangeReplacePayload("guarded.txt", sha, "v2\n"))), nil
		case index == 6:
			return e2eActionResponse(domain.RootActionWait, "Awaiting review on guarded.txt", "", "operator review required"), nil
		case index == 7:
			return e2eActionResponse(domain.RootActionFinish, "Guarded edit applied after approval", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}

	turns := h.turns()
	if _, err := turns.Execute(t.Context(), h.turnRequest("t1-r2-pause", "Read 5 rounds then edit guarded.txt")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	edits, err := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
		t.Fatalf("expected proposed edit, got %#v err=%v", edits, err)
	}
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: edits[0].ID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	cont := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: edits[0].ID,
	})
	if cont.State != "completed" {
		t.Fatalf("resume approval failed: %#v", cont)
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "guarded.txt"))
	if string(body) != "v2\n" {
		t.Fatalf("expected v2\\n after direct apply, got %q", string(body))
	}
	if e2eHasBoundaryPrompt(p.Requests()) {
		t.Fatal("observed unexpected 4-round boundary prompt")
	}
}

func TestThroughputE2E_Tier1_R2_TwelveRoundsMultiCallThroughput(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 80}, p, nil)
	for i := 1; i <= 4; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("m%d.txt", i), fmt.Sprintf("m%d\n", i))
	}
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index < 12 {
			return e2eToolResponse(
				e2eToolCall(fmt.Sprintf("r%d-1", index), "workspace_read", e2eReadPayload("m1.txt", 1, 2)),
				e2eToolCall(fmt.Sprintf("r%d-2", index), "workspace_read", e2eReadPayload("m2.txt", 1, 2)),
				e2eToolCall(fmt.Sprintf("r%d-3", index), "workspace_read", e2eReadPayload("m3.txt", 1, 2)),
				e2eToolCall(fmt.Sprintf("r%d-4", index), "workspace_read", e2eReadPayload("m4.txt", 1, 2)),
			), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "12 rounds x 4 calls completed uninterrupted", "done", ""), nil
	}
	res, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r2-12x4", "Execute 48 calls across 12 rounds"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 12 || step.ToolCalls != 48 || step.ToolBoundary {
		t.Fatalf("unexpected 12x4 result: %#v", step)
	}
	if e2eCountBoundaryRollovers(t, h.st, h.run.ID) != 0 {
		t.Fatal("unexpected boundary rollover during 12-round segment")
	}
}

func TestThroughputE2E_Tier1_R3_OneStepWorkspaceChangeAutoAndFull(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			p := &e2eScriptedProvider{}
			h := newE2EHarness(t, mode, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, nil)
			existingSHA := e2eWriteFile(t, h.root, "existing.txt", "hello world\n")
			patchSHA := e2eWriteFile(t, h.root, "patch.txt", "alpha\nbeta\ngamma\n")
			moveSHA := e2eWriteFile(t, h.root, "src.txt", "move-content\n")

			p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 0:
					return e2eToolResponse(
						e2eToolCall("c1", "workspace_change", e2eChangeCreatePayload("created.txt", "created-1step\n")),
						e2eToolCall("c2", "workspace_change", e2eChangeReplacePayload("existing.txt", existingSHA, "replaced-1step\n")),
						e2eToolCall("c3", "workspace_change", e2eChangePatchPayload("patch.txt", patchSHA, "beta", "BETA")),
						e2eToolCall("c4", "workspace_change", e2eChangeMovePayload("src.txt", moveSHA, "dst.txt")),
					), nil
				case 1:
					// All 4 workspace_change results must report applied + file_written=true directly.
					var results []llm.ToolResult
					for _, m := range req.Messages {
						results = append(results, m.ToolResults...)
					}
					if len(results) != 4 {
						return nil, fmt.Errorf("expected 4 results, got %d", len(results))
					}
					for i, r := range results {
						if r.IsError {
							return nil, fmt.Errorf("call %d errored: %s", i, r.Content)
						}
						out := e2eExtractResultEnvelopeStdout(t, r.Content)
						if out["status"] != "applied" || out["file_written"] != true {
							return nil, fmt.Errorf("call %d was not 1-step applied: %v", i, out)
						}
					}
					return e2eActionResponse(domain.RootActionFinish, "All 4 workspace_change actions applied in 1 step", "done", ""), nil
				default:
					return nil, fmt.Errorf("unexpected extra round %d", index)
				}
			}

			res, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r3-1step-"+string(mode), "Create, replace, patch, move in 1 step"))
			if err != nil {
				t.Fatalf("execute failed: %v", err)
			}
			if res.Execution.Execution.Steps[0].ToolRounds != 1 || res.Execution.Execution.Steps[0].ToolCalls != 4 {
				t.Fatalf("expected 1 round / 4 calls (zero workspace_apply calls), got %#v", res.Execution.Execution.Steps[0])
			}
			for path, want := range map[string]string{
				"created.txt":  "created-1step\n",
				"existing.txt": "replaced-1step\n",
				"patch.txt":    "alpha\nBETA\ngamma\n",
				"dst.txt":      "move-content\n",
			} {
				got, readErr := os.ReadFile(filepath.Join(h.root, path))
				if readErr != nil || string(got) != want {
					t.Fatalf("file %s = %q (err=%v), want %q", path, string(got), readErr, want)
				}
			}
			if _, statErr := os.Stat(filepath.Join(h.root, "src.txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("move left src.txt behind: %v", statErr)
			}
			edits, err := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
			if err != nil || len(edits) != 4 {
				t.Fatalf("expected 4 audit file_edits, got %#v err=%v", edits, err)
			}
			for _, ed := range edits {
				if ed.Status != fileedit.StatusApplied {
					t.Fatalf("edit %s status=%s want=applied", ed.ID, ed.Status)
				}
				ap, apErr := h.st.GetApprovalByProposal(t.Context(), ed.ID)
				if apErr != nil || ap.Mode != "automatic" || ap.Status != approval.StatusApproved {
					t.Fatalf("edit %s missing automatic approval audit: %#v err=%v", ed.ID, ap, apErr)
				}
			}
		})
	}
}

func TestThroughputE2E_Tier1_R3_WorkspaceApplyBackwardCompatibleReplay(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 10}, p, nil)
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return e2eToolResponse(e2eToolCall("c1", "workspace_change", e2eChangeCreatePayload("compat.txt", "compat\n"))), nil
		case 1:
			var out map[string]any
			for _, m := range req.Messages {
				for _, r := range m.ToolResults {
					out = e2eExtractResultEnvelopeStdout(t, r.Content)
				}
			}
			applyArgs, ok := out["apply_arguments"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("missing backward-compatible apply_arguments: %v", out)
			}
			rawArgs, _ := json.Marshal(applyArgs)
			return e2eToolResponse(e2eToolCall("a1", "workspace_apply", string(rawArgs))), nil
		case 2:
			applyCallIDs := make(map[string]bool)
			for _, m := range req.Messages {
				for _, tc := range m.ToolCalls {
					if tc.Name == "workspace_apply" {
						applyCallIDs[tc.ID] = true
					}
				}
			}
			foundApplyReplay := false
			for _, m := range req.Messages {
				for _, r := range m.ToolResults {
					if !applyCallIDs[r.ToolCallID] {
						continue
					}
					foundApplyReplay = true
					if r.IsError {
						return nil, fmt.Errorf("backward-compatible workspace_apply errored: %s", r.Content)
					}
					out := e2eExtractResultEnvelopeStdout(t, r.Content)
					if out["status"] != "applied" || out["file_written"] != false || out["replayed"] != true {
						return nil, fmt.Errorf("expected idempotent no-write replay, got %v", out)
					}
				}
			}
			if !foundApplyReplay {
				return nil, errors.New("missing workspace_apply tool result")
			}
			return e2eActionResponse(domain.RootActionFinish, "Replay compatibility verified", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	if _, err := h.turns().Execute(t.Context(), h.turnRequest("t1-r3-compat", "Verify workspace_apply replay compatibility")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
}

func TestThroughputE2E_Tier1_R3_ApprovalRequiredChangeDirectGoApplyWithoutSecondToolCall(t *testing.T) {
	checker := newE2EConcurrencyChecker(0)
	checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 15}, p, checker)
	sha := e2eWriteFile(t, h.root, "sensitive.txt", "before\n")

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return e2eToolResponse(e2eToolCall("p1", "workspace_change", e2eChangeReplacePayload("sensitive.txt", sha, "after\n"))), nil
		case 1:
			for _, m := range req.Messages {
				for _, r := range m.ToolResults {
					out := e2eExtractResultEnvelopeStdout(t, r.Content)
					if out["review_required"] != true || out["status"] != "proposed" {
						return nil, fmt.Errorf("expected review_required proposal, got %v", out)
					}
				}
			}
			return e2eActionResponse(domain.RootActionWait, "Waiting for operator approval", "", "approval required"), nil
		case 2:
			var combined strings.Builder
			for _, m := range req.Messages {
				combined.WriteString(m.Content)
			}
			if !strings.Contains(combined.String(), "applied") {
				return nil, fmt.Errorf("approval continuation did not report direct apply status: %s", combined.String())
			}
			return e2eActionResponse(domain.RootActionFinish, "Go applied directly after approval", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d (model must not issue workspace_apply)", index)
		}
	}

	turns := h.turns()
	if _, err := turns.Execute(t.Context(), h.turnRequest("t1-r3-approval", "Edit sensitive.txt")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(h.root, "sensitive.txt")); string(data) != "before\n" {
		t.Fatalf("file modified before approval: %q", string(data))
	}
	edits, err := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits=%#v err=%v", edits, err)
	}
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: edits[0].ID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	cont := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: edits[0].ID,
	})
	if cont.State != "completed" {
		t.Fatalf("continuation=%#v", cont)
	}
	if data, _ := os.ReadFile(filepath.Join(h.root, "sensitive.txt")); string(data) != "after\n" {
		t.Fatalf("expected direct Go apply after approval, got %q", string(data))
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.ToolName == "workspace_apply" {
				t.Fatalf("unexpected workspace_apply call in direct Go apply flow: %#v", c)
			}
		}
	}
}

func TestThroughputE2E_Tier1_R3_WorkspaceDeleteApprovalDirectGoApply(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 15}, p, nil)
	sha := e2eWriteFile(t, h.root, "obsolete.txt", "delete me\n")

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			delPayload, _ := json.Marshal(toolgateway.WorkspaceDeletePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "propose",
				Path: "obsolete.txt", ConfirmPath: "obsolete.txt", ExpectedSHA256: sha,
			})
			return e2eToolResponse(e2eToolCall("d1", "workspace_delete", string(delPayload))), nil
		case 1:
			return e2eActionResponse(domain.RootActionWait, "Approve deletion of obsolete.txt", "", "delete requires approval"), nil
		case 2:
			return e2eActionResponse(domain.RootActionFinish, "File deleted by Go after approval", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}

	turns := h.turns()
	if _, err := turns.Execute(t.Context(), h.turnRequest("t1-r3-delete", "Delete obsolete.txt")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	edits, err := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	if err != nil || len(edits) != 1 || edits[0].Operation != fileedit.OperationDelete {
		t.Fatalf("expected 1 delete proposal, got %#v err=%v", edits, err)
	}
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: edits[0].ID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	cont := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: edits[0].ID,
	})
	if cont.State != "completed" {
		t.Fatalf("delete continuation=%#v", cont)
	}
	if _, statErr := os.Stat(filepath.Join(h.root, "obsolete.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("obsolete.txt still exists after operator approval: %v", statErr)
	}
}

// ============================================================================
// TIER 2: BOUNDARY & CORNER CASES
// ============================================================================

func TestThroughputE2E_Tier2_RejectsNineToolCallsBatch(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, nil)
	for i := 1; i <= 9; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("n%d.txt", i), fmt.Sprintf("v%d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			calls := make([]llm.ToolCall, 0, 9)
			for i := 1; i <= 9; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("nine-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("n%d.txt", i), 1, 2)))
			}
			return e2eToolResponse(calls...), nil
		case 1:
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("eight-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("n%d.txt", i), 1, 2)))
			}
			return e2eToolResponse(calls...), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "Recovered with 8-call batch after 9-call rejection", "done", ""), nil
		}
	}
	res, err := h.turns().Execute(t.Context(), h.turnRequest("t2-9-calls", "Send 9 calls then repair to 8"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ProtocolRepairs != 1 || step.ToolCalls != 8 || step.ToolRounds != 1 {
		t.Fatalf("expected 1 repair then 8 calls, got %#v", step)
	}
}

func TestThroughputE2E_Tier2_ParentContextCancelDuringConcurrentBatch(t *testing.T) {
	entered := make(chan struct{})
	var enteredCount atomic.Int32
	hold := make(chan struct{})
	checker := newE2EConcurrencyChecker(0)
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
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	h.startRun(t)
	for i := 1; i <= 8; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("c%d.txt", i), fmt.Sprintf("c%d\n", i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		calls := make([]llm.ToolCall, 0, 8)
		for i := 1; i <= 8; i++ {
			calls = append(calls, e2eToolCall(fmt.Sprintf("cancel-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("c%d.txt", i), 1, 2)))
		}
		return e2eToolResponse(calls...), nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := h.runner().Step(ctx, h.run.ID)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(hold)
		t.Fatal("concurrent read batch did not reach in-flight state")
	}
	cancel()
	close(hold)
	var stepErr error
	select {
	case stepErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Step did not return after parent cancellation")
	}
	if apperror.CodeOf(stepErr) != apperror.CodeCancelled {
		t.Fatalf("expected CodeCancelled on parent cancel, got %v", stepErr)
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.Status == domain.SupervisorToolCompleted {
				t.Fatalf("cancelled batch persisted completed call %#v", c)
			}
		}
	}
}

func TestThroughputE2E_Tier2_PartialMaxToolCallsExhaustionInBatch(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 3}, p, nil)
	for i := 1; i <= 8; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("p%d.txt", i), fmt.Sprintf("p%d\n", i))
	}
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("pb-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("p%d.txt", i), 1, 2)))
			}
			return e2eToolResponse(calls...), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "Observed partial budget exhaustion", "done", ""), nil
	}
	if _, err := h.turns().Execute(t.Context(), h.turnRequest("t2-partial-budget", "8 reads with budget=3")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	usage, err := h.st.GetToolCallUsage(t.Context(), h.run.ID)
	if err != nil || usage.Consumed != 3 || usage.ExhaustedAt == nil {
		t.Fatalf("expected exactly 3 charged calls & exhausted budget, got %#v err=%v", usage, err)
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 8 {
		t.Fatalf("rounds=%#v err=%v", rounds, err)
	}
	completed, failed := 0, 0
	for _, c := range rounds[0].Calls {
		switch c.Status {
		case domain.SupervisorToolCompleted:
			completed++
		case domain.SupervisorToolFailed:
			failed++
		}
	}
	if completed != 3 || failed != 5 {
		t.Fatalf("expected 3 completed and 5 failed in batch, got completed=%d failed=%d", completed, failed)
	}
}

func TestThroughputE2E_Tier2_SteeringSupersedesMidBatch(t *testing.T) {
	holdSecond := make(chan struct{})
	firstEntered := make(chan struct{})
	var calls atomic.Int32
	checker := newE2EConcurrencyChecker(0)
	checker.onEnter = func(c tools.Call) {
		if c.Name == "work_item_create" && calls.Add(1) == 1 {
			close(firstEntered)
		}
	}
	checker.holdIf = func(c tools.Call) <-chan struct{} {
		if c.Name == "work_item_create" && calls.Load() == 1 {
			return holdSecond
		}
		return nil
	}

	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return e2eToolResponse(
				e2eToolCall("sb-1", "work_item_create", `{"title":"First item"}`),
				e2eToolCall("sb-2", "work_item_create", `{"title":"Superseded item"}`),
				e2eToolCall("sb-3", "note_create", `{"title":"Superseded note","content":"skip"}`),
			), nil
		}
		return e2eActionResponse(domain.RootActionFinish, "Mid-batch steering honored", "done", ""), nil
	}

	turns := h.turns()
	done := make(chan error, 1)
	go func() {
		_, err := turns.Execute(context.Background(), h.turnRequest("t2-steer-midbatch", "Run exclusive batch"))
		done <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		close(holdSecond)
		t.Fatal("first call did not enter")
	}
	if _, err := application.NewSessionMessageSubmissionService(h.st).Submit(t.Context(), application.SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: h.run.SessionID,
		Content: "Stop creating items", OperationKey: "t2-steer-midbatch-msg", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	}); err != nil {
		close(holdSecond)
		t.Fatal(err)
	}
	close(holdSecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("steered batch failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("steered batch timed out")
	}
	items, _ := h.st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: h.run.ID})
	if len(items) != 1 || items[0].Title != "First item" {
		t.Fatalf("expected only First item before supersession, got %#v", items)
	}
	rounds, _ := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	superseded := 0
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.ErrorCode == "steering_superseded" {
				superseded++
			}
		}
	}
	if superseded < 2 {
		t.Fatalf("expected remaining 2 calls superseded mid-batch, got %d", superseded)
	}
}

func TestThroughputE2E_Tier2_CASConflictOnOneStepAndPostApprovalDirectApply(t *testing.T) {
	t.Run("one_step_cas_conflict", func(t *testing.T) {
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 10}, p, nil)
		staleSHA := e2eWriteFile(t, h.root, "race.txt", "original\n")
		p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index == 0 {
				// Simulate external edit before workspace_change executes.
				e2eWriteFile(t, h.root, "race.txt", "external-update\n")
				return e2eToolResponse(e2eToolCall("cas-1", "workspace_change", e2eChangeReplacePayload("race.txt", staleSHA, "model-update\n"))), nil
			}
			hasErr := false
			for _, m := range req.Messages {
				for _, r := range m.ToolResults {
					if r.IsError {
						hasErr = true
					}
				}
			}
			if !hasErr {
				return nil, errors.New("expected CAS conflict error tool result")
			}
			return e2eActionResponse(domain.RootActionFinish, "Observed CAS conflict safely", "done", ""), nil
		}
		if _, err := h.turns().Execute(t.Context(), h.turnRequest("t2-cas-1step", "CAS conflict on 1-step")); err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if got, _ := os.ReadFile(filepath.Join(h.root, "race.txt")); string(got) != "external-update\n" {
			t.Fatalf("external disk change was clobbered: %q", string(got))
		}
	})

	t.Run("post_approval_direct_apply_cas_conflict", func(t *testing.T) {
		checker := newE2EConcurrencyChecker(0)
		checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
		p := &e2eScriptedProvider{}
		h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 10}, p, checker)
		sha := e2eWriteFile(t, h.root, "cas_review.txt", "before\n")
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch index {
			case 0:
				return e2eToolResponse(e2eToolCall("cas-rev", "workspace_change", e2eChangeReplacePayload("cas_review.txt", sha, "after\n"))), nil
			case 1:
				return e2eActionResponse(domain.RootActionWait, "Awaiting approval", "", "review required"), nil
			case 2:
				return e2eActionResponse(domain.RootActionFinish, "Reported CAS failure after approval", "done", ""), nil
			default:
				return nil, fmt.Errorf("unexpected call %d", index)
			}
		}
		turns := h.turns()
		if _, err := turns.Execute(t.Context(), h.turnRequest("t2-cas-review", "Propose then conflict")); err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		edits, _ := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
		if len(edits) != 1 {
			t.Fatalf("edits=%#v", edits)
		}
		// External change on disk before operator approves!
		e2eWriteFile(t, h.root, "cas_review.txt", "external-concurrent-edit\n")
		if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
			Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: edits[0].ID, Action: application.FileEditApproveIntent,
		}); err != nil {
			t.Fatal(err)
		}
		_ = turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
			RunID: h.run.ID, Kind: "file_edit", ProposalID: edits[0].ID,
		})
		if got, _ := os.ReadFile(filepath.Join(h.root, "cas_review.txt")); string(got) != "external-concurrent-edit\n" {
			t.Fatalf("post-approval CAS failure overwrote external file: %q", string(got))
		}
	})
}

func TestThroughputE2E_Tier2_OperatorDenyLeavesFileUntouched(t *testing.T) {
	checker := newE2EConcurrencyChecker(0)
	checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 10}, p, checker)
	sha := e2eWriteFile(t, h.root, "deny.txt", "untouched\n")
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return e2eToolResponse(e2eToolCall("d-1", "workspace_change", e2eChangeReplacePayload("deny.txt", sha, "modified\n"))), nil
		case 1:
			return e2eActionResponse(domain.RootActionWait, "Awaiting review", "", "review"), nil
		case 2:
			return e2eActionResponse(domain.RootActionFinish, "Operator denied edit", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := h.turns()
	if _, err := turns.Execute(t.Context(), h.turnRequest("t2-deny", "Deny proposal")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	edits, _ := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	if len(edits) != 1 {
		t.Fatalf("edits=%#v", edits)
	}
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: edits[0].ID, Action: application.FileEditDeny,
	}); err != nil {
		t.Fatal(err)
	}
	cont := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: edits[0].ID,
	})
	if cont.State != "completed" {
		t.Fatalf("continuation=%#v", cont)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "deny.txt")); string(got) != "untouched\n" {
		t.Fatalf("denied edit modified disk: %q", string(got))
	}
	stored, _ := h.st.GetFileEdit(t.Context(), edits[0].ID)
	if stored.Status != fileedit.StatusDenied {
		t.Fatalf("expected denied edit status, got %s", stored.Status)
	}
}

func TestThroughputE2E_Tier2_PathTraversalSecretAndSymlinkRejectionInOneStep(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"path_traversal", e2eChangeCreatePayload("../escape.txt", "bad\n")},
		{"secret_payload", e2eChangeCreatePayload("leak.txt", "token=sk-"+strings.Repeat("a", 32)+"\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &e2eScriptedProvider{}
			h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 10}, p, nil)
			p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index == 0 {
					return e2eToolResponse(e2eToolCall("bad-1", "workspace_change", tc.payload)), nil
				}
				return e2eActionResponse(domain.RootActionFinish, "Unsafe request rejected", "done", ""), nil
			}
			_, err := h.turns().Execute(t.Context(), h.turnRequest("t2-unsafe-"+tc.name, "Reject unsafe write"))
			if err == nil {
				// Even if model finishes after repair/tool error, verify nothing was written.
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(h.root), "escape.txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("path traversal wrote outside workspace: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(h.root, "leak.txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("secret payload wrote leak.txt: %v", statErr)
			}
			edits, listErr := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
			if listErr != nil || len(edits) != 0 {
				t.Fatalf("unsafe payload persisted file_edit: %#v err=%v", edits, listErr)
			}
		})
	}
}

func TestThroughputE2E_Tier2_SegmentRolloverOver24RoundsAnd32ToolCalls(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 8, MaxToolCalls: 100}, p, nil)
	e2eWriteFile(t, h.root, "seg1.txt", "s1\n")
	e2eWriteFile(t, h.root, "seg2.txt", "s2\n")

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index < 24:
			return e2eToolResponse(
				e2eToolCall(fmt.Sprintf("r%d-a", index), "workspace_read", e2eReadPayload("seg1.txt", 1, 2)),
				e2eToolCall(fmt.Sprintf("r%d-b", index), "workspace_read", e2eReadPayload("seg2.txt", 1, 2)),
			), nil
		case index == 24:
			if len(req.Tools) != 0 {
				return nil, errors.New("expected boundary tool withdrawal at round 24")
			}
			return e2eActionResponse(domain.RootActionContinue, "Rolling over after 24 rounds (48 calls)", "", ""), nil
		case index == 25 || index == 26:
			if len(req.Tools) == 0 {
				return nil, errors.New("tools missing in segment 2 after rollover")
			}
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("s2-%d", index), "workspace_read", e2eReadPayload("seg1.txt", 1, 2))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "Completed 26 rounds / 50 tool calls across rollover", "done", ""), nil
		}
	}

	res, err := h.turns().Execute(t.Context(), h.turnRequest("t2-rollover-24", "Exceed 24 rounds and 32 calls"))
	if err != nil {
		t.Fatalf("execute failed across 24-round rollover (>32 calls): %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 26 || step.ToolCalls != 50 {
		t.Fatalf("expected 26 rounds / 50 calls, got %#v", step)
	}
	if rollovers := e2eCountBoundaryRollovers(t, h.st, h.run.ID); rollovers != 1 {
		t.Fatalf("expected exactly 1 segment rollover at round 24, got %d", rollovers)
	}
}

func TestThroughputE2E_Tier2_PrivateAssistantHistoryEarlyRollover(t *testing.T) {
	p := &e2eScriptedProvider{privateHistory: true}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 8, MaxToolCalls: 40}, p, nil)
	// Set a 16K context window (above ~12.6k base prompt+tools, below 24 full rounds of big reads)
	// so at least 1 tool round runs before private-history context pressure forces tool withdrawal.
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	if err := h.router.SetContextWindow(ref, llm.ContextWindow{
		ProtocolVersion:     llm.ContextWindowProtocolVersion,
		WindowTokens:        16384,
		SafetyMarginTokens:  512,
		DefaultOutputTokens: 512,
		MaxOutputTokens:     1024,
		Source:              "e2e_tight_window",
	}); err != nil {
		t.Fatal(err)
	}
	bigContent := strings.Repeat("line of contextual data for private history pressure\n", 80)
	e2eWriteFile(t, h.root, "big.txt", bigContent)

	withdrewTools := false
	toolRoundsExecuted := 0
	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if len(req.Tools) == 0 {
			withdrewTools = true
			last := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(last, "private-history context window limit") {
				return nil, fmt.Errorf("expected private-history context-pressure prompt, got: %s", last)
			}
			return e2eActionResponse(domain.RootActionFinish, "Finished cleanly after private-history tool withdrawal", "done", ""), nil
		}
		if !withdrewTools && index < 10 {
			toolRoundsExecuted++
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("ph-%d", index), "workspace_read", e2eReadPayload("big.txt", 1, 80))), nil
		}
		return nil, fmt.Errorf("context pressure did not withdraw tools within %d rounds", index)
	}

	res, err := h.turns().Execute(t.Context(), h.turnRequest("t2-private-hist", "Trigger private history context pressure withdrawal"))
	if err != nil {
		t.Fatalf("expected graceful private-history tool withdrawal instead of context limit failure: %v", err)
	}
	if !withdrewTools || toolRoundsExecuted < 1 {
		t.Fatalf("expected >=1 tool round and tool withdrawal under private-history context pressure; withdrewTools=%t toolRounds=%d", withdrewTools, toolRoundsExecuted)
	}
	if res.Execution.Execution.Steps[0].ToolBoundary || res.Execution.Execution.Steps[0].RequestedAction != domain.RootActionFinish {
		t.Fatalf("expected clean finish without boundary rollover, got step=%#v", res.Execution.Execution.Steps[0])
	}
}

// ============================================================================
// TIER 3: CROSS-FEATURE PAIRWISE COMBINATIONS
// ============================================================================

func TestThroughputE2E_Tier3_ParallelEightReadBatchInTenRoundSegmentWithOneStepWrite(t *testing.T) {
	checker := newE2EConcurrencyChecker(8)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 40}, p, checker)
	for i := 1; i <= 8; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("mod%d.go", i), fmt.Sprintf("package mod%d\nvar V%d = %d\n", i, i, i))
	}
	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index == 0:
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("p8-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("mod%d.go", i), 1, 5)))
			}
			return e2eToolResponse(calls...), nil
		case index >= 1 && index <= 8:
			target := fmt.Sprintf("mod%d.go", index)
			sha := fileedit.HashText(fmt.Sprintf("package mod%d\nvar V%d = %d\n", index, index, index))
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("w-%d", index), "workspace_change",
				e2eChangePatchPayload(target, sha, fmt.Sprintf("V%d = %d", index, index), fmt.Sprintf("V%d = %d", index, index*10)))), nil
		case index == 9:
			return e2eToolResponse(e2eToolCall("verify", "workspace_read", e2eReadPayload("mod8.go", 1, 5))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "10-round parallel read + 1-step write complete", "done", ""), nil
		}
	}
	res, err := h.turns().Execute(t.Context(), h.turnRequest("t3-pair-1", "8-read batch + 8x 1-step writes + verify in 10 rounds"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 10 || step.ToolCalls != 17 || checker.MaxInFlight() != 8 {
		t.Fatalf("unexpected pairwise metrics: step=%#v maxInFlight=%d", step, checker.MaxInFlight())
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "mod8.go")); !strings.Contains(string(got), "V8 = 80") {
		t.Fatalf("expected mod8.go patched in 1 step, got %q", string(got))
	}
}

func TestThroughputE2E_Tier3_ConcurrentReadsExclusiveBarrierAndMidTurnSteering(t *testing.T) {
	checker := newE2EConcurrencyChecker(3)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 40}, p, checker)
	for i := 1; i <= 6; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("s%d.txt", i), fmt.Sprintf("s%d\n", i))
	}
	round5Started := make(chan struct{})
	releaseRound5 := make(chan struct{})

	p.respond = func(ctx context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return e2eToolResponse(
				e2eToolCall("r1", "workspace_read", e2eReadPayload("s1.txt", 1, 2)),
				e2eToolCall("r2", "workspace_read", e2eReadPayload("s2.txt", 1, 2)),
				e2eToolCall("r3", "workspace_read", e2eReadPayload("s3.txt", 1, 2)),
				e2eToolCall("wc", "workspace_change", e2eChangeCreatePayload("added.txt", "barrier-ok\n")),
				e2eToolCall("r4", "workspace_read", e2eReadPayload("s4.txt", 1, 2)),
				e2eToolCall("r5", "workspace_read", e2eReadPayload("s5.txt", 1, 2)),
				e2eToolCall("r6", "workspace_read", e2eReadPayload("s6.txt", 1, 2)),
			), nil
		case 1, 2, 3:
			return e2eToolResponse(e2eToolCall(fmt.Sprintf("step-%d", index), "workspace_read", e2eReadPayload("added.txt", 1, 2))), nil
		case 4:
			close(round5Started)
			select {
			case <-releaseRound5:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return e2eToolResponse(e2eToolCall("stale-r5", "workspace_read", e2eReadPayload("s1.txt", 1, 2))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "Steered after concurrent+barrier rounds", "done", ""), nil
		}
	}

	turns := h.turns()
	done := make(chan error, 1)
	go func() {
		_, err := turns.Execute(context.Background(), h.turnRequest("t3-pair-2", "Concurrent reads + barrier + steering"))
		done <- err
	}()
	select {
	case <-round5Started:
	case <-time.After(5 * time.Second):
		close(releaseRound5)
		t.Fatal("round 5 did not start")
	}
	if _, err := application.NewSessionMessageSubmissionService(h.st).Submit(t.Context(), application.SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: h.run.SessionID,
		Content: "Wrap up now", OperationKey: "e2e-t3-pair-2-steer-0001", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	}); err != nil {
		close(releaseRound5)
		t.Fatal(err)
	}
	close(releaseRound5)
	if err := <-done; err != nil {
		t.Fatalf("steered run failed: %v", err)
	}
	if checker.MaxInFlight() != 3 {
		t.Fatalf("expected 3 concurrent reads, got %d", checker.MaxInFlight())
	}
	if errMsg := checker.ExclusiveOverlapError(); errMsg != "" {
		t.Fatalf("barrier violated: %s", errMsg)
	}
}

func TestThroughputE2E_Tier3_MixedAuthorizedOneStepWritesAndApprovalGatedDelete(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, nil)
	delSHA := e2eWriteFile(t, h.root, "temp.txt", "drop me\n")

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			delPayload, _ := json.Marshal(toolgateway.WorkspaceDeletePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "propose",
				Path: "temp.txt", ConfirmPath: "temp.txt", ExpectedSHA256: delSHA,
			})
			return e2eToolResponse(
				e2eToolCall("k1", "workspace_change", e2eChangeCreatePayload("kept.txt", "kept\n")),
				e2eToolCall("d1", "workspace_delete", string(delPayload)),
			), nil
		case 1:
			return e2eActionResponse(domain.RootActionWait, "Approve delete of temp.txt", "", "delete review"), nil
		case 2:
			return e2eActionResponse(domain.RootActionFinish, "kept.txt created in 1-step & temp.txt deleted after approval", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}

	turns := h.turns()
	if _, err := turns.Execute(t.Context(), h.turnRequest("t3-pair-3", "Create kept.txt + delete temp.txt")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	// kept.txt should already be on disk immediately from 1-step authorized write, while temp.txt still exists awaiting review.
	if got, _ := os.ReadFile(filepath.Join(h.root, "kept.txt")); string(got) != "kept\n" {
		t.Fatalf("1-step create did not apply before delete pause: %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(h.root, "temp.txt")); err != nil {
		t.Fatalf("temp.txt deleted before approval: %v", err)
	}
	edits, _ := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	var delEditID string
	for _, ed := range edits {
		if ed.Operation == fileedit.OperationDelete {
			delEditID = ed.ID
		}
	}
	if delEditID == "" {
		t.Fatal("missing delete edit record")
	}
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: delEditID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	cont := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: delEditID,
	})
	if cont.State != "completed" {
		t.Fatalf("continuation=%#v", cont)
	}
	if _, err := os.Stat(filepath.Join(h.root, "temp.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp.txt not deleted after approval: %v", err)
	}
}

// ============================================================================
// TIER 4: REAL-WORLD APPLICATION SCENARIOS
// ============================================================================

func TestThroughputE2E_Tier4_MultiFileInvestigationAndAtomicPatch(t *testing.T) {
	checker := newE2EConcurrencyChecker(8)
	p := &e2eScriptedProvider{vision: llm.VisionSupported}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 30}, p, checker)
	e2eWriteFile(t, h.root, "main.go", "package main\nfunc Compute() int { return 1 }\n")
	mathSHA := e2eWriteFile(t, h.root, "pkg/math.go", "package pkg\nfunc Add(a, b int) int { return a - b }\n")
	e2eWriteFile(t, h.root, "pkg/math_test.go", "package pkg\n// test Add\n")
	e2eWriteFile(t, h.root, "README.md", "# Service\n")
	if err := os.WriteFile(filepath.Join(h.root, "arch.png"), e2eMakePNG(t, 55), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := newE2ECodeIntelManager(t, h.workspaceID)
	snap, err := mgr.InitializeServer(t.Context(), h.workspaceID, h.root, "e2e-lsp")
	if err != nil {
		t.Fatal(err)
	}
	h.codeIntel = mgr
	symPayload, _ := json.Marshal(toolgateway.CodeIntelPayload{
		Version: toolgateway.CodeIntelProtocolVersion, ServerID: snap.ServerID,
		ServerGeneration: snap.Generation, CapabilityFingerprint: snap.CapabilityFingerprint,
		Path: "main.go", Limit: 10,
	})

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			// Wave 1: 8-way parallel multi-file investigation (glob + grep + 4 reads + code_document_symbols + image).
			return e2eToolResponse(
				e2eToolCall("inv-1", "workspace_glob", `{"version":"agent-code-tools.v1","pattern":"**/*.go","limit":20}`),
				e2eToolCall("inv-2", "workspace_grep", `{"version":"agent-code-tools.v1","query":"Add(","pattern":"*.go","limit":20,"case_sensitive":true}`),
				e2eToolCall("inv-3", "workspace_read", e2eReadPayload("main.go", 1, 10)),
				e2eToolCall("inv-4", "workspace_read", e2eReadPayload("pkg/math.go", 1, 10)),
				e2eToolCall("inv-5", "workspace_read", e2eReadPayload("pkg/math_test.go", 1, 10)),
				e2eToolCall("inv-6", "workspace_read", e2eReadPayload("README.md", 1, 10)),
				e2eToolCall("inv-7", string(toolgateway.CodeDocumentSymbolsTool), string(symPayload)),
				e2eToolCall("inv-8", "workspace_view_image", `{"version":"agent-code-tools.v1","path":"arch.png"}`),
			), nil
		case 1:
			// Wave 2: 1-step atomic patches (no workspace_apply round-trip).
			return e2eToolResponse(
				e2eToolCall("fix-1", "workspace_change", e2eChangePatchPayload("pkg/math.go", mathSHA, "return a - b", "return a + b")),
				e2eToolCall("fix-2", "workspace_change", e2eChangeCreatePayload("pkg/extra.go", "package pkg\nfunc Mul(a, b int) int { return a * b }\n")),
			), nil
		case 2:
			// Wave 3: verification read.
			return e2eToolResponse(e2eToolCall("ver-1", "workspace_read", e2eReadPayload("pkg/math.go", 1, 10))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "Investigated, patched, and verified in 3 rounds", "done", ""), nil
		}
	}

	res, err := h.turns().Execute(t.Context(), h.turnRequest("t4-multifile", "Investigate repo, fix Add bug, add Mul, verify"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 3 || step.ToolCalls != 11 || checker.MaxInFlight() != 8 {
		t.Fatalf("expected 3 rounds / 11 calls / maxInFlight=8, got step=%#v maxInFlight=%d", step, checker.MaxInFlight())
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "pkg/math.go")); !strings.Contains(string(got), "return a + b") {
		t.Fatalf("pkg/math.go not fixed: %q", string(got))
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "pkg/extra.go")); !strings.Contains(string(got), "func Mul") {
		t.Fatalf("pkg/extra.go not created: %q", string(got))
	}
}

func TestThroughputE2E_Tier4_IterativeEightRoundDebugFixCycle(t *testing.T) {
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 40}, p, nil)
	sha := e2eWriteFile(t, h.root, "app.go", "package main\nvar step = 0\n")

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return e2eToolResponse(e2eToolCall("d0", "workspace_list", `{"version":"agent-code-tools.v1","path":".","limit":20}`)), nil
		case 1:
			return e2eToolResponse(e2eToolCall("d1", "workspace_read", e2eReadPayload("app.go", 1, 5))), nil
		case 2:
			next := "package main\nvar step = 1\n"
			res := e2eToolResponse(e2eToolCall("d2", "workspace_change", e2eChangeReplacePayload("app.go", sha, next)))
			sha = fileedit.HashText(next)
			return res, nil
		case 3:
			return e2eToolResponse(e2eToolCall("d3", "workspace_read", e2eReadPayload("app.go", 1, 5))), nil
		case 4:
			next := "package main\nvar step = 2\n"
			res := e2eToolResponse(e2eToolCall("d4", "workspace_change", e2eChangeReplacePayload("app.go", sha, next)))
			sha = fileedit.HashText(next)
			return res, nil
		case 5:
			return e2eToolResponse(e2eToolCall("d5", "workspace_read", e2eReadPayload("app.go", 1, 5))), nil
		case 6:
			next := "package main\nvar step = 3\n"
			res := e2eToolResponse(e2eToolCall("d6", "workspace_change", e2eChangeReplacePayload("app.go", sha, next)))
			sha = fileedit.HashText(next)
			return res, nil
		case 7:
			return e2eToolResponse(e2eToolCall("d7", "workspace_read", e2eReadPayload("app.go", 1, 5))), nil
		case 8:
			return e2eToolResponse(e2eToolCall("d8", "workspace_change", e2eChangeMovePayload("app.go", sha, "app_v3.go"))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "9-round debug/fix cycle completed seamlessly", "done", ""), nil
		}
	}

	res, err := h.turns().Execute(t.Context(), h.turnRequest("t4-debug-9", "Iterate through 9 debug/fix rounds"))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	step := res.Execution.Execution.Steps[0]
	if step.ToolRounds != 9 || step.ToolCalls != 9 || step.ToolBoundary {
		t.Fatalf("expected 9 uninterrupted rounds, got %#v", step)
	}
	if e2eHasBoundaryPrompt(p.Requests()) || e2eCountBoundaryRollovers(t, h.st, h.run.ID) != 0 {
		t.Fatal("observed artificial 4-round stall inside 9-round debug/fix cycle")
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "app_v3.go")); string(got) != "package main\nvar step = 3\n" {
		t.Fatalf("final file content=%q", string(got))
	}
}

func TestThroughputE2E_Tier4_ConcurrentExplorationRecoveredAfterSteeringCorrection(t *testing.T) {
	checker := newE2EConcurrencyChecker(8)
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 40}, p, checker)
	for i := 1; i <= 8; i++ {
		e2eWriteFile(t, h.root, fmt.Sprintf("old_%d.txt", i), fmt.Sprintf("old %d\n", i))
	}
	targetSHA := e2eWriteFile(t, h.root, "real_target.txt", "before\n")
	round2Started := make(chan struct{})
	releaseRound2 := make(chan struct{})

	p.respond = func(ctx context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, e2eToolCall(fmt.Sprintf("exp-%d", i), "workspace_read", e2eReadPayload(fmt.Sprintf("old_%d.txt", i), 1, 2)))
			}
			return e2eToolResponse(calls...), nil
		case 1:
			close(round2Started)
			select {
			case <-releaseRound2:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return e2eToolResponse(e2eToolCall("wrong-edit", "workspace_change", e2eChangeCreatePayload("wrong.txt", "wrong\n"))), nil
		case 2:
			return e2eToolResponse(e2eToolCall("right-edit", "workspace_change", e2eChangeReplacePayload("real_target.txt", targetSHA, "corrected\n"))), nil
		default:
			return e2eActionResponse(domain.RootActionFinish, "Recovered and edited real_target.txt", "done", ""), nil
		}
	}

	turns := h.turns()
	done := make(chan error, 1)
	go func() {
		_, err := turns.Execute(context.Background(), h.turnRequest("t4-steer-recover", "Explore and update target"))
		done <- err
	}()
	select {
	case <-round2Started:
	case <-time.After(5 * time.Second):
		close(releaseRound2)
		t.Fatal("round 2 did not start")
	}
	if _, err := application.NewSessionMessageSubmissionService(h.st).Submit(t.Context(), application.SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: h.run.SessionID,
		Content: "Do not create wrong.txt; update real_target.txt instead", OperationKey: "e2e-t4-steer-message-0001", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	}); err != nil {
		close(releaseRound2)
		t.Fatal(err)
	}
	close(releaseRound2)
	if err := <-done; err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if checker.MaxInFlight() != 8 {
		t.Fatalf("expected 8 concurrent initial reads, got %d", checker.MaxInFlight())
	}
	if _, statErr := os.Stat(filepath.Join(h.root, "wrong.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("superseded edit created wrong.txt: %v", statErr)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "real_target.txt")); string(got) != "corrected\n" {
		t.Fatalf("real_target.txt=%q", string(got))
	}
}

func TestThroughputE2E_Tier4_WorkspaceConflictRecoveryAfterExternalChange(t *testing.T) {
	checker := newE2EConcurrencyChecker(0)
	checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
	p := &e2eScriptedProvider{}
	h := newE2EHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 6, MaxToolCalls: 20}, p, checker)
	sha1 := e2eWriteFile(t, h.root, "shared.txt", "line1\n")

	p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return e2eToolResponse(e2eToolCall("p1", "workspace_change", e2eChangeReplacePayload("shared.txt", sha1, "line1-model\n"))), nil
		case 1:
			return e2eActionResponse(domain.RootActionWait, "Review edit to shared.txt", "", "review required"), nil
		case 2:
			// After CAS conflict during direct Go apply, model re-reads shared.txt to recover.
			return e2eToolResponse(e2eToolCall("reread", "workspace_read", e2eReadPayload("shared.txt", 1, 5))), nil
		case 3:
			sha2 := fileedit.HashText("line1\nexternal\n")
			return e2eToolResponse(e2eToolCall("p2", "workspace_change", e2eChangeReplacePayload("shared.txt", sha2, "line1-merged\nexternal\n"))), nil
		case 4:
			return e2eActionResponse(domain.RootActionWait, "Review updated merge proposal", "", "review required"), nil
		case 5:
			return e2eActionResponse(domain.RootActionFinish, "Conflict recovered cleanly", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}

	turns := h.turns()
	if _, err := turns.Execute(t.Context(), h.turnRequest("t4-conflict-rec", "Update shared.txt")); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	edits, _ := h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	firstID := edits[0].ID
	// External modification causes CAS conflict on first approval.
	e2eWriteFile(t, h.root, "shared.txt", "line1\nexternal\n")
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: firstID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	cont1 := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: firstID,
	})
	if cont1.State != "completed" {
		t.Fatalf("first continuation=%#v", cont1)
	}
	edits, _ = h.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: h.run.SessionID})
	var secondID string
	for _, ed := range edits {
		if ed.ID != firstID {
			secondID = ed.ID
		}
	}
	if secondID == "" {
		t.Fatal("model did not propose recovery edit after conflict")
	}
	if _, err := application.NewFileEditReviewService(h.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: h.run.ID, EditID: secondID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	cont2 := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{
		RunID: h.run.ID, Kind: "file_edit", ProposalID: secondID,
	})
	if cont2.State != "completed" {
		t.Fatalf("second continuation=%#v", cont2)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "shared.txt")); string(got) != "line1-merged\nexternal\n" {
		t.Fatalf("expected merged recovery content, got %q", string(got))
	}
}
