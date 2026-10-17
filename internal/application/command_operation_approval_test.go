package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/outputsafe"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type commandApprovalChecker struct {
	policy.Checker
	require, deny bool
}

func (c *commandApprovalChecker) CheckToolCall(call tools.Call) policy.Decision {
	if call.Name == string(toolgateway.CommandRuntimeTool) {
		return policy.Decision{Allowed: !c.deny, NeedsApproval: c.require, Risk: "high", Reason: "fixture command host policy"}
	}
	return c.Checker.CheckToolCall(call)
}

type commandApprovalPrepareStore struct {
	*store.SQLiteStore
	afterPrepare func()
}

func (s *commandApprovalPrepareStore) PrepareCommandRuntimeJobForAgent(ctx context.Context, job runner.CommandRuntimeJob, actor domain.AgentAttribution) (runner.CommandRuntimeJob, bool, error) {
	value, replayed, err := s.SQLiteStore.PrepareCommandRuntimeJobForAgent(ctx, job, actor)
	if err == nil && !replayed && s.afterPrepare != nil {
		s.afterPrepare()
	}
	return value, replayed, err
}

type commandApprovalFixture struct {
	st            *store.SQLiteStore
	preparedStore *commandApprovalPrepareStore
	supervisor    *AgentRunner
	service       *CommandRuntimeService
	turn          domain.SupervisorTurn
	call          domain.SupervisorToolCall
	caps          domain.ExecutionPermissionRuntimeCapabilities
	checker       *commandApprovalChecker
	root          string
	path          string
	manager       *runner.CommandRuntimeManager
}

func newCommandApprovalFixture(t *testing.T, mode domain.RunExecutionPermissionMode, require bool, phases ...domain.ExecutionPhase) *commandApprovalFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &commandApprovalFixture{root: root, checker: &commandApprovalChecker{Checker: policy.NewDefaultChecker(), require: require}}
	f.path = filepath.Join(t.TempDir(), "command-approval.db")
	f.st, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	workspace := store.WorkspaceRecord{ID: "command-approval-workspace", Name: "owned process fixture", RootPath: f.root}
	if err = f.st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(f.st)
	phase := domain.ExecutionPhaseDeliver
	if len(phases) > 0 {
		phase = phases[0]
	}
	_, run, err := runs.Create(t.Context(), CreateRunRequest{Goal: "Test exact native process review", Profile: "code", Surface: "code", Phase: string(phase), WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxTokens: 10000, MaxToolCalls: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunExecutionProfileService(f.st).Change(t.Context(), ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "command-approval-profile", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	f.caps = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if mode != domain.RunExecutionPermissionAsk {
		if _, err = NewRunExecutionPermissionService(f.st, f.caps).Change(t.Context(), ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: string(mode), ConfirmFull: mode == domain.RunExecutionPermissionFull, OperationKey: "command-approval-mode", RequestedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "command-approval-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = f.st.BeginSupervisorTurn(t.Context(), lease.Lease, "execute the fixed command approval fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.preparedStore = &commandApprovalPrepareStore{SQLiteStore: f.st}
	manager, err := runner.NewPlatformCommandRuntimeManager(f.preparedStore, idgen.New("command-approval-owner"))
	if err != nil {
		t.Fatal(err)
	}
	f.manager = manager
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := f.manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	f.service, err = NewCommandRuntimeService(f.st, manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.service)
	return f
}

func commandApprovalNativeInput(t *testing.T, background bool) toolgateway.CommandRuntimeInput {
	t.Helper()
	executable, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("native Node fixture is required", err)
	}
	script := `require('fs').appendFileSync('count.txt','1');process.stdout.write('command-approval-result');`
	stdin, closed := runner.CommandRuntimeStdinClosed, true
	action := toolgateway.CommandRuntimeActionRun
	if background {
		action = toolgateway.CommandRuntimeActionStart
		stdin = runner.CommandRuntimeStdinPipe
		closed = false
		script = `process.stdin.on('data',b=>{require('fs').appendFileSync('stdin.txt',b);process.stdout.write('stdin-observed');});process.stdin.on('end',()=>process.exit(0));`
	}
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: action,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess, Executable: executable, Arguments: []string{"-e", script},
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: stdin, CloseInitialStdin: closed, TimeoutMilliseconds: 10000,
			Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096}, Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "write a local fixture marker exactly once"}}}
	if !background {
		n := 4096
		input.MaxBytes = &n
		input.FailurePolicy = toolgateway.CommandRuntimeFailFast
	}
	return input
}

