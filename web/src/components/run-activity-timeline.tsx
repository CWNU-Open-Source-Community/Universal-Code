import {
  Bot,
  Check,
  ChevronRight,
  CircleDot,
  FilePenLine,
  Globe,
  ListChecks,
  MessageSquareText,
  Network,
  ShieldCheck,
  UserRound,
  Wrench,
} from "lucide-react";
import type { PublicModelStreamSnapshot, RunActivityItemView, RunActivityView } from "../api/types";
import type { PublicModelStreamStatus } from "../hooks/use-public-model-stream";
import { formatDate } from "../lib/format";
import { useLocale } from "../lib/locale";
import { SafeMarkdown } from "./safe-markdown";

type Translator = (chinese: string, english: string) => string;

const sourceLabels: Record<RunActivityItemView["source"], [string, string]> = {
  harness: ["Harness 事件", "Harness event"],
  model: ["模型公开更新", "Public model update"],
  operator: ["用户", "Operator"],
};

const statusLabels: Record<string, [string, string]> = {
  approved: ["已批准", "Approved"],
  blocked: ["已阻止", "Blocked"],
  cancelled: ["已取消", "Cancelled"],
  cancelling: ["取消中", "Cancelling"],
  completed: ["已完成", "Completed"],
  denied: ["已拒绝", "Denied"],
  expired: ["已超时", "Expired"],
  failed: ["失败", "Failed"],
  pending: ["待处理", "Pending"],
  // This table labels saved Harness disclosures, never live tool preparation.
  running: ["记录时执行中", "Executing when recorded"],
  satisfied: ["已满足", "Satisfied"],
  selected: ["已选择", "Selected"],
  superseded: ["已替换", "Superseded"],
  waiting: ["等待中", "Waiting"],
};

function ActivityIcon({ kind, source }: Pick<RunActivityItemView, "kind" | "source">) {
  if (source === "model") return <Bot aria-hidden="true" size={16} />;
  if (source === "operator") return <UserRound aria-hidden="true" size={16} />;
  switch (kind) {
  case "approval":
    return <ShieldCheck aria-hidden="true" size={16} />;
  case "file_change":
    return <FilePenLine aria-hidden="true" size={16} />;
  case "plan":
    return <ListChecks aria-hidden="true" size={16} />;
  case "tool_call":
    return <Wrench aria-hidden="true" size={16} />;
  case "model_call":
    return <MessageSquareText aria-hidden="true" size={16} />;
  case "dependency":
    return <Network aria-hidden="true" size={16} />;
  case "browser":
    return <Globe aria-hidden="true" size={16} />;
  default:
    return <CircleDot aria-hidden="true" size={15} />;
  }
}

export function RunActivityTimeline({ activity, liveCommentary = null,
  liveStatus = "stopped", streamError = "" }: {
  activity: RunActivityView;
  liveCommentary?: PublicModelStreamSnapshot | null;
  liveStatus?: PublicModelStreamStatus;
  streamError?: string;
}) {
  const { t } = useLocale();
  if (activity.private_reasoning_included) {
    return (
      <section aria-label={t("Run 活动", "Run activity")} className="run-activity">
        <div className="inline-warning">
          {t("活动投影已拒绝：服务端声明其中包含模型私有推理。",
            "Activity projection rejected: the server declared private model reasoning.")}
        </div>
      </section>
    );
  }
  const provisional = liveCommentary?.content_kind === "tool_commentary" &&
    liveCommentary.text.trim() &&
    !hasDurablePublicUpdate(activity.items, liveCommentary)
    ? provisionalActivityItem(activity, liveCommentary, liveStatus) : null;
  const liveTools = liveCommentary?.items.filter((item) => item.type === "tool_call") ?? [];
  return (
    <section aria-label={t("Run 活动", "Run activity")} className="run-activity">
      <header className="run-activity-header">
        <div>
          <h2>{t("活动", "Activity")}</h2>
          <p>{t("公开模型更新与 Go 记录的执行事实",
            "Public model updates and execution facts recorded by Go")}</p>
        </div>
        <span className="run-activity-safety"
          title={t("显示公开摘要和已保存的操作事件",
            "Shows public summaries and saved operation events")}>
          <ShieldCheck aria-hidden="true" size={15} />
          {t("公开活动", "Public activity")}
        </span>
      </header>
      {streamError && <div className="inline-warning">
        {t("活动流连接", "Activity stream")}: {streamError}
      </div>}
      {activity.truncated && (
        <div className="run-activity-window">
          {t(`当前显示最近一段活动，已读取到事件 #${activity.through_sequence}`,
            `Showing recent activity through event #${activity.through_sequence}`)}
        </div>
      )}
      {activity.items.length === 0 && !provisional && liveTools.length === 0 ? (
        <div className="run-activity-empty">
          <MessageSquareText aria-hidden="true" size={21} />
          <span>{t("还没有公开活动", "No public activity yet")}</span>
        </div>
      ) : (
        <ol className="run-activity-list">
          {groupActivityItems(activity.items).map((entry) => entry.type === "message" ?
            <ActivityMessage item={entry.item} key={entry.item.id} t={t} /> :
            <HarnessDisclosure items={entry.items} key={entry.id} t={t} />)}
          {provisional && <ActivityMessage item={provisional} key={provisional.id}
            provisional t={t} />}
          {liveTools.map((item) => <LiveToolPreparation item={item} key={item.id} t={t} />)}
        </ol>
      )}
    </section>
  );
}

