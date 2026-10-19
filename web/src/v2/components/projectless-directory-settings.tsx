import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FolderOpen } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ProjectlessDirectory } from "../../api/types";
import { chooseDesktopProjectlessDirectory, desktopBridgeAvailable, desktopProjectlessDirectoryEnabled } from "../../lib/desktop-bridge";
import { useLocale } from "../../lib/locale";

export const projectlessDirectoryQueryKey = ["v2", "projectless-directory"] as const;

export function V2ProjectlessDirectorySettings({ client }: { client: APIClient }) {
  return client.hasProjectlessWorkspaces ? <ProjectlessDirectorySettings client={client} /> : null;
}

function ProjectlessDirectorySettings({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const inputID = useId();
  const [editing, setEditing] = useState(false);
  const [path, setPath] = useState("");
  const [saved, setSaved] = useState(false);
  const native = desktopProjectlessDirectoryEnabled();
  const canImport = !desktopBridgeAvailable() && client.hasWorkspaceImport;
  const current = useQuery({ queryKey: projectlessDirectoryQueryKey,
    queryFn: ({ signal }) => client.getProjectlessDirectory(signal),
    enabled: client.hasProjectlessWorkspaces === true, retry: false });
  const update = useMutation({
    mutationFn: async (directory: string | undefined): Promise<ProjectlessDirectory | null> => {
      if (native) return chooseDesktopProjectlessDirectory();
      return client.setProjectlessDirectory(directory!);
    },
    onMutate: () => setSaved(false),
    onSuccess: async (settings) => {
      if (!settings) return;
      await queries.cancelQueries({ queryKey: projectlessDirectoryQueryKey });
      queries.setQueryData(projectlessDirectoryQueryKey, settings);
      setEditing(false);
      setPath("");
      setSaved(true);
    },
  });
  return <section className="v2-settings-section v2-projectless-directory" aria-labelledby={`${inputID}-heading`}>
    <h2 id={`${inputID}-heading`}>{t("本地聊天目录", "Local chat directory")}</h2>
    <div className="v2-settings-card">
      <div className="v2-setting-row">
        <div><strong>{t("默认工作目录", "Default working directory")}</strong>
          <span>{t("每个新建本地聊天使用独立文件夹。已有聊天和当前草稿保留原目录，聊天记录由应用统一保存。", "Each new local chat gets its own folder. Existing chats and the current draft keep their directories, and the app stores chat history centrally.")}</span>
          {current.data && !current.isError ? <code className="v2-projectless-path">{current.data.directory_path}</code>
            : current.isPending ? <span>{t("正在读取目录…", "Loading directory…")}</span> : null}
        </div>
        <div className="v2-setting-value"><button disabled={update.isPending || (!native && !canImport)} type="button"
          onClick={() => { update.reset(); setSaved(false); if (native) update.mutate(undefined); else setEditing(true); }}>
          <FolderOpen aria-hidden="true" size={14} />{update.isPending ? t("正在更改…", "Changing…") : t("更改目录", "Change directory")}
        </button></div>
      </div>
      {editing && canImport && <form className="v2-projectless-form" onSubmit={(event) => {
        event.preventDefault(); if (path.trim() && !update.isPending) update.mutate(path.trim());
      }}>
        <p>{t("输入运行 Universal-Code 服务的电脑上已存在的文件夹完整路径。", "Enter the full path to an existing folder on the computer running the Universal-Code service.")}</p>
        <label htmlFor={inputID}>{t("服务电脑上的目录", "Directory on the service computer")}</label>
        <input autoComplete="off" disabled={update.isPending} id={inputID} maxLength={4096}
          onChange={(event) => setPath(event.target.value)} required spellCheck={false} value={path} />
        <div className="v2-setting-value"><button disabled={update.isPending} onClick={() => setEditing(false)} type="button">{t("取消", "Cancel")}</button>
          <button disabled={!path.trim() || update.isPending} type="submit">{t("使用此目录", "Use this directory")}</button></div>
      </form>}
    </div>
    {!native && !canImport && <p className="v2-settings-hint">{t("此连接尚未启用目录选择。", "Directory selection is not enabled for this connection.")}</p>}
    {current.isError && <><p className="v2-inline-error" role="alert">{t("无法读取目录：", "Could not load the directory: ")}{current.error.message}</p>
      <button className="v2-setting-link" onClick={() => void current.refetch()} type="button">{t("重新读取目录", "Reload directory")}</button></>}
    {update.isError && <p className="v2-inline-error" role="alert">{t("目录未能更改：", "Could not change the directory: ")}{update.error.message}</p>}
    {saved && <p className="v2-settings-hint" role="status">{t("默认目录已更新，将用于新建的本地聊天。", "The default directory has been updated for new local chats.")}</p>}
  </section>;
}
