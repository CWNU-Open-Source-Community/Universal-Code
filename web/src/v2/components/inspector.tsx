import { useLocale, type LocaleText } from "../../lib/locale";
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
const filterLabels = (t: LocaleText): Record<Filter, string> => ({ all: t("全部", "All"), tools: t("工具", "Tool"), approvals: t("审批", "Approvals"), exceptions: t("异常", "Exceptions") });
const sourceLabels = (t: LocaleText) => ({ harness: t("执行记录", "Execution records"), model: t("模型公开内容", "Public model content"), operator: t("用户输入", "User input"), peer: t("其他聊天", "Other conversations") });
const stageLabels = (t: LocaleText) => ({ started: t("已开始", "Started"), arguments_ready: t("参数就绪", "Arguments ready"), running: t("运行中", "Running"), result: t("结果", "Result"), blocked: t("已阻塞", "Blocked") });
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
  const { t, locale } = useLocale();
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
  return <section className={`v2-inspector-content${ui.selected ? " has-selection" : ""}`} aria-label={t("Inspector 记录", "Inspector records")}>
    <header className="v2-inspector-controls">
      <div className="v2-inspector-range">
        <strong>{t("执行记录", "Execution records")}</strong>
        <span>{t(`已加载 ${durableItems.length} 条`, `${durableItems.length} records loaded`)}{hasOlder ? t(" · 还有更早记录", " · Earlier records available") : t(" · 已到最早记录", " · Earliest record reached")}</span>
        {items.some((item) => item.provisional) && <span>{t("含未落盘的实时内容", "Includes unsaved live content")}</span>}
      </div>
      <div className="v2-inspector-tools">
        <div className="v2-inspector-filters" role="group" aria-label={t("记录类型", "Record type")}>
          {(Object.keys(filterLabels(t)) as Filter[]).map((filter) => <button key={filter} type="button"
            aria-pressed={ui.filter === filter} onClick={() => update({ filter, position: undefined })}>{filterLabels(t)[filter]}</button>)}
        </div>
        <label className="v2-inspector-search"><Search size={15} aria-hidden="true" />
          <input aria-label={t("在已加载记录中查找", "Search loaded records")} placeholder={t("查找已加载记录", "Search loaded records")} value={ui.search}
            onChange={(event) => update({ search: event.target.value, position: undefined })} /></label>
        {(runs.length > 1 || ui.runID) && <label className="v2-inspector-run"><span>{t("执行范围", "Run scope")}</span><select aria-label={t("执行记录范围", "Execution record scope")} value={ui.runID}
          onChange={(event) => update({ runID: event.target.value, position: undefined })}>
          <option value="">{t("全部执行记录", "All runs")}</option>
          {ui.runID && !runs.some(([runID]) => runID === ui.runID) && <option value={ui.runID}>{t("已选执行（暂未加载）", "Selected run (not loaded)")}</option>}
          {runs.map(([runID, ordinal]) => <option key={runID} value={runID}>
            {t("执行", "Run")}{ordinal}{runID === currentRunID ? t("· 当前", " · Current") : t("· 历史只读", " · Historical, read only")}</option>)}
        </select></label>}
      </div>
      <div className="v2-inspector-scope"><span>{t(`显示 ${visible.filter((item) => item.sequence !== 0).length} 条记录 · 查找仅覆盖已加载内容`, `${visible.filter((item) => item.sequence !== 0).length} records shown · Search covers loaded content only`)}</span>
        {hasFilters && <button type="button" onClick={reset}>{t("清除筛选", "Clear filters")}</button>}
        <button type="button" onClick={() => { reset(); setLatestRequest((value) => value + 1); }}>
          <ArrowDown size={14} aria-hidden="true" />{t("回到最新", "Jump to latest")}</button>
      </div>
    </header>
    <div className="v2-inspector-body">
      <div className="v2-inspector-records" ref={listRef}>
        {visible.length > 0 ? <VirtualTranscriptList key={`${ui.filter}:${ui.runID}:${ui.search}`}
          items={visible} hasOlder={hasOlder} isFetchingOlder={isFetchingOlder} onLoadOlder={onLoadOlder}
          t={t} initialPosition={ui.position} onPositionChange={onPositionChange} latestRequest={latestRequest}
          renderItem={(item) => <button type="button" className={`v2-inspector-row${item.sequence === 0 ? " is-boundary" : ""}${isException(item) ? " is-exception" : ""}`}
            aria-pressed={ui.selected?.id === item.id && ui.selected.runID === item.run_id}
            onClick={(event) => { selectedButton.current = event.currentTarget; update({ selected: { id: item.id, runID: item.run_id } }); }}>
            <span className="v2-inspector-row-heading"><strong>{item.sequence === 0 ? t(`执行 ${item.run_ordinal}`, `Run ${item.run_ordinal}`) : item.tool_name || item.title}</strong>
              {item.status && <span>{item.sequence === 0 ? <LifecycleStatusLabel status={item.status} />
                : item.status === "running" && item.durable && !item.provisional ? t("记录时执行中", "Running when recorded") : <StatusLabel status={item.status} />}</span>}</span>
            {item.detail && <span className="v2-inspector-row-preview">{item.detail.slice(0, 200)}</span>}
            {!!item.images?.length && <span className="v2-inspector-row-preview">{t(`${item.images.length} 张图片`, `${item.images.length} images`)}</span>}
            <span className="v2-inspector-row-meta"><span>{sourceLabels(t)[item.source]}</span>
              <span>{t(`执行 ${item.run_ordinal}`, `Run ${item.run_ordinal}`)}</span>
              <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleTimeString(locale, { hour12: false })}</time>
              {item.provisional && <span>{t("未落盘", "Not saved")}</span>}</span>
          </button>} /> : <div className="v2-inspector-empty" role="status">
          <p>{items.length ? t("已加载范围内没有匹配记录。", "No matching records in the loaded history.") : t("还没有公开记录。", "No public records yet.")}</p>
          {hasOlder && <button type="button" disabled={isFetchingOlder} onClick={onLoadOlder}>
            {isFetchingOlder ? t("正在加载较早记录…", "Loading earlier records…") : t("加载较早记录", "Load earlier records")}</button>}
        </div>}
      </div>
      {ui.selected && <aside className="v2-inspector-detail" aria-label={t("选中记录详情", "Selected record details")}
        onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); closeDetail(); } }}>
        <header><strong>{t("记录详情", "Record details")}</strong><button ref={closeRef} type="button" aria-label={t("关闭记录详情", "Close record details")} onClick={closeDetail}><X size={18} /></button></header>
        {selected ? <InspectorDetail key={`${selected.run_id}:${selected.id}`} client={client} threadID={threadID} item={selected} /> :
          <p>{t("选中记录暂不在已加载范围内。可加载更早记录后继续查看。", "The selected record is outside the loaded history. Load earlier records to view it.")}</p>}
      </aside>}
    </div>
  </section>;
}

