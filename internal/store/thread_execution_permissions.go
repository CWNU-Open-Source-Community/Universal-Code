package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const threadExecutionPermissionSnapshotSelect = `SELECT id, thread_id, mission_id, revision,
	protocol_version, mode, approval_policy, command_scope, filesystem_scope,
	network_scope, persistent_terminal, background_process, agent_terminal_input,
	risk_tier, required_gate, policy_version, operator_confirmed, process_enabled,
	execution_authorized, capability_grant, requested_by, reason, created_at
	FROM thread_execution_permission_snapshots`

type threadExecutionPermissionQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteStore) GetThreadExecutionPermission(ctx context.Context,
	threadID string,
) (domain.ThreadExecutionPermissionSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if !domain.ValidAgentID(threadID) || strings.ContainsRune(threadID, 0) {
		return domain.ThreadExecutionPermissionSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "Thread execution permission Thread id is invalid")
	}
	return getCurrentThreadExecutionPermissionSnapshot(ctx, s.db, threadID)
}

func (s *SQLiteStore) GetThreadExecutionPermissionSnapshot(ctx context.Context,
	id string,
) (domain.ThreadExecutionPermissionSnapshot, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.ThreadExecutionPermissionSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "Thread execution permission snapshot id is invalid")
	}
	return getThreadExecutionPermissionSnapshot(ctx, s.db, id)
}

func (s *SQLiteStore) GetThreadExecutionPermissionOperation(ctx context.Context,
	keyDigest string,
) (domain.ThreadExecutionPermissionOperation, bool, error) {
	keyDigest = strings.TrimSpace(keyDigest)
	if !validStoreDigest(keyDigest) {
		return domain.ThreadExecutionPermissionOperation{}, false, apperror.New(
			apperror.CodeInvalidArgument, "Thread execution permission operation digest is invalid")
	}
	return getThreadExecutionPermissionOperation(ctx, s.db, keyDigest)
}

