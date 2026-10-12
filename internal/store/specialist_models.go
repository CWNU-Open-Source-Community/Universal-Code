package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const (
	maxSpecialistModelTransportAttempts = 5
	maxSpecialistModelInputBytes        = 32 * 1024
)

const specialistModelCallSelect = `SELECT agent_attempt_id, run_id, agent_id,
	model_attempt_number, transport_attempt, max_attempts, protocol_repair, tool_round, provider, model, status, outcome,
	input_fingerprint, action_fingerprint,
	error_text, retry_after_millis, retry_planned, elapsed_millis, stream_events, stream_bytes,
	input_tokens, output_tokens, total_tokens, usage_recorded, action_kind, report_outcome,
	policy_allowed, policy_needs_approval, policy_risk, policy_reason,
	user_message_id, assistant_message_id, started_at, finished_at
	FROM specialist_model_calls`

type specialistModelCallRecord struct {
	AgentAttemptID      string
	RunID               string
	AgentID             string
	ModelAttempt        int
	TransportAttempt    int
	MaxAttempts         int
	ProtocolRepair      int
	ToolRound           int
	Provider            string
	Model               string
	InputFingerprint    string
	ActionFingerprint   string
	Status              string
	Outcome             llm.Outcome
	ErrorText           string
	RetryAfterMillis    int64
	RetryPlanned        bool
	ElapsedMillis       int64
	StreamEvents        int
	StreamBytes         int
	Usage               domain.AgentAttemptUsage
	UsageRecorded       bool
	ActionKind          domain.SpecialistActionKind
	ReportOutcome       domain.CompletionOutcome
	PolicyAllowed       int
	PolicyNeedsApproval bool
	PolicyRisk          string
	PolicyReason        string
	UserMessageID       *int64
	AssistantMessageID  *int64
	StartedAt           time.Time
	FinishedAt          *time.Time
}

type specialistModelTerminal struct {
	notDispatched      bool
	status             string
	usage              *llm.Usage
	action             *domain.SpecialistAction
	decision           *policy.Decision
	input              string
	userMessageID      *int64
	assistantMessageID *int64
	inputFingerprint   string
	actionFingerprint  string
	repairStatus       domain.SpecialistProtocolRepairStatus
	repairReason       string
	toolResponse       *llm.ChatResponse
}

// NextSpecialistModelAttempt returns the next global model sequence, the next
// phase-local transport sequence, and the child's cumulative model time.
func (s *SQLiteStore) NextSpecialistModelAttempt(ctx context.Context,
	ref domain.AgentAttemptRef, protocolRepair int,
) (int, int, int64, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return 0, 0, 0, apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist model attempt reference is invalid", err)
	}
	if protocolRepair < 0 || protocolRepair > 1 {
		return 0, 0, 0, apperror.New(apperror.CodeInvalidArgument,
			"Specialist protocol repair number must be zero or one")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, _, _, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return 0, 0, 0, err
	}
	repair, repairFound, err := getSpecialistProtocolRepairTx(ctx, tx, attempt.ID)
	if err != nil {
		return 0, 0, 0, err
	}
	toolMode, err := specialistToolsEnabledTx(ctx, tx, attempt.ID)
	if err != nil {
		return 0, 0, 0, err
	}
	toolRound := 0
	if toolMode {
		toolRound, err = specialistToolRoundTx(ctx, tx, attempt.ID)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	if err = requireChildToolModeTx(ctx, tx, attempt.ID, toolRound); err != nil {
		return 0, 0, 0, err
	}
	if protocolRepair == 0 && specialistToolModePrimaryCommitted(attempt.UsageRecordedAt != nil, toolMode, repairFound) {
		return 0, 0, 0, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist primary model phase was already committed")
	}
	if protocolRepair == 1 && (!repairFound || repair.Status != domain.SpecialistRepairPending ||
		attempt.UsageRecordedAt == nil) {
		return 0, 0, 0, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist protocol repair is not pending")
	}
	var count, phaseCount, started int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN protocol_repair = ? AND tool_round = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'started' THEN 1 ELSE 0 END), 0)
		FROM specialist_model_calls WHERE agent_attempt_id = ?`, protocolRepair, toolRound, attempt.ID).
		Scan(&count, &phaseCount, &started); err != nil {
		return 0, 0, 0, err
	}
	if started != 0 {
		return 0, 0, 0, apperror.New(apperror.CodeConflict,
			"Specialist already has an active model call")
	}
	var executionMillis int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(elapsed_millis), 0)
		FROM specialist_model_calls WHERE agent_id = ?`, attempt.AgentID).
		Scan(&executionMillis); err != nil {
		return 0, 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return count + 1, phaseCount + 1, executionMillis, nil
}

