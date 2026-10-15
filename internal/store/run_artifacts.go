package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolrun"
)

const defaultRunArtifactListLimit = 100

func (s *SQLiteStore) CaptureToolOutput(ctx context.Context, request artifact.CaptureRequest) ([]artifact.Descriptor, error) {
	normalized, err := artifact.NormalizeCaptureRequest(request)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	descriptors, err := captureToolOutputTx(ctx, tx, normalized)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return descriptors, nil
}

// Callers normalize the capture request before joining their receipt transaction.
func captureToolOutputTx(ctx context.Context, tx *sql.Tx, normalized artifact.CaptureRequest) ([]artifact.Descriptor, error) {
	binding, err := requireArtifactRunBindingTx(ctx, tx, normalized)
	if err != nil {
		return nil, err
	}
	if err := validateArtifactSourceTx(ctx, tx, normalized); err != nil {
		return nil, err
	}

	descriptors := make([]artifact.Descriptor, 0, len(normalized.Outputs))
	for _, output := range normalized.Outputs {
		existing, found, err := getRunArtifactBySourceTx(ctx, tx, normalized.RunID, normalized.SourceID, output.Stream)
		if err != nil {
			return nil, err
		}
		if found {
			if existing.ToolName != normalized.ToolName || existing.SessionID != normalized.SessionID ||
				existing.WorkspaceID != normalized.WorkspaceID || existing.MIME != output.MIME ||
				existing.Content != output.Content || existing.Redacted != output.Redacted {
				return nil, apperror.New(apperror.CodeConflict,
					"artifact source stream was already captured with different content or metadata")
			}
			descriptors = append(descriptors, existing.Descriptor)
			continue
		}

		now := time.Now().UTC()
		descriptor := artifact.Descriptor{
			ID: idgen.New("artifact"), RunID: normalized.RunID, SessionID: normalized.SessionID,
			WorkspaceID: normalized.WorkspaceID, SourceID: normalized.SourceID, ToolName: normalized.ToolName,
			Stream: output.Stream, Kind: artifact.KindToolOutput, MIME: output.MIME, Encoding: artifact.EncodingUTF8,
			SHA256: artifact.Hash(output.Content), SizeBytes: int64(len([]byte(output.Content))),
			Redacted: output.Redacted, CreatedAt: now,
		}
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_artifacts
			(id, run_id, session_id, workspace_id, source_id, tool_name, stream, kind, mime, encoding,
			 sha256, size_bytes, content, redacted, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			descriptor.ID, descriptor.RunID, descriptor.SessionID, descriptor.WorkspaceID,
			descriptor.SourceID, descriptor.ToolName, descriptor.Stream, descriptor.Kind, descriptor.MIME,
			descriptor.Encoding, descriptor.SHA256, descriptor.SizeBytes, output.Content,
			descriptor.Redacted, ts(descriptor.CreatedAt)); err != nil {
			return nil, err
		}
		event, err := events.New(descriptor.RunID, binding.MissionID, events.ArtifactCreatedEvent,
			"artifact_store", descriptor.ID, map[string]any{
				"artifact_id": descriptor.ID, "source_id": descriptor.SourceID, "session_id": descriptor.SessionID,
				"workspace_id": descriptor.WorkspaceID, "tool_name": descriptor.ToolName,
				"stream": descriptor.Stream, "kind": descriptor.Kind, "mime": descriptor.MIME,
				"encoding": descriptor.Encoding, "sha256": descriptor.SHA256,
				"size_bytes": descriptor.SizeBytes, "redacted": descriptor.Redacted,
			})
		if err != nil {
			return nil, err
		}
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return nil, err
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

func (s *SQLiteStore) GetRunArtifact(ctx context.Context, id string) (artifact.Blob, error) {
	id = strings.TrimSpace(id)
	if id == "" || !utf8.ValidString(id) || len([]rune(id)) > artifact.MaxIdentityRunes {
		return artifact.Blob{}, errors.New("artifact id is required and bounded")
	}
	return getRunArtifactRow(s.db.QueryRowContext(ctx, runArtifactSelect+` WHERE id = ?`, id))
}

func (s *SQLiteStore) ListRunArtifacts(ctx context.Context, filter artifact.ListFilter) ([]artifact.Descriptor, error) {
	filter.RunID = strings.TrimSpace(filter.RunID)
	filter.SourceID = strings.TrimSpace(filter.SourceID)
	for label, value := range map[string]string{"run id": filter.RunID, "source id": filter.SourceID} {
		if !utf8.ValidString(value) || len([]rune(value)) > artifact.MaxIdentityRunes {
			return nil, fmt.Errorf("artifact %s must be bounded UTF-8", label)
		}
	}
	if filter.Stream != "" && !filter.Stream.Valid() {
		return nil, fmt.Errorf("invalid artifact stream %q", filter.Stream)
	}
	if filter.Limit < 0 || filter.Limit > artifact.MaxListLimit {
		return nil, fmt.Errorf("artifact limit must be between 0 and %d", artifact.MaxListLimit)
	}
	if err := validateStoreListOffset(filter.Offset); err != nil {
		return nil, err
	}
	if filter.Limit == 0 {
		filter.Limit = defaultRunArtifactListLimit
	}
	query := runArtifactDescriptorSelect + ` WHERE 1=1`
	var args []any
	if filter.RunID != "" {
		query += ` AND run_id = ?`
		args = append(args, filter.RunID)
	}
	if filter.SourceID != "" {
		query += ` AND source_id = ?`
		args = append(args, filter.SourceID)
	}
	if filter.Stream != "" {
		query += ` AND stream = ?`
		args = append(args, filter.Stream)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var descriptors []artifact.Descriptor
	for rows.Next() {
		descriptor, err := scanRunArtifactDescriptor(rows)
		if err != nil {
			return nil, err
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, rows.Err()
}

func requireArtifactRunBindingTx(ctx context.Context, tx *sql.Tx, request artifact.CaptureRequest) (runBinding, error) {
	var binding runBinding
	var sessionID string
	var workspaceID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT runs.mission_id, runs.session_id, missions.workspace_id
		FROM runs JOIN missions ON missions.id = runs.mission_id WHERE runs.id = ?`, request.RunID).
		Scan(&binding.MissionID, &sessionID, &workspaceID); err != nil {
		return runBinding{}, err
	}
	binding.RunID = request.RunID
	binding.WorkspaceID = workspaceID.String
	if sessionID != request.SessionID || workspaceID.String != request.WorkspaceID {
		return runBinding{}, errors.New("artifact scope does not match its Run, Session, and Workspace binding")
	}
	return binding, nil
}

func validateArtifactSourceTx(ctx context.Context, tx *sql.Tx, request artifact.CaptureRequest) error {
	requested := make(map[artifact.Stream]artifact.Output, len(request.Outputs))
	for _, output := range request.Outputs {
		requested[output.Stream] = output
	}
	var status, stdout, stderr string
	switch request.ToolName {
	case mcpReferenceArtifactSource:
		output, found := requested[artifact.StreamStdout]
		if len(requested) != 1 || !found || output.MIME != "application/json" || !output.Redacted || !json.Valid([]byte(output.Content)) {
			return errors.New("MCP selection artifacts require one sanitized JSON result")
		}
		var runID, sessionID, workspaceID string
		if err := tx.QueryRowContext(ctx, `SELECT run.id,run.session_id,COALESCE(mission.workspace_id,'')
		 FROM thread_mcp_references ref JOIN thread_message_intents intent ON intent.operation_key_digest=ref.intent_digest
		 JOIN runs run ON run.id=intent.run_id JOIN missions mission ON mission.id=run.mission_id
		 WHERE ref.operation_id=? AND intent.rejected=0 AND (ref.state='started' OR (ref.state='completed' AND ref.receipt='result_received'))`, request.SourceID).
			Scan(&runID, &sessionID, &workspaceID); err != nil {
			return err
		}
		if runID != request.RunID || sessionID != request.SessionID || workspaceID != request.WorkspaceID {
			return errors.New("artifact source scope does not match the stored MCP selection")
		}
		return nil
	case "read_file", "list_workspace",
		"mcp_tool_call",
		"workspace_list", "workspace_read", "workspace_glob", "workspace_grep",
		"workspace_change", "workspace_apply", "workspace_delete":
		var runID, sessionID, workspaceID, toolName string
		if err := tx.QueryRowContext(ctx, `SELECT run_id, session_id, workspace_id, tool_name
			FROM run_tool_calls WHERE id = ?`, request.SourceID).
			Scan(&runID, &sessionID, &workspaceID, &toolName); err != nil {
			return err
		}
		if runID != request.RunID || sessionID != request.SessionID || workspaceID != request.WorkspaceID ||
			toolName != request.ToolName {
			return errors.New("artifact source scope does not match the stored tool invocation")
		}
		return nil
	case toolrun.ShellTool:
		var sessionID, workspaceID, storedStdout, storedStderr sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id, status, stdout, stderr
			FROM tool_runs WHERE id = ?`, request.SourceID).
			Scan(&sessionID, &workspaceID, &status, &storedStdout, &storedStderr); err != nil {
			return err
		}
		stdout = storedStdout.String
		stderr = storedStderr.String
		if sessionID.String != request.SessionID || workspaceID.String != request.WorkspaceID {
			return errors.New("artifact source scope does not match the stored Shell proposal")
		}
		if status != toolrun.StatusCompleted && status != toolrun.StatusFailed {
			return errors.New("artifact Shell source must be terminal")
		}
	case "script_process":
		var sessionID, workspaceID string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id, status, stdout, stderr
			FROM script_process_proposals WHERE id = ?`, request.SourceID).
			Scan(&sessionID, &workspaceID, &status, &stdout, &stderr); err != nil {
			return err
		}
		if sessionID != request.SessionID || workspaceID != request.WorkspaceID {
			return errors.New("artifact source scope does not match the stored ScriptProcess proposal")
		}
		if status != string(scriptprocess.StatusCompleted) && status != string(scriptprocess.StatusFailed) {
			return errors.New("artifact ScriptProcess source must be terminal")
		}
	case "command_runtime":
		var runID, sessionID, workspaceID string
		if err := tx.QueryRowContext(ctx, `SELECT run_id, session_id, workspace_id,
			state, stdout, stderr FROM command_runtime_jobs WHERE id = ?`, request.SourceID).
			Scan(&runID, &sessionID, &workspaceID, &status, &stdout, &stderr); err != nil {
			return err
		}
		if runID != request.RunID || sessionID != request.SessionID ||
			workspaceID != request.WorkspaceID {
			return errors.New("artifact source scope does not match the stored command runtime Job")
		}
		if status != "completed" && status != "failed" && status != "timed_out" &&
			status != "cancelled" && status != "killed" && status != "interrupted" {
			return errors.New("artifact command runtime source must be terminal")
		}
	case "replace_file":
		var sessionID, storedReason sql.NullString
		var workspaceID string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id, status, reason
			FROM file_edits WHERE id = ?`, request.SourceID).
			Scan(&sessionID, &workspaceID, &status, &storedReason); err != nil {
			return err
		}
		if sessionID.String != request.SessionID || workspaceID != request.WorkspaceID {
			return errors.New("artifact source scope does not match the stored FileEdit proposal")
		}
		if status != "failed" {
			return errors.New("artifact FileEdit source must be failed")
		}
		stderr = storedReason.String
	default:
		return fmt.Errorf("tool %q does not support output artifact capture", request.ToolName)
	}
	for stream, content := range map[artifact.Stream]string{
		artifact.StreamStdout: stdout,
		artifact.StreamStderr: stderr,
	} {
		output, found := requested[stream]
		if content == "" {
			if found {
				return fmt.Errorf("artifact %s was requested for an empty source stream", stream)
			}
			continue
		}
		if !found || output.Content != content {
			return fmt.Errorf("artifact %s content does not match the stored source stream", stream)
		}
	}
	return nil
}

func getRunArtifactBySourceTx(ctx context.Context, tx *sql.Tx, runID string, sourceID string, stream artifact.Stream) (artifact.Blob, bool, error) {
	blob, err := getRunArtifactRow(tx.QueryRowContext(ctx, runArtifactSelect+
		` WHERE run_id = ? AND source_id = ? AND stream = ?`, runID, sourceID, stream))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Blob{}, false, nil
	}
	return blob, err == nil, err
}

const runArtifactDescriptorSelect = `SELECT id, run_id, session_id, workspace_id, source_id, tool_name,
	stream, kind, mime, encoding, sha256, size_bytes, redacted, created_at FROM run_artifacts`

const runArtifactSelect = `SELECT id, run_id, session_id, workspace_id, source_id, tool_name,
	stream, kind, mime, encoding, sha256, size_bytes, redacted, created_at, content FROM run_artifacts`

func scanRunArtifactDescriptor(row scanner) (artifact.Descriptor, error) {
	var descriptor artifact.Descriptor
	var redacted int
	var createdAt string
	if err := row.Scan(&descriptor.ID, &descriptor.RunID, &descriptor.SessionID, &descriptor.WorkspaceID,
		&descriptor.SourceID, &descriptor.ToolName, &descriptor.Stream, &descriptor.Kind, &descriptor.MIME,
		&descriptor.Encoding, &descriptor.SHA256, &descriptor.SizeBytes, &redacted, &createdAt); err != nil {
		return artifact.Descriptor{}, err
	}
	descriptor.Redacted = redacted != 0
	descriptor.CreatedAt = parseTS(createdAt)
	if err := descriptor.Validate(); err != nil {
		return artifact.Descriptor{}, err
	}
	return descriptor, nil
}

func getRunArtifactRow(row scanner) (artifact.Blob, error) {
	var blob artifact.Blob
	var redacted int
	var createdAt string
	if err := row.Scan(&blob.ID, &blob.RunID, &blob.SessionID, &blob.WorkspaceID, &blob.SourceID,
		&blob.ToolName, &blob.Stream, &blob.Kind, &blob.MIME, &blob.Encoding, &blob.SHA256,
		&blob.SizeBytes, &redacted, &createdAt, &blob.Content); err != nil {
		return artifact.Blob{}, err
	}
	blob.Redacted = redacted != 0
	blob.CreatedAt = parseTS(createdAt)
	if err := blob.Validate(); err != nil {
		return artifact.Blob{}, err
	}
	return blob, nil
}
