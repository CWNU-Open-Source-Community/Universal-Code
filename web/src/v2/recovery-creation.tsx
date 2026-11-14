import { useLocale } from "../lib/locale";
import { localUIMessage } from "../lib/local-ui-message";
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { canonicalExactNetworkTarget, canonicalWebAccessSettings } from "../api/client";
import type { APIClient } from "../api/client";
import type { TaskBudgetSettings, ThreadCreationControlRequestView, ThreadDetailView, ThreadView } from "../api/types";
import { normalizedTaskBudget } from "../api/task-configuration";
import type { V2FileReference } from "./components/file-context";
import { inspectV2CreationRequest } from "./recovery-api";
import { recoveryTurnKey, validRecoveryFiles, validRecoveryTurn } from "./recovery-session";
import { useV2PersistenceWarning, useV2RecoveryStore, V2PersistenceError } from "./recovery-storage";
import type { V2TurnInput } from "./use-thread-turn";
import { validImageAttachments, type WorkspaceImageAttachment } from "../api/image-attachments";
import { validV2DraftVersion, type V2DraftVersion } from "./draft-version";
import { validFileAttachments, type WorkspaceFileAttachment } from "../api/file-attachments";
import { submitSkillChoice, validProjectSkillChoice, type ProjectSkillChoice } from "../api/project-skills";
import { validMCPReferences, type MCPDraftReference } from "../api/mcp-references";
import type { ExecutionApprovalMode } from "./components/approval-mode-contract";
import { V2ConfirmDialog } from "./components/dialog";
import { applyCreationApproval } from "./creation-approval";

