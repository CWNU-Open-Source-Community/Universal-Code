package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

const webFetchApprovalActionClass = "public_https_fetch"

func (s *SQLiteStore) PrepareWebFetchAuthorization(ctx context.Context,
	request domain.WebFetchAuthorizationRequest,
) (domain.WebFetchAuthorization, bool, error) {
	request.ID = strings.TrimSpace(request.ID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.MissionID = strings.TrimSpace(request.MissionID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.SupervisorToolCallID = strings.TrimSpace(request.SupervisorToolCallID)
	request.CanonicalURL = strings.TrimSpace(request.CanonicalURL)
	request.ExactTarget = strings.TrimSpace(request.ExactTarget)
	request.RequestFingerprint = strings.TrimSpace(request.RequestFingerprint)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if request.ID == "" || request.RunID == "" || request.MissionID == "" ||
		request.SessionID == "" || request.SupervisorToolCallID == "" ||
		request.CanonicalURL == "" || request.ExactTarget == "" ||
		len(request.RequestFingerprint) != 64 || request.SupervisorTurn < 1 ||
		request.RequestedBy == "" {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodeInvalidArgument, "web fetch authorization request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id,
		status, config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, request.RunID))
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	var threadID string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM thread_runs WHERE run_id = ?`,
		run.ID).Scan(&threadID); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if existing, found, err := getWebFetchAuthorizationForCallTx(ctx, tx,
		run.ID, request.SupervisorToolCallID); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	} else if found {
		if existing.RequestFingerprint != request.RequestFingerprint ||
			existing.CanonicalURL != request.CanonicalURL ||
			existing.ExactTarget != request.ExactTarget ||
			existing.SupervisorTurn != request.SupervisorTurn {
			return domain.WebFetchAuthorization{}, false, apperror.New(
				apperror.CodeConflict, "web fetch approval call binding changed")
		}
		if err := tx.Commit(); err != nil {
			return domain.WebFetchAuthorization{}, false, err
		}
		return existing, existing.Status == domain.WebFetchAuthorizationApproved ||
			existing.Status == domain.WebFetchAuthorizationConsumed, nil
	}
	if run.Status != domain.RunRunning || run.MissionID != request.MissionID ||
		run.SessionID != request.SessionID {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"web fetch approval requires the current running Run")
	}
	var toolName, status, payloadJSON string
	if err := tx.QueryRowContext(ctx, `SELECT tool_name, status, payload_json
		FROM run_supervisor_tool_calls WHERE run_id = ? AND turn = ? AND call_id = ?`,
		run.ID, request.SupervisorTurn, request.SupervisorToolCallID).
		Scan(&toolName, &status, &payloadJSON); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if toolName != "web_fetch" || status != string(domain.SupervisorToolPending) {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"web fetch approval is not bound to an exact pending Supervisor call")
	}
	var payload toolgateway.WebFetchPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return domain.WebFetchAuthorization{}, false, apperror.Wrap(
			apperror.CodeFailedPrecondition, "pending Web fetch payload is invalid", err)
	}
	boundURL := payload.URL
	if payload.SourceID != "" {
		source, found, sourceErr := getWebSource(ctx, tx, run.ID, payload.SourceID)
		if sourceErr != nil {
			return domain.WebFetchAuthorization{}, false, sourceErr
		}
		if !found {
			return domain.WebFetchAuthorization{}, false, apperror.New(
				apperror.CodeFailedPrecondition,
				"pending Web fetch source is no longer available")
		}
		boundURL = source.CanonicalURL
	}
	boundURL, err = webevidence.CanonicalizePublicHTTPSURL(boundURL)
	if err != nil {
		return domain.WebFetchAuthorization{}, false, apperror.Wrap(
			apperror.CodePolicyDenied, "pending Web fetch target is outside public HTTPS", err)
	}
	parsed, err := url.Parse(boundURL)
	if err != nil || parsed.Hostname() == "" {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodePolicyDenied, "pending Web fetch target host is invalid")
	}
	boundTarget := strings.ToLower(parsed.Hostname())
	if _, err := (webevidence.NetworkAuthority{Mode: "allowlist",
		AllowedTargets: []string{boundTarget}}).Authorize(boundURL); err != nil {
		return domain.WebFetchAuthorization{}, false, apperror.Wrap(
			apperror.CodePolicyDenied, "pending Web fetch target cannot be approved", err)
	}
	expectedFingerprint := approval.Fingerprint(domain.WebFetchAuthorizationProtocolVersion,
		run.ID, run.SessionID, request.SupervisorToolCallID, boundURL, boundTarget)
	if request.CanonicalURL != boundURL || request.ExactTarget != boundTarget ||
		request.RequestFingerprint != expectedFingerprint {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodeConflict,
			"web fetch approval does not match its exact pending Supervisor payload")
	}
	if grant, found, err := getThreadWebFetchGrantTx(ctx, tx, threadID,
		request.ExactTarget); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return domain.WebFetchAuthorization{}, false, err
		}
		return grant, true, nil
	}
	now := time.Now().UTC()
	approvalRecord, _, err := ensureApprovalTx(ctx, tx, approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey("web_fetch", request.ID),
		ProposalID:     request.ID, SessionID: run.SessionID,
		WorkspaceID: request.WorkspaceID, ToolName: "web_fetch",
		ActionClass: webFetchApprovalActionClass, Mode: "per_call",
		Status: approval.StatusPending, RequestFingerprint: request.RequestFingerprint,
		RequestedBy: request.RequestedBy, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	value := domain.WebFetchAuthorization{ID: request.ID, ApprovalID: approvalRecord.ID,
		ThreadID: threadID, RunID: run.ID, MissionID: run.MissionID,
		SessionID: run.SessionID, WorkspaceID: request.WorkspaceID,
		SupervisorTurn:       request.SupervisorTurn,
		SupervisorToolCallID: request.SupervisorToolCallID,
		CanonicalURL:         request.CanonicalURL, ExactTarget: request.ExactTarget,
		RequestFingerprint: request.RequestFingerprint,
		Scope:              domain.WebFetchAuthorizationOnce,
		Status:             domain.WebFetchAuthorizationPending, RequestedBy: request.RequestedBy,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := value.Validate(); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO web_fetch_authorizations
		(id, approval_id, thread_id, run_id, mission_id, session_id, workspace_id,
		supervisor_turn, supervisor_tool_call_id, canonical_url, exact_target,
		request_fingerprint, authorization_scope, status, requested_by, reviewed_by,
		version, created_at, updated_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 1, ?, ?, NULL)`,
		value.ID, value.ApprovalID, value.ThreadID, value.RunID, value.MissionID,
		value.SessionID, value.WorkspaceID, value.SupervisorTurn,
		value.SupervisorToolCallID, value.CanonicalURL, value.ExactTarget,
		value.RequestFingerprint, value.Scope, value.Status, value.RequestedBy,
		ts(now), ts(now)); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.WebFetchAuthorizationRequestedEvent, "web_fetch_authorization", value.ID,
		map[string]any{"approval_id": value.ApprovalID,
			"supervisor_turn":         value.SupervisorTurn,
			"supervisor_tool_call_id": value.SupervisorToolCallID,
			"exact_target":            value.ExactTarget, "canonical_url": value.CanonicalURL,
			"operator_review_required": true}); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if err := transitionSupervisorRunTx(ctx, tx, &run, domain.RunWaitingApproval,
		"public HTTPS host "+value.ExactTarget+" is awaiting operator approval", now); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	return value, false, nil
}

