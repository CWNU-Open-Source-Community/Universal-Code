package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"

	"github.com/mattn/go-sqlite3"
)

func TestCommandRuntimeJobLedgerFencesScopeAndPreservesTerminalAudit(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "command-runtime-jobs.db")
	st, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	workspaceRoot := t.TempDir()
	workspace := WorkspaceRecord{ID: "workspace-command-runtime", Name: "command-runtime",
		RootPath: workspaceRoot}
	if err := st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	mission, runRecord, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "persist one fenced managed command", Profile: "code", WorkspaceID: workspace.ID,
		Surface: "code", Phase: "deliver",
		Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileResult, err := application.NewRunExecutionProfileService(st).Change(ctx,
		application.ChangeRunExecutionProfileRequest{RunID: runRecord.ID, Profile: "local",
			OperationKey: "command-runtime-profile-0001", RequestedBy: "test_operator",
			Reason: "run an owned local command"})
	if err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	permissionResult, err := application.NewRunExecutionPermissionService(st, caps).Change(ctx,
		application.ChangeRunExecutionPermissionRequest{RunID: runRecord.ID,
			Mode: string(domain.RunExecutionPermissionFull), ConfirmFull: true,
			OperationKey: "command-runtime-full-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	startedRun, err := runs.Start(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation, live := caps.FullAccessGeneration(permissionResult.Permission)
	if !live || generation == 0 {
		t.Fatal("confirmed current Full selection did not activate its runtime grant")
	}
	fence, err := caps.RuntimeAuthority.IssueRunAuthorizationFence(startedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, startedRun.ID)
	mode, err := st.GetRunMode(ctx, startedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, startedRun.ID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	specialistStarted := time.Now().UTC()
	child, replayed, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{
		AgentID:   "agent-command-runtime-specialist",
		SessionID: "session-command-runtime-specialist", RunID: startedRun.ID,
		ParentAgentID: root.ID, Title: "command actor conflict",
		Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 64,
		MaxChildren: 2, CreatedAt: specialistStarted},
		"command-runtime-agent-admission-0001")
	if err != nil || replayed {
		t.Fatalf("admit conflicting actor replayed=%t err=%v", replayed, err)
	}
	childAttempt, replayed, err := st.BeginSpecialistAttempt(ctx,
		domain.AgentAttemptStart{AttemptID: "attempt-command-runtime-specialist",
			RunID: startedRun.ID, AgentID: child.ID, ParentAgentID: root.ID,
			Lease: lease, StartedAt: specialistStarted}, "command-runtime-agent-attempt-0001")
	if err != nil || replayed {
		t.Fatalf("begin conflicting actor attempt replayed=%t err=%v", replayed, err)
	}
	if _, err := st.BeginSupervisorTurn(ctx, lease, "bind Command Runtime actor"); err != nil {
		t.Fatal(err)
	}
	root, found, err = st.GetRootAgent(ctx, startedRun.ID)
	if err != nil || !found {
		t.Fatalf("active root agent found=%t err=%v", found, err)
	}
	if root.ActiveAttemptID == "" {
		t.Fatal("started root Agent has no active attempt for command attribution")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.NormalizeCommandRuntimeSpec(runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: executable, Arguments: []string{"--help"}, WorkingDirectory: ".",
		Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MinCommandRuntimeInlineBytes,
			ArtifactBytes: runner.MinCommandRuntimeInlineBytes},
		Network:     runner.CommandRuntimeNetworkDisabled,
		Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "test persistence",
	}, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := runner.CommandRuntimeJob{
		ID: "command-job-ledger-1", OperationDigest: testCommandRuntimeDigest("operation-1"),
		RequestFingerprint: testCommandRuntimeDigest("request-1"),
		InvocationID:       "invocation-1", RunID: startedRun.ID, MissionID: mission.ID,
		SessionID: startedRun.SessionID, WorkspaceID: workspace.ID, RootAgentID: root.ID,
		WorkspaceRootSHA256: resolved.WorkspaceRootSHA256,
		ModeSnapshotID:      mode.ID, ModeRevision: mode.Revision,
		ProfileSnapshotID:      profileResult.Profile.ID,
		ProfileRevision:        profileResult.Profile.Revision,
		PermissionSnapshotID:   permissionResult.Permission.ID,
		PermissionRevision:     permissionResult.Permission.Revision,
		PermissionMode:         permissionResult.Permission.Mode,
		PermissionRuntimeEpoch: caps.RuntimeAuthority.RuntimeEpoch(),
		PermissionGeneration:   generation, RunAuthorizationFence: fence,
		LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseOwnerID: lease.OwnerID,
		Adapter: commandruntimeadapter.HostUnsandboxed(strings.Repeat("d", 64)),
		OwnerID: "command-runtime-owner-1", OwnerGeneration: 1,
		OwnerRenewedAt: now, OwnerExpiresAt: now.Add(time.Minute), IntentJSON: `{}`,
		SpecFingerprint: runner.CommandRuntimeSpecFingerprint(resolved),
		Profile:         resolved.Spec.Profile, ExecutablePath: resolved.ExecutablePath,
		ExecutableSHA256:  resolved.ExecutableSHA256,
		EnvironmentSHA256: resolved.EnvironmentSHA256,
		WorkingDirectory:  resolved.Spec.WorkingDirectory,
		StdinPolicy:       resolved.Spec.StdinPolicy, Network: resolved.Spec.Network,
		Credentials:         resolved.Spec.Credentials,
		TimeoutMilliseconds: resolved.Spec.TimeoutMilliseconds,
		InlineLimitBytes:    resolved.Spec.Output.InlineBytes,
		ArtifactLimitBytes:  resolved.Spec.Output.ArtifactBytes,
		State:               runner.CommandRuntimeJobPrepared, OutputFramesJSON: "[]",
		StdinClosed: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	rootAttribution := domain.AgentAttribution{AgentID: root.ID,
		AgentAttemptID: root.ActiveAttemptID, Source: domain.AgentAttributionRecorded}
	threadRecord, err := st.GetThreadByRun(ctx, job.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveThreadCommandServicePolicy(ctx, threadRecord.ID, 1, domain.CommandServicePolicyExclusive); err != nil {
		t.Fatal(err)
	}
	shared := job
	shared.IntentJSON = `{"workspace_mode":"shared_service"}`
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, shared, rootAttribution); apperror.CodeOf(err) != apperror.CodeConflict || errors.Unwrap(err) == nil || !strings.Contains(errors.Unwrap(err).Error(), "Thread requires exclusive") {
		t.Fatalf("durable admission bypassed exclusive preference: %v", err)
	}
	// Ordinary commands remain available under the same saved preference.
	prepared, replayed, err := st.PrepareCommandRuntimeJobForAgent(ctx, job,
		rootAttribution)
	if err != nil || replayed || prepared.ID != job.ID {
		t.Fatalf("prepare=%#v replayed=%t err=%v", prepared, replayed, err)
	}
	storedAttribution, err := st.GetThreadCommandRuntimeJobAgentAttribution(ctx,
		threadRecord.ID, job.ID)
	if err != nil || storedAttribution != rootAttribution {
		t.Fatalf("stored Command Agent attribution=%#v err=%v",
			storedAttribution, err)
	}
	replay, replayed, err := st.PrepareCommandRuntimeJobForAgent(ctx, job,
		rootAttribution)
	if err != nil || !replayed || replay.ID != job.ID {
		t.Fatalf("replay=%#v replayed=%t err=%v", replay, replayed, err)
	}
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, job,
		(domain.AgentAttribution{AgentID: child.ID, AgentAttemptID: childAttempt.ID,
			Source: domain.AgentAttributionRecorded})); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("conflicting Command Agent replay error=%v", err)
	}
	reused := job
	reused.RequestFingerprint = testCommandRuntimeDigest("different-request")
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, reused,
		rootAttribution); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("operation-key reuse error=%v", err)
	}
	stale := job
	stale.ID = "command-job-ledger-stale"
	stale.OperationDigest = testCommandRuntimeDigest("operation-stale")
	stale.RequestFingerprint = testCommandRuntimeDigest("request-stale")
	stale.LeaseGeneration++
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, stale,
		rootAttribution); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale generation error=%v", err)
	}
	startedAt := now.Add(time.Millisecond)
	running := prepared
	running.State = runner.CommandRuntimeJobRunning
	running.PID = 4242
	running.ProcessGroup = 4242
	running.JobAssignedAtCreation = true
	running.StartedAt = &startedAt
	running.Version++
	running.UpdatedAt = startedAt
	running, err = st.UpdateCommandRuntimeJob(ctx, running, prepared.Version)
	if err != nil {
		t.Fatal(err)
	}
	oversizedUTF8 := strings.Repeat("界", running.ArtifactLimitBytes/2)
	if _, err := st.db.ExecContext(ctx, `UPDATE command_runtime_jobs
		SET stdout = ?, stdout_observed_bytes = ?, version = version + 1
		WHERE id = ?`, oversizedUTF8, len([]byte(oversizedUTF8)), running.ID); err == nil {
		t.Fatal("SQLite accepted multibyte output beyond the byte artifact limit")
	}
	renewedAt := startedAt.Add(time.Millisecond)
	renewed := running
	renewed.OwnerRenewedAt = renewedAt
	renewed.OwnerExpiresAt = renewedAt.Add(time.Minute)
	renewed.Version++
	renewed.UpdatedAt = renewedAt
	running, err = st.UpdateCommandRuntimeJob(ctx, renewed, running.Version)
	if err != nil || !running.OwnerRenewedAt.Equal(renewedAt) {
		t.Fatalf("running owner heartbeat=%#v err=%v", running, err)
	}
	t.Run("cancelled-heartbeat-readback", func(t *testing.T) {
		// Cancel only after the real SQLite UPDATE succeeds. The manager keeps
		// its prior version when renewal returns an error; its terminal write
		// below must still be able to use that version.
		renewCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		updates := 0
		interleavedDB := sql.OpenDB(commandRuntimeAfterUpdateConnector{
			dsn: sqliteDSN(databasePath), afterUpdate: func(result driver.Result) {
				changed, err := result.RowsAffected()
				if err != nil || changed != 1 {
					t.Fatalf("heartbeat SQL changed=%d err=%v", changed, err)
				}
				updates++
				cancel()
			},
		})
		interleavedDB.SetMaxOpenConns(1)
		originalDB := st.db
		st.db = interleavedDB
		defer func() {
			st.db = originalDB
			_ = interleavedDB.Close()
		}()
		attempt := running
		attempt.OwnerRenewedAt = running.OwnerRenewedAt.Add(time.Millisecond)
		attempt.OwnerExpiresAt = attempt.OwnerRenewedAt.Add(time.Minute)
		attempt.UpdatedAt = attempt.OwnerRenewedAt
		attempt.Version++
		_, err := st.UpdateCommandRuntimeJob(renewCtx, attempt, running.Version)
		if updates != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled heartbeat updates=%d err=%v", updates, err)
		}
		durable, err := st.GetCommandRuntimeJob(ctx, running.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("heartbeat owner=%s generation=%d expected=%d attempted=%d durable=%d state=%s",
			durable.OwnerID, durable.OwnerGeneration, running.Version, attempt.Version,
			durable.Version, durable.State)
		if durable.Version != running.Version || durable.OwnerID != running.OwnerID ||
			durable.OwnerGeneration != running.OwnerGeneration ||
			!durable.OwnerRenewedAt.Equal(running.OwnerRenewedAt) {
			t.Errorf("cancelled heartbeat consumed the manager's retained version: durable=%d retained=%d",
				durable.Version, running.Version)
		}
	})
	if active, err := st.CommandRuntimeJobOwnershipActive(ctx, running); err != nil || !active {
		t.Fatalf("active command runtime ownership=%t err=%v", active, err)
	}
	if jobs, err := st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{
		WorkspaceRootSHA256: running.WorkspaceRootSHA256, UnreapedOnly: true,
	}); err != nil || len(jobs) != 1 || jobs[0].ID != running.ID {
		t.Fatalf("physical workspace active jobs: %+v %v", jobs, err)
	}
	if jobs, err := st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{
		WorkspaceRootSHA256: strings.Repeat("0", 64), UnreapedOnly: true,
	}); err != nil || len(jobs) != 0 {
		t.Fatalf("unrelated workspace jobs: %+v %v", jobs, err)
	}
	if _, err := st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{
		WorkspaceRootSHA256: "invalid-digest",
	}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("invalid physical workspace digest: %v", err)
	}
	expireTestRunExecutionLease(t, ctx, st, lease)
	if active, err := st.CommandRuntimeJobOwnershipActive(ctx, running); err != nil || !active {
		t.Fatalf("owner heartbeat did not survive turn lease release: active=%t err=%v",
			active, err)
	}
	completedAt := startedAt.Add(time.Millisecond)
	exitCode := 0
	terminal := running
	terminal.State = runner.CommandRuntimeJobCompleted
	terminal.Stdout = "ok\n"
	terminal.StdoutObservedBytes = 3
	terminal.OutputCursor = 3
	terminal.ExitCode = &exitCode
	terminal.TreeReaped = true
	terminal.StdoutSHA256 = testCommandRuntimeDigest("stdout")
	terminal.StderrSHA256 = testCommandRuntimeDigest("stderr")
	terminal.CompletedAt = &completedAt
	terminal.Version++
	terminal.UpdatedAt = completedAt
	terminal, err = st.UpdateCommandRuntimeJob(ctx, terminal, running.Version)
	t.Logf("terminal CAS owner=%s generation=%d expected=%d result=%d state=%s err=%v",
		running.OwnerID, running.OwnerGeneration, running.Version, terminal.Version, terminal.State, err)
	if err != nil || terminal.State != runner.CommandRuntimeJobCompleted {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	if _, err := st.UpdateCommandRuntimeJob(ctx, terminal, running.Version); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("already consumed terminal version did not remain a conflict: %v", err)
	}
	if active, err := st.CommandRuntimeJobOwnershipActive(ctx, terminal); err != nil || active {
		t.Fatalf("terminal command runtime ownership=%t err=%v", active, err)
	}
	jobs, err := st.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{RunID: startedRun.ID, Limit: 10})
	if err != nil || len(jobs) != 1 || jobs[0].Stdout != "ok\n" || !jobs[0].TreeReaped {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}
	if jobs, err := st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{
		WorkspaceRootSHA256: terminal.WorkspaceRootSHA256, UnreapedOnly: true,
	}); err != nil || len(jobs) != 0 {
		t.Fatalf("reaped job remained in active workspace query: %+v %v", jobs, err)
	}
	operatorJob := job
	operatorJob.ID = "command-job-ledger-operator"
	operatorJob.OperationDigest = testCommandRuntimeDigest("operation-operator")
	operatorJob.RequestFingerprint = testCommandRuntimeDigest("request-operator")
	operatorLease := acquireTestRunExecutionLease(t, ctx, st, startedRun.ID)
	operatorJob.LeaseID = operatorLease.LeaseID
	operatorJob.LeaseGeneration = operatorLease.Generation
	operatorJob.LeaseOwnerID = operatorLease.OwnerID
	operatorAttribution := domain.AgentAttribution{AgentID: root.ID,
		Source: domain.AgentAttributionOperatorRoot}
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, operatorJob,
		operatorAttribution); apperror.CodeOf(err) != apperror.CodePolicyDenied ||
		!strings.Contains(err.Error(), "live host provenance") {
		t.Fatalf("direct Store write forged operator provenance: %v", err)
	}
	if jobs, err := st.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{RunID: startedRun.ID, Limit: 10}); err != nil || len(jobs) != 1 {
		t.Fatalf("rejected operator write changed ledger: jobs=%#v err=%v", jobs, err)
	}
	expireTestRunExecutionLease(t, ctx, st, operatorLease)
	probe := &commandLedgerOperatorPrepareStore{SQLiteStore: st}
	manager, err := runner.NewPlatformCommandRuntimeManager(probe, "command-ledger-operator-owner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Error(err)
		}
	}()
	service, err := application.NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("native Go fixture is required", err)
	}
	operatorSpec := resolved.Spec
	operatorSpec.Executable, operatorSpec.Arguments = goExecutable, []string{"version"}
	operatorSpec.TimeoutMilliseconds = 10000
	request := application.OperatorCommandRequest{RunID: startedRun.ID,
		OperationKey: "command-ledger-native-operator", RequestedBy: "test_operator",
		Command: operatorSpec, ConfirmExecution: true}
	operatorResult, err := service.RunOperatorCommand(ctx, request)
	if err != nil || operatorResult.Replayed || operatorResult.Job.State != runner.CommandRuntimeJobCompleted ||
		!operatorResult.Job.TreeReaped || operatorResult.Job.PID <= 0 ||
		!strings.HasPrefix(operatorResult.Job.Stdout, "go version ") {
		t.Fatalf("native operator result=%#v err=%v", operatorResult, err)
	}
	if probe.prepares != 1 || !probe.privateProvenance || probe.attribution != operatorAttribution ||
		probe.prepared.ID != operatorResult.Job.ID {
		t.Fatalf("operator did not cross the real manager preparation boundary: %#v", probe)
	}
	operatorJob = probe.prepared
	var operatorInvocation int
	if err := st.db.QueryRowContext(ctx, `SELECT operator_invocation FROM command_runtime_jobs WHERE id = ?`,
		operatorJob.ID).Scan(&operatorInvocation); err != nil || operatorInvocation != 1 {
		t.Fatalf("stored operator invocation=%d err=%v", operatorInvocation, err)
	}
	storedOperator, err := st.GetThreadCommandRuntimeJobAgentAttribution(ctx,
		threadRecord.ID, operatorJob.ID)
	if err != nil || storedOperator != operatorAttribution ||
		storedOperator.AgentAttemptID != "" {
		t.Fatalf("stored operator-root attribution=%#v err=%v", storedOperator, err)
	}
	if replayedOperator, replayed, err := st.PrepareCommandRuntimeJobForAgent(ctx,
		operatorJob, operatorAttribution); err != nil || !replayed ||
		replayedOperator.ID != operatorJob.ID {
		t.Fatalf("replay operator-root=%#v replayed=%t err=%v",
			replayedOperator, replayed, err)
	}
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, operatorJob,
		rootAttribution); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("operator-root replay accepted Agent-attempt substitution: %v", err)
	}
	if replay, err := service.RunOperatorCommand(ctx, request); err != nil || !replay.Replayed ||
		replay.Job.ID != operatorResult.Job.ID || replay.Job.Version != operatorResult.Job.Version ||
		replay.Job.PID != operatorResult.Job.PID || probe.prepares != 1 {
		t.Fatalf("terminal operator replay restarted preparation: result=%#v prepares=%d err=%v", replay, probe.prepares, err)
	}
	supervisorJob := job
	supervisorLease := acquireTestRunExecutionLease(t, ctx, st, startedRun.ID)
	supervisorJob.LeaseID = supervisorLease.LeaseID
	supervisorJob.LeaseGeneration = supervisorLease.Generation
	supervisorJob.LeaseOwnerID = supervisorLease.OwnerID
	supervisorJob.ID = "command-job-ledger-supervisor-root"
	supervisorJob.OperationDigest = testCommandRuntimeDigest("operation-supervisor-root")
	supervisorJob.RequestFingerprint = testCommandRuntimeDigest("request-supervisor-root")
	if preparedSupervisor, replayed, err := st.PrepareCommandRuntimeJob(ctx,
		supervisorJob); err != nil || replayed || preparedSupervisor.ID != supervisorJob.ID {
		t.Fatalf("prepare inferred supervisor-root=%#v replayed=%t err=%v",
			preparedSupervisor, replayed, err)
	}
	storedSupervisor, err := st.GetThreadCommandRuntimeJobAgentAttribution(ctx,
		threadRecord.ID, supervisorJob.ID)
	if err != nil || storedSupervisor != (domain.AgentAttribution{
		AgentID: root.ID, AgentAttemptID: root.ActiveAttemptID,
		Source: domain.AgentAttributionSupervisorRoot}) {
		t.Fatalf("stored inferred supervisor-root attribution=%#v err=%v",
			storedSupervisor, err)
	}
	artifacts, err := st.CaptureToolOutput(ctx, artifact.CaptureRequest{
		RunID: terminal.RunID, SessionID: terminal.SessionID,
		WorkspaceID: terminal.WorkspaceID, SourceID: terminal.ID,
		ToolName: "command_runtime", Outputs: []artifact.Output{{
			Stream: artifact.StreamStdout, MIME: "text/plain; charset=utf-8",
			Content: terminal.Stdout,
		}},
	})
	if err != nil || len(artifacts) != 1 || artifacts[0].SHA256 != artifact.Hash(terminal.Stdout) {
		t.Fatalf("command runtime output artifact=%#v err=%v", artifacts, err)
	}
	if _, err := st.db.ExecContext(ctx,
		`DELETE FROM command_runtime_jobs WHERE id = ?`, terminal.ID); err == nil {
		t.Fatal("terminal command runtime audit row was deleted")
	}
	mutated := terminal
	mutated.OwnerID = strings.Repeat("x", 8)
	mutated.Version++
	mutated.UpdatedAt = completedAt.Add(time.Millisecond)
	if _, err := st.UpdateCommandRuntimeJob(ctx, mutated, terminal.Version); err == nil {
		t.Fatal("immutable command runtime ownership was changed")
	}
}

