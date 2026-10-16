package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

const defaultGrantListLimit = 100
const maxGrantListLimit = 500

func (s *SQLiteStore) CreateSessionGrant(ctx context.Context, request approval.CreateGrantRequest) (approval.GrantResult, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return approval.GrantResult{}, err
	}
	normalized.Reason = redact.String(normalized.Reason)
	normalized.GrantedBy = redact.String(normalized.GrantedBy)
	if normalized.ToolName == "command_runtime" && normalized.ScopeFingerprint == "" {
		return approval.GrantResult{}, errors.New("command grants must have explicit bounded scope and limits")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return approval.GrantResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	binding, bound, err := runBindingForSessionTx(ctx, tx, normalized.SessionID)
	if err != nil {
		return approval.GrantResult{}, err
	}
	if !bound {
		return approval.GrantResult{}, errors.New("session approval grants require a Run-bound session")
	}
	if normalized.WorkspaceID == "" {
		normalized.WorkspaceID = binding.WorkspaceID
	}
	if normalized.WorkspaceID != binding.WorkspaceID {
		return approval.GrantResult{}, errors.New("approval grant workspace does not match the attached run")
	}
	if !grantToolClassMatches(normalized.ToolName, normalized.ActionClass) {
		return approval.GrantResult{}, errors.New("approval grant tool and action class do not match")
	}
	fingerprint := approval.GrantRequestFingerprint(normalized)
	operationKey := approval.GrantOperationKeyDigest(normalized.IdempotencyKey)
	operation, found, err := getGrantOperationTx(ctx, tx, operationKey)
	if err != nil {
		return approval.GrantResult{}, err
	}
	if found {
		grant, err := getSessionGrantTx(ctx, tx, operation.GrantID)
		if err != nil {
			return approval.GrantResult{}, err
		}
		if normalized.ToolName == "command_runtime" {
			// Command generation is host bookkeeping, estimated before this
			// transaction. A racing replay uses the saved generation; all
			// requested scope, limits and review metadata still bind the key.
			normalized.Generation = grant.Generation
			fingerprint = approval.GrantRequestFingerprint(normalized)
		}
		if operation.Action != "grant" || operation.RequestFingerprint != fingerprint || operation.ResultStatus != approval.GrantActive {
			return approval.GrantResult{}, errors.New("approval grant idempotency key was already used for a different operation")
		}
		if err := tx.Commit(); err != nil {
			return approval.GrantResult{}, err
		}
		return approval.GrantResult{Grant: grant, Replayed: true}, nil
	}
	if err := requireGrantableRunTx(ctx, tx, binding.RunID); err != nil {
		return approval.GrantResult{}, err
	}
	if normalized.ScopeFingerprint != "" {
		if err := reconcileBoundedGrantScopeTx(ctx, tx, binding.RunID,
			normalized, time.Now().UTC()); err != nil {
			return approval.GrantResult{}, err
		}
	}
	grant, found, err := findActiveSessionGrant(ctx, tx, approval.GrantQuery{
		RunID: binding.RunID, SessionID: normalized.SessionID, WorkspaceID: normalized.WorkspaceID,
		ToolName: normalized.ToolName, ActionClass: normalized.ActionClass,
		ScopeFingerprint: normalized.ScopeFingerprint,
		ModeSnapshotID:   normalized.ModeSnapshotID, ModeRevision: normalized.ModeRevision,
		InteractionSnapshotID:      normalized.InteractionSnapshotID,
		InteractionRevision:        normalized.InteractionRevision,
		ExecutionProfileSnapshotID: normalized.ExecutionProfileSnapshotID,
		ExecutionProfileRevision:   normalized.ExecutionProfileRevision,
		PermissionSnapshotID:       normalized.PermissionSnapshotID,
		PermissionRevision:         normalized.PermissionRevision,
		PermissionMode:             normalized.PermissionMode,
		WorkspaceRootFingerprint:   normalized.WorkspaceRootFingerprint,
		CapabilityGeneration:       normalized.CapabilityGeneration,
	})
	if err != nil {
		return approval.GrantResult{}, err
	}
	if found && normalized.ScopeFingerprint != "" {
		if normalized.ToolName == "command_runtime" {
			// The exact query above already binds every authority/scope field.
			// A different exact command can supply different review metadata,
			// but reusing its group must retain the original TTL and use cap.
			if grant.ExpiresAt == nil || grant.MaxUses != normalized.MaxUses || grant.ExpiresAt.Sub(grant.CreatedAt) != normalized.TTL {
				return approval.GrantResult{}, errors.New("active bounded approval grant has different limits or authority")
			}
			normalized.Generation = grant.Generation
			fingerprint = approval.GrantRequestFingerprint(normalized)
		} else if grant.RequestFingerprint != fingerprint {
			return approval.GrantResult{}, errors.New("active bounded approval grant has different limits or authority")
		}
	}
	if !found {
		now := time.Now().UTC()
		var expiresAt *time.Time
		if normalized.ScopeFingerprint != "" {
			expires := now.Add(normalized.TTL)
			expiresAt = &expires
		}
		grant = approval.SessionGrant{
			ID: idgen.New("grant"), RunID: binding.RunID, SessionID: normalized.SessionID,
			WorkspaceID: normalized.WorkspaceID, ToolName: normalized.ToolName, ActionClass: normalized.ActionClass,
			Status: approval.GrantActive, RequestFingerprint: fingerprint, Reason: normalized.Reason,
			GrantedBy: normalized.GrantedBy, Version: 1, CreatedAt: now, UpdatedAt: now,
			ScopeFingerprint: normalized.ScopeFingerprint,
			Generation:       normalized.Generation, MaxUses: normalized.MaxUses,
			UsesRemaining: normalized.MaxUses, ExpiresAt: expiresAt,
			ModeSnapshotID: normalized.ModeSnapshotID, ModeRevision: normalized.ModeRevision,
			InteractionSnapshotID:      normalized.InteractionSnapshotID,
			InteractionRevision:        normalized.InteractionRevision,
			ExecutionProfileSnapshotID: normalized.ExecutionProfileSnapshotID,
			ExecutionProfileRevision:   normalized.ExecutionProfileRevision,
			PermissionSnapshotID:       normalized.PermissionSnapshotID,
			PermissionRevision:         normalized.PermissionRevision,
			PermissionMode:             normalized.PermissionMode,
			WorkspaceRootFingerprint:   normalized.WorkspaceRootFingerprint,
			CapabilityGeneration:       normalized.CapabilityGeneration,
		}
		if err := grant.Validate(); err != nil {
			return approval.GrantResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO approval_session_grants
			(id, run_id, session_id, workspace_id, tool_name, action_class, status, request_fingerprint,
			 reason, revocation_reason, granted_by, revoked_by, version, created_at, updated_at, revoked_at,
			 scope_fingerprint, grant_generation, max_uses, uses_remaining, expires_at,
			 mode_snapshot_id, mode_revision, interaction_snapshot_id, interaction_revision,
			 execution_profile_snapshot_id, execution_profile_revision,
			 permission_snapshot_id, permission_revision, permission_mode,
			 workspace_root_fingerprint, capability_generation)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', ?, ?, ?, NULL,
			 ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			grant.ID, grant.RunID, grant.SessionID, grant.WorkspaceID, grant.ToolName, grant.ActionClass,
			grant.Status, grant.RequestFingerprint, grant.Reason, grant.GrantedBy, grant.Version,
			ts(grant.CreatedAt), ts(grant.UpdatedAt), grant.ScopeFingerprint,
			grant.Generation, grant.MaxUses, grant.UsesRemaining, nullableTS(grant.ExpiresAt),
			grant.ModeSnapshotID, grant.ModeRevision, grant.InteractionSnapshotID,
			grant.InteractionRevision, grant.ExecutionProfileSnapshotID,
			grant.ExecutionProfileRevision, grant.PermissionSnapshotID,
			grant.PermissionRevision, grant.PermissionMode,
			grant.WorkspaceRootFingerprint, grant.CapabilityGeneration); err != nil {
			return approval.GrantResult{}, err
		}
		if err := appendGrantEventTx(ctx, tx, grant, events.ApprovalGrantCreatedEvent); err != nil {
			return approval.GrantResult{}, err
		}
	}
	if err := insertGrantOperationTx(ctx, tx, operationKey, grant.ID, "grant", fingerprint, approval.GrantActive); err != nil {
		return approval.GrantResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.GrantResult{}, err
	}
	return approval.GrantResult{Grant: grant, Replayed: found}, nil
}

