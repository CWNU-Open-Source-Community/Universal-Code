package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func (s *SQLiteStore) GetStandardCodeContinuation(ctx context.Context, runID string) (*domain.StandardCodeContinuation, error) {
	return getStandardCodeContinuation(ctx, s.db, runID)
}

// Only the published adjacent Thread binding can supply initial observations.
// The physical directory is validated separately by the application resolver.
func getStandardCodeContinuation(ctx context.Context, q drydockQueryer, runID string) (*domain.StandardCodeContinuation, error) {
	c := &domain.StandardCodeContinuation{}
	var threadID, missionID string
	err := q.QueryRowContext(ctx, `SELECT binding.predecessor_run_id,binding.drydock_id,d.workspace_id,binding.thread_id,r.mission_id
 FROM thread_drydock_bindings binding JOIN runs r ON r.id=binding.run_id
 JOIN thread_runs current ON current.run_id=binding.run_id
 JOIN thread_runs previous ON previous.run_id=binding.predecessor_run_id AND previous.thread_id=current.thread_id AND previous.ordinal+1=current.ordinal
 JOIN runs old ON old.id=previous.run_id AND old.mission_id=r.mission_id AND old.status IN ('completed','failed','cancelled')
 JOIN run_file_drydock_bindings prior ON prior.run_id=old.id AND prior.drydock_id=binding.drydock_id
 JOIN drydock_workspaces d ON d.id=binding.drydock_id
 WHERE binding.run_id=? AND binding.thread_id=current.thread_id AND current.predecessor_run_id=old.id`, runID).
		Scan(&c.PredecessorRunID, &c.DrydockID, &c.WorkspaceID, &threadID, &missionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	previous, err := scanStandardCodeSupervisorLedgerEntry(q.QueryRowContext(ctx,
		standardCodeSupervisorLedgerSelect+` WHERE run_id=? ORDER BY snapshot_version DESC LIMIT 1`, c.PredecessorRunID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c.SnapshotVersion = previous.Snapshot.Version
	c.ConsecutiveReadRounds = previous.Snapshot.ConsecutiveReadRounds
	c.MutationEpoch = previous.Snapshot.MutationEpoch
	if c.MutationEpoch == 0 {
		return c, c.Validate()
	}
	mutation, err := scanStandardCodeSupervisorLedgerEntry(q.QueryRowContext(ctx,
		standardCodeSupervisorLedgerSelect+` WHERE run_id=? AND kind IN ('call_observed','phase_advanced')
 AND tool_kind='workspace_mutation' AND reason_code='workspace_mutation_checkpoint_verified'
 AND CAST(json_extract(snapshot_json,'$.mutation_epoch') AS INTEGER)=? ORDER BY snapshot_version DESC LIMIT 1`, c.PredecessorRunID, c.MutationEpoch))
	if errors.Is(err, sql.ErrNoRows) && previous.Snapshot.Continuation != nil {
		origin := previous.Snapshot.Continuation
		if origin.MutationEpoch != c.MutationEpoch {
			return nil, invalidStandardCodeContinuation()
		}
		mutation, err = scanStandardCodeSupervisorLedgerEntry(q.QueryRowContext(ctx,
			standardCodeSupervisorLedgerSelect+` WHERE run_id=? AND snapshot_version=?`, origin.MutationRunID, origin.MutationSnapshotVersion))
	}
	if err != nil {
		return nil, errors.Join(err, invalidStandardCodeContinuation())
	}
	validToolCall := mutation.Kind == domain.StandardCodeSupervisorCallObserved && mutation.ResultStatus == domain.SupervisorToolCompleted
	validDirectApply := mutation.Kind == domain.StandardCodeSupervisorPhaseAdvanced && strings.HasPrefix(mutation.ToolAction, "direct_apply:")
	if (!validToolCall && !validDirectApply) ||
		mutation.ToolKind != domain.StandardCodeToolWorkspaceMutation ||
		mutation.ReasonCode != "workspace_mutation_checkpoint_verified" ||
		mutation.Snapshot.MissionID != missionID || mutation.Snapshot.MutationEpoch != c.MutationEpoch {
		return nil, invalidStandardCodeContinuation()
	}
	var exact bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thread_runs origin
 JOIN thread_runs previous ON previous.run_id=? AND previous.thread_id=origin.thread_id AND previous.ordinal>=origin.ordinal
 JOIN run_file_drydock_bindings physical ON physical.run_id=origin.run_id
 WHERE origin.run_id=? AND origin.thread_id=? AND physical.drydock_id=?)`,
		c.PredecessorRunID, mutation.Snapshot.RunID, threadID, c.DrydockID).Scan(&exact); err != nil {
		return nil, err
	}
	if !exact {
		return nil, invalidStandardCodeContinuation()
	}
	// An observed mutation must still name the exact completed FileTool journal
	// and sealed after checkpoint, not merely an old nonzero counter.
	var checkpointID string
	if validToolCall {
		if err := q.QueryRowContext(ctx, `SELECT cp.id FROM workspace_checkpoints cp
 JOIN workspace_checkpoint_transactions journal ON journal.after_checkpoint_id=cp.id
 JOIN run_supervisor_tool_calls call ON call.run_id=journal.run_id AND call.attempt_id=cp.attempt_id
 AND call.turn=? AND call.call_id=? AND call.status='completed'
 AND (
   (call.tool_name='workspace_apply' AND json_extract(call.payload_json,'$.edit_id')=journal.trigger_receipt_id)
   OR (call.tool_name='workspace_change' AND json_extract(call.result_json,'$.metadata.status')='applied' AND json_extract(call.result_json,'$.metadata.edit_id')=journal.trigger_receipt_id)
 )
 WHERE journal.run_id=? AND journal.workspace_id=? AND journal.kind='file_tool' AND journal.status='completed'
 AND cp.run_id=journal.run_id AND cp.mission_id=? AND cp.workspace_id=journal.workspace_id
 AND cp.attempt_id=? AND cp.capability_generation=? AND cp.phase='after' AND cp.sealed=1
 AND cp.trigger_receipt_id=journal.trigger_receipt_id AND cp.root_fingerprint=?
 ORDER BY cp.created_at DESC LIMIT 1`, mutation.Snapshot.Turn, mutation.ToolCallID, mutation.Snapshot.RunID, c.WorkspaceID, missionID,
			mutation.Snapshot.AttemptID, mutation.Snapshot.CapabilityGeneration, mutation.Snapshot.WorkspaceRootFingerprint).Scan(&checkpointID); err != nil {
			return nil, errors.Join(err, invalidStandardCodeContinuation())
		}
	} else {
		editID := strings.TrimPrefix(mutation.ToolAction, "direct_apply:")
		if editID == "" {
			return nil, invalidStandardCodeContinuation()
		}
		if err := q.QueryRowContext(ctx, `SELECT cp.id FROM workspace_checkpoints cp
 JOIN workspace_checkpoint_transactions journal ON journal.after_checkpoint_id=cp.id
 JOIN file_edits fe ON fe.id=journal.trigger_receipt_id AND fe.status='applied'
 JOIN tool_approvals ap ON ap.proposal_id=fe.id AND ap.run_id=journal.run_id AND ap.session_id=fe.session_id AND ap.workspace_id=fe.workspace_id AND ap.action_class='workspace_write' AND ap.status='approved'
 WHERE journal.run_id=? AND journal.workspace_id=? AND journal.kind='file_tool' AND journal.status='completed'
 AND journal.trigger_receipt_id=?
 AND cp.run_id=journal.run_id AND cp.mission_id=? AND cp.workspace_id=journal.workspace_id
 AND cp.attempt_id IN (?,'') AND cp.phase='after' AND cp.sealed=1
 AND cp.trigger_receipt_id=journal.trigger_receipt_id AND cp.root_fingerprint=?
 ORDER BY cp.created_at DESC LIMIT 1`, mutation.Snapshot.RunID, c.WorkspaceID, editID, missionID,
			mutation.Snapshot.AttemptID, mutation.Snapshot.WorkspaceRootFingerprint).Scan(&checkpointID); err != nil {
			return nil, errors.Join(err, invalidStandardCodeContinuation())
		}
	}
	cp, err := getWorkspaceCheckpoint(ctx, q, checkpointID)
	if err != nil {
		return nil, err
	}
	txn, err := scanWorkspaceCheckpointTransaction(q.QueryRowContext(ctx,
		`SELECT `+workspaceCheckpointTransactionColumns+` FROM workspace_checkpoint_transactions WHERE after_checkpoint_id=? AND kind='file_tool' AND status='completed'`, checkpointID))
	if err != nil {
		return nil, err
	}
	fingerprint := runmutation.Fingerprint("standard_code_mutation_checkpoint.v1", txn.TriggerReceiptID, txn.ID,
		txn.BeforeCheckpointID, txn.AfterCheckpointID, cp.RootFingerprint, mutation.Snapshot.ExpectedCapabilityGeneration)
	if cp.Phase != workspacecheckpoint.PhaseAfter || fingerprint != mutation.EvidenceFingerprint || fingerprint != mutation.Snapshot.LastEvidenceFingerprint {
		return nil, invalidStandardCodeContinuation()
	}
	c.MutationRunID, c.MutationSnapshotVersion, c.MutationCheckpointID = mutation.Snapshot.RunID, mutation.Snapshot.Version, cp.ID
	return c, c.Validate()
}

func invalidStandardCodeContinuation() error {
	return apperror.New(apperror.CodeFailedPrecondition, "The previous coding mutation has no exact retained checkpoint observation")
}

func requireStandardCodeContinuationTx(ctx context.Context, tx *sql.Tx, entry domain.StandardCodeSupervisorLedgerEntry) error {
	c := entry.Snapshot.Continuation
	if entry.Snapshot.Version > 1 {
		previous, err := scanStandardCodeSupervisorLedgerEntry(tx.QueryRowContext(ctx,
			standardCodeSupervisorLedgerSelect+` WHERE run_id=? AND snapshot_version=?`, entry.Snapshot.RunID, entry.Snapshot.Version-1))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(c, previous.Snapshot.Continuation) {
			return invalidStandardCodeContinuation()
		}
		return nil
	}
	if c == nil {
		return nil
	}
	exact, err := getStandardCodeContinuation(ctx, tx, entry.Snapshot.RunID)
	if err != nil {
		return err
	}
	s := entry.Snapshot
	if !reflect.DeepEqual(c, exact) || s.MutationEpoch != c.MutationEpoch || s.ConsecutiveReadRounds != c.ConsecutiveReadRounds ||
		s.VerifiedMutationEpoch != 0 || len(s.Jobs) != 0 || len(s.VerificationJobIDs) != 0 || s.DeliveryID != "" ||
		s.CommandsUsed != 0 || s.JobsStarted != 0 || s.FixRounds != 0 || s.OutputBytes != 0 || s.TotalToolRounds != 0 {
		return invalidStandardCodeContinuation()
	}
	return nil
}
