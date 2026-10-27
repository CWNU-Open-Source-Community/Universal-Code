import { useLocale, type LocaleText } from "../../lib/locale";
import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { executeThreadGit, observeThreadGit, previewThreadGit, readThreadGit, type ThreadGitPreview, type ThreadGitResult, type ThreadGitSpec } from "../../api/task-delivery";
import { ErrorState, LoadingState, StatusLabel } from "../../components/common";
import { useV2PersistentState, useV2RecoveryStore } from "../recovery-storage";
import type { ReviewCommentResult } from "../review-comment-draft";
import { ReviewDiff } from "./review-diff";
import { taskReviewKey } from "./task-overview";
import { gitFormRepositoryID, gitFormRevision, useGitFormState } from "./git-form-state";
import "./task-delivery.css";
import "./task-git.css";

const operationLabels = (t: LocaleText) => ({ commit: t("提交所选文件", "Commit selected files"), stage: t("暂存所选文件", "Stage selected files"), unstage: t("取消所选文件暂存", "Unstage selected files"), create_branch: t("创建并切换分支", "Create and switch branch"), switch_branch: t("切换已有分支", "Switch to existing branch"), worktree_create: t("创建独立工作目录", "Create worktree"), push_branch: t("推送当前分支", "Push current branch") });
type Operation = keyof ReturnType<typeof operationLabels>;
const directoryName = (path: string) => path.replace(/[\\/]+$/u, "").split(/[\\/]/u).pop() || path;
interface GitAttempt { threadID: string; runID: string; key: string; operation: Operation }
const validAttempt = (value: unknown, threadID: string): value is GitAttempt => Boolean(value && typeof value === "object" &&
  "threadID" in value && value.threadID === threadID && "runID" in value && typeof value.runID === "string" &&
  "key" in value && typeof value.key === "string" && /^task-git-[\w-]+$/u.test(value.key) &&
  "operation" in value && typeof value.operation === "string" && Object.hasOwn(operationLabels((chinese) => chinese), value.operation));
export const taskGitKey = (threadID: string) => ["thread", threadID, "git"] as const;

