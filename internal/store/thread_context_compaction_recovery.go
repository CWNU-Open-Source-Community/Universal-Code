package store

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (s *SQLiteStore) ListThreadContextCompactions(ctx context.Context, threadID string) (domain.ThreadContextCompactionOperations, error) {
	view := domain.ThreadContextCompactionOperations{Version: domain.ThreadContextCompactionOperationsVersion,
		ThreadID: threadID, Operations: []domain.ThreadContextCompactionOperationView{}}
	if _, err := s.GetThread(ctx, threadID); err != nil {
		return view, apperror.Normalize(err)
	}
	rows, err := s.db.QueryContext(ctx, threadContextOperationSelect+" WHERE thread_id=? ORDER BY status IN ('prepared','started','generated') DESC,id DESC LIMIT 20", threadID)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		o, err := scanThreadContextOperation(rows)
		if err != nil {
			return view, err
		}
		view.Operations = append(view.Operations, o.View())
	}
	return view, rows.Err()
}

// Recovery is bound to a durable operation, not the browser's lost request key.
// Discard and provider completion serialize in this transaction. Unknown sent
// usage remains reserved; a later receipt can settle it but cannot revive work.
func (s *SQLiteStore) RecoverThreadContextCompaction(ctx context.Context, request domain.ThreadContextCompactionRecoveryRequest) (domain.ThreadContextCompactionOperation, domain.ThreadContextCompactionSnapshot, error) {
	var snap domain.ThreadContextCompactionSnapshot
	request, err := request.Normalize()
	if err != nil {
		return domain.ThreadContextCompactionOperation{}, snap, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ThreadContextCompactionOperation{}, snap, err
	}
	defer tx.Rollback()
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE thread_id=? AND operation_id=?", request.ThreadID, request.OperationID))
	if err != nil {
		return o, snap, apperror.Normalize(err)
	}
	if o.RunID != request.ExpectedRunID {
		return o, snap, apperror.New(apperror.CodeConflict, "thread_context_source_changed: recovery Run does not match the operation")
	}
	if o.Status == "completed" || o.Status == "noop" {
		o.Result.Replayed = true
		return o, snap, tx.Commit()
	}
	if request.Action == "resume" {
		if o.Status != "generated" {
			return o, snap, apperror.New(apperror.CodeConflict, "thread_context_operation_pending: no saved summary is available; inspect or discard this operation before starting another")
		}
		snap, err = readThreadContextSnapshot(ctx, tx, o.ThreadID, o.RunID)
		if err != nil {
			return o, snap, err
		}
		if snap.SourceSHA256 != o.SourceSHA256 {
			return o, snap, apperror.New(apperror.CodeConflict, "thread_context_source_changed: the saved summary no longer matches current history; discard it")
		}
		if err = requireThreadContextIdleTx(ctx, tx, o.RunID); err != nil {
			return o, snap, err
		}
	} else if o.Status != "failed" {
		previous := o.Status
		if _, err = tx.ExecContext(ctx, "UPDATE thread_context_compactions SET status='failed',error_code=?,error_message=?,terminal_at=COALESCE(terminal_at,?) WHERE id=?", apperror.CodeCancelled, domain.ThreadContextCompactionDiscarded, ts(time.Now().UTC()), o.ID); err != nil {
			return o, snap, err
		}
		run, err := getRunControlRunTx(ctx, tx, o.RunID)
		if err != nil {
			return o, snap, err
		}
		if err = appendSupervisorEventTx(ctx, tx, run, "context_compaction.discarded", "thread_context_compaction", o.OperationID,
			map[string]any{"operation_id": o.OperationID, "previous_status": previous, "requested_by": request.RequestedBy, "usage_unknown": o.StartedAt != nil && o.CompletionSequence == 0}); err != nil {
			return o, snap, err
		}
		o, err = scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE id=?", o.ID))
		if err != nil {
			return o, snap, err
		}
	}
	if err = reconcileMonetaryReservationsTx(ctx, tx, o.RunID); err != nil {
		return o, snap, err
	}
	return o, snap, tx.Commit()
}
