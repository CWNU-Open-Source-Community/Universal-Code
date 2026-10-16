package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func loadSpecialistTaskBriefTx(ctx context.Context, tx *sql.Tx, attempt domain.AgentAttempt) (domain.SpecialistTaskBrief, bool, error) {
	var raw, fingerprint string
	var turn int64
	err := tx.QueryRowContext(ctx, `SELECT brief_json,fingerprint,turn_number FROM specialist_task_briefs
  WHERE agent_attempt_id=? AND run_id=? AND agent_id=? AND parent_agent_id=?`,
		attempt.ID, attempt.RunID, attempt.AgentID, attempt.ParentAgentID).Scan(&raw, &fingerprint, &turn)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistTaskBrief{}, false, nil
	}
	if err != nil {
		return domain.SpecialistTaskBrief{}, false, err
	}
	var brief domain.SpecialistTaskBrief
	if err := json.Unmarshal([]byte(raw), &brief); err != nil {
		return brief, false, err
	}
	if turn != attempt.Turn || brief.Fingerprint != fingerprint || brief.RunID != attempt.RunID ||
		brief.AgentID != attempt.AgentID || brief.ParentAgentID != attempt.ParentAgentID {
		return brief, false, apperror.New(apperror.CodeFailedPrecondition, "Specialist brief binding differs from its attempt")
	}
	if err := brief.Validate(); err != nil {
		return brief, false, apperror.Wrap(apperror.CodeFailedPrecondition, "Specialist brief integrity failed", err)
	}
	// Messages are immutable and retained; verify their original binding on reads.
	messages := make([]domain.AgentMessage, 0, len(brief.Sources))
	for _, source := range brief.Sources {
		if source.Kind != "parent_instruction" {
			continue
		}
		message, err := scanAgentMessage(tx.QueryRowContext(ctx, agentMessageSelect+` WHERE id=?`, source.ID))
		if err != nil {
			return brief, false, err
		}
		p, err := domain.DecodeAgentInstructionPayload(message.PayloadJSON)
		if err != nil || message.RunID != brief.RunID || message.RecipientAgentID != brief.AgentID ||
			message.SenderAgentID != brief.ParentAgentID || message.Sequence != source.Sequence || p.Version != source.Version ||
			domain.SpecialistInstructionPayloadSHA256(message.PayloadJSON) != source.SHA256 {
			return brief, false, apperror.New(apperror.CodeFailedPrecondition, "Specialist brief source binding changed")
		}
		messages = append(messages, message)
	}
	rebuilt, err := domain.BuildSpecialistTaskBrief(brief.RunID, brief.AgentID, brief.ParentAgentID, messages, brief.WorkItems)
	if err != nil || rebuilt.Fingerprint != brief.Fingerprint {
		return brief, false, apperror.New(apperror.CodeFailedPrecondition, "Specialist brief effective constraints differ from their retained sources")
	}
	return brief, true, nil
}

func prepareSpecialistTaskBriefTx(ctx context.Context, tx *sql.Tx, attempt domain.AgentAttempt,
	selected []domain.AgentMessage, now time.Time) (domain.SpecialistTaskBrief, error) {
	query := agentMessageSelect + ` WHERE run_id=? AND recipient_agent_id=? AND sender_agent_id=?
  AND kind='instruction' AND semantic='message' AND (status='consumed'`
	args := []any{attempt.RunID, attempt.AgentID, attempt.ParentAgentID}
	for _, message := range selected {
		query += ` OR id=?`
		args = append(args, message.ID)
	}
	query += `) ORDER BY sequence LIMIT ?`
	args = append(args, domain.MaxSpecialistBriefSources+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.SpecialistTaskBrief{}, err
	}
	messages, err := scanAgentMessages(rows)
	_ = rows.Close()
	if err != nil {
		return domain.SpecialistTaskBrief{}, err
	}
	workRows, err := tx.QueryContext(ctx, workItemSelect+` WHERE run_id=? AND owner_agent_id=?
  AND status IN ('pending','in_progress','blocked')
  ORDER BY CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,
   CASE status WHEN 'in_progress' THEN 0 WHEN 'blocked' THEN 1 ELSE 2 END,updated_at DESC,id LIMIT ?`,
		attempt.RunID, attempt.AgentID, domain.MaxSpecialistBriefWorkItems+1)
	if err != nil {
		return domain.SpecialistTaskBrief{}, err
	}
	work := []domain.WorkItem{}
	for workRows.Next() {
		item, err := scanWorkItem(workRows)
		if err != nil {
			_ = workRows.Close()
			return domain.SpecialistTaskBrief{}, err
		}
		work = append(work, item)
	}
	err = workRows.Err()
	_ = workRows.Close()
	if err != nil {
		return domain.SpecialistTaskBrief{}, err
	}
	for i := range work {
		work[i], err = getWorkItemTx(ctx, tx, work[i].ID)
		if err != nil {
			return domain.SpecialistTaskBrief{}, err
		}
	}
	brief, err := domain.BuildSpecialistTaskBrief(attempt.RunID, attempt.AgentID, attempt.ParentAgentID, messages, work)
	if err != nil {
		return brief, apperror.Wrap(apperror.CodeResourceExhausted, "Specialist task brief cannot be prepared completely", err)
	}
	raw, err := json.Marshal(brief)
	if err != nil {
		return brief, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO specialist_task_briefs
  (agent_attempt_id,run_id,agent_id,parent_agent_id,turn_number,brief_json,fingerprint,prepared_at)
  VALUES (?,?,?,?,?,?,?,?)`, attempt.ID, attempt.RunID, attempt.AgentID, attempt.ParentAgentID,
		attempt.Turn, string(raw), brief.Fingerprint, ts(now))
	return brief, err
}

func validateSpecialistInstructionSendTx(ctx context.Context, tx *sql.Tx, message domain.AgentMessage,
	recipient domain.AgentNode) error {
	var header struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(message.PayloadJSON), &header)
	if header.Version != domain.SpecialistInstructionOperationVersion {
		return nil
	}
	if !domain.EligibleSpecialistContextMessage(message) || recipient.Role != domain.AgentRoleSpecialist ||
		recipient.ParentID != message.SenderAgentID {
		return apperror.New(apperror.CodeFailedPrecondition, "Specialist operation must come from its direct root parent")
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id=?`, message.SenderAgentID))
	if err != nil {
		return err
	}
	if parent.RunID != message.RunID || parent.Role != domain.AgentRoleRoot || parent.Terminal() {
		return apperror.New(apperror.CodeFailedPrecondition, "Specialist operation parent is not active")
	}
	if _, err := domain.DecodeAgentInstructionPayload(message.PayloadJSON); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "invalid Specialist operation", err)
	}
	rows, err := tx.QueryContext(ctx, agentMessageSelect+` WHERE run_id=? AND recipient_agent_id=? AND sender_agent_id=?
  AND kind='instruction' AND semantic='message' ORDER BY sequence LIMIT ?`, message.RunID, message.RecipientAgentID,
		message.SenderAgentID, domain.MaxSpecialistBriefSources+1)
	if err != nil {
		return err
	}
	prior, err := scanAgentMessages(rows)
	_ = rows.Close()
	if err != nil {
		return err
	}
	prior = append(prior, message)
	if _, err := domain.BuildSpecialistTaskBrief(message.RunID, message.RecipientAgentID, message.SenderAgentID, prior, nil); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition, "Specialist operation conflicts with current source state", err)
	}
	return nil
}

