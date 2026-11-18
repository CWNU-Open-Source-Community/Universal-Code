import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronRight, FileCheck2, FileDiff, FileText, History,
  LoaderCircle, PanelRightClose, X } from "lucide-react";
import type { APIClient } from "../api/client";
import type { ApprovalContinuationView, FileEditApplyView, FileEditPreviewView, FileEditQueueView, FileEditReviewRequestView, OperationReceiptView } from "../api/types";
import { formatBytes, formatDate } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge, StatusLabel } from "./common";
import { OperationReceipt } from "./operation-receipt";
import { ApprovalContinuationNotice } from "./approval-continuation-notice";
import { FileProposalRecovery } from "./file-proposal-recovery";
import { parseUnifiedDiff, type UnifiedDiff as ParsedUnifiedDiff } from "./unified-diff";

type RevertAttempt = { runID: string; sourceEditID: string; path: string; operationKey: string;
  state: "pending" | "unknown"; error?: string } | { state: "created"; editID: string };
type RevertAttempts = Record<string, RevertAttempt>;
const revertAttemptsKey = (runID: string) => ["run", runID, "file-revert-attempts"] as const;
type ApplyAttempt = { runID: string; editID: string; path: string; operationKey: string;
  state: "pending" | "unknown"; error?: string };
type ApplyAttempts = Record<string, ApplyAttempt>;
const applyAttemptsKey = (runID: string) => ["run", runID, "file-apply-attempts"] as const;
const inverseGuidance: Record<FileEditPreviewView["status"], [string, string]> = {
  proposed: ["这是待审的撤销差异。核对后批准，再应用到文件。", "This revert diff is awaiting review. Review and approve it, then apply it to the file."],
  approved: ["撤销提案已批准，等待应用后写入文件。", "The revert proposal is approved and awaits application to the file."],
  applied: ["撤销修改已应用。请结合当前文件核对结果。", "The revert changes were applied. Inspect the current file to confirm the result."],
  denied: ["撤销提案已拒绝，可查看这份记录。", "The revert proposal was denied. Its record remains available for review."],
  failed: ["撤销应用失败，文件可能已部分改变。请核对当前文件和操作结果。", "The revert application failed and the file may have partially changed. Inspect the current file and operation result."],
};
const metadataOnlyDiff = (edit: FileEditPreviewView, diff: ParsedUnifiedDiff) =>
  (edit.operation === "delete" || edit.operation === "move") && !diff.lines.some((line) => line.kind === "hunk");
export type FileEditReviewTarget = { runID: string; editID: string; workspaceID: string };
type EditSelection = { runID: string; editID: string; workspaceID?: string };

