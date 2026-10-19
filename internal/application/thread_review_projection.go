package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func (s *ThreadReviewService) Review(ctx context.Context, threadID string) (ThreadReview, error) {
	if s == nil || s.store == nil {
		return ThreadReview{}, apperror.New(apperror.CodeUnavailable, "Thread review is unavailable")
	}
	if strings.TrimSpace(threadID) == "" {
		return ThreadReview{}, apperror.New(apperror.CodeInvalidArgument, "Thread identity is required")
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, stable, err := s.reviewOnce(ctx, threadID)
		if err != nil || stable {
			return result, err
		}
	}
	return ThreadReview{}, apperror.New(apperror.CodeConflict, "Thread or workspace changed while reading the review; refresh to observe the current revision")
}

func (s *ThreadReviewService) reviewOnce(ctx context.Context, threadID string) (ThreadReview, bool, error) {
	thread, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return ThreadReview{}, false, apperror.Normalize(err)
	}
	if thread.ID != threadID {
		return ThreadReview{}, false, threadReviewBindingError()
	}
	bindings, err := s.store.ListThreadRuns(ctx, threadID)
	if err != nil {
		return ThreadReview{}, false, apperror.Normalize(err)
	}
	currentID := thread.ActiveRunID
	if currentID == "" {
		currentID = thread.LastRunID
	}
	if len(bindings) == 0 || bindings[len(bindings)-1].RunID != currentID {
		return ThreadReview{}, false, threadReviewBindingError()
	}
	for i, binding := range bindings {
		if binding.Validate() != nil || binding.ThreadID != threadID || binding.Ordinal != int64(i+1) ||
			(i > 0 && binding.PredecessorRunID != bindings[i-1].RunID) {
			return ThreadReview{}, false, threadReviewBindingError()
		}
	}
	result := ThreadReview{ThreadID: threadID, ThreadVersion: thread.Version, CurrentRunID: currentID,
		ObservedAt: time.Now().UTC(), ChangeScope: "recorded_file_edits", TotalRuns: len(bindings),
		Runs: []ThreadReviewRun{}, AppliedChanges: []ThreadReviewChange{}, UnappliedChanges: []ThreadReviewChange{},
		Checks: []ThreadReviewCheck{}, Reasons: []string{},
		Target:   ThreadReviewTarget{State: "unavailable", Kind: "unknown", SourceWorkspaceID: thread.WorkspaceID},
		Revision: ThreadReviewRevision{State: "unavailable", RepositoryKind: "unknown", Reasons: []string{}}}
	if len(bindings) > MaxThreadReviewRuns {
		result.markPartial("older_runs_omitted")
		bindings = bindings[len(bindings)-MaxThreadReviewRuns:]
	}
	current, err := s.store.GetRun(ctx, currentID)
	if err != nil {
		return ThreadReview{}, false, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, thread.MissionID)
	if err != nil {
		return ThreadReview{}, false, apperror.Normalize(err)
	}
	if current.ID != currentID || current.MissionID != thread.MissionID || mission.ID != thread.MissionID || mission.WorkspaceID != thread.WorkspaceID {
		return ThreadReview{}, false, threadReviewBindingError()
	}
	physical, targetErr := ResolveRunFileWorkspace(ctx, s.store, current, mission, s.drydocks)
	var snapshot workspacecheckpoint.Snapshot
	if targetErr != nil {
		result.markPartial("current_target_unavailable")
		result.Revision.Reasons = append(result.Revision.Reasons, "current_target_unavailable")
	} else {
		result.Target = ThreadReviewTarget{State: "available", Kind: "source", SourceWorkspaceID: physical.Source.ID,
			WorkspaceID: physical.Workspace.ID, RootPath: physical.Workspace.RootPath}
		if physical.Drydock != nil {
			result.Target.Kind, result.Target.DrydockID = "drydock", physical.Drydock.ID
		}
		snapshot, err = captureThreadReview(ctx, current, mission, physical)
		if err != nil {
			result.markPartial("current_revision_unavailable")
			result.Revision.Reasons = append(result.Revision.Reasons, "current_revision_unavailable")
		} else {
			result.Revision = threadReviewRevision(snapshot)
			result.Target.RootFingerprint = snapshot.Checkpoint.RootFingerprint
			if result.Revision.RepositoryKind == "git" {
				// Existing hardened read path supports .git files in linked worktrees,
				// disables optional index locks, and enables no mutation capability.
				observer, observeErr := repository.NewAdvancedExecutor("", false)
				if observeErr == nil {
					binding, readErr := observer.CaptureAdvancedBinding(ctx, physical.Workspace.RootPath)
					observeErr = readErr
					if readErr == nil {
						if binding.Head != snapshot.Checkpoint.BaseCommit || binding.IndexSHA256 != snapshot.Checkpoint.IndexSHA256 || binding.Branch != snapshot.Checkpoint.Branch {
							return result, false, nil
						}
						empty := sha256.Sum256(nil)
						dirty := binding.StatusSHA256 != hex.EncodeToString(empty[:])
						result.Revision.Dirty = &dirty
					}
				}
				if observeErr != nil {
					result.Revision.State = "partial"
					result.Revision.Reasons = append(result.Revision.Reasons, "git_status_unavailable")
				}
			}
			if result.Revision.State != "available" {
				result.markPartial("current_revision_partial")
			}
		}
	}
	entries := make(map[string]workspacecheckpoint.Entry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = entry
	}
	diffRemaining := MaxThreadReviewDiffBytes
	// Newer Runs first, with the exact persistent Run/Session provenance retained.
	for index := len(bindings) - 1; index >= 0; index-- {
		binding := bindings[index]
		run, err := s.store.GetRun(ctx, binding.RunID)
		if err != nil {
			return ThreadReview{}, false, apperror.Normalize(err)
		}
		if run.ID != binding.RunID || run.SessionID != binding.SessionID || run.MissionID != mission.ID {
			return ThreadReview{}, false, threadReviewBindingError()
		}
		linked, err := s.store.GetSession(ctx, run.SessionID)
		if err != nil {
			return ThreadReview{}, false, apperror.Normalize(err)
		}
		if linked.ID != run.SessionID || linked.WorkspaceID != mission.WorkspaceID {
			return ThreadReview{}, false, threadReviewBindingError()
		}
		sequence, err := s.store.LatestRunEventSequence(ctx, run.ID)
		if err != nil {
			return ThreadReview{}, false, apperror.Normalize(err)
		}
		workspaceID, err := s.historicalReviewWorkspace(ctx, thread, run)
		if err != nil {
			return ThreadReview{}, false, err
		}
		handoffURL := "/api/v1/runs/" + url.PathEscape(run.ID) + "/code-handoff"
		result.Runs = append(result.Runs, ThreadReviewRun{RunID: run.ID, SessionID: run.SessionID,
			Ordinal: binding.Ordinal, WorkspaceID: workspaceID, SourceEventSequence: sequence, HandoffURL: handoffURL})
		if err := s.addReviewChanges(ctx, run, mission.WorkspaceID, workspaceID, entries, &diffRemaining, &result); err != nil {
			return ThreadReview{}, false, err
		}
		if s.handoffs == nil {
			result.markPartial("handoff_reader_unavailable")
			continue
		}
		handoff, err := s.handoffs.Build(ctx, run.ID)
		if err != nil {
			result.markPartial("handoff_unavailable:" + run.ID)
			continue
		}
		if handoff.RunID != run.ID || handoff.SessionID != run.SessionID || handoff.MissionID != mission.ID || handoff.WorkspaceID != mission.WorkspaceID {
			return ThreadReview{}, false, threadReviewBindingError()
		}
		if handoff.SourceEventSequence != sequence {
			return result, false, nil
		}
		addThreadReviewChecks(&result, handoff, handoffURL)
	}
	if result.Revision.RevisionSHA256 != "" {
		after, err := captureThreadReview(ctx, current, mission, physical)
		if err != nil || checkpointRevision(after.Checkpoint) != result.Revision.RevisionSHA256 {
			return result, false, nil
		}
	}
	after, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return ThreadReview{}, false, apperror.Normalize(err)
	}
	if after.ID != thread.ID || after.Version != thread.Version || after.ActiveRunID != thread.ActiveRunID ||
		after.LastRunID != thread.LastRunID || after.MissionID != thread.MissionID || after.WorkspaceID != thread.WorkspaceID ||
		after.Status != thread.Status || !after.UpdatedAt.Equal(thread.UpdatedAt) {
		return result, false, nil
	}
	for _, run := range result.Runs {
		sequence, err := s.store.LatestRunEventSequence(ctx, run.RunID)
		if err != nil {
			return ThreadReview{}, false, apperror.Normalize(err)
		}
		if sequence != run.SourceEventSequence {
			return result, false, nil
		}
	}
	return result, true, nil
}

