import { useEffect, useId, useMemo, useRef, useState } from "react";
import { SquareSlash, Settings2, Trash2 } from "lucide-react";
import { useLocale } from "../../lib/locale";
import { createPromptCommand, deletePromptCommand, expandPromptCommand,
  maximumPromptCommandBodyBytes, PromptCommandError, promptCommandIssueText,
  readPromptCommands, sortedPromptCommands, updatePromptCommand,
  type PromptCommand, type PromptCommandInput } from "../../lib/prompt-commands";

// The "/" menu mirrors the built-in session slash-command surface for the
// operator's own stored commands. Selection only ever inserts text into the
// composer draft: the operator reviews and sends it themselves, so the menu
// cannot bypass any policy, tool, or permission surface.
export function V2PromptCommandMenu({ storage, query, onSelect, onDismiss }: {
  storage: Storage | undefined;
  query: string;
  onSelect: (expanded: string) => void;
  onDismiss: () => void;
}) {
  const { t } = useLocale();
  const [managing, setManaging] = useState(false);
  const [revision, setRevision] = useState(0);
  const id = useId();
  const commands = useMemo(() => sortedPromptCommands(readPromptCommands(storage)), [storage, revision]);
  const matches = useMemo(() => commands.filter((command) => command.name.startsWith(query)), [commands, query]);
  // Type-ahead surface: focus stays in the composer textarea while the menu is
  // open (selection is pointer-driven), so keystrokes are never stolen. Outside
  // dismissal is owned by the composer form, not this menu.
  if (managing) {
    return <V2PromptCommandManager storage={storage} onClose={() => {
      setManaging(false);
      setRevision((value) => value + 1);
    }} />;
  }
  return <div aria-label={t("自定义指令", "Custom commands")} className="v2-prompt-command-menu" id={id} role="menu">
    {matches.length === 0 && <p className="v2-prompt-command-empty" role="status">
      {query ? t("没有匹配的指令。", "No matching commands.") : t("指令库还是空的。", "The command library is empty.")}
    </p>}
    {matches.map((command) => <button className="v2-prompt-command-item" data-command={command.name} key={command.id}
      onMouseDown={(event) => event.preventDefault()} role="menuitem" tabIndex={-1} type="button"
      onClick={() => onSelect(expandPromptCommand(command.body, ""))}>
      <SquareSlash aria-hidden="true" size={14} />
      <span><strong>/{command.name}</strong>{command.title && <small>{command.title}</small>}</span>
    </button>)}
    <div className="v2-prompt-command-menu-footer">
      <button className="v2-prompt-command-manage" role="menuitem" tabIndex={-1} type="button"
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => setManaging(true)}>
        <Settings2 aria-hidden="true" size={14} />
        <span>{t("管理指令库…", "Manage commands…")}</span>
      </button>
      <button aria-label={t("关闭指令菜单", "Close the command menu")} className="v2-prompt-command-manage"
        onMouseDown={(event) => event.preventDefault()} role="menuitem" tabIndex={-1} type="button"
        onClick={onDismiss}>
        {t("关闭", "Close")}
      </button>
    </div>
  </div>;
}

interface ManagerFormState {
  id: string | null;
  name: string;
  title: string;
  body: string;
}

