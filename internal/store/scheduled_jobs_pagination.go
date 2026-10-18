package store

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// ListScheduledJobsByCreationPage uses immutable creation order so later
// schedules and status changes do not shift a continuation page.
func (s *SQLiteStore) ListScheduledJobsByCreationPage(ctx context.Context, runID string,
	beforeCreatedAt time.Time, beforeID string, limit int,
) ([]domain.ScheduledJob, error) {
	runID = strings.TrimSpace(runID)
	if runID != "" && (!domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0)) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "scheduled job Run filter is invalid")
	}
	if err := validateStoreCreationPage(beforeCreatedAt, beforeID, limit); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "scheduled job page is invalid", err)
	}
	query := scheduledJobSelect + `WHERE 1=1`
	var args []any
	if runID != "" {
		query += ` AND owner_run_id = ?`
		args = append(args, runID)
	}
	createdKey := creationTimestampKeySQL("scheduled_jobs.created_at")
	if !beforeCreatedAt.IsZero() {
		query += ` AND (` + createdKey + ` < ? OR (` + createdKey + ` = ? AND id < ?))`
		anchor := creationTimestampKey(beforeCreatedAt)
		args = append(args, anchor, anchor, beforeID)
	}
	query += ` ORDER BY ` + createdKey + ` DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ScheduledJob, 0, limit)
	for rows.Next() {
		record, err := scanScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, record.Job)
	}
	return items, rows.Err()
}
