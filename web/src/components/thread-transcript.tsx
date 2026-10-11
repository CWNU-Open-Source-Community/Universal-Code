import {
  Bot,
  Check,
  CircleDot,
  FilePenLine,
  ExternalLink,
  ListChecks,
  Milestone,
  PackageCheck,
  Search,
  ShieldCheck,
  Terminal,
  UserRound,
  Wrench,
  BookOpen,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type UIEvent,
} from "react";
import type { PublicModelStreamSnapshot, ThreadTranscriptItemView } from "../api/types";
import type { PublicModelStreamStatus } from "../hooks/use-public-model-stream";
import { formatDate, formatNumber, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { StatusBadge } from "./common";
import { LifecycleStatusBadge } from "./lifecycle-status";
import { SafeMarkdown } from "./safe-markdown";

type Translator = (chinese: string, english: string) => string;

const estimatedRowHeight = 132;
const virtualOverscan = 8;
const maximumFallbackRows = 80;

const stageLabels: Record<ThreadTranscriptItemView["stage"], [string, string]> = {
  started: ["已开始", "Started"],
  arguments_ready: ["参数已就绪", "Arguments ready"],
  running: ["运行中", "Running"],
  result: ["结果", "Result"],
  blocked: ["已阻塞", "Blocked"],
};

const sourceLabels: Record<ThreadTranscriptItemView["source"], [string, string]> = {
  harness: ["Harness 事实", "Harness fact"],
  model: ["模型公开内容", "Public model content"],
  operator: ["用户", "Operator"],
};

export interface ThreadTranscriptProps {
  durableItems: ThreadTranscriptItemView[];
  hasOlder: boolean;
  isFetchingOlder: boolean;
  liveSnapshot?: PublicModelStreamSnapshot | null;
  liveStatus?: PublicModelStreamStatus;
  onLoadOlder: () => void;
  pendingItems?: ThreadTranscriptItemView[];
  streamError?: string;
}

export function ThreadTranscript({ durableItems, hasOlder, isFetchingOlder,
  liveSnapshot = null, liveStatus = "stopped", onLoadOlder, pendingItems = [],
  streamError = "" }: ThreadTranscriptProps) {
  const { t } = useLocale();
  const items = useMemo(() => mergeThreadTranscriptItems(
    durableItems, pendingItems, liveSnapshot, liveStatus),
  [durableItems, liveSnapshot, liveStatus, pendingItems]);
  return (
    <section aria-label={t("Thread 对话与活动", "Thread conversation and activity")}
      className="thread-transcript-shell">
      <header className="thread-transcript-header">
        <div>
          <h2>{t("Thread 记录", "Thread transcript")}</h2>
          <p>{t("消息、Run 边界、步骤、工具项与 Go 记录的结构化执行事实",
            "Messages, Run boundaries, Steps, Tool Items, and structured execution facts recorded by Go")}</p>
        </div>
        <span className="thread-transcript-safety"
          title={t("显示公开回复和已确认的操作记录",
            "Shows public replies and confirmed operation records") }>
          <ShieldCheck aria-hidden="true" size={15} />
          {t("公开记录", "Public records")}
        </span>
      </header>
      {streamError && <div className="inline-warning" role="status">
        {t("实时活动连接", "Live activity connection")}: {streamError}
      </div>}
      <VirtualTranscriptList hasOlder={hasOlder} isFetchingOlder={isFetchingOlder}
        items={items} onLoadOlder={onLoadOlder} t={t} />
    </section>
  );
}

export function mergeThreadTranscriptItems(durableItems: ThreadTranscriptItemView[],
  pendingItems: ThreadTranscriptItemView[], snapshot: PublicModelStreamSnapshot | null,
  liveStatus: PublicModelStreamStatus, exactStreamIdentity = false): ThreadTranscriptItemView[] {
  const durableByID = new Map<string, ThreadTranscriptItemView>();
  const durableCanonical = new Set<string>();
  for (const item of durableItems) {
    if (!durableByID.has(item.id)) durableByID.set(item.id, item);
    durableCanonical.add(item.canonical_id);
    if (item.stream_item_id) durableCanonical.add(item.stream_item_id);
    if (item.source_ref) durableCanonical.add(item.source_ref);
  }
  const provisional: ThreadTranscriptItemView[] = [];
  for (const item of pendingItems) {
    if (!durableCanonical.has(item.canonical_id) && !durableByID.has(item.id)) {
      provisional.push(item);
    }
  }
  if (snapshot) {
    const knownRunOrdinal = durableItems.find((item) =>
      item.run_id === snapshot.call.run_id)?.run_ordinal;
    const runOrdinal = knownRunOrdinal ??
      Math.max(0, ...durableItems.map((item) => item.run_ordinal)) + 1;
    const expectedRound = snapshot.call.tool_round + 1;
    const text = snapshot.text.trim();
    const messageItem = snapshot.items.find((item) => item.type === "message");
    const exactMatch = (stream: PublicModelStreamSnapshot["items"][number]) => durableItems.some((item) =>
      item.run_id === snapshot.call.run_id && [item.canonical_id, item.source_ref, item.stream_item_id,
        item.stream_call_id, item.durable_call_id].some((id) => Boolean(id) &&
        [stream.id, stream.call_id, stream.durable_call_id].includes(id)));
    const commentaryConfirmed = exactStreamIdentity ? Boolean(messageItem && exactMatch(messageItem)) : durableItems.some((item) =>
      item.run_id === snapshot.call.run_id && item.source === "model" && (
      (item.attempt_id === snapshot.call.attempt_id &&
        item.model_attempt === snapshot.call.model_attempt && item.tool_round === expectedRound) ||
      (Boolean(text) && item.created_at >= snapshot.call.started_at && item.detail?.trim() === text)
    ));
    if (text && !commentaryConfirmed) {
      provisional.push({
        version: "thread_transcript.v1",
        id: `live-message:${snapshot.call.attempt_id}:${snapshot.call.model_attempt}:${expectedRound}`,
        canonical_id: messageItem?.id ??
          `live-message:${snapshot.call.attempt_id}:${snapshot.call.model_attempt}:${expectedRound}`,
        run_id: snapshot.call.run_id, run_ordinal: runOrdinal,
        sequence: Number.MAX_SAFE_INTEGER - 2, activity_type: "message",
        stage: liveStatus === "finalizing" ? "result" : "running",
        kind: "model_update", source: "model", title: "Universal-Code", detail: text,
        status: liveStatus === "finalizing" ? "completed" : "running",
        verifiable: false, instruction_authorized: false,
        attempt_id: snapshot.call.attempt_id, model_attempt: snapshot.call.model_attempt,
        tool_round: expectedRound, provisional: true, durable: false,
        created_at: snapshot.updated_at,
      });
    }
    snapshot.items.filter((item) => item.type === "tool_call").forEach((item, index) => {
      if (exactStreamIdentity ? exactMatch(item) : durableCanonical.has(item.id)) return;
      provisional.push({
        version: "thread_transcript.v1", id: `live-tool:${item.id}`,
        canonical_id: item.id, run_id: snapshot.call.run_id, run_ordinal: runOrdinal,
        sequence: Number.MAX_SAFE_INTEGER - 1, position: index + 1,
        activity_type: classifyLiveTool(item.tool_name ?? ""),
        stage: liveToolStage(item.status), kind: "tool_call", source: "model",
        title: item.tool_name || "tool", detail: liveToolDetail(item.status, item.argument_bytes ?? 0),
        status: item.status, verifiable: false, instruction_authorized: false,
        attempt_id: snapshot.call.attempt_id, model_attempt: snapshot.call.model_attempt,
        tool_round: expectedRound, tool_name: item.tool_name,
        stream_response_id: item.response_id, stream_item_id: item.id,
        stream_call_id: item.call_id, provisional: true, durable: false,
        created_at: snapshot.updated_at,
      });
    });
  }
  return [...durableByID.values(), ...provisional].sort(compareTranscriptItems);
}

function compareTranscriptItems(left: ThreadTranscriptItemView,
  right: ThreadTranscriptItemView): number {
  return left.run_ordinal - right.run_ordinal || left.sequence - right.sequence ||
    (left.position ?? 0) - (right.position ?? 0) ||
    left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id);
}

