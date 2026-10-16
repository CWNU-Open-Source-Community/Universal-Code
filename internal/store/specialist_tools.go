package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func (s *SQLiteStore) BindSpecialistTools(ctx context.Context, b domain.SpecialistToolBinding) error {
	if err := b.Ref.Validate(); err != nil {
		return err
	}
	if b.Mode != "read" && b.Mode != "batch" {
		return errors.New("invalid Specialist tool mode")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, b.Ref)
	if err != nil {
		return err
	}
	if child.SessionID != b.SessionID || attempt.UsageRecordedAt != nil {
		return errors.New("child tool binding differs from its fresh attempt")
	}
	var workspaceID string
	if err = tx.QueryRowContext(ctx, `SELECT workspace_id FROM missions WHERE id=?`, run.MissionID).Scan(&workspaceID); err != nil {
		return err
	}
	if b.WorkspaceID != workspaceID {
		return errors.New("child tool source workspace differs from its mission")
	}
	if b.Mode == "read" {
		raw, e := json.Marshal(child.AllowedReadPaths)
		if e != nil {
			return e
		}
		if len(child.AllowedReadPaths) == 0 || child.ToolCallLimit <= 0 || b.AllowedReadPathsJSON != string(raw) {
			return apperror.New(apperror.CodePolicyDenied, "child read authority differs from admission")
		}
	} else {
		b.AllowedReadPathsJSON = "[]"
	}
	if b.Mode == "batch" {
		var n int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM batch_delivery_workspaces WHERE plan_id=? AND ordinal=? AND generation=? AND agent_id=?`, b.BatchPlanID, b.BatchOrdinal, b.BatchGeneration, child.ID).Scan(&n)
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("child tool binding lacks its admitted Batch workspace")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO specialist_tool_bindings(agent_attempt_id,run_id,agent_id,session_id,workspace_id,root_fingerprint,brief_fingerprint,mode,batch_plan_id,batch_ordinal,batch_generation,created_at,allowed_read_paths_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.Ref.AttemptID, b.Ref.RunID, b.Ref.AgentID, b.SessionID, b.WorkspaceID, b.RootFingerprint, b.BriefFingerprint, b.Mode, b.BatchPlanID, b.BatchOrdinal, b.BatchGeneration, ts(time.Now().UTC()), b.AllowedReadPathsJSON)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func specialistToolsEnabledTx(ctx context.Context, tx *sql.Tx, attemptID string) (bool, error) {
	var table int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='specialist_tool_bindings'`).Scan(&table); err != nil {
		return false, err
	}
	if table == 0 {
		return false, nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_bindings WHERE agent_attempt_id=?`, attemptID).Scan(&n)
	return n == 1, err
}

func specialistToolRoundTx(ctx context.Context, tx *sql.Tx, attemptID string) (int, error) {
	var round, pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_rounds WHERE agent_attempt_id=?`, attemptID).Scan(&round); err != nil {
		return 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_calls WHERE agent_attempt_id=? AND status='pending'`, attemptID).Scan(&pending); err != nil {
		return 0, err
	}
	if pending != 0 {
		return 0, apperror.New(apperror.CodeFailedPrecondition, "child model continuation still has pending tools")
	}
	return round, nil
}

func (s *SQLiteStore) RecordSpecialistToolModelCompleted(ctx context.Context, ref domain.AgentAttemptRef, model llm.ModelAttempt, response llm.ChatResponse, input string, decision policy.Decision) (domain.AgentAttempt, error) {
	if len(response.ToolCalls) == 0 || len(response.ToolCalls) > domain.MaxSpecialistToolCalls || model.ToolRound >= domain.MaxSpecialistToolRounds {
		return domain.AgentAttempt{}, errors.New("child native tool batch exceeds its bound")
	}
	calls, err := llm.NormalizeToolCalls(response.ToolCalls)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	for i := range calls {
		canonical, err := toolgateway.NormalizeSpecialistNativePayload(calls[i].Name, calls[i].Arguments)
		if err != nil {
			return domain.AgentAttempt{}, err
		}
		calls[i].Arguments = canonical
		calls[i].Authority = nil
	}
	response.ToolCalls = calls
	if response.Replay != nil {
		response.Replay, err = response.Replay.BindToolCalls(calls)
		if err != nil {
			return domain.AgentAttempt{}, err
		}
	}
	if response.Usage.Validate() != nil {
		return domain.AgentAttempt{}, errors.New("invalid child provider usage")
	}
	model.Outcome = llm.OutcomeSuccess
	model.ErrorText = ""
	model.RetryPlanned = false
	model.RetryAfter = 0
	if err := validateSpecialistModelIdentity(model); err != nil {
		return domain.AgentAttempt{}, err
	}
	decision, err = normalizeSpecialistPolicyDecision(decision)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if !decision.Allowed || decision.NeedsApproval {
		return domain.AgentAttempt{}, errors.New("child model tool response was denied")
	}
	raw, err := json.Marshal(llm.Message{Role: "assistant", Content: response.Text, ToolCalls: calls})
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	return s.recordSpecialistModelTerminal(ctx, ref, model, specialistModelTerminal{
		status: "completed", usage: &response.Usage, decision: &decision, input: input,
		inputFingerprint:  runmutation.Fingerprint("specialist_model_input.v1", input),
		actionFingerprint: runmutation.Fingerprint("specialist_native_tool_batch.v1", string(raw)), toolResponse: &response,
	})
}

func insertSpecialistToolRoundTx(ctx context.Context, tx *sql.Tx, ref domain.AgentAttemptRef, model llm.ModelAttempt, response llm.ChatResponse, now time.Time) error {
	enabled, err := specialistToolsEnabledTx(ctx, tx, ref.AttemptID)
	if err != nil {
		return err
	}
	if !enabled {
		return errors.New("child tool response lacks a Go-issued tool binding")
	}
	var prior int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_rounds WHERE agent_attempt_id=?`, ref.AttemptID).Scan(&prior); err != nil {
		return err
	}
	if prior != model.ToolRound {
		return errors.New("child tool round is not the next model response")
	}
	var replay []byte
	if response.Replay != nil {
		if err := response.Replay.ValidateSource(model.Provider, model.Model); err != nil {
			return err
		}
		if err := response.Replay.ValidateToolCalls(response.ToolCalls); err != nil {
			return err
		}
		replay, err = response.Replay.EncodeForStore()
		if err != nil {
			return err
		}
		if len(replay) > 8*1024*1024 {
			return errors.New("child provider replay exceeds its bound")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO specialist_tool_rounds(agent_attempt_id,round,model_attempt,assistant_text,provider_replay,created_at) VALUES(?,?,?,?,?,?)`, ref.AttemptID, prior+1, model.Number, response.Text, replay, ts(now))
	if err != nil {
		return err
	}
	for i, c := range response.ToolCalls {
		_, err = tx.ExecContext(ctx, `INSERT INTO specialist_tool_calls(agent_attempt_id,round,position,call_id,name,payload_json,status,created_at) VALUES(?,?,?,?,?,?,'pending',?)`, ref.AttemptID, prior+1, i+1, c.ID, c.Name, string(c.Arguments), ts(now))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) ListSpecialistToolRounds(ctx context.Context, ref domain.AgentAttemptRef) ([]domain.SpecialistToolRound, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.round,r.model_attempt,r.assistant_text,r.provider_replay,c.position,c.call_id,c.name,c.payload_json,c.status,c.result_json,c.error_code,c.charge_id,c.created_at,c.completed_at FROM specialist_tool_rounds r JOIN specialist_tool_calls c ON c.agent_attempt_id=r.agent_attempt_id AND c.round=r.round JOIN specialist_tool_bindings b ON b.agent_attempt_id=r.agent_attempt_id WHERE b.agent_attempt_id=? AND b.run_id=? AND b.agent_id=? ORDER BY r.round,c.position`, ref.AttemptID, ref.RunID, ref.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SpecialistToolRound{}
	for rows.Next() {
		var round domain.SpecialistToolRound
		var c domain.SpecialistToolCall
		var created string
		var completed sql.NullString
		var replay []byte
		if err := rows.Scan(&round.Round, &round.ModelAttempt, &round.AssistantText, &replay, &c.Position, &c.CallID, &c.Name, &c.PayloadJSON, &c.Status, &c.ResultJSON, &c.ErrorCode, &c.ChargeID, &created, &completed); err != nil {
			return nil, err
		}
		round.ProviderReplay = replay
		c.Ref = ref
		c.Round = round.Round
		c.ModelAttempt = round.ModelAttempt
		c.CreatedAt = parseTS(created)
		c.CompletedAt = parseNullableTS(completed)
		if len(out) == 0 || out[len(out)-1].Round != round.Round {
			out = append(out, round)
		}
		out[len(out)-1].Calls = append(out[len(out)-1].Calls, c)
	}
	return out, rows.Err()
}

// ValidateSpecialistToolDispatch proves that gateway fields refer to a pending
// provider call from this child's current leased attempt, not a root alias.
func (s *SQLiteStore) ValidateSpecialistToolDispatch(ctx context.Context, call toolgateway.ToolCall, mode string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ref := domain.AgentAttemptRef{RunID: call.RunID, AgentID: call.AgentID, AttemptID: call.AgentAttemptID}
	attempt, child, _, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return err
	}
	if child.SessionID != call.SessionID || attempt.LeaseID != call.LeaseID || attempt.LeaseGeneration != call.LeaseGeneration {
		return errors.New("child tool actor or lease changed")
	}
	var n int
	input, _, err := toolgateway.NormalizeSpecialistToolPayload(call.Payload)
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_bindings b JOIN specialist_tool_calls c ON c.agent_attempt_id=b.agent_attempt_id JOIN specialist_task_briefs brief ON brief.agent_attempt_id=b.agent_attempt_id WHERE b.agent_attempt_id=? AND b.workspace_id=? AND b.mode=? AND b.brief_fingerprint=brief.fingerprint AND (b.mode='batch' OR (b.allowed_read_paths_json=(SELECT allowed_read_paths_json FROM agent_nodes WHERE id=b.agent_id) AND json_array_length(b.allowed_read_paths_json)>0)) AND c.call_id=? AND c.status='pending' AND c.payload_json=? AND c.name=? AND c.charge_id=?`, attempt.ID, call.WorkspaceID, mode, call.SpecialistToolCallID, string(input.Arguments), input.Tool, call.InvocationID).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return apperror.New(apperror.CodePolicyDenied, "child tool dispatch differs from its charged durable call")
	}
	return tx.Commit()
}

func (s *SQLiteStore) CompleteSpecialistToolCall(ctx context.Context, c domain.SpecialistToolCall) error {
	if c.Status == domain.SupervisorToolPending || !json.Valid([]byte(c.ResultJSON)) || len(c.ResultJSON) > domain.MaxSupervisorToolResultBytes {
		return errors.New("invalid child tool receipt")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, _, run, err := loadActiveAgentAttemptTx(ctx, tx, c.Ref)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE specialist_tool_calls SET status=?,result_json=?,error_code=?,completed_at=? WHERE agent_attempt_id=? AND call_id=? AND status='pending'`, c.Status, c.ResultJSON, c.ErrorCode, ts(now), c.Ref.AttemptID, c.CallID)
	if err != nil {
		return err
	}
	if err = requireSingleAgentAttemptUpdate(result, "child tool call already resolved"); err != nil {
		return err
	}
	err = appendSupervisorEventTx(ctx, tx, run, "agent.tool.completed", "specialist_runner", c.CallID, map[string]any{"agent_id": c.Ref.AgentID, "agent_attempt_id": c.Ref.AttemptID, "tool": c.Name, "status": c.Status, "error_code": c.ErrorCode, "result_sha256": runmutation.Fingerprint("specialist_tool_result.v1", c.ResultJSON)})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetSpecialistToolBinding(ctx context.Context, ref domain.AgentAttemptRef) (domain.SpecialistToolBinding, error) {
	b := domain.SpecialistToolBinding{Ref: ref}
	err := s.db.QueryRowContext(ctx, `SELECT session_id,workspace_id,root_fingerprint,brief_fingerprint,mode,batch_plan_id,batch_ordinal,batch_generation,allowed_read_paths_json FROM specialist_tool_bindings WHERE agent_attempt_id=? AND run_id=? AND agent_id=?`, ref.AttemptID, ref.RunID, ref.AgentID).Scan(&b.SessionID, &b.WorkspaceID, &b.RootFingerprint, &b.BriefFingerprint, &b.Mode, &b.BatchPlanID, &b.BatchOrdinal, &b.BatchGeneration, &b.AllowedReadPathsJSON)
	return b, err
}

func (s *SQLiteStore) CountSpecialistModelAttempts(ctx context.Context, ref domain.AgentAttemptRef) (int, error) {
	if err := ref.Validate(); err != nil {
		return 0, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM specialist_model_calls m JOIN agent_attempts a ON a.id=m.agent_attempt_id WHERE a.id=? AND a.run_id=? AND a.agent_id=?`, ref.AttemptID, ref.RunID, ref.AgentID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) ListBatchSpecialistVerificationResults(ctx context.Context, planID string, ordinal int, generation int64, agentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.result_json FROM specialist_tool_bindings b JOIN agent_attempts a ON a.id=b.agent_attempt_id
	 JOIN batch_delivery_workspaces w ON w.plan_id=b.batch_plan_id AND w.ordinal=b.batch_ordinal AND w.generation=b.batch_generation AND w.agent_id=b.agent_id
	 JOIN specialist_tool_calls c ON c.agent_attempt_id=a.id
	 WHERE b.mode='batch' AND b.batch_plan_id=? AND b.batch_ordinal=? AND b.batch_generation=? AND b.agent_id=?
	 AND a.status='continued' AND c.name='verify' AND c.status='completed' AND c.charge_id<>'' ORDER BY a.turn_number DESC,c.round DESC,c.position DESC LIMIT 16`, planID, ordinal, generation, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

func requireChildToolModeTx(ctx context.Context, tx *sql.Tx, attemptID string, round int) error {
	enabled, err := specialistToolsEnabledTx(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if !enabled && round != 0 {
		return errors.New("no-tool Specialist cannot start a tool continuation")
	}
	if enabled {
		actual, err := specialistToolRoundTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if actual != round {
			return fmt.Errorf("child tool round %d differs from recorded %d", round, actual)
		}
		if round > 0 {
			var final int
			err = tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_model_calls WHERE agent_attempt_id=? AND status='completed' AND action_kind IN ('continue','finish')`, attemptID).Scan(&final)
			if err != nil {
				return err
			}
			if final != 0 {
				return errors.New("child final lifecycle response was already committed")
			}
		}
	}
	return nil
}

func specialistToolModePrimaryCommitted(usageRecorded bool, tools bool, repairFound bool) bool {
	return repairFound || (usageRecorded && !tools)
}