func (f *commandApprovalFixture) record(t *testing.T, input toolgateway.CommandRuntimeInput, round int) {
	t.Helper()
	raw, _ := json.Marshal(input)
	f.recordTool(t, toolgateway.CommandRuntimeTool, raw, round)
}
func (f *commandApprovalFixture) recordTool(t *testing.T, name toolgateway.ToolName, raw json.RawMessage, round int) {
	t.Helper()
	ctx := t.Context()
	permission, err := f.st.GetRunExecutionPermission(ctx, f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	advertised, err := f.supervisor.supervisorCommandRuntimeTools(ctx, f.turn.Run.ID, permission.Mode)
	if err != nil || len(advertised.Authority) == 0 {
		t.Fatalf("native command advertisement missing: %v", err)
	}
	calls, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "provider-command", Name: string(name), Arguments: raw}}, f.turn.Run.ID, f.turn.Checkpoint.NextTurn, round,
		f.turn.Mode.Surface, f.turn.Mode.Phase, permission.Mode, false, false, supervisorToolOptions{CommandRuntime: advertised})
	if err != nil {
		t.Fatal(err)
	}
	calls, err = f.supervisor.bindCommandRuntimeCalls(ctx, calls)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := f.st.GetSupervisorCheckpoint(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	f.turn.Checkpoint = checkpoint
	attempt := llm.ModelAttempt{Number: round, ToolRound: round - 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-command-fixture", Model: "fixture"}
	if _, err = f.st.RecordSupervisorModelStarted(ctx, f.turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	f.turn.Checkpoint, err = f.st.RecordSupervisorModelCompleted(ctx, f.turn.Checkpoint, attempt, llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, ToolCalls: calls})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(ctx, f.turn.Checkpoint)
	if err != nil || len(rounds) != round {
		t.Fatalf("recorded round missing: %v", err)
	}
	f.call = rounds[round-1].Calls[0]
}

func (f *commandApprovalFixture) resume(t *testing.T) (bool, error) {
	t.Helper()
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil {
		return false, err
	}
	_, waiting, err := f.supervisor.resumeSupervisorTools(t.Context(), f.turn, rounds)
	return waiting, err
}
func (f *commandApprovalFixture) decide(t *testing.T, action ApprovalControlAction) {
	t.Helper()
	record, err := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewApprovalControlService(f.st, toolgateway.New(nil, f.checker), f.checker).Decide(t.Context(), DecideApprovalControlRequest{Version: ApprovalControlProtocolVersion,
		RunID: f.call.RunID, ApprovalID: record.ID, Action: action, OperationKey: "command-review-" + f.call.CallID, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
}
func (f *commandApprovalFixture) scope(t *testing.T) (toolgateway.CommandRuntimeContext, toolgateway.CommandRuntimeInput) {
	t.Helper()
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(f.call.AuthorityJSON))
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := toolgateway.CommandToolInput(toolgateway.ToolName(f.call.ToolName), json.RawMessage(f.call.PayloadJSON), a)
	if err != nil {
		t.Fatal(err)
	}
	return toolgateway.CommandRuntimeContext{ProjectTest: f.call.ToolName == string(toolgateway.RunTestsTool), InvocationID: "direct-command-operation", OperationKey: runmutation.SupervisorToolExecutionKey(f.call.RunID, f.call.Turn, f.call.ToolName, f.call.PayloadJSON, f.call.CallID),
		RunID: f.call.RunID, MissionID: f.turn.Mission.ID, RootAgentID: f.call.AgentID, AgentID: f.call.AgentID, AgentAttemptID: f.call.AgentAttemptID, SessionID: f.turn.Run.SessionID, WorkspaceID: f.turn.Mission.WorkspaceID,
		Surface: f.turn.Mode.Surface, Phase: f.turn.Mode.Phase, Role: domain.AgentRoleRoot, Profile: f.turn.Mode.Profile, ModeRevision: f.turn.Mode.Revision,
		PermissionMode: a.PermissionMode, PermissionRevision: a.PermissionRevision, PermissionSnapshotID: a.PermissionSnapshotID, PermissionGeneration: a.PermissionGeneration,
		PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence, SupervisorToolCallID: f.call.CallID,
		CapabilityGeneration: a.Adapter.Generation, LeaseID: f.turn.Checkpoint.LeaseID, LeaseGeneration: f.turn.Checkpoint.LeaseGeneration, RequestedBy: "run_supervisor", Adapter: a.Adapter,
		PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "high", Reason: "fixture passes upstream decision"}}, input
}
func (f *commandApprovalFixture) assertNoMarker(t *testing.T, name string) {
	t.Helper()
	if value, err := os.ReadFile(filepath.Join(f.root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected process effect: %s %q %v", name, value, err)
	}
}

func TestCommandOperationApprovalRealNativeThreeModes(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalFixture(t, mode, false)
			f.record(t, commandApprovalNativeInput(t, false), 1)
			waiting, err := f.resume(t)
			if mode != domain.RunExecutionPermissionFull {
				if err != nil || !waiting {
					t.Fatalf("missing review: waiting=%t err=%v", waiting, err)
				}
				f.assertNoMarker(t, "count.txt")
				jobs, listErr := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 20})
				if listErr != nil || len(jobs) != 0 {
					t.Fatal("review created a job", jobs, listErr)
				}
				f.decide(t, ApprovalControlApproveOnce)
				waiting, err = f.resume(t)
			}
			if err != nil || waiting {
				t.Fatalf("dispatch failed: %v waiting=%t", err, waiting)
			}
			call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || call.Status != domain.SupervisorToolCompleted || !strings.Contains(call.ResultJSON, "command-approval-result") {
				t.Fatalf("missing process receipt: %+v %v", call, err)
			}
			if waiting, err = f.resume(t); err != nil || waiting {
				t.Fatal(err)
			}
			count, err := os.ReadFile(filepath.Join(f.root, "count.txt"))
			if err != nil || string(count) != "1" {
				t.Fatalf("duplicate/missing execution: %q %v", count, err)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 20})
			if err != nil || len(jobs) != 1 || jobs[0].PermissionMode != mode || jobs[0].RunAuthorizationFence == 0 || jobs[0].PermissionRuntimeEpoch != f.caps.RuntimeAuthority.RuntimeEpoch() {
				t.Fatalf("lost job authority %+v %v", jobs, err)
			}
		})
	}
}

