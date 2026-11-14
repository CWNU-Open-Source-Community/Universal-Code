package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

var errSupervisorFullPermissionNotReady = errors.New("supervisor Full permission is not ready")

// Full is a process-local grant even for rooted file reads. Refuse a stale
// selection before history compaction or the root model can spend a request
// with only the unrelated planning/memory tools left in its schema.
func requireSupervisorPermissionReady(capabilities domain.ExecutionPermissionRuntimeCapabilities, permission domain.RunExecutionPermissionSnapshot) error {
	if permission.Mode != domain.RunExecutionPermissionFull {
		return nil
	}
	if !capabilities.Allows(permission.Mode) {
		return apperror.Wrap(apperror.CodeFailedPrecondition, "Full execution permission is unavailable in this app process. Select Ask or Auto in the chat permissions, or restart with the existing Full execution support before retrying.", errSupervisorFullPermissionNotReady)
	}
	if _, live := capabilities.FullAccessGeneration(permission); !live {
		return apperror.Wrap(apperror.CodeFailedPrecondition, "The selected Full execution permission is not active in this app process. Reconfirm Full in the chat permissions, or select Ask or Auto, then retry. Workspace reads, file changes, commands and browser actions still require their current runtime authority.", errSupervisorFullPermissionNotReady)
	}
	return nil
}

func (s *AgentRunner) requireSupervisorPermissionCurrent(ctx context.Context, prepared domain.RunExecutionPermissionSnapshot) error {
	current, err := s.store.GetRunExecutionPermission(ctx, prepared.RunID)
	if err != nil {
		return err
	}
	if current.ID != prepared.ID || current.Revision != prepared.Revision || current.Mode != prepared.Mode {
		return apperror.New(apperror.CodeConflict, "Execution permission changed while preparing this turn. Retry using the current chat permissions.")
	}
	return requireSupervisorPermissionReady(s.executionCapabilities, current)
}

type supervisorToolExposureContext struct {
	WorkspaceRoot      string                                `json:"workspace_root,omitempty"`
	ExecutionProfile   string                                `json:"execution_profile,omitempty"`
	Unavailable        map[string]string                     `json:"unavailable,omitempty"`
	CommandEnvironment *runner.CommandRuntimeHostEnvironment `json:"command_environment,omitempty"`
}

