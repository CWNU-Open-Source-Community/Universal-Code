package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
)

func TestSpecialistContextCommitsParentInstructionExactlyOnce(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "Specialist context commit", 2, 64)
	message := sendSpecialistInstructionTestMessage(t, ctx, st, fixture,
		"Review only the child-owned work and report evidence.", "specialist-context-send-0001")
	attempt, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "specialist-context-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := st.PrepareSpecialistContext(ctx, attemptRef(attempt))
	if err != nil || batch.Recovered || len(batch.Messages) != 1 ||
		batch.Messages[0].ID != message.ID {
		t.Fatalf("Specialist context was not prepared: batch=%#v err=%v", batch, err)
	}
	replayed, err := st.PrepareSpecialistContext(ctx, attemptRef(attempt))
	if err != nil || !replayed.Recovered || len(replayed.Messages) != 1 ||
		replayed.Messages[0].ID != message.ID {
		t.Fatalf("Specialist context replay changed: batch=%#v err=%v", replayed, err)
	}
	if _, err := st.ConsumeAgentMessages(ctx, fixture.Child.ID, 1); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("manual consume stole a prepared instruction: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	if _, err := st.RestoreAgentGraph(ctx, fixture.Run.ID); err != nil {
		t.Fatalf("prepared Specialist context did not restore: %v", err)
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, attemptRef(attempt),
		domain.AgentAttemptUsage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
		"specialist-context-usage-0001"); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.PrepareSpecialistContext(ctx, attemptRef(attempt))
	if err != nil || !recovered.Recovered || recovered.TaskBrief.Fingerprint != batch.TaskBrief.Fingerprint || len(recovered.Messages) != 1 || recovered.Messages[0].ID != message.ID {
		t.Fatalf("recorded usage prevented exact context recovery: %+v %v", recovered, err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_specialist_context_commit_event
		BEFORE INSERT ON run_events
		WHEN NEW.type = 'agent.inbox_context_committed' AND NEW.subject_id = '`+attempt.ID+`'
		BEGIN SELECT RAISE(ABORT, 'forced Specialist context commit failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ContinueSpecialistAttempt(ctx, attemptRef(attempt),
		"specialist-context-continue-0001"); err == nil {
		t.Fatal("forced context event failure did not roll back continuation")
	}
	pending, err := st.ListAgentMessages(ctx, fixture.Child.ID, true, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != message.ID {
		t.Fatalf("failed commit consumed the instruction: messages=%#v err=%v", pending, err)
	}
	prepared, err := listSpecialistContextDeliveriesDB(ctx, st, fixture.Run.ID,
		domain.RootInboxDeliveryPrepared)
	if err != nil || len(prepared) != 1 {
		t.Fatalf("failed commit changed the delivery: deliveries=%#v err=%v", prepared, err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_specialist_context_commit_event`); err != nil {
		t.Fatal(err)
	}
	continued, replayedMutation, err := st.ContinueSpecialistAttempt(ctx, attemptRef(attempt),
		"specialist-context-continue-0001")
	if err != nil || replayedMutation || continued.Status != domain.AgentAttemptContinued {
		t.Fatalf("Specialist context did not commit: attempt=%#v replayed=%t err=%v",
			continued, replayedMutation, err)
	}
	all, err := st.ListAgentMessages(ctx, fixture.Child.ID, false, 10)
	if err != nil || len(all) != 1 || all[0].Status != domain.AgentMessageConsumed ||
		all[0].ConsumedAt == nil {
		t.Fatalf("instruction was not consumed exactly once: messages=%#v err=%v", all, err)
	}
	committed, err := listSpecialistContextDeliveriesDB(ctx, st, fixture.Run.ID,
		domain.RootInboxDeliveryCommitted)
	if err != nil || len(committed) != 1 || committed[0].MessageID != message.ID {
		t.Fatalf("committed delivery is invalid: deliveries=%#v err=%v", committed, err)
	}
	if _, err := st.RestoreAgentGraph(ctx, fixture.Run.ID); err != nil {
		t.Fatalf("committed Specialist context did not restore: %v", err)
	}
	if countSpecialistContextEvent(t, st, fixture.Run.ID,
		events.AgentInboxContextPreparedEvent) != 1 ||
		countSpecialistContextEvent(t, st, fixture.Run.ID,
			events.AgentInboxContextCommittedEvent) != 1 {
		t.Fatal("Specialist context lifecycle events were not committed exactly once")
	}
}

func TestSpecialistContextCrashPreservesInstructionForFreshAttempt(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "Specialist context retry", 3, 64)
	message := sendSpecialistInstructionTestMessage(t, ctx, st, fixture,
		"Keep this instruction available after worker loss.", "specialist-context-send-0002")
	first, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "specialist-context-start-0002")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareSpecialistContext(ctx, attemptRef(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE specialist_context_deliveries
		SET status = 'superseded', resolved_at = ? WHERE agent_attempt_id = ?`,
		ts(time.Now().UTC()), first.ID); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("SQLite allowed an active context to be superseded: %v", err)
	}
	crashed, _, err := st.CrashSpecialistAttempt(ctx, domain.AgentAttemptFailureRequest{
		Ref: attemptRef(first), Failure: domain.AgentAttemptFailure{
			Code: "worker_lost", Reason: "worker stopped before lifecycle commit",
		}, NotificationMessageID: idgen.New("agentmsg"), FailedAt: time.Now().UTC(),
	}, "specialist-context-crash-0001")
	if err != nil || crashed.Status != domain.AgentAttemptCrashed {
		t.Fatalf("Specialist context crash failed: attempt=%#v err=%v", crashed, err)
	}
	pending, err := st.ListAgentMessages(ctx, fixture.Child.ID, true, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != message.ID {
		t.Fatalf("crash consumed the instruction: messages=%#v err=%v", pending, err)
	}
	superseded, err := listSpecialistContextDeliveriesDB(ctx, st, fixture.Run.ID,
		domain.RootInboxDeliverySuperseded)
	if err != nil || len(superseded) != 1 || superseded[0].AgentAttemptID != first.ID {
		t.Fatalf("crash did not supersede the old delivery: deliveries=%#v err=%v",
			superseded, err)
	}
	if countSpecialistContextEvent(t, st, fixture.Run.ID,
		events.AgentInboxContextSupersededEvent) != 1 {
		t.Fatal("Specialist context supersession event was not committed exactly once")
	}
	second, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "specialist-context-start-0003")
	if err != nil {
		t.Fatal(err)
	}
	secondBatch, err := st.PrepareSpecialistContext(ctx, attemptRef(second))
	if err != nil || secondBatch.Recovered || len(secondBatch.Messages) != 1 ||
		secondBatch.Messages[0].ID != message.ID {
		t.Fatalf("fresh attempt did not receive preserved instruction: batch=%#v err=%v",
			secondBatch, err)
	}
}

func TestSpecialistContextFinishConsumesInstructionWithCompletion(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "Specialist context finish", 1, 32)
	message := sendSpecialistInstructionTestMessage(t, ctx, st, fixture,
		"Finish with a bounded completion report.", "specialist-context-send-finish")
	attempt, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "specialist-context-start-finish")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareSpecialistContext(ctx, attemptRef(attempt)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, attemptRef(attempt),
		domain.AgentAttemptUsage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
		"specialist-context-usage-finish"); err != nil {
		t.Fatal(err)
	}
	completion := newCompletionTestValue(fixture.Run.ID, fixture.Root.ID, fixture.Child.ID,
		attempt.ID, domain.CompletionSucceeded, nil, nil, "bounded work completed")
	stored, replayed, err := st.FinishSpecialist(ctx, completion,
		"specialist-context-completion-finish")
	if err != nil || replayed || stored.AttemptID != attempt.ID {
		t.Fatalf("Specialist finish did not commit context: completion=%#v replayed=%t err=%v",
			stored, replayed, err)
	}
	messages, err := st.ListAgentMessages(ctx, fixture.Child.ID, false, 10)
	if err != nil || len(messages) != 1 || messages[0].ID != message.ID ||
		messages[0].Status != domain.AgentMessageConsumed {
		t.Fatalf("finish did not consume parent instruction: messages=%#v err=%v", messages, err)
	}
	committed, err := listSpecialistContextDeliveriesDB(ctx, st, fixture.Run.ID,
		domain.RootInboxDeliveryCommitted)
	if err != nil || len(committed) != 1 || committed[0].AgentAttemptID != attempt.ID {
		t.Fatalf("finish delivery is invalid: deliveries=%#v err=%v", committed, err)
	}
}