func TestCommandOperationApprovalNativeSinkRejectsBypass(t *testing.T) {
	for _, scenario := range []string{"pending_full", "denied_full", "payload_changed", "cancelled", "revoked", "new_epoch", "no_execution_start", "stale_revision", "stale_backend", "revoke_after_job_prepare", "lease_after_job_prepare", "policy_deny_after_review"} {
		t.Run(scenario, func(t *testing.T) {
			mode := domain.RunExecutionPermissionAsk
			if strings.HasSuffix(scenario, "_full") {
				mode = domain.RunExecutionPermissionFull
			}
			f := newCommandApprovalFixture(t, mode, true)
			f.record(t, commandApprovalNativeInput(t, false), 1)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("missing review", err)
			}
			if scenario == "denied_full" {
				f.decide(t, ApprovalControlDeny)
			} else if scenario != "pending_full" {
				f.decide(t, ApprovalControlApproveOnce)
			}
			if scenario != "no_execution_start" {
				if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
					t.Fatal(err)
				}
			}
			scope, input := f.scope(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "stale_revision":
				scope.PermissionRevision++
			case "stale_backend":
				scope.CapabilityGeneration = strings.Repeat("0", 64)
			case "payload_changed":
				input.Commands[0].Arguments[1] = `require('fs').appendFileSync('count.txt','2')`
			case "cancelled":
				cancel()
			case "revoked":
				f.caps.RuntimeAuthority.RevokeRun(f.call.RunID)
			case "new_epoch":
				f.service.capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
			case "revoke_after_job_prepare":
				f.preparedStore.afterPrepare = func() { f.caps.RuntimeAuthority.RevokeRun(f.call.RunID) }
			case "lease_after_job_prepare":
				f.preparedStore.afterPrepare = func() {
					lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.call.RunID)
					if err != nil {
						t.Error(err)
						return
					}
					if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
						t.Error(err)
					}
				}
			case "policy_deny_after_review":
				f.checker.deny = true
			}
			if _, err := f.service.ExecuteCommandRuntime(ctx, scope, input); err == nil {
				t.Fatal("native sink accepted invalid authority")
			}
			f.assertNoMarker(t, "count.txt")
		})
	}
}

