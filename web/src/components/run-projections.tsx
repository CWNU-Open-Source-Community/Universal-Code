import { useEffect, useMemo, useRef, useState } from "react";
import { useIsMutating, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, FileSearch, GitBranch, LoaderCircle, Network, PackageCheck, ScanSearch, ShieldAlert } from "lucide-react";
import type { APIClient } from "../api/client";
import type {
  AgentGraphView,
  AgentNodeView,
  BatchDeliverySnapshotView,
  DelegationView,
  ExternalSkillProjectionView,
  FanoutPlanView,
  FindingReportSummaryView,
  FindingReportView,
  ModelCancellationRequestView,
  RunDetailView,
  SpecialistModelCancellationView,
} from "../api/types";
import { usePagedResource } from "../hooks/use-paged-resource";
import { formatBytes, formatDate, formatNumber, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { diagnosticVocabulary } from "../lib/vocabulary";
import { EmptyState, ErrorState, LoadMoreButton, LoadingState, StatusBadge } from "./common";
import { BatchPreparation, BatchWorkbenchControls } from "./batch-workbench";
import { SpecialistExecutionControl } from "./specialist-execution";

export function ExternalSkillsPanel({ projection }: { projection: ExternalSkillProjectionView }) {
  const { t } = useLocale();
  return (
    <section className="detail-section external-skills-section" aria-label={t("外部 Skill 来源", "External Skill provenance")}>
      <div className="section-heading">
        <h2><PackageCheck aria-hidden="true" size={15} />{t("外部 Skill", "External skills")}</h2>
        <span>{t(`已选择 ${formatNumber(projection.item_count)} 个`, `${formatNumber(projection.item_count)} selected`)}</span>
      </div>
      <dl className="detail-grid compact external-skill-summary">
        <Metric label={t("工作面", "Surface")} value={`${projection.surface} / ${projection.profile}`} />
        <Metric label={t("模式修订", "Mode revision")} value={formatNumber(projection.mode_revision)} />
        <Metric label={t("Token 上限", "Token bound")} value={`${formatNumber(projection.token_upper_bound)} / ${formatNumber(projection.token_budget)}`} />
        <Metric label={t("根 Agent 交付", "Root delivery")} value={`${formatNumber(projection.root_delivery.committed)} / ${formatNumber(projection.root_delivery.prepared)}`} />
        <Metric label={t("专家 Agent 交付", "Specialist delivery")} value={`${formatNumber(projection.specialist_delivery.committed)} / ${formatNumber(projection.specialist_delivery.prepared)}`} />
        <Metric label={t("操作者确认", "Operator confirmation")} value={projection.operator_confirmed ? t("已确认", "Confirmed") : t("缺失", "Missing")} />
        <Metric label={t("上下文交付", "Context delivery")} value={projection.context_delivery_authorized ? t("已授权", "Authorized") : t("关闭", "Closed")} />
        <Metric label={t("工具权限", "Tool authority")} value={projection.tool_capability_grant ? t("已授予", "Granted") : t("关闭", "Closed")} />
      </dl>
      <ol className="external-skill-list">
        {projection.items.map((item) => (
          <li key={`${item.ordinal}-${item.name}-${item.version}`}>
            <span className="external-skill-order">#{item.ordinal}</span>
            <div><strong>{item.name}@{item.version}</strong><small>{item.trust_class}</small></div>
            <span>{t(`最多 ${formatNumber(item.token_upper_bound)} Tokens`, `${formatNumber(item.token_upper_bound)} tokens max`)}</span>
            <span>{t(`${formatNumber(item.declared_tool_count)} 个声明工具`, `${formatNumber(item.declared_tool_count)} declared tools`)}</span>
            <span>{item.specialist_eligible ? t("专家可用", "Specialist") : t("仅根 Agent", "Root only")}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}

export function ExternalSkillsSection({ client, runID, initial }: {
  client: APIClient;
  runID: string;
  initial: ExternalSkillProjectionView;
}) {
  const query = useQuery({
    queryKey: ["run", runID, "external-skills"],
    queryFn: ({ signal }) => client.getRunExternalSkills(runID, signal),
    initialData: initial,
  });
  return <ExternalSkillsPanel projection={query.data ?? initial} />;
}

export function AgentGraphPanel({ client, runID }: ProjectionProps) {
  const { t } = useLocale();
  const executing = useIsMutating({ mutationKey: ["run", runID, "specialist-execute"] });
  const run = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(runID)}`, {}, signal),
  });
  const query = useQuery({
    queryKey: ["run", runID, "agent-graph"],
    queryFn: ({ signal }) => client.get<AgentGraphView>(`/runs/${encodeURIComponent(runID)}/agent-graph`, {}, signal),
    refetchInterval: (query) => executing > 0 || query.state.data?.nodes.some((node) => node.role === "specialist" && node.status === "running") ? 3000 : false,
  });
  if (query.isLoading) return <LoadingState label="加载 Agent 图" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  if (query.data.nodes.length === 0) return <EmptyState>暂无 Agent</EmptyState>;
  const nodes = query.data.nodes;
  // Render the child tree: the root first, its direct children indented
  // beneath it (depth is bounded at 1 by the admission contract).
  const ordered = [...nodes].sort((left, right) => {
    if (left.id === query.data.root_agent_id) return -1;
    if (right.id === query.data.root_agent_id) return 1;
    return (left.parent_id ?? "").localeCompare(right.parent_id ?? "") || left.id.localeCompare(right.id);
  });
  const root = ordered.find((node) => node.id === query.data.root_agent_id);
  return (
    <div className="projection-stack agent-graph" aria-label={t("Agent 图", "Agent graph")}>
      {root && (
        <article className={`agent-node agent-depth-${root.depth}`} key={root.id}>
          <header>
            <span className="node-role"><GitBranch aria-hidden="true" size={15} />{root.role}</span>
            <strong>{shortID(root.id)}</strong>
            <StatusBadge status={root.status} />
          </header>
          <dl className="projection-metrics">
            <Metric label={t(...diagnosticVocabulary.session)} value={shortID(root.session_id)} />
            <Metric label={t("配置档", "Profile")} value={root.profile} />
            <Metric label={t("回合", "Turns")} value={`${formatNumber(root.turns_used)} / ${formatNumber(root.turn_limit)}`} />
            <Metric label="Tokens" value={`${formatNumber(root.tokens_used)} / ${formatNumber(root.token_limit)}`} />
            <Metric label={t("子槽位", "Child slots")} value={t(`${formatNumber(root.child_limit)} 个`, `${formatNumber(root.child_limit)}`)} />
          </dl>
          <div className="tag-line">{root.skills.map((skill) => <code key={skill}>{skill}</code>)}</div>
        </article>
      )}
      {ordered.filter((node) => node.id !== query.data.root_agent_id).map((node) => (
        <article className={`agent-node agent-depth-${node.depth} agent-child`} key={node.id}>
          <header>
            <span className="node-role"><GitBranch aria-hidden="true" size={15} />{node.role}</span>
            <strong>{shortID(node.id)}</strong>
            <StatusBadge status={node.status} />
          </header>
          <dl className="projection-metrics">
            <Metric label={t(...diagnosticVocabulary.session)} value={shortID(node.session_id)} />
            <Metric label={t("配置档", "Profile")} value={node.profile} />
            <Metric label={t("回合", "Turns")} value={`${formatNumber(node.turns_used)} / ${formatNumber(node.turn_limit)}`} />
            <Metric label="Tokens" value={`${formatNumber(node.tokens_used)} / ${formatNumber(node.token_limit)}`} />
            <Metric label={t("剩余回合", "Turns left")} value={formatNumber(Math.max(0, node.turn_limit - node.turns_used))} />
            <Metric label={t("剩余Tokens", "Tokens left")} value={formatNumber(Math.max(0, node.token_limit - node.tokens_used))} />
          </dl>
          <div className="tag-line">{node.skills.map((skill) => <code key={skill}>{skill}</code>)}</div>
          {node.completion && (
            <div className="completion-summary">
              <StatusBadge status={node.completion.outcome} />
              <span>{node.completion.summary}</span>
            </div>
          )}
          {client.hasControl && node.status === "running" && node.active_attempt_id &&
            node.id !== query.data.root_agent_id && (
            <SpecialistCancelControl agent={node} client={client} runID={runID} />
          )}
          <SpecialistExecutionControl key={`${client.baseURL}/${runID}/${node.id}`} client={client} runID={runID} agent={node} runStatus={run.data?.run.status} />
        </article>
      ))}
    </div>
  );
}

function SpecialistCancelControl({ agent, client, runID }: {
  agent: AgentNodeView;
  client: APIClient;
  runID: string;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const attemptID = agent.active_attempt_id ?? "";
  const [modelAttempt, setModelAttempt] = useState(1);
  const [lastResult, setLastResult] = useState<SpecialistModelCancellationView | null>(null);
  const operationKey = useRef<string | null>(null);
  const cancel = useMutation({
    mutationFn: () => {
      const key = operationKey.current ??
        (operationKey.current = `web-agent-cancel-call-${globalThis.crypto.randomUUID()}`);
      const body: ModelCancellationRequestView = { attempt_id: attemptID, model_attempt: modelAttempt };
      return client.cancelSpecialistModelCall(runID, agent.id, body, key);
    },
    onSuccess: (result) => {
      operationKey.current = null;
      setLastResult(result);
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
  });
  return (
    <div className="agent-cancel-control">
      <div className="run-execution-control">
        <label htmlFor={`agent-cancel-attempt-${agent.id}`}>{t("模型尝试", "Model attempt")}</label>
        <input id={`agent-cancel-attempt-${agent.id}`} min={1} type="number"
          onChange={(event) => setModelAttempt(Math.max(1,
            Number.parseInt(event.target.value, 10) || 1))} value={modelAttempt} />
      </div>
      <button className="command-button danger" disabled={cancel.isPending}
        onClick={() => cancel.mutate()} type="button">
        {cancel.isPending
          ? <LoaderCircle aria-hidden="true" className="spin" size={16} />
          : <Ban aria-hidden="true" size={16} />}
        {t("取消模型调用", "Cancel model call")}
      </button>
      {lastResult && (
        <div className="run-control-result" role="status">
          <StatusBadge status={lastResult.status} />
          <span>{t("尝试", "attempt")} {lastResult.model_attempt}</span>
          {lastResult.replayed && <span>{t("已重放", "replayed")}</span>}
        </div>
      )}
      {cancel.error && <div className="inline-warning" role="alert">
        {cancel.error instanceof Error ? cancel.error.message : t("取消模型调用失败", "Model call cancellation failed")}
      </div>}
    </div>
  );
}

export function DelegationsPanel({ client, runID }: ProjectionProps) {
  const { t } = useLocale();
  const query = usePagedResource<DelegationView>(client, ["run", runID, "delegations"],
    `/runs/${encodeURIComponent(runID)}/delegations`, { limit: 50 });
  const items = useMemo(() => query.data?.pages.flatMap((page) => page.items) ?? [], [query.data]);
  if (query.isLoading) return <LoadingState label="加载委派" />;
  if (query.isError) return <ErrorState error={query.error} />;
  if (items.length === 0) return <EmptyState>暂无委派提案</EmptyState>;
  return (
    <div className="projection-stack">
      {items.map((item) => (
        <article className="delegation-item" key={item.id}>
          <header className="projection-header">
            <div><span className="projection-kicker"><Network aria-hidden="true" size={14} />{shortID(item.id)}</span><strong>{item.assignments.map((entry) => entry.title).join(" / ")}</strong></div>
            <div className="status-line"><StatusBadge status={item.status} />{item.review && <StatusBadge status={item.review.decision} />}{item.application && <StatusBadge status={item.application.status} />}</div>
          </header>
          <div className="assignment-list">
            {item.assignments.map((assignment) => (
              <section key={assignment.ordinal}>
                <header><span>#{assignment.ordinal}</span><strong>{assignment.title}</strong>{assignment.application_status && <StatusBadge status={assignment.application_status} />}</header>
                <p>{assignment.goal}</p>
                <footer><span>{assignment.skills.join(" · ")}</span><span>{t(`${formatNumber(assignment.turn_limit)} 回合 / ${formatNumber(assignment.token_limit)} Tokens`, `${formatNumber(assignment.turn_limit)} turns / ${formatNumber(assignment.token_limit)} tokens`)}</span>{assignment.agent_id && <code>{shortID(assignment.agent_id)}</code>}</footer>
              </section>
            ))}
          </div>
          <dl className="projection-metrics">
            <Metric label={t("审阅", "Review")} value={item.review ? `${item.review.decision} · ${item.review.reviewed_by}` : t("待处理", "pending")} />
            <Metric label={t("应用", "Application")} value={item.application?.status ?? "-"} />
            {item.application && <Metric label={t("准入上限", "Admission caps")} value={`${item.application.max_children} child · ${formatNumber(item.application.max_turns_per_child)} 回合 · ${formatNumber(item.application.max_tokens_per_child)} Tokens`} />}
            {item.application?.stop_code && <Metric label={t("停止原因", "Stop code")} value={item.application.stop_code} />}
            <Metric label={t("调度", "Schedule")} value={item.latest_schedule?.status ?? (item.latest_schedule ? t("已请求", "requested") : "-")} />
            <Metric label={t("创建时间", "Created")} value={formatDate(item.created_at)} />
          </dl>
        </article>
      ))}
      <LoadMoreButton hasNextPage={Boolean(query.hasNextPage)} isFetching={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()} />
    </div>
  );
}

export function FanoutPanel({ client, runID }: ProjectionProps) {
  const { t } = useLocale();
  const query = usePagedResource<FanoutPlanView>(client, ["run", runID, "fanout"],
    `/runs/${encodeURIComponent(runID)}/fanout-plans`, { limit: 50 });
  const items = useMemo(() => query.data?.pages.flatMap((page) => page.items) ?? [], [query.data]);
  if (query.isLoading) return <LoadingState label="加载 Fan-out" />;
  if (query.isError) return <ErrorState error={query.error} />;
  if (items.length === 0) return <EmptyState>暂无只读 Fan-out 计划</EmptyState>;
  return (
    <div className="projection-stack">
      {items.map((plan) => (
        <article className="fanout-item" key={plan.id}>
          <header className="projection-header">
            <div><span className="projection-kicker"><ScanSearch aria-hidden="true" size={14} />{shortID(plan.id)}</span><strong>{plan.goal}</strong></div>
            <StatusBadge status={plan.latest_execution?.status ?? plan.status} />
          </header>
          <dl className="projection-metrics">
            <Metric label={t("档位", "Tier")} value={`${plan.requested_tier} → ${plan.effective_parallelism}`} />
            <Metric label={t("范围", "Scope")} value={plan.scope_path} />
            <Metric label={t("分片数", "Shards")} value={formatNumber(plan.shard_count)} />
            {plan.latest_execution && <Metric label={t("单分片输出上限", "Max output/shard")} value={formatNumber(plan.latest_execution.max_output_tokens_per_shard)} />}
            {plan.latest_execution?.stop_code && <Metric label={t("停止原因", "Stop code")} value={plan.latest_execution.stop_code} />}
            <Metric label={t("文件", "Files")} value={formatNumber(plan.file_count)} />
            <Metric label={t("输入", "Input")} value={formatBytes(plan.total_bytes)} />
            <Metric label={t("已排除", "Excluded")} value={formatNumber(plan.excluded_count)} />
          </dl>
          <FanoutExecutions client={client} runID={runID} planID={plan.id} />
        </article>
      ))}
      <LoadMoreButton hasNextPage={Boolean(query.hasNextPage)} isFetching={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()} />
    </div>
  );
}

function FanoutExecutions({ client, runID, planID }: { client: APIClient; runID: string; planID: string }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["run", runID, "fanout-executions", planID],
    queryFn: ({ signal }) => client.getRunFanoutExecutions(runID, planID, signal),
  });
  const cancel = useMutation({
    mutationFn: (executionID: string) => client.cancelRunFanoutExecution(runID, executionID, {
      version: "readonly_fanout_cancel.v1", confirm_cancel: true,
    }, `web-fanout-cancel-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "fanout"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "fanout-executions", planID] });
    },
  });
  if (query.isLoading) return <LoadingState label="加载执行历史" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  if (query.data.items.length === 0) return <div className="projection-placeholder">尚未执行</div>;
  return (
    <div className="fanout-executions">
      <h4>{t("执行历史", "Execution history")}</h4>
      {query.data.items.map((execution) => (
        <section className="fanout-execution-item" key={execution.id}>
          <header>
            <span className="projection-kicker">{shortID(execution.id)}</span>
            <StatusBadge status={execution.status} />
            <span>{formatDate(execution.started_at)}</span>
            {client.hasControl && execution.status === "running" && (
              <button className="command-button danger" disabled={cancel.isPending}
                onClick={() => cancel.mutate(execution.id)} type="button">
                {cancel.isPending
                  ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                  : <Ban aria-hidden="true" size={15} />}
                {t("取消执行", "Cancel execution")}
              </button>
            )}
          </header>
          <ShardTable execution={execution} />
          {execution.stop_code && <div className="projection-placeholder">{execution.stop_code}</div>}
        </section>
      ))}
      {cancel.error && <div className="inline-warning" role="alert">
        {cancel.error instanceof Error ? cancel.error.message : t("取消 Fan-out 执行失败", "Fan-out execution cancellation failed")}
      </div>}
    </div>
  );
}

