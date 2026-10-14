package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
)

func TestProjectActionsInspectDetectedSubdirectory(t *testing.T) {
	f, preparation := preparationFixture(t)
	selected := &CommandRuntimeService{store: f.st, adapter: commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "directory-fixture", "generation")}
	runtime := preparationAdmissionRuntime{CommandRuntimeService: f.service, selected: selected}
	preparation.runtime = runtime
	if err := os.Mkdir(filepath.Join(f.root, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		if err := os.Rename(filepath.Join(f.root, name), filepath.Join(f.root, "web", name)); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := preparation.Inspect(t.Context(), f.run.ID, "web")
	if err != nil || !prepared.Available {
		t.Fatalf("subdirectory preparation unavailable: %+v %v", prepared, err)
	}
	actions := NewProjectActionsService(f.st, runtime, nil)
	root, err := actions.List(t.Context(), f.run.ID)
	if err != nil || root.Target != "." || root.Actions[0].Available {
		t.Fatalf("root detection: %+v %v", root, err)
	}
	view, err := actions.List(t.Context(), f.run.ID, "web")
	if err != nil || view.Target != "web" || !view.Actions[0].Available || view.Actions[0].Configured || view.Actions[0].CommandID != "test.npm" {
		t.Fatalf("subdirectory detection: %+v %v", view, err)
	}
	spec, err := PrepareProjectCommandForAdapter(f.run, "test", view.Target, selected.adapter, f.root)
	if err != nil || spec.WorkingDirectory != "web" {
		t.Fatalf("listed directory cannot be tested: %+v %v", spec, err)
	}
	if _, err := actions.List(t.Context(), f.run.ID, "../other"); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("unsafe inspection target accepted: %v", err)
	}
}

