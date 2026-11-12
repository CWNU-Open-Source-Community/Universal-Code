import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { ThreadDetailView, WorkspaceView } from "../../api/types";
import { DesktopSkillPreviewDialog } from "../../components/desktop-skill-preview";
import { SkillPackageRegistry, type SkillPackageRegistrySource } from "../../components/skill-package-registry";
import { SkillCatalogTrust } from "../../components/skill-catalog-trust";
import { listDesktopSkillPackages, removeDesktopSkillPackage } from "../../lib/desktop-bridge";
import { SafeWebReadinessPanel } from "../../components/safe-web-readiness";
import { AboutSettings, ExtensionSettings, WebSkillInstall, persistDensity, readDensity,
  type Density } from "../../components/shared-settings-panels";
import { applyRunNavigationMode, readRunNavigationMode, type RunNavigationMode } from "../../lib/run-navigation";
import { v2QueryKeys } from "../query-keys";
import { useConnectionStore } from "../../state/connection";
import { useLocale } from "../../lib/locale";
import "./advanced-settings.css";

export function V2ExtensionSettings({ client, threadID, workspaces = [], onOpenTask }: {
  client: APIClient; threadID: string; workspaces?: WorkspaceView[]; onOpenTask?: (workspaceID?: string) => void;
}) {
  const { t } = useLocale();
  const [workspaceID, setWorkspaceID] = useState("");
  const thread = useQuery({
    queryKey: v2QueryKeys.thread(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal),
    enabled: Boolean(threadID),
  });
  const runID = (thread.data?.active_run ?? thread.data?.last_run)?.id;
  if (threadID && (thread.isPending || thread.isError || (!runID && !thread.data?.thread.workspace_id))) return <>
    <h1>{t("扩展与代码智能", "Extensions & code intelligence")}</h1>
    {thread.isPending ? <p role="status">{t("正在读取当前任务的扩展范围…", "Loading the extension scope for the current task…")}</p> : <p role="alert">
      {t("无法确定当前任务的扩展范围。", "Unable to determine the extension scope for the current task.")}<button className="v2-setting-link" onClick={() => void thread.refetch()}
        type="button">{t("重试", "Retry")}</button></p>}
  </>;
  return <div className="v2-shared-settings">
    {!threadID && <label className="extension-scope-picker">{t("接入工作区", "Workspace to connect")}
      <select aria-label={t("接入工作区", "Workspace to connect")} value={workspaceID} onChange={(event) => setWorkspaceID(event.target.value)}>
        <option value="">{t("全部已登记状态（选择工作区以接入）", "All registered entries (select a workspace to connect)")}</option>
        {workspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
      </select>
      {onOpenTask && <button className="settings-action" onClick={() => onOpenTask(workspaceID || undefined)} type="button">{t("打开新任务输入区", "Open new task composer")}</button>}
    </label>}
    <ExtensionSettings client={client} key={`${runID ?? "global"}/${workspaceID}`} onOpenTask={onOpenTask}
      selectedRunID={runID ?? ""} selectedWorkspaceID={threadID ? thread.data?.thread.workspace_id : workspaceID} />
  </div>;
}

export function V2SkillSettings({ client, desktop }: { client: APIClient; desktop: boolean }) {
  const { t } = useLocale();
  const [previewOpen, setPreviewOpen] = useState(false);
  const registry: SkillPackageRegistrySource = desktop ? {
    load: (includeRemoved, signal) => listDesktopSkillPackages({ includeRemoved })
      .then((value) => { if (signal?.aborted) throw new Error("Desktop Skill registry read was cancelled"); return value; }),
    remove: (skill, operationKey) => removeDesktopSkillPackage(skill, operationKey),
  } : {
    load: (includeRemoved, signal) => client.listSkillPackages({ includeRemoved }, signal),
    remove: (skill, operationKey) => client.removeSkillPackage({
      protocol_version: "skill_package_removal_request.v1", skill, confirm_remove: true,
    }, operationKey),
  };
  return <><h1>{t("Skill 包", "Skill packages")}</h1><section className="v2-settings-section v2-shared-settings"><div className="v2-settings-card v2-skill-settings-card">
    {desktop ? <>
      <p>{t("先预览 ZIP 包的结构、来源与文件，再确认安装到 Code 或 Cyber 工作面。", "Preview the ZIP package's structure, source, and files before confirming installation to Code or Cyber.")}</p>
      <button className="settings-action" onClick={() => setPreviewOpen(true)} type="button">{t("预览 Skill 包", "Preview skill package")}</button>
      {!client.hasSkillInstallation && <p>{t("当前连接可预览包结构。具备安装权限的连接可继续确认安装。", "This connection can preview package structure. Installation requires a connection with installation permission.")}</p>}
    </> : client.hasSkillInstallation ? <>
      <p>{t("网页端可上传 ZIP 包或原生 SKILL.md 目录并登记到 Code。结构校验和内容解析由服务端执行。", "Upload a ZIP package or a SKILL.md directory to register it with Code. The server validates the structure and parses the content.")}</p>
      <WebSkillInstall client={client} />
    </> : <p role="status">{t("连接具备 Skill 安装权限的服务后，可选择 ZIP 包加入技能库。", "Connect with skill installation permission to add a ZIP package to the library.")}</p>}
    <SkillPackageRegistry enabled={client.hasSkillInstallation} source={registry} />
    <SkillCatalogTrust client={client} />
  </div></section>
    {desktop && <DesktopSkillPreviewDialog installationEnabled={client.hasSkillInstallation}
      onClose={() => setPreviewOpen(false)} open={previewOpen} />}
  </>;
}

export function V2RecordPreferences({ onOpenHistory }: {
  onOpenHistory: (returnFocus?: HTMLElement | null) => void;
}) {
  const { t } = useLocale();
  const [density, setDensity] = useState(readDensity);
  const [navigation, setNavigation] = useState(readRunNavigationMode);
  const changeDensity = (value: Density) => {
    setDensity(value);
    document.documentElement.dataset.prayuDensity = value;
    persistDensity(value);
  };
  const changeNavigation = (value: RunNavigationMode) => {
    setNavigation(value);
    applyRunNavigationMode(value);
  };
  return <><h1>{t("记录显示", "Record preferences")}</h1><section className="v2-settings-section v2-shared-settings"><div className="v2-settings-card v2-record-preferences">
    <div><strong>{t("记录间距", "Record spacing")}</strong><div className="v2-setting-segmented" role="group" aria-label={t("记录间距", "Record spacing")}>
      {(["comfortable", "compact"] as const).map((value) => <button aria-pressed={density === value}
        key={value} onClick={() => changeDensity(value)} type="button">{value === "compact" ? t("紧凑", "Compact") : t("舒展", "Comfortable")}</button>)}
    </div></div>
    <div><strong>{t("运行记录导航", "Run navigation")}</strong><div className="v2-setting-segmented" role="group" aria-label={t("运行记录导航", "Run navigation")}>
      {(["compact", "diagnostic"] as const).map((value) => <button aria-pressed={navigation === value}
        key={value} onClick={() => changeNavigation(value)} type="button">{value === "compact" ? t("精简", "Compact") : t("完整诊断", "Full diagnostics")}</button>)}
    </div></div>
    <button className="settings-action" onClick={(event) => onOpenHistory(event.currentTarget)} type="button">{t("打开历史记录", "Open history")}</button>
  </div></section></>;
}

export function V2AboutSettings({ client, desktop }: { client: APIClient; desktop: boolean }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const disconnect = useConnectionStore((state) => state.disconnect);
  const health = useQuery({ queryKey: ["health"], queryFn: ({ signal }) => client.health(signal), retry: false });
  const [diagnosticsOpen, setDiagnosticsOpen] = useState(false);
  return <div className="v2-shared-settings">
    {health.isPending ? <><h1>{t("关于", "About")}</h1><p role="status">{t("正在读取应用版本…", "Loading app version…")}</p></> : health.isError ? <>
      <h1>{t("关于", "About")}</h1><p role="alert">{t("应用信息读取失败，版本未知。", "Unable to load app information. Version unknown.")}
        <button className="v2-setting-link" onClick={() => void health.refetch()} type="button">{t("重试", "Retry")}</button></p>
    </> : <AboutSettings desktop={desktop} health={health.data} />}
    <details className="v2-settings-diagnostics" onToggle={(event) => setDiagnosticsOpen(event.currentTarget.open)}>
      <summary>{t("连接与网页诊断", "Connection & web diagnostics")}</summary>
      <p>{t("这些结果来自当前连接的服务与运行环境。", "These results come from the connected service and its runtime environment.")}</p>
      {health.data && <p>{t("服务状态：", "Service status: ")}{health.data.status}</p>}
      {diagnosticsOpen && <SafeWebReadinessPanel client={client} />}
    </details>
    {!desktop && <section className="v2-settings-section"><h2>{t("当前界面连接", "Current app connection")}</h2>
      <p>{t("断开后可重新填写连接信息。任务和已受理操作会继续在服务中运行；要停止任务，请使用任务中的停止操作。", "After disconnecting, you can enter new connection details. Tasks and accepted operations continue on the service. Use the task's stop action to stop a task.")}</p>
      <button className="settings-action" onClick={() => { queryClient.clear(); disconnect(); }} type="button">{t("断开连接", "Disconnect")}</button>
    </section>}
  </div>;
}
