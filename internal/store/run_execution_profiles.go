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

const runExecutionProfileSnapshotSelect = `SELECT id, run_id, mission_id, revision,
	protocol_version, profile, backend, approval_policy, filesystem_scope,
	network_scope, risk_tier, required_gate, policy_version, process_enabled,
	execution_authorized, capability_grant, requested_by, reason, created_at
	FROM run_execution_profile_snapshots`

type runExecutionProfileQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteStore) GetRunExecutionProfile(ctx context.Context,
	runID string,
) (domain.RunExecutionProfileSnapshot, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return domain.RunExecutionProfileSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "Run execution profile Run id is invalid")
	}
	return getCurrentRunExecutionProfileSnapshot(ctx, s.db, runID)
}

func (s *SQLiteStore) GetRunExecutionProfileSnapshot(ctx context.Context,
	id string,
) (domain.RunExecutionProfileSnapshot, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.RunExecutionProfileSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "Run execution profile snapshot id is invalid")
	}
	return getRunExecutionProfileSnapshot(ctx, s.db, id)
}

func (s *SQLiteStore) GetRunExecutionProfileOperation(ctx context.Context,
	keyDigest string,
) (domain.RunExecutionProfileOperation, bool, error) {
	keyDigest = strings.TrimSpace(keyDigest)
	if !validStoreDigest(keyDigest) {
		return domain.RunExecutionProfileOperation{}, false, apperror.New(
			apperror.CodeInvalidArgument, "Run execution profile operation digest is invalid")
	}
	return getRunExecutionProfileOperation(ctx, s.db, keyDigest)
}

