import { useCallback, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { MessagesSquare } from "lucide-react";
import type { APIClient } from "../api/client";
import type { MessageView, RunDetailView, SessionDetailView } from "../api/types";
import { usePagedResource } from "../hooks/use-paged-resource";
import { formatDate, formatNumber, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { diagnosticVocabulary } from "../lib/vocabulary";
import { EmptyState, ErrorState, KeyValue, LoadMoreButton, LoadingState, StatusBadge } from "./common";
import { LifecycleStatusBadge } from "./lifecycle-status";
import { SessionComposer, SessionSteeringQueue, type SessionComposerStatus } from "./session-composer";
import { SafeMarkdown } from "./safe-markdown";
import "./resource-workspace-navigation.css";

export function SessionWorkspace({ client, sessionID, onOpenPlugins }: {
  client: APIClient;
  sessionID: string;
  onOpenPlugins?: () => void;
}) {
  const { t } = useLocale();
  const [inputOpen, setInputOpen] = useState(false);
  const [inputStatus, setInputStatus] = useState<SessionComposerStatus>({ pending: false, error: null });
  const showInputStatus = useCallback((status: SessionComposerStatus) => {
    setInputStatus(status);
    if (status.error) setInputOpen(true);
  }, []);
  const detailQuery = useQuery({
    queryKey: ["session", sessionID],
    queryFn: ({ signal }) => client.get<SessionDetailView>(`/sessions/${encodeURIComponent(sessionID)}`, {}, signal),
    enabled: Boolean(sessionID),
  });
  const messagesQuery = usePagedResource<MessageView>(client, ["session", sessionID, "messages"],
    `/sessions/${encodeURIComponent(sessionID)}/messages`, { limit: 100, include_compacted: true }, Boolean(sessionID));
  const messages = useMemo(() => messagesQuery.data?.pages.flatMap((page) => page.items) ?? [], [messagesQuery.data]);
  const contextTokens = useMemo(() => messages.filter((message) => !message.compacted)
    .reduce((total, message) => total + message.token_estimate, 0), [messages]);
  const boundRunID = detailQuery.data?.run?.id ?? "";
  const runQuery = useQuery({
    queryKey: ["run", boundRunID],
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(boundRunID)}`, {}, signal),
    enabled: Boolean(boundRunID) && (client.hasSessionMessages ||
      client.hasSessionSteeringControl || client.hasPlanDelivery),
  });

  if (!sessionID) {
    return <div className="workspace-empty"><MessagesSquare aria-hidden="true" size={24} /><h1>{t("选择一个 Session 诊断", "Select a Session diagnostic")}</h1></div>;
  }
  if (detailQuery.isLoading) {
    return <LoadingState label={t("加载 Session 诊断", "Loading Session diagnostic")} />;
  }
  if (detailQuery.isError || !detailQuery.data) {
    return <ErrorState error={detailQuery.error} />;
  }
  const detail = detailQuery.data;
  const boundRun = runQuery.data?.run ?? detail.run ?? null;

  return (
    <div className="workspace-view">
      <header className="workspace-header">
        <div>
          <div className="workspace-kicker">{t(...diagnosticVocabulary.session)} {shortID(detail.session.id)}</div>
          <h1>{detail.session.title}</h1>
          <div className="header-meta"><span>{t("上下文记录状态：", "Context record state: ")}<LifecycleStatusBadge status={detail.session.status} kind="session" /></span><span>{detail.session.route}</span></div>
        </div>
      </header>
      <aside className="inline-warning" role="note">
        <strong>{t("高级诊断 / 兼容视图", "Advanced diagnostics / compatibility view")}</strong>
        {" · "}
        <span>{t(
          "本次执行的上下文记录。继续交流请返回对话。",
          "Context records for this execution. Return to the conversation to continue chatting.")}</span>
      </aside>
      <div className="session-summary">
        <dl className="detail-grid">
          <KeyValue label={t("工作区", "Workspace")} value={detail.session.workspace_id} />
          <KeyValue label={t("绑定的 Run", "Bound Run")} value={detail.run ? shortID(detail.run.id) : "-"} />
          <KeyValue label={t("创建时间", "Created")} value={formatDate(detail.session.created_at)} />
          <KeyValue label={t("更新时间", "Updated")} value={formatDate(detail.session.updated_at)} />
        </dl>
      </div>
      <div className="workspace-content session-content">
        <div className="section-heading"><h2>{t("消息", "Messages")}</h2><span>{formatNumber(messages.length)}</span></div>
        {messagesQuery.isLoading && <LoadingState />}
        {messagesQuery.isError && <ErrorState error={messagesQuery.error} />}
        {!messagesQuery.isLoading && !messagesQuery.isError && messages.length === 0 && <EmptyState>暂无消息</EmptyState>}
        <div className="message-list">
          {messages.map((message) => (
            <article className={`message-row role-${message.role}`} key={message.id}>
              <header><strong>{message.role}</strong><StatusBadge status={message.source_kind} /><span>{formatNumber(message.token_estimate)} {t("令牌", "tokens")}</span>{message.compacted && <StatusBadge status="compacted" />}<time dateTime={message.created_at}>{formatDate(message.created_at)}</time></header>
              {message.role === "assistant" ?
                <SafeMarkdown>{message.content}</SafeMarkdown> : <p>{message.content}</p>}
            </article>
          ))}
        </div>
        <LoadMoreButton hasNextPage={Boolean(messagesQuery.hasNextPage)} isFetching={messagesQuery.isFetchingNextPage} onClick={() => void messagesQuery.fetchNextPage()} />
      </div>
      <SessionSteeringQueue client={client} diagnosticSession sessionID={sessionID}
        run={boundRun}
        state={runQuery.data?.operator_steering ?? null} />
      {boundRun && <div className="resource-session-run-state" role="status"><span>{t("绑定执行记录状态", "Bound execution record state")}</span>
        {runQuery.isError ? <span>{t("状态暂不可用，无法刷新绑定运行", "State temporarily unavailable; could not refresh the bound Run")}</span>
          : <LifecycleStatusBadge status={boundRun.status} />}</div>}
      {boundRun && client.hasSessionMessages && <details className="resource-workspace-input" open={inputOpen}
        onToggle={(event) => setInputOpen(event.currentTarget.open)}><summary><span>{t("向此会话补充输入（高级）", "Add input to this Session (advanced)")}</span>
          <span role="status">{inputStatus.pending ? t(" · 输入请求处理中", " · Input in progress") : inputStatus.error ? t(" · 输入未完成，请查看原因", " · Input needs attention") : ""}</span></summary>
        <p>{t("输入目标：此上下文绑定的执行。发送时会核对执行状态与权限；继续整个任务请返回对话。", "Input target: the execution bound to this context. Sending checks its state and permissions. Return to the conversation to continue the whole task.")}</p>
      <SessionComposer client={client} contextPartial={Boolean(messagesQuery.hasNextPage)} diagnosticSession onStatusChange={showInputStatus}
        contextTokens={contextTokens} key={sessionID} onOpenPlugins={onOpenPlugins}
        phase={runQuery.data?.mode.phase} run={boundRun} sessionID={sessionID}
        workspaceID={detail.session.workspace_id ?? ""} />
      </details>}
    </div>
  );
}
