package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerHostInputRequirementIsAtomicImmutableAndMetadataOnly(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	intent, plan, _, _ := newDockerContainerAttemptStoreIntent(t, ctx, st, run.ID, root,
		"docker-host-input-requirement")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	acquired, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_host_input_requirement_owner", time.Minute)
	if err != nil || acquired.Attempt.HostInputRequirement == nil ||
		acquired.Attempt.HostInputRequirement.Required ||
		acquired.Attempt.HostInputRequirement.OperatorConfirmed {
		t.Fatalf("begin attempt did not atomically persist requirement: %#v err=%v", acquired, err)
	}
	loaded, found, err := st.GetDockerHostInputRequirement(ctx, intent.ID)
	if err != nil || !found || loaded.RequirementFingerprint != requirement.RequirementFingerprint {
		t.Fatalf("load host input requirement by attempt: %#v found=%t err=%v",
			loaded, found, err)
	}
	byOperation, found, err := st.GetDockerHostInputRequirementByOperation(ctx,
		intent.OperationKeyDigest)
	if err != nil || !found || byOperation.AttemptID != intent.ID {
		t.Fatalf("load host input requirement by operation: %#v found=%t err=%v",
			byOperation, found, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_host_input_requirements
		SET required = 1, operator_confirmed = 1 WHERE attempt_id = ?`, intent.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("host input requirement was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_host_input_requirements
		WHERE attempt_id = ?`, intent.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("host input requirement was deletable: %v", err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundRequirement := false
	for _, event := range timeline {
		if event.Type == events.SandboxDockerHostInputRequirementEvent {
			foundRequirement = true
			if !strings.Contains(event.PayloadJSON, `"required":false`) ||
				!strings.Contains(event.PayloadJSON, `"before_daemon_stage":true`) {
				t.Fatalf("host input requirement event is incomplete: %#v", event)
			}
		}
		for _, private := range []string{root, "/workspace", intent.OperationKeyDigest} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("host input requirement event leaked private material %q: %#v",
					private, event)
			}
		}
	}
	if !foundRequirement {
		t.Fatal("host input requirement event was not recorded")
	}
	assertDockerHostInputRequirementSchemaPrivacy(t, ctx, st)
}

func TestSchemaV58RejectsNewAttemptWithoutRequirementBeforeStage(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	intent, _, _, request := newDockerContainerAttemptStoreIntent(t, ctx, st, run.ID, root,
		"docker-host-input-missing-requirement")
	now := time.Now().UTC()
	lease := sandbox.DockerContainerAttemptLease{
		AttemptID: intent.ID, LeaseID: newSandboxLeaseID(),
		OwnerID: "docker_host_input_missing_requirement_owner", Generation: 1,
		Status: sandbox.DockerContainerAttemptLeaseActive, AcquiredAt: now,
		ExpiresAt: now.Add(time.Minute),
	}
	if err := lease.Validate(); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerContainerAttemptIntentTx(ctx, tx, intent); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_docker_container_attempt_leases
		(attempt_id, lease_id, owner_id, generation, status, acquired_at, expires_at,
		released_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`, lease.AttemptID, lease.LeaseID,
		lease.OwnerID, lease.Generation, lease.Status, ts(lease.AcquiredAt),
		ts(lease.ExpiresAt)); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	stageResult, err := sandbox.NewDockerContainerStageResult(endpoint, request,
		strings.Repeat("c", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := sandbox.NewDockerContainerAttemptStage(intent.ID, lease.Generation,
		stageResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordDockerContainerAttemptStage(ctx, stage, lease); err == nil ||
		!strings.Contains(err.Error(), "requirement is not durable before stage") {
		t.Fatalf("schema v58 admitted a new attempt without a durable requirement: %v", err)
	}
}

func TestDockerHostInputRequirementGuardsCompletionBeforeStagingIntent(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	attempt, plan, spec, request, _ := newStagedDockerHostInputAttempt(t, ctx, st,
		run.ID, root, "docker-host-input-required-completion")
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	cleanupResult, err := sandbox.NewDockerContainerCleanupResult(endpoint, request,
		attempt.Stage.Result, true)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := sandbox.NewDockerContainerAttemptCleanup(attempt.Intent.ID,
		attempt.Lease.Generation, cleanupResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = st.RecordDockerContainerAttemptCleanup(ctx, cleanup, attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
	rehearsal, operation, completion := dockerAttemptCompletionFixture(t, plan, spec,
		request, attempt)
	if _, _, err := st.CompleteDockerContainerRehearsalAttempt(ctx, completion, rehearsal,
		operation, attempt.Lease); err == nil ||
		!strings.Contains(err.Error(), "Required Docker host input staging is incomplete") {
		t.Fatalf("schema v58 allowed required staging to disappear before its intent: %v", err)
	}
}

func TestDockerHostInputRequirementFalseRejectsLaterStaging(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	attempt, plan, _, _, manifest := newStagedDockerHostInputAttemptWithRequirement(t, ctx,
		st, run.ID, root, "docker-host-input-not-required", false)
	intent, err := sandbox.NewDockerHostInputStagingIntent(
		"sandbox-docker-host-input-intent-not-required", strings.Repeat("a", 64), attempt,
		plan, manifest, plan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PrepareDockerHostInputStagingIntent(ctx, intent,
		attempt.Lease); err == nil ||
		!strings.Contains(err.Error(), "conflicts with the durable requirement") {
		t.Fatalf("schema v58 allowed a false requirement to widen after stage: %v", err)
	}
}

func TestDockerHostInputRequirementIndependentCandidateIDsConverge(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	firstIntent, plan, _, request := newDockerContainerAttemptStoreIntent(t, ctx, st,
		run.ID, root, "docker-host-input-candidate-convergence")
	firstRequirement := newDockerContainerAttemptRequirement(t, firstIntent, plan, false)
	first, err := st.BeginDockerContainerRehearsalAttempt(ctx, firstIntent,
		firstRequirement, "docker_host_input_candidate_one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	failure, err := sandbox.NewDockerContainerAttemptFailure(firstIntent.ID, 1,
		first.Attempt.Lease.Generation, sandbox.DockerContainerAttemptFailureStage,
		sandbox.DockerContainerAttemptFailureCheckpoint, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailDockerContainerRehearsalAttempt(ctx, failure,
		first.Attempt.Lease); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	secondIntent, err := sandbox.NewDockerContainerAttemptIntent(
		idgen.New("sandbox-docker-attempt"), firstIntent.OperationKeyDigest, plan, request,
		endpoint, plan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if secondIntent.ID == firstIntent.ID ||
		secondIntent.IntentFingerprint != firstIntent.IntentFingerprint {
		t.Fatalf("test candidates do not isolate row identity: first=%#v second=%#v",
			firstIntent, secondIntent)
	}
	secondRequirement := newDockerContainerAttemptRequirement(t, secondIntent, plan, false)
	if secondRequirement.AttemptID == firstRequirement.AttemptID ||
		secondRequirement.RequirementFingerprint != firstRequirement.RequirementFingerprint {
		t.Fatalf("requirement semantic fingerprint depends on candidate identity: first=%#v second=%#v",
			firstRequirement, secondRequirement)
	}
	second, err := st.BeginDockerContainerRehearsalAttempt(ctx, secondIntent,
		secondRequirement, "docker_host_input_candidate_two", time.Minute)
	if err != nil || second.Attempt.Intent.ID != firstIntent.ID ||
		second.Attempt.HostInputRequirement == nil ||
		second.Attempt.HostInputRequirement.AttemptID != firstIntent.ID ||
		second.Attempt.Lease.Generation != 2 {
		t.Fatalf("independent host input requirement candidates did not converge: %#v err=%v",
			second, err)
	}
}

func TestSchemaV58UpgradePreservesV57AttemptWithoutFabricatingRequirement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-host-input-v57.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	intent, plan, _, _ := newDockerContainerAttemptStoreIntent(t, ctx, st, run.ID, root,
		"docker-host-input-v58-upgrade")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	if _, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_host_input_upgrade_owner", time.Minute); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, path, 57)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v57 did not upgrade to v58: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetDockerContainerRehearsalAttempt(ctx, intent.ID)
	if err != nil || loaded.Intent.ID != intent.ID || loaded.HostInputRequirement != nil {
		t.Fatalf("schema v58 changed the legacy attempt choice: %#v err=%v", loaded, err)
	}
	var legacyCount int
	if err := upgraded.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sandbox_docker_host_input_requirement_legacy_attempts
		WHERE attempt_id = ?`, intent.ID).Scan(&legacyCount); err != nil || legacyCount != 1 {
		t.Fatalf("schema v58 did not mark the preexisting attempt as legacy: count=%d err=%v",
			legacyCount, err)
	}
	if _, err := upgraded.db.ExecContext(ctx,
		`DELETE FROM sandbox_docker_host_input_requirement_legacy_attempts
		WHERE attempt_id = ?`, intent.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("schema v58 legacy marker was mutable: %v", err)
	}
	tx, err := upgraded.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerHostInputRequirementTx(ctx, tx, requirement, intent); err == nil ||
		!strings.Contains(err.Error(), "authority mismatch") {
		_ = tx.Rollback()
		t.Fatalf("schema v58 fabricated a requirement for a legacy attempt: %v", err)
	}
	_ = tx.Rollback()
}

func assertDockerHostInputRequirementSchemaPrivacy(t *testing.T, ctx context.Context,
	st *SQLiteStore,
) {
	t.Helper()
	rows, err := st.db.QueryContext(ctx,
		`PRAGMA table_info(sandbox_docker_host_input_requirements)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue,
			&primaryKey); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "host_path", "mount_source", "mount_target", "container_id", "socket_path",
			"manifest_json", "content", "descriptor", "lease_id", "lease_owner",
			"operation_key":
			t.Fatalf("schema v58 persists private host input material in %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