func (s *SQLiteStore) GetWebFetchAuthorization(ctx context.Context, id string) (
	domain.WebFetchAuthorization, error,
) {
	return getWebFetchAuthorizationRow(s.db.QueryRowContext(ctx,
		webFetchAuthorizationSelect+` WHERE id = ?`, strings.TrimSpace(id)))
}

func (s *SQLiteStore) GetWebFetchAuthorizationByApproval(ctx context.Context,
	approvalID string,
) (domain.WebFetchAuthorization, error) {
	return getWebFetchAuthorizationRow(s.db.QueryRowContext(ctx,
		webFetchAuthorizationSelect+` WHERE approval_id = ?`, strings.TrimSpace(approvalID)))
}

// ListRecoverableWebFetchAuthorizations returns decided calls whose owning
// Supervisor Turn has not crossed a durable boundary yet. A completed tool
// result remains recoverable while the Run is running: the process may have
// stopped after recording that result but before the next model step.
func (s *SQLiteStore) ListRecoverableWebFetchAuthorizations(ctx context.Context,
	runID string, limit int,
) ([]domain.WebFetchAuthorization, error) {
	runID = strings.TrimSpace(runID)
	if (runID != "" && !domain.ValidAgentID(runID)) || limit < 1 || limit > 101 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"recoverable Web fetch authorization filter is invalid")
	}
	rows, err := s.db.QueryContext(ctx, webFetchAuthorizationSelect+`
		WHERE id IN (SELECT recoverable_authorization.id
		FROM web_fetch_authorizations recoverable_authorization
		JOIN runs recoverable_run ON recoverable_run.id = recoverable_authorization.run_id
		JOIN run_supervisor_checkpoints recoverable_checkpoint
			ON recoverable_checkpoint.run_id = recoverable_authorization.run_id
		JOIN run_supervisor_tool_calls recoverable_call
			ON recoverable_call.run_id = recoverable_authorization.run_id
			AND recoverable_call.turn = recoverable_authorization.supervisor_turn
			AND recoverable_call.call_id = recoverable_authorization.supervisor_tool_call_id
		WHERE (? = '' OR recoverable_authorization.run_id = ?)
			AND recoverable_authorization.status IN ('approved','denied','consumed')
			AND recoverable_checkpoint.phase = ?
			AND recoverable_checkpoint.next_turn = recoverable_authorization.supervisor_turn
			AND recoverable_call.tool_name = 'web_fetch'
			AND ((recoverable_run.status = 'waiting_approval'
					AND recoverable_call.status = 'pending')
				OR (recoverable_run.status = 'running'
					AND recoverable_call.status IN ('pending','completed','denied','failed'))))
		ORDER BY decided_at, id LIMIT ?`,
		runID, runID, domain.SupervisorTurnStarted, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.WebFetchAuthorization, 0)
	for rows.Next() {
		value, scanErr := getWebFetchAuthorizationRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) DecideWebFetchAuthorization(ctx context.Context, id string,
	scope domain.WebFetchAuthorizationScope, approve bool, operationKey, reviewedBy,
	reason string,
) (domain.WebFetchAuthorization, bool, error) {
	id = strings.TrimSpace(id)
	if !scope.Valid() || (!approve && scope != domain.WebFetchAuthorizationOnce) {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodeInvalidArgument, "web fetch approval decision scope is invalid")
	}
	action := approval.ActionDeny
	if approve {
		action, reason = approval.ActionApprove, ""
	}
	desired := domain.WebFetchAuthorizationDenied
	if approve {
		desired = domain.WebFetchAuthorizationApproved
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getWebFetchAuthorizationTx(ctx, tx, id)
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	decision, err := decideApprovalTx(ctx, tx, approval.DecisionRequest{
		ProposalID: current.ID, IdempotencyKey: strings.TrimSpace(operationKey),
		Action: action, Reason: strings.TrimSpace(reason),
		ReviewedBy: strings.TrimSpace(reviewedBy),
	})
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if current.Status != domain.WebFetchAuthorizationPending {
		sameApprovedDecision := approve && current.Scope == scope &&
			(current.Status == domain.WebFetchAuthorizationApproved ||
				(current.Status == domain.WebFetchAuthorizationConsumed &&
					scope == domain.WebFetchAuthorizationOnce))
		if (!sameApprovedDecision && current.Status != desired) ||
			(approve && current.Scope != scope) {
			return domain.WebFetchAuthorization{}, false, apperror.New(
				apperror.CodeConflict, "web fetch approval was already decided differently")
		}
		if err := tx.Commit(); err != nil {
			return domain.WebFetchAuthorization{}, false, err
		}
		return current, true, nil
	}
	now := decision.Approval.UpdatedAt.UTC()
	result, err := tx.ExecContext(ctx, `UPDATE web_fetch_authorizations SET
		authorization_scope = ?, status = ?, reviewed_by = ?, version = version + 1,
		updated_at = ?, decided_at = ? WHERE id = ? AND version = ? AND status = 'pending'`,
		scope, desired, decision.Approval.ReviewedBy, ts(now), ts(now), current.ID,
		current.Version)
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return domain.WebFetchAuthorization{}, false, apperror.New(
			apperror.CodeConflict, "web fetch authorization changed concurrently")
	}
	current.Scope, current.Status, current.ReviewedBy = scope, desired,
		decision.Approval.ReviewedBy
	current.Version++
	current.UpdatedAt, current.DecidedAt = now, &now
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id,
		status, config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, current.RunID))
	if err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.WebFetchAuthorizationDecidedEvent, "web_fetch_authorization", current.ID,
		map[string]any{"approval_id": current.ApprovalID, "status": current.Status,
			"authorization_scope": current.Scope, "exact_target": current.ExactTarget}); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WebFetchAuthorization{}, false, err
	}
	return current, decision.Replayed, nil
}

