import type { PublicModelStreamSnapshot, ThreadTranscriptItemView } from "../../api/types";
import type { PublicModelStreamStatus } from "../../hooks/use-public-model-stream";
import { validImageAttachments, type WorkspaceImageAttachment } from "../../api/image-attachments";
import { validFileAttachments, type WorkspaceFileAttachment } from "../../api/file-attachments";

export type NarrativeToolKind = "search" | "read" | "edit" | "execute" | "verify" | "plan";
export type NarrativeFileChange = "proposed" | "approved" | "applied" | "denied" | "failed" | "unknown";

export const fileChangeLabels: Record<NarrativeFileChange, string> = {
  proposed: "已提出修改，等待审阅",
  approved: "修改已批准，尚未应用",
  applied: "修改已应用",
  denied: "修改已拒绝",
  failed: "文件修改失败",
  unknown: "文件修改记录",
};

export type ThreadTranscriptActivityItem = ThreadTranscriptItemView & {
  activity_detail_ref?: string;
  detail_available?: boolean;
  attachments?: WorkspaceFileAttachment[];
};

export type NarrativeEntry =
  | {
      id: string;
      kind: "user";
      text: string;
      images?: WorkspaceImageAttachment[];
      attachments?: WorkspaceFileAttachment[];
      status?: string;
      deliveryMode?: string;
      promotedToMessageID?: string;
      promotedFromMessageID?: string;
      createdAt: string;
      provisional: boolean;
    }
  | {
      id: string;
      kind: "assistant";
      text: string;
      runId?: string;
      createdAt: string;
      provisional: boolean;
    }
  | {
      id: string;
      kind: "activity";
      activity: NarrativeToolKind;
      title: string;
      detail: string;
      status: string;
      createdAt: string;
      runId: string;
      count: number;
      provisional: boolean;
      items: Array<{
        title: string;
        detail: string;
        status: string;
        provisional: boolean;
        detailRef?: string;
        detailAvailable: boolean;
        fileChange?: NarrativeFileChange;
        summary?: NonNullable<ThreadTranscriptItemView["activity_summary"]>;
        webEvidence?: NonNullable<ThreadTranscriptItemView["web_evidence"]>;
      }>;
    }
  | {
      id: string;
      kind: "notice";
      tone: "neutral" | "warning" | "success";
      text: string;
      createdAt: string;
      failureOrigin?: { runId: string; sourceRef: string; eventSequence: number };
    };

const toolActivities = new Set<NarrativeToolKind>([
  "search", "read", "edit", "execute", "verify", "plan",
]);

const hiddenHarnessStatuses = new Set([
  "started", "running", "queued", "checkpointed", "selection_drained",
]);

const syntheticContinuation = /^Continue mission at turn \d+ using only the structured tools offered by Go when needed:/u;

export interface LiveNarrativeProjection {
  runId: string;
  snapshot: PublicModelStreamSnapshot | null;
  status: PublicModelStreamStatus;
}

interface LiveTranscriptContext {
  identities: Set<string>;
  runOrdinals: Map<string, number>;
  nextRunOrdinal: number;
  modelRounds: Set<string>;
  modelTextTimes: Map<string, string>;
}

function compareTranscriptItems(left: ThreadTranscriptItemView, right: ThreadTranscriptItemView): number {
  const time = Date.parse(left.created_at) - Date.parse(right.created_at);
  if (time !== 0) return time;
  return left.run_ordinal - right.run_ordinal || left.sequence - right.sequence;
}

/** Index durable identities once, independently of the model's changing output. */
export function prepareThreadNarrative(transcript: ThreadTranscriptActivityItem[]) {
  return {
    transcript,
    entries: projectThreadNarrative(transcript),
    context: liveTranscriptContext(transcript),
    latest: transcript.reduce<ThreadTranscriptItemView | undefined>((latest, item) =>
      !latest || compareTranscriptItems(latest, item) < 0 ? item : latest, undefined),
  };
}

