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
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerContainerRehearsalLedgerIsImmutablePrivateAndNonExecuting(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st,
		run.ID, root, "docker-rehearsal-ledger")
	plan, planOperation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
		"docker-rehearsal-ledger-plan")
	if _, _, err := st.CreateDockerContainerPlan(ctx, plan, planOperation); err != nil {
		t.Fatal(err)
	}
	rehearsal, operation := newDockerContainerRehearsalStoreRecord(t, ctx, root, plan,
		observation, manifest, "docker-rehearsal-ledger-operation")
	stored, replayed, err := st.CreateDockerContainerRehearsal(ctx, rehearsal, operation)
	if err != nil || replayed || stored.ID != rehearsal.ID {
		t.Fatalf("create Docker rehearsal: value=%#v replayed=%t err=%v", stored, replayed, err)
	}
	if stored.ProductionExecutionSubmitted || stored.ProductionVerified ||
		stored.BackendEnabled || stored.ExecutionAuthorized || stored.ArtifactCommitAuthorized ||
		!stored.ContainerNeverStarted || !stored.ProcessNeverExecuted ||
		stored.DaemonWriteCount != 2 {
		t.Fatalf("Docker rehearsal gained execution authority: %#v", stored)
	}
	loaded, err := st.GetDockerContainerRehearsal(ctx, stored.ID)
	if err != nil || loaded.RehearsalFingerprint != stored.RehearsalFingerprint ||
		len(loaded.Result.Steps) != sandbox.MaxDockerContainerWriteSteps {
		t.Fatalf("load Docker rehearsal: value=%#v err=%v", loaded, err)
	}
	listed, err := st.ListDockerContainerRehearsals(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != stored.ID {
		t.Fatalf("list Docker rehearsals: values=%#v err=%v", listed, err)
	}
	replayedValue, replayed, err := st.CreateDockerContainerRehearsal(ctx, rehearsal, operation)
	if err != nil || !replayed || replayedValue.ID != stored.ID {
		t.Fatalf("replay Docker rehearsal: value=%#v replayed=%t err=%v",
			replayedValue, replayed, err)
	}

	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_container_rehearsals
		SET process_never_executed = 0 WHERE id = ?`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Docker rehearsal root was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_container_rehearsal_steps
		WHERE rehearsal_id = ? AND ordinal = 1`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Docker rehearsal step was deletable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_container_rehearsal_operations
		WHERE rehearsal_id = ?`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Docker rehearsal operation was deletable: %v", err)
	}
	tampered := rehearsal
	tampered.ProcessNeverExecuted = false
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerContainerRehearsalTx(ctx, tx, tampered); err == nil {
		_ = tx.Rollback()
		t.Fatal("direct SQL accepted a Docker rehearsal execution claim")
	}
	_ = tx.Rollback()

	for _, table := range []string{"sandbox_docker_container_rehearsals",
		"sandbox_docker_container_rehearsal_steps",
		"sandbox_docker_container_rehearsal_operations"} {
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
			case "container_id", "host_path", "mount_source", "mount_target", "executable",
				"arguments_json", "environment_value", "secret_reference", "manifest_json":
				_ = rows.Close()
				t.Fatalf("schema v55 persists private data in %s.%s", table, name)
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
		if event.Type != events.SandboxDockerRehearsalRecordedEvent {
			continue
		}
		found = true
		for _, private := range []string{root, "/workspace", "/output/report.json",
			"private-build-command", strings.Repeat("c", 64), plan.ImageDigest,
			stored.ContainerIDFingerprint} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("schema v55 event leaked private data %q: %#v", private, event)
			}
		}
	}
	if !found {
		t.Fatal("Docker container rehearsal event was not recorded")
	}
	var artifacts int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_artifacts
		WHERE run_id = ?`, run.ID).Scan(&artifacts); err != nil || artifacts != 0 {
		t.Fatalf("Docker rehearsal created production Artifacts: count=%d err=%v", artifacts, err)
	}
}

