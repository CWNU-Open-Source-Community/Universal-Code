package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type claimContentionReviewStore struct {
	*SQLiteStore
	mu                     sync.Mutex
	claimedAt              time.Time
	claimEvents, runEvents []string
	prepareOnce            sync.Once
	claimCalls             atomic.Int32
	firstTimeout           chan struct{}
	timeoutOnce            sync.Once
	runRenewStore          *SQLiteStore
}

func (s *claimContentionReviewStore) ClaimWorkflowExecution(ctx context.Context, id, owner string) (domain.WorkflowExecution, bool, error) {
	started := time.Now()
	e, ok, err := s.SQLiteStore.ClaimWorkflowExecution(ctx, id, owner)
	if ok && err == nil {
		s.mu.Lock()
		s.claimedAt = started
		s.mu.Unlock()
	}
	return e, ok, err
}
func (s *claimContentionReviewStore) PrepareWorkflowStep(ctx context.Context, step domain.WorkflowStepExecution, owner string) (domain.WorkflowStepExecution, error) {
	step, err := s.SQLiteStore.PrepareWorkflowStep(ctx, step, owner)
	if err == nil {
		s.prepareOnce.Do(func() {
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
		})
	}
	return step, err
}
func (s *claimContentionReviewStore) RenewWorkflowExecutionClaim(ctx context.Context, id, owner string) (bool, error) {
	s.claimCalls.Add(1)
	start := time.Now()
	// The assertion keeps this same test source buildable on the actual
	// pre-renewal revision, which never invokes this additional method.
	renewer, ok := any(s.SQLiteStore).(interface {
		RenewWorkflowExecutionClaim(context.Context, string, string) (bool, error)
	})
	if !ok {
		return false, errors.New("pre-renewal store unexpectedly asked to renew")
	}
	renewed, err := renewer.RenewWorkflowExecutionClaim(ctx, id, owner)
	outerErr := ctx.Err()
	deadline, _ := ctx.Deadline()
	if errors.Is(err, context.DeadlineExceeded) {
		s.timeoutOnce.Do(func() { close(s.firstTimeout) })
	}
	s.mu.Lock()
	s.claimEvents = append(s.claimEvents, fmt.Sprintf("start=%s elapsed=%s renewed=%t err=%v outer_err=%v deadline_left=%s", start.Sub(s.claimedAt).Round(time.Millisecond), time.Since(start).Round(time.Millisecond), renewed, err, outerErr, time.Until(deadline).Round(time.Microsecond)))
	s.mu.Unlock()
	return renewed, err
}
func (s *claimContentionReviewStore) RenewRunExecutionLease(ctx context.Context, lease domain.RunExecutionLease, ttl time.Duration) (domain.RunExecutionLease, error) {
	start := time.Now()
	backend := s.SQLiteStore
	if s.runRenewStore != nil {
		backend = s.runRenewStore
	}
	lease, err := backend.RenewRunExecutionLease(ctx, lease, ttl)
	s.mu.Lock()
	s.runEvents = append(s.runEvents, fmt.Sprintf("start=%s elapsed=%s err=%v", start.Sub(s.claimedAt).Round(time.Millisecond), time.Since(start).Round(time.Millisecond), err))
	s.mu.Unlock()
	return lease, err
}

type claimContentionReviewProvider struct {
	llm.MockProvider
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
	cancelled        atomic.Bool
	cancelledAt      atomic.Int64
	cancelledSignal  chan struct{}
	cancelOnce       sync.Once
	response         llm.ChatResponse
}

func (*claimContentionReviewProvider) Name() string { return "claim-contention-review" }
func (p *claimContentionReviewProvider) Chat(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return &p.response, nil
	case <-ctx.Done():
		p.cancelled.Store(true)
		p.cancelledAt.Store(time.Now().UnixNano())
		p.cancelOnce.Do(func() { close(p.cancelledSignal) })
		return nil, ctx.Err()
	}
}
func (p *claimContentionReviewProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	r, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 1)
	chunk := llm.FinalChatChunk(r)
	chunk.Text = r.Text
	ch <- chunk
	close(ch)
	return ch, nil
}

func TestWorkflowClaimContentionReviewHealthyCall(t *testing.T) {
	t.Parallel()
	runWorkflowClaimContentionReview(t, "temporary")
}

func TestWorkflowClaimPersistentContentionStopsBeforeKnownExpiry(t *testing.T) {
	t.Parallel()
	runWorkflowClaimContentionReview(t, "persistent")
}

func TestWorkflowClaimContentionRetryStopsWithParent(t *testing.T) {
	t.Parallel()
	runWorkflowClaimContentionReview(t, "parent_cancel")
}

