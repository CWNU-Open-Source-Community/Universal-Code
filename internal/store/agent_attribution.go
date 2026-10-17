package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func storeTableExists(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) (bool, error) {
	var count int
	err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = ?`, name).Scan(&count)
	return count == 1, err
}

func requireRecordedAgentAttributionTx(ctx context.Context, tx *sql.Tx, runID string,
	attribution domain.AgentAttribution,
) error {
	if attribution.Source != domain.AgentAttributionRecorded ||
		attribution.Validate() != nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"recorded Agent attribution is invalid")
	}
	var role, activeAttempt string
	err := tx.QueryRowContext(ctx, `SELECT role, active_attempt_id FROM agent_nodes
		WHERE run_id = ? AND id = ?`, runID, attribution.AgentID).
		Scan(&role, &activeAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"recorded Agent does not belong to the Run")
	}
	if err != nil {
		return err
	}
	switch domain.AgentRole(role) {
	case domain.AgentRoleRoot:
		if activeAttempt != attribution.AgentAttemptID {
			return apperror.New(apperror.CodeConflict,
				"root Agent attempt is no longer active")
		}
	case domain.AgentRoleSpecialist:
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_attempts
			WHERE run_id = ? AND agent_id = ? AND id = ?`, runID,
			attribution.AgentID, attribution.AgentAttemptID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return apperror.New(apperror.CodeFailedPrecondition,
				"specialist Agent attempt was not found")
		}
	default:
		return apperror.New(apperror.CodeFailedPrecondition,
			"recorded Agent role is invalid")
	}
	return nil
}

