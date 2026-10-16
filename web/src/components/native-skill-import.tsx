import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import { useLocale } from "../lib/locale";

export function NativeSkillImport({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const operationKey = useRef("");
  const [files, setFiles] = useState<File[]>([]);
  const [confirmed, setConfirmed] = useState(false);
  const inventory = useQuery({ queryKey: ["extensions", "", ""],
    queryFn: ({ signal }) => client.extensionInventory("", signal), enabled: client.hasSkillInstallation });
  const enabled = Boolean(client.hasSkillInstallation && inventory.data?.onboarding?.native_skill_directory_import);
  const rootName = files[0]?.webkitRelativePath.split("/")[0] ?? "";
  const totalBytes = files.reduce((total, file) => total + file.size, 0);
  const install = useMutation({
    mutationFn: async () => {
      if (!enabled) throw new Error(t("当前服务未开放原生 Skill 目录导入。", "Native Skill directory import is unavailable on this service."));
      if (!rootName || files.length === 0 || files.length > 1024 || totalBytes > 8 * 1024 * 1024 ||
        files.some((file) => !file.webkitRelativePath.startsWith(`${rootName}/`))) {
        throw new Error(t("请选择一个目录，最多 1024 个文件、合计 8 MiB。", "Choose one directory with up to 1024 files and 8 MiB total."));
      }
      const entries = await Promise.all(files.map(async (file) => {
        const bytes = new Uint8Array(await file.arrayBuffer());
        let binary = "";
        for (let offset = 0; offset < bytes.length; offset += 0x8000) binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
        return { path: file.webkitRelativePath.slice(rootName.length + 1), content_base64: btoa(binary) };
      }));
      return client.installSkillPackage({ version: "plugin-installation.v2", surface: "code", confirm_untrusted: true,
        directory: { root_name: rootName, files: entries },
      }, operationKey.current);
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["extensions"] }),
  });
  return <div className="settings-web-skill">
    <p>{t("选择含 SKILL.md 的原生 Skill 目录，连同所需资源一起上传。服务端解析内容并暂存，随后在“扩展与代码智能”中审查、启用，再在任务中选择。",
      "Upload a native Skill directory containing SKILL.md and its resources. The service parses and stages it. Review and enable it in Extensions, then select it in your task.")}</p>
    {!enabled && <p role="status">{inventory.isLoading ? t("正在确认目录导入能力…", "Checking directory import support…") :
      t("当前服务或连接未开放原生 Skill 目录导入，请检查服务版本与控制权限。", "Native Skill directory import is unavailable. Check the service version and control access.")}</p>}
    <input hidden multiple type="file" aria-label={t("选择原生 Skill 目录", "Choose native Skill directory")}
      ref={(node) => { input.current = node; node?.setAttribute("webkitdirectory", ""); }}
      disabled={!enabled || install.isPending} onChange={(event) => {
        const selected = Array.from(event.target.files ?? []);
        if (selected.length) {
          setFiles(selected);
          setConfirmed(false);
          install.reset();
          operationKey.current = `web-skill-directory-${crypto.randomUUID()}`;
        }
        event.currentTarget.value = "";
      }} />
    <button className="settings-action" disabled={!enabled || install.isPending}
      onClick={() => input.current?.click()} type="button">{t("选择原生 Skill 目录", "Choose native Skill directory")}</button>
    {files.length > 0 && <div className="shared-skill-confirmation">
      <p>{rootName} · {files.length} {t("个文件", "files")} · {totalBytes} bytes</p>
      <details><summary>{t("查看待上传文件", "Inspect selected files")}</summary><ul>
        {files.map((file) => <li key={file.webkitRelativePath}>{file.webkitRelativePath} · {file.size} bytes</li>)}
      </ul></details>
      <label><input checked={confirmed} type="checkbox" disabled={install.isPending || install.isSuccess}
        onChange={(event) => setConfirmed(event.target.checked)} />
        {t("确认上传这些文件并以不受信任状态暂存到 Code", "Upload these files and stage them as untrusted in Code")}</label>
      <button className="settings-action" type="button" disabled={!enabled || !confirmed || install.isPending || install.isSuccess}
        onClick={() => install.mutate()}>{install.isPending ? t("正在暂存…", "Staging…") : t("暂存原生 Skill", "Stage native Skill")}</button>
    </div>}
    {install.data && "installation" in install.data && <p role="status">
      {t("原生 Skill 当前状态", "Native Skill current state")}: {install.data.installation.state} · {install.data.installation.manifest.name}
    </p>}
    {install.error && <p className="inline-warning" role="alert">{install.error instanceof Error ? install.error.message : t("目录导入失败", "Directory import failed")}</p>}
  </div>;
}
