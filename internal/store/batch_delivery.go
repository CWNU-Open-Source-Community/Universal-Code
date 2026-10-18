package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// CreateBatchDeliveryPlan atomically binds a batch-delivery.v1 plan to one
// approved, admitted core proposal. Filesystem materialization happens only
// after this prepared intent and every child lease are durable.
func (s *SQLiteStore) CreateBatchDeliveryPlan(ctx context.Context,
	plan domain.BatchDeliveryPlan, workspaces []domain.BatchDeliveryWorkspace,
) (domain.BatchDeliveryPlan, []domain.BatchDeliveryWorkspace, bool, error) {
	if err := validateBatchDeliveryPlanInput(plan, workspaces); err != nil {
		return domain.BatchDeliveryPlan{}, nil, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "batch delivery plan is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint, storedID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, id FROM batch_delivery_plans
		WHERE operation_digest = ?`, plan.OperationDigest).Scan(&storedFingerprint, &storedID)
	if err == nil {
		if storedFingerprint != plan.RequestFingerprint {
			return domain.BatchDeliveryPlan{}, nil, false, apperror.New(apperror.CodeConflict,
				"batch delivery operation key was reused for different intent")
		}
		stored, found, loadErr := getBatchDeliveryPlanTx(ctx, tx, storedID)
		if loadErr != nil || !found {
			return domain.BatchDeliveryPlan{}, nil, false, loadErr
		}
		children, loadErr := listBatchDeliveryWorkspacesTx(ctx, tx, stored.ID)
		if loadErr != nil {
			return domain.BatchDeliveryPlan{}, nil, false, loadErr
		}
		if err := tx.Commit(); err != nil {
			return domain.BatchDeliveryPlan{}, nil, false, err
		}
		return stored, children, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	var runStatus domain.RunStatus
	var missionID string
	if err := tx.QueryRowContext(ctx, `SELECT status, mission_id FROM runs WHERE id = ?`,
		plan.RunID).Scan(&runStatus, &missionID); err != nil {
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	if runStatus != domain.RunRunning {
		return domain.BatchDeliveryPlan{}, nil, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery requires a running Run")
	}
	_ = missionID
	proposal, found, err := getChildTaskProposalTxForBatch(ctx, tx, plan.ProposalID)
	if err != nil || !found {
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	if proposal.RunID != plan.RunID || proposal.RootAgentID != plan.RootAgentID ||
		proposal.WorkspaceID != plan.WorkspaceID || proposal.Status != domain.ChildTaskProposalApproved ||
		proposal.Surface != domain.ChildTaskSurfaceCore {
		return domain.BatchDeliveryPlan{}, nil, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery requires the matching approved core child proposal")
	}
	if err := validateBatchSpecAgainstProposalTx(ctx, tx, plan, proposal, workspaces); err != nil {
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	specJSON, _ := json.Marshal(plan.Spec)
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_plans
		(id, run_id, proposal_id, root_agent_id, workspace_id, status, spec_json,
		base_commit, source_branch, operation_digest, request_fingerprint, created_by,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.RunID, plan.ProposalID, plan.RootAgentID, plan.WorkspaceID,
		plan.Status, string(specJSON), plan.BaseCommit, plan.SourceBranch,
		plan.OperationDigest, plan.RequestFingerprint, plan.CreatedBy,
		ts(plan.CreatedAt), ts(plan.UpdatedAt)); err != nil {
		if isUniqueViolation(err) {
			return domain.BatchDeliveryPlan{}, nil, false, apperror.Wrap(apperror.CodeConflict,
				"batch delivery proposal, branch, or worktree is already bound", err)
		}
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	for _, workspace := range workspaces {
		profileJSON, _ := json.Marshal(workspace.ToolProfile)
		if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_workspaces
			(plan_id, ordinal, agent_id, generation, status, branch, worktree_root,
			base_commit, head_commit, owner_token_digest, tool_profile_json,
			tool_profile_fingerprint, lease_expires_at, last_heartbeat_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			workspace.PlanID, workspace.Ordinal, workspace.AgentID, workspace.Generation,
			workspace.Status, workspace.Branch, workspace.WorktreeRoot, workspace.BaseCommit,
			workspace.HeadCommit, workspace.OwnerTokenDigest, string(profileJSON),
			workspace.ToolProfileFingerprint, ts(workspace.LeaseExpiresAt),
			ts(workspace.LastHeartbeatAt), ts(workspace.CreatedAt), ts(workspace.UpdatedAt)); err != nil {
			return domain.BatchDeliveryPlan{}, nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.BatchDeliveryPlan{}, nil, false, err
	}
	return plan, workspaces, false, nil
}

func validateBatchDeliveryPlanInput(plan domain.BatchDeliveryPlan,
	workspaces []domain.BatchDeliveryWorkspace,
) error {
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.RunID) == "" ||
		strings.TrimSpace(plan.ProposalID) == "" || strings.TrimSpace(plan.RootAgentID) == "" ||
		strings.TrimSpace(plan.WorkspaceID) == "" || strings.TrimSpace(plan.CreatedBy) == "" ||
		plan.Status != domain.BatchDeliveryPreparing || plan.CreatedAt.IsZero() ||
		plan.UpdatedAt.Before(plan.CreatedAt) || !batchCommitValid(plan.BaseCommit) ||
		strings.TrimSpace(plan.SourceBranch) == "" || !batchDigestValid(plan.OperationDigest) ||
		!batchDigestValid(plan.RequestFingerprint) {
		return errors.New("batch delivery plan identities, state, or digests are invalid")
	}
	if err := plan.Spec.Validate(); err != nil {
		return err
	}
	if len(workspaces) != len(plan.Spec.Tasks) {
		return errors.New("batch delivery workspace count does not match its task count")
	}
	for index, workspace := range workspaces {
		if workspace.PlanID != plan.ID || workspace.Ordinal != index+1 ||
			strings.TrimSpace(workspace.AgentID) == "" || workspace.Generation != 1 ||
			workspace.Status != domain.BatchWorkspacePreparing ||
			strings.TrimSpace(workspace.Branch) == "" || strings.TrimSpace(workspace.WorktreeRoot) == "" ||
			workspace.BaseCommit != plan.BaseCommit || workspace.HeadCommit != "" ||
			!batchDigestValid(workspace.OwnerTokenDigest) ||
			workspace.ToolProfile.Validate() != nil ||
			workspace.ToolProfileFingerprint != workspace.ToolProfile.Fingerprint() ||
			workspace.LeaseExpiresAt.IsZero() || workspace.LastHeartbeatAt.IsZero() ||
			!workspace.LeaseExpiresAt.After(workspace.LastHeartbeatAt) ||
			workspace.CreatedAt.IsZero() || workspace.UpdatedAt.Before(workspace.CreatedAt) {
			return fmt.Errorf("batch delivery workspace %d is invalid", index+1)
		}
	}
	return nil
}

func validateBatchSpecAgainstProposalTx(ctx context.Context, tx *sql.Tx,
	plan domain.BatchDeliveryPlan, proposal domain.ChildTaskProposal,
	workspaces []domain.BatchDeliveryWorkspace,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT ordinal, status, admitted_agent_id
		FROM child_task_assignments WHERE proposal_id = ? ORDER BY ordinal`, proposal.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	assignments := make(map[int]struct {
		status string
		agent  string
	}, len(workspaces))
	for rows.Next() {
		var ordinal int
		var status, agent string
		if err := rows.Scan(&ordinal, &status, &agent); err != nil {
			return err
		}
		assignments[ordinal] = struct {
			status string
			agent  string
		}{status: status, agent: agent}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(proposal.Spec.Tasks) != len(plan.Spec.Tasks) || len(assignments) != len(workspaces) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery task set does not match the admitted proposal")
	}
	for index, batchTask := range plan.Spec.Tasks {
		proposalTask := proposal.Spec.Tasks[index]
		assignment, ok := assignments[batchTask.Ordinal]
		if !ok || assignment.status != domain.ChildTaskAssignmentAdmitted || assignment.agent == "" ||
			workspaces[index].AgentID != assignment.agent ||
			batchTask.Budget.TurnLimit != proposalTask.TurnLimit ||
			batchTask.Budget.TokenLimit != proposalTask.TokenLimit ||
			batchTask.Budget.TimeoutMillis != proposalTask.TimeoutMillis ||
			!slices.Equal(batchTask.DependencyOrdinals, proposalTask.DependencyOrdinals) ||
			!slices.Equal(batchTask.ExpectedArtifacts, proposalTask.ExpectedArtifacts) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"batch delivery task bindings drifted from the admitted proposal")
		}
	}
	return nil
}

