package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
)

const specialistToolCallSelect = `SELECT a.run_id,a.agent_id,c.agent_attempt_id,c.round,c.position,r.model_attempt,c.call_id,c.name,c.payload_json,c.authority_json,c.approval_source_id,c.approval_id,c.status,c.result_json,c.error_code,c.charge_id,c.created_at,c.started_at,c.completed_at FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id JOIN specialist_tool_rounds r ON r.agent_attempt_id=c.agent_attempt_id AND r.round=c.round`

func scanSpecialistToolCall(row scanner) (domain.SpecialistToolCall, error) {
	var c domain.SpecialistToolCall
	var created string
	var started, completed sql.NullString
	err := row.Scan(&c.Ref.RunID, &c.Ref.AgentID, &c.Ref.AttemptID, &c.Round, &c.Position, &c.ModelAttempt, &c.CallID, &c.Name, &c.PayloadJSON, &c.AuthorityJSON, &c.ApprovalSourceID, &c.ApprovalID, &c.Status, &c.ResultJSON, &c.ErrorCode, &c.ChargeID, &created, &started, &completed)
	c.CreatedAt = parseTS(created)
	c.StartedAt = parseNullableTS(started)
	c.CompletedAt = parseNullableTS(completed)
	return c, err
}

func getSpecialistToolCallTx(ctx context.Context, tx *sql.Tx, ref domain.AgentAttemptRef, callID string) (domain.SpecialistToolCall, error) {
	return scanSpecialistToolCall(tx.QueryRowContext(ctx, specialistToolCallSelect+` WHERE a.run_id=? AND a.agent_id=? AND c.agent_attempt_id=? AND c.call_id=?`, ref.RunID, ref.AgentID, ref.AttemptID, callID))
}

// GetSpecialistToolCall also reads historical receipts for owned Job checks.
// It does not authorize another dispatch.
func (s *SQLiteStore) GetSpecialistToolCall(ctx context.Context, ref domain.AgentAttemptRef, callID string) (domain.SpecialistToolCall, error) {
	if err := ref.Validate(); err != nil {
		return domain.SpecialistToolCall{}, err
	}
	return scanSpecialistToolCall(s.db.QueryRowContext(ctx, specialistToolCallSelect+` WHERE a.run_id=? AND a.agent_id=? AND c.agent_attempt_id=? AND c.call_id=?`, ref.RunID, ref.AgentID, ref.AttemptID, callID))
}

func getSpecialistApprovalCallTx(ctx context.Context, tx *sql.Tx, runID, proposalID string) (domain.SpecialistToolCall, bool, error) {
	c, err := scanSpecialistToolCall(tx.QueryRowContext(ctx, specialistToolCallSelect+` WHERE a.run_id=? AND c.approval_source_id=?`, runID, proposalID))
	return c, c.StartedAt != nil, err
}

func (s *SQLiteStore) GetSpecialistApprovalCall(ctx context.Context, runID, proposalID string) (domain.SpecialistToolCall, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SpecialistToolCall{}, false, err
	}
	defer tx.Rollback()
	c, started, err := getSpecialistApprovalCallTx(ctx, tx, runID, proposalID)
	if err != nil {
		return c, started, err
	}
	if err = validateSpecialistApprovalPermissionTx(ctx, tx, runID, c.AuthorityJSON); err != nil {
		return c, started, err
	}
	return c, started, tx.Commit()
}

func validateSpecialistWorkerBindingTx(ctx context.Context, tx *sql.Tx, ref domain.AgentAttemptRef, name string) (domain.SpecialistToolBinding, error) {
	b := domain.SpecialistToolBinding{Ref: ref}
	err := tx.QueryRowContext(ctx, `SELECT b.session_id,b.workspace_id,b.root_fingerprint,b.brief_fingerprint,b.mode,b.batch_plan_id,b.batch_ordinal,b.batch_generation,b.allowed_read_paths_json,b.worker_profile_json FROM specialist_tool_bindings b JOIN agent_nodes n ON n.id=b.agent_id JOIN specialist_task_briefs brief ON brief.agent_attempt_id=b.agent_attempt_id WHERE b.agent_attempt_id=? AND b.run_id=? AND b.agent_id=? AND b.mode='worker' AND b.worker_profile_json=n.worker_profile_json AND b.brief_fingerprint=brief.fingerprint`, ref.AttemptID, ref.RunID, ref.AgentID).Scan(&b.SessionID, &b.WorkspaceID, &b.RootFingerprint, &b.BriefFingerprint, &b.Mode, &b.BatchPlanID, &b.BatchOrdinal, &b.BatchGeneration, &b.AllowedReadPathsJSON, &b.WorkerProfileJSON)
	if err != nil {
		return b, err
	}
	var profile *domain.WorkerProfile
	if err = json.Unmarshal([]byte(b.WorkerProfileJSON), &profile); err != nil {
		return b, err
	}
	if !profile.AllowsTool(name) {
		return b, apperror.New(apperror.CodePolicyDenied, "child worker tool permission was revoked or narrowed")
	}
	return b, nil
}