export interface CreationIntent {
  version: "v2_creation_recovery.v1";
  operationID: string;
  request: ThreadCreationControlRequestView & { workspace_id: string };
  content: string;
  files: V2FileReference[];
  images?: WorkspaceImageAttachment[];
  attachments?: WorkspaceFileAttachment[];
  mcpReferences?: MCPDraftReference[];
  projectSkills?: ProjectSkillChoice;
  approvalMode?: ExecutionApprovalMode;
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

function validRequest(value: unknown): value is CreationIntent["request"] {
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
  if (!record(value)) return false;
  let expectedGoal = "文件对话";
  if (Array.isArray(value.mcpReferences) && value.mcpReferences.length) expectedGoal = "MCP 内容";
  if (Array.isArray(value.images) && value.images.length) expectedGoal = "图片对话";
  if (typeof value.content === "string" && value.content) expectedGoal = value.content;
  if (Object.keys(value).some((key) => !["version", "operationID", "request", "content", "files", "images", "attachments", "mcpReferences", "projectSkills", "approvalMode", "submittedDraft", "draftVersion", "createdAt"].includes(key)) || value.version !== "v2_creation_recovery.v1" ||
    !identity(value.operationID) || value.operationID.length > 128 || !validRequest(value.request) ||
    typeof value.content !== "string" || new TextEncoder().encode(value.content).byteLength > 16384 ||
    (!text(value.content) && !(Array.isArray(value.images) && value.images.length > 0) &&
      !(Array.isArray(value.attachments) && value.attachments.length > 0) && !(Array.isArray(value.mcpReferences) && value.mcpReferences.length > 0)) ||
    value.request.goal !== expectedGoal || !validRecoveryFiles(value.files) ||
    (value.images !== undefined && !validImageAttachments(value.images, value.request.workspace_id)) ||
    (value.attachments !== undefined && !validFileAttachments(value.attachments, value.request.workspace_id)) ||
    (value.mcpReferences !== undefined && !validMCPReferences(value.mcpReferences)) ||
    (value.projectSkills !== undefined && !validProjectSkillChoice(value.projectSkills)) ||
    (value.approvalMode !== undefined && !["ask", "auto", "full"].includes(String(value.approvalMode))) ||
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
const intentBody = ({ request, content, files, images, attachments, mcpReferences, projectSkills, submittedDraft }: CreationIntent) =>
  ({ request, content, files, images, attachments, mcpReferences, projectSkills, submittedDraft });

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

// A route remount must not start another permission POST while the original
// creation is still completing. Keep only live flights, never authorization.
const creationFlights = new Set<string>();
const flightListeners = new Set<() => void>();
let flightRevision = 0;
const notifyFlights = () => {
  flightRevision++;
  flightListeners.forEach((listener) => listener());
};
const subscribeFlights = (listener: () => void) => {
  flightListeners.add(listener);
  return () => { flightListeners.delete(listener); };
};
const changeFlight = (key: string, active: boolean) => {
  if (active) creationFlights.add(key); else creationFlights.delete(key);
  notifyFlights();
};

export function useV2CreationRecovery(client: APIClient, workspaceID: string,
  onRecovered: (thread: ThreadView, submittedDraft: string, files: V2FileReference[], input: V2TurnInput, shouldOpen: boolean) => void,
  busy = false,
): {
  prepare(request: ThreadCreationControlRequestView, content: string, files: V2FileReference[], submittedDraft: string, images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], projectSkills?: ProjectSkillChoice, mcpReferences?: MCPDraftReference[], approvalMode?: ExecutionApprovalMode): CreationIntent;
  resolve(intent: CreationIntent, confirmFull?: boolean): Promise<ThreadView>;
  handoff(intent: CreationIntent, thread: ThreadView): V2TurnInput;
  notice: ReactNode;
  pending: boolean;
} {
  const { t } = useLocale();
  const store = useV2RecoveryStore();
  const unhandledStorageWarning = useV2PersistenceWarning(workspaceID
    ? `image-upload:${JSON.stringify([workspaceID, ""])}:` : undefined);
  const [intents, setIntents] = useState<CreationIntent[]>([]);
  const [observations, setObservations] = useState<Record<string, Observation>>({});
  const [loadError, setLoadError] = useState("");
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const verifiedThreads = useRef(new Map<string, ThreadView>());
  const [fullConfirmation, setFullConfirmation] = useState<CreationIntent | null>(null);
  const recovering = useRef(false);
  const [recoveringID, setRecoveringID] = useState("");
  const flightKey = JSON.stringify([client.baseURL, workspaceID]);
  const observedFlightRevision = useSyncExternalStore(subscribeFlights, () => flightRevision);
  const pending = creationFlights.has(flightKey);
  const context = useRef({ client, workspaceID, generation: 0, mounted: true });
  if (context.current.client !== client || context.current.workspaceID !== workspaceID) {
    context.current = { client, workspaceID, generation: context.current.generation + 1, mounted: true };
  }
  useEffect(() => {
    context.current.mounted = true;
    return () => { context.current.mounted = false; context.current.generation++; };
  }, []);

  const loadIntents = (): CreationIntent[] => {
    if (!store) throw new V2PersistenceError(t("当前连接不支持可靠保存创建请求，请先完成连接。", "This connection cannot reliably save creation requests. Complete the connection first."));
    const entries = store.entries<unknown>("creation:");
    const turns = store.entries<unknown>("turn:");
    const malformed = entries.some(([key, value]) => !validIntent(value) || key !== creationKey(value)) ||
      turns.some(([key, value]) => !validRecoveryTurn(value) || key !== recoveryTurnKey(value));
    try { store.assertReadable(); } catch (failure) {
      // Preserve the global guard and its warning while identifying this
      // journal's own failure ahead of an unrelated image-record error.
      if (malformed) throw invalid();
      throw failure;
    }
    if (malformed) throw invalid();
    return entries.map(([, value]) => value as CreationIntent);
  };
  const previousFlightRevision = useRef(observedFlightRevision);
  useEffect(() => {
    const changed = previousFlightRevision.current !== observedFlightRevision;
    previousFlightRevision.current = observedFlightRevision;
    if (!changed || pending) return;
    try { setIntents(loadIntents().filter((intent) => intent.request.workspace_id === workspaceID)); }
    catch (failure) { setLoadError(failure instanceof Error ? failure.message : invalid().message); }
    // Reconcile an original page's completed handoff after a route remount;
    // this only reads the local journal and never replays a request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [observedFlightRevision, pending, workspaceID]);
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
    setError("");
    setFullConfirmation(null);
    let stopRetry: (() => void) | undefined;
    const load = () => {
      let saved: CreationIntent[];
      try {
        saved = loadIntents().filter((intent) => intent.request.workspace_id === workspaceID);
      } catch (failure) {
        setLoadError(failure instanceof Error ? failure.message : invalid().message);
        return false;
      }
      // Only a failed initial read listens to store changes. Once readable,
      // typing and unrelated writes must not re-inspect unknown creations.
      stopRetry?.(); stopRetry = undefined;
      setIntents(saved);
      setLoadError("");
      for (const intent of saved) {
        void inspect(intent, abort.signal).then((thread) => {
          if (abort.signal.aborted) return;
          if (thread && !intent.projectSkills?.refs.length && intent.approvalMode === undefined) verifiedThreads.current.set(intent.operationID, thread);
          setObservations((current) => ({ ...current, [intent.operationID]: thread
            ? { thread, message: t("已找到原对话。点击“打开原对话”查看首条消息的提交结果。", "The original conversation was found. Open it to check the first message submission.") }
            : { message: t("本次查询尚未找到创建记录。可点击“重新核对”；再次发送相同内容时会先核对原请求。", "No creation record was found. Check again; resending the same content checks the original request first.") } }));
        }).catch(() => {
          if (!abort.signal.aborted) setObservations((current) => ({ ...current,
            [intent.operationID]: { message: t("暂时无法核对原创建请求，原内容与请求标识已保留。请点击“重新核对”查看结果。", "Could not check the original creation request. Its content and identity are kept. Select Check again to read the result.") } }));
        });
      }
      return true;
    };
    if (!load()) stopRetry = store.subscribePrefix?.("", load);
    return () => { stopRetry?.(); abort.abort(); };
    // Mount/scope/explicit refresh scan once; a failed load waits for storage
    // repair. Preparing a send must not schedule an extra creation action.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [store, client, workspaceID, refresh]);

  const prepare = (request: ThreadCreationControlRequestView, content: string, files: V2FileReference[], submittedDraft: string, images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], projectSkills?: ProjectSkillChoice, mcpReferences?: MCPDraftReference[], approvalMode?: ExecutionApprovalMode): CreationIntent => {
    if (creationFlights.has(flightKey) || recovering.current) throw new Error(t("正在提交原请求，请稍候。", "The original request is being submitted. Please wait."));
    context.current.generation++;
    if (!validRequest(request)) throw invalid();
    const saved = loadIntents();
    const candidate: CreationIntent = { version: "v2_creation_recovery.v1", operationID: crypto.randomUUID(),
      request, content, files, ...(images?.length ? { images } : {}), ...(attachments?.length ? { attachments } : {}), submittedDraft,
      ...(mcpReferences?.length ? { mcpReferences } : {}),
      ...(projectSkills?.refs.length ? { projectSkills } : {}),
      ...(approvalMode !== undefined ? { approvalMode } : {}),
      ...(draftVersion !== undefined ? { draftVersion } : {}), createdAt: new Date().toISOString() };
    if (!validIntent(candidate) || request.workspace_id !== workspaceID) throw invalid();
    const matched = saved.find((intent) => canonical(intentBody(intent)) === canonical(intentBody(candidate)));
    if (matched) {
      // Changing approval choices must not create another conversation after a
      // lost creation response. The backend creation body/key stays the same.
      if (approvalMode === undefined || matched.approvalMode === approvalMode) return matched;
      const updated = { ...matched, approvalMode };
      store!.write(creationKey(updated), updated);
      verifiedThreads.current.delete(updated.operationID);
      setIntents((current) => current.map((item) => item.operationID === updated.operationID ? updated : item));
      return updated;
    }
    const intent = JSON.parse(JSON.stringify(candidate)) as CreationIntent;
    store!.write(creationKey(intent), intent);
    setIntents((current) => [...current, intent]);
    return intent;
  };
  const resolve = async (intent: CreationIntent, confirmFull = false): Promise<ThreadView> => {
    if (creationFlights.has(flightKey)) throw new Error(t("正在提交原请求，请稍候。", "The original request is being submitted. Please wait."));
    const saved = requireSaved(intent);
    changeFlight(flightKey, true);
    verifiedThreads.current.delete(saved.operationID);
    try {
      if (saved.approvalMode === "full" && !confirmFull) throw new Error(t("请先确认完全访问权限，首条消息尚未发送。", "Confirm Full access before sending the first message."));
      const existing = await inspect(saved);
      // Recheck local ownership after the GET, before any creation POST.
      requireSaved(saved);
      const thread = existing ?? boundThread((await client.createThread(saved.request, createOperationKey(saved))).thread, workspaceID);
      if (saved.projectSkills?.refs.length) {
        await submitSkillChoice(client, thread.last_run_id, saved.projectSkills, saved.operationID);
        requireSaved(saved);
      }
      requireSaved(saved);
      await applyCreationApproval(client, thread.id, saved.approvalMode, confirmFull);
      requireSaved(saved);
      verifiedThreads.current.set(saved.operationID, thread);
      setObservations((current) => ({ ...current, [saved.operationID]: { thread, message: t("已找到原对话；首条消息将沿用原请求标识。", "The original conversation was found. Its first message will use the original request identity.") } }));
      return thread;
    } catch (failure) {
      setObservations((current) => ({ ...current, [saved.operationID]: {
        message: `${t("首条消息尚未发送，原对话和内容已保留。", "The first message has not been sent. The original conversation and content are kept. ")}${failure instanceof Error ? localUIMessage(failure.message, t) : t("请重新核对后重试。", "Check again and retry.")}`, } }));
      throw failure;
    } finally {
      changeFlight(flightKey, false);
    }
  };

  const recover = async (intent: CreationIntent, confirmFull = false) => {
    if (recovering.current || creationFlights.has(flightKey) || busy) return;
    const generation = context.current.generation;
    recovering.current = true;
    setRecoveringID(intent.operationID);
    setError("");
    setFullConfirmation(null);
    try {
      const inspected = verifiedThreads.current.get(intent.operationID);
      const thread = intent.approvalMode === undefined && !intent.projectSkills?.refs.length && inspected
        ? inspected : await resolve(intent, confirmFull);
      const input = handoff(intent, thread);
      onRecovered(thread, intent.submittedDraft, intent.files, input,
        context.current.mounted && context.current.generation === generation);
    } catch (failure) {
      if (context.current.mounted && context.current.generation === generation) {
        setError(failure instanceof Error ? failure.message : invalid().message);
      }
    }
    finally { recovering.current = false; setRecoveringID(""); }
  };
  const handoff = (intent: CreationIntent, thread: ThreadView): V2TurnInput => {
    const saved = requireSaved(intent);
    if (verifiedThreads.current.get(saved.operationID)?.id !== thread.id) throw invalid();
    boundThread(thread, workspaceID);
    const input: V2TurnInput = { threadID: thread.id, workspaceID, content: saved.content, draft: saved.submittedDraft,
      ...(saved.files.length ? { files: saved.files } : {}),
      ...(saved.images?.length ? { images: saved.images } : {}),
      ...(saved.attachments?.length ? { attachments: saved.attachments } : {}),
      ...(saved.mcpReferences?.length ? { mcpReferences: saved.mcpReferences } : {}),
      ...(saved.draftVersion ? { draftVersion: saved.draftVersion } : {}),
      operationKey: `v2-thread-create-turn-${saved.operationID}`, createdAt: saved.createdAt };
    const previous = store!.read<unknown>(recoveryTurnKey(input), null);
    if (previous !== null && (!validRecoveryTurn(previous) || canonical(previous) !== canonical(input))) throw invalid();
    store!.write(recoveryTurnKey(input), input);
    store!.remove(creationKey(saved));
    notifyFlights();
    setIntents((current) => current.filter((item) => item.operationID !== saved.operationID));
    return input;
  };

  // NewConversation always mounts the current draft's image recovery actions.
  // Only a storage load warning represented by those actions is projected out.
  const visibleLoadError = loadError && loadError === store?.getWarning() ? unhandledStorageWarning : loadError;
  const notice = !store ? null : <>
    {(visibleLoadError || error) && <p className="v2-notice tone-warning" role="alert">{localUIMessage(visibleLoadError || error, t)}</p>}
    {intents.map((intent) => <section key={intent.operationID} className="v2-notice" aria-label={t("上次创建的对话", "Previously created conversation")}>
      <p>{intent.request.goal.slice(0, 120)}</p>
      <p role="status">{observations[intent.operationID]?.message ?? t("原创建请求已保存，结果尚待确认。可点击“重新核对”查看记录。", "The original creation request is saved and its result is unconfirmed. Select Check again to read the record.")}</p>
      {observations[intent.operationID]?.thread && <button className="v2-composer-chip" disabled={busy || pending || Boolean(recoveringID)} onClick={() => {
        if (intent.approvalMode === "full") setFullConfirmation(intent);
        else void recover(intent);
      }}>{t("打开原对话", "Open original conversation")}</button>}
      <button className="v2-composer-chip" disabled={busy || pending || Boolean(recoveringID)} onClick={() => setRefresh((value) => value + 1)}>{t("重新核对", "Check again")}</button>
    </section>)}
    {fullConfirmation && <V2ConfirmDialog open danger busy={busy || pending} title={t("启用完全访问权限？", "Enable Full access?")}
      description={t("允许此对话访问项目文件、执行本机命令和访问公共网络，无需逐次批准。可能造成数据丢失或敏感信息泄露。", "Allow this conversation to access project files, run local commands, and access public networks without per-operation approval. This may cause data loss or expose sensitive information.")}
      confirmLabel={t("确认并继续", "Confirm and continue")} onCancel={() => setFullConfirmation(null)}
      onConfirm={() => { void recover(fullConfirmation, true); }} />}
  </>;
  return { prepare, resolve, handoff, notice, pending: pending || Boolean(recoveringID) };
}
