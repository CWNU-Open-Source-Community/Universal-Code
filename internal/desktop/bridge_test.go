package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

const (
	testDesktopReadToken    = "desktop-read-token-0123456789abcdef"
	testDesktopControlToken = "desktop-control-token-0123456789abc"
	testDesktopUIDigest     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type testSkillPackagePicker struct {
	mu      sync.Mutex
	path    string
	err     error
	started chan struct{}
	release chan struct{}
	calls   int
}

type testUserTerminalController struct {
	startRequest  DesktopTerminalStartRequest
	readRequest   DesktopTerminalReadRequest
	writeRequest  DesktopTerminalWriteRequest
	resizeRequest DesktopTerminalResizeRequest
	closeRequest  DesktopTerminalCloseRequest
}

func (c *testUserTerminalController) Start(_ context.Context,
	request DesktopTerminalStartRequest,
) (DesktopTerminalSession, error) {
	c.startRequest = request
	return DesktopTerminalSession{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       "user-terminal-test", RunID: request.RunID,
		State: "running", Backend: "test-user-terminal",
		Columns: request.Columns, Rows: request.Rows,
		UserOwned: true, JobAssignedAtCreation: true,
		KillOnJobClose: true, Persistent: true, ProcessLocal: true,
	}, nil
}

func (c *testUserTerminalController) Get(_ context.Context,
	sessionID string,
) (DesktopTerminalSession, error) {
	return DesktopTerminalSession{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       sessionID, RunID: "run-terminal-test",
		State: "running", Backend: "test-user-terminal",
		Columns: 100, Rows: 30, UserOwned: true,
		JobAssignedAtCreation: true, KillOnJobClose: true,
		Persistent: true, ProcessLocal: true,
	}, nil
}

func (c *testUserTerminalController) Read(_ context.Context,
	request DesktopTerminalReadRequest,
) (DesktopTerminalOutput, error) {
	c.readRequest = request
	return DesktopTerminalOutput{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       request.SessionID, BaseCursor: request.Cursor,
		NextCursor: request.Cursor, State: "running",
	}, nil
}

func (c *testUserTerminalController) Write(_ context.Context,
	request DesktopTerminalWriteRequest,
) (DesktopTerminalWriteResult, error) {
	c.writeRequest = request
	return DesktopTerminalWriteResult{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       request.SessionID, BytesWritten: len([]byte(request.Data)),
	}, nil
}

func (c *testUserTerminalController) Resize(_ context.Context,
	request DesktopTerminalResizeRequest,
) error {
	c.resizeRequest = request
	return nil
}

func (c *testUserTerminalController) Close(_ context.Context,
	request DesktopTerminalCloseRequest,
) error {
	c.closeRequest = request
	return nil
}

func (p *testSkillPackagePicker) OpenSkillPackage(ctx context.Context) (string, error) {
	p.mu.Lock()
	p.calls++
	started := p.started
	release := p.release
	path := p.path
	err := p.err
	p.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return path, err
}

func TestDesktopBridgeBindsOnlyReviewedMethods(t *testing.T) {
	typ := reflect.TypeFor[*DesktopBridge]()
	want := []string{
		"Bootstrap", "CloseUserTerminal", "ConfirmVerificationSettings", "GetDebugTerminalAgentInput",
		"GetUserTerminal", "GetVerificationSettings", "GrantDebugTerminalAgentInput",
		"ImportWorkspace", "InspectClipboardFiles", "InstallSkillPackage",
		"ListSkillPackages", "OpenWorkspace", "PasteClipboardFiles", "PreviewSkillPackage",
		"ReadUserTerminal", "RemoveSkillPackage", "ResizeUserTerminal",
		"RestartWithRiskProfile", "RestartWithSandboxSettings", "RestartWithVerificationSettings",
		"RevokeDebugTerminalAgentInput",
		"SelectSkillPackage",
		"StartUserTerminal", "WorkspaceLaunchers", "WriteUserTerminal",
	}
	if typ.NumMethod() != len(want) {
		t.Fatalf("exported method count = %d, want %d", typ.NumMethod(), len(want))
	}
	for index, name := range want {
		if method := typ.Method(index); method.Name != name {
			t.Fatalf("method %d = %s, want %s", index, method.Name, name)
		}
	}
	for _, methodName := range []string{"SelectSkillPackage", "PreviewSkillPackage"} {
		method, ok := typ.MethodByName(methodName)
		if !ok {
			t.Fatalf("missing method %s", methodName)
		}
		for index := 1; index < method.Type.NumIn(); index++ {
			if method.Type.In(index).Kind() == reflect.String && methodName == "SelectSkillPackage" {
				t.Fatalf("native selection method accepts a renderer string: %s", method.Type)
			}
		}
	}
}

func TestDesktopBridgeBootstrapsMemoryOnlyClosedAuthority(t *testing.T) {
	bridge := newTestDesktopBridge(t, context.Background(), &testSkillPackagePicker{})
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.ProtocolVersion != ConnectionBootstrapProtocolVersion ||
		bootstrap.APIBaseURL != DesktopAPIBasePath || bootstrap.APIVersion != "api.v1" ||
		bootstrap.AppVersion != "test" || bootstrap.UIDigest != testDesktopUIDigest ||
		bootstrap.ReadToken != testDesktopReadToken ||
		bootstrap.ControlToken != testDesktopControlToken || !bootstrap.ControlEnabled ||
		!bootstrap.ThreadControlEnabled || !bootstrap.RunCreationEnabled ||
		bootstrap.StandardCodePresetEnabled || !bootstrap.SessionMessageEnabled ||
		!bootstrap.RunLifecycleEnabled || !bootstrap.RunExecutionEnabled ||
		bootstrap.PlanDeliveryControlEnabled || bootstrap.ApprovalControlEnabled ||
		bootstrap.ModelControlEnabled || bootstrap.ProviderCredentialEnabled ||
		bootstrap.ExecutionPermissionControlEnabled ||
		bootstrap.BrowserCDPPermissionControlEnabled || bootstrap.FullCDPDebugEnabled ||
		bootstrap.FullCDPSessionControlEnabled ||
		bootstrap.OperatorApprovalEnabled || bootstrap.DangerFullAccessEnabled ||
		bootstrap.WorkspaceSandboxEnabled || !bootstrap.CommandRuntimeProtocolAvailable ||
		bootstrap.CommandRuntimeAdapterInstalled || bootstrap.CommandRuntimeAdapterReady ||
		bootstrap.CommandRuntimeEnabled ||
		bootstrap.FileEditReviewEnabled || bootstrap.FileEditProposalEnabled ||
		bootstrap.RunWakeControlEnabled || bootstrap.FileEditApplyEnabled ||
		bootstrap.RunWakeExecutionEnabled || bootstrap.RunWakeWorkerEnabled ||
		bootstrap.ScheduledJobControlEnabled || bootstrap.ScheduledJobWorkerEnabled ||
		bootstrap.ReadOnlyDefault || bootstrap.ProcessExecutionEnabled ||
		bootstrap.ShellExecutionEnabled || bootstrap.DockerExecutionEnabled ||
		!bootstrap.AgentCodeToolsEnabled || bootstrap.CodeIntelEnabled ||
		bootstrap.SkillInstallationEnabled || bootstrap.EvidenceAttachmentEnabled ||
		bootstrap.VerificationEvidenceEnabled || bootstrap.EmbeddedAnalyzerExecutionEnabled ||
		bootstrap.UserTerminalEnabled ||
		bootstrap.AgentTerminalInputDefault ||
		!bootstrap.WorkspaceCheckpointControlEnabled ||
		bootstrap.BatchDeliveryControlEnabled ||
		bootstrap.BatchDeliveryHostValidationEnabled ||
		bootstrap.GitAdvancedControlEnabled ||
		bootstrap.GitHubReviewControlEnabled ||
		bootstrap.UIEvidenceControlEnabled ||
		bootstrap.WorkspaceOpenEnabled ||
		bootstrap.WorkspaceImportEnabled ||
		bootstrap.RendererPathInputSupported ||
		bootstrap.RiskProfileRestartEnabled || bootstrap.ChatGPTAuthEnabled {
		t.Fatalf("unexpected bootstrap: %#v", bootstrap)
	}
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, string(raw), []string{
		"agent_code_tools_enabled", "api_base_url", "api_version", "app_version", "approval_control_enabled",
		"code_intel_enabled",
		"control_enabled", "control_token",
		"execution_permission_control_enabled", "workspace_sandbox_enabled",
		"operator_approval_enabled",
		"browser_cdp_permission_control_enabled", "full_cdp_debug_enabled",
		"full_cdp_session_control_enabled",
		"danger_full_access_enabled",
		"command_runtime_enabled", "command_runtime_protocol_available",
		"command_runtime_adapter_installed", "command_runtime_adapter_ready",
		"thread_control_enabled",
		"docker_execution_enabled", "file_edit_review_enabled", "file_edit_proposal_enabled",
		"model_control_enabled", "provider_credential_enabled", "chatgpt_auth_enabled",
		"file_edit_apply_enabled",
		"process_execution_enabled", "protocol_version", "read_only_default",
		"plan_delivery_control_enabled", "read_token", "renderer_path_input_supported",
		"risk_profile_restart_enabled",
		"run_creation_enabled", "standard_code_preset_enabled", "shell_execution_enabled",
		"run_execution_enabled", "run_lifecycle_enabled", "run_wake_control_enabled",
		"run_wake_execution_enabled", "run_wake_worker_enabled",
		"scheduled_job_control_enabled", "scheduled_job_worker_enabled",
		"session_message_enabled", "session_steering_control_enabled",
		"skill_installation_enabled", "evidence_attachment_enabled",
		"verification_evidence_enabled", "embedded_analyzer_execution_enabled",
		"user_terminal_enabled",
		"agent_terminal_input_default", "ui_digest",
		"workspace_checkpoint_control_enabled",
		"git_advanced_control_enabled",
		"github_review_control_enabled",
		"batch_delivery_control_enabled", "batch_delivery_host_validation_enabled",
		"ui_evidence_control_enabled",
		"workspace_import_enabled",
		"workspace_open_enabled",
	})
}

