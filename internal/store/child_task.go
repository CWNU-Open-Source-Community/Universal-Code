package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/waitgraph"
)

// CreateChildTaskProposal persists one Go-validated, model-proposed child
// task set. Replays by operation key and semantically equal duplicate
// proposals both return the original proposal without a second write.
func (s *SQLiteStore) CreateChildTaskProposal(ctx context.Context,
	operation domain.ChildTaskOperation, proposal domain.ChildTaskProposal,
	policyEvent, proposalEvent, toolEvent events.Event,
) (domain.ChildTaskProposal, bool, error) {
	if err := proposal.Validate(); err != nil {
		return domain.ChildTaskProposal{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"child task proposal is invalid", err)
	}
	if err := operation.Validate(); err != nil {
		return domain.ChildTaskProposal{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"child task operation is invalid", err)
	}
	if proposal.Status != domain.ChildTaskProposalProposed {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeInvalidArgument,
			"new child task proposals must start proposed")
	}
	dedupFingerprint := runmutation.Fingerprint("child_task_dedup.v1", proposal.RunID,
		proposal.RootAgentID, proposal.Spec.SpecJSONFingerprint())
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	_, status, missionID, err := loadMonetaryRunTx(ctx, tx, proposal.RunID)
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	if status == domain.RunCancelled || status == domain.RunCompleted || status == domain.RunFailed {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"terminal runs cannot receive child task proposals")
	}
	// Pin the run row to serialize concurrent child task mutations under
	// the single-writer SQLite pool (same touch pattern as agent messages).
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`,
		proposal.RunID); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	var storedFingerprint, storedProposalID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, proposal_id
		FROM child_task_proposal_operations WHERE operation_key_digest = ?`, operation.KeyDigest).
		Scan(&storedFingerprint, &storedProposalID)
	if err == nil {
		if storedFingerprint != operation.RequestFingerprint {
			return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeConflict,
				"child task operation key was already used for different intent")
		}
		existing, found, scanErr := s.getChildTaskProposalTx(ctx, tx, storedProposalID)
		if scanErr != nil || !found {
			return domain.ChildTaskProposal{}, false, scanErr
		}
		if err := tx.Commit(); err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ChildTaskProposal{}, false, err
	}
	// Duplicate detection: an equal task set proposed again resolves to the
	// original proposal regardless of the operation key.
	var duplicateID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM child_task_proposals
		WHERE dedup_fingerprint = ?`, dedupFingerprint).Scan(&duplicateID)
	if err == nil {
		existing, found, scanErr := s.getChildTaskProposalTx(ctx, tx, duplicateID)
		if scanErr != nil || !found {
			return domain.ChildTaskProposal{}, false, scanErr
		}
		if err := tx.Commit(); err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ChildTaskProposal{}, false, err
	}
	if proposal.Surface == domain.ChildTaskSurfaceCore {
		if err := requireCoreChildTaskBudgetTx(ctx, tx, proposal); err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
	}
	specJSON, err := json.Marshal(proposal.Spec)
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO child_task_proposals
		(id, run_id, root_agent_id, session_id, workspace_id, status, spec_json, surface,
		fanout_tier, dedup_fingerprint, requested_by, version, created_at, reviewed_at, reviewer)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '')`,
		proposal.ID, proposal.RunID, proposal.RootAgentID, proposal.SessionID,
		proposal.WorkspaceID, proposal.Status, string(specJSON), string(proposal.Surface),
		string(proposal.FanoutTier), dedupFingerprint, proposal.RequestedBy, proposal.Version,
		ts(proposal.CreatedAt)); err != nil {
		// Concurrent duplicate: recover as a replay of the already persisted
		// equal proposal instead of surfacing a raw constraint error.
		if isUniqueViolation(err) {
			var concurrentID string
			if scanErr := tx.QueryRowContext(ctx, `SELECT id FROM child_task_proposals
				WHERE dedup_fingerprint = ?`, dedupFingerprint).Scan(&concurrentID); scanErr == nil {
				existing, found, scanErr := s.getChildTaskProposalTx(ctx, tx, concurrentID)
				if scanErr != nil || !found {
					return domain.ChildTaskProposal{}, false, scanErr
				}
				if err := tx.Commit(); err != nil {
					return domain.ChildTaskProposal{}, false, err
				}
				return existing, true, nil
			}
		}
		return domain.ChildTaskProposal{}, false, err
	}
	for _, task := range proposal.Spec.Tasks {
		depsJSON, err := json.Marshal(task.DependencyOrdinals)
		if err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO child_task_assignments
			(proposal_id, ordinal, surface, fanout_tier, status, dependency_ordinals_json,
			turn_limit, token_limit, timeout_millis, admitted_agent_id, fanout_plan_id,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
			proposal.ID, task.Ordinal, string(proposal.Surface), string(proposal.FanoutTier),
			domain.ChildTaskAssignmentProposed, string(depsJSON), task.TurnLimit,
			task.TokenLimit, task.TimeoutMillis, ts(proposal.CreatedAt), ts(proposal.CreatedAt)); err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO child_task_proposal_operations
		(operation_key_digest, request_fingerprint, proposal_id, created_at) VALUES (?, ?, ?, ?)`,
		operation.KeyDigest, operation.RequestFingerprint, proposal.ID, ts(operation.CreatedAt)); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	for _, event := range []events.Event{policyEvent, proposalEvent, toolEvent} {
		if event.RunID != proposal.RunID || event.MissionID != missionID {
			return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeInvalidArgument,
				"child task event scope does not match its proposal")
		}
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	return proposal, false, nil
}

// requireCoreChildTaskBudgetTx atomically pre-checks the core-surface
// proposal against the current child count and the effective root budget
// before the proposal is persisted.
func requireCoreChildTaskBudgetTx(ctx context.Context, tx *sql.Tx,
	proposal domain.ChildTaskProposal,
) error {
	var children int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes
		WHERE run_id = ? AND role = ?`, proposal.RunID, domain.AgentRoleSpecialist).
		Scan(&children); err != nil {
		return err
	}
	if children+len(proposal.Spec.Tasks) > domain.MaxAgentChildren {
		return apperror.New(apperror.CodeResourceExhausted,
			"core child capacity cannot exceed two children per Run")
	}
	var budgetJSON string
	var runStatus domain.RunStatus
	if err := tx.QueryRowContext(ctx, `SELECT budget_json, status FROM runs WHERE id = ?`,
		proposal.RunID).Scan(&budgetJSON, &runStatus); err != nil {
		return err
	}
	_ = runStatus
	var budget domain.Budget
	if err := json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		return err
	}
	root, found, err := getRootAgentTx(ctx, tx, proposal.RunID)
	if err != nil {
		return err
	}
	if !found {
		return apperror.New(apperror.CodeFailedPrecondition, "child task proposal requires a root Agent")
	}
	_ = root
	effective, err := effectiveRootBudgetTx(ctx, tx, domain.Run{ID: proposal.RunID, Budget: budget},
		proposal.RootAgentID)
	if err != nil {
		return err
	}
	var totalTurns, totalTokens int64
	for _, task := range proposal.Spec.Tasks {
		totalTurns += task.TurnLimit
		totalTokens += task.TokenLimit
	}
	if totalTurns >= int64(effective.MaxTurns) {
		return apperror.New(apperror.CodeResourceExhausted,
			"child task turn budget exceeds the remaining root allowance")
	}
	if effective.MaxTokens > 0 && totalTokens >= effective.MaxTokens {
		return apperror.New(apperror.CodeResourceExhausted,
			"child task token budget exceeds the remaining root allowance")
	}
	return nil
}

