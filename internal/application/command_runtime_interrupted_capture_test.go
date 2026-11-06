package application

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
)

// The database and consumers are real. The sandbox transport is the existing
// controlled fixture; this wrapper models the collector's terminal decision.
type commandRuntimeInterruptedCaptureStore struct {
	*store.SQLiteStore
	exit int
}

func (s commandRuntimeInterruptedCaptureStore) UpdateCommandRuntimeJob(ctx context.Context, job runner.CommandRuntimeJob, previous int64) (runner.CommandRuntimeJob, error) {
	if job.State.Terminal() {
		job.State, job.ExitCode = runner.CommandRuntimeJobInterrupted, &s.exit
		job.TimedOut, job.Cancelled, job.Killed = false, false, false
	}
	return s.SQLiteStore.UpdateCommandRuntimeJob(ctx, job, previous)
}

func installInterruptedCaptureRuntime(t *testing.T, f *commandApprovalSandboxFixture, exit int) *batchSBXBackend {
	t.Helper()
	backend := installBatchSBX(t, f)
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := commandRuntimeInterruptedCaptureStore{f.st, exit}
	manager, err := runner.NewSandboxCommandRuntimeManager(state, f.service.sandbox, "interrupted-capture-owner")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSandboxedCommandRuntimeService(state, manager, f.service.sandbox, f.caps, f.drydockFixture.service)
	if err != nil {
		t.Fatal(err)
	}
	f.service, f.manager = service, manager
	f.mux, err = NewCommandRuntimeMultiplexer(f.host, service)
	if err != nil {
		t.Fatal(err)
	}
	f.supervisor.WithCommandRuntime(f.mux)
	return backend
}

func TestCommandRuntimeInterruptedCaptureStopsForegroundBatchAndReplays(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true)
	backend := installInterruptedCaptureRuntime(t, f, 0)
	input := commandApprovalSandboxInput(t)
	input.Commands[0].Executable = "/usr/bin/go"
	input.Commands[0].TimeoutMilliseconds = 5000
	input.Commands = append(input.Commands, input.Commands[0])
	result, err := f.execute(t, t.Context(), input, 1)
	if err != nil || len(result.Jobs) != 1 || result.Jobs[0].State != runner.CommandRuntimeJobInterrupted || result.Jobs[0].ExitCode == nil || *result.Jobs[0].ExitCode != 0 || len(backend.requests) != 1 {
		t.Fatalf("interrupted exit zero did not stop fail_fast: %+v %v", result, err)
	}
	call, found, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
	var envelope supervisorToolResultEnvelope
	if err != nil || !found || json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || !strings.Contains(envelope.Stdout, `"state":"interrupted"`) {
		t.Fatalf("tool projection lost the interruption: %+v %v", call, err)
	}
	if waiting, err := f.resume(t); err != nil || waiting || len(backend.requests) != 1 {
		t.Fatalf("interrupted tool operation was re-executed: waiting=%t requests=%d err=%v", waiting, len(backend.requests), err)
	}
}

func TestGitAdvancedBisectInterruptedCaptureNeverClassifiesRevision(t *testing.T) {
	for _, exit := range []int{0, 1} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true, true)
			backend := installInterruptedCaptureRuntime(t, f, exit)
			fixture, spec := prepareGitBisectRuntime(t, f)
			review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion,
				RunID: fixture.run.ID, OperationKey: "bisect-interrupted-capture", RequestedBy: "test_operator", Scope: fixture.scope(), Spec: spec})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := f.st.DecideApproval(t.Context(), approval.DecisionRequest{ProposalID: review.Operation.ID,
				IdempotencyKey: "approve-interrupted-capture", Action: approval.ActionApprove, ReviewedBy: "test_operator"})
			if err != nil {
				t.Fatal(err)
			}
			request := GitAdvancedExecuteRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
				OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID, RequestedBy: "test_operator", Scope: fixture.scope()}
			result, err := fixture.service.Execute(t.Context(), request)
			if err == nil || len(backend.requests) != 1 || len(result.Receipt.BisectExecutions) != 1 {
				t.Fatalf("interrupted capture was accepted as a recipe result: %+v %v", result, err)
			}
			job, err := f.st.GetCommandRuntimeJob(t.Context(), result.Receipt.BisectExecutions[0].Execution.JobID)
			if err != nil || job.State != runner.CommandRuntimeJobInterrupted || job.ExitCode == nil || *job.ExitCode != exit || !job.TreeReaped {
				t.Fatalf("SQLite interruption receipt changed: %+v %v", job, err)
			}
			if head := strings.TrimSpace(runFixtureGit(t, "-C", f.root, "rev-parse", "HEAD")); head != spec.ExpectedCurrent {
				t.Fatal("interrupted revision was classified")
			}
			if replayed, err := fixture.service.Execute(t.Context(), request); err != nil || !replayed.Replayed || len(backend.requests) != 1 {
				t.Fatalf("interrupted recipe was re-executed: %+v %v", replayed, err)
			}
		})
	}
}
