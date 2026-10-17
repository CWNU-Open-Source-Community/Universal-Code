package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const specialistDelegationApplicationSelect = `SELECT id, review_id, proposal_id, run_id,
	root_agent_id, status, assignment_count, policy_fingerprint, max_children,
	max_turns_per_child, max_tokens_per_child, requested_by, stop_code, version,
	created_at, updated_at, completed_at FROM specialist_delegation_applications`

const specialistDelegationApplicationAssignmentSelect = `SELECT application_id, proposal_id,
	ordinal, status, admission_operation_digest, instruction_operation_digest,
	agent_id, message_id, version, created_at, updated_at
	FROM specialist_delegation_application_assignments`

func (s *SQLiteStore) BeginSpecialistDelegationApplication(ctx context.Context,
	application domain.SpecialistDelegationApplication,
	operation domain.SpecialistDelegationApplicationOperation,
	checks []domain.SpecialistDelegationPolicyCheck,
) (domain.SpecialistDelegationApplication, bool, error) {
	application = normalizeSpecialistDelegationApplication(application)
	operation = normalizeSpecialistDelegationApplicationOperation(operation)
	checks = normalizeSpecialistDelegationPolicyChecks(checks)
	if err := validateSpecialistDelegationApplicationMutation(application, operation); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireStructuredMutationWriteLockTx(ctx, tx, application.RunID); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	existingOperation, found, err := getSpecialistDelegationApplicationOperation(ctx, tx,
		operation.KeyDigest)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if found {
		if err := validateSpecialistDelegationApplicationReplay(existingOperation, operation); err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
		stored, err := getSpecialistDelegationApplication(ctx, tx,
			existingOperation.ApplicationID)
		if err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
		return stored, true, nil
	}
	if existing, exists, err := getSpecialistDelegationApplicationByProposal(ctx, tx,
		application.ProposalID); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	} else if exists {
		return domain.SpecialistDelegationApplication{}, false, apperror.New(
			apperror.CodeConflict, fmt.Sprintf("specialist delegation proposal already has a %s application",
				existing.Status))
	}
	for _, assignment := range application.Assignments {
		if assignment.Status != domain.SpecialistDelegationAssignmentPending ||
			!assignment.CreatedAt.Equal(application.CreatedAt) ||
			!assignment.UpdatedAt.Equal(application.CreatedAt) {
			return domain.SpecialistDelegationApplication{}, false, apperror.New(
				apperror.CodeInvalidArgument,
				"new specialist delegation application assignments must start pending")
		}
	}
	if err := validateSpecialistDelegationPolicyChecks(application, checks); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	run, err := requireSpecialistDelegationApplicationBindingTx(ctx, tx, application)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_delegation_applications
		(id, review_id, proposal_id, run_id, root_agent_id, status, assignment_count,
		policy_fingerprint, max_children, max_turns_per_child, max_tokens_per_child,
		requested_by, stop_code, version, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, NULL)`,
		application.ID, application.ReviewID, application.ProposalID, application.RunID,
		application.RootAgentID, application.Status, application.AssignmentCount,
		application.PolicyFingerprint, application.MaxChildren,
		application.MaxTurnsPerChild, application.MaxTokensPerChild,
		application.RequestedBy, application.Version, ts(application.CreatedAt),
		ts(application.UpdatedAt)); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	for _, assignment := range application.Assignments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_delegation_application_assignments
			(application_id, proposal_id, ordinal, status, admission_operation_digest,
			instruction_operation_digest, agent_id, message_id, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?)`, assignment.ApplicationID,
			assignment.ProposalID, assignment.Ordinal, assignment.Status,
			assignment.AdmissionOperationDigest, assignment.InstructionOperationDigest,
			assignment.Version, ts(assignment.CreatedAt), ts(assignment.UpdatedAt)); err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_delegation_application_operations
		(operation_key_digest, request_fingerprint, application_id, review_id, proposal_id,
		run_id, requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		operation.KeyDigest, operation.RequestFingerprint, application.ID, application.ReviewID,
		application.ProposalID, application.RunID, application.RequestedBy,
		ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverSpecialistDelegationApplication(ctx, operation, err)
	}
	for _, check := range checks {
		policyEvent, err := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent,
			"delegation_application", application.ID, map[string]any{
				"application_id": application.ID, "proposal_id": application.ProposalID,
				"review_id": application.ReviewID, "assignment_ordinal": check.Ordinal,
				"allowed": check.Allowed, "needs_approval": check.NeedsApproval,
				"approval_satisfied_by_review": check.NeedsApproval,
				"risk":                         check.Risk, "reason": check.Reason,
				"admission_authorized": false,
			})
		if err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
		policyEvent.CreatedAt = application.CreatedAt
		if _, err := insertRunEventTx(ctx, tx, policyEvent); err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.AgentDelegationApplicationStartedEvent, "delegation_application", application.ID,
		map[string]any{
			"application_id": application.ID, "proposal_id": application.ProposalID,
			"review_id": application.ReviewID, "root_agent_id": application.RootAgentID,
			"assignment_count":     application.AssignmentCount,
			"admission_authorized": true, "scheduling_started": false,
		}); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	return application, false, nil
}

