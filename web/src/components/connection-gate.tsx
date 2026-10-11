import { useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowRight, LoaderCircle } from "lucide-react";
import { APIClient, clientCapabilitiesFromRuntime } from "../api/client";
import { desktopBridgeAvailable, desktopErrorMessage, loadDesktopBootstrap } from "../lib/desktop-bridge";
import { useConnectionStore } from "../state/connection";
import { useLocale } from "../lib/locale";
import { PrayuBrand } from "./prayu-brand";

export function ConnectionGate() {
  const { t } = useLocale();
  const [token, setToken] = useState("");
  const [controlToken, setControlToken] = useState("");
  const [error, setError] = useState("");
  const [connecting, setConnecting] = useState(false);
  const connect = useConnectionStore((state) => state.connect);
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!desktopBridgeAvailable()) {
      return;
    }
    let active = true;
    setConnecting(true);
    setError("");
    void loadDesktopBootstrap().then(async (bootstrap) => {
      if (!bootstrap || !active) {
        return;
      }
      const client = new APIClient(bootstrap.read_token, bootstrap.api_base_url,
        bootstrap.control_token);
      const [health, runtime] = await Promise.all([client.health(), client.runtimeCapabilities()]);
      if (!active) {
        return;
      }
      queryClient.clear();
      connect(bootstrap.read_token, health, bootstrap.control_token, {
        runControlEnabled: bootstrap.control_enabled,
        // This HTTP capability is separate from the native directory picker.
        workspaceImportEnabled: false,
        executionPermissionControlEnabled: bootstrap.execution_permission_control_enabled,
        workspaceSandboxEnabled: bootstrap.workspace_sandbox_enabled,
        browserCDPPermissionControlEnabled:
          bootstrap.browser_cdp_permission_control_enabled,
        fullCDPDebugEnabled: bootstrap.full_cdp_debug_enabled,
        fullCDPSessionControlEnabled: bootstrap.full_cdp_session_control_enabled,
        operatorApprovalEnabled: bootstrap.operator_approval_enabled,
        dangerFullAccessEnabled: bootstrap.danger_full_access_enabled,

        commandRuntimeEnabled: bootstrap.command_runtime_enabled,
        commandRuntimeProtocolAvailable: bootstrap.command_runtime_protocol_available,
        commandRuntimeAdapterInstalled: bootstrap.command_runtime_adapter_installed,
        commandRuntimeAdapterReady: bootstrap.command_runtime_adapter_ready,
        runCreationEnabled: bootstrap.run_creation_enabled,
        standardCodePresetEnabled: bootstrap.standard_code_preset_enabled,
        sessionMessageEnabled: bootstrap.session_message_enabled,
        threadControlEnabled: bootstrap.thread_control_enabled,
        sessionSteeringControlEnabled: bootstrap.session_steering_control_enabled,
        runLifecycleEnabled: bootstrap.run_lifecycle_enabled,
        runExecutionEnabled: bootstrap.run_execution_enabled,
        threadExecutionReadEnabled: runtime.thread_execution_read_enabled === true,
        planDeliveryControlEnabled: bootstrap.plan_delivery_control_enabled,
        approvalControlEnabled: bootstrap.approval_control_enabled,


		modelControlEnabled: bootstrap.model_control_enabled,
		providerCredentialEnabled: bootstrap.provider_credential_enabled,
		fileEditReviewEnabled: bootstrap.file_edit_review_enabled,
		fileEditProposalEnabled: bootstrap.file_edit_proposal_enabled,
		fileEditApplyEnabled: bootstrap.file_edit_apply_enabled,
		runWakeControlEnabled: bootstrap.run_wake_control_enabled,
		runWakeExecutionEnabled: bootstrap.run_wake_execution_enabled,
		runWakeWorkerEnabled: bootstrap.run_wake_worker_enabled,
		scheduledJobControlEnabled: bootstrap.scheduled_job_control_enabled,
		scheduledJobWorkerEnabled: bootstrap.scheduled_job_worker_enabled,
		skillInstallationEnabled: bootstrap.skill_installation_enabled,
		evidenceAttachmentEnabled: bootstrap.evidence_attachment_enabled,
		verificationEvidenceEnabled: bootstrap.verification_evidence_enabled,
		uiEvidenceControlEnabled: bootstrap.ui_evidence_control_enabled,
		embeddedAnalyzerExecutionEnabled: bootstrap.embedded_analyzer_execution_enabled,
		gitAdvancedControlEnabled: bootstrap.git_advanced_control_enabled,
		githubReviewControlEnabled: bootstrap.github_review_control_enabled,
		workspaceCheckpointControlEnabled: bootstrap.workspace_checkpoint_control_enabled,
		batchDeliveryControlEnabled: bootstrap.batch_delivery_control_enabled,
		batchDeliveryHostValidationEnabled: bootstrap.batch_delivery_host_validation_enabled,
		dockerExecutionEnabled: bootstrap.docker_execution_enabled,
		agentCodeToolsEnabled: bootstrap.agent_code_tools_enabled,
		codeIntelEnabled: bootstrap.code_intel_enabled,
      }, client.baseURL);
    }).catch((caught: unknown) => {
      if (active) {
        setError(desktopErrorMessage(caught));
      }
    }).finally(() => {
      if (active) {
        setConnecting(false);
      }
    });
    return () => {
      active = false;
    };
  }, [connect, queryClient]);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const candidate = token.trim();
    if (!candidate || connecting) {
      return;
    }
    setConnecting(true);
    setError("");
    try {
      const client = new APIClient(candidate);
      const [health, capabilities] = await Promise.all([
        client.health(), client.runtimeCapabilities(),
      ]);
      queryClient.clear();
      connect(candidate, health, controlToken.trim(), clientCapabilitiesFromRuntime(capabilities),
        client.baseURL);
      setToken("");
      setControlToken("");
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("连接失败，请检查服务地址与访问令牌。", "Connection failed. Check the service address and access token."));
    } finally {
      setConnecting(false);
    }
  };

  return (
    <main className="connection-page">
      <form className="connection-panel" onSubmit={submit}>
        <PrayuBrand className="connection-brand" variant="hero" />
        <div className="connection-heading">
          <h1>{t("连接 Universal-Code", "Connect to Universal-Code")}</h1>
          <p>{t("填写服务提供的访问令牌，打开项目和任务。", "Enter the access token supplied by your service to open projects and tasks.")}</p>
        </div>
        {connecting && desktopBridgeAvailable() &&
          <div className="desktop-connecting"><LoaderCircle aria-hidden="true" className="spin" size={16} />{t("启动桌面工作台", "Starting desktop workbench")}</div>}
        <label className="field-label" htmlFor="read-token">{t("查看令牌", "Read access token")}</label>
        <div className="token-row">
          <input
            autoCapitalize="none"
            autoComplete="off"
            autoCorrect="off"
            id="read-token"
            aria-describedby="read-token-help"
            name="read-token"
            onChange={(event) => setToken(event.target.value)}
            placeholder="CYBERAGENT_API_TOKEN"
            spellCheck={false}
            type="password"
            value={token}
          />
          <button aria-label="连接" disabled={!token.trim() || connecting} title="连接" type="submit">
            {connecting ? <LoaderCircle aria-hidden="true" className="spin" size={18} /> : <ArrowRight aria-hidden="true" size={18} />}
          </button>
        </div>
        <p className="connection-token-help" id="read-token-help">{t("用于查看项目、对话和执行记录。", "View projects, conversations and execution history.")}</p>
        <label className="field-label optional-token-label" htmlFor="control-token">
          {t("操作令牌", "Control access token")} <span>{t("可选", "optional")}</span>
        </label>
        <input
          autoCapitalize="none"
          autoComplete="off"
          autoCorrect="off"
          className="control-token-input"
          id="control-token"
          aria-describedby="control-token-help"
          name="control-token"
          onChange={(event) => setControlToken(event.target.value)}
          placeholder="CYBERAGENT_API_CONTROL_TOKEN"
          spellCheck={false}
          type="password"
          value={controlToken}
        />
        <p className="connection-token-help" id="control-token-help">{t("创建任务和执行操作时填写，具体操作范围取决于服务设置。", "Add this token to create tasks and use the actions enabled by your service.")}</p>
        {error && <div className="connection-error" role="alert">{error}</div>}
      </form>
    </main>
  );
}
