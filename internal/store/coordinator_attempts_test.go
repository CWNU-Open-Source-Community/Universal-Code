package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/session"
)

type specialistAttemptFixture struct {
	Run   domain.Run
	Root  domain.AgentNode
	Child domain.AgentNode
	Lease domain.RunExecutionLease
}

func TestSpecialistAttemptLifecycleChargesBudgetsAndReplaysExactlyOnce(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "attempt lifecycle", 2, 32)

	firstStart := newAttemptStart(fixture, idgen.New("attempt"))
	first, replayed, err := st.BeginSpecialistAttempt(ctx, firstStart, "attempt-start-operation-0001")
	if err != nil || replayed || first.Status != domain.AgentAttemptRunning || first.Turn != 1 {
		t.Fatalf("first attempt did not start: attempt=%#v replayed=%t err=%v", first, replayed, err)
	}
	replayedStart := firstStart
	replayedStart.AttemptID = idgen.New("attempt")
	replayedStart.StartedAt = time.Now().UTC()
	replay, replayed, err := st.BeginSpecialistAttempt(ctx, replayedStart,
		"attempt-start-operation-0001")
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatalf("start retry was not stable: attempt=%#v replayed=%t err=%v", replay, replayed, err)
	}

	usage := domain.AgentAttemptUsage{
		InputTokens: 5, OutputTokens: 4, TotalTokens: 9, ExecutionMillis: 12,
	}
	ref := attemptRef(first)
	charged, replayed, err := st.RecordSpecialistAttemptUsage(ctx, ref, usage,
		"attempt-usage-operation-0001")
	if err != nil || replayed || charged.UsageRecordedAt == nil || charged.Usage != usage {
		t.Fatalf("attempt usage was not charged: attempt=%#v replayed=%t err=%v",
			charged, replayed, err)
	}
	chargedReplay, replayed, err := st.RecordSpecialistAttemptUsage(ctx, ref, usage,
		"attempt-usage-operation-0001")
	if err != nil || !replayed || chargedReplay.Usage != usage {
		t.Fatalf("usage retry was not stable: attempt=%#v replayed=%t err=%v",
			chargedReplay, replayed, err)
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, ref, usage,
		"attempt-usage-operation-0002"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("usage was charged twice under a new key: code=%s err=%v", apperror.CodeOf(err), err)
	}

	continued, replayed, err := st.ContinueSpecialistAttempt(ctx, ref,
		"attempt-continue-operation-0001")
	if err != nil || replayed || continued.Status != domain.AgentAttemptContinued {
		t.Fatalf("attempt did not continue: attempt=%#v replayed=%t err=%v", continued, replayed, err)
	}
	continuedReplay, replayed, err := st.ContinueSpecialistAttempt(ctx, ref,
		"attempt-continue-operation-0001")
	if err != nil || !replayed || continuedReplay.Status != domain.AgentAttemptContinued {
		t.Fatalf("continuation retry was not stable: attempt=%#v replayed=%t err=%v",
			continuedReplay, replayed, err)
	}
	child, err := st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentReady || child.TurnsUsed != 1 ||
		child.TokensUsed != usage.TotalTokens || child.ActiveAttemptID != "" {
		t.Fatalf("continuation projection is invalid: child=%#v err=%v", child, err)
	}

	second, replayed, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "attempt-start-operation-0002")
	if err != nil || replayed || second.Turn != 2 {
		t.Fatalf("second attempt did not start: attempt=%#v replayed=%t err=%v", second, replayed, err)
	}
	secondUsage := domain.AgentAttemptUsage{
		InputTokens: 7, OutputTokens: 5, TotalTokens: 12, ExecutionMillis: 20,
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, attemptRef(second), secondUsage,
		"attempt-usage-operation-0003"); err != nil {
		t.Fatal(err)
	}
	rawSecret := "sk-" + strings.Repeat("x", 32)
	crashRequest := domain.AgentAttemptFailureRequest{
		Ref: attemptRef(second), Failure: domain.AgentAttemptFailure{
			Code: "provider_error", Reason: "provider returned " + rawSecret,
		}, NotificationMessageID: idgen.New("agentmsg"), FailedAt: time.Now().UTC(),
	}
	crashed, replayed, err := st.CrashSpecialistAttempt(ctx, crashRequest,
		"attempt-crash-operation-0001")
	if err != nil || replayed || crashed.Status != domain.AgentAttemptCrashed ||
		strings.Contains(crashed.Failure.Reason, rawSecret) ||
		!strings.Contains(crashed.Failure.Reason, "[REDACTED:api-key]") {
		t.Fatalf("crash was not durably redacted: attempt=%#v replayed=%t err=%v",
			crashed, replayed, err)
	}
	crashRequest.NotificationMessageID = idgen.New("agentmsg")
	crashRequest.FailedAt = time.Now().UTC()
	crashedReplay, replayed, err := st.CrashSpecialistAttempt(ctx, crashRequest,
		"attempt-crash-operation-0001")
	if err != nil || !replayed || crashedReplay.ID != crashed.ID ||
		crashedReplay.NotificationMessageID != crashed.NotificationMessageID {
		t.Fatalf("crash retry was not stable: attempt=%#v replayed=%t err=%v",
			crashedReplay, replayed, err)
	}
	child, err = st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentFailed || child.TurnsUsed != 2 ||
		child.TokensUsed != usage.TotalTokens+secondUsage.TotalTokens || child.FinishedAt == nil {
		t.Fatalf("exhausted child projection is invalid: child=%#v err=%v", child, err)
	}
	childSession, err := st.GetSession(ctx, fixture.Child.SessionID)
	if err != nil || childSession.Status != session.StatusArchived {
		t.Fatalf("exhausted child Session was not archived: session=%#v err=%v", childSession, err)
	}
	attempts, err := st.ListAgentAttempts(ctx, fixture.Child.ID)
	if err != nil || len(attempts) != 2 || attempts[0].Status != domain.AgentAttemptContinued ||
		attempts[1].Status != domain.AgentAttemptCrashed {
		t.Fatalf("attempt history is invalid: attempts=%#v err=%v", attempts, err)
	}
	messages, err := st.ListAgentMessages(ctx, fixture.Root.ID, true, 10)
	if err != nil || len(messages) != 1 || messages[0].Kind != domain.AgentMessageNotification ||
		strings.Contains(messages[0].PayloadJSON, rawSecret) {
		t.Fatalf("failure notification is invalid: messages=%#v err=%v", messages, err)
	}
	if _, err := st.RestoreAgentGraph(ctx, fixture.Run.ID); err != nil {
		t.Fatalf("attempt graph was not recoverable: %v", err)
	}
	timeline, err := st.ListRunEvents(ctx, fixture.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countRunEventType(timeline, events.AgentTurnStartedEvent) != 2 ||
		countRunEventType(timeline, events.AgentAttemptUsageRecordedEvent) != 2 ||
		countRunEventType(timeline, events.AgentTurnCompletedEvent) != 1 ||
		countRunEventType(timeline, events.AgentTurnFailedEvent) != 1 {
		t.Fatalf("attempt audit stream is incomplete: %#v", timeline)
	}
}

