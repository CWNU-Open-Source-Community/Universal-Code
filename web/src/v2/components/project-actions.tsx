import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { projectActionIsRunning, type ProjectActionSlot } from "../../api/project-actions";
import { ErrorState, StatusLabel } from "../../components/common";
import { useLocale } from "../../lib/locale";
import { useV2PersistentState, useV2RecoveryStore } from "../recovery-storage";
import { taskReviewKey } from "./task-overview";
import "./project-actions.css";

interface ActionAttempt {
  runID: string;
  slot: ProjectActionSlot;
  target: string;
  key: string;
  jobID?: string;
}
function validAttempt(value: unknown, runID: string): value is ActionAttempt {
  if (!value || typeof value !== "object") return false;
  const item = value as Partial<ActionAttempt>;
  return item.runID === runID && (item.slot === "test" || item.slot === "format") &&
    typeof item.target === "string" && typeof item.key === "string" &&
    /^project-action-[\w-]+$/u.test(item.key) && (item.jobID === undefined || typeof item.jobID === "string");
}
const actionKey = (client: APIClient, runID: string) => ["run", runID, "project-actions", client.baseURL] as const;
const jobKey = (client: APIClient, runID: string, jobID: string) => [...actionKey(client, runID), "job", jobID] as const;

export function ProjectActions(props: {
  client: APIClient; runID: string; threadID: string; working?: boolean;
  onFeedback?: (context: string) => void;
}) {
  return <ProjectActionsForRun key={props.client.baseURL + ":" + props.runID} {...props} />;
}

