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

func TestSpecialistCompletionIsAtomicIdempotentAndRecoverable(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	mission, run, root, child, attemptID := prepareRunningSpecialist(t, ctx, st,
		"persist a child completion")

	workItem, err := application.NewWorkItemService(st).Create(ctx, application.CreateWorkItemRequest{
		RunID: run.ID, Title: "handoff parser audit", OwnerAgentID: child.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedNote, err := application.NewNoteService(st).Create(ctx, application.CreateNoteRequest{
		RunID: run.ID, Title: "shared finding", Content: "parent-visible context",
		Visibility: string(domain.NoteVisibilityRoot), OwnerAgentID: child.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateNote, err := application.NewNoteService(st).Create(ctx, application.CreateNoteRequest{
		RunID: run.ID, Title: "private scratchpad", Content: "child-only context",
		Visibility: string(domain.NoteVisibilityOwner), OwnerAgentID: child.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	succeeded := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID,
		domain.CompletionSucceeded, []string{workItem.ID}, nil, "done")
	if _, _, err := st.FinishSpecialist(ctx, succeeded,
		"completion-success-with-active-work"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("successful completion left active work: code=%s err=%v", apperror.CodeOf(err), err)
	}
	partialMissingWork := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID,
		domain.CompletionPartial, nil, nil, "handoff missing")
	if _, _, err := st.FinishSpecialist(ctx, partialMissingWork,
		"completion-partial-missing-work"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("partial completion omitted active work: code=%s err=%v", apperror.CodeOf(err), err)
	}
	privateReference := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID,
		domain.CompletionPartial, []string{workItem.ID}, []string{privateNote.ID}, "private handoff")
	if _, _, err := st.FinishSpecialist(ctx, privateReference,
		"completion-private-note"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("private completion Note was accepted: code=%s err=%v", apperror.CodeOf(err), err)
	}
	oversizedSecret := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID,
		domain.CompletionPartial, []string{workItem.ID}, []string{sharedNote.ID},
		"sk-"+strings.Repeat("q", domain.MaxCompletionSummaryBytes+128))
	if _, _, err := st.FinishSpecialist(ctx, oversizedSecret,
		"completion-oversized-secret"); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("oversized pre-redaction completion was accepted: code=%s err=%v",
			apperror.CodeOf(err), err)
	}

	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_completion_reported_event
		BEFORE INSERT ON run_events WHEN NEW.type = 'agent.completion_reported'
		BEGIN SELECT RAISE(ABORT, 'forced completion event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	rawSecret := "sk-" + strings.Repeat("z", 32)
	valid := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID,
		domain.CompletionPartial, []string{workItem.ID}, []string{sharedNote.ID},
		"handoff complete "+rawSecret)
	operationKey := "completion-atomic-operation-0001"
	if _, _, err := st.FinishSpecialist(ctx, valid, operationKey); err == nil {
		t.Fatal("forced completion event failure did not abort the transaction")
	}
	assertCompletionRolledBack(t, ctx, st, child, root)
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_completion_reported_event`); err != nil {
		t.Fatal(err)
	}

	completed, replayed, err := st.FinishSpecialist(ctx, valid, operationKey)
	if err != nil || replayed {
		t.Fatalf("valid completion failed: completion=%#v replayed=%t err=%v", completed, replayed, err)
	}
	if completed.ID != valid.ID || completed.MessageID != valid.MessageID ||
		completed.Report.Outcome != domain.CompletionPartial ||
		strings.Contains(completed.Report.Summary, rawSecret) ||
		!strings.Contains(completed.Report.Summary, "[REDACTED:api-key]") {
		t.Fatalf("stored completion is invalid or unredacted: %#v", completed)
	}
	stored, found, err := st.GetAgentCompletion(ctx, child.ID)
	if err != nil || !found || stored.ID != completed.ID ||
		len(stored.Report.WorkItemIDs) != 1 || stored.Report.WorkItemIDs[0] != workItem.ID ||
		len(stored.Report.NoteIDs) != 1 || stored.Report.NoteIDs[0] != sharedNote.ID {
		t.Fatalf("completion report was not recoverable: found=%t completion=%#v err=%v",
			found, stored, err)
	}
	completedChild, err := st.GetAgentNode(ctx, child.ID)
	if err != nil || completedChild.Status != domain.AgentCompleted || completedChild.FinishedAt == nil ||
		completedChild.ActiveAttemptID != "" || completedChild.StatusReason != "partial completion reported" {
		t.Fatalf("Specialist was not completed atomically: child=%#v err=%v", completedChild, err)
	}
	childSession, err := st.GetSession(ctx, child.SessionID)
	if err != nil || childSession.Status != session.StatusArchived {
		t.Fatalf("Specialist Session was not archived: session=%#v err=%v", childSession, err)
	}
	inbox, err := st.ListAgentMessages(ctx, root.ID, true, 10)
	if err != nil || len(inbox) != 1 || inbox[0].ID != completed.MessageID ||
		inbox[0].Kind != domain.AgentMessageResult || strings.Contains(inbox[0].PayloadJSON, rawSecret) ||
		!strings.Contains(inbox[0].PayloadJSON, completed.ID) {
		t.Fatalf("parent completion inbox is invalid: inbox=%#v err=%v", inbox, err)
	}
	graph, err := st.RestoreAgentGraph(ctx, run.ID)
	if err != nil || len(graph.Nodes) != 2 || len(graph.PendingMessages) != 1 {
		t.Fatalf("completed graph was not recoverable: graph=%#v err=%v", graph, err)
	}

	retry := valid
	retry.ID = idgen.New("completion")
	retry.MessageID = idgen.New("agentmsg")
	retry.CreatedAt = time.Now().UTC().Add(time.Minute)
	repeated, replayed, err := st.FinishSpecialist(ctx, retry, operationKey)
	if err != nil || !replayed || repeated.ID != completed.ID || repeated.MessageID != completed.MessageID {
		t.Fatalf("completion replay was not stable: completion=%#v replayed=%t err=%v",
			repeated, replayed, err)
	}
	retry.Report.Summary = "different completion intent"
	if _, _, err := st.FinishSpecialist(ctx, retry, operationKey); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed completion intent did not conflict: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, _, err := st.FinishSpecialist(ctx, retry,
		"completion-stale-attempt-operation"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("stale attempt completed twice: code=%s err=%v", apperror.CodeOf(err), err)
	}

	items, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(items, events.AgentCompletionReportedEvent) != 1 ||
		countRunEventType(items, events.AgentMessageSentEvent) != 1 {
		t.Fatalf("completion audit events are invalid: events=%#v err=%v", items, err)
	}
	for _, item := range items {
		if strings.Contains(item.PayloadJSON, rawSecret) || strings.Contains(item.PayloadJSON, operationKey) {
			t.Fatalf("completion event leaked sensitive input: %#v", item)
		}
	}
	var storedDigest string
	if err := st.db.QueryRowContext(ctx, `SELECT operation_key_digest FROM agent_completion_operations
		WHERE report_id = ?`, completed.ID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if storedDigest == operationKey || len(storedDigest) != 64 {
		t.Fatalf("raw completion operation key was persisted: %q", storedDigest)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_completion_reports SET summary = ? WHERE id = ?`,
		"tampered", completed.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("SQLite allowed a completion report update: %v", err)
	}
	unchanged, found, err := st.GetAgentCompletion(ctx, child.ID)
	if err != nil || !found || unchanged.Report.Summary != completed.Report.Summary {
		t.Fatalf("rejected report update changed durable state: completion=%#v found=%t err=%v",
			unchanged, found, err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM agent_completion_reports WHERE id = ?`,
		completed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RestoreAgentGraph(ctx, run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("graph restore accepted a completed Specialist without its report: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	_ = mission
}

func TestConcurrentSpecialistCompletionConvergesAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completion.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx := context.Background()
	_, run, root, child, attemptID := prepareRunningSpecialist(t, ctx, first,
		"converge child completion")
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	type result struct {
		completion domain.AgentCompletion
		replayed   bool
		err        error
	}
	results := make(chan result, 2)
	stores := []*SQLiteStore{first, second}
	var wait sync.WaitGroup
	for index, current := range stores {
		wait.Add(1)
		go func(index int, current *SQLiteStore) {
			defer wait.Done()
			completion := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID,
				domain.CompletionSucceeded, nil, nil, "specialist completed")
			completion.CreatedAt = completion.CreatedAt.Add(time.Duration(index) * time.Millisecond)
			stored, replayed, err := current.FinishSpecialist(ctx, completion,
				"completion-concurrent-operation")
			results <- result{completion: stored, replayed: replayed, err: err}
		}(index, current)
	}
	wait.Wait()
	close(results)
	reportID := ""
	replays := 0
	for current := range results {
		if current.err != nil {
			t.Fatalf("concurrent completion failed: %v", current.err)
		}
		if reportID == "" {
			reportID = current.completion.ID
		}
		if current.completion.ID != reportID {
			t.Fatalf("concurrent completion returned different reports: %s and %s",
				reportID, current.completion.ID)
		}
		if current.replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf("concurrent completion replay count = %d, want 1", replays)
	}
	var reports, operations, messages int
	if err := first.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_completion_reports`).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_completion_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages
		WHERE sender_agent_id = ? AND recipient_agent_id = ? AND kind = 'result'`, child.ID, root.ID).
		Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if reports != 1 || operations != 1 || messages != 1 {
		t.Fatalf("concurrent completion duplicated state: reports=%d operations=%d messages=%d",
			reports, operations, messages)
	}
}

func TestSQLiteUpgradesV22ToSpecialistCompletionReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v22.db")
	st := openCurrentTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"))

	ctx := context.Background()
	_, run := createWorkItemTestRun(t, ctx, st, "upgrade completion protocol")
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root was not created before migration simulation: found=%t err=%v", found, err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 22)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	version, err := st.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("v22 database did not upgrade to v23: version=%d err=%v", version, err)
	}
	preserved, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found || preserved.ID != root.ID {
		t.Fatalf("v22 Agent graph was not preserved: root=%#v found=%t err=%v", preserved, found, err)
	}
	if completion, found, err := st.GetAgentCompletion(ctx, root.ID); err != nil || found || completion.ID != "" {
		t.Fatalf("migration invented a root completion: completion=%#v found=%t err=%v",
			completion, found, err)
	}
	var migrationCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 23`).
		Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("schema v23 migration ledger is inconsistent: count=%d err=%v", migrationCount, err)
	}
}

