package store

import (
	"context"
	"database/sql"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

// materializeThreadExecutionSettingsTx preserves the operator's backend and
// interaction intent in a plain source-workspace successor. The caller has
// validated the Thread ancestry in this transaction. Drydock/Standard Code
// continuations use their existing directory-bound configuration path instead.
// These snapshots cannot grant execution; approvals, Session grants, processes
// and leases are deliberately not read or copied here.
func materializeThreadExecutionSettingsTx(ctx context.Context, tx *sql.Tx,
	thread domain.Thread, predecessor, candidate domain.Run, mode domain.RunModeSnapshot,
) (map[string]any, error) {
	var exact int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_runs old
		JOIN thread_runs next ON next.thread_id=old.thread_id AND next.predecessor_run_id=old.run_id
		JOIN sessions old_session ON old_session.id=old.session_id
		JOIN sessions next_session ON next_session.id=next.session_id
		WHERE old.thread_id=? AND old.run_id=? AND old.session_id=?
		AND next.run_id=? AND next.session_id=?
		AND old_session.workspace_id=? AND next_session.workspace_id=?`,
		thread.ID, predecessor.ID, predecessor.SessionID, candidate.ID, candidate.SessionID,
		thread.WorkspaceID, thread.WorkspaceID).Scan(&exact); err != nil {
		return nil, err
	}
	if exact != 1 || candidate.MissionID != predecessor.MissionID ||
		candidate.MissionID != thread.MissionID || !predecessor.Terminal() ||
		candidate.Status != domain.RunCreated || mode.RunID != candidate.ID {
		return nil, apperror.New(apperror.CodeConflict, "Thread execution settings continuation scope changed")
	}
	previousProfile, err := getCurrentRunExecutionProfileSnapshot(ctx, tx, predecessor.ID)
	if err != nil {
		return nil, err
	}
	previousInteraction, err := getCurrentRunExecutionInteractionSnapshot(ctx, tx, predecessor.ID)
	if err != nil {
		return nil, err
	}
	profile, err := getCurrentRunExecutionProfileSnapshot(ctx, tx, candidate.ID)
	if err != nil {
		return nil, err
	}
	if profile.Profile != previousProfile.Profile ||
		(profile.IsDefaultPreview() && previousProfile.Profile == domain.RunExecutionProfilePreview && !previousProfile.IsDefaultPreview()) {
		profile, err = profile.Next(idgen.New("run-exec-profile"), previousProfile.Profile,
			"thread_continuation", "Continue the selected project backend; execution requires current authority", candidate.CreatedAt)
		if err != nil {
			return nil, err
		}
		if err := insertRunExecutionProfileSnapshotTx(ctx, tx, profile); err != nil {
			return nil, err
		}
	}
	interaction, err := getCurrentRunExecutionInteractionSnapshot(ctx, tx, candidate.ID)
	if err != nil {
		return nil, err
	}
	// A later backend/surface edit invalidates an older trust confirmation. Do
	// not revive it merely because the backend eventually has the same name.
	confirmedBinding := thread.WorkspaceID != "" &&
		previousInteraction.Surface == mode.Surface &&
		previousInteraction.ExecutionProfile == previousProfile.Profile &&
		previousInteraction.ExecutionProfileRevision == previousProfile.Revision
	desiredMode, desiredTrust, confirmed := domain.RunExecutionInteractionPreview, domain.WorkspaceTrustUntrusted, false
	if confirmedBinding {
		desiredMode, desiredTrust, confirmed = previousInteraction.Mode, previousInteraction.WorkspaceTrust, previousInteraction.OperatorConfirmed
	}
	requiresConfirmation := !confirmedBinding && previousInteraction.Mode != domain.RunExecutionInteractionPreview
	reason := "Continue the confirmed project interaction preference; runtime authority is not inherited"
	if requiresConfirmation {
		reason = "Execution settings changed since workspace confirmation; review and confirm the current interaction settings"
	}
	if interaction.Mode != desiredMode || interaction.ExecutionProfileRevision != profile.Revision {
		interaction, err = interaction.Next(idgen.New("run-exec-interaction"), desiredMode,
			mode, profile, desiredTrust, confirmed, "thread_continuation", reason, candidate.CreatedAt)
		if err != nil {
			return nil, err
		}
		if err := insertRunExecutionInteractionSnapshotTx(ctx, tx, interaction); err != nil {
			return nil, err
		}
	}
	// The enclosing successor event is the atomic provenance receipt. There
	// is no replayable operation that could transfer the old authorization.
	return map[string]any{
		"execution_profile_source_snapshot_id":     previousProfile.ID,
		"execution_profile_snapshot_id":            profile.ID,
		"execution_profile":                        profile.Profile,
		"execution_interaction_source_snapshot_id": previousInteraction.ID,
		"execution_interaction_snapshot_id":        interaction.ID,
		"execution_interaction":                    interaction.Mode,
		"execution_settings_require_confirmation":  requiresConfirmation,
	}, nil
}
