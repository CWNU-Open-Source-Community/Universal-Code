package contextmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	HandoffMemoryProtocolVersion = "handoff_memory.v1"
	LegacyHandoffProtocolVersion = "handoff_memory.v0"
	MaxHandoffMemoryRecords      = 12
	MaxHandoffMemoryChars        = 4000
	MaxHandoffRecordChars        = 1000
	MaxHandoffSourceRefChars     = 256
)

type handoffMemoryEnvelope struct {
	Version                string                `json:"version"`
	TaskID                 string                `json:"task_id"`
	WorkspaceID            string                `json:"workspace_id,omitempty"`
	PreviousSummaryID      int64                 `json:"previous_summary_id,omitempty"`
	CompactedMessageCount  int                   `json:"compacted_message_count"`
	PreservedMessageCount  int                   `json:"preserved_message_count"`
	SourceThroughMessageID int64                 `json:"source_through_message_id,omitempty"`
	LastOrdinal            int                   `json:"last_ordinal"`
	RecordsOmitted         int                   `json:"records_omitted"`
	Records                []handoffMemoryRecord `json:"records"`
	Generated              *GeneratedSummary     `json:"generated,omitempty"`
	// Continuity windows already link the exact current summary for readback.
	// Under their extra outer-metadata pressure, original operator sources take
	// precedence over an assistant-progress excerpt, never over actual evidence.
	preferOperatorRecords bool
	generatedAnchorsOnly  bool
}

type handoffMemoryRecord struct {
	Ordinal               int    `json:"ordinal"`
	Category              string `json:"category"`
	Role                  string `json:"role"`
	SourceKind            string `json:"source_kind"`
	SourceRef             string `json:"source_ref,omitempty"`
	SourceMessageID       int64  `json:"source_message_id,omitempty"`
	SourceContentSHA256   string `json:"source_content_sha256,omitempty"`
	ContentSHA256         string `json:"content_sha256,omitempty"`
	InstructionAuthorized bool   `json:"instruction_authorized"`
	Content               string `json:"content"`
}

func buildHandoffMemory(ctx context.Context, taskID string, workspaceID string, previous Summary,
	hasPrevious bool, older []Message, preservedCount int, config Config, strategy SummaryStrategy,
) (string, int, int, error) {
	records, previousOmitted, compactedCount, lastOrdinal, sourceThrough :=
		previousHandoffRecords(taskID, workspaceID, previous, hasPrevious, config.MaxLineChars)
	newOlder, nextSourceThrough, err := newHandoffMessages(older, sourceThrough)
	if err != nil {
		return "", 0, 0, err
	}
	for _, message := range newOlder {
		role := normalizeRole(message.Role)
		content := excerptHandoffContent(redact.String(message.Content), config.MaxLineChars)
		if content == "" {
			continue
		}
		lastOrdinal = saturatingTokenAdd(lastOrdinal, 1)
		sourceKind, instructionAuthorized := summaryMessageAuthority(message, role)
		sourceKind = normalizeHandoffSourceKind(sourceKind)
		instructionAuthorized = validHandoffAuthority(role, sourceKind, instructionAuthorized)
		sourceContentDigest := strings.TrimSpace(message.ContentSHA256)
		if !validHandoffDigest(sourceContentDigest) {
			sourceContentDigest = ""
		}
		records = append(records, handoffMemoryRecord{
			Ordinal:  lastOrdinal,
			Category: handoffRecordCategory(role, sourceKind, instructionAuthorized),
			Role:     role, SourceKind: sourceKind,
			SourceRef:           sanitizeHandoffSourceRef(message.SourceRef),
			SourceMessageID:     message.SourceMessageID,
			SourceContentSHA256: sourceContentDigest, ContentSHA256: handoffContentSHA256(content),
			InstructionAuthorized: instructionAuthorized, Content: content,
		})
	}
	compactedCount = saturatingTokenAdd(compactedCount, len(newOlder))
	envelope := handoffMemoryEnvelope{
		Version: HandoffMemoryProtocolVersion, TaskID: taskID, WorkspaceID: workspaceID,
		CompactedMessageCount: compactedCount, PreservedMessageCount: preservedCount,
		SourceThroughMessageID: nextSourceThrough,
		LastOrdinal:            lastOrdinal, RecordsOmitted: previousOmitted,
		Records: []handoffMemoryRecord{},
	}
	if hasPrevious {
		envelope.PreviousSummaryID = previous.ID
		envelope.Generated = generatedFromSummaryContent(previous.Content)
	}
	content, err := fitHandoffRecords(ctx, envelope, records, config.MaxSummaryChars, strategy)
	if err != nil {
		return "", 0, 0, err
	}
	return content, compactedCount, len(newOlder), nil
}

