package application

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
)

func TestThreadTurnStopsAtPendingMCPApprovalAndResumesSameTurn(t *testing.T) {
	f := newMCPRecoveryAcceptanceRuntime(t, domain.RunExecutionPermissionAsk, 1, false)
	request := ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(f.run.ID), Content: "Look up the exact reviewed MCP input.",
		OperationKey: "thread-mcp-pending-operation-0001", RequestedBy: "operator"}
	result, err := f.turns.Execute(t.Context(), request)
	if err != nil || result.Execution == nil || result.Execution.Handoff.Result == nil {
		t.Fatalf("Thread did not return its approval boundary: result=%+v err=%v", result, err)
	}
	handoff := result.Execution.Handoff
	if handoff.Result.Status != domain.RunExecutionHandoffCompleted || handoff.Result.StopReason != "root_wait" ||
		result.Submission.Message.Status != domain.OperatorSteeringPending || result.Submission.Run.Status != domain.RunRunning {
		t.Fatalf("approval wait lost its resumable message or Run: %+v", result)
	}
	if f.requests.Load() != 0 || f.wireCalls.Load() != 0 {
		t.Fatal("Thread dispatched the MCP call before operator consent")
	}
	var found bool
	f.pending, found, err = f.st.GetSupervisorCheckpoint(t.Context(), f.run.ID)
	if err != nil || !found || f.pending.Phase != domain.SupervisorTurnStarted {
		t.Fatalf("missing original pending turn: %+v err=%v", f.pending, err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.pending)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
		t.Fatalf("missing exact durable MCP intent: %+v err=%v", rounds, err)
	}
	f.calls = rounds[0].Calls
	f.assertApproval(t, 0, approval.StatusPending)
	if retry, err := f.turns.Execute(t.Context(), request); err != nil || !retry.Replayed || retry.Submission.Message.ID != result.Submission.Message.ID {
		t.Fatalf("pending request retry restarted or lost the original message: %+v err=%v", retry, err)
	}
	second := threadTurnExecutionRequest(SubmitThreadMessageRequest{ThreadID: request.ThreadID, OperationKey: request.OperationKey,
		RequestedBy: request.RequestedBy}, f.run.ID, 2)
	if _, found, err := f.st.GetRunExecutionHandoff(t.Context(),
		runmutation.RunExecutionHandoffOperationDigest(f.run.ID, second.OperationKey)); err != nil || found {
		t.Fatalf("Thread created a second handoff while waiting for approval: found=%t err=%v", found, err)
	}
	f.provider.mu.Lock()
	modelRequests := f.provider.requests
	f.provider.mu.Unlock()
	if modelRequests != 1 {
		t.Fatalf("approval wait repeated the initial model request: %d", modelRequests)
	}
	f.decide(t, f.request(t, 0, ApprovalControlApproveOnce, "thread-mcp-consent-operation-0001"))
	if resumed := f.resume(t, 0); resumed.State != "completed" {
		t.Fatalf("exact approved Thread turn did not resume: %+v", resumed)
	}
	f.assertCall(t, 0, domain.SupervisorToolCompleted, "", true)
	f.assertTurnSettled(t, 1, 1)
	message, err := f.st.GetOperatorSteering(t.Context(), result.Submission.Message.ID)
	if err != nil || message.Status != domain.OperatorSteeringCommitted {
		t.Fatalf("approval continuation did not commit the original Thread input: %+v err=%v", message, err)
	}
	if replay, err := f.turns.Execute(t.Context(), request); err != nil || !replay.Replayed || replay.Submission.Message.ID != message.ID {
		t.Fatalf("settled Thread request did not replay: %+v err=%v", replay, err)
	}
	f.assertTurnSettled(t, 1, 1)
}

