package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type delayedApplyFeedback struct {
	delegate toolgateway.AgentCodeExecutor
	after    func(context.Context)
}

func (e delayedApplyFeedback) ExecuteAgentCode(ctx context.Context, scope toolgateway.AgentCodeExecutionScope,
	name toolgateway.ToolName, payload json.RawMessage,
) (toolgateway.AgentCodeExecutionResult, error) {
	result, err := e.delegate.ExecuteAgentCode(ctx, scope, name, payload)
	if err == nil {
		e.after(ctx)
	}
	return result, err
}

func TestSupervisorCommittedWorkspaceApplySurvivesToolDeadline(t *testing.T) {
	f := newWorkspaceApplyReceiptFixture(t, toolgateway.WorkspaceApplyTool)
	f.runner.tools.WithAgentCodeExecutor(delayedApplyFeedback{delegate: NewAgentCodeToolExecutor(f.state, f.checker),
		after: func(ctx context.Context) { <-ctx.Done() }})
	// Exercise the production deadline branch without waiting its 30-second
	// default. Real SQLite and filesystem writes finish before delayed feedback.
	result, err := f.runner.invokeSupervisorToolWithTimeout(t.Context(), f.turn, f.durable, 2*time.Second)
	if err != nil || result.Status != domain.SupervisorToolCompleted {
		t.Fatalf("committed apply lost to feedback deadline: result=%+v err=%v", result, err)
	}
	var envelope supervisorToolResultEnvelope
	if err := json.Unmarshal([]byte(result.ResultJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Metadata["status"] != "applied" || envelope.Metadata["artifact_count"] != "1" {
		t.Fatalf("successful write or its artifact missing: %+v", envelope)
	}
	archives, err := f.state.ListRunArtifacts(t.Context(), artifact.ListFilter{RunID: f.turn.Run.ID})
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives=%+v err=%v", archives, err)
	}
	blob, err := f.state.GetRunArtifact(t.Context(), archives[0].ID)
	if err != nil || blob.Content != envelope.Stdout || blob.SourceID == f.durable.CallID {
		t.Fatalf("original gateway invocation/output not archived: blob=%+v err=%v", blob.Descriptor, err)
	}
	stored, _, err := f.state.RecordSupervisorToolResult(t.Context(), f.turn.Checkpoint, result)
	if err != nil {
		t.Fatal(err)
	}
	permission, _ := f.state.GetRunExecutionPermission(t.Context(), f.turn.Run.ID)
	machine := &standardCodeSupervisorTurn{store: f.state, turn: f.turn, permission: permission,
		fileWorkspaceID: f.turn.Mission.WorkspaceID,
		snapshot: domain.StandardCodeSupervisorSnapshot{RunID: f.turn.Run.ID, MissionID: f.turn.Mission.ID,
			WorkspaceID: f.turn.Mission.WorkspaceID, RootAgentID: f.turn.Agent.ID,
			AttemptID: f.turn.Checkpoint.AttemptID, CapabilityGeneration: f.authority.CapabilityGeneration,
			ModeRevision: f.turn.Mode.Revision, PermissionRevision: permission.Revision, State: domain.StandardCodeSupervisorEdit}}
	if reason, err := machine.observeWorkspaceMutation(t.Context(), stored, standardCodeCallDescriptor{EditID: f.edit.ID}, envelope); err != nil || reason != "workspace_mutation_checkpoint_verified" || machine.snapshot.MutationEpoch != 1 {
		t.Fatalf("completion guard rejected the exact committed receipt: reason=%s snapshot=%+v err=%v", reason, machine.snapshot, err)
	}
	f.assertApplied(t)
}

func TestSupervisorWorkspaceApplyParentCancelRemainsCancelled(t *testing.T) {
	f := newWorkspaceApplyReceiptFixture(t, toolgateway.WorkspaceApplyTool)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.runner.tools.WithAgentCodeExecutor(delayedApplyFeedback{delegate: NewAgentCodeToolExecutor(f.state, f.checker),
		after: func(context.Context) { cancel() }})
	result, err := f.runner.invokeSupervisorToolWithTimeout(ctx, f.turn, f.durable, time.Minute)
	if apperror.CodeOf(err) != apperror.CodeCancelled || result.Status == domain.SupervisorToolCompleted {
		t.Fatalf("parent cancellation became successful: result=%+v err=%v", result, err)
	}
	f.assertApplied(t)
}

func TestSupervisorOtherAgentCodeToolDeadlineRemainsFailed(t *testing.T) {
	f := newWorkspaceApplyReceiptFixture(t, toolgateway.WorkspaceReadTool)
	f.runner.tools.WithAgentCodeExecutor(delayedApplyFeedback{delegate: NewAgentCodeToolExecutor(f.state, f.checker),
		after: func(ctx context.Context) { <-ctx.Done() }})
	result, err := f.runner.invokeSupervisorToolWithTimeout(t.Context(), f.turn, f.durable, 50*time.Millisecond)
	if err != nil || result.Status != domain.SupervisorToolFailed || result.ErrorCode != string(apperror.CodeDeadlineExceeded) {
		t.Fatalf("ordinary tool timeout changed: result=%+v err=%v", result, err)
	}
}

func TestSupervisorWorkspaceApplyParentCancelDuringReceiptRecovery(t *testing.T) {
	for _, phase := range []string{"lookup", "archive"} {
		t.Run(phase, func(t *testing.T) {
			f := newWorkspaceApplyReceiptFixture(t, toolgateway.WorkspaceApplyTool)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			altered := &alteredWorkspaceApplyReceiptStore{SQLiteStore: f.state}
			if phase == "lookup" {
				altered.operation = func(*fileedit.ApplyOperation, **fileedit.ApplyResult, *bool) { cancel() }
			} else {
				altered.archive = func(ctx context.Context) {
					if ctx.Err() == nil {
						cancel()
					}
				}
			}
			f.runner = NewAgentRunner(altered, nil, f.checker)
			f.runner.tools.WithAgentCodeExecutor(delayedApplyFeedback{delegate: NewAgentCodeToolExecutor(f.state, f.checker),
				after: func(ctx context.Context) { <-ctx.Done() }})
			result, err := f.runner.invokeSupervisorToolWithTimeout(ctx, f.turn, f.durable, 2*time.Second)
			if apperror.CodeOf(err) != apperror.CodeCancelled || result.Status == domain.SupervisorToolCompleted {
				t.Fatalf("parent cancellation during %s was lost: result=%+v err=%v", phase, result, err)
			}
			f.assertApplied(t)
		})
	}
}

type alteredWorkspaceApplyReceiptStore struct {
	*store.SQLiteStore
	operation  func(*fileedit.ApplyOperation, **fileedit.ApplyResult, *bool)
	boundary   func(*workspacecheckpoint.Transaction)
	checkpoint func(*workspacecheckpoint.Checkpoint)
	archiveErr error
	archive    func(context.Context)
}

func (s *alteredWorkspaceApplyReceiptStore) GetFileEditApplyOperation(ctx context.Context, digest string) (fileedit.ApplyOperation, *fileedit.ApplyResult, bool, error) {
	op, result, found, err := s.SQLiteStore.GetFileEditApplyOperation(ctx, digest)
	if err == nil && s.operation != nil {
		s.operation(&op, &result, &found)
	}
	return op, result, found, err
}

func (s *alteredWorkspaceApplyReceiptStore) GetWorkspaceCheckpointTransactionByOperation(ctx context.Context, digest string) (workspacecheckpoint.Transaction, bool, error) {
	boundary, found, err := s.SQLiteStore.GetWorkspaceCheckpointTransactionByOperation(ctx, digest)
	if err == nil && s.boundary != nil {
		s.boundary(&boundary)
	}
	return boundary, found, err
}

func (s *alteredWorkspaceApplyReceiptStore) GetWorkspaceCheckpoint(ctx context.Context, id string) (workspacecheckpoint.Checkpoint, error) {
	checkpoint, err := s.SQLiteStore.GetWorkspaceCheckpoint(ctx, id)
	if err == nil && s.checkpoint != nil {
		s.checkpoint(&checkpoint)
	}
	return checkpoint, err
}

func (s *alteredWorkspaceApplyReceiptStore) CaptureToolOutput(ctx context.Context, request artifact.CaptureRequest) ([]artifact.Descriptor, error) {
	if s.archive != nil {
		s.archive(ctx)
	}
	if s.archiveErr != nil {
		return nil, s.archiveErr
	}
	return s.SQLiteStore.CaptureToolOutput(ctx, request)
}

func TestWorkspaceApplyDeadlineReconcilesOwnedDrydockReceipt(t *testing.T) {
	d := newDrydockApplicationFixture(t, "apply receipt owned root")
	checkpoints, err := NewWorkspaceCheckpointService(d.state, domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	d.service.WithCheckpointService(checkpoints)
	owned := mustCreateDrydock(t, d)
	f := prepareWorkspaceApplyReceiptFixture(t, workspaceApplyReceiptFixture{state: d.state, root: owned.Path,
		workspaceID: owned.WorkspaceID, checker: reviewedDrydockFilePolicy{}, drydocks: d.service}, d.run, toolgateway.WorkspaceApplyTool)
	writeDrydockTestFile(t, filepath.Join(d.sourceRoot, "source-canary.txt"), "source remains untouched\n")
	outcome, err := f.runner.tools.Invoke(t.Context(), f.gatewayCall)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Call.WorkspaceID == outcome.Result.Metadata["workspace_id"] {
		t.Fatal("fixture did not separate source and owned workspace identities")
	}
	reconciled, found, err := f.runner.reconcileWorkspaceApplyDeadline(t.Context(), f.turn, f.durable, outcome)
	if err != nil || !found || reconciled.Result.Stdout != outcome.Result.Stdout ||
		reconciled.Call.InvocationID != outcome.Call.InvocationID {
		t.Fatalf("exact owned receipt rejected: found=%t outcome=%+v err=%v", found, reconciled.Result, err)
	}
	if readDrydockTestFile(t, filepath.Join(d.sourceRoot, "source-canary.txt")) != "source remains untouched\n" {
		t.Fatal("receipt reconciliation changed source")
	}
	f.assertApplied(t)
}

func TestWorkspaceApplyDeadlineRequiresExactDurableReceipt(t *testing.T) {
	f := newWorkspaceApplyReceiptFixture(t, toolgateway.WorkspaceApplyTool)
	outcome, err := f.runner.tools.Invoke(t.Context(), f.gatewayCall)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  func(*alteredWorkspaceApplyReceiptStore)
	}{
		{"no operation", func(s *alteredWorkspaceApplyReceiptStore) {
			s.operation = func(_ *fileedit.ApplyOperation, _ **fileedit.ApplyResult, found *bool) { *found = false }
		}},
		{"prepared only", func(s *alteredWorkspaceApplyReceiptStore) {
			s.operation = func(_ *fileedit.ApplyOperation, r **fileedit.ApplyResult, _ *bool) { *r = nil }
		}},
		{"failed apply", func(s *alteredWorkspaceApplyReceiptStore) {
			s.operation = func(_ *fileedit.ApplyOperation, r **fileedit.ApplyResult, _ *bool) {
				(*r).Status = fileedit.ApplyFailed
			}
		}},
		{"different intent", func(s *alteredWorkspaceApplyReceiptStore) {
			s.operation = func(o *fileedit.ApplyOperation, _ **fileedit.ApplyResult, _ *bool) {
				o.RequestFingerprint = strings.Repeat("a", 64)
			}
		}},
		{"different workspace", func(s *alteredWorkspaceApplyReceiptStore) {
			s.operation = func(o *fileedit.ApplyOperation, _ **fileedit.ApplyResult, _ *bool) { o.WorkspaceID = "other-workspace" }
		}},
		{"different proposal bytes", func(s *alteredWorkspaceApplyReceiptStore) {
			s.operation = func(o *fileedit.ApplyOperation, _ **fileedit.ApplyResult, _ *bool) {
				o.ProposedHash = strings.Repeat("b", 64)
			}
		}},
		{"different invocation", func(s *alteredWorkspaceApplyReceiptStore) {
			s.boundary = func(b *workspacecheckpoint.Transaction) { b.RequestFingerprint = strings.Repeat("c", 64) }
		}},
		{"unfinished boundary", func(s *alteredWorkspaceApplyReceiptStore) {
			s.boundary = func(b *workspacecheckpoint.Transaction) { b.Status = workspacecheckpoint.TransactionPrepared }
		}},
		{"different attempt", func(s *alteredWorkspaceApplyReceiptStore) {
			s.checkpoint = func(c *workspacecheckpoint.Checkpoint) { c.AttemptID = "other-attempt" }
		}},
		{"different capability", func(s *alteredWorkspaceApplyReceiptStore) {
			s.checkpoint = func(c *workspacecheckpoint.Checkpoint) { c.CapabilityGeneration = strings.Repeat("d", 64) }
		}},
		{"unsealed after", func(s *alteredWorkspaceApplyReceiptStore) {
			s.checkpoint = func(c *workspacecheckpoint.Checkpoint) {
				if c.Phase == workspacecheckpoint.PhaseAfter {
					c.ParentCheckpointID = "other-checkpoint"
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			altered := &alteredWorkspaceApplyReceiptStore{SQLiteStore: f.state}
			tc.set(altered)
			runner := *f.runner
			runner.store = altered
			if _, reconciled, err := runner.reconcileWorkspaceApplyDeadline(t.Context(), f.turn, f.durable, outcome); err != nil || reconciled {
				t.Fatalf("mismatched receipt reconciled=%t err=%v", reconciled, err)
			}
		})
	}
	// Replaying reconciliation reuses the same immutable output artifact and
	// never reruns the FileEdit or creates another checkpoint transaction.
	beforeFile, err := os.Stat(filepath.Join(f.root, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	beforeUsage, err := f.state.GetToolCallUsage(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, reconciled, err := f.runner.reconcileWorkspaceApplyDeadline(t.Context(), f.turn, f.durable, outcome); err != nil || !reconciled {
			t.Fatalf("exact receipt did not replay: reconciled=%t err=%v", reconciled, err)
		}
	}
	archives, _ := f.state.ListRunArtifacts(t.Context(), artifact.ListFilter{RunID: f.turn.Run.ID})
	if len(archives) != 1 {
		t.Fatalf("artifact replay created duplicates: %+v", archives)
	}
	afterFile, err := os.Stat(filepath.Join(f.root, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	afterUsage, err := f.state.GetToolCallUsage(t.Context(), f.turn.Run.ID)
	if err != nil || afterUsage.Consumed != beforeUsage.Consumed ||
		!os.SameFile(beforeFile, afterFile) || !beforeFile.ModTime().Equal(afterFile.ModTime()) {
		t.Fatalf("receipt reconciliation repeated mutation or charged budget: before=%+v after=%+v err=%v", beforeUsage, afterUsage, err)
	}
	archiveFailure := errors.New("injected artifact archive failure")
	altered := &alteredWorkspaceApplyReceiptStore{SQLiteStore: f.state, archiveErr: archiveFailure}
	runner := *f.runner
	runner.store, runner.tools = altered, toolgateway.New(altered, f.checker)
	if _, reconciled, err := runner.reconcileWorkspaceApplyDeadline(t.Context(), f.turn, f.durable, outcome); reconciled || !errors.Is(err, archiveFailure) {
		t.Fatalf("archive failure hidden: reconciled=%t err=%v", reconciled, err)
	}
	f.assertApplied(t)
}

type postCommitCleanupStore struct {
	*store.SQLiteStore
	completed         bool
	lateApprovalReads int
}

func (s *postCommitCleanupStore) CompleteFileEditApply(ctx context.Context, result fileedit.ApplyResult) (fileedit.ApplyResult, bool, error) {
	value, replayed, err := s.SQLiteStore.CompleteFileEditApply(ctx, result)
	if err == nil {
		s.completed = true
	}
	return value, replayed, err
}

func (s *postCommitCleanupStore) GetApprovalByProposal(ctx context.Context, editID string) (approval.Record, error) {
	if s.completed {
		s.lateApprovalReads++
		return approval.Record{}, errors.New("post-commit binding resolution must not run for staging cleanup")
	}
	return s.SQLiteStore.GetApprovalByProposal(ctx, editID)
}

func TestFileEditApplyStagingCleanupUsesAlreadyVerifiedRoot(t *testing.T) {
	f := newWorkspaceApplyReceiptFixture(t, toolgateway.WorkspaceApplyTool)
	wrapped := &postCommitCleanupStore{SQLiteStore: f.state}
	result, err := NewFileEditApplyService(wrapped, f.checker).Apply(t.Context(), ApplyFileEditRequest{
		Version: fileedit.FileEditApplyProtocolVersion, RunID: f.turn.Run.ID, EditID: f.edit.ID,
		OperationKey: f.gatewayCall.OperationKey, AppliedBy: f.turn.Agent.ID, InvocationID: f.durable.CallID,
		CapabilityGeneration: f.authority.CapabilityGeneration,
		LeaseID:              f.turn.Checkpoint.LeaseID, LeaseGeneration: f.turn.Checkpoint.LeaseGeneration})
	if err != nil || result.Result.Status != fileedit.ApplyCompleted || result.StagingCleanup.Pending || wrapped.lateApprovalReads != 0 {
		t.Fatalf("staging cleanup resolved binding again: result=%+v late_reads=%d err=%v", result, wrapped.lateApprovalReads, err)
	}
	f.assertApplied(t)
}

type workspaceApplyReceiptFixture struct {
	state       *store.SQLiteStore
	runner      *AgentRunner
	checker     policy.Checker
	turn        domain.SupervisorTurn
	durable     domain.SupervisorToolCall
	authority   toolgateway.AgentCodeCallAuthority
	gatewayCall toolgateway.ToolCall
	edit        fileedit.Edit
	root        string
	workspaceID string
	drydocks    *RunWorktreeService
}

func newWorkspaceApplyReceiptFixture(t *testing.T, name toolgateway.ToolName) workspaceApplyReceiptFixture {
	t.Helper()
	f := workspaceApplyReceiptFixture{root: t.TempDir(), workspaceID: "workspace-apply-receipt", checker: reviewedDrydockFilePolicy{}}
	state, err := store.Open(filepath.Join(t.TempDir(), "receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.state = state
	t.Cleanup(func() { _ = state.Close() })
	if err := state.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "workspace-apply-receipt", Name: "receipt", RootPath: f.root}); err != nil {
		t.Fatal(err)
	}
	_, created, err := NewRunService(state).Create(t.Context(), CreateRunRequest{Goal: "apply reviewed bytes", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: "workspace-apply-receipt", Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 10}})
	if err != nil {
		t.Fatal(err)
	}
	return prepareWorkspaceApplyReceiptFixture(t, f, created, name)
}

func prepareWorkspaceApplyReceiptFixture(t *testing.T, f workspaceApplyReceiptFixture, created domain.Run, name toolgateway.ToolName) workspaceApplyReceiptFixture {
	t.Helper()
	state := f.state
	if err := os.WriteFile(filepath.Join(f.root, "target.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := NewRunService(state).Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.edit, err = fileedit.NewManager(state).Propose(t.Context(), fileedit.Proposal{SessionID: run.SessionID,
		WorkspaceID: f.workspaceID, WorkspaceRoot: f.root, Path: "target.txt", ProposedText: "after\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileEditReviewService(state).WithRunWorktree(f.drydocks).Review(t.Context(), ReviewFileEditRequest{Version: FileEditReviewProtocolVersion,
		RunID: run.ID, EditID: f.edit.ID, Action: FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	lease, err := state.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "apply-receipt-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = state.BeginSupervisorTurn(t.Context(), lease.Lease, "apply the reviewed edit")
	if err != nil {
		t.Fatal(err)
	}
	f.runner = NewAgentRunner(state, nil, f.checker).WithRunWorktree(f.drydocks)
	permission, err := state.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := f.runner.supervisorAgentCodeCapabilities(t.Context(), f.turn, permission)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &f.authority); err != nil {
		t.Fatal(err)
	}
	var payload json.RawMessage
	if name == toolgateway.WorkspaceApplyTool {
		payload = mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion, EditID: f.edit.ID,
			ExpectedAction: "replace", ExpectedOriginalSHA256: f.edit.OriginalHash, ExpectedProposedSHA256: f.edit.ProposedHash})
	} else {
		payload = json.RawMessage(`{"version":"agent-code-tools.v1","path":"target.txt","start_line":1,"end_line":10}`)
	}
	payload, err = toolgateway.NormalizeAgentCodePayload(name, payload)
	if err != nil {
		t.Fatal(err)
	}
	key := supervisorToolOperationKey(run.ID, f.turn.Checkpoint.NextTurn, name, payload)
	id, err := runmutation.SupervisorToolCallID(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := state.RecordSupervisorModelStarted(t.Context(), f.turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	f.turn.Checkpoint, err = state.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, attempt,
		llm.ChatResponse{Provider: "test", Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: []llm.ToolCall{{ID: id, Name: string(name), Arguments: payload, Authority: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := state.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
		t.Fatalf("rounds=%+v err=%v", rounds, err)
	}
	f.durable = rounds[0].Calls[0]
	if _, err := state.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.durable.CallID); err != nil {
		t.Fatal(err)
	}
	a := f.authority
	f.gatewayCall = toolgateway.ToolCall{Name: name, Payload: payload, OperationKey: key, RunID: run.ID, MissionID: run.MissionID,
		AgentID: f.turn.Agent.ID, AgentAttemptID: f.durable.AttemptID, SessionID: run.SessionID, WorkspaceID: f.turn.Mission.WorkspaceID,
		LeaseID: lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation, RequestedBy: "run_supervisor", SupervisorTurn: f.durable.Turn, SupervisorToolCallID: f.durable.CallID,
		RootFingerprint: a.RootFingerprint, Surface: a.Surface, Phase: a.Phase, Role: a.Role, Profile: a.Profile, PermissionMode: a.PermissionMode,
		PermissionSnapshotID: a.PermissionSnapshotID, PermissionGeneration: a.PermissionGeneration, PermissionRuntimeEpoch: a.PermissionRuntimeEpoch,
		RunAuthorizationFence: a.RunAuthorizationFence, ModeRevision: a.ModeRevision, PermissionRevision: a.PermissionRevision, CapabilityGeneration: a.CapabilityGeneration}
	return f
}

func (f workspaceApplyReceiptFixture) assertApplied(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "target.txt"))
	if err != nil || string(data) != "after\n" {
		t.Fatalf("file=%q err=%v", data, err)
	}
	transactions, err := f.state.ListWorkspaceCheckpointTransactions(t.Context(), f.turn.Run.ID, 10)
	if err != nil || len(transactions) != 1 || transactions[0].Status != workspacecheckpoint.TransactionCompleted {
		t.Fatalf("mutation retried or lost its checkpoint: transactions=%+v err=%v", transactions, err)
	}
}
