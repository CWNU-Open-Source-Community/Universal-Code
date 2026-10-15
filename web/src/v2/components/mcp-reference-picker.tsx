import { useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ExtensionMCPServerView } from "../../api/types";
import { mcpTemplateVariables, type MCPDraftReference, type MCPReference } from "../../api/mcp-references";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useV2DraftDocument } from "../draft-context";
import "./mcp-reference-picker.css";

export const v2MCPReferenceKey = (workspaceID: string, threadID: string) =>
  ["v2", "draft-mcp", workspaceID, threadID] as const;

export function useV2MCPReferences(workspaceID: string, threadID: string) {
  const draft = useV2DraftDocument(workspaceID, threadID);
  const cache = useQueryClient();
  const key = v2MCPReferenceKey(workspaceID, threadID);
  const query = useQuery<MCPDraftReference[]>({ queryKey: key, queryFn: () => [],
    enabled: false, initialData: [], gcTime: Infinity });
  const update = (change: (current: MCPDraftReference[]) => MCPDraftReference[]) => {
    if (draft) draft.document.update(draft.scope, (current) => ({ ...current, mcpReferences: change(current.mcpReferences ?? []) }), draft.state.ref);
    else cache.setQueryData<MCPDraftReference[]>(key, (current) => change(current ?? []));
  };
  return { references: draft ? draft.state.snapshot.mcpReferences ?? [] : query.data, update };
}

interface Choice {
  kind: "resource" | "template" | "prompt";
  name: string;
  server: ExtensionMCPServerView;
  fields: { name: string; description?: string; required: boolean }[];
}
const choiceLabels: Record<Choice["kind"], string> = { resource: "资源", template: "资源模板", prompt: "Prompt" };

