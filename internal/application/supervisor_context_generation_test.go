package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/session"
)

type previousModelReport string

type summaryUsageFixture struct {
	ThreadContextCompactionStore
	total int64
}

func (s summaryUsageFixture) GetRunAgentUsage(context.Context, string) (domain.RunAgentUsage, error) {
	return domain.RunAgentUsage{TotalTokens: s.total}, nil
}

func (s previousModelReport) Generate(_ context.Context, input contextmgr.SummaryGenerationRequest) (contextmgr.SummaryGenerationResponse, error) {
	return contextmgr.SummaryGenerationResponse{Text: string(s), Receipt: contextmgr.SummaryGenerationReceipt{
		Provider: "fixture", Model: "summary", RunID: "run", AttemptID: "attempt", ModelAttempt: 1,
		CompletionSequence: 1, SourceSHA256: input.SourceSHA256,
	}}, nil
}

func TestSummaryGenerationPlacesNewerUserReversalAfterPreviousHandoff(t *testing.T) {
	const goal = "Prepare the public interface guide."
	const a = "Use the original public entry point."
	const b = "Use the renamed private entry point."
	const constraint = "Keep the command spelling unchanged."
	const stale = "MODEL_REPORT: the private entry point is current."
	const evidence = "The old design document still lists the private entry point."
	source := func(id int64, role, kind, text string) contextmgr.Message {
		return contextmgr.Message{SourceMessageID: id, Role: role, SourceKind: kind, Content: text,
			ContentSHA256: session.ContentSHA256(text), InstructionAuthorized: kind == session.SourceOperatorMessage}
	}
	manager := contextmgr.NewManager(nil, contextmgr.Config{PreserveRecentMessages: 1}).
		WithSummaryGenerator(previousModelReport(stale), session.ContentSHA256("first snapshot"))
	first, err := manager.Compact(t.Context(), "session", "workspace", []contextmgr.Message{
		source(1, "user", session.SourceOperatorMessage, a),
		source(2, "user", session.SourceOperatorMessage, constraint),
		source(3, "user", session.SourceOperatorMessage, b),
		source(4, "assistant", session.SourceModelResponse, "Recorded the private entry point."),
		source(5, "user", session.SourceOperatorMessage, a),
	})
	if err != nil || !first.Generated {
		t.Fatalf("prior generated handoff: %v %s", err, first.GenerationFallbackReason)
	}
	first.Summary.ID = 1
	input := contextmgr.SummaryGenerationRequest{TaskID: "session", WorkspaceID: "workspace", Previous: first.Summary, HasPrevious: true,
		Messages:     []contextmgr.Message{first.Preserved[0], source(6, "tool", session.SourceToolResult, evidence)},
		SourceSHA256: session.ContentSHA256("full snapshot"), MaxOutputChars: contextmgr.MaxGeneratedSummaryChars}
	turn := domain.SupervisorTurn{Mission: domain.Mission{Goal: goal}}
	window := llm.DefaultContextWindow()
	for _, manual := range []bool{true, false} {
		name := "automatic"
		if manual {
			name = "manual"
		}
		t.Run(name, func(t *testing.T) {
			var request llm.ChatRequest
			var err error
			if manual {
				g := threadContextSummaryGenerator{}
				g.snapshot.Mission = turn.Mission
				request, _, _, _, _, err = g.prepareSummaryRequest(t.Context(), llm.ChatRequest{}, window, input)
			} else {
				request, err = supervisorSummaryRequest(turn, input, llm.ModelRef{Provider: "test", Model: "fixture"}, window, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			data := request.Messages[1].Content
			var payload summaryModelInput
			if err := json.Unmarshal([]byte(data), &payload); err != nil || len(payload.Timeline) != 3 ||
				payload.Timeline[0].OriginalGoal != goal || strings.Contains(payload.Timeline[1].PreviousHandoff, stale) ||
				!strings.Contains(payload.Timeline[1].PreviousHandoff, "Recorded the private entry point.") ||
				!slices.Equal(payload.Timeline[2].Messages, input.Messages) {
				t.Fatal("timeline lost a source identity, user authority, or original tool observation")
			}
			quote := contextmgr.OperatorRestatement{EarlierIdenticalSource: "message:1", InterveningSource: "message:3", SourceID: "message:5", Text: a}
			if !strings.Contains(payload.Timeline[1].PreviousHandoff, constraint) || !strings.Contains(data, first.Summary.ContentSHA256) ||
				payload.Chronology == nil || *payload.Chronology != quote {
				t.Fatal("the source chronology must distinguish the later submission from the earlier identical source")
			}
			if !strings.Contains(request.Messages[0].Content, "verified_operator_chronology") {
				t.Fatal("the summary writer has no guidance for the delivered source chronology")
			}
			if !strings.Contains(payload.Timeline[1].PreviousHandoff, b) || strings.Count(data, b) != 1 {
				t.Fatal("the citation repeated an intervening user's words outside the historical source")
			}
			// With room only for the actual inputs, drop the optional citation.
			request.Messages = slices.Delete(slices.Clone(request.Messages), 1, 2)
			request.Messages[0].Content = strings.TrimSuffix(request.Messages[0].Content, quotedSubmissionGuidance)
			payload.Chronology = nil
			sourceJSON, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			requiredData, err := llm.ContextCompactionDataMessage(sourceJSON)
			if err != nil {
				t.Fatal(err)
			}
			sourceBudget := int64(estimateModelRequestTokens(request) + estimateModelMessageTokens(requiredData))
			if err := addSummaryHistory(&request, window, sourceBudget, goal, "", nil, input, 0); err != nil {
				t.Fatal(err)
			}
			var narrow summaryModelInput
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &narrow); err != nil || narrow.Chronology != nil ||
				len(narrow.Timeline) != 3 || !slices.Equal(narrow.Timeline[2].Messages, input.Messages) || narrow.Timeline[1].PreviousHandoff != payload.Timeline[1].PreviousHandoff ||
				int64(estimateModelRequestTokens(request)) != sourceBudget {
				t.Fatal("a tight budget displaced source history to keep an optional chronology")
			}
			if strings.Contains(request.Messages[0].Content, "verified_operator_chronology") {
				t.Fatal("the request asked the summary writer to interpret an undelivered chronology")
			}
			partial := llm.ChatRequest{Messages: []llm.Message{request.Messages[0]}, Metadata: map[string]string{}}
			if err := addSummaryHistory(&partial, window, 1<<30, goal, "", nil, input, 1); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(partial.Messages[0].Content+partial.Messages[1].Content, "verified_operator_chronology") {
				t.Fatal("a partial timeline might have omitted a later change to the quoted submission")
			}
			if manual {
				const spent = int64(91)
				g := threadContextSummaryGenerator{service: &ThreadContextCompactionService{store: summaryUsageFixture{total: spent}}}
				g.snapshot.Mission = turn.Mission
				g.snapshot.Run.Budget.MaxTokens = sourceBudget + int64(request.MaxTokens) + spent
				tight, _, _, omitted, _, err := g.prepareSummaryRequest(t.Context(), llm.ChatRequest{}, window, input)
				if err != nil || omitted != 0 || tight.Messages[1].Content != request.Messages[1].Content {
					t.Fatal("the optional citation consumed tokens reserved for source history on a capped Run")
				}
			}
			later := source(7, "user", session.SourceOperatorMessage, "Use the third public entry point.")
			more := input
			more.Messages = append(slices.Clone(input.Messages), later)
			next := llm.ChatRequest{Messages: []llm.Message{request.Messages[0]}, Metadata: map[string]string{}}
			if err := addSummaryHistory(&next, window, 1<<30, goal, "", nil, more, 0); err != nil {
				t.Fatal(err)
			}
			var nextInput summaryModelInput
			if err := json.Unmarshal([]byte(next.Messages[1].Content), &nextInput); err != nil || len(nextInput.Timeline) != 3 ||
				nextInput.Chronology == nil || *nextInput.Chronology != quote ||
				!slices.Equal(nextInput.Timeline[2].Messages, more.Messages) || next.Metadata["input_fingerprint"] != session.ContentSHA256(next.Messages[1].Content) ||
				next.Metadata["input_fingerprint"] == request.Metadata["input_fingerprint"] {
				t.Fatal("the historical quotation displaced a subsequent new user decision or reused the previous input receipt")
			}
		})
	}
}

func TestSummaryRequestPreservesSourcesOrFallsBackBeforeCallingModel(t *testing.T) {
	turn := domain.SupervisorTurn{Mission: domain.Mission{Goal: "原目标：保留接口"}}
	input := contextmgr.SummaryGenerationRequest{TaskID: "session", SourceSHA256: strings.Repeat("a", 64),
		Messages: []contextmgr.Message{{Content: strings.Repeat("旧历史", 20000)}}}
	_, err := supervisorSummaryRequest(turn, input, llm.ModelRef{Provider: "fixture", Model: "summary"}, llm.DefaultContextWindow(), true)
	if err == nil || !strings.Contains(err.Error(), "generation_input_window") {
		t.Fatalf("oversized originals must fall back before any truncated generation request: %v", err)
	}
	input.Messages[0].Content = "修改要求：保留 UTF-8；工具检查失败。"
	request, err := supervisorSummaryRequest(turn, input, llm.ModelRef{Provider: "fixture", Model: "summary"}, llm.DefaultContextWindow(), true)
	if err != nil || len(request.Tools) != 0 || request.Metadata["context_history_omitted"] != "0" ||
		!strings.Contains(request.Messages[1].Content, input.Messages[0].Content) || !strings.Contains(request.Messages[1].Content, turn.Mission.Goal) {
		t.Fatalf("complete source and original goal must survive without tools: request=%+v err=%v", request, err)
	}
	if strings.Contains(request.Messages[0].Content, "verified_operator_chronology") {
		t.Fatal("ordinary input unnecessarily asked the writer to resolve a repeated user submission")
	}
	if !strings.Contains(request.Messages[0].Content, "targeting at most 1200 Unicode characters") ||
		!strings.Contains(request.Messages[0].Content, "hard acceptance limit is 1600 Unicode characters") ||
		request.MaxTokens != llm.DefaultContextWindow().OutputLimit(2048) || !request.JSONMode {
		t.Fatalf("summary target must leave character headroom without lowering the JSON token allowance: %+v", request)
	}
}

func TestSummaryChronologyOnlyQuotesVerbatimProviderText(t *testing.T) {
	const original = `{"note":"pa\u0073sword=example-value"}`
	input := contextmgr.SummaryGenerationRequest{MaxOutputChars: contextmgr.MaxGeneratedSummaryChars}
	for i, text := range []string{original, "A separate update.", original} {
		input.Messages = append(input.Messages, contextmgr.Message{Role: "user", SourceKind: session.SourceOperatorMessage,
			InstructionAuthorized: true, SourceMessageID: int64(i + 1), Content: text, ContentSHA256: session.ContentSHA256(text)})
	}
	req, err := supervisorSummaryRequest(domain.SupervisorTurn{}, input, llm.ModelRef{}, llm.DefaultContextWindow(), true)
	if err != nil {
		t.Fatal(err)
	}
	var payload summaryModelInput
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	last := payload.Timeline[len(payload.Timeline)-1].Messages[2]
	if !strings.Contains(last.Content, "[REDACTED") || last.ContentSHA256 != session.ContentSHA256(original) ||
		payload.Chronology != nil || strings.Contains(req.Messages[0].Content, "verified_operator_chronology") {
		t.Fatal("a redacted projection was described as an exact original user quotation")
	}
}

func TestSummaryRequestIncludesInputInRemainingRunBudget(t *testing.T) {
	turn := domain.SupervisorTurn{Run: domain.Run{Budget: domain.Budget{MaxTokens: 5000}}, Checkpoint: domain.SupervisorCheckpoint{TotalTokens: 4500}}
	_, err := supervisorSummaryRequest(turn, contextmgr.SummaryGenerationRequest{TaskID: "session"},
		llm.ModelRef{Provider: "fixture", Model: "summary"}, llm.DefaultContextWindow(), true)
	if err == nil || !strings.Contains(err.Error(), "generation_token_budget") {
		t.Fatalf("auxiliary call must reserve input and continuation budget: %v", err)
	}
}

func TestSummaryRequestProjectInstructionDeliveryRefusesOverflowWithoutDroppingRulesOrHistory(t *testing.T) {
	root := t.TempDir()
	content := "SUMMARY_REQUIRED_BEGIN\n" + strings.Repeat("r", 12*1024) + "\nSUMMARY_REQUIRED_END"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := projectconfig.DiscoverInstructions(t.Context(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	turn := domain.SupervisorTurn{Mission: domain.Mission{Goal: "Preserve the original goal and required project constraints"}}
	turn.Run.Config.ProjectInstructions, _ = json.Marshal(legacy)
	turn.Run.Config.ProjectInstructionsFingerprint = legacy.Fingerprint
	input := contextmgr.SummaryGenerationRequest{TaskID: "session", SourceSHA256: strings.Repeat("a", 64),
		Messages: []contextmgr.Message{{Content: "HISTORICAL_SOURCE_BEGIN " + strings.Repeat("h", 2048) + " HISTORICAL_SOURCE_END"}}}
	ref := llm.ModelRef{Provider: "fixture", Model: "summary"}
	window := llm.ContextWindow{ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: 4096,
		SafetyMarginTokens: 128, DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "summary_required_test"}
	request, audit, err := supervisorSummaryRequestAndAudit(turn, input, ref, window, true, llm.ChatRequest{})
	if err != nil || len(request.Messages) != 2 || audit != nil {
		t.Fatalf("unclassified summary compatibility changed: err=%v messages=%d audit=%+v", err, len(request.Messages), audit)
	}
	pinned, err := projectconfig.ClassifyInstructionSnapshot(legacy, []projectconfig.InstructionSourceDelivery{{
		Path: legacy.Sources[0].Path, ContentSHA256: legacy.Sources[0].ContentSHA256, Requirement: projectconfig.InstructionMandatory,
	}})
	if err != nil {
		t.Fatal(err)
	}
	turn.Run.Config.ProjectInstructions, _ = json.Marshal(pinned)
	turn.Run.Config.ProjectInstructionsFingerprint = pinned.Fingerprint
	request, audit, err = supervisorSummaryRequestAndAudit(turn, input, ref, window, true, llm.ChatRequest{})
	if err == nil || !strings.Contains(err.Error(), "generation_input_window") || len(request.Messages) != 0 || audit != nil {
		t.Fatalf("overflow must return no dispatchable request or claimed delivery: err=%v messages=%d audit=%+v", err, len(request.Messages), audit)
	}
	request, audit, err = supervisorSummaryRequestAndAudit(turn, input, ref, llm.DefaultContextWindow(), true, llm.ChatRequest{})
	if err != nil || audit == nil || len(audit.Included) != 1 || audit.Included[0].SourceID != pinned.DeliverySourceID(0) ||
		len(request.Messages) != 3 || !strings.Contains(request.Messages[1].Content, input.Messages[0].Content) {
		t.Fatalf("fitting request lost original history or required-source audit: err=%v audit=%+v", err, audit)
	}
	var envelope projectInstructionDeliveryEnvelope
	if json.Unmarshal([]byte(request.Messages[2].Content), &envelope) != nil || request.Messages[2].Role != "user" ||
		envelope.Content != pinned.Sources[0].Content || envelope.Source.Snapshot != pinned.Fingerprint || envelope.Authority != pinned.Sources[0].Authority {
		t.Fatal("summary request changed the complete mandatory source, pin or workflow-only authority")
	}
}
