package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

type historyScope struct {
	threadID    string
	runID       string
	workspaceID string
	sessions    map[string]string // exact Session -> Run, validated as one predecessor chain
	ordinals    map[string]int
}

type historyQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type historyCursor struct {
	Version       int              `json:"v,omitempty"`
	Run           string           `json:"r"`
	Workspace     string           `json:"w,omitempty"`
	QuerySHA      string           `json:"q"`
	MaxMessage    int64            `json:"m"`
	MaxTool       int64            `json:"t"`
	MaxEvent      int64            `json:"e"`
	MaxSummary    int64            `json:"s,omitempty"`
	MaxContinuity int64            `json:"c,omitempty"`
	MaxMemory     int64            `json:"mm,omitempty"`
	MaxNote       int64            `json:"n,omitempty"`
	SnapshotAt    string           `json:"at,omitempty"`
	Offset        int              `json:"o"`
	Position      *historyPosition `json:"p,omitempty"`
}

type historyPosition struct {
	Ordinal int    `json:"o"`
	Stamp   string `json:"s"`
	Kind    string `json:"k"`
	ID      int64  `json:"i"`
}

type historyToolRef struct {
	Run     string `json:"r"`
	Turn    int    `json:"t"`
	Attempt string `json:"a"`
	Call    string `json:"c"`
}

// beginThreadRequestObservation uses a dedicated, deferred read transaction:
// the normal store DSN is BEGIN IMMEDIATE even for TxOptions.ReadOnly.
func (s *SQLiteStore) historyScope(ctx context.Context, q historyQueryer, runID string) (historyScope, error) {
	value := historyScope{runID: runID, sessions: map[string]string{}, ordinals: map[string]int{}}
	if !domain.ValidAgentID(runID) {
		return value, apperror.New(apperror.CodeInvalidArgument, "history recall requires a valid current Run")
	}
	var workspaceID, missionID string
	err := q.QueryRowContext(ctx, `SELECT t.id,t.workspace_id,t.mission_id FROM threads t
		JOIN thread_runs b ON b.thread_id=t.id AND b.run_id=?
		WHERE t.last_run_id=b.run_id AND t.status='active'
		AND (t.active_run_id IS NULL OR t.active_run_id='' OR t.active_run_id=b.run_id)`, runID).
		Scan(&value.threadID, &workspaceID, &missionID)
	if errors.Is(err, sql.ErrNoRows) {
		return value, apperror.New(apperror.CodeFailedPrecondition, "history recall requires the current Run of an open Thread")
	}
	if err != nil {
		return value, err
	}
	value.workspaceID = workspaceID
	rows, err := q.QueryContext(ctx, `SELECT b.run_id,b.session_id,b.ordinal,COALESCE(b.predecessor_run_id,''),
		r.session_id,r.mission_id,COALESCE(m.workspace_id,''),COALESCE(s.workspace_id,''),
		(SELECT COUNT(*) FROM thread_events e WHERE e.thread_id=b.thread_id AND e.run_id=b.run_id
		 AND e.type='thread.run_successor_created' AND e.source='thread_continuation'
		 AND json_extract(e.payload_json,'$.predecessor_run_id')=b.predecessor_run_id
		 AND json_extract(e.payload_json,'$.successor_run_id')=b.run_id)
		FROM thread_runs b LEFT JOIN runs r ON r.id=b.run_id
		LEFT JOIN missions m ON m.id=r.mission_id LEFT JOIN sessions s ON s.id=b.session_id
		WHERE b.thread_id=? ORDER BY b.ordinal LIMIT 1025`, value.threadID)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	previous, count := "", 0
	for rows.Next() {
		var boundRun, boundSession, predecessor, actualSession, actualMission, actualWorkspace, sessionWorkspace string
		var ordinal, events int
		if err := rows.Scan(&boundRun, &boundSession, &ordinal, &predecessor, &actualSession, &actualMission, &actualWorkspace, &sessionWorkspace, &events); err != nil {
			return value, err
		}
		count++
		if count > 1024 {
			return value, apperror.New(apperror.CodeResourceExhausted, "history lineage exceeds the bounded recall scope")
		}
		if ordinal != count || predecessor != previous || actualSession != boundSession || actualMission != missionID || actualWorkspace != workspaceID || sessionWorkspace != workspaceID || (ordinal > 1 && events != 1) {
			return value, apperror.New(apperror.CodeFailedPrecondition, "history recall predecessor or Session provenance is inconsistent")
		}
		value.sessions[boundSession] = boundRun
		value.ordinals[boundRun] = ordinal
		previous = boundRun
	}
	if err := rows.Err(); err != nil {
		return value, err
	}
	if previous != runID || count == 0 {
		return value, apperror.New(apperror.CodeFailedPrecondition, "history recall does not end at the current Run")
	}
	return value, nil
}

