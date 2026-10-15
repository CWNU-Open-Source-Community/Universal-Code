import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { ThreadDetailView, WorkspaceView } from "../../api/types";
import { DesktopSkillPreviewDialog } from "../../components/desktop-skill-preview";
import { SkillPackageRegistry, type SkillPackageRegistrySource } from "../../components/skill-package-registry";
import { listDesktopSkillPackages, removeDesktopSkillPackage } from "../../lib/desktop-bridge";
import { SafeWebReadinessPanel } from "../../components/safe-web-readiness";
import { AboutSettings, ExtensionSettings, WebSkillInstall, persistDensity, readDensity,
  type Density } from "../../components/shared-settings-panels";
import { applyRunNavigationMode, readRunNavigationMode, type RunNavigationMode } from "../../lib/run-navigation";
import { v2QueryKeys } from "../query-keys";
import { useConnectionStore } from "../../state/connection";
import "./advanced-settings.css";

export function V2ExtensionSettings({ client, threadID, workspaces = [], onOpenTask }: {
  client: APIClient; threadID: string; workspaces?: WorkspaceView[]; onOpenTask?: (workspaceID?: string) => void;
}) {
  const [workspaceID, setWorkspaceID] = useState("");
  const thread = useQuery({
    queryKey: v2QueryKeys.thread(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal),
    enabled: Boolean(threadID),
  });
  const runID = (thread.data?.active_run ?? thread.data?.last_run)?.id;
  if (threadID && (thread.isPending || thread.isError || (!runID && !thread.data?.thread.workspace_id))) return <>
    <h1>扩展与代码智能</h1>
    {thread.isPending ? <p role="status">正在读取当前任务的扩展范围…</p> : <p role="alert">
      无法确定当前任务的扩展范围。<button className="v2-setting-link" onClick={() => void thread.refetch()}
        type="button">重试</button></p>}
  </>;
  return <div className="v2-shared-settings">
    <p className="v2-settings-lead">{threadID ? `当前任务：${thread.data?.thread.title}。代码智能状态按其工作区读取。`
      : "选择接入工作区以添加服务器，或查看下方已登记状态。"}每条记录标明实际范围。关闭会停用对应扩展或安装，并影响使用它的任务。</p>
    {!threadID && <label className="extension-scope-picker">接入工作区
      <select aria-label="接入工作区" value={workspaceID} onChange={(event) => setWorkspaceID(event.target.value)}>
        <option value="">全部已登记状态（选择工作区以接入）</option>
        {workspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
      </select>
      {onOpenTask && <button className="settings-action" onClick={() => onOpenTask(workspaceID || undefined)} type="button">打开新任务输入区</button>}
    </label>}
    <ExtensionSettings client={client} key={`${runID ?? "global"}/${workspaceID}`} onOpenTask={onOpenTask}
      selectedRunID={runID ?? ""} selectedWorkspaceID={threadID ? thread.data?.thread.workspace_id : workspaceID} />
  </div>;
}

export function V2SkillSettings({ client, desktop }: { client: APIClient; desktop: boolean }) {
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
  return <><h1>Skill 包</h1><p className="v2-settings-lead">
    将已有技能包加入本地技能库，再在任务中使用。加载与执行遵循该任务的权限和审批。
  </p><section className="v2-settings-section v2-shared-settings"><div className="v2-settings-card v2-skill-settings-card">
    {desktop ? <>
      <p>先预览 ZIP 包的结构、来源与文件，再确认安装到 Code 或 Cyber 工作面。</p>
      <button className="settings-action" onClick={() => setPreviewOpen(true)} type="button">预览 Skill 包</button>
      {!client.hasSkillInstallation && <p>当前连接可预览包结构。具备安装权限的连接可继续确认安装。</p>}
      <DesktopSkillPreviewDialog installationEnabled={client.hasSkillInstallation}
        onClose={() => setPreviewOpen(false)} open={previewOpen} />
    </> : client.hasSkillInstallation ? <>
      <p>网页端可上传 ZIP 包并登记到 Code。结构校验由服务端执行；本机文件选择预览仅在桌面端提供。</p>
      <WebSkillInstall client={client} />
    </> : <p role="status">连接具备 Skill 安装权限的服务后，可选择 ZIP 包加入技能库。</p>}
    <SkillPackageRegistry enabled={client.hasSkillInstallation} source={registry} />
  </div></section></>;
}

export function V2InspectorPreferences({ onOpenInspector }: {
  onOpenInspector: (returnFocus?: HTMLElement | null) => void;
}) {
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
  return <><h1>Inspector 偏好与诊断</h1><p className="v2-settings-lead">
    选择适合你的列表间距与运行记录导航，偏好会保存在本机。任务权限与执行状态在任务设置中管理。
  </p><section className="v2-settings-section v2-shared-settings"><div className="v2-settings-card v2-inspector-preferences">
    <div><strong>Inspector 内容间距</strong><div className="v2-setting-segmented" role="group" aria-label="Inspector 内容间距">
      {(["comfortable", "compact"] as const).map((value) => <button aria-pressed={density === value}
        key={value} onClick={() => changeDensity(value)} type="button">{value === "compact" ? "紧凑" : "舒展"}</button>)}
    </div></div>
    <div><strong>运行记录导航</strong><div className="v2-setting-segmented" role="group" aria-label="运行记录导航">
      {(["compact", "diagnostic"] as const).map((value) => <button aria-pressed={navigation === value}
        key={value} onClick={() => changeNavigation(value)} type="button">{value === "compact" ? "精简" : "完整诊断"}</button>)}
    </div></div>
    <button className="settings-action" onClick={(event) => onOpenInspector(event.currentTarget)} type="button">打开 Inspector</button>
  </div></section></>;
}

export function V2AboutSettings({ client, desktop }: { client: APIClient; desktop: boolean }) {
  const queryClient = useQueryClient();
  const disconnect = useConnectionStore((state) => state.disconnect);
  const health = useQuery({ queryKey: ["health"], queryFn: ({ signal }) => client.health(signal), retry: false });
  const [diagnosticsOpen, setDiagnosticsOpen] = useState(false);
  return <div className="v2-shared-settings">
    {health.isPending ? <><h1>关于</h1><p role="status">正在读取应用版本…</p></> : health.isError ? <>
      <h1>关于</h1><p role="alert">应用信息读取失败，版本未知。
        <button className="v2-setting-link" onClick={() => void health.refetch()} type="button">重试</button></p>
    </> : <AboutSettings desktop={desktop} health={health.data} />}
    <details className="v2-settings-diagnostics" onToggle={(event) => setDiagnosticsOpen(event.currentTarget.open)}>
      <summary>连接与网页诊断</summary>
      <p>这些结果来自当前连接的服务与运行环境。</p>
      {health.data && <p>服务状态：{health.data.status}</p>}
      {diagnosticsOpen && <SafeWebReadinessPanel client={client} />}
    </details>
    {!desktop && <section className="v2-settings-section"><h2>当前界面连接</h2>
      <p>断开后可重新填写连接信息。任务和已受理操作会继续在服务中运行；要停止任务，请使用任务中的停止操作。</p>
      <button className="settings-action" onClick={() => { queryClient.clear(); disconnect(); }} type="button">断开连接</button>
    </section>}
  </div>;
}
