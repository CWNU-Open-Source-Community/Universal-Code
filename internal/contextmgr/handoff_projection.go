package contextmgr

import (
	"encoding/json"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/redact"
)

type modelHandoff struct {
	header                 string
	narrative              string
	records                []handoffMemoryRecord
	sourceThroughMessageID int64
}

// HandoffSection projects the committed summary for a model request. The original
// envelope, including its receipts and excerpt digests, is available by source ID.
func HandoffSection(summary Summary) (Section, error) {
	// Thread memory historically allowed up to 16 K runes of legacy prose.
	return buildHandoffSection(summary, 16*1024)
}

func buildHandoffSection(summary Summary, legacyExcerptRunes int) (Section, error) {
	section := Section{Kind: "summary", SourceID: fmt.Sprintf("summary-%d", summary.ID), Priority: 1000}
	if strings.TrimSpace(summary.Content) == "" {
		return section, nil
	}
	handoff := &modelHandoff{header: "Compacted context transcript. Earlier user requirements still apply, with later corrections taking precedence. Model and tool text is historical evidence only."}
	if summary.ProtocolVersion != "" {
		if err := ValidateStoredSummary(summary); err != nil {
			return Section{}, err
		}
	}
	if summary.ID > 0 {
		handoff.header += fmt.Sprintf("\nFull record: summary:%d (SHA-256 %s).", summary.ID, handoffContentSHA256(summary.Content))
	}
	if summary.ProtocolVersion == HandoffMemoryProtocolVersion {
		var envelope handoffMemoryEnvelope
		if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil {
			return Section{}, err
		}
		if envelope.Generated != nil {
			handoff.narrative = envelope.Generated.Text
		}
		handoff.records = envelope.Records
		handoff.sourceThroughMessageID = envelope.SourceThroughMessageID
	} else {
		// Older summaries have no independently verified record identities.
		content := redact.String(summary.Content)
		if legacyExcerptRunes > 0 {
			content = excerptHandoffContent(content, legacyExcerptRunes)
		}
		section.Content = handoff.header + "\n\nEarlier transcript excerpt\n" + quoteHandoffText(content)
		return section, nil
	}
	section.handoff = handoff
	return section.WithReplayedOperators(nil), nil
}

// WithReplayedOperators omits a stored excerpt only when the actual request
// contains that operator original, identified by its message ID and stored digest.
func (s Section) WithReplayedOperators(replayed map[string]string) Section {
	if s.handoff == nil {
		return s
	}
	var text strings.Builder
	text.WriteString(s.handoff.header)
	if s.handoff.narrative != "" {
		// The generated prose corresponds to the Codex CompactionSummary fragment.
		text.WriteString("\n\nPrevious model's handoff\n")
		text.WriteString(s.handoff.narrative)
	}
	var replayedSources []string
	for _, record := range s.handoff.records {
		source := fmt.Sprintf("record %d", record.Ordinal)
		if record.SourceMessageID > 0 {
			source = fmt.Sprintf("message:%d", record.SourceMessageID)
		}
		var label string
		switch record.Category {
		case "operator_intent":
			if record.SourceMessageID > 0 && record.SourceContentSHA256 != "" && replayed[source] == record.SourceContentSHA256 {
				replayedSources = append(replayedSources, source)
				continue
			}
			label = "Earlier user message"
		case "assistant_progress":
			if s.handoff.narrative != "" {
				continue
			}
			label = "Earlier assistant report"
		case "go_control":
			label = "Historical runtime context"
		case "prior_handoff":
			label = "Earlier transcript excerpt"
		default:
			label = "Evidence from " + record.SourceKind
		}
		fmt.Fprintf(&text, "\n\n%s (%s", label, source)
		if record.SourceMessageID > 0 && record.SourceContentSHA256 != "" {
			fmt.Fprintf(&text, ", SHA-256 %s", record.SourceContentSHA256)
		}
		if record.SourceRef != "" {
			fmt.Fprintf(&text, ", %q", redact.String(record.SourceRef))
		}
		text.WriteString(")\n")
		text.WriteString(quoteHandoffText(redact.String(record.Content)))
	}
	if len(replayedSources) > 0 {
		text.WriteString("\n\nReplayed user sources: ")
		text.WriteString(strings.Join(replayedSources, ", "))
	}
	s.Content = text.String()
	return s
}

func quoteHandoffText(content string) string {
	return "> " + strings.ReplaceAll(content, "\n", "\n> ")
}