func TestSpecialistAttemptTakeoverFencesOldWorkerAndRecoversOnce(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixtureWithoutLease(t, ctx, st,
		"attempt takeover", 3, 64)
	firstLease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: fixture.Run.ID, OwnerID: "attempt-worker-a", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.Lease = firstLease.Lease
	first, _, err := st.BeginSpecialistAttempt(ctx, newAttemptStart(fixture, idgen.New("attempt")),
		"takeover-attempt-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	expireTestRunExecutionLease(t, ctx, st, firstLease.Lease)
	secondLease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: fixture.Run.ID, OwnerID: "attempt-worker-b", TTL: time.Minute,
	})
	if err != nil || !secondLease.TookOver || secondLease.Lease.Generation != 2 {
		t.Fatalf("attempt lease was not taken over: acquisition=%#v err=%v", secondLease, err)
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, attemptRef(first),
		domain.AgentAttemptUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		"stale-worker-usage-operation-0001"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale worker was not fenced: code=%s err=%v", apperror.CodeOf(err), err)
	}
	recovered, err := st.RecoverSpecialistAttempts(ctx, secondLease.Lease)
	if err != nil || len(recovered) != 1 || recovered[0].ID != first.ID ||
		recovered[0].Status != domain.AgentAttemptCrashed ||
		recovered[0].Failure.Code != "worker_lost" {
		t.Fatalf("stale attempt was not recovered: attempts=%#v err=%v", recovered, err)
	}
	replayedRecovery, err := st.RecoverSpecialistAttempts(ctx, secondLease.Lease)
	if err != nil || len(replayedRecovery) != 0 {
		t.Fatalf("recovery was not exactly once: attempts=%#v err=%v", replayedRecovery, err)
	}
	child, err := st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentReady || child.TurnsUsed != 1 ||
		child.TokensUsed != 0 || child.ActiveAttemptID != "" {
		t.Fatalf("recovered child projection is invalid: child=%#v err=%v", child, err)
	}
	fixture.Lease = secondLease.Lease
	second, replayed, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "takeover-attempt-start-0002")
	if err != nil || replayed || second.Turn != 2 || second.LeaseGeneration != 2 {
		t.Fatalf("successor worker did not resume: attempt=%#v replayed=%t err=%v", second, replayed, err)
	}
	if _, err := st.RestoreAgentGraph(ctx, fixture.Run.ID); err != nil {
		t.Fatalf("recovered graph did not restore: %v", err)
	}
}