func (s *SQLiteStore) ValidateSpecialistToolSource(ctx context.Context, source domain.SpecialistToolSource) (domain.SpecialistToolCall, error) {
	if err := source.Ref.Validate(); err != nil {
		return domain.SpecialistToolCall{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	defer tx.Rollback()
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, source.Ref)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	b, err := validateSpecialistWorkerBindingTx(ctx, tx, source.Ref, source.Name)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	c, err := getSpecialistToolCallTx(ctx, tx, source.Ref, source.CallID)
	if err != nil {
		return c, err
	}
	if run.Status != domain.RunRunning || child.SessionID != source.SessionID || b.SessionID != source.SessionID || b.WorkspaceID != source.WorkspaceID || attempt.LeaseID != source.LeaseID || attempt.LeaseGeneration != source.LeaseGeneration || c.Status != domain.SupervisorToolPending || c.Name != source.Name || c.PayloadJSON != source.PayloadJSON || c.AuthorityJSON != source.AuthorityJSON || (source.ChargeID != "" && c.ChargeID != source.ChargeID) || (source.RequireStarted && (source.ChargeID == "" || c.StartedAt == nil)) {
		return c, apperror.New(apperror.CodeConflict, "child runtime source differs from its exact durable call")
	}
	return c, tx.Commit()
}

func (s *SQLiteStore) BindSpecialistToolAuthority(ctx context.Context, ref domain.AgentAttemptRef, callID, authorityJSON string) (domain.SpecialistToolCall, error) {
	var authority map[string]json.RawMessage
	if len(authorityJSON) > domain.MaxSupervisorToolAuthorityBytes || json.Unmarshal([]byte(authorityJSON), &authority) != nil || authority == nil {
		return domain.SpecialistToolCall{}, errors.New("invalid child host authority")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(authorityJSON)); err != nil {
		return domain.SpecialistToolCall{}, err
	}
	authorityJSON = compact.String()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	defer tx.Rollback()
	if _, _, _, err = loadActiveAgentAttemptTx(ctx, tx, ref); err != nil {
		return domain.SpecialistToolCall{}, err
	}
	c, err := getSpecialistToolCallTx(ctx, tx, ref, callID)
	if err != nil {
		return c, err
	}
	if _, err = validateSpecialistWorkerBindingTx(ctx, tx, ref, c.Name); err != nil {
		return c, err
	}
	if c.Status != domain.SupervisorToolPending || (c.AuthorityJSON != "" && c.AuthorityJSON != authorityJSON) {
		return c, apperror.New(apperror.CodeConflict, "child host authority is already pinned")
	}
	if c.AuthorityJSON == "" {
		if c.StartedAt != nil {
			return c, errors.New("child call already started without host authority")
		}
		c.AuthorityJSON = authorityJSON
		c.ApprovalSourceID = domain.SpecialistToolApprovalID(ref, callID)
		if _, err = tx.ExecContext(ctx, `UPDATE specialist_tool_calls SET authority_json=?,approval_source_id=? WHERE agent_attempt_id=? AND call_id=? AND status='pending' AND authority_json='' AND started_at IS NULL`, c.AuthorityJSON, c.ApprovalSourceID, ref.AttemptID, callID); err != nil {
			return c, err
		}
	}
	return c, tx.Commit()
}

func (s *SQLiteStore) SetSpecialistToolApproval(ctx context.Context, ref domain.AgentAttemptRef, callID, approvalID string) (domain.SpecialistToolCall, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	defer tx.Rollback()
	if _, _, _, err = loadActiveAgentAttemptTx(ctx, tx, ref); err != nil {
		return domain.SpecialistToolCall{}, err
	}
	c, err := getSpecialistToolCallTx(ctx, tx, ref, callID)
	if err != nil {
		return c, err
	}
	b, err := validateSpecialistWorkerBindingTx(ctx, tx, ref, c.Name)
	if err != nil {
		return c, err
	}
	record, err := getApprovalTx(ctx, tx, approvalID, "")
	if err != nil {
		return c, err
	}
	if c.Status != domain.SupervisorToolPending || c.StartedAt != nil || c.AuthorityJSON == "" || (c.ApprovalID != "" && c.ApprovalID != approvalID) || record.RunID != ref.RunID || record.SessionID != b.SessionID || record.WorkspaceID != b.WorkspaceID || record.ProposalID != c.ApprovalSourceID || record.RequestFingerprint != domain.SpecialistToolApprovalFingerprint(c) {
		return c, errors.New("child approval differs from its pinned call")
	}
	c.ApprovalID = approvalID
	if _, err = tx.ExecContext(ctx, `UPDATE specialist_tool_calls SET approval_id=? WHERE agent_attempt_id=? AND call_id=? AND status='pending' AND started_at IS NULL`, approvalID, ref.AttemptID, callID); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *SQLiteStore) MarkSpecialistToolStarted(ctx context.Context, ref domain.AgentAttemptRef, callID, authorityJSON string) (domain.SpecialistToolCall, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	defer tx.Rollback()
	_, _, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	c, err := getSpecialistToolCallTx(ctx, tx, ref, callID)
	if err != nil {
		return c, err
	}
	if _, err = validateSpecialistWorkerBindingTx(ctx, tx, ref, c.Name); err != nil {
		return c, err
	}
	if run.Status != domain.RunRunning || c.Status != domain.SupervisorToolPending || c.ChargeID == "" || c.AuthorityJSON == "" || c.AuthorityJSON != authorityJSON {
		return c, errors.New("child start lacks its charged pinned authority")
	}
	if c.ApprovalID != "" {
		record, err := getApprovalTx(ctx, tx, c.ApprovalID, "")
		if err != nil {
			return c, err
		}
		matches := record.ProposalID == c.ApprovalSourceID && record.RequestFingerprint == domain.SpecialistToolApprovalFingerprint(c)
		if !matches && (c.Name == "workspace_apply" || c.Name == "workspace_delete") {
			matches = specialistFileEditApprovalMatchesTx(ctx, tx, c, record) == nil
		}
		if record.Status != approval.StatusApproved || !matches {
			return c, apperror.New(apperror.CodePolicyDenied, "child tool approval is not approved")
		}
	}
	if c.StartedAt == nil {
		now := time.Now().UTC()
		c.StartedAt = &now
		if _, err = tx.ExecContext(ctx, `UPDATE specialist_tool_calls SET started_at=? WHERE agent_attempt_id=? AND call_id=? AND status='pending' AND started_at IS NULL`, ts(now), ref.AttemptID, callID); err != nil {
			return c, err
		}
	}
	return c, tx.Commit()
}

// ResumeSpecialistToolAttempt transfers only the execution lease of an
// intentionally yielded approval wait. Intent, authority, rounds and usage stay.
func (s *SQLiteStore) ResumeSpecialistToolAttempt(ctx context.Context, ref domain.AgentAttemptRef, lease domain.RunExecutionLease) (domain.AgentAttempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	defer tx.Rollback()
	if err = ref.Validate(); err != nil {
		return domain.AgentAttempt{}, err
	}
	if lease.RunID != ref.RunID {
		return domain.AgentAttempt{}, errors.New("child resume lease belongs to another Run")
	}
	if err = requireRunExecutionLeaseTx(ctx, tx, ref.RunID, lease.LeaseID, lease.Generation); err != nil {
		return domain.AgentAttempt{}, err
	}
	a, err := scanAgentAttempt(tx.QueryRowContext(ctx, agentAttemptSelect+` WHERE id=? AND run_id=? AND agent_id=?`, ref.AttemptID, ref.RunID, ref.AgentID))
	if err != nil {
		return a, err
	}
	var live int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_nodes n JOIN runs r ON r.id=n.run_id WHERE n.id=? AND n.run_id=? AND n.role='specialist' AND n.status='running' AND n.active_attempt_id=? AND n.worker_profile_json<>'null' AND r.status='running'`, ref.AgentID, ref.RunID, ref.AttemptID).Scan(&live)
	if err != nil {
		return a, err
	}
	if live != 1 || a.Status != domain.AgentAttemptRunning {
		return a, errors.New("child approval attempt is no longer active")
	}
	var waiting int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_calls c JOIN tool_approvals p ON p.id=c.approval_id WHERE c.agent_attempt_id=? AND c.status='pending' AND c.started_at IS NULL AND (p.proposal_id=c.approval_source_id OR (c.name IN ('workspace_apply','workspace_delete') AND p.proposal_id=json_extract(c.payload_json,'$.edit_id') AND p.action_class='workspace_write')) AND p.status IN ('approved','denied')`, ref.AttemptID).Scan(&waiting)
	if err != nil {
		return a, err
	}
	if waiting == 0 {
		return a, apperror.New(apperror.CodeFailedPrecondition, "child approval is still pending")
	}
	if a.LeaseID != lease.LeaseID || a.LeaseGeneration != lease.Generation {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_attempts SET lease_id=?,lease_generation=?,updated_at=? WHERE id=? AND status='running'`, lease.LeaseID, lease.Generation, ts(time.Now().UTC()), a.ID); err != nil {
			return a, err
		}
		a.LeaseID = lease.LeaseID
		a.LeaseGeneration = lease.Generation
	}
	return a, tx.Commit()
}

func specialistApprovalRunBindingTx(ctx context.Context, tx *sql.Tx, sessionID string) (runBinding, bool, error) {
	var b runBinding
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.mission_id,m.workspace_id FROM agent_nodes n JOIN runs r ON r.id=n.run_id JOIN missions m ON m.id=r.mission_id WHERE n.session_id=? AND n.role='specialist'`, strings.TrimSpace(sessionID)).Scan(&b.RunID, &b.MissionID, &b.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return b, false, nil
	}
	return b, err == nil, err
}
