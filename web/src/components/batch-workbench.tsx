import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../api/client";
import type { BatchWorkbenchPrepareRequestView, BatchWorkbenchView, ChildTaskProposalView } from "../api/types";
import { useLocale } from "../lib/locale";
import { ErrorState, LoadingState } from "./common";
import { recoveryObject, recoveryPath, recoveryText, useVerificationRecord } from "./verification-recovery";
import "./batch-workbench.css";

const version = "batch-delivery-workbench.v1";
type PrepareIntent = { version: 1; runID: string; key: string; body: BatchWorkbenchPrepareRequestView; legacy?: true };
type ChildIntent = { version: 1; runID: string; planID: string; ordinal: number; key: string; kind: "owner" | "execute"; generation: number; retry: boolean; legacy?: true };
const selectionKey = (runID: string) => `batch-workbench-selection:${JSON.stringify([runID])}`;
const draftKey = (runID: string, proposalID: string) => `batch-workbench-draft:${JSON.stringify([runID, proposalID])}`;
const settlementKey = (runID: string, suffix: string) => `batch-workbench-legacy-settled:${JSON.stringify([runID, suffix])}`;

function readLegacy<T>(client: APIClient, runID: string, suffix: string, convert: (value: unknown) => T | null): { intent: T | null; error: string } {
  // Old windows did not record data_store_id. Read only this endpoint/Run key;
  // the current service must independently confirm the Run before migration.
  try {
    const raw = sessionStorage.getItem(`universal-code.batch-workbench.v1:${client.baseURL}:${runID}:${suffix}`);
    if (raw === null) return { intent: null, error: "" };
    if (new TextEncoder().encode(raw).byteLength > 2 * 1024 * 1024) throw new Error("legacy request exceeds recovery capacity");
    const intent = convert(JSON.parse(raw));
    if (!intent) throw new Error("invalid legacy request");
    return { intent, error: "" };
  } catch { return { intent: null, error: "无法核对旧窗口的批量请求；原记录已保留，当前不能创建新操作。" }; }
}
function useLegacyBatchRecovery<T extends PrepareIntent | ChildIntent>(client: APIClient, runID: string, suffix: string,
  journal: ReturnType<typeof useVerificationRecord<T | null>>, readiness: boolean | ((request: T) => boolean), convert: (value: unknown) => T | null) {
  const source = useMemo(() => readLegacy(client, runID, suffix, convert), [client.baseURL, runID, suffix]);
  const ready = Boolean(source.intent && (typeof readiness === "function" ? readiness(source.intent) : readiness));
  const settled = useVerificationRecord<{ version: 1; runID: string; key: string } | null>(settlementKey(runID, suffix), null,
    (value): value is { version: 1; runID: string; key: string } | null => value === null || recoveryObject(value) && value.version === 1 && value.runID === runID && isKey(value));
  const pending = Boolean(source.intent && !journal.value && settled.value?.key !== source.intent.key);
  const [failure, setFailure] = useState({ store: journal.store, key: journal.key, message: "" });
  const recover = () => {
    if (!pending || !ready || !source.intent) return;
    try {
      if (settled.error) throw new Error(settled.error);
      journal.save({ ...source.intent, legacy: true });
      setFailure({ store: journal.store, key: journal.key, message: "" });
    } catch (error) { setFailure({ store: journal.store, key: journal.key, message: error instanceof Error ? error.message : String(error) }); }
  };
  useEffect(() => { recover(); }, [journal.store, journal.key, journal.value, ready, source, settled.value]);
  return { blocked: !journal.value && Boolean(source.error || pending), pending,
    error: source.error || (failure.store === journal.store && failure.key === journal.key ? failure.message : ""), recover };
}
function settleLegacy(request: PrepareIntent | ChildIntent, journal: { store: ReturnType<typeof useVerificationRecord>["store"] }, suffix: string) {
  if (request.legacy) journal.store!.write(settlementKey(request.runID, suffix), { version: 1, runID: request.runID, key: request.key });
}
function isKey(value: unknown): value is { key: string } {
  return Boolean(value && typeof value === "object" && "key" in value && typeof value.key === "string" &&
    /^web-batch-[a-z]+-[a-f0-9-]{36}$/u.test(value.key));
}
function isPrepareIntent(value: unknown, runID: string): value is PrepareIntent | null {
  if (value === null) return true;
  if (!isKey(value) || !("version" in value) || value.version !== 1 || !("runID" in value) || value.runID !== runID || !("body" in value) || !recoveryObject(value.body)) return false;
  const body = value.body as Partial<BatchWorkbenchPrepareRequestView>;
  return (!("legacy" in value) || value.legacy === true) && body.version === version && body.confirm === true && recoveryText(body.proposal_id, 256) && body.proposal_id.length > 0 &&
    Array.isArray(body.tasks) && body.tasks.length > 0 && body.tasks.length <= 2 && body.tasks.every((task, index) =>
      recoveryObject(task) && task.ordinal === index + 1 && Array.isArray(task.ownership_hints) && task.ownership_hints.length > 0 && task.ownership_hints.length <= 32 &&
      task.ownership_hints.every((hint) => recoveryObject(hint) && recoveryPath(hint.path) && ["file", "directory"].includes(String(hint.kind))) &&
      Array.isArray(task.validations) && task.validations.length > 0 && task.validations.length <= 16 && task.validations.some((test) => test?.kind === "git_diff_check") &&
      task.validations.every((test) => recoveryObject(test) && recoveryText(test.id, 256) && ["git_diff_check", "go_test", "npm_test"].includes(String(test.kind)) && recoveryPath(test.scope)));
}
function isChildIntent(value: unknown, runID: string, planID: string, ordinal: number): value is ChildIntent | null {
  return value === null || isKey(value) && "version" in value && value.version === 1 && "runID" in value && value.runID === runID &&
    "planID" in value && value.planID === planID && "ordinal" in value && value.ordinal === ordinal && "generation" in value && Number.isSafeInteger(value.generation) &&
    Number(value.generation) >= 1 && Number(value.generation) <= 8 && (!("legacy" in value) || value.legacy === true) && "kind" in value &&
    ["owner", "execute"].includes(String(value.kind)) && "retry" in value && typeof value.retry === "boolean";
}

