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

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// The provider controls the requests; Node, SQLite, native Batch edits and Git
// all run through their real host adapters.
type workerBatchTestProvider struct {
	*nativeBatchTestProvider
	input toolgateway.CommandRuntimeInput
	calls int
}

func (p *workerBatchTestProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		seen := map[string]bool{}
		for _, spec := range request.Tools {
			if seen[spec.Name] {
				p.t.Fatalf("duplicate Batch worker tool: %s", spec.Name)
			}
			seen[spec.Name] = true
		}
		for _, name := range []string{"command_runtime", "workspace_read", "workspace_change", "workspace_apply", "git_commit", "verify"} {
			if !seen[name] {
				p.t.Fatalf("missing Batch worker tool %s", name)
			}
		}
		if seen["workspace_delete"] || seen["workspace_review"] {
			p.t.Fatal("generic workspace mutation escaped the Batch catalogue")
		}
		payload, _ := json.Marshal(p.input)
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: []llm.ToolCall{{ID: "batch-worker-node", Name: "command_runtime", Arguments: payload}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
	}
	if p.calls == 2 {
		last := request.Messages[len(request.Messages)-1]
		if len(last.ToolResults) != 1 || last.ToolResults[0].IsError || !strings.Contains(last.ToolResults[0].Content, "batch-worker-cwd") {
			p.t.Fatalf("Node did not complete: %+v", last)
		}
		body, err := os.ReadFile(filepath.Join(p.root, "internal", "one", "command.txt"))
		if err != nil || string(body) != "1" {
			p.t.Fatalf("Node did not write exactly once in its Batch checkout: %q %v", body, err)
		}
	}
	return p.nativeBatchTestProvider.Chat(ctx, request)
}

func (p *workerBatchTestProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.ChatChunk, 2)
	out <- llm.ChatChunk{Text: response.Text}
	out <- llm.FinalChatChunk(response)
	close(out)
	return out, nil
}

func batchWorkerTestRuntime(t *testing.T, fixture batchDeliveryApplicationFixture, router *llm.Router) *AgentRunner {
	t.Helper()
	manager, err := runner.NewPlatformCommandRuntimeManager(fixture.store, "batch-worker-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	commands, err := NewCommandRuntimeService(fixture.store, manager, fixture.caps)
	if err != nil {
		t.Fatal(err)
	}
	return NewAgentRunner(fixture.store, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(fixture.caps).WithCommandRuntime(commands)
}

type batchWorkerCloseProbe struct {
	*BatchDeliveryModelWorker
	closed   []domain.AgentAttemptRef
	closeErr error
}

func (p *batchWorkerCloseProbe) CloseBatchWorkerResources(ref domain.AgentAttemptRef) error {
	p.closed = append(p.closed, ref)
	return errors.Join(p.BatchDeliveryModelWorker.CloseBatchWorkerResources(ref), p.closeErr)
}

func TestBatchWorkerRunsNodeInOwnedCheckoutAndResumesExactApproval(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionFull, domain.RunExecutionPermissionAsk} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newBatchDeliveryApplicationFixtureWithWorker(t, false, mode, &domain.WorkerProfile{})
			workbench, prepared := prepareBatchWorkbenchFixture(t, fixture)
			owned := prepared.Snapshot.Workspaces[0]
			input := commandApprovalNativeInput(t, false)
			input.Commands[0].Arguments = []string{"-e", `require('fs').appendFileSync('internal/one/command.txt','1');console.log('batch-worker-cwd:'+process.cwd());`}
			provider := &workerBatchTestProvider{nativeBatchTestProvider: &nativeBatchTestProvider{t: t, root: owned.WorktreeRoot}, input: input}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			runtime := batchWorkerTestRuntime(t, fixture, router)
			worker := NewBatchDeliveryModelWorker(fixture.service, fixture.store, router, policy.NewDefaultChecker()).WithNativeTools().WithWorkerRuntime(runtime)
			closer := &batchWorkerCloseProbe{BatchDeliveryModelWorker: worker}
			workbench.WithWorker(closer)
			request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1, ExpectedGeneration: 1, OperationKey: "batch-worker-execute-001", Confirm: true}
			result, err := workbench.ExecuteWorkbench(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := fixture.store.ListAgentAttempts(t.Context(), owned.AgentID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("worker attempts: %+v %v", attempts, err)
			}
			ref := specialistAttemptRef(attempts[0])
			if mode == domain.RunExecutionPermissionAsk {
				if !result.Children[0].WaitingApproval || result.Children[0].OutcomeUnresolved || result.Children[0].Executing || provider.calls != 1 || len(closer.closed) != 0 {
					t.Fatalf("approval wait: %+v calls=%d", result.Children[0], provider.calls)
				}
				if _, err := os.Stat(filepath.Join(owned.WorktreeRoot, "internal", "one", "command.txt")); !os.IsNotExist(err) {
					t.Fatalf("pending command ran: %v", err)
				}
				rounds, err := fixture.store.ListSpecialistToolRounds(t.Context(), ref)
				if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
					t.Fatalf("pending rounds: %+v %v", rounds, err)
				}
				pending := rounds[0].Calls[0]
				if pending.ApprovalID == "" || pending.StartedAt != nil || pending.ChargeID != "" {
					t.Fatalf("pending command was charged or lost approval: %+v", pending)
				}
				waiting, err := workbench.ExecuteWorkbench(t.Context(), request)
				if err != nil || !waiting.Children[0].WaitingApproval || !waiting.Replayed || provider.calls != 1 {
					t.Fatalf("pending replay: %+v calls=%d err=%v", waiting.Children, provider.calls, err)
				}
				unchanged, _, err := fixture.store.GetAgentAttempt(t.Context(), ref.AttemptID)
				if err != nil || unchanged.Usage != attempts[0].Usage {
					t.Fatalf("waiting consumed model usage: %+v %v", unchanged, err)
				}
				if _, err := fixture.store.DecideApproval(t.Context(), approval.DecisionRequest{ProposalID: pending.ApprovalSourceID, IdempotencyKey: "approve-batch-worker-node", Action: approval.ActionApprove, ReviewedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
				result, err = workbench.ExecuteWorkbench(t.Context(), request)
				if err != nil {
					t.Fatalf("resume: %v", err)
				}
			}
			if result.Children[0].WaitingApproval || result.Children[0].OutcomeUnresolved || len(result.Snapshot.Receipts) != 1 || provider.calls != 8 {
				t.Fatalf("completed worker: %+v calls=%d receipts=%d", result.Children[0], provider.calls, len(result.Snapshot.Receipts))
			}
			if len(closer.closed) != 1 || closer.closed[0] != (domain.AgentAttemptRef{RunID: fixture.run.ID, AgentID: owned.AgentID}) {
				t.Fatalf("submitted worker resources were not closed at the child scope: %+v", closer.closed)
			}
			attempts, err = fixture.store.ListAgentAttempts(t.Context(), owned.AgentID)
			if err != nil || len(attempts) != 1 || attempts[0].ID != ref.AttemptID || attempts[0].Usage.TotalTokens != 40 {
				t.Fatalf("approval created another attempt or lost accounting: %+v %v", attempts, err)
			}
			rounds, err := fixture.store.ListSpecialistToolRounds(t.Context(), ref)
			if err != nil || len(rounds) != 7 {
				t.Fatalf("tool rounds: %+v %v", rounds, err)
			}
			for _, round := range rounds {
				call := round.Calls[0]
				if call.Status != domain.SupervisorToolCompleted || call.StartedAt == nil || call.ChargeID == "" || call.AuthorityJSON == "" {
					t.Fatalf("unbound or unaccounted Batch operation: %+v", call)
				}
			}
			for _, root := range []string{fixture.repository, prepared.Snapshot.Workspaces[1].WorktreeRoot} {
				if _, err := os.Stat(filepath.Join(root, "internal", "one", "command.txt")); !os.IsNotExist(err) {
					t.Fatalf("command escaped its checkout into %s: %v", root, err)
				}
				body, err := os.ReadFile(filepath.Join(root, "internal", "one", "base.txt"))
				if err != nil || string(body) != "base\n" {
					t.Fatalf("native edit escaped its checkout: %q %v", body, err)
				}
			}
			replayed, err := workbench.ExecuteWorkbench(t.Context(), request)
			if err != nil || !replayed.Replayed || provider.calls != 8 {
				t.Fatalf("finished intent replayed: %v calls=%d err=%v", replayed.Replayed, provider.calls, err)
			}
		})
	}
}

