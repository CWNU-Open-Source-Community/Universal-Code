package domain

// ModelUsage projects persisted provider receipts across all agents in a Run.
// Cache counts are subsets of InputTokens; reported calls distinguish absent
// cache details in older receipts from an explicitly reported zero.
type ModelUsage struct {
	InputTokens             int64 `json:"input_tokens"`
	OutputTokens            int64 `json:"output_tokens"`
	TotalTokens             int64 `json:"total_tokens"`
	CacheReadTokens         int64 `json:"cache_read_tokens"`
	CacheCreationTokens     int64 `json:"cache_creation_tokens"`
	CacheCreation5mTokens   int64 `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens   int64 `json:"cache_creation_1h_tokens"`
	CacheUsageReportedCalls int64 `json:"cache_usage_reported_calls"`
	Calls                   int64 `json:"calls"`
}