export function BatchPreparation({ client, runID, usedProposalIDs, onReviewProposals }: {
  client: APIClient; runID: string; usedProposalIDs: string[]; onReviewProposals: () => void;
}) {
  const { t } = useLocale();
  const cache = useQueryClient();
  const journal = useVerificationRecord<PrepareIntent | null>(`batch-workbench-prepare:${JSON.stringify([runID])}`, null,
    (value): value is PrepareIntent | null => isPrepareIntent(value, runID));
  const intent = journal.value;
  const selection = useVerificationRecord(selectionKey(runID), { version: 1, runID, selected: "" },
    (value): value is { version: number; runID: string; selected: string } => recoveryObject(value) && value.version === 1 && value.runID === runID && recoveryText(value.selected, 256));
  const selected = selection.value.selected || intent?.body.proposal_id || "";
  const [localError, setLocalError] = useState("");
  const query = useQuery({ queryKey: ["run", runID, "child-tasks"],
    queryFn: ({ signal }) => client.getRunChildTaskProposals(runID, signal) });
  const legacy = useLegacyBatchRecovery(client, runID, "prepare", journal,
    (request) => query.isSuccess && query.data.items.some((proposal) => proposal.run_id === runID && proposal.id === request.body.proposal_id),
    (value) => {
      if (!recoveryObject(value)) return null;
      const request = { version: 1, runID, key: value.key, body: value.body };
      return isPrepareIntent(request, runID) ? request : null;
    });
  const action = useMutation({ mutationFn: (submission: { request: PrepareIntent; client: APIClient; journal: typeof journal }) =>
    submission.client.prepareBatchWorkbench(submission.request.runID, submission.request.body, submission.request.key),
    onSuccess: (result, submission) => {
      try {
        settleLegacy(submission.request, submission.journal, "prepare");
        submission.journal.store!.remove(draftKey(submission.request.runID, submission.request.body.proposal_id));
        submission.journal.store!.remove(selectionKey(submission.request.runID));
        submission.journal.remove();
      } catch (failure) { setLocalError(failure instanceof Error ? failure.message : String(failure)); }
      cache.setQueryData(["run", submission.request.runID, "batch-workbench", result.snapshot.plan.id], result);
      void cache.invalidateQueries({ queryKey: ["run", submission.request.runID, "batch-deliveries"] });
    },
  });
  const candidates = query.data?.items.filter((proposal) => proposal.surface === "core" &&
    proposal.status === "approved" && proposal.tasks.length <= 2 &&
    proposal.assignments.length === proposal.tasks.length &&
    proposal.assignments.every((assignment) => assignment.status === "admitted") && !usedProposalIDs.includes(proposal.id)) ?? [];
  const proposal = candidates.find((candidate) => candidate.id === selected) ?? candidates[0];
  const dispatch = (request: PrepareIntent) => {
    if (legacy.blocked) return;
    try { journal.save(request); setLocalError(""); action.mutate({ request, client, journal }); }
    catch (failure) { setLocalError(failure instanceof Error ? failure.message : String(failure)); }
  };
  const submit = (body: BatchWorkbenchPrepareRequestView) => {
    if (intent || action.isPending || legacy.blocked) return;
    dispatch({ version: 1, runID, key: `web-batch-prepare-${crypto.randomUUID()}`, body });
  };
  return <section className="batch-workbench-preparation" aria-label={t("准备独立任务", "Prepare isolated tasks")}>
    <div className="section-heading"><h3>{t("准备独立任务", "Prepare isolated tasks")}</h3>
      <button type="button" className="compact-command" onClick={onReviewProposals}>{t("审阅子任务提案", "Review child proposals")}</button></div>
    <p>{t("选择已准入的子任务，填写负责的项目路径。准备完成后，逐项启动、检查交付并合并。",
      "Choose admitted child tasks and assign their project paths. After preparation, start each task, inspect its delivery, and merge.")}</p>
    <p>{t("每次启动处理最多 4 个小型文本文件。为任务选择完整内容可放入一次指令的负责路径。",
      "Each start handles up to 4 small text files. Choose owned paths whose complete contents fit one instruction.")}</p>
    {query.isLoading && <LoadingState label={t("读取子任务", "Reading child tasks")} />}
    {query.isError && <><ErrorState error={query.error} /><button type="button" className="compact-command" onClick={() => void query.refetch()}>{t("重新读取子任务", "Reload child tasks")}</button></>}
    {intent ? <div className="batch-workbench-recovery" role="status">
      <p>{t("原准备请求已保存。选择“核对准备结果”，继续确认这次请求。", "The original preparation request is saved. Choose Check preparation result to confirm this request.")}</p>
      <ul>{intent.body.tasks.map((task) => <li key={task.ordinal}>#{task.ordinal}: {task.ownership_hints.map((hint) => hint.path).join(", ")}</li>)}</ul>
      <button type="button" className="command-button" disabled={action.isPending} onClick={() => dispatch(intent)}>{t("核对准备结果", "Check preparation result")}</button>
      {action.error instanceof APIRequestError && action.error.status >= 400 && action.error.status < 500 &&
        <button type="button" className="compact-command" onClick={() => { try { settleLegacy(intent, journal, "prepare"); journal.remove(); action.reset(); setLocalError(""); } catch (failure) { setLocalError(String(failure)); } }}>{t("调整准备请求", "Revise preparation request")}</button>}
    </div> : legacy.blocked ? <div role="status"><p>{legacy.error || t("旧窗口原请求待保存；先读取当前任务身份，再保存原请求以核对结果。", "The old window's request needs saving. Read the current task identity, then save the original request to check its result.")}</p>
      {legacy.pending && <button type="button" className="compact-command" onClick={legacy.recover}>{t("重新保存原请求", "Retry saving original request")}</button>}
    </div> : proposal ? <>
      <label>{t("子任务提案", "Child task proposal")}<select value={proposal.id} onChange={(event) => selection.edit({ version: 1, runID, selected: event.target.value })}>
        {candidates.map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.tasks.map((task) => task.title).join(" / ")}</option>)}
      </select></label>
      <BatchPreparationForm key={`${runID}:${proposal.id}`} runID={runID} proposal={proposal} hostValidation={client.hasBatchDeliveryHostValidation}
        pending={action.isPending} onPrepare={submit} />
    </> : !query.isLoading && !query.isError && <p>{t("先在“审阅子任务提案”中批准并准入 1–2 个独立任务，再回来准备负责路径。", "Approve and admit 1–2 isolated tasks in Review child proposals, then return to assign their paths.")}</p>}
    {action.isError && <p className="inline-warning" role="alert">{action.error instanceof Error ? action.error.message : t("准备结果读取失败", "Preparation result could not be read")}</p>}
    {(localError || journal.error || selection.error) && <p className="inline-warning" role="status">{localError || journal.error || selection.error}</p>}
  </section>;
}

