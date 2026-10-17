package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestDockerProductionEvidenceReviewPersistsReplaysAndRejectsHalfRecords(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	gateReview := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-review")
	evidence, attempt := completeDockerProductionEvidenceHarnessReviewFixture(
		t, ctx, st, gateReview, "production-evidence-review-capture")
	keyDigest := runmutation.Fingerprint(
		sandbox.DockerProductionEvidenceReviewOperationVersion,
		evidence.ID, "review-operation")
	review, err := sandbox.NewDockerProductionEvidenceReview(
		idgen.New("sandbox-docker-production-evidence-review"), keyDigest, "reviewer",
		sandbox.DockerProductionEvidenceReviewDecisionAccepted,
		sandbox.DockerProductionEvidenceReviewReasonMetadataScopeAccepted,
		evidence, attempt, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerProductionEvidenceReviewOperation(keyDigest, review)
	if err != nil {
		t.Fatal(err)
	}
	tamperedOperation := operation
	tamperedOperation.RequestFingerprint = strings.Repeat("a", 64)
	if err := validateStoredDockerProductionEvidenceReviewOperation(
		review, tamperedOperation); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("tampered stored operation binding was accepted: %v", err)
	}
	assertDockerProductionEvidenceReviewOperationCannotCommitAlone(
		t, ctx, st, operation)
	assertDockerProductionEvidenceReviewCannotCommitAlone(t, ctx, st, review)
	assertDockerProductionEvidenceReviewSQLRejectsTampering(
		t, ctx, st, review, operation)
	stored, replayed, err := st.CreateDockerProductionEvidenceReview(ctx, review, operation)
	if err != nil || replayed || !stored.ReceiptAccepted || stored.StartGatePassed ||
		stored.ProductionVerifiedCount != 0 || stored.BlockerCount != sandbox.MaxBackendChecks ||
		stored.ContainerStartAuthorized || stored.ProcessExecutionAuthorized ||
		stored.OutputExportAuthorized || stored.ArtifactCommitAuthorized {
		t.Fatalf("store evidence review: %#v replayed=%t err=%v", stored, replayed, err)
	}
	loaded, err := st.GetDockerProductionEvidenceReview(ctx, stored.ID)
	if err != nil || loaded.ReviewFingerprint != stored.ReviewFingerprint {
		t.Fatalf("load evidence review: %#v err=%v", loaded, err)
	}
	byEvidence, found, err := st.GetDockerProductionEvidenceReviewByEvidence(ctx, evidence.ID)
	if err != nil || !found || byEvidence.ID != stored.ID {
		t.Fatalf("load evidence review by receipt: %#v found=%t err=%v",
			byEvidence, found, err)
	}
	byAttempt, found, err := st.GetDockerProductionEvidenceAttemptByEvidence(ctx, evidence.ID)
	if err != nil || !found || byAttempt.Attempt.ID != attempt.Attempt.ID ||
		byAttempt.HarnessResult == nil {
		t.Fatalf("load v67 attempt by evidence: %#v found=%t err=%v",
			byAttempt, found, err)
	}
	listed, err := st.ListDockerProductionEvidenceReviews(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != stored.ID {
		t.Fatalf("list evidence reviews: %#v err=%v", listed, err)
	}
	replayedValue, replayed, err := st.CreateDockerProductionEvidenceReview(ctx,
		review, operation)
	if err != nil || !replayed || replayedValue.ID != stored.ID || !replayedValue.Replayed {
		t.Fatalf("replay evidence review: %#v replayed=%t err=%v",
			replayedValue, replayed, err)
	}
	conflictDigest := runmutation.Fingerprint(
		sandbox.DockerProductionEvidenceReviewOperationVersion,
		evidence.ID, "different-review-operation")
	conflict, err := sandbox.NewDockerProductionEvidenceReview(
		idgen.New("sandbox-docker-production-evidence-review"), conflictDigest, "reviewer",
		sandbox.DockerProductionEvidenceReviewDecisionRejected,
		sandbox.DockerProductionEvidenceReviewReasonOperatorRejected,
		evidence, attempt, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	conflictOperation, err := sandbox.NewDockerProductionEvidenceReviewOperation(
		conflictDigest, conflict)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateDockerProductionEvidenceReview(ctx, conflict,
		conflictOperation); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("one receipt accepted two review decisions: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_production_evidence_reviews
		SET receipt_accepted = 0 WHERE id = ?`, stored.ID); err == nil {
		t.Fatal("Docker production evidence review was mutable")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_production_evidence_reviews
		WHERE id = ?`, stored.ID); err == nil {
		t.Fatal("Docker production evidence review was deletable")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_production_evidence_review_operations
		SET request_fingerprint = ? WHERE key_digest = ?`, strings.Repeat("a", 64),
		operation.KeyDigest); err == nil {
		t.Fatal("Docker production evidence review operation was mutable")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_production_evidence_review_operations
		WHERE key_digest = ?`, operation.KeyDigest); err == nil {
		t.Fatal("Docker production evidence review operation was deletable")
	}
	eventsForRun, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEvent := false
	for _, event := range eventsForRun {
		if event.Type != "sandbox.docker_production_evidence_reviewed" {
			continue
		}
		foundEvent = true
		if strings.Contains(event.PayloadJSON, "review-operation") ||
			strings.Contains(event.PayloadJSON, "reviewer") ||
			strings.Contains(event.PayloadJSON, "daemon-id") {
			t.Fatalf("evidence review event leaked private input: %s", event.PayloadJSON)
		}
	}
	if !foundEvent {
		t.Fatal("missing Docker production evidence review event")
	}
}

func TestDockerProductionEvidenceReviewPersistsRejectedDecision(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	gateReview := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-review-rejected")
	evidence, attempt := completeDockerProductionEvidenceHarnessReviewFixture(
		t, ctx, st, gateReview, "production-evidence-review-rejected-capture")
	keyDigest := runmutation.Fingerprint(
		sandbox.DockerProductionEvidenceReviewOperationVersion,
		evidence.ID, "rejected-review-operation")
	review, err := sandbox.NewDockerProductionEvidenceReview(
		idgen.New("sandbox-docker-production-evidence-review"), keyDigest, "reviewer",
		sandbox.DockerProductionEvidenceReviewDecisionRejected,
		sandbox.DockerProductionEvidenceReviewReasonInsufficientEvidence,
		evidence, attempt, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerProductionEvidenceReviewOperation(keyDigest, review)
	if err != nil {
		t.Fatal(err)
	}
	stored, replayed, err := st.CreateDockerProductionEvidenceReview(ctx, review, operation)
	if err != nil || replayed || stored.ReceiptAccepted ||
		stored.Decision != sandbox.DockerProductionEvidenceReviewDecisionRejected ||
		stored.ReasonCode != sandbox.DockerProductionEvidenceReviewReasonInsufficientEvidence ||
		stored.ProductionVerifiedCount != 0 || stored.BlockerCount != sandbox.MaxBackendChecks ||
		stored.StartGatePassed || stored.ProcessExecutionAuthorized {
		t.Fatalf("store rejected evidence review: %#v replayed=%t err=%v",
			stored, replayed, err)
	}
	replayedValue, replayed, err := st.CreateDockerProductionEvidenceReview(
		ctx, review, operation)
	if err != nil || !replayed || replayedValue.ID != stored.ID ||
		replayedValue.ReceiptAccepted {
		t.Fatalf("replay rejected evidence review: %#v replayed=%t err=%v",
			replayedValue, replayed, err)
	}
	listed, err := st.ListDockerProductionEvidenceReviews(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != stored.ID ||
		listed[0].ReceiptAccepted {
		t.Fatalf("list rejected evidence review: %#v err=%v", listed, err)
	}
	eventsForRun, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range eventsForRun {
		if event.Type == "sandbox.docker_production_evidence_reviewed" &&
			strings.Contains(event.PayloadJSON, `"decision":"rejected"`) &&
			strings.Contains(event.PayloadJSON, `"receipt_accepted":false`) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing metadata-only rejected evidence review event")
	}
}

func TestDockerProductionEvidenceReviewConcurrentStoresConverge(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-production-evidence-review-concurrent.db")
	first, run, root := openSandboxManifestStoreAt(t, ctx, databasePath)
	t.Cleanup(func() { _ = first.Close() })
	gateReview := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, first, run.ID, root, "production-evidence-review-concurrent")
	evidence, attempt := completeDockerProductionEvidenceHarnessReviewFixture(
		t, ctx, first, gateReview, "production-evidence-review-concurrent-capture")
	keyDigest := runmutation.Fingerprint(
		sandbox.DockerProductionEvidenceReviewOperationVersion,
		evidence.ID, "concurrent-review-operation")
	review, err := sandbox.NewDockerProductionEvidenceReview(
		idgen.New("sandbox-docker-production-evidence-review"), keyDigest, "reviewer",
		sandbox.DockerProductionEvidenceReviewDecisionAccepted,
		sandbox.DockerProductionEvidenceReviewReasonMetadataScopeAccepted,
		evidence, attempt, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerProductionEvidenceReviewOperation(keyDigest, review)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	stores := []*SQLiteStore{first, second}
	values := make([]sandbox.DockerProductionEvidenceReview, len(stores))
	replayed := make([]bool, len(stores))
	errorsFound := make([]error, len(stores))
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			values[index], replayed[index], errorsFound[index] =
				stores[index].CreateDockerProductionEvidenceReview(ctx, review, operation)
		}(index)
	}
	close(start)
	group.Wait()
	for index, err := range errorsFound {
		if err != nil || values[index].ID != review.ID {
			t.Fatalf("concurrent evidence review %d diverged: value=%#v replayed=%t err=%v",
				index, values[index], replayed[index], err)
		}
	}
	if replayed[0] == replayed[1] {
		t.Fatalf("concurrent evidence reviews did not produce one commit and one replay: %v",
			replayed)
	}
	eventsForRun, err := first.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range eventsForRun {
		if event.Type == "sandbox.docker_production_evidence_reviewed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent evidence review event count=%d", count)
	}
}

func TestSchemaV68PreservesV67ReceiptWithoutFabricatingReview(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-production-evidence-v67-review.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	gateReview := prepareDockerProductionEvidenceReviewStoreFixture(
		t, ctx, st, run.ID, root, "production-evidence-review-upgrade")
	evidence, _ := completeDockerProductionEvidenceHarnessReviewFixture(
		t, ctx, st, gateReview, "production-evidence-review-upgrade-capture")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 67)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v68 upgrade version=%d err=%v", version, err)
	}
	if _, found, err := upgraded.GetDockerProductionEvidenceReviewByEvidence(ctx,
		evidence.ID); err != nil || found {
		t.Fatalf("schema v68 fabricated a review: found=%t err=%v", found, err)
	}
}