func getChildTaskProposalTxForBatch(ctx context.Context, tx *sql.Tx,
	id string,
) (domain.ChildTaskProposal, bool, error) {
	var proposal domain.ChildTaskProposal
	var specJSON, surface, tier, status, created string
	err := tx.QueryRowContext(ctx, `SELECT id, run_id, root_agent_id, session_id,
		workspace_id, status, spec_json, surface, fanout_tier, requested_by, version,
		created_at FROM child_task_proposals WHERE id = ?`, id).
		Scan(&proposal.ID, &proposal.RunID, &proposal.RootAgentID, &proposal.SessionID,
			&proposal.WorkspaceID, &status, &specJSON, &surface, &tier, &proposal.RequestedBy,
			&proposal.Version, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ChildTaskProposal{}, false, nil
	}
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	proposal.Status, proposal.Surface = status, domain.ChildTaskSurface(surface)
	proposal.FanoutTier, proposal.CreatedAt = domain.ReadOnlyFanoutTier(tier), parseTS(created)
	if err := json.Unmarshal([]byte(specJSON), &proposal.Spec); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	for index := range proposal.Spec.Tasks {
		proposal.Spec.Tasks[index].Ordinal = index + 1
	}
	return proposal, true, nil
}

func (s *SQLiteStore) GetBatchDeliveryPlan(ctx context.Context,
	id string,
) (domain.BatchDeliveryPlan, bool, error) {
	return getBatchDeliveryPlanTx(ctx, s.db, strings.TrimSpace(id))
}

func (s *SQLiteStore) GetBatchDeliveryPlanByProposal(ctx context.Context,
	proposalID string,
) (domain.BatchDeliveryPlan, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM batch_delivery_plans WHERE proposal_id = ?`,
		strings.TrimSpace(proposalID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryPlan{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryPlan{}, false, err
	}
	return getBatchDeliveryPlanTx(ctx, s.db, id)
}

func (s *SQLiteStore) ListBatchDeliveryPlans(ctx context.Context, runID string,
	limit int,
) ([]domain.BatchDeliveryPlan, error) {
	if strings.TrimSpace(runID) == "" || limit <= 0 || limit > 64 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "batch delivery list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM batch_delivery_plans
		WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]domain.BatchDeliveryPlan, 0, len(ids))
	for _, id := range ids {
		plan, found, err := getBatchDeliveryPlanTx(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, plan)
		}
	}
	return out, nil
}

// ListRecoverableBatchDeliveryPlans returns a bounded oldest-first startup
// queue. Terminal plans are immutable history and never reconstructed as live
// authority.
func (s *SQLiteStore) ListRecoverableBatchDeliveryPlans(ctx context.Context,
	limit int,
) ([]domain.BatchDeliveryPlan, error) {
	if limit <= 0 || limit > 256 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery recovery limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id FROM batch_delivery_plans p
		WHERE p.status NOT IN ('completed', 'aborted') OR EXISTS (
			SELECT 1 FROM batch_delivery_workspaces w JOIN agent_nodes n ON n.id=w.agent_id
			WHERE w.plan_id=p.id AND n.status NOT IN ('completed','failed','cancelled'))
		ORDER BY p.updated_at, p.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]domain.BatchDeliveryPlan, 0, len(ids))
	for _, id := range ids {
		value, found, err := getBatchDeliveryPlanTx(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, value)
		}
	}
	return out, nil
}

func getBatchDeliveryPlanTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.BatchDeliveryPlan, bool, error) {
	var value domain.BatchDeliveryPlan
	var status, specJSON, created, updated string
	err := queryer.QueryRowContext(ctx, `SELECT id, run_id, proposal_id, root_agent_id,
		workspace_id, status, spec_json, base_commit, source_branch, operation_digest,
		request_fingerprint, created_by, created_at, updated_at
		FROM batch_delivery_plans WHERE id = ?`, id).
		Scan(&value.ID, &value.RunID, &value.ProposalID, &value.RootAgentID,
			&value.WorkspaceID, &status, &specJSON, &value.BaseCommit, &value.SourceBranch,
			&value.OperationDigest, &value.RequestFingerprint, &value.CreatedBy,
			&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryPlan{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryPlan{}, false, err
	}
	value.Status = domain.BatchDeliveryStatus(status)
	value.CreatedAt, value.UpdatedAt = parseTS(created), parseTS(updated)
	if err := json.Unmarshal([]byte(specJSON), &value.Spec); err != nil {
		return domain.BatchDeliveryPlan{}, false, err
	}
	return value, true, nil
}

func (s *SQLiteStore) ListBatchDeliveryWorkspaces(ctx context.Context,
	planID string,
) ([]domain.BatchDeliveryWorkspace, error) {
	return listBatchDeliveryWorkspacesTx(ctx, s.db, strings.TrimSpace(planID))
}

func listBatchDeliveryWorkspacesTx(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, planID string) ([]domain.BatchDeliveryWorkspace, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT plan_id, ordinal, agent_id, generation,
		status, branch, worktree_root, base_commit, head_commit, owner_token_digest,
		tool_profile_json, tool_profile_fingerprint, lease_expires_at, last_heartbeat_at,
		created_at, updated_at FROM batch_delivery_workspaces WHERE plan_id = ? ORDER BY ordinal`,
		planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.BatchDeliveryWorkspace, 0, domain.MaxBatchDeliveryTasks)
	for rows.Next() {
		value, err := scanBatchDeliveryWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetBatchDeliveryWorkspace(ctx context.Context, planID string,
	ordinal int,
) (domain.BatchDeliveryWorkspace, bool, error) {
	return getBatchDeliveryWorkspaceTx(ctx, s.db, strings.TrimSpace(planID), ordinal)
}

type batchRowScanner interface{ Scan(...any) error }

func getBatchDeliveryWorkspaceTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, planID string, ordinal int) (domain.BatchDeliveryWorkspace, bool, error) {
	row := queryer.QueryRowContext(ctx, `SELECT plan_id, ordinal, agent_id, generation,
		status, branch, worktree_root, base_commit, head_commit, owner_token_digest,
		tool_profile_json, tool_profile_fingerprint, lease_expires_at, last_heartbeat_at,
		created_at, updated_at FROM batch_delivery_workspaces
		WHERE plan_id = ? AND ordinal = ?`, planID, ordinal)
	value, err := scanBatchDeliveryWorkspace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryWorkspace{}, false, nil
	}
	return value, err == nil, err
}

func scanBatchDeliveryWorkspace(row batchRowScanner) (domain.BatchDeliveryWorkspace, error) {
	var value domain.BatchDeliveryWorkspace
	var status, profileJSON, lease, heartbeat, created, updated string
	err := row.Scan(&value.PlanID, &value.Ordinal, &value.AgentID, &value.Generation,
		&status, &value.Branch, &value.WorktreeRoot, &value.BaseCommit, &value.HeadCommit,
		&value.OwnerTokenDigest, &profileJSON, &value.ToolProfileFingerprint,
		&lease, &heartbeat, &created, &updated)
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, err
	}
	value.Status = domain.BatchDeliveryWorkspaceStatus(status)
	value.LeaseExpiresAt, value.LastHeartbeatAt = parseTS(lease), parseTS(heartbeat)
	value.CreatedAt, value.UpdatedAt = parseTS(created), parseTS(updated)
	if err := json.Unmarshal([]byte(profileJSON), &value.ToolProfile); err != nil {
		return domain.BatchDeliveryWorkspace{}, err
	}
	if err := value.ToolProfile.Validate(); err != nil ||
		value.ToolProfile.Fingerprint() != value.ToolProfileFingerprint {
		return domain.BatchDeliveryWorkspace{}, errors.New("persisted batch delivery tool profile is invalid")
	}
	return value, nil
}

// ActivateBatchDeliveryWorkspace commits the filesystem-create readback and
// the dispatch mailbox entry in one transaction.
func (s *SQLiteStore) ActivateBatchDeliveryWorkspace(ctx context.Context,
	message domain.BatchDeliveryMailboxMessage, headCommit string,
) (domain.BatchDeliveryWorkspace, domain.BatchDeliveryMailboxMessage, bool, error) {
	if message.Kind != domain.BatchMailboxDispatch || !batchCommitValid(headCommit) {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeInvalidArgument, "batch delivery dispatch is invalid")
	}
	return s.appendBatchDeliveryMailbox(ctx, message, "", time.Time{}, headCommit, true)
}

