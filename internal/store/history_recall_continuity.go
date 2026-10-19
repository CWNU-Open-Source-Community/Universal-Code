package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

// A continuity reference identifies the Run holding the original pinned JSON,
// not a mutable "latest" summary or a foreign source mentioned inside it.
type historyContinuityRef struct {
	Run         string `json:"r"`
	Fingerprint string `json:"f"`
}

func readHistorySummary(ctx context.Context, q historyQueryer, scope historyScope, id int64) (domain.HistoryRecord, map[string]string, error) {
	var summary contextmgr.Summary
	var created string
	err := q.QueryRowContext(ctx, `SELECT s.id,s.task_id,COALESCE(s.workspace_id,''),s.protocol_version,
		COALESCE(s.previous_summary_id,0),s.content,s.content_sha256,s.compacted_message_count,
		s.source_message_count,s.preserved_message_count,s.token_estimate,s.created_at
		FROM context_summaries s JOIN thread_runs b ON b.session_id=s.task_id
		WHERE s.id=? AND b.thread_id=?`, id, scope.threadID).
		Scan(&summary.ID, &summary.TaskID, &summary.WorkspaceID, &summary.ProtocolVersion,
			&summary.PreviousSummaryID, &summary.Content, &summary.ContentSHA256, &summary.CompactedMessageCount,
			&summary.SourceMessageCount, &summary.PreservedMessageCount, &summary.TokenEstimate, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	runID, ok := scope.sessions[summary.TaskID]
	if !ok || summary.WorkspaceID != scope.runWorkspaces[runID] {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	summary.CreatedAt = parseTS(created)
	if err := contextmgr.ValidateStoredSummary(summary); err != nil {
		return domain.HistoryRecord{}, nil, apperror.Wrap(apperror.CodeFailedPrecondition, "stored history summary is invalid", err)
	}
	digest := session.ContentSHA256(summary.Content)
	// Legacy rows may have no persisted digest. Compute the digest of their
	// unchanged original bytes for paging without filling or rewriting the row.
	if summary.ContentSHA256 != "" && summary.ContentSHA256 != digest {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeFailedPrecondition, "stored history summary digest does not match its original content")
	}
	if summary.PreviousSummaryID != 0 {
		var bound bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM context_summaries
			WHERE id=? AND id<? AND task_id=? AND COALESCE(workspace_id,'')=?)`,
			summary.PreviousSummaryID, summary.ID, summary.TaskID, summary.WorkspaceID).Scan(&bound); err != nil {
			return domain.HistoryRecord{}, nil, err
		}
		if !bound {
			return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeFailedPrecondition, "stored history summary predecessor does not match its Session")
		}
	}
	record, err := safeHistoryMetadata(domain.HistoryRecord{
		SourceID: "summary:" + strconv.FormatInt(id, 10), Kind: "summary", RunID: runID,
		SessionID: summary.TaskID, SummaryID: id, PreviousSummaryID: summary.PreviousSummaryID,
		ProvenanceVersion: summary.ProtocolVersion, SourceKind: "compacted_transcript",
		SourceRef: summary.TaskID, ContentSHA256: digest, CreatedAt: created,
	})
	return record, map[string]string{"content": summary.Content}, err
}

func readHistoryContinuity(ctx context.Context, q historyQueryer, scope historyScope, where, expectedFingerprint string, args ...any) (domain.HistoryRecord, map[string]string, error) {
	args = append(args, scope.threadID)
	var holderRun, holderSession, rawConfig, created string
	err := q.QueryRowContext(ctx, `SELECT r.id,r.session_id,r.config_json,r.created_at
		FROM runs r JOIN thread_runs b ON b.run_id=r.id WHERE `+where+` AND b.thread_id=?`, args...).
		Scan(&holderRun, &holderSession, &rawConfig, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if err != nil {
		return domain.HistoryRecord{}, nil, err
	}
	if scope.sessions[holderSession] != holderRun {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	var config struct {
		Snapshot    json.RawMessage `json:"continuity_context"`
		Fingerprint string          `json:"continuity_context_fingerprint"`
	}
	if json.Unmarshal([]byte(rawConfig), &config) != nil || len(config.Snapshot) > 512*1024 {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeFailedPrecondition, "stored continuity configuration is invalid")
	}
	if len(config.Snapshot) == 0 || string(config.Snapshot) == "null" {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	var snapshot contextmgr.ContinuitySnapshot
	if json.Unmarshal(config.Snapshot, &snapshot) != nil {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeFailedPrecondition, "stored continuity snapshot cannot be decoded")
	}
	// Merely mentioning a Run in a valid fork snapshot does not extend scope.
	// Both its holder and historical source must be in this exact Thread chain.
	if scope.sessions[snapshot.SourceSessionID] != snapshot.SourceRunID || scope.ordinals[snapshot.SourceRunID] == 0 {
		return domain.HistoryRecord{}, nil, historyNotFound()
	}
	if scope.ordinals[snapshot.SourceRunID] > scope.ordinals[holderRun] || snapshot.WorkspaceID != scope.runWorkspaces[holderRun] {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeFailedPrecondition, "stored continuity source ordering or Workspace binding is invalid")
	}
	if err := snapshot.Validate(); err != nil || config.Fingerprint != snapshot.Fingerprint {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeFailedPrecondition, "stored continuity snapshot fingerprint or no-authority binding is invalid")
	}
	if expectedFingerprint != "" && expectedFingerprint != snapshot.Fingerprint {
		return domain.HistoryRecord{}, nil, apperror.New(apperror.CodeConflict, "history continuity fingerprint does not match the requested source")
	}
	// Keep the original RawMessage bytes. Re-marshalling the decoded snapshot
	// would change whitespace/order and is not the original content hash. Its
	// semantic fingerprint also intentionally differs from that byte digest.
	content := string(config.Snapshot)
	record, err := safeHistoryMetadata(domain.HistoryRecord{
		SourceID: "continuity:" + encodeHistoryToken(historyContinuityRef{Run: holderRun, Fingerprint: snapshot.Fingerprint}),
		Kind:     "continuity", RunID: holderRun, SessionID: holderSession,
		SummaryID: snapshot.SummaryID, ContinuityFingerprint: snapshot.Fingerprint,
		ProvenanceVersion: snapshot.ProtocolVersion, SourceKind: "continuity_context",
		SourceRef: snapshot.SourceRunID, ContentSHA256: session.ContentSHA256(content), CreatedAt: created,
	})
	return record, map[string]string{"content": content, "summary": snapshot.SummaryContent}, err
}
