package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
)

const specialistOperatorScheduleRequestSelect = `SELECT id, application_id, proposal_id,
	run_id, root_agent_id, max_rounds, agent_count, policy_fingerprint,
	requested_by, created_at FROM specialist_operator_schedule_requests`

func (s *SQLiteStore) CreateSpecialistOperatorScheduleRequest(ctx context.Context,
	request domain.SpecialistOperatorScheduleRequest,
	operation domain.SpecialistOperatorScheduleOperation,
	checks []domain.SpecialistDelegationPolicyCheck,
) (domain.SpecialistOperatorScheduleRequest, bool, error) {
	request = normalizeSpecialistOperatorScheduleRequest(request)
	operation = normalizeSpecialistOperatorScheduleOperation(operation)
	checks = normalizeSpecialistDelegationPolicyChecks(checks)
	if err := validateSpecialistOperatorScheduleMutation(request, operation); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireStructuredMutationWriteLockTx(ctx, tx, request.RunID); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	existingOperation, found, err := getSpecialistOperatorScheduleOperation(ctx, tx,
		operation.KeyDigest)
	if err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	if found {
		if err := validateSpecialistOperatorScheduleReplay(existingOperation,
			operation); err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, false, err
		}
		stored, err := getSpecialistOperatorScheduleRequest(ctx, tx,
			existingOperation.RequestID)
		if err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, false, err
		}
		return stored, true, nil
	}
	if err := validateSpecialistOperatorSchedulePolicyChecks(request, checks); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	run, err := requireSpecialistOperatorScheduleBindingTx(ctx, tx, request)
	if err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_operator_schedule_requests
		(id, application_id, proposal_id, run_id, root_agent_id, max_rounds,
		agent_count, policy_fingerprint, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.ID, request.ApplicationID,
		request.ProposalID, request.RunID, request.RootAgentID, request.MaxRounds,
		len(request.AgentIDs), request.PolicyFingerprint, request.RequestedBy,
		ts(request.CreatedAt)); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false,
			normalizeSpecialistOperatorScheduleError(err)
	}
	for index, agentID := range request.AgentIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_operator_schedule_request_agents
			(request_id, run_id, agent_id, ordinal) VALUES (?, ?, ?, ?)`, request.ID,
			request.RunID, agentID, index+1); err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, false,
				normalizeSpecialistOperatorScheduleError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_operator_schedule_operations
		(operation_key_digest, request_fingerprint, request_id, application_id,
		proposal_id, run_id, requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		operation.KeyDigest, operation.RequestFingerprint, request.ID,
		request.ApplicationID, request.ProposalID, request.RunID, request.RequestedBy,
		ts(operation.CreatedAt)); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false,
			normalizeSpecialistOperatorScheduleError(err)
	}
	for _, check := range checks {
		policyEvent, err := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent,
			"specialist_operator_schedule", request.ID, map[string]any{
				"operator_schedule_request_id": request.ID,
				"application_id":               request.ApplicationID, "proposal_id": request.ProposalID,
				"target_ordinal": check.Ordinal, "allowed": check.Allowed,
				"needs_approval":                    check.NeedsApproval,
				"approval_satisfied_by_application": check.NeedsApproval,
				"risk":                              check.Risk, "reason": check.Reason,
				"operator_authorized": true, "model_authorized": false,
			})
		if err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, false, err
		}
		policyEvent.CreatedAt = request.CreatedAt
		if _, err := insertRunEventTx(ctx, tx, policyEvent); err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, false, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.AgentOperatorScheduleRequestedEvent, "operator", request.ID,
		map[string]any{
			"operator_schedule_request_id": request.ID,
			"application_id":               request.ApplicationID, "proposal_id": request.ProposalID,
			"root_agent_id": request.RootAgentID, "agent_ids": request.AgentIDs,
			"child_count": len(request.AgentIDs), "max_rounds": request.MaxRounds,
			"requested_by":        request.RequestedBy,
			"operator_authorized": true, "model_authorized": false,
		}); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	return request, false, nil
}

func (s *SQLiteStore) GetSpecialistOperatorScheduleRequest(ctx context.Context,
	id string,
) (domain.SpecialistOperatorScheduleRequest, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.SpecialistOperatorScheduleRequest{}, apperror.New(
			apperror.CodeInvalidArgument,
			"specialist operator schedule request id is invalid")
	}
	return getSpecialistOperatorScheduleRequest(ctx, s.db, id)
}

func (s *SQLiteStore) GetSpecialistOperatorScheduleRequestByOperation(ctx context.Context,
	keyDigest string,
) (domain.SpecialistOperatorScheduleRequest, domain.SpecialistOperatorScheduleOperation,
	bool, error,
) {
	keyDigest = strings.TrimSpace(keyDigest)
	if len(keyDigest) != 64 {
		return domain.SpecialistOperatorScheduleRequest{},
			domain.SpecialistOperatorScheduleOperation{}, false,
			apperror.New(apperror.CodeInvalidArgument,
				"specialist operator schedule operation digest is invalid")
	}
	operation, found, err := getSpecialistOperatorScheduleOperation(ctx, s.db, keyDigest)
	if err != nil || !found {
		return domain.SpecialistOperatorScheduleRequest{}, operation, found, err
	}
	request, err := getSpecialistOperatorScheduleRequest(ctx, s.db,
		operation.RequestID)
	return request, operation, true, err
}

func (s *SQLiteStore) GetLatestSpecialistOperatorScheduleRequestByApplication(
	ctx context.Context, applicationID string,
) (domain.SpecialistOperatorScheduleRequest, bool, error) {
	applicationID = strings.TrimSpace(applicationID)
	if !domain.ValidAgentID(applicationID) || strings.ContainsRune(applicationID, 0) {
		return domain.SpecialistOperatorScheduleRequest{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"specialist delegation application id is invalid")
	}
	var requestID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM specialist_operator_schedule_requests
		WHERE application_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		applicationID).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistOperatorScheduleRequest{}, false, nil
	}
	if err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, false, err
	}
	request, err := getSpecialistOperatorScheduleRequest(ctx, s.db, requestID)
	return request, true, err
}

func (s *SQLiteStore) GetLatestSpecialistOperatorScheduleAttempt(ctx context.Context,
	requestID string,
) (domain.SpecialistSchedule, domain.SpecialistOperatorScheduleAttempt, bool, error) {
	requestID = strings.TrimSpace(requestID)
	if !domain.ValidAgentID(requestID) || strings.ContainsRune(requestID, 0) {
		return domain.SpecialistSchedule{}, domain.SpecialistOperatorScheduleAttempt{},
			false, apperror.New(apperror.CodeInvalidArgument,
				"specialist operator schedule request id is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SpecialistSchedule{}, domain.SpecialistOperatorScheduleAttempt{},
			false, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, found, err := getLatestSpecialistOperatorScheduleAttempt(ctx, tx, requestID)
	if err != nil || !found {
		return domain.SpecialistSchedule{}, attempt, found, err
	}
	schedule, err := getSpecialistScheduleTx(ctx, tx, attempt.ScheduleID)
	if err != nil {
		return domain.SpecialistSchedule{}, domain.SpecialistOperatorScheduleAttempt{},
			false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistSchedule{}, domain.SpecialistOperatorScheduleAttempt{},
			false, err
	}
	return schedule.Schedule, attempt, true, nil
}

func getSpecialistOperatorScheduleRequest(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.SpecialistOperatorScheduleRequest, error) {
	request, agentCount, err := scanSpecialistOperatorScheduleRequest(
		queryer.QueryRowContext(ctx, specialistOperatorScheduleRequestSelect+` WHERE id = ?`, id))
	if err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, err
	}
	rowsQueryer, ok := queryer.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return domain.SpecialistOperatorScheduleRequest{}, errors.New(
			"specialist operator schedule queryer cannot load Agents")
	}
	rows, err := rowsQueryer.QueryContext(ctx, `SELECT agent_id
		FROM specialist_operator_schedule_request_agents WHERE request_id = ?
		ORDER BY ordinal`, id)
	if err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return domain.SpecialistOperatorScheduleRequest{}, err
		}
		request.AgentIDs = append(request.AgentIDs, agentID)
	}
	if err := rows.Err(); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, err
	}
	if len(request.AgentIDs) != agentCount {
		return domain.SpecialistOperatorScheduleRequest{}, apperror.New(
			apperror.CodeConflict,
			"specialist operator schedule Agent count is inconsistent")
	}
	var operations int
	if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_operator_schedule_operations WHERE request_id = ?`, id).
		Scan(&operations); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, err
	}
	if operations != 1 {
		return domain.SpecialistOperatorScheduleRequest{}, apperror.New(
			apperror.CodeConflict,
			"specialist operator schedule operation binding is missing")
	}
	if err := request.Validate(); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"invalid persisted specialist operator schedule request", err)
	}
	return request, nil
}