function classifyLiveTool(name: string): ThreadTranscriptItemView["activity_type"] {
  switch (name.toLowerCase()) {
  case "history_search": return "search";
  case "history_read": return "read";
  case "list_workspace": case "workspace_list": case "workspace_glob": case "workspace_grep":
  case "workspace_search": case "code_search": case "search": case "find_files":
  case "github_review_evidence_list": case "code_workspace_symbols": case "code_document_symbols":
  case "code_references": case "code_implementation": case "code_call_hierarchy":
  case "code_type_hierarchy": case "web_search": case "source_search": return "search";
  case "read_file": case "workspace_read": case "note_get": case "artifact_get":
  case "github_review_evidence_read": case "code_definition": case "code_hover":
  case "code_signature_help": case "web_fetch": case "web_citation": return "read";
  case "replace_file": case "file_edit": case "apply_patch": case "workspace_restore":
  case "workspace_change": case "workspace_apply": case "workspace_delete": return "edit";
  case "verification_record": case "verification_plan": case "ui_evidence": case "run_tests":
  case "code_diagnostics": return "verify";
  case "delivery_checkpoint": case "artifact_create": case "code_handoff":
  case "plan_delivery_propose": return "delivery";
  case "work_item_create": case "note_create": case "specialist_delegation_propose":
  case "child_task_propose": case "controlled_command_propose": case "host_command_propose":
  case "one_shot_command_propose": case "sandbox_docker_run_propose":
  case "skill_candidate_propose": return "checkpoint";
  default: return "execute";
  }
}

