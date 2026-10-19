package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

const (
	ThreadChangeSummaryVersion = "thread_change_summary.v1"
	MaxThreadChangeEdits       = 500
	MaxThreadChangeDiffBytes   = 512 * 1024
)

type ThreadChangeSummary struct {
	Version         string                  `json:"version"`
	ThreadID        string                  `json:"thread_id"`
	SourceEventID   string                  `json:"source_event_id"`
	RunID           string                  `json:"run_id"`
	ChangeScope     string                  `json:"change_scope"`
	Files           []ThreadChangeFile      `json:"files"`
	FileCount       int                     `json:"file_count"`
	Additions       int                     `json:"additions"`
	Deletions       int                     `json:"deletions"`
	CountsAvailable bool                    `json:"counts_available"`
	Partial         bool                    `json:"partial"`
	Reasons         []string                `json:"reasons"`
	Undo            *ThreadChangeUndoResult `json:"undo,omitempty"`
}

type ThreadChangeFile struct {
	WorkspaceID     string   `json:"workspace_id"`
	Path            string   `json:"path"`
	DestinationPath string   `json:"destination_path,omitempty"`
	Operation       string   `json:"operation"`
	Additions       int      `json:"additions"`
	Deletions       int      `json:"deletions"`
	CountsAvailable bool     `json:"counts_available"`
	Diff            string   `json:"diff"`
	DiffTruncated   bool     `json:"diff_truncated"`
	EditIDs         []string `json:"edit_ids"`
	Reason          string   `json:"reason,omitempty"`
}

