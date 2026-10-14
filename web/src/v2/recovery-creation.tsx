import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { canonicalExactNetworkTarget, canonicalWebAccessSettings } from "../api/client";
import type { APIClient } from "../api/client";
import type { TaskBudgetSettings, ThreadCreationControlRequestView, ThreadDetailView, ThreadView } from "../api/types";
import { normalizedTaskBudget } from "../api/task-configuration";
import type { V2FileReference } from "./components/file-context";
import { inspectV2CreationRequest } from "./recovery-api";
import { recoveryTurnKey, validRecoveryFiles, validRecoveryTurn } from "./recovery-session";
import { useV2RecoveryStore, V2PersistenceError } from "./recovery-storage";
import type { V2TurnInput } from "./use-thread-turn";
import { validImageAttachments, type WorkspaceImageAttachment } from "../api/image-attachments";
import { validV2DraftVersion, type V2DraftVersion } from "./draft-version";
import { validFileAttachments, type WorkspaceFileAttachment } from "../api/file-attachments";
import { validProjectSkillChoice, type ProjectSkillChoice } from "../api/project-skills";

export interface CreationIntent {
  version: "v2_creation_recovery.v1";
  operationID: string;
  request: ThreadCreationControlRequestView;
  content: string;
  files: V2FileReference[];
  images?: WorkspaceImageAttachment[];
  attachments?: WorkspaceFileAttachment[];
  projectSkills?: ProjectSkillChoice;
  submittedDraft: string;
  draftVersion?: V2DraftVersion;
  createdAt: string;
}

const invalid = () => new V2PersistenceError("原创建请求无法可靠核对；已保留原记录，未创建或发送新消息。");
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const identity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
const text = (value: unknown, maxBytes = 16384): value is string => typeof value === "string" &&
  value.trim().length > 0 && new TextEncoder().encode(value).byteLength <= maxBytes;
const creationKey = (intent: CreationIntent) => `creation:${intent.operationID}`;
const createOperationKey = (intent: CreationIntent) => `v2-thread-create-${intent.operationID}`;
const allowedRequestKeys = new Set(["version", "workspace_id", "goal", "profile", "surface", "phase", "network_mode", "allowed_targets", "web_access_mode", "web_allowed_targets", "provider", "model", "budget"]);

function validRequest(value: unknown): value is ThreadCreationControlRequestView {
  if (!record(value) || Object.keys(value).some((key) => !allowedRequestKeys.has(key)) ||
    value.version !== "thread_creation.v1" || !identity(value.workspace_id) || !text(value.goal) ||
    (value.profile !== undefined && !["code", "review", "learn", "script"].includes(String(value.profile))) ||
    (value.surface !== undefined && !["code", "cyber"].includes(String(value.surface))) ||
    (value.phase !== undefined && !["plan", "deliver"].includes(String(value.phase))) ||
    (value.network_mode !== undefined && !["disabled", "allowlist"].includes(String(value.network_mode))) ||
    ((value.provider === undefined) !== (value.model === undefined))) return false;
  try { normalizedTaskBudget(value.budget as TaskBudgetSettings | undefined); } catch { return false; }
  if (value.web_access_mode !== undefined || value.web_allowed_targets !== undefined) {
    try { canonicalWebAccessSettings(value.web_access_mode, value.web_allowed_targets ?? []); }
    catch { return false; }
  }
  for (const name of ["provider", "model"]) {
    if (value[name] !== undefined && (typeof value[name] !== "string" || !value[name] ||
      value[name].trim() !== value[name] || [...value[name]].length > 256 || /[\u0000-\u001f\u007f/]/u.test(value[name]))) return false;
  }
  if (value.allowed_targets !== undefined && (!Array.isArray(value.allowed_targets) || value.allowed_targets.length > 256 ||
    !value.allowed_targets.every((target) => typeof target === "string" && target.trim() === target && text(target, 4096)))) return false;
  if (value.network_mode === "allowlist") {
    if (!Array.isArray(value.allowed_targets) || value.allowed_targets.length === 0) return false;
    try { value.allowed_targets.forEach((target) => canonicalExactNetworkTarget(target)); }
    catch { return false; }
    return true;
  }
  return value.allowed_targets === undefined || value.allowed_targets.length === 0;
}

function validIntent(value: unknown): value is CreationIntent {
  if (!record(value) || Object.keys(value).some((key) => !["version", "operationID", "request", "content", "files", "images", "attachments", "projectSkills", "submittedDraft", "draftVersion", "createdAt"].includes(key)) || value.version !== "v2_creation_recovery.v1" ||
    !identity(value.operationID) || value.operationID.length > 128 || !validRequest(value.request) ||
    typeof value.content !== "string" || new TextEncoder().encode(value.content).byteLength > 16384 ||
    (!text(value.content) && !(Array.isArray(value.images) && value.images.length > 0) &&
      !(Array.isArray(value.attachments) && value.attachments.length > 0)) ||
    value.request.goal !== (value.content || (Array.isArray(value.images) && value.images.length > 0 ? "图片对话" : "文件对话")) || !validRecoveryFiles(value.files) ||
    (value.images !== undefined && !validImageAttachments(value.images, value.request.workspace_id)) ||
    (value.attachments !== undefined && !validFileAttachments(value.attachments, value.request.workspace_id)) ||
    (value.projectSkills !== undefined && !validProjectSkillChoice(value.projectSkills)) ||
    (value.draftVersion !== undefined && !validV2DraftVersion(value.draftVersion, value.request.workspace_id)) ||
    typeof value.submittedDraft !== "string" || value.submittedDraft.trim() !== value.content.trim() ||
    typeof value.createdAt !== "string" || !Number.isFinite(Date.parse(value.createdAt))) return false;
  return true;
}