func reconcileBoundedGrantScopeTx(ctx context.Context, tx *sql.Tx, runID string,
	request approval.CreateGrantRequest, at time.Time,
) error {
	grant, err := getSessionGrantRow(tx.QueryRowContext(ctx, grantSelect+`
		WHERE run_id = ? AND session_id = ? AND workspace_id = ? AND tool_name = ?
		AND action_class = ? AND scope_fingerprint = ? AND status = ?`, runID,
		request.SessionID, request.WorkspaceID, request.ToolName, request.ActionClass,
		request.ScopeFingerprint, approval.GrantActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !grant.Bounded() {
		return errors.New("bounded risk escalation scope resolved to a legacy approval grant")
	}
	reasonCode := ""
	reason := ""
	eventType := events.ApprovalGrantInvalidatedEvent
	if grant.ExpiresAt == nil || !at.Before(*grant.ExpiresAt) {
		reasonCode = "expired"
		reason = "bounded risk escalation grant expired"
		eventType = events.ApprovalGrantExpiredEvent
	} else if grant.UsesRemaining <= 0 {
		reasonCode = "uses_exhausted"
		reason = "bounded risk escalation grant has no remaining uses"
	} else if grant.ModeSnapshotID != request.ModeSnapshotID ||
		grant.ModeRevision != request.ModeRevision ||
		grant.InteractionSnapshotID != request.InteractionSnapshotID ||
		grant.InteractionRevision != request.InteractionRevision ||
		grant.ExecutionProfileSnapshotID != request.ExecutionProfileSnapshotID ||
		grant.ExecutionProfileRevision != request.ExecutionProfileRevision ||
		grant.PermissionSnapshotID != request.PermissionSnapshotID ||
		grant.PermissionRevision != request.PermissionRevision ||
		grant.PermissionMode != request.PermissionMode ||
		grant.WorkspaceRootFingerprint != request.WorkspaceRootFingerprint ||
		grant.CapabilityGeneration != request.CapabilityGeneration {
		reasonCode = "authority_drift"
		reason = "bounded risk escalation grant authority changed before replacement"
	}
	if reasonCode == "" {
		return nil
	}
	return endBoundedGrantTx(ctx, tx, &grant, reasonCode, reason,
		"approval_store", at, eventType)
}

func (s *SQLiteStore) RevokeSessionGrant(ctx context.Context, request approval.RevokeGrantRequest) (approval.GrantResult, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return approval.GrantResult{}, err
	}
	normalized.Reason = redact.String(normalized.Reason)
	normalized.RevokedBy = redact.String(normalized.RevokedBy)
	fingerprint := approval.GrantRevocationFingerprint(normalized)
	operationKey := approval.GrantOperationKeyDigest(normalized.IdempotencyKey)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return approval.GrantResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	operation, found, err := getGrantOperationTx(ctx, tx, operationKey)
	if err != nil {
		return approval.GrantResult{}, err
	}
	if found {
		if operation.Action != "revoke" || operation.GrantID != normalized.GrantID ||
			operation.RequestFingerprint != fingerprint || operation.ResultStatus != approval.GrantRevoked {
			return approval.GrantResult{}, errors.New("approval grant idempotency key was already used for a different operation")
		}
		grant, err := getSessionGrantTx(ctx, tx, operation.GrantID)
		if err != nil {
			return approval.GrantResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return approval.GrantResult{}, err
		}
		return approval.GrantResult{Grant: grant, Replayed: true}, nil
	}
	grant, err := getSessionGrantTx(ctx, tx, normalized.GrantID)
	if err != nil {
		return approval.GrantResult{}, err
	}
	changed := false
	if grant.Status == approval.GrantActive {
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE approval_session_grants SET status = ?, revocation_reason = ?,
			revoked_by = ?, version = version + 1, updated_at = ?, revoked_at = ?
			WHERE id = ? AND version = ? AND status = ?`, approval.GrantRevoked, normalized.Reason,
			normalized.RevokedBy, ts(now), ts(now), grant.ID, grant.Version, approval.GrantActive)
		if err != nil {
			return approval.GrantResult{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return approval.GrantResult{}, err
		}
		if rows != 1 {
			return approval.GrantResult{}, errors.New("approval grant changed concurrently")
		}
		grant.Status = approval.GrantRevoked
		grant.RevocationReason = normalized.Reason
		grant.RevokedBy = normalized.RevokedBy
		grant.Version++
		grant.UpdatedAt = now
		grant.RevokedAt = &now
		changed = true
		if err := appendGrantEventTx(ctx, tx, grant, events.ApprovalGrantRevokedEvent); err != nil {
			return approval.GrantResult{}, err
		}
	}
	if err := grant.Validate(); err != nil {
		return approval.GrantResult{}, err
	}
	if err := insertGrantOperationTx(ctx, tx, operationKey, grant.ID, "revoke", fingerprint, approval.GrantRevoked); err != nil {
		return approval.GrantResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.GrantResult{}, err
	}
	return approval.GrantResult{Grant: grant, Replayed: !changed}, nil
}

func (s *SQLiteStore) AuthorizeApprovalWithSessionGrant(ctx context.Context, proposalID string, grantID string) (approval.DecisionResult, error) {
	return s.authorizeApprovalWithSessionGrant(ctx, proposalID, grantID, nil)
}

func (s *SQLiteStore) authorizeApprovalWithSessionGrant(ctx context.Context, proposalID string, grantID string, commandReview *approval.DecisionRequest) (approval.DecisionResult, error) {
	proposalID = strings.TrimSpace(proposalID)
	grantID = strings.TrimSpace(grantID)
	if err := validateApprovalFilterIdentity("proposal id", proposalID, false); err != nil {
		return approval.DecisionResult{}, err
	}
	if err := validateApprovalFilterIdentity("grant id", grantID, false); err != nil {
		return approval.DecisionResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return approval.DecisionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := getApprovalTx(ctx, tx, "", proposalID)
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if record.ToolName == "host_command_propose" {
		return approval.DecisionResult{}, errors.New("historical host grant consumption is retired")
	}
	if record.ToolName == mcp.OperationApprovalTool {
		return approval.DecisionResult{}, errors.New("MCP grants require the built-in application scope verifier")
	}
	if (record.ToolName == "command_runtime") != (commandReview != nil) {
		return approval.DecisionResult{}, errors.New("command grant consumption requires an explicit exact operator review")
	}
	if record.Status == approval.StatusApproved && record.GrantID == grantID {
		consumption, found, loadErr := getGrantConsumptionByProposalTx(ctx, tx, proposalID)
		if loadErr != nil {
			return approval.DecisionResult{}, loadErr
		}
		if commandReview != nil {
			if !found || consumption.Validate() != nil || consumption.ApprovalID != record.ID ||
				consumption.GrantID != grantID || consumption.RunID != record.RunID {
				return approval.DecisionResult{}, errors.New("bounded command replay has no matching consumption")
			}
			if err := sealCommandGrantReviewTx(ctx, tx, record, grantID, *commandReview); err != nil {
				return approval.DecisionResult{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return approval.DecisionResult{}, err
		}
		result := approval.DecisionResult{Approval: record, Replayed: true}
		if found {
			result.Consumption = &consumption
		}
		return result, nil
	}
	if record.Status != approval.StatusPending {
		return approval.DecisionResult{}, fmt.Errorf("approval %s is already %s", record.ID, record.Status)
	}
	grant, err := getSessionGrantTx(ctx, tx, grantID)
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if commandReview != nil && !grant.Bounded() {
		return approval.DecisionResult{}, errors.New("command approvals require a bounded grant")
	}
	if grant.Status != approval.GrantActive || grant.RunID != record.RunID || grant.SessionID != record.SessionID ||
		grant.WorkspaceID != record.WorkspaceID || grant.ToolName != record.ToolName || grant.ActionClass != record.ActionClass {
		return approval.DecisionResult{}, errors.New("session grant does not authorize this approval scope")
	}
	if err := requireGrantableRunTx(ctx, tx, grant.RunID); err != nil {
		return approval.DecisionResult{}, err
	}
	now := time.Now().UTC()
	var consumption *approval.GrantConsumption
	if grant.Bounded() {
		query, err := boundedApprovalGrantScopeTx(ctx, tx, record)
		if err != nil {
			return approval.DecisionResult{}, err
		}
		if grant.ExpiresAt == nil || !now.Before(*grant.ExpiresAt) {
			if err := endBoundedGrantTx(ctx, tx, &grant, "expired",
				"bounded risk escalation grant expired", "approval_store", now,
				events.ApprovalGrantExpiredEvent); err != nil {
				return approval.DecisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return approval.DecisionResult{}, err
			}
			return approval.DecisionResult{}, errors.New("bounded risk escalation grant expired")
		}
		if grant.UsesRemaining <= 0 {
			return approval.DecisionResult{}, errors.New("bounded risk escalation grant has no remaining uses")
		}
		if !approval.MatchesBoundedGrantScope(grant, query) {
			return approval.DecisionResult{}, errors.New("bounded approval grant exact scope is stale")
		}
		useOrdinal := grant.MaxUses - grant.UsesRemaining + 1
		value := approval.GrantConsumption{ID: idgen.New("grant-consumption"),
			GrantID: grant.ID, ProposalID: record.ProposalID, ApprovalID: record.ID,
			RunID: grant.RunID, ScopeFingerprint: grant.ScopeFingerprint,
			GrantGeneration: grant.Generation, UseOrdinal: useOrdinal, CreatedAt: now}
		value.Fingerprint = approval.GrantConsumptionFingerprint(value)
		if err := value.Validate(); err != nil {
			return approval.DecisionResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO approval_grant_consumptions
			(id, grant_id, proposal_id, approval_id, run_id, scope_fingerprint,
			grant_generation, use_ordinal, consumption_fingerprint, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.GrantID,
			value.ProposalID, value.ApprovalID, value.RunID, value.ScopeFingerprint,
			value.GrantGeneration, value.UseOrdinal, value.Fingerprint,
			ts(value.CreatedAt)); err != nil {
			return approval.DecisionResult{}, err
		}
		grant.UsesRemaining--
		grant.Version++
		grant.UpdatedAt = now
		if grant.UsesRemaining == 0 {
			grant.Status = approval.GrantRevoked
			grant.RevocationReason = "bounded grant uses exhausted"
			grant.RevokedBy = "approval_store"
			grant.RevokedAt = &now
		}
		result, err := tx.ExecContext(ctx, `UPDATE approval_session_grants SET
			uses_remaining = ?, status = ?, revocation_reason = ?, revoked_by = ?,
			version = ?, updated_at = ?, revoked_at = ?
			WHERE id = ? AND version = ? AND status = ? AND uses_remaining = ?`,
			grant.UsesRemaining, grant.Status, grant.RevocationReason, grant.RevokedBy,
			grant.Version, ts(grant.UpdatedAt), nullableTS(grant.RevokedAt), grant.ID,
			grant.Version-1, approval.GrantActive, grant.UsesRemaining+1)
		if err != nil {
			return approval.DecisionResult{}, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return approval.DecisionResult{}, err
			}
			return approval.DecisionResult{}, errors.New("bounded grant changed concurrently")
		}
		if err := appendGrantEventTx(ctx, tx, grant,
			events.ApprovalGrantConsumedEvent); err != nil {
			return approval.DecisionResult{}, err
		}
		if grant.Status == approval.GrantRevoked {
			if err := appendGrantEventTx(ctx, tx, grant,
				events.ApprovalGrantInvalidatedEvent); err != nil {
				return approval.DecisionResult{}, err
			}
		}
		consumption = &value
	}
	// Expiry above commits its lifecycle event but not a successful decision.
	// Seal only after validation and consumption; any key conflict rolls the
	// consumption back in this same transaction.
	if commandReview != nil {
		if err := sealCommandGrantReviewTx(ctx, tx, record, grantID, *commandReview); err != nil {
			return approval.DecisionResult{}, err
		}
	}
	reason := "authorized by active session grant"
	reviewedBy := "session_grant"
	if commandReview != nil {
		reviewedBy = commandReview.ReviewedBy
	}
	requiredGrantStatus := approval.GrantActive
	if grant.Bounded() && grant.UsesRemaining == 0 {
		requiredGrantStatus = approval.GrantRevoked
	}
	result, err := tx.ExecContext(ctx, `UPDATE tool_approvals SET status = ?, grant_id = ?, decision_reason = ?,
		reviewed_by = ?, version = version + 1, updated_at = ?, decided_at = ?
		WHERE id = ? AND version = ? AND status = ? AND EXISTS
			(SELECT 1 FROM approval_session_grants WHERE id = ? AND status = ?)`,
		approval.StatusApproved, grant.ID, reason, reviewedBy, ts(now), ts(now), record.ID,
		record.Version, approval.StatusPending, grant.ID,
		requiredGrantStatus)
	if err != nil {
		return approval.DecisionResult{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if rows != 1 {
		return approval.DecisionResult{}, errors.New("approval or session grant changed concurrently")
	}
	record.Status = approval.StatusApproved
	record.GrantID = grant.ID
	record.DecisionReason = reason
	record.ReviewedBy = reviewedBy
	record.Version++
	record.UpdatedAt = now
	record.DecidedAt = &now
	if err := record.Validate(); err != nil {
		return approval.DecisionResult{}, err
	}
	if err := appendApprovalEventTx(ctx, tx, record, events.ApprovalDecidedEvent); err != nil {
		return approval.DecisionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.DecisionResult{}, err
	}
	return approval.DecisionResult{Approval: record, Consumption: consumption}, nil
}

func (s *SQLiteStore) FindActiveSessionGrant(ctx context.Context, query approval.GrantQuery) (approval.SessionGrant, bool, error) {
	return findActiveSessionGrant(ctx, s.db, query)
}

func (s *SQLiteStore) GetSessionGrant(ctx context.Context, id string) (approval.SessionGrant, error) {
	id = strings.TrimSpace(id)
	if err := validateApprovalFilterIdentity("grant id", id, false); err != nil {
		return approval.SessionGrant{}, err
	}
	return getSessionGrantRow(s.db.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, id))
}

func (s *SQLiteStore) ListSessionGrants(ctx context.Context, filter approval.GrantListFilter) ([]approval.SessionGrant, error) {
	filter.RunID = strings.TrimSpace(filter.RunID)
	filter.SessionID = strings.TrimSpace(filter.SessionID)
	filter.ToolName = strings.TrimSpace(filter.ToolName)
	for label, value := range map[string]string{"run id": filter.RunID, "session id": filter.SessionID, "tool name": filter.ToolName} {
		if err := validateApprovalFilterIdentity(label, value, true); err != nil {
			return nil, err
		}
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, fmt.Errorf("invalid approval grant status %q", filter.Status)
	}
	if filter.Limit < 0 || filter.Limit > maxGrantListLimit {
		return nil, fmt.Errorf("approval grant limit must be between 0 and %d", maxGrantListLimit)
	}
	if filter.Limit == 0 {
		filter.Limit = defaultGrantListLimit
	}
	query := grantSelect + ` WHERE 1=1`
	var args []any
	if filter.RunID != "" {
		query += ` AND run_id = ?`
		args = append(args, filter.RunID)
	}
	if filter.SessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, filter.SessionID)
	}
	if filter.ToolName != "" {
		query += ` AND tool_name = ?`
		args = append(args, filter.ToolName)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []approval.SessionGrant
	for rows.Next() {
		grant, err := getSessionGrantRow(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func findActiveSessionGrant(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query approval.GrantQuery) (approval.SessionGrant, bool, error) {
	query.RunID = strings.TrimSpace(query.RunID)
	query.SessionID = strings.TrimSpace(query.SessionID)
	query.WorkspaceID = strings.TrimSpace(query.WorkspaceID)
	query.ToolName = strings.TrimSpace(query.ToolName)
	query.ActionClass = strings.TrimSpace(query.ActionClass)
	query.ScopeFingerprint = strings.ToLower(strings.TrimSpace(query.ScopeFingerprint))
	query.ModeSnapshotID = strings.TrimSpace(query.ModeSnapshotID)
	query.InteractionSnapshotID = strings.TrimSpace(query.InteractionSnapshotID)
	query.ExecutionProfileSnapshotID = strings.TrimSpace(query.ExecutionProfileSnapshotID)
	query.PermissionSnapshotID = strings.TrimSpace(query.PermissionSnapshotID)
	query.PermissionMode = strings.TrimSpace(query.PermissionMode)
	query.WorkspaceRootFingerprint = strings.ToLower(strings.TrimSpace(query.WorkspaceRootFingerprint))
	query.CapabilityGeneration = strings.ToLower(strings.TrimSpace(query.CapabilityGeneration))
	for label, value := range map[string]string{
		"run id": query.RunID, "session id": query.SessionID, "workspace id": query.WorkspaceID,
		"tool name": query.ToolName, "action class": query.ActionClass,
	} {
		if err := validateApprovalFilterIdentity(label, value, label == "run id" || label == "workspace id"); err != nil {
			return approval.SessionGrant{}, false, err
		}
	}
	if query.SessionID == "" || query.ToolName == "" || query.ActionClass == "" {
		return approval.SessionGrant{}, false, errors.New("session grant lookup requires session, tool, and action class")
	}
	sqlText := grantSelect + ` JOIN runs ON runs.id = approval_session_grants.run_id
		JOIN sessions ON sessions.id = approval_session_grants.session_id
		WHERE approval_session_grants.session_id = ? AND approval_session_grants.workspace_id = ?
		AND approval_session_grants.tool_name = ? AND approval_session_grants.action_class = ?
		AND approval_session_grants.status = ? AND sessions.status = ? AND runs.status NOT IN (?, ?, ?)`
	args := []any{query.SessionID, query.WorkspaceID, query.ToolName, query.ActionClass, approval.GrantActive,
		session.StatusActive, domain.RunCompleted, domain.RunFailed, domain.RunCancelled}
	if query.RunID != "" {
		sqlText += ` AND approval_session_grants.run_id = ?`
		args = append(args, query.RunID)
	}
	if query.ScopeFingerprint != "" {
		sqlText += ` AND approval_session_grants.scope_fingerprint = ?
			AND approval_session_grants.mode_snapshot_id = ?
			AND approval_session_grants.mode_revision = ?
			AND approval_session_grants.interaction_snapshot_id = ?
			AND approval_session_grants.interaction_revision = ?
			AND approval_session_grants.execution_profile_snapshot_id = ?
			AND approval_session_grants.execution_profile_revision = ?
			AND approval_session_grants.permission_snapshot_id = ?
			AND approval_session_grants.permission_revision = ?
			AND approval_session_grants.permission_mode = ?
			AND approval_session_grants.workspace_root_fingerprint = ?
			AND approval_session_grants.capability_generation = ?
			AND approval_session_grants.uses_remaining > 0
			AND julianday(approval_session_grants.expires_at) > julianday('now')`
		args = append(args, query.ScopeFingerprint, query.ModeSnapshotID,
			query.ModeRevision, query.InteractionSnapshotID, query.InteractionRevision,
			query.ExecutionProfileSnapshotID, query.ExecutionProfileRevision,
			query.PermissionSnapshotID, query.PermissionRevision, query.PermissionMode,
			query.WorkspaceRootFingerprint, query.CapabilityGeneration)
	} else {
		sqlText += ` AND approval_session_grants.scope_fingerprint = ''`
	}
	grant, err := getSessionGrantRow(queryer.QueryRowContext(ctx, sqlText, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return approval.SessionGrant{}, false, nil
	}
	return grant, err == nil, err
}

func requireGrantableRunTx(ctx context.Context, tx *sql.Tx, runID string) error {
	var status domain.RunStatus
	var sessionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT runs.status, sessions.status FROM runs
		JOIN sessions ON sessions.id = runs.session_id WHERE runs.id = ?`, strings.TrimSpace(runID)).
		Scan(&status, &sessionStatus); err != nil {
		return err
	}
	if status == domain.RunCompleted || status == domain.RunFailed || status == domain.RunCancelled {
		return fmt.Errorf("run %s is terminal and cannot use session approval grants", runID)
	}
	if sessionStatus != session.StatusActive {
		return fmt.Errorf("run %s session is not active and cannot use session approval grants", runID)
	}
	return nil
}

func grantToolClassMatches(toolName string, actionClass string) bool {
	switch toolName {
	case "shell":
		return actionClass == "shell"
	case "replace_file":
		return actionClass == "workspace_write"
	case "command_runtime":
		return actionClass == "command_process"
	default:
		return false
	}
}

func appendGrantEventTx(ctx context.Context, tx *sql.Tx, grant approval.SessionGrant, eventType string) error {
	return appendRunEventForSessionTx(ctx, tx, grant.SessionID, eventType, "approval_store", grant.ID, map[string]any{
		"grant_id": grant.ID, "run_id": grant.RunID, "session_id": grant.SessionID,
		"workspace_id": grant.WorkspaceID, "tool_name": grant.ToolName, "action_class": grant.ActionClass,
		"status": grant.Status, "reason": grant.Reason, "revocation_reason": grant.RevocationReason,
		"granted_by": grant.GrantedBy, "revoked_by": grant.RevokedBy, "version": grant.Version,
		"scope_fingerprint": grant.ScopeFingerprint, "generation": grant.Generation,
		"max_uses": grant.MaxUses, "uses_remaining": grant.UsesRemaining,
	})
}

const grantSelect = `SELECT approval_session_grants.id, approval_session_grants.run_id,
	approval_session_grants.session_id, approval_session_grants.workspace_id, approval_session_grants.tool_name,
	approval_session_grants.action_class, approval_session_grants.status, approval_session_grants.request_fingerprint,
	approval_session_grants.reason, approval_session_grants.revocation_reason, approval_session_grants.granted_by,
	approval_session_grants.revoked_by, approval_session_grants.version, approval_session_grants.created_at,
	approval_session_grants.updated_at, approval_session_grants.revoked_at,
	approval_session_grants.scope_fingerprint, approval_session_grants.grant_generation,
	approval_session_grants.max_uses, approval_session_grants.uses_remaining,
	approval_session_grants.expires_at, approval_session_grants.mode_snapshot_id,
	approval_session_grants.mode_revision, approval_session_grants.interaction_snapshot_id,
	approval_session_grants.interaction_revision,
	approval_session_grants.execution_profile_snapshot_id,
	approval_session_grants.execution_profile_revision,
	approval_session_grants.permission_snapshot_id,
	approval_session_grants.permission_revision, approval_session_grants.permission_mode,
	approval_session_grants.workspace_root_fingerprint,
	approval_session_grants.capability_generation FROM approval_session_grants`

func getSessionGrantTx(ctx context.Context, tx *sql.Tx, id string) (approval.SessionGrant, error) {
	return getSessionGrantRow(tx.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, strings.TrimSpace(id)))
}

