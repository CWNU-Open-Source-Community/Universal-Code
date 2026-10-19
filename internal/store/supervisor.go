package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

const (
	maxSupervisorErrorChars = 4096
	maxSupervisorInputChars = 64 * 1024
)

const supervisorCheckpointSelect = `SELECT run_id, lease_id, lease_generation, next_turn, phase, attempt_id, last_error,
	pending_input, repair_phase, repair_reason, input_tokens, output_tokens, total_tokens, execution_millis, updated_at,pending_image_count,pending_attachment_count
	FROM run_supervisor_checkpoints WHERE run_id = ?`

func (s *SQLiteStore) GetSupervisorCheckpoint(ctx context.Context, runID string) (domain.SupervisorCheckpoint, bool, error) {
	checkpoint, err := scanSupervisorCheckpoint(s.db.QueryRowContext(ctx, supervisorCheckpointSelect, strings.TrimSpace(runID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SupervisorCheckpoint{}, false, nil
	}
	if err != nil {
		return domain.SupervisorCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

func (s *SQLiteStore) BeginSupervisorTurn(ctx context.Context, lease domain.RunExecutionLease,
	pendingInput string,
) (domain.SupervisorTurn, error) {
	return s.beginSupervisorTurn(ctx, lease, pendingInput, false, "", "")
}

func (s *SQLiteStore) BeginSupervisorSteeringTurn(ctx context.Context,
	lease domain.RunExecutionLease,
) (domain.SupervisorTurn, error) {
	return s.beginSupervisorTurn(ctx, lease, "", true, "", "")
}

func (s *SQLiteStore) BeginSupervisorSteeringTurnForMessage(ctx context.Context,
	lease domain.RunExecutionLease, messageID string,
) (domain.SupervisorTurn, error) {
	messageID = strings.TrimSpace(messageID)
	if !domain.ValidAgentID(messageID) || strings.ContainsRune(messageID, 0) {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeInvalidArgument,
			"operator steering message id is invalid")
	}
	return s.beginSupervisorTurn(ctx, lease, "", true, messageID, "")
}

func (s *SQLiteStore) beginSupervisorTurn(ctx context.Context, lease domain.RunExecutionLease,
	pendingInput string, requireSteering bool, requiredSteeringID string, approvalHandoffID string,
) (domain.SupervisorTurn, error) {
	if err := ctx.Err(); err != nil {
		return domain.SupervisorTurn{}, apperror.Normalize(err)
	}
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive {
		return domain.SupervisorTurn{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"active run execution lease is required", err)
	}
	pendingInput, err := normalizeSupervisorInput(pendingInput)
	if err != nil {
		return domain.SupervisorTurn{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SupervisorTurn{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireRunExecutionLeaseTx(ctx, tx, lease.RunID, lease.LeaseID, lease.Generation); err != nil {
		return domain.SupervisorTurn{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, lease.RunID))
	if err != nil {
		return domain.SupervisorTurn{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is %s; supervisor requires running", run.ID, run.Status))
	}
	if err := lockRunningRunForSteeringTx(ctx, tx, run.ID); err != nil {
		return domain.SupervisorTurn{}, err
	}
	var applyingDelegations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_applications WHERE run_id = ? AND status = 'applying'`,
		run.ID).Scan(&applyingDelegations); err != nil {
		return domain.SupervisorTurn{}, err
	}
	if applyingDelegations != 0 {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
			"root Supervisor is reserved by an active delegation application")
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile, workspace_id, scope_json,
		created_at, updated_at FROM missions WHERE id = ?`, run.MissionID))
	if err != nil {
		return domain.SupervisorTurn{}, err
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.SupervisorTurn{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"run mode snapshot is required before supervisor execution", err)
	}
	executionRun := run
	if root, found, err := getRootAgentTx(ctx, tx, run.ID); err != nil {
		return domain.SupervisorTurn{}, err
	} else if found {
		effectiveBudget, err := effectiveRootBudgetTx(ctx, tx, run, root.ID)
		if err != nil {
			return domain.SupervisorTurn{}, err
		}
		executionRun.Budget = effectiveBudget
	}
	checkpoint, ok, err := getSupervisorCheckpointTx(ctx, tx, run.ID)
	if err != nil {
		return domain.SupervisorTurn{}, err
	}
	if !ok {
		checkpoint = domain.SupervisorCheckpoint{
			RunID: run.ID, LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation,
			NextTurn: 1, Phase: domain.SupervisorIdle, UpdatedAt: time.Now().UTC(),
		}
	}
	approvalImageCount := 0
	approvalAttachmentCount := 0
	if approvalHandoffID != "" {
		continuation, err := requireApprovalContinuationStartTx(ctx, tx, run, checkpoint, approvalHandoffID)
		if err != nil {
			return domain.SupervisorTurn{}, err
		}
		pendingInput = continuation.Input
		approvalImageCount = continuation.ImageCount
		approvalAttachmentCount = continuation.AttachmentCount
	}
	if checkpoint.Phase == domain.SupervisorRunCompleted || checkpoint.Phase == domain.SupervisorRunFailed {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s supervisor is finalized as %s", run.ID, checkpoint.Phase))
	}
	if checkpoint.Phase == domain.SupervisorTurnStarted {
		var prepared int
		query := `SELECT COUNT(*)
			FROM operator_steering_deliveries WHERE run_id = ? AND attempt_id = ?
				AND status = 'prepared'`
		args := []any{run.ID, checkpoint.AttemptID}
		if requiredSteeringID != "" {
			query += ` AND message_id = ?`
			args = append(args, requiredSteeringID)
		}
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&prepared); err != nil {
			return domain.SupervisorTurn{}, err
		}
		if requireSteering {
			if prepared != 1 {
				return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
					"active Supervisor turn is not operator steering")
			}
		}
		leaseChanged := checkpoint.LeaseID != lease.LeaseID || checkpoint.LeaseGeneration != lease.Generation
		checkpointChanged := leaseChanged
		if leaseChanged {
			checkpoint.LeaseID = lease.LeaseID
			checkpoint.LeaseGeneration = lease.Generation
			checkpoint.UpdatedAt = time.Now().UTC()
		}
		if pendingInput != "" {
			switch {
			case !checkpoint.HasPendingInput():
				checkpoint.PendingInput = pendingInput
				checkpoint.UpdatedAt = time.Now().UTC()
				checkpointChanged = true
			case checkpoint.PendingInput != pendingInput:
				return domain.SupervisorTurn{}, apperror.New(apperror.CodeConflict, "supervisor turn already has different pending input")
			}
		}
		if checkpointChanged {
			if err := upsertSupervisorCheckpointTx(ctx, tx, checkpoint); err != nil {
				return domain.SupervisorTurn{}, err
			}
		}
		agentNode, _, agentChanged, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
			Status: domain.AgentRunning, ActiveAttemptID: checkpoint.AttemptID,
			TurnsUsed: int64(checkpoint.NextTurn - 1), TokensUsed: checkpoint.TotalTokens,
		}, checkpoint.UpdatedAt)
		if err != nil {
			return domain.SupervisorTurn{}, err
		}
		if agentChanged {
			if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
				return domain.SupervisorTurn{}, err
			}
		}
		_, approvalContinuation, err := approvalContinuationForCheckpointTx(ctx, tx, checkpoint)
		if err != nil {
			return domain.SupervisorTurn{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.SupervisorTurn{}, err
		}
		return domain.SupervisorTurn{Run: executionRun, Mission: mission, Mode: mode, Agent: agentNode,
			Checkpoint: checkpoint, OperatorSteering: prepared == 1, ApprovalContinuation: approvalContinuation, Recovered: true}, nil
	}
	if checkpoint.NextTurn > executionRun.Budget.MaxTurns {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("run %s exhausted its %d turn budget", run.ID, executionRun.Budget.MaxTurns))
	}
	if executionRun.Budget.MaxTokens > 0 && checkpoint.TotalTokens >= executionRun.Budget.MaxTokens {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("run %s exhausted its %d token budget", run.ID, executionRun.Budget.MaxTokens))
	}
	if run.Budget.TimeoutSeconds > 0 && checkpoint.ExecutionMillis >= run.Budget.TimeoutSeconds*1000 {
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeDeadlineExceeded,
			fmt.Sprintf("run %s exhausted its %s execution timeout", run.ID, time.Duration(run.Budget.TimeoutSeconds)*time.Second))
	}
	failedInput := ""
	preferredSteeringID := ""
	if checkpoint.Phase == domain.SupervisorTurnFailed {
		failedInput = checkpoint.PendingInput
		preferredSteeringID, _, err = supersedeOperatorSteeringDeliveryTx(ctx, tx, run,
			checkpoint, time.Now().UTC())
		if err != nil {
			return domain.SupervisorTurn{}, err
		}
		if pendingInput == "" {
			pendingInput = failedInput
		}
	}
	if requiredSteeringID != "" {
		if preferredSteeringID != "" && preferredSteeringID != requiredSteeringID {
			return domain.SupervisorTurn{}, apperror.New(apperror.CodeConflict,
				"failed operator steering delivery does not match the required message")
		}
		preferredSteeringID = requiredSteeringID
	}

	checkpoint.Phase = domain.SupervisorTurnStarted
	checkpoint.LeaseID = lease.LeaseID
	checkpoint.LeaseGeneration = lease.Generation
	checkpoint.AttemptID = idgen.New("attempt")
	checkpoint.PendingInput = pendingInput
	checkpoint.PendingImageCount = approvalImageCount
	checkpoint.PendingAttachmentCount = approvalAttachmentCount
	checkpoint.RepairPhase = domain.ProtocolRepairNone
	checkpoint.RepairReason = ""
	checkpoint.LastError = ""
	checkpoint.UpdatedAt = time.Now().UTC()
	var steeringMessage domain.OperatorSteeringMessage
	steeringFound := false
	if (pendingInput == "" && approvalHandoffID == "") || (preferredSteeringID != "" && pendingInput == failedInput) {
		steeringMessage, steeringFound, err = selectOperatorSteeringForTurnTx(ctx, tx,
			run.ID, preferredSteeringID)
		if err != nil {
			return domain.SupervisorTurn{}, err
		}
		if preferredSteeringID != "" && !steeringFound {
			return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
				"required operator steering message is no longer pending")
		}
		if steeringFound {
			pendingInput = steeringMessage.Content
			checkpoint.PendingInput = pendingInput
			checkpoint.PendingImageCount = steeringMessage.ImageCount
			checkpoint.PendingAttachmentCount = steeringMessage.AttachmentCount
		}
	}
	if requireSteering && !steeringFound {
		if checkpoint.PendingInput != "" {
			return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
				"Supervisor pending input is not queued operator steering")
		}
		return domain.SupervisorTurn{}, apperror.New(apperror.CodeFailedPrecondition,
			"operator steering queue is empty")
	}
	if err := upsertSupervisorCheckpointTx(ctx, tx, checkpoint); err != nil {
		return domain.SupervisorTurn{}, err
	}
	if steeringFound {
		if _, err := prepareOperatorSteeringDeliveryTx(ctx, tx, run, checkpoint,
			steeringMessage, checkpoint.UpdatedAt); err != nil {
			return domain.SupervisorTurn{}, err
		}
	}
	agentNode, _, agentChanged, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
		Status: domain.AgentRunning, ActiveAttemptID: checkpoint.AttemptID,
		TurnsUsed: int64(checkpoint.NextTurn - 1), TokensUsed: checkpoint.TotalTokens,
	}, checkpoint.UpdatedAt)
	if err != nil {
		return domain.SupervisorTurn{}, err
	}
	if agentChanged {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.SupervisorTurn{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorCheckpointedEvent, "run_supervisor", run.ID, map[string]any{
		"phase": checkpoint.Phase, "next_turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
	}); err != nil {
		return domain.SupervisorTurn{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnStartedEvent, "run_supervisor", checkpoint.AttemptID, map[string]any{
		"agent_id": agentNode.ID, "turn": checkpoint.NextTurn,
		"attempt_id": checkpoint.AttemptID, "recovered": false, "approval_continuation_handoff_id": approvalHandoffID,
	}); err != nil {
		return domain.SupervisorTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SupervisorTurn{}, err
	}
	return domain.SupervisorTurn{Run: executionRun, Mission: mission, Mode: mode,
		Agent: agentNode, Checkpoint: checkpoint, OperatorSteering: steeringFound}, nil
}

func (s *SQLiteStore) BindSupervisorTurnInput(ctx context.Context, checkpoint domain.SupervisorCheckpoint, input string) (domain.SupervisorCheckpoint, error) {
	if err := checkpoint.Validate(); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can bind input")
	}
	input, err := normalizeSupervisorInput(input)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if input == "" {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "supervisor input is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, ok, err := getSupervisorCheckpointTx(ctx, tx, checkpoint.RunID)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if !ok {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "supervisor checkpoint was not found")
	}
	if err := requireSupervisorCheckpointLeaseTx(ctx, tx, checkpoint, current); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if current.Phase != domain.SupervisorTurnStarted || current.NextTurn != checkpoint.NextTurn || current.AttemptID != checkpoint.AttemptID {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "supervisor checkpoint changed before input binding")
	}
	if current.HasPendingInput() && current.PendingInput != input {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "supervisor turn already has different pending input")
	}
	if !current.HasPendingInput() {
		current.PendingInput = input
		current.UpdatedAt = time.Now().UTC()
		if err := upsertSupervisorCheckpointTx(ctx, tx, current); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	return current, nil
}

