package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

func readBrowserNetworkPreference(ctx context.Context, q runControlQueryer, workspaceID string) (domain.BrowserNetworkPreference, error) {
	p := domain.BrowserNetworkPreference{WorkspaceID: workspaceID, BrowserNetworkAccess: domain.BrowserNetworkOff}
	var origins string
	err := q.QueryRowContext(ctx, `SELECT revision,mode,origins_json FROM workspace_browser_network_preferences WHERE workspace_id=?`, workspaceID).Scan(&p.Revision, &p.BrowserNetworkAccess, &origins)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(origins), &p.BrowserOrigins)
	return p, err
}

func (s *SQLiteStore) GetBrowserNetworkPreference(ctx context.Context, workspaceID string) (domain.BrowserNetworkPreference, error) {
	return readBrowserNetworkPreference(ctx, s.db, workspaceID)
}

func readSuccessorBrowserNetworkPreference(ctx context.Context, q runControlQueryer, runID string) (domain.BrowserNetworkPreference, bool, error) {
	var p domain.BrowserNetworkPreference
	var origins string
	err := q.QueryRowContext(ctx, `SELECT revision,mode,origins_json FROM run_browser_network_preferences WHERE run_id=?`, runID).Scan(&p.Revision, &p.BrowserNetworkAccess, &origins)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	err = json.Unmarshal([]byte(origins), &p.BrowserOrigins)
	return p, true, err
}

func readBrowserNetworkSelection(ctx context.Context, q runControlQueryer, runID, workspaceID string) (domain.BrowserNetworkAccessSelection, error) {
	v := domain.BrowserNetworkAccessSelection{Version: domain.BrowserNetworkAccessVersion, RunID: runID, WorkspaceID: workspaceID}
	if runID != "" {
		var raw string
		var status domain.RunStatus
		var permission domain.RunExecutionPermissionMode
		if err := q.QueryRowContext(ctx, `SELECT r.config_json,COALESCE(m.workspace_id,''),r.status FROM runs r JOIN missions m ON m.id=r.mission_id WHERE r.id=?`, runID).Scan(&raw, &v.WorkspaceID, &status); err != nil {
			return v, err
		}
		var config domain.RunConfig
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return v, err
		}
		if config.BrowserNetworkAccess == "" {
			if err := q.QueryRowContext(ctx, `SELECT mode FROM run_execution_permission_snapshots WHERE run_id=? ORDER BY revision DESC LIMIT 1`, runID).Scan(&permission); err != nil {
				return v, err
			}
		}
		v.Revision = config.EffectiveBrowserNetworkRevision()
		v.BrowserNetworkAccess = config.EffectiveBrowserNetworkAccess(permission)
		if v.BrowserNetworkAccess != domain.BrowserNetworkInternet {
			v.BrowserOrigins = config.BrowserOrigins
		}
		if (domain.Run{Status: status}).Terminal() {
			v.ForNextRun = true
			p, found, err := readSuccessorBrowserNetworkPreference(ctx, q, runID)
			if err != nil {
				return v, err
			}
			if found {
				v.Revision, v.BrowserNetworkAccess, v.BrowserOrigins = p.Revision, p.BrowserNetworkAccess, p.BrowserOrigins
			}
		}
	}
	if v.WorkspaceID != "" {
		p, err := readBrowserNetworkPreference(ctx, q, v.WorkspaceID)
		if err != nil {
			return v, err
		}
		if runID == "" {
			v.Revision, v.BrowserNetworkAccess, v.BrowserOrigins = p.Revision, p.BrowserNetworkAccess, p.BrowserOrigins
		} else {
			v.ProjectPreference = &p
		}
	}
	return v, nil
}

func (s *SQLiteStore) GetBrowserNetworkAccess(ctx context.Context, runID, workspaceID string) (domain.BrowserNetworkAccessSelection, error) {
	if runID == "" {
		if _, err := s.GetWorkspaceInfo(ctx, workspaceID); err != nil {
			return domain.BrowserNetworkAccessSelection{}, err
		}
	}
	return readBrowserNetworkSelection(ctx, s.db, runID, workspaceID)
}