func TestCommandOperationApprovalExpiredAndUnknownNeverResends(t *testing.T) {
	for _, scenario := range []string{"denied", "revoked", "started_unknown", "executable_changed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCommandApprovalFixture(t, domain.RunExecutionPermissionAsk, false)
			input := commandApprovalNativeInput(t, false)
			if scenario == "executable_changed" {
				original, err := exec.LookPath("whoami")
				if err != nil {
					t.Fatal("native identity fixture is required", err)
				}
				image, err := os.ReadFile(original)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "pinned-fixture.exe")
				if err := os.WriteFile(path, image, 0700); err != nil {
					t.Fatal(err)
				}
				input.Commands[0].Executable = path
			}
			f.record(t, input, 1)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("missing review", err)
			}
			if scenario == "denied" {
				f.decide(t, ApprovalControlDeny)
			} else {
				f.decide(t, ApprovalControlApproveOnce)
			}
			want := "command_authority_expired"
			switch scenario {
			case "denied":
				want = "policy_denied"
			case "revoked":
				f.caps.RuntimeAuthority.RevokeRun(f.call.RunID)
			case "started_unknown":
				want = "outcome_unknown"
				if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
					t.Fatal(err)
				}
			case "executable_changed":
				// Keep the native header valid: the pinned image hash must reject
				// the changed bytes before an OS launch is possible.
				file, err := os.OpenFile(input.Commands[0].Executable, os.O_APPEND|os.O_WRONLY, 0700)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString("changed fixture executable bytes")
				if err = errors.Join(err, file.Close()); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if waiting, err := f.resume(t); err != nil || waiting {
					t.Fatalf("recovery failed %t %v", waiting, err)
				}
			}
			call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || call.ErrorCode != want {
				t.Fatalf("lost negative receipt %+v %v", call, err)
			}
			f.assertNoMarker(t, "count.txt")
		})
	}
}

func TestCommandOperationApprovalStdinHasIndependentConsent(t *testing.T) {
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionAsk, false)
	input := commandApprovalNativeInput(t, true)
	input.Commands[0].TimeoutMilliseconds = 30000
	f.record(t, input, 1)
	if waiting, err := f.resume(t); err != nil || !waiting {
		t.Fatal("missing launch review", err)
	}
	f.decide(t, ApprovalControlApproveOnce)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatal("launch failed", err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 20})
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		job, readErr := f.st.GetCommandRuntimeJob(ctx, jobs[0].ID)
		t.Logf("stdin Job diagnostic: state=%s created=%s started=%v completed=%v owner_renewed=%s owner_expires=%s stderr=%q error=%v",
			job.State, job.CreatedAt, job.StartedAt, job.CompletedAt, job.OwnerRenewedAt, job.OwnerExpiresAt,
			outputsafe.Sanitize([]byte(job.Stderr)), readErr)
		call, started, readErr := f.st.GetSupervisorApprovalCall(ctx, f.call.RunID, f.call.CallID)
		t.Logf("stdin call diagnostic: status=%s started=%t code=%s result=%q error=%v",
			call.Status, started, call.ErrorCode, outputsafe.Sanitize([]byte(call.ResultJSON)), readErr)
	})
	data, closeAfter := "exact reviewed stdin", true
	f.record(t, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWriteStdin, JobID: jobs[0].ID, Stdin: &data, CloseStdin: &closeAfter}, 2)
	if waiting, err := f.resume(t); err != nil || !waiting {
		t.Fatal("launch approval leaked to stdin", err)
	}
	f.assertNoMarker(t, "stdin.txt")
	// Observe a real owner renewal while the independent stdin review is
	// pending, rather than letting fast hosts finish before the first heartbeat.
	waitingAt := time.Now().UTC()
	for {
		current, err := f.st.GetCommandRuntimeJob(t.Context(), jobs[0].ID)
		if err != nil || current.State.Terminal() {
			t.Fatalf("stdin review lost its owned Job: state=%s error=%v", current.State, err)
		}
		if current.State == runner.CommandRuntimeJobRunning && current.OwnerRenewedAt.After(waitingAt) {
			break
		}
		if _, _, err := f.manager.Wait(t.Context(), jobs[0].ID, 100*time.Millisecond, ^uint64(0), runner.MinCommandRuntimeOutputRead); err != nil {
			t.Fatal(err)
		}
	}
	f.assertNoMarker(t, "stdin.txt")
	f.decide(t, ApprovalControlApproveOnce)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatal("stdin failed", err)
	}
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatal("stdin replay failed", err)
	}
	job, err := f.service.waitForTerminal(t.Context(), jobs[0].ID)
	if err != nil || job.State != runner.CommandRuntimeJobCompleted {
		t.Fatal(job, err)
	}
	value, err := os.ReadFile(filepath.Join(f.root, "stdin.txt"))
	if err != nil || string(value) != data {
		t.Fatalf("stdin changed or duplicated: %q %v", value, err)
	}
}