function LiveToolPreparation({ item, t }: {
  item: PublicModelStreamSnapshot["items"][number];
  t: Translator;
}) {
  const labels: Record<string, [string, string]> = {
    in_progress: ["正在准备调用", "Preparing call"],
    ready_for_validation: ["参数已就绪，等待验证", "Arguments ready; awaiting validation"],
    completed: ["已准备，正在提交验证", "Prepared; submitting for validation"],
    failed: ["准备失败", "Preparation failed"],
    cancelled: ["准备已取消", "Preparation cancelled"],
  };
  const label = labels[item.status] ?? [item.status, item.status];
  return (
    <li className="run-activity-item source-model is-provisional live-tool-preparation">
      <span className="run-activity-marker"><Wrench aria-hidden="true" size={16} /></span>
      <div className="run-activity-body">
        <div className="run-activity-meta">
          <span className="run-activity-source source-model">{t("模型工具请求", "Model tool request")}</span>
          <span className="run-activity-live"><span aria-hidden="true" />{t("临时", "Live")}</span>
        </div>
        <h3>{item.tool_name}</h3>
        <div className="run-activity-detail">{t(...label)}
          {item.argument_bytes ? ` · ${item.argument_bytes} ${t("字节", "bytes")}` : ""}
        </div>
        <small>{t("正在准备工具请求，服务端验证通过后执行。这里显示准备状态和参数大小。",
          "Preparing the tool request; execution follows service validation. Preparation status and argument size appear here.")}</small>
      </div>
    </li>
  );
}

type ActivityEntry =
  | { type: "message"; item: RunActivityItemView }
  | { type: "harness"; id: string; items: RunActivityItemView[] };

export function groupActivityItems(items: RunActivityItemView[]): ActivityEntry[] {
  const entries: ActivityEntry[] = [];
  for (const item of items) {
    if (item.source !== "harness") {
      entries.push({ type: "message", item });
      continue;
    }
    const previous = entries.at(-1);
    if (previous?.type === "harness" && previous.items.at(-1)?.kind === item.kind) {
      previous.items.push(item);
      continue;
    }
    entries.push({ type: "harness", id: `harness-${item.id}`, items: [item] });
  }
  return entries;
}

