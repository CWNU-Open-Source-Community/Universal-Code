package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

func assertFreshInputDelivery(t *testing.T, request llm.ChatRequest, original string) {
	t.Helper()
	if delivery := request.Metadata["input_delivery"]; delivery != "" {
		t.Fatalf("fresh submission has input_delivery=%q", delivery)
	}
	for i := len(request.Messages) - 1; i >= 0; i-- {
		message := request.Messages[i]
		if message.Role != "user" || len(message.ToolResults) != 0 {
			continue
		}
		if message.Content == original {
			return
		}
		if strings.HasPrefix(message.Content, "Harness input delivery:") {
			t.Fatal("fresh submission was wrapped as an internal continuation")
		}
	}
	t.Fatal("fresh request is missing the original user message")
}

func assertInternalInputDelivery(t *testing.T, request llm.ChatRequest, original string) {
	t.Helper()
	if request.Metadata["input_delivery"] != "tool_boundary_continuation" {
		t.Errorf("prepared continuation is advertised as a fresh request: input_delivery=%q", request.Metadata["input_delivery"])
	}
	for _, message := range request.Messages {
		if message.Role != "user" || !strings.HasPrefix(message.Content, "Harness input delivery:") {
			continue
		}
		start := strings.Index(message.Content, "{\"version\":\"supervisor_input_delivery.v1\"")
		var envelope struct {
			Version       string `json:"version"`
			Delivery      string `json:"delivery"`
			AcceptedInput string `json:"accepted_input"`
		}
		if start < 0 || json.NewDecoder(strings.NewReader(message.Content[start:])).Decode(&envelope) != nil ||
			envelope.Version != "supervisor_input_delivery.v1" || envelope.Delivery != "tool_boundary_continuation" ||
			envelope.AcceptedInput != original {
			t.Fatalf("continuation did not preserve the complete accepted input: %q", message.Content)
		}
		return
	}
	t.Errorf("continuation replayed the bootstrap as an unlabeled current user request: %q", original)
}

func TestToolBoundaryInputDeliveryDistinguishesSameTextNewSubmission(t *testing.T) {
	st, _, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	input.Content = "Navigate in a new browser session, then add a row and check it. Preserve all original constraints."
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index <= domain.MaxSupervisorToolRounds:
			assertFreshInputDelivery(t, request, input.Content)
			return boundaryRead(fmt.Sprintf("initial-read-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Only the remaining check needs doing", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			assertInternalInputDelivery(t, request, input.Content)
			boundaryContextText(t, request)
			return boundaryRead("remaining-check", 1), nil
		case index == domain.MaxSupervisorToolRounds+3:
			assertInternalInputDelivery(t, request, input.Content)
			return textResponse(rootActionResponse(domain.RootActionFinish, "Check complete", "done", "")), nil
		case index == domain.MaxSupervisorToolRounds+4:
			assertFreshInputDelivery(t, request, input.Content)
			return textResponse(rootActionResponse(domain.RootActionFinish, "New request answered", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model request %d", index)
		}
	}
	service := toolBoundaryService(st, st, provider)
	if _, err := service.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
	input.OperationKey = "same-text-new-submission"
	if _, err := service.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != domain.MaxSupervisorToolRounds+4 {
		t.Fatal("input labeling changed scheduling or repeated a provider request")
	}
}

func TestToolBoundaryInputDeliverySurvivesSteeringAndContextRebuild(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	for index := 0; index < 6; index++ {
		role := "user"
		if index%2 != 0 {
			role = "assistant"
		}
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, role,
			fmt.Sprintf("Previous observation %d: %s", index, strings.Repeat("Older bounded evidence remains recallable. ", 90)))); err != nil {
			t.Fatal(err)
		}
	}
	correction := "Keep the live page; the row is already added. Only collapse and inspect."
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= domain.MaxSupervisorToolRounds {
			return boundaryRead(fmt.Sprintf("prepared-read-%d", index), 1), nil
		}
		if index == domain.MaxSupervisorToolRounds+1 {
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Final check remains", "", "")), nil
		}
		assertInternalInputDelivery(t, request, input.Content)
		switch index {
		case domain.MaxSupervisorToolRounds + 2:
			if _, err := application.NewSessionMessageSubmissionService(st).Submit(ctx, application.SubmitSessionMessageRequest{
				Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: run.SessionID,
				Content: correction, OperationKey: "input-delivery-steering", RequestedBy: "test_operator",
				DeliveryMode: domain.OperatorSteeringCurrentTurn,
			}); err != nil {
				t.Fatal(err)
			}
			return boundaryRead("steering-read", 1), nil
		case domain.MaxSupervisorToolRounds + 3:
			if request.Metadata["pending_user_instructions"] != "1" {
				t.Fatal("rebuild lost the later accepted user correction")
			}
			// A genuine provider context-limit response rebuilds the protected
			// input without replaying the preceding completed tool.
			return nil, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit,
				Provider: provider.Name(), Message: "bounded context limit"}
		case domain.MaxSupervisorToolRounds + 4:
			if request.Metadata["pending_user_instructions"] != "1" || request.Metadata["context_summary_id"] == "" {
				t.Fatal("context recovery did not compact actual history or lost the accepted correction")
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Remaining check complete", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model request %d", index)
		}
	}
	if _, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if len(requests) != domain.MaxSupervisorToolRounds+4 {
		t.Fatalf("requests=%d", len(requests))
	}
	for _, request := range requests[domain.MaxSupervisorToolRounds+2:] {
		found := false
		for _, message := range request.Messages {
			if strings.HasPrefix(message.Content, "Harness input delivery:") {
				var envelope map[string]string
				start := strings.Index(message.Content, "{\"version\":\"supervisor_input_delivery.v1\"")
				if start >= 0 && json.NewDecoder(strings.NewReader(message.Content[start:])).Decode(&envelope) == nil {
					end := start + strings.Index(message.Content[start:], "}\n")
					found = end > start && !strings.Contains(envelope["accepted_input"], correction) && strings.Contains(message.Content[end:], correction)
				}
			}
		}
		if !found {
			t.Fatal("the protected continuation input lost the exact correction")
		}
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
}
