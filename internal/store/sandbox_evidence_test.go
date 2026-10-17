package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/sandbox"
)

func TestSandboxBackendEvidenceAndOutputSimulationLedgersAreImmutableAndPrivate(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	service, manifest, preflight := createDockerPreflightStoreFixture(t, ctx, st, run.ID,
		"store-evidence")
	evidence, err := service.RecordSimulatedBackendEvidence(ctx,
		application.RecordSandboxBackendEvidenceRequest{
			PreflightID: preflight.ID, Manifest: manifest,
			ImageDigest:  "sha256:" + strings.Repeat("c", 64),
			OperationKey: "store-evidence-record", RequestedBy: "store_evidence_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	fixture := sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion,
		Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout, FileType: sandbox.OutputFileTypeStream, Content: "ok"},
			{Kind: sandbox.OutputKindStderr, FileType: sandbox.OutputFileTypeStream,
				Content: "TOKEN=sk-123456789012345678901234567890"},
		}}
	simulation, err := service.SimulateOutputTransaction(ctx,
		application.SimulateSandboxOutputRequest{
			EvidenceID: evidence.ID, Manifest: manifest, Fixture: fixture,
			OperationKey: "store-output-simulation", RequestedBy: "store_evidence_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	var evidenceItems, outputItems, artifactCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandbox_backend_evidence_items
		WHERE evidence_id = ?`, evidence.ID).Scan(&evidenceItems); err != nil || evidenceItems != 16 {
		t.Fatalf("evidence item count=%d err=%v", evidenceItems, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandbox_output_simulation_items
		WHERE simulation_id = ?`, simulation.ID).Scan(&outputItems); err != nil || outputItems != 2 {
		t.Fatalf("output simulation item count=%d err=%v", outputItems, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_artifacts
		WHERE run_id = ?`, run.ID).Scan(&artifactCount); err != nil || artifactCount != 0 {
		t.Fatalf("simulation created production Artifacts: count=%d err=%v", artifactCount, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_backend_evidence
		SET production_verified = 1 WHERE id = ?`, evidence.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("backend evidence root was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_backend_evidence_items
		WHERE evidence_id = ? AND ordinal = 1`, evidence.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("backend evidence item was deletable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_output_simulations
		SET production_artifact_count = 1 WHERE id = ?`, simulation.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("output simulation root was mutable: %v", err)
	}

	for _, table := range []string{"sandbox_backend_evidence", "sandbox_backend_evidence_items",
		"sandbox_output_simulations", "sandbox_output_simulation_items"} {
		rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			switch name {
			case "content", "raw_path", "output_path", "workspace_root", "command",
				"arguments_json", "secret_value", "lease_id", "owner_id", "container_id":
				_ = rows.Close()
				t.Fatalf("schema v52 persists private data in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}

	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range timeline {
		if event.Type != events.SandboxBackendEvidenceRecordedEvent &&
			event.Type != events.SandboxOutputSimulationRecordedEvent {
			continue
		}
		if strings.Contains(event.PayloadJSON, root) || strings.Contains(event.PayloadJSON, "sk-123456") ||
			strings.Contains(event.PayloadJSON, evidence.Report.ImageDigest) ||
			strings.Contains(event.PayloadJSON, simulation.FixtureDigest) ||
			strings.Contains(event.PayloadJSON, preflight.OutputPlan.Slots[0].LocatorFingerprint) {
			t.Fatalf("schema v52 event leaked private data: %#v", event)
		}
	}
}

func TestSandboxBackendEvidenceConcurrentReplayConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cyberagent.db")
	st1, run, _ := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = st1.Close() })
	service1, manifest, preflight := createDockerPreflightStoreFixture(t, ctx, st1, run.ID,
		"concurrent-evidence")
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	services := []*application.SandboxManifestService{
		service1, application.NewSandboxManifestService(st2, policy.NewDefaultChecker()),
	}
	request := application.RecordSandboxBackendEvidenceRequest{
		PreflightID: preflight.ID, Manifest: manifest,
		ImageDigest:  "sha256:" + strings.Repeat("d", 64),
		OperationKey: "concurrent-evidence-record", RequestedBy: "store_evidence_operator",
	}
	start := make(chan struct{})
	results := make([]sandbox.BackendEvidence, len(services))
	errorsFound := make([]error, len(services))
	var group sync.WaitGroup
	for index := range services {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], errorsFound[index] = services[index].RecordSimulatedBackendEvidence(ctx, request)
		}(index)
	}
	close(start)
	group.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || results[0].ID == "" ||
		results[0].ID != results[1].ID || results[0].Replayed == results[1].Replayed {
		t.Fatalf("concurrent backend evidence replay diverged: results=%#v errors=%v",
			results, errorsFound)
	}
	fixture := sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion,
		Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout, FileType: sandbox.OutputFileTypeStream, Content: "out"},
			{Kind: sandbox.OutputKindStderr, FileType: sandbox.OutputFileTypeStream, Content: "err"},
		}}
	outputRequest := application.SimulateSandboxOutputRequest{
		EvidenceID: results[0].ID, Manifest: manifest, Fixture: fixture,
		OperationKey: "concurrent-output-simulation", RequestedBy: "store_evidence_operator",
	}
	outputResults := make([]sandbox.OutputSimulation, len(services))
	outputErrors := make([]error, len(services))
	start = make(chan struct{})
	for index := range services {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			outputResults[index], outputErrors[index] = services[index].SimulateOutputTransaction(ctx,
				outputRequest)
		}(index)
	}
	close(start)
	group.Wait()
	if outputErrors[0] != nil || outputErrors[1] != nil || outputResults[0].ID == "" ||
		outputResults[0].ID != outputResults[1].ID ||
		outputResults[0].Replayed == outputResults[1].Replayed {
		t.Fatalf("concurrent output simulation replay diverged: results=%#v errors=%v",
			outputResults, outputErrors)
	}
}