func assertDockerProductionEvidenceReviewOperationCannotCommitAlone(t *testing.T,
	ctx context.Context, st *SQLiteStore,
	operation sandbox.DockerProductionEvidenceReviewOperation,
) {
	t.Helper()
	tx, err := st.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sandbox_docker_production_evidence_review_operations
		(key_digest, request_fingerprint, review_id, evidence_id, attempt_id, run_id,
		reviewed_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.ReviewID, operation.EvidenceID,
		operation.AttemptID, operation.RunID, operation.ReviewedBy, ts(operation.CreatedAt))
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("stage isolated evidence review operation: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("isolated evidence review operation committed without its review")
	}
	if _, found, err := st.GetDockerProductionEvidenceReviewOperation(ctx,
		operation.KeyDigest); err != nil || found {
		t.Fatalf("failed half-operation remained durable: found=%t err=%v", found, err)
	}
}

func assertDockerProductionEvidenceReviewCannotCommitAlone(t *testing.T,
	ctx context.Context, st *SQLiteStore, review sandbox.DockerProductionEvidenceReview,
) {
	t.Helper()
	tx, err := st.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerProductionEvidenceReviewTx(ctx, tx, review); err == nil {
		_ = tx.Rollback()
		t.Fatal("isolated evidence review was inserted without its operation")
	}
	_ = tx.Rollback()
}

