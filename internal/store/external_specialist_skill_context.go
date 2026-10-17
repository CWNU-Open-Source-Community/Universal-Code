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

const externalSpecialistContextPreparationSelect = `SELECT id, run_id, mission_id,
	agent_id, parent_agent_id, agent_attempt_id, turn_number, parent_selection_id,
	protocol_version, parent_selection_fingerprint, mode_snapshot_id, mode_revision,
	surface, profile, assignment_fingerprint, context_fingerprint, item_count,
	token_budget, token_upper_bound, redaction_count, prepared_at
	FROM specialist_external_skill_context_preparations`

const externalSpecialistContextCommitSelect = `SELECT preparation_id, run_id,
	agent_attempt_id, model_attempt, committed_at
	FROM specialist_external_skill_context_commits`

func (s *SQLiteStore) PrepareExternalSpecialistSkillContext(ctx context.Context,
	ref domain.AgentAttemptRef,
	request skills.ExternalSpecialistContextPreparationRequest,
) (skills.ExternalSpecialistContextPreparation, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "external Specialist reference is invalid", err)
	}
	if err := request.Validate(); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "external Specialist Skill context is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		ref.AgentID); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		attempt.ParentAgentID))
	if err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	selection, found, err := getExternalSkillSelectionByRun(ctx, tx, run.ID)
	if err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if !found {
		return skills.ExternalSpecialistContextPreparation{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"external Specialist Skill context requires a parent external selection")
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if err := validateExternalSpecialistContextBinding(request, selection, mode,
		attempt, child, parent, run); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if err := validatePluginSkillSelectionTx(ctx, tx, selection, domain.AgentRoleSpecialist); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	existing, found, err := getExternalSpecialistContextPreparationByAttemptTx(ctx, tx,
		attempt.ID)
	if err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if found {
		if existing.ExternalSpecialistContextPreparationRequest != request {
			return skills.ExternalSpecialistContextPreparation{}, apperror.New(
				apperror.CodeConflict,
				"prepared external Specialist context does not match reconstructed context")
		}
		existing.Recovered = true
		if err := tx.Commit(); err != nil {
			return skills.ExternalSpecialistContextPreparation{}, err
		}
		return existing, nil
	}
	if attempt.UsageRecordedAt != nil {
		return skills.ExternalSpecialistContextPreparation{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"external Specialist Skill context must precede model usage")
	}
	preparation := skills.ExternalSpecialistContextPreparation{
		ID: idgen.New("external-specialist-skillctx"),
		ExternalSpecialistContextPreparationRequest: request,
		PreparedAt: time.Now().UTC(),
	}
	if err := preparation.Validate(); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_external_skill_context_preparations
		(id, run_id, mission_id, agent_id, parent_agent_id, agent_attempt_id,
		turn_number, parent_selection_id, protocol_version, parent_selection_fingerprint,
		mode_snapshot_id, mode_revision, surface, profile, assignment_fingerprint,
		context_fingerprint, item_count, token_budget, token_upper_bound,
		redaction_count, prepared_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		preparation.ID, request.RunID, request.MissionID, request.AgentID,
		request.ParentAgentID, request.AgentAttemptID, request.Turn,
		request.ParentSelectionID, request.ProtocolVersion,
		request.ParentSelectionFingerprint, request.ModeSnapshotID, request.ModeRevision,
		request.Surface, request.Profile, request.AssignmentFingerprint,
		request.ContextFingerprint, request.ItemCount, request.TokenBudget,
		request.TokenUpperBound, request.RedactionCount, ts(preparation.PreparedAt)); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.ExternalSpecialistSkillContextPreparedEvent, "external_specialist_skills",
		preparation.ID, externalSpecialistContextEventPayload(preparation, 0)); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	if err := tx.Commit(); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	return preparation, nil
}

func commitExternalSpecialistSkillContextTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, attempt domain.AgentAttempt, child domain.AgentNode,
	modelAttempt int,
) error {
	selection, selected, err := getExternalSkillSelectionByRun(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	selectedItem, specialistSelected := skills.ExternalSpecialistItem(selection)
	preparation, prepared, err := getExternalSpecialistContextPreparationByAttemptTx(ctx, tx,
		attempt.ID)
	if err != nil {
		return err
	}
	if !selected || !specialistSelected {
		if prepared {
			return apperror.New(apperror.CodeFailedPrecondition,
				"external Specialist context exists without an eligible selection")
		}
		return nil
	}
	if !prepared {
		return apperror.New(apperror.CodeFailedPrecondition,
			"eligible external Specialist Skill was not prepared for the model call")
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
	if err := validateExternalSpecialistContextBinding(
		preparation.ExternalSpecialistContextPreparationRequest, selection, mode,
		attempt, child, parent, run); err != nil {
		return err
	}
	if err := validatePluginSkillSelectionTx(ctx, tx, selection, domain.AgentRoleSpecialist); err != nil {
		return err
	}
	if preparation.TokenUpperBound > selectedItem.TokenUpperBound {
		return apperror.New(apperror.CodeFailedPrecondition,
			"external Specialist Skill context exceeds its pinned item")
	}
	existing, found, err := getExternalSpecialistContextCommitTx(ctx, tx, preparation.ID)
	if err != nil {
		return err
	}
	if found {
		if existing.RunID != run.ID || existing.AgentAttemptID != attempt.ID ||
			existing.ModelAttempt <= 0 || existing.ModelAttempt > modelAttempt {
			return apperror.New(apperror.CodeConflict,
				"external Specialist Skill context commit does not match model attempt")
		}
		return nil
	}
	commit := skills.ExternalSpecialistContextCommit{
		PreparationID: preparation.ID, RunID: run.ID, AgentAttemptID: attempt.ID,
		ModelAttempt: modelAttempt, CommittedAt: time.Now().UTC(),
	}
	if err := commit.Validate(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_external_skill_context_commits
		(preparation_id, run_id, agent_attempt_id, model_attempt, committed_at)
		VALUES (?, ?, ?, ?, ?)`, commit.PreparationID, commit.RunID,
		commit.AgentAttemptID, commit.ModelAttempt, ts(commit.CommittedAt)); err != nil {
		return err
	}
	return appendSupervisorEventTx(ctx, tx, run,
		events.ExternalSpecialistSkillContextCommittedEvent,
		"external_specialist_skills", preparation.ID,
		externalSpecialistContextEventPayload(preparation, modelAttempt))
}

func validateExternalSpecialistContextBinding(
	request skills.ExternalSpecialistContextPreparationRequest,
	selection skills.ExternalSelection, mode domain.RunModeSnapshot,
	attempt domain.AgentAttempt, child, parent domain.AgentNode, run domain.Run,
) error {
	if err := request.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"external Specialist Skill context request is invalid", err)
	}
	if err := selection.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable external Skill selection is invalid", err)
	}
	selected, found := skills.ExternalSpecialistItem(selection)
	if !found {
		return apperror.New(apperror.CodeFailedPrecondition,
			"external selection has no Specialist-eligible Skill")
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
		selection.RunID != run.ID || selection.MissionID != run.MissionID ||
		selection.Surface != request.Surface || selection.Profile != request.Profile ||
		selection.ID != request.ParentSelectionID ||
		selection.Fingerprint != request.ParentSelectionFingerprint ||
		mode.RunID != run.ID || mode.MissionID != run.MissionID ||
		mode.ID != request.ModeSnapshotID || mode.Revision != request.ModeRevision ||
		mode.Surface != request.Surface || mode.Profile != request.Profile ||
		child.Profile != request.Profile || request.ItemCount != 1 ||
		request.TokenUpperBound <= 0 || request.TokenUpperBound > selected.TokenUpperBound ||
		request.AssignmentFingerprint != skills.SpecialistAssignmentFingerprint(child) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"external Specialist Skill context provenance does not match durable Run state")
	}
	return nil
}

func externalSpecialistContextEventPayload(
	preparation skills.ExternalSpecialistContextPreparation, modelAttempt int,
) map[string]any {
	request := preparation.ExternalSpecialistContextPreparationRequest
	return map[string]any{
		"protocol": request.ProtocolVersion, "agent_id": request.AgentID,
		"parent_agent_id":  request.ParentAgentID,
		"agent_attempt_id": request.AgentAttemptID, "turn": request.Turn,
		"surface": request.Surface, "profile": request.Profile,
		"mode_revision": request.ModeRevision, "item_count": request.ItemCount,
		"token_budget":      request.TokenBudget,
		"token_upper_bound": request.TokenUpperBound,
		"redaction_count":   request.RedactionCount, "model_attempt": modelAttempt,
		"trust_class":       skills.PackageTrustOperatorInstalledUntrusted,
		"context_injection": modelAttempt > 0, "tool_capability_grant": false,
	}
}

func getExternalSpecialistContextPreparationByAttemptTx(ctx context.Context,
	tx *sql.Tx, attemptID string,
) (skills.ExternalSpecialistContextPreparation, bool, error) {
	value, err := scanExternalSpecialistContextPreparation(tx.QueryRowContext(ctx,
		externalSpecialistContextPreparationSelect+` WHERE agent_attempt_id = ?`, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return skills.ExternalSpecialistContextPreparation{}, false, nil
	}
	return value, err == nil, err
}

func getExternalSpecialistContextCommitTx(ctx context.Context, tx *sql.Tx,
	preparationID string,
) (skills.ExternalSpecialistContextCommit, bool, error) {
	value, err := scanExternalSpecialistContextCommit(tx.QueryRowContext(ctx,
		externalSpecialistContextCommitSelect+` WHERE preparation_id = ?`, preparationID))
	if errors.Is(err, sql.ErrNoRows) {
		return skills.ExternalSpecialistContextCommit{}, false, nil
	}
	return value, err == nil, err
}

func scanExternalSpecialistContextPreparation(row scanner) (
	skills.ExternalSpecialistContextPreparation, error,
) {
	var value skills.ExternalSpecialistContextPreparation
	request := &value.ExternalSpecialistContextPreparationRequest
	var preparedAt string
	if err := row.Scan(&value.ID, &request.RunID, &request.MissionID,
		&request.AgentID, &request.ParentAgentID, &request.AgentAttemptID, &request.Turn,
		&request.ParentSelectionID, &request.ProtocolVersion,
		&request.ParentSelectionFingerprint, &request.ModeSnapshotID,
		&request.ModeRevision, &request.Surface, &request.Profile,
		&request.AssignmentFingerprint, &request.ContextFingerprint, &request.ItemCount,
		&request.TokenBudget, &request.TokenUpperBound, &request.RedactionCount,
		&preparedAt); err != nil {
		return skills.ExternalSpecialistContextPreparation{}, err
	}
	value.PreparedAt = parseTS(preparedAt)
	return value, value.Validate()
}

func scanExternalSpecialistContextCommit(row scanner) (
	skills.ExternalSpecialistContextCommit, error,
) {
	var value skills.ExternalSpecialistContextCommit
	var committedAt string
	if err := row.Scan(&value.PreparationID, &value.RunID, &value.AgentAttemptID,
		&value.ModelAttempt, &committedAt); err != nil {
		return skills.ExternalSpecialistContextCommit{}, err
	}
	value.CommittedAt = parseTS(committedAt)
	return value, value.Validate()
}
