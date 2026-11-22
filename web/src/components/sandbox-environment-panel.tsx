import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, RefreshCw, RotateCw } from "lucide-react";
import { APIRequestError, type APIClient } from "../api/client";
import type { SandboxEnvironmentControlRequestView, SandboxEnvironmentSettingsView } from "../api/types";
import { sandboxEnvironmentQueryKey, sandboxSettingsMatch, validSandboxSettings } from "../api/sandbox-environment";
import { desktopErrorMessage, desktopSandboxRestartEnabled, restartDesktopWithSandboxSettings } from "../lib/desktop-bridge";
import { useLocale } from "../lib/locale";
import { SandboxBackendSelector, sandboxBackendLabels } from "./sandbox-backend-selector";
import "./sandbox-environment-panel.css";

const localDefaults: SandboxEnvironmentSettingsView = {
  default_backend: "local", docker_enabled: false, docker_image_digest: "", sbx_enabled: false, sbx_template: "",
};
const saveIntentKey = ["sandbox", "environment-save-intent"] as const;
interface SaveIntent {
  body: SandboxEnvironmentControlRequestView;
  state: "pending" | "unknown" | "confirmed" | "conflict" | "rejected";
  error?: string;
  status?: number;
}

export function SandboxEnvironmentPanel({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const edited = useRef(false);
  const [draft, setDraft] = useState<SandboxEnvironmentSettingsView>(localDefaults);
  const [restarting, setRestarting] = useState(false);
  const environment = useQuery({ queryKey: sandboxEnvironmentQueryKey,
    queryFn: ({ signal }) => client.getSandboxEnvironment(signal), retry: false });
  const intent = useQuery<SaveIntent | null>({ queryKey: saveIntentKey, queryFn: () => null,
    enabled: false, initialData: null, gcTime: Infinity });
  const pending = intent.data?.state === "pending";
  const unknown = intent.data?.state === "unknown";
  const conflict = intent.data?.state === "conflict";
  const value = environment.data;
  useEffect(() => {
    if (intent.data && intent.data.state !== "confirmed") setDraft(intent.data.body.settings);
    else if (!edited.current && value) setDraft(value.settings);
  }, [intent.data, value]);
  const save = useMutation({ mutationFn: (body: SandboxEnvironmentControlRequestView) => client.saveSandboxEnvironment(body),
    onMutate: () => queries.cancelQueries({ queryKey: sandboxEnvironmentQueryKey }),
    onSuccess: (result, body) => {
      queries.setQueryData(saveIntentKey, { body, state: "confirmed" });
      queries.setQueryData(sandboxEnvironmentQueryKey, result);
      setDraft(result.settings); edited.current = false;
      void queries.invalidateQueries({ queryKey: sandboxEnvironmentQueryKey });
    },
    onError: (error, body) => {
      const conflict = error instanceof APIRequestError && error.status === 409;
      const rejected = error instanceof APIRequestError && [400, 401, 403, 404, 412, 413, 422].includes(error.status);
      queries.setQueryData(saveIntentKey, { body, state: conflict ? "conflict" : rejected ? "rejected" : "unknown", error: error.message,
        ...(error instanceof APIRequestError ? { status: error.status } : {}) });
    },
  });
  const restart = useMutation({ mutationFn: () => restartDesktopWithSandboxSettings(),
    onSuccess: (result) => setRestarting(result.status === "restarting") });
  const locked = pending || unknown || conflict || restarting || restart.isPending;
  const change = (next: SandboxEnvironmentSettingsView) => {
    const current = queries.getQueryData<SaveIntent | null>(saveIntentKey);
    if (locked || current?.state === "pending" || current?.state === "unknown" || current?.state === "conflict") return;
    edited.current = true; setDraft(next);
    if (current?.state === "confirmed" || current?.state === "rejected") queries.setQueryData(saveIntentKey, null);
  };
  const submit = (body: SandboxEnvironmentControlRequestView) => {
    const current = queries.getQueryData<SaveIntent | null>(saveIntentKey);
    if (!client.hasControl || current?.state === "pending" || restarting ||
      (current?.state === "unknown" && JSON.stringify(current.body) !== JSON.stringify(body))) return;
    queries.setQueryData(saveIntentKey, { body, state: "pending" });
    save.mutate(body);
  };
  const reread = async () => {
    const result = await environment.refetch();
    if (!result.data || result.isError) return;
    queries.setQueryData(saveIntentKey, null);
    edited.current = false; setDraft(result.data.settings);
  };
  const dirty = Boolean(value && !sandboxSettingsMatch(draft, value.settings));
  const valid = validSandboxSettings(draft);
  const canRestart = desktopSandboxRestartEnabled();
  return <section aria-label={t("执行环境设置", "Execution environment settings")} className="sandbox-environment-panel">
    <div className="section-heading"><h2>{t("执行环境", "Execution environment")}</h2>
      <button className="compact-command" disabled={environment.isFetching || pending || restarting} onClick={() => void environment.refetch()} type="button">
        <RefreshCw aria-hidden="true" size={14} />{t("检测环境", "Check environments")}</button></div>
    {environment.isPending && <p role="status">{t("正在检测本机执行环境…", "Checking local execution environments…")}</p>}
    {environment.isError && <p role="alert">{t("执行环境检测失败。", "Couldn’t check environments. ")}{environment.error.message}</p>}
    {value && <>
      <div aria-label={t("当前进程环境", "Current environments")} className="sandbox-environment-observations">
        {value.backends.map((backend) => <article key={backend.backend}>
          <strong>{sandboxBackendLabels[backend.backend]}</strong>
          <span>{value.probe_status === "not_checked" ? t("检测待完成", "Check pending") : backend.ready ? t("当前进程已就绪", "Ready")
            : backend.status === "disabled" ? t("当前进程待启用", "Disabled") : backend.status === "configuration_required" ? t("需要配置固定镜像", "Pinned image required") : t("当前环境待准备", "Setup required")}</span>
          {value.probe_status === "checked" && !backend.ready && <p>{backend.installed ? t("已检测到本机安装", "Installed locally") : t("尚未检测到可用安装", "No installation detected")}</p>}
          {backend.blockers.map((blocker) => <p key={blocker.code}>{blocker.message}</p>)}
        </article>)}
      </div>
      <fieldset disabled={locked || !client.hasControl} className="sandbox-environment-form">
        <legend>{t("保存下次启动设置", "Settings for the next launch")}</legend>
        <p>{t("默认环境仅用于尚未配置编码环境的任务。", "The default applies to tasks without a configured coding environment.")}</p>
        <SandboxBackendSelector value={draft.default_backend} disabled={locked || !client.hasControl}
          onChange={(backend) => change({ ...draft, default_backend: backend })} />
        <div className="sandbox-environment-fields">
          <label className="sandbox-environment-toggle"><input checked={draft.docker_enabled} onChange={(event) => change({ ...draft, docker_enabled: event.currentTarget.checked })} type="checkbox" />{t("启用 Docker Engine", "Enable Docker Engine")}</label>
          <label>{t("Docker 固定镜像摘要", "Pinned Docker image digest")}<input autoComplete="off" maxLength={71} onChange={(event) => change({ ...draft, docker_image_digest: event.currentTarget.value })}
            placeholder="sha256:…" spellCheck={false} value={draft.docker_image_digest} /></label>
          <p>{t("填写已审阅的 Linux 工具链镜像的完整 SHA-256 摘要。", "Enter the full SHA-256 digest of the reviewed Linux toolchain image.")}</p>
          <label className="sandbox-environment-toggle"><input checked={draft.sbx_enabled} onChange={(event) => change({ ...draft, sbx_enabled: event.currentTarget.checked })} type="checkbox" />{t("启用 Docker Sandboxes (sbx)", "Enable Docker Sandboxes (sbx)")}</label>
          <label>{t("sbx 固定模板", "Pinned sbx template")}<input autoComplete="off" maxLength={512} onChange={(event) => change({ ...draft, sbx_template: event.currentTarget.value })}
            placeholder="registry/repository@sha256:…" spellCheck={false} value={draft.sbx_template} /></label>
          <p>{t("sbx 0.47.0 需要登录专用 traverse-runtime 命名空间，并使用含完整 SHA-256 摘要的 OCI 模板。", "sbx 0.47.0 requires sign-in to the traverse-runtime namespace and an OCI template with a full SHA-256 digest.")}</p>
        </div>
        {!valid && <p role="alert">{t("请启用选作默认的后端，并核对固定镜像摘要或 sbx 模板格式。", "Enable the default backend and check the image digest or sbx template format.")}</p>}
        <button className="command-button primary" disabled={!dirty || !valid || locked || !client.hasControl}
          onClick={() => submit({ version: "sandbox_environment.v1", expected_revision: value.revision, settings: { ...draft } })} type="button">
          {pending && <LoaderCircle aria-hidden="true" className="spin" size={14} />}{t("保存执行环境设置", "Save execution environments")}</button>
      </fieldset>
      {!client.hasControl && <p>{t("保存需要桌面控制权限。", "Saving requires a desktop control connection.")}</p>}
      {intent.data?.state === "confirmed" && <p role="status">{t("执行环境设置已保存。", "Execution environment settings saved.")}</p>}
      {pending && <p role="status">{t("正在保存设置…", "Saving settings…")}</p>}
      {unknown && <div role="alert"><p>{t("保存结果待确认。", "Save unconfirmed. ")}{intent.data?.error}</p>
        <button disabled={!client.hasControl || pending} onClick={() => submit(intent.data!.body)} type="button">{t("核对保存结果", "Check save result")}</button></div>}
      {conflict && <div role="alert"><p>{t("执行环境设置已被修改。重新读取当前配置，核对后再保存。", "Settings changed. Reload the configuration before saving again. ")}{intent.data?.error}</p>
        <button disabled={environment.isFetching} onClick={() => void reread()} type="button">{t("重新读取配置", "Reload configuration")}</button></div>}
      {intent.data?.state === "rejected" && <p role="alert">{t("设置未保存，请核对配置和连接权限后重试。", "Settings weren’t saved. Check configuration and access, then retry. ")}{intent.data.error}</p>}
      {intent.data?.state === "rejected" && intent.data.status === 412 && <p>{t("请重新检测环境。如仍无法保存，请重启应用。", "Check environments again. If saving still fails, restart the app.")}</p>}
      {value.restart_required && <div className="sandbox-environment-restart" role="status"><p>{t("重启后应用已保存的执行环境。", "Restart to apply the saved execution environments.")}</p>
        {canRestart ? <button disabled={locked || dirty || environment.isFetching} onClick={() => restart.mutate()} type="button">
          <RotateCw aria-hidden="true" size={14} />{restarting ? t("正在重启应用…", "Restarting…") : t("重启并读取执行环境设置", "Restart to apply execution environments")}</button>
          : <p>{t("请关闭并重新打开桌面应用。", "Close and reopen the desktop app.")}</p>}
        {restart.isError && <p role="alert">{t("重启失败。", "Restart failed. ")}{desktopErrorMessage(restart.error)}</p>}
      </div>}
      <details><summary>{t("查看配置与检测记录", "Configuration and check results")}</summary><pre>{JSON.stringify(value, null, 2)}</pre></details>
    </>}
  </section>;
}