func (s *SQLiteStore) ResumeWebFetchAuthorizationRun(ctx context.Context, id string) (
	domain.Run, bool, error,
) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := getWebFetchAuthorizationTx(ctx, tx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, false, err
	}
	if value.Status == domain.WebFetchAuthorizationPending {
		return domain.Run{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization is still pending")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id,
		status, config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, value.RunID))
	if err != nil {
		return domain.Run{}, false, err
	}
	if run.Status == domain.RunRunning {
		if err := tx.Commit(); err != nil {
			return domain.Run{}, false, err
		}
		return run, true, nil
	}
	if run.Status != domain.RunWaitingApproval {
		return domain.Run{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization Run is not waiting for approval")
	}
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, run.ID, time.Now().UTC()); err != nil {
		return domain.Run{}, false, err
	}
	checkpoint, found, err := getSupervisorCheckpointTx(ctx, tx, run.ID)
	if err != nil {
		return domain.Run{}, false, err
	}
	if !found || checkpoint.Phase != domain.SupervisorTurnStarted ||
		checkpoint.NextTurn != value.SupervisorTurn {
		return domain.Run{}, false, apperror.New(apperror.CodeConflict,
			"web fetch authorization no longer owns the waiting Supervisor Turn")
	}
	var toolName, callStatus, payloadJSON string
	if err := tx.QueryRowContext(ctx, `SELECT tool_name, status, payload_json
		FROM run_supervisor_tool_calls WHERE run_id = ? AND turn = ? AND call_id = ?`,
		value.RunID, value.SupervisorTurn, value.SupervisorToolCallID).
		Scan(&toolName, &callStatus, &payloadJSON); err != nil {
		return domain.Run{}, false, err
	}
	if toolName != "web_fetch" || callStatus != string(domain.SupervisorToolPending) {
		return domain.Run{}, false, apperror.New(apperror.CodeConflict,
			"web fetch authorization no longer owns the pending Supervisor call")
	}
	if err := validateWebFetchAuthorizationPayloadBinding(ctx, tx, value,
		payloadJSON); err != nil {
		return domain.Run{}, false, err
	}
	if err := transitionSupervisorRunTx(ctx, tx, &run, domain.RunRunning,
		"web fetch authorization decision is ready for exact continuation",
		time.Now().UTC()); err != nil {
		return domain.Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, false, err
	}
	return run, false, nil
}

