package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const readOnlyFanoutPlanSelect = `SELECT id, run_id, workspace_id, scope_path, goal,
	protocol_version, requested_tier, effective_parallelism, status,
	capability_fingerprint, snapshot_digest, file_count, total_bytes, excluded_count,
	shard_count, requested_by, version, created_at, updated_at
	FROM readonly_fanout_plans`

const readOnlyFanoutFileSelect = `SELECT plan_id, ordinal, shard_ordinal, relative_path,
	size_bytes, content_sha256 FROM readonly_fanout_files`

const readOnlyFanoutShardSelect = `SELECT plan_id, ordinal, status, file_count,
	total_bytes, input_digest, version, created_at, updated_at FROM readonly_fanout_shards`

type readOnlyFanoutQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *SQLiteStore) CreateReadOnlyFanoutPlan(ctx context.Context,
	plan domain.ReadOnlyFanoutPlan, operation domain.ReadOnlyFanoutOperation,
	policyEvent events.Event, plannedEvent events.Event,
) (domain.ReadOnlyFanoutPlan, bool, error) {
	plan = normalizeReadOnlyFanoutPlan(plan)
	operation = normalizeReadOnlyFanoutOperation(operation)
	if err := validateReadOnlyFanoutPlanMutation(plan, operation, policyEvent,
		plannedEvent); err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireReadOnlyFanoutWriteLockTx(ctx, tx, plan.RunID); err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	existing, found, err := getReadOnlyFanoutOperation(ctx, tx, operation.KeyDigest)
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	if found {
		if err := validateReadOnlyFanoutReplay(existing, operation); err != nil {
			return domain.ReadOnlyFanoutPlan{}, false, err
		}
		stored, err := getReadOnlyFanoutPlan(ctx, tx, existing.PlanID)
		if err != nil {
			return domain.ReadOnlyFanoutPlan{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutPlan{}, false, err
		}
		return stored, true, nil
	}
	run, mission, err := requireReadOnlyFanoutPlanBindingTx(ctx, tx, plan)
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	for _, event := range []events.Event{policyEvent, plannedEvent} {
		if event.RunID != run.ID || event.MissionID != mission.ID ||
			!event.CreatedAt.Equal(plan.CreatedAt) {
			return domain.ReadOnlyFanoutPlan{}, false, apperror.New(
				apperror.CodeInvalidArgument,
				"read-only fan-out event scope or timestamp does not match its plan")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_plans
		(id, run_id, workspace_id, scope_path, goal, protocol_version, requested_tier,
		effective_parallelism, status, capability_fingerprint, snapshot_digest,
		file_count, total_bytes, excluded_count, shard_count, requested_by, version,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.RunID, plan.WorkspaceID, plan.ScopePath, plan.Goal,
		plan.ProtocolVersion, plan.RequestedTier, plan.EffectiveParallelism, plan.Status,
		plan.CapabilityFingerprint, plan.SnapshotDigest, plan.FileCount, plan.TotalBytes,
		plan.ExcludedCount, plan.ShardCount, plan.RequestedBy, plan.Version,
		ts(plan.CreatedAt), ts(plan.UpdatedAt)); err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	for _, file := range plan.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_files
			(plan_id, ordinal, shard_ordinal, relative_path, size_bytes, content_sha256)
			VALUES (?, ?, ?, ?, ?, ?)`, file.PlanID, file.Ordinal, file.ShardOrdinal,
			file.RelativePath, file.SizeBytes, file.ContentSHA256); err != nil {
			return domain.ReadOnlyFanoutPlan{}, false, err
		}
	}
	for _, shard := range plan.Shards {
		if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_shards
			(plan_id, ordinal, status, file_count, total_bytes, input_digest, version,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, shard.PlanID,
			shard.Ordinal, shard.Status, shard.FileCount, shard.TotalBytes,
			shard.InputDigest, shard.Version, ts(shard.CreatedAt), ts(shard.UpdatedAt)); err != nil {
			return domain.ReadOnlyFanoutPlan{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_operations
		(operation_key_digest, request_fingerprint, plan_id, run_id, workspace_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.PlanID, operation.RunID,
		operation.WorkspaceID, operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverReadOnlyFanoutPlan(ctx, operation, err)
	}
	for _, event := range []events.Event{policyEvent, plannedEvent} {
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return domain.ReadOnlyFanoutPlan{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	return plan, false, nil
}

func acquireReadOnlyFanoutWriteLockTx(ctx context.Context, tx *sql.Tx,
	runID string,
) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE runs SET updated_at = updated_at WHERE id = ?`, runID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeNotFound, "read-only fan-out Run was not found")
	}
	return nil
}

func (s *SQLiteStore) GetReadOnlyFanoutPlan(ctx context.Context,
	id string,
) (domain.ReadOnlyFanoutPlan, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) {
		return domain.ReadOnlyFanoutPlan{}, apperror.New(
			apperror.CodeInvalidArgument, "read-only fan-out plan id is invalid")
	}
	return getReadOnlyFanoutPlan(ctx, s.db, id)
}

