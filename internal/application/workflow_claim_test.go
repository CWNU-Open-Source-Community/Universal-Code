package application_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type workflowClaimProbeStore struct {
	*store.SQLiteStore
	renewalStarted chan struct{}
	renewalGate    <-chan struct{}
	renewalFailure error
	releaseFailure error
	startedOnce    sync.Once
	renewals       atomic.Int32
}

func (s *workflowClaimProbeStore) RenewWorkflowExecutionClaim(ctx context.Context, id, owner string) (bool, error) {
	s.startedOnce.Do(func() { close(s.renewalStarted) })
	select {
	case <-s.renewalGate:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if s.renewalFailure != nil {
		return false, s.renewalFailure
	}
	renewed, err := s.SQLiteStore.RenewWorkflowExecutionClaim(ctx, id, owner)
	if renewed && err == nil {
		s.renewals.Add(1)
	}
	return renewed, err
}

func (s *workflowClaimProbeStore) ReleaseWorkflowExecution(ctx context.Context, id, owner string) error {
	return errors.Join(s.SQLiteStore.ReleaseWorkflowExecution(ctx, id, owner), s.releaseFailure)
}

// A model fixture can observe cancellation while retaining the runner until its
// drain gate opens. That proves the independent Run lease covers cleanup too.
type workflowClaimProvider struct {
	*specialistTestProvider
	entered, release, cancelled, drain chan struct{}
	reply                              llm.ChatResponse
}

func (p *workflowClaimProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.calls++
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	close(p.entered)
	select {
	case <-p.release:
		return &p.reply, nil
	case <-ctx.Done():
		close(p.cancelled)
		<-p.drain
		return nil, ctx.Err()
	}
}

func (p *workflowClaimProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	reply, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 1)
	chunk := llm.FinalChatChunk(reply)
	chunk.Text = reply.Text
	chunks <- chunk
	close(chunks)
	return chunks, nil
}

func workflowClaimService(st application.WorkflowStore, p llm.Provider) *application.WorkflowService {
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	return application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
}

func waitWorkflowClaimSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(15 * time.Second):
		t.Fatalf("did not observe %s", label)
	}
}

func TestWorkflowClaimRenewsDuringLongWorker(t *testing.T) {
	t.Parallel()
	base := &specialistTestProvider{}
	raw, run, _, path := newWorkflowFixture(t, base)
	gate := make(chan struct{})
	close(gate)
	st := &workflowClaimProbeStore{SQLiteStore: raw, renewalStarted: make(chan struct{}), renewalGate: gate}
	p := &workflowClaimProvider{specialistTestProvider: base, entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{}), drain: make(chan struct{}), reply: workflowResponse(t, domain.CompletionSucceeded)}
	close(p.drain)
	svc := workflowClaimService(st, p)
	e := startHostWorkflow(t, svc, run.ID, "long-renewed-claim", workflowSpec(workflowWorker("one")))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := svc.Advance(ctx, e.ID); done <- err }()
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(p.release) }); cancel(); <-done })
	waitWorkflowClaimSignal(t, p.entered, "long model call")
	second, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// This crosses the real initial 30-second expiry. All renewal writes and
	// the second store's claim attempt use SQLite and the wall clock.
	select {
	case <-time.After(35 * time.Second):
	case <-p.cancelled:
		t.Fatal("long-running worker lost its live claim")
	}
	if st.renewals.Load() < 3 {
		t.Fatalf("missing claim heartbeats: renewals=%d", st.renewals.Load())
	}
	if _, claimed, err := second.ClaimWorkflowExecution(t.Context(), e.ID, "second-live-host"); err != nil || claimed {
		t.Fatalf("second host stole renewed claim: claimed=%t err=%v", claimed, err)
	}
	releaseOnce.Do(func() { close(p.release) })
	select {
	case err := <-done:
		// Keep cleanup's wait usable after consuming the result.
		done <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long worker did not drain")
	}
	workflowTick(t, svc)
	e, err = svc.Get(t.Context(), e.ID)
	if err != nil || e.Status != "completed" || e.State.Jobs != 1 || base.calls != 1 {
		t.Fatalf("renewed work duplicated or lost: execution=%+v calls=%d err=%v", e, base.calls, err)
	}
	t.Logf("worker remained exclusively owned past 35s; successful renewals=%d model_calls=%d", st.renewals.Load(), base.calls)
}