export function V2MCPReferencePicker({ client, workspaceID, runID, kind, onClose, onSelect, returnFocusRef }: {
  client: APIClient;
  workspaceID: string;
  runID?: string;
  kind: "resource" | "prompt";
  onClose: () => void;
  onSelect: (reference: MCPDraftReference) => void;
  returnFocusRef: RefObject<HTMLButtonElement | null>;
}) {
  const [search, setSearch] = useState("");
  const [selection, setSelection] = useState<Choice | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const firstField = useRef<HTMLInputElement>(null);
  useEffect(() => { if (selection) firstField.current?.focus(); }, [selection]);
  const fieldValue = (name: string) => Object.hasOwn(values, name) ? values[name] : "";
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, close, { isolateBackground: true, returnFocusRef });
  const inventory = useQuery({ queryKey: ["v2", "mcp-composer-catalog", workspaceID, runID ?? ""],
    queryFn: ({ signal }) => client.extensionInventory(runID ?? "", signal, workspaceID), staleTime: 0 });
  const choices: Choice[] = [];
  for (const server of inventory.data?.mcp_servers ?? []) {
    if (server.state !== "enabled" || server.health !== "healthy" || !server.capabilities.fingerprint ||
      server.approved_capability_fingerprint !== server.capabilities.fingerprint) continue;
    if (kind === "resource") {
      choices.push(...server.capabilities.resources.map((name): Choice => ({ kind: "resource", name, server, fields: [] })));
      choices.push(...(server.capabilities.resource_templates ?? []).map((name): Choice => ({ kind: "template", name, server,
        fields: mcpTemplateVariables(name).map((name) => ({ name, required: false })) })));
    } else {
      choices.push(...server.capabilities.prompts.map((name): Choice => ({ kind: "prompt", name, server,
        fields: server.capabilities.prompt_details?.find((prompt) => prompt.name === name)?.arguments ?? [] })));
    }
  }
  const filtered = choices.filter(({ name, server }) => `${server.name} ${name}`.toLocaleLowerCase().includes(search.toLocaleLowerCase()));
  const add = (choice: Choice) => {
    const arguments_: Record<string, string> = Object.fromEntries(choice.fields
      .filter(({ name, required }) => required || fieldValue(name) !== "")
      .map(({ name }) => [name, fieldValue(name)]));
    let parameters: MCPReference["arguments"] = { uri: choice.name };
    if (choice.kind === "prompt") parameters = { name: choice.name, arguments: arguments_ };
    else if (choice.kind === "template") parameters = { uri_template: choice.name, variables: arguments_ };
    onSelect({ id: crypto.randomUUID(), label: choice.name, serverName: choice.server.name,
      reference: { server_id: choice.server.id, capability_fingerprint: choice.server.capabilities.fingerprint!,
        method: choice.kind === "prompt" ? "prompts/get" : "resources/read",
        arguments: parameters } });
    onClose();
  };
  return createPortal(<div className="v2-inspector-backdrop" role="presentation"
    onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section aria-label={kind === "resource" ? "选择 MCP 资源" : "选择 MCP Prompt"} aria-modal="true"
      className="v2-inspector-drawer v2-mcp-picker" ref={dialog} role="dialog" tabIndex={-1}>
      <header><strong>{kind === "resource" ? "引用 MCP 资源" : "使用 MCP Prompt"}</strong>
        <button aria-label="关闭 MCP 选择" onClick={onClose} ref={close} type="button"><X aria-hidden="true" size={17} /></button></header>
      <p>发送消息即授权读取这次选定的内容。服务器返回的内容会作为参考资料提供给模型。</p>
      {selection ? <form className="v2-mcp-arguments" onSubmit={(event) => { event.preventDefault(); event.stopPropagation(); add(selection); }}>
        <button className="v2-mcp-back" onClick={() => { setSelection(null); setValues({}); }} type="button">
          <ArrowLeft aria-hidden="true" size={15} /> 返回列表</button>
        <strong>{selection.server.name}</strong><code>{selection.name}</code>
        {selection.kind === "template" && <p>填写需要的模板变量；空的可选变量将省略。</p>}
        {selection.fields.map((field, index) => <label key={field.name}><span>{field.name}{field.required ? "（必填）" : "（可选）"}</span>
          {field.description && <small>{field.description}</small>}
          <input autoComplete="off" maxLength={16384} ref={index === 0 ? firstField : undefined} required={field.required} value={fieldValue(field.name)}
            onChange={(event) => setValues((current) => ({ ...current, [field.name]: event.target.value }))} />
        </label>)}
        <button className="v2-mcp-add" disabled={selection.fields.some(({ name, required }) => required && !fieldValue(name).trim())} type="submit">添加到消息</button>
      </form> : <div className="v2-mcp-catalog">
        <input aria-label="搜索 MCP 内容" placeholder="搜索服务器或名称…" type="search" value={search} onChange={(event) => setSearch(event.target.value)} />
        {inventory.isPending && <p role="status">正在读取已批准的 MCP 目录…</p>}
        {inventory.error && <p role="alert">{inventory.error instanceof Error ? inventory.error.message : "MCP 目录加载失败"}
          <button onClick={() => void inventory.refetch()} type="button">重试</button></p>}
        {!inventory.isPending && !inventory.error && filtered.length === 0 && <p role="status">{choices.length === 0
          ? "当前项目没有已启用的 MCP 内容。可在设置的“扩展”中连接并批准服务器。" : "没有匹配的 MCP 内容。"}</p>}
        <ul>{filtered.map((choice) => <li key={JSON.stringify([choice.server.id, choice.kind, choice.name])}>
          <button aria-label={`${choice.server.name} · ${choiceLabels[choice.kind]} · ${choice.name}`}
            onClick={() => { if (choice.fields.length) { setValues({}); setSelection(choice); } else add(choice); }} type="button">
            <span>{choice.server.name}<small>{choiceLabels[choice.kind]}</small></span>
            <code>{choice.name}</code>{choice.fields.length > 0 && <small>{choice.fields.length} 个参数</small>}
          </button></li>)}</ul>
      </div>}
    </section>
  </div>, document.body);
}

export function V2MCPReferenceChips({ references, disabled, pendingIDs, onRemove }: {
  references: MCPDraftReference[];
  disabled: boolean;
  pendingIDs: string[];
  onRemove: (id: string) => void;
}) {
  return references.length > 0 ? <div className="v2-file-references" aria-label="待发送的 MCP 引用">
    {references.map((item) => <span key={item.id} title={`${item.serverName} · ${item.label}\n${JSON.stringify(item.reference.arguments)}`}>
      <small>{item.reference.method === "prompts/get" ? "Prompt" : "MCP"}</small><code>{item.serverName}: {item.label}</code>
      {pendingIDs.includes(item.id) && <small>随消息提交中</small>}
      <button aria-label={`移除 MCP 引用 ${item.label}`} disabled={disabled || pendingIDs.includes(item.id)} onClick={() => onRemove(item.id)} type="button">
        <X aria-hidden="true" size={13} /></button></span>)}
  </div> : null;
}