func (s *SQLiteStore) ListReadOnlyFanoutPlans(ctx context.Context,
	runID string, limit int,
) ([]domain.ReadOnlyFanoutPlan, error) {
	if limit <= 0 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out list limit must be between 1 and 100")
	}
	return s.ListReadOnlyFanoutPlansPage(ctx, runID, 0, limit)
}

func (s *SQLiteStore) ListReadOnlyFanoutPlansPage(ctx context.Context,
	runID string, offset int, limit int,
) ([]domain.ReadOnlyFanoutPlan, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || offset < 0 || limit <= 0 || limit > 101 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out page requires a valid Run and bounded offset/limit")
	}
	rows, err := s.db.QueryContext(ctx, readOnlyFanoutPlanSelect+
		` WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		runID, limit, offset)
	if err != nil {
		return nil, err
	}
	plans := make([]domain.ReadOnlyFanoutPlan, 0)
	for rows.Next() {
		plan, err := scanReadOnlyFanoutPlan(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range plans {
		if err := loadReadOnlyFanoutPlanChildren(ctx, s.db, &plans[index]); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func (s *SQLiteStore) ListReadOnlyFanoutPlanSummariesPage(ctx context.Context,
	runID string, offset int, limit int,
) ([]domain.ReadOnlyFanoutPlanSummary, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || offset < 0 || limit <= 0 || limit > 101 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out summary page requires a valid Run and bounded offset/limit")
	}
	rows, err := s.db.QueryContext(ctx, readOnlyFanoutPlanSelect+
		` WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		runID, limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([]domain.ReadOnlyFanoutPlanSummary, 0)
	for rows.Next() {
		plan, err := scanReadOnlyFanoutPlan(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		summary := plan.Summary()
		if err := summary.Validate(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		result = append(result, summary)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SQLiteStore) GetReadOnlyFanoutOperation(ctx context.Context,
	keyDigest string,
) (domain.ReadOnlyFanoutOperation, bool, error) {
	keyDigest = strings.TrimSpace(keyDigest)
	if !validStoreDigest(keyDigest) {
		return domain.ReadOnlyFanoutOperation{}, false, apperror.New(
			apperror.CodeInvalidArgument, "read-only fan-out operation digest is invalid")
	}
	return getReadOnlyFanoutOperation(ctx, s.db, keyDigest)
}

func normalizeReadOnlyFanoutPlan(plan domain.ReadOnlyFanoutPlan,
) domain.ReadOnlyFanoutPlan {
	plan.ID = strings.TrimSpace(plan.ID)
	plan.RunID = strings.TrimSpace(plan.RunID)
	plan.WorkspaceID = strings.TrimSpace(plan.WorkspaceID)
	plan.Goal = strings.TrimSpace(redact.String(plan.Goal))
	plan.ProtocolVersion = strings.TrimSpace(plan.ProtocolVersion)
	plan.RequestedTier = domain.ReadOnlyFanoutTier(strings.TrimSpace(
		string(plan.RequestedTier)))
	plan.Status = domain.ReadOnlyFanoutStatus(strings.TrimSpace(string(plan.Status)))
	plan.CapabilityFingerprint = strings.TrimSpace(plan.CapabilityFingerprint)
	plan.SnapshotDigest = strings.TrimSpace(plan.SnapshotDigest)
	plan.RequestedBy = strings.TrimSpace(redact.String(plan.RequestedBy))
	plan.CreatedAt = plan.CreatedAt.UTC()
	plan.UpdatedAt = plan.UpdatedAt.UTC()
	for index := range plan.Files {
		plan.Files[index].PlanID = strings.TrimSpace(plan.Files[index].PlanID)
		plan.Files[index].ContentSHA256 = strings.TrimSpace(
			plan.Files[index].ContentSHA256)
	}
	for index := range plan.Shards {
		plan.Shards[index].PlanID = strings.TrimSpace(plan.Shards[index].PlanID)
		plan.Shards[index].Status = domain.ReadOnlyFanoutShardStatus(strings.TrimSpace(
			string(plan.Shards[index].Status)))
		plan.Shards[index].InputDigest = strings.TrimSpace(plan.Shards[index].InputDigest)
		plan.Shards[index].CreatedAt = plan.Shards[index].CreatedAt.UTC()
		plan.Shards[index].UpdatedAt = plan.Shards[index].UpdatedAt.UTC()
	}
	return plan
}

func normalizeReadOnlyFanoutOperation(operation domain.ReadOnlyFanoutOperation,
) domain.ReadOnlyFanoutOperation {
	operation.KeyDigest = strings.TrimSpace(operation.KeyDigest)
	operation.RequestFingerprint = strings.TrimSpace(operation.RequestFingerprint)
	operation.PlanID = strings.TrimSpace(operation.PlanID)
	operation.RunID = strings.TrimSpace(operation.RunID)
	operation.WorkspaceID = strings.TrimSpace(operation.WorkspaceID)
	operation.RequestedBy = strings.TrimSpace(redact.String(operation.RequestedBy))
	operation.CreatedAt = operation.CreatedAt.UTC()
	return operation
}

func validateReadOnlyFanoutPlanMutation(plan domain.ReadOnlyFanoutPlan,
	operation domain.ReadOnlyFanoutOperation, policyEvent events.Event,
	plannedEvent events.Event,
) error {
	if err := plan.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"read-only fan-out plan is invalid", err)
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"read-only fan-out operation is invalid", err)
	}
	if operation.PlanID != plan.ID || operation.RunID != plan.RunID ||
		operation.WorkspaceID != plan.WorkspaceID ||
		operation.RequestedBy != plan.RequestedBy ||
		!operation.CreatedAt.Equal(plan.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out operation does not match its plan")
	}
	expectedFingerprint := runmutation.Fingerprint("readonly_fanout_plan_request.v1",
		plan.RunID, plan.WorkspaceID, plan.ScopePath, plan.Goal,
		string(plan.RequestedTier), plan.RequestedBy)
	if operation.RequestFingerprint != expectedFingerprint {
		return apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out request fingerprint is invalid")
	}
	if err := validateReadOnlyFanoutPolicyEvent(policyEvent, plan); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"read-only fan-out Policy event is invalid", err)
	}
	if err := validateReadOnlyFanoutPlannedEvent(plannedEvent, plan); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"read-only fan-out planned event is invalid", err)
	}
	return nil
}