func (s *SQLiteStore) TransitionThreadExecutionPermission(ctx context.Context,
	snapshot domain.ThreadExecutionPermissionSnapshot,
	operation domain.ThreadExecutionPermissionOperation,
) (domain.ThreadExecutionPermissionSnapshot,
	domain.ThreadExecutionPermissionOperation, bool, error,
) {
	if err := validateThreadExecutionPermissionMutation(snapshot, operation); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireThreadExecutionPermissionWriteLockTx(ctx, tx, snapshot.ThreadID); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	if existing, found, err := getThreadExecutionPermissionOperation(
		ctx, tx, operation.KeyDigest); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	} else if found {
		if err := validateThreadExecutionPermissionReplay(existing, operation); err != nil {
			return domain.ThreadExecutionPermissionSnapshot{},
				domain.ThreadExecutionPermissionOperation{}, false, err
		}
		stored, err := getThreadExecutionPermissionSnapshot(ctx, tx, existing.SnapshotID)
		if err != nil {
			return domain.ThreadExecutionPermissionSnapshot{},
				domain.ThreadExecutionPermissionOperation{}, false, err
		}
		if err := validateThreadExecutionPermissionOperationBinding(existing, stored); err != nil {
			return domain.ThreadExecutionPermissionSnapshot{},
				domain.ThreadExecutionPermissionOperation{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ThreadExecutionPermissionSnapshot{},
				domain.ThreadExecutionPermissionOperation{}, false, err
		}
		return stored, existing, true, nil
	}
	current, err := getCurrentThreadExecutionPermissionSnapshot(ctx, tx, snapshot.ThreadID)
	if err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	threadRecord, err := scanThread(tx.QueryRowContext(ctx,
		threadSelect+` WHERE id = ?`, snapshot.ThreadID))
	if err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	if threadRecord.Status != domain.ThreadActive {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, apperror.New(
				apperror.CodeFailedPrecondition,
				"Thread execution permission can only change while the Thread is active")
	}
	if snapshot.MissionID != threadRecord.MissionID ||
		snapshot.Revision != current.Revision+1 ||
		!domain.PermissionPolicyTransition(current.ProtocolVersion, current.PolicyVersion, snapshot.ProtocolVersion, snapshot.PolicyVersion, true) ||
		snapshot.CreatedAt.Before(current.CreatedAt) {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, apperror.New(
				apperror.CodeConflict,
				"Thread execution permission changed concurrently or attempted to change immutable policy")
	}
	operation, err = applyThreadExecutionPermissionToCurrentRunTx(
		ctx, tx, threadRecord, current, snapshot, operation)
	if err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	if operation.CurrentRunEffect.AppliesToCurrentRun() {
		if _, _, err := synchronizeRunBrowserCDPForThreadPermissionTx(ctx, tx,
			operation.CurrentRunID, snapshot.MissionID, current.Mode, snapshot,
			operation.KeyDigest); err != nil {
			return domain.ThreadExecutionPermissionSnapshot{},
				domain.ThreadExecutionPermissionOperation{}, false, err
		}
	}
	if err := insertThreadExecutionPermissionSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback()
		return s.recoverThreadExecutionPermissionTransition(ctx, operation, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_execution_permission_operations
		(operation_key_digest, request_fingerprint, snapshot_id, thread_id,
		requested_by, current_run_id, current_run_effect,
		current_run_permission_snapshot_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.SnapshotID, operation.ThreadID,
		operation.RequestedBy, nullableString(operation.CurrentRunID),
		operation.CurrentRunEffect,
		nullableString(operation.CurrentRunPermissionSnapshotID),
		ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverThreadExecutionPermissionTransition(ctx, operation, err)
	}
	payload, err := json.Marshal(map[string]any{
		"protocol": snapshot.ProtocolVersion, "revision": snapshot.Revision,
		"from": current.Mode, "to": snapshot.Mode,
		"policy_version": snapshot.PolicyVersion,
		"requested_by":   snapshot.RequestedBy, "reason": snapshot.Reason,
		"process_enabled": false, "execution_authorized": false,
		"capability_grant": false, "current_run_id": operation.CurrentRunID,
		"current_run_effect":                 operation.CurrentRunEffect,
		"current_run_permission_snapshot_id": operation.CurrentRunPermissionSnapshotID,
		"applies_to_current_run":             operation.CurrentRunEffect.AppliesToCurrentRun(),
		"applies_to_future_successor_runs":   true,
	})
	if err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, 'thread.execution_permission_selected',
		'thread_execution_permission', ?, ?)`, snapshot.ThreadID,
		nullableString(operation.CurrentRunID), string(payload),
		ts(snapshot.CreatedAt)); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	return snapshot, operation, false, nil
}

func applyThreadExecutionPermissionToCurrentRunTx(ctx context.Context, tx *sql.Tx,
	threadRecord domain.Thread, previous, preference domain.ThreadExecutionPermissionSnapshot,
	operation domain.ThreadExecutionPermissionOperation,
) (domain.ThreadExecutionPermissionOperation, error) {
	if threadRecord.ActiveRunID == "" {
		operation.CurrentRunEffect = domain.ThreadExecutionPermissionNoActiveRun
		if err := operation.Validate(); err != nil {
			return domain.ThreadExecutionPermissionOperation{}, err
		}
		return operation, nil
	}
	run, err := getRunControlRunTx(ctx, tx, threadRecord.ActiveRunID)
	if err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	if run.Terminal() || run.MissionID != threadRecord.MissionID {
		return domain.ThreadExecutionPermissionOperation{}, apperror.New(
			apperror.CodeConflict, "Thread active Run projection is invalid")
	}
	current, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	operation.CurrentRunID = run.ID
	operation.CurrentRunEffect = domain.ThreadExecutionPermissionApplied
	operation.CurrentRunPermissionSnapshotID = current.ID
	if runPermissionMatchesThreadPreference(current, preference) &&
		!preference.Mode.IsFullPreference() {
		if err := operation.Validate(); err != nil {
			return domain.ThreadExecutionPermissionOperation{}, err
		}
		return operation, nil
	}
	// An interactive Run stays "running" between replies. Explicitly
	// reconfirming its existing Full policy must be able to rotate the cold
	// process grant at that idle boundary; it is not a deferred policy upgrade.
	fullReconfirmation := run.Status == domain.RunRunning &&
		previous.Mode == domain.RunExecutionPermissionFull &&
		preference.Mode == domain.RunExecutionPermissionFull &&
		runPermissionMatchesThreadPreference(current, preference)
	immediateDowngrade := threadPermissionTransitionRevokesHighRisk(
		current.Mode, preference.Mode)
	if immediateDowngrade {
		if err := releaseRunExecutionLeaseForPermissionDowngradeTx(
			ctx, tx, run, preference.CreatedAt); err != nil {
			return domain.ThreadExecutionPermissionOperation{}, err
		}
	} else if !fullReconfirmation && (run.Status == domain.RunPreparing ||
		run.Status == domain.RunRunning ||
		run.Status == domain.RunWaitingApproval) {
		// A Thread preference is allowed to move ahead of an executing or
		// approval-blocked Run, but it must not rewrite that Run's immutable
		// execution contract. The current snapshot remains exact and the new
		// preference is materialized only when a successor Run is created.
		operation.CurrentRunEffect = domain.ThreadExecutionPermissionDeferred
		if err := operation.Validate(); err != nil {
			return domain.ThreadExecutionPermissionOperation{}, err
		}
		return operation, nil
	} else {
		var activeLeaseCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_execution_leases
			WHERE run_id = ? AND status = 'active'
				AND julianday(expires_at) > julianday(?)`, run.ID, ts(preference.CreatedAt)).
			Scan(&activeLeaseCount); err != nil {
			return domain.ThreadExecutionPermissionOperation{}, err
		}
		if activeLeaseCount != 0 {
			return domain.ThreadExecutionPermissionOperation{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Thread permission cannot raise the current Run while an execution lease is active; wait for the current step to finish and retry")
		}
		if run.Status != domain.RunCreated && run.Status != domain.RunPaused && !fullReconfirmation {
			return domain.ThreadExecutionPermissionOperation{}, apperror.New(
				apperror.CodeFailedPrecondition,
				fmt.Sprintf("Thread permission escalation cannot change the current Run while it is %s", run.Status))
		}
		if err := requireThreadLifecycleRunQuiescentTx(ctx, tx, run.ID,
			preference.CreatedAt); err != nil {
			return domain.ThreadExecutionPermissionOperation{}, apperror.Wrap(
				apperror.CodeFailedPrecondition,
				"Thread permission escalation requires a quiescent current Run", err)
		}
	}
	if preference.CreatedAt.Before(current.CreatedAt) {
		return domain.ThreadExecutionPermissionOperation{}, apperror.New(
			apperror.CodeConflict,
			"current Run permission changed while the Thread preference was being applied")
	}
	next, err := current.Next(idgen.New("run-exec-permission"), preference.Mode,
		preference.OperatorConfirmed, preference.RequestedBy,
		"applied Thread execution permission preference "+preference.ID+
			" to current Run; runtime authority reset", preference.CreatedAt)
	if err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	matrix, err := next.CapabilityMatrix()
	if err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	runOperation := domain.RunExecutionPermissionOperation{
		KeyDigest: runmutation.Fingerprint(
			"thread_execution_permission_current_run_operation.v1",
			operation.KeyDigest, run.ID, preference.ID),
		RequestFingerprint: runExecutionPermissionRequestFingerprint(next),
		SnapshotID:         next.ID, RunID: next.RunID, RequestedBy: next.RequestedBy,
		CreatedAt: next.CreatedAt,
	}
	event, err := events.New(next.RunID, next.MissionID,
		events.RunExecutionPermissionSelectedEvent, "run_execution_permission", next.ID,
		map[string]any{
			"protocol": next.ProtocolVersion, "revision": next.Revision,
			"from": current.Mode, "to": next.Mode,
			"approval_policy": next.ApprovalPolicy, "command_scope": next.CommandScope,
			"filesystem_scope": next.FilesystemScope, "network_scope": next.NetworkScope,
			"persistent_terminal":  next.PersistentTerminal,
			"background_process":   next.BackgroundProcess,
			"agent_terminal_input": next.AgentTerminalInput,
			"risk_tier":            next.RiskTier, "required_gate": next.RequiredGate,
			"policy_version": next.PolicyVersion, "requested_by": next.RequestedBy,
			"reason": next.Reason, "process_enabled": false,
			"execution_authorized": false, "capability_grant": false,
			"capability_matrix": map[string]any{
				"workspace_read":            matrix.WorkspaceRead,
				"workspace_write":           matrix.WorkspaceWrite,
				"sandboxed_command_runtime": matrix.SandboxedCommandRuntime,
				"unsandboxed_host_process":  matrix.UnsandboxedHostProcess,
				"network_access":            matrix.NetworkAccess,
				"credential_access":         matrix.CredentialAccess,
				"user_home_access":          matrix.UserHomeAccess,
				"persistent_user_terminal":  matrix.PersistentUserTerminal,
				"persistent_agent_terminal": matrix.PersistentAgentTerminal,
				"full_cdp":                  matrix.FullCDP,
				"out_of_scope_policy":       matrix.OutOfScopePolicy,
			},
		})
	if err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	event.CreatedAt = next.CreatedAt
	if err := validateRunExecutionPermissionMutation(next, runOperation, event); err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	if err := insertRunExecutionPermissionSnapshotTx(ctx, tx, next); err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_execution_permission_operations
		(operation_key_digest, request_fingerprint, snapshot_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`, runOperation.KeyDigest,
		runOperation.RequestFingerprint, runOperation.SnapshotID, runOperation.RunID,
		runOperation.RequestedBy, ts(runOperation.CreatedAt)); err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	operation.CurrentRunPermissionSnapshotID = next.ID
	if err := operation.Validate(); err != nil {
		return domain.ThreadExecutionPermissionOperation{}, err
	}
	return operation, nil
}

func threadPermissionTransitionRevokesHighRisk(current,
	target domain.RunExecutionPermissionMode,
) bool {
	return domain.PermissionTransitionRevokes(current, target)
}

func releaseRunExecutionLeaseForPermissionDowngradeTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, at time.Time,
) error {
	lease, found, err := getRunExecutionLeaseTx(ctx, tx, run.ID)
	if err != nil || !found || !lease.ActiveAt(at) {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_execution_leases
		SET status = 'released', released_at = ?
		WHERE run_id = ? AND lease_id = ? AND owner_id = ? AND generation = ?
			AND status = 'active'`, ts(at), lease.RunID, lease.LeaseID,
		lease.OwnerID, lease.Generation)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return apperror.New(apperror.CodeConflict,
			"Run execution lease changed before permission downgrade")
	}
	return appendSupervisorEventTx(ctx, tx, run,
		events.RunExecutionLeaseReleasedEvent, "thread_execution_permission",
		run.ID, map[string]any{"owner_id": lease.OwnerID,
			"generation": lease.Generation, "released_at": at,
			"reason": "higher-risk execution permission revoked"})
}

