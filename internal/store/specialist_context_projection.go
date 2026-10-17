package store

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

var specialistContextStatements = []string{`CREATE TABLE specialist_context_projections (
 agent_attempt_id TEXT PRIMARY KEY REFERENCES agent_attempts(id),
 through_round INTEGER NOT NULL DEFAULT 0 CHECK(through_round BETWEEN 0 AND 16),
 source_sha256 TEXT NOT NULL DEFAULT '',
 rejected_model INTEGER NOT NULL DEFAULT 0 CHECK(rejected_model >= 0),
 recovery_input_limit INTEGER NOT NULL DEFAULT 0 CHECK(recovery_input_limit >= 0),
 CHECK((through_round=0 AND source_sha256='') OR (through_round>0 AND length(source_sha256)=64)),
 CHECK((rejected_model=0 AND recovery_input_limit=0) OR (rejected_model>0 AND recovery_input_limit>0))
);`,
	`DROP TRIGGER trg_specialist_model_call_phase_sequence;`,
	`CREATE TRIGGER trg_specialist_model_call_phase_sequence
 BEFORE INSERT ON specialist_model_calls
 WHEN NEW.transport_attempt <> (
  SELECT COUNT(*) + 1 FROM specialist_model_calls
  WHERE agent_attempt_id=NEW.agent_attempt_id AND protocol_repair=NEW.protocol_repair AND tool_round=NEW.tool_round
   AND model_attempt_number<>COALESCE((SELECT rejected_model FROM specialist_context_projections WHERE agent_attempt_id=NEW.agent_attempt_id),0)
 )
 BEGIN SELECT RAISE(ABORT,'Specialist model call is not the next phase transport attempt'); END;`,
	`CREATE TRIGGER trg_specialist_context_projection_monotonic BEFORE UPDATE ON specialist_context_projections
 WHEN NEW.agent_attempt_id<>OLD.agent_attempt_id OR NEW.through_round<OLD.through_round
 OR (NEW.through_round=OLD.through_round AND NEW.source_sha256<>OLD.source_sha256)
 OR (OLD.rejected_model>0 AND (NEW.rejected_model<>OLD.rejected_model OR NEW.recovery_input_limit<>OLD.recovery_input_limit))
 BEGIN SELECT RAISE(ABORT,'Child context projection cannot rewind'); END;`,
}

type specialistContextQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func specialistContextState(ctx context.Context, q specialistContextQueryer, ref domain.AgentAttemptRef) (domain.SpecialistContextState, error) {
	var state domain.SpecialistContextState
	err := q.QueryRowContext(ctx, `SELECT p.through_round,p.source_sha256,p.rejected_model,p.recovery_input_limit
 FROM specialist_context_projections p JOIN agent_attempts a ON a.id=p.agent_attempt_id
 WHERE a.id=? AND a.run_id=? AND a.agent_id=?`, ref.AttemptID, ref.RunID, ref.AgentID).
		Scan(&state.ThroughRound, &state.SourceSHA256, &state.RejectedModel, &state.RecoveryInputLimit)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return state, err
}

func (s *SQLiteStore) GetSpecialistContextState(ctx context.Context, ref domain.AgentAttemptRef) (domain.SpecialistContextState, error) {
	if err := ref.Validate(); err != nil {
		return domain.SpecialistContextState{}, err
	}
	return specialistContextState(ctx, s.db, ref)
}

