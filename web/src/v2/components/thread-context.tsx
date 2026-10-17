import { useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { EvidenceInventoryView, ProjectInstructionStateView, ThreadDetailView } from "../../api/types";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { ContextDiagnosticsPanel, validContextDiagnostics, type ContextDiagnostics } from "./context-diagnostics";
import { V2ThreadCompactControl, V2ThreadPinsPanel } from "./thread-context-controls";
import "./thread-context.css";

export interface RunStoredContextSummary {
  id: number; previous_summary_id: number; protocol_version: string; content: string; content_sha256: string;
  content_redacted: boolean; content_truncated: boolean; compacted_message_count: number;
  source_message_count: number; preserved_message_count: number; created_at: string;
}
export interface RunInheritedContext {
  source_run_id: string; source_session_id: string; fingerprint: string; summary_id: number;
  summary_content: string; summary_content_sha256: string; content_redacted: boolean; content_truncated: boolean;
  recent_message_count: number;
  memories: { id: string; scope: "user" | "project"; scope_id: string; version: number; content_sha256: string }[];
}
export interface RunContextSummary {
  run_id: string; thread_id: string; session_id: string; workspace_id: string; capability_grant: false;
  current_summary?: RunStoredContextSummary; inherited_context?: RunInheritedContext;
  diagnostics?: ContextDiagnostics;
  diagnostics_unavailable?: boolean;
}

const record = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value);
const integer = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0;
const digest = (value: unknown): value is string => typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
const text = (value: unknown): value is string => typeof value === "string";
const identity = (value: unknown): value is string => text(value) && value.trim().length > 0;
const publicText = (value: unknown): value is string => text(value) && new TextEncoder().encode(value).length <= 16 * 1024;
const boolean = (value: unknown): value is boolean => typeof value === "boolean";
const invalid = () => new Error("上下文记录的来源或格式不匹配，已停止展示。请重新读取。");

export function parseRunContextSummary(value: unknown, threadID: string, runID: string, sessionID: string, workspaceID: string): RunContextSummary {
  if (!record(value) || value.thread_id !== threadID || value.run_id !== runID || value.session_id !== sessionID ||
    value.workspace_id !== workspaceID || value.capability_grant !== false) throw invalid();
  if (value.diagnostics !== undefined && !validContextDiagnostics(value.diagnostics)) throw invalid();
  if (value.diagnostics_unavailable !== undefined && typeof value.diagnostics_unavailable !== "boolean" ||
    value.diagnostics_unavailable === true && value.diagnostics !== undefined) throw invalid();
  if (value.current_summary !== undefined) {
    const item = value.current_summary;
    if (!record(item) || !integer(item.id) || item.id === 0 || !integer(item.previous_summary_id) ||
      !identity(item.protocol_version) || !publicText(item.content) || !digest(item.content_sha256) ||
      !boolean(item.content_redacted) || !boolean(item.content_truncated) || !integer(item.compacted_message_count) ||
      !integer(item.source_message_count) || !integer(item.preserved_message_count) ||
      item.preserved_message_count > item.source_message_count || !text(item.created_at) || !Number.isFinite(Date.parse(item.created_at))) throw invalid();
  }
  if (value.inherited_context !== undefined) {
    const item = value.inherited_context;
    if (!record(item) || !identity(item.source_run_id) || !identity(item.source_session_id) || !digest(item.fingerprint) ||
      !integer(item.summary_id) || !publicText(item.summary_content) ||
      (item.summary_content ? !digest(item.summary_content_sha256) : item.summary_content_sha256 !== "" || item.summary_id !== 0) ||
      !boolean(item.content_redacted) || !boolean(item.content_truncated) || !integer(item.recent_message_count) ||
      !Array.isArray(item.memories) || item.memories.some((memory) => !record(memory) || !identity(memory.id) ||
        !["user", "project"].includes(String(memory.scope)) || !identity(memory.scope_id) || !integer(memory.version) || memory.version === 0 || !digest(memory.content_sha256))) throw invalid();
  }
  return value as unknown as RunContextSummary;
}