func assertDockerProductionEvidenceReviewSQLRejectsTampering(t *testing.T,
	ctx context.Context, st *SQLiteStore, review sandbox.DockerProductionEvidenceReview,
	operation sandbox.DockerProductionEvidenceReviewOperation,
) {
	t.Helper()
	tests := []struct {
		name   string
		mutate func(*sandbox.DockerProductionEvidenceReview)
	}{
		{name: "request fingerprint", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.RequestFingerprint = strings.Repeat("a", 64)
		}},
		{name: "start gate", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.StartGateReviewID = idgen.New("wrong-start-gate-review")
		}},
		{name: "Mission", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.MissionID = idgen.New("wrong-mission")
		}},
		{name: "workspace", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.WorkspaceID = idgen.New("wrong-workspace")
		}},
		{name: "capture fingerprint", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.EvidenceCaptureFingerprint = strings.Repeat("b", 64)
		}},
		{name: "harness fingerprint", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.HarnessResultFingerprint = strings.Repeat("c", 64)
		}},
		{name: "production verification", mutate: func(value *sandbox.DockerProductionEvidenceReview) {
			value.ProductionVerifiedCount = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, err := st.db.BeginTx(ctx, &sql.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO sandbox_docker_production_evidence_review_operations
				(key_digest, request_fingerprint, review_id, evidence_id, attempt_id, run_id,
				reviewed_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
				operation.RequestFingerprint, operation.ReviewID, operation.EvidenceID,
				operation.AttemptID, operation.RunID, operation.ReviewedBy,
				ts(operation.CreatedAt))
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("stage review operation: %v", err)
			}
			changed := review
			test.mutate(&changed)
			if err := insertDockerProductionEvidenceReviewTx(ctx, tx, changed); err == nil {
				_ = tx.Rollback()
				t.Fatal("tampered evidence review bypassed SQL binding")
			}
			_ = tx.Rollback()
		})
	}
}