type ThreadChangeStore interface {
	ListThreadReplyFileEditEvents(context.Context, string, string, int) (events.Event, []events.Event, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetFileEdit(context.Context, string) (fileedit.Edit, error)
	FileEditWorkspaceBelongsToRun(context.Context, string, string, string) (bool, error)
}

type ThreadChangeService struct{ store ThreadChangeStore }

func NewThreadChangeService(store ThreadChangeStore) *ThreadChangeService {
	return &ThreadChangeService{store: store}
}

// Summary only observes persisted FileEdit applications before one exact reply.
// It never reads the current filesystem or attributes shell/user writes to AI.
func (s *ThreadChangeService) Summary(ctx context.Context, threadID, sourceEventID string) (ThreadChangeSummary, error) {
	if !validControlIdentity(threadID) || !validControlIdentity(sourceEventID) {
		return ThreadChangeSummary{}, apperror.New(apperror.CodeInvalidArgument, "Thread and reply event identities are required")
	}
	source, facts, err := s.store.ListThreadReplyFileEditEvents(ctx, threadID, sourceEventID, MaxThreadChangeEdits+1)
	if err != nil {
		return ThreadChangeSummary{}, apperror.Normalize(err)
	}
	var reply struct {
		Role       string `json:"role"`
		SourceKind string `json:"source_kind"`
		SessionID  string `json:"session_id"`
	}
	if source.EventID != sourceEventID || source.Type != events.SessionMessageEvent || source.Source != "session_store" ||
		json.Unmarshal([]byte(source.PayloadJSON), &reply) != nil || reply.Role != "assistant" || reply.SourceKind != session.SourceModelResponse {
		return ThreadChangeSummary{}, threadChangeBindingError()
	}
	run, err := s.store.GetRun(ctx, source.RunID)
	if err != nil {
		return ThreadChangeSummary{}, apperror.Normalize(err)
	}
	if run.ID != source.RunID || run.SessionID != reply.SessionID || run.MissionID != source.MissionID {
		return ThreadChangeSummary{}, threadChangeBindingError()
	}
	result := ThreadChangeSummary{Version: ThreadChangeSummaryVersion, ThreadID: threadID,
		SourceEventID: sourceEventID, RunID: run.ID, ChangeScope: "recorded_file_edits",
		Files: []ThreadChangeFile{}, CountsAvailable: true, Reasons: []string{}}
	if len(facts) > MaxThreadChangeEdits {
		result.CountsAvailable = false
		result.markPartial("edit_limit")
		return result, nil
	}
	// Current paths follow moves; the ordered list retains first-application order.
	current := map[string]*threadChangeFileState{}
	ordered := []*threadChangeFileState{}
	seen := map[string]bool{}
	var previousSequence int64
	for _, fact := range facts {
		if fact.RunID != run.ID || fact.Type != events.FileEditAppliedEvent || fact.Source != "fileedit_store" ||
			fact.Sequence <= previousSequence || fact.Sequence >= source.Sequence || !validControlIdentity(fact.SubjectID) {
			return ThreadChangeSummary{}, threadChangeBindingError()
		}
		previousSequence = fact.Sequence
		if seen[fact.SubjectID] {
			continue
		}
		seen[fact.SubjectID] = true
		edit, err := s.store.GetFileEdit(ctx, fact.SubjectID)
		if err != nil {
			return ThreadChangeSummary{}, apperror.Normalize(err)
		}
		if err := s.validateEdit(ctx, run, fact, edit); err != nil {
			return ThreadChangeSummary{}, err
		}
		key := edit.WorkspaceID + "\x00" + edit.Path
		state := current[key]
		if state == nil {
			state = &threadChangeFileState{workspaceID: edit.WorkspaceID, path: edit.Path, currentPath: edit.Path,
				originalHash: edit.OriginalHash, currentHash: edit.OriginalHash,
				originalText: edit.OriginalText, currentText: edit.OriginalText, editIDs: []string{}}
			current[key] = state
			ordered = append(ordered, state)
		}
		if state.currentHash != edit.OriginalHash {
			state.reason = "non_contiguous_edits"
		}
		if edit.SecretsRedacted || redact.String(edit.OriginalText) != edit.OriginalText || redact.String(edit.ProposedText) != edit.ProposedText {
			state.reason = "secrets_redacted"
		}
		state.editIDs = append(state.editIDs, edit.ID)
		state.currentHash, state.currentText = edit.ProposedHash, edit.ProposedText
		if edit.Operation == fileedit.OperationMove {
			destinationKey := edit.WorkspaceID + "\x00" + edit.DestinationPath
			if destination := current[destinationKey]; destination != nil && destination != state {
				// Two separately tracked files converge on one path. Do not merge
				// their bodies or imply that intervening content was authored here.
				destination.reason, state.reason = "non_contiguous_edits", "non_contiguous_edits"
			}
			delete(current, key)
			current[destinationKey] = state
			state.currentPath = edit.DestinationPath
			state.currentHash, state.currentText = edit.DestinationProposedHash, edit.OriginalText
		}
	}
	remaining := MaxThreadChangeDiffBytes
	diffDeadline := time.Now().Add(time.Second)
	for _, state := range ordered {
		if err := ctx.Err(); err != nil {
			return ThreadChangeSummary{}, err
		}
		if state.originalHash == state.currentHash && (state.path == state.currentPath || state.currentHash == "missing") {
			continue
		}
		file := state.project(time.Until(diffDeadline))
		if file.Reason != "" {
			result.CountsAvailable = false
			result.markPartial(file.Reason)
		}
		if len(file.Diff) > remaining {
			file.Diff, file.DiffTruncated = "", true
			result.markPartial("diff_budget")
		} else {
			remaining -= len(file.Diff)
		}
		result.Files = append(result.Files, file)
		result.Additions += file.Additions
		result.Deletions += file.Deletions
	}
	result.FileCount = len(result.Files)
	return result, nil
}

func (s *ThreadChangeService) validateEdit(ctx context.Context, run domain.Run, fact events.Event, edit fileedit.Edit) error {
	var payload struct {
		SessionID       string `json:"session_id"`
		WorkspaceID     string `json:"workspace_id"`
		Path            string `json:"path"`
		Operation       string `json:"operation"`
		DestinationPath string `json:"destination_path"`
		Status          string `json:"status"`
	}
	if json.Unmarshal([]byte(fact.PayloadJSON), &payload) != nil || edit.ID != fact.SubjectID ||
		edit.Status != fileedit.StatusApplied || payload.Status != fileedit.StatusApplied ||
		payload.SessionID != edit.SessionID || payload.WorkspaceID != edit.WorkspaceID || payload.Path != edit.Path ||
		payload.Operation != edit.Operation || payload.DestinationPath != edit.DestinationPath || edit.Path == "" {
		return threadChangeBindingError()
	}
	if _, err := fileedit.NormalizeOperation(edit.Operation); err != nil {
		return threadChangeBindingError()
	}
	belongs, err := s.store.FileEditWorkspaceBelongsToRun(ctx, run.ID, edit.SessionID, edit.WorkspaceID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !belongs {
		return threadChangeBindingError()
	}
	return nil
}

type threadChangeFileState struct {
	workspaceID, path, currentPath string
	originalHash, currentHash      string
	originalText, currentText      string
	editIDs                        []string
	reason                         string
}

func (state *threadChangeFileState) project(diffBudget time.Duration) ThreadChangeFile {
	file := ThreadChangeFile{WorkspaceID: state.workspaceID, Path: safeThreadActivityPath(state.path),
		Operation: fileedit.OperationReplace, CountsAvailable: state.reason == "", EditIDs: state.editIDs, Reason: state.reason}
	if state.originalHash == "missing" {
		file.Operation, file.Path = fileedit.OperationCreate, safeThreadActivityPath(state.currentPath)
	} else if state.currentHash == "missing" {
		file.Operation = fileedit.OperationDelete
	} else if state.path != state.currentPath {
		file.Operation, file.DestinationPath = fileedit.OperationMove, safeThreadActivityPath(state.currentPath)
	}
	if !file.CountsAvailable {
		return file
	}
	if file.Operation == fileedit.OperationMove {
		file.Diff = fmt.Sprintf("rename from %s\nrename to %s\n", file.Path, file.DestinationPath)
	}
	if state.originalText != state.currentText {
		diff, additions, deletions, available := threadChangeDiff(file.Path, state.originalText, state.currentText, diffBudget)
		if !available {
			file.CountsAvailable, file.Reason, file.Diff = false, "diff_timeout", ""
			return file
		}
		file.Additions, file.Deletions = additions, deletions
		file.Diff += diff
	}
	return file
}

func (result *ThreadChangeSummary) markPartial(reason string) {
	result.Partial = true
	for _, previous := range result.Reasons {
		if previous == reason {
			return
		}
	}
	result.Reasons = append(result.Reasons, reason)
}

func threadChangeBindingError() error {
	return apperror.New(apperror.CodeFailedPrecondition, "durable Thread change records have an inconsistent binding")
}
