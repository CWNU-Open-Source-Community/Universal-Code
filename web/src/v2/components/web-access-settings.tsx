import { useId, useMemo, useRef, useState } from "react";
import type { WebAccessMode } from "../../api/types";
import { V2ConfirmDialog } from "./dialog";
import { canonicalizeExactNetworkTargets, exactNetworkTargetLooksValid, parseExactNetworkTargets } from "./network-scope-control";
import { V2WebAccessModeSelect, webAccessModeDetails } from "./web-access-mode";

export function V2WebAccessSettings({ mode, targets, onChange, onOpenPermissions,
  onOpenEnvironment, hasThread }: {
  mode: WebAccessMode;
  targets: string[];
  onChange?: (mode: WebAccessMode, targets: string[]) => void;
  onOpenPermissions: () => void;
  onOpenEnvironment: () => void;
  hasThread: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const saveRef = useRef<HTMLButtonElement>(null);
  const editRef = useRef<HTMLButtonElement>(null);
  const detailID = useId();
  const raw = useMemo(() => parseExactNetworkTargets(draft), [draft]);
  const invalid = raw.filter((target) => !exactNetworkTargetLooksValid(target));
  const parsed = useMemo(() => invalid.length === 0 ? canonicalizeExactNetworkTargets(raw) : [], [invalid.length, raw]);

  return <>
    <h1>配置</h1>
    <p className="v2-settings-lead">为新对话设置网页访问方式。设置会自动保存，当前对话可在输入区单独调整。</p>
    <section className="v2-settings-section"><h2>新对话默认设置</h2>
      <div className="v2-settings-card v2-web-access-settings">
        <div className="v2-setting-row">
          <div><strong>网页搜索</strong><span id={detailID}>{webAccessModeDetails[mode]}</span></div>
          <div className="v2-setting-value"><V2WebAccessModeSelect mode={mode} describedBy={detailID}
            disabled={!onChange} onChange={(next) => {
              setEditing(false); setConfirmOpen(false);
              onChange?.(next, next === "rules" ? targets : []);
            }} /></div>
        </div>
        {mode === "rules" && <>
          <div className="v2-setting-row">
            <div><strong>预先允许的域名</strong><span>{targets.length
              ? targets.join("、") : "网页读取按请求批准；添加域名后可直接读取。"}</span></div>
            <div className="v2-setting-value"><button aria-expanded={editing} disabled={!onChange}
              onClick={() => { setDraft(targets.join("\n")); setEditing((value) => !value); }} ref={editRef} type="button">{editing ? "收起" : "编辑域名"}</button></div>
          </div>
          {editing && <div className="v2-web-access-target-editor">
            <label><span>允许直接读取的 HTTPS 域名</span><textarea aria-label="新对话允许的 HTTPS 域名"
              onChange={(event) => setDraft(event.target.value)} placeholder={"docs.example.com\nhttps://example.org"}
              rows={4} spellCheck={false} value={draft} /></label>
            <p>每行填写一个公网 HTTPS 主机或 origin。</p>
            {invalid.length > 0 && <p className="v2-network-error" role="alert">只接受无路径、查询或通配符的公网 HTTPS 目标：{invalid.join("、")}</p>}
            <button disabled={invalid.length > 0} ref={saveRef} onClick={() => setConfirmOpen(true)} type="button">保存域名</button>
          </div>}
        </>}
        <p className="v2-web-access-unavailable">缓存搜索、索引搜索：待支持</p>
      </div>
    </section>
    <section className="v2-settings-section"><h2>任务与执行</h2>
      <div className="v2-settings-card">
        <div className="v2-setting-row"><div><strong>当前任务权限</strong><span>{hasThread
          ? "调整当前对话的操作确认方式与项目访问范围。" : "打开一个对话后，可调整它的操作确认方式。"}</span></div>
          <div className="v2-setting-value"><button disabled={!hasThread} onClick={onOpenPermissions} type="button">管理</button></div></div>
        <div className="v2-setting-row"><div><strong>执行环境</strong><span>设置可用的本机与沙盒环境。</span></div>
          <div className="v2-setting-value"><button onClick={onOpenEnvironment} type="button">设置环境</button></div></div>
      </div>
    </section>
    <V2ConfirmDialog confirmLabel="保存域名" description={parsed.length
      ? `新对话将允许直接读取 ${parsed.length} 个公网 HTTPS 域名，其他网页读取仍按请求批准。`
      : "新对话中的网页读取将按请求批准。"}
      title="保存新对话网页规则？" open={confirmOpen} returnFocusRef={saveRef}
      onCancel={() => setConfirmOpen(false)} onConfirm={() => {
        onChange?.("rules", parsed); setConfirmOpen(false); setEditing(false);
        requestAnimationFrame(() => editRef.current?.focus());
      }} />
  </>;
}