function ActivityMessage({ item, provisional = false, t }: {
  item: RunActivityItemView;
  provisional?: boolean;
  t: Translator;
}) {
  return (
    <li className={`run-activity-item source-${item.source}${provisional ? " is-provisional" : ""}`}>
      <span className="run-activity-marker"><ActivityIcon kind={item.kind} source={item.source} /></span>
      <div className="run-activity-body">
        <div className="run-activity-meta">
          <span className={`run-activity-source source-${item.source}`}>
            {item.verifiable && <Check aria-hidden="true" size={12} />}{t(...sourceLabels[item.source])}
          </span>
          {provisional && <span className="run-activity-live"><span aria-hidden="true" />
            {t("临时", "Live")}</span>}
          <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
          {!provisional && <span className="run-activity-sequence">#{item.sequence}</span>}
        </div>
        <h3>{item.title}</h3>
        {item.detail && (item.source === "model" ?
          <SafeMarkdown className="run-activity-detail">{item.detail}</SafeMarkdown> :
          <div className="run-activity-detail">{item.detail}</div>)}
        {item.source === "model" && <small>{provisional
          ? t("正在生成的公开进度，验证后显示已保存活动。对话历史保存最终确认的内容。",
            "Public progress is being generated; saved activity appears after validation. Conversation history stores the final confirmed content.")
          : t("模型公开回复供你参考；实际操作结果请核对执行记录。",
            "Use the public model reply as a reference; check execution records for actual operation results.")}</small>}
        {item.source === "operator" &&
          <small>{item.instruction_authorized ?
            t("已授权的用户输入", "Authorized operator input") :
            t("非指令证据", "Non-instruction evidence")}</small>}
      </div>
    </li>
  );
}

function hasDurablePublicUpdate(items: RunActivityItemView[], snapshot: PublicModelStreamSnapshot): boolean {
  const expectedToolRound = snapshot.call.tool_round + 1;
  const expectedText = snapshot.text.trim();
  return items.some((item) => item.source === "model" && (
    (item.attempt_id === snapshot.call.attempt_id &&
      item.model_attempt === snapshot.call.model_attempt &&
      item.tool_round === expectedToolRound) ||
    (Boolean(expectedText) && item.created_at >= snapshot.call.started_at &&
      item.detail?.trim() === expectedText)
  ));
}

function provisionalActivityItem(activity: RunActivityView, snapshot: PublicModelStreamSnapshot,
  status: PublicModelStreamStatus): RunActivityItemView {
  const expectedToolRound = snapshot.call.tool_round + 1;
  return {
    id: `public-commentary-${snapshot.call.attempt_id}-${snapshot.call.model_attempt}-${expectedToolRound}`,
    sequence: activity.through_sequence + 1,
    kind: "model_update",
    source: "model",
    title: "Universal-Code",
    detail: snapshot.text.trim(),
    status: status === "finalizing" ? "completed" : "running",
    verifiable: false,
    instruction_authorized: false,
    created_at: snapshot.updated_at,
    attempt_id: snapshot.call.attempt_id,
    model_attempt: snapshot.call.model_attempt,
    tool_round: expectedToolRound,
  };
}

function HarnessDisclosure({ items, t }: { items: RunActivityItemView[]; t: Translator }) {
  const status = disclosureStatus(items);
  const first = items[0];
  const last = items[items.length - 1];
  if (!first || !last) return null;
  const rows = disclosureRows(items, t);
  const defaultOpen = ["blocked", "failed", "pending", "waiting"].includes(status);
  return (
    <li className="run-activity-item source-harness harness-disclosure-item">
      <details className={`run-activity-disclosure status-${status}`} open={defaultOpen}>
        <summary>
          <ChevronRight aria-hidden="true" className="disclosure-chevron" size={15} />
          <ActivityIcon kind={first.kind} source="harness" />
          <strong>{disclosureTitle(first.kind, rows.length, t)}</strong>
          {status && <span className={`run-activity-status status-${status}`}
            title={status === "running" ? t("事件保存时的执行状态。查看当前进度请核对最新活动。", "Execution state when the event was saved. Check the latest activity for current progress.") : undefined}>
            {statusLabels[status] ? t(...statusLabels[status]) : status}
          </span>}
          <time dateTime={last.created_at}>{formatDate(last.created_at)}</time>
        </summary>
        <ol className="run-activity-disclosure-list">
          {rows.map((row) => <li key={row.id}>
            <span className={`disclosure-state status-${row.status}`} aria-hidden="true" />
            <div>
              <strong>{row.title}</strong>
              {row.detail && <p>{row.detail}</p>}
            </div>
            <span className="run-activity-sequence">#{row.sequence}</span>
          </li>)}
        </ol>
      </details>
    </li>
  );
}

function disclosureTitle(kind: RunActivityItemView["kind"], count: number, t: Translator): string {
  const suffix = count > 1 ? t(`${count} 项`, `${count} items`) : "";
  switch (kind) {
  case "tool_call": return t(`运行了 ${Math.max(1, count)} 个操作`,
    `Ran ${Math.max(1, count)} ${count === 1 ? "operation" : "operations"}`);
  case "model_call": return count > 1 ? t(`模型调用 ${count} 次`, `${count} model calls`) :
    t("模型调用", "Model call");
  case "approval": return `${t("审批记录", "Approval record")}${suffix ? ` · ${suffix}` : ""}`;
  case "file_change": return `${t("文件更改", "File change")}${suffix ? ` · ${suffix}` : ""}`;
  case "plan": return `${t("计划记录", "Plan record")}${suffix ? ` · ${suffix}` : ""}`;
  default: return `${t("运行详情", "Run details")}${suffix ? ` · ${suffix}` : ""}`;
  }
}

function disclosureStatus(items: RunActivityItemView[]): string {
  for (const status of ["failed", "blocked", "denied"]) {
    if (items.some((item) => item.status === status)) return status;
  }
  if (items.some((item) => item.title === "工具批次完成" && item.status === "completed")) {
    return "completed";
  }
  for (const status of ["pending", "waiting", "running", "completed"]) {
    if (items.some((item) => item.status === status)) return status;
  }
  return items.at(-1)?.status ?? "";
}

interface DisclosureRow {
  id: string;
  title: string;
  detail: string;
  status: string;
  sequence: number;
}

function disclosureRows(items: RunActivityItemView[], t: Translator): DisclosureRow[] {
  if (items[0]?.kind !== "tool_call") {
    return items.map((item) => ({
      id: item.id, title: item.title, detail: item.detail ?? "",
      status: item.status ?? "", sequence: item.sequence,
    }));
  }
  const results = items.filter((item) => item.title === "工具结果已记录");
  if (results.length > 0) {
    return results.map((item) => ({
      id: item.id, title: item.detail || t("工具操作", "Tool operation"), detail: "",
      status: item.status ?? "", sequence: item.sequence,
    }));
  }
  const batch = items.find((item) => item.title === "工具调用已请求");
  const tools = batch?.detail?.split("、").map((name) => name.trim()).filter(Boolean) ?? [];
  if (batch && tools.length > 0) {
    const status = disclosureStatus(items);
    return tools.map((title, index) => ({
      id: `${batch.id}-operation-${index}`, title, detail: "", status,
      sequence: batch.sequence,
    }));
  }
  return items.filter((item) => item.title !== "工具批次完成").map((item) => ({
    id: item.id, title: item.detail || item.title, detail: "",
    status: item.status ?? "", sequence: item.sequence,
  }));
}
