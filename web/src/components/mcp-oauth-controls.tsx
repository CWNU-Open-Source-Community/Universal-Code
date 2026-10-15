import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { MCPCredentialBindingView } from "../api/types";
import { useLocale } from "../lib/locale";

export function MCPOAuthControls({ client, binding, queryKey }: { client: APIClient; binding: MCPCredentialBindingView; queryKey: readonly unknown[] }) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const [issuer, setIssuer] = useState("");
  const [clientID, setClientID] = useState("");
  const [clientIDMode, setClientIDMode] = useState<"pre_registered" | "metadata_url">("pre_registered");
  const [confirmed, setConfirmed] = useState(false);
  const status = useQuery({ queryKey,
    queryFn: ({ signal }) => client.mcpCredentialStatus(binding, signal), retry: false,
    refetchInterval: (query) => query.state.data?.oauth?.state === "authorizing" ? 1500 : false });
  const action = useMutation({ mutationFn: (action: "oauth-discover" | "oauth-start" | "oauth-cancel") => {
    if (!status.data || !confirmed) throw new Error(t("请确认授权操作。", "Confirm the authorization action."));
    return client.changeMCPOAuth({ version: "mcp-credential.v1", binding, action, confirm: true,
      expected_reference_fingerprint: status.data.reference_fingerprint,
      ...(action === "oauth-start" ? { oauth_issuer: issuer, oauth_client_id: clientID, oauth_client_id_mode: clientIDMode } : action === "oauth-cancel" ? { oauth_flow_id: status.data.oauth?.flow_id } : {}) });
  }, retry: false, onSuccess: (data) => {
    if (data.oauth?.issuers?.length === 1) setIssuer(data.oauth.issuers[0]!);
    if (data.oauth?.state !== "discovered") {
      const safe = { ...data, oauth: data.oauth ? { ...data.oauth, authorization_url: undefined } : undefined };
      queries.setQueryData(queryKey, safe);
      void status.refetch();
    }
  }, onError: () => { void status.refetch(); } });
  const oauth = action.data?.oauth?.state === "discovered" ? action.data.oauth : status.data?.oauth;
  const authorizationURL = oauth?.state === "authorizing" && oauth.flow_id === action.data?.oauth?.flow_id ? action.data?.oauth?.authorization_url : undefined;
  const pending = oauth?.state === "authorizing" || action.isPending;
  const enabled = client.hasExtensionControl && status.data?.store_available && !status.data.endpoint_conflict && status.data.registration_count === 1;
  return <details className="extension-onboarding"><summary>{t("OAuth 浏览器授权", "OAuth browser authorization")}</summary>
    <p>{t("支持预注册或已托管元数据 URL 的公共 OAuth 客户端。浏览器须运行在 Go 服务所在电脑；客户端需允许动态端口的 127.0.0.1 回调。", "For public OAuth clients using pre-registration or a hosted metadata URL. Use a browser on the Go service computer; the client must allow 127.0.0.1 callbacks with dynamic ports.")}</p>
    <p role="status">{t("授权状态", "Authorization state")}: {oauth?.state ?? (status.isPending ? t("读取中", "Loading") : t("尚未授权", "Not authorized"))}</p>
    {oauth?.expires_at && <p>{t("过期时间", "Expires")}: {oauth.expires_at}</p>}
    <div className="extension-form"><fieldset disabled={!enabled || action.isPending}>
      <div className="extension-checks"><label><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />{t("我确认连接此服务器发现发行者，并将授权令牌保存在此专用系统凭据名称下。", "I confirm contacting this server to discover issuers and storing authorization tokens under this dedicated system credential name.")}</label></div>
      <button type="button" className="settings-action" disabled={!confirmed || pending} onClick={() => action.mutate("oauth-discover")}>{t("发现 OAuth 发行者", "Discover OAuth issuers")}</button>
      <label>{t("确认信任的发行者", "Confirm trusted issuer")}
        <input value={issuer} onChange={(event) => setIssuer(event.target.value)} type="url" placeholder="https://issuer.example" disabled={pending} />
      </label>
      {oauth?.issuers?.map((value) => <button className="settings-action" type="button" key={value} onClick={() => setIssuer(value)}>{value}</button>)}
      <label>{t("客户端 ID 类型", "Client ID type")}<select value={clientIDMode} disabled={pending} onChange={(event) => setClientIDMode(event.target.value as "pre_registered" | "metadata_url")}>
        <option value="pre_registered">{t("预注册公共客户端", "Pre-registered public client")}</option>
        <option value="metadata_url">{t("客户端元数据 URL", "Client metadata URL")}</option>
      </select></label>
      {clientIDMode === "metadata_url" && <p>{t("请填写已托管的 HTTPS 客户端元数据文档 URL；发行者必须声明支持此方式。", "Enter an already hosted HTTPS client metadata document URL; the issuer must advertise support.")}</p>}
      <label>{clientIDMode === "metadata_url" ? t("客户端元数据 URL", "Client metadata URL") : t("公共客户端 ID", "Public client ID")}<input value={clientID} onChange={(event) => setClientID(event.target.value)} maxLength={256} autoComplete="off" disabled={pending} /></label>
      <div className="extension-actions">
        <button className="settings-action" type="button" disabled={!confirmed || !issuer || !clientID || pending} onClick={() => action.mutate("oauth-start")}>{t("开始授权", "Start authorization")}</button>
        <button className="settings-action" type="button" disabled={!confirmed || oauth?.state !== "authorizing"} onClick={() => action.mutate("oauth-cancel")}>{t("取消授权", "Cancel authorization")}</button>
      </div>
    </fieldset></div>
    {authorizationURL && <a className="settings-action" href={authorizationURL} target="_blank" rel="noreferrer">{t("打开授权页面", "Open authorization page")}</a>}
    <button className="settings-action" type="button" disabled={status.isFetching || action.isPending} onClick={() => { action.reset(); void status.refetch(); }}>{t("刷新授权状态", "Refresh authorization state")}</button>
    <p>{t("授权后仍须批准能力发现并审查服务器能力。下方“删除本地令牌”可断开本地认证；远端撤销请使用服务方账户设置。", "After authorization, approve discovery and review server capabilities. Remove local token below to disconnect local authentication; revoke remote access in the service account settings.")}</p>
    {(status.error || action.error) && <p role="alert" className="inline-warning">{t("OAuth 操作未完成，请刷新状态并核对发行者与客户端配置。", "OAuth did not complete. Refresh status and check the issuer and client configuration.")}</p>}
  </details>;
}
