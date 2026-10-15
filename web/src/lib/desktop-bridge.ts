import { parsePluginSkillInstall, parseSkillPackageInventory, parseSkillPackageRemoval } from "../api/client";
import type { PluginSkillInstallView, SkillPackageInventoryView, SkillPackageRemovalView } from "../api/types";
import { validImageAttachments, type WorkspaceImageAttachment } from "../api/image-attachments";
import { validFileAttachments, type WorkspaceFileAttachment } from "../api/file-attachments";

export const desktopConnectionProtocol = "desktop_connection_bootstrap.v1";
export const desktopClipboardFilesProtocol = "desktop_clipboard_files.v1";

export interface DesktopClipboardFilesRequest {
  version: typeof desktopClipboardFilesProtocol;
  workspace_id: string;
  operation_key: string;
}
export interface DesktopClipboardFilesResult {
  version: typeof desktopClipboardFilesProtocol;
  workspace_id: string;
  status: "processed" | "empty" | "unsupported" | "partial" | "unknown";
  batch_complete: boolean;
  images: WorkspaceImageAttachment[];
  attachments: WorkspaceFileAttachment[];
  rejected: Array<{ name: string; code: string; message: string }>;
}
export const desktopSkillDialogProtocol = "desktop_skill_package_dialog.v1";
export const desktopSkillSelectionProtocol = "desktop_file_selection.v1";
export const desktopSkillPreviewProtocol = "desktop_skill_package_preview.v1";
export const desktopSkillInstallProtocol = "desktop_skill_package_install.v1";
export const desktopSkillInventoryProtocol = "desktop_skill_package_inventory.v1";
export const desktopSkillRemovalProtocol = "desktop_skill_package_removal.v1";
export const desktopWorkspaceLauncherProtocol = "desktop_workspace_launcher_list.v1";
export const desktopWorkspaceOpenProtocol = "desktop_workspace_open.v1";
export const desktopWorkspaceImportProtocol = "desktop_workspace_import.v1";
export const desktopUserTerminalProtocol = "desktop_user_terminal.v1";
export const desktopDebugTerminalAgentInputProtocol =
  "desktop_debug_terminal_agent_input.v1";
export const desktopRiskRestartProtocol = "desktop_risk_restart.v1";
export const desktopSandboxRestartProtocol = "desktop_sandbox_restart.v1";

export interface DesktopSandboxRestartRequest {
  protocol_version: typeof desktopSandboxRestartProtocol;
}
export interface DesktopSandboxRestartResult {
  protocol_version: typeof desktopSandboxRestartProtocol;
  status: "cancelled" | "restarting";
  restart_required: true;
  arbitrary_arguments_accepted: false;
  persistent_runtime_grant: false;
}

export type DesktopDebugRiskProfile = "debug";
export type DesktopRuntimeRiskProfile = "safe" | DesktopDebugRiskProfile;

export interface DesktopDebugRestartRequest {
  protocol_version: typeof desktopRiskRestartProtocol;
  profile: DesktopDebugRiskProfile;
}

export interface DesktopDebugRestartResult {
  protocol_version: typeof desktopRiskRestartProtocol;
  profile: DesktopDebugRiskProfile;
  status: "cancelled" | "restarting";
  restart_required: true;
  arbitrary_arguments_accepted: false;
  persistent_runtime_grant: false;
}

export interface DesktopOperationReceipt {
  protocol_version: "operation_receipt.v1";
  kind: "skill_package_install";
  outcome: "installed";
  durable: true;
  replayed: boolean;
  retry_safe: true;
  retry_strategy: "same_operation_key";
  recovery_action: "none";
  cleanup_state: "not_applicable";
}

export interface DesktopConnectionBootstrap {
  protocol_version: typeof desktopConnectionProtocol;
  api_base_url: "/api/v1";
  api_version: "api.v1";
  app_version: string;
  ui_digest: string;
  read_token: string;
  control_token: string;
  control_enabled: boolean;
  execution_permission_control_enabled: boolean;
  workspace_sandbox_enabled: boolean;
  browser_cdp_permission_control_enabled: boolean;
  full_cdp_debug_enabled: boolean;
  full_cdp_session_control_enabled: boolean;
  operator_approval_enabled: boolean;
  danger_full_access_enabled: boolean;
  command_runtime_enabled: boolean;
  command_runtime_protocol_available: true;
  command_runtime_adapter_installed: boolean;
  command_runtime_adapter_ready: boolean;
  thread_control_enabled: boolean;
  run_creation_enabled: boolean;
  standard_code_preset_enabled: boolean;
  session_message_enabled: boolean;
  session_steering_control_enabled: boolean;
  run_lifecycle_enabled: boolean;
  run_execution_enabled: boolean;
  plan_delivery_control_enabled: boolean;
  approval_control_enabled: boolean;
  model_control_enabled: boolean;
  provider_credential_enabled: boolean;
  chatgpt_auth_enabled?: boolean;
  file_edit_review_enabled: boolean;
  file_edit_proposal_enabled: boolean;
  run_wake_control_enabled: boolean;
  file_edit_apply_enabled: boolean;
  run_wake_execution_enabled: boolean;
  run_wake_worker_enabled: boolean;
  scheduled_job_control_enabled: boolean;
  scheduled_job_worker_enabled: boolean;
  read_only_default: boolean;
  process_execution_enabled: boolean;
  shell_execution_enabled: boolean;
  docker_execution_enabled: boolean;
  agent_code_tools_enabled: true;
  code_intel_enabled: boolean;
  skill_installation_enabled: boolean;
  evidence_attachment_enabled: boolean;
  verification_evidence_enabled: boolean;
  embedded_analyzer_execution_enabled: boolean;
  workspace_checkpoint_control_enabled: boolean;
  git_advanced_control_enabled: boolean;
  github_review_control_enabled: boolean;
  batch_delivery_control_enabled: boolean;
  batch_delivery_host_validation_enabled: boolean;
  ui_evidence_control_enabled: boolean;
  user_terminal_enabled: boolean;
  agent_terminal_input_default: false;
  workspace_open_enabled: boolean;
  workspace_import_enabled: boolean;
  risk_profile_restart_enabled: boolean;
  renderer_path_input_supported: false;
}

export interface DesktopTerminalSession {
  protocol_version: typeof desktopUserTerminalProtocol;
  session_id: string;
  run_id: string;
  state: "running" | "exited" | "failed";
  backend: string;
  columns: number;
  rows: number;
  output_base_cursor: number;
  output_next_cursor: number;
  exit_code: number;
  user_owned: true;
  agent_input_default: false;
  job_assigned_at_creation: true;
  kill_on_job_close: true;
  persistent: true;
  process_local: true;
  raw_output_persisted: false;
}

export interface DesktopTerminalOutput {
  protocol_version: typeof desktopUserTerminalProtocol;
  session_id: string;
  base_cursor: number;
  next_cursor: number;
  data_base64: string;
  data_bytes: number;
  dropped: boolean;
  state: "running" | "exited" | "failed";
}

export interface DesktopTerminalWriteResult {
  protocol_version: typeof desktopUserTerminalProtocol;
  session_id: string;
  bytes_written: number;
}

export interface DesktopDebugTerminalAgentInputBinding {
  protocol_version: typeof desktopDebugTerminalAgentInputProtocol;
  binding_id: string;
  run_id: string;
  terminal_session_id: string;
  issued_at: string;
  expires_at: string;
  process_local: true;
  token_exposed: false;
  raw_input_persisted: false;
}

export interface DesktopWorkspaceLauncher {
  id: string;
  label: string;
  kind: "folder" | "terminal" | "editor";
}

export interface DesktopWorkspaceLauncherList {
  protocol_version: typeof desktopWorkspaceLauncherProtocol;
  workspace_id: string;
  launchers: DesktopWorkspaceLauncher[];
  root_path_exposed: false;
  renderer_path_input_supported: false;
  arbitrary_arguments_accepted: false;
  agent_authority_granted: false;
}

