package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

type threadSearchCreationStore struct {
	*SQLiteStore
	createdAt time.Time
}

func (s *threadSearchCreationStore) CreateMissionRun(ctx context.Context,
	mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot,
	linkedSession session.Session, createSession bool, initialEvents []events.Event,
) error {
	run.CreatedAt, run.UpdatedAt = s.createdAt, s.createdAt
	return s.SQLiteStore.CreateMissionRun(ctx, mission, run, mode, linkedSession, createSession, initialEvents)
}

func TestSQLiteThreadSearchFiltersBeforeLimitAndPreservesCreationKeyset(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour)
	seed := &threadSearchCreationStore{SQLiteStore: st, createdAt: base}
	runs := application.NewRunService(seed)
	var matching []string
	for index := 0; index < 106; index++ {
		title := fmt.Sprintf("unrelated newer task %03d", index)
		if index < 5 {
			title = fmt.Sprintf("Literal FIND 中文_%% Ä %03d", index)
		}
		if index == 105 {
			title = "Literal FIND 中文_%% ä spelling differs"
		}
		seed.createdAt = base.Add(time.Duration(index) * time.Second)
		if index < 5 {
			// Equal timestamps exercise the identity tiebreaker independently
			// of mutable updated_at and lifecycle status.
			seed.createdAt = base
		}
		_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: title, Profile: "review", ModelRoute: "review"})
		if err != nil {
			t.Fatal(err)
		}
		if index < 5 {
			matching = append(matching, domain.InitialThreadID(run.ID))
		}
	}
	slices.Sort(matching)
	slices.Reverse(matching)
	filter := domain.ThreadFilter{TitleQuery: " FIND 中文_% Ä ", Limit: 2}
	first, err := st.ListThreadsByCreationPage(ctx, filter, time.Time{}, "")
	if err != nil || len(first) != 2 || first[0].ID != matching[0] || first[1].ID != matching[1] {
		t.Fatalf("search excluded older tasks or lost literal/case matching: first=%+v err=%v", first, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE threads SET updated_at=? WHERE id=?`, ts(time.Now().UTC()), matching[2]); err != nil {
		t.Fatal(err)
	}
	seed.createdAt = time.Now().UTC()
	if _, _, err := runs.Create(ctx, application.CreateRunRequest{Goal: "new FIND 中文_% Ä", Profile: "review", ModelRoute: "review"}); err != nil {
		t.Fatal(err)
	}
	second, err := st.ListThreadsByCreationPage(ctx, filter, first[1].CreatedAt, first[1].ID)
	if err != nil || len(second) != 2 || second[0].ID != matching[2] || second[1].ID != matching[3] {
		t.Fatalf("search continuation shifted after update/insert: second=%+v err=%v", second, err)
	}
	third, err := st.ListThreadsByCreationPage(ctx, filter, second[1].CreatedAt, second[1].ID)
	if err != nil || len(third) != 1 || third[0].ID != matching[4] {
		t.Fatalf("search continuation omitted final match: third=%+v err=%v", third, err)
	}
	if empty, err := st.ListThreadsByCreationPage(ctx, domain.ThreadFilter{TitleQuery: "absent", Limit: 2}, time.Time{}, ""); err != nil || len(empty) != 0 {
		t.Fatalf("absent search=%+v err=%v", empty, err)
	}
}

func TestSQLiteThreadListOrdersExactSecondTimestampsChronologically(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	seed := &threadSearchCreationStore{SQLiteStore: st, createdAt: base}
	runs := application.NewRunService(seed)
	threadIDs := make([]string, 0, 3)
	for index, offset := range []time.Duration{0, 100 * time.Millisecond, 110 * time.Millisecond} {
		seed.createdAt = base.Add(offset)
		_, run, err := runs.Create(ctx, application.CreateRunRequest{
			Goal: fmt.Sprintf("creation order %d", index), Profile: "review", ModelRoute: "review"})
		if err != nil {
			t.Fatal(err)
		}
		threadIDs = append(threadIDs, domain.InitialThreadID(run.ID))
	}
	var before time.Time
	beforeID := ""
	for _, expected := range []string{threadIDs[2], threadIDs[1], threadIDs[0]} {
		items, err := st.ListThreadsByCreationPage(ctx, domain.ThreadFilter{Limit: 1}, before, beforeID)
		if err != nil || len(items) != 1 || items[0].ID != expected {
			t.Fatalf("creation order page: %#v %v; want %s", items, err, expected)
		}
		before, beforeID = items[0].CreatedAt, items[0].ID
	}
	items, err := st.ListThreadsByCreationPage(ctx, domain.ThreadFilter{Limit: 1}, before, beforeID)
	if err != nil || len(items) != 0 {
		t.Fatalf("unexpected extra threads: %#v %v", items, err)
	}
}

func TestSQLiteThreadExecutionFactsKeepExpiredLeaseAndUnfinishedRequestsUncertain(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	_, run := createWorkItemTestRun(t, ctx, st, "execution facts")
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	read := func(wantUnsettled bool) {
		t.Helper()
		facts, err := st.GetThreadExecutionFacts(ctx, []string{threadID})
		if err != nil || len(facts) != 1 || facts[threadID].RunID != run.ID ||
			facts[threadID].RunStatus != domain.RunRunning || facts[threadID].Unsettled != wantUnsettled {
			t.Fatalf("facts=%+v err=%v wantUnsettled=%t", facts, err, wantUnsettled)
		}
	}
	read(false)
	lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "external-cli-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	read(true)
	expireTestRunExecutionLease(t, ctx, st, lease.Lease)
	read(true)
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	read(false)
	if _, err := st.ReserveThreadMessageIntent(ctx, domain.ThreadMessageIntentRequest{
		ThreadID: threadID, Content: "unfinished accepted request", RequestedBy: "operator",
		OperationKey: "thread-facts-intent-0001",
	}); err != nil {
		t.Fatal(err)
	}
	read(true)
	if _, err := st.GetThreadExecutionFacts(ctx, []string{""}); err == nil {
		t.Fatal("invalid batch identity accepted")
	}
}

func TestSQLiteThreadExecutionFactsTerminalRecoveryAndCancellationRetainClosedOutcome(t *testing.T) {
	for _, terminal := range []domain.RunStatus{domain.RunFailed, domain.RunCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			st := openWorkItemTestStore(t)
			ctx := t.Context()
			_, run := createWorkItemTestRun(t, ctx, st, "close failed checkpoint")
			if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			threadID := domain.InitialThreadID(run.ID)
			if _, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
				RunID: run.ID, SessionID: run.SessionID, Content: "input with recorded failure",
				OperationKey: "thread-facts-failed-input-0001", RequestedBy: "operator",
			}); err != nil {
				t.Fatal(err)
			}
			operation := domain.RunExecutionHandoffOperation{
				ID: idgen.New("run-handoff"), ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
				KeyDigest:          runmutation.RunExecutionHandoffOperationDigest(run.ID, "thread-facts-handoff-0001"),
				RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(run.ID, "operator", 1),
				RunID:              run.ID, SessionID: run.SessionID, RequestedBy: "operator", MaxSteps: 1, CreatedAt: time.Now().UTC(),
			}
			if _, _, err := st.PrepareRunExecutionHandoff(ctx, operation); err != nil {
				t.Fatal(err)
			}
			lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "worker", TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			turn, err := st.BeginSupervisorTurn(ctx, lease.Lease, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.FailSupervisorTurn(ctx, turn.Checkpoint, "recorded model failure", 0); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.CompleteRunExecutionHandoff(ctx, operation.ID, lease.Lease,
				domain.RunExecutionHandoffFailed, "failed_precondition", "failed_precondition", 0, false, false); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetThreadExecutionFacts(ctx, []string{threadID})
			if err != nil || !before[threadID].Unsettled {
				t.Fatalf("open failure not uncertain: %+v err=%v", before, err)
			}
			if terminal == domain.RunFailed {
				if _, err := application.NewThreadRunRecoveryService(st).Recover(ctx, application.RecoverThreadRunRequest{
					Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadID, RunID: run.ID,
					HandoffOperationID: operation.ID, OperationKey: "thread-facts-recover-0001", RequestedBy: "operator",
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			checkpoint, found, err := st.GetSupervisorCheckpoint(ctx, run.ID)
			if err != nil || !found || checkpoint.Phase != domain.SupervisorTurnFailed {
				t.Fatalf("fixture lost retained checkpoint: %+v err=%v", checkpoint, err)
			}
			after, err := st.GetThreadExecutionFacts(ctx, []string{threadID})
			if err != nil || after[threadID].RunStatus != terminal || after[threadID].Unsettled {
				t.Fatalf("explicit terminal outcome remained uncertain: %+v err=%v", after, err)
			}
		})
	}
}
