package application

import (
	"context"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

// FileEditCheckpointService reuses the ordinary mutation journal and captures,
// but its cursor belongs to the Drydock lifecycle. The source Run cursor is
// never changed by an edit in the isolated workspace.
func (s *RunWorktreeService) FileEditCheckpointService() *WorkspaceCheckpointService {
	if s == nil || s.checkpoints == nil {
		return nil
	}
	owned := *s.checkpoints
	owned.strictMutationReplay = true
	owned.store = &drydockFileBoundaryStore{WorkspaceCheckpointStore: s.checkpoints.store, owner: s}
	owned.withRunWorkspaceResolver(func(ctx context.Context, runID string) (session.WorkspaceInfo, bool, error) {
		run, err := s.store.GetRun(ctx, runID)
		if err != nil {
			return session.WorkspaceInfo{}, false, err
		}
		mission, err := s.store.GetMission(ctx, run.MissionID)
		if err != nil {
			return session.WorkspaceInfo{}, false, err
		}
		resolved, err := ResolveRunFileWorkspace(ctx, s.checkpoints.store, run, mission, s)
		if err != nil {
			return session.WorkspaceInfo{}, false, err
		}
		return resolved.Workspace, true, nil
	})
	return &owned
}

type drydockFileBoundaryStore struct {
	WorkspaceCheckpointStore
	owner       *RunWorktreeService
	gitMutation bool
}

func (s *drydockFileBoundaryStore) ListCommandRuntimeJobs(ctx context.Context, filter runner.CommandRuntimeListFilter) ([]runner.CommandRuntimeJob, error) {
	reader, ok := s.WorkspaceCheckpointStore.(commandRuntimeJobLister)
	if !ok {
		return nil, nil
	}
	return reader.ListCommandRuntimeJobs(ctx, filter)
}

func (s *drydockFileBoundaryStore) GetCommandRuntimeJob(ctx context.Context, id string) (runner.CommandRuntimeJob, error) {
	reader, ok := s.WorkspaceCheckpointStore.(interface {
		GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error)
	})
	if !ok {
		return runner.CommandRuntimeJob{}, apperror.New(apperror.CodeFailedPrecondition,
			"command runtime Job metadata is unavailable")
	}
	return reader.GetCommandRuntimeJob(ctx, id)
}

func (s *RunWorktreeService) gitMutationCheckpointService() *WorkspaceCheckpointService {
	value := s.FileEditCheckpointService()
	if value != nil {
		value.store.(*drydockFileBoundaryStore).gitMutation = true
	}
	return value
}

func (s *drydockFileBoundaryStore) CheckThreadGitIdle(ctx context.Context, threadID, runID string, lease *domain.RunExecutionLease) error {
	checker, ok := s.owner.store.(interface {
		CheckThreadGitIdle(context.Context, string, string, *domain.RunExecutionLease) error
	})
	if !s.gitMutation || !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Drydock operator Git authority is unavailable")
	}
	return checker.CheckThreadGitIdle(ctx, threadID, runID, lease)
}

func (s *drydockFileBoundaryStore) GetWorkspaceCheckpointRunState(ctx context.Context,
	runID string,
) (workspacecheckpoint.RunState, bool, error) {
	d, found, err := readRunFileDrydock(ctx, s.owner.store, runID)
	if err != nil || !found {
		if err != nil {
			return workspacecheckpoint.RunState{}, false, err
		}
		return s.WorkspaceCheckpointStore.GetWorkspaceCheckpointRunState(ctx, runID)
	}
	state := workspacecheckpoint.RunState{RunID: runID, WorkspaceID: d.WorkspaceID,
		CurrentCheckpointID: d.LastCheckpointID, UpdatedAt: d.UpdatedAt}
	if d.LastCheckpointID == "" {
		return state, false, nil
	}
	transactions, err := s.WorkspaceCheckpointStore.ListWorkspaceCheckpointTransactions(ctx, runID,
		workspaceCheckpointListLimit)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	for _, transaction := range transactions {
		if transaction.WorkspaceID == d.WorkspaceID && transaction.Status.Terminal() &&
			transaction.AfterCheckpointID == d.LastCheckpointID {
			state.LastTransactionID = transaction.ID
			break
		}
	}
	return state, true, nil
}

