import { beforeEach, describe, expect, it, vi } from "vitest";

import type { DesktopSkillPreview } from "./desktop-bridge";

const bootstrap = {
  protocol_version: "desktop_connection_bootstrap.v1",
  agent_code_tools_enabled: true,
  code_intel_enabled: false,
  api_base_url: "/api/v1",
  api_version: "api.v1",
  app_version: "v0.1.0",
  ui_digest: "a".repeat(64),
  read_token: "read-token-0123456789abcdefghijklmnop",
  control_token: "",
  control_enabled: false,
  execution_permission_control_enabled: false,
  browser_cdp_permission_control_enabled: false,
  full_cdp_debug_enabled: false,
  full_cdp_session_control_enabled: false,
  operator_approval_enabled: false,
  danger_full_access_enabled: false,

  workspace_sandbox_enabled: false,
  command_runtime_enabled: false,
  command_runtime_protocol_available: true,
  command_runtime_adapter_installed: false,
  command_runtime_adapter_ready: false,
  thread_control_enabled: false,
  run_creation_enabled: false,
  standard_code_preset_enabled: false,
  session_message_enabled: false,
  session_steering_control_enabled: false,
  run_lifecycle_enabled: false,
  run_execution_enabled: false,
  plan_delivery_control_enabled: false,
  approval_control_enabled: false,


  model_control_enabled: false,
  provider_credential_enabled: false,
  file_edit_review_enabled: false,
  file_edit_proposal_enabled: false,
  run_wake_control_enabled: false,
  file_edit_apply_enabled: false,
  run_wake_execution_enabled: false,
  run_wake_worker_enabled: false,
  scheduled_job_control_enabled: false,
  scheduled_job_worker_enabled: false,
  read_only_default: true,
  process_execution_enabled: false,
  shell_execution_enabled: false,
  docker_execution_enabled: false,
  skill_installation_enabled: false,
  evidence_attachment_enabled: false,
  verification_evidence_enabled: false,
  ui_evidence_control_enabled: false,
  embedded_analyzer_execution_enabled: false,
  workspace_checkpoint_control_enabled: false,
  git_advanced_control_enabled: false,
  github_review_control_enabled: false,
  batch_delivery_control_enabled: false,
  batch_delivery_host_validation_enabled: false,
  user_terminal_enabled: false,
  agent_terminal_input_default: false,
  workspace_open_enabled: false,
  workspace_import_enabled: false,
  risk_profile_restart_enabled: false,
  renderer_path_input_supported: false,
};

const restartBootstrap = {
  ...bootstrap,
  control_token: "control-token-0123456789abcdefghijkl",
  execution_permission_control_enabled: true,
  operator_approval_enabled: true,
  read_only_default: false,
  risk_profile_restart_enabled: true,
};

const selection = {
  protocol_version: "desktop_file_selection.v1",
  handle: "A".repeat(43),
  expires_at: "2026-07-18T10:00:00Z",
};

const preview: DesktopSkillPreview = {
  protocol_version: "desktop_skill_package_preview.v1",
  package_protocol: "skill_package.v1",
  skill_protocol: "skill.v1",
  name: "review-helper",
  version: "1.0.0",
  profiles: ["review"],
  declared_tools: ["workspace_list"],
  declared_tool_count: 1,
  content_bytes: 128,
  content_token_upper_bound: 32,
  archive_sha256: "b".repeat(64),
  package_fingerprint: "c".repeat(64),
  archive_bytes: 512,
  uncompressed_bytes: 384,
  entry_count: 2,
  trust_class: "operator_installed_untrusted",
  risk_codes: ["untrusted_instructions"],
  executable_asset_count: 0,
  install_hook_count: 0,
  import_command_execution: false,
  import_network_access: false,
  import_provider_calls: false,
  tool_capability_grant: false,
  installation_authorized: false,
  validated: true,
  confirmation_handle: "D".repeat(43),
  confirmation_expires_at: "2026-07-18T10:05:00Z",
};