export function ChildTasksPanel({ client, runID }: ProjectionProps) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["run", runID, "child-tasks"],
    queryFn: ({ signal }) => client.getRunChildTaskProposals(runID, signal),
  });
  const [tier, setTier] = useState("2");
  const [busy, setBusy] = useState("");
  const review = useMutation({
    mutationFn: ({ proposalID, action }: { proposalID: string; action: "approve" | "deny" }) =>
      client.reviewRunChildTaskProposal(runID, proposalID, {
        version: "child_task_review.v1", action, reviewer: "web_operator",
        fanout_tier: tier, confirm_review: true,
      }, `web-child-task-review-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "child-tasks"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] });
    },
  });
  const admit = useMutation({
    mutationFn: (proposalID: string) => client.admitRunChildTaskProposal(runID, proposalID, {
      version: "child_task_admit.v1", confirm_admit: true,
    }, `web-child-task-admit-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "child-tasks"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "fanout"] });
    },
  });
  if (query.isLoading) return <LoadingState label="加载子任务提案" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  if (query.data.items.length === 0) return <EmptyState>暂无子任务提案</EmptyState>;
  return (
    <div className="projection-stack">
      {query.data.items.map((item) => (
        <article className="delegation-item" key={item.id}>
          <header className="projection-header">
            <div><span className="projection-kicker"><Network aria-hidden="true" size={14} />{shortID(item.id)}</span><strong>{item.tasks.map((task) => task.title).join(" / ")}</strong></div>
            <div className="status-line"><StatusBadge status={item.status} /><span>{item.surface}{item.fanout_tier ? ` · 档位 ${item.fanout_tier}` : ""}</span></div>
          </header>
          <div className="assignment-list">
            {item.tasks.map((task) => (
              <section key={task.ordinal}>
                <header><span>#{task.ordinal}</span><strong>{task.title}</strong></header>
                <p>{task.goal}</p>
                <footer><span>{task.skills.join(" · ")}</span><span>{t(`${formatNumber(task.turn_limit)} 回合 / ${formatNumber(task.token_limit)} Tokens / ${formatNumber(task.timeout_millis)} ms`, `${formatNumber(task.turn_limit)} turns / ${formatNumber(task.token_limit)} tokens / ${formatNumber(task.timeout_millis)} ms`)}</span>{task.dependency_ordinals.length > 0 && <code>{`依赖 #${task.dependency_ordinals.join(", #")}`}</code>}</footer>
              </section>
            ))}
          </div>
          {item.assignments.length > 0 && (
            <dl className="projection-metrics">
              {item.assignments.map((assignment) => (
                <Metric key={assignment.ordinal} label={`#${assignment.ordinal}`} value={assignment.status + (assignment.admitted_agent_id ? ` · ${shortID(assignment.admitted_agent_id)}` : "")} />
              ))}
            </dl>
          )}
          {client.hasControl && item.status === "proposed" && (
            <div className="run-execution-control">
              <label htmlFor={`child-task-tier-${item.id}`}>{t("fan-out 档位上限", "Fan-out tier ceiling")}</label>
              <select id={`child-task-tier-${item.id}`} onChange={(event) => setTier(event.target.value)} value={tier}>
                {["1", "2", "4", "6"].map((value) => <option key={value} value={value}>{value}</option>)}
              </select>
              <button className="command-button" disabled={review.isPending || admit.isPending}
                onClick={() => { setBusy(item.id); review.mutate({ proposalID: item.id, action: "approve" }); }} type="button">
                {t("批准", "Approve")}
              </button>
              <button className="command-button danger" disabled={review.isPending || admit.isPending}
                onClick={() => { setBusy(item.id); review.mutate({ proposalID: item.id, action: "deny" }); }} type="button">
                {t("拒绝", "Deny")}
              </button>
            </div>
          )}
          {client.hasControl && item.status === "approved" && (
            <div className="run-execution-control">
              <button className="command-button" disabled={admit.isPending}
                onClick={() => admit.mutate(item.id)} type="button">
                {admit.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> : <Network aria-hidden="true" size={15} />}
                {t("准入子任务", "Admit child tasks")}
              </button>
            </div>
          )}
          {(review.isError || admit.isError) && <div className="inline-warning" role="alert">
            {(review.isError ? review.error : admit.error) instanceof Error
              ? String((review.isError ? review.error : admit.error))
              : t("子任务审阅失败", "Child task review failed")}
          </div>}
          {busy === item.id && (review.isPending || admit.isPending) && <span className="projection-placeholder">{t("处理中", "Processing")}</span>}
        </article>
      ))}
    </div>
  );
}

