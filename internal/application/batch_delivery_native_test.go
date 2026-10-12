package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

type nativeBatchTestProvider struct {
	t        *testing.T
	requests []llm.ChatRequest
}

func (*nativeBatchTestProvider) Name() string { return "batch-native-test" }
func (*nativeBatchTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "batch-native-test"}}, nil
}
func (*nativeBatchTestProvider) SupportsTools(string) bool    { return true }
func (*nativeBatchTestProvider) SupportsJSONMode(string) bool { return true }
func (*nativeBatchTestProvider) SupportsVision(string) bool   { return false }
func (p *nativeBatchTestProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests = append(p.requests, request)
	n := len(p.requests)
	var name string
	var args any
	previous := func(target any) {
		last := request.Messages[len(request.Messages)-1]
		if len(last.ToolResults) != 1 {
			p.t.Fatalf("missing native result: %+v", last)
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(last.ToolResults[0].Content), &envelope); err != nil {
			p.t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Result, target); err != nil {
			p.t.Fatal(err)
		}
	}
	switch n {
	case 1:
		name = "workspace_glob"
		args = toolgateway.WorkspaceGlobPayload{Version: toolgateway.AgentCodeRegistryVersion, Pattern: "internal/one/*", Limit: 20}
	case 2:
		name = "workspace_read"
		args = toolgateway.WorkspaceReadPayload{Version: toolgateway.AgentCodeRegistryVersion, Path: "internal/one/base.txt", StartLine: 1, EndLine: 4}
	case 3:
		var read workspace.AgentCodeRead
		previous(&read)
		name = "workspace_change"
		args = toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: read.Path, ExpectedSHA256: read.ContentSHA256, Replacements: []toolgateway.WorkspaceReplacement{{OldText: "base", NewText: "native child", ExpectedOccurrences: 1}}}
	case 4:
		var edit fileedit.Preview
		previous(&edit)
		name = "workspace_apply"
		args = toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.ID, ExpectedAction: "propose_patch", ExpectedOriginalSHA256: edit.OriginalHash, ExpectedProposedSHA256: edit.ProposedHash}
	case 5:
		name = "git_commit"
		args = map[string]string{"message": "apply independently observed child edit"}
	case 6:
		name = "verify"
		args = map[string]any{}
	case 7:
		var check struct {
			Passed   bool                              `json:"passed"`
			Receipts []domain.BatchDeliveryTestReceipt `json:"receipts"`
		}
		previous(&check)
		if !check.Passed || len(check.Receipts) != 1 || check.Receipts[0].ExitCode != 0 {
			p.t.Fatalf("missing declared checks: %+v", check)
		}
		raw, _ := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Applied observed edit, ran the declared check and committed the isolated diff."})
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", Text: string(raw), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
	default:
		p.t.Fatal("unbounded native loop")
	}
	payload, _ := json.Marshal(args)
	return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("batch-native-%d", n), Name: name, Arguments: payload}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
}
func (p *nativeBatchTestProvider) StreamChat(ctx context.Context, r llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, r)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.ChatChunk, 2)
	out <- llm.ChatChunk{Text: response.Text}
	out <- llm.FinalChatChunk(response)
	close(out)
	return out, nil
}

func TestNativeBatchChildDiscoversBeyondFourFilesEditsVerifiesAndCommits(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(fixture.repository, "internal", "one", fmt.Sprintf("large-%d.txt", i)), []byte(strings.Repeat("observed source\n", 700)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, "-C", fixture.repository, "add", "internal/one")
	fixtureGit(t, "-C", fixture.repository, "commit", "--quiet", "-m", "seed beyond snapshot ceiling")
	// Reuse admission/owner setup; the native provider replaces the bridge provider.
	_, work, _ := batchModelWorkerFixture(t, fixture, &batchEditTestProvider{}, fixture.store)
	provider := &nativeBatchTestProvider{t: t}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	worker := NewBatchDeliveryModelWorker(fixture.service, fixture.store, router, policy.NewDefaultChecker()).WithNativeTools()
	result, err := worker.ExecuteBatchChild(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 7 || len(result.EvidenceRefs) < 8 {
		t.Fatalf("native work/result: requests=%d result=%+v", len(provider.requests), result)
	}
	child, err := fixture.store.GetAgentNode(t.Context(), work.Workspace.AgentID)
	if err != nil || child.TurnsUsed != 1 || child.TokensUsed != 35 {
		t.Fatalf("native child usage: %+v %v", child, err)
	}
	if got, err := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "base.txt")); err != nil || string(got) != "native child\n" {
		t.Fatalf("isolated source=%q %v", got, err)
	}
	if source, _ := os.ReadFile(filepath.Join(fixture.repository, "internal", "one", "base.txt")); string(source) != "base\n" {
		t.Fatal("native child modified parent source")
	}
	attempts, err := fixture.store.ListAgentAttempts(t.Context(), child.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v %v", attempts, err)
	}
	rounds, err := fixture.store.ListSpecialistToolRounds(t.Context(), specialistAttemptRef(attempts[0]))
	if err != nil || len(rounds) != 6 {
		t.Fatalf("native rounds=%+v %v", rounds, err)
	}
	for _, round := range rounds {
		if round.Calls[0].ChargeID == "" || round.Calls[0].Status != domain.SupervisorToolCompleted {
			t.Fatalf("unaccounted child receipt=%+v", round)
		}
	}
	for _, request := range provider.requests {
		encoded, _ := json.Marshal(request)
		if strings.Contains(string(encoded), work.Authority.OwnerToken) {
			t.Fatal("Batch owner token entered provider input")
		}
	}
	var verified struct {
		Result struct {
			Receipts []domain.BatchDeliveryTestReceipt `json:"receipts"`
		} `json:"result"`
	}
	if err = json.Unmarshal([]byte(rounds[5].Calls[0].ResultJSON), &verified); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{PlanID: work.Plan.ID, Ordinal: 1, Generation: 1, OwnerToken: work.Authority.OwnerToken, OperationKey: "native-batch-submit-0001", EvidenceRefs: result.EvidenceRefs})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt.TestReceipts, verified.Result.Receipts) {
		t.Fatalf("Submit repeated or changed the final native verification: %+v %+v", receipt.TestReceipts, verified.Result.Receipts)
	}
}
