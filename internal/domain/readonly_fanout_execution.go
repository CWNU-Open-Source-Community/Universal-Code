package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	ReadOnlyFanoutReportVersion           = "readonly_fanout_report.v1"
	DefaultReadOnlyFanoutMaxOutputTokens  = 1024
	MinReadOnlyFanoutMaxOutputTokens      = 128
	MaxReadOnlyFanoutMaxOutputTokens      = 4096
	MaxReadOnlyFanoutFindings             = 32
	MaxReadOnlyFanoutReportBytes          = 128 * 1024
	MaxReadOnlyFanoutSummaryRunes         = 4096
	MaxReadOnlyFanoutFindingTitleRunes    = 256
	MaxReadOnlyFanoutFindingDetailRunes   = 2048
	MaxReadOnlyFanoutFindingCategoryRunes = 64
	MaxReadOnlyFanoutAttemptsPerShard     = 3
)

type ReadOnlyFindingSeverity string

const (
	ReadOnlyFindingInfo     ReadOnlyFindingSeverity = "info"
	ReadOnlyFindingLow      ReadOnlyFindingSeverity = "low"
	ReadOnlyFindingMedium   ReadOnlyFindingSeverity = "medium"
	ReadOnlyFindingHigh     ReadOnlyFindingSeverity = "high"
	ReadOnlyFindingCritical ReadOnlyFindingSeverity = "critical"
)

func (s ReadOnlyFindingSeverity) Valid() bool {
	switch s {
	case ReadOnlyFindingInfo, ReadOnlyFindingLow, ReadOnlyFindingMedium,
		ReadOnlyFindingHigh, ReadOnlyFindingCritical:
		return true
	default:
		return false
	}
}

type ReadOnlyFanoutFinding struct {
	Severity   ReadOnlyFindingSeverity `json:"severity"`
	Category   string                  `json:"category"`
	Title      string                  `json:"title"`
	Detail     string                  `json:"detail"`
	Path       string                  `json:"path"`
	LineStart  int                     `json:"line_start"`
	LineEnd    int                     `json:"line_end"`
	Confidence int                     `json:"confidence"`
}

func (f ReadOnlyFanoutFinding) Validate(allowedPaths map[string]struct{}) error {
	if !f.Severity.Valid() || !validReadOnlyReportText(f.Category,
		MaxReadOnlyFanoutFindingCategoryRunes) ||
		!validReadOnlyReportText(f.Title, MaxReadOnlyFanoutFindingTitleRunes) ||
		!validReadOnlyReportText(f.Detail, MaxReadOnlyFanoutFindingDetailRunes) ||
		!validReadOnlyFanoutPath(f.Path) || f.LineStart < 0 || f.LineEnd < f.LineStart ||
		f.Confidence < 0 || f.Confidence > 100 {
		return errors.New("read-only fan-out finding is invalid")
	}
	if _, allowed := allowedPaths[f.Path]; !allowed {
		return errors.New("read-only fan-out finding references a file outside its shard")
	}
	return nil
}

type ReadOnlyFanoutReport struct {
	Version  string                  `json:"version"`
	Summary  string                  `json:"summary"`
	Findings []ReadOnlyFanoutFinding `json:"findings"`
}

func (r ReadOnlyFanoutReport) Normalize(allowedPaths map[string]struct{},
) (ReadOnlyFanoutReport, error) {
	r.Version = strings.TrimSpace(r.Version)
	r.Summary = strings.TrimSpace(redact.String(r.Summary))
	r.Findings = slices.Clone(r.Findings)
	for index := range r.Findings {
		finding := &r.Findings[index]
		finding.Severity = ReadOnlyFindingSeverity(strings.ToLower(strings.TrimSpace(
			string(finding.Severity))))
		finding.Category = strings.TrimSpace(redact.String(finding.Category))
		finding.Title = strings.TrimSpace(redact.String(finding.Title))
		finding.Detail = strings.TrimSpace(redact.String(finding.Detail))
		finding.Path = strings.TrimSpace(finding.Path)
	}
	if err := r.Validate(allowedPaths); err != nil {
		return ReadOnlyFanoutReport{}, err
	}
	return r, nil
}