func previousHandoffRecords(taskID string, workspaceID string, previous Summary,
	hasPrevious bool, maxLineChars int,
) ([]handoffMemoryRecord, int, int, int, int64) {
	if !hasPrevious {
		return []handoffMemoryRecord{}, 0, 0, 0, 0
	}
	if previous.ProtocolVersion == HandoffMemoryProtocolVersion &&
		previous.ContentSHA256 == handoffContentSHA256(previous.Content) {
		var envelope handoffMemoryEnvelope
		if json.Unmarshal([]byte(previous.Content), &envelope) == nil &&
			validateHandoffEnvelope(envelope, taskID, workspaceID) == nil {
			lastOrdinal := envelope.LastOrdinal
			for index := range envelope.Records {
				record := &envelope.Records[index]
				record.Content = excerptHandoffContent(redact.String(record.Content),
					maxLineChars)
				record.SourceKind = normalizeHandoffSourceKind(record.SourceKind)
				record.SourceRef = sanitizeHandoffSourceRef(record.SourceRef)
				record.InstructionAuthorized = validHandoffAuthority(record.Role,
					record.SourceKind, record.InstructionAuthorized)
				record.Category = handoffRecordCategory(record.Role, record.SourceKind,
					record.InstructionAuthorized)
				record.ContentSHA256 = handoffContentSHA256(record.Content)
			}
			return envelope.Records, envelope.RecordsOmitted,
				envelope.CompactedMessageCount, lastOrdinal, envelope.SourceThroughMessageID
		}
	}
	content := excerptHandoffContent(redact.String(previous.Content), maxLineChars)
	compacted := previous.SourceMessageCount - previous.PreservedMessageCount
	if compacted < 1 && content != "" {
		compacted = 1
	}
	if content == "" {
		return []handoffMemoryRecord{}, 0, compacted, 0, 0
	}
	return []handoffMemoryRecord{{
		Ordinal: 1, Category: "prior_handoff", Role: "tool",
		SourceKind: "compacted_transcript", ContentSHA256: handoffContentSHA256(content),
		InstructionAuthorized: false, Content: content,
	}}, 0, compacted, 1, 0
}

func newHandoffMessages(messages []Message, previousThrough int64) ([]Message, int64, error) {
	if previousThrough < 0 {
		return nil, 0, errors.New("handoff source message high-water is invalid")
	}
	out := make([]Message, 0, len(messages))
	lastPositiveID := int64(0)
	nextThrough := previousThrough
	for _, message := range messages {
		if message.SourceMessageID < 0 {
			return nil, 0, errors.New("handoff source message id is invalid")
		}
		if message.SourceMessageID > 0 {
			if message.SourceMessageID <= lastPositiveID {
				return nil, 0, errors.New("handoff source message ids are not increasing")
			}
			lastPositiveID = message.SourceMessageID
			if message.SourceMessageID <= previousThrough {
				continue
			}
			nextThrough = message.SourceMessageID
		}
		out = append(out, message)
	}
	return out, nextThrough, nil
}

func fitHandoffRecords(ctx context.Context, envelope handoffMemoryEnvelope, records []handoffMemoryRecord,
	maxRunes int, strategy SummaryStrategy,
) (string, error) {
	if envelope.Generated != nil {
		return fitGeneratedHandoffRecords(ctx, envelope, records, maxRunes, strategy)
	}
	return fitPlainHandoffRecords(ctx, envelope, records, maxRunes, strategy)
}

