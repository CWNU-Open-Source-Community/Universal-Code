package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
)

const specialistDelegationProposalSelect = `SELECT id, run_id, root_agent_id, session_id,
	workspace_id, protocol_version, status, assignment_count, requested_by, version, created_at
	FROM specialist_delegation_proposals`

type specialistDelegationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *SQLiteStore) CreateSpecialistDelegationProposal(ctx context.Context,
	operation domain.SpecialistDelegationOperation,
	proposal domain.SpecialistDelegationProposal,
	policyEvent events.Event, proposalEvent events.Event,
	toolEvent events.Event,
) (domain.SpecialistDelegationProposal, bool, error) {
	operation = normalizeSpecialistDelegationOperation(operation)
	proposal = normalizeSpecialistDelegationProposal(proposal)
	if err := validateSpecialistDelegationMutation(operation, proposal, policyEvent,
		proposalEvent, toolEvent); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireStructuredMutationWriteLockTx(ctx, tx, operation.RunID); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, operation.RunID, operation.LeaseID,
		operation.LeaseGeneration); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	existing, found, err := getSpecialistDelegationOperationTx(ctx, tx, operation.KeyDigest)
	if err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	if found {
		if err := validateSpecialistDelegationReplay(existing, operation); err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
		stored, err := getSpecialistDelegationProposalTx(ctx, tx, existing.ProposalID)
		if err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
		return stored, true, nil
	}
	run, mission, root, err := requireSpecialistDelegationBindingTx(ctx, tx, operation, proposal)
	if err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	if err := validateSpecialistDelegationCapacityTx(ctx, tx, run, root, proposal.Spec); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if event.RunID != run.ID || event.MissionID != mission.ID {
			return domain.SpecialistDelegationProposal{}, false, apperror.New(
				apperror.CodeInvalidArgument, "specialist delegation event scope does not match its Run")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_delegation_proposals
		(id, run_id, root_agent_id, session_id, workspace_id, protocol_version, status,
		assignment_count, requested_by, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, proposal.ID, proposal.RunID,
		proposal.RootAgentID, proposal.SessionID, proposal.WorkspaceID, proposal.Spec.Version,
		proposal.Status, len(proposal.Spec.Assignments), proposal.RequestedBy,
		proposal.Version, ts(proposal.CreatedAt)); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	for _, assignment := range proposal.Spec.Assignments {
		skillsJSON, err := json.Marshal(assignment.Skills)
		if err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
		pathsJSON, err := json.Marshal(assignment.AllowedReadPaths)
		if err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
		workerJSON, err := domain.WorkerProfileJSON(assignment.Worker)
		if err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_delegation_assignments
			(proposal_id, ordinal, title, goal, skills_json, turn_limit, token_limit,allowed_read_paths_json,tool_call_limit,cost_limit_micros,worker_profile_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, proposal.ID, assignment.Ordinal,
			assignment.Title, assignment.Goal, string(skillsJSON), assignment.TurnLimit,
			assignment.TokenLimit, string(pathsJSON), assignment.ToolCallLimit, assignment.CostLimitMicros, workerJSON); err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_delegation_operations
		(operation_key_digest, request_fingerprint, invocation_id, proposal_id, run_id,
		session_id, workspace_id, root_agent_id, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.InvocationID, proposal.ID, operation.RunID,
		operation.SessionID, operation.WorkspaceID, operation.RootAgentID,
		operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverSpecialistDelegationProposal(ctx, operation, err)
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return domain.SpecialistDelegationProposal{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	return proposal, false, nil
}

func (s *SQLiteStore) GetSpecialistDelegationProposal(ctx context.Context,
	id string,
) (domain.SpecialistDelegationProposal, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) {
		return domain.SpecialistDelegationProposal{}, apperror.New(
			apperror.CodeInvalidArgument, "specialist delegation proposal id is invalid")
	}
	return getSpecialistDelegationProposal(ctx, s.db, id)
}

func (s *SQLiteStore) ListSpecialistDelegationProposals(ctx context.Context,
	runID string, limit int,
) ([]domain.SpecialistDelegationProposal, error) {
	if limit <= 0 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation list limit must be between 1 and 100")
	}
	return s.ListSpecialistDelegationProposalsPage(ctx, runID, 0, limit)
}

func (s *SQLiteStore) ListSpecialistDelegationProposalsPage(ctx context.Context,
	runID string, offset int, limit int,
) ([]domain.SpecialistDelegationProposal, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation Run id is invalid")
	}
	if offset < 0 || limit <= 0 || limit > 101 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation page bounds are invalid")
	}
	rows, err := s.db.QueryContext(ctx, specialistDelegationProposalSelect+
		` WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		runID, limit, offset)
	if err != nil {
		return nil, err
	}
	proposals := make([]domain.SpecialistDelegationProposal, 0)
	counts := make([]int, 0)
	for rows.Next() {
		proposal, count, err := scanSpecialistDelegationProposalBase(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		proposals = append(proposals, proposal)
		counts = append(counts, count)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range proposals {
		assignments, err := listSpecialistDelegationAssignments(ctx, s.db,
			proposals[index].ID, counts[index])
		if err != nil {
			return nil, err
		}
		proposals[index].Spec.Assignments = assignments
		if err := proposals[index].Validate(); err != nil {
			return nil, err
		}
	}
	return proposals, nil
}

func normalizeSpecialistDelegationOperation(
	operation domain.SpecialistDelegationOperation,
) domain.SpecialistDelegationOperation {
	operation.KeyDigest = strings.TrimSpace(operation.KeyDigest)
	operation.RequestFingerprint = strings.TrimSpace(operation.RequestFingerprint)
	operation.InvocationID = strings.TrimSpace(operation.InvocationID)
	operation.ProposalID = strings.TrimSpace(operation.ProposalID)
	operation.RunID = strings.TrimSpace(operation.RunID)
	operation.SessionID = strings.TrimSpace(operation.SessionID)
	operation.WorkspaceID = strings.TrimSpace(operation.WorkspaceID)
	operation.RootAgentID = strings.TrimSpace(operation.RootAgentID)
	operation.LeaseID = strings.TrimSpace(operation.LeaseID)
	operation.RequestedBy = strings.TrimSpace(redact.String(operation.RequestedBy))
	operation.CreatedAt = operation.CreatedAt.UTC()
	return operation
}

func normalizeSpecialistDelegationProposal(
	proposal domain.SpecialistDelegationProposal,
) domain.SpecialistDelegationProposal {
	proposal.ID = strings.TrimSpace(proposal.ID)
	proposal.RunID = strings.TrimSpace(proposal.RunID)
	proposal.RootAgentID = strings.TrimSpace(proposal.RootAgentID)
	proposal.SessionID = strings.TrimSpace(proposal.SessionID)
	proposal.WorkspaceID = strings.TrimSpace(proposal.WorkspaceID)
	proposal.RequestedBy = strings.TrimSpace(redact.String(proposal.RequestedBy))
	for index := range proposal.Spec.Assignments {
		proposal.Spec.Assignments[index].Title = redact.String(
			strings.TrimSpace(proposal.Spec.Assignments[index].Title))
		proposal.Spec.Assignments[index].Goal = redact.String(
			strings.TrimSpace(proposal.Spec.Assignments[index].Goal))
	}
	proposal.Spec, _ = domain.NormalizeSpecialistDelegationSpec(proposal.Spec)
	proposal.CreatedAt = proposal.CreatedAt.UTC()
	return proposal
}

func validateSpecialistDelegationMutation(operation domain.SpecialistDelegationOperation,
	proposal domain.SpecialistDelegationProposal, policyEvent events.Event,
	proposalEvent events.Event, toolEvent events.Event,
) error {
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist delegation operation is invalid", err)
	}
	if err := proposal.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist delegation proposal is invalid", err)
	}
	if operation.ProposalID != proposal.ID || operation.RunID != proposal.RunID ||
		operation.RootAgentID != proposal.RootAgentID || operation.SessionID != proposal.SessionID ||
		operation.WorkspaceID != proposal.WorkspaceID || operation.RequestedBy != proposal.RequestedBy {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation operation does not match its proposal")
	}
	if policyEvent.Type != events.PolicyDecisionEvent ||
		policyEvent.SubjectID != operation.InvocationID ||
		proposalEvent.Type != events.AgentDelegationProposedEvent ||
		proposalEvent.SubjectID != proposal.ID ||
		toolEvent.Type != events.ToolCompletedEvent || toolEvent.SubjectID != operation.InvocationID {
		return apperror.New(apperror.CodeInvalidArgument,
			"specialist delegation events do not match the operation")
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if err := event.Validate(); err != nil {
			return apperror.Wrap(apperror.CodeInvalidArgument,
				"specialist delegation event is invalid", err)
		}
	}
	return nil
}

func requireSpecialistDelegationBindingTx(ctx context.Context, tx *sql.Tx,
	operation domain.SpecialistDelegationOperation,
	proposal domain.SpecialistDelegationProposal,
) (domain.Run, domain.Mission, domain.AgentNode, error) {
	run, mission, err := getCoordinatorRunTx(ctx, tx, operation.RunID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, err
	}
	if run.Status != domain.RunRunning || run.SessionID != operation.SessionID ||
		mission.WorkspaceID != operation.WorkspaceID {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation scope does not match its running Run")
	}
	root, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		operation.RootAgentID))
	if err != nil {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, err
	}
	if root.RunID != run.ID || root.Role != domain.AgentRoleRoot || root.ParentID != "" ||
		root.Status != domain.AgentRunning || root.ActiveAttemptID == "" ||
		proposal.RootAgentID != root.ID {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation requires the active root Agent")
	}
	if !slices.Contains(root.Skills, domain.AgentSkillSpecialistDelegation) {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"root Agent does not hold the Specialist delegation proposal capability")
	}
	var checkpointCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_checkpoints
		WHERE run_id = ? AND phase = 'turn_started' AND attempt_id = ?
			AND lease_id = ? AND lease_generation = ?`, run.ID, root.ActiveAttemptID,
		operation.LeaseID, operation.LeaseGeneration).Scan(&checkpointCount); err != nil {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, err
	}
	if checkpointCount != 1 {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation is not bound to the active root turn")
	}
	var invocationCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_tool_calls
		WHERE id = ? AND run_id = ? AND session_id = ? AND workspace_id = ?
			AND tool_name = 'specialist_delegation_propose' AND action_class = 'agent_proposal'`,
		operation.InvocationID, operation.RunID, operation.SessionID,
		operation.WorkspaceID).Scan(&invocationCount); err != nil {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, err
	}
	if invocationCount != 1 {
		return domain.Run{}, domain.Mission{}, domain.AgentNode{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation is not backed by the Run tool budget ledger")
	}
	return run, mission, root, nil
}

func validateSpecialistDelegationCapacityTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, root domain.AgentNode, spec domain.SpecialistDelegationSpec,
) error {
	var childCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes
		WHERE run_id = ? AND parent_id = ? AND status NOT IN ('completed','failed','cancelled')`, run.ID, root.ID).Scan(&childCount); err != nil {
		return err
	}
	if childCount+len(spec.Assignments) > domain.MaxAgentChildren {
		return apperror.New(apperror.CodeResourceExhausted,
			"specialist delegation exceeds the remaining child capacity")
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
	totalTurns := int64(0)
	totalTokens := int64(0)
	for _, assignment := range spec.Assignments {
		for _, skill := range assignment.Skills {
			if !domain.DelegableAgentSkill(skill) {
				return apperror.New(apperror.CodeInvalidArgument,
					"specialist delegation capability cannot be delegated")
			}
			if _, allowed := allowedSkills[skill]; !allowed {
				return apperror.New(apperror.CodeInvalidArgument,
					"specialist delegation skills must be a subset of root capabilities")
			}
		}
		if totalTurns > int64(^uint64(0)>>1)-assignment.TurnLimit ||
			totalTokens > domain.MaxAgentTokenReservation-assignment.TokenLimit {
			return apperror.New(apperror.CodeResourceExhausted,
				"specialist delegation aggregate budget overflowed")
		}
		totalTurns += assignment.TurnLimit
		totalTokens += assignment.TokenLimit
	}
	if totalTurns > root.TurnLimit-root.TurnsUsed-2 {
		return apperror.New(apperror.CodeResourceExhausted,
			"specialist delegation must leave the active turn and one future root turn available")
	}
	if root.TokenLimit > 0 && totalTokens >= root.TokenLimit-root.TokensUsed {
		return apperror.New(apperror.CodeResourceExhausted,
			"specialist delegation must leave root token capacity available")
	}
	return nil
}