func scanSpecialistOperatorScheduleRequest(row scanner,
) (domain.SpecialistOperatorScheduleRequest, int, error) {
	var request domain.SpecialistOperatorScheduleRequest
	var agentCount int
	var createdAt string
	if err := row.Scan(&request.ID, &request.ApplicationID, &request.ProposalID,
		&request.RunID, &request.RootAgentID, &request.MaxRounds, &agentCount,
		&request.PolicyFingerprint, &request.RequestedBy, &createdAt); err != nil {
		return domain.SpecialistOperatorScheduleRequest{}, 0, err
	}
	request.CreatedAt = parseTS(createdAt)
	return request, agentCount, nil
}

func getSpecialistOperatorScheduleOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.SpecialistOperatorScheduleOperation, bool, error) {
	var operation domain.SpecialistOperatorScheduleOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, request_id, application_id, proposal_id, run_id,
		requested_by, created_at FROM specialist_operator_schedule_operations
		WHERE operation_key_digest = ?`, keyDigest).Scan(&operation.KeyDigest,
		&operation.RequestFingerprint, &operation.RequestID,
		&operation.ApplicationID, &operation.ProposalID, &operation.RunID,
		&operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistOperatorScheduleOperation{}, false, nil
	}
	if err != nil {
		return domain.SpecialistOperatorScheduleOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

func getLatestSpecialistOperatorScheduleAttempt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, requestID string) (domain.SpecialistOperatorScheduleAttempt, bool, error) {
	var attempt domain.SpecialistOperatorScheduleAttempt
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT request_id, schedule_id, ordinal,
		created_at FROM specialist_operator_schedule_attempts WHERE request_id = ?
		ORDER BY ordinal DESC LIMIT 1`, requestID).Scan(&attempt.RequestID,
		&attempt.ScheduleID, &attempt.Ordinal, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistOperatorScheduleAttempt{}, false, nil
	}
	if err != nil {
		return domain.SpecialistOperatorScheduleAttempt{}, false, err
	}
	attempt.CreatedAt = parseTS(createdAt)
	return attempt, true, attempt.Validate()
}