func fitPlainHandoffRecords(ctx context.Context, envelope handoffMemoryEnvelope, records []handoffMemoryRecord,
	maxRunes int, strategy SummaryStrategy,
) (string, error) {
	if maxRunes < 512 {
		return "", errors.New("handoff memory requires at least 512 summary characters")
	}
	candidates, err := rankHandoffRecords(ctx, records, strategy)
	if err != nil {
		return "", err
	}
	// Reserve independently sourced context before spending the remaining space
	// on recent history. Recency alone lets repeated "continue" messages evict
	// the objective, and model progress must not crowd out actual tool evidence.
	selected := handoffAnchorRecordsWithPolicy(records, envelope.preferOperatorRecords, envelope.generatedAnchorsOnly)
	for len(selected) > 0 {
		fitted, ok, err := fitHandoffSelection(envelope, selected, len(records), maxRunes, 0)
		if err != nil {
			return "", err
		}
		if ok {
			selected = fitted
			break
		}
		// Keep recent operator sources ahead of the initial objective and older
		// updates when source metadata alone exhausts this bounded envelope.
		selected = selected[:len(selected)-1]
	}
	for _, candidate := range candidates {
		if len(selected) >= MaxHandoffMemoryRecords || handoffRecordAlreadySelected(selected, candidate) {
			continue
		}
		trial := append(append([]handoffMemoryRecord(nil), selected...), candidate)
		// Optional records may use spare space, but cannot further shorten the
		// reserved objective, correction, progress, or evidence.
		fitted, ok, err := fitHandoffSelection(envelope, trial, len(records), maxRunes, len(selected))
		if err != nil {
			return "", err
		}
		if ok {
			selected = fitted
		}
	}
	encoded, err := encodeHandoffSelection(envelope, selected, len(records))
	if err != nil {
		return "", err
	}
	if utf8.RuneCount(encoded) > maxRunes {
		return "", errors.New("handoff memory envelope exceeds its character limit")
	}
	return string(encoded), nil
}

func encodeHandoffSelection(envelope handoffMemoryEnvelope, selected []handoffMemoryRecord,
	recordCount int,
) ([]byte, error) {
	selected = append(make([]handoffMemoryRecord, 0, len(selected)), selected...)
	sort.SliceStable(selected, func(left int, right int) bool {
		return selected[left].Ordinal < selected[right].Ordinal
	})
	envelope.Records = selected
	envelope.RecordsOmitted = saturatingTokenAdd(envelope.RecordsOmitted, recordCount-len(selected))
	return json.Marshal(envelope)
}

func fitHandoffSelection(envelope handoffMemoryEnvelope, records []handoffMemoryRecord,
	recordCount, maxRunes, fixedCount int,
) ([]handoffMemoryRecord, bool, error) {
	encoded, err := encodeHandoffSelection(envelope, records, recordCount)
	if err != nil {
		return nil, false, err
	}
	if utf8.RuneCount(encoded) <= maxRunes {
		return records, true, nil
	}
	// Metadata and content share the existing envelope budget. A common content
	// ceiling gives each reserved source space without assigning semantic truth
	// to any of its text. Short records remain complete.
	low, high := 96, MaxHandoffRecordChars
	if envelope.preferOperatorRecords {
		// The window's two exact source references consume additional space.
		// Short operator corrections remain complete; long excerpts still have
		// a fixed lower bound and explicit readback sources, never empty text.
		low = 64
	}
	var best []handoffMemoryRecord
	for low <= high {
		limit := low + (high-low)/2
		trial := append([]handoffMemoryRecord(nil), records...)
		for index := fixedCount; index < len(trial); index++ {
			trial[index].Content = excerptHandoffContent(trial[index].Content, limit)
			trial[index].ContentSHA256 = handoffContentSHA256(trial[index].Content)
			// A complete source needs only its original digest on the wire. Do
			// not shorten a small update if adding a separate excerpt digest
			// would consume more space than preserving that update completely.
			before, _ := json.Marshal(records[index])
			after, _ := json.Marshal(trial[index])
			if utf8.RuneCount(after) >= utf8.RuneCount(before) {
				trial[index] = records[index]
			}
		}
		encoded, err := encodeHandoffSelection(envelope, trial, recordCount)
		if err != nil {
			return nil, false, err
		}
		if utf8.RuneCount(encoded) <= maxRunes {
			best = trial
			low = limit + 1
		} else {
			high = limit - 1
		}
	}
	return best, best != nil, nil
}

func handoffAnchorRecords(records []handoffMemoryRecord) []handoffMemoryRecord {
	return handoffAnchorRecordsWithPolicy(records, false, true)
}

