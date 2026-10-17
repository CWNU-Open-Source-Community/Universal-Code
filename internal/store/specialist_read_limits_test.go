package store

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"path/filepath"
	"testing"
	"time"
)

func TestSpecialistIndependentCostLimitWithoutRunCapSurvivesRetryReopen(t *testing.T) {
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "child-only-cost.db")
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "independent child cost", Profile: "code", WorkspaceID: "cost-workspace", Budget: domain.Budget{MaxTurns: 12, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	prices := monetaryTestSnapshot(t, time.Now().UTC())
	if _, err = application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, _, err := st.GetRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	children := []domain.AgentNode{}
	for _, name := range []string{"first", "second"} {
		child, _, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID, ParentAgentID: root.ID, Title: name, Skills: []string{"model.chat"}, TurnLimit: 3, TokenLimit: 64, MaxChildren: 2, CreatedAt: time.Now().UTC(), AllowedReadPaths: []string{"notes.txt"}, ToolCallLimit: 1, CostLimitMicros: 1000}, "cost-child-admission-"+name)
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	start := func(child domain.AgentNode, key string) (domain.AgentAttemptRef, llm.ModelAttempt) {
		active, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: child.ID, ParentAgentID: root.ID, Lease: lease, StartedAt: time.Now().UTC()}, key)
		if err != nil {
			t.Fatal(err)
		}
		ref := attemptRef(active)
		brief, err := st.PrepareSpecialistContext(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		a := llm.ModelAttempt{SpecialistAttemptID: ref.AttemptID, Number: 1, TransportAttempt: 1, MaxAttempts: 2, Provider: "mock", Model: "mock-code", Context: &llm.ModelContextAudit{TokenBudget: 1, EstimatedTokens: 1, Included: []llm.ModelContextSource{{Kind: "specialist_task_brief", SourceID: brief.TaskBrief.Fingerprint, Tokens: 1}}}}
		if _, err := st.RecordSpecialistModelStarted(ctx, ref, a); err != nil {
			t.Fatal(err)
		}
		return ref, a
	}
	reserve := func(a llm.ModelAttempt, amount int64) error {
		_, _, err := st.ReserveModelCost(ctx, domain.MonetaryReserveRequest{RunID: run.ID, Scope: domain.MonetaryScopeSpecialist, Provider: a.Provider, Model: a.Model, AttemptNumber: a.MonetaryAttemptNumber(), ReservedMicros: amount, PriceFingerprint: prices.Fingerprint, EstimateSource: "child-cost-test"})
		return err
	}
	ref, a := start(children[0], "cost-first-attempt-0001")
	budget := application.NewMonetaryBudgetService(st)
	if _, err = budget.ReserveModelCall(ctx, run, domain.MonetaryScopeSpecialist, a, llm.ChatRequest{MaxTokens: 496}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("missing child prices were accepted: %v", err)
	}
	if _, _, err = st.ImportPriceSnapshot(ctx, prices); err != nil {
		t.Fatal(err)
	}
	if _, err = budget.ReserveModelCall(ctx, run, domain.MonetaryScopeSpecialist, a, llm.ChatRequest{MaxTokens: 496}); err != nil {
		t.Fatalf("child-only application pricing gate: %v", err)
	}
	a.Outcome, a.ErrorText, a.RetryPlanned = llm.OutcomeRetryable, "lost response", true
	if _, err = st.RecordSpecialistModelFailed(ctx, ref, a, nil); err != nil {
		t.Fatal(err)
	}
	// The unknown sent call keeps its exposure, even before settlement is replayed.
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	money, err := st.GetMonetaryUsage(ctx, run.ID)
	if err != nil || !money.Tracked || money.CapMicros != 0 || money.ReservedMicros != 1000 || money.ReleasedMicros != 0 {
		t.Fatalf("child-only ledger: %+v %v", money, err)
	}
	a.Number, a.TransportAttempt, a.Outcome, a.ErrorText, a.RetryPlanned = 2, 2, "", "", false
	if _, err = st.RecordSpecialistModelStarted(ctx, ref, a); err != nil {
		t.Fatal(err)
	}
	if err = reserve(a, 1); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("retry reset child cost: %v", err)
	}
	a.Outcome, a.ErrorText = llm.OutcomePermanent, "budget denied before dispatch"
	if _, err = st.RecordSpecialistModelNotDispatched(ctx, ref, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CrashSpecialistAttempt(ctx, domain.AgentAttemptFailureRequest{Ref: ref, Failure: domain.AgentAttemptFailure{Code: "cost_limit", Reason: "child budget exhausted"}, NotificationMessageID: idgen.New("agentmsg"), FailedAt: time.Now().UTC()}, "cost-crash-attempt-0001"); err != nil {
		t.Fatal(err)
	}
	// A new attempt shares the first child's already consumed cost.
	_, next := start(children[0], "cost-first-next-attempt-0001")
	if err = reserve(next, 1); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("new attempt reset child cost: %v", err)
	}
	// A second child can spend its own cap after the first has exhausted its cap.
	_, other := start(children[1], "cost-second-attempt-0001")
	if err = reserve(other, 1000); err != nil {
		t.Fatalf("second child borrowed exhausted budget: %v", err)
	}
	node, err := st.GetAgentNode(ctx, children[0].ID)
	if err != nil || node.CostUsedMicros != 1000 {
		t.Fatalf("first child cost projection: %+v %v", node, err)
	}
	node, err = st.GetAgentNode(ctx, children[1].ID)
	if err != nil || node.CostUsedMicros != 1000 {
		t.Fatalf("second child cost projection: %+v %v", node, err)
	}
	// Lifecycle projection and snapshots must preserve the separate child authority
	// and cost ledger when pausing and resuming active attempts.
	for _, transition := range []func(context.Context, string) (domain.Run, error){application.NewRunService(st).Pause, application.NewRunService(st).Resume} {
		if _, err := transition(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		graph, err := st.RestoreAgentGraph(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, child := range graph.Nodes {
			if child.ParentID == "" {
				continue
			}
			if len(child.AllowedReadPaths) != 1 || child.AllowedReadPaths[0] != "notes.txt" || child.ToolCallLimit != 1 || child.CostLimitMicros != 1000 || child.CostUsedMicros != 1000 {
				t.Fatalf("lifecycle changed child scope or cumulative cost: %+v", child)
			}
		}
	}
	// Import only the v199 columns into an actual historical schema. Existing
	// model-start receipts must recover reservation ownership on upgrade, while
	// historical children receive no new scope or independent limits.
	legacyPath := filepath.Join(t.TempDir(), "populated-v199.db")
	legacy := historicalTestDatabaseFromSeed(t, st, legacyPath, 199)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	for _, child := range children {
		node, err := upgraded.GetAgentNode(ctx, child.ID)
		if err != nil || len(node.AllowedReadPaths) != 0 || node.ToolCallLimit != 0 || node.CostLimitMicros != 0 || node.CostUsedMicros != 1000 {
			t.Fatalf("populated upgrade lost ownership or granted authority: %+v %v", node, err)
		}
	}
}

func TestSchemaV200PreservesMigrationHistoryAndDefaultsToNoReadAuthority(t *testing.T) {
	file := filepath.Join(t.TempDir(), "v199.db")
	st := openHistoricalTestDatabase(t, file, 199)
	before, err := st.loadAppliedMigrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for version, entry := range before {
		if after[version] != entry {
			t.Fatalf("migration %d changed", version)
		}
	}
	for _, table := range []string{"agent_nodes", "specialist_tool_bindings"} {
		var def string
		if err = st.db.QueryRow(`SELECT dflt_value FROM pragma_table_info(?) WHERE name='allowed_read_paths_json'`, table).Scan(&def); err != nil || def != "'[]'" {
			t.Fatalf("%s inherited read scope: %s %v", table, def, err)
		}
	}
}