function liveToolStage(status: PublicModelStreamSnapshot["items"][number]["status"]):
ThreadTranscriptItemView["stage"] {
  if (status === "ready_for_validation" || status === "completed") return "arguments_ready";
  if (status === "failed" || status === "cancelled") return "blocked";
  return "started";
}

function liveToolDetail(status: PublicModelStreamSnapshot["items"][number]["status"],
  argumentBytes: number): string {
  const state = status === "ready_for_validation" ? "Arguments ready; awaiting Go validation" :
    status === "completed" ? "Prepared; submitting for Go validation" :
      status === "failed" ? "Preparation failed" : status === "cancelled" ?
        "Preparation cancelled" : "Preparing call";
  return argumentBytes > 0 ? `${state} · ${argumentBytes} bytes` : state;
}

export interface TranscriptReadingPosition {
  anchorID: string;
  offset: number;
  nearBottom: boolean;
}

export function VirtualTranscriptList({ items, hasOlder, isFetchingOlder, onLoadOlder, t,
  renderItem, initialPosition, onPositionChange, latestRequest = 0 }: {
  items: ThreadTranscriptItemView[];
  hasOlder: boolean;
  isFetchingOlder: boolean;
  onLoadOlder: () => void;
  t: Translator;
  renderItem?: (item: ThreadTranscriptItemView) => ReactNode;
  initialPosition?: TranscriptReadingPosition;
  onPositionChange?: (position: TranscriptReadingPosition) => void;
  latestRequest?: number;
}) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const sizes = useRef(new Map<string, number>());
  const [measureRevision, setMeasureRevision] = useState(0);
  const [viewport, setViewport] = useState({ height: 0, scrollTop: 0 });
  const previous = useRef({ firstID: "", lastID: "", scrollHeight: 0, nearBottom: true });
  const readingPosition = useRef(initialPosition);
  const handledLatest = useRef(latestRequest);
  const offsets = useMemo(() => {
    const values = new Array<number>(items.length + 1);
    values[0] = 0;
    for (let index = 0; index < items.length; index++) {
      values[index + 1] = values[index] + (sizes.current.get(items[index].id) ?? estimatedRowHeight);
    }
    return values;
  // measureRevision is the explicit invalidation signal for the mutable size map.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, measureRevision]);
  const totalHeight = offsets[offsets.length - 1] ?? 0;
  const range = useMemo(() => visibleRange(offsets, viewport.scrollTop,
    viewport.height, items.length), [items.length, offsets, viewport]);
  const updateSize = useCallback((id: string, height: number) => {
    if (!Number.isFinite(height) || height <= 0 || Math.abs((sizes.current.get(id) ?? 0) - height) < 1) {
      return;
    }
    sizes.current.set(id, height);
    setMeasureRevision((current) => current + 1);
  }, []);
  const updateViewport = useCallback((element: HTMLDivElement | null) => {
    if (!element) return;
    setViewport({ height: element.clientHeight, scrollTop: element.scrollTop });
  }, []);
  const rememberPosition = useCallback((element: HTMLDivElement) => {
    const index = offsetIndex(offsets, element.scrollTop);
    const position = { anchorID: items[index]?.id ?? "", offset: element.scrollTop - (offsets[index] ?? 0),
      nearBottom: element.scrollHeight - element.scrollTop - element.clientHeight < 96 };
    readingPosition.current = position;
    onPositionChange?.(position);
  }, [items, offsets, onPositionChange]);
  useEffect(() => {
    const element = viewportRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => updateViewport(element));
    observer.observe(element);
    return () => observer.disconnect();
  }, [updateViewport]);
  useLayoutEffect(() => {
    const element = viewportRef.current;
    if (!element || items.length === 0) return;
    const firstID = items[0].id;
    const lastID = items.at(-1)?.id ?? "";
    const old = previous.current;
    const anchor = readingPosition.current;
    const anchorIndex = anchor ? items.findIndex((item) => item.id === anchor.anchorID) : -1;
    if (handledLatest.current !== latestRequest) {
      element.scrollTop = element.scrollHeight;
      handledLatest.current = latestRequest;
    } else if (onPositionChange && anchor && !anchor.nearBottom && anchorIndex >= 0) {
      element.scrollTop = (offsets[anchorIndex] ?? 0) + anchor.offset;
    } else if (!old.lastID) {
      element.scrollTop = element.scrollHeight;
    } else if (old.lastID === lastID && old.firstID !== firstID) {
      element.scrollTop += Math.max(0, element.scrollHeight - old.scrollHeight);
    } else if (old.lastID !== lastID && old.nearBottom) {
      element.scrollTop = element.scrollHeight;
    }
    previous.current = { firstID, lastID, scrollHeight: element.scrollHeight,
      nearBottom: element.scrollHeight - element.scrollTop - element.clientHeight < 96 };
    updateViewport(element);
    if (onPositionChange) rememberPosition(element);
  }, [items, totalHeight, updateViewport, latestRequest, offsets, onPositionChange, rememberPosition]);
  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    const element = event.currentTarget;
    previous.current.nearBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 96;
    updateViewport(element);
    if (onPositionChange) rememberPosition(element);
    if (element.scrollTop < 160 && hasOlder && !isFetchingOlder) onLoadOlder();
  };
  if (items.length === 0) {
    return <div className="thread-transcript-empty"><CircleDot aria-hidden="true" size={18} />
      {t("还没有公开记录", "No public transcript yet")}</div>;
  }
  return <div className="thread-transcript-viewport" data-testid="thread-transcript-viewport"
    onScroll={onScroll} ref={viewportRef} tabIndex={0}>
    <div aria-live="polite" className="thread-transcript-load-state">
      {hasOlder && <button className="load-more" disabled={isFetchingOlder}
        onClick={onLoadOlder} type="button">{isFetchingOlder ?
          t("正在加载较早记录…", "Loading earlier records…") :
          t("加载较早记录", "Load earlier records")}</button>}
      <span>{formatNumber(items.length)} {t("项已载入", "items loaded")}</span>
    </div>
    <ol className="thread-transcript-list" style={{ minHeight: totalHeight }}>
      <li aria-hidden="true" className="thread-transcript-spacer"
        style={{ height: offsets[range.start] ?? 0 }} />
      {items.slice(range.start, range.end).map((item) =>
        <MeasuredTranscriptRow item={item} key={item.id} onSize={updateSize} t={t} renderItem={renderItem} />)}
      <li aria-hidden="true" className="thread-transcript-spacer"
        style={{ height: Math.max(0, totalHeight - (offsets[range.end] ?? totalHeight)) }} />
    </ol>
  </div>;
}

