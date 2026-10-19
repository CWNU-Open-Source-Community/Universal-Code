package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const ThreadChangeUndoVersion = "thread_change_undo.v1"

type ThreadChangeUndoRequest struct {
	Version       string `json:"version"`
	SourceEventID string `json:"source_event_id"`
	Confirm       bool   `json:"confirm"`
}

type ThreadChangeUndoConflict struct {
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
}

type ThreadChangeUndoResult struct {
	Version        string                     `json:"version"`
	ThreadID       string                     `json:"thread_id"`
	SourceEventID  string                     `json:"source_event_id"`
	RunID          string                     `json:"run_id"`
	State          string                     `json:"state"`
	Conflicts      []ThreadChangeUndoConflict `json:"conflicts"`
	RestoredPaths  []string                   `json:"restored_paths"`
	RemainingPaths []string                   `json:"remaining_paths"`
	Replayed       bool                       `json:"replayed"`
}

type ThreadChangeUndoStore interface {
	ThreadChangeStore
	WorkspaceCheckpointStore
	GetThread(context.Context, string) (domain.Thread, error)
	CheckThreadChangeUndoIdle(context.Context, string, string, string) error
	ClaimThreadChangeUndo(context.Context, workspacecheckpoint.Transaction) (workspacecheckpoint.Transaction, bool, error)
}

type ThreadChangeUndoService struct {
	store        ThreadChangeUndoStore
	capabilities domain.ExecutionPermissionRuntimeCapabilities
	drydocks     *RunWorktreeService
}

func NewThreadChangeUndoService(store ThreadChangeUndoStore, capabilities domain.ExecutionPermissionRuntimeCapabilities) *ThreadChangeUndoService {
	return &ThreadChangeUndoService{store: store, capabilities: capabilities}
}

func (s *ThreadChangeUndoService) WithDrydocks(drydocks *RunWorktreeService) *ThreadChangeUndoService {
	s.drydocks = drydocks
	return s
}

func threadUndoDigest(threadID, eventID string) string {
	return runmutation.Fingerprint(ThreadChangeUndoVersion, threadID, eventID)
}

// Status only reads the durable receipt; it deliberately does not inspect live
// files. Preview performs the fresh filesystem and authorization checks.
func (s *ThreadChangeUndoService) Status(ctx context.Context, summary ThreadChangeSummary) (ThreadChangeUndoResult, error) {
	r := ThreadChangeUndoResult{Version: ThreadChangeUndoVersion, ThreadID: summary.ThreadID,
		SourceEventID: summary.SourceEventID, RunID: summary.RunID, State: "available",
		Conflicts: []ThreadChangeUndoConflict{}, RestoredPaths: []string{}, RemainingPaths: []string{}}
	if summary.FileCount == 0 {
		r.State = "unavailable"
	}
	t, found, err := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx, threadUndoDigest(summary.ThreadID, summary.SourceEventID))
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if !found {
		return r, nil
	}
	if !workspacecheckpoint.IsThreadUndo(t) || t.TriggerReceiptID != workspacecheckpoint.ThreadUndoReceiptPrefix+summary.SourceEventID {
		return r, threadChangeBindingError()
	}
	r.Replayed = true
	switch t.Status {
	case workspacecheckpoint.TransactionCompleted:
		r.State = "completed"
	case workspacecheckpoint.TransactionPrepared, workspacecheckpoint.TransactionApplying:
		r.State = "applying"
	default:
		r.State = "partial"
	}
	if t.AfterCheckpointID != "" {
		after, e := s.store.GetWorkspaceCheckpointSnapshot(ctx, t.AfterCheckpointID)
		if e != nil {
			return r, apperror.Normalize(e)
		}
		target, e := s.store.GetWorkspaceCheckpointSnapshot(ctx, t.TargetCheckpointID)
		if e != nil {
			return r, apperror.Normalize(e)
		}
		classifyThreadUndoPaths(&r, target, after)
	} else {
		target, e := s.store.GetWorkspaceCheckpointSnapshot(ctx, t.TargetCheckpointID)
		if e != nil {
			return r, apperror.Normalize(e)
		}
		for _, entry := range target.Entries {
			r.RemainingPaths = append(r.RemainingPaths, safeThreadActivityPath(entry.Path))
		}
	}
	if r.State == "partial" {
		r.Conflicts = append(r.Conflicts, ThreadChangeUndoConflict{Reason: "restore_failed"})
	}
	return r, nil
}

