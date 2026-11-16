package domain

const (
	ThreadFailureToolRequestRejected    = "tool_request_rejected"
	ThreadFailureEmptyModelResponse     = "empty_model_response"
	ThreadFailureInvalidModelResponse   = "invalid_model_response"
	ThreadFailureContextWindowExceeded  = "context_window_exceeded"
	ThreadFailureFullPermissionNotReady = "full_permission_not_ready"
)

// ThreadTurnFailure is the immutable product-turn outcome. Committing its
// operator message records consumption into the conversation, not success.
type ThreadTurnFailure struct {
	ThreadID           string `json:"thread_id"`
	RunID              string `json:"run_id"`
	HandoffOperationID string `json:"handoff_operation_id"`
	MessageID          string `json:"message_id"`
	AttemptID          string `json:"attempt_id"`
	Turn               int    `json:"turn"`
	UserMessageID      int64  `json:"user_message_id"`
	OutcomeMessageID   int64  `json:"outcome_message_id"`
	ErrorCode          string `json:"error_code"`
	FailureStage       string `json:"failure_stage,omitempty"`
	EventSequence      int64  `json:"-"`
}