func runPermissionMatchesThreadPreference(current domain.RunExecutionPermissionSnapshot,
	preference domain.ThreadExecutionPermissionSnapshot,
) bool {
	return current.Mode == preference.Mode &&
		current.PolicyVersion == preference.PolicyVersion &&
		current.ApprovalPolicy == preference.ApprovalPolicy &&
		current.CommandScope == preference.CommandScope &&
		current.FilesystemScope == preference.FilesystemScope &&
		current.NetworkScope == preference.NetworkScope &&
		current.PersistentTerminal == preference.PersistentTerminal &&
		current.BackgroundProcess == preference.BackgroundProcess &&
		current.AgentTerminalInput == preference.AgentTerminalInput &&
		current.RiskTier == preference.RiskTier &&
		current.RequiredGate == preference.RequiredGate &&
		!current.ProcessEnabled && !current.ExecutionAuthorized &&
		!current.CapabilityGrant
}

func insertInitialThreadExecutionPermissionSnapshotTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.ThreadExecutionPermissionSnapshot, threadRecord domain.Thread,
) error {
	if err := snapshot.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"initial Thread execution permission is invalid", err)
	}
	if err := requireRedactedThreadExecutionPermissionSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Revision != 1 || snapshot.ThreadID != threadRecord.ID ||
		snapshot.MissionID != threadRecord.MissionID ||
		snapshot.Mode != domain.RunExecutionPermissionAsk ||
		threadRecord.Status != domain.ThreadActive ||
		snapshot.CreatedAt.Before(threadRecord.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"initial Thread execution permission does not match its active Thread")
	}
	return insertThreadExecutionPermissionSnapshotTx(ctx, tx, snapshot)
}

