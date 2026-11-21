package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

type Message struct {
	Role                  string
	Content               string
	CreatedAt             time.Time
	SourceMessageID       int64
	SourceKind            string
	SourceRef             string
	ContentSHA256         string
	InstructionAuthorized bool
}

type Summary struct {
	ID                    int64
	TaskID                string
	WorkspaceID           string
	ProtocolVersion       string
	PreviousSummaryID     int64
	Content               string
	ContentSHA256         string
	CompactedMessageCount int
	SourceMessageCount    int
	PreservedMessageCount int
	TokenEstimate         int
	CreatedAt             time.Time
}

type Result struct {
	Compacted                bool
	Summary                  Summary
	Preserved                []Message
	RemovedMessages          int
	Generated                bool
	GenerationFallbackReason string
}

type Config struct {
	MaxMessagesBeforeCompact int
	PreserveRecentMessages   int
	MaxSummaryChars          int
	MaxLineChars             int
}

type SummaryStore interface {
	SaveContextSummary(ctx context.Context, summary Summary) (Summary, error)
	LatestContextSummary(ctx context.Context, taskID string) (Summary, bool, error)
}

type Manager struct {
	store                  SummaryStore
	config                 Config
	beforeCompactGuard     func(context.Context, string, string, int, int) error
	strategy               SummaryStrategy
	generator              SummaryGenerator
	generationSourceSHA256 string
}

func NewManager(store SummaryStore, config Config) *Manager {
	return &Manager{store: store, config: config.withDefaults()}
}

// WithBeforeCompactGuard installs a Go-owned, fail-closed boundary immediately
// before an actual compaction. It is not called when MaybeCompact decides no
// work is needed.
func (m *Manager) WithBeforeCompactGuard(
	guard func(context.Context, string, string, int, int) error,
) *Manager {
	if m != nil {
		m.beforeCompactGuard = guard
	}
	return m
}

func DefaultConfig() Config {
	return Config{
		MaxMessagesBeforeCompact: 8,
		PreserveRecentMessages:   4,
		MaxSummaryChars:          MaxHandoffMemoryChars,
		MaxLineChars:             MaxHandoffRecordChars,
	}
}

func (c Config) withDefaults() Config {
	defaults := DefaultConfig()
	if c.MaxMessagesBeforeCompact <= 0 {
		c.MaxMessagesBeforeCompact = defaults.MaxMessagesBeforeCompact
	}
	if c.PreserveRecentMessages <= 0 {
		c.PreserveRecentMessages = defaults.PreserveRecentMessages
	}
	if c.MaxSummaryChars < 512 {
		c.MaxSummaryChars = defaults.MaxSummaryChars
	} else if c.MaxSummaryChars > MaxHandoffMemoryChars {
		c.MaxSummaryChars = MaxHandoffMemoryChars
	}
	if c.MaxLineChars <= 0 {
		c.MaxLineChars = defaults.MaxLineChars
	} else if c.MaxLineChars > MaxHandoffRecordChars {
		c.MaxLineChars = MaxHandoffRecordChars
	}
	if c.PreserveRecentMessages > c.MaxMessagesBeforeCompact {
		c.PreserveRecentMessages = c.MaxMessagesBeforeCompact
	}
	return c
}

func (m *Manager) ShouldCompact(messages []Message) bool {
	return len(messages) > m.config.MaxMessagesBeforeCompact
}

func (m *Manager) MaybeCompact(ctx context.Context, taskID string, workspaceID string, messages []Message) (Result, error) {
	if !m.ShouldCompact(messages) {
		return Result{Compacted: false, Preserved: redactMessages(messages)}, nil
	}
	return m.Compact(ctx, taskID, workspaceID, messages)
}

