package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

const storePlanDeliveryPayload = `{"version":"plan_delivery.v1","directions":[` +
	`{"title":"Conservative","summary":"Keep changes narrow.","tradeoffs":["More sequential work"],"modules":[{"title":"Inspect","objective":"Inspect current boundaries.","acceptance_criteria":["Boundaries recorded"],"dependencies":[]}]},` +
	`{"title":"Balanced","summary":"Deliver a vertical slice.","tradeoffs":["Moderate breadth"],"modules":[{"title":"Implement","objective":"Implement the core path.","acceptance_criteria":["Focused tests pass"],"dependencies":[]},{"title":"Audit","objective":"Audit the completed slice.","acceptance_criteria":["Audit recorded"],"dependencies":[1]}]},` +
	`{"title":"Accelerated","summary":"Prepare independent slices.","tradeoffs":["Higher review load"],"modules":[{"title":"Prepare","objective":"Prepare independent work.","acceptance_criteria":["Work stays bounded"],"dependencies":[]}]}]}`

func TestSchemaV41UpgradeSupportsImmutablePlanDeliveryLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v41-plan-delivery.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	ctx := context.Background()
	runs := newMigrationFixtureRunService(t, st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "upgrade and preserve Plan/Delivery", Profile: "review", Phase: "plan",
		ModelRoute: "store-plan/model",
		Budget:     domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 41)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v41 did not upgrade: version=%d err=%v", version, err)
	}
	mode, err := st.GetRunMode(ctx, run.ID)
	if err != nil || mode.Phase != domain.ExecutionPhasePlan || mode.Revision != 1 {
		t.Fatalf("schema v41 upgrade changed Run mode: %#v err=%v", mode, err)
	}
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &storePlanProvider{responses: []*llm.ChatResponse{
		{Provider: "store-plan", Model: "model", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			ToolCalls: []llm.ToolCall{{ID: "store-plan-call", Name: "plan_delivery_propose",
				Arguments: json.RawMessage(storePlanDeliveryPayload)}}},
		{Text: storeRootWaitResponse(t), Provider: "store-plan", Model: "model",
			Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}},
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	if _, err := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).Step(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	proposals, err := st.ListPlanDeliveryProposals(ctx, run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("upgraded schema rejected Plan proposal: %#v err=%v", proposals, err)
	}
	selected, err := application.NewPlanDeliveryService(st).Select(ctx,
		application.SelectPlanDeliveryDirectionRequest{
			ProposalID: proposals[0].ID, Direction: 2,
			OperationKey: "store-plan-choice-0001", RequestedBy: "operator",
		})
	if err != nil || len(selected.WorkItems) != 2 {
		t.Fatalf("upgraded schema rejected Plan choice: %#v err=%v", selected, err)
	}

	mutations := []struct {
		statement string
		argument  any
	}{
		{`UPDATE plan_delivery_proposals SET requested_by = 'other' WHERE id = ?`, proposals[0].ID},
		{`DELETE FROM plan_delivery_proposals WHERE id = ?`, proposals[0].ID},
		{`UPDATE plan_delivery_directions SET title = 'other' WHERE proposal_id = ?`, proposals[0].ID},
		{`DELETE FROM plan_delivery_directions WHERE proposal_id = ?`, proposals[0].ID},
		{`UPDATE plan_delivery_modules SET objective = 'other' WHERE proposal_id = ?`, proposals[0].ID},
		{`DELETE FROM plan_delivery_modules WHERE proposal_id = ?`, proposals[0].ID},
		{`UPDATE plan_delivery_proposal_operations SET requested_by = 'other' WHERE proposal_id = ?`, proposals[0].ID},
		{`DELETE FROM plan_delivery_proposal_operations WHERE proposal_id = ?`, proposals[0].ID},
		{`UPDATE plan_delivery_selections SET direction_ordinal = 1 WHERE id = ?`, selected.Selection.ID},
		{`DELETE FROM plan_delivery_selections WHERE id = ?`, selected.Selection.ID},
		{`UPDATE plan_delivery_selection_items SET module_ordinal = 1 WHERE selection_id = ?`, selected.Selection.ID},
		{`DELETE FROM plan_delivery_selection_items WHERE selection_id = ?`, selected.Selection.ID},
		{`UPDATE plan_delivery_selection_operations SET requested_by = 'other' WHERE selection_id = ?`, selected.Selection.ID},
		{`DELETE FROM plan_delivery_selection_operations WHERE selection_id = ?`, selected.Selection.ID},
	}
	for _, mutation := range mutations {
		if _, err := st.db.ExecContext(ctx, mutation.statement, mutation.argument); err == nil {
			t.Fatalf("immutable Plan/Delivery mutation succeeded: %s", mutation.statement)
		}
	}
	loaded, found, err := st.GetPlanDeliverySelectionByRun(ctx, run.ID)
	if err != nil || !found || loaded.ID != selected.Selection.ID ||
		loaded.DirectionOrdinal != 2 || len(loaded.Items) != 2 {
		t.Fatalf("Plan/Delivery ledger changed after mutation attempts: %#v found=%t err=%v",
			loaded, found, err)
	}
}

