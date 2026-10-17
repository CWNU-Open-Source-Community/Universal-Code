package store

import (
	"context"

	"cyberagent-workbench/internal/domain"
)

// GetHistoricalSpecialistApprovalCall reads the immutable source identity and
// dispatch receipt for cleanup. It grants no execution or review authority and
// intentionally remains readable after permission, lease or child revocation.
func (s *SQLiteStore) GetHistoricalSpecialistApprovalCall(ctx context.Context, runID, sourceID string) (domain.SpecialistToolCall, bool, error) {
	call, err := scanSpecialistToolCall(s.db.QueryRowContext(ctx, specialistToolCallSelect+` WHERE a.run_id=? AND c.approval_source_id=?`, runID, sourceID))
	return call, call.StartedAt != nil, err
}
