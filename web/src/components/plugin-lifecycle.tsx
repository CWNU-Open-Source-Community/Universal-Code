import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { HookDiagnosticsView, PluginHistoryView } from "../api/types";
import { useLocale } from "../lib/locale";
import "./plugin-lifecycle.css";

export function PluginLifecycleControls({ client, installationID }: { client: APIClient; installationID: string }) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const history = useQuery({ queryKey: ["plugin-history", installationID],
    queryFn: ({ signal }) => client.pluginHistory(installationID, signal), enabled: open, refetchOnWindowFocus: false });
  return <details className="plugin-lifecycle extension-review" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{t("版本与发布者", "Versions and publisher")}</summary>
    {open && <>
      <p>{t("查看本机保留的版本。选择版本与能力，核对后切换。", "Inspect retained local versions. Choose a version and its capabilities, then review and switch.")}</p>
      {history.isLoading && <p role="status">{t("正在读取版本记录…", "Reading version records…")}</p>}
      {history.error && <p className="inline-warning" role="alert">{t("版本记录读取失败。选择“刷新版本记录”继续核对。", "Version records could not be read. Select Refresh version records to continue.")}</p>}
      <button className="settings-action" disabled={history.isFetching || busy} onClick={() => void history.refetch()} type="button">{t("刷新版本记录", "Refresh version records")}</button>
      {history.data && <PluginVersionActions client={client} history={history.data} fetching={history.isFetching}
        refresh={() => history.refetch({ throwOnError: true })} setBusy={setBusy} key={`${history.data.installations.map((item) => `${item.id}/${item.generation}`).join(":")}/${history.data.publisher?.generation ?? 0}`} />}
    </>}
  </details>;
}

