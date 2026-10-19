package application

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type ThreadProjectMoveStore interface {
	ThreadStore
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
	LatestRunEventSequence(context.Context, string) (int64, error)
	GetThreadProjectMove(context.Context, domain.ThreadProjectMoveRequest) (domain.ThreadProjectMoveResult, bool, error)
	MoveThreadProject(context.Context, domain.ThreadProjectMoveRequest, string, int64, domain.Mission, domain.Run, domain.RunModeSnapshot, session.Session, []events.Event) (domain.ThreadProjectMoveResult, error)
}
type ThreadProjectMoveService struct {
	store     ThreadProjectMoveStore
	authority *domain.ExecutionPermissionRuntimeAuthority
}

func NewThreadProjectMoveService(store ThreadProjectMoveStore, authority *domain.ExecutionPermissionRuntimeAuthority) *ThreadProjectMoveService {
	return &ThreadProjectMoveService{store: store, authority: authority}
}
func (s *ThreadProjectMoveService) Move(ctx context.Context, r domain.ThreadProjectMoveRequest) (domain.ThreadProjectMoveResult, error) {
	key, err := domain.NormalizeAgentOperationKey(r.OperationKey)
	if r.Version != domain.ThreadProjectMoveVersion || !domain.ValidAgentID(r.ThreadID) || !domain.ValidAgentID(r.RequestedBy) || r.ExpectedVersion < 1 || err != nil || key != r.OperationKey || strings.ContainsAny(key, " \t\r\n") || (r.ProjectWorkspaceID != "" && !domain.ValidAgentID(r.ProjectWorkspaceID)) {
		return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeInvalidArgument, "Project move identity or version is invalid")
	}
	if result, found, err := s.store.GetThreadProjectMove(ctx, r); err != nil || found {
		return result, err
	}
	t, err := s.store.GetThread(ctx, r.ThreadID)
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	old, err := s.store.GetRun(ctx, t.LastRunID)
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	sequence, err := s.store.LatestRunEventSequence(ctx, old.ID)
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	var prepared preparedRun
	if r.ProjectWorkspaceID != "" {
		// Projectless allocation storage is introduced after the move ledger.
		if allocations, ok := s.store.(interface {
			IsProjectlessWorkspace(context.Context, string) (bool, error)
		}); ok {
			internal, err := allocations.IsProjectlessWorkspace(ctx, r.ProjectWorkspaceID)
			if err != nil {
				return domain.ThreadProjectMoveResult{}, err
			}
			if internal {
				return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeInvalidArgument, "Target directory is a local chat folder, not a registered project")
			}
		}
	}
	if r.ProjectWorkspaceID != "" && r.ProjectWorkspaceID != t.WorkspaceID {
		workspace, err := s.store.GetWorkspaceInfo(ctx, r.ProjectWorkspaceID)
		if err != nil {
			return domain.ThreadProjectMoveResult{}, apperror.Normalize(err)
		}
		mission, err := s.store.GetMission(ctx, old.MissionID)
		if err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		mode, err := s.store.GetRunMode(ctx, old.ID)
		if err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		project, rejected, err := projectconfig.ResolveWorkspace(ctx, workspace.RootPath, mission.Profile, old.Budget, toolgateway.TypedActionIDs())
		if err != nil || len(rejected) > 0 {
			return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeFailedPrecondition, "Target project configuration could not be safely loaded")
		}
		instructions, err := projectconfig.DiscoverInstructions(ctx, workspace.RootPath, ".")
		if err != nil {
			return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeFailedPrecondition, "Target project instructions could not be safely loaded")
		}
		var continuity *contextmgr.ContinuitySnapshot
		{
			continuityMission := mission
			// Legacy chats have no source filesystem, but still have durable messages.
			if continuityMission.WorkspaceID == "" {
				continuityMission.WorkspaceID = workspace.ID
			}
			snapshot, err := NewThreadService(s.store).captureThreadContinuity(ctx, old, continuityMission, time.Now().UTC())
			if err != nil {
				return domain.ThreadProjectMoveResult{}, err
			}
			snapshot.WorkspaceID = workspace.ID
			snapshot.ProjectConfigFingerprint = ""
			snapshot.ProjectInstructionsFingerprint = ""
			snapshot.Memories = nil
			snapshot.GitBranch = ""
			snapshot.GitHead = ""
			snapshot.InheritedContext = nil
			snapshot, err = contextmgr.SealContinuitySnapshot(snapshot)
			if err != nil {
				return domain.ThreadProjectMoveResult{}, err
			}
			continuity = &snapshot
		}
		prepared, err = prepareRun(ctx, CreateRunRequest{Goal: mission.Goal, Profile: string(mission.Profile), Surface: string(mode.Surface), Phase: string(mode.Phase), WorkspaceID: workspace.ID, ModelRoute: old.Config.ModelRoute, Interactive: true, Budget: old.Budget, RequestedBudget: old.Config.RequestedBudget, RequestedBy: r.RequestedBy, ProjectConfig: project, ProjectInstructions: &instructions, ContinuityContext: continuity}, nil)
		if err != nil {
			return domain.ThreadProjectMoveResult{}, apperror.Normalize(err)
		}
	}
	result, err := s.store.MoveThreadProject(ctx, r, old.ID, sequence, prepared.Mission, prepared.Run, prepared.Mode, prepared.Session, prepared.InitialEvents)
	if err == nil && prepared.Run.ID != "" && s.authority != nil {
		s.authority.RevokeThread(t.ID)
		s.authority.RevokeRun(old.ID)
	}
	return result, apperror.Normalize(err)
}
