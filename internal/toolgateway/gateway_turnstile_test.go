package toolgateway

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
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolbudget"
)

type turnstileBudgetStore struct {
	*memoryStore
	mu          sync.Mutex
	limit       int64
	consumed    int64
	chargeOrder []string
}

func (s *turnstileBudgetStore) ChargeToolCall(ctx context.Context, req toolbudget.ChargeRequest) (toolbudget.Usage, error) {
	if err := ctx.Err(); err != nil {
		return toolbudget.Usage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chargeOrder = append(s.chargeOrder, req.AgentAttemptID)
	if s.limit > 0 && s.consumed >= s.limit {
		now := time.Now().UTC()
		return toolbudget.Usage{
			Tracked:     true,
			RunID:       req.RunID,
			Limit:       s.limit,
			Consumed:    s.consumed,
			Remaining:   0,
			ExhaustedAt: &now,
		}, apperror.New(apperror.CodeResourceExhausted, "tool budget exhausted")
	}
	s.consumed++
	remaining := int64(-1)
	if s.limit > 0 {
		remaining = s.limit - s.consumed
	}
	return toolbudget.Usage{
		Tracked:    true,
		RunID:      req.RunID,
		Limit:      s.limit,
		Consumed:   s.consumed,
		Remaining:  remaining,
		LastCharge: fmt.Sprintf("charge-%d", s.consumed),
	}, nil
}

func (s *turnstileBudgetStore) ChargeOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.chargeOrder...)
}

type turnstileConcurrentSearchExecutor struct {
	mcpExecutorStub
	barrierTarget int
	barrierReady  chan struct{}
	mu            sync.Mutex
	inFlight      int
	maxInFlight   int
	arrived       int
}

func newTurnstileConcurrentSearchExecutor(barrierTarget int) *turnstileConcurrentSearchExecutor {
	e := &turnstileConcurrentSearchExecutor{barrierTarget: barrierTarget}
	if barrierTarget > 1 {
		e.barrierReady = make(chan struct{})
	}
	return e
}

func (e *turnstileConcurrentSearchExecutor) SearchMCP(ctx context.Context, _ MCPExecutionScope, _ MCPSearchPayload) (json.RawMessage, error) {
	e.mu.Lock()
	e.inFlight++
	if e.inFlight > e.maxInFlight {
		e.maxInFlight = e.inFlight
	}
	var waitCh chan struct{}
	if e.barrierTarget > 1 && e.barrierReady != nil {
		e.arrived++
		if e.arrived >= e.barrierTarget {
			select {
			case <-e.barrierReady:
			default:
				close(e.barrierReady)
			}
		}
		waitCh = e.barrierReady
	}
	e.mu.Unlock()

	if waitCh != nil {
		select {
		case <-waitCh:
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}

	e.mu.Lock()
	e.inFlight--
	e.mu.Unlock()
	return json.RawMessage(`{"operations":[]}`), nil
}

func (e *turnstileConcurrentSearchExecutor) MaxInFlight() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxInFlight
}

func testTurnstileMCPSearchCall(callID string, opKey string) ToolCall {
	return ToolCall{
		Name: MCPToolSearchTool, Payload: json.RawMessage(`{"query":"docs"}`),
		RunID: "run-1", MissionID: "mission-1", AgentID: "agent-1", AgentAttemptID: callID,
		SessionID: "session-1", WorkspaceID: "workspace-1", OperationKey: opKey,
		SupervisorToolCallID: callID, SupervisorTurn: 1,
		Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot,
		PermissionMode: domain.RunExecutionPermissionAsk, PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		LeaseID: "lease-1", LeaseGeneration: 1, RequestedBy: "run_supervisor",
	}
}

func TestOrderedBudgetTurnstileSerializesChargeToolCallWhileOverlappingIO(t *testing.T) {
	st := &turnstileBudgetStore{memoryStore: newMemoryStore()}
	exec := newTurnstileConcurrentSearchExecutor(4)
	gw := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec)

	ready := make([]chan struct{}, 5)
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	close(ready[0])

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Reverse startup delay so later slots enter Invoke before slot 0.
			if idx == 0 {
				time.Sleep(25 * time.Millisecond)
			}
			slotCtx, release := WithOrderedBudgetTurnstile(t.Context(), ready[idx], ready[idx+1])
			defer release()
			callID := fmt.Sprintf("call-%d", idx+1)
			opKey := fmt.Sprintf("%064d", idx+1)
			_, errs[idx] = gw.Invoke(slotCtx, testTurnstileMCPSearchCall(callID, opKey))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("slot %d unexpected error: %v", i, err)
		}
	}
	wantOrder := []string{"call-1", "call-2", "call-3", "call-4"}
	gotOrder := st.ChargeOrder()
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("ChargeToolCall order mismatch: got=%v want=%v", gotOrder, wantOrder)
	}
	if exec.MaxInFlight() != 4 {
		t.Fatalf("expected 4 overlapping tool I/O executions after ChargeToolCall, got maxInFlight=%d", exec.MaxInFlight())
	}
}

