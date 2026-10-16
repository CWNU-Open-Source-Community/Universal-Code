package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

func computerUseAppScopeTx(ctx context.Context, tx *sql.Tx, record approval.Record, allowStarted bool) (approval.ComputerUseAppScope, error) {
	var scope approval.ComputerUseAppScope
	proposal := approval.Proposal{ProposalID: record.ProposalID, SessionID: record.SessionID,
		WorkspaceID: record.WorkspaceID, ToolName: record.ToolName, ActionClass: record.ActionClass,
		Mode: record.Mode, Status: approval.StatusPending, RequestFingerprint: record.RequestFingerprint,
		RequestedBy: record.RequestedBy}
	if record.Validate() != nil {
		return scope, errors.New("invalid Computer Use approval")
	}
	if err := validateMCPApprovalSourceStateTx(ctx, tx, proposal, allowStarted); err != nil {
		return scope, err
	}
	call, _, err := getSupervisorApprovalCallTx(ctx, tx, record.RunID, record.ProposalID)
	if err != nil {
		return scope, err
	}
	authority, err := mcp.DecodeSupervisorCallAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil {
		return scope, err
	}
	server, err := getMCPClientServer(ctx, tx, authority.ServerID)
	if err != nil {
		return scope, err
	}
	if !mcp.IsComputerUseServer(server.Descriptor) || authority.PermissionMode != domain.RunExecutionPermissionAsk ||
		authority.PermissionRuntimeEpoch == "" || authority.RunAuthorizationFence == 0 {
		return scope, errors.New("application approval requires the live built-in Computer Use source in Ask mode")
	}
	payload, _, err := toolgateway.NormalizeMCPToolPayload(json.RawMessage(call.PayloadJSON))
	if err != nil {
		return scope, err
	}
	target, err := approval.ParseComputerUseTarget(payload.ToolName, payload.Arguments)
	if err != nil || target.AppID == "" || (payload.Method != "" && payload.Method != "tools/call") {
		return scope, errors.New("application approval requires an exact Computer Use application target")
	}
	if target.SystemShortcut {
		return scope, errors.New("system shortcuts require an exact single-call approval")
	}
	return approval.ComputerUseAppScope{RunID: record.RunID, SessionID: record.SessionID, WorkspaceID: record.WorkspaceID,
		AppID: target.AppID, PermissionSnapshotID: authority.PermissionSnapshotID, PermissionRevision: authority.PermissionRevision,
		PermissionMode: string(authority.PermissionMode), PermissionGeneration: authority.PermissionGeneration,
		PermissionRuntimeEpoch: authority.PermissionRuntimeEpoch, RunAuthorizationFence: authority.RunAuthorizationFence,
		ServerID: authority.ServerID, DescriptorFingerprint: authority.DescriptorFingerprint, ServerGeneration: authority.ServerGeneration,
		CapabilityFingerprint: payload.CapabilityFingerprint}, nil
}

func findComputerUseAppGrantTx(ctx context.Context, tx *sql.Tx, scope approval.ComputerUseAppScope) (approval.SessionGrant, bool, error) {
	grant, err := getSessionGrantRow(tx.QueryRowContext(ctx, grantSelect+`
		JOIN approval_computer_use_app_grant_scopes app ON app.grant_id=approval_session_grants.id
		JOIN tool_approvals origin ON origin.id=app.source_approval_id
		WHERE app.scope_fingerprint=? AND app.app_id=? AND approval_session_grants.run_id=?
		AND approval_session_grants.session_id=? AND approval_session_grants.workspace_id=?
		AND approval_session_grants.tool_name=? AND approval_session_grants.action_class=?
		AND approval_session_grants.status='active' AND origin.status='approved'
		AND origin.grant_id=approval_session_grants.id`, scope.Fingerprint(), scope.AppID,
		scope.RunID, scope.SessionID, scope.WorkspaceID, mcp.OperationApprovalTool, scope.GrantActionClass()))
	if errors.Is(err, sql.ErrNoRows) {
		return approval.SessionGrant{}, false, nil
	}
	return grant, err == nil, err
}