func TestSpecialistContextRejectsMalformedInstructionProtocol(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "Specialist context protocol", 2, 64)
	malformed, _, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: fixture.Run.ID, SenderAgentID: fixture.Root.ID,
		RecipientAgentID: fixture.Child.ID, Kind: domain.AgentMessageInstruction,
		Semantic:    domain.AgentMessageSemanticMessage,
		PayloadJSON: `{"instruction":"missing protocol version"}`,
	}, "specialist-context-malformed-send")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "specialist-context-start-0004")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareSpecialistContext(ctx, attemptRef(attempt)); err == nil ||
		!strings.Contains(err.Error(), "instruction payload") {
		t.Fatalf("malformed instruction entered Specialist context: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO specialist_context_deliveries
		(run_id, agent_id, parent_agent_id, agent_attempt_id, turn_number, message_id,
		ordinal, status, prepared_at, resolved_at) VALUES (?, ?, ?, ?, ?, ?, 1, 'prepared', ?, NULL)`,
		fixture.Run.ID, fixture.Child.ID, fixture.Root.ID, attempt.ID, attempt.Turn,
		malformed.ID, ts(time.Now().UTC())); err == nil ||
		!strings.Contains(err.Error(), "eligible pending parent instruction") {
		t.Fatalf("SQLite accepted malformed Specialist context delivery: %v", err)
	}
}

func TestSchemaV27PreservesSpecialistRuntimeAndAddsContextLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v26.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixtureWithoutLease(t, ctx, st,
		"Specialist context migration", 2, 32)
	st = historicalTestDatabaseFromSeed(t, st, path, 26)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("v26 database did not upgrade: version=%d err=%v", version, err)
	}
	fixture.Lease = acquireTestRunExecutionLease(t, ctx, st, fixture.Run.ID)
	message := sendSpecialistInstructionTestMessage(t, ctx, st, fixture,
		"Use the migrated context ledger.", "specialist-context-migrated-send")
	attempt, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "specialist-context-migrated-start")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := st.PrepareSpecialistContext(ctx, attemptRef(attempt))
	if err != nil || len(batch.Messages) != 1 || batch.Messages[0].ID != message.ID {
		t.Fatalf("migrated context ledger is unusable: batch=%#v err=%v", batch, err)
	}
}

func sendSpecialistInstructionTestMessage(t testing.TB, ctx context.Context, st *SQLiteStore,
	fixture specialistAttemptFixture, instruction string, operationKey string,
) domain.AgentMessage {
	t.Helper()
	payload, err := json.Marshal(domain.AgentInstructionPayload{
		Version: domain.SpecialistInstructionVersion, Instruction: instruction,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, replayed, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: fixture.Run.ID, SenderAgentID: fixture.Root.ID,
		RecipientAgentID: fixture.Child.ID, Kind: domain.AgentMessageInstruction,
		Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(payload),
	}, operationKey)
	if err != nil || replayed {
		t.Fatalf("send Specialist instruction: message=%#v replayed=%t err=%v",
			message, replayed, err)
	}
	return message
}

func listSpecialistContextDeliveriesDB(ctx context.Context, st *SQLiteStore, runID string,
	status domain.RootInboxDeliveryStatus,
) ([]domain.SpecialistContextDelivery, error) {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	items, err := listSpecialistContextDeliveriesTx(ctx, tx,
		specialistContextDeliverySelect+` WHERE run_id = ? AND status = ?
		ORDER BY agent_attempt_id, ordinal`, runID, status)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func countSpecialistContextEvent(t testing.TB, st *SQLiteStore, runID string,
	eventType string,
) int {
	t.Helper()
	timeline, err := st.ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range timeline {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
