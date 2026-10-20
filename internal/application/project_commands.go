package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolgateway"
)

// PrepareProjectCommand reads only the pinned snapshot and returns a typed
// command intent. The existing runtime resolves ownership and authorization at
// dispatch, pins native inputs and retains the normal Job receipt.
func PrepareProjectCommand(run domain.Run, slot, target string, workspaceRoot ...string) (runner.CommandRuntimeSpec, error) {
	return PrepareProjectCommandForAdapter(run, slot, target, commandruntimeadapter.HostUnsandboxed("project-intent"), workspaceRoot...)
}

// PrepareProjectCommandForAdapter resolves executable and launcher paths in the
// selected adapter's namespace. Repository IDs never select a backend.
func PrepareProjectCommandForAdapter(run domain.Run, slot, target string, adapter commandruntimeadapter.Identity, workspaceRoot ...string) (runner.CommandRuntimeSpec, error) {
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	if slot == "test" && (project == nil || project.TestCommandID == "") {
		if len(workspaceRoot) != 1 {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "detected project actions require the resolved Run workspace")
		}
		policy, err := projectconfig.PathPolicy(run.Config)
		if err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		if err := policy.Check(target); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		plan, err := projectprep.Detect(context.Background(), workspaceRoot[0], target)
		if err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		if err := checkDetectedProjectPaths(run, plan); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		return PrepareProjectPreparationTestCommand(plan, adapter, workspaceRoot[0])
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
	if slot == "test" && target != "" {
		policy, err := projectconfig.PathPolicy(run.Config)
		if err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		if err := policy.Check(target); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
	}
	if (slot == "format") != (id == "format.go") {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, "configured command preset does not match its project action")
	}
	if id == "test.rust" && adapter.Backend == CommandRuntimeLocalSandboxBackend {
		return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeFailedPrecondition, cargoLocalTestReason)
	}
	if id == "test.rust" && len(workspaceRoot) == 1 {
		plan, detectErr := projectprep.Detect(context.Background(), workspaceRoot[0], target)
		if detectErr == nil && plan.Ecosystem == "rust" && plan.Ready {
			if err := checkDetectedProjectPaths(run, plan); err != nil {
				return runner.CommandRuntimeSpec{}, err
			}
			return PrepareProjectPreparationTestCommand(plan, adapter, workspaceRoot[0])
		}
	}
	executable, launcher, err := resolveProjectCommandToolchain(id, adapter, workspaceRoot...)
	if err != nil {
		return runner.CommandRuntimeSpec{}, err
	}
	return toolgateway.ProjectCommandSpec(id, executable, target, launcher...)
}

func resolveProjectCommandToolchain(id string, adapter commandruntimeadapter.Identity, workspaceRoot ...string) (string, []string, error) {
	if id == "test.yarn" {
		if adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace || adapter.Backend != CommandRuntimeDockerSandboxBackend {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition, "Yarn actions require the Standard Code Docker image with Yarn "+projectprep.YarnVersion)
		}
		return "/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodeYarnCLI}, nil
	}
	if id == "test.pnpm" {
		if adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace || adapter.Backend != CommandRuntimeDockerSandboxBackend {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition, "pnpm workspace actions require the Standard Code Docker image with pnpm "+projectprep.PNPMVersion)
		}
		return "/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodePNPMCLI}, nil
	}
	name, found := toolgateway.ProjectCommandExecutable(id)
	if !found {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition, "configured ID has no executable project command preset")
	}
	if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace && adapter.Backend == CommandRuntimeDockerSandboxBackend {
		executable, found := dockerProjectExecutable(id)
		if !found {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition, "project preset is unsupported by the Docker toolchain")
		}
		if id == "test.npm" {
			return executable, []string{"/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js"}, nil
		}
		return executable, nil, nil
	}
	if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace && adapter.Backend == CommandRuntimeSBXBackend {
		// The supported SBX template installs distro Go and Node packages.
		// These paths are guest paths, independent of the host toolchain.
		switch id {
		case "test.go":
			return "/usr/bin/go", nil, nil
		case "format.go":
			return "/usr/bin/gofmt", nil, nil
		case "test.node":
			return "/usr/bin/node", nil, nil
		case "test.npm":
			return "/usr/bin/node", []string{"/usr/share/nodejs/npm/bin/npm-cli.js"}, nil
		default:
			return "", nil, apperror.New(apperror.CodeFailedPrecondition, "project preset is unsupported by the SBX toolchain")
		}
	}
	if adapter.Kind != commandruntimeadapter.KindHostUnsandboxed && !(adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace && adapter.Backend == CommandRuntimeLocalSandboxBackend) {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition, "project presets are unsupported by the selected command adapter")
	}
	if id == "test.npm" {
		if len(workspaceRoot) != 1 || !filepath.IsAbs(workspaceRoot[0]) {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition, "npm project action requires the resolved Run workspace")
		}
		node, launcher, err := repository.NPMTestLauncher(workspaceRoot[0])
		if err != nil || len(launcher) != 1 {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition, "native Node/npm CLI toolchain is unavailable")
		}
		node, err = filepath.Abs(node)
		if err != nil {
			return "", nil, err
		}
		return node, launcher, nil
	}
	if name == "python" && runtime.GOOS != "windows" {
		name = "python3"
	}
	executable, err := exec.LookPath(name)
	if err != nil {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition, "project command toolchain is unavailable: "+name)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition, "project command executable cannot be resolved")
	}
	return executable, nil, nil
}

