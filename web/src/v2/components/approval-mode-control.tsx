import { useEffect, useId, useRef, useState } from "react";
import { Check, ChevronDown, ShieldCheck, ShieldOff, UserCheck } from "lucide-react";
import { useLocale } from "../../lib/locale";
import type { ApprovalModeControlProps, ExecutionApprovalMode, FullActivationState } from "./approval-mode-contract";
import { V2ConfirmDialog } from "./dialog";

export function V2ApprovalModeControl({ mode, fullActivation, fullUnavailableReason,
  pending, disabled = false, error, variant = "menu", selectionOnly = false, onRequestChange }: ApprovalModeControlProps) {
  const { t } = useLocale();
  const choices = [
    { mode: "ask", label: t("请求批准", "Request approval"), icon: UserCheck,
      detail: t("文件、命令与网页操作按请求批准。",
        "Request approval for file, command, and web operations.") },
    { mode: "auto", label: t("帮我批准", "Approve for me"), icon: ShieldCheck,
      detail: t("自动审核文件、命令与网页操作，必要时向你确认。",
        "Review file, command, and web operations automatically; ask you when needed.") },
    { mode: "full", label: t("完全访问权限", "Full access"), icon: ShieldOff,
      detail: t("允许文件、本机命令与公共网络访问，无需逐次批准。",
        "Allow file, local command, and public network access without per-operation approval.") },
  ] as const;
  const activationLabels: Record<FullActivationState, string> = {
    inactive: t("未激活", "inactive"), active: t("已激活", "active"),
    unavailable: t("不可用", "unavailable"),
  };
  const [open, setOpen] = useState(false);
  const [confirmation, setConfirmation] = useState<{
    mode: ExecutionApprovalMode; activation: FullActivationState;
  } | null>(null);
  const confirmingRef = useRef(false);
  const shellRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const settingsRef = useRef<HTMLElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const focusLastRef = useRef(false);
  const tabbingRef = useRef(false);
  const id = useId();
  const blocked = pending || disabled;
  const selected = choices.find((choice) => choice.mode === mode)!;
  const SelectedIcon = selected.icon;
  const coldFull = mode === "full" && fullActivation === "inactive";
  const unavailable = fullUnavailableReason?.trim() ||
    t("完全访问需要当前运行环境提供支持，请查看服务的权限配置。", "Full access requires runtime support. Check the service's permission configuration.");
  const status = t(`完全访问${activationLabels[fullActivation]}`, `Full access ${activationLabels[fullActivation]}`);
  const confirmationOpen = confirmation !== null && confirmation.mode === mode &&
    confirmation.activation === fullActivation && !disabled && fullActivation !== "unavailable";

  // Refreshed host state invalidates an old confirmation, never grants access.
  useEffect(() => {
    if (confirmation) {
      const target = returnFocusRef.current;
      if (!target?.isConnected || (target instanceof HTMLButtonElement && target.disabled)) {
        (variant === "menu" ? triggerRef.current : settingsRef.current)?.focus();
      }
    }
    confirmingRef.current = false;
    setConfirmation(null);
  }, [mode, fullActivation, disabled, variant, selectionOnly]);

  useEffect(() => {
    if (!open) return;
    tabbingRef.current = false;
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []);
    (items[focusLastRef.current ? items.length - 1 : 0] ?? menuRef.current)?.focus();
    const outside = (event: MouseEvent) => {
      if (!shellRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    window.addEventListener("click", outside);
    return () => window.removeEventListener("click", outside);
  }, [open]);

  const closeMenu = (restoreFocus = false) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  };
  const choose = (next: ExecutionApprovalMode, trigger: HTMLButtonElement) => {
    if (blocked || (next === "full" && fullActivation === "unavailable")) return;
    if (next === mode && !coldFull) return;
    if (next === "full") {
      returnFocusRef.current = variant === "menu" ? triggerRef.current : trigger;
      confirmingRef.current = true;
      setConfirmation({ mode, activation: fullActivation });
      closeMenu();
      return;
    }
    closeMenu(true);
    onRequestChange({ mode: next, confirmFull: false });
  };
  const cancel = () => {
    if (pending) return;
    confirmingRef.current = false;
    setConfirmation(null);
  };
  const confirm = () => {
    if (blocked || !confirmationOpen || !confirmingRef.current) return;
    // Close synchronously before notifying the host so repeated confirmation
    // cannot submit again before pending props arrive.
    confirmingRef.current = false;
    setConfirmation(null);
    if (variant === "settings") returnFocusRef.current = settingsRef.current;
    onRequestChange({ mode: "full", confirmFull: true });
  };

  const options = <>
    <div aria-label={t("执行权限档位", "Execution approval modes")} className="v2-permission-options" role="group">
      {choices.map(({ mode: value, label, detail, icon: Icon }) => {
        const active = value === mode;
        return <button aria-label={label} aria-describedby={`${id}-${value}${value === "full" && fullActivation === "unavailable" ? ` ${id}-unavailable` : ""}`}
          aria-checked={variant === "menu" ? active : undefined}
          aria-pressed={variant === "settings" ? active : undefined}
          className={value === "full" ? "is-risk" : ""}
          disabled={blocked || active || (value === "full" && fullActivation === "unavailable")}
          key={value} onClick={(event) => choose(value, event.currentTarget)}
          role={variant === "menu" ? "menuitemradio" : undefined}
          tabIndex={variant === "menu" ? -1 : undefined} type="button">
          <Icon aria-hidden="true" size={17} /><span><strong>{label}</strong>
            <small id={`${id}-${value}`}>{value === "full" && fullActivation === "unavailable"
              ? t("当前不可用", "Currently unavailable") : value === "full" && active && !selectionOnly
                ? `${detail} ${t("已选择", "Selected")} · ${activationLabels[fullActivation]}` : detail}</small></span>
          {active ? <Check aria-hidden="true" size={15} /> : null}
        </button>;
      })}
    </div>
    {fullActivation === "unavailable" && <details className="v2-permission-availability">
      <summary>{t("查看完全访问的启用条件", "View Full access requirements")}</summary>
      <p id={`${id}-unavailable`}>{unavailable}</p>
    </details>}
    {coldFull && <button className="v2-permission-downgrade" disabled={blocked}
      onClick={(event) => choose("full", event.currentTarget)}
      role={variant === "menu" ? "menuitem" : undefined}
      tabIndex={variant === "menu" ? -1 : undefined} type="button">
      <ShieldOff aria-hidden="true" size={16} /><span><strong>{selectionOnly
        ? t("确认完全访问权限", "Confirm Full access") : t("重新激活完全访问权限", "Reactivate Full access")}</strong>
        {!selectionOnly && <small>{t("已保存选择；重新激活仍需确认。", "Your preference is saved; reactivation still requires confirmation.")}</small>}</span>
    </button>}
  </>;

  return <div aria-busy={pending} className={`v2-permission-control is-${variant}`} ref={shellRef}>
    {variant === "menu" ? <>
      <button aria-controls={open ? id : undefined} aria-expanded={open} aria-haspopup="menu"
        className={`v2-composer-chip${mode === "full" ? " is-risk" : ""}`}
        title={`${selected.label}${mode === "full" && !selectionOnly ? ` · ${activationLabels[fullActivation]}` : ""}`}
        onClick={() => { focusLastRef.current = false; setOpen((value) => !value); }}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault(); focusLastRef.current = event.key === "ArrowUp"; setOpen(true);
          }
        }} ref={triggerRef} type="button">
        <SelectedIcon aria-hidden="true" size={14} /><span className="v2-composer-permission-label">{selected.label}
        {mode === "full" && !selectionOnly ? ` · ${activationLabels[fullActivation]}` : ""}</span>
        <ChevronDown aria-hidden="true" size={13} />
      </button>
      {open && <div aria-label={t("选择执行权限", "Choose execution permissions")} className="v2-permission-popover" id={id}
        onBlur={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null) &&
            (event.relatedTarget !== triggerRef.current || tabbingRef.current)) setOpen(false);
        }} onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault(); event.stopPropagation(); closeMenu(true); return;
          }
          if (event.key === "Tab") { tabbingRef.current = true; return; }
          if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
          event.preventDefault(); event.stopPropagation();
          const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
          const current = items.indexOf(document.activeElement as HTMLButtonElement);
          const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1
            : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
          items[next]?.focus();
        }} ref={menuRef} role="menu" tabIndex={-1}>
        <header><strong>{t("执行权限", "Execution permissions")}</strong>{!selectionOnly && <span>{status}</span>}</header>{options}
      </div>}
    </> : <section aria-label={t("执行权限", "Execution permissions")} className="v2-settings-card v2-permission-settings-card"
      ref={settingsRef} tabIndex={-1}>
      <header><div><h2>{t("执行权限", "Execution permissions")}</h2>{!selectionOnly && <p>{status}</p>}</div></header>{options}
    </section>}
    {pending && <span role="status">{t("正在更新权限…", "Updating permissions…")}</span>}
    {error && <p className="v2-inline-error" role="alert">{error.trim() || t("权限更新失败", "Failed to update permissions")}</p>}
    {confirmationOpen && <V2ConfirmDialog open busy={pending}
      confirmLabel={selectionOnly ? t("确认", "Confirm") : coldFull ? t("确认重新激活", "Confirm reactivation") : t("确认启用", "Confirm activation")} danger
      description={t("允许访问项目文件、执行本机命令和访问公共网络，无需逐次批准。可能造成数据丢失或敏感信息泄露；已设置的环境与网络限制仍然有效。",
        "Allow project file access, local commands, and public network access without per-operation approval. This may cause data loss or expose sensitive information. Configured environment and network restrictions still apply.")}
      onCancel={cancel} onConfirm={confirm} returnFocusRef={returnFocusRef}
      title={selectionOnly ? t("确认完全访问权限？", "Confirm Full access?") : coldFull ? t("重新激活完全访问权限？", "Reactivate Full access?") : t("启用完全访问权限？", "Enable Full access?")} />}
  </div>;
}
