package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/threadtranscript"
)

// Search uses the existing durable records, including compacted messages. The
// SQL substring is only a candidate filter: public projection and redaction are
// checked before matching or advancing the externally visible result cursor.
const threadSearchCandidates = `WITH candidates AS (
	SELECT 'title-' || thread.id AS id, thread.id AS thread_id, thread.title,
	 thread.status, thread.workspace_id, '' AS message_id, 'title' AS role,
	 thread.title AS content, thread.created_at, 0 AS ordinal, 0 AS sequence
	FROM threads thread
	UNION ALL
	SELECT event.event_id, thread.id, thread.title, thread.status,
	 thread.workspace_id, event.event_id,
	 CASE WHEN event.type='operator.steering_queued' THEN 'user'
	 ELSE json_extract(event.payload_json, '$.role') END,
	 CASE WHEN event.type='operator.steering_queued' THEN steering.content
	 ELSE json_extract(event.payload_json, '$.content') END,
	 event.created_at, binding.ordinal, event.sequence
	FROM threads thread JOIN thread_runs binding ON binding.thread_id=thread.id
	JOIN run_events event ON event.run_id=binding.run_id
	LEFT JOIN operator_steering_messages steering ON steering.id=event.subject_id
	 AND steering.run_id=binding.run_id AND steering.session_id=binding.session_id
	WHERE (event.type='session.message_created' AND (
	 (json_extract(event.payload_json,'$.role')='user' AND json_extract(event.payload_json,'$.source_kind')='operator_message') OR
	 (json_extract(event.payload_json,'$.role')='assistant' AND json_extract(event.payload_json,'$.source_kind')='model_response')))
	 OR (event.type='operator.steering_queued' AND steering.status IN ('pending','cancelled','committed'))
) SELECT id, thread_id, title, status, workspace_id, message_id, role,
 content, created_at, ordinal, sequence FROM candidates WHERE status IN ('active'`

func (s *SQLiteStore) SearchThreadMessages(ctx context.Context, query string, includeArchived bool,
	before time.Time, beforeID string, limit int,
) ([]domain.ThreadSearchMatch, error) {
	if err := validateStoreCreationPage(before, beforeID, limit); err != nil {
		return nil, err
	}
	query, err := domain.NormalizeThreadTitleQuery(query)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, errors.New("saved message search requires a query")
	}
	items := make([]domain.ThreadSearchMatch, 0, limit)
	for len(items) < limit {
		batch, err := s.threadSearchCandidates(ctx, query, includeArchived, before, beforeID, "", "", 100)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			item.Content = strings.TrimSpace(redact.String(item.Content))
			normalized := foldSearchContent(item.Content)
			if !strings.Contains(normalized, query) {
				continue
			}
			if item.MessageID != "" {
				visible, err := s.publicSearchMessage(ctx, item)
				if err != nil {
					return nil, err
				}
				if !visible {
					continue
				}
			}
			item.ThreadTitle = redact.String(item.ThreadTitle)
			items = append(items, item)
			if len(items) == limit {
				return items, nil
			}
		}
		last := batch[len(batch)-1]
		before, beforeID = last.CreatedAt, last.ID
	}
	return items, nil
}

var syntheticSearchMessage = regexp.MustCompile(`^Continue mission at turn \d+ using only the structured tools offered by Go when needed:`)
var mockSearchResponse = regexp.MustCompile(`(?i)^mock\s+(?:plan|response)\b`)

func foldSearchContent(content string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, content)
}

