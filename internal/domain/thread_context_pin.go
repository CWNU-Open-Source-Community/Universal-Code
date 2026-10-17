package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ThreadContextPinsVersion      = "thread_context_pins.v1"
	ContextPinProposalVersion     = "context_pin_proposal.v1"
	MaxThreadContextPins          = 32
	MaxThreadContextPinRunes      = 8192
	MaxThreadContextPinTotalRunes = 32768
	MaxPendingContextPinProposals = 16
)

type ThreadContextPin struct {
	ID              string    `json:"id"`
	ThreadID        string    `json:"thread_id"`
	Text            string    `json:"text"`
	Version         int64     `json:"version"`
	Status          string    `json:"status"`
	ContentSHA256   string    `json:"content_sha256"`
	SourceKind      string    `json:"source_kind"`
	SourceMessageID int64     `json:"source_message_id,omitempty"`
	ProposalID      string    `json:"proposal_id,omitempty"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ContextPinProposalSpec struct {
	Version         string `json:"version"`
	Text            string `json:"text"`
	Question        string `json:"question,omitempty"`
	SourceMessageID int64  `json:"source_message_id,omitempty"`
}

func (s ContextPinProposalSpec) Validate() error {
	if s.Version != ContextPinProposalVersion {
		return errors.New("context pin proposal version is invalid")
	}
	if err := ValidateContextPinText(s.Text); err != nil {
		return err
	}
	if !utf8.ValidString(s.Question) || utf8.RuneCountInString(s.Question) > 1024 || strings.ContainsRune(s.Question, 0) {
		return errors.New("context pin question must be valid text of at most 1024 characters")
	}
	if s.SourceMessageID < 0 {
		return errors.New("context pin source message id is invalid")
	}
	return nil
}

type ContextPinProposal struct {
	ID                string     `json:"id"`
	ThreadID          string     `json:"thread_id"`
	RunID             string     `json:"run_id"`
	Version           int64      `json:"version"`
	Status            string     `json:"status"`
	Text              string     `json:"text"`
	Question          string     `json:"question"`
	SourceMessageID   int64      `json:"source_message_id,omitempty"`
	ContentSHA256     string     `json:"content_sha256"`
	Choice            string     `json:"choice,omitempty"`
	PinID             string     `json:"pin_id,omitempty"`
	ReplyText         string     `json:"reply_text,omitempty"`
	ReplyOperationKey string     `json:"reply_operation_key,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	ResolvedAt        *time.Time `json:"resolved_at,omitempty"`
}

// Scope is supplied by the harness, never by model JSON.
type ContextPinProposalScope struct {
	RunID           string
	RootAgentID     string
	SessionID       string
	WorkspaceID     string
	InvocationID    string
	OperationKey    string
	LeaseID         string
	LeaseGeneration int64
	RequestedBy     string
}

type ThreadContextPins struct {
	Version       string               `json:"version"`
	ThreadID      string               `json:"thread_id"`
	Revision      int64                `json:"revision"`
	Pins          []ThreadContextPin   `json:"pins"`
	Proposals     []ContextPinProposal `json:"proposals"`
	Busy          bool                 `json:"busy"`
	EffectiveFrom string               `json:"effective_from"`
}

// Operator controls are separate from the model-only proposal capability.
type ThreadContextPinControl struct {
	Version            string `json:"version"`
	ThreadID           string `json:"-"`
	Action             string `json:"action"`
	ExpectedRevision   int64  `json:"expected_revision"`
	PinID              string `json:"pin_id,omitempty"`
	ExpectedPinVersion int64  `json:"expected_pin_version,omitempty"`
	Text               string `json:"text,omitempty"`
	SourceMessageID    int64  `json:"source_message_id,omitempty"`
	OperationKey       string `json:"-"`
	RequestedBy        string `json:"-"`
}

type ResolveContextPinProposal struct {
	Version                 string `json:"version"`
	ThreadID                string `json:"-"`
	ProposalID              string `json:"-"`
	ExpectedRevision        int64  `json:"expected_revision"`
	ExpectedProposalVersion int64  `json:"expected_proposal_version"`
	Choice                  string `json:"choice"`
	Text                    string `json:"text,omitempty"`
	PinCustom               bool   `json:"pin_custom,omitempty"`
	OperationKey            string `json:"-"`
	RequestedBy             string `json:"-"`
}

type ThreadContextPinResult struct {
	State             ThreadContextPins `json:"state"`
	ReplyText         string            `json:"reply_text,omitempty"`
	ReplyOperationKey string            `json:"reply_operation_key,omitempty"`
	Replayed          bool              `json:"replayed"`
}

func ValidateContextPinText(text string) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(text) == "" {
		return errors.New("context pin text must be nonempty valid UTF-8")
	}
	if utf8.RuneCountInString(text) > MaxThreadContextPinRunes {
		return fmt.Errorf("context pin text exceeds %d characters", MaxThreadContextPinRunes)
	}
	return nil
}

func ValidateThreadContextPinOperator(version, threadID, key, actor string, revision int64) error {
	if version != ThreadContextPinsVersion || strings.TrimSpace(threadID) == "" || revision < 0 {
		return errors.New("Thread context pin control version, Thread and revision are required")
	}
	if strings.TrimSpace(key) == "" || len(key) > 256 || strings.TrimSpace(actor) == "" || len(actor) > 256 || actor == "run_supervisor" || actor == "specialist" {
		return errors.New("context pin changes require an explicit operator and operation key")
	}
	return nil
}

func ContextPinRequestJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