func (s *ThreadReviewService) historicalReviewWorkspace(ctx context.Context, thread domain.Thread, run domain.Run) (string, error) {
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	owned, found, err := readRunFileDrydock(ctx, s.store, run.ID)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	if !found {
		if reader, ok := s.store.(runFilePresetStore); ok {
			_, configured, err := reader.GetConfiguredStandardCodePresetOperation(ctx, run.ID)
			if err != nil {
				return "", apperror.Normalize(err)
			}
			if configured {
				return "", nil
			} // Unavailable target is never relabelled as the source.
		}
		return mission.WorkspaceID, nil
	}
	if owned.MissionID != mission.ID || owned.SourceWorkspaceID != mission.WorkspaceID || owned.WorkspaceID == "" || owned.WorkspaceID == mission.WorkspaceID {
		return "", threadReviewBindingError()
	}
	if owned.RunID == run.ID {
		if owned.SessionID != run.SessionID {
			return "", threadReviewBindingError()
		}
	} else {
		reader, ok := s.store.(interface {
			GetThreadDrydockBinding(context.Context, string) (domain.ThreadDrydockBinding, bool, error)
		})
		if !ok {
			return "", threadReviewBindingError()
		}
		binding, found, err := reader.GetThreadDrydockBinding(ctx, run.ID)
		if err != nil {
			return "", apperror.Normalize(err)
		}
		if !found || binding.ThreadID != thread.ID || binding.RunID != run.ID || binding.DrydockID != owned.ID {
			return "", threadReviewBindingError()
		}
	}
	return owned.WorkspaceID, nil
}