func TestOrderedBudgetTurnstileEarlyPreToolDenialAndValidationErrorDoNotDeadlock(t *testing.T) {
	st := &turnstileBudgetStore{memoryStore: newMemoryStore()}
	exec := newTurnstileConcurrentSearchExecutor(1)
	engine := hooks.NewEngine(nil)
	if err := engine.Replace([]hooks.Registration{{
		PluginID: "deny-op-1", PluginFingerprint: strings.Repeat("b", 64),
		Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "deny-search", Event: hooks.PreTool,
			Action: hooks.ActionDeny, FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			ToolNames: []string{string(MCPToolSearchTool)}, Message: "blocked",
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// Slot 0 uses a gateway with PreTool denial; slot 1 fails argument validation;
	// slots 2 and 3 use a normal gateway and must charge in order without deadlock.
	gwDenied := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec).WithLifecycleHooks(engine)
	gwAllowed := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec)

	ready := make([]chan struct{}, 5)
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	close(ready[0])

	var wg sync.WaitGroup
	outcomes := make([]Outcome, 4)
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			slotCtx, release := WithOrderedBudgetTurnstile(t.Context(), ready[idx], ready[idx+1])
			defer release()
			call := testTurnstileMCPSearchCall(fmt.Sprintf("call-%d", idx+1), fmt.Sprintf("%064d", idx+1))
			switch idx {
			case 0:
				outcomes[idx], errs[idx] = gwDenied.Invoke(slotCtx, call)
			case 1:
				call.LeaseID = "" // triggers lease validation early return before ChargeToolCall
				outcomes[idx], errs[idx] = gwAllowed.Invoke(slotCtx, call)
			default:
				outcomes[idx], errs[idx] = gwAllowed.Invoke(slotCtx, call)
			}
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || outcomes[0].Decision.Allowed {
		t.Fatalf("expected slot 0 hook denial, got outcome=%+v err=%v", outcomes[0], errs[0])
	}
	if errs[1] == nil {
		t.Fatalf("expected slot 1 lease validation error, got nil")
	}
	if errs[2] != nil || errs[3] != nil {
		t.Fatalf("slots 2/3 failed: errs=%v", errs)
	}
	wantOrder := []string{"call-3", "call-4"}
	gotOrder := st.ChargeOrder()
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("expected only slots 2 and 3 charged in order, got=%v want=%v", gotOrder, wantOrder)
	}
}

func TestOrderedBudgetTurnstilePartialBudgetExhaustionIsDeterministic(t *testing.T) {
	st := &turnstileBudgetStore{memoryStore: newMemoryStore(), limit: 2}
	exec := newTurnstileConcurrentSearchExecutor(1)
	gw := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec)

	ready := make([]chan struct{}, 5)
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	close(ready[0])

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx < 2 {
				time.Sleep(time.Duration(2-idx) * 10 * time.Millisecond)
			}
			slotCtx, release := WithOrderedBudgetTurnstile(t.Context(), ready[idx], ready[idx+1])
			defer release()
			_, errs[idx] = gw.Invoke(slotCtx, testTurnstileMCPSearchCall(fmt.Sprintf("call-%d", idx+1), fmt.Sprintf("%064d", idx+1)))
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("expected slots 0 and 1 to succeed, got errs=%v", errs)
	}
	for idx := 2; idx < 4; idx++ {
		if apperror.CodeOf(errs[idx]) != apperror.CodeResourceExhausted {
			t.Fatalf("expected slot %d to fail deterministically with CodeResourceExhausted, got %v", idx, errs[idx])
		}
	}
}