type ContextProps = {
  client: APIClient; threadID: string; detail: ThreadDetailView;
  onClose: () => void; onRequestChange: (content: string) => void;
  returnFocusRef?: RefObject<HTMLElement | null>;
  onOpenRecovery?: (pane: "context" | "checkpoints") => void;
  onOpenSession?: () => void;
  onReply?: (content: string, operationKey: string) => Promise<void>;
};

export function V2ThreadContext(props: ContextProps) {
  // A task change discards only this drawer's local correction editor, never the conversation draft.
  return <ThreadContextContent key={props.threadID} {...props} />;
}

function ThreadContextContent({ client, threadID, detail, onClose, onRequestChange, returnFocusRef, onOpenRecovery, onOpenSession, onReply }: ContextProps) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, closeButton, { isolateBackground: true, returnFocusRef });
  const [correction, setCorrection] = useState("");
  const run = detail.active_run ?? detail.last_run;
  const workspaceID = detail.thread.workspace_id ?? "";
  const sessionID = run.session_id ?? "";
  const bound = detail.thread.id === threadID && Boolean(run.id && sessionID);
  const path = `/runs/${encodeURIComponent(run.id)}`;
  const key = ["thread-context", client.baseURL, threadID, run.id, sessionID, workspaceID];
  const summary = useQuery<RunContextSummary>({ queryKey: [...key, "summary"], enabled: bound, retry: false, refetchOnMount: "always",
    refetchInterval: (query) => run.status === "running" && !query.state.error ? 3000 : false,
    queryFn: async ({ signal }) => parseRunContextSummary(await client.get<unknown>(`${path}/context-summary`, {}, signal), threadID, run.id, sessionID, workspaceID) });
  const instructions = useQuery({ queryKey: [...key, "instructions"], enabled: bound && Boolean(workspaceID), retry: false, refetchOnMount: "always",
    queryFn: async ({ signal }) => {
      const value = await client.get<ProjectInstructionStateView>(`${path}/project-instructions`, {}, signal);
      if (value.run_id !== run.id || value.workspace_id !== workspaceID || value.capability_grant !== false ||
        typeof value.pinned_present !== "boolean" || typeof value.stale !== "boolean" ||
        (value.pinned_present && (value.pinned?.run_id !== run.id || !Array.isArray(value.pinned?.snapshot?.sources) ||
          value.pinned.snapshot.sources.some((source) => !identity(source.path) || !digest(source.content_sha256) ||
            !identity(source.kind) || !text(source.scope))))) throw invalid();
      return value;
    } });
  const evidence = useQuery({ queryKey: [...key, "evidence"], enabled: bound && Boolean(workspaceID), retry: false, refetchOnMount: "always",
    queryFn: async ({ signal }) => {
      const value = await client.evidenceInventory(run.id, signal);
      if (value.protocol_version !== "session_evidence_inventory.v1" || value.run_id !== run.id || !Array.isArray(value.items) || typeof value.truncated !== "boolean" ||
        value.items.some((item) => item.run_id !== run.id || item.session_id !== sessionID || item.workspace_id !== workspaceID ||
          item.instruction_authorized !== false || !identity(item.attachment_id) || !identity(item.source_kind) || !identity(item.source_ref) || !digest(item.content_sha256))) throw invalid();
      return value;
    } });
  const current = summary.isError ? undefined : summary.data?.current_summary;
  const inherited = summary.isError ? undefined : summary.data?.inherited_context;
  const busy = summary.isFetching || instructions.isFetching || evidence.isFetching;
  const refresh = () => { void summary.refetch(); if (workspaceID) { void instructions.refetch(); void evidence.refetch(); } };
  const requestChange = () => {
    if (!bound || !correction.trim()) return;
    const source = [`执行：${run.id}`, ...(current ? [`已保存摘要：${current.id} / SHA256 ${current.content_sha256}`] : [])];
    onRequestChange(`上下文补充与纠正\n${source.join("\n")}\n\n${correction.trim()}`);
  };
  return createPortal(<div className="v2-context-overlay">
    <section className="v2-thread-context" role="dialog" aria-modal="true" aria-label="任务上下文" ref={dialog}>
      <header><h2>任务上下文</h2><button type="button" ref={closeButton} aria-label="关闭任务上下文" onClick={onClose}><X size={18} /></button></header>
      {(onOpenRecovery || onOpenSession) && <nav className="v2-context-workflows" aria-label="上下文与恢复工具">
        {onOpenRecovery && <><button disabled={!bound} onClick={() => onOpenRecovery("context")} type="button">续接与记忆</button>
          <button disabled={!bound} onClick={() => onOpenRecovery("checkpoints")} type="button">工作区恢复</button></>}
        {onOpenSession && <button disabled={!bound} onClick={onOpenSession} type="button">会话原始记录</button>}
      </nav>}
      {onOpenRecovery && <p className="v2-context-help">续接与记忆保留对话来源；工作区恢复使用所选执行的文件检查点。恢复操作仍需预览和当前权限。</p>}
      <p className="v2-context-help">查看此执行保存的项目指令、引用和摘要。需要补充目标或纠正理解时，可在下方写好要求并带回对话。</p>
      {!bound ? <p role="alert">任务与执行记录尚未对应，暂不显示上下文。</p> : <>
        <div className="v2-context-toolbar"><span>{detail.thread.title}</span><button type="button" disabled={busy} onClick={refresh}><RefreshCw size={15} />{busy ? "正在读取" : "重新读取"}</button></div>
        <details className="v2-context-identities"><summary>查看来源标识</summary><dl><dt>任务</dt><dd>{threadID}</dd><dt>执行</dt><dd>{run.id}</dd><dt>会话</dt><dd>{sessionID}</dd><dt>项目</dt><dd>{workspaceID || "未绑定项目"}</dd></dl></details>
        <V2ThreadPinsPanel client={client} threadID={threadID} onReply={onReply} />
        <section aria-labelledby="context-instructions"><h3 id="context-instructions">项目指令</h3>
          {!workspaceID ? <p>此执行没有绑定项目。</p> : instructions.isError ? <ContextError label="项目指令" error={instructions.error} /> : !instructions.data ? <p role="status">正在读取项目指令…</p> : <>
            {instructions.data.stale && <p className="v2-context-notice" role="status">项目指令文件已有变化，此执行沿用创建时固定的版本。需要采用新要求时，请在对话中说明。</p>}
            {!instructions.data.pinned_present ? <p>此执行没有已固定的项目指令快照。</p> : !instructions.data.pinned.snapshot.sources.length ? <p>已固定的快照中没有项目指令文件。</p> : <ul className="v2-context-sources">
              {instructions.data.pinned.snapshot.sources.map((source) => <li key={`${source.ordinal}:${source.path}`}><strong>{source.path}</strong><span>{source.kind === "agents" || source.kind === "agents_md" ? "项目指令" : source.kind} · 作用范围：{source.scope === "" || source.scope === "." ? "项目根目录" : source.scope}</span><details><summary>来源与版本</summary><dl><dt>SHA256</dt><dd>{source.content_sha256}</dd><dt>载入时间</dt><dd>{source.loaded_at}</dd><dt>采用原因</dt><dd>{source.why_effective}</dd><dt>相对顺序</dt><dd>{source.ordinal}</dd></dl>{source.redacted && <p>来源内容已脱敏。</p>}</details></li>)}
            </ul>}
            <p className="v2-context-help">展开“来源与版本”可核对指令文件、作用范围和载入时间。</p>
          </>}
        </section>
        <section aria-labelledby="context-references"><h3 id="context-references">已附加的引用</h3>
          {!workspaceID ? <p>此执行没有项目引用目录。</p> : evidence.isError ? <ContextError label="引用" error={evidence.error} /> : !evidence.data ? <p role="status">正在读取引用…</p> : <ContextReferences value={evidence.data} />}
          <p className="v2-context-help">以下记录已附加的参考资料及其版本。每次请求采用的内容与摘要可能不同；工具读取的其他文件可在工作记录中核对。</p>
        </section>
        {!summary.isError && summary.data && <ContextDiagnosticsPanel value={summary.data.diagnostics} unavailable={summary.data.diagnostics_unavailable}
          recovery={detail.recovery} runID={run.id} runStatus={run.status} />}
        <section aria-labelledby="context-summary"><h3 id="context-summary">摘要与压缩</h3>
          {summary.isError ? <ContextError label="摘要" error={summary.error} /> : !summary.data ? <p role="status">正在读取摘要…</p> : <>
            {current ? <><p>此会话已保存压缩摘要，累计归纳 {current.compacted_message_count} 条消息；保存时保留最近 {current.preserved_message_count} 条原消息。</p>
              <SummaryText content={current.content} redacted={current.content_redacted} truncated={current.content_truncated} />
              <details><summary>摘要版本与来源</summary><dl><dt>摘要 ID</dt><dd>{current.id}</dd><dt>前一摘要 ID</dt><dd>{current.previous_summary_id || "无"}</dd><dt>来源消息计数</dt><dd>{current.source_message_count}</dd><dt>保存时间</dt><dd>{current.created_at}</dd><dt>存储原文 SHA256</dt><dd>{current.content_sha256}</dd></dl></details>
            </> : <p>此会话尚无已保存的压缩摘要，较早消息可在对话记录中查看。</p>}
            {inherited && <div className="v2-context-inherited"><h4>从前序执行继承</h4><p>继承了 {inherited.recent_message_count} 条近期消息和 {inherited.memories.length} 条长期信息引用。</p>
              {inherited.summary_content ? <SummaryText content={inherited.summary_content} redacted={inherited.content_redacted} truncated={inherited.content_truncated} /> : <p>继承快照没有摘要正文。</p>}
              <details><summary>继承来源与长期信息引用</summary><dl><dt>来源执行</dt><dd>{inherited.source_run_id}</dd><dt>来源会话</dt><dd>{inherited.source_session_id}</dd><dt>快照指纹</dt><dd>{inherited.fingerprint}</dd><dt>摘要原文 SHA256</dt><dd>{inherited.summary_content_sha256 || "无"}</dd></dl>
                {inherited.memories.map((memory) => <p key={`${memory.scope}:${memory.scope_id}:${memory.id}`}>{memory.scope === "user" ? "用户" : "项目"}信息：{memory.id} · 版本 {memory.version}<br />SHA256 {memory.content_sha256}</p>)}
              </details></div>}
            <p className="v2-context-help">摘要可能省略细节，消息计数记录压缩当时的范围。持续要求可在上方固定；执行操作仍按当前任务权限审批。</p>
          </>}
          <V2ThreadCompactControl key={run.id} client={client} threadID={threadID} runID={run.id} />
        </section>
        <section className="v2-context-correction" aria-labelledby="context-correction"><h3 id="context-correction">补充与纠正</h3>
          <label htmlFor="thread-context-correction">需要继续保留的目标、限制或纠正</label><textarea id="thread-context-correction" value={correction} onChange={(event) => setCorrection(event.target.value)} rows={4} placeholder="例如：继续原目标，但不要改动已有接口；刚才关于输出格式的判断应改为…" />
          <p className="v2-context-help">点击后将这些要求加入原对话草稿，确认内容后发送给 Agent 处理。</p><button type="button" disabled={!correction.trim()} onClick={requestChange}>补充到对话输入</button>
        </section>
      </>}
    </section>
  </div>, document.body);
}

