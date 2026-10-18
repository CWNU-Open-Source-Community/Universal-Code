package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestOperatorCommandCheckpointWaitsForPreparationFinalizer(t *testing.T) {
	f := newOperatorCommandFixture(t, domain.RunExecutionPermissionAsk)
	request := f.request(t, true)
	finalizing, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	type outcome struct {
		value OperatorCommandResult
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := f.service.runPreparedOperatorCommand(t.Context(), request, nil, nil, func(ctx context.Context, root string) error {
			close(finalizing)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return os.WriteFile(filepath.Join(root, "finalized-bin.txt"), []byte("owned preparation finalizer"), 0600)
		})
		done <- outcome{value, err}
	}()
	select {
	case <-finalizing:
	case <-time.After(20 * time.Second):
		t.Fatal("command never reached finalizer")
	}
	if _, err := f.service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	transactions, err := f.st.ListWorkspaceCheckpointTransactions(t.Context(), f.run.ID, 10)
	if err != nil || len(transactions) != 1 || transactions[0].Status != workspacecheckpoint.TransactionPrepared {
		t.Fatalf("reconciler sealed before finalizer: %+v %v", transactions, err)
	}
	close(release)
	result := <-done
	if result.err != nil || result.value.Job.State != runner.CommandRuntimeJobCompleted {
		t.Fatalf("prepared command %+v %v", result.value, result.err)
	}
	transactions, err = f.st.ListWorkspaceCheckpointTransactions(t.Context(), f.run.ID, 10)
	if err != nil || len(transactions) != 1 || transactions[0].Status != workspacecheckpoint.TransactionCompleted {
		t.Fatalf("completion %+v %v", transactions, err)
	}
	snapshot, err := f.st.GetWorkspaceCheckpointSnapshot(t.Context(), transactions[0].AfterCheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range snapshot.Entries {
		if entry.Path == "finalized-bin.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("after checkpoint omitted preparation finalizer output")
	}
	if _, err := f.service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !f.manager.Available() {
		t.Fatal("successful completion shut down manager")
	}
}

type operatorIncompleteReceiptStore struct {
	*store.SQLiteStore
	state  runner.CommandRuntimeJobState
	reaped bool
}

func (s operatorIncompleteReceiptStore) GetCommandRuntimeJob(ctx context.Context, id string) (runner.CommandRuntimeJob, error) {
	job, err := s.SQLiteStore.GetCommandRuntimeJob(ctx, id)
	if err == nil && job.State == runner.CommandRuntimeJobCompleted {
		job.State, job.TreeReaped = s.state, s.reaped
		job.Cancelled = s.state == runner.CommandRuntimeJobCancelled
	}
	return job, err
}

func TestOperatorCommandPreparationFinalizerRequiresCompletedReapedJob(t *testing.T) {
	for _, test := range []struct {
		name   string
		state  runner.CommandRuntimeJobState
		reaped bool
	}{
		{"cancelled exit zero", runner.CommandRuntimeJobCancelled, true},
		{"failed exit zero", runner.CommandRuntimeJobFailed, true},
		{"unreaped exit zero", runner.CommandRuntimeJobCompleted, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOperatorCommandFixture(t, domain.RunExecutionPermissionAsk)
			f.service.store = operatorIncompleteReceiptStore{SQLiteStore: f.st, state: test.state, reaped: test.reaped}
			called := false
			result, err := f.service.runPreparedOperatorCommand(t.Context(), f.request(t, true), nil, nil, func(context.Context, string) error { called = true; return nil })
			if err != nil || result.Job.State != test.state || result.Job.ExitCode == nil || *result.Job.ExitCode != 0 {
				t.Fatalf("incomplete receipt %+v %v", result, err)
			}
			if called {
				t.Fatal("preparation finalizer ran without completed, reaped success")
			}
		})
	}
}

type operatorCommandFixture struct {
	st         *store.SQLiteStore
	probe      *commandApprovalPrepareStore
	service    *CommandRuntimeService
	manager    *runner.CommandRuntimeManager
	caps       domain.ExecutionPermissionRuntimeCapabilities
	checker    *commandApprovalChecker
	run        domain.Run
	root, path string
}

