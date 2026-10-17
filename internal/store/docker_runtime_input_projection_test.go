package store

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

type storeRuntimeInputBundle struct {
	*bytes.Reader
	report sandbox.HostInputBundleReport
}

func (bundle *storeRuntimeInputBundle) Report() sandbox.HostInputBundleReport {
	return bundle.report
}

func (bundle *storeRuntimeInputBundle) Close() error { return nil }

func TestDockerRuntimeInputProjectionLedgerIsAtomicImmutablePrivateAndConcurrent(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	plan, operation := prepareDockerRuntimeInputProjectionStoreFixture(t, ctx, st,
		run.ID, root, "docker-runtime-input-ledger")
	stored, replayed, err := st.CreateDockerRuntimeInputProjectionPlan(ctx, plan, operation)
	if err != nil || replayed || stored.ID != plan.ID {
		t.Fatalf("create runtime projection: %#v replayed=%t err=%v", stored, replayed, err)
	}
	secondRoot := t.TempDir()
	if err := st.SaveWorkspace(ctx, WorkspaceRecord{ID: "ws-runtime-input-second",
		Name: "runtime-input-second", RootPath: secondRoot}); err != nil {
		t.Fatal(err)
	}
	_, secondRun, err := application.NewRunService(st).Create(ctx,
		application.CreateRunRequest{Goal: "repeat the same runtime input projection",
			Profile: "code", WorkspaceID: "ws-runtime-input-second",
			Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 4}})
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, secondOperation := prepareDockerRuntimeInputProjectionStoreFixture(t,
		ctx, st, secondRun.ID, secondRoot, "docker-runtime-input-ledger-second")
	if secondPlan.BundleDigest != plan.BundleDigest ||
		secondPlan.ManifestFingerprint != plan.ManifestFingerprint ||
		secondPlan.Items[0].ItemFingerprint == plan.Items[0].ItemFingerprint {
		t.Fatal("identical cross-Run input was not isolated by handoff identity")
	}
	if _, replayed, err := st.CreateDockerRuntimeInputProjectionPlan(ctx, secondPlan,
		secondOperation); err != nil || replayed {
		t.Fatalf("identical input fingerprint was incorrectly global: replayed=%t err=%v",
			replayed, err)
	}

	const readers = 8
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, readers)
	for index := 0; index < readers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, wasReplay, createErr := st.CreateDockerRuntimeInputProjectionPlan(
				ctx, plan, operation)
			if createErr != nil {
				errorsByWorker <- createErr
				return
			}
			if !wasReplay || value.ID != plan.ID {
				errorsByWorker <- fmt.Errorf("unexpected replay: id=%s replayed=%t",
					value.ID, wasReplay)
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for workerErr := range errorsByWorker {
		t.Fatal(workerErr)
	}

	loaded, err := st.GetDockerRuntimeInputProjectionPlan(ctx, plan.ID)
	if err != nil || loaded.ProjectionFingerprint != plan.ProjectionFingerprint ||
		len(loaded.Items) != len(plan.Items) {
		t.Fatalf("load runtime projection: %#v err=%v", loaded, err)
	}
	byHandoff, found, err := st.GetDockerRuntimeInputProjectionPlanByHandoff(ctx,
		plan.HandoffID)
	if err != nil || !found || byHandoff.ID != plan.ID {
		t.Fatalf("load projection by handoff: %#v found=%t err=%v", byHandoff, found, err)
	}
	listed, err := st.ListDockerRuntimeInputProjectionPlans(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != plan.ID {
		t.Fatalf("list runtime projections: %#v err=%v", listed, err)
	}
	loadedOperation, found, err := st.GetDockerRuntimeInputProjectionOperation(ctx,
		operation.KeyDigest)
	if err != nil || !found || loadedOperation.ProjectionID != plan.ID {
		t.Fatalf("load runtime projection operation: %#v found=%t err=%v",
			loadedOperation, found, err)
	}

	for _, mutation := range []string{
		`UPDATE sandbox_docker_runtime_input_projection_plans SET daemon_applied = 1 WHERE id = ?`,
		`UPDATE sandbox_docker_runtime_input_projection_items SET read_only = 0 WHERE projection_id = ?`,
		`DELETE FROM sandbox_docker_runtime_input_projection_operations WHERE projection_id = ?`,
		`DELETE FROM sandbox_docker_runtime_input_projection_completions WHERE projection_id = ?`,
	} {
		if _, err := st.db.ExecContext(ctx, mutation, plan.ID); err == nil ||
			!strings.Contains(err.Error(), "cannot be") {
			t.Fatalf("runtime projection mutation was not rejected: %s: %v", mutation, err)
		}
	}
	assertDockerRuntimeInputProjectionSchemaPrivacy(t, ctx, st)

	var persisted string
	if err := st.db.QueryRowContext(ctx, `SELECT
		plan.protocol_version || '|' || plan.status || '|' || plan.trust_class || '|' ||
		plan.requested_by || '|' || item.kind || '|' || item.target_fingerprint || '|' ||
		item.archive_root_fingerprint || '|' || item.volume_name_fingerprint
		FROM sandbox_docker_runtime_input_projection_plans plan
		JOIN sandbox_docker_runtime_input_projection_items item
			ON item.projection_id = plan.id WHERE plan.id = ?`, plan.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, "main.txt", "source payload", "/workspace",
		"cyberagent-runtime-"} {
		if strings.Contains(persisted, private) {
			t.Fatalf("runtime projection tables leaked %q: %s", private, persisted)
		}
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEvent := false
	for _, event := range timeline {
		if event.Type == events.SandboxDockerRuntimeInputProjectionEvent {
			foundEvent = true
			if !strings.Contains(event.PayloadJSON, `"daemon_applied":false`) ||
				!strings.Contains(event.PayloadJSON, `"execution_authorized":false`) {
				t.Fatalf("runtime projection event lost non-authority facts: %#v", event)
			}
		}
		for _, private := range []string{root, "main.txt", "source payload", "/workspace",
			"cyberagent-runtime-"} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("runtime projection event leaked %q: %#v", private, event)
			}
		}
	}
	if !foundEvent {
		t.Fatal("runtime projection event was not recorded")
	}
}

