import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { GitPullRequest } from "lucide-react";
import type { APIClient } from "../api/client";
import { discoverPullRequest } from "../api/task-delivery";
import { useLocale } from "../lib/locale";
import { TaskPRFollowup } from "../v2/components/task-pr-followup";

/** Event automations use the same PR/CI follower as the task's delivery view. */
export function AutomationPRFollowup({ client, threadID }: { client: APIClient; threadID: string }) {
  const { t } = useLocale();
  const [connectionID, setConnectionID] = useState("");
  const [prNumber, setPRNumber] = useState(0);
  const connections = useQuery({ queryKey: ["github-review", "connections"],
    queryFn: ({ signal }) => client.githubReviewConnections(false, signal), enabled: client.hasGitHubReviewControl });
  const discovery = useQuery({ queryKey: ["thread", threadID, "pr-discovery", connectionID, ""],
    queryFn: ({ signal }) => discoverPullRequest(client, threadID, connectionID, "", signal),
    enabled: client.hasGitHubReviewControl && Boolean(connectionID) });
  const selected = discovery.data?.pull_requests.find((item) => item.number === prNumber) ?? discovery.data?.pull_requests[0];
  return <section className="automation-pr-target">
    <header><GitPullRequest aria-hidden="true" size={16} /><strong>{t("PR 与 CI 反馈", "PR and CI feedback")}</strong></header>
    {!client.hasGitHubReviewControl ? <p>{t("当前连接未启用 GitHub 控制。请在连接与环境中接入 GitHub。", "GitHub control is unavailable. Connect GitHub in Connections and environment.")}</p> : <>
      <div className="automation-pr-fields">
        <label>{t("GitHub 连接", "GitHub connection")}<select value={connectionID} onChange={(event) => { setConnectionID(event.target.value); setPRNumber(0); }}>
          <option value="">{t("选择仓库连接", "Select a repository connection")}</option>
          {connections.data?.map((item) => <option key={item.connection.id} value={item.connection.id}>{item.connection.repository.full_name}</option>)}
        </select></label>
        {connectionID && <label>{t("已有 PR", "Existing PR")}<select value={selected?.number || 0} onChange={(event) => setPRNumber(Number(event.target.value))}>
          <option value={0}>{discovery.isLoading ? t("正在读取 PR…", "Loading PRs…") : t("选择 PR", "Select a PR")}</option>
          {discovery.data?.pull_requests.map((item) => <option key={item.number} value={item.number}>#{item.number} {item.title.text}</option>)}
        </select></label>}
      </div>
      {discovery.isSuccess && !discovery.data.pull_requests.length && <p>{t("此任务当前分支没有 PR。请先在任务中创建或选择 PR。", "This task’s branch has no PR. Create or select a PR in the task first.")}</p>}
      {connections.error && <p role="alert">{connections.error.message}</p>}
      {discovery.error && <p role="alert">{discovery.error.message}</p>}
      <TaskPRFollowup client={client} threadID={threadID} connectionID={connectionID} pullRequest={selected?.number || 0}
        canStart={Boolean(connectionID && selected?.state === "open" && !selected.merged && discovery.isSuccess && !discovery.isFetching)} />
    </>}
  </section>;
}