func TestBatchWorkerSubmitCleanupFailureReplaysOnlyCleanup(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixtureWithWorker(t, false, domain.RunExecutionPermissionFull, &domain.WorkerProfile{})
	workbench, prepared := prepareBatchWorkbenchFixture(t, fixture)
	owned := prepared.Snapshot.Workspaces[0]
	provider := &nativeBatchTestProvider{t: t, root: owned.WorktreeRoot}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runtime := NewAgentRunner(fixture.store, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(fixture.caps)
	worker := NewBatchDeliveryModelWorker(fixture.service, fixture.store, router, policy.NewDefaultChecker()).WithNativeTools().WithWorkerRuntime(runtime)
	closeFailure := errors.New("controlled resource close failure")
	closer := &batchWorkerCloseProbe{BatchDeliveryModelWorker: worker, closeErr: closeFailure}
	workbench.WithWorker(closer)
	request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1, ExpectedGeneration: 1, OperationKey: "batch-worker-close-001", Confirm: true}
	if _, err := workbench.ExecuteWorkbench(t.Context(), request); !errors.Is(err, closeFailure) {
		t.Fatalf("cleanup failure was hidden: %v", err)
	}
	current, err := workbench.WorkbenchSnapshot(t.Context(), request.PlanID)
	if err != nil || len(current.Snapshot.Receipts) != 1 || current.Children[0].OutcomeUnresolved || len(provider.requests) != 7 || len(closer.closed) != 1 {
		t.Fatalf("submitted result was lost: receipts=%d model=%d closed=%v err=%v", len(current.Snapshot.Receipts), len(provider.requests), closer.closed, err)
	}
	closer.closeErr = nil
	replayed, err := workbench.ExecuteWorkbench(t.Context(), request)
	if err != nil || !replayed.Replayed || len(provider.requests) != 7 || len(closer.closed) != 2 || len(replayed.Snapshot.Receipts) != 1 {
		t.Fatalf("cleanup recovery replayed work: replay=%t model=%d closed=%v err=%v", replayed.Replayed, len(provider.requests), closer.closed, err)
	}
}