func (s *SQLiteStore) GetSpecialistDelegationApplication(ctx context.Context,
	id string,
) (domain.SpecialistDelegationApplication, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.SpecialistDelegationApplication{}, apperror.New(
			apperror.CodeInvalidArgument, "specialist delegation application id is invalid")
	}
	return getSpecialistDelegationApplication(ctx, s.db, id)
}

func (s *SQLiteStore) GetSpecialistDelegationApplicationByProposal(ctx context.Context,
	proposalID string,
) (domain.SpecialistDelegationApplication, bool, error) {
	proposalID = strings.TrimSpace(proposalID)
	if !domain.ValidAgentID(proposalID) || strings.ContainsRune(proposalID, 0) {
		return domain.SpecialistDelegationApplication{}, false, apperror.New(
			apperror.CodeInvalidArgument, "specialist delegation proposal id is invalid")
	}
	return getSpecialistDelegationApplicationByProposal(ctx, s.db, proposalID)
}

func (s *SQLiteStore) MarkSpecialistDelegationAssignmentAdmitted(ctx context.Context,
	applicationID string, ordinal int, agentID string,
) (domain.SpecialistDelegationApplicationAssignment, bool, error) {
	return s.transitionSpecialistDelegationApplicationAssignment(ctx, applicationID, ordinal,
		agentID, "", domain.SpecialistDelegationAssignmentAdmitted)
}

func (s *SQLiteStore) MarkSpecialistDelegationAssignmentInstructed(ctx context.Context,
	applicationID string, ordinal int, agentID string, messageID string,
) (domain.SpecialistDelegationApplicationAssignment, bool, error) {
	return s.transitionSpecialistDelegationApplicationAssignment(ctx, applicationID, ordinal,
		agentID, messageID, domain.SpecialistDelegationAssignmentInstructed)
}

