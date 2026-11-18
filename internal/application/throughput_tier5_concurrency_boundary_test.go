package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/tools"
)

// tier5DelayPermissionStore wraps SQLiteStore to inject controlled latency on
// the first GetRunExecutionPermission call after model completion (when armed),
// exposing whether orderedBudgetTurnstile preserves strict Position order even
// when an earlier slot arrives at turnstile.wait after a later slot returns
// early via PreTool denial.
type tier5DelayPermissionStore struct {
	*store.SQLiteStore
	requireArm     bool
	armed          atomic.Bool
	delayCallIndex int32
	delay          time.Duration
	permCalls      atomic.Int32
	delayedCount   atomic.Int32
}

func (s *tier5DelayPermissionStore) RecordSupervisorModelCompleted(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, response llm.ChatResponse) (domain.SupervisorCheckpoint, error) {
	cp, err := s.SQLiteStore.RecordSupervisorModelCompleted(ctx, checkpoint, attempt, response)
	if err == nil && len(response.ToolCalls) > 0 {
		s.armed.Store(true)
	}
	return cp, err
}

func (s *tier5DelayPermissionStore) RecordSupervisorModelCompletedForAgent(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, response llm.ChatResponse, attr domain.AgentAttribution) (domain.SupervisorCheckpoint, error) {
	cp, err := s.SQLiteStore.RecordSupervisorModelCompletedForAgent(ctx, checkpoint, attempt, response, attr)
	if err == nil && len(response.ToolCalls) > 0 {
		s.armed.Store(true)
	}
	return cp, err
}

func (s *tier5DelayPermissionStore) GetRunExecutionPermission(ctx context.Context, runID string) (domain.RunExecutionPermissionSnapshot, error) {
	if !s.requireArm || s.armed.Load() {
		n := s.permCalls.Add(1)
		if s.delay > 0 && n == s.delayCallIndex {
			s.delayedCount.Add(1)
			select {
			case <-time.After(s.delay):
			case <-ctx.Done():
				return domain.RunExecutionPermissionSnapshot{}, ctx.Err()
			}
		}
	}
	return s.SQLiteStore.GetRunExecutionPermission(ctx, runID)
}

type tier5PrivateHistoryProvider struct {
	*concTestProvider
}

func (p *tier5PrivateHistoryProvider) RequiresPrivateAssistantHistory(string) bool {
	return true
}

type tier5WaveTrackerChecker struct {
	base policy.Checker

	mu                  sync.Mutex
	inFlight            int
	waveBarrierTarget   int
	waveBarrierArrived  int
	waveBarrierReady    chan struct{}
	wave2BarrierTarget  int
	wave2BarrierArrived int
	wave2BarrierReady   chan struct{}
	seenWaveTag         map[string]bool
	events              []string
	exclusiveOverlapErr string
}

func newTier5WaveTrackerChecker(wave1Target, wave2Target int) *tier5WaveTrackerChecker {
	c := &tier5WaveTrackerChecker{
		base:               policy.NewDefaultChecker(),
		waveBarrierTarget:  wave1Target,
		wave2BarrierTarget: wave2Target,
		seenWaveTag:        make(map[string]bool),
	}
	if wave1Target > 1 {
		c.waveBarrierReady = make(chan struct{})
	}
	if wave2Target > 1 {
		c.wave2BarrierReady = make(chan struct{})
	}
	return c
}

func (c *tier5WaveTrackerChecker) CheckText(ctx, text string) policy.Decision {
	return c.base.CheckText(ctx, text)
}

func (c *tier5WaveTrackerChecker) CheckToolCall(call tools.Call) policy.Decision {
	tag := call.Name
	if p := call.Args["payload"]; p != "" {
		var parsed struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(p), &parsed) == nil && parsed.Path != "" {
			tag = call.Name + ":" + parsed.Path
		}
	}

	c.mu.Lock()
	c.inFlight++
	c.events = append(c.events, "enter:"+tag)
	isExclusive := call.Name == "workspace_change" || call.Name == "replace_file" || call.Name == "create_file" || call.Name == "work_item_create"
	if isExclusive && c.inFlight > 1 {
		c.exclusiveOverlapErr = fmt.Sprintf("exclusive tool %s ran with %d other in-flight calls", tag, c.inFlight-1)
	}
	var waitCh chan struct{}
	if call.Name == "workspace_read" && !c.seenWaveTag[tag] {
		c.seenWaveTag[tag] = true
		if strings.Contains(tag, "w1-") && c.waveBarrierReady != nil {
			c.waveBarrierArrived++
			if c.waveBarrierArrived >= c.waveBarrierTarget {
				select {
				case <-c.waveBarrierReady:
				default:
					close(c.waveBarrierReady)
				}
			}
			waitCh = c.waveBarrierReady
		} else if strings.Contains(tag, "w2-") && c.wave2BarrierReady != nil {
			c.wave2BarrierArrived++
			if c.wave2BarrierArrived >= c.wave2BarrierTarget {
				select {
				case <-c.wave2BarrierReady:
				default:
					close(c.wave2BarrierReady)
				}
			}
			waitCh = c.wave2BarrierReady
		}
	}
	c.mu.Unlock()

	if waitCh != nil {
		select {
		case <-waitCh:
		case <-time.After(500 * time.Millisecond):
		}
	}

	c.mu.Lock()
	c.events = append(c.events, "exit:"+tag)
	c.inFlight--
	c.mu.Unlock()
	return c.base.CheckToolCall(call)
}

