import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { GitHubReviewConnectionView } from "../../api/types";
import { createPullRequest, discoverPullRequest, observePullRequest, previewPullRequest, readThreadGit, refreshPullRequest, type PullRequestPreview, type PullRequestPreviewRequest, type PullRequestRefresh, type PullRequestResult } from "../../api/task-delivery";
import { ErrorState, LoadingState, StatusBadge } from "../../components/common";
import { useV2PersistentState, useV2RecoveryStore } from "../recovery-storage";
import { taskGitKey } from "./task-git";
import { TaskPRFollowup } from "./task-pr-followup";
import "./task-delivery.css";
import "./task-pull-request.css";

interface PRAttempt { threadID: string; key: string }
function validAttempt(value: unknown, threadID: string): value is PRAttempt {
  return Boolean(value && typeof value === "object" && "threadID" in value && value.threadID === threadID && "key" in value && typeof value.key === "string" && /^task-pr-[\w-]+$/u.test(value.key));
}
export function githubURL(value: string | undefined): string | undefined {
  try { const url = new URL(value || ""); return url.protocol === "https:" && url.hostname === "github.com" && !url.username && !url.password ? url.href : undefined; } catch { return undefined; }
}
export function TaskPullRequest({ client, threadID, working, onFeedback, onGit }: {
  client: APIClient; threadID: string; working: boolean; onFeedback: (context: string) => void; onGit: () => void;
}) {
  const queryClient = useQueryClient(), store = useV2RecoveryStore();
  const storageKey = `thread:${threadID}:pr-attempt`;
  const [saved, setSaved] = useV2PersistentState<PRAttempt | null>(storageKey, null);
  const [connectionID, setConnectionID] = useV2PersistentState(`thread:${threadID}:pr-connection`, "");
  const [number, setNumber] = useV2PersistentState(`thread:${threadID}:pr-number`, 0);
  const [base, setBase] = useV2PersistentState(`thread:${threadID}:pr-base`, "");
  const [title, setTitle] = useV2PersistentState(`thread:${threadID}:pr-title`, "");
  const [body, setBody] = useV2PersistentState(`thread:${threadID}:pr-body`, "");
  const [localPreview, setLocalPreview] = useState<{ attempt: PRAttempt; result: PullRequestPreview } | null>(null);
  const [localError, setLocalError] = useState("");
  const validSaved = saved && validAttempt(saved, threadID) ? saved : null;
  const current = useRef({ threadID, key: validSaved?.key, number, store });
  current.current = { threadID, key: validSaved?.key, number, store };
  const [lookup, setLookup] = useState({ threadID, connectionID, base });
  if (lookup.threadID !== threadID || lookup.connectionID !== connectionID) setLookup({ threadID, connectionID, base });
  const lookupBase = lookup.threadID === threadID && lookup.connectionID === connectionID ? lookup.base : base;
  const baseChanged = base !== lookupBase;
  const connections = useQuery({ queryKey: ["github-review", "connections"], queryFn: ({ signal }) => client.githubReviewConnections(false, signal), enabled: client.hasGitHubReviewControl });
  const git = useQuery({ queryKey: taskGitKey(threadID), queryFn: ({ signal }) => readThreadGit(client, threadID, signal), enabled: client.hasGitHubReviewControl });
  const discovery = useQuery({ queryKey: ["thread", threadID, "pr-discovery", connectionID, lookupBase],
    queryFn: ({ signal }) => discoverPullRequest(client, threadID, connectionID, lookupBase, signal), enabled: client.hasGitHubReviewControl && Boolean(connectionID), retry: false, refetchOnMount: "always" });
  const observe = useQuery({ queryKey: ["thread", threadID, "pr-request", validSaved?.key],
    queryFn: ({ signal }) => observePullRequest(client, threadID, validSaved!.key, signal), enabled: Boolean(validSaved), retry: false, refetchOnMount: "always" });
  const selectedPR = discovery.data?.pull_requests.find((pr) => pr.number === number) ?? discovery.data?.pull_requests[0];
  const selectedNumber = selectedPR?.number ?? number;
  const projection = useQuery({ queryKey: ["run", git.data?.run_id, "github-review", connectionID, selectedNumber],
    queryFn: ({ signal }) => client.githubReviewProjection(git.data!.run_id, connectionID, selectedNumber, signal),
    enabled: Boolean(git.data?.run_id && connectionID && selectedNumber > 0 && client.hasGitHubReviewControl) });
  const refresh = useMutation({ mutationFn: ({ connection, pr }: { connection: string; pr: number }) => refreshPullRequest(client, threadID, connection, pr),
    onSuccess: (value, request) => { queryClient.setQueryData(["thread", threadID, "pr-refresh", request.connection, request.pr], value);
      void projection.refetch(); void discovery.refetch(); } });
  const fresh = useQuery<PullRequestRefresh | null>({ queryKey: ["thread", threadID, "pr-refresh", connectionID, selectedNumber], queryFn: () => null, enabled: false, initialData: null });
  const snapshot = fresh.data?.snapshot ?? projection.data?.snapshots.find((item) => item.identity.number === selectedNumber);
  const drift = Boolean(snapshot && (snapshot.identity.head_sha !== git.data?.head_oid || fresh.data?.stale));
  const invalidate = (id: string) => { void queryClient.invalidateQueries({ queryKey: ["thread", id, "pr-discovery"] });
    void queryClient.invalidateQueries({ queryKey: ["thread", id, "pr-request"] }); };
  const prepare = useMutation({ mutationFn: ({ attempt, request }: { attempt: PRAttempt; request: PullRequestPreviewRequest }) => {
    store?.assertReadable();
    if (!store || store.read<PRAttempt | null>(`thread:${attempt.threadID}:pr-attempt`, null)?.key !== attempt.key) throw new Error("本机请求记录已变化，请刷新核对。");
    return previewPullRequest(client, attempt.threadID, request);
  },
    onSuccess: (value, { attempt }) => {
      if (current.current.threadID === attempt.threadID && current.current.key === attempt.key) setLocalPreview({ attempt, result: value });
    }, onSettled: (_value, _error, { attempt }) => invalidate(attempt.threadID) });
  const create = useMutation({ mutationFn: async ({ attempt, preview, approval }: {
    attempt: PRAttempt; preview: NonNullable<PullRequestResult["preview"]>; approval: NonNullable<PullRequestResult["approval"]>; selection: number; scope: typeof store;
  }) => {
    store?.assertReadable();
    if (!store || store.read<PRAttempt | null>(`thread:${attempt.threadID}:pr-attempt`, null)?.key !== attempt.key) throw new Error("本机请求记录已变化，请刷新核对。");
    if (approval.Status === "pending") await client.decideApproval(preview.run_id, approval.ID,
      { version: "approval_control.v1", action: "approve_once" }, `${attempt.key}-approve`);
    else if (approval.Status !== "approved") throw new Error("原审批不能用于创建，请重新核对。");
    return createPullRequest(client, attempt.threadID, preview.operation_id, approval.ID, attempt.key);
  }, onSuccess: (value, { attempt, selection, scope }) => {
    if (current.current.store !== scope) return;
    queryClient.setQueryData(["thread", attempt.threadID, "pr-request", attempt.key], value);
    if (value.pull_request && current.current.threadID === attempt.threadID && current.current.key === attempt.key && current.current.number === selection &&
      scope?.read<PRAttempt | null>(`thread:${attempt.threadID}:pr-attempt`, null)?.key === attempt.key && scope.read(`thread:${attempt.threadID}:pr-number`, 0) === selection) setNumber(value.pull_request.number);
  }, onSettled: (_value, _error, { attempt, scope }) => {
    if (current.current.store !== scope) return;
    if (current.current.threadID === attempt.threadID && current.current.key === attempt.key) setLocalPreview(null);
    invalidate(attempt.threadID);
  } });
  function clearAttempt(attempt: PRAttempt) {
    const key = `thread:${attempt.threadID}:pr-attempt`;
    if (!store || store.read<PRAttempt | null>(key, null)?.key !== attempt.key) throw new Error("本机请求记录已变化，请刷新核对。");
    store.write(key, null);
    if (current.current.threadID === attempt.threadID && current.current.key === attempt.key) {
      setSaved(null); setLocalPreview(null); prepare.reset(); create.reset();
    }
  }
  const discard = useMutation({ mutationFn: async ({ attempt, preview, approval }: {
    attempt: PRAttempt; preview: NonNullable<PullRequestResult["preview"]>; approval: NonNullable<PullRequestResult["approval"]>; scope: typeof store;
  }) => {
    // A pending approval must be explicitly denied before the local intent is forgotten.
    await client.decideApproval(preview.run_id, approval.ID, { version: "approval_control.v1", action: "deny" }, `${attempt.key}-deny`);
  }, onSuccess: (_value, { attempt, scope }) => { if (current.current.store === scope) clearAttempt(attempt); },
    onSettled: (_value, _error, { attempt, scope }) => { if (current.current.store === scope) invalidate(attempt.threadID); } });
  const observed = observe.data;
  const matchingPreview = localPreview?.attempt.threadID === threadID && localPreview.attempt.key === validSaved?.key ? localPreview.result : undefined;
  const reviewed = observed?.state === "proposed" ? observed.preview : matchingPreview?.preview;
  const approval = observed?.state === "proposed" ? observed.approval : matchingPreview?.approval;
  const pending = prepare.isPending || create.isPending || discard.isPending;
  const canCreate = Boolean(validSaved && reviewed && approval && ["pending", "approved"].includes(approval.Status) && (!observed || observed.state === "proposed") && !working && !pending && !observe.isFetching && !observe.isError);
  function findPullRequests() {
    if (!baseChanged) void discovery.refetch();
    setLookup({ threadID, connectionID, base });
  }
  function prepareIntent() {
    if (!store || saved || !discovery.data?.head_published || !title.trim() || pending || baseChanged) return;
    try {
      store.assertReadable(); if (store.read(storageKey, null)) throw new Error("已有待核对 PR 请求，请刷新后核对。");
      const attempt = { threadID, key: `task-pr-${crypto.randomUUID()}` };
      store.write(storageKey, attempt); setSaved(attempt); setLocalPreview(null); setLocalError("");
      prepare.mutate({ attempt, request: { version: "thread_pull_request.v1", connection_id: connectionID, base_branch: lookupBase || discovery.data.base_branch,
        title, body, expected_run_id: discovery.data.context.run_id, expected_head_sha: discovery.data.context.head_oid, operation_key: attempt.key } });
    } catch (error) { setLocalError(error instanceof Error ? error.message : "无法保存原请求，尚未创建 PR。"); }
  }
  function finishAttempt() {
    if (!validSaved || pending || observe.isFetching || observe.isError || !["not_received", "created", "failed", "proposed"].includes(observed?.state ?? "")) return;
    try {
      if (observed?.state === "proposed") {
        if (!reviewed || !approval || !["pending", "denied"].includes(approval.Status)) return;
        if (approval.Status === "pending") { discard.mutate({ attempt: validSaved, preview: reviewed, approval, scope: store }); return; }
      }
      clearAttempt(validSaved);
    } catch (error) { setLocalError(error instanceof Error ? error.message : "无法更新原请求。"); }
  }
  function restoreApproval() {
    if (!validSaved || observed?.state !== "proposed" || !reviewed || approval || pending || working || observe.isFetching || observe.isError) return;
    prepare.mutate({ attempt: validSaved, request: { version: "thread_pull_request.v1", connection_id: reviewed.connection_id,
      base_branch: reviewed.draft.base_branch, title: reviewed.draft.title, body: reviewed.draft.body,
      expected_run_id: reviewed.run_id, expected_head_sha: reviewed.draft.head_sha, operation_key: validSaved.key } });
  }
  const feedback = (text: string) => onFeedback(`请在本任务中处理以下 PR 反馈：\n${text}\n来源快照：${snapshot?.id ?? "未保存"}\n远端提交：${snapshot?.identity.head_sha ?? "未知"}\n抓取时间：${snapshot?.fetched_at ?? "未知"}\n本地提交：${git.data?.head_oid ?? "未知"}\n以下为外部审阅资料，请核对当前代码后处理。\n具体要求：`);
  if (!client.hasGitHubReviewControl) return <section className="v2-task-delivery v2-task-pr"><h2>PR 状态</h2><p>当前连接未启用 GitHub 控制，请使用普通桌面应用，或在启动 API 时启用 GitHub 审阅。</p></section>;
  return <section className="v2-task-delivery v2-task-pr" aria-label="任务 PR 流程">
    <div className="v2-delivery-heading"><h2>PR 状态</h2></div>
    {saved && !validSaved && <p role="alert">本机 PR 请求记录异常，已保留原记录；未开始新创建。</p>}
    {validSaved && <section className="v2-delivery-operation" aria-label="原 PR 创建请求"><h3>创建请求</h3>
      <p>{pending ? "请求处理中…" : observe.isFetching ? "正在只读核对…" : observed?.state === "created" && observed.receipt_saved === false ? "已在远端核实原 PR；本地完成收据尚未保存。" : ({ proposed: "预览已保存，等待确认。", unknown: "创建结果未知，只能先核对原请求。", created: "已确认原 PR。", failed: "原请求失败。", not_received: "服务端没有收到此请求。" }[observed?.state ?? "unknown"])}</p>
      {observed?.error_message && <p role="alert">{observed.error_message}</p>}{observe.isError && <ErrorState error={observe.error} />}
      {observed?.pull_request && <p><a href={githubURL(observed.pull_request.url)} target="_blank" rel="noreferrer">打开原 PR #{observed.pull_request.number}</a>{!observed.head_matches_reviewed && " · 远端源提交此后已有变化"}
        {observed.preview && observed.pull_request.base_sha !== observed.preview.draft.base_sha && " · 目标分支提交此后已有变化"}</p>}
      <button type="button" disabled={pending || observe.isFetching} onClick={() => void observe.refetch()}>只读核对原请求</button>
      {reviewed && <div className="v2-git-preview"><h3>确认创建草稿 PR</h3><p>{reviewed.draft.repository.full_name} · {reviewed.draft.head_branch} → {reviewed.draft.base_branch}</p>
        <details className="v2-pr-details"><summary>完整提交身份</summary><dl><div><dt>源提交</dt><dd><code>{reviewed.draft.head_sha}</code></dd></div><div><dt>目标提交</dt><dd><code>{reviewed.draft.base_sha}</code></dd></div><div><dt>原请求</dt><dd><code>{validSaved.key}</code></dd></div></dl></details><strong>{reviewed.draft.title}</strong><pre>{reviewed.draft.body}</pre>
        {approval ? <button type="button" disabled={!canCreate || !client.hasApprovalControl} onClick={() => create.mutate({ attempt: validSaved, preview: reviewed, approval, selection: number, scope: store })}>批准并创建这份草稿 PR</button>
          : <><p>原预览已保存，审批记录尚未就绪。请先补齐原预览的审批，再确认创建 PR。</p>
            <button type="button" disabled={pending || working || observe.isFetching || observe.isError || observed?.state !== "proposed"} onClick={restoreApproval}>补齐原预览的审批</button></>}
        {approval?.Status === "approved" && <p>原审批已批准；清除本机记录不能撤销它。请继续核对或执行原请求。</p>}
        {approval?.Status === "denied" && <p>原审批已拒绝，不能用于创建 PR。</p>}
      </div>}
      {!pending && !observe.isFetching && !observe.isError && (["not_received", "created", "failed"].includes(observed?.state ?? "") ||
        (observed?.state === "proposed" && reviewed && approval && ["pending", "denied"].includes(approval.Status))) &&
        <button type="button" disabled={approval?.Status === "pending" && !client.hasApprovalControl} onClick={finishAttempt}>{observed?.state === "proposed" ? approval?.Status === "pending" ? "拒绝原审批并放弃预览" : "放弃此预览并重新填写" : "确认结果并继续"}</button>}
    </section>}
    {prepare.isError && <ErrorState error={prepare.error} />}{create.isError && <ErrorState error={create.error} />}{discard.isError && <ErrorState error={discard.error} />}{localError && <p role="alert">{localError}</p>}
    {connections.isLoading && <LoadingState label="正在读取 GitHub 连接…" />}{connections.isError && <ErrorState error={connections.error} />}
    {!connectionID && <p className="v2-pr-empty">选择项目的 GitHub 连接，查看 PR、CI 和审阅意见。</p>}
    {connectionID && <>
      {baseChanged && <p role="status">目标分支草稿已保存，请点击查找后再预览 PR。</p>}
      {discovery.isFetching && <p role="status">正在核对远端分支和已有 PR…</p>}{discovery.isError && <ErrorState error={discovery.error} />}
      {discovery.data && <section className="v2-pr-overview" aria-label="当前 PR 状态">
        {selectedPR ? <header><h3><a href={githubURL(selectedPR.url)} target="_blank" rel="noreferrer">#{selectedPR.number} {selectedPR.title.text}</a></h3>
          <span className="v2-pr-state">{selectedPR.merged ? "已合并" : selectedPR.draft ? "草稿" : selectedPR.state === "open" ? "待审阅" : "已关闭"}</span></header>
          : <h3>当前分支尚无开放的 PR</h3>}
        <p className="v2-pr-meta">{discovery.data.repository.full_name} · {discovery.data.context.branch} → {discovery.data.base_branch}</p>
        {!discovery.data.head_published && <div className="v2-pr-notice"><p>请先推送当前提交，再创建 PR。</p><button type="button" onClick={onGit}>前往提交与推送</button></div>}
      </section>}
      {Boolean(discovery.data && discovery.data.pull_requests.length > 1) && <label>已有 PR<select value={selectedNumber} onChange={(event) => setNumber(Number(event.target.value))}>
        {discovery.data!.pull_requests.map((pr) => <option key={pr.number} value={pr.number}>#{pr.number} {pr.title.text} · {pr.draft ? "草稿" : pr.state}</option>)}</select></label>}
      <div className="v2-delivery-actions">
        {selectedNumber > 0 && <button type="button" disabled={refresh.isPending || pending} onClick={() => refresh.mutate({ connection: connectionID, pr: selectedNumber })}>{refresh.isPending ? "正在从 GitHub 抓取…" : "刷新远端 CI 和评论"}</button>}
        <button type="button" disabled={discovery.isFetching} onClick={findPullRequests}>查找当前分支的 PR</button>
      </div>
    </>}
    <TaskPRFollowup client={client} threadID={threadID} connectionID={connectionID} pullRequest={selectedNumber}
      canStart={Boolean(connectionID && selectedPR?.state === "open" && !selectedPR.merged && !discovery.isFetching && !discovery.isError)} />
    {connectionID && <>
      {selectedNumber > 0 && <>
        {refresh.isError && <ErrorState error={refresh.error} />}{projection.isError && <ErrorState error={projection.error} />}
        {!snapshot && <p>尚无这份 PR 的 CI 或评论快照，请刷新远端。</p>}
        {snapshot && <>
          <p className="v2-pr-meta">上次抓取：{new Date(snapshot.fetched_at).toLocaleString()} · 提交 <code>{snapshot.identity.head_sha.slice(0, 12)}</code></p>
          {Boolean(git.data?.changes.length) && <p role="status" className="v2-pr-notice">工作目录还有未提交的改动，以下 CI 对应上方提交。请先核对本次 PR 的范围，选择相关文件提交并推送，再核对新提交的检查结果。</p>}
          {(drift || refresh.isError) && <p role="status" className="v2-pr-notice">{refresh.isError ? "本次刷新失败，以下保留上次读取的记录。请重试刷新远端 CI 和评论。" : "远端记录与当前本地版本不同。请核对本地提交与远端提交，推送后刷新检查结果。"}</p>}
          {(snapshot.omissions.length > 0 || fresh.data?.omissions.length) ? <details className="v2-pr-details"><summary>部分资料未能读取</summary><ul>{[...new Set([...snapshot.omissions, ...(fresh.data?.omissions ?? [])])].map((reason) => <li key={reason}>{reason}</li>)}</ul></details> : null}
          <section className="v2-pr-records" aria-label="CI 检查"><h3>CI 检查 <span>{snapshot.check_runs.length} 项检查 · {snapshot.jobs.length} 项作业</span></h3>{!snapshot.check_runs.length && !snapshot.jobs.length && <p>CI 结果尚未读取，检查结论待确认。请刷新远端 CI 和评论。</p>}
          {snapshot.check_runs.map((check) => <article className="v2-delivery-change" key={`check:${check.id}`}><header><strong>{check.name}</strong><StatusBadge status={check.conclusion || check.status} /></header>
            {check.head_sha !== snapshot.identity.head_sha && <p>此检查来自不同提交。</p>}
            {(check.summary.text || check.text.text) && <details><summary>检查输出</summary><pre>{check.summary.text}{check.summary.text && check.text.text ? "\n" : ""}{check.text.text}</pre></details>}
            <button type="button" onClick={() => feedback(`检查 ${check.name} / ${check.id}\n提交：${check.head_sha}\n状态：${check.status} / ${check.conclusion || "尚无结论"}\n${check.summary.text}\n${check.text.text}`)}>引用检查并修复</button></article>)}
          {snapshot.jobs.map((job) => <article className="v2-delivery-change" key={`job:${job.id}`}><header><strong>{job.name}</strong><StatusBadge status={job.conclusion || job.status} /></header>
            <details><summary>日志摘录</summary><pre>{job.failed_log.text || job.log_reason || "未抓取日志"}</pre></details>
            <button type="button" onClick={() => feedback(`CI 作业 ${job.name} / ${job.id}\n提交：${job.head_sha}\n状态：${job.status} / ${job.conclusion || "尚无结论"}\n日志状态：${job.log_state}\n${job.failed_log.text || job.log_reason || "未抓取日志"}`)}>引用作业并修复</button></article>)}
          </section><section className="v2-pr-records" aria-label="审阅意见"><h3>审阅意见</h3>
          {!snapshot.threads.length && !snapshot.loose_comments.length && !snapshot.reviews.length && <p>没有已抓取的审阅意见。</p>}
          {snapshot.threads.map((thread) => <article className="v2-delivery-change" key={thread.id}><header><strong>{thread.path.split(/[\\/]/u).at(-1)}:{thread.line || ""}</strong><span>{thread.resolved ? "已解决" : "未解决"} · {thread.outdated ? "已过期位置" : "原评论位置"}</span></header>
            <details><summary>评论位置</summary><p>{thread.path} · {thread.side} {thread.line || "未提供行号"}</p></details>
            {thread.comments.map((comment) => <div key={comment.node_id}><p>{comment.author}</p><pre>{comment.body.text}</pre>
              <button type="button" onClick={() => feedback(`评论：${comment.url || comment.node_id}\n文件：${thread.path}\n位置：${thread.side} ${thread.line || "未提供"}\n原提交：${comment.position.commit_sha}\n是否过期：${thread.outdated}\n作者：${comment.author}\n${comment.body.text}`)}>引用意见并修复</button></div>)}</article>)}
          {snapshot.loose_comments.map((comment) => <article className="v2-delivery-change" key={comment.node_id}><p>{comment.author}</p><pre>{comment.body.text}</pre>
            <button type="button" onClick={() => feedback(`评论：${comment.url || comment.node_id}\n作者：${comment.author}\n${comment.body.text}`)}>引用意见并修复</button></article>)}
          {snapshot.reviews.filter((review) => review.body.text).map((review) => <article className="v2-delivery-change" key={review.node_id}><p>{review.author} · {review.state}</p><pre>{review.body.text}</pre>
            <button type="button" onClick={() => feedback(`审阅：${review.node_id}\n提交：${review.commit_sha}\n作者：${review.author}\n${review.body.text}`)}>引用审阅并修复</button></article>)}
          </section>
          <details className="v2-pr-details"><summary>快照与完整提交身份</summary><dl>
            <div><dt>来源快照</dt><dd><code>{snapshot.id}</code></dd></div><div><dt>PR 提交</dt><dd><code>{snapshot.identity.head_sha}</code></dd></div>
            <div><dt>本地提交</dt><dd><code>{git.data?.head_oid || "未知"}</code></dd></div>
          </dl></details>
        </>}
      </>}
      <details className="v2-pr-details"><summary>目标分支与远端详情</summary><div className="v2-pr-detail-content">
        <label>目标分支<input value={base} placeholder={discovery.data?.base_branch || "留空使用仓库默认分支"} disabled={Boolean(saved) || pending} onChange={(event) => setBase(event.target.value)} /></label>
        {discovery.data && <dl><div><dt>本地提交</dt><dd><code>{discovery.data.context.head_oid}</code></dd></div>
          <div><dt>远端提交</dt><dd><code>{discovery.data.remote_head_sha || "此分支尚未发布"}</code></dd></div>
          <div><dt>核对时间</dt><dd>{new Date(discovery.data.checked_at).toLocaleString()}</dd></div></dl>}
      </div></details>
      {!discovery.data?.pull_requests.length && !saved && <details className="v2-pr-compose"><summary>创建草稿 PR</summary>
        <form className="v2-delivery-form" onSubmit={(event) => { event.preventDefault(); prepareIntent(); }}>
          <label>标题<input required maxLength={256} value={title} disabled={pending} onChange={(event) => setTitle(event.target.value)} /></label>
          <label>说明<textarea required rows={5} maxLength={16000} value={body} disabled={pending} onChange={(event) => setBody(event.target.value)} placeholder="说明解决的问题、实际改动与验证结果" /></label>
          <button type="submit" disabled={!store || pending || working || baseChanged || discovery.isFetching || discovery.isError || !discovery.data?.head_published || !discovery.data?.write_enabled}>预览草稿 PR</button>
          {!store && <p>连接未提供本机恢复身份，无法保存 PR 创建请求。</p>}
          {discovery.data && !discovery.data.write_enabled && <p>此 GitHub 连接尚未允许写入，请在连接设置中明确开启。</p>}
          {discovery.data && !discovery.data.write_permission_verified && <p>创建时仍由 GitHub 核对仓库写入权限。</p>}
        </form>
      </details>}
    </>}
    <details className="v2-pr-details v2-pr-connection"><summary><span>{connectionID ? "连接设置" : "选择或接入 GitHub"}</span>{connectionID && <span className="v2-pr-connection-name">{connections.data?.find((item) => item.connection.id === connectionID)?.connection.repository.full_name || "已选择连接"}</span>}</summary><div className="v2-pr-detail-content">
      <label>GitHub 连接<select value={connectionID} disabled={pending || Boolean(saved)} onChange={(event) => { setConnectionID(event.target.value); setNumber(0); setLocalPreview(null); }}>
        <option value="">选择此项目的仓库连接</option>{connections.data?.map((item) => <option key={item.connection.id} value={item.connection.id}>{item.connection.repository.full_name} · {item.credential.configured ? "已连接" : "尚未登录"}</option>)}</select></label>
      <details><summary>接入 GitHub 或保存凭据</summary><GitHubConnectionSetup key={connectionID} client={client} threadID={threadID} selectedConnection={connectionID}
        connection={connections.data?.find((item) => item.connection.id === connectionID)?.connection}
        onConfigured={(id) => { setConnectionID(id); void connections.refetch(); void discovery.refetch(); }} /></details>
    </div></details>
  </section>;
}

function GitHubConnectionSetup({ client, threadID, selectedConnection, connection, onConfigured }: {
  client: APIClient; threadID: string; selectedConnection: string; connection?: GitHubReviewConnectionView; onConfigured: (id: string) => void;
}) {
  const [repository, setRepository] = useState(""), [name, setName] = useState("traverse-github");
  const [token, setToken] = useState(""), [write, setWrite] = useState<boolean | null>(null);
  const writeEnabled = write ?? connection?.network.write_enabled ?? false;
  const secretRef = useRef("");
  const mutation = useMutation({ mutationFn: async () => {
    const secret = secretRef.current;
    secretRef.current = "";
    let connectionID = selectedConnection;
    if (repository.trim()) {
      const [owner, repo, extra] = repository.trim().split("/");
      if (!owner || !repo || extra) throw new Error("请填写 owner/repository 格式的仓库名称。");
      const configured = await client.configureGitHubReview({ repository: { host: "github.com", owner, name: repo, full_name: `${owner}/${repo}`, private: false },
        credential: { name: name.trim(), kind: "fine_grained_pat" }, enabled: true, expected_generation: 0, allowed_log_hosts: [], write_enabled: writeEnabled });
      connectionID = configured.connection.id;
      onConfigured(connectionID);
    } else if (connection && writeEnabled !== connection.network.write_enabled) {
      const kind = connection.credential.kind;
      if (connection.repository.host !== "github.com" || !["github_app_device", "oauth_user", "fine_grained_pat"].includes(kind)) throw new Error("此连接类型不能在这里更新。");
      const configured = await client.configureGitHubReview({ connection_id: connection.id,
        repository: { ...connection.repository, host: "github.com" }, credential: { name: connection.credential.name, kind: kind as "github_app_device" | "oauth_user" | "fine_grained_pat" }, client_id: connection.client_id,
        enabled: connection.enabled, expected_generation: connection.generation,
        allowed_log_hosts: connection.network.allowed_log_hosts, write_enabled: writeEnabled });
      connectionID = configured.connection.id;
    }
    if (!connectionID) throw new Error("请先选择连接或填写仓库名称。");
    if (secret) await client.postControl(`/threads/${encodeURIComponent(threadID)}/pull-request/credential`,
      { version: "thread_pull_request.v1", connection_id: connectionID, token: secret }, `pr-credential-${crypto.randomUUID()}`);
    return connectionID;
  }, onSuccess: onConfigured });
  return <form className="v2-delivery-form" onSubmit={(event) => { event.preventDefault(); secretRef.current = token; setToken(""); mutation.mutate(); }}>
    <p>使用 GitHub 的细粒度访问令牌；凭据保存到本机系统凭据库。已有连接可只更新令牌。</p>
    <label>新连接的仓库<input value={repository} onChange={(event) => setRepository(event.target.value)} placeholder="owner/repository（已有连接可留空）" disabled={mutation.isPending} /></label>
    {repository.trim() && <label>凭据名称<input required value={name} onChange={(event) => setName(event.target.value)} disabled={mutation.isPending} /></label>}
    <label><input type="checkbox" checked={writeEnabled} onChange={(event) => setWrite(event.target.checked)} disabled={mutation.isPending} />允许经我确认后向此仓库写入</label>
    <label>GitHub 访问令牌<input type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} disabled={mutation.isPending} /></label>
    <button type="submit" disabled={mutation.isPending}>{mutation.isPending ? "正在保存…" : "保存 GitHub 连接"}</button>
    {mutation.isError && <ErrorState error={mutation.error} />}{mutation.isSuccess && <p role="status">连接已保存，请核对远端仓库。</p>}
  </form>;
}
