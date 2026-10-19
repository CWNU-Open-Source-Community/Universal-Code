import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, Search, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { PublicModelStreamSnapshot, ThreadDetailView, ThreadTranscriptItemView } from "../../api/types";
import type { PublicModelStreamStatus } from "../../hooks/use-public-model-stream";
import { StatusLabel } from "../../components/common";
import { LifecycleStatusLabel } from "../../components/lifecycle-status";
import { SafeMarkdown } from "../../components/safe-markdown";
import { mergeThreadTranscriptItems, VirtualTranscriptList,
  type TranscriptReadingPosition } from "../../components/thread-transcript";
import { ActivityItemDetail } from "./activity-detail";
import { V2ImagePreview } from "./image-input";
import "./inspector.css";

export interface V2InspectorProps {
  client: APIClient;
  threadID: string;
  detail: ThreadDetailView;
  durableItems: ThreadTranscriptItemView[];
  liveSnapshot: PublicModelStreamSnapshot | null;
  liveStatus: PublicModelStreamStatus;
  hasOlder: boolean;
  isFetchingOlder: boolean;
  onLoadOlder: () => void;
}

type Filter = "all" | "tools" | "approvals" | "exceptions";
interface InspectorState {
  filter: Filter;
  search: string;
  runID: string;
  selected: { id: string; runID: string } | null;
  position?: TranscriptReadingPosition;
}
const emptyState: InspectorState = { filter: "all", search: "", runID: "", selected: null };
const filterLabels: Record<Filter, string> = { all: "全部", tools: "工具", approvals: "审批", exceptions: "异常" };
const sourceLabels = { harness: "执行记录", model: "模型公开内容", operator: "用户输入", peer: "其他聊天" };
const stageLabels = { started: "已开始", arguments_ready: "参数就绪", running: "运行中", result: "结果", blocked: "已阻塞" };
const translate = (chinese: string) => chinese;
const failureStates = new Set(["failed", "failure", "error", "denied", "deny", "blocked", "cancelled", "canceled",
  "timed_out", "timeout", "interrupted", "killed", "unavailable", "verification_unavailable", "conflict", "stopped"]);

function isException(item: ThreadTranscriptItemView): boolean {
  return item.stage === "blocked" || item.web_evidence?.state === "failed" || failureStates.has((item.status ?? "").toLowerCase()) ||
    failureStates.has((item.activity_summary?.status ?? "").toLowerCase()) ||
    (item.activity_summary?.exit_code !== undefined && item.activity_summary.exit_code !== 0);
}

function inCategory(item: ThreadTranscriptItemView, filter: Filter): boolean {
  if (filter === "tools") return item.kind === "tool_call" || Boolean(item.tool_name || item.detail_available);
  if (filter === "approvals") return item.kind === "approval" || item.activity_type === "approval";
  if (filter === "exceptions") return isException(item);
  return true;
}

/** Read-only content area. Submission, approvals and execution state belong to the shared conversation owner. */
export function V2Inspector(props: V2InspectorProps) {
  return <InspectorContent key={props.threadID} {...props} />;
}

