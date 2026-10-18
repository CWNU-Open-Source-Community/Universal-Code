package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	CompletionReportVersion         = "agent_completion.v1"
	MaxCompletionSummaryRunes       = 4096
	MaxCompletionSummaryBytes       = 8 * 1024
	MaxCompletionWorkItemReferences = 16
	MaxCompletionNoteReferences     = 16
	MaxCompletionReferenceBytes     = 256
	MaxCompletionDataBytes          = 4 * 1024
	MaxCompletionDataDepth          = 8
)

type CompletionOutcome string

const (
	CompletionSucceeded CompletionOutcome = "succeeded"
	CompletionPartial   CompletionOutcome = "partial"
)

// CompletionReport is the versioned child-to-parent result contract.
type CompletionReport struct {
	Version     string            `json:"version"`
	Outcome     CompletionOutcome `json:"outcome"`
	Summary     string            `json:"summary"`
	WorkItemIDs []string          `json:"work_item_ids"`
	NoteIDs     []string          `json:"note_ids"`
	// Data is bounded, untrusted result data; it grants no execution authority.
	Data json.RawMessage `json:"data,omitempty"`
}

type AgentCompletion struct {
	ID            string
	RunID         string
	AgentID       string
	ParentAgentID string
	AttemptID     string
	Report        CompletionReport
	MessageID     string
	CreatedAt     time.Time
}

func ValidCompletionOutcome(outcome CompletionOutcome) bool {
	return outcome == CompletionSucceeded || outcome == CompletionPartial
}

func NormalizeCompletionReport(report CompletionReport) (CompletionReport, error) {
	report.Version = strings.TrimSpace(report.Version)
	report.Outcome = CompletionOutcome(strings.ToLower(strings.TrimSpace(string(report.Outcome))))
	report.Summary = strings.TrimSpace(report.Summary)
	var err error
	report.WorkItemIDs, err = normalizeCompletionReferences(report.WorkItemIDs,
		MaxCompletionWorkItemReferences)
	if err != nil {
		return CompletionReport{}, fmt.Errorf("completion work item references are invalid: %w", err)
	}
	report.NoteIDs, err = normalizeCompletionReferences(report.NoteIDs, MaxCompletionNoteReferences)
	if err != nil {
		return CompletionReport{}, fmt.Errorf("completion note references are invalid: %w", err)
	}
	report.Data, err = normalizeCompletionData(report.Data)
	if err != nil {
		return CompletionReport{}, err
	}
	if err := report.Validate(); err != nil {
		return CompletionReport{}, err
	}
	return report, nil
}

func (r CompletionReport) Validate() error {
	if r.Version != CompletionReportVersion {
		return fmt.Errorf("unsupported completion report version %q", r.Version)
	}
	if !ValidCompletionOutcome(r.Outcome) {
		return fmt.Errorf("invalid completion outcome %q", r.Outcome)
	}
	if !utf8.ValidString(r.Summary) || strings.TrimSpace(r.Summary) != r.Summary || r.Summary == "" ||
		strings.ContainsRune(r.Summary, 0) || utf8.RuneCountInString(r.Summary) > MaxCompletionSummaryRunes ||
		len([]byte(r.Summary)) > MaxCompletionSummaryBytes {
		return fmt.Errorf("completion summary must contain between 1 and %d characters within %d bytes",
			MaxCompletionSummaryRunes, MaxCompletionSummaryBytes)
	}
	if r.WorkItemIDs == nil || r.NoteIDs == nil {
		return errors.New("completion reference lists must be present")
	}
	workItems, err := normalizeCompletionReferences(r.WorkItemIDs, MaxCompletionWorkItemReferences)
	if err != nil || !slices.Equal(workItems, r.WorkItemIDs) {
		return errors.New("completion work item references must be normalized, unique, and sorted")
	}
	notes, err := normalizeCompletionReferences(r.NoteIDs, MaxCompletionNoteReferences)
	if err != nil || !slices.Equal(notes, r.NoteIDs) {
		return errors.New("completion note references must be normalized, unique, and sorted")
	}
	if _, err := normalizeCompletionData(r.Data); err != nil {
		return err
	}
	return nil
}

// RedactCompletionReport applies the shared JSON credential and value redactor
// after checking the original bounds, then checks the redacted report's bounds.
func RedactCompletionReport(report CompletionReport) (CompletionReport, error) {
	report, err := NormalizeCompletionReport(report)
	if err != nil {
		return CompletionReport{}, err
	}
	report.Summary = redact.String(report.Summary)
	if len(report.Data) > 0 {
		report.Data, err = redact.SanitizeSensitiveJSON(report.Data)
		if err != nil {
			return CompletionReport{}, err
		}
	}
	return NormalizeCompletionReport(report)
}

func normalizeCompletionData(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxCompletionDataBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, fmt.Errorf("completion data must be valid UTF-8 JSON within %d bytes", MaxCompletionDataBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("completion data must be a JSON object or null")
	}
	if err := validateCompletionDataDepth(value, 1); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(canonical) > MaxCompletionDataBytes {
		return nil, fmt.Errorf("completion data exceeds %d bytes", MaxCompletionDataBytes)
	}
	return canonical, nil
}

// The root object has depth one; nested objects and arrays each add one.
func validateCompletionDataDepth(value any, depth int) error {
	switch typed := value.(type) {
	case map[string]any:
		if depth > MaxCompletionDataDepth {
			return fmt.Errorf("completion data exceeds JSON depth %d", MaxCompletionDataDepth)
		}
		for _, child := range typed {
			if err := validateCompletionDataDepth(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if depth > MaxCompletionDataDepth {
			return fmt.Errorf("completion data exceeds JSON depth %d", MaxCompletionDataDepth)
		}
		for _, child := range typed {
			if err := validateCompletionDataDepth(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func DecodeCompletionReport(payloadJSON string) (CompletionReport, error) {
	var report CompletionReport
	if err := decodeStrictAgentPayload(payloadJSON, &report); err != nil {
		return CompletionReport{}, fmt.Errorf("invalid completion report: %w", err)
	}
	report, err := NormalizeCompletionReport(report)
	if err != nil {
		return CompletionReport{}, fmt.Errorf("invalid completion report: %w", err)
	}
	return report, nil
}

func (c AgentCompletion) Validate() error {
	for _, value := range []string{c.ID, c.RunID, c.AgentID, c.ParentAgentID, c.AttemptID, c.MessageID} {
		if !validAgentIdentity(value, false) {
			return errors.New("completion identities are required and must be normalized")
		}
	}
	if c.AgentID == c.ParentAgentID {
		return errors.New("completion child and parent identities must differ")
	}
	if err := c.Report.Validate(); err != nil {
		return err
	}
	if c.CreatedAt.IsZero() {
		return errors.New("completion creation time is required")
	}
	return nil
}

func normalizeCompletionReferences(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("reference count exceeds %d", limit)
	}
	unique := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) ||
			len([]byte(value)) > MaxCompletionReferenceBytes {
			return nil, fmt.Errorf("reference must be normalized UTF-8 within %d bytes",
				MaxCompletionReferenceBytes)
		}
		unique[value] = struct{}{}
	}
	out := make([]string, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, nil
}
