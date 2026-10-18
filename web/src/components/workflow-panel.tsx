import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { WorkflowExecution, WorkflowSpec, WorkflowStep } from "../api/workflows";
import { useLocale } from "../lib/locale";
import { ErrorState, LoadingState, StatusBadge } from "./common";
import "./workflow-panel.css";

const operationKey = () => `workflow-${crypto.randomUUID()}`;
const pretty = (value: unknown) => JSON.stringify(value, null, 2);

export function workerWorkflow(name: string, goal: string, worker: boolean, tools: string, parentHistory = false): WorkflowSpec {
  return {
    version: "workflow.v1", name,
    limits: { max_steps: 32, max_jobs: 8, max_tokens: 32000, max_turns: 16, timeout_millis: 1800000 },
    steps: [{ id: "work", kind: "worker", inputs: { request: { step: "$input" } }, task: {
      title: name, goal, skills: ["model.chat"], input_refs: [], dependency_ordinals: [], surface_hint: "core",
      turn_limit: 4, token_limit: 8000, timeout_millis: 600000, expected_artifacts: [], tool_call_limit: 16,
      ...(worker ? { worker: { tools: tools.trim() ? tools.split(",").map((item) => item.trim()).filter(Boolean) : null,
        ...(parentHistory ? { history_scope: "parent_snapshot" } : {}) } }
        : { allowed_read_paths: ["."] }),
    } }],
  };
}

