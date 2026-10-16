import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RotateCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { confirmDesktopVerificationSettings, desktopErrorMessage, desktopVerificationSettingsAvailable,
  desktopVerificationSettingsProtocol, getDesktopVerificationSettings, restartDesktopWithVerificationSettings,
  type DesktopVerificationSettings, type DesktopVerificationSettingsRequest } from "../lib/desktop-bridge";
import { useV2PersistenceWarning, useV2RecoveryStore } from "../v2/recovery-storage";
import "./sandbox-environment-panel.css";

const settingsKey = ["desktop-verification-settings"] as const;

export function VerificationSettingsPanel({ client }: { client: Pick<APIClient, "baseURL"> }) {
  const available = client.baseURL === "/api/v1" && desktopVerificationSettingsAvailable();
  const queries = useQueryClient();
  const recovery = useV2RecoveryStore();
  const persistenceWarning = useV2PersistenceWarning();
  const [draft, setDraft] = useState<DesktopVerificationSettings | null>(null);
  const [notice, setNotice] = useState("");
  const settings = useQuery({ queryKey: settingsKey, queryFn: getDesktopVerificationSettings,
    enabled: available, retry: false, refetchOnWindowFocus: false });
  const save = useMutation({ mutationFn: (request: DesktopVerificationSettingsRequest) => confirmDesktopVerificationSettings(request),
    onSuccess: (result) => {
      queries.setQueryData(settingsKey, result.view);
      if (result.status === "saved") setDraft(null);
      setNotice(result.status === "cancelled" ? "已取消保存，启用偏好未更改。" :
        result.view.restart_required ? "验证设置已保存，重启应用后生效。" : "验证设置已保存，当前进程已使用这些设置。");
      void queries.invalidateQueries({ predicate: (query) => query.queryKey.includes("capability-readiness") });
    },
  });
  const restart = useMutation({ mutationFn: async () => {
    if (persistenceWarning) throw new Error("部分草稿或待确认请求尚未保存，请先处理保存提示再重启。");
    recovery?.assertReadable();
    return restartDesktopWithVerificationSettings();
  }, onSuccess: (result) => setNotice(result.status === "cancelled" ? "已取消重启，保存的设置仍保留。" : "正在重启应用…") });
  const value = settings.data;
  const selection = draft ?? value?.settings;
  const dirty = !!value && !!selection && (selection.ui_evidence_enabled !== value.settings.ui_evidence_enabled ||
    selection.batch_validation_enabled !== value.settings.batch_validation_enabled);
  const restarting = restart.isPending || restart.data?.status === "restarting";
  const busy = save.isPending || restarting;
  const reload = async () => {
    const result = await settings.refetch();
    if (!result.error) { setDraft(null); save.reset(); restart.reset(); setNotice("已重新读取保存的验证设置。"); }
  };

  return <section aria-label="验证功能设置" className="sandbox-environment-panel">
    <h2>验证功能</h2>
    <p>保存后记住下次启动的启用偏好。浏览器验证可运行页面检查；批量验证可运行项目中的 Go 和 npm 验证命令。具体执行继续遵循当前任务权限。</p>
    {!available ? <p>请在支持验证设置的桌面版本中打开“设置 → 执行环境”，连接本机服务后启用。</p> : <>
      {settings.isPending && <p role="status">正在读取验证设置…</p>}
      {settings.isError && <p role="alert">无法读取验证设置：{desktopErrorMessage(settings.error)}</p>}
      {value && selection && <>
        <fieldset className="sandbox-environment-form" disabled={busy || !value.can_save || save.isError}>
          <legend>下次启动使用的验证功能</legend>
          <div className="sandbox-environment-fields">
            <label className="sandbox-environment-toggle"><input type="checkbox" checked={selection.ui_evidence_enabled}
              onChange={(event) => { setDraft({ ...selection, ui_evidence_enabled: event.currentTarget.checked }); setNotice(""); }} />浏览器验证</label>
            <p>当前进程：{value.active_settings.ui_evidence_enabled ? "已启用" : "未启用"}。</p>
            <label className="sandbox-environment-toggle"><input type="checkbox" checked={selection.batch_validation_enabled}
              onChange={(event) => { setDraft({ ...selection, batch_validation_enabled: event.currentTarget.checked }); setNotice(""); }} />批量验证</label>
            <p>当前进程：{value.active_settings.batch_validation_enabled ? "已启用" : "未启用"}。</p>
          </div>
          <button type="button" className="command-button primary" disabled={!dirty || settings.isFetching}
            onClick={() => { setNotice(""); save.mutate({ protocol_version: desktopVerificationSettingsProtocol,
              expected_revision: value.revision, settings: { ...selection } }); }}>
            {save.isPending ? "等待原生确认…" : "保存验证设置"}</button>
        </fieldset>
        {value.restart_required && <div className="sandbox-environment-restart">
          <p>保存设置与当前进程不同。重启应用后应用启用或关闭的选择；任务权限和执行授权由新会话重新核对。</p>
          <button type="button" disabled={busy || dirty || settings.isFetching || save.isError || !value.can_restart || !!persistenceWarning}
            onClick={() => { setNotice(""); restart.mutate(); }}><RotateCw aria-hidden="true" size={14} />
            {restarting ? "正在重启应用…" : "重启以应用验证设置"}</button>
          {persistenceWarning && <p role="alert">{persistenceWarning} 请先处理保存问题，再重启应用。</p>}
        </div>}
      </>}
      {save.isError && <p role="alert">保存结果待核对：{desktopErrorMessage(save.error)} 请重新读取设置后继续。</p>}
      {restart.isError && <p role="alert">重启失败，已保存的设置仍保留，可重试：{desktopErrorMessage(restart.error)}</p>}
      {notice && <p role="status">{notice}</p>}
      <button type="button" disabled={busy || settings.isFetching} onClick={() => void reload()}>重新读取验证设置</button>
    </>}
  </section>;
}
