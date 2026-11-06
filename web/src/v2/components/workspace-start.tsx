import { useEffect, useId, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { Folder, FolderOpen, FolderPlus, LoaderCircle, Plus, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { desktopWorkspaceImportEnabled, selectDesktopWorkspaceDirectory, createDesktopWorkspace } from "../../lib/desktop-bridge";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { projectDetailsKey } from "./project-header";
import "./project-dialog.css";

export function V2WorkspaceStart({ client, onSelect, variant = "composer" }: {
  client: APIClient; onSelect: (workspace: WorkspaceView) => void;
  variant?: "composer" | "sidebar";
}) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  return <div className="v2-workspace-start">
    <button className={variant === "sidebar" ? "v2-project-add" : "v2-composer-chip"}
      aria-label={variant === "sidebar" ? t("添加项目", "Add project") : undefined}
      title={variant === "sidebar" ? t("添加项目", "Add project") : undefined}
      onClick={() => setOpen(true)} ref={trigger} type="button">
      {variant === "sidebar" ? <Plus aria-hidden="true" size={18} /> : <FolderOpen aria-hidden="true" size={15} />}
      {variant !== "sidebar" && t("接入项目", "Connect project")}
    </button>
    {open && <ProjectDialog client={client} onClose={() => setOpen(false)} onSaved={onSelect} returnFocusRef={trigger} />}
  </div>;
}

export function ProjectDialog({ client, workspace, onClose, onSaved, returnFocusRef }: {
  client: APIClient; workspace?: WorkspaceView; onClose: () => void;
  onSaved: (workspace: WorkspaceView) => void; returnFocusRef?: RefObject<HTMLElement | null>;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [name, setName] = useState(workspace?.name ?? "");
  const [directory, setDirectory] = useState("");
  const [pathInput, setPathInput] = useState("");
  const [editingPath, setEditingPath] = useState(false);
  const [selection, setSelection] = useState("");
  const titleID = useId();
  const input = useRef<HTMLInputElement>(null);
  const path = useRef<HTMLInputElement>(null);
  const native = desktopWorkspaceImportEnabled();
  const canCreate = native || client.hasWorkspaceImport;
  const details = useQuery({ queryKey: projectDetailsKey(workspace?.id ?? ""),
    queryFn: ({ signal }) => client.getWorkspaceProject(workspace!.id, signal), enabled: Boolean(workspace && client.hasControl) });
  const choosing = useMutation({ mutationFn: () => selectDesktopWorkspaceDirectory(),
    onSuccess: (folder) => {
      if (!folder) return;
      setSelection(folder.handle); setDirectory(folder.directory_path);
      if (!name.trim()) setName(folder.name);
    } });
  const saving = useMutation({
    mutationFn: async () => workspace ? client.renameWorkspace(workspace.id, name.trim())
      : native ? createDesktopWorkspace(selection, name.trim())
        : client.importWorkspace(directory, undefined, name.trim()).then((result) => result.workspace),
    onSuccess: async (saved) => {
      queryClient.setQueryData<InfiniteData<{ items: WorkspaceView[]; page: unknown }>>(v2QueryKeys.workspaces,
        (current) => current && { ...current, pages: current.pages.map((page) => ({ ...page,
          items: page.items.map((item) => item.id === saved.id ? { ...item, name: saved.name } : item) })) });
      await queryClient.invalidateQueries({ queryKey: v2QueryKeys.workspaces });
      await queryClient.invalidateQueries({ queryKey: projectDetailsKey(saved.id) });
      onSaved(saved); onClose();
    },
  });
  const busy = choosing.isPending || saving.isPending;
  const close = () => { if (!busy) onClose(); };
  const dialog = useModalFocusTrap<HTMLElement>(true, close, busy, input, { isolateBackground: true, returnFocusRef });
  useEffect(() => {
    if (busy) dialog.current?.focus();
    else if (saving.isError || choosing.isError) input.current?.focus();
  }, [busy, saving.isError, choosing.isError, dialog]);
  const nameBytes = new TextEncoder().encode(name.trim()).length;
  const error = saving.error instanceof APIRequestError ? saving.error.code : "";
  const addPath = () => {
    const value = pathInput.trim();
    if (!value) return;
    setDirectory(value); setEditingPath(false);
    if (!name.trim()) setName(value.replace(/[\\/]+$/, "").split(/[\\/]/).at(-1) ?? "");
    input.current?.focus();
  };
  const displayedDirectory = workspace ? details.data?.directory_path : directory;
  return createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget) close();
  }}><section aria-labelledby={titleID} aria-modal="true" className="v2-dialog v2-project-dialog" ref={dialog} role="dialog" tabIndex={-1}>
    <header><h2 id={titleID}>{workspace ? t("编辑项目", "Edit project") : t("创建项目", "Create project")}</h2>
      <button aria-label={t("关闭项目窗口", "Close project dialog")} disabled={busy} onClick={close} type="button"><X aria-hidden="true" size={19} /></button></header>
    <form onSubmit={(event) => { event.preventDefault();
      if (!busy && nameBytes > 0 && nameBytes <= 128 && (workspace ? client.hasControl : canCreate && directory && !editingPath)) saving.mutate(); }}>
      <div className="v2-project-dialog-body">
        <label className="v2-project-name"><Folder aria-hidden="true" size={20} />
          <input aria-label={t("项目名称", "Project name")} autoComplete="off" disabled={busy || Boolean(workspace && !client.hasControl)}
            maxLength={128} onChange={(event) => setName(event.target.value)} placeholder={t("项目名称", "Project name")}
            ref={input} required value={name} />
        </label>
        {nameBytes > 128 && <p role="alert" className="v2-inline-error">{t("项目名称不能超过 128 字节，请缩短名称。", "Project names must be at most 128 bytes. Shorten the name.")}</p>}
        <div className="v2-project-source-label">{t("源文件夹", "Source folder")}</div>
        <div className={"v2-project-source" + (displayedDirectory && !editingPath ? " has-folder" : "")}>
          {workspace ? details.isError ? <p role="alert">{t("项目目录读取失败。", "Could not load the project folder.")}
            <button onClick={() => void details.refetch()} type="button">{t("重试", "Retry")}</button></p>
            : !displayedDirectory ? <p>{t("正在读取项目目录…", "Loading project folder…")}</p> : null
            : editingPath ? <div className="v2-project-path-entry">
              <label>{t("项目文件夹路径", "Project folder path")}<input autoFocus autoComplete="off" disabled={busy} maxLength={4096}
                onChange={(event) => setPathInput(event.target.value)} onKeyDown={(event) => {
                  if (event.key !== "Enter" || event.nativeEvent.isComposing || event.keyCode === 229) return;
                  event.preventDefault();
                  addPath();
                }} placeholder={t("文件夹的完整路径", "Full folder path")} ref={path} spellCheck={false} value={pathInput} /></label>
              <p>{t("输入运行 Universal-Code 的电脑上的文件夹路径。", "Enter the folder path on the computer running Universal-Code.")}</p>
              <button disabled={!pathInput.trim() || busy} onClick={addPath} type="button"><FolderPlus aria-hidden="true" size={16} />{t("添加", "Add")}</button>
            </div> : !directory && <><p>{native ? t("在此电脑上添加文件夹", "Add a folder on this computer")
              : t("添加项目的源文件夹", "Add the project's source folder")}</p>
              {canCreate && <button disabled={busy} onClick={() => { if (native) choosing.mutate(); else setEditingPath(true); }} type="button">
                {choosing.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={16} /> : <FolderPlus aria-hidden="true" size={17} />}{t("添加", "Add")}</button>}</>}
          {displayedDirectory && !editingPath && <><FolderOpen aria-hidden="true" size={22} /><div className="v2-project-source-path">
            <strong>{displayedDirectory.replace(/[\\/]+$/, "").split(/[\\/]/).at(-1)}</strong><span>{displayedDirectory}</span>
          </div>{!workspace && <button disabled={busy} onClick={() => {
            if (native) choosing.mutate(); else { setPathInput(directory); setEditingPath(true); }
          }} type="button">{t("更换", "Change")}</button>}</>}
        </div>
        {!workspace && !canCreate && <p className="v2-notice" role="status">{t("此连接尚未启用目录导入。在服务启动命令中加入", "Directory import is disabled. Add")}
          <code> --enable-workspace-import</code>{t("，并使用控制令牌重新连接。", " to the service startup command and reconnect with a control token.")}</p>}
        {workspace && <p className="v2-project-folder-note">{t("修改名称后，项目内的聊天和源文件夹保持原位。", "Renaming keeps the project's chats and source folder in place.")}</p>}
        {choosing.isError && <p className="v2-inline-error" role="alert">{t("未能选择文件夹，请重试。", "Could not select a folder. Try again.")}</p>}
        {saving.isError && <p className="v2-inline-error" role="alert">{error === "CONFLICT" ? t("这个项目名称已被使用，请换一个名称。", "This project name is already in use. Choose another name.")
          : error === "INVALID_ARGUMENT" ? t("请检查项目名称和文件夹路径，目录必须已存在。", "Check the project name and folder path. The folder must exist.")
            : error === "POLICY_DENIED" || error === "UNAUTHENTICATED" ? t("此连接无法修改项目，请使用控制令牌重新连接。", "This connection cannot change projects. Reconnect with a control token.")
              : t("项目未能保存，请检查连接后重试。填写的内容已保留。", "Could not save the project. Check the connection and retry. Your input has been kept.")}</p>}
      </div>
      <footer><button disabled={busy} onClick={close} type="button">{t("取消", "Cancel")}</button>
        <button className="primary" disabled={busy || nameBytes === 0 || nameBytes > 128 || (workspace ? !client.hasControl : !canCreate || !directory || editingPath)}
          type="submit">{saving.isPending ? t("正在保存…", "Saving…") : workspace ? t("保存更改", "Save changes") : t("创建项目", "Create project")}</button></footer>
    </form>
  </section></div>, document.body);
}