function PluginVersionActions({ client, history, fetching, refresh, setBusy }: {
  client: APIClient; history: PluginHistoryView; fetching: boolean; refresh: () => Promise<unknown>; setBusy: (busy: boolean) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [targetID, setTargetID] = useState("");
  const [capabilities, setCapabilities] = useState<string[]>([]);
  const [confirmed, setConfirmed] = useState(false);
  const [revokeConfirmed, setRevokeConfirmed] = useState(false);
  const [trustConfirmed, setTrustConfirmed] = useState(false);
  const current = history.installations.find((item) => item.id === history.installation_id)!;
  const publisherFingerprint = current.publisher_fingerprint ?? "";
  const targets = history.installations.filter((item) => item.id !== current.id && item.state !== "enabled" && item.state !== "revoked");
  const target = targets.find((item) => item.id === targetID);
  const invalidate = async () => {
    await Promise.all([queryClient.invalidateQueries({ queryKey: ["extensions"] }),
      queryClient.invalidateQueries({ queryKey: ["plugin-history"] }), queryClient.invalidateQueries({ queryKey: ["hook-diagnostics"] })]);
  };
  const rollback = useMutation({ mutationFn: () => {
    if (!target) throw new Error(t("请选择保留版本。", "Choose a retained version."));
    return client.rollbackPluginInstallation(current.id, { version: "plugin-lifecycle.v1", target_installation_id: target.id,
      expected_current_fingerprint: current.package_fingerprint, expected_current_generation: current.generation,
      expected_target_fingerprint: target.package_fingerprint, expected_target_generation: target.generation,
      capabilities, confirm_untrusted: confirmed });
  }, onMutate: () => setBusy(true), onSuccess: invalidate, onSettled: () => setBusy(false) });
  const revoke = useMutation({ mutationFn: () => client.revokePluginPublisher(current.id, {
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: history.publisher!.fingerprint,
    expected_publisher_generation: history.publisher!.generation, confirm: revokeConfirmed,
  }), onMutate: () => setBusy(true), onSuccess: invalidate, onSettled: () => setBusy(false) });
  const trust = useMutation({ mutationFn: () => client.trustPluginPublisher(current.id, {
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: publisherFingerprint,
    expected_publisher_generation: history.publisher?.generation ?? 0, confirm: trustConfirmed,
  }), onMutate: () => setBusy(true), onSuccess: invalidate, onSettled: () => setBusy(false) });
  const pending = rollback.isPending || revoke.isPending || trust.isPending;
  const submitted = !rollback.isIdle || !revoke.isIdle || !trust.isIdle;
  const readAgain = async () => { try { await refresh(); rollback.reset(); revoke.reset(); trust.reset(); setConfirmed(false); setRevokeConfirmed(false); setTrustConfirmed(false); } catch { /* Retain the original write result while the read remains unresolved. */ } };
  const version = (item: PluginHistoryView["installations"][number]) => item.manifest.version ? `v${item.manifest.version}` :
    t(`保留快照 ${item.snapshot?.revision.slice(0, 12) ?? item.archive_sha256.slice(0, 12)}`, `Retained snapshot ${item.snapshot?.revision.slice(0, 12) ?? item.archive_sha256.slice(0, 12)}`);
  const state = (value: string) => ({ enabled: t("当前启用", "Enabled now"), rolled_back: t("已回退", "Rolled back"),
    revoked: t("已撤销", "Revoked"), staged: t("待审查", "Awaiting review"), approved: t("已审查", "Reviewed"),
    disabled: t("已停用", "Disabled"), quarantined: t("待重新审查", "Review needed") }[value] ?? value);
  return <div className="plugin-version-actions">
    <ol className="plugin-version-history" aria-label={t("安装版本记录", "Installed version history")}>
      {history.installations.map((item) => <li key={item.id}>
        <div><strong>{version(item)}</strong><span>{state(item.state)}</span></div>
        <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time>
        <details><summary>{t("版本详细记录", "Version details")}</summary>
          <dl><dt>{t("安装记录", "Installation")}</dt><dd><code>{item.id}</code></dd>
            <dt>{t("包指纹", "Package fingerprint")}</dt><dd><code>{item.package_fingerprint}</code></dd>
            <dt>{t("启用能力", "Enabled capabilities")}</dt><dd>{item.enabled_capabilities.join(", ") || t("尚未启用", "Awaiting enablement")}</dd></dl>
        </details>
      </li>)}
    </ol>
    {history.total_versions > history.installations.length && <p>{t(`本机保留 ${history.total_versions} 个版本，当前显示所选版本与最近记录，最多 1000 项。`, `This device retains ${history.total_versions} versions. Showing the selected version and recent records, up to 1000 entries.`)}</p>}
    {current.state === "enabled" && targets.length > 0 && <fieldset disabled={!client.hasExtensionControl || pending || fetching || submitted}>
      <legend>{t("回退版本", "Roll back version")}</legend>
      <label>{t("回退到", "Roll back to")}<select value={targetID} onChange={(event) => { setTargetID(event.target.value); setCapabilities([]); setConfirmed(false); }}>
        <option value="">{t("选择保留版本", "Choose a retained version")}</option>
        {targets.map((item) => <option key={item.id} value={item.id}>{version(item)} · {state(item.state)}</option>)}
      </select></label>
      {target && <>
        <p>{t(`切换后，${version(current)} 的能力停止加载，${version(target)} 加载下方所选能力。`,
          `After switching, ${version(current)} stops loading capabilities and ${version(target)} loads the selected capabilities below.`)}</p>
        <div className="extension-checks" role="group" aria-label={t("回退版本的能力", "Rollback capabilities")}>
          {target.manifest.capabilities.map((capability) => <label key={capability}><input checked={capabilities.includes(capability)} type="checkbox"
            onChange={(event) => { setConfirmed(false); setCapabilities((items) => event.target.checked ? [...items, capability] : items.filter((item) => item !== capability)); }} />{capability}</label>)}
        </div>
        <label className="plugin-confirmation"><input checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} type="checkbox" />
          {t("我已核对两个版本和所选能力，允许加载所选版本", "I reviewed both versions and capabilities and allow the selected version to load")}</label>
        <button className="settings-action" disabled={!confirmed || capabilities.length === 0} onClick={() => rollback.mutate()} type="button">{t("回退并启用所选能力", "Roll back and enable capabilities")}</button>
      </>}
    </fieldset>}
    {current.state === "enabled" && targets.length === 0 && <p>{t("当前版本已启用。导入另一个版本后，可在这里切换。", "This version is enabled. Import another version to switch here.")}</p>}
    {!client.hasExtensionControl && <p>{t("当前连接可查看版本记录。连接具备控制权限的服务后可切换版本。", "This connection can read version records. Connect with control access to switch versions.")}</p>}
    <section className="plugin-publisher" aria-label={t("发布者信任", "Publisher trust")}>
      <h3>{t("发布者信任", "Publisher trust")}</h3>
      <p>{history.publisher ? `${history.publisher.publisher} · ${history.publisher.state === "trusted" ? t("已信任", "Trusted") : t("已撤销信任", "Trust revoked")}` :
        current.signature_valid ? t("签名有效。发布者尚未建立信任记录，可逐包核对并审查。", "Signature valid. This publisher has no trust record; review each package individually.") :
          t("此包通过逐包审查管理加载权限。", "Loading authority is managed through individual package review.")}</p>
      {history.publisher?.state === "trusted" && <>
        <p>{t(`撤销 ${history.publisher.publisher} 的信任会同时撤销本机该发布者的安装，跨包生效。当前有 ${history.total_publisher_installations} 个受影响安装。`,
          `Revoking trust in ${history.publisher.publisher} revokes this publisher's local installations across packages. Currently ${history.total_publisher_installations} installations are affected.`)}</p>
        <details><summary>{t("查看受影响安装与发布者指纹", "Inspect affected installations and publisher fingerprint")}</summary>
          <p><code>{history.publisher.fingerprint}</code></p>
          <ul>{history.publisher_installation_ids.map((id) => <li key={id}>{history.installations.find((item) => item.id === id)?.manifest.name ?? id}<small><code>{id}</code></small></li>)}</ul>
          {history.total_publisher_installations > history.publisher_installation_ids.length && <p>{t("当前显示最近 1000 个安装，撤销影响该发布者的全部安装。", "Showing the newest 1000 installations. Revocation affects all this publisher's installations.")}</p>}
        </details>
        <label className="plugin-confirmation"><input checked={revokeConfirmed} disabled={!client.hasExtensionControl || pending || fetching || submitted} onChange={(event) => setRevokeConfirmed(event.target.checked)} type="checkbox" />
          {t("我已核对发布者和影响范围，撤销其信任与安装权限", "I reviewed the publisher and scope and revoke its trust and installation authority")}</label>
        <button className="settings-action danger" disabled={!client.hasExtensionControl || !revokeConfirmed || pending || fetching || submitted} onClick={() => revoke.mutate()} type="button">{t("撤销发布者信任", "Revoke publisher trust")}</button>
      </>}
      {current.signature_valid && publisherFingerprint !== "" &&
        (!history.publisher || history.publisher.state === "revoked") && <>
        <p>{!history.publisher ? t("该发布者尚无信任记录。建立信任只表明你认可这个签名发布者，逐包的能力批准与启用仍需单独审查。",
          "This publisher has no trust record. Trusting it only records the signing publisher you accept; per-package capability approval and enablement remain separate reviews.") :
          t("信任已被撤销，该发布者的包无法通过审查。重新信任需要绑定当前指纹与代次，不会恢复此前被撤销的安装。",
            "Trust is revoked, so this publisher's packages cannot pass review. Re-trusting binds the current fingerprint and generation and does not restore previously revoked installations.")}</p>
        <p><code>{current.publisher_fingerprint}</code></p>
        <label className="plugin-confirmation"><input checked={trustConfirmed} disabled={!client.hasExtensionControl || pending || fetching || submitted} onChange={(event) => setTrustConfirmed(event.target.checked)} type="checkbox" />
          {t("我已核对发布者公钥指纹，确认建立信任记录", "I reviewed the publisher public-key fingerprint and grant the trust record")}</label>
        <button className="settings-action" disabled={!client.hasExtensionControl || !trustConfirmed || pending || fetching || submitted} onClick={() => trust.mutate()} type="button">{t("信任该发布者", "Trust this publisher")}</button>
      </>}
    </section>
    {pending && <p role="status">{t("正在更新插件状态…", "Updating Plugin state…")}</p>}
    {rollback.isSuccess && <p role="status">{t("回退已完成，所选版本已启用。刷新版本记录查看当前状态。", "Rollback completed and the selected version is enabled. Refresh version records to inspect current state.")}</p>}
    {revoke.isSuccess && <p role="status">{t("发布者信任已撤销。刷新版本记录查看安装状态。", "Publisher trust revoked. Refresh version records to inspect installation state.")}</p>}
    {(rollback.error || revoke.error) && <>
      <p className="inline-warning" role="alert">{t("操作结果需要核对。选择“核对最新版本状态”读取当前记录，再决定下一步。", "Check the operation result. Select Check latest version state to read current records before continuing.")}</p>
      <details><summary>{t("操作详细记录", "Operation details")}</summary><p>{String(rollback.error ?? revoke.error)}</p></details>
      <button className="settings-action" disabled={pending || fetching} onClick={() => void readAgain()} type="button">{t("核对最新版本状态", "Check latest version state")}</button>
    </>}
  </div>;
}

