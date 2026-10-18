import { useId, useMemo, useRef, useState } from "react";
import type { WebAccessMode } from "../../api/types";
import { V2ConfirmDialog } from "./dialog";
import { canonicalizeExactNetworkTargets, exactNetworkTargetLooksValid, parseExactNetworkTargets } from "./network-scope-control";
import { V2WebAccessModeSelect, webAccessModeDetails } from "./web-access-mode";
import { useLocale } from "../../lib/locale";

export function V2WebAccessSettings({ mode, targets, onChange, onOpenPermissions,
  onOpenEnvironment, hasThread }: {
  mode: WebAccessMode;
  targets: string[];
  onChange?: (mode: WebAccessMode, targets: string[]) => void;
  onOpenPermissions: () => void;
  onOpenEnvironment: () => void;
  hasThread: boolean;
}) {
  const { t } = useLocale();
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
    <h1>{t("配置", "Configuration")}</h1>
    <p className="v2-settings-lead">{t("为新对话设置网页访问方式。设置会自动保存，当前对话可在输入区单独调整。", "Set web access for new conversations. Settings are saved automatically; adjust the current conversation in its composer.")}</p>
    <section className="v2-settings-section"><h2>{t("新对话默认设置", "New conversation defaults")}</h2>
      <div className="v2-settings-card v2-web-access-settings">
        <div className="v2-setting-row">
          <div><strong>{t("网页搜索", "Web search")}</strong><span id={detailID}>{t(webAccessModeDetails[mode],
            mode === "disabled" ? "Disable new web searches and fetches. Saved pages remain readable."
              : mode === "rules" ? "Use configured search services. Preapproved domains can be read directly; other pages require approval."
                : "Use configured search services and read public HTTPS pages without per-site approval.")}</span></div>
          <div className="v2-setting-value"><V2WebAccessModeSelect mode={mode} describedBy={detailID}
            disabled={!onChange} onChange={(next) => {
              setEditing(false); setConfirmOpen(false);
              onChange?.(next, next === "rules" ? targets : []);
            }} /></div>
        </div>
        {mode === "rules" && <>
          <div className="v2-setting-row">
            <div><strong>{t("预先允许的域名", "Preapproved domains")}</strong><span>{targets.length
              ? targets.join(t("、", ", ")) : t("网页读取按请求批准；添加域名后可直接读取。", "Web reads require approval. Add domains to allow direct access.")}</span></div>
            <div className="v2-setting-value"><button aria-expanded={editing} disabled={!onChange}
              onClick={() => { setDraft(targets.join("\n")); setEditing((value) => !value); }} ref={editRef} type="button">{editing ? t("收起", "Collapse") : t("编辑域名", "Edit domains")}</button></div>
          </div>
          {editing && <div className="v2-web-access-target-editor">
            <label><span>{t("允许直接读取的 HTTPS 域名", "HTTPS domains allowed for direct access")}</span><textarea aria-label={t("新对话允许的 HTTPS 域名", "Allowed HTTPS domains for new conversations")}
              onChange={(event) => setDraft(event.target.value)} placeholder={"docs.example.com\nhttps://example.org"}
              rows={4} spellCheck={false} value={draft} /></label>
            <p>{t("每行填写一个公网 HTTPS 主机或 origin。", "Enter one public HTTPS host or origin per line.")}</p>
            {invalid.length > 0 && <p className="v2-network-error" role="alert">{t("只接受无路径、查询或通配符的公网 HTTPS 目标：", "Only public HTTPS targets without paths, queries, or wildcards are accepted: ")}{invalid.join(t("、", ", "))}</p>}
            <button disabled={invalid.length > 0} ref={saveRef} onClick={() => setConfirmOpen(true)} type="button">{t("保存域名", "Save domains")}</button>
          </div>}
        </>}
        <p className="v2-web-access-unavailable">{t("缓存搜索、索引搜索：待支持", "Cached and indexed search: not yet supported")}</p>
      </div>
    </section>
    <section className="v2-settings-section"><h2>{t("任务与执行", "Tasks & execution")}</h2>
      <div className="v2-settings-card">
        <div className="v2-setting-row"><div><strong>{t("当前任务权限", "Current task permissions")}</strong><span>{hasThread
          ? t("调整当前对话的操作确认方式与项目访问范围。", "Adjust approval behavior and project access for the current conversation.") : t("打开一个对话后，可调整它的操作确认方式。", "Open a conversation to adjust its approval behavior.")}</span></div>
          <div className="v2-setting-value"><button disabled={!hasThread} onClick={onOpenPermissions} type="button">{t("管理", "Manage")}</button></div></div>
        <div className="v2-setting-row"><div><strong>{t("执行环境", "Execution environment")}</strong><span>{t("设置可用的本机与沙盒环境。", "Configure available local and sandbox environments.")}</span></div>
          <div className="v2-setting-value"><button onClick={onOpenEnvironment} type="button">{t("设置环境", "Configure environment")}</button></div></div>
      </div>
    </section>
    <V2ConfirmDialog confirmLabel={t("保存域名", "Save domains")} description={parsed.length
      ? t(`新对话将允许直接读取 ${parsed.length} 个公网 HTTPS 域名，其他网页读取仍按请求批准。`, `New conversations will allow direct access to ${parsed.length} public HTTPS domains. Other pages will still require approval.`)
      : t("新对话中的网页读取将按请求批准。", "Web reads in new conversations will require approval.")}
      title={t("保存新对话网页规则？", "Save web rules for new conversations?")} open={confirmOpen} returnFocusRef={saveRef}
      onCancel={() => setConfirmOpen(false)} onConfirm={() => {
        onChange?.("rules", parsed); setConfirmOpen(false); setEditing(false);
        requestAnimationFrame(() => editRef.current?.focus());
      }} />
  </>;
}