export function projectLiveThreadNarrative(prepared: ReturnType<typeof prepareThreadNarrative>,
  live: LiveNarrativeProjection): NarrativeEntry[] {
  if (!live.snapshot || live.snapshot.call.run_id !== live.runId) return prepared.entries;
  const provisional = projectLiveTranscriptItems(prepared.context, live.snapshot, live.status);
  if (!provisional.length) return prepared.entries;
  // Replayed output can precede newer durable facts. Keep the original ordering
  // semantics for that uncommon case; ordinary live updates only project a tail.
  if (prepared.latest && provisional.some((item) => compareTranscriptItems(item, prepared.latest!) < 0)) {
    return projectThreadNarrative(prepared.transcript, live);
  }
  const tail = projectThreadNarrative(provisional);
  if (!tail.length) return prepared.entries;
  const previous = prepared.entries.at(-1);
  const first = tail[0];
  if (previous?.kind === "activity" && first.kind === "activity" &&
    previous.activity === first.activity && previous.runId === first.runId) {
    // The visible group can grow without mutating the cached durable group or
    // any historical leaf (including its original detail/source references).
    const merged: NarrativeEntry = { ...previous,
      title: first.title || previous.title, detail: first.detail || previous.detail,
      status: first.status || previous.status, provisional: previous.provisional || first.provisional,
      count: previous.count + first.count, items: [...previous.items, ...first.items] };
    return [...prepared.entries.slice(0, -1), merged, ...tail.slice(1)];
  }
  return [...prepared.entries, ...tail];
}

function isSyntheticOperatorInput(item: ThreadTranscriptItemView): boolean {
  return syntheticContinuation.test(item.detail?.trim() ?? "");
}

function isInternalModelUpdate(item: ThreadTranscriptItemView): boolean {
  const detail = item.detail?.trim() ?? "";
  return /^mock\s+(?:plan|response)\b/iu.test(detail) || syntheticContinuation.test(detail) ||
    item.kind === "model_call";
}

function isInternalHarnessLifecycle(item: ThreadTranscriptItemView): boolean {
  return item.kind === "model_call" ||
    /^(?:Agent 回合|Supervisor 检查点|Run 状态|模型调用|模型响应)/u.test(item.title.trim());
}

function isArtifactStoragePlaceholder(item: ThreadTranscriptActivityItem): boolean {
  // artifact.created also records ordinary tool output. Its empty audit label
  // is not a model answer or proof of delivery; the actual tool row remains.
  return item.source === "harness" && item.kind === "plan" && item.activity_type === "delivery" &&
    item.stage === "result" && item.status === "completed" && item.durable && !item.provisional &&
    item.title.trim() === "交付物已记录" && !item.detail?.trim() &&
    !item.activity_summary && !item.web_evidence && !item.activity_detail_ref && !item.detail_available;
}

function normalizedText(item: ThreadTranscriptItemView): string {
  const detail = item.detail?.trim() ?? "";
  const title = item.title.trim();
  return detail && detail !== title ? detail : title;
}

function noticeTone(item: ThreadTranscriptItemView): "neutral" | "warning" | "success" {
  const status = (item.status ?? "").toLowerCase();
  if (/(fail|error|block|deny|cancel)/u.test(status)) return "warning";
  if (/(complete|success|pass|deliver|finish)/u.test(status)) return "success";
  return "neutral";
}

/**
 * Turns the durable audit transcript into the small, human-facing story used by v2.
 * Run boundaries, sequence numbers and Harness lifecycle chatter stay available to
 * Inspector but never become default conversation rows.
 */