func (s *ThreadReviewService) addReviewChanges(ctx context.Context, run domain.Run, sourceID, targetID string,
	entries map[string]workspacecheckpoint.Entry, diffRemaining *int, result *ThreadReview) error {
	for offset := 0; ; offset += 100 {
		previews, err := s.store.ListFileEditPreviewsPage(ctx, fileedit.ListFilter{SessionID: run.SessionID}, offset, 100)
		if err != nil {
			return apperror.Normalize(err)
		}
		for _, preview := range previews {
			if preview.SessionID != run.SessionID || (preview.WorkspaceID != sourceID && (targetID == "" || preview.WorkspaceID != targetID)) || !fileedit.ValidStatus(preview.Status) {
				return threadReviewBindingError()
			}
			if len(result.AppliedChanges)+len(result.UnappliedChanges) >= MaxThreadReviewChanges {
				result.markPartial("changes_omitted")
				return nil
			}
			change := ThreadReviewChange{RunID: run.ID, SessionID: run.SessionID, WorkspaceID: preview.WorkspaceID,
				EditID: preview.ID, Operation: preview.Operation, Path: preview.Path, DestinationPath: preview.DestinationPath,
				Status: preview.Status, OriginalSHA256: preview.OriginalHash, ProposedSHA256: preview.ProposedHash,
				DestinationOriginalSHA256: preview.DestinationOriginalHash, DestinationProposedSHA256: preview.DestinationProposedHash,
				Diff: preview.Diff, Redacted: preview.SecretsRedacted, UpdatedAt: preview.UpdatedAt, CurrentMatch: "unavailable"}
			if len(change.Diff) > *diffRemaining {
				end := *diffRemaining
				for end > 0 && !utf8.ValidString(change.Diff[:end]) {
					end--
				}
				change.Diff, change.DiffTruncated = change.Diff[:end], true
				result.markPartial("diffs_truncated")
			}
			*diffRemaining -= len(change.Diff)
			if result.Target.State == "available" && change.WorkspaceID != result.Target.WorkspaceID {
				change.CurrentMatch = "other_target"
			} else if result.Revision.RevisionSHA256 != "" {
				change.CurrentSHA256, change.CurrentMatch = matchThreadReviewChange(change, entries, result.Revision.State == "available")
			}
			if preview.Status == fileedit.StatusApplied {
				result.AppliedChanges = append(result.AppliedChanges, change)
			} else {
				result.UnappliedChanges = append(result.UnappliedChanges, change)
			}
		}
		if len(previews) < 100 {
			return nil
		}
	}
}

