package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const runModeSnapshotSelect = `SELECT id, run_id, mission_id, revision,
	protocol_version, surface, phase, profile, scope_json, policy_version,
	requested_by, reason, created_at FROM run_mode_snapshots`

type runModeQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteStore) GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return domain.RunModeSnapshot{}, apperror.New(apperror.CodeInvalidArgument,
			"run mode Run id is invalid")
	}
	return getCurrentRunModeSnapshot(ctx, s.db, runID)
}

func (s *SQLiteStore) GetRunModeSnapshot(ctx context.Context,
	id string,
) (domain.RunModeSnapshot, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.RunModeSnapshot{}, apperror.New(apperror.CodeInvalidArgument,
			"run mode snapshot id is invalid")
	}
	return getRunModeSnapshot(ctx, s.db, id)
}

func (s *SQLiteStore) GetRunModeOperation(ctx context.Context,
	keyDigest string,
) (domain.RunModeOperation, bool, error) {
	keyDigest = strings.TrimSpace(keyDigest)
	if !validStoreDigest(keyDigest) {
		return domain.RunModeOperation{}, false, apperror.New(apperror.CodeInvalidArgument,
			"run mode operation digest is invalid")
	}
	return getRunModeOperation(ctx, s.db, keyDigest)
}

func (s *SQLiteStore) TransitionRunPhase(ctx context.Context, snapshot domain.RunModeSnapshot,
	operation domain.RunModeOperation, event events.Event,
) (domain.RunModeSnapshot, bool, error) {
	return s.transitionRunPhase(ctx, "", "", snapshot, operation, event)
}

func (s *SQLiteStore) TransitionThreadRunPhase(ctx context.Context, threadID, proposalID string,
	snapshot domain.RunModeSnapshot, operation domain.RunModeOperation, event events.Event,
) (domain.RunModeSnapshot, bool, error) {
	return s.transitionRunPhase(ctx, threadID, proposalID, snapshot, operation, event)
}