type threadUndoPath struct {
	beforeHash, beforeText, afterHash, afterText, modePath, sourceEditID string
	beforeMode                                                           uint32
}

// Undo restores only paths proven by exact applied receipts before this reply.
// The existing checkpoint writer supplies path confinement and per-path CAS;
// its immutable transaction supplies durable replay and execution exclusion.
func (s *ThreadChangeUndoService) Undo(ctx context.Context, threadID string, request ThreadChangeUndoRequest) (ThreadChangeUndoResult, error) {
	if request.Version != ThreadChangeUndoVersion {
		return ThreadChangeUndoResult{}, apperror.New(apperror.CodeInvalidArgument, "unsupported Thread change undo version")
	}
	summary, err := NewThreadChangeService(s.store).Summary(ctx, threadID, request.SourceEventID)
	if err != nil {
		return ThreadChangeUndoResult{}, err
	}
	r, err := s.Status(ctx, summary)
	if err != nil || r.State == "completed" || r.State == "unavailable" {
		return r, err
	}
	digest := threadUndoDigest(threadID, request.SourceEventID)
	t, found, err := s.store.GetWorkspaceCheckpointTransactionByOperation(ctx, digest)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if found {
		return r, nil
	}
	thread, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	run, err := s.store.GetRun(ctx, thread.LastRunID)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	files, err := ResolveRunFileWorkspace(ctx, s.store, run, mission, s.drydocks)
	if err != nil {
		return threadUndoBlocked(r, "workspace_changed"), nil
	}
	if files.Drydock != nil {
		if err := requireCurrentRunFileDrydock(ctx, s.store, run.ID, *files.Drydock); err != nil {
			return threadUndoBlocked(r, "workspace_changed"), nil
		}
	}
	if err := requireSharedCommandServicesStopped(ctx, s.store, files.Workspace.RootPath); err != nil {
		return threadUndoBlocked(r, "run_busy"), nil
	}
	if thread.Status != domain.ThreadActive {
		return threadUndoBlocked(r, "run_busy"), nil
	}
	if err := s.store.CheckThreadChangeUndoIdle(ctx, threadID, run.ID, t.ID); err != nil {
		return threadUndoBlocked(r, "run_busy"), nil
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if mode.RunID != run.ID || mode.MissionID != mission.ID || mode.Surface != domain.ExecutionSurfaceCode || mode.Phase != domain.ExecutionPhaseDeliver ||
		permission.RunID != run.ID || permission.MissionID != mission.ID || !s.capabilities.AllowsSnapshot(permission) ||
		(permission.Mode.IsApprovalMode() && !s.capabilities.OperatorApprovalEnabled) {
		return threadUndoBlocked(r, "permission_denied"), nil
	}
	lease, leased, err := s.store.GetRunExecutionLease(ctx, run.ID)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if leased && lease.ActiveAt(time.Now().UTC()) {
		return threadUndoBlocked(r, "run_busy"), nil
	}
	paths, err := s.paths(ctx, summary, files.Workspace.ID, run)
	if err != nil {
		return threadUndoBlocked(r, "incomplete_history"), nil
	}
	observed, err := workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{ID: idgen.New("thread-undo-observed"), RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID,
		WorkspaceID: files.Workspace.ID, WorkspaceRoot: files.Workspace.RootPath, Trigger: workspacecheckpoint.TriggerRewindPreflight, Phase: workspacecheckpoint.PhasePreflight,
		TriggerReceiptID: workspacecheckpoint.ThreadUndoReceiptPrefix + request.SourceEventID, CreatedAt: time.Now().UTC()})
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if err := validateThreadUndoObservation(files.Workspace.RootPath, observed, paths); err != nil {
		return threadUndoBlocked(r, "incomplete_history"), nil
	}
	if err := s.resolveThreadUndoModes(ctx, summary.RunID, files.Workspace.ID, observed, paths); err != nil {
		return threadUndoBlocked(r, "incomplete_history"), nil
	}
	observed = scopeThreadUndoSnapshot(observed, paths)
	before := threadUndoSnapshot(observed, paths, false, idgen.New("thread-undo-before"))
	target := threadUndoSnapshot(observed, paths, true, idgen.New("thread-undo-target"))
	preview, err := workspacecheckpoint.PreviewRestore(before, target, observed)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	classifyThreadUndoPaths(&r, target, observed)
	r.Conflicts = []ThreadChangeUndoConflict{}
	for _, c := range preview.Conflicts {
		r.Conflicts = append(r.Conflicts, ThreadChangeUndoConflict{Path: safeThreadActivityPath(c.Path), Reason: "files_changed"})
	}
	if len(r.Conflicts) > 0 {
		r.State = "conflict"
		return r, nil
	}
	r.State = "available"
	if !request.Confirm {
		return r, nil
	}
	for _, snapshot := range []workspacecheckpoint.Snapshot{before, target} {
		if _, _, err = s.store.CreateWorkspaceCheckpoint(ctx, snapshot); err != nil {
			return r, apperror.Normalize(err)
		}
	}
	now := time.Now().UTC()
	t = workspacecheckpoint.Transaction{ID: "thread-undo-" + digest[:40], ProtocolVersion: workspacecheckpoint.ProtocolVersion, OperationKeyDigest: digest,
		RequestFingerprint: runmutation.Fingerprint(ThreadChangeUndoVersion, threadID, request.SourceEventID, run.ID, files.Workspace.ID), RunID: run.ID, WorkspaceID: files.Workspace.ID,
		Kind: workspacecheckpoint.TransactionUndo, TriggerReceiptID: workspacecheckpoint.ThreadUndoReceiptPrefix + request.SourceEventID,
		BeforeCheckpointID: before.Checkpoint.ID, TargetCheckpointID: target.Checkpoint.ID, Status: workspacecheckpoint.TransactionPrepared,
		RecoveryLevel: workspacecheckpoint.RecoveryComplete, ConflictJSON: "[]", CreatedAt: now, UpdatedAt: now}
	t, r.Replayed, err = s.store.CreateWorkspaceCheckpointTransaction(ctx, t)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if r.Replayed {
		return s.Status(ctx, summary)
	}
	var claimed bool
	t, claimed, err = s.store.ClaimThreadChangeUndo(ctx, t)
	if err != nil {
		return r, apperror.Normalize(err)
	}
	if !claimed {
		return s.Status(ctx, summary)
	}
	_, applyErr := workspacecheckpoint.ApplyRestore(ctx, files.Workspace.RootPath, before, target, observed)
	// Complete against a context that survives a disconnected HTTP caller.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	after, captureErr := workspacecheckpoint.Capture(finishCtx, workspacecheckpoint.CaptureRequest{ID: idgen.New("thread-undo-after"), RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID,
		WorkspaceID: files.Workspace.ID, WorkspaceRoot: files.Workspace.RootPath, Trigger: workspacecheckpoint.TriggerRewindResult, Phase: workspacecheckpoint.PhaseAfter,
		TriggerReceiptID: t.ID, CreatedAt: time.Now().UTC()})
	if captureErr != nil {
		r.State = "partial"
		r.Conflicts = append(r.Conflicts, ThreadChangeUndoConflict{Reason: "restore_failed"})
		return r, errors.Join(applyErr, captureErr)
	}
	if err := validateThreadUndoObservation(files.Workspace.RootPath, after, paths); err != nil {
		return r, apperror.Normalize(err)
	}
	after = scopeThreadUndoSnapshot(after, paths)
	if _, _, err = s.store.CreateWorkspaceCheckpoint(finishCtx, after); err != nil {
		return r, apperror.Normalize(err)
	}
	classifyThreadUndoPaths(&r, target, after)
	verification, verifyErr := workspacecheckpoint.PreviewRestore(target, target, after)
	if verifyErr != nil || len(verification.Conflicts) > 0 {
		applyErr = errors.Join(applyErr, verifyErr, errors.New("reply undo final state changed"))
	}
	t.AfterCheckpointID = after.Checkpoint.ID
	if applyErr == nil && len(r.RemainingPaths) == 0 {
		r.State = "completed"
		t.Status = workspacecheckpoint.TransactionCompleted
	} else {
		r.State = "partial"
		r.Conflicts = append(r.Conflicts, ThreadChangeUndoConflict{Reason: "restore_failed"})
		t.Status = workspacecheckpoint.TransactionInterrupted
		t.ErrorCode = "restore_failed"
	}
	t.UpdatedAt = time.Now().UTC()
	t.CompletedAt = &t.UpdatedAt
	if _, _, err = s.store.UpdateWorkspaceCheckpointTransaction(finishCtx, t); err != nil {
		return r, apperror.Normalize(err)
	}
	return r, nil
}