func (s *SQLiteStore) transitionSpecialistDelegationApplicationAssignment(ctx context.Context,
	applicationID string, ordinal int, agentID string, messageID string,
	target domain.SpecialistDelegationAssignmentApplicationStatus,
) (domain.SpecialistDelegationApplicationAssignment, bool, error) {
	applicationID = strings.TrimSpace(applicationID)
	agentID = strings.TrimSpace(agentID)
	messageID = strings.TrimSpace(messageID)
	if !domain.ValidAgentID(applicationID) || !domain.ValidAgentID(agentID) ||
		ordinal <= 0 || ordinal > domain.MaxSpecialistDelegationAssignments ||
		(target == domain.SpecialistDelegationAssignmentInstructed && !domain.ValidAgentID(messageID)) {
		return domain.SpecialistDelegationApplicationAssignment{}, false, apperror.New(
			apperror.CodeInvalidArgument, "specialist delegation assignment transition scope is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	application, err := getSpecialistDelegationApplication(ctx, tx, applicationID)
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	assignment, err := getSpecialistDelegationApplicationAssignment(ctx, tx,
		applicationID, ordinal)
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	if assignment.Status == target ||
		(target == domain.SpecialistDelegationAssignmentAdmitted &&
			assignment.Status == domain.SpecialistDelegationAssignmentInstructed) {
		if assignment.AgentID != agentID || (messageID != "" && assignment.MessageID != messageID) {
			return domain.SpecialistDelegationApplicationAssignment{}, false, apperror.New(
				apperror.CodeConflict, "specialist delegation assignment result changed during replay")
		}
		if err := tx.Commit(); err != nil {
			return domain.SpecialistDelegationApplicationAssignment{}, false, err
		}
		return assignment, true, nil
	}
	if application.Status != domain.SpecialistDelegationApplying {
		return domain.SpecialistDelegationApplicationAssignment{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			fmt.Sprintf("specialist delegation application is %s", application.Status))
	}
	if target == domain.SpecialistDelegationAssignmentAdmitted &&
		assignment.Status != domain.SpecialistDelegationAssignmentPending {
		return domain.SpecialistDelegationApplicationAssignment{}, false, apperror.New(
			apperror.CodeConflict, "specialist delegation assignment is not pending")
	}
	if target == domain.SpecialistDelegationAssignmentInstructed &&
		(assignment.Status != domain.SpecialistDelegationAssignmentAdmitted ||
			assignment.AgentID != agentID) {
		return domain.SpecialistDelegationApplicationAssignment{}, false, apperror.New(
			apperror.CodeConflict, "specialist delegation assignment is not admitted by this application")
	}
	now := time.Now().UTC()
	if now.Before(assignment.UpdatedAt) {
		now = assignment.UpdatedAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE specialist_delegation_application_assignments
		SET status = ?, agent_id = ?, message_id = ?, version = version + 1, updated_at = ?
		WHERE application_id = ? AND ordinal = ? AND status = ? AND version = ?`, target,
		agentID, nullableString(messageID), ts(now), applicationID, ordinal,
		assignment.Status, assignment.Version)
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	if rows != 1 {
		return domain.SpecialistDelegationApplicationAssignment{}, false, apperror.New(
			apperror.CodeConflict, "specialist delegation assignment changed concurrently")
	}
	updated, err := getSpecialistDelegationApplicationAssignment(ctx, tx,
		applicationID, ordinal)
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, application.RunID)
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	eventType := events.AgentDelegationAssignmentAdmittedEvent
	payload := map[string]any{
		"application_id": application.ID, "proposal_id": application.ProposalID,
		"assignment_ordinal": ordinal, "agent_id": agentID,
		"instruction_delivered": false, "scheduling_started": false,
	}
	if target == domain.SpecialistDelegationAssignmentInstructed {
		eventType = events.AgentDelegationInstructionDeliveredEvent
		payload["message_id"] = messageID
		payload["instruction_delivered"] = true
	}
	if err := appendSupervisorEventTx(ctx, tx, run, eventType,
		"delegation_application", application.ID, payload); err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, false, err
	}
	return updated, false, nil
}

func (s *SQLiteStore) CompleteSpecialistDelegationApplication(ctx context.Context,
	applicationID string,
) (domain.SpecialistDelegationApplication, bool, error) {
	applicationID = strings.TrimSpace(applicationID)
	if !domain.ValidAgentID(applicationID) {
		return domain.SpecialistDelegationApplication{}, false, apperror.New(
			apperror.CodeInvalidArgument, "specialist delegation application id is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	application, err := getSpecialistDelegationApplication(ctx, tx, applicationID)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if application.Status == domain.SpecialistDelegationApplied {
		if err := tx.Commit(); err != nil {
			return domain.SpecialistDelegationApplication{}, false, err
		}
		return application, true, nil
	}
	if application.Status != domain.SpecialistDelegationApplying {
		return domain.SpecialistDelegationApplication{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			fmt.Sprintf("specialist delegation application is %s", application.Status))
	}
	for _, assignment := range application.Assignments {
		if assignment.Status != domain.SpecialistDelegationAssignmentInstructed {
			return domain.SpecialistDelegationApplication{}, false, apperror.New(
				apperror.CodeFailedPrecondition,
				"specialist delegation application still has incomplete assignments")
		}
	}
	now := time.Now().UTC()
	if now.Before(application.UpdatedAt) {
		now = application.UpdatedAt
	}
	for _, assignment := range application.Assignments {
		if now.Before(assignment.UpdatedAt) {
			now = assignment.UpdatedAt
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE specialist_delegation_applications
		SET status = 'applied', version = version + 1, updated_at = ?, completed_at = ?
		WHERE id = ? AND status = 'applying' AND version = ?`, ts(now), ts(now),
		application.ID, application.Version)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if rows != 1 {
		return domain.SpecialistDelegationApplication{}, false, apperror.New(
			apperror.CodeConflict, "specialist delegation application changed concurrently")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, application.RunID)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentDelegationAppliedEvent,
		"delegation_application", application.ID, map[string]any{
			"application_id": application.ID, "proposal_id": application.ProposalID,
			"review_id": application.ReviewID, "assignment_count": application.AssignmentCount,
			"instruction_count":  application.AssignmentCount,
			"scheduling_started": false,
		}); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	updated, err := getSpecialistDelegationApplication(ctx, tx, application.ID)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	return updated, false, nil
}

func normalizeSpecialistDelegationApplication(
	application domain.SpecialistDelegationApplication,
) domain.SpecialistDelegationApplication {
	application.ID = strings.TrimSpace(application.ID)
	application.ReviewID = strings.TrimSpace(application.ReviewID)
	application.ProposalID = strings.TrimSpace(application.ProposalID)
	application.RunID = strings.TrimSpace(application.RunID)
	application.RootAgentID = strings.TrimSpace(application.RootAgentID)
	application.Status = domain.SpecialistDelegationApplicationStatus(
		strings.TrimSpace(string(application.Status)))
	application.PolicyFingerprint = strings.TrimSpace(application.PolicyFingerprint)
	application.RequestedBy = strings.TrimSpace(redact.String(application.RequestedBy))
	application.StopCode = strings.TrimSpace(redact.String(application.StopCode))
	application.CreatedAt = application.CreatedAt.UTC()
	application.UpdatedAt = application.UpdatedAt.UTC()
	if application.CompletedAt != nil {
		completed := application.CompletedAt.UTC()
		application.CompletedAt = &completed
	}
	for index := range application.Assignments {
		assignment := &application.Assignments[index]
		assignment.ApplicationID = strings.TrimSpace(assignment.ApplicationID)
		assignment.ProposalID = strings.TrimSpace(assignment.ProposalID)
		assignment.Status = domain.SpecialistDelegationAssignmentApplicationStatus(
			strings.TrimSpace(string(assignment.Status)))
		assignment.AdmissionOperationDigest = strings.TrimSpace(assignment.AdmissionOperationDigest)
		assignment.InstructionOperationDigest = strings.TrimSpace(assignment.InstructionOperationDigest)
		assignment.AgentID = strings.TrimSpace(assignment.AgentID)
		assignment.MessageID = strings.TrimSpace(assignment.MessageID)
		assignment.CreatedAt = assignment.CreatedAt.UTC()
		assignment.UpdatedAt = assignment.UpdatedAt.UTC()
	}
	return application
}

func normalizeSpecialistDelegationApplicationOperation(
	operation domain.SpecialistDelegationApplicationOperation,
) domain.SpecialistDelegationApplicationOperation {
	operation.KeyDigest = strings.TrimSpace(operation.KeyDigest)
	operation.RequestFingerprint = strings.TrimSpace(operation.RequestFingerprint)
	operation.ApplicationID = strings.TrimSpace(operation.ApplicationID)
	operation.ReviewID = strings.TrimSpace(operation.ReviewID)
	operation.ProposalID = strings.TrimSpace(operation.ProposalID)
	operation.RunID = strings.TrimSpace(operation.RunID)
	operation.RequestedBy = strings.TrimSpace(redact.String(operation.RequestedBy))
	operation.CreatedAt = operation.CreatedAt.UTC()
	return operation
}

func normalizeSpecialistDelegationPolicyChecks(
	checks []domain.SpecialistDelegationPolicyCheck,
) []domain.SpecialistDelegationPolicyCheck {
	normalized := slices.Clone(checks)
	for index := range normalized {
		normalized[index].Risk = strings.TrimSpace(redact.String(normalized[index].Risk))
		normalized[index].Reason = strings.TrimSpace(redact.String(normalized[index].Reason))
	}
	return normalized
}

func validateSpecialistDelegationApplicationMutation(
	application domain.SpecialistDelegationApplication,
	operation domain.SpecialistDelegationApplicationOperation,
) error {
	if err := application.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist delegation application is invalid", err)
	}
	if application.Status != domain.SpecialistDelegationApplying {
		return apperror.New(apperror.CodeInvalidArgument,
			"new specialist delegation application must be applying")
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist delegation application operation is invalid", err)
	}
	if operation.ApplicationID != application.ID || operation.ReviewID != application.ReviewID ||
		operation.ProposalID != application.ProposalID || operation.RunID != application.RunID ||
		operation.RequestedBy != application.RequestedBy ||
		!operation.CreatedAt.Equal(application.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation application operation does not match its application")
	}
	expectedFingerprint := runmutation.Fingerprint(
		"specialist_delegation_application_request.v1", application.ReviewID,
		application.ProposalID, application.RunID, application.RequestedBy)
	if operation.RequestFingerprint != expectedFingerprint {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation application request fingerprint is invalid")
	}
	for _, assignment := range application.Assignments {
		admissionKey, err := domain.SpecialistDelegationAdmissionOperationKey(
			application.ID, assignment.Ordinal)
		if err != nil {
			return err
		}
		instructionKey, err := domain.SpecialistDelegationInstructionOperationKey(
			application.ID, assignment.Ordinal)
		if err != nil {
			return err
		}
		expectedAdmissionDigest := runmutation.Fingerprint("agent_admission_operation.v1",
			application.RunID, admissionKey)
		expectedInstructionDigest := runmutation.Fingerprint("agent_message_operation.v1",
			application.RunID, instructionKey)
		if assignment.AdmissionOperationDigest != expectedAdmissionDigest ||
			assignment.InstructionOperationDigest != expectedInstructionDigest {
			return apperror.New(apperror.CodeInvalidArgument,
				"specialist delegation application assignment operation digest is invalid")
		}
	}
	return nil
}

