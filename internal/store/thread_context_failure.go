package store

import (
	"context"
	"database/sql"
	"errors"

	"cyberagent-workbench/internal/domain"
)

// These gates are Go-owned and run before the next model request. Bind their
// label to the exact sealed handoff; provider messages and generic budget
// exhaustion cannot manufacture this failure stage.
func recordedThreadHandoffFailureStage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, handoffID string) (string, error) {
	var code, reason string
	err := q.QueryRowContext(ctx, `SELECT UPPER(error_code),stop_reason FROM run_execution_handoff_results
		WHERE operation_id=? AND status='failed'`, handoffID).Scan(&code, &reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && code == "RESOURCE_EXHAUSTED" && reason == domain.ThreadFailureContextWindowExceeded {
		return domain.ThreadFailureContextWindowExceeded, nil
	}
	if err == nil && code == "FAILED_PRECONDITION" && reason == domain.ThreadFailureFullPermissionNotReady {
		return domain.ThreadFailureFullPermissionNotReady, nil
	}
	return "", nil
}