func (s *SQLiteStore) transitionRunPhase(ctx context.Context, threadID, proposalID string,
	snapshot domain.RunModeSnapshot, operation domain.RunModeOperation, event events.Event,
) (domain.RunModeSnapshot, bool, error) {
	snapshot.Scope = domain.CloneScope(snapshot.Scope)
	if err := validateRunModeMutation(snapshot, operation, event); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireRunModeWriteLockTx(ctx, tx, snapshot.RunID); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if existing, found, err := getRunModeOperation(ctx, tx, operation.KeyDigest); err != nil {
		return domain.RunModeSnapshot{}, false, err
	} else if found {
		if err := validateRunModeReplay(existing, operation); err != nil {
			return domain.RunModeSnapshot{}, false, err
		}
		stored, err := getRunModeSnapshot(ctx, tx, existing.SnapshotID)
		if err != nil {
			return domain.RunModeSnapshot{}, false, err
		}
		if err := validateRunModeOperationBinding(existing, stored); err != nil {
			return domain.RunModeSnapshot{}, false, err
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
	if threadID != "" {
		if err := requireUnusedThreadPlanSuccessorKeyTx(ctx, tx, threadID, operation.KeyDigest); err != nil {
			return domain.RunModeSnapshot{}, false, err
		}
		if selected, found, err := getPlanDeliverySelectionOperation(ctx, tx, operation.KeyDigest); err != nil {
			return domain.RunModeSnapshot{}, false, err
		} else if found && (proposalID == "" || selected.ProposalID != proposalID || selected.RunID != snapshot.RunID || selected.RequestedBy != operation.RequestedBy) {
			return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict, "Thread Plan key belongs to a different confirmation")
		}
		if proposalID == "" {
			if _, selected, err := getPlanDeliverySelectionByRun(ctx, tx, snapshot.RunID); err != nil {
				return domain.RunModeSnapshot{}, false, err
			} else if selected {
				return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict, "A Plan was selected while changing mode; start the new mode in a Thread successor")
			}
		}
		if err := requireThreadPlanPreparationTx(ctx, tx, threadID, snapshot.RunID, proposalID); err != nil {
			return domain.RunModeSnapshot{}, false, err
		}
	}
	run, mission, err := getCoordinatorRunTx(ctx, tx, snapshot.RunID)
	if err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if !domain.CanChangeRunPhase(run.Status) {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run phase can only change while created or paused; Run is %s", run.Status))
	}
	var activeLeaseCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_execution_leases
		WHERE run_id = ? AND status = 'active' AND julianday(expires_at) > julianday('now')`,
		run.ID).Scan(&activeLeaseCount); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if activeLeaseCount != 0 {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"run phase cannot change while an execution lease is active")
	}
	if snapshot.MissionID != run.MissionID || snapshot.MissionID != mission.ID ||
		snapshot.Profile != mission.Profile ||
		snapshot.Scope.WorkspaceID != mission.Scope.WorkspaceID ||
		snapshot.Revision != current.Revision+1 || snapshot.Phase == current.Phase ||
		!snapshot.SamePolicy(current) || snapshot.CreatedAt.Before(current.CreatedAt) {
		return domain.RunModeSnapshot{}, false, apperror.New(apperror.CodeConflict,
			"run mode changed concurrently or attempted to change immutable policy")
	}
	if err := insertRunModeSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback()
		return s.recoverRunModeTransition(ctx, operation, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_mode_operations
		(operation_key_digest, request_fingerprint, snapshot_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.SnapshotID, operation.RunID,
		operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverRunModeTransition(ctx, operation, err)
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	return snapshot, false, nil
}

func insertInitialRunModeSnapshotTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.RunModeSnapshot, run domain.Run, mission domain.Mission,
) error {
	snapshot.Scope = domain.CloneScope(snapshot.Scope)
	if err := snapshot.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "initial run mode is invalid", err)
	}
	if err := requireRedactedRunModeSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Revision != 1 || snapshot.RunID != run.ID ||
		snapshot.MissionID != run.MissionID || snapshot.MissionID != mission.ID ||
		snapshot.Profile != mission.Profile || !sameRunModeScope(snapshot.Scope, mission.Scope) ||
		run.Status != domain.RunCreated || snapshot.CreatedAt.Before(run.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"initial run mode does not match its created Run and Mission")
	}
	return insertRunModeSnapshotTx(ctx, tx, snapshot)
}

func appendInitialRunModeEventTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.RunModeSnapshot,
) error {
	event, err := events.New(snapshot.RunID, snapshot.MissionID, events.RunModeSelectedEvent,
		"run_mode", snapshot.ID, map[string]any{
			"protocol": snapshot.ProtocolVersion, "revision": snapshot.Revision,
			"surface": snapshot.Surface, "phase": snapshot.Phase,
			"profile": snapshot.Profile, "policy_version": snapshot.PolicyVersion,
			"network_mode":         snapshot.Scope.NetworkMode,
			"allowed_target_count": len(snapshot.Scope.AllowedTargets),
			"capability_grant":     false,
		})
	if err != nil {
		return err
	}
	event.CreatedAt = snapshot.CreatedAt
	_, err = insertRunEventTx(ctx, tx, event)
	return err
}

func insertRunModeSnapshotTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.RunModeSnapshot,
) error {
	scopeJSON, err := marshalRedactedJSON(snapshot.Scope)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_mode_snapshots
		(id, run_id, mission_id, revision, protocol_version, surface, phase,
		profile, scope_json, policy_version, requested_by, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.ID,
		snapshot.RunID, snapshot.MissionID, snapshot.Revision, snapshot.ProtocolVersion,
		snapshot.Surface, snapshot.Phase, snapshot.Profile, scopeJSON,
		snapshot.PolicyVersion, snapshot.RequestedBy, snapshot.Reason,
		ts(snapshot.CreatedAt))
	return err
}

func validateRunModeMutation(snapshot domain.RunModeSnapshot,
	operation domain.RunModeOperation, event events.Event,
) error {
	if err := snapshot.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "run mode snapshot is invalid", err)
	}
	if err := requireRedactedRunModeSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Revision <= 1 {
		return apperror.New(apperror.CodeInvalidArgument,
			"run mode transition snapshot revision must exceed one")
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "run mode operation is invalid", err)
	}
	if operation.SnapshotID != snapshot.ID || operation.RunID != snapshot.RunID ||
		operation.RequestedBy != snapshot.RequestedBy ||
		!operation.CreatedAt.Equal(snapshot.CreatedAt) ||
		operation.RequestFingerprint != runModeRequestFingerprint(snapshot) {
		return apperror.New(apperror.CodeInvalidArgument,
			"run mode operation does not match its snapshot")
	}
	if err := validateRunPhaseChangedEvent(event, snapshot); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"run phase event is invalid", err)
	}
	return nil
}

func requireRedactedRunModeSnapshot(snapshot domain.RunModeSnapshot) error {
	if redact.String(snapshot.RequestedBy) != snapshot.RequestedBy ||
		redact.String(snapshot.Reason) != snapshot.Reason {
		return apperror.New(apperror.CodeInvalidArgument,
			"run mode requester and reason must be redacted before persistence")
	}
	return nil
}

func validateRunPhaseChangedEvent(event events.Event, snapshot domain.RunModeSnapshot) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != events.RunPhaseChangedEvent || event.Source != "run_mode" ||
		event.RunID != snapshot.RunID || event.MissionID != snapshot.MissionID ||
		event.SubjectID != snapshot.ID || !event.CreatedAt.Equal(snapshot.CreatedAt) {
		return errors.New("run phase event identity does not match its snapshot")
	}
	if err := rejectDuplicateRunModeEventFields(event.PayloadJSON); err != nil {
		return err
	}
	var payload struct {
		Protocol           string                  `json:"protocol"`
		Revision           int64                   `json:"revision"`
		Surface            domain.ExecutionSurface `json:"surface"`
		From               domain.ExecutionPhase   `json:"from"`
		To                 domain.ExecutionPhase   `json:"to"`
		PolicyVersion      string                  `json:"policy_version"`
		NetworkMode        string                  `json:"network_mode"`
		AllowedTargetCount int                     `json:"allowed_target_count"`
		RequestedBy        string                  `json:"requested_by"`
		Reason             string                  `json:"reason"`
		CapabilityGrant    *bool                   `json:"capability_grant"`
	}
	decoder := json.NewDecoder(strings.NewReader(event.PayloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("run phase event contains trailing data")
	}
	if payload.Protocol != snapshot.ProtocolVersion || payload.Revision != snapshot.Revision ||
		payload.Surface != snapshot.Surface || !payload.From.Valid() ||
		payload.From == snapshot.Phase || payload.To != snapshot.Phase ||
		payload.PolicyVersion != snapshot.PolicyVersion ||
		payload.NetworkMode != snapshot.Scope.NetworkMode ||
		payload.AllowedTargetCount != len(snapshot.Scope.AllowedTargets) ||
		payload.RequestedBy != snapshot.RequestedBy || payload.Reason != snapshot.Reason ||
		payload.CapabilityGrant == nil || *payload.CapabilityGrant {
		return errors.New("run phase event does not match its closed capability boundary")
	}
	return nil
}

func rejectDuplicateRunModeEventFields(payloadJSON string) error {
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("run phase event payload must be a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("run phase event field is invalid")
		}
		field, ok := token.(string)
		if !ok {
			return errors.New("run phase event field name is invalid")
		}
		if _, exists := seen[field]; exists {
			return errors.New("run phase event contains a duplicate field")
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("run phase event field value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("run phase event payload is not closed")
	}
	return nil
}

func acquireRunModeWriteLockTx(ctx context.Context, tx *sql.Tx, runID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`, runID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeNotFound, "run mode Run was not found")
	}
	return nil
}

