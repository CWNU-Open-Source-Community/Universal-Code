package domain

// ThreadStatistics describes the root agent across every Run in a Thread.
// Input tokens include cache reads. TimedOutputTokens includes only receipts
// with a measured generation duration; historical receipts stay untimed.
type ThreadStatistics struct {
	Turns              int64               `json:"turns"`
	Steps              int64               `json:"steps"`
	InputTokens        int64               `json:"input_tokens"`
	OutputTokens       int64               `json:"output_tokens"`
	TotalTokens        int64               `json:"total_tokens"`
	CacheReadTokens    int64               `json:"cache_read_tokens"`
	CacheReportedCalls int64               `json:"cache_reported_calls"`
	UsageCalls         int64               `json:"usage_calls"`
	TimedOutputTokens  int64               `json:"timed_output_tokens"`
	DecodeMillis       int64               `json:"decode_millis"`
	Context            *ThreadContextUsage `json:"context,omitempty"`
}

// ThreadContextUsage is the most recent root request's saved window estimate.
type ThreadContextUsage struct {
	InputTokens  int64  `json:"input_tokens"`
	WindowTokens int64  `json:"window_tokens"`
	Estimated    bool   `json:"estimated"`
	WindowSource string `json:"window_source"`
}
