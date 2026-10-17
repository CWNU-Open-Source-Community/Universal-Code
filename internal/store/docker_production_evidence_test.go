package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerProductionEvidenceAttemptPersistsReplaysAndRemainsImmutable(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-store")
	attempt := newDockerProductionEvidenceAttemptStoreFixture(t, review,
		"production-evidence-store-capture")
	assertDockerProductionEvidenceAttemptOperationKeyBoundBySQL(t, ctx, st, attempt)
	acquired, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt, "store-worker",
		sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil || acquired.Replayed || acquired.Record.Lease.Generation != 1 {
		t.Fatalf("begin Docker production evidence attempt: %#v err=%v", acquired, err)
	}
	if _, err := st.AcquireDockerProductionEvidenceAttempt(ctx, attempt.ID,
		attempt.RequestedBy, "other-worker",
		sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("active production evidence lease was acquired twice: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_production_evidence_attempt_leases
		WHERE attempt_id = ?`, attempt.ID); err == nil {
		t.Fatal("Docker production evidence attempt lease was deletable")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_production_evidence_attempt_leases
		SET status = 'released', released_at = ? WHERE attempt_id = ?`,
		ts(acquired.Record.Lease.ExpiresAt.Add(time.Second)), attempt.ID); err == nil {
		t.Fatal("Docker production evidence attempt lease accepted a post-expiry release")
	}
	reconciliation := newDockerProductionEvidenceReconciliationStoreFixture(t, acquired.Record)
	record, replayed, err := st.RecordDockerProductionEvidenceReconciliation(ctx,
		reconciliation, acquired.Record.Lease)
	if err != nil || replayed || len(record.Reconciliations) != 1 {
		t.Fatalf("record Docker production evidence reconciliation: %#v replayed=%t err=%v",
			record, replayed, err)
	}
	value, operation, result := newDockerProductionEvidenceCompletionStoreFixture(
		t, ctx, review, record)
	completed, created, replayed, err := st.CompleteDockerProductionEvidenceAttempt(ctx,
		value, operation, result, record.Lease)
	if err != nil || replayed || completed.Result == nil ||
		completed.Lease.Status != sandbox.DockerProductionEvidenceAttemptLeaseReleased {
		t.Fatalf("complete Docker production evidence attempt: %#v replayed=%t err=%v",
			completed, replayed, err)
	}
	if created.StartGatePassed || created.ProcessExecutionAuthorized ||
		created.ArtifactCommitAuthorized || created.RequiredCheckCount != sandbox.MaxBackendChecks {
		t.Fatalf("stored evidence widened authority: %#v", created)
	}
	loaded, err := st.GetDockerProductionEvidence(ctx, created.ID)
	if err != nil || loaded.CaptureFingerprint != created.CaptureFingerprint ||
		len(loaded.Items) != sandbox.MaxBackendChecks {
		t.Fatalf("load Docker production evidence: %#v err=%v", loaded, err)
	}
	loadedAttempt, err := st.GetDockerProductionEvidenceAttempt(ctx, attempt.ID)
	if err != nil || loadedAttempt.Result == nil ||
		loadedAttempt.Result.EvidenceID != created.ID || len(loadedAttempt.Reconciliations) != 1 {
		t.Fatalf("load Docker production evidence attempt: %#v err=%v", loadedAttempt, err)
	}
	listed, err := st.ListDockerProductionEvidenceAttempts(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Attempt.ID != attempt.ID {
		t.Fatalf("list Docker production evidence attempts: %#v err=%v", listed, err)
	}
	replayedAcquisition, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt,
		"store-worker", sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil || !replayedAcquisition.Replayed || replayedAcquisition.Record.Result == nil {
		t.Fatalf("replay completed attempt: %#v err=%v", replayedAcquisition, err)
	}
	replayedValue, replayed, err := st.CreateDockerProductionEvidence(ctx, value, operation)
	if err != nil || !replayed || replayedValue.ID != created.ID {
		t.Fatalf("legacy completed evidence replay: %#v replayed=%t err=%v",
			replayedValue, replayed, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_production_evidence_attempts
		SET real_daemon_contact_authorized = 1 WHERE id = ?`, attempt.ID); err == nil {
		t.Fatal("Docker production evidence attempt was mutable")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_production_evidence_reconciliations
		WHERE attempt_id = ?`, attempt.ID); err == nil {
		t.Fatal("Docker production evidence reconciliation was mutable")
	}
	assertDockerProductionEvidenceRequiresWriteAheadAttempt(t, ctx, st, review)
	eventsForRun, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := map[string]bool{
		"sandbox.docker_production_evidence_attempt_prepared":  false,
		"sandbox.docker_production_evidence_reconciled":        false,
		"sandbox.docker_production_evidence_attempt_completed": false,
		"sandbox.docker_production_evidence_captured":          false,
	}
	for _, event := range eventsForRun {
		if _, ok := wantEvents[event.Type]; ok {
			wantEvents[event.Type] = true
		}
	}
	for eventType, found := range wantEvents {
		if !found {
			t.Fatalf("missing Docker production evidence event %s", eventType)
		}
	}
}

func TestDockerProductionEvidenceAttemptFailureRecoveryFencesStaleGeneration(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-recovery")
	attempt := newDockerProductionEvidenceAttemptStoreFixture(t, review,
		"production-evidence-recovery-capture")
	first, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt, "worker-one",
		sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	firstReconciliation := newDockerProductionEvidenceReconciliationStoreFixture(t, first.Record)
	firstRecord, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx,
		firstReconciliation, first.Record.Lease)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := st.RecordDockerProductionEvidenceAttemptFailure(ctx, attempt.ID,
		firstRecord.Lease, sandbox.DockerProductionEvidenceAttemptErrorCollector,
		time.Now().UTC())
	if err != nil || len(failed.Failures) != 1 ||
		failed.Lease.Status != sandbox.DockerProductionEvidenceAttemptLeaseReleased {
		t.Fatalf("record attempt failure: %#v err=%v", failed, err)
	}
	second, err := st.AcquireDockerProductionEvidenceAttempt(ctx, attempt.ID,
		attempt.RequestedBy, "worker-two", sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil || second.Record.Lease.Generation != 2 || second.TookOver {
		t.Fatalf("reacquire released attempt: %#v err=%v", second, err)
	}
	staleReconciliation, err := sandbox.NewDockerProductionEvidenceReconciliation(
		attempt, firstRecord.Lease, time.Now().UTC())
	if err == nil {
		if _, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx,
			staleReconciliation, firstRecord.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
			t.Fatalf("stale generation reconciled after reacquisition: %v", err)
		}
	}
	secondReconciliation := newDockerProductionEvidenceReconciliationStoreFixture(t, second.Record)
	secondRecord, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx,
		secondReconciliation, second.Record.Lease)
	if err != nil || secondReconciliation.Status !=
		sandbox.DockerProductionEvidenceReconciliationRestart {
		t.Fatalf("record restart reconciliation: %#v err=%v", secondRecord, err)
	}
	value, operation, result := newDockerProductionEvidenceCompletionStoreFixture(
		t, ctx, review, secondRecord)
	completed, _, _, err := st.CompleteDockerProductionEvidenceAttempt(ctx,
		value, operation, result, secondRecord.Lease)
	if err != nil || completed.Result == nil || completed.Lease.Generation != 2 ||
		len(completed.Failures) != 1 || len(completed.Reconciliations) != 2 {
		t.Fatalf("complete recovered attempt: %#v err=%v", completed, err)
	}
}

func TestDockerProductionEvidenceHarnessPersistsGETOnlyEvidenceAndRemainsImmutable(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-harness")
	attempt := newDockerProductionEvidenceAttemptStoreFixture(t, review,
		"production-evidence-harness-capture")
	acquired, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt,
		"harness-worker", sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	control := newDockerProductionEvidenceReconciliationStoreFixture(t, acquired.Record)
	record, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx, control,
		acquired.Record.Lease)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.GetDockerContainerPlan(ctx, review.ContainerPlanID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := sandbox.NewDockerProductionEvidenceHarnessIntent(record.Attempt,
		review, plan, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err := st.PrepareDockerProductionEvidenceHarnessIntent(ctx,
		intent, record.Lease)
	if err != nil || replayed || record.HarnessIntent == nil {
		t.Fatalf("prepare production evidence harness: %#v replayed=%t err=%v",
			record, replayed, err)
	}
	inertEvidence, inertOperation, inertResult :=
		newDockerProductionEvidenceCompletionStoreFixture(t, ctx, review, record)
	if _, _, _, err := st.CompleteDockerProductionEvidenceAttempt(ctx, inertEvidence,
		inertOperation, inertResult, record.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("v67 harness intent fell back to the v66 result path: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sandbox_docker_production_evidence_harness_intents
		SET daemon_write_authorized = 1 WHERE attempt_id = ?`, attempt.ID); err == nil {
		t.Fatal("Docker production evidence harness intent was mutable")
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	inventory, err := sandbox.NewDockerProductionEvidenceHarnessInventory(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	harnessReconciliation, err := sandbox.NewDockerProductionEvidenceHarnessReconciliation(
		intent, record.Lease, control, inventory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, replayed, err = st.RecordDockerProductionEvidenceHarnessReconciliation(ctx,
		harnessReconciliation, record.Lease)
	if err != nil || replayed || len(record.HarnessReconciliations) != 1 {
		t.Fatalf("record production evidence harness reconciliation: %#v replayed=%t err=%v",
			record, replayed, err)
	}
	observation, err := sandbox.NewDockerProductionEvidenceHarnessObservation(
		review.AuthorityFingerprint, strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	value, err := sandbox.NewDockerProductionEvidence(
		idgen.New("sandbox-docker-production-evidence"), attempt.OperationKeyDigest,
		review.RequestedBy, review, observation, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerProductionEvidenceOperation(
		attempt.OperationKeyDigest, value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sandbox.NewDockerProductionEvidenceHarnessResult(intent,
		record.Lease, harnessReconciliation, value)
	if err != nil {
		t.Fatal(err)
	}
	assertDockerProductionEvidenceHarnessResultTimeBoundBySQL(t, ctx, st, value, result)
	completed, stored, replayed, err := st.CompleteDockerProductionEvidenceHarnessAttempt(
		ctx, value, operation, result, record.Lease)
	if err != nil || replayed || completed.Result != nil ||
		completed.HarnessResult == nil ||
		completed.Lease.Status != sandbox.DockerProductionEvidenceAttemptLeaseReleased ||
		!stored.RealDaemonContacted || stored.ObservedCount != sandbox.MaxBackendChecks ||
		stored.ProductionVerifiedCount != 0 || stored.StartGatePassed ||
		stored.ContainerStartAuthorized || stored.ProcessExecutionAuthorized ||
		stored.OutputExportAuthorized || stored.ArtifactCommitAuthorized {
		t.Fatalf("complete production evidence harness: %#v evidence=%#v replayed=%t err=%v",
			completed, stored, replayed, err)
	}
	loaded, err := st.GetDockerProductionEvidenceAttempt(ctx, attempt.ID)
	if err != nil || loaded.HarnessIntent == nil || loaded.HarnessResult == nil ||
		len(loaded.HarnessReconciliations) != 1 || loaded.Result != nil {
		t.Fatalf("load production evidence harness attempt: %#v err=%v", loaded, err)
	}
	if _, err := st.db.ExecContext(ctx,
		`DELETE FROM sandbox_docker_production_evidence_harness_reconciliations
		WHERE attempt_id = ?`, attempt.ID); err == nil {
		t.Fatal("Docker production evidence harness reconciliation was deletable")
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sandbox_docker_production_evidence_harness_results
		SET container_start_authorized = 1 WHERE attempt_id = ?`, attempt.ID); err == nil {
		t.Fatal("Docker production evidence harness result was mutable")
	}
	replayedAttempt, replayedEvidence, replayed, err :=
		st.CompleteDockerProductionEvidenceHarnessAttempt(ctx, value, operation, result,
			acquired.Record.Lease)
	if err != nil || !replayed || replayedAttempt.HarnessResult == nil ||
		replayedEvidence.ID != stored.ID {
		t.Fatalf("replay production evidence harness: %#v evidence=%#v replayed=%t err=%v",
			replayedAttempt, replayedEvidence, replayed, err)
	}
	eventsForRun, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := map[string]bool{
		"sandbox.docker_production_evidence_harness_prepared":   false,
		"sandbox.docker_production_evidence_harness_reconciled": false,
		"sandbox.docker_production_evidence_harness_completed":  false,
	}
	for _, event := range eventsForRun {
		if _, ok := wantEvents[event.Type]; ok {
			wantEvents[event.Type] = true
		}
	}
	for eventType, found := range wantEvents {
		if !found {
			t.Fatalf("missing Docker production evidence harness event %s", eventType)
		}
	}
}

func TestSchemaV67PreservesInFlightV66AttemptWithoutFabricatingHarness(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-production-evidence-v66.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-v67-upgrade")
	attempt := newDockerProductionEvidenceAttemptStoreFixture(t, review,
		"production-evidence-v67-upgrade-capture")
	acquired, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt,
		"upgrade-worker", sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	control := newDockerProductionEvidenceReconciliationStoreFixture(t, acquired.Record)
	if _, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx, control,
		acquired.Record.Lease); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 66)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if version, err := upgraded.SchemaVersion(ctx); err != nil ||
		version != LatestSchemaVersion {
		t.Fatalf("schema v67 upgrade version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetDockerProductionEvidenceAttempt(ctx, attempt.ID)
	if err != nil || loaded.HarnessIntent != nil || loaded.HarnessResult != nil ||
		len(loaded.HarnessReconciliations) != 0 || len(loaded.Reconciliations) != 1 {
		t.Fatalf("schema v67 fabricated a harness: %#v err=%v", loaded, err)
	}
}

func TestDockerProductionEvidenceAttemptExpiredLeaseCanOnlyBeTakenOverByNextGeneration(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-takeover")
	attempt := newDockerProductionEvidenceAttemptStoreFixture(t, review,
		"production-evidence-takeover-capture")
	first, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt, "worker-one",
		sandbox.MinDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	firstReconciliation := newDockerProductionEvidenceReconciliationStoreFixture(t, first.Record)
	if _, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx,
		firstReconciliation, first.Record.Lease); err != nil {
		t.Fatal(err)
	}
	time.Sleep(sandbox.MinDockerProductionEvidenceAttemptLeaseTTL + 150*time.Millisecond)
	second, err := st.AcquireDockerProductionEvidenceAttempt(ctx, attempt.ID,
		attempt.RequestedBy, "worker-two", sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil || !second.TookOver || second.Record.Lease.Generation != 2 ||
		second.Record.Lease.OwnerID != "worker-two" {
		t.Fatalf("expired attempt was not generation-fenced: %#v err=%v", second, err)
	}
	if _, err := st.RecordDockerProductionEvidenceAttemptFailure(ctx, attempt.ID,
		first.Record.Lease, sandbox.DockerProductionEvidenceAttemptErrorDeadline,
		time.Now().UTC()); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale lease recorded a failure after takeover: %v", err)
	}
	reconciliation := newDockerProductionEvidenceReconciliationStoreFixture(t, second.Record)
	if reconciliation.Status != sandbox.DockerProductionEvidenceReconciliationRestart {
		t.Fatalf("takeover did not require restart reconciliation: %#v", reconciliation)
	}
}

func TestSchemaV65PreservesReviewWithoutFabricatingProductionEvidence(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-production-evidence-v64.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-upgrade")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 64)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v65 upgrade version=%d err=%v", version, err)
	}
	if _, err := upgraded.GetDockerStartGateReview(ctx, review.ID); err != nil {
		t.Fatalf("schema v65 lost start-gate review: %v", err)
	}
	values, err := upgraded.ListDockerProductionEvidence(ctx, run.ID, 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("schema v65 fabricated production evidence: %#v err=%v", values, err)
	}
	attempts, err := upgraded.ListDockerProductionEvidenceAttempts(ctx, run.ID, 10)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("schema v66 fabricated production evidence attempts: %#v err=%v", attempts, err)
	}
}