func TestConcurrentSpecialistAttemptStartConvergesAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	firstStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, firstStore,
		"concurrent attempt", 2, 32)
	type result struct {
		attempt  domain.AgentAttempt
		replayed bool
		err      error
	}
	stores := []*SQLiteStore{firstStore, secondStore}
	results := make(chan result, len(stores))
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(len(stores))
	for _, st := range stores {
		st := st
		go func() {
			ready.Done()
			<-start
			attempt, replayed, err := st.BeginSpecialistAttempt(ctx,
				newAttemptStart(fixture, idgen.New("attempt")),
				"concurrent-attempt-start-0001")
			results <- result{attempt: attempt, replayed: replayed, err: err}
		}()
	}
	ready.Wait()
	close(start)
	created, replays := 0, 0
	attemptID := ""
	for range stores {
		item := <-results
		if item.err != nil {
			t.Fatalf("concurrent schedule failed: %v", item.err)
		}
		if attemptID == "" {
			attemptID = item.attempt.ID
		} else if item.attempt.ID != attemptID {
			t.Fatalf("concurrent schedule diverged: first=%s next=%s", attemptID, item.attempt.ID)
		}
		if item.replayed {
			replays++
		} else {
			created++
		}
	}
	if created != 1 || replays != 1 {
		t.Fatalf("expected one create and one replay, got create=%d replay=%d", created, replays)
	}
	attempts, err := firstStore.ListAgentAttempts(ctx, fixture.Child.ID)
	if err != nil || len(attempts) != 1 || attempts[0].ID != attemptID {
		t.Fatalf("concurrent schedule persisted duplicate attempts: attempts=%#v err=%v", attempts, err)
	}
	child, err := firstStore.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.TurnsUsed != 1 || child.ActiveAttemptID != attemptID {
		t.Fatalf("concurrent schedule charged budget twice: child=%#v err=%v", child, err)
	}
	timeline, err := firstStore.ListRunEvents(ctx, fixture.Run.ID)
	if err != nil || countRunEventType(timeline, events.AgentTurnStartedEvent) != 1 {
		t.Fatalf("concurrent schedule duplicated audit events: events=%#v err=%v", timeline, err)
	}
}