func (s *SQLiteStore) TransitionRunExecutionProfile(ctx context.Context,
	snapshot domain.RunExecutionProfileSnapshot,
	operation domain.RunExecutionProfileOperation, event events.Event,
) (domain.RunExecutionProfileSnapshot, bool, error) {
	if err := validateRunExecutionProfileMutation(snapshot, operation, event); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireRunExecutionProfileWriteLockTx(ctx, tx, snapshot.RunID); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if existing, found, err := getRunExecutionProfileOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	} else if found {
		if err := validateRunExecutionProfileReplay(existing, operation); err != nil {
			return domain.RunExecutionProfileSnapshot{}, false, err
		}
		stored, err := getRunExecutionProfileSnapshot(ctx, tx, existing.SnapshotID)
		if err != nil {
			return domain.RunExecutionProfileSnapshot{}, false, err
		}
		if err := validateRunExecutionProfileOperationBinding(existing, stored); err != nil {
			return domain.RunExecutionProfileSnapshot{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.RunExecutionProfileSnapshot{}, false, err
		}
		return stored, true, nil
	}
	current, err := getCurrentRunExecutionProfileSnapshot(ctx, tx, snapshot.RunID)
	if err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	run, mission, err := getCoordinatorRunTx(ctx, tx, snapshot.RunID)
	if err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if !domain.CanChangeRunExecutionProfile(run.Status) {
		return domain.RunExecutionProfileSnapshot{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			fmt.Sprintf("Run execution profile can only change while created or paused; Run is %s",
				run.Status))
	}
	var activeLeaseCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_execution_leases
		WHERE run_id = ? AND status = 'active' AND julianday(expires_at) > julianday('now')`,
		run.ID).Scan(&activeLeaseCount); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if activeLeaseCount != 0 {
		return domain.RunExecutionProfileSnapshot{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"Run execution profile cannot change while an execution lease is active")
	}
	if err := requireThreadLifecycleRunQuiescentTx(ctx, tx, run.ID, snapshot.CreatedAt); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if snapshot.MissionID != run.MissionID || snapshot.MissionID != mission.ID ||
		snapshot.Revision != current.Revision+1 ||
		(snapshot.Profile == current.Profile && !(current.IsDefaultPreview() && snapshot.Profile == domain.RunExecutionProfilePreview)) ||
		snapshot.ProtocolVersion != current.ProtocolVersion ||
		snapshot.PolicyVersion != current.PolicyVersion ||
		snapshot.CreatedAt.Before(current.CreatedAt) {
		return domain.RunExecutionProfileSnapshot{}, false, apperror.New(
			apperror.CodeConflict,
			"Run execution profile changed concurrently or attempted to change immutable policy")
	}
	if err := insertRunExecutionProfileSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback()
		return s.recoverRunExecutionProfileTransition(ctx, operation, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_execution_profile_operations
		(operation_key_digest, request_fingerprint, snapshot_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.SnapshotID, operation.RunID,
		operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverRunExecutionProfileTransition(ctx, operation, err)
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	return snapshot, false, nil
}

func insertInitialRunExecutionProfileSnapshotTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.RunExecutionProfileSnapshot, run domain.Run, mission domain.Mission,
) error {
	if err := snapshot.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"initial Run execution profile is invalid", err)
	}
	if err := requireRedactedRunExecutionProfileSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Revision != 1 || snapshot.RunID != run.ID ||
		snapshot.MissionID != run.MissionID || snapshot.MissionID != mission.ID ||
		snapshot.Profile != domain.RunExecutionProfilePreview ||
		run.Status != domain.RunCreated || snapshot.CreatedAt.Before(run.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"initial Run execution profile does not match its created Run and Mission")
	}
	return insertRunExecutionProfileSnapshotTx(ctx, tx, snapshot)
}

func insertRunExecutionProfileSnapshotTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.RunExecutionProfileSnapshot,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_execution_profile_snapshots
		(id, run_id, mission_id, revision, protocol_version, profile, backend,
		approval_policy, filesystem_scope, network_scope, risk_tier, required_gate,
		policy_version, process_enabled, execution_authorized, capability_grant,
		requested_by, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.ID, snapshot.RunID, snapshot.MissionID, snapshot.Revision,
		snapshot.ProtocolVersion, snapshot.Profile, snapshot.Backend,
		snapshot.ApprovalPolicy, snapshot.FilesystemScope, snapshot.NetworkScope,
		snapshot.RiskTier, snapshot.RequiredGate, snapshot.PolicyVersion,
		snapshot.ProcessEnabled, snapshot.ExecutionAuthorized, snapshot.CapabilityGrant,
		snapshot.RequestedBy, snapshot.Reason, ts(snapshot.CreatedAt))
	return err
}

func validateRunExecutionProfileMutation(snapshot domain.RunExecutionProfileSnapshot,
	operation domain.RunExecutionProfileOperation, event events.Event,
) error {
	if err := snapshot.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Run execution profile snapshot is invalid", err)
	}
	if err := requireRedactedRunExecutionProfileSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Revision <= 1 {
		return apperror.New(apperror.CodeInvalidArgument,
			"Run execution profile transition revision must exceed one")
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Run execution profile operation is invalid", err)
	}
	if operation.SnapshotID != snapshot.ID || operation.RunID != snapshot.RunID ||
		operation.RequestedBy != snapshot.RequestedBy ||
		!operation.CreatedAt.Equal(snapshot.CreatedAt) ||
		operation.RequestFingerprint != runExecutionProfileRequestFingerprint(snapshot) {
		return apperror.New(apperror.CodeInvalidArgument,
			"Run execution profile operation does not match its snapshot")
	}
	if err := validateRunExecutionProfileChangedEvent(event, snapshot); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Run execution profile event is invalid", err)
	}
	return nil
}

func requireRedactedRunExecutionProfileSnapshot(
	snapshot domain.RunExecutionProfileSnapshot,
) error {
	if redact.String(snapshot.RequestedBy) != snapshot.RequestedBy ||
		redact.String(snapshot.Reason) != snapshot.Reason {
		return apperror.New(apperror.CodeInvalidArgument,
			"Run execution profile requester and reason must be redacted before persistence")
	}
	return nil
}

func validateRunExecutionProfileChangedEvent(event events.Event,
	snapshot domain.RunExecutionProfileSnapshot,
) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != events.RunExecutionProfileSelectedEvent ||
		event.Source != "run_execution_profile" || event.RunID != snapshot.RunID ||
		event.MissionID != snapshot.MissionID || event.SubjectID != snapshot.ID ||
		!event.CreatedAt.Equal(snapshot.CreatedAt) {
		return errors.New("run execution profile event identity does not match its snapshot")
	}
	if err := rejectDuplicateJSONFields(event.PayloadJSON); err != nil {
		return err
	}
	var payload struct {
		Protocol            string                          `json:"protocol"`
		Revision            int64                           `json:"revision"`
		From                domain.RunExecutionProfile      `json:"from"`
		To                  domain.RunExecutionProfile      `json:"to"`
		Backend             domain.ExecutionBackend         `json:"backend"`
		ApprovalPolicy      domain.ExecutionApprovalPolicy  `json:"approval_policy"`
		FilesystemScope     domain.ExecutionFilesystemScope `json:"filesystem_scope"`
		NetworkScope        domain.ExecutionNetworkScope    `json:"network_scope"`
		RiskTier            domain.ExecutionRiskTier        `json:"risk_tier"`
		RequiredGate        domain.ExecutionRequiredGate    `json:"required_gate"`
		PolicyVersion       string                          `json:"policy_version"`
		RequestedBy         string                          `json:"requested_by"`
		Reason              string                          `json:"reason"`
		ProcessEnabled      *bool                           `json:"process_enabled"`
		ExecutionAuthorized *bool                           `json:"execution_authorized"`
		CapabilityGrant     *bool                           `json:"capability_grant"`
	}
	decoder := json.NewDecoder(strings.NewReader(event.PayloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("run execution profile event contains trailing data")
	}
	if payload.Protocol != snapshot.ProtocolVersion || payload.Revision != snapshot.Revision ||
		!payload.From.Valid() ||
		(payload.From == snapshot.Profile && !(snapshot.Profile == domain.RunExecutionProfilePreview && snapshot.Revision == 2)) ||
		payload.To != snapshot.Profile ||
		payload.Backend != snapshot.Backend || payload.ApprovalPolicy != snapshot.ApprovalPolicy ||
		payload.FilesystemScope != snapshot.FilesystemScope ||
		payload.NetworkScope != snapshot.NetworkScope || payload.RiskTier != snapshot.RiskTier ||
		payload.RequiredGate != snapshot.RequiredGate ||
		payload.PolicyVersion != snapshot.PolicyVersion ||
		payload.RequestedBy != snapshot.RequestedBy || payload.Reason != snapshot.Reason ||
		payload.ProcessEnabled == nil || *payload.ProcessEnabled ||
		payload.ExecutionAuthorized == nil || *payload.ExecutionAuthorized ||
		payload.CapabilityGrant == nil || *payload.CapabilityGrant {
		return errors.New("run execution profile event does not match its closed capability boundary")
	}
	return nil
}

func rejectDuplicateJSONFields(payloadJSON string) error {
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("event payload must be a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("event payload field is invalid")
		}
		field, ok := token.(string)
		if !ok {
			return errors.New("event payload field name is invalid")
		}
		if _, exists := seen[field]; exists {
			return errors.New("event payload contains a duplicate field")
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("event payload field value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("event payload is not closed")
	}
	return nil
}

func acquireRunExecutionProfileWriteLockTx(ctx context.Context, tx *sql.Tx,
	runID string,
) error {
	result, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`, runID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeNotFound,
			"Run execution profile Run was not found")
	}
	return nil
}

