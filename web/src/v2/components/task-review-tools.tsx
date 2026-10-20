import { useLocale } from "../../lib/locale";
import { lazy, Suspense, useState } from "react";
import { Camera, GitBranch, GitPullRequest, Layers } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { GitAdvancedReviewResultView, GitHubReviewWriteReviewResultView } from "../../api/types";

const BatchDeliveriesPanel = lazy(() => import("../../components/run-projections")
  .then((module) => ({ default: module.BatchDeliveriesPanel })));
const GitAdvancedPanel = lazy(() => import("../../components/git-advanced-panel")
  .then((module) => ({ default: module.GitAdvancedPanel })));
const GitHubReviewPanel = lazy(() => import("../../components/github-review-panel")
  .then((module) => ({ default: module.GitHubReviewPanel })));
const UIEvidencePanel = lazy(() => import("../../components/ui-evidence-panel")
  .then((module) => ({ default: module.UIEvidencePanel })));
const ApprovalPanel = lazy(() => import("../../components/approval-panel")
  .then((module) => ({ default: module.ApprovalPanel })));

type Tool = "batch" | "git-advanced" | "github-review" | "ui-evidence";


export interface TaskReviewToolReviews {
  git: GitAdvancedReviewResultView | null;
  github: GitHubReviewWriteReviewResultView | null;
}

export interface TaskReviewToolMemory {
  generation: number;
  client?: APIClient;
  threadID?: string;
  runID?: string;
  reviews?: TaskReviewToolReviews;
}

export function TaskReviewTools({ client, runID, threadID, onOpenDelivery, retainedReviews,
  onGitReviewChange, onGithubReviewChange, initialTool }: {
  client: APIClient; runID: string; threadID: string; onOpenDelivery: () => void;
  retainedReviews?: TaskReviewToolReviews;
  onGitReviewChange: (review: GitAdvancedReviewResultView | null) => void;
  onGithubReviewChange: (review: GitHubReviewWriteReviewResultView | null) => void;
  initialTool?: "github-review";
}) {
  const { t } = useLocale();
  const tools = [
  { value: "batch", title: t("批量交付", "Batch delivery"), description: t("独立验收子任务、要求返工、按顺序合并交付。", "Accept subtasks individually, request rework and merge deliveries in order."), icon: Layers },
  { value: "git-advanced", title: t("高级 Git", "Advanced Git"), description: t("审阅局部暂存、Stash、Rebase 等精确仓库操作。", "Review partial staging, stash, rebase and other repository operations."), icon: GitBranch },
  { value: "github-review", title: t("GitHub 审阅", "GitHub review"), description: t("读取 PR 与 CI 证据，审阅并审批 GitHub 写回。", "Read PR and CI evidence, review and approve GitHub updates."), icon: GitPullRequest },
  { value: "ui-evidence", title: t("UI 取证", "UI evidence"), description: t("查看真实浏览器证据，审阅并启动精确验证清单。", "View real browser evidence, review and start specific verification steps."), icon: Camera },
] as const;
  const [selected, setSelected] = useState<Tool | null>(initialTool ?? null);
  const [showApprovals, setShowApprovals] = useState(false);
  const [gitReview, setGitReview] = useState(retainedReviews?.git ?? null);
  const [githubReview, setGithubReview] = useState(retainedReviews?.github ?? null);
  return <section className="v2-review-tools" aria-label={t("所选执行的交付工具", "Delivery tools for the selected run")}>
    <p>{t("选择工具，查看所选执行的交付状态或准备下一步操作。执行前会核对能力、审批与运行条件。", "Choose a tool to view delivery status or prepare the next action. Capabilities, approvals and execution conditions are checked before running.")}</p>
    <details><summary>{t("工具使用的执行记录", "Run used by the tool")}</summary><code>{runID}</code></details>
    <div className="v2-review-tool-picker" role="group" aria-label={t("选择交付工具", "Choose delivery tool")}>
      {tools.map(({ value, title, description, icon: Icon }) => <button key={value} type="button"
        aria-pressed={selected === value && !showApprovals} onClick={() => { setSelected(value); setShowApprovals(false); }}>
        <span><Icon aria-hidden="true" size={17} /><strong>{title}</strong></span><small>{description}</small>
      </button>)}
    </div>
    <div className="v2-review-tool-content" aria-live="polite">
      <Suspense fallback={<p role="status">{t("正在加载交付工具…", "Loading delivery tool…")}</p>}>
        {showApprovals ? <>
          <button className="compact-command" type="button" onClick={() => setShowApprovals(false)}>{t("返回所选工具", "Back to selected tool")}</button>
          <ApprovalPanel client={client} runID={runID} threadID={threadID} />
        </> : <>
          {selected === "batch" && <BatchDeliveriesPanel client={client} runID={runID} />}
          {selected === "git-advanced" && <GitAdvancedPanel client={client} runID={runID}
            retainedReview={gitReview} onRetainedReviewChange={(review) => { setGitReview(review); onGitReviewChange(review); }}
            onOpenApprovals={() => setShowApprovals(true)} />}
          {selected === "github-review" && <GitHubReviewPanel client={client} runID={runID}
            retainedReview={githubReview} onRetainedReviewChange={(review) => { setGithubReview(review); onGithubReviewChange(review); }}
            onOpenApprovals={() => setShowApprovals(true)} onOpenDelivery={onOpenDelivery} />}
          {selected === "ui-evidence" && <UIEvidencePanel client={client} runID={runID} threadID={threadID} />}
          {!selected && <p>{t("选择工具后才会读取对应状态。切换执行记录后，请重新选择工具并审阅。", "Status is loaded after selecting a tool. Select and review the tool again after switching runs.")}</p>}
        </>}
      </Suspense>
    </div>
  </section>;
}
