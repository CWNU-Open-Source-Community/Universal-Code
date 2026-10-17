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

func TestDockerRuntimeInputApplicationLedgerFencesStaleWorkersAndPersistsMetadataOnly(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	projection, operation := prepareDockerRuntimeInputProjectionStoreFixture(t, ctx, st,
		run.ID, root, "runtime-input-application-ledger")
	projection, replayed, err := st.CreateDockerRuntimeInputProjectionPlan(ctx,
		projection, operation)
	if err != nil || replayed {
		t.Fatalf("create projection: replayed=%t err=%v", replayed, err)
	}
	intent, request := newDockerRuntimeInputApplicationStoreFixture(t, ctx, st, root, projection)
	acquired, err := st.BeginDockerRuntimeInputApplication(ctx, intent,
		"runtime_input_application_owner", time.Minute)
	if err != nil || acquired.Record.Lease.Generation != 1 || acquired.Replayed || acquired.TookOver {
		t.Fatalf("begin application: %#v err=%v", acquired, err)
	}
	if _, err := st.BeginDockerRuntimeInputApplication(ctx, intent,
		"second_runtime_input_owner", time.Minute); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("active application lease was not exclusive: %v", err)
	}

	failed, err := st.RecordDockerRuntimeInputApplicationFailure(ctx, intent.ID,
		acquired.Record.Lease, sandbox.DockerRuntimeInputApplicationErrorConnection,
		time.Now().UTC())
	if err != nil || failed.Lease.Status != sandbox.DockerRuntimeInputApplicationLeaseReleased ||
		len(failed.Failures) != 1 {
		t.Fatalf("record application failure: %#v err=%v", failed, err)
	}
	resumed, err := st.AcquireDockerRuntimeInputApplication(ctx, intent.ID,
		intent.RequestedBy, "resumed_runtime_input_owner", time.Minute)
	if err != nil || resumed.Record.Lease.Generation != 2 || resumed.TookOver {
		t.Fatalf("resume released application: %#v err=%v", resumed, err)
	}
	staleResult, err := sandbox.NewDockerRuntimeInputApplicationResult(
		"runtime-input-stale-result", intent, acquired.Record.Lease, request,
		strings.Repeat("a", 64), 3+5*len(request.Mounts), 1+4*len(request.Mounts), 0,
		time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteDockerRuntimeInputApplication(ctx, staleResult,
		acquired.Record.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale application worker committed: %v", err)
	}
	result, err := sandbox.NewDockerRuntimeInputApplicationResult(
		"runtime-input-result", intent, resumed.Record.Lease, request,
		strings.Repeat("b", 64), 3+5*len(request.Mounts), 1+4*len(request.Mounts), 0,
		time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	completed, replayed, err := st.CompleteDockerRuntimeInputApplication(ctx, result,
		resumed.Record.Lease)
	if err != nil || replayed || completed.Result == nil ||
		completed.Lease.Status != sandbox.DockerRuntimeInputApplicationLeaseReleased {
		t.Fatalf("complete application: %#v replayed=%t err=%v", completed, replayed, err)
	}
	replayedAcquisition, err := st.BeginDockerRuntimeInputApplication(ctx, intent,
		"third_runtime_input_owner", time.Minute)
	if err != nil || !replayedAcquisition.Replayed || replayedAcquisition.Record.Result == nil {
		t.Fatalf("application replay failed: %#v err=%v", replayedAcquisition, err)
	}

	loaded, err := st.GetDockerRuntimeInputApplication(ctx, intent.ID)
	if err != nil || loaded.Result == nil || loaded.Result.ResultFingerprint != result.ResultFingerprint {
		t.Fatalf("load application: %#v err=%v", loaded, err)
	}
	byProjection, found, err := st.GetDockerRuntimeInputApplicationByProjection(ctx,
		projection.ID)
	if err != nil || !found || byProjection.Intent.ID != intent.ID {
		t.Fatalf("load application by projection: %#v found=%t err=%v",
			byProjection, found, err)
	}
	byOperation, found, err := st.GetDockerRuntimeInputApplicationByOperation(ctx,
		intent.OperationKeyDigest)
	if err != nil || !found || byOperation.Intent.ID != intent.ID {
		t.Fatalf("load application by operation: %#v found=%t err=%v",
			byOperation, found, err)
	}
	if _, _, err := st.GetDockerRuntimeInputApplicationByOperation(ctx,
		strings.Repeat("z", 64)); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("non-hex application operation digest was accepted: %v", err)
	}
	listed, err := st.ListDockerRuntimeInputApplications(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Intent.ID != intent.ID {
		t.Fatalf("list applications: %#v err=%v", listed, err)
	}

	for _, mutation := range []string{
		`UPDATE sandbox_docker_runtime_input_application_intents SET container_start_authorized = 1 WHERE id = ?`,
		`UPDATE sandbox_docker_runtime_input_application_results SET process_executed = 1 WHERE intent_id = ?`,
		`DELETE FROM sandbox_docker_runtime_input_application_failures WHERE intent_id = ?`,
	} {
		if _, err := st.db.ExecContext(ctx, mutation, intent.ID); err == nil ||
			!strings.Contains(err.Error(), "cannot be") {
			t.Fatalf("application mutation was not rejected: %s: %v", mutation, err)
		}
	}
	assertDockerRuntimeInputApplicationSchemaPrivacy(t, ctx, st)
	var persisted string
	if err := st.db.QueryRowContext(ctx, `SELECT intent.protocol_version || '|' ||
		intent.intent_fingerprint || '|' || result.target_container_fingerprint || '|' ||
		result.target_inspection_fingerprint FROM sandbox_docker_runtime_input_application_intents intent
		JOIN sandbox_docker_runtime_input_application_results result ON result.intent_id = intent.id
		WHERE intent.id = ?`, intent.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, "/workspace", "/output", "main.txt",
		"source payload", "cyberagent-runtime-"} {
		if strings.Contains(persisted, private) {
			t.Fatalf("application ledger leaked %q: %s", private, persisted)
		}
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPrepared, foundFailed, foundCompleted := false, false, false
	for _, event := range timeline {
		switch event.Type {
		case events.SandboxDockerRuntimeInputApplicationPreparedEvent:
			foundPrepared = true
		case events.SandboxDockerRuntimeInputApplicationFailedEvent:
			foundFailed = true
		case events.SandboxDockerRuntimeInputApplicationCompletedEvent:
			foundCompleted = true
		}
	}
	if !foundPrepared || !foundFailed || !foundCompleted {
		t.Fatalf("application lifecycle events missing: prepared=%t failed=%t completed=%t",
			foundPrepared, foundFailed, foundCompleted)
	}
}

func TestSchemaV61PreservesV60ProjectionWithoutFabricatingApplication(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-runtime-input-application-v60.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	projection, operation := prepareDockerRuntimeInputProjectionStoreFixture(t, ctx, st,
		run.ID, root, "runtime-input-application-upgrade")
	if _, _, err := st.CreateDockerRuntimeInputProjectionPlan(ctx, projection, operation); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 60)
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
		t.Fatalf("schema v61 upgrade version=%d err=%v", version, err)
	}
	values, err := upgraded.ListDockerRuntimeInputApplications(ctx, run.ID, 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("schema v61 fabricated applications: %#v err=%v", values, err)
	}
	if _, found, err := upgraded.GetDockerRuntimeInputApplicationByProjection(ctx,
		projection.ID); err != nil || found {
		t.Fatalf("schema v61 fabricated projection application: found=%t err=%v", found, err)
	}
}

func newDockerRuntimeInputApplicationStoreFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, root string, projection sandbox.DockerRuntimeInputProjectionPlan,
) (sandbox.DockerRuntimeInputApplicationIntent, sandbox.DockerRuntimeInputApplicationRequest) {
	t.Helper()
	intent, request, _ := newDockerRuntimeInputApplicationStoreFixtureFull(
		t, ctx, st, root, projection)
	return intent, request
}

func newDockerRuntimeInputApplicationStoreFixtureFull(t *testing.T, ctx context.Context,
	st *SQLiteStore, root string, projection sandbox.DockerRuntimeInputProjectionPlan,
) (sandbox.DockerRuntimeInputApplicationIntent, sandbox.DockerRuntimeInputApplicationRequest,
	sandbox.DockerContainerWriteRequest) {
	t.Helper()
	manifest := sandboxStoreTestManifest()
	manifest.Backend = sandbox.BackendDocker
	manifest.Command.Executable = "private-build-command"
	manifest.Mounts = []sandbox.Mount{
		{Source: "src", Target: "/workspace", Access: sandbox.MountReadOnly},
		{Source: "output", Target: "/output", Access: sandbox.MountReadWrite},
	}
	manifest.Output.Paths = []string{"/output/report.json"}
	containerPlan, err := st.GetDockerContainerPlan(ctx, projection.ContainerPlanID)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := st.GetDockerObservation(ctx, containerPlan.ObservationID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := sandbox.CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeRequest, err := sandbox.NewDockerContainerWriteRequest(ctx, root, spec)
	if err != nil {
		t.Fatal(err)
	}
	bundle := newStoreRuntimeInputBundle(t, manifest)
	compilation, err := sandbox.CompileDockerRuntimeInputProjectionBundle(ctx, manifest,
		bundle, projection.HandoffFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	intent, err := sandbox.NewDockerRuntimeInputApplicationIntent(
		idgen.New("sandbox-docker-runtime-input-application"),
		runmutation.Fingerprint(sandbox.DockerRuntimeInputApplicationIntentProtocolVersion,
			projection.ID, "apply"), projection, endpoint, true, true,
		projection.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if intent.ProjectionFingerprint != projection.ProjectionFingerprint ||
		writeRequest.Spec.SpecFingerprint != intent.SpecFingerprint ||
		compilation.BundleReportFingerprint != projection.BundleReportFingerprint ||
		compilation.BundleDigest != projection.BundleDigest ||
		compilation.ProjectionSetFingerprint != projection.ProjectionSetFingerprint {
		t.Fatalf("application fixture authority mismatch: spec=%t report=%t bundle=%t set=%t",
			writeRequest.Spec.SpecFingerprint == intent.SpecFingerprint,
			compilation.BundleReportFingerprint == projection.BundleReportFingerprint,
			compilation.BundleDigest == projection.BundleDigest,
			compilation.ProjectionSetFingerprint == projection.ProjectionSetFingerprint)
	}
	request, err := sandbox.NewDockerRuntimeInputApplicationRequest(intent, projection,
		compilation, writeRequest)
	if err != nil {
		t.Fatal(err)
	}
	return intent, request, writeRequest
}

func assertDockerRuntimeInputApplicationSchemaPrivacy(t *testing.T, ctx context.Context,
	st *SQLiteStore,
) {
	t.Helper()
	for _, table := range []string{"sandbox_docker_runtime_input_application_intents",
		"sandbox_docker_runtime_input_application_leases",
		"sandbox_docker_runtime_input_application_failures",
		"sandbox_docker_runtime_input_application_results"} {
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
				t.Fatalf("schema v61 persists private material in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