export interface DesktopWorkspaceOpenRequest {
  protocol_version: typeof desktopWorkspaceOpenProtocol;
  workspace_id: string;
  launcher_id: string;
}

export interface DesktopWorkspaceOpenResult {
  protocol_version: typeof desktopWorkspaceOpenProtocol;
  workspace_id: string;
  launcher_id: string;
  status: "cancelled" | "started";
  operator_confirmed: boolean;
  external_process_started: boolean;
  arbitrary_arguments_accepted: false;
  command_executed: false;
  root_path_exposed: false;
  agent_authority_granted: false;
}

export interface DesktopImportedWorkspace {
  id: string;
  name: string;
  created_at: string;
}

export interface DesktopWorkspaceImportResult {
  protocol_version: typeof desktopWorkspaceImportProtocol;
  status: "cancelled" | "registered";
  workspace: DesktopImportedWorkspace | null;
  root_path_exposed: false;
  renderer_path_input_supported: false;
  directory_content_modified: false;
  agent_authority_granted: false;
}

export interface DesktopSkillSelection {
  protocol_version: typeof desktopSkillSelectionProtocol;
  handle: string;
  expires_at: string;
}

export interface DesktopSkillDialogResult {
  protocol_version: typeof desktopSkillDialogProtocol;
  status: "cancelled" | "selected";
  selection: DesktopSkillSelection | null;
}

export interface DesktopLegacySkillPreview {
  protocol_version: typeof desktopSkillPreviewProtocol;
  package_protocol: string;
  skill_protocol: string;
  name: string;
  version: string;
  profiles: string[];
  declared_tools: string[];
  declared_tool_count: number;
  content_bytes: number;
  content_token_upper_bound: number;
  archive_sha256: string;
  package_fingerprint: string;
  archive_bytes: number;
  uncompressed_bytes: number;
  entry_count: number;
  trust_class: string;
  risk_codes: string[];
  executable_asset_count: number;
  install_hook_count: number;
  import_command_execution: false;
  import_network_access: false;
  import_provider_calls: false;
  tool_capability_grant: false;
  installation_authorized: false;
  validated: true;
  confirmation_handle: string;
  confirmation_expires_at: string;
}

export interface DesktopNativeSkillPreview {
  protocol_version: typeof desktopSkillPreviewProtocol;
  package_protocol: "plugin-installation.v2";
  format: "agent-skills" | "agent-plugins";
  name: string;
  version: string;
  archive_sha256: string;
  archive_bytes: number;
  entry_count: number;
  skill_count: number;
  validated: true;
  confirmation_handle: string;
  confirmation_expires_at: string;
}
export type DesktopSkillPreview = DesktopLegacySkillPreview | DesktopNativeSkillPreview;

export interface DesktopSkillInstallRequest {
  protocol_version: typeof desktopSkillInstallProtocol;
  confirmation_handle: string;
  surface: "code" | "cyber";
  operation_key: string;
  confirm_untrusted: true;
}

export interface DesktopSkillInventoryRequest {
  protocol_version: typeof desktopSkillInventoryProtocol;
  surface?: string;
  profile?: string;
  include_removed?: boolean;
}

export interface DesktopSkillRemovalRequest {
  protocol_version: typeof desktopSkillRemovalProtocol;
  skill: string;
  operation_key: string;
  confirm_remove: true;
}

export type DesktopSkillInstallResult = DesktopLegacySkillInstallResult | PluginSkillInstallView;

export interface DesktopLegacySkillInstallResult {
  protocol_version: typeof desktopSkillInstallProtocol;
  name: string;
  version: string;
  surface: "code" | "cyber";
  trust_class: "operator_installed_untrusted";
  archive_sha256: string;
  package_fingerprint: string;
  replayed: boolean;
  recovered_pending: boolean;
  import_command_execution: false;
  import_network_access: false;
  import_provider_calls: false;
  tool_capability_grant: false;
  run_selection_authorized: false;
  context_injection_authorized: false;
  receipt: DesktopOperationReceipt;
}

interface NativeDesktopBridge {
  Bootstrap: () => Promise<unknown>;
  PasteClipboardFiles?: (request: DesktopClipboardFilesRequest) => Promise<unknown>;
  InspectClipboardFiles?: (request: DesktopClipboardFilesRequest) => Promise<unknown>;
  ImportWorkspace?: () => Promise<unknown>;
  InstallSkillPackage: (request: DesktopSkillInstallRequest) => Promise<unknown>;
  ListSkillPackages?: (request: DesktopSkillInventoryRequest) => Promise<unknown>;
  RemoveSkillPackage?: (request: DesktopSkillRemovalRequest) => Promise<unknown>;
  OpenWorkspace?: (request: DesktopWorkspaceOpenRequest) => Promise<unknown>;
  PreviewSkillPackage: (handle: string) => Promise<unknown>;
  SelectSkillPackage: () => Promise<unknown>;
  WorkspaceLaunchers?: (workspaceID: string) => Promise<unknown>;
  StartUserTerminal?: (request: {
    protocol_version: typeof desktopUserTerminalProtocol;
    run_id: string;
    columns: number;
    rows: number;
    replace_existing: boolean;
    confirm_debug_boundary: true;
  }) => Promise<unknown>;
  GetUserTerminal?: (sessionID: string) => Promise<unknown>;
  ReadUserTerminal?: (request: {
    protocol_version: typeof desktopUserTerminalProtocol;
    session_id: string;
    cursor: number;
    max_bytes: number;
  }) => Promise<unknown>;
  WriteUserTerminal?: (request: {
    protocol_version: typeof desktopUserTerminalProtocol;
    session_id: string;
    data: string;
    user_confirmed: true;
  }) => Promise<unknown>;
  ResizeUserTerminal?: (request: {
    protocol_version: typeof desktopUserTerminalProtocol;
    session_id: string;
    columns: number;
    rows: number;
    user_confirmed: true;
  }) => Promise<unknown>;
  CloseUserTerminal?: (request: {
    protocol_version: typeof desktopUserTerminalProtocol;
    session_id: string;
    user_confirmed: true;
  }) => Promise<unknown>;
  GrantDebugTerminalAgentInput?: (request: {
    protocol_version: typeof desktopDebugTerminalAgentInputProtocol;
    run_id: string;
    terminal_session_id: string;
    ttl_seconds: number;
    confirm_full_access: true;
    confirm_agent_terminal_input: true;
  }) => Promise<unknown>;
  GetDebugTerminalAgentInput?: (runID: string) => Promise<unknown>;
  RevokeDebugTerminalAgentInput?: (request: {
    protocol_version: typeof desktopDebugTerminalAgentInputProtocol;
    binding_id: string;
    operator_confirmed: true;
  }) => Promise<void>;
  RestartWithRiskProfile?: (request: DesktopDebugRestartRequest) => Promise<unknown>;
  RestartWithSandboxSettings?: (request: DesktopSandboxRestartRequest) => Promise<unknown>;
}

type NativeWorkspaceBridge = NativeDesktopBridge & Required<Pick<NativeDesktopBridge,
  "OpenWorkspace" | "WorkspaceLaunchers">>;
type NativeWorkspaceImportBridge = NativeDesktopBridge & Required<Pick<NativeDesktopBridge,
  "ImportWorkspace">>;
type NativeTerminalBridge = NativeDesktopBridge & Required<Pick<NativeDesktopBridge,
  "StartUserTerminal" | "GetUserTerminal" | "ReadUserTerminal" |
  "WriteUserTerminal" | "ResizeUserTerminal" | "CloseUserTerminal">>;
type NativeDebugTerminalAgentInputBridge = NativeDesktopBridge & Required<Pick<NativeDesktopBridge,
  "GrantDebugTerminalAgentInput" | "GetDebugTerminalAgentInput" |
  "RevokeDebugTerminalAgentInput">>;
type NativeDebugRestartBridge = NativeDesktopBridge & Required<Pick<NativeDesktopBridge,
  "RestartWithRiskProfile">>;

