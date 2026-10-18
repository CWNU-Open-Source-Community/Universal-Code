import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { WorkflowExecution, WorkflowSpec, WorkflowStep } from "../api/workflows";
import { useLocale } from "../lib/locale";
import { formatDate } from "../lib/format";
import { ErrorState, LoadingState, StatusBadge } from "./common";
import "./workflow-panel.css";

const operationKey = () => `workflow-${crypto.randomUUID()}`;
const pretty = (value: unknown) => JSON.stringify(value, null, 2);

export function workerWorkflow(name: string, goal: string, worker: boolean, tools: string, parentHistory = false,
  completion: NonNullable<WorkflowStep["completion"]> = { mode: worker ? "verified_code" : "report" }): WorkflowSpec {
  return {
    version: "workflow.v1", name,
    limits: { max_steps: 32, max_jobs: 8, max_tokens: 32000, max_turns: 16, timeout_millis: 1800000 },
    steps: [{ id: "work", kind: "worker", completion: worker ? completion : { mode: "report" }, inputs: { request: { step: "$input" } }, task: {
      title: name, goal, skills: ["model.chat"], input_refs: [], dependency_ordinals: [], surface_hint: "core",
      turn_limit: 4, token_limit: 8000, timeout_millis: 600000, expected_artifacts: [], tool_call_limit: 16,
      ...(worker ? { worker: { tools: tools.trim() ? tools.split(",").map((item) => item.trim()).filter(Boolean) : null,
        ...(parentHistory ? { history_scope: "parent_snapshot" } : {}) } }
        : { allowed_read_paths: ["."] }),
    } }],
  };
}

export function workflowExample(): WorkflowSpec {
  const spec = workerWorkflow("Inspect and repair", "Inspect the requested change. Report succeeded when complete, or partial when repair is needed.", true, "", false, { mode: "report" });
  spec.steps[0].id = "inspect";
  const repair: WorkflowStep = {
    ...workerWorkflow("Repair", "Use the inspection and previous repair results to finish the requested change. Report succeeded only after verification.", true, "").steps[0],
    id: "repair", inputs: { inspection: { step: "inspect" }, request: { step: "$input" } },
  };
  spec.steps.push({ id: "needs_repair", kind: "if", condition: { ref: { step: "inspect", path: "/outcome" }, equals: "partial" },
    then: [{ id: "repair_loop", kind: "repeat", max_iterations: 3,
      condition: { ref: { step: "repair", path: "/outcome" }, equals: "succeeded" }, body: [repair] }],
  });
  return spec;
}

export function isolatedWorkflow(name: string): WorkflowSpec {
  return {
    version: "workflow.v1", name,
    limits: { max_steps: 32, max_jobs: 8, max_tokens: 32000, max_turns: 16, timeout_millis: 1800000 },
    steps: [{ id: "delivery", kind: "batch", batch_plan: { step: "$input", path: "/plan_id" } }],
  };
}

export function dynamicWorkflowExample(): WorkflowSpec {
  const spec = workerWorkflow("Parallel review", "Inspect the request and identify up to three independent review items. Finish with a data object containing an items array; each item describes the file and question to review. Return an empty array when there is nothing to review.", false, "");
  spec.steps[0].id = "plan";
  spec.limits = { max_steps: 32, max_jobs: 8, max_tokens: 48000, max_turns: 24, timeout_millis: 1800000 };
  spec.steps.push({
    ...workerWorkflow("Review item", "Review the supplied item within the authorized reading scope. Return findings and evidence in the completion report and data object.", false, "").steps[0],
    id: "reviews", kind: "map", items: { step: "plan", path: "/data/items" },
    max_items: 3, parallelism: 2, max_attempts: 2,
    inputs: { request: { step: "$input" } },
  }, {
    ...workerWorkflow("Summarize", "Combine the saved review results. Report findings with their evidence, and explain any incomplete outcomes.", false, "").steps[0],
    id: "summary", inputs: { reviews: { step: "reviews", path: "/items" }, request: { step: "$input" } },
  });
  return spec;
}