function visibleRange(offsets: number[], scrollTop: number, height: number, count: number) {
  if (height <= 0) return { start: Math.max(0, count - maximumFallbackRows), end: count };
  const first = offsetIndex(offsets, Math.max(0, scrollTop));
  const last = offsetIndex(offsets, scrollTop + height) + 1;
  return { start: Math.max(0, first - virtualOverscan),
    end: Math.min(count, last + virtualOverscan) };
}

function offsetIndex(offsets: number[], target: number): number {
  let low = 0;
  let high = Math.max(0, offsets.length - 1);
  while (low < high) {
    const middle = Math.floor((low + high + 1) / 2);
    if ((offsets[middle] ?? 0) <= target) low = middle;
    else high = middle - 1;
  }
  return Math.max(0, Math.min(low, offsets.length - 2));
}

function MeasuredTranscriptRow({ item, onSize, t, renderItem }: {
  item: ThreadTranscriptItemView;
  onSize: (id: string, height: number) => void;
  t: Translator;
  renderItem?: (item: ThreadTranscriptItemView) => ReactNode;
}) {
  const rowRef = useRef<HTMLLIElement>(null);
  useLayoutEffect(() => {
    const element = rowRef.current;
    if (!element) return;
    const measure = () => onSize(item.id, element.getBoundingClientRect().height);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [item.id, onSize]);
  return <li className="thread-transcript-virtual-row" data-transcript-id={item.id} ref={rowRef}>
    {renderItem ? renderItem(item) : <TranscriptItem item={item} t={t} />}
  </li>;
}

function TranscriptItem({ item, t }: { item: ThreadTranscriptItemView; t: Translator }) {
  if (item.sequence === 0) {
    return <article aria-label={t(`Run ${item.run_ordinal} 边界`, `Run ${item.run_ordinal} boundary`)}
      className="thread-run-boundary">
      <span><Milestone aria-hidden="true" size={16} /></span>
      <div><strong>Run {item.run_ordinal}</strong><p>{item.detail}</p></div>
      <LifecycleStatusBadge status={item.status || item.boundary_reason || "checkpoint"} />
      <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
    </article>;
  }
  const isMessage = item.activity_type === "message";
  return <article aria-label={`${t(...sourceLabels[item.source])}: ${item.title}`}
    className={`thread-transcript-item type-${item.activity_type} source-${item.source}${
      item.provisional ? " is-provisional" : ""}`}>
    <span className="thread-transcript-marker"><TranscriptIcon item={item} /></span>
    <div className="thread-transcript-body">
      <header>
        <span className={`thread-transcript-source source-${item.source}`}>
          {item.verifiable && <Check aria-hidden="true" size={12} />}{t(...sourceLabels[item.source])}
        </span>
        <span className={`thread-transcript-stage stage-${item.stage}`}>
          {item.stage === "running" && item.durable && !item.provisional
            ? t("记录时执行中", "Executing when recorded") : t(...stageLabels[item.stage])}</span>
        {item.provisional && <span className="thread-transcript-live"><span aria-hidden="true" />
          {t("临时", "Live")}</span>}
        <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
        <span className="thread-transcript-sequence">R{item.run_ordinal} · #{item.sequence}</span>
      </header>
      <h3>{item.tool_name || item.title}</h3>
      {item.detail && <TranscriptDetail detail={item.detail} markdown={isMessage && item.source === "model"}
        t={t} />}
      {item.web_evidence && <WebEvidenceCard evidence={item.web_evidence} t={t} />}
      {item.kind === "tool_call" && <small>{t(
        "参数与原始输出保持隐藏；状态来自结构化 item 事件。",
        "Arguments and raw output stay hidden; status comes from structured item events.")}</small>}
      {item.source === "model" && <small>{item.provisional ? t(
        "临时公开内容会由具有相同稳定身份的持久事件替换。",
        "Provisional public content is replaced by the durable event with the same stable identity.") : t(
        "模型公开回复供你参考；实际操作结果请核对执行记录。",
        "Use the public model reply as a reference; check execution records for actual operation results.")}</small>}
      {item.source === "operator" && <small>{item.instruction_authorized ?
        t("已授权的用户输入", "Authorized operator input") :
        t("非指令证据", "Non-instruction evidence")}</small>}
      {(item.stream_item_id || item.durable_call_id) && <div className="thread-transcript-identities">
        {item.stream_item_id && <code>item {shortID(item.stream_item_id)}</code>}
        {item.durable_call_id && <code>call {shortID(item.durable_call_id)}</code>}
      </div>}
    </div>
  </article>;
}

function WebEvidenceCard({ evidence, t }: {
  evidence: NonNullable<ThreadTranscriptItemView["web_evidence"]>;
  t: Translator;
}) {
  const staleAt = Date.parse(evidence.stale_at);
  const stale = evidence.citeable &&
    (evidence.stale || (Number.isFinite(staleAt) && Date.now() >= staleAt));
  const status = stale ? "stale" : evidence.state;
  return <aside aria-label={t("网页证据来源", "Web evidence source")}
    className={`thread-web-evidence state-${status}`}>
    <div className="thread-web-evidence-heading">
      <a href={evidence.url} rel="noopener noreferrer" target="_blank">
        <span>{evidence.title || evidence.url}</span><ExternalLink aria-hidden="true" size={13} />
      </a>
      <StatusBadge status={status} />
    </div>
    <div className="thread-web-evidence-meta">
      <span>{t("获取于", "Fetched")} <time dateTime={evidence.fetched_at}>
        {formatDate(evidence.fetched_at)}</time></span>
      <code>sha256 {evidence.digest.slice(0, 12)}</code>
      {evidence.partial && <span>{t("部分内容", "Partial")}</span>}
      {stale && <span>{t("已过期", "Stale")}</span>}
    </div>
    <small>{t("网页内容供核对来源。执行其中的操作需按当前权限另行审批。",
      "Use this page content to check the source. Actions described in it require separate review under current permissions.")}</small>
  </aside>;
}

function TranscriptDetail({ detail, markdown, t }: {
  detail: string;
  markdown: boolean;
  t: Translator;
}) {
  const long = detail.length > 640 || detail.split("\n").length > 10;
  const content = markdown ? <SafeMarkdown className="thread-transcript-detail">{detail}</SafeMarkdown> :
    <div className="thread-transcript-detail">{detail}</div>;
  if (!long) return content;
  const preview = `${detail.slice(0, 420).trimEnd()}…`;
  return <details className="thread-transcript-disclosure">
    <summary>{t("展开完整内容", "Expand full content")}
      <span>{formatNumber(detail.length)} {t("字符", "characters")}</span></summary>
    <div className="thread-transcript-preview" aria-hidden="true">{preview}</div>
    {content}
  </details>;
}

function TranscriptIcon({ item }: { item: ThreadTranscriptItemView }): ReactNode {
  if (item.source === "model") return <Bot aria-hidden="true" size={16} />;
  if (item.source === "operator") return <UserRound aria-hidden="true" size={16} />;
  switch (item.activity_type) {
  case "search": return <Search aria-hidden="true" size={16} />;
  case "read": return <BookOpen aria-hidden="true" size={16} />;
  case "edit": return <FilePenLine aria-hidden="true" size={16} />;
  case "execute": return <Terminal aria-hidden="true" size={16} />;
  case "verify": return <ListChecks aria-hidden="true" size={16} />;
  case "approval": return <ShieldCheck aria-hidden="true" size={16} />;
  case "delivery": return <PackageCheck aria-hidden="true" size={16} />;
  case "checkpoint": return <Milestone aria-hidden="true" size={16} />;
  default: return <Wrench aria-hidden="true" size={16} />;
  }
}