func (s *SQLiteStore) threadSearchCandidates(ctx context.Context, query string, archived bool,
	before time.Time, beforeID, threadID, messageID string, limit int,
) ([]domain.ThreadSearchMatch, error) {
	sql := threadSearchCandidates
	if archived {
		sql += `,'archived'`
	}
	sql += `)`
	args := []any{}
	if query != "" {
		sql += ` AND instr(lower(content), ?) > 0`
		args = append(args, query)
	}
	if threadID != "" {
		sql += ` AND thread_id=? AND message_id=?`
		args = append(args, threadID, messageID)
	}
	if !before.IsZero() {
		key := creationTimestampKeySQL("created_at")
		sql += ` AND (` + key + ` < ? OR (` + key + ` = ? AND id < ?))`
		anchor := creationTimestampKey(before)
		args = append(args, anchor, anchor, beforeID)
	}
	sql += ` ORDER BY ` + creationTimestampKeySQL("created_at") + ` DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.ThreadSearchMatch
	for rows.Next() {
		var item domain.ThreadSearchMatch
		var created string
		if err := rows.Scan(&item.ID, &item.ThreadID, &item.ThreadTitle, &item.ThreadStatus,
			&item.WorkspaceID, &item.MessageID, &item.Role, &item.Content, &created, &item.RunOrdinal, &item.Sequence); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTS(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) publicSearchMessage(ctx context.Context, item domain.ThreadSearchMatch) (bool, error) {
	content := strings.TrimSpace(item.Content)
	if syntheticSearchMessage.MatchString(content) || item.Role == "assistant" && mockSearchResponse.MatchString(content) {
		return false, nil
	}
	source, err := s.ListThreadTranscriptSourceBefore(ctx, item.ThreadID, item.RunOrdinal, item.Sequence+1, 1)
	if err != nil {
		return false, err
	}
	if len(source) == 1 && source[0].Event != nil && source[0].Event.Type == "operator.steering_queued" {
		_, peer, err := s.ThreadPeerMessageForSteering(ctx, source[0].Event.SubjectID)
		if err != nil || peer {
			return false, err
		}
	}
	items, err := threadtranscript.Build(item.ThreadID, source)
	if err != nil {
		return false, err
	}
	for _, projected := range items {
		if projected.ID == item.MessageID && projected.Type == threadtranscript.TypeMessage &&
			(projected.Source == "operator" || projected.Source == "model") {
			return true, nil
		}
	}
	return false, nil
}

func (s *SQLiteStore) GetThreadSearchMessage(ctx context.Context, threadID, messageID string) (domain.ThreadSearchMatch, error) {
	items, err := s.threadSearchCandidates(ctx, "", true, time.Time{}, "", threadID, messageID, 1)
	if err != nil {
		return domain.ThreadSearchMatch{}, err
	}
	// Public progress and replies inherited by a branch are commentary events,
	// not session messages. Resolve their full body through the same public
	// projection and redaction below without broadening search candidates.
	if len(items) == 0 {
		item := domain.ThreadSearchMatch{ID: messageID, MessageID: messageID, ThreadID: threadID, Role: "assistant"}
		var created string
		err := s.db.QueryRowContext(ctx, `SELECT thread.title, thread.status, thread.workspace_id,
		 COALESCE(json_extract(event.payload_json,'$.text'),''), event.created_at, binding.ordinal, event.sequence
		 FROM threads thread JOIN thread_runs binding ON binding.thread_id=thread.id
		 JOIN run_events event ON event.run_id=binding.run_id
		 WHERE thread.id=? AND thread.status IN ('active','archived') AND event.event_id=? AND event.type=?`,
			threadID, messageID, events.ModelPublicCommentaryEvent).Scan(&item.ThreadTitle, &item.ThreadStatus,
			&item.WorkspaceID, &item.Content, &created, &item.RunOrdinal, &item.Sequence)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.ThreadSearchMatch{}, err
		}
		if err == nil {
			item.CreatedAt = parseTS(created)
			items = append(items, item)
		}
	}
	if len(items) > 0 {
		item := items[0]
		visible, err := s.publicSearchMessage(ctx, item)
		if err != nil {
			return domain.ThreadSearchMatch{}, err
		}
		if visible {
			item.Content = redact.String(item.Content)
			return item, nil
		}
	}
	return domain.ThreadSearchMatch{}, apperror.New(apperror.CodeNotFound, "Saved public message was not found in this Thread")
}
