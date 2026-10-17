package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerContainerPlanLedgerIsImmutablePrivateAndNonAuthorizing(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st,
		run.ID, root, "docker-plan-ledger")
	plan, operation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
		"docker-plan-ledger-operation")
	stored, replayed, err := st.CreateDockerContainerPlan(ctx, plan, operation)
	if err != nil || replayed || stored.ID != plan.ID {
		t.Fatalf("create Docker plan: stored=%#v replayed=%t err=%v", stored, replayed, err)
	}
	if stored.ProductionSubmitted || stored.ProductionVerified || stored.BackendAvailable ||
		stored.BackendEnabled || stored.ExecutionAuthorized || stored.ArtifactCommitAuthorized ||
		stored.Transaction.DaemonWriteCount != 0 || stored.Transaction.BackendTouched {
		t.Fatalf("Docker plan gained production authority: %#v", stored)
	}
	loaded, err := st.GetDockerContainerPlan(ctx, stored.ID)
	if err != nil || loaded.PlanFingerprint != stored.PlanFingerprint ||
		len(loaded.Controls) != sandbox.MaxDockerContainerControls ||
		len(loaded.Transaction.Steps) != sandbox.MaxDockerWriteSteps {
		t.Fatalf("load Docker plan: loaded=%#v err=%v", loaded, err)
	}
	listed, err := st.ListDockerContainerPlans(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != stored.ID {
		t.Fatalf("list Docker plans: values=%#v err=%v", listed, err)
	}

	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_container_plans
		SET production_submitted = 1 WHERE id = ?`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Docker plan root was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_container_plan_controls
		WHERE plan_id = ? AND ordinal = 1`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Docker plan control was deletable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_container_plan_steps
		SET production_applied = 1 WHERE plan_id = ? AND ordinal = 1`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Docker plan step was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_container_plan_operations
		WHERE plan_id = ?`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Docker plan operation was deletable: %v", err)
	}
	_, unsupportedManifest, unsupportedObservation := createDockerContainerPlanStoreAuthority(t,
		ctx, st, run.ID, root, "docker-plan-unsupported-authority")
	unsupported, _ := newDockerContainerPlanStoreRecord(t, ctx, unsupportedObservation,
		unsupportedManifest, "docker-plan-unsupported-authority")
	unsupported.BackendAvailable = true
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerContainerPlanTx(ctx, tx, unsupported); err == nil {
		_ = tx.Rollback()
		t.Fatal("direct SQL accepted production authority in a Docker plan")
	}
	_ = tx.Rollback()

	_, incompleteManifest, incompleteObservation := createDockerContainerPlanStoreAuthority(t,
		ctx, st, run.ID, root, "docker-plan-incomplete-children")
	incomplete, incompleteOperation := newDockerContainerPlanStoreRecord(t, ctx,
		incompleteObservation, incompleteManifest, "docker-plan-incomplete-children")
	tx, err = st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerContainerPlanTx(ctx, tx, incomplete); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for _, control := range incomplete.Controls[:len(incomplete.Controls)-1] {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_docker_container_plan_controls
			(plan_id, ordinal, name, state, control_digest, planned, applied, verified)
			VALUES (?, ?, ?, ?, ?, 1, 0, 0)`, incomplete.ID, control.Ordinal, control.Name,
			control.State, control.ControlDigest); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	for _, step := range incomplete.Transaction.Steps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_docker_container_plan_steps
			(plan_id, ordinal, name, state, step_digest, simulated, production_applied)
			VALUES (?, ?, ?, ?, ?, 1, 0)`, incomplete.ID, step.Ordinal, step.Name,
			step.State, step.StepDigest); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_docker_container_plan_operations
		(operation_key_digest, request_fingerprint, plan_id, observation_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, incompleteOperation.KeyDigest,
		incompleteOperation.RequestFingerprint, incompleteOperation.PlanID,
		incompleteOperation.ObservationID, incompleteOperation.RunID,
		incompleteOperation.RequestedBy, ts(incompleteOperation.CreatedAt)); err == nil ||
		!strings.Contains(err.Error(), "operation binding is invalid") {
		_ = tx.Rollback()
		t.Fatalf("SQLite accepted an incomplete Docker plan control set: %v", err)
	}
	_ = tx.Rollback()

	for _, table := range []string{"sandbox_docker_container_plans",
		"sandbox_docker_container_plan_controls", "sandbox_docker_container_plan_steps",
		"sandbox_docker_container_plan_operations"} {
		rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue,
				&primaryKey); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			switch name {
			case "executable", "arguments_json", "working_directory", "mount_source",
				"mount_target", "network_target", "environment_value", "secret_reference",
				"secret_path", "container_name", "label_name", "label_value", "container_id",
				"lease_id", "owner_id", "manifest_json":
				_ = rows.Close()
				t.Fatalf("schema v54 persists private data in %s.%s", table, name)
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
	found := false
	for _, event := range timeline {
		if event.Type != events.SandboxDockerContainerPlanRecordedEvent {
			continue
		}
		found = true
		for _, private := range []string{root, "/workspace", "/output/report.json",
			"private-build-command",
			observation.Report.ImageDigest, stored.SpecFingerprint,
			stored.Transaction.TransactionFingerprint} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("schema v54 event leaked private data %q: %#v", private, event)
			}
		}
	}
	if !found {
		t.Fatal("Docker container plan event was not recorded")
	}
	var artifacts int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_artifacts
		WHERE run_id = ?`, run.ID).Scan(&artifacts); err != nil || artifacts != 0 {
		t.Fatalf("Docker fake write plan created production Artifacts: count=%d err=%v", artifacts, err)
	}
}

func TestDockerContainerPlanConcurrentReplayConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-plan-concurrent.db")
	st1, run, root := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = st1.Close() })
	_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st1,
		run.ID, root, "docker-plan-concurrent")
	first, firstOperation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
		"docker-plan-concurrent-operation")
	second := first
	second.ID = idgen.New("sandbox-docker-plan")
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	secondOperation := firstOperation
	secondOperation.PlanID = second.ID
	secondOperation.CreatedAt = second.CreatedAt
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	stores := []*SQLiteStore{st1, st2}
	plans := []sandbox.DockerContainerPlan{first, second}
	operations := []sandbox.DockerContainerPlanOperation{firstOperation, secondOperation}
	results := make([]sandbox.DockerContainerPlan, 2)
	replayed := make([]bool, 2)
	errorsFound := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], replayed[index], errorsFound[index] = stores[index].CreateDockerContainerPlan(
				ctx, plans[index], operations[index])
		}(index)
	}
	close(start)
	group.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || results[0].ID != results[1].ID ||
		(results[0].ID != first.ID && results[0].ID != second.ID) || replayed[0] == replayed[1] {
		t.Fatalf("concurrent Docker plans diverged: results=%#v replayed=%v errors=%v",
			results, replayed, errorsFound)
	}
	listed, err := st1.ListDockerContainerPlans(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != results[0].ID {
		t.Fatalf("concurrent Docker plans did not converge: %#v err=%v", listed, err)
	}
}

func TestDockerContainerPlanLimitCancellationAndSchemaV53Upgrade(t *testing.T) {
	for _, historical := range []bool{false, true} {
		name := "current"
		if historical {
			name = "historical_upgrade"
		}
		t.Run(name, func(t *testing.T) {
			var historicalVersion []int
			if historical {
				historicalVersion = []int{177}
			}
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "docker-plan-v53.db")
			seedPath := path
			if historical {
				seedPath = filepath.Join(t.TempDir(), "seed.db")
			}
			st, run, root := openSandboxManifestStoreAt(t, ctx, seedPath, historicalVersion...)
			restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
			t.Cleanup(func() { _ = st.Close() })
			service, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st,
				run.ID, root, "docker-plan-limit")
			plan, operation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
				"docker-plan-limit-operation")
			if _, _, err := st.CreateDockerContainerPlan(ctx, plan, operation); err != nil {
				t.Fatal(err)
			}
			second, secondOperation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
				"docker-plan-second-operation")
			if _, _, err := st.CreateDockerContainerPlan(ctx, second, secondOperation); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
				t.Fatalf("Docker plan per-observation limit error=%v code=%s", err, apperror.CodeOf(err))
			}

			_, manifest2, observation2 := createDockerContainerPlanStoreAuthority(t, ctx, st,
				run.ID, root, "docker-plan-cancelled")
			cancelled, _ := newDockerContainerPlanStoreRecord(t, ctx, observation2, manifest2,
				"docker-plan-cancelled-operation")
			if _, err := service.CancelDisabledExecution(ctx, application.CancelSandboxExecutionRequest{
				ExecutionID: observation2.ExecutionID, OperationKey: "docker-plan-cancelled-request",
				RequestedBy: observation2.RequestedBy,
			}); err != nil {
				t.Fatal(err)
			}
			tx, err := st.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := insertDockerContainerPlanTx(ctx, tx, cancelled); err == nil ||
				!strings.Contains(err.Error(), "authority binding is invalid") {
				_ = tx.Rollback()
				t.Fatalf("SQLite accepted a Docker plan after cancellation: %v", err)
			}
			_ = tx.Rollback()

			if !historical {
				return
			}

			restoreLegacyContext()
			st = historicalTestDatabaseFromSeed(t, st, path, 53)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
				t.Fatalf("schema v53 did not upgrade to v54: version=%d err=%v", version, err)
			}
			loaded, err := st.GetDockerObservation(ctx, observation.ID)
			if err != nil || loaded.ID != observation.ID {
				t.Fatalf("schema v53 observation was not preserved: %#v err=%v", loaded, err)
			}
		})
	}
}

func createDockerContainerPlanStoreAuthority(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, root, prefix string,
) (*application.SandboxManifestService, sandbox.Manifest, sandbox.DockerObservation) {
	t.Helper()
	for _, name := range []string{"src", "output"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	service := newSandboxFixtureService(t, st)
	manifest := sandboxStoreTestManifest()
	manifest.Backend = sandbox.BackendDocker
	manifest.Command.Executable = "private-build-command"
	manifest.Mounts = []sandbox.Mount{
		{Source: "src", Target: "/workspace", Access: sandbox.MountReadOnly},
		{Source: "output", Target: "/output", Access: sandbox.MountReadWrite},
	}
	manifest.Output.Paths = []string{"/output/report.json"}
	requestedBy := "store_docker_plan_operator"
	prepared, err := service.Prepare(ctx, application.PrepareSandboxManifestRequest{
		RunID: runID, Manifest: manifest, OperationKey: prefix + "-prepare",
		RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.RequestApproval(ctx, prepared.Preparation.ID, requestedBy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewApproval(ctx, prepared.Preparation.ID, approval.ActionApprove,
		prefix+"-review", requestedBy, ""); err != nil {
		t.Fatal(err)
	}
	validated, err := service.ValidateExecutionCandidate(ctx,
		application.ValidateSandboxExecutionCandidateRequest{PreparationID: prepared.Preparation.ID,
			Manifest: manifest, ApprovalID: record.ID, OperationKey: prefix + "-candidate",
			RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := service.BeginDisabledExecution(ctx, application.BeginSandboxExecutionRequest{
		CandidateID: validated.Candidate.ID, Manifest: manifest,
		OperationKey: prefix + "-begin", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.PrepareDisabledPreflight(ctx,
		application.PrepareSandboxPreflightRequest{ExecutionID: lifecycle.Execution.ID,
			Manifest: manifest, OperationKey: prefix + "-preflight", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := "sha256:" + strings.Repeat("9", 64)
	evidence, err := service.RecordSimulatedBackendEvidence(ctx,
		application.RecordSandboxBackendEvidenceRequest{PreflightID: preflight.ID,
			Manifest: manifest, ImageDigest: imageDigest, OperationKey: prefix + "-evidence",
			RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	fixture := sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion,
		Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout, FileType: sandbox.OutputFileTypeStream, Content: "stdout"},
			{Kind: sandbox.OutputKindStderr, FileType: sandbox.OutputFileTypeStream, Content: "stderr"},
			{Kind: sandbox.OutputKindFile, FileType: sandbox.OutputFileTypeRegular, Content: "{}"},
		}}
	simulation, err := service.SimulateOutputTransaction(ctx,
		application.SimulateSandboxOutputRequest{EvidenceID: evidence.ID, Manifest: manifest,
			Fixture: fixture, OperationKey: prefix + "-simulation", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	service.WithDockerProductionObserver(sandbox.NewReadOnlyDockerProductionObserver(
		dockerObservationStoreTransport{imageDigest: imageDigest}))
	observation, err := service.ObserveDockerBackend(ctx, application.ObserveDockerBackendRequest{
		EvidenceID: evidence.ID, OutputSimulationID: simulation.ID, Manifest: manifest,
		OperationKey: prefix + "-observation", RequestedBy: requestedBy})
	if err != nil {
		t.Fatal(err)
	}
	return service, manifest, observation
}

func newDockerContainerPlanStoreRecord(t *testing.T, ctx context.Context,
	observation sandbox.DockerObservation, manifest sandbox.Manifest, operationKey string,
) (sandbox.DockerContainerPlan, sandbox.DockerContainerPlanOperation) {
	t.Helper()
	spec, err := sandbox.CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := sandbox.NewInMemoryDockerWriteTransaction().Simulate(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan, err := sandbox.NewDockerContainerPlan(idgen.New("sandbox-docker-plan"), observation,
		spec, transaction, observation.RequestedBy, now)
	if err != nil {
		t.Fatal(err)
	}
	operation := sandbox.DockerContainerPlanOperation{
		KeyDigest: runmutation.Fingerprint("docker_container_plan_store_test.v1", operationKey),
		PlanID:    plan.ID, ObservationID: observation.ID, RunID: observation.RunID,
		RequestedBy: observation.RequestedBy, CreatedAt: now,
	}
	operation.RequestFingerprint = sandbox.DockerContainerPlanRequestFingerprint(plan)
	return plan, operation
}