func (s *SQLiteStore) NextSupervisorModelAttempt(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	protocolRepair int, toolRound int,
) (int, int, error) {
	return s.nextSupervisorModelAttempt(ctx, checkpoint, protocolRepair, toolRound, 0)
}

func (s *SQLiteStore) NextSupervisorModelAttemptForSteering(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, protocolRepair int, toolRound int,
	steeringSequence int64,
) (int, int, error) {
	return s.nextSupervisorModelAttempt(ctx, checkpoint, protocolRepair, toolRound, steeringSequence)
}

func (s *SQLiteStore) nextSupervisorModelAttempt(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, protocolRepair int, toolRound int,
	steeringSequence int64,
) (int, int, error) {
	if err := checkpoint.Validate(); err != nil {
		return 0, 0, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return 0, 0, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can call a model")
	}
	if protocolRepair < 0 || protocolRepair > 1 {
		return 0, 0, apperror.New(apperror.CodeInvalidArgument, "protocol repair number must be zero or one")
	}
	if toolRound < 0 || toolRound > domain.MaxSupervisorExecutionRounds {
		return 0, 0, apperror.New(apperror.CodeInvalidArgument, "supervisor tool round is out of range")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	_, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return 0, 0, err
	}
	if protocolRepair == 0 && current.RepairPhase != domain.ProtocolRepairNone {
		return 0, 0, apperror.New(apperror.CodeConflict, "primary model attempt cannot run during protocol repair")
	}
	if protocolRepair == 1 && current.RepairPhase != domain.ProtocolRepairPending {
		return 0, 0, apperror.New(apperror.CodeFailedPrecondition, "protocol repair is not pending")
	}
	var totalCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events
		WHERE run_id = ? AND type = ? AND source = ? AND subject_id LIKE ?`, checkpoint.RunID,
		events.ModelStartedEvent, "model_gateway", supervisorModelSubjectPrefix(checkpoint)+"%").Scan(&totalCount); err != nil {
		return 0, 0, err
	}
	transportCount, err := supervisorModelPurposeCountForSteeringTx(ctx, tx, checkpoint,
		protocolRepair, toolRound, steeringSequence)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return totalCount + 1, transportCount + 1, nil
}

func (s *SQLiteStore) RecordSupervisorModelStarted(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) (bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return false, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return false, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can call a model")
	}
	attempt = sanitizeModelAttempt(attempt)
	if attempt.Outcome != "" || strings.TrimSpace(attempt.ErrorText) != "" {
		return false, apperror.New(apperror.CodeInvalidArgument, "started model attempt cannot have an outcome or error")
	}
	if attempt.StreamEvents != 0 || attempt.StreamBytes != 0 {
		return false, apperror.New(apperror.CodeInvalidArgument, "started model attempt cannot have stream counters")
	}
	if err := attempt.ValidateStarted(); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument, "invalid started model attempt", err)
	}
	if attempt.SupervisorAttemptID != "" && attempt.SupervisorAttemptID != checkpoint.AttemptID {
		return false, apperror.New(apperror.CodeConflict, "model monetary identity does not match its Supervisor attempt")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return false, err
	}
	if err := lockRunningRunForSteeringTx(ctx, tx, run.ID); err != nil {
		return false, err
	}
	if attempt.Purpose == "" {
		if err := commitRootSkillContextTx(ctx, tx, run, current, attempt.Number); err != nil {
			return false, err
		}
		if err := commitExternalRootSkillContextTx(ctx, tx, run, current, attempt.Number); err != nil {
			return false, err
		}
	}
	subject := supervisorModelSubject(checkpoint, attempt.Number)
	exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ModelStartedEvent, subject)
	if err != nil {
		return false, err
	}
	if exists {
		if err := requireSupervisorModelStartedMatchTx(ctx, tx, run.ID, subject, attempt); err != nil {
			return false, err
		}
		var inputEstimate int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload_json,'$.input_estimate'),0) FROM run_events
			WHERE run_id=? AND type=? AND source='model_gateway' AND subject_id=?`, run.ID, events.ModelStartedEvent, subject).Scan(&inputEstimate); err != nil {
			return false, err
		}
		if inputEstimate != attempt.InputEstimate {
			return false, apperror.New(apperror.CodeConflict, "model start replay changed its request estimate")
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	var startedCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events
		WHERE run_id = ? AND type = ? AND source = ? AND subject_id LIKE ?`, run.ID,
		events.ModelStartedEvent, "model_gateway", supervisorModelSubjectPrefix(checkpoint)+"%").Scan(&startedCount); err != nil {
		return false, err
	}
	if attempt.Number != startedCount+1 {
		return false, apperror.New(apperror.CodeConflict, "model attempt number is not the next durable attempt")
	}
	transportCount, err := supervisorModelPurposeCountForSteeringTx(ctx, tx, checkpoint,
		attempt.ProtocolRepair, attempt.ToolRound, attempt.SteeringSequence)
	if err != nil {
		return false, err
	}
	if attempt.Purpose == "" && attempt.TransportNumber() != transportCount+1 {
		return false, apperror.New(apperror.CodeConflict, "model transport attempt number is not the next durable attempt")
	}
	if err := requireSupervisorContextRecoveryReductionTx(ctx, tx, current, attempt); err != nil {
		return false, err
	}
	if attempt.Purpose == llm.ModelPurposeContextCompaction {
		if err := requireNewSupervisorCompactionSourceTx(ctx, tx, current, attempt); err != nil {
			return false, err
		}
	}
	if attempt.ProtocolRepair == 0 && current.RepairPhase != domain.ProtocolRepairNone {
		return false, apperror.New(apperror.CodeConflict, "primary model attempt cannot run during protocol repair")
	}
	if attempt.ProtocolRepair == 1 && current.RepairPhase != domain.ProtocolRepairPending {
		return false, apperror.New(apperror.CodeFailedPrecondition, "protocol repair is not pending")
	}
	if attempt.Purpose == "" {
		sequence, err := midturnSteeringSequenceTx(ctx, tx, run.ID, current.AttemptID)
		if err != nil {
			return false, err
		}
		if sequence != attempt.SteeringSequence {
			return false, apperror.New(apperror.CodeConflict,
				"Current-turn correction arrived before model start; rebuild the request")
		}
	}
	if err := resolveSupersededModelCancellationsTx(ctx, tx, checkpoint, attempt.Number); err != nil {
		return false, err
	}
	payload := map[string]any{
		"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"model_attempt": attempt.Number, "transport_attempt": attempt.TransportNumber(),
		"max_attempts": attempt.MaxAttempts, "protocol_repair": attempt.ProtocolRepair,
		"tool_round":        attempt.ToolRound,
		"steering_sequence": attempt.SteeringSequence,
		"provider":          attempt.Provider, "model": attempt.Model, "context": attempt.Context,
	}
	if attempt.InputEstimate > 0 {
		payload["input_estimate"] = attempt.InputEstimate
	}
	if attempt.ContextWindowTokens > 0 {
		payload["context_window_tokens"] = attempt.ContextWindowTokens
		payload["context_window_source"] = attempt.ContextWindowSource
	}
	addSupervisorCompactionIdentity(payload, attempt)
	addSupervisorMonetaryIdentity(payload, attempt)
	if attempt.Purpose == llm.ModelPurposeContextCompaction || attempt.SupervisorAttemptID != "" {
		payload["lease_id"] = current.LeaseID
		payload["lease_generation"] = current.LeaseGeneration
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.ModelStartedEvent, "model_gateway", subject, payload); err != nil {
		return false, err
	}
	if attempt.Purpose != llm.ModelPurposeContextCompaction {
		if err := markPeerInputDeliveredTx(ctx, tx, checkpoint); err != nil {
			return false, err
		}
	}
	if attempt.ProtocolRepair == 1 {
		repairStarted, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ProtocolRepairStartedEvent, checkpoint.AttemptID)
		if err != nil {
			return false, err
		}
		if !repairStarted {
			if err := appendSupervisorEventTx(ctx, tx, run, events.ProtocolRepairStartedEvent, "run_supervisor", checkpoint.AttemptID, map[string]any{
				"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID, "protocol_repair": 1,
				"model_attempt": attempt.Number,
			}); err != nil {
				return false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) RecordSupervisorModelCancelRequested(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, reason string) (bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return false, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return false, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can cancel a model call")
	}
	attempt = sanitizeModelAttempt(attempt)
	if attempt.Outcome != "" || strings.TrimSpace(attempt.ErrorText) != "" || attempt.StreamEvents != 0 || attempt.StreamBytes != 0 {
		return false, apperror.New(apperror.CodeInvalidArgument, "model cancellation requires the original started attempt")
	}
	if err := attempt.ValidateStarted(); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument, "invalid model cancellation attempt", err)
	}
	reason = sanitizeSupervisorText(reason)
	if reason == "" {
		reason = "active model call cancellation requested"
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, _, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return false, err
	}
	subject := supervisorModelSubject(checkpoint, attempt.Number)
	if err := requireSupervisorModelStartedMatchTx(ctx, tx, run.ID, subject, attempt); err != nil {
		return false, err
	}
	exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ModelCancelRequestedEvent, subject)
	if err != nil {
		return false, err
	}
	if exists {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	for _, terminalType := range []string{events.ModelCompletedEvent, events.ModelFailedEvent} {
		exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID, terminalType, subject)
		if err != nil {
			return false, err
		}
		if exists {
			return false, apperror.New(apperror.CodeFailedPrecondition, "model attempt is already terminal")
		}
	}
	payload := map[string]any{
		"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"model_attempt": attempt.Number, "transport_attempt": attempt.TransportNumber(),
		"protocol_repair": attempt.ProtocolRepair, "tool_round": attempt.ToolRound,
		"provider": attempt.Provider, "model": attempt.Model,
		"reason": reason,
	}
	addSupervisorCompactionIdentity(payload, attempt)
	addSupervisorMonetaryIdentity(payload, attempt)
	if err := appendSupervisorEventTx(ctx, tx, run, events.ModelCancelRequestedEvent, "run_supervisor", subject, payload); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) RecordSupervisorModelDelta(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, delta llm.ModelDelta) (bool, error) {
	if attempt.Purpose != "" {
		return false, apperror.New(apperror.CodeInvalidArgument, "auxiliary model attempts cannot publish deltas")
	}
	if err := checkpoint.Validate(); err != nil {
		return false, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return false, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can record model deltas")
	}
	attempt = sanitizeModelAttempt(attempt)
	if attempt.Outcome != "" || strings.TrimSpace(attempt.ErrorText) != "" || attempt.StreamEvents != 0 || attempt.StreamBytes != 0 {
		return false, apperror.New(apperror.CodeInvalidArgument, "model delta requires the original started attempt")
	}
	if err := attempt.ValidateStarted(); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument, "invalid model delta attempt", err)
	}
	if err := delta.Validate(llm.MaxModelDeltaEvents, llm.MaxModelOutputBytes); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument, "invalid model delta", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, _, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return false, err
	}
	modelSubject := supervisorModelSubject(checkpoint, attempt.Number)
	if err := requireSupervisorModelStartedMatchTx(ctx, tx, run.ID, modelSubject, attempt); err != nil {
		return false, err
	}
	for _, terminalType := range []string{events.ModelCompletedEvent, events.ModelFailedEvent} {
		exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID, terminalType, modelSubject)
		if err != nil {
			return false, err
		}
		if exists {
			return false, apperror.New(apperror.CodeConflict, "model attempt is already terminal")
		}
	}
	deltaSubject := supervisorModelDeltaSubject(checkpoint, attempt.Number, delta.Sequence)
	exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ModelDeltaEvent, deltaSubject)
	if err != nil {
		return false, err
	}
	if exists {
		var payloadJSON string
		if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events
			WHERE run_id = ? AND type = ? AND source = ? AND subject_id = ?`, run.ID,
			events.ModelDeltaEvent, "model_gateway", deltaSubject).Scan(&payloadJSON); err != nil {
			return false, err
		}
		var payload supervisorModelDeltaPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return false, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable model delta payload", err)
		}
		if payload.Sequence != delta.Sequence || payload.ChunkCount != delta.ChunkCount || payload.ByteCount != delta.ByteCount ||
			payload.TotalBytes != delta.TotalBytes || payload.Done != delta.Done ||
			payload.ResponseID != delta.ResponseID || payload.StreamSequence != delta.StreamSequence ||
			!sameStreamBoundaries(payload.Boundaries, delta.Boundaries) {
			return false, apperror.New(apperror.CodeConflict, "model delta replay does not match its durable event")
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	state, err := supervisorModelDeltaStateTx(ctx, tx, run.ID, modelSubject)
	if err != nil {
		return false, err
	}
	if state.Done {
		return false, apperror.New(apperror.CodeConflict, "model stream was already completed")
	}
	if delta.Sequence != state.Count+1 || delta.TotalBytes != state.TotalBytes+delta.ByteCount {
		return false, apperror.New(apperror.CodeConflict, "model delta is not the next durable stream update")
	}
	itemStream := delta.ResponseID != "" || delta.StreamSequence != 0 || len(delta.Boundaries) != 0
	if itemStream {
		if state.Count > 0 && state.ResponseID == "" ||
			state.ResponseID != "" && state.ResponseID != delta.ResponseID ||
			delta.StreamSequence <= state.StreamSequence {
			return false, apperror.New(apperror.CodeConflict,
				"model delta item stream identity or sequence changed")
		}
		for _, boundary := range delta.Boundaries {
			if boundary.Sequence <= state.StreamSequence {
				return false, apperror.New(apperror.CodeConflict,
					"model delta item boundary is not the next durable update")
			}
		}
	} else if state.ResponseID != "" {
		return false, apperror.New(apperror.CodeConflict,
			"model delta omitted established item stream metadata")
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.ModelDeltaEvent, "model_gateway", deltaSubject, map[string]any{
		"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"model_attempt": attempt.Number, "transport_attempt": attempt.TransportNumber(),
		"protocol_repair": attempt.ProtocolRepair, "tool_round": attempt.ToolRound,
		"delta_sequence": delta.Sequence,
		"chunk_count":    delta.ChunkCount, "delta_bytes": delta.ByteCount,
		"total_bytes": delta.TotalBytes, "done": delta.Done,
		"stream_response_id": delta.ResponseID, "stream_sequence": delta.StreamSequence,
		"item_boundaries": delta.Boundaries,
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) RecordSupervisorModelPublicCommentary(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt,
	commentary domain.ModelPublicCommentary,
) (bool, error) {
	if attempt.Purpose != "" {
		return false, apperror.New(apperror.CodeInvalidArgument, "auxiliary model attempts cannot publish commentary")
	}
	if err := checkpoint.Validate(); err != nil {
		return false, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return false, apperror.New(apperror.CodeFailedPrecondition,
			"only a started supervisor turn can record model public commentary")
	}
	attempt = sanitizeModelAttempt(attempt)
	if attempt.Outcome != "" || strings.TrimSpace(attempt.ErrorText) != "" ||
		attempt.RetryAfter != 0 || attempt.RetryPlanned {
		return false, apperror.New(apperror.CodeInvalidArgument,
			"model public commentary requires a non-terminal model attempt")
	}
	if err := attempt.ValidateStarted(); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument,
			"invalid model public commentary attempt", err)
	}
	if err := commentary.Validate(); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument,
			"invalid model public commentary", err)
	}
	if commentary.RunID != checkpoint.RunID || commentary.AttemptID != checkpoint.AttemptID ||
		commentary.ModelAttempt != attempt.Number || commentary.ToolRound != attempt.ToolRound+1 {
		return false, apperror.New(apperror.CodeConflict,
			"model public commentary identity does not match its active attempt")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, _, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return false, err
	}
	modelSubject := supervisorModelSubject(checkpoint, attempt.Number)
	if err := requireSupervisorModelStartedMatchTx(ctx, tx, run.ID, modelSubject, attempt); err != nil {
		return false, err
	}
	for _, terminalType := range []string{events.ModelCompletedEvent, events.ModelFailedEvent} {
		exists, existsErr := supervisorModelEventExistsTx(ctx, tx, run.ID, terminalType, modelSubject)
		if existsErr != nil {
			return false, existsErr
		}
		if exists {
			return false, apperror.New(apperror.CodeConflict, "model attempt is already terminal")
		}
	}
	subject := supervisorModelPublicCommentarySubject(checkpoint, attempt.Number, commentary.Phase)
	exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID,
		events.ModelPublicCommentaryEvent, subject)
	if err != nil {
		return false, err
	}
	if exists {
		var payloadJSON string
		if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events
			WHERE run_id = ? AND type = ? AND source = ? AND subject_id = ?`, run.ID,
			events.ModelPublicCommentaryEvent, "model_gateway", subject).Scan(&payloadJSON); err != nil {
			return false, err
		}
		var durable domain.ModelPublicCommentary
		if err := json.Unmarshal([]byte(payloadJSON), &durable); err != nil {
			return false, apperror.Wrap(apperror.CodeFailedPrecondition,
				"invalid durable model public commentary payload", err)
		}
		if durable != commentary {
			return false, apperror.New(apperror.CodeConflict,
				"model public commentary replay does not match its durable event")
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.ModelPublicCommentaryEvent,
		"model_gateway", subject, commentary); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) RecordSupervisorModelCompleted(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt,
	response llm.ChatResponse,
) (domain.SupervisorCheckpoint, error) {
	return s.recordSupervisorModelCompleted(ctx, checkpoint, attempt, response, 0, nil)
}

// RecordSupervisorModelCompletedForAgent persists the execution Agent beside
// every tool call in the same transaction as the successful model response.
// RootAgentID in tool authority remains a separate authority anchor.
func (s *SQLiteStore) RecordSupervisorModelCompletedForAgent(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt,
	response llm.ChatResponse, attribution domain.AgentAttribution,
) (domain.SupervisorCheckpoint, error) {
	if attribution.Source != domain.AgentAttributionRecorded ||
		attribution.Validate() != nil {
		return domain.SupervisorCheckpoint{}, apperror.New(
			apperror.CodeInvalidArgument, "recorded Supervisor Agent attribution is invalid")
	}
	return s.recordSupervisorModelCompleted(ctx, checkpoint, attempt, response, 0,
		&attribution)
}

func (s *SQLiteStore) RecordSupervisorModelCompletedWithRootActionRecovery(
	ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	attempt llm.ModelAttempt, response llm.ChatResponse,
	discardedTrailingBytes int,
) (domain.SupervisorCheckpoint, error) {
	if discardedTrailingBytes <= 0 || discardedTrailingBytes > llm.MaxModelOutputBytes {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument,
			"root action compatibility recovery has invalid trailing byte count")
	}
	return s.recordSupervisorModelCompleted(ctx, checkpoint, attempt, response,
		discardedTrailingBytes, nil)
}

func (s *SQLiteStore) recordSupervisorModelCompleted(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt,
	response llm.ChatResponse, discardedTrailingBytes int,
	attribution *domain.AgentAttribution,
) (domain.SupervisorCheckpoint, error) {
	if attempt.Purpose != "" {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "auxiliary model completion requires its dedicated receipt")
	}
	attempt = sanitizeModelAttempt(attempt)
	if err := attempt.ValidateCompleted(); err != nil {
		return domain.SupervisorCheckpoint{}, apperror.Wrap(apperror.CodeInvalidArgument, "invalid completed model attempt", err)
	}
	elapsedMillis, err := supervisorModelElapsedMillis(attempt.Elapsed)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if _, _, _, err := supervisorUsage(response.Usage); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if response.Replay.RequiresPrivateAssistantHistory() && response.Text != response.Replay.AssistantText() {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "private assistant replay differs from the accepted model text")
	}
	toolCalls := []llm.ToolCall(nil)
	if len(response.ToolCalls) > 0 {
		var err error
		toolCalls, err = normalizeSupervisorToolCallsForStore(response.ToolCalls,
			checkpoint.RunID, checkpoint.NextTurn, attempt.ToolRound+1)
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	responseID := strings.TrimSpace(response.ResponseID)
	if responseID == "" {
		responseID = llm.StableStreamID("response", checkpoint.RunID, checkpoint.AttemptID,
			strconv.Itoa(attempt.Number))
	}
	toolCalls, outputItems, err := llm.CompleteOutputItems(responseID, response.Text,
		toolCalls, response.Items)
	if err != nil {
		return domain.SupervisorCheckpoint{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"invalid completed model output items", err)
	}
	payload := map[string]any{
		"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"model_attempt": attempt.Number, "transport_attempt": attempt.TransportNumber(),
		"max_attempts": attempt.MaxAttempts, "protocol_repair": attempt.ProtocolRepair,
		"tool_round": attempt.ToolRound,
		"provider":   attempt.Provider, "model": attempt.Model, "outcome": attempt.Outcome,
		"elapsed_millis": elapsedMillis, "usage": response.Usage,
		"stream_events": attempt.StreamEvents, "stream_bytes": attempt.StreamBytes,
		"tool_call_count":    len(toolCalls),
		"stream_response_id": responseID, "output_items": outputItems,
	}
	addSupervisorMonetaryIdentity(payload, attempt)
	if attempt.SupervisorAttemptID != "" {
		payload["usage_unknown"] = supervisorModelUsageUnknown(response.Usage)
	}
	if discardedTrailingBytes > 0 {
		payload["root_action_compatibility"] = map[string]any{
			"version":                  "root_action_compatibility.v1",
			"recovery":                 "trailing_non_json_commentary_discarded",
			"discarded_trailing_bytes": discardedTrailingBytes,
		}
	}
	return s.recordSupervisorModelTerminal(ctx, checkpoint, attempt, events.ModelCompletedEvent, payload,
		supervisorModelTerminalOptions{Usage: &response.Usage, ToolCalls: toolCalls,
			Attribution: attribution, Replay: response.Replay})
}

func (s *SQLiteStore) RecordSupervisorModelFailed(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) (domain.SupervisorCheckpoint, error) {
	attempt = sanitizeModelAttempt(attempt)
	if err := attempt.ValidateFailed(); err != nil {
		return domain.SupervisorCheckpoint{}, apperror.Wrap(apperror.CodeInvalidArgument, "invalid failed model attempt", err)
	}
	elapsedMillis, err := supervisorModelElapsedMillis(attempt.Elapsed)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	payload := map[string]any{
		"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"model_attempt": attempt.Number, "transport_attempt": attempt.TransportNumber(),
		"max_attempts": attempt.MaxAttempts, "protocol_repair": attempt.ProtocolRepair,
		"tool_round": attempt.ToolRound,
		"provider":   attempt.Provider, "model": attempt.Model, "outcome": attempt.Outcome,
		"error": attempt.ErrorText, "elapsed_millis": elapsedMillis,
		"retry_after_millis": attempt.RetryAfter.Milliseconds(), "retry_planned": attempt.RetryPlanned,
		"stream_events": attempt.StreamEvents, "stream_bytes": attempt.StreamBytes,
	}
	addSupervisorCompactionIdentity(payload, attempt)
	addSupervisorMonetaryIdentity(payload, attempt)
	payload["usage_unknown"] = true
	return s.recordSupervisorModelTerminal(ctx, checkpoint, attempt, events.ModelFailedEvent, payload, supervisorModelTerminalOptions{AccountingOnly: attempt.SupervisorAttemptID != ""})
}

func (s *SQLiteStore) RecordSupervisorProtocolFailure(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, response llm.ChatResponse, reason string, requestRepair bool) (domain.SupervisorCheckpoint, error) {
	if attempt.Purpose != "" {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "auxiliary model attempts cannot enter protocol repair")
	}
	reason = sanitizeSupervisorText(reason)
	if reason == "" {
		reason = "provider returned an invalid root lifecycle response"
	}
	attempt = sanitizeModelAttempt(attempt)
	attempt.Outcome = llm.OutcomeInvalidResponse
	attempt.ErrorText = reason
	attempt.RetryAfter = 0
	attempt.RetryPlanned = false
	if err := attempt.ValidateFailed(); err != nil {
		return domain.SupervisorCheckpoint{}, apperror.Wrap(apperror.CodeInvalidArgument, "invalid protocol failure attempt", err)
	}
	elapsedMillis, err := supervisorModelElapsedMillis(attempt.Elapsed)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if _, _, _, err := supervisorUsage(response.Usage); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	phase := domain.ProtocolRepairExhausted
	eventType := events.ProtocolRepairFailedEvent
	continueRound := 0
	textToolRepair := false
	if requestRepair {
		if attempt.ProtocolRepair != 0 {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "only a primary model response can request protocol repair")
		}
		if rejectedRound, toolRepair := domain.SupervisorToolRequestRepairRound(reason); toolRepair &&
			(rejectedRound != attempt.ToolRound || len(response.ToolCalls) == 0) {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "tool request repair must identify the rejected nonempty batch at its original round")
		}
		if round, continuationRepair := domain.SupervisorThreadContinueRepairRound(reason); continuationRepair {
			var action domain.RootAction
			decodeErr := json.Unmarshal([]byte(response.Text), &action)
			action.Version = strings.TrimSpace(action.Version)
			action.Message = strings.TrimSpace(action.Message)
			action.Summary = strings.TrimSpace(action.Summary)
			action.Reason = strings.TrimSpace(action.Reason)
			if round != attempt.ToolRound || len(response.ToolCalls) != 0 ||
				decodeErr != nil || action.Validate() != nil || action.Kind != domain.RootActionContinue {
				return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "Thread continuation correction requires the original tool-free continue at its completed round")
			}
			continueRound = round
		}
		if round, textRepair := domain.SupervisorTextToolRepairRound(reason); textRepair {
			if round != attempt.ToolRound || len(response.ToolCalls) != 0 || !domain.RootActionHasTrailingToolCalls(response.Text) {
				return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "text tool-call correction requires original trailing call markup and no native calls")
			}
			continueRound, textToolRepair = round, true
		}
		phase = domain.ProtocolRepairPending
		eventType = events.ProtocolRepairRequestedEvent
	} else if attempt.ProtocolRepair == 0 {
		// Rejected tool requests and empty answers cannot be fixed by a
		// text-only repair. Charge their usage and record the failure without
		// inventing a repair attempt or an exhausted repair result.
		phase, eventType = domain.ProtocolRepairNone, ""
	}
	failureStage := domain.ThreadFailureInvalidModelResponse
	if len(response.ToolCalls) > 0 {
		failureStage = domain.ThreadFailureToolRequestRejected
	} else if strings.TrimSpace(response.Text) == "" {
		failureStage = domain.ThreadFailureEmptyModelResponse
	}
	payload := map[string]any{
		"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"model_attempt": attempt.Number, "transport_attempt": attempt.TransportNumber(),
		"max_attempts": attempt.MaxAttempts, "protocol_repair": attempt.ProtocolRepair,
		"tool_round": attempt.ToolRound,
		"provider":   attempt.Provider, "model": attempt.Model, "outcome": attempt.Outcome,
		"error": attempt.ErrorText, "elapsed_millis": elapsedMillis,
		"retry_after_millis": 0, "retry_planned": false, "usage": response.Usage,
		"failure_stage":   failureStage,
		"tool_call_count": len(response.ToolCalls),
		"stream_events":   attempt.StreamEvents, "stream_bytes": attempt.StreamBytes,
	}
	addSupervisorMonetaryIdentity(payload, attempt)
	payload["usage_unknown"] = supervisorModelUsageUnknown(response.Usage)
	var rejected *llm.ToolRequestRejection
	if len(response.ToolCalls) > 0 {
		rejected = response.ToolRequestRejection
		if rejected == nil {
			// Direct store callers have received evidence but no offered schema.
			// Never reconstruct an old capability/schema from current state.
			rejected = llm.NewToolRequestRejection(response.ToolCalls, nil)
		}
		if !rejected.MatchesReceived(response.ToolCalls) {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "rejected tool diagnostic must bind the original received batch")
		}
	}
	return s.recordSupervisorModelTerminal(ctx, checkpoint, attempt, events.ModelFailedEvent, payload, supervisorModelTerminalOptions{
		Usage: &response.Usage, RepairPhase: phase, RepairReason: reason, RepairEvent: eventType,
		ThreadContinueRepairRound: continueRound,
		TextToolRepair:            textToolRepair,
		RejectedToolRequest:       rejected,
	})
}

type supervisorModelTerminalOptions struct {
	ThreadContinueRepairRound int
	TextToolRepair            bool
	AccountingOnly            bool
	Usage                     *llm.Usage
	RepairPhase               domain.ProtocolRepairPhase
	RepairReason              string
	RepairEvent               string
	ToolCalls                 []llm.ToolCall
	Attribution               *domain.AgentAttribution
	Replay                    *llm.ProviderReplay
	RejectedToolRequest       *llm.ToolRequestRejection
}

func (s *SQLiteStore) recordSupervisorModelTerminal(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, eventType string, payload map[string]any, options supervisorModelTerminalOptions) (domain.SupervisorCheckpoint, error) {
	if attempt.DecodeMillis != nil {
		payload["decode_millis"] = *attempt.DecodeMillis
	}
	if attempt.SteeringSequence > 0 {
		payload["steering_sequence"] = attempt.SteeringSequence
	}
	if eventType == events.ModelFailedEvent && attempt.FailureReason != "" && attempt.FailureReason != llm.ProviderFailureNone {
		if !attempt.FailureReason.Valid() {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "invalid model failure reason")
		}
		payload["failure_reason"] = attempt.FailureReason
	}
	if options.Replay != nil && (eventType != events.ModelCompletedEvent || options.AccountingOnly || attempt.Purpose != "" ||
		(len(options.ToolCalls) == 0 && !options.Replay.RequiresPrivateAssistantHistory())) {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "provider replay requires a supported successful primary response")
	}
	if options.RejectedToolRequest != nil && (eventType != events.ModelFailedEvent || options.AccountingOnly || attempt.Purpose != "" || len(options.ToolCalls) != 0 || options.Replay != nil) {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "rejection diagnostics require a non-executable primary protocol failure")
	}
	if err := checkpoint.Validate(); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can finish a model attempt")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	run, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if attempt.Purpose == llm.ModelPurposeContextCompaction || (options.AccountingOnly && attempt.SupervisorAttemptID != "") {
		// A sent auxiliary call can finish after Stop or owner loss. Only its
		// original start permits late accounting, never renewed execution.
		run, current, err = requireSupervisorCompactionAccountingTx(ctx, tx, checkpoint, attempt)
	}
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if options.ThreadContinueRepairRound > 0 || options.TextToolRepair {
		if err := requireThreadContinueRepairSourceTx(ctx, tx, run, current, options.ThreadContinueRepairRound); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if !options.AccountingOnly && eventType == events.ModelFailedEvent && attempt.ProtocolRepair == 1 && domain.IsSupervisorToolRequestRepair(current.RepairReason) {
		// A text-only answer cannot conceal the original rejected tool request.
		// Keep its marker through exhaustion so terminal replay has the same
		// durable failure classification after reopening the checkpoint.
		round, _ := domain.SupervisorToolRequestRepairRound(current.RepairReason)
		corrected, err := supervisorToolRequestRepairHasResultTx(ctx, tx, current, round)
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		if !corrected {
			payload["failure_stage"] = domain.ThreadFailureToolRequestRejected
		}
		if options.RepairPhase == domain.ProtocolRepairExhausted && !domain.IsSupervisorToolRequestRepair(options.RepairReason) {
			options.RepairReason, err = domain.NewSupervisorToolRequestRepairReason(round, options.RepairReason)
			if err != nil {
				return domain.SupervisorCheckpoint{}, err
			}
		}
	}
	subject := supervisorModelSubject(checkpoint, attempt.Number)
	started, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ModelStartedEvent, subject)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if !started {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "model attempt was not started")
	}
	if err := requireSupervisorModelStartedMatchTx(ctx, tx, run.ID, subject, attempt); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	deltaState, err := supervisorModelDeltaStateTx(ctx, tx, run.ID, subject)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if attempt.StreamEvents != deltaState.Count || attempt.StreamBytes != deltaState.TotalBytes {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "model terminal stream counters do not match durable deltas")
	}
	if eventType == events.ModelCompletedEvent && deltaState.Count > 0 && !deltaState.Done {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "completed model attempt requires a completed stream")
	}
	completed, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ModelCompletedEvent, subject)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	failed, err := supervisorModelEventExistsTx(ctx, tx, run.ID, events.ModelFailedEvent, subject)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if (eventType == events.ModelCompletedEvent && completed) || (eventType == events.ModelFailedEvent && failed) {
		if eventType == events.ModelFailedEvent && !options.AccountingOnly {
			if err := requireSupervisorToolRejectionReplayTx(ctx, tx, checkpoint, attempt, options.RejectedToolRequest); err != nil {
				return domain.SupervisorCheckpoint{}, err
			}
		}
		if eventType == events.ModelCompletedEvent && attempt.Purpose == "" && !options.AccountingOnly {
			if err := requireSupervisorProviderReplayMatchTx(ctx, tx, checkpoint, attempt, options.Replay, options.ToolCalls); err != nil {
				return domain.SupervisorCheckpoint{}, err
			}
		}
		if attempt.Purpose == llm.ModelPurposeContextCompaction || attempt.SupervisorAttemptID != "" {
			if err := requireSupervisorCompactionTerminalReplayTx(ctx, tx, run.ID, subject, eventType, payload); err != nil {
				if options.AccountingOnly && options.Usage != nil && current.AttemptID == checkpoint.AttemptID && current.NextTurn == checkpoint.NextTurn && current.LeaseID == checkpoint.LeaseID && current.LeaseGeneration == checkpoint.LeaseGeneration {
					acknowledged, lookupErr := supervisorAccountingUsageAcknowledgedTx(ctx, tx, run.ID, subject, attempt, *options.Usage, payload)
					if lookupErr != nil {
						return domain.SupervisorCheckpoint{}, lookupErr
					}
					if acknowledged {
						return current, err
					}
				}
				return domain.SupervisorCheckpoint{}, err
			}
		}
		if options.Attribution != nil && len(options.ToolCalls) > 0 {
			if err := requireSupervisorToolReplayAttributionTx(ctx, tx, checkpoint,
				options.ToolCalls, *options.Attribution); err != nil {
				return domain.SupervisorCheckpoint{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		return current, nil
	}
	if completed || failed {
		if options.AccountingOnly && options.Usage != nil {
			acknowledged, err := supervisorAccountingUsageAcknowledgedTx(ctx, tx, run.ID, subject, attempt, *options.Usage, payload)
			if err != nil {
				return domain.SupervisorCheckpoint{}, err
			}
			if acknowledged && current.AttemptID == checkpoint.AttemptID && current.NextTurn == checkpoint.NextTurn && current.LeaseID == checkpoint.LeaseID && current.LeaseGeneration == checkpoint.LeaseGeneration {
				return current, apperror.New(apperror.CodeConflict, "model usage was already accounted by its original terminal event")
			}
		}
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "model attempt already has a terminal event")
	}
	if !options.AccountingOnly && attempt.Purpose == "" && attempt.ProtocolRepair == 0 && current.RepairPhase != domain.ProtocolRepairNone {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "primary model attempt cannot finish during protocol repair")
	}
	if !options.AccountingOnly && attempt.ProtocolRepair == 1 && current.RepairPhase != domain.ProtocolRepairPending {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "protocol repair is not pending")
	}
	if options.RepairPhase != domain.ProtocolRepairNone {
		switch options.RepairPhase {
		case domain.ProtocolRepairPending:
			if current.RepairPhase != domain.ProtocolRepairNone {
				return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "protocol repair was already requested")
			}
		case domain.ProtocolRepairExhausted:
			if current.RepairPhase != domain.ProtocolRepairPending {
				return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "protocol repair is not pending")
			}
		default:
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "invalid protocol repair transition")
		}
		if strings.TrimSpace(options.RepairReason) == "" || strings.TrimSpace(options.RepairEvent) == "" {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "protocol repair transition requires a reason and event")
		}
	}
	rejectedRound, toolRequestRepair := domain.SupervisorToolRequestRepairRound(current.RepairReason)
	toolRequestRepair = toolRequestRepair && current.RepairPhase == domain.ProtocolRepairPending && attempt.ProtocolRepair == 1
	_, continuationRepair := domain.SupervisorThreadContinueRepairRound(current.RepairReason)
	continuationRepair = continuationRepair && current.RepairPhase == domain.ProtocolRepairPending && attempt.ProtocolRepair == 1
	_, textToolRepair := domain.SupervisorTextToolRepairRound(current.RepairReason)
	textToolRepair = textToolRepair && current.RepairPhase == domain.ProtocolRepairPending && attempt.ProtocolRepair == 1
	if len(options.ToolCalls) > 0 && (eventType != events.ModelCompletedEvent || options.Usage == nil ||
		(attempt.ProtocolRepair != 0 && !toolRequestRepair && !continuationRepair && !textToolRepair) || options.RepairPhase != domain.ProtocolRepairNone) {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument,
			"supervisor tool calls require a successful primary response or a pending Go-validated tool correction")
	}
	if !options.AccountingOnly && eventType == events.ModelCompletedEvent && toolRequestRepair && len(options.ToolCalls) == 0 {
		corrected, err := supervisorToolRequestRepairHasResultTx(ctx, tx, current, rejectedRound)
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		if !corrected {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument,
				"tool request repair requires a corrected tool batch result before a text-only completion")
		}
	}
	if options.AccountingOnly && (len(options.ToolCalls) > 0 || options.Attribution != nil || options.RepairPhase != domain.ProtocolRepairNone) {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "accounting-only terminal cannot execute tools or change repair state")
	}
	if attempt.Purpose == llm.ModelPurposeContextCompaction {
		if len(options.ToolCalls) != 0 || options.Attribution != nil || options.RepairPhase != domain.ProtocolRepairNone {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "compaction cannot change root tool or repair state")
		}
		payload["checkpoint_before_sha256"] = supervisorCompactionHash(current)
	}
	elapsedMillis, err := supervisorModelElapsedMillis(attempt.Elapsed)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	current.ExecutionMillis, err = supervisorAddCounter(current.ExecutionMillis, elapsedMillis, "execution time")
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if options.Usage != nil {
		inputTokens, outputTokens, totalTokens, err := supervisorUsage(*options.Usage)
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		current.InputTokens, err = supervisorAddCounter(current.InputTokens, inputTokens, "input token")
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		current.OutputTokens, err = supervisorAddCounter(current.OutputTokens, outputTokens, "output token")
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		current.TotalTokens, err = supervisorAddCounter(current.TotalTokens, totalTokens, "total token")
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if options.RepairPhase != domain.ProtocolRepairNone {
		current.RepairPhase = options.RepairPhase
		current.RepairReason = sanitizeSupervisorText(options.RepairReason)
	}
	current.UpdatedAt = time.Now().UTC()
	if attempt.Purpose == llm.ModelPurposeContextCompaction {
		payload["checkpoint_after_sha256"] = supervisorCompactionHash(current)
	}
	if err := upsertSupervisorCheckpointTx(ctx, tx, current); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	// Tools can execute before the turn completes. Publish their charged root
	// usage atomically, deriving lifecycle state from the current Run so late
	// accounting after Stop cannot revive an Agent or restore an old attempt.
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile,
		workspace_id, scope_json, created_at, updated_at FROM missions WHERE id = ?`, run.MissionID))
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	projection, err := rootAgentProjectionForRunTx(ctx, tx, run)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if _, _, changed, err := syncRootAgentTx(ctx, tx, run, mission, projection, current.UpdatedAt); err != nil {
		return domain.SupervisorCheckpoint{}, err
	} else if changed {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, eventType, "model_gateway", subject, payload); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if err := insertSupervisorToolRejectionTx(ctx, tx, checkpoint, attempt, options.RejectedToolRequest); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if err := resolveSupervisorModelCancellationTx(ctx, tx, checkpoint, attempt); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if len(options.ToolCalls) > 0 {
		if err := insertSupervisorToolRoundTx(ctx, tx, run, current, attempt,
			options.ToolCalls, options.Attribution); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		if err := insertSupervisorProviderReplayTx(ctx, tx, current, attempt, options.Replay, options.ToolCalls); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	} else if options.Replay.RequiresPrivateAssistantHistory() {
		if err := insertSupervisorAssistantReplayTx(ctx, tx, current, attempt, options.Replay); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if options.RepairPhase != domain.ProtocolRepairNone {
		if err := appendSupervisorEventTx(ctx, tx, run, options.RepairEvent, "run_supervisor", checkpoint.AttemptID, map[string]any{
			"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
			"protocol_repair": 1, "model_attempt": attempt.Number, "reason": current.RepairReason,
		}); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	return current, nil
}

func supervisorToolRequestRepairHasResultTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint, rejectedRound int) (bool, error) {
	var corrected int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_tool_rounds
		WHERE run_id = ? AND turn = ? AND attempt_id = ? AND round > ? AND completed_at IS NOT NULL`,
		checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID, rejectedRound).Scan(&corrected)
	return corrected > 0, err
}

func requireActiveSupervisorAttemptTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint) (domain.Run, domain.SupervisorCheckpoint, error) {
	current, ok, err := getSupervisorCheckpointTx(ctx, tx, checkpoint.RunID)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	if !ok {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "supervisor checkpoint was not found")
	}
	if err := requireSupervisorCheckpointLeaseTx(ctx, tx, checkpoint, current); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	if current.Phase != domain.SupervisorTurnStarted || current.NextTurn != checkpoint.NextTurn || current.AttemptID != checkpoint.AttemptID {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "supervisor checkpoint changed before model attempt")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, checkpoint.RunID))
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "run stopped before model attempt")
	}
	return run, current, nil
}

func supervisorModelEventExistsTx(ctx context.Context, tx *sql.Tx, runID string, eventType string, subject string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ? AND type = ? AND subject_id = ?`,
		runID, eventType, subject).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func supervisorModelPurposeCountTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	protocolRepair int, toolRound int,
) (int, error) {
	return supervisorModelPurposeCountForSteeringTx(ctx, tx, checkpoint, protocolRepair, toolRound, 0)
}

