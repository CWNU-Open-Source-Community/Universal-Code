package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const ModelHarnessProtocolVersion = "model_harness.v1"

const (
	HarnessTransportMock                  = "mock"
	HarnessTransportAnthropicMessages     = "anthropic_messages"
	HarnessTransportOpenAIChatCompletions = "openai_chat_completions"
	HarnessTransportOpenAIResponses       = "openai_responses"
	HarnessTransportOllamaChat            = "ollama_chat"
	HarnessTransportProviderContract      = "provider_contract"
)

const (
	HarnessToolStrategyNative = "native"
	HarnessToolStrategyNone   = "none"
)

const (
	HarnessJSONStrategyNative = "native"
	HarnessJSONStrategyPrompt = "prompt"
	HarnessJSONStrategyNone   = "none"
)

const (
	HarnessQualificationTrusted  = "trusted_builtin"
	HarnessQualificationRequired = "qualification_required"
	HarnessQualificationVerified = "verified"
)

type HarnessWorkload string

const (
	HarnessWorkloadRoot            HarnessWorkload = "root"
	HarnessWorkloadSpecialist      HarnessWorkload = "specialist"
	HarnessWorkloadSpecialistTools HarnessWorkload = "specialist_tools"
	HarnessWorkloadFanout          HarnessWorkload = "readonly_fanout"
)

// ModelHarness describes how one exact model is allowed to consume the Go-owned
// agent protocol. Transport compatibility alone does not imply Harness
// compatibility.
type ModelHarness struct {
	ProtocolVersion      string
	TransportProtocol    string
	ToolStrategy         string
	JSONStrategy         string
	QualificationStatus  string
	ToolCallsQualified   bool
	ToolResultsQualified bool
	StrictJSONQualified  bool
	StreamingQualified   bool
	BindingDigest        string
	QualifiedAt          time.Time
	QualificationExpires time.Time
}

type HarnessQualification struct {
	ProtocolVersion      string
	BindingDigest        string
	ToolCallsQualified   bool
	ToolResultsQualified bool
	StrictJSONQualified  bool
	StreamingQualified   bool
	QualifiedAt          time.Time
	ExpiresAt            time.Time
}

// ModelHarnessDescriber is optional so existing in-process Provider extensions
// retain source compatibility. Production providers should implement it.
type ModelHarnessDescriber interface {
	DescribeModelHarness(model string) ModelHarness
}

func (h ModelHarness) Validate() error {
	if h.ProtocolVersion != ModelHarnessProtocolVersion {
		return errors.New("model Harness protocol version is invalid")
	}
	switch h.TransportProtocol {
	case HarnessTransportMock, HarnessTransportAnthropicMessages,
		HarnessTransportOpenAIChatCompletions,
		HarnessTransportOpenAIResponses,
		HarnessTransportOllamaChat,
		HarnessTransportProviderContract:
	default:
		return errors.New("model Harness transport protocol is invalid")
	}
	switch h.ToolStrategy {
	case HarnessToolStrategyNative, HarnessToolStrategyNone:
	default:
		return errors.New("model Harness tool strategy is invalid")
	}
	switch h.JSONStrategy {
	case HarnessJSONStrategyNative, HarnessJSONStrategyPrompt, HarnessJSONStrategyNone:
	default:
		return errors.New("model Harness JSON strategy is invalid")
	}
	switch h.QualificationStatus {
	case HarnessQualificationTrusted, HarnessQualificationRequired,
		HarnessQualificationVerified:
	default:
		return errors.New("model Harness qualification status is invalid")
	}
	if len(h.BindingDigest) != sha256.Size*2 {
		return errors.New("model Harness binding digest is invalid")
	}
	if h.QualificationStatus == HarnessQualificationVerified {
		if h.QualifiedAt.IsZero() || h.QualificationExpires.IsZero() ||
			!h.QualificationExpires.After(h.QualifiedAt) {
			return errors.New("verified model Harness timestamps are invalid")
		}
	}
	if h.ToolStrategy == HarnessToolStrategyNone &&
		(h.ToolCallsQualified || h.ToolResultsQualified || h.StreamingQualified) {
		return errors.New("tool-disabled model Harness cannot qualify tool behavior")
	}
	if h.JSONStrategy == HarnessJSONStrategyNone && h.StrictJSONQualified {
		return errors.New("JSON-disabled model Harness cannot qualify strict JSON")
	}
	return nil
}

