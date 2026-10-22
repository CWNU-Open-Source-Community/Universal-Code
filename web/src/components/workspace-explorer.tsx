import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, type UseQueryResult } from "@tanstack/react-query";
import { ArrowLeft, File, Folder, FolderOpen, Paperclip, Pencil, Search, ShieldCheck, X } from "lucide-react";
import type { APIClient } from "../api/client";
import type { WorkspaceExplorerView, WorkspaceSearchView } from "../api/types";
import { formatBytes } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import { WorkspaceFileQuote, type WorkspaceQuoteResult } from "./workspace-file-quote";
import { FileProposalEditor } from "./file-proposal-editor";
import { useFileEditLeave } from "./file-edit-drafts";

export function WorkspaceExplorer({ client, workspaceID, runID = "", initialPath = ".", initialLine, onSelectReference, onPathChange, onQuote, onReturnToDraft }: {
  client: APIClient;
  workspaceID: string;
  runID?: string;
  initialPath?: string;
  initialLine?: number;
  onSelectReference?: (file: WorkspaceExplorerView) => void;
  onPathChange?: (path: string) => void;
  onQuote?: (content: string) => WorkspaceQuoteResult;
  onReturnToDraft?: () => void;
}) {
  const { t } = useLocale();
  const leave = useFileEditLeave();
  const [path, setPath] = useState(initialPath);
  const [highlightLine, setHighlightLine] = useState<number | undefined>(initialLine);
  const highlightedLineRef = useRef<HTMLDivElement>(null);
  const explorerRef = useRef<HTMLElement>(null);
  const [searchInput, setSearchInput] = useState("");
  const [searchQuery, setSearchQuery] = useState("");
  const operationKeys = useRef(new Map<string, string>());
  useEffect(() => {
    setPath(initialPath);
    setHighlightLine(initialLine);
    setSearchInput("");
    setSearchQuery("");
    operationKeys.current.clear();
    proposalSource.reset();
  }, [workspaceID, runID, initialPath, initialLine]);
  const query = useQuery({
    queryKey: ["workspace", workspaceID, "explore", path],
    queryFn: ({ signal }) => client.workspaceExplore(workspaceID, path, signal),
    enabled: Boolean(workspaceID),
  });
  useEffect(() => {
    if (highlightLine && highlightedLineRef.current) {
      highlightedLineRef.current.scrollIntoView?.({ block: "center", behavior: "smooth" });
    }
  }, [highlightLine, path, query.data]);
  const search = useQuery({
    queryKey: ["workspace", workspaceID, "search", searchQuery],
    queryFn: ({ signal }) => client.workspaceSearch(workspaceID, searchQuery, signal),
    enabled: Boolean(workspaceID && searchQuery),
  });
  const attachment = useMutation({
    mutationFn: ({ sourceRef, contentSHA256 }: {
      sourceRef: string;
      contentSHA256: string;
    }) => client.attachEvidence(runID, {
      version: "session_evidence_attachment.v1",
      source_kind: "workspace_file",
      source_ref: sourceRef,
      content_sha256: contentSHA256,
    }, evidenceOperationKey(operationKeys.current, sourceRef, contentSHA256)),
  });
  const proposalSource = useMutation({
    mutationFn: (sourcePath: string) => client.issueFileEditProposalSource(runID, sourcePath),
  });
  const parent = useMemo(() => parentPath(path), [path]);
  const navigate = (nextPath: string) => leave(() => {
    proposalSource.reset();
    setPath(nextPath);
    onPathChange?.(nextPath);
  });

  const submitSearch = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const normalized = searchInput.trim();
    if (normalized && normalized === searchInput && normalized.length <= 128) {
      setSearchQuery(normalized);
    }
  };

  const attach = (sourceRef: string, contentSHA256: string) => {
    attachment.mutate({ sourceRef, contentSHA256 });
  };

  if (!workspaceID) return <section className="workspace-explorer">
    <EmptyState>{t("绑定项目后可查看文件，请返回对话核对项目选择。", "Bind a project to inspect files. Return to the conversation to review the project selection.")}</EmptyState>
  </section>;
  if (query.isLoading) return <section className="workspace-explorer">
    <LoadingState label={t("正在加载工作区文件", "Loading Workspace files")} />
  </section>;
  if (query.isError || !query.data) return <section className="workspace-explorer">
    <div className="explorer-error">
      <h4>{t("无法读取工作区文件", "Unable to read Workspace files")}</h4>
      <p className="explorer-error-hint" role="alert">{t("目标路径读取失败，请重试或返回上级目录确认文件位置。", "Reading this path failed. Retry or return to the parent directory to locate the file.")}</p>
      <div className="explorer-error-actions">
        <button className="compact-command" disabled={query.isFetching}
          onClick={() => void query.refetch()} type="button">{t("重试文件读取", "Retry file read")}</button>
        <button className="compact-command" disabled={path === "." || query.isFetching}
          onClick={() => navigate(parent)} type="button">{t("返回上级目录", "Return to parent")}</button>
      </div>
      <details className="explorer-error-details">
        <summary>{t("错误详情", "Error details")}</summary>
        <ErrorState error={query.error} />
      </details>
    </div>
  </section>;
  const snapshot = query.data;

  return <section className="workspace-explorer" aria-label={t("工作区文件", "Workspace files")} ref={explorerRef}>
    <header className="explorer-toolbar">
      <button aria-label={t("打开上级目录", "Open parent directory")} className="icon-button"
        disabled={path === "." || query.isFetching} onClick={() => navigate(parent)}
        title={t("上级目录", "Parent directory")} type="button">
        <ArrowLeft aria-hidden="true" size={16} />
      </button>
      <FolderOpen aria-hidden="true" size={16} />
      <code>{snapshot.path}</code>
      {snapshot.truncated && <StatusBadge status="truncated" />}
      {snapshot.kind === "file" && onSelectReference && <button className="compact-command"
        disabled={query.isFetching} onClick={() => onSelectReference(snapshot)} type="button">
        <Paperclip aria-hidden="true" size={14} />{t("引用此文件", "Reference this file")}</button>}
      {snapshot.kind === "file" && !onSelectReference && client.hasEvidenceAttachment && runID &&
        <button className="compact-command" disabled={attachment.isPending}
          onClick={() => attach(snapshot.path, snapshot.provenance.content_sha256)} type="button">
          <Paperclip aria-hidden="true" size={14} />{t("附加证据", "Attach evidence")}
        </button>}
      {snapshot.kind === "file" && !onSelectReference && client.hasFileEditProposals && runID &&
        !snapshot.truncated && snapshot.redaction_count === 0 &&
        <button className="compact-command" disabled={proposalSource.isPending}
          onClick={() => proposalSource.mutate(snapshot.path)} type="button">
          <Pencil aria-hidden="true" size={14} />{t("编辑提案", "Edit proposal")}
        </button>}
    </header>
    <form className="explorer-search" onSubmit={submitSearch} role="search">
      <Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索工作区证据", "Search Workspace evidence")} maxLength={128}
        onChange={(event) => setSearchInput(event.target.value)}
        placeholder={t("搜索文件", "Search files")} type="search" value={searchInput} />
      <button aria-label={t("搜索工作区", "Search Workspace")} className="icon-button"
        disabled={!searchInput.trim() || searchInput.trim() !== searchInput || search.isFetching}
        title={t("搜索", "Search")} type="submit"><Search aria-hidden="true" size={15} /></button>
    </form>
    <div className="explorer-provenance">
      <ShieldCheck aria-hidden="true" size={14} />
      <span>{snapshot.provenance.source_kind} / {t("文件来源记录", "File source record")}</span>
      {snapshot.redaction_count > 0 && <span>{t(`${snapshot.redaction_count} 项已脱敏`, `${snapshot.redaction_count} redacted`)}</span>}
      <code>{snapshot.provenance.content_sha256.slice(0, 12)}</code>
    </div>
    {attachment.isError && <ErrorState error={attachment.error} />}
    {proposalSource.isError && <ErrorState error={proposalSource.error} />}
    {attachment.data && <div className="explorer-attachment-status" role="status">
      <ShieldCheck aria-hidden="true" size={14} />
      {t("证据已作为不具授权效力的上下文附加", "Evidence attached as non-authorizing context")}
      {attachment.data.replayed && <StatusBadge status="replayed" />}
    </div>}
    {searchQuery && <WorkspaceSearchResults client={client} runID={runID}
      pending={attachment.isPending} query={search} onAttach={attach}
      onClear={() => setSearchQuery("")} onOpen={navigate} />}
    {snapshot.kind === "directory" && snapshot.entries.length === 0 &&
      <EmptyState>{t("目录为空", "Directory is empty")}</EmptyState>}
    {snapshot.kind === "directory" && snapshot.entries.length > 0 &&
      <div className="explorer-list" role="list">
        {snapshot.entries.map((entry) => {
          const Icon = entry.kind === "directory" ? Folder : File;
          return <div key={entry.path} role="listitem">
            <button disabled={!entry.readable || query.isFetching}
              onClick={() => navigate(entry.path)} type="button">
              <Icon aria-hidden="true" size={16} />
              <span>{entry.name}</span>
              <small>{entry.kind === "file" ? formatBytes(entry.size_bytes) : entry.kind}</small>
            </button>
          </div>;
        })}
      </div>}
    {snapshot.kind === "file" && onQuote && proposalSource.data?.path !== snapshot.path &&
      <WorkspaceFileQuote key={JSON.stringify([workspaceID, runID, snapshot.path, snapshot.provenance.content_sha256])}
        rootRef={explorerRef} snapshot={snapshot} runID={runID} disabled={query.isFetching}
        onQuote={onQuote} onReturnToDraft={onReturnToDraft} />}
    {proposalSource.data && proposalSource.data.path === snapshot.path ?
      <FileProposalEditor client={client} onClose={() => proposalSource.reset()}
        runID={runID} source={proposalSource.data} /> :
    snapshot.kind === "file" && (() => {
      const lines = snapshot.content.split("\n");
      const lineOutOfRange = highlightLine !== undefined && (highlightLine < 1 || highlightLine > lines.length);
      const lineHighlighted = highlightLine !== undefined && highlightLine >= 1 && highlightLine <= lines.length;
      return <div className="explorer-file">
        <div>
          <span>{t(`已显示 ${formatBytes(snapshot.returned_bytes)}`, `${formatBytes(snapshot.returned_bytes)} shown`)}</span>
          <span>{t(`共 ${formatBytes(snapshot.total_bytes)}`, `${formatBytes(snapshot.total_bytes)} total`)}</span>
        </div>
        {lineOutOfRange && <div className="explorer-line-warning" role="alert">
          {t(`定位到第 ${highlightLine} 行失败：超出当前显示范围（已显示 ${lines.length} 行${snapshot.truncated ? "，文件已截断" : ""}）`,
            `Failed to position line ${highlightLine}: out of loaded range (${lines.length} lines shown${snapshot.truncated ? ", truncated" : ""})`)}
        </div>}
        {lineHighlighted && <div className="explorer-line-notice" role="status">
          {t(`已定位到第 ${highlightLine} 行`, `Positioned at line ${highlightLine}`)}
        </div>}
        <div className="explorer-file-lines">
          {lines.map((lineContent, index) => {
            const lineNum = index + 1;
            const isHighlight = lineNum === highlightLine;
            return <div className={`explorer-file-line${isHighlight ? " is-highlighted" : ""}`}
              key={lineNum} ref={isHighlight ? highlightedLineRef : undefined} data-line={lineNum}>
              <span className="explorer-line-number">{lineNum}</span>
              <span className="explorer-line-content">{lineContent}</span>
            </div>;
          })}
        </div>
      </div>;
    })()}
  </section>;
}

