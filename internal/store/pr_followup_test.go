package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func newPRFollowupFixture(t *testing.T) (*SQLiteStore, string, domain.PRFollowupState) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "followup.db")
	st := openCurrentTestDatabase(t, path)
	workspace := WorkspaceRecord{ID: "pr-followup-workspace", Name: "pr-followup", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	created, err := application.NewControlledRunCreationService(st).Create(t.Context(), application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "Follow this pull request", WorkspaceID: workspace.ID,
		OperationKey: "pr-followup-test-thread", RequestedBy: "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state, inserted, err := st.CreatePRFollowup(t.Context(), domain.PRFollowupState{Record: domain.PRFollowup{
		ID: "pr-followup-test", ThreadID: domain.InitialThreadID(created.Run.ID), ConnectionID: "github-connection",
		Repository: "owner/repo", WorkspaceID: workspace.ID, RootPathSHA256: strings.Repeat("a", 64), Branch: "feature",
		PullRequest: 42, IntervalSeconds: 60, DeadlineAt: now.Add(time.Hour), MaxTurns: 3,
		CreatedAt: now, NextCheckAt: now,
	}}, "request-one")
	if err != nil || !inserted {
		t.Fatalf("create: inserted=%t err=%v", inserted, err)
	}
	return st, path, state
}

func startPRFollowupTestTurn(t *testing.T, st *SQLiteStore, state domain.PRFollowupState) domain.PRFollowupState {
	t.Helper()
	state.Observed = map[string]string{"review:7": "digest-one"}
	state.Initialized = true
	now := time.Now().UTC()
	state.Pending = &domain.PRFollowupTurn{ID: "turn-one", OperationKey: "followup-submit-one", Content: "Address the exact review body",
		State: domain.PRFollowupTurnQueued, CreatedAt: now, UpdatedAt: now}
	state.Record.TurnsStarted++
	saved, err := st.SavePRFollowup(t.Context(), state, state.Record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestPRFollowupPersistsReplayAndPublicProjectionAcrossReopen(t *testing.T) {
	st, path, initial := newPRFollowupFixture(t)
	claimed, ok, err := st.ClaimDuePRFollowup(t.Context(), "worker-one", time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	state := startPRFollowupTestTurn(t, st, claimed)
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.GetPRFollowup(t.Context(), initial.Record.ID)
	if err != nil || state.Pending == nil || state.Pending.OperationKey != "followup-submit-one" || state.Pending.Content != "Address the exact review body" ||
		state.Record.TurnsStarted != 1 || !state.Initialized || state.Observed["review:7"] != "digest-one" || state.LeaseToken != claimed.LeaseToken {
		t.Fatalf("restart lost replay state: %#v %v", state, err)
	}
	replayed, inserted, err := reopened.CreatePRFollowup(t.Context(), initial, "request-one")
	if err != nil || inserted || replayed.Record.Revision != state.Record.Revision || replayed.Record.TurnsStarted != 1 {
		t.Fatalf("idempotent create reset state: %#v %t %v", replayed, inserted, err)
	}
	if _, _, err = reopened.CreatePRFollowup(t.Context(), initial, "different-request"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("operation key reuse accepted: %v", err)
	}
	public, err := reopened.ListPRFollowups(t.Context(), initial.Record.ThreadID, 10)
	if err != nil || len(public) != 1 {
		t.Fatalf("list: %#v %v", public, err)
	}
	for _, value := range []any{state, public} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{claimed.LeaseToken, "followup-submit-one", "Address the exact review body", "digest-one"} {
			if strings.Contains(string(payload), secret) {
				t.Fatalf("public JSON contains internal value %q", secret)
			}
		}
	}
}

func TestPRFollowupConcurrentClaimRenewAndTakeover(t *testing.T) {
	st, path, initial := newPRFollowupFixture(t)
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Now().UTC()
	type claimResult struct {
		state domain.PRFollowupState
		ok    bool
		err   error
	}
	results := make(chan claimResult, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			store := st
			if i%2 == 1 {
				store = second
			}
			state, ok, err := store.ClaimDuePRFollowup(t.Context(), fmt.Sprintf("worker-%d", i), now)
			results <- claimResult{state, ok, err}
		})
	}
	wg.Wait()
	close(results)
	var winner domain.PRFollowupState
	claims := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claims++
			winner = result.state
		}
	}
	if claims != 1 || winner.Record.Revision != initial.Record.Revision {
		t.Fatalf("concurrent claims=%d winner=%#v", claims, winner)
	}
	if ok, err := second.RenewPRFollowupLease(t.Context(), winner.Record.ID, winner.LeaseOwner, winner.LeaseToken, now.Add(10*time.Second)); err != nil || !ok {
		t.Fatalf("renew: %t %v", ok, err)
	}
	winner.Record.LastResult = "checked"
	saved, err := st.SavePRFollowup(t.Context(), winner, winner.Record.Revision)
	if err != nil || !saved.LeaseUntil.Equal(now.Add(40*time.Second)) {
		t.Fatalf("save overwrote renewal: %#v %v", saved, err)
	}
	if ok, err := second.RenewPRFollowupLease(t.Context(), winner.Record.ID, winner.LeaseOwner, winner.LeaseToken, now.Add(41*time.Second)); err != nil || ok {
		t.Fatalf("expired lease renewed: %t %v", ok, err)
	}
	takeover, ok, err := second.ClaimDuePRFollowup(t.Context(), "replacement", now.Add(41*time.Second))
	if err != nil || !ok || takeover.LeaseToken == winner.LeaseToken {
		t.Fatalf("takeover: %#v %t %v", takeover, ok, err)
	}
	if err = st.ReleasePRFollowupLease(t.Context(), winner.Record.ID, winner.LeaseOwner, winner.LeaseToken); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetPRFollowup(t.Context(), winner.Record.ID)
	if err != nil || current.LeaseToken != takeover.LeaseToken {
		t.Fatalf("old release cleared replacement: %#v %v", current, err)
	}
	winner.Record.Revision = current.Record.Revision
	if _, err = st.SavePRFollowup(t.Context(), winner, current.Record.Revision); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("old worker saved after takeover: %v", err)
	}
}

