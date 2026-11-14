package session

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	EvidenceAttachmentProtocolVersion = "session_evidence_attachment.v1"
	EvidenceInventoryProtocolVersion  = "session_evidence_inventory.v1"
	MaxEvidenceInventoryItems         = 100
)

type EvidenceAttachment struct {
	ID                 string
	ProtocolVersion    string
	OperationKeyDigest string
	RequestFingerprint string
	RunID              string
	SessionID          string
	WorkspaceID        string
	SourceKind         string
	SourceRef          string
	ContentSHA256      string
	SessionMessageID   int64
	AttachedBy         string
	EventSequence      int64
	CreatedAt          time.Time
}

// PreparedEvidenceAttachment is a validated snapshot awaiting the caller's
// atomic persistence boundary. It is never itself a model-visible message.
type PreparedEvidenceAttachment struct {
	Attachment EvidenceAttachment
	Message    Message
}

type EvidenceInventoryItem struct {
	AttachmentID          string
	RunID                 string
	SessionID             string
	WorkspaceID           string
	SourceKind            string
	SourceRef             string
	ContentSHA256         string
	InstructionAuthorized bool
	AttachedAt            time.Time
}

func (i EvidenceInventoryItem) Validate() error {
	if i.InstructionAuthorized {
		return errors.New("evidence inventory cannot authorize document instructions")
	}
	attachment := EvidenceAttachment{
		ID: i.AttachmentID, ProtocolVersion: EvidenceAttachmentProtocolVersion,
		OperationKeyDigest: strings.Repeat("0", 64),
		RequestFingerprint: strings.Repeat("0", 64),
		RunID:              i.RunID, SessionID: i.SessionID, WorkspaceID: i.WorkspaceID,
		SourceKind: i.SourceKind, SourceRef: i.SourceRef, ContentSHA256: i.ContentSHA256,
		SessionMessageID: 1, AttachedBy: "inventory", EventSequence: 1,
		CreatedAt: i.AttachedAt,
	}
	return attachment.Validate()
}

type EvidenceInventory struct {
	ProtocolVersion string
	RunID           string
	Items           []EvidenceInventoryItem
	Truncated       bool
}

func (i EvidenceInventory) Validate() error {
	if i.ProtocolVersion != EvidenceInventoryProtocolVersion ||
		!validEvidenceIdentity(i.RunID) || len(i.Items) > MaxEvidenceInventoryItems {
		return errors.New("evidence inventory envelope is invalid")
	}
	seen := make(map[string]struct{}, len(i.Items))
	for _, item := range i.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if item.RunID != i.RunID {
			return errors.New("evidence inventory item escaped its Run")
		}
		if _, exists := seen[item.AttachmentID]; exists {
			return errors.New("evidence inventory contains a duplicate attachment")
		}
		seen[item.AttachmentID] = struct{}{}
	}
	return nil
}

func (a EvidenceAttachment) Validate() error {
	if a.ProtocolVersion != EvidenceAttachmentProtocolVersion ||
		!validSHA256(a.OperationKeyDigest) || !validSHA256(a.RequestFingerprint) ||
		!validSHA256(a.ContentSHA256) || a.SourceKind != SourceWorkspaceFile ||
		a.SessionMessageID <= 0 || a.EventSequence <= 0 || a.CreatedAt.IsZero() {
		return errors.New("evidence attachment protocol, digest, or durable binding is invalid")
	}
	for _, value := range []string{a.ID, a.RunID, a.SessionID, a.WorkspaceID, a.AttachedBy} {
		if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
			utf8.RuneCountInString(value) > 256 || strings.ContainsRune(value, 0) {
			return errors.New("evidence attachment identity is invalid")
		}
		for _, current := range value {
			if unicode.IsControl(current) {
				return errors.New("evidence attachment identity contains control characters")
			}
		}
	}
	if a.SourceRef == "" ||
		!utf8.ValidString(a.SourceRef) || utf8.RuneCountInString(a.SourceRef) > MaxContextSourceRefRunes ||
		strings.ContainsRune(a.SourceRef, 0) || !validWorkspaceEvidenceRef(a.SourceRef) {
		return errors.New("evidence attachment source reference is invalid")
	}
	for _, current := range a.SourceRef {
		if unicode.IsControl(current) {
			return errors.New("evidence attachment source reference contains control characters")
		}
	}
	return nil
}

func validWorkspaceEvidenceRef(value string) bool {
	if value == "." || strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, `\:`) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validEvidenceIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 256 || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}
