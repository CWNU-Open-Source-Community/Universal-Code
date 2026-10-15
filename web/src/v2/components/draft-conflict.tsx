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
      if (currentToken.current === token) setCopyStatus({ token, message: `已复制${label}的完整正文。` });
    } catch {
      if (currentToken.current === token) setCopyStatus({ token, message: "未能复制，请选中正文后手动复制；版本内容未改变。" });
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
        message: error instanceof Error ? error.message : "未能选用此版本，请重新核对。" });
    }
  };

  return <section className="v2-draft-conflict" aria-label="草稿版本核对">
    {state.error && <div className="v2-draft-conflict-error" role="alert">
      <p><strong>草稿尚未安全保存。</strong> 当前页面仍保留内容，刷新或关闭可能丢失未保存的修改；请先复制需要保留的文字。</p>
      <p>{state.error}</p>
    </div>}
    {state.conflict && <>
      <div className="v2-draft-conflict-summary">
        <p role="status"><strong>草稿有 {state.heads.length} 个待核对版本。</strong> 请先比较并选用一份，再发送消息。</p>
        <button type="button" aria-expanded={expanded} aria-controls={`${id}-versions`}
          onClick={() => setExpanded((value) => !value)}>{expanded ? "收起版本" : "查看版本"}</button>
      </div>
      {expanded && <div className="v2-draft-conflict-versions" id={`${id}-versions`}>
        <p>选用时以该版本的整份文字、文件引用、图片和附件为准；其余版本不在本次发送中。需要合并文字时，请先复制所需内容，选用后在草稿中编辑。</p>
        <fieldset>
          <legend>选择要继续编辑的完整版本</legend>
          <div className="v2-draft-conflict-grid">{state.heads.map((head, index) => {
            const label = `版本 ${index + 1}`;
            const checked = Boolean(selected && sameRef(selected.ref, head.ref));
            return <article key={JSON.stringify(head.ref)} className={`v2-draft-conflict-version${checked ? " is-selected" : ""}`}>
              <header>
                <label><input type="radio" name={`${id}-version`} checked={checked} onChange={() => {
                  setSelection({ token: state.headToken, ref: head.ref }); setActionError(null);
                }} /><strong>{label}</strong></label>
                {sameRef(state.ref, head.ref) && <span>本窗口当前版本</span>}
              </header>
              <SnapshotContent client={client} workspaceID={workspaceID} snapshot={head.snapshot} label={label} />
              <button type="button" onClick={() => void copy(head, label)} aria-label={`复制${label}正文`}>复制正文</button>
            </article>;
          })}</div>
        </fieldset>
        {staleSelection && <p role="status">版本列表已变化，请重新核对并选择要使用的版本。</p>}
        {copyStatus?.token === state.headToken && <p role="status">{copyStatus.message}</p>}
        {actionError?.token === state.headToken && <p className="v2-draft-conflict-error" role="alert">{actionError.message}</p>}
        <div className="v2-draft-conflict-actions">
          <button type="button" disabled={!selected} onClick={confirm}>确认选用此版本</button>
          <span>选用后替换输入框中的草稿，确认内容后再发送。</span>
        </div>
      </div>}
    </>}
  </section>;
}

function SnapshotContent({ client, workspaceID, snapshot, label }: {
  client: APIClient; workspaceID: string; snapshot: DraftSnapshot; label: string;
}) {
  const matchingImages = snapshot.images.filter((image) => image.workspace_id === workspaceID);
  return <>
    {snapshot.text ? <pre className="v2-draft-conflict-text" tabIndex={0} aria-label={`${label}完整正文`}>{snapshot.text}</pre>
      : <p className="v2-draft-conflict-empty">此版本没有文字。</p>}
    <p>文件引用（{snapshot.files.length}）</p>
    {snapshot.files.length > 0 && <ul className="v2-draft-conflict-files">{snapshot.files.map((file) => <li key={file.id}>
      <span>{file.path}{file.partial ? " · 部分内容" : ""}{file.redacted ? " · 已脱敏" : ""}</span>
      <code>SHA256 {file.digest}</code>
    </li>)}</ul>}
    <p>图片（{snapshot.images.length}）</p>
    <V2ImagePreview client={client} images={matchingImages} />
    <p>文件附件（{snapshot.attachments?.length ?? 0}）</p>
    {(snapshot.mcpReferences?.length ?? 0) > 0 && <><p>MCP 引用（{snapshot.mcpReferences!.length}）</p>
      <ul className="v2-draft-conflict-files">{snapshot.mcpReferences!.map((item) => <li key={item.id}>
        <span>{item.serverName} · {item.label}</span><code>{JSON.stringify(item.reference.arguments)}</code></li>)}</ul></>}
    <V2FileAttachments client={client} attachments={(snapshot.attachments ?? []).filter((file) => file.workspace_id === workspaceID)} />
    {matchingImages.length !== snapshot.images.length && <p className="v2-draft-conflict-error">存在其他项目的图片，未读取其预览；请核对草稿来源。</p>}
    {snapshot.images.length > 0 && <details><summary>图片版本信息</summary>
      <ul className="v2-draft-conflict-files">{snapshot.images.map((image) => <li key={image.id}>
        <span>{image.name || "截图"} · {image.width} × {image.height}</span><code>SHA256 {image.sha256}</code>
      </li>)}</ul>
    </details>}
  </>;
}
