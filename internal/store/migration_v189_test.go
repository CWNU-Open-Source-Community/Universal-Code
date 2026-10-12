package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

func TestSchemaV189RepairsSBXCommandScopeWithoutRewritingHistory(t *testing.T) {
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "actual-v188.db")
	state := openHistoricalTestDatabase(t, file, 188)
	docker := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "migration-test", strings.Repeat("a", 64))
	oldJob, oldActor := commandScopeV189Fixture(t, state, "docker", docker, "historical-docker")
	stored, replayed, err := state.PrepareCommandRuntimeJobForAgent(ctx, oldJob, oldActor)
	if err != nil || replayed {
		t.Fatalf("real v188 Docker Job seed: replay=%t error=%v", replayed, err)
	}
	oldJob = stored
	sbx := commandruntimeadapter.SandboxedWorkspace(sandbox.SBXBackendName, sandbox.SBXPolicyVersion, strings.Repeat("b", 64))
	job, actor := commandScopeV189Fixture(t, state, "sbx", sbx, "selected-sbx")
	if _, _, err := state.PrepareCommandRuntimeJobForAgent(ctx, job, actor); err == nil || errors.Unwrap(err) == nil ||
		!strings.Contains(errors.Unwrap(err).Error(), "command runtime scope is stale or unauthorized") {
		t.Fatalf("v188 must reject the otherwise valid SBX Job at the missing SQL scope branch: error=%v cause=%v", err, errors.Unwrap(err))
	}
	history, err := state.loadAppliedMigrations(ctx)
	if err != nil || len(history) != 188 {
		t.Fatal("not an actual v188 database", err)
	}
	beforeSchema := commandScopeV189Schema(t, state)
	beforeRows := map[string][][]any{}
	for _, table := range []string{"workspaces", "command_runtime_jobs", "command_runtime_job_agents", "run_execution_profile_snapshots", "run_execution_permission_snapshots", "run_execution_interaction_snapshots"} {
		beforeRows[table] = legacyFixtureRows(t, state, table)
	}
	// Check v189's exact change before Open applies subsequent migrations.
	if err := state.applyMigration(ctx, migrationPlan()[188]); err != nil {
		t.Fatal("upgrade v188 to v189", err)
	}
	const trigger = "trigger:trg_command_runtime_job_insert_scope"
	const branch = "OR (NEW.adapter_backend = 'docker_standard_code' AND profile.profile = 'docker')"
	wantSchema := beforeSchema
	if strings.Count(wantSchema[trigger], branch) != 1 {
		t.Fatal("historical scope anchor changed")
	}
	wantSchema[trigger] = strings.Replace(wantSchema[trigger], branch, branch+
		"\n\t\t\t\t\t\t\tOR (NEW.adapter_backend = 'docker_sandboxes' AND profile.profile = 'sbx')", 1)
	if !reflect.DeepEqual(wantSchema, commandScopeV189Schema(t, state)) {
		t.Fatal("v189 changed schema outside the exact SBX adapter/profile scope branch")
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(file)
	if err != nil {
		t.Fatal("upgrade to current schema", err)
	}
	defer state.Close()
	if version, err := state.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("upgraded schema version=%d error=%v", version, err)
	}
	current, err := state.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for version, entry := range history {
		if current[version] != entry {
			t.Fatalf("published migration %d changed", version)
		}
	}
	for table, rows := range beforeRows {
		if !reflect.DeepEqual(rows, legacyFixtureRows(t, state, table)) {
			t.Fatalf("upgrade rewrote historical %s rows", table)
		}
	}
	loaded, err := state.GetCommandRuntimeJob(ctx, oldJob.ID)
	if err != nil || !reflect.DeepEqual(loaded, oldJob) {
		t.Fatal("historical Docker Job projection changed", err)
	}
	admitted, replayed, err := state.PrepareCommandRuntimeJobForAgent(ctx, job, actor)
	if err != nil || replayed || admitted.Adapter.Backend != sandbox.SBXBackendName {
		t.Fatalf("distinct SBX Job rejected after forward repair: replay=%t error=%v cause=%v", replayed, err, errors.Unwrap(err))
	}
	if got, replayed, err := state.PrepareCommandRuntimeJobForAgent(ctx, job, actor); err != nil || !replayed || !reflect.DeepEqual(got, admitted) {
		t.Fatalf("exact migrated SBX Job replay: replay=%t error=%v", replayed, err)
	}
	// Direct SQL bypasses Go validation so these controls prove that the repaired
	// SQLite fence still rejects stale snapshots, actor/lease changes, relabelled
	// backends, network and credential widening.
	for i, tc := range []struct {
		name   string
		change map[string]any
	}{
		{"docker-cannot-use-sbx-profile", map[string]any{"adapter_backend": application.CommandRuntimeDockerSandboxBackend}},
		{"local-cannot-use-sbx-profile", map[string]any{"adapter_backend": "local_windows_lpac"}},
		{"unknown-backend", map[string]any{"adapter_backend": "unknown_sandbox"}},
		{"stale-mode", map[string]any{"mode_revision": job.ModeRevision + 1}},
		{"stale-profile", map[string]any{"profile_revision": job.ProfileRevision + 1}},
		{"stale-permission", map[string]any{"permission_revision": job.PermissionRevision + 1}},
		{"changed-permission-mode", map[string]any{"permission_mode": "auto"}},
		{"stale-lease-generation", map[string]any{"lease_generation": job.LeaseGeneration + 1}},
		{"changed-lease-owner", map[string]any{"lease_owner_id": "different-owner"}},
		{"cross-run-root", map[string]any{"root_agent_id": oldJob.RootAgentID}},
		{"host-network", map[string]any{"network": "host"}},
		{"credentials", map[string]any{"credentials": "host"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := cloneCommandScopeV189(t, state, job.ID, i+1, tc.change); err == nil {
				t.Fatal("SQLite accepted a widened or stale SBX launch")
			}
		})
	}
	lease, found, err := state.GetRunExecutionLease(ctx, job.RunID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := cloneCommandScopeV189(t, state, job.ID, 100, nil); err == nil {
		t.Fatal("SQLite admitted SBX with a released Run lease")
	}
	assertNoForeignKeyViolations(t, state.db)
	assertLatestMigrationLedger(t, state, migrationPlan())
	if err := state.applyMigration(ctx, migrationPlan()[188]); err != nil {
		t.Fatal("v189 replay", err)
	}
}

