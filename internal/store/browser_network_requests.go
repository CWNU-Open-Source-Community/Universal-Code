package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func (s *SQLiteStore) CreateBrowserNetworkRequest(ctx context.Context, v domain.BrowserNetworkRequest) error {
	a, err := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(v.AuthorityJSON))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(v.ID, domain.BrowserNetworkRequestPrefix) || len(v.ID) > 256 || v.RunID != a.RunID || v.SessionID != a.SessionID || v.WorkspaceID != a.WorkspaceID || v.ManagerBootID != a.ManagerBootID || len(a.DeclaredOrigins) == 0 || len(v.Fingerprint) != 64 || len(v.BodySHA256) != 64 || v.BodyBytes < 0 || v.BodyBytes > 1048576 || v.RuntimeID == "" || !v.ExpiresAt.After(time.Now()) || v.ExpiresAt.After(time.Now().Add(3*time.Minute)) {
		return errors.New("invalid paused browser network request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID, workspaceID string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(r.session_id,''),COALESCE(m.workspace_id,'') FROM runs r JOIN missions m ON m.id=r.mission_id WHERE r.id=?`, v.RunID).Scan(&sessionID, &workspaceID); err != nil {
		return err
	}
	if a.Role == domain.AgentRoleSpecialist {
		binding, bound, e := specialistApprovalRunBindingTx(ctx, tx, v.SessionID)
		if e != nil {
			return e
		}
		if !bound || binding.RunID != v.RunID {
			return errors.New("browser network worker ownership changed")
		}
	} else if sessionID != v.SessionID {
		return errors.New("browser network session ownership changed")
	}
	if workspaceID != v.WorkspaceID {
		return errors.New("browser network workspace changed")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO browser_network_requests VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'paused')`, v.ID, v.RunID, v.SessionID, v.WorkspaceID, v.ManagerBootID, v.RuntimeID, v.Fingerprint, v.AuthorityJSON, v.Method, v.DisplayURL, v.BodySHA256, v.BodyBytes, ts(v.ExpiresAt))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, _, err = ensureApprovalTx(ctx, tx, approval.Proposal{IdempotencyKey: approval.ProposalIdempotencyKey(toolgateway.AgentBrowserApprovalTool, v.ID), ProposalID: v.ID, SessionID: v.SessionID, WorkspaceID: v.WorkspaceID, ToolName: toolgateway.AgentBrowserApprovalTool, ActionClass: "browser_external_write", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: v.Fingerprint, DecisionReason: fmt.Sprintf("%s %s (%d body bytes, SHA256 %s)", v.Method, v.DisplayURL, v.BodyBytes, v.BodySHA256), RequestedBy: "agent_browser_network", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return err
	}
	return tx.Commit()
}

const browserNetworkRequestSelect = `SELECT id,run_id,session_id,workspace_id,manager_boot_id,runtime_id,fingerprint,authority_json,method,display_url,body_sha256,body_bytes,expires_at,status FROM browser_network_requests WHERE id=?`

func readBrowserNetworkRequest(row interface{ Scan(...any) error }) (domain.BrowserNetworkRequest, error) {
	var v domain.BrowserNetworkRequest
	var expiry string
	err := row.Scan(&v.ID, &v.RunID, &v.SessionID, &v.WorkspaceID, &v.ManagerBootID, &v.RuntimeID, &v.Fingerprint, &v.AuthorityJSON, &v.Method, &v.DisplayURL, &v.BodySHA256, &v.BodyBytes, &expiry, &v.Status)
	if err != nil {
		return v, err
	}
	v.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	return v, err
}
func (s *SQLiteStore) GetBrowserNetworkRequest(ctx context.Context, id string) (domain.BrowserNetworkRequest, error) {
	return readBrowserNetworkRequest(s.db.QueryRowContext(ctx, browserNetworkRequestSelect, id))
}

func validateBrowserNetworkApprovalTx(ctx context.Context, tx *sql.Tx, record approval.Record) error {
	v, err := readBrowserNetworkRequest(tx.QueryRowContext(ctx, browserNetworkRequestSelect, record.ProposalID))
	if err != nil {
		return err
	}
	if v.Status != "paused" || !v.ExpiresAt.After(time.Now()) || v.RunID != record.RunID || v.SessionID != record.SessionID || v.WorkspaceID != record.WorkspaceID || v.Fingerprint != record.RequestFingerprint || record.GrantID != "" || record.Mode != "per_call" || record.ToolName != toolgateway.AgentBrowserApprovalTool || record.ActionClass != "browser_external_write" {
		return errors.New("browser network request is no longer live")
	}
	return nil
}
func (s *SQLiteStore) ConsumeBrowserNetworkRequest(ctx context.Context, id, fingerprint string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, err := getApprovalTx(ctx, tx, "", id)
	if err != nil {
		return err
	}
	if record.Status != approval.StatusApproved || record.RequestFingerprint != fingerprint {
		return errors.New("exact browser network approval required")
	}
	if err = validateBrowserNetworkApprovalTx(ctx, tx, record); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE browser_network_requests SET status='consumed' WHERE id=? AND fingerprint=? AND status='paused'`, id, fingerprint)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("browser network approval already consumed")
	}
	return tx.Commit()
}
func (s *SQLiteStore) InvalidateBrowserNetworkRequest(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE browser_network_requests SET status='invalidated' WHERE id=? AND status='paused'`, id); err != nil {
		return err
	}
	record, err := getApprovalTx(ctx, tx, "", id)
	if err == nil && record.Status == approval.StatusPending {
		_, err = decideApprovalTx(ctx, tx, approval.DecisionRequest{ProposalID: id, IdempotencyKey: "browser-request-expired-" + id, Action: approval.ActionDeny, Reason: "The paused browser request ended without dispatch.", ReviewedBy: "agent_browser_runtime"})
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) FinishBrowserNetworkRequest(ctx context.Context, id string, dispatched bool) error {
	status := "failed"
	if dispatched {
		status = "dispatched"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_network_requests SET status=? WHERE id=? AND status='consumed'`, status, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("browser network request has no consumed approval")
	}
	return nil
}
func (s *SQLiteStore) InvalidateOldBrowserNetworkRequests(ctx context.Context, boot string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM browser_network_requests WHERE status='paused' AND manager_boot_id<>?`, boot)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.InvalidateBrowserNetworkRequest(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