declare global {
  interface Window {
    go?: {
      desktop?: {
        DesktopBridge?: Partial<NativeDesktopBridge>;
      };
    };
  }
}

let activeBootstrap: DesktopConnectionBootstrap | null = null;
let bootstrapPromise: Promise<DesktopConnectionBootstrap> | null = null;

export function desktopBridgeAvailable(): boolean {
  return getBridge() !== null;
}

export function desktopClipboardFilesAvailable(): boolean {
  const bridge = getBridge();
  return typeof bridge?.PasteClipboardFiles === "function" && typeof bridge?.InspectClipboardFiles === "function";
}

const clipboardIdentity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
function clipboardRequest(workspaceID: string, key: string): DesktopClipboardFilesRequest {
  if (!clipboardIdentity(workspaceID) || !clipboardIdentity(key) || key.length < 16) throw new Error("原生粘贴请求身份无效。");
  return { version: desktopClipboardFilesProtocol, workspace_id: workspaceID, operation_key: key };
}

export function parseDesktopClipboardFilesResult(value: unknown, workspaceID: string, inspecting: boolean): DesktopClipboardFilesResult {
  const invalid = () => new Error("原生粘贴结果无法可靠核对；请保留原请求，尚未加入附件。");
  if (!isRecord(value) || Object.keys(value).some((key) => !["version", "workspace_id", "status", "batch_complete", "images", "attachments", "rejected"].includes(key)) ||
    value.version !== desktopClipboardFilesProtocol || value.workspace_id !== workspaceID || typeof value.batch_complete !== "boolean" ||
    !validImageAttachments(value.images, workspaceID) || !validFileAttachments(value.attachments, workspaceID) ||
    !Array.isArray(value.rejected) || value.images.length + value.attachments.length + value.rejected.length > 4 ||
    !value.rejected.every((item) => isRecord(item) && Object.keys(item).length === 3 && typeof item.name === "string" && item.name.length > 0 && [...item.name].length <= 160 &&
      !/[\u0000-\u001f\u007f/\\]/u.test(item.name) && typeof item.code === "string" && /^[A-Z_]{1,64}$/u.test(item.code) &&
      typeof item.message === "string" && item.message.length > 0 && item.message.length <= 2048)) throw invalid();
  const count = value.images.length + value.attachments.length;
  if (value.status === "unsupported") {
    if (value.batch_complete || count || value.rejected.length) throw invalid();
  } else if (inspecting) {
    if (value.batch_complete || value.rejected.length || (value.status !== "partial" && value.status !== "unknown") ||
      (value.status === "partial" ? count === 0 : count !== 0)) throw invalid();
  } else if (!value.batch_complete || (value.status !== "processed" && value.status !== "empty") ||
    (value.status === "empty" ? count + value.rejected.length !== 0 : count + value.rejected.length === 0)) throw invalid();
  return value as unknown as DesktopClipboardFilesResult;
}

export async function pasteDesktopClipboardFiles(workspaceID: string, key: string): Promise<DesktopClipboardFilesResult> {
  const request = clipboardRequest(workspaceID, key);
  const bridge = getBridge();
  if (!bridge?.PasteClipboardFiles || !bridge.InspectClipboardFiles) throw new Error("当前界面不支持原生文件粘贴，请使用选择文件。");
  return parseDesktopClipboardFilesResult(await bridge.PasteClipboardFiles(request), workspaceID, false);
}

export async function inspectDesktopClipboardFiles(workspaceID: string, key: string): Promise<DesktopClipboardFilesResult> {
  const request = clipboardRequest(workspaceID, key);
  const bridge = getBridge();
  if (!bridge?.InspectClipboardFiles) throw new Error("当前界面无法核对原生粘贴记录。");
  return parseDesktopClipboardFilesResult(await bridge.InspectClipboardFiles(request), workspaceID, true);
}

// desktopIsMacPlatform reports whether the Desktop shell runs inside the
// macOS WebKit renderer. The shell keeps native traffic lights, so the
// renderer reserves their left titlebar space instead of drawing overlapping
// navigation or duplicating the window controls.
export function desktopIsMacPlatform(): boolean {
  if (!desktopBridgeAvailable()) {
    return false;
  }
  const platform = typeof navigator === "undefined" ? "" : (navigator.platform ?? "");
  return typeof platform === "string" && platform.toLowerCase().includes("mac");
}

export function desktopRuntimeActive(): boolean {
  return activeBootstrap !== null;
}

export function desktopWorkspaceImportEnabled(): boolean {
  return activeBootstrap?.workspace_import_enabled === true && getWorkspaceImportBridge() !== null;
}

export function desktopDebugRestartEnabled(): boolean {
  return activeBootstrap?.risk_profile_restart_enabled === true && getDebugRestartBridge() !== null;
}

export function desktopSandboxRestartEnabled(): boolean {
  return activeBootstrap?.risk_profile_restart_enabled === true &&
    typeof getBridge()?.RestartWithSandboxSettings === "function";
}

export async function restartDesktopWithSandboxSettings(): Promise<DesktopSandboxRestartResult> {
  const bridge = getBridge();
  if (!desktopSandboxRestartEnabled() || !bridge?.RestartWithSandboxSettings) {
    throw new Error("Desktop sandbox settings restart is unavailable");
  }
  const value = await bridge.RestartWithSandboxSettings({ protocol_version: desktopSandboxRestartProtocol });
  if (!hasExactKeys(value, ["protocol_version", "status", "restart_required", "arbitrary_arguments_accepted", "persistent_runtime_grant"]) ||
    value.protocol_version !== desktopSandboxRestartProtocol || !["cancelled", "restarting"].includes(String(value.status)) ||
    value.restart_required !== true || value.arbitrary_arguments_accepted !== false || value.persistent_runtime_grant !== false) {
    throw new Error("Desktop sandbox restart result was rejected");
  }
  return value as unknown as DesktopSandboxRestartResult;
}

export function desktopCurrentRiskProfile(): DesktopRuntimeRiskProfile | null {
  if (!activeBootstrap) return null;
  if (activeBootstrap.user_terminal_enabled) return "debug";
  return "safe";
}

export async function loadDesktopBootstrap(): Promise<DesktopConnectionBootstrap | null> {
  if (activeBootstrap) {
    return activeBootstrap;
  }
  const bridge = getBridge();
  if (!bridge) {
    return null;
  }
  if (!bootstrapPromise) {
    bootstrapPromise = bridge.Bootstrap().then((value) => {
      if (!validBootstrap(value)) {
        throw new Error("Desktop connection bootstrap was rejected");
      }
      activeBootstrap = value;
      return value;
    });
  }
  try {
    return await bootstrapPromise;
  } catch (error) {
    bootstrapPromise = null;
    throw error;
  }
}

export async function selectDesktopSkillPreview(): Promise<DesktopSkillPreview | null> {
  const bridge = getBridge();
  if (!bridge) {
    throw new Error("Desktop native bridge is unavailable");
  }
  const dialogValue = await bridge.SelectSkillPackage();
  if (!validDialogResult(dialogValue)) {
    throw new Error("Desktop Skill selection result was rejected");
  }
  if (dialogValue.status === "cancelled") {
    return null;
  }
  if (!dialogValue.selection) {
    throw new Error("Desktop Skill selection result was rejected");
  }
  const previewValue = await bridge.PreviewSkillPackage(dialogValue.selection.handle);
  if (!validPreview(previewValue)) {
    throw new Error("Desktop Skill preview was rejected");
  }
  return previewValue;
}

export async function importDesktopWorkspace(): Promise<DesktopImportedWorkspace | null> {
  const bridge = getWorkspaceImportBridge();
  if (!bridge || !activeBootstrap?.workspace_import_enabled) {
    throw new Error("Desktop Workspace import is disabled");
  }
  const value = await bridge.ImportWorkspace();
  if (!validWorkspaceImportResult(value)) {
    throw new Error("Desktop Workspace import result was rejected");
  }
  return value.status === "cancelled" ? null : value.workspace;
}

