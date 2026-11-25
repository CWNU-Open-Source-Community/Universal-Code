package contextmgr

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// FitOperatorExcerpt projects a verified, already-redacted operator message from
// storage. The source digest identifies the complete stored text, including
// when request-local redaction changes its bytes.
func FitOperatorExcerpt(original Message, summary Summary, remaining int) (Message, bool, error) {
	handoff, err := HandoffSection(summary)
	if err != nil {
		return Message{}, false, err
	}
	return fitOperatorExcerpt(original, handoff, remaining)
}

// WithOperatorExcerptSource retains just the redacted ends in case the whole
// turn becomes a readback reference during request fitting.
func (s Section) WithOperatorExcerptSource(original Message) Section {
	original.Content = excerptHandoffContent(original.Content, MaxHandoffRecordChars)
	s.operatorExcerpt = &original
	return s
}

// FitStoredOperatorExcerpt uses the original identity and the handoff actually
// selected for this request. Sections without a verified source stay references.
func (s Section) FitStoredOperatorExcerpt(handoff Section, remaining int) (Message, bool, error) {
	if s.operatorExcerpt == nil {
		return Message{}, false, nil
	}
	source := *s.operatorExcerpt
	if s.SourceID != fmt.Sprintf("message:%d", source.SourceMessageID) || s.SourceContentSHA256 != source.ContentSHA256 {
		return Message{}, false, errors.New("operator excerpt does not match its selected source")
	}
	return fitOperatorExcerpt(source, handoff, remaining)
}

func fitOperatorExcerpt(original Message, handoff Section, remaining int) (Message, bool, error) {
	if original.Role != "user" || original.SourceKind != "operator_message" || !original.InstructionAuthorized ||
		original.SourceMessageID <= 0 || !validHandoffDigest(original.ContentSHA256) {
		return Message{}, false, errors.New("operator excerpt lacks a verified user source")
	}
	header := fmt.Sprintf("Earlier user message excerpt (message:%d, SHA-256 %s).\n", original.SourceMessageID, original.ContentSHA256)
	if EstimateMessageTokens(original.Role, header) >= remaining {
		return Message{}, false, nil
	}
	if handoff.handoff != nil {
		for _, record := range handoff.handoff.records {
			if record.SourceMessageID == original.SourceMessageID && record.SourceContentSHA256 == original.ContentSHA256 &&
				record.Category == "operator_intent" && record.InstructionAuthorized {
				return Message{}, false, nil
			}
		}
	}
	sample := excerptHandoffContent(original.Content, MaxHandoffRecordChars)
	// Five runes allow one source rune on each side of the shortest marker.
	low, high := 5, utf8.RuneCountInString(sample)
	best := ""
	for low <= high {
		mid := low + (high-low)/2
		excerpt := excerptHandoffContent(sample, mid)
		if EstimateMessageTokens(original.Role, header+excerpt) <= remaining {
			best, low = excerpt, mid+1
		} else {
			high = mid - 1
		}
	}
	if best == "" {
		return Message{}, false, nil
	}
	original.Content = header + best
	return original, true, nil
}