function ContextError({ label, error }: { label: string; error: Error }) {
  return <p role="alert">{label}读取失败，当前内容未确认。{error.message}</p>;
}

function ContextReferences({ value }: { value: EvidenceInventoryView }) {
  return <>{!value.items.length ? <p>此执行尚无已附加的引用记录。</p> : <ul className="v2-context-sources">{value.items.map((item) => <li key={item.attachment_id}><strong>{item.source_ref}</strong><span>{sourceLabel(item.source_kind)}</span><details><summary>引用版本</summary><dl><dt>引用 ID</dt><dd>{item.attachment_id}</dd><dt>SHA256</dt><dd>{item.content_sha256}</dd><dt>附加时间</dt><dd>{item.attached_at}</dd></dl></details></li>)}</ul>}{value.truncated && <p role="status">引用列表已达到接口上限，当前只显示部分记录。</p>}</>;
}

function sourceLabel(kind: string): string {
  return ({ workspace_file: "项目文件", workspace_image: "图片", file: "文件", tool_result: "工具结果", operator_message: "操作者消息", note: "笔记", artifact: "产物" } as Record<string, string>)[kind] ?? `来源类型：${kind}`;
}

// Decode only the existing stored summary envelopes for reading. The original
// public text remains available; these records never become instructions.
type SummaryExcerpts = { texts: string[]; omitted: number; rolling?: boolean; generated?: { text: string; excerpted: boolean }[] };

