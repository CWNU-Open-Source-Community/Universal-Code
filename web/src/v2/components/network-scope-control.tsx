import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Globe2, ShieldCheck } from "lucide-react";
import { canonicalExactNetworkTarget } from "../../api/client";
import type { WebAccessMode } from "../../api/types";
import { V2ConfirmDialog } from "./dialog";
import { V2WebAccessModeSelect, webAccessModeDetails, webAccessModeLabels } from "./web-access-mode";

export type V2NetworkMode = WebAccessMode;

export function parseExactNetworkTargets(value: string): string[] {
  return [...new Set(value.split(/[\n,]/u).map((item) => item.trim()).filter(Boolean))];
}

export function exactNetworkTargetLooksValid(value: string): boolean {
  try {
    canonicalExactNetworkTarget(value);
    return true;
  } catch {
    return false;
  }
}

export function canonicalizeExactNetworkTargets(values: string[]): string[] {
  return [...new Set(values.map((value) => canonicalExactNetworkTarget(value)))].sort();
}

export function V2NetworkScopeControl({ mode, targets, disabled = false, onChange }: {
  mode: V2NetworkMode;
  targets: string[];
  disabled?: boolean;
  onChange: (mode: V2NetworkMode, targets: string[]) => void;
}) {
  const [open, setOpen] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [draft, setDraft] = useState(() => targets.join("\n"));
  const shellRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const saveRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLElement>(null);
  const rawTargets = useMemo(() => parseExactNetworkTargets(draft), [draft]);
  const invalid = rawTargets.filter((target) => !exactNetworkTargetLooksValid(target));
  const parsed = useMemo(() => invalid.length === 0
    ? canonicalizeExactNetworkTargets(rawTargets) : [], [invalid.length, rawTargets]);

  useEffect(() => {
    if (open) panelRef.current?.focus();
  }, [open]);
  useEffect(() => {
    if (!open || confirmOpen) return;
    const closeOutside = (event: MouseEvent) => {
      if (!shellRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) {
        event.preventDefault(); setOpen(false); triggerRef.current?.focus();
      }
    };
    window.addEventListener("click", closeOutside);
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("click", closeOutside);
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [open, confirmOpen]);

  const requestEnable = () => {
    if (invalid.length > 0) return;
    setConfirmOpen(true);
  };

  return <div className="v2-network-scope-control" ref={shellRef}>
    <button aria-expanded={open} aria-haspopup="dialog" className="v2-composer-chip"
      disabled={disabled} onClick={() => {
        setDraft(targets.join("\n"));
        setOpen((value) => !value);
      }} ref={triggerRef} type="button">
      {mode !== "disabled" ? <Globe2 aria-hidden="true" size={14} />
        : <ShieldCheck aria-hidden="true" size={14} />}
      {webAccessModeLabels[mode]}
      <ChevronDown aria-hidden="true" size={13} />
    </button>
    {open && <section aria-label="新对话网页访问" className="v2-network-popover" ref={panelRef} role="dialog" tabIndex={-1}>
      <header><strong>网页访问与搜索</strong><span>新对话默认设置</span></header>
      <div className="v2-web-access-mode-row"><label>网页搜索</label><V2WebAccessModeSelect mode={mode}
        onChange={(next) => {
          const nextTargets = next === "rules" ? targets : [];
          setDraft(nextTargets.join("\n")); onChange(next, nextTargets);
        }} /></div>
      <p>{webAccessModeDetails[mode]}</p>
      <p>缓存搜索、索引搜索：待支持</p>
      {mode === "rules" && <><p>每行填写一个允许直接读取的公网 HTTPS 主机或 origin。</p>
      <textarea aria-label="允许访问的 HTTPS 目标" onChange={(event) => setDraft(event.target.value)}
        placeholder={"docs.example.com\nhttps://search.example.org"} rows={4} spellCheck={false}
        value={draft} />
      {invalid.length > 0 && <p className="v2-network-error" role="alert">
        只接受无路径、查询或通配符的公网 HTTPS 目标：{invalid.join("、")}
      </p>}
      <footer><button className="primary" disabled={invalid.length > 0}
          onClick={requestEnable} ref={saveRef} type="button"><Check aria-hidden="true" size={14} />保存域名</button></footer></>}
    </section>}
    <V2ConfirmDialog confirmLabel="保存域名"
      description={parsed.length ? `新对话将允许直接读取 ${parsed.length} 个公网 HTTPS 域名，其他网页读取仍按请求批准。` : "新对话中的网页读取将按请求批准。"}
      onCancel={() => setConfirmOpen(false)} onConfirm={() => {
        onChange("rules", parsed);
        setConfirmOpen(false);
        setOpen(false);
        requestAnimationFrame(() => triggerRef.current?.focus());
      }} open={confirmOpen} returnFocusRef={saveRef} title="保存新对话网页规则？" />
  </div>;
}
