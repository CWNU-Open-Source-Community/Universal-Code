package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolbudget"
)

func TestSchemaV182KeepsFrozenHistoricalPrefix(t *testing.T) {
	plan := migrationPlan()
	if len(plan) < 182 || plan[181].Version != 182 {
		t.Fatal("v182 is not registered")
	}
	// Reviewed C34a6142/B12 v181 baseline plus the v131 command-history repair.
	// TestSchemaV131AcceptsExactReleasedHistory covers the released v131 ledger.
	const previousDigest = "9beb9126888da0a1448054f923fede33c0d45d25f508841a5a8c54bf94111414"
	if got := migrationPlanDigest(plan[:181]); got != previousDigest {
		t.Fatalf("a historical migration changed: %s", got)
	}
	if plan[181].DisableForeignKeys || len(plan[181].Statements) != 16 {
		t.Fatal("v182 must replace exactly eight triggers with foreign keys enabled")
	}
}

func TestSchemaV182StatementBatchOnlyReplacesBudgetTriggers(t *testing.T) {
	state := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "v181-statements.db"), 181)
	readObjects := func() map[string]string {
		t.Helper()
		rows, err := state.db.QueryContext(t.Context(), `SELECT type || ':' || name, COALESCE(sql, '')
			FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		objects := map[string]string{}
		for rows.Next() {
			var name, definition string
			if err := rows.Scan(&name, &definition); err != nil {
				t.Fatal(err)
			}
			objects[name] = definition
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return objects
	}
	before := readObjects()
	step := migration{Version: 182, Name: "Live Sandbox candidate budgets under exact active Run leases",
		Statements: sandboxLiveCandidateBudgetStatements(migrationPlan()[:181])}
	if err := state.applyMigration(t.Context(), step); err != nil {
		t.Fatal(err)
	}
	after := readObjects()
	if len(after) != len(before) {
		t.Fatalf("migration added or lost schema objects: %d -> %d", len(before), len(after))
	}
	want := map[string]bool{}
	for _, name := range []string{"execution_candidate", "disabled_execution", "disabled_preflight", "backend_evidence",
		"output_simulation", "docker_observation", "docker_container_plan", "docker_product_admission"} {
		want["trigger:trg_sandbox_"+name+"_insert"] = true
	}
	changed := 0
	for name, definition := range before {
		if after[name] != definition {
			if !want[name] {
				t.Fatalf("migration changed an unrelated schema object: %s", name)
			}
			changed++
		}
	}
	if changed != 8 {
		t.Fatalf("migration changed %d triggers, want 8", changed)
	}
	assertNoForeignKeyViolations(t, state.db)
}

func TestSchemaV182UpgradesRealV181WithoutChangingRowsOrRecovery(t *testing.T) {
	checkV182PopulatedUpgrade(t, false)
}

// Exercise the migration body separately from the registered Open path. This
// also keeps upgrade behavior covered independently of the clean-install path.
func TestSchemaV182StatementBatchPreservesPopulatedV181(t *testing.T) {
	checkV182PopulatedUpgrade(t, true)
}

func checkV182PopulatedUpgrade(t *testing.T, direct bool) {
	t.Helper()
	ctx := t.Context()
	path, root := filepath.Join(t.TempDir(), "v181.db"), t.TempDir()
	state := openHistoricalTestDatabase(t, path, 181)
	run := newV182SandboxRun(t, state, root, domain.Budget{MaxTurns: 4, MaxToolCalls: 8})
	fixture := newDockerSandboxStoreFixture(t, ctx, state, run, root, "v182-history")
	insertV180DockerAdmission(t, state, fixture.Admission, 73)
	beginDockerSandboxStoreStart(t, ctx, state, fixture)
	if _, _, err := state.BeginDockerContainerLifecycle(ctx, fixture.Intent, "historical-owner", time.Minute); err != nil {
		t.Fatal(err)
	}
	launch := newDockerSandboxStoreLaunch(t, fixture)
	if _, _, err := state.BindDockerSandboxLaunch(ctx, launch); err != nil {
		t.Fatal(err)
	}
	tables := []string{"runs", "sessions", "sandbox_execution_candidates", "sandbox_execution_candidate_operations",
		"sandbox_disabled_executions", "sandbox_disabled_preflights", "sandbox_backend_evidence",
		"sandbox_output_simulations", "sandbox_docker_observations", "sandbox_docker_container_plans",
		"sandbox_docker_product_admissions", "sandbox_docker_product_start_requests", "sandbox_docker_product_launches",
		"sandbox_docker_lifecycle_intents", "sandbox_docker_lifecycle_transitions", "run_execution_permission_snapshots"}
	before := make(map[string][][]any)
	for _, table := range tables {
		before[table] = dockerMigrationRows(t, state, table)
	}
	ledger, err := state.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := state.SchemaVersion(ctx); err != nil || version != 181 {
		t.Fatalf("fixture is not genuine v181: version=%d err=%v", version, err)
	}
	expectedPlan := migrationPlan()
	if direct {
		step := migration{Version: 182, Name: "Live Sandbox candidate budgets under exact active Run leases",
			Statements: sandboxLiveCandidateBudgetStatements(expectedPlan[:181])}
		if err := state.applyMigration(ctx, step); err != nil {
			t.Fatal(err)
		}
		expectedPlan = append(append([]migration(nil), expectedPlan[:181]...), step)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	var upgraded *SQLiteStore
	if direct {
		upgraded, err = openTestDatabaseWithoutMigration(path)
	} else {
		upgraded, err = Open(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], dockerMigrationRows(t, upgraded, table)) {
			t.Fatalf("migration changed existing %s rows", table)
		}
	}
	var rowID int
	if err := upgraded.db.QueryRowContext(ctx, `SELECT rowid FROM sandbox_docker_product_admissions WHERE id=?`, fixture.Admission.ID).Scan(&rowID); err != nil || rowID != 73 {
		t.Fatalf("migration changed admission rowid: %d err=%v", rowID, err)
	}
	current, err := upgraded.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for version, record := range ledger {
		if current[version] != record {
			t.Fatalf("migration ledger entry %d changed", version)
		}
	}
	recoverable, err := upgraded.ListRecoverableDockerSandboxes(ctx, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].Admission.ID != fixture.Admission.ID {
		t.Fatalf("historical native recovery was lost: count=%d err=%v", len(recoverable), err)
	}
	if _, replayed, err := upgraded.BindDockerSandboxLaunch(ctx, launch); err != nil || !replayed {
		t.Fatalf("historical launch replay failed: replayed=%t err=%v", replayed, err)
	}
	for _, table := range []string{"sandbox_execution_candidates", "sandbox_docker_product_admissions"} {
		if _, err := upgraded.db.ExecContext(ctx, "UPDATE "+table+" SET requested_by='changed'"); err == nil {
			t.Fatalf("migration removed immutable update protection from %s", table)
		}
		if _, err := upgraded.db.ExecContext(ctx, "DELETE FROM "+table); err == nil {
			t.Fatalf("migration removed immutable delete protection from %s", table)
		}
	}
	states := []*SQLiteStore{upgraded}
	if !direct {
		fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		got, err := sqliteSchemaDigest(ctx, upgraded.db)
		if err != nil {
			t.Fatal(err)
		}
		want, err := sqliteSchemaDigest(ctx, fresh.db)
		if err != nil || got != want || want != cleanInstallBaselineSchemaSHA256 {
			t.Fatalf("v181 upgrade and clean install diverged: upgrade=%s fresh=%s err=%v", got, want, err)
		}
		states = append(states, fresh)
	}
	for _, state := range states {
		if direct {
			if version, err := state.SchemaVersion(ctx); err != nil || version != 182 || len(current) != 182 {
				t.Fatalf("direct migration did not retain the exact 182-entry ledger: version=%d records=%d err=%v", version, len(current), err)
			}
		} else {
			assertLatestMigrationLedger(t, state, expectedPlan)
		}
		assertNoForeignKeyViolations(t, state.db)
		var integrity string
		if err := state.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity check: %q err=%v", integrity, err)
		}
	}
}

func newV182SandboxRun(t *testing.T, state *SQLiteStore, root string, budget domain.Budget) domain.Run {
	t.Helper()
	if err := state.SaveWorkspace(t.Context(), WorkspaceRecord{ID: "ws-v182", Name: "v182", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(t.Context(), application.CreateRunRequest{
		Goal: "verify current Sandbox transaction budgets", Profile: "code", WorkspaceID: "ws-v182", Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type sandboxV182SQLFixture struct {
	state     *SQLiteStore
	run       domain.Run
	lease     domain.RunExecutionLease
	candidate sandbox.ExecutionCandidate
	admission domain.DockerSandboxAdmission
	planProbe sandbox.DockerContainerPlan
	ids       map[string]string
}

func (f sandboxV182SQLFixture) charge(t *testing.T) {
	t.Helper()
	if _, err := f.state.ChargeToolCall(t.Context(), toolbudget.ChargeRequest{
		RunID: f.run.ID, SessionID: f.run.SessionID, WorkspaceID: "ws-v182",
		ToolName: "command_runtime", ActionClass: "process", RequestedBy: "store_docker_plan_operator",
	}); err != nil {
		t.Fatal(err)
	}
}

func (f sandboxV182SQLFixture) setCounter(t *testing.T, kind string, value int64) {
	t.Helper()
	queries := map[string]string{
		"tokens": `UPDATE agent_nodes SET tokens_used=? WHERE run_id=? AND role='root'`,
		"millis": `UPDATE run_supervisor_checkpoints SET execution_millis=? WHERE run_id=?`,
		"tools":  `UPDATE run_tool_usage SET consumed=? WHERE run_id=?`,
	}
	result, err := f.state.db.ExecContext(t.Context(), queries[kind], value, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("counter fixture did not change exactly one real projection: %s rows=%d err=%v", kind, count, err)
	}
	if kind == "tokens" {
		// The root Agent projection and its Supervisor checkpoint must agree.
		// Keep this real invariant while varying the persisted usage under test.
		if _, err := f.state.db.ExecContext(t.Context(), `UPDATE run_supervisor_checkpoints
			SET input_tokens=?, output_tokens=0, total_tokens=? WHERE run_id=?`, value, value, f.run.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// This fixture uses existing native records and real store APIs. It creates a
// separate v2 candidate under a real current Run lease; it never updates the
// immutable original candidate or disables a trigger. Direct counter writes
// seed deterministic SQLite projections without invoking a model provider.
func newSandboxV182SQLFixture(t *testing.T, live, advance, direct bool) sandboxV182SQLFixture {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "current-budget.db")
	var state *SQLiteStore
	var err error
	if direct {
		state = openHistoricalTestDatabase(t, path, 181)
		if err := state.applyMigration(ctx, migration{Version: 182,
			Name:       "Live Sandbox candidate budgets under exact active Run leases",
			Statements: sandboxLiveCandidateBudgetStatements(migrationPlan()[:181])}); err != nil {
			t.Fatal(err)
		}
	} else {
		state, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = state.Close() })
	root := t.TempDir()
	run := newV182SandboxRun(t, state, root, domain.Budget{MaxTurns: 4, MaxTokens: 10, MaxToolCalls: 4, TimeoutSeconds: 120})
	f := sandboxV182SQLFixture{state: state, run: run}
	if _, _, err := state.RegisterRootAgent(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := state.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := upsertSupervisorCheckpointTx(ctx, tx, domain.SupervisorCheckpoint{
		RunID: run.ID, NextTurn: 1, Phase: domain.SupervisorIdle, ExecutionMillis: 1000, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.setCounter(t, "tokens", 1)
	f.charge(t)
	base := newDockerSandboxStoreFixture(t, ctx, state, run, root, "v182-sql-authority")
	validated, err := state.GetSandboxExecutionCandidate(ctx, base.Plan.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	f.candidate = validated.Candidate
	plan := base.Plan
	if live {
		acquired, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
			RunID: run.ID, OwnerID: "v182-native-owner", TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		f.lease = acquired.Lease
		f.candidate.ID, f.candidate.ValidatedAt = idgen.New("v182-native-candidate"), time.Now().UTC()
		f.candidate.LeaseQuiescent = false
		f.candidate.RunLeaseID, f.candidate.RunLeaseGeneration, f.candidate.RunLeaseOwnerID = f.lease.LeaseID, f.lease.Generation, f.lease.OwnerID
		operation := sandbox.CandidateOperation{KeyDigest: runmutation.Fingerprint("v182-native", f.candidate.ID),
			RequestFingerprint: sandbox.CandidateOperationRequestFingerprint(f.candidate), CandidateID: f.candidate.ID,
			PreparationID: f.candidate.PreparationID, RunID: run.ID, RequestedBy: f.candidate.RequestedBy, CreatedAt: f.candidate.ValidatedAt}
		if _, _, err := state.CreateSandboxExecutionCandidate(ctx, f.candidate, operation); err != nil {
			t.Fatal(err)
		}
		if advance {
			f.charge(t)
			f.setCounter(t, "tokens", 2)
			f.setCounter(t, "millis", 2000)
		}
		plan = createV182NativePlan(t, f, base.Manifest)
	}
	if f.candidate.TokensUsed != 1 || f.candidate.ExecutionMillisUsed != 1000 || f.candidate.ToolCallsUsed != 1 {
		t.Fatalf("fixture lost original usage: %+v", f.candidate)
	}
	f.admission = base.Admission
	f.admission.ID = idgen.New("v182-admission")
	f.admission.OperationKeyDigest = runmutation.Fingerprint("v182-admission", f.admission.ID)
	f.admission.LifecycleOperationDigest = runmutation.Fingerprint("v182-lifecycle", f.admission.ID)
	f.admission.PlanID, f.admission.CandidateID = plan.ID, plan.CandidateID
	f.admission.PlanFingerprint, f.admission.SpecFingerprint = plan.PlanFingerprint, plan.SpecFingerprint
	f.admission.AuthorityFingerprint = plan.AuthorityFingerprint
	f.admission.CreatedAt, f.admission.ReadinessExpiresAt = time.Now().UTC(), time.Now().UTC().Add(time.Minute)
	f.admission = f.currentAdmission(t)
	if _, _, err := state.CreateDockerSandboxAdmission(ctx, f.admission); err != nil {
		t.Fatal(err)
	}
	var executionID, preflightID, evidenceID, simulationID, observationID string
	if err := state.db.QueryRowContext(ctx, `SELECT execution.id, preflight.id, evidence.id, simulation.id, observation.id
		FROM sandbox_docker_container_plans plan
		JOIN sandbox_docker_observations observation ON observation.id=plan.observation_id
		JOIN sandbox_output_simulations simulation ON simulation.id=observation.output_simulation_id
		JOIN sandbox_backend_evidence evidence ON evidence.id=observation.evidence_id
		JOIN sandbox_disabled_preflights preflight ON preflight.id=evidence.preflight_id
		JOIN sandbox_disabled_executions execution ON execution.id=preflight.execution_id
		WHERE plan.id=?`, plan.ID).Scan(&executionID, &preflightID, &evidenceID, &simulationID, &observationID); err != nil {
		t.Fatal(err)
	}
	f.ids = map[string]string{"sandbox_execution_candidates": f.candidate.ID,
		"sandbox_disabled_executions": executionID, "sandbox_disabled_preflights": preflightID,
		"sandbox_backend_evidence": evidenceID, "sandbox_output_simulations": simulationID,
		"sandbox_docker_observations": observationID, "sandbox_docker_container_plans": plan.ID,
		"sandbox_docker_product_admissions": f.admission.ID}
	// The plan trigger permits only one plan per observation, even for a
	// duplicate INSERT OR IGNORE. Give its SQL probe an unused real observation
	// so that this independent authority condition cannot mask a budget failure.
	service := application.NewSandboxManifestService(state, policy.NewDefaultChecker())
	service.WithDockerProductionObserver(sandbox.NewReadOnlyDockerProductionObserver(
		dockerObservationStoreTransport{imageDigest: plan.ImageDigest}))
	observation, err := service.ObserveDockerBackend(ctx, application.ObserveDockerBackendRequest{
		EvidenceID: evidenceID, OutputSimulationID: simulationID, Manifest: base.Manifest,
		OperationKey: "v182-plan-sql-probe", RequestedBy: f.candidate.RequestedBy})
	if err != nil {
		t.Fatal(err)
	}
	f.planProbe, _ = newDockerContainerPlanStoreRecord(t, ctx, observation, base.Manifest, "v182-plan-sql-probe")
	return f
}

func createV182NativePlan(t *testing.T, f sandboxV182SQLFixture, manifest sandbox.Manifest) sandbox.DockerContainerPlan {
	t.Helper()
	ctx, requestedBy := t.Context(), f.candidate.RequestedBy
	service := application.NewSandboxManifestService(f.state, policy.NewDefaultChecker())
	lifecycle, err := service.BeginDisabledExecution(ctx, application.BeginSandboxExecutionRequest{
		CandidateID: f.candidate.ID, Manifest: manifest, OperationKey: "v182-native-lifecycle", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.PrepareDisabledPreflight(ctx, application.PrepareSandboxPreflightRequest{
		ExecutionID: lifecycle.Execution.ID, Manifest: manifest, OperationKey: "v182-native-preflight", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := "sha256:" + strings.Repeat("9", 64)
	evidence, err := service.RecordSimulatedBackendEvidence(ctx, application.RecordSandboxBackendEvidenceRequest{
		PreflightID: preflight.ID, Manifest: manifest, ImageDigest: imageDigest, OperationKey: "v182-native-evidence", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	simulation, err := service.SimulateOutputTransaction(ctx, application.SimulateSandboxOutputRequest{
		EvidenceID: evidence.ID, Manifest: manifest, OperationKey: "v182-native-simulation", RequestedBy: requestedBy,
		Fixture: sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion, Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout, FileType: sandbox.OutputFileTypeStream, Content: "stdout"},
			{Kind: sandbox.OutputKindStderr, FileType: sandbox.OutputFileTypeStream, Content: "stderr"},
			{Kind: sandbox.OutputKindFile, FileType: sandbox.OutputFileTypeRegular, Content: "{}"}}}})
	if err != nil {
		t.Fatal(err)
	}
	service.WithDockerProductionObserver(sandbox.NewReadOnlyDockerProductionObserver(dockerObservationStoreTransport{imageDigest: imageDigest}))
	observation, err := service.ObserveDockerBackend(ctx, application.ObserveDockerBackendRequest{
		EvidenceID: evidence.ID, OutputSimulationID: simulation.ID, Manifest: manifest,
		OperationKey: "v182-native-observation", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	plan, operation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest, "v182-native-plan")
	if _, _, err := f.state.CreateDockerContainerPlan(ctx, plan, operation); err != nil {
		t.Fatal(err)
	}
	return plan
}

func (f sandboxV182SQLFixture) currentAdmission(t *testing.T) domain.DockerSandboxAdmission {
	t.Helper()
	value := f.admission
	usage, err := f.state.GetRunAgentUsage(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := f.state.GetToolCallUsage(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	value.ToolCallsRemaining = f.run.Budget.MaxToolCalls - tools.Consumed
	value.WallClockSeconds = int((f.run.Budget.TimeoutSeconds*1000 - usage.TotalExecutionMillis) / 1000)
	value.AdmissionFingerprint = domain.DockerSandboxAdmissionFingerprint(value)
	return value
}

// Most probes use INSERT OR IGNORE to encounter the existing immutable id after
// evaluating the real BEFORE INSERT trigger. Plans use a fresh observation and
// a rolled-back insert because their trigger explicitly rejects duplicates.
// Both paths isolate SQL enforcement from Go validation without changing rows.
// The progress fixture also creates the entire native chain successfully after
// all three real counters advance.
func TestSchemaV182SQLTriggersRecheckAllCurrentBudgetAndLeaseBindings(t *testing.T) {
	checkV182SQLTriggerMatrix(t, false)
}

func TestSchemaV182StatementBatchSQLTriggers(t *testing.T) {
	checkV182SQLTriggerMatrix(t, true)
}

func checkV182SQLTriggerMatrix(t *testing.T, direct bool) {
	t.Helper()
	for _, change := range []string{"unchanged", "progress", "quiescent", "tokens rollback", "millis rollback", "tools rollback",
		"tokens exhausted", "millis exhausted", "tools exhausted", "lease revoked", "lease replaced", "lease expired", "run cancelled",
		"stale admission tools", "stale admission time"} {
		t.Run(change, func(t *testing.T) {
			f := newSandboxV182SQLFixture(t, change != "quiescent", change == "progress", direct)
			ctx := t.Context()
			before := make(map[string][][]any)
			for table := range f.ids {
				before[table] = dockerMigrationRows(t, f.state, table)
			}
			switch change {
			case "quiescent", "stale admission tools":
				f.charge(t)
			case "stale admission time":
				f.setCounter(t, "millis", 2000)
			case "tokens rollback":
				f.setCounter(t, "tokens", 0)
			case "millis rollback":
				f.setCounter(t, "millis", 0)
			case "tools rollback":
				f.setCounter(t, "tools", 0)
			case "tokens exhausted":
				f.setCounter(t, "tokens", f.run.Budget.MaxTokens)
			case "millis exhausted":
				f.setCounter(t, "millis", f.run.Budget.TimeoutSeconds*1000)
			case "tools exhausted":
				for i := int64(1); i < f.run.Budget.MaxToolCalls; i++ {
					f.charge(t)
				}
			case "lease revoked", "lease replaced":
				if _, _, err := f.state.ReleaseRunExecutionLease(ctx, f.lease); err != nil {
					t.Fatal(err)
				}
				if change == "lease replaced" {
					if _, err := f.state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
						RunID: f.run.ID, OwnerID: "v182-replacement-owner", TTL: time.Minute}); err != nil {
						t.Fatal(err)
					}
				}
			case "lease expired":
				if _, err := f.state.db.ExecContext(ctx, `UPDATE run_execution_leases SET expires_at=? WHERE run_id=?`,
					ts(time.Now().UTC().Add(-time.Millisecond)), f.run.ID); err != nil {
					t.Fatal(err)
				}
			case "run cancelled":
				if _, err := application.NewRunService(f.state).Cancel(ctx, f.run.ID); err != nil {
					t.Fatal(err)
				}
			}
			for _, table := range []string{"sandbox_execution_candidates", "sandbox_disabled_executions", "sandbox_disabled_preflights",
				"sandbox_backend_evidence", "sandbox_output_simulations", "sandbox_docker_observations", "sandbox_docker_container_plans", "sandbox_docker_product_admissions"} {
				if strings.HasPrefix(change, "stale admission") && table != "sandbox_docker_product_admissions" {
					continue
				}
				t.Run(table, func(t *testing.T) {
					overrides := map[string]any{}
					if table == "sandbox_docker_product_admissions" && !strings.HasPrefix(change, "stale admission") {
						current := f.currentAdmission(t)
						overrides["wall_clock_seconds"], overrides["tool_calls_remaining"] = current.WallClockSeconds, current.ToolCallsRemaining
						overrides["admission_fingerprint"] = current.AdmissionFingerprint
					}
					var err error
					if table == "sandbox_docker_container_plans" {
						err = probeV182SQLPlanInsert(ctx, f.state, f.planProbe)
					} else {
						err = probeV182SQLInsert(ctx, f.state, table, f.ids[table], overrides)
					}
					if change == "unchanged" || change == "progress" {
						if err != nil {
							t.Fatalf("SQL rejected valid current usage: %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), "binding is invalid") {
						t.Fatalf("SQL did not reject stale current authority at its binding trigger: %v", err)
					}
				})
			}
			for table, rows := range before {
				if !reflect.DeepEqual(rows, dockerMigrationRows(t, f.state, table)) {
					t.Fatalf("SQL probe changed immutable %s records", table)
				}
			}
		})
	}
}

func probeV182SQLPlanInsert(ctx context.Context, state *SQLiteStore, plan sandbox.DockerContainerPlan) error {
	tx, err := state.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	insertErr := insertDockerContainerPlanTx(ctx, tx, plan)
	rollbackErr := tx.Rollback()
	if insertErr != nil {
		return insertErr
	}
	return rollbackErr
}

func probeV182SQLInsert(ctx context.Context, state *SQLiteStore, table, id string, overrides map[string]any) error {
	rows, err := state.db.QueryContext(ctx, "SELECT * FROM "+table+" WHERE id=?", id)
	if err != nil {
		return err
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return err
	}
	values, pointers := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if !rows.Next() {
		_ = rows.Close()
		return fmt.Errorf("missing SQL trigger fixture %s %s", table, id)
	}
	if err := rows.Scan(pointers...); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i, column := range columns {
		if value, found := overrides[column]; found {
			values[i] = value
		}
		columns[i] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
	}
	query := "INSERT OR IGNORE INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
	result, err := state.db.ExecContext(ctx, query, values...)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 0 {
		return fmt.Errorf("SQL trigger probe unexpectedly wrote rows: %d err=%v", count, err)
	}
	return nil
}
