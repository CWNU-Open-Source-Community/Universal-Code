package application

import (
	"slices"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
)

const modelMessageFramingTokens = 4

type modelContextLayout struct {
	HistoryStart int
	HistoryCount int
}

func (l modelContextLayout) shifted(offset int) modelContextLayout {
	l.HistoryStart += offset
	return l
}

type modelContextPlan struct {
	WindowTokens        int
	InputLimitTokens    int
	EstimatedInput      int
	OutputLimitTokens   int
	OutputReserveTokens int
	HistoryOmitted      int
	SafetyMarginTokens  int
}

func constrainRequestToModelWindow(request llm.ChatRequest, window llm.ContextWindow,
	layout modelContextLayout, inputCaps ...int,
) (llm.ChatRequest, modelContextPlan, error) {
	if err := window.Validate(); err != nil {
		return llm.ChatRequest{}, modelContextPlan{}, apperror.Wrap(
			apperror.CodeFailedPrecondition, "model context-window policy is invalid", err)
	}
	if layout.HistoryStart < 0 || layout.HistoryCount < 0 ||
		layout.HistoryStart > len(request.Messages) ||
		layout.HistoryCount > len(request.Messages)-layout.HistoryStart {
		return llm.ChatRequest{}, modelContextPlan{}, apperror.New(
			apperror.CodeFailedPrecondition, "model context history layout is invalid")
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	request.Tools = append([]llm.ToolSpec(nil), request.Tools...)
	// Planning always reserves output space. Optional wire limits stay omitted
	// unless the caller explicitly bounded the request (including cost budgets).
	outputReserve := request.PlannedOutputTokens(window)
	if request.MaxTokens > 0 || !request.AllowsDefaultOutput() {
		request.MaxTokens = outputReserve
	}
	inputLimit, err := window.InputLimit(outputReserve)
	if err != nil {
		return llm.ChatRequest{}, modelContextPlan{}, apperror.Wrap(
			apperror.CodeFailedPrecondition, "model context input limit is invalid", err)
	}
	if len(inputCaps) > 0 {
		if inputCaps[0] < 0 {
			return llm.ChatRequest{}, modelContextPlan{}, apperror.New(apperror.CodeFailedPrecondition, "model input capacity is negative")
		}
		if inputCaps[0] < inputLimit {
			inputLimit = inputCaps[0]
		}
	}
	omitted := 0
	estimated := estimateModelRequestTokens(request)
	for _, message := range request.Messages {
		if message.Replay.RequiresPrivateAssistantHistory() && estimated > inputLimit {
			return llm.ChatRequest{}, modelContextPlan{}, apperror.New(apperror.CodeResourceExhausted,
				"native private assistant history exceeds the conservative input window; history truncation is unsupported")
		}
	}
	// Long sequences of small messages can legitimately remain active. Size
	// the discarded prefix once instead of rescanning the suffix per message.
	for estimated > inputLimit && omitted < layout.HistoryCount {
		estimated -= estimateModelMessageTokens(request.Messages[layout.HistoryStart+omitted])
		omitted++
	}
	if omitted > 0 {
		request.Messages = slices.Delete(request.Messages, layout.HistoryStart, layout.HistoryStart+omitted)
		estimated = estimateModelRequestTokens(request)
	}
	plan := modelContextPlan{
		WindowTokens: window.WindowTokens, InputLimitTokens: inputLimit,
		EstimatedInput: estimated, OutputLimitTokens: request.MaxTokens,
		OutputReserveTokens: outputReserve,
		HistoryOmitted:      omitted, SafetyMarginTokens: window.SafetyMarginTokens,
	}
	// Return the omission plan even when mandatory input also overflows.
	// Root callers must persist eligible history before rebuilding the request;
	// they must never send this temporary lossy slice to the provider.
	if estimated > inputLimit {
		return llm.ChatRequest{}, plan, apperror.New(apperror.CodeResourceExhausted,
			"mandatory model context exceeds the conservative input window")
	}
	metadata := make(map[string]string, len(request.Metadata)+8)
	for key, value := range request.Metadata {
		metadata[key] = value
	}
	metadata["context_protocol"] = window.ProtocolVersion
	metadata["context_window_source"] = redact.String(window.Source)
	metadata["context_window_tokens"] = strconv.Itoa(plan.WindowTokens)
	metadata["context_input_limit"] = strconv.Itoa(plan.InputLimitTokens)
	metadata["context_input_estimate"] = strconv.Itoa(plan.EstimatedInput)
	metadata["context_output_limit"] = strconv.Itoa(plan.OutputLimitTokens)
	metadata["context_output_reserve"] = strconv.Itoa(plan.OutputReserveTokens)
	metadata["context_output_mode"] = "explicit"
	if request.MaxTokens == 0 {
		metadata["context_output_mode"] = "provider_default"
	}
	metadata["context_safety_margin"] = strconv.Itoa(plan.SafetyMarginTokens)
	metadata["context_history_omitted"] = strconv.Itoa(plan.HistoryOmitted)
	request.Metadata = metadata
	return request, plan, nil
}

func estimateModelRequestTokens(request llm.ChatRequest) int {
	total := 8
	for _, message := range request.Messages {
		total = addModelTokens(total, estimateModelMessageTokens(message))
	}
	for _, tool := range request.Tools {
		total = addModelTokens(total, modelMessageFramingTokens)
		total = addModelTokens(total, contextmgr.EstimateTokens(tool.Name))
		total = addModelTokens(total, contextmgr.EstimateTokens(tool.Description))
		total = addModelTokens(total, contextmgr.EstimateTokens(string(tool.Parameters)))
	}
	if request.JSONMode {
		total = addModelTokens(total, 8)
	}
	return total
}

func estimateModelMessageTokens(message llm.Message) int {
	total := contextmgr.EstimateMessageTokens(message.Role, message.Content)
	// Opaque continuation state is still part of the provider input. Count
	// its bytes conservatively without exposing it to public context logs.
	if message.Replay != nil {
		total = addModelTokens(total, message.Replay.ContextBytes())
	}
	for _, image := range message.Images {
		total = addModelTokens(total, llm.EstimateImageTokens(image))
	}
	for _, call := range message.ToolCalls {
		total = addModelTokens(total, contextmgr.EstimateTokens(call.ID))
		total = addModelTokens(total, contextmgr.EstimateTokens(call.Name))
		total = addModelTokens(total, contextmgr.EstimateTokens(string(call.Arguments)))
	}
	for _, result := range message.ToolResults {
		total = addModelTokens(total, contextmgr.EstimateTokens(result.ToolCallID))
		total = addModelTokens(total, contextmgr.EstimateTokens(result.Content))
	}
	return total
}

func addModelTokens(current int, addition int) int {
	if addition <= 0 {
		return current
	}
	maxInt := int(^uint(0) >> 1)
	if current > maxInt-addition {
		return maxInt
	}
	return current + addition
}
