package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

const maxWorkerHistorySnapshotSources = 4096

var workerHistorySnapshotStatements = []string{
	`CREATE TABLE worker_parent_history_snapshots (
 agent_id TEXT PRIMARY KEY REFERENCES agent_nodes(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 parent_agent_id TEXT NOT NULL REFERENCES agent_nodes(id),
 sources_json TEXT NOT NULL CHECK(json_valid(sources_json)),
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64),
 created_at TEXT NOT NULL
);`,
	`CREATE TRIGGER trg_worker_parent_history_snapshot_immutable BEFORE UPDATE ON worker_parent_history_snapshots
 BEGIN SELECT RAISE(ABORT,'Worker parent history snapshot is immutable'); END;`,
}

type workerHistoryParentSource struct {
	Record domain.HistoryRecord `json:"record"`
	Parts  map[string]string    `json:"part_sha256"`
}

// Capture inside admission, after creating the child and before committing it.
// Replaying an admission does not invoke this function or add later sources.
func (s *SQLiteStore) captureWorkerParentHistoryTx(ctx context.Context, tx *sql.Tx, child domain.AgentNode) error {
	if child.Worker == nil || child.Worker.EffectiveHistoryScope() != domain.WorkerHistoryParentSnapshot {
		return nil
	}
	scope, err := s.historyScope(ctx, tx, child.RunID)
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition, "parent_snapshot requires the parent's current open Thread history", err)
	}
	sources := []workerHistoryParentSource{}
	seen := map[string]bool{}
	request := domain.HistorySearchRequest{Limit: domain.HistorySearchMaxResults}
	for {
		page, err := s.searchThreadHistory(ctx, tx, child.RunID, request)
		if err != nil {
			return err
		}
		for _, record := range page.Records {
			// Root recall also searches user/project memories outside this Thread.
			// A parent snapshot grants only its conversation lineage. Memory text
			// already delivered in a parent message or receipt remains in that source.
			if record.Kind == "context_memory" {
				continue
			}
			if seen[record.SourceID] {
				continue
			}
			if len(sources) == maxWorkerHistorySnapshotSources {
				return apperror.New(apperror.CodeResourceExhausted, "parent history snapshot exceeds 4096 sources; delegate a smaller context instead")
			}
			record, parts, err := readHistorySource(ctx, tx, scope, record.SourceID)
			if err != nil {
				return err
			}
			digests := make(map[string]string, len(parts))
			for part, content := range parts {
				digests[part] = session.ContentSHA256(content)
			}
			sources = append(sources, workerHistoryParentSource{Record: record, Parts: digests})
			seen[record.SourceID] = true
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == request.Cursor {
			return errors.New("parent history snapshot did not advance")
		}
		request.Cursor = page.NextCursor
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	if len(raw) > 8*1024*1024 {
		return apperror.New(apperror.CodeResourceExhausted, "parent history snapshot source metadata exceeds 8 MiB")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_parent_history_snapshots
 (agent_id,run_id,parent_agent_id,sources_json,content_sha256,created_at) VALUES(?,?,?,?,?,?)`,
		child.ID, child.RunID, child.ParentID, string(raw), session.ContentSHA256(string(raw)), ts(time.Now().UTC()))
	return err
}

func loadWorkerParentHistoryTx(ctx context.Context, tx *sql.Tx, child domain.AgentNode) ([]workerHistoryParentSource, error) {
	if child.Worker == nil || child.Worker.EffectiveHistoryScope() != domain.WorkerHistoryParentSnapshot {
		return nil, nil
	}
	var raw, digest string
	err := tx.QueryRowContext(ctx, `SELECT sources_json,content_sha256 FROM worker_parent_history_snapshots
 WHERE agent_id=? AND run_id=? AND parent_agent_id=?`, child.ID, child.RunID, child.ParentID).Scan(&raw, &digest)
	if err != nil {
		return nil, err
	}
	var sources []workerHistoryParentSource
	if session.ContentSHA256(raw) != digest || json.Unmarshal([]byte(raw), &sources) != nil || len(sources) > maxWorkerHistorySnapshotSources {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "worker parent history snapshot integrity changed")
	}
	return sources, nil
}

func (s *SQLiteStore) readWorkerParentHistorySource(ctx context.Context, tx *sql.Tx,
	child domain.AgentNode, source workerHistoryParentSource,
) (domain.HistoryRecord, map[string]string, error) {
	scope, err := s.historyScope(ctx, tx, child.RunID)
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	record, parts, err := readHistorySource(ctx, tx, scope, source.Record.SourceID)
	if err != nil {
		return record, nil, err
	}
	if len(parts) != len(source.Parts) {
		return record, nil, apperror.New(apperror.CodeConflict, "delegated parent history source changed")
	}
	for part, content := range parts {
		if source.Parts[part] != session.ContentSHA256(content) {
			return record, nil, apperror.New(apperror.CodeConflict, "delegated parent history content changed")
		}
	}
	return record, parts, nil
}
