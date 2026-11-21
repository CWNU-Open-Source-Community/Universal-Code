package contextmgr

import (
	"fmt"
	"maps"
	"slices"

	"cyberagent-workbench/internal/redact"
)

type OperatorSubmission struct {
	SourceID string `json:"source_id"`
	Text     string `json:"text"`
}

type OperatorRestatement struct {
	EarlierIdenticalSource string                `json:"earlier_identical_source"`
	Submissions            [2]OperatorSubmission `json:"operator_submissions_in_time_order"`
}

// LatestOperatorRestatement identifies a new submission of identical original
// text with another operator submission in between. A prior round's sequence
// alone is not a new submission. The result only describes source order.
func (s Section) LatestOperatorRestatement(messages []Message) *OperatorRestatement {
	type source struct {
		digest, text string
		submitted    bool
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
		item.submitted = item.submitted || submitted
		// Only quote a whole, already redacted original. Excerpts, long rows and
		// pre-redaction rows can still identify an earlier source by its digest.
		const maxQuoteTokens = 96
		if len(text) <= 4*maxQuoteTokens && EstimateTokens(text) <= maxQuoteTokens &&
			handoffContentSHA256(text) == digest && redact.String(text) == text {
			item.text = text
		}
		sources[id] = item
	}
	var through int64
	if s.handoff != nil {
		through = s.handoff.sourceThroughMessageID
		for _, record := range s.handoff.records {
			if record.Category == "operator_intent" && record.InstructionAuthorized {
				add(record.SourceMessageID, record.SourceContentSHA256, record.Content, false)
			}
		}
	}
	for _, message := range messages {
		if message.Role == "user" && message.SourceKind == "operator_message" && message.InstructionAuthorized {
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
		if earlier == 0 || !current.submitted || current.text == "" || betweenID <= earlier {
			continue
		}
		between := sources[betweenID]
		if between.text != "" {
			latest = &OperatorRestatement{EarlierIdenticalSource: fmt.Sprintf("message:%d", earlier),
				Submissions: [2]OperatorSubmission{{fmt.Sprintf("message:%d", betweenID), between.text},
					{fmt.Sprintf("message:%d", id), current.text}}}
		}
	}
	return latest
}
