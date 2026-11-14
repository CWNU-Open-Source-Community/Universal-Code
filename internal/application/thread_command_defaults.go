package application

import (
	"context"
	"fmt"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runworktree"
)

type threadCommandRuntimeInventory interface {
	InstalledCommandRuntimeAdapters() []commandruntimeadapter.Identity
}

type threadCommandBackendReadiness interface {
	commandProfileReady(context.Context, string, domain.RunExecutionProfile) (bool, error)
}

type threadCommandDefaults struct {
	runtime threadCommandRuntimeInventory
}

// WithDefaultCommandRuntime opts a product host into ordinary local execution.
// Server/safe-view composition keeps the initial Preview unless it opts in.
// Approval mode, command review, owned workspaces and dispatch remain unchanged.
func (s *ThreadTurnService) WithDefaultCommandRuntime(runtime threadCommandRuntimeInventory) *ThreadTurnService {
	if s != nil {
		s.commandDefaults = &threadCommandDefaults{runtime: runtime}
	}
	return s
}

func (s *ThreadTurnService) commandProfileForTurn(ctx context.Context, run domain.Run) (domain.RunExecutionProfileSnapshot, bool, error) {
	if s.commandDefaults == nil {
		return domain.RunExecutionProfileSnapshot{}, false, nil
	}
	mode, err := s.threads.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if mode.Surface != domain.ExecutionSurfaceCode || mode.Phase != domain.ExecutionPhaseDeliver {
		return domain.RunExecutionProfileSnapshot{}, false, nil
	}
	store, ok := s.threads.store.(RunExecutionProfileStore)
	if !ok {
		return domain.RunExecutionProfileSnapshot{}, false, apperror.New(apperror.CodeFailedPrecondition, "This app cannot prepare command execution settings. Update the desktop app before retrying.")
	}
	profile, err := store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return domain.RunExecutionProfileSnapshot{}, false, err
	}
	if profile.Profile == domain.RunExecutionProfilePreview && !profile.IsDefaultPreview() {
		return profile, false, nil // An explicitly selected process-free environment.
	}
	target := profile.Profile
	if profile.IsDefaultPreview() {
		target = domain.RunExecutionProfileLocal
	}
	mission, err := s.threads.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return profile, false, err
	}
	if mission.WorkspaceID == "" {
		return profile, false, apperror.New(apperror.CodeFailedPrecondition, "Choose a project folder for this chat before running commands.")
	}
	owned, err := runHasOwnedFileWorkspace(ctx, s.threads.store, run.ID)
	if err != nil {
		return profile, false, err
	}
	if owned {
		workspace, found, err := readRunFileDrydock(ctx, s.threads.store, run.ID)
		if err != nil {
			return profile, false, err
		}
		if !found || (workspace.State != runworktree.StateReady && workspace.State != runworktree.StateDelivered) {
			return profile, false, apperror.New(apperror.CodeFailedPrecondition, "The selected isolated workspace is not ready. Restore its execution environment in advanced settings before retrying.")
		}
		if err := requireCurrentRunFileDrydock(ctx, s.threads.store, run.ID, workspace); err != nil {
			return profile, false, err
		}
	}
	if s.commandDefaults.runtime != nil {
		for _, adapter := range s.commandDefaults.runtime.InstalledCommandRuntimeAdapters() {
			if adapter.Executable() && commandRuntimeExecutionProfile(adapter) == target &&
				((adapter.Kind == commandruntimeadapter.KindHostUnsandboxed && !owned) ||
					(adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace && owned)) {
				if checker, ok := s.commandDefaults.runtime.(threadCommandBackendReadiness); ok {
					ready, err := checker.commandProfileReady(ctx, run.ID, target)
					if err != nil {
						return profile, false, err
					}
					if !ready {
						break
					}
				}
				return profile, true, nil
			}
		}
	}
	return profile, false, apperror.New(apperror.CodeFailedPrecondition,
		fmt.Sprintf("The %s command environment is unavailable in this app. Enable its runtime in desktop settings or select a ready environment in advanced execution settings, then retry.", target))
}

// Read backend readiness without acquiring a lease, starting a Job, changing
// approval or substituting another environment. Dispatch still rechecks it.
func (s *CommandRuntimeService) commandProfileReady(ctx context.Context, runID string, profile domain.RunExecutionProfile) (bool, error) {
	adapter, installed := s.InstalledCommandRuntimeAdapter()
	if !installed || commandRuntimeExecutionProfile(adapter) != profile {
		return false, nil
	}
	owned, err := runHasOwnedFileWorkspace(ctx, s.store, runID)
	if err != nil {
		return false, err
	}
	if adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		return !owned, nil
	}
	if !owned {
		return false, nil
	}
	if checker, ok := s.sandbox.(commandRuntimeSandboxReadiness); ok {
		return checker.Ready(ctx, runID)
	}
	return true, nil
}

func (m *CommandRuntimeMultiplexer) commandProfileReady(ctx context.Context, runID string, profile domain.RunExecutionProfile) (bool, error) {
	for _, service := range m.adapters {
		ready, err := service.commandProfileReady(ctx, runID, profile)
		if err != nil || ready {
			return ready, err
		}
	}
	return false, nil
}

func (s *ThreadTurnService) defaultCommandNeedsSuccessor(ctx context.Context, run domain.Run) (bool, error) {
	profile, enabled, err := s.commandProfileForTurn(ctx, run)
	return enabled && profile.IsDefaultPreview() && run.Status == domain.RunRunning, err
}

func (s *ThreadTurnService) prepareDefaultCommandProfile(ctx context.Context, run domain.Run) error {
	profile, enabled, err := s.commandProfileForTurn(ctx, run)
	if err != nil || !enabled || !profile.IsDefaultPreview() {
		return err
	}
	if run.Status != domain.RunCreated && run.Status != domain.RunPaused {
		return apperror.New(apperror.CodeFailedPrecondition,
			"This chat needs a new idle execution before local commands can start. Finish the current task or approval, then send the next message.")
	}
	store := s.threads.store.(RunExecutionProfileStore)
	_, err = NewRunExecutionProfileService(store).Change(ctx, ChangeRunExecutionProfileRequest{
		RunID: run.ID, Profile: string(domain.RunExecutionProfileLocal),
		OperationKey: "desktop-default-command-runtime-" + run.ID,
		RequestedBy:  "desktop_default", Reason: "Use the desktop local command runtime; chat approval mode controls each operation",
	})
	return err
}