func (s *SQLiteStore) getChildTaskProposalTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.ChildTaskProposal, bool, error) {
	var proposal domain.ChildTaskProposal
	var specJSON, surface, tier, status, reviewer string
	var reviewedAt sql.NullString
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT id, run_id, root_agent_id, session_id,
		workspace_id, status, spec_json, surface, fanout_tier, requested_by, version,
		created_at, reviewed_at, reviewer FROM child_task_proposals WHERE id = ?`, id).
		Scan(&proposal.ID, &proposal.RunID, &proposal.RootAgentID, &proposal.SessionID,
			&proposal.WorkspaceID, &status, &specJSON, &surface, &tier, &proposal.RequestedBy,
			&proposal.Version, &createdAt, &reviewedAt, &reviewer)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ChildTaskProposal{}, false, nil
		}
		return domain.ChildTaskProposal{}, false, err
	}
	_ = reviewedAt
	_ = reviewer
	proposal.Status = status
	proposal.Surface = domain.ChildTaskSurface(surface)
	proposal.FanoutTier = domain.ReadOnlyFanoutTier(tier)
	proposal.CreatedAt = parseTS(createdAt)
	if err := json.Unmarshal([]byte(specJSON), &proposal.Spec); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	for index := range proposal.Spec.Tasks {
		proposal.Spec.Tasks[index].Ordinal = index + 1
	}
	return proposal, true, nil
}

// ReviewChildTaskProposal records one operator decision. Approve may
// override the fan-out tier with an explicit user ceiling (1/2/4/6).
func (s *SQLiteStore) ReviewChildTaskProposal(ctx context.Context,
	review domain.ChildTaskReview, operationKey string,
) (domain.ChildTaskProposal, bool, error) {
	review, err := review.Normalize()
	if err != nil {
		return domain.ChildTaskProposal{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"child task review is invalid", err)
	}
	normalizedKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.ChildTaskProposal{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"child task review operation key is invalid", err)
	}
	keyDigest := runmutation.OperationKeyDigest("child_task_review", review.ProposalID, normalizedKey)
	requestFingerprint := runmutation.Fingerprint("child_task_review_request.v1",
		review.ProposalID, review.Action, review.Reviewer, string(review.FanoutTier))
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint FROM child_task_review_operations
		WHERE operation_key_digest = ?`, keyDigest).Scan(&storedFingerprint)
	if err == nil {
		if storedFingerprint != requestFingerprint {
			return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeConflict,
				"child task review key was already used for different intent")
		}
		existing, found, scanErr := s.getChildTaskProposalTx(ctx, tx, review.ProposalID)
		if scanErr != nil || !found {
			return domain.ChildTaskProposal{}, false, scanErr
		}
		if err := tx.Commit(); err != nil {
			return domain.ChildTaskProposal{}, false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ChildTaskProposal{}, false, err
	}
	proposal, found, err := s.getChildTaskProposalTx(ctx, tx, review.ProposalID)
	if err != nil || !found {
		return domain.ChildTaskProposal{}, false, err
	}
	if proposal.Status != domain.ChildTaskProposalProposed {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeConflict,
			"child task proposal was already reviewed")
	}
	newStatus := domain.ChildTaskProposalDenied
	if review.Action == "approve" {
		newStatus = domain.ChildTaskProposalApproved
	}
	tier := proposal.FanoutTier
	if review.Action == "approve" && proposal.Surface == domain.ChildTaskSurfaceReadOnlyFanout {
		tier = review.FanoutTier
	}
	result, err := tx.ExecContext(ctx, `UPDATE child_task_proposals SET status = ?, fanout_tier = ?,
		reviewed_at = ?, reviewer = ? WHERE id = ? AND status = 'proposed'`,
		newStatus, string(tier), ts(review.ReviewedAt), review.Reviewer, proposal.ID)
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeConflict,
			"child task proposal was reviewed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE child_task_assignments SET fanout_tier = ?,
		updated_at = ? WHERE proposal_id = ?`, string(tier), ts(review.ReviewedAt),
		proposal.ID); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO child_task_review_operations
		(operation_key_digest, request_fingerprint, proposal_id, created_at) VALUES (?, ?, ?, ?)`,
		keyDigest, requestFingerprint, proposal.ID, ts(review.ReviewedAt)); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	_, _, missionID, err := loadMonetaryRunTx(ctx, tx, proposal.RunID)
	if err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	proposal.Status = newStatus
	proposal.FanoutTier = tier
	_ = appendSupervisorEventTx(ctx, tx, domain.Run{ID: proposal.RunID, MissionID: missionID},
		events.ChildTaskReviewedEvent, "agent_coordinator", proposal.ID, map[string]any{
			"status": newStatus, "surface": string(proposal.Surface),
			"fanout_tier": string(tier), "reviewer": review.Reviewer,
			"task_count": len(proposal.Spec.Tasks),
		})
	if err := tx.Commit(); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	return proposal, false, nil
}