function WorkspaceSearchResults({ client, runID, pending, query, onAttach, onClear, onOpen }: {
  client: APIClient;
  runID: string;
  pending: boolean;
  query: UseQueryResult<WorkspaceSearchView, Error>;
  onAttach: (path: string, digest: string) => void;
  onClear: () => void;
  onOpen: (path: string) => void;
}) {
  const { t } = useLocale();
  if (query.isLoading) return <LoadingState label={t("正在搜索工作区", "Searching Workspace")} />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  const data = query.data;
  return <section className="explorer-search-results" aria-label={t("工作区搜索结果", "Workspace search results")}>
    <header><strong>{t(`${data.results.length} 个结果`, `${data.results.length} results`)}</strong>
      {data.truncated && <StatusBadge status="truncated" />}
      <button aria-label={t("关闭搜索结果", "Close search results")} className="icon-button" onClick={onClear}
        title={t("关闭搜索", "Close search")} type="button"><X aria-hidden="true" size={14} /></button>
    </header>
    {data.results.length === 0 && <EmptyState>{t("没有匹配的证据", "No matching evidence")}</EmptyState>}
    {data.results.map((result) => <div className="explorer-search-result" key={result.path}>
      <button className="search-result-open" onClick={() => onOpen(result.path)} type="button">
        <File aria-hidden="true" size={15} />
        <span><strong>{result.path}</strong>
          <small>{result.line > 0 ? t(`第 ${result.line} 行`, `line ${result.line}`) : result.match_kind}</small>
          {result.snippet && <code>{result.snippet}</code>}</span>
      </button>
      {client.hasEvidenceAttachment && runID &&
        <button aria-label={t(`将 ${result.path} 附加为证据`, `Attach ${result.path} as evidence`)} className="icon-button"
          disabled={pending} onClick={() => onAttach(result.path,
            result.provenance.content_sha256)} title={t("附加不具授权效力的证据", "Attach non-authorizing evidence")} type="button">
          <Paperclip aria-hidden="true" size={14} />
        </button>}
    </div>)}
  </section>;
}

function evidenceOperationKey(keys: Map<string, string>, path: string, digest: string): string {
  const identity = `${path}:${digest}`;
  const existing = keys.get(identity);
  if (existing) return existing;
  const key = `evidence-${crypto.randomUUID()}`;
  keys.set(identity, key);
  return key;
}

function parentPath(path: string): string {
  if (path === "." || !path.includes("/")) return ".";
  return path.slice(0, path.lastIndexOf("/")) || ".";
}