func threadUndoBlocked(r ThreadChangeUndoResult, reason string) ThreadChangeUndoResult {
	r.State = "unavailable"
	r.Conflicts = append(r.Conflicts, ThreadChangeUndoConflict{Reason: reason})
	return r
}

func (s *ThreadChangeUndoService) paths(ctx context.Context, summary ThreadChangeSummary, workspaceID string, currentRun domain.Run) (map[string]threadUndoPath, error) {
	source, facts, err := s.store.ListThreadReplyFileEditEvents(ctx, summary.ThreadID, summary.SourceEventID, MaxThreadChangeEdits+1)
	if err != nil {
		return nil, err
	}
	if len(facts) > MaxThreadChangeEdits {
		return nil, threadChangeBindingError()
	}
	run, err := s.store.GetRun(ctx, source.RunID)
	if err != nil {
		return nil, err
	}
	paths := map[string]threadUndoPath{}
	seen := map[string]bool{}
	set := func(path, from, fromText, to, toText, sourceEditID string) error {
		if !validProposalSourcePath(path) || !validPersistedOriginal(from, fromText) || !validPersistedOriginal(to, toText) {
			return threadChangeBindingError()
		}
		if old, ok := paths[path]; ok {
			if old.afterHash != from {
				return threadChangeBindingError()
			}
			old.afterHash, old.afterText = to, toText
			paths[path] = old
		} else {
			paths[path] = threadUndoPath{beforeHash: from, beforeText: fromText, afterHash: to, afterText: toText, modePath: path, sourceEditID: sourceEditID}
		}
		return nil
	}
	for _, fact := range facts {
		if seen[fact.SubjectID] {
			continue
		}
		seen[fact.SubjectID] = true
		e, err := s.store.GetFileEdit(ctx, fact.SubjectID)
		if err != nil {
			return nil, err
		}
		if err = NewThreadChangeService(s.store).validateEdit(ctx, run, fact, e); err != nil {
			return nil, err
		}
		if e.WorkspaceID != workspaceID || e.SecretsRedacted || redact.String(e.OriginalText) != e.OriginalText || redact.String(e.ProposedText) != e.ProposedText {
			return nil, threadChangeBindingError()
		}
		if err = checkRunProjectPaths(currentRun, e.Path, e.DestinationPath); err != nil {
			return nil, err
		}
		if err = checkRunProjectPaths(run, e.Path, e.DestinationPath); err != nil {
			return nil, err
		}
		if err = set(e.Path, e.OriginalHash, e.OriginalText, e.ProposedHash, e.ProposedText, e.ID); err != nil {
			return nil, err
		}
		if e.Operation == fileedit.OperationMove {
			if e.ProposedHash != "missing" || e.DestinationOriginalHash != "missing" || e.DestinationProposedHash != e.OriginalHash {
				return nil, threadChangeBindingError()
			}
			if err = set(e.DestinationPath, "missing", "", e.DestinationProposedHash, e.OriginalText, e.ID); err != nil {
				return nil, err
			}
			for path, p := range paths {
				if p.modePath == e.Path {
					p.modePath = e.DestinationPath
					paths[path] = p
				}
			}
		}
	}
	return paths, nil
}