func insertThreadExecutionPermissionSnapshotTx(ctx context.Context, tx *sql.Tx,
	snapshot domain.ThreadExecutionPermissionSnapshot,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO thread_execution_permission_snapshots
		(id, thread_id, mission_id, revision, protocol_version, mode, approval_policy,
		command_scope, filesystem_scope, network_scope, persistent_terminal,
		background_process, agent_terminal_input, risk_tier, required_gate,
		policy_version, operator_confirmed, process_enabled, execution_authorized,
		capability_grant, requested_by, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.ID, snapshot.ThreadID, snapshot.MissionID, snapshot.Revision,
		snapshot.ProtocolVersion, snapshot.Mode, snapshot.ApprovalPolicy,
		snapshot.CommandScope, snapshot.FilesystemScope, snapshot.NetworkScope,
		snapshot.PersistentTerminal, snapshot.BackgroundProcess, snapshot.AgentTerminalInput,
		snapshot.RiskTier, snapshot.RequiredGate, snapshot.PolicyVersion,
		snapshot.OperatorConfirmed, snapshot.ProcessEnabled, snapshot.ExecutionAuthorized,
		snapshot.CapabilityGrant, snapshot.RequestedBy, snapshot.Reason,
		ts(snapshot.CreatedAt))
	return err
}

func validateThreadExecutionPermissionMutation(
	snapshot domain.ThreadExecutionPermissionSnapshot,
	operation domain.ThreadExecutionPermissionOperation,
) error {
	if err := snapshot.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Thread execution permission snapshot is invalid", err)
	}
	if err := requireRedactedThreadExecutionPermissionSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Revision <= 1 {
		return apperror.New(apperror.CodeInvalidArgument,
			"Thread execution permission transition revision must exceed one")
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Thread execution permission operation is invalid", err)
	}
	if operation.SnapshotID != snapshot.ID || operation.ThreadID != snapshot.ThreadID ||
		operation.RequestedBy != snapshot.RequestedBy ||
		!operation.CreatedAt.Equal(snapshot.CreatedAt) ||
		operation.RequestFingerprint != threadExecutionPermissionRequestFingerprint(snapshot) {
		return apperror.New(apperror.CodeInvalidArgument,
			"Thread execution permission operation does not match its snapshot")
	}
	return nil
}

