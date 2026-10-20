package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

const preparationPythonLock = "lock-version='1.0'\ncreated-by='fixture'\n[[packages]]\nname='six'\nversion='1.17.0'\n[[packages.wheels]]\nurl='https://files.pythonhosted.org/six-1.17.0-py2.py3-none-any.whl'\nhashes={sha256='4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274'}\n"

func TestProjectPreparationPythonCommandBoundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pylock.toml"), []byte(preparationPythonLock), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := projectprep.Detect(t.Context(), root, ".")
	if err != nil || !plan.Ready {
		t.Fatalf("plan %+v %v", plan, err)
	}
	docker := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeDockerSandboxBackend, "fixture", "generation")
	spec, err := PrepareProjectPreparationCommand(plan, docker, root, false)
	program := projectprep.PythonPreparationProgram(false)
	if err != nil || spec.Executable != "/opt/toolchains/python/usr-local/bin/python3" || len(spec.Arguments) != len(program)+5 || spec.Arguments[0] != "-I" || spec.Arguments[len(program)] != "/workspace/" || spec.Network != runner.CommandRuntimeNetworkDisabled || spec.Credentials != runner.CommandRuntimeCredentialsNone {
		t.Fatalf("Python install boundary %+v %v", spec, err)
	}
	if !strings.Contains(spec.Arguments[len(program)+4], ".traverse-preparation/") || strings.Contains(spec.Arguments[len(program)+4], root) {
		t.Fatal("Python installer escaped its task-private guest directory")
	}
	test, err := PrepareProjectPreparationTestCommand(plan, docker, root)
	if err != nil || test.Arguments[len(projectprep.PythonPreparationProgram(true))+4] != "unittest" {
		t.Fatalf("Python test does not use prepared environment %+v %v", test, err)
	}
	for _, command := range []runner.CommandRuntimeSpec{spec, test} {
		for _, argument := range command.Arguments {
			if len(argument) > sandbox.MaxCommandArgumentBytes || strings.ContainsAny(argument, "\r\n") {
				t.Fatal("fixed Python program exceeds the sandbox literal-argument boundary")
			}
		}
	}
	for _, adapter := range []commandruntimeadapter.Identity{commandruntimeadapter.HostUnsandboxed("fixture"), commandruntimeadapter.SandboxedWorkspace(CommandRuntimeLocalSandboxBackend, "fixture", "generation"), commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "fixture", "generation")} {
		if _, err := PrepareProjectPreparationCommand(plan, adapter, root, false); err == nil {
			t.Fatalf("Python installation silently fell back to %+v", adapter)
		}
	}
	for _, id := range []string{"test.python", "test.pytest"} {
		run := domain.Run{Config: pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: id})}
		prepared, err := PrepareProjectCommandForAdapter(run, "test", ".", docker, root)
		module := "unittest"
		if id == "test.pytest" {
			module = "pytest"
		}
		if err != nil || prepared.Arguments[len(projectprep.PythonPreparationProgram(true))+4] != module {
			t.Fatalf("configured %s does not use the private preparation: %+v %v", id, prepared, err)
		}
		plain, err := PrepareProjectCommandForAdapter(run, "test", ".", docker, t.TempDir())
		if err != nil || len(plain.Arguments) < 2 || plain.Arguments[0] != "-m" || plain.Arguments[1] != module {
			t.Fatalf("existing %s without pylock changed: %+v %v", id, plain, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pylock.toml"), []byte("invalid lock"), 0600); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{Config: pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.python"})}
	if _, err := PrepareProjectCommandForAdapter(run, "test", ".", docker, root); err == nil {
		t.Fatal("invalid Python lock silently ran the unprepared preset")
	}
}

func TestProjectActionsDetectedPythonJobReadAndColdReplay(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("lease %v %v", found, err)
	}
	if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(f.owned.Path, "pylock.toml")
	if err := os.WriteFile(manifest, []byte(preparationPythonLock), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := f.drydockFixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{RunID: f.turn.Run.ID, ExpectedGeneration: f.owned.Generation, OperationKey: "python-project-files", RequestedBy: "operator", Title: "Python manifest", ConfirmObservedChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	f.owned = checkpoint.Workspace
	service := NewProjectActionsService(f.st, f.mux, f.drydockFixture.service)
	plan, err := projectprep.Detect(t.Context(), f.owned.Path, ".")
	if err != nil {
		t.Fatal(err)
	}
	install, err := PrepareProjectPreparationCommand(plan, f.service.adapter, f.owned.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor).NormalizeCommandRuntimeSpec(install, f.owned.Path)
	if err != nil {
		t.Fatal("installer rejected by Docker guest normalization", err)
	}
	if _, err := commandRuntimeDockerCommand(resolved); err != nil {
		t.Fatal("installer rejected by Standard Code command contract", err)
	}
	view, err := service.List(t.Context(), f.turn.Run.ID)
	if err != nil || len(view.Actions) == 0 || !view.Actions[0].Available || view.Actions[0].Configured || view.Actions[0].CommandID != "test.python" {
		t.Fatalf("detected Python action %+v %v", view, err)
	}
	request := ProjectActionRequest{RunID: f.turn.Run.ID, Slot: "test", OperationKey: "python-detected-test", RequestedBy: "web_operator", ConfirmExecution: true}
	started, err := service.Run(t.Context(), request)
	if err != nil || started.Job.ID == "" {
		t.Fatalf("Python start %+v %v", started, err)
	}
	completed := waitProjectAction(t, service, f.turn.Run.ID, started.Job.ID, time.Minute)
	if completed.Job.State != runner.CommandRuntimeJobCompleted {
		t.Fatalf("Python Job cannot be read: %+v", completed)
	}
	job, err := f.st.GetCommandRuntimeJob(t.Context(), started.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var intent runner.CommandRuntimeSpec
	if err := json.Unmarshal([]byte(job.IntentJSON), &intent); err != nil || !strings.HasPrefix(intent.Purpose, "Detected project action test.python ") {
		t.Fatalf("Python purpose missing %+v %v", intent, err)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	cold := NewProjectActionsService(f.st, nil, nil)
	replay, err := cold.Run(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Job.ID != started.Job.ID {
		t.Fatalf("Python cold replay %+v %v", replay, err)
	}
	f.assertDispatchCounts(t, 1, 1)
}
