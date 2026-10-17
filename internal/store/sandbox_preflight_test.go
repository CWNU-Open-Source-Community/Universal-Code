package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/sandbox"
)

func TestSandboxPreflightLedgerIsDisabledOpaqueAndImmutable(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	lifecycle := createSandboxLifecycleStoreFixture(t, ctx, st, run.ID)
	service := application.NewSandboxManifestService(st, policy.NewDefaultChecker())
	preflight, err := service.PrepareDisabledPreflight(ctx,
		application.PrepareSandboxPreflightRequest{
			ExecutionID: lifecycle.Execution.ID, Manifest: sandboxStoreTestManifest(),
			OperationKey: "store-preflight-create", RequestedBy: "lifecycle_store_test",
		})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.BackendEnabled || preflight.ExecutionAuthorized ||
		preflight.ArtifactCommitAuthorized || preflight.Handshake.Available ||
		preflight.Handshake.ContainerIdentity.Bound || preflight.OutputPlan.ExportEnabled {
		t.Fatalf("stored Sandbox preflight widened authority: %#v", preflight)
	}
	var checks, slots int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandbox_backend_preflight_checks
		WHERE preflight_id = ?`, preflight.ID).Scan(&checks); err != nil || checks != 16 {
		t.Fatalf("stored backend check count=%d err=%v", checks, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandbox_output_export_slots
		WHERE preflight_id = ?`, preflight.ID).Scan(&slots); err != nil || slots != 2 {
		t.Fatalf("stored output slot count=%d err=%v", slots, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_disabled_preflights
		SET backend_available = 1 WHERE id = ?`, preflight.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Sandbox preflight root was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_backend_preflight_checks
		WHERE preflight_id = ? AND ordinal = 1`, preflight.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Sandbox preflight check was deletable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_output_export_slots
		SET artifact_commit_authorized = 1 WHERE preflight_id = ? AND ordinal = 1`,
		preflight.ID); err == nil || !strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Sandbox output slot was mutable: %v", err)
	}

	rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(sandbox_disabled_preflights)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "manifest_json", "executable", "arguments_json", "command_text", "raw_path",
			"output_path", "workspace_root", "lease_id", "owner_id", "container_id":
			t.Fatalf("schema v51 persists private Sandbox data in column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var schemaSQL string
	if err := st.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 'sandbox_backend_preflight_checks'`).Scan(&schemaSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(schemaSQL, "required = 1 AND verified = 0") {
		t.Fatalf("schema v51 does not fail closed for backend checks: %s", schemaSQL)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range timeline {
		if event.Type == events.SandboxPreflightRecordedEvent &&
			(strings.Contains(event.PayloadJSON, root) ||
				strings.Contains(event.PayloadJSON, preflight.OutputPlan.Fingerprint)) {
			t.Fatalf("Sandbox preflight event leaked private data: %#v", event)
		}
	}
}

func TestSandboxPreflightConcurrentReplayConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cyberagent.db")
	st1, run, _ := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = st1.Close() })
	lifecycle := createSandboxLifecycleStoreFixture(t, ctx, st1, run.ID)
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	services := []*application.SandboxManifestService{
		application.NewSandboxManifestService(st1, policy.NewDefaultChecker()),
		application.NewSandboxManifestService(st2, policy.NewDefaultChecker()),
	}
	request := application.PrepareSandboxPreflightRequest{
		ExecutionID: lifecycle.Execution.ID, Manifest: sandboxStoreTestManifest(),
		OperationKey: "store-preflight-concurrent", RequestedBy: "lifecycle_store_test",
	}
	start := make(chan struct{})
	results := make([]sandbox.DisabledPreflight, len(services))
	errorsFound := make([]error, len(services))
	var group sync.WaitGroup
	for index := range services {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], errorsFound[index] = services[index].PrepareDisabledPreflight(ctx, request)
		}(index)
	}
	close(start)
	group.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil ||
		results[0].ID == "" || results[0].ID != results[1].ID {
		t.Fatalf("concurrent Sandbox preflight replay diverged: results=%#v errors=%v",
			results, errorsFound)
	}
	if results[0].Replayed == results[1].Replayed {
		t.Fatalf("expected one Sandbox preflight create and one replay: %#v", results)
	}
	values, err := st1.ListSandboxDisabledPreflights(ctx, run.ID, 10)
	if err != nil || len(values) != 1 {
		t.Fatalf("concurrent Sandbox preflight created duplicates: %#v err=%v", values, err)
	}
}

func TestSchemaV50UpgradeAddsSandboxPreflightWithoutLosingLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v50.db")
	st, run, _ := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	lifecycle := createSandboxLifecycleStoreFixture(t, ctx, st, run.ID)
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, path, 50)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v50 did not upgrade to latest: version=%d err=%v", version, err)
	}
	loaded, err := st.GetSandboxDisabledExecution(ctx, lifecycle.Execution.ID)
	if err != nil || loaded.Execution.ID != lifecycle.Execution.ID {
		t.Fatalf("schema v50 lifecycle was not preserved: %#v err=%v", loaded, err)
	}
	var table string
	if err := st.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'sandbox_disabled_preflights'`).Scan(&table); err != nil ||
		table != "sandbox_disabled_preflights" {
		t.Fatalf("schema v51 Sandbox preflight ledger is missing: %q err=%v", table, err)
	}
}