function summaryExcerpts(content: string, depth = 0): SummaryExcerpts | null {
  if (depth > 1) return null;
  try {
    const value: unknown = JSON.parse(content);
    if (!record(value)) return null;
    if (value.version === "thread_summary_window.v1" && value.lossy === true && value.instruction_authorized === false &&
      Array.isArray(value.sources) && value.sources.length > 0 && value.sources.length <= 2 &&
      value.sources.every((item) => record(item) && text(item.source_id) && digest(item.content_sha256) &&
        (item.part === "content" || item.part === "summary")) && record(value.projection) && value.projection.version === "handoff_memory.v1") {
      const projection = summaryExcerpts(JSON.stringify(value.projection), depth + 1);
      return projection ? { ...projection, rolling: true } : null;
    }
    if (value.version === "handoff_memory.v1" && Array.isArray(value.records) && integer(value.records_omitted) &&
      value.records.every((item) => record(item) && text(item.content))) {
      let generated: SummaryExcerpts["generated"];
      if (value.generated !== undefined) {
        const item = value.generated;
        if (!record(item) || item.version !== "generated_handoff.v1" || !identity(item.text) ||
          Array.from(item.text).length > 1600 || !digest(item.text_sha256) || !digest(item.input_fingerprint) ||
          item.instruction_authorized !== false || !boolean(item.text_excerpted) || !integer(item.source_refs_omitted) ||
          !Array.isArray(item.source_refs) || item.source_refs.length > 2 ||
          item.source_refs.some((source) => !record(source) || !identity(source.source_id) || !digest(source.content_sha256)) ||
          !record(item.receipt) || !identity(item.receipt.run_id) || !identity(item.receipt.attempt_id) ||
          !integer(item.receipt.model_attempt) || item.receipt.model_attempt === 0 ||
          !integer(item.receipt.completion_sequence) || item.receipt.completion_sequence === 0 ||
          !identity(item.receipt.provider) || !identity(item.receipt.model) || !digest(item.receipt.source_sha256)) return null;
        generated = [{ text: item.text, excerpted: item.text_excerpted }];
      }
      return { texts: value.records.map((item) => item.content as string), omitted: value.records_omitted, generated };
    }
    if (value.kind === "thread_summary_bundle" && Array.isArray(value.summaries) && value.summaries.every((item) => record(item) && text(item.content))) {
      const parts: SummaryExcerpts[] = value.summaries.map((item) => summaryExcerpts(item.content as string, depth + 1) ?? { texts: [item.content as string], omitted: 0 });
      return { texts: parts.flatMap((item) => item.texts), omitted: parts.reduce((sum, item) => sum + item.omitted, 0), generated: parts.flatMap((item) => item.generated ?? []) };
    }
  } catch { /* Historical text and bounded public excerpts remain readable. */ }
  return null;
}