export function projectThreadNarrative(
  transcript: ThreadTranscriptActivityItem[],
  live?: LiveNarrativeProjection,
): NarrativeEntry[] {
  const result: NarrativeEntry[] = [];
  const noticeEvents = new Set<string>();
  type ActivityEntry = Extract<NarrativeEntry, { kind: "activity" }>;
  type ActivityLeaf = ActivityEntry["items"][number];
  const canonicalActivities = new Map<string, {
    entry: ActivityEntry;
    leaf: ActivityLeaf;
    stageRank: number;
  }>();
  const provisional = live?.snapshot && live.snapshot.call.run_id === live.runId
    ? projectLiveTranscriptItems(liveTranscriptContext(transcript), live.snapshot, live.status) : [];
  const ordered = [...transcript, ...provisional].sort(compareTranscriptItems);

  for (const item of ordered) {
    if (item.sequence === 0 || isArtifactStoragePlaceholder(item)) continue;
    if (item.source === "harness" && item.kind === "tool_call" && !item.tool_name &&
      item.title.trim() === "工具批次完成") continue;
    // Delivery is also used by real model answers. Reclassify only the exact
    // Plan tool, so lifecycle receipts cannot become assistant prose.
    const planTool = item.kind === "tool_call" && item.tool_name === "plan_delivery_propose" &&
      (item.source === "harness" || item.source === "model");
    const activity = planTool ? "plan" : item.activity_type as NarrativeToolKind;
    if (toolActivities.has(activity)) {
      const evidence = safeWebEvidenceNarrative(item);
      const fileChange = item.kind === "file_change" ? fileChangeState(item.status) : undefined;
      const leaf = {
        title: (planTool && item.stage === "result" && !item.durable_call_id && !item.stream_item_id &&
          !item.stream_call_id && (!item.canonical_id || item.canonical_id === item.id)
          ? "规划结果记录" : undefined) ??
          (fileChange ? fileChangeLabels[fileChange] : undefined) ??
          evidence?.title ?? item.activity_summary?.command ??
          safeToolActivityTitle(item.tool_name ?? "", activity, item.title),
        detail: evidence?.detail ?? item.detail?.trim() ?? "",
        status: evidence?.status ?? item.status?.trim() ?? item.stage?.trim() ?? "",
        provisional: item.provisional,
        ...(item.activity_detail_ref ? { detailRef: item.activity_detail_ref } : {}),
        detailAvailable: item.detail_available === true && Boolean(item.activity_detail_ref),
        ...(fileChange ? { fileChange } : {}),
        ...(item.activity_summary ? { summary: item.activity_summary } : {}),
        ...(item.web_evidence ? { webEvidence: item.web_evidence } : {}),
      };
      const canonicalKey = `${item.run_id}:${item.canonical_id || item.id}`;
      const stageRank = toolLifecycleStageRank(item.stage);
      const canonical = canonicalActivities.get(canonicalKey);
      if (canonical) {
        if (stageRank >= canonical.stageRank) {
          if (!leaf.detailRef && canonical.leaf.detailRef) {
            leaf.detailRef = canonical.leaf.detailRef;
            leaf.detailAvailable = canonical.leaf.detailAvailable;
          }
          Object.assign(canonical.leaf, leaf);
          canonical.stageRank = stageRank;
          canonical.entry.title = leaf.title || canonical.entry.title;
          canonical.entry.detail = leaf.detail || canonical.entry.detail;
          canonical.entry.status = leaf.status || canonical.entry.status;
          canonical.entry.provisional = canonical.entry.items.some((entry) => entry.provisional);
        }
        continue;
      }
      const previous = result.at(-1);
      if (previous?.kind === "activity" && previous.activity === activity &&
        item.run_id === previous.runId) {
        previous.items.push(leaf);
        previous.count = previous.items.length;
        previous.title = leaf.title || previous.title;
        previous.detail = leaf.detail || previous.detail;
        previous.status = leaf.status || previous.status;
        previous.provisional = previous.provisional || leaf.provisional;
        canonicalActivities.set(canonicalKey, { entry: previous, leaf, stageRank });
      } else {
        const entry: ActivityEntry = {
          id: item.id,
          kind: "activity",
          activity,
          title: leaf.title,
          detail: leaf.detail,
          status: leaf.status,
          createdAt: item.created_at,
          runId: item.run_id,
          count: 1,
          provisional: item.provisional,
          items: [leaf],
        };
        result.push(entry);
        canonicalActivities.set(canonicalKey, { entry, leaf, stageRank });
      }
      continue;
    }

    if (item.source === "operator" || item.kind === "operator_input") {
      if (isSyntheticOperatorInput(item)) continue;
      const images = validImageAttachments(item.images) ? item.images : undefined;
      const attachments = validFileAttachments(item.attachments) ? item.attachments : undefined;
      // A pure attachment has no user-authored text. Do not substitute the queue
      // event's status title as if the user had typed it.
      const text = images?.length || attachments?.length ? item.detail?.trim() ?? "" : normalizedText(item);
      if (text || images?.length || attachments?.length) result.push({ id: item.id, kind: "user", text, images, attachments,
        status: item.status, deliveryMode: item.delivery_mode, promotedToMessageID: item.promoted_to_message_id,
        promotedFromMessageID: item.promoted_from_message_id, createdAt: item.created_at, provisional: item.provisional });
      continue;
    }

    if (item.source === "model" || item.activity_type === "delivery") {
      if (isInternalModelUpdate(item)) continue;
      const text = normalizedText(item);
      if (!text) continue;
      const previous = result.at(-1);
      if (previous?.kind === "assistant" && !previous.provisional && !item.provisional && previous.runId === item.run_id) {
        // A Supervisor may emit the same final answer again while closing its
        // synthetic continuation turn. Keep distinct progress updates, but do
        // not show an identical durable answer twice in the conversation.
        if (previous.text !== text) previous.text = `${previous.text}\n\n${text}`;
      } else {
        result.push({ id: item.id, kind: "assistant", text,
          runId: item.run_id,
          createdAt: item.created_at, provisional: item.provisional });
      }
      continue;
    }

    const status = (item.status ?? item.stage ?? "").toLowerCase();
    if (item.activity_type === "approval") continue;
    if (item.source === "harness" && isInternalHarnessLifecycle(item)) continue;
    const tone = noticeTone(item);
    if (item.source === "harness" && tone !== "warning") continue;
    if (hiddenHarnessStatuses.has(status) && noticeTone(item) === "neutral") continue;
    if (tone !== "neutral") {
      const text = normalizedText(item);
      const eventKey = `${item.run_id}:${item.sequence}:${item.id}`;
      if (noticeEvents.has(eventKey)) continue;
      noticeEvents.add(eventKey);
      const failureOrigin = item.source === "harness" && item.kind === "harness_status" &&
        item.stage === "blocked" && ["failed", "cancelled"].includes(item.status ?? "") &&
        item.durable && !item.provisional && item.source_ref
        ? { runId: item.run_id, sourceRef: item.source_ref, eventSequence: item.sequence } : undefined;
      result.push({ id: item.id, kind: "notice", tone,
        text, createdAt: item.created_at, ...(failureOrigin ? { failureOrigin } : {}) });
    }
  }

  return result;
}