func (s *SQLiteStore) recoverRunModeTransition(ctx context.Context,
	operation domain.RunModeOperation, original error,
) (domain.RunModeSnapshot, bool, error) {
	existing, found, err := getRunModeOperation(ctx, s.db, operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.RunModeSnapshot{}, false, original
		}
		return domain.RunModeSnapshot{}, false, errors.Join(original, err)
	}
	if err := validateRunModeReplay(existing, operation); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	stored, err := getRunModeSnapshot(ctx, s.db, existing.SnapshotID)
	if err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	if err := validateRunModeOperationBinding(existing, stored); err != nil {
		return domain.RunModeSnapshot{}, false, err
	}
	return stored, true, nil
}

func validateRunModeReplay(existing, request domain.RunModeOperation) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.RunID != request.RunID || existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"run mode operation key was already used for different intent")
	}
	return nil
}

func validateRunModeOperationBinding(operation domain.RunModeOperation,
	snapshot domain.RunModeSnapshot,
) error {
	if operation.SnapshotID != snapshot.ID || operation.RunID != snapshot.RunID ||
		operation.RequestedBy != snapshot.RequestedBy ||
		!operation.CreatedAt.Equal(snapshot.CreatedAt) ||
		operation.RequestFingerprint != runModeRequestFingerprint(snapshot) {
		return apperror.New(apperror.CodeInternal,
			"stored run mode operation binding is invalid")
	}
	return nil
}