func validateSpecialistDelegationPolicyChecks(
	application domain.SpecialistDelegationApplication,
	checks []domain.SpecialistDelegationPolicyCheck,
) error {
	if len(checks) != application.AssignmentCount {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation application policy check count is invalid")
	}
	for _, check := range checks {
		if err := check.Validate(); err != nil {
			return apperror.Wrap(apperror.CodeInvalidArgument,
				"specialist delegation application policy check is invalid", err)
		}
		if !check.Allowed {
			return apperror.New(apperror.CodePolicyDenied,
				"specialist delegation application was denied by Policy")
		}
	}
	fingerprint, err := domain.SpecialistDelegationPolicyFingerprint(checks)
	if err != nil {
		return err
	}
	if fingerprint != application.PolicyFingerprint {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation application policy fingerprint is invalid")
	}
	return nil
}

func requireSpecialistDelegationApplicationBindingTx(ctx context.Context, tx *sql.Tx,
	application domain.SpecialistDelegationApplication,
) (domain.Run, error) {
	proposal, err := getSpecialistDelegationProposalTx(ctx, tx, application.ProposalID)
	if err != nil {
		return domain.Run{}, err
	}
	review, found, err := getSpecialistDelegationReviewByProposal(ctx, tx, proposal.ID)
	if err != nil {
		return domain.Run{}, err
	}
	if !found || review.ID != application.ReviewID ||
		review.Decision != domain.SpecialistDelegationApproved ||
		review.RunID != application.RunID || review.RootAgentID != application.RootAgentID ||
		review.ReviewedBy != application.RequestedBy || application.CreatedAt.Before(review.CreatedAt) {
		return domain.Run{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application requires its exact approved review")
	}
	var reviewOperationCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_review_operations
		WHERE review_id = ? AND proposal_id = ? AND run_id = ?`, review.ID,
		proposal.ID, proposal.RunID).Scan(&reviewOperationCount); err != nil {
		return domain.Run{}, err
	}
	if reviewOperationCount != 1 || proposal.RunID != application.RunID ||
		proposal.RootAgentID != application.RootAgentID ||
		len(proposal.Spec.Assignments) != application.AssignmentCount {
		return domain.Run{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application proposal binding is invalid")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, application.RunID)
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.Run{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application requires a running Run")
	}
	root, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		application.RootAgentID))
	if err != nil {
		return domain.Run{}, err
	}
	if root.RunID != run.ID || root.Role != domain.AgentRoleRoot || root.ParentID != "" ||
		root.Status != domain.AgentReady || root.ActiveAttemptID != "" {
		return domain.Run{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application requires the idle ready root Agent")
	}
	var activeChildWork int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM specialist_schedules WHERE run_id = ? AND status = 'running')
		+ (SELECT COUNT(*) FROM agent_attempts WHERE run_id = ? AND status = 'running')`,
		run.ID, run.ID).Scan(&activeChildWork); err != nil {
		return domain.Run{}, err
	}
	if activeChildWork != 0 {
		return domain.Run{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application requires idle child scheduling")
	}
	var rootSessionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id = ?`,
		root.SessionID).Scan(&rootSessionStatus); err != nil {
		return domain.Run{}, err
	}
	if rootSessionStatus != session.StatusActive {
		return domain.Run{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application requires an active root Session")
	}
	if err := validateSpecialistDelegationApplicationCapacityTx(ctx, tx, run, root,
		proposal.Spec, application); err != nil {
		return domain.Run{}, err
	}
	return run, nil
}

func validateSpecialistDelegationApplicationCapacityTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, root domain.AgentNode, spec domain.SpecialistDelegationSpec,
	application domain.SpecialistDelegationApplication,
) error {
	var childCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes
		WHERE run_id = ? AND parent_id = ? AND status NOT IN ('completed','failed','cancelled')`, run.ID, root.ID).Scan(&childCount); err != nil {
		return err
	}
	if childCount+len(spec.Assignments) > application.MaxChildren ||
		childCount+len(spec.Assignments) > domain.MaxAgentChildren {
		return apperror.New(apperror.CodeResourceExhausted,
			"specialist delegation application exceeds child capacity")
	}
	if root.ChildLimit != 0 && root.ChildLimit != application.MaxChildren {
		return apperror.New(apperror.CodeConflict,
			"root Agent already uses a different child capacity policy")
	}
	var historyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes WHERE run_id=?`, run.ID).Scan(&historyCount); err != nil {
		return err
	}
	if historyCount+len(spec.Assignments) > domain.MaxAgentHistoryNodesPerRun {
		return apperror.New(apperror.CodeResourceExhausted, "specialist delegation exceeds worker history bound")
	}
	allowedSkills := make(map[string]struct{}, len(root.Skills))
	for _, skill := range root.Skills {
		allowedSkills[skill] = struct{}{}
	}
	proposedTurns := int64(0)
	proposedTokens := int64(0)
	for _, assignment := range spec.Assignments {
		if assignment.TurnLimit > application.MaxTurnsPerChild ||
			assignment.TokenLimit > application.MaxTokensPerChild {
			return apperror.New(apperror.CodeInvalidArgument,
				"specialist delegation assignment exceeds application policy")
		}
		for _, skill := range assignment.Skills {
			if !domain.DelegableAgentSkill(skill) {
				return apperror.New(apperror.CodeInvalidArgument,
					"specialist delegation application includes a non-delegable capability")
			}
			if _, allowed := allowedSkills[skill]; !allowed {
				return apperror.New(apperror.CodeInvalidArgument,
					"specialist delegation application skills exceed root capabilities")
			}
		}
		proposedTurns += assignment.TurnLimit
		proposedTokens += assignment.TokenLimit
	}
	reservedTurns, reservedTokens, err := specialistReservationsTx(ctx, tx, run.ID, root.ID)
	if err != nil {
		return err
	}
	if int64(run.Budget.MaxTurns)-root.TurnsUsed-reservedTurns-proposedTurns < 1 {
		return apperror.New(apperror.CodeResourceExhausted,
			"specialist delegation application must leave one root turn available")
	}
	if run.Budget.MaxTokens > 0 &&
		run.Budget.MaxTokens-root.TokensUsed-reservedTokens-proposedTokens < 1 {
		return apperror.New(apperror.CodeResourceExhausted,
			"specialist delegation application must leave root token capacity available")
	}
	return nil
}

func validateSpecialistDelegationApplicationReplay(existing,
	request domain.SpecialistDelegationApplicationOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.ReviewID != request.ReviewID || existing.ProposalID != request.ProposalID ||
		existing.RunID != request.RunID || existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"specialist delegation application operation key was already used for different intent")
	}
	return nil
}

func (s *SQLiteStore) recoverSpecialistDelegationApplication(ctx context.Context,
	operation domain.SpecialistDelegationApplicationOperation, original error,
) (domain.SpecialistDelegationApplication, bool, error) {
	existing, found, err := getSpecialistDelegationApplicationOperation(ctx, s.db,
		operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.SpecialistDelegationApplication{}, false, original
		}
		return domain.SpecialistDelegationApplication{}, false, errors.Join(original, err)
	}
	if err := validateSpecialistDelegationApplicationReplay(existing, operation); err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	application, err := s.GetSpecialistDelegationApplication(ctx, existing.ApplicationID)
	return application, true, err
}

func getSpecialistDelegationApplicationOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.SpecialistDelegationApplicationOperation, bool, error) {
	var operation domain.SpecialistDelegationApplicationOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest, request_fingerprint,
		application_id, review_id, proposal_id, run_id, requested_by, created_at
		FROM specialist_delegation_application_operations WHERE operation_key_digest = ?`,
		keyDigest).Scan(&operation.KeyDigest, &operation.RequestFingerprint,
		&operation.ApplicationID, &operation.ReviewID, &operation.ProposalID,
		&operation.RunID, &operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistDelegationApplicationOperation{}, false, nil
	}
	if err != nil {
		return domain.SpecialistDelegationApplicationOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

func getSpecialistDelegationApplication(ctx context.Context,
	queryer specialistDelegationQueryer, id string,
) (domain.SpecialistDelegationApplication, error) {
	application, err := scanSpecialistDelegationApplication(queryer.QueryRowContext(ctx,
		specialistDelegationApplicationSelect+` WHERE id = ?`, id))
	if err != nil {
		return domain.SpecialistDelegationApplication{}, err
	}
	assignments, err := listSpecialistDelegationApplicationAssignments(ctx, queryer, id)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, err
	}
	application.Assignments = assignments
	return application, application.Validate()
}