func (s *SQLiteStore) RecordSpecialistModelStarted(ctx context.Context,
	ref domain.AgentAttemptRef, modelAttempt llm.ModelAttempt,
) (bool, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return false, apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist model attempt reference is invalid", err)
	}
	modelAttempt = sanitizeModelAttempt(modelAttempt)
	if modelAttempt.SpecialistAttemptID != "" && (modelAttempt.SpecialistAttemptID != ref.AttemptID || modelAttempt.MonetaryAttemptNumber() == 0) {
		return false, apperror.New(apperror.CodeInvalidArgument, "Specialist monetary identity differs from its Agent attempt")
	}
	if modelAttempt.Outcome != "" || modelAttempt.ErrorText != "" || modelAttempt.RetryAfter != 0 ||
		modelAttempt.Elapsed != 0 || modelAttempt.RetryPlanned || modelAttempt.StreamEvents != 0 ||
		modelAttempt.StreamBytes != 0 {
		return false, apperror.New(apperror.CodeInvalidArgument,
			"started Specialist model call cannot contain terminal metadata")
	}
	if err := validateSpecialistModelIdentity(modelAttempt); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return false, err
	}
	existing, found, err := getSpecialistModelCallTx(ctx, tx, attempt.ID, modelAttempt.Number)
	if err != nil {
		return false, err
	}
	if err := requireSpecialistBriefModelStartTx(ctx, tx, attempt, modelAttempt); err != nil {
		return false, err
	}
	if found {
		if err := requireSpecialistMonetaryStartTx(ctx, tx, ref.RunID, ref.AttemptID, modelAttempt); err != nil {
			return false, err
		}
		if err := requireSpecialistModelIdentity(existing, modelAttempt); err != nil {
			return false, err
		}
		if err := commitSpecialistSkillContextTx(ctx, tx, run, attempt, child,
			modelAttempt.Number); err != nil {
			return false, err
		}
		if err := commitExternalSpecialistSkillContextTx(ctx, tx, run, attempt, child,
			modelAttempt.Number); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	repair, repairFound, err := getSpecialistProtocolRepairTx(ctx, tx, attempt.ID)
	if err != nil {
		return false, err
	}
	toolMode, err := specialistToolsEnabledTx(ctx, tx, attempt.ID)
	if err != nil {
		return false, err
	}
	if err = requireChildToolModeTx(ctx, tx, attempt.ID, modelAttempt.ToolRound); err != nil {
		return false, err
	}
	if modelAttempt.ProtocolRepair == 0 && specialistToolModePrimaryCommitted(attempt.UsageRecordedAt != nil, toolMode, repairFound) {
		return false, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist primary model phase was already committed")
	}
	if modelAttempt.ProtocolRepair == 1 && (!repairFound ||
		repair.Status != domain.SpecialistRepairPending || attempt.UsageRecordedAt == nil) {
		return false, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist protocol repair is not pending")
	}
	var count, phaseCount, active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN protocol_repair = ? AND tool_round = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'started' THEN 1 ELSE 0 END), 0)
		FROM specialist_model_calls WHERE agent_attempt_id = ?`, modelAttempt.ProtocolRepair, modelAttempt.ToolRound, attempt.ID).
		Scan(&count, &phaseCount, &active); err != nil {
		return false, err
	}
	if active != 0 || modelAttempt.Number != count+1 ||
		modelAttempt.TransportNumber() != phaseCount+1 {
		return false, apperror.New(apperror.CodeConflict,
			"Specialist model attempt is not the next durable phase call")
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_model_calls
		(agent_attempt_id, run_id, agent_id, model_attempt_number, transport_attempt,
		max_attempts, protocol_repair, tool_round, provider, model, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'started', ?)`, attempt.ID, attempt.RunID,
		attempt.AgentID, modelAttempt.Number, modelAttempt.TransportNumber(), modelAttempt.MaxAttempts,
		modelAttempt.ProtocolRepair, modelAttempt.ToolRound, modelAttempt.Provider, modelAttempt.Model, ts(now)); err != nil {
		return false, err
	}
	if err := commitSpecialistSkillContextTx(ctx, tx, run, attempt, child,
		modelAttempt.Number); err != nil {
		return false, err
	}
	if err := commitExternalSpecialistSkillContextTx(ctx, tx, run, attempt, child,
		modelAttempt.Number); err != nil {
		return false, err
	}
	if err := resolveSupersededSpecialistModelCancellationsTx(ctx, tx,
		domain.AgentAttemptRef{RunID: attempt.RunID, AgentID: attempt.AgentID,
			AttemptID: attempt.ID}, modelAttempt.Number, now); err != nil {
		return false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.ModelStartedEvent,
		"specialist_model_gateway", specialistModelSubject(attempt.ID, modelAttempt.Number), map[string]any{
			"agent_id": child.ID, "parent_agent_id": child.ParentID, "agent_attempt_id": attempt.ID,
			"turn": attempt.Turn, "model_attempt": modelAttempt.Number,
			"transport_attempt": modelAttempt.TransportNumber(), "max_attempts": modelAttempt.MaxAttempts,
			"protocol_repair": modelAttempt.ProtocolRepair,
			"provider":        modelAttempt.Provider, "model": modelAttempt.Model, "context": modelAttempt.Context,
			"monetary_attempt_number": specialistMonetaryPayloadNumber(modelAttempt),
		}); err != nil {
		return false, err
	}
	if modelAttempt.ProtocolRepair == 1 && modelAttempt.TransportNumber() == 1 {
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentProtocolRepairStartedEvent,
			"specialist_runner", attempt.ID, map[string]any{
				"agent_id": attempt.AgentID, "agent_attempt_id": attempt.ID,
				"turn": attempt.Turn, "protocol_repair": 1,
				"model_attempt": modelAttempt.Number,
			}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) RecordSpecialistModelCompleted(ctx context.Context,
	ref domain.AgentAttemptRef, modelAttempt llm.ModelAttempt, response llm.ChatResponse,
	input string, action domain.SpecialistAction, decision policy.Decision,
) (domain.AgentAttempt, error) {
	modelAttempt = sanitizeModelAttempt(modelAttempt)
	modelAttempt.Outcome = llm.OutcomeSuccess
	modelAttempt.ErrorText = ""
	modelAttempt.RetryAfter = 0
	modelAttempt.RetryPlanned = false
	if err := validateSpecialistModelIdentity(modelAttempt); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := modelAttempt.ValidateCompleted(); err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"completed Specialist model call is invalid", err)
	}
	if len(response.ToolCalls) != 0 {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist no-tool turn cannot complete with tool calls")
	}
	decoded, err := domain.DecodeSpecialistAction(response.Text)
	if err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Specialist response does not match its lifecycle action", err)
	}
	normalized, err := domain.NormalizeSpecialistAction(action)
	if err != nil || !sameSpecialistAction(decoded, normalized) {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeConflict,
			"Specialist lifecycle action differs from the provider response")
	}
	safeAction, err := redactSpecialistAction(normalized)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	decision, err = normalizeSpecialistPolicyDecision(decision)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	input = redact.String(strings.TrimSpace(input))
	if input == "" || !utf8.ValidString(input) || len([]byte(input)) > maxSpecialistModelInputBytes {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument,
			"Specialist model input is empty, invalid, or too large")
	}
	if err := response.Usage.Validate(); err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Specialist provider returned invalid usage", err)
	}
	actionJSON, err := json.Marshal(safeAction)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	repairStatus := domain.SpecialistProtocolRepairStatus("")
	if modelAttempt.ProtocolRepair == 1 {
		repairStatus = domain.SpecialistRepairCompleted
	}
	return s.recordSpecialistModelTerminal(ctx, ref, modelAttempt, specialistModelTerminal{
		status: "completed", usage: &response.Usage, action: &safeAction,
		decision:          &decision,
		input:             input,
		inputFingerprint:  runmutation.Fingerprint("specialist_model_input.v1", input),
		actionFingerprint: runmutation.Fingerprint("specialist_model_action.v1", string(actionJSON)),
		repairStatus:      repairStatus,
	})
}