// AppendBatchDeliveryMailbox persists one child heartbeat/message under the
// exact owner token digest and generation. The digest is never returned by a
// public projection.
func (s *SQLiteStore) AppendBatchDeliveryMailbox(ctx context.Context,
	message domain.BatchDeliveryMailboxMessage, ownerTokenDigest string,
	leaseExpiresAt time.Time,
) (domain.BatchDeliveryWorkspace, domain.BatchDeliveryMailboxMessage, bool, error) {
	return s.appendBatchDeliveryMailbox(ctx, message, ownerTokenDigest, leaseExpiresAt, "", false)
}

// AbortBatchDeliveryWorkspace records the root-owned aborted mailbox message
// and terminal workspace state atomically. It is used only after the
// application has inspected and, when safe, cleaned the exact Git worktree.
func (s *SQLiteStore) AbortBatchDeliveryWorkspace(ctx context.Context,
	message domain.BatchDeliveryMailboxMessage, finalStatus domain.BatchDeliveryWorkspaceStatus,
	headCommit string,
) (domain.BatchDeliveryWorkspace, domain.BatchDeliveryMailboxMessage, bool, error) {
	if err := validateBatchMailboxInput(message); err != nil ||
		message.Kind != domain.BatchMailboxAborted ||
		(finalStatus != domain.BatchWorkspaceCancelled &&
			finalStatus != domain.BatchWorkspaceFailed &&
			finalStatus != domain.BatchWorkspaceOrphaned) ||
		(headCommit != "" && !batchCommitValid(headCommit)) {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeInvalidArgument,
				"batch delivery abort transition is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint, storedID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, id FROM batch_delivery_mailbox
		WHERE operation_digest = ?`, message.OperationDigest).Scan(&storedFingerprint, &storedID)
	if err == nil {
		if storedFingerprint != message.RequestFingerprint {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
				apperror.New(apperror.CodeConflict, "batch abort operation key was reused")
		}
		stored, found, loadErr := getBatchMailboxMessageTx(ctx, tx, storedID)
		if loadErr != nil || !found {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
				loadErr
		}
		workspace, found, loadErr := getBatchDeliveryWorkspaceTx(ctx, tx,
			stored.PlanID, stored.Ordinal)
		if loadErr != nil || !found {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
				loadErr
		}
		if err := tx.Commit(); err != nil {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
		}
		return workspace, stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	workspace, found, err := getBatchDeliveryWorkspaceTx(ctx, tx, message.PlanID,
		message.Ordinal)
	if err != nil || !found {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	if workspace.Generation != message.Generation || workspace.Status.Terminal() {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeConflict, "batch delivery abort generation is stale")
	}
	if err := insertBatchMailboxTx(ctx, tx, message); err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	newHead := workspace.HeadCommit
	if headCommit != "" {
		newHead = headCommit
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_delivery_workspaces
		SET status = ?, head_commit = ?, lease_expires_at = ?, last_heartbeat_at = ?,
		updated_at = ? WHERE plan_id = ? AND ordinal = ? AND generation = ? AND status = ?`,
		finalStatus, newHead, ts(message.CreatedAt), ts(message.CreatedAt), ts(message.CreatedAt),
		message.PlanID, message.Ordinal, message.Generation, workspace.Status)
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeConflict, "batch delivery abort changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	workspace.Status, workspace.HeadCommit = finalStatus, newHead
	workspace.LeaseExpiresAt, workspace.LastHeartbeatAt = message.CreatedAt, message.CreatedAt
	workspace.UpdatedAt = message.CreatedAt
	return workspace, message, false, nil
}

func (s *SQLiteStore) appendBatchDeliveryMailbox(ctx context.Context,
	message domain.BatchDeliveryMailboxMessage, ownerTokenDigest string,
	leaseExpiresAt time.Time, headCommit string, dispatch bool,
) (domain.BatchDeliveryWorkspace, domain.BatchDeliveryMailboxMessage, bool, error) {
	if err := validateBatchMailboxInput(message); err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "batch delivery mailbox message is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint, storedID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, id FROM batch_delivery_mailbox
		WHERE operation_digest = ?`, message.OperationDigest).Scan(&storedFingerprint, &storedID)
	if err == nil {
		if storedFingerprint != message.RequestFingerprint {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
				apperror.New(apperror.CodeConflict, "batch mailbox operation key was reused")
		}
		stored, found, loadErr := getBatchMailboxMessageTx(ctx, tx, storedID)
		if loadErr != nil || !found {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, loadErr
		}
		workspace, found, loadErr := getBatchDeliveryWorkspaceTx(ctx, tx, stored.PlanID, stored.Ordinal)
		if loadErr != nil || !found {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, loadErr
		}
		if err := tx.Commit(); err != nil {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
		}
		return workspace, stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	workspace, found, err := getBatchDeliveryWorkspaceTx(ctx, tx, message.PlanID, message.Ordinal)
	if err != nil || !found {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	if workspace.Generation != message.Generation {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeConflict, "stale batch delivery generation")
	}
	if !dispatch {
		if !batchDigestValid(ownerTokenDigest) || ownerTokenDigest != workspace.OwnerTokenDigest {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
				apperror.New(apperror.CodePolicyDenied, "batch delivery owner token does not match")
		}
		if !leaseExpiresAt.After(message.CreatedAt) || message.CreatedAt.After(workspace.LeaseExpiresAt) {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
				apperror.New(apperror.CodeDeadlineExceeded, "batch delivery lease expired")
		}
	}
	newStatus, err := batchMailboxTransition(workspace.Status, message.Kind)
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	if dispatch && (workspace.Status != domain.BatchWorkspacePreparing || headCommit != workspace.BaseCommit) {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeConflict, "batch delivery dispatch binding drifted")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1
		FROM batch_delivery_mailbox WHERE plan_id = ? AND ordinal = ? AND generation = ?`,
		message.PlanID, message.Ordinal, message.Generation).Scan(&message.Sequence); err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	evidenceJSON, _ := json.Marshal(message.EvidenceRefs)
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_mailbox
		(id, plan_id, ordinal, generation, sequence, kind, actor, summary,
		evidence_refs_json, operation_digest, request_fingerprint, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, message.ID, message.PlanID,
		message.Ordinal, message.Generation, message.Sequence, message.Kind, message.Actor,
		message.Summary, string(evidenceJSON), message.OperationDigest,
		message.RequestFingerprint, ts(message.CreatedAt)); err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	newLease := workspace.LeaseExpiresAt
	if !dispatch {
		newLease = leaseExpiresAt
	}
	newHead := workspace.HeadCommit
	if dispatch {
		newHead = headCommit
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_delivery_workspaces
		SET status = ?, head_commit = ?, lease_expires_at = ?, last_heartbeat_at = ?, updated_at = ?
		WHERE plan_id = ? AND ordinal = ? AND generation = ? AND status = ?`, newStatus,
		newHead, ts(newLease), ts(message.CreatedAt), ts(message.CreatedAt), message.PlanID,
		message.Ordinal, message.Generation, workspace.Status)
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false,
			apperror.New(apperror.CodeConflict, "batch delivery workspace changed concurrently")
	}
	workspace.Status, workspace.HeadCommit = newStatus, newHead
	workspace.LeaseExpiresAt, workspace.LastHeartbeatAt = newLease, message.CreatedAt
	workspace.UpdatedAt = message.CreatedAt
	if dispatch {
		if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_plans SET status = 'active',
			updated_at = ? WHERE id = ? AND status = 'preparing'`, ts(message.CreatedAt),
			message.PlanID); err != nil {
			return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.BatchDeliveryWorkspace{}, domain.BatchDeliveryMailboxMessage{}, false, err
	}
	return workspace, message, false, nil
}

func validateBatchMailboxInput(message domain.BatchDeliveryMailboxMessage) error {
	message.Summary = strings.TrimSpace(message.Summary)
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.PlanID) == "" ||
		message.Ordinal < 1 || message.Ordinal > domain.MaxBatchDeliveryTasks ||
		message.Generation <= 0 || !domain.ValidBatchDeliveryMailboxKind(message.Kind) ||
		strings.TrimSpace(message.Actor) == "" || message.Summary == "" ||
		len([]rune(message.Summary)) > domain.MaxBatchMailboxSummaryRunes ||
		len(message.EvidenceRefs) > domain.MaxBatchMailboxEvidenceRefs ||
		!batchDigestValid(message.OperationDigest) || !batchDigestValid(message.RequestFingerprint) ||
		message.CreatedAt.IsZero() {
		return errors.New("batch mailbox identities, content, or digests are invalid")
	}
	for _, ref := range message.EvidenceRefs {
		if strings.TrimSpace(ref) == "" || len([]byte(ref)) > 2048 || strings.ContainsRune(ref, 0) {
			return errors.New("batch mailbox evidence reference is invalid")
		}
	}
	return nil
}