function InspectorDetail({ client, threadID, item }: { client: APIClient; threadID: string; item: ThreadTranscriptItemView }) {
  const { t } = useLocale();
  return <div className="v2-inspector-detail-content">
    <h3>{item.tool_name || item.title}</h3>
    <p className="v2-inspector-origin">{sourceLabels(t)[item.source]} · {item.stage === "running" && item.durable && !item.provisional ? t("记录时执行中", "Running when recorded") : stageLabels(t)[item.stage]}
      {item.provisional ? t("· 尚未落盘", " · Not saved yet") : t("· 已保存", " · Saved")}</p>
    {item.durable && !item.provisional && item.status === "running" && item.sequence !== 0 &&
      <p className="v2-inspector-origin">{t("这是记录保存时的执行状态。Agent 的当前活动请查看对话上方的工作状态。", "This was the execution state when the record was saved. Check the conversation's activity status for what the agent is doing now.")}</p>}
    {item.source === "model" && <p className="v2-inspector-origin">{t("公开模型内容；实际执行结果以对应工具记录为准。", "Public model content. Tool records show the actual execution results.")}</p>}
    {item.detail && (item.activity_type === "message" && item.source === "model" ? <SafeMarkdown>{item.detail}</SafeMarkdown> :
      <pre className="v2-inspector-record-text">{item.detail}</pre>)}
    {item.source === "operator" && <V2ImagePreview client={client} images={item.images ?? []} />}
    {(item.detail_available && item.activity_detail_ref || item.web_evidence) && <ul className="v2-inspector-structured-detail">
      <ActivityItemDetail client={client} threadID={threadID} expectedRunID={item.run_id} initialOpen item={{
        title: item.title, detail: "", status: item.status ?? "", provisional: item.provisional,
        detailAvailable: item.durable && !item.provisional && Boolean(item.detail_available && item.activity_detail_ref),
        detailRef: item.activity_detail_ref, summary: item.activity_summary, webEvidence: item.web_evidence,
      }} /></ul>}
    <details className="v2-inspector-identities"><summary>{t("来源与精确标识", "Source and exact identifiers")}</summary>
      {item.images?.map((image) => <p key={image.id}>{t("图片", "Image")}{image.name || image.id}<br />
        <code>{image.id}</code><br /><code>SHA256 {image.sha256}</code></p>)}
      <dl>{Object.entries({ [t("记录 ID", "Record ID")]: item.id, [t("稳定标识", "Stable ID")]: item.canonical_id, [t("执行 ID", "Run ID")]: item.run_id,
        [t("序号", "Sequence")]: item.sequence, [t("来源", "Source")]: item.source, [t("来源引用", "Source reference")]: item.source_ref, [t("调用 ID", "Call ID")]: item.durable_call_id,
        [t("尝试 ID", "Attempt ID")]: item.attempt_id, [t("模型尝试", "Model attempt")]: item.model_attempt, [t("工具轮次（非用户工作轮）", "Tool round (not user turn)")]: item.tool_round,
        [t("流记录", "Stream record")]: item.stream_item_id, [t("详情引用", "Detail reference")]: item.activity_detail_ref, [t("时间", "Time")]: item.created_at,
        [t("可核验记录", "Verifiable record")]: item.verifiable ? t("是", "Yes") : t("否", "No"), [t("用户指令授权", "Authorized by user instruction")]: item.instruction_authorized ? t("是", "Yes") : t("否", "No") })
        .filter(([, value]) => value !== undefined).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    </details>
  </div>;
}
