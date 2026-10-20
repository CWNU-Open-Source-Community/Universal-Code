import { useLocale } from "../../lib/locale";
import { useId, useRef, useState } from "react";
import type { APIClient } from "../../api/client";
import type { DraftHead, DraftRef, DraftSnapshot, DraftState } from "../draft-document";
import { V2ImagePreview } from "./image-input";
import { V2FileAttachments } from "./file-input";
import "./draft-conflict.css";

const sameRef = (left: DraftRef | null, right: DraftRef) =>
  left?.branchID === right.branchID && left.seq === right.seq;

export function V2DraftConflict({ state, client, workspaceID, onResolve }: {
  state: DraftState;
  client: APIClient;
  workspaceID: string;
  onResolve: (expectedHeadToken: string, selectedRef: DraftRef) => void;
}) {
  const { t } = useLocale();
  const id = useId();
  const [expanded, setExpanded] = useState(false);
  const [selection, setSelection] = useState<{ token: string; ref: DraftRef } | null>(null);
  const [actionError, setActionError] = useState<{ token: string; message: string } | null>(null);
  const [copyStatus, setCopyStatus] = useState<{ token: string; message: string } | null>(null);
  const currentToken = useRef(state.headToken);
  currentToken.current = state.headToken;
  const selected = selection?.token === state.headToken
    ? state.heads.find((head) => sameRef(selection.ref, head.ref)) : undefined;
  const staleSelection = Boolean(selection && selection.token !== state.headToken);

  if (!state.conflict && !state.error) return null;

  const copy = async (head: DraftHead, label: string) => {
    const token = state.headToken;
    try {
      if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
      await navigator.clipboard.writeText(head.snapshot.text);
      if (currentToken.current === token) setCopyStatus({ token, message: t(`已复制${label}的完整正文。`, `Copied the full text of ${label}.`) });
    } catch {
      if (currentToken.current === token) setCopyStatus({ token, message: t("未能复制，请选中正文后手动复制；版本内容未改变。", "Could not copy. Select and copy the text manually; this version is unchanged.") });
    }
  };
  const confirm = () => {
    if (!state.conflict || !selected || selection?.token !== state.headToken) return;
    setActionError(null);
    try {
      onResolve(state.headToken, selected.ref);
      setSelection(null);
    } catch (error) {
      setActionError({ token: state.headToken,
        message: error instanceof Error ? error.message : t("未能选用此版本，请重新核对。", "Could not select this version. Check again.") });
    }
  };

  return <section className="v2-draft-conflict" aria-label={t("草稿版本核对", "Review draft versions")}>
    {state.error && <div className="v2-draft-conflict-error" role="alert">
      <p><strong>{t("草稿尚未安全保存。", "The draft has not been safely saved.")}</strong> {t("当前页面仍保留内容，刷新或关闭可能丢失未保存的修改；请先复制需要保留的文字。", "The page still holds your content. Reloading or closing may lose unsaved edits; copy any text you need first.")}</p>
      <p>{state.error}</p>
    </div>}
    {state.conflict && <>
      <div className="v2-draft-conflict-summary">
        <p role="status"><strong>{t(`草稿有 ${state.heads.length} 个待核对版本。`, `The draft has ${state.heads.length} versions to review.`)}</strong> {t("请先比较并选用一份，再发送消息。", "Compare and choose a version before sending.")}</p>
        <button type="button" aria-expanded={expanded} aria-controls={`${id}-versions`}
          onClick={() => setExpanded((value) => !value)}>{expanded ? t("收起版本", "Hide versions") : t("查看版本", "View versions")}</button>
      </div>
      {expanded && <div className="v2-draft-conflict-versions" id={`${id}-versions`}>
        <p>{t("选用时以该版本的整份文字、文件引用、图片和附件为准；其余版本不在本次发送中。需要合并文字时，请先复制所需内容，选用后在草稿中编辑。", "The selected version supplies all text, file references, images and attachments for this message. To combine text, copy what you need first, choose a version, then edit the draft.")}</p>
        <fieldset>
          <legend>{t("选择要继续编辑的完整版本", "Choose the complete version to continue editing")}</legend>
          <div className="v2-draft-conflict-grid">{state.heads.map((head, index) => {
            const label = t(`版本 ${index + 1}`, `Version ${index + 1}`);
            const checked = Boolean(selected && sameRef(selected.ref, head.ref));
            return <article key={JSON.stringify(head.ref)} className={`v2-draft-conflict-version${checked ? " is-selected" : ""}`}>
              <header>
                <label><input type="radio" name={`${id}-version`} checked={checked} onChange={() => {
                  setSelection({ token: state.headToken, ref: head.ref }); setActionError(null);
                }} /><strong>{label}</strong></label>
                {sameRef(state.ref, head.ref) && <span>{t("本窗口当前版本", "Current version in this window")}</span>}
              </header>
              <SnapshotContent client={client} workspaceID={workspaceID} snapshot={head.snapshot} label={label} />
              <button type="button" onClick={() => void copy(head, label)} aria-label={t(`复制${label}正文`, `Copy text of ${label}`)}>{t("复制正文", "Copy text")}</button>
            </article>;
          })}</div>
        </fieldset>
        {staleSelection && <p role="status">{t("版本列表已变化，请重新核对并选择要使用的版本。", "The version list changed. Review it and choose a version again.")}</p>}
        {copyStatus?.token === state.headToken && <p role="status">{copyStatus.message}</p>}
        {actionError?.token === state.headToken && <p className="v2-draft-conflict-error" role="alert">{actionError.message}</p>}
        <div className="v2-draft-conflict-actions">
          <button type="button" disabled={!selected} onClick={confirm}>{t("确认选用此版本", "Use this version")}</button>
          <span>{t("选用后替换输入框中的草稿，确认内容后再发送。", "This replaces the composer draft. Review it before sending.")}</span>
        </div>
      </div>}
    </>}
  </section>;
}