// DecideMCPComputerUseAppApproval is a narrow extension of the existing
// approval ledger. Its scope comes from the pending call, never the request.
func (s *SQLiteStore) DecideMCPComputerUseAppApproval(ctx context.Context, request approval.DecisionRequest) (approval.DecisionResult, approval.SessionGrant, bool, error) {
	var decision approval.DecisionResult
	var grant approval.SessionGrant
	normalized, err := request.Normalize()
	if err != nil || normalized.Action != approval.ActionApprove {
		return decision, grant, false, errors.New("application approval requires an explicit approval decision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return decision, grant, false, err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, "", normalized.ProposalID)
	if err != nil {
		return decision, grant, false, err
	}
	operationKey := approval.OperationKeyDigest(normalized.IdempotencyKey)
	operation, replay, err := getApprovalOperationTx(ctx, tx, operationKey)
	if err != nil {
		return decision, grant, false, err
	}
	if replay {
		if record.GrantID == "" || operation.ApprovalID != record.ID || operation.Action != approval.ActionApprove ||
			operation.ResultStatus != approval.StatusApproved || record.Status != approval.StatusApproved ||
			operation.RequestFingerprint != computerUseAppReviewFingerprint(normalized, record.GrantID) {
			return decision, grant, false, errors.New("application approval key belongs to a different decision")
		}
		grant, err = getSessionGrantTx(ctx, tx, record.GrantID)
		if err != nil {
			return decision, grant, false, err
		}
		if err := tx.Commit(); err != nil {
			return decision, grant, false, err
		}
		return approval.DecisionResult{Approval: record, Replayed: true}, grant, false, nil
	}
	if record.Status != approval.StatusPending || record.GrantID != "" {
		return decision, grant, false, errors.New("application approval is no longer pending")
	}
	scope, err := computerUseAppScopeTx(ctx, tx, record, false)
	if err != nil {
		return decision, grant, false, err
	}
	if err := requireGrantableRunTx(ctx, tx, record.RunID); err != nil {
		return decision, grant, false, err
	}
	grant, found, err := findComputerUseAppGrantTx(ctx, tx, scope)
	if err != nil {
		return decision, grant, false, err
	}
	if !found {
		now := time.Now().UTC()
		grant = approval.SessionGrant{ID: idgen.New("grant"), RunID: record.RunID, SessionID: record.SessionID,
			WorkspaceID: record.WorkspaceID, ToolName: mcp.OperationApprovalTool, ActionClass: scope.GrantActionClass(),
			Status: approval.GrantActive, RequestFingerprint: approval.Fingerprint("computer_use_app_grant.v1", scope.Fingerprint(), record.ID, normalized.ReviewedBy),
			Reason: "Computer Use application access for this Run", GrantedBy: normalized.ReviewedBy, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := grant.Validate(); err != nil {
			return decision, grant, false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO approval_session_grants
			(id,run_id,session_id,workspace_id,tool_name,action_class,status,request_fingerprint,reason,granted_by,version,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, grant.ID, grant.RunID, grant.SessionID, grant.WorkspaceID, grant.ToolName,
			grant.ActionClass, grant.Status, grant.RequestFingerprint, grant.Reason, grant.GrantedBy, grant.Version, ts(now), ts(now))
		if err != nil {
			return decision, grant, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO approval_computer_use_app_grant_scopes(grant_id,source_approval_id,app_id,scope_fingerprint) VALUES(?,?,?,?)`,
			grant.ID, record.ID, scope.AppID, scope.Fingerprint()); err != nil {
			return decision, grant, false, err
		}
		if err := appendGrantEventTx(ctx, tx, grant, events.ApprovalGrantCreatedEvent); err != nil {
			return decision, grant, false, err
		}
	}
	record, err = approveComputerUseAppRecordTx(ctx, tx, record, grant, normalized.ReviewedBy)
	if err != nil {
		return decision, grant, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_operations(idempotency_key,approval_id,action,request_fingerprint,result_status,created_at) VALUES(?,?,?,?,?,?)`,
		operationKey, record.ID, approval.ActionApprove, computerUseAppReviewFingerprint(normalized, grant.ID), approval.StatusApproved, ts(time.Now().UTC())); err != nil {
		return decision, grant, false, err
	}
	if err := tx.Commit(); err != nil {
		return decision, grant, false, err
	}
	return approval.DecisionResult{Approval: record}, grant, !found, nil
}

func computerUseAppReviewFingerprint(request approval.DecisionRequest, grantID string) string {
	return approval.Fingerprint("computer_use_app_review.v1", approval.DecisionFingerprint(request), grantID)
}

func approveComputerUseAppRecordTx(ctx context.Context, tx *sql.Tx, record approval.Record, grant approval.SessionGrant, reviewer string) (approval.Record, error) {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE tool_approvals SET status=?,grant_id=?,decision_reason=?,reviewed_by=?,version=version+1,updated_at=?,decided_at=?
		WHERE id=? AND version=? AND status='pending' AND grant_id IS NULL`, approval.StatusApproved, grant.ID,
		"authorized by this Run's Computer Use application grant", reviewer, ts(now), ts(now), record.ID, record.Version)
	if err != nil {
		return record, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return record, errors.New("Computer Use approval changed concurrently")
	}
	record.Status, record.GrantID, record.ReviewedBy = approval.StatusApproved, grant.ID, reviewer
	record.DecisionReason = "authorized by this Run's Computer Use application grant"
	record.Version++
	record.UpdatedAt, record.DecidedAt = now, &now
	if err := record.Validate(); err != nil {
		return record, err
	}
	return record, appendApprovalEventTx(ctx, tx, record, events.ApprovalDecidedEvent)
}

// TryAuthorizeMCPComputerUseAppApproval consumes only the already reviewed app
// scope. A new exact call still receives its own durable approval evidence.
func (s *SQLiteStore) TryAuthorizeMCPComputerUseAppApproval(ctx context.Context, proposalID string) (approval.Record, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return approval.Record{}, false, err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, "", proposalID)
	if err != nil {
		return record, false, err
	}
	if record.Status != approval.StatusPending {
		return record, false, errors.New("only pending Computer Use calls can use an application grant")
	}
	scope, err := computerUseAppScopeTx(ctx, tx, record, false)
	if err != nil {
		return record, false, err
	}
	grant, found, err := findComputerUseAppGrantTx(ctx, tx, scope)
	if err != nil || !found {
		return record, false, err
	}
	record, err = approveComputerUseAppRecordTx(ctx, tx, record, grant, "session_grant")
	if err != nil {
		return record, false, err
	}
	return record, true, tx.Commit()
}

func (s *SQLiteStore) CheckMCPComputerUseAppGrant(ctx context.Context, proposalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, "", proposalID)
	if err != nil {
		return err
	}
	scope, err := computerUseAppScopeTx(ctx, tx, record, true)
	if err != nil {
		return err
	}
	grant, found, err := findComputerUseAppGrantTx(ctx, tx, scope)
	if err != nil {
		return err
	}
	if !found || record.Status != approval.StatusApproved || record.GrantID != grant.ID {
		return errors.New("Computer Use application grant expired, was revoked, or changed scope")
	}
	return tx.Commit()
}