func (r ReadOnlyFanoutReport) Validate(allowedPaths map[string]struct{}) error {
	if r.Version != ReadOnlyFanoutReportVersion ||
		!validReadOnlyReportText(r.Summary, MaxReadOnlyFanoutSummaryRunes) ||
		len(r.Findings) > MaxReadOnlyFanoutFindings || len(allowedPaths) == 0 {
		return errors.New("read-only fan-out report metadata is invalid")
	}
	for _, finding := range r.Findings {
		if err := finding.Validate(allowedPaths); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > MaxReadOnlyFanoutReportBytes {
		return errors.New("read-only fan-out report exceeds its byte limit")
	}
	return nil
}

func DecodeReadOnlyFanoutReport(text string,
	allowedPaths map[string]struct{},
) (ReadOnlyFanoutReport, error) {
	if !utf8.ValidString(text) || len([]byte(text)) == 0 ||
		len([]byte(text)) > MaxReadOnlyFanoutReportBytes {
		return ReadOnlyFanoutReport{}, errors.New(
			"read-only fan-out report must be bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	var report ReadOnlyFanoutReport
	if err := decoder.Decode(&report); err != nil {
		return ReadOnlyFanoutReport{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ReadOnlyFanoutReport{}, errors.New(
			"read-only fan-out report contains trailing data")
	}
	return report.Normalize(allowedPaths)
}

func EncodeReadOnlyFanoutReport(report ReadOnlyFanoutReport,
	allowedPaths map[string]struct{},
) (string, error) {
	normalized, err := report.Normalize(allowedPaths)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func ReadOnlyFanoutReportDigest(reportJSON string) (string, error) {
	if !utf8.ValidString(reportJSON) || len(reportJSON) == 0 ||
		len([]byte(reportJSON)) > MaxReadOnlyFanoutReportBytes ||
		!json.Valid([]byte(reportJSON)) {
		return "", errors.New("read-only fan-out report JSON is invalid")
	}
	digest := sha256.Sum256([]byte(reportJSON))
	return hex.EncodeToString(digest[:]), nil
}

func ReadOnlyFanoutFindingFingerprint(executionID string, shardOrdinal int,
	finding ReadOnlyFanoutFinding,
) (string, error) {
	if !validAgentIdentity(strings.TrimSpace(executionID), false) ||
		shardOrdinal <= 0 || shardOrdinal > MaxReadOnlyFanoutParallelism {
		return "", errors.New("read-only fan-out finding scope is invalid")
	}
	allowed := map[string]struct{}{finding.Path: {}}
	if err := finding.Validate(allowed); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(finding)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte("readonly_fanout_finding.v1"), []byte(executionID),
		[]byte(fmt.Sprintf("%d", shardOrdinal)), encoded,
	}, []byte{0}))
	return hex.EncodeToString(digest[:]), nil
}

type ReadOnlyFanoutExecutionStatus string

const (
	ReadOnlyFanoutExecutionRunning   ReadOnlyFanoutExecutionStatus = "running"
	ReadOnlyFanoutExecutionCompleted ReadOnlyFanoutExecutionStatus = "completed"
	ReadOnlyFanoutExecutionFailed    ReadOnlyFanoutExecutionStatus = "failed"
	ReadOnlyFanoutExecutionCancelled ReadOnlyFanoutExecutionStatus = "cancelled"
)

func (s ReadOnlyFanoutExecutionStatus) Valid() bool {
	return s == ReadOnlyFanoutExecutionRunning || s == ReadOnlyFanoutExecutionCompleted ||
		s == ReadOnlyFanoutExecutionFailed || s == ReadOnlyFanoutExecutionCancelled
}

func (s ReadOnlyFanoutExecutionStatus) Terminal() bool {
	return s == ReadOnlyFanoutExecutionCompleted || s == ReadOnlyFanoutExecutionFailed ||
		s == ReadOnlyFanoutExecutionCancelled
}

type ReadOnlyFanoutExecutionShardStatus string

const (
	ReadOnlyFanoutExecutionShardPending   ReadOnlyFanoutExecutionShardStatus = "pending"
	ReadOnlyFanoutExecutionShardRunning   ReadOnlyFanoutExecutionShardStatus = "running"
	ReadOnlyFanoutExecutionShardCompleted ReadOnlyFanoutExecutionShardStatus = "completed"
	ReadOnlyFanoutExecutionShardFailed    ReadOnlyFanoutExecutionShardStatus = "failed"
	ReadOnlyFanoutExecutionShardCancelled ReadOnlyFanoutExecutionShardStatus = "cancelled"
)

func (s ReadOnlyFanoutExecutionShardStatus) Valid() bool {
	switch s {
	case ReadOnlyFanoutExecutionShardPending, ReadOnlyFanoutExecutionShardRunning,
		ReadOnlyFanoutExecutionShardCompleted, ReadOnlyFanoutExecutionShardFailed,
		ReadOnlyFanoutExecutionShardCancelled:
		return true
	default:
		return false
	}
}

func (s ReadOnlyFanoutExecutionShardStatus) Terminal() bool {
	return s == ReadOnlyFanoutExecutionShardCompleted ||
		s == ReadOnlyFanoutExecutionShardFailed ||
		s == ReadOnlyFanoutExecutionShardCancelled
}

type ReadOnlyFanoutExecutionShard struct {
	ExecutionID    string
	PlanID         string
	Ordinal        int
	Status         ReadOnlyFanoutExecutionShardStatus
	InputDigest    string
	AttemptCount   int
	CurrentAttempt int
	Provider       string
	Model          string
	InputTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	ElapsedMillis  int64
	ReportJSON     string
	ReportDigest   string
	FindingCount   int
	ErrorCode      string
	ErrorReason    string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

func (s ReadOnlyFanoutExecutionShard) Validate() error {
	for _, value := range []string{s.ExecutionID, s.PlanID} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out execution shard identities are invalid")
		}
	}
	if s.Ordinal <= 0 || s.Ordinal > MaxReadOnlyFanoutParallelism || !s.Status.Valid() ||
		!validLowerHexDigest(s.InputDigest) || s.AttemptCount < 0 ||
		s.AttemptCount > MaxReadOnlyFanoutAttemptsPerShard ||
		s.CurrentAttempt < 0 || s.CurrentAttempt > s.AttemptCount || s.InputTokens < 0 ||
		s.OutputTokens < 0 || s.TotalTokens < 0 ||
		s.InputTokens > int64(^uint64(0)>>1)-s.OutputTokens ||
		s.TotalTokens < s.InputTokens+s.OutputTokens || s.ElapsedMillis < 0 ||
		s.FindingCount < 0 || s.FindingCount > MaxReadOnlyFanoutFindings ||
		s.Version <= 0 || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return errors.New("read-only fan-out execution shard state is invalid")
	}
	switch s.Status {
	case ReadOnlyFanoutExecutionShardPending:
		if s.CurrentAttempt != 0 || s.Provider != "" || s.Model != "" ||
			s.ReportJSON != "" || s.ReportDigest != "" || s.ErrorCode != "" ||
			s.ErrorReason != "" || s.StartedAt != nil || s.FinishedAt != nil {
			return errors.New("pending read-only fan-out shard has execution metadata")
		}
	case ReadOnlyFanoutExecutionShardRunning:
		if s.AttemptCount <= 0 || s.CurrentAttempt != s.AttemptCount ||
			s.StartedAt == nil || s.FinishedAt != nil || s.Provider != "" ||
			s.Model != "" || s.ReportJSON != "" || s.ErrorCode != "" {
			return errors.New("running read-only fan-out shard is invalid")
		}
	case ReadOnlyFanoutExecutionShardCompleted:
		reportDigest, reportErr := ReadOnlyFanoutReportDigest(s.ReportJSON)
		if !validReadOnlyFanoutTerminalShard(s) || reportErr != nil ||
			reportDigest != s.ReportDigest || s.ErrorCode != "" ||
			s.ErrorReason != "" {
			return errors.New("completed read-only fan-out shard is invalid")
		}
	case ReadOnlyFanoutExecutionShardFailed:
		if !validReadOnlyFanoutTerminalShard(s) || s.ReportJSON != "" ||
			s.ReportDigest != "" || s.FindingCount != 0 || s.ErrorCode == "" ||
			!validReadOnlyReportText(s.ErrorReason, MaxReadOnlyFanoutFindingDetailRunes) {
			return errors.New("failed read-only fan-out shard is invalid")
		}
	case ReadOnlyFanoutExecutionShardCancelled:
		if !validReadOnlyFanoutCancelledShard(s) || s.ReportJSON != "" ||
			s.ReportDigest != "" || s.FindingCount != 0 || s.ErrorCode == "" ||
			!validReadOnlyReportText(s.ErrorReason, MaxReadOnlyFanoutFindingDetailRunes) {
			return errors.New("cancelled read-only fan-out shard is invalid")
		}
	}
	return nil
}