// Compare every admitted body and draft/reference field. Property insertion
// order is immaterial, while changed targets, file hashes or text get a new key.
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (record(value)) return `{${Object.keys(value).filter((key) => value[key] !== undefined).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
// A later edit can return to the same submitted body while its local draft ref
// advances. Recover that original creation/key; its old ref only governs cleanup.
const intentBody = ({ request, content, files, images, attachments, projectSkills, submittedDraft }: CreationIntent) =>
  ({ request, content, files, images, attachments, projectSkills, submittedDraft });

function boundThread(value: unknown, workspaceID: string, expectedID?: string): ThreadView {
  if (!record(value) || value.protocol_version !== "thread.v1" || !identity(value.id) ||
    (expectedID !== undefined && value.id !== expectedID) || value.workspace_id !== workspaceID ||
    !identity(value.mission_id) || !identity(value.last_run_id) || typeof value.title !== "string" ||
    !["active", "archived", "deleted"].includes(String(value.status)) ||
    !["ready", "waiting_approval", "successor_required", "unavailable"].includes(String(value.composer_state)) ||
    !Number.isSafeInteger(value.version) || Number(value.version) < 1 ||
    typeof value.created_at !== "string" || !Number.isFinite(Date.parse(value.created_at)) ||
    typeof value.updated_at !== "string" || !Number.isFinite(Date.parse(value.updated_at))) throw invalid();
  return value as unknown as ThreadView;
}

type Observation = { thread?: ThreadView; message: string };

export function useV2CreationRecovery(client: APIClient, workspaceID: string,
  onRecovered: (thread: ThreadView, submittedDraft: string, files: V2FileReference[], input: V2TurnInput) => void,
): {
  prepare(request: ThreadCreationControlRequestView, content: string, files: V2FileReference[], submittedDraft: string, images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], projectSkills?: ProjectSkillChoice): CreationIntent;
  resolve(intent: CreationIntent): Promise<ThreadView>;
  handoff(intent: CreationIntent, thread: ThreadView): V2TurnInput;
  notice: ReactNode;
} {
  const store = useV2RecoveryStore();
  const [intents, setIntents] = useState<CreationIntent[]>([]);
  const [observations, setObservations] = useState<Record<string, Observation>>({});
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const verifiedThreads = useRef(new Map<string, ThreadView>());

  const loadIntents = (): CreationIntent[] => {
    if (!store) throw new V2PersistenceError("当前连接不支持可靠保存创建请求，请先完成连接。");
    const entries = store.entries<unknown>("creation:");
    const turns = store.entries<unknown>("turn:");
    store.assertReadable();
    if (entries.some(([key, value]) => !validIntent(value) || key !== creationKey(value)) ||
      turns.some(([key, value]) => !validRecoveryTurn(value) || key !== recoveryTurnKey(value))) throw invalid();
    return entries.map(([, value]) => value as CreationIntent);
  };
  const requireSaved = (intent: CreationIntent): CreationIntent => {
    const saved = loadIntents().find((item) => item.operationID === intent.operationID);
    if (!validIntent(intent) || !saved || canonical(saved) !== canonical(intent) || saved.request.workspace_id !== workspaceID) throw invalid();
    return saved;
  };
  const inspect = async (intent: CreationIntent, signal?: AbortSignal): Promise<ThreadView | null> => {
    const result = await inspectV2CreationRequest(client, { operationKey: createOperationKey(intent), workspaceID: intent.request.workspace_id, signal });
    if (result.state === "not_received") return null;
    if (result.state !== "completed" || !result.thread_id) throw invalid();
    const detail = await client.get<ThreadDetailView>(`/threads/${encodeURIComponent(result.thread_id)}`, {}, signal);
    return boundThread(detail?.thread, intent.request.workspace_id, result.thread_id);
  };

  useEffect(() => {
    if (!store) return;
    const abort = new AbortController();
    verifiedThreads.current.clear();
    setObservations({});
    setIntents([]);
    let saved: CreationIntent[];
    try {
      saved = loadIntents().filter((intent) => intent.request.workspace_id === workspaceID);
      setIntents(saved);
      setError("");
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : invalid().message);
      return;
    }
    for (const intent of saved) {
      void inspect(intent, abort.signal).then((thread) => {
        if (abort.signal.aborted) return;
        if (thread && !intent.projectSkills?.refs.length) verifiedThreads.current.set(intent.operationID, thread);
        setObservations((current) => ({ ...current, [intent.operationID]: thread
          ? { thread, message: "已找到原对话。点击“打开原对话”查看首条消息的提交结果。" }
          : { message: "本次查询尚未找到创建记录。可点击“重新核对”；再次发送相同内容时会先核对原请求。" } }));
      }).catch(() => {
        if (!abort.signal.aborted) setObservations((current) => ({ ...current,
          [intent.operationID]: { message: "暂时无法核对原创建请求，原内容与请求标识已保留。请点击“重新核对”查看结果。" } }));
      });
    }
    return () => abort.abort();
    // Only mounting, changing scope or an explicit read-only refresh scans the
    // journal. Preparing a new send must not schedule an extra creation action.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [store, client, workspaceID, refresh]);

  const prepare = (request: ThreadCreationControlRequestView, content: string, files: V2FileReference[], submittedDraft: string, images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], projectSkills?: ProjectSkillChoice): CreationIntent => {
    const saved = loadIntents();
    const candidate: CreationIntent = { version: "v2_creation_recovery.v1", operationID: crypto.randomUUID(),
      request, content, files, ...(images?.length ? { images } : {}), ...(attachments?.length ? { attachments } : {}), submittedDraft,
      ...(projectSkills?.refs.length ? { projectSkills } : {}),
      ...(draftVersion !== undefined ? { draftVersion } : {}), createdAt: new Date().toISOString() };
    if (!validIntent(candidate) || request.workspace_id !== workspaceID) throw invalid();
    const matched = saved.find((intent) => canonical(intentBody(intent)) === canonical(intentBody(candidate)));
    if (matched) return matched;
    const intent = JSON.parse(JSON.stringify(candidate)) as CreationIntent;
    store!.write(creationKey(intent), intent);
    setIntents((current) => [...current, intent]);
    return intent;
  };
  const resolve = async (intent: CreationIntent): Promise<ThreadView> => {
    const saved = requireSaved(intent);
    try {
      const existing = await inspect(saved);
      // Recheck local ownership after the GET, before any creation POST.
      requireSaved(saved);
      const thread = existing ?? boundThread((await client.createThread(saved.request, createOperationKey(saved))).thread, workspaceID);
      if (saved.projectSkills?.refs.length) {
        await client.selectProjectSkills(thread.last_run_id, saved.projectSkills, `v2-project-skills-${saved.operationID}`);
        requireSaved(saved);
      }
      verifiedThreads.current.set(saved.operationID, thread);
      setObservations((current) => ({ ...current, [saved.operationID]: { thread, message: "已找到原对话；首条消息将沿用原请求标识。" } }));
      return thread;
    } catch (failure) {
      setObservations((current) => ({ ...current, [saved.operationID]: {
        message: `创建或技能选择尚未完成，原对话和内容已保留。${failure instanceof Error ? failure.message : "请重新核对后重试。"}`, } }));
      throw failure;
    }
  };
  const handoff = (intent: CreationIntent, thread: ThreadView): V2TurnInput => {
    const saved = requireSaved(intent);
    if (verifiedThreads.current.get(saved.operationID)?.id !== thread.id) throw invalid();
    boundThread(thread, workspaceID);
    const input: V2TurnInput = { threadID: thread.id, workspaceID, content: saved.content, draft: saved.submittedDraft,
      ...(saved.files.length ? { files: saved.files } : {}),
      ...(saved.images?.length ? { images: saved.images } : {}),
      ...(saved.attachments?.length ? { attachments: saved.attachments } : {}),
      ...(saved.draftVersion ? { draftVersion: saved.draftVersion } : {}),
      operationKey: `v2-thread-create-turn-${saved.operationID}`, createdAt: saved.createdAt };
    const previous = store!.read<unknown>(recoveryTurnKey(input), null);
    if (previous !== null && (!validRecoveryTurn(previous) || canonical(previous) !== canonical(input))) throw invalid();
    store!.write(recoveryTurnKey(input), input);
    store!.remove(creationKey(saved));
    setIntents((current) => current.filter((item) => item.operationID !== saved.operationID));
    return input;
  };

  const notice = !store ? null : <>
    {error && <p className="v2-notice tone-warning" role="alert">{error}</p>}
    {intents.map((intent) => <section key={intent.operationID} className="v2-notice" aria-label="上次创建的对话">
      <p>{intent.request.goal.slice(0, 120)}</p>
      <p role="status">{observations[intent.operationID]?.message ?? "原创建请求已保存，结果尚待确认。可点击“重新核对”查看记录。"}</p>
      {observations[intent.operationID]?.thread && <button className="v2-composer-chip" onClick={async () => {
        try {
          const thread = intent.projectSkills?.refs.length ? await resolve(intent) : observations[intent.operationID].thread!;
          const input = handoff(intent, thread);
          onRecovered(thread, intent.submittedDraft, intent.files, input);
        } catch (failure) { setError(failure instanceof Error ? failure.message : invalid().message); }
      }}>打开原对话</button>}
      <button className="v2-composer-chip" onClick={() => setRefresh((value) => value + 1)}>重新核对</button>
    </section>)}
  </>;
  return { prepare, resolve, handoff, notice };
}