func TestDockerContainerRehearsalConcurrentReplayConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-rehearsal-concurrent.db")
	st1, run, root := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = st1.Close() })
	_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st1,
		run.ID, root, "docker-rehearsal-concurrent")
	plan, planOperation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
		"docker-rehearsal-concurrent-plan")
	if _, _, err := st1.CreateDockerContainerPlan(ctx, plan, planOperation); err != nil {
		t.Fatal(err)
	}
	first, firstOperation := newDockerContainerRehearsalStoreRecord(t, ctx, root, plan,
		observation, manifest, "docker-rehearsal-concurrent-operation")
	second := first
	second.ID = idgen.New("sandbox-docker-rehearsal")
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	secondOperation := firstOperation
	secondOperation.RehearsalID = second.ID
	secondOperation.CreatedAt = second.CreatedAt
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	stores := []*SQLiteStore{st1, st2}
	rehearsals := []sandbox.DockerContainerRehearsal{first, second}
	operations := []sandbox.DockerContainerRehearsalOperation{firstOperation, secondOperation}
	results := make([]sandbox.DockerContainerRehearsal, 2)
	replayed := make([]bool, 2)
	errorsFound := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], replayed[index], errorsFound[index] =
				stores[index].CreateDockerContainerRehearsal(ctx, rehearsals[index], operations[index])
		}(index)
	}
	close(start)
	group.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || results[0].ID != results[1].ID ||
		(results[0].ID != first.ID && results[0].ID != second.ID) || replayed[0] == replayed[1] {
		t.Fatalf("concurrent Docker rehearsals diverged: results=%#v replayed=%v errors=%v",
			results, replayed, errorsFound)
	}
	listed, err := st1.ListDockerContainerRehearsals(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != results[0].ID {
		t.Fatalf("concurrent Docker rehearsals did not converge: %#v err=%v", listed, err)
	}
}

func TestDockerContainerRehearsalLimitAndSchemaV54Upgrade(t *testing.T) {
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
			path := filepath.Join(t.TempDir(), "docker-rehearsal-v54.db")
			seedPath := path
			if historical {
				seedPath = filepath.Join(t.TempDir(), "seed.db")
			}
			st, run, root := openSandboxManifestStoreAt(t, ctx, seedPath, historicalVersion...)
			restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
			t.Cleanup(func() { _ = st.Close() })
			_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st,
				run.ID, root, "docker-rehearsal-limit")
			plan, planOperation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
				"docker-rehearsal-limit-plan")
			if _, _, err := st.CreateDockerContainerPlan(ctx, plan, planOperation); err != nil {
				t.Fatal(err)
			}
			first, firstOperation := newDockerContainerRehearsalStoreRecord(t, ctx, root, plan,
				observation, manifest, "docker-rehearsal-first")
			if _, _, err := st.CreateDockerContainerRehearsal(ctx, first, firstOperation); err != nil {
				t.Fatal(err)
			}
			second, secondOperation := newDockerContainerRehearsalStoreRecord(t, ctx, root, plan,
				observation, manifest, "docker-rehearsal-second")
			if _, _, err := st.CreateDockerContainerRehearsal(ctx, second,
				secondOperation); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
				t.Fatalf("Docker rehearsal per-plan limit error=%v code=%s", err, apperror.CodeOf(err))
			}

			if !historical {
				return
			}

			restoreLegacyContext()
			st = historicalTestDatabaseFromSeed(t, st, path, 54)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
				t.Fatalf("schema v54 did not upgrade to v55: version=%d err=%v", version, err)
			}
			loaded, err := st.GetDockerContainerPlan(ctx, plan.ID)
			if err != nil || loaded.ID != plan.ID {
				t.Fatalf("schema v54 plan was not preserved: %#v err=%v", loaded, err)
			}
		})
	}
}

func newDockerContainerRehearsalStoreRecord(t *testing.T, ctx context.Context, root string,
	plan sandbox.DockerContainerPlan, observation sandbox.DockerObservation,
	manifest sandbox.Manifest, operationKey string,
) (sandbox.DockerContainerRehearsal, sandbox.DockerContainerRehearsalOperation) {
	t.Helper()
	for _, name := range []string{"src", "output"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := sandbox.CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	request, err := sandbox.NewDockerContainerWriteRequest(ctx, root, spec)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	result, err := sandbox.NewDockerContainerWriteResult(endpoint, request,
		strings.Repeat("c", 64), 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rehearsal, err := sandbox.NewDockerContainerRehearsal(
		idgen.New("sandbox-docker-rehearsal"), plan, spec, result,
		plan.RequestedBy, now)
	if err != nil {
		t.Fatal(err)
	}
	operation := sandbox.DockerContainerRehearsalOperation{
		KeyDigest: runmutation.Fingerprint("docker_container_rehearsal_store_test.v1",
			operationKey),
		RehearsalID: rehearsal.ID, PlanID: plan.ID, RunID: plan.RunID,
		RequestedBy: plan.RequestedBy, CreatedAt: now,
	}
	operation.RequestFingerprint = sandbox.DockerContainerRehearsalRequestFingerprint(rehearsal)
	return rehearsal, operation
}