export async function restartDesktopInDebugMode(): Promise<DesktopDebugRestartResult> {
  const bridge = getDebugRestartBridge();
  if (!bridge || !activeBootstrap?.risk_profile_restart_enabled) {
    throw new Error("Desktop Debug restart is disabled");
  }
  const request: DesktopDebugRestartRequest = {
    protocol_version: desktopRiskRestartProtocol,
    profile: "debug",
  };
  const value = await bridge.RestartWithRiskProfile(request);
  if (!validDebugRestartResult(value)) {
    throw new Error("Desktop Debug restart result was rejected");
  }
  return value;
}

export async function installDesktopSkillPackage(preview: DesktopSkillPreview,
  surface: "code" | "cyber", operationKey: string): Promise<DesktopSkillInstallResult> {
  const bridge = getBridge();
  if (!bridge || !activeBootstrap?.skill_installation_enabled) {
    throw new Error("Desktop Skill installation is disabled");
  }
  if (!validPreview(preview) || !boundedText(operationKey, 16, 256) || /\s/u.test(operationKey)) {
    throw new Error("Desktop Skill installation request was rejected");
  }
  const value = await bridge.InstallSkillPackage({
    protocol_version: desktopSkillInstallProtocol,
    confirmation_handle: preview.confirmation_handle,
    surface,
    operation_key: operationKey,
    confirm_untrusted: true,
  });
  if (!validInstallResult(value, preview, surface)) {
    throw new Error("Desktop Skill installation result was rejected");
  }
  return value;
}

export async function listDesktopSkillPackages(filters: {
  surface?: string; profile?: string; includeRemoved?: boolean;
} = {}): Promise<SkillPackageInventoryView> {
  const bridge = getBridge();
  if (!bridge?.ListSkillPackages || !activeBootstrap?.skill_installation_enabled) {
    throw new Error("Desktop Skill package Registry is disabled");
  }
  const request: DesktopSkillInventoryRequest = {
    protocol_version: desktopSkillInventoryProtocol,
  };
  if (filters.surface !== undefined) {
    if (!boundedText(filters.surface, 1, 64)) {
      throw new Error("Desktop Skill inventory surface was rejected");
    }
    request.surface = filters.surface;
  }
  if (filters.profile !== undefined) {
    if (!boundedText(filters.profile, 1, 64)) {
      throw new Error("Desktop Skill inventory Profile was rejected");
    }
    request.profile = filters.profile;
  }
  if (filters.includeRemoved !== undefined) request.include_removed = filters.includeRemoved;
  return parseSkillPackageInventory(await bridge.ListSkillPackages(request));
}

export async function removeDesktopSkillPackage(skill: string,
  operationKey: string): Promise<SkillPackageRemovalView> {
  const bridge = getBridge();
  if (!bridge?.RemoveSkillPackage || !activeBootstrap?.skill_installation_enabled) {
    throw new Error("Desktop Skill package Registry is disabled");
  }
  if (!boundedText(skill, 3, 512) || !skill.includes("@") ||
    !boundedText(operationKey, 16, 256) || /\s/u.test(operationKey)) {
    throw new Error("Desktop Skill package removal request was rejected");
  }
  const request: DesktopSkillRemovalRequest = {
    protocol_version: desktopSkillRemovalProtocol,
    skill,
    operation_key: operationKey,
    confirm_remove: true,
  };
  return parseSkillPackageRemoval(await bridge.RemoveSkillPackage(request), request);
}

export async function listDesktopWorkspaceLaunchers(
  workspaceID: string,
): Promise<DesktopWorkspaceLauncher[]> {
  const bridge = getWorkspaceBridge();
  if (!bridge || !activeBootstrap?.workspace_open_enabled) {
    throw new Error("Desktop workspace opening is disabled");
  }
  if (!validWorkspaceID(workspaceID)) {
    throw new Error("Desktop workspace launcher request was rejected");
  }
  const value = await bridge.WorkspaceLaunchers(workspaceID);
  if (!validWorkspaceLauncherList(value, workspaceID)) {
    throw new Error("Desktop workspace launcher list was rejected");
  }
  return value.launchers;
}

export async function openDesktopWorkspace(workspaceID: string,
  launcherID: string): Promise<DesktopWorkspaceOpenResult> {
  const bridge = getWorkspaceBridge();
  if (!bridge || !activeBootstrap?.workspace_open_enabled) {
    throw new Error("Desktop workspace opening is disabled");
  }
  if (!validWorkspaceID(workspaceID) || !validLauncherID(launcherID)) {
    throw new Error("Desktop workspace open request was rejected");
  }
  const request: DesktopWorkspaceOpenRequest = {
    protocol_version: desktopWorkspaceOpenProtocol,
    workspace_id: workspaceID,
    launcher_id: launcherID,
  };
  const value = await bridge.OpenWorkspace(request);
  if (!validWorkspaceOpenResult(value, request)) {
    throw new Error("Desktop workspace open result was rejected");
  }
  return value;
}

export function desktopUserTerminalEnabled(): boolean {
  return activeBootstrap?.user_terminal_enabled === true && getTerminalBridge() !== null;
}

export function desktopDebugTerminalAgentInputEnabled(): boolean {
  return activeBootstrap?.user_terminal_enabled === true &&
    activeBootstrap.danger_full_access_enabled === true &&
    getDebugTerminalAgentInputBridge() !== null;
}

export async function startDesktopUserTerminal(runID: string,
  columns = 120, rows = 32, replaceExisting = false): Promise<DesktopTerminalSession> {
  const bridge = getTerminalBridge();
  if (!bridge || !activeBootstrap?.user_terminal_enabled) {
    throw new Error("Desktop user terminal is disabled");
  }
  if (!validWorkspaceID(runID) || !validTerminalSize(columns, rows)) {
    throw new Error("Desktop terminal start request was rejected");
  }
  const value = await bridge.StartUserTerminal({
    protocol_version: desktopUserTerminalProtocol,
    run_id: runID,
    columns,
    rows,
    replace_existing: replaceExisting,
    confirm_debug_boundary: true,
  });
  if (!validTerminalSession(value, runID)) {
    throw new Error("Desktop terminal session was rejected");
  }
  return value;
}

export async function getDesktopUserTerminal(
  sessionID: string,
): Promise<DesktopTerminalSession> {
  const bridge = getTerminalBridge();
  if (!bridge || !activeBootstrap?.user_terminal_enabled || !validWorkspaceID(sessionID)) {
    throw new Error("Desktop user terminal is disabled");
  }
  const value = await bridge.GetUserTerminal(sessionID);
  if (!validTerminalSession(value)) {
    throw new Error("Desktop terminal session was rejected");
  }
  return value;
}

export async function readDesktopUserTerminal(sessionID: string,
  cursor: number, maxBytes = 65536): Promise<DesktopTerminalOutput> {
  const bridge = getTerminalBridge();
  if (!bridge || !activeBootstrap?.user_terminal_enabled ||
    !validWorkspaceID(sessionID) || !safeCount(cursor) ||
    !Number.isInteger(maxBytes) || maxBytes < 1 || maxBytes > 65536) {
    throw new Error("Desktop terminal read request was rejected");
  }
  const value = await bridge.ReadUserTerminal({
    protocol_version: desktopUserTerminalProtocol,
    session_id: sessionID,
    cursor,
    max_bytes: maxBytes,
  });
  if (!validTerminalOutput(value, sessionID)) {
    throw new Error("Desktop terminal output was rejected");
  }
  return value;
}

