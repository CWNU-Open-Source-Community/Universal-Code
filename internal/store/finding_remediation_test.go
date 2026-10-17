package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

func TestFindingAcceptanceRemediationAndFixConvergeAcrossStores(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "finding-remediation.db", 1)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-remediation-plan", "finding-remediation-execution")
	report, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil || len(report.Findings) != 1 {
		t.Fatalf("finding report setup failed: %#v err=%v", report, err)
	}
	finding := report.Findings[0]
	projectionDigest := report.ProjectionDigest
	service := application.NewFindingReportService(st)
	_, err = service.Accept(ctx, application.DecideFindingAcceptanceRequest{
		FindingID: finding.ID, OperationKey: "finding-accept-before-validation-0001",
		Reason: "must not accept an unverified assertion", DecidedBy: "remediation_operator",
	})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("pre-validation acceptance code=%s err=%v", apperror.CodeOf(err), err)
	}

	validationArtifact := captureFindingValidationArtifact(t, ctx, st, run,
		"echo validation-evidence")
	staleRemediationArtifact := captureFindingValidationArtifact(t, ctx, st, run,
		"echo created-before-acceptance")
	if _, err := service.AttachArtifactEvidence(ctx,
		application.AttachFindingArtifactEvidenceRequest{
			FindingID: finding.ID, ArtifactID: validationArtifact.ID,
			OperationKey: "finding-remediation-validation-evidence-0001",
			AttachedBy:   "remediation_operator", Note: "reproduces the assertion",
		}); err != nil {
		t.Fatal(err)
	}
	validation, err := service.DecideValidation(ctx,
		application.DecideFindingValidationRequest{
			FindingID: finding.ID, OperationKey: "finding-remediation-validation-0001",
			Status: domain.FindingStatusValidated, Reason: "frozen Artifact confirms it",
			DecidedBy: "remediation_operator",
		})
	if err != nil {
		t.Fatal(err)
	}

	databasePath := sqliteDatabasePath(t, ctx, st)
	second, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	stores := []*SQLiteStore{st, second}
	acceptanceRequest := application.DecideFindingAcceptanceRequest{
		FindingID: finding.ID, OperationKey: "finding-acceptance-converge-0001",
		Reason: "operator accepts the validated risk", DecidedBy: "remediation_operator",
	}
	type acceptanceOutcome struct {
		result application.DecideFindingAcceptanceResult
		err    error
	}
	acceptanceResults := make(chan acceptanceOutcome, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Add(1)
		go func(current int) {
			defer workers.Done()
			<-start
			result, err := application.NewFindingReportService(stores[current%2]).
				Accept(ctx, acceptanceRequest)
			acceptanceResults <- acceptanceOutcome{result: result, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(acceptanceResults)
	acceptanceCreated := 0
	var acceptanceID string
	for outcome := range acceptanceResults {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if !outcome.result.Replayed {
			acceptanceCreated++
		}
		if acceptanceID == "" {
			acceptanceID = outcome.result.Acceptance.ID
		} else if outcome.result.Acceptance.ID != acceptanceID {
			t.Fatalf("acceptance replay changed identity: %#v", outcome.result)
		}
	}
	if acceptanceCreated != 1 {
		t.Fatalf("expected one acceptance creation, got %d", acceptanceCreated)
	}
	_, err = service.Accept(ctx, application.DecideFindingAcceptanceRequest{
		FindingID: finding.ID, OperationKey: acceptanceRequest.OperationKey,
		Reason: "changed acceptance intent", DecidedBy: acceptanceRequest.DecidedBy,
	})
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed acceptance intent code=%s err=%v", apperror.CodeOf(err), err)
	}
	_, err = service.Fix(ctx, application.DecideFindingFixRequest{
		FindingID: finding.ID, OperationKey: "finding-fix-without-remediation-0001",
		Reason: "must not fix without evidence", DecidedBy: "remediation_operator",
	})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("evidence-free fix code=%s err=%v", apperror.CodeOf(err), err)
	}
	_, err = service.AttachRemediationEvidence(ctx,
		application.AttachFindingRemediationEvidenceRequest{
			FindingID: finding.ID, ArtifactID: validationArtifact.ID,
			OperationKey: "finding-reuse-validation-artifact-0001",
			AttachedBy:   "remediation_operator", Note: "must not reuse validation evidence",
		})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("validation Artifact reuse code=%s err=%v", apperror.CodeOf(err), err)
	}
	_, err = service.AttachRemediationEvidence(ctx,
		application.AttachFindingRemediationEvidenceRequest{
			FindingID: finding.ID, ArtifactID: staleRemediationArtifact.ID,
			OperationKey: "finding-stale-remediation-artifact-0001",
			AttachedBy:   "remediation_operator", Note: "must be newer than acceptance",
		})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("stale remediation Artifact code=%s err=%v", apperror.CodeOf(err), err)
	}

	remediationArtifact := captureFindingValidationArtifact(t, ctx, st, run,
		"echo remediation-complete")
	remediationRequest := application.AttachFindingRemediationEvidenceRequest{
		FindingID: finding.ID, ArtifactID: remediationArtifact.ID,
		OperationKey: "finding-remediation-evidence-converge-0001",
		AttachedBy:   "remediation_operator", Note: "fresh output proves the correction",
	}
	type remediationOutcome struct {
		result application.AttachFindingRemediationEvidenceResult
		err    error
	}
	remediationResults := make(chan remediationOutcome, 8)
	start = make(chan struct{})
	workers = sync.WaitGroup{}
	for index := range 8 {
		workers.Add(1)
		go func(current int) {
			defer workers.Done()
			<-start
			result, err := application.NewFindingReportService(stores[current%2]).
				AttachRemediationEvidence(ctx, remediationRequest)
			remediationResults <- remediationOutcome{result: result, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(remediationResults)
	remediationCreated := 0
	var remediationEvidenceID string
	for outcome := range remediationResults {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if !outcome.result.Replayed {
			remediationCreated++
		}
		if remediationEvidenceID == "" {
			remediationEvidenceID = outcome.result.Evidence.ID
		} else if outcome.result.Evidence.ID != remediationEvidenceID {
			t.Fatalf("remediation Evidence replay changed identity: %#v", outcome.result)
		}
	}
	if remediationCreated != 1 {
		t.Fatalf("expected one remediation Evidence creation, got %d", remediationCreated)
	}

	fixRequest := application.DecideFindingFixRequest{
		FindingID: finding.ID, OperationKey: "finding-fix-converge-0001",
		Reason:    "fresh remediation output confirms the finding is fixed",
		DecidedBy: "remediation_operator",
	}
	type fixOutcome struct {
		result application.DecideFindingFixResult
		err    error
	}
	fixResults := make(chan fixOutcome, 8)
	start = make(chan struct{})
	workers = sync.WaitGroup{}
	for index := range 8 {
		workers.Add(1)
		go func(current int) {
			defer workers.Done()
			<-start
			result, err := application.NewFindingReportService(stores[current%2]).
				Fix(ctx, fixRequest)
			fixResults <- fixOutcome{result: result, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(fixResults)
	fixCreated := 0
	var fixID string
	for outcome := range fixResults {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if !outcome.result.Replayed {
			fixCreated++
		}
		if fixID == "" {
			fixID = outcome.result.Fix.ID
		} else if outcome.result.Fix.ID != fixID {
			t.Fatalf("fix replay changed identity: %#v", outcome.result)
		}
	}
	if fixCreated != 1 {
		t.Fatalf("expected one fix creation, got %d", fixCreated)
	}
	lateArtifact := captureFindingValidationArtifact(t, ctx, st, run,
		"echo too-late-remediation")
	_, err = service.AttachRemediationEvidence(ctx,
		application.AttachFindingRemediationEvidenceRequest{
			FindingID: finding.ID, ArtifactID: lateArtifact.ID,
			OperationKey: "finding-remediation-after-fix-0001",
			AttachedBy:   "remediation_operator", Note: "must not change frozen fix evidence",
		})
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("post-fix remediation code=%s err=%v", apperror.CodeOf(err), err)
	}

	verified, err := service.VerifyArtifactEvidence(ctx, finding.ID)
	if err != nil || verified.Status != domain.FindingStatusFixed ||
		verified.ValidationID != validation.Validation.ID ||
		verified.AcceptanceID != acceptanceID || verified.FixID != fixID ||
		verified.ArtifactEvidenceCount != 1 ||
		verified.RemediationEvidenceCount != 1 ||
		verified.ArtifactEvidenceDigest == "" || verified.RemediationEvidenceDigest == "" {
		t.Fatalf("fixed finding verification failed: %#v err=%v", verified, err)
	}
	loaded, err := second.GetFindingReport(ctx, report.ID)
	if err != nil || loaded.ProjectionDigest != projectionDigest ||
		len(loaded.Findings) != 1 || loaded.Findings[0].EffectiveStatus() != domain.FindingStatusFixed ||
		loaded.Findings[0].Acceptance == nil || loaded.Findings[0].Fix == nil ||
		len(loaded.Findings[0].RemediationEvidence) != 1 {
		t.Fatalf("remediation projection drifted: %#v err=%v", loaded, err)
	}
	if digest, err := domain.FindingReportProjectionDigest(loaded); err != nil ||
		digest != projectionDigest {
		t.Fatalf("source projection changed after lifecycle overlays: digest=%s err=%v", digest, err)
	}
	for table, want := range map[string]int{
		"finding_acceptance_decisions":            1,
		"finding_acceptance_operations":           1,
		"finding_remediation_evidence":            1,
		"finding_remediation_evidence_operations": 1,
		"finding_fix_decisions":                   1,
		"finding_fix_operations":                  1,
	} {
		var count int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil || count != want {
			t.Fatalf("unexpected %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	var rawKeys int
	if err := st.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM finding_acceptance_operations
			WHERE operation_key_digest = ? OR request_fingerprint = ?) +
		(SELECT COUNT(*) FROM finding_remediation_evidence_operations
			WHERE operation_key_digest = ? OR request_fingerprint = ?) +
		(SELECT COUNT(*) FROM finding_fix_operations
			WHERE operation_key_digest = ? OR request_fingerprint = ?)`,
		acceptanceRequest.OperationKey, acceptanceRequest.OperationKey,
		remediationRequest.OperationKey, remediationRequest.OperationKey,
		fixRequest.OperationKey, fixRequest.OperationKey).Scan(&rawKeys); err != nil || rawKeys != 0 {
		t.Fatalf("raw lifecycle operation keys were stored: count=%d err=%v", rawKeys, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.FindingAcceptedEvent) != 1 ||
		countRunEventType(timeline, events.FindingRemediationEvidenceAttachedEvent) != 1 ||
		countRunEventType(timeline, events.FindingFixedEvent) != 1 {
		t.Fatalf("finding remediation timeline drifted: err=%v", err)
	}
	for _, event := range timeline {
		if event.Type != events.FindingAcceptedEvent &&
			event.Type != events.FindingRemediationEvidenceAttachedEvent &&
			event.Type != events.FindingFixedEvent {
			continue
		}
		for _, secret := range []string{acceptanceRequest.Reason, remediationRequest.Note,
			fixRequest.Reason, remediationArtifact.Content} {
			if strings.Contains(event.PayloadJSON, secret) {
				t.Fatalf("finding remediation event leaked narrative/content: %s", event.PayloadJSON)
			}
		}
	}
	for _, statement := range []string{
		`UPDATE finding_acceptance_decisions SET reason = 'changed' WHERE id = '` + acceptanceID + `'`,
		`DELETE FROM finding_acceptance_operations`,
		`UPDATE finding_remediation_evidence SET note = 'changed' WHERE id = '` + remediationEvidenceID + `'`,
		`DELETE FROM finding_remediation_evidence_operations`,
		`UPDATE finding_fix_decisions SET reason = 'changed' WHERE id = '` + fixID + `'`,
		`DELETE FROM finding_fix_operations`,
	} {
		if _, err := st.db.ExecContext(ctx, statement); err == nil {
			t.Fatalf("immutable remediation fact accepted %q", statement)
		}
	}
}

func TestSchemaV36ValidatedFindingSurvivesRemediationMigration(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "finding-remediation-v36.db")
	st, run, _ := createReadOnlyFanoutFixture(t, "seed.db", 1, 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-remediation-v36-plan", "finding-remediation-v36-execution")
	report, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewFindingReportService(st)
	blob := captureFindingValidationArtifact(t, ctx, st, run, "echo v36-validation")
	if _, err := service.AttachArtifactEvidence(ctx,
		application.AttachFindingArtifactEvidenceRequest{
			FindingID: report.Findings[0].ID, ArtifactID: blob.ID,
			OperationKey: "finding-v36-evidence-0001", AttachedBy: "migration_operator",
			Note: "preserved validation evidence",
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DecideValidation(ctx,
		application.DecideFindingValidationRequest{
			FindingID: report.Findings[0].ID, OperationKey: "finding-v36-validation-0001",
			Status: domain.FindingStatusValidated, Reason: "preserved validated decision",
			DecidedBy: "migration_operator",
		}); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 36)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v36 did not upgrade: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetFindingReport(ctx, report.ID)
	if err != nil || loaded.ProjectionDigest != report.ProjectionDigest ||
		len(loaded.Findings) != 1 ||
		loaded.Findings[0].EffectiveStatus() != domain.FindingStatusValidated ||
		loaded.Findings[0].Acceptance != nil ||
		len(loaded.Findings[0].RemediationEvidence) != 0 || loaded.Findings[0].Fix != nil {
		t.Fatalf("v36 validation did not survive v37: %#v err=%v", loaded, err)
	}
	for _, table := range []string{
		"finding_acceptance_decisions", "finding_acceptance_operations",
		"finding_remediation_evidence", "finding_remediation_evidence_operations",
		"finding_fix_decisions", "finding_fix_operations",
	} {
		var count int
		if err := upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration synthesized %s rows=%d err=%v", table, count, err)
		}
	}
}
