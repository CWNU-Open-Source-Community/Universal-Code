package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

// Restart recovery records the actual selected paths and releases the existing
// execution reservation. It never guesses that a partial undo succeeded.
func (s *WorkspaceCheckpointService) reconcileThreadChangeUndo(ctx context.Context, binding workspaceCheckpointBinding, t workspacecheckpoint.Transaction) error {
	before, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, t.BeforeCheckpointID)
	if err != nil {
		return apperror.Normalize(err)
	}
	paths := map[string]threadUndoPath{}
	for _, e := range before.Entries {
		paths[e.Path] = threadUndoPath{}
	}
	after, err := workspacecheckpoint.Capture(ctx, s.workspaceCaptureRequest(binding, workspacecheckpoint.CaptureRequest{
		ID: idgen.New("thread-undo-recovered"), Trigger: workspacecheckpoint.TriggerRewindResult, Phase: workspacecheckpoint.PhaseAfter,
		TriggerReceiptID: t.ID, CreatedAt: time.Now().UTC()}))
	if err != nil {
		return apperror.Normalize(err)
	}
	if err := validateThreadUndoObservation(binding.workspace.RootPath, after, paths); err != nil {
		return apperror.Normalize(err)
	}
	after = scopeThreadUndoSnapshot(after, paths)
	if _, _, err = s.store.CreateWorkspaceCheckpoint(ctx, after); err != nil {
		return apperror.Normalize(err)
	}
	t.Status = workspacecheckpoint.TransactionInterrupted
	t.ErrorCode = "process_restart_reconciliation"
	t.AfterCheckpointID = after.Checkpoint.ID
	t.UpdatedAt = time.Now().UTC()
	t.CompletedAt = &t.UpdatedAt
	_, _, err = s.store.UpdateWorkspaceCheckpointTransaction(ctx, t)
	return apperror.Normalize(err)
}