// This connector uses the production SQLite driver, with one scheduling point
// after an UPDATE and before the store reads the resulting row back.
type commandRuntimeAfterUpdateConnector struct {
	dsn         string
	afterUpdate func(driver.Result)
}

func (c commandRuntimeAfterUpdateConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &commandRuntimeAfterUpdateConn{SQLiteConn: conn.(*sqlite3.SQLiteConn), afterUpdate: c.afterUpdate}, nil
}

func (commandRuntimeAfterUpdateConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }

type commandRuntimeAfterUpdateConn struct {
	*sqlite3.SQLiteConn
	afterUpdate func(driver.Result)
}

func (c *commandRuntimeAfterUpdateConn) ExecContext(ctx context.Context, query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	result, err := c.SQLiteConn.ExecContext(ctx, query, args)
	if err == nil && strings.HasPrefix(query, "UPDATE command_runtime_jobs SET") {
		c.afterUpdate(result)
	}
	return result, err
}

// Observe the public manager/store boundary without fabricating its private
// preparation marker or substituting a fake process starter.
type commandLedgerOperatorPrepareStore struct {
	*SQLiteStore
	prepared          runner.CommandRuntimeJob
	attribution       domain.AgentAttribution
	privateProvenance bool
	prepares          int
}

func (s *commandLedgerOperatorPrepareStore) PrepareCommandRuntimeJobForAgent(ctx context.Context,
	job runner.CommandRuntimeJob, attribution domain.AgentAttribution,
) (runner.CommandRuntimeJob, bool, error) {
	s.prepares++
	s.prepared, s.attribution = job, attribution
	s.privateProvenance = runner.OperatorCommandJobPrepared(ctx, job)
	return s.SQLiteStore.PrepareCommandRuntimeJobForAgent(ctx, job, attribution)
}

func testCommandRuntimeDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