describe("desktop native bridge", () => {
  beforeEach(() => {
    vi.resetModules();
    delete window.go;
  });

  it("uses only the public message from native errors", async () => {
    const { desktopErrorMessage } = await import("./desktop-bridge");
    const message = "File actions support files up to 5 MiB";
    expect(desktopErrorMessage(new Error(message))).toBe(message);
    expect(desktopErrorMessage({ code: "RESOURCE_EXHAUSTED", message, cause: "private cause", stack: "private stack" })).toBe(message);
    for (const value of [{ code: "INTERNAL", cause: "private cause" }, { message: { detail: "private detail" } }, { message: "  ", stack: "private stack" }]) {
      expect(desktopErrorMessage(value)).toBe("Desktop operation failed");
    }
  });

  it("saves only the confirmed verification choices and restarts through a separate fixed request", async () => {
    const initial = verificationView();
    const saved = verificationView(true);
    const confirm = vi.fn().mockResolvedValue({ protocol_version: initial.protocol_version, status: "saved", replayed: false, view: saved });
    const restartResult = { protocol_version: initial.protocol_version, status: "restarting", restart_required: true,
      arbitrary_arguments_accepted: false, persistent_runtime_grant: false };
    const restart = vi.fn().mockResolvedValue(restartResult);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(restartBootstrap), GetVerificationSettings: vi.fn().mockResolvedValue(initial),
      ConfirmVerificationSettings: confirm, RestartWithVerificationSettings: restart });
    const module = await import("./desktop-bridge");
    expect(module.desktopVerificationSettingsAvailable()).toBe(false);
    await module.loadDesktopBootstrap();
    expect(module.desktopVerificationSettingsAvailable()).toBe(true);
    await expect(module.getDesktopVerificationSettings()).resolves.toEqual(initial);
    const request: import("./desktop-bridge").DesktopVerificationSettingsRequest = {
      protocol_version: module.desktopVerificationSettingsProtocol, expected_revision: 1, settings: saved.settings };
    expect((await module.confirmDesktopVerificationSettings(request)).view).toEqual(saved);
    expect(confirm).toHaveBeenCalledWith(request);
    expect(restart).not.toHaveBeenCalled();
    await expect(module.restartDesktopWithVerificationSettings()).resolves.toEqual(restartResult);
    expect(restart).toHaveBeenCalledWith({ protocol_version: module.desktopVerificationSettingsProtocol });
  });

  it("rejects mismatched native settings and execution grants instead of reporting a successful save", async () => {
    const initial = verificationView();
    const confirm = vi.fn().mockResolvedValue({ protocol_version: initial.protocol_version, status: "saved", replayed: false, view: initial });
    const restart = vi.fn().mockResolvedValue({ protocol_version: initial.protocol_version, status: "restarting", restart_required: true,
      arbitrary_arguments_accepted: false, persistent_runtime_grant: true });
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(restartBootstrap), GetVerificationSettings: vi.fn().mockResolvedValue(initial),
      ConfirmVerificationSettings: confirm, RestartWithVerificationSettings: restart });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.confirmDesktopVerificationSettings({ protocol_version: module.desktopVerificationSettingsProtocol,
      expected_revision: 1, settings: { ...initial.settings, ui_evidence_enabled: true } })).rejects.toThrow("不一致");
    await expect(module.restartDesktopWithVerificationSettings()).rejects.toThrow("无法核对");
    for (const invalid of [{ ...initial, capability_grant: true }, { ...initial, restart_required: true },
      { ...initial, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...initial, settings: { ...initial.settings, command: "injected" } },
      { ...initial, ui_evidence: { installed: true, blocked_by: [], remediation: [] } }]) {
      expect(() => module.parseDesktopVerificationSettings(invalid)).toThrow("无法核对");
    }
  });

  it("keeps verification settings unavailable without the verified native startup capability", async () => {
    const read = vi.fn();
    installBridge({ GetVerificationSettings: read, ConfirmVerificationSettings: vi.fn(), RestartWithVerificationSettings: vi.fn() });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopVerificationSettingsAvailable()).toBe(false);
    await expect(module.getDesktopVerificationSettings()).rejects.toThrow("不支持");
    expect(read).not.toHaveBeenCalled();
  });

  it("detects absence without changing ordinary browser behavior", async () => {
    const module = await import("./desktop-bridge");
    expect(module.desktopBridgeAvailable()).toBe(false);
    expect(module.desktopRuntimeActive()).toBe(false);
    await expect(module.loadDesktopBootstrap()).resolves.toBeNull();
  });

  it("accepts only a closed-authority same-origin bootstrap", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(bootstrap) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(bootstrap);
    expect(module.desktopRuntimeActive()).toBe(true);
  });

  it("accepts ChatGPT account management independently of run execution control", async () => {
    const accountBootstrap = { ...bootstrap, control_token: restartBootstrap.control_token,
      read_only_default: false, model_control_enabled: true, provider_credential_enabled: true,
      chatgpt_auth_enabled: true };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(accountBootstrap) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(accountBootstrap);
  });

  it("rejects ChatGPT authority without provider credential management", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap,
      control_token: restartBootstrap.control_token, read_only_default: false,
      model_control_enabled: true, chatgpt_auth_enabled: true }) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow();
  });

  it("restarts only through the native Debug profile contract", async () => {
    const result = {
      protocol_version: "desktop_risk_restart.v1",
      profile: "debug",
      status: "restarting",
      restart_required: true,
      arbitrary_arguments_accepted: false,
      persistent_runtime_grant: false,
    };
    const restart = vi.fn().mockResolvedValue(result);
    installBridge({
      Bootstrap: vi.fn().mockResolvedValue(restartBootstrap),
      RestartWithRiskProfile: restart,
    });
    const module = await import("./desktop-bridge");

    await module.loadDesktopBootstrap();

    expect(module.desktopDebugRestartEnabled()).toBe(true);
    await expect(module.restartDesktopInDebugMode()).resolves.toEqual(result);
    expect(restart).toHaveBeenCalledWith({
      protocol_version: "desktop_risk_restart.v1",
      profile: "debug",
    });
    expect(Object.keys(restart.mock.calls[0]![0]).sort()).toEqual([
      "profile", "protocol_version",
    ]);
  });

  it("keeps Debug restart disabled until bootstrap and the native method agree", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(restartBootstrap) });
    let module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopDebugRestartEnabled()).toBe(false);
    await expect(module.restartDesktopInDebugMode())
      .rejects.toThrow("disabled");

    vi.resetModules();
    const restart = vi.fn();
    installBridge({ RestartWithRiskProfile: restart });
    module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopDebugRestartEnabled()).toBe(false);
    await expect(module.restartDesktopInDebugMode())
      .rejects.toThrow("disabled");
    expect(restart).not.toHaveBeenCalled();
  });

  it("reads saved sandbox settings through the fixed native restart request only", async () => {
    const result = { protocol_version: "desktop_sandbox_restart.v1", status: "cancelled",
      restart_required: true, arbitrary_arguments_accepted: false, persistent_runtime_grant: false };
    const restart = vi.fn().mockResolvedValue(result);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(restartBootstrap), RestartWithSandboxSettings: restart });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopSandboxRestartEnabled()).toBe(true);
    await expect(module.restartDesktopWithSandboxSettings()).resolves.toEqual(result);
    expect(restart).toHaveBeenCalledExactlyOnceWith({ protocol_version: "desktop_sandbox_restart.v1" });
    for (const invalid of [
      { ...result, argv: ["--danger-full-access"] },
      { ...result, arbitrary_arguments_accepted: true },
      { ...result, persistent_runtime_grant: true },
      { ...result, restart_required: false },
      { ...result, status: "started" },
    ]) {
      restart.mockResolvedValueOnce(invalid);
      await expect(module.restartDesktopWithSandboxSettings()).rejects.toThrow("result was rejected");
    }
  });

  it("requires both bootstrap restart capability and the sandbox restart native method", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(restartBootstrap) });
    let module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopSandboxRestartEnabled()).toBe(false);
    await expect(module.restartDesktopWithSandboxSettings()).rejects.toThrow("unavailable");

    vi.resetModules();
    const restart = vi.fn();
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(bootstrap), RestartWithSandboxSettings: restart });
    module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopSandboxRestartEnabled()).toBe(false);
    await expect(module.restartDesktopWithSandboxSettings()).rejects.toThrow("unavailable");
    expect(restart).not.toHaveBeenCalled();
  });

  it("rejects non-closed Debug restart results", async () => {
    const valid = {
      protocol_version: "desktop_risk_restart.v1",
      profile: "debug",
      status: "cancelled",
      restart_required: true,
      arbitrary_arguments_accepted: false,
      persistent_runtime_grant: false,
    };
    const restart = vi.fn().mockResolvedValue(valid);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(restartBootstrap),
      RestartWithRiskProfile: restart });
    let module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();

    await expect(module.restartDesktopInDebugMode()).resolves.toEqual(valid);
    expect(restart).toHaveBeenCalledTimes(1);

    for (const invalid of [
      { ...valid, path: "C:\\PRIVATE" },
      { ...valid, argv: ["--danger-full-access"] },
      { ...valid, env: { SECRET: "value" } },
      { ...valid, environment: { SECRET: "value" } },
      { ...valid, pid: 1234 },
      { ...valid, arbitrary_arguments_accepted: true },
      { ...valid, persistent_runtime_grant: true },
      { ...valid, restart_required: false },
      { ...valid, status: "started" },
      { ...valid, profile: "full_access" },
    ]) {
      restart.mockResolvedValueOnce(invalid);
      await expect(module.restartDesktopInDebugMode())
        .rejects.toThrow("result was rejected");
    }

    vi.resetModules();
    const missingFlag = { ...bootstrap } as Record<string, unknown>;
    delete missingFlag.risk_profile_restart_enabled;
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(missingFlag) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
  });

  it("reports only the process-level Debug state after bootstrap", async () => {
    const fixtures = [
      [bootstrap, "safe"],
      [{ ...restartBootstrap, danger_full_access_enabled: true }, "safe"],
      [{ ...restartBootstrap, danger_full_access_enabled: true,
        user_terminal_enabled: true, process_execution_enabled: true, shell_execution_enabled: true }, "debug"],
    ] as const;
    for (const [fixture, expected] of fixtures) {
      vi.resetModules();
      delete window.go;
      installBridge({ Bootstrap: vi.fn().mockResolvedValue(fixture) });
      const module = await import("./desktop-bridge");
      expect(module.desktopCurrentRiskProfile()).toBeNull();
      await module.loadDesktopBootstrap();
      expect(module.desktopCurrentRiskProfile()).toBe(expected);
    }
  });

  it("rejects a restart capability without its control, permission, and approval closure", async () => {
    for (const invalid of [{
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      control_enabled: true,
      read_only_default: false,
      risk_profile_restart_enabled: true,
    }, {
      ...restartBootstrap,
      operator_approval_enabled: false,
    }]) {
      vi.resetModules();
      delete window.go;
      installBridge({ Bootstrap: vi.fn().mockResolvedValue(invalid),
        RestartWithRiskProfile: vi.fn() });
      const module = await import("./desktop-bridge");
      await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
    }
  });

  it("accepts Run creation without enabling existing Run controls", async () => {
    const creationOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      control_enabled: false,
      run_creation_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(creationOnly) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(creationOnly);
  });

  it("accepts Session messages without enabling other control capabilities", async () => {
    const messagesOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      session_message_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(messagesOnly) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(messagesOnly);
  });

  it("accepts evidence attachment as an independent capability", async () => {
    const evidenceOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      evidence_attachment_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(evidenceOnly) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(evidenceOnly);
  });

  it("accepts verification evidence as an independent capability", async () => {
    const verificationOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      verification_evidence_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(verificationOnly) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(verificationOnly);
  });

  it("accepts Session steering cancellation without enabling sibling capabilities", async () => {
    const steeringOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      session_steering_control_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(steeringOnly) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(steeringOnly);
  });

  it("accepts Plan and approval controls as independent capabilities", async () => {
    const planOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      plan_delivery_control_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(planOnly) });
    let module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(planOnly);

    vi.resetModules();
    const approvalOnly = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      approval_control_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(approvalOnly) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(approvalOnly);
  });


  it("accepts Docker execution with permission control and operator approval", async () => {
    const dockerEnabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      control_enabled: true,
      execution_permission_control_enabled: true,
      operator_approval_enabled: true,
      docker_execution_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(dockerEnabled) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(dockerEnabled);
  });

  it.each([
    "control_token", "batch_delivery_control_enabled", "execution_permission_control_enabled",
    "operator_approval_enabled", "danger_full_access_enabled",
  ])("rejects batch host validation without its %s gate", async (missing) => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      execution_permission_control_enabled: true,
      operator_approval_enabled: true,
      danger_full_access_enabled: true,
      batch_delivery_control_enabled: true,
      batch_delivery_host_validation_enabled: true,
      read_only_default: false,
      [missing]: missing === "control_token" ? "" : false,
    }) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("bootstrap was rejected");
    expect(module.desktopRuntimeActive()).toBe(false);
  });

  it("accepts Advanced Git only with permission, approval, and checkpoint gates", async () => {
    const enabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      execution_permission_control_enabled: true,
      operator_approval_enabled: true,
      workspace_checkpoint_control_enabled: true,
      git_advanced_control_enabled: true,
      github_review_control_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(enabled) });
    let module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(enabled);

    vi.resetModules();
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({
      ...enabled, workspace_checkpoint_control_enabled: false,
    }) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
  });

  it("rejects Docker execution authority without permission control", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      docker_execution_enabled: true,
      read_only_default: false,
    }) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
  });

  it("accepts an installed Full CDP session owner without treating it as Debug authority", async () => {
    const fullCDPAdapter = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      execution_permission_control_enabled: true,
      browser_cdp_permission_control_enabled: true,
      operator_approval_enabled: true,
      danger_full_access_enabled: true,
      full_cdp_debug_enabled: true,
      full_cdp_session_control_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(fullCDPAdapter) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).resolves.toEqual(fullCDPAdapter);
  });

  it.each([
    { full_cdp_debug_enabled: false, browser_cdp_permission_control_enabled: true,
      danger_full_access_enabled: true },
    { full_cdp_debug_enabled: true, browser_cdp_permission_control_enabled: false,
      danger_full_access_enabled: true },
    { full_cdp_debug_enabled: true, browser_cdp_permission_control_enabled: true,
      danger_full_access_enabled: false },
  ])("rejects Full CDP session availability without its production permission stack", async (flags) => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({
      ...bootstrap,
      ...flags,
      control_token: "control-token-0123456789abcdefghijkl",
      execution_permission_control_enabled: true,
      operator_approval_enabled: true,
      full_cdp_session_control_enabled: true,
      read_only_default: false,
    }) });
    const module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
  });

  it("rejects authority widening and extra local-file fields", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap, process_execution_enabled: true }) });
    let module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");

    vi.resetModules();
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      full_cdp_debug_enabled: true,
      read_only_default: false,
    }) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");

    vi.resetModules();
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap, source_path: "C:\\PRIVATE" }) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");

    vi.resetModules();
    const missingPermissionFlag = { ...bootstrap } as Record<string, unknown>;
    delete missingPermissionFlag.execution_permission_control_enabled;
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(missingPermissionFlag) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");

    vi.resetModules();
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({
      ...bootstrap,
      control_token: bootstrap.read_token,
      control_enabled: true,
    run_creation_enabled: false,
      read_only_default: false,
    }) });
    module = await import("./desktop-bridge");
    await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
  });

  it("opens the native picker and consumes only its opaque handle", async () => {
    const select = vi.fn().mockResolvedValue({
      protocol_version: "desktop_skill_package_dialog.v1",
      status: "selected",
      selection,
    });
    const consume = vi.fn().mockResolvedValue(preview);
    installBridge({ SelectSkillPackage: select, PreviewSkillPackage: consume });
    const module = await import("./desktop-bridge");
    await expect(module.selectDesktopSkillPreview()).resolves.toEqual(preview);
    expect(select).toHaveBeenCalledWith();
    expect(consume).toHaveBeenCalledWith(selection.handle);
  });

  it("installs only after an enabled bootstrap and validates inert authority", async () => {
    const enabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      skill_installation_enabled: true,
      read_only_default: false,
    };
    const result = {
      protocol_version: "desktop_skill_package_install.v1",
      name: preview.name, version: preview.version, surface: "code",
      trust_class: "operator_installed_untrusted",
      archive_sha256: preview.archive_sha256,
      package_fingerprint: preview.package_fingerprint,
      replayed: false, recovered_pending: false,
      import_command_execution: false, import_network_access: false,
      import_provider_calls: false, tool_capability_grant: false,
      run_selection_authorized: false, context_injection_authorized: false,
      receipt: { protocol_version: "operation_receipt.v1", kind: "skill_package_install",
        outcome: "installed", durable: true, replayed: false, retry_safe: true,
        retry_strategy: "same_operation_key", recovery_action: "none",
        cleanup_state: "not_applicable" },
    };
    const install = vi.fn().mockResolvedValue(result);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(enabled), InstallSkillPackage: install });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.installDesktopSkillPackage(preview, "code",
      "desktop-install-operation-0001")).resolves.toEqual(result);
    expect(install).toHaveBeenCalledWith({
      protocol_version: "desktop_skill_package_install.v1",
      confirmation_handle: preview.confirmation_handle,
      surface: "code",
      operation_key: "desktop-install-operation-0001",
      confirm_untrusted: true,
    });
  });

  it("previews native original files and validates the real staged Plugin result", async () => {
    const native = { protocol_version: "desktop_skill_package_preview.v1", package_protocol: "plugin-installation.v2",
      format: "agent-skills", name: "native-skill", version: "", archive_sha256: "b".repeat(64), archive_bytes: 1024,
      entry_count: 4, skill_count: 1, validated: true, confirmation_handle: preview.confirmation_handle,
      confirmation_expires_at: preview.confirmation_expires_at } as const;
    const installation = { protocol_version: "plugin-installation.v2", id: "plugin-import-native",
      manifest: { id: "portable-native", name: native.name, version: "", publisher: "", description: "", capabilities: ["skills"] },
      snapshot: { format: native.format, revision: native.archive_sha256, surface: "code" },
      source: { kind: "local_directory", uri: "", sha256: native.archive_sha256 },
      archive_sha256: native.archive_sha256, package_fingerprint: "c".repeat(64),
      signature_present: false, signature_valid: false, state: "staged", enabled_capabilities: [], generation: 1,
      staged_by: "desktop_operator", created_at: "2026-10-03T00:00:00Z", updated_at: "2026-10-03T00:00:00Z" };
    const result = { protocol_version: "plugin-installation.v2", installation, replayed: false };
    const install = vi.fn().mockResolvedValue(result);
    const previewCall = vi.fn().mockResolvedValue(native);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl", skill_installation_enabled: true, read_only_default: false }),
      SelectSkillPackage: vi.fn().mockResolvedValue({ protocol_version: "desktop_skill_package_dialog.v1", status: "selected", selection }),
      PreviewSkillPackage: previewCall, InstallSkillPackage: install });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.selectDesktopSkillPreview()).resolves.toEqual(native);
    await expect(module.installDesktopSkillPackage(native, "code", "native-install-operation")).resolves.toEqual(result);
    install.mockResolvedValueOnce({ ...result, installation: { ...installation, archive_sha256: "d".repeat(64) } });
    await expect(module.installDesktopSkillPackage(native, "code", "native-install-operation")).rejects.toThrow("rejected");
    install.mockResolvedValueOnce({ ...result, installation: { ...installation, source: { ...installation.source, uri: "C:\\private" } } });
    await expect(module.installDesktopSkillPackage(native, "code", "native-install-operation")).rejects.toThrow("rejected");
    previewCall.mockResolvedValueOnce({ ...native, source_path: "C:\\private" });
    await expect(module.selectDesktopSkillPreview()).rejects.toThrow("rejected");
  });

  it("lists and removes historical Skill packages only through an enabled native bridge", async () => {
    const item = {
      skill: "native.review@1.0.0", surface: "code", profiles: ["review"], surfaces: ["code"],
      phases: ["deliver"], roles: ["root"], user_invocable: true, model_invocable: false,
      explicit_only: true, tool_dependencies: ["read_file"], content_sha256: "a".repeat(64),
      content_bytes: 24, content_token_upper_bound: 24, archive_sha256: "b".repeat(64),
      package_fingerprint: "c".repeat(64), archive_bytes: 512, uncompressed_bytes: 256,
      entry_count: 2, trust_class: "operator_installed_untrusted", risk_codes: [],
      status: "installed", object_verified: false, operator_confirmed: true,
      import_command_execution: false, import_network_access: false,
      import_provider_calls: false, run_selection_authorized: false,
      context_injection_authorized: false, tool_capability_grant: false,
      content_body_exposed: false, installed_by: "desktop_operator",
      created_at: "2026-10-15T00:00:00Z", completed_at: "2026-10-15T00:00:01Z",
    };
    const inventory = { protocol_version: "skill_package_inventory.v1", capability_grant: false,
      surface: "", profile: "", include_removed: true, items: [item], returned_count: 1,
      unverified_count: 1 };
    const removal = {
      protocol_version: "skill_package_removal_control.v1", skill: "native.review@1.0.0",
      surface: "code", status: "removed", capability_grant: false,
      package_object_retained: true, historical_recovery_preserved: true,
      future_selection_enabled: false, run_selection_authorized: false,
      context_injection_authorized: false, tool_capability_grant: false,
      removed_by: "desktop_operator", removed_at: "2026-10-15T01:00:00Z", replayed: false,
      receipt: { protocol_version: "operation_receipt.v1", kind: "skill_package_remove",
        outcome: "removed", durable: true, replayed: false, retry_safe: true,
        retry_strategy: "same_operation_key", recovery_action: "none",
        cleanup_state: "not_applicable" },
    };
    const enabledBootstrap = { ...bootstrap, control_token: "control-token-0123456789abcdefghijkl",
      read_only_default: false, skill_installation_enabled: true };
    const list = vi.fn().mockResolvedValue(inventory);
    const remove = vi.fn().mockResolvedValue(removal);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(enabledBootstrap),
      ListSkillPackages: list, RemoveSkillPackage: remove });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();

    await expect(module.listDesktopSkillPackages({ includeRemoved: true }))
      .resolves.toEqual(inventory);
    expect(list).toHaveBeenCalledWith({
      protocol_version: "desktop_skill_package_inventory.v1", include_removed: true,
    });
    await expect(module.removeDesktopSkillPackage("native.review@1.0.0",
      "native-skill-remove-operation-0001")).resolves.toEqual(removal);
    expect(remove).toHaveBeenCalledWith({
      protocol_version: "desktop_skill_package_removal.v1", skill: "native.review@1.0.0",
      operation_key: "native-skill-remove-operation-0001", confirm_remove: true,
    });

    await expect(module.listDesktopSkillPackages({ surface: " ".repeat(200) }))
      .rejects.toThrow("rejected");
    await expect(module.removeDesktopSkillPackage("native.review",
      "native-skill-remove-operation-0002")).rejects.toThrow("rejected");
    await expect(module.removeDesktopSkillPackage("native.review@1.0.0", "short")).rejects.toThrow("rejected");
    expect(list).toHaveBeenCalledTimes(1);
    expect(remove).toHaveBeenCalledTimes(1);
  });

  it("treats a shell without the Registry methods as disabled", async () => {
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl", read_only_default: false,
      skill_installation_enabled: true }) });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.listDesktopSkillPackages()).rejects.toThrow("disabled");
    await expect(module.removeDesktopSkillPackage("native.review@1.0.0",
      "native-skill-remove-operation-0003")).rejects.toThrow("disabled");
  });

  it("does not consume a cancelled selection or path-bearing preview", async () => {
    const consume = vi.fn().mockResolvedValue(preview);
    installBridge({
      SelectSkillPackage: vi.fn().mockResolvedValue({
        protocol_version: "desktop_skill_package_dialog.v1",
        status: "cancelled",
        selection: null,
      }),
      PreviewSkillPackage: consume,
    });
    let module = await import("./desktop-bridge");
    await expect(module.selectDesktopSkillPreview()).resolves.toBeNull();
    expect(consume).not.toHaveBeenCalled();

    vi.resetModules();
    installBridge({
      SelectSkillPackage: vi.fn().mockResolvedValue({
        protocol_version: "desktop_skill_package_dialog.v1",
        status: "selected",
        selection,
      }),
      PreviewSkillPackage: vi.fn().mockResolvedValue({ ...preview, source_path: "C:\\PRIVATE" }),
    });
    module = await import("./desktop-bridge");
    await expect(module.selectDesktopSkillPreview()).rejects.toThrow("rejected");
  });

  it("selects a native folder without registering and creates only from the opaque handle", async()=>{
    const selected={handle:"a".repeat(64),directory_path:"D:/projects/chosen",name:"chosen"};
    const created={id:"ws-chosen",name:"Custom / name",created_at:"2026-10-20T00:00:00Z"};
    const choose=vi.fn().mockResolvedValue({status:"selected",selection:selected});const create=vi.fn().mockResolvedValue(created);
    installBridge({Bootstrap:vi.fn().mockResolvedValue({...bootstrap,control_token:"control-token-0123456789abcdefghijkl",control_enabled:false,run_creation_enabled:true,workspace_import_enabled:true,read_only_default:false}),SelectWorkspaceDirectory:choose,CreateWorkspace:create});
    const module=await import("./desktop-bridge");await module.loadDesktopBootstrap();
    expect(await module.selectDesktopWorkspaceDirectory()).toEqual(selected);expect(create).not.toHaveBeenCalled();
    expect(await module.createDesktopWorkspace(selected.handle," Custom / name ")).toEqual(created);expect(create).toHaveBeenCalledWith({handle:selected.handle,name:"Custom / name"});
    await expect(module.createDesktopWorkspace(selected.directory_path,"name")).rejects.toThrow();expect(create).toHaveBeenCalledTimes(1);
    choose.mockResolvedValue({status:"cancelled",selection:null});expect(await module.selectDesktopWorkspaceDirectory()).toBeNull();expect(create).toHaveBeenCalledTimes(1);
  });

  it("opens a registered Workspace only through a strict pathless native contract", async () => {
    const enabled = { ...bootstrap, workspace_open_enabled: true };
    const launchers = {
      protocol_version: "desktop_workspace_launcher_list.v1",
      workspace_id: "workspace-1",
      launchers: [
        { id: "file-explorer", label: "File Explorer", kind: "folder" },
        { id: "terminal", label: "Terminal", kind: "terminal" },
      ],
      root_path_exposed: false,
      renderer_path_input_supported: false,
      arbitrary_arguments_accepted: false,
      agent_authority_granted: false,
    };
    const result = {
      protocol_version: "desktop_workspace_open.v1",
      workspace_id: "workspace-1",
      launcher_id: "file-explorer",
      status: "started",
      operator_confirmed: true,
      external_process_started: true,
      arbitrary_arguments_accepted: false,
      command_executed: false,
      root_path_exposed: false,
      agent_authority_granted: false,
    };
    const list = vi.fn().mockResolvedValue(launchers);
    const open = vi.fn().mockResolvedValue(result);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(enabled),
      WorkspaceLaunchers: list, OpenWorkspace: open });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.listDesktopWorkspaceLaunchers("workspace-1"))
      .resolves.toEqual(launchers.launchers);
    await expect(module.openDesktopWorkspace("workspace-1", "file-explorer"))
      .resolves.toEqual(result);
    expect(list).toHaveBeenCalledWith("workspace-1");
    expect(open).toHaveBeenCalledWith({
      protocol_version: "desktop_workspace_open.v1",
      workspace_id: "workspace-1",
      launcher_id: "file-explorer",
    });
  });

  it("imports a selected directory through a strict pathless native contract", async () => {
    const enabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      run_creation_enabled: true,
      workspace_import_enabled: true,
      read_only_default: false,
    };
    const imported = {
      protocol_version: "desktop_workspace_import.v1",
      status: "registered",
      workspace: { id: "ws-import-0123456789abcdef", name: "project",
        created_at: "2026-08-13T01:02:03Z" },
      root_path_exposed: false,
      renderer_path_input_supported: false,
      directory_content_modified: false,
      agent_authority_granted: false,
    };
    const importWorkspace = vi.fn().mockResolvedValue(imported);
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(enabled),
      ImportWorkspace: importWorkspace });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.importDesktopWorkspace()).resolves.toEqual(imported.workspace);
    expect(module.desktopWorkspaceImportEnabled()).toBe(true);
    expect(importWorkspace).toHaveBeenCalledWith();

    importWorkspace.mockResolvedValue({ ...imported, root_path: "C:\\PRIVATE" });
    await expect(module.importDesktopWorkspace()).rejects.toThrow("rejected");
  });

  it("selects a chat directory natively without accepting a renderer path", async () => {
    const settings = { version: "projectless_directory.v1", directory_path: "D:\\Chats", default_directory_path: "C:\\Users\\me\\Documents\\Universal-Code" };
    const choose = vi.fn().mockResolvedValue({ status: "selected", settings });
    installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl", run_creation_enabled: true,
      session_message_enabled: true, thread_control_enabled: true, read_only_default: false,
      projectless_workspaces_enabled: true }), ChooseProjectlessDirectory: choose });
    const module = await import("./desktop-bridge");
    expect(module.desktopProjectlessDirectoryEnabled()).toBe(false);
    await module.loadDesktopBootstrap();
    expect(module.desktopProjectlessDirectoryEnabled()).toBe(true);
    await expect(module.chooseDesktopProjectlessDirectory()).resolves.toEqual(settings);
    expect(choose).toHaveBeenCalledExactlyOnceWith();
    choose.mockResolvedValueOnce({ status: "cancelled", settings: null });
    await expect(module.chooseDesktopProjectlessDirectory()).resolves.toBeNull();
    choose.mockResolvedValueOnce({ status: "selected", settings: { ...settings, arbitrary_path_input: true } });
    await expect(module.chooseDesktopProjectlessDirectory()).rejects.toThrow("rejected");
  });

  it("keeps chat directory selection disabled for legacy bootstraps and rejects inconsistent flags", async () => {
    const choose = vi.fn();
    installBridge({ ChooseProjectlessDirectory: choose });
    let module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    expect(module.desktopProjectlessDirectoryEnabled()).toBe(false);
    await expect(module.chooseDesktopProjectlessDirectory()).rejects.toThrow("disabled");
    expect(choose).not.toHaveBeenCalled();
    for (const flag of ["true", true]) {
      vi.resetModules();
      installBridge({ Bootstrap: vi.fn().mockResolvedValue({ ...bootstrap, projectless_workspaces_enabled: flag }) });
      module = await import("./desktop-bridge");
      await expect(module.loadDesktopBootstrap()).rejects.toThrow("rejected");
    }
  });

  it("treats a cancelled Workspace directory picker as a non-mutating result", async () => {
    const enabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      run_creation_enabled: true,
      workspace_import_enabled: true,
      read_only_default: false,
    };
    installBridge({ Bootstrap: vi.fn().mockResolvedValue(enabled),
      ImportWorkspace: vi.fn().mockResolvedValue({
        protocol_version: "desktop_workspace_import.v1",
        status: "cancelled",
        workspace: null,
        root_path_exposed: false,
        renderer_path_input_supported: false,
        directory_content_modified: false,
        agent_authority_granted: false,
      }) });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.importDesktopWorkspace()).resolves.toBeNull();
  });

  it("rejects path disclosure, arbitrary arguments, and inconsistent open receipts", async () => {
    const enabled = { ...bootstrap, workspace_open_enabled: true };
    installBridge({
      Bootstrap: vi.fn().mockResolvedValue(enabled),
      WorkspaceLaunchers: vi.fn().mockResolvedValue({
        protocol_version: "desktop_workspace_launcher_list.v1",
        workspace_id: "workspace-1",
        launchers: [],
        root_path_exposed: false,
        renderer_path_input_supported: false,
        arbitrary_arguments_accepted: false,
        agent_authority_granted: false,
        root_path: "C:\\PRIVATE",
      }),
    });
    let module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.listDesktopWorkspaceLaunchers("workspace-1")).rejects.toThrow("rejected");

    vi.resetModules();
    installBridge({
      Bootstrap: vi.fn().mockResolvedValue(enabled),
      OpenWorkspace: vi.fn().mockResolvedValue({
        protocol_version: "desktop_workspace_open.v1",
        workspace_id: "workspace-1",
        launcher_id: "terminal",
        status: "started",
        operator_confirmed: false,
        external_process_started: true,
        arbitrary_arguments_accepted: false,
        command_executed: false,
        root_path_exposed: false,
        agent_authority_granted: false,
      }),
    });
    module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.openDesktopWorkspace("workspace-1", "terminal")).rejects.toThrow("rejected");
    await expect(module.openDesktopWorkspace("workspace-1", "terminal -- powershell"))
      .rejects.toThrow("request was rejected");
  });

  it("uses a user-only terminal contract and validates decoded output bytes", async () => {
    const enabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      read_only_default: false,
      process_execution_enabled: true,
      shell_execution_enabled: true,
      user_terminal_enabled: true,
    };
    const session = {
      protocol_version: "desktop_user_terminal.v1",
      session_id: "user-terminal-1",
      run_id: "run-1",
      state: "running",
      backend: "windows-conpty",
      columns: 120,
      rows: 32,
      output_base_cursor: 0,
      output_next_cursor: 2,
      exit_code: 0,
      user_owned: true,
      agent_input_default: false,
      job_assigned_at_creation: true,
      kill_on_job_close: true,
      persistent: true,
      process_local: true,
      raw_output_persisted: false,
    };
    const start = vi.fn().mockResolvedValue(session);
    const get = vi.fn().mockResolvedValue(session);
    const read = vi.fn()
      .mockResolvedValueOnce({
        protocol_version: "desktop_user_terminal.v1",
        session_id: session.session_id,
        base_cursor: 0,
        next_cursor: 2,
        data_base64: "b2s=",
        data_bytes: 2,
        dropped: false,
        state: "running",
      })
      .mockResolvedValueOnce({
        protocol_version: "desktop_user_terminal.v1",
        session_id: session.session_id,
        base_cursor: 0,
        next_cursor: 2,
        data_base64: "b2s=",
        data_bytes: 3,
        dropped: false,
        state: "running",
      });
    const write = vi.fn().mockResolvedValue({
      protocol_version: "desktop_user_terminal.v1",
      session_id: session.session_id,
      bytes_written: 2,
    });
    const resize = vi.fn().mockResolvedValue(undefined);
    const close = vi.fn().mockResolvedValue(undefined);
    installBridge({
      Bootstrap: vi.fn().mockResolvedValue(enabled),
      StartUserTerminal: start,
      GetUserTerminal: get,
      ReadUserTerminal: read,
      WriteUserTerminal: write,
      ResizeUserTerminal: resize,
      CloseUserTerminal: close,
    });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.startDesktopUserTerminal("run-1")).resolves.toEqual(session);
    await expect(module.getDesktopUserTerminal(session.session_id)).resolves.toEqual(session);
    await expect(module.readDesktopUserTerminal(session.session_id, 0))
      .resolves.toMatchObject({ data_base64: "b2s=", data_bytes: 2 });
    await expect(module.writeDesktopUserTerminal(session.session_id, "ok"))
      .resolves.toMatchObject({ bytes_written: 2 });
    const maximumPaste = "中".repeat(5461) + "a";
    write.mockResolvedValueOnce({
      protocol_version: "desktop_user_terminal.v1", session_id: session.session_id, bytes_written: 16 * 1024,
    });
    await expect(module.writeDesktopUserTerminal(session.session_id, maximumPaste))
      .resolves.toMatchObject({ bytes_written: 16 * 1024 });
    await expect(module.writeDesktopUserTerminal(session.session_id, maximumPaste + "a"))
      .rejects.toThrow("input was rejected");
    expect(write).toHaveBeenCalledTimes(2);
    await expect(module.resizeDesktopUserTerminal(session.session_id, 100, 30))
      .resolves.toBeUndefined();
    await expect(module.closeDesktopUserTerminal(session.session_id))
      .resolves.toBeUndefined();
    expect(start).toHaveBeenCalledWith({
      protocol_version: "desktop_user_terminal.v1",
      run_id: "run-1",
      columns: 120,
      rows: 32,
      replace_existing: false,
      confirm_debug_boundary: true,
    });
    expect(write).toHaveBeenCalledWith({
      protocol_version: "desktop_user_terminal.v1",
      session_id: session.session_id,
      data: "ok",
      user_confirmed: true,
    });
    await expect(module.readDesktopUserTerminal(session.session_id, 0))
      .rejects.toThrow("output was rejected");
    for (const state of ["starting", "closed"]) {
      get.mockResolvedValueOnce({ ...session, state });
      await expect(module.getDesktopUserTerminal(session.session_id)).resolves.toMatchObject({ state });
      read.mockResolvedValueOnce({ protocol_version: "desktop_user_terminal.v1", session_id: session.session_id,
        base_cursor: 0, next_cursor: 0, data_base64: "", data_bytes: 0, dropped: false, state });
      await expect(module.readDesktopUserTerminal(session.session_id, 0)).resolves.toMatchObject({ state });
    }
    get.mockResolvedValueOnce({ ...session, state: "invented" });
    await expect(module.getDesktopUserTerminal(session.session_id)).rejects.toThrow("rejected");
  });

  it("grants only a bounded process-local Full terminal Agent-input lease", async () => {
    const enabled = {
      ...bootstrap,
      control_token: "control-token-0123456789abcdefghijkl",
      read_only_default: false,

      workspace_sandbox_enabled: false,
      execution_permission_control_enabled: true,
      operator_approval_enabled: true,
      danger_full_access_enabled: true,
      process_execution_enabled: true,
      shell_execution_enabled: true,
      user_terminal_enabled: true,
    };
    const binding = {
      protocol_version: "desktop_debug_terminal_agent_input.v1",
      binding_id: "terminal-input-lease-1",
      run_id: "run-1",
      terminal_session_id: "user-terminal-1",
      issued_at: "2026-08-18T01:00:00Z",
      expires_at: "2026-08-18T01:05:00Z",
      process_local: true,
      token_exposed: false,
      raw_input_persisted: false,
    };
    const grant = vi.fn().mockResolvedValue(binding);
    const get = vi.fn().mockResolvedValue(binding);
    const revoke = vi.fn().mockResolvedValue(undefined);
    installBridge({
      Bootstrap: vi.fn().mockResolvedValue(enabled),
      GrantDebugTerminalAgentInput: grant,
      GetDebugTerminalAgentInput: get,
      RevokeDebugTerminalAgentInput: revoke,
    });
    const module = await import("./desktop-bridge");
    await module.loadDesktopBootstrap();
    await expect(module.grantDesktopDebugTerminalAgentInput(
      "run-1", "user-terminal-1", 300)).resolves.toEqual(binding);
    await expect(module.getDesktopDebugTerminalAgentInput("run-1"))
      .resolves.toEqual(binding);
    await expect(module.revokeDesktopDebugTerminalAgentInput(binding.binding_id))
      .resolves.toBeUndefined();
    expect(grant).toHaveBeenCalledWith({
      protocol_version: "desktop_debug_terminal_agent_input.v1",
      run_id: "run-1",
      terminal_session_id: "user-terminal-1",
      ttl_seconds: 300,
      confirm_full_access: true,
      confirm_agent_terminal_input: true,
    });
    expect(revoke).toHaveBeenCalledWith({
      protocol_version: "desktop_debug_terminal_agent_input.v1",
      binding_id: binding.binding_id,
      operator_confirmed: true,
    });
  });
});

