package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
)

const maxStoreReadPageLimit = 101

// SQLite's date functions retain only millisecond precision and RFC3339Nano
// strings have variable-width fractions. These expressions pad UTC fractions
// to nine digits so text comparison preserves the exact stored time.
func creationTimestampKeySQL(column string) string {
	return `(CASE
	WHEN instr(` + column + `, '.') = 0 THEN
		substr(` + column + `, 1, length(` + column + `) - 1) || '.000000000Z'
	ELSE
		substr(` + column + `, 1, instr(` + column + `, '.')) ||
		substr(substr(` + column + `, instr(` + column + `, '.') + 1,
			length(` + column + `) - instr(` + column + `, '.') - 1) || '000000000', 1, 9) || 'Z'
	END)`
}

var (
	runCreationTimestampKeySQL     = creationTimestampKeySQL("runs.created_at")
	sessionCreationTimestampKeySQL = creationTimestampKeySQL("sessions.created_at")
)

func creationTimestampKey(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func validateStoreCreationPage(beforeCreatedAt time.Time, beforeID string, limit int) error {
	if limit <= 0 || limit > maxStoreReadPageLimit {
		return fmt.Errorf("creation page limit must be between 1 and %d", maxStoreReadPageLimit)
	}
	if beforeCreatedAt.IsZero() {
		if beforeID != "" {
			return errors.New("creation page timestamp and identity must be provided together")
		}
		return nil
	}
	if strings.TrimSpace(beforeID) == "" || strings.TrimSpace(beforeID) != beforeID ||
		!utf8.ValidString(beforeID) || utf8.RuneCountInString(beforeID) > 256 {
		return errors.New("creation page identity is invalid")
	}
	return nil
}

func validateStoreReadPage(offset int, limit int) error {
	if err := validateStoreListOffset(offset); err != nil {
		return err
	}
	if limit <= 0 || limit > maxStoreReadPageLimit {
		return fmt.Errorf("read page limit must be between 1 and %d", maxStoreReadPageLimit)
	}
	return nil
}

func (s *SQLiteStore) ListSessionsPage(ctx context.Context, offset int, limit int) ([]session.Session, error) {
	if err := validateStoreReadPage(offset, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, title, route, status, created_at, updated_at
		FROM sessions ORDER BY `+sessionCreationTimestampKeySQL+` DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]session.Session, 0, limit)
	for rows.Next() {
		record, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListSessionsByCreationPage(ctx context.Context, beforeCreatedAt time.Time,
	beforeID string, limit int,
) ([]session.Session, error) {
	if err := validateStoreCreationPage(beforeCreatedAt, beforeID, limit); err != nil {
		return nil, err
	}
	query := `SELECT id, workspace_id, title, route, status, created_at, updated_at FROM sessions`
	var args []any
	if !beforeCreatedAt.IsZero() {
		query += ` WHERE (` + sessionCreationTimestampKeySQL + ` < ?
			OR (` + sessionCreationTimestampKeySQL + ` = ? AND id < ?))`
		anchor := creationTimestampKey(beforeCreatedAt)
		args = append(args, anchor, anchor, beforeID)
	}
	query += ` ORDER BY ` + sessionCreationTimestampKeySQL + ` DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]session.Session, 0, limit)
	for rows.Next() {
		record, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListWorkspacesPage(ctx context.Context, offset int, limit int) ([]WorkspaceRecord, error) {
	if err := validateStoreReadPage(offset, limit); err != nil {
		return nil, err
	}
	query := `SELECT id, name, root_path, created_at FROM workspaces`
	if s.hideDrydockWorkspaces {
		query += ` WHERE NOT EXISTS (SELECT 1 FROM drydock_workspaces drydock
			WHERE drydock.workspace_id = workspaces.id)
			AND NOT EXISTS (SELECT 1 FROM projectless_workspace_allocations local
			WHERE local.workspace_id = workspaces.id)`
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY name, id LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkspaceRecord, 0, limit)
	for rows.Next() {
		record, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListSessionMessagesPage(ctx context.Context, sessionID string,
	includeCompacted bool, offset int, limit int,
) ([]session.Message, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if err := validateStoreReadPage(offset, limit); err != nil {
		return nil, err
	}
	query := `SELECT id, session_id, role, content, provenance_version, source_kind, source_ref,
		content_sha256, instruction_authorized, token_estimate, compacted, created_at
		FROM session_messages WHERE session_id = ?`
	if !includeCompacted {
		query += ` AND compacted = 0`
	}
	query += ` ORDER BY id LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]session.Message, 0, limit)
	for rows.Next() {
		message, err := scanSessionMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListRecentSessionMessages(ctx context.Context, sessionID string,
	includeCompacted bool, limit int,
) ([]session.Message, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if err := validateStoreReadPage(0, limit); err != nil {
		return nil, err
	}
	query := `SELECT id, session_id, role, content, provenance_version, source_kind, source_ref,
		content_sha256, instruction_authorized, token_estimate, compacted, created_at
		FROM session_messages WHERE session_id = ?`
	if !includeCompacted {
		query += ` AND compacted = 0`
	}
	query += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]session.Message, 0, limit)
	for rows.Next() {
		message, err := scanSessionMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out, nil
}

func (s *SQLiteStore) ListFileEditPreviewsPage(ctx context.Context,
	filter fileedit.ListFilter, offset int, limit int,
) ([]fileedit.Preview, error) {
	if err := validateStoreReadPage(offset, limit); err != nil {
		return nil, err
	}
	query := `SELECT id, session_id, workspace_id, path, operation_kind, destination_path,
		status, diff_text, original_hash, proposed_hash, destination_original_hash,
		destination_proposed_hash, reason, secrets_redacted, created_at, updated_at
		FROM file_edits WHERE 1=1`
	var args []any
	if sessionID := strings.TrimSpace(filter.SessionID); sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	if workspaceID := strings.TrimSpace(filter.WorkspaceID); workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		if !fileedit.ValidStatus(status) {
			return nil, fmt.Errorf("invalid file edit status %q", status)
		}
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]fileedit.Preview, 0, limit)
	for rows.Next() {
		preview, err := scanFileEditPreview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, preview)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetFileEditPreview(ctx context.Context, id string) (fileedit.Preview, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, session_id, workspace_id, path,
		operation_kind, destination_path, status, diff_text, original_hash, proposed_hash,
		destination_original_hash, destination_proposed_hash,
		reason, secrets_redacted, created_at, updated_at
		FROM file_edits WHERE id = ?`, strings.TrimSpace(id))
	return scanFileEditPreview(row)
}

func (s *SQLiteStore) ListRunEventsPage(ctx context.Context, runID string,
	offset int, limit int,
) ([]events.Event, error) {
	var err error
	if runID, err = validateReadRunID(runID); err != nil {
		return nil, err
	}
	if err := validateStoreReadPage(offset, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, event_id, version, run_id, mission_id, sequence,
		type, source, subject_id, payload_json, created_at FROM run_events
		WHERE run_id = ? ORDER BY sequence LIMIT ? OFFSET ?`, runID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]events.Event, 0, limit)
	for rows.Next() {
		event, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListRunEventsAfterSequence(ctx context.Context, runID string,
	afterSequence int64, limit int,
) ([]events.Event, error) {
	var err error
	if runID, err = validateReadRunID(runID); err != nil {
		return nil, err
	}
	if afterSequence < 0 {
		return nil, errors.New("event sequence cursor cannot be negative")
	}
	if err := validateStoreReadPage(0, limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, event_id, version, run_id, mission_id, sequence,
		type, source, subject_id, payload_json, created_at FROM run_events
		WHERE run_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, runID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]events.Event, 0, limit)
	for rows.Next() {
		event, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LatestRunEventSequence(ctx context.Context,
	runID string,
) (int64, error) {
	var err error
	if runID, err = validateReadRunID(runID); err != nil {
		return 0, err
	}
	var sequence int64
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.sequence), 0)
		FROM runs r LEFT JOIN run_events e ON e.run_id = r.id
		WHERE r.id = ? GROUP BY r.id`, runID).Scan(&sequence)
	if err != nil {
		return 0, err
	}
	return sequence, nil
}

func validateReadRunID(runID string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || !utf8.ValidString(runID) || len([]rune(runID)) > 256 {
		return "", errors.New("run id is required and bounded")
	}
	return runID, nil
}

func (s *SQLiteStore) GetRunArtifactDescriptor(ctx context.Context, id string) (artifact.Descriptor, error) {
	id = strings.TrimSpace(id)
	if id == "" || !utf8.ValidString(id) || len([]rune(id)) > artifact.MaxIdentityRunes {
		return artifact.Descriptor{}, errors.New("artifact id is required and bounded")
	}
	return scanRunArtifactDescriptor(s.db.QueryRowContext(ctx, runArtifactDescriptorSelect+` WHERE id = ?`, id))
}