func validReadOnlyFanoutTerminalShard(s ReadOnlyFanoutExecutionShard) bool {
	return s.AttemptCount > 0 && s.CurrentAttempt == s.AttemptCount &&
		s.StartedAt != nil && s.FinishedAt != nil &&
		!s.FinishedAt.Before(*s.StartedAt) && s.Provider != "" && s.Model != ""
}

func validReadOnlyFanoutCancelledShard(s ReadOnlyFanoutExecutionShard) bool {
	if s.AttemptCount > 0 && s.CurrentAttempt == s.AttemptCount {
		return validReadOnlyFanoutTerminalShard(s)
	}
	return s.CurrentAttempt == 0 && s.Provider == "" && s.Model == "" &&
		s.InputTokens == 0 && s.OutputTokens == 0 && s.TotalTokens == 0 &&
		s.ElapsedMillis == 0 && s.StartedAt == nil && s.FinishedAt != nil &&
		!s.FinishedAt.Before(s.CreatedAt)
}

type ReadOnlyFanoutExecution struct {
	ID                      string
	PlanID                  string
	RunID                   string
	WorkspaceID             string
	Status                  ReadOnlyFanoutExecutionStatus
	Parallelism             int
	MaxOutputTokensPerShard int
	SnapshotDigest          string
	RequestedBy             string
	StopCode                string
	Version                 int64
	StartedAt               time.Time
	UpdatedAt               time.Time
	FinishedAt              *time.Time
	Shards                  []ReadOnlyFanoutExecutionShard
}

