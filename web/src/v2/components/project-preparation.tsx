import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { normalizePreparationTarget, projectPreparationIsActive, validProjectPreparationRequest,
  type ProjectPreparationOperation, type ProjectPreparationRequest, type ProjectPreparationView } from "../../api/project-preparation";
import { ErrorState } from "../../components/common";
import { useLocale } from "../../lib/locale";
import { useV2PersistentState, useV2RecoveryStore } from "../recovery-storage";
import { taskReviewKey } from "./task-overview";
import "./project-preparation.css";

interface PreparationAttempt {
  runID: string;
  key: string;
  body: ProjectPreparationRequest;
  operationID?: string;
  cancelKey?: string;
}
function validAttempt(value: unknown, runID: string): value is PreparationAttempt {
  if (!value || typeof value !== "object") return false;
  const attempt = value as Partial<PreparationAttempt>;
  return attempt.runID === runID && typeof attempt.key === "string" && /^project-preparation-[\w-]+$/u.test(attempt.key) &&
    validProjectPreparationRequest(attempt.body) &&
    (attempt.operationID === undefined || (typeof attempt.operationID === "string" && attempt.operationID.length > 0)) &&
    (attempt.cancelKey === undefined || (typeof attempt.cancelKey === "string" && /^project-preparation-cancel-[\w-]+$/u.test(attempt.cancelKey)));
}
const preparationKey = (client: APIClient, runID: string, target: string) =>
  ["run", runID, "project-preparation", client.baseURL, target] as const;
const operationKey = (client: APIClient, runID: string, operationID: string) =>
  ["run", runID, "project-preparation-operation", client.baseURL, operationID] as const;

function environmentLabel(backend: string, t: (chinese: string, english: string) => string): string {
  if (backend === "host" || backend === "host_unsandboxed") return t("本机", "Host");
  if (backend === "local" || backend === "local_sandbox" || backend === "local_windows_lpac") return t("本地沙箱", "Local sandbox");
  if (backend === "docker" || backend === "docker_sandbox" || backend === "docker_standard_code") return "Docker";
  if (backend === "sbx" || backend === "sbx_sandbox" || backend === "docker_sandboxes") return "Docker Sandboxes (sbx)";
  return backend;
}

export interface ProjectPreparationPanelProps {
  view: ProjectPreparationView;
  operation?: ProjectPreparationOperation;
  disabled?: boolean;
  busy?: boolean;
  uncertain?: boolean;
  operationReadFailed?: boolean;
  onPrepare: (selection: { allowedSources: string[]; runInstallScripts: boolean }) => void;
  onCancel: () => void;
  onReconcile: () => void;
  onReadOperation: () => void;
}