func TestPRFollowupPauseRevokesWorkerAndPreservesUnfinishedThreadUniqueness(t *testing.T) {
	st, _, initial := newPRFollowupFixture(t)
	worker, ok, err := st.ClaimDuePRFollowup(t.Context(), "worker", time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	worker = startPRFollowupTestTurn(t, st, worker)
	control, err := st.GetPRFollowup(t.Context(), worker.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	control.LeaseOwner, control.LeaseToken = "", ""
	for _, status := range []string{domain.PRFollowupPaused, domain.PRFollowupBlocked, domain.PRFollowupReady} {
		control.Record.Status = status
		control, err = st.SavePRFollowup(t.Context(), control, control.Record.Revision)
		if err != nil || control.LeaseToken != "" || control.Pending == nil || len(control.Observed) != 1 {
			t.Fatalf("control lost state: %#v %v", control, err)
		}
		duplicate := initial
		duplicate.Record.ID = "another-followup"
		if _, _, err = st.CreatePRFollowup(t.Context(), duplicate, "second-request"); apperror.CodeOf(err) != apperror.CodeConflict {
			t.Fatalf("unfinished %s allowed second subscription: %v", status, err)
		}
	}
	if _, err = st.SavePRFollowup(t.Context(), worker, worker.Record.Revision); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("old revision saved after pause: %v", err)
	}
	if _, err = st.SavePRFollowup(t.Context(), worker, control.Record.Revision); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("revoked worker saved with newer revision: %v", err)
	}
	if ok, err = st.RenewPRFollowupLease(t.Context(), worker.Record.ID, worker.LeaseOwner, worker.LeaseToken, time.Now().UTC()); err != nil || ok {
		t.Fatalf("revoked lease renewed: %t %v", ok, err)
	}
	control.Record.Status = domain.PRFollowupCancelled
	if _, err = st.SavePRFollowup(t.Context(), control, control.Record.Revision); err != nil {
		t.Fatal(err)
	}
	initial.Record.ID = "replacement-followup"
	if _, created, err := st.CreatePRFollowup(t.Context(), initial, "replacement-request"); err != nil || !created {
		t.Fatalf("terminal record blocked new subscription: %t %v", created, err)
	}
}

