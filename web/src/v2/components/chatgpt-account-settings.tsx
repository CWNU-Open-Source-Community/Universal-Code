import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Copy, ExternalLink, LoaderCircle, LogOut, RefreshCw } from "lucide-react";
import type { ProviderDefinitionView } from "../../api/types";
import type { APIClient } from "../../api/client";
import { chatGPTAuthProtocol, validChatGPTStartURL, type ChatGPTAccountView,
  type ChatGPTAuthStatusView } from "../../api/chatgpt-auth";

import { useLocale, type LocaleText } from "../../lib/locale";

export type V2ReadyModelSource = Pick<ProviderDefinitionView, "id" | "default_model">;
export const chatGPTStatusQueryKey = ["v2", "models", "chatgpt-auth"] as const;
const routesQueryKey = ["v2", "models", "available-routes"] as const;

export function useChatGPTAuthStatus(client: APIClient) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: chatGPTStatusQueryKey,
    queryFn: ({ signal }) => client.chatGPTAuthStatus(signal),
    enabled: Boolean(client.hasChatGPTAuth),
    retry: false,
    refetchInterval: (query) => query.state.error === null && query.state.data?.login?.state === "waiting" ? 1_000 : false,
    refetchIntervalInBackground: true,
  });
  const loginState = query.data?.login?.state;
  const loginID = query.data?.login?.id;
  useEffect(() => {
    if (loginState !== "succeeded") return;
    void queryClient.invalidateQueries({ queryKey: routesQueryKey });
    void queryClient.invalidateQueries({ queryKey: ["models", "availability"] });
    void queryClient.invalidateQueries({ predicate: (entry) => entry.queryKey[0] === "v2" &&
      entry.queryKey[1] === "thread" && entry.queryKey[3] === "model-route" });
  }, [loginID, loginState, queryClient]);
  return query;
}

const accountStateLabel = (t: LocaleText): Record<ChatGPTAccountView["state"], string> => ({
  connected: t("已连接", "Connected"), signed_out: t("已退出", "Signed out"), verification_pending: t("等待验证", "Awaiting verification"), reauthorization_required: t("需要重新登录", "Sign in again"),
});

