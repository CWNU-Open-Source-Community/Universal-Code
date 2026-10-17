package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"encoding/json"
)

const ThreadContextCompactionVersion = "thread_context_compaction.v1"

type ThreadContextCompactionRequest struct {
	Version       string `json:"version"`
	ThreadID      string `json:"thread_id"`
	ExpectedRunID string `json:"expected_run_id"`
	Focus         string `json:"focus,omitempty"`
	OperationKey  string `json:"-"`
	RequestedBy   string `json:"-"`
}

func (r ThreadContextCompactionRequest) Normalize() (ThreadContextCompactionRequest, error) {
	r.ThreadID, r.ExpectedRunID = strings.TrimSpace(r.ThreadID), strings.TrimSpace(r.ExpectedRunID)
	r.Focus, r.RequestedBy = strings.TrimSpace(r.Focus), strings.TrimSpace(r.RequestedBy)
	key, err := NormalizeAgentOperationKey(r.OperationKey)
	if err != nil || r.Version != ThreadContextCompactionVersion || !ValidAgentID(r.ThreadID) ||
		!ValidAgentID(r.ExpectedRunID) || !ValidAgentID(r.RequestedBy) || !utf8.ValidString(r.Focus) ||
		strings.ContainsRune(r.Focus, 0) || utf8.RuneCountInString(r.Focus) > 2000 {
		return r, apperror.New(apperror.CodeInvalidArgument, "Thread context compaction request is invalid")
	}
	r.OperationKey = key
	return r, nil
}

type ThreadContextCompactionResult struct {
	Version              string          `json:"version"`
	ThreadID             string          `json:"thread_id"`
	RunID                string          `json:"run_id"`
	SessionID            string          `json:"session_id"`
	Status               string          `json:"status"`
	SummaryID            int64           `json:"summary_id"`
	RemovedMessages      int             `json:"removed_messages"`
	PreservedMessages    int             `json:"preserved_messages"`
	SourceSHA256         string          `json:"source_sha256"`
	InputFingerprint     string          `json:"input_fingerprint,omitempty"`
	InputMessagesOmitted int             `json:"input_messages_omitted"`
	Generated            bool            `json:"generated"`
	Replayed             bool            `json:"replayed"`
	Usage                json.RawMessage `json:"usage,omitempty"`
}

// These are independent operation identities, never Supervisor turns or leases.
type ThreadContextCompactionOperation struct {
	ID                   int64
	OperationID          string
	ThreadID             string
	RunID                string
	SessionID            string
	SourceSHA256         string
	InputFingerprint     string
	InputMessagesOmitted int
	Status               string
	Provider             string
	Model                string
	ResponseText         string
	CompletionSequence   int64
	Usage                json.RawMessage
	Result               ThreadContextCompactionResult
	ErrorCode            apperror.Code
	ErrorMessage         string
	CreatedAt            time.Time
	StartedAt            *time.Time
	Replayed             bool
}

type ThreadContextCompactionSnapshot struct {
	Thread              Thread
	Run                 Run
	Mission             Mission
	Messages            []contextmgr.Message
	Summary             contextmgr.Summary
	HasSummary          bool
	OperatorSequence    int64
	ThreadEventSequence int64
	SourceSHA256        string
}