export function BatchDeliveriesPanel({ client, runID }: ProjectionProps) {
  const { t } = useLocale();
  const [showProposals, setShowProposals] = useState(false);
  const query = useQuery({
    queryKey: ["run", runID, "batch-deliveries"],
    queryFn: ({ signal }) => client.getRunBatchDeliveries(runID, signal),
  });
  if (query.isLoading) return <LoadingState label={t("加载批量交付", "Loading batch deliveries")} />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  return (
    <section className="detail-section" aria-label={t("可交付子智能体", "Deliverable children")}>
      <div className="section-heading">
        <h2><GitBranch aria-hidden="true" size={15} />{t("批量交付", "Batch delivery")}</h2>
        <span>{t(`${formatNumber(query.data.items.length)} 个计划`, `${formatNumber(query.data.items.length)} plans`)}</span>
      </div>
      <p className="projection-placeholder">{client.hasBatchDeliveryHostValidation
        ? t("Go/npm 验证已启用，将使用当前 Run 所选的 Local、Docker 或 SBX 环境；环境未就绪时拒绝执行。开始前需核对当前执行状态、Full 进程激活和权限。",
          "Go/npm validation uses the current Run's selected Local, Docker, or SBX environment and refuses execution when it is not ready. Check the current execution state, Full process activation, and permissions before starting.")
        : t("当前可检查 Git 差异。运行 Go/npm 验证需要完成下方执行配置。",
          "Git diff checks are available. Complete the execution settings below to run Go/npm validation.")}</p>
      <details><summary>{t("核对批量交付配置", "Review batch delivery settings")}</summary>
        <p>{t("Go/npm 验证需要明确启用 --enable-batch-validation-execution、--enable-permission-control 和 --enable-danger-full-access，并连接批量交付控制、控制凭证和操作员审批能力。开始时，所选执行须正在运行且具有当前 Full 进程激活。",
          "Go/npm validation requires explicit --enable-batch-validation-execution, --enable-permission-control, and --enable-danger-full-access settings, plus batch delivery control, a control credential, and operator approval capability. The selected execution must be running with current Full process activation at the start.")}</p>
      </details>
      {!client.hasBatchDeliveryControl && <p className="projection-placeholder">{t(
        "当前可查看批量交付。Desktop 明确启用 --enable-batch-delivery-control 并连接控制凭证后，可验收、要求修改、合并或检查恢复状态。Go/npm 验证需另行完成上述配置。",
        "Batch deliveries are available for review. Explicitly enable --enable-batch-delivery-control in Desktop and connect a control credential to accept, request changes, merge, or reconcile. Configure Go/npm validation separately as described above.",
      )}</p>}
      {client.hasBatchDeliveryControl && typeof client.getRunChildTaskProposals === "function" && <>
        <BatchPreparation key={`${client.baseURL}/${runID}`} client={client} runID={runID}
          usedProposalIDs={query.data.items.map((plan) => plan.proposal_id)} onReviewProposals={() => setShowProposals(!showProposals)} />
        {showProposals && <ChildTasksPanel client={client} runID={runID} />}
      </>}
      {query.data.items.length === 0 ?
        <EmptyState>{t("还没有批量交付计划。可在对话中提出需要独立完成并合并的子任务。", "No batch delivery plans yet. Describe independently deliverable subtasks in the conversation to prepare one.")}</EmptyState> :
        <div className="projection-stack">
          {query.data.items.map((plan) =>
            <BatchDeliveryDetail client={client} key={plan.id} planID={plan.id} runID={runID} />)}
        </div>}
    </section>
  );
}

