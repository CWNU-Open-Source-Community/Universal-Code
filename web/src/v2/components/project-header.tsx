import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { Check, ChevronRight, Copy, Folder, FolderOpen, MessageCircle, MoreHorizontal, Pin, Settings, SquarePen } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { useLocale } from "../../lib/locale";

export const projectDetailsKey = (id: string) => ["v2", "workspace-project", id] as const;

function ProjectDirectoryCopy({ path }: { path: string }) {
  const { t } = useLocale();
  const [state, setState] = useState<"idle" | "copying" | "copied" | "failed">("idle");
  const copy = async () => {
    if (state === "copying") return;
    setState("copying");
    try {
      await navigator.clipboard.writeText(path);
      setState("copied");
    } catch {
      setState("failed");
    }
  };
  return <button className="v2-project-card-edit" aria-label={t("复制项目目录", "Copy project directory")}
    aria-disabled={state === "copying"} onClick={() => void copy()} type="button">
    {state === "copied" ? <Check aria-hidden="true" size={16} /> : <Copy aria-hidden="true" size={16} />}
    <span role="status" aria-live="polite">{state === "copying" ? t("正在复制…", "Copying…")
      : state === "copied" ? t("已复制项目目录", "Project directory copied")
        : state === "failed" ? t("复制失败，请重试", "Copy failed. Try again")
          : t("复制项目目录", "Copy project directory")}</span>
  </button>;
}

export function ProjectHeader({ workspace, label, expanded, onToggle, onNewChat, onEdit, client, pinned = false, onTogglePin }: {
  workspace: WorkspaceView; label: string; expanded: boolean; onToggle: () => void;
  onNewChat?: () => void; onEdit?: (workspace: WorkspaceView) => void; client?: APIClient;
  pinned?: boolean; onTogglePin?: () => void;
}) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 0, top: 0 });
  const row = useRef<HTMLElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const more = useRef<HTMLButtonElement>(null);
  const edit = useRef<HTMLButtonElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const focusCard = useRef(false);
  const details = useQuery({ queryKey: projectDetailsKey(workspace.id),
    queryFn: ({ signal }) => client!.getWorkspaceProject(workspace.id, signal),
    enabled: open && Boolean(client?.hasControl), staleTime: 10_000 });
  const clearTimer = () => clearTimeout(timer.current);
  const scheduleClose = () => { clearTimer(); if (!card.current?.contains(document.activeElement)) timer.current = setTimeout(() => setOpen(false), 160); };
  useEffect(() => () => clearTimeout(timer.current), []);
  useLayoutEffect(() => {
    if (!open || !row.current || !card.current) return;
    const bounds = row.current.getBoundingClientRect();
    const popup = card.current.getBoundingClientRect();
    const fitsRight = bounds.right + 8 + popup.width <= window.innerWidth - 12;
    setPosition({ left: fitsRight ? bounds.right + 8 : Math.max(12, window.innerWidth - popup.width - 12),
      top: Math.max(12, Math.min(fitsRight ? bounds.top : bounds.bottom + 6, window.innerHeight - popup.height - 12)) });
    if (focusCard.current) { (edit.current ?? card.current).focus(); focusCard.current = false; }
  }, [open, details.data, details.isError, pinned]);
  useEffect(() => {
    if (!open) return;
    const close = (event: Event) => {
      if (event.type === "pointerdown" && (row.current?.contains(event.target as Node) || card.current?.contains(event.target as Node))) return;
      if (event.type === "scroll" && card.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (card.current?.contains(document.activeElement)) more.current?.focus();
      setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", escape);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [open]);
  const leaveFocus = (target: EventTarget | null) => {
    if (!row.current?.contains(target as Node) && !card.current?.contains(target as Node)) setOpen(false);
  };
  return <header className={`v2-project-row${open ? " is-open" : ""}`} ref={row}
    onMouseEnter={() => { clearTimer(); timer.current = setTimeout(() => setOpen(true), 350); }}
    onMouseLeave={scheduleClose} onBlur={(event) => leaveFocus(event.relatedTarget)}>
    <button className="v2-project-title" aria-expanded={expanded} onClick={onToggle} type="button" title={label}>
      <FolderOpen aria-hidden="true" className="v2-project-folder" size={17} />
      <ChevronRight aria-hidden="true" className={`v2-project-chevron${expanded ? " is-expanded" : ""}`} size={16} />
      <span>{label}</span>
      {pinned && <Pin className="v2-project-pin-marker" size={12} aria-hidden="true" />}
    </button>
    <div className="v2-project-row-actions">
      <button aria-label={t(`${label} 项目信息`, `${label} project details`)} aria-expanded={open} aria-haspopup="dialog" ref={more}
      onClick={() => { clearTimer(); if (open) (edit.current ?? card.current)?.focus(); else { focusCard.current = true; setOpen(true); } }} type="button">
        <MoreHorizontal aria-hidden="true" size={16} /></button>
      {onNewChat && <button aria-label={t(`在 ${label} 中新建聊天`, `New chat in ${label}`)}
        onClick={() => { clearTimer(); setOpen(false); onNewChat(); }} type="button"><SquarePen aria-hidden="true" size={15} /></button>}
    </div>
    {open && createPortal(<div className="v2-project-card" ref={card} role="dialog" tabIndex={-1} aria-label={t(`${label} 项目信息`, `${label} project details`)}
      style={position} onMouseEnter={clearTimer} onMouseLeave={scheduleClose} onBlur={(event) => leaveFocus(event.relatedTarget)}>
      <strong><FolderOpen aria-hidden="true" size={18} /><span>{details.data?.name ?? workspace.name}</span>
        {onTogglePin && <button className="v2-project-pin" type="button" onClick={onTogglePin} aria-pressed={pinned}
          aria-label={pinned ? t("取消置顶项目", "Unpin project") : t("置顶项目", "Pin project")}
          title={pinned ? t("取消置顶项目", "Unpin project") : t("置顶项目", "Pin project")}>
          <Pin aria-hidden="true" size={16} fill={pinned ? "currentColor" : "none"} />
        </button>}
      </strong>
      {details.data ? <>
        <p><MessageCircle aria-hidden="true" size={16} /><span>{t(`${details.data.thread_count} 个聊天 · ${details.data.active_thread_count} 个活跃聊天`,
          `${details.data.thread_count} chats · ${details.data.active_thread_count} active`)}</span></p>
        <p className="v2-project-card-path"><Folder aria-hidden="true" size={16} /><span>{details.data.directory_path}</span></p>
      </> : <p>{!client?.hasControl ? t("连接操作令牌后可查看项目详情。", "Connect with a control token to view project details.")
        : details.isError ? t("项目信息读取失败。", "Could not load project details.") : t("正在读取项目信息…", "Loading project details…")}
        {details.isError && <button onClick={() => void details.refetch()} type="button">{t("重试", "Retry")}</button>}</p>}
      {details.data?.directory_path && <ProjectDirectoryCopy key={`${workspace.id}:${details.data.directory_path}`} path={details.data.directory_path} />}
      {onEdit && <button className="v2-project-card-edit" ref={edit} onClick={() => {
        clearTimer(); setOpen(false); more.current?.focus(); onEdit(details.data ?? workspace);
      }} type="button"><Settings aria-hidden="true" size={16} />{t("编辑项目", "Edit project")}</button>}
    </div>, document.body)}
  </header>;
}
