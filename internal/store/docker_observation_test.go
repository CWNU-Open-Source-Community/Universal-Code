package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

type dockerObservationStoreTransport struct {
	imageDigest string
}

func (transport dockerObservationStoreTransport) Endpoint() sandbox.DockerObservationEndpoint {
	endpoint, _ := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	return endpoint
}

func (dockerObservationStoreTransport) Ping(context.Context) error { return nil }

func (dockerObservationStoreTransport) Version(context.Context) (sandbox.DockerDaemonVersion, error) {
	return sandbox.DockerDaemonVersion{
		APIVersion: "1.47", MinAPIVersion: "1.24", EngineVersion: "27.5.1",
		GitCommit: "abc123", OSType: "linux", Architecture: "amd64",
	}, nil
}

func (dockerObservationStoreTransport) Info(context.Context) (sandbox.DockerDaemonInfo, error) {
	return sandbox.DockerDaemonInfo{
		ID: "private-daemon-id", Name: "private-build-host", DockerRootDir: "/private/docker",
		ServerVersion: "27.5.1", OperatingSystem: "Test Linux", OSType: "linux",
		Architecture: "amd64", Driver: "overlay2", CgroupDriver: "systemd",
		CgroupVersion: "2", DefaultRuntime: "runc", NCPU: 8,
		MemoryBytes: 16 * 1024 * 1024 * 1024, PidsLimit: true,
		SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"},
	}, nil
}

func (transport dockerObservationStoreTransport) InspectImage(context.Context,
	string,
) (sandbox.DockerImageInspection, error) {
	return sandbox.DockerImageInspection{
		ID:          "sha256:" + strings.Repeat("a", 64),
		RepoDigests: []string{"example.invalid/workbench@" + transport.imageDigest},
		OSType:      "linux", Architecture: "amd64", SizeBytes: 1048576,
		User: "65532:65532", RootFSType: "layers", GraphDriver: "overlay2",
	}, nil
}

