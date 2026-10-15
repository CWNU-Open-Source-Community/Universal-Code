import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { ExtensionMCPServerView, MCPCredentialBindingView } from "../api/types";
import { validMCPBearerSecret } from "../api/mcp-credentials";
import { MCPOAuthControls } from "./mcp-oauth-controls";
import { useLocale } from "../lib/locale";

const clients = new WeakMap<APIClient, number>();
let nextClient = 0;
function clientIdentity(client: APIClient) {
  let identity = clients.get(client);
  if (identity === undefined) { identity = ++nextClient; clients.set(client, identity); }
  return identity;
}

// Changing connection, descriptor or scope remounts all transient input and
// mutations. An old completion can only update its own metadata query key.
export function MCPCredentialControls({ client, server, capability }: {
  client: APIClient; server: ExtensionMCPServerView; capability?: boolean;
}) {
  const { t } = useLocale();
  if (server.native_source || server.transport === "stdio") return <p>{t(
    "查看此服务器或插件的接入配置以管理认证。此处的 Bearer 令牌表单适用于远程服务器。", "Review this server or plugin integration configuration to manage authentication. The bearer token form here is available for remote servers.")}</p>;
  if (!server.credential_ref) return <p>{t(
    "描述符未指定凭据。需要认证时，请使用专用凭据名称登记服务器，再在此输入令牌。",
    "No credential is referenced. For authentication, register the server with a dedicated credential name, then enter its token here.")}</p>;
  if (!capability) return <p role="status">{t("MCP 凭据管理待接入。使用支持系统凭据存储的服务后可在此保存令牌。", "MCP credential management needs service support. Connect to a service with system credential storage to save tokens here.")}</p>;
  return <BoundMCPCredentialControls key={`${clientIdentity(client)}/${server.id}/${server.descriptor_fingerprint}/${server.workspace_id}/${server.run_id ?? ""}/${server.target}/${server.credential_ref}`}
    client={client} server={server} />;
}