// AdmitChildTaskProposal turns an approved proposal into admitted children:
// core tasks go through the fenced Specialist admission and bind their
// declared dependencies onto the schema v101 wait ledger; read-only tasks
// get one immutable fan-out plan per task. Execution stays operator-driven.
func (s *SQLiteStore) AdmitChildTaskProposal(ctx context.Context, proposalID,
	operationKey string,
) (domain.ChildTaskProposal, []domain.ChildTaskAssignment, error) {
	proposalID = strings.TrimSpace(proposalID)
	_, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.ChildTaskProposal{}, nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"child task admission key is invalid", err)
	}
	proposal, found, err := s.getChildTaskProposal(ctx, proposalID)
	if err != nil || !found {
		return domain.ChildTaskProposal{}, nil, err
	}
	if proposal.Status != domain.ChildTaskProposalApproved {
		return domain.ChildTaskProposal{}, nil, apperror.New(apperror.CodeFailedPrecondition,
			"child task proposal requires an operator approval before admission")
	}
	assignments, err := s.listChildTaskAssignments(ctx, proposalID)
	if err != nil {
		return domain.ChildTaskProposal{}, nil, err
	}
	for index := range assignments {
		if assignments[index].Status == domain.ChildTaskAssignmentAdmitted {
			continue
		}
		if proposal.Surface == domain.ChildTaskSurfaceCore {
			task := proposal.Spec.Tasks[assignments[index].Ordinal-1]
			instruction, err := json.Marshal(domain.AgentInstructionPayload{
				Version: domain.SpecialistInstructionVersion, Instruction: task.Goal,
			})
			if err != nil {
				return domain.ChildTaskProposal{}, nil, err
			}
			// Admission and instruction delivery commit separately. Bind both
			// retries to the assignment, including retries with a new caller key.
			ordinal := strconv.Itoa(assignments[index].Ordinal)
			admissionKey := runmutation.Fingerprint("child_task_admission.v1", proposal.ID, ordinal)
			admitted, _, err := s.admitSpecialist(ctx, domain.SpecialistAdmission{
				AgentID: idgen.New("agent"), SessionID: idgen.New("sess"),
				RunID: proposal.RunID, ParentAgentID: proposal.RootAgentID,
				Title: task.Title, Skills: task.Skills,
				TurnLimit: task.TurnLimit, TokenLimit: task.TokenLimit,
				AllowedReadPaths: task.AllowedReadPaths, ToolCallLimit: task.ToolCallLimit, CostLimitMicros: task.CostLimitMicros,
				MaxChildren: domain.MaxAgentChildren, CreatedAt: time.Now().UTC(),
			}, admissionKey, &childTaskAdmissionBinding{proposalID: proposal.ID, task: task})
			if err != nil {
				return domain.ChildTaskProposal{}, nil, apperror.Normalize(err)
			}
			if _, _, err := s.SendAgentMessage(ctx, domain.AgentMessage{
				ID: idgen.New("agentmsg"), RunID: proposal.RunID,
				SenderAgentID: proposal.RootAgentID, RecipientAgentID: admitted.ID,
				Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage,
				PayloadJSON: string(instruction),
			}, runmutation.Fingerprint("child_task_instruction.v1", proposal.ID, ordinal)); err != nil {
				return domain.ChildTaskProposal{}, nil, apperror.Normalize(err)
			}
			assignments[index].AdmittedAgentID = admitted.ID
			if err := s.markChildTaskAssignmentAdmitted(ctx, proposal.ID,
				assignments[index].Ordinal, admitted.ID, ""); err != nil {
				return domain.ChildTaskProposal{}, nil, err
			}
			assignments[index].Status = domain.ChildTaskAssignmentAdmitted
		} else {
			// Read-only tasks stay on the existing operator-driven fan-out
			// surface: the proposal records the binding intent and the Go-side
			// permission ceiling (list/read only, operator tier override), while
			// immutable plan creation and execution remain the operator's
			// explicit steps through the existing fan-out flow.
			if err := s.markChildTaskAssignmentAdmitted(ctx, proposal.ID,
				assignments[index].Ordinal, "", ""); err != nil {
				return domain.ChildTaskProposal{}, nil, err
			}
			assignments[index].Status = domain.ChildTaskAssignmentAdmitted
		}
	}
	// Bind each dependency to the immutable proposal and task ordinals. The
	// admitted child's creation time anchors its deadline across retries.
	admittedByOrdinal := make(map[int]string, len(assignments))
	for _, assignment := range assignments {
		if assignment.AdmittedAgentID != "" {
			admittedByOrdinal[assignment.Ordinal] = assignment.AdmittedAgentID
		}
	}
	for _, task := range proposal.Spec.Tasks {
		sourceID := admittedByOrdinal[task.Ordinal]
		if sourceID == "" {
			continue
		}
		for _, dependency := range task.DependencyOrdinals {
			targetID := admittedByOrdinal[dependency]
			if targetID == "" {
				continue
			}
			// Reuse even settled edges, including those written before admission
			// keys were bound to the proposal rather than the HTTP request.
			_, err := scanDependencyEdge(s.db.QueryRowContext(ctx, dependencyEdgeSelect+`
				WHERE run_id = ? AND source_kind = 'agent' AND source_id = ?
				AND target_kind = 'agent' AND target_id = ? AND generation = 1
				AND reason = 'child task dependency' AND failure_policy = 'fail'`,
				proposal.RunID, sourceID, targetID))
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return domain.ChildTaskProposal{}, nil, err
			}
			source, err := s.GetAgentNode(ctx, sourceID)
			if err != nil {
				return domain.ChildTaskProposal{}, nil, err
			}
			now := source.CreatedAt
			dependencyKey := runmutation.Fingerprint("child_task_dependency.v1", proposal.ID,
				strconv.Itoa(task.Ordinal), strconv.Itoa(dependency))
			if _, _, err := s.RecordDependencyWait(ctx, domain.DependencyEdge{
				ID: idgen.New("depedge"), RunID: proposal.RunID,
				SourceKind: waitgraph.KindAgent, SourceID: sourceID,
				TargetKind: waitgraph.KindAgent, TargetID: targetID,
				Reason: "child task dependency", State: domain.AgentDependencyWait,
				FailurePolicy: domain.DependencyPolicyFail, Generation: 1,
				Deadline:  now.Add(time.Duration(task.TimeoutMillis) * time.Millisecond),
				CreatedAt: now, UpdatedAt: now,
			}, dependencyKey); err != nil {
				return domain.ChildTaskProposal{}, nil, err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ChildTaskProposal{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, _, missionID, err := loadMonetaryRunTx(ctx, tx, proposal.RunID)
	if err != nil {
		return domain.ChildTaskProposal{}, nil, err
	}
	if err := appendSupervisorEventTx(ctx, tx, domain.Run{ID: proposal.RunID, MissionID: missionID},
		events.ChildTaskAdmittedEvent, "agent_coordinator", proposal.ID, map[string]any{
			"surface": string(proposal.Surface), "task_count": len(proposal.Spec.Tasks),
		}); err != nil {
		return domain.ChildTaskProposal{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ChildTaskProposal{}, nil, err
	}
	return proposal, assignments, nil
}

// GetChildTaskProposal returns one proposal by id.
func (s *SQLiteStore) GetChildTaskProposal(ctx context.Context, id string) (domain.ChildTaskProposal, bool, error) {
	return s.getChildTaskProposal(ctx, strings.TrimSpace(id))
}

// ListChildTaskAssignments returns the proposal's per-task records.
func (s *SQLiteStore) ListChildTaskAssignments(ctx context.Context, proposalID string) ([]domain.ChildTaskAssignment, error) {
	return s.listChildTaskAssignments(ctx, strings.TrimSpace(proposalID))
}

func (s *SQLiteStore) markChildTaskAssignmentAdmitted(ctx context.Context, proposalID string,
	ordinal int, agentID, planID string,
) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE child_task_assignments SET status = 'admitted',
		admitted_agent_id = ?, fanout_plan_id = ?, updated_at = ?
		WHERE proposal_id = ? AND ordinal = ? AND status = 'proposed'`,
		agentID, planID, ts(now), proposalID, ordinal)
	return err
}

type childTaskAdmissionBinding struct {
	proposalID string
	task       domain.ChildTask
}

// Bind the assignment in the child's creation transaction. A wake during
// interrupted instruction/dependency delivery must still see the declared waits.
func bindChildTaskAdmissionTx(ctx context.Context, tx *sql.Tx, binding *childTaskAdmissionBinding, agentID string) error {
	if binding == nil {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE child_task_assignments SET admitted_agent_id = ?
		WHERE proposal_id = ? AND ordinal = ? AND surface = 'core'
		AND (admitted_agent_id = '' OR admitted_agent_id = ?)`,
		agentID, binding.proposalID, binding.task.Ordinal, agentID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return apperror.New(apperror.CodeConflict, "child task assignment admission binding changed")
	}
	return nil
}

func (s *SQLiteStore) getChildTaskProposal(ctx context.Context, id string,
) (domain.ChildTaskProposal, bool, error) {
	return s.getChildTaskProposalTx(ctx, s.db, id)
}

func (s *SQLiteStore) listChildTaskAssignments(ctx context.Context, proposalID string,
) ([]domain.ChildTaskAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proposal_id, ordinal, surface, fanout_tier,
		status, dependency_ordinals_json, turn_limit, token_limit, timeout_millis,
		admitted_agent_id, fanout_plan_id, created_at, updated_at
		FROM child_task_assignments WHERE proposal_id = ? ORDER BY ordinal`, proposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ChildTaskAssignment, 0, 6)
	for rows.Next() {
		var assignment domain.ChildTaskAssignment
		var surface, tier, status, depsJSON, created, updated string
		if err := rows.Scan(&assignment.ProposalID, &assignment.Ordinal, &surface, &tier,
			&status, &depsJSON, &assignment.TurnLimit, &assignment.TokenLimit,
			&assignment.TimeoutMillis, &assignment.AdmittedAgentID, &assignment.FanoutPlanID,
			&created, &updated); err != nil {
			return nil, err
		}
		assignment.Surface = domain.ChildTaskSurface(surface)
		assignment.FanoutTier = domain.ReadOnlyFanoutTier(tier)
		assignment.Status = status
		assignment.CreatedAt = parseTS(created)
		assignment.UpdatedAt = parseTS(updated)
		_ = depsJSON
		out = append(out, assignment)
	}
	return out, rows.Err()
}

// ListChildTaskProposals returns the run's proposals, newest first.
func (s *SQLiteStore) ListChildTaskProposals(ctx context.Context, runID string,
	limit int,
) ([]domain.ChildTaskProposal, error) {
	runID = strings.TrimSpace(runID)
	if limit <= 0 || limit > 64 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "child task proposal list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM child_task_proposals
		WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, 8)
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
	out := make([]domain.ChildTaskProposal, 0, len(ids))
	for _, id := range ids {
		proposal, found, err := s.getChildTaskProposal(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, proposal)
		}
	}
	return out, nil
}
