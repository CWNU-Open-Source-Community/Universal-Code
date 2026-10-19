package application

import (
	"context"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/session"
)

type ThreadBranchStore interface {
	GetThread(context.Context, string) (domain.Thread, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetSession(context.Context, string) (session.Session, error)
	GetThreadBranchSource(context.Context, domain.ThreadBranchRequest) (domain.ThreadBranchSource, error)
	GetThreadBranchOperation(context.Context, domain.ThreadBranchRequest) (domain.ThreadBranchRecord, bool, error)
	CreateThreadBranch(context.Context, domain.ThreadBranchRequest, domain.Mission, domain.Run,
		domain.RunModeSnapshot, session.Session, []events.Event) (domain.ThreadBranchRecord, error)
}

type ThreadBranchResult struct {
	Thread        domain.Thread
	Run           domain.Run
	Session       session.Session
	SourceEventID string
	Replayed      bool
}

type ThreadBranchService struct {
	store          ThreadBranchStore
	lifecycleHooks *hooks.Engine
}

func NewThreadBranchService(store ThreadBranchStore) *ThreadBranchService {
	return &ThreadBranchService{store: store}
}

func (s *ThreadBranchService) WithLifecycleHooks(engine *hooks.Engine) *ThreadBranchService {
	s.lifecycleHooks = engine
	return s
}

func (s *ThreadBranchService) Branch(ctx context.Context, request domain.ThreadBranchRequest) (ThreadBranchResult, error) {
	if s == nil || s.store == nil {
		return ThreadBranchResult{}, apperror.New(apperror.CodeFailedPrecondition, "Thread branch store is unavailable")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || strings.ContainsAny(key, " \t\r\n") ||
		request.Version != domain.ThreadBranchProtocolVersion || !domain.ValidAgentID(request.ThreadID) ||
		!domain.ValidAgentID(request.SourceEventID) || !domain.ValidAgentID(request.RequestedBy) {
		return ThreadBranchResult{}, apperror.New(apperror.CodeInvalidArgument, "Thread branch identity or version is invalid")
	}
	if stored, found, err := s.store.GetThreadBranchOperation(ctx, request); err != nil {
		return ThreadBranchResult{}, err
	} else if found {
		return s.load(ctx, stored)
	}
	source, err := s.store.GetThreadBranchSource(ctx, request)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	run, err := s.store.GetRun(ctx, source.RunID)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	prepared, err := prepareRun(ctx, CreateRunRequest{
		Goal: mission.Goal, Profile: string(mission.Profile), WorkspaceID: mission.WorkspaceID,
		Surface: string(mode.Surface), Phase: string(mode.Phase), ModelRoute: run.Config.ModelRoute,
		Interactive: true, Budget: run.Budget, RequestedBudget: run.Config.RequestedBudget,
		RequestedBy: request.RequestedBy, WebAccessMode: string(mode.Scope.WebAccessMode),
		WebAllowedTargets: append([]string(nil), mode.Scope.WebAllowedTargets...),
	}, nil)
	if err != nil {
		return ThreadBranchResult{}, apperror.Normalize(err)
	}
	// Retain the pinned project guidance; execution leases, approvals, continuity
	// snapshots and provider-private runtime state belong to their original Run.
	prepared.Run.Config.ProjectConfig = append([]byte(nil), run.Config.ProjectConfig...)
	prepared.Run.Config.ProjectConfigFingerprint = run.Config.ProjectConfigFingerprint
	prepared.Run.Config.ProjectInstructions = append([]byte(nil), run.Config.ProjectInstructions...)
	prepared.Run.Config.ProjectInstructionsFingerprint = run.Config.ProjectInstructionsFingerprint
	if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.SessionOpened, prepared.Run.ID,
		mission.WorkspaceID, map[string]any{"session_id": prepared.Session.ID, "source": "thread_branch"}); err != nil {
		return ThreadBranchResult{}, err
	}
	stored, err := s.store.CreateThreadBranch(ctx, request, prepared.Mission, prepared.Run, prepared.Mode, prepared.Session, prepared.InitialEvents)
	if err != nil {
		return ThreadBranchResult{}, apperror.Normalize(err)
	}
	return s.load(ctx, stored)
}

func (s *ThreadBranchService) load(ctx context.Context, record domain.ThreadBranchRecord) (ThreadBranchResult, error) {
	thread, err := s.store.GetThread(ctx, record.ThreadID)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	run, err := s.store.GetRun(ctx, record.RunID)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	sess, err := s.store.GetSession(ctx, record.SessionID)
	if err != nil {
		return ThreadBranchResult{}, err
	}
	return ThreadBranchResult{Thread: thread, Run: run, Session: sess, SourceEventID: record.SourceEventID, Replayed: record.Replayed}, nil
}