function fileChangeState(status: string | undefined): NarrativeFileChange {
  switch (status) {
  case "pending": case "proposed": return "proposed";
  case "approved": return "approved";
  case "completed": case "applied": return "applied";
  case "denied": return "denied";
  case "failed": return "failed";
  default: return "unknown";
  }
}

function safeWebEvidenceNarrative(item: ThreadTranscriptItemView): {
  title: string;
  detail: string;
  status: string;
} | undefined {
  if (item.tool_name !== "web_fetch" || !item.web_evidence) return undefined;
  if (item.status === "robots_ignored") return {
    title: item.title === "Full Access 已忽略站点 Robots 限制" ||
      item.title === "Robots 无法验证，已按 Full Access 继续"
      ? item.title : "网页已抓取（未检查 Robots）",
    detail: item.title === "Full Access 已忽略站点 Robots 限制"
      ? "站点禁止抓取；Full Access 仍继续创建了快照"
      : item.title === "Robots 无法验证，已按 Full Access 继续"
        ? "未能验证站点 Robots 规则；Full Access 仍继续创建了快照"
        : "抓取结果可用，但未验证站点的 Robots 规则",
    status: "robots_ignored",
  };
  switch (item.web_evidence.state) {
  case "fetched": return { title: "网页已抓取", detail: "已创建可引用的网页快照", status: "fetched" };
  case "partial": return { title: "网页已部分抓取", detail: "快照内容不完整，请谨慎引用", status: "partial" };
  case "stale": return { title: "网页快照已过期", detail: "现有快照可能不再反映当前页面", status: "stale" };
  case "blocked": return item.title === "Robots 规则阻止抓取"
    ? { title: "Robots 规则阻止抓取", detail: "站点的 Robots 规则不允许本次抓取", status: "blocked" }
    : { title: "网页抓取被阻止", detail: "未获得可验证的网页内容", status: "blocked" };
  case "failed": return { title: "网页验证不可用", detail: "未能创建可验证的网页快照", status: "verification_unavailable" };
  default: return undefined;
  }
}