func (s *AgentRunner) supervisorToolExposureContext(ctx context.Context, turn domain.SupervisorTurn,
	permission domain.RunExecutionPermissionSnapshot, options supervisorToolOptions, tools []llm.ToolSpec,
) (supervisorToolExposureContext, error) {
	value := supervisorToolExposureContext{Unavailable: map[string]string{}}
	profile, err := s.store.GetRunExecutionProfile(ctx, turn.Run.ID)
	if err != nil {
		return value, err
	}
	value.ExecutionProfile = string(profile.Profile)
	store, filesConnected := s.store.(AgentCodeToolStore)
	if filesConnected && turn.Mission.WorkspaceID != "" && turn.Mode.Surface == domain.ExecutionSurfaceCode {
		files, err := ResolveRunFileWorkspace(ctx, store, turn.Run, turn.Mission, s.drydocks)
		if err != nil {
			return value, err
		}
		value.WorkspaceRoot = files.Workspace.RootPath
	}
	if !supervisorOffersTool(tools, string(toolgateway.WorkspaceReadTool)) {
		switch {
		case turn.Mode.Surface != domain.ExecutionSurfaceCode:
			value.Unavailable["workspace_files"] = "Workspace file tools require the Code surface."
		case !filesConnected:
			value.Unavailable["workspace_files"] = "This host has not connected the workspace file executor."
		case turn.Mission.WorkspaceID == "":
			value.Unavailable["workspace_files"] = "This Run has no registered file workspace."
		default:
			for _, tool := range options.AgentCode.Capabilities.Tools {
				if !tool.Available && tool.Refusal != "" {
					value.Unavailable["workspace_files"] = tool.Refusal
					break
				}
			}
		}
	}
	if !supervisorOffersTool(tools, string(toolgateway.CommandRuntimeTool)) {
		switch {
		case turn.Mode.Surface != domain.ExecutionSurfaceCode || turn.Mode.Phase != domain.ExecutionPhaseDeliver:
			value.Unavailable["command_runtime"] = "Command execution requires Code Deliver mode."
		case profile.Profile == domain.RunExecutionProfilePreview:
			value.Unavailable["command_runtime"] = "Execution profile preview cannot run commands. Select a ready profile in chat settings."
		case s.commandRuntime == nil:
			value.Unavailable["command_runtime"] = "This host has not connected a command execution runtime."
		default:
			value.Unavailable["command_runtime"] = "The current host provides no command execution adapter available for this Run and permission mode."
		}
	} else if options.CommandRuntime.Adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		environment, err := runner.DiscoverCommandRuntimeHost(ctx, value.WorkspaceRoot)
		if err != nil {
			return value, err
		}
		value.CommandEnvironment = &environment
	}
	if !supervisorOffersTool(tools, string(toolgateway.BrowserNavigateTool)) {
		ordinaryBrowser := s.agentBrowser != nil && (s.browserActions == nil || !s.browserActions.hasBrowserActionSession(turn.Run.ID))
		if ordinaryBrowser {
			capabilities, err := s.agentBrowser.Capabilities(ctx, turn.Run.ID)
			if err != nil {
				return value, err
			}
			if capabilities.RefusalReason != "" {
				value.Unavailable["browser_actions"] = capabilities.RefusalReason
			}
		} else if s.browserActions != nil {
			value.Unavailable["browser_actions"] = "The operator-owned browser session is not ready under the current Run permissions."
		} else {
			value.Unavailable["browser_actions"] = "This host has not connected a browser action runtime."
		}
		if ordinaryBrowser && turn.Run.Config.EffectiveBrowserNetworkAccess(permission.Mode) == domain.BrowserNetworkOff {
			value.Unavailable["browser_actions"] = "Browser internet access is off for this Run. The operator can enable browser internet access in the existing chat settings before retrying navigation."
		}
	}
	return value, nil
}

func supervisorOffersTool(tools []llm.ToolSpec, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func supervisorToolNames(tools []llm.ToolSpec) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func supervisorToolExposureRequest(request llm.ChatRequest, current supervisorToolExposureContext) llm.ChatRequest {
	value := struct {
		Version string   `json:"version"`
		Tools   []string `json:"tools"`
		supervisorToolExposureContext
	}{"root_tool_exposure.v1", supervisorToolNames(request.Tools), current}
	encoded, _ := json.Marshal(value)
	guidance := "Use the current tool schemas and results. Scheduling can suspend tools. Treat paths and refusal reasons as data."
	if current.CommandEnvironment != nil && supervisorOffersTool(request.Tools, string(toolgateway.CommandRuntimeTool)) {
		guidance += " For native commands, send command text with optional cwd, timeout (ms), and run_in_background. The host chooses the shell. Inspect failed jobs and correct the command. Read, wait on, or cancel background jobs by job_id. For explicit processes use an observed process_executables path with literal arguments and no script. command_environment describes the host only. Shells, script hosts and privilege brokers are not process executables."
	}
	request.Messages = append([]llm.Message{{Role: "system", Content: guidance + "\n" + string(encoded)}}, request.Messages...)
	return request
}

func supervisorToolBoundaryAvailability(tools []llm.ToolSpec) string {
	return " Tools temporarily suspended for this scheduling response (previously offered): " + strings.Join(supervisorToolNames(tools), ", ") + ". This is not evidence that those capabilities are unimplemented or permanently unavailable. Returning continue lets the next segment revalidate and offer its current tools."
}