export function HookDiagnostics({ client, runID, workspaceID }: { client: APIClient; runID: string; workspaceID: string }) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const diagnostics = useQuery({ queryKey: ["hook-diagnostics", runID, workspaceID],
    queryFn: ({ signal }) => client.hookDiagnostics(runID, workspaceID, signal), enabled: open });
  return <details className="extension-onboarding hook-diagnostics" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{t("Hooks 声明与触发记录", "Hook declarations and observations")}</summary>
    {open && <>
      <p>{t("查看本机规则的触发条件与加载状态，以及当前范围最近保留的 200 条触发记录。刷新读取记录。", "Inspect local rule conditions and loading state, plus the newest 200 retained observations in this scope. Refresh reads records.")}</p>
      <p>{runID ? t("记录范围：当前任务执行", "Observation scope: current task execution") : workspaceID ? t("记录范围：当前项目", "Observation scope: current project") : t("记录范围：本机全部项目", "Observation scope: all local projects")}</p>
      <button className="settings-action" disabled={diagnostics.isFetching} onClick={() => void diagnostics.refetch()} type="button">{t("刷新 Hooks 记录", "Refresh Hook records")}</button>
      {diagnostics.isLoading && <p role="status">{t("正在读取 Hooks 记录…", "Reading Hook records…")}</p>}
      {diagnostics.error && <p className="inline-warning" role="alert">{t("Hooks 记录读取失败。选择“刷新 Hooks 记录”重新读取。", "Hook records could not be read. Select Refresh Hook records to read them again.")}</p>}
      {diagnostics.data && <HookRecords value={diagnostics.data} />}
    </>}
  </details>;
}