func TestSandboxOutputSimulationLimitFailsClosedInServiceAndSQL(t *testing.T) {
	ctx := context.Background()
	st, run, _ := openSandboxManifestStore(t, ctx)
	service, manifest, preflight := createDockerPreflightStoreFixture(t, ctx, st, run.ID,
		"output-limit")
	evidence, err := service.RecordSimulatedBackendEvidence(ctx,
		application.RecordSandboxBackendEvidenceRequest{
			PreflightID: preflight.ID, Manifest: manifest,
			ImageDigest:  "sha256:" + strings.Repeat("f", 64),
			OperationKey: "output-limit-evidence", RequestedBy: "store_evidence_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	fixture := sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion,
		Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout, FileType: sandbox.OutputFileTypeStream, Content: "out"},
			{Kind: sandbox.OutputKindStderr, FileType: sandbox.OutputFileTypeStream, Content: "err"},
		}}
	var last sandbox.OutputSimulation
	for index := 0; index < sandbox.MaxOutputSimulationsPerEvidence; index++ {
		last, err = service.SimulateOutputTransaction(ctx,
			application.SimulateSandboxOutputRequest{
				EvidenceID: evidence.ID, Manifest: manifest, Fixture: fixture,
				OperationKey: "output-limit-simulation-" + string(rune('a'+index)),
				RequestedBy:  "store_evidence_operator",
			})
		if err != nil {
			t.Fatalf("simulation %d failed: %v", index+1, err)
		}
	}
	_, err = service.SimulateOutputTransaction(ctx, application.SimulateSandboxOutputRequest{
		EvidenceID: evidence.ID, Manifest: manifest, Fixture: fixture,
		OperationKey: "output-limit-simulation-overflow", RequestedBy: "store_evidence_operator",
	})
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("ninth simulation code=%s err=%v", apperror.CodeOf(err), err)
	}

	_, err = st.db.ExecContext(ctx, `INSERT INTO sandbox_output_simulations
		SELECT ?, evidence_id, preflight_id, execution_id, run_id, mission_id, workspace_id,
			protocol_version, status, output_plan_fingerprint, fixture_digest, transaction_digest,
			expected_slot_count, staged_output_count, staged_output_bytes, fake_artifact_count,
			production_artifact_count, all_or_nothing, simulation_only,
			artifact_commit_authorized, backend_enabled, execution_authorized, requested_by, created_at
		FROM sandbox_output_simulations WHERE id = ?`, "sandbox-output-sim-overflow", last.ID)
	if err == nil || !strings.Contains(err.Error(), "sandbox output simulation binding is invalid") {
		t.Fatalf("SQL bypassed the per-evidence simulation limit: %v", err)
	}
}

func TestSchemaV51UpgradeAddsSandboxEvidenceWithoutLosingPreflight(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v51.db")
	st, run, _ := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	_, _, preflight := createDockerPreflightStoreFixture(t, ctx, st, run.ID, "upgrade-evidence")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, path, 51)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v51 did not upgrade to v52: version=%d err=%v", version, err)
	}
	loaded, err := st.GetSandboxDisabledPreflight(ctx, preflight.ID)
	if err != nil || loaded.ID != preflight.ID {
		t.Fatalf("schema v51 preflight was not preserved: %#v err=%v", loaded, err)
	}
	var table string
	if err := st.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'sandbox_backend_evidence'`).Scan(&table); err != nil ||
		table != "sandbox_backend_evidence" {
		t.Fatalf("schema v52 evidence ledger is missing: %q err=%v", table, err)
	}
}

func createDockerPreflightStoreFixture(t *testing.T, ctx context.Context, st *SQLiteStore,
	runID, prefix string,
) (*application.SandboxManifestService, sandbox.Manifest, sandbox.DisabledPreflight) {
	t.Helper()
	service := newSandboxFixtureService(t, st)
	manifest := sandboxStoreTestManifest()
	manifest.Backend = sandbox.BackendDocker
	prepared, err := service.Prepare(ctx, application.PrepareSandboxManifestRequest{
		RunID: runID, Manifest: manifest, OperationKey: prefix + "-prepare-operation",
		RequestedBy: "store_evidence_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.RequestApproval(ctx, prepared.Preparation.ID, "store_evidence_operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewApproval(ctx, prepared.Preparation.ID, approval.ActionApprove,
		prefix+"-review-operation", "store_evidence_operator", ""); err != nil {
		t.Fatal(err)
	}
	validated, err := service.ValidateExecutionCandidate(ctx,
		application.ValidateSandboxExecutionCandidateRequest{
			PreparationID: prepared.Preparation.ID, Manifest: manifest, ApprovalID: record.ID,
			OperationKey: prefix + "-candidate-operation", RequestedBy: "store_evidence_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := service.BeginDisabledExecution(ctx, application.BeginSandboxExecutionRequest{
		CandidateID: validated.Candidate.ID, Manifest: manifest,
		OperationKey: prefix + "-begin-operation", RequestedBy: "store_evidence_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.PrepareDisabledPreflight(ctx,
		application.PrepareSandboxPreflightRequest{
			ExecutionID: lifecycle.Execution.ID, Manifest: manifest,
			OperationKey: prefix + "-preflight-operation", RequestedBy: "store_evidence_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	return service, manifest, preflight
}
