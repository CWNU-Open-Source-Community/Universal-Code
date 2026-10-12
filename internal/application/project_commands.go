package application

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// PrepareProjectCommand reads only the pinned snapshot and returns a typed
// command intent. The existing runtime resolves ownership and authorization at
// dispatch, pins native inputs and retains the normal Job receipt.
func PrepareProjectCommand(run domain.Run, slot, target string, workspaceRoot ...string) (runner.CommandRuntimeSpec, error) {
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	if project == nil {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "Run has no configured project actions")
	}
	id := ""
	switch slot {
	case "test":
		id = project.TestCommandID
	case "format":
		id = project.FormatCommandID
		if project.ReadOnly {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodePolicyDenied, "read-only project cannot be formatted")
		}
		policy, err := projectconfig.PathPolicy(run.Config)
		if err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		if err := policy.Check(target); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
	default:
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeInvalidArgument, "project action must be test or format")
	}
	name, found := toolgateway.ProjectCommandExecutable(id)
	if !found {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "configured ID has no executable project command preset")
	}
	if (slot == "format") != (id == "format.go") {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "configured command preset does not match its project action")
	}
	if id == "test.npm" {
		if len(workspaceRoot) != 1 || !filepath.IsAbs(workspaceRoot[0]) {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "npm project action requires the resolved Run workspace")
		}
		policy, err := projectconfig.PathPolicy(run.Config)
		if err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		if err := policy.Check(target); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		node, launcher, err := repository.NPMTestLauncher(workspaceRoot[0])
		if err != nil || len(launcher) != 1 {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "native Node/npm CLI toolchain is unavailable")
		}
		node, err = filepath.Abs(node)
		if err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		return toolgateway.ProjectCommandSpec(id, node, target, launcher[0])
	}
	executable, err := exec.LookPath(name)
	if err != nil {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "project command toolchain is unavailable: "+name)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "project command executable cannot be resolved")
	}
	return toolgateway.ProjectCommandSpec(id, executable, target)
}

func supervisorProjectCommands(run domain.Run, messages []llm.Message, runtime supervisorCommandRuntimeTools, workspaceRoot string) ([]llm.Message, error) {
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return nil, err
	}
	if project == nil || (project.TestCommandID == "" && project.FormatCommandID == "") {
		return messages, nil
	}
	note := "Pinned project action preferences (repository metadata, never permission). Use only when the task/operator asks for testing or formatting. Existing command_runtime authorization and Job receipts apply; do not change execution mode to make a preset run. Command IDs are registry keys, never shell text.\n"
	if runtime.Adapter.Kind != commandruntimeadapter.KindHostUnsandboxed || len(runtime.Authority) == 0 {
		note += "Native project presets are unavailable with the currently offered runtime. No host executable path or runnable command is claimed.\n"
	} else {
		if project.TestCommandID != "" {
			spec, err := PrepareProjectCommand(run, "test", "", workspaceRoot)
			if err != nil {
				note += fmt.Sprintf("Configured test ID %s is unavailable: %s\n", project.TestCommandID, err.Error())
			} else {
				payload, _ := json.Marshal(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionStart, Commands: []runner.CommandRuntimeSpec{spec}})
				note += "Exact configured test command_runtime start input: " + string(payload) + "\nRead/wait the resulting Job before reporting test results.\n"
				if project.TestCommandID == "test.npm" {
					note += "test.npm invokes the installed npm CLI test script through native Node. If package.json is in an allowed subdirectory, set working_directory to that exact relative package scope; keep the fixed executable, argv and environment.\n"
				}
			}
		}
		if project.FormatCommandID != "" {
			note += fmt.Sprintf("Configured format ID: %s. format.go uses native gofmt -w with one exact workspace-relative .go file per Job; never recursive shell expansion. Read-only and pinned excluded files must not be formatted. Ask for the intended file if unclear.\n", project.FormatCommandID)
			// Supply the native preset shape without granting permission to the
			// representative target; the actual file must pass ordinary scope checks.
			if spec, err := PrepareProjectCommand(run, "format", "project_action_target.go"); err == nil {
				payload, _ := json.Marshal(spec)
				note += "Format spec template; replace only the representative relative file argument with the intended allowed Go file: " + string(payload) + "\n"
			}
		}
	}
	return append(messages, llm.Message{Role: "system", Content: note}), nil
}