export async function writeDesktopUserTerminal(sessionID: string,
  data: string): Promise<DesktopTerminalWriteResult> {
  const bridge = getTerminalBridge();
  const bytes = new TextEncoder().encode(data).byteLength;
  if (!bridge || !activeBootstrap?.user_terminal_enabled ||
    !validWorkspaceID(sessionID) || bytes < 1 || bytes > 16 * 1024) {
    throw new Error("Desktop terminal input was rejected");
  }
  const value = await bridge.WriteUserTerminal({
    protocol_version: desktopUserTerminalProtocol,
    session_id: sessionID,
    data,
    user_confirmed: true,
  });
  if (!validTerminalWriteResult(value, sessionID, bytes)) {
    throw new Error("Desktop terminal write result was rejected");
  }
  return value;
}

export async function resizeDesktopUserTerminal(sessionID: string,
  columns: number, rows: number): Promise<void> {
  const bridge = getTerminalBridge();
  if (!bridge || !activeBootstrap?.user_terminal_enabled ||
    !validWorkspaceID(sessionID) || !validTerminalSize(columns, rows)) {
    throw new Error("Desktop terminal resize request was rejected");
  }
  await bridge.ResizeUserTerminal({
    protocol_version: desktopUserTerminalProtocol,
    session_id: sessionID,
    columns,
    rows,
    user_confirmed: true,
  });
}

export async function closeDesktopUserTerminal(sessionID: string): Promise<void> {
  const bridge = getTerminalBridge();
  if (!bridge || !activeBootstrap?.user_terminal_enabled || !validWorkspaceID(sessionID)) {
    throw new Error("Desktop terminal close request was rejected");
  }
  await bridge.CloseUserTerminal({
    protocol_version: desktopUserTerminalProtocol,
    session_id: sessionID,
    user_confirmed: true,
  });
}

export async function grantDesktopDebugTerminalAgentInput(runID: string,
  terminalSessionID: string, ttlSeconds = 300):
  Promise<DesktopDebugTerminalAgentInputBinding> {
  const bridge = getDebugTerminalAgentInputBridge();
  if (!bridge || !desktopDebugTerminalAgentInputEnabled() ||
    !validWorkspaceID(runID) || !validWorkspaceID(terminalSessionID) ||
    !Number.isInteger(ttlSeconds) || ttlSeconds < 15 || ttlSeconds > 900) {
    throw new Error("Desktop Debug terminal Agent-input grant was rejected");
  }
  const value = await bridge.GrantDebugTerminalAgentInput({
    protocol_version: desktopDebugTerminalAgentInputProtocol,
    run_id: runID,
    terminal_session_id: terminalSessionID,
    ttl_seconds: ttlSeconds,
    confirm_full_access: true,
    confirm_agent_terminal_input: true,
  });
  if (!validDebugTerminalAgentInputBinding(value, runID, terminalSessionID)) {
    throw new Error("Desktop Debug terminal Agent-input binding was rejected");
  }
  return value;
}

export async function getDesktopDebugTerminalAgentInput(runID: string):
  Promise<DesktopDebugTerminalAgentInputBinding> {
  const bridge = getDebugTerminalAgentInputBridge();
  if (!bridge || !desktopDebugTerminalAgentInputEnabled() || !validWorkspaceID(runID)) {
    throw new Error("Desktop Debug terminal Agent input is disabled");
  }
  const value = await bridge.GetDebugTerminalAgentInput(runID);
  if (!validDebugTerminalAgentInputBinding(value, runID)) {
    throw new Error("Desktop Debug terminal Agent-input binding was rejected");
  }
  return value;
}

export async function revokeDesktopDebugTerminalAgentInput(bindingID: string): Promise<void> {
  const bridge = getDebugTerminalAgentInputBridge();
  if (!bridge || !desktopDebugTerminalAgentInputEnabled() || !validWorkspaceID(bindingID)) {
    throw new Error("Desktop Debug terminal Agent-input revoke was rejected");
  }
  await bridge.RevokeDebugTerminalAgentInput({
    protocol_version: desktopDebugTerminalAgentInputProtocol,
    binding_id: bindingID,
    operator_confirmed: true,
  });
}

export function desktopErrorMessage(value: unknown): string {
  if (value instanceof Error && value.message.trim()) {
    return value.message;
  }
  if (isRecord(value) && typeof value.message === "string" && value.message.trim()) {
    return value.message;
  }
  return "Desktop operation failed";
}

function getBridge(): NativeDesktopBridge | null {
  const candidate = window.go?.desktop?.DesktopBridge;
  if (!candidate || typeof candidate.Bootstrap !== "function" ||
    typeof candidate.InstallSkillPackage !== "function" ||
    typeof candidate.SelectSkillPackage !== "function" ||
    typeof candidate.PreviewSkillPackage !== "function") {
    return null;
  }
  return candidate as NativeDesktopBridge;
}

function getWorkspaceBridge(): NativeWorkspaceBridge | null {
  const bridge = getBridge();
  if (!bridge || typeof bridge.OpenWorkspace !== "function" ||
    typeof bridge.WorkspaceLaunchers !== "function") {
    return null;
  }
  return bridge as NativeWorkspaceBridge;
}

function getWorkspaceImportBridge(): NativeWorkspaceImportBridge | null {
  const bridge = getBridge();
  if (!bridge || typeof bridge.ImportWorkspace !== "function") {
    return null;
  }
  return bridge as NativeWorkspaceImportBridge;
}

function getTerminalBridge(): NativeTerminalBridge | null {
  const bridge = getBridge();
  if (!bridge || typeof bridge.StartUserTerminal !== "function" ||
    typeof bridge.GetUserTerminal !== "function" ||
    typeof bridge.ReadUserTerminal !== "function" ||
    typeof bridge.WriteUserTerminal !== "function" ||
    typeof bridge.ResizeUserTerminal !== "function" ||
    typeof bridge.CloseUserTerminal !== "function") {
    return null;
  }
  return bridge as NativeTerminalBridge;
}

function getDebugTerminalAgentInputBridge(): NativeDebugTerminalAgentInputBridge | null {
  const bridge = getBridge();
  if (!bridge || typeof bridge.GrantDebugTerminalAgentInput !== "function" ||
    typeof bridge.GetDebugTerminalAgentInput !== "function" ||
    typeof bridge.RevokeDebugTerminalAgentInput !== "function") {
    return null;
  }
  return bridge as NativeDebugTerminalAgentInputBridge;
}

function getDebugRestartBridge(): NativeDebugRestartBridge | null {
  const bridge = getBridge();
  if (!bridge || typeof bridge.RestartWithRiskProfile !== "function") {
    return null;
  }
  return bridge as NativeDebugRestartBridge;
}