func validateThreadUndoObservation(root string, snapshot workspacecheckpoint.Snapshot, paths map[string]threadUndoPath) error {
	captured := map[string]string{}
	for _, e := range snapshot.Entries {
		captured[e.Path] = e.WorktreeSHA256
	}
	for path := range paths {
		hash, err := fileedit.CurrentCreateHash(root, path)
		if err != nil {
			return err
		}
		observed := captured[path]
		if observed == "" {
			observed = "missing"
		}
		if hash != observed {
			return apperror.New(apperror.CodeConflict, "reply path was not captured exactly")
		}
	}
	return nil
}

func (s *ThreadChangeUndoService) resolveThreadUndoModes(ctx context.Context, runID, workspaceID string, observed workspacecheckpoint.Snapshot, paths map[string]threadUndoPath) error {
	modes := map[string]uint32{}
	for _, e := range observed.Entries {
		modes[e.Path] = e.Mode
	}
	var checkpoints []workspacecheckpoint.Checkpoint
	for path, p := range paths {
		if p.beforeHash == "missing" {
			continue
		}
		p.beforeMode = modes[p.modePath]
		if p.beforeMode == 0 {
			if checkpoints == nil {
				var err error
				checkpoints, err = s.store.ListWorkspaceCheckpoints(ctx, runID, workspaceCheckpointListLimit)
				if err != nil {
					return err
				}
			}
			for _, c := range checkpoints {
				if c.WorkspaceID != workspaceID || c.Trigger != workspacecheckpoint.TriggerFileTool || c.Phase != workspacecheckpoint.PhaseBefore || c.TriggerReceiptID != p.sourceEditID {
					continue
				}
				snapshot, err := s.store.GetWorkspaceCheckpointSnapshot(ctx, c.ID)
				if err != nil {
					return err
				}
				for _, e := range snapshot.Entries {
					if e.Path == path && e.WorktreeSHA256 == p.beforeHash && e.Kind == workspacecheckpoint.EntryFile {
						p.beforeMode = e.Mode
						break
					}
				}
				if p.beforeMode != 0 {
					break
				}
			}
			if p.beforeMode == 0 {
				return threadChangeBindingError()
			}
		}
		paths[path] = p
	}
	return nil
}