function ProjectActionsForRun({ client, runID, threadID, working = false, onFeedback }: {
  client: APIClient; runID: string; threadID: string; working?: boolean;
  onFeedback?: (context: string) => void;
}) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const store = useV2RecoveryStore();
  const storageKey = "run:" + runID + ":project-action";
  const [saved, setSaved] = useV2PersistentState<ActionAttempt | null>(storageKey, null);
  const attempt = validAttempt(saved, runID) ? saved : null;
  const damaged = saved !== null && !attempt;
  const [targets, setTargets] = useState({ test: "", format: "" });
  const [localError, setLocalError] = useState("");
  const submitting = useRef(false);
  const actions = useQuery({ queryKey: actionKey(client, runID),
    queryFn: ({ signal }) => client.listProjectActions(runID, signal), retry: false });
  const job = useQuery({ queryKey: jobKey(client, runID, attempt?.jobID ?? ""),
    queryFn: ({ signal }) => client.getProjectActionJob(runID, attempt!.jobID!, signal),
    enabled: Boolean(attempt?.jobID), retry: false, refetchOnMount: "always",
    refetchInterval: (query) => query.state.data && projectActionIsRunning(query.state.data.job.state) ? 1500 : false });
  const result = job.data;
  const terminalJobID = result && !projectActionIsRunning(result.job.state) ? result.job.id : undefined;
  const baseURL = client.baseURL;
  useEffect(() => {
    if (!terminalJobID) return;
    void queries.invalidateQueries({ queryKey: ["run", runID, "project-actions", baseURL], exact: true });
    void queries.invalidateQueries({ queryKey: taskReviewKey(threadID) });
  }, [terminalJobID, baseURL, runID, threadID, queries]);
  const active = Boolean(attempt && (!attempt.jobID || !result || projectActionIsRunning(result.job.state)));
  const saveAttempt = (value: ActionAttempt | null) => {
    store?.write(storageKey, value);
    setSaved(value);
  };
  const execute = useMutation({ retry: false,
    mutationFn: (input: ActionAttempt) => client.runProjectAction(input.runID, input.slot, input.target, input.key),
    onSuccess: (value, input) => {
      queries.setQueryData(jobKey(client, input.runID, value.job.id), value);
      try { saveAttempt({ ...input, jobID: value.job.id }); }
      catch (error) { setLocalError(error instanceof Error ? error.message : t("无法保存运行记录。", "Could not save the execution record.")); }
    },
    onError: (error) => {
      // Rejections before admission cannot have started a Job. Unknown outcomes
      // retain the original key so a retry reads the same execution.
      if (error instanceof APIRequestError && error.status >= 400 && error.status < 500) {
        try { saveAttempt(null); } catch { /* The persistence provider displays the storage error. */ }
      }
    },
    onSettled: () => {
      submitting.current = false;
      void queries.invalidateQueries({ queryKey: actionKey(client, runID), exact: true });
      void queries.invalidateQueries({ queryKey: taskReviewKey(threadID) });
    },
  });
  const stop = useMutation({ retry: false,
    mutationFn: (jobID: string) => client.stopThreadApplicationService(threadID, runID, jobID),
    onSettled: () => { void job.refetch(); },
  });
  function submit(input: ActionAttempt) {
    if (submitting.current || !client.hasControl || !client.hasRunExecution || working) return;
    setLocalError("");
    try { saveAttempt(input); }
    catch (error) { setLocalError(error instanceof Error ? error.message : t("无法保存运行请求。", "Could not save the execution request.")); return; }
    submitting.current = true;
    execute.mutate(input);
  }
  const backend = actions.data?.backend ?? result?.backend;
  const backendLabel = backend === "host" || backend === "host_unsandboxed" ? t("本机", "Host")
    : backend === "local" || backend === "local_sandbox" ? t("本地沙箱", "Local sandbox")
      : backend === "docker" || backend === "docker_sandbox" || backend === "docker_standard_code" ? "Docker" : backend;
  const failed = result && !projectActionIsRunning(result.job.state) &&
    (result.job.state !== "completed" || result.job.exit_code !== 0);
  const reasonLabel = (reason: string) => {
    if (reason === "rebuild the Standard Code Docker image to enable npm tests and gofmt")
      return t("请重建 Standard Code Docker 镜像，以启用 npm 测试和 Go 格式化。", reason);
    if (reason === "read-only project cannot be formatted") return t("项目设为只读，无法格式化文件。", reason);
    if (reason === "Run is not executable") return t("此执行已结束。返回对话继续任务后，可以运行项目动作。", reason);
    if (reason === "project actions require Code Deliver phase")
      return t("请先进入代码模式的交付阶段，再运行项目动作。", reason);
    if (reason === "command runtime is unavailable" || reason === "project action execution is disabled" ||
      reason === "selected project command runtime is unavailable")
      return t("当前连接的命令执行环境不可用。", reason);
    if (reason === "selected sandbox command runtime is not ready" || reason === "sandbox workspace is unavailable")
      return t("请先在任务设置中准备沙箱工作区。", reason);
    if (reason.startsWith("project command toolchain is unavailable: "))
      return t("当前环境未找到工具：", "Tool not found in this environment: ") + reason.slice("project command toolchain is unavailable: ".length);
    return reason;
  };
  return <section className="v2-task-delivery v2-project-actions" aria-label={t("项目测试与格式化", "Project tests and formatting")}>
    <div className="v2-delivery-heading"><h2>{t("项目测试与格式化", "Project tests and formatting")}</h2>
      <button type="button" disabled={actions.isFetching} onClick={() => void actions.refetch()}>{t("刷新动作", "Refresh actions")}</button></div>
    <p className="v2-delivery-muted">{t("使用本次任务保存的项目配置运行，结果保存在命令记录中。", "Run the project actions saved for this task. Results are retained in command history.")}
      {backendLabel && <> {t("执行环境：", "Environment: ")}{backendLabel}</>}</p>
    {actions.isPending && <p role="status">{t("正在读取项目动作…", "Reading project actions…")}</p>}
    {actions.isError && <ErrorState error={actions.error} />}
    {working && <p role="status">{t("代理正在执行，暂停后可手动运行项目动作。", "The agent is working. Pause it to run project actions manually.")}</p>}
    {(!client.hasControl || !client.hasRunExecution) && <p role="status">{t("当前连接仅可查看结果，没有命令执行权限。", "This connection can view results but cannot execute commands.")}</p>}
    {damaged && <p role="alert">{t("保存的运行请求无法读取，请先到命令记录核对。", "The saved request could not be read. Check command history first.")}</p>}
    {localError && <p role="alert">{localError}</p>}
    {actions.data && !actions.isError && <div className="v2-project-action-forms">
      {actions.data.actions.map((action) => {
        const slot = action.slot as ProjectActionSlot;
        const directory = action.target_kind === "directory";
        const needsFile = !directory;
        return <form key={slot} onSubmit={(event) => {
          event.preventDefault();
          if (!action.available || active || damaged) return;
          submit({ runID, slot, target: targets[slot].trim(), key: "project-action-" + crypto.randomUUID() });
        }}>
          <h3>{slot === "test" ? t("测试", "Tests") : t("格式化", "Formatting")}</h3>
          {action.configured ? <code>{action.command_id}</code> : <p>{t("项目尚未配置此动作，请在 .prayu/config.yaml 中设置。", "Configure this action in .prayu/config.yaml.")}</p>}
          {action.configured && <label>
            {directory ? t("测试目录（留空使用项目根目录）", "Test directory (empty for project root)") : t("要格式化的文件", "File to format")}
            <input value={targets[slot]} required={needsFile} disabled={active || working}
              placeholder={directory ? "web" : "internal/example.go"}
              onChange={(event) => setTargets((current) => ({ ...current, [slot]: event.target.value }))} />
            <small>{t("填写相对于项目目录的路径。", "Enter a path relative to the project directory.")}</small>
          </label>}
          {action.configured && action.reason && <p className="v2-delivery-muted">{reasonLabel(action.reason)}</p>}
          <button type="submit" disabled={!action.available || active || damaged || working || !client.hasControl ||
            !client.hasRunExecution || (needsFile && !targets[slot].trim())}>
            {slot === "test" ? t("运行测试", "Run tests") : t("格式化文件", "Format file")}
          </button>
        </form>;
      })}
    </div>}
    {execute.isPending && <p role="status">{t("正在启动命令…", "Starting command…")}</p>}
    {execute.isError && <ErrorState error={execute.error} />}
    {attempt && !attempt.jobID && !execute.isPending && <div role="status">
      <p>{t("上次请求的运行结果尚未确认。", "The previous request's outcome is not confirmed.")}</p>
      <button type="button" disabled={working || !client.hasControl || !client.hasRunExecution} onClick={() => submit(attempt)}>{t("确认上次运行", "Check previous execution")}</button>
    </div>}
    {attempt?.jobID && job.isPending && <p role="status">{t("正在读取命令结果…", "Reading command results…")}</p>}
    {job.isError && <div role="alert"><p>{t("命令状态读取失败。", "Could not read command status.")}</p>
      <button type="button" onClick={() => void job.refetch()}>{t("重试读取", "Retry reading")}</button></div>}
    {result && <section className="v2-project-action-result" aria-label={t("最近运行结果", "Latest execution result")}>
      <div className="v2-delivery-heading"><h3>{result.slot === "test" ? t("测试结果", "Test result") : t("格式化结果", "Formatting result")}</h3>
        <StatusLabel status={job.isError ? "unknown" : failed ? "failed" : result.job.state} /></div>
      {result.job.exit_code !== undefined && <p>{t("退出码：", "Exit code: ")}{result.job.exit_code}</p>}
      <details><summary>{t("命令记录", "Command record")}</summary><p>Job <code>{result.job.id}</code></p><p>Run <code>{runID}</code></p></details>
      {result.job.can_stop && <button type="button" disabled={stop.isPending || !client.hasControl || !client.hasRunExecution}
        onClick={() => stop.mutate(result.job.id)}>{t("停止运行", "Stop execution")}</button>}
      {stop.isError && <ErrorState error={stop.error} />}
      <h4>{t("标准输出", "Standard output")}</h4><pre>{result.job.stdout || t("暂无输出。", "No output yet.")}</pre>
      {result.job.stderr && <><h4>{t("标准错误", "Standard error")}</h4><pre>{result.job.stderr}</pre></>}
      {(result.job.stdout_truncated || result.job.stderr_truncated) && <p>{t("输出较长，此处显示保留的部分。", "Only the retained portion of the output is shown.")}</p>}
      {failed && onFeedback && <button type="button" onClick={() => onFeedback(
        "请修复这次项目验证失败。\nRun: " + runID + "\nJob: " + result.job.id + "\n动作: " + result.command_id +
        "\n退出码: " + result.job.exit_code + "\n" + (result.job.stderr || result.job.stdout).slice(-4000))}>
        {t("将失败结果放回对话", "Send failure to conversation")}</button>}
    </section>}
  </section>;
}
