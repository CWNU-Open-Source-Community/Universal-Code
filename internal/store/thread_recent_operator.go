package store

import (
	"context"
	"errors"
	"fmt"

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

// An unbound Session first looks for complete originals in its own compacted prefix.
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

// Read at most one additional body for an excerpt. The small ID scan is bounded
// independently of the number and sizes of old messages, and scanSessionMessage
// verifies the full stored digest before the caller redacts or takes either end.
func (s *SQLiteStore) LatestOversizedSessionOperator(ctx context.Context, sessionID string, afterID, beforeID int64, tokenBudget int) (session.Message, bool, error) {
	if afterID < 0 || beforeID <= afterID || tokenBudget < 1 || tokenBudget > session.MaxRecentOperatorTokens {
		return session.Message{}, false, errors.New("invalid compacted Session excerpt boundary or token budget")
	}
	rows, err := s.readCompactedOperatorMessages(ctx, 1, `WITH recent AS (
		SELECT message.id FROM session_messages message
		WHERE message.session_id=? AND message.id>? AND message.id<? AND message.compacted=1 AND `+storedOperatorSource+`
		ORDER BY message.id DESC LIMIT ?
	) `+operatorMessageColumns+`FROM session_messages message JOIN recent ON recent.id=message.id
		WHERE message.token_estimate>? OR length(CAST(message.content AS BLOB))>?
		ORDER BY message.id DESC LIMIT ?`, sessionID, afterID, beforeID, session.ContextProvenanceVersion,
		session.SourceOperatorMessage, session.MaxRecentOperatorMessages, tokenBudget, 4*tokenBudget)
	if err != nil || len(rows) == 0 {
		return session.Message{}, false, err
	}
	return rows[0], true, nil
}

func (s *SQLiteStore) readCompactedOperatorMessages(ctx context.Context, limit int, query string, args ...any) ([]session.Message, error) {
	if limit < 1 || limit > session.MaxRecentOperatorMessages {
		return nil, fmt.Errorf("recent operator message limit must be between 1 and %d", session.MaxRecentOperatorMessages)
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