func scopeThreadUndoSnapshot(snapshot workspacecheckpoint.Snapshot, paths map[string]threadUndoPath) workspacecheckpoint.Snapshot {
	entries := []workspacecheckpoint.Entry{}
	for _, entry := range snapshot.Entries {
		if _, ok := paths[entry.Path]; ok {
			entries = append(entries, entry)
		}
	}
	snapshot.Entries = entries
	// Preserve the index digest for drift detection, without persisting or
	// restoring index content as part of a reply's selected file paths.
	snapshot.Checkpoint.IndexBlobSHA256 = ""
	return sealThreadUndoSnapshot(snapshot)
}

func threadUndoSnapshot(observed workspacecheckpoint.Snapshot, paths map[string]threadUndoPath, target bool, id string) workspacecheckpoint.Snapshot {
	snapshot := observed
	snapshot.Checkpoint.ID = id
	snapshot.Checkpoint.Phase = workspacecheckpoint.PhaseStandalone
	snapshot.Entries = []workspacecheckpoint.Entry{}
	snapshot.Blobs = []workspacecheckpoint.Blob{}
	modes := map[string]uint32{}
	for _, e := range observed.Entries {
		modes[e.Path] = e.Mode
	}
	for path, p := range paths {
		hash, text := p.afterHash, p.afterText
		if target {
			hash, text = p.beforeHash, p.beforeText
		}
		mode := modes[path]
		if target && p.beforeMode != 0 {
			mode = p.beforeMode
		}
		if mode == 0 {
			mode = 0600
		}
		e := workspacecheckpoint.Entry{Path: path, Kind: workspacecheckpoint.EntryFile, Mode: mode, WorktreeSHA256: hash, Recoverable: true}
		if hash == "missing" {
			e.State = workspacecheckpoint.StateMissing
			e.StoragePolicy = workspacecheckpoint.StorageMissing
			e.Mode = 0
		} else {
			e.State = workspacecheckpoint.StatePresent
			e.StoragePolicy = workspacecheckpoint.StorageStored
			e.Size = int64(len(text))
			e.BlobSHA256 = hash
			snapshot.Blobs = append(snapshot.Blobs, workspacecheckpoint.Blob{SHA256: hash, Content: []byte(text), CreatedAt: snapshot.Checkpoint.CreatedAt})
		}
		snapshot.Entries = append(snapshot.Entries, e)
	}
	// Undo never restores the index; preserve its digest without storing a blob.
	snapshot.Checkpoint.IndexBlobSHA256 = ""
	return sealThreadUndoSnapshot(snapshot)
}