func (s *SQLiteStore) recoverRunExecutionProfileTransition(ctx context.Context,
	operation domain.RunExecutionProfileOperation, original error,
) (domain.RunExecutionProfileSnapshot, bool, error) {
	existing, found, err := getRunExecutionProfileOperation(ctx, s.db, operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.RunExecutionProfileSnapshot{}, false, original
		}
		return domain.RunExecutionProfileSnapshot{}, false, errors.Join(original, err)
	}
	if err := validateRunExecutionProfileReplay(existing, operation); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	stored, err := getRunExecutionProfileSnapshot(ctx, s.db, existing.SnapshotID)
	if err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if err := validateRunExecutionProfileOperationBinding(existing, stored); err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	return stored, true, nil
}

func validateRunExecutionProfileReplay(existing,
	request domain.RunExecutionProfileOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.RunID != request.RunID || existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"Run execution profile operation key was already used for different intent")
	}
	return nil
}

func validateRunExecutionProfileOperationBinding(
	operation domain.RunExecutionProfileOperation,
	snapshot domain.RunExecutionProfileSnapshot,
) error {
	if operation.SnapshotID != snapshot.ID || operation.RunID != snapshot.RunID ||
		operation.RequestedBy != snapshot.RequestedBy ||
		!operation.CreatedAt.Equal(snapshot.CreatedAt) ||
		operation.RequestFingerprint != runExecutionProfileRequestFingerprint(snapshot) {
		return apperror.New(apperror.CodeInternal,
			"stored Run execution profile operation binding is invalid")
	}
	return nil
}

