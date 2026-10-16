package contextmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/redact"
)

const (
	MemoryCandidateProtocolVersion = "context_memory_candidate.v1"
	MaxMemoryCandidates            = 50
	MaxMemoryCandidateSourceScan   = 200
)

type MemoryCandidateStatus string

const (
	MemoryCandidatePending   MemoryCandidateStatus = "pending"
	MemoryCandidateConfirmed MemoryCandidateStatus = "confirmed"
	MemoryCandidateRejected  MemoryCandidateStatus = "rejected"
	MemoryCandidateStale     MemoryCandidateStatus = "stale"
)

type MemoryCandidateSource struct {
	RunID                 string    `json:"run_id"`
	NoteID                string    `json:"note_id"`
	NoteVersion           int64     `json:"note_version"`
	Category              string    `json:"category"`
	References            []string  `json:"references,omitempty"`
	EvidenceIDs           []string  `json:"evidence_ids,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
	CompletionKind        string    `json:"completion_kind"`
	CompletionRef         string    `json:"completion_ref"`
	CompletionFingerprint string    `json:"completion_fingerprint"`
}

// MemoryCandidate is a review queue entry. Only a pending entry retains the
// copied conclusion. Resolved entries are permanent, content-free tombstones,
// so deleting the confirmed memory also removes this second copy of its text.
type MemoryCandidate struct {
	ID               string                `json:"id"`
	ProtocolVersion  string                `json:"protocol_version"`
	WorkspaceID      string                `json:"workspace_id"`
	Title            string                `json:"title,omitempty"`
	Content          string                `json:"content,omitempty"`
	Fingerprint      string                `json:"fingerprint"`
	Status           MemoryCandidateStatus `json:"status"`
	Source           MemoryCandidateSource `json:"source"`
	Confidence       string                `json:"confidence"`
	ConfidenceReason string                `json:"confidence_reason"`
	Redacted         bool                  `json:"redacted"`
	Version          int64                 `json:"version"`
	MemoryID         string                `json:"memory_id,omitempty"`
	ReviewedBy       string                `json:"reviewed_by,omitempty"`
	ReviewedAt       *time.Time            `json:"reviewed_at,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

type MemoryCandidateFilter struct {
	WorkspaceID     string
	IncludeResolved bool
	Limit           int
}

type ReviewMemoryCandidateRequest struct {
	WorkspaceID           string
	ExpectedVersion       int64
	ExpectedFingerprint   string
	ExpectedSourceVersion int64
	RequestedBy           string
	Confirm               bool
}

type MemoryCandidateReviewResult struct {
	Candidate MemoryCandidate `json:"candidate"`
	MemoryID  string          `json:"memory_id,omitempty"`
	Replayed  bool            `json:"replayed"`
}

func (f MemoryCandidateFilter) Validate() error {
	if !validMemoryIdentity(f.WorkspaceID) {
		return errors.New("memory candidate Workspace identity is invalid")
	}
	if f.Limit < 0 || f.Limit > MaxMemoryCandidates {
		return fmt.Errorf("memory candidate limit must be between 0 and %d", MaxMemoryCandidates)
	}
	return nil
}

func (r ReviewMemoryCandidateRequest) Validate() error {
	if err := (MemoryCandidateFilter{WorkspaceID: r.WorkspaceID}).Validate(); err != nil {
		return err
	}
	if r.ExpectedVersion <= 0 || r.ExpectedSourceVersion <= 0 || !validCandidateDigest(r.ExpectedFingerprint) {
		return errors.New("memory candidate review requires the reviewed version, fingerprint and source version")
	}
	return ValidateMemoryActor(r.RequestedBy)
}

func NewMemoryCandidate(workspaceID, title, content string, source MemoryCandidateSource, at time.Time) (MemoryCandidate, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	identity, _ := json.Marshal([]string{workspaceID, source.NoteID})
	candidate := MemoryCandidate{
		ID:              "memory-candidate-" + memoryContentDigest(string(identity)),
		ProtocolVersion: MemoryCandidateProtocolVersion, WorkspaceID: workspaceID,
		Title: strings.TrimSpace(redact.String(title)), Content: strings.TrimSpace(redact.String(content)),
		Status: MemoryCandidatePending, Source: source,
		Confidence:       "operator_review_required",
		ConfidenceReason: "Copied from a source-linked decision or summary Note. The recorded completion proves the source boundary only; the conclusion and its reuse still require operator review.",
		Redacted:         redact.String(title) != title || redact.String(content) != content || strings.Contains(title+content, "[REDACTED:"),
		Version:          1, CreatedAt: at, UpdatedAt: at,
	}
	if source.CompletionKind == "thread_reply" {
		candidate.ConfidenceReason = "Copied from a source-linked decision or summary Note after a recorded Thread reply finished. Finishing that reply does not establish task, plan or acceptance completion; the conclusion and its reuse require operator review."
	}
	for _, value := range append(append([]string{}, source.References...), source.EvidenceIDs...) {
		if value != redact.String(value) || memorySensitivePath(value) || !validMemoryText(value) || len([]byte(value)) > MaxMemoryReferenceBytes {
			return MemoryCandidate{}, errors.New("memory candidate source is sensitive or invalid")
		}
	}
	candidate.Fingerprint = candidate.reviewFingerprint()
	if err := candidate.Validate(); err != nil {
		return MemoryCandidate{}, err
	}
	return candidate, nil
}

func (c MemoryCandidate) Validate() error {
	if c.ID != "memory-candidate-"+candidateIdentityDigest(c.WorkspaceID, c.Source.NoteID) || c.ProtocolVersion != MemoryCandidateProtocolVersion ||
		!validMemoryIdentity(c.WorkspaceID) || !validMemoryIdentity(c.Source.RunID) || !validMemoryIdentity(c.Source.NoteID) ||
		c.Source.NoteVersion < 1 || c.Source.UpdatedAt.IsZero() || c.Version < 1 || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) ||
		!validCandidateDigest(c.Fingerprint) || !validCandidateDigest(c.Source.CompletionFingerprint) ||
		c.Confidence != "operator_review_required" || !validMemoryText(c.ConfidenceReason) || c.ConfidenceReason == "" {
		return errors.New("memory candidate identity, source or audit metadata is invalid")
	}
	if c.Source.Category != "decision" && c.Source.Category != "summary" {
		return errors.New("memory candidate source category is invalid")
	}
	if len(c.Source.References) > 32 || len(c.Source.EvidenceIDs) > 64 {
		return errors.New("memory candidate source references exceed their bound")
	}
	for _, value := range append(append([]string{}, c.Source.References...), c.Source.EvidenceIDs...) {
		if value == "" || value != strings.TrimSpace(value) || value != redact.String(value) || memorySensitivePath(value) || !validMemoryText(value) || len([]byte(value)) > MaxMemoryReferenceBytes {
			return errors.New("memory candidate source is sensitive or invalid")
		}
	}
	switch c.Source.CompletionKind {
	case "completed_run", "thread_reply", "delivery_checkpoint", "delivery_receipt":
	default:
		return errors.New("memory candidate completion source is invalid")
	}
	if c.Source.CompletionRef == "" || len(c.Source.CompletionRef) > 512 || !validMemoryText(c.Source.CompletionRef) || c.Source.CompletionRef != redact.String(c.Source.CompletionRef) {
		return errors.New("memory candidate completion reference is invalid")
	}
	switch c.Status {
	case MemoryCandidatePending:
		if c.Version != 1 || c.Fingerprint != c.reviewFingerprint() || c.ReviewedAt != nil || c.ReviewedBy != "" || c.MemoryID != "" {
			return errors.New("pending memory candidate changed after preparation")
		}
		if _, err := c.Memory("candidate_validation", c.CreatedAt); err != nil {
			return err
		}
	case MemoryCandidateConfirmed, MemoryCandidateRejected, MemoryCandidateStale:
		if c.Title != "" || c.Content != "" || len(c.Source.References) != 0 || len(c.Source.EvidenceIDs) != 0 || c.Version != 2 {
			return errors.New("resolved memory candidate must be a content-free tombstone")
		}
		if c.Status != MemoryCandidateStale {
			if err := ValidateMemoryActor(c.ReviewedBy); err != nil {
				return err
			}
			if c.ReviewedAt == nil || c.ReviewedAt.Before(c.CreatedAt) {
				return errors.New("memory candidate review timestamp is invalid")
			}
		}
		if (c.Status == MemoryCandidateConfirmed) != (c.MemoryID != "") {
			return errors.New("confirmed memory candidate requires its memory identity")
		}
	default:
		return errors.New("memory candidate status is invalid")
	}
	return nil
}