function validBootstrap(value: unknown): value is DesktopConnectionBootstrap {
  const chatGPTKeys = isRecord(value) && Object.hasOwn(value, "chatgpt_auth_enabled")
    ? ["chatgpt_auth_enabled"] : [];
  if (!hasExactKeys(value, [
    ...chatGPTKeys,
    "agent_code_tools_enabled", "api_base_url", "api_version", "app_version",
    "code_intel_enabled",
    "approval_control_enabled", "command_runtime_enabled",
    "command_runtime_protocol_available", "command_runtime_adapter_installed",
    "command_runtime_adapter_ready",
    "execution_permission_control_enabled", "operator_approval_enabled",
    "workspace_sandbox_enabled",
    "browser_cdp_permission_control_enabled", "full_cdp_debug_enabled",
    "full_cdp_session_control_enabled",
    "danger_full_access_enabled",
    "control_enabled", "control_token", "docker_execution_enabled", "file_edit_apply_enabled",
    "evidence_attachment_enabled",
	"verification_evidence_enabled", "embedded_analyzer_execution_enabled",
	"workspace_checkpoint_control_enabled", "git_advanced_control_enabled",
	"github_review_control_enabled",
	"batch_delivery_control_enabled",
	"batch_delivery_host_validation_enabled", "ui_evidence_control_enabled",
    "user_terminal_enabled", "agent_terminal_input_default",
    "file_edit_proposal_enabled", "file_edit_review_enabled", "model_control_enabled",
    "provider_credential_enabled", "process_execution_enabled",
    "protocol_version", "read_only_default",
    "plan_delivery_control_enabled", "read_token", "renderer_path_input_supported",
    "thread_control_enabled",
    "run_creation_enabled", "standard_code_preset_enabled", "shell_execution_enabled",
    "run_execution_enabled", "run_lifecycle_enabled", "run_wake_control_enabled",
    "run_wake_execution_enabled", "run_wake_worker_enabled",
    "scheduled_job_control_enabled", "scheduled_job_worker_enabled",
    "session_message_enabled", "skill_installation_enabled", "ui_digest",
    "session_steering_control_enabled",
    "workspace_import_enabled",
    "workspace_open_enabled",
    "risk_profile_restart_enabled",
  ])) {
    return false;
  }
  return value.protocol_version === desktopConnectionProtocol && value.api_base_url === "/api/v1" &&
    value.agent_code_tools_enabled === true &&
    typeof value.code_intel_enabled === "boolean" &&
    value.api_version === "api.v1" && boundedText(value.app_version, 1, 64) &&
    isSHA256(value.ui_digest) && validToken(value.read_token) &&
    typeof value.control_token === "string" && typeof value.control_enabled === "boolean" &&
    typeof value.execution_permission_control_enabled === "boolean" &&
    typeof value.workspace_sandbox_enabled === "boolean" &&
    typeof value.browser_cdp_permission_control_enabled === "boolean" &&
    typeof value.full_cdp_debug_enabled === "boolean" &&
    typeof value.full_cdp_session_control_enabled === "boolean" &&
    typeof value.operator_approval_enabled === "boolean" &&
    typeof value.danger_full_access_enabled === "boolean" &&
    typeof value.command_runtime_enabled === "boolean" &&
    value.command_runtime_protocol_available === true &&
    typeof value.command_runtime_adapter_installed === "boolean" &&
    typeof value.command_runtime_adapter_ready === "boolean" &&
    (!value.command_runtime_adapter_ready || value.command_runtime_adapter_installed) &&
    typeof value.thread_control_enabled === "boolean" &&
    typeof value.run_creation_enabled === "boolean" &&
    typeof value.standard_code_preset_enabled === "boolean" &&
    typeof value.session_message_enabled === "boolean" &&
    typeof value.session_steering_control_enabled === "boolean" &&
    typeof value.run_lifecycle_enabled === "boolean" &&
    typeof value.run_execution_enabled === "boolean" &&
    typeof value.plan_delivery_control_enabled === "boolean" &&
    typeof value.approval_control_enabled === "boolean" &&
	typeof value.model_control_enabled === "boolean" &&
	typeof value.provider_credential_enabled === "boolean" &&
    (value.chatgpt_auth_enabled === undefined || typeof value.chatgpt_auth_enabled === "boolean") &&
    (!value.chatgpt_auth_enabled || (value.model_control_enabled && value.provider_credential_enabled)) &&
	typeof value.file_edit_review_enabled === "boolean" &&
	typeof value.file_edit_proposal_enabled === "boolean" &&
	typeof value.run_wake_control_enabled === "boolean" &&
    typeof value.file_edit_apply_enabled === "boolean" &&
    typeof value.run_wake_execution_enabled === "boolean" &&
    typeof value.run_wake_worker_enabled === "boolean" &&
    typeof value.scheduled_job_control_enabled === "boolean" &&
    typeof value.scheduled_job_worker_enabled === "boolean" &&
    typeof value.skill_installation_enabled === "boolean" &&
    typeof value.evidence_attachment_enabled === "boolean" &&
    typeof value.verification_evidence_enabled === "boolean" &&
    typeof value.embedded_analyzer_execution_enabled === "boolean" &&
    typeof value.workspace_checkpoint_control_enabled === "boolean" &&
    typeof value.git_advanced_control_enabled === "boolean" &&
    typeof value.github_review_control_enabled === "boolean" &&
    typeof value.batch_delivery_control_enabled === "boolean" &&
    typeof value.batch_delivery_host_validation_enabled === "boolean" &&
    typeof value.ui_evidence_control_enabled === "boolean" &&
    typeof value.docker_execution_enabled === "boolean" &&
    typeof value.user_terminal_enabled === "boolean" &&
    value.agent_terminal_input_default === false &&
    typeof value.workspace_import_enabled === "boolean" &&
    typeof value.workspace_open_enabled === "boolean" &&
    typeof value.risk_profile_restart_enabled === "boolean" &&
    (!value.risk_profile_restart_enabled || (value.control_token !== "" &&
      value.execution_permission_control_enabled && value.operator_approval_enabled)) &&
    (value.control_token !== "") === (value.control_enabled || value.run_creation_enabled ||
      value.standard_code_preset_enabled ||
      value.execution_permission_control_enabled ||
      value.browser_cdp_permission_control_enabled ||
      value.full_cdp_session_control_enabled ||
      value.session_message_enabled || value.session_steering_control_enabled ||
      value.run_lifecycle_enabled || value.run_execution_enabled ||
	  value.plan_delivery_control_enabled || value.approval_control_enabled ||
	  value.model_control_enabled || value.provider_credential_enabled ||
	  value.file_edit_review_enabled || value.file_edit_proposal_enabled ||
	  value.run_wake_control_enabled || value.file_edit_apply_enabled ||
      value.run_wake_execution_enabled || value.run_wake_worker_enabled ||
      value.scheduled_job_control_enabled || value.scheduled_job_worker_enabled ||
      value.skill_installation_enabled ||
      value.evidence_attachment_enabled || value.verification_evidence_enabled ||
	  value.embedded_analyzer_execution_enabled || value.workspace_checkpoint_control_enabled ||
	  value.git_advanced_control_enabled || value.github_review_control_enabled ||
	  value.batch_delivery_control_enabled || value.batch_delivery_host_validation_enabled ||
	  value.ui_evidence_control_enabled ||
	  value.docker_execution_enabled ||
      value.user_terminal_enabled) &&
    (value.control_token === "" || validToken(value.control_token)) &&
    value.control_token !== value.read_token &&
    ((!value.execution_permission_control_enabled &&
      !value.workspace_sandbox_enabled && !value.operator_approval_enabled && !value.danger_full_access_enabled) ||
      (value.execution_permission_control_enabled &&
      (!value.danger_full_access_enabled || value.operator_approval_enabled))) &&

    (!value.batch_delivery_host_validation_enabled ||
      (value.batch_delivery_control_enabled &&
        value.execution_permission_control_enabled && value.operator_approval_enabled &&
        value.danger_full_access_enabled)) &&
    (!value.git_advanced_control_enabled ||
      (value.execution_permission_control_enabled && value.operator_approval_enabled &&
        value.workspace_checkpoint_control_enabled)) &&
    (!value.github_review_control_enabled ||
      (value.execution_permission_control_enabled && value.operator_approval_enabled)) &&
    (!value.full_cdp_debug_enabled ||
      (value.browser_cdp_permission_control_enabled && value.danger_full_access_enabled)) &&
    (!value.full_cdp_session_control_enabled ||
      (value.full_cdp_debug_enabled && value.browser_cdp_permission_control_enabled &&
        value.danger_full_access_enabled)) &&
    (!value.ui_evidence_control_enabled ||
      (value.command_runtime_enabled && value.browser_cdp_permission_control_enabled &&
        value.run_execution_enabled)) &&
    value.command_runtime_enabled ===
      (value.run_execution_enabled && value.command_runtime_adapter_ready) &&
    value.thread_control_enabled ===
      (value.run_creation_enabled && value.session_message_enabled) &&
    value.read_only_default === !(value.control_enabled || value.run_creation_enabled ||
      value.standard_code_preset_enabled ||
      value.execution_permission_control_enabled ||
      value.browser_cdp_permission_control_enabled ||
      value.full_cdp_session_control_enabled ||
      value.session_message_enabled || value.session_steering_control_enabled ||
      value.run_lifecycle_enabled || value.run_execution_enabled ||
	  value.plan_delivery_control_enabled || value.approval_control_enabled ||
	  value.model_control_enabled || value.provider_credential_enabled ||
	  value.file_edit_review_enabled || value.file_edit_proposal_enabled ||
	  value.run_wake_control_enabled || value.file_edit_apply_enabled ||
      value.run_wake_execution_enabled || value.run_wake_worker_enabled ||
      value.scheduled_job_control_enabled || value.scheduled_job_worker_enabled ||
      value.skill_installation_enabled ||
      value.evidence_attachment_enabled || value.verification_evidence_enabled ||
	  value.embedded_analyzer_execution_enabled || value.workspace_checkpoint_control_enabled ||
	  value.git_advanced_control_enabled || value.github_review_control_enabled ||
	  value.batch_delivery_control_enabled || value.batch_delivery_host_validation_enabled ||
	  value.ui_evidence_control_enabled ||
	  value.docker_execution_enabled ||
      value.user_terminal_enabled) &&
    value.process_execution_enabled ===
      (value.user_terminal_enabled || value.command_runtime_enabled) &&
    value.shell_execution_enabled ===
      (value.user_terminal_enabled || value.command_runtime_enabled) &&
    (!value.docker_execution_enabled || (value.execution_permission_control_enabled &&
      value.operator_approval_enabled)) &&
    value.renderer_path_input_supported === false;
}