export function WorkflowPanel({ client, runID, runStatus }: { client: APIClient; runID: string; runStatus?: string }) {
  const { t } = useLocale();
  const cache = useQueryClient();
  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [worker, setWorker] = useState(true);
  const [tools, setTools] = useState("");
  const [parentHistory, setParentHistory] = useState(false);
  const [completionMode, setCompletionMode] = useState<"verified_code" | "report">("verified_code");
  const [verificationTarget, setVerificationTarget] = useState("");
  const [isolated, setIsolated] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [source, setSource] = useState(() => pretty(workflowExample()));
  const [selected, setSelected] = useState("");
  const [inputs, setInputs] = useState("{}");
  const definitions = useQuery({ queryKey: ["workflow-definitions"], queryFn: ({ signal }) => client.workflows.definitions(signal) });
  const executions = useQuery({
    queryKey: ["run", runID, "workflows"], queryFn: ({ signal }) => client.workflows.list(runID, signal),
    refetchOnMount: "always", refetchOnWindowFocus: "always", refetchIntervalInBackground: false,
    refetchInterval: (query) => !query.state.error && query.state.data?.some((item) =>
      ["running", "waiting", "waiting_approval"].includes(item.status)) ? 2_000 : false,
  });
  const canControl = client.hasControl && client.hasModelControl;
  const definition = definitions.data?.find((item) => item.id === selected);
  const completion = { mode: completionMode,
    ...(completionMode === "verified_code" && verificationTarget.trim() ? { target: verificationTarget.trim() } : {}) };

  useEffect(() => {
    if (executions.data) void cache.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] }, { cancelRefetch: false });
  }, [cache, runID, executions.data]);

  function publish(value: WorkflowExecution) {
    cache.setQueryData<WorkflowExecution[]>(["run", runID, "workflows"], (old) => [value, ...(old ?? []).filter((item) => item.id !== value.id)]);
  }
  const save = useMutation({
    mutationFn: () => client.workflows.save(advanced ? JSON.parse(source) as WorkflowSpec : isolated ? isolatedWorkflow(name.trim()) : workerWorkflow(name.trim(), goal.trim(), worker, tools, parentHistory, completion), operationKey()),
    onSuccess: (saved) => {
      setSelected(saved.id);
      cache.setQueryData(["workflow-definitions"], (old: typeof definitions.data) => [saved, ...(old ?? []).filter((item) => item.id !== saved.id)]);
    },
  });
  const action = useMutation({
    mutationFn: async ({ kind, item }: { kind: "start" | "continue" | "resume" | "rerun" | "retry"; item?: WorkflowExecution }) => {
      if (kind === "start") return client.workflows.start(runID, selected, JSON.parse(inputs), operationKey());
      if (!item) throw new Error("Select a saved execution");
      if (kind === "rerun") return client.workflows.rerun(runID, item.id, operationKey());
      if (kind === "retry") return client.workflows.retryFailed(runID, item.id, item.revision, operationKey());
      return client.workflows.resume(runID, item.id, item.revision, operationKey());
    },
    onSuccess: publish,
    onSettled: () => { void cache.invalidateQueries({ queryKey: ["run", runID, "workflows"] }); },
  });
  const pause = useMutation({
    mutationFn: (item: WorkflowExecution) => client.workflows.pause(runID, item.id, item.revision, operationKey()),
    onSuccess: publish,
    onSettled: () => { void cache.invalidateQueries({ queryKey: ["run", runID, "workflows"] }); },
  });

  return <section className="detail-section workflow-panel" aria-label={t("工作流", "Workflows")}>
    <div className="section-heading"><h2>{t("工作流", "Workflows")}</h2><span>{t("保存、执行与继续", "Save, run and continue")}</span></div>
    <p>{t("共享工作区子代理与隔离交付可以组合；已保存结果可用于分支、循环和并行子任务。", "Combine shared workspace workers and isolated deliveries. Saved results can drive branches, loops and parallel tasks.")}</p>
    <p>{t("应用运行期间会自动推进已启动的流程；刷新页面不会中断执行。", "The app advances started workflows while it is running; refreshing this page does not interrupt execution.")}</p>
    <details>
      <summary>{t("创建工作流", "Create workflow")}</summary>
      <div className="workflow-form">
        <label className="workflow-checkbox"><input type="checkbox" checked={advanced} onChange={(event) => setAdvanced(event.target.checked)} />{t("编辑 JSON 程序", "Edit JSON program")}</label>
        {advanced ? <>
          <p>{t("先检查模板的任务与预算，再保存。if 比较已完成步骤的结构化字段；repeat 每次完成后检查条件，最多执行 max_iterations 次。$input 引用启动输入。", "Review the template tasks and budgets before saving. if compares committed result fields; repeat checks its condition after each iteration, up to max_iterations. $input references launch inputs.")}</p>
          <button type="button" className="command-button" onClick={() => setSource(pretty(workflowExample()))}>{t("载入分支与循环模板", "Load branch and loop template")}</button>
          <button type="button" className="command-button" onClick={() => setSource(pretty(dynamicWorkflowExample()))}>{t("载入动态并行审阅模板", "Load dynamic parallel review template")}</button>
          <button type="button" className="command-button" onClick={() => setSource(pretty(isolatedWorkflow(name.trim() || "Isolated delivery")))}>{t("载入隔离交付模板", "Load isolated delivery template")}</button>
          <p>{t("并行审阅模板根据读取结果展开最多 3 项任务，同时执行最多 2 项，全部完成后汇总。任务与读取范围在保存时固定，结果只提供输入；总预算跨重试累计。", "The parallel review template expands up to 3 items from read results, runs up to 2 at once, then joins their results. Tasks and reading scope are fixed when saved; results supply inputs. Total budgets accumulate across retries.")}</p>
          <label>{t("工作流 JSON（可复制）", "Workflow JSON (copyable)")}<textarea className="workflow-json" rows={20} value={source} onChange={(event) => setSource(event.target.value)} spellCheck={false} /></label>
        </> : <>
          <label>{t("名称", "Name")}<input value={name} onChange={(event) => setName(event.target.value)} /></label>
          <label>{t("工作区方式", "Workspace mode")}<select value={isolated ? "batch" : "shared"} onChange={(event) => setIsolated(event.target.value === "batch")}>
            <option value="shared">{t("共享当前工作区", "Share current workspace")}</option>
            <option value="batch">{t("隔离交付（Batch）", "Isolated delivery (Batch)")}</option>
          </select></label>
          {isolated ? <p>{t("使用已在 Batch 中批准并准备的交付计划。启动输入填写 plan_id；流程执行子任务、等待独立复核，再生成集成分支。", "Use a delivery plan already approved and prepared in Batch. Set plan_id in launch inputs; the workflow runs its children, waits for independent review, then creates an integration branch.")}</p> : <>
          <label>{t("任务", "Task")}<textarea rows={4} value={goal} onChange={(event) => setGoal(event.target.value)} /></label>
          <label className="workflow-checkbox"><input type="checkbox" checked={worker} onChange={(event) => setWorker(event.target.checked)} />{t("执行型子代理", "Execution worker")}</label>
          {worker ? <>
            <label>{t("任务类型", "Task type")}<select value={completionMode} onChange={(event) => setCompletionMode(event.target.value === "report" ? "report" : "verified_code")}>
              <option value="verified_code">{t("代码交付：验证通过后完成", "Code delivery: verified completion")}</option>
              <option value="report">{t("分析报告", "Analysis report")}</option>
            </select></label>
            {completionMode === "verified_code" ? <>
              <label>{t("测试目录（可选）", "Test directory (optional)")}<input value={verificationTarget} onChange={(event) => setVerificationTarget(event.target.value)} placeholder={t("留空使用项目根目录", "Leave blank for the project root")} /></label>
              <p>{t("完成前核对当前源码的真实测试结果；代码变化或测试失败后，旧结果不能用于交付。委派工具需包含 run_tests。", "Completion checks real test results for the current source. Changed code or a failed test invalidates the earlier result. Delegated tools must include run_tests.")}</p>
            </> : <p>{t("以分析报告完成，不要求项目测试结果。", "Finish with an analysis report; project test results are not required.")}</p>}
            <label>{t("委派工具（逗号分隔，可留空）", "Delegated tools (comma-separated, optional)")}<input value={tools} onChange={(event) => setTools(event.target.value)} placeholder={t("留空继承当前可委派工具", "Leave blank to inherit available worker tools")} /></label>
            <label className="workflow-checkbox"><input type="checkbox" checked={parentHistory} onChange={(event) => setParentHistory(event.target.checked)} />{t("同时交付当前父会话历史", "Include current parent history")}</label>
            <p>{parentHistory ? t("创建子代理时固定当时父会话可见的历史范围；后续新增消息不会自动加入。", "Capture the parent's visible history when the child is created; later messages are not added automatically.") : t("默认仅能回读自己的记录和明确交付的任务材料。", "Recall is limited to the child's records and explicitly delivered task material.")}</p>
          </>
            : <p>{t("只读模式：允许读取当前工作区。", "Read-only mode: allow reading the current workspace.")}</p>}
          <p>{t("单步骤：最多 4 轮、8,000 tokens、10 分钟；完整流程：最多 16 轮、32,000 tokens、30 分钟。JSON 编辑器可调整。", "Per job: 4 turns, 8,000 tokens, 10 minutes. Whole workflow: 16 turns, 32,000 tokens, 30 minutes. Adjust in the JSON editor.")}</p>
          </>}
          <button type="button" className="command-button" onClick={() => { setSource(pretty(isolated ? isolatedWorkflow(name.trim() || "Isolated delivery") : workerWorkflow(name.trim() || "Workflow", goal.trim() || "Describe the task", worker, tools, parentHistory, completion))); setAdvanced(true); }}>{t("在 JSON 中编辑任务与预算", "Edit task and budgets in JSON")}</button>
        </>}
        <button type="button" className="command-button" disabled={!canControl || save.isPending || (!advanced && (!name.trim() || (!isolated && !goal.trim())))} onClick={() => save.mutate()}>{save.isPending ? t("保存中…", "Saving…") : t("保存定义", "Save definition")}</button>
        {save.error && <ErrorState error={save.error} />}
      </div>
    </details>
    <div className="workflow-form">
      <label>{t("已保存的工作流", "Saved workflow")}<select value={selected} onChange={(event) => setSelected(event.target.value)}>
        <option value="">{t("选择定义", "Select definition")}</option>
        {definitions.data?.map((item) => <option value={item.id} key={item.id}>{item.spec.name} · {item.id.slice(-8)}</option>)}
      </select></label>
      {definition && <details><summary>{t("检查任务、工具和预算", "Review tasks, tools and budgets")}</summary><pre>{pretty(definition.spec)}</pre></details>}
      <label>{t("启动输入 JSON", "Launch inputs JSON")}<textarea rows={3} value={inputs} onChange={(event) => setInputs(event.target.value)} spellCheck={false} /></label>
      <p>{t("启动后，子代理在流程预算和主代理权限内执行。工具名单留空会继承当前可委派工具。隔离交付完成时产物位于集成分支，后续共享步骤仍使用当前工作区。", "Workers execute within the workflow budgets and parent permissions. An omitted tool list inherits available worker tools. Isolated results live on an integration branch; later shared steps still use the current workspace.")}</p>
      <button type="button" className="command-button" disabled={!canControl || !selected || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "start" })}>{t("启动并运行", "Start and run")}</button>
      {runStatus !== "running" && <p>{t("先恢复当前任务，再启动或继续工作流。", "Resume the current task before starting or continuing a workflow.")}</p>}
      {!canControl && <p>{t("运行工作流需要模型控制权限。", "Model control permission is required to run workflows.")}</p>}
    </div>
    {(definitions.isLoading || executions.isLoading) && <LoadingState />}
    {definitions.error && <ErrorState error={definitions.error} />}
    {executions.error && <><ErrorState error={executions.error} /><button type="button" className="command-button" disabled={executions.isFetching} onClick={() => void executions.refetch()}>{t("重新读取工作流", "Reload workflows")}</button></>}
    {action.error && <ErrorState error={action.error} />}
    {pause.error && <ErrorState error={pause.error} />}
    <div className="projection-stack">
      {executions.data?.map((item) => <article className="delegation-item" key={item.id}>
        <div className="section-heading"><strong>{definitions.data?.find((saved) => saved.id === item.definition_id)?.spec.name ?? item.definition_id}</strong><StatusBadge status={item.status} /></div>
        <p><code>{item.id}</code> · {item.state.jobs} {t("个已完成步骤", "completed jobs")} · {t("检查点", "checkpoint")} {item.revision}</p>
        {item.reason && <p>{item.reason}</p>}
        {Object.entries(item.state.maps ?? {}).map(([key, group]) => <details className="workflow-task-group" key={key}>
          <summary><code>{group.step_id}</code> · {group.items.filter((task) => task.status === "completed").length}/{group.items.length} {t("项完成", "completed")}
            {group.items.some((task) => task.status === "failed") && <> · {group.items.filter((task) => task.status === "failed").length} {t("项失败", "failed")}</>}</summary>
          <ol className="workflow-task-list">
            {group.items.map((task) => <li key={task.index}>
              <div className="section-heading"><span>{t(`第 ${task.generation} 次尝试`, `Attempt ${task.generation}`)}</span><StatusBadge status={task.status} /></div>
              {task.reason && <p>{task.reason === "waiting_approval" ? t("等待工具审批", "Waiting for tool approval") : task.reason}</p>}
              <details><summary>{t("任务输入与记录", "Task input and record")}</summary><code>{task.step_execution_id}</code>
                {task.value !== undefined && <pre>{pretty(task.value)}</pre>}
              </details>
            </li>)}
          </ol>
        </details>)}
        {Object.keys(item.state.verifications ?? {}).length > 0 && <section className="workflow-verifications" aria-label={t("上次验证核对", "Last verification checks")}>
          {Object.entries(item.state.verifications ?? {}).map(([target, check]) => <article className="workflow-verification" key={target}>
            <div className="section-heading"><strong>{target === "." ? t("项目根目录", "Project root") : <code>{target}</code>}</strong>
              <span>{check.state === "passed" && check.reusable ? t("上次核对通过", "Passed at last check") : t("上次核对未通过", "Not passed at last check")}</span></div>
            <p>{t("核对时间", "Checked at")} <time dateTime={check.checked_at}>{formatDate(check.checked_at)}</time></p>
            {check.reason && <p>{check.reason}</p>}
            <details><summary>{t("测试与源码记录", "Test and source record")}</summary><dl>
              <dt>{t("验证要求步骤", "Required by step")}</dt><dd><code>{check.step_id}</code></dd>
              <dt>{t("执行子代理", "Test worker")}</dt><dd><code>{check.agent_id}</code></dd>
              <dt>{t("测试任务", "Test job")}</dt><dd><code>{check.job_id || "-"}</code></dd>
              <dt>{t("源码版本", "Source version")}</dt><dd><code>{check.source_version || "-"}</code></dd>
              <dt>{t("退出状态", "Exit code")}</dt><dd>{check.exit_code ?? "-"}</dd>
            </dl></details>
          </article>)}
        </section>}
        {item.status === "waiting_approval" && <p role="status">{t("子代理正在等待工具审批。完成任务中的待审批项后，应用会自动继续。", "The child is waiting for tool approval. The app continues automatically after the pending approval is resolved.")}</p>}
        {item.status === "running" && runStatus === "running" && <p role="status">{t("应用正在推进流程；每轮自动保存检查点。", "The app is advancing this workflow; each turn saves a checkpoint.")}</p>}
        {item.status === "failed" && item.state.blocked?.retryable && <p>{t("可以仅重试失败项，保留成功项和原输入；总预算与截止时间不会重置。", "Retry failed items while preserving successful items and original inputs. Total budgets and the deadline are not reset.")}</p>}
        <div className="workflow-actions">
          {!["completed", "failed", "paused"].includes(item.status) && <>
            {item.status === "waiting" && <button type="button" className="command-button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "continue", item })}>{t("继续运行", "Continue running")}</button>}
            <button type="button" className="command-button" disabled={!canControl || action.isPending || pause.isPending} onClick={() => pause.mutate(item)}>{t("暂停流程", "Pause workflow")}</button>
          </>}
          {item.status === "paused" && <button type="button" className="command-button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "resume", item })}>{t("恢复并运行", "Resume and run")}</button>}
          {item.status === "failed" && item.state.blocked?.retryable && <button type="button" className="command-button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "retry", item })}>{t("仅重试失败项", "Retry failed items only")}</button>}
          <button type="button" className="command-button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "rerun", item })}>{t("以原输入重新运行", "Rerun original inputs")}</button>
        </div>
        <details><summary>{t("输入与已保存结果", "Inputs and saved results")}</summary><pre>{pretty({ inputs: item.inputs, outputs: item.state.outputs })}</pre></details>
      </article>)}
    </div>
  </section>;
}
