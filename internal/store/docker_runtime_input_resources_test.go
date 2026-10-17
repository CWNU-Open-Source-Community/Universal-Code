package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerRuntimeInputResourceLedgerIsReplayableFencedAndMetadataOnly(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	application, descriptor := prepareCompletedDockerRuntimeInputApplicationForResourceTest(
		t, ctx, st, run.ID, root, "runtime-resource-ledger")
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	observation := sandbox.DockerRuntimeInputResourceObservation{
		EndpointClass: endpoint.Class, EndpointFingerprint: endpoint.Fingerprint,
		TargetState:      sandbox.DockerRuntimeInputResourceTargetOwned,
		OwnedVolumeCount: len(descriptor.Mounts), DaemonReadCount: len(descriptor.Mounts) + 1,
		ObservedAt: time.Now().UTC(),
	}
	inspection, err := sandbox.NewDockerRuntimeInputResourceInspection(
		idgen.New("sandbox-docker-runtime-input-resource-inspection"),
		runmutation.Fingerprint(sandbox.DockerRuntimeInputResourceInspectionOperationVersion,
			application.Intent.ID, "inspect"), application.Intent.RequestedBy,
		application, descriptor, observation)
	if err != nil {
		t.Fatal(err)
	}
	recorded, replayed, err := st.RecordDockerRuntimeInputResourceInspection(ctx, inspection)
	if err != nil || replayed || !recorded.Complete || !recorded.CleanupEligible {
		t.Fatalf("record inspection: %#v replayed=%t err=%v", recorded, replayed, err)
	}
	replayedInspection, replayed, err := st.RecordDockerRuntimeInputResourceInspection(ctx, inspection)
	if err != nil || !replayed || !replayedInspection.Replayed ||
		replayedInspection.InspectionFingerprint != inspection.InspectionFingerprint {
		t.Fatalf("replay inspection: %#v replayed=%t err=%v",
			replayedInspection, replayed, err)
	}
	byOperation, found, err := st.GetDockerRuntimeInputResourceInspectionByOperation(ctx,
		inspection.OperationKeyDigest)
	if err != nil || !found || byOperation.ID != inspection.ID {
		t.Fatalf("load inspection by operation: %#v found=%t err=%v", byOperation, found, err)
	}
	listedInspections, err := st.ListDockerRuntimeInputResourceInspections(ctx, run.ID, 10)
	if err != nil || len(listedInspections) != 1 || listedInspections[0].ID != inspection.ID {
		t.Fatalf("list inspections: %#v err=%v", listedInspections, err)
	}

	cleanupIntent, err := sandbox.NewDockerRuntimeInputResourceCleanupIntent(
		idgen.New("sandbox-docker-runtime-input-resource-cleanup"),
		runmutation.Fingerprint(sandbox.DockerRuntimeInputResourceCleanupOperationVersion,
			inspection.ID, "cleanup"), inspection, descriptor, endpoint, true, true,
		inspection.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := st.BeginDockerRuntimeInputResourceCleanup(ctx, cleanupIntent,
		"runtime_resource_cleanup_owner", time.Minute)
	if err != nil || acquired.Record.Lease.Generation != 1 || acquired.Replayed || acquired.TookOver {
		t.Fatalf("begin cleanup: %#v err=%v", acquired, err)
	}
	if _, err := st.BeginDockerRuntimeInputResourceCleanup(ctx, cleanupIntent,
		"second_runtime_resource_cleanup_owner", time.Minute); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("active cleanup lease was not exclusive: %v", err)
	}
	if _, err := st.RecordDockerRuntimeInputResourceCleanupFailure(ctx, cleanupIntent.ID,
		acquired.Record.Lease, sandbox.DockerRuntimeInputResourceErrorConnection,
		time.Now().UTC().Add(15*time.Second)); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("future cleanup failure timestamp was not rejected: %v", err)
	}
	failed, err := st.RecordDockerRuntimeInputResourceCleanupFailure(ctx, cleanupIntent.ID,
		acquired.Record.Lease, sandbox.DockerRuntimeInputResourceErrorConnection,
		time.Now().UTC())
	if err != nil || failed.Lease.Status != sandbox.DockerRuntimeInputResourceCleanupLeaseReleased ||
		len(failed.Failures) != 1 {
		t.Fatalf("record cleanup failure: %#v err=%v", failed, err)
	}
	resumed, err := st.AcquireDockerRuntimeInputResourceCleanup(ctx, cleanupIntent.ID,
		cleanupIntent.RequestedBy, "resumed_runtime_resource_cleanup_owner", time.Minute)
	if err != nil || resumed.Record.Lease.Generation != 2 || resumed.TookOver {
		t.Fatalf("resume cleanup: %#v err=%v", resumed, err)
	}
	total := len(descriptor.Mounts) + 1
	staleResult, err := sandbox.NewDockerRuntimeInputResourceCleanupResult(
		"runtime-resource-stale-result", cleanupIntent, acquired.Record.Lease, descriptor,
		total, 0, total, 2*total, total, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteDockerRuntimeInputResourceCleanup(ctx, staleResult,
		acquired.Record.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale cleanup worker committed: %v", err)
	}
	futureResult, err := sandbox.NewDockerRuntimeInputResourceCleanupResult(
		"runtime-resource-future-result", cleanupIntent, resumed.Record.Lease, descriptor,
		total, 0, total, 2*total, total, time.Now().UTC().Add(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteDockerRuntimeInputResourceCleanup(ctx, futureResult,
		resumed.Record.Lease); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("future cleanup result timestamp was not rejected: %v", err)
	}
	result, err := sandbox.NewDockerRuntimeInputResourceCleanupResult(
		"runtime-resource-cleanup-result", cleanupIntent, resumed.Record.Lease, descriptor,
		total, 0, total, 2*total, total, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	completed, replayed, err := st.CompleteDockerRuntimeInputResourceCleanup(ctx, result,
		resumed.Record.Lease)
	if err != nil || replayed || completed.Result == nil ||
		completed.Lease.Status != sandbox.DockerRuntimeInputResourceCleanupLeaseReleased {
		t.Fatalf("complete cleanup: %#v replayed=%t err=%v", completed, replayed, err)
	}
	replayedAcquisition, err := st.BeginDockerRuntimeInputResourceCleanup(ctx, cleanupIntent,
		"third_runtime_resource_cleanup_owner", time.Minute)
	if err != nil || !replayedAcquisition.Replayed || replayedAcquisition.Record.Result == nil {
		t.Fatalf("cleanup replay failed: %#v err=%v", replayedAcquisition, err)
	}
	loaded, err := st.GetDockerRuntimeInputResourceCleanup(ctx, cleanupIntent.ID)
	if err != nil || loaded.Result == nil || loaded.Result.ResultFingerprint != result.ResultFingerprint {
		t.Fatalf("load cleanup: %#v err=%v", loaded, err)
	}
	byCleanupOperation, found, err := st.GetDockerRuntimeInputResourceCleanupByOperation(ctx,
		cleanupIntent.OperationKeyDigest)
	if err != nil || !found || byCleanupOperation.Intent.ID != cleanupIntent.ID {
		t.Fatalf("load cleanup by operation: %#v found=%t err=%v",
			byCleanupOperation, found, err)
	}
	listedCleanups, err := st.ListDockerRuntimeInputResourceCleanups(ctx, run.ID, 10)
	if err != nil || len(listedCleanups) != 1 || listedCleanups[0].Intent.ID != cleanupIntent.ID {
		t.Fatalf("list cleanups: %#v err=%v", listedCleanups, err)
	}
	projection, err := st.GetDockerRuntimeInputProjectionPlan(ctx, application.Intent.ProjectionID)
	if err != nil {
		t.Fatal(err)
	}
	containerPlan, err := st.GetDockerContainerPlan(ctx, application.Intent.ContainerPlanID)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := st.GetSandboxDisabledPreflight(ctx, containerPlan.PreflightID)
	if err != nil {
		t.Fatal(err)
	}
	reviewBinding := sandbox.DockerStartGateReviewBinding{
		CleanupIntentID: cleanupIntent.ID, CleanupResultID: result.ID,
		ApplicationIntentID: application.Intent.ID, ApplicationResultID: application.Result.ID,
		ProjectionID: projection.ID, ContainerPlanID: containerPlan.ID,
		PreflightID: preflight.ID, RunID: application.Intent.RunID,
		MissionID: application.Intent.MissionID, WorkspaceID: application.Intent.WorkspaceID,
		ManifestFingerprint:      application.Intent.ManifestFingerprint,
		ThreatModelFingerprint:   preflight.Handshake.ThreatModelFingerprint,
		CleanupResultFingerprint: result.ResultFingerprint,
		MaxLogBytes:              preflight.OutputPlan.MaxOutputBytes,
	}
	reviewKey := runmutation.Fingerprint(sandbox.DockerStartGateReviewOperationVersion,
		cleanupIntent.ID, "start-gate-review")
	review, err := sandbox.NewDockerStartGateReview(
		idgen.New("sandbox-docker-start-gate-review"), reviewKey, cleanupIntent.RequestedBy,
		reviewBinding, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	reviewOperation, err := sandbox.NewDockerStartGateReviewOperation(reviewKey, review)
	if err != nil {
		t.Fatal(err)
	}
	storedReview, replayed, err := st.CreateDockerStartGateReview(ctx, review, reviewOperation)
	if err != nil || replayed || storedReview.StartGatePassed ||
		storedReview.BlockerCount != sandbox.MaxBackendChecks ||
		storedReview.ContainerStartAuthorized || storedReview.ProcessExecutionAuthorized {
		t.Fatalf("create start-gate review: %#v replayed=%t err=%v",
			storedReview, replayed, err)
	}
	replayedReview, replayed, err := st.CreateDockerStartGateReview(ctx, review, reviewOperation)
	if err != nil || !replayed || !replayedReview.Replayed || replayedReview.ID != review.ID {
		t.Fatalf("replay start-gate review: %#v replayed=%t err=%v",
			replayedReview, replayed, err)
	}
	loadedReview, err := st.GetDockerStartGateReview(ctx, review.ID)
	if err != nil || loadedReview.ReviewFingerprint != review.ReviewFingerprint ||
		len(loadedReview.Checks) != sandbox.MaxBackendChecks ||
		len(loadedReview.Lifecycle.Transitions) != sandbox.DockerStartGateLifecycleTransitionCount {
		t.Fatalf("load start-gate review: %#v err=%v", loadedReview, err)
	}
	reviewByCleanup, found, err := st.GetDockerStartGateReviewByCleanup(ctx, cleanupIntent.ID)
	if err != nil || !found || reviewByCleanup.ID != review.ID {
		t.Fatalf("load start-gate review by cleanup: %#v found=%t err=%v",
			reviewByCleanup, found, err)
	}
	listedReviews, err := st.ListDockerStartGateReviews(ctx, run.ID, 10)
	if err != nil || len(listedReviews) != 1 || listedReviews[0].ID != review.ID {
		t.Fatalf("list start-gate reviews: %#v err=%v", listedReviews, err)
	}

	for _, mutation := range []string{
		`UPDATE sandbox_docker_runtime_input_resource_inspections SET cleanup_eligible = 0 WHERE id = ?`,
		`UPDATE sandbox_docker_runtime_input_resource_cleanup_intents SET process_execution_authorized = 1 WHERE id = ?`,
		`DELETE FROM sandbox_docker_runtime_input_resource_cleanup_failures WHERE intent_id = ?`,
		`UPDATE sandbox_docker_runtime_input_resource_cleanup_results SET target_absent = 0 WHERE intent_id = ?`,
		`UPDATE sandbox_docker_start_gate_reviews SET start_gate_passed = 1 WHERE id = ?`,
		`UPDATE sandbox_docker_start_gate_review_checks SET production_verified = 1 WHERE review_id = ?`,
		`UPDATE sandbox_docker_process_lifecycle_blueprints SET implementation_present = 1 WHERE review_id = ?`,
		`DELETE FROM sandbox_docker_process_lifecycle_transitions WHERE review_id = ?`,
	} {
		id := cleanupIntent.ID
		if strings.Contains(mutation, "resource_inspections") {
			id = inspection.ID
		} else if strings.Contains(mutation, "start_gate") ||
			strings.Contains(mutation, "process_lifecycle") {
			id = review.ID
		}
		if _, err := st.db.ExecContext(ctx, mutation, id); err == nil ||
			!strings.Contains(err.Error(), "cannot be") {
			t.Fatalf("resource ledger mutation was not rejected: %s: %v", mutation, err)
		}
	}
	for query, id := range map[string]string{
		`DELETE FROM sandbox_docker_runtime_input_resource_cleanup_leases WHERE intent_id = ?`: cleanupIntent.ID,
		`DELETE FROM sandbox_docker_runtime_input_application_leases WHERE intent_id = ?`:      application.Intent.ID,
	} {
		if _, err := st.db.ExecContext(ctx, query, id); err == nil ||
			!strings.Contains(err.Error(), "cannot be deleted") {
			t.Fatalf("lease deletion was not rejected: %s: %v", query, err)
		}
	}
	assertDockerRuntimeInputResourceSchemaPrivacy(t, ctx, st)
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundInspected, foundPrepared, foundFailed, foundCompleted, foundStartGate :=
		false, false, false, false, false
	for _, event := range timeline {
		switch event.Type {
		case events.SandboxDockerRuntimeInputResourceInspectedEvent:
			foundInspected = true
		case events.SandboxDockerRuntimeInputResourceCleanupPreparedEvent:
			foundPrepared = true
		case events.SandboxDockerRuntimeInputResourceCleanupFailedEvent:
			foundFailed = true
		case events.SandboxDockerRuntimeInputResourceCleanupCompletedEvent:
			foundCompleted = true
		case events.SandboxDockerStartGateReviewedEvent:
			foundStartGate = true
		}
	}
	if !foundInspected || !foundPrepared || !foundFailed || !foundCompleted || !foundStartGate {
		t.Fatalf("resource lifecycle events missing: inspected=%t prepared=%t failed=%t completed=%t start_gate=%t",
			foundInspected, foundPrepared, foundFailed, foundCompleted, foundStartGate)
	}
}

func TestSchemaV63PreservesV62CleanupWithoutFabricatingStartGateReview(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-start-gate-v62.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	cleanup, application := prepareCompletedDockerRuntimeInputResourceCleanupForReviewTest(
		t, ctx, st, run.ID, root, "start-gate-upgrade")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 62)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	version, err := upgraded.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v63 upgrade version=%d err=%v", version, err)
	}
	if loaded, err := upgraded.GetDockerRuntimeInputApplication(ctx,
		application.Intent.ID); err != nil || loaded.Result == nil {
		t.Fatalf("schema v63 lost v61 application: %#v err=%v", loaded, err)
	}
	if loaded, err := upgraded.GetDockerRuntimeInputResourceCleanup(ctx,
		cleanup.Intent.ID); err != nil || loaded.Result == nil {
		t.Fatalf("schema v63 lost v62 cleanup: %#v err=%v", loaded, err)
	}
	reviews, err := upgraded.ListDockerStartGateReviews(ctx, run.ID, 10)
	if err != nil || len(reviews) != 0 {
		t.Fatalf("schema v63 fabricated start-gate reviews: %#v err=%v", reviews, err)
	}
}

func TestDockerStartGateReviewConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-start-gate-concurrency.db")
	first, run, root := openSandboxManifestStoreAt(t, ctx, databasePath)
	t.Cleanup(func() { _ = first.Close() })
	cleanup, application := prepareCompletedDockerRuntimeInputResourceCleanupForReviewTest(
		t, ctx, first, run.ID, root, "start-gate-concurrency")
	projection, err := first.GetDockerRuntimeInputProjectionPlan(ctx, application.Intent.ProjectionID)
	if err != nil {
		t.Fatal(err)
	}
	containerPlan, err := first.GetDockerContainerPlan(ctx, application.Intent.ContainerPlanID)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := first.GetSandboxDisabledPreflight(ctx, containerPlan.PreflightID)
	if err != nil {
		t.Fatal(err)
	}
	binding := sandbox.DockerStartGateReviewBinding{
		CleanupIntentID: cleanup.Intent.ID, CleanupResultID: cleanup.Result.ID,
		ApplicationIntentID: application.Intent.ID, ApplicationResultID: application.Result.ID,
		ProjectionID: projection.ID, ContainerPlanID: containerPlan.ID,
		PreflightID: preflight.ID, RunID: application.Intent.RunID,
		MissionID: application.Intent.MissionID, WorkspaceID: application.Intent.WorkspaceID,
		ManifestFingerprint:      application.Intent.ManifestFingerprint,
		ThreatModelFingerprint:   preflight.Handshake.ThreatModelFingerprint,
		CleanupResultFingerprint: cleanup.Result.ResultFingerprint,
		MaxLogBytes:              preflight.OutputPlan.MaxOutputBytes,
	}
	second, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	key := runmutation.Fingerprint(sandbox.DockerStartGateReviewOperationVersion,
		cleanup.Intent.ID, "concurrent-review")
	type outcome struct {
		value    sandbox.DockerStartGateReview
		replayed bool
		err      error
	}
	results := make(chan outcome, 2)
	for _, current := range []*SQLiteStore{first, second} {
		go func(current *SQLiteStore) {
			review, buildErr := sandbox.NewDockerStartGateReview(
				idgen.New("sandbox-docker-start-gate-review"), key,
				cleanup.Intent.RequestedBy, binding, true, time.Now().UTC())
			if buildErr != nil {
				results <- outcome{err: buildErr}
				return
			}
			operation, buildErr := sandbox.NewDockerStartGateReviewOperation(key, review)
			if buildErr != nil {
				results <- outcome{err: buildErr}
				return
			}
			value, replayed, createErr := current.CreateDockerStartGateReview(
				ctx, review, operation)
			results <- outcome{value: value, replayed: replayed, err: createErr}
		}(current)
	}
	firstResult, secondResult := <-results, <-results
	if firstResult.err != nil || secondResult.err != nil ||
		firstResult.value.ID != secondResult.value.ID ||
		firstResult.replayed == secondResult.replayed {
		t.Fatalf("concurrent reviews did not converge: first=%#v second=%#v",
			firstResult, secondResult)
	}
}