func getSpecialistDelegationApplicationByProposal(ctx context.Context,
	queryer specialistDelegationQueryer, proposalID string,
) (domain.SpecialistDelegationApplication, bool, error) {
	application, err := scanSpecialistDelegationApplication(queryer.QueryRowContext(ctx,
		specialistDelegationApplicationSelect+` WHERE proposal_id = ?`, proposalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistDelegationApplication{}, false, nil
	}
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	assignments, err := listSpecialistDelegationApplicationAssignments(ctx, queryer,
		application.ID)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, false, err
	}
	application.Assignments = assignments
	return application, true, application.Validate()
}

func scanSpecialistDelegationApplication(row scanner,
) (domain.SpecialistDelegationApplication, error) {
	var application domain.SpecialistDelegationApplication
	var status string
	var createdAt, updatedAt string
	var completedAt sql.NullString
	err := row.Scan(&application.ID, &application.ReviewID, &application.ProposalID,
		&application.RunID, &application.RootAgentID, &status, &application.AssignmentCount,
		&application.PolicyFingerprint, &application.MaxChildren,
		&application.MaxTurnsPerChild, &application.MaxTokensPerChild,
		&application.RequestedBy, &application.StopCode, &application.Version,
		&createdAt, &updatedAt, &completedAt)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, err
	}
	application.Status = domain.SpecialistDelegationApplicationStatus(status)
	application.CreatedAt = parseTS(createdAt)
	application.UpdatedAt = parseTS(updatedAt)
	application.CompletedAt = parseNullableTS(completedAt)
	return application, nil
}

