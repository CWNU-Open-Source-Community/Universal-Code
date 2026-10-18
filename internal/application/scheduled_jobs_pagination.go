package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// ListPage is the bounded keyset reader; List keeps its existing first-page
// contract for CLI and other application callers.
func (s *ScheduledJobService) ListPage(ctx context.Context, runID string,
	beforeCreatedAt time.Time, beforeID string, limit int,
) ([]domain.ScheduledJob, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "scheduled job store is required")
	}
	reader, ok := s.store.(interface {
		ListScheduledJobsByCreationPage(context.Context, string, time.Time, string, int) ([]domain.ScheduledJob, error)
	})
	if !ok {
		return nil, apperror.New(apperror.CodeUnavailable, "scheduled job pagination is unavailable")
	}
	items, err := reader.ListScheduledJobsByCreationPage(ctx, runID, beforeCreatedAt, beforeID, limit)
	return items, apperror.Normalize(err)
}