// The manager is a plain client-side CRUD dialog over the operator's own
// stored commands. It never executes a body, never sends one, and grants no
// authority: the worst outcome of a hostile stored command is text appearing
// in the operator's own draft.
function V2PromptCommandManager({ storage, onClose }: {
  storage: Storage | undefined;
  onClose: () => void;
}) {
  const { t } = useLocale();
  const [revision, setRevision] = useState(0);
  const [form, setForm] = useState<ManagerFormState>({ id: null, name: "", title: "", body: "" });
  const [error, setError] = useState<string>("");
  const commands = useMemo(() => sortedPromptCommands(readPromptCommands(storage)), [storage, revision]);
  const refresh = () => setRevision((value) => value + 1);
  const submit = () => {
    const input: PromptCommandInput = { name: form.name.trim(), title: form.title.trim(), body: form.body };
    try {
      if (form.id === null) createPromptCommand(storage, input);
      else updatePromptCommand(storage, form.id, input);
      setError("");
      setForm({ id: null, name: "", title: "", body: "" });
      refresh();
    } catch (failure) {
      setError(failure instanceof PromptCommandError ? failure.message : promptCommandIssueText("storage_unavailable"));
    }
  };
  const remove = (command: PromptCommand) => {
    try {
      deletePromptCommand(storage, command.id);
      if (form.id === command.id) setForm({ id: null, name: "", title: "", body: "" });
      setError("");
      refresh();
    } catch (failure) {
      setError(failure instanceof PromptCommandError ? failure.message : promptCommandIssueText("storage_unavailable"));
    }
  };
  return <div className="v2-prompt-command-overlay" role="presentation"
    onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <div aria-label={t("管理自定义指令", "Manage custom commands")} className="v2-prompt-command-manager" role="dialog" aria-modal="true">
      <header><strong>{t("管理自定义指令", "Manage custom commands")}</strong>
        <button aria-label={t("关闭", "Close")} type="button" onClick={onClose}>✕</button></header>
      <p className="v2-prompt-command-hint">
        {t("指令是保存在本机的常用提示词模板。在输入框键入 / 加指令名即可把内容填入草稿；正文里的 $ARGUMENTS 会被替换为指令名后输入的文字。发送前仍可编辑。", "Commands are locally saved prompt templates. Type / plus a command name in the composer to fill the draft; $ARGUMENTS in the body is replaced by whatever you type after the name. You can still edit before sending.")}
      </p>
      <form className="v2-prompt-command-form" onSubmit={(event) => { event.preventDefault(); submit(); }}>
        <div className="v2-prompt-command-row">
          <label>/ <input aria-label={t("指令名", "Command name")} maxLength={32} onChange={(event) =>
            setForm((current) => ({ ...current, name: event.target.value }))} placeholder="release-check"
            value={form.name} /></label>
          <label><span className="sr-only">{t("标题", "Title")}</span>
            <input aria-label={t("标题（可选）", "Title (optional)")} maxLength={128} onChange={(event) =>
              setForm((current) => ({ ...current, title: event.target.value }))} placeholder={t("标题（可选）", "Title (optional)")}
              value={form.title} /></label>
        </div>
        <textarea aria-label={t("指令内容", "Command body")} maxLength={maximumPromptCommandBodyBytes}
          onChange={(event) => setForm((current) => ({ ...current, body: event.target.value }))}
          placeholder={t("要填入草稿的提示词正文…（可包含 $ARGUMENTS）", "Prompt body to insert into the draft… ($ARGUMENTS supported)")}
          rows={6} value={form.body} />
        {error && <p role="alert">{error}</p>}
        <div className="v2-prompt-command-row">
          <button type="submit">{form.id === null ? t("新建指令", "Create command") : t("保存修改", "Save changes")}</button>
          {form.id !== null && <button type="button" onClick={() => setForm({ id: null, name: "", title: "", body: "" })}>
            {t("改为新建", "Switch to creating")}</button>}
        </div>
      </form>
      <ul className="v2-prompt-command-list">
        {commands.map((command) => <li key={command.id}>
          <div><strong>/{command.name}</strong>{command.title && <small>{command.title}</small>}</div>
          <div className="v2-prompt-command-actions">
            <button type="button" onClick={() => setForm({ id: command.id, name: command.name,
              title: command.title, body: command.body })}>{t("编辑", "Edit")}</button>
            <button aria-label={t(`删除 /${command.name}`, `Delete /${command.name}`)} type="button"
              onClick={() => remove(command)}><Trash2 aria-hidden="true" size={14} /></button>
          </div>
        </li>)}
        {commands.length === 0 && <li className="v2-prompt-command-empty">{t("还没有保存任何指令。", "No saved commands yet.")}</li>}
      </ul>
    </div>
  </div>;
}
