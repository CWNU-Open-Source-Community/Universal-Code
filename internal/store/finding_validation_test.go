package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

func TestFindingArtifactEvidenceAndValidationConvergeAcrossStores(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "finding-validation.db", 1)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-validation-plan", "finding-validation-execution")
	report, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil || len(report.Findings) != 1 {
		t.Fatalf("finding report setup failed: %#v err=%v", report, err)
	}
	finding := report.Findings[0]
	projectionDigest := report.ProjectionDigest
	service := application.NewFindingReportService(st)
	_, err = service.DecideValidation(ctx, application.DecideFindingValidationRequest{
		FindingID: finding.ID, OperationKey: "finding-validate-empty-0001",
		Status: domain.FindingStatusValidated, Reason: "requires concrete evidence",
		DecidedBy: "validation_operator",
	})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("evidence-free validation code=%s err=%v", apperror.CodeOf(err), err)
	}

	primaryArtifact := captureFindingValidationArtifact(t, ctx, st, run,
		"echo primary-validation-evidence")
	secondaryArtifact := captureFindingValidationArtifact(t, ctx, st, run,
		"echo post-decision-evidence")
	mission, err := st.GetMission(ctx, run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	_, otherRun, err := application.NewRunService(st).Create(ctx,
		application.CreateRunRequest{
			Goal: "cross-run Artifact Evidence", Profile: "code",
			WorkspaceID: mission.WorkspaceID,
			Budget:      domain.Budget{MaxTurns: 5, MaxToolCalls: 5},
		})
	if err != nil {
		t.Fatal(err)
	}
	otherArtifact := captureFindingValidationArtifact(t, ctx, st, otherRun,
		"echo cross-run-evidence")
	_, err = service.AttachArtifactEvidence(ctx,
		application.AttachFindingArtifactEvidenceRequest{
			FindingID: finding.ID, ArtifactID: otherArtifact.ID,
			OperationKey: "finding-cross-run-0001", AttachedBy: "validation_operator",
			Note: "must not cross the Run boundary",
		})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("cross-Run evidence code=%s err=%v", apperror.CodeOf(err), err)
	}

	databasePath := sqliteDatabasePath(t, ctx, st)
	second, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	evidenceRequest := application.AttachFindingArtifactEvidenceRequest{
		FindingID: finding.ID, ArtifactID: primaryArtifact.ID,
		OperationKey: "finding-evidence-converge-0001",
		AttachedBy:   "validation_operator", Note: "reproduced with a frozen tool output",
	}
	type evidenceOutcome struct {
		result application.AttachFindingArtifactEvidenceResult
		err    error
	}
	evidenceResults := make(chan evidenceOutcome, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	stores := []*SQLiteStore{st, second}
	for index := range 8 {
		workers.Add(1)
		go func(current int) {
			defer workers.Done()
			<-start
			result, err := application.NewFindingReportService(stores[current%2]).
				AttachArtifactEvidence(ctx, evidenceRequest)
			evidenceResults <- evidenceOutcome{result: result, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(evidenceResults)
	evidenceCreated := 0
	var evidenceID string
	for outcome := range evidenceResults {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if !outcome.result.Replayed {
			evidenceCreated++
		}
		if evidenceID == "" {
			evidenceID = outcome.result.Evidence.ID
		} else if outcome.result.Evidence.ID != evidenceID {
			t.Fatalf("evidence replay changed identity: %#v", outcome.result)
		}
	}
	if evidenceCreated != 1 {
		t.Fatalf("expected one evidence creation, got %d", evidenceCreated)
	}

	validationRequest := application.DecideFindingValidationRequest{
		FindingID: finding.ID, OperationKey: "finding-validation-converge-0001",
		Status:    domain.FindingStatusValidated,
		Reason:    "Artifact output confirms the reported behavior",
		DecidedBy: "validation_operator",
	}
	type validationOutcome struct {
		result application.DecideFindingValidationResult
		err    error
	}
	validationResults := make(chan validationOutcome, 8)
	start = make(chan struct{})
	workers = sync.WaitGroup{}
	for index := range 8 {
		workers.Add(1)
		go func(current int) {
			defer workers.Done()
			<-start
			result, err := application.NewFindingReportService(stores[current%2]).
				DecideValidation(ctx, validationRequest)
			validationResults <- validationOutcome{result: result, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(validationResults)
	validationCreated := 0
	var validationID string
	for outcome := range validationResults {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if !outcome.result.Replayed {
			validationCreated++
		}
		if validationID == "" {
			validationID = outcome.result.Validation.ID
		} else if outcome.result.Validation.ID != validationID {
			t.Fatalf("validation replay changed identity: %#v", outcome.result)
		}
	}
	if validationCreated != 1 {
		t.Fatalf("expected one validation creation, got %d", validationCreated)
	}

	_, err = service.AttachArtifactEvidence(ctx,
		application.AttachFindingArtifactEvidenceRequest{
			FindingID: finding.ID, ArtifactID: secondaryArtifact.ID,
			OperationKey: "finding-evidence-after-decision-0001",
			AttachedBy:   "validation_operator", Note: "late evidence must be rejected",
		})
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("post-decision evidence code=%s err=%v", apperror.CodeOf(err), err)
	}
	_, err = service.DecideValidation(ctx, application.DecideFindingValidationRequest{
		FindingID: finding.ID, OperationKey: "finding-second-decision-0001",
		Status: domain.FindingStatusRejected, Reason: "attempt to replace a decision",
		DecidedBy: "validation_operator",
	})
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("second decision code=%s err=%v", apperror.CodeOf(err), err)
	}

	verified, err := service.VerifyArtifactEvidence(ctx, finding.ID)
	if err != nil || verified.Status != domain.FindingStatusValidated ||
		verified.ArtifactEvidenceCount != 1 || verified.ArtifactEvidenceDigest == "" {
		t.Fatalf("verification failed: %#v err=%v", verified, err)
	}
	loaded, err := second.GetFindingReport(ctx, report.ID)
	if err != nil || loaded.ProjectionDigest != projectionDigest ||
		len(loaded.Findings[0].ArtifactEvidence) != 1 ||
		loaded.Findings[0].Validation == nil ||
		loaded.Findings[0].Validation.Status != domain.FindingStatusValidated {
		t.Fatalf("validation projection drifted: %#v err=%v", loaded, err)
	}
	for table, want := range map[string]int{
		"finding_artifact_evidence":            1,
		"finding_artifact_evidence_operations": 1,
		"finding_validation_decisions":         1,
		"finding_validation_operations":        1,
	} {
		var count int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil || count != want {
			t.Fatalf("unexpected %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	var rawKeys int
	if err := st.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM finding_artifact_evidence_operations
			WHERE operation_key_digest = ? OR request_fingerprint = ?) +
		(SELECT COUNT(*) FROM finding_validation_operations
			WHERE operation_key_digest = ? OR request_fingerprint = ?)`,
		evidenceRequest.OperationKey, evidenceRequest.OperationKey,
		validationRequest.OperationKey, validationRequest.OperationKey).Scan(&rawKeys); err != nil || rawKeys != 0 {
		t.Fatalf("raw operation keys were stored: count=%d err=%v", rawKeys, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline,
		events.FindingArtifactEvidenceAttachedEvent) != 1 ||
		countRunEventType(timeline, events.FindingValidationDecidedEvent) != 1 {
		t.Fatalf("finding validation timeline drifted: err=%v", err)
	}
	for _, event := range timeline {
		if event.Type != events.FindingArtifactEvidenceAttachedEvent &&
			event.Type != events.FindingValidationDecidedEvent {
			continue
		}
		for _, secret := range []string{
			evidenceRequest.Note, validationRequest.Reason, primaryArtifact.Content,
		} {
			if strings.Contains(event.PayloadJSON, secret) {
				t.Fatalf("finding validation event leaked narrative/content: %s", event.PayloadJSON)
			}
		}
	}
	for _, statement := range []string{
		`UPDATE run_artifacts SET content = 'changed' WHERE id = '` + primaryArtifact.ID + `'`,
		`DELETE FROM run_artifacts WHERE id = '` + primaryArtifact.ID + `'`,
		`UPDATE finding_artifact_evidence SET note = 'changed' WHERE id = '` + evidenceID + `'`,
		`DELETE FROM finding_validation_decisions WHERE id = '` + validationID + `'`,
	} {
		if _, err := st.db.ExecContext(ctx, statement); err == nil {
			t.Fatalf("immutable validation fact accepted %q", statement)
		}
	}
}

func TestSchemaV35ReportSurvivesFindingValidationMigration(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "finding-validation-v35.db")
	st, run, _ := createReadOnlyFanoutFixture(t, "seed.db", 1, 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-validation-v35-plan", "finding-validation-v35-execution")
	report, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 35)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v35 did not upgrade: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetFindingReport(ctx, report.ID)
	if err != nil || loaded.ProjectionDigest != report.ProjectionDigest ||
		len(loaded.Findings) != 1 || len(loaded.Findings[0].ArtifactEvidence) != 0 ||
		loaded.Findings[0].Validation != nil {
		t.Fatalf("v35 report did not survive v36: %#v err=%v", loaded, err)
	}
	for _, table := range []string{
		"finding_artifact_evidence", "finding_artifact_evidence_operations",
		"finding_validation_decisions", "finding_validation_operations",
	} {
		var count int
		if err := upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration synthesized %s rows=%d err=%v", table, count, err)
		}
	}
}

func TestFindingCanBeRejectedWithoutArtifactEvidence(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "finding-rejection.db", 1)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-rejection-plan", "finding-rejection-execution")
	report, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewFindingReportService(st)
	result, err := service.DecideValidation(ctx,
		application.DecideFindingValidationRequest{
			FindingID:    report.Findings[0].ID,
			OperationKey: "finding-rejection-empty-0001",
			Status:       domain.FindingStatusRejected,
			Reason:       "model assertion could not be reproduced",
			DecidedBy:    "validation_operator",
		})
	if err != nil || result.Replayed ||
		result.Validation.Status != domain.FindingStatusRejected ||
		result.Validation.ArtifactEvidenceCount != 0 ||
		result.Validation.ArtifactEvidenceDigest == "" {
		t.Fatalf("evidence-free rejection failed: %#v err=%v", result, err)
	}
	verified, err := service.VerifyArtifactEvidence(ctx, report.Findings[0].ID)
	if err != nil || verified.Status != domain.FindingStatusRejected ||
		verified.ArtifactEvidenceCount != 0 {
		t.Fatalf("rejected finding verification failed: %#v err=%v", verified, err)
	}
}

func TestSchemaV36FreezesLegacyArtifactAndRejectsPreMigrationTampering(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "finding-validation-tamper.db")
	st, run, _ := createReadOnlyFanoutFixture(t, "seed.db", 1, 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-validation-tamper-plan", "finding-validation-tamper-execution")
	report, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob := captureFindingValidationArtifact(t, ctx, st, run,
		"echo legacy-tamper-evidence")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 35)
	if _, err := st.db.ExecContext(ctx, `UPDATE run_artifacts SET content = ? WHERE id = ?`,
		strings.Repeat("x", int(blob.SizeBytes)), blob.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err := upgraded.GetRunArtifact(ctx, blob.ID); err == nil ||
		!strings.Contains(err.Error(), "hash") {
		t.Fatalf("legacy Artifact tampering was not detected after migration: %v", err)
	}
	_, err = application.NewFindingReportService(upgraded).AttachArtifactEvidence(ctx,
		application.AttachFindingArtifactEvidenceRequest{
			FindingID: report.Findings[0].ID, ArtifactID: blob.ID,
			OperationKey: "finding-tampered-artifact-0001",
			AttachedBy:   "validation_operator", Note: "must reject corrupt evidence",
		})
	if err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered legacy Artifact was attached: %v", err)
	}
	if _, err := upgraded.db.ExecContext(ctx, `UPDATE run_artifacts SET content = ? WHERE id = ?`,
		blob.Content, blob.ID); err == nil || !strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("migrated Artifact was not frozen: %v", err)
	}
}

func captureFindingValidationArtifact(t *testing.T, ctx context.Context,
	st *SQLiteStore, run domain.Run, command string,
) artifact.Blob {
	t.Helper()
	mission, err := st.GetMission(ctx, run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	gateway := toolgateway.New(st, policy.NewDefaultChecker())
	proposal, err := gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ShellTool, Arguments: map[string]string{"command": command},
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID,
		RequestedBy: "validation_test",
	})
	if err != nil || proposal.Proposal == nil {
		t.Fatalf("Artifact proposal failed: %#v err=%v", proposal, err)
	}
	approved, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ShellTool,
		ProposalID: proposal.Proposal.ID, ReviewedBy: "validation_test",
	})
	if err != nil || approved.Result == nil {
		t.Fatalf("Artifact approval failed: %#v err=%v", approved, err)
	}
	blob, err := st.GetRunArtifact(ctx, approved.Result.Metadata["artifact_stdout_id"])
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func sqliteDatabasePath(t *testing.T, ctx context.Context, st *SQLiteStore) string {
	t.Helper()
	var sequence int
	var name, path string
	if err := st.db.QueryRowContext(ctx, `PRAGMA database_list`).
		Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}