interface BatchDraft { version: 1; runID: string; proposalID: string; paths: Record<number, string>; kinds: Record<number, "file" | "directory">; goScopes: Record<number, string>; npmScopes: Record<number, string> }
function validBatchDraft(value: unknown, runID: string, proposalID: string): value is BatchDraft {
  if (!recoveryObject(value) || value.version !== 1 || value.runID !== runID || value.proposalID !== proposalID) return false;
  return ["paths", "kinds", "goScopes", "npmScopes"].every((field) => recoveryObject(value[field]) &&
    Object.entries(value[field]).every(([ordinal, entry]) => ["1", "2"].includes(ordinal) &&
      (field === "kinds" ? ["file", "directory"].includes(String(entry)) : recoveryText(entry, field === "paths" ? 131072 : 4096))));
}
function BatchPreparationForm({ runID, proposal, hostValidation, pending, onPrepare }: {
  runID: string; proposal: ChildTaskProposalView; hostValidation: boolean; pending: boolean;
  onPrepare: (body: BatchWorkbenchPrepareRequestView) => void;
}) {
  const { t } = useLocale();
  const draft = useVerificationRecord<BatchDraft>(draftKey(runID, proposal.id), { version: 1, runID, proposalID: proposal.id,
    paths: {}, kinds: {}, goScopes: {}, npmScopes: {} }, (value): value is BatchDraft => validBatchDraft(value, runID, proposal.id));
  const { paths, kinds, goScopes, npmScopes } = draft.value;
  const [review, setReview] = useState({ store: draft.store, key: draft.key, checked: false });
  const confirmed = review.store === draft.store && review.key === draft.key && review.checked;
  const setConfirmed = (checked: boolean) => setReview({ store: draft.store, key: draft.key, checked });
  const change = <K extends "paths" | "kinds" | "goScopes" | "npmScopes",>(field: K, value: BatchDraft[K]) => {
    draft.edit((current) => ({ ...current, [field]: value })); setConfirmed(false);
  };
  const setPaths = (value: BatchDraft["paths"]) => change("paths", value), setKinds = (value: BatchDraft["kinds"]) => change("kinds", value);
  const setGoScopes = (value: BatchDraft["goScopes"]) => change("goScopes", value), setNpmScopes = (value: BatchDraft["npmScopes"]) => change("npmScopes", value);
  const hasPaths = proposal.tasks.every((task) => (paths[task.ordinal] ?? "").trim());
  return <form className="batch-workbench-form" onSubmit={(event) => {
    event.preventDefault();
    if (!confirmed || !hasPaths || pending || draft.error) return;
    onPrepare({ version, proposal_id: proposal.id, confirm: true, tasks: proposal.tasks.map((task) => ({
      ordinal: task.ordinal, ownership_hints: (paths[task.ordinal] ?? "").split(/\r?\n/u).map((path) => path.trim()).filter(Boolean)
        .map((path) => ({ path, kind: kinds[task.ordinal] ?? "file" })),
      validations: [{ id: "diff", kind: "git_diff_check", scope: "." },
        ...(hostValidation && goScopes[task.ordinal]?.trim() ? [{ id: "go", kind: "go_test", scope: goScopes[task.ordinal].trim() }] : []),
        ...(hostValidation && npmScopes[task.ordinal]?.trim() ? [{ id: "npm", kind: "npm_test", scope: npmScopes[task.ordinal].trim() }] : [])],
    })) });
  }}>
    {proposal.tasks.map((task) => <fieldset key={task.ordinal} disabled={pending}>
      <legend>#{task.ordinal} {task.title}</legend><p>{task.goal}</p>
      <p className="projection-placeholder">{t(`${task.turn_limit} 回合，${task.token_limit} Tokens，${Math.ceil(task.timeout_millis / 60_000)} 分钟`,
        `${task.turn_limit} turns, ${task.token_limit} tokens, ${Math.ceil(task.timeout_millis / 60_000)} minutes`)}</p>
      {task.dependency_ordinals.length > 0 && <p>{t(`等待任务 ${task.dependency_ordinals.join(", ")} 验收后开始`, `Starts after task ${task.dependency_ordinals.join(", ")} is accepted`)}</p>}
      <label>{t("负责路径（每行一个）", "Owned paths (one per line)")}<textarea rows={3} required value={paths[task.ordinal] ?? ""}
        placeholder="internal/parser/example.go" onChange={(event) => setPaths({ ...paths, [task.ordinal]: event.target.value })} /></label>
      <label>{t("路径类型", "Path type")}<select value={kinds[task.ordinal] ?? "file"} onChange={(event) => setKinds({ ...kinds, [task.ordinal]: event.target.value as "file" | "directory" })}>
        <option value="file">{t("文件", "File")}</option><option value="directory">{t("目录与其中的文件", "Directory and its files")}</option>
      </select></label>
      <p>{t("交付时检查完整 Git 差异与干净提交。", "Delivery checks the complete Git diff and clean commits.")}</p>
      {hostValidation && <details><summary>{t("添加 Go/npm 验证", "Add Go/npm validation")}</summary>
        <p>{t("这些验证会在本机执行子任务提交的代码。", "These validations run the child’s committed code on this computer.")}</p>
        <label>{t("Go 验证范围", "Go validation scope")}<input value={goScopes[task.ordinal] ?? ""} placeholder="internal/parser" onChange={(event) => setGoScopes({ ...goScopes, [task.ordinal]: event.target.value })} /></label>
        <label>{t("npm 项目路径", "npm project path")}<input value={npmScopes[task.ordinal] ?? ""} placeholder="web" onChange={(event) => setNpmScopes({ ...npmScopes, [task.ordinal]: event.target.value })} /></label>
      </details>}
    </fieldset>)}
    <label className="checkbox-row"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
      {t("已核对任务、负责路径和验证范围，准备独立工作区", "I reviewed the tasks, owned paths, and validations; prepare independent workspaces")}</label>
    {draft.error && <div className="inline-warning" role="status"><p>{draft.error}</p>
      {draft.store && <button type="button" className="compact-command" disabled={pending}
        onClick={() => { try { draft.discard(); setConfirmed(false); } catch { /* The record keeps its visible persistence error. */ } }}>{t("放弃未保存草稿", "Discard unsaved draft")}</button>}
    </div>}
    <button type="submit" className="command-button" disabled={pending || !confirmed || !hasPaths || Boolean(draft.error)}>{t("准备独立任务", "Prepare isolated tasks")}</button>
  </form>;
}

