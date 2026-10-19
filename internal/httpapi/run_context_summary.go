package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/redact"
)

// These are durable context records, not a reconstruction of a provider request.
// Reading them never compacts history, refreshes instructions, or grants authority.
type RunContextSummaryView struct {
	RunID                  string                       `json:"run_id"`
	ThreadID               string                       `json:"thread_id"`
	SessionID              string                       `json:"session_id"`
	WorkspaceID            string                       `json:"workspace_id"`
	CapabilityGrant        bool                         `json:"capability_grant"`
	CurrentSummary         *RunStoredContextSummaryView `json:"current_summary,omitempty"`
	InheritedContext       *RunInheritedContextView     `json:"inherited_context,omitempty"`
	Diagnostics            *RunContextDiagnosticsView   `json:"diagnostics,omitempty"`
	DiagnosticsUnavailable bool                         `json:"diagnostics_unavailable,omitempty"`
}

type RunStoredContextSummaryView struct {
	ID                    int64     `json:"id"`
	PreviousSummaryID     int64     `json:"previous_summary_id"`
	ProtocolVersion       string    `json:"protocol_version"`
	Content               string    `json:"content"`
	ContentSHA256         string    `json:"content_sha256"`
	ContentRedacted       bool      `json:"content_redacted"`
	ContentTruncated      bool      `json:"content_truncated"`
	CompactedMessageCount int       `json:"compacted_message_count"`
	SourceMessageCount    int       `json:"source_message_count"`
	PreservedMessageCount int       `json:"preserved_message_count"`
	CreatedAt             time.Time `json:"created_at"`
}

type RunInheritedContextView struct {
	SourceRunID          string                                 `json:"source_run_id"`
	SourceSessionID      string                                 `json:"source_session_id"`
	Fingerprint          string                                 `json:"fingerprint"`
	SummaryID            int64                                  `json:"summary_id"`
	SummaryContent       string                                 `json:"summary_content"`
	SummaryContentSHA256 string                                 `json:"summary_content_sha256"`
	ContentRedacted      bool                                   `json:"content_redacted"`
	ContentTruncated     bool                                   `json:"content_truncated"`
	RecentMessageCount   int                                    `json:"recent_message_count"`
	Memories             []contextmgr.ContinuityMemoryReference `json:"memories"`
}

type runContextSummaryReader interface {
	LatestContextSummary(context.Context, string) (contextmgr.Summary, bool, error)
}

func (a *API) runContextSummary(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	reader, ok := a.store.(runContextSummaryReader)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "stored context summary reader is unavailable")
	}
	ctx := request.Context()
	run, err := a.store.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	thread, err := a.store.GetThreadByRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	sess, err := a.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return nil, nil, err
	}
	mission, err := a.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return nil, nil, err
	}
	if run.ID != runID || sess.ID != run.SessionID || mission.ID != run.MissionID ||
		sess.WorkspaceID != mission.WorkspaceID {
		return nil, nil, apperror.New(apperror.CodeConflict, "stored context Run, Session, and Workspace binding differs")
	}
	view := RunContextSummaryView{RunID: run.ID, ThreadID: thread.ID,
		SessionID: sess.ID, WorkspaceID: mission.WorkspaceID}
	if diagnostics, ok := a.store.(runContextDiagnosticReader); ok {
		view.Diagnostics, err = readRunContextDiagnostics(ctx, diagnostics, run)
		if err != nil {
			view.Diagnostics = nil
			view.DiagnosticsUnavailable = true
		}
	}
	summary, found, err := reader.LatestContextSummary(ctx, sess.ID)
	if err != nil {
		return nil, nil, err
	}
	if found {
		if err := contextmgr.ValidateStoredSummary(summary); err != nil {
			return nil, nil, apperror.Wrap(apperror.CodeInternal, "stored context summary is invalid", err)
		}
		if summary.ID <= 0 || summary.TaskID != sess.ID ||
			(summary.WorkspaceID != "" && summary.WorkspaceID != mission.WorkspaceID) {
			return nil, nil, apperror.New(apperror.CodeConflict, "stored context summary escaped its requested Session")
		}
		digest := contextSummaryDigest(summary.Content)
		if summary.ContentSHA256 != "" && summary.ContentSHA256 != digest {
			return nil, nil, apperror.New(apperror.CodeConflict, "stored context summary content hash differs")
		}
		content, redacted, truncated := publicContextSummary(summary.Content)
		view.CurrentSummary = &RunStoredContextSummaryView{ID: summary.ID,
			PreviousSummaryID: summary.PreviousSummaryID, ProtocolVersion: summary.ProtocolVersion,
			Content: content, ContentSHA256: digest, ContentRedacted: redacted, ContentTruncated: truncated,
			CompactedMessageCount: summary.CompactedMessageCount, SourceMessageCount: summary.SourceMessageCount,
			PreservedMessageCount: summary.PreservedMessageCount, CreatedAt: summary.CreatedAt}
	}
	if len(run.Config.ContinuityContext) > 0 {
		var inherited contextmgr.ContinuitySnapshot
		if err := json.Unmarshal(run.Config.ContinuityContext, &inherited); err != nil {
			return nil, nil, apperror.Wrap(apperror.CodeInternal, "inherited context is invalid", err)
		}
		if err := inherited.Validate(); err != nil {
			return nil, nil, apperror.Wrap(apperror.CodeInternal, "inherited context is invalid", err)
		}
		if inherited.WorkspaceID != mission.WorkspaceID || inherited.Fingerprint != run.Config.ContinuityContextFingerprint {
			return nil, nil, apperror.New(apperror.CodeConflict, "inherited context binding differs")
		}
		content, redacted, truncated := publicContextSummary(inherited.SummaryContent)
		view.InheritedContext = &RunInheritedContextView{SourceRunID: inherited.SourceRunID,
			SourceSessionID: inherited.SourceSessionID, Fingerprint: inherited.Fingerprint,
			SummaryID: inherited.SummaryID, SummaryContent: content,
			SummaryContentSHA256: inherited.SummaryContentSHA256, ContentRedacted: redacted, ContentTruncated: truncated,
			RecentMessageCount: len(inherited.RecentMessages),
			Memories:           append([]contextmgr.ContinuityMemoryReference{}, inherited.Memories...)}
	}
	return view, nil, nil
}

func contextSummaryDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func publicContextSummary(content string) (string, bool, bool) {
	public := redact.String(content)
	redacted := public != content
	const maxBytes = 16 * 1024
	if len(public) <= maxBytes {
		return public, redacted, false
	}
	end := maxBytes
	for !utf8.RuneStart(public[end]) {
		end--
	}
	return public[:end], redacted, true
}