func requireSpecialistOperatorScheduleBindingTx(ctx context.Context, tx *sql.Tx,
	request domain.SpecialistOperatorScheduleRequest,
) (domain.Run, error) {
	application, err := getSpecialistDelegationApplication(ctx, tx,
		request.ApplicationID)
	if err != nil {
		return domain.Run{}, err
	}
	if application.Status != domain.SpecialistDelegationApplied ||
		application.ProposalID != request.ProposalID || application.RunID != request.RunID ||
		application.RootAgentID != request.RootAgentID ||
		application.RequestedBy != request.RequestedBy || application.CompletedAt == nil ||
		request.CreatedAt.Before(*application.CompletedAt) {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"specialist operator schedule requires its applied delegation application")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, request.RunID))
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"specialist operator scheduling requires a running Run")
	}
	root, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		request.RootAgentID))
	if err != nil {
		return domain.Run{}, err
	}
	if root.RunID != run.ID || root.Role != domain.AgentRoleRoot ||
		root.Status != domain.AgentReady || root.ActiveAttemptID != "" {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"specialist operator scheduling requires the ready Run root")
	}
	var runningSchedules int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_schedules
		WHERE run_id = ? AND status = 'running'`, run.ID).Scan(&runningSchedules); err != nil {
		return domain.Run{}, err
	}
	if runningSchedules != 0 {
		return domain.Run{}, apperror.New(apperror.CodeConflict,
			"Run already has an active Specialist schedule")
	}
	assignmentAgents := make(map[string]struct{}, len(application.Assignments))
	for _, assignment := range application.Assignments {
		if assignment.Status == domain.SpecialistDelegationAssignmentInstructed {
			assignmentAgents[assignment.AgentID] = struct{}{}
		}
	}
	for _, agentID := range request.AgentIDs {
		if _, found := assignmentAgents[agentID]; !found {
			return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("Agent %s is not an instructed assignment of this application",
					agentID))
		}
		agent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
			agentID))
		if err != nil {
			return domain.Run{}, err
		}
		if agent.RunID != run.ID || agent.ParentID != root.ID ||
			agent.Role != domain.AgentRoleSpecialist || agent.Status != domain.AgentReady ||
			agent.ActiveAttemptID != "" {
			return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("Specialist %s is not ready for operator scheduling", agentID))
		}
		reserved, err := hasPendingSpecialistOperatorScheduleReservationTx(ctx, tx,
			run.ID, agentID, "")
		if err != nil {
			return domain.Run{}, err
		}
		if reserved {
			return domain.Run{}, apperror.New(apperror.CodeConflict,
				fmt.Sprintf("Specialist %s is reserved by another operator schedule", agentID))
		}
	}
	return run, nil
}

func hasPendingSpecialistOperatorScheduleReservationTx(ctx context.Context, tx *sql.Tx,
	runID, agentID, exceptRequestID string,
) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_operator_schedule_request_agents reserved
		WHERE reserved.run_id = ? AND reserved.agent_id = ?
			AND reserved.request_id != ?
			AND (
				NOT EXISTS (SELECT 1 FROM specialist_operator_schedule_attempts attempt
					WHERE attempt.request_id = reserved.request_id)
				OR COALESCE((SELECT schedule.status
					FROM specialist_operator_schedule_attempts attempt
					JOIN specialist_schedules schedule ON schedule.id = attempt.schedule_id
					WHERE attempt.request_id = reserved.request_id
					ORDER BY attempt.ordinal DESC LIMIT 1), '') IN ('running', 'abandoned')
			)`, runID, agentID, exceptRequestID).Scan(&count)
	return count != 0, err
}