func prepareRunningSpecialist(t *testing.T, ctx context.Context, st *SQLiteStore,
	goal string,
) (domain.Mission, domain.Run, domain.AgentNode, domain.AgentNode, string) {
	t.Helper()
	mission, run := createWorkItemTestRun(t, ctx, st, goal)
	started, err := application.NewRunService(st).Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run = started
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not created: found=%t err=%v", found, err)
	}
	child, replayed, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{
		AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID,
		ParentAgentID: root.ID, Title: "completion specialist",
		Skills:    []string{"model.chat", "note_create", "work_item_create"},
		TurnLimit: 2, TokenLimit: 64, MaxChildren: 2, CreatedAt: time.Now().UTC(),
	}, "completion-specialist-admission")
	if err != nil || replayed {
		t.Fatalf("specialist admission failed: child=%#v replayed=%t err=%v", child, replayed, err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	attempt, replayed, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: child.ID,
		ParentAgentID: root.ID, Lease: lease, StartedAt: time.Now().UTC(),
	}, "completion-specialist-start")
	if err != nil || replayed {
		t.Fatalf("test Specialist was not scheduled: attempt=%#v replayed=%t err=%v",
			attempt, replayed, err)
	}
	attempt, replayed, err = st.RecordSpecialistAttemptUsage(ctx, domain.AgentAttemptRef{
		RunID: run.ID, AgentID: child.ID, AttemptID: attempt.ID,
	}, domain.AgentAttemptUsage{
		InputTokens: 4, OutputTokens: 3, TotalTokens: 7, ExecutionMillis: 10,
	}, "completion-specialist-usage")
	if err != nil || replayed {
		t.Fatalf("test Specialist usage was not recorded: attempt=%#v replayed=%t err=%v",
			attempt, replayed, err)
	}
	child, err = st.GetAgentNode(ctx, child.ID)
	if err != nil || child.Status != domain.AgentRunning || child.ActiveAttemptID != attempt.ID {
		t.Fatalf("running Specialist projection is invalid: child=%#v err=%v", child, err)
	}
	return mission, run, root, child, attempt.ID
}

