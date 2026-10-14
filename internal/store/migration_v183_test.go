package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
)

func TestSchemaV183PreservesHistoricalCandidateReceiptsAndPendingRecovery(t *testing.T) {
	// Includes the v131 command-history repair; released v131 compatibility is tested separately.
	if got := migrationPlanDigest(migrationPlan()[:182]); got != "d3f0e75c895fb0abaaed6c1a3a88c440de5672601da40d20e1a512118becab2e" {
		t.Fatalf("historical migration prefix changed: %s", got)
	}
	home := t.TempDir()
	path := filepath.Join(home, "v182.db")
	st := openHistoricalTestDatabase(t, path, 182)
	objects, err := skills.NewLocalPackageObjectStore(home)
	if err != nil {
		t.Fatal(err)
	}
	old, oldReview := seedReviewedCandidateV183(t, st, "historical-imported")
	pending, pendingReview := seedReviewedCandidateV183(t, st, "historical-pending")
	oldRequest := candidateRequestV183(old, "historical-candidate-import-key")
	pendingRequest := candidateRequestV183(pending, "historical-candidate-pending-key")
	oldInstall := seedLegacyCandidateIntentV183(t, st, old, oldRequest)
	seedLegacyCandidateIntentV183(t, st, pending, pendingRequest)
	raw, _ := skills.BuildUnsignedPackage(old.Manifest, []byte(old.Content))
	object, err := objects.Put(t.Context(), raw, skills.DescriptorForInstallation(oldInstall))
	if err != nil {
		t.Fatal(err)
	}
	result, err := skills.NewPackageInstallResult(oldInstall, object, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompletePackageInstallation(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	receipt := skills.SkillCandidateImport{ID: "historical-candidate-receipt", ProtocolVersion: skills.SkillCandidateImportProtocolVersion,
		OperationKeyDigest: runmutation.Fingerprint("skill_candidate_import_operation.v1", oldRequest.OperationKey),
		CandidateID:        old.ID, CandidateFingerprint: old.CandidateFingerprint, ReviewFingerprint: oldReview.ReviewFingerprint,
		InstallationID: oldInstall.ID, InstallationFingerprint: oldInstall.InstallationFingerprint,
		ImportedBy: oldRequest.ImportedBy, CreatedAt: time.Now().UTC()}
	receipt.RequestFingerprint = skills.SkillCandidateImportRequestFingerprint(receipt)
	receipt.ImportFingerprint = skills.SkillCandidateImportFingerprint(receipt)
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(t.Context(), `INSERT INTO skill_candidate_imports
		(id, protocol_version, operation_key_digest, request_fingerprint, candidate_id, candidate_fingerprint,
		review_fingerprint, installation_id, installation_fingerprint, imported_by, import_fingerprint, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, receipt.ID, receipt.ProtocolVersion, receipt.OperationKeyDigest,
		receipt.RequestFingerprint, receipt.CandidateID, receipt.CandidateFingerprint, receipt.ReviewFingerprint,
		receipt.InstallationID, receipt.InstallationFingerprint, receipt.ImportedBy, receipt.ImportFingerprint, ts(receipt.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	beforeSchema := schemaObjectsV183(t, st)
	beforeLedger, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var beforeRow string
	const oldRow = `SELECT json_array(rowid, id, protocol_version, operation_key_digest, request_fingerprint,
		candidate_id, candidate_fingerprint, review_fingerprint, installation_id, installation_fingerprint,
		imported_by, import_fingerprint, created_at) FROM skill_candidate_imports WHERE id = ?`
	if err := st.db.QueryRowContext(t.Context(), oldRow, receipt.ID).Scan(&beforeRow); err != nil {
		t.Fatal(err)
	}
	// Compare v182 with exactly v183 before applying later authorized migrations.
	if err := st.applyMigration(t.Context(), migrationPlan()[182]); err != nil {
		t.Fatal(err)
	}
	afterSchema := schemaObjectsV183(t, st)
	for name, definition := range beforeSchema {
		if afterSchema[name] != definition {
			t.Fatalf("v183 rewrote historical schema object %s", name)
		}
	}
	afterLedger, err := st.loadAppliedMigrations(t.Context())
	if err != nil || len(afterLedger) != len(beforeLedger)+1 {
		t.Fatalf("migration ledger changed: %v", err)
	}
	for version, entry := range beforeLedger {
		if afterLedger[version] != entry {
			t.Fatalf("historical migration %d changed", version)
		}
	}
	var afterRow string
	if err := st.db.QueryRowContext(t.Context(), oldRow, receipt.ID).Scan(&afterRow); err != nil || afterRow != beforeRow {
		t.Fatalf("historical receipt bytes or rowid changed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := candidateServiceV183(t, st, objects)
	got, err := service.Import(t.Context(), oldRequest)
	if err != nil || !got.Replayed || got.Installation != nil || !reflect.DeepEqual(*got.Record.Import, receipt) {
		t.Fatalf("old receipt replay changed meaning: %+v err=%v", got, err)
	}
	recovered, err := service.Import(t.Context(), pendingRequest)
	if err != nil || !recovered.RecoveredPending || recovered.Installation != nil ||
		recovered.Record.Import.ProtocolVersion != skills.SkillCandidateImportProtocolVersion ||
		recovered.Record.Import.ReviewFingerprint != pendingReview.ReviewFingerprint {
		t.Fatalf("historical pending recovery=%+v err=%v", recovered, err)
	}
	assertTableCount(t, st, "skill_candidate_imports", 2)
	assertTableCount(t, st, "plugin_installations", 0)
	assertTableCount(t, st, "skill_candidate_plugin_imports", 0)
	newCandidate, _ := seedReviewedCandidateV183(t, st, "post-upgrade-plugin")
	installed, err := service.Import(t.Context(), candidateRequestV183(newCandidate, "post-upgrade-plugin-operation"))
	if err != nil || installed.Installation == nil || installed.Record.Import.ProtocolVersion != skills.SkillCandidatePluginImportProtocolVersion {
		t.Fatalf("post-upgrade install=%+v err=%v", installed, err)
	}
	assertTableCount(t, st, "skill_package_installations", 2)
	assertTableCount(t, st, "plugin_installations", 1)
	assertTableCount(t, st, "skill_candidate_plugin_imports", 1)
}

type failedCandidateReceiptV183 struct {
	*SQLiteStore
	captured *skills.SkillCandidateImport
}

func (s failedCandidateReceiptV183) CreateSkillCandidateImport(_ context.Context, value skills.SkillCandidateImport) (skills.SkillCandidateRecord, bool, error) {
	if s.captured != nil {
		*s.captured = value
	}
	return skills.SkillCandidateRecord{}, false, errors.New("simulated interruption after Plugin commit")
}

func TestSkillCandidatePluginReceiptRecoversAfterRestartAndDoesNotReenable(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "fresh.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	objects, _ := skills.NewLocalPackageObjectStore(home)
	builtins, _ := skills.BuiltinRegistry()
	candidate, _ := seedReviewedCandidateV183(t, st, "fresh-plugin")
	request := candidateRequestV183(candidate, "fresh-candidate-plugin-operation")
	registry := application.NewSkillPackageRegistryService(st, objects, builtins)
	failed := application.NewSkillCandidateService(failedCandidateReceiptV183{SQLiteStore: st}, registry)
	if _, err := failed.Import(t.Context(), request); err == nil {
		t.Fatal("interrupted receipt was reported successful")
	}
	assertTableCount(t, st, "plugin_installations", 1)
	assertTableCount(t, st, "skill_candidate_plugin_imports", 0)
	assertTableCount(t, st, "skill_package_installations", 0)
	assertTableCount(t, st, "skill_package_install_operations", 0)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := candidateServiceV183(t, st, objects)
	got, err := service.Import(t.Context(), request)
	if err != nil || !got.Replayed || !got.RecoveredPending || got.Installation == nil || got.Record.Import == nil {
		t.Fatalf("recovery=%+v err=%v", got, err)
	}
	installation := *got.Installation
	receipt := *got.Record.Import
	if installation.State != plugins.StateStaged || len(installation.EnabledCapabilities) != 0 ||
		receipt.ProtocolVersion != skills.SkillCandidatePluginImportProtocolVersion ||
		receipt.InstallationID != installation.ID || receipt.InstallationGeneration != installation.Generation ||
		receipt.InstallationFingerprint != plugins.InstallationFingerprint(installation) ||
		installation.Source.URI != candidate.ID || installation.Source.Kind != "catalog" ||
		installation.Snapshot.Legacy.PackageFingerprint != candidate.PackageFingerprint {
		t.Fatalf("candidate/Plugin receipt binding changed: %+v", got)
	}
	raw, _ := skills.BuildUnsignedPackage(candidate.Manifest, []byte(candidate.Content))
	retained, err := st.LoadPluginObject(t.Context(), installation.ID)
	if err != nil || !bytes.Equal(raw, retained) {
		t.Fatal("candidate archive was rewritten", err)
	}
	for _, mutate := range []func(*application.ImportSkillCandidateRequest){
		func(r *application.ImportSkillCandidateRequest) { r.OperationKey += "-changed" },
		func(r *application.ImportSkillCandidateRequest) { r.ImportedBy = "other-human" },
		func(r *application.ImportSkillCandidateRequest) {
			r.CandidateFingerprint = runmutation.Fingerprint("wrong-candidate")
		},
	} {
		changed := request
		mutate(&changed)
		if _, err := service.Import(t.Context(), changed); err == nil {
			t.Fatal("changed candidate request replayed")
		}
	}
	pluginService, _ := plugins.NewService(st)
	for _, action := range []plugins.ReviewAction{plugins.ReviewApprove, plugins.ReviewEnable, plugins.ReviewDisable, plugins.ReviewRevoke} {
		installation, err = pluginService.Review(t.Context(), installation.ID, plugins.ReviewRequest{Action: action,
			ExpectedPackageFingerprint: installation.PackageFingerprint, ExpectedGeneration: installation.Generation,
			Capabilities: []plugins.Capability{plugins.CapabilitySkills}, ConfirmUntrusted: true, ReviewedBy: "reviewer"})
		if err != nil {
			t.Fatal(err)
		}
		got, err = service.Import(t.Context(), request)
		if err != nil || !got.Replayed || got.RecoveredPending || got.Installation.State != installation.State ||
			got.Installation.Generation != installation.Generation || !reflect.DeepEqual(*got.Record.Import, receipt) {
			t.Fatalf("replay changed reviewed lifecycle after %s: %+v err=%v", action, got, err)
		}
	}
	assertTableCount(t, st, "plugin_objects", 1)
	assertTableCount(t, st, "plugin_installations", 1)
	assertTableCount(t, st, "skill_candidate_plugin_imports", 1)
	assertTableCount(t, st, "skill_package_installations", 0)
	assertTableCount(t, st, "skill_candidate_imports", 0)
}

func candidateServiceV183(t *testing.T, st *SQLiteStore, objects skills.PackageObjectStore) *application.SkillCandidateService {
	t.Helper()
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return application.NewSkillCandidateService(st, application.NewSkillPackageRegistryService(st, objects, builtins))
}

func candidateRequestV183(candidate skills.SkillCandidate, key string) application.ImportSkillCandidateRequest {
	return application.ImportSkillCandidateRequest{CandidateID: candidate.ID, CandidateFingerprint: candidate.CandidateFingerprint,
		OperationKey: key, ImportedBy: "reviewer", ConfirmUntrusted: true}
}

// Uses only the v182-supported tables and guards. Historical seeding never
// disables triggers or edits migration checksums to mimic an old database.
func seedReviewedCandidateV183(t *testing.T, st *SQLiteStore, name string) (skills.SkillCandidate, skills.SkillCandidateReview) {
	t.Helper()
	ctx := t.Context()
	workspace := WorkspaceRecord{ID: idgen.New("workspace"), Name: name, RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	service := application.NewRunService(st)
	_, run, err := service.Create(ctx, application.CreateRunRequest{Goal: "review reusable instructions", Profile: "code",
		Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 8, MaxTokens: 8192}})
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := st.RegisterRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	candidate := fixtureSkillCandidate(t, run, root, workspace.ID, name)
	seedSkillCandidateInvocation(t, st, run, workspace.ID, candidate.InvocationID, 1)
	if _, _, err := st.CreateSkillCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	review := skills.SkillCandidateReview{ID: idgen.New("review"), ProtocolVersion: skills.SkillCandidateReviewProtocolVersion,
		OperationKeyDigest: runmutation.Fingerprint("v183-review", name), CandidateID: candidate.ID,
		CandidateFingerprint: candidate.CandidateFingerprint, Decision: skills.SkillCandidateReviewApprove,
		Reviewer: "reviewer", CreatedAt: time.Now().UTC()}
	review.RequestFingerprint = skills.SkillCandidateReviewRequestFingerprint(review)
	review.ReviewFingerprint = skills.SkillCandidateReviewFingerprint(review)
	if err := review.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO skill_candidate_reviews
		(id, protocol_version, operation_key_digest, request_fingerprint, candidate_id,
		candidate_fingerprint, decision, reason, reviewer, review_fingerprint, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, review.ID, review.ProtocolVersion, review.OperationKeyDigest,
		review.RequestFingerprint, review.CandidateID, review.CandidateFingerprint, review.Decision, review.Reason,
		review.Reviewer, review.ReviewFingerprint, ts(review.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	return candidate, review
}

func seedLegacyCandidateIntentV183(t *testing.T, st *SQLiteStore, candidate skills.SkillCandidate,
	request application.ImportSkillCandidateRequest) skills.PackageInstallation {
	t.Helper()
	raw, err := skills.BuildUnsignedPackage(candidate.Manifest, []byte(candidate.Content))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := skills.ParsePackage(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := runmutation.Fingerprint("skill_candidate_import_operation.v1", request.OperationKey)
	key := runmutation.Fingerprint("skill_package_install_operation.v1", "candidate-"+digest[:48])
	installation, err := skills.NewPackageInstallation(idgen.New("historical-install"), parsed,
		domain.ExecutionSurfaceCode, key, request.ImportedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyskill.Insert(t.Context(), st.db, installation); err != nil {
		t.Fatal(err)
	}
	return installation
}

func schemaObjectsV183(t *testing.T, st *SQLiteStore) map[string]string {
	t.Helper()
	rows, err := st.db.QueryContext(t.Context(), `SELECT type || ':' || name, COALESCE(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSchemaV183RejectsCandidatePluginSubstitution(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "binding.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objects, _ := skills.NewLocalPackageObjectStore(t.TempDir())
	builtins, _ := skills.BuiltinRegistry()
	candidate, _ := seedReviewedCandidateV183(t, st, "exact-candidate")
	other, otherReview := seedReviewedCandidateV183(t, st, "different-candidate")
	var receipt skills.SkillCandidateImport
	failed := application.NewSkillCandidateService(failedCandidateReceiptV183{SQLiteStore: st, captured: &receipt},
		application.NewSkillPackageRegistryService(st, objects, builtins))
	if _, err := failed.Import(t.Context(), candidateRequestV183(candidate, "candidate-binding-operation")); err == nil || receipt.ID == "" {
		t.Fatal("failed to retain interruption boundary")
	}
	for _, test := range []struct {
		name   string
		change func(*skills.SkillCandidateImport)
	}{
		{"missing installation", func(r *skills.SkillCandidateImport) { r.InstallationID = "missing-plugin" }},
		{"changed generation", func(r *skills.SkillCandidateImport) { r.InstallationGeneration++ }},
		{"changed archive", func(r *skills.SkillCandidateImport) { r.ArchiveSHA256 = runmutation.Fingerprint("other-archive") }},
		{"changed package", func(r *skills.SkillCandidateImport) { r.PackageFingerprint = runmutation.Fingerprint("other-package") }},
		{"changed lifecycle", func(r *skills.SkillCandidateImport) {
			r.InstallationFingerprint = runmutation.Fingerprint("other-installation")
		}},
		{"changed importer", func(r *skills.SkillCandidateImport) { r.ImportedBy = "other-reviewer" }},
		{"other approved candidate", func(r *skills.SkillCandidateImport) {
			r.CandidateID = other.ID
			r.CandidateFingerprint = other.CandidateFingerprint
			r.ReviewFingerprint = otherReview.ReviewFingerprint
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := receipt
			test.change(&changed)
			changed.RequestFingerprint = skills.SkillCandidateImportRequestFingerprint(changed)
			changed.ImportFingerprint = skills.SkillCandidateImportFingerprint(changed)
			if err := changed.Validate(); err != nil {
				t.Fatalf("negative fixture invalid before binding check: %v", err)
			}
			if _, _, err := st.CreateSkillCandidateImport(t.Context(), changed); err == nil {
				t.Fatal("accepted substituted Plugin receipt")
			}
		})
	}
	assertTableCount(t, st, "skill_candidate_plugin_imports", 0)
	if _, _, err := st.CreateSkillCandidateImport(t.Context(), receipt); err != nil {
		t.Fatal("valid exact receipt rejected after negative attempts", err)
	}
	changed := receipt
	changed.InstallationID = "other-valid-plugin-id"
	changed.ImportFingerprint = skills.SkillCandidateImportFingerprint(changed)
	if _, _, err := st.CreateSkillCandidateImport(t.Context(), changed); err == nil {
		t.Fatal("existing receipt accepted a different Plugin identity")
	}
	for _, query := range []string{
		"UPDATE skill_candidate_plugin_imports SET installation_generation = installation_generation + 1 WHERE id = ?",
		"DELETE FROM skill_candidate_plugin_imports WHERE id = ?",
	} {
		if _, err := st.db.ExecContext(t.Context(), query, receipt.ID); err == nil {
			t.Fatal("immutable receipt changed", query)
		}
	}
	assertTableCount(t, st, "skill_candidate_plugin_imports", 1)
}