func TestSchemaV62PreservesV61ApplicationWithoutFabricatingResourceRecords(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-runtime-input-resources-v61.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	application, _ := prepareCompletedDockerRuntimeInputApplicationForResourceTest(
		t, ctx, st, run.ID, root, "runtime-resource-upgrade")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 61)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	version, err := upgraded.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v62 upgrade version=%d err=%v", version, err)
	}
	if loaded, err := upgraded.GetDockerRuntimeInputApplication(ctx,
		application.Intent.ID); err != nil || loaded.Result == nil {
		t.Fatalf("schema v62 lost v61 application: %#v err=%v", loaded, err)
	}
	inspections, err := upgraded.ListDockerRuntimeInputResourceInspections(ctx, run.ID, 10)
	if err != nil || len(inspections) != 0 {
		t.Fatalf("schema v62 fabricated inspections: %#v err=%v", inspections, err)
	}
	cleanups, err := upgraded.ListDockerRuntimeInputResourceCleanups(ctx, run.ID, 10)
	if err != nil || len(cleanups) != 0 {
		t.Fatalf("schema v62 fabricated cleanups: %#v err=%v", cleanups, err)
	}
}

func prepareCompletedDockerRuntimeInputApplicationForResourceTest(t *testing.T,
	ctx context.Context, st *SQLiteStore, runID, root, key string,
) (sandbox.DockerRuntimeInputApplicationRecord, sandbox.DockerRuntimeInputResourceDescriptor) {
	t.Helper()
	projection, operation := prepareDockerRuntimeInputProjectionStoreFixture(
		t, ctx, st, runID, root, key)
	projection, replayed, err := st.CreateDockerRuntimeInputProjectionPlan(ctx, projection, operation)
	if err != nil || replayed {
		t.Fatalf("create projection: replayed=%t err=%v", replayed, err)
	}
	intent, request, writeRequest := newDockerRuntimeInputApplicationStoreFixtureFull(
		t, ctx, st, root, projection)
	acquired, err := st.BeginDockerRuntimeInputApplication(ctx, intent,
		"runtime_resource_application_owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sandbox.NewDockerRuntimeInputApplicationResult(
		idgen.New("sandbox-docker-runtime-input-result"), intent, acquired.Record.Lease, request,
		strings.Repeat("d", 64), 3+5*len(request.Mounts), 1+4*len(request.Mounts), 0,
		time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	application, replayed, err := st.CompleteDockerRuntimeInputApplication(
		ctx, result, acquired.Record.Lease)
	if err != nil || replayed {
		t.Fatalf("complete application: replayed=%t err=%v", replayed, err)
	}
	descriptor, err := sandbox.NewDockerRuntimeInputResourceDescriptor(
		application, projection, writeRequest)
	if err != nil {
		t.Fatal(err)
	}
	return application, descriptor
}

func prepareCompletedDockerRuntimeInputResourceCleanupForReviewTest(t *testing.T,
	ctx context.Context, st *SQLiteStore, runID, root, key string,
) (sandbox.DockerRuntimeInputResourceCleanupRecord,
	sandbox.DockerRuntimeInputApplicationRecord,
) {
	t.Helper()
	application, descriptor := prepareCompletedDockerRuntimeInputApplicationForResourceTest(
		t, ctx, st, runID, root, key)
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	observation := sandbox.DockerRuntimeInputResourceObservation{
		EndpointClass: endpoint.Class, EndpointFingerprint: endpoint.Fingerprint,
		TargetState:      sandbox.DockerRuntimeInputResourceTargetOwned,
		OwnedVolumeCount: len(descriptor.Mounts), DaemonReadCount: len(descriptor.Mounts) + 1,
		ObservedAt: time.Now().UTC(),
	}
	inspection, err := sandbox.NewDockerRuntimeInputResourceInspection(
		idgen.New("sandbox-docker-runtime-input-resource-inspection"),
		runmutation.Fingerprint(sandbox.DockerRuntimeInputResourceInspectionOperationVersion,
			application.Intent.ID, key+"-inspect"), application.Intent.RequestedBy,
		application, descriptor, observation)
	if err != nil {
		t.Fatal(err)
	}
	inspection, _, err = st.RecordDockerRuntimeInputResourceInspection(ctx, inspection)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := sandbox.NewDockerRuntimeInputResourceCleanupIntent(
		idgen.New("sandbox-docker-runtime-input-resource-cleanup"),
		runmutation.Fingerprint(sandbox.DockerRuntimeInputResourceCleanupOperationVersion,
			inspection.ID, key+"-cleanup"), inspection, descriptor, endpoint, true, true,
		inspection.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := st.BeginDockerRuntimeInputResourceCleanup(ctx, intent,
		"start_gate_cleanup_owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	total := len(descriptor.Mounts) + 1
	result, err := sandbox.NewDockerRuntimeInputResourceCleanupResult(
		idgen.New("sandbox-docker-runtime-input-resource-cleanup-result"), intent,
		acquired.Record.Lease, descriptor, total, 0, total, 2*total, total,
		time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	completed, _, err := st.CompleteDockerRuntimeInputResourceCleanup(ctx, result,
		acquired.Record.Lease)
	if err != nil {
		t.Fatal(err)
	}
	return completed, application
}

func assertDockerRuntimeInputResourceSchemaPrivacy(t *testing.T, ctx context.Context,
	st *SQLiteStore,
) {
	t.Helper()
	for _, table := range []string{"sandbox_docker_runtime_input_resource_inspections",
		"sandbox_docker_runtime_input_resource_cleanup_intents",
		"sandbox_docker_runtime_input_resource_cleanup_leases",
		"sandbox_docker_runtime_input_resource_cleanup_failures",
		"sandbox_docker_runtime_input_resource_cleanup_results",
		"sandbox_docker_start_gate_reviews", "sandbox_docker_start_gate_review_checks",
		"sandbox_docker_process_lifecycle_blueprints",
		"sandbox_docker_process_lifecycle_transitions",
		"sandbox_docker_start_gate_review_operations"} {
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
				"raw_content", "container_id", "container_name", "volume_name", "carrier_name",
				"manifest_json", "archive_blob", "operation_key":
				_ = rows.Close()
				t.Fatalf("schema v62 persists private material in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