func batchMailboxTransition(current domain.BatchDeliveryWorkspaceStatus,
	kind domain.BatchDeliveryMailboxKind,
) (domain.BatchDeliveryWorkspaceStatus, error) {
	switch kind {
	case domain.BatchMailboxDispatch:
		if current == domain.BatchWorkspacePreparing {
			return domain.BatchWorkspaceDispatched, nil
		}
	case domain.BatchMailboxAck:
		if current == domain.BatchWorkspaceDispatched {
			return domain.BatchWorkspaceAcknowledged, nil
		}
	case domain.BatchMailboxProgress:
		if current == domain.BatchWorkspaceAcknowledged || current == domain.BatchWorkspaceWorking ||
			current == domain.BatchWorkspaceQuestion || current == domain.BatchWorkspaceChangesRequested {
			return domain.BatchWorkspaceWorking, nil
		}
	case domain.BatchMailboxQuestion:
		if current == domain.BatchWorkspaceAcknowledged || current == domain.BatchWorkspaceWorking {
			return domain.BatchWorkspaceQuestion, nil
		}
	case domain.BatchMailboxEvidence:
		if current == domain.BatchWorkspaceAcknowledged || current == domain.BatchWorkspaceWorking ||
			current == domain.BatchWorkspaceQuestion {
			return current, nil
		}
	case domain.BatchMailboxAborted:
		if !current.Terminal() {
			return domain.BatchWorkspaceCancelled, nil
		}
	}
	return "", apperror.New(apperror.CodeFailedPrecondition,
		fmt.Sprintf("batch mailbox %s cannot transition workspace from %s", kind, current))
}

func getBatchMailboxMessageTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.BatchDeliveryMailboxMessage, bool, error) {
	var value domain.BatchDeliveryMailboxMessage
	var kind, evidenceJSON, created string
	err := queryer.QueryRowContext(ctx, `SELECT id, plan_id, ordinal, generation,
		sequence, kind, actor, summary, evidence_refs_json, operation_digest,
		request_fingerprint, created_at FROM batch_delivery_mailbox WHERE id = ?`, id).
		Scan(&value.ID, &value.PlanID, &value.Ordinal, &value.Generation, &value.Sequence,
			&kind, &value.Actor, &value.Summary, &evidenceJSON, &value.OperationDigest,
			&value.RequestFingerprint, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryMailboxMessage{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryMailboxMessage{}, false, err
	}
	value.Kind, value.CreatedAt = domain.BatchDeliveryMailboxKind(kind), parseTS(created)
	if err := json.Unmarshal([]byte(evidenceJSON), &value.EvidenceRefs); err != nil {
		return domain.BatchDeliveryMailboxMessage{}, false, err
	}
	return value, true, nil
}

func (s *SQLiteStore) GetBatchDeliveryMailboxByOperationDigest(ctx context.Context,
	operationDigest string,
) (domain.BatchDeliveryMailboxMessage, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM batch_delivery_mailbox
		WHERE operation_digest = ?`, strings.TrimSpace(operationDigest)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryMailboxMessage{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryMailboxMessage{}, false, err
	}
	return getBatchMailboxMessageTx(ctx, s.db, id)
}

func (s *SQLiteStore) ListBatchDeliveryMailbox(ctx context.Context, planID string,
	ordinal int, limit int,
) ([]domain.BatchDeliveryMailboxMessage, error) {
	if strings.TrimSpace(planID) == "" || ordinal < 1 || ordinal > domain.MaxBatchDeliveryTasks ||
		limit <= 0 || limit > 512 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "batch mailbox list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM batch_delivery_mailbox
		WHERE plan_id = ? AND ordinal = ? ORDER BY generation, sequence LIMIT ?`,
		planID, ordinal, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]domain.BatchDeliveryMailboxMessage, 0, len(ids))
	for _, id := range ids {
		message, found, err := getBatchMailboxMessageTx(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, message)
		}
	}
	return out, nil
}

// RecordBatchDeliveryReceipt binds the independently computed Git inspection
// and test receipts to the current generation, then emits ready-for-review in
// the same transaction.
func (s *SQLiteStore) RecordBatchDeliveryReceipt(ctx context.Context,
	receipt domain.BatchDeliveryReceipt, ownerTokenDigest string,
	readyMessage domain.BatchDeliveryMailboxMessage,
) (domain.BatchDeliveryReceipt, bool, error) {
	if err := validateBatchReceiptInput(receipt, readyMessage); err != nil {
		return domain.BatchDeliveryReceipt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "batch delivery receipt is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint, storedID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, id FROM batch_delivery_receipts
		WHERE operation_digest = ?`, receipt.OperationDigest).Scan(&storedFingerprint, &storedID)
	if err == nil {
		if storedFingerprint != receipt.RequestFingerprint {
			return domain.BatchDeliveryReceipt{}, false, apperror.New(apperror.CodeConflict,
				"batch delivery receipt operation key was reused")
		}
		stored, found, loadErr := getBatchDeliveryReceiptTx(ctx, tx, storedID)
		if loadErr != nil || !found {
			return domain.BatchDeliveryReceipt{}, false, loadErr
		}
		if err := tx.Commit(); err != nil {
			return domain.BatchDeliveryReceipt{}, false, err
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	workspace, found, err := getBatchDeliveryWorkspaceTx(ctx, tx, receipt.PlanID, receipt.Ordinal)
	if err != nil || !found {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if workspace.Generation != receipt.Generation || workspace.OwnerTokenDigest != ownerTokenDigest ||
		workspace.BaseCommit != receipt.BaseCommit || workspace.Branch == "" ||
		receipt.HeadCommit == receipt.BaseCommit {
		return domain.BatchDeliveryReceipt{}, false, apperror.New(apperror.CodeConflict,
			"batch delivery receipt binding is stale or unauthorized")
	}
	if workspace.Status != domain.BatchWorkspaceAcknowledged &&
		workspace.Status != domain.BatchWorkspaceWorking &&
		workspace.Status != domain.BatchWorkspaceQuestion {
		return domain.BatchDeliveryReceipt{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"batch delivery receipt requires an acknowledged active generation")
	}
	if !receipt.CreatedAt.Before(workspace.LeaseExpiresAt) {
		return domain.BatchDeliveryReceipt{}, false, apperror.New(
			apperror.CodeDeadlineExceeded, "batch delivery receipt lease expired")
	}
	plan, found, err := getBatchDeliveryPlanTx(ctx, tx, receipt.PlanID)
	if err != nil || !found {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if plan.Status.Terminal() || plan.Status == domain.BatchDeliveryMerging {
		return domain.BatchDeliveryReceipt{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"batch delivery plan no longer accepts child receipts")
	}
	if err := requireBatchPlanRunRunningTx(ctx, tx, plan); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if receipt.DiffBytes > plan.Spec.Contract.MaxDiffBytes ||
		len(receipt.ChangedFiles) > plan.Spec.Contract.MaxChangedFiles {
		return domain.BatchDeliveryReceipt{}, false, apperror.New(apperror.CodeResourceExhausted,
			"batch delivery exceeds its declared contract")
	}
	filesJSON, _ := json.Marshal(receipt.ChangedFiles)
	testsJSON, _ := json.Marshal(receipt.TestReceipts)
	evidenceJSON, _ := json.Marshal(receipt.EvidenceRefs)
	limitationsJSON, _ := json.Marshal(receipt.Limitations)
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_receipts
		(id, plan_id, ordinal, generation, protocol_version, base_commit, head_commit,
		diff_sha256, call_chain_sha256, diff_bytes, diff_stat, changed_files_json,
		test_receipts_json, evidence_refs_json, limitations_json, operation_digest,
		request_fingerprint, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.ID, receipt.PlanID, receipt.Ordinal, receipt.Generation,
		receipt.ProtocolVersion, receipt.BaseCommit, receipt.HeadCommit, receipt.DiffSHA256,
		receipt.CallChainSHA256, receipt.DiffBytes, receipt.DiffStat, string(filesJSON),
		string(testsJSON), string(evidenceJSON), string(limitationsJSON),
		receipt.OperationDigest, receipt.RequestFingerprint, ts(receipt.CreatedAt)); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if err := insertBatchMailboxTx(ctx, tx, readyMessage); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_delivery_workspaces
		SET status = 'ready_for_review', head_commit = ?, last_heartbeat_at = ?, updated_at = ?
		WHERE plan_id = ? AND ordinal = ? AND generation = ? AND owner_token_digest = ?`,
		receipt.HeadCommit, ts(receipt.CreatedAt), ts(receipt.CreatedAt), receipt.PlanID,
		receipt.Ordinal, receipt.Generation, ownerTokenDigest)
	if err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.BatchDeliveryReceipt{}, false, apperror.New(apperror.CodeConflict,
			"batch delivery workspace changed before receipt commit")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_plans SET status = 'reviewing',
		updated_at = ? WHERE id = ? AND status IN ('active', 'blocked')`,
		ts(receipt.CreatedAt), receipt.PlanID); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	return receipt, false, nil
}

func validateBatchReceiptInput(receipt domain.BatchDeliveryReceipt,
	message domain.BatchDeliveryMailboxMessage,
) error {
	if strings.TrimSpace(receipt.ID) == "" || strings.TrimSpace(receipt.PlanID) == "" ||
		receipt.Ordinal < 1 || receipt.Ordinal > domain.MaxBatchDeliveryTasks ||
		receipt.Generation <= 0 || receipt.ProtocolVersion != domain.BatchDeliveryReceiptVersion ||
		!batchCommitValid(receipt.BaseCommit) || !batchCommitValid(receipt.HeadCommit) ||
		!batchDigestValid(receipt.DiffSHA256) || !batchDigestValid(receipt.CallChainSHA256) ||
		receipt.DiffBytes <= 0 || receipt.DiffBytes > domain.MaxBatchDiffBytes ||
		strings.TrimSpace(receipt.DiffStat) == "" || len(receipt.ChangedFiles) == 0 ||
		len(receipt.ChangedFiles) > domain.MaxBatchChangedFiles ||
		len(receipt.TestReceipts) == 0 || len(receipt.TestReceipts) > domain.MaxBatchDeliveryTestReceipts ||
		len(receipt.EvidenceRefs) > domain.MaxBatchMailboxEvidenceRefs ||
		len(receipt.Limitations) > domain.MaxBatchDeliveryLimitations ||
		!batchDigestValid(receipt.OperationDigest) || !batchDigestValid(receipt.RequestFingerprint) ||
		receipt.CreatedAt.IsZero() {
		return errors.New("batch delivery receipt identities or evidence are invalid")
	}
	for _, test := range receipt.TestReceipts {
		if strings.TrimSpace(test.RequirementID) == "" || test.ExitCode != 0 ||
			!batchDigestValid(test.OutputSHA256) || test.DurationMillis < 0 || test.CompletedAt.IsZero() {
			return errors.New("batch delivery test receipt is invalid or failed")
		}
	}
	for _, limitation := range receipt.Limitations {
		if strings.TrimSpace(limitation) == "" ||
			len([]rune(limitation)) > domain.MaxBatchDeliveryLimitationRunes {
			return errors.New("batch delivery limitation is invalid")
		}
	}
	if err := validateBatchMailboxInput(message); err != nil ||
		message.PlanID != receipt.PlanID || message.Ordinal != receipt.Ordinal ||
		message.Generation != receipt.Generation || message.Kind != domain.BatchMailboxReadyForReview ||
		message.CreatedAt != receipt.CreatedAt {
		return errors.New("batch delivery ready mailbox binding is invalid")
	}
	return nil
}

func insertBatchMailboxTx(ctx context.Context, tx *sql.Tx,
	message domain.BatchDeliveryMailboxMessage,
) error {
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1
		FROM batch_delivery_mailbox WHERE plan_id = ? AND ordinal = ? AND generation = ?`,
		message.PlanID, message.Ordinal, message.Generation).Scan(&message.Sequence); err != nil {
		return err
	}
	evidenceJSON, _ := json.Marshal(message.EvidenceRefs)
	_, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_mailbox
		(id, plan_id, ordinal, generation, sequence, kind, actor, summary,
		evidence_refs_json, operation_digest, request_fingerprint, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, message.ID, message.PlanID,
		message.Ordinal, message.Generation, message.Sequence, message.Kind, message.Actor,
		message.Summary, string(evidenceJSON), message.OperationDigest,
		message.RequestFingerprint, ts(message.CreatedAt))
	return err
}

