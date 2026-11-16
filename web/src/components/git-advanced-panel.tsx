import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, GitBranch, LoaderCircle, RefreshCw, ShieldCheck } from "lucide-react";
import type { APIClient } from "../api/client";
import { gitAdvancedProtocol, type GitAdvancedOperation } from "../api/git-advanced";
import type {
  GitAdvancedPreviewView,
  GitAdvancedReviewResultView,
  GitAdvancedSpecView,
} from "../api/types";
import { formatDate, formatWorkspacePathLabel, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, KeyValue, LoadingState, StatusBadge } from "./common";

const objectIDPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/u;

function exactObjects(value: string): string[] {
  return value.split(/[\s,]+/u).map((item) => item.trim()).filter(Boolean);
}

function operationKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `desktop-git-advanced-${crypto.randomUUID()}`;
  }
  return `desktop-git-advanced-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function createSpec(operation: GitAdvancedOperation,
  fields: Partial<GitAdvancedSpecView> = {}): GitAdvancedSpecView {
  return { protocol_version: gitAdvancedProtocol, operation, ...fields };
}

export function GitAdvancedPanel({ client, runID, onOpenApprovals,
  retainedReview, onRetainedReviewChange }: {
  client: APIClient;
  runID: string;
  onOpenApprovals: () => void;
  retainedReview?: GitAdvancedReviewResultView | null;
  onRetainedReviewChange?: (value: GitAdvancedReviewResultView | null) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [localReview, setLocalReview] = useState<GitAdvancedReviewResultView | null>(null);
  const review = retainedReview === undefined ? localReview : retainedReview;
  const setReview = (value: GitAdvancedReviewResultView | null) => {
    setLocalReview(value);
    onRetainedReviewChange?.(value);
  };
  const [hunkOperation, setHunkOperation] = useState<GitAdvancedOperation>("hunk_stage");
  const [hunkPaths, setHunkPaths] = useState("");
  const [selectedHunks, setSelectedHunks] = useState<string[]>([]);
  const [stashMessage, setStashMessage] = useState("Desktop checkpoint stash");
  const [includeUntracked, setIncludeUntracked] = useState(false);
  const [keepIndex, setKeepIndex] = useState(false);
  const [rebaseUpstream, setRebaseUpstream] = useState("");
  const [rebaseOnto, setRebaseOnto] = useState("");
  const [cherryCommits, setCherryCommits] = useState("");
  const [bisectGood, setBisectGood] = useState("");
  const [bisectBad, setBisectBad] = useState("");
  const [bisectRecipe, setBisectRecipe] = useState<"go_test" | "npm_test">("go_test");
  const [bisectSteps, setBisectSteps] = useState("32");
  const [bisectTimeout, setBisectTimeout] = useState("120");
  const validBisectLimits = /^[1-9][0-9]*$/u.test(bisectSteps) && Number(bisectSteps) <= 128 &&
    /^[1-9][0-9]*$/u.test(bisectTimeout) && Number(bisectTimeout) <= 900;
  const [worktreeName, setWorktreeName] = useState("");
  const [worktreeBranch, setWorktreeBranch] = useState("");

  const query = useQuery({
    queryKey: ["run", runID, "git-advanced"],
    queryFn: ({ signal }) => client.gitAdvancedProjection(runID, signal),
    enabled: Boolean(runID) && client.hasGitAdvancedControl,
  });
  const discover = useMutation({
    mutationFn: (spec: GitAdvancedSpecView) => client.discoverGitAdvancedHunks(runID, spec),
    onSuccess: (value) => {
      setReview(value);
      setSelectedHunks(value.preview.hunks.map((hunk) => hunk.id));
    },
  });
  const requestReview = useMutation({
    mutationFn: (spec: GitAdvancedSpecView) => {
      const scope = query.data?.authority.scope;
      if (!scope) throw new Error("Git authority projection is unavailable");
      return client.reviewGitAdvanced(runID, {
        operation_key: operationKey(), scope, spec,
      });
    },
    onSuccess: (value) => {
      setReview(value);
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "git-advanced"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "approvals"] });
    },
  });
  const execute = useMutation({
    mutationFn: () => {
      const scope = query.data?.authority.scope;
      const operationID = review?.operation?.id;
      const approvalID = review?.approval?.ID;
      if (!scope || !operationID || !approvalID) {
        throw new Error("An exact reviewed operation and approval are required");
      }
      return client.executeGitAdvanced(runID, {
        operation_id: operationID, approval_id: approvalID, scope,
      });
    },
    onSuccess: () => {
      setReview(null);
      setSelectedHunks([]);
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "git-advanced"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "approvals"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "checkpoints"] });
    },
  });
  const pending = discover.isPending || requestReview.isPending || execute.isPending;
  const mutationError = discover.error || requestReview.error || execute.error;
  const projection = query.data;
  const sequence = projection?.sequence;
  const canMutate = Boolean(projection?.authority.executable) && !pending;
  const selectedSet = useMemo(() => new Set(selectedHunks), [selectedHunks]);
  const paths = hunkPaths.split(/\r?\n/u).filter(Boolean);
  const hunkSpec = createSpec(hunkOperation, paths.length > 0 ? { paths } : {});
  const discovered = review?.preview.operation === hunkOperation &&
    JSON.stringify(review.preview.spec.paths ?? []) === JSON.stringify(paths) ? review.preview : null;

  const reviewSpec = (spec: GitAdvancedSpecView) => {
    setReview(null);
    requestReview.mutate(spec);
  };

  if (!client.hasGitAdvancedControl) {
    return <section aria-label={t("高级 Git", "Advanced Git")} className="repository-state-panel">
      <header className="panel-header"><div><GitBranch aria-hidden="true" size={17} />
        <h2>{t("高级 Git", "Advanced Git")}</h2></div></header>
      <EmptyState>{t(
        "启用高级 Git、权限控制、操作员审批和项目检查点后，可在这里预览并审批仓库操作。",
        "Enable Advanced Git, permission control, operator approval, and workspace checkpoints to preview and approve repository operations here.",
      )}</EmptyState>
    </section>;
  }
  if (query.isLoading) return <LoadingState label={t("加载高级 Git 状态", "Loading Advanced Git state")} />;
  if (query.isError || !projection) return <ErrorState error={query.error} />;

  return <section aria-label={t("高级 Git", "Advanced Git")} className="repository-state-panel git-advanced-panel">
    <header className="panel-header">
      <div><GitBranch aria-hidden="true" size={17} /><h2>{t("高级 Git", "Advanced Git")}</h2></div>
      <button aria-label={t("刷新高级 Git", "Refresh Advanced Git")} className="icon-button"
        disabled={query.isFetching || pending} onClick={() => void query.refetch()} type="button">
        <RefreshCw aria-hidden="true" className={query.isFetching ? "spin" : ""} size={16} />
      </button>
    </header>

    <div className="repository-reference git-advanced-authority">
      <KeyValue label="HEAD" value={shortID(projection.binding.head)} />
      <KeyValue label={t("分支", "Branch")}
        value={projection.binding.detached ? t("分离 HEAD", "detached HEAD") : projection.binding.branch} />
      <KeyValue label={t("权限修订", "Permission revision")}
        value={String(projection.authority.permission_revision)} />
      <KeyValue label={t("租约代次", "Lease generation")}
        value={String(projection.authority.scope.lease_generation)} />
      <KeyValue label={t("可执行", "Executable")}
        value={projection.authority.executable ? t("是", "yes") : t("否", "no")} />
      <KeyValue label={t("上游", "Upstream")}
        value={projection.binding.upstream_ref ?? t("未配置", "not configured")} />
    </div>
    <details className="git-advanced-evidence">
      <summary>{t("精确仓库绑定", "Exact repository binding")}</summary>
      <code>{projection.binding.repository_sha256}</code>
      <small>index {projection.binding.index_sha256}</small>
      <small>worktree {projection.binding.worktree_sha256}</small>
      <small>status {projection.binding.status_sha256}</small>
      <small>stash {projection.binding.stash_sha256}</small>
      <small>sequence {projection.binding.sequence_sha256}</small>
      {projection.binding.upstream_oid && <small>upstream {projection.binding.upstream_oid}</small>}
    </details>

    {projection.conflict.active && <section className="git-advanced-conflicts">
      <h3><AlertTriangle aria-hidden="true" size={16} />{t("冲突状态", "Conflict state")}</h3>
      {projection.conflict.files.map((file) => <article key={file.path}>
        <strong>{formatWorkspacePathLabel(file.path)}</strong>
        <small>base {file.base_oid ?? "—"}</small><small>ours {file.ours_oid ?? "—"}</small>
        <small>theirs {file.theirs_oid ?? "—"}</small>
      </article>)}
    </section>}

    <section className="git-advanced-section">
      <h3>{t("按差异块操作", "Diff hunk operations")}</h3>
      <div className="git-advanced-form-row">
        <select aria-label={t("Hunk 操作", "Hunk operation")} value={hunkOperation}
          onChange={(event) => setHunkOperation(event.target.value as GitAdvancedOperation)}>
          <option value="hunk_stage">stage</option><option value="hunk_unstage">unstage</option>
          <option value="hunk_revert">revert</option>
        </select>
        <textarea aria-label={t("限定路径", "Restricted paths")} onChange={(event) => setHunkPaths(event.target.value)}
          placeholder={t("可选：每行一个仓库相对路径", "Optional: one repository-relative path per line")}
          rows={2} value={hunkPaths} />
        <button disabled={!canMutate} onClick={() => discover.mutate(hunkSpec)} type="button">
          {t("查看可选差异块", "Inspect available hunks")}
        </button>
      </div>
      {discovered && discovered.hunks.length > 0 &&
        <div className="git-advanced-hunks">
          {discovered.hunks.map((hunk) => <label key={hunk.id}>
            <input checked={selectedSet.has(hunk.id)} onChange={() => setSelectedHunks((current) =>
              current.includes(hunk.id) ? current.filter((id) => id !== hunk.id) : [...current, hunk.id])}
              type="checkbox" />
            <span>{formatWorkspacePathLabel(hunk.path)} · -{hunk.old_start},+{hunk.new_start}</span>
            <pre>{hunk.patch}</pre>
          </label>)}
          <button disabled={!canMutate || selectedHunks.length === 0} onClick={() => reviewSpec(
            { ...hunkSpec, hunk_ids: selectedHunks })} type="button">
            {t("请求所选 hunk 审批", "Request approval for selected hunks")}
          </button>
        </div>}
    </section>

    <section className="git-advanced-section">
      <h3>{t("Stash（精确对象）", "Stashes (exact objects)")}</h3>
      <div className="git-advanced-form-row">
        <input aria-label={t("Stash 审计消息", "Stash audit message")} onChange={(event) => setStashMessage(event.target.value)}
          value={stashMessage} />
        <label><input checked={includeUntracked} onChange={(event) => setIncludeUntracked(event.target.checked)}
          type="checkbox" />{t("包含未跟踪文件", "Include untracked")}</label>
        <label><input checked={keepIndex} onChange={(event) => setKeepIndex(event.target.checked)}
          type="checkbox" />{t("保留索引", "Keep index")}</label>
        <button disabled={!canMutate || !stashMessage.trim()} onClick={() => reviewSpec(createSpec("stash_create",
          { message: stashMessage.trim(), include_untracked: includeUntracked, keep_index: keepIndex }))} type="button">
          {t("请求创建审批", "Request create approval")}
        </button>
      </div>
      {projection.stashes.length === 0 ? <small>{t("没有 stash", "No stashes")}</small> :
        projection.stashes.map((stash) => <article className="git-advanced-stash" key={stash.oid}>
          <strong>{stash.subject || t("无主题", "No subject")}</strong><code>{stash.oid}</code>
          <small>base {stash.base_commit}</small><small>index {stash.index_commit}</small>
          {stash.untracked_commit && <small>untracked {stash.untracked_commit}</small>}
          <ul>{stash.files.map((file) => <li key={`${file.change}:${file.path}`}>{file.change}: {formatWorkspacePathLabel(file.path)}</li>)}</ul>
          <div className="git-advanced-actions">
            {(["stash_apply", "stash_pop", "stash_drop"] as const).map((operation) =>
              <button disabled={!canMutate} key={operation} onClick={() => reviewSpec(createSpec(operation,
                { stash_oid: stash.oid, ...(operation === "stash_apply" || operation === "stash_pop" ?
                  { restore_index: true } : {}) }))} type="button">{operation.replace("stash_", "")}</button>)}
          </div>
        </article>)}
    </section>

    <section className="git-advanced-section">
      <h3>{t("序列与恢复", "Sequences and recovery")}</h3>
      {sequence ? <article>
        <div><StatusBadge status={sequence.status} /> {sequence.kind} · {shortID(sequence.id)} · HEAD {shortID(sequence.current_head)}</div>
        <div className="git-advanced-actions">
          {sequence.kind !== "bisect" && (["continue", "skip", "abort"] as const).map((action) => {
            const operation = `${sequence.kind}_${action}` as GitAdvancedOperation;
            const allowed = action === "continue" ? projection.conflict.can_continue :
              action === "skip" ? projection.conflict.can_skip : projection.conflict.can_abort;
            return <button disabled={!canMutate || !allowed} key={action}
              onClick={() => reviewSpec(createSpec(operation, { sequence_id: sequence.id }))}
              type="button">{action}</button>;
          })}
          {sequence.kind === "bisect" && <>
            {(["good", "bad", "skip"] as const).map((mark) => <button disabled={!canMutate}
              key={mark} onClick={() => reviewSpec(createSpec(`bisect_${mark}` as GitAdvancedOperation,
                { sequence_id: sequence.id, expected_current: sequence.current_head }))}
              type="button">{mark}</button>)}
            <label>{t("验证模板", "Verification template")}
              <select aria-label={t("Bisect 验证模板", "Bisect verification template")} disabled={!canMutate}
                value={bisectRecipe} onChange={(event) => { setReview(null); setBisectRecipe(event.target.value as "go_test" | "npm_test"); }}>
                <option value="go_test">Go · go test</option><option value="npm_test">NPM · npm test</option>
              </select>
            </label>
            <label>{t("最多步骤", "Maximum steps")}
              <input aria-label={t("Bisect 最多步骤", "Bisect maximum steps")} type="number" min={1} max={128} step={1}
                disabled={!canMutate} value={bisectSteps} onChange={(event) => { setReview(null); setBisectSteps(event.target.value); }} />
            </label>
            <label>{t("每步超时（秒）", "Timeout per step (seconds)")}
              <input aria-label={t("Bisect 每步超时", "Bisect step timeout")} type="number" min={1} max={900} step={1}
                disabled={!canMutate} value={bisectTimeout} onChange={(event) => { setReview(null); setBisectTimeout(event.target.value); }} />
            </label>
            <button disabled={!canMutate || !validBisectLimits} onClick={() => reviewSpec(createSpec("bisect_run", {
              sequence_id: sequence.id, expected_current: sequence.current_head,
              recipe: { name: bisectRecipe, max_steps: Number(bisectSteps), timeout_seconds: Number(bisectTimeout) },
            }))} type="button">{t("预览自动定位", "Preview automatic bisect")}</button>
            {!validBisectLimits && <small role="alert">{t("步骤必须为 1–128，单步超时必须为 1–900 秒。",
              "Use 1–128 steps and a timeout of 1–900 seconds per step.")}</small>}
            <button disabled={!canMutate} onClick={() => reviewSpec(createSpec("bisect_reset",
              { sequence_id: sequence.id }))} type="button">reset</button>
          </>}
        </div>
      </article> : <div className="git-advanced-sequence-starts">
        <div className="git-advanced-form-row">
          <input aria-label={t("Rebase upstream OID", "Rebase upstream OID")} onChange={(event) => setRebaseUpstream(event.target.value.trim())}
            placeholder="upstream OID" value={rebaseUpstream} />
          <input aria-label={t("Rebase onto OID", "Rebase onto OID")} onChange={(event) => setRebaseOnto(event.target.value.trim())}
            placeholder="onto OID" value={rebaseOnto} />
          <button disabled={!canMutate || !objectIDPattern.test(rebaseUpstream) || !objectIDPattern.test(rebaseOnto)}
            onClick={() => reviewSpec(createSpec("rebase_start", { upstream_oid: rebaseUpstream, onto_oid: rebaseOnto }))}
            type="button">rebase</button>
        </div>
        <div className="git-advanced-form-row">
          <input aria-label={t("Cherry-pick 提交 OID", "Cherry-pick commit OIDs")} onChange={(event) => setCherryCommits(event.target.value)}
            placeholder={t("逗号分隔的精确 OID", "Comma-separated exact OIDs")} value={cherryCommits} />
          <button disabled={!canMutate || exactObjects(cherryCommits).length === 0 ||
            !exactObjects(cherryCommits).every((oid) => objectIDPattern.test(oid))}
            onClick={() => reviewSpec(createSpec("cherry_pick_start", { commits: exactObjects(cherryCommits) }))}
            type="button">cherry-pick</button>
        </div>
        <div className="git-advanced-form-row">
          <input aria-label={t("Bisect good OID", "Bisect good OID")} onChange={(event) => setBisectGood(event.target.value.trim())}
            placeholder="good OID" value={bisectGood} />
          <input aria-label={t("Bisect bad OID", "Bisect bad OID")} onChange={(event) => setBisectBad(event.target.value.trim())}
            placeholder="bad OID" value={bisectBad} />
          <button disabled={!canMutate || bisectGood === bisectBad || !objectIDPattern.test(bisectGood) ||
            !objectIDPattern.test(bisectBad)} onClick={() => reviewSpec(createSpec("bisect_start",
            { good_commit: bisectGood, bad_commit: bisectBad }))} type="button">bisect</button>
        </div>
      </div>}
    </section>

    <section className="git-advanced-section">
      <h3>{t("托管 Worktree", "Managed worktrees")}</h3>
      <div className="git-advanced-form-row">
        <input aria-label={t("Worktree 名称", "Worktree name")} onChange={(event) => setWorktreeName(event.target.value.trim())}
          placeholder="review-branch" value={worktreeName} />
        <input aria-label={t("Worktree 分支", "Worktree branch")} onChange={(event) => setWorktreeBranch(event.target.value.trim())}
          placeholder="review/branch" value={worktreeBranch} />
        <button disabled={!canMutate || !worktreeName || !worktreeBranch || !objectIDPattern.test(projection.binding.head)}
          onClick={() => reviewSpec(createSpec("worktree_create", { worktree_name: worktreeName,
            branch: worktreeBranch, commit: projection.binding.head }))} type="button">create</button>
        <button disabled={!canMutate} onClick={() => reviewSpec(createSpec("worktree_prune"))}
          type="button">prune</button>
      </div>
      {projection.worktrees.map((worktree) => <article className="git-advanced-worktree" key={worktree.id}>
        <strong>{worktree.name}</strong> <StatusBadge status={worktree.present ?
          worktree.locked ? "locked" : "present" : "removed"} />
        <small>{worktree.branch} · {shortID(worktree.head)} · path sha256 {worktree.path_sha256}</small>
        <div className="git-advanced-actions">
          <button disabled={!canMutate || worktree.locked || !worktree.present}
            onClick={() => reviewSpec(createSpec("worktree_lock", { worktree_id: worktree.id,
              worktree_name: worktree.name, lock_reason: "Locked from Desktop" }))} type="button">lock</button>
          <button disabled={!canMutate || !worktree.locked || !worktree.present}
            onClick={() => reviewSpec(createSpec("worktree_unlock", { worktree_id: worktree.id,
              worktree_name: worktree.name }))} type="button">unlock</button>
          <button disabled={!canMutate || !worktree.present || worktree.locked}
            onClick={() => reviewSpec(createSpec("worktree_remove", { worktree_id: worktree.id,
              worktree_name: worktree.name }))} type="button">remove</button>
        </div>
      </article>)}
    </section>

    {review && <PreviewEvidence preview={review.preview} />}
    {review?.operation && review.approval && <section className="git-advanced-approval">
      <h3><ShieldCheck aria-hidden="true" size={16} />{t("精确一次审批", "Exact one-time approval")}</h3>
      <p>{t("先在审批页核准这份不可变 preview，再返回执行；执行前会重新校验仓库、上游、权限和租约。",
        "Approve this immutable preview in Approvals, then return to execute. Repository, upstream, permission, and lease are revalidated first.")}</p>
      <code>{review.approval.ID}</code>
      <div className="git-advanced-actions">
        <button onClick={onOpenApprovals} type="button">{t("打开审批", "Open approvals")}</button>
        <button disabled={execute.isPending} onClick={() => execute.mutate()} type="button">
          {execute.isPending && <LoaderCircle aria-hidden="true" className="spin" size={14} />}
          {t("执行已审批操作", "Execute approved operation")}
        </button>
      </div>
    </section>}
    {mutationError && <ErrorState error={mutationError} />}

    <section className="git-advanced-section">
      <h3>{t("已保存的 Git 操作", "Saved Git operations")}</h3>
      {projection.operations.length === 0 ? <small>{t("暂无操作", "No operations")}</small> :
        projection.operations.map((operation) => <details key={operation.id}>
          <summary><StatusBadge status={operation.status} /> {operation.operation} · {shortID(operation.id)} · {formatDate(operation.created_at)}</summary>
          <PreviewEvidence preview={operation.preview} compact />
          {operation.receipt && <div className="git-advanced-receipt">
            <strong>{t("收据", "Receipt")}: {operation.receipt.status}</strong>
            <small>checkpoint {operation.receipt.checkpoint_id ?? "—"}</small>
            <small>pre {operation.receipt.pre_binding.status_sha256}</small>
            <small>post {operation.receipt.post_binding.status_sha256}</small>
            {operation.receipt.error_code && <small>{operation.receipt.error_code}: {operation.receipt.error_summary}</small>}
          </div>}
        </details>)}
    </section>
  </section>;
}

function PreviewEvidence({ preview, compact = false }: {
  preview: GitAdvancedPreviewView;
  compact?: boolean;
}) {
  const { t } = useLocale();
  return <section className="git-advanced-preview">
    <h3>{t("操作预览", "Operation preview")} · {preview.operation}</h3>
    <p>{preview.summary}</p>
    <code>{preview.id}</code>
    {preview.blocked_reasons.length > 0 && <ul className="inline-warning">
      {preview.blocked_reasons.map((reason) => <li key={reason}>{reason}</li>)}
    </ul>}
    {!compact && <>
      {preview.files.length > 0 && <ul>{preview.files.map((file) =>
        <li key={`${file.change}:${file.path}`}>{file.destructive ? "⚠ " : ""}{file.change}: {formatWorkspacePathLabel(file.path)}</li>)}</ul>}
      {preview.hunks.map((hunk) => <pre key={hunk.id}>{hunk.patch}</pre>)}
      <small>{t("恢复", "Recovery")}: {preview.recovery.required ?
        `${preview.recovery.checkpoint_level ?? "checkpoint"} / ${preview.recovery.restore_action ?? "restore"}` :
        t("不需要", "not required")}</small>
      {preview.recovery.incomplete_reasons.map((reason) => <small key={reason}>{reason}</small>)}
    </>}
  </section>;
}
