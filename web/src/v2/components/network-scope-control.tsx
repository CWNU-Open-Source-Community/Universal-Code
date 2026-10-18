import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Globe2, ShieldCheck } from "lucide-react";
import { canonicalExactNetworkTarget } from "../../api/client";
import type { WebAccessMode } from "../../api/types";
import { useLocale } from "../../lib/locale";
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
  const { t } = useLocale();
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
      {t(webAccessModeLabels[mode], mode === "disabled" ? "Search disabled" : mode === "rules" ? "Rule-based search" : "Full access")}
      <ChevronDown aria-hidden="true" size={13} />
    </button>
    {open && <section aria-label={t("新对话网页访问", "New conversation web access")} className="v2-network-popover" ref={panelRef} role="dialog" tabIndex={-1}>
      <header><strong>{t("网页访问与搜索", "Web access and search")}</strong><span>{t("新对话默认设置", "New conversation defaults")}</span></header>
      <div className="v2-web-access-mode-row"><label>{t("网页搜索", "Web search")}</label><V2WebAccessModeSelect mode={mode}
        onChange={(next) => {
          const nextTargets = next === "rules" ? targets : [];
          setDraft(nextTargets.join("\n")); onChange(next, nextTargets);
        }} /></div>
      <p>{t(webAccessModeDetails[mode], mode === "disabled" ? "New web searches and fetches are disabled. Saved pages can still be read."
        : mode === "rules" ? "Use the configured search service. Approved domains can be read directly; other web requests require approval."
        : "Use the configured search service and read public HTTPS pages directly without per-site approval.")}</p>
      <p>{t("缓存搜索、索引搜索：待支持", "Cached and indexed search: coming soon")}</p>
      {mode === "rules" && <><p>{t("每行填写一个允许直接读取的公网 HTTPS 主机或 origin。", "Enter one public HTTPS host or origin per line to allow direct access.")}</p>
      <textarea aria-label={t("允许访问的 HTTPS 目标", "Allowed HTTPS targets")} onChange={(event) => setDraft(event.target.value)}
        placeholder={"docs.example.com\nhttps://search.example.org"} rows={4} spellCheck={false}
        value={draft} />
      {invalid.length > 0 && <p className="v2-network-error" role="alert">
        {t("只接受无路径、查询或通配符的公网 HTTPS 目标：", "Only public HTTPS targets without paths, queries, or wildcards are accepted: ")}{invalid.join(t("、", ", "))}
      </p>}
      <footer><button className="primary" disabled={invalid.length > 0}
          onClick={requestEnable} ref={saveRef} type="button"><Check aria-hidden="true" size={14} />{t("保存域名", "Save domains")}</button></footer></>}
    </section>}
    <V2ConfirmDialog confirmLabel={t("保存域名", "Save domains")}
      description={parsed.length ? t(`新对话将允许直接读取 ${parsed.length} 个公网 HTTPS 域名，其他网页读取仍按请求批准。`, `New conversations will allow direct access to ${parsed.length} public HTTPS domains. Other web requests require approval.`) : t("新对话中的网页读取将按请求批准。", "Web requests in new conversations will require approval.")}
      onCancel={() => setConfirmOpen(false)} onConfirm={() => {
        onChange("rules", parsed);
        setConfirmOpen(false);
        setOpen(false);
        requestAnimationFrame(() => triggerRef.current?.focus());
      }} open={confirmOpen} returnFocusRef={saveRef} title={t("保存新对话网页规则？", "Save web rules for new conversations?")} />
  </div>;
}