function SummaryText({ content, redacted, truncated }: { content: string; redacted: boolean; truncated: boolean }) {
  const excerpts = truncated ? null : summaryExcerpts(content);
  return <div className="v2-context-summary-text">{redacted && <p role="status">显示文本已脱敏，SHA256 对应存储原文。</p>}{truncated && <p role="status">显示文本达到 16 KiB 上限，当前只展示部分摘要。</p>}
    {excerpts ? <>{excerpts.rolling && <p role="status">这是用于继续对话的滚动摘要，可能省略细节。较早内容仍保留，模型可按来源回读原文。</p>}
      {Boolean(excerpts.generated?.length) && <><h4>模型生成的摘要</h4>{excerpts.generated?.map((item, index) => <div key={index}><p className="v2-context-generated-text">{item.text}</p>{item.excerpted && <p role="status">受摘要容量限制，这里保留了生成说明的部分内容。</p>}</div>)}<p className="v2-context-help">生成说明可能有遗漏或误解；可对照原文摘录，并在下方补充纠正。</p><h4>原文摘录</h4></>}
      {excerpts.texts.map((line, index) => <p key={index}>{line}</p>)}{excerpts.omitted > 0 && <p role="status">这份摘要记录了 {excerpts.omitted} 条条目省略。</p>}<details><summary>查看保存的摘要文本</summary><pre>{content}</pre></details></> : <pre>{content}</pre>}
  </div>;
}
