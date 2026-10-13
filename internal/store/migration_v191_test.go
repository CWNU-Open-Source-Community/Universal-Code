package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
)

func TestV191PreservesSchedulesAndSealsContinuationReplay(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "retained schedule", 2, 64)
	usage, err := st.GetRunAgentUsage(ctx, fixture.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := domain.SpecialistScheduleStart{ID: "schedule-before-v191", RunID: fixture.Run.ID,
		AgentIDs: []string{fixture.Child.ID}, MaxRounds: 2, Lease: fixture.Lease, UsageBefore: usage, StartedAt: time.Now().UTC()}
	if _, err := st.StartSpecialistSchedule(ctx, start); err != nil {
		t.Fatal(err)
	}
	before, err := st.FinishSpecialistSchedule(ctx, domain.SpecialistScheduleFinish{ID: start.ID, Lease: fixture.Lease,
		Status: domain.SpecialistScheduleCompleted, StopReason: "round_limit", UsageAfter: usage, FinishedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	historical := historicalTestDatabaseFromSeed(t, st, filepath.Join(t.TempDir(), "v190.db"), 190)
	if err := historical.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := historical.GetSpecialistSchedule(ctx, before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("retained schedule changed: before=%+v after=%+v err=%v", before, after, err)
	}

	start.ID = "schedule-continuation-v191"
	start.ContinuationDigest = strings.Repeat("a", 64)
	if _, err := historical.StartSpecialistSchedule(ctx, start); err != nil {
		t.Fatal(err)
	}
	completed, err := historical.FinishSpecialistSchedule(ctx, domain.SpecialistScheduleFinish{ID: start.ID, Lease: fixture.Lease,
		Status: domain.SpecialistScheduleCompleted, StopReason: "all_terminal", UsageAfter: usage, FinishedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	start.ID = "schedule-duplicate-must-not-start"
	replay, err := historical.StartSpecialistSchedule(ctx, start)
	if err != nil || !reflect.DeepEqual(replay.Schedule, completed) {
		t.Fatalf("continuation dispatched twice: %+v err=%v", replay, err)
	}
	if _, err := historical.db.ExecContext(ctx, `UPDATE specialist_schedules SET continuation_digest = ? WHERE id = ?`, strings.Repeat("b", 64), completed.ID); err == nil {
		t.Fatal("continuation identity was mutable")
	}
	assertNoForeignKeyViolations(t, historical.db)
}