func supervisorModelPurposeCountForSteeringTx(ctx context.Context, tx *sql.Tx,
	checkpoint domain.SupervisorCheckpoint, protocolRepair int, toolRound int,
	steeringSequence int64,
) (int, error) {
	recoveredThrough, err := supervisorContextRecoverySourceTx(ctx, tx, checkpoint, protocolRepair, toolRound)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM run_events WHERE run_id = ? AND type = ? AND source = ?
		AND subject_id LIKE ? ORDER BY sequence`, checkpoint.RunID, events.ModelStartedEvent, "model_gateway",
		supervisorModelSubjectPrefix(checkpoint)+"%")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return 0, err
		}
		payload, err := parseSupervisorModelStartedPayload(payloadJSON)
		if err != nil {
			return 0, err
		}
		if payload.Purpose == "" && payload.protocolRepair() == protocolRepair &&
			payload.toolRound() == toolRound && payload.SteeringSequence == steeringSequence &&
			payload.ModelAttempt > recoveredThrough {
			count++
		}
	}
	return count, rows.Err()
}

type supervisorModelStartedPayload struct {
	SupervisorAttemptID    string                 `json:"supervisor_attempt_id,omitempty"`
	Turn                   int                    `json:"turn,omitempty"`
	AttemptID              string                 `json:"attempt_id,omitempty"`
	Purpose                string                 `json:"purpose,omitempty"`
	CompactionSourceSHA256 string                 `json:"compaction_source_sha256,omitempty"`
	MonetaryAttemptNumber  int64                  `json:"monetary_attempt_number,omitempty"`
	LeaseID                string                 `json:"lease_id,omitempty"`
	LeaseGeneration        int64                  `json:"lease_generation,omitempty"`
	ModelAttempt           int                    `json:"model_attempt"`
	InputEstimate          int                    `json:"input_estimate,omitempty"`
	TransportAttempt       *int                   `json:"transport_attempt"`
	MaxAttempts            int                    `json:"max_attempts"`
	ProtocolRepair         *int                   `json:"protocol_repair"`
	ToolRound              *int                   `json:"tool_round"`
	SteeringSequence       int64                  `json:"steering_sequence,omitempty"`
	Provider               string                 `json:"provider"`
	Model                  string                 `json:"model"`
	Context                *llm.ModelContextAudit `json:"context,omitempty"`
}

func (p supervisorModelStartedPayload) transportAttempt() int {
	if p.TransportAttempt != nil {
		return *p.TransportAttempt
	}
	return p.ModelAttempt
}

func (p supervisorModelStartedPayload) protocolRepair() int {
	if p.ProtocolRepair != nil {
		return *p.ProtocolRepair
	}
	return 0
}

func (p supervisorModelStartedPayload) toolRound() int {
	if p.ToolRound != nil {
		return *p.ToolRound
	}
	return 0
}

func parseSupervisorModelStartedPayload(payloadJSON string) (supervisorModelStartedPayload, error) {
	var payload supervisorModelStartedPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return supervisorModelStartedPayload{}, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable model start payload", err)
	}
	if payload.ModelAttempt <= 0 || payload.MaxAttempts <= 0 || payload.InputEstimate < 0 ||
		payload.SteeringSequence < 0 || strings.TrimSpace(payload.Provider) == "" ||
		strings.TrimSpace(payload.Model) == "" {
		return supervisorModelStartedPayload{}, apperror.New(apperror.CodeFailedPrecondition, "incomplete durable model start payload")
	}
	if payload.transportAttempt() <= 0 || payload.transportAttempt() > payload.MaxAttempts ||
		payload.protocolRepair() < 0 || payload.protocolRepair() > 1 ||
		payload.toolRound() < 0 || payload.toolRound() > domain.MaxSupervisorExecutionRounds {
		return supervisorModelStartedPayload{}, apperror.New(apperror.CodeFailedPrecondition, "invalid durable model start counters")
	}
	if payload.Context != nil {
		if err := payload.Context.Validate(); err != nil {
			return supervisorModelStartedPayload{}, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable model context audit", err)
		}
	}
	identity := llm.ModelAttempt{SupervisorAttemptID: payload.SupervisorAttemptID, Number: payload.ModelAttempt, TransportAttempt: payload.transportAttempt(), MaxAttempts: payload.MaxAttempts, ProtocolRepair: payload.protocolRepair(), ToolRound: payload.toolRound(), SteeringSequence: payload.SteeringSequence, Provider: payload.Provider, Model: payload.Model, Purpose: payload.Purpose, CompactionSourceSHA256: payload.CompactionSourceSHA256}
	if err := identity.ValidateStarted(); err != nil {
		return supervisorModelStartedPayload{}, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable model purpose", err)
	}
	if (payload.Purpose != "" || payload.SupervisorAttemptID != "") && payload.MonetaryAttemptNumber != identity.MonetaryAttemptNumber() {
		return supervisorModelStartedPayload{}, apperror.New(apperror.CodeFailedPrecondition, "invalid durable compaction cost identity")
	}
	if (payload.Purpose == llm.ModelPurposeContextCompaction || payload.SupervisorAttemptID != "") && (payload.LeaseID == "" || payload.LeaseGeneration <= 0 || payload.Turn <= 0 || payload.AttemptID == "") {
		return supervisorModelStartedPayload{}, apperror.New(apperror.CodeFailedPrecondition, "compaction start lease identity is missing")
	}
	if payload.SupervisorAttemptID != "" && payload.SupervisorAttemptID != payload.AttemptID {
		return supervisorModelStartedPayload{}, apperror.New(apperror.CodeFailedPrecondition, "durable model monetary identity differs from its execution attempt")
	}
	return payload, nil
}

func requireSupervisorModelStartedMatchTx(ctx context.Context, tx *sql.Tx, runID string, subject string, attempt llm.ModelAttempt) error {
	var payloadJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events
		WHERE run_id = ? AND type = ? AND source = ? AND subject_id = ?`, runID, events.ModelStartedEvent, "model_gateway", subject).Scan(&payloadJSON); err != nil {
		return err
	}
	payload, err := parseSupervisorModelStartedPayload(payloadJSON)
	if err != nil {
		return err
	}
	if payload.ModelAttempt != attempt.Number || payload.transportAttempt() != attempt.TransportNumber() ||
		payload.MaxAttempts != attempt.MaxAttempts || payload.protocolRepair() != attempt.ProtocolRepair ||
		payload.toolRound() != attempt.ToolRound || payload.SteeringSequence != attempt.SteeringSequence ||
		payload.Provider != attempt.Provider || payload.Model != attempt.Model || payload.Purpose != attempt.Purpose || payload.CompactionSourceSHA256 != attempt.CompactionSourceSHA256 || payload.SupervisorAttemptID != attempt.SupervisorAttemptID {
		return apperror.New(apperror.CodeConflict, "model terminal metadata does not match its durable start event")
	}
	return nil
}