function toolLifecycleStageRank(stage: ThreadTranscriptItemView["stage"]): number {
  switch (stage) {
  case "result": case "blocked": return 3;
  case "running": return 2;
  case "arguments_ready": return 1;
  default: return 0;
  }
}

function projectLiveTranscriptItems(context: LiveTranscriptContext,
  snapshot: PublicModelStreamSnapshot,
  liveStatus: PublicModelStreamStatus): ThreadTranscriptItemView[] {
  const knownIdentities = context.identities;
  const runOrdinal = context.runOrdinals.get(snapshot.call.run_id) ?? context.nextRunOrdinal;
  const expectedRound = snapshot.call.tool_round + 1;
  const createdAt = snapshot.updated_at;
  const projected: ThreadTranscriptItemView[] = [];
  const text = snapshot.text.trim();
  const messageItem = snapshot.items.find((item) => item.type === "message");
  const messageIdentity = messageItem?.id ??
    `live-message:${snapshot.call.attempt_id}:${snapshot.call.model_attempt}:${expectedRound}`;
  const commentaryConfirmed = (messageItem ? snapshotItemIsDurable(messageItem, knownIdentities) : false) ||
    context.modelRounds.has(JSON.stringify([snapshot.call.run_id, snapshot.call.attempt_id,
      snapshot.call.model_attempt, expectedRound])) ||
    (Boolean(text) && (context.modelTextTimes.get(JSON.stringify([snapshot.call.run_id, text])) ?? "") >=
      snapshot.call.started_at);
  if (text && !commentaryConfirmed) {
    projected.push({
      version: "thread_transcript.v1",
      id: `live-message:${snapshot.call.attempt_id}:${snapshot.call.model_attempt}:${expectedRound}`,
      canonical_id: messageIdentity,
      run_id: snapshot.call.run_id,
      run_ordinal: runOrdinal,
      sequence: Number.MAX_SAFE_INTEGER - 2,
      activity_type: "message",
      stage: liveStatus === "finalizing" || snapshot.message_complete ? "result" : "running",
      kind: "model_update",
      source: "model",
      title: snapshot.content_kind === "tool_commentary" ? "工作进展" : "Universal-Code",
      detail: text,
      status: liveStatus === "finalizing" || snapshot.message_complete ? "completed" : "running",
      verifiable: false,
      instruction_authorized: false,
      attempt_id: snapshot.call.attempt_id,
      model_attempt: snapshot.call.model_attempt,
      tool_round: expectedRound,
      stream_response_id: messageItem?.response_id ?? snapshot.response_id,
      stream_item_id: messageItem?.id,
      provisional: true,
      durable: false,
      created_at: createdAt,
    });
  }

  snapshot.items.filter((item) => item.type === "tool_call").forEach((item, index) => {
    if (snapshotItemIsDurable(item, knownIdentities)) return;
    projected.push({
      version: "thread_transcript.v1",
      id: `live-tool:${item.id}`,
      canonical_id: item.id,
      run_id: snapshot.call.run_id,
      run_ordinal: runOrdinal,
      sequence: Number.MAX_SAFE_INTEGER - 1,
      position: index + 1,
      activity_type: classifyLiveTool(item.tool_name ?? ""),
      stage: liveToolStage(item.status),
      kind: "tool_call",
      source: "model",
      title: safeToolActivityTitle(item.tool_name ?? "", classifyLiveTool(item.tool_name ?? ""),
        "工具执行"),
      detail: liveToolDetail(item.status, item.argument_bytes ?? 0),
      status: item.status,
      verifiable: false,
      instruction_authorized: false,
      attempt_id: snapshot.call.attempt_id,
      model_attempt: snapshot.call.model_attempt,
      tool_round: expectedRound,
      tool_name: item.tool_name,
      stream_response_id: item.response_id,
      stream_item_id: item.id,
      stream_call_id: item.call_id,
      durable_call_id: item.durable_call_id,
      provisional: true,
      durable: false,
      created_at: createdAt,
    });
  });
  return projected;
}