func listSpecialistDelegationApplicationAssignments(ctx context.Context,
	queryer specialistDelegationQueryer, applicationID string,
) ([]domain.SpecialistDelegationApplicationAssignment, error) {
	rows, err := queryer.QueryContext(ctx, specialistDelegationApplicationAssignmentSelect+
		` WHERE application_id = ? ORDER BY ordinal`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make([]domain.SpecialistDelegationApplicationAssignment, 0,
		domain.MaxSpecialistDelegationAssignments)
	for rows.Next() {
		assignment, err := scanSpecialistDelegationApplicationAssignment(rows)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	return assignments, rows.Err()
}

func getSpecialistDelegationApplicationAssignment(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, applicationID string, ordinal int,
) (domain.SpecialistDelegationApplicationAssignment, error) {
	return scanSpecialistDelegationApplicationAssignment(queryer.QueryRowContext(ctx,
		specialistDelegationApplicationAssignmentSelect+
			` WHERE application_id = ? AND ordinal = ?`, applicationID, ordinal))
}

func scanSpecialistDelegationApplicationAssignment(row scanner,
) (domain.SpecialistDelegationApplicationAssignment, error) {
	var assignment domain.SpecialistDelegationApplicationAssignment
	var status string
	var agentID, messageID sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&assignment.ApplicationID, &assignment.ProposalID, &assignment.Ordinal,
		&status, &assignment.AdmissionOperationDigest, &assignment.InstructionOperationDigest,
		&agentID, &messageID, &assignment.Version, &createdAt, &updatedAt)
	if err != nil {
		return domain.SpecialistDelegationApplicationAssignment{}, err
	}
	assignment.Status = domain.SpecialistDelegationAssignmentApplicationStatus(status)
	assignment.AgentID = agentID.String
	assignment.MessageID = messageID.String
	assignment.CreatedAt = parseTS(createdAt)
	assignment.UpdatedAt = parseTS(updatedAt)
	return assignment, assignment.Validate()
}

func requireSpecialistDelegationApplicationAdmissionTx(ctx context.Context, tx *sql.Tx,
	runID string, operationDigest string,
) error {
	var applying, matching int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_delegation_applications
		WHERE run_id = ? AND status = 'applying'`, runID).Scan(&applying); err != nil {
		return err
	}
	if applying == 0 {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_application_assignments assignment
		JOIN specialist_delegation_applications application
			ON application.id = assignment.application_id
		WHERE application.run_id = ? AND application.status = 'applying'
			AND assignment.admission_operation_digest = ?
			AND assignment.status IN ('pending', 'admitted')`, runID,
		operationDigest).Scan(&matching); err != nil {
		return err
	}
	if matching != 1 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"specialist admission is reserved by an active delegation application")
	}
	return nil
}