func TestProjectActionsDetectedTestReplaysAfterSourcesDisappear(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("fixture lease %v %v", found, err)
	}
	if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(f.owned.Path, "go.mod")
	if err := os.WriteFile(manifest, []byte("module example.org/detected\ngo 1.26.9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := f.drydockFixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{RunID: f.turn.Run.ID, ExpectedGeneration: f.owned.Generation, OperationKey: "detect-project-files", RequestedBy: "operator", Title: "Project manifest", ConfirmObservedChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	f.owned = checkpoint.Workspace
	if _, err := f.service.projectCommandService(t.Context(), f.turn.Run.ID); err != nil {
		t.Fatalf("detected fixture runtime unavailable: %v", err)
	}
	plan, err := projectprep.Detect(t.Context(), f.owned.Path, ".")
	if err != nil {
		t.Fatal(err)
	}
	service := NewProjectActionsService(f.st, f.mux, f.drydockFixture.service)
	view, err := service.List(t.Context(), f.turn.Run.ID)
	if err != nil || len(view.Actions) != 2 || view.Actions[0].Configured || !view.Actions[0].Available || view.Actions[0].CommandID != "test.go" {
		t.Fatalf("unconfigured project test missing: %+v %v", view, err)
	}
	request := ProjectActionRequest{RunID: f.turn.Run.ID, Slot: "test", OperationKey: "detected-test-once", RequestedBy: "web_operator", ConfirmExecution: true}
	started, err := service.Run(t.Context(), request)
	if err != nil || started.Job.ID == "" {
		t.Fatalf("detected start %+v %v", started, err)
	}
	completed := waitProjectAction(t, service, f.turn.Run.ID, started.Job.ID, time.Minute)
	if completed.Job.State != runner.CommandRuntimeJobCompleted || !strings.Contains(completed.Job.Stdout, "controlled sandbox output") {
		t.Fatalf("detected receipt %+v", completed)
	}
	job, err := f.st.GetCommandRuntimeJob(t.Context(), started.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var intent runner.CommandRuntimeSpec
	if err := json.Unmarshal([]byte(job.IntentJSON), &intent); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(intent.Purpose, "Detected project action test.go ") || len(strings.Fields(intent.Purpose)) != 6 || strings.Fields(intent.Purpose)[5] != plan.Fingerprint {
		t.Fatalf("source plan binding missing: %+v", intent)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	cold := NewProjectActionsService(f.st, nil, nil)
	replay, err := cold.Run(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Job.ID != started.Job.ID {
		t.Fatalf("detected cold replay %+v %v", replay, err)
	}
	request.Target = "different"
	if _, err := cold.Run(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed target reused consent: %v", err)
	}
	f.assertDispatchCounts(t, 1, 1)
}

func pinActionConfig(value projectconfig.Effective) domain.RunConfig {
	raw, _ := json.Marshal(value)
	return domain.RunConfig{ProjectConfig: raw, ProjectConfigFingerprint: value.Fingerprint()}
}

func TestProjectCommandsSelectGuestPathsWithoutHostToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	adapter := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeDockerSandboxBackend, "docker-standard-code-guest.v1", "guest-generation")
	for _, id := range []string{"test.go", "test.node", "test.npm", "format.go"} {
		project := projectconfig.Effective{Protocol: projectconfig.ProtocolVersion}
		slot, target := "test", "pkg"
		if id == "format.go" {
			slot, target = "format", "pkg/valid.go"
			project.FormatCommandID = id
		} else {
			project.TestCommandID = id
		}
		run := domain.Run{Config: pinActionConfig(project)}
		spec, err := PrepareProjectCommandForAdapter(run, slot, target, adapter)
		if err != nil || !strings.HasPrefix(spec.Executable, "/opt/toolchains/") {
			t.Fatalf("%s acquired a host dependency: %+v %v", id, spec, err)
		}
		if id == "test.npm" && spec.Arguments[0] != "/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js" {
			t.Fatalf("host npm launcher escaped: %+v", spec)
		}
		if id != "format.go" && spec.WorkingDirectory != "pkg" {
			t.Fatalf("test directory lost: %+v", spec)
		}
	}
}

func projectActionsFixture(t *testing.T, readonly bool) (*operatorCommandFixture, *ProjectActionsService) {
	return projectActionsFixtureWithTestID(t, readonly, "test.go")
}

func TestProjectActionsClosedManagerIsUnavailable(t *testing.T) {
	f, service := projectActionsFixture(t, false)
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.manager.Available() {
		t.Fatal("closed command manager advertised availability")
	}
	view, err := service.List(t.Context(), f.run.ID)
	if err != nil || view.Actions[0].Available || view.Actions[0].Reason == "" {
		t.Fatalf("closed runtime project action %+v %v", view, err)
	}
}

func projectActionsFixtureWithTestID(t *testing.T, readonly bool, testID string) (*operatorCommandFixture, *ProjectActionsService) {
	f := newOperatorCommandFixture(t, domain.RunExecutionPermissionAsk)
	config := filepath.Join(f.root, ".prayu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	body := "protocol: project_config.v1\ntest_command_id: " + testID + "\nformat_command_id: format.go\nexclude_paths: [private/**, '__*.go']\n"
	if readonly {
		body += "read_only: true\n"
	}
	if err := os.WriteFile(config, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(f.st)
	project := projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, ReadOnly: readonly, TestCommandID: testID, FormatCommandID: "format.go", ExcludePaths: []string{"private/**", "__*.go"}}
	_, run, err := runs.Create(t.Context(), CreateRunRequest{Goal: "Execute the configured project action", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: "operator-workspace", Budget: domain.Budget{MaxTurns: 4, MaxTokens: 10000, MaxToolCalls: 12}, ProjectConfig: &project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionProfileService(f.st).Change(t.Context(), ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "project-action-profile", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	f.run, err = runs.Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, NewProjectActionsService(f.st, f.service, nil)
}

type projectActionsUnavailableWorkspaceStore struct {
	ProjectActionsStore
}

func (s projectActionsUnavailableWorkspaceStore) GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error) {
	return session.WorkspaceInfo{}, apperror.New(apperror.CodeUnavailable, "workspace is unavailable")
}

func TestProjectActionsListWorkspaceFailureDisablesActions(t *testing.T) {
	f, _ := projectActionsFixture(t, false)
	service := NewProjectActionsService(projectActionsUnavailableWorkspaceStore{f.st}, f.service, nil)
	view, err := service.List(t.Context(), f.run.ID)
	if err != nil || len(view.Actions) != 2 {
		t.Fatalf("unavailable workspace did not retain the action view: %+v %v", view, err)
	}
	for _, action := range view.Actions {
		if !action.Configured || action.Available || action.Reason != "Run workspace is unavailable" {
			t.Fatalf("workspace failure did not disable configured action: %+v", action)
		}
	}
}

func TestProjectActionsBackgroundJobSurvivesRequestAndStreamsBeforeStop(t *testing.T) {
	f, service := projectActionsFixtureWithTestID(t, false, "test.node")
	if err := os.Mkdir(filepath.Join(f.root, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	script := `const {test}=require('node:test');test('slow',async()=>{process.stdout.write('live-before-exit\n');await new Promise(resolve=>setTimeout(resolve,30000));});`
	if err := os.WriteFile(filepath.Join(f.root, "tests", "slow.test.js"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	requestContext, disconnect := context.WithCancel(t.Context())
	started, err := service.Run(requestContext, ProjectActionRequest{RunID: f.run.ID, Slot: "test", Target: "tests", OperationKey: "background-node-test", RequestedBy: "web_operator", ConfirmExecution: true})
	disconnect()
	if err != nil || started.Job.ID == "" {
		t.Fatalf("background start %+v %v", started, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	for {
		view, err := service.GetJob(ctx, f.run.ID, started.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(view.Job.Stdout, "live-before-exit") {
			if view.Job.State != runner.CommandRuntimeJobRunning || !view.Job.CanStop {
				t.Fatalf("live output lost Job ownership: %+v", view)
			}
			break
		}
		if view.Job.State.Terminal() {
			t.Fatalf("request disconnect killed active Job: %+v", view)
		}
		select {
		case <-ctx.Done():
			t.Fatal("no live output before stop", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	job, err := f.st.GetCommandRuntimeJob(t.Context(), started.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	stopped, _, err := service.runtime.StopThreadApplicationServiceJob(t.Context(), job)
	if err != nil || (stopped.State != runner.CommandRuntimeJobStopping && !stopped.State.Terminal()) {
		t.Fatalf("owned stop %+v %v", stopped, err)
	}
	view := waitProjectAction(t, service, f.run.ID, started.Job.ID)
	if view.Job.State == runner.CommandRuntimeJobCompleted || !strings.Contains(view.Job.Stdout, "live-before-exit") || view.Job.CanStop {
		t.Fatalf("cancelled Job lost state/output: %+v", view)
	}
	job, err = f.st.GetCommandRuntimeJob(t.Context(), started.Job.ID)
	if err != nil || !job.TreeReaped {
		t.Fatalf("stop did not reap owned tree: %+v %v", job, err)
	}
}

func TestProjectActionsNonzeroTestKeepsSavedOutput(t *testing.T) {
	f, service := projectActionsFixtureWithTestID(t, false, "test.node")
	script := `const {test}=require('node:test');test('failure',()=>{process.stdout.write('failure-output\n');throw new Error('expected failure');});`
	if err := os.WriteFile(filepath.Join(f.root, "failure.test.js"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := service.Run(t.Context(), ProjectActionRequest{RunID: f.run.ID, Slot: "test", OperationKey: "node-failure-output", RequestedBy: "web_operator", ConfirmExecution: true})
	if err != nil || value.Job.ID == "" {
		t.Fatalf("failed test start %+v %v", value, err)
	}
	value = waitProjectAction(t, service, f.run.ID, value.Job.ID)
	if value.Job.State != runner.CommandRuntimeJobFailed || value.Job.ExitCode == nil || *value.Job.ExitCode == 0 || !strings.Contains(value.Job.Stdout, "failure-output") {
		t.Fatalf("failed test became success: %+v", value)
	}
}

func waitProjectAction(t *testing.T, service *ProjectActionsService, runID, jobID string, timeout ...time.Duration) ProjectActionResultView {
	t.Helper()
	wait := 12 * time.Second
	if len(timeout) == 1 {
		wait = timeout[0]
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	for {
		value, err := service.GetJob(ctx, runID, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if value.Job.State.Terminal() {
			return value
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestProjectActionsFormatRealFileReplayColdAndRejectChangedTarget(t *testing.T) {
	f, service := projectActionsFixture(t, false)
	view, err := service.List(t.Context(), f.run.ID)
	if err != nil || len(view.Actions) != 2 || !view.Actions[1].Available {
		t.Fatalf("exclude patterns incorrectly disabled formatting: %+v %v", view, err)
	}
	if err := os.MkdirAll(filepath.Join(f.root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(f.root, "pkg", "value.go")
	if err := os.WriteFile(filename, []byte("package pkg\nfunc Value()int{return 1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := ProjectActionRequest{RunID: f.run.ID, Slot: "format", Target: "pkg/value.go", OperationKey: "project-format-once", RequestedBy: "web_operator", ConfirmExecution: true}
	started, err := service.Run(t.Context(), request)
	if err != nil || started.Job.ID == "" {
		t.Fatalf("project action start %+v %v", started, err)
	}
	value := waitProjectAction(t, service, f.run.ID, started.Job.ID)
	if value.Job.State != runner.CommandRuntimeJobCompleted || value.Job.ExitCode == nil || *value.Job.ExitCode != 0 {
		t.Fatalf("formatter receipt: %+v", value)
	}
	content, err := os.ReadFile(filename)
	if err != nil || !strings.Contains(string(content), "func Value() int") {
		t.Fatalf("real formatting did not occur: %q %v", content, err)
	}
	// Cold reads do not require a running adapter, a current executable, or
	// the project's later on-disk configuration.
	cold := NewProjectActionsService(f.st, nil, nil)
	t.Setenv("PATH", t.TempDir())
	if err := os.WriteFile(filepath.Join(f.root, ".prayu", "config.yaml"), []byte("invalid current file"), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := cold.Run(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Job.ID != value.Job.ID {
		t.Fatalf("cold replay %+v %v", replay, err)
	}
	request.Target = "pkg/other.go"
	if _, err := cold.Run(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed target acquired old key: %v", err)
	}
	if _, err := cold.GetJob(t.Context(), "another-run", value.Job.ID); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("foreign Run read: %v", err)
	}
}

func TestProjectActionsExcludedAndHostPolicyRefuseBeforeJob(t *testing.T) {
	for _, kind := range []string{"excluded", "policy"} {
		t.Run(kind, func(t *testing.T) {
			f, service := projectActionsFixture(t, kind == "readonly")
			target := "allowed.go"
			if kind == "excluded" {
				target = "private/hidden.go"
			}
			if kind == "policy" {
				f.checker.deny = true
				if err := os.WriteFile(filepath.Join(f.root, target), []byte("package pkg\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Run(t.Context(), ProjectActionRequest{RunID: f.run.ID, Slot: "format", Target: target, OperationKey: "refuse-project-format", RequestedBy: "web_operator", ConfirmExecution: true}); err == nil {
				t.Fatalf("%s action was allowed", kind)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.run.ID, Limit: 10})
			if err != nil || len(jobs) != 0 {
				t.Fatalf("refusal created a Job: %+v %v", jobs, err)
			}
		})
	}
}

func TestProjectActionDockerGuestNormalizerPinsGuestAndRejectsHostArguments(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	project := projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.npm"}
	spec, err := PrepareProjectCommandForAdapter(domain.Run{Config: pinActionConfig(project)}, "test", "", f.service.adapter)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.service.normalizeCommandRuntimeSpec(spec, f.owned.Path)
	if err != nil || resolved.ExecutableIdentityKind != runner.CommandRuntimeExecutableTemplatePathSHA256 || !strings.HasPrefix(resolved.ExecutablePath, "/opt/toolchains/") {
		t.Fatalf("guest spec %+v %v", resolved, err)
	}
	command, err := commandRuntimeDockerCommand(resolved)
	if err != nil || command.Toolchain != "node" || command.Arguments[0] != spec.Arguments[0] {
		t.Fatalf("Docker npm mapping %+v %v", command, err)
	}
	spec.Arguments[0] = `C:\Users\operator\npm-cli.js`
	if _, err := f.service.normalizeCommandRuntimeSpec(spec, f.owned.Path); err == nil {
		t.Fatal("host launcher sent to Docker")
	}
	if err := f.service.projectPresetReady(t.Context(), "test.npm"); err == nil {
		t.Fatal("old image claimed new npm capability")
	}
	t.Run("historical POSIX selector", func(t *testing.T) {
		legacy := spec
		legacy.Executable = "/usr/local/go/bin/go"
		legacy.Arguments, legacy.Environment = []string{"version"}, []runner.CommandRuntimeEnvironment{}
		resolved, err := f.service.normalizeCommandRuntimeSpec(legacy, f.owned.Path)
		if err != nil || resolved.ExecutablePath != "/opt/toolchains/go/bin/go" || resolved.Spec.Executable != resolved.ExecutablePath || resolved.ExecutableIdentityKind != runner.CommandRuntimeExecutableTemplatePathSHA256 {
			t.Fatalf("native selector acquired a host launch binding: %+v %v", resolved, err)
		}
		legacy.Executable = "/opt/toolchains/foreign/bin/go"
		if _, err := f.service.normalizeCommandRuntimeSpec(legacy, f.owned.Path); err == nil {
			t.Fatal("incorrect guest toolchain path was accepted")
		}
	})
}