func runModeRequestFingerprint(snapshot domain.RunModeSnapshot) string {
	return runmutation.Fingerprint("run_phase_change_request.v1", snapshot.RunID,
		string(snapshot.Phase), snapshot.RequestedBy, snapshot.Reason)
}

func getRunModeSnapshot(ctx context.Context, queryer runModeQueryer,
	id string,
) (domain.RunModeSnapshot, error) {
	return scanRunModeSnapshot(queryer.QueryRowContext(ctx,
		runModeSnapshotSelect+` WHERE id = ?`, id))
}

func getCurrentRunModeSnapshot(ctx context.Context, queryer runModeQueryer,
	runID string,
) (domain.RunModeSnapshot, error) {
	return scanRunModeSnapshot(queryer.QueryRowContext(ctx,
		runModeSnapshotSelect+` WHERE run_id = ? ORDER BY revision DESC LIMIT 1`, runID))
}

func scanRunModeSnapshot(scanner interface{ Scan(...any) error }) (domain.RunModeSnapshot, error) {
	var snapshot domain.RunModeSnapshot
	var scopeJSON string
	var createdAt string
	if err := scanner.Scan(&snapshot.ID, &snapshot.RunID, &snapshot.MissionID,
		&snapshot.Revision, &snapshot.ProtocolVersion, &snapshot.Surface,
		&snapshot.Phase, &snapshot.Profile, &scopeJSON, &snapshot.PolicyVersion,
		&snapshot.RequestedBy, &snapshot.Reason, &createdAt); err != nil {
		return domain.RunModeSnapshot{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(scopeJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot.Scope); err != nil {
		return domain.RunModeSnapshot{}, fmt.Errorf("decode run mode scope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.RunModeSnapshot{}, errors.New("run mode scope contains trailing data")
	}
	snapshot.CreatedAt = parseTS(createdAt)
	snapshot.Scope = domain.CloneScope(snapshot.Scope)
	return snapshot, snapshot.Validate()
}

func getRunModeOperation(ctx context.Context, queryer runModeQueryer,
	keyDigest string,
) (domain.RunModeOperation, bool, error) {
	var operation domain.RunModeOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, snapshot_id, run_id, requested_by, created_at
		FROM run_mode_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&operation.KeyDigest, &operation.RequestFingerprint, &operation.SnapshotID,
			&operation.RunID, &operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunModeOperation{}, false, nil
	}
	if err != nil {
		return domain.RunModeOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

func sameRunModeScope(left, right domain.Scope) bool {
	if !left.SameWebAccess(right) || left.WorkspaceID != right.WorkspaceID || left.NetworkMode != right.NetworkMode ||
		len(left.AllowedTargets) != len(right.AllowedTargets) {
		return false
	}
	for index := range left.AllowedTargets {
		if left.AllowedTargets[index] != right.AllowedTargets[index] {
			return false
		}
	}
	return true
}