export function BatchWorkbenchControls({ client, runID, planID, ordinal }: {
  client: APIClient; runID: string; planID: string; ordinal: number;
}) {
  const { t } = useLocale();
  const cache = useQueryClient();
  const journal = useVerificationRecord<ChildIntent | null>(`batch-workbench-child:${JSON.stringify([runID, planID, ordinal])}`, null,
    (value): value is ChildIntent | null => isChildIntent(value, runID, planID, ordinal));
  const intent = journal.value;
  const [localError, setLocalError] = useState("");
  const query = useQuery({ queryKey: ["run", runID, "batch-workbench", planID],
    queryFn: ({ signal }) => client.getBatchWorkbench(runID, planID, signal),
    refetchInterval: (state) => state.state.data?.children.some((child) => child.executing) ? 1500 : false });
  const legacy = useLegacyBatchRecovery(client, runID, `${planID}/${ordinal}`, journal,
    query.isSuccess && query.data.snapshot.plan.run_id === runID && query.data.snapshot.plan.id === planID && query.data.children.some((child) => child.ordinal === ordinal),
    (value) => {
      if (!recoveryObject(value)) return null;
      const request = { version: 1, runID, planID, ordinal, key: value.key, kind: value.kind, retry: value.retry, generation: value.generation };
      return isChildIntent(request, runID, planID, ordinal) ? request : null;
    });
  const invalidate = (result: BatchWorkbenchView, sourceRunID: string, sourcePlanID: string) => {
    cache.setQueryData(["run", sourceRunID, "batch-workbench", sourcePlanID], result);
    void cache.invalidateQueries({ queryKey: ["run", sourceRunID, "batch-delivery", sourcePlanID] });
    void cache.invalidateQueries({ queryKey: ["run", sourceRunID, "batch-deliveries"] });
    void cache.invalidateQueries({ queryKey: ["run", sourceRunID, "agent-graph"] });
  };
  const action = useMutation({ mutationFn: ({ request, client: source }: { request: ChildIntent; client: APIClient; journal: typeof journal }) => request.kind === "owner"
    ? source.recoverBatchWorkbenchOwner(request.runID, request.planID, request.ordinal, { version, confirm: true, retry: request.retry, expected_generation: request.generation }, request.key)
    : source.executeBatchWorkbenchChild(request.runID, request.planID, request.ordinal, { version, confirm: true, expected_generation: request.generation }, request.key),
    onSuccess: (result, submission) => {
      const suspended = submission.request.kind === "execute" && result.children.some((child) => child.ordinal === submission.request.ordinal && child.waiting_approval);
      if (!suspended) {
        try { settleLegacy(submission.request, submission.journal, `${submission.request.planID}/${submission.request.ordinal}`); submission.journal.remove(); }
        catch (failure) { setLocalError(String(failure)); }
      }
      invalidate(result, submission.request.runID, submission.request.planID);
    },
    onError: () => { void query.refetch(); },
  });
  if (query.isLoading) return <p role="status">{t("读取任务准备状态…", "Reading task readiness…")}</p>;
  if (query.isError || !query.data) return <div><ErrorState error={query.error} /><button type="button" className="compact-command" onClick={() => void query.refetch()}>{t("重新读取任务状态", "Reload task status")}</button></div>;
  const child = query.data.children.find((item) => item.ordinal === ordinal);
  const delivery = query.data.snapshot.children.find((item) => item.workspace.ordinal === ordinal);
  if (!child || !delivery) return null;
  const status = delivery.workspace.status;
  const active = ["dispatched", "acknowledged", "working", "question"].includes(status);
  const waiting = query.data.snapshot.plan.spec.tasks[ordinal - 1].dependency_ordinals.some((dependency) =>
    !query.data?.snapshot.children.some((item) => item.workspace.ordinal === dependency && ["accepted", "merged"].includes(item.workspace.status)));
  const submit = (kind: ChildIntent["kind"], retry = false) => {
    if (intent || action.isPending || legacy.blocked) return;
    dispatch({ version: 1, runID, planID, ordinal, kind, retry, generation: child.generation, key: `web-batch-${kind}-${crypto.randomUUID()}` });
  };
  const dispatch = (request: ChildIntent) => {
    if (legacy.blocked) return;
    try { journal.save(request); setLocalError(""); action.mutate({ request, client, journal }); }
    catch (failure) { setLocalError(failure instanceof Error ? failure.message : String(failure)); }
  };
  return <div className="batch-workbench-controls">
    {!query.data.worker_available && <p>{t("任务已准备。连接当前运行时的批量执行能力后，可在这里启动子任务。", "Tasks are prepared. Connect the current runtime’s batch execution capability to start a child here.")}</p>}
    {child.executing && <p role="status">{t("子任务正在独立工作区执行。完成后检查交付结果。", "The child is working in its isolated workspace. Inspect the delivery when it completes.")}</p>}
    {active && !child.executing && <p>{t("子任务在独立工作区内使用已委派工具，修改负责路径、提交并执行交付验证。需要批准的操作会暂停等待。", "The child uses its delegated tools in an isolated workspace, edits owned paths, commits and verifies the delivery. Operations needing approval wait for a decision.")}</p>}
    {child.waiting_approval && <p role="status">{t("先处理任务中的工具审批，再继续原子任务；等待期间不会重复调用模型。", "Resolve the task's tool approval, then continue the same child operation. Waiting does not call the model again.")}</p>}
    {child.outcome_unresolved && <p>{t("上次执行结果需要核对。刷新交付记录，或恢复新的代次后继续。", "The previous execution needs confirmation. Refresh its delivery record, or recover a new generation to continue.")}</p>}
    {waiting && <p>{t("先验收依赖任务，再启动这个任务。", "Accept the dependency task before starting this task.")}</p>}
    {intent ? <div role="status"><p>{t("原操作已保存。核对结果会沿用这次请求。", "The original operation is saved. Checking the result keeps this request’s identity.")}</p>
      <button type="button" className="command-button" disabled={action.isPending} onClick={() => dispatch(intent)}>{child.waiting_approval ? t("审批后继续子任务", "Continue child after approval") : t("核对任务操作结果", "Check child operation result")}</button>
      {action.error instanceof APIRequestError && action.error.status >= 400 && action.error.status < 500 &&
        <button type="button" className="compact-command" onClick={() => { try { settleLegacy(intent, journal, `${planID}/${ordinal}`); journal.remove(); action.reset(); setLocalError(""); } catch (failure) { setLocalError(String(failure)); } }}>{t("查看当前任务并继续", "Continue from the current task")}</button>}
    </div> : legacy.blocked ? <div role="status"><p>{legacy.error || t("旧窗口原操作待保存；核对当前任务身份后才能继续。", "The old window's operation needs saving. Confirm the current task identity before continuing.")}</p>
      {legacy.pending && <button type="button" className="compact-command" onClick={legacy.recover}>{t("重新保存原请求", "Retry saving original request")}</button>}
    </div> : <>
      {active && child.owner_available && !child.outcome_unresolved && <button type="button" className="command-button"
        disabled={action.isPending || child.executing || !query.data.worker_available || waiting}
        onClick={() => submit("execute")}>{t("启动子任务并返回交付", "Start child and return delivery")}</button>}
      {active && (!child.owner_available || child.outcome_unresolved) && child.generation < 8 &&
        <button type="button" className="command-button" disabled={action.isPending || child.executing} onClick={() => submit("owner")}>{t("恢复任务所有权", "Recover child ownership")}</button>}
      {status === "changes_requested" && child.generation < 8 && <>
        <p>{t("审阅意见已保存。准备返工后，启动子任务按意见修改并重新交付。", "Review feedback is saved. Prepare rework, then start the child to revise and return a new delivery.")}</p>
        <button type="button" className="command-button" disabled={action.isPending || child.executing} onClick={() => submit("owner", true)}>{t("按审阅意见准备返工", "Prepare rework from review feedback")}</button>
      </>}
    </>}
    {action.isPending && <p role="status">{t("正在处理子任务操作…", "Processing child operation…")}</p>}
    {action.isError && <p className="inline-warning" role="alert">{action.error instanceof Error ? action.error.message : t("操作结果读取失败", "Operation result could not be read")}</p>}
    {(localError || journal.error) && <p className="inline-warning" role="status">{localError || journal.error}</p>}
    <button type="button" className="compact-command" disabled={query.isFetching} onClick={() => void query.refetch()}>{t("刷新任务进度", "Refresh child progress")}</button>
  </div>;
}