/** Displays the Go-owned plan; a click passes the reviewed selections to its controller. */
export function ProjectPreparationPanel({ view, operation, disabled = false, busy = false, uncertain = false,
  operationReadFailed = false, onPrepare, onCancel, onReconcile, onReadOperation }: ProjectPreparationPanelProps) {
  const { t } = useLocale();
  const [allowedSources, setAllowedSources] = useState<string[]>([]);
  const [runInstallScripts, setRunInstallScripts] = useState(false);
  const active = operation ? projectPreparationIsActive(operation.state) : false;
  const blocked = disabled || busy || active || uncertain || operationReadFailed;
  const backend = environmentLabel(view.backend, t);
  const installCommand = runInstallScripts
    ? view.plan.install_command + "\nnpm rebuild --offline" : view.plan.install_command;
  const stateLabel = (state: string) => ({
    pending: t("等待开始", "Waiting to start"), downloading: t("正在获取依赖", "Fetching dependencies"),
    installing: t("正在安装依赖", "Installing dependencies"), ready: t("项目已准备", "Project prepared"),
    failed: t("准备失败", "Preparation failed"), cancelled: t("已取消准备", "Preparation cancelled"),
    interrupted: t("准备已中断", "Preparation interrupted"),
  })[state] ?? state;
  const fetchComplete = operation && operation.completed_packages === operation.total_packages;
  const installComplete = operation?.state === "ready";
  const terminalFailure = operation && ["failed", "cancelled", "interrupted"].includes(operation.state);

  return <div className="v2-preparation-plan">
    <dl className="v2-preparation-facts">
      <div><dt>{t("执行环境", "Environment")}</dt><dd>{backend || t("尚未配置", "Not configured")}</dd></div>
      <div><dt>{t("工具链", "Toolchain")}</dt><dd>{view.plan.toolchain || t("尚未检测到", "Not detected")}
        {view.plan.requested_version && <small>{t("项目要求：", "Project requirement: ")}{view.plan.requested_version}</small>}</dd></div>
      <div><dt>{t("依赖缓存", "Dependency cache")}</dt><dd>{view.cache.status === "ready" ? t("可直接复用", "Ready to reuse")
        : view.cache.status === "partial" ? t("部分可复用", "Partially cached") : t("尚无缓存", "Empty")}
        <small>{t("已缓存 ", "Cached ")}{view.cache.cached_packages} / {view.cache.total_packages} {t("个包", "packages")}</small></dd></div>
    </dl>
    <details className="v2-preparation-sources">
      <summary>{t("配置与锁文件", "Configuration and lockfiles")}</summary>
      {view.plan.files.length > 0 ? <ul>{view.plan.files.map((file) => <li key={file.path}><code>{file.path}</code></li>)}</ul>
        : <p>{t("此目录未检测到支持的项目文件。", "No supported project files were detected in this directory.")}</p>}
    </details>
    <div className="v2-preparation-commands" aria-label={t("检测出的项目命令", "Detected project commands")}>
      <div><span>{t("安装依赖", "Install dependencies")}</span><code>{installCommand || t("未检测到安装命令", "No install command detected")}</code></div>
      <div><span>{t("运行测试", "Run tests")}</span><code>{view.plan.test_command || t("未检测到测试命令", "No test command detected")}</code></div>
    </div>
    {view.plan.issues.length > 0 && <div role="alert" className="v2-preparation-issues"><ul>{view.plan.issues.map((issue) => <li key={issue}>{issue}</li>)}</ul></div>}
    {view.reason && <p className="v2-delivery-muted">{view.reason}</p>}
    <fieldset className="v2-preparation-downloads" disabled={blocked || !view.available}>
      <legend>{t("本次准备的依赖下载", "Dependency downloads for this preparation")}</legend>
      <p>{t("勾选允许下载的依赖源。留空时使用已有缓存。授权按本次锁文件生效，网页搜索使用任务中的独立设置。",
        "Select the package sources to allow. Leave them unchecked to use existing cache. Authorization applies to this lockfile; web search uses the task's separate setting.")}</p>
      {view.plan.sources.length > 0 ? view.plan.sources.map((source) => <label key={source}>
        <input type="checkbox" checked={allowedSources.includes(source)} onChange={(event) => setAllowedSources((current) =>
          event.target.checked ? [...current, source] : current.filter((item) => item !== source))} />
        <span>{t("允许下载：", "Allow downloads: ")}<code>{source}</code></span>
      </label>) : <p>{t("此项目没有需要下载的依赖源。", "This project has no package sources to download from.")}</p>}
      {view.plan.ecosystem === "npm" && <label className="v2-preparation-scripts"><input type="checkbox" checked={runInstallScripts}
        onChange={(event) => setRunInstallScripts(event.target.checked)} />
        <span>{t("运行依赖的安装脚本（在", "Run dependency install scripts (in ")}{backend}{t("中）", ")")}</span></label>}
    </fieldset>
    <div className="v2-preparation-actions">
      <button type="button" disabled={blocked || !view.available} onClick={() => onPrepare({ allowedSources, runInstallScripts })}>
        {terminalFailure ? t("重新准备项目", "Prepare project again") : t("准备项目", "Prepare project")}</button>
      {allowedSources.length === 0 && <span>{t("使用缓存准备", "Prepare from cache")}</span>}
      {uncertain && <button type="button" disabled={disabled || busy} onClick={onReconcile}>{t("确认上次准备", "Check previous preparation")}</button>}
    </div>
    {uncertain && <p role="status">{t("上次准备请求的结果尚未确认。确认时会核对原请求。", "The previous preparation outcome is unconfirmed. Checking uses the original request.")}</p>}
    {operationReadFailed && <div role="alert"><p>{t("无法读取准备进度。", "Could not read preparation progress.")}</p>
      <button type="button" onClick={onReadOperation}>{t("重试读取进度", "Retry reading progress")}</button></div>}
    {operation && <section className="v2-preparation-progress" aria-label={t("项目准备进度", "Project preparation progress")}>
      <div className="v2-delivery-heading"><h3 role="status" aria-live="polite">{stateLabel(operation.state)}</h3>
        {operation.can_cancel && <button type="button" disabled={disabled || busy} onClick={onCancel}>{t("取消准备", "Cancel preparation")}</button>}</div>
      <ol>
        <li><span>{t("确认项目与锁文件", "Review project and lockfiles")}</span><strong>{t("已确认", "Reviewed")}</strong></li>
        <li><span>{t("获取依赖", "Fetch dependencies")}</span><strong>{operation.completed_packages} / {operation.total_packages}
          {fetchComplete ? t("，已完成", ", complete") : operation.state === "downloading" ? t("，获取中", ", fetching") : ""}</strong></li>
        <li><span>{t("在当前环境安装", "Install in the selected environment")}</span><strong>{installComplete ? t("已完成", "Complete")
          : operation.state === "installing" ? t("安装中", "Installing") : operation.job ? stateLabel(operation.state) : t("待安装", "Waiting")}</strong></li>
      </ol>
      {operation.total_packages > 0 && <progress aria-label={t("依赖获取进度", "Dependency fetch progress")} max={operation.total_packages} value={operation.completed_packages} />}
      <p className="v2-delivery-muted">{t("复用 ", "Reused ")}{operation.cache_hits}{t(" 个缓存包；下载 ", " cached packages; downloaded ")}
        {new Intl.NumberFormat().format(operation.downloaded_bytes)} {t("字节", "bytes")}</p>
      {operation.error && <p role="alert">{operation.error}</p>}
      {operation.job && <details className="v2-preparation-output" open={operation.state === "failed"}>
        <summary>{t("安装命令输出", "Install command output")}{operation.job.exit_code !== undefined
          ? ` (${t("退出码", "exit code")} ${operation.job.exit_code})` : ""}</summary>
        <pre>{operation.job.stdout || t("暂无标准输出。", "No standard output yet.")}</pre>
        {operation.job.stderr && <pre aria-label={t("标准错误", "Standard error")}>{operation.job.stderr}</pre>}
        {(operation.job.stdout_truncated || operation.job.stderr_truncated) && <p>{t("此处显示保留的部分输出。", "The retained portion of output is shown.")}</p>}
      </details>}
      <details><summary>{t("准备记录", "Preparation record")}</summary><p>{t("准备：", "Preparation: ")}<code>{operation.id}</code></p>
        <p>Run <code>{operation.run_id}</code></p>{operation.job && <p>Job <code>{operation.job.id}</code></p>}</details>
    </section>}
  </div>;
}