func (m *Manager) Compact(ctx context.Context, taskID string, workspaceID string, messages []Message) (Result, error) {
	var previous Summary
	var hasPrevious bool
	var err error
	if m.store != nil {
		previous, hasPrevious, err = m.store.LatestContextSummary(ctx, taskID)
		if err != nil {
			return Result{}, err
		}
	}
	result, err := m.PrepareCandidate(ctx, taskID, workspaceID, messages, previous, hasPrevious)
	if err != nil {
		return Result{}, err
	}
	// A repeated source high-water reuses its immutable summary.
	if m.store != nil && result.Summary.ID == 0 {
		result.Summary, err = m.store.SaveContextSummary(ctx, result.Summary)
		if err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

// PrepareCandidate computes a bounded summary without reading or writing the
// store. Callers that publish it later must revalidate the source snapshot and
// previous summary before atomically saving it and marking compacted messages.
func (m *Manager) PrepareCandidate(ctx context.Context, taskID string, workspaceID string,
	messages []Message, previous Summary, hasPrevious bool,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(taskID) == "" {
		return Result{}, errors.New("task id is required")
	}
	if len(messages) == 0 {
		return Result{}, errors.New("at least one message is required")
	}

	preserveCount := m.config.PreserveRecentMessages
	if preserveCount > len(messages) {
		preserveCount = len(messages)
	}
	if preserveCount == len(messages) && len(messages) > 0 {
		preserveCount = len(messages) - 1
	}
	if m.beforeCompactGuard != nil {
		if err := m.beforeCompactGuard(ctx, taskID, strings.TrimSpace(workspaceID),
			len(messages), preserveCount); err != nil {
			return Result{}, err
		}
	}
	removeCount := len(messages) - preserveCount
	older := messages[:removeCount]
	preserved := redactMessages(messages[removeCount:])

	workspaceID = strings.TrimSpace(workspaceID)
	if hasPrevious {
		if err := ValidateStoredSummary(previous); err != nil {
			return Result{}, fmt.Errorf("load previous handoff summary: %w", err)
		}
		if previous.TaskID != taskID || previous.WorkspaceID != workspaceID {
			return Result{}, errors.New("previous handoff summary task or workspace does not match")
		}
	} else {
		previous = Summary{}
	}
	sources, newlyCompacted, err := collectHandoffSources(taskID, workspaceID, previous,
		hasPrevious, older, len(preserved), m.config.MaxLineChars)
	if err != nil {
		return Result{}, err
	}
	content, err := fitHandoffRecords(ctx, sources, sources.Records, m.config.MaxSummaryChars, m.strategy)
	if err != nil {
		return Result{}, err
	}
	if hasPrevious && newlyCompacted == 0 {
		return Result{
			Compacted: true, Summary: previous, Preserved: preserved,
			RemovedMessages: removeCount,
		}, nil
	}
	summary := Summary{
		TaskID:                taskID,
		WorkspaceID:           workspaceID,
		ProtocolVersion:       HandoffMemoryProtocolVersion,
		PreviousSummaryID:     previous.ID,
		Content:               content,
		ContentSHA256:         handoffContentSHA256(content),
		CompactedMessageCount: sources.CompactedMessageCount,
		SourceMessageCount:    saturatingTokenAdd(sources.CompactedMessageCount, len(preserved)),
		PreservedMessageCount: len(preserved),
		TokenEstimate:         EstimateTokens(content),
		CreatedAt:             time.Now().UTC(),
	}
	generated, generationFallback := false, ""
	if m.generator != nil {
		candidate, generationErr := m.generateHandoff(ctx, taskID, workspaceID, older, previous, hasPrevious)
		if generationErr == nil {
			summary, generationErr = applyGeneratedHandoff(ctx, summary, sources, candidate, m.config.MaxSummaryChars, m.strategy)
			generated = generationErr == nil
		}
		if generationErr != nil {
			if summaryGenerationMustAbort(generationErr) {
				return Result{}, generationErr
			}
			generationFallback = excerptHandoffContent(redact.String(generationErr.Error()), 256)
		}
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	return Result{
		Compacted:       true,
		Summary:         summary,
		Preserved:       preserved,
		RemovedMessages: removeCount,
		Generated:       generated, GenerationFallbackReason: generationFallback,
	}, nil
}

func (m *Manager) Latest(ctx context.Context, taskID string) (Summary, bool, error) {
	if m.store == nil {
		return Summary{}, false, errors.New("summary store is not configured")
	}
	return m.store.LatestContextSummary(ctx, taskID)
}

func (m *Manager) BuildPrompt(system string, summary Summary, recent []Message, originals ...Message) ([]Message, error) {
	handoff, err := buildHandoffSection(summary, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(recent)+len(originals)+2)
	if strings.TrimSpace(system) != "" {
		out = append(out, Message{
			Role: "system", Content: system, SourceKind: "go_control", InstructionAuthorized: true,
		})
	}
	if handoff.Content != "" {
		replayed := make(map[string]Message, len(originals))
		for _, original := range originals {
			if original.Role != "user" || original.SourceKind != "operator_message" || !original.InstructionAuthorized ||
				original.SourceMessageID <= 0 || !validHandoffDigest(original.ContentSHA256) {
				return nil, errors.New("compacted operator original lacks a verified source")
			}
			out = append(out, original)
			replayed[fmt.Sprintf("message:%d", original.SourceMessageID)] = original
		}
		handoffRole := "user"
		if len(originals) > 0 {
			handoffRole = "assistant"
		}
		out = append(out, Message{
			Role: handoffRole, SourceKind: "compacted_transcript", InstructionAuthorized: false,
			Content: handoff.WithReplayedOperators(replayed).Content,
		})
	}
	out = append(out, redactMessages(recent)...)
	return out, nil
}

func ParseMessage(raw string) Message {
	raw = strings.TrimSpace(raw)
	role := "user"
	content := raw
	if before, after, ok := strings.Cut(raw, ":"); ok {
		candidate := strings.ToLower(strings.TrimSpace(before))
		if isKnownRole(candidate) {
			role = candidate
			content = strings.TrimSpace(after)
		}
	}
	sourceKind, authorized := summaryMessageAuthority(Message{}, role)
	return Message{
		Role: role, Content: redact.String(content), CreatedAt: time.Now().UTC(),
		SourceKind: sourceKind, InstructionAuthorized: authorized,
	}
}

// AutoCompactInputThreshold leaves room for another step after the model's
// output allowance and safety margin have already been reserved.
func AutoCompactInputThreshold(inputLimit int) int { return inputLimit - inputLimit/10 }

// EstimateMessageTokens includes the role and per-message framing for text.
func EstimateMessageTokens(role, content string) int {
	return saturatingTokenAdd(4+EstimateTokens(role), EstimateTokens(content))
}

func EstimateTokens(value string) int {
	if value == "" {
		return 0
	}
	// ASCII prose is usually close to four characters per token. Unknown model
	// tokenizers are much less predictable for CJK, emoji, and other Unicode,
	// so count each non-ASCII UTF-8 byte as one token. This intentionally
	// overestimates some models instead of allowing multilingual prompts to
	// overflow a provider context window.
	tokens := 0
	for len(value) > 0 {
		asciiEnd := 0
		for asciiEnd < len(value) && value[asciiEnd] < utf8.RuneSelf {
			asciiEnd++
		}
		if asciiEnd > 0 {
			tokens = saturatingTokenAdd(tokens, estimateASCIITokens(value[:asciiEnd]))
			value = value[asciiEnd:]
			continue
		}
		_, size := utf8.DecodeRuneInString(value)
		if size <= 0 {
			size = 1
		}
		tokens = saturatingTokenAdd(tokens, size)
		value = value[size:]
	}
	return tokens
}

func estimateASCIITokens(value string) int {
	if value == "" {
		return 0
	}
	words := len(strings.Fields(value))
	byChars := (len(value) + 3) / 4
	if words > byChars {
		return words
	}
	return byChars
}

func saturatingTokenAdd(current int, addition int) int {
	if addition <= 0 {
		return current
	}
	maxInt := int(^uint(0) >> 1)
	if current > maxInt-addition {
		return maxInt
	}
	return current + addition
}

func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if isKnownRole(role) {
		return role
	}
	return "tool"
}

func isKnownRole(role string) bool {
	switch role {
	case "system", "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func trimRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func copyMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	return out
}

func redactMessages(messages []Message) []Message {
	out := copyMessages(messages)
	for i := range out {
		out[i].Content = redact.String(out[i].Content)
	}
	return out
}

func summaryMessageAuthority(message Message, role string) (string, bool) {
	if source := strings.TrimSpace(message.SourceKind); source != "" {
		return source, message.InstructionAuthorized
	}
	switch role {
	case "user":
		return "operator_message", true
	case "assistant":
		return "model_response", false
	case "system":
		return "go_control", true
	case "tool":
		return "tool_result", false
	default:
		return "legacy_unclassified", false
	}
}