func supervisorModelSubjectPrefix(checkpoint domain.SupervisorCheckpoint) string {
	return checkpoint.AttemptID + "/model/"
}

func supervisorModelSubject(checkpoint domain.SupervisorCheckpoint, number int) string {
	return fmt.Sprintf("%s%d", supervisorModelSubjectPrefix(checkpoint), number)
}

func supervisorModelDeltaSubject(checkpoint domain.SupervisorCheckpoint, number int, sequence int) string {
	return fmt.Sprintf("%s/delta/%d", supervisorModelSubject(checkpoint, number), sequence)
}

func supervisorModelPublicCommentarySubject(checkpoint domain.SupervisorCheckpoint, number int,
	phase domain.ModelPublicCommentaryPhase,
) string {
	return fmt.Sprintf("%s/public-commentary/%s", supervisorModelSubject(checkpoint, number), phase)
}

type supervisorModelDeltaPayload struct {
	Sequence       int                  `json:"delta_sequence"`
	ChunkCount     int                  `json:"chunk_count"`
	ByteCount      int                  `json:"delta_bytes"`
	TotalBytes     int                  `json:"total_bytes"`
	Done           bool                 `json:"done"`
	ResponseID     string               `json:"stream_response_id,omitempty"`
	StreamSequence int                  `json:"stream_sequence,omitempty"`
	Boundaries     []llm.StreamBoundary `json:"item_boundaries,omitempty"`
}