func TestSchemaV60PreservesV59HandoffWithoutFabricatingProjection(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "docker-runtime-input-v59.db")
	st, run, root := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	plan, _ := prepareDockerRuntimeInputProjectionStoreFixture(t, ctx, st, run.ID,
		root, "docker-runtime-input-upgrade")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 59)
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
		t.Fatalf("schema v60 upgrade version=%d err=%v", version, err)
	}
	values, err := upgraded.ListDockerRuntimeInputProjectionPlans(ctx, run.ID, 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("schema v60 fabricated projection records: %#v err=%v", values, err)
	}
	if _, found, err := upgraded.GetDockerRuntimeInputProjectionPlanByHandoff(ctx,
		plan.HandoffID); err != nil || found {
		t.Fatalf("schema v60 fabricated projection for handoff: found=%t err=%v", found, err)
	}
}

func prepareDockerRuntimeInputProjectionStoreFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, root, prefix string,
) (sandbox.DockerRuntimeInputProjectionPlan, sandbox.DockerRuntimeInputProjectionOperation) {
	t.Helper()
	attempt, containerPlan, spec, writeRequest, manifest :=
		newStagedDockerHostInputAttemptWithRequirements(t, ctx, st, runID, root,
			prefix, true, true)
	bundle := newStoreRuntimeInputBundle(t, manifest)
	stagingDigest := runmutation.Fingerprint(
		"sandbox_docker_host_input_staging_operation.v1", attempt.Intent.OperationKeyDigest)
	stagingIntent, err := sandbox.NewDockerHostInputStagingIntent(
		idgen.New("sandbox-docker-host-input-intent"), stagingDigest, attempt,
		containerPlan, manifest, containerPlan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PrepareDockerHostInputStagingIntent(ctx, stagingIntent,
		attempt.Lease); err != nil {
		t.Fatal(err)
	}
	stagingValue, err := sandbox.NewDockerHostInputStaging(
		idgen.New("sandbox-docker-host-input-staging"), stagingIntent,
		attempt.Lease.Generation, bundle.report, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	staging, _, err := st.RecordDockerHostInputStaging(ctx, stagingValue, attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
	handoffDigest := runmutation.Fingerprint(
		"sandbox_docker_host_input_handoff_operation.v1", attempt.Intent.OperationKeyDigest)
	handoffIntent, err := sandbox.NewDockerHostInputHandoffIntent(
		idgen.New("sandbox-docker-host-input-handoff-intent"), handoffDigest,
		attempt, containerPlan, staging, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PrepareDockerHostInputHandoffIntent(ctx, handoffIntent,
		attempt.Lease); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	handoffRequest, err := sandbox.NewDockerHostInputHandoffRequest(handoffIntent,
		writeRequest, attempt.Stage.Result, bundle.report)
	if err != nil {
		t.Fatal(err)
	}
	handoffResult, err := sandbox.NewDockerHostInputHandoffResult(endpoint,
		handoffRequest, strings.Repeat("d", 64), 8, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	handoffValue, err := sandbox.NewDockerHostInputHandoff(
		idgen.New("sandbox-docker-host-input-handoff"), handoffIntent,
		attempt.Lease.Generation, handoffResult, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	handoff, _, err := st.RecordDockerHostInputHandoff(ctx, handoffValue, attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
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
	attempt, _, err = st.RecordDockerContainerAttemptCleanup(ctx, cleanup, attempt.Lease)
	if err != nil {
		t.Fatal(err)
	}
	rehearsal, rehearsalOperation, completion := dockerAttemptCompletionFixture(t,
		containerPlan, spec, writeRequest, attempt)
	if _, _, err := st.CompleteDockerContainerRehearsalAttempt(ctx, completion,
		rehearsal, rehearsalOperation, attempt.Lease); err != nil {
		t.Fatal(err)
	}
	attempt, err = st.GetDockerContainerRehearsalAttempt(ctx, attempt.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	compilation, err := sandbox.CompileDockerRuntimeInputProjectionBundle(ctx,
		manifest, bundle, handoff.Handoff.HandoffFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := runmutation.Fingerprint(
		sandbox.DockerRuntimeInputProjectionOperationVersion,
		handoff.Intent.ID, prefix+"-projection")
	if _, err := sandbox.NewDockerRuntimeInputProjectionPlan(
		idgen.New("sandbox-docker-runtime-input-plan"), keyDigest, attempt,
		containerPlan, handoff, compilation, true, containerPlan.RequestedBy,
		handoff.Handoff.CreatedAt.Add(-time.Second)); err == nil {
		t.Fatal("runtime input projection accepted pre-handoff chronology")
	}
	plan, err := sandbox.NewDockerRuntimeInputProjectionPlan(
		idgen.New("sandbox-docker-runtime-input-plan"), keyDigest, attempt,
		containerPlan, handoff, compilation, true, containerPlan.RequestedBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := sandbox.NewDockerRuntimeInputProjectionOperation(keyDigest, plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan, operation
}

func newStoreRuntimeInputBundle(t *testing.T,
	manifest sandbox.Manifest,
) *storeRuntimeInputBundle {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	rootName := "mounts/001"
	fileName := rootName + "/main.txt"
	content := []byte("source payload")
	for _, header := range []*tar.Header{
		{Name: rootName + "/", Typeflag: tar.TypeDir, Mode: 0o555},
		{Name: fileName, Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(content))},
	} {
		header.Uid, header.Gid = 65532, 65532
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Unix(0, 0).UTC()
		header.ChangeTime = time.Unix(0, 0).UTC()
		header.Format = tar.FormatPAX
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := writer.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := sandbox.HostInputBundleRequest{WorkspaceRoot: "C:/not-persisted",
		Manifest: manifest}
	contentDigest := sha256.Sum256(content)
	sourceParts := []string{"sandbox_host_input_source_snapshot.v1", "2",
		storeRuntimeInputFingerprint("sandbox_host_input_archive_path.v1", rootName),
		strconv.Itoa(int(tar.TypeDir)), "0",
		storeRuntimeInputFingerprint("sandbox_host_input_directory.v1", rootName),
		storeRuntimeInputFingerprint("sandbox_host_input_archive_path.v1", fileName),
		strconv.Itoa(int(tar.TypeReg)), strconv.Itoa(len(content)),
		hex.EncodeToString(contentDigest[:]),
	}
	bundleDigest := sha256.Sum256(output.Bytes())
	report, err := sandbox.NewHostInputBundleReport(sandbox.HostInputBundleMeasurements{
		ReadOnlyMountCount: 1, RegularFileCount: 1, DirectoryCount: 1,
		SourceBytes: int64(len(content)), BundleBytes: int64(output.Len()),
		SourceSnapshotDigest:  storeRuntimeInputFingerprint(sourceParts...),
		ArtifactPayloadDigest: request.ArtifactPayloadDigest(),
		BundleDigest:          hex.EncodeToString(bundleDigest[:]),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return &storeRuntimeInputBundle{Reader: bytes.NewReader(output.Bytes()), report: report}
}

func storeRuntimeInputFingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		value := []byte(part)
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write(value)
		_, _ = hash.Write([]byte{'|'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func assertDockerRuntimeInputProjectionSchemaPrivacy(t *testing.T, ctx context.Context,
	st *SQLiteStore,
) {
	t.Helper()
	for _, table := range []string{"sandbox_docker_runtime_input_projection_plans",
		"sandbox_docker_runtime_input_projection_items",
		"sandbox_docker_runtime_input_projection_completions",
		"sandbox_docker_runtime_input_projection_operations"} {
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
			case "target", "target_path", "source_path", "workspace_root", "content",
				"raw_content", "archive", "archive_blob", "volume_name",
				"container_id", "manifest_json", "operation_key":
				_ = rows.Close()
				t.Fatalf("schema v60 persists private material in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