function liveTranscriptContext(transcript: ThreadTranscriptItemView[]): LiveTranscriptContext {
  const identities = new Set<string>();
  const runOrdinals = new Map<string, number>();
  const modelRounds = new Set<string>();
  const modelTextTimes = new Map<string, string>();
  let maxRunOrdinal = 0;
  for (const item of transcript) {
    if (!runOrdinals.has(item.run_id)) runOrdinals.set(item.run_id, item.run_ordinal);
    maxRunOrdinal = Math.max(maxRunOrdinal, item.run_ordinal);
    for (const identity of [item.id, item.canonical_id, item.source_ref, item.stream_item_id,
      item.stream_call_id, item.durable_call_id]) {
      if (identity) identities.add(identity);
    }
    if (item.source === "model") {
      modelRounds.add(JSON.stringify([item.run_id, item.attempt_id, item.model_attempt, item.tool_round]));
      const key = JSON.stringify([item.run_id, item.detail?.trim()]);
      if (item.created_at > (modelTextTimes.get(key) ?? "")) modelTextTimes.set(key, item.created_at);
    }
  }
  return { identities, runOrdinals, nextRunOrdinal: maxRunOrdinal + 1, modelRounds, modelTextTimes };
}

function snapshotItemIsDurable(item: PublicModelStreamSnapshot["items"][number],
  identities: Set<string>): boolean {
  return [item.id, item.call_id, item.durable_call_id]
    .some((identity) => Boolean(identity && identities.has(identity)));
}

function classifyLiveTool(name: string): Exclude<NarrativeToolKind, "plan"> {
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
  default: return "execute";
  }
}

function safeToolActivityTitle(toolName: string, activity: NarrativeToolKind,
  fallback: string): string {
  switch (toolName.toLowerCase()) {
  case "history_search": return "搜索对话历史";
  case "history_read": return "读取历史原文";
  case "plan_delivery_propose": return "生成交付计划";
  case "read_file": case "workspace_read": return "读取文件";
  case "workspace_search": case "workspace_grep": case "workspace_glob": case "code_search":
  case "find_files": return "搜索工作区";
  case "workspace_apply": return "文件应用请求";
  case "workspace_change": case "replace_file": case "file_edit":
  case "apply_patch": return "文件修改请求";
  case "run_tests": case "code_diagnostics": return "验证";
  case "web_search": return "联网搜索";
  case "source_search": return "搜索专业来源";
  case "web_fetch": return "抓取网页";
  }
  if (!toolName) return fallback;
  const generic: Record<NarrativeToolKind, string> = {
    search: "搜索", read: "读取", edit: "修改", execute: "工具执行", verify: "验证", plan: "规划",
  };
  return generic[activity];
}

function liveToolStage(status: string): ThreadTranscriptItemView["stage"] {
  if (status === "ready_for_validation" || status === "completed") return "arguments_ready";
  if (status === "failed" || status === "cancelled") return "blocked";
  return "started";
}

function liveToolDetail(status: string, argumentBytes: number): string {
  const state = status === "ready_for_validation" ? "参数已就绪，等待 Go 验证" :
    status === "completed" ? "准备完成，正在提交 Go 验证" :
      status === "failed" ? "工具请求准备失败" : status === "cancelled" ?
        "工具请求已取消" : "正在准备工具请求";
  return argumentBytes > 0 ? `${state} · ${argumentBytes} 字节` : state;
}
