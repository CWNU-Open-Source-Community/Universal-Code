package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

const TaskConfigurationVersion = "task_configuration.v1"

type TaskConfigurationRequest struct {
	WorkspaceID string                     `json:"workspace_id"`
	Profile     string                     `json:"profile,omitempty"`
	Budget      *domain.TaskBudgetSettings `json:"budget,omitempty"`
}

type ConfigurationSource struct {
	Field  string `json:"field"`
	Source string `json:"source"`
}

// The projection contains exclusion counts and validated exact Skill identities,
// never source bytes, credentials or host filesystem locations.
type ProjectConfigurationView struct {
	Protocol             string   `json:"protocol"`
	ReadOnly             bool     `json:"read_only"`
	AllowedProfiles      []string `json:"allowed_profiles"`
	ExcludedPathCount    int      `json:"excluded_path_count"`
	SkillSuggestionCount int      `json:"skill_suggestion_count"`
	SkillSuggestions     []string `json:"skill_suggestions,omitempty"`
	TestCommandID        string   `json:"test_command_id,omitempty"`
	FormatCommandID      string   `json:"format_command_id,omitempty"`
}

type TaskConfigurationView struct {
	Version            string                    `json:"version"`
	WorkspaceID        string                    `json:"workspace_id"`
	Profile            string                    `json:"profile"`
	RequestedBudget    domain.Budget             `json:"requested_budget"`
	Budget             domain.Budget             `json:"budget"`
	Sources            []ConfigurationSource     `json:"sources"`
	ProjectDisposition string                    `json:"project_disposition"`
	Project            *ProjectConfigurationView `json:"project,omitempty"`
	ProjectFingerprint string                    `json:"project_fingerprint,omitempty"`
	Rejections         []projectconfig.Rejection `json:"rejections"`
	Fingerprint        string                    `json:"fingerprint,omitempty"`
	CapabilityGrant    bool                      `json:"capability_grant"`
}

type taskConfigurationStore interface {
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
}

type TaskConfigurationService struct{ store taskConfigurationStore }

func NewTaskConfigurationService(store taskConfigurationStore) *TaskConfigurationService {
	return &TaskConfigurationService{store: store}
}

func (s *TaskConfigurationService) Preview(ctx context.Context, request TaskConfigurationRequest) (TaskConfigurationView, error) {
	if s == nil || s.store == nil {
		return TaskConfigurationView{}, apperror.New(apperror.CodeFailedPrecondition, "configuration store is required")
	}
	if !domain.ValidAgentID(request.WorkspaceID) || strings.TrimSpace(request.WorkspaceID) != request.WorkspaceID {
		return TaskConfigurationView{}, apperror.New(apperror.CodeInvalidArgument, "configuration workspace id is invalid")
	}
	profile := domain.ProfileCode
	if request.Profile != "" {
		parsed, err := domain.ParseProfile(request.Profile)
		if err != nil || string(parsed) != request.Profile {
			return TaskConfigurationView{}, apperror.New(apperror.CodeInvalidArgument, "configuration profile is invalid")
		}
		profile = parsed
	}
	budget, err := request.Budget.Normalize()
	if err != nil {
		return TaskConfigurationView{}, apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, request.WorkspaceID)
	if err != nil {
		return TaskConfigurationView{}, apperror.Normalize(err)
	}
	if workspace.ID != request.WorkspaceID || workspace.RootPath == "" {
		return TaskConfigurationView{}, apperror.New(apperror.CodeFailedPrecondition, "configuration workspace record is invalid")
	}
	project, rejected, loadErr := projectconfig.ResolveWorkspace(ctx, workspace.RootPath, profile, budget, toolgateway.TypedActionIDs())
	if loadErr != nil {
		if ctx.Err() != nil {
			return TaskConfigurationView{}, ctx.Err()
		}
		// Decoder errors may contain repository secrets and absolute paths.
		// Return only a stable rejection and withhold any partial effective view.
		return configurationView(request.WorkspaceID, profile, budget, nil,
			[]projectconfig.Rejection{{Field: "project_config", Reason: "project configuration could not be safely loaded; fix .prayu/config.yaml"}}), nil
	}
	return configurationView(request.WorkspaceID, profile, budget, project, rejected), nil
}