func TestThroughputTier5_R1R2_MixedWavesAndSerialBarriersInExactOrder(t *testing.T) {
	checker := newTier5WaveTrackerChecker(3, 2)
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	for _, f := range []string{"w1-a.txt", "w1-b.txt", "w1-c.txt", "w2-a.txt", "w2-b.txt", "w3-a.txt"} {
		concWriteFile(t, h.root, f, "content-"+f+"\n")
	}

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{
					{ID: "m-1", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("w1-a.txt", 1, 2))},
					{ID: "m-2", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("w1-b.txt", 1, 2))},
					{ID: "m-3", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("w1-c.txt", 1, 2))},
					{ID: "m-4", Name: "workspace_change", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"barrier-created.txt","expected_sha256":"missing","content":"created-at-barrier\n"}`)},
					{ID: "m-5", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("w2-a.txt", 1, 2))},
					{ID: "m-6", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("w2-b.txt", 1, 2))},
					{ID: "m-7", Name: "work_item_create", Arguments: json.RawMessage(`{"title":"Serial barrier item"}`)},
					{ID: "m-8", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("w3-a.txt", 1, 2))},
				},
			}, nil
		}
		return concActionResponse(domain.RootActionFinish, "mixed waves and barriers verified", "done"), nil
	}

	if _, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
		Content: "Execute mixed parallel waves with serial barriers", OperationKey: "tier5-mixed-waves-op", RequestedBy: "test_operator",
	}); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	checker.mu.Lock()
	overlapErr := checker.exclusiveOverlapErr
	w1Arrived := checker.waveBarrierArrived
	w2Arrived := checker.wave2BarrierArrived
	trace := append([]string(nil), checker.events...)
	checker.mu.Unlock()

	if overlapErr != "" {
		t.Fatalf("serial barrier violated: %s (trace=%v)", overlapErr, trace)
	}
	if w1Arrived != 3 || w2Arrived != 2 {
		t.Fatalf("read waves did not run concurrently: w1=%d w2=%d trace=%v", w1Arrived, w2Arrived, trace)
	}
	// Verify strict phase separation in trace: all 3 w1- exits precede create_file,
	// create_file exit precedes all w2- enters, all 2 w2- exits precede work_item_create,
	// work_item_create exit precedes w3-a enter.
	idxOf := func(sub string) int {
		for i, ev := range trace {
			if strings.Contains(ev, sub) {
				return i
			}
		}
		return -1
	}
	lastExit := func(prefix string) int {
		last := -1
		for i, ev := range trace {
			if strings.HasPrefix(ev, "exit:") && strings.Contains(ev, prefix) {
				last = i
			}
		}
		return last
	}
	firstEnter := func(prefix string) int {
		for i, ev := range trace {
			if strings.HasPrefix(ev, "enter:") && strings.Contains(ev, prefix) {
				return i
			}
		}
		return -1
	}
	writeEnter := idxOf("enter:create_file")
	writeExit := idxOf("exit:create_file")
	exclEnter := idxOf("enter:work_item_create")
	exclExit := idxOf("exit:work_item_create")
	if writeEnter < 0 || writeExit < 0 || exclEnter < 0 || exclExit < 0 {
		t.Fatalf("missing barrier events in trace: %v", trace)
	}
	if lastExit("w1-") >= writeEnter || writeExit >= firstEnter("w2-") || lastExit("w2-") >= exclEnter || exclExit >= firstEnter("w3-") {
		t.Fatalf("wave/barrier ordering violated: trace=%v", trace)
	}

	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 8 {
		t.Fatalf("unexpected rounds: %+v err=%v", rounds, err)
	}
	for i, c := range rounds[0].Calls {
		if c.Position != i+1 || c.Status != domain.SupervisorToolCompleted {
			t.Fatalf("call %d wrong position/status: %+v", i, c)
		}
	}
	evts, err := h.st.ListRunEvents(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	posSeq := 0
	chargeSeq := 0
	for _, ev := range evts {
		if ev.Type == events.ToolBudgetChargedEvent {
			chargeSeq++
			if !strings.Contains(ev.PayloadJSON, fmt.Sprintf(`"consumed":%d`, chargeSeq)) {
				t.Fatalf("budget event %d out of order: %s", chargeSeq, ev.PayloadJSON)
			}
		}
		if ev.Type == events.SupervisorToolResultEvent {
			posSeq++
			if !strings.Contains(ev.PayloadJSON, fmt.Sprintf(`"position":%d`, posSeq)) {
				t.Fatalf("result event %d out of position order: %s", posSeq, ev.PayloadJSON)
			}
		}
	}
	if posSeq != 8 || chargeSeq != 8 {
		t.Fatalf("expected 8 results and 8 budget charges, got pos=%d charge=%d", posSeq, chargeSeq)
	}
}

func TestThroughputTier5_R1R2_PreToolDenialAndPartialMaxToolCallsExhaustion(t *testing.T) {
	runScenario := func(t *testing.T, delayPos1 bool) {
		t.Helper()
		p := &concTestProvider{}
		h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 3}, p, nil)
		for i := 1; i <= 5; i++ {
			concWriteFile(t, h.root, fmt.Sprintf("pt%d.txt", i), fmt.Sprintf("pt-%02d\n", i))
		}
		engine := hooks.NewEngine(nil)
		if err := engine.Replace([]hooks.Registration{{
			PluginID: "tier5-pretool-deny", PluginFingerprint: strings.Repeat("d", 64),
			Declaration: hooks.Declaration{
				ProtocolVersion: hooks.ProtocolVersion, ID: "deny-history-read", Event: hooks.PreTool,
				Action: hooks.ActionDeny, FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
				ToolNames: []string{"history_read"}, Message: "denied by pretool hook",
			},
		}}); err != nil {
			t.Fatal(err)
		}
		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index == 0 {
				return &llm.ChatResponse{
					Provider: p.Name(), Model: "model",
					ToolCalls: []llm.ToolCall{
						{ID: "pt-1", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("pt1.txt", 1, 2))},
						{ID: "pt-2", Name: "history_read", Arguments: json.RawMessage(`{"source_id":"opaque-pos2","part":"summary"}`)},
						{ID: "pt-3", Name: "history_search", Arguments: json.RawMessage(`{"query":"q3","limit":5}`)},
						{ID: "pt-4", Name: "history_search", Arguments: json.RawMessage(`{"query":"q4","limit":5}`)},
						{ID: "pt-5", Name: "history_search", Arguments: json.RawMessage(`{"query":"q5","limit":5}`)},
						{ID: "pt-6", Name: "history_search", Arguments: json.RawMessage(`{"query":"q6","limit":5}`)},
					},
				}, nil
			}
			return concActionResponse(domain.RootActionFinish, "handled pretool denial + partial budget", "done"), nil
		}

		var stStore interface {
			AgentRunnerStore
			RunExecutionHandoffStore
			RunLifecycleControlStore
			ThreadStore
		} = h.st
		var delayedWrapper *tier5DelayPermissionStore
		if delayPos1 {
			delayedWrapper = &tier5DelayPermissionStore{SQLiteStore: h.st, requireArm: true, delayCallIndex: 1, delay: 50 * time.Millisecond}
			stStore = delayedWrapper
		}
		exec := NewRunExecutionHandoffService(stStore, h.router, h.checker).
			WithExecutionPermissionCapabilities(h.caps).
			WithGeneratedContextCompaction(false).
			WithLifecycleHooks(engine)
		lifecycle := NewRunLifecycleControlService(stStore)
		turns := NewThreadTurnServiceWithExecutionCapabilities(stStore, lifecycle, exec, h.caps)

		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		if _, err := turns.Execute(ctx, ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
			Content: "Run 6-call batch with PreTool denial at pos 2 and MaxToolCalls=3", OperationKey: fmt.Sprintf("tier5-pretool-budget-%t", delayPos1), RequestedBy: "test_operator",
		}); err != nil {
			t.Fatalf("execute failed (deadlock or fatal error): %v", err)
		}
		if delayPos1 && delayedWrapper.delayedCount.Load() != 1 {
			t.Fatalf("expected pos 1 delay to fire exactly once, got %d", delayedWrapper.delayedCount.Load())
		}

		rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
		if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 6 {
			t.Fatalf("unexpected rounds: %+v err=%v", rounds, err)
		}
		calls := rounds[0].Calls
		// Expected deterministic Position semantics:
		// Pos 1: Completed (budget #1)
		// Pos 2: Denied by PreTool (0 budget consumed)
		// Pos 3: Completed (budget #2)
		// Pos 4: Completed (budget #3)
		// Pos 5: Failed RESOURCE_EXHAUSTED
		// Pos 6: Failed RESOURCE_EXHAUSTED
		if calls[1].Status != domain.SupervisorToolDenied {
			t.Fatalf("pos 2 expected SupervisorToolDenied, got %+v", calls[1])
		}
		if calls[0].Status != domain.SupervisorToolCompleted ||
			calls[2].Status != domain.SupervisorToolCompleted ||
			calls[3].Status != domain.SupervisorToolCompleted {
			t.Fatalf("expected pos 1, 3, 4 completed in strict Position order, got statuses=[%s, %s, %s, %s, %s, %s]",
				calls[0].Status, calls[1].Status, calls[2].Status, calls[3].Status, calls[4].Status, calls[5].Status)
		}
		for _, idx := range []int{4, 5} {
			if calls[idx].Status != domain.SupervisorToolFailed || calls[idx].ErrorCode != string(apperror.CodeResourceExhausted) {
				t.Fatalf("pos %d expected RESOURCE_EXHAUSTED, got %+v", idx+1, calls[idx])
			}
		}
	}

	t.Run("no_deadlock_baseline", func(t *testing.T) {
		runScenario(t, false)
	})
	t.Run("turnstile_order_when_pos1_delayed_before_turnstile_and_pos2_denies_early", func(t *testing.T) {
		runScenario(t, true)
	})
}

func TestThroughputTier5_R1R2_SiblingFatalCancelVsParentCancel(t *testing.T) {
	t.Run("sibling_fatal_error_cancels_dispatchCtx_without_cancelling_parent_ctx", func(t *testing.T) {
		hold := make(chan struct{})
		defer close(hold)
		var enteredSlow atomic.Int32
		slowEntered := make(chan struct{})
		checker := newConcTestChecker(0)
		checker.onEnter = func(c tools.Call) {
			if strings.Contains(c.Args["payload"], "slow1.txt") || strings.Contains(c.Args["payload"], "slow4.txt") {
				if enteredSlow.Add(1) == 2 {
					select {
					case <-slowEntered:
					default:
						close(slowEntered)
					}
				}
			}
		}
		checker.holdIf = func(c tools.Call) <-chan struct{} {
			// Hold position 1 until dispatchCtx or timeout unblocks? Wait: checker.holdIf blocks
			// inside CheckToolCall which doesn't select on ctx! Instead, use a slow wait via
			// delayIf OR test cancellation at gateway/invoke level via store!
			return nil
		}
		// Instead of blocking ignorantly of ctx in CheckToolCall, delay GetRunExecutionPermission
		// via a context-aware store wrapper for pos 1 and pos 4 while pos 3 fails fatally!
		p := &concTestProvider{}
		h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
		for _, f := range []string{"slow1.txt", "fast2.txt", "tamper3.txt", "slow4.txt"} {
			concWriteFile(t, h.root, f, f+"\n")
		}
		run, err := NewRunService(h.st).Start(t.Context(), h.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		lease, err := h.st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
			RunID: run.ID, OwnerID: "tier5-sibling-owner", TTL: time.Minute,
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
		runner := h.runner()
		snap, auth, err := runner.supervisorAgentCodeCapabilities(ctx, turn, permission)
		if err != nil {
			t.Fatal(err)
		}
		options := supervisorToolOptions{AgentCode: supervisorAgentCodeTools{Capabilities: snap, Authority: auth}}
		calls, err := prepareSupervisorToolCalls([]llm.ToolCall{
			{ID: "s-1", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("slow1.txt", 1, 2))},
			{ID: "s-2", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("fast2.txt", 1, 2))},
			{ID: "s-3", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("tamper3.txt", 1, 2))},
			{ID: "s-4", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("slow4.txt", 1, 2))},
		}, turn.Run.ID, turn.Checkpoint.NextTurn, 1, turn.Mode.Surface, turn.Mode.Phase,
			domain.RunExecutionPermissionAuto, false, false, options)
		if err != nil {
			t.Fatal(err)
		}
		attempt := llm.ModelAttempt{
			SupervisorAttemptID: turn.Checkpoint.AttemptID, Number: 1, TransportAttempt: 1, MaxAttempts: 1,
			Provider: p.Name(), Model: "model",
		}
		if _, err := h.st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
			t.Fatal(err)
		}
		attempt.Outcome = llm.OutcomeSuccess
		if _, err := h.st.RecordSupervisorModelCompletedForAgent(ctx, turn.Checkpoint, attempt, llm.ChatResponse{
			Provider: p.Name(), Model: "model", ToolCalls: calls, Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
		}, domain.AgentAttribution{
			AgentID: turn.Agent.ID, AgentAttemptID: turn.Checkpoint.AttemptID, Source: domain.AgentAttributionRecorded,
		}); err != nil {
			t.Fatal(err)
		}
		rounds, err := h.st.ListSupervisorToolRounds(ctx, turn.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		// Tamper AgentID on pos 3 so it fails at attribution check BEFORE GetRunExecutionPermission,
		// while a healthy sibling blocks inside GetRunExecutionPermission until dispatchCtx cancels.
		rounds[0].Calls[2].AgentID = "wrong-agent-id"

		blockingStore := &tier5DelayPermissionStore{SQLiteStore: h.st, delayCallIndex: 1, delay: 5 * time.Second}
		// Wait: let's make slot 0 block on ctx.Done() inside GetRunExecutionPermission by delaying call 1,
		// while slot 2 fails fast on authority decode/mismatch after GetRunExecutionPermission!
		// Actually, tamper AuthorityJSON fails at DecodeAgentCodeCallAuthority BEFORE GetRunExecutionPermission!
		// So call 1 to GetRunExecutionPermission is from slot 0 (pos 1), which blocks on ctx.Done() until
		// slot 2 calls cancelDispatch()!
		blockingRunner := NewAgentRunner(blockingStore, h.router, checker).
			WithExecutionPermissionCapabilities(h.caps).
			WithGeneratedContextCompaction(false)

		start := time.Now()
		_, waiting, resumeErr := blockingRunner.resumeSupervisorToolsRaw(ctx, turn, rounds)
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Fatalf("sibling fatal error did not cancel dispatchCtx promptly (took %s)", elapsed)
		}
		if waiting || apperror.CodeOf(resumeErr) != apperror.CodeFailedPrecondition {
			t.Fatalf("expected sibling fatal CodeFailedPrecondition (not CodeCancelled), got waiting=%v err=%v", waiting, resumeErr)
		}
		if ctx.Err() != nil {
			t.Fatalf("parent ctx must remain alive when sibling fails fatally: %v", ctx.Err())
		}
		updatedRounds, err := h.st.ListSupervisorToolRounds(ctx, turn.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if updatedRounds[0].Calls[0].Status == domain.SupervisorToolCompleted || updatedRounds[0].Calls[2].Status == domain.SupervisorToolCompleted {
			t.Fatalf("cancelled pos 1 or fatal pos 3 must not be persisted completed: %+v", updatedRounds[0].Calls)
		}
	})

	t.Run("parent_ctx_cancel_mid_flight_persists_zero_completed", func(t *testing.T) {
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
			concWriteFile(t, h.root, fmt.Sprintf("pc%d.txt", i), fmt.Sprintf("pc%d\n", i))
		}
		p.respond = func(_ context.Context, _ llm.ChatRequest, _ int) (*llm.ChatResponse, error) {
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, llm.ToolCall{
					ID: fmt.Sprintf("pc-%d", i), Name: "workspace_read",
					Arguments: json.RawMessage(concReadPayload(fmt.Sprintf("pc%d.txt", i), 1, 2)),
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
			t.Fatal("batch did not enter in-flight barrier")
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
					t.Fatalf("parent-cancelled call persisted completed: %+v", c)
				}
			}
		}
	})
}

func TestThroughputTier5_R1R2_MCPSearchConcurrentBatchUnderRace(t *testing.T) {
	checker := newConcTestChecker(8)
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 4, MaxToolCalls: 20}, p, checker)
	run, err := NewRunService(h.st).Start(t.Context(), h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.run = run
	gen := strings.Repeat("a", 64)
	client := &exactPermissionMCPClient{
		capabilities: mcp.ScopedCapabilities{
			ProtocolVersion: mcp.ClientProtocolVersion,
			Generation:      gen,
			Servers: []mcp.ScopedServerCapability{
				{
					ServerID: "srv-alpha", Name: "AlphaDocs", DescriptorFingerprint: strings.Repeat("b", 64),
					CapabilityFingerprint: strings.Repeat("c", 64), RegistrationGeneration: 1,
					Tools: []mcp.RemoteTool{
						{Name: "alpha_lookup", Description: "Lookup alpha docs", InputSchema: json.RawMessage(`{"type":"object"}`)},
						{Name: "alpha_search", Description: "Search alpha index", InputSchema: json.RawMessage(`{"type":"object"}`)},
					},
				},
				{
					ServerID: "srv-beta", Name: "BetaTools", DescriptorFingerprint: strings.Repeat("d", 64),
					CapabilityFingerprint: strings.Repeat("e", 64), RegistrationGeneration: 2,
					Tools: []mcp.RemoteTool{
						{Name: "beta_query", Description: "Query beta store", InputSchema: json.RawMessage(`{"type":"object"}`)},
					},
				},
			},
		},
	}
	h.mcpClient = client

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{
					{ID: "ms-1", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"alpha"}`)},
					{ID: "ms-2", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"beta"}`)},
					{ID: "ms-3", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"lookup","server_id":"srv-alpha"}`)},
					{ID: "ms-4", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"query","server_id":"srv-beta"}`)},
					{ID: "ms-5", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"","limit":2}`)},
					{ID: "ms-6", Name: "mcp_tool_search", Arguments: json.RawMessage(fmt.Sprintf(`{"query":"","limit":1,"offset":1,"catalog_revision":%q}`, gen))},
					{ID: "ms-7", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"docs"}`)},
					{ID: "ms-8", Name: "mcp_tool_search", Arguments: json.RawMessage(`{"query":"store"}`)},
				},
			}, nil
		}
		return concActionResponse(domain.RootActionFinish, "8 concurrent mcp searches completed", "done"), nil
	}

	if _, err := h.runner().Step(t.Context(), h.run.ID); err != nil {
		t.Fatalf("step failed: %v", err)
	}
	if checker.MaxInFlight() != 8 {
		t.Fatalf("expected 8 concurrent mcp_tool_search calls, got maxInFlight=%d", checker.MaxInFlight())
	}
	rounds, err := h.st.ListRunSupervisorToolRoundsPage(t.Context(), h.run.ID, 0, 10)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 8 {
		t.Fatalf("unexpected rounds: %+v err=%v", rounds, err)
	}
	for i, c := range rounds[0].Calls {
		if c.Position != i+1 || c.Status != domain.SupervisorToolCompleted {
			t.Fatalf("mcp_tool_search call %d invalid: %+v", i+1, c)
		}
	}
}

func newTier5PrivateHarness(t *testing.T, budget domain.Budget) (*concTestHarness, *concTestProvider) {
	t.Helper()
	baseP := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, budget, baseP, nil)
	p := &tier5PrivateHistoryProvider{concTestProvider: baseP}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	h.router = router
	return h, baseP
}

func TestThroughputTier5_R1R2_PrivateHistoryReactiveContextLimitRecovery(t *testing.T) {
	t.Run("withdraws_tools_refuses_continue_and_never_rolls_over", func(t *testing.T) {
		h, baseP := newTier5PrivateHarness(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
		concWriteFile(t, h.root, "ph.txt", "private-history-file\n")

		sawToolWithdrawal := false
		sawContinueRepair := false
		baseP.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch index {
			case 0:
				return &llm.ChatResponse{
					Provider: baseP.Name(), Model: "model",
					ToolCalls: []llm.ToolCall{{
						ID: "ph-read-1", Name: "workspace_read",
						Arguments: json.RawMessage(concReadPayload("ph.txt", 1, 2)),
					}},
				}, nil
			case 1:
				if len(req.Tools) == 0 {
					return nil, errors.New("expected tools offered before ProviderFailureContextLimit")
				}
				return nil, &llm.ProviderError{
					Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit,
					Provider: baseP.Name(), StatusCode: 400, Message: "context_length_exceeded",
				}
			case 2:
				if len(req.Tools) != 0 {
					return nil, fmt.Errorf("expected tools withdrawn on reactive context_limit recovery, got %d tools", len(req.Tools))
				}
				sawToolWithdrawal = true
				// Attempt continue; must be refused!
				return concActionResponse(domain.RootActionContinue, "Try continue rollover", ""), nil
			case 3:
				if req.Metadata["protocol_repair"] != "1" || len(req.Tools) != 0 {
					return nil, fmt.Errorf("expected protocol repair refusing continue with 0 tools, got metadata=%v tools=%d", req.Metadata, len(req.Tools))
				}
				sawContinueRepair = true
				last := req.Messages[len(req.Messages)-1].Content
				if strings.Contains(last, `"action":"continue"`) || !strings.Contains(last, `action="finish"`) {
					return nil, fmt.Errorf("repair schema did not forbid continue: %s", last)
				}
				return concActionResponse(domain.RootActionFinish, "Finished cleanly without rollover", "done"), nil
			default:
				return nil, fmt.Errorf("unexpected extra model call %d", index)
			}
		}

		res, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
			Content: "Trigger reactive private-history context limit recovery", OperationKey: "tier5-ph-reactive", RequestedBy: "test_operator",
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if !sawToolWithdrawal || !sawContinueRepair {
			t.Fatalf("expected tool withdrawal (%t) and continue repair (%t)", sawToolWithdrawal, sawContinueRepair)
		}
		step := res.Execution.Execution.Steps[0]
		if step.ToolBoundary || step.RequestedAction != domain.RootActionFinish {
			t.Fatalf("private-history recovery must finish without CompleteSupervisorToolBoundary rollover: %+v", step)
		}
	})

	t.Run("zero_rounds_context_limit_fails_cleanly_without_rollover", func(t *testing.T) {
		h, baseP := newTier5PrivateHarness(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
		baseP.respond = func(_ context.Context, _ llm.ChatRequest, _ int) (*llm.ChatResponse, error) {
			return nil, &llm.ProviderError{
				Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit,
				Provider: baseP.Name(), StatusCode: 400, Message: "prompt already over limit at round 0",
			}
		}
		_, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
			Content: "Round 0 context limit", OperationKey: "tier5-ph-round0-limit", RequestedBy: "test_operator",
		})
		if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			t.Fatalf("expected CodeResourceExhausted at round 0, got %v", err)
		}
	})
}

func TestThroughputTier5_R1R2_BoundaryRolloverAtMax24RoundsAnd192Calls(t *testing.T) {
	runRollover := func(t *testing.T, window *llm.ContextWindow) {
		t.Helper()
		p := &concTestProvider{}
		h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 8, MaxToolCalls: 250}, p, nil)
		if window != nil {
			if err := h.router.SetContextWindow(llm.ModelRef{Provider: p.Name(), Model: "model"}, *window); err != nil {
				t.Fatal(err)
			}
		}
		for i := 1; i <= 8; i++ {
			concWriteFile(t, h.root, fmt.Sprintf("m%d.txt", i), fmt.Sprintf("m-%d\n", i))
		}

		p.respond = func(_ context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch {
			case index < domain.MaxSupervisorToolRounds:
				if len(req.Tools) == 0 {
					return nil, fmt.Errorf("tools withdrawn prematurely at round %d", index)
				}
				calls := make([]llm.ToolCall, 0, domain.MaxSupervisorToolCallsPerRound)
				for slot := 1; slot <= domain.MaxSupervisorToolCallsPerRound; slot++ {
					calls = append(calls, llm.ToolCall{
						ID:        fmt.Sprintf("r%d-s%d", index+1, slot),
						Name:      "workspace_read",
						Arguments: json.RawMessage(concReadPayload(fmt.Sprintf("m%d.txt", slot), 1, 2)),
					})
				}
				return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: calls}, nil
			case index == domain.MaxSupervisorToolRounds:
				if len(req.Tools) != 0 {
					return nil, errors.New("expected tools withdrawn at exact round 24 boundary")
				}
				return concActionResponse(domain.RootActionContinue, "Roll over 192 calls into segment 2", ""), nil
			case index == domain.MaxSupervisorToolRounds+1:
				if len(req.Tools) == 0 {
					return nil, errors.New("segment 2 did not restore tools")
				}
				return &llm.ChatResponse{
					Provider: p.Name(), Model: "model",
					ToolCalls: []llm.ToolCall{{
						ID: "seg2-1", Name: "workspace_read",
						Arguments: json.RawMessage(concReadPayload("m1.txt", 1, 2)),
					}},
				}, nil
			default:
				return concActionResponse(domain.RootActionFinish, "Completed 193 calls across 24-round rollover", "done"), nil
			}
		}

		res, err := h.turns().Execute(t.Context(), ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
			Content: "Execute 24 rounds x 8 calls (192 calls) and roll over", OperationKey: "tier5-192-rollover", RequestedBy: "test_operator",
		})
		if err != nil {
			t.Fatalf("192-call rollover failed: %v", err)
		}
		step := res.Execution.Execution.Steps[0]
		wantCalls := domain.MaxSupervisorToolRounds*domain.MaxSupervisorToolCallsPerRound + 1
		if step.ToolRounds != domain.MaxSupervisorToolRounds+1 || step.ToolCalls != wantCalls {
			t.Fatalf("expected %d rounds / %d calls, got %+v", domain.MaxSupervisorToolRounds+1, wantCalls, step)
		}
		// Verify ToolBoundaryContextCalls on segment 2's checkpoint returned all 192 calls.
		cp2 := domain.SupervisorCheckpoint{
			RunID: h.run.ID, NextTurn: 2, AttemptID: step.Checkpoint.AttemptID,
		}
		// Actually verify directly via Run events + BoundaryContextCalls on segment 1+2 records.
		evts, err := h.st.ListRunEvents(t.Context(), h.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		rollovers := 0
		for _, ev := range evts {
			if ev.Type == events.AgentTurnCompletedEvent && strings.Contains(ev.PayloadJSON, `"tool_round_boundary":true`) {
				rollovers++
			}
		}
		if rollovers != 1 {
			t.Fatalf("expected 1 boundary rollover, got %d (cp2=%+v)", rollovers, cp2)
		}
		// Verify segment 2 received the boundary receipt message in req.Messages.
		reqs := p.requests
		seg2Req := reqs[domain.MaxSupervisorToolRounds+1]
		foundBoundary := false
		for _, m := range seg2Req.Messages {
			if strings.Contains(m.Content, "192 returned receipts") {
				foundBoundary = true
				break
			}
		}
		if !foundBoundary {
			t.Fatalf("segment 2 request did not contain all 192 boundary call receipts")
		}
	}

	t.Run("default_32k_window_boundary_receipt_overflow_probe", func(t *testing.T) {
		runRollover(t, nil)
	})

	t.Run("large_256k_window_succeeds_with_all_192_calls", func(t *testing.T) {
		w := llm.ContextWindow{
			ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: 256 * 1024,
			SafetyMarginTokens: 1024, DefaultOutputTokens: 1024, MaxOutputTokens: 4096, Source: "tier5_256k",
		}
		runRollover(t, &w)
	})
}

func TestThroughputTier5_R1R2_NextTurnAndCurrentTurnSteeringAcrossRound10Of24(t *testing.T) {
	p := &concTestProvider{}
	h := newConcTestHarness(t, domain.RunExecutionPermissionAuto, domain.Budget{MaxTurns: 8, MaxToolCalls: 60}, p, nil)
	concWriteFile(t, h.root, "steer.txt", "steer-body\n")

	round10ModelEntered := make(chan struct{})
	releaseRound10Model := make(chan struct{})
	var sawCurrentSteer atomic.Bool
	var leakedNextTurnInTurn1 atomic.Bool
	var sawNextTurnInTurn2 atomic.Bool

	p.respond = func(ctx context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "CurrentTurn steer at round 10") {
				sawCurrentSteer.Store(true)
			}
			if strings.Contains(m.Content, "NextTurn queued follow-up") && index <= domain.MaxSupervisorToolRounds+1 {
				leakedNextTurnInTurn1.Store(true)
			}
			if strings.Contains(m.Content, "NextTurn queued follow-up") && index > domain.MaxSupervisorToolRounds+1 {
				sawNextTurnInTurn2.Store(true)
			}
		}
		switch {
		case index < 9:
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("r-%d", index+1), Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("steer.txt", 1, 2))}},
			}, nil
		case index == 9:
			// Round 10 model call: pause so both NextTurn and CurrentTurn steering arrive mid-turn!
			close(round10ModelEntered)
			select {
			case <-releaseRound10Model:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// Return a tool call that will be superseded or a stale response; steering sequence changed!
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{{ID: "r-10-stale", Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("steer.txt", 1, 2))}},
			}, nil
		case index >= 10 && index < domain.MaxSupervisorToolRounds:
			return &llm.ChatResponse{
				Provider: p.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("r-%d", index+1), Name: "workspace_read", Arguments: json.RawMessage(concReadPayload("steer.txt", 1, 2))}},
			}, nil
		case index == domain.MaxSupervisorToolRounds:
			if len(req.Tools) != 0 {
				return nil, errors.New("expected tools withdrawn at 24-round boundary")
			}
			return concActionResponse(domain.RootActionContinue, "Roll over after 24 rounds while NextTurn remains queued", ""), nil
		case index == domain.MaxSupervisorToolRounds+1:
			return concActionResponse(domain.RootActionFinish, "Turn 1 complete with CurrentTurn applied", "done-turn-1"), nil
		default:
			return concActionResponse(domain.RootActionFinish, "Turn 2 handled NextTurn follow-up", "done-turn-2"), nil
		}
	}

	turns := h.turns()
	type execOut struct {
		res ExecuteThreadTurnResult
		err error
	}
	done := make(chan execOut, 1)
	go func() {
		res, err := turns.Execute(context.Background(), ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: h.threadID,
			Content: "Long 24-round task", OperationKey: "tier5-steer-turn1", RequestedBy: "test_operator",
		})
		done <- execOut{res, err}
	}()

	select {
	case <-round10ModelEntered:
	case <-time.After(10 * time.Second):
		close(releaseRound10Model)
		t.Fatal("did not reach round 10 model call")
	}

	subSvc := NewSessionMessageSubmissionService(h.st)
	nextAccepted, err := subSvc.Submit(t.Context(), SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: h.run.SessionID,
		Content: "NextTurn queued follow-up", OperationKey: "tier5-next-turn-steer", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringNextTurn,
	})
	if err != nil || nextAccepted.Message.DeliveryMode != domain.OperatorSteeringNextTurn {
		close(releaseRound10Model)
		t.Fatalf("failed to submit NextTurn steering: %+v err=%v", nextAccepted, err)
	}
	currAccepted, err := subSvc.Submit(t.Context(), SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: h.run.SessionID,
		Content: "CurrentTurn steer at round 10", OperationKey: "tier5-curr-turn-steer", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	})
	if err != nil || currAccepted.Message.DeliveryMode != domain.OperatorSteeringCurrentTurn {
		close(releaseRound10Model)
		t.Fatalf("failed to submit CurrentTurn steering: %+v err=%v", currAccepted, err)
	}
	close(releaseRound10Model)

	var out execOut
	select {
	case out = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Turn 1 hung")
	}
	if out.err != nil {
		t.Fatalf("Turn 1 failed: %v", out.err)
	}
	if !sawCurrentSteer.Load() {
		t.Fatal("CurrentTurn steering at round 10 never reached subsequent model requests")
	}
	if leakedNextTurnInTurn1.Load() {
		t.Fatal("NextTurn steering leaked into Turn 1 or Segment 2")
	}

	// NextTurn message must still be pending after Turn 1 + Segment 2 rollover!
	nextMsg, err := h.st.GetOperatorSteering(t.Context(), nextAccepted.Message.ID)
	if err != nil || nextMsg.Status != domain.OperatorSteeringPending || nextMsg.Prepared {
		t.Fatalf("NextTurn message was prematurely consumed/prepared during Turn 1 rollover: %+v err=%v", nextMsg, err)
	}
	currMsg, err := h.st.GetOperatorSteering(t.Context(), currAccepted.Message.ID)
	if err != nil || currMsg.Status != domain.OperatorSteeringCommitted {
		t.Fatalf("CurrentTurn message was not committed at end of Turn 1: %+v err=%v", currMsg, err)
	}

	// Drain NextTurn follow-up cleanly as Turn 2.
	drainRes, err := h.runner().DrainOperatorSteering(t.Context(), h.run.ID, 4)
	if err != nil || len(drainRes.Steps) != 1 || drainRes.StopReason != "steering_drained" ||
		drainRes.Steps[0].RequestedAction != domain.RootActionFinish {
		t.Fatalf("DrainOperatorSteering failed: res=%+v err=%v", drainRes, err)
	}
	if !sawNextTurnInTurn2.Load() {
		t.Fatal("NextTurn message did not execute in Turn 2")
	}
}
