package contextmgr

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"cyberagent-workbench/internal/redact"
)

type OperatorRestatement struct {
	EarlierIdenticalSource string `json:"earlier_identical_source"`
	InterveningSource      string `json:"intervening_operator_source"`
	SourceID               string `json:"source_id"`
	Text                   string `json:"text"`
}

func boundedOperatorQuote(text string) bool {
	const maxQuoteTokens = 96
	return text != "" && len(text) <= 4*maxQuoteTokens && EstimateTokens(text) <= maxQuoteTokens && redact.String(text) == text
}

// LatestOperatorRestatement identifies a new submission of identical original
// text with another operator submission in between. Only the new submission
// is quoted. Earlier messages remain available by their original identities.
func (s Section) LatestOperatorRestatement(messages []Message) *OperatorRestatement {
	type source struct {
		digest, text string
	}
	sources := make(map[int64]source)
	add := func(id int64, digest, text string, submitted bool) {
		if id <= 0 || !validHandoffDigest(digest) {
			return
		}
		item, exists := sources[id]
		if exists && item.digest != digest {
			// Conflicting bindings for one identity cannot support a quotation.
			sources[id] = source{}
			return
		}
		item.digest = digest
		// Earlier sources need only their digest. A new quotation must fit whole.
		if submitted && boundedOperatorQuote(text) {
			item.text = text
		}
		sources[id] = item
	}
	var through int64
	if s.handoff != nil {
		through = s.handoff.sourceThroughMessageID
		for _, record := range s.handoff.records {
			if record.Category == "operator_intent" && record.InstructionAuthorized {
				add(record.SourceMessageID, record.SourceContentSHA256, "", false)
			}
		}
	}
	for _, message := range messages {
		if message.Role == "user" && message.SourceKind == "operator_message" && message.InstructionAuthorized &&
			message.ContentSHA256 == handoffContentSHA256(message.Content) {
			add(message.SourceMessageID, message.ContentSHA256, message.Content, message.SourceMessageID > through)
		}
	}
	seen := make(map[string]int64)
	var latest *OperatorRestatement
	var lastID int64
	for _, id := range slices.Sorted(maps.Keys(sources)) {
		betweenID := lastID
		lastID = id
		current := sources[id]
		if current.digest == "" {
			continue
		}
		earlier := seen[current.digest]
		seen[current.digest] = id
		if earlier == 0 || current.text == "" || betweenID <= earlier || sources[betweenID].digest == "" {
			continue
		}
		latest = &OperatorRestatement{EarlierIdenticalSource: fmt.Sprintf("message:%d", earlier),
			InterveningSource: fmt.Sprintf("message:%d", betweenID), SourceID: fmt.Sprintf("message:%d", id), Text: current.text}
	}
	return latest
}

// ReplayedOperatorMessage supplies the delivered text with its saved source.
// A section without the original provenance can still identify its readback.
func (s Section) ReplayedOperatorMessage() Message {
	if source := s.operatorExcerpt; source != nil && s.SourceID == fmt.Sprintf("message:%d", source.SourceMessageID) && s.SourceContentSHA256 == source.ContentSHA256 {
		original := *source
		original.Content = s.Content
		return original
	}
	return Message{Content: s.Content, ContentSHA256: s.SourceContentSHA256}
}

func (s Section) withReplayedRestatement(view, replayedFooter string, replayed map[string]Message) string {
	if len(replayed) < 3 || (s.archive == nil && len(view) >= len(s.Content)) {
		return view
	}
	// Some verified originals survive in the store/replay window after their
	// summary excerpt has been evicted. Any surviving record must agree.
	records := make(map[int64]handoffMemoryRecord, len(s.handoff.records))
	for _, row := range s.handoff.records {
		records[row.SourceMessageID] = row
	}
	var originals []Message
	for ref, actual := range replayed {
		if actual.Role != "user" || actual.SourceKind != "operator_message" || !actual.InstructionAuthorized ||
			actual.SourceMessageID <= 0 || actual.SourceMessageID > s.handoff.sourceThroughMessageID ||
			ref != fmt.Sprintf("message:%d", actual.SourceMessageID) || handoffContentSHA256(actual.Content) != actual.ContentSHA256 {
			continue
		}
		if row, exists := records[actual.SourceMessageID]; exists && (row.Category != "operator_intent" || actual.ContentSHA256 != row.SourceContentSHA256) {
			continue
		}
		protected, err := redact.ModelHistoryText(actual.Content)
		if err != nil || protected != actual.Content {
			continue
		}
		originals = append(originals, actual)
	}
	// These are the actual turns in this request. The auxiliary compactor's
	// previous high-water mark is irrelevant to a statement about their order.
	fact := (Section{}).LatestOperatorRestatement(originals)
	if fact == nil {
		return view
	}
	order := fmt.Sprintf("\n\nReplayed user submissions, earliest to latest\n%s → %s → %s", fact.EarlierIdenticalSource, fact.InterveningSource, fact.SourceID)
	lastQuote := fmt.Sprintf("\nAt %s the user repeated %s verbatim\n%s", fact.SourceID, fact.EarlierIdenticalSource, quoteHandoffText(fact.Text))
	slices.SortFunc(originals, func(a, b Message) int { return cmp.Compare(a.SourceMessageID, b.SourceMessageID) })
	refs := make([]string, len(originals))
	for i, original := range originals {
		refs[i] = fmt.Sprintf("message:%d", original.SourceMessageID)
	}
	var following string
	if later := refs[slices.Index(refs, fact.SourceID)+1:]; len(later) > 0 {
		following = fmt.Sprintf("\nLater replayed user messages include %s.", strings.Join(later, ", "))
	}
	reservedTokens := EstimateTokens(s.Content)
	fits := func(text string) bool { return len(text) <= len(s.Content) && EstimateTokens(text) <= reservedTokens }
	if s.archive != nil && s.archive.matches(originals) {
		prefix := s.archive.text + replayedFooter + order
		// The intervening identity was selected from the same complete, verified user replays as the repeat.
		if middle := replayed[fact.InterveningSource].Content; boundedOperatorQuote(middle) {
			const guidance = "\nFor a current value, use the later user statement about the same subject, unless a subsequent user message changes it."
			pair := fmt.Sprintf("\nAt %s the user said\n%s", fact.InterveningSource, quoteHandoffText(middle))
			if projected := prefix + pair + lastQuote + guidance + following; fits(projected) {
				return projected
			}
		}
		if projected := prefix + lastQuote + following; fits(projected) {
			return projected
		}
	}
	if withNote := view + order + lastQuote + following; fits(withNote) {
		return withNote
	}
	return view
}