func TestDesktopBridgeProjectsRunOwnedCommandRuntimeSeparatelyFromUserTerminal(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, RunExecutionEnabled: true,
		ExecutionPermissionControlEnabled: true, OperatorApprovalEnabled: true,
		DangerFullAccessEnabled:        true,
		CommandRuntimeAdapterInstalled: true, CommandRuntimeAdapterReady: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.CommandRuntimeProtocolAvailable ||
		!bootstrap.CommandRuntimeAdapterInstalled || !bootstrap.CommandRuntimeAdapterReady ||
		!bootstrap.CommandRuntimeEnabled || !bootstrap.ProcessExecutionEnabled ||
		!bootstrap.ShellExecutionEnabled || bootstrap.UserTerminalEnabled ||
		bootstrap.AgentTerminalInputDefault || bootstrap.DockerExecutionEnabled {
		t.Fatalf("command runtime ownership projection is wrong: %#v", bootstrap)
	}
}

func TestDesktopBridgeWorkspaceSandboxReadinessDoesNotEnableHostExecution(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	base := DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, WorkspaceSandboxEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	}
	if _, err := NewDesktopBridge(base); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("Workspace Sandbox readiness escaped permission control: %v", err)
	}
	base.ExecutionPermissionControlEnabled = true
	bridge, err := NewDesktopBridge(base)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.WorkspaceSandboxEnabled || bootstrap.CommandRuntimeEnabled ||
		bootstrap.ProcessExecutionEnabled || bootstrap.ShellExecutionEnabled ||
		bootstrap.DangerFullAccessEnabled {
		t.Fatalf("Workspace Sandbox readiness widened host execution: %#v", bootstrap)
	}
}