func validateSpecialistDelegationReplay(existing,
	request domain.SpecialistDelegationOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.RunID != request.RunID || existing.SessionID != request.SessionID ||
		existing.WorkspaceID != request.WorkspaceID ||
		existing.RootAgentID != request.RootAgentID ||
		existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"specialist delegation idempotency key was already used for different intent")
	}
	return nil
}

func (s *SQLiteStore) recoverSpecialistDelegationProposal(ctx context.Context,
	operation domain.SpecialistDelegationOperation, original error,
) (domain.SpecialistDelegationProposal, bool, error) {
	existing, found, err := getSpecialistDelegationOperation(ctx, s.db, operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.SpecialistDelegationProposal{}, false, original
		}
		return domain.SpecialistDelegationProposal{}, false, errors.Join(original, err)
	}
	if err := validateSpecialistDelegationReplay(existing, operation); err != nil {
		return domain.SpecialistDelegationProposal{}, false, err
	}
	proposal, err := s.GetSpecialistDelegationProposal(ctx, existing.ProposalID)
	return proposal, true, err
}

func getSpecialistDelegationOperationTx(ctx context.Context, tx *sql.Tx,
	keyDigest string,
) (domain.SpecialistDelegationOperation, bool, error) {
	return getSpecialistDelegationOperation(ctx, tx, keyDigest)
}

func getSpecialistDelegationOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string,
) (domain.SpecialistDelegationOperation, bool, error) {
	var operation domain.SpecialistDelegationOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest, request_fingerprint,
		invocation_id, proposal_id, run_id, session_id, workspace_id, root_agent_id,
		requested_by, created_at
		FROM specialist_delegation_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&operation.KeyDigest, &operation.RequestFingerprint, &operation.InvocationID,
			&operation.ProposalID, &operation.RunID, &operation.SessionID,
			&operation.WorkspaceID, &operation.RootAgentID, &operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistDelegationOperation{}, false, nil
	}
	if err != nil {
		return domain.SpecialistDelegationOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.ValidatePersisted()
}

func getSpecialistDelegationProposalTx(ctx context.Context, tx *sql.Tx,
	id string,
) (domain.SpecialistDelegationProposal, error) {
	return getSpecialistDelegationProposal(ctx, tx, id)
}

func getSpecialistDelegationProposal(ctx context.Context, queryer specialistDelegationQueryer,
	id string,
) (domain.SpecialistDelegationProposal, error) {
	proposal, count, err := scanSpecialistDelegationProposalBase(queryer.QueryRowContext(ctx,
		specialistDelegationProposalSelect+` WHERE id = ?`, id))
	if err != nil {
		return domain.SpecialistDelegationProposal{}, err
	}
	assignments, err := listSpecialistDelegationAssignments(ctx, queryer, id, count)
	if err != nil {
		return domain.SpecialistDelegationProposal{}, err
	}
	proposal.Spec.Assignments = assignments
	return proposal, proposal.Validate()
}