func (s *SQLiteStore) GetBatchDeliveryReceipt(ctx context.Context, planID string,
	ordinal int, generation int64,
) (domain.BatchDeliveryReceipt, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM batch_delivery_receipts
		WHERE plan_id = ? AND ordinal = ? AND generation = ?`, planID, ordinal, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryReceipt{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	return getBatchDeliveryReceiptTx(ctx, s.db, id)
}

func getBatchDeliveryReceiptTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.BatchDeliveryReceipt, bool, error) {
	var value domain.BatchDeliveryReceipt
	var filesJSON, testsJSON, evidenceJSON, limitationsJSON, created string
	err := queryer.QueryRowContext(ctx, `SELECT id, plan_id, ordinal, generation,
		protocol_version, base_commit, head_commit, diff_sha256, call_chain_sha256,
		diff_bytes, diff_stat, changed_files_json, test_receipts_json, evidence_refs_json,
		limitations_json, operation_digest, request_fingerprint, created_at
		FROM batch_delivery_receipts WHERE id = ?`, id).
		Scan(&value.ID, &value.PlanID, &value.Ordinal, &value.Generation,
			&value.ProtocolVersion, &value.BaseCommit, &value.HeadCommit, &value.DiffSHA256,
			&value.CallChainSHA256, &value.DiffBytes, &value.DiffStat, &filesJSON,
			&testsJSON, &evidenceJSON, &limitationsJSON, &value.OperationDigest,
			&value.RequestFingerprint, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryReceipt{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	value.CreatedAt = parseTS(created)
	if err := json.Unmarshal([]byte(filesJSON), &value.ChangedFiles); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if err := json.Unmarshal([]byte(testsJSON), &value.TestReceipts); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &value.EvidenceRefs); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	if err := json.Unmarshal([]byte(limitationsJSON), &value.Limitations); err != nil {
		return domain.BatchDeliveryReceipt{}, false, err
	}
	return value, true, nil
}

// RecordBatchDeliveryReview stores the independent review and the matching
// accepted/changes-requested mailbox state atomically.
func (s *SQLiteStore) RecordBatchDeliveryReview(ctx context.Context,
	review domain.BatchDeliveryReview, message domain.BatchDeliveryMailboxMessage,
) (domain.BatchDeliveryReview, bool, error) {
	if err := validateBatchReviewInput(review, message); err != nil {
		return domain.BatchDeliveryReview{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "batch delivery review is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint, storedID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, id FROM batch_delivery_reviews
		WHERE operation_digest = ?`, review.OperationDigest).Scan(&storedFingerprint, &storedID)
	if err == nil {
		if storedFingerprint != review.RequestFingerprint {
			return domain.BatchDeliveryReview{}, false, apperror.New(apperror.CodeConflict,
				"batch delivery review operation key was reused")
		}
		stored, found, loadErr := getBatchDeliveryReviewTx(ctx, tx, storedID)
		if loadErr != nil || !found {
			return domain.BatchDeliveryReview{}, false, loadErr
		}
		if err := tx.Commit(); err != nil {
			return domain.BatchDeliveryReview{}, false, err
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryReview{}, false, err
	}
	receipt, found, err := getBatchDeliveryReceiptTx(ctx, tx, review.ReceiptID)
	if err != nil || !found {
		return domain.BatchDeliveryReview{}, false, err
	}
	workspace, found, err := getBatchDeliveryWorkspaceTx(ctx, tx, review.PlanID, review.Ordinal)
	if err != nil || !found {
		return domain.BatchDeliveryReview{}, false, err
	}
	if workspace.Status != domain.BatchWorkspaceReadyForReview ||
		workspace.Generation != review.Generation || workspace.AgentID == review.Reviewer ||
		receipt.PlanID != review.PlanID || receipt.Ordinal != review.Ordinal ||
		receipt.Generation != review.Generation || receipt.BaseCommit != review.BaseCommit ||
		receipt.HeadCommit != review.HeadCommit || receipt.DiffSHA256 != review.DiffSHA256 ||
		receipt.CallChainSHA256 != review.CallChainSHA256 {
		return domain.BatchDeliveryReview{}, false, apperror.New(apperror.CodeConflict,
			"batch delivery review does not match the current full-diff receipt")
	}
	plan, found, err := getBatchDeliveryPlanTx(ctx, tx, review.PlanID)
	if err != nil || !found {
		return domain.BatchDeliveryReview{}, false, err
	}
	if plan.Status.Terminal() || plan.Status == domain.BatchDeliveryMerging {
		return domain.BatchDeliveryReview{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"batch delivery plan no longer accepts reviews")
	}
	if err := requireBatchPlanRunRunningTx(ctx, tx, plan); err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_reviews
		(id, plan_id, ordinal, generation, protocol_version, receipt_id, reviewer, verdict,
		summary, base_commit, head_commit, diff_sha256, call_chain_sha256,
		full_diff_reviewed, call_chain_reviewed, tests_reviewed, operation_digest,
		request_fingerprint, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, 1, ?, ?, ?)`,
		review.ID, review.PlanID, review.Ordinal, review.Generation, review.ProtocolVersion,
		review.ReceiptID, review.Reviewer, review.Verdict, review.Summary, review.BaseCommit,
		review.HeadCommit, review.DiffSHA256, review.CallChainSHA256,
		review.OperationDigest, review.RequestFingerprint, ts(review.CreatedAt)); err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	if err := insertBatchMailboxTx(ctx, tx, message); err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	status := domain.BatchWorkspaceChangesRequested
	if review.Verdict == domain.BatchReviewAccepted {
		status = domain.BatchWorkspaceAccepted
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_delivery_workspaces SET status = ?,
		updated_at = ? WHERE plan_id = ? AND ordinal = ? AND generation = ?
		AND status = 'ready_for_review'`, status, ts(review.CreatedAt), review.PlanID,
		review.Ordinal, review.Generation)
	if err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.BatchDeliveryReview{}, false, apperror.New(apperror.CodeConflict,
			"batch delivery workspace changed during review")
	}
	if review.Verdict == domain.BatchReviewAccepted {
		if _, err := settleBatchDeliveryDependenciesTx(ctx, tx, review.PlanID, review.Ordinal,
			review.Generation, review.ReceiptID, review.ID); err != nil {
			return domain.BatchDeliveryReview{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	return review, false, nil
}

func validateBatchReviewInput(review domain.BatchDeliveryReview,
	message domain.BatchDeliveryMailboxMessage,
) error {
	if strings.TrimSpace(review.ID) == "" || strings.TrimSpace(review.PlanID) == "" ||
		review.Ordinal < 1 || review.Ordinal > domain.MaxBatchDeliveryTasks ||
		review.Generation <= 0 || review.ProtocolVersion != domain.BatchDeliveryReviewVersion ||
		strings.TrimSpace(review.ReceiptID) == "" || strings.TrimSpace(review.Reviewer) == "" ||
		(review.Verdict != domain.BatchReviewAccepted && review.Verdict != domain.BatchReviewChangesRequested) ||
		strings.TrimSpace(review.Summary) == "" || len([]rune(review.Summary)) > domain.MaxBatchMailboxSummaryRunes ||
		!batchCommitValid(review.BaseCommit) || !batchCommitValid(review.HeadCommit) ||
		!batchDigestValid(review.DiffSHA256) || !batchDigestValid(review.CallChainSHA256) ||
		!review.FullDiffReviewed || !review.CallChainReviewed || !review.TestsReviewed ||
		!batchDigestValid(review.OperationDigest) || !batchDigestValid(review.RequestFingerprint) ||
		review.CreatedAt.IsZero() {
		return errors.New("batch delivery review fields are invalid")
	}
	wantKind := domain.BatchMailboxChangesRequested
	if review.Verdict == domain.BatchReviewAccepted {
		wantKind = domain.BatchMailboxAccepted
	}
	if err := validateBatchMailboxInput(message); err != nil || message.PlanID != review.PlanID ||
		message.Ordinal != review.Ordinal || message.Generation != review.Generation ||
		message.Kind != wantKind || message.Actor != review.Reviewer || message.CreatedAt != review.CreatedAt {
		return errors.New("batch delivery review mailbox binding is invalid")
	}
	return nil
}

func getBatchDeliveryReviewTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.BatchDeliveryReview, bool, error) {
	var value domain.BatchDeliveryReview
	var verdict, created string
	err := queryer.QueryRowContext(ctx, `SELECT id, plan_id, ordinal, generation,
		protocol_version, receipt_id, reviewer, verdict, summary, base_commit, head_commit,
		diff_sha256, call_chain_sha256, full_diff_reviewed, call_chain_reviewed,
		tests_reviewed, operation_digest, request_fingerprint, created_at
		FROM batch_delivery_reviews WHERE id = ?`, id).
		Scan(&value.ID, &value.PlanID, &value.Ordinal, &value.Generation,
			&value.ProtocolVersion, &value.ReceiptID, &value.Reviewer, &verdict, &value.Summary,
			&value.BaseCommit, &value.HeadCommit, &value.DiffSHA256, &value.CallChainSHA256,
			&value.FullDiffReviewed, &value.CallChainReviewed, &value.TestsReviewed,
			&value.OperationDigest, &value.RequestFingerprint, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryReview{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	value.Verdict, value.CreatedAt = domain.BatchDeliveryReviewVerdict(verdict), parseTS(created)
	return value, true, nil
}

func (s *SQLiteStore) GetBatchDeliveryReview(ctx context.Context,
	receiptID string,
) (domain.BatchDeliveryReview, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM batch_delivery_reviews WHERE receipt_id = ?`,
		receiptID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryReview{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryReview{}, false, err
	}
	return getBatchDeliveryReviewTx(ctx, s.db, id)
}

// RetryBatchDeliveryWorkspace fences every stale child holder by advancing
// generation and rotating the owner token digest.
func (s *SQLiteStore) RetryBatchDeliveryWorkspace(ctx context.Context, planID string,
	ordinal int, expectedGeneration int64, newTokenDigest string,
	leaseExpiresAt, now time.Time,
) (domain.BatchDeliveryWorkspace, error) {
	if !batchDigestValid(newTokenDigest) || expectedGeneration <= 0 || !leaseExpiresAt.After(now) {
		return domain.BatchDeliveryWorkspace{}, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery retry fence is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE batch_delivery_workspaces
		SET generation = generation + 1, status = 'working', owner_token_digest = ?,
		head_commit = '', lease_expires_at = ?, last_heartbeat_at = ?, updated_at = ?
		WHERE plan_id = ? AND ordinal = ? AND generation = ? AND status = 'changes_requested'`,
		newTokenDigest, ts(leaseExpiresAt), ts(now), ts(now), planID, ordinal, expectedGeneration)
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.BatchDeliveryWorkspace{}, apperror.New(apperror.CodeConflict,
			"batch delivery retry generation changed concurrently")
	}
	value, found, err := s.GetBatchDeliveryWorkspace(ctx, planID, ordinal)
	if err != nil || !found {
		return domain.BatchDeliveryWorkspace{}, err
	}
	return value, nil
}

// RotateBatchDeliveryWorkspaceOwner replaces a lost owner token after a
// restart. Advancing generation fences the prior holder and returns the child
// to dispatched so a fresh ack is required before mutation continues.
func (s *SQLiteStore) RotateBatchDeliveryWorkspaceOwner(ctx context.Context,
	planID string, ordinal int, expectedGeneration int64, newTokenDigest string,
	leaseExpiresAt, now time.Time,
) (domain.BatchDeliveryWorkspace, error) {
	if !batchDigestValid(newTokenDigest) || expectedGeneration <= 0 || !leaseExpiresAt.After(now) {
		return domain.BatchDeliveryWorkspace{}, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery owner rotation fence is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE batch_delivery_workspaces
		SET generation = generation + 1, status = 'dispatched', owner_token_digest = ?,
		lease_expires_at = ?, last_heartbeat_at = ?, updated_at = ?
		WHERE plan_id = ? AND ordinal = ? AND generation = ?
		AND status IN ('dispatched', 'acknowledged', 'working', 'question')`,
		newTokenDigest, ts(leaseExpiresAt), ts(now), ts(now), planID, ordinal,
		expectedGeneration)
	if err != nil {
		return domain.BatchDeliveryWorkspace{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.BatchDeliveryWorkspace{}, apperror.New(apperror.CodeConflict,
			"batch delivery owner rotation changed concurrently")
	}
	value, found, err := s.GetBatchDeliveryWorkspace(ctx, planID, ordinal)
	if err != nil || !found {
		return domain.BatchDeliveryWorkspace{}, err
	}
	return value, nil
}

func (s *SQLiteStore) SetBatchDeliveryWorkspaceStatus(ctx context.Context, planID string,
	ordinal int, generation int64, from, to domain.BatchDeliveryWorkspaceStatus,
	head string, now time.Time,
) error {
	if !domain.ValidBatchDeliveryWorkspaceStatus(from) ||
		!domain.ValidBatchDeliveryWorkspaceStatus(to) || generation <= 0 ||
		(head != "" && !batchCommitValid(head)) || now.IsZero() {
		return apperror.New(apperror.CodeInvalidArgument, "batch workspace transition is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE batch_delivery_workspaces
		SET status = ?, head_commit = CASE WHEN ? = '' THEN head_commit ELSE ? END, updated_at = ?
		WHERE plan_id = ? AND ordinal = ? AND generation = ? AND status = ?`,
		to, head, head, ts(now), planID, ordinal, generation, from)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return apperror.New(apperror.CodeConflict, "batch workspace transition changed concurrently")
	}
	return nil
}