function InspectorContent({ client, threadID, detail, durableItems, liveSnapshot, liveStatus,
  hasOlder, isFetchingOlder, onLoadOlder }: V2InspectorProps) {
  const queryClient = useQueryClient();
  const stateKey = useMemo(() => ["v2", "thread", threadID, "inspector-view"], [threadID]);
  const state = useQuery<InspectorState>({ queryKey: stateKey, queryFn: () => emptyState,
    initialData: emptyState, enabled: false, staleTime: Infinity, gcTime: Infinity }).data;
  const ui = state ?? emptyState;
  const update = useCallback((patch: Partial<InspectorState>) => {
    queryClient.setQueryData<InspectorState>(stateKey, (old) => ({ ...(old ?? emptyState), ...patch }));
  }, [queryClient, stateKey]);
  const onPositionChange = useCallback((position: TranscriptReadingPosition) => {
    queryClient.setQueryData<InspectorState>(stateKey, (old) => {
      const previous = old?.position;
      return previous?.anchorID === position.anchorID && previous.offset === position.offset &&
        previous.nearBottom === position.nearBottom ? old : { ...(old ?? emptyState), position };
    });
  }, [queryClient, stateKey]);
  const items = useMemo(() => mergeThreadTranscriptItems(durableItems, [], liveSnapshot, liveStatus, true),
    [durableItems, liveSnapshot, liveStatus]);
  const runs = useMemo(() => [...new Map(items.map((item) => [item.run_id, item.run_ordinal])).entries()]
    .sort((a, b) => a[1] - b[1]), [items]);
  const visible = useMemo(() => {
    const term = ui.search.trim().toLocaleLowerCase();
    if (ui.filter === "all" && !term) return items.filter((item) => !ui.runID || item.run_id === ui.runID);
    const matches = items.filter((item) => item.sequence !== 0 && (!ui.runID || item.run_id === ui.runID) &&
      inCategory(item, ui.filter) && (!term || [item.title, item.detail, item.tool_name, item.status,
        item.id, item.canonical_id, item.source_ref, item.run_id].some((text) => text?.toLocaleLowerCase().includes(term))));
    const matchedIDs = new Set(matches.map((item) => `${item.run_id}:${item.id}`));
    const matchedRuns = new Set(matches.map((item) => item.run_id));
    return items.filter((item) => item.sequence === 0 ? matchedRuns.has(item.run_id) : matchedIDs.has(`${item.run_id}:${item.id}`));
  }, [items, ui.filter, ui.runID, ui.search]);
  const selected = ui.selected ? items.find((item) => item.id === ui.selected?.id && item.run_id === ui.selected.runID) : undefined;
  const listRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const selectedButton = useRef<HTMLButtonElement | null>(null);
  const restoreFocus = useRef(false);
  const [latestRequest, setLatestRequest] = useState(0);
  useEffect(() => { if (ui.selected) closeRef.current?.focus(); }, [ui.selected?.id, ui.selected?.runID]);
  useLayoutEffect(() => {
    if (ui.selected || !restoreFocus.current) return;
    restoreFocus.current = false;
    if (selectedButton.current?.isConnected) selectedButton.current.focus();
    else listRef.current?.querySelector<HTMLElement>(".thread-transcript-viewport")?.focus();
  }, [ui.selected]);
  const closeDetail = useCallback(() => {
    restoreFocus.current = true;
    update({ selected: null });
  }, [update]);
  const currentRunID = detail.thread.active_run_id || detail.thread.last_run_id;
  const hasFilters = ui.filter !== "all" || ui.search !== "" || ui.runID !== "";
  const reset = () => update({ filter: "all", search: "", runID: "", position: undefined });
  return <section className={`v2-inspector-content${ui.selected ? " has-selection" : ""}`} aria-label="Inspector 记录">
    <header className="v2-inspector-controls">
      <div className="v2-inspector-range">
        <strong>执行记录</strong>
        <span>已加载 {durableItems.length} 条{hasOlder ? " · 还有更早记录" : " · 已到最早记录"}</span>
        {items.some((item) => item.provisional) && <span>含未落盘的实时内容</span>}
      </div>
      <div className="v2-inspector-tools">
        <div className="v2-inspector-filters" role="group" aria-label="记录类型">
          {(Object.keys(filterLabels) as Filter[]).map((filter) => <button key={filter} type="button"
            aria-pressed={ui.filter === filter} onClick={() => update({ filter, position: undefined })}>{filterLabels[filter]}</button>)}
        </div>
        <label className="v2-inspector-search"><Search size={15} aria-hidden="true" />
          <input aria-label="在已加载记录中查找" placeholder="查找已加载记录" value={ui.search}
            onChange={(event) => update({ search: event.target.value, position: undefined })} /></label>
        {(runs.length > 1 || ui.runID) && <label className="v2-inspector-run"><span>执行范围</span><select aria-label="执行记录范围" value={ui.runID}
          onChange={(event) => update({ runID: event.target.value, position: undefined })}>
          <option value="">全部执行记录</option>
          {ui.runID && !runs.some(([runID]) => runID === ui.runID) && <option value={ui.runID}>已选执行（暂未加载）</option>}
          {runs.map(([runID, ordinal]) => <option key={runID} value={runID}>
            执行 {ordinal}{runID === currentRunID ? " · 当前" : " · 历史只读"}</option>)}
        </select></label>}
      </div>
      <div className="v2-inspector-scope"><span>显示 {visible.filter((item) => item.sequence !== 0).length} 条记录 · 查找仅覆盖已加载内容</span>
        {hasFilters && <button type="button" onClick={reset}>清除筛选</button>}
        <button type="button" onClick={() => { reset(); setLatestRequest((value) => value + 1); }}>
          <ArrowDown size={14} aria-hidden="true" />回到最新</button>
      </div>
    </header>
    <div className="v2-inspector-body">
      <div className="v2-inspector-records" ref={listRef}>
        {visible.length > 0 ? <VirtualTranscriptList key={`${ui.filter}:${ui.runID}:${ui.search}`}
          items={visible} hasOlder={hasOlder} isFetchingOlder={isFetchingOlder} onLoadOlder={onLoadOlder}
          t={translate} initialPosition={ui.position} onPositionChange={onPositionChange} latestRequest={latestRequest}
          renderItem={(item) => <button type="button" className={`v2-inspector-row${item.sequence === 0 ? " is-boundary" : ""}${isException(item) ? " is-exception" : ""}`}
            aria-pressed={ui.selected?.id === item.id && ui.selected.runID === item.run_id}
            onClick={(event) => { selectedButton.current = event.currentTarget; update({ selected: { id: item.id, runID: item.run_id } }); }}>
            <span className="v2-inspector-row-heading"><strong>{item.sequence === 0 ? `执行 ${item.run_ordinal}` : item.tool_name || item.title}</strong>
              {item.status && <span>{item.sequence === 0 ? <LifecycleStatusLabel status={item.status} />
                : item.status === "running" && item.durable && !item.provisional ? "记录时执行中" : <StatusLabel status={item.status} />}</span>}</span>
            {item.detail && <span className="v2-inspector-row-preview">{item.detail.slice(0, 200)}</span>}
            {!!item.images?.length && <span className="v2-inspector-row-preview">{item.images.length} 张图片</span>}
            <span className="v2-inspector-row-meta"><span>{sourceLabels[item.source]}</span>
              <span>执行 {item.run_ordinal}</span>
              <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleTimeString("zh-CN", { hour12: false })}</time>
              {item.provisional && <span>未落盘</span>}</span>
          </button>} /> : <div className="v2-inspector-empty" role="status">
          <p>{items.length ? "已加载范围内没有匹配记录。" : "还没有公开记录。"}</p>
          {hasOlder && <button type="button" disabled={isFetchingOlder} onClick={onLoadOlder}>
            {isFetchingOlder ? "正在加载较早记录…" : "加载较早记录"}</button>}
        </div>}
      </div>
      {ui.selected && <aside className="v2-inspector-detail" aria-label="选中记录详情"
        onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); closeDetail(); } }}>
        <header><strong>记录详情</strong><button ref={closeRef} type="button" aria-label="关闭记录详情" onClick={closeDetail}><X size={18} /></button></header>
        {selected ? <InspectorDetail key={`${selected.run_id}:${selected.id}`} client={client} threadID={threadID} item={selected} /> :
          <p>选中记录暂不在已加载范围内。可加载更早记录后继续查看。</p>}
      </aside>}
    </div>
  </section>;
}