func (s *drydockFileBoundaryStore) AdvanceWorkspaceCheckpointRunState(ctx context.Context,
	value workspacecheckpoint.RunState, expected string,
) (workspacecheckpoint.RunState, bool, error) {
	d, found, err := readRunFileDrydock(ctx, s.owner.store, value.RunID)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	if !found || value.WorkspaceID != d.WorkspaceID {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"file boundary does not belong to this Run's Drydock")
	}
	fingerprint := runmutation.Fingerprint("drydock-file-boundary-cursor.v1", value.RunID,
		value.WorkspaceID, value.CurrentCheckpointID, value.LastTransactionID, expected)
	digest := drydockOperationDigest(runworktree.OperationCheckpoint, value.RunID, fingerprint)
	if receipt, replayed, err := s.owner.store.GetDrydockReceiptByOperation(ctx, digest); err != nil {
		return workspacecheckpoint.RunState{}, false, err
	} else if replayed {
		if receipt.DrydockID != d.ID || receipt.RequestFingerprint != fingerprint ||
			receipt.CheckpointID != value.CurrentCheckpointID || receipt.Outcome != runworktree.OutcomeSucceeded {
			return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
				"Drydock file boundary replay binding differs")
		}
		value.UpdatedAt = receipt.CreatedAt
		return value, true, nil
	}
	if d.LastCheckpointID != expected || (d.State != runworktree.StateReady && d.State != runworktree.StateDelivered) {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"Drydock file boundary cursor changed")
	}
	snapshot, err := s.WorkspaceCheckpointStore.GetWorkspaceCheckpointSnapshot(ctx, value.CurrentCheckpointID)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	checkpoint := snapshot.Checkpoint
	kind, trigger := workspacecheckpoint.TransactionFileTool, workspacecheckpoint.TriggerFileTool
	if s.gitMutation {
		kind, trigger = workspacecheckpoint.TransactionGitMutation, workspacecheckpoint.TriggerGitMutation
	}
	run, err := s.owner.store.GetRun(ctx, value.RunID)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	if checkpoint.RunID != value.RunID || checkpoint.MissionID != d.MissionID ||
		checkpoint.SessionID != run.SessionID || checkpoint.WorkspaceID != d.WorkspaceID ||
		checkpoint.ParentCheckpointID != expected {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"Drydock file boundary checkpoint binding differs")
	}
	if value.LastTransactionID != "" {
		transaction, present, err := s.WorkspaceCheckpointStore.GetWorkspaceCheckpointTransaction(ctx, value.LastTransactionID)
		if err != nil {
			return workspacecheckpoint.RunState{}, false, err
		}
		if !present || transaction.RunID != value.RunID ||
			transaction.WorkspaceID != d.WorkspaceID || !transaction.Status.Terminal() ||
			transaction.Kind != kind || checkpoint.Phase != workspacecheckpoint.PhaseAfter ||
			transaction.AfterCheckpointID != checkpoint.ID || transaction.BeforeCheckpointID != expected ||
			!((checkpoint.Trigger == trigger && checkpoint.TriggerReceiptID == transaction.TriggerReceiptID) ||
				(transaction.Status == workspacecheckpoint.TransactionInterrupted && checkpoint.Trigger == workspacecheckpoint.TriggerRewindResult && checkpoint.TriggerReceiptID == transaction.ID)) {
			return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
				"Drydock file boundary transaction binding differs")
		}
	} else if checkpoint.Phase != workspacecheckpoint.PhaseBefore || checkpoint.Trigger != trigger {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"Drydock file boundary requires a before checkpoint")
	}
	verified, _, observed, err := s.owner.loadExactDrydock(ctx, value.RunID, d.Generation, false)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	if verified.ID != d.ID || verified.LastCheckpointID != expected {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"Drydock changed during file checkpoint attribution")
	}
	// The journal may have been sealed before a crash. Never attribute a later
	// external tree to that older checkpoint when completing its cursor write.
	current, err := workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{
		ID: checkpoint.ID, RunID: value.RunID, MissionID: d.MissionID, SessionID: run.SessionID,
		WorkspaceID: d.WorkspaceID, WorkspaceRoot: d.Path,
		Trigger: workspacecheckpoint.TriggerRewindPreflight, Phase: workspacecheckpoint.PhasePreflight,
		TriggerReceiptID: checkpoint.ID, CreatedAt: s.owner.now().UTC(),
	})
	if err != nil {
		return workspacecheckpoint.RunState{}, false, apperror.Normalize(err)
	}
	actual := current.Checkpoint
	if actual.RootFingerprint != checkpoint.RootFingerprint || actual.RootPathSHA256 != checkpoint.RootPathSHA256 ||
		actual.BaseCommit != checkpoint.BaseCommit || actual.Branch != checkpoint.Branch ||
		actual.IndexSHA256 != checkpoint.IndexSHA256 || actual.ManifestSHA256 != checkpoint.ManifestSHA256 {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"Drydock changed after the file checkpoint; refusing to attribute a different tree")
	}
	_, _, confirmed, err := s.owner.loadExactDrydock(ctx, value.RunID, d.Generation, false)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	if confirmed.Binding.Fingerprint() != observed.Binding.Fingerprint() ||
		confirmed.RootFingerprint != observed.RootFingerprint {
		return workspacecheckpoint.RunState{}, false, apperror.New(apperror.CodeConflict,
			"Drydock changed during checkpoint verification")
	}
	before := d.Generation
	previousBinding := d.ExpectedBindingFingerprint
	d.RootFingerprint, d.ExpectedHead = observed.RootFingerprint, observed.Binding.Head
	d.ExpectedBindingFingerprint = observed.Binding.Fingerprint()
	d.LastCheckpointID = checkpoint.ID
	d.Generation++
	d.UpdatedAt = s.owner.now().UTC()
	receipt := s.owner.transitionReceipt(d, before, runworktree.OperationCheckpoint, digest,
		fingerprint, runworktree.OutcomeSucceeded, "", "attributed reviewed file boundary "+strconv.FormatInt(before, 10),
		previousBinding, d.ExpectedBindingFingerprint, "", checkpoint.ID, "")
	receipt.RunID = value.RunID
	updated, replayed, err := s.owner.store.AdvanceDrydock(ctx, d, before, receipt)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	value.UpdatedAt = updated.UpdatedAt
	return value, replayed, nil
}