function BatchDeliveryDetail({ client, runID, planID }: ProjectionProps & { planID: string }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [reviews, setReviews] = useState<Record<string, { summary: string; confirmed: boolean }>>({});
  const [baseReplayConfirmed, setBaseReplayConfirmed] = useState(false);
  const [busy, setBusy] = useState("");
  const query = useQuery({
    queryKey: ["run", runID, "batch-delivery", planID],
    queryFn: ({ signal }) => client.getRunBatchDelivery(runID, planID, signal),
  });
  const action = useMutation({
    mutationFn: async (command: { kind: "accept" | "changes" | "merge" | "cancel" | "reconcile";
      child?: BatchDeliverySnapshotView["children"][number] }) => {
      const operation = `web-batch-${command.kind}-${globalThis.crypto.randomUUID()}`;
      switch (command.kind) {
        case "accept":
        case "changes": {
          const child = command.child;
          if (!child) throw new Error("A child delivery is required");
          const review = reviews[`${child.workspace.ordinal}/${child.workspace.generation}/${child.receipt?.id}`];
          if (!review?.confirmed || !review.summary.trim()) throw new Error("Confirm the independent review of this delivery");
          return client.reviewRunBatchDeliveryChild(runID, planID,
            child.workspace.ordinal, {
              version: "batch_delivery_review_control.v1",
              generation: child.workspace.generation,
              reviewer: "web_batch_delivery_reviewer",
              verdict: command.kind === "accept" ? "accepted" : "changes_requested",
              summary: review.summary.trim(), full_diff_reviewed: true,
              call_chain_reviewed: true, tests_reviewed: true,
            }, operation);
        }
        case "merge":
          return client.mergeRunBatchDelivery(runID, planID, {
            version: "batch_delivery_merge.v1",
            ordered_ordinals: query.data?.plan.spec.tasks.map((task) => task.ordinal) ?? [],
            confirm_replay: baseReplayConfirmed, confirm: true,
          }, operation);
        case "cancel":
          return client.cancelRunBatchDelivery(runID, planID, {
            version: "batch_delivery_cancel.v1",
            reason: "operator cancelled from the Desktop batch delivery panel",
            confirm: true,
          }, operation);
        case "reconcile":
          return client.reconcileRunBatchDelivery(runID, planID, {
            version: "batch_delivery_reconcile.v1", confirm: true,
          });
      }
    },
    onSuccess: () => {
      setReviews({});
      setBaseReplayConfirmed(false);
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "batch-deliveries"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "batch-delivery", planID] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "batch-workbench", planID] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] });
    },
  });
  if (query.isLoading) return <LoadingState label={t("加载交付详情", "Loading delivery details")} />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  const snapshot = query.data;
  const accepted = snapshot.children.length > 0 &&
    snapshot.children.every((child) => child.workspace.status === "accepted" ||
      child.workspace.status === "merged");
  const terminal = snapshot.plan.status === "completed" || snapshot.plan.status === "aborted";
  const cleanupPending = snapshot.plan.status === "aborted" && (
    snapshot.children.some((child) => !["merged", "cancelled", "failed", "orphaned"].includes(child.workspace.status)) ||
    (snapshot.merge_queue != null && !["completed", "aborted"].includes(snapshot.merge_queue.status))
  );
  return (
    <article className="delegation-item">
      <header className="projection-header">
        <div><span className="projection-kicker"><GitBranch aria-hidden="true" size={14} />
          {shortID(snapshot.plan.id)}</span><strong>{t("隔离 Worktree 交付", "Isolated worktree delivery")}</strong></div>
        <div className="status-line"><StatusBadge status={snapshot.plan.status} />
          <code>{snapshot.plan.base_commit.slice(0, 10)}</code></div>
      </header>
      <dl className="projection-metrics">
        <Metric label={t("源分支", "Source branch")} value={snapshot.plan.source_branch} />
        <Metric label={t("子任务", "Children")} value={formatNumber(snapshot.children.length)} />
        <Metric label={t("合并进度", "Merge progress")} value={snapshot.merge_queue ?
          `${formatNumber(snapshot.merge_queue.next_index)}/${formatNumber(snapshot.merge_queue.ordered_ordinals.length)}` : "-"} />
      </dl>
      <div className="assignment-list">
        {snapshot.children.map((child) => {
          const profile = child.workspace.tool_profile;
          const reviewKey = `${child.workspace.ordinal}/${child.workspace.generation}/${child.receipt?.id}`;
          const review = reviews[reviewKey] ?? { summary: "", confirmed: false };
          return <section key={child.workspace.ordinal}>
            <header><span>#{child.workspace.ordinal}</span>
              <strong>{shortID(child.workspace.agent_id)}</strong>
              <StatusBadge status={child.workspace.status} /></header>
            <p><code>{child.workspace.branch}</code> · {t("代次", "generation")} {formatNumber(child.workspace.generation)} · {t("租约至", "lease until")} {formatDate(child.workspace.lease_expires_at)}</p>
            <footer>
              <span>{t("开放", "Allowed")}: {[
                profile.workspace_list && "List", profile.workspace_read && "Read",
                profile.workspace_search && "Search", profile.workspace_change && "Change",
                profile.workspace_apply && "Apply", profile.git_status && "Git status",
                profile.git_diff && "Git diff", profile.git_commit && "Git commit",
              ].filter(Boolean).join(" · ")}</span>
              <span>{t("执行范围：上方项目工具；删除、Shell、进程、网络、凭证、调试终端与子派生保持关闭",
                "Execution scope: the project tools above; delete, shell, process, network, credentials, debug terminal, and spawning remain disabled")}</span>
            </footer>
            {child.receipt && <div className="projection-placeholder">
              <strong>{t("交付结果", "Delivery result")}</strong> · {child.receipt.diff_stat} ·
              {` ${formatBytes(child.receipt.diff_bytes)}`}<br />
              {child.receipt.changed_files.map((path) => <code key={path}>{path} </code>)}<br />
              {t("验证", "Validation")}: {child.receipt.test_receipts.map((test) =>
                `${test.requirement_id}:${test.exit_code === 0 ? "pass" : "fail"}`).join(" · ")}
            </div>}
            {child.review && <div className="projection-placeholder">
              <StatusBadge status={child.review.verdict} /> {child.review.summary} · {shortID(child.review.reviewer)}
            </div>}
            {client.hasBatchDeliveryControl && !terminal && typeof client.getBatchWorkbench === "function" &&
              <BatchWorkbenchControls key={`${client.baseURL}/${runID}/${planID}/${child.workspace.ordinal}`}
                client={client} runID={runID} planID={planID} ordinal={child.workspace.ordinal} />}
            {child.mailbox.length > 0 && <ol className="projection-placeholder">
              {child.mailbox.slice(-8).map((message) => <li key={message.id}>
                <StatusBadge status={message.kind} /> {message.summary} · {formatDate(message.created_at)}
              </li>)}
            </ol>}
            {client.hasBatchDeliveryControl && !terminal && child.workspace.status === "ready_for_review" &&
              <div className="run-execution-control">
                <label htmlFor={`batch-review-${planID}-${child.workspace.ordinal}`}>
                  {t("独立审阅摘要", "Independent review summary")}</label>
                <input id={`batch-review-${planID}-${child.workspace.ordinal}`}
                  className="batch-review-input"
                  onChange={(event) => setReviews({ ...reviews, [reviewKey]: { ...review, summary: event.target.value } })}
                  placeholder={t("必填", "Required")} value={review.summary} />
                <label className="checkbox-row"><input checked={review.confirmed}
                  onChange={(event) => setReviews({ ...reviews, [reviewKey]: { ...review, confirmed: event.target.checked } })} type="checkbox" />
                  {t("我已独立核对与共同基线的完整差异、调用链和测试结果",
                    "I independently reviewed the full merge-base diff, call chain, and test results")}
                </label>
                <button className="command-button" disabled={action.isPending ||
                  !review.summary.trim() || !review.confirmed}
                  onClick={() => { setBusy(`accept-${child.workspace.ordinal}`); action.mutate({ kind: "accept", child }); }} type="button">
                  {t("接受", "Accept")}</button>
                <button className="command-button danger" disabled={action.isPending ||
                  !review.summary.trim() || !review.confirmed}
                  onClick={() => { setBusy(`changes-${child.workspace.ordinal}`); action.mutate({ kind: "changes", child }); }} type="button">
                  {t("要求修改", "Request changes")}</button>
              </div>}
            {busy.endsWith(`-${child.workspace.ordinal}`) && action.isPending &&
              <span className="projection-placeholder">{t("正在重新检查完整差异与测试", "Rechecking full diff and tests")}</span>}
          </section>;
        })}
      </div>
      {snapshot.merge_queue && <div className="projection-placeholder">
        <StatusBadge status={snapshot.merge_queue.status} /> {t("合并队列", "Merge queue")}
        {snapshot.merge_queue.failure_code && <> · <strong>{snapshot.merge_queue.failure_code}</strong>
          {snapshot.merge_queue.failure_summary ? `: ${snapshot.merge_queue.failure_summary}` : ""}</>}
      </div>}
      {client.hasBatchDeliveryControl && (!terminal || cleanupPending) && <div className="run-execution-control">
        {accepted && !terminal && <>
          <label className="checkbox-row"><input checked={baseReplayConfirmed}
            onChange={(event) => setBaseReplayConfirmed(event.target.checked)} type="checkbox" />
            {t("若源分支已前进，确认将已复核交付重放到最新 base",
              "If the source advanced, confirm replaying reviewed deliveries onto the latest base")}
          </label>
          <button className="command-button" disabled={action.isPending}
            onClick={() => { setBusy("merge"); action.mutate({ kind: "merge" }); }} type="button">
            {t("按依赖顺序合并", "Merge in dependency order")}</button>
        </>}
        {!terminal && <button className="command-button" disabled={action.isPending}
          onClick={() => { setBusy("reconcile"); action.mutate({ kind: "reconcile" }); }} type="button">
          {t("核对并恢复状态", "Reconcile state")}</button>}
        <button className="command-button danger" disabled={action.isPending}
          onClick={() => { setBusy("cancel"); action.mutate({ kind: "cancel" }); }} type="button">
          {cleanupPending ? t("重试取消清理", "Retry cancellation cleanup") : t("取消并保留现场", "Cancel and preserve state")}</button>
      </div>}
      {action.isPending && !busy.includes("-") && <span className="projection-placeholder">
        <LoaderCircle aria-hidden="true" className="spin" size={15} /> {t("处理中", "Working")}</span>}
      {action.isError && <div className="inline-warning" role="alert">
        {action.error instanceof Error ? action.error.message : t("批量交付操作失败", "Batch delivery operation failed")}
        <p>{cleanupPending
          ? t("刷新交付状态；待验证 Job 和 Run 操作结束后，重试取消清理。", "Refresh the delivery state, then retry cancellation cleanup after the validation Job and Run operation end.")
          : t("请核对最新交付状态。结果待确认时，使用「核对并恢复状态」检查原操作。", "Check the latest delivery state. When the outcome needs confirmation, use Reconcile state to inspect the original operation.")}</p>
        <button className="compact-command" onClick={() => void query.refetch()} type="button">{t("刷新交付状态", "Refresh delivery state")}</button>
      </div>}
    </article>
  );
}

