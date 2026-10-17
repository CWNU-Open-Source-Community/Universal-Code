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

func TestDockerHostInputStagingGuardsCompletionAndStoresMetadataOnly(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	attempt, plan, spec, request, manifest := newStagedDockerHostInputAttempt(t, ctx, st,
		run.ID, root, "docker-host-input-ledger")
	operationDigest := runmutation.Fingerprint(
		"sandbox_docker_host_input_staging_operation.v1", attempt.Intent.OperationKeyDigest)
	intent, err := sandbox.NewDockerHostInputStagingIntent(
		idgen.New("sandbox-docker-host-input-intent"), operationDigest, attempt, plan,
		manifest, plan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err := st.PrepareDockerHostInputStagingIntent(ctx, intent, attempt.Lease)
	if err != nil || replayed || record.Staging != nil {
		t.Fatalf("prepare host input staging intent: %#v replayed=%t err=%v",
			record, replayed, err)
	}
	replayedRecord, replayed, err := st.PrepareDockerHostInputStagingIntent(ctx, intent,
		attempt.Lease)
	if err != nil || !replayed || replayedRecord.Intent.ID != intent.ID {
		t.Fatalf("replay host input staging intent: %#v replayed=%t err=%v",
			replayedRecord, replayed, err)
	}

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
	rehearsal, rehearsalOperation, completion := dockerAttemptCompletionFixture(t, plan, spec,
		request, attempt)
	if _, _, err := st.CompleteDockerContainerRehearsalAttempt(ctx, completion, rehearsal,
		rehearsalOperation, attempt.Lease); err == nil ||
		!strings.Contains(err.Error(), "host input staging is incomplete") {
		t.Fatalf("schema v57 allowed completion before host input evidence: %v", err)
	}

	report := hostInputBundleStoreReport(t, root, manifest)
	value, err := sandbox.NewDockerHostInputStaging(
		idgen.New("sandbox-docker-host-input-staging"), intent, attempt.Lease.Generation,
		report, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err = st.RecordDockerHostInputStaging(ctx, value, attempt.Lease)
	if err != nil || replayed || record.Staging == nil {
		t.Fatalf("record host input staging: %#v replayed=%t err=%v", record, replayed, err)
	}
	byAttempt, found, err := st.GetDockerHostInputStagingByAttempt(ctx, attempt.Intent.ID)
	if err != nil || !found || byAttempt.Staging == nil || byAttempt.Intent.ID != intent.ID {
		t.Fatalf("load host input staging by attempt: %#v found=%t err=%v", byAttempt, found, err)
	}
	byOperation, found, err := st.GetDockerHostInputStagingByOperation(ctx, operationDigest)
	if err != nil || !found || byOperation.Intent.ID != intent.ID {
		t.Fatalf("load host input staging by operation: %#v found=%t err=%v",
			byOperation, found, err)
	}
	byPlan, found, err := st.GetDockerHostInputStagingByPlan(ctx, plan.ID)
	if err != nil || !found || byPlan.Intent.ID != intent.ID {
		t.Fatalf("load host input staging by plan: %#v found=%t err=%v", byPlan, found, err)
	}
	listed, err := st.ListDockerHostInputStagings(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Intent.ID != intent.ID {
		t.Fatalf("list host input stagings: %#v err=%v", listed, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_host_input_stagings
		SET daemon_consumed = 1 WHERE id = ?`, value.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("host input staging was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_host_input_staging_intents
		WHERE id = ?`, intent.ID); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("host input staging intent was deletable: %v", err)
	}

	stored, replayed, err := st.CompleteDockerContainerRehearsalAttempt(ctx, completion,
		rehearsal, rehearsalOperation, attempt.Lease)
	if err != nil || replayed || stored.ID != rehearsal.ID {
		t.Fatalf("complete attempt after host input evidence: %#v replayed=%t err=%v",
			stored, replayed, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundIntent, foundStaged := false, false
	for _, event := range timeline {
		switch event.Type {
		case events.SandboxDockerHostInputIntentEvent:
			foundIntent = true
		case events.SandboxDockerHostInputStagedEvent:
			foundStaged = true
		}
		for _, private := range []string{root, "main.txt", "source payload"} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("host input staging event leaked private material %q: %#v", private, event)
			}
		}
	}
	if !foundIntent || !foundStaged {
		t.Fatalf("host input staging timeline is incomplete: intent=%t staged=%t",
			foundIntent, foundStaged)
	}
	assertDockerHostInputSchemaPrivacy(t, ctx, st)
}

func TestDockerHostInputStagingFencesStaleLeaseGeneration(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	attempt, plan, _, _, manifest := newStagedDockerHostInputAttempt(t, ctx, st,
		run.ID, root, "docker-host-input-fence")
	operationDigest := runmutation.Fingerprint(
		"sandbox_docker_host_input_staging_operation.v1", attempt.Intent.OperationKeyDigest)
	intent, err := sandbox.NewDockerHostInputStagingIntent(
		idgen.New("sandbox-docker-host-input-intent"), operationDigest, attempt, plan,
		manifest, plan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PrepareDockerHostInputStagingIntent(ctx, intent, attempt.Lease); err != nil {
		t.Fatal(err)
	}
	failure, err := sandbox.NewDockerContainerAttemptFailure(attempt.Intent.ID, 1,
		attempt.Lease.Generation, sandbox.DockerContainerAttemptFailureStage,
		sandbox.DockerContainerAttemptFailureCheckpoint, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailDockerContainerRehearsalAttempt(ctx, failure, attempt.Lease); err != nil {
		t.Fatal(err)
	}
	taken, err := st.AcquireDockerContainerRehearsalAttempt(ctx, attempt.Intent.ID,
		plan.RequestedBy, "docker_host_input_takeover", time.Minute)
	if err != nil || taken.Attempt.Lease.Generation != 2 {
		t.Fatalf("take over host input staging attempt: %#v err=%v", taken, err)
	}
	report := hostInputBundleStoreReport(t, root, manifest)
	staleValue, err := sandbox.NewDockerHostInputStaging(
		idgen.New("sandbox-docker-host-input-staging"), intent, attempt.Lease.Generation,
		report, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordDockerHostInputStaging(ctx, staleValue, attempt.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale host input staging lease committed: %v", err)
	}
	currentValue, err := sandbox.NewDockerHostInputStaging(
		idgen.New("sandbox-docker-host-input-staging"), intent,
		taken.Attempt.Lease.Generation, report, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err := st.RecordDockerHostInputStaging(ctx, currentValue,
		taken.Attempt.Lease)
	if err != nil || replayed || record.Staging == nil ||
		record.Staging.LeaseGeneration != 2 {
		t.Fatalf("current host input staging lease did not commit: %#v replayed=%t err=%v",
			record, replayed, err)
	}
}

func TestDockerHostInputStagingConcurrentReplayConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-host-input-concurrent.db")
	first, run, root := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = first.Close() })
	attempt, plan, _, _, manifest := newStagedDockerHostInputAttempt(t, ctx, first,
		run.ID, root, "docker-host-input-concurrent")
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	stores := []*SQLiteStore{first, second}
	operationDigest := runmutation.Fingerprint(
		"sandbox_docker_host_input_staging_operation.v1", attempt.Intent.OperationKeyDigest)
	intents := make([]sandbox.DockerHostInputStagingIntent, len(stores))
	for index := range intents {
		intents[index], err = sandbox.NewDockerHostInputStagingIntent(
			idgen.New("sandbox-docker-host-input-intent"), operationDigest, attempt, plan,
			manifest, plan.RequestedBy, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	prepared := make([]sandbox.DockerHostInputStagingRecord, len(stores))
	prepareReplayed := make([]bool, len(stores))
	prepareErrors := make([]error, len(stores))
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			prepared[index], prepareReplayed[index], prepareErrors[index] =
				stores[index].PrepareDockerHostInputStagingIntent(ctx, intents[index], attempt.Lease)
		}(index)
	}
	close(start)
	group.Wait()
	if prepareErrors[0] != nil || prepareErrors[1] != nil ||
		prepareReplayed[0] == prepareReplayed[1] ||
		prepared[0].Intent.ID != prepared[1].Intent.ID || prepared[0].Staging != nil ||
		prepared[1].Staging != nil ||
		(prepared[0].Intent.ID != intents[0].ID && prepared[0].Intent.ID != intents[1].ID) {
		t.Fatalf("concurrent host input intents did not converge: records=%#v replayed=%v errors=%v",
			prepared, prepareReplayed, prepareErrors)
	}
	intent := prepared[0].Intent
	report := hostInputBundleStoreReport(t, root, manifest)
	values := make([]sandbox.DockerHostInputStaging, len(stores))
	for index := range values {
		values[index], err = sandbox.NewDockerHostInputStaging(
			idgen.New("sandbox-docker-host-input-staging"), intent, attempt.Lease.Generation,
			report, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make([]sandbox.DockerHostInputStagingRecord, len(stores))
	replayed := make([]bool, len(stores))
	errorsFound := make([]error, len(stores))
	start = make(chan struct{})
	group = sync.WaitGroup{}
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], replayed[index], errorsFound[index] =
				stores[index].RecordDockerHostInputStaging(ctx, values[index], attempt.Lease)
		}(index)
	}
	close(start)
	group.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || replayed[0] == replayed[1] ||
		results[0].Staging == nil || results[1].Staging == nil ||
		results[0].Staging.ID != results[1].Staging.ID ||
		(results[0].Staging.ID != values[0].ID && results[0].Staging.ID != values[1].ID) {
		t.Fatalf("concurrent host input staging did not converge: results=%#v replayed=%v errors=%v",
			results, replayed, errorsFound)
	}
	listed, err := first.ListDockerHostInputStagings(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Staging == nil ||
		listed[0].Staging.ID != results[0].Staging.ID {
		t.Fatalf("concurrent host input staging ledger diverged: %#v err=%v", listed, err)
	}
}

func TestSchemaV57UpgradePreservesV56AttemptWithoutFabricatingEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "docker-host-input-v56.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	attempt, _, _, _, _ := newStagedDockerHostInputAttempt(t, ctx, st, run.ID, root,
		"docker-host-input-upgrade")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, path, 56)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v56 did not upgrade to v57: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetDockerContainerRehearsalAttempt(ctx, attempt.Intent.ID)
	if err != nil || loaded.Stage == nil || loaded.Intent.ID != attempt.Intent.ID {
		t.Fatalf("schema v57 did not preserve v56 attempt: %#v err=%v", loaded, err)
	}
	values, err := upgraded.ListDockerHostInputStagings(ctx, run.ID, 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("schema v57 fabricated host input evidence: %#v err=%v", values, err)
	}
}

func newStagedDockerHostInputAttempt(t *testing.T, ctx context.Context, st *SQLiteStore,
	runID, root, prefix string,
) (sandbox.DockerContainerRehearsalAttempt, sandbox.DockerContainerPlan,
	sandbox.DockerContainerSpec, sandbox.DockerContainerWriteRequest, sandbox.Manifest) {
	t.Helper()
	return newStagedDockerHostInputAttemptWithRequirement(t, ctx, st, runID, root, prefix, true)
}

func newStagedDockerHostInputAttemptWithRequirement(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, root, prefix string, required bool,
) (sandbox.DockerContainerRehearsalAttempt, sandbox.DockerContainerPlan,
	sandbox.DockerContainerSpec, sandbox.DockerContainerWriteRequest, sandbox.Manifest) {
	return newStagedDockerHostInputAttemptWithRequirements(t, ctx, st, runID, root,
		prefix, required, false)
}

func newStagedDockerHostInputAttemptWithRequirements(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, root, prefix string, required, handoffRequired bool,
) (sandbox.DockerContainerRehearsalAttempt, sandbox.DockerContainerPlan,
	sandbox.DockerContainerSpec, sandbox.DockerContainerWriteRequest, sandbox.Manifest) {
	t.Helper()
	for _, name := range []string{"src", "output"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.txt"),
		[]byte("source payload"), 0o644); err != nil {
		t.Fatal(err)
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
	requirement, err := sandbox.NewDockerHostInputRequirement(intent, plan, required, required)
	if err != nil {
		t.Fatal(err)
	}
	handoffRequirement, err := sandbox.NewDockerHostInputHandoffRequirement(intent, plan,
		requirement, handoffRequired, handoffRequired)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := st.BeginDockerContainerRehearsalAttempt(ctx, intent, requirement,
		"docker_host_input_owner", time.Minute, handoffRequirement)
	if err != nil {
		t.Fatal(err)
	}
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
	staged, _, err := st.RecordDockerContainerAttemptStage(ctx, stage, acquired.Attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
	return staged, plan, spec, request, manifest
}

func hostInputBundleStoreReport(t *testing.T, root string,
	manifest sandbox.Manifest,
) sandbox.HostInputBundleReport {
	t.Helper()
	bundleRequest := sandbox.HostInputBundleRequest{WorkspaceRoot: root, Manifest: manifest}
	if err := bundleRequest.Validate(); err != nil {
		t.Fatal(err)
	}
	report, err := sandbox.NewHostInputBundleReport(sandbox.HostInputBundleMeasurements{
		ReadOnlyMountCount: bundleRequest.ReadOnlyMountCount(),
		RegularFileCount:   bundleRequest.ReadOnlyMountCount(), BundleBytes: 4096,
		SourceSnapshotDigest:  strings.Repeat("a", 64),
		ArtifactPayloadDigest: bundleRequest.ArtifactPayloadDigest(),
		BundleDigest:          strings.Repeat("b", 64),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func dockerAttemptCompletionFixture(t *testing.T, plan sandbox.DockerContainerPlan,
	spec sandbox.DockerContainerSpec, request sandbox.DockerContainerWriteRequest,
	attempt sandbox.DockerContainerRehearsalAttempt,
) (sandbox.DockerContainerRehearsal, sandbox.DockerContainerRehearsalOperation,
	sandbox.DockerContainerAttemptCompletion) {
	t.Helper()
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	result, err := sandbox.NewDockerContainerWriteResultFromRecovery(endpoint, request,
		attempt.Stage.Result, attempt.Cleanup.Result)
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
		KeyDigest: attempt.Intent.OperationKeyDigest, RehearsalID: rehearsal.ID,
		PlanID: plan.ID, RunID: plan.RunID, RequestedBy: plan.RequestedBy, CreatedAt: now,
	}
	operation.RequestFingerprint = sandbox.DockerContainerRehearsalRequestFingerprint(rehearsal)
	completion, err := sandbox.NewDockerContainerAttemptCompletion(attempt.Intent.ID,
		rehearsal.ID, attempt.Lease.Generation, now)
	if err != nil {
		t.Fatal(err)
	}
	return rehearsal, operation, completion
}

func assertDockerHostInputSchemaPrivacy(t *testing.T, ctx context.Context, st *SQLiteStore) {
	t.Helper()
	for _, table := range []string{"sandbox_docker_host_input_staging_intents",
		"sandbox_docker_host_input_stagings"} {
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
			case "host_path", "workspace_root", "mount_source", "mount_target", "content",
				"raw_content", "container_id", "manifest_json", "bundle_blob":
				_ = rows.Close()
				t.Fatalf("schema v57 persists private material in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