func TestPlanDeliverySQLiteRejectsDuplicateTitlesBeforeOperationCommit(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "plan-delivery-title-guards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx,
		application.CreateRunRequest{
			Goal: "exercise SQLite title guards", Profile: "review", Phase: "plan",
			Budget: domain.Budget{MaxTurns: 2, MaxToolCalls: 2},
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	defer func() { _, _, _ = st.ReleaseRunExecutionLease(ctx, lease) }()
	turn, err := st.BeginSupervisorTurn(ctx, lease, "test title constraints")
	if err != nil {
		t.Fatal(err)
	}
	mode, err := st.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	proposalID := "plan-proposal-sql-title-guard"
	if _, err := st.db.ExecContext(ctx, `INSERT INTO plan_delivery_proposals
		(id, run_id, root_agent_id, session_id, workspace_id, mode_revision,
		protocol_version, status, direction_count, proposal_fingerprint,
		requested_by, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'plan_delivery.v1', 'proposed', 3, ?,
		'run_supervisor', 1, ?)`, proposalID, run.ID, turn.Agent.ID,
		run.SessionID, turn.Mission.WorkspaceID, mode.Revision,
		strings.Repeat("a", 64), ts(now)); err != nil {
		t.Fatal(err)
	}
	insertDirection := func(ordinal int, title string, modules int) error {
		_, err := st.db.ExecContext(ctx, `INSERT INTO plan_delivery_directions
			(proposal_id, ordinal, title, summary, tradeoffs_json, module_count)
			VALUES (?, ?, ?, 'bounded summary', '["bounded tradeoff"]', ?)`,
			proposalID, ordinal, title, modules)
		return err
	}
	if err := insertDirection(1, "Same direction", 2); err != nil {
		t.Fatal(err)
	}
	if err := insertDirection(2, "same direction", 1); err == nil {
		t.Fatal("SQLite accepted a duplicate direction title")
	}
	if err := insertDirection(2, "Second direction", 1); err != nil {
		t.Fatal(err)
	}
	if err := insertDirection(3, "Third direction", 1); err != nil {
		t.Fatal(err)
	}
	insertModule := func(ordinal int, title string, dependencies string) error {
		_, err := st.db.ExecContext(ctx, `INSERT INTO plan_delivery_modules
			(proposal_id, direction_ordinal, ordinal, title, objective,
			acceptance_json, dependencies_json)
			VALUES (?, 1, ?, ?, 'bounded objective', '["criterion"]', ?)`,
			proposalID, ordinal, title, dependencies)
		return err
	}
	if err := insertModule(1, "Same module", `[]`); err != nil {
		t.Fatal(err)
	}
	if err := insertModule(2, "same module", `[1]`); err == nil {
		t.Fatal("SQLite accepted a duplicate module title")
	}
	if err := insertModule(2, "Second module", `[1]`); err != nil {
		t.Fatal(err)
	}
}

func TestPlanDeliveryNullDependenciesPersistAndSettleTool(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "plan-null-dependencies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "persist a model plan with no dependencies", Profile: "review", Phase: "plan",
		ModelRoute: "store-plan/model", Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &storePlanProvider{responses: []*llm.ChatResponse{
		{Provider: "store-plan", Model: "model", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			ToolCalls: []llm.ToolCall{{ID: "null-dependencies-plan", Name: "plan_delivery_propose",
				Arguments: json.RawMessage(strings.ReplaceAll(storePlanDeliveryPayload, `"dependencies":[]`, `"dependencies":null`))}}},
		{Text: storeRootWaitResponse(t), Provider: "store-plan", Model: "model", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}},
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	if _, err := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).Step(ctx, run.ID); err != nil {
		t.Fatalf("model plan with null dependencies did not finish: %v", err)
	}
	proposals, err := st.ListPlanDeliveryProposals(ctx, run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("model proposal was not persisted: proposals=%d err=%v", len(proposals), err)
	}
	var emptyArrays, pending int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_delivery_modules WHERE proposal_id=? AND dependencies_json='[]'`, proposals[0].ID).Scan(&emptyArrays); err != nil || emptyArrays != 3 {
		t.Fatalf("empty module dependencies were not stored as arrays: count=%d err=%v", emptyArrays, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_tool_calls WHERE run_id=? AND status='pending'`, run.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("plan tool left unresolved work: pending=%d err=%v", pending, err)
	}
	if _, err := application.NewPlanDeliveryService(st).Select(ctx, application.SelectPlanDeliveryDirectionRequest{
		ProposalID: proposals[0].ID, Direction: 2, OperationKey: "select-null-dependencies", RequestedBy: "operator",
	}); err != nil {
		t.Fatalf("persisted model plan cannot be selected: %v", err)
	}
}

type storePlanProvider struct {
	responses []*llm.ChatResponse
}

func (*storePlanProvider) Name() string { return "store-plan" }

func (*storePlanProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "store-plan", Capabilities: []string{"chat", "tools"}}}, nil
}

func (p *storePlanProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	if len(p.responses) == 0 {
		return nil, errors.New("store Plan provider response queue is empty")
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	copy := *response
	copy.ToolCalls = append([]llm.ToolCall{}, response.ToolCalls...)
	return &copy, nil
}

func (p *storePlanProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		chunks <- llm.ChatChunk{Text: response.Text}
	}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*storePlanProvider) SupportsTools(string) bool    { return true }
func (*storePlanProvider) SupportsVision(string) bool   { return false }
func (*storePlanProvider) SupportsJSONMode(string) bool { return true }

func storeRootWaitResponse(t *testing.T) string {
	t.Helper()
	encoded, err := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion,
		Kind: domain.RootActionWait, Message: "three directions are ready",
		Reason: "operator direction choice required"})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