func (s *SQLiteStore) RecordSpecialistProtocolFailure(ctx context.Context,
	ref domain.AgentAttemptRef, modelAttempt llm.ModelAttempt, usage llm.Usage,
	reason string, requestRepair bool,
) (domain.AgentAttempt, error) {
	modelAttempt = sanitizeModelAttempt(modelAttempt)
	modelAttempt.Outcome = llm.OutcomeInvalidResponse
	modelAttempt.ErrorText = normalizeSpecialistRepairReason(reason)
	modelAttempt.RetryAfter = 0
	modelAttempt.RetryPlanned = false
	if err := validateSpecialistModelIdentity(modelAttempt); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := modelAttempt.ValidateFailed(); err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist protocol failure model call is invalid", err)
	}
	if err := usage.Validate(); err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Specialist protocol failure usage is invalid", err)
	}
	repairStatus := domain.SpecialistRepairExhausted
	if requestRepair {
		if modelAttempt.ProtocolRepair != 0 {
			return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument,
				"only a primary Specialist response can request protocol repair")
		}
		repairStatus = domain.SpecialistRepairPending
	} else if modelAttempt.ProtocolRepair != 1 {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument,
			"only a Specialist repair response can exhaust protocol repair")
	}
	return s.recordSpecialistModelTerminal(ctx, ref, modelAttempt, specialistModelTerminal{
		status: "failed", usage: &usage, repairStatus: repairStatus,
		repairReason: modelAttempt.ErrorText,
	})
}

