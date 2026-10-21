import { useState } from "react";
import { RotateCcw, Search } from "lucide-react";
import { useLocale } from "../../lib/locale";
import { eventShortcut, saveChatPreferences, shortcutBinding, shortcutCommands, shortcutLabel,
  useChatPreferences, validShortcut, type ShortcutCommand } from "../chat-preferences";

export function V2KeyboardSettings() {
  const { t } = useLocale();
  const preferences = useChatPreferences();
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState<ShortcutCommand | "keySearch" | null>(null);
  const [error, setError] = useState("");
  const save = (shortcuts: typeof preferences.shortcuts) => {
    try { saveChatPreferences({ shortcuts }); setError(""); setRecording(null); }
    catch { setError(t("未能保存快捷键，请检查本机存储后重试。", "Could not save shortcuts. Check local storage and try again.")); }
  };
  return <>
    <h1>{t("快捷键", "Keyboard shortcuts")}</h1>
    <p className="v2-settings-lead">{t("搜索操作，或录制组合键。点击快捷键即可修改；修改后立即生效。", "Find an action or record a key combination. Click a shortcut to change it; changes take effect immediately.")}</p>
    <p className="v2-settings-lead">{t("上一／下一聊天按侧栏已加载的可见顺序切换，到达边缘时停止。可先展开项目或加载更早聊天。", "Previous and next chat follow the loaded, visible sidebar order and stop at its edges. Expand a project or load earlier chats to include them.")}</p>
    <div className="v2-shortcut-search">
      <Search size={16} aria-hidden="true" />
      <input aria-label={t("搜索快捷键", "Search shortcuts")} placeholder={t("搜索操作或快捷键…", "Search actions or shortcuts…")}
        readOnly={recording === "keySearch"} value={query} onChange={(event) => setQuery(event.target.value)}
        data-shortcut-recorder={recording === "keySearch" || undefined}
        onKeyDown={(event) => {
          if (recording !== "keySearch") return;
          event.preventDefault(); event.stopPropagation();
          if (event.key === "Escape") { setRecording(null); return; }
          const binding = eventShortcut(event.nativeEvent);
          if (binding) { setQuery(shortcutLabel(binding)); setRecording(null); }
        }} />
      <button type="button" aria-pressed={recording === "keySearch"} onClick={(event) => {
        setRecording(recording === "keySearch" ? null : "keySearch");
        event.currentTarget.parentElement?.querySelector("input")?.focus();
      }}>{recording === "keySearch" ? t("请按组合键…", "Press shortcut…") : t("录制按键", "Record keys")}</button>
    </div>
    <div className="v2-settings-card v2-shortcut-list">
      {shortcutCommands.filter((command) => `${command.zh} ${command.en} ${shortcutLabel(shortcutBinding(preferences, command.id))}`
        .toLocaleLowerCase().includes(query.toLocaleLowerCase().trim())).map((command) => {
        const binding = shortcutBinding(preferences, command.id);
        const label = t(command.zh, command.en);
        return <div className="v2-shortcut-row" key={command.id}>
          <span>{label}</span>
          <button type="button" className="v2-shortcut-binding" aria-label={t(`修改${label}快捷键`, `Change shortcut for ${label}`)}
            data-shortcut-recorder={recording === command.id || undefined}
            onClick={() => { setError(""); setRecording(command.id); }}
            onBlur={() => { if (recording === command.id) setRecording(null); }}
            onKeyDown={(event) => {
              if (recording !== command.id) return;
              event.preventDefault(); event.stopPropagation();
              if (event.nativeEvent.isComposing || event.keyCode === 229 || event.repeat) return;
              if (event.key === "Escape") { setRecording(null); return; }
              if (event.key === "Backspace" || event.key === "Delete") { save({ ...preferences.shortcuts, [command.id]: "" }); return; }
              const next = eventShortcut(event.nativeEvent);
              if (!next) return;
              if (!validShortcut(next, command.id)) { setError(t("此组合键保留给系统、浏览器或文本编辑，请换一个。", "This shortcut is reserved for the system, browser or text editing. Choose another.")); return; }
              const conflict = shortcutCommands.find(({ id }) => id !== command.id && shortcutBinding(preferences, id) === next);
              if (conflict) { setError(t(`已用于「${conflict.zh}」，请先修改或清除该快捷键。`, `Already used by “${conflict.en}”. Change or clear that shortcut first.`)); return; }
              save({ ...preferences.shortcuts, [command.id]: next });
            }}><kbd>{recording === command.id ? t("请按组合键…", "Press shortcut…") : binding ? shortcutLabel(binding) : t("未设置", "Unassigned")}</kbd></button>
          <button type="button" className="v2-shortcut-reset" disabled={preferences.shortcuts[command.id] === undefined}
            aria-label={t(`恢复${label}默认快捷键`, `Reset shortcut for ${label}`)} title={t("恢复默认", "Reset default")}
            onClick={() => {
              const conflict = shortcutCommands.find(({ id }) => id !== command.id && shortcutBinding(preferences, id) === command.binding);
              if (conflict) { setError(t(`默认组合键已用于「${conflict.zh}」。`, `The default shortcut is used by “${conflict.en}”.`)); return; }
              const shortcuts = { ...preferences.shortcuts }; delete shortcuts[command.id]; save(shortcuts);
            }}><RotateCcw size={15} aria-hidden="true" /></button>
        </div>;
      })}
      {!shortcutCommands.some((command) => `${command.zh} ${command.en} ${shortcutLabel(shortcutBinding(preferences, command.id))}`
        .toLocaleLowerCase().includes(query.toLocaleLowerCase().trim())) && <p>{t("没有匹配的快捷键", "No matching shortcuts")}</p>}
    </div>
    {error && <p role="alert">{error}</p>}
    <p className="v2-settings-lead">{t("录制时按 Esc 取消，按 Backspace 清除。菜单使用方向键与 Enter；Shift+Enter 换行，Ctrl／⌘+Enter 发送。", "While recording, Escape cancels and Backspace clears. Use arrows and Enter in menus; Shift+Enter adds a line and Ctrl/⌘+Enter sends.")}</p>
    <button type="button" className="v2-shortcut-reset-all" onClick={() => save({})}>{t("恢复全部默认快捷键", "Reset all shortcuts")}</button>
  </>;
}