export function workflowExample(): WorkflowSpec {
  const spec = workerWorkflow("Inspect and repair", "Inspect the requested change. Report succeeded when complete, or partial when repair is needed.", true, "");
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

export function WorkflowPanel({ client, runID, runStatus }: { client: APIClient; runID: string; runStatus?: string }) {
  const { t } = useLocale();
  const cache = useQueryClient();
  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [worker, setWorker] = useState(true);
  const [tools, setTools] = useState("");
  const [parentHistory, setParentHistory] = useState(false);
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

  useEffect(() => {
    if (executions.data) void cache.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] }, { cancelRefetch: false });
  }, [cache, runID, executions.data]);

  function publish(value: WorkflowExecution) {
    cache.setQueryData<WorkflowExecution[]>(["run", runID, "workflows"], (old) => [value, ...(old ?? []).filter((item) => item.id !== value.id)]);
  }
  const save = useMutation({
    mutationFn: () => client.workflows.save(advanced ? JSON.parse(source) as WorkflowSpec : workerWorkflow(name.trim(), goal.trim(), worker, tools, parentHistory), operationKey()),
    onSuccess: (saved) => {
      setSelected(saved.id);
      cache.setQueryData(["workflow-definitions"], (old: typeof definitions.data) => [saved, ...(old ?? []).filter((item) => item.id !== saved.id)]);
    },
  });
  const action = useMutation({
    mutationFn: async ({ kind, item }: { kind: "start" | "continue" | "resume" | "rerun"; item?: WorkflowExecution }) => {
      if (kind === "start") return client.workflows.start(runID, selected, JSON.parse(inputs), operationKey());
      if (!item) throw new Error("Select a saved execution");
      if (kind === "rerun") return client.workflows.rerun(runID, item.id, operationKey());
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
    <p>{t("每个新步骤创建独立子代理；中间结果会保存，可用于条件分支和有界循环。", "Each new job creates an independent child. Saved results can drive conditions and bounded loops.")}</p>
    <p>{t("应用运行期间会自动推进已启动的流程；刷新页面不会中断执行。", "The app advances started workflows while it is running; refreshing this page does not interrupt execution.")}</p>
    <details>
      <summary>{t("创建工作流", "Create workflow")}</summary>
      <div className="workflow-form">
        <label className="workflow-checkbox"><input type="checkbox" checked={advanced} onChange={(event) => setAdvanced(event.target.checked)} />{t("编辑 JSON 程序", "Edit JSON program")}</label>
        {advanced ? <>
          <p>{t("先检查模板的任务与预算，再保存。if 比较已完成步骤的结构化字段；repeat 每次完成后检查条件，最多执行 max_iterations 次。$input 引用启动输入。", "Review the template tasks and budgets before saving. if compares committed result fields; repeat checks its condition after each iteration, up to max_iterations. $input references launch inputs.")}</p>
          <button type="button" onClick={() => setSource(pretty(workflowExample()))}>{t("载入分支与循环模板", "Load branch and loop template")}</button>
          <label>{t("工作流 JSON（可复制）", "Workflow JSON (copyable)")}<textarea className="workflow-json" rows={20} value={source} onChange={(event) => setSource(event.target.value)} spellCheck={false} /></label>
        </> : <>
          <label>{t("名称", "Name")}<input value={name} onChange={(event) => setName(event.target.value)} /></label>
          <label>{t("任务", "Task")}<textarea rows={4} value={goal} onChange={(event) => setGoal(event.target.value)} /></label>
          <label className="workflow-checkbox"><input type="checkbox" checked={worker} onChange={(event) => setWorker(event.target.checked)} />{t("执行型子代理", "Execution worker")}</label>
          {worker ? <>
            <label>{t("委派工具（逗号分隔，可留空）", "Delegated tools (comma-separated, optional)")}<input value={tools} onChange={(event) => setTools(event.target.value)} placeholder={t("留空继承当前可委派工具", "Leave blank to inherit available worker tools")} /></label>
            <label className="workflow-checkbox"><input type="checkbox" checked={parentHistory} onChange={(event) => setParentHistory(event.target.checked)} />{t("同时交付当前父会话历史", "Include current parent history")}</label>
            <p>{parentHistory ? t("创建子代理时固定当时父会话可见的历史范围；后续新增消息不会自动加入。", "Capture the parent's visible history when the child is created; later messages are not added automatically.") : t("默认仅能回读自己的记录和明确交付的任务材料。", "Recall is limited to the child's records and explicitly delivered task material.")}</p>
          </>
            : <p>{t("只读模式：允许读取当前工作区。", "Read-only mode: allow reading the current workspace.")}</p>}
          <p>{t("单步骤：最多 4 轮、8,000 tokens、10 分钟；完整流程：最多 16 轮、32,000 tokens、30 分钟。JSON 编辑器可调整。", "Per job: 4 turns, 8,000 tokens, 10 minutes. Whole workflow: 16 turns, 32,000 tokens, 30 minutes. Adjust in the JSON editor.")}</p>
          <button type="button" onClick={() => { setSource(pretty(workerWorkflow(name.trim() || "Workflow", goal.trim() || "Describe the task", worker, tools, parentHistory))); setAdvanced(true); }}>{t("在 JSON 中编辑任务与预算", "Edit task and budgets in JSON")}</button>
        </>}
        <button type="button" className="command-button" disabled={!canControl || save.isPending || (!advanced && (!name.trim() || !goal.trim()))} onClick={() => save.mutate()}>{save.isPending ? t("保存中…", "Saving…") : t("保存定义", "Save definition")}</button>
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
      <p>{t("启动后，已保存定义中的子代理会在流程预算和主代理权限内自动执行。留空工具名单会继承当前主代理工具。", "Starting authorizes workers in the saved definition to run within its budgets and the parent's permissions. An omitted tool list inherits the current parent tools.")}</p>
      <button type="button" className="command-button" disabled={!canControl || !selected || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "start" })}>{t("启动并运行", "Start and run")}</button>
      {runStatus !== "running" && <p>{t("先恢复当前任务，再启动或继续工作流。", "Resume the current task before starting or continuing a workflow.")}</p>}
      {!canControl && <p>{t("运行工作流需要模型控制权限。", "Model control permission is required to run workflows.")}</p>}
    </div>
    {(definitions.isLoading || executions.isLoading) && <LoadingState />}
    {definitions.error && <ErrorState error={definitions.error} />}
    {executions.error && <><ErrorState error={executions.error} /><button type="button" disabled={executions.isFetching} onClick={() => void executions.refetch()}>{t("重新读取工作流", "Reload workflows")}</button></>}
    {action.error && <ErrorState error={action.error} />}
    {pause.error && <ErrorState error={pause.error} />}
    <div className="projection-stack">
      {executions.data?.map((item) => <article className="delegation-item" key={item.id}>
        <div className="section-heading"><strong>{definitions.data?.find((saved) => saved.id === item.definition_id)?.spec.name ?? item.definition_id}</strong><StatusBadge status={item.status} /></div>
        <p><code>{item.id}</code> · {item.state.jobs} {t("个已完成步骤", "completed jobs")} · {t("检查点", "checkpoint")} {item.revision}</p>
        {item.reason && <p>{item.reason}</p>}
        {item.status === "waiting_approval" && <p role="status">{t("子代理正在等待工具审批。完成任务中的待审批项后，应用会自动继续。", "The child is waiting for tool approval. The app continues automatically after the pending approval is resolved.")}</p>}
        {item.status === "running" && runStatus === "running" && <p role="status">{t("应用正在推进流程；每轮自动保存检查点。", "The app is advancing this workflow; each turn saves a checkpoint.")}</p>}
        <div className="workflow-actions">
          {!["completed", "failed", "paused"].includes(item.status) && <>
            {item.status === "waiting" && <button type="button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "continue", item })}>{t("继续运行", "Continue running")}</button>}
            <button type="button" disabled={!canControl || action.isPending || pause.isPending} onClick={() => pause.mutate(item)}>{t("暂停流程", "Pause workflow")}</button>
          </>}
          {item.status === "paused" && <button type="button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "resume", item })}>{t("恢复并运行", "Resume and run")}</button>}
          <button type="button" disabled={!canControl || action.isPending || pause.isPending || runStatus !== "running"} onClick={() => action.mutate({ kind: "rerun", item })}>{t("以原输入重新运行", "Rerun original inputs")}</button>
        </div>
        <details><summary>{t("输入与已保存结果", "Inputs and saved results")}</summary><pre>{pretty({ inputs: item.inputs, outputs: item.state.outputs })}</pre></details>
      </article>)}
    </div>
  </section>;
}
