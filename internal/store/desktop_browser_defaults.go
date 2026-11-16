package store

import (
	"context"

	"cyberagent-workbench/internal/domain"
)

// EnableDesktopBrowserDefaults is an opt-in for the Windows desktop host,
// called before its execution services start. Store.Open and other hosts keep
// their existing off default. This is runtime host policy, not a schema change
// or a remembered operator preference.
func (s *SQLiteStore) EnableDesktopBrowserDefaults(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range defaultBrowserInternetStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.desktopBrowserDefaults.Store(true)
	return nil
}

func (s *SQLiteStore) defaultBrowserNetworkAccess() domain.BrowserNetworkAccess {
	if s.desktopBrowserDefaults.Load() {
		return domain.BrowserNetworkInternet
	}
	return domain.BrowserNetworkOff
}

// DesktopBrowserDefaultNeedsSuccessor lets the next explicit Thread input
// apply a default skipped during startup. Pending approvals remain on their
// original policy; the existing epoch transition checks leases and other work.
func (s *SQLiteStore) DesktopBrowserDefaultNeedsSuccessor(ctx context.Context, runID string) (bool, error) {
	if !s.desktopBrowserDefaults.Load() {
		return false, nil
	}
	var pending bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r
		WHERE r.id=? AND r.status IN ('created','running','paused')
		AND NOT EXISTS (SELECT 1 FROM tool_approvals approval WHERE approval.run_id=r.id AND approval.status='pending')
		AND `+trustedDefaultBrowserNetworkSQL+`)`, runID).Scan(&pending)
	return pending, err
}

// Released desktop creation did not accept an initial browser policy. Only
// its controlled creation/branch/project-move records can prove that rev1 off
// (or an older absent policy) was a default. Explicit choices, remembered
// project policies and unknown direct/CLI creation remain unchanged.
const trustedDefaultBrowserNetworkSQL = `
 ((json_extract(r.config_json,'$.browser_network_access')='off'
   AND json_extract(r.config_json,'$.browser_network_revision')=1)
  OR (json_extract(r.config_json,'$.browser_network_access') IS NULL
      AND COALESCE(json_extract(r.config_json,'$.browser_network_revision'),0)=0))
 AND COALESCE(json_array_length(json_extract(r.config_json,'$.browser_origins')),0)=0
 AND json_extract(r.config_json,'$.interactive')=1
 AND EXISTS (
  SELECT 1 FROM thread_runs owner WHERE owner.run_id=r.id
   AND (
    EXISTS (SELECT 1 FROM thread_runs origin JOIN run_creation_operations creation ON creation.run_id=origin.run_id
            WHERE origin.thread_id=owner.thread_id)
    OR EXISTS (SELECT 1 FROM thread_events branch WHERE branch.type='thread.branched' AND branch.source='thread_branch'
               AND json_extract(branch.payload_json,'$.record.thread_id')=owner.thread_id)
    OR EXISTS (SELECT 1 FROM thread_project_moves move JOIN thread_runs moved ON moved.run_id=move.successor_run_id
               WHERE moved.thread_id=owner.thread_id)
   )
   AND NOT EXISTS (
    SELECT 1 FROM thread_runs related JOIN runs prior ON prior.id=related.run_id
    WHERE related.thread_id=owner.thread_id AND (
     COALESCE(json_extract(prior.config_json,'$.browser_network_revision'),0)>1
     OR COALESCE(json_array_length(json_extract(prior.config_json,'$.browser_origins')),0)>0
     OR json_extract(prior.config_json,'$.browser_network_access')='restricted'
    )
   )
   AND NOT EXISTS (
    SELECT 1 FROM run_browser_network_preferences preference JOIN thread_runs related ON related.run_id=preference.run_id
    WHERE related.thread_id=owner.thread_id
   )
   AND NOT EXISTS (
    SELECT 1 FROM workspace_browser_network_preferences preference JOIN missions mission ON mission.workspace_id=preference.workspace_id
    JOIN runs prior ON prior.mission_id=mission.id JOIN thread_runs related ON related.run_id=prior.id
    WHERE related.thread_id=owner.thread_id
   )
   AND NOT EXISTS (
    SELECT 1 FROM browser_network_access_operations operation JOIN thread_runs related
      ON related.run_id=json_extract(operation.result_json,'$.run_id')
    WHERE related.thread_id=owner.thread_id
   )
 )`

var defaultBrowserInternetStatements = []string{
	// Terminal Run configs remain historical facts. Their next successor uses
	// this same predicate while committing the Thread transaction instead.
	`UPDATE runs AS r SET config_json=json_set(config_json,
       '$.browser_network_access','internet',
       '$.browser_network_revision',MAX(COALESCE(json_extract(config_json,'$.browser_network_revision'),1),1)+1)
     WHERE r.status IN ('created','running','paused')
       AND NOT EXISTS (SELECT 1 FROM run_execution_leases lease
         WHERE lease.run_id=r.id AND lease.status='active' AND julianday(lease.expires_at)>julianday('now'))
       AND NOT EXISTS (SELECT 1 FROM tool_approvals approval
         WHERE approval.run_id=r.id AND approval.status='pending')
       AND ` + trustedDefaultBrowserNetworkSQL,
}
