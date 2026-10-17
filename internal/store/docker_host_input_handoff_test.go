package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerHostInputHandoffIsWriteAheadImmutableAndCompletionGated(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	attempt, plan, spec, writeRequest, manifest :=
		newStagedDockerHostInputAttemptWithRequirements(t, ctx, st, run.ID, root,
			"docker-host-input-handoff-ledger", true, true)
	staging := prepareDockerHostInputStagingForHandoff(t, ctx, st, attempt, plan, manifest, root)
	handoffDigest := runmutation.Fingerprint(
		"sandbox_docker_host_input_handoff_operation.v1", attempt.Intent.OperationKeyDigest)
	intent, err := sandbox.NewDockerHostInputHandoffIntent(
		idgen.New("sandbox-docker-host-input-handoff-intent"), handoffDigest,
		attempt, plan, staging, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err := st.PrepareDockerHostInputHandoffIntent(ctx, intent, attempt.Lease)
	if err != nil || replayed || record.Handoff != nil {
		t.Fatalf("prepare handoff intent: %#v replayed=%t err=%v", record, replayed, err)
	}
	if _, _, err := st.PrepareDockerHostInputHandoffIntent(ctx, intent,
		attempt.Lease); err != nil {
		t.Fatalf("replay handoff intent: %v", err)
	}

	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	cleanupResult, err := sandbox.NewDockerContainerCleanupResult(endpoint, writeRequest,
		attempt.Stage.Result, true)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := sandbox.NewDockerContainerAttemptCleanup(attempt.Intent.ID,
		attempt.Lease.Generation, cleanupResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// A cleanup cannot race ahead of a required write-ahead handoff intent.
	if _, _, err := st.RecordDockerContainerAttemptCleanup(ctx, cleanup,
		attempt.Lease); err == nil {
		t.Fatal("required handoff intent allowed cleanup before its result")
	}

	handoffRequest, err := sandbox.NewDockerHostInputHandoffRequest(intent, writeRequest,
		attempt.Stage.Result, staging.Staging.Report)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sandbox.NewDockerHostInputHandoffResult(endpoint, handoffRequest,
		strings.Repeat("d", 64), 8, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	value, err := sandbox.NewDockerHostInputHandoff(
		idgen.New("sandbox-docker-host-input-handoff"), intent,
		attempt.Lease.Generation, result, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err = st.RecordDockerHostInputHandoff(ctx, value, attempt.Lease)
	if err != nil || replayed || record.Handoff == nil {
		t.Fatalf("record handoff: %#v replayed=%t err=%v", record, replayed, err)
	}
	byAttempt, found, err := st.GetDockerHostInputHandoffByAttempt(ctx, attempt.Intent.ID)
	if err != nil || !found || byAttempt.Handoff == nil ||
		byAttempt.Handoff.HandoffFingerprint != value.HandoffFingerprint {
		t.Fatalf("load handoff by attempt: %#v found=%t err=%v", byAttempt, found, err)
	}
	listed, err := st.ListDockerHostInputHandoffs(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Intent.ID != intent.ID {
		t.Fatalf("list handoffs: %#v err=%v", listed, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_host_input_handoffs
		SET daemon_consumed = 0 WHERE id = ?`, value.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("handoff result was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_host_input_handoff_intents
		WHERE id = ?`, intent.ID); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("handoff intent was deletable: %v", err)
	}

	attempt, _, err = st.RecordDockerContainerAttemptCleanup(ctx, cleanup, attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
	rehearsal, operation, completion := dockerAttemptCompletionFixture(t, plan, spec,
		writeRequest, attempt)
	stored, replayed, err := st.CompleteDockerContainerRehearsalAttempt(ctx, completion,
		rehearsal, operation, attempt.Lease)
	if err != nil || replayed || stored.ID != rehearsal.ID {
		t.Fatalf("complete after handoff: %#v replayed=%t err=%v", stored, replayed, err)
	}

	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundRequirement, foundIntent, foundResult := false, false, false
	for _, event := range timeline {
		switch event.Type {
		case events.SandboxDockerHostInputHandoffRequirementEvent:
			foundRequirement = true
		case events.SandboxDockerHostInputHandoffIntentEvent:
			foundIntent = true
		case events.SandboxDockerHostInputHandoffEvent:
			foundResult = true
		}
		for _, private := range []string{root, "main.txt", "source payload"} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("handoff event leaked private material %q: %#v", private, event)
			}
		}
	}
	if !foundRequirement || !foundIntent || !foundResult {
		t.Fatalf("handoff timeline incomplete: requirement=%t intent=%t result=%t",
			foundRequirement, foundIntent, foundResult)
	}
}

func TestSchemaV59PreservesV58AttemptAsExplicitLegacy(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-host-input-v58.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	intent, plan, _, _ := newDockerContainerAttemptStoreIntent(t, ctx, st, run.ID, root,
		"docker-host-input-v59-upgrade")
	requirement := newDockerContainerAttemptRequirement(t, intent, plan, false)
	acquired, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_host_input_v59_upgrade", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 58)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	loaded, err := upgraded.GetDockerContainerRehearsalAttempt(ctx, acquired.Attempt.Intent.ID)
	if err != nil || loaded.HostInputRequirement == nil ||
		loaded.HostInputHandoffRequirement != nil {
		t.Fatalf("v59 changed the legacy attempt choice: %#v err=%v", loaded, err)
	}
	var count int
	if err := upgraded.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sandbox_docker_host_input_handoff_legacy_attempts
		WHERE attempt_id = ?`, acquired.Attempt.Intent.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("v59 did not mark legacy attempt: count=%d err=%v", count, err)
	}
}

func prepareDockerHostInputStagingForHandoff(t *testing.T, ctx context.Context,
	st *SQLiteStore, attempt sandbox.DockerContainerRehearsalAttempt,
	plan sandbox.DockerContainerPlan, manifest sandbox.Manifest, root string,
) sandbox.DockerHostInputStagingRecord {
	t.Helper()
	digest := runmutation.Fingerprint("sandbox_docker_host_input_staging_operation.v1",
		attempt.Intent.OperationKeyDigest)
	intent, err := sandbox.NewDockerHostInputStagingIntent(
		idgen.New("sandbox-docker-host-input-intent"), digest, attempt, plan, manifest,
		plan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PrepareDockerHostInputStagingIntent(ctx, intent,
		attempt.Lease); err != nil {
		t.Fatal(err)
	}
	report := hostInputBundleStoreReport(t, root, manifest)
	value, err := sandbox.NewDockerHostInputStaging(
		idgen.New("sandbox-docker-host-input-staging"), intent,
		attempt.Lease.Generation, report, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := st.RecordDockerHostInputStaging(ctx, value, attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
