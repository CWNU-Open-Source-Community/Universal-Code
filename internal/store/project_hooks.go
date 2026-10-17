package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (s *SQLiteStore) GetProjectHookTrust(ctx context.Context, workspaceID string) (domain.ProjectHookTrust, bool, error) {
	return getProjectHookTrust(ctx, s.db, workspaceID)
}

type projectHookQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getProjectHookTrust(ctx context.Context, q projectHookQuerier, workspaceID string) (domain.ProjectHookTrust, bool, error) {
	var result domain.ProjectHookTrust
	var stamp string
	err := q.QueryRowContext(ctx, `SELECT workspace_id,root_fingerprint,fingerprint,trusted,revision,updated_at FROM project_hook_trust WHERE workspace_id=?`, workspaceID).
		Scan(&result.WorkspaceID, &result.RootFingerprint, &result.Fingerprint, &result.Trusted, &result.Revision, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	result.UpdatedAt, err = time.Parse(time.RFC3339Nano, stamp)
	return result, true, err
}

func (s *SQLiteStore) ReviewProjectHooks(ctx context.Context, value domain.ProjectHookTrust, expectedRevision int64) (domain.ProjectHookTrust, error) {
	validHash := func(s string) bool {
		b, err := hex.DecodeString(s)
		return err == nil && len(b) == 32 && strings.ToLower(s) == s
	}
	if !domain.ValidAgentID(value.WorkspaceID) || !validHash(value.RootFingerprint) || !validHash(value.Fingerprint) || expectedRevision < 0 {
		return domain.ProjectHookTrust{}, apperror.New(apperror.CodeInvalidArgument, "project hook review binding is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectHookTrust{}, err
	}
	defer tx.Rollback()
	current, found, err := getProjectHookTrust(ctx, tx, value.WorkspaceID)
	if err != nil {
		return domain.ProjectHookTrust{}, err
	}
	if found && current.RootFingerprint == value.RootFingerprint && current.Fingerprint == value.Fingerprint && current.Trusted == value.Trusted {
		return current, nil
	}
	if current.Revision != expectedRevision {
		return domain.ProjectHookTrust{}, apperror.New(apperror.CodeConflict, "project hook review changed; refresh before confirming")
	}
	value.Revision = expectedRevision + 1
	value.UpdatedAt = time.Now().UTC()
	if !found {
		_, err = tx.ExecContext(ctx, `INSERT INTO project_hook_trust(workspace_id,root_fingerprint,fingerprint,trusted,revision,updated_at) VALUES(?,?,?,?,?,?)`,
			value.WorkspaceID, value.RootFingerprint, value.Fingerprint, value.Trusted, value.Revision, value.UpdatedAt.Format(time.RFC3339Nano))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE project_hook_trust SET root_fingerprint=?,fingerprint=?,trusted=?,revision=?,updated_at=? WHERE workspace_id=? AND revision=?`,
			value.RootFingerprint, value.Fingerprint, value.Trusted, value.Revision, value.UpdatedAt.Format(time.RFC3339Nano), value.WorkspaceID, expectedRevision)
	}
	if err != nil {
		return domain.ProjectHookTrust{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.ProjectHookTrust{}, err
	}
	return value, nil
}