func requireSettledSpecialistContextTx(ctx context.Context, tx *sql.Tx, ref domain.AgentAttemptRef) error {
	if _, _, _, err := loadActiveAgentAttemptTx(ctx, tx, ref); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_model_calls WHERE agent_attempt_id=? AND status='started'`, ref.AttemptID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return apperror.New(apperror.CodeConflict, "child context has an unsettled model call")
	}
	return nil
}

func (s *SQLiteStore) SaveSpecialistContextProjection(ctx context.Context, ref domain.AgentAttemptRef, expected domain.SpecialistContextState, through int, sourceSHA string) (domain.SpecialistContextState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return expected, err
	}
	defer tx.Rollback()
	if err := requireSettledSpecialistContextTx(ctx, tx, ref); err != nil {
		return expected, err
	}
	current, err := specialistContextState(ctx, tx, ref)
	if err != nil {
		return expected, err
	}
	if current != expected {
		return expected, apperror.New(apperror.CodeConflict, "child context projection changed")
	}
	rounds, err := listSpecialistToolRounds(ctx, tx, ref)
	if err != nil {
		return expected, err
	}
	if through <= current.ThroughRound || through > len(rounds) || domain.SpecialistContextSourceSHA256(rounds) != sourceSHA {
		return expected, apperror.New(apperror.CodeConflict, "child context sources changed")
	}
	if current.ThroughRound > 0 && domain.SpecialistContextSourceSHA256(rounds[:current.ThroughRound]) != current.SourceSHA256 {
		return expected, apperror.New(apperror.CodeConflict, "child context retained sources changed")
	}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if !call.Status.Terminal() || call.CompletedAt == nil {
				return expected, errors.New("child context requires settled tool rounds")
			}
		}
		if len(round.ProviderReplay) > 0 {
			replay, err := llm.DecodeProviderReplay(round.ProviderReplay)
			if err != nil {
				return expected, err
			}
			if replay.RequiresPrivateAssistantHistory() {
				return expected, errors.New("child private history cannot be replaced by receipts")
			}
		}
	}
	current.ThroughRound, current.SourceSHA256 = through, domain.SpecialistContextSourceSHA256(rounds[:through])
	_, err = tx.ExecContext(ctx, `INSERT INTO specialist_context_projections(agent_attempt_id,through_round,source_sha256)
 VALUES(?,?,?) ON CONFLICT(agent_attempt_id) DO UPDATE SET through_round=excluded.through_round,source_sha256=excluded.source_sha256`, ref.AttemptID, through, current.SourceSHA256)
	if err != nil {
		return expected, err
	}
	return current, tx.Commit()
}

// A terminal, classified rejection is required. The watermark and lower input
// ceiling survive approval pauses/reopen; another rejection cannot reopen it.
func (s *SQLiteStore) ClaimSpecialistContextRecovery(ctx context.Context, ref domain.AgentAttemptRef, model llm.ModelAttempt, rejectedInput int) (bool, error) {
	if rejectedInput <= 1 || model.FailureReason != llm.ProviderFailureContextLimit {
		return false, errors.New("child context recovery requires a context rejection")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := requireSettledSpecialistContextTx(ctx, tx, ref); err != nil {
		return false, err
	}
	state, err := specialistContextState(ctx, tx, ref)
	if err != nil {
		return false, err
	}
	if state.RejectedModel != 0 {
		return false, nil
	}
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_model_calls c JOIN run_events e
 ON e.run_id=c.run_id AND e.subject_id=? AND e.source='specialist_model_gateway' AND e.type=?
 WHERE c.agent_attempt_id=? AND c.run_id=? AND c.agent_id=? AND c.model_attempt_number=? AND c.status='failed'
 AND c.model_attempt_number=(SELECT MAX(model_attempt_number) FROM specialist_model_calls WHERE agent_attempt_id=c.agent_attempt_id)
 AND json_extract(e.payload_json,'$.failure_reason')=?`, specialistModelSubject(ref.AttemptID, model.Number), events.ModelFailedEvent,
		ref.AttemptID, ref.RunID, ref.AgentID, model.Number, llm.ProviderFailureContextLimit).Scan(&valid)
	if err != nil {
		return false, err
	}
	if valid != 1 {
		return false, apperror.New(apperror.CodeConflict, "child context rejection has no exact terminal receipt")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO specialist_context_projections(agent_attempt_id,rejected_model,recovery_input_limit)
 VALUES(?,?,?) ON CONFLICT(agent_attempt_id) DO UPDATE SET rejected_model=excluded.rejected_model,recovery_input_limit=excluded.recovery_input_limit`, ref.AttemptID, model.Number, rejectedInput-1)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// The one classified rejection starts a new bounded transport group. Global
// model numbering and accounting remain monotonic, even with MaxAttempts=1.
func specialistContextTransportCountTx(ctx context.Context, tx *sql.Tx, attemptID string, round, repair, count int) (int, error) {
	var recovered int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_context_projections p JOIN specialist_model_calls c
 ON c.agent_attempt_id=p.agent_attempt_id AND c.model_attempt_number=p.rejected_model
 WHERE p.agent_attempt_id=? AND c.tool_round=? AND c.protocol_repair=? AND c.status='failed'`, attemptID, round, repair).Scan(&recovered)
	return count - recovered, err
}

func (s *SQLiteStore) ReadSpecialistToolResult(ctx context.Context, ref domain.AgentAttemptRef, request domain.SpecialistResultReadRequest) (domain.SpecialistResultPage, error) {
	var page domain.SpecialistResultPage
	if err := request.Validate(); err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	_, child, _, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return page, err
	}
	call, err := getSpecialistToolCallTx(ctx, tx, ref, request.CallID)
	if err != nil {
		return page, err
	}
	if !call.Status.Terminal() || call.CompletedAt == nil {
		return page, errors.New("child receipt is not terminal")
	}
	if child.Worker != nil {
		if _, err := validateSpecialistWorkerBindingTx(ctx, tx, ref, domain.SpecialistResultReadName); err != nil {
			return page, err
		}
		if call.Name != domain.SpecialistResultReadName && !child.Worker.AllowsTool(call.Name) {
			return page, apperror.New(apperror.CodePolicyDenied, "child receipt source permission was revoked")
		}
		if call.AuthorityJSON != "" {
			if err := validateSpecialistApprovalPermissionTx(ctx, tx, ref.RunID, call.AuthorityJSON); err != nil {
				return page, err
			}
		}
	} else {
		var valid int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_tool_bindings b JOIN agent_nodes n ON n.id=b.agent_id
 WHERE b.agent_attempt_id=? AND (b.mode='batch' OR (b.mode='read' AND b.allowed_read_paths_json=n.allowed_read_paths_json AND json_array_length(n.allowed_read_paths_json)>0))`, ref.AttemptID).Scan(&valid); err != nil {
			return page, err
		}
		if valid != 1 {
			return page, apperror.New(apperror.CodePolicyDenied, "child receipt read scope was revoked")
		}
	}
	content := call.ResultJSON
	if request.Part == "arguments" {
		content = call.PayloadJSON
	}
	if session.ContentSHA256(content) != request.ExpectedSHA256 {
		return page, apperror.New(apperror.CodeConflict, "child receipt digest changed")
	}
	if request.Offset > len(content) || (request.Offset < len(content) && !utf8.RuneStart(content[request.Offset])) {
		return page, errors.New("child receipt offset is outside a UTF-8 boundary")
	}
	end := min(len(content), request.Offset+request.Limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	page = domain.SpecialistResultPage{Version: "specialist_result_page.v1", CallID: call.CallID, Part: request.Part,
		Status: call.Status, Content: content[request.Offset:end], ContentSHA256: request.ExpectedSHA256,
		Offset: request.Offset, NextOffset: end, TotalBytes: len(content), HasMore: end < len(content)}
	return page, tx.Commit()
}
