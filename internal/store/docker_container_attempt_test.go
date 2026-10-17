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

func TestDockerContainerAttemptStagesCleansAndCompletesAtomically(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	intent, plan, spec, request := newDockerContainerAttemptStoreIntent(t, ctx, st,
		run.ID, root, "docker-attempt-complete")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	acquired, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_attempt_owner", time.Minute)
	if err != nil || acquired.Replayed || acquired.TookOver ||
		acquired.Attempt.Status != sandbox.DockerContainerAttemptStatusPrepared ||
		acquired.Attempt.Lease.Generation != 1 {
		t.Fatalf("begin Docker attempt: value=%#v err=%v", acquired, err)
	}
	if _, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"other_docker_attempt_owner", time.Minute); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("active Docker attempt lease was not exclusive: %v", err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	stageResult, err := sandbox.NewDockerContainerStageResult(endpoint, request,
		strings.Repeat("c", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := sandbox.NewDockerContainerAttemptStage(intent.ID,
		acquired.Attempt.Lease.Generation, stageResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	staged, replayed, err := st.RecordDockerContainerAttemptStage(ctx, stage,
		acquired.Attempt.Lease)
	if err != nil || replayed || staged.Stage == nil || len(staged.Stage.Result.Controls) != 19 ||
		staged.Status != sandbox.DockerContainerAttemptStatusStaged {
		t.Fatalf("record Docker attempt stage: value=%#v replayed=%t err=%v",
			staged, replayed, err)
	}
	cleanupResult, err := sandbox.NewDockerContainerCleanupResult(endpoint, request,
		stageResult, true)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := sandbox.NewDockerContainerAttemptCleanup(intent.ID,
		staged.Lease.Generation, cleanupResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cleaned, replayed, err := st.RecordDockerContainerAttemptCleanup(ctx, cleanup,
		staged.Lease)
	if err != nil || replayed || cleaned.Cleanup == nil ||
		cleaned.Status != sandbox.DockerContainerAttemptStatusCleaned {
		t.Fatalf("record Docker attempt cleanup: value=%#v replayed=%t err=%v",
			cleaned, replayed, err)
	}
	result, err := sandbox.NewDockerContainerWriteResultFromRecovery(endpoint, request,
		stageResult, cleanupResult)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rehearsal, err := sandbox.NewDockerContainerRehearsal(
		idgen.New("sandbox-docker-rehearsal"), plan, spec, result, plan.RequestedBy, now)
	if err != nil {
		t.Fatal(err)
	}
	operation := sandbox.DockerContainerRehearsalOperation{
		KeyDigest: intent.OperationKeyDigest, RehearsalID: rehearsal.ID,
		PlanID: plan.ID, RunID: plan.RunID, RequestedBy: plan.RequestedBy, CreatedAt: now,
	}
	operation.RequestFingerprint = sandbox.DockerContainerRehearsalRequestFingerprint(rehearsal)
	completion, err := sandbox.NewDockerContainerAttemptCompletion(intent.ID, rehearsal.ID,
		cleaned.Lease.Generation, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, replayed, err := st.CompleteDockerContainerRehearsalAttempt(ctx, completion,
		rehearsal, operation, cleaned.Lease)
	if err != nil || replayed || stored.ID != rehearsal.ID {
		t.Fatalf("complete Docker attempt: value=%#v replayed=%t err=%v",
			stored, replayed, err)
	}
	loaded, err := st.GetDockerContainerRehearsalAttempt(ctx, intent.ID)
	if err != nil || loaded.Status != sandbox.DockerContainerAttemptStatusCompleted ||
		loaded.Completion == nil || loaded.Completion.RehearsalID != rehearsal.ID ||
		loaded.Lease.Status != sandbox.DockerContainerAttemptLeaseReleased ||
		loaded.Stage.Result.ContainerStarted || loaded.Stage.Result.ProcessExecuted ||
		loaded.Stage.Result.ExecutionAuthorized {
		t.Fatalf("load completed Docker attempt: value=%#v err=%v", loaded, err)
	}
	listed, err := st.ListDockerContainerRehearsalAttempts(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Intent.ID != intent.ID {
		t.Fatalf("list Docker attempts: values=%#v err=%v", listed, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_container_attempt_stages
		SET process_never_executed = 0 WHERE attempt_id = ?`, intent.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Docker attempt stage was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_container_attempt_controls
		WHERE attempt_id = ? AND ordinal = 1`, intent.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Docker attempt control was deletable: %v", err)
	}

	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPrepared, foundStaged, foundCleanup, foundCompleted := false, false, false, false
	for _, event := range timeline {
		switch event.Type {
		case events.SandboxDockerAttemptPreparedEvent:
			foundPrepared = true
		case events.SandboxDockerAttemptStagedEvent:
			foundStaged = true
		case events.SandboxDockerAttemptCleanupEvent:
			foundCleanup = true
		case events.SandboxDockerAttemptCompletedEvent:
			foundCompleted = true
		}
		for _, private := range []string{root, "/workspace", strings.Repeat("c", 64),
			plan.ImageDigest, stageResult.ContainerIDFingerprint} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("Docker attempt event leaked private material %q: %#v", private, event)
			}
		}
	}
	if !foundPrepared || !foundStaged || !foundCleanup || !foundCompleted {
		t.Fatalf("Docker attempt timeline is incomplete: prepared=%t staged=%t cleanup=%t completed=%t",
			foundPrepared, foundStaged, foundCleanup, foundCompleted)
	}
}

func TestDockerContainerAttemptFailureReleaseAndExpiredTakeoverAreFenced(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	intent, plan, _, request := newDockerContainerAttemptStoreIntent(t, ctx, st,
		run.ID, root, "docker-attempt-failure")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	first, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_attempt_owner_one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO sandbox_docker_container_attempt_failures
		(attempt_id, ordinal, lease_generation, phase, code, retryable,
		failure_fingerprint, created_at) VALUES (?, 1, ?, 'stage', 'secret_value', 0, ?, ?)`,
		intent.ID, first.Attempt.Lease.Generation, strings.Repeat("a", 64),
		ts(time.Now().UTC())); err == nil {
		t.Fatal("schema v56 accepted an unbounded Docker attempt failure code")
	}
	failure, err := sandbox.NewDockerContainerAttemptFailure(intent.ID, 1,
		first.Attempt.Lease.Generation, sandbox.DockerContainerAttemptFailureStage,
		sandbox.DockerContainerWriteFailureConnection, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	failed, err := st.FailDockerContainerRehearsalAttempt(ctx, failure, first.Attempt.Lease)
	if err != nil || failed.Lease.Status != sandbox.DockerContainerAttemptLeaseReleased ||
		len(failed.Failures) != 1 {
		t.Fatalf("record Docker attempt failure: value=%#v err=%v", failed, err)
	}
	second, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_attempt_owner_two", sandbox.MinDockerContainerAttemptLeaseTTL)
	if err != nil || second.TookOver || second.Attempt.Lease.Generation != 2 {
		t.Fatalf("reacquire released Docker attempt: value=%#v err=%v", second, err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	stageResult, err := sandbox.NewDockerContainerStageResult(endpoint, request,
		strings.Repeat("c", 64), true)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := sandbox.NewDockerContainerAttemptStage(intent.ID,
		second.Attempt.Lease.Generation, stageResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordDockerContainerAttemptStage(ctx, stage,
		second.Attempt.Lease); err != nil {
		t.Fatal(err)
	}
	time.Sleep(sandbox.MinDockerContainerAttemptLeaseTTL + 100*time.Millisecond)
	third, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_attempt_owner_three", time.Minute)
	if err != nil || !third.TookOver || third.Attempt.Lease.Generation != 3 {
		t.Fatalf("take over expired Docker attempt: value=%#v err=%v", third, err)
	}
	requestPlan := third.Attempt.Intent
	if requestPlan.IntentFingerprint != intent.IntentFingerprint {
		t.Fatal("Docker attempt takeover changed immutable intent")
	}
	if _, _, err := st.RecordDockerContainerAttemptStage(ctx, stage,
		second.Attempt.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale generation replay borrowed the takeover lease: %v", err)
	}
}

func TestDockerContainerAttemptFailureLedgerExhaustionBlocksFurtherAcquisition(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	intent, plan, _, _ := newDockerContainerAttemptStoreIntent(t, ctx, st,
		run.ID, root, "docker-attempt-failure-limit")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	acquisition, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		idgen.New("docker-attempt-owner"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for ordinal := 1; ordinal <= sandbox.MaxDockerContainerAttemptFailures; ordinal++ {
		failure, err := sandbox.NewDockerContainerAttemptFailure(intent.ID, ordinal,
			acquisition.Attempt.Lease.Generation,
			sandbox.DockerContainerAttemptFailureStage,
			sandbox.DockerContainerAttemptFailureCheckpoint, true, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		failed, err := st.FailDockerContainerRehearsalAttempt(ctx, failure,
			acquisition.Attempt.Lease)
		if err != nil || len(failed.Failures) != ordinal ||
			failed.Lease.Status != sandbox.DockerContainerAttemptLeaseReleased {
			t.Fatalf("record Docker attempt failure %d: value=%#v err=%v",
				ordinal, failed, err)
		}
		if ordinal == sandbox.MaxDockerContainerAttemptFailures {
			break
		}
		acquisition, err = st.AcquireDockerContainerRehearsalAttempt(ctx, intent.ID,
			intent.RequestedBy, idgen.New("docker-attempt-owner"), time.Minute)
		if err != nil {
			t.Fatalf("reacquire Docker attempt after failure %d: %v", ordinal, err)
		}
	}
	if _, err := st.AcquireDockerContainerRehearsalAttempt(ctx, intent.ID,
		intent.RequestedBy, idgen.New("docker-attempt-owner"), time.Minute); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("exhausted Docker attempt admitted another generation: %v", err)
	}
	loaded, err := st.GetDockerContainerRehearsalAttempt(ctx, intent.ID)
	if err != nil || len(loaded.Failures) != sandbox.MaxDockerContainerAttemptFailures ||
		loaded.Lease.Status != sandbox.DockerContainerAttemptLeaseReleased {
		t.Fatalf("exhausted Docker attempt changed after denied acquire: value=%#v err=%v",
			loaded, err)
	}
}

func TestDockerContainerAttemptConcurrentStoresAdmitOneLease(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-attempt-concurrent.db")
	firstStore, run, root := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = firstStore.Close() })
	intent, plan, _, _ := newDockerContainerAttemptStoreIntent(t, ctx, firstStore,
		run.ID, root, "docker-attempt-concurrent")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	secondStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	stores := []*SQLiteStore{firstStore, secondStore}
	owners := []string{"docker_attempt_owner_one", "docker_attempt_owner_two"}
	results := make([]sandbox.DockerContainerAttemptAcquisition, 2)
	errorsFound := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], errorsFound[index] = stores[index].BeginDockerContainerRehearsalAttempt(
				ctx, intent, requirement, owners[index], time.Minute)
		}(index)
	}
	close(start)
	group.Wait()
	successes, conflicts := 0, 0
	for index, err := range errorsFound {
		if err == nil {
			successes++
			if results[index].Attempt.Lease.Generation != 1 {
				t.Fatalf("first Docker attempt lease generation=%d",
					results[index].Attempt.Lease.Generation)
			}
			continue
		}
		if apperror.CodeOf(err) == apperror.CodeConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent Docker attempt returned unexpected error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent Docker attempts did not admit exactly one lease: results=%#v errors=%v",
			results, errorsFound)
	}
	listed, err := firstStore.ListDockerContainerRehearsalAttempts(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Intent.ID != intent.ID ||
		listed[0].Lease.Status != sandbox.DockerContainerAttemptLeaseActive {
		t.Fatalf("concurrent Docker attempt ledger diverged: values=%#v err=%v", listed, err)
	}
}

func TestDockerContainerAttemptSchemaStoresNoRawExecutionMaterial(t *testing.T) {
	ctx := context.Background()
	st, _, _ := openSandboxManifestStore(t, ctx)
	var controlSchema string
	if err := st.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 'sandbox_docker_container_attempt_controls'`).
		Scan(&controlSchema); err != nil {
		t.Fatal(err)
	}
	for _, invariant := range []string{
		"ordinal = 1 AND name = 'image_digest_exact'",
		"ordinal = 11 AND name = 'mount_configuration_exact_private'",
		"ordinal = 19 AND name = 'container_never_started'",
		"execution_evidence = 0",
	} {
		if !strings.Contains(controlSchema, invariant) {
			t.Fatalf("schema v56 control matrix lacks invariant %q: %s",
				invariant, controlSchema)
		}
	}
	for trigger, invariant := range map[string]string{
		"trg_sandbox_docker_container_attempt_lease_update":      "julianday(NEW.acquired_at) >= julianday(OLD.released_at)",
		"trg_sandbox_docker_container_attempt_failure_insert":    "julianday(lease.expires_at) > julianday('now')",
		"trg_sandbox_docker_container_attempt_completion_insert": "julianday(lease.expires_at) > julianday('now')",
	} {
		var triggerSchema string
		if err := st.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
			WHERE type = 'trigger' AND name = ?`, trigger).Scan(&triggerSchema); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(triggerSchema, invariant) {
			t.Fatalf("schema v56 trigger %s lacks invariant %q: %s",
				trigger, invariant, triggerSchema)
		}
	}
	for _, table := range []string{
		"sandbox_docker_container_rehearsal_attempts",
		"sandbox_docker_container_attempt_stages",
		"sandbox_docker_container_attempt_controls",
		"sandbox_docker_container_attempt_cleanups",
		"sandbox_docker_container_attempt_failures",
		"sandbox_docker_container_attempt_completions",
	} {
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
			case "container_id", "host_path", "mount_source", "mount_target", "socket_path",
				"executable", "arguments_json", "environment_value", "secret_reference",
				"manifest_json", "spec_json":
				_ = rows.Close()
				t.Fatalf("schema v56 persists private execution material in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchemaV56UpgradePreservesV55Rehearsal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-attempt-v55.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st,
		run.ID, root, "docker-attempt-upgrade")
	plan, planOperation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
		"docker-attempt-upgrade-plan")
	if _, _, err := st.CreateDockerContainerPlan(ctx, plan, planOperation); err != nil {
		t.Fatal(err)
	}
	rehearsal, operation := newDockerContainerRehearsalStoreRecord(t, ctx, root, plan,
		observation, manifest, "docker-attempt-upgrade-rehearsal")
	if _, _, err := st.CreateDockerContainerRehearsal(ctx, rehearsal, operation); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, path, 55)
	if version, err := st.SchemaVersion(ctx); err != nil || version != 55 {
		t.Fatalf("fixture schema=%d want=55 err=%v", version, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v55 did not upgrade to v56: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetDockerContainerRehearsal(ctx, rehearsal.ID)
	if err != nil || loaded.ID != rehearsal.ID {
		t.Fatalf("schema v55 rehearsal was not preserved: value=%#v err=%v", loaded, err)
	}
	attempts, err := upgraded.ListDockerContainerRehearsalAttempts(ctx, run.ID, 10)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("schema v56 fabricated attempts for historical rehearsals: %#v err=%v",
			attempts, err)
	}
}

func newDockerContainerAttemptStoreIntent(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, root, prefix string,
) (sandbox.DockerContainerAttemptIntent, sandbox.DockerContainerPlan,
	sandbox.DockerContainerSpec, sandbox.DockerContainerWriteRequest) {
	t.Helper()
	for _, name := range []string{"src", "output"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, manifest, observation := createDockerContainerPlanStoreAuthority(t, ctx, st,
		runID, root, prefix)
	plan, operation := newDockerContainerPlanStoreRecord(t, ctx, observation, manifest,
		prefix+"-plan")
	if _, _, err := st.CreateDockerContainerPlan(ctx, plan, operation); err != nil {
		t.Fatal(err)
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
	intent, err := sandbox.NewDockerContainerAttemptIntent(
		idgen.New("sandbox-docker-attempt"),
		runmutation.Fingerprint("sandbox_docker_container_rehearsal_operation.v1",
			plan.ID, prefix+"-operation"), plan, request, endpoint, plan.RequestedBy,
		time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return intent, plan, spec, request
}

func newDockerContainerAttemptRequirement(t *testing.T,
	intent sandbox.DockerContainerAttemptIntent, plan sandbox.DockerContainerPlan,
	required bool,
) sandbox.DockerHostInputRequirement {
	t.Helper()
	requirement, err := sandbox.NewDockerHostInputRequirement(intent, plan, required, required)
	if err != nil {
		t.Fatal(err)
	}
	return requirement
}