func TestWorkflowClaimRenewalLossCancelsAndDrainsRunLease(t *testing.T) {
	for _, mode := range []string{"storage_failure", "owner_replaced", "renewal_timeout"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			base := &specialistTestProvider{}
			raw, run, _, path := newWorkflowFixture(t, base)
			gate := make(chan struct{})
			st := &workflowClaimProbeStore{SQLiteStore: raw, renewalStarted: make(chan struct{}), renewalGate: gate}
			failure := errors.New("injected claim renewal storage failure")
			if mode == "storage_failure" {
				st.renewalFailure = failure
			}
			p := &workflowClaimProvider{specialistTestProvider: base, entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{}), drain: make(chan struct{})}
			svc := workflowClaimService(st, p)
			e := startHostWorkflow(t, svc, run.ID, "lost-claim-"+mode, workflowSpec(workflowWorker("one")))
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { _, err := svc.Advance(ctx, e.ID); done <- err }()
			var gateOnce, drainOnce sync.Once
			t.Cleanup(func() {
				gateOnce.Do(func() { close(gate) })
				drainOnce.Do(func() { close(p.drain) })
				cancel()
				<-done
			})
			waitWorkflowClaimSignal(t, p.entered, "model start")
			waitWorkflowClaimSignal(t, st.renewalStarted, "first claim renewal")
			if mode == "owner_replaced" {
				// Inject expiry at this exact renewal boundary; replacement itself
				// still uses production SQLite claim/fencing code.
				db, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.ExecContext(t.Context(), `UPDATE workflow_executions SET claim_until=? WHERE id=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), e.ID)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				if _, claimed, err := raw.ClaimWorkflowExecution(t.Context(), e.ID, "replacement-owner"); err != nil || !claimed {
					t.Fatalf("replace expired owner: claimed=%t err=%v", claimed, err)
				}
			}
			if mode != "renewal_timeout" {
				gateOnce.Do(func() { close(gate) })
			}
			waitWorkflowClaimSignal(t, p.cancelled, "model cancellation after claim renewal loss")
			lease, found, err := raw.GetRunExecutionLease(t.Context(), run.ID)
			if err != nil || !found || !lease.ActiveAt(time.Now()) {
				t.Fatalf("draining execution lost its Run lease: %+v found=%t err=%v", lease, found, err)
			}
			if _, err := raw.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "second-worker-during-drain", TTL: time.Minute}); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("second worker entered while old worker was draining: %v", err)
			}
			select {
			case err := <-done:
				done <- err
				t.Fatalf("interpreter returned before worker drained: %v", err)
			default:
			}
			drainOnce.Do(func() { close(p.drain) })
			select {
			case err := <-done:
				done <- err
				if err == nil || mode == "storage_failure" && !errors.Is(err, failure) || mode == "owner_replaced" && !strings.Contains(err.Error(), "claim was lost") || mode == "renewal_timeout" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("claim renewal error was swallowed: mode=%s err=%v", mode, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled interpreter did not stop and join")
			}
			lease, _, err = raw.GetRunExecutionLease(t.Context(), run.ID)
			if err != nil || lease.Status != domain.RunExecutionLeaseReleased || base.calls != 1 {
				t.Fatalf("Run lease not released after drain: lease=%+v calls=%d err=%v", lease, base.calls, err)
			}
			if mode == "owner_replaced" {
				if renewed, err := raw.RenewWorkflowExecutionClaim(t.Context(), e.ID, "replacement-owner"); err != nil || !renewed {
					t.Fatalf("old owner's deferred release cleared replacement claim: renewed=%t err=%v", renewed, err)
				}
			}
			t.Logf("%s cancelled provider, retained Run exclusion through drain, returned error and released Run lease", mode)
		})
	}
}

func TestWorkflowClaimReleaseErrorIsReturned(t *testing.T) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
	raw, run, _, _ := newWorkflowFixture(t, p)
	failure := errors.New("injected workflow claim release failure")
	gate := make(chan struct{})
	close(gate)
	st := &workflowClaimProbeStore{SQLiteStore: raw, releaseFailure: failure, renewalStarted: make(chan struct{}), renewalGate: gate}
	svc := workflowClaimService(st, p)
	e := startHostWorkflow(t, svc, run.ID, "claim-release-error", workflowSpec(workflowWorker("one")))
	if _, err := svc.Advance(t.Context(), e.ID); !errors.Is(err, failure) {
		t.Fatalf("claim release error was swallowed: %v", err)
	}
}
