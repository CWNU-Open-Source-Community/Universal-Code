package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestSpecialistOperatorScheduleConvergesAndContinuesExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-schedule.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	run, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 2)
	applicationState := applyOperatorScheduleFixture(t, ctx, st, proposal.ID,
		review.ReviewedBy)
	router := operatorScheduleRouter()
	services := []*application.SpecialistOperatorScheduleService{
		application.NewSpecialistOperatorScheduleService(st, router, policy.NewDefaultChecker()),
		application.NewSpecialistOperatorScheduleService(other, router, policy.NewDefaultChecker()),
	}
	request := application.ExecuteSpecialistOperatorScheduleRequest{
		ProposalID: proposal.ID, MaxRounds: 1,
		OperationKey: "operator-schedule-concurrent-0001", RequestedBy: review.ReviewedBy,
	}
	const workers = 8
	type scheduleResult struct {
		result application.ExecuteSpecialistOperatorScheduleResult
		err    error
	}
	results := make(chan scheduleResult, workers)
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func(service *application.SpecialistOperatorScheduleService) {
			defer wg.Done()
			result, err := service.Execute(ctx, request)
			results <- scheduleResult{result: result, err: err}
		}(services[index%len(services)])
	}
	wg.Wait()
	close(results)
	requestIDs := map[string]bool{}
	scheduleIDs := map[string]bool{}
	for current := range results {
		if current.err != nil {
			t.Fatal(current.err)
		}
		if current.result.Schedule.Status != domain.SpecialistScheduleCompleted ||
			current.result.Attempt.Ordinal != 1 || current.result.Schedule.TurnsStarted != 2 {
			t.Fatalf("concurrent schedule result drifted: %#v", current.result)
		}
		requestIDs[current.result.Request.ID] = true
		scheduleIDs[current.result.Schedule.ID] = true
	}
	if len(requestIDs) != 1 || len(scheduleIDs) != 1 {
		t.Fatalf("concurrent schedule did not converge: requests=%#v schedules=%#v",
			requestIDs, scheduleIDs)
	}
	assertOperatorScheduleCounts(t, ctx, st, run.ID, 1, 2, 1, 1, 1, 2, 2)
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.AgentOperatorScheduleRequestedEvent) != 1 ||
		countRunEventType(timeline, events.AgentScheduleStartedEvent) != 1 ||
		countRunEventType(timeline, events.AgentScheduleStoppedEvent) != 1 {
		t.Fatalf("operator schedule events diverged: %#v err=%v", timeline, err)
	}
	for _, event := range timeline {
		if strings.Contains(event.PayloadJSON, request.OperationKey) {
			t.Fatalf("raw operator schedule key reached events: %s", event.PayloadJSON)
		}
	}
	replayed, err := services[0].Execute(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Request.ID == "" ||
		replayed.Schedule.ID == "" {
		t.Fatalf("terminal schedule replay failed: %#v err=%v", replayed, err)
	}
	changed := request
	changed.MaxRounds = 2
	if _, err := services[0].Execute(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed schedule intent did not conflict: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	unauthorized := request
	unauthorized.OperationKey = "operator-schedule-unauthorized-0002"
	unauthorized.RequestedBy = "different_operator"
	if _, err := services[0].Execute(ctx, unauthorized); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("different application operator was accepted: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	continued := request
	continued.OperationKey = "operator-schedule-continue-0003"
	continuedResult, err := services[1].Execute(ctx, continued)
	if err != nil || continuedResult.Replayed || continuedResult.Request.ID == replayed.Request.ID ||
		continuedResult.Schedule.ID == replayed.Schedule.ID ||
		continuedResult.Schedule.Status != domain.SpecialistScheduleCompleted ||
		continuedResult.Schedule.TurnsStarted != 2 {
		t.Fatalf("explicit continuation failed: %#v err=%v", continuedResult, err)
	}
	assertOperatorScheduleCounts(t, ctx, st, run.ID, 2, 4, 2, 2, 2, 4, 4)
	if _, err := st.db.ExecContext(ctx, `UPDATE specialist_operator_schedule_requests
		SET max_rounds = 2 WHERE id = ?`, continuedResult.Request.ID); err == nil {
		t.Fatal("direct operator schedule request mutation succeeded")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM specialist_operator_schedule_attempts
		WHERE request_id = ?`, continuedResult.Request.ID); err == nil {
		t.Fatal("direct operator schedule attempt deletion succeeded")
	}
	var rawKeyCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_operator_schedule_operations
		WHERE operation_key_digest IN (?, ?) OR request_fingerprint IN (?, ?)`,
		request.OperationKey, continued.OperationKey, request.OperationKey,
		continued.OperationKey).Scan(&rawKeyCount); err != nil {
		t.Fatal(err)
	}
	if rawKeyCount != 0 {
		t.Fatal("raw operator schedule operation key was persisted")
	}
	if applicationState.AssignmentCount != 2 {
		t.Fatalf("application assignment count drifted: %#v", applicationState)
	}
}

func TestSpecialistOperatorSchedulePolicyDenialCreatesNoRequest(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "operator-schedule-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	applyOperatorScheduleFixture(t, ctx, st, proposal.ID, review.ReviewedBy)
	service := application.NewSpecialistOperatorScheduleService(st,
		operatorScheduleRouter(), denyingDelegationApplicationChecker{})
	_, err = service.Execute(ctx, application.ExecuteSpecialistOperatorScheduleRequest{
		ProposalID: proposal.ID, MaxRounds: 1,
		OperationKey: "operator-schedule-policy-denied", RequestedBy: review.ReviewedBy,
	})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("schedule Policy denial drifted: code=%s err=%v", apperror.CodeOf(err), err)
	}
	var requests, attempts int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM specialist_operator_schedule_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM specialist_operator_schedule_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || attempts != 0 {
		t.Fatalf("Policy denial created schedule state: requests=%d attempts=%d", requests, attempts)
	}
}

func TestSpecialistOperatorScheduleRecoversExpiredStartedAttempt(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "operator-schedule-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	applyOperatorScheduleFixture(t, ctx, st, proposal.ID, review.ReviewedBy)
	failing := &failFirstOperatorScheduleStartStore{SQLiteStore: st}
	request := application.ExecuteSpecialistOperatorScheduleRequest{
		ProposalID: proposal.ID, MaxRounds: 1,
		OperationKey: "operator-schedule-recovery-0001", RequestedBy: review.ReviewedBy,
	}
	created, err := application.NewSpecialistOperatorScheduleService(failing,
		operatorScheduleRouter(), policy.NewDefaultChecker()).Execute(ctx, request)
	if apperror.CodeOf(err) != apperror.CodeUnavailable || created.Request.ID == "" {
		t.Fatalf("injected start failure did not preserve request: %#v code=%s err=%v",
			created, apperror.CodeOf(err), err)
	}
	usage, err := st.GetRunAgentUsage(ctx, created.Request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: created.Request.RunID, OwnerID: "expired-operator-schedule-worker",
		TTL: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldID := idgen.New("schedule")
	started, err := st.StartSpecialistSchedule(ctx, domain.SpecialistScheduleStart{
		ID: oldID, RunID: created.Request.RunID, AgentIDs: created.Request.AgentIDs,
		MaxRounds: created.Request.MaxRounds, OperatorRequestID: created.Request.ID,
		Lease: lease.Lease, UsageBefore: usage, StartedAt: time.Now().UTC(),
	})
	if err != nil || started.Schedule.ID != oldID {
		t.Fatalf("seed operator schedule did not start: %#v err=%v", started, err)
	}
	time.Sleep(220 * time.Millisecond)
	recovered, err := application.NewSpecialistOperatorScheduleService(st,
		operatorScheduleRouter(), policy.NewDefaultChecker()).Execute(ctx, request)
	if err != nil || !recovered.Replayed || !recovered.Recovered ||
		recovered.Attempt.Ordinal != 2 || recovered.Schedule.ID == oldID ||
		recovered.Schedule.Status != domain.SpecialistScheduleCompleted {
		t.Fatalf("expired operator schedule did not recover: %#v err=%v", recovered, err)
	}
	old, err := st.GetSpecialistSchedule(ctx, oldID)
	if err != nil || old.Status != domain.SpecialistScheduleAbandoned {
		t.Fatalf("expired schedule was not abandoned: %#v err=%v", old, err)
	}
}

func TestSchemaV37ApplicationSurvivesOperatorScheduleMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-schedule-v37.db")
	st := openCurrentTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"))

	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	applicationState := applyOperatorScheduleFixture(t, ctx, st, proposal.ID,
		review.ReviewedBy)
	st = historicalTestDatabaseFromSeed(t, st, path, 37)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v37 did not upgrade: version=%d err=%v", version, err)
	}
	loaded, found, err := upgraded.GetSpecialistDelegationApplicationByProposal(ctx,
		proposal.ID)
	if err != nil || !found || loaded.ID != applicationState.ID ||
		loaded.Status != domain.SpecialistDelegationApplied {
		t.Fatalf("v37 application did not survive v38: %#v found=%t err=%v",
			loaded, found, err)
	}
	for _, table := range []string{
		"specialist_operator_schedule_requests",
		"specialist_operator_schedule_request_agents",
		"specialist_operator_schedule_operations",
		"specialist_operator_schedule_attempts",
	} {
		var count int
		if err := upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration synthesized %s rows=%d err=%v", table, count, err)
		}
	}
}

