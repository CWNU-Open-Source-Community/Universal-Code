package domain

const ThreadBranchProtocolVersion = "thread_branch.v1"

// ThreadBranchRequest names a durable assistant reply, rather than a mutable
// latest-message position or a browser-generated transcript projection.
type ThreadBranchRequest struct {
	Version       string
	ThreadID      string
	SourceEventID string
	OperationKey  string
	RequestedBy   string
}

type ThreadBranchSource struct {
	RunID string
}

type ThreadBranchRecord struct {
	ThreadID      string `json:"thread_id"`
	RunID         string `json:"run_id"`
	SessionID     string `json:"session_id"`
	SourceEventID string `json:"source_event_id"`
	Replayed      bool   `json:"replayed"`
}
