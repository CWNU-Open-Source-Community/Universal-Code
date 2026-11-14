package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/threadtranscript"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func newThreadTaskTest(t *testing.T) (*SQLiteStore, domain.Run) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveWorkspace(t.Context(), WorkspaceRecord{ID: "thread-task-workspace", Name: "task-workspace", RootPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := newMigrationFixtureRunService(t, st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Task boundaries", Profile: "code", Interactive: true, WorkspaceID: "thread-task-workspace",
		Budget: domain.Budget{MaxTurns: 20, MaxToolCalls: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = application.NewRunService(st).Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

func queueThreadTask(t *testing.T, st *SQLiteStore, run domain.Run, key string) string {
	t.Helper()
	queued, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "same text", OperationKey: "thread-task-input-" + key,
		RequestedBy: "store_test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	return queued.Message.ID
}

func prepareThreadTask(t *testing.T, st *SQLiteStore, run domain.Run, maxSteps int) (domain.RunExecutionHandoffOperation, domain.RunExecutionLease) {
	t.Helper()
	key := idgen.New("thread-task-handoff")
	op := domain.RunExecutionHandoffOperation{
		ID: idgen.New("run-handoff"), ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
		KeyDigest:          runmutation.RunExecutionHandoffOperationDigest(run.ID, key),
		RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(run.ID, "store_test_operator", maxSteps),
		RunID:              run.ID, SessionID: run.SessionID, RequestedBy: "store_test_operator", MaxSteps: maxSteps, CreatedAt: time.Now().UTC(),
	}
	if _, _, err := st.PrepareRunExecutionHandoff(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	return op, acquireTestRunExecutionLease(t, t.Context(), st, run.ID)
}

func beginThreadTask(t *testing.T, st *SQLiteStore, lease domain.RunExecutionLease, input string) domain.SupervisorCheckpoint {
	t.Helper()
	turn, err := st.BeginSupervisorSteeringTurnForMessage(t.Context(), lease, input)
	if err != nil {
		t.Fatal(err)
	}
	return turn.Checkpoint
}

func completeThreadTaskSegment(t *testing.T, st *SQLiteStore, cp domain.SupervisorCheckpoint, boundary bool) domain.SupervisorCheckpoint {
	t.Helper()
	if boundary {
		for round := 0; round < domain.MaxSupervisorToolRounds; round++ {
			a := startToolRepairModel(t, st, cp, 0, round)
			a.Outcome = llm.OutcomeSuccess
			call := toolRepairNote(t, cp)
			call.ID, _ = runmutation.SupervisorToolCallID(runmutation.SupervisorToolOperationKey(cp.RunID, cp.NextTurn, call.Name, string(call.Arguments)), round+1)
			response := llm.ChatResponse{ToolCalls: []llm.ToolCall{call}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
			var err error
			cp, err = st.RecordSupervisorModelCompleted(t.Context(), cp, a, response)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.RecordSupervisorToolExecutionStarted(t.Context(), cp, call.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err = st.RecordSupervisorToolResult(t.Context(), cp, domain.SupervisorToolResult{CallID: call.ID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"status":"completed"}`, CompletedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	round := 0
	if boundary {
		round = domain.MaxSupervisorToolRounds
	}
	a := startToolRepairModel(t, st, cp, 0, round)
	a.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Text: "finished segment", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	var err error
	cp, err = st.RecordSupervisorModelCompleted(t.Context(), cp, a, response)
	if err != nil {
		t.Fatal(err)
	}
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish, Message: "finished segment", Summary: "finished segment"}
	if boundary {
		action.Kind = domain.RootActionContinue
		action.Summary = ""
		_, cp, _, err = st.CompleteSupervisorToolBoundary(t.Context(), cp, response, action, policy.Decision{Allowed: true}, 0)
	} else {
		_, cp, _, err = st.CompleteSupervisorTurn(t.Context(), cp, response, action, policy.Decision{Allowed: true}, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	return cp
}

func endThreadTask(t *testing.T, st *SQLiteStore, op domain.RunExecutionHandoffOperation, lease domain.RunExecutionLease, failed bool, steps int) {
	t.Helper()
	status, reason, code := domain.RunExecutionHandoffCompleted, "selection_drained", ""
	if failed {
		status, reason, code = domain.RunExecutionHandoffFailed, "cancelled", "cancelled"
	}
	if _, _, err := st.CompleteRunExecutionHandoff(t.Context(), op.ID, lease, status, reason, code, steps, true, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
}

func applyThreadTaskEdit(t *testing.T, st *SQLiteStore, run domain.Run, name string) string {
	t.Helper()
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := st.GetWorkspaceByID(t.Context(), mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	manager := fileedit.NewManager(st)
	edit, err := manager.Propose(t.Context(), fileedit.Proposal{SessionID: run.SessionID, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath, Path: name, Operation: fileedit.OperationCreate, ExpectedOriginalHash: "missing", ProposedText: "task change\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Approve(t.Context(), edit.ID, workspace.RootPath); err != nil {
		t.Fatal(err)
	}
	return edit.ID
}

func taskIndexForTest(t *testing.T, st *SQLiteStore, run domain.Run) *threadTaskIndex {
	t.Helper()
	conn, finish, err := st.beginThreadRequestObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	index, err := loadThreadTaskIndex(t.Context(), conn, domain.InitialThreadID(run.ID), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestThreadTasksGroupToolSegmentsAndPageOutsideInput(t *testing.T) {
	st, run := newThreadTaskTest(t)
	input := queueThreadTask(t, st, run, "segments")
	op, lease := prepareThreadTask(t, st, run, 1)
	cp := beginThreadTask(t, st, lease, input)
	firstEdit := applyThreadTaskEdit(t, st, run, "first.txt")
	cp = completeThreadTaskSegment(t, st, cp, true)
	index := taskIndexForTest(t, st, run)
	if got := index.tasks[input].task; got.State != "running" || got.FinalMessageID != "" || got.ChangeSourceEventID != "" {
		t.Fatalf("internal yield ended task: %+v", got)
	}
	secondEdit := applyThreadTaskEdit(t, st, run, "second.txt")
	completeThreadTaskSegment(t, st, cp, false)
	if got := taskIndexForTest(t, st, run).tasks[input].task; got.State != "running" {
		t.Fatalf("unconfirmed handoff ended task: %+v", got)
	}
	endThreadTask(t, st, op, lease, false, 1)
	index = taskIndexForTest(t, st, run)
	span := index.tasks[input]
	if span.task.State != "completed" || span.task.FinalMessageID == "" || span.task.FinalMessageID != span.task.ChangeSourceEventID || len(index.attempts) != 2 {
		t.Fatalf("task metadata: %+v attempts=%v", span.task, index.attempts)
	}
	threadID := domain.InitialThreadID(run.ID)
	_, facts, err := st.ListThreadReplyFileEditEvents(t.Context(), threadID, span.task.ChangeSourceEventID, 100)
	if err != nil || len(facts) != 2 || facts[0].SubjectID != firstEdit || facts[1].SubjectID != secondEdit {
		t.Fatalf("whole task edits: %+v %v", facts, err)
	}
	summary, err := application.NewThreadChangeService(st).Summary(t.Context(), threadID, span.task.ChangeSourceEventID)
	if err != nil || summary.FileCount != 2 {
		t.Fatalf("task summary: %+v %v", summary, err)
	}
	var before int64
	foundFinal, foundInput := false, false
	for page := 0; page < 100; page++ {
		ordinal := int64(0)
		if before > 0 {
			ordinal = 1
		}
		sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), threadID, ordinal, before, 7)
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) == 0 {
			break
		}
		items, err := threadtranscript.Build(threadID, sources)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.ID == span.task.FinalMessageID {
				foundFinal = true
				if item.Task == nil || item.Task.ID != input || item.Task.FinalMessageID != item.ID {
					t.Fatalf("paged final lost task: %+v", item)
				}
			}
			if item.SourceRef == input {
				foundInput = true
				if item.Task == nil || item.Task.ID != input {
					t.Fatalf("paged input lost task: %+v", item)
				}
			}
		}
		before = sources[len(sources)-1].Sequence
		if before == 0 {
			break
		}
	}
	if !foundFinal || !foundInput {
		t.Fatalf("paged records missing final=%t input=%t", foundFinal, foundInput)
	}
}

func TestThreadTasksSeparateSameTextInputsInOneHandoff(t *testing.T) {
	st, run := newThreadTaskTest(t)
	first, second := queueThreadTask(t, st, run, "first"), queueThreadTask(t, st, run, "second")
	op, lease := prepareThreadTask(t, st, run, 2)
	for i, input := range []string{first, second} {
		cp := beginThreadTask(t, st, lease, input)
		applyThreadTaskEdit(t, st, run, fmt.Sprintf("task-%d.txt", i))
		completeThreadTaskSegment(t, st, cp, false)
	}
	endThreadTask(t, st, op, lease, false, 2)
	index := taskIndexForTest(t, st, run)
	if len(index.tasks) != 2 {
		t.Fatalf("same text inputs collapsed: %+v", index.tasks)
	}
	for _, input := range []string{first, second} {
		task := index.tasks[input].task
		if task.State != "completed" || task.FinalMessageID == "" {
			t.Fatalf("input not completed: %+v", task)
		}
		_, facts, err := st.ListThreadReplyFileEditEvents(t.Context(), domain.InitialThreadID(run.ID), task.ChangeSourceEventID, 100)
		if err != nil || len(facts) != 1 {
			t.Fatalf("cross-input edits: %+v %v", facts, err)
		}
	}
	if index.tasks[first].task.FinalMessageID == index.tasks[second].task.FinalMessageID {
		t.Fatal("two inputs share final")
	}
}

func TestThreadTasksInterruptedChangesAndResumedAttempt(t *testing.T) {
	st, run := newThreadTaskTest(t)
	input := queueThreadTask(t, st, run, "interrupt")
	op, lease := prepareThreadTask(t, st, run, 1)
	cp := beginThreadTask(t, st, lease, input)
	steering, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "correction", DeliveryMode: domain.OperatorSteeringCurrentTurn,
		OperationKey: "thread-task-current-correction", RequestedBy: "store_test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if index := taskIndexForTest(t, st, run); index.inputs[steering.Message.ID] != input {
		t.Fatal("steering did not follow exact active attempt")
	}
	edit := applyThreadTaskEdit(t, st, run, "interrupted.txt")
	if _, err := st.FailSupervisorTurn(t.Context(), cp, "cancelled", 0); err != nil {
		t.Fatal(err)
	}
	endThreadTask(t, st, op, lease, true, 0)
	task := taskIndexForTest(t, st, run).tasks[input].task
	if task.State != "interrupted" || task.FinalMessageID != "" || task.ChangeSourceEventID == "" {
		t.Fatalf("interrupted metadata: %+v", task)
	}
	_, facts, err := st.ListThreadReplyFileEditEvents(t.Context(), domain.InitialThreadID(run.ID), task.ChangeSourceEventID, 100)
	if err != nil || len(facts) != 1 || facts[0].SubjectID != edit {
		t.Fatalf("interrupted edits: %+v %v", facts, err)
	}
	summary, err := application.NewThreadChangeService(st).Summary(t.Context(), domain.InitialThreadID(run.ID), task.ChangeSourceEventID)
	if err != nil || summary.FileCount != 1 {
		t.Fatalf("interrupted summary: %+v %v", summary, err)
	}
	journal := workspacecheckpoint.Transaction{RunID: run.ID, TriggerReceiptID: workspacecheckpoint.ThreadUndoReceiptPrefix + task.ChangeSourceEventID}
	tx, err := st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = requireThreadChangeUndoJournalTx(t.Context(), tx, journal)
	_ = tx.Rollback()
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("interrupted source must bind while prepared input still blocks undo: %v", err)
	}
	if _, err := application.NewRunService(st).Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	_, lease = prepareThreadTask(t, st, run, 1)
	cp = beginThreadTask(t, st, lease, input)
	if resumed := taskIndexForTest(t, st, run).tasks[input].task; resumed.State != "running" || resumed.FinalMessageID != "" || resumed.ChangeSourceEventID != "" || resumed.FinishedAt != nil {
		t.Fatalf("old ending froze resumed task: %+v", resumed)
	}
	if _, _, err := st.ListThreadReplyFileEditEvents(t.Context(), domain.InitialThreadID(run.ID), task.ChangeSourceEventID, 100); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old interruption still a current task ending: %v", err)
	}
	tx, err = st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = requireThreadChangeUndoJournalTx(t.Context(), tx, journal)
	_ = tx.Rollback()
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("resumed task accepted stale interruption undo: %v", err)
	}
}

func TestThreadTasksKeepCompletedInputWhenNextInputIsInterrupted(t *testing.T) {
	st, run := newThreadTaskTest(t)
	first, second := queueThreadTask(t, st, run, "batch-first"), queueThreadTask(t, st, run, "batch-second")
	op, lease := prepareThreadTask(t, st, run, 2)
	cp := beginThreadTask(t, st, lease, first)
	completeThreadTaskSegment(t, st, cp, false)
	cp = beginThreadTask(t, st, lease, second)
	applyThreadTaskEdit(t, st, run, "second-interrupted.txt")
	if _, err := st.FailSupervisorTurn(t.Context(), cp, "cancelled", 0); err != nil {
		t.Fatal(err)
	}
	endThreadTask(t, st, op, lease, true, 1)
	index := taskIndexForTest(t, st, run)
	if index.tasks[first].task.State != "completed" || index.tasks[first].task.FinalMessageID == "" || index.tasks[second].task.State != "interrupted" || index.tasks[second].task.FinalMessageID != "" {
		t.Fatalf("batch ending lost input boundaries: first=%+v second=%+v", index.tasks[first].task, index.tasks[second].task)
	}
}

func TestThreadTasksInterruptedUndoJournalRequiresExactEndingAndIdle(t *testing.T) {
	st, run := newThreadTaskTest(t)
	input := queueThreadTask(t, st, run, "closed-interruption")
	op, lease := prepareThreadTask(t, st, run, 1)
	cp := beginThreadTask(t, st, lease, input)
	applyThreadTaskEdit(t, st, run, "closed-interrupted.txt")
	if _, err := st.FailSupervisorTurn(t.Context(), cp, "cancelled", 0); err != nil {
		t.Fatal(err)
	}
	endThreadTask(t, st, op, lease, true, 0)
	if _, closed, err := st.EndFailedThreadTurn(t.Context(), domain.InitialThreadID(run.ID), run.ID, op.ID); err != nil || !closed {
		t.Fatalf("close failure: %t %v", closed, err)
	}
	task := taskIndexForTest(t, st, run).tasks[input].task
	if task.State != "interrupted" || task.FinalMessageID != "" {
		t.Fatalf("failure closure invented final: %+v", task)
	}
	journal := workspacecheckpoint.Transaction{RunID: run.ID, TriggerReceiptID: workspacecheckpoint.ThreadUndoReceiptPrefix + task.ChangeSourceEventID}
	tx, err := st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = requireThreadChangeUndoJournalTx(t.Context(), tx, journal)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("closed interrupted source rejected journal: %v", err)
	}
	journal.TriggerReceiptID = workspacecheckpoint.ThreadUndoReceiptPrefix + "not-the-ending"
	tx, err = st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = requireThreadChangeUndoJournalTx(t.Context(), tx, journal)
	_ = tx.Rollback()
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unbound undo source accepted: %v", err)
	}
}

// A model update without an execution delivery cannot invent a task.
func TestThreadTasksOmitUnknownOwnership(t *testing.T) {
	st, run := newThreadTaskTest(t)
	lease := acquireTestRunExecutionLease(t, t.Context(), st, run.ID)
	turn, err := st.BeginSupervisorTurn(t.Context(), lease, "unlinked legacy input")
	if err != nil {
		t.Fatal(err)
	}
	a := startToolRepairModel(t, st, turn.Checkpoint, 0, 0)
	if a.Number < 1 {
		t.Fatal("model did not start")
	}
	sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), domain.InitialThreadID(run.ID), 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if source.Task != nil {
			t.Fatalf("unlinked event invented task: %+v", source.Task)
		}
	}
}
