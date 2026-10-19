package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

// ListThreadReplyFileEditEvents reads the exact durable reply and its applied
// FileEdit facts in one snapshot. User Session messages are committed after
// tools, so the previous assistant reply, rather than a user-message timestamp,
// is the start of this interval. Run ownership also bounds the first reply.
func (s *SQLiteStore) ListThreadReplyFileEditEvents(ctx context.Context,
	threadID, sourceEventID string, limit int,
) (events.Event, []events.Event, error) {
	if limit <= 0 || limit > 1000 {
		return events.Event{}, nil, errors.New("Thread reply change limit must be between 1 and 1000")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return events.Event{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	source, err := scanRunEvent(tx.QueryRowContext(ctx, `SELECT e.id,e.event_id,e.version,
		e.run_id,e.mission_id,e.sequence,e.type,e.source,e.subject_id,e.payload_json,e.created_at
		FROM run_events e JOIN thread_runs b ON b.run_id=e.run_id
		WHERE b.thread_id=? AND e.event_id=? AND e.type=? AND e.source='session_store'
		AND json_extract(e.payload_json,'$.session_id')=b.session_id
		AND json_extract(e.payload_json,'$.role')='assistant'
		AND json_extract(e.payload_json,'$.source_kind')=?`, strings.TrimSpace(threadID),
		strings.TrimSpace(sourceEventID), events.SessionMessageEvent, session.SourceModelResponse))
	if err != nil {
		return events.Event{}, nil, err
	}
	var after int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM run_events
		WHERE run_id=? AND sequence<? AND type=? AND source='session_store'
		AND json_extract(payload_json,'$.session_id')=(SELECT session_id FROM runs WHERE id=?)
		AND json_extract(payload_json,'$.role')='assistant'
		AND json_extract(payload_json,'$.source_kind')=?`, source.RunID, source.Sequence,
		events.SessionMessageEvent, source.RunID, session.SourceModelResponse).Scan(&after)
	if err != nil {
		return events.Event{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,event_id,version,run_id,mission_id,
		sequence,type,source,subject_id,payload_json,created_at FROM run_events
		WHERE run_id=? AND sequence>? AND sequence<? AND type=? AND source='fileedit_store'
		ORDER BY sequence LIMIT ?`, source.RunID, after, source.Sequence, events.FileEditAppliedEvent, limit)
	if err != nil {
		return events.Event{}, nil, err
	}
	items := []events.Event{}
	for rows.Next() {
		item, readErr := scanRunEvent(rows)
		if readErr != nil {
			_ = rows.Close()
			return events.Event{}, nil, readErr
		}
		items = append(items, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return events.Event{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return events.Event{}, nil, err
	}
	return source, items, nil
}