func requireCommandRuntimeAttributionTx(ctx context.Context, tx *sql.Tx, runID,
	rootAgentID string, attribution domain.AgentAttribution,
) error {
	switch attribution.Source {
	case domain.AgentAttributionRecorded:
		return requireRecordedAgentAttributionTx(ctx, tx, runID, attribution)
	case domain.AgentAttributionOperatorRoot:
		if attribution.Validate() != nil || attribution.AgentID != rootAgentID {
			return apperror.New(apperror.CodeInvalidArgument,
				"operator Command Runtime root attribution is invalid")
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes
			WHERE run_id = ? AND id = ? AND parent_id IS NULL AND role = 'root'`,
			runID, rootAgentID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return apperror.New(apperror.CodeFailedPrecondition,
				"operator Command Runtime root does not belong to the Run")
		}
		return nil
	default:
		return apperror.New(apperror.CodeInvalidArgument,
			"Command Runtime attribution source is invalid")
	}
}

func inferredSupervisorRootAttributionTx(ctx context.Context, tx *sql.Tx,
	runID, attemptID string,
) (domain.AgentAttribution, error) {
	root, found, err := getRootAgentTx(ctx, tx, runID)
	if err != nil {
		return domain.AgentAttribution{}, err
	}
	if !found || root.ActiveAttemptID != attemptID {
		return domain.AgentAttribution{Source: domain.AgentAttributionLegacyUnknown}, nil
	}
	return domain.AgentAttribution{AgentID: root.ID, AgentAttemptID: attemptID,
		Source: domain.AgentAttributionSupervisorRoot}, nil
}

func insertSupervisorToolAgentAttributionTx(ctx context.Context, tx *sql.Tx,
	call domain.SupervisorToolCall, attribution domain.AgentAttribution,
) error {
	available, err := storeTableExists(ctx, tx,
		"run_supervisor_tool_call_agents")
	if err != nil || !available {
		return err
	}
	if err := attribution.Validate(); err != nil {
		return err
	}
	var agentID, attemptID any
	if attribution.AgentID != "" {
		agentID = attribution.AgentID
	}
	if attribution.AgentAttemptID != "" {
		attemptID = attribution.AgentAttemptID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_supervisor_tool_call_agents
		(run_id, turn, attempt_id, call_id, agent_id, agent_attempt_id,
		 attribution_source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		call.RunID, call.Turn, call.AttemptID, call.CallID, agentID, attemptID,
		attribution.Source, ts(call.CreatedAt))
	if err != nil {
		return apperror.Wrap(apperror.CodeConflict,
			"Supervisor tool Agent attribution was rejected", err)
	}
	return nil
}

func requireSupervisorToolReplayAttributionTx(ctx context.Context, tx *sql.Tx,
	checkpoint domain.SupervisorCheckpoint, calls []llm.ToolCall,
	attribution domain.AgentAttribution,
) error {
	available, err := storeTableExists(ctx, tx,
		"run_supervisor_tool_call_agents")
	if err != nil || !available {
		return err
	}
	for _, call := range calls {
		var agentID, attemptID sql.NullString
		var source string
		err := tx.QueryRowContext(ctx, `SELECT agent_id, agent_attempt_id,
			attribution_source FROM run_supervisor_tool_call_agents
			WHERE run_id = ? AND turn = ? AND attempt_id = ? AND call_id = ?`,
			checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID,
			call.ID).Scan(&agentID, &attemptID, &source)
		if errors.Is(err, sql.ErrNoRows) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Supervisor tool replay Agent attribution is missing")
		}
		if err != nil {
			return err
		}
		stored := domain.AgentAttribution{AgentID: agentID.String,
			AgentAttemptID: attemptID.String,
			Source:         domain.AgentAttributionSource(source)}
		if stored.Validate() != nil || stored != attribution {
			return apperror.New(apperror.CodeConflict,
				"Supervisor tool replay Agent attribution changed")
		}
	}
	return nil
}

func (s *SQLiteStore) loadSupervisorToolAgentAttribution(ctx context.Context,
	call *domain.SupervisorToolCall,
) error {
	if call == nil {
		return errors.New("Supervisor tool call is required")
	}
	available, err := storeTableExists(ctx, s.db,
		"run_supervisor_tool_call_agents")
	if err != nil {
		return err
	}
	if !available {
		call.AgentAttribution = domain.AgentAttributionLegacyUnknown
		return nil
	}
	var agentID, agentAttemptID sql.NullString
	var source string
	err = s.db.QueryRowContext(ctx, `SELECT agent_id, agent_attempt_id,
		attribution_source FROM run_supervisor_tool_call_agents
		WHERE run_id = ? AND turn = ? AND attempt_id = ? AND call_id = ?`,
		call.RunID, call.Turn, call.AttemptID, call.CallID).
		Scan(&agentID, &agentAttemptID, &source)
	if errors.Is(err, sql.ErrNoRows) {
		call.AgentAttribution = domain.AgentAttributionLegacyUnknown
		return nil
	}
	if err != nil {
		return err
	}
	call.AgentID = agentID.String
	call.AgentAttemptID = agentAttemptID.String
	call.AgentAttribution = domain.AgentAttributionSource(source)
	if err := (domain.AgentAttribution{AgentID: call.AgentID,
		AgentAttemptID: call.AgentAttemptID, Source: call.AgentAttribution}).Validate(); err != nil {
		return fmt.Errorf("invalid durable Supervisor tool Agent attribution: %w", err)
	}
	return nil
}

func (s *SQLiteStore) enrichSupervisorToolRoundAttribution(ctx context.Context,
	rounds []domain.SupervisorToolRound,
) error {
	for roundIndex := range rounds {
		for callIndex := range rounds[roundIndex].Calls {
			if err := s.loadSupervisorToolAgentAttribution(ctx,
				&rounds[roundIndex].Calls[callIndex]); err != nil {
				return err
			}
		}
	}
	return enrichProjectHookRounds(ctx, s.db, rounds)
}

func insertCommandRuntimeAgentAttributionTx(ctx context.Context, tx *sql.Tx,
	jobID, runID string, createdAt any, attribution domain.AgentAttribution,
) error {
	available, err := storeTableExists(ctx, tx, "command_runtime_job_agents")
	if err != nil || !available {
		return err
	}
	if err := attribution.Validate(); err != nil {
		return err
	}
	var agentID, attemptID any
	if attribution.AgentID != "" {
		agentID = attribution.AgentID
	}
	if attribution.AgentAttemptID != "" {
		attemptID = attribution.AgentAttemptID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO command_runtime_job_agents
		(job_id, run_id, agent_id, agent_attempt_id, attribution_source, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, jobID, runID, agentID, attemptID,
		attribution.Source, createdAt)
	if err != nil {
		return apperror.Wrap(apperror.CodeConflict,
			"Command Runtime Agent attribution was rejected", err)
	}
	return nil
}

func loadCommandRuntimeAgentAttributionTx(ctx context.Context, tx *sql.Tx,
	jobID string,
) (domain.AgentAttribution, bool, error) {
	available, err := storeTableExists(ctx, tx, "command_runtime_job_agents")
	if err != nil || !available {
		return domain.AgentAttribution{}, false, err
	}
	var agentID, attemptID sql.NullString
	var source string
	err = tx.QueryRowContext(ctx, `SELECT agent_id, agent_attempt_id,
		attribution_source FROM command_runtime_job_agents WHERE job_id = ?`,
		jobID).Scan(&agentID, &attemptID, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentAttribution{}, false, nil
	}
	if err != nil {
		return domain.AgentAttribution{}, false, err
	}
	value := domain.AgentAttribution{AgentID: agentID.String,
		AgentAttemptID: attemptID.String,
		Source:         domain.AgentAttributionSource(source)}
	if err := value.Validate(); err != nil {
		return domain.AgentAttribution{}, false, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"invalid Command Runtime Agent attribution", err)
	}
	return value, true, nil
}

func (s *SQLiteStore) GetThreadCommandRuntimeJobAgentAttribution(ctx context.Context,
	threadID, jobID string,
) (domain.AgentAttribution, error) {
	var agentID, attemptID sql.NullString
	var source string
	err := s.db.QueryRowContext(ctx, `SELECT actor.agent_id,
		actor.agent_attempt_id, actor.attribution_source
		FROM command_runtime_job_agents actor
		JOIN thread_runs binding ON binding.run_id = actor.run_id
		WHERE binding.thread_id = ? AND actor.job_id = ?
		ORDER BY binding.ordinal DESC LIMIT 1`, threadID, jobID).
		Scan(&agentID, &attemptID, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentAttribution{Source: domain.AgentAttributionLegacyUnknown}, nil
	}
	if err != nil {
		return domain.AgentAttribution{}, err
	}
	value := domain.AgentAttribution{AgentID: agentID.String,
		AgentAttemptID: attemptID.String,
		Source:         domain.AgentAttributionSource(source)}
	if err := value.Validate(); err != nil {
		return domain.AgentAttribution{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"invalid Command Runtime Agent attribution", err)
	}
	return value, nil
}