func (s *SQLiteStore) SetBatchDeliveryPlanStatus(ctx context.Context, planID string,
	from []domain.BatchDeliveryStatus, to domain.BatchDeliveryStatus, now time.Time,
) error {
	if len(from) == 0 || !domain.ValidBatchDeliveryStatus(to) || now.IsZero() {
		return apperror.New(apperror.CodeInvalidArgument, "batch plan transition is invalid")
	}
	placeholders := make([]string, len(from))
	args := []any{to, ts(now), planID}
	for index, status := range from {
		if !domain.ValidBatchDeliveryStatus(status) {
			return apperror.New(apperror.CodeInvalidArgument, "batch plan source status is invalid")
		}
		placeholders[index] = "?"
		args = append(args, status)
	}
	query := `UPDATE batch_delivery_plans SET status = ?, updated_at = ? WHERE id = ? AND status IN (` +
		strings.Join(placeholders, ",") + `)`
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return apperror.New(apperror.CodeConflict, "batch plan transition changed concurrently")
	}
	return nil
}

// CreateBatchDeliveryMergeQueue persists the local-only ordered merge intent.
func (s *SQLiteStore) CreateBatchDeliveryMergeQueue(ctx context.Context,
	queue domain.BatchDeliveryMergeQueue,
) (domain.BatchDeliveryMergeQueue, bool, error) {
	if err := validateBatchMergeQueueInput(queue); err != nil {
		return domain.BatchDeliveryMergeQueue{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "batch merge queue is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint, storedID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, id FROM batch_delivery_merge_queues
		WHERE operation_digest = ?`, queue.OperationDigest).Scan(&storedFingerprint, &storedID)
	if err == nil {
		if storedFingerprint != queue.RequestFingerprint {
			return domain.BatchDeliveryMergeQueue{}, false, apperror.New(apperror.CodeConflict,
				"batch merge queue operation key was reused")
		}
		stored, found, loadErr := getBatchMergeQueueTx(ctx, tx, storedID)
		if loadErr != nil || !found {
			return domain.BatchDeliveryMergeQueue{}, false, loadErr
		}
		if err := tx.Commit(); err != nil {
			return domain.BatchDeliveryMergeQueue{}, false, err
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	plan, found, err := getBatchDeliveryPlanTx(ctx, tx, queue.PlanID)
	if err != nil || !found {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	if plan.Status != domain.BatchDeliveryReviewing && plan.Status != domain.BatchDeliveryBlocked {
		return domain.BatchDeliveryMergeQueue{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch merge queue requires reviewed deliveries")
	}
	workspaces, err := listBatchDeliveryWorkspacesTx(ctx, tx, queue.PlanID)
	if err != nil || len(workspaces) != len(queue.OrderedOrdinals) {
		return domain.BatchDeliveryMergeQueue{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"batch merge queue task set is incomplete")
	}
	for _, ordinal := range queue.OrderedOrdinals {
		if ordinal < 1 || ordinal > len(workspaces) ||
			workspaces[ordinal-1].Status != domain.BatchWorkspaceAccepted {
			return domain.BatchDeliveryMergeQueue{}, false, apperror.New(apperror.CodeFailedPrecondition,
				"batch merge queue requires every ordered delivery to be accepted")
		}
	}
	ordinalsJSON, _ := json.Marshal(queue.OrderedOrdinals)
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_merge_queues
		(id, plan_id, protocol_version, status, base_commit, latest_base_commit,
		integration_branch, integration_root, integration_head, ordered_ordinals_json,
		next_index, failure_code, failure_summary, operation_digest, request_fingerprint,
		created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		queue.ID, queue.PlanID, queue.ProtocolVersion, queue.Status, queue.BaseCommit,
		queue.LatestBaseCommit, queue.IntegrationBranch, queue.IntegrationRoot,
		queue.IntegrationHead, string(ordinalsJSON), queue.NextIndex, queue.FailureCode,
		queue.FailureSummary, queue.OperationDigest, queue.RequestFingerprint,
		queue.CreatedBy, ts(queue.CreatedAt), ts(queue.UpdatedAt)); err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_plans SET status = 'merging',
		updated_at = ? WHERE id = ? AND status IN ('reviewing', 'blocked')`,
		ts(queue.CreatedAt), queue.PlanID); err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	return queue, false, nil
}

func validateBatchMergeQueueInput(queue domain.BatchDeliveryMergeQueue) error {
	if strings.TrimSpace(queue.ID) == "" || strings.TrimSpace(queue.PlanID) == "" ||
		queue.ProtocolVersion != domain.BatchDeliveryMergeQueueVersion ||
		queue.Status != domain.BatchMergeQueuePrepared || !batchCommitValid(queue.BaseCommit) ||
		!batchCommitValid(queue.LatestBaseCommit) || strings.TrimSpace(queue.IntegrationBranch) == "" ||
		strings.TrimSpace(queue.IntegrationRoot) == "" || queue.IntegrationHead != "" ||
		len(queue.OrderedOrdinals) == 0 || len(queue.OrderedOrdinals) > domain.MaxBatchDeliveryTasks ||
		queue.NextIndex != 0 || queue.FailureCode != "" || queue.FailureSummary != "" ||
		!batchDigestValid(queue.OperationDigest) || !batchDigestValid(queue.RequestFingerprint) ||
		strings.TrimSpace(queue.CreatedBy) == "" || queue.CreatedAt.IsZero() ||
		queue.UpdatedAt.Before(queue.CreatedAt) {
		return errors.New("batch merge queue identities or state are invalid")
	}
	seen := map[int]struct{}{}
	for _, ordinal := range queue.OrderedOrdinals {
		if ordinal < 1 || ordinal > domain.MaxBatchDeliveryTasks {
			return errors.New("batch merge queue ordinal is invalid")
		}
		if _, duplicate := seen[ordinal]; duplicate {
			return errors.New("batch merge queue ordinals must be unique")
		}
		seen[ordinal] = struct{}{}
	}
	return nil
}

func (s *SQLiteStore) GetBatchDeliveryMergeQueue(ctx context.Context,
	id string,
) (domain.BatchDeliveryMergeQueue, bool, error) {
	return getBatchMergeQueueTx(ctx, s.db, strings.TrimSpace(id))
}

func (s *SQLiteStore) GetBatchDeliveryMergeQueueByPlan(ctx context.Context,
	planID string,
) (domain.BatchDeliveryMergeQueue, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM batch_delivery_merge_queues
		WHERE plan_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		strings.TrimSpace(planID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryMergeQueue{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	return getBatchMergeQueueTx(ctx, s.db, id)
}

func getBatchMergeQueueTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.BatchDeliveryMergeQueue, bool, error) {
	var value domain.BatchDeliveryMergeQueue
	var status, ordinalsJSON, created, updated string
	err := queryer.QueryRowContext(ctx, `SELECT id, plan_id, protocol_version, status,
		base_commit, latest_base_commit, integration_branch, integration_root,
		integration_head, ordered_ordinals_json, next_index, failure_code, failure_summary,
		operation_digest, request_fingerprint, created_by, created_at, updated_at
		FROM batch_delivery_merge_queues WHERE id = ?`, id).
		Scan(&value.ID, &value.PlanID, &value.ProtocolVersion, &status,
			&value.BaseCommit, &value.LatestBaseCommit, &value.IntegrationBranch,
			&value.IntegrationRoot, &value.IntegrationHead, &ordinalsJSON, &value.NextIndex,
			&value.FailureCode, &value.FailureSummary, &value.OperationDigest,
			&value.RequestFingerprint, &value.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BatchDeliveryMergeQueue{}, false, nil
	}
	if err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	value.Status = domain.BatchDeliveryMergeQueueStatus(status)
	value.CreatedAt, value.UpdatedAt = parseTS(created), parseTS(updated)
	if err := json.Unmarshal([]byte(ordinalsJSON), &value.OrderedOrdinals); err != nil {
		return domain.BatchDeliveryMergeQueue{}, false, err
	}
	return value, true, nil
}

func (s *SQLiteStore) MarkBatchDeliveryMergeQueueRunning(ctx context.Context,
	queueID, integrationHead string, now time.Time,
) error {
	return s.updateBatchMergeQueue(ctx, queueID, domain.BatchMergeQueuePrepared,
		domain.BatchMergeQueueRunning, 0, integrationHead, "", "", now)
}

func (s *SQLiteStore) BlockBatchDeliveryMergeQueue(ctx context.Context, queueID string,
	from domain.BatchDeliveryMergeQueueStatus, integrationHead, failureCode,
	failureSummary string, now time.Time,
) error {
	if from != domain.BatchMergeQueuePrepared && from != domain.BatchMergeQueueRunning ||
		strings.TrimSpace(failureCode) == "" || strings.TrimSpace(failureSummary) == "" ||
		(integrationHead != "" && !batchCommitValid(integrationHead)) || now.IsZero() {
		return apperror.New(apperror.CodeInvalidArgument, "batch merge queue failure is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE batch_delivery_merge_queues
		SET status = 'blocked', integration_head = ?, failure_code = ?, failure_summary = ?,
		updated_at = ? WHERE id = ? AND status = ?`, integrationHead, failureCode,
		failureSummary, ts(now), queueID, from)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return apperror.New(apperror.CodeConflict, "batch merge queue changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_plans SET status = 'blocked',
		updated_at = ? WHERE id = (SELECT plan_id FROM batch_delivery_merge_queues WHERE id = ?)
		AND status = 'merging'`, ts(now), queueID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) AbortBatchDeliveryMergeQueue(ctx context.Context, queueID string,
	from domain.BatchDeliveryMergeQueueStatus, integrationHead, summary string,
	now time.Time,
) error {
	if from != domain.BatchMergeQueuePrepared && from != domain.BatchMergeQueueRunning &&
		from != domain.BatchMergeQueueBlocked || strings.TrimSpace(summary) == "" ||
		(integrationHead != "" && !batchCommitValid(integrationHead)) || now.IsZero() {
		return apperror.New(apperror.CodeInvalidArgument,
			"batch merge queue abort is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE batch_delivery_merge_queues
		SET status = 'aborted', integration_head = ?, failure_code = 'cancelled',
		failure_summary = ?, updated_at = ? WHERE id = ? AND status = ?`,
		integrationHead, strings.TrimSpace(summary), ts(now), strings.TrimSpace(queueID), from)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var current string
		if scanErr := s.db.QueryRowContext(ctx,
			`SELECT status FROM batch_delivery_merge_queues WHERE id = ?`, queueID).
			Scan(&current); scanErr == nil && current == string(domain.BatchMergeQueueAborted) {
			return nil
		}
		return apperror.New(apperror.CodeConflict,
			"batch merge queue abort changed concurrently")
	}
	return nil
}

func (s *SQLiteStore) ListBatchDeliveryMergeSteps(ctx context.Context,
	queueID string,
) ([]domain.BatchDeliveryMergeStep, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT queue_id, step_index, ordinal,
		input_head, pre_merge_head, post_merge_head, status, validation_json,
		failure_code, created_at, completed_at FROM batch_delivery_merge_steps
		WHERE queue_id = ? ORDER BY step_index`, strings.TrimSpace(queueID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BatchDeliveryMergeStep
	for rows.Next() {
		var value domain.BatchDeliveryMergeStep
		var status, created string
		var completed sql.NullString
		if err := rows.Scan(&value.QueueID, &value.StepIndex, &value.Ordinal,
			&value.InputHead, &value.PreMergeHead, &value.PostMergeHead, &status,
			&value.ValidationJSON, &value.FailureCode, &created, &completed); err != nil {
			return nil, err
		}
		value.Status = domain.BatchDeliveryMergeQueueStatus(status)
		value.CreatedAt = parseTS(created)
		if completed.Valid {
			parsed := parseTS(completed.String)
			value.CompletedAt = &parsed
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CompleteBatchDeliveryMergeStep(ctx context.Context,
	step domain.BatchDeliveryMergeStep, nextIndex int, integrationHead string,
	queueStatus domain.BatchDeliveryMergeQueueStatus, failureCode, failureSummary string,
) error {
	if strings.TrimSpace(step.QueueID) == "" || step.StepIndex < 0 ||
		step.Ordinal < 1 || step.Ordinal > domain.MaxBatchDeliveryTasks ||
		!batchCommitValid(step.InputHead) || !batchCommitValid(step.PreMergeHead) ||
		(step.PostMergeHead != "" && !batchCommitValid(step.PostMergeHead)) ||
		step.CreatedAt.IsZero() || step.CompletedAt == nil || step.CompletedAt.IsZero() ||
		(queueStatus != domain.BatchMergeQueueRunning && queueStatus != domain.BatchMergeQueueBlocked &&
			queueStatus != domain.BatchMergeQueueCompleted) {
		return apperror.New(apperror.CodeInvalidArgument, "batch merge step is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if queueStatus != domain.BatchMergeQueueBlocked {
		var runStatus string
		if err := tx.QueryRowContext(ctx, `SELECT run.status
			FROM batch_delivery_merge_queues queue
			JOIN batch_delivery_plans plan ON plan.id = queue.plan_id
			JOIN runs run ON run.id = plan.run_id
			WHERE queue.id = ?`, step.QueueID).Scan(&runStatus); err != nil {
			return err
		}
		if runStatus != string(domain.RunRunning) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"batch merge completion requires a running Run")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO batch_delivery_merge_steps
		(queue_id, step_index, ordinal, input_head, pre_merge_head, post_merge_head,
		status, validation_json, failure_code, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, step.QueueID, step.StepIndex,
		step.Ordinal, step.InputHead, step.PreMergeHead, step.PostMergeHead, step.Status,
		step.ValidationJSON, step.FailureCode, ts(step.CreatedAt), ts(*step.CompletedAt)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_delivery_merge_queues SET status = ?,
		next_index = ?, integration_head = ?, failure_code = ?, failure_summary = ?, updated_at = ?
		WHERE id = ? AND status = 'running' AND next_index = ?`, queueStatus, nextIndex,
		integrationHead, failureCode, failureSummary, ts(*step.CompletedAt), step.QueueID,
		step.StepIndex)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return apperror.New(apperror.CodeConflict, "batch merge queue advanced concurrently")
	}
	if queueStatus == domain.BatchMergeQueueCompleted {
		var planID string
		if err := tx.QueryRowContext(ctx, `SELECT plan_id FROM batch_delivery_merge_queues
			WHERE id = ?`, step.QueueID).Scan(&planID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_plans SET status = 'completed',
			updated_at = ? WHERE id = ? AND status = 'merging'`, ts(*step.CompletedAt), planID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_workspaces SET status = 'merged',
			updated_at = ? WHERE plan_id = ? AND status = 'accepted'`,
			ts(*step.CompletedAt), planID); err != nil {
			return err
		}
	} else if queueStatus == domain.BatchMergeQueueBlocked {
		if _, err := tx.ExecContext(ctx, `UPDATE batch_delivery_plans SET status = 'blocked',
			updated_at = ? WHERE id = (SELECT plan_id FROM batch_delivery_merge_queues WHERE id = ?)
			AND status = 'merging'`, ts(*step.CompletedAt), step.QueueID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func requireBatchPlanRunRunningTx(ctx context.Context, tx *sql.Tx,
	plan domain.BatchDeliveryPlan,
) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`,
		plan.RunID).Scan(&status); err != nil {
		return err
	}
	if status != string(domain.RunRunning) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery durable transition requires a running Run")
	}
	return nil
}

func (s *SQLiteStore) updateBatchMergeQueue(ctx context.Context, queueID string,
	from, to domain.BatchDeliveryMergeQueueStatus, nextIndex int, head, code, summary string,
	now time.Time,
) error {
	if strings.TrimSpace(queueID) == "" || now.IsZero() || nextIndex < 0 ||
		(head != "" && !batchCommitValid(head)) {
		return apperror.New(apperror.CodeInvalidArgument, "batch merge queue transition is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE batch_delivery_merge_queues
		SET status = ?, next_index = ?, integration_head = ?, failure_code = ?,
		failure_summary = ?, updated_at = ? WHERE id = ? AND status = ?`,
		to, nextIndex, head, code, summary, ts(now), queueID, from)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return apperror.New(apperror.CodeConflict, "batch merge queue changed concurrently")
	}
	return nil
}

func batchCommitValid(value string) bool {
	if (len(value) != 40 && len(value) != 64) || value != strings.ToLower(value) {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func batchDigestValid(value string) bool {
	return len(value) == 64 && batchCommitValid(value)
}