func matchThreadReviewChange(change ThreadReviewChange, entries map[string]workspacecheckpoint.Entry, complete bool) (string, string) {
	lookup := func(path string) (string, bool) {
		entry, found := entries[path]
		if !found {
			return "missing", complete
		}
		if entry.State == workspacecheckpoint.StateMissing {
			return "missing", true
		}
		// Capture initializes excluded/unreadable entries with a "missing"
		// sentinel too. Only an explicit missing state proves absence; a link,
		// ignored directory, or unreadable file is not an applied deletion.
		if entry.Kind != workspacecheckpoint.EntryFile || entry.StoragePolicy == workspacecheckpoint.StorageUnreadable || len(entry.WorktreeSHA256) != 64 {
			return "", false
		}
		_, err := hex.DecodeString(entry.WorktreeSHA256)
		return entry.WorktreeSHA256, err == nil
	}
	current, known := lookup(change.Path)
	if !known {
		return "", "unavailable"
	}
	if current != change.ProposedSHA256 {
		if current == "missing" {
			return current, "missing"
		}
		return current, "changed"
	}
	if change.Operation == fileedit.OperationMove {
		destination, known := lookup(change.DestinationPath)
		if !known {
			return current, "unavailable"
		}
		if destination != change.DestinationProposedSHA256 {
			return current, "changed"
		}
	}
	return current, "matches"
}

func captureThreadReview(ctx context.Context, run domain.Run, mission domain.Mission, files RunFileWorkspace) (workspacecheckpoint.Snapshot, error) {
	return workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{ID: "thread-review-observation", RunID: run.ID,
		MissionID: mission.ID, SessionID: run.SessionID, WorkspaceID: files.Workspace.ID, WorkspaceRoot: files.Workspace.RootPath,
		Trigger: workspacecheckpoint.TriggerManual, Phase: workspacecheckpoint.PhaseStandalone, TriggerReceiptID: "thread-review-observation",
		RequestedBy: "operator", Title: "Read-only task review", CreatedAt: time.Now().UTC()})
}

func threadReviewRevision(snapshot workspacecheckpoint.Snapshot) ThreadReviewRevision {
	cp := snapshot.Checkpoint
	result := ThreadReviewRevision{State: "available", RepositoryKind: "git", Head: cp.BaseCommit, Branch: cp.Branch,
		IndexSHA256: cp.IndexSHA256, ManifestSHA256: cp.ManifestSHA256, RevisionSHA256: checkpointRevision(cp), Reasons: []string{}}
	if cp.BaseCommit == "non-git" {
		result.RepositoryKind, result.Head = "none", ""
	}
	// Recovery completeness is intentionally not advertised as revision coverage.
	// A non-Git directory can still have a complete observed file manifest.
	for _, reason := range cp.IncompleteReasons {
		if reason == "workspace is not an exact Git worktree" {
			continue
		}
		result.State = "partial"
		result.Reasons = append(result.Reasons, reason)
	}
	return result
}

