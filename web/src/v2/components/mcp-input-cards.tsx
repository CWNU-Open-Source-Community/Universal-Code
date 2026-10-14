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
  const clientID = inputClientID(client);
  const query = useQuery({ queryKey: ["mcp-inputs", clientID, runID], queryFn: ({ signal }) => listMCPInputs(client, runID, signal), refetchInterval: 1_000, gcTime: 0, retry: false });
  return <section className="v2-approval-stack" aria-label="MCP 服务请求输入">
    {query.data?.filter((input) => Date.parse(input.expires_at) > Date.now()).map((input) => <MCPInputCard key={`${clientID}:${input.id}`} client={client} input={input} onAnswered={() => void query.refetch()} />)}
    {query.isError && <p role="status">MCP 输入请求暂不可用。<button type="button" onClick={() => void query.refetch()}>重试</button></p>}
  </section>;
}
function MCPInputCard({ client, input, onAnswered }: { client: APIClient; input: MCPInput; onAnswered: () => void }) {
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
            setError("请输入有效数字；整数必须在浏览器可精确表示的范围内。"); setBusy(false); return;
          }
          content[field.name] = numeric;
        } else content[field.name] = value;
      }
    }
    // Keep answers out of React Query's mutation cache and clear local state as
    // soon as the single HTTP attempt starts. A failed POST is never retried.
    setValues({});
    try { await respondMCPInput(client, input, action, action === "accept" && input.mode === "form" ? content : undefined); setFinished(true); onAnswered(); }
    catch { setError("未能确认答复已送达，请刷新请求状态后再决定；原输入已清空。"); onAnswered(); }
    finally { setBusy(false); }
  };
  if (finished) return null;
  const update = (name: string, value: unknown) => setValues((old) => ({ ...old, [name]: value }));
  return <form className="v2-approval-card v2-mcp-input" onSubmit={(event) => { event.preventDefault(); void answer("accept"); }}>
    <h3>{input.server_name} 请求输入</h3>
    <p>{input.server_id} · {input.method}</p>
    <p>{input.message}</p>
    {input.mode === "url" ? <>
      <p>外部授权网站：<strong>{input.host}</strong></p>
      <a href={input.url} target="_blank" rel="noopener noreferrer">打开外部授权页面</a>
      <p>完成网站上的操作后，点击“已完成”。</p>
    </> : (input.fields ?? []).map((field) => <p key={field.name}>
      <label className={field.type === "boolean" ? "v2-mcp-checkbox" : undefined}>{field.title}{field.required ? "（必填）" : ""}
        {field.description && <small>{field.description}</small>}
        <InputControl field={field} value={values[field.name]} update={(value) => update(field.name, value)} />
      </label>
    </p>)}
    {error && <p role="alert">{error}</p>}
    {!client.hasExtensionControl && <p>此连接只能查看输入请求，请使用具有控制权限的连接答复。</p>}
    <footer><button className="primary" type="submit" disabled={busy || !client.hasExtensionControl}>{input.mode === "url" ? "已完成" : "提交答复"}</button>
    <button className="secondary" type="button" disabled={busy || !client.hasExtensionControl} onClick={() => void answer("decline")}>拒绝提供</button>
    <button className="secondary" type="button" disabled={busy || !client.hasExtensionControl} onClick={() => void answer("cancel")}>取消交互</button></footer>
  </form>;
}

function defaultFieldValue(field: MCPInputField): unknown {
  if (field.default !== undefined) return field.default;
  if (field.type === "boolean") return false;
  if (field.type === "array") return [];
  return "";
}
function InputControl({ field, value, update }: { field: MCPInputField; value: unknown; update: (value: unknown) => void }) {
  if (field.type === "boolean") return <input type="checkbox" checked={value === true} onChange={(event) => update(event.target.checked)} />;
  if (field.enum?.length) return <select multiple={field.type === "array"} required={field.required}
    value={(value ?? defaultFieldValue(field)) as string | string[]}
    onChange={(event) => update(field.type === "array" ? Array.from(event.target.selectedOptions, (option) => option.value) : event.target.value)}>
    {field.type !== "array" && <option value="">请选择</option>}
    {field.enum.map((option, index) => <option key={option} value={option}>{field.enum_titles?.[index] || option}</option>)}
  </select>;
  return <input type={field.type === "string" ? "text" : "number"} step={field.type === "integer" ? 1 : "any"}
    required={field.required} maxLength={32768} value={String(value ?? "")} onChange={(event) => update(event.target.value)} />;
}