func configurationView(workspaceID string, profile domain.Profile, requested domain.Budget,
	project *projectconfig.Effective, rejected []projectconfig.Rejection,
) TaskConfigurationView {
	view := TaskConfigurationView{Version: TaskConfigurationVersion, WorkspaceID: workspaceID,
		Profile: string(profile), RequestedBudget: requested, Budget: requested,
		Sources: []ConfigurationSource{}, Rejections: append([]projectconfig.Rejection{}, rejected...), ProjectDisposition: "absent"}
	if len(rejected) != 0 {
		view.ProjectDisposition = "rejected"
		return view
	}
	defaults := domain.DefaultBudget()
	for _, field := range []struct {
		name   string
		custom bool
	}{
		{"budget.max_turns", requested.MaxTurns != defaults.MaxTurns},
		{"budget.max_tokens", requested.MaxTokens != defaults.MaxTokens},
		{"budget.max_tool_calls", requested.MaxToolCalls != defaults.MaxToolCalls},
		{"budget.max_cost_usd", requested.MaxCostUSD != defaults.MaxCostUSD},
		{"budget.timeout_seconds", requested.TimeoutSeconds != defaults.TimeoutSeconds},
	} {
		source := "default"
		if field.custom {
			source = "operator"
		}
		view.Sources = append(view.Sources, ConfigurationSource{Field: field.name, Source: source})
	}
	if project != nil {
		view.ProjectDisposition = "applied"
		view.ProjectFingerprint = project.Fingerprint()
		view.Project = &ProjectConfigurationView{Protocol: project.Protocol, ReadOnly: project.ReadOnly,
			AllowedProfiles: append([]string{}, project.AllowedProfiles...), ExcludedPathCount: len(project.ExcludePaths),
			SkillSuggestionCount: len(project.SkillSuggestions), SkillSuggestions: append([]string{}, project.SkillSuggestions...), TestCommandID: project.TestCommandID, FormatCommandID: project.FormatCommandID}
		if project.MaxTurns > 0 && project.MaxTurns < view.Budget.MaxTurns {
			view.Budget.MaxTurns = project.MaxTurns
			view.Sources[0].Source = "project"
		}
		if project.MaxToolCalls > 0 && int64(project.MaxToolCalls) < view.Budget.MaxToolCalls {
			view.Budget.MaxToolCalls = int64(project.MaxToolCalls)
			view.Sources[2].Source = "project"
		}
		for _, field := range []struct {
			name     string
			supplied bool
		}{
			{"read_only", project.ReadOnly}, {"allowed_profiles", len(project.AllowedProfiles) > 0},
			{"exclude_paths", len(project.ExcludePaths) > 0}, {"skill_suggestions", len(project.SkillSuggestions) > 0},
			{"test_command_id", project.TestCommandID != ""}, {"format_command_id", project.FormatCommandID != ""},
		} {
			source := "default"
			if field.supplied {
				source = "project"
			}
			view.Sources = append(view.Sources, ConfigurationSource{Field: field.name, Source: source})
		}
	}
	raw, _ := json.Marshal(view)
	digest := sha256.Sum256(raw)
	view.Fingerprint = hex.EncodeToString(digest[:])
	return view
}

// PinnedTaskConfiguration projects only stored facts. It never reloads the
// project file, including for successor Runs or creation replay.
func PinnedTaskConfiguration(run domain.Run, workspaceID string, profile domain.Profile) (TaskConfigurationView, error) {
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return TaskConfigurationView{}, err
	}
	requested := run.Config.CreationBudget()
	if run.Config.RequestedBudget == nil {
		requested = run.Budget
	}
	view := configurationView(workspaceID, profile, requested, project, nil)
	if run.Config.RequestedBudget == nil {
		for index := range view.Sources {
			view.Sources[index].Source = "snapshot"
		}
		view.Fingerprint = ""
		raw, _ := json.Marshal(view)
		digest := sha256.Sum256(raw)
		view.Fingerprint = hex.EncodeToString(digest[:])
	}
	if view.Budget != run.Budget {
		return TaskConfigurationView{}, apperror.New(apperror.CodeConflict, "pinned task budget binding is invalid")
	}
	return view, nil
}