func TestDesktopBridgeProjectsBatchValidationOnlyWithExplicitFullAccess(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	base := DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken:                      testDesktopControlToken,
		ExecutionPermissionControlEnabled: true, OperatorApprovalEnabled: true,
		BatchDeliveryControlEnabled:        true,
		BatchDeliveryHostValidationEnabled: true,
		APIVersion:                         "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	}
	if _, err := NewDesktopBridge(base); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("batch validation without danger-full-access error=%v", err)
	}
	base.DangerFullAccessEnabled = true
	bridge, err := NewDesktopBridge(base)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.BatchDeliveryControlEnabled ||
		!bootstrap.BatchDeliveryHostValidationEnabled {
		t.Fatalf("batch validation bootstrap projection is wrong: %#v", bootstrap)
	}
}

func TestDesktopBridgeProjectsOnlyTheProcessLocalDockerCapability(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider:                   func() context.Context { return context.Background() },
		FilePicker:                        &testSkillPackagePicker{},
		ReadToken:                         testDesktopReadToken,
		ControlToken:                      testDesktopControlToken,
		ExecutionPermissionControlEnabled: true,
		OperatorApprovalEnabled:           true,
		DockerExecutionEnabled:            true,
		APIVersion:                        "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.DockerExecutionEnabled || bootstrap.ReadOnlyDefault ||
		bootstrap.ProcessExecutionEnabled || bootstrap.ShellExecutionEnabled ||
		bootstrap.DangerFullAccessEnabled || bootstrap.UserTerminalEnabled || bootstrap.AgentTerminalInputDefault {
		t.Fatalf("Docker bootstrap widened unrelated process authority: %#v", bootstrap)
	}
}

