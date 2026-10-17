package store

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/session"
)

// The caller is the root request assembler. Limit to this Run's Thread lineage
// and exclude its still-active history, which already appears in the request.
// Pending/cancelled steering and evidence pretending to be user text are never
// recovered as original user instructions.
func (s *SQLiteStore) ListCompactedThreadOperatorMessages(ctx context.Context, runID string, limit int) ([]session.Message, error) {
	if limit < 1 || limit > 64 {
		return nil, errors.New("recent operator message limit must be between 1 and 64")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message.id, message.session_id, message.role, message.content,
		message.provenance_version, message.source_kind, message.source_ref, message.content_sha256,
		message.instruction_authorized, message.token_estimate, message.compacted, message.created_at
		FROM thread_runs current_binding
		JOIN thread_runs binding ON binding.thread_id=current_binding.thread_id AND binding.ordinal<=current_binding.ordinal
		JOIN session_messages message ON message.session_id=binding.session_id
		WHERE current_binding.run_id=? AND message.role='user'
		AND message.provenance_version=? AND message.source_kind=? AND message.instruction_authorized=1
		AND (binding.session_id<>current_binding.session_id OR message.compacted=1)
		ORDER BY binding.ordinal DESC, message.id DESC LIMIT ?`,
		runID, session.ContextProvenanceVersion, session.SourceOperatorMessage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []session.Message
	for rows.Next() {
		message, err := scanSessionMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
