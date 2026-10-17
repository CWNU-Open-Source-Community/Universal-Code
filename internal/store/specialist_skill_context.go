package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/skills"
)

const specialistSkillContextPreparationSelect = `SELECT id, run_id, mission_id,
	agent_id, parent_agent_id, agent_attempt_id, turn_number, parent_selection_id,
	protocol_version, parent_selection_fingerprint, mode_snapshot_id, mode_revision,
	surface, profile, assignment_fingerprint, context_fingerprint, item_count,
	token_budget, token_upper_bound, redaction_count, prepared_at
	FROM specialist_skill_context_preparations`

const specialistSkillContextCommitSelect = `SELECT preparation_id, run_id,
	agent_attempt_id, model_attempt, committed_at
	FROM specialist_skill_context_commits`

func (s *SQLiteStore) PrepareSpecialistSkillContext(ctx context.Context,
	ref domain.AgentAttemptRef, request skills.SpecialistContextPreparationRequest,
) (skills.SpecialistContextPreparation, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return skills.SpecialistContextPreparation{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Specialist Skill context reference is invalid", err)
	}
	if err := request.Validate(); err != nil {
		return skills.SpecialistContextPreparation{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Specialist Skill context request is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		ref.AgentID); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		attempt.ParentAgentID))
	if err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	selection, found, err := getSkillSelectionByRun(ctx, tx, run.ID)
	if err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	if !found {
		return skills.SpecialistContextPreparation{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Specialist Skill context requires a persisted parent Run selection")
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	if err := validateSpecialistSkillContextBinding(request, selection, mode,
		attempt, child, parent, run); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	existing, found, err := getSpecialistSkillContextPreparationByAttemptTx(ctx, tx,
		attempt.ID)
	if err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	if found {
		if existing.SpecialistContextPreparationRequest != request {
			return skills.SpecialistContextPreparation{}, apperror.New(
				apperror.CodeConflict,
				"prepared Specialist Skill context does not match its reconstructed context")
		}
		existing.Recovered = true
		if err := tx.Commit(); err != nil {
			return skills.SpecialistContextPreparation{}, err
		}
		return existing, nil
	}
	if attempt.UsageRecordedAt != nil {
		return skills.SpecialistContextPreparation{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Specialist Skill context must be prepared before model usage is recorded")
	}
	now := time.Now().UTC()
	preparation := skills.SpecialistContextPreparation{
		ID:                                  idgen.New("specialist-skillctx"),
		SpecialistContextPreparationRequest: request,
		PreparedAt:                          now,
	}
	if err := preparation.Validate(); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_skill_context_preparations
		(id, run_id, mission_id, agent_id, parent_agent_id, agent_attempt_id,
		turn_number, parent_selection_id, protocol_version, parent_selection_fingerprint,
		mode_snapshot_id, mode_revision, surface, profile, assignment_fingerprint,
		context_fingerprint, item_count, token_budget, token_upper_bound,
		redaction_count, prepared_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		preparation.ID, request.RunID, request.MissionID, request.AgentID,
		request.ParentAgentID, request.AgentAttemptID, request.Turn,
		request.ParentSelectionID, request.ProtocolVersion,
		request.ParentSelectionFingerprint, request.ModeSnapshotID, request.ModeRevision,
		request.Surface, request.Profile, request.AssignmentFingerprint,
		request.ContextFingerprint, request.ItemCount, request.TokenBudget,
		request.TokenUpperBound, request.RedactionCount, ts(now)); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.SpecialistSkillContextPreparedEvent, "specialist_skills", preparation.ID,
		specialistSkillContextEventPayload(preparation, 0)); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	if err := tx.Commit(); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	return preparation, nil
}

// commitSpecialistSkillContextTx binds the prepared metadata to the first
// durable model start. The exact Skill bodies remain in application memory.
func commitSpecialistSkillContextTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	attempt domain.AgentAttempt, child domain.AgentNode, modelAttempt int,
) error {
	selection, selected, err := getSkillSelectionByRun(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	preparation, prepared, err := getSpecialistSkillContextPreparationByAttemptTx(ctx, tx,
		attempt.ID)
	if err != nil {
		return err
	}
	if !selected {
		if prepared {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Specialist Skill context exists without a parent Run selection")
		}
		return nil
	}
	if !prepared {
		return apperror.New(apperror.CodeFailedPrecondition,
			"persisted parent Skill selection was not prepared for the active Specialist attempt")
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		attempt.ParentAgentID))
	if err != nil {
		return err
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	if err := validateSpecialistSkillContextBinding(
		preparation.SpecialistContextPreparationRequest, selection, mode,
		attempt, child, parent, run); err != nil {
		return err
	}
	existing, found, err := getSpecialistSkillContextCommitTx(ctx, tx, preparation.ID)
	if err != nil {
		return err
	}
	if found {
		if existing.RunID != run.ID || existing.AgentAttemptID != attempt.ID ||
			existing.ModelAttempt <= 0 || existing.ModelAttempt > modelAttempt {
			return apperror.New(apperror.CodeConflict,
				"Specialist Skill context commit does not match the model attempt")
		}
		return nil
	}
	commit := skills.SpecialistContextCommit{
		PreparationID: preparation.ID, RunID: run.ID, AgentAttemptID: attempt.ID,
		ModelAttempt: modelAttempt, CommittedAt: time.Now().UTC(),
	}
	if err := commit.Validate(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_skill_context_commits
		(preparation_id, run_id, agent_attempt_id, model_attempt, committed_at)
		VALUES (?, ?, ?, ?, ?)`, commit.PreparationID, commit.RunID,
		commit.AgentAttemptID, commit.ModelAttempt, ts(commit.CommittedAt)); err != nil {
		return err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.SpecialistSkillContextCommittedEvent, "specialist_skills", preparation.ID,
		specialistSkillContextEventPayload(preparation, commit.ModelAttempt)); err != nil {
		return err
	}
	return nil
}

func validateSpecialistSkillContextBinding(
	request skills.SpecialistContextPreparationRequest, selection skills.Selection,
	mode domain.RunModeSnapshot, attempt domain.AgentAttempt, child domain.AgentNode,
	parent domain.AgentNode, run domain.Run,
) error {
	if err := request.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist Skill context request is invalid", err)
	}
	if err := selection.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable parent Skill selection is invalid", err)
	}
	if err := mode.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Run mode is invalid", err)
	}
	if request.RunID != run.ID || request.MissionID != run.MissionID ||
		request.AgentID != child.ID || request.ParentAgentID != parent.ID ||
		request.AgentAttemptID != attempt.ID || request.Turn != attempt.Turn ||
		attempt.RunID != run.ID || attempt.AgentID != child.ID ||
		attempt.ParentAgentID != parent.ID || attempt.Status != domain.AgentAttemptRunning ||
		child.RunID != run.ID || child.Role != domain.AgentRoleSpecialist ||
		child.Status != domain.AgentRunning || child.ParentID != parent.ID ||
		child.ActiveAttemptID != attempt.ID || parent.RunID != run.ID ||
		parent.Role != domain.AgentRoleRoot || parent.Terminal() ||
		!slices.Contains(child.Skills, "model.chat") ||
		selection.ID != request.ParentSelectionID || selection.RunID != run.ID ||
		selection.MissionID != run.MissionID || selection.Profile != request.Profile ||
		selection.Fingerprint != request.ParentSelectionFingerprint ||
		mode.ID != request.ModeSnapshotID || mode.RunID != run.ID ||
		mode.MissionID != run.MissionID || mode.Revision != request.ModeRevision ||
		mode.Surface != request.Surface || mode.Profile != request.Profile ||
		child.Profile != request.Profile ||
		request.AssignmentFingerprint != skills.SpecialistAssignmentFingerprint(child) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Specialist Skill context provenance does not match durable Run state")
	}
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"embedded Skill Registry is unavailable", err)
	}
	selected, found, err := registry.SpecialistSelectionItem(selection, skills.ExecutionContext{
		Surface: mode.Surface, Phase: mode.Phase, Profile: mode.Profile,
		Role: domain.AgentRoleSpecialist,
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"Specialist Skill compatibility could not be reconstructed", err)
	}
	if !found {
		if request.ItemCount != 0 || request.TokenUpperBound != 0 ||
			request.RedactionCount != 0 {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Specialist Skill context exceeds its empty mode policy")
		}
		return nil
	}
	if request.ItemCount != 1 || request.TokenUpperBound <= 0 ||
		request.TokenUpperBound > selected.TokenUpperBound {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Specialist Skill context exceeds its minimal parent-selected subset")
	}
	return nil
}

func specialistSkillContextEventPayload(
	preparation skills.SpecialistContextPreparation, modelAttempt int,
) map[string]any {
	request := preparation.SpecialistContextPreparationRequest
	return map[string]any{
		"protocol": request.ProtocolVersion, "agent_id": request.AgentID,
		"parent_agent_id":  request.ParentAgentID,
		"agent_attempt_id": request.AgentAttemptID, "turn": request.Turn,
		"surface": request.Surface, "profile": request.Profile,
		"mode_revision": request.ModeRevision, "item_count": request.ItemCount,
		"token_budget":      request.TokenBudget,
		"token_upper_bound": request.TokenUpperBound,
		"redaction_count":   request.RedactionCount, "model_attempt": modelAttempt,
		"context_injection": modelAttempt > 0, "tool_capability_grant": false,
	}
}

func getSpecialistSkillContextPreparationByAttemptTx(ctx context.Context, tx *sql.Tx,
	attemptID string,
) (skills.SpecialistContextPreparation, bool, error) {
	item, err := scanSpecialistSkillContextPreparation(tx.QueryRowContext(ctx,
		specialistSkillContextPreparationSelect+` WHERE agent_attempt_id = ?`, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return skills.SpecialistContextPreparation{}, false, nil
	}
	return item, err == nil, err
}

func getSpecialistSkillContextCommitTx(ctx context.Context, tx *sql.Tx,
	preparationID string,
) (skills.SpecialistContextCommit, bool, error) {
	item, err := scanSpecialistSkillContextCommit(tx.QueryRowContext(ctx,
		specialistSkillContextCommitSelect+` WHERE preparation_id = ?`, preparationID))
	if errors.Is(err, sql.ErrNoRows) {
		return skills.SpecialistContextCommit{}, false, nil
	}
	return item, err == nil, err
}

func scanSpecialistSkillContextPreparation(row scanner) (
	skills.SpecialistContextPreparation, error,
) {
	var preparation skills.SpecialistContextPreparation
	request := &preparation.SpecialistContextPreparationRequest
	var preparedAt string
	if err := row.Scan(&preparation.ID, &request.RunID, &request.MissionID,
		&request.AgentID, &request.ParentAgentID, &request.AgentAttemptID, &request.Turn,
		&request.ParentSelectionID, &request.ProtocolVersion,
		&request.ParentSelectionFingerprint, &request.ModeSnapshotID, &request.ModeRevision,
		&request.Surface, &request.Profile, &request.AssignmentFingerprint,
		&request.ContextFingerprint, &request.ItemCount, &request.TokenBudget,
		&request.TokenUpperBound, &request.RedactionCount, &preparedAt); err != nil {
		return skills.SpecialistContextPreparation{}, err
	}
	preparation.PreparedAt = parseTS(preparedAt)
	return preparation, preparation.Validate()
}

func scanSpecialistSkillContextCommit(row scanner) (skills.SpecialistContextCommit, error) {
	var commit skills.SpecialistContextCommit
	var committedAt string
	if err := row.Scan(&commit.PreparationID, &commit.RunID, &commit.AgentAttemptID,
		&commit.ModelAttempt, &committedAt); err != nil {
		return skills.SpecialistContextCommit{}, err
	}
	commit.CommittedAt = parseTS(committedAt)
	return commit, commit.Validate()
}
