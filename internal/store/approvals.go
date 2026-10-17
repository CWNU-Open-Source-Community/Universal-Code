package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/toolrun"
)

const defaultApprovalListLimit = 100
const maxApprovalListLimit = 500

func (s *SQLiteStore) EnsureApproval(ctx context.Context, proposal approval.Proposal) (approval.Record, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return approval.Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateApprovalProposalSourceTx(ctx, tx, proposal); err != nil {
		return approval.Record{}, err
	}
	record, _, err := ensureApprovalTx(ctx, tx, proposal)
	if err != nil {
		return approval.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.Record{}, err
	}
	return record, nil
}

func (s *SQLiteStore) DecideApproval(ctx context.Context, request approval.DecisionRequest) (approval.DecisionResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return approval.DecisionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := decideApprovalTx(ctx, tx, request)
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.DecisionResult{}, err
	}
	return result, nil
}

func decideApprovalTx(ctx context.Context, tx *sql.Tx,
	request approval.DecisionRequest,
) (approval.DecisionResult, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return approval.DecisionResult{}, err
	}
	normalized.Reason = redact.String(normalized.Reason)
	normalized.ReviewedBy = redact.String(normalized.ReviewedBy)
	fingerprint := approval.DecisionFingerprint(normalized)
	operationKey := approval.OperationKeyDigest(normalized.IdempotencyKey)
	desired, _ := normalized.Action.Status()

	operation, found, err := getApprovalOperationTx(ctx, tx, operationKey)
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if found {
		if operation.Action != normalized.Action || operation.RequestFingerprint != fingerprint || operation.ResultStatus != desired {
			return approval.DecisionResult{}, errors.New("approval idempotency key was already used for a different decision")
		}
		record, err := getApprovalTx(ctx, tx, operation.ApprovalID, "")
		if err != nil {
			return approval.DecisionResult{}, err
		}
		if record.ProposalID != normalized.ProposalID {
			return approval.DecisionResult{}, errors.New("approval idempotency key belongs to a different proposal")
		}
		return approval.DecisionResult{Approval: record, Replayed: true}, nil
	}

	record, err := getApprovalTx(ctx, tx, "", normalized.ProposalID)
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if record.ToolName == "host_command_propose" {
		return approval.DecisionResult{}, errors.New("historical host approvals are read-only; use Command Runtime for a new command")
	}

	changed := false
	if record.Status == approval.StatusPending {
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE tool_approvals SET
			status = ?, decision_reason = ?, reviewed_by = ?, version = version + 1,
			updated_at = ?, decided_at = ? WHERE id = ? AND version = ? AND status = ?`,
			desired, normalized.Reason, normalized.ReviewedBy, ts(now), ts(now), record.ID, record.Version, approval.StatusPending)
		if err != nil {
			return approval.DecisionResult{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return approval.DecisionResult{}, err
		}
		if rows != 1 {
			return approval.DecisionResult{}, errors.New("approval changed concurrently")
		}
		record.Status = desired
		record.DecisionReason = normalized.Reason
		record.ReviewedBy = normalized.ReviewedBy
		record.Version++
		record.UpdatedAt = now
		record.DecidedAt = &now
		changed = true
	} else if record.Status != desired {
		return approval.DecisionResult{}, fmt.Errorf("approval %s is already %s", record.ID, record.Status)
	}
	if err := record.Validate(); err != nil {
		return approval.DecisionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_operations
		(idempotency_key, approval_id, action, request_fingerprint, result_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, operationKey, record.ID, normalized.Action, fingerprint, desired, ts(time.Now().UTC())); err != nil {
		return approval.DecisionResult{}, err
	}
	if changed {
		if err := appendApprovalEventTx(ctx, tx, record, events.ApprovalDecidedEvent); err != nil {
			return approval.DecisionResult{}, err
		}
	}
	return approval.DecisionResult{Approval: record, Replayed: !changed}, nil
}

func (s *SQLiteStore) GetApproval(ctx context.Context, id string) (approval.Record, error) {
	id = strings.TrimSpace(id)
	if err := validateApprovalFilterIdentity("id", id, false); err != nil {
		return approval.Record{}, err
	}
	return getApprovalRow(s.db.QueryRowContext(ctx, approvalSelect+` WHERE id = ?`, id))
}

func (s *SQLiteStore) GetApprovalByProposal(ctx context.Context, proposalID string) (approval.Record, error) {
	proposalID = strings.TrimSpace(proposalID)
	if err := validateApprovalFilterIdentity("proposal id", proposalID, false); err != nil {
		return approval.Record{}, err
	}
	return getApprovalRow(s.db.QueryRowContext(ctx, approvalSelect+` WHERE proposal_id = ?`, proposalID))
}

func (s *SQLiteStore) ListApprovals(ctx context.Context, filter approval.ListFilter) ([]approval.Record, error) {
	filter.RunID = strings.TrimSpace(filter.RunID)
	filter.SessionID = strings.TrimSpace(filter.SessionID)
	filter.ToolName = strings.TrimSpace(filter.ToolName)
	for label, value := range map[string]string{"run id": filter.RunID, "session id": filter.SessionID, "tool name": filter.ToolName} {
		if err := validateApprovalFilterIdentity(label, value, true); err != nil {
			return nil, err
		}
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, fmt.Errorf("invalid approval status %q", filter.Status)
	}
	if filter.Limit < 0 || filter.Limit > maxApprovalListLimit {
		return nil, fmt.Errorf("approval limit must be between 0 and %d", maxApprovalListLimit)
	}
	if filter.Limit == 0 {
		filter.Limit = defaultApprovalListLimit
	}
	query := approvalSelect + ` WHERE 1=1`
	var args []any
	if filter.RunID != "" {
		query += ` AND run_id = ?`
		args = append(args, filter.RunID)
	}
	if filter.SessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, filter.SessionID)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if filter.ToolName != "" {
		query += ` AND tool_name = ?`
		args = append(args, filter.ToolName)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []approval.Record
	for rows.Next() {
		record, err := getApprovalRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func syncToolApprovalTx(ctx context.Context, tx *sql.Tx, run toolrun.ToolRun, previousStatus string, existed bool) error {
	status, mode, reviewer, decidedAt, err := approvalStateForToolRun(run, existed)
	if err != nil {
		return err
	}
	proposal := approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey("shell", run.ID), ProposalID: run.ID,
		SessionID: run.SessionID, WorkspaceID: run.WorkspaceID, ToolName: "shell", ActionClass: "shell",
		Mode: mode, Status: status, RequestFingerprint: approval.ShellFingerprint(run.SessionID, run.WorkspaceID, run.Command),
		DecisionReason: run.PolicyReason, RequestedBy: "tool_gateway", ReviewedBy: reviewer,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, DecidedAt: decidedAt,
	}
	if existed && run.Status == toolrun.StatusDenied {
		record, loadErr := getApprovalTx(ctx, tx, "", run.ID)
		if loadErr == nil {
			proposal.Mode = record.Mode
		} else if !errors.Is(loadErr, sql.ErrNoRows) {
			return loadErr
		}
	}
	if !existed || previousStatus == run.Status {
		_, _, err := ensureApprovalTx(ctx, tx, proposal)
		return err
	}
	return requireApprovalStatusTx(ctx, tx, proposal, status)
}

func validateApprovalProposalSourceTx(ctx context.Context, tx *sql.Tx, proposal approval.Proposal) error {
	proposal.ProposalID = strings.TrimSpace(proposal.ProposalID)
	proposal.SessionID = strings.TrimSpace(proposal.SessionID)
	proposal.WorkspaceID = strings.TrimSpace(proposal.WorkspaceID)
	proposal.ToolName = strings.TrimSpace(proposal.ToolName)
	proposal.ActionClass = strings.TrimSpace(proposal.ActionClass)
	proposal.Mode = strings.TrimSpace(proposal.Mode)
	for label, value := range map[string]string{
		"proposal id": proposal.ProposalID, "session id": proposal.SessionID, "workspace id": proposal.WorkspaceID,
		"tool name": proposal.ToolName, "action class": proposal.ActionClass, "mode": proposal.Mode,
	} {
		if err := validateApprovalFilterIdentity(label, value, label == "session id" || label == "workspace id"); err != nil {
			return err
		}
	}
	if proposal.RequestedBy == "specialist_runner" {
		return validateSpecialistApprovalSourceTx(ctx, tx, proposal)
	}
	switch proposal.ToolName {
	case string(toolgateway.CommandRuntimeTool):
		return validateCommandApprovalSourceTx(ctx, tx, proposal)
	case mcp.OperationApprovalTool:
		return validateMCPApprovalSourceTx(ctx, tx, proposal)
	case toolgateway.AgentBrowserApprovalTool:
		return validateAgentBrowserApprovalSourceTx(ctx, tx, proposal)
	case "thread.git":
		return validateThreadGitApprovalSourceTx(ctx, tx, proposal)
	case "shell":
		var sessionID, workspaceID sql.NullString
		var command, status string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id, command, status FROM tool_runs WHERE id = ?`, proposal.ProposalID).
			Scan(&sessionID, &workspaceID, &command, &status); err != nil {
			return err
		}
		expectedStatus, expectedMode, _, _, err := approvalStateForToolRun(toolrun.ToolRun{Status: status}, true)
		if err != nil {
			return err
		}
		if status == toolrun.StatusDenied && proposal.Mode == "never" {
			expectedMode = "never"
		}
		if proposal.SessionID != sessionID.String || proposal.WorkspaceID != workspaceID.String ||
			proposal.ActionClass != "shell" || proposal.Status != expectedStatus || proposal.Mode != expectedMode ||
			proposal.RequestFingerprint != approval.ShellFingerprint(sessionID.String, workspaceID.String, command) {
			return errors.New("approval request does not match the stored shell proposal")
		}
	case "replace_file", "create_file", "move_file", "delete_file":
		var sessionID sql.NullString
		var workspaceID, operation, path, destinationPath, originalHash, proposedHash string
		var destinationOriginalHash, destinationProposedHash, status string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id, operation_kind,
			path, destination_path, original_hash, proposed_hash,
			destination_original_hash, destination_proposed_hash, status
			FROM file_edits WHERE id = ?`, proposal.ProposalID).
			Scan(&sessionID, &workspaceID, &operation, &path, &destinationPath,
				&originalHash, &proposedHash, &destinationOriginalHash,
				&destinationProposedHash, &status); err != nil {
			return err
		}
		edit := fileedit.Edit{Status: status, Operation: operation, Path: path,
			DestinationPath: destinationPath, OriginalHash: originalHash,
			ProposedHash: proposedHash, DestinationOriginalHash: destinationOriginalHash,
			DestinationProposedHash: destinationProposedHash}
		expectedStatus, _, _, err := approvalStateForFileEdit(edit, true)
		if err != nil {
			return err
		}
		if proposal.SessionID != sessionID.String || proposal.WorkspaceID != workspaceID ||
			proposal.ToolName != fileedit.ApprovalToolName(edit) ||
			proposal.ActionClass != "workspace_write" || proposal.Mode != "per_call" ||
			proposal.Status != expectedStatus || proposal.RequestFingerprint !=
			fileedit.ApprovalFingerprint(sessionID.String, workspaceID, edit) {
			return errors.New("approval request does not match the stored file edit proposal")
		}
	case "script_process":
		var sessionID, workspaceID, approvalFingerprint, status string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id, approval_fingerprint, status
			FROM script_process_proposals WHERE id = ?`, proposal.ProposalID).
			Scan(&sessionID, &workspaceID, &approvalFingerprint, &status); err != nil {
			return err
		}
		expectedStatus, expectedMode, _, _, err := approvalStateForScriptProcess(scriptprocess.Process{Status: scriptprocess.Status(status)}, true)
		if err != nil {
			return err
		}
		if status == string(scriptprocess.StatusDenied) && proposal.Mode == "never" {
			expectedMode = "never"
		}
		if proposal.SessionID != sessionID || proposal.WorkspaceID != workspaceID ||
			proposal.ActionClass != "process" || proposal.Mode != expectedMode || proposal.Status != expectedStatus ||
			proposal.RequestFingerprint != approvalFingerprint {
			return errors.New("approval request does not match the stored script process proposal")
		}
	case "sandbox.manifest":
		var sessionID, workspaceID, authorizationFingerprint, runStatus string
		var boundApprovalID, validationApprovalStatus string
		var policyAllowed, needsApproval int
		if err := tx.QueryRowContext(ctx, `SELECT run.session_id, preparation.workspace_id,
			preparation.authorization_fingerprint, run.status, validation.policy_allowed,
			validation.needs_approval, validation.approval_id, validation.approval_status
			FROM sandbox_manifest_preparations preparation
			JOIN sandbox_manifest_validations validation ON validation.preparation_id = preparation.id
			JOIN runs run ON run.id = preparation.run_id WHERE preparation.id = ?`, proposal.ProposalID).
			Scan(&sessionID, &workspaceID, &authorizationFingerprint, &runStatus,
				&policyAllowed, &needsApproval, &boundApprovalID, &validationApprovalStatus); err != nil {
			return err
		}
		if proposal.SessionID != sessionID || proposal.WorkspaceID != workspaceID ||
			proposal.ActionClass != "sandbox_execute" || proposal.Mode != "per_call" ||
			proposal.Status != approval.StatusPending || proposal.RequestFingerprint != authorizationFingerprint ||
			policyAllowed != 1 || needsApproval != 1 || boundApprovalID != "" ||
			validationApprovalStatus != "required" ||
			runStatus == string(domain.RunCompleted) || runStatus == string(domain.RunFailed) ||
			runStatus == string(domain.RunCancelled) {
			return errors.New("approval request does not match the stored sandbox Manifest preparation")
		}
	case gitadvanced.ApprovalToolName:
		var sessionID, workspaceID, approvalFingerprint, operationStatus string
		var approvalID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id,
			approval_fingerprint, status, approval_id FROM git_advanced_operations
			WHERE id = ?`, proposal.ProposalID).Scan(&sessionID, &workspaceID,
			&approvalFingerprint, &operationStatus, &approvalID); err != nil {
			return err
		}
		if proposal.SessionID != sessionID || proposal.WorkspaceID != workspaceID ||
			proposal.ActionClass != gitadvanced.ApprovalActionClass ||
			proposal.Mode != "per_call" || proposal.Status != approval.StatusPending ||
			proposal.RequestFingerprint != approvalFingerprint ||
			operationStatus != string(gitadvanced.OperationProposed) || approvalID.Valid {
			return errors.New("approval request does not match the stored Git advanced preview")
		}
	case "github.pull_request":
		return validateThreadPullRequestApprovalSourceTx(ctx, tx, proposal)
	case githubreview.ApprovalToolName:
		var sessionID, workspaceID, approvalFingerprint, operationStatus string
		var approvalID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id,
			approval_fingerprint, status, approval_id FROM github_review_write_operations
			WHERE id = ?`, proposal.ProposalID).Scan(&sessionID, &workspaceID,
			&approvalFingerprint, &operationStatus, &approvalID); err != nil {
			return err
		}
		if proposal.SessionID != sessionID || proposal.WorkspaceID != workspaceID ||
			proposal.ActionClass != githubreview.ApprovalActionClass ||
			proposal.Mode != "per_call" || proposal.Status != approval.StatusPending ||
			proposal.RequestFingerprint != approvalFingerprint ||
			operationStatus != string(githubreview.OperationProposed) || approvalID.Valid {
			return errors.New("approval request does not match the stored GitHub review write preview")
		}
	case "web_fetch":
		var sessionID, workspaceID, requestFingerprint, status string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, workspace_id,
			request_fingerprint, status FROM web_fetch_authorizations WHERE id = ?`,
			proposal.ProposalID).Scan(&sessionID, &workspaceID, &requestFingerprint,
			&status); err != nil {
			return err
		}
		if proposal.SessionID != sessionID || proposal.WorkspaceID != workspaceID ||
			proposal.ActionClass != webFetchApprovalActionClass ||
			proposal.Mode != "per_call" || proposal.Status != approval.StatusPending ||
			proposal.RequestFingerprint != requestFingerprint || status != "pending" {
			return errors.New("approval request does not match the stored web fetch authorization")
		}
	default:
		return fmt.Errorf("unsupported approval tool %q", proposal.ToolName)
	}
	return nil
}

func syncFileEditApprovalTx(ctx context.Context, tx *sql.Tx, edit fileedit.Edit, previousStatus string, existed bool) error {
	_, automatic, err := getFileEditAutoAuthorizationTx(ctx, tx, edit.ID)
	if err != nil {
		return err
	}
	status, reviewer, decidedAt, err := approvalStateForFileEdit(edit, existed || automatic)
	if err != nil {
		return err
	}
	mode := "per_call"
	decisionReason := edit.Reason
	if automatic {
		if edit.Status == fileedit.StatusProposed || edit.Status == fileedit.StatusDenied ||
			status != approval.StatusApproved {
			return errors.New("automatic FileEdit cannot require or receive operator review")
		}
		mode = "automatic"
		reviewer = "automatic_policy"
		decisionReason = "Current operation policy automatically authorized this exact file edit"
	}
	toolName := fileedit.ApprovalToolName(edit)
	requestedBy := "tool_gateway"
	if _, _, child, err := specialistFileEditBindingTx(ctx, tx, edit.SessionID, false); err != nil {
		return err
	} else if child {
		requestedBy = "specialist_fileedit"
	}
	proposal := approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey(toolName, edit.ID), ProposalID: edit.ID,
		SessionID: edit.SessionID, WorkspaceID: edit.WorkspaceID, ToolName: toolName, ActionClass: "workspace_write",
		Mode: mode, Status: status,
		RequestFingerprint: fileedit.ApprovalFingerprint(edit.SessionID, edit.WorkspaceID, edit),
		DecisionReason:     decisionReason, RequestedBy: requestedBy, ReviewedBy: reviewer,
		CreatedAt: edit.CreatedAt, UpdatedAt: edit.UpdatedAt, DecidedAt: decidedAt,
	}
	if !existed || previousStatus == edit.Status {
		_, _, err := ensureApprovalTx(ctx, tx, proposal)
		return err
	}
	return requireApprovalStatusTx(ctx, tx, proposal, status)
}

func approvalStateForToolRun(run toolrun.ToolRun, existed bool) (approval.Status, string, string, *time.Time, error) {
	if !existed && run.Status != toolrun.StatusProposed && run.Status != toolrun.StatusDenied {
		return "", "", "", nil, errors.New("new tool run must begin as proposed or policy-denied")
	}
	switch run.Status {
	case toolrun.StatusProposed:
		return approval.StatusPending, "per_call", "", nil, nil
	case toolrun.StatusDenied:
		if !existed {
			decided := run.UpdatedAt
			return approval.StatusDenied, "never", "policy", &decided, nil
		}
		return approval.StatusDenied, "per_call", "operator", &run.UpdatedAt, nil
	case toolrun.StatusApproved, toolrun.StatusRunning, toolrun.StatusCompleted, toolrun.StatusFailed:
		return approval.StatusApproved, "per_call", "operator", &run.UpdatedAt, nil
	default:
		return "", "", "", nil, fmt.Errorf("invalid tool run status %q", run.Status)
	}
}

func approvalStateForFileEdit(edit fileedit.Edit, existed bool) (approval.Status, string, *time.Time, error) {
	if !existed && edit.Status != fileedit.StatusProposed {
		return "", "", nil, errors.New("new file edit must begin as proposed")
	}
	switch edit.Status {
	case fileedit.StatusProposed:
		return approval.StatusPending, "", nil, nil
	case fileedit.StatusDenied:
		return approval.StatusDenied, "operator", &edit.UpdatedAt, nil
	case fileedit.StatusApproved, fileedit.StatusApplied, fileedit.StatusFailed:
		return approval.StatusApproved, "operator", &edit.UpdatedAt, nil
	default:
		return "", "", nil, fmt.Errorf("invalid file edit status %q", edit.Status)
	}
}

func ensureApprovalTx(ctx context.Context, tx *sql.Tx, proposal approval.Proposal) (approval.Record, bool, error) {
	proposal.IdempotencyKey = strings.TrimSpace(proposal.IdempotencyKey)
	proposal.ProposalID = strings.TrimSpace(proposal.ProposalID)
	proposal.SessionID = strings.TrimSpace(proposal.SessionID)
	proposal.WorkspaceID = strings.TrimSpace(proposal.WorkspaceID)
	proposal.ToolName = strings.TrimSpace(proposal.ToolName)
	proposal.ActionClass = strings.TrimSpace(proposal.ActionClass)
	proposal.Mode = strings.TrimSpace(proposal.Mode)
	proposal.DecisionReason = redact.String(strings.TrimSpace(proposal.DecisionReason))
	proposal.RequestedBy = redact.String(strings.TrimSpace(proposal.RequestedBy))
	proposal.ReviewedBy = redact.String(strings.TrimSpace(proposal.ReviewedBy))
	if proposal.CreatedAt.IsZero() {
		proposal.CreatedAt = time.Now().UTC()
	}
	if proposal.UpdatedAt.IsZero() {
		proposal.UpdatedAt = proposal.CreatedAt
	}
	binding, bound, err := runBindingForSessionTx(ctx, tx, proposal.SessionID)
	if err != nil {
		return approval.Record{}, false, err
	}
	if !bound && (proposal.RequestedBy == "specialist_runner" || proposal.RequestedBy == "specialist_fileedit") {
		binding, bound, err = specialistApprovalRunBindingTx(ctx, tx, proposal.SessionID)
		if err != nil {
			return approval.Record{}, false, err
		}
	}
	runID := ""
	if bound {
		if strings.TrimSpace(binding.WorkspaceID) != proposal.WorkspaceID {
			if proposal.ToolName == "thread.git" && proposal.ActionClass == "git_write" {
				if err := requireFileEditWorkspaceTx(ctx, tx, proposal.SessionID, proposal.WorkspaceID, true); err != nil {
					return approval.Record{}, false, err
				}
			} else if err := requireFileEditApprovalWorkspaceTx(ctx, tx, proposal.SessionID,
				proposal.WorkspaceID, proposal.ProposalID, proposal.ToolName, proposal.ActionClass); err != nil {
				return approval.Record{}, false, err
			}
		}
		runID = binding.RunID
	}
	record := approval.Record{
		ID: idgen.New("approval"), IdempotencyKey: proposal.IdempotencyKey, ProposalID: proposal.ProposalID,
		RunID: runID, SessionID: proposal.SessionID, WorkspaceID: proposal.WorkspaceID,
		ToolName: proposal.ToolName, ActionClass: proposal.ActionClass, Mode: proposal.Mode, Status: proposal.Status,
		RequestFingerprint: proposal.RequestFingerprint, DecisionReason: proposal.DecisionReason,
		RequestedBy: proposal.RequestedBy, ReviewedBy: proposal.ReviewedBy, Version: 1,
		CreatedAt: proposal.CreatedAt, UpdatedAt: proposal.UpdatedAt, DecidedAt: proposal.DecidedAt,
	}
	if err := record.Validate(); err != nil {
		return approval.Record{}, false, err
	}
	existing, err := getApprovalTx(ctx, tx, "", proposal.ProposalID)
	if err == nil {
		if err := approvalIdentityMatches(existing, record); err != nil {
			return approval.Record{}, false, err
		}
		if existing.RunID != "" && record.RunID != "" && existing.RunID != record.RunID {
			return approval.Record{}, false, errors.New("approval is already bound to a different run")
		}
		if existing.RunID == "" && record.RunID != "" {
			now := time.Now().UTC()
			result, updateErr := tx.ExecContext(ctx, `UPDATE tool_approvals SET run_id = ?, version = version + 1,
				updated_at = ? WHERE id = ? AND version = ? AND run_id IS NULL`,
				record.RunID, ts(now), existing.ID, existing.Version)
			if updateErr != nil {
				return approval.Record{}, false, updateErr
			}
			rows, updateErr := result.RowsAffected()
			if updateErr != nil {
				return approval.Record{}, false, updateErr
			}
			if rows != 1 {
				return approval.Record{}, false, errors.New("approval run binding changed concurrently")
			}
			existing.RunID = record.RunID
			existing.Version++
			existing.UpdatedAt = now
			if err := appendApprovalEventTx(ctx, tx, existing, events.ApprovalBoundEvent); err != nil {
				return approval.Record{}, false, err
			}
		}
		if record.Status != approval.StatusPending && existing.Status != record.Status {
			return approval.Record{}, false, fmt.Errorf("approval %s is %s, expected %s", existing.ID, existing.Status, record.Status)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return approval.Record{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tool_approvals
		(id, idempotency_key, proposal_id, run_id, session_id, workspace_id, tool_name, action_class, mode,
		 status, request_fingerprint, decision_reason, requested_by, reviewed_by, version, created_at, updated_at, decided_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.IdempotencyKey, record.ProposalID, record.RunID, record.SessionID, record.WorkspaceID,
		record.ToolName, record.ActionClass, record.Mode, record.Status, record.RequestFingerprint,
		record.DecisionReason, record.RequestedBy, record.ReviewedBy, record.Version,
		ts(record.CreatedAt), ts(record.UpdatedAt), optionalTS(record.DecidedAt))
	if err != nil {
		return approval.Record{}, false, err
	}
	if err := appendApprovalEventTx(ctx, tx, record, events.ApprovalRequestedEvent); err != nil {
		return approval.Record{}, false, err
	}
	if record.Status != approval.StatusPending {
		if err := appendApprovalEventTx(ctx, tx, record, events.ApprovalDecidedEvent); err != nil {
			return approval.Record{}, false, err
		}
	}
	return record, true, nil
}

func requireApprovalStatusTx(ctx context.Context, tx *sql.Tx, proposal approval.Proposal, required approval.Status) error {
	record, err := getApprovalTx(ctx, tx, "", proposal.ProposalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("tool proposal has no durable approval record")
		}
		return err
	}
	expected := approval.Record{
		IdempotencyKey: proposal.IdempotencyKey, ProposalID: proposal.ProposalID,
		SessionID: proposal.SessionID, WorkspaceID: proposal.WorkspaceID, ToolName: proposal.ToolName,
		ActionClass: proposal.ActionClass, Mode: proposal.Mode, RequestFingerprint: proposal.RequestFingerprint,
	}
	if err := approvalIdentityMatches(record, expected); err != nil {
		return err
	}
	if record.Status != required {
		return fmt.Errorf("tool proposal requires durable approval status %s, got %s", required, record.Status)
	}
	return nil
}

func approvalIdentityMatches(existing approval.Record, expected approval.Record) error {
	if existing.IdempotencyKey != expected.IdempotencyKey || existing.ProposalID != expected.ProposalID ||
		existing.SessionID != expected.SessionID || existing.WorkspaceID != expected.WorkspaceID ||
		existing.ToolName != expected.ToolName || existing.ActionClass != expected.ActionClass ||
		existing.Mode != expected.Mode || existing.RequestFingerprint != expected.RequestFingerprint {
		return errors.New("approval proposal identity or fingerprint changed")
	}
	return nil
}

func appendApprovalEventTx(ctx context.Context, tx *sql.Tx, record approval.Record, eventType string) error {
	return appendRunEventForSessionTx(ctx, tx, record.SessionID, eventType, "approval_store", record.ID, map[string]any{
		"approval_id": record.ID, "proposal_id": record.ProposalID, "grant_id": record.GrantID, "run_id": record.RunID,
		"session_id": record.SessionID, "workspace_id": record.WorkspaceID, "tool_name": record.ToolName,
		"action_class": record.ActionClass, "mode": record.Mode, "status": record.Status,
		"request_fingerprint": record.RequestFingerprint, "reason": record.DecisionReason,
		"requested_by": record.RequestedBy, "reviewed_by": record.ReviewedBy, "version": record.Version,
	})
}

const approvalSelect = `SELECT id, idempotency_key, proposal_id, grant_id, run_id, session_id, workspace_id,
	tool_name, action_class, mode, status, request_fingerprint, decision_reason, requested_by,
	reviewed_by, version, created_at, updated_at, decided_at FROM tool_approvals`

func getApprovalTx(ctx context.Context, tx *sql.Tx, id string, proposalID string) (approval.Record, error) {
	if strings.TrimSpace(id) != "" {
		return getApprovalRow(tx.QueryRowContext(ctx, approvalSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	}
	return getApprovalRow(tx.QueryRowContext(ctx, approvalSelect+` WHERE proposal_id = ?`, strings.TrimSpace(proposalID)))
}

func getApprovalRow(row scanner) (approval.Record, error) {
	var record approval.Record
	var grantID, runID sql.NullString
	var createdAt, updatedAt string
	var decidedAt sql.NullString
	if err := row.Scan(&record.ID, &record.IdempotencyKey, &record.ProposalID, &grantID, &runID, &record.SessionID,
		&record.WorkspaceID, &record.ToolName, &record.ActionClass, &record.Mode, &record.Status,
		&record.RequestFingerprint, &record.DecisionReason, &record.RequestedBy, &record.ReviewedBy,
		&record.Version, &createdAt, &updatedAt, &decidedAt); err != nil {
		return approval.Record{}, err
	}
	record.GrantID = grantID.String
	record.RunID = runID.String
	record.CreatedAt = parseTS(createdAt)
	record.UpdatedAt = parseTS(updatedAt)
	if decidedAt.Valid {
		parsed := parseTS(decidedAt.String)
		record.DecidedAt = &parsed
	}
	if err := record.Validate(); err != nil {
		return approval.Record{}, err
	}
	return record, nil
}

type approvalOperation struct {
	ApprovalID         string
	Action             approval.Action
	RequestFingerprint string
	ResultStatus       approval.Status
}

func getApprovalOperationTx(ctx context.Context, tx *sql.Tx, key string) (approvalOperation, bool, error) {
	var operation approvalOperation
	err := tx.QueryRowContext(ctx, `SELECT approval_id, action, request_fingerprint, result_status
		FROM approval_operations WHERE idempotency_key = ?`, key).
		Scan(&operation.ApprovalID, &operation.Action, &operation.RequestFingerprint, &operation.ResultStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return approvalOperation{}, false, nil
	}
	return operation, err == nil, err
}

func optionalTS(value *time.Time) any {
	if value == nil {
		return nil
	}
	return ts(*value)
}

func validateApprovalFilterIdentity(label string, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("approval %s is required", label)
	}
	if !utf8.ValidString(value) || len([]rune(value)) > approval.MaxIdentityRunes {
		return fmt.Errorf("approval %s must be bounded UTF-8", label)
	}
	return nil
}