func TestDesktopBridgeUserTerminalIsExplicitAndUserOnly(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	controller := &testUserTerminalController{}
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, UserTerminalEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
		UserTerminalController: controller,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.UserTerminalEnabled || !bootstrap.ProcessExecutionEnabled ||
		!bootstrap.ShellExecutionEnabled || bootstrap.AgentTerminalInputDefault ||
		bootstrap.ReadOnlyDefault || bootstrap.DockerExecutionEnabled {
		t.Fatalf("user terminal bootstrap widened authority: %#v", bootstrap)
	}
	startRequest := DesktopTerminalStartRequest{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		RunID:           "run-terminal-test", Columns: 100, Rows: 30,
		ConfirmDebugBoundary: true,
	}
	session, err := bridge.StartUserTerminal(startRequest)
	if err != nil || session.SessionID != "user-terminal-test" ||
		controller.startRequest != startRequest {
		t.Fatalf("start session=%#v request=%#v err=%v",
			session, controller.startRequest, err)
	}
	readRequest := DesktopTerminalReadRequest{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       session.SessionID, MaxBytes: 1024,
	}
	if _, err := bridge.ReadUserTerminal(readRequest); err != nil ||
		controller.readRequest != readRequest {
		t.Fatalf("read request=%#v err=%v", controller.readRequest, err)
	}
	writeRequest := DesktopTerminalWriteRequest{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       session.SessionID, Data: "dir\r", UserConfirmed: true,
	}
	if _, err := bridge.WriteUserTerminal(writeRequest); err != nil ||
		controller.writeRequest != writeRequest {
		t.Fatalf("write request=%#v err=%v", controller.writeRequest, err)
	}
	resizeRequest := DesktopTerminalResizeRequest{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       session.SessionID, Columns: 110, Rows: 35,
		UserConfirmed: true,
	}
	if err := bridge.ResizeUserTerminal(resizeRequest); err != nil ||
		controller.resizeRequest != resizeRequest {
		t.Fatalf("resize request=%#v err=%v", controller.resizeRequest, err)
	}
	closeRequest := DesktopTerminalCloseRequest{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       session.SessionID, UserConfirmed: true,
	}
	if err := bridge.CloseUserTerminal(closeRequest); err != nil ||
		controller.closeRequest != closeRequest {
		t.Fatalf("close request=%#v err=%v", controller.closeRequest, err)
	}

	disabled := newTestDesktopBridge(t, context.Background(),
		&testSkillPackagePicker{})
	if _, err := disabled.StartUserTerminal(startRequest); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("disabled terminal error=%v", err)
	}
}

func TestDesktopBridgeSeparatesEvidenceAttachmentFromOtherControls(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, EvidenceAttachmentEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.EvidenceAttachmentEnabled || bootstrap.ControlEnabled ||
		bootstrap.RunCreationEnabled || bootstrap.SessionMessageEnabled ||
		bootstrap.SkillInstallationEnabled || bootstrap.ReadOnlyDefault ||
		bootstrap.ControlToken == "" {
		t.Fatalf("evidence attachment widened another capability: %#v", bootstrap)
	}
}

func TestDesktopBridgeSeparatesVerificationEvidenceFromOtherControls(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, VerificationEvidenceEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.VerificationEvidenceEnabled || bootstrap.ControlEnabled ||
		bootstrap.EvidenceAttachmentEnabled || bootstrap.RunCreationEnabled ||
		bootstrap.SkillInstallationEnabled || bootstrap.ReadOnlyDefault ||
		bootstrap.ControlToken == "" {
		t.Fatalf("verification evidence widened another capability: %#v", bootstrap)
	}
}