func requireSpecialistDelegationApplicationMessageTx(ctx context.Context, tx *sql.Tx,
	runID string, operationDigest string, recipientID string,
) error {
	var applying, matching int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_delegation_applications
		WHERE run_id = ? AND status = 'applying'`, runID).Scan(&applying); err != nil {
		return err
	}
	if applying == 0 {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_application_assignments assignment
		JOIN specialist_delegation_applications application
			ON application.id = assignment.application_id
		WHERE application.run_id = ? AND application.status = 'applying'
			AND assignment.instruction_operation_digest = ?
			AND assignment.agent_id = ? AND assignment.status IN ('admitted', 'instructed')`,
		runID, operationDigest, recipientID).Scan(&matching); err != nil {
		return err
	}
	if matching != 1 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"agent messages are reserved by an active delegation application")
	}
	return nil
}

func requireNoApplyingSpecialistDelegationApplicationTx(ctx context.Context, tx *sql.Tx,
	runID string,
) error {
	var applying int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_applications WHERE run_id = ? AND status = 'applying'`,
		runID).Scan(&applying); err != nil {
		return err
	}
	if applying != 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"specialist scheduling is reserved by an active delegation application")
	}
	return nil
}

func abortSpecialistDelegationApplicationsTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, at time.Time,
) error {
	if run.Status != domain.RunCompleted && run.Status != domain.RunFailed &&
		run.Status != domain.RunCancelled {
		return nil
	}
	rows, err := tx.QueryContext(ctx, specialistDelegationApplicationSelect+
		` WHERE run_id = ? AND status = 'applying' ORDER BY created_at, id`, run.ID)
	if err != nil {
		return err
	}
	applications := make([]domain.SpecialistDelegationApplication, 0, 1)
	for rows.Next() {
		application, err := scanSpecialistDelegationApplication(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		applications = append(applications, application)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for index := range applications {
		assignments, err := listSpecialistDelegationApplicationAssignments(ctx, tx,
			applications[index].ID)
		if err != nil {
			return err
		}
		applications[index].Assignments = assignments
		terminalAt := at.UTC()
		if terminalAt.Before(applications[index].UpdatedAt) {
			terminalAt = applications[index].UpdatedAt
		}
		for _, assignment := range assignments {
			if terminalAt.Before(assignment.UpdatedAt) {
				terminalAt = assignment.UpdatedAt
			}
		}
		stopCode := "run_" + string(run.Status)
		result, err := tx.ExecContext(ctx, `UPDATE specialist_delegation_applications
			SET status = 'aborted', stop_code = ?, version = version + 1,
			updated_at = ?, completed_at = ? WHERE id = ? AND status = 'applying' AND version = ?`,
			stopCode, ts(terminalAt), ts(terminalAt), applications[index].ID,
			applications[index].Version)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return apperror.New(apperror.CodeConflict,
				"specialist delegation application changed during Run termination")
		}
		admitted := 0
		instructed := 0
		for _, assignment := range assignments {
			if assignment.Status == domain.SpecialistDelegationAssignmentAdmitted ||
				assignment.Status == domain.SpecialistDelegationAssignmentInstructed {
				admitted++
			}
			if assignment.Status == domain.SpecialistDelegationAssignmentInstructed {
				instructed++
			}
		}
		if err := appendSupervisorEventTx(ctx, tx, run,
			events.AgentDelegationApplicationAbortedEvent, "delegation_application",
			applications[index].ID, map[string]any{
				"application_id": applications[index].ID,
				"proposal_id":    applications[index].ProposalID,
				"stop_code":      stopCode, "admitted_count": admitted,
				"instruction_count": instructed, "scheduling_started": false,
			}); err != nil {
			return err
		}
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