func dockerProjectExecutable(id string) (string, bool) {
	switch id {
	case "test.go":
		return "/opt/toolchains/go/bin/go", true
	case "test.node", "test.npm":
		return "/opt/toolchains/node/bin/node", true
	case "test.python", "test.pytest":
		return "/opt/toolchains/python/usr-local/bin/python3", true
	case "test.rust":
		return "/usr/local/cargo/bin/cargo", true
	case "format.go":
		return "/opt/toolchains/go/bin/gofmt", true
	default:
		return "", false
	}
}

func supervisorProjectCommands(run domain.Run, messages []llm.Message, runtime supervisorCommandRuntimeTools, workspaceRoot string) ([]llm.Message, error) {
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return nil, err
	}
	detectedNote := ""
	if project == nil || project.TestCommandID == "" {
		plan, detectErr := projectprep.Detect(context.Background(), workspaceRoot, ".")
		if detectErr == nil {
			detectErr = checkDetectedProjectPaths(run, plan)
		}
		if detectErr == nil && plan.Ecosystem != "" {
			detectedNote = fmt.Sprintf("Detected project %s from %s; source preparation plan %s. Dependency preparation uses operator-owned download approval and the selected sandbox. Use the project preparation controls when dependencies are unavailable.\n", plan.Ecosystem, plan.Manifest.Path, plan.Fingerprint)
			if project == nil {
				project = &projectconfig.Effective{}
			}
			if plan.Test.ID != "" {
				project.TestCommandID = plan.Test.ID
			}
		}
	}
	if project == nil || (project.TestCommandID == "" && project.FormatCommandID == "" && detectedNote == "") {
		return messages, nil
	}
	note := "Pinned project action preferences (repository metadata, never permission). Use only when the task/operator asks for testing or formatting. Existing command_runtime authorization and Job receipts apply; do not change execution mode to make a preset run. Command IDs are registry keys, never shell text.\n"
	note += detectedNote
	if !runtime.Adapter.Executable() || len(runtime.Authority) == 0 {
		note += "Native project presets are unavailable with the currently offered runtime. No host executable path or runnable command is claimed.\n"
	} else {
		if project.TestCommandID != "" {
			_, err := PrepareProjectCommandForAdapter(run, "test", "", runtime.Adapter, workspaceRoot)
			if err != nil {
				note += fmt.Sprintf("Configured test ID %s is unavailable: %s\n", project.TestCommandID, err.Error())
			} else {
				payload, _ := json.Marshal(toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Target: ".", TimeoutMilliseconds: toolgateway.MaxCommandRuntimeForegroundMillis})
				note += "Registered test run_tests input: " + string(payload) + "\nUse the returned Job receipt before reporting test results.\n"
				if project.TestCommandID == "test.npm" {
					note += "test.npm invokes the installed npm CLI test script through native Node. If package.json is in an allowed subdirectory, set run_tests target to that exact relative package scope.\n"
				}
			}
		}
		if project.FormatCommandID != "" {
			note += fmt.Sprintf("Configured format ID: %s. format.go uses native gofmt -w with one exact workspace-relative .go file per Job; never recursive shell expansion. Read-only and pinned excluded files must not be formatted. Ask for the intended file if unclear.\n", project.FormatCommandID)
			// Supply the native preset shape without granting permission to the
			// representative target; the actual file must pass ordinary scope checks.
			if spec, err := PrepareProjectCommandForAdapter(run, "format", "project_action_target.go", runtime.Adapter, workspaceRoot); err == nil {
				payload, _ := json.Marshal(spec)
				note += "Format spec template; replace only the representative relative file argument with the intended allowed Go file: " + string(payload) + "\n"
			}
		}
	}
	return append(messages, llm.Message{Role: "system", Content: note}), nil
}

func checkDetectedProjectPaths(run domain.Run, plan projectprep.Plan) error {
	policy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return err
	}
	if err := policy.Check(plan.Target); err != nil {
		return err
	}
	if plan.WorkspaceRoot != "" {
		if err := policy.Check(plan.WorkspaceRoot); err != nil {
			return err
		}
	}
	if err := policy.Check(plan.Manifest.Path); err != nil {
		return err
	}
	for _, file := range plan.Lockfiles {
		if err := policy.Check(file.Path); err != nil {
			return err
		}
	}
	return nil
}