func TestDesktopBridgeSeparatesModelDiffAndWakeControls(t *testing.T) {
	for _, current := range []struct {
		name  string
		model bool
		diff  bool
		wake  bool
	}{
		{name: "model", model: true},
		{name: "diff", diff: true},
		{name: "wake", wake: true},
	} {
		t.Run(current.name, func(t *testing.T) {
			selector, preview := NewSkillPackagePreviewBoundary()
			bridge, err := NewDesktopBridge(DesktopBridgeConfig{
				ContextProvider: func() context.Context { return context.Background() },
				FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
				ControlToken: testDesktopControlToken, ModelControlEnabled: current.model,
				FileEditReviewEnabled: current.diff, RunWakeControlEnabled: current.wake,
				APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
				Selector: selector, PreviewBridge: preview,
			})
			if err != nil {
				t.Fatal(err)
			}
			bootstrap, err := bridge.Bootstrap()
			if err != nil {
				t.Fatal(err)
			}
			if bootstrap.ModelControlEnabled != current.model ||
				bootstrap.FileEditReviewEnabled != current.diff ||
				bootstrap.RunWakeControlEnabled != current.wake ||
				bootstrap.ControlEnabled || bootstrap.RunCreationEnabled ||
				bootstrap.RunExecutionEnabled || bootstrap.ProcessExecutionEnabled ||
				bootstrap.ShellExecutionEnabled || bootstrap.DockerExecutionEnabled ||
				bootstrap.ReadOnlyDefault {
				t.Fatalf("new control capability widened authority: %#v", bootstrap)
			}
		})
	}
}

func TestDesktopBridgeSeparatesPlanAndApprovalControls(t *testing.T) {
	for _, current := range []struct {
		name     string
		plan     bool
		approval bool
	}{
		{name: "plan", plan: true},
		{name: "approval", approval: true},
	} {
		t.Run(current.name, func(t *testing.T) {
			selector, preview := NewSkillPackagePreviewBoundary()
			bridge, err := NewDesktopBridge(DesktopBridgeConfig{
				ContextProvider: func() context.Context { return context.Background() },
				FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
				ControlToken:               testDesktopControlToken,
				PlanDeliveryControlEnabled: current.plan,
				ApprovalControlEnabled:     current.approval,
				APIVersion:                 "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
				Selector: selector, PreviewBridge: preview,
			})
			if err != nil {
				t.Fatal(err)
			}
			bootstrap, err := bridge.Bootstrap()
			if err != nil {
				t.Fatal(err)
			}
			if bootstrap.PlanDeliveryControlEnabled != current.plan ||
				bootstrap.ApprovalControlEnabled != current.approval ||
				bootstrap.ControlEnabled || bootstrap.RunCreationEnabled ||
				bootstrap.SessionMessageEnabled || bootstrap.RunLifecycleEnabled ||
				bootstrap.RunExecutionEnabled || bootstrap.ReadOnlyDefault {
				t.Fatalf("Plan or approval capability widened authority: %#v", bootstrap)
			}
		})
	}
}

func TestDesktopBridgeSeparatesRunLifecycleAndExecutionCapabilities(t *testing.T) {
	for _, current := range []struct {
		name      string
		lifecycle bool
		execution bool
	}{
		{name: "lifecycle", lifecycle: true},
		{name: "execution", execution: true},
	} {
		t.Run(current.name, func(t *testing.T) {
			selector, preview := NewSkillPackagePreviewBoundary()
			bridge, err := NewDesktopBridge(DesktopBridgeConfig{
				ContextProvider: func() context.Context { return context.Background() },
				FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
				ControlToken:        testDesktopControlToken,
				RunLifecycleEnabled: current.lifecycle, RunExecutionEnabled: current.execution,
				APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
				Selector: selector, PreviewBridge: preview,
			})
			if err != nil {
				t.Fatal(err)
			}
			bootstrap, err := bridge.Bootstrap()
			if err != nil {
				t.Fatal(err)
			}
			if bootstrap.RunLifecycleEnabled != current.lifecycle ||
				bootstrap.RunExecutionEnabled != current.execution ||
				bootstrap.ControlEnabled || bootstrap.RunCreationEnabled ||
				bootstrap.SessionMessageEnabled || bootstrap.SessionSteeringControlEnabled ||
				bootstrap.ReadOnlyDefault {
				t.Fatalf("Run operation capability widened authority: %#v", bootstrap)
			}
		})
	}
}