func TestPRFollowupSaveProtectsIdentityObservationsAndPendingReplay(t *testing.T) {
	st, _, state := newPRFollowupFixture(t)
	state = startPRFollowupTestTurn(t, st, state)
	mutations := map[string]func(*domain.PRFollowupState){
		"identity":        func(s *domain.PRFollowupState) { s.Record.Branch = "other" },
		"observations":    func(s *domain.PRFollowupState) { s.Observed = nil },
		"initialization":  func(s *domain.PRFollowupState) { s.Initialized = false },
		"drop pending":    func(s *domain.PRFollowupState) { s.Pending = nil },
		"content":         func(s *domain.PRFollowupState) { s.Pending.Content = "different" },
		"operation key":   func(s *domain.PRFollowupState) { s.Pending.OperationKey = "different" },
		"counter replay":  func(s *domain.PRFollowupState) { s.Record.TurnsStarted++ },
		"replace running": func(s *domain.PRFollowupState) { s.Pending.ID = "another"; s.Record.TurnsStarted++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			current, err := st.GetPRFollowup(t.Context(), state.Record.ID)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&current)
			if _, err = st.SavePRFollowup(t.Context(), current, current.Record.Revision); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
	state.Pending.State = domain.PRFollowupTurnCompleted
	state, err := st.SavePRFollowup(t.Context(), state, state.Record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	state.Pending.ID, state.Pending.OperationKey = "turn-two", "followup-submit-two"
	state.Pending.State = domain.PRFollowupTurnQueued
	state.Record.TurnsStarted++
	state, err = st.SavePRFollowup(t.Context(), state, state.Record.Revision)
	if err != nil || state.Record.TurnsStarted != 2 {
		t.Fatalf("new turn was not counted once: %#v %v", state, err)
	}
}

func TestPRFollowupMigrationFromV201(t *testing.T) {
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "upgrade.db"), 201)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := st.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != LatestSchemaVersion {
		t.Fatalf("migration version=%d want=%d err=%v", version, LatestSchemaVersion, err)
	}
	if _, err := st.ListPRFollowups(t.Context(), "", 10); err != nil {
		t.Fatal(err)
	}
	assertNoForeignKeyViolations(t, st.db)
}

func TestPRFollowupDeadlineStopsRenewalAndCanBeRecordedAsExhausted(t *testing.T) {
	st, _, initial := newPRFollowupFixture(t)
	worker, ok, err := st.ClaimDuePRFollowup(t.Context(), "worker", initial.Record.DeadlineAt.Add(-5*time.Second))
	if err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if ok, err := st.RenewPRFollowupLease(t.Context(), worker.Record.ID, worker.LeaseOwner, worker.LeaseToken, initial.Record.DeadlineAt); err != nil || ok {
		t.Fatalf("deadline renewed an otherwise valid lease: %t %v", ok, err)
	}
	if err := st.ReleasePRFollowupLease(t.Context(), worker.Record.ID, worker.LeaseOwner, worker.LeaseToken); err != nil {
		t.Fatal(err)
	}
	worker, ok, err = st.ClaimDuePRFollowup(t.Context(), "expiry-worker", initial.Record.DeadlineAt)
	if err != nil || !ok {
		t.Fatalf("expired subscription could not be finalized: %t %v", ok, err)
	}
	worker.Record.Status = domain.PRFollowupExhausted
	if _, err := st.SavePRFollowup(t.Context(), worker, worker.Record.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestPRFollowupCreatePersistsInitialCommentBaseline(t *testing.T) {
	st, path, original := newPRFollowupFixture(t)
	control := original
	for _, includeExisting := range []bool{false, true} {
		control.Record.Status = domain.PRFollowupCancelled
		if _, err := st.SavePRFollowup(t.Context(), control, control.Record.Revision); err != nil {
			t.Fatal(err)
		}
		baseline := original
		baseline.Record.ID = fmt.Sprintf("baseline-%t", includeExisting)
		baseline.Record.IncludeExistingComments = includeExisting
		baseline.Initialized = true
		baseline.Observed = map[string]string{}
		if !includeExisting {
			baseline.Observed["review:already-present"] = "baseline-digest"
		}
		created, inserted, err := st.CreatePRFollowup(t.Context(), baseline, baseline.Record.ID)
		if err != nil || !inserted || !created.Initialized {
			t.Fatalf("create initialized baseline: %#v %t %v", created, inserted, err)
		}
		control = created
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, includeExisting := range []bool{false, true} {
		saved, err := reopened.GetPRFollowup(t.Context(), fmt.Sprintf("baseline-%t", includeExisting))
		if err != nil || !saved.Initialized || saved.Record.IncludeExistingComments != includeExisting {
			t.Fatalf("reload initialized baseline: %#v %v", saved, err)
		}
		if (!includeExisting && saved.Observed["review:already-present"] != "baseline-digest") || (includeExisting && len(saved.Observed) != 0) {
			t.Fatalf("initial observations changed: %#v", saved.Observed)
		}
	}
}
