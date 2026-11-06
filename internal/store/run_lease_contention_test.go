package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

type runLeaseContentionStore struct {
	*SQLiteStore
	timedOut  chan struct{}
	once      sync.Once
	renewals  atomic.Int32
	attempts  atomic.Int32
	started   chan int32
	cancelled chan struct{}
}

func (s *runLeaseContentionStore) RenewRunExecutionLease(ctx context.Context,
	lease domain.RunExecutionLease, ttl time.Duration,
) (domain.RunExecutionLease, error) {
	attempt := s.attempts.Add(1)
	s.started <- attempt
	renewed, err := s.SQLiteStore.RenewRunExecutionLease(ctx, lease, ttl)
	if errors.Is(err, context.DeadlineExceeded) {
		s.once.Do(func() { close(s.timedOut) })
	}
	if err == nil {
		s.renewals.Add(1)
	}
	if attempt == 2 && errors.Is(err, context.Canceled) {
		close(s.cancelled)
	}
	return renewed, err
}

type runLeaseContentionProvider struct {
	llm.MockProvider
	entered, release, cancelled chan struct{}
	once                        sync.Once
	calls                       atomic.Int32
	cancelledAt                 atomic.Int64
	response                    llm.ChatResponse
}

func (*runLeaseContentionProvider) Name() string { return "run-lease-contention" }

func (p *runLeaseContentionProvider) Chat(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	select {
	case <-ctx.Done():
		p.cancelledAt.Store(time.Now().UnixNano())
		close(p.cancelled)
		return nil, ctx.Err()
	case <-p.release:
		return &p.response, nil
	}
}

func (p *runLeaseContentionProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	r, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 1)
	chunk := llm.FinalChatChunk(r)
	chunk.Text = r.Text
	chunks <- chunk
	close(chunks)
	return chunks, nil
}

func TestAgentRunnerSurvivesTemporarySQLitePoolContention(t *testing.T) {
	runAgentRunnerSQLiteLeaseContention(t, "temporary")
}

func TestAgentRunnerRunLeaseContentionStops(t *testing.T) {
	for _, mode := range []string{"expiry", "caller_cancel", "owner_replaced"} {
		t.Run(mode, func(t *testing.T) { runAgentRunnerSQLiteLeaseContention(t, mode) })
	}
}

