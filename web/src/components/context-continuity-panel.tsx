import { useMemo, useState, type CSSProperties, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  GitBranch,
  GitFork,
  History,
  MemoryStick,
  Plus,
  RefreshCw,
  Save,
  ShieldOff,
  Trash2,
} from "lucide-react";
import type { APIClient } from "../api/client";
import type {
  ContextMemoryExportView,
  ContextMemoryView,
  ContinuityBranchView,
  ContinuityNodeView,
  ProjectInstructionStateView,
  SessionTreeNodeView,
  SessionTreeView,
} from "../api/types";
import { formatDate, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { v2QueryKeys } from "../v2/query-keys";
import { ErrorState, LoadingState, StatusBadge } from "./common";
import { MemoryCandidatesPanel } from "./memory-candidates-panel";

type MemoryScope = "project" | "user";
type BranchKind = "fork" | "resume";

interface MemoryDraft {
  title: string;
  content: string;
  sourceRef: string;
  references: string;
  retention: string;
  redactSensitive: boolean;
}

const emptyMemoryDraft: MemoryDraft = {
  title: "",
  content: "",
  sourceRef: "",
  references: "",
  retention: "",
  redactSensitive: false,
};

export function ContextContinuityPanel({ client, runID, sessionID, workspaceID, onOpenRun }: {
  client: APIClient;
  runID: string;
  sessionID: string;
  workspaceID: string;
  onOpenRun?: (runID: string) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [memoryScope, setMemoryScope] = useState<MemoryScope>(workspaceID ? "project" : "user");
  const [memoryDraft, setMemoryDraft] = useState<MemoryDraft>(emptyMemoryDraft);
  const [editingMemoryID, setEditingMemoryID] = useState("");
  const [editDraft, setEditDraft] = useState<MemoryDraft>(emptyMemoryDraft);
  const [checkpointTitle, setCheckpointTitle] = useState("");
  const [checkpointSummary, setCheckpointSummary] = useState("");
  const [branchGoal, setBranchGoal] = useState("");
  const [compareLeftID, setCompareLeftID] = useState("");
  const [compareRightID, setCompareRightID] = useState("");

  const scopeID = memoryScope === "project" ? workspaceID : "local-user";
  const instructionKey = ["run", runID, "project-instructions"] as const;
  const memoryKey = ["context-memories", memoryScope, scopeID] as const;
  const treeKey = ["session", sessionID, "continuity-tree"] as const;

  const instructionsQuery = useQuery({
    queryKey: instructionKey,
    queryFn: ({ signal }) => client.get<ProjectInstructionStateView>(
      `/runs/${encodeURIComponent(runID)}/project-instructions`, {}, signal),
    enabled: Boolean(runID && workspaceID),
  });
  const memoriesQuery = useQuery({
    queryKey: memoryKey,
    queryFn: ({ signal }) => client.get<ContextMemoryView[]>("/memories", {
      scope: memoryScope,
      scope_id: scopeID,
      include_disabled: true,
      include_expired: true,
      limit: 500,
    }, signal),
    enabled: memoryScope === "user" || Boolean(scopeID),
  });
  const treeQuery = useQuery({
    queryKey: treeKey,
    queryFn: ({ signal }) => client.get<SessionTreeView>(
      `/sessions/${encodeURIComponent(sessionID)}/tree`, {}, signal),
    enabled: Boolean(sessionID),
  });

  const refreshInstructions = useMutation({
    mutationFn: (state: ProjectInstructionStateView) => client.postControl<ProjectInstructionStateView>(
      `/runs/${encodeURIComponent(runID)}/project-instructions/refresh`, {
        target_path: state.live.target_path,
        expected_fingerprint: state.pinned.snapshot.fingerprint,
        expected_live_fingerprint: state.live.fingerprint,
        confirm: true,
      }, `web-instruction-refresh-${globalThis.crypto.randomUUID()}`),
    onSuccess: (state) => queryClient.setQueryData(instructionKey, state),
  });

  const createMemory = useMutation({
    mutationFn: (draft: MemoryDraft) => client.postControl<ContextMemoryView>("/memories", {
      scope: memoryScope,
      scope_id: scopeID,
      title: draft.title,
      content: draft.content,
      source_ref: draft.sourceRef || undefined,
      references: memoryReferenceList(draft.references),
      retention_until: retentionTimestamp(draft.retention),
      redact_sensitive: draft.redactSensitive,
    }, `web-memory-create-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      setMemoryDraft(emptyMemoryDraft);
      void queryClient.invalidateQueries({ queryKey: memoryKey });
    },
  });

  const updateMemory = useMutation({
    mutationFn: ({ memory, draft, status }: {
      memory: ContextMemoryView;
      draft?: MemoryDraft;
      status?: "active" | "disabled";
    }) => client.patchControl<ContextMemoryView>(`/memories/${encodeURIComponent(memory.id)}`, {
      expected_version: memory.version,
      title: draft?.title,
      content: draft?.content,
      source_ref: draft?.sourceRef,
      references: draft ? memoryReferenceList(draft.references) : undefined,
      retention_until: draft ? retentionTimestamp(draft.retention) ?? null : undefined,
      status,
      redact_sensitive: draft?.redactSensitive ?? false,
    }),
    onSuccess: () => {
      setEditingMemoryID("");
      void queryClient.invalidateQueries({ queryKey: memoryKey });
      void queryClient.invalidateQueries({ queryKey: treeKey });
    },
  });

  const deleteMemory = useMutation({
    mutationFn: (memory: ContextMemoryView) => client.deleteControl<{
      id: string;
      deleted: boolean;
      recoverable: boolean;
    }>(`/memories/${encodeURIComponent(memory.id)}`, { expected_version: memory.version }),
    onSuccess: () => {
      setEditingMemoryID("");
      void queryClient.invalidateQueries({ queryKey: memoryKey });
      void queryClient.invalidateQueries({ queryKey: treeKey });
    },
  });

  const createCheckpoint = useMutation({
    mutationFn: () => client.postControl<ContinuityNodeView>(
      `/runs/${encodeURIComponent(runID)}/continuity-checkpoints`, {
        title: checkpointTitle,
        summary: checkpointSummary,
      }, `web-continuity-checkpoint-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      setCheckpointTitle("");
      setCheckpointSummary("");
      void queryClient.invalidateQueries({ queryKey: treeKey });
    },
  });

  const branch = useMutation({
    mutationFn: ({ node, kind }: { node: SessionTreeNodeView; kind: BranchKind }) =>
      client.postControl<ContinuityBranchView>(
        `/continuity-nodes/${encodeURIComponent(node.id)}/${kind}`,
        { goal: branchGoal || undefined },
        `web-continuity-${kind}-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["runs"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.inspectorRecords });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.workspaces });
    },
  });

  const branchFrom = (node: SessionTreeNodeView, kind: BranchKind) => {
    // Observer callbacks stop on unmount; background completion still refreshes
    // the records above, but must not take navigation away from another page.
    branch.mutate({ node, kind }, { onSuccess: (result) => onOpenRun?.(result.run.id) });
  };

  const exportMemories = useMutation({
    mutationFn: () => client.get<ContextMemoryExportView>("/memories/export", {
      scope: memoryScope,
      scope_id: scopeID,
    }),
    onSuccess: (value) => downloadJSON(
      `prayu-${memoryScope}-memory-${new Date().toISOString().slice(0, 10)}.json`, value),
  });

  const treeNodes = treeQuery.data?.nodes ?? [];
  const depths = useMemo(() => continuityDepths(treeNodes), [treeNodes]);
  const compareLeft = treeNodes.find((node) => node.id === compareLeftID) ?? treeNodes.at(-2);
  const compareRight = treeNodes.find((node) => node.id === compareRightID) ?? treeNodes.at(-1);

  const submitMemory = (event: FormEvent) => {
    event.preventDefault();
    if (memoryDraft.title.trim() && memoryDraft.content.trim()) createMemory.mutate(memoryDraft);
  };

  return <div className="context-continuity">
    <section className="context-boundary" aria-label={t("上下文安全边界", "Context safety boundary")}>
      <ShieldOff aria-hidden="true" size={18} />
      <div>
        <strong>{t("带上上下文，重新核对权限", "Carry context forward and review permissions")}</strong>
        <p>{t(
          "这里保存指令、记忆和历史快照。分支或恢复后，请按新执行的状态核对审批、能力、凭据、网络权限、进程、终端租约和执行档位。",
          "Save instructions, memories, and historical snapshots here. After branching or resuming, review approvals, capabilities, credentials, network access, processes, terminal leases, and execution profiles for the new execution.",
        )}</p>
      </div>
    </section>

    <ProjectInstructionsSection client={client} query={instructionsQuery}
      refreshing={refreshInstructions.isPending} refreshError={refreshInstructions.error}
      onRefresh={() => instructionsQuery.data && refreshInstructions.mutate(instructionsQuery.data)} />

    <section className="context-section">
      <header className="context-section-header">
        <div><MemoryStick aria-hidden="true" size={17} />
          <span><strong>{t("长期记忆", "Long-term memory")}</strong>
            <small>{t("由你选择并保存，可设置保留期、禁用、导出或永久删除", "Choose what to save, with retention, disable, export, and permanent deletion controls")}</small>
          </span>
        </div>
        <div className="context-header-actions">
          <select aria-label={t("记忆范围", "Memory scope")} value={memoryScope}
            onChange={(event) => setMemoryScope(event.target.value as MemoryScope)}>
            <option disabled={!workspaceID} value="project">{t("当前项目", "Current project")}</option>
            <option value="user">{t("本机用户", "Local user")}</option>
          </select>
          <button className="compact-command" disabled={exportMemories.isPending}
            onClick={() => exportMemories.mutate()} type="button">
            <Save aria-hidden="true" size={13} />{t("导出", "Export")}
          </button>
        </div>
      </header>
      {!client.hasControl && <div className="inline-warning">{t(
        "当前连接为只读；可检查和导出记忆，但写入操作需要控制令牌。",
        "This connection is read-only. Memory inspection and export remain available; writes require a control token.",
      )}</div>}
      {memoryScope === "project" && workspaceID && <MemoryCandidatesPanel key={workspaceID}
        client={client} workspaceID={workspaceID} onOpenRun={onOpenRun} />}
      <form className="context-memory-form" onSubmit={submitMemory}>
        <input aria-label={t("记忆标题", "Memory title")} maxLength={256}
          onChange={(event) => setMemoryDraft((draft) => ({ ...draft, title: event.target.value }))}
          placeholder={t("明确、可验证的标题", "Specific, verifiable title")}
          value={memoryDraft.title} />
        <textarea aria-label={t("记忆内容", "Memory content")} maxLength={16 * 1024}
          onChange={(event) => setMemoryDraft((draft) => ({ ...draft, content: event.target.value }))}
          placeholder={t("填写可复用的偏好或事实，凭据请使用专用设置", "Add reusable preferences or facts; manage credentials in their dedicated settings")}
          value={memoryDraft.content} />
        <input aria-label={t("记忆引用", "Memory references")} maxLength={16 * 1024}
          onChange={(event) => setMemoryDraft((draft) => ({ ...draft, references: event.target.value }))}
          placeholder={t("可选引用，以逗号或换行分隔", "Optional references, separated by commas or new lines")}
          value={memoryDraft.references} />
        <div className="context-form-row">
          <input aria-label={t("来源引用", "Source reference")} maxLength={512}
            onChange={(event) => setMemoryDraft((draft) => ({ ...draft, sourceRef: event.target.value }))}
            placeholder={t("可选来源引用", "Optional source reference")} value={memoryDraft.sourceRef} />
          <label>{t("保留至", "Retain until")}<input type="date" value={memoryDraft.retention}
            onChange={(event) => setMemoryDraft((draft) => ({ ...draft, retention: event.target.value }))} /></label>
          <label className="context-checkbox"><input checked={memoryDraft.redactSensitive} type="checkbox"
            onChange={(event) => setMemoryDraft((draft) => ({ ...draft, redactSensitive: event.target.checked }))} />
            {t("检测到敏感值时显式脱敏", "Explicitly redact detected sensitive values")}</label>
          <button className="command-button" disabled={!client.hasControl || createMemory.isPending ||
            !memoryDraft.title.trim() || !memoryDraft.content.trim()} type="submit">
            <Plus aria-hidden="true" size={14} />{t("写入记忆", "Create memory")}
          </button>
        </div>
      </form>
      {createMemory.error && <div className="inline-warning">{humanError(createMemory.error)}</div>}
      {updateMemory.error && <div className="inline-warning">{humanError(updateMemory.error)}</div>}
      {deleteMemory.error && <div className="inline-warning">{humanError(deleteMemory.error)}</div>}
      {exportMemories.error && <div className="inline-warning">{humanError(exportMemories.error)}</div>}
      {memoriesQuery.isLoading ? <LoadingState label={t("加载记忆", "Loading memories")} /> :
        memoriesQuery.isError ? <ErrorState error={memoriesQuery.error} /> :
          <div className="context-memory-list">
            {(memoriesQuery.data ?? []).map((memory) => {
              const editing = editingMemoryID === memory.id;
              const expired = Boolean(memory.retention_until && Date.parse(memory.retention_until) <= Date.now());
              return <article key={memory.id}>
                <header>
                  <span><strong>{memory.title}</strong><code>{shortID(memory.id)} · v{memory.version}</code></span>
                  <StatusBadge status={expired ? "expired" : memory.status} />
                  <time>{formatDate(memory.updated_at)}</time>
                </header>
                {editing ? <div className="context-memory-editor">
                  <input aria-label={t("编辑标题", "Edit title")} maxLength={256} value={editDraft.title}
                    onChange={(event) => setEditDraft((draft) => ({ ...draft, title: event.target.value }))} />
                  <textarea aria-label={t("编辑内容", "Edit content")} maxLength={16 * 1024}
                    value={editDraft.content}
                    onChange={(event) => setEditDraft((draft) => ({ ...draft, content: event.target.value }))} />
                  <input aria-label={t("编辑引用", "Edit references")} maxLength={16 * 1024}
                    value={editDraft.references}
                    onChange={(event) => setEditDraft((draft) => ({ ...draft, references: event.target.value }))} />
                  <div className="context-form-row">
                    <input aria-label={t("编辑来源引用", "Edit source reference")} maxLength={512}
                      value={editDraft.sourceRef}
                      onChange={(event) => setEditDraft((draft) => ({ ...draft, sourceRef: event.target.value }))} />
                    <label>{t("保留至", "Retain until")}<input type="date" value={editDraft.retention}
                      onChange={(event) => setEditDraft((draft) => ({ ...draft, retention: event.target.value }))} /></label>
                    <label className="context-checkbox"><input checked={editDraft.redactSensitive} type="checkbox"
                      onChange={(event) => setEditDraft((draft) => ({ ...draft, redactSensitive: event.target.checked }))} />
                      {t("允许脱敏", "Allow redaction")}</label>
                  </div>
                  <div className="context-row-actions">
                    <button className="compact-command" disabled={updateMemory.isPending ||
                      !editDraft.title.trim() || !editDraft.content.trim()}
                      onClick={() => updateMemory.mutate({ memory, draft: editDraft })} type="button">
                      <Save aria-hidden="true" size={13} />{t("保存", "Save")}
                    </button>
                    <button className="compact-command" onClick={() => setEditingMemoryID("")}
                      type="button">{t("取消", "Cancel")}</button>
                  </div>
                </div> : <>
                  <p>{memory.content}</p>
                  <footer>
                    <span>{memory.source_ref || t("用户显式输入", "Explicit operator input")}</span>
                    {memory.references.length > 0 && <span>{t("引用", "References")}: {memory.references.join(", ")}</span>}
                    <span>{memory.retention_until ? `${t("保留至", "Retained until")} ${formatDate(memory.retention_until)}` :
                      t("无到期时间", "No expiry")}</span>
                    {memory.redacted && <span>{t("已脱敏", "Redacted")}</span>}
                    <div className="context-row-actions">
                      <button className="compact-command" disabled={!client.hasControl} onClick={() => {
                        setEditingMemoryID(memory.id);
                        setEditDraft({ title: memory.title, content: memory.content,
                          sourceRef: memory.source_ref ?? "", references: memory.references.join("\n"),
                          retention: memory.retention_until?.slice(0, 10) ?? "",
                          redactSensitive: memory.redacted });
                      }} type="button">{t("编辑", "Edit")}</button>
                      <button className="compact-command" disabled={!client.hasControl || updateMemory.isPending}
                        onClick={() => updateMemory.mutate({ memory,
                          status: memory.status === "active" ? "disabled" : "active" })} type="button">
                        {memory.status === "active" ? t("禁用", "Disable") : t("启用", "Enable")}
                      </button>
                      <button className="compact-command danger" disabled={!client.hasControl || deleteMemory.isPending}
                        onClick={() => {
                          if (globalThis.confirm(t("永久删除这条记忆？此操作不可恢复。", "Permanently delete this memory? This cannot be recovered."))) {
                            deleteMemory.mutate(memory);
                          }
                        }} type="button"><Trash2 aria-hidden="true" size={12} />{t("永久删除", "Delete permanently")}</button>
                    </div>
                  </footer>
                </>}
              </article>;
            })}
            {(memoriesQuery.data?.length ?? 0) === 0 && <p className="context-empty">{t(
              "此范围还没有记忆。在上方填写要保留的偏好或事实，点击「写入记忆」保存。",
              "This scope has no saved memories. Add a preference or fact above, then choose Create memory to save it.",
            )}</p>}
          </div>}
    </section>

    <section className="context-section">
      <header className="context-section-header">
        <div><History aria-hidden="true" size={17} />
          <span><strong>{t("上下文分支与检查点", "Context branches and checkpoints")}</strong>
            <small>{t("保存一个继续工作的起点，或将已有上下文复制到独立执行", "Save a starting point for later work or copy existing context into an independent execution")}</small>
          </span>
        </div>
      </header>
      <div className="context-checkpoint-form">
        <input aria-label={t("检查点标题", "Checkpoint title")} maxLength={1024}
          onChange={(event) => setCheckpointTitle(event.target.value)}
          placeholder={t("检查点标题（可选）", "Checkpoint title (optional)")} value={checkpointTitle} />
        <input aria-label={t("检查点摘要", "Checkpoint summary")} maxLength={4096}
          onChange={(event) => setCheckpointSummary(event.target.value)}
          placeholder={t("简短摘要（可选）", "Short summary (optional)")} value={checkpointSummary} />
        <button className="command-button" disabled={!client.hasControl || createCheckpoint.isPending}
          onClick={() => createCheckpoint.mutate()} type="button">
          <Plus aria-hidden="true" size={14} />{t("创建检查点", "Create checkpoint")}
        </button>
      </div>
      <input aria-label={t("新分支目标", "New branch goal")} className="context-branch-goal"
        maxLength={4096} onChange={(event) => setBranchGoal(event.target.value)}
        placeholder={t("可选：填写新分支要继续完成的目标", "Optional: set the goal to continue in the new branch")}
        value={branchGoal} />
      {createCheckpoint.error && <div className="inline-warning">{humanError(createCheckpoint.error)}</div>}
      {branch.error && <div className="inline-warning">{humanError(branch.error)}</div>}
      {treeQuery.isLoading ? <LoadingState label={t("加载 Run 内 Session 树", "Loading Run-local Session tree")} /> :
        treeQuery.isError ? <ErrorState error={treeQuery.error} /> : <>
          <div className="continuity-tree" role="tree">
            {treeNodes.map((node) => <article key={node.id} role="treeitem"
              style={{ "--context-depth": depths.get(node.id) ?? 0 } as CSSProperties}>
              <span className="continuity-line" />
              <GitBranch aria-hidden="true" size={15} />
              <div>
                <header><strong>{node.title}</strong><StatusBadge status={node.status} /></header>
                <p>{node.summary || `${node.kind} · ${shortID(node.run_id)}`}</p>
                <small>{node.kind} · {formatDate(node.created_at)} · {node.fingerprint?.slice(0, 12) || "derived"}</small>
                {node.git_branch && <small>{node.git_branch} · {node.git_head?.slice(0, 12) || "no HEAD"}</small>}
                {node.warnings.map((warning) => <span className="context-node-warning" key={warning}>{warning}</span>)}
              </div>
              {!node.derived && <div className="context-row-actions">
                <button className="compact-command" disabled={!client.hasControl || branch.isPending}
                  onClick={() => branchFrom(node, "fork")} type="button">
                  <GitFork aria-hidden="true" size={12} />Fork
                </button>
                <button className="compact-command" disabled={!client.hasControl || branch.isPending}
                  onClick={() => branchFrom(node, "resume")} type="button">
                  <RefreshCw aria-hidden="true" size={12} />Resume
                </button>
              </div>}
            </article>)}
          </div>
          {treeNodes.length === 0 && <p className="context-empty">{t("先创建检查点，再选择从该上下文分支或继续。", "Create a checkpoint, then choose to branch or continue from its context.")}</p>}
          {treeNodes.length > 1 && <div className="context-branch-compare">
            <header><strong>{t("分支比较", "Branch comparison")}</strong></header>
            <div>
              <select aria-label={t("比较左侧节点", "Left comparison node")}
                value={compareLeft?.id ?? ""} onChange={(event) => setCompareLeftID(event.target.value)}>
                {treeNodes.map((node) => <option key={node.id} value={node.id}>{node.title} · {shortID(node.id)}</option>)}
              </select>
              <span>↔</span>
              <select aria-label={t("比较右侧节点", "Right comparison node")}
                value={compareRight?.id ?? ""} onChange={(event) => setCompareRightID(event.target.value)}>
                {treeNodes.map((node) => <option key={node.id} value={node.id}>{node.title} · {shortID(node.id)}</option>)}
              </select>
            </div>
            {compareLeft && compareRight && <dl>
              <dt>{t("上下文指纹", "Context fingerprint")}</dt>
              <dd className={compareLeft.fingerprint === compareRight.fingerprint ? "same" : "changed"}>
                {compareLeft.fingerprint === compareRight.fingerprint ? t("相同", "same") : t("不同", "different")}
              </dd>
              <dt>Run</dt><dd>{shortID(compareLeft.run_id)} → {shortID(compareRight.run_id)}</dd>
              <dt>{t("Run 内 Session", "Run-local Session")}</dt><dd>{shortID(compareLeft.session_id)} → {shortID(compareRight.session_id)}</dd>
              <dt>{t("项目配置", "Project config")}</dt><dd>
                {shortFingerprint(compareLeft.project_config_fingerprint)} → {shortFingerprint(compareRight.project_config_fingerprint)}
              </dd>
              <dt>{t("项目指令", "Project instructions")}</dt><dd>
                {shortFingerprint(compareLeft.project_instructions_fingerprint)} → {shortFingerprint(compareRight.project_instructions_fingerprint)}
              </dd>
              <dt>Git</dt><dd>
                {compareLeft.git_branch || "—"}@{shortFingerprint(compareLeft.git_head)} → {compareRight.git_branch || "—"}@{shortFingerprint(compareRight.git_head)}
              </dd>
              <dt>{t("状态", "Status")}</dt><dd>{compareLeft.status} → {compareRight.status}</dd>
            </dl>}
          </div>}
        </>}
    </section>
  </div>;
}

function ProjectInstructionsSection({ client, query, refreshing, refreshError, onRefresh }: {
  client: APIClient;
  query: ReturnType<typeof useQuery<ProjectInstructionStateView>>;
  refreshing: boolean;
  refreshError: Error | null;
  onRefresh: () => void;
}) {
  const { t } = useLocale();
  if (query.isLoading) return <section className="context-section"><LoadingState label={t("加载项目指令", "Loading project instructions")} /></section>;
  if (query.isError || !query.data) return <section className="context-section"><ErrorState error={query.error} /></section>;
  const state = query.data;
  const snapshot = state.pinned_present ? state.pinned.snapshot : state.live;
  return <section className="context-section">
    <header className="context-section-header">
      <div><GitBranch aria-hidden="true" size={17} />
        <span><strong>{t("项目指令快照", "Project instruction snapshot")}</strong>
          <small>{t("本次执行使用创建时保存的指令；核对差异并确认后可更新", "This execution uses the instructions saved at creation; review and confirm differences to update them")}</small>
        </span>
      </div>
      <div className="context-header-actions">
        <StatusBadge status={state.stale ? "stale" : state.pinned_present ? "pinned" : "uninitialized"} />
        {state.stale && <button className="command-button" disabled={!client.hasControl || refreshing}
          onClick={onRefresh} type="button"><RefreshCw aria-hidden="true" size={14} />
          {state.pinned_present ? t("确认并刷新", "Confirm refresh") : t("确认并固定", "Confirm and pin")}</button>}
      </div>
    </header>
    {state.stale && <div className="inline-warning">{t(
      "磁盘指令已有变化。本次执行仍使用原快照；核对下方差异后点击确认更新。",
      "Instructions on disk have changed. This execution uses the prior snapshot; review the differences below and confirm the update.",
    )}</div>}
    {refreshError && <div className="inline-warning">{humanError(refreshError)}</div>}
    <dl className="context-fingerprint-grid">
      <dt>{t("固定指纹", "Pinned fingerprint")}</dt><dd>{state.pinned.snapshot.fingerprint || t("尚未固定", "not pinned")}</dd>
      <dt>{t("磁盘指纹", "Live fingerprint")}</dt><dd>{state.live.fingerprint}</dd>
      <dt>{t("目标路径", "Target path")}</dt><dd>{state.live.target_path || "."}</dd>
      <dt>{t("能力授予", "Capability grant")}</dt><dd>{String(state.capability_grant)}</dd>
    </dl>
    {state.diff.requires_confirmation && <div className="instruction-diff-summary">
      <span>+ {state.diff.added.join(", ") || "—"}</span>
      <span>~ {state.diff.changed.join(", ") || "—"}</span>
      <span>− {state.diff.removed.join(", ") || "—"}</span>
      {state.diff.order_changed && <span>{t("优先级顺序已变化", "Precedence order changed")}</span>}
    </div>}
    <div className="instruction-source-list">
      {snapshot.sources.map((source) => <details key={`${source.path}:${source.ordinal}`}>
        <summary>
          <span><strong>{source.path}</strong><small>{source.kind} · {source.scope}</small></span>
          <span>{t("优先级", "precedence")} {source.precedence}</span>
        </summary>
        <p><strong>{t("生效原因：", "Why effective: ")}</strong>{source.why_effective}</p>
        <p><strong>{t("适用范围：", "Applies to: ")}</strong>{source.applicable_to.join(", ")}</p>
        <p><strong>{t("可信级别：", "Trust: ")}</strong>{source.trust}; {t("作为上下文使用，执行授权需单独审批", "used as context; execution authority requires separate approval")}</p>
        <pre>{source.content}</pre>
      </details>)}
      {snapshot.sources.length === 0 && <p className="context-empty">{t("此路径尚无项目指令。可在项目中添加适用的指令文件，再确认刷新。", "This path has no project instructions. Add an applicable instruction file to the project, then confirm a refresh.")}</p>}
    </div>
    {snapshot.conflicts.length > 0 && <div className="instruction-conflicts">
      <strong>{t("优先级冲突", "Precedence conflicts")}</strong>
      {snapshot.conflicts.map((conflict) => <p key={`${conflict.higher_precedence_path}:${conflict.lower_precedence_path}`}>
        {conflict.higher_precedence_path} ← {conflict.lower_precedence_path}: {conflict.resolution}
      </p>)}
    </div>}
  </section>;
}

function retentionTimestamp(value: string): string | undefined {
  if (!value) return undefined;
  const [year, month, day] = value.split("-").map(Number);
  return new Date(year, month - 1, day, 23, 59, 59, 999).toISOString();
}

function memoryReferenceList(value: string): string[] {
  return [...new Set(value.split(/[\n,]/u).map((item) => item.trim()).filter(Boolean))];
}

function shortFingerprint(value?: string): string {
  return value?.slice(0, 12) || "—";
}

function continuityDepths(nodes: SessionTreeNodeView[]): Map<string, number> {
  const byID = new Map(nodes.map((node) => [node.id, node]));
  const depths = new Map<string, number>();
  const visit = (node: SessionTreeNodeView, seen = new Set<string>()): number => {
    const cached = depths.get(node.id);
    if (cached !== undefined) return cached;
    if (seen.has(node.id)) return 0;
    seen.add(node.id);
    const linkedID = node.parent_id || node.source_node_id;
    const linked = linkedID ? byID.get(linkedID) : undefined;
    const depth = linked ? Math.min(8, visit(linked, seen) + 1) : 0;
    depths.set(node.id, depth);
    return depth;
  };
  nodes.forEach((node) => visit(node));
  return depths;
}

function downloadJSON(filename: string, value: unknown): void {
  const url = URL.createObjectURL(new Blob([`${JSON.stringify(value, null, 2)}\n`], {
    type: "application/json",
  }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}

function humanError(value: unknown): string {
  return value instanceof Error ? value.message : String(value);
}