func newOperatorCommandFixture(t *testing.T, mode domain.RunExecutionPermissionMode) *operatorCommandFixture {
	return newOperatorCommandFixtureAtStatus(t, mode, domain.RunRunning)
}

func newOperatorCommandFixtureAtStatus(t *testing.T, mode domain.RunExecutionPermissionMode, status domain.RunStatus) *operatorCommandFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &operatorCommandFixture{root: root, path: filepath.Join(t.TempDir(), "operator.db")}
	f.st, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	workspace := store.WorkspaceRecord{ID: "operator-workspace", Name: "operator fixture", RootPath: f.root}
	if err := f.st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(f.st)
	_, f.run, err = runs.Create(t.Context(), CreateRunRequest{Goal: "Run the exact operator command", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxTokens: 10000, MaxToolCalls: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunExecutionProfileService(f.st).Change(t.Context(), ChangeRunExecutionProfileRequest{RunID: f.run.ID, Profile: "local", OperationKey: "operator-profile", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	f.caps = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if mode != domain.RunExecutionPermissionAsk {
		if _, err = NewRunExecutionPermissionService(f.st, f.caps).Change(t.Context(), ChangeRunExecutionPermissionRequest{RunID: f.run.ID, Mode: string(mode), ConfirmFull: mode == domain.RunExecutionPermissionFull, OperationKey: "operator-mode-selection", RequestedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	if status != domain.RunCreated {
		if f.run, err = runs.Start(t.Context(), f.run.ID); err != nil {
			t.Fatal(err)
		}
		if status == domain.RunPaused {
			if f.run, err = runs.Pause(t.Context(), f.run.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.probe = &commandApprovalPrepareStore{SQLiteStore: f.st}
	f.manager, err = runner.NewPlatformCommandRuntimeManager(f.probe, idgen.New("operator-owner"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := f.manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	f.service, err = NewCommandRuntimeService(f.st, f.manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	f.checker = &commandApprovalChecker{Checker: policy.NewDefaultChecker()}
	f.service.SetCommandRuntimePolicy(f.checker)
	return f
}

func (f *operatorCommandFixture) request(t *testing.T, confirmed bool) OperatorCommandRequest {
	t.Helper()
	return OperatorCommandRequest{RunID: f.run.ID, OperationKey: "operator-native-once", RequestedBy: "operator", Command: commandApprovalNativeInput(t, false).Commands[0], ConfirmExecution: confirmed}
}

func (f *operatorCommandFixture) noProcess(t *testing.T) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(f.root, "count.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected native effect %q %v", b, err)
	}
}

func TestOperatorCommandThreeModesShareNativeAuthorization(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newOperatorCommandFixture(t, mode)
			if mode != domain.RunExecutionPermissionFull {
				if _, err := f.service.RunOperatorCommand(t.Context(), f.request(t, false)); err == nil {
					t.Fatal("unreviewed host operation allowed")
				}
				f.noProcess(t)
				jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.run.ID, Limit: 10})
				if err != nil || len(jobs) != 0 {
					t.Fatalf("missing approval consumed operation: %v %v", jobs, err)
				}
			}
			request := f.request(t, mode != domain.RunExecutionPermissionFull)
			request.Command.TimeoutMilliseconds = 120_000
			request.Command.Output = runner.CommandRuntimeOutputPolicy{InlineBytes: 64 * 1024, ArtifactBytes: 64 * 1024}
			result, err := f.service.RunOperatorCommand(t.Context(), request)
			if err != nil || result.Replayed || result.Job.State != runner.CommandRuntimeJobCompleted || !result.Job.TreeReaped ||
				result.Job.TimeoutMilliseconds != request.Command.TimeoutMilliseconds || result.Job.InlineLimitBytes != request.Command.Output.InlineBytes {
				t.Fatalf("operator result=%+v err=%v", result, err)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.run.ID, Limit: 10})
			if err != nil || len(jobs) != 1 || jobs[0].ID != result.Job.ID {
				t.Fatalf("operator command did not retain one Job: %+v err=%v", jobs, err)
			}
			db, err := sql.Open("sqlite3", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var actor, source string
			var attempt sql.NullString
			err = db.QueryRowContext(t.Context(), `SELECT agent_id,agent_attempt_id,attribution_source FROM command_runtime_job_agents WHERE job_id=?`, result.Job.ID).Scan(&actor, &attempt, &source)
			if err != nil || actor != result.Job.RootAgentID || source != string(domain.AgentAttributionOperatorRoot) || attempt.Valid {
				t.Fatalf("operator attribution=%q %q %+v %v", actor, source, attempt, err)
			}
			if b, err := os.ReadFile(filepath.Join(f.root, "count.txt")); err != nil || string(b) != "1" {
				t.Fatalf("native marker=%q %v", b, err)
			}
			// Completed replay does not acquire a lease or reactivate Full.
			f.caps.RuntimeAuthority.RevokeRun(f.run.ID)
			if err := f.manager.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := f.st.Close(); err != nil {
				t.Fatal(err)
			}
			f.st, err = store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			cold := &CommandRuntimeService{store: f.st}
			replay, err := cold.RunOperatorCommand(t.Context(), request)
			if err != nil || !replay.Replayed || replay.Job.ID != result.Job.ID {
				t.Fatalf("cold replay=%+v %v", replay, err)
			}
			request.Command.Arguments = append(request.Command.Arguments, "changed")
			if _, err := cold.RunOperatorCommand(t.Context(), request); err == nil {
				t.Fatal("changed replay request accepted")
			}
			if b, _ := os.ReadFile(filepath.Join(f.root, "count.txt")); string(b) != "1" {
				t.Fatalf("replay repeated native effect %q", b)
			}
		})
	}
}

func TestOperatorCommandPolicyAndColdFullCannotBeOverridden(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newOperatorCommandFixture(t, mode)
			f.checker.deny = true
			if _, err := f.service.RunOperatorCommand(t.Context(), f.request(t, true)); err == nil {
				t.Fatal("operator confirmation overrode host policy")
			}
			f.noProcess(t)
			if mode == domain.RunExecutionPermissionFull {
				f.checker.deny = false
				f.caps.RuntimeAuthority.RevokeRun(f.run.ID)
				if _, err := f.service.RunOperatorCommand(t.Context(), f.request(t, true)); err == nil {
					t.Fatal("command confirmation reactivated Full")
				}
				f.noProcess(t)
			}
		})
	}
}

func TestOperatorCommandStoppedRunRequiresPrivateConsentAndPreservesState(t *testing.T) {
	for _, status := range []domain.RunStatus{domain.RunCreated, domain.RunPaused} {
		for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
			t.Run(string(status)+"/"+string(mode), func(t *testing.T) {
				f := newOperatorCommandFixtureAtStatus(t, mode, status)
				request := f.request(t, true)
				lease, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.run.ID, OwnerID: "public-scope-owner", TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				mission, err := f.st.GetMission(t.Context(), f.run.MissionID)
				if err != nil {
					t.Fatal(err)
				}
				root, found, err := f.st.GetRootAgent(t.Context(), f.run.ID)
				if err != nil || !found {
					t.Fatal("root", err)
				}
				permission, err := f.st.GetRunExecutionPermission(t.Context(), f.run.ID)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, generation, epoch, fence, live := bindAgentCodeRuntime(f.caps, permission)
				if !live {
					t.Fatal("fixture runtime is not live")
				}
				adapter, available := f.manager.AdapterIdentity()
				if !available {
					t.Fatal("fixture adapter unavailable")
				}
				runMode, err := f.st.GetRunMode(t.Context(), f.run.ID)
				if err != nil {
					t.Fatal(err)
				}
				scope := toolgateway.CommandRuntimeContext{InvocationID: "public-stopped-invocation", OperationKey: "public-stopped-key",
					RunID: f.run.ID, MissionID: mission.ID, SessionID: f.run.SessionID, WorkspaceID: mission.WorkspaceID,
					RootAgentID: root.ID, AgentID: root.ID, PermissionMode: permission.Mode, PermissionRevision: permission.Revision,
					PermissionSnapshotID: snapshot, PermissionGeneration: generation, PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence,
					LeaseID: lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation, CapabilityGeneration: adapter.Generation,
					Surface: runMode.Surface, Phase: runMode.Phase, Profile: runMode.Profile, Role: domain.AgentRoleRoot, ModeRevision: runMode.Revision,
					Adapter: adapter, PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "high", Reason: "check stopped Run gate"}}
				limit := request.Command.Output.InlineBytes
				input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionRun,
					Commands: []runner.CommandRuntimeSpec{request.Command}, FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &limit}
				boundary := f.service.commandRuntimeBoundaryRequest(scope, "public-checkpoint-key", "public-checkpoint-receipt")
				if _, err := f.service.checkpoints.BeginBoundary(t.Context(), boundary); err == nil || !strings.Contains(err.Error(), "execution lease is stale") {
					t.Fatal("checkpoint accepted a stopped Run without private consent", err)
				}
				for _, source := range []string{"run_supervisor", toolgateway.CommandRuntimeRequestedByOperator} {
					scope.RequestedBy = source
					scope.AgentAttemptID = ""
					if source == "run_supervisor" {
						scope.AgentAttemptID = "untrusted-attempt"
					}
					if err := scope.Validate(); err != nil {
						t.Fatal("invalid public scope fixture", err)
					}
					if _, err := f.service.ExecuteCommandRuntime(t.Context(), scope, input); err == nil || !strings.Contains(err.Error(), "durable binding is stale") {
						t.Fatalf("public source %q bypassed stopped Run boundary: %v", source, err)
					}
				}
				if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
					t.Fatal(err)
				}
				if _, err := f.service.RunOperatorCommand(t.Context(), f.request(t, false)); err == nil {
					t.Fatal("stopped Run executed without exact confirmation")
				}
				f.noProcess(t)
				result, err := f.service.RunOperatorCommand(t.Context(), request)
				if err != nil || result.Replayed || result.Job.State != runner.CommandRuntimeJobCompleted || !result.Job.TreeReaped {
					t.Fatalf("confirmed stopped command: %+v %v", result, err)
				}
				db, err := sql.Open("sqlite3", f.path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var operator, oldIntents int
				if err := db.QueryRowContext(t.Context(), `SELECT operator_invocation FROM command_runtime_jobs WHERE id=?`, result.Job.ID).Scan(&operator); err != nil || operator != 1 {
					t.Fatal("Job provenance", operator, err)
				}
				if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM controlled_command_execution_intents`).Scan(&oldIntents); err != nil || oldIntents != 0 {
					t.Fatal("second execution ledger written", oldIntents, err)
				}
				f.caps.RuntimeAuthority.RevokeRun(f.run.ID)
				cold := &CommandRuntimeService{store: f.st}
				replay, err := cold.RunOperatorCommand(t.Context(), request)
				if err != nil || !replay.Replayed || replay.Job.ID != result.Job.ID {
					t.Fatal("cold replay", replay, err)
				}
				run, err := f.st.GetRun(t.Context(), f.run.ID)
				if err != nil || run.Status != status {
					t.Fatal("Run state changed", run, err)
				}
				if b, err := os.ReadFile(filepath.Join(f.root, "count.txt")); err != nil || string(b) != "1" {
					t.Fatalf("execution count %q %v", b, err)
				}
			})
		}
	}
}

// Retain the exact authority and ownership checks, but preserve their timings
// when a native Windows Job is interrupted before its command timeout.
type fixedOperatorDiagnosticStore struct {
	*commandApprovalPrepareStore
	mu            sync.Mutex
	entries       []string
	nativeEntries []string
	nativeJobs    chan runner.CommandRuntimeJob
	nativeStop    chan struct{}
	nativeStopped chan struct{}
	nativeSampled chan struct{}
}

func (s *fixedOperatorDiagnosticStore) record(ctx context.Context, operation string, started time.Time, detail string, err error) {
	remaining := "none"
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline).String()
	}
	entry := fmt.Sprintf("%s %s elapsed=%s deadline_remaining=%s context_error=%v error=%v %s",
		time.Now().UTC().Format(time.RFC3339Nano), operation, time.Since(started), remaining, ctx.Err(), err, detail)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	if len(s.entries) > 64 {
		s.entries = s.entries[len(s.entries)-64:]
	}
}

func (s *fixedOperatorDiagnosticStore) GetRun(ctx context.Context, id string) (domain.Run, error) {
	started := time.Now()
	run, err := s.SQLiteStore.GetRun(ctx, id)
	s.record(ctx, "GetRun", started, "status="+string(run.Status), err)
	return run, err
}

func (s *fixedOperatorDiagnosticStore) GetRunExecutionLease(ctx context.Context, id string) (domain.RunExecutionLease, bool, error) {
	started := time.Now()
	lease, found, err := s.SQLiteStore.GetRunExecutionLease(ctx, id)
	s.record(ctx, "GetRunExecutionLease", started, fmt.Sprintf("found=%t generation=%d status=%s expires_at=%s", found, lease.Generation, lease.Status, lease.ExpiresAt.UTC().Format(time.RFC3339Nano)), err)
	return lease, found, err
}

func (s *fixedOperatorDiagnosticStore) RenewRunExecutionLease(ctx context.Context, expected domain.RunExecutionLease, ttl time.Duration) (domain.RunExecutionLease, error) {
	started := time.Now()
	lease, err := s.SQLiteStore.RenewRunExecutionLease(ctx, expected, ttl)
	s.record(ctx, "RenewRunExecutionLease", started, fmt.Sprintf("expected_generation=%d generation=%d status=%s expires_at=%s", expected.Generation, lease.Generation, lease.Status, lease.ExpiresAt.UTC().Format(time.RFC3339Nano)), err)
	return lease, err
}

func (s *fixedOperatorDiagnosticStore) ReleaseRunExecutionLease(ctx context.Context, expected domain.RunExecutionLease) (domain.RunExecutionLease, bool, error) {
	started := time.Now()
	lease, replayed, err := s.SQLiteStore.ReleaseRunExecutionLease(ctx, expected)
	s.record(ctx, "ReleaseRunExecutionLease", started, fmt.Sprintf("expected_generation=%d generation=%d status=%s replayed=%t", expected.Generation, lease.Generation, lease.Status, replayed), err)
	return lease, replayed, err
}

func (s *fixedOperatorDiagnosticStore) GetRunExecutionInteraction(ctx context.Context, id string) (domain.RunExecutionInteractionSnapshot, error) {
	started := time.Now()
	interaction, err := s.SQLiteStore.GetRunExecutionInteraction(ctx, id)
	s.record(ctx, "GetRunExecutionInteraction", started, fmt.Sprintf("revision=%d mode=%s", interaction.Revision, interaction.Mode), err)
	return interaction, err
}

func (s *fixedOperatorDiagnosticStore) UpdateCommandRuntimeJob(ctx context.Context, job runner.CommandRuntimeJob, previous int64) (runner.CommandRuntimeJob, error) {
	started := time.Now()
	updated, err := s.SQLiteStore.UpdateCommandRuntimeJob(ctx, job, previous)
	s.record(ctx, "UpdateCommandRuntimeJob", started, fmt.Sprintf("state=%s previous_version=%d renewed_at=%s expires_at=%s", job.State, previous, job.OwnerRenewedAt.UTC().Format(time.RFC3339Nano), job.OwnerExpiresAt.UTC().Format(time.RFC3339Nano)), err)
	// Kernel queries run in one separate test worker, never under the manager's
	// heartbeat locks/deadline. A full one-slot queue simply drops this sample.
	s.enqueueNativeSample(job)
	return updated, err
}

func (s *fixedOperatorDiagnosticStore) startNativeSamples() {
	s.nativeJobs = make(chan runner.CommandRuntimeJob, 1)
	s.nativeStop = make(chan struct{})
	s.nativeStopped = make(chan struct{})
	s.nativeSampled = make(chan struct{}, 1)
	go func() {
		defer close(s.nativeStopped)
		for {
			select {
			case <-s.nativeStop:
				return
			case job := <-s.nativeJobs:
				select {
				case <-s.nativeStop:
					return
				default:
				}
				if sample := fixedOperatorNativeProcessSample(job); sample != "" {
					s.mu.Lock()
					s.nativeEntries = append(s.nativeEntries, sample)
					if len(s.nativeEntries) > 16 {
						s.nativeEntries = s.nativeEntries[len(s.nativeEntries)-16:]
					}
					s.mu.Unlock()
					select {
					case s.nativeSampled <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
}

func (s *fixedOperatorDiagnosticStore) enqueueNativeSample(job runner.CommandRuntimeJob) {
	if job.State != runner.CommandRuntimeJobRunning || job.PID <= 0 {
		return
	}
	select {
	case <-s.nativeStop:
		return
	default:
	}
	select {
	case s.nativeJobs <- job:
	default:
	}
}

func (s *fixedOperatorDiagnosticStore) stopNativeSamples() {
	close(s.nativeStop)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-s.nativeStopped:
	case <-timer.C:
		// A diagnostic cannot delay fixture cleanup indefinitely or change the
		// original test result. The worker only holds its own query handle.
		s.mu.Lock()
		s.nativeEntries = append(s.nativeEntries, "native diagnostic worker did not finish within the cleanup budget")
		s.mu.Unlock()
	}
}

func newFixedOperatorFixture(t *testing.T, status domain.RunStatus, kind runner.ControlledCommandKind, timeout time.Duration) (*operatorCommandFixture, OperatorCommandRequest) {
	t.Helper()
	initial := status
	if status == domain.RunRunning {
		initial = domain.RunCreated
	}
	f := newOperatorCommandFixtureAtStatus(t, domain.RunExecutionPermissionAsk, initial)
	interaction, err := NewRunExecutionInteractionService(f.st).Change(t.Context(), ChangeRunExecutionInteractionRequest{RunID: f.run.ID, Mode: "controlled", Trust: "trusted", ConfirmWorkspaceTrust: true, OperationKey: "fixed-command-interaction", RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if status == domain.RunRunning {
		if f.run, err = NewRunService(f.st).Start(t.Context(), f.run.ID); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := f.st.GetRunExecutionProfile(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runner.PlanControlledCommand(runner.ControlledCommandPlanRequest{ID: "fixed-command-plan", WorkspaceID: "operator-workspace", WorkspaceRoot: f.root,
		Interaction: interaction.Interaction, CurrentProfile: profile, CurrentSurface: domain.ExecutionSurfaceCode, Kind: kind, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	diagnostics := &fixedOperatorDiagnosticStore{commandApprovalPrepareStore: f.probe}
	diagnostics.startNativeSamples()
	t.Cleanup(func() {
		diagnostics.stopNativeSamples()
		if !t.Failed() {
			return
		}
		diagnostics.mu.Lock()
		entries := append([]string(nil), diagnostics.entries...)
		nativeEntries := append([]string(nil), diagnostics.nativeEntries...)
		diagnostics.mu.Unlock()
		for _, entry := range entries {
			t.Log("fixed operator diagnostic:", entry)
		}
		for _, entry := range nativeEntries {
			t.Log("fixed operator native diagnostic:", entry)
		}
	})
	var command runner.CommandRuntimeSpec
	f.manager, command, err = runner.NewFixedCommandRuntimeManager(diagnostics, "fixed-command-owner", plan, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.caps.DangerFullAccessEnabled = false
	f.service, err = NewCommandRuntimeService(diagnostics, f.manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	request := OperatorCommandRequest{RunID: f.run.ID, OperationKey: "fixed-command-once", RequestedBy: "operator", Command: command, ConfirmExecution: true}
	return f, request
}

func TestOperatorCommandDrainsMultipleOutputPages(t *testing.T) {
	// Paging uses the shared operator Job path. Native fixed-command isolation
	// is covered separately, without making output size depend on PowerShell's
	// cold startup and directory enumeration on hosted Windows runners.
	f := newOperatorCommandFixtureAtStatus(t, domain.RunExecutionPermissionAsk, domain.RunCreated)
	request := f.request(t, true)
	request.Command.Arguments = []string{"-e", `require('fs').appendFileSync('count.txt','1');process.stdout.write(('page-'+'x'.repeat(100)+'\n').repeat(400));process.stderr.write('paging-complete\n');`}
	request.Command.TimeoutMilliseconds = 60_000
	request.Command.Output = runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MaxControlledOutputCaptureBytes, ArtifactBytes: runner.MaxControlledOutputCaptureBytes}
	wantStdout := strings.Repeat("page-"+strings.Repeat("x", 100)+"\n", 400)
	const wantStderr = "paging-complete\n"
	if len(wantStdout) <= toolgateway.MaxCommandRuntimePageBytes || len(wantStdout) > runner.MaxControlledOutputCaptureBytes {
		t.Fatal("fixture must span multiple pages within the capture limit")
	}
	result, err := f.service.RunOperatorCommand(t.Context(), request)
	if err != nil || result.Job.State != runner.CommandRuntimeJobCompleted || !result.Job.TreeReaped {
		t.Fatal("paged operator command", result, err)
	}
	if result.Job.Stdout != wantStdout || result.Job.Stderr != wantStderr {
		t.Fatalf("output pages changed: stdout bytes=%d stderr=%q", len(result.Job.Stdout), result.Job.Stderr)
	}
	f.caps.RuntimeAuthority.RevokeRun(f.run.ID)
	replay, found, err := ReadOperatorCommand(t.Context(), f.st, request)
	if err != nil || !found || !replay.Replayed || replay.Job.ID != result.Job.ID || replay.Job.Stdout != wantStdout || replay.Job.Stderr != wantStderr {
		t.Fatal("paged output replay changed", replay, found, err)
	}
	if marker, err := os.ReadFile(filepath.Join(f.root, "count.txt")); err != nil || string(marker) != "1" {
		t.Fatalf("paged command was not executed exactly once: %q %v", marker, err)
	}
}

func TestOperatorFixedCommandUsesSharedJobLedger(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fixed restricted native adapter is Windows-only")
	}
	for _, status := range []domain.RunStatus{domain.RunCreated, domain.RunPaused, domain.RunRunning} {
		t.Run(string(status), func(t *testing.T) {
			f, request := newFixedOperatorFixture(t, status, runner.ControlledCommandGoVersion, 0)
			if request.Command.TimeoutMilliseconds != 30_000 {
				t.Fatal("fixed default timeout changed")
			}
			result, err := f.service.RunOperatorCommand(t.Context(), request)
			if err != nil || result.Replayed || result.Job.State != runner.CommandRuntimeJobCompleted || !result.Job.TreeReaped || !strings.HasPrefix(result.Job.Stdout, "go version ") || result.Job.Adapter.BackendIdentity != runner.RestrictedFixedCommandBackend {
				t.Fatalf("fixed native Job %+v %v", result, err)
			}
			f.caps.RuntimeAuthority.RevokeRun(f.run.ID)
			replay, found, err := ReadOperatorCommand(t.Context(), f.st, request)
			if err != nil || !found || !replay.Replayed || replay.Job.ID != result.Job.ID {
				t.Fatal("fixed receipt replay", replay, found, err)
			}
			run, err := f.st.GetRun(t.Context(), f.run.ID)
			if err != nil || run.Status != status {
				t.Fatal("fixed command changed Run", run, err)
			}
		})
	}
	t.Run("maximum-timeout", func(t *testing.T) {
		f, request := newFixedOperatorFixture(t, domain.RunCreated, runner.ControlledCommandGoVersion, 2*time.Minute)
		result, err := f.service.RunOperatorCommand(t.Context(), request)
		if err != nil || result.Job.State != runner.CommandRuntimeJobCompleted || result.Job.TimeoutMilliseconds != 120_000 || !result.Job.TreeReaped {
			t.Fatal("fixed maximum timeout", result, err)
		}
	})
	t.Run("native-timeout", func(t *testing.T) {
		f, request := newFixedOperatorFixture(t, domain.RunPaused, runner.ControlledCommandPowerShellWorkspaceList, time.Millisecond)
		result, err := f.service.RunOperatorCommand(t.Context(), request)
		if err != nil || result.Job.State != runner.CommandRuntimeJobTimedOut || !result.Job.TreeReaped {
			t.Fatal("fixed timeout did not reap its Job", result, err)
		}
	})
	t.Run("cancel-running-handoff", func(t *testing.T) {
		f, request := newFixedOperatorFixture(t, domain.RunPaused, runner.ControlledCommandGoVersion, 0)
		plan, _ := f.manager.FixedCommandPlan()
		if err := f.manager.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var err error
		f.manager, request.Command, err = runner.NewFixedCommandRuntimeManager(&operatorCancelHandoffStore{SQLiteStore: f.st, cancel: cancel}, "fixed-handoff-owner", plan, f.root)
		if err != nil {
			t.Fatal(err)
		}
		f.service, err = NewCommandRuntimeService(f.st, f.manager, f.caps)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RunOperatorCommand(ctx, request); !errors.Is(err, context.Canceled) {
			t.Fatal("fixed handoff lost client cancellation", err)
		}
		jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.run.ID, Limit: 10})
		if err != nil || len(jobs) != 1 || !jobs[0].TreeReaped {
			t.Fatal("fixed handoff did not reap exactly one Job", jobs, err)
		}
		// go version can finish before foreground cancellation reaches Stop.
		// Preserve that successful receipt; cancellation must still reap a live Job.
		switch job := jobs[0]; job.State {
		case runner.CommandRuntimeJobCancelled:
		case runner.CommandRuntimeJobCompleted:
			if job.ExitCode == nil || *job.ExitCode != 0 || !strings.HasPrefix(job.Stdout, "go version ") {
				t.Fatal("fixed handoff lost successful completion", job)
			}
		default:
			t.Fatal("fixed handoff ended in an unexpected state", job)
		}
		replay, found, err := ReadOperatorCommand(t.Context(), f.st, request)
		if err != nil || !found || !replay.Replayed || replay.Job.ID != jobs[0].ID || replay.Job.State != jobs[0].State {
			t.Fatal("fixed command handoff replay changed", replay, found, err)
		}
	})

}

func TestOperatorCommandRechecksRevocationAndLeaseBeforeNativeStart(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		for _, status := range []domain.RunStatus{domain.RunRunning, domain.RunCreated, domain.RunPaused} {
			if mode != domain.RunExecutionPermissionAuto && status != domain.RunRunning {
				continue
			}
			for _, change := range []string{"revoke", "lease", "cancel"} {
				t.Run(string(mode)+"/"+string(status)+"/"+change, func(t *testing.T) {
					f := newOperatorCommandFixtureAtStatus(t, mode, status)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					f.probe.afterPrepare = func() {
						switch change {
						case "revoke":
							f.caps.RuntimeAuthority.RevokeRun(f.run.ID)
						case "lease":
							lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.run.ID)
							if err != nil {
								t.Fatal(err)
							}
							if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
								t.Fatal(err)
							}
						case "cancel":
							cancel()
						}
					}
					if _, err := f.service.RunOperatorCommand(ctx, f.request(t, true)); err == nil {
						t.Fatal("stale native admission succeeded")
					}
					f.noProcess(t)
				})
			}
		}
	}
}

func TestOperatorCommandCannotBorrowLeaseOrChangeRunState(t *testing.T) {
	f := newOperatorCommandFixture(t, domain.RunExecutionPermissionAuto)
	lease, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.run.ID, OwnerID: "other-owner", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RunOperatorCommand(t.Context(), f.request(t, true)); err == nil {
		t.Fatal("borrowed another worker lease")
	}
	current, _, err := f.st.GetRunExecutionLease(t.Context(), f.run.ID)
	if err != nil || current.LeaseID != lease.Lease.LeaseID || current.OwnerID != "other-owner" {
		t.Fatalf("lease changed %+v %v", current, err)
	}
	if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunService(f.st).Pause(t.Context(), f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RunOperatorCommand(t.Context(), f.request(t, false)); err == nil {
		t.Fatal("unconfirmed paused Run executed")
	}
	run, err := f.st.GetRun(t.Context(), f.run.ID)
	if err != nil || run.Status != domain.RunPaused {
		t.Fatalf("Run state changed %+v %v", run, err)
	}
	f.noProcess(t)
}

func TestOperatorCommandScopeCannotForgeOperatorConsent(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalFixture(t, mode, false)
			f.record(t, commandApprovalNativeInput(t, false), 1)
			scope, input := f.scope(t)
			scope.RequestedBy = toolgateway.CommandRuntimeRequestedByOperator
			scope.AgentAttemptID = ""
			if _, err := f.service.ExecuteCommandRuntime(t.Context(), scope, input); err == nil {
				t.Fatal("serialized operator claim forged review")
			}
			f.assertNoMarker(t, "count.txt")
		})
	}
}