func getSessionGrantRow(row scanner) (approval.SessionGrant, error) {
	var grant approval.SessionGrant
	var createdAt, updatedAt string
	var revokedAt, expiresAt sql.NullString
	if err := row.Scan(&grant.ID, &grant.RunID, &grant.SessionID, &grant.WorkspaceID, &grant.ToolName,
		&grant.ActionClass, &grant.Status, &grant.RequestFingerprint, &grant.Reason, &grant.RevocationReason,
		&grant.GrantedBy, &grant.RevokedBy, &grant.Version, &createdAt, &updatedAt, &revokedAt,
		&grant.ScopeFingerprint, &grant.Generation, &grant.MaxUses, &grant.UsesRemaining,
		&expiresAt, &grant.ModeSnapshotID, &grant.ModeRevision,
		&grant.InteractionSnapshotID, &grant.InteractionRevision,
		&grant.ExecutionProfileSnapshotID, &grant.ExecutionProfileRevision,
		&grant.PermissionSnapshotID, &grant.PermissionRevision, &grant.PermissionMode,
		&grant.WorkspaceRootFingerprint, &grant.CapabilityGeneration); err != nil {
		return approval.SessionGrant{}, err
	}
	grant.CreatedAt = parseTS(createdAt)
	grant.UpdatedAt = parseTS(updatedAt)
	if revokedAt.Valid {
		parsed := parseTS(revokedAt.String)
		grant.RevokedAt = &parsed
	}
	if expiresAt.Valid {
		parsed := parseTS(expiresAt.String)
		grant.ExpiresAt = &parsed
	}
	if err := grant.Validate(); err != nil {
		return approval.SessionGrant{}, err
	}
	return grant, nil
}