function validDebugRestartResult(value: unknown): value is DesktopDebugRestartResult {
  return hasExactKeys(value, ["arbitrary_arguments_accepted", "persistent_runtime_grant",
    "profile", "protocol_version", "restart_required", "status"]) &&
    value.protocol_version === desktopRiskRestartProtocol && value.profile === "debug" &&
    (value.status === "cancelled" || value.status === "restarting") &&
    value.restart_required === true && value.arbitrary_arguments_accepted === false &&
    value.persistent_runtime_grant === false;
}

function validTerminalSession(value: unknown,
  runID?: string): value is DesktopTerminalSession {
  if (!hasExactKeys(value, ["agent_input_default", "backend", "columns", "exit_code",
    "job_assigned_at_creation", "kill_on_job_close", "output_base_cursor",
    "output_next_cursor", "persistent", "process_local", "protocol_version",
    "raw_output_persisted", "rows", "run_id", "session_id", "state", "user_owned"])) {
    return false;
  }
  return value.protocol_version === desktopUserTerminalProtocol &&
    validWorkspaceID(value.session_id) && validWorkspaceID(value.run_id) &&
    (runID === undefined || value.run_id === runID) &&
    (value.state === "running" || value.state === "exited" || value.state === "failed") &&
    boundedText(value.backend, 1, 64) && validTerminalSize(value.columns, value.rows) &&
    safeCount(value.output_base_cursor) && safeCount(value.output_next_cursor) &&
    value.output_next_cursor >= value.output_base_cursor &&
    Number.isInteger(value.exit_code) && value.user_owned === true &&
    value.agent_input_default === false && value.job_assigned_at_creation === true &&
    value.kill_on_job_close === true && value.persistent === true &&
    value.process_local === true && value.raw_output_persisted === false;
}

function validTerminalOutput(value: unknown,
  sessionID: string): value is DesktopTerminalOutput {
  return hasExactKeys(value, ["base_cursor", "data_base64", "data_bytes", "dropped",
    "next_cursor", "protocol_version", "session_id", "state"]) &&
    value.protocol_version === desktopUserTerminalProtocol &&
    value.session_id === sessionID && safeCount(value.base_cursor) &&
    safeCount(value.next_cursor) && value.next_cursor >= value.base_cursor &&
    safeCount(value.data_bytes) && value.data_bytes <= 65536 &&
    validBase64Payload(value.data_base64, value.data_bytes) &&
    typeof value.dropped === "boolean" &&
    (value.state === "running" || value.state === "exited" || value.state === "failed");
}

function validBase64Payload(value: unknown, expectedBytes: number): boolean {
  if (typeof value !== "string" || value.length > 4 * Math.ceil(65536 / 3) ||
    value.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)) {
    return false;
  }
  try {
    return window.atob(value).length === expectedBytes;
  } catch {
    return false;
  }
}

function validTerminalWriteResult(value: unknown, sessionID: string,
  expectedBytes: number): value is DesktopTerminalWriteResult {
  return hasExactKeys(value, ["bytes_written", "protocol_version", "session_id"]) &&
    value.protocol_version === desktopUserTerminalProtocol &&
    value.session_id === sessionID && value.bytes_written === expectedBytes;
}

function validDebugTerminalAgentInputBinding(value: unknown, runID: string,
  terminalSessionID?: string): value is DesktopDebugTerminalAgentInputBinding {
  return hasExactKeys(value, ["binding_id", "expires_at", "issued_at", "process_local",
    "protocol_version", "raw_input_persisted", "run_id", "terminal_session_id",
    "token_exposed"]) &&
    value.protocol_version === desktopDebugTerminalAgentInputProtocol &&
    validWorkspaceID(value.binding_id) && value.run_id === runID &&
    validWorkspaceID(value.terminal_session_id) &&
    (terminalSessionID === undefined || value.terminal_session_id === terminalSessionID) &&
    typeof value.issued_at === "string" && Number.isFinite(Date.parse(value.issued_at)) &&
    typeof value.expires_at === "string" && Number.isFinite(Date.parse(value.expires_at)) &&
    Date.parse(value.expires_at) > Date.parse(value.issued_at) &&
    value.process_local === true && value.token_exposed === false &&
    value.raw_input_persisted === false;
}

function validTerminalSize(columns: unknown, rows: unknown): columns is number {
  return Number.isInteger(columns) && Number.isInteger(rows) &&
    (columns as number) >= 20 && (columns as number) <= 300 &&
    (rows as number) >= 5 && (rows as number) <= 120;
}

function validWorkspaceLauncherList(value: unknown,
  workspaceID: string): value is DesktopWorkspaceLauncherList {
  if (!hasExactKeys(value, ["agent_authority_granted", "arbitrary_arguments_accepted",
    "launchers", "protocol_version", "renderer_path_input_supported", "root_path_exposed",
    "workspace_id"]) || value.protocol_version !== desktopWorkspaceLauncherProtocol ||
    value.workspace_id !== workspaceID || value.root_path_exposed !== false ||
    value.renderer_path_input_supported !== false || value.arbitrary_arguments_accepted !== false ||
    value.agent_authority_granted !== false || !Array.isArray(value.launchers) ||
    value.launchers.length > 12) {
    return false;
  }
  const ids = new Set<string>();
  for (const launcher of value.launchers) {
    if (!hasExactKeys(launcher, ["id", "kind", "label"]) || !validLauncherID(launcher.id) ||
      !boundedText(launcher.label, 1, 80) ||
      (launcher.kind !== "folder" && launcher.kind !== "terminal" && launcher.kind !== "editor") ||
      ids.has(launcher.id)) {
      return false;
    }
    ids.add(launcher.id);
  }
  return true;
}

function validWorkspaceOpenResult(value: unknown,
  request: DesktopWorkspaceOpenRequest): value is DesktopWorkspaceOpenResult {
  if (!hasExactKeys(value, ["agent_authority_granted", "arbitrary_arguments_accepted",
    "command_executed", "external_process_started", "launcher_id", "operator_confirmed",
    "protocol_version", "root_path_exposed", "status", "workspace_id"]) ||
    value.protocol_version !== desktopWorkspaceOpenProtocol ||
    value.workspace_id !== request.workspace_id || value.launcher_id !== request.launcher_id ||
    value.root_path_exposed !== false || value.arbitrary_arguments_accepted !== false ||
    value.command_executed !== false || value.agent_authority_granted !== false) {
    return false;
  }
  if (value.status === "started") {
    return value.operator_confirmed === true && value.external_process_started === true;
  }
  return value.status === "cancelled" && value.operator_confirmed === false &&
    value.external_process_started === false;
}