func TestDockerObservationLedgerIsImmutablePrivateAndNonAuthorizing(t *testing.T) {
	ctx := context.Background()
	st, run, root := openSandboxManifestStore(t, ctx)
	evidence, simulation := createDockerObservationAuthorityFixture(t, ctx, st, run.ID,
		"observation-ledger")
	observation, operation := newDockerObservationStoreRecord(t, ctx, evidence, simulation,
		"observation-ledger-record")
	stored, replayed, err := st.CreateDockerObservation(ctx, observation, operation)
	if err != nil || replayed || stored.ID != observation.ID {
		t.Fatalf("create Docker observation: stored=%#v replayed=%t err=%v", stored, replayed, err)
	}
	if !stored.Report.ProductionObserved || stored.Report.ProductionVerified ||
		stored.Report.BackendAvailable || stored.Report.BackendEnabled ||
		stored.Report.ExecutionAuthorized || stored.Report.ArtifactCommitAuthorized {
		t.Fatalf("Docker observation gained authority: %#v", stored.Report)
	}
	loaded, err := st.GetDockerObservation(ctx, stored.ID)
	if err != nil || loaded.Report.ObservationFingerprint != stored.Report.ObservationFingerprint ||
		len(loaded.Report.Items) != sandbox.MaxDockerObservationItems {
		t.Fatalf("load Docker observation: loaded=%#v err=%v", loaded, err)
	}
	listed, err := st.ListDockerObservations(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != stored.ID {
		t.Fatalf("list Docker observations: values=%#v err=%v", listed, err)
	}

	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_observations
		SET production_verified = 1 WHERE id = ?`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Docker observation root was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_docker_observation_items
		WHERE observation_id = ? AND ordinal = 1`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Docker observation item was deletable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_docker_observation_operations
		SET requested_by = 'other' WHERE observation_id = ?`, stored.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Docker observation operation was mutable: %v", err)
	}
	unsupported, _ := newDockerObservationStoreRecord(t, ctx, evidence, simulation,
		"observation-unsupported-authority")
	unsupported.Report.BackendAvailable = true
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDockerObservationTx(ctx, tx, unsupported); err == nil {
		_ = tx.Rollback()
		t.Fatal("direct SQL accepted backend authority from a read-only observation")
	}
	_ = tx.Rollback()

	for _, table := range []string{"sandbox_docker_observations",
		"sandbox_docker_observation_items", "sandbox_docker_observation_operations"} {
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
			case "daemon_id", "daemon_name", "docker_root_dir", "socket_path", "host_path",
				"workspace_root", "command", "arguments_json", "secret_value", "container_id",
				"repo_digests_json", "manifest_json", "lease_id", "owner_id":
				_ = rows.Close()
				t.Fatalf("schema v53 persists private data in %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}

	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEvent := false
	for _, event := range timeline {
		if event.Type != events.SandboxDockerObservationRecordedEvent {
			continue
		}
		foundEvent = true
		for _, private := range []string{root, "private-daemon-id", "private-build-host",
			"/private/docker", evidence.Report.ImageDigest, stored.Report.EndpointFingerprint,
			stored.Report.ObservationFingerprint} {
			if strings.Contains(event.PayloadJSON, private) {
				t.Fatalf("schema v53 event leaked private data %q: %#v", private, event)
			}
		}
	}
	if !foundEvent {
		t.Fatal("Docker observation event was not recorded")
	}
}

func TestDockerObservationConcurrentReplayConvergesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cyberagent.db")
	st1, run, _ := openSandboxManifestStoreAt(t, ctx, path)
	t.Cleanup(func() { _ = st1.Close() })
	evidence, simulation := createDockerObservationAuthorityFixture(t, ctx, st1, run.ID,
		"observation-concurrent")
	observation, operation := newDockerObservationStoreRecord(t, ctx, evidence, simulation,
		"observation-concurrent-record")
	otherObservation := observation
	otherObservation.ID += "-other"
	otherObservation.CreatedAt = observation.CreatedAt.Add(time.Second)
	otherOperation := operation
	otherOperation.ObservationID = otherObservation.ID
	otherOperation.CreatedAt = otherObservation.CreatedAt
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	stores := []*SQLiteStore{st1, st2}
	observations := []sandbox.DockerObservation{observation, otherObservation}
	operations := []sandbox.DockerObservationOperation{operation, otherOperation}
	results := make([]sandbox.DockerObservation, len(stores))
	replayed := make([]bool, len(stores))
	errorsFound := make([]error, len(stores))
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], replayed[index], errorsFound[index] =
				stores[index].CreateDockerObservation(ctx, observations[index], operations[index])
		}(index)
	}
	close(start)
	group.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || results[0].ID != results[1].ID ||
		(results[0].ID != observation.ID && results[0].ID != otherObservation.ID) ||
		replayed[0] == replayed[1] {
		t.Fatalf("concurrent Docker observation replay diverged: results=%#v replayed=%v errors=%v",
			results, replayed, errorsFound)
	}
}

func TestDockerObservationLimitAndSchemaV52Upgrade(t *testing.T) {
	for _, historical := range []bool{false, true} {
		name := "current"
		if historical {
			name = "historical_upgrade"
		}
		t.Run(name, func(t *testing.T) {
			var historicalVersion []int
			if historical {
				historicalVersion = []int{177}
			}
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "v52.db")
			seedPath := path
			if historical {
				seedPath = filepath.Join(t.TempDir(), "seed.db")
			}
			st, run, _ := openSandboxManifestStoreAt(t, ctx, seedPath, historicalVersion...)
			restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
			t.Cleanup(func() { _ = st.Close() })
			evidence, simulation := createDockerObservationAuthorityFixture(t, ctx, st, run.ID,
				"observation-limit")
			for index := 0; index < sandbox.MaxDockerObservationsPerSimulation; index++ {
				observation, operation := newDockerObservationStoreRecord(t, ctx, evidence, simulation,
					"observation-limit-"+string(rune('a'+index)))
				if _, _, err := st.CreateDockerObservation(ctx, observation, operation); err != nil {
					t.Fatalf("create Docker observation %d: %v", index+1, err)
				}
			}
			overflow, operation := newDockerObservationStoreRecord(t, ctx, evidence, simulation,
				"observation-limit-overflow")
			if _, _, err := st.CreateDockerObservation(ctx, overflow, operation); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
				t.Fatalf("Docker observation limit error=%v code=%s", err, apperror.CodeOf(err))
			}
			tx, err := st.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := insertDockerObservationTx(ctx, tx, overflow); err == nil ||
				!strings.Contains(err.Error(), "authority binding is invalid") {
				_ = tx.Rollback()
				t.Fatalf("SQLite observation limit was bypassed: %v", err)
			}
			_ = tx.Rollback()

			if !historical {
				return
			}

			restoreLegacyContext()
			st = historicalTestDatabaseFromSeed(t, st, path, 52)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
				t.Fatalf("schema v52 did not upgrade to v53: version=%d err=%v", version, err)
			}
			loadedEvidence, err := st.GetSandboxBackendEvidence(ctx, evidence.ID)
			if err != nil || loadedEvidence.ID != evidence.ID {
				t.Fatalf("schema v52 evidence was not preserved: %#v err=%v", loadedEvidence, err)
			}
			loadedSimulation, err := st.GetSandboxOutputSimulation(ctx, simulation.ID)
			if err != nil || loadedSimulation.ID != simulation.ID {
				t.Fatalf("schema v52 simulation was not preserved: %#v err=%v", loadedSimulation, err)
			}
		})
	}
}

func createDockerObservationAuthorityFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID, prefix string,
) (sandbox.BackendEvidence, sandbox.OutputSimulation) {
	t.Helper()
	service, manifest, preflight := createDockerPreflightStoreFixture(t, ctx, st, runID, prefix)
	requestedBy := "store_evidence_operator"
	evidence, err := service.RecordSimulatedBackendEvidence(ctx,
		application.RecordSandboxBackendEvidenceRequest{
			PreflightID: preflight.ID, Manifest: manifest,
			ImageDigest:  "sha256:" + strings.Repeat("e", 64),
			OperationKey: prefix + "-evidence", RequestedBy: requestedBy,
		})
	if err != nil {
		t.Fatal(err)
	}
	fixture := sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion,
		Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout, FileType: sandbox.OutputFileTypeStream,
				Content: "bounded stdout"},
			{Kind: sandbox.OutputKindStderr, FileType: sandbox.OutputFileTypeStream,
				Content: "bounded stderr"},
		}}
	simulation, err := service.SimulateOutputTransaction(ctx,
		application.SimulateSandboxOutputRequest{
			EvidenceID: evidence.ID, Manifest: manifest, Fixture: fixture,
			OperationKey: prefix + "-simulation", RequestedBy: requestedBy,
		})
	if err != nil {
		t.Fatal(err)
	}
	return evidence, simulation
}

func newDockerObservationStoreRecord(t *testing.T, ctx context.Context,
	evidence sandbox.BackendEvidence, simulation sandbox.OutputSimulation, operationKey string,
) (sandbox.DockerObservation, sandbox.DockerObservationOperation) {
	t.Helper()
	observation := sandbox.DockerObservation{
		ID: idgen.New("sandbox-docker-observation"), EvidenceID: evidence.ID,
		OutputSimulationID: simulation.ID, PreflightID: evidence.PreflightID,
		ExecutionID: evidence.ExecutionID, CandidateID: evidence.CandidateID,
		PreparationID: evidence.PreparationID, RunID: evidence.RunID,
		MissionID: evidence.MissionID, WorkspaceID: evidence.WorkspaceID,
		ManifestFingerprint:      evidence.ManifestFingerprint,
		AuthorizationFingerprint: evidence.AuthorizationFingerprint,
		PolicyFingerprint:        evidence.PolicyFingerprint,
		MountBindingFingerprint:  evidence.MountBindingFingerprint,
		InputArtifactDigest:      evidence.InputArtifactDigest,
		ThreatModelFingerprint:   evidence.ThreatModelFingerprint,
		OutputPlanFingerprint:    evidence.Report.OutputPlanFingerprint,
		Report:                   sandbox.DockerObservationReport{ImageDigest: evidence.Report.ImageDigest},
		RequestedBy:              evidence.RequestedBy, CreatedAt: time.Now().UTC(),
	}
	observer := sandbox.NewReadOnlyDockerProductionObserver(dockerObservationStoreTransport{
		imageDigest: evidence.Report.ImageDigest,
	})
	report, err := observer.Observe(ctx, sandbox.DockerObservationProbeRequest{
		BindingFingerprint: sandbox.DockerObservationBindingFingerprint(observation),
		ImageDigest:        evidence.Report.ImageDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation.Report = report
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	operation := sandbox.DockerObservationOperation{
		KeyDigest:     runmutation.Fingerprint("docker_observation_store_test.v1", operationKey),
		ObservationID: observation.ID, EvidenceID: evidence.ID,
		OutputSimulationID: simulation.ID, RunID: evidence.RunID,
		RequestedBy: evidence.RequestedBy, CreatedAt: observation.CreatedAt,
	}
	operation.RequestFingerprint = sandbox.DockerObservationRequestFingerprint(observation)
	return observation, operation
}