export function TaskGit({ client, threadID, working, onFeedback, onAddComment, onPullRequest, onOpenWorktree }: {
  client: APIClient; threadID: string; working: boolean; onFeedback: (context: string) => void; onPullRequest: () => void;
  onAddComment?: (context: string) => ReviewCommentResult;
  onOpenWorktree?: (workspace: WorkspaceView) => void;
}) {
  const { t } = useLocale();
  const operations = operationLabels(t);
  const queryClient = useQueryClient();
  const store = useV2RecoveryStore();
  const storageKey = `thread:${threadID}:git-attempt`;
  const [saved, setSaved] = useV2PersistentState<GitAttempt | null>(storageKey, null);
  const [legacyMessage, saveMessage] = useV2PersistentState(`thread:${threadID}:git-message`, "");
  const [legacyBranch, saveBranch] = useV2PersistentState(`thread:${threadID}:git-branch`, "");
  const [legacyWorktreeName, saveWorktreeName] = useV2PersistentState(`thread:${threadID}:git-worktree-name`, "");
  const [preview, setPreview] = useState<ThreadGitPreview | null>(null);
  const [result, setResult] = useState<ThreadGitResult | null>(null);
  const [localError, setLocalError] = useState("");
  const [checking, setChecking] = useState(false);
  const validSaved = saved !== null && validAttempt(saved, threadID) ? saved : null;
  const damaged = saved !== null && !validSaved;
  const state = useQuery({ queryKey: taskGitKey(threadID), queryFn: ({ signal }) => readThreadGit(client, threadID, signal), refetchOnMount: "always" });
  const form = useGitFormState(client.baseURL, threadID, state.data,
    { message: legacyMessage, branch: legacyBranch, worktreeName: legacyWorktreeName });
  const { operation, paths, message, branch, worktreeName, remote, credentialName } = form.value;
  const setOperation = (value: Operation) => form.update({ operation: value });
  const setPaths = (change: string[] | ((previous: string[]) => string[])) => form.update((previous) => ({ ...previous,
    paths: typeof change === "function" ? change(previous.paths) : change }));
  const setMessage = (value: string) => { form.update({ message: value }); saveMessage(value); };
  const setBranch = (value: string) => { form.update({ branch: value }); saveBranch(value); };
  const setWorktreeName = (value: string) => { form.update({ worktreeName: value }); saveWorktreeName(value); };
  const setRemote = (value: string) => form.update({ remote: value });
  const setCredentialName = (value: string) => form.update({ credentialName: value });
  const generation = useRef(0);
  const mounted = useRef(true);
  const preflight = useRef<AbortController | null>(null);
  const live = useRef({ threadID, repositoryID: form.repositoryID, form: JSON.stringify(form.value), working });
  live.current = { threadID, repositoryID: form.repositoryID, form: JSON.stringify(form.value), working };
  const previousRepository = useRef(form.repositoryID);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; preflight.current?.abort(); };
  }, []);
  useEffect(() => {
    if (previousRepository.current && form.repositoryID && previousRepository.current !== form.repositoryID) {
      generation.current++; preflight.current?.abort(); setPreview(null); setResult(null);
      setLocalError(t("当前任务的实际操作目录已变化。原目录的表单已保留，请在当前目录重新选择并预览。", "This task's working directory changed. The previous form is preserved. Select and preview actions in the current directory."));
    }
    previousRepository.current = form.repositoryID;
  }, [form.repositoryID]);
  const credentials = useQuery({ queryKey: ["github-review", "connections"], queryFn: ({ signal }) => client.githubReviewConnections(false, signal), enabled: client.hasGitHubReviewControl && operation === "push_branch" });
  const openWorktree = useMutation({ mutationFn: (path: string) => client.importWorkspace(path),
    onSuccess: (value) => onOpenWorktree?.(value.workspace) });
  const observe = useQuery({ queryKey: ["thread", threadID, "git-request", validSaved?.key],
    queryFn: ({ signal }) => observeThreadGit(client, threadID, validSaved!.key, signal), enabled: Boolean(validSaved), retry: false, refetchOnMount: "always" });
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: taskGitKey(threadID) });
    void queryClient.invalidateQueries({ queryKey: taskReviewKey(threadID) });
  };
  const prepare = useMutation({ mutationFn: async (spec: ThreadGitSpec) => {
    const guard = beginCheck();
    const fresh = await readThreadGit(client, threadID, guard.signal);
    ensureCurrent(guard);
    queryClient.setQueryData(taskGitKey(threadID), fresh);
    if (gitFormRevision(fresh) !== gitFormRevision(state.data!)) {
      throw new Error(t("仓库内容、分支或目录已变化。已保留原表单，请核对当前状态后重新预览。", "Repository content, branch or directory changed. The form is preserved. Check current status and preview again."));
    }
    if (!fresh.can_execute) throw new Error(fresh.blocked_reason || t("当前仓库不能执行 Git 操作。", "Git operations are unavailable for this repository."));
    if (spec.paths?.some((path) => !fresh.changes.some((file) => file.path === path))) throw new Error(t("部分所选文件已不在当前列表，请核对后重新预览。", "Some selected files are no longer listed. Review and preview again."));
    const value = await previewThreadGit(client, threadID, fresh.run_id, spec);
    ensureCurrent(guard);
    if (gitFormRevision(value) !== gitFormRevision(fresh)) throw new Error(t("生成预览期间仓库已变化，请刷新并重新预览。", "The repository changed during preview generation. Refresh and preview again."));
    form.update({ basis: gitFormRevision(fresh), previewed: true });
    setPreview(value);
    return value;
  } });
  const execute = useMutation({
    mutationFn: ({ reviewed, attempt }: { reviewed: ThreadGitPreview; attempt: GitAttempt; formSnapshot: string }) => executeThreadGit(client, threadID, {
      version: "thread_git.v1", run_id: reviewed.run_id, spec: reviewed.spec, operation_key: attempt.key,
      expected_preview_fingerprint: reviewed.preview_fingerprint, requested_by: "local-operator",
    }),
    onSuccess: (value, { attempt, formSnapshot }) => {
      setResult(value);
      queryClient.setQueryData(["thread", threadID, "git-request", attempt.key], value);
      if (value.state === "completed") {
        clearAttempt(attempt, formSnapshot);
      }
    },
    onSettled: (_data, _error, { attempt }) => {
      setPreview(null); invalidate();
      void queryClient.invalidateQueries({ queryKey: ["thread", threadID, "git-request", attempt.key] });
    },
  });
  function clearAttempt(attempt: GitAttempt, expectedForm = JSON.stringify(form.value)) {
    try {
      const persisted = store?.read<GitAttempt | null>(storageKey, null);
      if (persisted?.key === attempt.key) {
        store!.write(storageKey, null); setSaved(null);
        // Consume only the form that was confirmed; a later editor keeps its
        // selection and preview basis even when this response arrives late.
        form.update((current) => JSON.stringify(current) === expectedForm
          ? { ...current, paths: [], basis: "", previewed: false } : current);
      }
      else if (persisted) throw new Error(t("本机记录已变化，请先核对另一个窗口中的操作。", "Local records changed. Check the action in the other window first."));
    } catch (error) { setLocalError(error instanceof Error ? error.message : t("无法更新原操作记录。", "Could not update the original operation record.")); }
  }
  function beginCheck() {
    preflight.current?.abort();
    preflight.current = new AbortController();
    return { ...live.current, generation: generation.current, signal: preflight.current.signal };
  }
  function ensureCurrent(guard: ReturnType<typeof beginCheck>) {
    if (!mounted.current || guard.signal.aborted || generation.current !== guard.generation ||
      guard.threadID !== live.current.threadID || guard.repositoryID !== live.current.repositoryID ||
      guard.form !== live.current.form || live.current.working) {
      throw new Error(t("页面或操作表单已变化，未执行原操作。请重新预览。", "The page or form changed. The action was not executed. Preview again."));
    }
  }
  async function confirm() {
    if (!preview?.can_execute || !store || saved || execute.isPending || checking || prepare.isPending || working) return;
    const reviewed = preview;
    const guard = beginCheck();
    setChecking(true);
    try {
      const fresh = await readThreadGit(client, threadID, guard.signal);
      ensureCurrent(guard);
      queryClient.setQueryData(taskGitKey(threadID), fresh);
      if (!fresh.can_execute || gitFormRevision(fresh) !== gitFormRevision(reviewed)) {
        setPreview(null);
        throw new Error(fresh.blocked_reason || t("仓库内容、分支或目录已变化，原确认已失效。请重新预览后再确认。", "Repository content, branch or directory changed, invalidating confirmation. Preview and confirm again."));
      }
      if (reviewed.spec.paths?.some((path) => !fresh.changes.some((file) => file.path === path))) {
        throw new Error(t("部分所选文件已不在当前列表，原确认已失效。请重新选择并预览。", "Selected files are no longer listed, invalidating confirmation. Select and preview again."));
      }
      const attempt: GitAttempt = { threadID, runID: reviewed.run_id, key: `task-git-${crypto.randomUUID()}`, operation: reviewed.spec.operation as Operation };
      store.assertReadable();
      if (store.read(storageKey, null)) throw new Error(t("本机已有待核对操作，请刷新后核对。", "A local operation needs checking. Refresh and check it."));
      store.write(storageKey, attempt); setSaved(attempt); setResult(null); setLocalError("");
      execute.mutate({ reviewed, attempt, formSnapshot: guard.form });
    } catch (error) {
      if (mounted.current && live.current.threadID === guard.threadID && live.current.repositoryID === guard.repositoryID) {
        setPreview(null);
        setLocalError(t(`重新核对未完成，尚未执行。${error instanceof Error ? error.message : t("请刷新后重试。", "Refresh and try again.")}`, `Recheck incomplete; nothing executed. ${error instanceof Error ? error.message : t("请刷新后重试。", "Refresh and try again.")}`));
      }
    } finally { if (mounted.current) setChecking(false); }
  }
  const changed = () => { generation.current++; preflight.current?.abort(); setPreview(null); prepare.reset(); setResult(null); };
  const selectedRemote = remote || state.data?.remotes.find((item) => item.name === "origin" && item.url)?.url || state.data?.remotes.find((item) => item.url)?.url || "";
  const needsFiles = ["commit", "stage", "unstage"].includes(operation);
  const missingPaths = paths.filter((path) => !state.data?.changes.some((file) => file.path === path));
  const unavailableRemote = operation === "push_branch" && Boolean(selectedRemote) && !state.data?.remotes.some((item) => item.url === selectedRemote);
  const unavailableBranch = operation === "switch_branch" && Boolean(branch) && !state.data?.branches.includes(branch);
  const drifted = Boolean(state.data && form.value.basis && form.value.basis !== gitFormRevision(state.data));
  const mutationBlocked = working || !client.hasControl || !state.data?.can_execute || state.isError || state.isFetching || Boolean(saved) || execute.isPending || checking;
  const shownResult = validSaved ? observe.data : result;
  const chooseOperation = (next: Operation) => { if (next !== operation) { changed(); setOperation(next); } };
  const operationLocked = Boolean(saved) || execute.isPending || prepare.isPending || checking;
  return <section className="v2-task-delivery v2-task-git" aria-label={t("任务 Git 流程", "Task Git workflow")}>
    <div className="v2-delivery-heading"><h2>Git</h2><button type="button" disabled={state.isFetching} onClick={() => { changed(); void state.refetch(); }}>{t("刷新仓库", "Refresh repository")}</button></div>
    {state.isLoading && <LoadingState label={t("正在读取实际工作目录的 Git 状态…", "Loading Git status for the working directory…")} />}
    {state.isError && <ErrorState error={state.error} />}
    {state.data && <>
      <div className="v2-delivery-summary v2-git-location" aria-label={t("当前 Git 目录", "Current Git directory")}>
        <div className="v2-git-location-title"><strong>{directoryName(state.data.repository_root)}</strong>
          <span>{state.data.workspace_id === state.data.source_workspace_id ? t("项目目录", "Project directory") : t("隔离目录", "Isolated directory")}</span></div>
        <dl className="v2-git-location-facts">
          <div><dt>{t("当前分支", "Current branch")}</dt><dd>{state.data.branch || t("分离的 HEAD", "Detached HEAD")}</dd></div>
          <div><dt>{t("当前提交", "Current commit")}</dt><dd>{state.data.head_oid.slice(0, 8) || t("尚无提交", "No commits yet")}</dd></div>
        </dl>
        <details className="v2-git-evidence"><summary>{t("完整位置与版本", "Full location and revision")}</summary><dl>
          <div><dt>{t("操作目录", "Working directory")}</dt><dd>{state.data.repository_root}</dd></div>
          <div><dt>{t("完整提交", "Full commit")}</dt><dd>{state.data.head_oid || t("尚无提交", "No commits yet")}</dd></div>
          <div><dt>{t("目录身份", "Directory identity")}</dt><dd>{state.data.workspace_id}</dd></div>
          {state.data.workspace_id !== state.data.source_workspace_id && <div><dt>{t("来源项目身份", "Source project identity")}</dt><dd>{state.data.source_workspace_id}</dd></div>}
        </dl></details>
      {state.data.truncated && <p role="status">{t("文件列表已截断，只能选择当前已显示的文件。", "The file list is truncated. Only displayed files can be selected.")}</p>}
      {!state.data.can_execute && <p role="status">{t("当前不能操作：", "Actions unavailable: ")}{state.data.blocked_reason || t("仓库或执行环境尚未就绪。", "Repository or execution environment not ready.")}</p>}
      {working && <p role="status">{t("请先停止当前任务执行，再修改 Git 状态。", "Stop task execution before changing Git state.")}</p>}</div>
      {!store && <p role="status">{t("当前连接尚未提供本机恢复身份，无法保存提交或推送的待核对请求。", "This connection has no local recovery identity. Pending commit or push requests cannot be saved.")}</p>}
      {damaged && <p role="alert">{t("本机 Git 操作记录异常，已保留原记录；未执行新操作。", "Invalid local Git operation record. The original is preserved; no new operation ran.")}</p>}
      {validSaved && <section className="v2-delivery-operation" aria-label={t("上次 Git 操作", "Previous Git operation")}>
        <strong>{t("核对上次", "Check previous ")}{operations[validSaved.operation]}</strong>
        <p>{execute.isPending ? t("请求正在处理。", "Request processing.") : observe.isFetching ? t("正在只读核对原操作…", "Checking the original operation without changes…") : shownResult?.state === "completed" ? t("已确认原操作完成。", "Original operation confirmed complete.") : shownResult?.state === "not_received" ? t("服务端没有收到原操作，可以重新预览后再执行。", "The server did not receive the original operation. Preview again before executing.") : t("结果尚未确认，暂不能开始新操作。", "Result unconfirmed. New operations cannot start yet.")}</p>
        {observe.isError && <ErrorState error={observe.error} />}
        <button type="button" disabled={execute.isPending || observe.isFetching} onClick={() => void observe.refetch()}>{t("只读核对原操作", "Check original operation")}</button>
        {!execute.isPending && !observe.isFetching && !observe.isError && ["not_received", "completed"].includes(observe.data?.state ?? "") &&
          <button type="button" onClick={() => { setResult(observe.data!); clearAttempt(validSaved); invalidate(); }}>{t("确认结果并继续", "Acknowledge result and continue")}</button>}
      </section>}
      {shownResult && <div role="status"><p>{shownResult.state === "completed" ? t(`已确认${operations[(shownResult.spec?.operation as Operation) ?? operation] ?? t("操作", "Operation ")}完成`, `Confirmed complete: ${operations[(shownResult.spec?.operation as Operation) ?? operation] ?? t("操作", "Operation ")}`) : shownResult.state === "unknown" ? t("原操作结果尚未确认", "Original operation result unconfirmed") : t("原操作未收到", "Original operation not received")}</p>
        {(shownResult.commit_oid || shownResult.remote_oid || shownResult.operation_id) && <details className="v2-git-evidence"><summary>{t("查看操作记录", "View operation record")}</summary>
          {shownResult.commit_oid && <p>{t("提交：", "Commit: ")}{shownResult.commit_oid}</p>}{shownResult.remote_oid && <p>{t("远端提交：", "Remote commit: ")}{shownResult.remote_oid}</p>}
          {shownResult.operation_id && <p>{t("操作标识：", "Operation ID: ")}{shownResult.operation_id}</p>}
          <p>{shownResult.receipt_saved ? t("执行收据已保存。", "Execution receipt saved.") : t("执行收据尚未保存；是否完成以上方对原操作的核对状态为准。", "Execution receipt not yet saved. Use the original operation's checked status above to determine completion.")}</p>
        </details>}
        {shownResult.reason && <p>{shownResult.reason}</p>}
        {shownResult.worktree_path && <><p>{t("独立目录：", "Worktree: ")}{shownResult.worktree_path}</p><p>{t("新目录已从已提交版本创建。要在其中工作，点击“在此目录开始新任务”并确认新任务权限；原任务和未提交修改保留在原目录。", "The worktree was created from committed content. Select Start a new task here and confirm its permissions. The original task and uncommitted changes remain in the original directory.")}</p>
          {onOpenWorktree && <button type="button" disabled={!client.hasWorkspaceImport || openWorktree.isPending} onClick={() => openWorktree.mutate(shownResult.worktree_path!)}>{t("在此目录开始新任务", "Start a new task here")}</button>}</>}
        {shownResult.state === "completed" && shownResult.spec?.operation === "push_branch" && <button type="button" onClick={onPullRequest}>{t("继续创建或查看 PR", "Create or view PR")}</button>}
      </div>}
      {localError && <p role="alert">{localError}</p>}
      {openWorktree.isError && <ErrorState error={openWorktree.error} />}
      {execute.isError && <p role="alert">{t("操作响应未能确认，已保留原请求。", "Operation response unconfirmed. Original request preserved.")}{execute.error.message}</p>}
      {drifted && <p className="v2-git-form-notice" role="status">{t("仓库版本已变化，原预览确认已失效。文件选择和输入已保留，请核对当前状态后重新预览。", "Repository revision changed, invalidating the preview. File selection and input are preserved. Check current status and preview again.")}</p>}
      {!drifted && form.value.previewed && !preview && <p className="v2-git-form-notice" role="status">{t("上次操作表单已保留。请重新预览并确认当前操作。", "The previous form is preserved. Preview and confirm the current action again.")}</p>}
      <form className="v2-delivery-form" onSubmit={(event) => {
        event.preventDefault(); if (mutationBlocked || prepare.isPending || (needsFiles && missingPaths.length) || unavailableRemote || unavailableBranch) return;
        const spec: ThreadGitSpec = { operation,
          ...(needsFiles ? { paths } : {}), ...(operation === "commit" ? { message } : {}),
          ...(["create_branch", "switch_branch", "worktree_create"].includes(operation) ? { branch } : {}),
          ...(operation === "worktree_create" ? { worktree_name: worktreeName } : {}),
          ...(operation === "push_branch" ? { branch: state.data!.branch, remote_url: selectedRemote, credential_name: credentialName } : {}),
        }; setLocalError(""); setPreview(null); prepare.mutate(spec);
      }}>
        <div className="v2-git-actions" role="group" aria-label={t("Git 操作", "Git action")}>
          <button type="button" className="v2-git-main-action" aria-pressed={operation === "commit"} disabled={operationLocked} onClick={() => chooseOperation("commit")}>{t("提交", "Commit")}</button>
          <button type="button" aria-pressed={operation === "push_branch"} disabled={operationLocked} onClick={() => chooseOperation("push_branch")}>{t("推送", "Push")}</button>
          <details className="v2-git-more-actions"><summary>{t("分支、独立目录与暂存", "Branches, worktrees and staging")}</summary>
            <div role="group" aria-label={t("分支与独立目录", "Branches and worktrees")}>{(["create_branch", "switch_branch", "worktree_create"] as const).map((value) =>
              <button type="button" key={value} aria-pressed={operation === value} disabled={operationLocked} onClick={() => chooseOperation(value)}>{operations[value]}</button>)}</div>
            <div role="group" aria-label={t("暂存管理", "Staging")}>{(["stage", "unstage"] as const).map((value) =>
              <button type="button" key={value} aria-pressed={operation === value} disabled={operationLocked} onClick={() => chooseOperation(value)}>{operations[value]}</button>)}</div>
          </details>
        </div>
        <h3>{operations[operation]}</h3>
        {needsFiles && <p>{t("以下包含手动编辑和其他任务的修改。仅处理你选中的文件，其他已暂存内容会保留。", "These include manual edits and other tasks' changes. Only selected files are processed; other staged changes are preserved.")}</p>}
        {needsFiles && missingPaths.length > 0 && <div className="v2-git-form-notice" role="alert"><p>{t("以下已选文件不在当前可用列表中；可能已提交、移除或因列表截断未显示，不能直接用于本次操作。", "These selected files are unavailable, possibly committed, removed or outside the truncated list. They cannot be used for this action.")}</p>
          <ul>{missingPaths.map((path) => <li key={path}>{path}</li>)}</ul>
          <button type="button" disabled={operationLocked} onClick={() => { changed(); setPaths((previous) => previous.filter((path) => !missingPaths.includes(path))); }}>{t("移除这些不可用选择", "Remove unavailable selections")}</button>
        </div>}
        {needsFiles && <fieldset disabled={mutationBlocked || prepare.isPending}><legend>{t("选择文件（", "Select files (")}{paths.length}）</legend>
          {!state.data.changes.length && <p>{t("当前没有 Git 文件变化。", "No Git file changes.")}</p>}
          <div className="v2-git-files">{state.data.changes.map((file) => <label key={file.path}><input type="checkbox" checked={paths.includes(file.path)}
            onChange={(event) => { changed(); setPaths((previous) => event.target.checked ? [...previous, file.path] : previous.filter((path) => path !== file.path)); }} />
            <span><strong>{file.path}</strong><small>{t("暂存 ", "Staged ")}<StatusLabel status={file.staging || "—"} /> {t(" · 工作目录 ", " · Working directory ")}<StatusLabel status={file.worktree || "—"} /></small></span></label>)}</div>
        </fieldset>}
        {operation === "commit" && <label>{t("提交说明", "Commit message")}<textarea rows={3} required maxLength={4096} disabled={Boolean(saved) || prepare.isPending} value={message} onChange={(event) => { changed(); setMessage(event.target.value); }} /></label>}
        {["create_branch", "worktree_create"].includes(operation) && <label>{t("新分支名称", "New branch name")}<input required value={branch} disabled={Boolean(saved) || prepare.isPending} onChange={(event) => { changed(); setBranch(event.target.value); }} placeholder="feature/my-change" /></label>}
        {operation === "worktree_create" && <><label>{t("独立目录名称", "Worktree name")}<input required value={worktreeName} disabled={Boolean(saved) || prepare.isPending} onChange={(event) => { changed(); setWorktreeName(event.target.value); }} placeholder="my-change" /></label>
          <p>{t("从当前已提交版本创建独立分支和 worktree 目录。当前任务保持原目录与权限；创建后可在新目录开始新任务，权限需独立确认。", "Create a branch and worktree from the current commit. This task keeps its directory and permissions. Start a new task in the worktree with separately confirmed permissions.")}</p></>}
        {operation === "switch_branch" && <label>{t("目标分支", "Target branch")}<select required value={branch} disabled={Boolean(saved) || prepare.isPending} onChange={(event) => { changed(); setBranch(event.target.value); }}>
          <option value="">{t("选择分支", "Choose branch")}</option>{unavailableBranch && <option value={branch} disabled>{t("原选择已不可用：", "Previous selection unavailable: ")}{branch}</option>}{state.data.branches.filter((name) => name !== state.data!.branch).map((name) => <option key={name}>{name}</option>)}</select></label>}
        {operation === "push_branch" && <><label>{t("推送目标", "Push target")}<select required value={selectedRemote} disabled={Boolean(saved) || prepare.isPending} onChange={(event) => { changed(); setRemote(event.target.value); }}>
          {unavailableRemote && <option value={selectedRemote} disabled>{t("原推送目标已不可用：", "Previous push target unavailable: ")}{selectedRemote}</option>}
          {!state.data.remotes.some((item) => item.url) && <option value="">{t("没有可用的推送目标", "No push targets available")}</option>}{state.data.remotes.map((item) => <option key={`${item.name}:${item.url}`} value={item.url || `unavailable:${item.name}`} disabled={!item.url}>{item.name} · {item.url || item.blocked_reason || t("此远端暂不支持", "Remote unsupported")}</option>)}</select></label>
          <label>{t("凭据名称", "Credential name")}<input list={`task-git-credentials-${threadID}`} value={credentialName} disabled={Boolean(saved) || prepare.isPending} onChange={(event) => { changed(); setCredentialName(event.target.value); }} placeholder={t("选择或填写已保存的 GitHub 凭据名称", "Select or enter a saved GitHub credential name")} /></label>
          <datalist id={`task-git-credentials-${threadID}`}>{credentials.data?.filter((item) => item.credential.configured && item.connection.credential.kind !== "github_app_device").map((item) => <option value={item.connection.credential.name} key={item.connection.id}>{item.connection.repository.full_name}</option>)}</datalist>
          <button type="button" onClick={onPullRequest}>{t("到 PR 页面接入 GitHub", "Connect GitHub on the PR page")}</button>
          <p>{t("推送范围为当前分支已有提交。要包含工作目录中的修改，请先提交所选文件。", "Push includes existing commits on this branch. Commit selected working directory changes first to include them.")}</p></>}
        <details className="v2-git-limits"><summary>{t("项目检查与提交签名", "Project checks and commit signing")}</summary><p>{t("内置 Git 操作跳过本地 hooks，生成无签名提交。项目要求的本地检查与 GPG 或 SSH 签名需另行完成。", "Built-in Git operations skip local hooks and create unsigned commits. Run required local checks and GPG or SSH signing separately.")}</p></details>
        {(unavailableRemote || unavailableBranch) && <p role="alert">{t("原目标已不可用，请明确选择当前有效的目标后重新预览。", "The previous target is unavailable. Select a valid target and preview again.")}</p>}
        <button type="submit" className="v2-delivery-primary" disabled={mutationBlocked || prepare.isPending || (needsFiles && (!paths.length || missingPaths.length > 0)) || unavailableRemote || unavailableBranch}>{prepare.isPending ? t("正在生成预览…", "Generating preview…") : t("预览本次操作", "Preview action")}</button>
      </form>
      {prepare.isError && <ErrorState error={prepare.error} />}
      {preview && <section className="v2-git-preview" aria-label={t("Git 操作确认", "Confirm Git action")}>
        <h3>{t("确认", "Confirm: ")}{operations[preview.spec.operation as Operation] ?? t("Git 操作", "Git action")}</h3>
        <p>{t("目录：", "Directory: ")}{directoryName(preview.repository_root)}</p><p>{t("分支：", "Branch: ")}{preview.branch || t("分离的 HEAD", "Detached HEAD")} {t(" · 基准：", " · Base: ")}{preview.head_oid.slice(0, 8) || t("尚无提交", "No commits yet")}</p>
        {preview.spec.branch && <p>{t("目标分支：", "Target branch: ")}{preview.spec.branch}</p>}{preview.spec.remote_url && <p>{t("目标远端：", "Target remote: ")}{preview.spec.remote_url}</p>}
        {preview.target_commit_oid && <p>{t("目标提交：", "Target commit: ")}{preview.target_commit_oid.slice(0, 8)}</p>}
        {preview.commit_author && <p>{t("提交作者：", "Commit author: ")}{preview.commit_author.name} &lt;{preview.commit_author.email}&gt;</p>}
        {preview.spec.message && <pre>{preview.spec.message}</pre>}
        {preview.spec.operation === "commit" && <p>{t("提交说明末尾会附加 Traverse-Operation 操作标识，用于断线后核对原提交。", "A Traverse-Operation ID is appended to the commit message to check the original commit after a disconnect.")}</p>}
        {preview.spec.paths?.length ? <div className="v2-git-selected"><p>{t("本次所选文件（", "Selected files (")}{preview.spec.paths.length}）</p><ul>{preview.spec.paths.map((path) => <li key={path}>{path}</li>)}</ul></div> : null}
        <details className="v2-git-evidence"><summary>{t("完整位置与预览记录", "Full location and preview record")}</summary><dl>
          <div><dt>{t("操作目录", "Working directory")}</dt><dd>{preview.repository_root}</dd></div>
          <div><dt>{t("完整基准", "Full base commit")}</dt><dd>{preview.head_oid || t("尚无提交", "No commits yet")}</dd></div>
          {preview.target_commit_oid && <div><dt>{t("完整目标提交", "Full target commit")}</dt><dd>{preview.target_commit_oid}</dd></div>}
          <div><dt>{t("预览指纹", "Preview fingerprint")}</dt><dd>{preview.preview_fingerprint}</dd></div>
        </dl></details>
        {preview.diff && <ReviewDiff patch={preview.diff} source={t(`Git 预览：${preview.preview_fingerprint}\n分支：${preview.branch}\n提交：${preview.head_oid}\n目录：${preview.repository_root}`, `Git preview: ${preview.preview_fingerprint}\nBranch: ${preview.branch}\nCommit: ${preview.head_oid}\nDirectory: ${preview.repository_root}`)} onFeedback={onFeedback} onAddComment={onAddComment} />}
        {!preview.can_execute && <p role="alert">{preview.blocked_reason || t("此预览当前不能执行。", "This preview cannot currently be executed.")}</p>}
        {preview.spec.operation === "commit" && <p>{t("本次提交会跳过本地 hooks，生成无签名提交。请确认这种提交方式符合项目要求。", "This commit skips local hooks and is unsigned. Confirm this meets project requirements.")}</p>}
        <p>{t("确认后仅执行这份预览；内容或分支变化时需要重新预览。", "Confirmation executes only this preview. Content or branch changes require a new preview.")}</p>
        <button type="button" className="v2-delivery-primary" disabled={mutationBlocked || prepare.isPending || !store || !preview.can_execute || Boolean(state.data && gitFormRevision(preview) !== gitFormRevision(state.data))} onClick={() => void confirm()}>{checking ? t("正在重新核对仓库…", "Rechecking repository…") : execute.isPending ? t("正在执行…", "Running…") : t(`确认${operations[preview.spec.operation as Operation] ?? t("执行", "Execute")}`, `Confirm: ${operations[preview.spec.operation as Operation] ?? t("执行", "Execute")}`)}</button>
      </section>}
    </>}
  </section>;
}