function ShardTable({ execution }: { execution: NonNullable<FanoutPlanView["latest_execution"]> }) {
  const { t } = useLocale();
  return (
    <div className="table-scroll shard-table"><table><thead><tr><th>{t("分片", "Shard")}</th><th>{t("状态", "Status")}</th><th>{t("模型", "Model")}</th><th>{t("尝试", "Attempt")}</th><th>{t("输入", "In")}</th><th>{t("输出", "Out")}</th><th>{t("发现", "Findings")}</th><th>{t("耗时", "Duration")}</th><th>{t("错误", "Error")}</th></tr></thead><tbody>
      {execution.shards.map((shard) => <tr key={shard.ordinal}><td>#{shard.ordinal}</td><td><StatusBadge status={shard.status} /></td><td>{shard.provider && shard.model ? `${shard.provider}/${shard.model}` : "-"}</td><td>{formatNumber(shard.current_attempt)}/{formatNumber(shard.attempt_count)}</td><td>{formatNumber(shard.input_tokens)}</td><td>{formatNumber(shard.output_tokens)}</td><td>{formatNumber(shard.finding_count)}</td><td>{formatNumber(shard.elapsed_millis)} ms</td><td>{shard.error_code || "-"}</td></tr>)}
    </tbody></table></div>
  );
}

export function FindingsPanel({ client, runID }: ProjectionProps) {
  const { t } = useLocale();
  const listQuery = usePagedResource<FindingReportSummaryView>(client, ["run", runID, "reports"],
    `/runs/${encodeURIComponent(runID)}/reports`, { limit: 50 });
  const reports = useMemo(() => listQuery.data?.pages.flatMap((page) => page.items) ?? [], [listQuery.data]);
  const [selectedID, setSelectedID] = useState("");
  useEffect(() => {
    if (reports.length > 0 && !reports.some((report) => report.id === selectedID)) setSelectedID(reports[0].id);
  }, [reports, selectedID]);
  const detailQuery = useQuery({
    queryKey: ["run", runID, "report", selectedID],
    queryFn: ({ signal }) => client.get<FindingReportView>(`/runs/${encodeURIComponent(runID)}/reports/${encodeURIComponent(selectedID)}`, {}, signal),
    enabled: selectedID !== "",
  });
  if (listQuery.isLoading) return <LoadingState label="加载 Finding 报告" />;
  if (listQuery.isError) return <ErrorState error={listQuery.error} />;
  if (reports.length === 0) return <EmptyState>暂无 Finding 报告</EmptyState>;
  return (
    <div className="finding-layout">
      <aside className="report-picker" aria-label={t("Finding 报告", "Finding reports")}>
        {reports.map((report) => (
          <button className={selectedID === report.id ? "selected" : ""} key={report.id} onClick={() => setSelectedID(report.id)} type="button">
            <span><FileSearch aria-hidden="true" size={14} />{shortID(report.id)}</span>
            <strong>{report.title}</strong>
            <small>{t(`${report.finding_count} 项发现 · ${report.severity.critical + report.severity.high} 项高危以上`, `${report.finding_count} findings · ${report.severity.critical + report.severity.high} high+`)}</small>
          </button>
        ))}
        <LoadMoreButton hasNextPage={Boolean(listQuery.hasNextPage)} isFetching={listQuery.isFetchingNextPage} onClick={() => void listQuery.fetchNextPage()} />
      </aside>
      <section className="finding-detail">
        {detailQuery.isLoading && <LoadingState label="加载报告详情" />}
        {detailQuery.isError && <ErrorState error={detailQuery.error} />}
        {detailQuery.data && <FindingList report={detailQuery.data} />}
      </section>
    </div>
  );
}

function FindingList({ report }: { report: FindingReportView }) {
  const { t } = useLocale();
  if (report.findings.length === 0) return <EmptyState>报告没有 Finding</EmptyState>;
  return <div className="projection-stack">{report.findings.map((finding) => (
    <article className="finding-item" key={finding.id}>
      <header className="projection-header"><div><span className="projection-kicker"><ShieldAlert aria-hidden="true" size={14} />#{finding.ordinal} · {finding.category}</span><strong>{finding.title}</strong></div><div className="status-line"><StatusBadge status={finding.severity} /><StatusBadge status={finding.status} /></div></header>
      <p>{finding.detail}</p>
      <dl className="projection-metrics">
        <Metric label={t("位置", "Location")} value={`${finding.relative_path}:${finding.line_start || "-"}`} />
        <Metric label={t("置信度", "Confidence")} value={`${finding.confidence}%`} />
        <Metric label={t("证据", "Evidence")} value={t(`${finding.evidence.length} 模型 / ${finding.lifecycle.validation_evidence_count} 验证`, `${finding.evidence.length} model / ${finding.lifecycle.validation_evidence_count} validation`)} />
        <Metric label={t("修复", "Remediation")} value={formatNumber(finding.lifecycle.remediation_evidence_count)} />
      </dl>
    </article>
  ))}</div>;
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value || "-"}</dd></div>;
}

interface ProjectionProps { client: APIClient; runID: string }
