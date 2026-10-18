package toolgateway

import (
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
)

const ProjectVerificationReceiptVersion = "project-verification-receipt.v1"

// ProjectVerificationReceipt is host-derived evidence. Reuse requires a fresh
// service check against current source content, actor, backend and native Job.
type ProjectVerificationReceipt struct {
	Version                string                         `json:"version"`
	RunID                  string                         `json:"run_id"`
	AgentID                string                         `json:"agent_id"`
	AgentAttemptID         string                         `json:"agent_attempt_id"`
	WorkspaceID            string                         `json:"workspace_id"`
	Target                 string                         `json:"target"`
	TestFingerprint        string                         `json:"test_fingerprint"`
	SourceVersion          string                         `json:"source_version"`
	CompletedSourceVersion string                         `json:"completed_source_version,omitempty"`
	Adapter                commandruntimeadapter.Identity `json:"adapter"`
	JobID                  string                         `json:"job_id"`
	JobRecordVersion       int64                          `json:"job_record_version"`
	State                  string                         `json:"state"`
	Cursor                 uint64                         `json:"cursor"`
	ExitCode               *int                           `json:"exit_code,omitempty"`
	TreeReaped             bool                           `json:"tree_reaped"`
	CompletedAt            *time.Time                     `json:"completed_at,omitempty"`
	Reusable               bool                           `json:"reusable"`
}