// SearchThreadHistory scans at most 256 original records per request. An empty
// match page with has_more=true is not a claim that older history has no match.
// Cursor watermarks exclude newly appended records and tool results sealed
// later, even when those calls were already pending at the first page. The
// exact persisted result event (not the caller-supplied completion timestamp)
// provides that latter boundary. Cursors are never authority.
func (s *SQLiteStore) SearchThreadHistory(ctx context.Context, runID string, request domain.HistorySearchRequest) (domain.HistorySearchResult, error) {
	q, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return domain.HistorySearchResult{}, err
	}
	defer finish()
	return s.searchThreadHistory(ctx, q, runID, request)
}

func (s *SQLiteStore) searchThreadHistory(ctx context.Context, q historyQueryer, runID string, request domain.HistorySearchRequest) (domain.HistorySearchResult, error) {
	value := domain.HistorySearchResult{Version: domain.HistoryRecallVersion, RunID: runID, SearchMode: "bounded_literal_substring", ScanLimit: domain.HistorySearchScanLimit, Records: []domain.HistoryRecord{}}
	if !utf8.ValidString(request.Query) || utf8.RuneCountInString(request.Query) > 256 || strings.ContainsRune(request.Query, 0) || request.Limit < 0 || request.Limit > domain.HistorySearchMaxResults {
		return value, apperror.New(apperror.CodeInvalidArgument, "history search requires a bounded literal query and limit; an empty query browses history")
	}
	if request.Limit == 0 {
		request.Limit = domain.HistorySearchMaxResults
	}
	scope, err := s.historyScope(ctx, q, runID)
	if err != nil {
		return value, err
	}
	value.ThreadID = scope.threadID
	cursor := historyCursor{Version: 2, Run: runID, Workspace: session.ContentSHA256(scope.workspaceID), QuerySHA: session.ContentSHA256(request.Query), SnapshotAt: ts(time.Now().UTC())}
	if request.Cursor != "" {
		cursor = historyCursor{}
		if len(request.Cursor) > 2048 || decodeHistoryToken(request.Cursor, &cursor) != nil ||
			cursor.Run != runID || cursor.QuerySHA != session.ContentSHA256(request.Query) ||
			cursor.Offset < 0 || cursor.Offset > 1000000 || cursor.MaxMessage < 0 || cursor.MaxTool < 0 ||
			cursor.MaxEvent < 0 || cursor.MaxSummary < 0 || cursor.MaxContinuity < 0 || cursor.MaxMemory < 0 || cursor.MaxNote < 0 ||
			(cursor.Version != 0 && cursor.Version != 2) ||
			(cursor.Version == 2 && (cursor.Workspace != session.ContentSHA256(scope.workspaceID) || parseTS(cursor.SnapshotAt).IsZero())) ||
			(cursor.Position != nil && (cursor.Version != 2 || cursor.Position.Ordinal < 1 || cursor.Position.ID < 1 || parseTS(cursor.Position.Stamp).IsZero())) {
			return value, apperror.New(apperror.CodeInvalidArgument, "history search cursor does not match this Run and query")
		}
	} else {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(m.id),0) FROM session_messages m JOIN thread_runs b ON b.session_id=m.session_id WHERE b.thread_id=?`, scope.threadID).Scan(&cursor.MaxMessage); err != nil {
			return value, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(c.rowid),0) FROM run_supervisor_tool_calls c JOIN thread_runs b ON b.run_id=c.run_id WHERE b.thread_id=?`, scope.threadID).Scan(&cursor.MaxTool); err != nil {
			return value, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.id),0) FROM run_events e JOIN thread_runs b ON b.run_id=e.run_id WHERE b.thread_id=?`, scope.threadID).Scan(&cursor.MaxEvent); err != nil {
			return value, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(s.id),0) FROM context_summaries s JOIN thread_runs b ON b.session_id=s.task_id WHERE b.thread_id=?`, scope.threadID).Scan(&cursor.MaxSummary); err != nil {
			return value, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(r.rowid),0) FROM runs r JOIN thread_runs b ON b.run_id=r.id WHERE b.thread_id=?`, scope.threadID).Scan(&cursor.MaxContinuity); err != nil {
			return value, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM context_memories WHERE (scope='user' AND scope_id='local-user') OR (scope='project' AND scope_id=? AND ?!='')`, scope.workspaceID, scope.workspaceID).Scan(&cursor.MaxMemory); err != nil {
			return value, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(n.rowid),0) FROM notes n JOIN thread_runs b ON b.run_id=n.run_id WHERE b.thread_id=?`, scope.threadID).Scan(&cursor.MaxNote); err != nil {
			return value, err
		}
	}
	value.SnapshotCursor = encodeHistoryToken(cursor)
	// Only identities are materialized. SQLite checks the original stored text;
	// at most the requested matches are loaded into Go for verified projection.
	position, positioned := historyPosition{}, 0
	if cursor.Position != nil {
		position, positioned = *cursor.Position, 1
	}
	rows, err := q.QueryContext(ctx, `WITH candidates AS MATERIALIZED (
		SELECT 'message' AS kind,m.id AS id,b.ordinal AS ordinal,m.created_at AS stamp FROM session_messages m
		JOIN thread_runs b ON b.session_id=m.session_id WHERE b.thread_id=? AND m.id<=?
		UNION ALL
		SELECT 'tool_call',c.rowid,b.ordinal,c.created_at FROM run_supervisor_tool_calls c
		JOIN thread_runs b ON b.run_id=c.run_id WHERE b.thread_id=? AND c.rowid<=?
		AND c.status IN ('completed','failed','denied') AND c.completed_at IS NOT NULL
		AND c.tool_name NOT IN ('history_search','history_read')
		AND EXISTS(SELECT 1 FROM run_events e WHERE e.id<=? AND e.run_id=c.run_id
		 AND e.source='run_supervisor' AND e.type='supervisor.tool_result_recorded' AND e.subject_id=c.call_id
		 AND json_extract(e.payload_json,'$.turn')=c.turn AND json_extract(e.payload_json,'$.attempt_id')=c.attempt_id
		 AND json_extract(e.payload_json,'$.round')=c.round AND json_extract(e.payload_json,'$.status')=c.status)
		UNION ALL
		SELECT 'summary',s.id,b.ordinal,s.created_at FROM context_summaries s
		JOIN thread_runs b ON b.session_id=s.task_id WHERE b.thread_id=? AND s.id<=?
		UNION ALL
		SELECT 'continuity',r.rowid,b.ordinal,r.created_at FROM runs r
		JOIN thread_runs b ON b.run_id=r.id WHERE b.thread_id=? AND r.rowid<=?
		AND json_type(r.config_json,'$.continuity_context')='object'
		UNION ALL
		SELECT 'context_memory',m.rowid,?,m.updated_at FROM context_memories m
		WHERE ?=2 AND m.rowid<=? AND m.updated_at<=? AND `+historyMemoryVisibleSQL+`
		UNION ALL
		SELECT 'note',n.rowid,b.ordinal,n.updated_at FROM notes n JOIN thread_runs b ON b.run_id=n.run_id
		WHERE ?=2 AND b.thread_id=? AND n.rowid<=? AND n.updated_at<=? AND `+historyNoteVisibleSQL+`
	), selected AS MATERIALIZED (SELECT * FROM candidates WHERE ?=0 OR ordinal<? OR
		(ordinal=? AND (stamp<? OR (stamp=? AND (kind>? OR (kind=? AND id<?)))))
		ORDER BY ordinal DESC,stamp DESC,kind,id DESC LIMIT ? OFFSET ?)
	SELECT kind,id,ordinal,stamp,CASE WHEN kind='message' THEN
		(SELECT CASE WHEN instr(m.content,?)>0 THEN 'content' ELSE '' END FROM session_messages m WHERE m.id=selected.id)
		WHEN kind='summary' THEN
		(SELECT CASE WHEN instr(s.content,?)>0 THEN 'content' ELSE '' END FROM context_summaries s WHERE s.id=selected.id)
		WHEN kind='continuity' THEN
		(SELECT CASE WHEN instr(COALESCE(json_extract(r.config_json,'$.continuity_context.summary_content'),''),?)>0 THEN 'summary'
		 WHEN instr(json_extract(r.config_json,'$.continuity_context'),?)>0 THEN 'content' ELSE '' END FROM runs r WHERE r.rowid=selected.id)
		WHEN kind='context_memory' THEN
		(SELECT CASE WHEN instr(m.content,?)>0 THEN 'content' WHEN instr(m.title,?)>0 THEN 'title' ELSE '' END FROM context_memories m WHERE m.rowid=selected.id)
		WHEN kind='note' THEN
		(SELECT CASE WHEN instr(n.content,?)>0 THEN 'content' WHEN instr(n.title,?)>0 THEN 'title' ELSE '' END FROM notes n WHERE n.rowid=selected.id)
		ELSE (SELECT CASE WHEN instr(c.result_json,?)>0 THEN 'result' WHEN instr(c.payload_json,?)>0 OR instr(c.tool_name,?)>0 THEN 'arguments' ELSE '' END FROM run_supervisor_tool_calls c WHERE c.rowid=selected.id) END
	FROM selected ORDER BY ordinal DESC,stamp DESC,kind,id DESC`, scope.threadID, cursor.MaxMessage, scope.threadID, cursor.MaxTool, cursor.MaxEvent,
		scope.threadID, cursor.MaxSummary, scope.threadID, cursor.MaxContinuity,
		len(scope.sessions), cursor.Version, cursor.MaxMemory, cursor.SnapshotAt, scope.workspaceID, scope.workspaceID, ts(time.Now().UTC()),
		cursor.Version, scope.threadID, cursor.MaxNote, cursor.SnapshotAt,
		positioned, position.Ordinal, position.Ordinal, position.Stamp, position.Stamp, position.Kind, position.Kind, position.ID,
		domain.HistorySearchScanLimit+1, cursor.Offset,
		request.Query, request.Query, request.Query, request.Query, request.Query, request.Query, request.Query, request.Query, request.Query, request.Query, request.Query)
	if err != nil {
		return value, err
	}
	type candidate struct {
		kind     string
		id       int64
		part     string
		position historyPosition
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.kind, &c.id, &c.position.Ordinal, &c.position.Stamp, &c.part); err != nil {
			_ = rows.Close()
			return value, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return value, err
	}
	for _, c := range candidates {
		if value.Scanned >= domain.HistorySearchScanLimit || len(value.Records) >= request.Limit {
			break
		}
		value.Scanned++
		c.position.Kind, c.position.ID = c.kind, c.id
		if cursor.Version == 2 {
			cursor.Position = &c.position
		}
		if c.part == "" {
			continue
		}
		var record domain.HistoryRecord
		var parts map[string]string
		switch c.kind {
		case "message":
			record, parts, err = readHistoryMessage(ctx, q, scope, c.id)
		case "summary":
			record, parts, err = readHistorySummary(ctx, q, scope, c.id)
		case "continuity":
			record, parts, err = readHistoryContinuity(ctx, q, scope, "r.rowid=?", "", c.id)
		case "context_memory":
			record, parts, err = readHistoryMemory(ctx, q, scope, "rowid=?", c.id)
		case "note":
			record, parts, err = readHistoryNote(ctx, q, scope, "rowid=?", c.id)
		default:
			record, parts, err = readHistoryTool(ctx, q, scope, "c.rowid=?", c.id)
		}
		// A locally held fork can name a foreign source. It contributes no
		// readable history, but must not hide valid local messages. Count the
		// candidate so continuation still advances over the same watermark.
		if c.kind == "continuity" && apperror.CodeOf(err) == apperror.CodeNotFound {
			continue
		}
		if err != nil {
			return value, err
		}
		record.MatchedPart = c.part
		if c.part == "title" {
			record.Excerpt = historyExcerpt(record.Title, request.Query)
		} else {
			record.Excerpt = historyExcerpt(parts[c.part], request.Query)
		}
		value.Records = append(value.Records, record)
	}
	value.HasMore = value.Scanned < len(candidates)
	if value.HasMore {
		if cursor.Version == 0 {
			cursor.Offset += value.Scanned
		}
		value.NextCursor = encodeHistoryToken(cursor)
	}
	return value, nil
}

func (s *SQLiteStore) ReadThreadHistory(ctx context.Context, runID string, request domain.HistoryReadRequest) (domain.HistoryReadResult, error) {
	q, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return domain.HistoryReadResult{}, err
	}
	defer finish()
	return s.readThreadHistory(ctx, q, runID, request)
}

func (s *SQLiteStore) readThreadHistory(ctx context.Context, q historyQueryer, runID string, request domain.HistoryReadRequest) (domain.HistoryReadResult, error) {
	value := domain.HistoryReadResult{Version: domain.HistoryRecallVersion, RunID: runID, Part: request.Part, Offset: request.Offset}
	if len(request.SourceID) > 2048 || request.Offset < 0 || request.Limit < 0 || request.Limit > domain.HistoryReadMaxBytes || (request.Offset > 0 && request.ExpectedSHA256 == "") {
		return value, apperror.New(apperror.CodeInvalidArgument, "history read requires bounded paging and an expected digest after the first page")
	}
	if request.Limit == 0 {
		request.Limit = domain.HistoryReadDefaultBytes
	}
	if request.Limit < 4 {
		return value, apperror.New(apperror.CodeInvalidArgument, "history page must fit one UTF-8 character")
	}
	scope, err := s.historyScope(ctx, q, runID)
	if err != nil {
		return value, err
	}
	value.ThreadID = scope.threadID
	var parts map[string]string
	if strings.HasPrefix(request.SourceID, "memory:") || strings.HasPrefix(request.SourceID, "note:") {
		value.Record, parts, err = readHistoryMutableSource(ctx, q, scope, request.SourceID)
	} else if strings.HasPrefix(request.SourceID, "message:") {
		id, parseErr := strconv.ParseInt(strings.TrimPrefix(request.SourceID, "message:"), 10, 64)
		if parseErr != nil || id <= 0 {
			return value, historyNotFound()
		}
		value.Record, parts, err = readHistoryMessage(ctx, q, scope, id)
	} else if strings.HasPrefix(request.SourceID, "summary:") {
		id, parseErr := strconv.ParseInt(strings.TrimPrefix(request.SourceID, "summary:"), 10, 64)
		if parseErr != nil || id <= 0 {
			return value, historyNotFound()
		}
		value.Record, parts, err = readHistorySummary(ctx, q, scope, id)
	} else if strings.HasPrefix(request.SourceID, "continuity:") {
		var ref historyContinuityRef
		if decodeHistoryToken(strings.TrimPrefix(request.SourceID, "continuity:"), &ref) != nil || !domain.ValidAgentID(ref.Run) || !validStoreDigest(ref.Fingerprint) {
			return value, historyNotFound()
		}
		value.Record, parts, err = readHistoryContinuity(ctx, q, scope, "r.id=?", ref.Fingerprint, ref.Run)
	} else if strings.HasPrefix(request.SourceID, "artifact:") {
		value.Record, parts, err = readHistoryMCPArtifact(ctx, q, scope, strings.TrimPrefix(request.SourceID, "artifact:"))
	} else if strings.HasPrefix(request.SourceID, "tool:") {
		var ref historyToolRef
		if decodeHistoryToken(strings.TrimPrefix(request.SourceID, "tool:"), &ref) != nil {
			return value, historyNotFound()
		}
		value.Record, parts, err = readHistoryTool(ctx, q, scope, "c.run_id=? AND c.turn=? AND c.attempt_id=? AND c.call_id=?", ref.Run, ref.Turn, ref.Attempt, ref.Call)
	} else {
		return value, historyNotFound()
	}
	if err != nil {
		return value, err
	}
	if request.Part == "" {
		request.Part = "content"
		if value.Record.Kind == "tool_call" {
			request.Part = "result"
		}
		value.Part = request.Part
	}
	content, ok := parts[request.Part]
	if !ok {
		return value, apperror.New(apperror.CodeInvalidArgument, "history part must be content for a message/summary/memory/note, content/summary for continuity, or arguments/result for a tool call")
	}
	value.ContentSHA256 = session.ContentSHA256(content)
	if request.ExpectedSHA256 != "" && request.ExpectedSHA256 != value.ContentSHA256 {
		return value, apperror.New(apperror.CodeConflict, "history content does not match the expected digest")
	}
	if request.Offset > len(content) || (request.Offset < len(content) && !utf8.RuneStart(content[request.Offset])) {
		return value, apperror.New(apperror.CodeInvalidArgument, "history offset is not a UTF-8 byte boundary")
	}
	end := min(len(content), request.Offset+request.Limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	value.Content = content[request.Offset:end]
	value.NextOffset = end
	value.TotalBytes = len(content)
	value.HasMore = end < len(content)
	return value, nil
}

func readHistoryMCPArtifact(ctx context.Context, q historyQueryer, scope historyScope, id string) (domain.HistoryRecord, map[string]string, error) {
	if !domain.ValidAgentID(id) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	blob, err := getRunArtifactRow(q.QueryRowContext(ctx, runArtifactSelect+` WHERE id=?
		AND tool_name IN ('mcp_tool_call','mcp_reference') AND stream='stdout' AND mime='application/json'
		AND run_id IN (SELECT run_id FROM thread_runs WHERE thread_id=?)`, id, scope.threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	if scope.sessions[blob.SessionID] != blob.RunID || blob.WorkspaceID != scope.workspaceID {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	record, err := safeHistoryMetadata(domain.HistoryRecord{SourceID: "artifact:" + id, Kind: "tool_output_artifact",
		RunID: blob.RunID, SessionID: blob.SessionID, SourceKind: session.SourceToolResult, SourceRef: blob.SourceID,
		ToolName: blob.ToolName, ContentSHA256: blob.SHA256, CreatedAt: ts(blob.CreatedAt)})
	return record, map[string]string{"content": blob.Content}, err
}

func readHistoryMessage(ctx context.Context, q historyQueryer, scope historyScope, id int64) (domain.HistoryRecord, map[string]string, error) {
	message, err := scanSessionMessage(q.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at FROM session_messages WHERE id=? AND session_id IN (SELECT session_id FROM thread_runs WHERE thread_id=?)`, id, scope.threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	runID, ok := scope.sessions[message.SessionID]
	if !ok {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	record, err := safeHistoryMetadata(domain.HistoryRecord{SourceID: "message:" + strconv.FormatInt(id, 10), Kind: "message", MessageID: id, RunID: runID, SessionID: message.SessionID, Role: message.Role, ProvenanceVersion: message.Provenance.Version, SourceKind: message.Provenance.SourceKind, SourceRef: message.Provenance.SourceRef, OriginalInstructionAuthorized: message.Provenance.InstructionAuthorized, ContentSHA256: session.ContentSHA256(message.Content), Compacted: message.Compacted, CreatedAt: ts(message.CreatedAt)})
	return record, map[string]string{"content": message.Content}, err
}

