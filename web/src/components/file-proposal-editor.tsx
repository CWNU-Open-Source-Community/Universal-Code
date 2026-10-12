import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Editor, { DiffEditor } from "@monaco-editor/react";
import { Check, FileDiff, LoaderCircle, Pencil, RefreshCw, X } from "lucide-react";
import type { APIClient } from "../api/client";
import type { FileEditProposalSourceView } from "../api/types";
import { useLocale } from "../lib/locale";
import { ErrorState, StatusBadge } from "./common";
import { useFileEditDraft, useFileEditLeave, useFileEditLeaveEntry } from "./file-edit-drafts";

export function FileProposalEditor({ client, runID, source, onClose }: {
  client: APIClient;
  runID: string;
  source: FileEditProposalSourceView;
  onClose: () => void;
}) {
  return <FileProposalSession key={JSON.stringify([source.workspace_id, runID, source.path, source.content_sha256])}
    client={client} runID={runID} source={source} onClose={onClose} />;
}

function FileProposalSession({ client, runID, source, onClose }: Parameters<typeof FileProposalEditor>[0]) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [activeSource, setActiveSource] = useState(source);
  const recovery = useFileEditDraft(runID, activeSource);
  const [draft, setDraft] = useState(recovery.text);
  const leave = useFileEditLeave();
  const [expired, setExpired] = useState(Date.parse(source.expires_at) <= Date.now());
  const [mode, setMode] = useState<"edit" | "diff">("edit");
  const [editorState, setEditorState] = useState<"loading" | "ready" | "failed">("loading");
  useEffect(() => {
    let active = true;
    void import("../lib/monaco-local").then(({ configureLocalMonaco }) => {
      configureLocalMonaco();
      if (active) setEditorState("ready");
    }).catch(() => {
      if (active) setEditorState("failed");
    });
    return () => { active = false; };
  }, []);
  useEffect(() => {
    setActiveSource(source);
  }, [source]);
  useEffect(() => {
    const remaining = Date.parse(activeSource.expires_at) - Date.now();
    setExpired(remaining <= 0);
    if (remaining <= 0) return;
    const timer = window.setTimeout(() => setExpired(true),
      Math.min(remaining, 2_147_483_647));
    return () => window.clearTimeout(timer);
  }, [activeSource.expires_at]);
  const mutation = useMutation({
    mutationFn: (proposedText: string) => client.createFileEditProposal(runID, {
      version: "file_edit_proposal.v1",
      source_handle: activeSource.source_handle,
      proposed_text: proposedText,
    }),
    onSuccess: (_, proposedText) => {
      recovery.submitted(proposedText);
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "file-edits"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "file-edit-change-set"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "approvals"] });
    },
  });
  const refresh = useMutation({
    mutationFn: () => client.reissueFileEditProposalSource(runID, activeSource.path,
      activeSource.content_sha256),
    onSuccess: (renewed) => {
      setActiveSource(renewed);
      mutation.reset();
    },
  });
  const bytes = new TextEncoder().encode(draft).length;
  const invalid = editorState !== "ready" || bytes > 256 * 1024 ||
    draft === activeSource.content || expired || recovery.changedBase;
  const language = languageForPath(activeSource.path);
  useFileEditLeaveEntry({ path: activeSource.path, dirty: Boolean(recovery.draft),
    busy: mutation.isPending, canSave: !invalid,
    save: () => mutation.mutateAsync(draft), keep: recovery.keep,
    discard: () => { recovery.discard(); setDraft(activeSource.content); mutation.reset(); },
  });

  return <section className="file-proposal-editor" aria-label={t(`编辑 ${activeSource.path} 的提案`, `Edit proposal for ${activeSource.path}`)}>
    <header>
      <div><Pencil aria-hidden="true" size={16} /><strong>{activeSource.path}</strong>
        <StatusBadge status="proposal only" /></div>
      <div className="segmented-control" aria-label={t("编辑器模式", "Editor mode")}>
        <button aria-pressed={mode === "edit"} onClick={() => setMode("edit")} type="button">
          <Pencil aria-hidden="true" size={14} />{t("编辑", "Edit")}
        </button>
        <button aria-pressed={mode === "diff"} onClick={() => setMode("diff")} type="button">
          <FileDiff aria-hidden="true" size={14} />{t("差异", "Diff")}
        </button>
      </div>
      <button aria-label={t("关闭文件提案编辑器", "Close file proposal editor")} className="icon-button"
        onClick={() => leave(onClose)} title={t("关闭编辑器", "Close editor")} type="button">
        <X aria-hidden="true" size={15} />
      </button>
    </header>
    <div className="monaco-proposal-surface">
      {editorState === "loading" && <div className="monaco-local-state" role="status">
        {t("正在加载本地编辑器", "Loading local editor")}
      </div>}
      {editorState === "failed" && <div className="monaco-local-state inline-warning" role="alert">
        {t("无法加载本地编辑器资源", "Local editor assets could not be loaded")}
      </div>}
      {editorState === "ready" && (mode === "edit" ?
        <Editor height="100%" language={language} onChange={(value) => {
          setDraft(value ?? ""); recovery.change(value ?? ""); mutation.reset();
        }}
        options={{ automaticLayout: true, fontSize: 13, letterSpacing: 0, minimap: { enabled: false },
          readOnly: mutation.isPending, renderWhitespace: "selection", scrollBeyondLastLine: false, tabSize: 2, wordWrap: "off" }}
        path={`proposal://${runID}/${activeSource.content_sha256}/${activeSource.path}`}
        theme="vs" value={draft} /> :
        <DiffEditor height="100%" language={language} modified={draft} original={activeSource.content}
          options={{ automaticLayout: true, fontSize: 13, letterSpacing: 0, minimap: { enabled: false },
            readOnly: true, renderSideBySide: true, scrollBeyondLastLine: false }}
          theme="vs" />)}
    </div>
    <footer>
      <span>{bytes.toLocaleString()} / {(256 * 1024).toLocaleString()} {t("字节", "bytes")}</span>
      <span>{t("来源", "Source")} {activeSource.content_sha256.slice(0, 12)}</span>
      {!mutation.data && (expired || mutation.isError) &&
        <button className="compact-command" disabled={refresh.isPending}
          onClick={() => refresh.mutate()} type="button">
          {refresh.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
            : <RefreshCw aria-hidden="true" size={14} />}
          {t("刷新来源", "Refresh source")}
        </button>}
      {mutation.data ? <span className="proposal-created-status" role="status">
        <Check aria-hidden="true" size={14} />{t("等待审阅", "Pending review")} {mutation.data.edit.id.slice(0, 12)}
      </span> : <button className="compact-command" disabled={invalid || mutation.isPending}
        onClick={() => mutation.mutate(draft)} type="button">
        {mutation.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
          : <FileDiff aria-hidden="true" size={14} />}
        {t("创建提案", "Create proposal")}
      </button>}
    </footer>
    {(recovery.draft || recovery.error) && <div role={recovery.persisted ? "status" : "alert"}>
      {recovery.persisted ? t("草稿已保存在本机", "Draft saved locally")
        : recovery.error ?? t("草稿仅保留在本窗口", "Draft is only kept in this window")}
    </div>}
    {recovery.changedBase && <div className="inline-warning" role="alert">
      <p>{t("文件自草稿保存后已变化。请核对差异，再以当前文件创建提案。", "The file has changed since this draft was saved. Review the diff before creating a proposal against the current file.")}</p>
      <button onClick={() => setMode("diff")} type="button">{t("核对差异", "Review diff")}</button>
      <button onClick={recovery.rebase} type="button">{t("已核对，使用当前版本", "Use current version after review")}</button>
    </div>}
    {bytes > 256 * 1024 && <div className="inline-warning" role="alert">
      {t("提案超过 Go 控制平面的 256 KiB 上限", "Proposal exceeds the 256 KiB Go boundary")}
    </div>}
    {mutation.isError && <ErrorState error={mutation.error} />}
    {refresh.isError && <ErrorState error={refresh.error} />}
  </section>;
}

function languageForPath(path: string): string {
  const extension = path.includes(".") ? path.slice(path.lastIndexOf(".") + 1).toLowerCase() : "";
  return ({ go: "go", ts: "typescript", tsx: "typescript", js: "javascript",
    jsx: "javascript", json: "json", md: "markdown", py: "python", rs: "rust",
    css: "css", html: "html", yaml: "yaml", yml: "yaml", sh: "shell" } as Record<string, string>)[extension] ?? "plaintext";
}
