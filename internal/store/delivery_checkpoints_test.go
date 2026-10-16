package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestDeliveryCheckpointSQLiteGuardsDirectMutationAndRunCompletion(t *testing.T) {
	st, ctx, run, selected := createStoreDeliveryGateFixture(t, "direct-guards")
	defer st.Close()
	work := application.NewWorkItemService(st)
	first, err := work.Transition(ctx, selected.WorkItems[0].ID, 0,
		domain.WorkItemInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := st.db.ExecContext(ctx, `UPDATE work_items SET status = 'completed',
		version = version + 1, updated_at = ?, completed_at = ? WHERE id = ?`,
		ts(now), ts(now), first.ID); err == nil {
		t.Fatal("SQLite allowed selected WorkItem completion without a checkpoint")
	}
	checkpoint := recordStoreDeliveryCheckpoint(t, ctx, st, first.ID,
		"store-delivery-first-0001", false)
	if _, err := st.db.ExecContext(ctx, `UPDATE delivery_checkpoints
		SET requested_by = 'other' WHERE id = ?`, checkpoint.ID); err == nil {
		t.Fatal("SQLite allowed Delivery checkpoint mutation")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM delivery_checkpoints
		WHERE id = ?`, checkpoint.ID); err == nil {
		t.Fatal("SQLite allowed Delivery checkpoint deletion")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM delivery_checkpoint_operations
		WHERE checkpoint_id = ?`, checkpoint.ID); err == nil {
		t.Fatal("SQLite allowed Delivery checkpoint operation deletion")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE notes SET content = 'tampered'
		WHERE id = ?`, checkpoint.HandoffNoteID); err == nil {
		t.Fatal("SQLite allowed Delivery handoff Note mutation")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM note_tags
		WHERE note_id = ?`, checkpoint.HandoffNoteID); err == nil {
		t.Fatal("SQLite allowed Delivery handoff Note relation mutation")
	}
	other, err := application.NewNoteService(st).Create(ctx,
		application.CreateNoteRequest{RunID: run.ID, Title: "other Note",
			Content: "separate relation owner", Tags: []string{"movable-tag"},
			SourceRefs: []string{"other-source"}, EvidenceIDs: []string{"other-evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	for table, column := range map[string]string{
		"note_tags": "tag", "note_sources": "source_ref", "note_evidence": "evidence_id",
	} {
		query := `UPDATE ` + table + ` SET note_id = ? WHERE note_id = ? AND ` + column + ` != ''`
		if _, err := st.db.ExecContext(ctx, query, checkpoint.HandoffNoteID, other.ID); err == nil {
			t.Fatalf("SQLite allowed %s relation to be moved onto a Delivery handoff Note", table)
		}
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE work_items SET status = 'completed',
		version = version + 1, updated_at = ?, completed_at = ? WHERE id = ?`,
		ts(now), ts(now), first.ID); err != nil {
		t.Fatalf("SQLite rejected completion with an exact checkpoint: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runs SET status = 'completed',
		updated_at = ?, finished_at = ? WHERE id = ?`, ts(now), ts(now), run.ID); err == nil {
		t.Fatal("SQLite completed a Run with an unfinished Delivery gate")
	}
	second, err := work.Transition(ctx, selected.WorkItems[1].ID, 0,
		domain.WorkItemInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	recordStoreDeliveryCheckpoint(t, ctx, st, second.ID,
		"store-delivery-final-0001", true)
	if _, err := st.db.ExecContext(ctx, `UPDATE work_items SET status = 'completed',
		version = version + 1, updated_at = ?, completed_at = ? WHERE id = ?`,
		ts(now), ts(now), second.ID); err != nil {
		t.Fatalf("SQLite rejected final completion with a full gate: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runs SET status = 'completed',
		updated_at = ?, finished_at = ? WHERE id = ?`, ts(now), ts(now), run.ID); err != nil {
		t.Fatalf("SQLite rejected a fully checkpointed Run: %v", err)
	}
}

func TestSchemaV44LeavesPartiallyCompletedLegacySelectionExplicitlyExempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery-legacy.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	st, ctx, run, selected := populateStoreDeliveryGateFixture(t, st, "legacy-exempt")
	work := application.NewWorkItemService(st)
	first, err := work.Transition(ctx, selected.WorkItems[0].ID, 0,
		domain.WorkItemInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 43)
	now := time.Now().UTC()
	if _, err := st.db.ExecContext(ctx, `UPDATE work_items SET status = 'completed',
		version = version + 1, updated_at = ?, completed_at = ? WHERE id = ?`,
		ts(now), ts(now), first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enforced, err := st.DeliveryGateEnforced(ctx, run.ID)
	if err != nil || enforced {
		t.Fatalf("legacy partial selection was silently enrolled: enforced=%t err=%v", enforced, err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO delivery_gate_enrollments
		(run_id, selection_id, enrolled_at) VALUES (?, ?, ?)`, run.ID,
		selected.Selection.ID, ts(time.Now().UTC())); err == nil {
		t.Fatal("partially completed legacy selection could be silently enrolled")
	}
	second, err := workWithStore(st).Transition(ctx, selected.WorkItems[1].ID, 0,
		domain.WorkItemInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err = workWithStore(st).Transition(ctx, second.ID, second.Version,
		domain.WorkItemCompleted, "")
	if err != nil || second.Status != domain.WorkItemCompleted {
		t.Fatalf("legacy-exempt selection lost compatibility: %#v err=%v", second, err)
	}
}

func createStoreDeliveryGateFixture(t *testing.T, suffix string, manual ...domain.PlanDeliveryManualAcceptance) (*SQLiteStore,
	context.Context, domain.Run, application.SelectPlanDeliveryDirectionResult,
) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "delivery-"+suffix+".db"))
	if err != nil {
		t.Fatal(err)
	}
	return populateStoreDeliveryGateFixture(t, st, suffix, manual...)
}

func populateStoreDeliveryGateFixture(t *testing.T, st *SQLiteStore, suffix string,
	manual ...domain.PlanDeliveryManualAcceptance,
) (*SQLiteStore, context.Context, domain.Run, application.SelectPlanDeliveryDirectionResult) {
	return populateStoreDeliveryGateFixtureInWorkspace(t, st, suffix, "", manual...)
}

func populateStoreDeliveryGateFixtureInWorkspace(t *testing.T, st *SQLiteStore, suffix, workspaceID string,
	manual ...domain.PlanDeliveryManualAcceptance,
) (*SQLiteStore, context.Context, domain.Run, application.SelectPlanDeliveryDirectionResult) {
	t.Helper()
	acceptance := domain.PlanDeliveryManualAcceptanceRequired
	if len(manual) != 0 {
		acceptance = manual[0]
	}
	ctx := context.Background()
	runService := newMigrationFixtureRunService(t, st)
	_, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "exercise Delivery gates " + suffix, Profile: "review", Phase: "plan",
		WorkspaceID: workspaceID,
		ModelRoute:  "store-plan/model",
		Budget:      domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &storePlanProvider{responses: []*llm.ChatResponse{
		{Provider: "store-plan", Model: "model",
			Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			ToolCalls: []llm.ToolCall{{ID: "delivery-plan-call-" + suffix,
				Name: "plan_delivery_propose", Arguments: json.RawMessage(storePlanDeliveryPayload)}}},
		{Text: storeRootWaitResponse(t), Provider: "store-plan", Model: "model",
			Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}},
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	if _, err := application.NewAgentRunner(st, router,
		policy.NewDefaultChecker()).Step(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	proposals, err := st.ListPlanDeliveryProposals(ctx, run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("Delivery proposal fixture failed: %#v err=%v", proposals, err)
	}
	selected, err := application.NewPlanDeliveryService(st).Select(ctx,
		application.SelectPlanDeliveryDirectionRequest{
			ProposalID: proposals[0].ID, Direction: 2, ManualAcceptance: acceptance,
			OperationKey: "store-delivery-choice-" + suffix,
			RequestedBy:  "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.ChangePhase(ctx, application.ChangeRunPhaseRequest{
		RunID: run.ID, Phase: "deliver",
		OperationKey: "store-delivery-mode-" + suffix,
		RequestedBy:  "operator", Reason: "accepted direction",
	}); err != nil {
		t.Fatal(err)
	}
	return st, ctx, run, selected
}

func recordStoreDeliveryCheckpoint(t *testing.T, ctx context.Context,
	st *SQLiteStore, workItemID, operationKey string, full bool,
) domain.DeliveryCheckpoint {
	t.Helper()
	request := application.RecordDeliveryCheckpointRequest{
		WorkItemID: workItemID, OperationKey: operationKey, RequestedBy: "operator",
		FocusedVerification: "focused tests passed", DiffAudit: "diff audit passed",
		SecurityAudit: "security audit passed", HandoffSummary: "bounded handoff",
	}
	if full {
		request.FunctionalVerification = "full suite passed"
		request.RobustnessAudit = "robustness review passed"
	}
	result, err := application.NewDeliveryCheckpointService(st).Record(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return result.Checkpoint
}

func workWithStore(st *SQLiteStore) *application.WorkItemService {
	return application.NewWorkItemService(st)
}
