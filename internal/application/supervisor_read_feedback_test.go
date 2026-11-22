package application

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func TestWorkspaceReadFeedbackUsesObservedLinesNotCallCounts(t *testing.T) {
	initial := recordedWorkspacePages(t)[2]
	alter := func(from, to int, changes map[string]any) domain.SupervisorToolCall {
		copy := initial
		copy.CallID += "-later"
		var e supervisorToolResultEnvelope
		_ = json.Unmarshal([]byte(copy.ResultJSON), &e)
		var page map[string]any
		_ = json.Unmarshal([]byte(e.Stdout), &page)
		lines := strings.Split(page["content"].(string), "\n")
		page["content"] = strings.Join(lines[from-1:to], "\n")
		page["start_line"], page["end_line"] = from, to
		for key, value := range changes {
			page[key] = value
			if text, ok := value.(string); ok {
				e.Metadata[key] = text
			}
		}
		body, _ := json.Marshal(page)
		e.Stdout = string(body)
		raw, err := marshalSupervisorToolResultEnvelope(e)
		if err != nil {
			t.Fatal(err)
		}
		copy.ResultJSON = string(raw)
		return copy
	}
	for _, tc := range []struct {
		name     string
		call     domain.SupervisorToolCall
		repeated int
	}{
		{"exact_read", alter(1, 60, nil), 1},
		{"subset_read", alter(1, 10, nil), 1},
		{"nested_read", alter(20, 40, nil), 1},
		{"changed_file", alter(1, 10, map[string]any{"content_sha256": strings.Repeat("d", 64)}), 0},
		{"other_workspace", alter(1, 10, map[string]any{"workspace_id": "other"}), 0},
		{"other_root", alter(1, 10, map[string]any{"root_fingerprint": strings.Repeat("e", 64)}), 0},
		{"different_body", alter(1, 1, map[string]any{"content": "different observed line"}), 0},
		{"redacted_body", alter(1, 10, map[string]any{"redaction_count": 1}), 0},
		{"incomplete_page", alter(1, 10, map[string]any{"content": "one line only"}), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.call.ResultJSON
			rounds := []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{tc.call}}}
			if got := repeatedWorkspaceReadCount([]domain.SupervisorToolCall{initial}, rounds); got != tc.repeated {
				t.Fatalf("repeats=%d want=%d", got, tc.repeated)
			}
			if tc.call.ResultJSON != before {
				t.Fatal("feedback mutated the sealed receipt")
			}
		})
	}
	// A genuine newly seen part is progress even with the same file hash.
	first, next := alter(1, 10, nil), alter(8, 20, nil)
	if repeatedWorkspaceReadCount([]domain.SupervisorToolCall{first}, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{next}}}) != 0 {
		t.Fatal("new visible lines were treated as a duplicate")
	}
	for _, status := range []domain.SupervisorToolCallStatus{domain.SupervisorToolPending, domain.SupervisorToolFailed, domain.SupervisorToolDenied, domain.SupervisorToolCallStatus("not_dispatched")} {
		invalid := initial
		invalid.Status = status
		if repeatedWorkspaceReadCount([]domain.SupervisorToolCall{initial}, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{invalid}}}) != 0 {
			t.Fatalf("%s counted as an observation", status)
		}
	}
}