func handoffAnchorRecordsWithPolicy(records []handoffMemoryRecord, preferOperatorRecords, generatedAnchorsOnly bool) []handoffMemoryRecord {
	var firstIntent, latestProgress, latestEvidence *handoffMemoryRecord
	var operators []handoffMemoryRecord
	evidencePriority := -1
	for index := range records {
		record := &records[index]
		if record.Category == "operator_intent" && record.InstructionAuthorized {
			if firstIntent == nil || record.Ordinal < firstIntent.Ordinal {
				firstIntent = record
			}
			if !handoffContinuationOnly(record.Content) {
				operators = append(operators, *record)
			}
		}
		if record.Category == "assistant_progress" &&
			(latestProgress == nil || record.Ordinal > latestProgress.Ordinal) {
			latestProgress = record
		}
		if record.Category == "external_evidence" {
			priority := 0
			if record.SourceKind == "tool_result" || record.SourceKind == "go_command_result" {
				priority = 1
			}
			if latestEvidence == nil || priority > evidencePriority ||
				(priority == evidencePriority && record.Ordinal > latestEvidence.Ordinal) {
				latestEvidence, evidencePriority = record, priority
			}
		}
	}
	// Treat every substantive operator source as a possible correction. A fixed
	// first-follow-up slot and one latest slot lost intermediate corrections as
	// soon as another request arrived. These sources now share the content budget;
	// source recency decides overflow, not business keywords or model claims.
	sort.SliceStable(operators, func(i, j int) bool { return operators[i].Ordinal > operators[j].Ordinal })
	selected := make([]handoffMemoryRecord, 0, MaxHandoffMemoryRecords)
	add := func(record *handoffMemoryRecord) {
		if record != nil && len(selected) < MaxHandoffMemoryRecords && !handoffRecordAlreadySelected(selected, *record) {
			selected = append(selected, *record)
		}
	}
	for index := 0; index < len(operators) && index < 2; index++ {
		add(&operators[index])
	}
	add(latestEvidence)
	if !preferOperatorRecords && !generatedAnchorsOnly {
		add(latestProgress)
	}
	add(firstIntent)
	if !generatedAnchorsOnly {
		for index := range operators {
			if len(selected) >= MaxHandoffMemoryRecords {
				break
			}
			add(&operators[index])
		}
	}
	if preferOperatorRecords && !generatedAnchorsOnly {
		add(latestProgress)
	}
	return selected
}

func handoffRecordAlreadySelected(selected []handoffMemoryRecord, candidate handoffMemoryRecord) bool {
	for _, record := range selected {
		if record.Ordinal == candidate.Ordinal {
			return true
		}
		// Deduplicate only identical operator text for selection. The durable
		// source rows and cumulative counters are never removed or rewritten.
		if record.Category == "operator_intent" && candidate.Category == "operator_intent" &&
			record.InstructionAuthorized && candidate.InstructionAuthorized &&
			((record.SourceContentSHA256 != "" && record.SourceContentSHA256 == candidate.SourceContentSHA256) ||
				(record.SourceContentSHA256 == "" && candidate.SourceContentSHA256 == "" && record.Content == candidate.Content)) {
			return true
		}
	}
	return false
}

func handoffContinuationOnly(content string) bool {
	// Exact, closed phrases only: "continue, but do not ..." remains substantive
	// operator input. This is selection priority, never an instruction classifier.
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "继续", "继续吧", "继续推进", "继续推进吧", "继续。", "继续吧。",
		"continue", "continue.", "please continue", "please continue.", "go on", "go on.":
		return true
	default:
		return false
	}
}

const handoffExcerptMarker = "\n[... bounded excerpt: middle omitted ...]\n"