func runExecutionProfileRequestFingerprint(
	snapshot domain.RunExecutionProfileSnapshot,
) string {
	return runmutation.Fingerprint("run_execution_profile_change_request.v1",
		snapshot.RunID, string(snapshot.Profile), snapshot.RequestedBy, snapshot.Reason)
}

func getRunExecutionProfileSnapshot(ctx context.Context,
	queryer runExecutionProfileQueryer, id string,
) (domain.RunExecutionProfileSnapshot, error) {
	return scanRunExecutionProfileSnapshot(queryer.QueryRowContext(ctx,
		runExecutionProfileSnapshotSelect+` WHERE id = ?`, id))
}

func getCurrentRunExecutionProfileSnapshot(ctx context.Context,
	queryer runExecutionProfileQueryer, runID string,
) (domain.RunExecutionProfileSnapshot, error) {
	return scanRunExecutionProfileSnapshot(queryer.QueryRowContext(ctx,
		runExecutionProfileSnapshotSelect+
			` WHERE run_id = ? ORDER BY revision DESC LIMIT 1`, runID))
}

func scanRunExecutionProfileSnapshot(scanner interface{ Scan(...any) error }) (
	domain.RunExecutionProfileSnapshot, error,
) {
	var snapshot domain.RunExecutionProfileSnapshot
	var createdAt string
	if err := scanner.Scan(&snapshot.ID, &snapshot.RunID, &snapshot.MissionID,
		&snapshot.Revision, &snapshot.ProtocolVersion, &snapshot.Profile,
		&snapshot.Backend, &snapshot.ApprovalPolicy, &snapshot.FilesystemScope,
		&snapshot.NetworkScope, &snapshot.RiskTier, &snapshot.RequiredGate,
		&snapshot.PolicyVersion, &snapshot.ProcessEnabled, &snapshot.ExecutionAuthorized,
		&snapshot.CapabilityGrant, &snapshot.RequestedBy, &snapshot.Reason,
		&createdAt); err != nil {
		return domain.RunExecutionProfileSnapshot{}, err
	}
	snapshot.CreatedAt = parseTS(createdAt)
	return snapshot, snapshot.Validate()
}

func getRunExecutionProfileOperation(ctx context.Context,
	queryer runExecutionProfileQueryer, keyDigest string,
) (domain.RunExecutionProfileOperation, bool, error) {
	var operation domain.RunExecutionProfileOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, snapshot_id, run_id, requested_by, created_at
		FROM run_execution_profile_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&operation.KeyDigest, &operation.RequestFingerprint, &operation.SnapshotID,
			&operation.RunID, &operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunExecutionProfileOperation{}, false, nil
	}
	if err != nil {
		return domain.RunExecutionProfileOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}