func (r *ThreadReview) markPartial(reason string) {
	r.Partial = true
	for _, previous := range r.Reasons {
		if reason == previous {
			return
		}
	}
	r.Reasons = append(r.Reasons, reason)
}

func threadReviewBindingError() error {
	return apperror.New(apperror.CodeConflict, "Thread review source identity is inconsistent")
}

func addThreadReviewChecks(result *ThreadReview, handoff CodeHandoff, handoffURL string) {
	add := func(check ThreadReviewCheck) {
		if len(result.Checks) >= MaxThreadReviewChecks {
			result.markPartial("checks_omitted")
			return
		}
		check.RunID, check.HandoffURL = handoff.RunID, handoffURL
		result.Checks = append(result.Checks, check)
	}
	for _, evidence := range handoff.Verification.References {
		add(ThreadReviewCheck{ID: evidence.ID, SourceKind: "verification_evidence", Title: evidence.ID,
			Outcome: string(evidence.Outcome), RevisionState: "unbound", Reason: "record_has_no_workspace_revision_binding", RecordedAt: evidence.CreatedAt})
	}
	if handoff.Verification.Truncated {
		result.markPartial("verification_references_omitted")
	}
	if handoff.HostCommands != nil {
		if handoff.HostCommands.Truncated {
			result.markPartial("host_command_references_omitted")
		}
		for _, command := range handoff.HostCommands.Items {
			if command.Receipt == nil {
				continue
			} // Approval is not an executed check.
			check := ThreadReviewCheck{ID: command.ProposalID, SourceKind: "host_command", Title: command.Purpose,
				Outcome: command.ResultStatus, ExitCode: &command.Receipt.ExitCode, RecordedAt: command.Receipt.CompletedAt,
				RevisionState: "unbound", Reason: "execution_receipt_has_no_workspace_revision_binding"}
			add(check)
		}
	}
	if report := handoff.StandardCodeDelivery; report != nil {
		if report.Binding.RunID != handoff.RunID || report.Binding.SessionID != handoff.SessionID {
			result.markPartial("delivery_binding_unavailable")
			return
		}
		for _, evidence := range report.Verifications {
			check := ThreadReviewCheck{ID: evidence.JobID, SourceKind: "standard_code", Title: evidence.JobID, Outcome: string(evidence.Conclusion),
				ExitCode: evidence.ExitCode, RecordedRevisionSHA256: evidence.RevisionSHA256, RevisionState: "unbound", Reason: "record_has_no_workspace_revision_binding"}
			if evidence.CompletedAt != nil {
				check.RecordedAt = *evidence.CompletedAt
			} else {
				check.RecordedAt = report.CreatedAt
			}
			if evidence.RevisionSHA256 != "" {
				switch {
				case result.Revision.State != "available":
					check.RevisionState, check.Reason = "unavailable", "current_revision_unavailable"
				case report.Binding.DrydockWorkspaceID != result.Target.WorkspaceID:
					check.RevisionState, check.Reason = "stale", "different_workspace_target"
				case evidence.RevisionSHA256 != result.Revision.RevisionSHA256:
					check.RevisionState, check.Reason = "stale", "workspace_revision_changed"
				case !evidence.CurrentRevision:
					check.RevisionState, check.Reason = "unavailable", "delivery_observation_not_current"
				default:
					check.RevisionState, check.Reason = "current", "exact_workspace_revision_matches"
				}
			}
			add(check)
		}
	}
}