func requireRedactedThreadExecutionPermissionSnapshot(
	snapshot domain.ThreadExecutionPermissionSnapshot,
) error {
	if redact.String(snapshot.RequestedBy) != snapshot.RequestedBy ||
		redact.String(snapshot.Reason) != snapshot.Reason {
		return apperror.New(apperror.CodeInvalidArgument,
			"Thread execution permission requester and reason must be redacted before persistence")
	}
	return nil
}

func acquireThreadExecutionPermissionWriteLockTx(ctx context.Context, tx *sql.Tx,
	threadID string,
) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE threads SET updated_at = updated_at WHERE id = ?`, threadID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeNotFound,
			"Thread execution permission Thread was not found")
	}
	return nil
}

func (s *SQLiteStore) recoverThreadExecutionPermissionTransition(ctx context.Context,
	operation domain.ThreadExecutionPermissionOperation, original error,
) (domain.ThreadExecutionPermissionSnapshot,
	domain.ThreadExecutionPermissionOperation, bool, error,
) {
	existing, found, err := getThreadExecutionPermissionOperation(
		ctx, s.db, operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.ThreadExecutionPermissionSnapshot{},
				domain.ThreadExecutionPermissionOperation{}, false, original
		}
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, errors.Join(original, err)
	}
	if err := validateThreadExecutionPermissionReplay(existing, operation); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	stored, err := getThreadExecutionPermissionSnapshot(ctx, s.db, existing.SnapshotID)
	if err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	if err := validateThreadExecutionPermissionOperationBinding(existing, stored); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{},
			domain.ThreadExecutionPermissionOperation{}, false, err
	}
	return stored, existing, true, nil
}

func validateThreadExecutionPermissionReplay(existing,
	request domain.ThreadExecutionPermissionOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.ThreadID != request.ThreadID || existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"Thread execution permission operation key was already used for different intent")
	}
	return nil
}

func validateThreadExecutionPermissionOperationBinding(
	operation domain.ThreadExecutionPermissionOperation,
	snapshot domain.ThreadExecutionPermissionSnapshot,
) error {
	if operation.SnapshotID != snapshot.ID || operation.ThreadID != snapshot.ThreadID ||
		operation.RequestedBy != snapshot.RequestedBy ||
		!operation.CreatedAt.Equal(snapshot.CreatedAt) ||
		operation.RequestFingerprint != threadExecutionPermissionRequestFingerprint(snapshot) {
		return apperror.New(apperror.CodeInternal,
			"stored Thread execution permission operation binding is invalid")
	}
	if operation.CurrentRunEffect == domain.ThreadExecutionPermissionNoActiveRun {
		if operation.CurrentRunID != "" || operation.CurrentRunPermissionSnapshotID != "" {
			return apperror.New(apperror.CodeInternal,
				"stored Thread execution permission no-active-Run effect is invalid")
		}
		return nil
	}
	if operation.CurrentRunEffect != domain.ThreadExecutionPermissionApplied &&
		operation.CurrentRunEffect != domain.ThreadExecutionPermissionPausedAndApplied &&
		operation.CurrentRunEffect != domain.ThreadExecutionPermissionDeferred {
		return apperror.New(apperror.CodeInternal,
			"stored Thread execution permission current Run effect is invalid")
	}
	return nil
}

func threadExecutionPermissionRequestFingerprint(
	snapshot domain.ThreadExecutionPermissionSnapshot,
) string {
	return runmutation.Fingerprint("thread_execution_permission_change_request.v1",
		snapshot.ThreadID, string(snapshot.Mode),
		fmt.Sprintf("%t", snapshot.OperatorConfirmed), snapshot.RequestedBy, snapshot.Reason)
}

func getThreadExecutionPermissionSnapshot(ctx context.Context,
	queryer threadExecutionPermissionQueryer, id string,
) (domain.ThreadExecutionPermissionSnapshot, error) {
	return scanThreadExecutionPermissionSnapshot(queryer.QueryRowContext(ctx,
		threadExecutionPermissionSnapshotSelect+` WHERE id = ?`, id))
}

func getCurrentThreadExecutionPermissionSnapshot(ctx context.Context,
	queryer threadExecutionPermissionQueryer, threadID string,
) (domain.ThreadExecutionPermissionSnapshot, error) {
	return scanThreadExecutionPermissionSnapshot(queryer.QueryRowContext(ctx,
		threadExecutionPermissionSnapshotSelect+
			` WHERE thread_id = ? ORDER BY revision DESC LIMIT 1`, threadID))
}

func scanThreadExecutionPermissionSnapshot(scanner interface{ Scan(...any) error }) (
	domain.ThreadExecutionPermissionSnapshot, error,
) {
	var snapshot domain.ThreadExecutionPermissionSnapshot
	var createdAt string
	if err := scanner.Scan(&snapshot.ID, &snapshot.ThreadID, &snapshot.MissionID,
		&snapshot.Revision, &snapshot.ProtocolVersion, &snapshot.Mode,
		&snapshot.ApprovalPolicy, &snapshot.CommandScope, &snapshot.FilesystemScope,
		&snapshot.NetworkScope, &snapshot.PersistentTerminal, &snapshot.BackgroundProcess,
		&snapshot.AgentTerminalInput, &snapshot.RiskTier, &snapshot.RequiredGate,
		&snapshot.PolicyVersion, &snapshot.OperatorConfirmed, &snapshot.ProcessEnabled,
		&snapshot.ExecutionAuthorized, &snapshot.CapabilityGrant, &snapshot.RequestedBy,
		&snapshot.Reason, &createdAt); err != nil {
		return domain.ThreadExecutionPermissionSnapshot{}, err
	}
	snapshot.CreatedAt = parseTS(createdAt)
	return snapshot, snapshot.Validate()
}

func getThreadExecutionPermissionOperation(ctx context.Context,
	queryer threadExecutionPermissionQueryer, keyDigest string,
) (domain.ThreadExecutionPermissionOperation, bool, error) {
	var operation domain.ThreadExecutionPermissionOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, snapshot_id, thread_id, requested_by,
		COALESCE(current_run_id, ''), current_run_effect,
		COALESCE(current_run_permission_snapshot_id, ''), created_at
		FROM thread_execution_permission_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&operation.KeyDigest, &operation.RequestFingerprint, &operation.SnapshotID,
			&operation.ThreadID, &operation.RequestedBy, &operation.CurrentRunID,
			&operation.CurrentRunEffect, &operation.CurrentRunPermissionSnapshotID,
			&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadExecutionPermissionOperation{}, false, nil
	}
	if err != nil {
		return domain.ThreadExecutionPermissionOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

// materializeThreadExecutionPermissionForSuccessorTx changes only the current
// immutable Run permission snapshot for a just-created successor. The Run was
// first created with its ordinary conservative snapshot, and no authority,
// lease, approval, process, adapter, network, or credential fact is copied.
func materializeThreadExecutionPermissionForSuccessorTx(ctx context.Context, tx *sql.Tx,
	threadRecord domain.Thread, run domain.Run,
) (domain.RunExecutionPermissionSnapshot, bool, error) {
	preference, err := getCurrentThreadExecutionPermissionSnapshot(ctx, tx, threadRecord.ID)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, false, err
	}
	current, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, false, err
	}
	// Successors write only the current three-mode schema, even when their
	// immutable Thread preference is historical. This projection grants no
	// process-local Full activation and never rewrites the historical row.
	mode := domain.RunExecutionPermissionMode(preference.Mode.ApprovalPreference())
	if mode == current.Mode {
		return current, false, nil
	}
	at := run.CreatedAt
	if at.Before(current.CreatedAt) {
		at = current.CreatedAt
	}
	if at.Before(preference.CreatedAt) {
		at = preference.CreatedAt
	}
	next, err := current.Next(idgen.New("run-exec-permission"), mode,
		mode == domain.RunExecutionPermissionFull, preference.RequestedBy,
		"materialized Thread execution permission preference "+preference.ID+
			" for successor Run; runtime authority reset", at)
	if err != nil {
		return domain.RunExecutionPermissionSnapshot{}, false, err
	}
	if err := insertRunExecutionPermissionSnapshotTx(ctx, tx, next); err != nil {
		return domain.RunExecutionPermissionSnapshot{}, false, err
	}
	operation := domain.RunExecutionPermissionOperation{
		KeyDigest: runmutation.Fingerprint(
			"thread_execution_permission_materialization_operation.v1",
			threadRecord.ID, preference.ID, run.ID),
		RequestFingerprint: runExecutionPermissionRequestFingerprint(next),
		SnapshotID:         next.ID, RunID: run.ID, RequestedBy: next.RequestedBy,
		CreatedAt: next.CreatedAt,
	}
	if err := operation.Validate(); err != nil {
		return domain.RunExecutionPermissionSnapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_execution_permission_operations
		(operation_key_digest, request_fingerprint, snapshot_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.SnapshotID, operation.RunID,
		operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		return domain.RunExecutionPermissionSnapshot{}, false, err
	}
	return next, true, nil
}

func executionModeIncludesFullCDP(mode domain.RunExecutionPermissionMode) bool {
	return mode.IncludesFullAccess()
}

// synchronizeRunBrowserCDPForThreadPermissionTx keeps Full CDP as a
// user-controllable sub-capability of Full Access and Debug. Entering either
// high-risk mode from a lower mode defaults the sub-capability on. Leaving the
// high-risk modes always turns it off. Moving between Full Access and Debug
// preserves the operator's current Full CDP choice.
func synchronizeRunBrowserCDPForThreadPermissionTx(ctx context.Context, tx *sql.Tx,
	runID, missionID string, previousMode domain.RunExecutionPermissionMode,
	preference domain.ThreadExecutionPermissionSnapshot, parentOperationDigest string,
) (domain.RunBrowserCDPPermissionSnapshot, bool, error) {
	current, err := getCurrentRunBrowserCDPPermissionSnapshot(ctx, tx, runID)
	if err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	if current.RunID != runID || current.MissionID != missionID {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, apperror.New(
			apperror.CodeConflict,
			"Run browser CDP permission does not match the Thread permission scope")
	}
	desired := domain.RunBrowserCDPPermissionRestricted
	if executionModeIncludesFullCDP(preference.Mode) {
		if executionModeIncludesFullCDP(previousMode) {
			// Full Access <-> Debug is a ceiling change, not a request to
			// override the operator's Full CDP sub-switch.
			return current, false, nil
		}
		desired = domain.RunBrowserCDPPermissionFullDebug
	}
	if current.Mode == desired {
		return current, false, nil
	}
	reason := "enabled Full CDP by default for Thread execution permission " + preference.ID
	if desired == domain.RunBrowserCDPPermissionRestricted {
		reason = "disabled Full CDP because Thread execution permission " + preference.ID +
			" does not include it"
	}
	return appendThreadManagedRunBrowserCDPTransitionTx(ctx, tx, current, desired,
		preference.RequestedBy, reason, preference.CreatedAt,
		runmutation.Fingerprint("thread_execution_permission_browser_cdp_operation.v1",
			parentOperationDigest, runID, preference.ID))
}

// materializeThreadBrowserCDPPermissionForSuccessorTx copies only the durable
// Full CDP sub-switch. It never copies transport, browser-start, runtime, or
// capability authority. Lower execution modes are always restricted. For a
// high-risk mode a valid predecessor supplies the operator's last sub-switch;
// without one the product default is Full CDP enabled.
func materializeThreadBrowserCDPPermissionForSuccessorTx(ctx context.Context, tx *sql.Tx,
	threadRecord domain.Thread, predecessor domain.Run, run domain.Run,
	preference domain.ThreadExecutionPermissionSnapshot,
) (domain.RunBrowserCDPPermissionSnapshot, bool, error) {
	current, err := getCurrentRunBrowserCDPPermissionSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	desired := domain.RunBrowserCDPPermissionRestricted
	if executionModeIncludesFullCDP(preference.Mode) {
		desired = domain.RunBrowserCDPPermissionFullDebug
		defaultOn, err := threadPermissionEnteredHighWithoutActiveRunTx(
			ctx, tx, preference)
		if err != nil {
			return domain.RunBrowserCDPPermissionSnapshot{}, false, err
		}
		if !defaultOn && predecessor.ID != "" &&
			predecessor.MissionID == threadRecord.MissionID {
			predecessorExecution, err := getCurrentRunExecutionPermissionSnapshot(
				ctx, tx, predecessor.ID)
			if err != nil {
				return domain.RunBrowserCDPPermissionSnapshot{}, false, err
			}
			// Restricted is not an operator Full-CDP choice while the
			// predecessor itself was below Full Access. In that case the new
			// high-risk preference uses the documented default-on behavior.
			if executionModeIncludesFullCDP(predecessorExecution.Mode) {
				inherited, err := getCurrentRunBrowserCDPPermissionSnapshot(ctx, tx,
					predecessor.ID)
				if err != nil {
					return domain.RunBrowserCDPPermissionSnapshot{}, false, err
				}
				desired = inherited.Mode
			}
		}
	}
	if current.Mode == desired {
		return current, false, nil
	}
	at := run.CreatedAt
	if at.Before(preference.CreatedAt) {
		at = preference.CreatedAt
	}
	return appendThreadManagedRunBrowserCDPTransitionTx(ctx, tx, current, desired,
		preference.RequestedBy,
		"materialized Thread Full CDP preference for successor Run "+run.ID, at,
		runmutation.Fingerprint(
			"thread_execution_permission_browser_cdp_materialization_operation.v1",
			threadRecord.ID, preference.ID, predecessor.ID, run.ID, string(desired)))
}

// threadPermissionEnteredHighWithoutActiveRunTx distinguishes a new
// low-to-high Thread preference generation from a stable high-risk preference.
// A terminal predecessor may still carry an explicitly disabled Full CDP
// snapshot; that old choice must not override the documented default-on
// behavior when the operator subsequently selected a low mode and then entered
// Full Access or Debug while there was no active Run. Full <-> Debug and
// same-mode Full confirmations keep inheriting the predecessor's explicit
// sub-switch because their previous Thread preference was already high.
func threadPermissionEnteredHighWithoutActiveRunTx(ctx context.Context, tx *sql.Tx,
	preference domain.ThreadExecutionPermissionSnapshot,
) (bool, error) {
	if !executionModeIncludesFullCDP(preference.Mode) || preference.Revision <= 1 {
		return false, nil
	}
	var previousMode string
	var currentRunID sql.NullString
	var currentRunEffect string
	err := tx.QueryRowContext(ctx, `SELECT previous.mode,
		operation.current_run_id, operation.current_run_effect
		FROM thread_execution_permission_snapshots AS previous
		JOIN thread_execution_permission_operations AS operation
			ON operation.snapshot_id = ?
		WHERE previous.thread_id = ? AND previous.revision = ?`,
		preference.ID, preference.ThreadID, preference.Revision-1).
		Scan(&previousMode, &currentRunID, &currentRunEffect)
	if err != nil {
		return false, apperror.Wrap(apperror.CodeConflict,
			"Thread Full CDP preference generation is incomplete", err)
	}
	parsedPrevious, err := domain.ParseRunExecutionPermissionMode(previousMode)
	if err != nil {
		return false, apperror.Wrap(apperror.CodeConflict,
			"previous Thread execution permission mode is invalid", err)
	}
	return !executionModeIncludesFullCDP(parsedPrevious) &&
		!currentRunID.Valid &&
		currentRunEffect == string(domain.ThreadExecutionPermissionNoActiveRun), nil
}

func appendThreadManagedRunBrowserCDPTransitionTx(ctx context.Context, tx *sql.Tx,
	current domain.RunBrowserCDPPermissionSnapshot,
	desired domain.RunBrowserCDPPermissionMode, requestedBy, reason string,
	at time.Time, operationDigest string,
) (domain.RunBrowserCDPPermissionSnapshot, bool, error) {
	if current.Mode == desired {
		return current, false, nil
	}
	if at.Before(current.CreatedAt) {
		at = current.CreatedAt
	}
	next, err := current.Next(idgen.New("run-browser-cdp-permission"), desired,
		desired == domain.RunBrowserCDPPermissionFullDebug, requestedBy, reason, at)
	if err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	operation := domain.RunBrowserCDPPermissionOperation{
		KeyDigest:          operationDigest,
		RequestFingerprint: runBrowserCDPPermissionRequestFingerprint(next),
		SnapshotID:         next.ID, RunID: next.RunID, RequestedBy: next.RequestedBy,
		CreatedAt: next.CreatedAt,
	}
	event, err := newThreadManagedRunBrowserCDPPermissionSelectedEvent(current, next)
	if err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	if err := validateRunBrowserCDPPermissionMutation(next, operation, event); err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	if err := insertRunBrowserCDPPermissionSnapshotTx(ctx, tx, next); err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_browser_cdp_permission_operations
		(operation_key_digest, request_fingerprint, snapshot_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.SnapshotID, operation.RunID,
		operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.RunBrowserCDPPermissionSnapshot{}, false, err
	}
	return next, true, nil
}

func newThreadManagedRunBrowserCDPPermissionSelectedEvent(
	current, next domain.RunBrowserCDPPermissionSnapshot,
) (events.Event, error) {
	event, err := events.New(next.RunID, next.MissionID,
		events.RunBrowserCDPPermissionSelectedEvent, "run_browser_cdp_permission",
		next.ID, map[string]any{
			"protocol": next.ProtocolVersion, "revision": next.Revision,
			"from": current.Mode, "to": next.Mode,
			"navigate_allowed":         next.NavigateAllowed,
			"dom_snapshot_allowed":     next.DOMSnapshotAllowed,
			"screenshot_allowed":       next.ScreenshotAllowed,
			"request_capture_allowed":  next.RequestCaptureAllowed,
			"request_mutation_allowed": next.RequestMutationAllowed,
			"request_replay_allowed":   next.RequestReplayAllowed,
			"cookie_access_allowed":    next.CookieAccessAllowed,
			"arbitrary_method_allowed": next.ArbitraryMethodAllowed,
			"risk_tier":                next.RiskTier, "required_gate": next.RequiredGate,
			"policy_version": next.PolicyVersion, "requested_by": next.RequestedBy,
			"reason": next.Reason, "transport_enabled": false,
			"browser_start_authorized": false, "runtime_authorized": false,
			"capability_grant": false,
		})
	if err != nil {
		return events.Event{}, err
	}
	event.CreatedAt = next.CreatedAt
	return event, nil
}