func newCompletionTestValue(runID string, parentID string, childID string, attemptID string,
	outcome domain.CompletionOutcome, workItemIDs []string, noteIDs []string,
	summary string,
) domain.AgentCompletion {
	if workItemIDs == nil {
		workItemIDs = []string{}
	}
	if noteIDs == nil {
		noteIDs = []string{}
	}
	return domain.AgentCompletion{
		ID: idgen.New("completion"), RunID: runID, AgentID: childID, ParentAgentID: parentID,
		AttemptID: attemptID, Report: domain.CompletionReport{
			Version: domain.CompletionReportVersion, Outcome: outcome, Summary: summary,
			WorkItemIDs: workItemIDs, NoteIDs: noteIDs,
		},
		MessageID: idgen.New("agentmsg"), CreatedAt: time.Now().UTC(),
	}
}

func assertCompletionRolledBack(t *testing.T, ctx context.Context, st *SQLiteStore,
	child domain.AgentNode, root domain.AgentNode,
) {
	t.Helper()
	current, err := st.GetAgentNode(ctx, child.ID)
	if err != nil || current.Status != domain.AgentRunning ||
		current.ActiveAttemptID != child.ActiveAttemptID {
		t.Fatalf("failed completion changed child: child=%#v err=%v", current, err)
	}
	childSession, err := st.GetSession(ctx, child.SessionID)
	if err != nil || childSession.Status != session.StatusActive {
		t.Fatalf("failed completion archived Session: session=%#v err=%v", childSession, err)
	}
	if completion, found, err := st.GetAgentCompletion(ctx, child.ID); err != nil || found || completion.ID != "" {
		t.Fatalf("failed completion left report: completion=%#v found=%t err=%v",
			completion, found, err)
	}
	messages, err := st.ListAgentMessages(ctx, root.ID, true, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("failed completion left parent message: messages=%#v err=%v", messages, err)
	}
	var operations int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_completion_operations`).
		Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("failed completion left operation ledger: count=%d err=%v", operations, err)
	}
}
