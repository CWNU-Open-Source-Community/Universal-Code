import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RotateCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { confirmDesktopVerificationSettings, desktopErrorMessage, desktopVerificationSettingsAvailable,
  desktopVerificationSettingsProtocol, getDesktopVerificationSettings, restartDesktopWithVerificationSettings,
  type DesktopVerificationSettings, type DesktopVerificationSettingsRequest } from "../lib/desktop-bridge";
import { useLocale } from "../lib/locale";
import { useV2PersistenceWarning, useV2RecoveryStore } from "../v2/recovery-storage";
import "./sandbox-environment-panel.css";

const settingsKey = ["desktop-verification-settings"] as const;

export function VerificationSettingsPanel({ client }: { client: Pick<APIClient, "baseURL"> }) {
  const { t } = useLocale();
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
      setNotice(result.status === "cancelled" ? t("已取消保存，启用偏好未更改。", "Save cancelled.") :
        result.view.restart_required ? t("验证设置已保存，重启应用后生效。", "Verification settings saved. Restart to apply.") : t("验证设置已保存。", "Verification settings saved."));
      void queries.invalidateQueries({ predicate: (query) => query.queryKey.includes("capability-readiness") });
    },
  });
  const restart = useMutation({ mutationFn: async () => {
    if (persistenceWarning) throw new Error(t("部分草稿或待确认请求尚未保存，请先处理保存提示再重启。", "Save pending drafts and requests before restarting."));
    recovery?.assertReadable();
    return restartDesktopWithVerificationSettings();
  }, onSuccess: (result) => setNotice(result.status === "cancelled" ? t("已取消重启，保存的设置仍保留。", "Restart cancelled. Your settings are saved.") : t("正在重启应用…", "Restarting…")) });
  const value = settings.data;
  const selection = draft ?? value?.settings;
  const dirty = !!value && !!selection && (selection.ui_evidence_enabled !== value.settings.ui_evidence_enabled ||
    selection.batch_validation_enabled !== value.settings.batch_validation_enabled);
  const restarting = restart.isPending || restart.data?.status === "restarting";
  const busy = save.isPending || restarting;
  const reload = async () => {
    const result = await settings.refetch();
    if (!result.error) { setDraft(null); save.reset(); restart.reset(); setNotice(t("已重新读取保存的验证设置。", "Verification settings reloaded.")); }
  };

  return <section aria-label={t("验证功能设置", "Verification settings")} className="sandbox-environment-panel">
    <h2>{t("验证功能", "Verification")}</h2>
    {!available ? <p>{t("请在支持验证设置的桌面版本中连接本机服务。", "Connect to a local service in a supported desktop app.")}</p> : <>
      {settings.isPending && <p role="status">{t("正在读取验证设置…", "Loading verification settings…")}</p>}
      {settings.isError && <p role="alert">{t("无法读取验证设置：", "Couldn’t load verification settings. ")}{desktopErrorMessage(settings.error)}</p>}
      {value && selection && <>
        <fieldset className="sandbox-environment-form" disabled={busy || !value.can_save || save.isError}>
          <legend>{t("下次启动使用的验证功能", "Enable on next launch")}</legend>
          <div className="sandbox-environment-fields">
            <label className="sandbox-environment-toggle"><input type="checkbox" checked={selection.ui_evidence_enabled}
              onChange={(event) => { setDraft({ ...selection, ui_evidence_enabled: event.currentTarget.checked }); setNotice(""); }} />{t("浏览器验证", "Browser verification")}</label>
            {selection.ui_evidence_enabled !== value.active_settings.ui_evidence_enabled && <p>{value.active_settings.ui_evidence_enabled
              ? t("当前进程：已启用", "Currently enabled") : t("当前进程：未启用", "Currently disabled")}</p>}
            <label className="sandbox-environment-toggle"><input type="checkbox" checked={selection.batch_validation_enabled}
              onChange={(event) => { setDraft({ ...selection, batch_validation_enabled: event.currentTarget.checked }); setNotice(""); }} />{t("批量验证（Go / npm）", "Batch verification (Go / npm)")}</label>
            {selection.batch_validation_enabled !== value.active_settings.batch_validation_enabled && <p>{value.active_settings.batch_validation_enabled
              ? t("当前进程：已启用", "Currently enabled") : t("当前进程：未启用", "Currently disabled")}</p>}
          </div>
          <button type="button" className="command-button primary" disabled={!dirty || settings.isFetching}
            onClick={() => { setNotice(""); save.mutate({ protocol_version: desktopVerificationSettingsProtocol,
              expected_revision: value.revision, settings: { ...selection } }); }}>
            {save.isPending ? t("等待确认…", "Waiting for confirmation…") : t("保存验证设置", "Save verification settings")}</button>
        </fieldset>
        {value.restart_required && <div className="sandbox-environment-restart">
          <p>{t("重启后应用更改，并重新核对任务权限。", "Restart to apply changes. Task permissions will be checked again.")}</p>
          <button type="button" disabled={busy || dirty || settings.isFetching || save.isError || !value.can_restart || !!persistenceWarning}
            onClick={() => { setNotice(""); restart.mutate(); }}><RotateCw aria-hidden="true" size={14} />
            {restarting ? t("正在重启应用…", "Restarting…") : t("重启以应用验证设置", "Restart to apply verification settings")}</button>
          {persistenceWarning && <p role="alert">{persistenceWarning} {t("请先处理保存问题，再重启应用。", "Resolve the save issue before restarting.")}</p>}
        </div>}
      </>}
      {save.isError && <p role="alert">{t("保存结果待核对：", "Save unconfirmed. ")}{desktopErrorMessage(save.error)} {t("请重新读取设置后继续。", "Reload the settings to continue.")}</p>}
      {restart.isError && <p role="alert">{t("重启失败，已保存的设置仍保留。", "Restart failed. Your settings are saved. ")}{desktopErrorMessage(restart.error)}</p>}
      {notice && <p role="status">{notice}</p>}
      <button type="button" disabled={busy || settings.isFetching} onClick={() => void reload()}>{t("重新读取验证设置", "Reload verification settings")}</button>
    </>}
  </section>;
}