func runWorkflowClaimContentionReview(t *testing.T, mode string) {
	raw, run, _, _ := workflowDueFixture(t)
	st := &claimContentionReviewStore{SQLiteStore: raw, firstTimeout: make(chan struct{})}
	if mode == "persistent" {
		// Isolate the claim's retry bound from the unchanged Run heartbeat:
		// that heartbeat still uses real SQLite, through a separate pool on
		// the same database, while the claim's sole connection stays occupied.
		leaseStore, err := Open(filepath.Join(raw.home, "workflow-due.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { leaseStore.Close() })
		st.runRenewStore = leaseStore
	}
	response, err := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "healthy model completed", Report: &domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "contention released before Run heartbeat", WorkItemIDs: []string{}, NoteIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	p := &claimContentionReviewProvider{entered: make(chan struct{}), release: make(chan struct{}), cancelledSignal: make(chan struct{}), response: llm.ChatResponse{Text: string(response), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	svc := application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
	d, err := svc.Save(t.Context(), domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "claim contention review", Limits: domain.WorkflowLimits{MaxSteps: 4, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000}, Steps: []domain.WorkflowStep{{ID: "one", Kind: "worker", Task: &domain.ChildTask{Title: "healthy", Goal: "Return a successful report", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 30000}}}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := svc.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: d.ID, OperationKey: "claim-contention-review"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	var advanced domain.WorkflowExecution
	var advanceErr error
	go func() { advanced, advanceErr = svc.Advance(ctx, e.ID); close(done) }()
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(p.release) })
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("interpreter did not finish")
		}
	})
	select {
	case <-p.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("model did not start")
	}
	st.mu.Lock()
	claimedAt := st.claimedAt
	st.mu.Unlock()
	t.Logf("model entered at %s; three-second prepare offset applies equally to both revisions", time.Since(claimedAt).Round(time.Millisecond))
	if mode == "temporary" {
		releaseTimer := time.AfterFunc(time.Until(claimedAt.Add(15*time.Second)), func() { releaseOnce.Do(func() { close(p.release) }) })
		defer releaseTimer.Stop()
	}
	time.Sleep(time.Until(claimedAt.Add(9 * time.Second)))
	conn, err := raw.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var proof int
	if err := conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&proof); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	blockedAt := time.Now()
	switch mode {
	case "temporary":
		time.Sleep(3500 * time.Millisecond)
	case "parent_cancel":
		select {
		case <-st.firstTimeout:
			cancel()
		case <-time.After(6 * time.Second):
			t.Fatal("first renewal timeout was not observed")
		}
		select {
		case <-p.cancelledSignal:
		case <-time.After(time.Second):
			t.Fatal("parent cancellation did not stop model")
		}
	case "persistent":
		select {
		case <-p.cancelledSignal:
		case <-time.After(time.Until(claimedAt.Add(31 * time.Second))):
			t.Fatal("retry exceeded the known claim expiry")
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("real sole SQLite connection occupied from %s for %s; SELECT proof=%d", blockedAt.Sub(claimedAt).Round(time.Millisecond), time.Since(blockedAt).Round(time.Millisecond), proof)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("interpreter did not settle")
	}
	st.mu.Lock()
	t.Logf("claim renewal events=%v; Run lease renewal events=%v", st.claimEvents, st.runEvents)
	st.mu.Unlock()
	t.Logf("model calls=%d cancelled=%t jobs=%d err=%v", p.calls.Load(), p.cancelled.Load(), advanced.State.Jobs, advanceErr)
	switch mode {
	case "temporary":
		if advanceErr != nil || p.cancelled.Load() || advanced.State.Jobs != 1 || st.claimCalls.Load() < 2 {
			t.Fatal("healthy call did not survive the temporary connection contention with a real retry")
		}
	case "persistent":
		cancelledAt := time.Unix(0, p.cancelledAt.Load())
		if !errors.Is(advanceErr, context.DeadlineExceeded) || !p.cancelled.Load() || cancelledAt.Before(claimedAt.Add(26*time.Second)) || !cancelledAt.Before(claimedAt.Add(30*time.Second)) || st.claimCalls.Load() < 2 {
			t.Fatalf("persistent contention did not stop inside the known claim window: cancelled=%s calls=%d err=%v", cancelledAt.Sub(claimedAt), st.claimCalls.Load(), advanceErr)
		}
		t.Logf("persistent contention cancelled at %s, before conservative initial expiry=30s", cancelledAt.Sub(claimedAt).Round(time.Millisecond))
	case "parent_cancel":
		if !errors.Is(advanceErr, context.Canceled) || !p.cancelled.Load() || st.claimCalls.Load() != 1 {
			t.Fatalf("parent cancellation retried or lost its cause: calls=%d err=%v", st.claimCalls.Load(), advanceErr)
		}
	}
}