type ReadOnlyFanoutExecutionShardSummary struct {
	ExecutionID    string
	PlanID         string
	Ordinal        int
	Status         ReadOnlyFanoutExecutionShardStatus
	AttemptCount   int
	CurrentAttempt int
	Provider       string
	Model          string
	InputTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	ElapsedMillis  int64
	FindingCount   int
	ErrorCode      string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

type ReadOnlyFanoutExecutionSummary struct {
	ID                      string
	PlanID                  string
	RunID                   string
	WorkspaceID             string
	Status                  ReadOnlyFanoutExecutionStatus
	Parallelism             int
	MaxOutputTokensPerShard int
	RequestedBy             string
	StopCode                string
	Version                 int64
	StartedAt               time.Time
	UpdatedAt               time.Time
	FinishedAt              *time.Time
	Shards                  []ReadOnlyFanoutExecutionShardSummary
}

func (e ReadOnlyFanoutExecutionSummary) Validate() error {
	for _, value := range []string{e.ID, e.PlanID, e.RunID, e.WorkspaceID, e.RequestedBy} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out execution summary identities are invalid")
		}
	}
	if !validReadOnlyFanoutSummaryLabel(e.StopCode, true) {
		return errors.New("read-only fan-out execution summary stop code is invalid")
	}
	if !e.Status.Valid() || e.Parallelism <= 0 || e.Parallelism > MaxReadOnlyFanoutParallelism ||
		e.MaxOutputTokensPerShard < MinReadOnlyFanoutMaxOutputTokens ||
		e.MaxOutputTokensPerShard > MaxReadOnlyFanoutMaxOutputTokens || e.Version <= 0 ||
		e.StartedAt.IsZero() || e.UpdatedAt.Before(e.StartedAt) || len(e.Shards) != e.Parallelism {
		return errors.New("read-only fan-out execution summary metadata is invalid")
	}
	allCompleted := true
	allTerminal := true
	for index, shard := range e.Shards {
		if shard.ExecutionID != e.ID || shard.PlanID != e.PlanID || shard.Ordinal != index+1 ||
			!shard.Status.Valid() || shard.AttemptCount < 0 ||
			shard.AttemptCount > MaxReadOnlyFanoutAttemptsPerShard ||
			shard.CurrentAttempt < 0 || shard.CurrentAttempt > shard.AttemptCount ||
			shard.InputTokens < 0 || shard.OutputTokens < 0 || shard.TotalTokens < 0 ||
			shard.InputTokens > int64(^uint64(0)>>1)-shard.OutputTokens ||
			shard.TotalTokens < shard.InputTokens+shard.OutputTokens || shard.ElapsedMillis < 0 ||
			shard.FindingCount < 0 || shard.FindingCount > MaxReadOnlyFanoutFindings ||
			shard.Version <= 0 || shard.CreatedAt.IsZero() || shard.UpdatedAt.Before(shard.CreatedAt) ||
			!validReadOnlyFanoutSummaryLabel(shard.Provider, true) ||
			!validReadOnlyFanoutSummaryLabel(shard.Model, true) ||
			!validReadOnlyFanoutSummaryLabel(shard.ErrorCode, true) {
			return errors.New("read-only fan-out execution shard summary is invalid")
		}
		switch shard.Status {
		case ReadOnlyFanoutExecutionShardPending:
			if shard.CurrentAttempt != 0 || shard.Provider != "" ||
				shard.Model != "" || shard.ErrorCode != "" || shard.InputTokens != 0 ||
				shard.OutputTokens != 0 || shard.TotalTokens != 0 || shard.ElapsedMillis != 0 ||
				shard.FindingCount != 0 || shard.StartedAt != nil || shard.FinishedAt != nil {
				return errors.New("pending read-only fan-out execution shard summary is invalid")
			}
		case ReadOnlyFanoutExecutionShardRunning:
			if shard.AttemptCount <= 0 || shard.CurrentAttempt != shard.AttemptCount ||
				shard.StartedAt == nil || shard.StartedAt.Before(shard.CreatedAt) ||
				shard.FinishedAt != nil || shard.Provider != "" || shard.Model != "" ||
				shard.ErrorCode != "" || shard.FindingCount != 0 {
				return errors.New("running read-only fan-out execution shard summary is invalid")
			}
		case ReadOnlyFanoutExecutionShardCompleted:
			if !validReadOnlyFanoutTerminalSummaryShard(shard) || shard.ErrorCode != "" {
				return errors.New("completed read-only fan-out execution shard summary is invalid")
			}
		case ReadOnlyFanoutExecutionShardFailed:
			if !validReadOnlyFanoutTerminalSummaryShard(shard) || shard.ErrorCode == "" ||
				shard.FindingCount != 0 {
				return errors.New("failed read-only fan-out execution shard summary is invalid")
			}
		case ReadOnlyFanoutExecutionShardCancelled:
			attempted := validReadOnlyFanoutTerminalSummaryShard(shard)
			beforeStart := shard.CurrentAttempt == 0 &&
				shard.Provider == "" && shard.Model == "" && shard.InputTokens == 0 &&
				shard.OutputTokens == 0 && shard.TotalTokens == 0 && shard.ElapsedMillis == 0 &&
				shard.StartedAt == nil && shard.FinishedAt != nil &&
				!shard.FinishedAt.Before(shard.CreatedAt)
			if (!attempted && !beforeStart) || shard.ErrorCode == "" || shard.FindingCount != 0 {
				return errors.New("terminal read-only fan-out execution shard summary is invalid")
			}
		}
		allCompleted = allCompleted && shard.Status == ReadOnlyFanoutExecutionShardCompleted
		allTerminal = allTerminal && shard.Status.Terminal()
	}
	if e.Status == ReadOnlyFanoutExecutionRunning {
		if e.FinishedAt != nil || e.StopCode != "" {
			return errors.New("running read-only fan-out execution summary has terminal metadata")
		}
		return nil
	}
	if e.FinishedAt == nil || e.FinishedAt.Before(e.StartedAt) ||
		!e.UpdatedAt.Equal(*e.FinishedAt) || !allTerminal {
		return errors.New("terminal read-only fan-out execution summary is incomplete")
	}
	if e.Status == ReadOnlyFanoutExecutionCompleted {
		if !allCompleted || e.StopCode != "" {
			return errors.New("completed read-only fan-out execution summary contains failures")
		}
	} else if allCompleted || strings.TrimSpace(e.StopCode) == "" {
		return errors.New("failed read-only fan-out execution summary requires a stop code")
	}
	return nil
}