func requireSpecialistBriefModelStartTx(ctx context.Context, tx *sql.Tx, attempt domain.AgentAttempt,
	model llm.ModelAttempt) error {
	// Legacy internal ledger callers remain compatible. Every actual Runner call
	// carries the source-bound identity introduced in ADR 0161.
	if model.SpecialistAttemptID == "" {
		return nil
	}
	brief, found, err := loadSpecialistTaskBriefTx(ctx, tx, attempt)
	if err != nil {
		return err
	}
	if !found {
		return apperror.New(apperror.CodeFailedPrecondition, "Specialist dispatch requires a prepared current-attempt task brief")
	}
	if model.Context == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Specialist dispatch lacks task brief provenance")
	}
	for _, source := range model.Context.Included {
		if source.Kind == "specialist_task_brief" && source.SourceID == brief.Fingerprint {
			return nil
		}
	}
	return apperror.New(apperror.CodeFailedPrecondition, "Specialist dispatch task brief fingerprint differs from its prepared snapshot")
}

func validateSpecialistBriefProjectionTx(ctx context.Context, tx *sql.Tx, nodes []domain.AgentNode) error {
	for _, node := range nodes {
		if node.Role != domain.AgentRoleSpecialist || node.Status != domain.AgentRunning {
			continue
		}
		attempt, err := scanAgentAttempt(tx.QueryRowContext(ctx, agentAttemptSelect+` WHERE id=?`, node.ActiveAttemptID))
		if err != nil {
			return err
		}
		_, _, err = loadSpecialistTaskBriefTx(ctx, tx, attempt)
		if err != nil {
			return err
		}
	}
	return nil
}

func validateSpecialistBriefCompletedInputTx(ctx context.Context, tx *sql.Tx, attempt domain.AgentAttempt, input string) error {
	brief, found, err := loadSpecialistTaskBriefTx(ctx, tx, attempt)
	if err != nil {
		return err
	}
	if !found {
		return apperror.New(apperror.CodeFailedPrecondition, "Specialist completion lacks its prepared task brief")
	}
	var delivered struct {
		Version      string `json:"version"`
		Fingerprint  string `json:"task_brief_fingerprint"`
		Instructions []struct {
			Instruction string `json:"instruction"`
		} `json:"parent_instructions"`
		Work []domain.SpecialistTaskWorkContext `json:"work_items"`
	}
	if err := json.Unmarshal([]byte(input), &delivered); err != nil || delivered.Version != domain.SpecialistContextVersion ||
		delivered.Fingerprint != brief.Fingerprint || len(delivered.Instructions) != len(brief.Instructions) || len(delivered.Work) != len(brief.WorkItems) {
		return apperror.New(apperror.CodeFailedPrecondition, "Specialist completed input differs from its prepared task brief")
	}
	for i, instruction := range brief.Instructions {
		if delivered.Instructions[i].Instruction != domain.SpecialistTaskInstructionProjection(instruction.Instruction) {
			return apperror.New(apperror.CodeFailedPrecondition, "Specialist completed input changed a required instruction")
		}
	}
	for i, item := range brief.WorkItems {
		expected, _ := json.Marshal(domain.SpecialistTaskWorkProjection(item))
		actual, _ := json.Marshal(delivered.Work[i])
		if string(expected) != string(actual) {
			return apperror.New(apperror.CodeFailedPrecondition, "Specialist completed input omitted or changed required owned work")
		}
	}
	return nil
}
