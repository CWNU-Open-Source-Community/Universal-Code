package domain

// History recall returns historical evidence and currently visible memory. It
// never restores an earlier instruction's authority or replays a tool operation.
const (
	HistoryRecallVersion    = "history_recall.v1"
	HistorySearchScanLimit  = 256
	HistorySearchMaxResults = 20
	HistoryReadDefaultBytes = 4096
	HistoryReadMaxBytes     = 8192
)

type HistorySearchRequest struct {
	Query  string `json:"query"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type HistoryReadRequest struct {
	SourceID       string `json:"source_id"`
	Part           string `json:"part,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type HistoryRecord struct {
	SourceID                      string `json:"source_id"`
	Kind                          string `json:"kind"`
	RunID                         string `json:"run_id"`
	SessionID                     string `json:"session_id"`
	MessageID                     int64  `json:"message_id,omitempty"`
	SummaryID                     int64  `json:"summary_id,omitempty"`
	MemoryID                      string `json:"memory_id,omitempty"`
	NoteID                        string `json:"note_id,omitempty"`
	SourceVersion                 int64  `json:"source_version,omitempty"`
	Scope                         string `json:"scope,omitempty"`
	ScopeID                       string `json:"scope_id,omitempty"`
	Title                         string `json:"title,omitempty"`
	Visibility                    string `json:"visibility,omitempty"`
	PreviousSummaryID             int64  `json:"previous_summary_id,omitempty"`
	ContinuityFingerprint         string `json:"continuity_fingerprint,omitempty"`
	Role                          string `json:"role,omitempty"`
	ProvenanceVersion             string `json:"provenance_version,omitempty"`
	SourceKind                    string `json:"source_kind"`
	SourceRef                     string `json:"source_ref,omitempty"`
	SourceRefSHA256               string `json:"source_ref_sha256,omitempty"`
	SourceRefRedacted             bool   `json:"source_ref_redacted,omitempty"`
	OriginalInstructionAuthorized bool   `json:"original_instruction_authorized"`
	ContentSHA256                 string `json:"content_sha256,omitempty"`
	Compacted                     bool   `json:"compacted,omitempty"`
	CallID                        string `json:"call_id,omitempty"`
	Turn                          int    `json:"turn,omitempty"`
	AttemptID                     string `json:"attempt_id,omitempty"`
	Round                         int    `json:"round,omitempty"`
	ToolName                      string `json:"tool_name,omitempty"`
	Status                        string `json:"status,omitempty"`
	ErrorCode                     string `json:"error_code,omitempty"`
	ArgumentsSHA256               string `json:"arguments_sha256,omitempty"`
	ResultSHA256                  string `json:"result_sha256,omitempty"`
	CreatedAt                     string `json:"created_at"`
	UpdatedAt                     string `json:"updated_at,omitempty"`
	RetentionUntil                string `json:"retention_until,omitempty"`
	CompletedAt                   string `json:"completed_at,omitempty"`
	MatchedPart                   string `json:"matched_part,omitempty"`
	Excerpt                       string `json:"excerpt,omitempty"`
}

type HistorySearchResult struct {
	Version               string          `json:"version"`
	ThreadID              string          `json:"thread_id"`
	RunID                 string          `json:"run_id"`
	InstructionAuthorized bool            `json:"instruction_authorized"`
	SearchMode            string          `json:"search_mode"`
	Records               []HistoryRecord `json:"records"`
	Scanned               int             `json:"scanned"`
	ScanLimit             int             `json:"scan_limit"`
	HasMore               bool            `json:"has_more"`
	NextCursor            string          `json:"next_cursor,omitempty"`
	SnapshotCursor        string          `json:"snapshot_cursor"`
}

type HistoryReadResult struct {
	Version               string        `json:"version"`
	ThreadID              string        `json:"thread_id"`
	RunID                 string        `json:"run_id"`
	InstructionAuthorized bool          `json:"instruction_authorized"`
	Record                HistoryRecord `json:"record"`
	Part                  string        `json:"part"`
	Content               string        `json:"content"`
	ContentSHA256         string        `json:"content_sha256"`
	Offset                int           `json:"offset"`
	NextOffset            int           `json:"next_offset"`
	TotalBytes            int           `json:"total_bytes"`
	HasMore               bool          `json:"has_more"`
}
