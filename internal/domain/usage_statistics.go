package domain

import "time"

// UsageStatistics is a read-only projection of this application's local model
// receipts. Calendar dates use the requested fixed UTC offset.
type UsageStatistics struct {
	GeneratedAt       time.Time          `json:"generated_at"`
	Period            string             `json:"period"`
	UTCOffsetMinutes  int                `json:"utc_offset_minutes"`
	StartDate         string             `json:"start_date"`
	EndDate           string             `json:"end_date"`
	Totals            UsageTotals        `json:"totals"`
	Cost              UsageCost          `json:"cost"`
	UnassignedCalls   int64              `json:"unassigned_calls"`
	Models            []UsageModel       `json:"models"`
	RecentSessions    []UsageSession     `json:"recent_sessions"`
	Days              []UsageDay         `json:"days"`
	ActivityStartDate string             `json:"activity_start_date"`
	ActivityEndDate   string             `json:"activity_end_date"`
	ActivityDays      []UsageActivityDay `json:"activity_days"`
}

// Cache tokens are subsets of input tokens. Coverage counts distinguish absent
// receipts/cache details from explicitly reported zero usage.
type UsageTotals struct {
	InputTokens             int64 `json:"input_tokens"`
	OutputTokens            int64 `json:"output_tokens"`
	TotalTokens             int64 `json:"total_tokens"`
	CacheReadTokens         int64 `json:"cache_read_tokens"`
	CacheCreationTokens     int64 `json:"cache_creation_tokens"`
	Calls                   int64 `json:"calls"`
	UsageReportedCalls      int64 `json:"usage_reported_calls"`
	CacheUsageReportedCalls int64 `json:"cache_usage_reported_calls"`
}

// KnownCostMicros sums only actual priced USD settlements in the settlement
// period. UntrackedCalls counts receipts in the receipt period without a settled
// reservation; the three counts can therefore describe different calendar days.
type UsageCost struct {
	Currency        string `json:"currency"`
	KnownCostMicros int64  `json:"known_cost_micros"`
	PricedCalls     int64  `json:"priced_calls"`
	UnpricedCalls   int64  `json:"unpriced_calls"`
	UntrackedCalls  int64  `json:"untracked_calls"`
}

type UsageModel struct {
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Usage    UsageTotals `json:"usage"`
}

type UsageSession struct {
	ThreadID       string      `json:"thread_id"`
	Title          string      `json:"title"`
	LastActivityAt time.Time   `json:"last_activity_at"`
	Usage          UsageTotals `json:"usage"`
}

type UsageDay struct {
	Date  string      `json:"date"`
	Usage UsageTotals `json:"usage"`
}

type UsageActivityDay struct {
	Date        string `json:"date"`
	Calls       int64  `json:"calls"`
	TotalTokens int64  `json:"total_tokens"`
}