export function V2ChatGPTAccountSettings({ client, query, onExit, prepareForDraft = false, onReady }: {
  client: APIClient;
  query: ReturnType<typeof useChatGPTAuthStatus>;
  onExit: () => void;
  prepareForDraft?: boolean;
  onReady?: (source: V2ReadyModelSource) => void;
}) {
  const queryClient = useQueryClient();
  const { t } = useLocale();
  const status = client.hasChatGPTAuth && !query.isError ? query.data : undefined;
  const [selectedID, setSelectedID] = useState("");
  const [selectedModel, setSelectedModel] = useState("");
  const [working, setWorking] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const mounted = useRef(true);
  const operation = useRef(false);
  const backRef = useRef<HTMLButtonElement>(null);
  const accounts = status?.accounts ?? [];
  const selected = accounts.find((account) => account.id === selectedID) ??
    accounts.find((account) => account.state === "connected") ?? accounts[0];
  const model = selected?.models.find((item) => item.id === selectedModel)?.id ?? selected?.models[0]?.id ?? "";
  const waiting = status?.login?.state === "waiting";
  const enabled = Boolean(client.hasChatGPTAuth && status?.available && !query.isError);
  const locked = Boolean(working);

  useEffect(() => {
    mounted.current = true;
    backRef.current?.focus();
    return () => { mounted.current = false; };
  }, []);

  const loginID = status?.login?.account_id;
  const loginState = status?.login?.state;
  useEffect(() => {
    if (loginState !== "succeeded") return;
    if (loginID) { setSelectedID(loginID); setSelectedModel(""); }
  }, [loginID, loginState]);

  const refreshRoutes = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: routesQueryKey }),
    queryClient.invalidateQueries({ queryKey: ["models", "availability"] }),
    queryClient.invalidateQueries({ predicate: (entry) => entry.queryKey[0] === "v2" &&
      entry.queryKey[1] === "thread" && entry.queryKey[3] === "model-route" }),
  ]);

  const act = async (name: string, action: () => Promise<ChatGPTAuthStatusView>) => {
    if (!enabled || operation.current) return;
    operation.current = true;
    setWorking(name); setError(""); setNotice("");
    await queryClient.cancelQueries({ queryKey: chatGPTStatusQueryKey });
    try {
      const result = await action();
      queryClient.setQueryData(chatGPTStatusQueryKey, result);
      await refreshRoutes();
    } catch (reason) {
      if (mounted.current) setError(reason instanceof Error ? reason.message : t("ChatGPT 账户操作失败，请重试。", "The ChatGPT account action failed. Try again."));
    } finally {
      await query.refetch();
      operation.current = false;
      if (mounted.current) setWorking("");
    }
  };

  const login = (accountID?: string) => act("login", () => client.startChatGPTLogin({
    version: chatGPTAuthProtocol, confirm: true, ...(accountID ? { account_id: accountID } : {}),
  }));

  const checkModel = async () => {
    if (!enabled || waiting || operation.current || selected?.state !== "connected" || !model || !client.hasModelControl) return;
    const provider = selected.provider;
    operation.current = true;
    setWorking("check"); setError(""); setNotice("");
    try {
      const result = await client.qualifyModelHarness({ version: "model_harness_qualification.v1",
        provider, model, confirm_qualification: true });
      if (!mounted.current) return;
      if (result.status !== "qualified" || result.failure_reason !== "none" || !result.harness.root_eligible) {
        setError(t(`模型检查未通过（${result.failure_reason}）。请检查账户状态后重试。`, `Model check failed (${result.failure_reason}). Check account status and try again.`));
        return;
      }
      const catalog = await client.availableModelRoutes();
      queryClient.setQueryData(routesQueryKey, catalog);
      if (!mounted.current) return;
      if (!catalog.routes.some((route) => route.provider_id === provider && route.model === model && route.selectable)) {
        setError(t("模型检查已完成，当前模型目录尚未确认可用，请稍后重试。", "The model check finished, but availability in the model catalog is not confirmed yet. Try again later."));
        return;
      }
      setNotice(t("模型检查已通过，可在对话中选择这个账户的模型。", "The model check passed. You can select this account's models in conversations."));
      if (prepareForDraft) onReady?.({ id: provider, default_model: model });
    } catch (reason) {
      if (mounted.current) setError(reason instanceof Error ? reason.message : t("模型检查失败，请重试。", "The model check failed. Try again."));
    } finally {
      await query.refetch();
      await refreshRoutes();
      operation.current = false;
      if (mounted.current) setWorking("");
    }
  };

  const copyStartURL = async () => {
    const url = status?.login?.start_url;
    if (!waiting || !validChatGPTStartURL(url)) return;
    try {
      await navigator.clipboard.writeText(url);
      setNotice(t("登录链接已复制，请在系统浏览器中打开。", "Sign-in link copied. Open it in your system browser."));
    } catch { setError(t("无法复制链接，请从下方选中本地登录地址后复制。", "Could not copy the link. Select and copy the local sign-in URL below.")); }
  };

  return <section className="v2-chatgpt-settings" aria-labelledby="v2-chatgpt-title">
    <button aria-label={t("返回模型选择", "Back to model selection")} className="v2-model-custom-back" onClick={onExit} ref={backRef} type="button">
      <ArrowLeft aria-hidden="true" size={16} />{t("返回模型", "Back to models")}</button>
    <header className="v2-model-catalog-heading">
      <h1 id="v2-chatgpt-title">{t("ChatGPT 账户", "ChatGPT account")}</h1>
      <p>{t("使用 ChatGPT 账户登录，按当前订阅与用量使用模型。登录信息由操作系统凭据库保护。", "Sign in with ChatGPT to use models under your subscription and usage limits. Your operating system's credential store protects sign-in information.")}</p>
    </header>
    <div className="v2-settings-card v2-chatgpt-card">
      <header><div><strong>Using ChatGPT plan</strong><p>{t("使用 ChatGPT 订阅", "Use a ChatGPT subscription")}</p></div>
        <div className="v2-provider-actions v2-chatgpt-actions">
          <button disabled={!enabled || locked || waiting} onClick={() => void login()} type="button">Continue with ChatGPT</button>
          <button disabled={!enabled || locked} onClick={() => void act("usage", () => client.openChatGPTUsage({
            version: chatGPTAuthProtocol, confirm: true,
          }))} type="button"><ExternalLink aria-hidden="true" size={14} />Manage usage<span className="sr-only">{t("，管理用量", ", manage usage")}</span></button>
        </div>
      </header>
      {!client.hasChatGPTAuth ? <p className="v2-chatgpt-help">{t("当前连接不支持 ChatGPT 账户登录，请使用支持此功能的桌面版本。", "This connection does not support ChatGPT sign-in. Use a desktop version that supports it.")}</p>
        : query.isError ? <div className="v2-chatgpt-help"><p role="alert">{t("无法读取 ChatGPT 账户状态，请重新读取后继续。", "Could not read ChatGPT account status. Reload it to continue.")}</p>
          <button className="v2-setting-link" onClick={() => void query.refetch()} type="button">{t("重新读取账户", "Reload accounts")}</button></div>
          : !status ? <p className="v2-chatgpt-help" role="status">{t("正在读取账户…", "Loading accounts…")}</p>
            : !status.available ? <p className="v2-chatgpt-help" role="alert">{t("操作系统凭据库当前不可用，请恢复后重新读取。", "The operating system's credential store is unavailable. Restore it and reload.")}</p> : null}
      {status?.login && <div className="v2-chatgpt-login" role={status.login.state === "failed" ? "alert" : "status"}>
        <p>{waiting ? <><LoaderCircle aria-hidden="true" className="v2-chatgpt-spinner" size={15} />{t("等待 ChatGPT 登录…", "Waiting for ChatGPT sign-in…")}</>
          : status.login.state === "succeeded" ? t("ChatGPT 登录成功，账户已保存。", "Signed in to ChatGPT. Account saved.")
            : status.login.state === "cancelled" ? t("登录已取消。", "Sign-in cancelled.") : t("ChatGPT 登录失败，请重试。", "ChatGPT sign-in failed. Try again.")}</p>
        {status.login.message && <p>{status.login.message}</p>}
        {waiting && <>
          <p>{t("请在系统浏览器完成登录。返回模型列表后仍可查看登录状态。", "Complete sign-in in your system browser. Sign-in status remains available from the model list.")}</p>
          <div className="v2-provider-actions v2-chatgpt-actions">
            {validChatGPTStartURL(status.login.start_url) && <button disabled={locked} onClick={() => void copyStartURL()} type="button">
              <Copy aria-hidden="true" size={14} />{t("复制登录链接", "Copy sign-in link")}</button>}
            <button disabled={locked || !enabled} onClick={() => void act("cancel", () => client.cancelChatGPTLogin({
              version: chatGPTAuthProtocol, attempt_id: status.login!.id, confirm: true,
            }))} type="button">{t("取消登录", "Cancel sign-in")}</button>
          </div>
          {validChatGPTStartURL(status.login.start_url) && <code className="v2-chatgpt-local-url">{status.login.start_url}</code>}
        </>}
      </div>}
      {status && <div className="v2-chatgpt-accounts">
        <h2>{t("已保存的账户", "Saved accounts")}</h2>
        {accounts.length ? <>
          <label>{t("账户", "Account")}<select aria-label={t("ChatGPT 账户", "ChatGPT account")} disabled={locked} value={selected?.id ?? ""}
            onChange={(event) => { setSelectedID(event.target.value); setSelectedModel(""); setNotice(""); setError(""); }}>
            {accounts.map((account) => <option key={account.id} value={account.id}>
              {account.label || account.email || t("ChatGPT 账户", "ChatGPT account")} · {accountStateLabel(t)[account.state]}
            </option>)}
          </select></label>
          {selected && <>
            <p className="v2-chatgpt-help">{selected.email && <span>{selected.email} · </span>}{accountStateLabel(t)[selected.state]}</p>
            <label>{t("模型", "Models")}<select aria-label={t("ChatGPT 模型", "ChatGPT models")} disabled={locked || selected.state !== "connected" || !selected.models.length}
              value={model} onChange={(event) => { setSelectedModel(event.target.value); setNotice(""); setError(""); }}>
              {selected.models.length ? selected.models.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)
                : <option value="">{t("登录后刷新模型", "Sign in to refresh models")}</option>}
            </select></label>
            <div className="v2-provider-actions v2-chatgpt-actions">
              <button disabled={!enabled || locked || waiting || !["connected", "verification_pending"].includes(selected.state)}
                onClick={() => void act("models", () => client.refreshChatGPTModels({
                  version: chatGPTAuthProtocol, account_id: selected.id, confirm: true,
                }))} type="button"><RefreshCw aria-hidden="true" size={14} />{t("刷新模型", "Refresh models")}</button>
              {selected.state !== "signed_out" && <button className="danger-quiet" disabled={!enabled || locked || waiting}
                onClick={() => void act("logout", () => client.logoutChatGPTAccount({
                  version: chatGPTAuthProtocol, account_id: selected.id, confirm: true,
                }))} type="button"><LogOut aria-hidden="true" size={14} />{t("退出账户", "Sign out")}</button>}
              {selected.state !== "connected" && <button aria-label={t("Continue with ChatGPT，重新登录所选账户", "Continue with ChatGPT, sign in to the selected account again")} disabled={!enabled || locked || waiting}
                  onClick={() => void login(selected.id)} type="button">Continue with ChatGPT</button>}
              <span />
              <button className="primary" disabled={!enabled || locked || waiting || !client.hasModelControl || selected.state !== "connected" || !model}
                onClick={() => void checkModel()} type="button">{working === "check" ? t("正在检查模型…", "Checking model…") : prepareForDraft ? t("检查并返回任务", "Check and return to task") : t("检查模型", "Check model")}</button>
            </div>
            <p className="v2-chatgpt-help">{t("检查通过后，即可在任务中使用此模型。", "After the check passes, this model can be used in tasks.")}</p>
          </>}
        </> : <p className="v2-chatgpt-help">{t("还没有保存的 ChatGPT 账户。完成登录后，账户会显示在这里。", "No saved ChatGPT accounts. Accounts appear here after sign-in.")}</p>}
      </div>}
    </div>
    {locked && working !== "check" && <p role="status">{t("正在处理账户操作…", "Processing account action…")}</p>}
    {error && <p className="v2-chatgpt-error" role="alert">{error}</p>}
    {notice && <p role="status">{notice}</p>}
  </section>;
}