func TestCommandOperationApprovalColdDatabaseNeverReactivatesOrResends(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		for _, state := range []string{"pending", "approved", "started_unknown"} {
			t.Run(string(mode)+"/"+state, func(t *testing.T) {
				f := newCommandApprovalFixture(t, mode, true)
				f.record(t, commandApprovalNativeInput(t, false), 1)
				if waiting, err := f.resume(t); err != nil || !waiting {
					t.Fatal("missing persisted review", err)
				}
				if state != "pending" {
					f.decide(t, ApprovalControlApproveOnce)
				}
				if state == "started_unknown" {
					if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.manager.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := f.st.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				f.st, err = store.Open(f.path)
				if err != nil {
					t.Fatal(err)
				}
				f.preparedStore.SQLiteStore = f.st
				f.caps.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
				f.manager, err = runner.NewPlatformCommandRuntimeManager(f.preparedStore, idgen.New("command-cold-owner"))
				if err != nil {
					t.Fatal(err)
				}
				f.service, err = NewCommandRuntimeService(f.st, f.manager, f.caps)
				if err != nil {
					t.Fatal(err)
				}
				f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.service)
				for i := 0; i < 2; i++ {
					if waiting, err := f.resume(t); err != nil || waiting {
						t.Fatal("cold recovery did not settle", err)
					}
				}
				call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
				want := "command_authority_expired"
				if state == "started_unknown" {
					want = "outcome_unknown"
				}
				if err != nil || call.ErrorCode != want {
					t.Fatalf("cold receipt %+v %v", call, err)
				}
				if jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 10}); err != nil || len(jobs) != 0 {
					t.Fatal("cold recovery created a process", jobs, err)
				}
				f.assertNoMarker(t, "count.txt")
			})
		}
	}
}

func TestCommandOperationApprovalWithoutRuntimeAuthorityStillRequiresReview(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalFixture(t, mode, false)
			f.caps.RuntimeAuthority = nil
			f.service.capabilities = f.caps
			f.supervisor.WithExecutionPermissionCapabilities(f.caps)
			f.record(t, commandApprovalNativeInput(t, false), 1)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("missing exact review", err)
			}
			f.assertNoMarker(t, "count.txt")
			f.decide(t, ApprovalControlApproveOnce)
			if waiting, err := f.resume(t); err != nil || waiting {
				t.Fatal("reviewed native action failed", err)
			}
			value, err := os.ReadFile(filepath.Join(f.root, "count.txt"))
			if err != nil || string(value) != "1" {
				t.Fatal("missing native action", string(value), err)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 10})
			if err != nil || len(jobs) != 1 || jobs[0].RunAuthorizationFence != 0 || jobs[0].PermissionRuntimeEpoch != "" {
				t.Fatal("invented runtime revocation proof", jobs, err)
			}
		})
	}
}

func TestCommandOperationApprovalRevocationReapsOriginalJob(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalFixture(t, mode, false)
			f.record(t, commandApprovalNativeInput(t, true), 1)
			waiting, err := f.resume(t)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				f.decide(t, ApprovalControlApproveOnce)
				waiting, err = f.resume(t)
			}
			if err != nil || waiting {
				t.Fatal("native job did not start", err)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 10})
			if err != nil || len(jobs) != 1 || jobs[0].State != runner.CommandRuntimeJobRunning {
				t.Fatal(jobs, err)
			}
			f.caps.RuntimeAuthority.RevokeRun(f.call.RunID)
			if count, err := f.service.Reconcile(t.Context()); err != nil || count != 1 {
				t.Fatal("revoked job was not reconciled", count, err)
			}
			job, err := f.service.waitForTerminal(t.Context(), jobs[0].ID)
			if err != nil || !job.State.Terminal() || !job.TreeReaped {
				t.Fatal("process tree was not reaped", job, err)
			}
			permission, err := f.st.GetRunExecutionPermission(t.Context(), f.call.RunID)
			if err != nil || permission.Mode != mode || permission.ID != jobs[0].PermissionSnapshotID {
				t.Fatal("test revoked a snapshot instead of the runtime fence", permission, err)
			}
			f.assertNoMarker(t, "stdin.txt")
		})
	}
}