func (s *SQLiteStore) RecordSpecialistModelFailed(ctx context.Context,
	ref domain.AgentAttemptRef, modelAttempt llm.ModelAttempt, usage *llm.Usage,
) (domain.AgentAttempt, error) {
	modelAttempt = sanitizeModelAttempt(modelAttempt)
	if err := validateSpecialistModelIdentity(modelAttempt); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := modelAttempt.ValidateFailed(); err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"failed Specialist model call is invalid", err)
	}
	if modelAttempt.RetryPlanned && (!modelAttempt.Outcome.Retryable() ||
		modelAttempt.TransportNumber() >= modelAttempt.MaxAttempts || usage != nil) {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument,
			"Specialist retry plan conflicts with its terminal outcome")
	}
	if usage != nil {
		copy := *usage
		if err := copy.Validate(); err != nil {
			return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"Specialist provider returned invalid usage", err)
		}
		usage = &copy
	}
	if usage != nil && modelAttempt.Outcome == llm.OutcomeInvalidResponse {
		var modeCount int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_bindings WHERE agent_attempt_id=?`, ref.AttemptID).Scan(&modeCount); err != nil || modeCount != 1 {
			return domain.AgentAttempt{}, apperror.New(apperror.CodeFailedPrecondition, "usage-bearing Specialist protocol failures require the repair ledger")
		}
	}
	return s.recordSpecialistModelTerminal(ctx, ref, modelAttempt, specialistModelTerminal{
		status: "failed", usage: usage,
	})
}

func (s *SQLiteStore) RecordSpecialistModelNotDispatched(ctx context.Context, ref domain.AgentAttemptRef,
	modelAttempt llm.ModelAttempt,
) (domain.AgentAttempt, error) {
	modelAttempt = sanitizeModelAttempt(modelAttempt)
	if err := validateSpecialistModelIdentity(modelAttempt); err != nil {
		return domain.AgentAttempt{}, err
	}
	if modelAttempt.ValidateFailed() != nil || modelAttempt.Outcome != llm.OutcomePermanent ||
		modelAttempt.SpecialistAttemptID == "" || modelAttempt.RetryPlanned || modelAttempt.RetryAfter != 0 ||
		modelAttempt.StreamEvents != 0 || modelAttempt.StreamBytes != 0 {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument, "invalid not-dispatched Specialist accounting receipt")
	}
	return s.recordSpecialistModelTerminal(ctx, ref, modelAttempt, specialistModelTerminal{
		status: "failed", usage: &llm.Usage{}, notDispatched: true,
	})
}

func specialistMonetaryPayloadNumber(attempt llm.ModelAttempt) int64 {
	if attempt.SpecialistAttemptID == "" {
		return 0
	}
	return attempt.MonetaryAttemptNumber()
}

func (s *SQLiteStore) recordSpecialistModelTerminal(ctx context.Context,
	ref domain.AgentAttemptRef, modelAttempt llm.ModelAttempt,
	terminal specialistModelTerminal,
) (domain.AgentAttempt, error) {
	ref = normalizeAgentAttemptRef(ref)
	if modelAttempt.SpecialistAttemptID != "" && (modelAttempt.SpecialistAttemptID != ref.AttemptID || modelAttempt.MonetaryAttemptNumber() == 0) {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument, "Specialist monetary identity differs from its Agent attempt")
	}
	if err := ref.Validate(); err != nil {
		return domain.AgentAttempt{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist model attempt reference is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	currentAttempt, err := scanAgentAttempt(tx.QueryRowContext(ctx,
		agentAttemptSelect+` WHERE id = ?`, ref.AttemptID))
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if currentAttempt.RunID != ref.RunID || currentAttempt.AgentID != ref.AgentID {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeInvalidArgument,
			"Specialist model attempt identity does not match its Agent attempt")
	}
	call, found, err := getSpecialistModelCallTx(ctx, tx, currentAttempt.ID, modelAttempt.Number)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if !found {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist model call was not started")
	}
	if err := requireSpecialistModelIdentity(call, modelAttempt); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := requireSpecialistMonetaryStartTx(ctx, tx, ref.RunID, ref.AttemptID, modelAttempt); err != nil {
		return domain.AgentAttempt{}, err
	}
	if modelAttempt.SpecialistAttemptID != "" && terminal.input != "" {
		if err := validateSpecialistBriefCompletedInputTx(ctx, tx, currentAttempt, terminal.input); err != nil {
			return domain.AgentAttempt{}, err
		}
	}
	if call.Status != "started" {
		var dispatch string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload_json,'$.dispatch'),'') FROM run_events
			WHERE run_id=? AND source='specialist_model_gateway' AND type IN ('model.completed','model.failed') AND subject_id=?`,
			ref.RunID, specialistModelSubject(ref.AttemptID, modelAttempt.Number)).Scan(&dispatch); err != nil {
			return domain.AgentAttempt{}, err
		}
		if (dispatch == "not_sent") != terminal.notDispatched {
			return domain.AgentAttempt{}, apperror.New(apperror.CodeConflict, "Specialist dispatch receipt replay differs from its durable record")
		}
		if err := requireSpecialistTerminalReplay(call, modelAttempt, terminal); err != nil {
			return domain.AgentAttempt{}, err
		}
		if err := requireSpecialistRepairTerminalReplayTx(ctx, tx, currentAttempt.ID,
			modelAttempt.Number, terminal); err != nil {
			return domain.AgentAttempt{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.AgentAttempt{}, err
		}
		return currentAttempt, nil
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	elapsedMillis, err := supervisorElapsedMillis(modelAttempt.Elapsed)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	var callUsage domain.AgentAttemptUsage
	if terminal.usage != nil {
		inputTokens, outputTokens, totalTokens, err := supervisorUsage(*terminal.usage)
		if err != nil {
			return domain.AgentAttempt{}, err
		}
		callUsage = domain.AgentAttemptUsage{
			InputTokens: inputTokens, OutputTokens: outputTokens,
			TotalTokens: totalTokens, ExecutionMillis: elapsedMillis,
		}
	}
	now := time.Now().UTC()
	attempt, child, usageChanged, err := applySpecialistModelUsageTx(ctx, tx, attempt,
		child, run, modelAttempt.Number, terminal.usage, elapsedMillis, now)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	var actionKind, reportOutcome string
	policyAllowed := -1
	policyNeedsApproval := false
	policyRisk, policyReason := "", ""
	if (terminal.action != nil || terminal.toolResponse != nil) && terminal.decision != nil {
		assistantContent := ""
		if terminal.toolResponse != nil {
			actionKind = "tool_calls"
			raw, encodeErr := json.Marshal(llm.Message{Role: "assistant", Content: terminal.toolResponse.Text, ToolCalls: terminal.toolResponse.ToolCalls})
			if encodeErr != nil {
				return domain.AgentAttempt{}, encodeErr
			}
			assistantContent = string(raw)
		} else {
			actionKind = string(terminal.action.Kind)
			assistantContent = terminal.action.Message
		}
		if terminal.action != nil && terminal.action.Report != nil {
			reportOutcome = string(terminal.action.Report.Outcome)
		}
		policyAllowed = 0
		if terminal.decision.Allowed {
			policyAllowed = 1
		}
		policyNeedsApproval = terminal.decision.NeedsApproval
		policyRisk = terminal.decision.Risk
		policyReason = terminal.decision.Reason
		if terminal.decision.Allowed && !terminal.decision.NeedsApproval {
			if child.SessionID == "" {
				return domain.AgentAttempt{}, apperror.New(apperror.CodeFailedPrecondition,
					"Specialist has no durable Session")
			}
			var sessionStatus string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id = ?`,
				child.SessionID).Scan(&sessionStatus); err != nil {
				return domain.AgentAttempt{}, err
			}
			if sessionStatus != session.StatusActive {
				return domain.AgentAttempt{}, apperror.New(apperror.CodeConflict,
					"Specialist Session is not active")
			}
			userMessage, err := saveSessionMessageTx(ctx, tx, session.Message{
				SessionID: child.SessionID, Role: "user", Content: terminal.input, CreatedAt: now,
			})
			if err != nil {
				return domain.AgentAttempt{}, err
			}
			assistantMessage, err := saveSessionMessageTx(ctx, tx, session.Message{
				SessionID: child.SessionID, Role: "assistant", Content: assistantContent,
				CreatedAt: now,
			})
			if err != nil {
				return domain.AgentAttempt{}, err
			}
			terminal.userMessageID = &userMessage.ID
			terminal.assistantMessageID = &assistantMessage.ID
		}
	}
	usageRecorded := terminal.usage != nil
	result, err := tx.ExecContext(ctx, `UPDATE specialist_model_calls SET status = ?, outcome = ?,
		input_fingerprint = ?, action_fingerprint = ?, error_text = ?, retry_after_millis = ?,
		retry_planned = ?, elapsed_millis = ?,
		stream_events = ?, stream_bytes = ?, input_tokens = ?, output_tokens = ?, total_tokens = ?,
		usage_recorded = ?, action_kind = ?, report_outcome = ?, policy_allowed = ?,
		policy_needs_approval = ?, policy_risk = ?, policy_reason = ?, user_message_id = ?,
		assistant_message_id = ?, finished_at = ?
		WHERE agent_attempt_id = ? AND model_attempt_number = ? AND status = 'started'`,
		terminal.status, modelAttempt.Outcome, terminal.inputFingerprint,
		terminal.actionFingerprint, modelAttempt.ErrorText,
		modelAttempt.RetryAfter.Milliseconds(), boolInt(modelAttempt.RetryPlanned), elapsedMillis,
		modelAttempt.StreamEvents, modelAttempt.StreamBytes, callUsage.InputTokens, callUsage.OutputTokens,
		callUsage.TotalTokens, boolInt(usageRecorded), actionKind, reportOutcome, policyAllowed,
		boolInt(policyNeedsApproval), policyRisk, policyReason, nullableInt64(terminal.userMessageID),
		nullableInt64(terminal.assistantMessageID), ts(now), attempt.ID, modelAttempt.Number)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result,
		"Specialist model call changed before terminal commit"); err != nil {
		return domain.AgentAttempt{}, err
	}
	if terminal.toolResponse != nil {
		if err := insertSpecialistToolRoundTx(ctx, tx, ref, modelAttempt, *terminal.toolResponse, now); err != nil {
			return domain.AgentAttempt{}, err
		}
	}
	if err := resolveSpecialistModelCancellationTx(ctx, tx,
		domain.AgentAttemptRef{RunID: attempt.RunID, AgentID: attempt.AgentID,
			AttemptID: attempt.ID}, modelAttempt.Number, string(modelAttempt.Outcome), now); err != nil {
		return domain.AgentAttempt{}, err
	}
	payload := map[string]any{
		"agent_id": attempt.AgentID, "parent_agent_id": attempt.ParentAgentID,
		"agent_attempt_id": attempt.ID, "turn": attempt.Turn,
		"model_attempt": modelAttempt.Number, "transport_attempt": modelAttempt.TransportNumber(),
		"max_attempts": modelAttempt.MaxAttempts, "provider": modelAttempt.Provider,
		"protocol_repair": modelAttempt.ProtocolRepair,
		"model":           modelAttempt.Model, "outcome": modelAttempt.Outcome,
		"elapsed_millis": elapsedMillis, "stream_events": modelAttempt.StreamEvents,
		"stream_bytes":            modelAttempt.StreamBytes,
		"monetary_attempt_number": specialistMonetaryPayloadNumber(modelAttempt),
	}
	if terminal.notDispatched {
		payload["dispatch"] = "not_sent"
	}
	eventType := events.ModelFailedEvent
	if terminal.status == "completed" {
		eventType = events.ModelCompletedEvent
		payload["usage"] = callUsage
		payload["action"] = actionKind
		payload["report_outcome"] = reportOutcome
		payload["policy_allowed"] = policyAllowed == 1
		payload["policy_needs_approval"] = policyNeedsApproval
		payload["session_user_message_id"] = terminal.userMessageID
		payload["session_assistant_message_id"] = terminal.assistantMessageID
	} else {
		payload["error"] = modelAttempt.ErrorText
		payload["retry_after_millis"] = modelAttempt.RetryAfter.Milliseconds()
		payload["retry_planned"] = modelAttempt.RetryPlanned
		if usageRecorded {
			payload["usage"] = callUsage
		}
	}
	subject := specialistModelSubject(attempt.ID, modelAttempt.Number)
	if err := appendSupervisorEventTx(ctx, tx, run, eventType,
		"specialist_model_gateway", subject, payload); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := commitSpecialistRepairTransitionTx(ctx, tx, run, attempt,
		modelAttempt.Number, terminal, now); err != nil {
		return domain.AgentAttempt{}, err
	}
	if terminal.decision != nil {
		if err := appendSupervisorEventTx(ctx, tx, run, events.PolicyDecisionEvent,
			"policy", subject, map[string]any{
				"context": "specialist_assistant_response", "agent_id": attempt.AgentID,
				"agent_attempt_id": attempt.ID, "allowed": terminal.decision.Allowed,
				"needs_approval": terminal.decision.NeedsApproval,
				"risk":           terminal.decision.Risk, "reason": terminal.decision.Reason,
			}); err != nil {
			return domain.AgentAttempt{}, err
		}
	}
	if usageChanged {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.AgentAttempt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentAttempt{}, err
	}
	return attempt, nil
}

func applySpecialistModelUsageTx(ctx context.Context, tx *sql.Tx,
	attempt domain.AgentAttempt, child domain.AgentNode, run domain.Run,
	modelAttempt int, delta *llm.Usage, elapsedMillis int64, now time.Time,
) (domain.AgentAttempt, domain.AgentNode, bool, error) {
	var priorInput, priorOutput, priorTotal, priorElapsed int64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(elapsed_millis), 0)
		FROM specialist_model_calls WHERE agent_attempt_id = ?
			AND model_attempt_number <> ? AND status <> 'started'`,
		attempt.ID, modelAttempt).Scan(&priorInput, &priorOutput, &priorTotal,
		&priorElapsed); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, false, err
	}
	executionMillis, err := supervisorAddCounter(priorElapsed, elapsedMillis,
		"Specialist model execution time")
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, false, err
	}
	if delta == nil && attempt.UsageRecordedAt == nil {
		return attempt, child, false, nil
	}
	aggregate := domain.AgentAttemptUsage{
		InputTokens: priorInput, OutputTokens: priorOutput,
		TotalTokens: priorTotal, ExecutionMillis: executionMillis,
	}
	var deltaTotal int64
	if delta != nil {
		inputTokens, outputTokens, totalTokens, err := supervisorUsage(*delta)
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		aggregate.InputTokens, err = supervisorAddCounter(aggregate.InputTokens,
			inputTokens, "Specialist model input token")
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		aggregate.OutputTokens, err = supervisorAddCounter(aggregate.OutputTokens,
			outputTokens, "Specialist model output token")
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		aggregate.TotalTokens, err = supervisorAddCounter(aggregate.TotalTokens,
			totalTokens, "Specialist model total token")
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		deltaTotal = totalTokens
	}
	if err := aggregate.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, false, err
	}
	if attempt.UsageRecordedAt != nil && (attempt.Usage.InputTokens != priorInput ||
		attempt.Usage.OutputTokens != priorOutput || attempt.Usage.TotalTokens != priorTotal ||
		attempt.Usage.ExecutionMillis != priorElapsed) {
		return domain.AgentAttempt{}, domain.AgentNode{}, false,
			apperror.New(apperror.CodeConflict,
				"Specialist Attempt usage disagrees with its durable model ledger")
	}
	updatedAttempt := attempt
	updatedAttempt.Usage = aggregate
	if updatedAttempt.UsageRecordedAt == nil {
		recordedAt := now.UTC()
		updatedAttempt.UsageRecordedAt = &recordedAt
	}
	updatedAttempt.UpdatedAt = now.UTC()
	if err := updatedAttempt.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_attempts SET input_tokens = ?,
		output_tokens = ?, total_tokens = ?, execution_millis = ?, usage_recorded_at = ?,
		updated_at = ? WHERE id = ? AND status = ?`, aggregate.InputTokens,
		aggregate.OutputTokens, aggregate.TotalTokens, aggregate.ExecutionMillis,
		ts(*updatedAttempt.UsageRecordedAt), ts(updatedAttempt.UpdatedAt), attempt.ID,
		domain.AgentAttemptRunning)
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, false, err
	}
	if err := requireSingleAgentAttemptUpdate(result,
		"Specialist Attempt changed during model usage commit"); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, false, err
	}
	updatedChild := child
	if delta != nil {
		updatedChild.TokensUsed, err = supervisorAddCounter(child.TokensUsed, deltaTotal,
			"Specialist token")
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		updatedChild.Version++
		updatedChild.UpdatedAt = now.UTC()
		if err := updatedChild.Validate(); err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		result, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET tokens_used = ?, version = ?,
			updated_at = ? WHERE id = ? AND version = ? AND status = ? AND active_attempt_id = ?`,
			updatedChild.TokensUsed, updatedChild.Version, ts(updatedChild.UpdatedAt), child.ID,
			child.Version, domain.AgentRunning, attempt.ID)
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		if err := requireSingleAgentAttemptUpdate(result,
			"Specialist changed during model usage commit"); err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
		if err := appendSupervisorEventTx(ctx, tx, run,
			events.AgentAttemptUsageRecordedEvent, "agent_coordinator", attempt.ID,
			agentAttemptEventPayload(updatedAttempt, false)); err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, false, err
		}
	}
	return updatedAttempt, updatedChild, true, nil
}

func commitSpecialistRepairTransitionTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	attempt domain.AgentAttempt, modelAttempt int, terminal specialistModelTerminal,
	now time.Time,
) error {
	switch terminal.repairStatus {
	case "":
		return nil
	case domain.SpecialistRepairPending:
		_, err := insertSpecialistProtocolRepairTx(ctx, tx, run, attempt, modelAttempt,
			terminal.repairReason, now)
		return err
	case domain.SpecialistRepairCompleted, domain.SpecialistRepairExhausted:
		_, err := resolveSpecialistProtocolRepairTx(ctx, tx, run, attempt,
			terminal.repairStatus, modelAttempt, now)
		return err
	default:
		return apperror.New(apperror.CodeInvalidArgument,
			"Specialist model terminal has an invalid repair transition")
	}
}

func requireSpecialistRepairTerminalReplayTx(ctx context.Context, tx *sql.Tx,
	attemptID string, modelAttempt int, terminal specialistModelTerminal,
) error {
	if terminal.repairStatus == "" {
		return nil
	}
	repair, found, err := getSpecialistProtocolRepairTx(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if !found {
		return apperror.New(apperror.CodeConflict,
			"Specialist model terminal replay is missing its repair state")
	}
	switch terminal.repairStatus {
	case domain.SpecialistRepairPending:
		if repair.RequestedModelAttempt != modelAttempt ||
			repair.Reason != normalizeSpecialistRepairReason(terminal.repairReason) {
			return apperror.New(apperror.CodeConflict,
				"Specialist repair request replay differs from its durable state")
		}
	case domain.SpecialistRepairCompleted, domain.SpecialistRepairExhausted:
		if repair.Status != terminal.repairStatus || repair.ResolvedModelAttempt != modelAttempt {
			return apperror.New(apperror.CodeConflict,
				"Specialist repair resolution replay differs from its durable state")
		}
	default:
		return apperror.New(apperror.CodeInvalidArgument,
			"Specialist repair replay status is invalid")
	}
	return nil
}

func validateSpecialistModelIdentity(attempt llm.ModelAttempt) error {
	if attempt.Purpose != "" {
		return apperror.New(apperror.CodeInvalidArgument, "Specialist calls cannot use Supervisor compaction purpose")
	}
	if err := attempt.ValidateStarted(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist model call identity is invalid", err)
	}
	if attempt.ToolRound < 0 || attempt.ToolRound > domain.MaxSpecialistToolRounds || attempt.MaxAttempts > maxSpecialistModelTransportAttempts {
		return apperror.New(apperror.CodeInvalidArgument,
			"Specialist no-tool model call has an unsupported attempt shape")
	}
	return nil
}

func getSpecialistModelCallTx(ctx context.Context, tx *sql.Tx, attemptID string,
	modelAttempt int,
) (specialistModelCallRecord, bool, error) {
	record, err := scanSpecialistModelCall(tx.QueryRowContext(ctx,
		specialistModelCallSelect+` WHERE agent_attempt_id = ? AND model_attempt_number = ?`,
		attemptID, modelAttempt))
	if errors.Is(err, sql.ErrNoRows) {
		return specialistModelCallRecord{}, false, nil
	}
	return record, err == nil, err
}

func scanSpecialistModelCall(row scanner) (specialistModelCallRecord, error) {
	var record specialistModelCallRecord
	var outcome, actionKind, reportOutcome, startedAt string
	var retryPlanned, usageRecorded, policyNeedsApproval int
	var userMessageID, assistantMessageID sql.NullInt64
	var finishedAt sql.NullString
	if err := row.Scan(&record.AgentAttemptID, &record.RunID, &record.AgentID,
		&record.ModelAttempt, &record.TransportAttempt, &record.MaxAttempts, &record.ProtocolRepair,
		&record.ToolRound,
		&record.Provider, &record.Model, &record.Status, &outcome,
		&record.InputFingerprint, &record.ActionFingerprint, &record.ErrorText,
		&record.RetryAfterMillis, &retryPlanned, &record.ElapsedMillis,
		&record.StreamEvents, &record.StreamBytes, &record.Usage.InputTokens,
		&record.Usage.OutputTokens, &record.Usage.TotalTokens, &usageRecorded,
		&actionKind, &reportOutcome, &record.PolicyAllowed, &policyNeedsApproval,
		&record.PolicyRisk, &record.PolicyReason, &userMessageID, &assistantMessageID,
		&startedAt, &finishedAt); err != nil {
		return specialistModelCallRecord{}, err
	}
	record.Outcome = llm.Outcome(outcome)
	record.RetryPlanned = retryPlanned != 0
	record.UsageRecorded = usageRecorded != 0
	record.ActionKind = domain.SpecialistActionKind(actionKind)
	record.ReportOutcome = domain.CompletionOutcome(reportOutcome)
	record.PolicyNeedsApproval = policyNeedsApproval != 0
	if userMessageID.Valid {
		record.UserMessageID = &userMessageID.Int64
	}
	if assistantMessageID.Valid {
		record.AssistantMessageID = &assistantMessageID.Int64
	}
	record.StartedAt = parseTS(startedAt)
	record.FinishedAt = parseNullableTS(finishedAt)
	return record, nil
}

func requireSpecialistModelIdentity(record specialistModelCallRecord,
	attempt llm.ModelAttempt,
) error {
	if record.ModelAttempt != attempt.Number || record.TransportAttempt != attempt.TransportNumber() ||
		record.MaxAttempts != attempt.MaxAttempts || record.ProtocolRepair != attempt.ProtocolRepair || record.ToolRound != attempt.ToolRound ||
		record.Provider != attempt.Provider ||
		record.Model != attempt.Model {
		return apperror.New(apperror.CodeConflict,
			"Specialist model call identity differs from its durable record")
	}
	return nil
}

func requireSpecialistTerminalReplay(record specialistModelCallRecord,
	attempt llm.ModelAttempt, terminal specialistModelTerminal,
) error {
	elapsedMillis, err := supervisorElapsedMillis(attempt.Elapsed)
	if err != nil {
		return err
	}
	if record.Status != terminal.status || record.Outcome != attempt.Outcome ||
		record.ErrorText != attempt.ErrorText ||
		record.RetryAfterMillis != attempt.RetryAfter.Milliseconds() ||
		record.RetryPlanned != attempt.RetryPlanned ||
		record.ElapsedMillis != elapsedMillis ||
		record.StreamEvents != attempt.StreamEvents || record.StreamBytes != attempt.StreamBytes {
		return apperror.New(apperror.CodeConflict,
			"Specialist model terminal replay differs from its durable record")
	}
	if terminal.usage != nil {
		input, output, total, err := supervisorUsage(*terminal.usage)
		if err != nil || !record.UsageRecorded || record.Usage.InputTokens != input ||
			record.Usage.OutputTokens != output || record.Usage.TotalTokens != total {
			return apperror.New(apperror.CodeConflict,
				"Specialist model usage replay differs from its durable record")
		}
	} else if record.UsageRecorded {
		return apperror.New(apperror.CodeConflict,
			"Specialist model replay omitted its durable usage")
	}
	if terminal.action != nil && terminal.decision != nil {
		reportOutcome := domain.CompletionOutcome("")
		if terminal.action.Report != nil {
			reportOutcome = terminal.action.Report.Outcome
		}
		allowed := 0
		if terminal.decision.Allowed {
			allowed = 1
		}
		if record.InputFingerprint != terminal.inputFingerprint ||
			record.ActionFingerprint != terminal.actionFingerprint ||
			record.ActionKind != terminal.action.Kind || record.ReportOutcome != reportOutcome ||
			record.PolicyAllowed != allowed ||
			record.PolicyNeedsApproval != terminal.decision.NeedsApproval ||
			record.PolicyRisk != terminal.decision.Risk ||
			record.PolicyReason != terminal.decision.Reason {
			return apperror.New(apperror.CodeConflict,
				"Specialist model action replay differs from its durable record")
		}
	} else if terminal.toolResponse != nil {
		if record.InputFingerprint != terminal.inputFingerprint || record.ActionFingerprint != terminal.actionFingerprint || record.ActionKind != "tool_calls" {
			return apperror.New(apperror.CodeConflict, "child tool response replay changed")
		}
	} else if record.InputFingerprint != "" || record.ActionFingerprint != "" {
		return apperror.New(apperror.CodeConflict,
			"Specialist model replay omitted its durable intent fingerprints")
	}
	return nil
}

func normalizeSpecialistPolicyDecision(decision policy.Decision) (policy.Decision, error) {
	decision.Reason = sanitizeSupervisorText(decision.Reason)
	decision.Risk = sanitizeSupervisorText(decision.Risk)
	if decision.Reason == "" {
		return policy.Decision{}, apperror.New(apperror.CodeInvalidArgument,
			"Specialist policy decision reason is required")
	}
	return decision, nil
}

func redactSpecialistAction(action domain.SpecialistAction) (domain.SpecialistAction, error) {
	action.Message = redact.String(action.Message)
	if action.Report != nil {
		report := *action.Report
		report.Summary = redact.String(report.Summary)
		action.Report = &report
	}
	action, err := domain.NormalizeSpecialistAction(action)
	if err != nil {
		return domain.SpecialistAction{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"redacted Specialist action is invalid", err)
	}
	return action, nil
}

func sameSpecialistAction(left domain.SpecialistAction, right domain.SpecialistAction) bool {
	if left.Version != right.Version || left.Kind != right.Kind || left.Message != right.Message {
		return false
	}
	if left.Report == nil || right.Report == nil {
		return left.Report == nil && right.Report == nil
	}
	return left.Report.Version == right.Report.Version && left.Report.Outcome == right.Report.Outcome &&
		left.Report.Summary == right.Report.Summary &&
		slices.Equal(left.Report.WorkItemIDs, right.Report.WorkItemIDs) &&
		slices.Equal(left.Report.NoteIDs, right.Report.NoteIDs)
}

func specialistModelSubject(attemptID string, modelAttempt int) string {
	return fmt.Sprintf("%s:model:%d", strings.TrimSpace(attemptID), modelAttempt)
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
