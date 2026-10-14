package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/mcp"
)

const mcpClientServerColumns = `protocol_version, descriptor_json,
	descriptor_fingerprint, state, capability_json, approved_capability_fingerprint,
	health, health_message, discovery_lease_id, discovery_lease_expires_at,
	generation, reviewed_by, reviewed_at, created_at, updated_at`

func (s *SQLiteStore) CreateMCPClientServer(ctx context.Context,
	record mcp.ServerRecord,
) (mcp.ServerRecord, bool, error) {
	if err := record.Validate(); err != nil || record.State != mcp.TrustStaged ||
		record.Generation != 1 {
		return mcp.ServerRecord{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"MCP client server record is invalid", err)
	}
	descriptorJSON, err := json.Marshal(record.Descriptor)
	if err != nil {
		return mcp.ServerRecord{}, false, err
	}
	// This denormalized column is a locator, not an execution input. Native
	// registrations resolve the acquired object referenced by descriptor_json;
	// never copy its original command, endpoint, env or headers into this row.
	target := record.Descriptor.Target
	if record.Descriptor.NativeSource != nil {
		target = "plugin-object:" + record.DescriptorFingerprint
	}
	capabilityJSON := []byte(`{}`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mcp.ServerRecord{}, false, err
	}
	defer tx.Rollback()
	existing, loadErr := getMCPClientServer(ctx, tx, record.Descriptor.ID)
	if loadErr == nil {
		if existing.DescriptorFingerprint != record.DescriptorFingerprint {
			return mcp.ServerRecord{}, false, apperror.New(apperror.CodeConflict,
				"MCP server identity is already bound to another descriptor")
		}
		return existing, true, nil
	}
	if !errors.Is(loadErr, sql.ErrNoRows) {
		return mcp.ServerRecord{}, false, loadErr
	}
	var workspaceExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE id = ?`,
		record.Descriptor.WorkspaceID).Scan(&workspaceExists); err != nil || workspaceExists != 1 {
		return mcp.ServerRecord{}, false, apperror.New(apperror.CodeNotFound,
			"MCP server Workspace scope does not exist")
	}
	if record.Descriptor.Scope == mcp.ScopeRun {
		var runWorkspace string
		if err := tx.QueryRowContext(ctx, `SELECT missions.workspace_id FROM runs
			JOIN missions ON missions.id = runs.mission_id WHERE runs.id = ?`,
			record.Descriptor.RunID).Scan(&runWorkspace); err != nil {
			return mcp.ServerRecord{}, false, err
		}
		if runWorkspace != record.Descriptor.WorkspaceID {
			return mcp.ServerRecord{}, false, apperror.New(apperror.CodeConflict,
				"MCP Run and Workspace scopes do not match")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_client_servers
		(id, protocol_version, name, transport, target, scope, run_id, workspace_id,
		descriptor_json, descriptor_fingerprint, state, capability_json,
		capability_fingerprint, approved_capability_fingerprint, health,
		health_message, discovery_lease_id, discovery_lease_expires_at, generation,
		reviewed_by, reviewed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, '', '', NULL, ?, '', NULL, ?, ?)`,
		record.Descriptor.ID, record.ProtocolVersion, record.Descriptor.Name,
		record.Descriptor.Transport, target, record.Descriptor.Scope,
		record.Descriptor.RunID, record.Descriptor.WorkspaceID, string(descriptorJSON),
		record.DescriptorFingerprint, record.State, string(capabilityJSON), record.Health,
		record.Generation, ts(record.CreatedAt), ts(record.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return mcp.ServerRecord{}, false, apperror.Wrap(apperror.CodeConflict,
				"MCP client server already exists or the server limit was reached", err)
		}
		return mcp.ServerRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return mcp.ServerRecord{}, false, err
	}
	return record, false, nil
}

func (s *SQLiteStore) GetMCPClientServer(ctx context.Context, id string) (mcp.ServerRecord, error) {
	value, err := getMCPClientServer(ctx, s.db, strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return mcp.ServerRecord{}, apperror.New(apperror.CodeNotFound,
			"MCP client server not found")
	}
	return value, err
}

