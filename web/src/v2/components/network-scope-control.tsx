import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Globe2, ShieldCheck } from "lucide-react";
import { canonicalExactNetworkTarget } from "../../api/client";
import { V2ConfirmDialog } from "./dialog";

export type V2NetworkMode = "disabled" | "allowlist";

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
  const rawTargets = useMemo(() => parseExactNetworkTargets(draft), [draft]);
  const invalid = rawTargets.filter((target) => !exactNetworkTargetLooksValid(target));
  const parsed = useMemo(() => invalid.length === 0
    ? canonicalizeExactNetworkTargets(rawTargets) : [], [invalid.length, rawTargets]);

  useEffect(() => {
    if (!open || confirmOpen) return;
    const closeOutside = (event: MouseEvent) => {
      if (!shellRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("click", closeOutside);
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("click", closeOutside);
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [open, confirmOpen]);

  const disableNetwork = () => {
    onChange("disabled", []);
    setConfirmOpen(false);
    setOpen(false);
  };
  const requestEnable = () => {
    if (parsed.length === 0 || invalid.length > 0) return;
    setConfirmOpen(true);
  };

  return <div className="v2-network-scope-control" ref={shellRef}>
    <button aria-expanded={open} aria-haspopup="dialog" className="v2-composer-chip"
      disabled={disabled} onClick={() => {
        setDraft(targets.join("\n"));
        setOpen((value) => !value);
      }} ref={triggerRef} type="button">
      {mode === "allowlist" ? <Globe2 aria-hidden="true" size={14} />
        : <ShieldCheck aria-hidden="true" size={14} />}
      {mode === "allowlist" ? `网页访问 · ${targets.length}` : "无网络"}
      <ChevronDown aria-hidden="true" size={13} />
    </button>
    {open && <section aria-label="新对话网络范围" className="v2-network-popover" role="dialog">
      <header><strong>网页访问范围</strong><span>仅当前新对话</span></header>
      <p>每行填写一个明确的公网 HTTPS 主机或 origin。网页搜索还需把所选搜索服务的主机加入这里。</p>
      <textarea aria-label="允许访问的 HTTPS 目标" onChange={(event) => setDraft(event.target.value)}
        placeholder={"docs.example.com\nhttps://search.example.org"} rows={4} spellCheck={false}
        value={draft} />
      {invalid.length > 0 && <p className="v2-network-error" role="alert">
        只接受无路径、查询或通配符的公网 HTTPS 目标：{invalid.join("、")}
      </p>}
      <footer><button disabled={mode === "disabled"} onClick={disableNetwork} type="button">
        保持无网络</button><button className="primary" disabled={parsed.length === 0 || invalid.length > 0}
          onClick={requestEnable} type="button"><Check aria-hidden="true" size={14} />使用白名单</button></footer>
    </section>}
    <V2ConfirmDialog confirmLabel="允许这些目标" danger
      description={`Agent 将能在此对话中访问 ${parsed.length} 个明确的公网 HTTPS 目标。网页内容始终按不可信证据处理；重定向、DNS、私网地址、非 HTTPS 目标和未列出的主机都会被后端拒绝。`}
      onCancel={() => setConfirmOpen(false)} onConfirm={() => {
        onChange("allowlist", parsed);
        setConfirmOpen(false);
        setOpen(false);
      }} open={confirmOpen} returnFocusRef={triggerRef} title="启用网页访问？" />
  </div>;
}
