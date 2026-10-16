package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
)

const planDeliveryProposalSelect = `SELECT id, run_id, root_agent_id, session_id,
	workspace_id, mode_revision, protocol_version, status, direction_count,
	proposal_fingerprint, requested_by, version, created_at
	FROM plan_delivery_proposals`

type planDeliveryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *SQLiteStore) CreatePlanDeliveryProposal(ctx context.Context,
	operation domain.PlanDeliveryProposalOperation,
	proposal domain.PlanDeliveryProposal,
	policyEvent events.Event, proposalEvent events.Event,
	toolEvent events.Event,
) (domain.PlanDeliveryProposal, bool, error) {
	operation = normalizePlanDeliveryProposalOperation(operation)
	var err error
	proposal, err = redactAndNormalizePlanDeliveryProposal(proposal)
	if err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	if err := validatePlanDeliveryProposalMutation(operation, proposal,
		policyEvent, proposalEvent, toolEvent); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireStructuredMutationWriteLockTx(ctx, tx, operation.RunID); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, operation.RunID,
		operation.LeaseID, operation.LeaseGeneration); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	if existing, found, err := getPlanDeliveryProposalOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	} else if found {
		if err := validatePlanDeliveryProposalReplay(existing, operation); err != nil {
			return domain.PlanDeliveryProposal{}, false, err
		}
		stored, err := getPlanDeliveryProposal(ctx, tx, existing.ProposalID)
		if err != nil {
			return domain.PlanDeliveryProposal{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.PlanDeliveryProposal{}, false, err
		}
		return stored, true, nil
	}
	run, mission, err := requirePlanDeliveryProposalBindingTx(ctx, tx,
		operation, proposal)
	if err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if event.RunID != run.ID || event.MissionID != mission.ID ||
			!event.CreatedAt.Equal(proposal.CreatedAt) {
			return domain.PlanDeliveryProposal{}, false, apperror.New(
				apperror.CodeInvalidArgument,
				"Plan/Delivery proposal event scope or timestamp does not match")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_delivery_proposals
		(id, run_id, root_agent_id, session_id, workspace_id, mode_revision,
		protocol_version, status, direction_count, proposal_fingerprint,
		requested_by, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, proposal.ID,
		proposal.RunID, proposal.RootAgentID, proposal.SessionID,
		proposal.WorkspaceID, proposal.ModeRevision, proposal.Spec.Version,
		proposal.Status, len(proposal.Spec.Directions), proposal.Fingerprint,
		proposal.RequestedBy, proposal.Version, ts(proposal.CreatedAt)); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	for _, direction := range proposal.Spec.Directions {
		tradeoffs, err := json.Marshal(direction.Tradeoffs)
		if err != nil {
			return domain.PlanDeliveryProposal{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plan_delivery_directions
			(proposal_id, ordinal, title, summary, tradeoffs_json, module_count)
			VALUES (?, ?, ?, ?, ?, ?)`, proposal.ID, direction.Ordinal,
			direction.Title, direction.Summary, string(tradeoffs),
			len(direction.Modules)); err != nil {
			return domain.PlanDeliveryProposal{}, false, err
		}
		for _, module := range direction.Modules {
			acceptance, err := json.Marshal(module.AcceptanceCriteria)
			if err != nil {
				return domain.PlanDeliveryProposal{}, false, err
			}
			dependencies, err := json.Marshal(module.Dependencies)
			if err != nil {
				return domain.PlanDeliveryProposal{}, false, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO plan_delivery_modules
				(proposal_id, direction_ordinal, ordinal, title, objective,
				acceptance_json, dependencies_json)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, proposal.ID, direction.Ordinal,
				module.Ordinal, module.Title, module.Objective,
				string(acceptance), string(dependencies)); err != nil {
				return domain.PlanDeliveryProposal{}, false, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_delivery_proposal_operations
		(operation_key_digest, request_fingerprint, invocation_id, proposal_id,
		run_id, session_id, workspace_id, root_agent_id, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.InvocationID, proposal.ID,
		operation.RunID, operation.SessionID, operation.WorkspaceID,
		operation.RootAgentID, operation.RequestedBy,
		ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverPlanDeliveryProposal(ctx, operation, err)
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return domain.PlanDeliveryProposal{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	return domain.ClonePlanDeliveryProposal(proposal), false, nil
}

func (s *SQLiteStore) GetPlanDeliveryProposal(ctx context.Context,
	id string,
) (domain.PlanDeliveryProposal, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.PlanDeliveryProposal{}, apperror.New(
			apperror.CodeInvalidArgument, "Plan/Delivery proposal id is invalid")
	}
	return getPlanDeliveryProposal(ctx, s.db, id)
}

func (s *SQLiteStore) ListPlanDeliveryProposals(ctx context.Context,
	runID string, limit int,
) ([]domain.PlanDeliveryProposal, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery proposal Run id is invalid")
	}
	if limit <= 0 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery proposal limit must be between 1 and 100")
	}
	rows, err := s.db.QueryContext(ctx, planDeliveryProposalSelect+
		` WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.PlanDeliveryProposal, 0)
	directionCounts := make([]int, 0)
	for rows.Next() {
		proposal, directionCount, err := scanPlanDeliveryProposalBase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, proposal)
		directionCounts = append(directionCounts, directionCount)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range out {
		out[index].Spec.Directions, err = listPlanDeliveryDirections(ctx, s.db,
			out[index].ID, directionCounts[index])
		if err != nil {
			return nil, err
		}
		if err := out[index].Validate(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func normalizePlanDeliveryProposalOperation(
	operation domain.PlanDeliveryProposalOperation,
) domain.PlanDeliveryProposalOperation {
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

func redactAndNormalizePlanDeliveryProposal(
	proposal domain.PlanDeliveryProposal,
) (domain.PlanDeliveryProposal, error) {
	proposal = domain.ClonePlanDeliveryProposal(proposal)
	proposal.ID = strings.TrimSpace(proposal.ID)
	proposal.RunID = strings.TrimSpace(proposal.RunID)
	proposal.RootAgentID = strings.TrimSpace(proposal.RootAgentID)
	proposal.SessionID = strings.TrimSpace(proposal.SessionID)
	proposal.WorkspaceID = strings.TrimSpace(proposal.WorkspaceID)
	proposal.RequestedBy = strings.TrimSpace(redact.String(proposal.RequestedBy))
	for directionIndex := range proposal.Spec.Directions {
		direction := &proposal.Spec.Directions[directionIndex]
		direction.Title = redact.String(strings.TrimSpace(direction.Title))
		direction.Summary = redact.String(strings.TrimSpace(direction.Summary))
		for index := range direction.Tradeoffs {
			direction.Tradeoffs[index] = redact.String(
				strings.TrimSpace(direction.Tradeoffs[index]))
		}
		for moduleIndex := range direction.Modules {
			module := &direction.Modules[moduleIndex]
			module.Title = redact.String(strings.TrimSpace(module.Title))
			module.Objective = redact.String(strings.TrimSpace(module.Objective))
			for index := range module.AcceptanceCriteria {
				module.AcceptanceCriteria[index] = redact.String(
					strings.TrimSpace(module.AcceptanceCriteria[index]))
			}
		}
	}
	var err error
	proposal.Spec, err = domain.NormalizePlanDeliverySpec(proposal.Spec)
	if err != nil {
		return domain.PlanDeliveryProposal{}, apperror.Wrap(
			apperror.CodeInvalidArgument,
			"redacted Plan/Delivery proposal is invalid", err)
	}
	proposal.CreatedAt = proposal.CreatedAt.UTC()
	proposal.Fingerprint = domain.PlanDeliveryProposalFingerprint(proposal)
	return proposal, nil
}

func validatePlanDeliveryProposalMutation(
	operation domain.PlanDeliveryProposalOperation,
	proposal domain.PlanDeliveryProposal,
	policyEvent events.Event, proposalEvent events.Event, toolEvent events.Event,
) error {
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Plan/Delivery proposal operation is invalid", err)
	}
	if err := proposal.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Plan/Delivery proposal is invalid", err)
	}
	if operation.ProposalID != proposal.ID || operation.RunID != proposal.RunID ||
		operation.RootAgentID != proposal.RootAgentID ||
		operation.SessionID != proposal.SessionID ||
		operation.WorkspaceID != proposal.WorkspaceID ||
		operation.RequestedBy != proposal.RequestedBy ||
		!operation.CreatedAt.Equal(proposal.CreatedAt) ||
		operation.RequestFingerprint != domain.PlanDeliveryProposalRequestFingerprint(proposal) {
		return apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery proposal operation does not match its proposal")
	}
	if policyEvent.Type != events.PolicyDecisionEvent ||
		policyEvent.SubjectID != operation.InvocationID ||
		proposalEvent.Type != events.PlanDeliveryProposedEvent ||
		proposalEvent.SubjectID != proposal.ID ||
		toolEvent.Type != events.ToolCompletedEvent ||
		toolEvent.SubjectID != operation.InvocationID {
		return apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery proposal events do not match the operation")
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if err := event.Validate(); err != nil {
			return apperror.Wrap(apperror.CodeInvalidArgument,
				"Plan/Delivery proposal event is invalid", err)
		}
	}
	return nil
}

func requirePlanDeliveryProposalBindingTx(ctx context.Context, tx *sql.Tx,
	operation domain.PlanDeliveryProposalOperation,
	proposal domain.PlanDeliveryProposal,
) (domain.Run, domain.Mission, error) {
	run, mission, err := getCoordinatorRunTx(ctx, tx, operation.RunID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if run.Status != domain.RunRunning || run.SessionID != operation.SessionID ||
		mission.WorkspaceID != operation.WorkspaceID ||
		mode.Phase != domain.ExecutionPhasePlan ||
		mode.Revision != proposal.ModeRevision {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Plan/Delivery proposal requires the current running Plan-phase Run")
	}
	root, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		operation.RootAgentID))
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if root.RunID != run.ID || root.Role != domain.AgentRoleRoot || root.ParentID != "" ||
		root.Status != domain.AgentRunning || root.ActiveAttemptID == "" ||
		proposal.RootAgentID != root.ID {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Plan/Delivery proposal requires the active root Agent")
	}
	var checkpointCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_checkpoints
		WHERE run_id = ? AND phase = 'turn_started' AND attempt_id = ?
			AND lease_id = ? AND lease_generation = ?`, run.ID, root.ActiveAttemptID,
		operation.LeaseID, operation.LeaseGeneration).Scan(&checkpointCount); err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if checkpointCount != 1 {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Plan/Delivery proposal is not bound to the active root turn")
	}
	var invocationCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_tool_calls
		WHERE id = ? AND run_id = ? AND session_id = ? AND workspace_id = ?
			AND tool_name = 'plan_delivery_propose' AND action_class = 'agent_proposal'`,
		operation.InvocationID, operation.RunID, operation.SessionID,
		operation.WorkspaceID).Scan(&invocationCount); err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if invocationCount != 1 {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Plan/Delivery proposal is not backed by the Run tool budget ledger")
	}
	return run, mission, nil
}

func validatePlanDeliveryProposalReplay(existing,
	request domain.PlanDeliveryProposalOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.RunID != request.RunID || existing.SessionID != request.SessionID ||
		existing.WorkspaceID != request.WorkspaceID ||
		existing.RootAgentID != request.RootAgentID ||
		existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"Plan/Delivery proposal idempotency key was already used for different intent")
	}
	return nil
}

func (s *SQLiteStore) recoverPlanDeliveryProposal(ctx context.Context,
	operation domain.PlanDeliveryProposalOperation, original error,
) (domain.PlanDeliveryProposal, bool, error) {
	existing, found, err := getPlanDeliveryProposalOperation(ctx, s.db,
		operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.PlanDeliveryProposal{}, false, original
		}
		return domain.PlanDeliveryProposal{}, false, errors.Join(original, err)
	}
	if err := validatePlanDeliveryProposalReplay(existing, operation); err != nil {
		return domain.PlanDeliveryProposal{}, false, err
	}
	proposal, err := s.GetPlanDeliveryProposal(ctx, existing.ProposalID)
	return proposal, true, err
}

func getPlanDeliveryProposalOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.PlanDeliveryProposalOperation, bool, error) {
	var operation domain.PlanDeliveryProposalOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, invocation_id, proposal_id, run_id, session_id,
		workspace_id, root_agent_id, requested_by, created_at
		FROM plan_delivery_proposal_operations WHERE operation_key_digest = ?`,
		keyDigest).Scan(&operation.KeyDigest, &operation.RequestFingerprint,
		&operation.InvocationID, &operation.ProposalID, &operation.RunID,
		&operation.SessionID, &operation.WorkspaceID, &operation.RootAgentID,
		&operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PlanDeliveryProposalOperation{}, false, nil
	}
	if err != nil {
		return domain.PlanDeliveryProposalOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.ValidatePersisted()
}

func getPlanDeliveryProposal(ctx context.Context, queryer planDeliveryQueryer,
	id string,
) (domain.PlanDeliveryProposal, error) {
	proposal, directionCount, err := scanPlanDeliveryProposalBase(
		queryer.QueryRowContext(ctx, planDeliveryProposalSelect+` WHERE id = ?`, id))
	if err != nil {
		return domain.PlanDeliveryProposal{}, err
	}
	proposal.Spec.Directions, err = listPlanDeliveryDirections(ctx, queryer,
		proposal.ID, directionCount)
	if err != nil {
		return domain.PlanDeliveryProposal{}, err
	}
	return proposal, proposal.Validate()
}

func scanPlanDeliveryProposalBase(row scanner) (domain.PlanDeliveryProposal, int, error) {
	var proposal domain.PlanDeliveryProposal
	var protocol, status, createdAt string
	var directionCount int
	if err := row.Scan(&proposal.ID, &proposal.RunID, &proposal.RootAgentID,
		&proposal.SessionID, &proposal.WorkspaceID, &proposal.ModeRevision,
		&protocol, &status, &directionCount, &proposal.Fingerprint,
		&proposal.RequestedBy, &proposal.Version, &createdAt); err != nil {
		return domain.PlanDeliveryProposal{}, 0, err
	}
	proposal.Spec.Version = protocol
	proposal.Status = domain.PlanDeliveryProposalStatus(status)
	proposal.CreatedAt = parseTS(createdAt)
	return proposal, directionCount, nil
}

func listPlanDeliveryDirections(ctx context.Context, queryer planDeliveryQueryer,
	proposalID string, expected int,
) ([]domain.PlanDeliveryDirection, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT ordinal, title, summary,
		tradeoffs_json, module_count FROM plan_delivery_directions
		WHERE proposal_id = ? ORDER BY ordinal`, proposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	directions := make([]domain.PlanDeliveryDirection, 0, expected)
	moduleCounts := make([]int, 0, expected)
	for rows.Next() {
		var direction domain.PlanDeliveryDirection
		var tradeoffsJSON string
		var moduleCount int
		if err := rows.Scan(&direction.Ordinal, &direction.Title,
			&direction.Summary, &tradeoffsJSON, &moduleCount); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tradeoffsJSON),
			&direction.Tradeoffs); err != nil {
			return nil, fmt.Errorf("decode Plan/Delivery tradeoffs: %w", err)
		}
		directions = append(directions, direction)
		moduleCounts = append(moduleCounts, moduleCount)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(directions) != expected {
		return nil, errors.New("Plan/Delivery direction count is inconsistent")
	}
	for index := range directions {
		directions[index].Modules, err = listPlanDeliveryModules(ctx, queryer,
			proposalID, directions[index].Ordinal, moduleCounts[index])
		if err != nil {
			return nil, err
		}
	}
	return directions, nil
}