func TestSpecialistAttemptSQLiteTriggersRejectForgedAndStaleLeases(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixtureWithoutLease(t, ctx, st,
		"attempt trigger lease", 2, 32)
	acquired, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: fixture.Run.ID, OwnerID: "trigger-worker-a", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.Lease = acquired.Lease
	now := time.Now().UTC()
	forgedID := idgen.New("attempt")
	if _, err := st.db.ExecContext(ctx, `INSERT INTO agent_attempts
		(id, run_id, agent_id, parent_agent_id, lease_id, lease_generation, turn_number, status,
		input_tokens, output_tokens, total_tokens, execution_millis, usage_recorded_at,
		failure_code, failure_reason, notification_message_id, started_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, 0, 0, 0, 0, NULL, '', '', '', ?, ?, NULL)`,
		forgedID, fixture.Run.ID, fixture.Child.ID, fixture.Root.ID, "forged-lease", 99,
		domain.AgentAttemptRunning, ts(now), ts(now)); err == nil {
		t.Fatal("SQLite accepted an Agent attempt under a forged Run lease")
	}
	if _, found, err := st.GetAgentAttempt(ctx, forgedID); err != nil || found {
		t.Fatalf("forged attempt was persisted: found=%t err=%v", found, err)
	}
	started, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "trigger-attempt-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	expireTestRunExecutionLease(t, ctx, st, acquired.Lease)
	if _, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: fixture.Run.ID, OwnerID: "trigger-worker-b", TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_attempts SET input_tokens = 1,
		output_tokens = 1, total_tokens = 2, execution_millis = 1,
		usage_recorded_at = ?, updated_at = ? WHERE id = ?`, ts(time.Now().UTC()),
		ts(time.Now().UTC()), started.ID); err == nil {
		t.Fatal("SQLite accepted Agent usage after the attempt lease was replaced")
	}
	stored, found, err := st.GetAgentAttempt(ctx, started.ID)
	if err != nil || !found || stored.UsageRecordedAt != nil || stored.Usage.TotalTokens != 0 {
		t.Fatalf("stale direct usage changed the attempt: attempt=%#v found=%t err=%v",
			stored, found, err)
	}
}

func TestSpecialistAttemptEventFailureRollsBackEntireSchedule(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "attempt rollback", 2, 32)
	attemptID := idgen.New("attempt")
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_attempt_started_event
		BEFORE INSERT ON run_events WHEN NEW.type = 'agent.turn_started'
		BEGIN SELECT RAISE(ABORT, 'forced attempt event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginSpecialistAttempt(ctx, newAttemptStart(fixture, attemptID),
		"rollback-attempt-start-0001"); err == nil {
		t.Fatal("forced attempt event failure did not abort scheduling")
	}
	if attempt, found, err := st.GetAgentAttempt(ctx, attemptID); err != nil || found || attempt.ID != "" {
		t.Fatalf("failed schedule left an attempt: attempt=%#v found=%t err=%v", attempt, found, err)
	}
	child, err := st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentReady || child.TurnsUsed != 0 ||
		child.ActiveAttemptID != "" {
		t.Fatalf("failed schedule charged child budget: child=%#v err=%v", child, err)
	}
	var mutationCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_attempt_mutations`).
		Scan(&mutationCount); err != nil || mutationCount != 0 {
		t.Fatalf("failed schedule left mutation record: count=%d err=%v", mutationCount, err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_attempt_started_event`); err != nil {
		t.Fatal(err)
	}
	started, replayed, err := st.BeginSpecialistAttempt(ctx, newAttemptStart(fixture, attemptID),
		"rollback-attempt-start-0001")
	if err != nil || replayed || started.ID != attemptID {
		t.Fatalf("rolled-back operation could not retry: attempt=%#v replayed=%t err=%v",
			started, replayed, err)
	}
}

func TestRunPauseInterruptsAttemptBeforeSpecialistProjectionMoves(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "attempt pause", 3, 64)
	started, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "pause-attempt-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Pause(ctx, fixture.Run.ID); err != nil {
		t.Fatal(err)
	}
	interrupted, found, err := st.GetAgentAttempt(ctx, started.ID)
	if err != nil || !found || interrupted.Status != domain.AgentAttemptInterrupted ||
		interrupted.Failure.Code != "run_paused" || interrupted.NotificationMessageID != "" {
		t.Fatalf("paused attempt is invalid: attempt=%#v found=%t err=%v", interrupted, found, err)
	}
	child, err := st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentWaiting || child.ActiveAttemptID != "" ||
		child.StatusReason != "run paused" {
		t.Fatalf("paused child projection is invalid: child=%#v err=%v", child, err)
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, attemptRef(started),
		domain.AgentAttemptUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		"paused-attempt-usage-0001"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("interrupted attempt accepted usage: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := application.NewRunService(st).Resume(ctx, fixture.Run.ID); err != nil {
		t.Fatal(err)
	}
	child, err = st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentReady || child.TurnsUsed != 1 {
		t.Fatalf("resumed child is invalid: child=%#v err=%v", child, err)
	}
	resumed, replayed, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "pause-attempt-start-0002")
	if err != nil || replayed || resumed.Turn != 2 {
		t.Fatalf("resumed child did not start a fresh attempt: attempt=%#v replayed=%t err=%v",
			resumed, replayed, err)
	}
}

func TestSchemaV24PreservesReadySpecialistAndAddsAttemptRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v23.db")
	st := openCurrentTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"))

	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixtureWithoutLease(t, ctx, st,
		"attempt migration", 2, 32)
	st = historicalTestDatabaseFromSeed(t, st, path, 23)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	child, err := st.GetAgentNode(ctx, fixture.Child.ID)
	if err != nil || child.Status != domain.AgentReady || child.TurnsUsed != 0 {
		t.Fatalf("v23 Specialist was not preserved: child=%#v err=%v", child, err)
	}
	attempts, err := st.ListAgentAttempts(ctx, child.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("migration invented attempt history: attempts=%#v err=%v", attempts, err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, fixture.Run.ID)
	fixture.Lease = lease
	started, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "migrated-attempt-start-0001")
	if err != nil || started.Status != domain.AgentAttemptRunning {
		t.Fatalf("migrated runtime did not schedule: attempt=%#v err=%v", started, err)
	}
	var migrationCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 24`).
		Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("schema v24 ledger is inconsistent: count=%d err=%v", migrationCount, err)
	}
}

