package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func readThreadCommandServicePolicy(ctx context.Context, db commandRuntimeJobQueryer, threadID string) (domain.ThreadCommandServicePolicy, error) {
	result := domain.ThreadCommandServicePolicy{Version: domain.ThreadCommandServicePolicyVersion, ThreadID: threadID}
	if !domain.ValidAgentID(threadID) {
		return result, apperror.New(apperror.CodeInvalidArgument, "Thread service policy identity is invalid")
	}
	err := db.QueryRowContext(ctx, `SELECT COALESCE(preference.policy,'auto'),COALESCE(preference.revision,1)
      FROM threads thread LEFT JOIN thread_command_service_policies preference ON preference.thread_id=thread.id
      WHERE thread.id=? AND thread.status<>'deleted'`, threadID).Scan(&result.Policy, &result.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return result, apperror.New(apperror.CodeNotFound, "Thread service policy was not found")
	}
	return result, err
}

func (s *SQLiteStore) GetThreadCommandServicePolicy(ctx context.Context, threadID string) (domain.ThreadCommandServicePolicy, error) {
	return readThreadCommandServicePolicy(ctx, s.db, threadID)
}

func (s *SQLiteStore) SaveThreadCommandServicePolicy(ctx context.Context, threadID string, expectedRevision int64, policy domain.CommandServicePolicy) (domain.ThreadCommandServicePolicy, error) {
	if !policy.Valid() || expectedRevision < 1 {
		return domain.ThreadCommandServicePolicy{}, apperror.New(apperror.CodeInvalidArgument, "Thread service policy or revision is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ThreadCommandServicePolicy{}, err
	}
	defer tx.Rollback()
	current, err := readThreadCommandServicePolicy(ctx, tx, threadID)
	if err != nil {
		return current, err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM threads WHERE id=?`, threadID).Scan(&status); err != nil {
		return current, err
	}
	if status != string(domain.ThreadActive) {
		return current, apperror.New(apperror.CodeFailedPrecondition, "service preferences require an active Thread")
	}
	if current.Revision != expectedRevision {
		if expectedRevision < math.MaxInt64 && current.Revision == expectedRevision+1 && current.Policy == policy {
			return current, tx.Commit()
		}
		return current, apperror.New(apperror.CodeConflict, "service preference changed; read its current revision before saving")
	}
	if current.Policy == policy {
		return current, tx.Commit()
	}
	if current.Revision == math.MaxInt64 {
		return current, apperror.New(apperror.CodeResourceExhausted, "service preference revision is exhausted")
	}
	current.Policy, current.Revision = policy, current.Revision+1
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_command_service_policies(thread_id,policy,revision,updated_at)
      VALUES(?,?,?,?) ON CONFLICT(thread_id) DO UPDATE SET policy=excluded.policy,
      revision=excluded.revision,updated_at=excluded.updated_at`, threadID, policy, current.Revision, ts(time.Now().UTC()))
	if err != nil {
		return current, err
	}
	return current, tx.Commit()
}

// Read at dispatch and each owner renewal, so a saved exclusive preference
// revokes live shared-service continuation for root and worker Jobs alike.
func (s *SQLiteStore) SharedCommandServicesAllowed(ctx context.Context, runID string) (bool, error) {
	if !domain.ValidAgentID(runID) {
		return false, apperror.New(apperror.CodeInvalidArgument, "service policy Run identity is invalid")
	}
	var policy domain.CommandServicePolicy
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(preference.policy,'auto')
      FROM thread_runs binding JOIN threads thread ON thread.id=binding.thread_id
      LEFT JOIN thread_command_service_policies preference ON preference.thread_id=binding.thread_id
      WHERE binding.run_id=? AND thread.status<>'deleted'`, runID).Scan(&policy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, apperror.New(apperror.CodeNotFound, "service policy Run was not found")
	}
	return policy == domain.CommandServicePolicyAuto, err
}