type supervisorModelDeltaState struct {
	Count          int
	TotalBytes     int
	Done           bool
	ResponseID     string
	StreamSequence int
}

func supervisorModelDeltaStateTx(ctx context.Context, tx *sql.Tx, runID string, modelSubject string) (supervisorModelDeltaState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM run_events
		WHERE run_id = ? AND type = ? AND source = ? AND subject_id LIKE ? ORDER BY sequence`,
		runID, events.ModelDeltaEvent, "model_gateway", modelSubject+"/delta/%")
	if err != nil {
		return supervisorModelDeltaState{}, err
	}
	defer rows.Close()
	state := supervisorModelDeltaState{}
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return supervisorModelDeltaState{}, err
		}
		var payload supervisorModelDeltaPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return supervisorModelDeltaState{}, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable model delta payload", err)
		}
		delta := llm.ModelDelta{
			Sequence: payload.Sequence, ChunkCount: payload.ChunkCount, ByteCount: payload.ByteCount,
			TotalBytes: payload.TotalBytes, Done: payload.Done, ResponseID: payload.ResponseID,
			StreamSequence: payload.StreamSequence, Boundaries: payload.Boundaries,
		}
		if err := delta.Validate(llm.MaxModelDeltaEvents, llm.MaxModelOutputBytes); err != nil {
			return supervisorModelDeltaState{}, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable model delta counters", err)
		}
		if payload.Sequence != state.Count+1 || payload.ChunkCount < 0 || payload.ByteCount < 0 ||
			payload.TotalBytes != state.TotalBytes+payload.ByteCount || state.Done {
			return supervisorModelDeltaState{}, apperror.New(apperror.CodeFailedPrecondition, "inconsistent durable model delta sequence")
		}
		itemStream := payload.ResponseID != "" || payload.StreamSequence != 0 || len(payload.Boundaries) != 0
		if itemStream {
			if state.Count > 0 && state.ResponseID == "" ||
				state.ResponseID != "" && state.ResponseID != payload.ResponseID ||
				payload.StreamSequence <= state.StreamSequence {
				return supervisorModelDeltaState{}, apperror.New(apperror.CodeFailedPrecondition,
					"inconsistent durable item stream identity or sequence")
			}
			for _, boundary := range payload.Boundaries {
				if boundary.Sequence <= state.StreamSequence {
					return supervisorModelDeltaState{}, apperror.New(apperror.CodeFailedPrecondition,
						"durable item boundary was replayed out of order")
				}
			}
			state.ResponseID = payload.ResponseID
			state.StreamSequence = payload.StreamSequence
		} else if state.ResponseID != "" {
			return supervisorModelDeltaState{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable item stream metadata disappeared")
		}
		state.Count++
		state.TotalBytes = payload.TotalBytes
		state.Done = payload.Done
	}
	if err := rows.Err(); err != nil {
		return supervisorModelDeltaState{}, err
	}
	if state.Count > llm.MaxModelDeltaEvents || state.TotalBytes > llm.MaxModelOutputBytes {
		return supervisorModelDeltaState{}, apperror.New(apperror.CodeFailedPrecondition, "durable model delta limits were exceeded")
	}
	return state, nil
}

func sameStreamBoundaries(left []llm.StreamBoundary, right []llm.StreamBoundary) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sanitizeModelAttempt(attempt llm.ModelAttempt) llm.ModelAttempt {
	attempt.Provider = redact.String(strings.TrimSpace(attempt.Provider))
	attempt.Model = redact.String(strings.TrimSpace(attempt.Model))
	attempt.ErrorText = sanitizeSupervisorText(attempt.ErrorText)
	attempt.ContextWindowSource = redact.String(strings.TrimSpace(attempt.ContextWindowSource))
	return attempt
}

func sanitizeSupervisorText(value string) string {
	value = redact.String(strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > maxSupervisorErrorChars {
		value = string(runes[:maxSupervisorErrorChars])
	}
	return value
}

func (s *SQLiteStore) CompleteSupervisorTurn(ctx context.Context, checkpoint domain.SupervisorCheckpoint, response llm.ChatResponse, action domain.RootAction, decision policy.Decision, elapsed time.Duration) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error) {
	return s.completeSupervisorTurn(ctx, checkpoint, response, action, decision, elapsed, false)
}

func (s *SQLiteStore) CompleteSupervisorToolBoundary(ctx context.Context, checkpoint domain.SupervisorCheckpoint, response llm.ChatResponse, action domain.RootAction, decision policy.Decision, elapsed time.Duration) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error) {
	return s.completeSupervisorTurn(ctx, checkpoint, response, action, decision, elapsed, true)
}

func (s *SQLiteStore) completeSupervisorTurn(ctx context.Context, checkpoint domain.SupervisorCheckpoint, response llm.ChatResponse, action domain.RootAction, decision policy.Decision, elapsed time.Duration, allowToolBoundary bool) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error) {
	emptyMessages := session.TurnMessages{}
	if err := checkpoint.Validate(); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition, "only a started supervisor turn can complete")
	}
	if !decision.Allowed {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition, "denied supervisor output cannot be completed")
	}
	action = sanitizeRootAction(action)
	if err := action.Validate(); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid root lifecycle action", err)
	}
	nativeResponse, acceptedAction := response, action
	response.Text = action.Message
	if _, _, _, err := supervisorUsage(response.Usage); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	elapsedMillis, err := supervisorElapsedMillis(elapsed)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	defer func() { _ = tx.Rollback() }()
	current, ok, err := getSupervisorCheckpointTx(ctx, tx, checkpoint.RunID)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if !ok {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition, "supervisor checkpoint was not found")
	}
	if err := requireSupervisorCheckpointLeaseTx(ctx, tx, checkpoint, current); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, checkpoint.RunID))
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	progressGuard, progressGuardFound, err := getRunProgressGuardTx(ctx, tx, run.ID)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	livelockReplay := action.Kind == domain.RootActionContinue &&
		current.Phase == domain.SupervisorWaiting && current.NextTurn == checkpoint.NextTurn+1 &&
		progressGuardFound && progressGuard.Status == domain.RunProgressDetected &&
		progressGuard.LastTurn == checkpoint.NextTurn
	completionReplay := livelockReplay || current.Phase == supervisorPhaseForAction(action.Kind) &&
		current.NextTurn == checkpoint.NextTurn+1
	if allowToolBoundary && !completionReplay && current.Phase == domain.SupervisorTurnStarted && current.NextTurn == checkpoint.NextTurn+1 {
		completionReplay, err = supervisorToolBoundaryReplayTx(ctx, tx, checkpoint, current)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if !completionReplay && current.Phase == domain.SupervisorIdle &&
		current.NextTurn == checkpoint.NextTurn+1 &&
		(action.Kind == domain.RootActionFinish || action.Kind == domain.RootActionWait) {
		completionReplay, err = operatorSteeringActionDeferredTx(ctx, tx, checkpoint)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if completionReplay {
		if err := requireSupervisorAssistantCompletionReplayTx(ctx, tx, checkpoint, nativeResponse, acceptedAction); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
		if err := tx.Commit(); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
		return run, current, emptyMessages, nil
	}
	if current.Phase != domain.SupervisorTurnStarted || current.NextTurn != checkpoint.NextTurn || current.AttemptID != checkpoint.AttemptID {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeConflict, "supervisor checkpoint changed before turn completion")
	}
	if run.Status != domain.RunRunning {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition, "run stopped before turn completion")
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile, workspace_id, scope_json,
		created_at, updated_at FROM missions WHERE id = ?`, run.MissionID))
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	agentNode, agentFound, err := getRootAgentTx(ctx, tx, run.ID)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if !agentFound {
		agentNode, _, _, err = syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
			Status: domain.AgentRunning, ActiveAttemptID: checkpoint.AttemptID,
			TurnsUsed: int64(checkpoint.NextTurn - 1), TokensUsed: checkpoint.TotalTokens,
		}, checkpoint.UpdatedAt)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	} else if agentNode.Status != domain.AgentRunning || agentNode.ActiveAttemptID != checkpoint.AttemptID {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeConflict,
			"root agent is not bound to the completing supervisor attempt")
	}
	if current.RepairPhase == domain.ProtocolRepairExhausted {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition, "exhausted protocol repair cannot complete a supervisor turn")
	}
	if err := requireLatestSupervisorModelCompletedTx(ctx, tx, run.ID, checkpoint); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	privateModelAttempt, privateReplayDigest, err := prepareSupervisorAssistantBindingTx(ctx, tx, checkpoint, nativeResponse)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := requireSupervisorToolsReadyTx(ctx, tx, checkpoint); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := lockRunningRunForSteeringTx(ctx, tx, run.ID); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := requireLatestSupervisorModelSteeringCurrentTx(ctx, tx, checkpoint); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	requestedAction := action.Kind
	_, continuationRepair := domain.SupervisorThreadContinueRepairRound(current.RepairReason)
	_, textToolRepair := domain.SupervisorTextToolRepairRound(current.RepairReason)
	if (continuationRepair || textToolRepair) &&
		current.RepairPhase == domain.ProtocolRepairPending && action.Kind == domain.RootActionContinue && !allowToolBoundary {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition,
			"Thread continuation correction must explicitly finish the reply or wait instead of silently consuming the input")
	}
	var interactiveSteering int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM operator_steering_deliveries
		WHERE run_id = ? AND attempt_id = ? AND status = 'prepared'`,
		run.ID, checkpoint.AttemptID).Scan(&interactiveSteering); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if interactiveSteering > 1 {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages,
			apperror.New(apperror.CodeConflict, "supervisor attempt has multiple prepared operator messages")
	}
	_, approvalContinuation, err := approvalContinuationForCheckpointTx(ctx, tx, current)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if run.Config.Interactive && (interactiveSteering == 1 || approvalContinuation) && action.Kind == domain.RootActionFinish {
		action.Kind = domain.RootActionContinue
		action.Summary = ""
		action.Reason = ""
		response.Text = action.Message
	}
	deferredSteering := 0
	if action.Kind == domain.RootActionFinish || action.Kind == domain.RootActionWait {
		deferredSteering, err = pendingOperatorSteeringAfterCurrentTx(ctx, tx, checkpoint)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
		if deferredSteering > 0 {
			action.Kind = domain.RootActionContinue
			action.Summary = ""
			action.Reason = ""
			response.Text = action.Message
		}
	}
	if action.Kind == domain.RootActionFinish {
		if err := requireRunModeAllowsCompletionTx(ctx, tx, run.ID); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
		if err := requireNoActiveWorkItemsForFinishTx(ctx, tx, run.ID); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	completedAt := time.Now().UTC()
	action, err = evaluateSupervisorProgressTx(ctx, tx, run, checkpoint, action,
		requestedAction, completedAt)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	retainBackgroundJobs := false
	if run.Config.Interactive && (interactiveSteering == 1 || approvalContinuation) &&
		requestedAction == domain.RootActionWait && action.Kind == domain.RootActionWait {
		retainBackgroundJobs, err = supervisorBackgroundJobWaitTx(ctx, tx, run, agentNode.ID, completedAt)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	response.Text = action.Message
	repairCompleted := current.RepairPhase == domain.ProtocolRepairPending
	var boundaryMessage domain.OperatorSteeringMessage
	var boundaryHandoff string
	if allowToolBoundary && action.Kind == domain.RootActionContinue {
		boundaryMessage, boundaryHandoff, err = prepareSupervisorToolBoundaryTx(ctx, tx, run, current)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	toolBoundary := boundaryHandoff != ""
	if !current.HasPendingInput() {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, apperror.New(apperror.CodeFailedPrecondition, "supervisor turn has no pending input")
	}
	userMessage, err := saveSupervisorOperatorInputTx(ctx, tx, run, current)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if toolBoundary && !approvalContinuation {
		if _, _, err := supersedeOperatorSteeringDeliveryTx(ctx, tx, run, checkpoint, completedAt); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	} else {
		if _, _, err := commitOperatorSteeringDeliveryTx(ctx, tx, run, checkpoint,
			userMessage, completedAt); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if _, err := commitMidTurnSteeringTx(ctx, tx, run, checkpoint, false); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := saveSupervisorToolContextTx(ctx, tx, run, checkpoint); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	assistantMessage, err := saveSessionMessageTx(ctx, tx, session.NewMessage(run.SessionID, "assistant", response.Text))
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	committedInboxMessages, err := commitRootInboxContextTx(ctx, tx, run, agentNode,
		checkpoint, time.Now().UTC())
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.PolicyDecisionEvent, "policy", checkpoint.AttemptID, map[string]any{
		"context": "supervisor_assistant_response", "allowed": decision.Allowed,
		"needs_approval": decision.NeedsApproval, "risk": decision.Risk, "reason": decision.Reason,
	}); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	completionPayload := map[string]any{
		"agent_id": agentNode.ID, "turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
		"user_message_id": userMessage.ID, "assistant_message_id": assistantMessage.ID,
		"provider": response.Provider, "model": response.Model, "usage": response.Usage,
		"lifecycle_action": action.Kind, "requested_lifecycle_action": requestedAction,
		"inbox_messages_committed": committedInboxMessages,
	}
	if privateModelAttempt > 0 {
		completionPayload["accepted_action_sha256"] = supervisorAcceptedActionSHA(acceptedAction)
	}
	if retainBackgroundJobs {
		completionPayload["background_jobs_retained"] = true
	}
	if toolBoundary {
		completionPayload["tool_round_boundary"] = true
		completionPayload["operator_message_id"] = boundaryMessage.ID
		completionPayload["handoff_operation_id"] = boundaryHandoff
		if approvalContinuation {
			completionPayload["approval_continuation_handoff_id"] = boundaryHandoff
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnCompletedEvent, "run_supervisor", checkpoint.AttemptID, completionPayload); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := insertSupervisorAssistantBindingTx(ctx, tx, checkpoint, assistantMessage, privateModelAttempt, privateReplayDigest); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if repairCompleted {
		if err := appendSupervisorEventTx(ctx, tx, run, events.ProtocolRepairCompletedEvent, "run_supervisor", checkpoint.AttemptID, map[string]any{
			"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID, "protocol_repair": 1,
		}); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if deferredSteering > 0 {
		if err := appendSupervisorEventTx(ctx, tx, run,
			events.OperatorSteeringActionDeferredEvent, "run_supervisor",
			checkpoint.AttemptID, map[string]any{
				"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
				"requested_action": requestedAction, "effective_action": action.Kind,
				"remaining_messages": deferredSteering,
			}); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	current.NextTurn++
	current.ExecutionMillis, err = supervisorAddCounter(current.ExecutionMillis, elapsedMillis, "execution time")
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	current.Phase = supervisorPhaseForAction(action.Kind)
	current.AttemptID = ""
	current.PendingInput = ""
	current.PendingImageCount = 0
	current.PendingAttachmentCount = 0
	current.RepairPhase = domain.ProtocolRepairNone
	current.RepairReason = ""
	current.LastError = ""
	current.UpdatedAt = completedAt
	switch action.Kind {
	case domain.RootActionFinish:
		if err := transitionSupervisorRunTx(ctx, tx, &run, domain.RunCompleted, action.Summary, current.UpdatedAt); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	case domain.RootActionWait:
		if !retainBackgroundJobs {
			if err := transitionSupervisorRunTx(ctx, tx, &run, domain.RunPaused, action.Reason, current.UpdatedAt); err != nil {
				return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
			}
		}
	}
	agentProjection := rootAgentProjection{
		Status: domain.AgentReady, TurnsUsed: int64(current.NextTurn - 1), TokensUsed: current.TotalTokens,
	}
	switch action.Kind {
	case domain.RootActionFinish:
		agentProjection.Status = domain.AgentCompleted
		agentProjection.StatusReason = action.Summary
	case domain.RootActionWait:
		agentProjection.Status = domain.AgentWaiting
		agentProjection.StatusReason = action.Reason
	}
	if _, _, agentChanged, err := syncRootAgentTx(ctx, tx, run, mission, agentProjection, current.UpdatedAt); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	} else if agentChanged {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if err := upsertSupervisorCheckpointTx(ctx, tx, current); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if toolBoundary {
		approvalHandoff := ""
		if approvalContinuation {
			approvalHandoff = boundaryHandoff
		}
		current, err = startSupervisorToolBoundaryTx(ctx, tx, run, mission, current, boundaryMessage, approvalHandoff)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorCheckpointedEvent, "run_supervisor", run.ID, map[string]any{
		"phase": current.Phase, "next_turn": current.NextTurn, "completed_turn": checkpoint.NextTurn,
		"input_tokens": current.InputTokens, "output_tokens": current.OutputTokens,
		"total_tokens": current.TotalTokens, "execution_millis": current.ExecutionMillis,
	}); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorActionEvent, "run_supervisor", run.ID, map[string]any{
		"action": action.Kind, "requested_action": requestedAction, "turn": checkpoint.NextTurn,
		"summary": action.Summary, "reason": action.Reason,
	}); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	if action.Kind == domain.RootActionFinish {
		if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorRunCompletedEvent, "run_supervisor", run.ID, map[string]any{
			"summary": action.Summary, "turns_completed": current.NextTurn - 1,
			"input_tokens": current.InputTokens, "output_tokens": current.OutputTokens,
			"total_tokens": current.TotalTokens, "execution_millis": current.ExecutionMillis,
		}); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if action.Kind == domain.RootActionWait {
		if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorRunWaitingEvent, "run_supervisor", run.ID, map[string]any{
			"reason": action.Reason, "next_turn": current.NextTurn,
		}); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, emptyMessages, err
	}
	return run, current, session.TurnMessages{User: userMessage, Assistant: assistantMessage}, nil
}

func requireNoActiveWorkItemsForFinishTx(ctx context.Context, tx *sql.Tx, runID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ? AND status = ?`, runID, domain.RunRunning)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeFailedPrecondition, "run stopped before work board completion check")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items
		WHERE run_id = ? AND status IN (?, ?, ?)`, runID,
		domain.WorkItemPending, domain.WorkItemInProgress, domain.WorkItemBlocked).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("root lifecycle finish conflicts with %d active work item(s)", active))
	}
	return requireDeliverySelectionCompletionTx(ctx, tx, runID)
}

func requireRunModeAllowsCompletionTx(ctx context.Context, tx *sql.Tx, runID string) error {
	mode, err := getCurrentRunModeSnapshot(ctx, tx, runID)
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"run mode snapshot is required before completion", err)
	}
	if mode.Phase == domain.ExecutionPhasePlan {
		return apperror.New(apperror.CodeFailedPrecondition,
			"plan-phase Run cannot be completed at the persistence boundary")
	}
	return nil
}

func requireLatestSupervisorModelCompletedTx(ctx context.Context, tx *sql.Tx, runID string, checkpoint domain.SupervisorCheckpoint) error {
	var eventType string
	if err := tx.QueryRowContext(ctx, `SELECT type FROM run_events
		WHERE run_id = ? AND source = ? AND subject_id LIKE ? AND type IN (?, ?, ?)
		AND COALESCE(json_extract(payload_json,'$.purpose'),'')=''
		ORDER BY sequence DESC LIMIT 1`, runID, "model_gateway", supervisorModelSubjectPrefix(checkpoint)+"%",
		events.ModelStartedEvent, events.ModelCompletedEvent, events.ModelFailedEvent).Scan(&eventType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperror.New(apperror.CodeFailedPrecondition, "supervisor turn has no completed model attempt")
		}
		return err
	}
	if eventType != events.ModelCompletedEvent {
		return apperror.New(apperror.CodeFailedPrecondition, "latest supervisor model attempt is not completed")
	}
	return nil
}

func (s *SQLiteStore) FailSupervisorTurn(ctx context.Context, checkpoint domain.SupervisorCheckpoint, cause string, elapsed time.Duration) (domain.SupervisorCheckpoint, error) {
	if err := checkpoint.Validate(); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	elapsedMillis, err := supervisorElapsedMillis(elapsed)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	cause = redact.String(strings.TrimSpace(cause))
	if cause == "" {
		cause = "supervisor turn failed"
	}
	runes := []rune(cause)
	if len(runes) > maxSupervisorErrorChars {
		cause = string(runes[:maxSupervisorErrorChars])
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, ok, err := getSupervisorCheckpointTx(ctx, tx, checkpoint.RunID)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if !ok {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "supervisor checkpoint was not found")
	}
	if err := requireSupervisorCheckpointLeaseTx(ctx, tx, checkpoint, current); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if current.Phase == domain.SupervisorTurnFailed && current.AttemptID == checkpoint.AttemptID {
		if err := tx.Commit(); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		return current, nil
	}
	if current.Phase != domain.SupervisorTurnStarted || current.NextTurn != checkpoint.NextTurn || current.AttemptID != checkpoint.AttemptID {
		return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict, "supervisor checkpoint changed before failure was recorded")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, checkpoint.RunID))
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile, workspace_id, scope_json,
		created_at, updated_at FROM missions WHERE id = ?`, run.MissionID))
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if run.Status == domain.RunRunning {
		agentNode, found, err := getRootAgentTx(ctx, tx, run.ID)
		if err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
		if !found {
			if _, _, _, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
				Status: domain.AgentRunning, ActiveAttemptID: checkpoint.AttemptID,
				TurnsUsed: int64(checkpoint.NextTurn - 1), TokensUsed: checkpoint.TotalTokens,
			}, checkpoint.UpdatedAt); err != nil {
				return domain.SupervisorCheckpoint{}, err
			}
		} else if agentNode.Status != domain.AgentRunning || agentNode.ActiveAttemptID != checkpoint.AttemptID {
			return domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict,
				"root agent is not bound to the failing supervisor attempt")
		}
	}
	repairAborted := current.RepairPhase == domain.ProtocolRepairPending
	repairReason := current.RepairReason
	current.Phase = domain.SupervisorTurnFailed
	current.RepairPhase = domain.ProtocolRepairNone
	current.RepairReason = ""
	current.LastError = cause
	current.ExecutionMillis, err = supervisorAddCounter(current.ExecutionMillis, elapsedMillis, "execution time")
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	current.UpdatedAt = time.Now().UTC()
	if err := upsertSupervisorCheckpointTx(ctx, tx, current); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	agentProjection, err := rootAgentProjectionForRunTx(ctx, tx, run)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	// A failed turn returns control to the operator. Pause the run so the
	// permission panel can expand network authority on this static boundary
	// and so queued steering cannot auto-wake a still-running failed run.
	// Same-lease steering retry remains valid while the lease is held; the
	// product resume paths (session chat / steering drain) already Resume
	// paused runs before the next operator turn.
	if run.Status == domain.RunRunning {
		const turnFailurePauseReason = "turn failed; awaiting operator input"
		agentProjection.Status = domain.AgentWaiting
		agentProjection.StatusReason = turnFailurePauseReason
		if err := transitionSupervisorRunTx(ctx, tx, &run, domain.RunPaused,
			turnFailurePauseReason, current.UpdatedAt); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	agentNode, _, agentChanged, err := syncRootAgentTx(ctx, tx, run, mission, agentProjection, current.UpdatedAt)
	if err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if agentChanged {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnFailedEvent, "run_supervisor", checkpoint.AttemptID, map[string]any{
		"agent_id": agentNode.ID, "turn": checkpoint.NextTurn,
		"attempt_id": checkpoint.AttemptID, "error": cause,
	}); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if repairAborted {
		if err := appendSupervisorEventTx(ctx, tx, run, events.ProtocolRepairFailedEvent, "run_supervisor", checkpoint.AttemptID, map[string]any{
			"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID, "protocol_repair": 1,
			"reason": cause, "requested_reason": repairReason, "stage": "aborted",
		}); err != nil {
			return domain.SupervisorCheckpoint{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorCheckpointedEvent, "run_supervisor", run.ID, map[string]any{
		"phase": current.Phase, "next_turn": current.NextTurn, "attempt_id": current.AttemptID,
		"execution_millis": current.ExecutionMillis,
	}); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	return current, nil
}

func (s *SQLiteStore) FinalizeSupervisorRun(ctx context.Context, lease domain.RunExecutionLease,
	target domain.RunStatus, summary string,
) (domain.Run, domain.SupervisorCheckpoint, error) {
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"active run execution lease is required", err)
	}
	if target != domain.RunCompleted && target != domain.RunFailed {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeInvalidArgument, "supervisor final status must be completed or failed")
	}
	summary = redact.String(strings.TrimSpace(summary))
	if summary == "" {
		if target == domain.RunCompleted {
			summary = "run completed"
		} else {
			summary = "run failed"
		}
	}
	runes := []rune(summary)
	if len(runes) > maxSupervisorErrorChars {
		summary = string(runes[:maxSupervisorErrorChars])
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireRunExecutionLeaseTx(ctx, tx, lease.RunID, lease.LeaseID, lease.Generation); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, lease.RunID))
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	checkpoint, ok, err := getSupervisorCheckpointTx(ctx, tx, run.ID)
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	if !ok {
		checkpoint = domain.SupervisorCheckpoint{
			RunID: run.ID, LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation,
			NextTurn: 1, Phase: domain.SupervisorIdle, UpdatedAt: time.Now().UTC(),
		}
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile, workspace_id, scope_json,
		created_at, updated_at FROM missions WHERE id = ?`, run.MissionID))
	if err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	if run.Status == target {
		projection, err := rootAgentProjectionForRunTx(ctx, tx, run)
		if err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		}
		projection.StatusReason = summary
		if _, _, changed, err := syncRootAgentTx(ctx, tx, run, mission, projection, time.Now().UTC()); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		} else if changed {
			if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
				return domain.Run{}, domain.SupervisorCheckpoint{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		}
		return run, checkpoint, nil
	}
	if run.Terminal() {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeConflict,
			fmt.Sprintf("run %s is already terminal as %s", run.ID, run.Status))
	}
	if target == domain.RunCompleted {
		if err := requireNoPendingOperatorSteeringTx(ctx, tx, run.ID); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		}
		if err := requireRunModeAllowsCompletionTx(ctx, tx, run.ID); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		}
		if err := requireNoActiveWorkItemsForFinishTx(ctx, tx, run.ID); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		}
	}
	if checkpoint.Phase == domain.SupervisorTurnStarted {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "cannot finalize while a supervisor turn is started")
	}
	if target == domain.RunCompleted && checkpoint.Phase != domain.SupervisorIdle {
		return domain.Run{}, domain.SupervisorCheckpoint{}, apperror.New(apperror.CodeFailedPrecondition, "only an idle supervisor can complete a run")
	}
	checkpoint.LeaseID = lease.LeaseID
	checkpoint.LeaseGeneration = lease.Generation
	if err := transitionSupervisorRunTx(ctx, tx, &run, target, summary, time.Now().UTC()); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	checkpoint.AttemptID = ""
	checkpoint.PendingInput = ""
	checkpoint.PendingImageCount = 0
	checkpoint.PendingAttachmentCount = 0
	checkpoint.RepairPhase = domain.ProtocolRepairNone
	checkpoint.RepairReason = ""
	checkpoint.UpdatedAt = run.UpdatedAt
	if target == domain.RunCompleted {
		checkpoint.Phase = domain.SupervisorRunCompleted
		checkpoint.LastError = ""
	} else {
		checkpoint.Phase = domain.SupervisorRunFailed
		checkpoint.LastError = summary
	}
	if err := upsertSupervisorCheckpointTx(ctx, tx, checkpoint); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	agentStatus := domain.AgentCompleted
	if target == domain.RunFailed {
		agentStatus = domain.AgentFailed
	}
	if _, _, agentChanged, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
		Status: agentStatus, StatusReason: summary, TurnsUsed: int64(checkpoint.NextTurn - 1),
		TokensUsed: checkpoint.TotalTokens,
	}, checkpoint.UpdatedAt); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	} else if agentChanged {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.Run{}, domain.SupervisorCheckpoint{}, err
		}
	}
	eventType := events.SupervisorRunCompletedEvent
	if target == domain.RunFailed {
		eventType = events.SupervisorRunFailedEvent
	}
	if err := appendSupervisorEventTx(ctx, tx, run, eventType, "run_supervisor", run.ID, map[string]any{
		"summary": summary, "turns_completed": checkpoint.NextTurn - 1,
		"input_tokens": checkpoint.InputTokens, "output_tokens": checkpoint.OutputTokens,
		"total_tokens": checkpoint.TotalTokens, "execution_millis": checkpoint.ExecutionMillis,
	}); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, domain.SupervisorCheckpoint{}, err
	}
	return run, checkpoint, nil
}

func getSupervisorCheckpointTx(ctx context.Context, tx *sql.Tx, runID string) (domain.SupervisorCheckpoint, bool, error) {
	checkpoint, err := scanSupervisorCheckpoint(tx.QueryRowContext(ctx, supervisorCheckpointSelect, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SupervisorCheckpoint{}, false, nil
	}
	if err != nil {
		return domain.SupervisorCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

func requireSupervisorCheckpointLeaseTx(ctx context.Context, tx *sql.Tx, expected domain.SupervisorCheckpoint,
	current domain.SupervisorCheckpoint,
) error {
	if expected.LeaseID == "" || expected.LeaseGeneration <= 0 ||
		current.LeaseID != expected.LeaseID || current.LeaseGeneration != expected.LeaseGeneration {
		return apperror.New(apperror.CodeConflict,
			"supervisor checkpoint execution lease fencing token changed")
	}
	return requireRunExecutionLeaseTx(ctx, tx, expected.RunID, expected.LeaseID, expected.LeaseGeneration)
}

func upsertSupervisorCheckpointTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint) error {
	if checkpoint.Phase != domain.SupervisorTurnStarted && checkpoint.Phase != domain.SupervisorTurnFailed {
		checkpoint.PendingImageCount = 0
		checkpoint.PendingAttachmentCount = 0
	}
	checkpoint.LastError = redact.String(checkpoint.LastError)
	checkpoint.PendingInput = redact.String(checkpoint.PendingInput)
	checkpoint.RepairReason = redact.String(checkpoint.RepairReason)
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_supervisor_checkpoints
		(run_id, lease_id, lease_generation, next_turn, phase, attempt_id, last_error, pending_input, repair_phase,
		repair_reason, input_tokens, output_tokens, total_tokens, execution_millis, updated_at,pending_image_count,pending_attachment_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET lease_id=excluded.lease_id,
		lease_generation=excluded.lease_generation, next_turn=excluded.next_turn, phase=excluded.phase,
		attempt_id=excluded.attempt_id, last_error=excluded.last_error, pending_input=excluded.pending_input,
		repair_phase=excluded.repair_phase, repair_reason=excluded.repair_reason,
		input_tokens=excluded.input_tokens,
		output_tokens=excluded.output_tokens, total_tokens=excluded.total_tokens,
		execution_millis=excluded.execution_millis, updated_at=excluded.updated_at,pending_image_count=excluded.pending_image_count,pending_attachment_count=excluded.pending_attachment_count`,
		checkpoint.RunID, checkpoint.LeaseID, checkpoint.LeaseGeneration,
		checkpoint.NextTurn, checkpoint.Phase, checkpoint.AttemptID,
		checkpoint.LastError, checkpoint.PendingInput, checkpoint.RepairPhase, checkpoint.RepairReason,
		checkpoint.InputTokens, checkpoint.OutputTokens, checkpoint.TotalTokens,
		checkpoint.ExecutionMillis, ts(checkpoint.UpdatedAt), checkpoint.PendingImageCount, checkpoint.PendingAttachmentCount)
	return err
}

