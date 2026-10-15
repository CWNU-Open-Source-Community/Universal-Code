import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { ExtensionMCPServerView, MCPCredentialBindingView } from "../api/types";
import { validMCPBearerSecret } from "../api/mcp-credentials";
import { useLocale } from "../lib/locale";

const clients = new WeakMap<APIClient, number>();
let nextClient = 0;
function clientIdentity(client: APIClient): number {
  let identity = clients.get(client);
  if (identity === undefined) { identity = ++nextClient; clients.set(client, identity); }
  return identity;
}

export function MCPEnvironmentCredentialControls({ client, server, capability }: {
  client: APIClient; server: ExtensionMCPServerView; capability?: boolean;
}) {
  const { t } = useLocale();
  if (server.native_source || server.transport !== "stdio" || !server.env_credential_refs) return null;
  const references = Array.from(new Set(Object.values(server.env_credential_refs))).sort();
  if (references.length === 0) return null;
  if (!capability) return <p role="status">{t("环境凭据管理需要支持系统凭据存储的服务。", "Environment credential management requires service support for the system credential store.")}</p>;
  return <>{references.map((reference) => <EnvironmentCredential key={`${clientIdentity(client)}/${server.id}/${server.descriptor_fingerprint}/${server.workspace_id}/${server.run_id ?? ""}/${server.target}/${reference}`}
    client={client} server={server} reference={reference} />)}</>;
}

function EnvironmentCredential({ client, server, reference }: { client: APIClient; server: ExtensionMCPServerView; reference: string }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [valid, setValid] = useState(false);
  const [confirmed, setConfirmed] = useState("");
  const binding: MCPCredentialBindingView = { server_id: server.id, workspace_id: server.workspace_id,
    ...(server.run_id ? { run_id: server.run_id } : {}), target: server.target, credential_ref: reference,
    expected_descriptor_fingerprint: server.descriptor_fingerprint };
  const queryKey = ["mcp-environment-credential", clientIdentity(client), server.id, server.descriptor_fingerprint, reference];
  const status = useQuery({ queryKey, queryFn: ({ signal }) => client.mcpCredentialStatus(binding, signal), retry: false });
  const change = useMutation({ mutationFn: (action: "set" | "delete") => {
    if (!status.data || status.isFetching || status.error || confirmed !== status.data.reference_fingerprint) throw new Error(t("请刷新凭据状态并明确确认。", "Refresh credential presence and confirm the action."));
    const secret = action === "set" ? input.current?.value ?? "" : "";
    if (input.current) input.current.value = "";
    setValid(false); setConfirmed("");
    return client.changeMCPCredential({ version: "mcp-credential.v1", binding, action, confirm: true,
      expected_reference_fingerprint: status.data.reference_fingerprint, ...(action === "set" ? { secret } : {}) });
  }, retry: false, onSuccess: (data) => queryClient.setQueryData(queryKey, data),
    onError: () => { void queryClient.invalidateQueries({ queryKey }); } });
  const canChange = client.hasExtensionControl && !!status.data?.store_available && !status.data.endpoint_conflict &&
    !status.isFetching && !change.isPending && !status.error;
  const error = status.error || change.error;
  const names = Object.entries(server.env_credential_refs ?? {}).filter(([, current]) => current === reference).map(([name]) => name);
  return <details className="extension-onboarding"><summary>{t("管理环境凭据", "Manage environment credential")}: {reference}</summary>
    <p>{t("注入环境变量", "Injected environment variables")}: {names.join(", ")}</p>
    <p>{t("保存到系统凭据库后，由 Go 在已批准的连接中注入。随后按服务器当前阶段批准发现或重新发现。", "Go injects the system-store secret into approved connections. Then approve discovery or rediscover according to the server's current stage.")}</p>
    <p role="status">{status.isPending ? t("正在读取凭据状态…", "Reading credential presence…") : status.error || !status.data ?
      t("凭据状态未知，请刷新。", "Credential presence is unknown; refresh.") : !status.data.store_available ?
      t("系统凭据库不可用。", "The system credential store is unavailable.") : status.data.configured ?
      t("本地环境凭据已保存", "Local environment credential stored") : t("本地尚未保存环境凭据", "No local environment credential stored")}</p>
    {status.data && <p>{t("引用此名称的 MCP 登记数", "MCP registrations using this name")}: {status.data.registration_count}</p>}
    {status.data?.endpoint_conflict && <p className="inline-warning">{t("凭据名称被不同地址或传输引用，请登记独立名称。", "Different endpoints or transports share this credential name; register separate names.")}</p>}
    {!client.hasExtensionControl && <p>{t("当前连接为只读。", "This connection is read-only.")}</p>}
    <button className="settings-action" type="button" disabled={status.isFetching || change.isPending} onClick={() => {
      setConfirmed(""); change.reset(); void status.refetch();
    }}>{t("刷新环境凭据状态", "Refresh environment credential presence")}</button>
    <fieldset disabled={!canChange} className="extension-form">
      <label>{t("环境凭据（8–2560 字节，无空白）", "Environment secret (8–2560 bytes, no whitespace)")}
        <input ref={input} type="password" autoComplete="off" maxLength={2560} spellCheck={false} onChange={(event) => {
          setValid(validMCPBearerSecret(event.currentTarget.value) && !event.currentTarget.value.startsWith("uc-mcp-oauth-v1:")); change.reset();
        }} /></label>
      <label><input type="checkbox" checked={!!status.data && confirmed === status.data.reference_fingerprint}
        onChange={(event) => setConfirmed(event.target.checked ? status.data!.reference_fingerprint : "")} />{t(
        "我确认更改此系统凭据名称及其全部引用。", "I confirm changing this system credential name and all its references.")}</label>
      <div className="extension-actions">
        <button className="settings-action" type="button" disabled={!status.data || confirmed !== status.data.reference_fingerprint || !valid} onClick={() => change.mutate("set")}>
          {status.data?.configured ? t("更新环境凭据", "Update environment credential") : t("保存环境凭据", "Save environment credential")}</button>
        <button className="settings-action danger" type="button" disabled={!status.data || confirmed !== status.data.reference_fingerprint || !status.data.configured} onClick={() => change.mutate("delete")}>
          {t("删除环境凭据", "Remove environment credential")}</button>
      </div>
    </fieldset>
    {change.isSuccess && <p role="status">{change.data.configured ? t("环境凭据已保存并回读确认。", "Environment credential saved and presence verified.") :
      t("环境凭据已删除并回读确认。", "Environment credential removed and absence verified.")}</p>}
    {error && <p className="inline-warning" role="alert">{error instanceof Error ? error.message : t("凭据操作失败，请刷新状态。", "Credential action failed; refresh presence.")}</p>}
  </details>;
}