func TestOrderedBudgetTurnstileRespectsContextCancellation(t *testing.T) {
	st := &turnstileBudgetStore{memoryStore: newMemoryStore()}
	exec := newTurnstileConcurrentSearchExecutor(1)
	gw := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec)

	waitCh := make(chan struct{}) // never closed
	nextCh := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	slotCtx, release := WithOrderedBudgetTurnstile(ctx, waitCh, nextCh)

	done := make(chan error, 1)
	go func() {
		defer release()
		_, err := gw.Invoke(slotCtx, testTurnstileMCPSearchCall("call-cancel", strings.Repeat("c", 64)))
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Invoke deadlocked waiting on turnstile after context cancellation")
	}

	select {
	case <-nextCh:
	case <-time.After(time.Second):
		t.Fatal("next channel was not released after cancellation")
	}

	// Idempotent release check.
	var releases atomic.Int32
	for i := 0; i < 3; i++ {
		release()
		releases.Add(1)
	}
	if releases.Load() != 3 {
		t.Fatalf("unexpected release count %d", releases.Load())
	}
}

func TestOrderedBudgetTurnstilePreToolDenialPreservesFIFOBudgetOrderWhenEarlierSlotDelayed(t *testing.T) {
	st := &turnstileBudgetStore{memoryStore: newMemoryStore(), limit: 3}
	exec := newTurnstileConcurrentSearchExecutor(1)
	engine := hooks.NewEngine(nil)
	if err := engine.Replace([]hooks.Registration{{
		PluginID: "deny-pos2", PluginFingerprint: strings.Repeat("d", 64),
		Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "deny-search-pos2", Event: hooks.PreTool,
			Action: hooks.ActionDeny, FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			ToolNames: []string{string(MCPToolSearchTool)}, Message: "blocked",
		},
	}}); err != nil {
		t.Fatal(err)
	}
	gwAllowed := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec)
	gwDenied := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec).WithLifecycleHooks(engine)

	ready := make([]chan struct{}, 6)
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	close(ready[0])

	var wg sync.WaitGroup
	outcomes := make([]Outcome, 5)
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx == 0 {
				time.Sleep(40 * time.Millisecond)
			}
			slotCtx, release := WithOrderedBudgetTurnstile(t.Context(), ready[idx], ready[idx+1])
			defer release()
			call := testTurnstileMCPSearchCall(fmt.Sprintf("call-%d", idx+1), fmt.Sprintf("%064d", idx+1))
			if idx == 1 {
				outcomes[idx], errs[idx] = gwDenied.Invoke(slotCtx, call)
				return
			}
			outcomes[idx], errs[idx] = gwAllowed.Invoke(slotCtx, call)
		}(i)
	}
	wg.Wait()

	if errs[1] != nil || outcomes[1].Decision.Allowed {
		t.Fatalf("expected slot 1 PreTool denial, got outcome=%+v err=%v", outcomes[1], errs[1])
	}
	for _, idx := range []int{0, 2, 3} {
		if errs[idx] != nil {
			t.Fatalf("expected slot %d to succeed in FIFO order, got err=%v", idx, errs[idx])
		}
	}
	if apperror.CodeOf(errs[4]) != apperror.CodeResourceExhausted {
		t.Fatalf("expected slot 4 to exhaust budget, got err=%v", errs[4])
	}
	wantOrder := []string{"call-1", "call-3", "call-4", "call-5"}
	gotOrder := st.ChargeOrder()
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("ChargeToolCall order mismatch: got=%v want=%v", gotOrder, wantOrder)
	}
}

func TestOrderedBudgetTurnstileCallerReleaseBeforeInvokeWaitsForEarlierSlot(t *testing.T) {
	st := &turnstileBudgetStore{memoryStore: newMemoryStore(), limit: 2}
	exec := newTurnstileConcurrentSearchExecutor(1)
	gw := New(st, policy.NewDefaultChecker()).WithMCPExecutor(exec)

	ready := make([]chan struct{}, 4)
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	close(ready[0])

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			slotCtx, release := WithOrderedBudgetTurnstile(t.Context(), ready[idx], ready[idx+1])
			defer release()
			if idx == 0 {
				time.Sleep(35 * time.Millisecond)
			}
			if idx == 1 {
				// Simulate caller short-circuiting before Gateway.Invoke; release() must
				// still wait for slot 0 before unblocking slot 2.
				return
			}
			call := testTurnstileMCPSearchCall(fmt.Sprintf("call-%d", idx+1), fmt.Sprintf("%064d", idx+1))
			_, errs[idx] = gw.Invoke(slotCtx, call)
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("expected slots 0 and 2 to succeed, got errs=%v", errs)
	}
	wantOrder := []string{"call-1", "call-3"}
	gotOrder := st.ChargeOrder()
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("early caller release bypassed earlier slot FIFO order: got=%v want=%v", gotOrder, wantOrder)
	}
}