func excerptHandoffContent(content string, maxRunes int) string {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= maxRunes {
		return content
	}
	marker := handoffExcerptMarker
	if maxRunes < 96 {
		marker = "\n[... middle omitted ...]\n"
	}
	markerRunes := utf8.RuneCountInString(marker)
	if maxRunes <= markerRunes+2 {
		marker, markerRunes = "...", 3
	}
	if maxRunes <= markerRunes {
		return trimRunes(content, maxRunes)
	}
	runes := []rune(content)
	remaining := maxRunes - markerRunes
	head := (remaining + 1) / 2
	tail := remaining - head
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

func handoffRetentionPriority(record handoffMemoryRecord) int {
	switch {
	case record.InstructionAuthorized && record.Category == "operator_intent" && handoffContinuationOnly(record.Content):
		return 0
	case record.InstructionAuthorized && record.Category == "operator_intent":
		return 4
	case record.Category == "assistant_progress":
		return 3
	case record.Category == "go_control":
		return 2
	default:
		return 1
	}
}

func handoffRecordCategory(role string, sourceKind string, authorized bool) string {
	switch {
	case sourceKind == "compacted_transcript":
		return "prior_handoff"
	case authorized && role == "user" && sourceKind == "operator_message":
		return "operator_intent"
	case authorized && role == "system" && sourceKind == "go_control":
		return "go_control"
	case role == "assistant":
		return "assistant_progress"
	default:
		return "external_evidence"
	}
}

func validHandoffAuthority(role string, sourceKind string, authorized bool) bool {
	if !authorized {
		return false
	}
	return (role == "user" && sourceKind == "operator_message") ||
		(role == "system" && sourceKind == "go_control")
}

func validateHandoffEnvelope(envelope handoffMemoryEnvelope, taskID string,
	workspaceID string,
) error {
	if err := validateGeneratedSummary(envelope.Generated); err != nil {
		return err
	}
	if envelope.Version != HandoffMemoryProtocolVersion || envelope.TaskID != taskID ||
		envelope.WorkspaceID != workspaceID || envelope.PreviousSummaryID < 0 ||
		envelope.SourceThroughMessageID < 0 ||
		envelope.CompactedMessageCount < 0 || envelope.PreservedMessageCount < 0 ||
		envelope.LastOrdinal < 0 || envelope.LastOrdinal > envelope.CompactedMessageCount ||
		envelope.RecordsOmitted < 0 || len(envelope.Records) > MaxHandoffMemoryRecords ||
		envelope.LastOrdinal != len(envelope.Records)+envelope.RecordsOmitted ||
		envelope.CompactedMessageCount < len(envelope.Records)+envelope.RecordsOmitted {
		return errors.New("handoff memory envelope metadata is invalid")
	}
	previousOrdinal := 0
	previousSourceMessageID := int64(0)
	for _, record := range envelope.Records {
		if record.Ordinal <= previousOrdinal || record.Ordinal > envelope.LastOrdinal ||
			!isKnownRole(record.Role) ||
			record.SourceKind != normalizeHandoffSourceKind(record.SourceKind) ||
			record.SourceRef != sanitizeHandoffSourceRef(record.SourceRef) ||
			record.SourceMessageID < 0 ||
			(record.SourceMessageID > 0 && (record.SourceMessageID <= previousSourceMessageID ||
				record.SourceMessageID > envelope.SourceThroughMessageID)) ||
			record.Content == "" || utf8.RuneCountInString(record.Content) > MaxHandoffRecordChars ||
			!utf8.ValidString(record.Content) ||
			record.ContentSHA256 != handoffContentSHA256(record.Content) ||
			(record.SourceContentSHA256 != "" && !validHandoffDigest(record.SourceContentSHA256)) ||
			record.InstructionAuthorized != validHandoffAuthority(record.Role,
				record.SourceKind, record.InstructionAuthorized) ||
			record.Category != handoffRecordCategory(record.Role, record.SourceKind,
				record.InstructionAuthorized) {
			return errors.New("handoff memory record is invalid")
		}
		previousOrdinal = record.Ordinal
		if record.SourceMessageID > 0 {
			previousSourceMessageID = record.SourceMessageID
		}
	}
	return nil
}

// storedHandoffEnvelope reports whether content already decodes as a handoff
// memory envelope, which is the point after which a text regex would be running
// over serialized JSON instead of over record text.
func storedHandoffEnvelope(content string) bool {
	var envelope handoffMemoryEnvelope
	return json.Unmarshal([]byte(content), &envelope) == nil &&
		envelope.Version == HandoffMemoryProtocolVersion
}

func PrepareSummaryForStorage(summary Summary) (Summary, error) {
	summary.TaskID = strings.TrimSpace(summary.TaskID)
	summary.WorkspaceID = strings.TrimSpace(summary.WorkspaceID)
	if summary.TaskID == "" {
		return Summary{}, errors.New("task id is required")
	}
	if summary.SourceMessageCount < 0 || summary.PreservedMessageCount < 0 ||
		summary.PreservedMessageCount > summary.SourceMessageCount {
		return Summary{}, errors.New("handoff summary source counters are invalid")
	}
	// A stored handoff envelope is serialized JSON whose record text is already
	// bound by per-record digests. Generated JSON additionally carries byte-bound
	// text and receipt fields, so verify each decoded string is already redacted;
	// applying a text regex to serialized JSON would consume escaped newlines and
	// invalidate an honest digest. An envelope without a generated receipt has the
	// same hazard without the extra shape: Compact masks text before it excerpts
	// and digests it, and redact.String is not idempotent, so a second pass here
	// would rewrite text that content_sha256 still binds and reject the record.
	if summary.ProtocolVersion == HandoffMemoryProtocolVersion && storedHandoffEnvelope(summary.Content) {
		if generatedFromSummaryContent(summary.Content) != nil {
			if err := validateGeneratedHandoffRedaction(summary.Content); err != nil {
				return Summary{}, err
			}
		}
		summary.Content = strings.TrimSpace(summary.Content)
	} else {
		summary.Content = strings.TrimSpace(redact.String(summary.Content))
	}
	if summary.Content == "" || !utf8.ValidString(summary.Content) {
		return Summary{}, errors.New("handoff summary content must be nonempty UTF-8")
	}
	if summary.ProtocolVersion == "" {
		compacted := summary.SourceMessageCount - summary.PreservedMessageCount
		if compacted < 1 {
			compacted = 1
		}
		recordContent := excerptHandoffContent(summary.Content, MaxHandoffRecordChars)
		record := handoffMemoryRecord{
			Ordinal: 1, Category: "prior_handoff", Role: "tool",
			SourceKind: "compacted_transcript", ContentSHA256: handoffContentSHA256(recordContent),
			InstructionAuthorized: false, Content: recordContent,
		}
		envelope := handoffMemoryEnvelope{
			Version: HandoffMemoryProtocolVersion, TaskID: summary.TaskID,
			WorkspaceID: summary.WorkspaceID, PreviousSummaryID: summary.PreviousSummaryID,
			CompactedMessageCount: compacted,
			PreservedMessageCount: summary.PreservedMessageCount,
			LastOrdinal:           1,
			Records:               []handoffMemoryRecord{record},
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return Summary{}, err
		}
		summary.Content = string(encoded)
		summary.CompactedMessageCount = compacted
		summary.SourceMessageCount = saturatingTokenAdd(compacted,
			summary.PreservedMessageCount)
		summary.ProtocolVersion = HandoffMemoryProtocolVersion
	}
	if summary.ProtocolVersion != HandoffMemoryProtocolVersion || summary.PreviousSummaryID < 0 ||
		summary.CompactedMessageCount < 1 || summary.PreservedMessageCount < 0 ||
		utf8.RuneCountInString(summary.Content) > MaxHandoffMemoryChars ||
		summary.SourceMessageCount != saturatingTokenAdd(summary.CompactedMessageCount,
			summary.PreservedMessageCount) {
		return Summary{}, errors.New("handoff summary counters are invalid")
	}
	var envelope handoffMemoryEnvelope
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil {
		return Summary{}, fmt.Errorf("decode handoff summary: %w", err)
	}
	if err := validateHandoffEnvelope(envelope, summary.TaskID, summary.WorkspaceID); err != nil {
		return Summary{}, err
	}
	if envelope.PreviousSummaryID != summary.PreviousSummaryID ||
		envelope.CompactedMessageCount != summary.CompactedMessageCount ||
		envelope.PreservedMessageCount != summary.PreservedMessageCount {
		return Summary{}, errors.New("handoff summary envelope binding is invalid")
	}
	expectedDigest := handoffContentSHA256(summary.Content)
	if summary.ContentSHA256 != "" && summary.ContentSHA256 != expectedDigest {
		return Summary{}, errors.New("handoff summary content digest does not match")
	}
	summary.ContentSHA256 = expectedDigest
	summary.TokenEstimate = EstimateTokens(summary.Content)
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = time.Now().UTC()
	}
	return summary, nil
}

func ValidateStoredSummary(summary Summary) error {
	if summary.ProtocolVersion == LegacyHandoffProtocolVersion {
		if strings.TrimSpace(summary.TaskID) == "" || summary.Content == "" ||
			!utf8.ValidString(summary.Content) || summary.SourceMessageCount < 0 ||
			summary.PreservedMessageCount < 0 ||
			summary.PreservedMessageCount > summary.SourceMessageCount {
			return errors.New("legacy context summary is invalid")
		}
		return nil
	}
	_, err := PrepareSummaryForStorage(summary)
	return err
}

func handoffContentSHA256(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func validHandoffDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeHandoffSourceKind(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "legacy_unclassified"
	}
	for _, current := range value {
		if (current < 'a' || current > 'z') && (current < '0' || current > '9') &&
			current != '_' {
			return "legacy_unclassified"
		}
	}
	return value
}

func sanitizeHandoffSourceRef(value string) string {
	return trimRunes(collapseWhitespace(redact.String(value)), MaxHandoffSourceRefChars)
}
