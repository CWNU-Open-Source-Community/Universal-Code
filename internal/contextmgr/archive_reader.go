package contextmgr

import "context"

type compactedSessionArchiveReader interface {
	ListCompleteCompactedSessionPrefix(context.Context, string, int64, int, int) ([]Message, error)
}

// LoadCompleteArchive prepares a source-backed alternative for a short handoff
// about a repeated user submission. Final request fitting decides whether the
// actual replay and its reserved space permit that alternative.
func (s Section) LoadCompleteArchive(ctx context.Context, store any, sessionID string, originals []Message) (Section, error) {
	reader, ok := store.(compactedSessionArchiveReader)
	count, through, maxBytes := s.ArchiveWindow()
	if !ok || count == 0 || sessionID == "" || s.handoff.taskID != sessionID ||
		(Section{}).LatestOperatorRestatement(originals) == nil {
		return s, nil
	}
	rows, err := reader.ListCompleteCompactedSessionPrefix(ctx, sessionID, through, count, maxBytes)
	if err != nil || len(rows) == 0 {
		return s, err
	}
	return s.WithCompleteArchive(rows)
}