func TestDesktopBridgeSeparatesSessionSteeringControlFromOtherControls(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, SessionSteeringControlEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.ControlEnabled || bootstrap.RunCreationEnabled ||
		bootstrap.SessionMessageEnabled || !bootstrap.SessionSteeringControlEnabled ||
		bootstrap.ReadOnlyDefault || bootstrap.ControlToken == "" {
		t.Fatalf("Session steering cancellation widened another capability: %#v", bootstrap)
	}
}

func TestDesktopBridgeSeparatesSessionMessagesFromOtherControls(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, SessionMessageEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.ControlEnabled || bootstrap.RunCreationEnabled ||
		!bootstrap.SessionMessageEnabled || bootstrap.ReadOnlyDefault ||
		bootstrap.ControlToken == "" {
		t.Fatalf("Session message submission widened another capability: %#v", bootstrap)
	}
}

func TestDesktopBridgeSeparatesRunCreationFromExistingRunControls(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, RunCreationEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.ControlEnabled || !bootstrap.RunCreationEnabled || bootstrap.ReadOnlyDefault ||
		bootstrap.ControlToken == "" {
		t.Fatalf("Run creation widened another capability: %#v", bootstrap)
	}
}

func TestDesktopBridgeSelectsAndConsumesNativePackageWithoutPathDisclosure(t *testing.T) {
	packagePath := filepath.Join(t.TempDir(), "PRIVATE_DESKTOP_PACKAGE.zip")
	writeDesktopSkillPackage(t, packagePath, "PRIVATE_DESCRIPTION", []byte("# Desktop\n"))
	picker := &testSkillPackagePicker{path: packagePath}
	bridge := newTestDesktopBridge(t, context.Background(), picker)

	result, err := bridge.SelectSkillPackage()
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != SkillPackageDialogProtocolVersion ||
		result.Status != SkillPackageDialogSelected || result.Selection == nil {
		t.Fatalf("unexpected dialog result: %#v", result)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, packagePath) || strings.Contains(serialized, filepath.Base(packagePath)) ||
		strings.Contains(serialized, "PRIVATE_DESCRIPTION") {
		t.Fatalf("dialog result disclosed native input: %s", serialized)
	}
	assertExactJSONKeys(t, serialized, []string{"protocol_version", "selection", "status"})

	preview, err := bridge.PreviewSkillPackage(result.Selection.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "desktop-review" || !preview.Validated || preview.InstallationAuthorized {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if _, err := bridge.PreviewSkillPackage(result.Selection.Handle); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("replay error = %v, code = %s", err, apperror.CodeOf(err))
	}
}

func TestDesktopBridgeCancellationAndNativeErrorsRemainPathFree(t *testing.T) {
	cancelled := newTestDesktopBridge(t, context.Background(), &testSkillPackagePicker{})
	result, err := cancelled.SelectSkillPackage()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SkillPackageDialogCancelled || result.Selection != nil {
		t.Fatalf("unexpected cancelled result: %#v", result)
	}

	privateError := errors.New(`open C:\PRIVATE\package.zip: access denied`)
	failing := newTestDesktopBridge(t, context.Background(), &testSkillPackagePicker{err: privateError})
	if _, err := failing.SelectSkillPackage(); apperror.CodeOf(err) != apperror.CodeUnavailable ||
		strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "package.zip") {
		t.Fatalf("native error was not bounded: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := newTestDesktopBridge(t, ctx, &testSkillPackagePicker{})
	if _, err := stopped.SelectSkillPackage(); apperror.CodeOf(err) != apperror.CodeCancelled {
		t.Fatalf("stopped selection error = %v, code = %s", err, apperror.CodeOf(err))
	}
}

func TestDesktopBridgeRejectsConcurrentNativeDialogs(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	picker := &testSkillPackagePicker{started: started, release: release}
	bridge := newTestDesktopBridge(t, context.Background(), picker)
	firstDone := make(chan error, 1)
	go func() {
		_, err := bridge.SelectSkillPackage()
		firstDone <- err
	}()
	<-started
	if _, err := bridge.SelectSkillPackage(); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("concurrent dialog error = %v, code = %s", err, apperror.CodeOf(err))
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	picker.mu.Lock()
	defer picker.mu.Unlock()
	if picker.calls != 1 {
		t.Fatalf("native dialog calls = %d, want 1", picker.calls)
	}
}

func TestNewDesktopBridgeRejectsInvalidMetadataAndDependencies(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	valid := DesktopBridgeConfig{
		ContextProvider: func() context.Context { return context.Background() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	}
	tests := []struct {
		name   string
		change func(*DesktopBridgeConfig)
	}{
		{name: "missing context", change: func(c *DesktopBridgeConfig) { c.ContextProvider = nil }},
		{name: "missing picker", change: func(c *DesktopBridgeConfig) { c.FilePicker = nil }},
		{name: "short token", change: func(c *DesktopBridgeConfig) { c.ReadToken = "short" }},
		{name: "same control token", change: func(c *DesktopBridgeConfig) { c.ControlToken = c.ReadToken }},
		{name: "capability without token", change: func(c *DesktopBridgeConfig) { c.RunCreationEnabled = true }},
		{name: "Docker capability without token", change: func(c *DesktopBridgeConfig) {
			c.ExecutionPermissionControlEnabled = true
			c.OperatorApprovalEnabled = true
			c.DockerExecutionEnabled = true
		}},
		{name: "Docker capability without permission control", change: func(c *DesktopBridgeConfig) {
			c.ControlToken = testDesktopControlToken
			c.OperatorApprovalEnabled = true
			c.DockerExecutionEnabled = true
		}},
		{name: "approval without token", change: func(c *DesktopBridgeConfig) { c.ApprovalControlEnabled = true }},

		{name: "Full CDP session control without production permission stack", change: func(c *DesktopBridgeConfig) {
			c.ControlToken = testDesktopControlToken
			c.FullCDPSessionControlEnabled = true
		}},
		{name: "evidence without token", change: func(c *DesktopBridgeConfig) { c.EvidenceAttachmentEnabled = true }},
		{name: "verification without token", change: func(c *DesktopBridgeConfig) { c.VerificationEvidenceEnabled = true }},
		{name: "terminal without token", change: func(c *DesktopBridgeConfig) {
			c.UserTerminalEnabled = true
			c.UserTerminalController = &testUserTerminalController{}
		}},
		{name: "terminal without controller", change: func(c *DesktopBridgeConfig) {
			c.ControlToken = testDesktopControlToken
			c.UserTerminalEnabled = true
		}},
		{name: "bad digest", change: func(c *DesktopBridgeConfig) { c.UIDigest = strings.Repeat("g", 64) }},
		{name: "missing version", change: func(c *DesktopBridgeConfig) { c.APIVersion = "" }},
		{name: "workspace resolver without launcher", change: func(c *DesktopBridgeConfig) {
			c.WorkspaceResolver = testWorkspaceResolver{}
		}},
		{name: "workspace launcher without resolver", change: func(c *DesktopBridgeConfig) {
			c.WorkspaceLauncher = &testWorkspaceLauncher{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			if _, err := NewDesktopBridge(config); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
				t.Fatalf("error = %v, code = %s", err, apperror.CodeOf(err))
			}
		})
	}
}

func TestDesktopBridgeProjectsInstalledFullCDPSessionControl(t *testing.T) {
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider:                    func() context.Context { return context.Background() },
		FilePicker:                         &testSkillPackagePicker{},
		ReadToken:                          testDesktopReadToken,
		ControlToken:                       testDesktopControlToken,
		ExecutionPermissionControlEnabled:  true,
		BrowserCDPPermissionControlEnabled: true,
		FullCDPDebugEnabled:                true,
		FullCDPSessionControlEnabled:       true,
		OperatorApprovalEnabled:            true,
		DangerFullAccessEnabled:            true,
		APIVersion:                         "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := bridge.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.FullCDPSessionControlEnabled || !bootstrap.FullCDPDebugEnabled ||
		!bootstrap.BrowserCDPPermissionControlEnabled || !bootstrap.DangerFullAccessEnabled {
		t.Fatalf("Full CDP production availability projection is wrong: %#v", bootstrap)
	}
}

func newTestDesktopBridge(t *testing.T, ctx context.Context, picker SkillPackageFilePicker) *DesktopBridge {
	t.Helper()
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return ctx }, FilePicker: picker,
		ReadToken: testDesktopReadToken, ControlToken: testDesktopControlToken,
		RunControlEnabled: true, RunCreationEnabled: true, SessionMessageEnabled: true,
		RunLifecycleEnabled: true, RunExecutionEnabled: true,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
		Selector: selector, PreviewBridge: preview,
	})
	if err != nil {
		t.Fatal(err)
	}
	return bridge
}