func prepareSpecialistAttemptFixture(t *testing.T, ctx context.Context, st *SQLiteStore,
	goal string, turnLimit int64, tokenLimit int64,
) specialistAttemptFixture {
	t.Helper()
	fixture := prepareSpecialistAttemptFixtureWithoutLease(t, ctx, st, goal, turnLimit, tokenLimit)
	fixture.Lease = acquireTestRunExecutionLease(t, ctx, st, fixture.Run.ID)
	return fixture
}

func prepareSpecialistAttemptFixtureWithoutLease(t *testing.T, ctx context.Context, st *SQLiteStore,
	goal string, turnLimit int64, tokenLimit int64,
) specialistAttemptFixture {
	t.Helper()
	_, run := createWorkItemTestRun(t, ctx, st, goal)
	started, err := application.NewRunService(st).Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not created: found=%t err=%v", found, err)
	}
	child, replayed, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{
		AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID,
		ParentAgentID: root.ID, Title: "attempt specialist", Skills: []string{"model.chat"},
		TurnLimit: turnLimit, TokenLimit: tokenLimit, MaxChildren: 2, CreatedAt: time.Now().UTC(),
	}, "attempt-fixture-admission")
	if err != nil || replayed {
		t.Fatalf("Specialist admission failed: child=%#v replayed=%t err=%v", child, replayed, err)
	}
	return specialistAttemptFixture{Run: started, Root: root, Child: child}
}

func newAttemptStart(fixture specialistAttemptFixture, attemptID string) domain.AgentAttemptStart {
	return domain.AgentAttemptStart{
		AttemptID: attemptID, RunID: fixture.Run.ID, AgentID: fixture.Child.ID,
		ParentAgentID: fixture.Root.ID, Lease: fixture.Lease, StartedAt: time.Now().UTC(),
	}
}

func attemptRef(attempt domain.AgentAttempt) domain.AgentAttemptRef {
	return domain.AgentAttemptRef{RunID: attempt.RunID, AgentID: attempt.AgentID, AttemptID: attempt.ID}
}