func TestSchemaV66PreservesLegacyEvidenceWithoutFabricatingAttempt(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-production-evidence-v65.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	review := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-legacy")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 65)
	value, operation := newLegacyDockerProductionEvidenceStoreFixture(t, ctx, review,
		"production-evidence-legacy-capture")
	if _, replayed, err := st.CreateDockerProductionEvidence(ctx, value, operation); err != nil || replayed {
		_ = st.Close()
		t.Fatalf("create schema v65 evidence: replayed=%t err=%v", replayed, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	loaded, err := upgraded.GetDockerProductionEvidence(ctx, value.ID)
	if err != nil || loaded.CaptureFingerprint != value.CaptureFingerprint {
		t.Fatalf("schema v66 lost legacy evidence: %#v err=%v", loaded, err)
	}
	attempts, err := upgraded.ListDockerProductionEvidenceAttempts(ctx, run.ID, 10)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("schema v66 fabricated a legacy attempt: %#v err=%v", attempts, err)
	}
}

func prepareDockerProductionEvidenceReviewStoreFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, root, key string,
) sandbox.DockerStartGateReview {
	t.Helper()
	cleanup, application := prepareCompletedDockerRuntimeInputResourceCleanupForReviewTest(
		t, ctx, st, runID, root, key)
	projection, err := st.GetDockerRuntimeInputProjectionPlan(ctx,
		application.Intent.ProjectionID)
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
	reviewKey := runmutation.Fingerprint(sandbox.DockerStartGateReviewOperationVersion,
		cleanup.Intent.ID, key+"-review")
	review, err := sandbox.NewDockerStartGateReview(
		idgen.New("sandbox-docker-start-gate-review"), reviewKey,
		cleanup.Intent.RequestedBy, sandbox.DockerStartGateReviewBinding{
			CleanupIntentID: cleanup.Intent.ID, CleanupResultID: cleanup.Result.ID,
			ApplicationIntentID: application.Intent.ID,
			ApplicationResultID: application.Result.ID, ProjectionID: projection.ID,
			ContainerPlanID: containerPlan.ID, PreflightID: preflight.ID,
			RunID: application.Intent.RunID, MissionID: application.Intent.MissionID,
			WorkspaceID:              application.Intent.WorkspaceID,
			ManifestFingerprint:      application.Intent.ManifestFingerprint,
			ThreatModelFingerprint:   preflight.Handshake.ThreatModelFingerprint,
			CleanupResultFingerprint: cleanup.Result.ResultFingerprint,
			MaxLogBytes:              preflight.OutputPlan.MaxOutputBytes,
		}, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	reviewOperation, err := sandbox.NewDockerStartGateReviewOperation(reviewKey, review)
	if err != nil {
		t.Fatal(err)
	}
	review, _, err = st.CreateDockerStartGateReview(ctx, review, reviewOperation)
	if err != nil {
		t.Fatal(err)
	}
	return review
}

func newDockerProductionEvidenceAttemptStoreFixture(t *testing.T,
	review sandbox.DockerStartGateReview, key string,
) sandbox.DockerProductionEvidenceAttempt {
	t.Helper()
	endpoint, err := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	operationKey := runmutation.Fingerprint(sandbox.DockerProductionEvidenceOperationVersion,
		review.ID, key)
	attempt, err := sandbox.NewDockerProductionEvidenceAttempt(
		idgen.New("sandbox-docker-production-evidence-attempt"), operationKey,
		review.RequestedBy, review, endpoint, true,
		sandbox.DefaultDockerProductionEvidenceCaptureTimeout, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func newDockerProductionEvidenceReconciliationStoreFixture(t *testing.T,
	record sandbox.DockerProductionEvidenceAttemptRecord,
) sandbox.DockerProductionEvidenceReconciliation {
	t.Helper()
	value, err := sandbox.NewDockerProductionEvidenceReconciliation(record.Attempt,
		record.Lease, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newDockerProductionEvidenceCompletionStoreFixture(t *testing.T, ctx context.Context,
	review sandbox.DockerStartGateReview, record sandbox.DockerProductionEvidenceAttemptRecord,
) (sandbox.DockerProductionEvidence, sandbox.DockerProductionEvidenceOperation,
	sandbox.DockerProductionEvidenceAttemptResult) {
	t.Helper()
	reconciliation, found := record.CurrentReconciliation()
	if !found {
		t.Fatal("production evidence completion requires reconciliation")
	}
	collector := sandbox.NewLocalDockerProductionEvidenceCollector()
	observation, err := collector.Capture(ctx, sandbox.DockerProductionEvidenceCaptureRequest{
		ReviewID: review.ID, RunID: review.RunID,
		AuthorityFingerprint: review.AuthorityFingerprint, AttemptID: record.Attempt.ID,
		LeaseGeneration: record.Lease.Generation, EndpointClass: record.Attempt.EndpointClass,
		EndpointFingerprint: record.Attempt.EndpointFingerprint,
		DeadlineAt:          time.Now().UTC().Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := sandbox.NewDockerProductionEvidence(
		idgen.New("sandbox-docker-production-evidence"), record.Attempt.OperationKeyDigest,
		review.RequestedBy, review, observation, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerProductionEvidenceOperation(
		record.Attempt.OperationKeyDigest, value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sandbox.NewDockerProductionEvidenceAttemptResult(record.Attempt,
		record.Lease, reconciliation, value)
	if err != nil {
		t.Fatal(err)
	}
	return value, operation, result
}

func newLegacyDockerProductionEvidenceStoreFixture(t *testing.T, ctx context.Context,
	review sandbox.DockerStartGateReview, key string,
) (sandbox.DockerProductionEvidence, sandbox.DockerProductionEvidenceOperation) {
	t.Helper()
	endpoint, err := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	collector := sandbox.NewLocalDockerProductionEvidenceCollector()
	observation, err := collector.Capture(ctx, sandbox.DockerProductionEvidenceCaptureRequest{
		ReviewID: review.ID, RunID: review.RunID,
		AuthorityFingerprint: review.AuthorityFingerprint, AttemptID: "legacy-attempt",
		LeaseGeneration: 1, EndpointClass: endpoint.Class,
		EndpointFingerprint: endpoint.Fingerprint,
		DeadlineAt:          time.Now().UTC().Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	operationKey := runmutation.Fingerprint(sandbox.DockerProductionEvidenceOperationVersion,
		review.ID, key)
	value, err := sandbox.NewDockerProductionEvidence(
		idgen.New("sandbox-docker-production-evidence"), operationKey, review.RequestedBy,
		review, observation, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerProductionEvidenceOperation(operationKey, value)
	if err != nil {
		t.Fatal(err)
	}
	return value, operation
}

func assertDockerProductionEvidenceAttemptOperationKeyBoundBySQL(t *testing.T,
	ctx context.Context, st *SQLiteStore, attempt sandbox.DockerProductionEvidenceAttempt,
) {
	t.Helper()
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertDockerProductionEvidenceAttemptTx(ctx, tx, attempt); err != nil {
		t.Fatal(err)
	}
	mismatchedKey := runmutation.Fingerprint(sandbox.DockerProductionEvidenceOperationVersion,
		attempt.ReviewID, "mismatch")
	if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_docker_production_evidence_attempt_operations
		(key_digest, request_fingerprint, attempt_id, review_id, run_id, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, mismatchedKey, attempt.RequestFingerprint,
		attempt.ID, attempt.ReviewID, attempt.RunID, attempt.RequestedBy,
		ts(attempt.CreatedAt)); err == nil {
		t.Fatal("SQLite accepted an attempt operation key not bound to its intent")
	}
}

func assertDockerProductionEvidenceRequiresWriteAheadAttempt(t *testing.T,
	ctx context.Context, st *SQLiteStore, review sandbox.DockerStartGateReview,
) {
	t.Helper()
	value, operation := newLegacyDockerProductionEvidenceStoreFixture(t, ctx, review,
		"write-ahead-bypass")
	if _, _, err := st.CreateDockerProductionEvidence(ctx, value, operation); err == nil ||
		!strings.Contains(err.Error(), "write-ahead attempt") {
		t.Fatalf("Docker production evidence bypassed its write-ahead attempt: %v", err)
	}
	if _, err := st.GetDockerProductionEvidence(ctx, value.ID); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("failed write-ahead bypass left evidence: %v", err)
	}
}

func assertDockerProductionEvidenceHarnessResultTimeBoundBySQL(t *testing.T,
	ctx context.Context, st *SQLiteStore, value sandbox.DockerProductionEvidence,
	result sandbox.DockerProductionEvidenceHarnessResult,
) {
	t.Helper()
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertDockerProductionEvidenceTx(ctx, tx, value); err != nil {
		t.Fatal(err)
	}
	for _, item := range value.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_docker_production_evidence_items
			(evidence_id, ordinal, name, probe_code, state, observed,
			production_verified, sufficient_for_start, blocker_code, evidence_digest)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, item.Ordinal, item.Name,
			item.ProbeCode, item.State, boolInt(item.Observed),
			boolInt(item.ProductionVerified), boolInt(item.SufficientForStart),
			item.BlockerCode, item.EvidenceDigest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sandbox_docker_production_evidence_harness_results
		(attempt_id, evidence_id, protocol_version, status, lease_generation,
		intent_fingerprint, reconciliation_fingerprint, evidence_capture_fingerprint,
		daemon_read_count, probe_count, observed_count, production_verified_count,
		real_daemon_contacted, daemon_write_authorized, container_start_authorized,
		process_execution_authorized, output_export_authorized,
		artifact_commit_authorized, result_fingerprint, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.AttemptID, result.EvidenceID, result.ProtocolVersion, result.Status,
		result.LeaseGeneration, result.IntentFingerprint,
		result.ReconciliationFingerprint, result.EvidenceCaptureFingerprint,
		result.DaemonReadCount, result.ProbeCount, result.ObservedCount,
		result.ProductionVerifiedCount, boolInt(result.RealDaemonContacted),
		boolInt(result.DaemonWriteAuthorized), boolInt(result.ContainerStartAuthorized),
		boolInt(result.ProcessExecutionAuthorized), boolInt(result.OutputExportAuthorized),
		boolInt(result.ArtifactCommitAuthorized), result.ResultFingerprint,
		ts(result.CreatedAt.Add(time.Millisecond))); err == nil {
		t.Fatal("SQLite accepted a harness result timestamp not bound to its evidence")
	}
}
