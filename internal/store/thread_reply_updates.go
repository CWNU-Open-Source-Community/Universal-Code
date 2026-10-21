package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/threadtranscript"
)

type threadReplyCursor struct {
	Version int    `json:"v"`
	Store   string `json:"s"`
	After   int64  `json:"a"`
}

type threadReplyRecord struct {
	position int64
	thread   domain.ThreadSearchMatch
	source   threadtranscript.Source
}

// ListThreadReplyUpdates scans at most limit source events plus one lookahead.
// An empty cursor establishes the current baseline without replaying history.
// Active and archived Threads are observed; deleted Threads never yield items.
// Empty result pages may have more work. Persist items before their cursor and
// continue while hasMore is true. Replaying a cursor returns the same message IDs.
// Equal text in separate saved messages does not establish replay identity.
func (s *SQLiteStore) ListThreadReplyUpdates(ctx context.Context, cursor string, limit int) (
	items []domain.ThreadSearchMatch, next string, hasMore bool, err error,
) {
	if limit < 1 || limit > 100 {
		return nil, "", false, apperror.New(apperror.CodeInvalidArgument, "reply update limit must be between 1 and 100")
	}
	var head int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM run_events`).Scan(&head); err != nil {
		return nil, "", false, err
	}
	state := threadReplyCursor{Version: 1, Store: s.DataStoreID(), After: head}
	items = make([]domain.ThreadSearchMatch, 0)
	if cursor == "" {
		return items, encodeThreadReplyCursor(state), false, nil
	}
	state, err = decodeThreadReplyCursor(cursor, s.DataStoreID(), head)
	if err != nil {
		return nil, "", false, err
	}
	records, err := s.threadReplyRecords(ctx, state.After, head, limit+1)
	if err != nil {
		return nil, "", false, err
	}
	hasMore = len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	for _, record := range records {
		if record.thread.ThreadStatus == domain.ThreadActive || record.thread.ThreadStatus == domain.ThreadArchived {
			visible, projectionErr := publicThreadReplySource(record.thread.ThreadID, record.source)
			if projectionErr != nil {
				return nil, "", false, projectionErr
			}
			if visible {
				items = append(items, record.thread)
			}
		}
		state.After = record.position
	}
	return items, encodeThreadReplyCursor(state), hasMore, nil
}

func (s *SQLiteStore) threadReplyRecords(ctx context.Context, after, head int64, limit int) ([]threadReplyRecord, error) {
	// Advance over private and unbound events too, without projecting their data.
	// The global primary key keeps additions and Run succession from shifting a page.
	rows, err := s.db.QueryContext(ctx, `SELECT event.id, COALESCE(thread.id,''),
	 COALESCE(thread.status,''), COALESCE(binding.ordinal,0), COALESCE(binding.session_id,''),
	 COALESCE(run.status,''), event.event_id,event.version,event.run_id,event.mission_id,
	 event.sequence,event.type,event.source,COALESCE(event.subject_id,''),
	 CASE WHEN thread.id IS NOT NULL AND event.source<>'thread_branch'
	  AND event.type IN ('session.message_created','model.public_commentary')
	  THEN event.payload_json ELSE '{}' END,event.created_at
	 FROM run_events event
	 LEFT JOIN thread_runs binding ON binding.run_id=event.run_id
	 LEFT JOIN threads thread ON thread.id=binding.thread_id AND thread.status IN ('active','archived')
	 LEFT JOIN runs run ON run.id=event.run_id
	 WHERE event.id>? AND event.id<=? ORDER BY event.id LIMIT ?`, after, head, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []threadReplyRecord
	for rows.Next() {
		var record threadReplyRecord
		var event events.Event
		var created string
		if err := rows.Scan(&record.position, &record.thread.ThreadID, &record.thread.ThreadStatus,
			&record.source.Ordinal, &record.source.SessionID, &record.source.RunStatus,
			&event.EventID, &event.Version, &event.RunID, &event.MissionID, &event.Sequence,
			&event.Type, &event.Source, &event.SubjectID, &event.PayloadJSON, &created); err != nil {
			return nil, err
		}
		event.CreatedAt = parseTS(created)
		record.source.RunID, record.source.Sequence = event.RunID, event.Sequence
		record.source.CreatedAt, record.source.Event = event.CreatedAt, &event
		record.thread.ID, record.thread.MessageID = event.EventID, event.EventID
		record.thread.RunOrdinal, record.thread.Sequence = record.source.Ordinal, event.Sequence
		record.thread.CreatedAt, record.thread.Role = event.CreatedAt, "assistant"
		records = append(records, record)
	}
	return records, rows.Err()
}

func publicThreadReplySource(threadID string, source threadtranscript.Source) (bool, error) {
	// Branches copy old assistant messages into new commentary events. They are
	// readable history, but their creation is not a new assistant reply.
	if source.Event.Source == "thread_branch" ||
		(source.Event.Type != events.SessionMessageEvent && source.Event.Type != events.ModelPublicCommentaryEvent) {
		return false, nil
	}
	items, err := threadtranscript.Build(threadID, []threadtranscript.Source{source})
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Source != "model" || item.Kind != "model_update" || item.Type != threadtranscript.TypeMessage {
			continue
		}
		// Match saved-message search's full-body visibility checks after the Go
		// public projection has accepted this source. No text leaves this reader.
		var payload struct {
			Content string `json:"content"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal([]byte(source.Event.PayloadJSON), &payload); err != nil {
			return false, err
		}
		text := payload.Content
		if source.Event.Type == events.ModelPublicCommentaryEvent {
			text = payload.Text
		}
		text = strings.TrimSpace(redact.String(text))
		return text != "" && !syntheticSearchMessage.MatchString(text) && !mockSearchResponse.MatchString(text), nil
	}
	return false, nil
}

func encodeThreadReplyCursor(cursor threadReplyCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeThreadReplyCursor(encoded, storeID string, head int64) (threadReplyCursor, error) {
	invalid := func() (threadReplyCursor, error) {
		return threadReplyCursor{}, apperror.New(apperror.CodeInvalidArgument, "reply update cursor does not match this data store or event position")
	}
	if len(encoded) > 512 {
		return invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor threadReplyCursor
	if err := decoder.Decode(&cursor); err != nil {
		return invalid()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || cursor.Version != 1 ||
		cursor.Store != storeID || storeID == "" || cursor.After < 0 || cursor.After > head {
		return invalid()
	}
	return cursor, nil
}
