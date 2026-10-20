import { useLocale } from "../../lib/locale";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { listMCPInputs, respondMCPInput, type MCPInput, type MCPInputField } from "../../api/mcp-inputs";

const inputClientIDs = new WeakMap<APIClient, number>();
let nextInputClientID = 0;
function inputClientID(client: APIClient): number {
  let id = inputClientIDs.get(client);
  if (id === undefined) { id = ++nextInputClientID; inputClientIDs.set(client, id); }
  return id;
}

export function MCPInputCards({ client, runID }: { client: APIClient; runID: string }) {
  const { t } = useLocale();
  const clientID = inputClientID(client);
  const query = useQuery({ queryKey: ["mcp-inputs", clientID, runID], queryFn: ({ signal }) => listMCPInputs(client, runID, signal), refetchInterval: 1_000, gcTime: 0, retry: false });
  return <section className="v2-approval-stack" aria-label={t("MCP 服务请求输入", "MCP service input requests")}>
    {query.data?.filter((input) => Date.parse(input.expires_at) > Date.now()).map((input) => <MCPInputCard key={`${clientID}:${input.id}`} client={client} input={input} onAnswered={() => void query.refetch()} />)}
    {query.isError && <p role="status">{t("MCP 输入请求暂不可用。", "MCP input requests are temporarily unavailable.")}<button type="button" onClick={() => void query.refetch()}>{t("重试", "Retry")}</button></p>}
  </section>;
}
function MCPInputCard({ client, input, onAnswered }: { client: APIClient; input: MCPInput; onAnswered: () => void }) {
  const { t } = useLocale();
  const [values, setValues] = useState<Record<string, unknown>>(() => Object.fromEntries((input.fields ?? []).map((field) => [field.name, defaultFieldValue(field)])));
  const [busy, setBusy] = useState(false);
  const [finished, setFinished] = useState(false);
  const [error, setError] = useState("");
  const answer = async (action: "accept" | "decline" | "cancel") => {
    if (busy || finished || !client.hasExtensionControl) return;
    setBusy(true); setError("");
    const content: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
    for (const field of action === "accept" && input.mode === "form" ? input.fields ?? [] : []) {
      const value = values[field.name];
      if (value !== "" && value !== undefined) {
        if (field.type === "number" || field.type === "integer") {
          const numeric = Number(value);
          if (!Number.isFinite(numeric) || (field.type === "integer" && !Number.isSafeInteger(numeric))) {
            setError(t("请输入有效数字；整数必须在浏览器可精确表示的范围内。", "Enter a valid number. Integers must be within the browser's exact numeric range.")); setBusy(false); return;
          }
          content[field.name] = numeric;
        } else content[field.name] = value;
      }
    }
    // Keep answers out of React Query's mutation cache and clear local state as
    // soon as the single HTTP attempt starts. A failed POST is never retried.
    setValues({});
    try { await respondMCPInput(client, input, action, action === "accept" && input.mode === "form" ? content : undefined); setFinished(true); onAnswered(); }
    catch { setError(t("未能确认答复已送达，请刷新请求状态后再决定；原输入已清空。", "Delivery is unconfirmed. Refresh the request before deciding again; the original input was cleared.")); onAnswered(); }
    finally { setBusy(false); }
  };
  if (finished) return null;
  const update = (name: string, value: unknown) => setValues((old) => ({ ...old, [name]: value }));
  return <form className="v2-approval-card v2-mcp-input" onSubmit={(event) => { event.preventDefault(); void answer("accept"); }}>
    <h3>{input.server_name} {t("请求输入", "Input requested")}</h3>
    <p>{input.server_id} · {input.method}</p>
    <p>{input.message}</p>
    {input.mode === "url" ? <>
      <p>{t("外部授权网站：", "External authorization website: ")}<strong>{input.host}</strong></p>
      <a href={input.url} target="_blank" rel="noopener noreferrer">{t("打开外部授权页面", "Open external authorization page")}</a>
      <p>{t("完成网站上的操作后，点击“已完成”。", "Complete the website's steps, then select Done.")}</p>
    </> : (input.fields ?? []).map((field) => <p key={field.name}>
      <label className={field.type === "boolean" ? "v2-mcp-checkbox" : undefined}>{field.title}{field.required ? t("（必填）", " (required)") : ""}
        {field.description && <small>{field.description}</small>}
        <InputControl field={field} value={values[field.name]} update={(value) => update(field.name, value)} />
      </label>
    </p>)}
    {error && <p role="alert">{error}</p>}
    {!client.hasExtensionControl && <p>{t("此连接只能查看输入请求，请使用具有控制权限的连接答复。", "This connection can only view input requests. Use a connection with control permission to reply.")}</p>}
    <footer><button className="primary" type="submit" disabled={busy || !client.hasExtensionControl}>{input.mode === "url" ? t("已完成", "Completed") : t("提交答复", "Submit reply")}</button>
    <button className="secondary" type="button" disabled={busy || !client.hasExtensionControl} onClick={() => void answer("decline")}>{t("拒绝提供", "Decline")}</button>
    <button className="secondary" type="button" disabled={busy || !client.hasExtensionControl} onClick={() => void answer("cancel")}>{t("取消交互", "Cancel interaction")}</button></footer>
  </form>;
}

function defaultFieldValue(field: MCPInputField): unknown {
  if (field.default !== undefined) return field.default;
  if (field.type === "boolean") return false;
  if (field.type === "array") return [];
  return "";
}
function InputControl({ field, value, update }: { field: MCPInputField; value: unknown; update: (value: unknown) => void }) {
  const { t } = useLocale();
  if (field.type === "boolean") return <input type="checkbox" checked={value === true} onChange={(event) => update(event.target.checked)} />;
  if (field.enum?.length) return <select multiple={field.type === "array"} required={field.required}
    value={(value ?? defaultFieldValue(field)) as string | string[]}
    onChange={(event) => update(field.type === "array" ? Array.from(event.target.selectedOptions, (option) => option.value) : event.target.value)}>
    {field.type !== "array" && <option value="">{t("请选择", "Choose an option")}</option>}
    {field.enum.map((option, index) => <option key={option} value={option}>{field.enum_titles?.[index] || option}</option>)}
  </select>;
  return <input type={field.type === "string" ? "text" : "number"} step={field.type === "integer" ? 1 : "any"}
    required={field.required} maxLength={32768} value={String(value ?? "")} onChange={(event) => update(event.target.value)} />;
}
