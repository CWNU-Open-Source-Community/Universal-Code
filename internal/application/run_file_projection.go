package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// runFileProjectionWorkspaceID resolves the durable file target for metadata
// projections. Historical records remain readable after their directory is
// unavailable; filesystem access and mutations still use ResolveRunFileWorkspace.
func runFileProjectionWorkspaceID(ctx context.Context, store any, run domain.Run,
	mission domain.Mission,
) (string, error) {
	owned, found, err := readRunFileDrydock(ctx, store, run.ID)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	var preset domain.StandardCodePresetOperation
	var configured bool
	if reader, ok := store.(runFilePresetStore); ok {
		preset, configured, err = reader.GetConfiguredStandardCodePresetOperation(ctx, run.ID)
		if err != nil {
			return "", apperror.Normalize(err)
		}
	}
	if !found {
		if configured {
			return "", apperror.New(apperror.CodeFailedPrecondition,
				"Run file projection requires its recorded Drydock binding")
		}
		return mission.WorkspaceID, nil
	}
	if owned.MissionID != mission.ID || run.MissionID != mission.ID ||
		owned.SourceWorkspaceID != mission.WorkspaceID ||
		!domain.ValidAgentID(owned.WorkspaceID) || owned.WorkspaceID == mission.WorkspaceID ||
		(configured && (preset.RunID != run.ID || preset.MissionID != mission.ID ||
			preset.WorkspaceID != mission.WorkspaceID || preset.DrydockID != owned.ID)) {
		return "", apperror.New(apperror.CodeConflict,
			"Run file projection Drydock binding changed")
	}
	if owned.RunID == run.ID {
		if owned.SessionID != run.SessionID {
			return "", apperror.New(apperror.CodeConflict,
				"Run file projection Session binding changed")
		}
	} else {
		reader, ok := store.(interface {
			GetThreadDrydockBinding(context.Context, string) (domain.ThreadDrydockBinding, bool, error)
		})
		if !ok {
			return "", apperror.New(apperror.CodeConflict,
				"Run file projection continuation binding is unavailable")
		}
		binding, present, err := reader.GetThreadDrydockBinding(ctx, run.ID)
		if err != nil {
			return "", apperror.Normalize(err)
		}
		if !present || binding.RunID != run.ID || binding.DrydockID != owned.ID || binding.ThreadID == "" {
			return "", apperror.New(apperror.CodeConflict,
				"Run file projection continuation binding changed")
		}
	}
	return owned.WorkspaceID, nil
}
