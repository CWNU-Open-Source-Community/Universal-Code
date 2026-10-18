import { useEffect, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { DiffEditor } from "@monaco-editor/react";
import { AlertTriangle, History, X } from "lucide-react";
import type { APIClient } from "../api/client";
import { useLocale } from "../lib/locale";
import { readPrayuTheme } from "../lib/appearance";
import { ErrorState, LoadingState, StatusBadge } from "./common";

export function FileProposalRecovery({ client, runID, editID, onClose }: {
  client: APIClient;
  runID: string;
  editID: string;
  onClose: () => void;
}) {
  const { t } = useLocale();
  const [editorState, setEditorState] = useState<"loading" | "ready" | "failed">("loading");
  const query = useQuery({
    queryKey: ["run", runID, "file-edit-recovery", editID],
    queryFn: ({ signal }) => client.recoverFileEditProposal(runID, editID, signal),
  });
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
  if (query.isLoading) return <RecoveryStatus onClose={onClose}>
    <LoadingState label={t("正在恢复待审提案", "Recovering pending proposal")} />
  </RecoveryStatus>;
  if (query.isError || !query.data) return <RecoveryStatus onClose={onClose}>
    <ErrorState error={query.error} />
  </RecoveryStatus>;
  const recovery = query.data;
  return <div className="file-proposal-editor file-proposal-recovery"
    aria-label={t(`已恢复 ${recovery.path} 的提案`, `Recovered proposal for ${recovery.path}`)}>
    <header>
      <div><History aria-hidden="true" size={16} /><strong>{recovery.path}</strong>
        <StatusBadge status={recovery.stale ? "stale" : "pending review"} /></div>
      <button aria-label={t("关闭已恢复提案", "Close recovered proposal")} className="icon-button"
        onClick={onClose} title={t("关闭恢复视图", "Close recovery")} type="button">
        <X aria-hidden="true" size={15} />
      </button>
    </header>
    <div className="monaco-proposal-surface">
      {editorState === "ready" ? <DiffEditor height="100%" language={languageForPath(recovery.path)}
        modified={recovery.proposed_content} original={recovery.original_content}
        options={{ automaticLayout: true, fontFamily: 'var(--prayu-font-mono, "JetBrains Mono", Consolas, monospace)',
          fontSize: 13, letterSpacing: 0, minimap: { enabled: false }, readOnly: true, renderSideBySide: true,
          scrollBeyondLastLine: false }} theme={readPrayuTheme() === "light" ? "vs" : "vs-dark"} /> :
        <div className={`monaco-local-state${editorState === "failed" ? " inline-warning" : ""}`}
          role={editorState === "failed" ? "alert" : "status"}>
          {editorState === "failed" ? t("无法加载本地编辑器资源", "Local editor assets could not be loaded") : t("正在加载本地编辑器", "Loading local editor")}
        </div>}
    </div>
    <footer>
      {recovery.stale && <span className="stale-proposal-status">
        <AlertTriangle aria-hidden="true" size={14} />{t("工作区源文件已改变", "Workspace source changed")}</span>}
      <span>{t("原始", "Original")} {recovery.original_sha256.slice(0, 12)}</span>
      <span>{t("提案", "Proposal")} {recovery.proposed_sha256.slice(0, 12)}</span>
    </footer>
  </div>;
}

function RecoveryStatus({ children, onClose }: {
  children: ReactNode;
  onClose: () => void;
}) {
  const { t } = useLocale();
  return <section aria-label={t("文件提案恢复状态", "File proposal recovery status")}
    className="file-proposal-editor file-proposal-recovery">
    <header>
      <div><History aria-hidden="true" size={16} /><strong>{t("提案恢复", "Proposal recovery")}</strong></div>
      <button aria-label={t("关闭已恢复提案", "Close recovered proposal")} className="icon-button"
        onClick={onClose} title={t("关闭恢复视图", "Close recovery")} type="button">
        <X aria-hidden="true" size={15} />
      </button>
    </header>
    {children}
  </section>;
}

function languageForPath(path: string): string {
  const extension = path.includes(".") ? path.slice(path.lastIndexOf(".") + 1).toLowerCase() : "";
  return ({ go: "go", ts: "typescript", tsx: "typescript", js: "javascript",
    jsx: "javascript", json: "json", md: "markdown", py: "python", rs: "rust",
    css: "css", html: "html", yaml: "yaml", yml: "yaml", sh: "shell" } as Record<string, string>)[extension] ?? "plaintext";
}