func validateSpecialistOperatorSchedulePolicyChecks(
	request domain.SpecialistOperatorScheduleRequest,
	checks []domain.SpecialistDelegationPolicyCheck,
) error {
	if len(checks) != len(request.AgentIDs) {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist operator schedule Policy checks do not match its targets")
	}
	for index, check := range checks {
		if err := check.Validate(); err != nil || check.Ordinal != index+1 || !check.Allowed {
			return apperror.New(apperror.CodePolicyDenied,
				"specialist operator schedule Policy check was denied or invalid")
		}
	}
	fingerprint, err := domain.SpecialistDelegationPolicyFingerprint(checks)
	if err != nil {
		return err
	}
	if fingerprint != request.PolicyFingerprint {
		return apperror.New(apperror.CodeConflict,
			"specialist operator schedule Policy fingerprint is inconsistent")
	}
	return nil
}

func normalizeSpecialistOperatorScheduleRequest(
	request domain.SpecialistOperatorScheduleRequest,
) domain.SpecialistOperatorScheduleRequest {
	request.ID = strings.TrimSpace(request.ID)
	request.ApplicationID = strings.TrimSpace(request.ApplicationID)
	request.ProposalID = strings.TrimSpace(request.ProposalID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.RootAgentID = strings.TrimSpace(request.RootAgentID)
	request.PolicyFingerprint = strings.TrimSpace(request.PolicyFingerprint)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	request.AgentIDs = slices.Clone(request.AgentIDs)
	for index := range request.AgentIDs {
		request.AgentIDs[index] = strings.TrimSpace(request.AgentIDs[index])
	}
	slices.Sort(request.AgentIDs)
	request.CreatedAt = request.CreatedAt.UTC()
	return request
}

func normalizeSpecialistOperatorScheduleOperation(
	operation domain.SpecialistOperatorScheduleOperation,
) domain.SpecialistOperatorScheduleOperation {
	operation.KeyDigest = strings.TrimSpace(operation.KeyDigest)
	operation.RequestFingerprint = strings.TrimSpace(operation.RequestFingerprint)
	operation.RequestID = strings.TrimSpace(operation.RequestID)
	operation.ApplicationID = strings.TrimSpace(operation.ApplicationID)
	operation.ProposalID = strings.TrimSpace(operation.ProposalID)
	operation.RunID = strings.TrimSpace(operation.RunID)
	operation.RequestedBy = strings.TrimSpace(redact.String(operation.RequestedBy))
	operation.CreatedAt = operation.CreatedAt.UTC()
	return operation
}

func validateSpecialistOperatorScheduleMutation(
	request domain.SpecialistOperatorScheduleRequest,
	operation domain.SpecialistOperatorScheduleOperation,
) error {
	if err := request.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist operator schedule request is invalid", err)
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist operator schedule operation is invalid", err)
	}
	if operation.RequestID != request.ID ||
		operation.ApplicationID != request.ApplicationID ||
		operation.ProposalID != request.ProposalID || operation.RunID != request.RunID ||
		operation.RequestedBy != request.RequestedBy ||
		!operation.CreatedAt.Equal(request.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist operator schedule operation does not match its request")
	}
	return nil
}

func validateSpecialistOperatorScheduleReplay(existing,
	request domain.SpecialistOperatorScheduleOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.ApplicationID != request.ApplicationID ||
		existing.ProposalID != request.ProposalID || existing.RunID != request.RunID ||
		existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"specialist operator schedule operation key was already used for different intent")
	}
	return nil
}

func normalizeSpecialistOperatorScheduleError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint failed") {
		return apperror.Wrap(apperror.CodeConflict,
			"specialist operator schedule fact already exists", err)
	}
	if strings.Contains(message, "binding is invalid") {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"specialist operator schedule binding was rejected", err)
	}
	return err
}