function InspectorDetail({ client, threadID, item }: { client: APIClient; threadID: string; item: ThreadTranscriptItemView }) {
  return <div className="v2-inspector-detail-content">
    <h3>{item.tool_name || item.title}</h3>
    <p className="v2-inspector-origin">{sourceLabels[item.source]} · {item.stage === "running" && item.durable && !item.provisional ? "记录时执行中" : stageLabels[item.stage]}
      {item.provisional ? " · 尚未落盘" : " · 已保存"}</p>
    {item.durable && !item.provisional && item.status === "running" && item.sequence !== 0 &&
      <p className="v2-inspector-origin">这是记录保存时的执行状态。Agent 的当前活动请查看对话上方的工作状态。</p>}
    {item.source === "model" && <p className="v2-inspector-origin">公开模型内容；实际执行结果以对应工具记录为准。</p>}
    {item.detail && (item.activity_type === "message" && item.source === "model" ? <SafeMarkdown>{item.detail}</SafeMarkdown> :
      <pre className="v2-inspector-record-text">{item.detail}</pre>)}
    {item.source === "operator" && <V2ImagePreview client={client} images={item.images ?? []} />}
    {(item.detail_available && item.activity_detail_ref || item.web_evidence) && <ul className="v2-inspector-structured-detail">
      <ActivityItemDetail client={client} threadID={threadID} expectedRunID={item.run_id} initialOpen item={{
        title: item.title, detail: "", status: item.status ?? "", provisional: item.provisional,
        detailAvailable: item.durable && !item.provisional && Boolean(item.detail_available && item.activity_detail_ref),
        detailRef: item.activity_detail_ref, summary: item.activity_summary, webEvidence: item.web_evidence,
      }} /></ul>}
    <details className="v2-inspector-identities"><summary>来源与精确标识</summary>
      {item.images?.map((image) => <p key={image.id}>图片 {image.name || image.id}<br />
        <code>{image.id}</code><br /><code>SHA256 {image.sha256}</code></p>)}
      <dl>{Object.entries({ "记录 ID": item.id, "稳定标识": item.canonical_id, "执行 ID": item.run_id,
        "序号": item.sequence, "来源": item.source, "来源引用": item.source_ref, "调用 ID": item.durable_call_id,
        "尝试 ID": item.attempt_id, "模型尝试": item.model_attempt, "工具轮次（非用户工作轮）": item.tool_round,
        "流记录": item.stream_item_id, "详情引用": item.activity_detail_ref, "时间": item.created_at,
        "可核验记录": item.verifiable ? "是" : "否", "用户指令授权": item.instruction_authorized ? "是" : "否" })
        .filter(([, value]) => value !== undefined).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    </details>
  </div>;
}