func endBoundedGrantTx(ctx context.Context, tx *sql.Tx, grant *approval.SessionGrant,
	reasonCode string, reason string, actor string, at time.Time, eventType string,
) error {
	if grant == nil || !grant.Bounded() || grant.Status != approval.GrantActive {
		return errors.New("active bounded approval grant is required")
	}
	previousVersion := grant.Version
	grant.Status = approval.GrantRevoked
	grant.RevocationReason = reason
	grant.RevokedBy = actor
	grant.Version++
	grant.UpdatedAt = at.UTC()
	grant.RevokedAt = &grant.UpdatedAt
	result, err := tx.ExecContext(ctx, `UPDATE approval_session_grants SET status = ?,
		revocation_reason = ?, revoked_by = ?, version = ?, updated_at = ?, revoked_at = ?
		WHERE id = ? AND version = ? AND status = ?`, grant.Status,
		grant.RevocationReason, grant.RevokedBy, grant.Version, ts(grant.UpdatedAt),
		ts(*grant.RevokedAt), grant.ID, previousVersion, approval.GrantActive)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return errors.New("bounded approval grant changed concurrently")
	}
	_ = reasonCode
	return appendGrantEventTx(ctx, tx, *grant, eventType)
}

func (s *SQLiteStore) GetGrantConsumptionByProposal(ctx context.Context,
	proposalID string,
) (approval.GrantConsumption, bool, error) {
	return getGrantConsumptionByProposalTx(ctx, s.db, strings.TrimSpace(proposalID))
}

func getGrantConsumptionByProposalTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, proposalID string) (approval.GrantConsumption, bool, error) {
	var value approval.GrantConsumption
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT id, grant_id, proposal_id,
		approval_id, run_id, scope_fingerprint, grant_generation, use_ordinal,
		consumption_fingerprint, created_at FROM approval_grant_consumptions
		WHERE proposal_id = ?`, proposalID).Scan(&value.ID, &value.GrantID,
		&value.ProposalID, &value.ApprovalID, &value.RunID,
		&value.ScopeFingerprint, &value.GrantGeneration, &value.UseOrdinal,
		&value.Fingerprint, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.GrantConsumption{}, false, nil
	}
	if err != nil {
		return approval.GrantConsumption{}, false, err
	}
	value.CreatedAt = parseTS(createdAt)
	if err := value.Validate(); err != nil {
		return approval.GrantConsumption{}, false, err
	}
	return value, true, nil
}

type grantOperation struct {
	GrantID            string
	Action             string
	RequestFingerprint string
	ResultStatus       approval.GrantStatus
}

func getGrantOperationTx(ctx context.Context, tx *sql.Tx, operationKey string) (grantOperation, bool, error) {
	var operation grantOperation
	err := tx.QueryRowContext(ctx, `SELECT grant_id, action, request_fingerprint, result_status
		FROM approval_grant_operations WHERE operation_key = ?`, operationKey).
		Scan(&operation.GrantID, &operation.Action, &operation.RequestFingerprint, &operation.ResultStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return grantOperation{}, false, nil
	}
	return operation, err == nil, err
}

func insertGrantOperationTx(ctx context.Context, tx *sql.Tx, operationKey string, grantID string, action string,
	fingerprint string, result approval.GrantStatus) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO approval_grant_operations
		(operation_key, grant_id, action, request_fingerprint, result_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, operationKey, grantID, action, fingerprint, result, ts(time.Now().UTC()))
	return err
}
