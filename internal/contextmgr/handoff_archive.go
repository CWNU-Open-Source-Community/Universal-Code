package contextmgr

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

// The request-local copy contains only protected text and stored operator
// identities. Neither the caller's slice nor the committed handoff is mutated.
type completeHandoffArchive struct {
	text      string
	operators map[int64]string
}

func (a *completeHandoffArchive) matches(replayed []Message) bool {
	for _, m := range replayed {
		if a.operators[m.SourceMessageID] != m.ContentSHA256 {
			return false
		}
	}
	return true
}

// ArchiveWindow bounds a lookup on the summary's own session and prefix.
// The actual handoff still has to fit together with its replayed source list.
func (s Section) ArchiveWindow() (count int, through int64, bytes int) {
	h := s.handoff
	if h == nil || h.narrative == "" || h.compactedMessageCount < 3 || h.compactedMessageCount > 2*MaxHandoffMemoryRecords ||
		h.sourceThroughMessageID < int64(h.compactedMessageCount) || len(h.records) == 0 || s.Content == "" {
		return 0, 0, 0
	}
	for _, r := range h.records {
		if r.Category == "prior_handoff" || r.SourceKind == "legacy_unclassified" || r.SourceMessageID <= 0 || !validHandoffDigest(r.SourceContentSHA256) {
			return 0, 0, 0
		}
	}
	return h.compactedMessageCount, h.sourceThroughMessageID, len(s.Content)
}

// WithCompleteArchive accepts raw, verified session rows in storage order.
// Keep the existing reservation until the model request's exact replays are known.
func (s Section) WithCompleteArchive(messages []Message) (Section, error) {
	s.archive = nil
	count, through, limit := s.ArchiveWindow()
	if count == 0 || len(messages) != count {
		return s, nil
	}
	rawBytes := 0
	var previous int64
	for _, m := range messages {
		if m.SourceMessageID <= previous {
			return s, fmt.Errorf("archived message %d is out of order", m.SourceMessageID)
		}
		previous = m.SourceMessageID
		if len(m.Content) > limit-rawBytes || m.SourceKind == "compacted_transcript" || m.SourceKind == "legacy_unclassified" {
			return s, nil
		}
		rawBytes += len(m.Content)
	}
	if messages[count-1].SourceMessageID != through {
		return s, nil
	}
	for _, r := range s.handoff.records {
		m := messages[r.Ordinal-1]
		ref, err := redact.ModelHistoryText(sanitizeHandoffSourceRef(m.SourceRef))
		if err != nil {
			return s, err
		}
		if r.SourceMessageID != m.SourceMessageID || r.SourceContentSHA256 != m.ContentSHA256 || r.Role != m.Role ||
			r.SourceKind != m.SourceKind || r.InstructionAuthorized != m.InstructionAuthorized || r.SourceRef != ref {
			return s, fmt.Errorf("archived message %d conflicts with its committed source", r.SourceMessageID)
		}
	}
	var text strings.Builder
	text.WriteString(s.handoff.header)
	text.WriteString("\n\nArchived messages, earliest to latest")
	operators := make(map[int64]string)
	for i, m := range messages {
		if !utf8.ValidString(m.Content) || m.ContentSHA256 != handoffContentSHA256(m.Content) ||
			!validArchiveAuthority(m) {
			return s, fmt.Errorf("invalid source, digest or order for archived message %d", m.SourceMessageID)
		}
		content, err := redact.ModelHistoryText(m.Content)
		if err != nil {
			return s, err
		}
		ref, err := redact.ModelHistoryText(m.SourceRef)
		if err != nil {
			return s, err
		}
		text.WriteByte('\n')
		if i > 0 {
			text.WriteByte('\n')
		}
		fmt.Fprintf(&text, "message:%d (%s, %s", m.SourceMessageID, m.Role, m.SourceKind)
		if ref != "" {
			fmt.Fprintf(&text, ", %q", ref)
		}
		if m.Role == "tool" {
			fmt.Fprintf(&text, ", SHA-256 %s", m.ContentSHA256)
		}
		text.WriteString(")\n")
		text.WriteString(quoteHandoffText(content))
		if m.Role == "user" {
			operators[m.SourceMessageID] = m.ContentSHA256
		}
	}
	if text.Len() <= limit && EstimateTokens(text.String()) <= EstimateTokens(s.Content) {
		s.archive = &completeHandoffArchive{text: text.String(), operators: operators}
	}
	return s, nil
}

func validArchiveAuthority(m Message) bool {
	if m.SourceKind != normalizeHandoffSourceKind(m.SourceKind) {
		return false
	}
	switch m.SourceKind {
	case "operator_message":
		return m.Role == "user" && m.InstructionAuthorized && m.SourceRef == ""
	case "model_response":
		return m.Role == "assistant" && !m.InstructionAuthorized && m.SourceRef == ""
	case "go_control":
		return m.Role == "system" && m.InstructionAuthorized && m.SourceRef == ""
	default:
		return m.Role == "tool" && !m.InstructionAuthorized && m.SourceRef != ""
	}
}
