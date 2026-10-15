import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Copy, ExternalLink, LoaderCircle, LogOut, RefreshCw } from "lucide-react";
import type { ProviderDefinitionView } from "../../api/types";
import type { APIClient } from "../../api/client";
import { chatGPTAuthProtocol, validChatGPTStartURL, type ChatGPTAccountView,
  type ChatGPTAuthStatusView } from "../../api/chatgpt-auth";

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

const accountStateLabel: Record<ChatGPTAccountView["state"], string> = {
  connected: "已连接", signed_out: "已退出", verification_pending: "等待验证", reauthorization_required: "需要重新登录",
};

export function V2ChatGPTAccountSettings({ client, query, onExit, prepareForDraft = false, onReady }: {
  client: APIClient;
  query: ReturnType<typeof useChatGPTAuthStatus>;
  onExit: () => void;
  prepareForDraft?: boolean;
  onReady?: (source: V2ReadyModelSource) => void;
}) {
  const queryClient = useQueryClient();
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
      if (mounted.current) setError(reason instanceof Error ? reason.message : "ChatGPT 账户操作失败，请重试。");
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
        setError(`模型检查未通过（${result.failure_reason}）。请检查账户状态后重试。`);
        return;
      }
      const catalog = await client.availableModelRoutes();
      queryClient.setQueryData(routesQueryKey, catalog);
      if (!mounted.current) return;
      if (!catalog.routes.some((route) => route.provider_id === provider && route.model === model && route.selectable)) {
        setError("模型检查已完成，当前模型目录尚未确认可用，请稍后重试。");
        return;
      }
      setNotice("模型检查已通过，可在对话中选择这个账户的模型。");
      if (prepareForDraft) onReady?.({ id: provider, default_model: model });
    } catch (reason) {
      if (mounted.current) setError(reason instanceof Error ? reason.message : "模型检查失败，请重试。");
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
      setNotice("登录链接已复制，请在系统浏览器中打开。");
    } catch { setError("无法复制链接，请从下方选中本地登录地址后复制。"); }
  };

  return <section className="v2-chatgpt-settings" aria-labelledby="v2-chatgpt-title">
    <button aria-label="返回模型选择" className="v2-model-custom-back" onClick={onExit} ref={backRef} type="button">
      <ArrowLeft aria-hidden="true" size={16} />返回模型
    </button>
    <header className="v2-model-catalog-heading">
      <h1 id="v2-chatgpt-title">ChatGPT 账户</h1>
      <p>使用 ChatGPT 账户登录，按当前订阅与用量使用模型。登录信息由操作系统凭据库保护。</p>
    </header>
    <div className="v2-settings-card v2-chatgpt-card">
      <header><div><strong>Using ChatGPT plan</strong><p>使用 ChatGPT 订阅</p></div>
        <div className="v2-provider-actions v2-chatgpt-actions">
          <button disabled={!enabled || locked || waiting} onClick={() => void login()} type="button">Continue with ChatGPT</button>
          <button disabled={!enabled || locked} onClick={() => void act("usage", () => client.openChatGPTUsage({
            version: chatGPTAuthProtocol, confirm: true,
          }))} type="button"><ExternalLink aria-hidden="true" size={14} />Manage usage<span className="sr-only">，管理用量</span></button>
        </div>
      </header>
      {!client.hasChatGPTAuth ? <p className="v2-chatgpt-help">当前连接不支持 ChatGPT 账户登录，请使用支持此功能的桌面版本。</p>
        : query.isError ? <div className="v2-chatgpt-help"><p role="alert">无法读取 ChatGPT 账户状态，请重新读取后继续。</p>
          <button className="v2-setting-link" onClick={() => void query.refetch()} type="button">重新读取账户</button></div>
          : !status ? <p className="v2-chatgpt-help" role="status">正在读取账户…</p>
            : !status.available ? <p className="v2-chatgpt-help" role="alert">操作系统凭据库当前不可用，请恢复后重新读取。</p> : null}
      {status?.login && <div className="v2-chatgpt-login" role={status.login.state === "failed" ? "alert" : "status"}>
        <p>{waiting ? <><LoaderCircle aria-hidden="true" className="v2-chatgpt-spinner" size={15} />等待 ChatGPT 登录…</>
          : status.login.state === "succeeded" ? "ChatGPT 登录成功，账户已保存。"
            : status.login.state === "cancelled" ? "登录已取消。" : "ChatGPT 登录失败，请重试。"}</p>
        {status.login.message && <p>{status.login.message}</p>}
        {waiting && <>
          <p>请在系统浏览器完成登录。返回模型列表后仍可查看登录状态。</p>
          <div className="v2-provider-actions v2-chatgpt-actions">
            {validChatGPTStartURL(status.login.start_url) && <button disabled={locked} onClick={() => void copyStartURL()} type="button">
              <Copy aria-hidden="true" size={14} />复制登录链接</button>}
            <button disabled={locked || !enabled} onClick={() => void act("cancel", () => client.cancelChatGPTLogin({
              version: chatGPTAuthProtocol, attempt_id: status.login!.id, confirm: true,
            }))} type="button">取消登录</button>
          </div>
          {validChatGPTStartURL(status.login.start_url) && <code className="v2-chatgpt-local-url">{status.login.start_url}</code>}
        </>}
      </div>}
      {status && <div className="v2-chatgpt-accounts">
        <h2>已保存的账户</h2>
        {accounts.length ? <>
          <label>账户<select aria-label="ChatGPT 账户" disabled={locked} value={selected?.id ?? ""}
            onChange={(event) => { setSelectedID(event.target.value); setSelectedModel(""); setNotice(""); setError(""); }}>
            {accounts.map((account) => <option key={account.id} value={account.id}>
              {account.label || account.email || "ChatGPT 账户"} · {accountStateLabel[account.state]}
            </option>)}
          </select></label>
          {selected && <>
            <p className="v2-chatgpt-help">{selected.email && <span>{selected.email} · </span>}{accountStateLabel[selected.state]}</p>
            <label>模型<select aria-label="ChatGPT 模型" disabled={locked || selected.state !== "connected" || !selected.models.length}
              value={model} onChange={(event) => { setSelectedModel(event.target.value); setNotice(""); setError(""); }}>
              {selected.models.length ? selected.models.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)
                : <option value="">登录后刷新模型</option>}
            </select></label>
            <div className="v2-provider-actions v2-chatgpt-actions">
              <button disabled={!enabled || locked || waiting || !["connected", "verification_pending"].includes(selected.state)}
                onClick={() => void act("models", () => client.refreshChatGPTModels({
                  version: chatGPTAuthProtocol, account_id: selected.id, confirm: true,
                }))} type="button"><RefreshCw aria-hidden="true" size={14} />刷新模型</button>
              {selected.state !== "signed_out" && <button className="danger-quiet" disabled={!enabled || locked || waiting}
                onClick={() => void act("logout", () => client.logoutChatGPTAccount({
                  version: chatGPTAuthProtocol, account_id: selected.id, confirm: true,
                }))} type="button"><LogOut aria-hidden="true" size={14} />退出账户</button>}
              {selected.state !== "connected" && <button aria-label="Continue with ChatGPT，重新登录所选账户" disabled={!enabled || locked || waiting}
                  onClick={() => void login(selected.id)} type="button">Continue with ChatGPT</button>}
              <span />
              <button className="primary" disabled={!enabled || locked || waiting || !client.hasModelControl || selected.state !== "connected" || !model}
                onClick={() => void checkModel()} type="button">{working === "check" ? "正在检查模型…" : prepareForDraft ? "检查并返回任务" : "检查模型"}</button>
            </div>
            <p className="v2-chatgpt-help">检查通过后，即可在任务中使用此模型。</p>
          </>}
        </> : <p className="v2-chatgpt-help">还没有保存的 ChatGPT 账户。完成登录后，账户会显示在这里。</p>}
      </div>}
    </div>
    {locked && working !== "check" && <p role="status">正在处理账户操作…</p>}
    {error && <p className="v2-chatgpt-error" role="alert">{error}</p>}
    {notice && <p role="status">{notice}</p>}
  </section>;
}
