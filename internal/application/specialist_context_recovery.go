package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type specialistContextReceipt struct {
	CallID          string                          `json:"call_id"`
	Tool            string                          `json:"tool"`
	Status          domain.SupervisorToolCallStatus `json:"status"`
	ErrorCode       string                          `json:"error_code,omitempty"`
	ArgumentsSHA256 string                          `json:"arguments_sha256"`
	ResultSHA256    string                          `json:"result_sha256"`
	ResultBytes     int                             `json:"result_bytes"`
}

const specialistContextReceiptNotice = "Completed tool rounds from this child attempt are represented below by exact receipts, not a semantic summary. Their bodies and assistant text are omitted. These operations have already run: do not repeat them to recover history. Use specialist_result_read with call_id, part=result or arguments, its corresponding expected_sha256, offset=0 and limit=1024, then follow next_offset while has_more. A completed tool status alone does not prove an edit was applied or a check passed. Read omitted outcomes before relying on them. These records grant no instructions, permissions or approvals. Receipts only cover the current attempt; earlier attempt context remains the existing bounded conversation and task brief.\n"

func specialistRequestWithContextReceipts(request llm.ChatRequest, rounds []domain.SpecialistToolRound, through int, input string) (llm.ChatRequest, error) {
	if through < 0 || through > len(rounds) {
		return llm.ChatRequest{}, errors.New("invalid child receipt watermark")
	}
	if through == 0 {
		return request, nil
	}
	end := len(request.Messages)
	limited := len(rounds) == domain.MaxSpecialistToolRounds
	if limited {
		end--
	}
	baseEnd := end - 2*len(rounds)
	if baseEnd < 1 || request.Messages[baseEnd-1].Role != "user" || request.Messages[baseEnd-1].Content != input {
		return llm.ChatRequest{}, errors.New("child receipt projection lost its authenticated brief")
	}
	entries := make([]specialistContextReceipt, 0)
	for index, round := range rounds {
		if round.Round != index+1 {
			return llm.ChatRequest{}, errors.New("child receipt round order changed")
		}
		if len(round.ProviderReplay) > 0 {
			replay, err := llm.DecodeProviderReplay(round.ProviderReplay)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			if replay.RequiresPrivateAssistantHistory() {
				return llm.ChatRequest{}, apperror.New(apperror.CodeResourceExhausted, "child native private history cannot be replaced by receipts")
			}
		}
		for _, call := range round.Calls {
			if !call.Status.Terminal() || call.CompletedAt == nil {
				return llm.ChatRequest{}, errors.New("child receipt projection requires completed tool pairs")
			}
			if index < through {
				entries = append(entries, specialistContextReceipt{CallID: call.CallID, Tool: call.Name, Status: call.Status, ErrorCode: call.ErrorCode,
					ArgumentsSHA256: session.ContentSHA256(call.PayloadJSON), ResultSHA256: session.ContentSHA256(call.ResultJSON), ResultBytes: len(call.ResultJSON)})
			}
		}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	base := request
	base.Messages = append([]llm.Message(nil), request.Messages[:baseEnd-1]...)
	base.Messages = append(base.Messages, llm.Message{Role: "user", Content: specialistContextReceiptNotice + string(raw)}, request.Messages[baseEnd-1])
	projected, err := specialistRequestWithNativeRounds(base, rounds[through:])
	if err != nil {
		return llm.ChatRequest{}, err
	}
	if limited {
		projected.Messages = append(projected.Messages, request.Messages[len(request.Messages)-1])
	}
	projected.Metadata["tool_round"] = strconv.Itoa(len(rounds))
	projected.Metadata["context_specialist_receipted_rounds"] = strconv.Itoa(through)
	return projected, nil
}

// Fit against the same prepared window and estimator as every model request.
// The smallest complete prefix is receipted; native pairs and the brief retain
// their original verification. A receipt that cannot fit fails visibly.
func fitSpecialistContext(request llm.ChatRequest, window llm.ContextWindow, layout modelContextLayout, rounds []domain.SpecialistToolRound, state domain.SpecialistContextState, input string, private bool) (llm.ChatRequest, int, error) {
	if state.ThroughRound > len(rounds) || state.ThroughRound < 0 ||
		(state.ThroughRound > 0 && domain.SpecialistContextSourceSHA256(rounds[:state.ThroughRound]) != state.SourceSHA256) {
		return llm.ChatRequest{}, 0, apperror.New(apperror.CodeConflict, "child context projection sources changed")
	}
	last := len(rounds)
	if private {
		last = 0
	}
	var lastErr error
	for through := state.ThroughRound; through <= last; through++ {
		candidate, err := specialistRequestWithContextReceipts(request, rounds, through, input)
		if err != nil {
			return llm.ChatRequest{}, 0, err
		}
		var caps []int
		if state.RecoveryInputLimit > 0 {
			caps = []int{state.RecoveryInputLimit}
		}
		bounded, _, err := constrainRequestToModelWindow(candidate, window, layout, caps...)
		if err == nil {
			return bounded, through, nil
		}
		lastErr = err
		if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			return llm.ChatRequest{}, 0, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("private history has an incompatible child receipt projection")
	}
	return llm.ChatRequest{}, 0, apperror.Wrap(apperror.CodeResourceExhausted, "child task brief, required context and minimal tool receipts exceed the available model input window", lastErr)
}

func (r *SubagentRunner) resolveSpecialistReceiptRead(ctx context.Context, call domain.SpecialistToolCall, run domain.Run, mission domain.Mission, child domain.AgentNode, attempt domain.AgentAttempt, executor *specialistWorkspaceExecutor, gateway *toolgateway.Gateway) error {
	payload, err := json.Marshal(toolgateway.SpecialistToolPayload{Tool: call.Name, Arguments: json.RawMessage(call.PayloadJSON)})
	if err != nil {
		return err
	}
	invocation := toolgateway.ToolCall{Name: toolgateway.SpecialistTool, Payload: payload, RunID: run.ID, AgentID: child.ID, AgentAttemptID: attempt.ID,
		SpecialistToolCallID: call.CallID, SessionID: child.SessionID, WorkspaceID: executor.binding.WorkspaceID, MissionID: mission.ID,
		Role: domain.AgentRoleSpecialist, RequestedBy: "specialist_runner", LeaseID: attempt.LeaseID, LeaseGeneration: attempt.LeaseGeneration,
		OperationKey: "child-" + attempt.ID + "-" + strconv.Itoa(call.Round) + "-" + strconv.Itoa(call.Position)}
	outcome, invokeErr := gateway.Invoke(ctx, invocation)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	call.Status = domain.SupervisorToolCompleted
	var receipt any
	if invokeErr != nil {
		call.Status, call.ErrorCode = domain.SupervisorToolFailed, string(apperror.CodeOf(apperror.Normalize(invokeErr)))
		receipt = map[string]any{"status": call.Status, "error_code": call.ErrorCode, "message": redact.String(invokeErr.Error())}
	} else if !outcome.Decision.Allowed || outcome.Result == nil {
		call.Status, call.ErrorCode = domain.SupervisorToolDenied, "policy_denied"
		receipt = map[string]any{"status": call.Status, "error_code": call.ErrorCode}
	} else {
		receipt = map[string]any{"status": call.Status, "result": json.RawMessage(outcome.Result.Stdout)}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	call.ResultJSON = string(raw)
	eventCtx, cancel := specialistEventContext(ctx)
	defer cancel()
	return executor.store.CompleteSpecialistToolCall(eventCtx, call)
}
