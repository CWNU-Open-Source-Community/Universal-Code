package domain

// ApprovalContinuation is an internal projection of the ordinary execution
// handoff journal. It is not a new user message or a new execution authority.
type ApprovalContinuation struct {
	Handoff             RunExecutionHandoff
	ThreadID            string
	OriginTurn          int
	OriginAttemptID     string
	UserMessageID       int64
	Input               string
	ImageCount          int
	AttachmentCount     int
	ApprovedFileEditIDs []string
}