func validReadOnlyFanoutTerminalSummaryShard(
	shard ReadOnlyFanoutExecutionShardSummary,
) bool {
	return shard.AttemptCount > 0 && shard.CurrentAttempt == shard.AttemptCount &&
		shard.StartedAt != nil && !shard.StartedAt.Before(shard.CreatedAt) &&
		shard.FinishedAt != nil && !shard.FinishedAt.Before(*shard.StartedAt) &&
		shard.Provider != "" && shard.Model != ""
}

func validReadOnlyFanoutSummaryLabel(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= 256 &&
		len([]byte(value)) <= 1024
}

func (e ReadOnlyFanoutExecution) Validate() error {
	for _, value := range []string{e.ID, e.PlanID, e.RunID, e.WorkspaceID,
		e.RequestedBy} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out execution identities are invalid")
		}
	}
	if !e.Status.Valid() || e.Parallelism <= 0 ||
		e.Parallelism > MaxReadOnlyFanoutParallelism ||
		e.MaxOutputTokensPerShard < MinReadOnlyFanoutMaxOutputTokens ||
		e.MaxOutputTokensPerShard > MaxReadOnlyFanoutMaxOutputTokens ||
		!validLowerHexDigest(e.SnapshotDigest) || e.Version <= 0 ||
		e.StartedAt.IsZero() || e.UpdatedAt.Before(e.StartedAt) ||
		len(e.Shards) != e.Parallelism {
		return errors.New("read-only fan-out execution state is invalid")
	}
	allCompleted := true
	allTerminal := true
	for index, shard := range e.Shards {
		if err := shard.Validate(); err != nil {
			return err
		}
		if shard.ExecutionID != e.ID || shard.PlanID != e.PlanID ||
			shard.Ordinal != index+1 {
			return errors.New("read-only fan-out execution shards are not contiguous")
		}
		allCompleted = allCompleted && shard.Status == ReadOnlyFanoutExecutionShardCompleted
		allTerminal = allTerminal && shard.Status.Terminal()
	}
	if e.Status == ReadOnlyFanoutExecutionRunning {
		if e.FinishedAt != nil || e.StopCode != "" {
			return errors.New("running read-only fan-out execution has terminal metadata")
		}
		return nil
	}
	if e.FinishedAt == nil || e.FinishedAt.Before(e.StartedAt) ||
		!e.UpdatedAt.Equal(*e.FinishedAt) || !allTerminal {
		return errors.New("terminal read-only fan-out execution is incomplete")
	}
	if e.Status == ReadOnlyFanoutExecutionCompleted {
		if !allCompleted || e.StopCode != "" {
			return errors.New("completed read-only fan-out execution contains failures")
		}
	} else if allCompleted || strings.TrimSpace(e.StopCode) == "" {
		return errors.New("failed read-only fan-out execution requires a stop code")
	}
	return nil
}

type ReadOnlyFanoutExecutionOperation struct {
	KeyDigest          string
	RequestFingerprint string
	ExecutionID        string
	PlanID             string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

func (o ReadOnlyFanoutExecutionOperation) Validate() error {
	for _, value := range []string{o.ExecutionID, o.PlanID, o.RunID, o.RequestedBy} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out execution operation identities are invalid")
		}
	}
	if !validLowerHexDigest(o.KeyDigest) || !validLowerHexDigest(o.RequestFingerprint) ||
		o.CreatedAt.IsZero() {
		return errors.New("read-only fan-out execution operation is invalid")
	}
	return nil
}

func validReadOnlyReportText(value string, maxRunes int) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maxRunes &&
		len([]byte(value)) <= maxRunes*4
}
