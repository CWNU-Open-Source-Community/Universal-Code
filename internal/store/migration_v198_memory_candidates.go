package store

var memoryCandidateStatements = []string{
	`CREATE TABLE context_memory_candidates (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		note_id TEXT NOT NULL,
		status TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		version INTEGER NOT NULL,
		payload_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
		UNIQUE(workspace_id, note_id),
		CHECK(status IN ('pending','confirmed','rejected','stale')),
		CHECK(length(fingerprint)=64 AND fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK((status='pending' AND version=1) OR (status<>'pending' AND version=2)),
		CHECK(json_valid(payload_json) AND json_type(payload_json)='object'
			AND json_extract(payload_json,'$.protocol_version')='context_memory_candidate.v1'
			AND json_extract(payload_json,'$.id')=id
			AND json_extract(payload_json,'$.workspace_id')=workspace_id
			AND json_extract(payload_json,'$.source.note_id')=note_id
			AND json_extract(payload_json,'$.status')=status
			AND json_extract(payload_json,'$.fingerprint')=fingerprint
			AND json_extract(payload_json,'$.version')=version),
		CHECK(status='pending' OR (COALESCE(json_extract(payload_json,'$.title'),'')=''
			AND COALESCE(json_extract(payload_json,'$.content'),'')=''
			AND COALESCE(json_array_length(payload_json,'$.source.references'),0)=0
			AND COALESCE(json_array_length(payload_json,'$.source.evidence_ids'),0)=0)),
		CHECK(julianday(created_at) IS NOT NULL AND julianday(updated_at)>=julianday(created_at))
	);`,
	`CREATE INDEX idx_memory_candidates_workspace_status_created
		ON context_memory_candidates(workspace_id,status,created_at DESC,id);`,
	`CREATE TRIGGER trg_memory_candidates_resolve_only BEFORE UPDATE ON context_memory_candidates
		WHEN OLD.status<>'pending' OR NEW.status='pending' OR NEW.version<>OLD.version+1
			OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.note_id<>OLD.note_id
			OR NEW.fingerprint<>OLD.fingerprint OR NEW.created_at<>OLD.created_at
		BEGIN SELECT RAISE(ABORT,'memory candidate identity and resolution are immutable'); END;`,
	`CREATE TRIGGER trg_memory_candidates_no_delete BEFORE DELETE ON context_memory_candidates
		BEGIN SELECT RAISE(ABORT,'memory candidate resolution tombstones cannot be deleted'); END;`,
}