type failFirstOperatorScheduleStartStore struct {
	*SQLiteStore
	failed atomic.Bool
}

func (s *failFirstOperatorScheduleStartStore) StartSpecialistSchedule(ctx context.Context,
	start domain.SpecialistScheduleStart,
) (domain.SpecialistScheduleStartResult, error) {
	if s.failed.CompareAndSwap(false, true) {
		return domain.SpecialistScheduleStartResult{}, apperror.New(
			apperror.CodeUnavailable, "injected operator schedule start failure")
	}
	return s.SQLiteStore.StartSpecialistSchedule(ctx, start)
}

func applyOperatorScheduleFixture(t *testing.T, ctx context.Context, st *SQLiteStore,
	proposalID, requestedBy string,
) domain.SpecialistDelegationApplication {
	t.Helper()
	service, err := application.NewDefaultSpecialistDelegationApplicationService(
		st, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, application.ApplySpecialistDelegationRequest{
		ProposalID: proposalID, OperationKey: "application-for-operator-schedule",
		RequestedBy: requestedBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Application
}

func operatorScheduleRouter() *llm.Router {
	provider := &operatorScheduleProvider{calls: map[string]int{}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return router
}

type operatorScheduleProvider struct {
	mu    sync.Mutex
	calls map[string]int
}

func (*operatorScheduleProvider) Name() string { return "operator-schedule-test" }

func (p *operatorScheduleProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: p.Name()}}, nil
}

func (p *operatorScheduleProvider) Chat(ctx context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	agentID := request.Metadata["agent_id"]
	p.mu.Lock()
	p.calls[agentID]++
	call := p.calls[agentID]
	p.mu.Unlock()
	action := domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion,
		Kind:    domain.SpecialistActionContinue, Message: "continue bounded operator work",
	}
	if call > 1 {
		action.Kind = domain.SpecialistActionFinish
		action.Message = "complete bounded operator work"
		action.Report = &domain.CompletionReport{
			Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
			Summary:     "Operator-scheduled Specialist work completed.",
			WorkItemIDs: []string{}, NoteIDs: []string{},
		}
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	return &llm.ChatResponse{
		Text: string(encoded), Provider: p.Name(), Model: "model",
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}

func (p *operatorScheduleProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*operatorScheduleProvider) SupportsTools(string) bool    { return false }
func (*operatorScheduleProvider) SupportsVision(string) bool   { return false }
func (*operatorScheduleProvider) SupportsJSONMode(string) bool { return true }

func assertOperatorScheduleCounts(t *testing.T, ctx context.Context, st *SQLiteStore,
	runID string, requests, requestAgents, operations, mappings, schedules,
	agentAttempts, modelCalls int,
) {
	t.Helper()
	queries := []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM specialist_operator_schedule_requests WHERE run_id = ?`, requests},
		{`SELECT COUNT(*) FROM specialist_operator_schedule_request_agents WHERE run_id = ?`, requestAgents},
		{`SELECT COUNT(*) FROM specialist_operator_schedule_operations WHERE run_id = ?`, operations},
		{`SELECT COUNT(*) FROM specialist_operator_schedule_attempts attempt
			JOIN specialist_operator_schedule_requests request ON request.id = attempt.request_id
			WHERE request.run_id = ?`, mappings},
		{`SELECT COUNT(*) FROM specialist_schedules WHERE run_id = ?`, schedules},
		{`SELECT COUNT(*) FROM agent_attempts WHERE run_id = ?`, agentAttempts},
		{`SELECT COUNT(*) FROM specialist_model_calls WHERE run_id = ?`, modelCalls},
	}
	for _, check := range queries {
		var got int
		if err := st.db.QueryRowContext(ctx, check.query, runID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("operator schedule count mismatch: query=%q got=%d want=%d",
				check.query, got, check.want)
		}
	}
}