func appendSupervisorEventTx(ctx context.Context, tx *sql.Tx, run domain.Run, eventType string, source string, subjectID string, payload any) error {
	event, err := events.New(run.ID, run.MissionID, eventType, source, subjectID, payload)
	if err != nil {
		return err
	}
	_, err = insertRunEventTx(ctx, tx, event)
	return err
}

func scanSupervisorCheckpoint(row scanner) (domain.SupervisorCheckpoint, error) {
	var checkpoint domain.SupervisorCheckpoint
	var phase string
	var attempt sql.NullString
	var lastError sql.NullString
	var repairPhase string
	var updated string
	if err := row.Scan(&checkpoint.RunID, &checkpoint.LeaseID, &checkpoint.LeaseGeneration,
		&checkpoint.NextTurn, &phase, &attempt, &lastError, &checkpoint.PendingInput,
		&repairPhase, &checkpoint.RepairReason,
		&checkpoint.InputTokens, &checkpoint.OutputTokens, &checkpoint.TotalTokens,
		&checkpoint.ExecutionMillis, &updated, &checkpoint.PendingImageCount, &checkpoint.PendingAttachmentCount); err != nil {
		return domain.SupervisorCheckpoint{}, err
	}
	checkpoint.Phase = domain.SupervisorPhase(phase)
	checkpoint.AttemptID = attempt.String
	checkpoint.LastError = lastError.String
	checkpoint.RepairPhase = domain.ProtocolRepairPhase(repairPhase)
	checkpoint.UpdatedAt = parseTS(updated)
	return checkpoint, checkpoint.Validate()
}

