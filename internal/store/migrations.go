package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const LatestSchemaVersion = 199

type migration struct {
	Version            int
	Name               string
	Statements         []string
	DisableForeignKeys bool
}

type appliedMigration struct {
	Name     string
	Checksum string
}

var runCentricSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS missions (
		id TEXT PRIMARY KEY,
		goal TEXT NOT NULL,
		profile TEXT NOT NULL,
		workspace_id TEXT,
		scope_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_missions_updated_at
		ON missions(updated_at);`,
	`CREATE TABLE IF NOT EXISTS runs (
		id TEXT PRIMARY KEY,
		mission_id TEXT NOT NULL,
		session_id TEXT,
		status TEXT NOT NULL,
		config_json TEXT NOT NULL,
		budget_json TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY(mission_id) REFERENCES missions(id)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_runs_mission_created_at
		ON runs(mission_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS idx_runs_status_updated_at
		ON runs(status, updated_at);`,
	`CREATE TABLE IF NOT EXISTS run_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		version TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		type TEXT NOT NULL,
		source TEXT NOT NULL,
		subject_id TEXT,
		payload_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id),
		FOREIGN KEY(mission_id) REFERENCES missions(id),
		UNIQUE(run_id, sequence)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_run_events_run_sequence
		ON run_events(run_id, sequence);`,
}

var runSessionProjectionStatements = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_session_id_unique
		ON runs(session_id)
		WHERE session_id IS NOT NULL AND session_id <> '';`,
	`CREATE TRIGGER IF NOT EXISTS trg_runs_session_insert
		BEFORE INSERT ON runs
		WHEN NEW.session_id IS NOT NULL AND NEW.session_id <> ''
			AND NOT EXISTS (SELECT 1 FROM sessions WHERE id = NEW.session_id)
		BEGIN
			SELECT RAISE(ABORT, 'run session does not exist');
		END;`,
	`CREATE TRIGGER IF NOT EXISTS trg_runs_session_update
		BEFORE UPDATE OF session_id ON runs
		WHEN NEW.session_id IS NOT NULL AND NEW.session_id <> ''
			AND NOT EXISTS (SELECT 1 FROM sessions WHERE id = NEW.session_id)
		BEGIN
			SELECT RAISE(ABORT, 'run session does not exist');
		END;`,
}

var browserLaunchGateStatements = []string{
	`CREATE TABLE browser_launch_attempts (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL UNIQUE,
		generation INTEGER NOT NULL,
		payload_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id),
		FOREIGN KEY(mission_id) REFERENCES missions(id),
		FOREIGN KEY(session_id) REFERENCES sessions(id),
		CHECK(generation = 1),
		CHECK(json_valid(payload_json)),
		CHECK(json_extract(payload_json, '$.protocol_version') = 'browser_launch_attempt.v1'),
		CHECK(json_extract(payload_json, '$.id') = id),
		CHECK(json_extract(payload_json, '$.run_id') = run_id),
		CHECK(json_extract(payload_json, '$.workspace_id') = workspace_id),
		CHECK(json_extract(payload_json, '$.session_id') = session_id),
		CHECK(json_extract(payload_json, '$.fingerprint') = fingerprint),
		CHECK(json_extract(payload_json, '$.generation') = generation),
		CHECK(json_extract(payload_json, '$.state') = 'prepared'),
		CHECK(json_extract(payload_json, '$.start_blocked') = 1),
		CHECK(json_extract(payload_json, '$.process_start_authorized') = 0),
		CHECK(json_extract(payload_json, '$.network_authorized') = 0),
		CHECK(json_extract(payload_json, '$.profile_write_authorized') = 0),
		CHECK(json_extract(payload_json, '$.artifact_commit_authorized') = 0),
		CHECK(json_extract(payload_json, '$.authority.process_start') = 0),
		CHECK(json_extract(payload_json, '$.authority.network_access') = 0),
		CHECK(json_extract(payload_json, '$.authority.profile_write') = 0),
		CHECK(json_extract(payload_json, '$.authority.request_mutation') = 0),
		CHECK(json_extract(payload_json, '$.authority.request_replay') = 0),
		CHECK(json_extract(payload_json, '$.authority.artifact_commit') = 0)
	);`,
	`CREATE INDEX idx_browser_launch_attempts_run_created
		ON browser_launch_attempts(run_id, created_at, id);`,
	`CREATE TABLE browser_launch_leases (
		id TEXT PRIMARY KEY,
		attempt_id TEXT NOT NULL UNIQUE,
		fingerprint TEXT NOT NULL UNIQUE,
		generation INTEGER NOT NULL,
		status TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		acquired_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		FOREIGN KEY(attempt_id) REFERENCES browser_launch_attempts(id),
		CHECK(generation = 1),
		CHECK(status = 'active'),
		CHECK(json_valid(payload_json)),
		CHECK(json_extract(payload_json, '$.protocol_version') = 'browser_launch_lease.v1'),
		CHECK(json_extract(payload_json, '$.id') = id),
		CHECK(json_extract(payload_json, '$.attempt_id') = attempt_id),
		CHECK(json_extract(payload_json, '$.fingerprint') = fingerprint),
		CHECK(json_extract(payload_json, '$.generation') = generation),
		CHECK(json_extract(payload_json, '$.status') = status),
		CHECK(json_extract(payload_json, '$.start_blocked') = 1),
		CHECK(json_extract(payload_json, '$.process_execution_authorized') = 0),
		CHECK(json_extract(payload_json, '$.process_termination_authorized') = 0),
		CHECK(json_extract(payload_json, '$.authority.process_start') = 0),
		CHECK(json_extract(payload_json, '$.authority.network_access') = 0),
		CHECK(json_extract(payload_json, '$.authority.profile_write') = 0),
		CHECK(json_extract(payload_json, '$.authority.request_mutation') = 0),
		CHECK(json_extract(payload_json, '$.authority.request_replay') = 0),
		CHECK(json_extract(payload_json, '$.authority.artifact_commit') = 0)
	);`,
	`CREATE TABLE browser_launch_preparation_operations (
		key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		attempt_id TEXT NOT NULL UNIQUE,
		lease_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(attempt_id) REFERENCES browser_launch_attempts(id),
		FOREIGN KEY(lease_id) REFERENCES browser_launch_leases(id),
		CHECK(length(key_digest) = 64),
		CHECK(length(request_fingerprint) = 64)
	);`,
	`CREATE TABLE browser_launch_reviews (
		id TEXT PRIMARY KEY,
		attempt_id TEXT NOT NULL UNIQUE,
		lease_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		decision TEXT NOT NULL,
		fingerprint TEXT NOT NULL UNIQUE,
		audit_parent_fingerprint TEXT NOT NULL,
		reviewer_sha256 TEXT NOT NULL,
		event_sequence INTEGER NOT NULL,
		payload_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(attempt_id) REFERENCES browser_launch_attempts(id),
		FOREIGN KEY(lease_id) REFERENCES browser_launch_leases(id),
		FOREIGN KEY(run_id) REFERENCES runs(id),
		FOREIGN KEY(mission_id) REFERENCES missions(id),
		UNIQUE(run_id, event_sequence),
		CHECK(decision IN ('accept_candidate', 'reject_candidate')),
		CHECK(audit_parent_fingerprint = '' OR length(audit_parent_fingerprint) = 64),
		CHECK(length(reviewer_sha256) = 64),
		CHECK(event_sequence > 0),
		CHECK(json_valid(payload_json)),
		CHECK(json_extract(payload_json, '$.protocol_version') = 'browser_launch_review.v1'),
		CHECK(json_extract(payload_json, '$.id') = id),
		CHECK(json_extract(payload_json, '$.attempt_id') = attempt_id),
		CHECK(json_extract(payload_json, '$.decision') = decision),
		CHECK(json_extract(payload_json, '$.fingerprint') = fingerprint),
		CHECK(json_extract(payload_json, '$.audit_parent_fingerprint') = audit_parent_fingerprint),
		CHECK(json_extract(payload_json, '$.reviewer_sha256') = reviewer_sha256),
		CHECK(json_extract(payload_json, '$.accepted_for_future_adapter') =
			CASE decision WHEN 'accept_candidate' THEN 1 ELSE 0 END),
		CHECK(json_extract(payload_json, '$.start_authorized') = 0),
		CHECK(json_extract(payload_json, '$.process_execution_authorized') = 0),
		CHECK(json_extract(payload_json, '$.network_authorized') = 0),
		CHECK(json_extract(payload_json, '$.profile_write_authorized') = 0),
		CHECK(json_extract(payload_json, '$.process_termination_authorized') = 0),
		CHECK(json_extract(payload_json, '$.filesystem_cleanup_authorized') = 0),
		CHECK(json_extract(payload_json, '$.artifact_commit_authorized') = 0),
		CHECK(json_extract(payload_json, '$.authority.process_start') = 0),
		CHECK(json_extract(payload_json, '$.authority.network_access') = 0),
		CHECK(json_extract(payload_json, '$.authority.profile_write') = 0),
		CHECK(json_extract(payload_json, '$.authority.request_mutation') = 0),
		CHECK(json_extract(payload_json, '$.authority.request_replay') = 0),
		CHECK(json_extract(payload_json, '$.authority.artifact_commit') = 0)
	);`,
	`CREATE INDEX idx_browser_launch_reviews_run_event
		ON browser_launch_reviews(run_id, event_sequence, id);`,
	`CREATE TABLE browser_launch_review_operations (
		key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		review_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(review_id) REFERENCES browser_launch_reviews(id),
		CHECK(length(key_digest) = 64),
		CHECK(length(request_fingerprint) = 64)
	);`,
	`CREATE TRIGGER trg_browser_launch_attempt_insert
		BEFORE INSERT ON browser_launch_attempts
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN sessions session_record ON session_record.id = run.session_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND run.session_id = NEW.session_id
				AND mission.workspace_id = NEW.workspace_id
				AND session_record.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Browser launch attempt Run binding is invalid');
		END;`,
	`CREATE TRIGGER trg_browser_launch_lease_insert
		BEFORE INSERT ON browser_launch_leases
		WHEN NOT EXISTS (
			SELECT 1 FROM browser_launch_attempts attempt
			WHERE attempt.id = NEW.attempt_id
				AND attempt.fingerprint = json_extract(NEW.payload_json, '$.attempt_fingerprint')
				AND attempt.generation = NEW.generation
		)
		BEGIN
			SELECT RAISE(ABORT, 'Browser launch lease attempt binding is invalid');
		END;`,
	`CREATE TRIGGER trg_browser_launch_preparation_operation_insert
		BEFORE INSERT ON browser_launch_preparation_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM browser_launch_attempts attempt
			JOIN browser_launch_leases lease ON lease.attempt_id = attempt.id
			WHERE attempt.id = NEW.attempt_id AND lease.id = NEW.lease_id
				AND attempt.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'Browser launch preparation operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_browser_launch_review_insert
		BEFORE INSERT ON browser_launch_reviews
		WHEN NOT EXISTS (
			SELECT 1 FROM browser_launch_attempts attempt
			JOIN browser_launch_leases lease ON lease.attempt_id = attempt.id
			JOIN run_events event ON event.run_id = attempt.run_id
				AND event.sequence = NEW.event_sequence
			WHERE attempt.id = NEW.attempt_id AND lease.id = NEW.lease_id
				AND attempt.run_id = NEW.run_id AND attempt.mission_id = NEW.mission_id
				AND attempt.workspace_id = NEW.workspace_id
				AND json_extract(NEW.payload_json, '$.attempt_fingerprint') = attempt.fingerprint
				AND json_extract(NEW.payload_json, '$.lease_fingerprint') = lease.fingerprint
				AND event.type = 'browser.launch_reviewed' AND event.subject_id = NEW.id
				AND json_extract(event.payload_json, '$.review_fingerprint') = NEW.fingerprint
				AND NEW.audit_parent_fingerprint = COALESCE((
					SELECT prior.fingerprint FROM browser_launch_reviews prior
					WHERE prior.run_id = NEW.run_id ORDER BY prior.event_sequence DESC LIMIT 1
				), '')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Browser launch review binding is invalid');
		END;`,
	`CREATE TRIGGER trg_browser_launch_review_operation_insert
		BEFORE INSERT ON browser_launch_review_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM browser_launch_reviews review
			WHERE review.id = NEW.review_id AND review.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'Browser launch review operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_browser_launch_attempt_update_immutable
		BEFORE UPDATE ON browser_launch_attempts BEGIN
			SELECT RAISE(ABORT, 'Browser launch attempt cannot be updated');
		END;`,
	`CREATE TRIGGER trg_browser_launch_attempt_delete_immutable
		BEFORE DELETE ON browser_launch_attempts BEGIN
			SELECT RAISE(ABORT, 'Browser launch attempt cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_browser_launch_lease_update_immutable
		BEFORE UPDATE ON browser_launch_leases BEGIN
			SELECT RAISE(ABORT, 'Browser launch lease cannot be updated');
		END;`,
	`CREATE TRIGGER trg_browser_launch_lease_delete_immutable
		BEFORE DELETE ON browser_launch_leases BEGIN
			SELECT RAISE(ABORT, 'Browser launch lease cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_browser_launch_preparation_operation_update_immutable
		BEFORE UPDATE ON browser_launch_preparation_operations BEGIN
			SELECT RAISE(ABORT, 'Browser launch preparation operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_browser_launch_preparation_operation_delete_immutable
		BEFORE DELETE ON browser_launch_preparation_operations BEGIN
			SELECT RAISE(ABORT, 'Browser launch preparation operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_browser_launch_review_update_immutable
		BEFORE UPDATE ON browser_launch_reviews BEGIN
			SELECT RAISE(ABORT, 'Browser launch review cannot be updated');
		END;`,
	`CREATE TRIGGER trg_browser_launch_review_delete_immutable
		BEFORE DELETE ON browser_launch_reviews BEGIN
			SELECT RAISE(ABORT, 'Browser launch review cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_browser_launch_review_operation_update_immutable
		BEFORE UPDATE ON browser_launch_review_operations BEGIN
			SELECT RAISE(ABORT, 'Browser launch review operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_browser_launch_review_operation_delete_immutable
		BEFORE DELETE ON browser_launch_review_operations BEGIN
			SELECT RAISE(ABORT, 'Browser launch review operation cannot be deleted');
		END;`,
}

var legacyTaskRunStatements = []string{
	`CREATE TABLE IF NOT EXISTS legacy_task_runs (
		task_id TEXT PRIMARY KEY,
		mission_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES tasks(id),
		FOREIGN KEY(mission_id) REFERENCES missions(id),
		FOREIGN KEY(run_id) REFERENCES runs(id)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_legacy_task_runs_run_id
		ON legacy_task_runs(run_id);`,
}

var supervisorCheckpointStatements = []string{
	`CREATE TABLE IF NOT EXISTS run_supervisor_checkpoints (
		run_id TEXT PRIMARY KEY,
		next_turn INTEGER NOT NULL,
		phase TEXT NOT NULL,
		attempt_id TEXT,
		last_error TEXT,
		updated_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id),
		CHECK(next_turn > 0)
	);`,
}

var supervisorBudgetStatements = []string{
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN execution_millis INTEGER NOT NULL DEFAULT 0;`,
}

var supervisorPendingInputStatements = []string{
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN pending_input TEXT NOT NULL DEFAULT '';`,
}

var supervisorProtocolRepairStatements = []string{
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN repair_phase TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN repair_reason TEXT NOT NULL DEFAULT '';`,
}

var workBoardStatements = []string{
	`CREATE TABLE work_items (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		priority TEXT NOT NULL,
		owner TEXT NOT NULL DEFAULT '',
		acceptance_json TEXT NOT NULL DEFAULT '[]',
		blocked_reason TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		completed_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		UNIQUE(run_id, id),
		CHECK(status IN ('pending', 'in_progress', 'blocked', 'completed', 'cancelled')),
		CHECK(priority IN ('low', 'normal', 'high', 'critical')),
		CHECK(version > 0),
		CHECK((status = 'blocked' AND length(trim(blocked_reason)) > 0) OR (status <> 'blocked' AND blocked_reason = '')),
		CHECK((status = 'completed' AND completed_at IS NOT NULL) OR (status <> 'completed' AND completed_at IS NULL))
	);`,
	`CREATE INDEX idx_work_items_run_status_priority
		ON work_items(run_id, status, priority, updated_at);`,
	`CREATE TABLE work_item_dependencies (
		run_id TEXT NOT NULL,
		work_item_id TEXT NOT NULL,
		depends_on_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(run_id, work_item_id, depends_on_id),
		FOREIGN KEY(run_id, work_item_id) REFERENCES work_items(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, depends_on_id) REFERENCES work_items(run_id, id) ON DELETE RESTRICT,
		CHECK(work_item_id <> depends_on_id)
	) WITHOUT ROWID;`,
	`CREATE INDEX idx_work_item_dependencies_target
		ON work_item_dependencies(run_id, depends_on_id, work_item_id);`,
}

var runNotesStatements = []string{
	`CREATE TABLE notes (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		category TEXT NOT NULL,
		visibility TEXT NOT NULL,
		owner TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		pinned INTEGER NOT NULL DEFAULT 0,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		archived_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		UNIQUE(run_id, id),
		CHECK(category IN ('observation', 'hypothesis', 'decision', 'summary', 'reference')),
		CHECK(visibility IN ('run', 'root', 'owner')),
		CHECK(status IN ('active', 'archived')),
		CHECK(pinned IN (0, 1)),
		CHECK(version > 0),
		CHECK((visibility = 'owner' AND length(trim(owner)) > 0) OR (visibility <> 'owner' AND owner = '')),
		CHECK((status = 'archived' AND archived_at IS NOT NULL) OR (status = 'active' AND archived_at IS NULL))
	);`,
	`CREATE INDEX idx_notes_run_status_pinned
		ON notes(run_id, status, pinned, updated_at);`,
	`CREATE INDEX idx_notes_run_category_visibility
		ON notes(run_id, category, visibility, updated_at);`,
	`CREATE TABLE note_tags (
		run_id TEXT NOT NULL,
		note_id TEXT NOT NULL,
		tag TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(run_id, note_id, tag),
		FOREIGN KEY(run_id, note_id) REFERENCES notes(run_id, id) ON DELETE CASCADE
	) WITHOUT ROWID;`,
	`CREATE INDEX idx_note_tags_lookup ON note_tags(run_id, tag, note_id);`,
	`CREATE TABLE note_sources (
		run_id TEXT NOT NULL,
		note_id TEXT NOT NULL,
		source_ref TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(run_id, note_id, source_ref),
		FOREIGN KEY(run_id, note_id) REFERENCES notes(run_id, id) ON DELETE CASCADE
	) WITHOUT ROWID;`,
	`CREATE TABLE note_evidence (
		run_id TEXT NOT NULL,
		note_id TEXT NOT NULL,
		evidence_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(run_id, note_id, evidence_id),
		FOREIGN KEY(run_id, note_id) REFERENCES notes(run_id, id) ON DELETE CASCADE
	) WITHOUT ROWID;`,
	`CREATE INDEX idx_note_evidence_lookup ON note_evidence(run_id, evidence_id, note_id);`,
}

var durableApprovalStatements = []string{
	`CREATE TABLE tool_approvals (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT,
		session_id TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '',
		tool_name TEXT NOT NULL,
		action_class TEXT NOT NULL,
		mode TEXT NOT NULL,
		status TEXT NOT NULL,
		request_fingerprint TEXT NOT NULL,
		decision_reason TEXT NOT NULL DEFAULT '',
		requested_by TEXT NOT NULL DEFAULT '',
		reviewed_by TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		decided_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		CHECK(mode IN ('automatic', 'per_call', 'session', 'never')),
		CHECK(status IN ('pending', 'approved', 'denied')),
		CHECK(version > 0),
		CHECK(length(request_fingerprint) = 64),
		CHECK((status = 'pending' AND decided_at IS NULL AND reviewed_by = '') OR
			(status <> 'pending' AND decided_at IS NOT NULL AND length(trim(reviewed_by)) > 0))
	);`,
	`CREATE INDEX idx_tool_approvals_run_status_updated_at
		ON tool_approvals(run_id, status, updated_at);`,
	`CREATE INDEX idx_tool_approvals_session_status_updated_at
		ON tool_approvals(session_id, status, updated_at);`,
	`CREATE TABLE approval_operations (
		idempotency_key TEXT PRIMARY KEY,
		approval_id TEXT NOT NULL,
		action TEXT NOT NULL,
		request_fingerprint TEXT NOT NULL,
		result_status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(approval_id) REFERENCES tool_approvals(id) ON DELETE CASCADE,
		CHECK(action IN ('approve', 'deny')),
		CHECK(result_status IN ('approved', 'denied')),
		CHECK(length(idempotency_key) = 64),
		CHECK(length(request_fingerprint) = 64)
	) WITHOUT ROWID;`,
	`CREATE INDEX idx_approval_operations_approval_created_at
		ON approval_operations(approval_id, created_at);`,
}

var sessionGrantAndToolBudgetStatements = []string{
	`CREATE TABLE approval_session_grants (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		tool_name TEXT NOT NULL,
		action_class TEXT NOT NULL,
		status TEXT NOT NULL,
		request_fingerprint TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		revocation_reason TEXT NOT NULL DEFAULT '',
		granted_by TEXT NOT NULL,
		revoked_by TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		revoked_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
		CHECK(status IN ('active', 'revoked')),
		CHECK(version > 0),
		CHECK(length(request_fingerprint) = 64),
		CHECK((status = 'active' AND revoked_at IS NULL AND revoked_by = '' AND revocation_reason = '') OR
			(status = 'revoked' AND revoked_at IS NOT NULL AND length(trim(revoked_by)) > 0))
	);`,
	`CREATE UNIQUE INDEX idx_approval_session_grants_active_scope
		ON approval_session_grants(session_id, workspace_id, tool_name, action_class)
		WHERE status = 'active';`,
	`CREATE INDEX idx_approval_session_grants_run_status_updated_at
		ON approval_session_grants(run_id, status, updated_at);`,
	`CREATE TABLE approval_grant_operations (
		operation_key TEXT PRIMARY KEY,
		grant_id TEXT NOT NULL,
		action TEXT NOT NULL,
		request_fingerprint TEXT NOT NULL,
		result_status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(grant_id) REFERENCES approval_session_grants(id) ON DELETE CASCADE,
		CHECK(action IN ('grant', 'revoke')),
		CHECK(result_status IN ('active', 'revoked')),
		CHECK(length(operation_key) = 64),
		CHECK(length(request_fingerprint) = 64)
	) WITHOUT ROWID;`,
	`CREATE INDEX idx_approval_grant_operations_grant_created_at
		ON approval_grant_operations(grant_id, created_at);`,
	`ALTER TABLE tool_approvals ADD COLUMN grant_id TEXT REFERENCES approval_session_grants(id);`,
	`CREATE INDEX idx_tool_approvals_grant_id ON tool_approvals(grant_id);`,
	`CREATE TABLE run_tool_usage (
		run_id TEXT PRIMARY KEY,
		consumed INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		exhausted_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		CHECK(consumed >= 0)
	) WITHOUT ROWID;`,
	`CREATE TABLE run_tool_calls (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '',
		tool_name TEXT NOT NULL,
		action_class TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		UNIQUE(run_id, sequence),
		CHECK(sequence > 0)
	);`,
	`CREATE INDEX idx_run_tool_calls_run_created_at
		ON run_tool_calls(run_id, created_at);`,
}

var typedScriptProcessStatements = []string{
	`CREATE TABLE script_process_proposals (
		id TEXT PRIMARY KEY,
		operation_key_digest TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		executable TEXT NOT NULL,
		arguments_json TEXT NOT NULL,
		working_directory TEXT NOT NULL,
		requested_backend TEXT NOT NULL,
		execution_mode TEXT NOT NULL,
		status TEXT NOT NULL,
		risk TEXT NOT NULL,
		policy_reason TEXT NOT NULL,
		stdout TEXT NOT NULL DEFAULT '',
		stderr TEXT NOT NULL DEFAULT '',
		exit_code INTEGER NOT NULL DEFAULT 0,
		request_fingerprint TEXT NOT NULL,
		approval_fingerprint TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
		CHECK(length(operation_key_digest) = 64),
		CHECK(length(request_fingerprint) = 64),
		CHECK(length(approval_fingerprint) = 64),
		CHECK(working_directory = '.'),
		CHECK(execution_mode = 'disabled'),
		CHECK(requested_backend IN ('sandbox', 'local')),
		CHECK(risk IN ('low', 'medium', 'high', 'critical')),
		CHECK(status IN ('proposed', 'approved', 'denied', 'completed', 'failed')),
		CHECK(json_valid(arguments_json)),
		CHECK(version > 0)
	);`,
	`CREATE INDEX idx_script_process_run_status_updated_at
		ON script_process_proposals(run_id, status, updated_at);`,
	`CREATE INDEX idx_script_process_session_status_updated_at
		ON script_process_proposals(session_id, status, updated_at);`,
	`CREATE INDEX idx_script_process_workspace_status_updated_at
		ON script_process_proposals(workspace_id, status, updated_at);`,
}

var runArtifactStatements = []string{
	`CREATE TABLE run_artifacts (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		source_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		stream TEXT NOT NULL,
		kind TEXT NOT NULL,
		mime TEXT NOT NULL,
		encoding TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		content TEXT NOT NULL,
		redacted INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
		UNIQUE(run_id, source_id, stream),
		CHECK(stream IN ('stdout', 'stderr')),
		CHECK(kind = 'tool_output'),
		CHECK(encoding = 'utf-8'),
		CHECK(length(sha256) = 64),
		CHECK(size_bytes > 0 AND size_bytes <= 4194304),
		CHECK(redacted IN (0, 1))
	);`,
	`CREATE INDEX idx_run_artifacts_run_created_at
		ON run_artifacts(run_id, created_at, id);`,
	`CREATE INDEX idx_run_artifacts_source_stream
		ON run_artifacts(source_id, stream, created_at);`,
}

var structuredToolOperationStatements = []string{
	`CREATE TABLE structured_tool_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		invocation_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		tool_name TEXT NOT NULL,
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
		FOREIGN KEY(invocation_id) REFERENCES run_tool_calls(id) ON DELETE RESTRICT,
		UNIQUE(tool_name, target_id),
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK((target_kind = 'work_item' AND tool_name = 'work_item_create')
			OR (target_kind = 'note' AND tool_name = 'note_create'))
	);`,
	`CREATE INDEX idx_structured_tool_operations_run_created_at
		ON structured_tool_operations(run_id, created_at, operation_key_digest);`,
	`CREATE TRIGGER trg_structured_tool_operation_invocation_scope
		BEFORE INSERT ON structured_tool_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM run_tool_calls
			WHERE id = NEW.invocation_id AND run_id = NEW.run_id AND session_id = NEW.session_id
				AND workspace_id = NEW.workspace_id AND tool_name = NEW.tool_name
				AND action_class = 'run_memory'
		)
		BEGIN
			SELECT RAISE(ABORT, 'structured tool invocation scope mismatch');
		END;`,
	`CREATE TRIGGER trg_structured_tool_operation_work_item_target
		BEFORE INSERT ON structured_tool_operations
		WHEN NEW.target_kind = 'work_item'
			AND NOT EXISTS (SELECT 1 FROM work_items WHERE id = NEW.target_id AND run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'structured tool WorkItem target mismatch');
		END;`,
	`CREATE TRIGGER trg_structured_tool_operation_note_target
		BEFORE INSERT ON structured_tool_operations
		WHEN NEW.target_kind = 'note'
			AND NOT EXISTS (SELECT 1 FROM notes WHERE id = NEW.target_id AND run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'structured tool Note target mismatch');
		END;`,
}

var supervisorToolLoopStatements = []string{
	`CREATE TABLE run_supervisor_tool_rounds (
		run_id TEXT NOT NULL,
		turn INTEGER NOT NULL,
		attempt_id TEXT NOT NULL,
		round INTEGER NOT NULL,
		model_attempt INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		completed_at TEXT,
		PRIMARY KEY(run_id, turn, attempt_id, round),
		UNIQUE(run_id, turn, attempt_id, model_attempt),
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		CHECK(turn > 0),
		CHECK(round BETWEEN 1 AND 4),
		CHECK(model_attempt > 0)
	);`,
	`CREATE TABLE run_supervisor_tool_calls (
		run_id TEXT NOT NULL,
		turn INTEGER NOT NULL,
		attempt_id TEXT NOT NULL,
		round INTEGER NOT NULL,
		position INTEGER NOT NULL,
		model_attempt INTEGER NOT NULL,
		call_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		status TEXT NOT NULL,
		result_json TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		completed_at TEXT,
		PRIMARY KEY(run_id, turn, attempt_id, round, position),
		UNIQUE(run_id, turn, attempt_id, call_id),
		FOREIGN KEY(run_id, turn, attempt_id, round)
			REFERENCES run_supervisor_tool_rounds(run_id, turn, attempt_id, round) ON DELETE CASCADE,
		CHECK(position BETWEEN 1 AND 4),
		CHECK(model_attempt > 0),
		CHECK(tool_name IN ('work_item_create', 'note_create')),
		CHECK(status IN ('pending', 'completed', 'denied', 'failed')),
		CHECK((status = 'pending' AND result_json = '' AND error_code = '' AND completed_at IS NULL)
			OR (status = 'completed' AND length(result_json) > 0 AND error_code = '' AND completed_at IS NOT NULL)
			OR (status IN ('denied', 'failed') AND length(result_json) > 0 AND length(error_code) > 0
				AND completed_at IS NOT NULL))
	);`,
	`CREATE INDEX idx_run_supervisor_tool_calls_pending
		ON run_supervisor_tool_calls(run_id, turn, attempt_id, status, round, position);`,
	`CREATE TRIGGER trg_supervisor_tool_round_active_attempt
		BEFORE INSERT ON run_supervisor_tool_rounds
		WHEN NOT EXISTS (
			SELECT 1 FROM run_supervisor_checkpoints
			WHERE run_id = NEW.run_id AND next_turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND phase = 'turn_started'
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool round is not bound to the active turn');
		END;`,
	`CREATE TRIGGER trg_supervisor_tool_round_completed_model
		BEFORE INSERT ON run_supervisor_tool_rounds
		WHEN NOT EXISTS (
			SELECT 1 FROM run_events
			WHERE run_id = NEW.run_id AND type = 'model.completed' AND source = 'model_gateway'
				AND subject_id = NEW.attempt_id || '/model/' || NEW.model_attempt
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool round requires a completed model attempt');
		END;`,
	`CREATE TRIGGER trg_supervisor_tool_call_model_attempt
		BEFORE INSERT ON run_supervisor_tool_calls
		WHEN NOT EXISTS (
			SELECT 1 FROM run_supervisor_tool_rounds
			WHERE run_id = NEW.run_id AND turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND round = NEW.round AND model_attempt = NEW.model_attempt
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool call model attempt mismatch');
		END;`,
	`CREATE TRIGGER trg_supervisor_tool_round_completion
		BEFORE UPDATE OF completed_at ON run_supervisor_tool_rounds
		WHEN NEW.completed_at IS NOT NULL AND EXISTS (
			SELECT 1 FROM run_supervisor_tool_calls
			WHERE run_id = NEW.run_id AND turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND round = NEW.round AND status = 'pending'
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool round still has pending calls');
		END;`,
}

var runExecutionLeaseStatements = []string{
	`CREATE TABLE run_execution_leases (
		run_id TEXT PRIMARY KEY,
		lease_id TEXT NOT NULL UNIQUE,
		owner_id TEXT NOT NULL,
		generation INTEGER NOT NULL,
		status TEXT NOT NULL,
		acquired_at TEXT NOT NULL,
		renewed_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		released_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		CHECK(generation > 0),
		CHECK(status IN ('active', 'released')),
		CHECK((status = 'active' AND released_at IS NULL)
			OR (status = 'released' AND released_at IS NOT NULL))
	);`,
	`CREATE INDEX idx_run_execution_leases_status_expires
		ON run_execution_leases(status, expires_at);`,
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN lease_id TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE run_supervisor_checkpoints ADD COLUMN lease_generation INTEGER NOT NULL DEFAULT 0
		CHECK(lease_generation >= 0 AND ((lease_id = '' AND lease_generation = 0)
			OR (lease_id <> '' AND lease_generation > 0)));`,
}

var modelCancellationStatements = []string{
	`CREATE TABLE run_model_cancellations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		model_attempt INTEGER NOT NULL,
		status TEXT NOT NULL,
		reason TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		requested_at TEXT NOT NULL,
		observed_at TEXT,
		resolved_at TEXT,
		resolution TEXT NOT NULL DEFAULT '',
		UNIQUE(run_id, attempt_id, model_attempt),
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		CHECK(model_attempt > 0),
		CHECK(status IN ('pending', 'observed', 'resolved')),
		CHECK((status = 'pending' AND observed_at IS NULL AND resolved_at IS NULL AND resolution = '')
			OR (status = 'observed' AND observed_at IS NOT NULL AND resolved_at IS NULL AND resolution = '')
			OR (status = 'resolved' AND resolved_at IS NOT NULL AND length(resolution) > 0))
	);`,
	`CREATE INDEX idx_run_model_cancellations_pending
		ON run_model_cancellations(run_id, attempt_id, model_attempt, status);`,
	`CREATE TABLE run_model_cancellation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		cancellation_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(cancellation_id) REFERENCES run_model_cancellations(id) ON DELETE CASCADE
	);`,
}

var agentCoordinatorStatements = []string{
	`CREATE TABLE agent_nodes (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		parent_id TEXT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		profile TEXT NOT NULL,
		skills_json TEXT NOT NULL,
		status TEXT NOT NULL,
		depth INTEGER NOT NULL,
		child_limit INTEGER NOT NULL,
		turn_limit INTEGER NOT NULL,
		token_limit INTEGER NOT NULL,
		turns_used INTEGER NOT NULL DEFAULT 0,
		tokens_used INTEGER NOT NULL DEFAULT 0,
		active_attempt_id TEXT NOT NULL DEFAULT '',
		status_reason TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		finished_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, parent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		UNIQUE(run_id, id),
		UNIQUE(run_id, session_id),
		CHECK(role IN ('root', 'specialist')),
		CHECK(status IN ('ready', 'running', 'waiting', 'completed', 'failed', 'cancelled')),
		CHECK(depth BETWEEN 0 AND 1),
		CHECK(child_limit BETWEEN 0 AND 2),
		CHECK(turn_limit > 0),
		CHECK(token_limit >= 0 AND turns_used >= 0 AND tokens_used >= 0),
		CHECK(version > 0),
		CHECK((role = 'root' AND parent_id IS NULL AND depth = 0)
			OR (role = 'specialist' AND parent_id IS NOT NULL AND depth > 0)),
		CHECK((status = 'running' AND length(trim(active_attempt_id)) > 0)
			OR (status <> 'running' AND active_attempt_id = '')),
		CHECK((status IN ('completed', 'failed', 'cancelled') AND finished_at IS NOT NULL)
			OR (status NOT IN ('completed', 'failed', 'cancelled') AND finished_at IS NULL))
	);`,
	`CREATE UNIQUE INDEX idx_agent_nodes_one_root
		ON agent_nodes(run_id) WHERE parent_id IS NULL;`,
	`CREATE INDEX idx_agent_nodes_run_status
		ON agent_nodes(run_id, status, depth, created_at);`,
	`CREATE TRIGGER trg_agent_root_session_matches_run
		BEFORE INSERT ON agent_nodes
		WHEN NEW.role = 'root' AND NOT EXISTS (
			SELECT 1 FROM runs WHERE id = NEW.run_id AND session_id = NEW.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'root agent session does not match run');
		END;`,
	`CREATE TRIGGER trg_agent_child_depth
		BEFORE INSERT ON agent_nodes
		WHEN NEW.parent_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM agent_nodes parent
			WHERE parent.run_id = NEW.run_id AND parent.id = NEW.parent_id
				AND NEW.depth = parent.depth + 1 AND parent.child_limit > (
					SELECT COUNT(*) FROM agent_nodes child
					WHERE child.run_id = NEW.run_id AND child.parent_id = parent.id
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'agent child depth or limit is invalid');
		END;`,
	`CREATE TRIGGER trg_agent_node_limit
		BEFORE INSERT ON agent_nodes
		WHEN (SELECT COUNT(*) FROM agent_nodes WHERE run_id = NEW.run_id) >= 3
		BEGIN
			SELECT RAISE(ABORT, 'agent graph node limit reached');
		END;`,
	`CREATE TABLE agent_messages (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		sender_agent_id TEXT,
		recipient_agent_id TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		kind TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		consumed_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, sender_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, recipient_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		UNIQUE(recipient_agent_id, sequence),
		CHECK(sender_agent_id IS NULL OR sender_agent_id <> recipient_agent_id),
		CHECK(sequence > 0),
		CHECK(kind IN ('control', 'instruction', 'result', 'notification')),
		CHECK(status IN ('pending', 'consumed')),
		CHECK((status = 'pending' AND consumed_at IS NULL)
			OR (status = 'consumed' AND consumed_at IS NOT NULL))
	);`,
	`CREATE INDEX idx_agent_messages_inbox
		ON agent_messages(recipient_agent_id, status, sequence);`,
	`CREATE TRIGGER trg_agent_inbox_limit
		BEFORE INSERT ON agent_messages
		WHEN (SELECT COUNT(*) FROM agent_messages
			WHERE recipient_agent_id = NEW.recipient_agent_id AND status = 'pending') >= 128
		BEGIN
			SELECT RAISE(ABORT, 'agent inbox limit reached');
		END;`,
	`CREATE TRIGGER trg_agent_message_history_limit
		BEFORE INSERT ON agent_messages
		WHEN (SELECT COUNT(*) FROM agent_messages
			WHERE recipient_agent_id = NEW.recipient_agent_id) >= 4096
		BEGIN
			SELECT RAISE(ABORT, 'agent message history limit reached');
		END;`,
	`CREATE TABLE agent_graph_snapshots (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		version INTEGER NOT NULL,
		protocol_version TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		node_count INTEGER NOT NULL,
		pending_message_count INTEGER NOT NULL,
		state_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		UNIQUE(run_id, version),
		CHECK(version > 0),
		CHECK(protocol_version = 'agent_graph.v1'),
		CHECK(node_count BETWEEN 1 AND 3),
		CHECK(pending_message_count BETWEEN 0 AND 384)
	);`,
	`CREATE INDEX idx_agent_graph_snapshots_latest
		ON agent_graph_snapshots(run_id, version DESC);`,
}

var agentInboxProtocolStatements = []string{
	`ALTER TABLE agent_messages ADD COLUMN semantic TEXT NOT NULL DEFAULT 'message'
		CHECK(semantic IN ('message', 'wake', 'dependency'));`,
	`CREATE TABLE agent_message_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		message_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(message_id) REFERENCES agent_messages(id) ON DELETE CASCADE,
		CHECK(length(operation_key_digest) = 64),
		CHECK(length(request_fingerprint) = 64)
	);`,
}

var specialistAdmissionStatements = []string{
	`CREATE TABLE agent_admission_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		agent_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(agent_id) REFERENCES agent_nodes(id) ON DELETE CASCADE,
		CHECK(length(operation_key_digest) = 64),
		CHECK(length(request_fingerprint) = 64)
	);`,
}

var agentMemoryOwnershipStatements = []string{
	`ALTER TABLE work_items ADD COLUMN owner_agent_id TEXT
		REFERENCES agent_nodes(id) ON DELETE RESTRICT
		CHECK(owner_agent_id IS NULL OR length(trim(owner_agent_id)) > 0);`,
	`ALTER TABLE notes ADD COLUMN owner_agent_id TEXT
		REFERENCES agent_nodes(id) ON DELETE RESTRICT
		CHECK(owner_agent_id IS NULL OR length(trim(owner_agent_id)) > 0);`,
	`CREATE INDEX idx_work_items_owner_agent
		ON work_items(run_id, owner_agent_id)
		WHERE owner_agent_id IS NOT NULL;`,
	`CREATE INDEX idx_notes_owner_agent
		ON notes(run_id, owner_agent_id)
		WHERE owner_agent_id IS NOT NULL;`,
	`CREATE TRIGGER trg_work_item_owner_agent_insert
		BEFORE INSERT ON work_items
		WHEN NEW.owner_agent_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM agent_nodes owner
			WHERE owner.id = NEW.owner_agent_id AND owner.run_id = NEW.run_id
				AND owner.status NOT IN ('completed', 'failed', 'cancelled')
		)
		BEGIN
			SELECT RAISE(ABORT, 'work item owner Agent does not belong to Run');
		END;`,
	`CREATE TRIGGER trg_work_item_owner_agent_update
		BEFORE UPDATE OF owner_agent_id, run_id ON work_items
		WHEN NEW.owner_agent_id IS NOT NULL AND (
			NOT EXISTS (
				SELECT 1 FROM agent_nodes owner
				WHERE owner.id = NEW.owner_agent_id AND owner.run_id = NEW.run_id
			) OR (
				COALESCE(OLD.owner_agent_id, '') <> NEW.owner_agent_id AND EXISTS (
					SELECT 1 FROM agent_nodes owner
					WHERE owner.id = NEW.owner_agent_id
						AND owner.status IN ('completed', 'failed', 'cancelled')
				)
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'work item owner Agent does not belong to Run');
		END;`,
	`CREATE TRIGGER trg_note_owner_agent_insert
		BEFORE INSERT ON notes
		WHEN NEW.owner_agent_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM agent_nodes owner
			WHERE owner.id = NEW.owner_agent_id AND owner.run_id = NEW.run_id
				AND owner.status NOT IN ('completed', 'failed', 'cancelled')
		)
		BEGIN
			SELECT RAISE(ABORT, 'note owner Agent does not belong to Run');
		END;`,
	`CREATE TRIGGER trg_note_owner_agent_update
		BEFORE UPDATE OF owner_agent_id, run_id ON notes
		WHEN NEW.owner_agent_id IS NOT NULL AND (
			NOT EXISTS (
				SELECT 1 FROM agent_nodes owner
				WHERE owner.id = NEW.owner_agent_id AND owner.run_id = NEW.run_id
			) OR (
				COALESCE(OLD.owner_agent_id, '') <> NEW.owner_agent_id AND EXISTS (
					SELECT 1 FROM agent_nodes owner
					WHERE owner.id = NEW.owner_agent_id
						AND owner.status IN ('completed', 'failed', 'cancelled')
				)
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'note owner Agent does not belong to Run');
		END;`,
}

var agentCompletionReportStatements = []string{
	`CREATE TABLE agent_completion_reports (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL UNIQUE,
		parent_agent_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		outcome TEXT NOT NULL,
		summary TEXT NOT NULL,
		work_item_ids_json TEXT NOT NULL,
		note_ids_json TEXT NOT NULL,
		message_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, parent_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(message_id) REFERENCES agent_messages(id) ON DELETE CASCADE,
		UNIQUE(run_id, id),
		CHECK(agent_id <> parent_agent_id),
		CHECK(length(trim(attempt_id)) > 0),
		CHECK(protocol_version = 'agent_completion.v1'),
		CHECK(outcome IN ('succeeded', 'partial')),
		CHECK(length(trim(summary)) BETWEEN 1 AND 4096),
		CHECK(length(CAST(summary AS BLOB)) <= 8192),
		CHECK(json_valid(work_item_ids_json) AND json_type(work_item_ids_json) = 'array'),
		CHECK(json_valid(note_ids_json) AND json_type(note_ids_json) = 'array'),
		CHECK(length(CAST(work_item_ids_json AS BLOB)) <= 8192),
		CHECK(length(CAST(note_ids_json AS BLOB)) <= 8192)
	);`,
	`CREATE INDEX idx_agent_completion_reports_run_created
		ON agent_completion_reports(run_id, created_at, id);`,
	`CREATE TABLE agent_completion_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		report_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES agent_completion_reports(id) ON DELETE CASCADE,
		CHECK(length(operation_key_digest) = 64),
		CHECK(length(request_fingerprint) = 64)
	);`,
	`CREATE TRIGGER trg_agent_completion_running_child
		BEFORE INSERT ON agent_completion_reports
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_nodes child
			JOIN agent_nodes parent
				ON parent.run_id = child.run_id AND parent.id = child.parent_id
			WHERE child.run_id = NEW.run_id AND child.id = NEW.agent_id
				AND parent.id = NEW.parent_agent_id
				AND child.role = 'specialist' AND parent.role = 'root'
				AND child.status = 'running' AND child.active_attempt_id = NEW.attempt_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'completion report requires the active Specialist attempt');
		END;`,
	`CREATE TRIGGER trg_agent_completion_message_matches
		BEFORE INSERT ON agent_completion_reports
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_messages message
			WHERE message.id = NEW.message_id AND message.run_id = NEW.run_id
				AND message.sender_agent_id = NEW.agent_id
				AND message.recipient_agent_id = NEW.parent_agent_id
				AND message.kind = 'result' AND message.semantic = 'message'
				AND message.status = 'pending'
		)
		BEGIN
			SELECT RAISE(ABORT, 'completion report message does not match its Agent relationship');
		END;`,
	`CREATE TRIGGER trg_agent_completed_requires_report
		BEFORE UPDATE OF status ON agent_nodes
		WHEN OLD.role = 'specialist' AND NEW.status = 'completed' AND OLD.status <> 'completed'
			AND NOT EXISTS (
				SELECT 1 FROM agent_completion_reports report
				WHERE report.run_id = NEW.run_id AND report.agent_id = NEW.id
					AND report.attempt_id = OLD.active_attempt_id
			)
		BEGIN
			SELECT RAISE(ABORT, 'completed Specialist requires a completion report');
		END;`,
	`CREATE TRIGGER trg_agent_completion_immutable
		BEFORE UPDATE ON agent_completion_reports
		BEGIN
			SELECT RAISE(ABORT, 'completion report is immutable');
		END;`,
}

var specialistAttemptStatements = []string{
	`CREATE TABLE agent_attempts (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		parent_agent_id TEXT NOT NULL,
		lease_id TEXT NOT NULL,
		lease_generation INTEGER NOT NULL,
		turn_number INTEGER NOT NULL,
		status TEXT NOT NULL,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		execution_millis INTEGER NOT NULL DEFAULT 0,
		usage_recorded_at TEXT,
		failure_code TEXT NOT NULL DEFAULT '',
		failure_reason TEXT NOT NULL DEFAULT '',
		notification_message_id TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		finished_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, parent_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		UNIQUE(run_id, id),
		UNIQUE(agent_id, turn_number),
		CHECK(agent_id <> parent_agent_id),
		CHECK(lease_generation > 0 AND turn_number > 0),
		CHECK(status IN ('running', 'continued', 'finished', 'crashed', 'interrupted')),
		CHECK(input_tokens >= 0 AND output_tokens >= 0 AND total_tokens >= 0 AND execution_millis >= 0),
		CHECK(total_tokens >= input_tokens AND total_tokens >= output_tokens),
		CHECK((usage_recorded_at IS NULL AND input_tokens = 0 AND output_tokens = 0
			AND total_tokens = 0 AND execution_millis = 0) OR usage_recorded_at IS NOT NULL),
		CHECK((status = 'running' AND finished_at IS NULL AND failure_code = ''
			AND failure_reason = '' AND notification_message_id = '')
			OR (status = 'continued' AND finished_at IS NOT NULL AND usage_recorded_at IS NOT NULL
				AND failure_code = '' AND failure_reason = '' AND notification_message_id = '')
			OR (status = 'finished' AND finished_at IS NOT NULL AND usage_recorded_at IS NOT NULL
				AND failure_code = '' AND failure_reason = '' AND length(trim(notification_message_id)) > 0)
			OR (status = 'crashed' AND finished_at IS NOT NULL AND length(trim(failure_code)) > 0
				AND length(trim(failure_reason)) > 0 AND length(trim(notification_message_id)) > 0)
			OR (status = 'interrupted' AND finished_at IS NOT NULL AND length(trim(failure_code)) > 0
				AND length(trim(failure_reason)) > 0 AND notification_message_id = ''))
	);`,
	`CREATE UNIQUE INDEX idx_agent_attempts_one_running
		ON agent_attempts(agent_id) WHERE status = 'running';`,
	`CREATE INDEX idx_agent_attempts_run_status
		ON agent_attempts(run_id, status, lease_generation, started_at);`,
	`CREATE TABLE agent_attempt_mutations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(attempt_id) REFERENCES agent_attempts(id) ON DELETE CASCADE,
		UNIQUE(attempt_id, kind),
		CHECK(length(operation_key_digest) = 64),
		CHECK(length(request_fingerprint) = 64),
		CHECK(kind IN ('start', 'usage', 'continue', 'crash'))
	);`,
	`CREATE TRIGGER trg_agent_attempt_running_child
		BEFORE INSERT ON agent_attempts
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_nodes child
			JOIN agent_nodes parent
				ON parent.run_id = child.run_id AND parent.id = child.parent_id
			JOIN runs run ON run.id = child.run_id
			JOIN run_execution_leases lease ON lease.run_id = child.run_id
			JOIN sessions child_session ON child_session.id = child.session_id
			WHERE child.run_id = NEW.run_id AND child.id = NEW.agent_id
				AND parent.id = NEW.parent_agent_id AND child.role = 'specialist'
				AND parent.role = 'root' AND child.status = 'ready'
				AND child.active_attempt_id = '' AND run.status = 'running'
				AND lease.lease_id = NEW.lease_id
				AND lease.generation = NEW.lease_generation AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday('now')
				AND child_session.status = 'active'
		)
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt requires a ready Specialist in a running Run');
		END;`,
	`CREATE TRIGGER trg_specialist_running_requires_attempt
		BEFORE UPDATE OF status, active_attempt_id ON agent_nodes
		WHEN NEW.role = 'specialist' AND NEW.status = 'running' AND NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			WHERE attempt.run_id = NEW.run_id AND attempt.agent_id = NEW.id
				AND attempt.parent_agent_id = NEW.parent_id
				AND attempt.id = NEW.active_attempt_id AND attempt.status = 'running'
		)
		BEGIN
			SELECT RAISE(ABORT, 'running Specialist requires a matching Agent attempt');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_terminal_child
		BEFORE UPDATE OF status ON agent_attempts
		WHEN OLD.status = 'running' AND NEW.status <> 'running' AND NOT EXISTS (
			SELECT 1 FROM agent_nodes child
			WHERE child.run_id = OLD.run_id AND child.id = OLD.agent_id
				AND child.status = 'running' AND child.active_attempt_id = OLD.id
		)
		BEGIN
			SELECT RAISE(ABORT, 'terminal Agent attempt requires its running Specialist');
		END;`,
	`CREATE TRIGGER trg_specialist_nonrunning_requires_terminal_attempt
		BEFORE UPDATE OF status, active_attempt_id ON agent_nodes
		WHEN OLD.role = 'specialist' AND OLD.status = 'running' AND NEW.status <> 'running'
			AND NOT EXISTS (
				SELECT 1 FROM agent_attempts attempt
				WHERE attempt.run_id = OLD.run_id AND attempt.agent_id = OLD.id
					AND attempt.id = OLD.active_attempt_id AND attempt.status <> 'running'
			)
		BEGIN
			SELECT RAISE(ABORT, 'non-running Specialist requires a terminal Agent attempt');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_identity_immutable
		BEFORE UPDATE OF id, run_id, agent_id, parent_agent_id, lease_id, lease_generation,
			turn_number, started_at ON agent_attempts
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt identity is immutable');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_terminal_immutable
		BEFORE UPDATE ON agent_attempts
		WHEN OLD.status <> 'running'
		BEGIN
			SELECT RAISE(ABORT, 'terminal Agent attempt is immutable');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_usage_immutable
		BEFORE UPDATE OF input_tokens, output_tokens, total_tokens, execution_millis,
			usage_recorded_at ON agent_attempts
		WHEN OLD.usage_recorded_at IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt usage is immutable');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_usage_requires_lease
		BEFORE UPDATE OF input_tokens, output_tokens, total_tokens, execution_millis,
			usage_recorded_at ON agent_attempts
		WHEN OLD.usage_recorded_at IS NULL AND NEW.usage_recorded_at IS NOT NULL
			AND NOT EXISTS (
				SELECT 1 FROM run_execution_leases lease
				WHERE lease.run_id = OLD.run_id AND lease.lease_id = OLD.lease_id
					AND lease.generation = OLD.lease_generation AND lease.status = 'active'
					AND julianday(lease.expires_at) > julianday('now')
			)
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt usage requires its active Run lease');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_notification_matches
		BEFORE UPDATE OF status, notification_message_id ON agent_attempts
		WHEN NEW.status IN ('finished', 'crashed') AND NOT EXISTS (
			SELECT 1 FROM agent_messages message
			WHERE message.id = NEW.notification_message_id AND message.run_id = NEW.run_id
				AND message.sender_agent_id = NEW.agent_id
				AND message.recipient_agent_id = NEW.parent_agent_id
				AND message.status = 'pending'
				AND ((NEW.status = 'finished' AND message.kind = 'result')
					OR (NEW.status = 'crashed' AND message.kind = 'notification'))
		)
		BEGIN
			SELECT RAISE(ABORT, 'terminal Agent attempt notification does not match its parent message');
		END;`,
	`CREATE TRIGGER trg_completion_requires_agent_attempt
		BEFORE INSERT ON agent_completion_reports
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE attempt.id = NEW.attempt_id AND attempt.run_id = NEW.run_id
				AND attempt.agent_id = NEW.agent_id
				AND attempt.parent_agent_id = NEW.parent_agent_id
				AND attempt.status = 'running' AND attempt.usage_recorded_at IS NOT NULL
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday('now')
		)
		BEGIN
			SELECT RAISE(ABORT, 'completion report requires a usage-recorded Agent attempt');
		END;`,
}

var rootInboxContextStatements = []string{
	`CREATE TABLE root_inbox_deliveries (
		run_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		supervisor_attempt_id TEXT NOT NULL,
		turn_number INTEGER NOT NULL,
		message_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		status TEXT NOT NULL,
		prepared_at TEXT NOT NULL,
		resolved_at TEXT,
		PRIMARY KEY(supervisor_attempt_id, message_id),
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(message_id) REFERENCES agent_messages(id) ON DELETE CASCADE,
		UNIQUE(supervisor_attempt_id, ordinal),
		CHECK(turn_number > 0),
		CHECK(ordinal BETWEEN 1 AND 4),
		CHECK(status IN ('prepared', 'committed', 'superseded')),
		CHECK((status = 'prepared' AND resolved_at IS NULL)
			OR (status IN ('committed', 'superseded') AND resolved_at IS NOT NULL))
	);`,
	`CREATE UNIQUE INDEX idx_root_inbox_one_prepared_message
		ON root_inbox_deliveries(message_id) WHERE status = 'prepared';`,
	`CREATE UNIQUE INDEX idx_root_inbox_one_committed_message
		ON root_inbox_deliveries(message_id) WHERE status = 'committed';`,
	`CREATE INDEX idx_root_inbox_run_status
		ON root_inbox_deliveries(run_id, status, prepared_at);`,
	`CREATE TRIGGER trg_root_inbox_delivery_insert
		BEFORE INSERT ON root_inbox_deliveries
		WHEN NOT EXISTS (
			SELECT 1 FROM run_supervisor_checkpoints checkpoint
			JOIN runs run ON run.id = checkpoint.run_id
			JOIN run_execution_leases lease ON lease.run_id = checkpoint.run_id
			JOIN agent_nodes root
				ON root.run_id = checkpoint.run_id AND root.id = NEW.root_agent_id
			JOIN agent_messages message ON message.id = NEW.message_id
			JOIN agent_nodes sender
				ON sender.run_id = message.run_id AND sender.id = message.sender_agent_id
			WHERE checkpoint.run_id = NEW.run_id
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = NEW.supervisor_attempt_id
				AND checkpoint.next_turn = NEW.turn_number
				AND run.status = 'running'
				AND lease.lease_id = checkpoint.lease_id
				AND lease.generation = checkpoint.lease_generation
				AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday('now')
				AND root.role = 'root' AND root.status = 'running'
				AND root.active_attempt_id = checkpoint.attempt_id
				AND message.run_id = NEW.run_id
				AND message.recipient_agent_id = root.id
				AND message.status = 'pending'
				AND sender.role = 'specialist' AND sender.parent_id = root.id
				AND (
					(message.semantic = 'dependency' AND message.kind = 'notification')
					OR (message.semantic = 'message' AND message.kind = 'result' AND EXISTS (
						SELECT 1 FROM agent_completion_reports report
						WHERE report.message_id = message.id AND report.agent_id = sender.id
					))
					OR (message.semantic = 'message' AND message.kind = 'notification' AND EXISTS (
						SELECT 1 FROM agent_attempts attempt
						WHERE attempt.notification_message_id = message.id
							AND attempt.agent_id = sender.id AND attempt.status = 'crashed'
					))
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'root inbox delivery requires an eligible pending child message');
		END;`,
	`CREATE TRIGGER trg_root_inbox_delivery_commit
		BEFORE UPDATE OF status, resolved_at ON root_inbox_deliveries
		WHEN OLD.status = 'prepared' AND NEW.status = 'committed' AND NOT EXISTS (
			SELECT 1 FROM run_supervisor_checkpoints checkpoint
			JOIN runs run ON run.id = checkpoint.run_id
			JOIN run_execution_leases lease ON lease.run_id = checkpoint.run_id
			JOIN agent_nodes root
				ON root.run_id = checkpoint.run_id AND root.id = OLD.root_agent_id
			JOIN agent_messages message ON message.id = OLD.message_id
			WHERE checkpoint.run_id = OLD.run_id
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = OLD.supervisor_attempt_id
				AND checkpoint.next_turn = OLD.turn_number
				AND run.status = 'running'
				AND lease.lease_id = checkpoint.lease_id
				AND lease.generation = checkpoint.lease_generation
				AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday('now')
				AND root.status = 'running' AND root.active_attempt_id = checkpoint.attempt_id
				AND message.run_id = OLD.run_id
				AND message.recipient_agent_id = root.id AND message.status = 'pending'
		)
		BEGIN
			SELECT RAISE(ABORT, 'root inbox delivery commit requires its active Supervisor attempt');
		END;`,
	`CREATE TRIGGER trg_root_inbox_delivery_active_supersede
		BEFORE UPDATE OF status ON root_inbox_deliveries
		WHEN OLD.status = 'prepared' AND NEW.status = 'superseded' AND EXISTS (
			SELECT 1 FROM run_supervisor_checkpoints checkpoint
			JOIN runs run ON run.id = checkpoint.run_id
			JOIN agent_nodes root
				ON root.run_id = checkpoint.run_id AND root.id = OLD.root_agent_id
			WHERE checkpoint.run_id = OLD.run_id AND run.status = 'running'
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = OLD.supervisor_attempt_id
				AND root.status = 'running' AND root.active_attempt_id = OLD.supervisor_attempt_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'active root inbox delivery cannot be superseded');
		END;`,
	`CREATE TRIGGER trg_root_inbox_delivery_identity_immutable
		BEFORE UPDATE OF run_id, root_agent_id, supervisor_attempt_id, turn_number,
			message_id, ordinal, prepared_at ON root_inbox_deliveries
		BEGIN
			SELECT RAISE(ABORT, 'root inbox delivery identity is immutable');
		END;`,
	`CREATE TRIGGER trg_root_inbox_delivery_terminal_immutable
		BEFORE UPDATE ON root_inbox_deliveries
		WHEN OLD.status <> 'prepared'
		BEGIN
			SELECT RAISE(ABORT, 'terminal root inbox delivery is immutable');
		END;`,
	`CREATE TRIGGER trg_root_inbox_delivery_prepared_delete
		BEFORE DELETE ON root_inbox_deliveries
		WHEN OLD.status = 'prepared'
		BEGIN
			SELECT RAISE(ABORT, 'prepared root inbox delivery cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_agent_message_prepared_delivery
		BEFORE UPDATE OF status, consumed_at ON agent_messages
		WHEN OLD.status = 'pending' AND NEW.status = 'consumed' AND EXISTS (
			SELECT 1 FROM root_inbox_deliveries delivery
			WHERE delivery.message_id = OLD.id AND delivery.status = 'prepared'
		)
		BEGIN
			SELECT RAISE(ABORT, 'prepared root inbox message must commit through its Supervisor turn');
		END;`,
}

var specialistModelCallStatements = []string{
	`CREATE TABLE specialist_model_calls (
		agent_attempt_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		model_attempt_number INTEGER NOT NULL,
		transport_attempt INTEGER NOT NULL,
		max_attempts INTEGER NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		input_fingerprint TEXT NOT NULL DEFAULT '',
		action_fingerprint TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		error_text TEXT NOT NULL DEFAULT '',
		retry_after_millis INTEGER NOT NULL DEFAULT 0,
		retry_planned INTEGER NOT NULL DEFAULT 0,
		elapsed_millis INTEGER NOT NULL DEFAULT 0,
		stream_events INTEGER NOT NULL DEFAULT 0,
		stream_bytes INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		usage_recorded INTEGER NOT NULL DEFAULT 0,
		action_kind TEXT NOT NULL DEFAULT '',
		report_outcome TEXT NOT NULL DEFAULT '',
		policy_allowed INTEGER NOT NULL DEFAULT -1,
		policy_needs_approval INTEGER NOT NULL DEFAULT 0,
		policy_risk TEXT NOT NULL DEFAULT '',
		policy_reason TEXT NOT NULL DEFAULT '',
		user_message_id INTEGER,
		assistant_message_id INTEGER,
		started_at TEXT NOT NULL,
		finished_at TEXT,
		PRIMARY KEY(agent_attempt_id, model_attempt_number),
		FOREIGN KEY(agent_attempt_id) REFERENCES agent_attempts(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(user_message_id) REFERENCES session_messages(id),
		FOREIGN KEY(assistant_message_id) REFERENCES session_messages(id),
		CHECK(model_attempt_number > 0 AND transport_attempt = model_attempt_number),
		CHECK(max_attempts BETWEEN 1 AND 5 AND model_attempt_number <= max_attempts),
		CHECK(status IN ('started', 'completed', 'failed')),
		CHECK(outcome IN ('', 'success', 'retryable', 'rate_limited', 'invalid_response', 'cancelled', 'permanent')),
		CHECK(retry_after_millis >= 0 AND retry_planned IN (0, 1)),
		CHECK(elapsed_millis >= 0 AND stream_events >= 0 AND stream_events <= 4096),
		CHECK(stream_bytes >= 0 AND stream_bytes <= 65536),
		CHECK(input_tokens >= 0 AND output_tokens >= 0 AND total_tokens >= 0),
		CHECK(total_tokens >= input_tokens AND total_tokens >= output_tokens),
		CHECK(usage_recorded IN (0, 1)),
		CHECK(policy_allowed IN (-1, 0, 1) AND policy_needs_approval IN (0, 1)),
		CHECK((status = 'started' AND outcome = '' AND error_text = ''
			AND input_fingerprint = '' AND action_fingerprint = ''
			AND retry_after_millis = 0 AND retry_planned = 0 AND elapsed_millis = 0
			AND stream_events = 0 AND stream_bytes = 0 AND usage_recorded = 0
			AND input_tokens = 0 AND output_tokens = 0 AND total_tokens = 0
			AND action_kind = '' AND report_outcome = '' AND policy_allowed = -1
			AND policy_needs_approval = 0 AND policy_risk = '' AND policy_reason = ''
			AND user_message_id IS NULL AND assistant_message_id IS NULL AND finished_at IS NULL)
		OR (status = 'completed' AND outcome = 'success' AND error_text = ''
			AND length(input_fingerprint) = 64 AND length(action_fingerprint) = 64
			AND retry_after_millis = 0 AND retry_planned = 0 AND usage_recorded = 1
			AND action_kind IN ('continue', 'finish')
			AND ((action_kind = 'continue' AND report_outcome = '')
				OR (action_kind = 'finish' AND report_outcome IN ('succeeded', 'partial')))
			AND policy_allowed IN (0, 1) AND length(trim(policy_reason)) > 0
			AND ((policy_allowed = 1 AND policy_needs_approval = 0
				AND user_message_id IS NOT NULL AND assistant_message_id IS NOT NULL)
				OR ((policy_allowed = 0 OR policy_needs_approval = 1)
					AND user_message_id IS NULL AND assistant_message_id IS NULL))
			AND finished_at IS NOT NULL)
		OR (status = 'failed' AND outcome IN ('retryable', 'rate_limited',
				'invalid_response', 'cancelled', 'permanent')
			AND input_fingerprint = '' AND action_fingerprint = ''
			AND length(trim(error_text)) > 0 AND action_kind = '' AND report_outcome = ''
			AND policy_allowed = -1 AND policy_needs_approval = 0
			AND policy_risk = '' AND policy_reason = ''
			AND user_message_id IS NULL AND assistant_message_id IS NULL
			AND (usage_recorded = 1 OR (input_tokens = 0 AND output_tokens = 0 AND total_tokens = 0))
			AND (retry_planned = 0 OR (usage_recorded = 0
				AND outcome IN ('retryable', 'rate_limited')
				AND model_attempt_number < max_attempts))
			AND finished_at IS NOT NULL))
	);`,
	`CREATE UNIQUE INDEX idx_specialist_model_one_started
		ON specialist_model_calls(agent_attempt_id) WHERE status = 'started';`,
	`CREATE INDEX idx_specialist_model_agent_started
		ON specialist_model_calls(agent_id, started_at, model_attempt_number);`,
	`CREATE TRIGGER trg_specialist_model_call_sequence
		BEFORE INSERT ON specialist_model_calls
		WHEN NEW.model_attempt_number <> (
			SELECT COUNT(*) + 1 FROM specialist_model_calls
			WHERE agent_attempt_id = NEW.agent_attempt_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call is not the next attempt');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_insert
		BEFORE INSERT ON specialist_model_calls
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE attempt.id = NEW.agent_attempt_id AND attempt.run_id = NEW.run_id
				AND attempt.agent_id = NEW.agent_id AND attempt.status = 'running'
				AND attempt.usage_recorded_at IS NULL
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.active_attempt_id = attempt.id AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call requires its active leased attempt');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_terminal_requires_lease
		BEFORE UPDATE OF status ON specialist_model_calls
		WHEN OLD.status = 'started' AND NEW.status <> 'started' AND NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE attempt.id = OLD.agent_attempt_id AND attempt.run_id = OLD.run_id
				AND attempt.agent_id = OLD.agent_id AND attempt.status = 'running'
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.active_attempt_id = attempt.id AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND ((NEW.usage_recorded = 0 AND attempt.usage_recorded_at IS NULL)
					OR (NEW.usage_recorded = 1 AND attempt.usage_recorded_at IS NOT NULL
						AND attempt.input_tokens = NEW.input_tokens
						AND attempt.output_tokens = NEW.output_tokens
						AND attempt.total_tokens = NEW.total_tokens))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model terminal requires its active leased usage state');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_identity_immutable
		BEFORE UPDATE OF agent_attempt_id, run_id, agent_id, model_attempt_number,
			transport_attempt, max_attempts, provider, model, started_at
		ON specialist_model_calls
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call identity is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_terminal_immutable
		BEFORE UPDATE ON specialist_model_calls
		WHEN OLD.status <> 'started'
		BEGIN
			SELECT RAISE(ABORT, 'terminal Specialist model call is immutable');
		END;`,
}

var specialistProtocolRepairStatements = []string{
	`DROP TRIGGER trg_specialist_model_call_sequence;`,
	`DROP TRIGGER trg_specialist_model_call_insert;`,
	`DROP TRIGGER trg_specialist_model_call_terminal_requires_lease;`,
	`DROP TRIGGER trg_specialist_model_call_identity_immutable;`,
	`DROP TRIGGER trg_specialist_model_call_terminal_immutable;`,
	`DROP INDEX idx_specialist_model_one_started;`,
	`DROP INDEX idx_specialist_model_agent_started;`,
	`ALTER TABLE specialist_model_calls RENAME TO specialist_model_calls_v27;`,
	`CREATE TABLE specialist_model_calls (
		agent_attempt_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		model_attempt_number INTEGER NOT NULL,
		transport_attempt INTEGER NOT NULL,
		max_attempts INTEGER NOT NULL,
		protocol_repair INTEGER NOT NULL DEFAULT 0,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		input_fingerprint TEXT NOT NULL DEFAULT '',
		action_fingerprint TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		error_text TEXT NOT NULL DEFAULT '',
		retry_after_millis INTEGER NOT NULL DEFAULT 0,
		retry_planned INTEGER NOT NULL DEFAULT 0,
		elapsed_millis INTEGER NOT NULL DEFAULT 0,
		stream_events INTEGER NOT NULL DEFAULT 0,
		stream_bytes INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		usage_recorded INTEGER NOT NULL DEFAULT 0,
		action_kind TEXT NOT NULL DEFAULT '',
		report_outcome TEXT NOT NULL DEFAULT '',
		policy_allowed INTEGER NOT NULL DEFAULT -1,
		policy_needs_approval INTEGER NOT NULL DEFAULT 0,
		policy_risk TEXT NOT NULL DEFAULT '',
		policy_reason TEXT NOT NULL DEFAULT '',
		user_message_id INTEGER,
		assistant_message_id INTEGER,
		started_at TEXT NOT NULL,
		finished_at TEXT,
		PRIMARY KEY(agent_attempt_id, model_attempt_number),
		FOREIGN KEY(agent_attempt_id) REFERENCES agent_attempts(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(user_message_id) REFERENCES session_messages(id),
		FOREIGN KEY(assistant_message_id) REFERENCES session_messages(id),
		CHECK(model_attempt_number > 0 AND transport_attempt > 0),
		CHECK(max_attempts BETWEEN 1 AND 5 AND transport_attempt <= max_attempts),
		CHECK(protocol_repair IN (0, 1)),
		CHECK(status IN ('started', 'completed', 'failed')),
		CHECK(outcome IN ('', 'success', 'retryable', 'rate_limited', 'invalid_response', 'cancelled', 'permanent')),
		CHECK(retry_after_millis >= 0 AND retry_planned IN (0, 1)),
		CHECK(elapsed_millis >= 0 AND stream_events >= 0 AND stream_events <= 4096),
		CHECK(stream_bytes >= 0 AND stream_bytes <= 65536),
		CHECK(input_tokens >= 0 AND output_tokens >= 0 AND total_tokens >= 0),
		CHECK(total_tokens >= input_tokens AND total_tokens >= output_tokens),
		CHECK(usage_recorded IN (0, 1)),
		CHECK(policy_allowed IN (-1, 0, 1) AND policy_needs_approval IN (0, 1)),
		CHECK((status = 'started' AND outcome = '' AND error_text = ''
			AND input_fingerprint = '' AND action_fingerprint = ''
			AND retry_after_millis = 0 AND retry_planned = 0 AND elapsed_millis = 0
			AND stream_events = 0 AND stream_bytes = 0 AND usage_recorded = 0
			AND input_tokens = 0 AND output_tokens = 0 AND total_tokens = 0
			AND action_kind = '' AND report_outcome = '' AND policy_allowed = -1
			AND policy_needs_approval = 0 AND policy_risk = '' AND policy_reason = ''
			AND user_message_id IS NULL AND assistant_message_id IS NULL AND finished_at IS NULL)
		OR (status = 'completed' AND outcome = 'success' AND error_text = ''
			AND length(input_fingerprint) = 64 AND length(action_fingerprint) = 64
			AND retry_after_millis = 0 AND retry_planned = 0 AND usage_recorded = 1
			AND action_kind IN ('continue', 'finish')
			AND ((action_kind = 'continue' AND report_outcome = '')
				OR (action_kind = 'finish' AND report_outcome IN ('succeeded', 'partial')))
			AND policy_allowed IN (0, 1) AND length(trim(policy_reason)) > 0
			AND ((policy_allowed = 1 AND policy_needs_approval = 0
				AND user_message_id IS NOT NULL AND assistant_message_id IS NOT NULL)
				OR ((policy_allowed = 0 OR policy_needs_approval = 1)
					AND user_message_id IS NULL AND assistant_message_id IS NULL))
			AND finished_at IS NOT NULL)
		OR (status = 'failed' AND outcome IN ('retryable', 'rate_limited',
				'invalid_response', 'cancelled', 'permanent')
			AND input_fingerprint = '' AND action_fingerprint = ''
			AND length(trim(error_text)) > 0 AND action_kind = '' AND report_outcome = ''
			AND policy_allowed = -1 AND policy_needs_approval = 0
			AND policy_risk = '' AND policy_reason = ''
			AND user_message_id IS NULL AND assistant_message_id IS NULL
			AND (usage_recorded = 1 OR (input_tokens = 0 AND output_tokens = 0 AND total_tokens = 0))
			AND (retry_planned = 0 OR (usage_recorded = 0
				AND outcome IN ('retryable', 'rate_limited')
				AND transport_attempt < max_attempts))
			AND finished_at IS NOT NULL))
	);`,
	`INSERT INTO specialist_model_calls
		(agent_attempt_id, run_id, agent_id, model_attempt_number, transport_attempt,
		max_attempts, protocol_repair, provider, model, input_fingerprint, action_fingerprint,
		status, outcome, error_text, retry_after_millis, retry_planned, elapsed_millis,
		stream_events, stream_bytes, input_tokens, output_tokens, total_tokens,
		usage_recorded, action_kind, report_outcome, policy_allowed, policy_needs_approval,
		policy_risk, policy_reason, user_message_id, assistant_message_id, started_at, finished_at)
	SELECT agent_attempt_id, run_id, agent_id, model_attempt_number, transport_attempt,
		max_attempts, 0, provider, model, input_fingerprint, action_fingerprint,
		status, outcome, error_text, retry_after_millis, retry_planned, elapsed_millis,
		stream_events, stream_bytes, input_tokens, output_tokens, total_tokens,
		usage_recorded, action_kind, report_outcome, policy_allowed, policy_needs_approval,
		policy_risk, policy_reason, user_message_id, assistant_message_id, started_at, finished_at
	FROM specialist_model_calls_v27;`,
	`DROP TABLE specialist_model_calls_v27;`,
	`CREATE TABLE specialist_protocol_repairs (
		agent_attempt_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		status TEXT NOT NULL,
		reason TEXT NOT NULL,
		requested_model_attempt INTEGER NOT NULL,
		resolved_model_attempt INTEGER,
		requested_at TEXT NOT NULL,
		resolved_at TEXT,
		FOREIGN KEY(agent_attempt_id) REFERENCES agent_attempts(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(agent_attempt_id, requested_model_attempt)
			REFERENCES specialist_model_calls(agent_attempt_id, model_attempt_number),
		FOREIGN KEY(agent_attempt_id, resolved_model_attempt)
			REFERENCES specialist_model_calls(agent_attempt_id, model_attempt_number),
		CHECK(status IN ('pending', 'completed', 'exhausted', 'aborted')),
		CHECK(length(trim(reason)) BETWEEN 1 AND 1024),
		CHECK(length(CAST(reason AS BLOB)) <= 4096),
		CHECK(requested_model_attempt > 0),
		CHECK((status = 'pending' AND resolved_model_attempt IS NULL AND resolved_at IS NULL)
			OR (status IN ('completed', 'exhausted')
				AND resolved_model_attempt > requested_model_attempt AND resolved_at IS NOT NULL)
			OR (status = 'aborted' AND resolved_model_attempt IS NULL AND resolved_at IS NOT NULL))
	);`,
	`CREATE UNIQUE INDEX idx_specialist_model_one_started
		ON specialist_model_calls(agent_attempt_id) WHERE status = 'started';`,
	`CREATE INDEX idx_specialist_model_agent_started
		ON specialist_model_calls(agent_id, started_at, model_attempt_number);`,
	`CREATE INDEX idx_specialist_repair_run_status
		ON specialist_protocol_repairs(run_id, status, requested_at);`,
	`CREATE TRIGGER trg_specialist_model_call_sequence
		BEFORE INSERT ON specialist_model_calls
		WHEN NEW.model_attempt_number <> (
			SELECT COUNT(*) + 1 FROM specialist_model_calls
			WHERE agent_attempt_id = NEW.agent_attempt_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call is not the next attempt');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_phase_sequence
		BEFORE INSERT ON specialist_model_calls
		WHEN NEW.transport_attempt <> (
			SELECT COUNT(*) + 1 FROM specialist_model_calls
			WHERE agent_attempt_id = NEW.agent_attempt_id
				AND protocol_repair = NEW.protocol_repair
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call is not the next phase transport attempt');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_insert
		BEFORE INSERT ON specialist_model_calls
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE attempt.id = NEW.agent_attempt_id AND attempt.run_id = NEW.run_id
				AND attempt.agent_id = NEW.agent_id AND attempt.status = 'running'
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.active_attempt_id = attempt.id AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND ((NEW.protocol_repair = 0 AND attempt.usage_recorded_at IS NULL
					AND NOT EXISTS (SELECT 1 FROM specialist_protocol_repairs repair
						WHERE repair.agent_attempt_id = attempt.id))
					OR (NEW.protocol_repair = 1 AND attempt.usage_recorded_at IS NOT NULL
						AND EXISTS (SELECT 1 FROM specialist_protocol_repairs repair
							WHERE repair.agent_attempt_id = attempt.id AND repair.status = 'pending')))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call requires its active leased protocol phase');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_terminal_requires_lease
		BEFORE UPDATE OF status ON specialist_model_calls
		WHEN OLD.status = 'started' AND NEW.status <> 'started' AND NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE attempt.id = OLD.agent_attempt_id AND attempt.run_id = OLD.run_id
				AND attempt.agent_id = OLD.agent_id AND attempt.status = 'running'
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.active_attempt_id = attempt.id AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND (NEW.usage_recorded = 0 OR (
					attempt.usage_recorded_at IS NOT NULL
					AND attempt.input_tokens = NEW.input_tokens + COALESCE((
						SELECT SUM(call.input_tokens) FROM specialist_model_calls call
						WHERE call.agent_attempt_id = OLD.agent_attempt_id
							AND call.model_attempt_number <> OLD.model_attempt_number
							AND call.status <> 'started' AND call.usage_recorded = 1), 0)
					AND attempt.output_tokens = NEW.output_tokens + COALESCE((
						SELECT SUM(call.output_tokens) FROM specialist_model_calls call
						WHERE call.agent_attempt_id = OLD.agent_attempt_id
							AND call.model_attempt_number <> OLD.model_attempt_number
							AND call.status <> 'started' AND call.usage_recorded = 1), 0)
					AND attempt.total_tokens = NEW.total_tokens + COALESCE((
						SELECT SUM(call.total_tokens) FROM specialist_model_calls call
						WHERE call.agent_attempt_id = OLD.agent_attempt_id
							AND call.model_attempt_number <> OLD.model_attempt_number
							AND call.status <> 'started' AND call.usage_recorded = 1), 0)
					AND attempt.execution_millis = NEW.elapsed_millis + COALESCE((
						SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.agent_attempt_id = OLD.agent_attempt_id
							AND call.model_attempt_number <> OLD.model_attempt_number
							AND call.status <> 'started'), 0)))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model terminal requires its active leased cumulative usage');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_identity_immutable
		BEFORE UPDATE OF agent_attempt_id, run_id, agent_id, model_attempt_number,
			transport_attempt, max_attempts, protocol_repair, provider, model, started_at
		ON specialist_model_calls
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model call identity is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_model_call_terminal_immutable
		BEFORE UPDATE ON specialist_model_calls
		WHEN OLD.status <> 'started'
		BEGIN
			SELECT RAISE(ABORT, 'terminal Specialist model call is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_repair_insert
		BEFORE INSERT ON specialist_protocol_repairs
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN specialist_model_calls call
				ON call.agent_attempt_id = attempt.id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE attempt.id = NEW.agent_attempt_id AND attempt.run_id = NEW.run_id
				AND attempt.agent_id = NEW.agent_id AND attempt.status = 'running'
				AND attempt.usage_recorded_at IS NOT NULL
				AND call.model_attempt_number = NEW.requested_model_attempt
				AND call.protocol_repair = 0 AND call.status = 'failed'
				AND call.outcome = 'invalid_response' AND call.usage_recorded = 1
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist repair requires a charged primary protocol failure');
		END;`,
	`CREATE TRIGGER trg_specialist_repair_resolve
		BEFORE UPDATE OF status, resolved_model_attempt, resolved_at
		ON specialist_protocol_repairs
		WHEN OLD.status <> 'pending' OR NOT (
			(NEW.status = 'completed' AND NEW.resolved_at IS NOT NULL AND EXISTS (
				SELECT 1 FROM specialist_model_calls call
				WHERE call.agent_attempt_id = OLD.agent_attempt_id
					AND call.model_attempt_number = NEW.resolved_model_attempt
					AND call.protocol_repair = 1 AND call.status = 'completed'
					AND call.outcome = 'success' AND call.usage_recorded = 1))
			OR (NEW.status = 'exhausted' AND NEW.resolved_at IS NOT NULL AND EXISTS (
				SELECT 1 FROM specialist_model_calls call
				WHERE call.agent_attempt_id = OLD.agent_attempt_id
					AND call.model_attempt_number = NEW.resolved_model_attempt
					AND call.protocol_repair = 1 AND call.status = 'failed'
					AND call.outcome = 'invalid_response' AND call.usage_recorded = 1))
			OR (NEW.status = 'aborted' AND NEW.resolved_model_attempt IS NULL
				AND NEW.resolved_at IS NOT NULL AND EXISTS (
					SELECT 1 FROM agent_attempts attempt
					JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
					WHERE attempt.id = OLD.agent_attempt_id AND attempt.status = 'running'
						AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
						AND ((lease.lease_id = attempt.lease_id
							AND lease.generation = attempt.lease_generation)
							OR lease.generation > attempt.lease_generation)))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist repair resolution is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_repair_identity_immutable
		BEFORE UPDATE OF agent_attempt_id, run_id, agent_id, reason,
			requested_model_attempt, requested_at ON specialist_protocol_repairs
		BEGIN
			SELECT RAISE(ABORT, 'Specialist repair identity is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_repair_terminal_immutable
		BEFORE UPDATE ON specialist_protocol_repairs
		WHEN OLD.status <> 'pending'
		BEGIN
			SELECT RAISE(ABORT, 'terminal Specialist repair is immutable');
		END;`,
	`DROP TRIGGER trg_agent_attempt_usage_immutable;`,
	`DROP TRIGGER trg_agent_attempt_usage_requires_lease;`,
	`CREATE TRIGGER trg_agent_attempt_usage_monotonic
		BEFORE UPDATE OF input_tokens, output_tokens, total_tokens, execution_millis,
			usage_recorded_at ON agent_attempts
		WHEN OLD.usage_recorded_at IS NOT NULL AND (
			NEW.usage_recorded_at IS NULL OR NEW.usage_recorded_at <> OLD.usage_recorded_at
			OR NEW.input_tokens < OLD.input_tokens OR NEW.output_tokens < OLD.output_tokens
			OR NEW.total_tokens < OLD.total_tokens OR NEW.execution_millis < OLD.execution_millis)
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt usage must grow monotonically');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_usage_requires_lease
		BEFORE UPDATE OF input_tokens, output_tokens, total_tokens, execution_millis,
			usage_recorded_at ON agent_attempts
		WHEN NOT EXISTS (
			SELECT 1 FROM run_execution_leases lease
			WHERE lease.run_id = OLD.run_id AND lease.lease_id = OLD.lease_id
				AND lease.generation = OLD.lease_generation AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday('now')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt usage requires its active Run lease');
		END;`,
	`CREATE TRIGGER trg_agent_attempt_repair_resolution
		BEFORE UPDATE OF status ON agent_attempts
		WHEN OLD.status = 'running' AND NEW.status <> 'running' AND (
			(NEW.status IN ('continued', 'finished') AND EXISTS (
				SELECT 1 FROM specialist_protocol_repairs repair
				WHERE repair.agent_attempt_id = OLD.id AND repair.status <> 'completed'))
			OR (NEW.status IN ('crashed', 'interrupted') AND EXISTS (
				SELECT 1 FROM specialist_protocol_repairs repair
				WHERE repair.agent_attempt_id = OLD.id AND repair.status = 'pending')))
		BEGIN
			SELECT RAISE(ABORT, 'Agent attempt requires a resolved Specialist repair');
		END;`,
}

var specialistScheduleControlStatements = []string{
	`CREATE TABLE specialist_schedules (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		lease_id TEXT NOT NULL,
		lease_generation INTEGER NOT NULL,
		max_rounds INTEGER NOT NULL,
		status TEXT NOT NULL,
		stop_reason TEXT NOT NULL DEFAULT '',
		rounds_completed INTEGER NOT NULL DEFAULT 0,
		turns_started INTEGER NOT NULL DEFAULT 0,
		recovered_attempts INTEGER NOT NULL DEFAULT 0,
		before_root_tokens INTEGER NOT NULL,
		before_specialist_tokens INTEGER NOT NULL,
		before_total_tokens INTEGER NOT NULL,
		before_root_execution_millis INTEGER NOT NULL,
		before_specialist_execution_millis INTEGER NOT NULL,
		before_total_execution_millis INTEGER NOT NULL,
		after_root_tokens INTEGER NOT NULL,
		after_specialist_tokens INTEGER NOT NULL,
		after_total_tokens INTEGER NOT NULL,
		after_root_execution_millis INTEGER NOT NULL,
		after_specialist_execution_millis INTEGER NOT NULL,
		after_total_execution_millis INTEGER NOT NULL,
		error_code TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		finished_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		CHECK(lease_generation > 0 AND max_rounds BETWEEN 1 AND 32),
		CHECK(status IN ('running', 'completed', 'failed', 'cancelled', 'abandoned')),
		CHECK(rounds_completed BETWEEN 0 AND max_rounds
			AND turns_started >= 0 AND recovered_attempts >= 0),
		CHECK(before_root_tokens >= 0 AND before_specialist_tokens >= 0
			AND before_total_tokens = before_root_tokens + before_specialist_tokens),
		CHECK(before_root_execution_millis >= 0 AND before_specialist_execution_millis >= 0
			AND before_total_execution_millis = before_root_execution_millis + before_specialist_execution_millis),
		CHECK(after_root_tokens >= 0 AND after_specialist_tokens >= 0
			AND after_total_tokens = after_root_tokens + after_specialist_tokens),
		CHECK(after_root_execution_millis >= 0 AND after_specialist_execution_millis >= 0
			AND after_total_execution_millis = after_root_execution_millis + after_specialist_execution_millis),
		CHECK(length(stop_reason) <= 64 AND length(error_code) <= 64),
		CHECK((status = 'running' AND stop_reason = '' AND error_code = ''
			AND rounds_completed = 0 AND turns_started = 0 AND recovered_attempts = 0
			AND before_root_tokens = after_root_tokens
			AND before_specialist_tokens = after_specialist_tokens
			AND before_total_tokens = after_total_tokens
			AND before_root_execution_millis = after_root_execution_millis
			AND before_specialist_execution_millis = after_specialist_execution_millis
			AND before_total_execution_millis = after_total_execution_millis
			AND finished_at IS NULL)
			OR (status <> 'running' AND length(trim(stop_reason)) BETWEEN 1 AND 64
				AND finished_at IS NOT NULL))
	);`,
	`CREATE TABLE specialist_schedule_agents (
		schedule_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		PRIMARY KEY(schedule_id, ordinal),
		UNIQUE(schedule_id, agent_id),
		FOREIGN KEY(schedule_id) REFERENCES specialist_schedules(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		CHECK(ordinal BETWEEN 1 AND 2)
	);`,
	`CREATE UNIQUE INDEX idx_specialist_schedule_one_running
		ON specialist_schedules(run_id) WHERE status = 'running';`,
	`CREATE INDEX idx_specialist_schedule_run_started
		ON specialist_schedules(run_id, started_at, id);`,
	`CREATE TRIGGER trg_specialist_schedule_insert
		BEFORE INSERT ON specialist_schedules
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN run_execution_leases lease ON lease.run_id = run.id
			WHERE run.id = NEW.run_id AND run.status = 'running'
				AND lease.lease_id = NEW.lease_id
				AND lease.generation = NEW.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist schedule requires its active Run lease');
		END;`,
	`CREATE TRIGGER trg_specialist_schedule_agent_insert
		BEFORE INSERT ON specialist_schedule_agents
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_schedules schedule
			JOIN agent_nodes child ON child.run_id = schedule.run_id
				AND child.id = NEW.agent_id
			JOIN agent_nodes parent ON parent.run_id = child.run_id
				AND parent.id = child.parent_id
			WHERE schedule.id = NEW.schedule_id AND schedule.run_id = NEW.run_id
				AND schedule.status = 'running' AND child.role = 'specialist'
				AND parent.role = 'root' AND parent.parent_id IS NULL
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist schedule target must be a direct child');
		END;`,
	`CREATE TRIGGER trg_specialist_schedule_terminal
		BEFORE UPDATE OF status, stop_reason, rounds_completed, turns_started,
			recovered_attempts, after_root_tokens, after_specialist_tokens,
			after_total_tokens, after_root_execution_millis,
			after_specialist_execution_millis, after_total_execution_millis,
			error_code, finished_at ON specialist_schedules
		WHEN OLD.status <> 'running' OR NEW.status = 'running' OR NOT EXISTS (
			SELECT 1 FROM run_execution_leases lease
			WHERE lease.run_id = OLD.run_id AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday('now')
				AND ((lease.lease_id = OLD.lease_id
					AND lease.generation = OLD.lease_generation)
					OR (NEW.status = 'abandoned'
						AND lease.generation > OLD.lease_generation))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist schedule terminal transition is not fenced');
		END;`,
	`CREATE TRIGGER trg_specialist_schedule_identity_immutable
		BEFORE UPDATE OF id, run_id, lease_id, lease_generation, max_rounds,
			before_root_tokens, before_specialist_tokens, before_total_tokens,
			before_root_execution_millis, before_specialist_execution_millis,
			before_total_execution_millis, started_at ON specialist_schedules
		BEGIN
			SELECT RAISE(ABORT, 'Specialist schedule identity is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_schedule_terminal_immutable
		BEFORE UPDATE ON specialist_schedules
		WHEN OLD.status <> 'running'
		BEGIN
			SELECT RAISE(ABORT, 'terminal Specialist schedule is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_schedule_agent_immutable
		BEFORE UPDATE ON specialist_schedule_agents
		BEGIN
			SELECT RAISE(ABORT, 'Specialist schedule target is immutable');
		END;`,
	`CREATE TABLE specialist_model_cancellations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		agent_attempt_id TEXT NOT NULL,
		model_attempt INTEGER NOT NULL,
		status TEXT NOT NULL,
		reason TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		requested_at TEXT NOT NULL,
		observed_at TEXT,
		resolved_at TEXT,
		resolution TEXT NOT NULL DEFAULT '',
		FOREIGN KEY(agent_attempt_id, model_attempt)
			REFERENCES specialist_model_calls(agent_attempt_id, model_attempt_number) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		UNIQUE(run_id, agent_id, agent_attempt_id, model_attempt),
		CHECK(model_attempt > 0),
		CHECK(status IN ('pending', 'observed', 'resolved')),
		CHECK(length(trim(reason)) BETWEEN 1 AND 1024),
		CHECK(length(CAST(reason AS BLOB)) <= 4096),
		CHECK(length(trim(requested_by)) BETWEEN 1 AND 256),
		CHECK((status = 'pending' AND observed_at IS NULL AND resolved_at IS NULL AND resolution = '')
			OR (status = 'observed' AND observed_at IS NOT NULL
				AND resolved_at IS NULL AND resolution = '')
			OR (status = 'resolved' AND resolved_at IS NOT NULL
				AND length(trim(resolution)) BETWEEN 1 AND 64))
	);`,
	`CREATE INDEX idx_specialist_model_cancellations_pending
		ON specialist_model_cancellations(run_id, agent_id, agent_attempt_id,
			model_attempt, status);`,
	`CREATE TABLE specialist_model_cancellation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		cancellation_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(cancellation_id) REFERENCES specialist_model_cancellations(id) ON DELETE CASCADE
	);`,
	`CREATE TRIGGER trg_specialist_model_cancellation_operation_immutable
		BEFORE UPDATE ON specialist_model_cancellation_operations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model cancellation operation is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_model_cancellation_insert
		BEFORE INSERT ON specialist_model_cancellations
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_model_calls call
			JOIN agent_attempts attempt ON attempt.id = call.agent_attempt_id
			JOIN agent_nodes child ON child.run_id = attempt.run_id
				AND child.id = attempt.agent_id
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE call.agent_attempt_id = NEW.agent_attempt_id
				AND call.model_attempt_number = NEW.model_attempt
				AND call.run_id = NEW.run_id AND call.agent_id = NEW.agent_id
				AND call.status = 'started' AND attempt.run_id = NEW.run_id
				AND attempt.agent_id = NEW.agent_id AND attempt.status = 'running'
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.active_attempt_id = attempt.id AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND NOT EXISTS (SELECT 1 FROM specialist_model_calls later
					WHERE later.agent_attempt_id = call.agent_attempt_id
						AND later.model_attempt_number > call.model_attempt_number)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model cancellation requires the latest active call');
		END;`,
	`CREATE TRIGGER trg_specialist_model_cancellation_transition
		BEFORE UPDATE OF status, observed_at, resolved_at, resolution
		ON specialist_model_cancellations
		WHEN NOT ((OLD.status = 'pending' AND NEW.status IN ('observed', 'resolved'))
			OR (OLD.status = 'observed' AND NEW.status = 'resolved'))
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model cancellation transition is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_model_cancellation_observe
		BEFORE UPDATE OF status ON specialist_model_cancellations
		WHEN NEW.status = 'observed' AND NOT EXISTS (
			SELECT 1 FROM specialist_model_calls call
			JOIN agent_attempts attempt ON attempt.id = call.agent_attempt_id
			JOIN agent_nodes child ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			WHERE call.agent_attempt_id = OLD.agent_attempt_id
				AND call.model_attempt_number = OLD.model_attempt
				AND call.status = 'started' AND attempt.status = 'running'
				AND child.status = 'running' AND child.active_attempt_id = attempt.id
				AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model cancellation observation is not fenced');
		END;`,
	`CREATE TRIGGER trg_specialist_model_cancellation_resolve
		BEFORE UPDATE OF status ON specialist_model_cancellations
		WHEN NEW.status = 'resolved' AND NOT (
			EXISTS (SELECT 1 FROM specialist_model_calls call
				WHERE call.agent_attempt_id = OLD.agent_attempt_id
					AND call.model_attempt_number = OLD.model_attempt
					AND call.status <> 'started' AND call.outcome = NEW.resolution)
			OR (NEW.resolution IN ('attempt_terminated', 'worker_lost', 'superseded')
				AND EXISTS (SELECT 1 FROM agent_attempts attempt
					WHERE attempt.id = OLD.agent_attempt_id AND attempt.status <> 'running'))
			OR (NEW.resolution = 'superseded' AND EXISTS (
				SELECT 1 FROM specialist_model_calls later
				WHERE later.agent_attempt_id = OLD.agent_attempt_id
					AND later.model_attempt_number > OLD.model_attempt))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model cancellation resolution is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_model_cancellation_identity_immutable
		BEFORE UPDATE OF id, run_id, agent_id, agent_attempt_id, model_attempt,
			reason, requested_by, requested_at ON specialist_model_cancellations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist model cancellation identity is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_model_cancellation_terminal_immutable
		BEFORE UPDATE ON specialist_model_cancellations
		WHEN OLD.status = 'resolved'
		BEGIN
			SELECT RAISE(ABORT, 'resolved Specialist model cancellation is immutable');
		END;`,
}

var specialistContextDeliveryStatements = []string{
	`CREATE TABLE specialist_context_deliveries (
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		parent_agent_id TEXT NOT NULL,
		agent_attempt_id TEXT NOT NULL,
		turn_number INTEGER NOT NULL,
		message_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		status TEXT NOT NULL,
		prepared_at TEXT NOT NULL,
		resolved_at TEXT,
		PRIMARY KEY(agent_attempt_id, message_id),
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, parent_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		FOREIGN KEY(agent_attempt_id) REFERENCES agent_attempts(id) ON DELETE CASCADE,
		FOREIGN KEY(message_id) REFERENCES agent_messages(id) ON DELETE CASCADE,
		UNIQUE(agent_attempt_id, ordinal),
		CHECK(turn_number > 0),
		CHECK(ordinal BETWEEN 1 AND 4),
		CHECK(status IN ('prepared', 'committed', 'superseded')),
		CHECK((status = 'prepared' AND resolved_at IS NULL)
			OR (status IN ('committed', 'superseded') AND resolved_at IS NOT NULL))
	);`,
	`CREATE UNIQUE INDEX idx_specialist_context_one_prepared_message
		ON specialist_context_deliveries(message_id) WHERE status = 'prepared';`,
	`CREATE UNIQUE INDEX idx_specialist_context_one_committed_message
		ON specialist_context_deliveries(message_id) WHERE status = 'committed';`,
	`CREATE INDEX idx_specialist_context_run_status
		ON specialist_context_deliveries(run_id, status, prepared_at);`,
	`CREATE TRIGGER trg_specialist_context_delivery_insert
		BEFORE INSERT ON specialist_context_deliveries
		WHEN NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN agent_nodes parent
				ON parent.run_id = attempt.run_id AND parent.id = attempt.parent_agent_id
			JOIN agent_messages message ON message.id = NEW.message_id
			WHERE attempt.id = NEW.agent_attempt_id
				AND attempt.run_id = NEW.run_id AND attempt.agent_id = NEW.agent_id
				AND attempt.parent_agent_id = NEW.parent_agent_id
				AND attempt.turn_number = NEW.turn_number AND attempt.status = 'running'
				AND attempt.usage_recorded_at IS NULL AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.active_attempt_id = attempt.id AND child.parent_id = parent.id
				AND parent.role = 'root' AND parent.status IN ('ready', 'running', 'waiting')
				AND message.run_id = NEW.run_id
				AND message.sender_agent_id = parent.id
				AND message.recipient_agent_id = child.id
				AND message.kind = 'instruction' AND message.semantic = 'message'
				AND message.status = 'pending'
				AND json_valid(message.payload_json)
				AND json_type(message.payload_json) = 'object'
				AND json_extract(message.payload_json, '$.version') = 'specialist_instruction.v1'
				AND json_type(message.payload_json, '$.instruction') = 'text'
				AND length(trim(json_extract(message.payload_json, '$.instruction'))) BETWEEN 1 AND 1200
				AND (SELECT COUNT(*) FROM json_each(message.payload_json)) = 2
				AND NOT EXISTS (
					SELECT 1 FROM json_each(message.payload_json) field
					WHERE field.key NOT IN ('version', 'instruction')
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist context delivery requires an eligible pending parent instruction');
		END;`,
	`CREATE TRIGGER trg_specialist_context_delivery_commit
		BEFORE UPDATE OF status, resolved_at ON specialist_context_deliveries
		WHEN OLD.status = 'prepared' AND NEW.status = 'committed' AND NOT EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN runs run ON run.id = attempt.run_id
			JOIN run_execution_leases lease ON lease.run_id = attempt.run_id
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			JOIN agent_messages message ON message.id = OLD.message_id
			WHERE attempt.id = OLD.agent_attempt_id
				AND attempt.run_id = OLD.run_id AND attempt.agent_id = OLD.agent_id
				AND attempt.parent_agent_id = OLD.parent_agent_id
				AND attempt.turn_number = OLD.turn_number AND attempt.status = 'running'
				AND attempt.usage_recorded_at IS NOT NULL AND run.status = 'running'
				AND lease.lease_id = attempt.lease_id
				AND lease.generation = attempt.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND child.status = 'running' AND child.active_attempt_id = attempt.id
				AND message.run_id = OLD.run_id
				AND message.sender_agent_id = OLD.parent_agent_id
				AND message.recipient_agent_id = child.id
				AND message.kind = 'instruction' AND message.semantic = 'message'
				AND message.status = 'pending'
				AND json_valid(message.payload_json)
				AND json_type(message.payload_json) = 'object'
				AND json_extract(message.payload_json, '$.version') = 'specialist_instruction.v1'
				AND json_type(message.payload_json, '$.instruction') = 'text'
				AND length(trim(json_extract(message.payload_json, '$.instruction'))) BETWEEN 1 AND 1200
				AND (SELECT COUNT(*) FROM json_each(message.payload_json)) = 2
				AND NOT EXISTS (
					SELECT 1 FROM json_each(message.payload_json) field
					WHERE field.key NOT IN ('version', 'instruction')
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist context commit requires its active usage-recorded attempt');
		END;`,
	`CREATE TRIGGER trg_specialist_context_delivery_active_supersede
		BEFORE UPDATE OF status ON specialist_context_deliveries
		WHEN OLD.status = 'prepared' AND NEW.status = 'superseded' AND EXISTS (
			SELECT 1 FROM agent_attempts attempt
			JOIN runs run ON run.id = attempt.run_id
			JOIN agent_nodes child
				ON child.run_id = attempt.run_id AND child.id = attempt.agent_id
			WHERE attempt.id = OLD.agent_attempt_id AND attempt.run_id = OLD.run_id
				AND attempt.status = 'running' AND run.status = 'running'
				AND child.status = 'running' AND child.active_attempt_id = attempt.id
		)
		BEGIN
			SELECT RAISE(ABORT, 'active Specialist context delivery cannot be superseded');
		END;`,
	`CREATE TRIGGER trg_specialist_context_delivery_identity_immutable
		BEFORE UPDATE OF run_id, agent_id, parent_agent_id, agent_attempt_id, turn_number,
			message_id, ordinal, prepared_at ON specialist_context_deliveries
		BEGIN
			SELECT RAISE(ABORT, 'Specialist context delivery identity is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_context_delivery_terminal_immutable
		BEFORE UPDATE ON specialist_context_deliveries
		WHEN OLD.status <> 'prepared'
		BEGIN
			SELECT RAISE(ABORT, 'terminal Specialist context delivery is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_context_delivery_prepared_delete
		BEFORE DELETE ON specialist_context_deliveries
		WHEN OLD.status = 'prepared'
		BEGIN
			SELECT RAISE(ABORT, 'prepared Specialist context delivery cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_agent_message_prepared_specialist_delivery
		BEFORE UPDATE OF status, consumed_at ON agent_messages
		WHEN OLD.status = 'pending' AND NEW.status = 'consumed' AND EXISTS (
			SELECT 1 FROM specialist_context_deliveries delivery
			WHERE delivery.message_id = OLD.id AND delivery.status = 'prepared'
		)
		BEGIN
			SELECT RAISE(ABORT, 'prepared Specialist instruction must commit through its Agent attempt');
		END;`,
}

var specialistDelegationProposalStatements = []string{
	`DROP TRIGGER trg_supervisor_tool_call_model_attempt;`,
	`DROP TRIGGER trg_supervisor_tool_round_completion;`,
	`DROP INDEX idx_run_supervisor_tool_calls_pending;`,
	`ALTER TABLE run_supervisor_tool_calls RENAME TO run_supervisor_tool_calls_v29;`,
	`CREATE TABLE run_supervisor_tool_calls (
		run_id TEXT NOT NULL,
		turn INTEGER NOT NULL,
		attempt_id TEXT NOT NULL,
		round INTEGER NOT NULL,
		position INTEGER NOT NULL,
		model_attempt INTEGER NOT NULL,
		call_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		status TEXT NOT NULL,
		result_json TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		completed_at TEXT,
		PRIMARY KEY(run_id, turn, attempt_id, round, position),
		UNIQUE(run_id, turn, attempt_id, call_id),
		FOREIGN KEY(run_id, turn, attempt_id, round)
			REFERENCES run_supervisor_tool_rounds(run_id, turn, attempt_id, round) ON DELETE CASCADE,
		CHECK(position BETWEEN 1 AND 4),
		CHECK(model_attempt > 0),
		CHECK(tool_name IN ('work_item_create', 'note_create', 'specialist_delegation_propose')),
		CHECK(status IN ('pending', 'completed', 'denied', 'failed')),
		CHECK((status = 'pending' AND result_json = '' AND error_code = '' AND completed_at IS NULL)
			OR (status = 'completed' AND length(result_json) > 0 AND error_code = '' AND completed_at IS NOT NULL)
			OR (status IN ('denied', 'failed') AND length(result_json) > 0 AND length(error_code) > 0
				AND completed_at IS NOT NULL))
	);`,
	`INSERT INTO run_supervisor_tool_calls
		(run_id, turn, attempt_id, round, position, model_attempt, call_id, tool_name,
		payload_json, status, result_json, error_code, created_at, completed_at)
		SELECT run_id, turn, attempt_id, round, position, model_attempt, call_id, tool_name,
		payload_json, status, result_json, error_code, created_at, completed_at
		FROM run_supervisor_tool_calls_v29;`,
	`DROP TABLE run_supervisor_tool_calls_v29;`,
	`CREATE INDEX idx_run_supervisor_tool_calls_pending
		ON run_supervisor_tool_calls(run_id, turn, attempt_id, status, round, position);`,
	`CREATE TRIGGER trg_supervisor_tool_call_model_attempt
		BEFORE INSERT ON run_supervisor_tool_calls
		WHEN NOT EXISTS (
			SELECT 1 FROM run_supervisor_tool_rounds
			WHERE run_id = NEW.run_id AND turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND round = NEW.round AND model_attempt = NEW.model_attempt
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool call model attempt mismatch');
		END;`,
	`CREATE TRIGGER trg_supervisor_tool_round_completion
		BEFORE UPDATE OF completed_at ON run_supervisor_tool_rounds
		WHEN NEW.completed_at IS NOT NULL AND EXISTS (
			SELECT 1 FROM run_supervisor_tool_calls
			WHERE run_id = NEW.run_id AND turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND round = NEW.round AND status = 'pending'
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool round still has pending calls');
		END;`,
	`CREATE TABLE specialist_delegation_proposals (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		protocol_version TEXT NOT NULL,
		status TEXT NOT NULL,
		assignment_count INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		CHECK(protocol_version = 'specialist_delegation.v1'),
		CHECK(status = 'proposed'),
		CHECK(assignment_count BETWEEN 1 AND 2),
		CHECK(length(trim(requested_by)) BETWEEN 1 AND 256),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_specialist_delegation_proposals_run_created
		ON specialist_delegation_proposals(run_id, created_at, id);`,
	`CREATE TABLE specialist_delegation_assignments (
		proposal_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		title TEXT NOT NULL,
		goal TEXT NOT NULL,
		skills_json TEXT NOT NULL,
		turn_limit INTEGER NOT NULL,
		token_limit INTEGER NOT NULL,
		PRIMARY KEY(proposal_id, ordinal),
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE CASCADE,
		CHECK(ordinal BETWEEN 1 AND 2),
		CHECK(title = trim(title) AND length(title) BETWEEN 1 AND 256),
		CHECK(length(CAST(title AS BLOB)) <= 1024),
		CHECK(goal = trim(goal) AND length(goal) BETWEEN 1 AND 1200),
		CHECK(length(CAST(goal AS BLOB)) <= 4800),
		CHECK(json_valid(skills_json) AND json_type(skills_json) = 'array'),
		CHECK(json_array_length(skills_json) BETWEEN 1 AND 16),
		CHECK(turn_limit > 0),
		CHECK(token_limit > 0)
	);`,
	`CREATE TABLE specialist_delegation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		invocation_id TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		root_agent_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(invocation_id) REFERENCES run_tool_calls(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE CASCADE,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE CASCADE,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = 'run_supervisor')
	);`,
	`CREATE TRIGGER trg_specialist_delegation_proposal_insert
		BEFORE INSERT ON specialist_delegation_proposals
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN agent_nodes root ON root.run_id = run.id AND root.id = NEW.root_agent_id
			WHERE run.id = NEW.run_id AND run.status = 'running'
				AND run.session_id = NEW.session_id
				AND COALESCE(mission.workspace_id, '') = NEW.workspace_id
				AND root.role = 'root' AND root.parent_id IS NULL AND root.depth = 0
				AND root.status = 'running' AND root.active_attempt_id <> ''
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation proposal requires the active root Agent');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_assignment_insert
		BEFORE INSERT ON specialist_delegation_assignments
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_proposals proposal
			JOIN agent_nodes root ON root.run_id = proposal.run_id AND root.id = proposal.root_agent_id
			WHERE proposal.id = NEW.proposal_id AND NEW.ordinal <= proposal.assignment_count
				AND NOT EXISTS (
					SELECT 1 FROM json_each(NEW.skills_json) requested
					WHERE requested.type <> 'text'
						OR requested.value <> lower(trim(requested.value))
						OR length(requested.value) NOT BETWEEN 1 AND 96
						OR requested.value GLOB '*[^a-z0-9._-]*'
						OR requested.value = 'specialist_delegation_propose'
						OR NOT EXISTS (
							SELECT 1 FROM json_each(root.skills_json) allowed
							WHERE allowed.type = 'text' AND allowed.value = requested.value
						)
						OR EXISTS (
							SELECT 1 FROM json_each(NEW.skills_json) later
							WHERE CAST(later.key AS INTEGER) > CAST(requested.key AS INTEGER)
								AND requested.value >= later.value
						)
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation assignment exceeds root capability');
		END;`,
	`CREATE TRIGGER trg_specialist_non_delegable_capability
		BEFORE INSERT ON agent_nodes
		WHEN NEW.role = 'specialist' AND EXISTS (
			SELECT 1 FROM json_each(NEW.skills_json) skill
			WHERE skill.type = 'text' AND skill.value = 'specialist_delegation_propose'
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist includes a non-delegable control capability');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_operation_insert
		BEFORE INSERT ON specialist_delegation_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_proposals proposal
			JOIN runs run ON run.id = proposal.run_id
			JOIN agent_nodes root ON root.run_id = proposal.run_id AND root.id = proposal.root_agent_id
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = proposal.run_id
			JOIN run_execution_leases lease ON lease.run_id = proposal.run_id
			JOIN run_tool_calls invocation ON invocation.id = NEW.invocation_id
			WHERE proposal.id = NEW.proposal_id AND proposal.run_id = NEW.run_id
				AND proposal.root_agent_id = NEW.root_agent_id
				AND proposal.session_id = NEW.session_id
				AND proposal.workspace_id = NEW.workspace_id
				AND proposal.requested_by = NEW.requested_by
				AND run.status = 'running' AND root.status = 'running'
				AND EXISTS (SELECT 1 FROM json_each(root.skills_json) root_skill
					WHERE root_skill.type = 'text'
						AND root_skill.value = 'specialist_delegation_propose')
				AND root.active_attempt_id = checkpoint.attempt_id
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.lease_id = lease.lease_id
				AND checkpoint.lease_generation = lease.generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND invocation.run_id = NEW.run_id AND invocation.session_id = NEW.session_id
				AND invocation.workspace_id = NEW.workspace_id
				AND invocation.tool_name = 'specialist_delegation_propose'
				AND invocation.action_class = 'agent_proposal'
				AND (SELECT COUNT(*) FROM specialist_delegation_assignments assignment
					WHERE assignment.proposal_id = proposal.id) = proposal.assignment_count
				AND (SELECT COUNT(*) FROM agent_nodes child
					WHERE child.run_id = proposal.run_id AND child.parent_id = root.id)
					+ proposal.assignment_count <= 2
				AND COALESCE((SELECT SUM(assignment.turn_limit)
					FROM specialist_delegation_assignments assignment
					WHERE assignment.proposal_id = proposal.id), 0)
					<= root.turn_limit - root.turns_used - 2
				AND (root.token_limit = 0 OR COALESCE((SELECT SUM(assignment.token_limit)
					FROM specialist_delegation_assignments assignment
					WHERE assignment.proposal_id = proposal.id), 0)
					< root.token_limit - root.tokens_used)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation operation is not authorized or exceeds capacity');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_proposal_immutable
		BEFORE UPDATE ON specialist_delegation_proposals
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation proposal is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_assignment_immutable
		BEFORE UPDATE ON specialist_delegation_assignments
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation assignment is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_operation_immutable
		BEFORE UPDATE ON specialist_delegation_operations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation operation is immutable');
		END;`,
}

var specialistDelegationReviewStatements = []string{
	`CREATE TABLE specialist_delegation_reviews (
		id TEXT PRIMARY KEY,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		decision TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		reviewed_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		CHECK(decision IN ('approved', 'rejected')),
		CHECK(reason = trim(reason) AND instr(reason, char(0)) = 0
			AND length(reason) <= 2048 AND length(CAST(reason AS BLOB)) <= 8192),
		CHECK(decision = 'approved' OR length(reason) > 0),
		CHECK(reviewed_by = trim(reviewed_by) AND length(reviewed_by) BETWEEN 1 AND 256
			AND instr(reviewed_by, char(0)) = 0),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_specialist_delegation_reviews_run_created
		ON specialist_delegation_reviews(run_id, created_at, id);`,
	`CREATE TABLE specialist_delegation_review_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		review_id TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		reviewed_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(review_id) REFERENCES specialist_delegation_reviews(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(reviewed_by = trim(reviewed_by) AND length(reviewed_by) BETWEEN 1 AND 256
			AND instr(reviewed_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_specialist_delegation_review_insert
		BEFORE INSERT ON specialist_delegation_reviews
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_proposals proposal
			JOIN runs run ON run.id = proposal.run_id
			JOIN agent_nodes root ON root.run_id = proposal.run_id
				AND root.id = proposal.root_agent_id
			WHERE proposal.id = NEW.proposal_id AND proposal.run_id = NEW.run_id
				AND proposal.root_agent_id = NEW.root_agent_id
				AND proposal.status = 'proposed'
				AND julianday(NEW.created_at) >= julianday(proposal.created_at)
				AND root.role = 'root' AND root.parent_id IS NULL
				AND (NEW.decision = 'rejected' OR run.status = 'running')
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation review binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_review_operation_insert
		BEFORE INSERT ON specialist_delegation_review_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_reviews review
			WHERE review.id = NEW.review_id AND review.proposal_id = NEW.proposal_id
				AND review.run_id = NEW.run_id AND review.reviewed_by = NEW.reviewed_by
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation review operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_review_immutable
		BEFORE UPDATE ON specialist_delegation_reviews
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation review is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_review_delete_immutable
		BEFORE DELETE ON specialist_delegation_reviews
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation review cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_review_operation_immutable
		BEFORE UPDATE ON specialist_delegation_review_operations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation review operation is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_review_operation_delete_immutable
		BEFORE DELETE ON specialist_delegation_review_operations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation review operation cannot be deleted');
		END;`,
}

var specialistDelegationApplicationStatements = []string{
	`CREATE TABLE specialist_delegation_applications (
		id TEXT PRIMARY KEY,
		review_id TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		status TEXT NOT NULL,
		assignment_count INTEGER NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		max_children INTEGER NOT NULL,
		max_turns_per_child INTEGER NOT NULL,
		max_tokens_per_child INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		stop_code TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		completed_at TEXT,
		FOREIGN KEY(review_id) REFERENCES specialist_delegation_reviews(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		CHECK(status IN ('applying', 'applied', 'aborted')),
		CHECK(assignment_count BETWEEN 1 AND 2),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(max_children BETWEEN 1 AND 2),
		CHECK(max_turns_per_child > 0),
		CHECK(max_tokens_per_child > 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK((status = 'applying' AND stop_code = '' AND version = 1 AND completed_at IS NULL)
			OR (status = 'applied' AND stop_code = '' AND version = 2 AND completed_at IS NOT NULL)
			OR (status = 'aborted' AND length(stop_code) > 0 AND version = 2 AND completed_at IS NOT NULL)),
		CHECK(julianday(updated_at) >= julianday(created_at)),
		CHECK(completed_at IS NULL OR julianday(completed_at) = julianday(updated_at))
	);`,
	`CREATE UNIQUE INDEX idx_specialist_delegation_applications_running
		ON specialist_delegation_applications(run_id) WHERE status = 'applying';`,
	`CREATE INDEX idx_specialist_delegation_applications_run_created
		ON specialist_delegation_applications(run_id, created_at, id);`,
	`CREATE TABLE specialist_delegation_application_assignments (
		application_id TEXT NOT NULL,
		proposal_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		status TEXT NOT NULL,
		admission_operation_digest TEXT NOT NULL UNIQUE,
		instruction_operation_digest TEXT NOT NULL UNIQUE,
		agent_id TEXT UNIQUE,
		message_id TEXT UNIQUE,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(application_id, ordinal),
		FOREIGN KEY(application_id) REFERENCES specialist_delegation_applications(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id, ordinal) REFERENCES specialist_delegation_assignments(proposal_id, ordinal) ON DELETE RESTRICT,
		FOREIGN KEY(agent_id) REFERENCES agent_nodes(id) ON DELETE RESTRICT,
		FOREIGN KEY(message_id) REFERENCES agent_messages(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 2),
		CHECK(status IN ('pending', 'admitted', 'instructed')),
		CHECK(length(admission_operation_digest) = 64
			AND admission_operation_digest = lower(admission_operation_digest)
			AND admission_operation_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(instruction_operation_digest) = 64
			AND instruction_operation_digest = lower(instruction_operation_digest)
			AND instruction_operation_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK((status = 'pending' AND agent_id IS NULL AND message_id IS NULL AND version = 1)
			OR (status = 'admitted' AND agent_id IS NOT NULL AND message_id IS NULL AND version = 2)
			OR (status = 'instructed' AND agent_id IS NOT NULL AND message_id IS NOT NULL AND version = 3)),
		CHECK(julianday(updated_at) >= julianday(created_at))
	);`,
	`CREATE TABLE specialist_delegation_application_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		application_id TEXT NOT NULL UNIQUE,
		review_id TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(application_id) REFERENCES specialist_delegation_applications(id) ON DELETE RESTRICT,
		FOREIGN KEY(review_id) REFERENCES specialist_delegation_reviews(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_specialist_delegation_application_insert
		BEFORE INSERT ON specialist_delegation_applications
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_reviews review
			JOIN specialist_delegation_review_operations review_operation
				ON review_operation.review_id = review.id AND review_operation.proposal_id = review.proposal_id
			JOIN specialist_delegation_proposals proposal ON proposal.id = review.proposal_id
			JOIN runs run ON run.id = proposal.run_id
			JOIN agent_nodes root ON root.run_id = proposal.run_id AND root.id = proposal.root_agent_id
			JOIN sessions root_session ON root_session.id = root.session_id
			WHERE review.id = NEW.review_id AND review.proposal_id = NEW.proposal_id
				AND review.run_id = NEW.run_id AND review.root_agent_id = NEW.root_agent_id
				AND review.decision = 'approved' AND review.reviewed_by = NEW.requested_by
				AND proposal.run_id = NEW.run_id AND proposal.root_agent_id = NEW.root_agent_id
				AND proposal.status = 'proposed' AND proposal.assignment_count = NEW.assignment_count
				AND run.status = 'running' AND root.role = 'root' AND root.parent_id IS NULL
				AND root.status = 'ready' AND root.active_attempt_id = ''
				AND root_session.status = 'active'
				AND root.child_limit IN (0, NEW.max_children)
				AND julianday(NEW.created_at) >= julianday(review.created_at)
				AND (SELECT COUNT(*) FROM agent_nodes child
					WHERE child.run_id = NEW.run_id AND child.parent_id = NEW.root_agent_id)
					+ NEW.assignment_count <= NEW.max_children
				AND NOT EXISTS (
					SELECT 1 FROM specialist_delegation_assignments assignment
					WHERE assignment.proposal_id = NEW.proposal_id
						AND (assignment.turn_limit > NEW.max_turns_per_child
							OR assignment.token_limit > NEW.max_tokens_per_child
							OR EXISTS (
								SELECT 1 FROM json_each(assignment.skills_json) requested
								WHERE requested.type != 'text'
									OR requested.value = 'specialist_delegation_propose'
									OR NOT EXISTS (SELECT 1 FROM json_each(root.skills_json) available
										WHERE available.type = 'text' AND available.value = requested.value))))
				AND root.turns_used
					+ COALESCE((SELECT SUM(child.turn_limit) FROM agent_nodes child
						WHERE child.run_id = NEW.run_id AND child.parent_id = NEW.root_agent_id), 0)
					+ (SELECT SUM(assignment.turn_limit) FROM specialist_delegation_assignments assignment
						WHERE assignment.proposal_id = NEW.proposal_id)
					< json_extract(run.budget_json, '$.max_turns')
				AND (COALESCE(json_extract(run.budget_json, '$.max_tokens'), 0) = 0
					OR root.tokens_used
						+ COALESCE((SELECT SUM(child.token_limit) FROM agent_nodes child
							WHERE child.run_id = NEW.run_id AND child.parent_id = NEW.root_agent_id), 0)
						+ (SELECT SUM(assignment.token_limit) FROM specialist_delegation_assignments assignment
							WHERE assignment.proposal_id = NEW.proposal_id)
						< json_extract(run.budget_json, '$.max_tokens'))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_assignment_insert
		BEFORE INSERT ON specialist_delegation_application_assignments
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_applications application
			JOIN specialist_delegation_assignments assignment
				ON assignment.proposal_id = application.proposal_id AND assignment.ordinal = NEW.ordinal
			WHERE application.id = NEW.application_id
				AND application.proposal_id = NEW.proposal_id
				AND application.status = 'applying'
				AND NEW.status = 'pending' AND NEW.agent_id IS NULL AND NEW.message_id IS NULL
				AND NEW.created_at = application.created_at
				AND NEW.updated_at = application.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application assignment binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_operation_insert
		BEFORE INSERT ON specialist_delegation_application_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_applications application
			WHERE application.id = NEW.application_id AND application.review_id = NEW.review_id
				AND application.proposal_id = NEW.proposal_id AND application.run_id = NEW.run_id
				AND application.requested_by = NEW.requested_by
				AND NEW.created_at = application.created_at
				AND (SELECT COUNT(*) FROM specialist_delegation_application_assignments assignment
					WHERE assignment.application_id = application.id) = application.assignment_count
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_assignment_transition
		BEFORE UPDATE ON specialist_delegation_application_assignments
		WHEN NEW.application_id != OLD.application_id OR NEW.proposal_id != OLD.proposal_id
			OR NEW.ordinal != OLD.ordinal
			OR NEW.admission_operation_digest != OLD.admission_operation_digest
			OR NEW.instruction_operation_digest != OLD.instruction_operation_digest
			OR NEW.created_at != OLD.created_at
			OR julianday(NEW.updated_at) < julianday(OLD.updated_at)
			OR NOT ((OLD.status = 'pending' AND NEW.status = 'admitted'
					AND NEW.agent_id IS NOT NULL AND NEW.message_id IS NULL
					AND NEW.version = OLD.version + 1)
				OR (OLD.status = 'admitted' AND NEW.status = 'instructed'
					AND NEW.agent_id = OLD.agent_id AND NEW.message_id IS NOT NULL
					AND NEW.version = OLD.version + 1))
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application assignment transition is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_assignment_admitted
		BEFORE UPDATE ON specialist_delegation_application_assignments
		WHEN NEW.status = 'admitted' AND NOT EXISTS (
			SELECT 1 FROM specialist_delegation_applications application
			JOIN specialist_delegation_application_operations application_operation
				ON application_operation.application_id = application.id
				AND application_operation.proposal_id = application.proposal_id
			JOIN specialist_delegation_assignments proposed
				ON proposed.proposal_id = application.proposal_id AND proposed.ordinal = NEW.ordinal
			JOIN agent_nodes child ON child.id = NEW.agent_id
			JOIN sessions child_session ON child_session.id = child.session_id
			JOIN agent_admission_operations admission
				ON admission.agent_id = child.id
				AND admission.operation_key_digest = NEW.admission_operation_digest
			WHERE application.id = NEW.application_id AND application.status = 'applying'
				AND child.run_id = application.run_id AND child.parent_id = application.root_agent_id
				AND child.role = 'specialist' AND child.depth = 1
				AND child.turn_limit = proposed.turn_limit AND child.token_limit = proposed.token_limit
				AND child.skills_json = proposed.skills_json AND child_session.title = proposed.title
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation admitted Agent binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_assignment_instructed
		BEFORE UPDATE ON specialist_delegation_application_assignments
		WHEN NEW.status = 'instructed' AND NOT EXISTS (
			SELECT 1 FROM specialist_delegation_applications application
			JOIN specialist_delegation_application_operations application_operation
				ON application_operation.application_id = application.id
				AND application_operation.proposal_id = application.proposal_id
			JOIN specialist_delegation_assignments proposed
				ON proposed.proposal_id = application.proposal_id AND proposed.ordinal = NEW.ordinal
			JOIN agent_messages message ON message.id = NEW.message_id
			JOIN agent_message_operations operation ON operation.message_id = message.id
				AND operation.operation_key_digest = NEW.instruction_operation_digest
			WHERE application.id = NEW.application_id AND application.status = 'applying'
				AND message.run_id = application.run_id
				AND message.sender_agent_id = application.root_agent_id
				AND message.recipient_agent_id = NEW.agent_id
				AND message.kind = 'instruction' AND message.semantic = 'message'
				AND message.status = 'pending' AND json_valid(message.payload_json)
				AND json_type(message.payload_json) = 'object'
				AND json_extract(message.payload_json, '$.version') = 'specialist_instruction.v1'
				AND json_extract(message.payload_json, '$.instruction') = proposed.goal
				AND (SELECT COUNT(*) FROM json_each(message.payload_json)) = 2
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation instruction binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_complete
		BEFORE UPDATE ON specialist_delegation_applications
		WHEN NEW.status = 'applied' AND (
			NOT EXISTS (SELECT 1 FROM specialist_delegation_application_operations operation
				WHERE operation.application_id = NEW.id
					AND operation.proposal_id = NEW.proposal_id)
			OR
			(SELECT COUNT(*) FROM specialist_delegation_application_assignments assignment
			WHERE assignment.application_id = NEW.id AND assignment.status = 'instructed')
				!= OLD.assignment_count
			OR (NEW.completed_at IS NOT NULL AND julianday(NEW.completed_at) <
				(SELECT MAX(julianday(assignment.updated_at))
				FROM specialist_delegation_application_assignments assignment
				WHERE assignment.application_id = NEW.id))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application is incomplete');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_transition
		BEFORE UPDATE ON specialist_delegation_applications
		WHEN NEW.id != OLD.id OR NEW.review_id != OLD.review_id OR NEW.proposal_id != OLD.proposal_id
			OR NEW.run_id != OLD.run_id OR NEW.root_agent_id != OLD.root_agent_id
			OR NEW.assignment_count != OLD.assignment_count
			OR NEW.policy_fingerprint != OLD.policy_fingerprint
			OR NEW.max_children != OLD.max_children
			OR NEW.max_turns_per_child != OLD.max_turns_per_child
			OR NEW.max_tokens_per_child != OLD.max_tokens_per_child
			OR NEW.requested_by != OLD.requested_by OR NEW.created_at != OLD.created_at
			OR OLD.status != 'applying' OR NEW.status NOT IN ('applied', 'aborted')
			OR NEW.version != OLD.version + 1 OR NEW.completed_at IS NULL
			OR NEW.updated_at != NEW.completed_at
			OR julianday(NEW.completed_at) <
				(SELECT MAX(julianday(assignment.updated_at))
				FROM specialist_delegation_application_assignments assignment
				WHERE assignment.application_id = NEW.id)
			OR (NEW.status = 'applied' AND NEW.stop_code != '')
			OR (NEW.status = 'aborted' AND length(NEW.stop_code) = 0)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application transition is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_delete_immutable
		BEFORE DELETE ON specialist_delegation_applications
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_assignment_delete_immutable
		BEFORE DELETE ON specialist_delegation_application_assignments
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application assignment cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_operation_immutable
		BEFORE UPDATE ON specialist_delegation_application_operations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application operation is immutable');
		END;`,
	`CREATE TRIGGER trg_specialist_delegation_application_operation_delete_immutable
		BEFORE DELETE ON specialist_delegation_application_operations
		BEGIN
			SELECT RAISE(ABORT, 'Specialist delegation application operation cannot be deleted');
		END;`,
}

var readOnlyFanoutPlanStatements = []string{
	`CREATE TABLE readonly_fanout_plans (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		scope_path TEXT NOT NULL,
		goal TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		requested_tier TEXT NOT NULL,
		effective_parallelism INTEGER NOT NULL,
		status TEXT NOT NULL,
		capability_fingerprint TEXT NOT NULL,
		snapshot_digest TEXT NOT NULL,
		file_count INTEGER NOT NULL,
		total_bytes INTEGER NOT NULL,
		excluded_count INTEGER NOT NULL,
		shard_count INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'readonly_fanout.v1'),
		CHECK(requested_tier IN ('auto', '1', '2', '4', '6')),
		CHECK(effective_parallelism BETWEEN 1 AND 6),
		CHECK(status = 'planned'),
		CHECK(capability_fingerprint = '735ca1ca0e0cdf09773b15aa7113e328744c6fde267f469c3f414648baf9e47b'
			AND length(capability_fingerprint) = 64
			AND capability_fingerprint = lower(capability_fingerprint)
			AND capability_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(snapshot_digest) = 64 AND snapshot_digest = lower(snapshot_digest)
			AND snapshot_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(file_count BETWEEN 1 AND 256),
		CHECK(total_bytes BETWEEN 0 AND 786432),
		CHECK(excluded_count BETWEEN 0 AND 20000),
		CHECK(shard_count BETWEEN 1 AND 6 AND shard_count = effective_parallelism),
		CHECK(scope_path = trim(scope_path) AND length(scope_path) BETWEEN 1 AND 2048
			AND instr(scope_path, char(0)) = 0 AND instr(scope_path, char(92)) = 0
			AND substr(scope_path, 1, 1) <> '/' AND scope_path <> '..'
			AND scope_path NOT LIKE '../%' AND instr('/' || scope_path || '/', '/../') = 0),
		CHECK(goal = trim(goal) AND length(goal) BETWEEN 1 AND 4096
			AND length(CAST(goal AS BLOB)) <= 16384 AND instr(goal, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK(version = 1 AND updated_at = created_at)
	);`,
	`CREATE INDEX idx_readonly_fanout_plans_run_created
		ON readonly_fanout_plans(run_id, created_at, id);`,
	`CREATE TABLE readonly_fanout_files (
		plan_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		shard_ordinal INTEGER NOT NULL,
		relative_path TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		content_sha256 TEXT NOT NULL,
		PRIMARY KEY(plan_id, ordinal),
		UNIQUE(plan_id, relative_path),
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 256),
		CHECK(shard_ordinal BETWEEN 1 AND 6),
		CHECK(size_bytes BETWEEN 0 AND 131072),
		CHECK(length(content_sha256) = 64 AND content_sha256 = lower(content_sha256)
			AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(relative_path = trim(relative_path) AND length(relative_path) BETWEEN 1 AND 2048
			AND instr(relative_path, char(0)) = 0 AND instr(relative_path, char(92)) = 0
			AND substr(relative_path, 1, 1) <> '/' AND relative_path NOT IN ('.', '..')
			AND relative_path NOT LIKE '../%'
			AND instr('/' || relative_path || '/', '/../') = 0)
	);`,
	`CREATE INDEX idx_readonly_fanout_files_shard
		ON readonly_fanout_files(plan_id, shard_ordinal, ordinal);`,
	`CREATE TABLE readonly_fanout_shards (
		plan_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		status TEXT NOT NULL,
		file_count INTEGER NOT NULL,
		total_bytes INTEGER NOT NULL,
		input_digest TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(plan_id, ordinal),
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 6),
		CHECK(status = 'pending'),
		CHECK(file_count BETWEEN 1 AND 256),
		CHECK(total_bytes BETWEEN 0 AND 786432),
		CHECK(length(input_digest) = 64 AND input_digest = lower(input_digest)
			AND input_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(version = 1 AND updated_at = created_at)
	);`,
	`CREATE TABLE readonly_fanout_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		plan_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_readonly_fanout_plan_insert
		BEFORE INSERT ON readonly_fanout_plans
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN sessions active_session ON active_session.id = run.session_id
			JOIN workspaces workspace ON workspace.id = NEW.workspace_id
			WHERE run.id = NEW.run_id AND run.status = 'running'
				AND mission.workspace_id = NEW.workspace_id
				AND json_valid(mission.scope_json)
				AND json_extract(mission.scope_json, '$.workspace_id') = NEW.workspace_id
				AND json_extract(mission.scope_json, '$.network_mode') = 'disabled'
				AND active_session.status = 'active'
				AND active_session.workspace_id = NEW.workspace_id
				AND julianday(NEW.created_at) >= julianday(run.created_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out plan binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_file_insert
		BEFORE INSERT ON readonly_fanout_files
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_plans plan
			WHERE plan.id = NEW.plan_id AND plan.status = 'planned'
				AND NEW.ordinal <= plan.file_count
				AND NEW.shard_ordinal <= plan.shard_count
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out file binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_shard_insert
		BEFORE INSERT ON readonly_fanout_shards
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_plans plan
			WHERE plan.id = NEW.plan_id AND plan.status = 'planned'
				AND NEW.ordinal <= plan.shard_count
				AND NEW.created_at = plan.created_at AND NEW.updated_at = plan.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out shard binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_operation_insert
		BEFORE INSERT ON readonly_fanout_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_plans plan
			WHERE plan.id = NEW.plan_id AND plan.run_id = NEW.run_id
				AND plan.workspace_id = NEW.workspace_id
				AND plan.requested_by = NEW.requested_by
				AND plan.created_at = NEW.created_at
				AND (SELECT COUNT(*) FROM readonly_fanout_files file
					WHERE file.plan_id = plan.id) = plan.file_count
				AND (SELECT COALESCE(SUM(file.size_bytes), 0)
					FROM readonly_fanout_files file WHERE file.plan_id = plan.id) = plan.total_bytes
				AND (SELECT COUNT(*) FROM readonly_fanout_shards shard
					WHERE shard.plan_id = plan.id) = plan.shard_count
				AND NOT EXISTS (
					SELECT 1 FROM readonly_fanout_shards shard
					WHERE shard.plan_id = plan.id AND (
						shard.file_count != (SELECT COUNT(*) FROM readonly_fanout_files file
							WHERE file.plan_id = plan.id AND file.shard_ordinal = shard.ordinal)
						OR shard.total_bytes != (SELECT COALESCE(SUM(file.size_bytes), 0)
							FROM readonly_fanout_files file
							WHERE file.plan_id = plan.id AND file.shard_ordinal = shard.ordinal)))
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_plan_immutable
		BEFORE UPDATE ON readonly_fanout_plans
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out plan is immutable');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_plan_delete_immutable
		BEFORE DELETE ON readonly_fanout_plans
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out plan cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_file_immutable
		BEFORE UPDATE ON readonly_fanout_files
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out file is immutable');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_file_delete_immutable
		BEFORE DELETE ON readonly_fanout_files
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out file cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_shard_immutable
		BEFORE UPDATE ON readonly_fanout_shards
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out shard is immutable');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_shard_delete_immutable
		BEFORE DELETE ON readonly_fanout_shards
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out shard cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_operation_immutable
		BEFORE UPDATE ON readonly_fanout_operations
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out operation is immutable');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_operation_delete_immutable
		BEFORE DELETE ON readonly_fanout_operations
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out operation cannot be deleted');
		END;`,
}

var readOnlyFanoutExecutionStatements = []string{
	`ALTER TABLE specialist_schedules ADD COLUMN before_readonly_tokens
		INTEGER NOT NULL DEFAULT 0 CHECK(before_readonly_tokens >= 0);`,
	`ALTER TABLE specialist_schedules ADD COLUMN before_readonly_execution_millis
		INTEGER NOT NULL DEFAULT 0 CHECK(before_readonly_execution_millis >= 0);`,
	`ALTER TABLE specialist_schedules ADD COLUMN after_readonly_tokens
		INTEGER NOT NULL DEFAULT 0 CHECK(after_readonly_tokens >= 0);`,
	`ALTER TABLE specialist_schedules ADD COLUMN after_readonly_execution_millis
		INTEGER NOT NULL DEFAULT 0 CHECK(after_readonly_execution_millis >= 0);`,
	`CREATE TRIGGER trg_specialist_schedule_readonly_usage_insert
		BEFORE INSERT ON specialist_schedules
		WHEN NEW.status = 'running' AND
			(NEW.before_readonly_tokens != NEW.after_readonly_tokens
			 OR NEW.before_readonly_execution_millis != NEW.after_readonly_execution_millis)
		BEGIN
			SELECT RAISE(ABORT, 'running Specialist schedule read-only usage must start equal');
		END;`,
	`CREATE TABLE readonly_fanout_executions (
		id TEXT PRIMARY KEY,
		plan_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		status TEXT NOT NULL,
		parallelism INTEGER NOT NULL,
		max_output_tokens_per_shard INTEGER NOT NULL,
		snapshot_digest TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		stop_code TEXT NOT NULL DEFAULT '',
		lease_id TEXT NOT NULL,
		lease_generation INTEGER NOT NULL,
		version INTEGER NOT NULL,
		started_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		finished_at TEXT,
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(status IN ('running', 'completed', 'failed', 'cancelled')),
		CHECK(parallelism BETWEEN 1 AND 6),
		CHECK(max_output_tokens_per_shard BETWEEN 128 AND 4096),
		CHECK(length(snapshot_digest) = 64 AND snapshot_digest = lower(snapshot_digest)
			AND snapshot_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK(length(stop_code) <= 256 AND instr(stop_code, char(0)) = 0),
		CHECK(lease_id = trim(lease_id) AND length(lease_id) BETWEEN 1 AND 256
			AND instr(lease_id, char(0)) = 0 AND lease_generation > 0),
		CHECK(version > 0 AND julianday(updated_at) >= julianday(started_at)),
		CHECK((status = 'running' AND finished_at IS NULL AND stop_code = '') OR
			(status = 'completed' AND finished_at IS NOT NULL AND stop_code = ''
				AND updated_at = finished_at) OR
			(status IN ('failed', 'cancelled') AND finished_at IS NOT NULL
				AND length(stop_code) > 0 AND updated_at = finished_at))
	);`,
	`CREATE INDEX idx_readonly_fanout_executions_run_started
		ON readonly_fanout_executions(run_id, started_at, id);`,
	`CREATE TABLE readonly_fanout_execution_shards (
		execution_id TEXT NOT NULL,
		plan_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		status TEXT NOT NULL,
		input_digest TEXT NOT NULL,
		attempt_count INTEGER NOT NULL,
		current_attempt INTEGER NOT NULL,
		provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		elapsed_millis INTEGER NOT NULL DEFAULT 0,
		report_json TEXT NOT NULL DEFAULT '',
		report_digest TEXT NOT NULL DEFAULT '',
		finding_count INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',
		error_reason TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		PRIMARY KEY(execution_id, ordinal),
		FOREIGN KEY(execution_id) REFERENCES readonly_fanout_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 6),
		CHECK(status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
		CHECK(length(input_digest) = 64 AND input_digest = lower(input_digest)
			AND input_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(attempt_count BETWEEN 0 AND 3 AND current_attempt BETWEEN 0 AND attempt_count),
		CHECK(length(provider) <= 256 AND length(model) <= 256
			AND instr(provider, char(0)) = 0 AND instr(model, char(0)) = 0),
		CHECK(input_tokens >= 0 AND output_tokens >= 0 AND total_tokens >= 0
			AND total_tokens >= input_tokens + output_tokens AND elapsed_millis >= 0),
		CHECK(length(CAST(report_json AS BLOB)) <= 131072),
		CHECK((report_digest = '') OR (length(report_digest) = 64
			AND report_digest = lower(report_digest)
			AND report_digest NOT GLOB '*[^0-9a-f]*')),
		CHECK(finding_count BETWEEN 0 AND 32),
		CHECK(length(error_code) <= 256 AND instr(error_code, char(0)) = 0
			AND length(CAST(error_reason AS BLOB)) <= 8192
			AND instr(error_reason, char(0)) = 0),
		CHECK(version > 0 AND julianday(updated_at) >= julianday(created_at)),
		CHECK((status = 'pending' AND current_attempt = 0 AND provider = '' AND model = ''
			AND input_tokens = 0 AND output_tokens = 0 AND total_tokens = 0
			AND elapsed_millis = 0 AND report_json = '' AND report_digest = ''
			AND finding_count = 0 AND error_code = '' AND error_reason = ''
			AND started_at IS NULL AND finished_at IS NULL) OR
			(status = 'running' AND attempt_count > 0 AND current_attempt = attempt_count
				AND provider = '' AND model = '' AND input_tokens = 0 AND output_tokens = 0
				AND total_tokens = 0 AND elapsed_millis = 0 AND report_json = ''
				AND report_digest = '' AND finding_count = 0 AND error_code = ''
				AND error_reason = '' AND started_at IS NOT NULL AND finished_at IS NULL) OR
			(status = 'completed' AND attempt_count > 0 AND current_attempt = attempt_count
				AND length(provider) > 0 AND length(model) > 0 AND json_valid(report_json)
				AND length(report_digest) = 64 AND finding_count >= 0
				AND error_code = '' AND error_reason = '' AND started_at IS NOT NULL
				AND finished_at IS NOT NULL AND updated_at = finished_at) OR
			(status = 'failed' AND attempt_count > 0 AND current_attempt = attempt_count
				AND length(provider) > 0 AND length(model) > 0 AND report_json = ''
				AND report_digest = '' AND finding_count = 0 AND length(error_code) > 0
				AND length(error_reason) > 0 AND started_at IS NOT NULL
				AND finished_at IS NOT NULL AND updated_at = finished_at) OR
			(status = 'cancelled' AND report_json = '' AND report_digest = ''
				AND finding_count = 0 AND length(error_code) > 0 AND length(error_reason) > 0
				AND finished_at IS NOT NULL AND updated_at = finished_at AND
				((attempt_count > 0 AND current_attempt = attempt_count
					AND length(provider) > 0 AND length(model) > 0 AND started_at IS NOT NULL) OR
				 (current_attempt = 0 AND provider = '' AND model = '' AND input_tokens = 0
					AND output_tokens = 0 AND total_tokens = 0 AND elapsed_millis = 0
					AND started_at IS NULL))))
	);`,
	`CREATE INDEX idx_readonly_fanout_execution_shards_status
		ON readonly_fanout_execution_shards(execution_id, status, ordinal);`,
	`CREATE TABLE readonly_fanout_model_calls (
		execution_id TEXT NOT NULL,
		plan_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		shard_ordinal INTEGER NOT NULL,
		attempt_number INTEGER NOT NULL,
		lease_id TEXT NOT NULL,
		lease_generation INTEGER NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		status TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		input_fingerprint TEXT NOT NULL,
		response_digest TEXT NOT NULL DEFAULT '',
		reserved_input_tokens INTEGER NOT NULL,
		reserved_output_tokens INTEGER NOT NULL,
		reserved_total_tokens INTEGER NOT NULL,
		reserved_millis INTEGER NOT NULL,
		usage_recorded INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		elapsed_recorded INTEGER NOT NULL DEFAULT 0,
		elapsed_millis INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',
		error_reason TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL,
		started_at TEXT NOT NULL,
		finished_at TEXT,
		PRIMARY KEY(execution_id, shard_ordinal, attempt_number),
		FOREIGN KEY(execution_id, shard_ordinal)
			REFERENCES readonly_fanout_execution_shards(execution_id, ordinal) ON DELETE RESTRICT,
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(shard_ordinal BETWEEN 1 AND 6 AND attempt_number > 0),
		CHECK(lease_id = trim(lease_id) AND length(lease_id) BETWEEN 1 AND 256
			AND instr(lease_id, char(0)) = 0 AND lease_generation > 0),
		CHECK(provider = trim(provider) AND model = trim(model)
			AND length(provider) BETWEEN 1 AND 256 AND length(model) BETWEEN 1 AND 256
			AND instr(provider, char(0)) = 0 AND instr(model, char(0)) = 0),
		CHECK(status IN ('started', 'completed', 'failed', 'cancelled', 'abandoned')),
		CHECK(length(input_fingerprint) = 64 AND input_fingerprint = lower(input_fingerprint)
			AND input_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK((response_digest = '') OR (length(response_digest) = 64
			AND response_digest = lower(response_digest)
			AND response_digest NOT GLOB '*[^0-9a-f]*')),
		CHECK(reserved_input_tokens >= 0 AND reserved_output_tokens BETWEEN 128 AND 4096
			AND reserved_total_tokens = reserved_input_tokens + reserved_output_tokens
			AND reserved_millis >= 0),
		CHECK(usage_recorded IN (0, 1) AND input_tokens >= 0 AND output_tokens >= 0
			AND total_tokens >= 0 AND total_tokens >= input_tokens + output_tokens),
		CHECK(elapsed_recorded IN (0, 1) AND elapsed_millis >= 0),
		CHECK(length(error_code) <= 256 AND instr(error_code, char(0)) = 0
			AND length(CAST(error_reason AS BLOB)) <= 8192
			AND instr(error_reason, char(0)) = 0),
		CHECK(version > 0),
		CHECK((status = 'started' AND outcome = '' AND response_digest = ''
			AND usage_recorded = 0 AND input_tokens = 0 AND output_tokens = 0
			AND total_tokens = 0 AND elapsed_recorded = 0 AND elapsed_millis = 0
			AND error_code = '' AND error_reason = '' AND finished_at IS NULL) OR
			(status = 'completed' AND outcome = 'success' AND length(response_digest) = 64
				AND usage_recorded = 1 AND elapsed_recorded = 1 AND error_code = ''
				AND error_reason = '' AND finished_at IS NOT NULL) OR
			(status IN ('failed', 'cancelled', 'abandoned') AND length(outcome) > 0
				AND response_digest = '' AND length(error_code) > 0
				AND length(error_reason) > 0 AND finished_at IS NOT NULL))
	);`,
	`CREATE INDEX idx_readonly_fanout_model_calls_run_status
		ON readonly_fanout_model_calls(run_id, status, execution_id, shard_ordinal);`,
	`CREATE TABLE readonly_fanout_findings (
		execution_id TEXT NOT NULL,
		shard_ordinal INTEGER NOT NULL,
		ordinal INTEGER NOT NULL,
		fingerprint TEXT NOT NULL,
		severity TEXT NOT NULL,
		category TEXT NOT NULL,
		title TEXT NOT NULL,
		detail TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		line_start INTEGER NOT NULL,
		line_end INTEGER NOT NULL,
		confidence INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(execution_id, shard_ordinal, ordinal),
		UNIQUE(execution_id, fingerprint),
		FOREIGN KEY(execution_id, shard_ordinal)
			REFERENCES readonly_fanout_execution_shards(execution_id, ordinal) ON DELETE RESTRICT,
		CHECK(shard_ordinal BETWEEN 1 AND 6 AND ordinal BETWEEN 1 AND 32),
		CHECK(length(fingerprint) = 64 AND fingerprint = lower(fingerprint)
			AND fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(severity IN ('info', 'low', 'medium', 'high', 'critical')),
		CHECK(category = trim(category) AND length(category) BETWEEN 1 AND 64),
		CHECK(title = trim(title) AND length(title) BETWEEN 1 AND 256),
		CHECK(detail = trim(detail) AND length(detail) BETWEEN 1 AND 2048),
		CHECK(relative_path = trim(relative_path) AND length(relative_path) BETWEEN 1 AND 2048
			AND instr(relative_path, char(0)) = 0 AND instr(relative_path, char(92)) = 0
			AND substr(relative_path, 1, 1) <> '/' AND relative_path NOT IN ('.', '..')
			AND relative_path NOT LIKE '../%'
			AND instr('/' || relative_path || '/', '/../') = 0),
		CHECK(line_start >= 0 AND line_end >= line_start AND confidence BETWEEN 0 AND 100)
	);`,
	`CREATE INDEX idx_readonly_fanout_findings_severity
		ON readonly_fanout_findings(execution_id, severity, shard_ordinal, ordinal);`,
	`CREATE TABLE readonly_fanout_execution_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		execution_id TEXT NOT NULL UNIQUE,
		plan_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(execution_id) REFERENCES readonly_fanout_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(plan_id) REFERENCES readonly_fanout_plans(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_readonly_fanout_execution_insert
		BEFORE INSERT ON readonly_fanout_executions
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_plans plan
			JOIN runs run ON run.id = plan.run_id
			JOIN run_execution_leases lease ON lease.run_id = run.id
			WHERE plan.id = NEW.plan_id AND plan.run_id = NEW.run_id
				AND plan.workspace_id = NEW.workspace_id AND plan.status = 'planned'
				AND plan.effective_parallelism = NEW.parallelism
				AND plan.snapshot_digest = NEW.snapshot_digest
				AND plan.requested_by = NEW.requested_by AND run.status = 'running'
				AND lease.lease_id = NEW.lease_id
				AND lease.generation = NEW.lease_generation AND lease.status = 'active'
				AND julianday(lease.expires_at) > julianday(NEW.started_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_shard_insert
		BEFORE INSERT ON readonly_fanout_execution_shards
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_executions execution
			JOIN readonly_fanout_shards plan_shard
				ON plan_shard.plan_id = execution.plan_id AND plan_shard.ordinal = NEW.ordinal
			WHERE execution.id = NEW.execution_id AND execution.plan_id = NEW.plan_id
				AND execution.status = 'running' AND NEW.ordinal <= execution.parallelism
				AND plan_shard.input_digest = NEW.input_digest
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution shard binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_operation_insert
		BEFORE INSERT ON readonly_fanout_execution_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_executions execution
			WHERE execution.id = NEW.execution_id AND execution.plan_id = NEW.plan_id
				AND execution.run_id = NEW.run_id AND execution.requested_by = NEW.requested_by
				AND execution.started_at = NEW.created_at
				AND (SELECT COUNT(*) FROM readonly_fanout_execution_shards shard
					WHERE shard.execution_id = execution.id) = execution.parallelism
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_transition
		BEFORE UPDATE ON readonly_fanout_executions
		WHEN OLD.status != 'running'
			OR NEW.id != OLD.id OR NEW.plan_id != OLD.plan_id OR NEW.run_id != OLD.run_id
			OR NEW.workspace_id != OLD.workspace_id OR NEW.parallelism != OLD.parallelism
			OR NEW.max_output_tokens_per_shard != OLD.max_output_tokens_per_shard
			OR NEW.snapshot_digest != OLD.snapshot_digest OR NEW.requested_by != OLD.requested_by
			OR NEW.started_at != OLD.started_at OR NEW.version != OLD.version + 1
			OR julianday(NEW.updated_at) < julianday(OLD.updated_at)
			OR (NEW.status = 'running' AND
				(NEW.finished_at IS NOT NULL OR NEW.stop_code != ''
				 OR NEW.lease_generation <= OLD.lease_generation
				 OR NEW.lease_id = OLD.lease_id))
			OR (NEW.status IN ('completed', 'failed', 'cancelled') AND
				(NEW.lease_id != OLD.lease_id OR NEW.lease_generation != OLD.lease_generation
				 OR NEW.finished_at IS NULL OR NEW.updated_at != NEW.finished_at))
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution transition is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_shard_transition
		BEFORE UPDATE ON readonly_fanout_execution_shards
		WHEN OLD.status IN ('completed', 'failed', 'cancelled')
			OR NEW.execution_id != OLD.execution_id OR NEW.plan_id != OLD.plan_id
			OR NEW.ordinal != OLD.ordinal OR NEW.input_digest != OLD.input_digest
			OR NEW.created_at != OLD.created_at OR NEW.version != OLD.version + 1
			OR julianday(NEW.updated_at) < julianday(OLD.updated_at)
			OR (OLD.status = 'pending' AND NEW.status NOT IN ('running', 'cancelled'))
			OR (OLD.status = 'running' AND NEW.status NOT IN
				('pending', 'completed', 'failed', 'cancelled'))
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution shard transition is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_model_call_insert
		BEFORE INSERT ON readonly_fanout_model_calls
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_executions execution
			JOIN readonly_fanout_execution_shards shard
				ON shard.execution_id = execution.id AND shard.ordinal = NEW.shard_ordinal
			JOIN run_execution_leases lease ON lease.run_id = execution.run_id
			WHERE execution.id = NEW.execution_id AND execution.plan_id = NEW.plan_id
				AND execution.run_id = NEW.run_id AND execution.status = 'running'
				AND execution.lease_id = NEW.lease_id
				AND execution.lease_generation = NEW.lease_generation
				AND shard.status = 'running' AND shard.current_attempt = NEW.attempt_number
				AND lease.lease_id = NEW.lease_id
				AND lease.generation = NEW.lease_generation AND lease.status = 'active'
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out model call binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_model_call_transition
		BEFORE UPDATE ON readonly_fanout_model_calls
		WHEN OLD.status != 'started' OR NEW.status = 'started'
			OR NEW.execution_id != OLD.execution_id OR NEW.plan_id != OLD.plan_id
			OR NEW.run_id != OLD.run_id OR NEW.shard_ordinal != OLD.shard_ordinal
			OR NEW.attempt_number != OLD.attempt_number OR NEW.lease_id != OLD.lease_id
			OR NEW.lease_generation != OLD.lease_generation OR NEW.provider != OLD.provider
			OR NEW.model != OLD.model OR NEW.input_fingerprint != OLD.input_fingerprint
			OR NEW.reserved_input_tokens != OLD.reserved_input_tokens
			OR NEW.reserved_output_tokens != OLD.reserved_output_tokens
			OR NEW.reserved_total_tokens != OLD.reserved_total_tokens
			OR NEW.reserved_millis != OLD.reserved_millis OR NEW.started_at != OLD.started_at
			OR NEW.version != OLD.version + 1
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out model call transition is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_finding_insert
		BEFORE INSERT ON readonly_fanout_findings
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_execution_shards shard
			JOIN readonly_fanout_files file ON file.plan_id = shard.plan_id
				AND file.shard_ordinal = shard.ordinal
			WHERE shard.execution_id = NEW.execution_id
				AND shard.ordinal = NEW.shard_ordinal AND shard.status = 'completed'
				AND file.relative_path = NEW.relative_path
		)
		BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out finding binding is invalid');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_delete_immutable
		BEFORE DELETE ON readonly_fanout_executions BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_shard_delete_immutable
		BEFORE DELETE ON readonly_fanout_execution_shards BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution shard cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_model_call_delete_immutable
		BEFORE DELETE ON readonly_fanout_model_calls BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out model call cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_finding_immutable
		BEFORE UPDATE ON readonly_fanout_findings BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out finding is immutable');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_finding_delete_immutable
		BEFORE DELETE ON readonly_fanout_findings BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out finding cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_operation_immutable
		BEFORE UPDATE ON readonly_fanout_execution_operations BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution operation is immutable');
		END;`,
	`CREATE TRIGGER trg_readonly_fanout_execution_operation_delete_immutable
		BEFORE DELETE ON readonly_fanout_execution_operations BEGIN
			SELECT RAISE(ABORT, 'read-only fan-out execution operation cannot be deleted');
		END;`,
}

var findingReportStatements = []string{
	`CREATE TABLE finding_reports (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		source_kind TEXT NOT NULL,
		source_id TEXT NOT NULL UNIQUE,
		protocol_version TEXT NOT NULL,
		status TEXT NOT NULL,
		title TEXT NOT NULL,
		projection_digest TEXT NOT NULL DEFAULT '',
		finding_count INTEGER NOT NULL DEFAULT 0,
		evidence_count INTEGER NOT NULL DEFAULT 0,
		info_count INTEGER NOT NULL DEFAULT 0,
		low_count INTEGER NOT NULL DEFAULT 0,
		medium_count INTEGER NOT NULL DEFAULT 0,
		high_count INTEGER NOT NULL DEFAULT 0,
		critical_count INTEGER NOT NULL DEFAULT 0,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(source_id) REFERENCES readonly_fanout_executions(id) ON DELETE RESTRICT,
		CHECK(source_kind = 'readonly_fanout_execution'),
		CHECK(protocol_version = 'finding_report.v1'),
		CHECK(status IN ('building', 'generated')),
		CHECK(title = trim(title) AND length(title) BETWEEN 1 AND 256
			AND length(CAST(title AS BLOB)) <= 1024 AND instr(title, char(0)) = 0),
		CHECK((projection_digest = '') OR (length(projection_digest) = 64
			AND projection_digest = lower(projection_digest)
			AND projection_digest NOT GLOB '*[^0-9a-f]*')),
		CHECK(finding_count BETWEEN 0 AND 192 AND evidence_count BETWEEN 0 AND 192),
		CHECK(info_count >= 0 AND low_count >= 0 AND medium_count >= 0
			AND high_count >= 0 AND critical_count >= 0
			AND info_count + low_count + medium_count + high_count + critical_count
				= finding_count),
		CHECK((status = 'building' AND projection_digest = '' AND finding_count = 0
			AND evidence_count = 0 AND version = 1) OR
			(status = 'generated' AND length(projection_digest) = 64 AND version = 2))
	);`,
	`CREATE INDEX idx_finding_reports_run_created
		ON finding_reports(run_id, created_at, id);`,
	`CREATE TABLE findings (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		fingerprint TEXT NOT NULL,
		status TEXT NOT NULL,
		severity TEXT NOT NULL,
		category TEXT NOT NULL,
		title TEXT NOT NULL,
		detail TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		line_start INTEGER NOT NULL,
		line_end INTEGER NOT NULL,
		confidence INTEGER NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		UNIQUE(report_id, ordinal),
		UNIQUE(report_id, fingerprint),
		CHECK(ordinal BETWEEN 1 AND 192),
		CHECK(length(fingerprint) = 64 AND fingerprint = lower(fingerprint)
			AND fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(status = 'draft'),
		CHECK(severity IN ('info', 'low', 'medium', 'high', 'critical')),
		CHECK(category = trim(category) AND length(category) BETWEEN 1 AND 64
			AND length(CAST(category AS BLOB)) <= 256 AND instr(category, char(0)) = 0),
		CHECK(title = trim(title) AND length(title) BETWEEN 1 AND 256
			AND length(CAST(title AS BLOB)) <= 1024 AND instr(title, char(0)) = 0),
		CHECK(detail = trim(detail) AND length(detail) BETWEEN 1 AND 2048
			AND length(CAST(detail AS BLOB)) <= 8192 AND instr(detail, char(0)) = 0),
		CHECK(relative_path = trim(relative_path)
			AND length(relative_path) BETWEEN 1 AND 2048
			AND instr(relative_path, char(0)) = 0 AND instr(relative_path, char(92)) = 0
			AND substr(relative_path, 1, 1) <> '/' AND relative_path NOT IN ('.', '..')
			AND relative_path NOT LIKE '../%'
			AND instr('/' || relative_path || '/', '/../') = 0),
		CHECK(line_start >= 0 AND line_end >= line_start AND confidence BETWEEN 0 AND 100),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_findings_report_severity
		ON findings(report_id, severity, ordinal);`,
	`CREATE TABLE finding_evidence (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		finding_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		kind TEXT NOT NULL,
		source_kind TEXT NOT NULL,
		source_id TEXT NOT NULL,
		source_shard INTEGER NOT NULL,
		source_ordinal INTEGER NOT NULL,
		source_fingerprint TEXT NOT NULL,
		source_digest TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		line_start INTEGER NOT NULL,
		line_end INTEGER NOT NULL,
		confidence INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(source_id, source_shard, source_ordinal)
			REFERENCES readonly_fanout_findings(execution_id, shard_ordinal, ordinal)
			ON DELETE RESTRICT,
		UNIQUE(finding_id, ordinal),
		UNIQUE(source_kind, source_id, source_shard, source_ordinal),
		CHECK(ordinal BETWEEN 1 AND 192),
		CHECK(kind = 'model_assertion'),
		CHECK(source_kind = 'readonly_fanout_finding'),
		CHECK(source_shard BETWEEN 1 AND 6 AND source_ordinal BETWEEN 1 AND 32),
		CHECK(length(source_fingerprint) = 64
			AND source_fingerprint = lower(source_fingerprint)
			AND source_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(source_digest) = 64 AND source_digest = lower(source_digest)
			AND source_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(relative_path = trim(relative_path)
			AND length(relative_path) BETWEEN 1 AND 2048
			AND instr(relative_path, char(0)) = 0 AND instr(relative_path, char(92)) = 0
			AND substr(relative_path, 1, 1) <> '/' AND relative_path NOT IN ('.', '..')
			AND relative_path NOT LIKE '../%'
			AND instr('/' || relative_path || '/', '/../') = 0),
		CHECK(line_start >= 0 AND line_end >= line_start AND confidence BETWEEN 0 AND 100)
	);`,
	`CREATE INDEX idx_finding_evidence_report_finding
		ON finding_evidence(report_id, finding_id, ordinal);`,
	`CREATE TRIGGER trg_finding_report_insert
		BEFORE INSERT ON finding_reports
		WHEN NOT EXISTS (
			SELECT 1 FROM readonly_fanout_executions execution
			WHERE execution.id = NEW.source_id AND execution.run_id = NEW.run_id
				AND execution.status = 'completed' AND execution.finished_at = NEW.created_at
				AND NEW.status = 'building' AND NEW.version = 1
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding report source binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_insert
		BEFORE INSERT ON findings
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_reports report
			JOIN readonly_fanout_findings source ON source.execution_id = report.source_id
			WHERE report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'building' AND source.severity = NEW.severity
				AND source.category = NEW.category AND source.title = NEW.title
				AND source.detail = NEW.detail AND source.relative_path = NEW.relative_path
				AND source.line_start = NEW.line_start AND source.line_end = NEW.line_end
				AND NEW.confidence = (
					SELECT MIN(candidate.confidence)
					FROM readonly_fanout_findings candidate
					WHERE candidate.execution_id = report.source_id
						AND candidate.severity = NEW.severity
						AND candidate.category = NEW.category
						AND candidate.title = NEW.title
						AND candidate.detail = NEW.detail
						AND candidate.relative_path = NEW.relative_path
						AND candidate.line_start = NEW.line_start
						AND candidate.line_end = NEW.line_end
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding source projection is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_evidence_insert
		BEFORE INSERT ON finding_evidence
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_reports report
			JOIN findings finding ON finding.report_id = report.id
			JOIN readonly_fanout_findings source
				ON source.execution_id = report.source_id
				AND source.shard_ordinal = NEW.source_shard
				AND source.ordinal = NEW.source_ordinal
			JOIN readonly_fanout_execution_shards shard
				ON shard.execution_id = source.execution_id
				AND shard.ordinal = source.shard_ordinal
			WHERE report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'building' AND finding.id = NEW.finding_id
				AND finding.run_id = NEW.run_id AND NEW.source_id = report.source_id
				AND NEW.source_fingerprint = source.fingerprint
				AND NEW.source_digest = shard.report_digest
				AND NEW.relative_path = source.relative_path
				AND NEW.line_start = source.line_start AND NEW.line_end = source.line_end
				AND NEW.confidence = source.confidence
				AND finding.severity = source.severity
				AND finding.category = source.category AND finding.title = source.title
				AND finding.detail = source.detail
				AND finding.relative_path = source.relative_path
				AND finding.line_start = source.line_start
				AND finding.line_end = source.line_end
				AND finding.confidence <= source.confidence
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding evidence source binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_report_generate
		BEFORE UPDATE ON finding_reports
		WHEN OLD.status != 'building' OR NEW.status != 'generated'
			OR NEW.id != OLD.id OR NEW.run_id != OLD.run_id
			OR NEW.source_kind != OLD.source_kind OR NEW.source_id != OLD.source_id
			OR NEW.protocol_version != OLD.protocol_version OR NEW.title != OLD.title
			OR NEW.created_at != OLD.created_at OR NEW.version != 2
			OR NEW.evidence_count != (SELECT COUNT(*)
				FROM readonly_fanout_findings source
				WHERE source.execution_id = OLD.source_id)
			OR NEW.finding_count != (SELECT COUNT(*) FROM (
				SELECT 1 FROM readonly_fanout_findings source
				WHERE source.execution_id = OLD.source_id
				GROUP BY source.severity, source.category, source.title, source.detail,
					source.relative_path, source.line_start, source.line_end
			))
			OR NEW.finding_count != (SELECT COUNT(*) FROM findings
				WHERE report_id = OLD.id)
			OR NEW.evidence_count != (SELECT COUNT(*) FROM finding_evidence
				WHERE report_id = OLD.id)
			OR NEW.info_count != (SELECT COUNT(*) FROM findings
				WHERE report_id = OLD.id AND severity = 'info')
			OR NEW.low_count != (SELECT COUNT(*) FROM findings
				WHERE report_id = OLD.id AND severity = 'low')
			OR NEW.medium_count != (SELECT COUNT(*) FROM findings
				WHERE report_id = OLD.id AND severity = 'medium')
			OR NEW.high_count != (SELECT COUNT(*) FROM findings
				WHERE report_id = OLD.id AND severity = 'high')
			OR NEW.critical_count != (SELECT COUNT(*) FROM findings
				WHERE report_id = OLD.id AND severity = 'critical')
			OR EXISTS (SELECT 1 FROM findings finding
				WHERE finding.report_id = OLD.id AND NOT EXISTS (
					SELECT 1 FROM finding_evidence evidence
					WHERE evidence.finding_id = finding.id))
			OR EXISTS (SELECT 1 FROM findings finding
				WHERE finding.report_id = OLD.id AND (
					(SELECT MIN(evidence.ordinal) FROM finding_evidence evidence
						WHERE evidence.finding_id = finding.id) != 1
					OR (SELECT MAX(evidence.ordinal) FROM finding_evidence evidence
						WHERE evidence.finding_id = finding.id) !=
						(SELECT COUNT(*) FROM finding_evidence evidence
							WHERE evidence.finding_id = finding.id)))
			OR (NEW.finding_count > 0 AND (SELECT MIN(ordinal) FROM findings
				WHERE report_id = OLD.id) != 1)
			OR (NEW.finding_count > 0 AND (SELECT MAX(ordinal) FROM findings
				WHERE report_id = OLD.id) != NEW.finding_count)
		BEGIN
			SELECT RAISE(ABORT, 'finding report generation is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_report_delete_immutable
		BEFORE DELETE ON finding_reports BEGIN
			SELECT RAISE(ABORT, 'finding report cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_update_immutable
		BEFORE UPDATE ON findings BEGIN
			SELECT RAISE(ABORT, 'finding cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_delete_immutable
		BEFORE DELETE ON findings BEGIN
			SELECT RAISE(ABORT, 'finding cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_evidence_update_immutable
		BEFORE UPDATE ON finding_evidence BEGIN
			SELECT RAISE(ABORT, 'finding evidence cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_evidence_delete_immutable
		BEFORE DELETE ON finding_evidence BEGIN
			SELECT RAISE(ABORT, 'finding evidence cannot be deleted');
		END;`,
}

var findingValidationStatements = []string{
	`CREATE TABLE finding_artifact_evidence (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		finding_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		artifact_sha256 TEXT NOT NULL,
		artifact_size_bytes INTEGER NOT NULL,
		artifact_mime TEXT NOT NULL,
		artifact_stream TEXT NOT NULL,
		artifact_tool TEXT NOT NULL,
		artifact_source_id TEXT NOT NULL,
		artifact_redacted INTEGER NOT NULL,
		attached_by TEXT NOT NULL,
		note TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(artifact_id) REFERENCES run_artifacts(id) ON DELETE RESTRICT,
		UNIQUE(finding_id, ordinal),
		UNIQUE(finding_id, artifact_id),
		CHECK(ordinal BETWEEN 1 AND 64),
		CHECK(length(artifact_sha256) = 64
			AND artifact_sha256 = lower(artifact_sha256)
			AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(artifact_size_bytes BETWEEN 1 AND 4194304),
		CHECK(artifact_mime = trim(artifact_mime)
			AND length(artifact_mime) BETWEEN 1 AND 256
			AND instr(artifact_mime, char(0)) = 0),
		CHECK(artifact_stream IN ('stdout', 'stderr')),
		CHECK(artifact_redacted IN (0, 1)),
		CHECK(artifact_tool = trim(artifact_tool)
			AND length(artifact_tool) BETWEEN 1 AND 256
			AND instr(artifact_tool, char(0)) = 0),
		CHECK(artifact_source_id = trim(artifact_source_id)
			AND length(artifact_source_id) BETWEEN 1 AND 256
			AND instr(artifact_source_id, char(0)) = 0),
		CHECK(attached_by = trim(attached_by) AND length(attached_by) BETWEEN 1 AND 256
			AND instr(attached_by, char(0)) = 0),
		CHECK(note = trim(note) AND length(note) BETWEEN 1 AND 2048
			AND length(CAST(note AS BLOB)) <= 8192 AND instr(note, char(0)) = 0)
	);`,
	`CREATE INDEX idx_finding_artifact_evidence_report_finding
		ON finding_artifact_evidence(report_id, finding_id, ordinal);`,
	`CREATE TABLE finding_artifact_evidence_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		evidence_id TEXT NOT NULL UNIQUE,
		finding_id TEXT NOT NULL,
		artifact_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		attached_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(evidence_id) REFERENCES finding_artifact_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(artifact_id) REFERENCES run_artifacts(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(attached_by = trim(attached_by) AND length(attached_by) BETWEEN 1 AND 256
			AND instr(attached_by, char(0)) = 0)
	);`,
	`CREATE TABLE finding_validation_decisions (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		finding_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		from_status TEXT NOT NULL,
		status TEXT NOT NULL,
		decided_by TEXT NOT NULL,
		reason TEXT NOT NULL,
		artifact_evidence_count INTEGER NOT NULL,
		artifact_evidence_digest TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(from_status = 'draft'),
		CHECK(status IN ('validated', 'rejected')),
		CHECK(decided_by = trim(decided_by) AND length(decided_by) BETWEEN 1 AND 256
			AND instr(decided_by, char(0)) = 0),
		CHECK(reason = trim(reason) AND length(reason) BETWEEN 1 AND 2048
			AND length(CAST(reason AS BLOB)) <= 8192 AND instr(reason, char(0)) = 0),
		CHECK(artifact_evidence_count BETWEEN 0 AND 64),
		CHECK(length(artifact_evidence_digest) = 64
			AND artifact_evidence_digest = lower(artifact_evidence_digest)
			AND artifact_evidence_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(status != 'validated' OR artifact_evidence_count > 0),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_finding_validation_run_created
		ON finding_validation_decisions(run_id, created_at, id);`,
	`CREATE TABLE finding_validation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		validation_id TEXT NOT NULL UNIQUE,
		finding_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		status TEXT NOT NULL,
		decided_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(validation_id) REFERENCES finding_validation_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(status IN ('validated', 'rejected')),
		CHECK(decided_by = trim(decided_by) AND length(decided_by) BETWEEN 1 AND 256
			AND instr(decided_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_finding_artifact_evidence_insert
		BEFORE INSERT ON finding_artifact_evidence
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_reports report
			JOIN findings finding ON finding.report_id = report.id
			JOIN run_artifacts artifact ON artifact.id = NEW.artifact_id
			WHERE report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'generated' AND finding.id = NEW.finding_id
				AND finding.run_id = NEW.run_id AND finding.status = 'draft'
				AND artifact.run_id = NEW.run_id
				AND artifact.sha256 = NEW.artifact_sha256
				AND artifact.size_bytes = NEW.artifact_size_bytes
				AND artifact.mime = NEW.artifact_mime
				AND artifact.stream = NEW.artifact_stream
				AND artifact.tool_name = NEW.artifact_tool
				AND artifact.source_id = NEW.artifact_source_id
				AND artifact.redacted = NEW.artifact_redacted
				AND artifact.created_at <= NEW.created_at
				AND NEW.ordinal = 1 + (SELECT COUNT(*)
					FROM finding_artifact_evidence existing
					WHERE existing.finding_id = NEW.finding_id)
				AND NOT EXISTS (SELECT 1 FROM finding_validation_decisions decision
					WHERE decision.finding_id = NEW.finding_id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding Artifact Evidence binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_artifact_evidence_operation_insert
		BEFORE INSERT ON finding_artifact_evidence_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_artifact_evidence evidence
			WHERE evidence.id = NEW.evidence_id
				AND evidence.finding_id = NEW.finding_id
				AND evidence.artifact_id = NEW.artifact_id
				AND evidence.run_id = NEW.run_id
				AND evidence.attached_by = NEW.attached_by
				AND evidence.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding Artifact Evidence operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_validation_insert
		BEFORE INSERT ON finding_validation_decisions
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_reports report
			JOIN findings finding ON finding.report_id = report.id
			WHERE report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'generated' AND finding.id = NEW.finding_id
				AND finding.run_id = NEW.run_id AND finding.status = NEW.from_status
				AND NEW.artifact_evidence_count = (SELECT COUNT(*)
					FROM finding_artifact_evidence evidence
					WHERE evidence.finding_id = NEW.finding_id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding validation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_validation_operation_insert
		BEFORE INSERT ON finding_validation_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_validation_decisions decision
			WHERE decision.id = NEW.validation_id
				AND decision.finding_id = NEW.finding_id
				AND decision.run_id = NEW.run_id
				AND decision.status = NEW.status
				AND decision.decided_by = NEW.decided_by
				AND decision.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding validation operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_run_artifact_update_immutable
		BEFORE UPDATE ON run_artifacts BEGIN
			SELECT RAISE(ABORT, 'run Artifact cannot be updated');
		END;`,
	`CREATE TRIGGER trg_run_artifact_delete_immutable
		BEFORE DELETE ON run_artifacts BEGIN
			SELECT RAISE(ABORT, 'run Artifact cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_artifact_evidence_update_immutable
		BEFORE UPDATE ON finding_artifact_evidence BEGIN
			SELECT RAISE(ABORT, 'finding Artifact Evidence cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_artifact_evidence_delete_immutable
		BEFORE DELETE ON finding_artifact_evidence BEGIN
			SELECT RAISE(ABORT, 'finding Artifact Evidence cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_artifact_evidence_operation_update_immutable
		BEFORE UPDATE ON finding_artifact_evidence_operations BEGIN
			SELECT RAISE(ABORT, 'finding Artifact Evidence operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_artifact_evidence_operation_delete_immutable
		BEFORE DELETE ON finding_artifact_evidence_operations BEGIN
			SELECT RAISE(ABORT, 'finding Artifact Evidence operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_validation_update_immutable
		BEFORE UPDATE ON finding_validation_decisions BEGIN
			SELECT RAISE(ABORT, 'finding validation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_validation_delete_immutable
		BEFORE DELETE ON finding_validation_decisions BEGIN
			SELECT RAISE(ABORT, 'finding validation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_validation_operation_update_immutable
		BEFORE UPDATE ON finding_validation_operations BEGIN
			SELECT RAISE(ABORT, 'finding validation operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_validation_operation_delete_immutable
		BEFORE DELETE ON finding_validation_operations BEGIN
			SELECT RAISE(ABORT, 'finding validation operation cannot be deleted');
		END;`,
}

var findingRemediationStatements = []string{
	`CREATE TABLE finding_acceptance_decisions (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		finding_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		validation_id TEXT NOT NULL UNIQUE,
		from_status TEXT NOT NULL,
		status TEXT NOT NULL,
		validation_artifact_evidence_count INTEGER NOT NULL,
		validation_artifact_evidence_digest TEXT NOT NULL,
		decided_by TEXT NOT NULL,
		reason TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(validation_id) REFERENCES finding_validation_decisions(id) ON DELETE RESTRICT,
		CHECK(from_status = 'validated'),
		CHECK(status = 'accepted'),
		CHECK(validation_artifact_evidence_count BETWEEN 1 AND 64),
		CHECK(length(validation_artifact_evidence_digest) = 64
			AND validation_artifact_evidence_digest = lower(validation_artifact_evidence_digest)
			AND validation_artifact_evidence_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(decided_by = trim(decided_by) AND length(decided_by) BETWEEN 1 AND 256
			AND instr(decided_by, char(0)) = 0),
		CHECK(reason = trim(reason) AND length(reason) BETWEEN 1 AND 2048
			AND length(CAST(reason AS BLOB)) <= 8192 AND instr(reason, char(0)) = 0),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_finding_acceptance_run_created
		ON finding_acceptance_decisions(run_id, created_at, id);`,
	`CREATE TABLE finding_acceptance_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		acceptance_id TEXT NOT NULL UNIQUE,
		validation_id TEXT NOT NULL,
		finding_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		decided_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(acceptance_id) REFERENCES finding_acceptance_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(validation_id) REFERENCES finding_validation_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(decided_by = trim(decided_by) AND length(decided_by) BETWEEN 1 AND 256
			AND instr(decided_by, char(0)) = 0)
	);`,
	`CREATE TABLE finding_remediation_evidence (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		finding_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		acceptance_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		artifact_sha256 TEXT NOT NULL,
		artifact_size_bytes INTEGER NOT NULL,
		artifact_mime TEXT NOT NULL,
		artifact_stream TEXT NOT NULL,
		artifact_tool TEXT NOT NULL,
		artifact_source_id TEXT NOT NULL,
		artifact_redacted INTEGER NOT NULL,
		attached_by TEXT NOT NULL,
		note TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(acceptance_id) REFERENCES finding_acceptance_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(artifact_id) REFERENCES run_artifacts(id) ON DELETE RESTRICT,
		UNIQUE(finding_id, ordinal),
		UNIQUE(finding_id, artifact_id),
		CHECK(ordinal BETWEEN 1 AND 64),
		CHECK(length(artifact_sha256) = 64
			AND artifact_sha256 = lower(artifact_sha256)
			AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(artifact_size_bytes BETWEEN 1 AND 4194304),
		CHECK(artifact_mime = trim(artifact_mime)
			AND length(artifact_mime) BETWEEN 1 AND 256
			AND instr(artifact_mime, char(0)) = 0),
		CHECK(artifact_stream IN ('stdout', 'stderr')),
		CHECK(artifact_redacted IN (0, 1)),
		CHECK(artifact_tool = trim(artifact_tool)
			AND length(artifact_tool) BETWEEN 1 AND 256
			AND instr(artifact_tool, char(0)) = 0),
		CHECK(artifact_source_id = trim(artifact_source_id)
			AND length(artifact_source_id) BETWEEN 1 AND 256
			AND instr(artifact_source_id, char(0)) = 0),
		CHECK(attached_by = trim(attached_by) AND length(attached_by) BETWEEN 1 AND 256
			AND instr(attached_by, char(0)) = 0),
		CHECK(note = trim(note) AND length(note) BETWEEN 1 AND 2048
			AND length(CAST(note AS BLOB)) <= 8192 AND instr(note, char(0)) = 0)
	);`,
	`CREATE INDEX idx_finding_remediation_evidence_report_finding
		ON finding_remediation_evidence(report_id, finding_id, ordinal);`,
	`CREATE TABLE finding_remediation_evidence_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		evidence_id TEXT NOT NULL UNIQUE,
		acceptance_id TEXT NOT NULL,
		finding_id TEXT NOT NULL,
		artifact_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		attached_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(evidence_id) REFERENCES finding_remediation_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(acceptance_id) REFERENCES finding_acceptance_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(artifact_id) REFERENCES run_artifacts(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(attached_by = trim(attached_by) AND length(attached_by) BETWEEN 1 AND 256
			AND instr(attached_by, char(0)) = 0)
	);`,
	`CREATE TABLE finding_fix_decisions (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		finding_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		acceptance_id TEXT NOT NULL UNIQUE,
		from_status TEXT NOT NULL,
		status TEXT NOT NULL,
		remediation_evidence_count INTEGER NOT NULL,
		remediation_evidence_digest TEXT NOT NULL,
		decided_by TEXT NOT NULL,
		reason TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(report_id) REFERENCES finding_reports(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(acceptance_id) REFERENCES finding_acceptance_decisions(id) ON DELETE RESTRICT,
		CHECK(from_status = 'accepted'),
		CHECK(status = 'fixed'),
		CHECK(remediation_evidence_count BETWEEN 1 AND 64),
		CHECK(length(remediation_evidence_digest) = 64
			AND remediation_evidence_digest = lower(remediation_evidence_digest)
			AND remediation_evidence_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(decided_by = trim(decided_by) AND length(decided_by) BETWEEN 1 AND 256
			AND instr(decided_by, char(0)) = 0),
		CHECK(reason = trim(reason) AND length(reason) BETWEEN 1 AND 2048
			AND length(CAST(reason AS BLOB)) <= 8192 AND instr(reason, char(0)) = 0),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_finding_fix_run_created
		ON finding_fix_decisions(run_id, created_at, id);`,
	`CREATE TABLE finding_fix_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		fix_id TEXT NOT NULL UNIQUE,
		acceptance_id TEXT NOT NULL,
		finding_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		decided_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(fix_id) REFERENCES finding_fix_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(acceptance_id) REFERENCES finding_acceptance_decisions(id) ON DELETE RESTRICT,
		FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(decided_by = trim(decided_by) AND length(decided_by) BETWEEN 1 AND 256
			AND instr(decided_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_finding_acceptance_insert
		BEFORE INSERT ON finding_acceptance_decisions
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_reports report
			JOIN findings finding ON finding.report_id = report.id
			JOIN finding_validation_decisions validation
				ON validation.finding_id = finding.id
			WHERE report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'generated' AND finding.id = NEW.finding_id
				AND finding.run_id = NEW.run_id AND validation.id = NEW.validation_id
				AND validation.status = NEW.from_status
				AND julianday(NEW.created_at) >= julianday(validation.created_at)
				AND NEW.validation_artifact_evidence_count =
					validation.artifact_evidence_count
				AND NEW.validation_artifact_evidence_digest =
					validation.artifact_evidence_digest
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding acceptance binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_acceptance_operation_insert
		BEFORE INSERT ON finding_acceptance_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_acceptance_decisions acceptance
			WHERE acceptance.id = NEW.acceptance_id
				AND acceptance.validation_id = NEW.validation_id
				AND acceptance.finding_id = NEW.finding_id
				AND acceptance.run_id = NEW.run_id
				AND acceptance.decided_by = NEW.decided_by
				AND acceptance.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding acceptance operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_remediation_evidence_insert
		BEFORE INSERT ON finding_remediation_evidence
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_acceptance_decisions acceptance
			JOIN finding_reports report ON report.id = acceptance.report_id
			JOIN findings finding ON finding.id = acceptance.finding_id
			JOIN run_artifacts artifact ON artifact.id = NEW.artifact_id
			WHERE acceptance.id = NEW.acceptance_id
				AND acceptance.finding_id = NEW.finding_id
				AND acceptance.run_id = NEW.run_id
				AND report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'generated' AND finding.run_id = NEW.run_id
				AND artifact.run_id = NEW.run_id
				AND artifact.sha256 = NEW.artifact_sha256
				AND artifact.size_bytes = NEW.artifact_size_bytes
				AND artifact.mime = NEW.artifact_mime
				AND artifact.stream = NEW.artifact_stream
				AND artifact.tool_name = NEW.artifact_tool
				AND artifact.source_id = NEW.artifact_source_id
				AND artifact.redacted = NEW.artifact_redacted
				AND julianday(NEW.created_at) >= julianday(acceptance.created_at)
				AND julianday(NEW.created_at) >= julianday(artifact.created_at)
				AND NEW.ordinal = 1 + (SELECT COUNT(*)
					FROM finding_remediation_evidence existing
					WHERE existing.finding_id = NEW.finding_id)
				AND NOT EXISTS (SELECT 1 FROM finding_artifact_evidence validation_evidence
					WHERE validation_evidence.finding_id = NEW.finding_id
						AND validation_evidence.artifact_id = NEW.artifact_id)
				AND NOT EXISTS (SELECT 1 FROM finding_fix_decisions fix
					WHERE fix.finding_id = NEW.finding_id)
				AND (SELECT sequence FROM run_events
					WHERE run_id = NEW.run_id AND type = 'artifact.created'
						AND subject_id = NEW.artifact_id
					ORDER BY sequence DESC LIMIT 1) >
					(SELECT sequence FROM run_events
					WHERE run_id = NEW.run_id AND type = 'finding.accepted'
						AND subject_id = NEW.acceptance_id
					ORDER BY sequence DESC LIMIT 1)
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding remediation Evidence binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_remediation_evidence_operation_insert
		BEFORE INSERT ON finding_remediation_evidence_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_remediation_evidence evidence
			WHERE evidence.id = NEW.evidence_id
				AND evidence.acceptance_id = NEW.acceptance_id
				AND evidence.finding_id = NEW.finding_id
				AND evidence.artifact_id = NEW.artifact_id
				AND evidence.run_id = NEW.run_id
				AND evidence.attached_by = NEW.attached_by
				AND evidence.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding remediation Evidence operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_fix_insert
		BEFORE INSERT ON finding_fix_decisions
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_acceptance_decisions acceptance
			JOIN finding_reports report ON report.id = acceptance.report_id
			JOIN findings finding ON finding.id = acceptance.finding_id
			WHERE acceptance.id = NEW.acceptance_id
				AND acceptance.finding_id = NEW.finding_id
				AND acceptance.run_id = NEW.run_id
				AND report.id = NEW.report_id AND report.run_id = NEW.run_id
				AND report.status = 'generated' AND finding.run_id = NEW.run_id
				AND acceptance.status = NEW.from_status
				AND julianday(NEW.created_at) >= julianday(acceptance.created_at)
				AND julianday(NEW.created_at) >= (SELECT MAX(julianday(evidence.created_at))
					FROM finding_remediation_evidence evidence
					WHERE evidence.finding_id = NEW.finding_id)
				AND NEW.remediation_evidence_count = (SELECT COUNT(*)
					FROM finding_remediation_evidence evidence
					WHERE evidence.finding_id = NEW.finding_id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding fix binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_fix_operation_insert
		BEFORE INSERT ON finding_fix_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM finding_fix_decisions fix
			WHERE fix.id = NEW.fix_id
				AND fix.acceptance_id = NEW.acceptance_id
				AND fix.finding_id = NEW.finding_id
				AND fix.run_id = NEW.run_id
				AND fix.decided_by = NEW.decided_by
				AND fix.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'finding fix operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_finding_acceptance_update_immutable
		BEFORE UPDATE ON finding_acceptance_decisions BEGIN
			SELECT RAISE(ABORT, 'finding acceptance cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_acceptance_delete_immutable
		BEFORE DELETE ON finding_acceptance_decisions BEGIN
			SELECT RAISE(ABORT, 'finding acceptance cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_acceptance_operation_update_immutable
		BEFORE UPDATE ON finding_acceptance_operations BEGIN
			SELECT RAISE(ABORT, 'finding acceptance operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_acceptance_operation_delete_immutable
		BEFORE DELETE ON finding_acceptance_operations BEGIN
			SELECT RAISE(ABORT, 'finding acceptance operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_remediation_evidence_update_immutable
		BEFORE UPDATE ON finding_remediation_evidence BEGIN
			SELECT RAISE(ABORT, 'finding remediation Evidence cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_remediation_evidence_delete_immutable
		BEFORE DELETE ON finding_remediation_evidence BEGIN
			SELECT RAISE(ABORT, 'finding remediation Evidence cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_remediation_evidence_operation_update_immutable
		BEFORE UPDATE ON finding_remediation_evidence_operations BEGIN
			SELECT RAISE(ABORT, 'finding remediation Evidence operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_remediation_evidence_operation_delete_immutable
		BEFORE DELETE ON finding_remediation_evidence_operations BEGIN
			SELECT RAISE(ABORT, 'finding remediation Evidence operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_fix_update_immutable
		BEFORE UPDATE ON finding_fix_decisions BEGIN
			SELECT RAISE(ABORT, 'finding fix cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_fix_delete_immutable
		BEFORE DELETE ON finding_fix_decisions BEGIN
			SELECT RAISE(ABORT, 'finding fix cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_finding_fix_operation_update_immutable
		BEFORE UPDATE ON finding_fix_operations BEGIN
			SELECT RAISE(ABORT, 'finding fix operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_finding_fix_operation_delete_immutable
		BEFORE DELETE ON finding_fix_operations BEGIN
			SELECT RAISE(ABORT, 'finding fix operation cannot be deleted');
		END;`,
}

var specialistOperatorScheduleStatements = []string{
	`CREATE TABLE specialist_operator_schedule_requests (
		id TEXT PRIMARY KEY,
		application_id TEXT NOT NULL,
		proposal_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		max_rounds INTEGER NOT NULL,
		agent_count INTEGER NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(application_id) REFERENCES specialist_delegation_applications(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(root_agent_id) REFERENCES agent_nodes(id) ON DELETE RESTRICT,
		CHECK(max_rounds BETWEEN 1 AND 32),
		CHECK(agent_count BETWEEN 1 AND 2),
		CHECK(length(policy_fingerprint) = 64
			AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by)
			AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_specialist_operator_schedule_request_application
		ON specialist_operator_schedule_requests(application_id, created_at, id);`,
	`CREATE INDEX idx_specialist_operator_schedule_request_run
		ON specialist_operator_schedule_requests(run_id, created_at, id);`,
	`CREATE TABLE specialist_operator_schedule_request_agents (
		request_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		PRIMARY KEY(request_id, ordinal),
		UNIQUE(request_id, agent_id),
		FOREIGN KEY(request_id) REFERENCES specialist_operator_schedule_requests(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(agent_id) REFERENCES agent_nodes(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 2)
	);`,
	`CREATE INDEX idx_specialist_operator_schedule_request_agent
		ON specialist_operator_schedule_request_agents(run_id, agent_id, request_id);`,
	`CREATE TABLE specialist_operator_schedule_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		request_id TEXT NOT NULL UNIQUE,
		application_id TEXT NOT NULL,
		proposal_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(request_id) REFERENCES specialist_operator_schedule_requests(id) ON DELETE RESTRICT,
		FOREIGN KEY(application_id) REFERENCES specialist_delegation_applications(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES specialist_delegation_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by)
			AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TABLE specialist_operator_schedule_attempts (
		request_id TEXT NOT NULL,
		schedule_id TEXT NOT NULL UNIQUE,
		ordinal INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(request_id, ordinal),
		FOREIGN KEY(request_id) REFERENCES specialist_operator_schedule_requests(id) ON DELETE RESTRICT,
		FOREIGN KEY(schedule_id) REFERENCES specialist_schedules(id) ON DELETE RESTRICT,
		CHECK(ordinal > 0)
	);`,
	`CREATE TRIGGER trg_specialist_operator_schedule_request_insert
		BEFORE INSERT ON specialist_operator_schedule_requests
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_delegation_applications application
			JOIN specialist_delegation_proposals proposal
				ON proposal.id = application.proposal_id
			JOIN runs run ON run.id = application.run_id
			JOIN agent_nodes root ON root.id = application.root_agent_id
			WHERE application.id = NEW.application_id
				AND application.proposal_id = NEW.proposal_id
				AND application.run_id = NEW.run_id
				AND application.root_agent_id = NEW.root_agent_id
				AND application.requested_by = NEW.requested_by
				AND application.status = 'applied'
				AND application.completed_at IS NOT NULL
				AND julianday(NEW.created_at) >= julianday(application.completed_at)
				AND proposal.run_id = NEW.run_id
				AND proposal.root_agent_id = NEW.root_agent_id
				AND run.status = 'running'
				AND root.run_id = NEW.run_id AND root.role = 'root'
				AND root.status = 'ready' AND root.active_attempt_id = ''
				AND NOT EXISTS (SELECT 1 FROM specialist_schedules schedule
					WHERE schedule.run_id = NEW.run_id AND schedule.status = 'running')
		)
		BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule request binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_request_agent_insert
		BEFORE INSERT ON specialist_operator_schedule_request_agents
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_operator_schedule_requests request
			JOIN specialist_delegation_application_assignments assignment
				ON assignment.application_id = request.application_id
			JOIN agent_nodes agent ON agent.id = assignment.agent_id
			WHERE request.id = NEW.request_id AND request.run_id = NEW.run_id
				AND assignment.status = 'instructed'
				AND assignment.agent_id = NEW.agent_id
				AND agent.run_id = NEW.run_id AND agent.role = 'specialist'
				AND agent.parent_id = request.root_agent_id
				AND agent.status = 'ready' AND agent.active_attempt_id = ''
				AND NEW.ordinal = 1 + (SELECT COUNT(*)
					FROM specialist_operator_schedule_request_agents existing
					WHERE existing.request_id = NEW.request_id)
				AND NOT EXISTS (
					SELECT 1 FROM specialist_operator_schedule_request_agents reserved
					WHERE reserved.run_id = NEW.run_id
						AND reserved.agent_id = NEW.agent_id
						AND reserved.request_id != NEW.request_id
						AND (
							NOT EXISTS (SELECT 1 FROM specialist_operator_schedule_attempts attempt
								WHERE attempt.request_id = reserved.request_id)
							OR COALESCE((SELECT schedule.status
								FROM specialist_operator_schedule_attempts attempt
								JOIN specialist_schedules schedule ON schedule.id = attempt.schedule_id
								WHERE attempt.request_id = reserved.request_id
								ORDER BY attempt.ordinal DESC LIMIT 1), '')
								IN ('running', 'abandoned')
						)
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule Agent binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_operation_insert
		BEFORE INSERT ON specialist_operator_schedule_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_operator_schedule_requests request
			WHERE request.id = NEW.request_id
				AND request.application_id = NEW.application_id
				AND request.proposal_id = NEW.proposal_id
				AND request.run_id = NEW.run_id
				AND request.requested_by = NEW.requested_by
				AND request.created_at = NEW.created_at
				AND request.agent_count = (SELECT COUNT(*)
					FROM specialist_operator_schedule_request_agents agent
					WHERE agent.request_id = request.id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_attempt_insert
		BEFORE INSERT ON specialist_operator_schedule_attempts
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_operator_schedule_requests request
			JOIN specialist_operator_schedule_operations operation
				ON operation.request_id = request.id
			JOIN specialist_schedules schedule ON schedule.id = NEW.schedule_id
			WHERE request.id = NEW.request_id
				AND schedule.run_id = request.run_id
				AND schedule.max_rounds = request.max_rounds
				AND schedule.status = 'running'
				AND schedule.started_at = NEW.created_at
				AND NEW.ordinal = 1 + (SELECT COUNT(*)
					FROM specialist_operator_schedule_attempts existing
					WHERE existing.request_id = NEW.request_id)
				AND (NEW.ordinal = 1 OR (SELECT previous.status
					FROM specialist_operator_schedule_attempts prior
					JOIN specialist_schedules previous ON previous.id = prior.schedule_id
					WHERE prior.request_id = NEW.request_id
					ORDER BY prior.ordinal DESC LIMIT 1) = 'abandoned')
				AND request.agent_count = (SELECT COUNT(*)
					FROM specialist_schedule_agents target
					WHERE target.schedule_id = NEW.schedule_id)
				AND NOT EXISTS (
					SELECT 1 FROM specialist_operator_schedule_request_agents requested
					LEFT JOIN specialist_schedule_agents target
						ON target.schedule_id = NEW.schedule_id
						AND target.agent_id = requested.agent_id
					WHERE requested.request_id = NEW.request_id
						AND target.agent_id IS NULL)
				AND NOT EXISTS (
					SELECT 1 FROM specialist_schedule_agents target
					LEFT JOIN specialist_operator_schedule_request_agents requested
						ON requested.request_id = NEW.request_id
						AND requested.agent_id = target.agent_id
					WHERE target.schedule_id = NEW.schedule_id
						AND requested.agent_id IS NULL)
		)
		BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule attempt binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_request_update_immutable
		BEFORE UPDATE ON specialist_operator_schedule_requests BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule request cannot be updated');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_request_delete_immutable
		BEFORE DELETE ON specialist_operator_schedule_requests BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule request cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_request_agent_update_immutable
		BEFORE UPDATE ON specialist_operator_schedule_request_agents BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule Agent cannot be updated');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_request_agent_delete_immutable
		BEFORE DELETE ON specialist_operator_schedule_request_agents BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule Agent cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_operation_update_immutable
		BEFORE UPDATE ON specialist_operator_schedule_operations BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_operation_delete_immutable
		BEFORE DELETE ON specialist_operator_schedule_operations BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_attempt_update_immutable
		BEFORE UPDATE ON specialist_operator_schedule_attempts BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule attempt cannot be updated');
		END;`,
	`CREATE TRIGGER trg_specialist_operator_schedule_attempt_delete_immutable
		BEFORE DELETE ON specialist_operator_schedule_attempts BEGIN
			SELECT RAISE(ABORT, 'specialist operator schedule attempt cannot be deleted');
		END;`,
}

var skillSelectionStatements = []string{
	`CREATE TABLE run_skill_selections (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL UNIQUE,
		mission_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		profile TEXT NOT NULL,
		token_budget INTEGER NOT NULL,
		token_upper_bound INTEGER NOT NULL,
		item_count INTEGER NOT NULL,
		selection_fingerprint TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'skill_selection.v1'),
		CHECK(profile IN ('code', 'review', 'learn', 'script')),
		CHECK(token_budget BETWEEN 1 AND 8192),
		CHECK(token_upper_bound BETWEEN 1 AND token_budget),
		CHECK(item_count BETWEEN 1 AND 8),
		CHECK(length(selection_fingerprint) = 64
			AND selection_fingerprint = lower(selection_fingerprint)
			AND selection_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256 AND instr(mission_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_run_skill_selections_mission
		ON run_skill_selections(mission_id, created_at, id);`,
	`CREATE TABLE run_skill_selection_items (
		selection_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		content_sha256 TEXT NOT NULL,
		content_bytes INTEGER NOT NULL,
		token_upper_bound INTEGER NOT NULL,
		PRIMARY KEY(selection_id, ordinal),
		UNIQUE(selection_id, name),
		FOREIGN KEY(selection_id) REFERENCES run_skill_selections(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 8),
		CHECK(length(name) BETWEEN 1 AND 64 AND name = lower(name)
			AND substr(name, 1, 1) GLOB '[a-z]' AND substr(name, -1, 1) != '-'
			AND name NOT GLOB '*[^a-z0-9-]*'),
		CHECK(length(version) BETWEEN 5 AND 29 AND version = trim(version)),
		CHECK(length(content_sha256) = 64 AND content_sha256 = lower(content_sha256)
			AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(content_bytes BETWEEN 1 AND 4096),
		CHECK(token_upper_bound BETWEEN 1 AND 4096),
		CHECK(token_upper_bound = content_bytes)
	);`,
	`CREATE TABLE run_skill_selection_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		selection_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL UNIQUE,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(selection_id) REFERENCES run_skill_selections(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_run_skill_selection_insert
		BEFORE INSERT ON run_skill_selections
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND run.status = 'created' AND mission.profile = NEW.profile
				AND julianday(NEW.created_at) >= julianday(run.created_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Skill selection Run binding is invalid');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_item_insert
		BEFORE INSERT ON run_skill_selection_items
		WHEN NOT EXISTS (
			SELECT 1 FROM run_skill_selections selection
			WHERE selection.id = NEW.selection_id
				AND NEW.ordinal = 1 + (SELECT COUNT(*) FROM run_skill_selection_items existing
					WHERE existing.selection_id = NEW.selection_id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Skill selection item binding is invalid');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_operation_insert
		BEFORE INSERT ON run_skill_selection_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM run_skill_selections selection
			WHERE selection.id = NEW.selection_id AND selection.run_id = NEW.run_id
				AND selection.requested_by = NEW.requested_by
				AND selection.created_at = NEW.created_at
				AND selection.item_count = (SELECT COUNT(*) FROM run_skill_selection_items item
					WHERE item.selection_id = selection.id)
				AND selection.token_upper_bound = (SELECT COALESCE(SUM(item.token_upper_bound), 0)
					FROM run_skill_selection_items item WHERE item.selection_id = selection.id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Skill selection operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_update_immutable
		BEFORE UPDATE ON run_skill_selections BEGIN
			SELECT RAISE(ABORT, 'Skill selection cannot be updated');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_delete_immutable
		BEFORE DELETE ON run_skill_selections BEGIN
			SELECT RAISE(ABORT, 'Skill selection cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_item_update_immutable
		BEFORE UPDATE ON run_skill_selection_items BEGIN
			SELECT RAISE(ABORT, 'Skill selection item cannot be updated');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_item_delete_immutable
		BEFORE DELETE ON run_skill_selection_items BEGIN
			SELECT RAISE(ABORT, 'Skill selection item cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_operation_update_immutable
		BEFORE UPDATE ON run_skill_selection_operations BEGIN
			SELECT RAISE(ABORT, 'Skill selection operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_run_skill_selection_operation_delete_immutable
		BEFORE DELETE ON run_skill_selection_operations BEGIN
			SELECT RAISE(ABORT, 'Skill selection operation cannot be deleted');
		END;`,
}

var rootSkillContextStatements = []string{
	`CREATE TABLE root_skill_context_preparations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		supervisor_attempt_id TEXT NOT NULL,
		turn_number INTEGER NOT NULL,
		selection_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		profile TEXT NOT NULL,
		selection_fingerprint TEXT NOT NULL,
		context_fingerprint TEXT NOT NULL,
		item_count INTEGER NOT NULL,
		token_budget INTEGER NOT NULL,
		token_upper_bound INTEGER NOT NULL,
		redaction_count INTEGER NOT NULL,
		prepared_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(selection_id) REFERENCES run_skill_selections(id) ON DELETE RESTRICT,
		UNIQUE(run_id, supervisor_attempt_id),
		CHECK(protocol_version = 'skill_context.v1'),
		CHECK(profile IN ('code', 'review', 'learn', 'script')),
		CHECK(turn_number > 0),
		CHECK(item_count BETWEEN 1 AND 8),
		CHECK(token_budget BETWEEN 1 AND 8192),
		CHECK(token_upper_bound BETWEEN 1 AND token_budget),
		CHECK(redaction_count BETWEEN 0 AND token_budget),
		CHECK(length(selection_fingerprint) = 64
			AND selection_fingerprint = lower(selection_fingerprint)
			AND selection_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(context_fingerprint) = 64
			AND context_fingerprint = lower(context_fingerprint)
			AND context_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256 AND instr(mission_id, char(0)) = 0),
		CHECK(root_agent_id = trim(root_agent_id) AND length(root_agent_id) BETWEEN 1 AND 256
			AND instr(root_agent_id, char(0)) = 0),
		CHECK(supervisor_attempt_id = trim(supervisor_attempt_id)
			AND length(supervisor_attempt_id) BETWEEN 1 AND 256
			AND instr(supervisor_attempt_id, char(0)) = 0),
		CHECK(selection_id = trim(selection_id) AND length(selection_id) BETWEEN 1 AND 256
			AND instr(selection_id, char(0)) = 0)
	);`,
	`CREATE INDEX idx_root_skill_context_run_turn
		ON root_skill_context_preparations(run_id, turn_number, prepared_at, id);`,
	`CREATE TABLE root_skill_context_commits (
		preparation_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		supervisor_attempt_id TEXT NOT NULL UNIQUE,
		model_attempt INTEGER NOT NULL,
		committed_at TEXT NOT NULL,
		FOREIGN KEY(preparation_id) REFERENCES root_skill_context_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(model_attempt > 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(supervisor_attempt_id = trim(supervisor_attempt_id)
			AND length(supervisor_attempt_id) BETWEEN 1 AND 256
			AND instr(supervisor_attempt_id, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_root_skill_context_preparation_insert
		BEFORE INSERT ON root_skill_context_preparations
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = run.id
			JOIN agent_nodes root ON root.run_id = run.id AND root.id = NEW.root_agent_id
			JOIN run_skill_selections selection ON selection.id = NEW.selection_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND mission.profile = NEW.profile AND run.status = 'running'
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = NEW.supervisor_attempt_id
				AND checkpoint.next_turn = NEW.turn_number
				AND root.role = 'root' AND root.status = 'running'
				AND root.active_attempt_id = NEW.supervisor_attempt_id
				AND selection.run_id = NEW.run_id AND selection.mission_id = NEW.mission_id
				AND selection.profile = NEW.profile
				AND selection.selection_fingerprint = NEW.selection_fingerprint
				AND selection.item_count = NEW.item_count
				AND selection.token_budget = NEW.token_budget
				AND NEW.token_upper_bound <= selection.token_upper_bound
		)
		BEGIN
			SELECT RAISE(ABORT, 'root Skill context preparation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_root_skill_context_commit_insert
		BEFORE INSERT ON root_skill_context_commits
		WHEN NOT EXISTS (
			SELECT 1 FROM root_skill_context_preparations preparation
			JOIN runs run ON run.id = preparation.run_id
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = run.id
			JOIN agent_nodes root
				ON root.run_id = run.id AND root.id = preparation.root_agent_id
			WHERE preparation.id = NEW.preparation_id
				AND preparation.run_id = NEW.run_id
				AND preparation.supervisor_attempt_id = NEW.supervisor_attempt_id
				AND run.status = 'running' AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = NEW.supervisor_attempt_id
				AND checkpoint.next_turn = preparation.turn_number
				AND root.role = 'root' AND root.status = 'running'
				AND root.active_attempt_id = NEW.supervisor_attempt_id
				AND julianday(NEW.committed_at) >= julianday(preparation.prepared_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'root Skill context commit binding is invalid');
		END;`,
	`CREATE TRIGGER trg_root_skill_context_preparation_update_immutable
		BEFORE UPDATE ON root_skill_context_preparations BEGIN
			SELECT RAISE(ABORT, 'root Skill context preparation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_root_skill_context_preparation_delete_immutable
		BEFORE DELETE ON root_skill_context_preparations BEGIN
			SELECT RAISE(ABORT, 'root Skill context preparation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_root_skill_context_commit_update_immutable
		BEFORE UPDATE ON root_skill_context_commits BEGIN
			SELECT RAISE(ABORT, 'root Skill context commit cannot be updated');
		END;`,
	`CREATE TRIGGER trg_root_skill_context_commit_delete_immutable
		BEFORE DELETE ON root_skill_context_commits BEGIN
			SELECT RAISE(ABORT, 'root Skill context commit cannot be deleted');
		END;`,
}

var rootModeSkillContextStatements = []string{
	`CREATE TABLE root_mode_skill_context_preparations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		supervisor_attempt_id TEXT NOT NULL,
		turn_number INTEGER NOT NULL,
		selection_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		profile TEXT NOT NULL,
		mode_snapshot_id TEXT NOT NULL,
		mode_revision INTEGER NOT NULL,
		surface TEXT NOT NULL,
		phase TEXT NOT NULL,
		selection_fingerprint TEXT NOT NULL,
		context_fingerprint TEXT NOT NULL,
		selection_item_count INTEGER NOT NULL,
		item_count INTEGER NOT NULL,
		token_budget INTEGER NOT NULL,
		token_upper_bound INTEGER NOT NULL,
		redaction_count INTEGER NOT NULL,
		prepared_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(selection_id) REFERENCES run_skill_selections(id) ON DELETE RESTRICT,
		FOREIGN KEY(mode_snapshot_id) REFERENCES run_mode_snapshots(id) ON DELETE RESTRICT,
		UNIQUE(run_id, supervisor_attempt_id),
		CHECK(protocol_version = 'skill_context.v1'),
		CHECK(profile IN ('code', 'review', 'learn', 'script')),
		CHECK(surface IN ('code', 'cyber')),
		CHECK(phase IN ('plan', 'deliver')),
		CHECK(turn_number > 0),
		CHECK(mode_revision > 0),
		CHECK(selection_item_count BETWEEN 1 AND 8),
		CHECK(item_count BETWEEN 0 AND selection_item_count),
		CHECK(token_budget BETWEEN 1 AND 8192),
		CHECK(token_upper_bound BETWEEN 0 AND token_budget),
		CHECK(redaction_count BETWEEN 0 AND token_budget),
		CHECK((item_count = 0 AND token_upper_bound = 0 AND redaction_count = 0)
			OR (item_count > 0 AND token_upper_bound > 0)),
		CHECK(length(selection_fingerprint) = 64
			AND selection_fingerprint = lower(selection_fingerprint)
			AND selection_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(context_fingerprint) = 64
			AND context_fingerprint = lower(context_fingerprint)
			AND context_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256 AND instr(mission_id, char(0)) = 0),
		CHECK(root_agent_id = trim(root_agent_id) AND length(root_agent_id) BETWEEN 1 AND 256
			AND instr(root_agent_id, char(0)) = 0),
		CHECK(supervisor_attempt_id = trim(supervisor_attempt_id)
			AND length(supervisor_attempt_id) BETWEEN 1 AND 256
			AND instr(supervisor_attempt_id, char(0)) = 0),
		CHECK(selection_id = trim(selection_id) AND length(selection_id) BETWEEN 1 AND 256
			AND instr(selection_id, char(0)) = 0),
		CHECK(mode_snapshot_id = trim(mode_snapshot_id)
			AND length(mode_snapshot_id) BETWEEN 1 AND 256
			AND instr(mode_snapshot_id, char(0)) = 0)
	);`,
	`CREATE INDEX idx_root_mode_skill_context_run_turn
		ON root_mode_skill_context_preparations(run_id, turn_number, prepared_at, id);`,
	`CREATE TABLE root_mode_skill_context_commits (
		preparation_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		supervisor_attempt_id TEXT NOT NULL UNIQUE,
		model_attempt INTEGER NOT NULL,
		committed_at TEXT NOT NULL,
		FOREIGN KEY(preparation_id) REFERENCES root_mode_skill_context_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(model_attempt > 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(supervisor_attempt_id = trim(supervisor_attempt_id)
			AND length(supervisor_attempt_id) BETWEEN 1 AND 256
			AND instr(supervisor_attempt_id, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_root_mode_skill_context_preparation_insert
		BEFORE INSERT ON root_mode_skill_context_preparations
		WHEN EXISTS (
			SELECT 1 FROM root_skill_context_preparations legacy
			WHERE legacy.run_id = NEW.run_id
				AND legacy.supervisor_attempt_id = NEW.supervisor_attempt_id
		) OR NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = run.id
			JOIN agent_nodes root ON root.run_id = run.id AND root.id = NEW.root_agent_id
			JOIN run_skill_selections selection ON selection.id = NEW.selection_id
			JOIN run_mode_snapshots mode ON mode.id = NEW.mode_snapshot_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND mission.profile = NEW.profile AND run.status = 'running'
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = NEW.supervisor_attempt_id
				AND checkpoint.next_turn = NEW.turn_number
				AND root.role = 'root' AND root.status = 'running'
				AND root.active_attempt_id = NEW.supervisor_attempt_id
				AND selection.run_id = NEW.run_id AND selection.mission_id = NEW.mission_id
				AND selection.profile = NEW.profile
				AND selection.selection_fingerprint = NEW.selection_fingerprint
				AND selection.item_count = NEW.selection_item_count
				AND selection.token_budget = NEW.token_budget
				AND NEW.item_count <= selection.item_count
				AND NEW.token_upper_bound <= selection.token_upper_bound
				AND mode.run_id = NEW.run_id AND mode.mission_id = NEW.mission_id
				AND mode.revision = NEW.mode_revision AND mode.surface = NEW.surface
				AND mode.phase = NEW.phase AND mode.profile = NEW.profile
				AND mode.revision = (
					SELECT MAX(current.revision) FROM run_mode_snapshots current
					WHERE current.run_id = NEW.run_id
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'mode-bound root Skill context preparation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_root_mode_skill_context_commit_insert
		BEFORE INSERT ON root_mode_skill_context_commits
		WHEN NOT EXISTS (
			SELECT 1 FROM root_mode_skill_context_preparations preparation
			JOIN runs run ON run.id = preparation.run_id
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = run.id
			JOIN agent_nodes root
				ON root.run_id = run.id AND root.id = preparation.root_agent_id
			WHERE preparation.id = NEW.preparation_id
				AND preparation.run_id = NEW.run_id
				AND preparation.supervisor_attempt_id = NEW.supervisor_attempt_id
				AND run.status = 'running' AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = NEW.supervisor_attempt_id
				AND checkpoint.next_turn = preparation.turn_number
				AND root.role = 'root' AND root.status = 'running'
				AND root.active_attempt_id = NEW.supervisor_attempt_id
				AND julianday(NEW.committed_at) >= julianday(preparation.prepared_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'mode-bound root Skill context commit binding is invalid');
		END;`,
	`CREATE TRIGGER trg_root_mode_skill_context_preparation_update_immutable
		BEFORE UPDATE ON root_mode_skill_context_preparations BEGIN
			SELECT RAISE(ABORT, 'mode-bound root Skill context preparation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_root_mode_skill_context_preparation_delete_immutable
		BEFORE DELETE ON root_mode_skill_context_preparations BEGIN
			SELECT RAISE(ABORT, 'mode-bound root Skill context preparation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_root_mode_skill_context_commit_update_immutable
		BEFORE UPDATE ON root_mode_skill_context_commits BEGIN
			SELECT RAISE(ABORT, 'mode-bound root Skill context commit cannot be updated');
		END;`,
	`CREATE TRIGGER trg_root_mode_skill_context_commit_delete_immutable
		BEFORE DELETE ON root_mode_skill_context_commits BEGIN
			SELECT RAISE(ABORT, 'mode-bound root Skill context commit cannot be deleted');
		END;`,
}

var runModeStatements = []string{
	`CREATE TABLE run_mode_snapshots (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		revision INTEGER NOT NULL,
		protocol_version TEXT NOT NULL,
		surface TEXT NOT NULL,
		phase TEXT NOT NULL,
		profile TEXT NOT NULL,
		scope_json TEXT NOT NULL,
		policy_version TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		reason TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		UNIQUE(run_id, revision),
		CHECK(revision > 0),
		CHECK(protocol_version = 'run_mode.v1'),
		CHECK(surface IN ('code', 'cyber')),
		CHECK(phase IN ('plan', 'deliver')),
		CHECK(profile IN ('code', 'review', 'learn', 'script')),
		CHECK(json_valid(scope_json) AND length(scope_json) BETWEEN 2 AND 1048576),
		CHECK(policy_version = 'mode_policy.v1'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256
			AND instr(mission_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK(reason = trim(reason) AND length(reason) BETWEEN 1 AND 1024
			AND instr(reason, char(0)) = 0)
	);`,
	`CREATE INDEX idx_run_mode_snapshots_run_revision
		ON run_mode_snapshots(run_id, revision DESC);`,
	`CREATE TABLE run_mode_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		snapshot_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(snapshot_id) REFERENCES run_mode_snapshots(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`INSERT INTO run_mode_snapshots
		(id, run_id, mission_id, revision, protocol_version, surface, phase, profile,
		scope_json, policy_version, requested_by, reason, created_at)
		SELECT printf('run-mode-v41-%016x', run.rowid), run.id, run.mission_id, 1, 'run_mode.v1',
			'code', 'deliver', mission.profile, mission.scope_json, 'mode_policy.v1',
			'schema_v41', 'legacy compatibility default', run.created_at
		FROM runs run JOIN missions mission ON mission.id = run.mission_id;`,
	`CREATE TRIGGER trg_run_mode_snapshot_insert
		BEFORE INSERT ON run_mode_snapshots
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND mission.profile = NEW.profile AND mission.scope_json = NEW.scope_json
				AND julianday(NEW.created_at) >= julianday(run.created_at)
				AND (
					(NEW.revision = 1 AND run.status = 'created' AND NOT EXISTS (
						SELECT 1 FROM run_mode_snapshots existing WHERE existing.run_id = NEW.run_id
					))
					OR
					(NEW.revision > 1 AND run.status IN ('created', 'paused')
					AND NOT EXISTS (
						SELECT 1 FROM run_execution_leases lease
						WHERE lease.run_id = NEW.run_id AND lease.status = 'active'
							AND julianday(lease.expires_at) > julianday('now')
					) AND EXISTS (
						SELECT 1 FROM run_mode_snapshots previous
						WHERE previous.run_id = NEW.run_id AND previous.revision = NEW.revision - 1
							AND previous.protocol_version = NEW.protocol_version
							AND previous.surface = NEW.surface AND previous.phase != NEW.phase
							AND previous.profile = NEW.profile AND previous.scope_json = NEW.scope_json
							AND previous.policy_version = NEW.policy_version
							AND julianday(NEW.created_at) >= julianday(previous.created_at)
					))
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Run mode snapshot binding or transition is invalid');
		END;`,
	`CREATE TRIGGER trg_run_mode_operation_insert
		BEFORE INSERT ON run_mode_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM run_mode_snapshots snapshot
			WHERE snapshot.id = NEW.snapshot_id AND snapshot.run_id = NEW.run_id
				AND snapshot.requested_by = NEW.requested_by
				AND snapshot.created_at = NEW.created_at AND snapshot.revision > 1
		)
		BEGIN
			SELECT RAISE(ABORT, 'Run mode operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_run_mode_snapshot_update_immutable
		BEFORE UPDATE ON run_mode_snapshots BEGIN
			SELECT RAISE(ABORT, 'Run mode snapshot cannot be updated');
		END;`,
	`CREATE TRIGGER trg_run_mode_snapshot_delete_immutable
		BEFORE DELETE ON run_mode_snapshots BEGIN
			SELECT RAISE(ABORT, 'Run mode snapshot cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_run_mode_operation_update_immutable
		BEFORE UPDATE ON run_mode_operations BEGIN
			SELECT RAISE(ABORT, 'Run mode operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_run_mode_operation_delete_immutable
		BEFORE DELETE ON run_mode_operations BEGIN
			SELECT RAISE(ABORT, 'Run mode operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_run_mode_plan_completion_guard
		BEFORE UPDATE OF status ON runs
		WHEN NEW.status = 'completed' AND OLD.status != 'completed'
			AND COALESCE((
				SELECT phase FROM run_mode_snapshots snapshot
				WHERE snapshot.run_id = NEW.id
				ORDER BY snapshot.revision DESC LIMIT 1
			), '') != 'deliver'
		BEGIN
			SELECT RAISE(ABORT, 'Plan-phase Run cannot be completed');
		END;`,
}

var planDeliveryStatements = []string{
	`DROP TRIGGER trg_supervisor_tool_call_model_attempt;`,
	`DROP TRIGGER trg_supervisor_tool_round_completion;`,
	`DROP INDEX idx_run_supervisor_tool_calls_pending;`,
	`ALTER TABLE run_supervisor_tool_calls RENAME TO run_supervisor_tool_calls_v41;`,
	`CREATE TABLE run_supervisor_tool_calls (
		run_id TEXT NOT NULL,
		turn INTEGER NOT NULL,
		attempt_id TEXT NOT NULL,
		round INTEGER NOT NULL,
		position INTEGER NOT NULL,
		model_attempt INTEGER NOT NULL,
		call_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		status TEXT NOT NULL,
		result_json TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		completed_at TEXT,
		PRIMARY KEY(run_id, turn, attempt_id, round, position),
		UNIQUE(run_id, turn, attempt_id, call_id),
		FOREIGN KEY(run_id, turn, attempt_id, round)
			REFERENCES run_supervisor_tool_rounds(run_id, turn, attempt_id, round) ON DELETE CASCADE,
		CHECK(position BETWEEN 1 AND 4),
		CHECK(model_attempt > 0),
		CHECK(tool_name IN ('work_item_create', 'note_create',
			'specialist_delegation_propose', 'plan_delivery_propose')),
		CHECK(status IN ('pending', 'completed', 'denied', 'failed')),
		CHECK((status = 'pending' AND result_json = '' AND error_code = '' AND completed_at IS NULL)
			OR (status = 'completed' AND length(result_json) > 0 AND error_code = '' AND completed_at IS NOT NULL)
			OR (status IN ('denied', 'failed') AND length(result_json) > 0 AND length(error_code) > 0
				AND completed_at IS NOT NULL))
	);`,
	`INSERT INTO run_supervisor_tool_calls
		(run_id, turn, attempt_id, round, position, model_attempt, call_id, tool_name,
		payload_json, status, result_json, error_code, created_at, completed_at)
		SELECT run_id, turn, attempt_id, round, position, model_attempt, call_id, tool_name,
		payload_json, status, result_json, error_code, created_at, completed_at
		FROM run_supervisor_tool_calls_v41;`,
	`DROP TABLE run_supervisor_tool_calls_v41;`,
	`CREATE INDEX idx_run_supervisor_tool_calls_pending
		ON run_supervisor_tool_calls(run_id, turn, attempt_id, status, round, position);`,
	`CREATE TRIGGER trg_supervisor_tool_call_model_attempt
		BEFORE INSERT ON run_supervisor_tool_calls
		WHEN NOT EXISTS (
			SELECT 1 FROM run_supervisor_tool_rounds
			WHERE run_id = NEW.run_id AND turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND round = NEW.round AND model_attempt = NEW.model_attempt
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool call model attempt mismatch');
		END;`,
	`CREATE TRIGGER trg_supervisor_tool_round_completion
		BEFORE UPDATE OF completed_at ON run_supervisor_tool_rounds
		WHEN NEW.completed_at IS NOT NULL AND EXISTS (
			SELECT 1 FROM run_supervisor_tool_calls
			WHERE run_id = NEW.run_id AND turn = NEW.turn AND attempt_id = NEW.attempt_id
				AND round = NEW.round AND status = 'pending'
		)
		BEGIN
			SELECT RAISE(ABORT, 'supervisor tool round still has pending calls');
		END;`,
	`CREATE TABLE plan_delivery_proposals (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		root_agent_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		mode_revision INTEGER NOT NULL,
		protocol_version TEXT NOT NULL,
		status TEXT NOT NULL,
		direction_count INTEGER NOT NULL,
		proposal_fingerprint TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, mode_revision) REFERENCES run_mode_snapshots(run_id, revision) ON DELETE RESTRICT,
		CHECK(protocol_version = 'plan_delivery.v1'),
		CHECK(status = 'proposed'),
		CHECK(direction_count = 3),
		CHECK(mode_revision > 0),
		CHECK(length(proposal_fingerprint) = 64
			AND proposal_fingerprint = lower(proposal_fingerprint)
			AND proposal_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = 'run_supervisor'),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_plan_delivery_proposals_run_created
		ON plan_delivery_proposals(run_id, created_at, id);`,
	`CREATE TABLE plan_delivery_directions (
		proposal_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		title TEXT NOT NULL,
		summary TEXT NOT NULL,
		tradeoffs_json TEXT NOT NULL,
		module_count INTEGER NOT NULL,
		PRIMARY KEY(proposal_id, ordinal),
		FOREIGN KEY(proposal_id) REFERENCES plan_delivery_proposals(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 3),
		CHECK(title = trim(title) AND length(title) BETWEEN 1 AND 240
			AND length(CAST(title AS BLOB)) <= 960 AND instr(title, char(0)) = 0),
		CHECK(summary = trim(summary) AND length(summary) BETWEEN 1 AND 1200
			AND length(CAST(summary AS BLOB)) <= 4800 AND instr(summary, char(0)) = 0),
		CHECK(json_valid(tradeoffs_json) AND json_type(tradeoffs_json) = 'array'
			AND json_array_length(tradeoffs_json) BETWEEN 1 AND 8),
		CHECK(module_count BETWEEN 1 AND 8)
	);`,
	`CREATE TABLE plan_delivery_modules (
		proposal_id TEXT NOT NULL,
		direction_ordinal INTEGER NOT NULL,
		ordinal INTEGER NOT NULL,
		title TEXT NOT NULL,
		objective TEXT NOT NULL,
		acceptance_json TEXT NOT NULL,
		dependencies_json TEXT NOT NULL,
		PRIMARY KEY(proposal_id, direction_ordinal, ordinal),
		FOREIGN KEY(proposal_id, direction_ordinal)
			REFERENCES plan_delivery_directions(proposal_id, ordinal) ON DELETE RESTRICT,
		CHECK(direction_ordinal BETWEEN 1 AND 3),
		CHECK(ordinal BETWEEN 1 AND 8),
		CHECK(title = trim(title) AND length(title) BETWEEN 1 AND 240
			AND length(CAST(title AS BLOB)) <= 960 AND instr(title, char(0)) = 0),
		CHECK(objective = trim(objective) AND length(objective) BETWEEN 1 AND 2400
			AND length(CAST(objective AS BLOB)) <= 9600 AND instr(objective, char(0)) = 0),
		CHECK(json_valid(acceptance_json) AND json_type(acceptance_json) = 'array'
			AND json_array_length(acceptance_json) BETWEEN 1 AND 8),
		CHECK(json_valid(dependencies_json) AND json_type(dependencies_json) = 'array'
			AND json_array_length(dependencies_json) BETWEEN 0 AND 7)
	);`,
	`CREATE TABLE plan_delivery_proposal_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		invocation_id TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		root_agent_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(proposal_id) REFERENCES plan_delivery_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(invocation_id) REFERENCES run_tool_calls(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = 'run_supervisor')
	);`,
	`CREATE TABLE plan_delivery_selections (
		id TEXT PRIMARY KEY,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL UNIQUE,
		root_agent_id TEXT NOT NULL,
		direction_ordinal INTEGER NOT NULL,
		note_id TEXT NOT NULL UNIQUE,
		module_count INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(proposal_id, direction_ordinal)
			REFERENCES plan_delivery_directions(proposal_id, ordinal) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, root_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, note_id) REFERENCES notes(run_id, id) ON DELETE RESTRICT,
		CHECK(direction_ordinal BETWEEN 1 AND 3),
		CHECK(module_count BETWEEN 1 AND 8),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK(version = 1)
	);`,
	`CREATE TABLE plan_delivery_selection_items (
		selection_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		module_ordinal INTEGER NOT NULL,
		work_item_id TEXT NOT NULL UNIQUE,
		PRIMARY KEY(selection_id, ordinal),
		FOREIGN KEY(selection_id) REFERENCES plan_delivery_selections(id) ON DELETE RESTRICT,
		FOREIGN KEY(work_item_id) REFERENCES work_items(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 8),
		CHECK(module_ordinal = ordinal)
	);`,
	`CREATE TABLE plan_delivery_selection_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		selection_id TEXT NOT NULL UNIQUE,
		proposal_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL UNIQUE,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(selection_id) REFERENCES plan_delivery_selections(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES plan_delivery_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_plan_delivery_proposal_insert
		BEFORE INSERT ON plan_delivery_proposals
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN sessions session ON session.id = run.session_id
			JOIN agent_nodes root ON root.run_id = run.id AND root.id = NEW.root_agent_id
			JOIN run_mode_snapshots mode ON mode.run_id = run.id
				AND mode.revision = NEW.mode_revision
			WHERE run.id = NEW.run_id AND run.session_id = NEW.session_id
				AND mission.workspace_id = NEW.workspace_id
				AND run.status = 'running' AND session.status = 'active'
				AND root.role = 'root' AND root.parent_id IS NULL
				AND root.status = 'running' AND root.active_attempt_id <> ''
				AND mode.phase = 'plan'
				AND NOT EXISTS (SELECT 1 FROM run_mode_snapshots later
					WHERE later.run_id = run.id AND later.revision > mode.revision)
				AND julianday(NEW.created_at) >= julianday(mode.created_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery proposal binding is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_direction_insert
		BEFORE INSERT ON plan_delivery_directions
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_proposals proposal
			WHERE proposal.id = NEW.proposal_id AND proposal.status = 'proposed'
				AND NEW.ordinal <= proposal.direction_count
				AND NOT EXISTS (SELECT 1 FROM plan_delivery_directions existing
					WHERE existing.proposal_id = NEW.proposal_id
						AND lower(existing.title) = lower(NEW.title))
				AND NOT EXISTS (SELECT 1 FROM json_each(NEW.tradeoffs_json) item
					WHERE item.type != 'text' OR length(trim(item.value)) = 0
						OR length(item.value) > 512 OR instr(item.value, char(0)) > 0)
				AND (SELECT COUNT(*) FROM json_each(NEW.tradeoffs_json)) =
					(SELECT COUNT(DISTINCT lower(value)) FROM json_each(NEW.tradeoffs_json))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery direction is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_module_insert
		BEFORE INSERT ON plan_delivery_modules
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_directions direction
			WHERE direction.proposal_id = NEW.proposal_id
				AND direction.ordinal = NEW.direction_ordinal
				AND NEW.ordinal <= direction.module_count
				AND NOT EXISTS (SELECT 1 FROM plan_delivery_modules existing
					WHERE existing.proposal_id = NEW.proposal_id
						AND existing.direction_ordinal = NEW.direction_ordinal
						AND lower(existing.title) = lower(NEW.title))
				AND NOT EXISTS (SELECT 1 FROM json_each(NEW.acceptance_json) item
					WHERE item.type != 'text' OR length(trim(item.value)) = 0
						OR length(item.value) > 512 OR instr(item.value, char(0)) > 0)
				AND (SELECT COUNT(*) FROM json_each(NEW.acceptance_json)) =
					(SELECT COUNT(DISTINCT lower(value)) FROM json_each(NEW.acceptance_json))
				AND NOT EXISTS (SELECT 1 FROM json_each(NEW.dependencies_json) dependency
					WHERE dependency.type != 'integer' OR dependency.value < 1
						OR dependency.value >= NEW.ordinal)
				AND (SELECT COUNT(*) FROM json_each(NEW.dependencies_json)) =
					(SELECT COUNT(DISTINCT value) FROM json_each(NEW.dependencies_json))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery module is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_proposal_operation_insert
		BEFORE INSERT ON plan_delivery_proposal_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_proposals proposal
			JOIN runs run ON run.id = proposal.run_id
			JOIN agent_nodes root ON root.run_id = run.id AND root.id = proposal.root_agent_id
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = run.id
			JOIN run_tool_calls invocation ON invocation.id = NEW.invocation_id
			WHERE proposal.id = NEW.proposal_id AND proposal.run_id = NEW.run_id
				AND proposal.session_id = NEW.session_id
				AND proposal.workspace_id = NEW.workspace_id
				AND proposal.root_agent_id = NEW.root_agent_id
				AND proposal.requested_by = NEW.requested_by
				AND proposal.created_at = NEW.created_at
				AND run.status = 'running' AND root.role = 'root'
				AND root.status = 'running' AND root.active_attempt_id <> ''
				AND checkpoint.phase = 'turn_started'
				AND checkpoint.attempt_id = root.active_attempt_id
				AND invocation.run_id = run.id AND invocation.session_id = proposal.session_id
				AND invocation.workspace_id = proposal.workspace_id
				AND invocation.tool_name = 'plan_delivery_propose'
				AND invocation.action_class = 'agent_proposal'
				AND (SELECT COUNT(*) FROM plan_delivery_directions direction
					WHERE direction.proposal_id = proposal.id) = proposal.direction_count
				AND NOT EXISTS (SELECT 1 FROM plan_delivery_directions direction
					WHERE direction.proposal_id = proposal.id
						AND (SELECT COUNT(*) FROM plan_delivery_modules module
							WHERE module.proposal_id = proposal.id
								AND module.direction_ordinal = direction.ordinal)
							!= direction.module_count)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery proposal operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_insert
		BEFORE INSERT ON plan_delivery_selections
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_proposals proposal
			JOIN plan_delivery_directions direction ON direction.proposal_id = proposal.id
				AND direction.ordinal = NEW.direction_ordinal
			JOIN runs run ON run.id = proposal.run_id
			JOIN agent_nodes root ON root.run_id = run.id AND root.id = NEW.root_agent_id
			JOIN notes note ON note.run_id = run.id AND note.id = NEW.note_id
			JOIN run_mode_snapshots mode ON mode.run_id = run.id
				AND mode.revision = proposal.mode_revision
			WHERE proposal.id = NEW.proposal_id AND proposal.run_id = NEW.run_id
				AND proposal.root_agent_id = NEW.root_agent_id
				AND run.status = 'paused' AND mode.phase = 'plan'
				AND NOT EXISTS (SELECT 1 FROM run_mode_snapshots later
					WHERE later.run_id = run.id AND later.revision > mode.revision)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases lease
					WHERE lease.run_id = run.id AND lease.status = 'active'
						AND julianday(lease.expires_at) > julianday('now'))
				AND root.role = 'root' AND root.parent_id IS NULL
				AND root.status IN ('ready', 'waiting') AND root.active_attempt_id = ''
				AND NEW.module_count = direction.module_count
				AND julianday(NEW.created_at) >= julianday(proposal.created_at)
				AND note.status = 'active' AND note.category = 'decision'
				AND note.visibility = 'run' AND note.owner = '' AND note.pinned = 1
				AND note.owner_agent_id = NEW.root_agent_id
				AND note.version = 1 AND note.created_at = NEW.created_at
				AND (SELECT COUNT(*) FROM note_tags tag
					WHERE tag.run_id = run.id AND tag.note_id = note.id
						AND tag.tag IN ('plan-delivery', 'selected-direction')) = 2
				AND (SELECT COUNT(*) FROM note_tags tag
					WHERE tag.run_id = run.id AND tag.note_id = note.id) = 2
				AND (SELECT COUNT(*) FROM note_sources source
					WHERE source.run_id = run.id AND source.note_id = note.id
						AND source.source_ref = 'plan_delivery:' || proposal.id) = 1
				AND (SELECT COUNT(*) FROM note_sources source
					WHERE source.run_id = run.id AND source.note_id = note.id) = 1
				AND NOT EXISTS (SELECT 1 FROM note_evidence evidence
					WHERE evidence.run_id = run.id AND evidence.note_id = note.id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection binding is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_item_insert
		BEFORE INSERT ON plan_delivery_selection_items
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_selections selection
			JOIN plan_delivery_modules module ON module.proposal_id = selection.proposal_id
				AND module.direction_ordinal = selection.direction_ordinal
				AND module.ordinal = NEW.module_ordinal
			JOIN work_items item ON item.id = NEW.work_item_id
			WHERE selection.id = NEW.selection_id AND NEW.ordinal <= selection.module_count
				AND item.run_id = selection.run_id AND item.status = 'pending'
				AND item.priority = 'normal' AND item.owner = ''
				AND item.owner_agent_id = selection.root_agent_id
				AND item.title = module.title AND item.description = module.objective
				AND item.version = 1 AND item.created_at = selection.created_at
				AND NOT EXISTS (SELECT 1 FROM json_each(module.acceptance_json) expected
					WHERE NOT EXISTS (SELECT 1 FROM json_each(item.acceptance_json) actual
						WHERE actual.type = 'text' AND actual.value = expected.value))
				AND NOT EXISTS (SELECT 1 FROM json_each(item.acceptance_json) actual
					WHERE NOT EXISTS (SELECT 1 FROM json_each(module.acceptance_json) expected
						WHERE expected.type = 'text' AND expected.value = actual.value))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection item is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_operation_insert
		BEFORE INSERT ON plan_delivery_selection_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_selections selection
			JOIN plan_delivery_proposals proposal ON proposal.id = selection.proposal_id
			JOIN runs run ON run.id = selection.run_id
			JOIN run_mode_snapshots mode ON mode.run_id = run.id
				AND mode.revision = proposal.mode_revision
			WHERE selection.id = NEW.selection_id
				AND selection.proposal_id = NEW.proposal_id
				AND selection.run_id = NEW.run_id
				AND selection.requested_by = NEW.requested_by
				AND selection.created_at = NEW.created_at
				AND run.status = 'paused' AND mode.phase = 'plan'
				AND NOT EXISTS (SELECT 1 FROM run_mode_snapshots later
					WHERE later.run_id = run.id AND later.revision > mode.revision)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases lease
					WHERE lease.run_id = run.id AND lease.status = 'active'
						AND julianday(lease.expires_at) > julianday('now'))
				AND (SELECT COUNT(*) FROM plan_delivery_selection_items item
					WHERE item.selection_id = selection.id) = selection.module_count
				AND NOT EXISTS (
					SELECT 1 FROM plan_delivery_modules module
					JOIN plan_delivery_selection_items current_item
						ON current_item.selection_id = selection.id
						AND current_item.module_ordinal = module.ordinal
					JOIN json_each(module.dependencies_json) dependency
					WHERE module.proposal_id = proposal.id
						AND module.direction_ordinal = selection.direction_ordinal
						AND NOT EXISTS (
							SELECT 1 FROM plan_delivery_selection_items dependency_item
							JOIN work_item_dependencies edge
								ON edge.run_id = selection.run_id
								AND edge.work_item_id = current_item.work_item_id
								AND edge.depends_on_id = dependency_item.work_item_id
							WHERE dependency_item.selection_id = selection.id
								AND dependency_item.module_ordinal = dependency.value))
				AND NOT EXISTS (
					SELECT 1 FROM plan_delivery_selection_items current_item
					JOIN work_item_dependencies edge ON edge.run_id = selection.run_id
						AND edge.work_item_id = current_item.work_item_id
					WHERE current_item.selection_id = selection.id
						AND NOT EXISTS (
							SELECT 1 FROM plan_delivery_modules module
							JOIN json_each(module.dependencies_json) dependency
							JOIN plan_delivery_selection_items dependency_item
								ON dependency_item.selection_id = selection.id
								AND dependency_item.module_ordinal = dependency.value
							WHERE module.proposal_id = proposal.id
								AND module.direction_ordinal = selection.direction_ordinal
								AND module.ordinal = current_item.module_ordinal
								AND dependency_item.work_item_id = edge.depends_on_id))
		)
		BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_proposal_update_immutable
		BEFORE UPDATE ON plan_delivery_proposals BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery proposal cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_proposal_delete_immutable
		BEFORE DELETE ON plan_delivery_proposals BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery proposal cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_direction_update_immutable
		BEFORE UPDATE ON plan_delivery_directions BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery direction cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_direction_delete_immutable
		BEFORE DELETE ON plan_delivery_directions BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery direction cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_module_update_immutable
		BEFORE UPDATE ON plan_delivery_modules BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery module cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_module_delete_immutable
		BEFORE DELETE ON plan_delivery_modules BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery module cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_proposal_operation_update_immutable
		BEFORE UPDATE ON plan_delivery_proposal_operations BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery proposal operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_proposal_operation_delete_immutable
		BEFORE DELETE ON plan_delivery_proposal_operations BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery proposal operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_update_immutable
		BEFORE UPDATE ON plan_delivery_selections BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_delete_immutable
		BEFORE DELETE ON plan_delivery_selections BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_item_update_immutable
		BEFORE UPDATE ON plan_delivery_selection_items BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection item cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_item_delete_immutable
		BEFORE DELETE ON plan_delivery_selection_items BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection item cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_operation_update_immutable
		BEFORE UPDATE ON plan_delivery_selection_operations BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_plan_delivery_selection_operation_delete_immutable
		BEFORE DELETE ON plan_delivery_selection_operations BEGIN
			SELECT RAISE(ABORT, 'Plan/Delivery selection operation cannot be deleted');
		END;`,
}

var contextProvenanceStatements = []string{
	`ALTER TABLE session_messages ADD COLUMN provenance_version TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE session_messages ADD COLUMN source_kind TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE session_messages ADD COLUMN source_ref TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE session_messages ADD COLUMN content_sha256 TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE session_messages ADD COLUMN instruction_authorized INTEGER NOT NULL DEFAULT 0;`,
	`UPDATE session_messages SET
		provenance_version = 'context_provenance.v0',
		source_kind = CASE
			WHEN role = 'assistant' AND content LIKE 'Workspace file %' THEN 'workspace_file'
			WHEN role = 'assistant' AND content LIKE 'Workspace list %' THEN 'workspace_listing'
			WHEN role = 'assistant' AND content LIKE 'File edit %' THEN 'workspace_diff'
			WHEN role = 'assistant' AND content LIKE 'Tool run %' THEN 'tool_result'
			WHEN role = 'user' THEN 'operator_message'
			WHEN role = 'assistant' THEN 'model_response'
			WHEN role = 'system' THEN 'go_control'
			ELSE 'tool_result'
		END,
		role = CASE
			WHEN role = 'assistant' AND (content LIKE 'Workspace file %'
				OR content LIKE 'Workspace list %' OR content LIKE 'File edit %'
				OR content LIKE 'Tool run %') THEN 'tool'
			ELSE role
		END,
		instruction_authorized = CASE WHEN role IN ('user', 'system') THEN 1 ELSE 0 END;`,
	`CREATE INDEX idx_session_messages_source_kind
		ON session_messages(session_id, source_kind, id);`,
	`CREATE TRIGGER trg_session_message_provenance_insert
		BEFORE INSERT ON session_messages
		WHEN NOT (
			NEW.provenance_version = 'context_provenance.v1'
			AND NEW.role IN ('user', 'assistant', 'system', 'tool')
			AND NEW.compacted IN (0, 1) AND NEW.token_estimate >= 0
			AND length(NEW.content_sha256) = 64
			AND NEW.content_sha256 = lower(NEW.content_sha256)
			AND NEW.content_sha256 NOT GLOB '*[^0-9a-f]*'
			AND NEW.source_ref = trim(NEW.source_ref)
			AND length(NEW.source_ref) <= 512 AND instr(NEW.source_ref, char(0)) = 0
			AND instr(NEW.source_ref, char(9)) = 0 AND instr(NEW.source_ref, char(10)) = 0
			AND instr(NEW.source_ref, char(13)) = 0
			AND NEW.instruction_authorized IN (0, 1)
			AND (
				(NEW.source_kind = 'operator_message' AND NEW.role = 'user'
					AND NEW.instruction_authorized = 1 AND NEW.source_ref = '')
				OR (NEW.source_kind = 'model_response' AND NEW.role = 'assistant'
					AND NEW.instruction_authorized = 0 AND NEW.source_ref = '')
				OR (NEW.source_kind = 'go_control' AND NEW.role = 'system'
					AND NEW.instruction_authorized = 1 AND NEW.source_ref = '')
				OR (NEW.source_kind IN ('workspace_file', 'workspace_listing', 'workspace_diff',
					'tool_result', 'go_command_result') AND NEW.role = 'tool'
					AND NEW.instruction_authorized = 0 AND length(NEW.source_ref) BETWEEN 1 AND 512)
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'session message context provenance is invalid');
		END;`,
	`CREATE TRIGGER trg_session_message_provenance_update_immutable
		BEFORE UPDATE ON session_messages
		WHEN NEW.id IS NOT OLD.id OR NEW.session_id IS NOT OLD.session_id
			OR NEW.role IS NOT OLD.role OR NEW.content IS NOT OLD.content
			OR NEW.token_estimate IS NOT OLD.token_estimate
			OR NEW.created_at IS NOT OLD.created_at
			OR NEW.provenance_version IS NOT OLD.provenance_version
			OR NEW.source_kind IS NOT OLD.source_kind OR NEW.source_ref IS NOT OLD.source_ref
			OR NEW.content_sha256 IS NOT OLD.content_sha256
			OR NEW.instruction_authorized IS NOT OLD.instruction_authorized
		BEGIN
			SELECT RAISE(ABORT, 'session message content and provenance are immutable');
		END;`,
	`CREATE TRIGGER trg_session_message_compaction_monotonic
		BEFORE UPDATE OF compacted ON session_messages
		WHEN NEW.compacted NOT IN (0, 1) OR NEW.compacted < OLD.compacted
		BEGIN
			SELECT RAISE(ABORT, 'session message compaction is monotonic');
		END;`,
	`CREATE TRIGGER trg_session_message_delete_immutable
		BEFORE DELETE ON session_messages BEGIN
			SELECT RAISE(ABORT, 'session messages cannot be deleted');
		END;`,
}

var deliveryCheckpointStatements = []string{
	`CREATE TABLE delivery_gate_enrollments (
		run_id TEXT PRIMARY KEY,
		selection_id TEXT NOT NULL UNIQUE,
		enrolled_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(selection_id) REFERENCES plan_delivery_selections(id) ON DELETE RESTRICT
	);`,
	`INSERT INTO delivery_gate_enrollments (run_id, selection_id, enrolled_at)
		SELECT selection.run_id, selection.id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		FROM plan_delivery_selections selection
		WHERE NOT EXISTS (
			SELECT 1 FROM plan_delivery_selection_items selected
			JOIN work_items work ON work.id = selected.work_item_id
			WHERE selected.selection_id = selection.id
				AND work.status IN ('completed', 'cancelled')
		);`,
	`CREATE TRIGGER trg_delivery_gate_enrollment_insert
		BEFORE INSERT ON delivery_gate_enrollments
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_selections selection
			WHERE selection.id = NEW.selection_id AND selection.run_id = NEW.run_id
				AND NOT EXISTS (
					SELECT 1 FROM plan_delivery_selection_items selected
					JOIN work_items work ON work.id = selected.work_item_id
					WHERE selected.selection_id = selection.id
						AND work.status IN ('completed', 'cancelled')
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery gate enrollment is invalid');
		END;`,
	`CREATE TRIGGER trg_delivery_gate_selection_enroll
		AFTER INSERT ON plan_delivery_selections BEGIN
			INSERT INTO delivery_gate_enrollments (run_id, selection_id, enrolled_at)
			VALUES (NEW.run_id, NEW.id, NEW.created_at);
		END;`,
	`CREATE TRIGGER trg_delivery_gate_enrollment_update_immutable
		BEFORE UPDATE ON delivery_gate_enrollments BEGIN
			SELECT RAISE(ABORT, 'Delivery gate enrollment cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_gate_enrollment_delete_immutable
		BEFORE DELETE ON delivery_gate_enrollments BEGIN
			SELECT RAISE(ABORT, 'Delivery gate enrollment cannot be deleted');
		END;`,
	`CREATE TABLE delivery_checkpoints (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		selection_id TEXT NOT NULL,
		proposal_id TEXT NOT NULL,
		work_item_id TEXT NOT NULL,
		direction_ordinal INTEGER NOT NULL,
		module_ordinal INTEGER NOT NULL,
		module_count INTEGER NOT NULL,
		mode_snapshot_id TEXT NOT NULL,
		mode_revision INTEGER NOT NULL,
		work_item_version INTEGER NOT NULL,
		acceptance_fingerprint TEXT NOT NULL,
		source_fingerprint TEXT NOT NULL,
		focused_verification TEXT NOT NULL,
		diff_audit TEXT NOT NULL,
		security_audit TEXT NOT NULL,
		full_gate_required INTEGER NOT NULL,
		functional_verification TEXT NOT NULL DEFAULT '',
		robustness_audit TEXT NOT NULL DEFAULT '',
		handoff_note_id TEXT NOT NULL UNIQUE,
		handoff_digest TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		version INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(selection_id) REFERENCES plan_delivery_selections(id) ON DELETE RESTRICT,
		FOREIGN KEY(proposal_id) REFERENCES plan_delivery_proposals(id) ON DELETE RESTRICT,
		FOREIGN KEY(selection_id, module_ordinal)
			REFERENCES plan_delivery_selection_items(selection_id, ordinal) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, work_item_id) REFERENCES work_items(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(mode_snapshot_id) REFERENCES run_mode_snapshots(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, mode_revision) REFERENCES run_mode_snapshots(run_id, revision) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, handoff_note_id) REFERENCES notes(run_id, id) ON DELETE RESTRICT,
		UNIQUE(work_item_id, mode_revision, work_item_version),
		CHECK(direction_ordinal BETWEEN 1 AND 3),
		CHECK(module_ordinal BETWEEN 1 AND 8),
		CHECK(module_count BETWEEN 1 AND 8 AND module_ordinal <= module_count),
		CHECK(mode_revision > 0 AND work_item_version > 0),
		CHECK(length(acceptance_fingerprint) = 64
			AND acceptance_fingerprint = lower(acceptance_fingerprint)
			AND acceptance_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(source_fingerprint) = 64
			AND source_fingerprint = lower(source_fingerprint)
			AND source_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(handoff_digest) = 64
			AND handoff_digest = lower(handoff_digest)
			AND handoff_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(focused_verification = trim(focused_verification)
			AND length(focused_verification) BETWEEN 1 AND 1024
			AND length(CAST(focused_verification AS BLOB)) <= 4096
			AND instr(focused_verification, char(0)) = 0),
		CHECK(diff_audit = trim(diff_audit) AND length(diff_audit) BETWEEN 1 AND 1024
			AND length(CAST(diff_audit AS BLOB)) <= 4096 AND instr(diff_audit, char(0)) = 0),
		CHECK(security_audit = trim(security_audit) AND length(security_audit) BETWEEN 1 AND 1024
			AND length(CAST(security_audit AS BLOB)) <= 4096 AND instr(security_audit, char(0)) = 0),
		CHECK(full_gate_required IN (0, 1)
			AND full_gate_required = CASE WHEN module_ordinal = module_count THEN 1 ELSE 0 END),
		CHECK((full_gate_required = 0 AND functional_verification = '' AND robustness_audit = '')
			OR (full_gate_required = 1
				AND functional_verification = trim(functional_verification)
				AND length(functional_verification) BETWEEN 1 AND 1024
				AND length(CAST(functional_verification AS BLOB)) <= 4096
				AND instr(functional_verification, char(0)) = 0
				AND robustness_audit = trim(robustness_audit)
				AND length(robustness_audit) BETWEEN 1 AND 1024
				AND length(CAST(robustness_audit AS BLOB)) <= 4096
				AND instr(robustness_audit, char(0)) = 0)),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK(version = 1)
	);`,
	`CREATE INDEX idx_delivery_checkpoints_run_module
		ON delivery_checkpoints(run_id, module_ordinal, created_at);`,
	`CREATE TABLE delivery_checkpoint_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		checkpoint_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		work_item_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(checkpoint_id) REFERENCES delivery_checkpoints(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, work_item_id) REFERENCES work_items(run_id, id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_delivery_checkpoint_insert
		BEFORE INSERT ON delivery_checkpoints
		WHEN NOT EXISTS (
			SELECT 1 FROM plan_delivery_selections selection
			JOIN delivery_gate_enrollments enrollment
				ON enrollment.run_id = selection.run_id AND enrollment.selection_id = selection.id
			JOIN plan_delivery_proposals proposal ON proposal.id = selection.proposal_id
			JOIN plan_delivery_selection_items selected
				ON selected.selection_id = selection.id AND selected.ordinal = NEW.module_ordinal
			JOIN work_items work ON work.run_id = selection.run_id
				AND work.id = selected.work_item_id
			JOIN runs run ON run.id = selection.run_id
			JOIN run_mode_snapshots mode ON mode.id = NEW.mode_snapshot_id
				AND mode.run_id = run.id AND mode.revision = NEW.mode_revision
			WHERE selection.id = NEW.selection_id AND selection.run_id = NEW.run_id
				AND selection.proposal_id = NEW.proposal_id
				AND selection.direction_ordinal = NEW.direction_ordinal
				AND selection.module_count = NEW.module_count
				AND selected.module_ordinal = NEW.module_ordinal
				AND selected.work_item_id = NEW.work_item_id
				AND proposal.run_id = NEW.run_id
				AND work.status = 'in_progress' AND work.version = NEW.work_item_version
				AND run.status = 'paused' AND mode.phase = 'deliver'
				AND NOT EXISTS (SELECT 1 FROM run_mode_snapshots later
					WHERE later.run_id = run.id AND later.revision > mode.revision)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases lease
					WHERE lease.run_id = run.id AND lease.status = 'active'
						AND julianday(lease.expires_at) > julianday('now'))
				AND julianday(NEW.created_at) >= julianday(mode.created_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery checkpoint binding is invalid');
		END;`,
	`CREATE TRIGGER trg_delivery_checkpoint_operation_insert
		BEFORE INSERT ON delivery_checkpoint_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.id = NEW.checkpoint_id AND checkpoint.run_id = NEW.run_id
				AND checkpoint.work_item_id = NEW.work_item_id
				AND checkpoint.requested_by = NEW.requested_by
				AND checkpoint.created_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery checkpoint operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_delivery_checkpoint_update_immutable
		BEFORE UPDATE ON delivery_checkpoints BEGIN
			SELECT RAISE(ABORT, 'Delivery checkpoint cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_checkpoint_delete_immutable
		BEFORE DELETE ON delivery_checkpoints BEGIN
			SELECT RAISE(ABORT, 'Delivery checkpoint cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_delivery_checkpoint_operation_update_immutable
		BEFORE UPDATE ON delivery_checkpoint_operations BEGIN
			SELECT RAISE(ABORT, 'Delivery checkpoint operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_checkpoint_operation_delete_immutable
		BEFORE DELETE ON delivery_checkpoint_operations BEGIN
			SELECT RAISE(ABORT, 'Delivery checkpoint operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_update_immutable
		BEFORE UPDATE ON notes
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = OLD.id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_delete_immutable
		BEFORE DELETE ON notes
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = OLD.id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_tag_insert_immutable
		BEFORE INSERT ON note_tags
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = NEW.note_id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note tags cannot be inserted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_tag_update_immutable
		BEFORE UPDATE ON note_tags
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id IN (OLD.note_id, NEW.note_id))
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note tags cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_tag_delete_immutable
		BEFORE DELETE ON note_tags
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = OLD.note_id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note tags cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_source_insert_immutable
		BEFORE INSERT ON note_sources
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = NEW.note_id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note sources cannot be inserted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_source_update_immutable
		BEFORE UPDATE ON note_sources
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id IN (OLD.note_id, NEW.note_id))
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note sources cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_source_delete_immutable
		BEFORE DELETE ON note_sources
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = OLD.note_id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note sources cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_evidence_insert_immutable
		BEFORE INSERT ON note_evidence
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = NEW.note_id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note evidence cannot be inserted');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_evidence_update_immutable
		BEFORE UPDATE ON note_evidence
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id IN (OLD.note_id, NEW.note_id))
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note evidence cannot be updated');
		END;`,
	`CREATE TRIGGER trg_delivery_handoff_note_evidence_delete_immutable
		BEFORE DELETE ON note_evidence
		WHEN EXISTS (SELECT 1 FROM delivery_checkpoints checkpoint
			WHERE checkpoint.handoff_note_id = OLD.note_id)
		BEGIN
			SELECT RAISE(ABORT, 'Delivery handoff Note evidence cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_delivery_work_item_completion_guard
		BEFORE UPDATE OF status ON work_items
		WHEN NEW.status = 'completed' AND OLD.status != 'completed'
			AND EXISTS (SELECT 1 FROM plan_delivery_selection_items selected
				JOIN plan_delivery_selections selection ON selection.id = selected.selection_id
				JOIN delivery_gate_enrollments enrollment
					ON enrollment.run_id = selection.run_id AND enrollment.selection_id = selection.id
				WHERE selected.work_item_id = OLD.id)
			AND NOT EXISTS (
				SELECT 1 FROM delivery_checkpoints checkpoint
				JOIN delivery_checkpoint_operations operation
					ON operation.checkpoint_id = checkpoint.id
				JOIN run_mode_snapshots mode ON mode.id = checkpoint.mode_snapshot_id
				WHERE checkpoint.run_id = OLD.run_id AND checkpoint.work_item_id = OLD.id
					AND checkpoint.work_item_version = OLD.version
					AND mode.run_id = OLD.run_id AND mode.revision = checkpoint.mode_revision
					AND mode.phase = 'deliver'
					AND NOT EXISTS (SELECT 1 FROM run_mode_snapshots later
						WHERE later.run_id = OLD.run_id AND later.revision > mode.revision)
			)
		BEGIN
			SELECT RAISE(ABORT, 'selected WorkItem requires a current Delivery checkpoint');
		END;`,
	`CREATE TRIGGER trg_delivery_run_completion_guard
		BEFORE UPDATE OF status ON runs
		WHEN NEW.status = 'completed' AND OLD.status != 'completed'
			AND EXISTS (SELECT 1 FROM delivery_gate_enrollments enrollment
				WHERE enrollment.run_id = NEW.id)
			AND EXISTS (
				SELECT 1 FROM plan_delivery_selection_items selected
				JOIN plan_delivery_selections selection ON selection.id = selected.selection_id
				JOIN work_items work ON work.id = selected.work_item_id
				WHERE selection.run_id = NEW.id AND (
					work.status != 'completed' OR NOT EXISTS (
						SELECT 1 FROM delivery_checkpoints checkpoint
						JOIN delivery_checkpoint_operations operation
							ON operation.checkpoint_id = checkpoint.id
						JOIN run_mode_snapshots mode ON mode.id = checkpoint.mode_snapshot_id
						WHERE checkpoint.run_id = NEW.id
							AND checkpoint.selection_id = selection.id
							AND checkpoint.work_item_id = work.id
							AND checkpoint.work_item_version = work.version - 1
							AND mode.run_id = NEW.id
							AND mode.revision = checkpoint.mode_revision
							AND mode.phase = 'deliver'
					)
				)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Run has incomplete Delivery checkpoint gates');
		END;`,
}

var operatorSteeringStatements = []string{
	`CREATE TABLE operator_steering_messages (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		status TEXT NOT NULL,
		content TEXT NOT NULL,
		content_sha256 TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		session_message_id INTEGER UNIQUE,
		created_at TEXT NOT NULL,
		committed_at TEXT,
		cancelled_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE RESTRICT,
		FOREIGN KEY(session_message_id) REFERENCES session_messages(id) ON DELETE RESTRICT,
		UNIQUE(run_id, sequence),
		CHECK(sequence > 0),
		CHECK(status IN ('pending', 'committed', 'cancelled')),
		CHECK(length(CAST(content AS BLOB)) BETWEEN 1 AND 16384
			AND content = trim(content) AND instr(content, char(0)) = 0),
		CHECK(length(content_sha256) = 64 AND content_sha256 = lower(content_sha256)
			AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK((status = 'pending' AND session_message_id IS NULL
				AND committed_at IS NULL AND cancelled_at IS NULL)
			OR (status = 'committed' AND session_message_id IS NOT NULL
				AND committed_at IS NOT NULL AND cancelled_at IS NULL)
			OR (status = 'cancelled' AND session_message_id IS NULL
				AND committed_at IS NULL AND cancelled_at IS NOT NULL))
	);`,
	`CREATE INDEX idx_operator_steering_run_status_sequence
		ON operator_steering_messages(run_id, status, sequence);`,
	`CREATE TABLE operator_steering_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		message_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(message_id) REFERENCES operator_steering_messages(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TABLE operator_steering_deliveries (
		id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL UNIQUE,
		turn INTEGER NOT NULL,
		status TEXT NOT NULL,
		prepared_at TEXT NOT NULL,
		terminal_at TEXT,
		FOREIGN KEY(message_id) REFERENCES operator_steering_messages(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(turn > 0),
		CHECK(status IN ('prepared', 'committed', 'superseded', 'cancelled')),
		CHECK((status = 'prepared' AND terminal_at IS NULL)
			OR (status != 'prepared' AND terminal_at IS NOT NULL))
	);`,
	`CREATE INDEX idx_operator_steering_deliveries_run_turn
		ON operator_steering_deliveries(run_id, turn, prepared_at);`,
	`CREATE UNIQUE INDEX idx_operator_steering_one_prepared
		ON operator_steering_deliveries(message_id) WHERE status = 'prepared';`,
	`CREATE UNIQUE INDEX idx_operator_steering_one_committed
		ON operator_steering_deliveries(message_id) WHERE status = 'committed';`,
	`CREATE TRIGGER trg_operator_steering_insert_binding
		BEFORE INSERT ON operator_steering_messages
		WHEN NOT EXISTS (SELECT 1 FROM runs run
			WHERE run.id = NEW.run_id AND run.session_id = NEW.session_id
				AND run.status IN ('running', 'paused'))
		BEGIN
			SELECT RAISE(ABORT, 'operator steering Run binding is invalid');
		END;`,
	`CREATE TRIGGER trg_operator_steering_update_monotonic
		BEFORE UPDATE ON operator_steering_messages
		WHEN NEW.id IS NOT OLD.id OR NEW.run_id IS NOT OLD.run_id
			OR NEW.session_id IS NOT OLD.session_id OR NEW.sequence IS NOT OLD.sequence
			OR NEW.content IS NOT OLD.content OR NEW.content_sha256 IS NOT OLD.content_sha256
			OR NEW.requested_by IS NOT OLD.requested_by OR NEW.created_at IS NOT OLD.created_at
			OR OLD.status != 'pending' OR NEW.status NOT IN ('committed', 'cancelled')
		BEGIN
			SELECT RAISE(ABORT, 'operator steering content is immutable and status is monotonic');
		END;`,
	`CREATE TRIGGER trg_operator_steering_commit_binding
		BEFORE UPDATE ON operator_steering_messages
		WHEN NEW.status = 'committed' AND NOT EXISTS (
			SELECT 1 FROM session_messages message
			WHERE message.id = NEW.session_message_id AND message.session_id = NEW.session_id
				AND message.role = 'user' AND message.content = NEW.content
				AND message.content_sha256 = NEW.content_sha256
				AND message.source_kind = 'operator_message'
				AND message.instruction_authorized = 1)
		BEGIN
			SELECT RAISE(ABORT, 'operator steering Session message binding is invalid');
		END;`,
	`CREATE TRIGGER trg_operator_steering_delete_immutable
		BEFORE DELETE ON operator_steering_messages BEGIN
			SELECT RAISE(ABORT, 'operator steering messages cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_operator_steering_operation_insert
		BEFORE INSERT ON operator_steering_operations
		WHEN NOT EXISTS (SELECT 1 FROM operator_steering_messages message
			WHERE message.id = NEW.message_id AND message.run_id = NEW.run_id
				AND message.requested_by = NEW.requested_by
				AND message.created_at = NEW.created_at)
		BEGIN
			SELECT RAISE(ABORT, 'operator steering operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_operator_steering_operation_update_immutable
		BEFORE UPDATE ON operator_steering_operations BEGIN
			SELECT RAISE(ABORT, 'operator steering operations cannot be updated');
		END;`,
	`CREATE TRIGGER trg_operator_steering_operation_delete_immutable
		BEFORE DELETE ON operator_steering_operations BEGIN
			SELECT RAISE(ABORT, 'operator steering operations cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_operator_steering_delivery_insert
		BEFORE INSERT ON operator_steering_deliveries
		WHEN NOT EXISTS (SELECT 1 FROM operator_steering_messages message
			JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id = message.run_id
			WHERE message.id = NEW.message_id AND message.run_id = NEW.run_id
				AND message.status = 'pending' AND message.content = checkpoint.pending_input
				AND checkpoint.phase = 'turn_started' AND checkpoint.attempt_id = NEW.attempt_id
				AND checkpoint.next_turn = NEW.turn)
		BEGIN
			SELECT RAISE(ABORT, 'operator steering delivery binding is invalid');
		END;`,
	`CREATE TRIGGER trg_operator_steering_delivery_update_monotonic
		BEFORE UPDATE ON operator_steering_deliveries
		WHEN NEW.id IS NOT OLD.id OR NEW.message_id IS NOT OLD.message_id
			OR NEW.run_id IS NOT OLD.run_id OR NEW.attempt_id IS NOT OLD.attempt_id
			OR NEW.turn IS NOT OLD.turn OR NEW.prepared_at IS NOT OLD.prepared_at
			OR OLD.status != 'prepared'
			OR NEW.status NOT IN ('committed', 'superseded', 'cancelled')
			OR NEW.terminal_at IS NULL
		BEGIN
			SELECT RAISE(ABORT, 'operator steering delivery identity is immutable and status is monotonic');
		END;`,
	`CREATE TRIGGER trg_operator_steering_delivery_delete_immutable
		BEFORE DELETE ON operator_steering_deliveries BEGIN
			SELECT RAISE(ABORT, 'operator steering deliveries cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_operator_steering_run_completion_guard
		BEFORE UPDATE OF status ON runs
		WHEN NEW.status = 'completed' AND OLD.status != 'completed'
			AND EXISTS (SELECT 1 FROM operator_steering_messages message
				WHERE message.run_id = NEW.id AND message.status = 'pending')
		BEGIN
			SELECT RAISE(ABORT, 'Run has pending operator steering');
		END;`,
}

var operatorSteeringControlStatements = []string{
	`CREATE TABLE operator_steering_cancellations (
		id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		reason TEXT NOT NULL,
		reason_sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(message_id) REFERENCES operator_steering_messages(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(kind IN ('operator', 'run_terminal')),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0),
		CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 2048
			AND reason = trim(reason) AND instr(reason, char(0)) = 0),
		CHECK(length(reason_sha256) = 64 AND reason_sha256 = lower(reason_sha256)
			AND reason_sha256 NOT GLOB '*[^0-9a-f]*')
	);`,
	`CREATE INDEX idx_operator_steering_cancellations_run_created
		ON operator_steering_cancellations(run_id, created_at);`,
	`CREATE TABLE operator_steering_cancellation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		cancellation_id TEXT NOT NULL UNIQUE,
		message_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(cancellation_id) REFERENCES operator_steering_cancellations(id) ON DELETE RESTRICT,
		FOREIGN KEY(message_id) REFERENCES operator_steering_messages(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_operator_steering_cancellation_insert
		BEFORE INSERT ON operator_steering_cancellations
		WHEN NOT EXISTS (
			SELECT 1 FROM operator_steering_messages message
			JOIN runs run ON run.id = message.run_id
			WHERE message.id = NEW.message_id AND message.run_id = NEW.run_id
				AND message.status = 'pending'
				AND ((NEW.kind = 'operator' AND run.status IN ('running', 'paused')
					AND NOT EXISTS (SELECT 1 FROM operator_steering_deliveries delivery
						WHERE delivery.message_id = message.id AND delivery.status = 'prepared'))
					OR (NEW.kind = 'run_terminal' AND run.status IN ('failed', 'cancelled')))
		)
		BEGIN
			SELECT RAISE(ABORT, 'operator steering cancellation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_operator_steering_cancellation_update_immutable
		BEFORE UPDATE ON operator_steering_cancellations BEGIN
			SELECT RAISE(ABORT, 'operator steering cancellations cannot be updated');
		END;`,
	`CREATE TRIGGER trg_operator_steering_cancellation_delete_immutable
		BEFORE DELETE ON operator_steering_cancellations BEGIN
			SELECT RAISE(ABORT, 'operator steering cancellations cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_operator_steering_cancellation_operation_insert
		BEFORE INSERT ON operator_steering_cancellation_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM operator_steering_cancellations cancellation
			WHERE cancellation.id = NEW.cancellation_id
				AND cancellation.message_id = NEW.message_id
				AND cancellation.run_id = NEW.run_id
				AND cancellation.kind = 'operator'
				AND cancellation.requested_by = NEW.requested_by
				AND cancellation.created_at = NEW.created_at)
		BEGIN
			SELECT RAISE(ABORT, 'operator steering cancellation operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_operator_steering_cancellation_operation_update_immutable
		BEFORE UPDATE ON operator_steering_cancellation_operations BEGIN
			SELECT RAISE(ABORT, 'operator steering cancellation operations cannot be updated');
		END;`,
	`CREATE TRIGGER trg_operator_steering_cancellation_operation_delete_immutable
		BEFORE DELETE ON operator_steering_cancellation_operations BEGIN
			SELECT RAISE(ABORT, 'operator steering cancellation operations cannot be deleted');
		END;`,
	`DROP TRIGGER trg_operator_steering_update_monotonic;`,
	`CREATE TRIGGER trg_operator_steering_update_monotonic
		BEFORE UPDATE ON operator_steering_messages
		WHEN NEW.id IS NOT OLD.id OR NEW.run_id IS NOT OLD.run_id
			OR NEW.session_id IS NOT OLD.session_id OR NEW.sequence IS NOT OLD.sequence
			OR NEW.content IS NOT OLD.content OR NEW.content_sha256 IS NOT OLD.content_sha256
			OR NEW.requested_by IS NOT OLD.requested_by OR NEW.created_at IS NOT OLD.created_at
			OR OLD.status != 'pending' OR NEW.status NOT IN ('committed', 'cancelled')
			OR (NEW.status = 'cancelled' AND NOT EXISTS (
				SELECT 1 FROM operator_steering_cancellations cancellation
				WHERE cancellation.message_id = OLD.id AND cancellation.run_id = OLD.run_id
					AND cancellation.created_at = NEW.cancelled_at
					AND (cancellation.kind = 'run_terminal' OR EXISTS (
						SELECT 1 FROM operator_steering_cancellation_operations operation
						WHERE operation.cancellation_id = cancellation.id
							AND operation.message_id = OLD.id AND operation.run_id = OLD.run_id))))
		BEGIN
			SELECT RAISE(ABORT, 'operator steering content is immutable and status is monotonic');
		END;`,
}

var specialistSkillContextStatements = []string{
	`CREATE TABLE specialist_skill_context_preparations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		parent_agent_id TEXT NOT NULL,
		agent_attempt_id TEXT NOT NULL UNIQUE,
		turn_number INTEGER NOT NULL,
		parent_selection_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		parent_selection_fingerprint TEXT NOT NULL,
		mode_snapshot_id TEXT NOT NULL,
		mode_revision INTEGER NOT NULL,
		surface TEXT NOT NULL,
		profile TEXT NOT NULL,
		assignment_fingerprint TEXT NOT NULL,
		context_fingerprint TEXT NOT NULL,
		item_count INTEGER NOT NULL,
		token_budget INTEGER NOT NULL,
		token_upper_bound INTEGER NOT NULL,
		redaction_count INTEGER NOT NULL,
		prepared_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id, parent_agent_id) REFERENCES agent_nodes(run_id, id) ON DELETE RESTRICT,
		FOREIGN KEY(agent_attempt_id) REFERENCES agent_attempts(id) ON DELETE RESTRICT,
		FOREIGN KEY(parent_selection_id) REFERENCES run_skill_selections(id) ON DELETE RESTRICT,
		FOREIGN KEY(mode_snapshot_id) REFERENCES run_mode_snapshots(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'specialist_skill_context.v1'),
		CHECK(surface IN ('code', 'cyber')),
		CHECK(profile IN ('code', 'review', 'learn', 'script')),
		CHECK(turn_number > 0 AND mode_revision > 0),
		CHECK(item_count BETWEEN 0 AND 1),
		CHECK(token_budget BETWEEN 1 AND 2048),
		CHECK(token_upper_bound BETWEEN 0 AND token_budget),
		CHECK(redaction_count BETWEEN 0 AND token_budget),
		CHECK((item_count = 0 AND token_upper_bound = 0 AND redaction_count = 0)
			OR (item_count = 1 AND token_upper_bound > 0)),
		CHECK(length(parent_selection_fingerprint) = 64
			AND parent_selection_fingerprint = lower(parent_selection_fingerprint)
			AND parent_selection_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(assignment_fingerprint) = 64
			AND assignment_fingerprint = lower(assignment_fingerprint)
			AND assignment_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(context_fingerprint) = 64
			AND context_fingerprint = lower(context_fingerprint)
			AND context_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256
			AND instr(mission_id, char(0)) = 0),
		CHECK(agent_id = trim(agent_id) AND length(agent_id) BETWEEN 1 AND 256
			AND instr(agent_id, char(0)) = 0),
		CHECK(parent_agent_id = trim(parent_agent_id) AND length(parent_agent_id) BETWEEN 1 AND 256
			AND instr(parent_agent_id, char(0)) = 0),
		CHECK(agent_attempt_id = trim(agent_attempt_id)
			AND length(agent_attempt_id) BETWEEN 1 AND 256 AND instr(agent_attempt_id, char(0)) = 0),
		CHECK(parent_selection_id = trim(parent_selection_id)
			AND length(parent_selection_id) BETWEEN 1 AND 256 AND instr(parent_selection_id, char(0)) = 0),
		CHECK(mode_snapshot_id = trim(mode_snapshot_id)
			AND length(mode_snapshot_id) BETWEEN 1 AND 256 AND instr(mode_snapshot_id, char(0)) = 0)
	);`,
	`CREATE INDEX idx_specialist_skill_context_run_agent_turn
		ON specialist_skill_context_preparations(run_id, agent_id, turn_number, prepared_at);`,
	`CREATE TABLE specialist_skill_context_commits (
		preparation_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		agent_attempt_id TEXT NOT NULL UNIQUE,
		model_attempt INTEGER NOT NULL,
		committed_at TEXT NOT NULL,
		FOREIGN KEY(preparation_id) REFERENCES specialist_skill_context_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(agent_attempt_id) REFERENCES agent_attempts(id) ON DELETE RESTRICT,
		CHECK(model_attempt > 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(agent_attempt_id = trim(agent_attempt_id)
			AND length(agent_attempt_id) BETWEEN 1 AND 256 AND instr(agent_attempt_id, char(0)) = 0)
	);`,
	`CREATE TRIGGER trg_specialist_skill_context_preparation_insert
		BEFORE INSERT ON specialist_skill_context_preparations
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN agent_attempts attempt ON attempt.id = NEW.agent_attempt_id
			JOIN agent_nodes child ON child.run_id = run.id AND child.id = NEW.agent_id
			JOIN agent_nodes parent ON parent.run_id = run.id AND parent.id = NEW.parent_agent_id
			JOIN run_skill_selections selection ON selection.id = NEW.parent_selection_id
			JOIN run_mode_snapshots mode ON mode.id = NEW.mode_snapshot_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND run.status = 'running' AND mission.profile = NEW.profile
				AND attempt.run_id = run.id AND attempt.agent_id = child.id
				AND attempt.parent_agent_id = parent.id AND attempt.status = 'running'
				AND attempt.turn_number = NEW.turn_number
				AND child.role = 'specialist' AND child.status = 'running'
				AND child.parent_id = parent.id AND child.active_attempt_id = attempt.id
				AND child.profile = NEW.profile AND parent.role = 'root'
				AND EXISTS (SELECT 1 FROM json_each(child.skills_json) skill
					WHERE skill.type = 'text' AND skill.value = 'model.chat')
				AND parent.status IN ('ready', 'running', 'waiting')
				AND selection.run_id = run.id AND selection.mission_id = mission.id
				AND selection.profile = NEW.profile
				AND selection.selection_fingerprint = NEW.parent_selection_fingerprint
				AND NEW.item_count <= selection.item_count
				AND NEW.token_upper_bound <= selection.token_upper_bound
				AND mode.run_id = run.id AND mode.mission_id = mission.id
				AND mode.revision = NEW.mode_revision AND mode.surface = NEW.surface
				AND mode.profile = NEW.profile
				AND NOT EXISTS (SELECT 1 FROM run_mode_snapshots later
					WHERE later.run_id = run.id AND later.revision > mode.revision)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist Skill context preparation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_skill_context_commit_insert
		BEFORE INSERT ON specialist_skill_context_commits
		WHEN NOT EXISTS (
			SELECT 1 FROM specialist_skill_context_preparations preparation
			JOIN agent_attempts attempt ON attempt.id = preparation.agent_attempt_id
			JOIN specialist_model_calls model_call
				ON model_call.agent_attempt_id = attempt.id
				AND model_call.model_attempt_number = NEW.model_attempt
			WHERE preparation.id = NEW.preparation_id
				AND preparation.run_id = NEW.run_id
				AND preparation.agent_attempt_id = NEW.agent_attempt_id
				AND attempt.status = 'running' AND model_call.status = 'started'
				AND julianday(NEW.committed_at) >= julianday(preparation.prepared_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'Specialist Skill context commit binding is invalid');
		END;`,
	`CREATE TRIGGER trg_specialist_skill_context_preparation_update_immutable
		BEFORE UPDATE ON specialist_skill_context_preparations BEGIN
			SELECT RAISE(ABORT, 'Specialist Skill context preparation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_specialist_skill_context_preparation_delete_immutable
		BEFORE DELETE ON specialist_skill_context_preparations BEGIN
			SELECT RAISE(ABORT, 'Specialist Skill context preparation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_specialist_skill_context_commit_update_immutable
		BEFORE UPDATE ON specialist_skill_context_commits BEGIN
			SELECT RAISE(ABORT, 'Specialist Skill context commit cannot be updated');
		END;`,
	`CREATE TRIGGER trg_specialist_skill_context_commit_delete_immutable
		BEFORE DELETE ON specialist_skill_context_commits BEGIN
			SELECT RAISE(ABORT, 'Specialist Skill context commit cannot be deleted');
		END;`,
}

var sandboxManifestStatements = []string{
	`CREATE TABLE sandbox_manifest_preparations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		cancellation_id TEXT NOT NULL UNIQUE,
		protocol_version TEXT NOT NULL,
		backend TEXT NOT NULL,
		manifest_fingerprint TEXT NOT NULL,
		authorization_fingerprint TEXT NOT NULL,
		workspace_fingerprint TEXT NOT NULL,
		scope_fingerprint TEXT NOT NULL,
		command_argument_count INTEGER NOT NULL,
		mount_count INTEGER NOT NULL,
		writable_mount_count INTEGER NOT NULL,
		environment_count INTEGER NOT NULL,
		secret_reference_count INTEGER NOT NULL,
		network_mode TEXT NOT NULL,
		allowed_target_count INTEGER NOT NULL,
		input_artifact_count INTEGER NOT NULL,
		output_count INTEGER NOT NULL,
		timeout_seconds INTEGER NOT NULL,
		grace_period_millis INTEGER NOT NULL,
		cpu_quota_millis INTEGER NOT NULL,
		memory_bytes INTEGER NOT NULL,
		pids INTEGER NOT NULL,
		max_output_bytes INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		prepared_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_manifest.v1'),
		CHECK(backend IN ('noop', 'local', 'docker')),
		CHECK(command_argument_count BETWEEN 0 AND 128),
		CHECK(mount_count BETWEEN 1 AND 32),
		CHECK(writable_mount_count BETWEEN 0 AND mount_count),
		CHECK(environment_count BETWEEN 0 AND 64),
		CHECK(secret_reference_count BETWEEN 0 AND environment_count),
		CHECK(network_mode IN ('disabled', 'allowlist')),
		CHECK((network_mode = 'disabled' AND allowed_target_count = 0)
			OR (network_mode = 'allowlist' AND allowed_target_count BETWEEN 1 AND 32)),
		CHECK(input_artifact_count BETWEEN 0 AND 16),
		CHECK(output_count BETWEEN 1 AND 18),
		CHECK(timeout_seconds BETWEEN 1 AND 3600),
		CHECK(grace_period_millis BETWEEN 0 AND 30000),
		CHECK(cpu_quota_millis BETWEEN 1 AND 8000),
		CHECK(memory_bytes BETWEEN 16777216 AND 8589934592),
		CHECK(pids BETWEEN 1 AND 512),
		CHECK(max_output_bytes BETWEEN 1 AND 16777216),
		CHECK(length(manifest_fingerprint) = 64 AND manifest_fingerprint = lower(manifest_fingerprint)
			AND manifest_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(authorization_fingerprint) = 64 AND authorization_fingerprint = lower(authorization_fingerprint)
			AND authorization_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(workspace_fingerprint) = 64 AND workspace_fingerprint = lower(workspace_fingerprint)
			AND workspace_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(scope_fingerprint) = 64 AND scope_fingerprint = lower(scope_fingerprint)
			AND scope_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256 AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256 AND instr(workspace_id, char(0)) = 0),
		CHECK(cancellation_id = trim(cancellation_id) AND length(cancellation_id) BETWEEN 1 AND 256
			AND instr(cancellation_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_manifest_preparations_run_prepared
		ON sandbox_manifest_preparations(run_id, prepared_at, id);`,
	`CREATE TABLE sandbox_manifest_validations (
		preparation_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		policy_allowed INTEGER NOT NULL,
		needs_approval INTEGER NOT NULL,
		risk TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		approval_id TEXT NOT NULL DEFAULT '',
		approval_status TEXT NOT NULL,
		validator_name TEXT NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		validated_at TEXT NOT NULL,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_validation.v1'),
		CHECK(policy_allowed IN (0, 1) AND needs_approval IN (0, 1)),
		CHECK(risk IN ('low', 'medium', 'high', 'critical')),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(approval_status IN ('not_applicable', 'not_required', 'required', 'pending', 'approved', 'denied')),
		CHECK((policy_allowed = 0 AND needs_approval = 0 AND approval_id = '' AND approval_status = 'not_applicable')
			OR (policy_allowed = 1 AND needs_approval = 0 AND approval_id = '' AND approval_status = 'not_required')
			OR (policy_allowed = 1 AND needs_approval = 1 AND approval_id = '' AND approval_status = 'required')
			OR (policy_allowed = 1 AND needs_approval = 1 AND length(approval_id) BETWEEN 1 AND 256
				AND approval_status IN ('pending', 'approved', 'denied'))),
		CHECK(validator_name = 'noop'),
		CHECK(backend_enabled = 0 AND execution_authorized = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(approval_id = trim(approval_id) AND instr(approval_id, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_manifest_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		preparation_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_sandbox_manifest_preparation_insert
		BEFORE INSERT ON sandbox_manifest_preparations
		WHEN NOT EXISTS (
			SELECT 1 FROM runs run
			JOIN missions mission ON mission.id = run.mission_id
			JOIN workspaces workspace ON workspace.id = NEW.workspace_id
			WHERE run.id = NEW.run_id AND run.mission_id = NEW.mission_id
				AND mission.id = NEW.mission_id AND mission.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND julianday(NEW.prepared_at) >= julianday(run.created_at)
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest preparation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_validation_insert
		BEFORE INSERT ON sandbox_manifest_validations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_manifest_preparations preparation
			JOIN runs run ON run.id = preparation.run_id
			WHERE preparation.id = NEW.preparation_id AND preparation.run_id = NEW.run_id
				AND julianday(NEW.validated_at) >= julianday(preparation.prepared_at)
				AND (NEW.policy_allowed = 0 OR NEW.needs_approval = 1
					OR (preparation.backend = 'noop' AND preparation.network_mode = 'disabled'
						AND preparation.writable_mount_count = 0
						AND preparation.secret_reference_count = 0))
				AND (NEW.approval_id = '' OR EXISTS (
					SELECT 1 FROM tool_approvals approval
					WHERE approval.id = NEW.approval_id AND approval.run_id = preparation.run_id
						AND approval.session_id = run.session_id
						AND approval.workspace_id = preparation.workspace_id
						AND approval.tool_name = 'sandbox.manifest'
						AND approval.action_class = 'sandbox_execute'
						AND approval.request_fingerprint = preparation.authorization_fingerprint
						AND approval.status = NEW.approval_status
				))
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest validation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_operation_insert
		BEFORE INSERT ON sandbox_manifest_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_manifest_preparations preparation
			JOIN sandbox_manifest_validations validation ON validation.preparation_id = preparation.id
			WHERE preparation.id = NEW.preparation_id AND preparation.run_id = NEW.run_id
				AND preparation.requested_by = NEW.requested_by
				AND preparation.prepared_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_preparation_update_immutable
		BEFORE UPDATE ON sandbox_manifest_preparations BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest preparation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_preparation_delete_immutable
		BEFORE DELETE ON sandbox_manifest_preparations BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest preparation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_validation_update_immutable
		BEFORE UPDATE ON sandbox_manifest_validations BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest validation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_validation_delete_immutable
		BEFORE DELETE ON sandbox_manifest_validations BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest validation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_operation_update_immutable
		BEFORE UPDATE ON sandbox_manifest_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_manifest_operation_delete_immutable
		BEFORE DELETE ON sandbox_manifest_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox manifest operation cannot be deleted');
		END;`,
}

var sandboxExecutionCandidateStatements = []string{
	`CREATE TABLE sandbox_execution_candidates (
		id TEXT PRIMARY KEY,
		preparation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		manifest_fingerprint TEXT NOT NULL,
		authorization_fingerprint TEXT NOT NULL,
		workspace_fingerprint TEXT NOT NULL,
		scope_fingerprint TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		mount_binding_fingerprint TEXT NOT NULL,
		approval_id TEXT NOT NULL DEFAULT '',
		approval_status TEXT NOT NULL,
		mount_count INTEGER NOT NULL,
		regular_file_mount_count INTEGER NOT NULL,
		directory_mount_count INTEGER NOT NULL,
		tokens_used INTEGER NOT NULL,
		execution_millis_used INTEGER NOT NULL,
		tool_calls_used INTEGER NOT NULL,
		budget_checked INTEGER NOT NULL,
		lease_quiescent INTEGER NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		validated_at TEXT NOT NULL,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_execution_candidate.v1'),
		CHECK(approval_status IN ('not_required', 'approved')),
		CHECK((approval_id = '' AND approval_status = 'not_required') OR
			(length(approval_id) BETWEEN 1 AND 256 AND approval_status = 'approved')),
		CHECK(mount_count BETWEEN 1 AND 32),
		CHECK(regular_file_mount_count >= 0 AND directory_mount_count >= 0 AND
			regular_file_mount_count + directory_mount_count = mount_count),
		CHECK(tokens_used >= 0 AND execution_millis_used >= 0 AND tool_calls_used >= 0),
		CHECK(budget_checked = 1 AND lease_quiescent = 1),
		CHECK(backend_enabled = 0 AND execution_authorized = 0),
		CHECK(length(manifest_fingerprint) = 64 AND manifest_fingerprint = lower(manifest_fingerprint)
			AND manifest_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(authorization_fingerprint) = 64 AND authorization_fingerprint = lower(authorization_fingerprint)
			AND authorization_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(workspace_fingerprint) = 64 AND workspace_fingerprint = lower(workspace_fingerprint)
			AND workspace_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(scope_fingerprint) = 64 AND scope_fingerprint = lower(scope_fingerprint)
			AND scope_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mount_binding_fingerprint) = 64 AND mount_binding_fingerprint = lower(mount_binding_fingerprint)
			AND mount_binding_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(preparation_id = trim(preparation_id) AND length(preparation_id) BETWEEN 1 AND 256
			AND instr(preparation_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256 AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256
			AND instr(workspace_id, char(0)) = 0),
		CHECK(approval_id = trim(approval_id) AND instr(approval_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_execution_candidates_run_validated
		ON sandbox_execution_candidates(run_id, validated_at, id);`,
	`CREATE TABLE sandbox_execution_candidate_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		candidate_id TEXT NOT NULL UNIQUE,
		preparation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(candidate_id) REFERENCES sandbox_execution_candidates(id) ON DELETE RESTRICT,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_sandbox_execution_candidate_insert
		BEFORE INSERT ON sandbox_execution_candidates
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_manifest_preparations preparation
			JOIN sandbox_manifest_validations validation ON validation.preparation_id = preparation.id
			JOIN runs run ON run.id = preparation.run_id
			JOIN missions mission ON mission.id = preparation.mission_id
			WHERE preparation.id = NEW.preparation_id AND preparation.run_id = NEW.run_id
				AND preparation.mission_id = NEW.mission_id AND preparation.workspace_id = NEW.workspace_id
				AND run.mission_id = NEW.mission_id AND mission.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND validation.policy_allowed = 1
				AND preparation.manifest_fingerprint = NEW.manifest_fingerprint
				AND preparation.authorization_fingerprint = NEW.authorization_fingerprint
				AND preparation.workspace_fingerprint = NEW.workspace_fingerprint
				AND preparation.scope_fingerprint = NEW.scope_fingerprint
				AND preparation.mount_count = NEW.mount_count
				AND validation.policy_fingerprint = NEW.policy_fingerprint
				AND julianday(NEW.validated_at) >= julianday(preparation.prepared_at)
				AND ((validation.needs_approval = 0 AND validation.approval_id = ''
						AND validation.approval_status = 'not_required' AND NEW.approval_id = ''
						AND NEW.approval_status = 'not_required')
					OR (validation.needs_approval = 1 AND validation.approval_id = ''
						AND validation.approval_status = 'required' AND NEW.approval_status = 'approved'
						AND EXISTS (SELECT 1 FROM tool_approvals approval
							WHERE approval.id = NEW.approval_id AND approval.proposal_id = NEW.preparation_id
								AND approval.run_id = NEW.run_id AND approval.session_id = run.session_id
								AND approval.workspace_id = NEW.workspace_id
								AND approval.tool_name = 'sandbox.manifest'
								AND approval.action_class = 'sandbox_execute'
								AND approval.mode = 'per_call' AND approval.status = 'approved'
								AND approval.request_fingerprint = NEW.authorization_fingerprint)))
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases lease
					WHERE lease.run_id = NEW.run_id AND lease.status = 'active'
						AND julianday(lease.expires_at) > julianday('now'))
				AND NEW.tokens_used =
					COALESCE((SELECT SUM(node.tokens_used) FROM agent_nodes node WHERE node.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.usage_recorded = 1 THEN call.total_tokens
						ELSE call.reserved_total_tokens END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND NEW.execution_millis_used =
					COALESCE((SELECT checkpoint.execution_millis FROM run_supervisor_checkpoints checkpoint
						WHERE checkpoint.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.elapsed_recorded = 1 THEN call.elapsed_millis
						ELSE call.reserved_millis END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND NEW.tool_calls_used = COALESCE((SELECT usage.consumed FROM run_tool_usage usage
					WHERE usage.run_id = NEW.run_id), 0)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER), 0) = 0
					OR NEW.tokens_used < CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER))
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER), 0) = 0
					OR NEW.execution_millis_used < CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER) * 1000)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER), 0) = 0
					OR NEW.tool_calls_used < CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER))
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox execution candidate binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_candidate_operation_insert
		BEFORE INSERT ON sandbox_execution_candidate_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_execution_candidates candidate
			WHERE candidate.id = NEW.candidate_id AND candidate.preparation_id = NEW.preparation_id
				AND candidate.run_id = NEW.run_id AND candidate.requested_by = NEW.requested_by
				AND candidate.validated_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox execution candidate operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_candidate_update_immutable
		BEFORE UPDATE ON sandbox_execution_candidates BEGIN
			SELECT RAISE(ABORT, 'sandbox execution candidate cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_candidate_delete_immutable
		BEFORE DELETE ON sandbox_execution_candidates BEGIN
			SELECT RAISE(ABORT, 'sandbox execution candidate cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_candidate_operation_update_immutable
		BEFORE UPDATE ON sandbox_execution_candidate_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox execution candidate operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_candidate_operation_delete_immutable
		BEFORE DELETE ON sandbox_execution_candidate_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox execution candidate operation cannot be deleted');
		END;`,
}

var sandboxLifecycleStatements = []string{
	`CREATE TABLE sandbox_disabled_executions (
		id TEXT PRIMARY KEY,
		candidate_id TEXT NOT NULL UNIQUE,
		preparation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		cancellation_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		manifest_fingerprint TEXT NOT NULL,
		authorization_fingerprint TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		mount_binding_fingerprint TEXT NOT NULL,
		input_artifact_count INTEGER NOT NULL,
		input_artifact_bytes INTEGER NOT NULL,
		input_artifact_digest TEXT NOT NULL,
		capture_stdout INTEGER NOT NULL,
		capture_stderr INTEGER NOT NULL,
		output_path_count INTEGER NOT NULL,
		max_output_bytes INTEGER NOT NULL,
		output_plan_fingerprint TEXT NOT NULL,
		initial_lease_id TEXT NOT NULL UNIQUE,
		initial_lease_generation INTEGER NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		backend_started INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(candidate_id) REFERENCES sandbox_execution_candidates(id) ON DELETE RESTRICT,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_execution.v1'),
		CHECK(input_artifact_count BETWEEN 0 AND 16),
		CHECK(input_artifact_bytes BETWEEN 0 AND 16777216),
		CHECK((input_artifact_count = 0 AND input_artifact_bytes = 0)
			OR (input_artifact_count > 0 AND input_artifact_bytes > 0)),
		CHECK(capture_stdout IN (0, 1) AND capture_stderr IN (0, 1)),
		CHECK(output_path_count BETWEEN 0 AND 16),
		CHECK(capture_stdout = 1 OR capture_stderr = 1 OR output_path_count > 0),
		CHECK(max_output_bytes BETWEEN 1 AND 16777216),
		CHECK(initial_lease_generation = 1),
		CHECK(backend_enabled = 0 AND execution_authorized = 0 AND backend_started = 0),
		CHECK(length(manifest_fingerprint) = 64 AND manifest_fingerprint = lower(manifest_fingerprint)
			AND manifest_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(authorization_fingerprint) = 64 AND authorization_fingerprint = lower(authorization_fingerprint)
			AND authorization_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mount_binding_fingerprint) = 64 AND mount_binding_fingerprint = lower(mount_binding_fingerprint)
			AND mount_binding_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(input_artifact_digest) = 64 AND input_artifact_digest = lower(input_artifact_digest)
			AND input_artifact_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(output_plan_fingerprint) = 64 AND output_plan_fingerprint = lower(output_plan_fingerprint)
			AND output_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(candidate_id = trim(candidate_id) AND length(candidate_id) BETWEEN 1 AND 256
			AND instr(candidate_id, char(0)) = 0),
		CHECK(preparation_id = trim(preparation_id) AND length(preparation_id) BETWEEN 1 AND 256
			AND instr(preparation_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256 AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256
			AND instr(workspace_id, char(0)) = 0),
		CHECK(cancellation_id = trim(cancellation_id) AND length(cancellation_id) BETWEEN 1 AND 256
			AND instr(cancellation_id, char(0)) = 0),
		CHECK(initial_lease_id = trim(initial_lease_id) AND length(initial_lease_id) BETWEEN 1 AND 256
			AND instr(initial_lease_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_disabled_executions_run_created
		ON sandbox_disabled_executions(run_id, created_at, id);`,
	`CREATE TABLE sandbox_execution_inputs (
		execution_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		mime TEXT NOT NULL,
		stream TEXT NOT NULL,
		source_id TEXT NOT NULL,
		redacted INTEGER NOT NULL,
		PRIMARY KEY(execution_id, ordinal),
		UNIQUE(execution_id, artifact_id),
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(artifact_id) REFERENCES run_artifacts(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 16),
		CHECK(length(sha256) = 64 AND sha256 = lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(size_bytes BETWEEN 1 AND 16777216),
		CHECK(length(mime) BETWEEN 1 AND 256 AND instr(mime, char(0)) = 0),
		CHECK(stream IN ('stdout', 'stderr')),
		CHECK(redacted IN (0, 1)),
		CHECK(artifact_id = trim(artifact_id) AND length(artifact_id) BETWEEN 1 AND 256
			AND instr(artifact_id, char(0)) = 0),
		CHECK(source_id = trim(source_id) AND length(source_id) BETWEEN 1 AND 256
			AND instr(source_id, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_execution_leases (
		execution_id TEXT PRIMARY KEY,
		lease_id TEXT NOT NULL UNIQUE,
		owner_id TEXT NOT NULL,
		generation INTEGER NOT NULL,
		status TEXT NOT NULL,
		acquired_at TEXT NOT NULL,
		renewed_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		released_at TEXT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		CHECK(generation >= 1),
		CHECK(status IN ('active', 'released')),
		CHECK((status = 'active' AND released_at IS NULL)
			OR (status = 'released' AND released_at IS NOT NULL)),
		CHECK(lease_id = trim(lease_id) AND length(lease_id) BETWEEN 1 AND 256 AND instr(lease_id, char(0)) = 0),
		CHECK(owner_id = trim(owner_id) AND length(owner_id) BETWEEN 1 AND 256 AND instr(owner_id, char(0)) = 0),
		CHECK(julianday(renewed_at) >= julianday(acquired_at)),
		CHECK(julianday(expires_at) > julianday(renewed_at)),
		CHECK(released_at IS NULL OR julianday(released_at) >= julianday(acquired_at))
	) WITHOUT ROWID;`,
	`CREATE INDEX idx_sandbox_execution_leases_status_expires
		ON sandbox_execution_leases(status, expires_at);`,
	`CREATE TABLE sandbox_execution_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		execution_id TEXT NOT NULL UNIQUE,
		candidate_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(candidate_id) REFERENCES sandbox_execution_candidates(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_execution_cancellations (
		id TEXT PRIMARY KEY,
		execution_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		cancellation_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		requested_at TEXT NOT NULL,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_execution_cancel.v1'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(cancellation_id = trim(cancellation_id) AND length(cancellation_id) BETWEEN 1 AND 256
			AND instr(cancellation_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE TABLE sandbox_execution_cancellation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		request_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(request_id) REFERENCES sandbox_execution_cancellations(id) ON DELETE RESTRICT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_cleanup_results (
		id TEXT PRIMARY KEY,
		execution_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		lease_id TEXT NOT NULL,
		lease_generation INTEGER NOT NULL,
		cancellation_observed INTEGER NOT NULL,
		backend_started INTEGER NOT NULL,
		orphan_detected INTEGER NOT NULL,
		orphan_reaped INTEGER NOT NULL,
		input_artifacts_verified INTEGER NOT NULL,
		output_artifact_count INTEGER NOT NULL,
		cleanup_complete INTEGER NOT NULL,
		outcome TEXT NOT NULL,
		reconciled_by TEXT NOT NULL,
		completed_at TEXT NOT NULL,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_cleanup.v1'),
		CHECK(lease_generation >= 1),
		CHECK(cancellation_observed IN (0, 1)),
		CHECK(backend_started = 0 AND orphan_detected = 0 AND orphan_reaped = 0),
		CHECK(input_artifacts_verified = 1 AND output_artifact_count = 0),
		CHECK(cleanup_complete = 1 AND outcome = 'backend_disabled'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(lease_id = trim(lease_id) AND length(lease_id) BETWEEN 1 AND 256 AND instr(lease_id, char(0)) = 0),
		CHECK(reconciled_by = trim(reconciled_by) AND length(reconciled_by) BETWEEN 1 AND 256
			AND instr(reconciled_by, char(0)) = 0)
	);`,
	`CREATE TABLE sandbox_cleanup_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		cleanup_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		reconciled_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(cleanup_id) REFERENCES sandbox_cleanup_results(id) ON DELETE RESTRICT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_sandbox_disabled_execution_insert
		BEFORE INSERT ON sandbox_disabled_executions
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_execution_candidates candidate
			JOIN sandbox_manifest_preparations preparation ON preparation.id = candidate.preparation_id
			JOIN runs run ON run.id = candidate.run_id
			JOIN missions mission ON mission.id = candidate.mission_id
			WHERE candidate.id = NEW.candidate_id AND candidate.preparation_id = NEW.preparation_id
				AND candidate.run_id = NEW.run_id AND candidate.mission_id = NEW.mission_id
				AND candidate.workspace_id = NEW.workspace_id
				AND preparation.cancellation_id = NEW.cancellation_id
				AND candidate.manifest_fingerprint = NEW.manifest_fingerprint
				AND candidate.authorization_fingerprint = NEW.authorization_fingerprint
				AND candidate.policy_fingerprint = NEW.policy_fingerprint
				AND candidate.mount_binding_fingerprint = NEW.mount_binding_fingerprint
				AND candidate.requested_by = NEW.requested_by
				AND candidate.backend_enabled = 0 AND candidate.execution_authorized = 0
				AND preparation.input_artifact_count = NEW.input_artifact_count
				AND preparation.max_output_bytes = NEW.max_output_bytes
				AND preparation.output_count = NEW.capture_stdout + NEW.capture_stderr + NEW.output_path_count
				AND run.mission_id = NEW.mission_id AND mission.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND julianday(NEW.created_at) >= julianday(candidate.validated_at)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases lease
					WHERE lease.run_id = NEW.run_id AND lease.status = 'active'
						AND julianday(lease.expires_at) > julianday('now'))
				AND candidate.tokens_used =
					COALESCE((SELECT SUM(node.tokens_used) FROM agent_nodes node WHERE node.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.usage_recorded = 1 THEN call.total_tokens
						ELSE call.reserved_total_tokens END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.execution_millis_used =
					COALESCE((SELECT checkpoint.execution_millis FROM run_supervisor_checkpoints checkpoint
						WHERE checkpoint.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.elapsed_recorded = 1 THEN call.elapsed_millis
						ELSE call.reserved_millis END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.tool_calls_used = COALESCE((SELECT usage.consumed FROM run_tool_usage usage
					WHERE usage.run_id = NEW.run_id), 0)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER), 0) = 0
					OR candidate.tokens_used < CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER))
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER), 0) = 0
					OR candidate.execution_millis_used < CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER) * 1000)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER), 0) = 0
					OR candidate.tool_calls_used < CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER))
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox disabled execution binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_input_insert
		BEFORE INSERT ON sandbox_execution_inputs
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_executions execution
			JOIN runs run ON run.id = execution.run_id
			JOIN run_artifacts artifact ON artifact.id = NEW.artifact_id
			WHERE execution.id = NEW.execution_id AND artifact.run_id = execution.run_id
				AND artifact.session_id = run.session_id AND artifact.workspace_id = execution.workspace_id
				AND artifact.sha256 = NEW.sha256 AND artifact.size_bytes = NEW.size_bytes
				AND artifact.mime = NEW.mime AND artifact.stream = NEW.stream
				AND artifact.source_id = NEW.source_id AND artifact.redacted = NEW.redacted
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox execution input Artifact binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_lease_insert
		BEFORE INSERT ON sandbox_execution_leases
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_executions execution
			WHERE execution.id = NEW.execution_id AND execution.initial_lease_id = NEW.lease_id
				AND execution.initial_lease_generation = NEW.generation
				AND NEW.status = 'active' AND NEW.acquired_at = execution.created_at
				AND NEW.renewed_at = NEW.acquired_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox initial execution lease binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_lease_update
		BEFORE UPDATE ON sandbox_execution_leases
		WHEN NEW.execution_id <> OLD.execution_id
			OR NEW.generation < OLD.generation OR NEW.generation > OLD.generation + 1
			OR (NEW.generation = OLD.generation AND NEW.lease_id <> OLD.lease_id)
			OR (NEW.generation = OLD.generation AND NEW.owner_id <> OLD.owner_id)
			OR (NEW.generation = OLD.generation AND NEW.acquired_at <> OLD.acquired_at)
			OR (NEW.generation = OLD.generation AND OLD.status = 'released' AND NEW.status <> 'released')
			OR (NEW.generation = OLD.generation AND OLD.status = 'active' AND NEW.status NOT IN ('active', 'released'))
			OR (NEW.generation = OLD.generation + 1 AND (
				NEW.status <> 'active' OR NEW.released_at IS NOT NULL
				OR NOT (OLD.status = 'released' OR julianday(OLD.expires_at) <= julianday('now'))))
		BEGIN
			SELECT RAISE(ABORT, 'sandbox execution lease transition is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_operation_insert
		BEFORE INSERT ON sandbox_execution_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_executions execution
			JOIN sandbox_execution_leases lease ON lease.execution_id = execution.id
			WHERE execution.id = NEW.execution_id AND execution.candidate_id = NEW.candidate_id
				AND execution.run_id = NEW.run_id AND execution.requested_by = NEW.requested_by
				AND execution.created_at = NEW.created_at
				AND lease.lease_id = execution.initial_lease_id
				AND lease.generation = execution.initial_lease_generation
				AND (SELECT COUNT(*) FROM sandbox_execution_inputs input
					WHERE input.execution_id = execution.id) = execution.input_artifact_count
				AND COALESCE((SELECT SUM(input.size_bytes) FROM sandbox_execution_inputs input
					WHERE input.execution_id = execution.id), 0) = execution.input_artifact_bytes
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox execution operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_cancellation_insert
		BEFORE INSERT ON sandbox_execution_cancellations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_executions execution
			WHERE execution.id = NEW.execution_id AND execution.run_id = NEW.run_id
				AND execution.cancellation_id = NEW.cancellation_id
				AND julianday(NEW.requested_at) >= julianday(execution.created_at)
				AND NOT EXISTS (SELECT 1 FROM sandbox_cleanup_results cleanup
					WHERE cleanup.execution_id = execution.id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox cancellation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_cancellation_operation_insert
		BEFORE INSERT ON sandbox_execution_cancellation_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_execution_cancellations cancellation
			WHERE cancellation.id = NEW.request_id AND cancellation.execution_id = NEW.execution_id
				AND cancellation.run_id = NEW.run_id AND cancellation.requested_by = NEW.requested_by
				AND cancellation.requested_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox cancellation operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_cleanup_result_insert
		BEFORE INSERT ON sandbox_cleanup_results
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_executions execution
			JOIN sandbox_execution_leases lease ON lease.execution_id = execution.id
			WHERE execution.id = NEW.execution_id AND execution.run_id = NEW.run_id
				AND lease.lease_id = NEW.lease_id AND lease.generation = NEW.lease_generation
				AND lease.status = 'active' AND julianday(lease.expires_at) > julianday('now')
				AND julianday(NEW.completed_at) >= julianday(execution.created_at)
				AND NEW.cancellation_observed = CASE WHEN EXISTS (
					SELECT 1 FROM sandbox_execution_cancellations cancellation
					WHERE cancellation.execution_id = execution.id) THEN 1 ELSE 0 END
				AND (SELECT COUNT(*) FROM sandbox_execution_inputs input
					JOIN run_artifacts artifact ON artifact.id = input.artifact_id
					WHERE input.execution_id = execution.id AND artifact.run_id = execution.run_id
						AND artifact.sha256 = input.sha256 AND artifact.size_bytes = input.size_bytes
						AND artifact.mime = input.mime AND artifact.stream = input.stream
						AND artifact.source_id = input.source_id AND artifact.redacted = input.redacted)
					= execution.input_artifact_count
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox cleanup result binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_cleanup_operation_insert
		BEFORE INSERT ON sandbox_cleanup_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_cleanup_results cleanup
			WHERE cleanup.id = NEW.cleanup_id AND cleanup.execution_id = NEW.execution_id
				AND cleanup.run_id = NEW.run_id AND cleanup.reconciled_by = NEW.reconciled_by
				AND cleanup.completed_at = NEW.created_at
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox cleanup operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_disabled_execution_update_immutable
		BEFORE UPDATE ON sandbox_disabled_executions BEGIN
			SELECT RAISE(ABORT, 'sandbox disabled execution cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_disabled_execution_delete_immutable
		BEFORE DELETE ON sandbox_disabled_executions BEGIN
			SELECT RAISE(ABORT, 'sandbox disabled execution cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_input_update_immutable
		BEFORE UPDATE ON sandbox_execution_inputs BEGIN
			SELECT RAISE(ABORT, 'sandbox execution input cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_input_delete_immutable
		BEFORE DELETE ON sandbox_execution_inputs BEGIN
			SELECT RAISE(ABORT, 'sandbox execution input cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_operation_update_immutable
		BEFORE UPDATE ON sandbox_execution_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox execution operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_operation_delete_immutable
		BEFORE DELETE ON sandbox_execution_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox execution operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_cancellation_update_immutable
		BEFORE UPDATE ON sandbox_execution_cancellations BEGIN
			SELECT RAISE(ABORT, 'sandbox cancellation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_cancellation_delete_immutable
		BEFORE DELETE ON sandbox_execution_cancellations BEGIN
			SELECT RAISE(ABORT, 'sandbox cancellation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_cancellation_operation_update_immutable
		BEFORE UPDATE ON sandbox_execution_cancellation_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox cancellation operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_execution_cancellation_operation_delete_immutable
		BEFORE DELETE ON sandbox_execution_cancellation_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox cancellation operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_cleanup_result_update_immutable
		BEFORE UPDATE ON sandbox_cleanup_results BEGIN
			SELECT RAISE(ABORT, 'sandbox cleanup result cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_cleanup_result_delete_immutable
		BEFORE DELETE ON sandbox_cleanup_results BEGIN
			SELECT RAISE(ABORT, 'sandbox cleanup result cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_cleanup_operation_update_immutable
		BEFORE UPDATE ON sandbox_cleanup_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox cleanup operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_cleanup_operation_delete_immutable
		BEFORE DELETE ON sandbox_cleanup_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox cleanup operation cannot be deleted');
		END;`,
}

var sandboxPreflightStatements = []string{
	`CREATE TABLE sandbox_disabled_preflights (
		id TEXT PRIMARY KEY,
		execution_id TEXT NOT NULL UNIQUE,
		candidate_id TEXT NOT NULL,
		preparation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		backend TEXT NOT NULL,
		manifest_fingerprint TEXT NOT NULL,
		authorization_fingerprint TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		mount_binding_fingerprint TEXT NOT NULL,
		input_artifact_digest TEXT NOT NULL,
		handshake_protocol TEXT NOT NULL,
		inspector_name TEXT NOT NULL,
		handshake_status TEXT NOT NULL,
		backend_available INTEGER NOT NULL,
		threat_model_fingerprint TEXT NOT NULL,
		container_identity_protocol TEXT NOT NULL,
		container_runtime TEXT NOT NULL,
		container_identity_bound INTEGER NOT NULL,
		container_identity_fingerprint TEXT NOT NULL,
		output_protocol TEXT NOT NULL,
		capture_stdout INTEGER NOT NULL,
		capture_stderr INTEGER NOT NULL,
		output_plan_fingerprint TEXT NOT NULL,
		output_slot_count INTEGER NOT NULL,
		max_output_bytes INTEGER NOT NULL,
		partial_failure_policy TEXT NOT NULL,
		truncation_policy TEXT NOT NULL,
		mime_policy TEXT NOT NULL,
		file_type_policy TEXT NOT NULL,
		restart_policy TEXT NOT NULL,
		raw_paths_stored INTEGER NOT NULL,
		output_export_enabled INTEGER NOT NULL,
		artifact_commit_authorized INTEGER NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(candidate_id) REFERENCES sandbox_execution_candidates(id) ON DELETE RESTRICT,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_preflight.v1'),
		CHECK(backend IN ('noop', 'local', 'docker')),
		CHECK(handshake_protocol = 'sandbox_backend_handshake.v1'),
		CHECK(inspector_name = 'disabled' AND handshake_status = 'backend_disabled'),
		CHECK(backend_available = 0),
		CHECK(container_identity_protocol = 'sandbox_container_identity.v1'),
		CHECK(container_runtime = 'none' AND container_identity_bound = 0
			AND container_identity_fingerprint = ''),
		CHECK(output_protocol = 'sandbox_output_export_plan.v1'),
		CHECK(capture_stdout IN (0, 1) AND capture_stderr IN (0, 1)),
		CHECK(output_slot_count BETWEEN 1 AND 18),
		CHECK(capture_stdout + capture_stderr <= output_slot_count),
		CHECK(max_output_bytes BETWEEN 1 AND 16777216),
		CHECK(partial_failure_policy = 'all_or_nothing'),
		CHECK(truncation_policy = 'aggregate_hard_limit'),
		CHECK(mime_policy = 'detect_and_validate'),
		CHECK(file_type_policy = 'regular_file_no_symlink_or_special'),
		CHECK(restart_policy = 'reconcile_before_retry'),
		CHECK(raw_paths_stored = 0 AND output_export_enabled = 0
			AND artifact_commit_authorized = 0),
		CHECK(backend_enabled = 0 AND execution_authorized = 0),
		CHECK(length(manifest_fingerprint) = 64 AND manifest_fingerprint = lower(manifest_fingerprint)
			AND manifest_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(authorization_fingerprint) = 64 AND authorization_fingerprint = lower(authorization_fingerprint)
			AND authorization_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mount_binding_fingerprint) = 64 AND mount_binding_fingerprint = lower(mount_binding_fingerprint)
			AND mount_binding_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(input_artifact_digest) = 64 AND input_artifact_digest = lower(input_artifact_digest)
			AND input_artifact_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(threat_model_fingerprint) = 64 AND threat_model_fingerprint = lower(threat_model_fingerprint)
			AND threat_model_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(output_plan_fingerprint) = 64 AND output_plan_fingerprint = lower(output_plan_fingerprint)
			AND output_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(execution_id = trim(execution_id) AND length(execution_id) BETWEEN 1 AND 256
			AND instr(execution_id, char(0)) = 0),
		CHECK(candidate_id = trim(candidate_id) AND length(candidate_id) BETWEEN 1 AND 256
			AND instr(candidate_id, char(0)) = 0),
		CHECK(preparation_id = trim(preparation_id) AND length(preparation_id) BETWEEN 1 AND 256
			AND instr(preparation_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256
			AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256
			AND instr(workspace_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_disabled_preflights_run_created
		ON sandbox_disabled_preflights(run_id, created_at, id);`,
	`CREATE TABLE sandbox_backend_preflight_checks (
		preflight_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		name TEXT NOT NULL,
		required INTEGER NOT NULL,
		verified INTEGER NOT NULL,
		evidence_state TEXT NOT NULL,
		PRIMARY KEY(preflight_id, ordinal),
		UNIQUE(preflight_id, name),
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 16),
		CHECK((ordinal = 1 AND name = 'host_path_isolation')
			OR (ordinal = 2 AND name = 'mount_propagation_private')
			OR (ordinal = 3 AND name = 'read_only_rootfs')
			OR (ordinal = 4 AND name = 'read_only_inputs')
			OR (ordinal = 5 AND name = 'dedicated_writable_output')
			OR (ordinal = 6 AND name = 'network_default_deny')
			OR (ordinal = 7 AND name = 'exact_network_allowlist')
			OR (ordinal = 8 AND name = 'ephemeral_secret_materialization')
			OR (ordinal = 9 AND name = 'non_root_container_identity')
			OR (ordinal = 10 AND name = 'cpu_memory_pid_limits')
			OR (ordinal = 11 AND name = 'wall_clock_timeout')
			OR (ordinal = 12 AND name = 'graceful_then_forced_kill')
			OR (ordinal = 13 AND name = 'orphan_reconciliation')
			OR (ordinal = 14 AND name = 'output_regular_file_only')
			OR (ordinal = 15 AND name = 'output_symlink_special_rejection')
			OR (ordinal = 16 AND name = 'atomic_output_artifact_commit')),
		CHECK(required = 1 AND verified = 0 AND evidence_state = 'not_probed')
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_output_export_slots (
		preflight_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		kind TEXT NOT NULL,
		locator_fingerprint TEXT NOT NULL,
		regular_file_required INTEGER NOT NULL,
		symlink_rejected INTEGER NOT NULL,
		special_file_rejected INTEGER NOT NULL,
		mime_detection_required INTEGER NOT NULL,
		redaction_required INTEGER NOT NULL,
		artifact_commit_authorized INTEGER NOT NULL,
		PRIMARY KEY(preflight_id, ordinal),
		UNIQUE(preflight_id, locator_fingerprint),
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 18),
		CHECK(kind IN ('stdout', 'stderr', 'file')),
		CHECK(length(locator_fingerprint) = 64 AND locator_fingerprint = lower(locator_fingerprint)
			AND locator_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK((kind IN ('stdout', 'stderr') AND regular_file_required = 0
			AND symlink_rejected = 0 AND special_file_rejected = 0)
			OR (kind = 'file' AND regular_file_required = 1
				AND symlink_rejected = 1 AND special_file_rejected = 1)),
		CHECK(mime_detection_required = 1 AND redaction_required = 1
			AND artifact_commit_authorized = 0)
	) WITHOUT ROWID;`,
	`CREATE UNIQUE INDEX idx_sandbox_output_export_stream_unique
		ON sandbox_output_export_slots(preflight_id, kind) WHERE kind IN ('stdout', 'stderr');`,
	`CREATE TABLE sandbox_preflight_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		preflight_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_sandbox_disabled_preflight_insert
		BEFORE INSERT ON sandbox_disabled_preflights
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_executions execution
			JOIN sandbox_execution_candidates candidate ON candidate.id = execution.candidate_id
			JOIN sandbox_manifest_preparations preparation ON preparation.id = execution.preparation_id
			JOIN sandbox_execution_leases sandbox_lease ON sandbox_lease.execution_id = execution.id
			JOIN runs run ON run.id = execution.run_id
			JOIN missions mission ON mission.id = execution.mission_id
			WHERE execution.id = NEW.execution_id AND execution.candidate_id = NEW.candidate_id
				AND execution.preparation_id = NEW.preparation_id AND execution.run_id = NEW.run_id
				AND execution.mission_id = NEW.mission_id AND execution.workspace_id = NEW.workspace_id
				AND preparation.backend = NEW.backend
				AND execution.manifest_fingerprint = NEW.manifest_fingerprint
				AND execution.authorization_fingerprint = NEW.authorization_fingerprint
				AND execution.policy_fingerprint = NEW.policy_fingerprint
				AND execution.mount_binding_fingerprint = NEW.mount_binding_fingerprint
				AND execution.input_artifact_digest = NEW.input_artifact_digest
				AND execution.capture_stdout = NEW.capture_stdout
				AND execution.capture_stderr = NEW.capture_stderr
				AND execution.output_path_count + execution.capture_stdout + execution.capture_stderr = NEW.output_slot_count
				AND execution.max_output_bytes = NEW.max_output_bytes
				AND execution.requested_by = NEW.requested_by
				AND execution.backend_enabled = 0 AND execution.execution_authorized = 0
				AND execution.backend_started = 0
				AND candidate.backend_enabled = 0 AND candidate.execution_authorized = 0
				AND sandbox_lease.status = 'released'
				AND run.mission_id = NEW.mission_id AND mission.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND julianday(NEW.created_at) >= julianday(execution.created_at)
				AND NOT EXISTS (SELECT 1 FROM sandbox_execution_cancellations cancellation
					WHERE cancellation.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM sandbox_cleanup_results cleanup
					WHERE cleanup.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases run_lease
					WHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'
						AND julianday(run_lease.expires_at) > julianday('now'))
				AND candidate.tokens_used =
					COALESCE((SELECT SUM(node.tokens_used) FROM agent_nodes node WHERE node.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.usage_recorded = 1 THEN call.total_tokens
						ELSE call.reserved_total_tokens END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.execution_millis_used =
					COALESCE((SELECT checkpoint.execution_millis FROM run_supervisor_checkpoints checkpoint
						WHERE checkpoint.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.elapsed_recorded = 1 THEN call.elapsed_millis
						ELSE call.reserved_millis END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.tool_calls_used = COALESCE((SELECT usage.consumed FROM run_tool_usage usage
					WHERE usage.run_id = NEW.run_id), 0)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER), 0) = 0
					OR candidate.tokens_used < CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER))
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER), 0) = 0
					OR candidate.execution_millis_used < CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER) * 1000)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER), 0) = 0
					OR candidate.tool_calls_used < CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER))
				AND (SELECT COUNT(*) FROM sandbox_execution_inputs input
					JOIN run_artifacts artifact ON artifact.id = input.artifact_id
					WHERE input.execution_id = execution.id AND artifact.run_id = execution.run_id
						AND artifact.session_id = run.session_id AND artifact.workspace_id = execution.workspace_id
						AND artifact.sha256 = input.sha256 AND artifact.size_bytes = input.size_bytes
						AND artifact.mime = input.mime AND artifact.stream = input.stream
						AND artifact.source_id = input.source_id AND artifact.redacted = input.redacted)
					= execution.input_artifact_count
				AND COALESCE((SELECT SUM(input.size_bytes) FROM sandbox_execution_inputs input
					WHERE input.execution_id = execution.id), 0) = execution.input_artifact_bytes
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox disabled preflight binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_preflight_check_insert
		BEFORE INSERT ON sandbox_backend_preflight_checks
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_disabled_preflights preflight
			WHERE preflight.id = NEW.preflight_id AND preflight.handshake_status = 'backend_disabled')
		BEGIN
			SELECT RAISE(ABORT, 'sandbox backend preflight check binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_export_slot_insert
		BEFORE INSERT ON sandbox_output_export_slots
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_disabled_preflights preflight
			WHERE preflight.id = NEW.preflight_id AND preflight.output_export_enabled = 0
				AND preflight.artifact_commit_authorized = 0)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox output export slot binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_preflight_operation_insert
		BEFORE INSERT ON sandbox_preflight_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_preflights preflight
			WHERE preflight.id = NEW.preflight_id AND preflight.execution_id = NEW.execution_id
				AND preflight.run_id = NEW.run_id AND preflight.requested_by = NEW.requested_by
				AND preflight.created_at = NEW.created_at
				AND (SELECT COUNT(*) FROM sandbox_backend_preflight_checks check_row
					WHERE check_row.preflight_id = preflight.id) = 16
				AND (SELECT COUNT(*) FROM sandbox_backend_preflight_checks check_row
					WHERE check_row.preflight_id = preflight.id AND check_row.required = 1
						AND check_row.verified = 0 AND check_row.evidence_state = 'not_probed') = 16
				AND (SELECT COUNT(*) FROM sandbox_output_export_slots slot
					WHERE slot.preflight_id = preflight.id) = preflight.output_slot_count
				AND (SELECT COUNT(*) FROM sandbox_output_export_slots slot
					WHERE slot.preflight_id = preflight.id AND slot.kind = 'stdout') = preflight.capture_stdout
				AND (SELECT COUNT(*) FROM sandbox_output_export_slots slot
					WHERE slot.preflight_id = preflight.id AND slot.kind = 'stderr') = preflight.capture_stderr
				AND (SELECT COUNT(*) FROM sandbox_output_export_slots slot
					WHERE slot.preflight_id = preflight.id AND slot.kind = 'file') =
					preflight.output_slot_count - preflight.capture_stdout - preflight.capture_stderr
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox preflight operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_disabled_preflight_update_immutable
		BEFORE UPDATE ON sandbox_disabled_preflights BEGIN
			SELECT RAISE(ABORT, 'sandbox disabled preflight cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_disabled_preflight_delete_immutable
		BEFORE DELETE ON sandbox_disabled_preflights BEGIN
			SELECT RAISE(ABORT, 'sandbox disabled preflight cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_preflight_check_update_immutable
		BEFORE UPDATE ON sandbox_backend_preflight_checks BEGIN
			SELECT RAISE(ABORT, 'sandbox backend preflight check cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_preflight_check_delete_immutable
		BEFORE DELETE ON sandbox_backend_preflight_checks BEGIN
			SELECT RAISE(ABORT, 'sandbox backend preflight check cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_export_slot_update_immutable
		BEFORE UPDATE ON sandbox_output_export_slots BEGIN
			SELECT RAISE(ABORT, 'sandbox output export slot cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_export_slot_delete_immutable
		BEFORE DELETE ON sandbox_output_export_slots BEGIN
			SELECT RAISE(ABORT, 'sandbox output export slot cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_preflight_operation_update_immutable
		BEFORE UPDATE ON sandbox_preflight_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox preflight operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_preflight_operation_delete_immutable
		BEFORE DELETE ON sandbox_preflight_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox preflight operation cannot be deleted');
		END;`,
}

var sandboxBackendEvidenceStatements = []string{
	`CREATE TABLE sandbox_backend_evidence (
		id TEXT PRIMARY KEY,
		preflight_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL,
		candidate_id TEXT NOT NULL,
		preparation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		source TEXT NOT NULL,
		trust_class TEXT NOT NULL,
		status TEXT NOT NULL,
		backend TEXT NOT NULL,
		image_digest TEXT NOT NULL,
		manifest_fingerprint TEXT NOT NULL,
		authorization_fingerprint TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		mount_binding_fingerprint TEXT NOT NULL,
		input_artifact_digest TEXT NOT NULL,
		threat_model_fingerprint TEXT NOT NULL,
		daemon_capabilities_fingerprint TEXT NOT NULL,
		mount_plan_fingerprint TEXT NOT NULL,
		network_plan_fingerprint TEXT NOT NULL,
		secret_plan_fingerprint TEXT NOT NULL,
		container_config_fingerprint TEXT NOT NULL,
		resource_plan_fingerprint TEXT NOT NULL,
		termination_plan_fingerprint TEXT NOT NULL,
		orphan_plan_fingerprint TEXT NOT NULL,
		output_plan_fingerprint TEXT NOT NULL,
		evidence_fingerprint TEXT NOT NULL,
		evidence_count INTEGER NOT NULL,
		satisfied_count INTEGER NOT NULL,
		verified_count INTEGER NOT NULL,
		production_verified INTEGER NOT NULL,
		backend_available INTEGER NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		artifact_commit_authorized INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(candidate_id) REFERENCES sandbox_execution_candidates(id) ON DELETE RESTRICT,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_backend_evidence.v1'),
		CHECK(source = 'in_memory_fake' AND trust_class = 'simulation_only'
			AND status = 'simulation_complete'),
		CHECK(backend = 'docker'),
		CHECK(length(image_digest) = 71 AND substr(image_digest, 1, 7) = 'sha256:'
			AND substr(image_digest, 8) = lower(substr(image_digest, 8))
			AND substr(image_digest, 8) NOT GLOB '*[^0-9a-f]*'),
		CHECK(evidence_count = 16 AND satisfied_count = 16 AND verified_count = 0),
		CHECK(production_verified = 0 AND backend_available = 0 AND backend_enabled = 0
			AND execution_authorized = 0 AND artifact_commit_authorized = 0),
		CHECK(length(manifest_fingerprint) = 64 AND manifest_fingerprint = lower(manifest_fingerprint)
			AND manifest_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(authorization_fingerprint) = 64 AND authorization_fingerprint = lower(authorization_fingerprint)
			AND authorization_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mount_binding_fingerprint) = 64 AND mount_binding_fingerprint = lower(mount_binding_fingerprint)
			AND mount_binding_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(input_artifact_digest) = 64 AND input_artifact_digest = lower(input_artifact_digest)
			AND input_artifact_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(threat_model_fingerprint) = 64 AND threat_model_fingerprint = lower(threat_model_fingerprint)
			AND threat_model_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(daemon_capabilities_fingerprint) = 64 AND daemon_capabilities_fingerprint = lower(daemon_capabilities_fingerprint)
			AND daemon_capabilities_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mount_plan_fingerprint) = 64 AND mount_plan_fingerprint = lower(mount_plan_fingerprint)
			AND mount_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(network_plan_fingerprint) = 64 AND network_plan_fingerprint = lower(network_plan_fingerprint)
			AND network_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(secret_plan_fingerprint) = 64 AND secret_plan_fingerprint = lower(secret_plan_fingerprint)
			AND secret_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(container_config_fingerprint) = 64 AND container_config_fingerprint = lower(container_config_fingerprint)
			AND container_config_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(resource_plan_fingerprint) = 64 AND resource_plan_fingerprint = lower(resource_plan_fingerprint)
			AND resource_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(termination_plan_fingerprint) = 64 AND termination_plan_fingerprint = lower(termination_plan_fingerprint)
			AND termination_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(orphan_plan_fingerprint) = 64 AND orphan_plan_fingerprint = lower(orphan_plan_fingerprint)
			AND orphan_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(output_plan_fingerprint) = 64 AND output_plan_fingerprint = lower(output_plan_fingerprint)
			AND output_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(evidence_fingerprint) = 64 AND evidence_fingerprint = lower(evidence_fingerprint)
			AND evidence_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(preflight_id = trim(preflight_id) AND length(preflight_id) BETWEEN 1 AND 256
			AND instr(preflight_id, char(0)) = 0),
		CHECK(execution_id = trim(execution_id) AND length(execution_id) BETWEEN 1 AND 256
			AND instr(execution_id, char(0)) = 0),
		CHECK(candidate_id = trim(candidate_id) AND length(candidate_id) BETWEEN 1 AND 256
			AND instr(candidate_id, char(0)) = 0),
		CHECK(preparation_id = trim(preparation_id) AND length(preparation_id) BETWEEN 1 AND 256
			AND instr(preparation_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256
			AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256
			AND instr(workspace_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_backend_evidence_run_created
		ON sandbox_backend_evidence(run_id, created_at, id);`,
	`CREATE TABLE sandbox_backend_evidence_items (
		evidence_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		name TEXT NOT NULL,
		evidence_state TEXT NOT NULL,
		evidence_digest TEXT NOT NULL,
		satisfied INTEGER NOT NULL,
		verified INTEGER NOT NULL,
		PRIMARY KEY(evidence_id, ordinal),
		UNIQUE(evidence_id, name),
		FOREIGN KEY(evidence_id) REFERENCES sandbox_backend_evidence(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 16),
		CHECK((ordinal = 1 AND name = 'host_path_isolation')
			OR (ordinal = 2 AND name = 'mount_propagation_private')
			OR (ordinal = 3 AND name = 'read_only_rootfs')
			OR (ordinal = 4 AND name = 'read_only_inputs')
			OR (ordinal = 5 AND name = 'dedicated_writable_output')
			OR (ordinal = 6 AND name = 'network_default_deny')
			OR (ordinal = 7 AND name = 'exact_network_allowlist')
			OR (ordinal = 8 AND name = 'ephemeral_secret_materialization')
			OR (ordinal = 9 AND name = 'non_root_container_identity')
			OR (ordinal = 10 AND name = 'cpu_memory_pid_limits')
			OR (ordinal = 11 AND name = 'wall_clock_timeout')
			OR (ordinal = 12 AND name = 'graceful_then_forced_kill')
			OR (ordinal = 13 AND name = 'orphan_reconciliation')
			OR (ordinal = 14 AND name = 'output_regular_file_only')
			OR (ordinal = 15 AND name = 'output_symlink_special_rejection')
			OR (ordinal = 16 AND name = 'atomic_output_artifact_commit')),
		CHECK(evidence_state = 'simulated_pass' AND satisfied = 1 AND verified = 0),
		CHECK(length(evidence_digest) = 64 AND evidence_digest = lower(evidence_digest)
			AND evidence_digest NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_backend_evidence_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		evidence_id TEXT NOT NULL UNIQUE,
		preflight_id TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(evidence_id) REFERENCES sandbox_backend_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_output_simulations (
		id TEXT PRIMARY KEY,
		evidence_id TEXT NOT NULL,
		preflight_id TEXT NOT NULL,
		execution_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		status TEXT NOT NULL,
		output_plan_fingerprint TEXT NOT NULL,
		fixture_digest TEXT NOT NULL,
		transaction_digest TEXT NOT NULL,
		expected_slot_count INTEGER NOT NULL,
		staged_output_count INTEGER NOT NULL,
		staged_output_bytes INTEGER NOT NULL,
		fake_artifact_count INTEGER NOT NULL,
		production_artifact_count INTEGER NOT NULL,
		all_or_nothing INTEGER NOT NULL,
		simulation_only INTEGER NOT NULL,
		artifact_commit_authorized INTEGER NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(evidence_id) REFERENCES sandbox_backend_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_output_simulation.v1'
			AND status = 'simulation_committed'),
		CHECK(expected_slot_count BETWEEN 1 AND 18
			AND staged_output_count = expected_slot_count
			AND fake_artifact_count = staged_output_count),
		CHECK(staged_output_bytes BETWEEN 1 AND 16777216),
		CHECK(production_artifact_count = 0 AND all_or_nothing = 1 AND simulation_only = 1
			AND artifact_commit_authorized = 0 AND backend_enabled = 0
			AND execution_authorized = 0),
		CHECK(length(output_plan_fingerprint) = 64 AND output_plan_fingerprint = lower(output_plan_fingerprint)
			AND output_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(fixture_digest) = 64 AND fixture_digest = lower(fixture_digest)
			AND fixture_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(transaction_digest) = 64 AND transaction_digest = lower(transaction_digest)
			AND transaction_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(evidence_id = trim(evidence_id) AND length(evidence_id) BETWEEN 1 AND 256
			AND instr(evidence_id, char(0)) = 0),
		CHECK(preflight_id = trim(preflight_id) AND length(preflight_id) BETWEEN 1 AND 256
			AND instr(preflight_id, char(0)) = 0),
		CHECK(execution_id = trim(execution_id) AND length(execution_id) BETWEEN 1 AND 256
			AND instr(execution_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256
			AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256
			AND instr(workspace_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_output_simulations_run_created
		ON sandbox_output_simulations(run_id, created_at, id);`,
	`CREATE TABLE sandbox_output_simulation_items (
		simulation_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		kind TEXT NOT NULL,
		locator_fingerprint TEXT NOT NULL,
		mime TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		redacted INTEGER NOT NULL,
		fake_artifact_fingerprint TEXT NOT NULL,
		PRIMARY KEY(simulation_id, ordinal),
		UNIQUE(simulation_id, locator_fingerprint),
		FOREIGN KEY(simulation_id) REFERENCES sandbox_output_simulations(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 18 AND kind IN ('stdout', 'stderr', 'file')),
		CHECK(length(locator_fingerprint) = 64 AND locator_fingerprint = lower(locator_fingerprint)
			AND locator_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mime) BETWEEN 1 AND 256 AND mime = trim(mime) AND instr(mime, char(0)) = 0),
		CHECK(length(sha256) = 64 AND sha256 = lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(size_bytes BETWEEN 1 AND 16777216 AND redacted IN (0, 1)),
		CHECK(length(fake_artifact_fingerprint) = 64
			AND fake_artifact_fingerprint = lower(fake_artifact_fingerprint)
			AND fake_artifact_fingerprint NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_output_simulation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		simulation_id TEXT NOT NULL UNIQUE,
		evidence_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(simulation_id) REFERENCES sandbox_output_simulations(id) ON DELETE RESTRICT,
		FOREIGN KEY(evidence_id) REFERENCES sandbox_backend_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64 AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_insert
		BEFORE INSERT ON sandbox_backend_evidence
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_disabled_preflights preflight
			JOIN sandbox_disabled_executions execution ON execution.id = preflight.execution_id
			JOIN sandbox_execution_candidates candidate ON candidate.id = preflight.candidate_id
			JOIN sandbox_manifest_preparations preparation ON preparation.id = preflight.preparation_id
			JOIN sandbox_execution_leases sandbox_lease ON sandbox_lease.execution_id = execution.id
			JOIN runs run ON run.id = preflight.run_id
			JOIN missions mission ON mission.id = preflight.mission_id
			WHERE preflight.id = NEW.preflight_id AND preflight.execution_id = NEW.execution_id
				AND preflight.candidate_id = NEW.candidate_id
				AND preflight.preparation_id = NEW.preparation_id AND preflight.run_id = NEW.run_id
				AND preflight.mission_id = NEW.mission_id AND preflight.workspace_id = NEW.workspace_id
				AND preflight.backend = 'docker' AND preparation.backend = 'docker'
				AND preflight.manifest_fingerprint = NEW.manifest_fingerprint
				AND preflight.authorization_fingerprint = NEW.authorization_fingerprint
				AND preflight.policy_fingerprint = NEW.policy_fingerprint
				AND preflight.mount_binding_fingerprint = NEW.mount_binding_fingerprint
				AND preflight.input_artifact_digest = NEW.input_artifact_digest
				AND preflight.threat_model_fingerprint = NEW.threat_model_fingerprint
				AND preflight.output_plan_fingerprint = NEW.output_plan_fingerprint
				AND preflight.requested_by = NEW.requested_by
				AND preflight.backend_available = 0 AND preflight.backend_enabled = 0
				AND preflight.execution_authorized = 0 AND preflight.artifact_commit_authorized = 0
				AND execution.backend_enabled = 0 AND execution.execution_authorized = 0
				AND execution.backend_started = 0 AND candidate.backend_enabled = 0
				AND candidate.execution_authorized = 0 AND sandbox_lease.status = 'released'
				AND run.mission_id = NEW.mission_id AND mission.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND NOT EXISTS (SELECT 1 FROM sandbox_execution_cancellations cancellation
					WHERE cancellation.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM sandbox_cleanup_results cleanup
					WHERE cleanup.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases run_lease
					WHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'
						AND julianday(run_lease.expires_at) > julianday('now'))
				AND candidate.tokens_used =
					COALESCE((SELECT SUM(node.tokens_used) FROM agent_nodes node WHERE node.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.usage_recorded = 1 THEN call.total_tokens
						ELSE call.reserved_total_tokens END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.execution_millis_used =
					COALESCE((SELECT checkpoint.execution_millis FROM run_supervisor_checkpoints checkpoint
						WHERE checkpoint.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.elapsed_recorded = 1 THEN call.elapsed_millis
						ELSE call.reserved_millis END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.tool_calls_used = COALESCE((SELECT usage.consumed FROM run_tool_usage usage
					WHERE usage.run_id = NEW.run_id), 0)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER), 0) = 0
					OR candidate.tokens_used < CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER))
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER), 0) = 0
					OR candidate.execution_millis_used < CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER) * 1000)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER), 0) = 0
					OR candidate.tool_calls_used < CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER))
				AND (SELECT COUNT(*) FROM sandbox_backend_preflight_checks check_row
					WHERE check_row.preflight_id = preflight.id AND check_row.required = 1
						AND check_row.verified = 0 AND check_row.evidence_state = 'not_probed') = 16
				AND (SELECT COUNT(*) FROM sandbox_execution_inputs input
					JOIN run_artifacts artifact ON artifact.id = input.artifact_id
					WHERE input.execution_id = execution.id AND artifact.run_id = execution.run_id
						AND artifact.session_id = run.session_id AND artifact.workspace_id = execution.workspace_id
						AND artifact.sha256 = input.sha256 AND artifact.size_bytes = input.size_bytes
						AND artifact.mime = input.mime AND artifact.stream = input.stream
						AND artifact.source_id = input.source_id AND artifact.redacted = input.redacted)
					= execution.input_artifact_count
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_item_insert
		BEFORE INSERT ON sandbox_backend_evidence_items
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_backend_evidence evidence
			WHERE evidence.id = NEW.evidence_id AND evidence.trust_class = 'simulation_only'
				AND evidence.production_verified = 0)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence item binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_operation_insert
		BEFORE INSERT ON sandbox_backend_evidence_operations
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_backend_evidence evidence
			WHERE evidence.id = NEW.evidence_id AND evidence.preflight_id = NEW.preflight_id
				AND evidence.run_id = NEW.run_id AND evidence.requested_by = NEW.requested_by
				AND evidence.created_at = NEW.created_at
				AND (SELECT COUNT(*) FROM sandbox_backend_evidence_items item
					WHERE item.evidence_id = evidence.id AND item.satisfied = 1
						AND item.verified = 0 AND item.evidence_state = 'simulated_pass') = 16)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_insert
		BEFORE INSERT ON sandbox_output_simulations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_backend_evidence evidence
			JOIN sandbox_disabled_preflights preflight ON preflight.id = evidence.preflight_id
			JOIN sandbox_disabled_executions execution ON execution.id = evidence.execution_id
			JOIN sandbox_execution_candidates candidate ON candidate.id = evidence.candidate_id
			JOIN sandbox_execution_leases sandbox_lease ON sandbox_lease.execution_id = execution.id
			JOIN runs run ON run.id = evidence.run_id
			WHERE evidence.id = NEW.evidence_id AND evidence.preflight_id = NEW.preflight_id
				AND evidence.execution_id = NEW.execution_id AND evidence.run_id = NEW.run_id
				AND evidence.mission_id = NEW.mission_id AND evidence.workspace_id = NEW.workspace_id
				AND evidence.output_plan_fingerprint = NEW.output_plan_fingerprint
				AND evidence.requested_by = NEW.requested_by
				AND evidence.production_verified = 0 AND evidence.backend_available = 0
				AND evidence.backend_enabled = 0 AND evidence.execution_authorized = 0
				AND evidence.artifact_commit_authorized = 0
				AND preflight.output_slot_count = NEW.expected_slot_count
				AND NEW.staged_output_bytes <= preflight.max_output_bytes
				AND execution.backend_started = 0 AND sandbox_lease.status = 'released'
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND NOT EXISTS (SELECT 1 FROM sandbox_execution_cancellations cancellation
					WHERE cancellation.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM sandbox_cleanup_results cleanup
					WHERE cleanup.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases run_lease
					WHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'
						AND julianday(run_lease.expires_at) > julianday('now'))
				AND candidate.tokens_used =
					COALESCE((SELECT SUM(node.tokens_used) FROM agent_nodes node WHERE node.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.usage_recorded = 1 THEN call.total_tokens
						ELSE call.reserved_total_tokens END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.execution_millis_used =
					COALESCE((SELECT checkpoint.execution_millis FROM run_supervisor_checkpoints checkpoint
						WHERE checkpoint.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.elapsed_recorded = 1 THEN call.elapsed_millis
						ELSE call.reserved_millis END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.tool_calls_used = COALESCE((SELECT usage.consumed FROM run_tool_usage usage
					WHERE usage.run_id = NEW.run_id), 0)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER), 0) = 0
					OR candidate.tokens_used < CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER))
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER), 0) = 0
					OR candidate.execution_millis_used < CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER) * 1000)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER), 0) = 0
					OR candidate.tool_calls_used < CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER))
				AND (SELECT COUNT(*) FROM sandbox_execution_inputs input
					JOIN run_artifacts artifact ON artifact.id = input.artifact_id
					WHERE input.execution_id = execution.id AND artifact.run_id = execution.run_id
						AND artifact.session_id = run.session_id AND artifact.workspace_id = execution.workspace_id
						AND artifact.sha256 = input.sha256 AND artifact.size_bytes = input.size_bytes
						AND artifact.mime = input.mime AND artifact.stream = input.stream
						AND artifact.source_id = input.source_id AND artifact.redacted = input.redacted)
					= execution.input_artifact_count
				AND COALESCE((SELECT SUM(input.size_bytes) FROM sandbox_execution_inputs input
					WHERE input.execution_id = execution.id), 0) = execution.input_artifact_bytes
				AND (SELECT COUNT(*) FROM sandbox_output_simulations existing
					WHERE existing.evidence_id = evidence.id) < 8
		)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_item_insert
		BEFORE INSERT ON sandbox_output_simulation_items
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_output_simulations simulation
			JOIN sandbox_output_export_slots slot ON slot.preflight_id = simulation.preflight_id
				AND slot.ordinal = NEW.ordinal
			WHERE simulation.id = NEW.simulation_id AND slot.kind = NEW.kind
				AND slot.locator_fingerprint = NEW.locator_fingerprint
				AND slot.artifact_commit_authorized = 0)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation item binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_operation_insert
		BEFORE INSERT ON sandbox_output_simulation_operations
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_output_simulations simulation
			WHERE simulation.id = NEW.simulation_id AND simulation.evidence_id = NEW.evidence_id
				AND simulation.run_id = NEW.run_id AND simulation.requested_by = NEW.requested_by
				AND simulation.created_at = NEW.created_at
				AND (SELECT COUNT(*) FROM sandbox_output_simulation_items item
					WHERE item.simulation_id = simulation.id) = simulation.staged_output_count
				AND (SELECT COALESCE(SUM(item.size_bytes), 0) FROM sandbox_output_simulation_items item
					WHERE item.simulation_id = simulation.id) = simulation.staged_output_bytes)
		BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_update_immutable
		BEFORE UPDATE ON sandbox_backend_evidence BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_delete_immutable
		BEFORE DELETE ON sandbox_backend_evidence BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_item_update_immutable
		BEFORE UPDATE ON sandbox_backend_evidence_items BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence item cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_item_delete_immutable
		BEFORE DELETE ON sandbox_backend_evidence_items BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence item cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_operation_update_immutable
		BEFORE UPDATE ON sandbox_backend_evidence_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_backend_evidence_operation_delete_immutable
		BEFORE DELETE ON sandbox_backend_evidence_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox backend evidence operation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_update_immutable
		BEFORE UPDATE ON sandbox_output_simulations BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_delete_immutable
		BEFORE DELETE ON sandbox_output_simulations BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_item_update_immutable
		BEFORE UPDATE ON sandbox_output_simulation_items BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation item cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_item_delete_immutable
		BEFORE DELETE ON sandbox_output_simulation_items BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation item cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_operation_update_immutable
		BEFORE UPDATE ON sandbox_output_simulation_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_output_simulation_operation_delete_immutable
		BEFORE DELETE ON sandbox_output_simulation_operations BEGIN
			SELECT RAISE(ABORT, 'sandbox output simulation operation cannot be deleted');
		END;`,
}

var sandboxDockerObservationStatements = []string{
	`CREATE TABLE sandbox_docker_observations (
		id TEXT PRIMARY KEY,
		evidence_id TEXT NOT NULL,
		output_simulation_id TEXT NOT NULL,
		preflight_id TEXT NOT NULL,
		execution_id TEXT NOT NULL,
		candidate_id TEXT NOT NULL,
		preparation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		mission_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		manifest_fingerprint TEXT NOT NULL,
		authorization_fingerprint TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL,
		mount_binding_fingerprint TEXT NOT NULL,
		input_artifact_digest TEXT NOT NULL,
		threat_model_fingerprint TEXT NOT NULL,
		output_plan_fingerprint TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		source TEXT NOT NULL,
		trust_class TEXT NOT NULL,
		status TEXT NOT NULL,
		endpoint_class TEXT NOT NULL,
		endpoint_fingerprint TEXT NOT NULL,
		binding_fingerprint TEXT NOT NULL,
		image_digest TEXT NOT NULL,
		failure_code TEXT NOT NULL,
		daemon_reachable INTEGER NOT NULL,
		image_inspected INTEGER NOT NULL,
		observation_complete INTEGER NOT NULL,
		production_observed INTEGER NOT NULL,
		production_verified INTEGER NOT NULL,
		backend_available INTEGER NOT NULL,
		backend_enabled INTEGER NOT NULL,
		execution_authorized INTEGER NOT NULL,
		artifact_commit_authorized INTEGER NOT NULL,
		api_version TEXT NOT NULL,
		min_api_version TEXT NOT NULL,
		engine_version TEXT NOT NULL,
		os_type TEXT NOT NULL,
		architecture TEXT NOT NULL,
		rootless INTEGER NOT NULL,
		user_namespace_enabled INTEGER NOT NULL,
		private_mount_state TEXT NOT NULL,
		cgroup_version TEXT NOT NULL,
		ncpu INTEGER NOT NULL,
		memory_bytes INTEGER NOT NULL,
		pids_limit_supported INTEGER NOT NULL,
		image_os_type TEXT NOT NULL,
		image_architecture TEXT NOT NULL,
		image_size_bytes INTEGER NOT NULL,
		image_user_state TEXT NOT NULL,
		daemon_identity_fingerprint TEXT NOT NULL,
		capability_fingerprint TEXT NOT NULL,
		image_fingerprint TEXT NOT NULL,
		observation_fingerprint TEXT NOT NULL,
		item_count INTEGER NOT NULL,
		observed_count INTEGER NOT NULL,
		verified_count INTEGER NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(evidence_id) REFERENCES sandbox_backend_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(output_simulation_id) REFERENCES sandbox_output_simulations(id) ON DELETE RESTRICT,
		FOREIGN KEY(preflight_id) REFERENCES sandbox_disabled_preflights(id) ON DELETE RESTRICT,
		FOREIGN KEY(execution_id) REFERENCES sandbox_disabled_executions(id) ON DELETE RESTRICT,
		FOREIGN KEY(candidate_id) REFERENCES sandbox_execution_candidates(id) ON DELETE RESTRICT,
		FOREIGN KEY(preparation_id) REFERENCES sandbox_manifest_preparations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(mission_id) REFERENCES missions(id) ON DELETE RESTRICT,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'sandbox_docker_observation.v1'
			AND source = 'docker_engine_api_read_only'
			AND trust_class = 'production_observation'),
		CHECK(status IN ('observation_complete', 'daemon_unavailable', 'image_unavailable')),
		CHECK(endpoint_class IN ('local_unix', 'local_npipe')),
		CHECK(failure_code IN ('none', 'connection_failed', 'transport_unsupported',
			'image_not_found')),
		CHECK(daemon_reachable IN (0, 1) AND image_inspected IN (0, 1)
			AND observation_complete IN (0, 1) AND production_observed IN (0, 1)
			AND production_verified = 0 AND backend_available = 0 AND backend_enabled = 0
			AND execution_authorized = 0 AND artifact_commit_authorized = 0
			AND rootless IN (0, 1) AND user_namespace_enabled IN (0, 1)
			AND pids_limit_supported IN (0, 1)),
		CHECK(item_count = 6 AND verified_count = 0 AND observed_count BETWEEN 0 AND 6),
		CHECK(private_mount_state IN ('unknown', 'not_observable_read_only')),
		CHECK(image_user_state IN ('unknown', 'explicit_non_root', 'root_or_empty')),
		CHECK(ncpu BETWEEN 0 AND 1000000 AND memory_bytes >= 0 AND image_size_bytes >= 0),
		CHECK(length(image_digest) = 71 AND substr(image_digest, 1, 7) = 'sha256:'
			AND substr(image_digest, 8) = lower(substr(image_digest, 8))
			AND substr(image_digest, 8) NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(endpoint_fingerprint) = 64
			AND endpoint_fingerprint = lower(endpoint_fingerprint)
			AND endpoint_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(binding_fingerprint) = 64
			AND binding_fingerprint = lower(binding_fingerprint)
			AND binding_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(observation_fingerprint) = 64
			AND observation_fingerprint = lower(observation_fingerprint)
			AND observation_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(manifest_fingerprint) = 64
			AND manifest_fingerprint = lower(manifest_fingerprint)
			AND manifest_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(authorization_fingerprint) = 64
			AND authorization_fingerprint = lower(authorization_fingerprint)
			AND authorization_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(policy_fingerprint) = 64 AND policy_fingerprint = lower(policy_fingerprint)
			AND policy_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(mount_binding_fingerprint) = 64
			AND mount_binding_fingerprint = lower(mount_binding_fingerprint)
			AND mount_binding_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(input_artifact_digest) = 64
			AND input_artifact_digest = lower(input_artifact_digest)
			AND input_artifact_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(threat_model_fingerprint) = 64
			AND threat_model_fingerprint = lower(threat_model_fingerprint)
			AND threat_model_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(output_plan_fingerprint) = 64
			AND output_plan_fingerprint = lower(output_plan_fingerprint)
			AND output_plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK((daemon_identity_fingerprint = '' AND capability_fingerprint = '') OR
			(length(daemon_identity_fingerprint) = 64
				AND daemon_identity_fingerprint = lower(daemon_identity_fingerprint)
				AND daemon_identity_fingerprint NOT GLOB '*[^0-9a-f]*'
				AND length(capability_fingerprint) = 64
				AND capability_fingerprint = lower(capability_fingerprint)
				AND capability_fingerprint NOT GLOB '*[^0-9a-f]*')),
		CHECK(image_fingerprint = '' OR (length(image_fingerprint) = 64
			AND image_fingerprint = lower(image_fingerprint)
			AND image_fingerprint NOT GLOB '*[^0-9a-f]*')),
		CHECK(length(api_version) <= 16 AND length(min_api_version) <= 16
			AND length(engine_version) <= 128 AND length(os_type) <= 32
			AND length(architecture) <= 64 AND length(cgroup_version) <= 32
			AND length(image_os_type) <= 32 AND length(image_architecture) <= 64
			AND instr(api_version, char(0)) = 0 AND instr(min_api_version, char(0)) = 0
			AND instr(engine_version, char(0)) = 0 AND instr(os_type, char(0)) = 0
			AND instr(architecture, char(0)) = 0 AND instr(cgroup_version, char(0)) = 0
			AND instr(image_os_type, char(0)) = 0 AND instr(image_architecture, char(0)) = 0),
		CHECK((status = 'daemon_unavailable' AND daemon_reachable = 0
			AND image_inspected = 0 AND observation_complete = 0 AND production_observed = 0
			AND failure_code IN ('connection_failed', 'transport_unsupported')
			AND api_version = '' AND min_api_version = '' AND engine_version = ''
			AND os_type = '' AND architecture = '' AND rootless = 0
			AND user_namespace_enabled = 0 AND private_mount_state = 'unknown'
			AND cgroup_version = '' AND ncpu = 0 AND memory_bytes = 0
			AND pids_limit_supported = 0 AND image_os_type = ''
			AND image_architecture = '' AND image_size_bytes = 0
			AND image_user_state = 'unknown' AND daemon_identity_fingerprint = ''
			AND capability_fingerprint = '' AND image_fingerprint = '' AND observed_count = 0)
			OR (status = 'image_unavailable' AND daemon_reachable = 1
				AND image_inspected = 0 AND observation_complete = 0 AND production_observed = 0
				AND failure_code = 'image_not_found' AND api_version <> ''
				AND min_api_version <> '' AND engine_version <> '' AND os_type <> ''
				AND architecture <> '' AND private_mount_state = 'not_observable_read_only'
				AND daemon_identity_fingerprint <> '' AND capability_fingerprint <> ''
				AND image_os_type = '' AND image_architecture = '' AND image_size_bytes = 0
				AND image_user_state = 'unknown' AND image_fingerprint = '' AND observed_count = 4)
			OR (status = 'observation_complete' AND daemon_reachable = 1
				AND image_inspected = 1 AND observation_complete = 1 AND production_observed = 1
				AND failure_code = 'none' AND api_version <> '' AND min_api_version <> ''
				AND engine_version <> '' AND os_type <> '' AND architecture <> ''
				AND private_mount_state = 'not_observable_read_only'
				AND daemon_identity_fingerprint <> '' AND capability_fingerprint <> ''
				AND image_os_type <> '' AND image_architecture <> ''
				AND image_user_state IN ('explicit_non_root', 'root_or_empty')
				AND image_fingerprint <> '' AND observed_count = 5)),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(evidence_id = trim(evidence_id) AND length(evidence_id) BETWEEN 1 AND 256
			AND instr(evidence_id, char(0)) = 0),
		CHECK(output_simulation_id = trim(output_simulation_id)
			AND length(output_simulation_id) BETWEEN 1 AND 256
			AND instr(output_simulation_id, char(0)) = 0),
		CHECK(preflight_id = trim(preflight_id) AND length(preflight_id) BETWEEN 1 AND 256
			AND instr(preflight_id, char(0)) = 0),
		CHECK(execution_id = trim(execution_id) AND length(execution_id) BETWEEN 1 AND 256
			AND instr(execution_id, char(0)) = 0),
		CHECK(candidate_id = trim(candidate_id) AND length(candidate_id) BETWEEN 1 AND 256
			AND instr(candidate_id, char(0)) = 0),
		CHECK(preparation_id = trim(preparation_id) AND length(preparation_id) BETWEEN 1 AND 256
			AND instr(preparation_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(mission_id = trim(mission_id) AND length(mission_id) BETWEEN 1 AND 256
			AND instr(mission_id, char(0)) = 0),
		CHECK(workspace_id = trim(workspace_id) AND length(workspace_id) BETWEEN 1 AND 256
			AND instr(workspace_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	);`,
	`CREATE INDEX idx_sandbox_docker_observations_run_created
		ON sandbox_docker_observations(run_id, created_at, id);`,
	`CREATE TABLE sandbox_docker_observation_items (
		observation_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		name TEXT NOT NULL,
		state TEXT NOT NULL,
		evidence_digest TEXT NOT NULL,
		observed INTEGER NOT NULL,
		verified INTEGER NOT NULL,
		PRIMARY KEY(observation_id, ordinal),
		UNIQUE(observation_id, name),
		FOREIGN KEY(observation_id) REFERENCES sandbox_docker_observations(id) ON DELETE RESTRICT,
		CHECK(ordinal BETWEEN 1 AND 6),
		CHECK((ordinal = 1 AND name = 'daemon_identity')
			OR (ordinal = 2 AND name = 'api_capabilities')
			OR (ordinal = 3 AND name = 'rootless_security')
			OR (ordinal = 4 AND name = 'private_mount_support')
			OR (ordinal = 5 AND name = 'platform_limits')
			OR (ordinal = 6 AND name = 'image_inspection')),
		CHECK(state IN ('observed', 'unavailable', 'not_found', 'not_observable_read_only')),
		CHECK(observed IN (0, 1) AND verified = 0),
		CHECK((state = 'observed' AND observed = 1) OR (state <> 'observed' AND observed = 0)),
		CHECK(length(evidence_digest) = 64 AND evidence_digest = lower(evidence_digest)
			AND evidence_digest NOT GLOB '*[^0-9a-f]*')
	) WITHOUT ROWID;`,
	`CREATE TABLE sandbox_docker_observation_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		observation_id TEXT NOT NULL UNIQUE,
		evidence_id TEXT NOT NULL,
		output_simulation_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(observation_id) REFERENCES sandbox_docker_observations(id) ON DELETE RESTRICT,
		FOREIGN KEY(evidence_id) REFERENCES sandbox_backend_evidence(id) ON DELETE RESTRICT,
		FOREIGN KEY(output_simulation_id) REFERENCES sandbox_output_simulations(id) ON DELETE RESTRICT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64
			AND operation_key_digest = lower(operation_key_digest)
			AND operation_key_digest NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(request_fingerprint) = 64
			AND request_fingerprint = lower(request_fingerprint)
			AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(observation_id = trim(observation_id) AND length(observation_id) BETWEEN 1 AND 256
			AND instr(observation_id, char(0)) = 0),
		CHECK(evidence_id = trim(evidence_id) AND length(evidence_id) BETWEEN 1 AND 256
			AND instr(evidence_id, char(0)) = 0),
		CHECK(output_simulation_id = trim(output_simulation_id)
			AND length(output_simulation_id) BETWEEN 1 AND 256
			AND instr(output_simulation_id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256
			AND instr(requested_by, char(0)) = 0)
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_insert
		BEFORE INSERT ON sandbox_docker_observations
		WHEN NOT EXISTS (
			SELECT 1 FROM sandbox_backend_evidence evidence
			JOIN sandbox_output_simulations simulation ON simulation.id = NEW.output_simulation_id
			JOIN sandbox_disabled_preflights preflight ON preflight.id = evidence.preflight_id
			JOIN sandbox_disabled_executions execution ON execution.id = evidence.execution_id
			JOIN sandbox_execution_candidates candidate ON candidate.id = evidence.candidate_id
			JOIN sandbox_manifest_preparations preparation ON preparation.id = evidence.preparation_id
			JOIN sandbox_execution_leases sandbox_lease ON sandbox_lease.execution_id = execution.id
			JOIN runs run ON run.id = evidence.run_id
			JOIN missions mission ON mission.id = evidence.mission_id
			WHERE evidence.id = NEW.evidence_id AND simulation.evidence_id = evidence.id
				AND evidence.preflight_id = NEW.preflight_id
				AND evidence.execution_id = NEW.execution_id
				AND evidence.candidate_id = NEW.candidate_id
				AND evidence.preparation_id = NEW.preparation_id
				AND evidence.run_id = NEW.run_id AND evidence.mission_id = NEW.mission_id
				AND evidence.workspace_id = NEW.workspace_id
				AND simulation.preflight_id = NEW.preflight_id
				AND simulation.execution_id = NEW.execution_id
				AND simulation.run_id = NEW.run_id AND simulation.mission_id = NEW.mission_id
				AND simulation.workspace_id = NEW.workspace_id
				AND evidence.manifest_fingerprint = NEW.manifest_fingerprint
				AND evidence.authorization_fingerprint = NEW.authorization_fingerprint
				AND evidence.policy_fingerprint = NEW.policy_fingerprint
				AND evidence.mount_binding_fingerprint = NEW.mount_binding_fingerprint
				AND evidence.input_artifact_digest = NEW.input_artifact_digest
				AND evidence.threat_model_fingerprint = NEW.threat_model_fingerprint
				AND evidence.output_plan_fingerprint = NEW.output_plan_fingerprint
				AND simulation.output_plan_fingerprint = NEW.output_plan_fingerprint
				AND evidence.image_digest = NEW.image_digest
				AND evidence.requested_by = NEW.requested_by
				AND simulation.requested_by = NEW.requested_by
				AND evidence.source = 'in_memory_fake' AND evidence.trust_class = 'simulation_only'
				AND evidence.status = 'simulation_complete' AND evidence.production_verified = 0
				AND evidence.backend_available = 0 AND evidence.backend_enabled = 0
				AND evidence.execution_authorized = 0 AND evidence.artifact_commit_authorized = 0
				AND simulation.status = 'simulation_committed' AND simulation.simulation_only = 1
				AND simulation.production_artifact_count = 0
				AND simulation.artifact_commit_authorized = 0
				AND simulation.backend_enabled = 0 AND simulation.execution_authorized = 0
				AND preflight.backend = 'docker' AND preparation.backend = 'docker'
				AND preflight.manifest_fingerprint = NEW.manifest_fingerprint
				AND preflight.authorization_fingerprint = NEW.authorization_fingerprint
				AND preflight.policy_fingerprint = NEW.policy_fingerprint
				AND preflight.mount_binding_fingerprint = NEW.mount_binding_fingerprint
				AND preflight.input_artifact_digest = NEW.input_artifact_digest
				AND preflight.threat_model_fingerprint = NEW.threat_model_fingerprint
				AND preflight.output_plan_fingerprint = NEW.output_plan_fingerprint
				AND preflight.requested_by = NEW.requested_by
				AND preflight.backend_available = 0 AND preflight.backend_enabled = 0
				AND preflight.execution_authorized = 0 AND preflight.artifact_commit_authorized = 0
				AND execution.backend_enabled = 0 AND execution.execution_authorized = 0
				AND execution.backend_started = 0 AND candidate.backend_enabled = 0
				AND candidate.execution_authorized = 0 AND sandbox_lease.status = 'released'
				AND run.mission_id = NEW.mission_id AND mission.workspace_id = NEW.workspace_id
				AND run.status IN ('created', 'preparing', 'running', 'waiting_approval', 'paused')
				AND NOT EXISTS (SELECT 1 FROM sandbox_execution_cancellations cancellation
					WHERE cancellation.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM sandbox_cleanup_results cleanup
					WHERE cleanup.execution_id = execution.id)
				AND NOT EXISTS (SELECT 1 FROM run_execution_leases run_lease
					WHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'
						AND julianday(run_lease.expires_at) > julianday('now'))
				AND candidate.tokens_used =
					COALESCE((SELECT SUM(node.tokens_used) FROM agent_nodes node
						WHERE node.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.usage_recorded = 1 THEN call.total_tokens
						ELSE call.reserved_total_tokens END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.execution_millis_used =
					COALESCE((SELECT checkpoint.execution_millis FROM run_supervisor_checkpoints checkpoint
						WHERE checkpoint.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(call.elapsed_millis) FROM specialist_model_calls call
						WHERE call.run_id = NEW.run_id), 0) +
					COALESCE((SELECT SUM(CASE WHEN call.elapsed_recorded = 1 THEN call.elapsed_millis
						ELSE call.reserved_millis END) FROM readonly_fanout_model_calls call
						WHERE call.run_id = NEW.run_id), 0)
				AND candidate.tool_calls_used = COALESCE((SELECT usage.consumed FROM run_tool_usage usage
					WHERE usage.run_id = NEW.run_id), 0)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER), 0) = 0
					OR candidate.tokens_used < CAST(json_extract(run.budget_json, '$.max_tokens') AS INTEGER))
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER), 0) = 0
					OR candidate.execution_millis_used < CAST(json_extract(run.budget_json, '$.timeout_seconds') AS INTEGER) * 1000)
				AND (COALESCE(CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER), 0) = 0
					OR candidate.tool_calls_used < CAST(json_extract(run.budget_json, '$.max_tool_calls') AS INTEGER))
				AND (SELECT COUNT(*) FROM sandbox_execution_inputs input
					JOIN run_artifacts artifact ON artifact.id = input.artifact_id
					WHERE input.execution_id = execution.id AND artifact.run_id = execution.run_id
						AND artifact.session_id = run.session_id
						AND artifact.workspace_id = execution.workspace_id
						AND artifact.sha256 = input.sha256 AND artifact.size_bytes = input.size_bytes
						AND artifact.mime = input.mime AND artifact.stream = input.stream
						AND artifact.source_id = input.source_id AND artifact.redacted = input.redacted)
					= execution.input_artifact_count
				AND COALESCE((SELECT SUM(input.size_bytes) FROM sandbox_execution_inputs input
					WHERE input.execution_id = execution.id), 0) = execution.input_artifact_bytes
				AND (SELECT COUNT(*) FROM sandbox_docker_observations existing
					WHERE existing.output_simulation_id = NEW.output_simulation_id) < 8
		)
		BEGIN
			SELECT RAISE(ABORT, 'Docker observation authority binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_item_insert
		BEFORE INSERT ON sandbox_docker_observation_items
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_docker_observations observation
			WHERE observation.id = NEW.observation_id
				AND ((observation.status = 'daemon_unavailable' AND NEW.state = 'unavailable')
					OR (observation.status = 'image_unavailable'
						AND ((NEW.ordinal IN (1, 2, 3, 5) AND NEW.state = 'observed')
							OR (NEW.ordinal = 4 AND NEW.state = 'not_observable_read_only')
							OR (NEW.ordinal = 6 AND NEW.state = 'not_found')))
					OR (observation.status = 'observation_complete'
						AND ((NEW.ordinal IN (1, 2, 3, 5, 6) AND NEW.state = 'observed')
							OR (NEW.ordinal = 4 AND NEW.state = 'not_observable_read_only')))))
		BEGIN
			SELECT RAISE(ABORT, 'Docker observation item binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_operation_insert
		BEFORE INSERT ON sandbox_docker_observation_operations
		WHEN NOT EXISTS (SELECT 1 FROM sandbox_docker_observations observation
			WHERE observation.id = NEW.observation_id AND observation.evidence_id = NEW.evidence_id
				AND observation.output_simulation_id = NEW.output_simulation_id
				AND observation.run_id = NEW.run_id AND observation.requested_by = NEW.requested_by
				AND observation.created_at = NEW.created_at
				AND (SELECT COUNT(*) FROM sandbox_docker_observation_items item
					WHERE item.observation_id = observation.id) = observation.item_count
				AND (SELECT COALESCE(SUM(item.observed), 0)
					FROM sandbox_docker_observation_items item
					WHERE item.observation_id = observation.id) = observation.observed_count
				AND (SELECT COALESCE(SUM(item.verified), 0)
					FROM sandbox_docker_observation_items item
					WHERE item.observation_id = observation.id) = 0)
		BEGIN
			SELECT RAISE(ABORT, 'Docker observation operation binding is invalid');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_update_immutable
		BEFORE UPDATE ON sandbox_docker_observations BEGIN
			SELECT RAISE(ABORT, 'Docker observation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_delete_immutable
		BEFORE DELETE ON sandbox_docker_observations BEGIN
			SELECT RAISE(ABORT, 'Docker observation cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_item_update_immutable
		BEFORE UPDATE ON sandbox_docker_observation_items BEGIN
			SELECT RAISE(ABORT, 'Docker observation item cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_item_delete_immutable
		BEFORE DELETE ON sandbox_docker_observation_items BEGIN
			SELECT RAISE(ABORT, 'Docker observation item cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_operation_update_immutable
		BEFORE UPDATE ON sandbox_docker_observation_operations BEGIN
			SELECT RAISE(ABORT, 'Docker observation operation cannot be updated');
		END;`,
	`CREATE TRIGGER trg_sandbox_docker_observation_operation_delete_immutable
		BEFORE DELETE ON sandbox_docker_observation_operations BEGIN
			SELECT RAISE(ABORT, 'Docker observation operation cannot be deleted');
		END;`,
}

var controlledCommandExecutionStatements = []string{
	`CREATE TABLE controlled_command_execution_intents (
		request_id TEXT PRIMARY KEY,
		protocol_version TEXT NOT NULL,
		policy_version TEXT NOT NULL,
		plan_id TEXT NOT NULL UNIQUE,
		plan_fingerprint TEXT NOT NULL UNIQUE,
		run_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		interaction_snapshot_id TEXT NOT NULL,
		interaction_revision INTEGER NOT NULL,
		execution_profile_revision INTEGER NOT NULL,
		kind TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id),
		FOREIGN KEY(interaction_snapshot_id)
			REFERENCES run_execution_interaction_snapshots(id),
		CHECK(protocol_version = 'controlled_command_execution_intent.v1'),
		CHECK(policy_version = 'controlled_command_execution_policy.v1'),
		CHECK(interaction_revision > 0),
		CHECK(execution_profile_revision > 0),
		CHECK(kind IN ('git-status', 'git-diff-check', 'go-version',
			'powershell-workspace-list'))
	);`,
	`CREATE INDEX idx_controlled_execution_intents_run_created
		ON controlled_command_execution_intents(run_id, created_at, request_id);`,
	`CREATE TABLE controlled_command_execution_receipts (
		request_id TEXT PRIMARY KEY,
		protocol_version TEXT NOT NULL,
		policy_version TEXT NOT NULL,
		backend TEXT NOT NULL,
		exit_code INTEGER NOT NULL,
		stdout_observed_bytes INTEGER NOT NULL,
		stdout_captured_bytes INTEGER NOT NULL,
		stdout_prefix_sha256 TEXT NOT NULL,
		stdout_truncated INTEGER NOT NULL,
		stderr_observed_bytes INTEGER NOT NULL,
		stderr_captured_bytes INTEGER NOT NULL,
		stderr_prefix_sha256 TEXT NOT NULL,
		stderr_truncated INTEGER NOT NULL,
		started_at TEXT NOT NULL,
		completed_at TEXT NOT NULL,
		timed_out INTEGER NOT NULL,
		cancelled INTEGER NOT NULL,
		output_limit_exceeded INTEGER NOT NULL,
		tree_reaped INTEGER NOT NULL,
		restricted_token INTEGER NOT NULL,
		low_integrity_token INTEGER NOT NULL,
		job_assigned_at_creation INTEGER NOT NULL,
		kill_on_job_close INTEGER NOT NULL,
		active_process_limit INTEGER NOT NULL,
		process_memory_limit INTEGER NOT NULL,
		stdin_closed INTEGER NOT NULL,
		environment_inherited INTEGER NOT NULL,
		network_requested INTEGER NOT NULL,
		persistent_process INTEGER NOT NULL,
		product_execution_enabled INTEGER NOT NULL,
		FOREIGN KEY(request_id)
			REFERENCES controlled_command_execution_intents(request_id)
				ON DELETE CASCADE,
		CHECK(protocol_version = 'controlled_command_execution.v1'),
		CHECK(policy_version = 'controlled_command_execution_policy.v1'),
		CHECK(stdout_observed_bytes BETWEEN 0 AND 67108864
			AND stdout_captured_bytes =
				CASE WHEN stdout_observed_bytes > 65536
					THEN 65536 ELSE stdout_observed_bytes END),
		CHECK(stderr_observed_bytes BETWEEN 0 AND 67108864
			AND stderr_captured_bytes =
				CASE WHEN stderr_observed_bytes > 65536
					THEN 65536 ELSE stderr_observed_bytes END),
		CHECK(length(stdout_prefix_sha256) = 64
			AND stdout_prefix_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(length(stderr_prefix_sha256) = 64
			AND stderr_prefix_sha256 NOT GLOB '*[^0-9a-f]*'),
		CHECK(stdout_truncated IN (0, 1)
			AND stdout_truncated = (stdout_observed_bytes > stdout_captured_bytes)),
		CHECK(stderr_truncated IN (0, 1)
			AND stderr_truncated = (stderr_observed_bytes > stderr_captured_bytes)),
		CHECK(julianday(started_at) IS NOT NULL
			AND julianday(completed_at) IS NOT NULL
			AND julianday(completed_at) >= julianday(started_at)),
		CHECK(timed_out IN (0, 1) AND cancelled IN (0, 1)
			AND NOT (timed_out = 1 AND cancelled = 1)),
		CHECK(output_limit_exceeded IN (0, 1)
			AND (output_limit_exceeded = 0
				OR stdout_observed_bytes = 67108864
				OR stderr_observed_bytes = 67108864)),
		CHECK(tree_reaped = 1 AND restricted_token = 1
			AND low_integrity_token = 1),
		CHECK(job_assigned_at_creation = 1 AND kill_on_job_close = 1),
		CHECK(active_process_limit = 1 AND process_memory_limit = 536870912),
		CHECK(stdin_closed = 1 AND environment_inherited = 0
			AND network_requested = 0 AND persistent_process = 0
			AND product_execution_enabled = 1)
	);`,
	`CREATE TRIGGER trg_controlled_execution_intent_update_immutable
		BEFORE UPDATE ON controlled_command_execution_intents BEGIN
			SELECT RAISE(ABORT, 'controlled command execution intent cannot be updated');
		END;`,
	`CREATE TRIGGER trg_controlled_execution_intent_delete_immutable
		BEFORE DELETE ON controlled_command_execution_intents BEGIN
			SELECT RAISE(ABORT, 'controlled command execution intent cannot be deleted');
		END;`,
	`CREATE TRIGGER trg_controlled_execution_receipt_update_immutable
		BEFORE UPDATE ON controlled_command_execution_receipts BEGIN
			SELECT RAISE(ABORT, 'controlled command execution receipt cannot be updated');
		END;`,
	`CREATE TRIGGER trg_controlled_execution_receipt_delete_immutable
		BEFORE DELETE ON controlled_command_execution_receipts BEGIN
			SELECT RAISE(ABORT, 'controlled command execution receipt cannot be deleted');
		END;`,
}

func (s *SQLiteStore) applyMigrations(ctx context.Context, migrations []migration) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite store is not open")
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL
	);`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.loadAppliedMigrations(ctx)
	if err != nil {
		return err
	}
	if err := validateMigrationPlan(migrations, applied); err != nil {
		return err
	}
	for _, item := range migrations {
		if _, ok := applied[item.Version]; ok {
			continue
		}
		if err := s.applyMigration(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) applyMigration(ctx context.Context, item migration) error {
	if item.Version <= 0 || strings.TrimSpace(item.Name) == "" || len(item.Statements) == 0 {
		return fmt.Errorf("invalid migration version=%d name=%q", item.Version, item.Name)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration %d connection: %w", item.Version, err)
	}
	defer conn.Close()
	foreignKeysDisabled := false
	if item.DisableForeignKeys {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF;`); err != nil {
			return fmt.Errorf("disable foreign keys for migration %d: %w", item.Version, err)
		}
		var enabled int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys;`).Scan(&enabled); err != nil || enabled != 0 {
			return fmt.Errorf("disable foreign keys for migration %d: state=%d err=%v",
				item.Version, enabled, err)
		}
		foreignKeysDisabled = true
		defer func() {
			if foreignKeysDisabled {
				_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON;`)
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", item.Version, err)
	}
	defer func() { _ = tx.Rollback() }()
	var recorded appliedMigration
	err = tx.QueryRowContext(ctx, `SELECT name, checksum FROM schema_migrations WHERE version = ?`,
		item.Version).Scan(&recorded.Name, &recorded.Checksum)
	if err == nil {
		if recorded.Name != item.Name || !acceptedMigrationChecksum(item, recorded.Checksum) {
			return fmt.Errorf("migration %d checksum or name mismatch", item.Version)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("recheck migration %d: %w", item.Version, err)
	}
	for index, stmt := range item.Statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration %d %q statement %d: %w", item.Version, item.Name, index+1, err)
		}
	}
	if item.DisableForeignKeys {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check;`)
		if err != nil {
			return fmt.Errorf("foreign-key check migration %d: %w", item.Version, err)
		}
		if rows.Next() {
			var table string
			var rowID sql.NullInt64
			var parent string
			var foreignKeyID int
			scanErr := rows.Scan(&table, &rowID, &parent, &foreignKeyID)
			_ = rows.Close()
			if scanErr != nil {
				return fmt.Errorf("scan foreign-key violation migration %d: %w",
					item.Version, scanErr)
			}
			return fmt.Errorf("migration %d introduced foreign-key violation table=%q row=%v parent=%q key=%d",
				item.Version, table, rowID, parent, foreignKeyID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read foreign-key check migration %d: %w", item.Version, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close foreign-key check migration %d: %w", item.Version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at)
		VALUES (?, ?, ?, ?)`, item.Version, item.Name, migrationChecksum(item), ts(time.Now().UTC())); err != nil {
		return fmt.Errorf("record migration %d: %w", item.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", item.Version, err)
	}
	if item.DisableForeignKeys {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
			return fmt.Errorf("restore foreign keys after migration %d: %w", item.Version, err)
		}
		var enabled int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys;`).Scan(&enabled); err != nil || enabled != 1 {
			return fmt.Errorf("restore foreign keys after migration %d: state=%d err=%v",
				item.Version, enabled, err)
		}
		foreignKeysDisabled = false
	}
	return nil
}

func (s *SQLiteStore) loadAppliedMigrations(ctx context.Context) (map[int]appliedMigration, error) {
	return loadAppliedMigrationsFrom(ctx, s.db)
}

func loadAppliedMigrationsFrom(ctx context.Context, queryer sqliteQueryer) (map[int]appliedMigration, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[int]appliedMigration{}
	for rows.Next() {
		var version int
		var item appliedMigration
		if err := rows.Scan(&version, &item.Name, &item.Checksum); err != nil {
			return nil, err
		}
		applied[version] = item
	}
	return applied, rows.Err()
}

func validateMigrationPlan(migrations []migration, applied map[int]appliedMigration) error {
	if len(migrations) != LatestSchemaVersion {
		return fmt.Errorf("latest schema version is %d but migration plan has %d entries", LatestSchemaVersion, len(migrations))
	}
	known := make(map[int]migration, len(migrations))
	for index, item := range migrations {
		expectedVersion := index + 1
		if item.Version != expectedVersion {
			return fmt.Errorf("migration plan must be contiguous: expected version %d, got %d", expectedVersion, item.Version)
		}
		if _, exists := known[item.Version]; exists {
			return fmt.Errorf("duplicate migration version %d", item.Version)
		}
		known[item.Version] = item
	}
	for version, recorded := range applied {
		item, ok := known[version]
		if !ok {
			return fmt.Errorf("database schema version %d is newer or unknown", version)
		}
		if recorded.Name != item.Name || !acceptedMigrationChecksum(item, recorded.Checksum) {
			return fmt.Errorf("migration %d checksum or name mismatch", version)
		}
	}
	for version := 1; version <= len(migrations); version++ {
		if _, ok := applied[version]; ok {
			continue
		}
		for later := version + 1; later <= len(migrations); later++ {
			if _, ok := applied[later]; ok {
				return fmt.Errorf("migration history has a gap at version %d", version)
			}
		}
		break
	}
	return nil
}

const (
	legacyWindowsPreviewMigration30Checksum  = "bef87a078e337c7c78f020b0470b5d3c8a889a42c3f5993ef62f16e761018ae7"
	legacyWindowsPreviewMigration97Checksum  = "844427411efc98247ebf521badb98a8c96e3a17b8aa5c7d429b7192d4dcad83b"
	legacyCommandRuntimeMigration131Checksum = "1b00f96d67981ef589988355b002410eb40102ed7459f37ebdec7fc79020c249"
	legacyWindowsPreviewMigration136Checksum = "5cccad921f47d44ac37f2206e91b355d55ce221d6c9c61e25e7fc08f1cbb6dbd"
)

func acceptedMigrationChecksum(item migration, recorded string) bool {
	if recorded == migrationChecksum(item) {
		return true
	}
	// Windows preview profiles recorded these exact released histories.
	// Migration v125 repairs the legacy v97 cleanup trigger after validation;
	// v138 repairs the two Supervisor authority triggers absent from the pinned
	// intermediate v136 history. The compatibility path remains exact and cannot
	// be widened at runtime.
	// The v131 repair changes only the projection of previously empty command
	// authority; databases that completed the released v131 have the same schema.
	return (item.Version == 30 && recorded == legacyWindowsPreviewMigration30Checksum) ||
		(item.Version == 97 && recorded == legacyWindowsPreviewMigration97Checksum) ||
		(item.Version == 131 && recorded == legacyCommandRuntimeMigration131Checksum) ||
		(item.Version == 136 && recorded == legacyWindowsPreviewMigration136Checksum)
}

func migrationChecksum(item migration) string {
	payload := strings.Join(item.Statements, "\x00")
	if item.DisableForeignKeys {
		payload += "\x00migration.foreign_keys=off"
	}
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func (s *SQLiteStore) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}
