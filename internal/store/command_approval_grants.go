package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/toolgateway"
)

// These are host-only ledger seams. The caller supplies the stored proposal
// identity; all scope and snapshot bindings come from its durable source.
func (s *SQLiteStore) GetCommandApprovalGrantScope(ctx context.Context, proposalID string) (approval.GrantQuery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return approval.GrantQuery{}, err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, "", proposalID)
	if err != nil {
		return approval.GrantQuery{}, err
	}
	query, err := commandApprovalGrantScopeTx(ctx, tx, record)
	if err != nil {
		return query, err
	}
	return query, tx.Commit()
}

func commandApprovalGrantScopeTx(ctx context.Context, tx *sql.Tx, record approval.Record) (approval.GrantQuery, error) {
	var query approval.GrantQuery
	call, _, err := getSupervisorApprovalCallTx(ctx, tx, record.RunID, record.ProposalID)
	if err != nil {
		return query, err
	}
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil {
		return query, err
	}
	input, canonical, err := toolgateway.CommandToolInput(toolgateway.ToolName(call.ToolName), json.RawMessage(call.PayloadJSON), a)
	if err != nil {
		return query, err
	}
	if record.Validate() != nil || record.ToolName != string(toolgateway.CommandRuntimeTool) ||
		record.ActionClass != "command_process" || record.Mode != "per_call" || !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(call.ToolName)) ||
		call.PayloadJSON != string(canonical) || record.RequestFingerprint != commandruntimeadapter.OperationApprovalFingerprint(call) ||
		a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion || a.ScopeFingerprint == "" ||
		input.ReviewScope == nil || len(input.Commands) != 1 ||
		(input.Action != toolgateway.CommandRuntimeActionRun && input.Action != toolgateway.CommandRuntimeActionStart) {
		return query, errors.New("bounded review requires the exact stored command and declared risk scope")
	}
	risk, err := input.ReviewScope.RiskScope()
	if err != nil {
		return query, err
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, record.RunID)
	if err != nil {
		return query, err
	}
	interaction, err := getCurrentRunExecutionInteractionSnapshot(ctx, tx, record.RunID)
	if err != nil {
		return query, err
	}
	profile, err := getCurrentRunExecutionProfileSnapshot(ctx, tx, record.RunID)
	if err != nil {
		return query, err
	}
	permission, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, record.RunID)
	if err != nil {
		return query, err
	}
	if !permission.Mode.IsApprovalMode() || permission.ID != a.PermissionSnapshotID || permission.Revision != a.PermissionRevision || permission.Mode != a.PermissionMode {
		return query, errors.New("bounded command permission changed")
	}
	adapter, err := json.Marshal(a.Adapter)
	if err != nil {
		return query, err
	}
	return approval.GrantQuery{RunID: record.RunID, SessionID: record.SessionID, WorkspaceID: record.WorkspaceID,
		ToolName: record.ToolName, ActionClass: record.ActionClass, ScopeFingerprint: risk.Fingerprint,
		ModeSnapshotID: mode.ID, ModeRevision: mode.Revision, InteractionSnapshotID: interaction.ID, InteractionRevision: interaction.Revision,
		ExecutionProfileSnapshotID: profile.ID, ExecutionProfileRevision: profile.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: string(permission.Mode),
		// The native host scope includes the actual owned workspace root, not
		// a model-declared cwd. Dispatch independently rechecks that same pin.
		WorkspaceRootFingerprint: a.ScopeFingerprint,
		CapabilityGeneration:     approval.Fingerprint("command_grant_binding.v1", string(adapter), a.PermissionRuntimeEpoch, fmt.Sprint(a.PermissionGeneration), fmt.Sprint(a.RunAuthorizationFence))}, nil
}

func (s *SQLiteStore) CheckCommandApprovalGrant(ctx context.Context, proposalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, "", proposalID)
	if err != nil {
		return err
	}
	query, err := commandApprovalGrantScopeTx(ctx, tx, record)
	if err != nil {
		return err
	}
	grant, err := getSessionGrantTx(ctx, tx, record.GrantID)
	if err != nil {
		return err
	}
	consumption, found, err := getGrantConsumptionByProposalTx(ctx, tx, proposalID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("bounded command approval has no exact consumption")
	}
	var revoked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM approval_grant_operations WHERE grant_id=? AND action='revoke')`, grant.ID).Scan(&revoked); err != nil {
		return err
	}
	if err := approval.CheckBoundedConsumption(grant, consumption, record, query, revoked, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) AuthorizeCommandApprovalWithSessionGrant(ctx context.Context, request approval.DecisionRequest, grantID string) (approval.DecisionResult, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if normalized.Action != approval.ActionApprove {
		return approval.DecisionResult{}, errors.New("bounded command use requires explicit operator approval")
	}
	return s.authorizeApprovalWithSessionGrant(ctx, normalized.ProposalID, grantID, &normalized)
}

func sealCommandGrantReviewTx(ctx context.Context, tx *sql.Tx, record approval.Record, grantID string, request approval.DecisionRequest) error {
	key := approval.OperationKeyDigest(request.IdempotencyKey)
	fingerprint := approval.Fingerprint("command_grant_review.v1", approval.DecisionFingerprint(request), grantID)
	operation, found, err := getApprovalOperationTx(ctx, tx, key)
	if err != nil {
		return err
	}
	if found {
		if operation.ApprovalID != record.ID || operation.Action != approval.ActionApprove || operation.RequestFingerprint != fingerprint || operation.ResultStatus != approval.StatusApproved {
			return errors.New("bounded command review key belongs to another exact decision")
		}
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO approval_operations (idempotency_key,approval_id,action,request_fingerprint,result_status,created_at) VALUES (?,?,?,?,?,?)`,
		key, record.ID, approval.ActionApprove, fingerprint, approval.StatusApproved, ts(time.Now().UTC()))
	return err
}

func pendingCommandGrantProposal(record approval.Record) approval.Proposal {
	return approval.Proposal{ProposalID: record.ProposalID, SessionID: record.SessionID, WorkspaceID: record.WorkspaceID,
		ToolName: record.ToolName, ActionClass: record.ActionClass, Mode: record.Mode, Status: approval.StatusPending,
		RequestFingerprint: record.RequestFingerprint, RequestedBy: record.RequestedBy}
}

func boundedApprovalGrantScopeTx(ctx context.Context, tx *sql.Tx, record approval.Record) (approval.GrantQuery, error) {
	if record.ToolName == string(toolgateway.CommandRuntimeTool) {
		if err := validateCommandApprovalSourceTx(ctx, tx, pendingCommandGrantProposal(record)); err != nil {
			return approval.GrantQuery{}, err
		}
		return commandApprovalGrantScopeTx(ctx, tx, record)
	}
	return approval.GrantQuery{}, errors.New("bounded command grants require Command Runtime")
}
