package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

// Both search and exact reads apply these predicates inside the same read
// transaction as the validated Thread lineage. Candidates are deliberately
// absent: only confirmed context_memories are eligible.
const historyMemoryVisibleSQL = `m.status='active' AND
	((m.scope='user' AND m.scope_id='local-user') OR (m.scope='project' AND m.scope_id=? AND ?!=''))
	AND (m.retention_until IS NULL OR julianday(m.retention_until)>julianday(?))`

// This is the existing root viewer rule applied separately to each legal Run.
// A specialist's owner-only note remains private even in the same Thread.
const historyNoteVisibleSQL = `n.status='active' AND
	(n.visibility IN ('run','root') OR (n.visibility='owner' AND
	(n.owner='root' OR EXISTS(SELECT 1 FROM agent_nodes a JOIN runs r ON r.id=a.run_id
	 WHERE a.id=n.owner_agent_id AND a.run_id=n.run_id AND a.role='root'
	 AND a.parent_id IS NULL AND a.session_id=r.session_id))))`

type historyMutableRef struct {
	Run        string `json:"r"`
	Workspace  string `json:"w"`
	ID         string `json:"i"`
	Version    int64  `json:"v"`
	ContentSHA string `json:"c"`
	StateSHA   string `json:"s"`
}

func mutableHistorySource(prefix string, scope historyScope, id string, version int64, content string, state any) string {
	encoded, _ := json.Marshal(state)
	return prefix + encodeHistoryToken(historyMutableRef{Run: session.ContentSHA256(scope.runID), Workspace: session.ContentSHA256(scope.workspaceID),
		ID: base64.RawURLEncoding.EncodeToString([]byte(id)), Version: version, ContentSHA: session.ContentSHA256(content), StateSHA: session.ContentSHA256(string(encoded))})
}

func readHistoryMutableSource(ctx context.Context, q historyQueryer, scope historyScope, source string) (domain.HistoryRecord, map[string]string, error) {
	prefix := "memory:"
	if strings.HasPrefix(source, "note:") {
		prefix = "note:"
	}
	var ref historyMutableRef
	if decodeHistoryToken(strings.TrimPrefix(source, prefix), &ref) != nil || ref.Run != session.ContentSHA256(scope.runID) ||
		ref.Workspace != session.ContentSHA256(scope.workspaceID) || ref.Version < 1 ||
		!validStoreDigest(ref.ContentSHA) || !validStoreDigest(ref.StateSHA) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	id, err := base64.RawURLEncoding.DecodeString(ref.ID)
	if err != nil || len(id) == 0 || len(id) > 256 {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	var record domain.HistoryRecord
	var parts map[string]string
	if prefix == "memory:" {
		record, parts, err = readHistoryMemory(ctx, q, scope, "id=?", string(id))
	} else {
		record, parts, err = readHistoryNote(ctx, q, scope, "id=?", string(id))
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	// The token seals the version and complete source state as well as the
	// content digest. A metadata-only update or delete/recreate cannot silently
	// turn an earlier source_id into the current record, even at byte offset 0.
	if record.SourceID != prefix+encodeHistoryToken(ref) {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeConflict, "memory or note source changed; search again for its current version")
	}
	return record, parts, nil
}

func readHistoryMemory(ctx context.Context, q historyQueryer, scope historyScope, where string, key any) (domain.HistoryRecord, map[string]string, error) {
	memory, err := scanContextMemory(q.QueryRowContext(ctx, contextMemorySelect+`m WHERE `+where+` AND `+historyMemoryVisibleSQL,
		key, scope.workspaceID, scope.workspaceID, ts(time.Now().UTC())))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	record := domain.HistoryRecord{SourceID: mutableHistorySource("memory:", scope, memory.ID, memory.Version, memory.Content, memory),
		Kind: "context_memory", MemoryID: memory.ID, SourceVersion: memory.Version, Scope: string(memory.Scope), ScopeID: memory.ScopeID,
		Title: redact.String(memory.Title), ProvenanceVersion: memory.ProtocolVersion, SourceKind: memory.SourceKind,
		SourceRef: memory.SourceRef, ContentSHA256: memory.ContentSHA256, Status: string(memory.Status),
		CreatedAt: ts(memory.CreatedAt), UpdatedAt: ts(memory.UpdatedAt)}
	if memory.RetentionUntil != nil {
		record.RetentionUntil = ts(*memory.RetentionUntil)
	}
	record, err = safeHistoryMetadata(record)
	return record, map[string]string{"content": memory.Content}, err
}

func readHistoryNote(ctx context.Context, q historyQueryer, scope historyScope, where string, key any) (domain.HistoryRecord, map[string]string, error) {
	note, err := scanNote(q.QueryRowContext(ctx, noteSelect+` n WHERE `+where+`
		AND n.run_id IN (SELECT run_id FROM thread_runs WHERE thread_id=?) AND `+historyNoteVisibleSQL, key, scope.threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	if err := note.Validate(); err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	var sessionID string
	for linked, run := range scope.sessions {
		if run == note.RunID {
			sessionID = linked
			break
		}
	}
	if sessionID == "" {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	// References and evidence relations are not part of the returned body or
	// routing metadata. The canonical note ID supplies provenance without
	// projecting arbitrary source_refs or evidence links as trusted sources.
	record, err := safeHistoryMetadata(domain.HistoryRecord{
		SourceID: mutableHistorySource("note:", scope, note.ID, note.Version, note.Content, note),
		Kind:     "note", NoteID: note.ID, RunID: note.RunID, SessionID: sessionID, SourceVersion: note.Version,
		Title: redact.String(note.Title), Visibility: string(note.Visibility), ProvenanceVersion: "note_context.v1",
		SourceKind: "run_note", SourceRef: note.ID, ContentSHA256: session.ContentSHA256(note.Content), Status: string(note.Status),
		CreatedAt: ts(note.CreatedAt), UpdatedAt: ts(note.UpdatedAt)})
	return record, map[string]string{"content": note.Content}, err
}
