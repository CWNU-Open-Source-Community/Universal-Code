package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolcontract"
)

const mcpReferenceArtifactSource = "mcp_reference"

func reserveThreadMCPReferencesTx(ctx context.Context, tx *sql.Tx, digest string, refs []domain.MCPReference, requestedBy string) error {
	for index, ref := range refs {
		raw, err := json.Marshal(ref)
		if err != nil {
			return err
		}
		operationID := "thread-mcp-" + digest + "-" + strconv.Itoa(index+1)
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_mcp_references
		 (intent_digest,position,operation_id,reference_json,requested_by) VALUES(?,?,?,?,?)`,
			digest, index+1, operationID, string(raw), requestedBy); err != nil {
			return err
		}
	}
	return nil
}

func requireThreadMCPCheckpointTx(ctx context.Context, tx *sql.Tx, cp domain.SupervisorCheckpoint) (string, error) {
	current, found, err := getSupervisorCheckpointTx(ctx, tx, cp.RunID)
	if err != nil {
		return "", err
	}
	if !found || current.AttemptID != cp.AttemptID || current.NextTurn != cp.NextTurn || current.Phase != domain.SupervisorTurnStarted {
		return "", apperror.New(apperror.CodeConflict, "MCP reference input attempt changed")
	}
	if err := requireSupervisorCheckpointLeaseTx(ctx, tx, cp, current); err != nil {
		return "", err
	}
	var messageID string
	err = tx.QueryRowContext(ctx, `SELECT delivery.message_id FROM operator_steering_deliveries delivery
	 JOIN operator_steering_messages message ON message.id=delivery.message_id
	 JOIN runs run ON run.id=delivery.run_id
	 WHERE delivery.run_id=? AND delivery.attempt_id=? AND delivery.status='prepared'
	 AND message.status='pending' AND run.status='running'`, cp.RunID, cp.AttemptID).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return messageID, err
}

const threadMCPReferenceSelect = `SELECT ref.operation_id,ref.intent_digest,ref.position,
 intent.message_id,ref.requested_by,ref.reference_json,ref.state,ref.receipt,ref.result_json,
 COALESCE(ref.evidence_message_id,0) FROM thread_mcp_references ref
 JOIN thread_message_intents intent ON intent.operation_key_digest=ref.intent_digest`

func scanThreadMCPReference(row scanner) (domain.ThreadMCPReference, error) {
	var result domain.ThreadMCPReference
	var raw string
	err := row.Scan(&result.OperationID, &result.IntentDigest, &result.Position, &result.MessageID,
		&result.RequestedBy, &raw, &result.State, &result.Receipt, &result.ResultJSON, &result.EvidenceMessageID)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &result.Reference)
	}
	if err == nil {
		err = domain.ValidateThreadMCPReferences([]domain.MCPReference{result.Reference})
	}
	return result, err
}

func (s *SQLiteStore) ListSupervisorMCPReferences(ctx context.Context, cp domain.SupervisorCheckpoint) ([]domain.ThreadMCPReference, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	messageID, err := requireThreadMCPCheckpointTx(ctx, tx, cp)
	if err != nil || messageID == "" {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, threadMCPReferenceSelect+` WHERE intent.message_id=? AND intent.run_id=? ORDER BY ref.position`, messageID, cp.RunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ThreadMCPReference
	for rows.Next() {
		value, err := scanThreadMCPReference(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// Claim is serialized with cancellation, lease handoff and other hosts. Only
// the caller receiving fresh=true may contact a server for this selection.
func (s *SQLiteStore) ClaimThreadMCPReference(ctx context.Context, cp domain.SupervisorCheckpoint, operationID string) (domain.ThreadMCPReference, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadMCPReference{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE threads SET updated_at=updated_at WHERE id=(
	 SELECT intent.thread_id FROM thread_message_intents intent JOIN thread_mcp_references ref
	 ON ref.intent_digest=intent.operation_key_digest WHERE ref.operation_id=?)`, operationID); err != nil {
		return domain.ThreadMCPReference{}, false, err
	}
	messageID, err := requireThreadMCPCheckpointTx(ctx, tx, cp)
	if err != nil {
		return domain.ThreadMCPReference{}, false, err
	}
	value, err := scanThreadMCPReference(tx.QueryRowContext(ctx, threadMCPReferenceSelect+` WHERE ref.operation_id=? AND intent.message_id=? AND intent.run_id=?`, operationID, messageID, cp.RunID))
	if err != nil {
		return value, false, err
	}
	fresh := value.State == "pending"
	if fresh {
		_, err = tx.ExecContext(ctx, `UPDATE thread_mcp_references SET state='started' WHERE operation_id=? AND state='pending'`, operationID)
		value.State = "started"
	}
	if err != nil {
		return value, false, err
	}
	return value, fresh, tx.Commit()
}