export function ProjectPreparation(props: { client: APIClient; runID: string; threadID: string; working?: boolean }) {
  return <ProjectPreparationForRun key={props.client.baseURL + ":" + props.runID} {...props} />;
}

function ProjectPreparationForRun({ client, runID, threadID, working = false }: {
  client: APIClient; runID: string; threadID: string; working?: boolean;
}) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const store = useV2RecoveryStore();
  const storageKey = "run:" + runID + ":project-preparation";
  const [saved, setSaved] = useV2PersistentState<PreparationAttempt | null>(storageKey, null);
  const attempt = validAttempt(saved, runID) ? saved : null;
  const damaged = saved !== null && !attempt;
  const [target, setTarget] = useState(attempt?.body.target ?? ".");
  const [draftTarget, setDraftTarget] = useState(target);
  const [localError, setLocalError] = useState("");
  const submitting = useRef(false);
  const plan = useQuery({ queryKey: preparationKey(client, runID, target),
    queryFn: ({ signal }) => client.getProjectPreparation(runID, target, signal), retry: false });
  const latestID = (attempt?.body.target === target ? attempt.operationID : undefined) ?? plan.data?.latest?.id;
  const operation = useQuery({ queryKey: operationKey(client, runID, latestID ?? ""),
    queryFn: ({ signal }) => client.getProjectPreparationOperation(runID, latestID!, signal),
    enabled: Boolean(latestID), retry: false, refetchOnMount: "always",
    refetchInterval: (query) => query.state.data && projectPreparationIsActive(query.state.data.state) ? 1500 : false });
  const result = operation.data ?? plan.data?.latest;
  const uncertain = Boolean(attempt && !attempt.operationID);
  const active = Boolean(result && projectPreparationIsActive(result.state));
  const terminalID = result && !projectPreparationIsActive(result.state) ? result.id : undefined;
  const baseURL = client.baseURL;
  useEffect(() => {
    if (!terminalID) return;
    void queries.invalidateQueries({ queryKey: ["run", runID, "project-preparation", baseURL] });
    void queries.invalidateQueries({ queryKey: ["run", runID, "project-actions", baseURL] });
    void queries.invalidateQueries({ queryKey: taskReviewKey(threadID) });
  }, [terminalID, baseURL, runID, threadID, queries]);
  const saveAttempt = (value: PreparationAttempt | null) => {
    store?.write(storageKey, value);
    setSaved(value);
  };
  const prepare = useMutation({ retry: false,
    mutationFn: (input: PreparationAttempt) => client.prepareProject(input.runID, input.body, input.key),
    onSuccess: (view, input) => {
      queries.setQueryData(preparationKey(client, input.runID, input.body.target), view);
      if (view.latest) queries.setQueryData(operationKey(client, input.runID, view.latest.id), view.latest);
      try { saveAttempt({ ...input, operationID: view.latest!.id }); }
      catch (error) { setLocalError(error instanceof Error ? error.message : t("无法保存准备记录。", "Could not save the preparation record.")); }
    },
    onError: (error) => {
      if (error instanceof APIRequestError && error.status >= 400 && error.status < 500) {
        try { saveAttempt(null); } catch { /* Recovery storage reports the failure. */ }
      }
    },
    onSettled: () => { submitting.current = false; void plan.refetch(); },
  });
  const cancel = useMutation({ retry: false,
    mutationFn: (input: { id: string; key: string }) => client.cancelProjectPreparation(runID, input.id, input.key),
    onSuccess: (value) => queries.setQueryData(operationKey(client, runID, value.id), value),
    onSettled: () => { void operation.refetch(); },
  });
  const disabled = working || damaged || !client.hasControl || !client.hasRunExecution;
  function submit(input: PreparationAttempt) {
    if (submitting.current || disabled) return;
    setLocalError("");
    try { saveAttempt(input); }
    catch (error) { setLocalError(error instanceof Error ? error.message : t("无法保存准备请求。", "Could not save the preparation request.")); return; }
    submitting.current = true;
    prepare.mutate(input);
  }
  function stop() {
    if (!result?.can_cancel || disabled || cancel.isPending) return;
    const key = attempt?.operationID === result.id && attempt.cancelKey
      ? attempt.cancelKey : "project-preparation-cancel-" + result.id;
    if (attempt?.operationID === result.id) {
      try { saveAttempt({ ...attempt, cancelKey: key }); }
      catch (error) { setLocalError(error instanceof Error ? error.message : t("无法保存取消请求。", "Could not save the cancellation request.")); return; }
    }
    cancel.mutate({ id: result.id, key });
  }
  return <section className="v2-task-delivery v2-project-preparation" aria-label={t("准备项目", "Prepare project")}>
    <div className="v2-delivery-heading"><h2>{t("准备项目", "Prepare project")}</h2>
      <button type="button" disabled={plan.isFetching} onClick={() => void plan.refetch()}>{t("刷新检测", "Refresh detection")}</button></div>
    <p className="v2-delivery-muted">{t("检测项目工具链和锁文件，获取依赖后在所选环境安装。准备完成后可运行下方的项目测试。",
      "Detect the toolchain and lockfiles, then fetch and install dependencies in the selected environment. Run the project tests below when preparation finishes.")}</p>
    <form className="v2-preparation-target" onSubmit={(event) => {
      event.preventDefault();
      let next: string;
      try { next = normalizePreparationTarget(draftTarget.trim() || "."); }
      catch (error) { setLocalError(error instanceof Error ? error.message : t("请输入项目内的相对目录。", "Enter a relative directory within the project.")); return; }
      setLocalError("");
      setDraftTarget(next);
      if (next === target) void plan.refetch(); else setTarget(next);
    }}><label>{t("项目目录", "Project directory")}<input value={draftTarget} disabled={active || uncertain || prepare.isPending}
      onChange={(event) => setDraftTarget(event.target.value)} placeholder=". / web" />
      <small>{t("填写项目内的相对目录，留空使用根目录。", "Enter a relative directory within the project; leave empty for its root.")}</small></label>
      <button type="submit" disabled={active || uncertain || prepare.isPending || plan.isFetching}>{t("检测目录", "Inspect directory")}</button></form>
    {plan.isPending && <p role="status">{t("正在检测项目…", "Inspecting project…")}</p>}
    {plan.isError && <ErrorState error={plan.error} />}
    {working && <p role="status">{t("代理正在执行，暂停后可准备项目。", "The agent is working. Pause it to prepare the project.")}</p>}
    {(!client.hasControl || !client.hasRunExecution) && <p>{t("当前连接可查看准备记录，运行准备需要命令执行权限。", "This connection can view preparation records; preparing requires execution control.")}</p>}
    {damaged && <p role="alert">{t("保存的准备请求无法读取，请先核对准备记录。", "The saved preparation request could not be read. Check preparation records first.")}</p>}
    {localError && <p role="alert">{localError}</p>}
    {prepare.isError && <ErrorState error={prepare.error} />}
    {cancel.isError && <ErrorState error={cancel.error} />}
    {prepare.isPending && <p role="status">{t("正在提交准备请求…", "Submitting preparation…")}</p>}
    {uncertain && !plan.data && <div role="status"><p>{t("上次准备请求的结果尚未确认。", "The previous preparation outcome is unconfirmed.")}</p>
      <button type="button" disabled={disabled || prepare.isPending} onClick={() => { if (attempt) submit(attempt); }}>
        {t("确认上次准备", "Check previous preparation")}</button></div>}
    {plan.data && <ProjectPreparationPanel key={plan.data.plan.fingerprint} view={plan.data} operation={result}
      disabled={disabled || plan.isError} busy={prepare.isPending || cancel.isPending} uncertain={uncertain}
      operationReadFailed={operation.isError} onReadOperation={() => void operation.refetch()}
      onCancel={stop} onReconcile={() => { if (attempt) submit(attempt); }}
      onPrepare={(selection) => submit({ runID, key: "project-preparation-" + crypto.randomUUID(), body: {
        version: "project-preparation.v1", target, expected_fingerprint: plan.data!.plan.fingerprint,
        allowed_sources: selection.allowedSources, run_install_scripts: selection.runInstallScripts, confirm_execution: true,
      } })} />}
  </section>;
}