func supervisorUsage(usage llm.Usage) (int64, int64, int64, error) {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return 0, 0, 0, apperror.New(apperror.CodeFailedPrecondition, "provider returned invalid token usage")
	}
	input := int64(usage.InputTokens)
	output := int64(usage.OutputTokens)
	total := int64(usage.TotalTokens)
	combined, err := supervisorAddCounter(input, output, "provider token usage")
	if err != nil {
		return 0, 0, 0, err
	}
	if total < combined {
		total = combined
	}
	normalized := usage
	normalized.TotalTokens = int(total)
	if normalized.Validate() != nil {
		return 0, 0, 0, apperror.New(apperror.CodeFailedPrecondition, "provider returned invalid cache token usage")
	}
	return input, output, total, nil
}

func normalizeSupervisorInput(input string) (string, error) {
	input = redact.String(strings.TrimSpace(input))
	if len([]rune(input)) > maxSupervisorInputChars {
		return "", apperror.New(apperror.CodeResourceExhausted, "supervisor input exceeds 65536 characters")
	}
	return input, nil
}

func sanitizeRootAction(action domain.RootAction) domain.RootAction {
	action.Version = strings.TrimSpace(action.Version)
	action.Kind = domain.RootActionKind(strings.TrimSpace(string(action.Kind)))
	action.Message = redact.String(strings.TrimSpace(action.Message))
	action.Summary = redact.String(strings.TrimSpace(action.Summary))
	action.Reason = redact.String(strings.TrimSpace(action.Reason))
	return action
}