func (s *SQLiteStore) RecheckThreadMCPReference(ctx context.Context, cp domain.SupervisorCheckpoint, operationID string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	messageID, err := requireThreadMCPCheckpointTx(ctx, tx, cp)
	if err != nil {
		return err
	}
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thread_mcp_references ref
	 JOIN thread_message_intents intent ON intent.operation_key_digest=ref.intent_digest
	 WHERE ref.operation_id=? AND ref.state='started' AND intent.message_id=? AND intent.run_id=? AND intent.rejected=0)`, operationID, messageID, cp.RunID).Scan(&allowed)
	if err == nil && !allowed {
		return apperror.New(apperror.CodeConflict, "MCP selection no longer belongs to this active input")
	}
	return err
}

// Result and non-authorizing session evidence commit together. Completion may
// be recorded after cancellation; recording an observed result grants no work.
func (s *SQLiteStore) CompleteThreadMCPReference(ctx context.Context, operationID, receipt, resultJSON, evidence, fullContent string) (domain.ThreadMCPReference, error) {
	if (toolcontract.Receipt{OperationID: operationID, State: toolcontract.ReceiptState(receipt)}).Validate() != nil || !json.Valid([]byte(resultJSON)) || len(resultJSON) > domain.MaxSupervisorToolResultBytes {
		return domain.ThreadMCPReference{}, apperror.New(apperror.CodeInvalidArgument, "MCP reference receipt is invalid")
	}
	if fullContent != "" && receipt != string(toolcontract.ReceiptResultReceived) {
		return domain.ThreadMCPReference{}, apperror.New(apperror.CodeInvalidArgument, "Only received MCP content can be retained")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadMCPReference{}, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := scanThreadMCPReference(tx.QueryRowContext(ctx, threadMCPReferenceSelect+` WHERE ref.operation_id=?`, operationID))
	if err != nil {
		return value, err
	}
	if value.State == "completed" {
		return value, nil
	}
	if value.State != "started" {
		return value, apperror.New(apperror.CodeConflict, "MCP reference has no claimed execution")
	}
	var runID, sessionID, workspaceID string
	if err = tx.QueryRowContext(ctx, `SELECT run.id,run.session_id,COALESCE(mission.workspace_id,'') FROM thread_message_intents intent
	 JOIN runs run ON run.id=intent.run_id JOIN missions mission ON mission.id=run.mission_id WHERE intent.operation_key_digest=?`, value.IntentDigest).Scan(&runID, &sessionID, &workspaceID); err != nil {
		return value, err
	}
	if fullContent != "" {
		full, err := redact.SanitizeSensitiveJSON([]byte(fullContent))
		if err != nil {
			return value, err
		}
		request, err := artifact.NormalizeCaptureRequest(artifact.CaptureRequest{RunID: runID, SessionID: sessionID, WorkspaceID: workspaceID,
			SourceID: operationID, ToolName: mcpReferenceArtifactSource,
			Outputs: []artifact.Output{{Stream: artifact.StreamStdout, MIME: "application/json", Content: string(full), Redacted: true}}})
		if err != nil {
			return value, err
		}
		descriptors, err := captureToolOutputTx(ctx, tx, request)
		if err != nil {
			return value, err
		}
		resultJSON, err = withMCPReferenceArtifact(resultJSON, descriptors[0])
		if err == nil {
			evidence, err = withMCPReferenceArtifact(evidence, descriptors[0])
		}
		if err != nil {
			return value, err
		}
	}
	message, err := saveSessionMessageTx(ctx, tx, session.NewEvidenceMessage(sessionID, session.SourceToolResult, operationID, evidence))
	if err != nil {
		return value, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE thread_mcp_references SET state='completed',receipt=?,result_json=?,evidence_message_id=? WHERE operation_id=? AND state='started'`, receipt, resultJSON, message.ID, operationID)
	if err != nil {
		return value, err
	}
	value.State, value.Receipt, value.ResultJSON, value.EvidenceMessageID = "completed", receipt, resultJSON, message.ID
	return value, tx.Commit()
}

func withMCPReferenceArtifact(content string, descriptor artifact.Descriptor) (string, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &result); err != nil || result == nil {
		return "", errors.New("MCP result envelope is invalid")
	}
	read, err := json.Marshal(domain.HistoryReadRequest{SourceID: "artifact:" + descriptor.ID, Part: "content",
		ExpectedSHA256: descriptor.SHA256, Limit: domain.HistoryReadDefaultBytes})
	if err != nil {
		return "", err
	}
	result["full_result_read"] = read
	result["full_result_notice"], _ = json.Marshal("Complete sanitized MCP JSON was saved; use history_read with full_result_read and follow next_offset to read further pages.")
	raw, err := json.Marshal(result)
	return string(raw), err
}
