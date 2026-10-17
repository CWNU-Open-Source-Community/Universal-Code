package contextmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	GeneratedHandoffVersion  = "generated_handoff.v1"
	MaxGeneratedSummaryChars = 1600
)

// ErrSummaryGenerationAborted prevents extractive fallback after Stop, lease
// loss, source drift or an unresolved potentially billed call. Implementations
// may wrap it; ordinary generation errors may fall back without claiming success.
var ErrSummaryGenerationAborted = errors.New("summary generation aborted")

type SummaryGenerationRequest struct {
	TaskID           string    `json:"task_id"`
	WorkspaceID      string    `json:"workspace_id"`
	SourceSHA256     string    `json:"source_sha256"`
	InputFingerprint string    `json:"input_fingerprint"`
	Messages         []Message `json:"messages"`
	Previous         Summary   `json:"previous"`
	HasPrevious      bool      `json:"has_previous"`
	MaxOutputChars   int       `json:"max_output_chars"`
}

type SummaryGenerationReceipt struct {
	RunID              string `json:"run_id"`
	AttemptID          string `json:"attempt_id"`
	ModelAttempt       int    `json:"model_attempt"`
	CompletionSequence int64  `json:"completion_sequence"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	SourceSHA256       string `json:"source_sha256"`
}

type SummaryGenerationResponse struct {
	Text    string
	Receipt SummaryGenerationReceipt
	// A Go-owned bounded projection may report its actual input identity.
	InputFingerprint     string
	InputMessagesOmitted int
}

type SummaryGenerator interface {
	Generate(context.Context, SummaryGenerationRequest) (SummaryGenerationResponse, error)
}

type GeneratedSummarySource struct {
	SourceID      string `json:"source_id"`
	ContentSHA256 string `json:"content_sha256"`
}

type GeneratedSummary struct {
	Version               string                   `json:"version"`
	Text                  string                   `json:"text"`
	TextSHA256            string                   `json:"text_sha256"`
	TextExcerpted         bool                     `json:"text_excerpted"`
	InputFingerprint      string                   `json:"input_fingerprint"`
	InputMessagesOmitted  int                      `json:"input_messages_omitted,omitempty"`
	SourceRefs            []GeneratedSummarySource `json:"source_refs"`
	SourceRefsOmitted     int                      `json:"source_refs_omitted"`
	Receipt               SummaryGenerationReceipt `json:"receipt"`
	InstructionAuthorized bool                     `json:"instruction_authorized"`
}

func (m *Manager) WithSummaryGenerator(generator SummaryGenerator, sourceSHA256 string) *Manager {
	if m != nil {
		m.generator, m.generationSourceSHA256 = generator, sourceSHA256
	}
	return m
}

// ParseSummaryGenerationText accepts model-authored text only. References,
// receipts and authority are never accepted from the model's JSON response.
// Generation models routinely append self-audit fields such as summary_chars,
// so this decode tolerates unknown fields instead of rejecting the whole
// candidate; stored-data paths keep strictContinuityWindowJSON's exact shape.
// Version and the bounded summary text are still validated exactly.
func ParseSummaryGenerationText(raw string) (string, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	var response struct {
		Version string `json:"version"`
		Summary string `json:"summary"`
	}
	if err := decoder.Decode(&response); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("trailing generated summary JSON")
	}
	if response.Version != GeneratedHandoffVersion {
		return "", errors.New("generated summary response requires generated_handoff.v1")
	}
	if err := generatedTextValidationError(response.Summary); err != nil {
		return "", err
	}
	return strings.TrimSpace(response.Summary), nil
}

func validGeneratedText(text string) bool {
	return generatedTextValidationError(text) == nil
}

func generatedTextValidationError(text string) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return errors.New("generated summary text requires valid UTF-8 without NUL")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("generated summary text must be nonempty")
	}
	if chars := utf8.RuneCountInString(text); chars > MaxGeneratedSummaryChars {
		return fmt.Errorf("generated summary has %d Unicode characters; maximum is %d", chars, MaxGeneratedSummaryChars)
	}
	return nil
}

func validateGeneratedSummary(value *GeneratedSummary) error {
	if value == nil {
		return nil
	}
	if value.Version != GeneratedHandoffVersion || value.InstructionAuthorized ||
		!validGeneratedText(value.Text) || value.TextSHA256 != handoffContentSHA256(value.Text) ||
		!validHandoffDigest(value.InputFingerprint) || value.InputMessagesOmitted < 0 || len(value.SourceRefs) < 1 || len(value.SourceRefs) > 2 || value.SourceRefsOmitted < 0 ||
		!validMemoryIdentity(value.Receipt.RunID) || !validMemoryIdentity(value.Receipt.AttemptID) ||
		!validMemoryIdentity(value.Receipt.Provider) || !validMemoryIdentity(value.Receipt.Model) ||
		value.Receipt.ModelAttempt <= 0 || value.Receipt.CompletionSequence <= 0 || !validHandoffDigest(value.Receipt.SourceSHA256) {
		return errors.New("generated handoff metadata or receipt is invalid")
	}
	seen := map[string]bool{}
	for _, source := range value.SourceRefs {
		kind, id, ok := strings.Cut(source.SourceID, ":")
		number, err := strconv.ParseInt(id, 10, 64)
		if !ok || (kind != "message" && kind != "summary") || err != nil || number <= 0 ||
			strconv.FormatInt(number, 10) != id || !validHandoffDigest(source.ContentSHA256) || seen[source.SourceID] {
			return errors.New("generated handoff source reference is invalid")
		}
		seen[source.SourceID] = true
	}
	return nil
}

func (m *Manager) generateHandoff(ctx context.Context, taskID, workspaceID string,
	older []Message, previous Summary, hasPrevious bool,
) (*GeneratedSummary, error) {
	if !validHandoffDigest(m.generationSourceSHA256) {
		return nil, fmt.Errorf("%w: generation requires an exact source snapshot", ErrSummaryGenerationAborted)
	}
	request := SummaryGenerationRequest{TaskID: taskID, WorkspaceID: workspaceID,
		SourceSHA256: m.generationSourceSHA256, Messages: append([]Message(nil), older...),
		Previous: previous, HasPrevious: hasPrevious, MaxOutputChars: MaxGeneratedSummaryChars}
	for index := range request.Messages {
		message := &request.Messages[index]
		digest := handoffContentSHA256(message.Content)
		if message.ContentSHA256 != "" && message.ContentSHA256 != digest {
			return nil, fmt.Errorf("%w: original generation message digest changed", ErrSummaryGenerationAborted)
		}
		message.ContentSHA256 = digest
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	request.InputFingerprint = handoffContentSHA256(string(encoded))
	// The generator gets a detached full source set. Its model-window budget
	// must either reject excess input or explicitly report its bounded projection.
	detached := request
	detached.Messages = append([]Message(nil), request.Messages...)
	response, err := m.generator.Generate(ctx, detached)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if response.Receipt.SourceSHA256 != request.SourceSHA256 {
		return nil, fmt.Errorf("%w: generation receipt source snapshot changed", ErrSummaryGenerationAborted)
	}
	if err := generatedTextValidationError(response.Text); err != nil {
		return nil, err
	}
	if response.InputMessagesOmitted < 0 || response.InputMessagesOmitted > len(request.Messages) ||
		(response.InputMessagesOmitted > 0 && response.InputFingerprint == "") {
		return nil, fmt.Errorf("%w: generated input coverage is invalid", ErrSummaryGenerationAborted)
	}
	fingerprint := request.InputFingerprint
	if response.InputFingerprint != "" {
		if !validHandoffDigest(response.InputFingerprint) {
			return nil, fmt.Errorf("%w: generated input fingerprint is invalid", ErrSummaryGenerationAborted)
		}
		fingerprint = response.InputFingerprint
	}
	text := strings.TrimSpace(redact.String(response.Text))
	// These references navigate the full compacted source set, including any
	// explicitly omitted generation input; they are not claims of model review.
	refs, omitted := generationNavigationSources(request)
	generated := &GeneratedSummary{Version: GeneratedHandoffVersion, Text: text,
		TextSHA256: handoffContentSHA256(text), InputFingerprint: fingerprint, InputMessagesOmitted: response.InputMessagesOmitted,
		SourceRefs: refs, SourceRefsOmitted: omitted, Receipt: response.Receipt}
	if err := validateGeneratedSummary(generated); err != nil {
		return nil, err
	}
	return generated, nil
}

func generationNavigationSources(request SummaryGenerationRequest) ([]GeneratedSummarySource, int) {
	var refs []GeneratedSummarySource
	if request.HasPrevious && request.Previous.ID > 0 {
		refs = append(refs, GeneratedSummarySource{SourceID: "summary:" + strconv.FormatInt(request.Previous.ID, 10),
			ContentSHA256: handoffContentSHA256(request.Previous.Content)})
	}
	first, lastEvidence := -1, -1
	count := len(refs)
	for index, message := range request.Messages {
		if message.SourceMessageID <= 0 {
			continue
		}
		count++
		if first == -1 {
			first = index
		}
		if message.SourceKind == "tool_result" || message.SourceKind == "go_command_result" {
			lastEvidence = index
		}
	}
	if lastEvidence == -1 {
		lastEvidence = len(request.Messages) - 1
	}
	indices := []int{first, lastEvidence}
	if len(refs) > 0 {
		indices = []int{lastEvidence, first}
	}
	for _, index := range indices {
		if len(refs) == 2 || index < 0 || request.Messages[index].SourceMessageID <= 0 {
			continue
		}
		message := request.Messages[index]
		source := GeneratedSummarySource{SourceID: "message:" + strconv.FormatInt(message.SourceMessageID, 10), ContentSHA256: message.ContentSHA256}
		if len(refs) == 0 || refs[0].SourceID != source.SourceID {
			refs = append(refs, source)
		}
	}
	return refs, max(0, count-len(refs))
}

func summaryGenerationMustAbort(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSummaryGenerationAborted)
}

func generatedFromSummaryContent(content string) *GeneratedSummary {
	var envelope handoffMemoryEnvelope
	if json.Unmarshal([]byte(content), &envelope) == nil && envelope.Version == HandoffMemoryProtocolVersion {
		return envelope.Generated
	}
	return nil
}

func applyGeneratedHandoff(summary Summary, generated *GeneratedSummary, maxChars int, strategy SummaryStrategy) (Summary, error) {
	var envelope handoffMemoryEnvelope
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil {
		return summary, err
	}
	envelope.Generated = generated
	content, err := fitHandoffRecords(context.Background(), envelope, envelope.Records, maxChars, strategy)
	if err != nil {
		return summary, err
	}
	// Existing omissions are already counted; fitting the currently visible
	// records may add new omissions but cannot claim newly compacted messages.
	var fitted handoffMemoryEnvelope
	if err := json.Unmarshal([]byte(content), &fitted); err != nil {
		return summary, err
	}
	if fitted.Generated == nil {
		return summary, errors.New("generated handoff could not fit beside original anchors")
	}
	candidate := summary
	candidate.Content, candidate.ContentSHA256 = content, handoffContentSHA256(content)
	candidate.TokenEstimate = EstimateTokens(content)
	if err := ValidateStoredSummary(candidate); err != nil {
		return summary, err
	}
	return candidate, nil
}

// Generation shares the 4K envelope but its validated text is kept complete.
// Original goal, recent operator sources and actual tool evidence are distinct
// core anchors. If both cannot fit, report an extractive fallback; silently
// head/tail-cutting the generated handoff can remove the correction it conveys.
func fitGeneratedHandoffRecords(ctx context.Context, envelope handoffMemoryEnvelope, records []handoffMemoryRecord, maxRunes int, strategy SummaryStrategy) (string, error) {
	if err := validateGeneratedSummary(envelope.Generated); err != nil {
		return "", err
	}
	plain := envelope
	plain.Generated = nil
	plain.generatedAnchorsOnly = false
	baseline, err := fitPlainHandoffRecords(ctx, plain, records, maxRunes, strategy)
	if err != nil {
		return "", err
	}
	envelope.generatedAnchorsOnly = true
	// An inherited valid handoff may itself exceed a smaller rolling window's
	// remaining budget. Fall back to its exact-source extractive projection;
	// cancellation or strategy errors below still propagate normally.
	empty, err := encodeHandoffSelection(envelope, nil, len(records))
	if err != nil {
		return "", err
	}
	if utf8.RuneCount(empty) > maxRunes {
		return baseline, nil
	}
	content, err := fitPlainHandoffRecords(ctx, envelope, records, maxRunes, strategy)
	if err != nil {
		return "", err
	}
	var fitted handoffMemoryEnvelope
	if err := json.Unmarshal([]byte(content), &fitted); err != nil {
		return "", err
	}
	for _, anchor := range handoffAnchorRecords(records) {
		found := false
		for _, record := range fitted.Records {
			if record.Ordinal == anchor.Ordinal {
				found = true
				break
			}
		}
		if !found {
			return baseline, nil
		}
	}
	return content, nil
}

func validateGeneratedHandoffRedaction(content string) error {
	var envelope handoffMemoryEnvelope
	if err := strictContinuityWindowJSON(content, &envelope); err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return err
	}
	var safe func(any) bool
	safe = func(value any) bool {
		switch typed := value.(type) {
		case string:
			return redact.String(typed) == typed
		case []any:
			for _, child := range typed {
				if !safe(child) {
					return false
				}
			}
		case map[string]any:
			for _, child := range typed {
				if !safe(child) {
					return false
				}
			}
		}
		return true
	}
	if !safe(value) {
		return errors.New("generated handoff requires redacted stored text and metadata")
	}
	return nil
}
