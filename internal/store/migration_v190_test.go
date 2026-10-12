package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestV190PreservesV189SpecialistRepairAndModelHistory(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "historical repaired Specialist", 2, 64)
	attempt, _, err := st.BeginSpecialistAttempt(ctx, newAttemptStart(fixture, idgen.New("attempt")), "v190-specialist-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	ref := attemptRef(attempt)
	primary := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 2, Provider: "mock", Model: "model"}
	if _, err = st.RecordSpecialistModelStarted(ctx, ref, primary); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RecordSpecialistProtocolFailure(ctx, ref, primary, llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}, "invalid lifecycle JSON", true); err != nil {
		t.Fatal(err)
	}
	repair := llm.ModelAttempt{Number: 2, TransportAttempt: 1, MaxAttempts: 2, ProtocolRepair: 1, Provider: "mock", Model: "model"}
	if _, err = st.RecordSpecialistModelStarted(ctx, ref, repair); err != nil {
		t.Fatal(err)
	}
	action := domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "repair completed"}
	response := llm.ChatResponse{Text: specialistActionResponse(t, action), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
	if _, err = st.RecordSpecialistModelCompleted(ctx, ref, repair, response, "assigned review", action, policy.Decision{Allowed: true, Risk: "low", Reason: "allowed repaired result"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ContinueSpecialistAttempt(ctx, ref, "v190-specialist-continue-0001"); err != nil {
		t.Fatal(err)
	}
	beforeAttempt, _, _ := st.GetAgentAttempt(ctx, attempt.ID)
	beforeRepair, _, _ := st.GetSpecialistProtocolRepair(ctx, attempt.ID)
	beforeMessages, err := st.ListSessionMessages(ctx, fixture.Child.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	historical := historicalTestDatabaseFromSeed(t, st, filepath.Join(t.TempDir(), "v189.db"), 189)
	beforeModels := legacyFixtureRows(t, historical, "specialist_model_calls")
	if err = historical.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	afterAttempt, found, err := historical.GetAgentAttempt(ctx, attempt.ID)
	if err != nil || !found || !reflect.DeepEqual(beforeAttempt, afterAttempt) {
		t.Fatalf("attempt changed: %+v %v", afterAttempt, err)
	}
	afterRepair, found, err := historical.GetSpecialistProtocolRepair(ctx, attempt.ID)
	if err != nil || !found || !reflect.DeepEqual(beforeRepair, afterRepair) {
		t.Fatalf("repair changed: %+v %v", afterRepair, err)
	}
	afterMessages, err := historical.ListSessionMessages(ctx, fixture.Child.SessionID, true)
	if err != nil || !reflect.DeepEqual(beforeMessages, afterMessages) {
		t.Fatal("Session messages changed across migration")
	}
	afterModels := legacyFixtureRows(t, historical, "specialist_model_calls")
	// v190 inserts tool_round after protocol_repair; retain all old values and rowids.
	for i, row := range afterModels {
		afterModels[i] = append(append([]any{}, row[:8]...), row[9:]...)
	}
	if !reflect.DeepEqual(beforeModels, afterModels) {
		t.Fatalf("model ledger changed across migration: before=%+v after=%+v", beforeModels, afterModels)
	}
	assertNoForeignKeyViolations(t, historical.db)
	for pragma, want := range map[string]int{"foreign_keys": 1, "legacy_alter_table": 0} {
		var got int
		if err = historical.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d err=%v", pragma, got, err)
		}
	}
}