func (s *SQLiteStore) ConsumeWebFetchAuthorization(ctx context.Context, runID,
	callID string,
) error {
	_, err := s.db.ExecContext(ctx, `UPDATE web_fetch_authorizations SET
		status = 'consumed', version = version + 1, updated_at = ?
		WHERE run_id = ? AND supervisor_tool_call_id = ? AND status = 'approved'
			AND authorization_scope = 'once'`, ts(time.Now().UTC()),
		strings.TrimSpace(runID), strings.TrimSpace(callID))
	return err
}

func validateWebFetchAuthorizationPayloadBinding(ctx context.Context, tx *sql.Tx,
	value domain.WebFetchAuthorization, payloadJSON string,
) error {
	var payload toolgateway.WebFetchPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"pending Web fetch payload is invalid", err)
	}
	boundURL := payload.URL
	if payload.SourceID != "" {
		source, found, err := getWebSource(ctx, tx, value.RunID, payload.SourceID)
		if err != nil {
			return err
		}
		if !found {
			return apperror.New(apperror.CodeFailedPrecondition,
				"pending Web fetch source is no longer available")
		}
		boundURL = source.CanonicalURL
	}
	boundURL, err := webevidence.CanonicalizePublicHTTPSURL(boundURL)
	if err != nil {
		return apperror.Wrap(apperror.CodePolicyDenied,
			"pending Web fetch target is outside public HTTPS", err)
	}
	parsed, err := url.Parse(boundURL)
	if err != nil || parsed.Hostname() == "" {
		return apperror.New(apperror.CodePolicyDenied,
			"pending Web fetch target host is invalid")
	}
	boundTarget := strings.ToLower(parsed.Hostname())
	if _, err := (webevidence.NetworkAuthority{Mode: "allowlist",
		AllowedTargets: []string{boundTarget}}).Authorize(boundURL); err != nil {
		return apperror.Wrap(apperror.CodePolicyDenied,
			"pending Web fetch target cannot be approved", err)
	}
	expectedFingerprint := approval.Fingerprint(domain.WebFetchAuthorizationProtocolVersion,
		value.RunID, value.SessionID, value.SupervisorToolCallID, boundURL, boundTarget)
	if value.CanonicalURL != boundURL || value.ExactTarget != boundTarget ||
		value.RequestFingerprint != expectedFingerprint {
		return apperror.New(apperror.CodeConflict,
			"web fetch approval no longer matches its exact pending Supervisor payload")
	}
	return nil
}

func getThreadWebFetchGrantTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, threadID, target string) (domain.WebFetchAuthorization, bool, error) {
	value, err := getWebFetchAuthorizationRow(queryer.QueryRowContext(ctx,
		webFetchAuthorizationSelect+` WHERE thread_id = ? AND exact_target = ?
		AND status = 'approved' AND authorization_scope = 'thread'
 AND julianday(decided_at) > COALESCE((SELECT MAX(julianday(selection.created_at)) FROM run_web_access_operations selection JOIN thread_runs member ON member.run_id=selection.run_id WHERE member.thread_id = ?),0)
		ORDER BY decided_at DESC, id DESC LIMIT 1`, threadID, target, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WebFetchAuthorization{}, false, nil
	}
	return value, err == nil, err
}

func getWebFetchAuthorizationForCallTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID, callID string) (domain.WebFetchAuthorization, bool, error) {
	value, err := getWebFetchAuthorizationRow(queryer.QueryRowContext(ctx,
		webFetchAuthorizationSelect+` WHERE run_id = ? AND supervisor_tool_call_id = ?`,
		runID, callID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WebFetchAuthorization{}, false, nil
	}
	return value, err == nil, err
}

func getWebFetchAuthorizationTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.WebFetchAuthorization, error) {
	return getWebFetchAuthorizationRow(queryer.QueryRowContext(ctx,
		webFetchAuthorizationSelect+` WHERE id = ?`, strings.TrimSpace(id)))
}

const webFetchAuthorizationSelect = `SELECT id, approval_id, thread_id, run_id,
	mission_id, session_id, workspace_id, supervisor_turn, supervisor_tool_call_id,
	canonical_url, exact_target, request_fingerprint, authorization_scope, status,
	requested_by, reviewed_by, version, created_at, updated_at, decided_at
	FROM web_fetch_authorizations`

func getWebFetchAuthorizationRow(row scanner) (domain.WebFetchAuthorization, error) {
	var value domain.WebFetchAuthorization
	var createdAt, updatedAt string
	var decidedAt sql.NullString
	if err := row.Scan(&value.ID, &value.ApprovalID, &value.ThreadID, &value.RunID,
		&value.MissionID, &value.SessionID, &value.WorkspaceID,
		&value.SupervisorTurn, &value.SupervisorToolCallID, &value.CanonicalURL,
		&value.ExactTarget, &value.RequestFingerprint, &value.Scope, &value.Status,
		&value.RequestedBy, &value.ReviewedBy, &value.Version, &createdAt, &updatedAt,
		&decidedAt); err != nil {
		return domain.WebFetchAuthorization{}, err
	}
	value.CreatedAt, value.UpdatedAt = parseTS(createdAt), parseTS(updatedAt)
	if decidedAt.Valid {
		parsed := parseTS(decidedAt.String)
		value.DecidedAt = &parsed
	}
	if err := value.Validate(); err != nil {
		return domain.WebFetchAuthorization{}, fmt.Errorf(
			"stored web fetch authorization is invalid: %w", err)
	}
	return value, nil
}