func TestCommandOperationApprovalLifecycleCannotEraseExistingReview(t *testing.T) {
	for _, state := range []string{"pending", "denied", "host_denied"} {
		t.Run(state, func(t *testing.T) {
			f := newCommandApprovalFixture(t, domain.RunExecutionPermissionFull, true)
			f.record(t, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionList}, 1)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("missing lifecycle review", err)
			}
			if state == "denied" {
				f.decide(t, ApprovalControlDeny)
			}
			if state == "host_denied" {
				f.decide(t, ApprovalControlApproveOnce)
				f.checker.deny = true
			}
			f.checker.require = false
			if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
				t.Fatal(err)
			}
			scope, input := f.scope(t)
			if _, err := f.service.ExecuteCommandRuntime(t.Context(), scope, input); err == nil {
				t.Fatal("native lifecycle sink ignored existing review or current denial")
			}
		})
	}
}

func TestCommandOperationApprovalCompletedStartOnlyContinuesItsOwnedJob(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalFixture(t, mode, false)
			input := commandApprovalNativeInput(t, true)
			input.Commands[0].TimeoutMilliseconds = 30000
			f.record(t, input, 1)
			waiting, err := f.resume(t)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				f.decide(t, ApprovalControlApproveOnce)
				if waiting, err := f.resume(t); err != nil || waiting {
					t.Fatalf("reviewed background start failed: waiting=%t err=%v", waiting, err)
				}
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 20})
			if err != nil || len(jobs) != 1 || jobs[0].State != runner.CommandRuntimeJobRunning {
				t.Fatalf("background Job was not admitted: %+v err=%v", jobs, err)
			}
			if _, err := readCommandApprovalSource(t.Context(), f.st, f.call.RunID, f.call.CallID); err == nil {
				t.Fatal("completed start became a pending review/recovery source")
			}
			scope, pinnedInput := f.scope(t)
			scope.InvocationID = jobs[0].InvocationID
			bindings, err := f.service.loadAuthorizedBindings(t.Context(), scope, false)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := f.service.normalizeCommandRuntimeSpec(pinnedInput.Commands[0], bindings.rootPath)
			if err != nil {
				t.Fatal(err)
			}
			check := func(service *CommandRuntimeService, candidate toolgateway.CommandRuntimeContext) error {
				start, err := service.authorizedCommandStart(candidate, bindings, candidate.OperationKey, resolved)
				if err != nil {
					return err
				}
				return start.DispatchCheck(t.Context(), resolved)
			}
			if err := check(f.service, scope); err != nil {
				t.Fatalf("exact still-owned Job lost its admitted source: %v", err)
			}
			wrong := scope
			wrong.InvocationID = "different-command-invocation"
			if err := check(f.service, wrong); err == nil {
				t.Fatal("completed start authorized a different invocation")
			}
			cold, err := runner.NewPlatformCommandRuntimeManager(f.st, idgen.New("cold-owned-start"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cold.Shutdown(context.Background()) })
			coldService, err := NewCommandRuntimeService(f.st, cold, f.caps)
			if err != nil {
				t.Fatal(err)
			}
			coldService.SetCommandRuntimePolicy(f.checker)
			if err := check(coldService, scope); err == nil {
				t.Fatal("durable start receipt reactivated authority in a different manager")
			}
			if _, err := f.manager.Stop(t.Context(), jobs[0].ID, true, 0); err != nil {
				t.Fatal(err)
			}
			if err := check(f.service, scope); err == nil {
				t.Fatal("completed start authorized a stopped Job")
			}
			if current, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 20}); err != nil || len(current) != 1 {
				t.Fatalf("rechecks created another Job: %+v err=%v", current, err)
			}
		})
	}
}

// observeCommandRuntime keeps the native result while the real Supervisor still
// performs preparation, consent, execution-start, gateway dispatch and receipts.
type observeCommandRuntime struct {
	*CommandRuntimeService
	result toolgateway.CommandRuntimeExecutionResult
}

func (o *observeCommandRuntime) ExecuteCommandRuntime(ctx context.Context, scope toolgateway.CommandRuntimeContext, input toolgateway.CommandRuntimeInput) (toolgateway.CommandRuntimeExecutionResult, error) {
	result, err := o.CommandRuntimeService.ExecuteCommandRuntime(ctx, scope, input)
	o.result = result
	return result, err
}