func TestThreadPendingMCPApprovalContinuesAcrossToolBoundary(t *testing.T) {
	f := newMCPRecoveryAcceptanceRuntime(t, domain.RunExecutionPermissionAsk, 1, false)
	request := ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(f.run.ID), Content: "Read the approved result and finish all five tool rounds.",
		OperationKey: "thread-mcp-approved-tool-boundary", RequestedBy: "operator"}
	f.provider.respond = func(modelRequest llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		response := &llm.ChatResponse{Provider: f.provider.Name(), Model: "fixture", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
		switch index {
		case 1:
			return nil, nil // The original MCP call must really stop for Ask approval.
		case 2, 3, 4, 6:
			if index == 6 {
				original, priorResults := false, false
				for _, message := range modelRequest.Messages {
					original = original || strings.Contains(message.Content, request.Content)
					priorResults = priorResults || strings.Contains(message.Content, "Completed tool segments of this exact accepted user input")
				}
				if len(modelRequest.Tools) == 0 || !original || !priorResults {
					return nil, fmt.Errorf("approval continuation lost its original input, tools or preceding results")
				}
			}
			response.ToolCalls = []llm.ToolCall{{ID: fmt.Sprintf("approved-boundary-note-%d", index), Name: "note_create",
				Arguments: json.RawMessage(fmt.Sprintf(`{"title":"Round result","category":"observation","content":"Recorded result in round %d"}`, index))}}
		case 5, 7:
			kind, message, reason := domain.RootActionContinue, "The fifth tool round still remains.", ""
			if index == 5 && (len(modelRequest.Tools) != 0 || !strings.Contains(modelRequest.Messages[len(modelRequest.Messages)-1].Content, "scheduling boundary")) {
				return nil, fmt.Errorf("expected the four-round scheduling boundary")
			}
			if index == 7 {
				kind, message = domain.RootActionWait, "All five tool rounds completed."
				reason = "Awaiting the next operator input"
			}
			raw, err := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: kind, Message: message, Reason: reason})
			response.Text = string(raw)
			return response, err
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
		return response, nil
	}
	first, err := f.turns.Execute(t.Context(), request)
	if err != nil || first.Execution == nil || first.Execution.Handoff.Result == nil || first.Execution.Handoff.Result.StopReason != "root_wait" {
		t.Fatalf("initial Thread did not stop at Ask: %+v %v", first, err)
	}
	var found bool
	f.pending, found, err = f.st.GetSupervisorCheckpoint(t.Context(), f.run.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.pending)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("initial round: %+v %v", rounds, err)
	}
	f.calls = rounds[0].Calls
	decision := f.request(t, 0, ApprovalControlApproveOnce, "thread-mcp-boundary-consent")
	f.decide(t, decision)
	if resumed := f.resume(t, 0); resumed.State != "completed" {
		checkpoint, _, checkpointErr := f.st.GetSupervisorCheckpoint(t.Context(), f.run.ID)
		t.Fatalf("resume: %+v model calls=%d checkpoint=%+v err=%v", resumed, f.provider.requests, checkpoint, checkpointErr)
	}
	if f.provider.requests != 7 || f.wireCalls.Load() != 1 {
		t.Fatalf("approval stopped before fifth round or repeated MCP: model=%d MCP=%d", f.provider.requests, f.wireCalls.Load())
	}
	message, err := f.st.GetOperatorSteering(t.Context(), first.Submission.Message.ID)
	if err != nil || message.Status != domain.OperatorSteeringCommitted {
		t.Fatalf("original input not committed: %+v %v", message, err)
	}
	messages, err := f.st.ListSessionMessages(t.Context(), f.run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	userMessages := 0
	for _, message := range messages {
		if message.Role == "user" && message.Content == request.Content {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("same input recorded %d times", userMessages)
	}
	f.decide(t, decision)
	_ = f.resume(t, 0)
	if replay, err := f.turns.Execute(t.Context(), request); err != nil || !replay.Replayed {
		t.Fatalf("Thread replay: %+v %v", replay, err)
	}
	if f.provider.requests != 7 || f.wireCalls.Load() != 1 {
		t.Fatal("replay repeated model or tool execution")
	}
}
