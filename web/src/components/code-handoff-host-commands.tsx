import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { CodeHandoffView } from "../api/types";
import { useLocale } from "../lib/locale";
import { ErrorState, LoadingState, StatusBadge } from "./common";
import { SavedHostCommandOutput } from "./saved-host-command-output";

type Commands = NonNullable<CodeHandoffView["host_commands"]>;
type Command = Commands["items"][number];

export function CodeHandoffHostCommands({ client, commands }: { client: APIClient; commands: Commands }) {
  const { t } = useLocale();
  return <section aria-label={t("宿主命令执行记录", "Recorded host commands")}>
    <h3>{t("宿主命令执行记录", "Recorded host commands")}</h3>
    {commands.items.length === 0 && <p>{t("尚无宿主命令提案。", "No host command proposals recorded.")}</p>}
    {commands.items.map((command) => <HostCommand key={`${command.proposal_id}:${command.result_id ?? "pending"}`}
      client={client} command={command} />)}
    {commands.truncated && <p>{t("这里只展示最近 20 条提案。", "Only the latest 20 proposals are shown.")}</p>}
  </section>;
}

function HostCommand({ client, command }: { client: APIClient; command: Command }) {
  const { t } = useLocale();
  const [opened, setOpened] = useState(false);
  const query = useQuery({
    queryKey: ["run", command.run_id, "handoff-host-output", command.proposal_id, command.result_id, command.content_sha256],
    enabled: opened && Boolean(command.receipt),
    queryFn: async ({ signal }) => {
      const detail = await client.hostCommandProposal(command.run_id, command.proposal_id, signal);
      if (detail.run_id !== command.run_id || detail.id !== command.proposal_id ||
        detail.session_id !== command.session_id || detail.workspace_id !== command.workspace_id ||
        detail.spec_fingerprint !== command.spec_fingerprint || detail.result?.id !== command.result_id ||
        detail.result?.source_ref !== command.source_ref || detail.result?.content_sha256 !== command.content_sha256 ||
        detail.receipt?.request_id !== command.receipt?.request_id) {
        throw new Error(t("已保存输出与这条命令收据不匹配。", "Saved output does not match this command receipt."));
      }
      return detail;
    },
    retry: false,
  });
  const receipt = command.receipt;
  return <article className="command-proposal-row">
    <h4>{command.purpose}</h4>
    <p>{t("工作目录", "Working directory")}：<code>{command.working_directory}</code></p>
    {receipt ? <>
      <p><StatusBadge status={command.result_status!} label={t("已记录执行结果", "Execution result recorded")} />
        {" · "}{t("退出码", "Exit code")} {receipt.exit_code}</p>
      {receipt.timed_out && <p>{t("命令已超时。", "The command timed out.")}</p>}
      {receipt.cancelled && <p>{t("命令已取消。", "The command was cancelled.")}</p>}
      {(receipt.stdout_truncated || receipt.stderr_truncated || receipt.output_limit_exceeded) &&
        <p>{t("输出达到保存上限，当前显示已保存的部分。", "Output reached the storage limit. The saved portion is displayed here.")}</p>}
      <button className="compact-command"
        onClick={() => setOpened((value) => !value)} type="button">
        {opened ? t("收起已保存输出", "Hide saved output") : t("查看已保存输出", "Read saved output")}</button>
      {opened && <div className="command-proposal-evidence host-command-outcome">
        {query.isLoading && <LoadingState />}
        {query.isError && <><ErrorState error={query.error} /><button onClick={() => void query.refetch()} type="button">
          {t("重试读取输出", "Retry reading output")}</button></>}
        {query.isSuccess && <SavedHostCommandOutput detail={query.data} />}
      </div>}
    </> : <p>{command.review_decision === "deny" ? t("提案已拒绝，没有执行结果。", "Proposal denied; no execution result.") :
      t("命令结果待确认，请核对原执行记录。", "The command result needs confirmation. Check the original execution record.")}</p>}
    <details className="saved-host-output-evidence"><summary>{t("执行详情", "Execution details")}</summary>
      <p>{t("提案", "Proposal")}：<code>{command.proposal_id}</code></p>
      {receipt && <>
        <p>{t("结果 / 执行记录", "Result / execution record")}：<code>{command.result_id}</code>{" · "}<code>{receipt.request_id}</code></p>
        <p>{t("开始 / 结束", "Started / completed")}：{receipt.started_at} / {receipt.completed_at}</p>
      </>}
    </details>
  </article>;
}