function SnapshotContent({ client, workspaceID, snapshot, label }: {
  client: APIClient; workspaceID: string; snapshot: DraftSnapshot; label: string;
}) {
  const { t } = useLocale();
  const matchingImages = snapshot.images.filter((image) => image.workspace_id === workspaceID);
  return <>
    {snapshot.text ? <pre className="v2-draft-conflict-text" tabIndex={0} aria-label={t(`${label}完整正文`, `Full text of ${label}`)}>{snapshot.text}</pre>
      : <p className="v2-draft-conflict-empty">{t("此版本没有文字。", "This version has no text.")}</p>}
    <p>{t(`文件引用（${snapshot.files.length}）`, `File references (${snapshot.files.length})`)}</p>
    {snapshot.files.length > 0 && <ul className="v2-draft-conflict-files">{snapshot.files.map((file) => <li key={file.id}>
      <span>{file.path}{file.partial ? t("· 部分内容", " · Partial content") : ""}{file.redacted ? t("· 已脱敏", " · Redacted") : ""}</span>
      <code>SHA256 {file.digest}</code>
    </li>)}</ul>}
    <p>{t(`图片（${snapshot.images.length}）`, `Images (${snapshot.images.length})`)}</p>
    <V2ImagePreview client={client} images={matchingImages} />
    <p>{t(`文件附件（${snapshot.attachments?.length ?? 0}）`, `File attachments (${snapshot.attachments?.length ?? 0})`)}</p>
    {(snapshot.mcpReferences?.length ?? 0) > 0 && <><p>{t(`MCP 引用（${snapshot.mcpReferences!.length}）`, `MCP references (${snapshot.mcpReferences!.length})`)}</p>
      <ul className="v2-draft-conflict-files">{snapshot.mcpReferences!.map((item) => <li key={item.id}>
        <span>{item.serverName} · {item.label}</span><code>{JSON.stringify(item.reference.arguments)}</code></li>)}</ul></>}
    <V2FileAttachments client={client} attachments={(snapshot.attachments ?? []).filter((file) => file.workspace_id === workspaceID)} />
    {matchingImages.length !== snapshot.images.length && <p className="v2-draft-conflict-error">{t("存在其他项目的图片，未读取其预览；请核对草稿来源。", "This draft includes images from another project. Their previews were not loaded; check the draft source.")}</p>}
    {snapshot.images.length > 0 && <details><summary>{t("图片版本信息", "Image version details")}</summary>
      <ul className="v2-draft-conflict-files">{snapshot.images.map((image) => <li key={image.id}>
        <span>{image.name || t("截图", "Screenshot")} · {image.width} × {image.height}</span><code>SHA256 {image.sha256}</code>
      </li>)}</ul>
    </details>}
  </>;
}