function validWorkspaceImportResult(value: unknown): value is DesktopWorkspaceImportResult {
  if (!hasExactKeys(value, ["agent_authority_granted", "directory_content_modified",
    "protocol_version", "renderer_path_input_supported", "root_path_exposed", "status",
    "workspace"]) || value.protocol_version !== desktopWorkspaceImportProtocol ||
    value.root_path_exposed !== false || value.renderer_path_input_supported !== false ||
    value.directory_content_modified !== false || value.agent_authority_granted !== false) {
    return false;
  }
  if (value.status === "cancelled") {
    return value.workspace === null;
  }
  return value.status === "registered" && hasExactKeys(value.workspace,
    ["created_at", "id", "name"]) && validWorkspaceID(value.workspace.id) &&
    boundedText(value.workspace.name, 1, 128) &&
    typeof value.workspace.created_at === "string" &&
    Number.isFinite(Date.parse(value.workspace.created_at));
}

function validDialogResult(value: unknown): value is DesktopSkillDialogResult {
  if (!hasExactKeys(value, ["protocol_version", "selection", "status"]) ||
    value.protocol_version !== desktopSkillDialogProtocol ||
    (value.status !== "cancelled" && value.status !== "selected")) {
    return false;
  }
  if (value.status === "cancelled") {
    return value.selection === null;
  }
  return validSelection(value.selection);
}

function validSelection(value: unknown): value is DesktopSkillSelection {
  return hasExactKeys(value, ["expires_at", "handle", "protocol_version"]) &&
    value.protocol_version === desktopSkillSelectionProtocol &&
    typeof value.handle === "string" && /^[A-Za-z0-9_-]{43}$/.test(value.handle) &&
    typeof value.expires_at === "string" && Number.isFinite(Date.parse(value.expires_at));
}

function validPreview(value: unknown): value is DesktopSkillPreview {
  if (isRecord(value) && value.package_protocol === "plugin-installation.v2") {
    return hasExactKeys(value, ["protocol_version", "package_protocol", "format", "name", "version",
      "archive_sha256", "archive_bytes", "entry_count", "skill_count", "validated",
      "confirmation_handle", "confirmation_expires_at"]) &&
      value.protocol_version === desktopSkillPreviewProtocol &&
      ["agent-skills", "agent-plugins"].includes(String(value.format)) &&
      boundedText(value.name, 1, 256) && (value.version === "" || boundedText(value.version, 1, 256)) &&
      isSHA256(value.archive_sha256) && safeCount(value.archive_bytes) && safeCount(value.entry_count) &&
      safeCount(value.skill_count) && value.validated === true &&
      typeof value.confirmation_handle === "string" && /^[A-Za-z0-9_-]{43}$/.test(value.confirmation_handle) &&
      typeof value.confirmation_expires_at === "string" && Number.isFinite(Date.parse(value.confirmation_expires_at));
  }
  if (!hasExactKeys(value, [
    "archive_bytes", "archive_sha256", "confirmation_expires_at", "confirmation_handle",
    "content_bytes", "content_token_upper_bound",
    "declared_tool_count", "declared_tools", "entry_count", "executable_asset_count",
    "import_command_execution", "import_network_access", "import_provider_calls",
    "install_hook_count", "installation_authorized", "name", "package_fingerprint",
    "package_protocol", "profiles", "protocol_version", "risk_codes", "skill_protocol",
    "tool_capability_grant", "trust_class", "uncompressed_bytes", "validated", "version",
  ])) {
    return false;
  }
  return value.protocol_version === desktopSkillPreviewProtocol &&
    boundedText(value.package_protocol, 1, 64) && boundedText(value.skill_protocol, 1, 64) &&
    boundedText(value.name, 1, 128) && boundedText(value.version, 1, 64) &&
    boundedStringArray(value.profiles, 8, 64) && boundedStringArray(value.declared_tools, 16, 128) &&
    value.declared_tool_count === value.declared_tools.length &&
    safeCount(value.content_bytes) && safeCount(value.content_token_upper_bound) &&
    isSHA256(value.archive_sha256) && isSHA256(value.package_fingerprint) &&
    safeCount(value.archive_bytes) && safeCount(value.uncompressed_bytes) && safeCount(value.entry_count) &&
    boundedText(value.trust_class, 1, 64) && boundedStringArray(value.risk_codes, 16, 128) &&
    value.executable_asset_count === 0 && value.install_hook_count === 0 &&
    value.import_command_execution === false && value.import_network_access === false &&
    value.import_provider_calls === false && value.tool_capability_grant === false &&
    value.installation_authorized === false && value.validated === true &&
    typeof value.confirmation_handle === "string" &&
    /^[A-Za-z0-9_-]{43}$/.test(value.confirmation_handle) &&
    typeof value.confirmation_expires_at === "string" &&
    Number.isFinite(Date.parse(value.confirmation_expires_at));
}

function validInstallResult(value: unknown, preview: DesktopSkillPreview,
  surface: "code" | "cyber"): value is DesktopSkillInstallResult {
  if (isRecord(value) && value.protocol_version === "plugin-installation.v2") {
    try {
      const { installation } = parsePluginSkillInstall(value, surface, true);
      return installation.archive_sha256 === preview.archive_sha256 &&
        installation.manifest.name === preview.name && installation.manifest.version === preview.version &&
        (installation.source.kind !== "local_directory" || installation.source.uri === "");
    } catch { return false; }
  }
  return "package_fingerprint" in preview && hasExactKeys(value, ["archive_sha256", "context_injection_authorized",
    "import_command_execution", "import_network_access", "import_provider_calls", "name",
    "package_fingerprint", "protocol_version", "receipt", "recovered_pending", "replayed",
    "run_selection_authorized", "surface", "tool_capability_grant", "trust_class", "version"]) &&
    value.protocol_version === desktopSkillInstallProtocol && value.name === preview.name &&
    value.version === preview.version && value.surface === surface &&
    value.trust_class === "operator_installed_untrusted" &&
    value.archive_sha256 === preview.archive_sha256 &&
    value.package_fingerprint === preview.package_fingerprint &&
    typeof value.replayed === "boolean" && typeof value.recovered_pending === "boolean" &&
    value.import_command_execution === false && value.import_network_access === false &&
    value.import_provider_calls === false && value.tool_capability_grant === false &&
    value.run_selection_authorized === false && value.context_injection_authorized === false &&
    validInstallReceipt(value.receipt, value.replayed);
}

function validInstallReceipt(value: unknown, replayed: boolean): value is DesktopOperationReceipt {
  return hasExactKeys(value, ["cleanup_state", "durable", "kind", "outcome", "protocol_version",
    "recovery_action", "replayed", "retry_safe", "retry_strategy"]) &&
    value.protocol_version === "operation_receipt.v1" && value.kind === "skill_package_install" &&
    value.outcome === "installed" && value.durable === true && value.replayed === replayed &&
    value.retry_safe === true && value.retry_strategy === "same_operation_key" &&
    value.recovery_action === "none" && value.cleanup_state === "not_applicable";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasExactKeys(value: unknown, expected: string[]): value is Record<string, unknown> {
  if (!isRecord(value)) {
    return false;
  }
  const keys = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return keys.length === wanted.length && keys.every((key, index) => key === wanted[index]);
}

function boundedText(value: unknown, minimum: number, maximum: number): value is string {
  return typeof value === "string" && value.trim() === value &&
    value.length >= minimum && value.length <= maximum;
}

function validToken(value: unknown): value is string {
  return boundedText(value, 32, 512) && !/\s/u.test(value);
}

function validWorkspaceID(value: unknown): value is string {
  return boundedText(value, 1, 256) && !/[\s\u0000-\u001f\u007f]/u.test(value);
}

function validLauncherID(value: unknown): value is string {
  return typeof value === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(value);
}

function isSHA256(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

function safeCount(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function boundedStringArray(value: unknown, maximumItems: number, maximumLength: number): value is string[] {
  return Array.isArray(value) && value.length <= maximumItems &&
    value.every((item) => boundedText(item, 1, maximumLength));
}
