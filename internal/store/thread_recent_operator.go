package store

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/session"
)

const operatorMessageColumns = `SELECT message.id, message.session_id, message.role, message.content,
	message.provenance_version, message.source_kind, message.source_ref, message.content_sha256,
	message.instruction_authorized, message.token_estimate, message.compacted, message.created_at `

const storedOperatorSource = `message.role='user' AND message.provenance_version=?
	AND message.source_kind=? AND message.instruction_authorized=1`

// The caller is the root request assembler. Limit to this Run's Thread lineage
// and exclude its still-active history, which already appears in the request.
// Pending/cancelled steering and evidence pretending to be user text are never
// recovered as original user instructions.
func (s *SQLiteStore) ListCompactedThreadOperatorMessages(ctx context.Context, runID string, limit int) ([]session.Message, error) {
	return s.readCompactedOperatorMessages(ctx, limit, operatorMessageColumns+`FROM thread_runs current_binding
		JOIN thread_runs binding ON binding.thread_id=current_binding.thread_id AND binding.ordinal<=current_binding.ordinal
		JOIN session_messages message ON message.session_id=binding.session_id
		WHERE current_binding.run_id=? AND `+storedOperatorSource+`
		AND (binding.session_id<>current_binding.session_id OR message.compacted=1)
		ORDER BY binding.ordinal DESC, message.id DESC LIMIT ?`,
		runID, session.ContextProvenanceVersion, session.SourceOperatorMessage)
}

// An unbound Session replays only its own compacted prefix. A record exceeding
// the remaining input budget cannot be replayed whole, so leave its bytes in storage.
func (s *SQLiteStore) ListCompactedSessionOperatorMessages(ctx context.Context, sessionID string, beforeID int64, tokenBudget int) ([]session.Message, error) {
	if beforeID <= 0 || tokenBudget < 1 || tokenBudget > session.MaxRecentOperatorTokens {
		return nil, errors.New("invalid compacted Session history boundary or token budget")
	}
	return s.readCompactedOperatorMessages(ctx, session.MaxRecentOperatorMessages, operatorMessageColumns+`FROM session_messages message
		WHERE message.session_id=? AND message.id<? AND message.compacted=1 AND `+storedOperatorSource+`
		AND message.token_estimate<=? AND length(CAST(message.content AS BLOB))<=?
		ORDER BY message.id DESC LIMIT ?`,
		sessionID, beforeID, session.ContextProvenanceVersion, session.SourceOperatorMessage, tokenBudget, 4*tokenBudget)
}

func (s *SQLiteStore) readCompactedOperatorMessages(ctx context.Context, limit int, query string, args ...any) ([]session.Message, error) {
	if limit < 1 || limit > session.MaxRecentOperatorMessages {
		return nil, errors.New("recent operator message limit must be between 1 and 64")
	}
	rows, err := s.db.QueryContext(ctx, query, append(args, limit)...)
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