func (q HarnessQualification) Validate(bindingDigest string, now time.Time) error {
	if q.ProtocolVersion != ModelHarnessProtocolVersion ||
		q.BindingDigest != bindingDigest || len(q.BindingDigest) != sha256.Size*2 {
		return errors.New("model Harness qualification binding is invalid")
	}
	if q.QualifiedAt.IsZero() || q.ExpiresAt.IsZero() ||
		!q.ExpiresAt.After(q.QualifiedAt) || !q.ExpiresAt.After(now) {
		return errors.New("model Harness qualification is expired or invalid")
	}
	if !q.ToolCallsQualified || !q.ToolResultsQualified ||
		!q.StrictJSONQualified || !q.StreamingQualified {
		return errors.New("model Harness qualification is incomplete")
	}
	return nil
}

func harnessBindingDigest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(strings.TrimSpace(part)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func providerContractHarness(provider Provider, model string) ModelHarness {
	toolStrategy := HarnessToolStrategyNative
	jsonStrategy := HarnessJSONStrategyPrompt
	if provider.SupportsJSONMode(model) {
		jsonStrategy = HarnessJSONStrategyNative
	}
	return ModelHarness{
		ProtocolVersion:   ModelHarnessProtocolVersion,
		TransportProtocol: HarnessTransportProviderContract,
		ToolStrategy:      toolStrategy, JSONStrategy: jsonStrategy,
		QualificationStatus: HarnessQualificationTrusted,
		ToolCallsQualified:  true, ToolResultsQualified: true,
		StrictJSONQualified: true, StreamingQualified: true,
		BindingDigest: harnessBindingDigest(provider.Name(), model,
			HarnessTransportProviderContract, toolStrategy, jsonStrategy),
	}
}

func applyHarnessQualification(base ModelHarness, qualification HarnessQualification,
	now time.Time,
) ModelHarness {
	if qualification.Validate(base.BindingDigest, now) != nil {
		return base
	}
	base.QualificationStatus = HarnessQualificationVerified
	base.ToolCallsQualified = qualification.ToolCallsQualified
	base.ToolResultsQualified = qualification.ToolResultsQualified
	base.StrictJSONQualified = qualification.StrictJSONQualified
	base.StreamingQualified = qualification.StreamingQualified
	base.QualifiedAt = qualification.QualifiedAt
	base.QualificationExpires = qualification.ExpiresAt
	return base
}

func prepareHarnessRequest(profile ModelHarness, workload HarnessWorkload,
	request ChatRequest,
) (ChatRequest, error) {
	if err := profile.Validate(); err != nil {
		return ChatRequest{}, err
	}
	switch workload {
	case HarnessWorkloadRoot, HarnessWorkloadSpecialist, HarnessWorkloadSpecialistTools, HarnessWorkloadFanout:
	default:
		return ChatRequest{}, errors.New("model Harness workload is invalid")
	}
	if workload != HarnessWorkloadRoot && workload != HarnessWorkloadSpecialistTools {
		request.Tools = nil
	}
	if len(request.Tools) > 0 {
		if profile.ToolStrategy != HarnessToolStrategyNative ||
			!profile.ToolCallsQualified || !profile.ToolResultsQualified ||
			!profile.StreamingQualified {
			return ChatRequest{}, fmt.Errorf(
				"model Harness is not qualified for streamed tool calling (%s)",
				profile.QualificationStatus)
		}
	}
	if !profile.StrictJSONQualified {
		return ChatRequest{}, fmt.Errorf(
			"model Harness is not qualified for strict JSON (%s)",
			profile.QualificationStatus)
	}
	switch profile.JSONStrategy {
	case HarnessJSONStrategyNative:
		request.JSONMode = true
	case HarnessJSONStrategyPrompt:
		request.JSONMode = false
	default:
		return ChatRequest{}, errors.New("model Harness does not support strict JSON")
	}
	if request.Metadata == nil {
		request.Metadata = make(map[string]string)
	}
	request.Metadata["harness_protocol"] = profile.ProtocolVersion
	request.Metadata["harness_transport"] = profile.TransportProtocol
	request.Metadata["harness_tool_strategy"] = profile.ToolStrategy
	request.Metadata["harness_json_strategy"] = profile.JSONStrategy
	request.Metadata["harness_workload"] = string(workload)
	return request, nil
}
