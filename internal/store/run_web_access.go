package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/webevidence"
)

func (s *SQLiteStore) GetRunWebAccessOperation(ctx context.Context, key string) (domain.RunWebAccessOperation, bool, error) {
	if !validStoreDigest(key) {
		return domain.RunWebAccessOperation{}, false, apperror.New(apperror.CodeInvalidArgument, "web access operation digest is invalid")
	}
	return getRunWebAccessOperation(ctx, s.db, key)
}

func getRunWebAccessOperation(ctx context.Context, q runModeQueryer, key string) (domain.RunWebAccessOperation, bool, error) {
	var operation domain.RunWebAccessOperation
	var at string
	err := q.QueryRowContext(ctx, `SELECT operation_key_digest,request_fingerprint,snapshot_id,run_id,expected_mode_revision,requested_by,created_at FROM run_web_access_operations WHERE operation_key_digest=?`, key).Scan(&operation.KeyDigest, &operation.RequestFingerprint, &operation.SnapshotID, &operation.RunID, &operation.ExpectedModeRevision, &operation.RequestedBy, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return operation, false, nil
	}
	if err != nil {
		return operation, false, err
	}
	operation.CreatedAt = parseTS(at)
	return operation, true, operation.Validate()
}

func webAccessOperationBinding(operation domain.RunWebAccessOperation, snapshot domain.RunModeSnapshot) bool {
	return operation.SnapshotID == snapshot.ID && operation.RunID == snapshot.RunID && operation.ExpectedModeRevision+1 == snapshot.Revision && operation.RequestedBy == snapshot.RequestedBy && operation.CreatedAt.Equal(snapshot.CreatedAt) && operation.RequestFingerprint == domain.RunWebAccessFingerprint(snapshot.RunID, operation.ExpectedModeRevision, snapshot.Scope.WebAccessMode, snapshot.Scope.WebAllowedTargets, snapshot.RequestedBy, snapshot.Reason)
}

func (s *SQLiteStore) TransitionRunWebAccess(ctx context.Context, snapshot domain.RunModeSnapshot, operation domain.RunWebAccessOperation, event events.Event) (domain.RunModeSnapshot, bool, error) {
	if err := snapshot.Validate(); err != nil {
		return domain.RunModeSnapshot{}, false, apperror.Wrap(apperror.CodeInvalidArgument, "web access snapshot is invalid", err)
	}
	if err := requireRedactedRunModeSnapshot(snapshot); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if err := operation.Validate(); err != nil {
		return domain.RunModeSnapshot{}, false, apperror.Wrap(apperror.CodeInvalidArgument, "web access operation is invalid", err)
	}
	if !snapshot.Scope.WebAccessMode.Valid() || !webAccessOperationBinding(operation, snapshot) {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "web access operation does not match snapshot")
	}
	if len(snapshot.Scope.WebAllowedTargets) > 0 {
		canonical, err := webevidence.NormalizeExactAuthorityTargets(snapshot.Scope.WebAllowedTargets)
		if err != nil || !sameStringSlice(canonical, snapshot.Scope.WebAllowedTargets) {
			return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "web rules require canonical exact public hosts")
		}
	}
	if err := event.Validate(); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if event.Type != events.RunWebAccessChangedEvent || event.Source != "run_web_access" || event.RunID != snapshot.RunID || event.MissionID != snapshot.MissionID || event.SubjectID != snapshot.ID || !event.CreatedAt.Equal(snapshot.CreatedAt) {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "web access event binding is invalid")
	}
	if err := rejectDuplicateRunModeEventFields(event.PayloadJSON); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	var payload struct {
		Protocol  string               `json:"protocol"`
		Revision  int64                `json:"revision"`
		Expected  int64                `json:"expected_mode_revision"`
		From      domain.WebAccessMode `json:"from"`
		To        domain.WebAccessMode `json:"to"`
		Count     int                  `json:"allowed_target_count"`
		Requester string               `json:"requested_by"`
		Reason    string               `json:"reason"`
	}
	decoder := json.NewDecoder(strings.NewReader(event.PayloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "web access event contains trailing data")
	}
	if payload.Protocol != "run_web_access_control.v1" || payload.Revision != snapshot.Revision || payload.Expected != operation.ExpectedModeRevision || !payload.From.Valid() || payload.To != snapshot.Scope.WebAccessMode || payload.Count != len(snapshot.Scope.WebAllowedTargets) || payload.Requester != snapshot.RequestedBy || payload.Reason != snapshot.Reason {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "web access event does not match snapshot")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireRunModeWriteLockTx(ctx, tx, snapshot.RunID); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if existing, found, err := getRunWebAccessOperation(ctx, tx, operation.KeyDigest); err != nil {
		return domain.RunModeSnapshot{}, false, err
	} else if found {
		if existing.RequestFingerprint != operation.RequestFingerprint || existing.RunID != operation.RunID {
			return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict, "web access key was already used for different intent")
		}
		stored, err := getRunModeSnapshot(ctx, tx, existing.SnapshotID)
		if err != nil {
			return domain.RunModeSnapshot{}, false, err
		}
		if !webAccessOperationBinding(existing, stored) {
			return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeInternal, "stored web access operation binding is invalid")
		}
		if err := tx.Commit(); err != nil {
			return domain.RunModeSnapshot{}, false, err
		}
		return stored, true, nil
	}
	current, err := getCurrentRunModeSnapshot(ctx, tx, snapshot.RunID)
	if err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	run, mission, err := getCoordinatorRunTx(ctx, tx, snapshot.RunID)
	if err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if !domain.CanChangeRunPhase(run.Status) {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict, "web access can change only while Run is created or paused")
	}
	var leases int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_execution_leases WHERE run_id=? AND status='active' AND julianday(expires_at)>julianday('now')`, run.ID).Scan(&leases); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if leases != 0 {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict, "web access cannot change while an execution lease is active")
	}
	if operation.ExpectedModeRevision != current.Revision || snapshot.Revision != current.Revision+1 || snapshot.MissionID != mission.ID || !snapshot.SameStaticPolicy(current) || snapshot.Phase != current.Phase || snapshot.Scope.NetworkMode != current.Scope.NetworkMode || !sameStringSlice(snapshot.Scope.AllowedTargets, current.Scope.AllowedTargets) || snapshot.CreatedAt.Before(current.CreatedAt) || payload.From != current.Scope.EffectiveWebAccessMode() {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict, "web access changed concurrently or another authority was modified")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_web_access_operations (operation_key_digest,request_fingerprint,snapshot_id,run_id,expected_mode_revision,requested_by,created_at) VALUES (?,?,?,?,?,?,?)`, operation.KeyDigest, operation.RequestFingerprint, operation.SnapshotID, operation.RunID, operation.ExpectedModeRevision, operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if err := insertRunModeSnapshotTx(ctx, tx, snapshot); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	return snapshot, false, nil
}