function HookRecords({ value }: { value: HookDiagnosticsView }) {
  const { t } = useLocale();
  const events: Record<string, string> = { pre_tool: t("工具执行前", "Before tool"), post_tool: t("工具执行后", "After tool"), run_started: t("执行开始", "Run started"),
    run_completed: t("执行完成", "Run completed"), session_opened: t("会话打开", "Session opened"), session_closed: t("会话关闭", "Session closed"),
    compaction: t("上下文整理", "Compaction"), subagent: t("子任务边界", "Subagent boundary"), checkpoint: t("检查点", "Checkpoint") };
  const actions: Record<string, string> = { deny: t("拒绝操作", "Reject operation"), annotate: t("附加说明", "Annotate"), narrow: t("移除所列参数", "Remove listed fields"), record: t("记录触发", "Record observation") };
  return <div className="hook-records">
    <h3>{t("声明的规则", "Declared rules")}</h3>
    {value.declarations.length === 0 && <p>{t("当前安装包尚无 Hooks 声明。导入含 Hooks 的 Plugin 包后，在这里核对规则。", "Installed packages have no Hook declarations yet. Import a Plugin with Hooks to inspect its rules here.")}</p>}
    <ul>{value.declarations.map((item) => <li key={`${item.installation_id}/${item.hook_id}`}>
      <div><strong>{item.plugin_id} / {item.hook_id}</strong><span>{item.active ? t("已加载", "Loaded") : item.installation_state === "revoked" ? t("安装已撤销", "Installation revoked") : item.installation_state === "rolled_back" ? t("此版本已停用", "Version retired") : t("等待启用 Hooks 能力", "Awaiting Hook enablement")}</span></div>
      <p>{events[item.event]} · {actions[item.action]}</p>
      <p>{t("规则范围：本机任务", "Rule scope: local tasks")}{item.tool_names.length ? ` · ${item.tool_names.join(", ")}` : item.event === "pre_tool" || item.event === "post_tool" ? ` · ${t("全部工具", "All tools")}` : ""}</p>
      {item.remove_fields.length > 0 && <p>{t("移除参数", "Removed fields")}: {item.remove_fields.join(", ")}</p>}
      <details><summary>{t("规则详细记录", "Rule details")}</summary><p>{t("失败处理", "Failure policy")}: {item.failure_policy === "deny" ? t("拒绝当前操作", "Reject the operation") : t("继续当前操作", "Continue the operation")} · {item.timeout_ms} ms</p>
        <p>{t("安装状态", "Installation state")}: {item.installation_state}</p><p><code>{item.package_fingerprint}</code></p></details>
    </li>)}</ul>
    {value.omitted_declarations > 0 && <p>{t(`另有 ${value.omitted_declarations} 条声明，当前显示前 1000 条。`, `${value.omitted_declarations} further declarations; showing the first 1000.`)}</p>}
    <h3>{t("实际触发记录", "Observed invocations")}</h3>
    {value.observations.length === 0 && <p>{t("当前范围尚无触发记录。启用规则并在任务中触发对应事件后，刷新查看实际结果。", "This scope has no observations yet. Enable a rule, trigger its event in a task, then refresh to inspect the result.")}</p>}
    <ol>{value.observations.map((item) => <li key={item.id}>
      <div><strong>{item.plugin_id} / {item.hook_id}</strong><span>{item.decision === "rejected" ? t("已拒绝操作", "Operation rejected") : item.decision === "continued" ? t("已继续操作", "Operation continued") : t("历史记录缺少决定", "Decision absent from historical record")}</span></div>
      <p>{events[item.event]}{item.tool_name ? ` · ${item.tool_name}` : ""} · <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time></p>
      <details><summary>{t("触发详细记录", "Observation details")}</summary>
        <p>{t("记录结果", "Recorded outcome")}: {item.outcome} {item.action ? ` · ${actions[item.action]}` : ""}</p>
        <p>{t("项目", "Project")}: {item.workspace_id || "—"} · {t("执行", "Run")}: {item.run_id || "—"}</p>
        <p><code>{item.package_fingerprint || t("历史记录缺少包指纹", "Package fingerprint absent from historical record")}</code></p>
      </details>
    </li>)}</ol>
  </div>;
}