func commandFixtureForScope(t *testing.T, st *store.SQLiteStore, service *CommandRuntimeService, scope toolgateway.CommandRuntimeContext) *commandApprovalFixture {
	t.Helper()
	ctx := t.Context()
	f := &commandApprovalFixture{st: st, service: service, caps: service.capabilities, checker: &commandApprovalChecker{Checker: policy.NewDefaultChecker()}}
	var err error
	f.turn.Run, err = st.GetRun(ctx, scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	f.turn.Mission, err = st.GetMission(ctx, scope.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	f.turn.Mode, err = st.GetRunMode(ctx, scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	f.turn.Agent, found, err = st.GetRootAgent(ctx, scope.RunID)
	if err != nil || !found {
		t.Fatalf("root: %t %v", found, err)
	}
	f.turn.Checkpoint, found, err = st.GetSupervisorCheckpoint(ctx, scope.RunID)
	if err != nil || !found {
		t.Fatalf("checkpoint: %t %v", found, err)
	}
	f.supervisor = NewAgentRunner(st, nil, f.checker).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(service)
	return f
}

func (f *commandApprovalFixture) execute(t *testing.T, ctx context.Context, input toolgateway.CommandRuntimeInput, round int) (toolgateway.CommandRuntimeExecutionResult, error) {
	t.Helper()
	f.record(t, input, round)
	observer := &observeCommandRuntime{CommandRuntimeService: f.service}
	f.supervisor.WithCommandRuntime(observer)
	rounds, err := f.st.ListSupervisorToolRounds(ctx, f.turn.Checkpoint)
	if err != nil {
		return observer.result, err
	}
	_, waiting, err := f.supervisor.resumeSupervisorTools(ctx, f.turn, rounds)
	if err == nil && waiting {
		err = errors.New("fixture unexpectedly requires consent")
	}
	if err == nil {
		call, found, readErr := f.st.GetSupervisorApprovalCall(ctx, f.call.RunID, f.call.CallID)
		if readErr != nil {
			err = readErr
		} else if !found || call.Status != domain.SupervisorToolCompleted {
			err = fmt.Errorf("command receipt: found=%t status=%s code=%s", found, call.Status, call.ErrorCode)
		}
	}
	return observer.result, err
}

func (f *commandApprovalFixture) startScope(t *testing.T, input toolgateway.CommandRuntimeInput, round int) toolgateway.CommandRuntimeContext {
	t.Helper()
	f.record(t, input, round)
	fresh, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID)
	if err != nil || !fresh {
		t.Fatalf("execution start: fresh=%t err=%v", fresh, err)
	}
	scope, _ := f.scope(t)
	return scope
}

func (f *commandApprovalFixture) completeTurn(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	checkpoint, found, err := f.st.GetSupervisorCheckpoint(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("checkpoint: %t %v", found, err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(ctx, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: len(rounds) + 1, ToolRound: len(rounds), TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-command-fixture", Model: "fixture"}
	if _, err = f.st.RecordSupervisorModelStarted(ctx, checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "command fixture turn complete"}
	raw, _ := json.Marshal(action)
	response := llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, Text: string(raw)}
	checkpoint, err = f.st.RecordSupervisorModelCompleted(ctx, checkpoint, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = f.st.CompleteSupervisorTurn(ctx, checkpoint, response, action, policy.Decision{Allowed: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.turn.Checkpoint = checkpoint
}

func (f *commandApprovalFixture) nextTurn(t *testing.T, lease domain.RunExecutionLease) {
	t.Helper()
	f.completeTurn(t)
	var err error
	f.turn, err = f.st.BeginSupervisorTurn(t.Context(), lease, "continue the fixed command fixture")
	if err != nil {
		t.Fatal(err)
	}
}

// Exercise the actual owner heartbeat after releasing the model turn lease.
// The existing Job timeout bounds this wait; no secondary timing assumption
// substitutes for observing a successful durable owner renewal.
func (f *commandApprovalFixture) nextTurnWithBackgroundJob(t *testing.T, manager *runner.CommandRuntimeManager, jobID string) {
	t.Helper()
	ctx := t.Context()
	oldAttempt := f.turn.Checkpoint.AttemptID
	oldScope, oldInput := f.scope(t)
	job, err := f.st.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	oldScope.InvocationID = job.InvocationID
	lease, found, err := f.st.GetRunExecutionLease(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("original lease: %t %v", found, err)
	}
	f.completeTurn(t)
	if _, _, err := f.st.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	releasedAt := time.Now().UTC()
	if stopped, err := f.service.Reconcile(ctx); err != nil || stopped != 0 {
		t.Fatalf("turn completion reaped its background Job: %d %v", stopped, err)
	}
	for {
		current, err := f.st.GetCommandRuntimeJob(ctx, jobID)
		if err != nil || current.State != runner.CommandRuntimeJobRunning {
			t.Fatalf("background Job lost ownership between turns: %+v %v", current, err)
		}
		if current.OwnerRenewedAt.After(releasedAt) {
			if current.OwnerID != job.OwnerID || current.OwnerGeneration != job.OwnerGeneration || current.LeaseID != job.LeaseID || current.LeaseGeneration != job.LeaseGeneration {
				t.Fatal("background continuation changed its original owner or admission lease")
			}
			break
		}
		if _, _, err := manager.Wait(ctx, jobID, 100*time.Millisecond, ^uint64(0), runner.MinCommandRuntimeOutputRead); err != nil {
			t.Fatal(err)
		}
	}
	acquired, err := f.st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: job.RunID, OwnerID: "command-background-next-turn", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = f.st.BeginSupervisorTurn(ctx, acquired.Lease, "continue the same background Job")
	if err != nil || f.turn.Checkpoint.AttemptID == oldAttempt || f.turn.Checkpoint.LeaseGeneration == lease.Generation {
		t.Fatalf("background continuation did not use a new attempt/lease: %+v %v", f.turn.Checkpoint, err)
	}
	if _, err := f.service.ExecuteCommandRuntime(ctx, oldScope, oldInput); err == nil {
		t.Fatal("old turn authority admitted a new command dispatch")
	}
}

func TestCommandOperationApprovalBackgroundJobSurvivesTurnAndLeaseHandoff(t *testing.T) {
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionFull, false)
	input := commandApprovalNativeInput(t, true)
	input.Commands[0].TimeoutMilliseconds = 30000
	result, err := f.execute(t, t.Context(), input, 1)
	if err != nil || len(result.Jobs) != 1 || result.Jobs[0].State != runner.CommandRuntimeJobRunning {
		t.Fatalf("background start: %+v %v", result, err)
	}
	jobID := result.Jobs[0].ID
	// record() can fail before execute() returns. Capture the durable Job before
	// fixture cleanup shuts down its owner, including when a helper calls Fatal.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		job, err := f.st.GetCommandRuntimeJob(ctx, jobID)
		if err != nil {
			t.Logf("background handoff Job diagnostic: %v", err)
			return
		}
		exitCode := "unset"
		if job.ExitCode != nil {
			exitCode = fmt.Sprint(*job.ExitCode)
		}
		t.Logf("background handoff Job: state=%s exit_code=%s owner_renewed_at=%s owner_expires_at=%s observed_at=%s",
			job.State, exitCode, job.OwnerRenewedAt.Format(time.RFC3339Nano),
			job.OwnerExpiresAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	})
	f.nextTurnWithBackgroundJob(t, f.manager, jobID)
	text, closeInput := "cross-turn\n", true
	if _, err := f.execute(t, t.Context(), toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWriteStdin, JobID: jobID, Stdin: &text, CloseStdin: &closeInput}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.waitForTerminal(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	cursor, size, wait := uint64(0), 4096, 1000
	result, err = f.execute(t, t.Context(), toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWait, JobID: jobID, Cursor: &cursor, MaxBytes: &size, WaitMilliseconds: &wait}, 2)
	if err != nil || len(result.Jobs) != 1 || result.Jobs[0].ID != jobID || result.Jobs[0].State != runner.CommandRuntimeJobCompleted || !result.Jobs[0].TreeReaped {
		t.Fatalf("cross-turn completion: %+v %v", result, err)
	}
	if bytes, err := os.ReadFile(filepath.Join(f.root, "stdin.txt")); err != nil || string(bytes) != text {
		t.Fatalf("cross-turn stdin: %q %v", bytes, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 20})
	if err != nil || len(jobs) != 1 || jobs[0].ID != jobID {
		t.Fatalf("cross-turn continuation duplicated the Job: %+v %v", jobs, err)
	}
}

func TestCommandOperationApprovalWorkspaceRootDriftReapsOriginalJob(t *testing.T) {
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionFull, false)
	result, err := f.execute(t, t.Context(), commandApprovalNativeInput(t, true), 1)
	if err != nil || len(result.Jobs) != 1 || result.Jobs[0].State != runner.CommandRuntimeJobRunning {
		t.Fatalf("start: %+v %v", result, err)
	}
	source, err := f.st.GetWorkspaceByID(t.Context(), f.turn.Mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	source.RootPath, err = filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SaveWorkspace(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if count, err := f.service.Reconcile(t.Context()); err != nil || count != 1 {
		t.Fatalf("root drift reconciliation: %d %v", count, err)
	}
	job, err := f.service.waitForTerminal(t.Context(), result.Jobs[0].ID)
	if err != nil || job.State != runner.CommandRuntimeJobKilled || !job.TreeReaped {
		t.Fatalf("root drift did not reap exact Job: %+v %v", job, err)
	}
	f.assertNoMarker(t, "stdin.txt")
}
