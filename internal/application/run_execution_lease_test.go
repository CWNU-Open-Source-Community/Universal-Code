package application_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

func TestAgentRunnerHeartbeatKeepsLongModelCallExclusive(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "heartbeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	provider := newLeaseBlockingProvider()
	t.Cleanup(provider.unblock)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "long model call", Profile: "code", ModelRoute: provider.Name() + "/model",
		Budget: domain.Budget{MaxTurns: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	leasePolicy := application.RunExecutionLeasePolicy{
		// Keep the production 2s renewal deadline while still waiting past the
		// original expiry and observing real SQLite renewals below.
		TTL: 3 * time.Second, RenewInterval: 150 * time.Millisecond,
	}
	first := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).
		WithRunExecutionLeaseOwner("heartbeat-worker-a").
		WithRunExecutionLeasePolicy(leasePolicy)
	second := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).
		WithRunExecutionLeaseOwner("heartbeat-worker-b").
		WithRunExecutionLeasePolicy(leasePolicy)

	type stepResult struct {
		result application.LifecycleResult
		err    error
	}
	finished := make(chan stepResult, 1)
	go func() {
		result, stepErr := first.Step(ctx, run.ID)
		finished <- stepResult{result: result, err: stepErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first supervisor did not reach the blocking model call")
	}
	initial, found, err := st.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || initial.OwnerID != "heartbeat-worker-a" {
		t.Fatalf("first supervisor lease was not visible: %#v found=%v err=%v", initial, found, err)
	}
	waitForRenewedLease(t, ctx, st, initial)

	if _, err := second.Step(ctx, run.ID); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("second supervisor entered during a renewed lease: code=%s err=%v", apperror.CodeOf(err), err)
	}
	provider.unblock()
	select {
	case completed := <-finished:
		if completed.err != nil || completed.result.Status != application.LifecycleTurnCompleted {
			t.Fatalf("first supervisor did not complete after release: %#v err=%v", completed.result, completed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first supervisor did not finish")
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("exclusive execution made %d model calls", provider.calls.Load())
	}
	lease, found, err := st.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || lease.Status != domain.RunExecutionLeaseReleased ||
		lease.LeaseID != initial.LeaseID || lease.Generation != initial.Generation {
		t.Fatalf("completed supervisor did not release its lease: %#v found=%v err=%v", lease, found, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countEventType(timeline, events.RunExecutionLeaseAcquiredEvent) != 1 ||
		countEventType(timeline, events.RunExecutionLeaseReleasedEvent) != 1 ||
		countEventType(timeline, events.RunExecutionLeaseTakenOverEvent) != 0 {
		t.Fatalf("heartbeat lease timeline is inconsistent: %#v", timeline)
	}
}

func TestAgentRunnerHeartbeatKeepsLongWebSearchExclusive(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "web-search-heartbeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "search one public source", Profile: "review", Surface: "code",
		Phase: "deliver", ModelRoute: "tool-loop/model", NetworkMode: "allowlist",
		AllowedTargets: []string{"search.example.com"},
		Budget:         domain.Budget{MaxTurns: 3, MaxToolCalls: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	model := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("provider-web-search", string(toolgateway.WebSearchTool),
			`{"version":"web_search.v1","query":"official specification","limit":1}`),
		textResponse(rootActionResponse(domain.RootActionContinue,
			"public source found", "", "")),
	}}
	search := newLeaseBlockingSearchProvider()
	t.Cleanup(search.unblock)
	service := webevidence.NewService(st, search, nil)
	leasePolicy := application.RunExecutionLeasePolicy{
		TTL: 600 * time.Millisecond, RenewInterval: 100 * time.Millisecond,
	}
	first := newToolLoopSupervisor(st, model).WithWebEvidence(service).
		WithRunExecutionLeaseOwner("web-search-worker-a").
		WithRunExecutionLeasePolicy(leasePolicy)
	second := newToolLoopSupervisor(st, model).WithWebEvidence(service).
		WithRunExecutionLeaseOwner("web-search-worker-b").
		WithRunExecutionLeasePolicy(leasePolicy)

	type stepResult struct {
		result application.LifecycleResult
		err    error
	}
	finished := make(chan stepResult, 1)
	go func() {
		result, stepErr := first.Step(ctx, run.ID)
		finished <- stepResult{result: result, err: stepErr}
	}()
	select {
	case <-search.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first supervisor did not reach the blocking web_search call")
	}
	initial, found, err := st.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || initial.OwnerID != "web-search-worker-a" {
		t.Fatalf("first supervisor lease was not visible: %#v found=%v err=%v",
			initial, found, err)
	}
	waitForRenewedLease(t, ctx, st, initial)
	current, found, err := st.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || current.Generation != initial.Generation ||
		current.LeaseID != initial.LeaseID {
		t.Fatalf("heartbeat changed the execution fence: initial=%#v current=%#v found=%v err=%v",
			initial, current, found, err)
	}
	if _, err := second.Step(ctx, run.ID); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("second supervisor entered during renewed web_search lease: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	search.unblock()
	select {
	case completed := <-finished:
		if completed.err != nil || completed.result.Status != application.LifecycleTurnCompleted ||
			completed.result.ToolRounds != 1 || completed.result.ToolCalls != 1 ||
			completed.result.Text != "public source found" {
			t.Fatalf("web_search supervisor did not complete: %#v err=%v",
				completed.result, completed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first supervisor did not finish after web_search release")
	}
	if search.calls.Load() != 1 {
		t.Fatalf("web_search backend made %d calls", search.calls.Load())
	}
	lease, found, err := st.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || lease.Status != domain.RunExecutionLeaseReleased {
		t.Fatalf("completed supervisor did not release its lease: %#v found=%v err=%v",
			lease, found, err)
	}
}

func waitForRenewedLease(t *testing.T, ctx context.Context, st *store.SQLiteStore,
	initial domain.RunExecutionLease,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, found, err := st.GetRunExecutionLease(ctx, initial.RunID)
		now := time.Now().UTC()
		if err == nil && found && now.After(initial.ExpiresAt) && current.ActiveAt(now) &&
			current.RenewedAt.After(initial.RenewedAt) && current.ExpiresAt.After(initial.ExpiresAt) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("execution lease was not renewed beyond its original expiry")
}

type leaseBlockingProvider struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	calls       atomic.Int64
}

type leaseBlockingSearchProvider struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	calls       atomic.Int64
}

func newLeaseBlockingSearchProvider() *leaseBlockingSearchProvider {
	return &leaseBlockingSearchProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (*leaseBlockingSearchProvider) Name() string { return "lease-search" }

func (*leaseBlockingSearchProvider) Endpoint() string { return "https://search.example.com/api" }

func (p *leaseBlockingSearchProvider) Search(ctx context.Context, _ string, _ int,
	_ webevidence.NetworkAuthority,
) ([]webevidence.ProviderResult, error) {
	p.calls.Add(1)
	p.startedOnce.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return []webevidence.ProviderResult{{
			URL: "https://docs.example.com/specification", Title: "Official specification",
			Snippet: "Public specification result", Rank: 1,
		}}, nil
	}
}

func (p *leaseBlockingSearchProvider) unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func newLeaseBlockingProvider() *leaseBlockingProvider {
	return &leaseBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (*leaseBlockingProvider) Name() string { return "lease-blocking" }

func (p *leaseBlockingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: p.Name()}}, nil
}

func (p *leaseBlockingProvider) Chat(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	return p.response(), nil
}

func (p *leaseBlockingProvider) StreamChat(ctx context.Context, _ llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	response := p.response()
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (p *leaseBlockingProvider) wait(ctx context.Context) error {
	p.calls.Add(1)
	p.startedOnce.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}

func (p *leaseBlockingProvider) unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *leaseBlockingProvider) response() *llm.ChatResponse {
	return &llm.ChatResponse{
		Text:     rootActionResponse(domain.RootActionContinue, "lease held", "", ""),
		Provider: p.Name(), Model: "model",
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}
}

func (*leaseBlockingProvider) SupportsTools(string) bool    { return false }
func (*leaseBlockingProvider) SupportsVision(string) bool   { return false }
func (*leaseBlockingProvider) SupportsJSONMode(string) bool { return false }