func validateReadOnlyFanoutPolicyEvent(event events.Event,
	plan domain.ReadOnlyFanoutPlan,
) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != events.PolicyDecisionEvent || event.Source != "readonly_fanout" ||
		event.SubjectID != plan.ID {
		return errors.New("read-only fan-out Policy event identity is invalid")
	}
	var payload struct {
		Context             string `json:"context"`
		Allowed             *bool  `json:"allowed"`
		NeedsApproval       *bool  `json:"needs_approval"`
		Risk                string `json:"risk"`
		Reason              string `json:"reason"`
		Capability          string `json:"capability"`
		ExecutionAuthorized *bool  `json:"execution_authorized"`
	}
	if err := decodeStrictReadOnlyFanoutEvent(event.PayloadJSON, &payload); err != nil {
		return err
	}
	if payload.Context != "readonly_fanout_plan" || payload.Allowed == nil ||
		!*payload.Allowed || payload.NeedsApproval == nil || *payload.NeedsApproval ||
		payload.Capability != "workspace_readonly" || payload.ExecutionAuthorized == nil ||
		*payload.ExecutionAuthorized || strings.TrimSpace(payload.Reason) == "" {
		return errors.New("read-only fan-out Policy event does not authorize planning only")
	}
	return nil
}

func validateReadOnlyFanoutPlannedEvent(event events.Event,
	plan domain.ReadOnlyFanoutPlan,
) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != events.ReadOnlyFanoutPlannedEvent || event.Source != "readonly_fanout" ||
		event.SubjectID != plan.ID {
		return errors.New("read-only fan-out planned event identity is invalid")
	}
	var payload struct {
		PlanID                string                    `json:"plan_id"`
		Protocol              string                    `json:"protocol"`
		RequestedTier         domain.ReadOnlyFanoutTier `json:"requested_tier"`
		EffectiveParallelism  int                       `json:"effective_parallelism"`
		FileCount             int                       `json:"file_count"`
		TotalBytes            int64                     `json:"total_bytes"`
		ExcludedCount         int                       `json:"excluded_count"`
		ShardCount            int                       `json:"shard_count"`
		SnapshotDigest        string                    `json:"snapshot_digest"`
		CapabilityFingerprint string                    `json:"capability_fingerprint"`
		Shell                 *bool                     `json:"shell"`
		FileWrite             *bool                     `json:"file_write"`
		Network               *bool                     `json:"network"`
		ChildSpawn            *bool                     `json:"child_spawn"`
		ExecutionAuthorized   *bool                     `json:"execution_authorized"`
	}
	if err := decodeStrictReadOnlyFanoutEvent(event.PayloadJSON, &payload); err != nil {
		return err
	}
	if payload.PlanID != plan.ID || payload.Protocol != plan.ProtocolVersion ||
		payload.RequestedTier != plan.RequestedTier ||
		payload.EffectiveParallelism != plan.EffectiveParallelism ||
		payload.FileCount != plan.FileCount || payload.TotalBytes != plan.TotalBytes ||
		payload.ExcludedCount != plan.ExcludedCount || payload.ShardCount != plan.ShardCount ||
		payload.SnapshotDigest != plan.SnapshotDigest ||
		payload.CapabilityFingerprint != plan.CapabilityFingerprint ||
		payload.Shell == nil || *payload.Shell || payload.FileWrite == nil ||
		*payload.FileWrite || payload.Network == nil || *payload.Network ||
		payload.ChildSpawn == nil || *payload.ChildSpawn ||
		payload.ExecutionAuthorized == nil || *payload.ExecutionAuthorized {
		return errors.New("read-only fan-out planned event does not match its plan")
	}
	return nil
}