func TestWorkspaceReadFeedbackPreservesNativePairsAndBoundary(t *testing.T) {
	calls := recordedWorkspacePages(t)
	rounds := []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{calls[1], calls[1]}}}
	request := llm.ChatRequest{
		Tools: []llm.ToolSpec{{Name: "workspace_read"}},
		Messages: []llm.Message{
			{Role: "assistant", Replay: new(llm.ProviderReplay), ToolCalls: []llm.ToolCall{{ID: "one", Name: "workspace_read"}}},
			{Role: "user", ToolResults: []llm.ToolResult{{ToolCallID: "one", Content: "public test result"}}},
		},
	}
	s := &AgentRunner{}
	feedback, err := s.workspaceReadFeedback(t.Context(), domain.SupervisorCheckpoint{}, true, rounds)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{llm.HarnessTransportAnthropicMessages, llm.HarnessTransportOpenAIChatCompletions,
		llm.HarnessTransportOpenAIResponses, llm.HarnessTransportOllamaChat, llm.HarnessTransportProviderContract} {
		t.Run(transport, func(t *testing.T) {
			request.Metadata = map[string]string{"harness_transport": transport}
			got := supervisorRuntimeFacts(request, llm.DefaultContextWindow(), 0, 0, nil, feedback)
			wantCount, wantRole := 3, "system"
			if transport == llm.HarnessTransportAnthropicMessages {
				wantCount, wantRole = 2, "user"
			}
			if len(got.Messages) != wantCount || got.Messages[wantCount-1].Role != wantRole ||
				!strings.Contains(got.Messages[wantCount-1].Content, "1 workspace_read") {
				t.Fatal("read feedback changed the tool conversation boundary")
			}
			if !reflect.DeepEqual(got.Tools, request.Tools) || !reflect.DeepEqual(got.Messages[0], request.Messages[0]) ||
				got.Messages[0].Replay != request.Messages[0].Replay || !reflect.DeepEqual(got.Messages[1].ToolResults, request.Messages[1].ToolResults) ||
				request.Messages[1].Content != "" {
				t.Fatal("advice changed native replay, tool result bytes or tools")
			}
		})
	}
	request.Tools = nil
	feedback, err = s.workspaceReadFeedback(t.Context(), domain.SupervisorCheckpoint{}, false, rounds)
	got := supervisorRuntimeFacts(request, llm.DefaultContextWindow(), 0, 0, nil, feedback)
	if err != nil || !reflect.DeepEqual(request, got) {
		t.Fatal("tool-free boundary was given conflicting tool advice")
	}
}

func TestSupervisorProjectsOnlyChangedStandardCodeStateAfterTools(t *testing.T) {
	machine := &standardCodeSupervisorTurn{snapshot: domain.StandardCodeSupervisorSnapshot{State: domain.StandardCodeSupervisorInspect, Version: 1}}
	machine.initialStateRole = supervisorHostStateRole(llm.HarnessTransportAnthropicMessages)
	machine.initialStateText = machine.runtimeState()
	messages := supervisorInitialStandardCodeState([]llm.Message{
		{Role: "system", Content: machine.Guidance()}, {Role: "user", Content: "Read the example."},
	}, machine)
	if len(messages) != 3 || !strings.Contains(messages[2].Content, machine.initialStateText) || messages[2].Role != "user" || messages[1].Content != "Read the example." {
		t.Fatal("the starting state is not bound to the original input")
	}
	request := llm.ChatRequest{Metadata: map[string]string{"harness_transport": llm.HarnessTransportAnthropicMessages},
		Messages: append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Name: "workspace_read"}}},
			llm.Message{Role: "user", ToolResults: []llm.ToolResult{{ToolCallID: "one", Content: "first line"}}})}
	if got := supervisorRuntimeFacts(request, llm.DefaultContextWindow(), 0, 1, machine, ""); !reflect.DeepEqual(got, request) {
		t.Fatal("an unchanged Standard Code state added a request-local item")
	}
	machine.snapshot.Version = 2
	machine.snapshot.CommandsUsed++
	got := supervisorRuntimeFacts(request, llm.DefaultContextWindow(), 0, 1, machine, "")
	last := len(got.Messages) - 1
	if machine.Guidance() != messages[0].Content || !reflect.DeepEqual(got.Messages[:last], request.Messages[:last]) ||
		!strings.Contains(got.Messages[last].Content, machine.runtimeState()) || !reflect.DeepEqual(got.Messages[last].ToolResults, request.Messages[last].ToolResults) {
		t.Fatal("the changed state replaced instructions, changed a result, or failed to reach the current step")
	}
}