func listPlanDeliveryModules(ctx context.Context, queryer planDeliveryQueryer,
	proposalID string, directionOrdinal int, expected int,
) ([]domain.PlanDeliveryModule, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT ordinal, title, objective,
		acceptance_json, dependencies_json FROM plan_delivery_modules
		WHERE proposal_id = ? AND direction_ordinal = ? ORDER BY ordinal`,
		proposalID, directionOrdinal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	modules := make([]domain.PlanDeliveryModule, 0, expected)
	for rows.Next() {
		var module domain.PlanDeliveryModule
		var acceptanceJSON, dependenciesJSON string
		if err := rows.Scan(&module.Ordinal, &module.Title, &module.Objective,
			&acceptanceJSON, &dependenciesJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(acceptanceJSON),
			&module.AcceptanceCriteria); err != nil {
			return nil, fmt.Errorf("decode Plan/Delivery acceptance criteria: %w", err)
		}
		if err := json.Unmarshal([]byte(dependenciesJSON),
			&module.Dependencies); err != nil {
			return nil, fmt.Errorf("decode Plan/Delivery dependencies: %w", err)
		}
		modules = append(modules, module)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(modules) != expected {
		return nil, errors.New("Plan/Delivery module count is inconsistent")
	}
	return modules, nil
}