func writeBrowserNetworkPreference(ctx context.Context, tx *sql.Tx, p domain.BrowserNetworkPreference, requestedBy string) error {
	origins, err := json.Marshal(p.BrowserOrigins)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_browser_network_preferences(workspace_id,revision,mode,origins_json,requested_by,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision,mode=excluded.mode,origins_json=excluded.origins_json,requested_by=excluded.requested_by,updated_at=excluded.updated_at`, p.WorkspaceID, p.Revision, p.BrowserNetworkAccess, string(origins), requestedBy, ts(time.Now().UTC()))
	return err
}

func (s *SQLiteStore) ChangeBrowserNetworkAccess(ctx context.Context, r domain.ChangeBrowserNetworkAccessRequest) (domain.BrowserNetworkAccessSelection, error) {
	key := runmutation.Fingerprint("browser_network_access_operation.v1", r.RunID, r.WorkspaceID, r.OperationKey)
	intent, _ := json.Marshal(r)
	fingerprint := runmutation.Fingerprint(domain.BrowserNetworkAccessVersion, string(intent))
	var empty domain.BrowserNetworkAccessSelection
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var storedFingerprint, result string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,result_json FROM browser_network_access_operations WHERE key_digest=?`, key).Scan(&storedFingerprint, &result)
	if err == nil {
		if storedFingerprint != fingerprint {
			return empty, apperror.New(apperror.CodeConflict, "Browser network key was already used for different intent")
		}
		if err := json.Unmarshal([]byte(result), &empty); err != nil {
			return empty, err
		}
		empty.Replayed = true
		return empty, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	current, err := readBrowserNetworkSelection(ctx, tx, r.RunID, r.WorkspaceID)
	if err != nil {
		return empty, err
	}
	if current.Revision != r.ExpectedRevision {
		return empty, apperror.New(apperror.CodeConflict, "Browser network access changed concurrently")
	}
	if r.RunID != "" {
		run, err := getRunControlRunTx(ctx, tx, r.RunID)
		if err != nil {
			return empty, err
		}
		if run.Terminal() {
			var hasSuccessor bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thread_runs WHERE predecessor_run_id=?)`, run.ID).Scan(&hasSuccessor); err != nil {
				return empty, err
			}
			if hasSuccessor {
				return empty, apperror.New(apperror.CodeConflict, "This Run already has a successor; select the current Run to change browser access")
			}
			origins, _ := json.Marshal(r.BrowserOrigins)
			if _, err = tx.ExecContext(ctx, `INSERT INTO run_browser_network_preferences(run_id,revision,mode,origins_json,requested_by,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET revision=excluded.revision,mode=excluded.mode,origins_json=excluded.origins_json,requested_by=excluded.requested_by,updated_at=excluded.updated_at`, run.ID, current.Revision+1, r.BrowserNetworkAccess, string(origins), r.RequestedBy, ts(time.Now().UTC())); err != nil {
				return empty, err
			}
		} else {
			run.Config.BrowserNetworkAccess, run.Config.BrowserOrigins = r.BrowserNetworkAccess, r.BrowserOrigins
			run.Config.BrowserNetworkRevision = current.Revision + 1
			if err := run.Config.Validate(); err != nil {
				return empty, err
			}
			raw, err := marshalRedactedJSON(run.Config)
			if err != nil {
				return empty, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE runs SET config_json=?,updated_at=? WHERE id=?`, raw, ts(time.Now().UTC()), run.ID); err != nil {
				return empty, err
			}
			// A paused outbound write and its approval must not survive a changed
			// browser policy, even if the replacement later selects the same mode.
			if err = invalidateBrowserNetworkPolicyRequestsTx(ctx, tx, run.ID, run.Config.BrowserNetworkRevision); err != nil {
				return empty, err
			}
		}
	}
	if r.RunID == "" || r.RememberForProject {
		if current.WorkspaceID == "" {
			return empty, apperror.New(apperror.CodeInvalidArgument, "Remembering browser access requires a project")
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE id=?`, current.WorkspaceID).Scan(&exists); err != nil {
			return empty, err
		}
		if exists != 1 {
			return empty, apperror.New(apperror.CodeNotFound, "Project was not found")
		}
		p, err := readBrowserNetworkPreference(ctx, tx, current.WorkspaceID)
		if err != nil {
			return empty, err
		}
		p.Revision++
		p.BrowserNetworkAccess, p.BrowserOrigins = r.BrowserNetworkAccess, r.BrowserOrigins
		if err := writeBrowserNetworkPreference(ctx, tx, p, r.RequestedBy); err != nil {
			return empty, err
		}
	}
	v, err := readBrowserNetworkSelection(ctx, tx, r.RunID, current.WorkspaceID)
	if err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO browser_network_access_operations VALUES(?,?,?,?,?)`, key, fingerprint, string(encoded), r.RequestedBy, ts(time.Now().UTC())); err != nil {
		return empty, fmt.Errorf("record browser selection: %w", err)
	}
	return v, tx.Commit()
}

func invalidateBrowserNetworkPolicyRequestsTx(ctx context.Context, tx *sql.Tx, runID string, revision int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT b.id FROM browser_network_requests b JOIN tool_approvals a ON a.proposal_id=b.id WHERE b.run_id=? AND b.status='paused' AND a.status='pending'`, runID)
	if err != nil {
		return err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE browser_network_requests SET status='invalidated' WHERE run_id=? AND status='paused'`, runID); err != nil {
		return err
	}
	for _, id := range pending {
		key := "browser-policy-" + runmutation.Fingerprint("browser_network_access_operation.v1", runID, fmt.Sprint(revision), id)
		if _, err = decideApprovalTx(ctx, tx, approval.DecisionRequest{ProposalID: id, IdempotencyKey: key, Action: approval.ActionDeny, Reason: "Browser network access changed before dispatch.", ReviewedBy: "browser_network_policy"}); err != nil {
			return err
		}
	}
	return nil
}
