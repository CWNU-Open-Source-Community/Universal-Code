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
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

type nativeBatchTestProvider struct {
	t        *testing.T
	requests []llm.ChatRequest
	create   bool
	root     string
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
		if p.create {
			args = toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "internal/one/native.txt", ExpectedSHA256: "missing", Content: "native child\n"}
		}
	case 4:
		var edit struct {
			fileedit.Preview
			ApplyArguments json.RawMessage `json:"apply_arguments"`
		}
		previous(&edit)
		if edit.ID == "" || edit.Status != fileedit.StatusProposed || !strings.Contains(edit.Diff, "+native child") || len(edit.ApplyArguments) == 0 {
			p.t.Fatalf("proposal did not provide a reviewable diff and apply arguments: %+v", edit)
		}
		before, err := os.ReadFile(filepath.Join(p.root, filepath.FromSlash(edit.Path)))
		if p.create {
			if !os.IsNotExist(err) {
				p.t.Fatalf("proposal wrote a new file before apply: %v", err)
			}
		} else if err != nil || string(before) != "base\n" {
			p.t.Fatalf("proposal wrote the patch before apply: %q %v", before, err)
		}
		name = "workspace_apply"
		args = edit.ApplyArguments
	case 5:
		var applied fileedit.Preview
		previous(&applied)
		if applied.Status != fileedit.StatusApplied || applied.ProposedHash != fileedit.HashText("native child\n") {
			p.t.Fatalf("proposal arguments did not apply the reviewed edit: %+v", applied)
		}
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
	found := false
	for _, tool := range request.Tools {
		if tool.Name != name {
			continue
		}
		found = true
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("native-tool.json", strings.NewReader(string(tool.Parameters))); err != nil {
			p.t.Fatal(err)
		}
		schema, err := compiler.Compile("native-tool.json")
		if err != nil {
			p.t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			p.t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			p.t.Fatalf("%s does not accept its model-visible arguments: %s %v", name, payload, err)
		}
		if (name == "workspace_change" || name == "workspace_apply") && !strings.Contains(tool.Description, "apply_arguments") {
			p.t.Fatalf("%s description omitted the proposal/apply handoff: %s", name, tool.Description)
		}
	}
	if !found {
		p.t.Fatalf("native tool was not advertised: %s", name)
	}
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
	// The long ownership list previously needed four brief messages by itself;
	// it must coexist with the durable admission goal in the same first turn.
	for index := range 24 {
		fixture.spec.Tasks[0].OwnershipHints = append(fixture.spec.Tasks[0].OwnershipHints,
			domain.BatchDeliveryOwnershipHint{
				Path: "long/" + strings.Repeat("x", 89) + fmt.Sprintf("%02d.txt", index),
				Kind: domain.BatchDeliveryOwnershipFile,
			})
	}
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(fixture.repository, "internal", "one", fmt.Sprintf("large-%d.txt", i)), []byte(strings.Repeat("observed source\n", 700)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, "-C", fixture.repository, "add", "internal/one")
	fixtureGit(t, "-C", fixture.repository, "commit", "--quiet", "-m", "seed beyond snapshot ceiling")
	// Reuse admission/owner setup; the native provider replaces the bridge provider.
	_, work, _ := batchModelWorkerFixture(t, fixture, &batchEditTestProvider{}, fixture.store)
	provider := &nativeBatchTestProvider{t: t, root: work.Workspace.WorktreeRoot}
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
	first := provider.requests[0]
	if strings.Count(first.Messages[0].Content, batchNativeDirections) != 1 ||
		strings.Contains(first.Messages[0].Content, batchEditDirections) {
		t.Fatal("native Batch system protocol was lost or mixed with the no-tool protocol")
	}
	text, parts := batchTestBriefText(t, first)
	if parts != 3 || first.Metadata["parent_instructions"] != "4" {
		t.Fatalf("native ownership brief and goal did not share the delivery window: parts=%d metadata=%v", parts, first.Metadata)
	}
	var brief struct {
		Owned     []domain.BatchDeliveryOwnershipHint `json:"owned"`
		Artifacts []domain.ChildTaskExpectedArtifact  `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(text), &brief); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(brief.Owned, fixture.spec.Tasks[0].OwnershipHints) ||
		!reflect.DeepEqual(brief.Artifacts, fixture.spec.Tasks[0].ExpectedArtifacts) {
		t.Fatal("native Batch ownership or artifacts were truncated")
	}
	var previousShape map[string]any
	if err := json.Unmarshal([]byte(text), &previousShape); err != nil {
		t.Fatal(err)
	}
	previousShape["directions"] = batchNativeDirections
	encoded, err := domain.MarshalSpecialistDeliveryContext(previousShape)
	if err != nil || utf8.RuneCount(encoded) <= 3*(domain.MaxSpecialistInstructionRunes-160) ||
		utf8.RuneCount(encoded) > 4*(domain.MaxSpecialistInstructionRunes-160) {
		t.Fatalf("native fixture no longer covers the former four-part brief: runes=%d error=%v", utf8.RuneCount(encoded), err)
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

func TestNativeBatchChildCreatesFromReviewableProposalArguments(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	_, work, _ := batchModelWorkerFixture(t, fixture, &batchEditTestProvider{}, fixture.store)
	provider := &nativeBatchTestProvider{t: t, create: true, root: work.Workspace.WorktreeRoot}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	worker := NewBatchDeliveryModelWorker(fixture.service, fixture.store, router, policy.NewDefaultChecker()).WithNativeTools()
	if _, err := worker.ExecuteBatchChild(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "native.txt")); err != nil || string(got) != "native child\n" {
		t.Fatalf("isolated created source=%q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.repository, "internal", "one", "native.txt")); !os.IsNotExist(err) {
		t.Fatalf("native child created a parent file: %v", err)
	}
}
