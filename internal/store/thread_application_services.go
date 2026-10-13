package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// ListThreadCommandRuntimeServiceMetadata does not read command intent, output,
// environment values, paths, or process IDs. The extra row proves truncation.
func (s *SQLiteStore) ListThreadCommandRuntimeServiceMetadata(ctx context.Context,
	threadID string, limit int,
) ([]runner.CommandRuntimeServiceMetadata, error) {
	if !domain.ValidAgentID(threadID) || limit < 1 || limit > 51 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "Thread service page is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT job.id,job.operation_digest,
		job.request_fingerprint,job.invocation_id,job.run_id,job.mission_id,
		job.session_id,job.workspace_id,job.root_agent_id,job.workspace_root_sha256,
		job.spec_fingerprint,job.owner_id,job.owner_generation,
		job.adapter_kind,job.adapter_backend,job.adapter_backend_identity,job.adapter_generation,
		job.adapter_isolation_grade,job.adapter_network_policy,job.adapter_credential_policy,
		job.state,job.exit_code,job.created_at,job.started_at,job.completed_at
		FROM command_runtime_jobs job JOIN thread_runs binding ON binding.run_id=job.run_id
		WHERE binding.thread_id=? ORDER BY job.created_at DESC,job.id LIMIT ?`, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]runner.CommandRuntimeServiceMetadata, 0, limit)
	for rows.Next() {
		var value runner.CommandRuntimeServiceMetadata
		id := &value.Identity
		var exitCode sql.NullInt64
		var created string
		var started, completed sql.NullString
		err := rows.Scan(&id.ID, &id.OperationDigest, &id.RequestFingerprint, &id.InvocationID,
			&id.RunID, &id.MissionID, &id.SessionID, &id.WorkspaceID, &id.RootAgentID,
			&id.WorkspaceRootSHA256, &id.SpecFingerprint, &id.OwnerID, &id.OwnerGeneration,
			&id.Adapter.Kind, &id.Adapter.Backend, &id.Adapter.BackendIdentity, &id.Adapter.Generation,
			&id.Adapter.IsolationGrade, &id.Adapter.NetworkPolicy, &id.Adapter.CredentialPolicy,
			&value.State, &exitCode, &created, &started, &completed)
		if err != nil {
			return nil, err
		}
		if exitCode.Valid {
			code := int(exitCode.Int64)
			value.ExitCode = &code
		}
		value.CreatedAt, value.StartedAt, value.CompletedAt = parseTS(created), parseNullableTS(started), parseNullableTS(completed)
		values = append(values, value)
	}
	return values, rows.Err()
}

// FindThreadCommandRuntimeStartCall proves source by the existing actor ledger
// and deterministic operation digest. InvocationID is a budget charge identity,
// not a Supervisor call ID. Missing, ambiguous, or truncated source stays unknown.
func (s *SQLiteStore) FindThreadCommandRuntimeStartCall(ctx context.Context,
	threadID string, identity runner.CommandRuntimeJobIdentity,
) (domain.SupervisorToolCall, bool, error) {
	if !domain.ValidAgentID(threadID) || !domain.ValidAgentID(identity.ID) {
		return domain.SupervisorToolCall{}, false, apperror.New(apperror.CodeInvalidArgument, "Thread service source is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT call.call_id,call.run_id,call.turn,
		call.attempt_id,call.payload_json FROM run_supervisor_tool_calls call
		JOIN run_supervisor_tool_call_agents source ON source.run_id=call.run_id
			AND source.turn=call.turn AND source.attempt_id=call.attempt_id AND source.call_id=call.call_id
		JOIN command_runtime_job_agents actor ON actor.run_id=source.run_id
			AND actor.agent_id=source.agent_id AND actor.agent_attempt_id=source.agent_attempt_id
		JOIN command_runtime_jobs job ON job.id=actor.job_id AND job.run_id=actor.run_id
		JOIN thread_runs binding ON binding.run_id=job.run_id
		WHERE binding.thread_id=? AND job.id=? AND actor.agent_id=job.root_agent_id
			AND call.tool_name='command_runtime'
			AND json_extract(call.payload_json,'$.action')='start'
		ORDER BY call.turn,call.round,call.position LIMIT ?`,
		threadID, identity.ID, domain.MaxSupervisorToolRounds*domain.MaxSupervisorToolCallsPerRound+1)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	var candidates []domain.SupervisorToolCall
	for rows.Next() {
		var call domain.SupervisorToolCall
		if err = rows.Scan(&call.CallID, &call.RunID, &call.Turn, &call.AttemptID, &call.PayloadJSON); err != nil {
			break
		}
		candidates = append(candidates, call)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil || readErr != nil {
		return domain.SupervisorToolCall{}, false, errors.Join(err, readErr)
	}
	if len(candidates) > domain.MaxSupervisorToolRounds*domain.MaxSupervisorToolCallsPerRound {
		return domain.SupervisorToolCall{}, false, nil
	}
	var matched domain.SupervisorToolCall
	count := 0
	for _, candidate := range candidates {
		input, canonical, err := toolgateway.NormalizeCommandRuntimePayload(json.RawMessage(candidate.PayloadJSON))
		if err != nil || input.Action != toolgateway.CommandRuntimeActionStart || string(canonical) != candidate.PayloadJSON {
			continue
		}
		keys := []string{
			runmutation.SupervisorToolExecutionKey(candidate.RunID, candidate.Turn, string(toolgateway.CommandRuntimeTool), candidate.PayloadJSON, candidate.CallID),
			// Previously persisted Jobs retain the payload-based operation identity.
			runmutation.SupervisorToolOperationKey(candidate.RunID, candidate.Turn, string(toolgateway.CommandRuntimeTool), candidate.PayloadJSON),
		}
		for _, key := range keys {
			digest, jobID := runner.CommandRuntimeOperationIdentity(candidate.RunID, key)
			if candidate.RunID == identity.RunID && digest == identity.OperationDigest && jobID == identity.ID {
				matched = candidate
				count++
				break
			}
		}
	}
	if count != 1 {
		return domain.SupervisorToolCall{}, false, nil
	}
	call, err := s.GetThreadSupervisorToolCall(ctx, threadID, matched.CallID)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	if call.RunID != matched.RunID || call.AttemptID != matched.AttemptID || call.Turn != matched.Turn || call.PayloadJSON != matched.PayloadJSON {
		return domain.SupervisorToolCall{}, false, nil
	}
	return call, true, nil
}