func commandScopeV189Schema(t *testing.T, state *SQLiteStore) map[string]string {
	t.Helper()
	rows, err := state.db.QueryContext(t.Context(), `SELECT type || ':' || name, sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		result[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func cloneCommandScopeV189(t *testing.T, state *SQLiteStore, sourceID string, nonce int, change map[string]any) error {
	t.Helper()
	overrides := map[string]any{"id": fmt.Sprintf("forged-v189-%d", nonce), "operation_digest": fmt.Sprintf("%064x", nonce), "request_fingerprint": fmt.Sprintf("%064x", nonce+1000)}
	for name, value := range change {
		overrides[name] = value
	}
	columns := "protocol_version," + commandRuntimeJobColumns + ",operator_invocation"
	var selected []string
	var args []any
	for _, column := range strings.Split(columns, ",") {
		column = strings.TrimSpace(column)
		if value, overridden := overrides[column]; overridden {
			selected = append(selected, "?")
			args = append(args, value)
		} else {
			selected = append(selected, column)
		}
	}
	args = append(args, sourceID)
	_, err := state.db.ExecContext(t.Context(), "INSERT INTO command_runtime_jobs ("+columns+") SELECT "+strings.Join(selected, ",")+" FROM command_runtime_jobs WHERE id=?", args...)
	return err
}

func commandScopeV189Fixture(t *testing.T, state *SQLiteStore, profile string, adapter commandruntimeadapter.Identity, suffix string) (runner.CommandRuntimeJob, domain.AgentAttribution) {
	t.Helper()
	ctx := t.Context()
	workspace := WorkspaceRecord{ID: "workspace-v189-" + suffix, Name: "owned scope fixture " + suffix, RootPath: t.TempDir()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	registered, err := state.GetWorkspaceByID(ctx, workspace.ID)
	if err != nil || registered.ID != workspace.ID || registered.Name != workspace.Name || registered.RootPath != workspace.RootPath {
		t.Fatalf("scope fixture workspace identity was not registered: got=%+v error=%v", registered, err)
	}
	runs := application.NewRunService(state)
	mission, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "verify distinct command backend SQL scope", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := application.NewRunExecutionProfileService(state).Change(ctx, application.ChangeRunExecutionProfileRequest{
		RunID: run.ID, Profile: profile, OperationKey: "v189-profile-" + suffix, RequestedBy: "operator", Reason: "test exact SQL adapter mapping"})
	if err != nil {
		t.Fatal(err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil || permission.Mode != domain.RunExecutionPermissionAsk {
		t.Fatal("scope fixture must retain default Ask permission", err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	acquired, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "v189-worker-" + suffix, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := state.BeginSupervisorTurn(ctx, acquired.Lease, "offline migration scope verification")
	if err != nil {
		t.Fatal(err)
	}
	mode, err := state.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatal("scope fixture root missing", err)
	}
	now := time.Now().UTC()
	job := runner.CommandRuntimeJob{ID: "job-v189-" + suffix, OperationDigest: testCommandRuntimeDigest("operation-" + suffix), RequestFingerprint: testCommandRuntimeDigest("request-" + suffix),
		InvocationID: turn.Checkpoint.AttemptID, RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID, WorkspaceID: workspace.ID, RootAgentID: root.ID,
		WorkspaceRootSHA256: strings.Repeat("c", 64), ModeSnapshotID: mode.ID, ModeRevision: mode.Revision,
		ProfileSnapshotID: selected.Profile.ID, ProfileRevision: selected.Profile.Revision, PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
		LeaseID: acquired.Lease.LeaseID, LeaseGeneration: acquired.Lease.Generation, LeaseOwnerID: acquired.Lease.OwnerID, Adapter: adapter,
		OwnerID: "v189-job-owner", OwnerGeneration: 1, OwnerRenewedAt: now, OwnerExpiresAt: now.Add(time.Minute), IntentJSON: `{}`,
		SpecFingerprint: strings.Repeat("d", 64), Profile: runner.CommandRuntimeProcess, ExecutablePath: "/usr/bin/python3", ExecutableSHA256: strings.Repeat("e", 64), EnvironmentSHA256: strings.Repeat("f", 64),
		WorkingDirectory: ".", StdinPolicy: runner.CommandRuntimeStdinClosed, Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		TimeoutMilliseconds: 1000, InlineLimitBytes: 4096, ArtifactLimitBytes: 4096, State: runner.CommandRuntimeJobPrepared, OutputFramesJSON: "[]", StdinClosed: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	return job, domain.AgentAttribution{AgentID: root.ID, AgentAttemptID: root.ActiveAttemptID, Source: domain.AgentAttributionRecorded}
}