func decodeStrictReadOnlyFanoutEvent(payloadJSON string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("read-only fan-out event contains trailing data")
	}
	return nil
}

func requireReadOnlyFanoutPlanBindingTx(ctx context.Context, tx *sql.Tx,
	plan domain.ReadOnlyFanoutPlan,
) (domain.Run, domain.Mission, error) {
	run, mission, err := getCoordinatorRunTx(ctx, tx, plan.RunID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, plan.RunID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if run.Status != domain.RunRunning || mission.WorkspaceID != plan.WorkspaceID ||
		mode.Scope.WorkspaceID != plan.WorkspaceID ||
		mode.Scope.NetworkMode != "disabled" || plan.CreatedAt.Before(run.CreatedAt) {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out plan requires its running local-workspace Run")
	}
	var bindingCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions active_session
		JOIN workspaces workspace ON workspace.id = ?
		WHERE active_session.id = ? AND active_session.workspace_id = workspace.id
			AND active_session.status = ?`, plan.WorkspaceID, run.SessionID,
		session.StatusActive).Scan(&bindingCount); err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if bindingCount != 1 {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out plan requires an active workspace-bound Session")
	}
	return run, mission, nil
}

func validateReadOnlyFanoutReplay(existing,
	request domain.ReadOnlyFanoutOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.RunID != request.RunID || existing.WorkspaceID != request.WorkspaceID ||
		existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"read-only fan-out operation key was already used for different intent")
	}
	return nil
}

func (s *SQLiteStore) recoverReadOnlyFanoutPlan(ctx context.Context,
	operation domain.ReadOnlyFanoutOperation, original error,
) (domain.ReadOnlyFanoutPlan, bool, error) {
	existing, found, err := getReadOnlyFanoutOperation(ctx, s.db, operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.ReadOnlyFanoutPlan{}, false, original
		}
		return domain.ReadOnlyFanoutPlan{}, false, errors.Join(original, err)
	}
	if err := validateReadOnlyFanoutReplay(existing, operation); err != nil {
		return domain.ReadOnlyFanoutPlan{}, false, err
	}
	plan, err := s.GetReadOnlyFanoutPlan(ctx, existing.PlanID)
	return plan, true, err
}

func getReadOnlyFanoutOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.ReadOnlyFanoutOperation, bool, error) {
	var operation domain.ReadOnlyFanoutOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest, request_fingerprint,
		plan_id, run_id, workspace_id, requested_by, created_at
		FROM readonly_fanout_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&operation.KeyDigest, &operation.RequestFingerprint, &operation.PlanID,
			&operation.RunID, &operation.WorkspaceID, &operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReadOnlyFanoutOperation{}, false, nil
	}
	if err != nil {
		return domain.ReadOnlyFanoutOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

func getReadOnlyFanoutPlan(ctx context.Context, queryer readOnlyFanoutQueryer,
	id string,
) (domain.ReadOnlyFanoutPlan, error) {
	plan, err := scanReadOnlyFanoutPlan(queryer.QueryRowContext(ctx,
		readOnlyFanoutPlanSelect+` WHERE id = ?`, id))
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	if err := loadReadOnlyFanoutPlanChildren(ctx, queryer, &plan); err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	return plan, nil
}

func loadReadOnlyFanoutPlanChildren(ctx context.Context, queryer readOnlyFanoutQueryer,
	plan *domain.ReadOnlyFanoutPlan,
) error {
	files, err := listReadOnlyFanoutFiles(ctx, queryer, plan.ID)
	if err != nil {
		return err
	}
	shards, err := listReadOnlyFanoutShards(ctx, queryer, plan.ID)
	if err != nil {
		return err
	}
	plan.Files = files
	plan.Shards = shards
	return plan.Validate()
}

func scanReadOnlyFanoutPlan(row scanner) (domain.ReadOnlyFanoutPlan, error) {
	var plan domain.ReadOnlyFanoutPlan
	var requestedTier, status, createdAt, updatedAt string
	err := row.Scan(&plan.ID, &plan.RunID, &plan.WorkspaceID, &plan.ScopePath,
		&plan.Goal, &plan.ProtocolVersion, &requestedTier, &plan.EffectiveParallelism,
		&status, &plan.CapabilityFingerprint, &plan.SnapshotDigest, &plan.FileCount,
		&plan.TotalBytes, &plan.ExcludedCount, &plan.ShardCount, &plan.RequestedBy,
		&plan.Version, &createdAt, &updatedAt)
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	plan.RequestedTier = domain.ReadOnlyFanoutTier(requestedTier)
	plan.Status = domain.ReadOnlyFanoutStatus(status)
	plan.CreatedAt = parseTS(createdAt)
	plan.UpdatedAt = parseTS(updatedAt)
	return plan, nil
}

func listReadOnlyFanoutFiles(ctx context.Context, queryer readOnlyFanoutQueryer,
	planID string,
) ([]domain.ReadOnlyFanoutFile, error) {
	rows, err := queryer.QueryContext(ctx, readOnlyFanoutFileSelect+
		` WHERE plan_id = ? ORDER BY ordinal`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]domain.ReadOnlyFanoutFile, 0)
	for rows.Next() {
		var file domain.ReadOnlyFanoutFile
		if err := rows.Scan(&file.PlanID, &file.Ordinal, &file.ShardOrdinal,
			&file.RelativePath, &file.SizeBytes, &file.ContentSHA256); err != nil {
			return nil, err
		}
		if err := file.Validate(); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func listReadOnlyFanoutShards(ctx context.Context, queryer readOnlyFanoutQueryer,
	planID string,
) ([]domain.ReadOnlyFanoutShard, error) {
	rows, err := queryer.QueryContext(ctx, readOnlyFanoutShardSelect+
		` WHERE plan_id = ? ORDER BY ordinal`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shards := make([]domain.ReadOnlyFanoutShard, 0)
	for rows.Next() {
		var shard domain.ReadOnlyFanoutShard
		var status, createdAt, updatedAt string
		if err := rows.Scan(&shard.PlanID, &shard.Ordinal, &status, &shard.FileCount,
			&shard.TotalBytes, &shard.InputDigest, &shard.Version, &createdAt,
			&updatedAt); err != nil {
			return nil, err
		}
		shard.Status = domain.ReadOnlyFanoutShardStatus(status)
		shard.CreatedAt = parseTS(createdAt)
		shard.UpdatedAt = parseTS(updatedAt)
		if err := shard.Validate(); err != nil {
			return nil, err
		}
		shards = append(shards, shard)
	}
	return shards, rows.Err()
}

func validStoreDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}