func (c MemoryCandidate) Memory(actor string, at time.Time) (Memory, error) {
	if c.Status != MemoryCandidatePending {
		return Memory{}, errors.New("only a pending memory candidate has reviewable content")
	}
	references := []string{"run:" + c.Source.RunID, fmt.Sprintf("note:%s@v%d", c.Source.NoteID, c.Source.NoteVersion), c.Source.CompletionRef}
	for _, evidenceID := range c.Source.EvidenceIDs {
		if len(references) < MaxMemoryReferences {
			references = append(references, "evidence:"+evidenceID)
		}
	}
	for _, ref := range c.Source.References {
		if len(references) < MaxMemoryReferences {
			references = append(references, ref)
		}
	}
	memory, err := PrepareMemory(CreateMemoryRequest{ID: "memory-" + candidateIdentityDigest(c.WorkspaceID, c.Source.NoteID),
		Scope: MemoryScopeProject, ScopeID: c.WorkspaceID, Title: c.Title, Content: c.Content,
		SourceKind: "operator_explicit", SourceRef: c.ID, References: references, RequestedBy: actor,
		RedactSensitive: true, ExplicitOperator: true}, at)
	memory.Redacted = memory.Redacted || c.Redacted
	return memory, err
}

func (c MemoryCandidate) Resolve(status MemoryCandidateStatus, actor, memoryID string, at time.Time) MemoryCandidate {
	c.Status, c.ReviewedBy, c.MemoryID, c.Version, c.UpdatedAt = status, actor, memoryID, c.Version+1, at.UTC()
	if actor != "" {
		reviewed := at.UTC()
		c.ReviewedAt = &reviewed
	}
	c.Title, c.Content = "", ""
	c.Source.References, c.Source.EvidenceIDs = nil, nil
	return c
}

func (c MemoryCandidate) reviewFingerprint() string {
	value, _ := json.Marshal(struct {
		WorkspaceID      string
		Title            string
		Content          string
		Source           MemoryCandidateSource
		ConfidenceReason string
		Redacted         bool
	}{c.WorkspaceID, c.Title, c.Content, c.Source, c.ConfidenceReason, c.Redacted})
	return memoryContentDigest(string(value))
}

func candidateIdentityDigest(workspaceID, noteID string) string {
	value, _ := json.Marshal([]string{workspaceID, noteID})
	return memoryContentDigest(string(value))
}

func validCandidateDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