func scanSpecialistDelegationProposalBase(row scanner,
) (domain.SpecialistDelegationProposal, int, error) {
	var proposal domain.SpecialistDelegationProposal
	var protocolVersion string
	var status string
	var count int
	var createdAt string
	err := row.Scan(&proposal.ID, &proposal.RunID, &proposal.RootAgentID,
		&proposal.SessionID, &proposal.WorkspaceID, &protocolVersion, &status, &count,
		&proposal.RequestedBy, &proposal.Version, &createdAt)
	if err != nil {
		return domain.SpecialistDelegationProposal{}, 0, err
	}
	proposal.Status = domain.SpecialistDelegationStatus(status)
	proposal.Spec.Version = protocolVersion
	proposal.CreatedAt = parseTS(createdAt)
	if count <= 0 || count > domain.MaxSpecialistDelegationAssignments {
		return domain.SpecialistDelegationProposal{}, 0,
			fmt.Errorf("invalid Specialist delegation assignment count %d", count)
	}
	return proposal, count, nil
}

func listSpecialistDelegationAssignments(ctx context.Context, queryer specialistDelegationQueryer,
	proposalID string, expected int,
) ([]domain.SpecialistDelegationAssignment, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT ordinal, title, goal, skills_json,
		turn_limit, token_limit,allowed_read_paths_json,tool_call_limit,cost_limit_micros,worker_profile_json FROM specialist_delegation_assignments
		WHERE proposal_id = ? ORDER BY ordinal`, proposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make([]domain.SpecialistDelegationAssignment, 0, expected)
	for rows.Next() {
		var assignment domain.SpecialistDelegationAssignment
		var skillsJSON string
		var pathsJSON, workerJSON string
		if err := rows.Scan(&assignment.Ordinal, &assignment.Title, &assignment.Goal,
			&skillsJSON, &assignment.TurnLimit, &assignment.TokenLimit, &pathsJSON, &assignment.ToolCallLimit, &assignment.CostLimitMicros, &workerJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(skillsJSON), &assignment.Skills); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(pathsJSON), &assignment.AllowedReadPaths); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(workerJSON), &assignment.Worker); err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(assignments) != expected {
		return nil, fmt.Errorf("specialist delegation assignment count mismatch: got %d want %d",
			len(assignments), expected)
	}
	return assignments, nil
}