func completeDockerProductionEvidenceHarnessReviewFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, gateReview sandbox.DockerStartGateReview, operationKey string,
) (sandbox.DockerProductionEvidence, sandbox.DockerProductionEvidenceAttemptRecord) {
	t.Helper()
	attempt := newDockerProductionEvidenceAttemptStoreFixture(t, gateReview, operationKey)
	acquired, err := st.BeginDockerProductionEvidenceAttempt(ctx, attempt,
		"evidence-review-worker", sandbox.DefaultDockerProductionEvidenceAttemptLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	control := newDockerProductionEvidenceReconciliationStoreFixture(t, acquired.Record)
	record, _, err := st.RecordDockerProductionEvidenceReconciliation(ctx, control,
		acquired.Record.Lease)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.GetDockerContainerPlan(ctx, gateReview.ContainerPlanID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := sandbox.NewDockerProductionEvidenceHarnessIntent(record.Attempt,
		gateReview, plan, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = st.PrepareDockerProductionEvidenceHarnessIntent(ctx, intent, record.Lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := sandbox.NewDockerProductionEvidenceHarnessInventory(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	harnessReconciliation, err := sandbox.NewDockerProductionEvidenceHarnessReconciliation(
		intent, record.Lease, control, inventory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = st.RecordDockerProductionEvidenceHarnessReconciliation(ctx,
		harnessReconciliation, record.Lease)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := sandbox.NewDockerProductionEvidenceHarnessObservation(
		gateReview.AuthorityFingerprint, strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := sandbox.NewDockerProductionEvidence(
		idgen.New("sandbox-docker-production-evidence"), attempt.OperationKeyDigest,
		gateReview.RequestedBy, gateReview, observation, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	evidenceOperation, err := sandbox.NewDockerProductionEvidenceOperation(
		attempt.OperationKeyDigest, evidence)
	if err != nil {
		t.Fatal(err)
	}
	harnessResult, err := sandbox.NewDockerProductionEvidenceHarnessResult(intent,
		record.Lease, harnessReconciliation, evidence)
	if err != nil {
		t.Fatal(err)
	}
	completed, stored, replayed, err := st.CompleteDockerProductionEvidenceHarnessAttempt(
		ctx, evidence, evidenceOperation, harnessResult, record.Lease)
	if err != nil || replayed || completed.HarnessResult == nil {
		t.Fatalf("complete v67 review fixture: %#v replayed=%t err=%v",
			completed, replayed, err)
	}
	return stored, completed
}