export function FileEditPanel({ client, runID, runStatus, onRequestChange, onRequestRevert,
  requestRevertUnavailableReason, onChanged, initialTarget }: {
  client: APIClient; runID: string; runStatus?: string;
  onRequestChange?: (edit: FileEditPreviewView) => void;
  onRequestRevert?: (edit: FileEditPreviewView) => void;
  requestRevertUnavailableReason?: string;
  onChanged?: () => void;
  initialTarget?: FileEditReviewTarget;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [receipts, setReceipts] = useState<Record<string, OperationReceiptView>>({});
  const [continuations, setContinuations] = useState<Record<string, ApprovalContinuationView>>({});
  const [recoveryEditID, setRecoveryEditID] = useState("");
  const [editSelection, setEditSelection] = useState<EditSelection>(() => initialTarget ?? { runID, editID: "" });
  const selectedEditID = editSelection.runID === runID ? editSelection.editID : "";
  const expectedWorkspaceID = editSelection.workspaceID;
  const targetRunMismatch = expectedWorkspaceID !== undefined && editSelection.runID !== runID;
  const setSelectedEditID = (editID: string) => setEditSelection({ runID, editID });
  const selection = useRef({ runID, selectedEditID });
  selection.current = { runID, selectedEditID };
  useEffect(() => {
    setEditSelection(initialTarget ?? { runID, editID: "" }); setRecoveryEditID("");
  }, [runID, initialTarget?.runID, initialTarget?.editID, initialTarget?.workspaceID]);
  const panel = useRef<HTMLElement>(null);
  const fileButtons = useRef(new Map<string, HTMLButtonElement>());
  const revertState = useQuery<RevertAttempts>({ queryKey: revertAttemptsKey(runID),
    queryFn: () => ({}), enabled: false, initialData: {}, gcTime: Infinity });
  const attempts = revertState.data ?? {};
  const applyState = useQuery<ApplyAttempts>({ queryKey: applyAttemptsKey(runID),
    queryFn: () => ({}), enabled: false, initialData: {}, gcTime: Infinity });
  const applyAttempts = applyState.data ?? {};
  const applyResult = useQuery<FileEditApplyView | null>({ queryKey: ["run", runID, "file-apply-result"],
    queryFn: () => null, enabled: false, initialData: null });
  const query = useQuery({
    queryKey: ["run", runID, "file-edits"],
    queryFn: ({ signal }) => client.fileEditQueue(runID, signal),
  });
  const changeSetQuery = useQuery({
    queryKey: ["run", runID, "file-edit-change-set"],
    queryFn: ({ signal }) => client.fileEditChangeSet(runID, signal),
  });
  const currentWorkspaceID = changeSetQuery.isSuccess && !changeSetQuery.isFetching
    ? changeSetQuery.data.workspace_id : "";
  const selectedInQueue = query.data?.items.find((edit) => edit.id === selectedEditID);
  const selectedQuery = useQuery({ queryKey: expectedWorkspaceID === undefined
    ? ["run", runID, "file-edit", selectedEditID] : ["run", runID, "file-edit", selectedEditID, "target", expectedWorkspaceID],
    queryFn: async ({ signal }) => {
      const edit = await client.fileEdit(runID, selectedEditID, signal);
      if (edit.id !== selectedEditID || (expectedWorkspaceID !== undefined && edit.workspace_id !== expectedWorkspaceID)) {
        throw new Error(t("文件提案与指定记录或原目录不一致，请刷新任务后重试。", "The file proposal does not match the requested record or directory. Refresh the task and retry."));
      }
      return edit;
    }, retry: false, refetchOnMount: "always",
    enabled: Boolean(selectedEditID && (expectedWorkspaceID !== undefined || !selectedInQueue || selectedInQueue.operation === "delete")) });
  const selectedEdit = !selectedEditID ? undefined : expectedWorkspaceID !== undefined
    ? selectedQuery.isSuccess && !selectedQuery.isFetching ? selectedQuery.data : undefined
    : selectedInQueue?.operation === "delete" ? selectedQuery.data : selectedInQueue ?? selectedQuery.data;
  const refreshEdits = (requestedRunID: string) => {
    void queryClient.invalidateQueries({ queryKey: ["run", requestedRunID, "file-edits"] });
    void queryClient.invalidateQueries({ queryKey: ["run", requestedRunID, "file-edit-change-set"] });
    void queryClient.invalidateQueries({ queryKey: ["run", requestedRunID, "file-edit"] });
    void queryClient.invalidateQueries({ queryKey: ["run", requestedRunID, "events"] });
  };
  const revert = useMutation({
    mutationFn: (attempt: Extract<RevertAttempt, { operationKey: string }>) =>
      client.createFileEditRevertProposal(attempt.runID, attempt.sourceEditID, attempt.operationKey),
    onSuccess: (result, attempt) => {
      queryClient.setQueryData<RevertAttempts>(revertAttemptsKey(attempt.runID), (current = {}) => {
        const existing = current[attempt.sourceEditID];
        if (!existing || existing.state === "created" || existing.operationKey !== attempt.operationKey) return current;
        return { ...current, [attempt.sourceEditID]: { state: "created", editID: result.edit.id } };
      });
      queryClient.setQueryData(["run", attempt.runID, "file-edit", result.edit.id], result.edit);
      queryClient.setQueryData<FileEditQueueView>(["run", attempt.runID, "file-edits"], (current) => current && ({
        ...current, items: [result.edit, ...current.items.filter((edit) => edit.id !== result.edit.id)] }));
      if (selection.current.runID === attempt.runID && selection.current.selectedEditID === attempt.sourceEditID) {
        setSelectedEditID(result.edit.id);
        onChanged?.();
      }
    },
    onError: (error, attempt) => {
      queryClient.setQueryData<RevertAttempts>(revertAttemptsKey(attempt.runID), (current = {}) => {
        const existing = current[attempt.sourceEditID];
        if (!existing || existing.state === "created" || existing.operationKey !== attempt.operationKey) return current;
        return { ...current, [attempt.sourceEditID]: { ...attempt, state: "unknown",
          error: error instanceof Error ? error.message : t("无法确认提案结果", "Proposal result could not be confirmed") } };
      });
    },
    onSettled: (_result, _error, attempt) => refreshEdits(attempt.runID),
  });
  const submitRevert = (edit: FileEditPreviewView) => {
    const stored = queryClient.getQueryData<RevertAttempts>(revertAttemptsKey(runID))?.[edit.id];
    if (stored?.state === "created") {
      const known = query.data?.items.find((item) => item.id === stored.editID) ??
        queryClient.getQueryData<FileEditPreviewView>(["run", runID, "file-edit", stored.editID]);
      if (known?.status !== "denied") { setSelectedEditID(stored.editID); return; }
    } else if (stored) return;
    if (!client.hasFileEditReview || edit.status !== "applied" ||
      edit.operation === "move" || edit.secrets_redacted) return;
    if (onRequestRevert && (runStatus !== "running" || !currentWorkspaceID || edit.workspace_id !== currentWorkspaceID)) {
      if (!requestRevertUnavailableReason) onRequestRevert(edit);
      return;
    }
    if (!currentWorkspaceID || edit.workspace_id !== currentWorkspaceID || runStatus !== "running") return;
    const attempt = { runID, sourceEditID: edit.id, path: edit.path,
      operationKey: `web-file-revert-${globalThis.crypto.randomUUID()}`, state: "pending" as const };
    queryClient.setQueryData<RevertAttempts>(revertAttemptsKey(runID), (current = {}) => ({ ...current, [edit.id]: attempt }));
    revert.mutate(attempt);
  };
  const confirmRevert = (attempt: Extract<RevertAttempt, { operationKey: string }>) => {
    const current = queryClient.getQueryData<RevertAttempts>(revertAttemptsKey(attempt.runID))?.[attempt.sourceEditID];
    if (!client.hasFileEditReview || current?.state !== "unknown" || current.operationKey !== attempt.operationKey) return;
    queryClient.setQueryData<RevertAttempts>(revertAttemptsKey(attempt.runID), (items = {}) => ({ ...items,
      [attempt.sourceEditID]: { ...attempt, state: "pending" } }));
    revert.mutate(attempt);
  };
  const review = useMutation({
    mutationFn: ({ requestedRunID, editID, action }: { requestedRunID: string; editID: string;
      action: FileEditReviewRequestView["action"] }) =>
      client.reviewFileEdit(requestedRunID, editID, { version: "file_edit_review.v1", action }),
    onSuccess: (result, { requestedRunID }) => {
      queryClient.setQueryData(["run", requestedRunID, "file-edit", result.edit.id], result.edit);
      if (result.continuation) setContinuations((current) => ({ ...current,
        [`${requestedRunID}:${result.edit.id}`]: result.continuation! }));
      if (selection.current.runID === requestedRunID) onChanged?.();
    },
    onSettled: (_result, _error, { requestedRunID }) => refreshEdits(requestedRunID),
  });
  const apply = useMutation({
    mutationFn: (attempt: ApplyAttempt) => client.applyFileEdit(attempt.runID, attempt.editID,
      { version: "file_edit_apply.v1" }, attempt.operationKey),
    onSuccess: (result, attempt) => {
      queryClient.setQueryData<ApplyAttempts>(applyAttemptsKey(attempt.runID), (current = {}) => {
        if (current[attempt.editID]?.operationKey !== attempt.operationKey) return current;
        const remaining = { ...current }; delete remaining[attempt.editID]; return remaining;
      });
      queryClient.setQueryData(["run", attempt.runID, "file-edit", result.edit.id], result.edit);
      setReceipts((current) => ({ ...current, [`${attempt.runID}:${result.edit.id}`]: result.receipt }));
      queryClient.setQueryData<FileEditApplyView>(["run", attempt.runID, "file-apply-result"], result);
      if (selection.current.runID === attempt.runID) onChanged?.();
    },
    onError: (error, attempt) => queryClient.setQueryData<ApplyAttempts>(applyAttemptsKey(attempt.runID), (current = {}) =>
      current[attempt.editID]?.operationKey === attempt.operationKey ? { ...current, [attempt.editID]: {
        ...attempt, state: "unknown", error: error instanceof Error ? error.message : t("无法确认应用结果", "Apply result could not be confirmed") } } : current),
    onSettled: (_result, _error, attempt) => refreshEdits(attempt.runID),
  });
  const submitApply = (edit: FileEditPreviewView) => {
    if (!currentWorkspaceID || edit.workspace_id !== currentWorkspaceID ||
      !client.hasFileEditApply || !query.data?.apply_enabled || !edit.apply_enabled ||
      queryClient.getQueryData<ApplyAttempts>(applyAttemptsKey(runID))?.[edit.id]) return;
    const attempt = { runID, editID: edit.id, path: edit.path,
      operationKey: `web-file-apply-${globalThis.crypto.randomUUID()}`, state: "pending" as const };
    queryClient.setQueryData<ApplyAttempts>(applyAttemptsKey(runID), (current = {}) => ({ ...current, [edit.id]: attempt }));
    apply.mutate(attempt);
  };
  const confirmApply = (attempt: ApplyAttempt) => {
    const current = queryClient.getQueryData<ApplyAttempts>(applyAttemptsKey(attempt.runID))?.[attempt.editID];
    if (!client.hasFileEditApply || current?.state !== "unknown" || current.operationKey !== attempt.operationKey) return;
    queryClient.setQueryData<ApplyAttempts>(applyAttemptsKey(attempt.runID), (items = {}) => ({ ...items,
      [attempt.editID]: { ...attempt, state: "pending" } }));
    apply.mutate(attempt);
  };
  const pendingOperations = <>
    {applyResult.data && <p role={applyResult.data.status === "failed" ? "alert" : "status"}>
      {applyResult.data.edit.path} · {applyResult.data.status === "failed"
        ? t("本次应用失败，文件可能已部分改变。请核对当前文件和操作结果后再处理。", "The apply attempt failed and the file may have partially changed. Inspect the current file and operation result before proceeding.")
        : t("文件应用已确认完成。", "File application is confirmed complete.")}</p>}
    {Object.entries(attempts).map(([sourceID, attempt]) => attempt.state !== "created" && <div
      className="inline-warning" role={attempt.state === "unknown" ? "alert" : "status"} key={sourceID}>
      <p>{attempt.path} · {attempt.state === "pending" ? t("正在生成撤销提案…", "Creating revert proposal…") :
        t("尚未确认撤销提案结果，请使用原请求确认，避免重复生成。", "The revert proposal result is unconfirmed. Confirm the original request to avoid duplicates.")}</p>
      {attempt.error && <p>{attempt.error}</p>}
      {attempt.state === "unknown" && <button disabled={!client.hasFileEditReview}
        onClick={() => confirmRevert(attempt)} type="button">{t("确认上次撤销提案", "Confirm previous revert proposal")}</button>}
    </div>)}
    {Object.values(applyAttempts).map((attempt) => <div className="inline-warning"
      role={attempt.state === "unknown" ? "alert" : "status"} key={attempt.editID}>
      <p>{attempt.path} · {attempt.state === "pending" ? t("正在应用修改…", "Applying changes…") :
        t("尚未确认文件应用结果，请按原请求确认。当前文件可能已经改变。", "The apply result is unconfirmed. Confirm the original request; the file may already have changed.")}</p>
      {attempt.error && <p>{attempt.error}</p>}
      {attempt.state === "unknown" && <button disabled={!client.hasFileEditApply}
        onClick={() => confirmApply(attempt)} type="button">{t("确认上次文件应用", "Confirm previous file apply")}</button>}
    </div>)}
  </>;
  if (query.isLoading) {
    return <>{pendingOperations}<LoadingState label={t("正在加载文件编辑预览", "Loading file edit previews")} /></>;
  }
  if (query.isError || !query.data) return <div>{pendingOperations}<ErrorState error={query.error} />
    <button onClick={() => void query.refetch()} type="button">{t("重试文件变更", "Retry file changes")}</button></div>;
  if (changeSetQuery.isSuccess && query.data.items.length === 0 && !selectedEditID && !targetRunMismatch && !Object.keys(attempts).length && !Object.keys(applyAttempts).length) return <EmptyState>{t("还没有文件修改。在对话中描述要改的内容，生成提案后可在这里审阅。", "No file changes yet. Describe the change in the conversation, then review its proposal here.")}</EmptyState>;
  const operationError = review.error;
  const changeSet = changeSetQuery.data;
  const partial = changeSet && changeSet.applied_count > 0 &&
    changeSet.applied_count < changeSet.returned_count;
  const parsedDiffs = new Map(query.data.items.map((edit) => [edit.id,
    parseUnifiedDiff(selectedEdit?.id === edit.id ? selectedEdit.diff : edit.diff)]));
  const countsIncomplete = query.data.truncated || new Set(query.data.items.map((edit) => edit.workspace_id)).size > 1 || query.data.items.some((edit) => edit.secrets_redacted ||
    metadataOnlyDiff(edit, parsedDiffs.get(edit.id)!));
  const additions = [...parsedDiffs.values()].reduce((total, diff) => total + diff.additions, 0);
  const deletions = [...parsedDiffs.values()].reduce((total, diff) => total + diff.deletions, 0);
  const selectedAttempt = selectedEdit ? attempts[selectedEdit.id] : undefined;
  const knownInverse = selectedAttempt?.state === "created" ? query.data.items.find((edit) => edit.id === selectedAttempt.editID) ??
    queryClient.getQueryData<FileEditPreviewView>(["run", runID, "file-edit", selectedAttempt.editID]) : undefined;
  const viewExisting = selectedAttempt?.state === "created" && knownInverse?.status !== "denied";
  return <section className="file-edit-panel" aria-label={t("文件编辑预览", "File edit previews")} ref={panel} tabIndex={-1}>
    <header className="projection-heading">
      <div><FileDiff aria-hidden="true" size={17} /><h2>{t("差异审阅", "Diff review")}</h2></div>
      <span>{query.data.items.length}{query.data.truncated ? "+" : ""} {t("项编辑", "edits")}
        {!countsIncomplete && <><b className="diff-additions">+{additions}</b><b className="diff-deletions">-{deletions}</b></>}</span>
    </header>
    {changeSetQuery.isLoading && <p role="status">{t("正在核对当前执行目录…", "Checking the current execution directory…")}</p>}
    {changeSetQuery.isError && <div role="alert"><p>{t("当前执行目录无法确认，已有编辑仍可按原目录审阅；暂不提供新的应用或撤销操作。",
      "The current execution directory could not be confirmed. Existing edits remain reviewable in their original scope; new apply and revert actions are unavailable.")}</p>
      <button onClick={() => void changeSetQuery.refetch()} type="button">{t("重试变更汇总", "Retry change summary")}</button></div>}
    {changeSet && !changeSetQuery.isError && <><p>{t("变更范围：当前执行目录。", "Change scope: the current execution directory.")}</p>
    <details><summary>{t("查看当前执行目录身份", "View current execution directory identity")}</summary><code>{changeSet.workspace_id}</code></details>
    <div aria-label={t("多文件变更集", "Multi-file change set")} className="file-change-set-summary">
      <span>{t("待审", "Pending")} <strong>{changeSet.proposed_count}</strong></span>
      <span>{t("已批准", "Approved")} <strong>{changeSet.approved_count}</strong></span>
      <span>{t("已应用", "Applied")} <strong>{changeSet.applied_count}</strong></span>
      {(changeSet.denied_count > 0 || changeSet.failed_count > 0) &&
        <span>{t("拒绝 / 失败", "Denied / failed")} <strong>{changeSet.denied_count} / {changeSet.failed_count}</strong></span>}
      <div className="file-change-set-policy">
        {partial && <StatusBadge status="partial" />}
        <span>{formatBytes(changeSet.total_diff_bytes)} / {t("逐文件授权", "per-file authority")}</span>
      </div>
    </div></>}
    {pendingOperations}
    {Object.entries(continuations).filter(([key]) => key.startsWith(`${runID}:`)).map(([key, continuation]) => {
      const edit = query.data.items.find((item) => key === `${runID}:${item.id}`);
      return <div key={key}>{edit && <p><code>{edit.path}</code></p>}
        <ApprovalContinuationNotice continuation={continuation} /></div>;
    })}
    {targetRunMismatch && <p role="alert">{t("此提案属于另一次执行，请返回对应执行记录打开。", "This proposal belongs to another execution. Open it from that execution's record.")}</p>}
    {selectedEditID && !selectedEdit && selectedQuery.isFetching && <LoadingState label={t("正在读取指定文件提案…", "Reading the requested file proposal…")} />}
    {selectedEditID && !selectedEdit && selectedQuery.isError && <div role="alert"><p>{t("指定文件提案无法确认，尚未打开：", "The requested file proposal could not be confirmed and has not been opened: ")}{selectedEditID}</p><ErrorState error={selectedQuery.error} />
      <button onClick={() => void selectedQuery.refetch()} type="button">{t("重试读取提案", "Retry proposal")}</button></div>}
    <div className={`file-review-workspace${selectedEdit ? " is-reviewing" : ""}`}>
      <div className="file-edit-list" aria-label={t("已更改文件", "Changed files")}>
      {query.data.items.map((edit) => {
        const diff = parsedDiffs.get(edit.id) ?? parseUnifiedDiff("");
        return <button aria-pressed={edit.id === selectedEditID} className="file-edit-row"
          key={edit.id} onClick={() => setSelectedEditID(edit.id)} ref={(element) => {
            if (element) fileButtons.current.set(edit.id, element); else fileButtons.current.delete(edit.id);
          }} type="button">
          <FileText aria-hidden="true" size={15} />
          <code>{edit.operation === "move"
            ? `${edit.path} → ${edit.destination_path}` : edit.path}</code>
          <span className="file-edit-row-meta">
            <span><StatusLabel status={edit.operation} /> · {currentWorkspaceID ? edit.workspace_id === currentWorkspaceID
              ? t("当前执行目录", "Current execution directory") : t("历史原目录", "Historical directory")
              : t("原目录记录", "Original directory record")}</span>
            <span className="file-edit-counts">{metadataOnlyDiff(edit, diff) ? t("仅元数据", "Metadata only") :
              <><b className="diff-additions">+{diff.additions}</b><b className="diff-deletions">-{diff.deletions}</b></>}</span>
            {edit.secrets_redacted && <span>{t("已脱敏", "redacted")}</span>}
            <StatusBadge status={edit.status} />
          </span>
          <ChevronRight aria-hidden="true" size={15} />
        </button>;
      })}
      </div>
      {selectedEdit && <FileReviewDrawer applyEnabled={query.data.apply_enabled}
        currentTarget={Boolean(currentWorkspaceID) && selectedEdit.workspace_id === currentWorkspaceID}
        client={client} diff={selectedEdit === selectedInQueue ? parsedDiffs.get(selectedEdit.id) ?? parseUnifiedDiff(selectedEdit.diff)
          : parseUnifiedDiff(selectedEdit.diff)}
        edit={selectedEdit} onApply={() => submitApply(selectedEdit)}
        runStatus={runStatus} onRevert={() => submitRevert(selectedEdit)}
        requestRevertInConversation={Boolean(onRequestRevert)} requestRevertUnavailableReason={requestRevertUnavailableReason}
        reverting={selectedAttempt?.state === "pending" || selectedAttempt?.state === "unknown" || Boolean(applyAttempts[selectedEdit.id])}
        viewExistingRevert={viewExisting} inverse={Object.values(attempts).some((attempt) =>
          attempt.state === "created" && attempt.editID === selectedEdit.id)}
        onRequestChange={onRequestChange ? () => onRequestChange(selectedEdit) : undefined}
        onClose={() => {
          setSelectedEditID("");
          requestAnimationFrame(() => (fileButtons.current.get(selectedEdit.id) ?? panel.current)?.focus());
        }} onRecover={() => setRecoveryEditID(selectedEdit.id)}
        onReview={(action) => review.mutate({ requestedRunID: runID, editID: selectedEdit.id, action })}
        receipt={receipts[`${runID}:${selectedEdit.id}`]} reviewing={review.isPending && review.variables?.editID === selectedEdit.id}
        reviewAction={review.variables?.action} applying={Boolean(applyAttempts[selectedEdit.id])} />}
    </div>
    {recoveryEditID && <FileProposalRecovery client={client} editID={recoveryEditID}
      onClose={() => setRecoveryEditID("")} runID={runID} />}
    {review.isError && review.variables.requestedRunID === runID && <div className="inline-warning" role="alert">
      {operationError instanceof Error ? operationError.message : t("文件编辑操作失败", "File edit operation failed")}
    </div>}
    {query.data.truncated && <p role="status">{t("当前显示部分编辑记录。核对完整变更范围时，请结合项目差异。",
      "A portion of edit history is displayed. Use the project diff to review the full change scope.")}</p>}
  </section>;
}

function FileReviewDrawer({ applyEnabled, applying, client, diff, edit, onApply, onClose,
  onRecover, onReview, receipt, reviewing, reviewAction, onRequestChange, runStatus,
  onRevert, reverting, viewExistingRevert, inverse, currentTarget,
  requestRevertInConversation, requestRevertUnavailableReason }: {
    applyEnabled: boolean;
    applying: boolean;
    client: APIClient;
    diff: ParsedUnifiedDiff;
    edit: FileEditPreviewView;
    onApply: () => void;
    onClose: () => void;
    onRecover: () => void;
    onRevert: () => void;
    runStatus?: string;
    reverting: boolean;
    viewExistingRevert: boolean;
    inverse: boolean;
    currentTarget: boolean;
    requestRevertInConversation: boolean;
    requestRevertUnavailableReason?: string;
    onRequestChange?: () => void;
    onReview: (action: FileEditReviewRequestView["action"]) => void;
    receipt?: OperationReceiptView;
    reviewing: boolean;
    reviewAction?: FileEditReviewRequestView["action"];
  }) {
  const { t } = useLocale();
  const conversationRevert = requestRevertInConversation && (runStatus !== "running" || !currentTarget);
  const close = useRef<HTMLButtonElement>(null);
  useEffect(() => { close.current?.focus(); }, [edit.id]);
  return <aside aria-label={t(`审阅 ${edit.path}`, `Review ${edit.path}`)} className="file-review-drawer">
    <header>
      <div><code>{edit.operation === "move"
        ? `${edit.path} → ${edit.destination_path}` : edit.path}</code>
        <StatusBadge status={edit.operation} /><StatusBadge status={edit.status} /></div>
      <button aria-label={t("关闭审阅", "Close review")} className="icon-button" onClick={onClose} ref={close}
        title={t("关闭审阅", "Close review")} type="button">
        <PanelRightClose aria-hidden="true" size={16} />
      </button>
    </header>
    <p>{currentTarget ? t("此编辑属于当前执行目录。", "This edit belongs to the current execution directory.")
      : t("这是原目录中的历史编辑，可查看当时的差异。", "This is a historical edit in its original directory. Review its recorded diff here.")}</p>
    <details><summary>{t("查看此编辑的原目录身份", "View this edit's original directory identity")}</summary><code>{edit.workspace_id}</code></details>
    <div className="file-review-meta">
      <span>{metadataOnlyDiff(edit, diff) ? t("未提供文本行数", "Text line counts unavailable") :
        <><b className="diff-additions">+{diff.additions}</b> <b className="diff-deletions">-{diff.deletions}</b></>}</span>
      <span>{edit.secrets_redacted ? t("敏感内容已脱敏", "Sensitive content redacted") : t("未触发脱敏", "No redaction triggered")}</span>
      <time dateTime={edit.updated_at}>{formatDate(edit.updated_at)}</time>
    </div>
    <UnifiedDiffView diff={diff} />
    {edit.reason && <p role={edit.status === "failed" ? "alert" : "status"}>{edit.reason}</p>}
    {inverse && <p role={edit.status === "failed" ? "alert" : "status"}>{t(...inverseGuidance[edit.status])}</p>}
    {edit.status === "applied" && <p>{edit.operation === "move" || edit.secrets_redacted
      ? t("此记录涉及移动或脱敏内容，暂不支持生成单文件撤销提案。", "Revert proposals are unavailable for moves or redacted content.")
      : viewExistingRevert ? t("撤销提案已生成，打开后可核对差异并完成审批。", "A revert proposal is ready. Open it to review the diff and approval steps.")
      : conversationRevert ? requestRevertUnavailableReason ?? t(
        "将此编辑的撤销要求加入对话草稿，发送后生成待审提案。应用前会核对文件版本；有后续变化时请先处理冲突。",
        "Add this edit's revert request to the conversation draft and send it to create a reviewable proposal. The file revision is checked before applying; resolve conflicts if later changes exist.")
      : !currentTarget ? t("撤销需使用已确认的当前执行目录。请返回此编辑对应的执行记录核对目录。", "Reverting requires a confirmed current execution directory. Return to this edit's execution record to check the directory.")
      : !client.hasFileEditReview ? t("当前可审阅已有变化。连接文件编辑控制权限后可生成撤销提案。", "Existing changes are available for review. Connect file edit control authority to create a revert proposal.")
      : runStatus !== "running" ? t("执行记录状态为“未结束”时才能生成撤销提案；已结束的记录仍可审阅。", "The execution record must be ‘Not ended’ to create a revert proposal; finished records remain available for review.")
        : t("生成此文件的待审撤销提案，批准并应用后写入。服务端核对完整原文与当前版本；文件有后续变化时，需先处理冲突。",
          "Create a revert proposal for this file, then approve and apply it to write changes. The service checks the full original content and current revision; resolve conflicts if the file changed later.")}</p>}
    {receipt && <OperationReceipt receipt={receipt} />}
    <footer>
      <span>{!currentTarget ? t("保留原目录的历史记录", "Historical record in its original directory") : onRequestChange ? edit.apply_enabled ? t("可应用到文件", "Ready to apply")
        : edit.status === "applied" ? t("写入已记录；后续检查须使用同一执行目录", "Write recorded; subsequent checks must use the same execution directory")
          : edit.status === "proposed" ? t("批准并直接应用", "Approve to apply") : t("尚不能应用", "Not ready to apply")
        : <>{t("应用权限", "Apply authority")}: {edit.apply_enabled ? t("就绪", "ready") : t("禁用", "disabled")}</>}</span>
      <div>
        {edit.status === "applied" && <button className="compact-command" disabled={!client.hasFileEditReview || reverting ||
          (!viewExistingRevert && (edit.operation === "move" || edit.secrets_redacted ||
            (conversationRevert ? Boolean(requestRevertUnavailableReason) : !currentTarget || runStatus !== "running")))}
          onClick={onRevert} type="button">{viewExistingRevert ? t("查看撤销提案", "View revert proposal") :
            conversationRevert ? t("在对话中撤销此编辑", "Revert this edit in conversation") :
            t("预览撤销此编辑", "Preview revert of this edit")}</button>}
        {onRequestChange && <button className="compact-command" onClick={onRequestChange} type="button">
          {t("引用此差异请求修改", "Request changes to this diff")}</button>}
        {currentTarget && !onRequestChange && client.hasFileEditProposals && edit.status === "proposed" &&
          edit.proposed_hash !== "missing" &&
          <button aria-label={t(`恢复 ${edit.path}`, `Recover ${edit.path}`)} className="icon-button"
            disabled={reviewing || applying} onClick={onRecover}
            title={t("恢复持久化待审提案", "Recover durable pending proposal")} type="button">
            <History aria-hidden="true" size={15} />
          </button>}
        {currentTarget && client.hasFileEditApply && applyEnabled && edit.apply_enabled &&
          <button aria-label={t(`应用 ${edit.path}`, `Apply ${edit.path}`)} className="icon-button"
            disabled={applying || reviewing} onClick={onApply}
            title={t("应用已批准的文件编辑", "Apply approved file edit")} type="button">
            {applying ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
              : <FileCheck2 aria-hidden="true" size={15} />}
            {onRequestChange && t("应用修改", "Apply changes")}
          </button>}
        {currentTarget && client.hasFileEditReview && edit.allowed_actions.includes("approve_intent") &&
          <button aria-label={t(`批准并应用 ${edit.path}`, `Approve and apply ${edit.path}`)} className="icon-button"
            disabled={reviewing} onClick={() => onReview("approve_intent")}
            title={t("批准并应用文件编辑", "Approve and apply file edit")} type="button">
            {reviewing && reviewAction === "approve_intent"
              ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
              : <Check aria-hidden="true" size={15} />}
            {onRequestChange && t("批准并应用", "Approve & apply")}
          </button>}
        {currentTarget && client.hasFileEditReview && edit.allowed_actions.includes("deny") &&
          <button aria-label={t(`拒绝 ${edit.path}`, `Deny ${edit.path}`)} className="icon-button"
            disabled={reviewing} onClick={() => onReview("deny")}
            title={t("拒绝文件编辑", "Deny file edit")} type="button">
            {reviewing && reviewAction === "deny"
              ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
              : <X aria-hidden="true" size={15} />}
            {onRequestChange && t("拒绝编辑", "Deny edit")}
          </button>}
      </div>
    </footer>
  </aside>;
}

function UnifiedDiffView({ diff }: { diff: ParsedUnifiedDiff }) {
  return <div className="unified-diff" role="table">
    {diff.lines.map((line, index) => <div className={`unified-diff-line is-${line.kind}`}
      key={`${index}-${line.kind}`} role="row">
      <span aria-hidden="true" className="unified-diff-number">{line.oldLine ?? ""}</span>
      <span aria-hidden="true" className="unified-diff-number">{line.newLine ?? ""}</span>
      <span aria-hidden="true" className="unified-diff-marker">{line.marker}</span>
      <code>{line.text || " "}</code>
    </div>)}
  </div>;
}