func runAgentRunnerSQLiteLeaseContention(t *testing.T, mode string) {
	t.Helper()
	raw, err := Open(filepath.Join(t.TempDir(), "run-lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	st := &runLeaseContentionStore{SQLiteStore: raw, timedOut: make(chan struct{}),
		started: make(chan int32, 100), cancelled: make(chan struct{})}
	action, err := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion,
		Kind: domain.RootActionContinue, Message: "model completed after temporary contention"})
	if err != nil {
		t.Fatal(err)
	}
	p := &runLeaseContentionProvider{entered: make(chan struct{}), release: make(chan struct{}),
		cancelled: make(chan struct{}), response: llm.ChatResponse{Text: string(action),
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{
		Goal: "complete one model call", Profile: "code", ModelRoute: p.Name() + "/model",
		Budget: domain.Budget{MaxTurns: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	// Keep the real 2s SQL renewal deadline and accelerate the heartbeat.
	// Temporary contention retains the production 30s TTL; the expiry case
	// shortens only the TTL. There is no workflow claim here.
	ttl := 30 * time.Second
	if mode == "expiry" {
		ttl = 4 * time.Second
	}
	runner := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).
		WithRunExecutionLeasePolicy(application.RunExecutionLeasePolicy{
			TTL: ttl, RenewInterval: 500 * time.Millisecond,
		})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		result application.LifecycleResult
		err    error
	}
	done := make(chan outcome, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result, stepErr := runner.Step(ctx, run.ID)
		done <- outcome{result: result, err: stepErr}
	}()
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(p.release) })
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("runner did not drain during cleanup")
		}
	})
	select {
	case <-p.entered:
	case result := <-done:
		t.Fatalf("runner ended before model: %#v err=%v", result.result, result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not enter model")
	}
	conn, err := raw.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var proof int
	if err := conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&proof); err != nil || proof != 1 {
		t.Fatalf("real SQLite connection proof=%d err=%v", proof, err)
	}
	heldAt := time.Now()
	select {
	case <-st.timedOut:
	case <-time.After(4 * time.Second):
		t.Fatal("no real SQL renewal timeout while pool was occupied")
	}
	confirmed, err := scanRunExecutionLease(conn.QueryRowContext(t.Context(), runExecutionLeaseSelect, run.ID))
	if err != nil || !confirmed.ActiveAt(time.Now()) {
		t.Fatalf("lease was not still active after renewal timeout: %#v err=%v", confirmed, err)
	}
	t.Logf("SQL renewal timed out after %s; confirmed lease still valid for %s, owner=%s generation=%d",
		time.Since(heldAt).Round(time.Millisecond), time.Until(confirmed.ExpiresAt).Round(time.Millisecond),
		confirmed.OwnerID, confirmed.Generation)
	if mode != "temporary" {
		if mode == "caller_cancel" {
			// Cancel while the second real renewal is blocked in database/sql.
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for attempt := int32(0); attempt < 2; {
				select {
				case attempt = <-st.started:
				case <-deadline.C:
					t.Fatal("renewal did not retry before caller cancellation")
				}
			}
			cancel()
			select {
			case <-st.cancelled:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("blocked SQL renewal did not inherit caller cancellation")
			}
		}
		if mode == "owner_replaced" {
			// Simulate another writer replacing the fence in actual SQLite.
			if _, err := conn.ExecContext(t.Context(), `UPDATE run_execution_leases
				SET owner_id = 'replacement-worker', generation = generation + 1 WHERE run_id = ?`, run.ID); err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
		}
		wait := 500 * time.Millisecond
		if mode == "expiry" {
			wait = time.Until(confirmed.ExpiresAt) + 500*time.Millisecond
		}
		select {
		case <-p.cancelled:
		case <-time.After(wait):
			t.Fatal("model was not cancelled at lease loss, expiry, or caller cancellation")
		}
		if mode == "expiry" {
			at := time.Unix(0, p.cancelledAt.Load())
			if at.Before(confirmed.ExpiresAt.Add(-100*time.Millisecond)) || at.After(confirmed.ExpiresAt.Add(500*time.Millisecond)) {
				t.Fatalf("persistent contention cancellation missed confirmed expiry: at=%s expiry=%s", at, confirmed.ExpiresAt)
			}
		}
		if mode != "owner_replaced" {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case result := <-done:
			if result.err == nil {
				t.Fatal("lease loss returned success")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("cancelled runner did not drain after connection release")
		}
		lease, found, err := raw.GetRunExecutionLease(t.Context(), run.ID)
		if err != nil || !found || mode == "owner_replaced" &&
			(lease.OwnerID != "replacement-worker" || lease.Generation != confirmed.Generation+1 || lease.Status != domain.RunExecutionLeaseActive) {
			t.Fatalf("lost owner changed replacement lease: %#v found=%t err=%v", lease, found, err)
		}
		if st.renewals.Load() != 0 || p.calls.Load() != 1 {
			t.Fatalf("lost lease renewed or repeated model: renewals=%d calls=%d", st.renewals.Load(), p.calls.Load())
		}
		return
	}
	// The real pool remains unavailable for longer than one renewal attempt.
	if remaining := 3500*time.Millisecond - time.Since(heldAt); remaining > 0 {
		timer := time.NewTimer(remaining)
		<-timer.C
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.cancelled:
		t.Fatal("healthy model was cancelled while its confirmed Run lease remained valid")
	default:
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for st.renewals.Load() == 0 {
		select {
		case result := <-done:
			t.Fatalf("runner ended before renewal recovered: %#v err=%v", result.result, result.err)
		case <-deadline.C:
			t.Fatal("Run lease did not renew after connection release")
		case <-time.After(10 * time.Millisecond):
		}
	}
	releaseOnce.Do(func() { close(p.release) })
	select {
	case result := <-done:
		if result.err != nil || result.result.Status != application.LifecycleTurnCompleted {
			t.Fatalf("runner did not complete: %#v err=%v", result.result, result.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runner did not finish after model release")
	}
	if p.calls.Load() != 1 {
		t.Fatalf("healthy model was invoked %d times", p.calls.Load())
	}
	lease, found, err := raw.GetRunExecutionLease(t.Context(), run.ID)
	if err != nil || !found || lease.Status != domain.RunExecutionLeaseReleased ||
		lease.LeaseID != confirmed.LeaseID || lease.Generation != confirmed.Generation {
		t.Fatalf("recovered execution did not release original lease: %#v found=%t err=%v", lease, found, err)
	}
}
