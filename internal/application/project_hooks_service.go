package application

import (
	"context"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspaceidentity"
)

const ProjectHooksVersion = "project-hooks.v1"

type ProjectHookView struct {
	Event     string `json:"event"`
	CommandID string `json:"command_id"`
	Target    string `json:"target"`
}

type ProjectHooksView struct {
	Version     string            `json:"version"`
	ThreadID    string            `json:"thread_id"`
	WorkspaceID string            `json:"workspace_id"`
	Fingerprint string            `json:"fingerprint"`
	Trusted     bool              `json:"trusted"`
	Revision    int64             `json:"revision"`
	MatchesTask bool              `json:"matches_task"`
	Hooks       []ProjectHookView `json:"hooks"`
	State       string            `json:"state"`
	Message     string            `json:"message"`
}

type ProjectHooksReviewRequest struct {
	Version             string `json:"version"`
	ExpectedFingerprint string `json:"expected_fingerprint"`
	ExpectedRevision    int64  `json:"expected_revision"`
}

type projectHooksStore interface {
	RunFileWorkspaceStore
	GetThread(context.Context, string) (domain.Thread, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetProjectHookTrust(context.Context, string) (domain.ProjectHookTrust, bool, error)
	ReviewProjectHooks(context.Context, domain.ProjectHookTrust, int64) (domain.ProjectHookTrust, error)
}

type ProjectHooksService struct {
	store    projectHooksStore
	drydocks *RunWorktreeService
}

func NewProjectHooksService(store projectHooksStore) *ProjectHooksService {
	return &ProjectHooksService{store: store}
}
func (s *ProjectHooksService) WithRunWorktree(d *RunWorktreeService) *ProjectHooksService {
	s.drydocks = d
	return s
}

type projectHooksBinding struct {
	run                          domain.Run
	workspaceID, rootFingerprint string
	rootPath                     string
	profile                      domain.Profile
	current, pinned              *projectconfig.Effective
	trust                        domain.ProjectHookTrust
	found                        bool
}

func (b projectHooksBinding) reviewFingerprint() string {
	configuration := b.current.HooksFingerprint()
	if configuration == "" {
		return ""
	}
	return runmutation.Fingerprint(ProjectHooksVersion, b.workspaceID, b.rootFingerprint, configuration)
}

func (s *ProjectHooksService) bind(ctx context.Context, runID string) (projectHooksBinding, error) {
	var b projectHooksBinding
	var err error
	b.run, err = s.store.GetRun(ctx, runID)
	if err != nil {
		return b, apperror.Normalize(err)
	}
	b.pinned, err = projectconfig.Pinned(b.run.Config)
	if err != nil {
		return b, err
	}
	mission, err := s.store.GetMission(ctx, b.run.MissionID)
	if err != nil {
		return b, apperror.Normalize(err)
	}
	files, err := ResolveRunFileWorkspace(ctx, s.store, b.run, mission, s.drydocks)
	if err != nil {
		return b, err
	}
	b.workspaceID = files.Workspace.ID
	b.rootPath, b.profile = files.Workspace.RootPath, mission.Profile
	b.rootFingerprint, err = workspaceidentity.Fingerprint(files.Workspace.RootPath)
	if err != nil {
		return b, apperror.New(apperror.CodeFailedPrecondition, "project hook workspace identity is unavailable")
	}
	b.trust, b.found, err = s.store.GetProjectHookTrust(ctx, b.workspaceID)
	return b, apperror.Normalize(err)
}

func (b *projectHooksBinding) loadConfiguration(ctx context.Context) error {
	var rejected []projectconfig.Rejection
	var err error
	b.current, rejected, err = projectconfig.ResolveWorkspace(ctx, b.rootPath, b.profile, b.run.Config.CreationBudget(), toolgateway.TypedActionIDs())
	if err != nil || len(rejected) > 0 {
		return apperror.New(apperror.CodeFailedPrecondition, "project hook configuration could not be safely loaded; check .prayu/config.yaml")
	}
	return nil
}

func (s *ProjectHooksService) threadRun(ctx context.Context, threadID string) (string, error) {
	if !domain.ValidAgentID(threadID) || strings.TrimSpace(threadID) != threadID {
		return "", apperror.New(apperror.CodeInvalidArgument, "task identity is invalid")
	}
	thread, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	if thread.Status == domain.ThreadDeleted {
		return "", apperror.New(apperror.CodeNotFound, "task was deleted")
	}
	if thread.ActiveRunID != "" {
		return thread.ActiveRunID, nil
	}
	return thread.LastRunID, nil
}

func projectHooksView(threadID string, b projectHooksBinding) ProjectHooksView {
	fingerprint := b.current.HooksFingerprint()
	v := ProjectHooksView{Version: ProjectHooksVersion, ThreadID: threadID, WorkspaceID: b.workspaceID, Fingerprint: b.reviewFingerprint(),
		Revision: b.trust.Revision, MatchesTask: fingerprint == b.pinned.HooksFingerprint(), Hooks: []ProjectHookView{}, State: "not_configured", Message: "项目尚未配置自动格式化或结束前测试。"}
	if fingerprint == "" {
		return v
	}
	if b.current.Hooks.AfterEditFormat {
		v.Hooks = append(v.Hooks, ProjectHookView{Event: "after_edit", CommandID: b.current.FormatCommandID, Target: "已应用的 .go 文件"})
	}
	if b.current.Hooks.BeforeFinishTest {
		v.Hooks = append(v.Hooks, ProjectHookView{Event: "before_finish", CommandID: b.current.TestCommandID, Target: "."})
	}
	v.State, v.Message = "needs_review", "首次使用前请审阅这组项目动作。后续任务会沿用相同配置的信任。"
	if b.found {
		switch {
		case !b.trust.Trusted:
			v.State, v.Message = "disabled", "项目自动动作已停用。"
		case b.trust.RootFingerprint != b.rootFingerprint || b.trust.Fingerprint != fingerprint:
			v.State, v.Message = "changed", "项目动作配置已变化，需要重新审阅。"
		default:
			v.Trusted = true
			v.State, v.Message = "trusted", "已信任当前配置，执行继续遵守任务权限和预算。"
		}
	}
	if !v.MatchesTask {
		v.Message += " 当前任务固定了旧项目配置；请在新任务中使用此配置。"
	}
	return v
}

func (s *ProjectHooksService) Get(ctx context.Context, threadID string) (ProjectHooksView, error) {
	runID, err := s.threadRun(ctx, threadID)
	if err != nil {
		return ProjectHooksView{}, err
	}
	b, err := s.bind(ctx, runID)
	if err != nil {
		return ProjectHooksView{}, err
	}
	if err := b.loadConfiguration(ctx); err != nil {
		return ProjectHooksView{}, err
	}
	return projectHooksView(threadID, b), nil
}

func (s *ProjectHooksService) Review(ctx context.Context, threadID, action string, r ProjectHooksReviewRequest) (ProjectHooksView, error) {
	if r.Version != ProjectHooksVersion || r.ExpectedRevision < 0 || (action != "trust" && action != "disable") {
		return ProjectHooksView{}, apperror.New(apperror.CodeInvalidArgument, "project hook review is invalid")
	}
	runID, err := s.threadRun(ctx, threadID)
	if err != nil {
		return ProjectHooksView{}, err
	}
	b, err := s.bind(ctx, runID)
	if err != nil {
		return ProjectHooksView{}, err
	}
	if err := b.loadConfiguration(ctx); err != nil {
		return ProjectHooksView{}, err
	}
	fingerprint := b.current.HooksFingerprint()
	if fingerprint == "" || b.reviewFingerprint() != r.ExpectedFingerprint {
		return ProjectHooksView{}, apperror.New(apperror.CodeConflict, "project hook definition changed; refresh before confirming")
	}
	b.trust, err = s.store.ReviewProjectHooks(ctx, domain.ProjectHookTrust{WorkspaceID: b.workspaceID, RootFingerprint: b.rootFingerprint, Fingerprint: fingerprint, Trusted: action == "trust"}, r.ExpectedRevision)
	if err != nil {
		return ProjectHooksView{}, err
	}
	b.found = true
	return projectHooksView(threadID, b), nil
}

func (s *ProjectHooksService) AuthorizeProjectHooks(ctx context.Context, runID string) (ProjectHookSelection, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return ProjectHookSelection{}, apperror.Normalize(err)
	}
	pinned, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return ProjectHookSelection{}, err
	}
	if pinned.HooksFingerprint() == "" {
		return ProjectHookSelection{}, nil
	}
	b, err := s.bind(ctx, runID)
	if err != nil {
		return ProjectHookSelection{}, err
	}
	if b.found && !b.trust.Trusted {
		return ProjectHookSelection{}, nil
	}
	if err := b.loadConfiguration(ctx); err != nil {
		return ProjectHookSelection{}, err
	}
	fingerprint := b.current.HooksFingerprint()
	if fingerprint == "" || fingerprint != pinned.HooksFingerprint() {
		return ProjectHookSelection{}, apperror.New(apperror.CodeFailedPrecondition, "project hook configuration changed; review it and use a task with the current project configuration")
	}
	if !b.found || b.trust.Fingerprint != fingerprint || b.trust.RootFingerprint != b.rootFingerprint {
		return ProjectHookSelection{}, apperror.New(apperror.CodeFailedPrecondition, "project hooks require review of the current definition")
	}
	return ProjectHookSelection{Fingerprint: fingerprint, AfterEditFormat: pinned.Hooks.AfterEditFormat, BeforeFinishTest: pinned.Hooks.BeforeFinishTest}, nil
}