func sealThreadUndoSnapshot(snapshot workspacecheckpoint.Snapshot) workspacecheckpoint.Snapshot {
	sort.Slice(snapshot.Entries, func(i, j int) bool { return snapshot.Entries[i].Path < snapshot.Entries[j].Path })
	needed := map[string]bool{}
	for _, e := range snapshot.Entries {
		if e.BlobSHA256 != "" {
			needed[e.BlobSHA256] = true
		}
	}
	if snapshot.Checkpoint.IndexBlobSHA256 != "" {
		needed[snapshot.Checkpoint.IndexBlobSHA256] = true
	}
	blobs := []workspacecheckpoint.Blob{}
	var stored int64
	for _, b := range snapshot.Blobs {
		if needed[b.SHA256] {
			blobs = append(blobs, b)
			stored += int64(len(b.Content))
			delete(needed, b.SHA256)
		}
	}
	snapshot.Blobs = blobs
	data, _ := json.Marshal(snapshot.Entries)
	hash := sha256.Sum256(data)
	snapshot.Checkpoint.ManifestSHA256 = hex.EncodeToString(hash[:])
	snapshot.Checkpoint.EntryCount = len(snapshot.Entries)
	snapshot.Checkpoint.StoredBytes = stored
	snapshot.Checkpoint.Title = workspacecheckpoint.ThreadUndoSnapshotTitle
	snapshot.Checkpoint.RecoveryLevel = workspacecheckpoint.RecoveryUnavailable
	return snapshot
}

func classifyThreadUndoPaths(r *ThreadChangeUndoResult, target, observed workspacecheckpoint.Snapshot) {
	live := map[string]string{}
	for _, e := range observed.Entries {
		live[e.Path] = e.WorktreeSHA256
	}
	r.RestoredPaths = []string{}
	r.RemainingPaths = []string{}
	for _, e := range target.Entries {
		hash := live[e.Path]
		if hash == "" {
			hash = "missing"
		}
		if hash == e.WorktreeSHA256 {
			r.RestoredPaths = append(r.RestoredPaths, safeThreadActivityPath(e.Path))
		} else {
			r.RemainingPaths = append(r.RemainingPaths, safeThreadActivityPath(e.Path))
		}
	}
}
