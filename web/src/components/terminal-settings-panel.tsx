import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useLocale } from "../lib/locale";
import { desktopErrorMessage } from "../lib/desktop-bridge";
import { desktopTerminalSettingsAvailable, getDesktopTerminalSettings, saveDesktopTerminalSettings,
  type TerminalSettings } from "../lib/desktop-terminal-settings";
import "./terminal-settings-panel.css";

const settingsKey = ["desktop-terminal-settings"] as const;
export function TerminalSettingsPanel() {
  const { t } = useLocale();
  const queries = useQueryClient();
  const available = desktopTerminalSettingsAvailable();
  const [draft, setDraft] = useState<{ revision: number; value: TerminalSettings } | null>(null);
  const [notice, setNotice] = useState("");
  const query = useQuery({ queryKey: settingsKey, queryFn: getDesktopTerminalSettings, enabled: available, retry: false, refetchOnWindowFocus: false });
  const save = useMutation({ mutationFn: async () => {
    if (!draft) throw new Error("No terminal preference change");
    return saveDesktopTerminalSettings(draft.revision, draft.value);
  }, onSuccess: (result) => {
    if (result.status === "saved") {
      queries.setQueryData(settingsKey, result.view); setDraft(null);
      setNotice(t("已保存。授权设置立即生效；shell 选择用于下次新建终端。", "Saved. Access settings apply now; the shell selection applies to new terminals."));
    } else setNotice(t("已取消，设置未改变。", "Cancelled. Settings are unchanged."));
  } });
  const view = query.data;
  const value = draft?.value ?? view?.settings;
  const change = (patch: Partial<TerminalSettings>) => {
    if (!view || !value) return;
    setDraft({ revision: draft?.revision ?? view.revision, value: { ...value, ...patch } }); setNotice(""); save.reset();
  };
  const reload = async () => { const result = await query.refetch(); if (result.isSuccess) { setDraft(null); save.reset(); setNotice(""); } };
  const shell = view?.shells.find(item => item.id === value?.default_shell);
  return <section className="v2-settings-section terminal-settings" aria-labelledby="terminal-settings-title">
    <h2 id="terminal-settings-title">{t("内置终端", "Integrated terminal")}</h2>
    <p>{t("手动运行命令、查看日志，也可以让 Agent 操作这个终端。Agent 自己的命令执行工具继续遵循任务权限。", "Run commands and inspect output yourself, or allow the Agent to use this terminal. The Agent’s own command tool continues to follow task permissions.")}</p>
    {!available ? <p>{t("请在桌面应用中设置终端。", "Configure the terminal in the desktop app.")}</p>
      : !value ? <p role={query.isError ? "alert" : "status"}>{query.isError ? desktopErrorMessage(query.error) : t("正在读取终端设置…", "Loading terminal settings…")}</p>
      : <fieldset disabled={save.isPending || !view?.can_save}>
        <label>{t("Agent 操作内置终端", "Agent access to the terminal")}
          <select value={value.agent_control} onChange={event => change({ agent_control: event.target.value as TerminalSettings["agent_control"] })}>
            <option value="disabled">{t("禁止（仍可手动使用）", "Disabled (manual use remains available)")}</option>
            <option value="manual">{t("手动限时授权", "Manual temporary access")}</option>
            <option value="full">{t("完全允许（任务需完全访问权限）", "Fully allowed (requires Full access for the task)")}</option>
          </select>
        </label>
        <p>{value.agent_control === "full"
          ? t("任务具备完全访问权限时，自动允许读取终端输出和提交命令，无需逐次审批。可在终端里随时停止 Agent 控制。", "With Full access for the task, reading output and submitting commands are allowed automatically, without repeated approvals. Stop Agent control from the terminal at any time.")
          : value.agent_control === "disabled" ? t("Agent 无法通过共享终端接口读取输出或提交命令；你仍可以手动输入、复制和引用日志。", "The Agent cannot read or write through the shared-terminal interface. You can still type, copy, and quote output yourself.")
            : t("在终端点击“允许 Agent”后，按下方时长临时授权；到期后需要重新授权。", "Select Allow Agent in the terminal to grant access for the duration below. Authorization expires automatically.")}</p>
        {value.agent_control === "manual" && <label>{t("每次授权时长（秒）", "Authorization duration (seconds)")}
          <input type="number" min={15} max={900} step={1} value={Number.isFinite(value.manual_ttl_seconds) ? value.manual_ttl_seconds : ""}
            onChange={event => change({ manual_ttl_seconds: event.target.valueAsNumber })} />
          <small>{t("15–900 秒，默认 300 秒（5 分钟）。", "15–900 seconds; default 300 seconds (5 minutes).")}</small>
        </label>}
        <label>{t("新终端使用的 shell", "Shell for new terminals")}
          <select value={value.default_shell} onChange={event => change({ default_shell: event.target.value as TerminalSettings["default_shell"] })}>
            <option value="auto">{t("自动（Windows 优先 PowerShell 7）", "Automatic (prefer PowerShell 7 on Windows)")}</option>
            {view?.shells.map(item => <option key={item.id} value={item.id} disabled={!item.available}>{item.name}{!item.available ? t(" · 未安装", " · Not installed") : ""}</option>)}
          </select>
        </label>
        {shell?.available && <p className="terminal-shell-path"><code>{shell.path}</code>{shell.version && <small>{t("可执行文件版本：", "Executable version: ")}{shell.version}</small>}</p>}
        <p>{t("已有终端保持原 shell。Windows 隔离执行环境仍使用 PowerShell 7，不受这里的 CMD 选择影响。", "Existing terminals keep their shell. Isolated execution on Windows continues to use PowerShell 7 regardless of a CMD selection here.")}</p>
        <button type="button" disabled={!draft || !Number.isInteger(value.manual_ttl_seconds) || value.manual_ttl_seconds < 15 || value.manual_ttl_seconds > 900} onClick={() => save.mutate()}>
          {save.isPending ? t("正在保存…", "Saving…") : t("保存终端设置", "Save terminal settings")}</button>
      </fieldset>}
    {save.isError && <p role="alert">{desktopErrorMessage(save.error)}</p>}
    {view && !view.can_save && <p>{t("当前启动方式使用默认终端设置。直接打开桌面应用即可保存偏好。", "This startup mode uses default terminal settings. Open the desktop app normally to save preferences.")}</p>}
    {notice && <p role="status">{notice}</p>}
    {available && <button className="v2-setting-link" type="button" disabled={save.isPending || query.isFetching} onClick={() => void reload()}>{t("重新读取终端设置", "Reload terminal settings")}</button>}
  </section>;
}