function verificationView(enabled = false) {
  return { protocol_version: "desktop_verification_settings.v1", revision: enabled ? 2 : 1,
    settings: { ui_evidence_enabled: enabled, batch_validation_enabled: false },
    active_settings: { ui_evidence_enabled: false, batch_validation_enabled: false },
    restart_required: enabled, can_save: true, can_restart: enabled, capability_grant: false,
    ui_evidence: { installed: false, blocked_by: ["startup_gate_closed"], remediation: [enabled ? "restart_application" : "enable_and_restart"] },
    batch_validation: { installed: false, blocked_by: ["startup_gate_closed"], remediation: ["enable_and_restart"] } };
}

function installBridge(overrides: Partial<{
  GetVerificationSettings: () => Promise<unknown>;
  ConfirmVerificationSettings: (request: unknown) => Promise<unknown>;
  RestartWithVerificationSettings: (request: unknown) => Promise<unknown>;
  Bootstrap: () => Promise<unknown>;
  ImportWorkspace: () => Promise<unknown>;
  SelectWorkspaceDirectory: () => Promise<unknown>;
  CreateWorkspace: (request: {handle:string;name:string}) => Promise<unknown>;
  ChooseProjectlessDirectory: () => Promise<unknown>;
  InstallSkillPackage: (request: unknown) => Promise<unknown>;
  ListSkillPackages: (request: unknown) => Promise<unknown>;
  RemoveSkillPackage: (request: unknown) => Promise<unknown>;
  PreviewSkillPackage: (handle: string) => Promise<unknown>;
  SelectSkillPackage: () => Promise<unknown>;
  OpenWorkspace: (request: unknown) => Promise<unknown>;
  WorkspaceLaunchers: (workspaceID: string) => Promise<unknown>;
  StartUserTerminal: (request: unknown) => Promise<unknown>;
  GetUserTerminal: (sessionID: string) => Promise<unknown>;
  ReadUserTerminal: (request: unknown) => Promise<unknown>;
  WriteUserTerminal: (request: unknown) => Promise<unknown>;
  ResizeUserTerminal: (request: unknown) => Promise<unknown>;
  CloseUserTerminal: (request: unknown) => Promise<unknown>;
  GrantDebugTerminalAgentInput: (request: unknown) => Promise<unknown>;
  GetDebugTerminalAgentInput: (runID: string) => Promise<unknown>;
  RevokeDebugTerminalAgentInput: (request: unknown) => Promise<void>;
  RestartWithRiskProfile: (request: unknown) => Promise<unknown>;
  RestartWithSandboxSettings: (request: unknown) => Promise<unknown>;
}>) {
  window.go = {
    desktop: {
      DesktopBridge: {
        Bootstrap: vi.fn().mockResolvedValue(bootstrap),
        InstallSkillPackage: vi.fn().mockRejectedValue(new Error("disabled")),
        ImportWorkspace: vi.fn().mockRejectedValue(new Error("disabled")),
        PreviewSkillPackage: vi.fn().mockResolvedValue(preview),
        OpenWorkspace: vi.fn().mockRejectedValue(new Error("disabled")),
        SelectSkillPackage: vi.fn().mockResolvedValue({
          protocol_version: "desktop_skill_package_dialog.v1",
          status: "cancelled",
          selection: null,
        }),
        WorkspaceLaunchers: vi.fn().mockRejectedValue(new Error("disabled")),
        ...overrides,
      },
    },
  };
}