// All durable registrations count, including other Workspaces and disabled
// entries: the OS credential name is shared independently of execution state.
func (s *SQLiteStore) ListMCPClientServersByCredentialRef(ctx context.Context, reference string) ([]mcp.ServerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mcpClientServerColumns+`
		FROM mcp_client_servers WHERE json_extract(descriptor_json, '$.credential_ref') = ?
		OR EXISTS (SELECT 1 FROM json_each(descriptor_json, '$.env_credential_refs') WHERE value = ?) ORDER BY id`, reference, reference)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mcp.ServerRecord{}
	for rows.Next() {
		value, err := scanMCPClientServer(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) ListMCPClientServers(ctx context.Context, runID, workspaceID string,
	limit int,
) ([]mcp.ServerRecord, error) {
	runID, workspaceID = strings.TrimSpace(runID), strings.TrimSpace(workspaceID)
	if workspaceID == "" || limit < 1 || limit > mcp.MaxClientServers {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"MCP client server list scope is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+mcpClientServerColumns+`
		FROM mcp_client_servers WHERE workspace_id = ?
		AND (scope = 'workspace' OR run_id = ?)
		ORDER BY updated_at DESC, id LIMIT ?`, workspaceID, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]mcp.ServerRecord, 0, limit)
	for rows.Next() {
		value, err := scanMCPClientServer(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) ListRecoverableMCPClientServers(ctx context.Context,
	limit int,
) ([]mcp.ServerRecord, error) {
	if limit < 1 || limit > mcp.MaxClientServers {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"MCP recovery list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+mcpClientServerColumns+`
		FROM mcp_client_servers WHERE health = 'connecting'
		ORDER BY updated_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]mcp.ServerRecord, 0, limit)
	for rows.Next() {
		value, scanErr := scanMCPClientServer(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) UpdateMCPClientServer(ctx context.Context, record mcp.ServerRecord,
	expectedGeneration int64,
) (mcp.ServerRecord, error) {
	if err := record.Validate(); err != nil || expectedGeneration < 1 ||
		record.Generation != expectedGeneration+1 {
		return mcp.ServerRecord{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"MCP client server update is invalid", err)
	}
	descriptorJSON, _ := json.Marshal(record.Descriptor)
	capabilityJSON := []byte(`{}`)
	capabilityFingerprint := ""
	if record.Capabilities.Fingerprint != "" {
		capabilityJSON, _ = json.Marshal(record.Capabilities)
		capabilityFingerprint = record.Capabilities.Fingerprint
	}
	var reviewedAt any
	if record.ReviewedAt != nil {
		reviewedAt = ts(*record.ReviewedAt)
	}
	var discoveryLeaseExpiresAt any
	if record.DiscoveryLeaseExpiresAt != nil {
		discoveryLeaseExpiresAt = ts(*record.DiscoveryLeaseExpiresAt)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mcp_client_servers SET
		state = ?, capability_json = ?, capability_fingerprint = ?,
		approved_capability_fingerprint = ?, health = ?, health_message = ?,
		discovery_lease_id = ?, discovery_lease_expires_at = ?, generation = ?,
		reviewed_by = ?, reviewed_at = ?, updated_at = ?
		WHERE id = ? AND descriptor_json = ? AND descriptor_fingerprint = ?
		AND generation = ?`, record.State, string(capabilityJSON), capabilityFingerprint,
		record.ApprovedCapabilityFingerprint, record.Health, record.HealthMessage,
		record.DiscoveryLeaseID, discoveryLeaseExpiresAt, record.Generation,
		record.ReviewedBy, reviewedAt, ts(record.UpdatedAt),
		record.Descriptor.ID, string(descriptorJSON), record.DescriptorFingerprint,
		expectedGeneration)
	if err != nil {
		return mcp.ServerRecord{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return mcp.ServerRecord{}, err
	}
	if changed != 1 {
		return mcp.ServerRecord{}, apperror.New(apperror.CodeConflict,
			"MCP client server changed concurrently")
	}
	return s.GetMCPClientServer(ctx, record.Descriptor.ID)
}

func (s *SQLiteStore) RecordMCPClientCall(ctx context.Context, audit mcp.CallAudit) error {
	if err := audit.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "MCP call audit is invalid", err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_client_calls
		(id, protocol_version, run_id, workspace_id, server_id, tool_name,
		capability_fingerprint, arguments_sha256, status, error_code, result_bytes,
		truncated, started_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		audit.ID, audit.ProtocolVersion, audit.RunID, audit.WorkspaceID, audit.ServerID,
		audit.ToolName, audit.CapabilityFingerprint, audit.ArgumentsSHA256, audit.Status,
		audit.ErrorCode, audit.ResultBytes, boolInt(audit.Truncated), ts(audit.StartedAt),
		ts(audit.CompletedAt))
	return err
}

func (s *SQLiteStore) ListMCPClientCalls(ctx context.Context, runID string,
	limit int,
) ([]mcp.CallAudit, error) {
	if strings.TrimSpace(runID) == "" || limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"MCP call audit list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT protocol_version, id, run_id,
		workspace_id, server_id, tool_name, capability_fingerprint, arguments_sha256,
		status, error_code, result_bytes, truncated, started_at, completed_at
		FROM mcp_client_calls WHERE run_id = ? ORDER BY completed_at DESC, id DESC LIMIT ?`,
		runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]mcp.CallAudit, 0, limit)
	for rows.Next() {
		var value mcp.CallAudit
		var truncated int
		var startedAt, completedAt string
		if err := rows.Scan(&value.ProtocolVersion, &value.ID, &value.RunID,
			&value.WorkspaceID, &value.ServerID, &value.ToolName,
			&value.CapabilityFingerprint, &value.ArgumentsSHA256, &value.Status,
			&value.ErrorCode, &value.ResultBytes, &truncated, &startedAt,
			&completedAt); err != nil {
			return nil, err
		}
		value.Truncated = truncated != 0
		value.StartedAt, value.CompletedAt = parseTS(startedAt), parseTS(completedAt)
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("stored MCP call audit is invalid: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type mcpClientServerScanner interface{ Scan(...any) error }

func getMCPClientServer(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string,
) (mcp.ServerRecord, error) {
	return scanMCPClientServer(queryer.QueryRowContext(ctx, `SELECT `+
		mcpClientServerColumns+` FROM mcp_client_servers WHERE id = ?`, id))
}

func scanMCPClientServer(scanner mcpClientServerScanner) (mcp.ServerRecord, error) {
	var record mcp.ServerRecord
	var descriptorJSON, capabilityJSON, state, health string
	var discoveryLeaseExpiresAt, reviewedAt sql.NullString
	var createdAt, updatedAt string
	if err := scanner.Scan(&record.ProtocolVersion, &descriptorJSON,
		&record.DescriptorFingerprint, &state, &capabilityJSON,
		&record.ApprovedCapabilityFingerprint, &health, &record.HealthMessage,
		&record.DiscoveryLeaseID, &discoveryLeaseExpiresAt, &record.Generation,
		&record.ReviewedBy, &reviewedAt, &createdAt, &updatedAt); err != nil {
		return mcp.ServerRecord{}, err
	}
	record.State, record.Health = mcp.TrustState(state), mcp.HealthStatus(health)
	record.CreatedAt, record.UpdatedAt = parseTS(createdAt), parseTS(updatedAt)
	if reviewedAt.Valid {
		value := parseTS(reviewedAt.String)
		record.ReviewedAt = &value
	}
	if discoveryLeaseExpiresAt.Valid {
		value := parseTS(discoveryLeaseExpiresAt.String)
		record.DiscoveryLeaseExpiresAt = &value
	}
	if err := json.Unmarshal([]byte(descriptorJSON), &record.Descriptor); err != nil {
		return mcp.ServerRecord{}, err
	}
	if capabilityJSON != "{}" {
		if err := json.Unmarshal([]byte(capabilityJSON), &record.Capabilities); err != nil {
			return mcp.ServerRecord{}, err
		}
	}
	if err := record.Validate(); err != nil {
		return mcp.ServerRecord{}, fmt.Errorf("stored MCP client server is invalid: %w", err)
	}
	return record, nil
}
