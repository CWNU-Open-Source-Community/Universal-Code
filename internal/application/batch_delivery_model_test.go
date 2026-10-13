package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type batchEditTestProvider struct {
	requests   []llm.ChatRequest
	respond    func(llm.ChatRequest, int) batchEditProposal
	rawRespond func(llm.ChatRequest, int) string
}

func (*batchEditTestProvider) Name() string { return "batch-edit-test" }
func (*batchEditTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "batch-edit-test"}}, nil
}
func (*batchEditTestProvider) SupportsTools(string) bool    { return false }
func (*batchEditTestProvider) SupportsVision(string) bool   { return false }
func (*batchEditTestProvider) SupportsJSONMode(string) bool { return true }
func (p *batchEditTestProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests = append(p.requests, request)
	var proposal string
	if p.rawRespond != nil {
		proposal = p.rawRespond(request, len(p.requests))
	} else {
		encoded, _ := json.Marshal(p.respond(request, len(p.requests)))
		proposal = string(encoded)
	}
	action, _ := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion,
		Kind: domain.SpecialistActionContinue, Message: proposal})
	return &llm.ChatResponse{Text: string(action), Provider: p.Name(), Model: "model",
		FinishReason: llm.FinishReasonStop, Usage: llm.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}, nil
}
func (p *batchEditTestProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func batchModelTestBrief(t *testing.T, request llm.ChatRequest) (files []batchEditFile, feedback string) {
	t.Helper()
	text, _ := batchModelTestBriefText(t, request)
	var brief struct {
		Files    []batchEditFile `json:"files"`
		Feedback string          `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(text), &brief); err != nil {
		t.Fatal(err)
	}
	return brief.Files, brief.Feedback
}

func batchModelTestBriefText(t *testing.T, request llm.ChatRequest) (string, int) {
	t.Helper()
	if len(request.Tools) != 0 {
		t.Fatal("Batch model received tools")
	}
	if len(request.Messages) == 0 || request.Messages[0].Role != "system" ||
		strings.Count(request.Messages[0].Content, batchEditDirections) != 1 {
		t.Fatal("Batch edit protocol is missing from the system prompt")
	}
	return batchTestBriefText(t, request)
}

func batchTestBriefText(t *testing.T, request llm.ChatRequest) (string, int) {
	t.Helper()
	var envelope struct {
		Instructions []struct {
			Instruction string `json:"instruction"`
		} `json:"parent_instructions"`
	}
	if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	var parts strings.Builder
	var parentInstructions []string
	partCount, expectedParts := 0, 0
	for _, part := range envelope.Instructions {
		if utf8.RuneCountInString(part.Instruction) > domain.MaxSpecialistInstructionRunes {
			t.Fatal("Batch brief part exceeds the Specialist instruction limit")
		}
		if !strings.HasPrefix(part.Instruction, "Current Batch edit brief JSON part ") {
			parentInstructions = append(parentInstructions, part.Instruction)
			continue
		}
		var ordinal, total int
		if _, err := fmt.Sscanf(part.Instruction, "Current Batch edit brief JSON part %d/%d;", &ordinal, &total); err != nil ||
			ordinal != partCount+1 || total < 1 || expectedParts != 0 && total != expectedParts {
			t.Fatalf("invalid Batch brief part order: %q", part.Instruction)
		}
		header := fmt.Sprintf(batchEditBriefPartHeader, ordinal, total)
		if !strings.HasPrefix(part.Instruction, header) || !strings.HasSuffix(part.Instruction, batchEditBriefPartEnd) {
			t.Fatal("missing Batch brief part framing")
		}
		parts.WriteString(strings.TrimSuffix(strings.TrimPrefix(part.Instruction, header), batchEditBriefPartEnd))
		partCount++
		expectedParts = total
	}
	if partCount == 0 || partCount != expectedParts {
		t.Fatalf("incomplete Batch brief: %d of %d parts", partCount, expectedParts)
	}
	var brief struct {
		Task       string `json:"task"`
		Directions string `json:"directions"`
	}
	if err := json.Unmarshal([]byte(parts.String()), &brief); err != nil {
		t.Fatal(err)
	}
	goalCount := 0
	for _, instruction := range parentInstructions {
		if instruction == brief.Task {
			goalCount++
		}
	}
	if brief.Task == "" || goalCount != 1 {
		t.Fatalf("admission goal must coexist with the Batch brief exactly once: goal=%q instructions=%q", brief.Task, parentInstructions)
	}
	if brief.Directions != "" {
		t.Fatal("fixed protocol still consumes durable brief capacity")
	}
	return parts.String(), partCount
}

func TestBatchModelWorkerPatchesExactCRLFAndUTF8BOMSourceBytes(t *testing.T) {
	for _, source := range []struct{ name, raw string }{
		{"crlf", "first\r\n第二行\r\n"},
		{"utf8-bom", "\ufefffirst\n第二行\n"},
		{"utf8-bom-crlf", "\ufefffirst\r\n第二行\r\n"},
	} {
		t.Run(source.name, func(t *testing.T) {
			fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
			batchModelTestCommitSource(t, fixture.repository, source.raw)
			provider := &batchEditTestProvider{respond: func(request llm.ChatRequest, _ int) batchEditProposal {
				files, _ := batchModelTestBrief(t, request)
				for _, file := range files {
					if file.Path != "internal/one/base.txt" {
						continue
					}
					digest := sha256.Sum256([]byte(source.raw))
					if file.Content != source.raw || file.SHA256 != hex.EncodeToString(digest[:]) {
						t.Fatalf("model source changed raw bytes: content=%q hash=%q", file.Content, file.SHA256)
					}
					return batchEditProposal{Version: batchEditProposalVersion, Summary: "update exact source bytes",
						Changes: []batchEditChange{{Action: "patch", Path: file.Path, ExpectedSHA256: file.SHA256,
							Replacements: []toolgateway.WorkspaceReplacement{{OldText: file.Content, NewText: strings.Replace(source.raw, "first", "updated", 1), ExpectedOccurrences: 1}}}}}
				}
				t.Fatal("owned raw-byte source missing")
				return batchEditProposal{}
			}}
			_, work, worker := batchModelWorkerFixture(t, fixture, provider, fixture.store)
			head := fixtureGit(t, "-C", work.Workspace.WorktreeRoot, "rev-parse", "HEAD")
			result, err := worker.ExecuteBatchChild(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "base.txt"))
			if err != nil || !bytes.Equal(content, []byte(strings.Replace(source.raw, "first", "updated", 1))) {
				t.Fatalf("patched bytes=%q error=%v", content, err)
			}
			if len(provider.requests) != 1 || len(result.EvidenceRefs) != 3 || fixtureGit(t, "-C", work.Workspace.WorktreeRoot, "rev-parse", "HEAD") == head {
				t.Fatal("exact-byte patch did not produce one accounted committed edit")
			}
			original, err := os.ReadFile(filepath.Join(fixture.repository, "internal", "one", "base.txt"))
			if err != nil || !bytes.Equal(original, []byte(source.raw)) {
				t.Fatal("source checkout changed")
			}
		})
	}
}

func TestBatchModelWorkerDispatchesLongCompleteBriefWithinFourMessageContext(t *testing.T) {
	for _, lines := range []int{170, 215} {
		t.Run(fmt.Sprintf("%d_source_lines", lines), func(t *testing.T) {
			fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
			raw := strings.Repeat("source text\n", lines)
			batchModelTestCommitSource(t, fixture.repository, raw)
			provider := &batchEditTestProvider{respond: func(request llm.ChatRequest, _ int) batchEditProposal {
				text, parts := batchModelTestBriefText(t, request)
				// Check against the old complete input bound, including the fixed
				// protocol now carried separately in the system prompt.
				var original map[string]any
				if err := json.Unmarshal([]byte(text), &original); err != nil {
					t.Fatal(err)
				}
				original["directions"] = batchEditDirections
				encoded, err := domain.MarshalSpecialistDeliveryContext(original)
				if err != nil {
					t.Fatal(err)
				}
				minimum := 2800
				if lines == 215 {
					minimum = 4000
				}
				if size := utf8.RuneCount(encoded); size <= minimum || size > domain.MaxSpecialistMessageRunes {
					t.Fatalf("complete source input outside regression range: %d runes", size)
				}
				if parts != domain.MaxSpecialistContextMessages-1 || request.Metadata["parent_instructions"] != "4" {
					t.Fatalf("long brief and admission goal did not share the four-message Specialist context: parts=%d metadata=%v", parts, request.Metadata)
				}
				files, _ := batchModelTestBrief(t, request)
				for _, file := range files {
					if file.Path == "internal/one/base.txt" {
						digest := sha256.Sum256([]byte(raw))
						if file.Content != raw || file.SHA256 != hex.EncodeToString(digest[:]) {
							t.Fatal("long source or its exact hash changed")
						}
						return batchEditProposal{Version: batchEditProposalVersion, Summary: "revise long owned text", Changes: []batchEditChange{
							{Action: "patch", Path: file.Path, ExpectedSHA256: file.SHA256, Replacements: []toolgateway.WorkspaceReplacement{{OldText: "source text", NewText: "updated text", ExpectedOccurrences: lines}}},
						}}
					}
				}
				t.Fatal("long source was omitted")
				return batchEditProposal{}
			}}
			_, work, worker := batchModelWorkerFixture(t, fixture, provider, fixture.store)
			if _, err := worker.ExecuteBatchChild(t.Context(), work); err != nil {
				t.Fatal(err)
			}
			if len(provider.requests) != 1 {
				t.Fatal("long complete brief did not dispatch exactly once")
			}
			content, err := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "base.txt"))
			if err != nil || string(content) != strings.Repeat("updated text\n", lines) {
				t.Fatalf("long-source edit=%q error=%v", content, err)
			}
		})
	}
}

func TestBatchModelWorkerRejectsSecretsBeforeAnyEdits(t *testing.T) {
	for _, location := range []string{"escaped-summary", "escaped-second-edit", "assembled-second-edit", "literal-second-edit"} {
		t.Run(location, func(t *testing.T) {
			fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
			if location == "assembled-second-edit" {
				if err := os.WriteFile(filepath.Join(fixture.repository, "internal", "one", "value.go"), []byte("package one\n\nconst Value = \"sk-PLACEHOLDER\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, "-C", fixture.repository, "add", "internal/one/value.go")
				fixtureGit(t, "-C", fixture.repository, "commit", "--quiet", "-m", "seed nonsecret placeholder")
			}
			secret := "sk-" + strings.Repeat("fixtureonly", 3)
			provider := &batchEditTestProvider{rawRespond: func(request llm.ChatRequest, _ int) string {
				files, _ := batchModelTestBrief(t, request)
				var base, value batchEditFile
				for _, file := range files {
					if file.Path == "internal/one/base.txt" {
						base = file
					}
					if file.Path == "internal/one/value.go" {
						value = file
					}
				}
				if base.Path == "" {
					t.Fatal("owned source missing")
				}
				proposal := batchEditProposal{Version: batchEditProposalVersion, Summary: "safe summary", Changes: []batchEditChange{
					{Action: "patch", Path: base.Path, ExpectedSHA256: base.SHA256, Replacements: []toolgateway.WorkspaceReplacement{{OldText: base.Content, NewText: "safe first change\n", ExpectedOccurrences: 1}}},
				}}
				if location == "escaped-summary" {
					proposal.Summary = secret
				} else if location == "assembled-second-edit" {
					if value.Path == "" || !strings.Contains(value.Content, "sk-PLACEHOLDER") {
						t.Fatal("assembled-secret source fixture missing")
					}
					proposal.Changes = append(proposal.Changes, batchEditChange{Action: "patch", Path: value.Path, ExpectedSHA256: value.SHA256,
						Replacements: []toolgateway.WorkspaceReplacement{{OldText: "PLACEHOLDER", NewText: strings.Repeat("a", 24), ExpectedOccurrences: 1}}})
				} else {
					proposal.Changes = append(proposal.Changes, batchEditChange{Action: "create", Path: "internal/one/new.txt", ExpectedSHA256: "missing", Content: secret + "\n"})
				}
				encoded, err := json.Marshal(proposal)
				if err != nil {
					t.Fatal(err)
				}
				if location == "literal-second-edit" {
					if redact.String(string(encoded)) == string(encoded) {
						t.Fatal("literal-secret fixture was not recognized by Specialist redaction")
					}
					return string(encoded)
				}
				raw := strings.ReplaceAll(string(encoded), secret, `\u0073k-`+strings.TrimPrefix(secret, "sk-"))
				if strings.Contains(raw, secret) || redact.String(raw) != raw {
					t.Fatal("fixture did not isolate decoded-secret detection from raw-string detection")
				}
				return raw
			}}
			_, work, worker := batchModelWorkerFixture(t, fixture, provider, fixture.store)
			head := fixtureGit(t, "-C", work.Workspace.WorktreeRoot, "rev-parse", "HEAD")
			_, err := worker.ExecuteBatchChild(t.Context(), work)
			want := apperror.CodePolicyDenied
			if location == "escaped-summary" {
				want = apperror.CodeInvalidArgument
			}
			if apperror.CodeOf(err) != want {
				t.Fatalf("decoded-secret error=%v", err)
			}
			if len(provider.requests) != 1 {
				t.Fatal("secret fixture never completed a model request")
			}
			child, err := fixture.store.GetAgentNode(t.Context(), work.Workspace.AgentID)
			if err != nil || child.TurnsUsed != 1 || child.TokensUsed != 30 || child.Status != domain.AgentReady {
				t.Fatalf("rejected response lost model accounting: %+v error=%v", child, err)
			}
			batchModelTestAssertNoEdits(t, fixture, work, head)
		})
	}
}

type batchMessageTrackingStore struct {
	*store.SQLiteStore
	sends int
}

func (s *batchMessageTrackingStore) SendAgentMessage(ctx context.Context, message domain.AgentMessage, key string) (domain.AgentMessage, bool, error) {
	s.sends++
	return s.SQLiteStore.SendAgentMessage(ctx, message, key)
}

func TestBatchModelWorkerPendingSourceOverflowStopsBeforeAnyMessagesOrModel(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	provider := &batchEditTestProvider{respond: func(llm.ChatRequest, int) batchEditProposal {
		t.Fatal("model called with overflowing pending sources")
		return batchEditProposal{}
	}}
	tracked := &batchMessageTrackingStore{SQLiteStore: fixture.store}
	_, work, worker := batchModelWorkerFixture(t, fixture, provider, tracked)
	initial, err := fixture.store.ListAgentMessages(t.Context(), work.Workspace.AgentID, false, domain.MaxAgentInboxMessages)
	if err != nil || len(initial) != 1 || initial[0].Status != domain.AgentMessagePending {
		t.Fatalf("missing pending admission goal: %+v error=%v", initial, err)
	}
	goal, err := domain.DecodeAgentInstructionPayload(initial[0].PayloadJSON)
	if err != nil || goal.Instruction != work.Task.Goal {
		t.Fatalf("incorrect admission goal: %+v error=%v", goal, err)
	}
	for index := 0; index < 3; index++ {
		payload, err := json.Marshal(domain.AgentInstructionPayload{Version: domain.SpecialistInstructionVersion, Instruction: "Preserve the existing pending parent instruction."})
		if err != nil {
			t.Fatal(err)
		}
		_, replayed, err := fixture.store.SendAgentMessage(t.Context(), domain.AgentMessage{ID: fmt.Sprintf("pending-source-%d", index), RunID: fixture.run.ID,
			SenderAgentID: fixture.root.ID, RecipientAgentID: work.Workspace.AgentID, Kind: domain.AgentMessageInstruction,
			Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(payload)}, fmt.Sprintf("pending-source-operation-%d", index))
		if err != nil || replayed {
			t.Fatalf("pending instruction error=%v replayed=%t", err, replayed)
		}
	}
	before, err := fixture.store.ListAgentMessages(t.Context(), work.Workspace.AgentID, false, domain.MaxAgentInboxMessages)
	if err != nil || len(before) != len(initial)+3 || before[0] != initial[0] {
		t.Fatalf("pending source fixture=%+v error=%v", before, err)
	}
	head := fixtureGit(t, "-C", work.Workspace.WorktreeRoot, "rev-parse", "HEAD")
	result, err := worker.ExecuteBatchChild(t.Context(), work)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("pending overflow error=%v", err)
	}
	if tracked.sends != 0 || len(provider.requests) != 0 || len(result.EvidenceRefs) != 0 {
		t.Fatal("overflow sent a partial brief or dispatched a model")
	}
	after, err := fixture.store.ListAgentMessages(t.Context(), work.Workspace.AgentID, false, domain.MaxAgentInboxMessages)
	if err != nil || len(after) != len(before) {
		t.Fatalf("overflow changed inbox: %+v error=%v", after, err)
	}
	for index := range before {
		if after[index] != before[index] {
			t.Fatal("overflow modified a pending source")
		}
	}
	child, err := fixture.store.GetAgentNode(t.Context(), work.Workspace.AgentID)
	if err != nil || child.TurnsUsed != 0 || child.TokensUsed != 0 || child.Status != domain.AgentReady {
		t.Fatalf("overflow dispatched or mutated child accounting: %+v error=%v", child, err)
	}
	batchModelTestAssertNoEdits(t, fixture, work, head)
}

func batchModelTestCommitSource(t *testing.T, repository, raw string) {
	t.Helper()
	fixtureGit(t, "-C", repository, "config", "core.autocrlf", "false")
	// Preserve CRLF in both the source checkout and index while keeping the
	// real Git diff check meaningful for spaces and other whitespace defects.
	fixtureGit(t, "-C", repository, "config", "core.whitespace", "cr-at-eol")
	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte("internal/one/base.txt -text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", "one", "base.txt"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", repository, "add", ".gitattributes", "internal/one/base.txt")
	fixtureGit(t, "-C", repository, "commit", "--quiet", "-m", "seed exact source bytes")
}

func batchModelTestAssertNoEdits(t *testing.T, fixture batchDeliveryApplicationFixture, work BatchDeliveryWorkRequest, head string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "base.txt"))
	if err != nil || string(content) != "base\n" {
		t.Fatalf("rejected proposal changed the first file: %q error=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected proposal created the second file: %v", err)
	}
	if fixtureGit(t, "-C", work.Workspace.WorktreeRoot, "rev-parse", "HEAD") != head || fixtureGit(t, "-C", work.Workspace.WorktreeRoot, "status", "--porcelain") != "" {
		t.Fatal("rejected work changed Git state")
	}
	edits, err := fixture.store.ListFileEdits(t.Context(), fileedit.ListFilter{})
	if err != nil || len(edits) != 0 {
		t.Fatalf("rejected work created edit proposals: %+v error=%v", edits, err)
	}
}

func TestBatchModelWorkerEditsSubmitsAndReworksWithAccountedTurns(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	source := strings.Repeat("base\n", 120)
	batchModelTestCommitSource(t, fixture.repository, source)
	const parentConstraint = "Preserve the exported API in every revision."
	provider := &batchEditTestProvider{respond: func(request llm.ChatRequest, turn int) batchEditProposal {
		_, parts := batchModelTestBriefText(t, request)
		wantParts := 2
		if turn == 2 {
			wantParts = 1
		}
		if parts != wantParts {
			t.Fatalf("rework retained stale Batch parts: got %d, want %d", parts, wantParts)
		}
		var envelope specialistContextEnvelope
		if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &envelope); err != nil {
			t.Fatal(err)
		}
		preserved := false
		for _, instruction := range envelope.ParentInstructions {
			preserved = preserved || instruction.Instruction == parentConstraint
		}
		if !preserved {
			t.Fatal("Batch execution lost the additional parent constraint")
		}
		files, feedback := batchModelTestBrief(t, request)
		if turn == 2 && feedback != "use revised wording" {
			t.Fatalf("missing current feedback: %q", feedback)
		}
		if turn == 1 && feedback != "" {
			t.Fatalf("unexpected feedback: %q", feedback)
		}
		for _, file := range files {
			if file.Path == "internal/one/base.txt" {
				digest := sha256.Sum256([]byte(file.Content))
				if file.SHA256 != hex.EncodeToString(digest[:]) {
					t.Fatal("rework source hash does not describe the current bytes")
				}
				replacement := "first version\n"
				if turn == 2 {
					replacement = "revised version\n"
				}
				return batchEditProposal{Version: batchEditProposalVersion, Summary: "revise child text",
					Changes: []batchEditChange{{Action: "patch", Path: file.Path, ExpectedSHA256: file.SHA256,
						Replacements: []toolgateway.WorkspaceReplacement{{OldText: file.Content, NewText: replacement, ExpectedOccurrences: 1}}}}}
			}
		}
		t.Fatal("owned source was omitted")
		return batchEditProposal{}
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	workbench := NewBatchDeliveryWorkbenchService(fixture.service).
		WithWorker(NewBatchDeliveryModelWorker(fixture.service, fixture.store, router, policy.NewDefaultChecker()))
	prepared, err := workbench.PrepareWorkbench(t.Context(), PrepareBatchDeliveryWorkbenchRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, OperationKey: "model-workbench-prepare-001", RequestedBy: fixture.root.ID, Confirm: true,
		Tasks: []BatchDeliveryWorkbenchTask{
			{Ordinal: 1, OwnershipHints: fixture.spec.Tasks[0].OwnershipHints, Validations: fixture.spec.Tasks[0].Validations},
			{Ordinal: 2, OwnershipHints: fixture.spec.Tasks[1].OwnershipHints, Validations: fixture.spec.Tasks[1].Validations},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	planID := prepared.Snapshot.Plan.ID
	payload, _ := json.Marshal(domain.AgentInstructionPayload{
		Version: domain.SpecialistInstructionVersion, Instruction: parentConstraint,
	})
	if _, _, err := fixture.store.SendAgentMessage(t.Context(), domain.AgentMessage{
		ID: "batch-additional-parent-constraint", RunID: fixture.run.ID,
		SenderAgentID: fixture.root.ID, RecipientAgentID: prepared.Snapshot.Workspaces[0].AgentID,
		Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage,
		PayloadJSON: string(payload),
	}, "batch-additional-parent-constraint-operation"); err != nil {
		t.Fatal(err)
	}
	request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: planID, Ordinal: 1, ExpectedGeneration: 1, OperationKey: "model-workbench-execute-001", Confirm: true}
	first, err := workbench.ExecuteWorkbench(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Snapshot.Receipts) != 1 || first.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceReadyForReview {
		t.Fatalf("missing real receipt: %+v", first)
	}
	if len(first.Snapshot.Receipts[0].EvidenceRefs) < 3 {
		t.Fatal("model attempt, edit and commit evidence were not retained")
	}
	if _, err := workbench.ExecuteWorkbench(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatal("execution replay dispatched the model twice")
	}
	if _, _, err := workbench.Review(t.Context(), ReviewBatchDeliveryRequest{PlanID: planID, Ordinal: 1, Generation: 1,
		Reviewer: fixture.root.ID, Verdict: domain.BatchReviewChangesRequested, Summary: "use revised wording",
		FullDiffReviewed: true, CallChainReviewed: true, TestsReviewed: true, OperationKey: "model-workbench-review-001"}); err != nil {
		t.Fatal(err)
	}
	recovered, err := workbench.RenewWorkbenchOwner(t.Context(), BatchDeliveryWorkbenchOwnerRequest{PlanID: planID,
		Ordinal: 1, ExpectedGeneration: 1, OperationKey: "model-workbench-recover-001", RequestedBy: fixture.root.ID, Retry: true, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedGeneration, request.OperationKey = recovered.Snapshot.Workspaces[0].Generation, "model-workbench-execute-002"
	second, err := workbench.ExecuteWorkbench(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Snapshot.Receipts) != 1 || second.Snapshot.Receipts[0].Generation != 2 ||
		second.Snapshot.Receipts[0].ID == first.Snapshot.Receipts[0].ID || len(provider.requests) != 2 {
		t.Fatal("feedback did not produce one new accounted delivery")
	}
	previous, found, err := fixture.store.GetBatchDeliveryReceipt(t.Context(), planID, 1, 1)
	if err != nil || !found || previous.ID != first.Snapshot.Receipts[0].ID {
		t.Fatal("rework lost the original immutable delivery receipt")
	}
	child, err := fixture.store.GetAgentNode(t.Context(), second.Snapshot.Workspaces[0].AgentID)
	if err != nil || child.TurnsUsed != 2 || child.TokensUsed != 60 || child.Status != domain.AgentReady {
		t.Fatalf("child accounting=%+v err=%v", child, err)
	}
	content, err := os.ReadFile(filepath.Join(second.Snapshot.Workspaces[0].WorktreeRoot, "internal", "one", "base.txt"))
	if err != nil || string(content) != "revised version\n" {
		t.Fatalf("reworked file=%q err=%v", content, err)
	}
	if original, _ := os.ReadFile(filepath.Join(fixture.repository, "internal", "one", "base.txt")); string(original) != source {
		t.Fatal("Batch edited the source checkout")
	}
	for _, request := range provider.requests {
		encoded, _ := json.Marshal(request.Messages)
		for _, owner := range workbench.owners {
			if strings.Contains(string(encoded), owner.OwnerToken) {
				t.Fatal("owner token entered model input")
			}
		}
	}
}

func TestBatchEditProposalRejectsUnobservedAndForeignEdits(t *testing.T) {
	task := domain.BatchDeliveryTaskSpec{OwnershipHints: []domain.BatchDeliveryOwnershipHint{{Path: "src", Kind: domain.BatchDeliveryOwnershipDirectory}}}
	files := []batchEditFile{{Path: "src/a.go", SHA256: strings.Repeat("a", 64), Content: "old"}}
	valid := batchEditChange{Action: "patch", Path: "src/a.go", ExpectedSHA256: files[0].SHA256,
		Replacements: []toolgateway.WorkspaceReplacement{{OldText: "old", NewText: "new", ExpectedOccurrences: 1}}}
	for _, kind := range []string{"foreign", "stale", "unread", "duplicate", "occurrence", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			change := valid
			change.Replacements = append([]toolgateway.WorkspaceReplacement{}, valid.Replacements...)
			proposal := batchEditProposal{Version: batchEditProposalVersion, Summary: "edit", Changes: []batchEditChange{change}}
			switch kind {
			case "foreign":
				proposal.Changes[0].Path = "other/a.go"
			case "stale":
				proposal.Changes[0].ExpectedSHA256 = strings.Repeat("b", 64)
			case "unread":
				proposal.Changes[0].Path = "src/unread.go"
			case "duplicate":
				proposal.Changes = append(proposal.Changes, change)
			case "occurrence":
				proposal.Changes[0].Replacements[0].ExpectedOccurrences = 2
			case "unsupported":
				proposal.Changes[0].Action = "delete"
			}
			encoded, _ := json.Marshal(proposal)
			if _, err := decodeBatchEditProposal(string(encoded), task, files); err == nil {
				t.Fatal("unsafe proposal accepted")
			}
		})
	}
}

func TestBatchModelWorkerCreatesAnExplicitlyOwnedNewFile(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	fixture.spec.Tasks[0].OwnershipHints = []domain.BatchDeliveryOwnershipHint{{Path: "internal/one/new.txt", Kind: domain.BatchDeliveryOwnershipFile}}
	provider := &batchEditTestProvider{respond: func(request llm.ChatRequest, _ int) batchEditProposal {
		files, _ := batchModelTestBrief(t, request)
		if len(files) != 0 {
			t.Fatal("new file snapshot contained unrelated source")
		}
		return batchEditProposal{Version: batchEditProposalVersion, Summary: "create owned text", Changes: []batchEditChange{
			{Action: "create", Path: "internal/one/new.txt", ExpectedSHA256: "missing", Content: "created\n"},
		}}
	}}
	_, work, worker := batchModelWorkerFixture(t, fixture, provider, fixture.store)
	result, err := worker.ExecuteBatchChild(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EvidenceRefs) != 3 {
		t.Fatal("creation lacks execution evidence")
	}
	content, err := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "new.txt"))
	if err != nil || string(content) != "created\n" {
		t.Fatalf("created bytes=%q err=%v", content, err)
	}
}

type batchMissingContextStore struct{ *store.SQLiteStore }

func (s batchMissingContextStore) PrepareSpecialistContext(ctx context.Context, ref domain.AgentAttemptRef) (domain.SpecialistContextBatch, error) {
	batch, err := s.SQLiteStore.PrepareSpecialistContext(ctx, ref)
	batch.TaskBrief.Instructions = nil
	return batch, err
}

func TestBatchModelWorkerMissingSourceStopsBeforeModelAndWrites(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	provider := &batchEditTestProvider{respond: func(llm.ChatRequest, int) batchEditProposal {
		t.Fatal("model called with missing Batch context")
		return batchEditProposal{}
	}}
	_, work, worker := batchModelWorkerFixture(t, fixture, provider, batchMissingContextStore{fixture.store})
	if _, err := worker.ExecuteBatchChild(t.Context(), work); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("missing-source error=%v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatal("missing-source turn dispatched a model")
	}
	if content, _ := os.ReadFile(filepath.Join(work.Workspace.WorktreeRoot, "internal", "one", "base.txt")); string(content) != "base\n" {
		t.Fatal("missing-source turn changed a file")
	}
}

func batchModelWorkerFixture(t *testing.T, fixture batchDeliveryApplicationFixture, provider *batchEditTestProvider, state BatchDeliveryModelStore) (PrepareBatchDeliveryResult, BatchDeliveryWorkRequest, *BatchDeliveryModelWorker) {
	t.Helper()
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{RunID: fixture.run.ID, ProposalID: fixture.proposal.ID,
		Spec: fixture.spec, WorktreeParent: t.TempDir(), OperationKey: "model-worker-prepare-001", RequestedBy: fixture.root.ID, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	owner := prepared.Authorities[0]
	child, _, _, err := fixture.service.SendMessage(t.Context(), SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID,
		Ordinal: 1, Generation: 1, OwnerToken: owner.OwnerToken, Kind: domain.BatchMailboxAck, Summary: "ready", OperationKey: "model-worker-ack-001"})
	if err != nil {
		t.Fatal(err)
	}
	work := BatchDeliveryWorkRequest{Plan: prepared.Plan, Workspace: child, Task: fixture.proposal.Spec.Tasks[0],
		Authority: BatchDeliveryToolAuthority{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1, OwnerToken: owner.OwnerToken, OperationKey: "model-worker-execute-001"}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return prepared, work, NewBatchDeliveryModelWorker(fixture.service, state, router, policy.NewDefaultChecker())
}

func TestBatchModelContextRequiresCurrentSourceBeforeDispatch(t *testing.T) {
	message := domain.AgentMessage{ID: "current", RunID: "run", RecipientAgentID: "child", SenderAgentID: "root"}
	payload, _ := json.Marshal(domain.AgentInstructionPayload{Version: domain.SpecialistInstructionVersion, Instruction: "current text"})
	message.PayloadJSON = string(payload)
	batch := domain.SpecialistContextBatch{RunID: "run", AgentID: "child", ParentAgentID: "root",
		TaskBrief: domain.SpecialistTaskBrief{Instructions: []domain.SpecialistBriefInstruction{{SourceID: "older", Instruction: "current text"}}}}
	guard := batchEditContextGuard{messages: []domain.AgentMessage{message}}
	if err := guard.validate(batch); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("stale snapshot accepted: %v", err)
	}
	batch.TaskBrief.Instructions[0].SourceID = "current"
	if err := guard.validate(batch); err != nil {
		t.Fatal(err)
	}
}
