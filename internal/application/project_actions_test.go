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
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
)

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

func waitProjectAction(t *testing.T, service *ProjectActionsService, runID, jobID string) ProjectActionResultView {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
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
