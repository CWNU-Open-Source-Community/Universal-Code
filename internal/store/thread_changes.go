package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

// ListThreadReplyFileEditEvents reads a proven task ending and its applied
// FileEdit facts in one snapshot, across internal tool-loop continuations.
// Historical replies without execution ownership retain their previous-reply
// interval. Handoff sources require a proven current task ending.
func (s *SQLiteStore) ListThreadReplyFileEditEvents(ctx context.Context,
	threadID, sourceEventID string, limit int,
) (events.Event, []events.Event, error) {
	if limit <= 0 || limit > 1000 {
		return events.Event{}, nil, errors.New("Thread reply change limit must be between 1 and 1000")
	}
	tx, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return events.Event{}, nil, err
	}
	defer finish()
	source, err := scanRunEvent(tx.QueryRowContext(ctx, `SELECT e.id,e.event_id,e.version,
		e.run_id,e.mission_id,e.sequence,e.type,e.source,e.subject_id,e.payload_json,e.created_at
		FROM run_events e JOIN thread_runs b ON b.run_id=e.run_id
		WHERE b.thread_id=? AND e.event_id=? AND ((e.type=? AND e.source='session_store'
		AND json_extract(e.payload_json,'$.session_id')=b.session_id
		AND json_extract(e.payload_json,'$.role')='assistant'
		AND json_extract(e.payload_json,'$.source_kind')=?)
		OR (e.type=? AND e.source='run_execution_handoff'))`, strings.TrimSpace(threadID),
		strings.TrimSpace(sourceEventID), events.SessionMessageEvent, session.SourceModelResponse, events.RunExecutionHandoffCompletedEvent))
	if err != nil {
		return events.Event{}, nil, err
	}
	var after int64
	index, err := loadThreadTaskIndex(ctx, tx, threadID, source.RunID)
	if err != nil {
		return events.Event{}, nil, err
	}
	taskBound := false
	taskID := ""
	for _, span := range index.tasks {
		if span.task.ChangeSourceEventID == source.EventID {
			after, taskBound = span.start-1, true
			taskID = span.task.ID
			break
		}
	}
	if !taskBound && source.Type != events.SessionMessageEvent {
		return events.Event{}, nil, sql.ErrNoRows
	}
	if !taskBound {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM run_events
		WHERE run_id=? AND sequence<? AND type=? AND source='session_store'
		AND json_extract(payload_json,'$.session_id')=(SELECT session_id FROM runs WHERE id=?)
		AND json_extract(payload_json,'$.role')='assistant'
		AND json_extract(payload_json,'$.source_kind')=?`, source.RunID, source.Sequence,
			events.SessionMessageEvent, source.RunID, session.SourceModelResponse).Scan(&after)
		if err != nil {
			return events.Event{}, nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,event_id,version,run_id,mission_id,
		sequence,type,source,subject_id,payload_json,created_at FROM run_events
		WHERE run_id=? AND sequence>? AND sequence<? AND type=? AND source='fileedit_store'
		ORDER BY sequence`, source.RunID, after, source.Sequence, events.FileEditAppliedEvent)
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
		if taskBound && index.ownerAt(item.Sequence) != taskID {
			continue
		}
		items = append(items, item)
		if len(items) == limit {
			break
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return events.Event{}, nil, err
	}
	return source, items, nil
}