func readHistoryTool(ctx context.Context, q historyQueryer, scope historyScope, where string, args ...any) (domain.HistoryRecord, map[string]string, error) {
	args = append(args, scope.threadID)
	call, err := scanSupervisorToolCall(q.QueryRowContext(ctx, `SELECT c.run_id,c.turn,c.attempt_id,c.round,c.position,c.model_attempt,c.call_id,c.stream_response_id,c.stream_item_id,c.stream_call_id,c.tool_name,c.payload_json,c.authority_json,c.status,c.result_json,c.error_code,c.created_at,c.completed_at
		FROM run_supervisor_tool_calls c JOIN run_supervisor_tool_rounds r ON r.run_id=c.run_id AND r.turn=c.turn AND r.attempt_id=c.attempt_id AND r.round=c.round AND r.model_attempt=c.model_attempt
		WHERE `+where+` AND c.run_id IN (SELECT run_id FROM thread_runs WHERE thread_id=?) AND c.status IN ('completed','failed','denied') AND c.completed_at IS NOT NULL AND c.tool_name NOT IN ('history_search','history_read')`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	var sessionID string
	for linked, run := range scope.sessions {
		if run == call.RunID {
			sessionID = linked
			break
		}
	}
	if sessionID == "" {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	ref := historyToolRef{Run: call.RunID, Turn: call.Turn, Attempt: call.AttemptID, Call: call.CallID}
	record, err := safeHistoryMetadata(domain.HistoryRecord{SourceID: "tool:" + encodeHistoryToken(ref), Kind: "tool_call", RunID: call.RunID, SessionID: sessionID, SourceKind: session.SourceToolResult, SourceRef: call.CallID, CallID: call.CallID, Turn: call.Turn, AttemptID: call.AttemptID, Round: call.Round, ToolName: call.ToolName, Status: string(call.Status), ErrorCode: call.ErrorCode, ArgumentsSHA256: session.ContentSHA256(call.PayloadJSON), ResultSHA256: session.ContentSHA256(call.ResultJSON), CreatedAt: ts(call.CreatedAt), CompletedAt: ts(*call.CompletedAt)})
	return record, map[string]string{"arguments": call.PayloadJSON, "result": call.ResultJSON}, err
}

// Stored bodies were redacted before sealing. Provenance references are a
// separate display field and may contain a path with a secret-shaped value.
// Redact that metadata once, preserving its original digest. Never rewrite an
// exact routing identity into a different identity to make it look safe.
func safeHistoryMetadata(record domain.HistoryRecord) (domain.HistoryRecord, error) {
	for _, id := range []string{record.SourceID, record.RunID, record.SessionID, record.CallID, record.AttemptID, record.Role, record.SourceKind, record.ProvenanceVersion, record.ToolName, record.Status, record.NoteID, record.MemoryID, record.Scope, record.ScopeID, record.Visibility} {
		if redact.String(id) != id {
			return domain.HistoryRecord{}, apperror.New(apperror.CodeFailedPrecondition, "history source metadata cannot be safely projected with its exact identity")
		}
	}
	if record.SourceRef != "" {
		record.SourceRefSHA256 = session.ContentSHA256(record.SourceRef)
		safe := redact.String(record.SourceRef)
		record.SourceRefRedacted = safe != record.SourceRef
		record.SourceRef = safe
	}
	record.ErrorCode = redact.String(record.ErrorCode)
	return record, nil
}

func historyNotFound() error {
	return apperror.New(apperror.CodeNotFound, "history source is not available in the current Thread")
}
func encodeHistoryToken(value any) string {
	encoded, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(encoded)
}
func decodeHistoryToken(value string, target any) error {
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}
func historyExcerpt(content, query string) string {
	position := strings.Index(content, query)
	if position < 0 {
		position = 0
	}
	start := max(0, position-96)
	for start > 0 && !utf8.RuneStart(content[start]) {
		start--
	}
	end := min(len(content), position+len(query)+192)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[start:end]
}