func supervisorPhaseForAction(kind domain.RootActionKind) domain.SupervisorPhase {
	switch kind {
	case domain.RootActionFinish:
		return domain.SupervisorRunCompleted
	case domain.RootActionWait:
		return domain.SupervisorWaiting
	case domain.RootActionContinue:
		return domain.SupervisorIdle
	default:
		return domain.SupervisorTurnFailed
	}
}

func transitionSupervisorRunTx(ctx context.Context, tx *sql.Tx, run *domain.Run, target domain.RunStatus, reason string, at time.Time) error {
	if run == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "supervisor run is required")
	}
	expected := run.Status
	if target == domain.RunCompleted {
		if err := requireNoPendingOperatorSteeringTx(ctx, tx, run.ID); err != nil {
			return err
		}
	}
	if err := run.Transition(target, at); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition, err.Error(), err)
	}
	configJSON, err := marshalRedactedJSON(run.Config)
	if err != nil {
		return err
	}
	budgetJSON, err := marshalRedactedJSON(run.Budget)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, config_json = ?, budget_json = ?,
		started_at = ?, finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		run.Status, configJSON, budgetJSON, nullableTS(run.StartedAt), nullableTS(run.FinishedAt),
		ts(run.UpdatedAt), run.ID, expected)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeConflict, "run changed before supervisor lifecycle transition")
	}
	if target == domain.RunFailed || target == domain.RunCancelled {
		if _, err := cancelOperatorSteeringTx(ctx, tx, *run, "run_supervisor", reason, at); err != nil {
			return err
		}
	}
	statusEvent, err := events.New(run.ID, run.MissionID, events.RunStatusChangedEvent, "run_supervisor", run.ID, map[string]any{
		"from": expected, "to": target, "reason": redact.String(strings.TrimSpace(reason)),
	})
	if err != nil {
		return err
	}
	_, err = insertRunEventTx(ctx, tx, statusEvent)
	if err != nil {
		return err
	}
	return abortSpecialistDelegationApplicationsTx(ctx, tx, *run, at)
}

func supervisorAddCounter(current, delta int64, name string) (int64, error) {
	if current < 0 || delta < 0 {
		return 0, apperror.New(apperror.CodeFailedPrecondition, name+" counter cannot be negative")
	}
	if delta > (1<<63-1)-current {
		return 0, apperror.New(apperror.CodeResourceExhausted, name+" counter overflow")
	}
	return current + delta, nil
}

func supervisorElapsedMillis(elapsed time.Duration) (int64, error) {
	if elapsed < 0 {
		return 0, apperror.New(apperror.CodeInvalidArgument, "supervisor elapsed time cannot be negative")
	}
	millis := elapsed.Milliseconds()
	if elapsed > 0 && millis == 0 {
		millis = 1
	}
	return millis, nil
}

func supervisorModelElapsedMillis(elapsed time.Duration) (int64, error) {
	millis, err := supervisorElapsedMillis(elapsed)
	if err != nil {
		return 0, err
	}
	if millis == 0 {
		millis = 1
	}
	return millis, nil
}