function BoundMCPCredentialControls({ client, server }: { client: APIClient; server: ExtensionMCPServerView }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [secretValid, setSecretValid] = useState(false);
  const [confirmed, setConfirmed] = useState("");
  const binding: MCPCredentialBindingView = { server_id: server.id, workspace_id: server.workspace_id,
    ...(server.run_id ? { run_id: server.run_id } : {}), target: server.target, credential_ref: server.credential_ref!,
    expected_descriptor_fingerprint: server.descriptor_fingerprint };
  const queryKey = ["mcp-credential", clientIdentity(client), server.id, server.descriptor_fingerprint];
  const status = useQuery({ queryKey, queryFn: ({ signal }) => client.mcpCredentialStatus(binding, signal), retry: false });
  const change = useMutation({ mutationFn: (action: "set" | "delete") => {
    if (!status.data || confirmed !== status.data.reference_fingerprint) throw new Error(t("请刷新凭据状态并明确确认。", "Refresh credential presence and confirm the action."));
    const secret = action === "set" ? input.current?.value ?? "" : "";
    if (input.current) input.current.value = "";
    setSecretValid(false);
    setConfirmed("");
    // Mutation variables retain only the action. Plaintext is request-local,
    // never React state, query data, browser storage, drafts or error messages.
    return client.changeMCPCredential({ version: "mcp-credential.v1", binding, action, confirm: true,
      expected_reference_fingerprint: status.data.reference_fingerprint, ...(action === "set" ? { secret } : {}) });
  }, retry: false, onSuccess: (data) => queryClient.setQueryData(queryKey, data),
    onError: () => { void queryClient.invalidateQueries({ queryKey }); } });
  const canChange = client.hasExtensionControl && !!status.data?.store_available && !status.data.endpoint_conflict &&
    !status.isFetching && !change.isPending && !status.error;
  const error = status.error || change.error;
  const nextStep = ["staged", "disabled", "quarantined"].includes(server.state) ? t(
    "保存后，先核对描述符并选择“批准能力发现”；批准完成后选择“重新发现”，检查远端认证与能力。",
    "After saving, first review the descriptor and select Approve discovery. Once approved, select Rediscover to check remote authentication and capabilities.")
    : server.state === "discovery_approved" ? t(
      "保存后选择“重新发现”，检查远端认证与能力；发现完成后核对能力指纹，再审查并启用能力。",
      "After saving, select Rediscover to check remote authentication and capabilities. Once discovery completes, review the capability fingerprint and enable capabilities.")
    : server.state === "capabilities_pending" ? t(
      "保存后选择“重新发现”检查远端认证与最新能力，再核对能力指纹并选择“审查并启用能力”。",
      "After saving, select Rediscover to check remote authentication and the latest capabilities. Then review the capability fingerprint and select Review and enable capabilities.")
    : server.state === "enabled" ? t(
      "保存后选择“重新发现”，检查远端认证与能力，并按返回的审查状态继续；工具可用时，在任务中验证调用。",
      "After saving, select Rediscover to check remote authentication and capabilities, then follow the returned review state. When tools are available, verify calls in a task.")
    : server.state === "revoked" ? t(
      "此登记已撤销。重新连接时，请登记并审查新的服务器描述符；此处可管理现有凭据名称的本地令牌。",
      "This registration is revoked. Register and review a new server descriptor to reconnect. You can manage the existing credential name's local token here.")
    : t("保存后，查看服务器的当前审查状态与可用操作，按该阶段继续接入。",
      "After saving, inspect the server's current review state and available actions to continue setup.");
  return <>{server.transport !== "websocket" && <MCPOAuthControls client={client} binding={binding} queryKey={queryKey} />}<details className="extension-onboarding"><summary>{t("管理 MCP 认证令牌", "Manage MCP authentication token")}</summary>
    <p>{t("将 Bearer 令牌保存到系统凭据库。", "Store the bearer token in the system credential store.")} {nextStep}</p>
    <p>{t("凭据名称", "Credential name")}: {server.credential_ref} · {server.target}</p>
    <p role="status">{status.isPending ? t("正在读取凭据状态…", "Reading credential presence…") : status.error || !status.data ?
      t("凭据状态未知，请刷新。", "Credential presence is unknown; refresh.") : !status.data.store_available ?
      t("系统凭据库不可用，无法读取或保存令牌。", "The system credential store is unavailable; token presence and storage are unavailable.") :
      status.data.configured ? t("本地令牌已保存", "Local token stored") : t("本地尚未保存令牌", "No local token stored")}</p>
    {status.data && <p>{t("系统凭据库", "System store")}: {status.data.store_kind} ·
      {t("引用此名称的 MCP 登记数", "MCP registrations using this name")}: {status.data.registration_count}</p>}
    {status.data && status.data.registration_count > 1 && <p className="inline-warning">{t(
      "此凭据名称由多个 MCP 登记共享。更新或删除会影响这些登记。", "Multiple MCP registrations share this credential. Updating or removing it affects those registrations.")}</p>}
    {status.data?.endpoint_conflict && <p className="inline-warning">{t(
      "凭据名称冲突：多个地址引用了此名称。请为每个服务器地址登记独立的凭据名称后重试。",
      "Credential name conflict: several endpoints reference this name. Register a separate credential name for each endpoint and retry.")}</p>}
    {!client.hasExtensionControl && <p>{t("当前连接为只读。连接具备控制权限的服务后可保存或删除凭据。", "This connection is read-only. Connect with control access to save or remove credentials.")}</p>}
    <button className="settings-action" type="button" disabled={status.isFetching || change.isPending} onClick={() => {
      setConfirmed(""); change.reset(); void status.refetch();
    }}>{t("刷新凭据状态", "Refresh credential presence")}</button>
    <fieldset disabled={!canChange} className="extension-form">
      <label>{t("Bearer 令牌（8–2560 字节，无空白）", "Bearer token (8–2560 bytes, no whitespace)")}
        <input ref={input} type="password" autoComplete="off" maxLength={2560} spellCheck={false} onChange={(event) => {
          setSecretValid(validMCPBearerSecret(event.currentTarget.value)); change.reset();
        }} /></label>
      <label><input type="checkbox" checked={!!status.data && confirmed === status.data.reference_fingerprint}
        onChange={(event) => setConfirmed(event.target.checked ? status.data!.reference_fingerprint : "")} />{t(
        "我确认更改此系统凭据名称及其全部引用；此名称也可能用于其他集成。",
        "I confirm changing this system credential name and all its references; other integrations may also use this name.")}</label>
      <div className="extension-actions">
        <button className="settings-action" type="button" disabled={!status.data || confirmed !== status.data.reference_fingerprint || !secretValid} onClick={() => change.mutate("set")}>
          {status.data?.configured ? t("更新本地令牌", "Update local token") : t("保存本地令牌", "Save local token")}</button>
        <button className="settings-action danger" type="button" disabled={!status.data || confirmed !== status.data.reference_fingerprint || !status.data.configured} onClick={() => change.mutate("delete")}>
          {t("删除本地令牌", "Remove local token")}</button>
      </div>
    </fieldset>
    <p>{t("提交后输入框会清空。删除会移除本地令牌；远端令牌有效性与服务器审查状态保留。要撤销远端令牌，请到服务方账户中操作。",
      "The input clears on submission. Removal deletes the local token; remote token validity and server review remain in place. Revoke the remote token in your service account.")}</p>
    {change.isSuccess && <p role="status">{change.data.configured ? t("令牌已保存，并回读确认存在。", "Token saved and local presence verified.") :
      t("本地令牌已删除，并回读确认不存在。", "Local token removed and absence verified.")}</p>}
    {error && <p className="inline-warning" role="alert">{error instanceof Error ? error.message : t("凭据操作失败，请刷新状态。", "Credential action failed; refresh presence.")}</p>}
  </details></>;
}
