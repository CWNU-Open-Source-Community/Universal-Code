import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, GitPullRequest, RefreshCw, ShieldCheck } from "lucide-react";
import { APIRequestError, type APIClient } from "../api/client";
import type { GitHubReviewConnectionView, GitHubReviewCredentialView, GitHubReviewProjectionView,
  GitHubReviewWriteReviewResultView, GitHubReviewWriteSpecView } from "../api/types";
import { formatDate, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, KeyValue, LoadingState, StatusBadge } from "./common";
import { V2ConfirmDialog } from "../v2/components/dialog";
import { GitHubReviewThreads, GitHubReviewWriteForm, githubWriteSupported } from "./github-review-write-form";

function operationKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `desktop-github-review-${crypto.randomUUID()}`;
  }
  return `desktop-github-review-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

type GitHubReviewSourceBinding = {
  runID: string;
  connectionID: string;
  snapshotID: string;
  connectionGeneration: number;
  credentialName: string;
  credentialKind: string;
  clientID: string;
  apiClient: APIClient;
};

// Kept only in the parent's React state, never sent as part of an API request.
type RetainedGitHubReview = GitHubReviewWriteReviewResultView & { sourceBinding?: GitHubReviewSourceBinding };

type GitHubReviewPanelProps = {
  client: APIClient;
  runID: string;
  onOpenApprovals: () => void;
  onOpenDelivery?: () => void;
  retainedReview?: RetainedGitHubReview | null;
  onRetainedReviewChange?: (value: RetainedGitHubReview | null) => void;
};

type ConnectionForm = {
  connection: GitHubReviewConnectionView | null;
  repository: string;
  credentialName: string;
  clientID: string;
  writeEnabled: boolean;
};

type RequestScope = { client: APIClient; runID: string; connectionID: string; revision: number };
type ReviewScope = RequestScope & { snapshotID: string; reviewRevision: number; number: number;
  sourceBinding: GitHubReviewSourceBinding };

function sourceMatches(binding: GitHubReviewSourceBinding | undefined,
  connection: GitHubReviewConnectionView | undefined | null, snapshotID: string | undefined,
  client: APIClient, runID: string): boolean {
  return Boolean(binding && connection && binding.apiClient === client && binding.runID === runID &&
    binding.connectionID === connection.id && binding.snapshotID === snapshotID &&
    binding.connectionGeneration === connection.generation && binding.credentialName === connection.credential.name &&
    binding.credentialKind === connection.credential.kind && binding.clientID === (connection.client_id ?? ""));
}

function connectionForm(connection: GitHubReviewConnectionView | null): ConnectionForm {
  return { connection, repository: connection?.repository.full_name ?? "",
    credentialName: connection?.credential.name ?? "prayu-github-app",
    clientID: connection?.client_id ?? "", writeEnabled: connection?.network.write_enabled ?? false };
}

export function GitHubReviewPanel(props: GitHubReviewPanelProps) {
  // A reviewed write and pending mutations belong to the original Run, even when the selected Run changes.
  const clientContext = useRef({ client: props.client, version: 0 });
  if (clientContext.current.client !== props.client) {
    clientContext.current = { client: props.client, version: clientContext.current.version + 1 };
  }
  return <GitHubReviewWorkspace key={`${props.runID}:${clientContext.current.version}`}
    {...props} clientContext={clientContext} />;
}

function GitHubReviewWorkspace({ client, runID, onOpenApprovals,
  onOpenDelivery, retainedReview, onRetainedReviewChange, clientContext }: GitHubReviewPanelProps & {
  clientContext: RefObject<{ client: APIClient; version: number }>;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [form, setForm] = useState(() => connectionForm(null));
  const [selectionReady, setSelectionReady] = useState(false);
  const connectionID = form.connection?.id ?? "";
  const [pullRequest, setPullRequest] = useState(retainedReview?.preview.identity.number ?? 0);
  const [device, setDevice] = useState<{ session_id: string; user_code: string;
    verification_uri: string } | null>(null);
  const [localReview, setLocalReview] = useState<RetainedGitHubReview | null>(retainedReview ?? null);
  const [writeDraftRevision, setWriteDraftRevision] = useState(0);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");
  const [conflict, setConflict] = useState(false);
  const [snapshotRefreshRequired, setSnapshotRefreshRequired] = useState(false);
  const [disconnectTarget, setDisconnectTarget] = useState<GitHubReviewConnectionView | null>(null);
  const disconnectButton = useRef<HTMLButtonElement>(null);
  const mounted = useRef(true);
  const revision = useRef(0);
  const reviewRevision = useRef(0);
  const current = useRef({ client, connectionID });
  current.current = { client, connectionID };
  const previousRetained = useRef(retainedReview);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  useEffect(() => {
    if (retainedReview !== previousRetained.current) {
      previousRetained.current = retainedReview;
      if (retainedReview !== undefined) setLocalReview(retainedReview);
    }
  }, [retainedReview]);
  const setReview = (value: RetainedGitHubReview | null) => {
    setLocalReview(value);
    onRetainedReviewChange?.(value);
  };
  const clearReview = () => {
    reviewRevision.current += 1;
    setReview(null);
  };
  const scope = (): RequestScope => ({ client, runID, connectionID, revision: revision.current });
  const isCurrent = (request: RequestScope) => mounted.current &&
    request.client === clientContext.current.client && request.client === current.current.client && request.runID === runID &&
    request.connectionID === current.current.connectionID && request.revision === revision.current;
  const startRequest = () => { setError(null); setNotice(""); };
  const reportError = (value: unknown, request: RequestScope) => {
    if (isCurrent(request)) setError(value);
  };

  const connections = useQuery({
    queryKey: ["github-review", "connections"],
    queryFn: ({ signal }) => client.githubReviewConnections(false, signal),
    enabled: client.hasGitHubReviewControl,
  });
  useEffect(() => {
    if (selectionReady || !connections.data || connections.isFetching) return;
    const retainedID = retainedReview?.operation.connection_id;
    const selected = connections.data.find((item) => item.connection.id === retainedID) ?? connections.data[0];
    setForm(connectionForm(selected?.connection ?? null));
    setSelectionReady(true);
  }, [selectionReady, connections.data, connections.isFetching, retainedReview]);
  const credential = useQuery({
    queryKey: ["github-review", "credential", connectionID],
    queryFn: ({ signal }) => client.githubReviewCredential(connectionID, signal),
    enabled: client.hasGitHubReviewControl && Boolean(connectionID),
  });
  const projection = useQuery({
    queryKey: ["run", runID, "github-review", connectionID, pullRequest],
    queryFn: ({ signal }) => client.githubReviewProjection(runID, connectionID, pullRequest, signal),
    enabled: client.hasGitHubReviewControl && Boolean(runID && connectionID),
  });

  const latest = projection.data?.snapshots[0];
  const isCurrentReview = (request: ReviewScope) => {
    const source = queryClient.getQueryData<GitHubReviewProjectionView>(
      ["run", request.runID, "github-review", request.connectionID, request.number]);
    const sourceCredential = queryClient.getQueryData<GitHubReviewCredentialView>(
      ["github-review", "credential", request.connectionID]);
    return isCurrent(request) && request.reviewRevision === reviewRevision.current &&
      source?.snapshots[0]?.id === request.snapshotID &&
      sourceMatches(request.sourceBinding, source.connection, request.snapshotID, request.client, request.runID) &&
      sourceMatches(request.sourceBinding, sourceCredential?.connection, request.snapshotID, request.client, request.runID);
  };
  const review = localReview && latest && !projection.isError && !credential.isError && localReview.operation.run_id === runID &&
    localReview.operation.connection_id === connectionID &&
    localReview.preview.identity.repository.full_name === latest.identity.repository.full_name &&
    localReview.preview.identity.number === latest.identity.number &&
    localReview.preview.identity.node_id === latest.identity.node_id &&
    localReview.preview.identity.state === latest.identity.state &&
    localReview.preview.identity.merged === latest.identity.merged &&
    localReview.preview.identity.draft === latest.identity.draft &&
    localReview.preview.identity.head_sha === latest.identity.head_sha &&
    localReview.preview.identity.base_sha === latest.identity.base_sha &&
    localReview.preview.capability_generation === latest.capability.generation &&
    localReview.preview.credential.name === form.connection?.credential.name &&
    localReview.preview.credential.kind === form.connection?.credential.kind &&
    sourceMatches(localReview.sourceBinding, form.connection, latest.id, client, runID) &&
    sourceMatches(localReview.sourceBinding, projection.data?.connection, latest.id, client, runID) &&
    sourceMatches(localReview.sourceBinding, credential.data?.connection, latest.id, client, runID) ? localReview : null;
  useEffect(() => {
    if (!localReview || !latest || !credential.data || projection.isFetching || credential.isFetching) return;
    if (!review) clearReview();
  }, [localReview, latest, review, credential.data, credential.isFetching, projection.isFetching]);

  const invalidate = (request: RequestScope) => {
    void queryClient.invalidateQueries({ queryKey: ["github-review"] });
    void queryClient.invalidateQueries({ queryKey: ["run", request.runID, "github-review"] });
  };
  const configure = useMutation({
    mutationFn: (request: RequestScope & { form: ConnectionForm }) => {
      const draft = request.form;
      const [owner, name, extra] = draft.repository.trim().split("/");
      if (!owner || !name || extra) throw new Error(t("仓库必须为 owner/name", "Repository must be owner/name"));
      const existing = draft.connection;
      return request.client.configureGitHubReview({
        ...(existing ? { connection_id: existing.id } : {}),
        repository: { ...(existing?.repository ?? { owner, name, full_name: `${owner}/${name}`, private: false }), host: "github.com" },
        credential: { name: draft.credentialName.trim(),
          kind: (existing?.credential.kind ?? "github_app_device") as "github_app_device" | "oauth_user" | "fine_grained_pat" },
        client_id: draft.clientID.trim() || undefined,
        allowed_log_hosts: existing?.network.allowed_log_hosts ?? [], write_enabled: draft.writeEnabled,
        enabled: existing?.enabled ?? true, expected_generation: existing?.generation ?? 0,
      });
    },
    onSuccess: (value, request) => {
      invalidate(request);
      if (!isCurrent(request)) return;
      revision.current += 1;
      setForm(connectionForm(value.connection));
      setDevice(null);
      clearReview();
      setConflict(false);
      setNotice(t("连接设置已保存。", "Connection settings saved."));
    },
    onError: (value, request) => {
      if (!isCurrent(request)) return;
      setError(value);
      setConflict(value instanceof APIRequestError && value.code === "CONFLICT");
    },
  });
  const reload = useMutation({
    mutationFn: (request: RequestScope) => request.client.githubReviewCredential(request.connectionID),
    onSuccess: (value, request) => {
      if (request.client !== clientContext.current.client) return;
      queryClient.setQueryData(["github-review", "credential", request.connectionID], value);
      if (!isCurrent(request)) return;
      revision.current += 1;
      setForm(connectionForm(value.connection));
      setDevice(null);
      clearReview();
      setError(null);
      setConflict(false);
      setNotice(t("已载入最新设置；请重新检查后保存。", "Latest settings loaded. Review them before saving."));
    },
    onError: reportError,
  });
  const beginDevice = useMutation({
    mutationFn: (request: RequestScope) => request.client.beginGitHubReviewDeviceFlow(request.connectionID),
    onSuccess: (value, request) => { if (isCurrent(request)) setDevice(value); },
    onError: reportError,
  });
  const pollDevice = useMutation({
    mutationFn: (request: RequestScope & { sessionID: string }) =>
      request.client.pollGitHubReviewDeviceFlow(request.connectionID, request.sessionID),
    onSuccess: (value, request) => {
      invalidate(request);
      if (isCurrent(request) && value.configured) { setDevice(null); clearReview(); }
    },
    onError: reportError,
  });
  const disconnect = useMutation({
    mutationFn: (request: RequestScope) => request.client.disconnectGitHubReview(request.connectionID),
    onSuccess: (value, request) => {
      if (request.client !== clientContext.current.client) return;
      const signedOut = (item: GitHubReviewCredentialView): GitHubReviewCredentialView =>
        item.connection.credential.name === value.connection.credential.name ? {
          ...item, credential: { ...item.credential, configured: false, refreshable: false,
            expires_at: undefined, refresh_expires_at: undefined },
        } : item;
      // The secret store is keyed by credential reference, which may be shared by multiple repositories.
      queryClient.setQueriesData<GitHubReviewCredentialView>({ queryKey: ["github-review", "credential"] },
        (item) => item ? signedOut(item) : item);
      queryClient.setQueryData(["github-review", "credential", request.connectionID], value);
      queryClient.setQueryData<GitHubReviewCredentialView[]>(["github-review", "connections"], (items) =>
        items?.map((item) => item.connection.id === request.connectionID ? value : signedOut(item)));
      invalidate(request);
      void queryClient.invalidateQueries({ predicate: (query) =>
        query.queryKey[0] === "run" && query.queryKey[2] === "github-review" });
      if (!isCurrent(request)) return;
      setNotice(t("已删除此连接的本机凭据。", "Local credential deleted for this connection."));
    },
    onError: reportError,
  });
  const qualify = useMutation({
    mutationFn: (request: RequestScope & { number: number }) => request.client.qualifyGitHubReview(request.connectionID, request.number),
    onError: reportError,
  });
  const fetchSnapshot = useMutation({
    mutationFn: (request: RequestScope & { number: number }) => request.client.fetchGitHubReview(request.connectionID, request.number),
    onSuccess: (_, request) => {
      invalidate(request);
      if (isCurrent(request)) setSnapshotRefreshRequired(false);
    },
    onError: reportError,
  });
  const buildEvidence = useMutation({
    mutationFn: (request: RequestScope & { snapshotID: string }) => request.client.buildGitHubReviewEvidence(request.runID, request.snapshotID),
    onSuccess: (_, request) => invalidate(request), onError: reportError,
  });
  const reviewWrite = useMutation({
    mutationFn: (request: ReviewScope & { spec: GitHubReviewWriteSpecView }) =>
      request.client.reviewGitHubWrite(request.runID, { connection_id: request.connectionID,
        snapshot_id: request.snapshotID, operation_key: operationKey(), spec: request.spec }),
    onSuccess: (value, request) => {
      invalidate(request);
      void queryClient.invalidateQueries({ queryKey: ["run", request.runID, "approvals"] });
      if (!isCurrentReview(request)) return;
      setReview({ ...value, sourceBinding: request.sourceBinding });
    },
    onError: (value, request) => { if (isCurrentReview(request)) setError(value); },
  });
  const executeWrite = useMutation({
    mutationFn: (request: ReviewScope & { review: RetainedGitHubReview }) => {
      const approvalID = "ID" in request.review.approval ? String(request.review.approval.ID) : "";
      if (!isCurrentReview(request) || request.review.operation.run_id !== request.runID ||
        request.review.operation.connection_id !== request.connectionID || !approvalID) {
        throw new Error("The exact reviewed write is no longer current");
      }
      return request.client.executeGitHubWrite(request.runID, request.review.operation.id, approvalID);
    },
    onSuccess: (_, request) => {
      invalidate(request);
      if (isCurrentReview(request)) { clearReview(); setWriteDraftRevision((value) => value + 1); }
    },
    onError: (value, request) => { if (isCurrentReview(request)) setError(value); },
  });
  const mutations = [configure, reload, beginDevice, pollDevice, disconnect, qualify,
    fetchSnapshot, buildEvidence, reviewWrite, executeWrite];
  const pending = mutations.some((mutation) => mutation.isPending && mutation.variables && isCurrent(mutation.variables));
  const connectionWriteEnabled = form.connection?.network.write_enabled === true;
  const credentialCurrent = !credential.isError && credential.data?.connection.generation === form.connection?.generation &&
    credential.data?.connection.credential.name === form.connection?.credential.name;
  const canSignIn = credentialCurrent && credential.data?.credential.store_available &&
    form.connection?.enabled && form.connection.credential.kind === "github_app_device";
  const canDisconnect = credentialCurrent && credential.data?.credential.store_available && credential.data.credential.configured;
  const canWrite = credentialCurrent && !snapshotRefreshRequired && !credential.isFetching && !projection.isError && !projection.isFetching &&
    credential.data?.credential.configured && form.connection?.enabled &&
    projection.data?.connection.generation === form.connection.generation && latest?.capability &&
    latest.capability.credential.name === form.connection.credential.name &&
    latest.capability.credential.kind === form.connection.credential.kind && connectionWriteEnabled;
  const disconnectConnectionChanged = (target: GitHubReviewConnectionView) => {
    const cachedCredential = queryClient.getQueryData<GitHubReviewCredentialView>(["github-review", "credential", target.id]);
    const cachedConnections = queryClient.getQueryData<GitHubReviewCredentialView[]>(["github-review", "connections"]);
    const cachedProjection = queryClient.getQueryData<GitHubReviewProjectionView>(["run", runID, "github-review", target.id, pullRequest]);
    return [cachedCredential?.connection, cachedConnections?.find((item) => item.connection.id === target.id)?.connection,
      cachedProjection?.connection].some((item) => item && item.generation > target.generation);
  };
  const rejectChangedDisconnect = () => {
    setDisconnectTarget(null);
    setError(new Error(t("连接设置已改变；重新载入最新设置后再删除本机凭据。",
      "Connection settings changed. Reload the latest settings before deleting its local credential.")));
  };
  useEffect(() => {
    if (disconnectTarget && disconnectConnectionChanged(disconnectTarget)) rejectChangedDisconnect();
  }, [disconnectTarget, credential.data, connections.data, projection.data]);
  const selectConnection = (id: string) => {
    revision.current += 1;
    current.current.connectionID = id;
    setSelectionReady(true);
    setForm(connectionForm(connections.data?.find((item) => item.connection.id === id)?.connection ?? null));
    setDevice(null);
    setPullRequest(0);
    clearReview();
    setError(null);
    setConflict(false);
    setSnapshotRefreshRequired(false);
    setNotice("");
    setDisconnectTarget(null);
  };
  const fetchRemote = (number: number) => {
    startRequest();
    clearReview();
    setSnapshotRefreshRequired(true);
    fetchSnapshot.mutate({ ...scope(), number });
  };
  const failedJobs = useMemo(() => latest?.jobs.filter((job) =>
    job.conclusion && !["success", "skipped", "neutral"].includes(job.conclusion)) ?? [], [latest]);
  const staleMappings = useMemo(() => projection.data?.evidence.flatMap((item) =>
    item.graph.mappings.filter((mapping) => mapping.state !== "verified")) ?? [], [projection.data]);
  const operationLabels: Record<string, string> = {
    submit_review: t("提交整体审阅", "Submit review"), reply: t("回复讨论", "Reply to discussion"),
    resolve: t("解决讨论", "Resolve discussion"), unresolve: t("重新打开讨论", "Reopen discussion"),
    request_reviewer: t("请求审阅人", "Request reviewers"),
  };

  if (!client.hasGitHubReviewControl) return <section className="repository-state-panel">
    <header className="panel-header"><div><GitPullRequest size={17} /><h2>GitHub Review</h2></div></header>
    <EmptyState>{t("启用 GitHub 审阅控制后，可连接仓库、查看 PR 并审批远端操作。", "Enable GitHub review control to connect a repository, inspect PRs, and approve remote actions.")}</EmptyState>
  </section>;
  if (connections.isLoading || (!selectionReady && !connections.isError)) return <LoadingState label={t("加载 GitHub 连接", "Loading GitHub connections")} />;
  if (connections.isError && !connections.data) return <ErrorState error={connections.error} />;

  return <section aria-label="GitHub Review" className="repository-state-panel github-review-panel">
    <header className="panel-header"><div><GitPullRequest size={17} /><h2>GitHub Review</h2></div>
      <button className="icon-button" disabled={pending} aria-label={t("刷新连接和远端 PR", "Refresh connection and remote PR")}
        onClick={() => { void connections.refetch(); const number = pullRequest || latest?.identity.number;
          if (connectionID && number) fetchRemote(number);
          else if (connectionID) { void credential.refetch(); void projection.refetch(); } }} type="button">
        <RefreshCw className={pending ? "spin" : ""} size={16} />
      </button></header>
    {Boolean(error) && <ErrorState error={error} />}
    {connections.isError && <ErrorState error={connections.error} />}
    {notice && <p role="status">{notice}</p>}
    {conflict && <p role="alert">{t(
      "此连接已被其他操作更新。草稿已保留；重新载入最新设置后再编辑保存。",
      "This connection was updated elsewhere. Your draft is preserved; reload the latest settings before editing and saving again.",
    )}</p>}

    <section className="github-review-section">
      <h3>{t("账户与仓库", "Account & repository")}</h3>
      <div className="github-review-form">
        <select aria-label={t("GitHub 连接", "GitHub connection")} value={connectionID}
          onChange={(event) => selectConnection(event.target.value)}>
          <option value="">{t("新建连接", "New connection")}</option>
          {connections.data?.map((item) => <option key={item.connection.id} value={item.connection.id}>
            {item.connection.repository.full_name} · {item.credential.configured ? t("已登录", "signed in") : t("未登录", "signed out")}
          </option>)}
        </select>
        <input aria-label={t("仓库", "Repository")} disabled={pending} readOnly={Boolean(form.connection)}
          onChange={(event) => setForm({ ...form, repository: event.target.value })}
          placeholder="owner/repository" value={form.repository} />
        <input aria-label={t("凭据引用", "Credential reference")} disabled={pending}
          onChange={(event) => setForm({ ...form, credentialName: event.target.value })}
          placeholder="prayu-github-app" value={form.credentialName} />
        <input aria-label="GitHub App Client ID" disabled={pending || Boolean(form.connection && form.connection.credential.kind !== "github_app_device")}
          onChange={(event) => setForm({ ...form, clientID: event.target.value })}
          placeholder="GitHub App Client ID" value={form.clientID} />
        <label><input checked={form.writeEnabled} disabled={pending}
          onChange={(event) => setForm({ ...form, writeEnabled: event.target.checked })}
          type="checkbox" />{t("允许逐次审批的远端写回", "Allow per-call approved write-back")}</label>
        <button disabled={pending || conflict || !form.repository.trim() || !form.credentialName.trim() ||
          ((!form.connection || form.connection.credential.kind === "github_app_device") && !form.clientID.trim())}
          onClick={() => { startRequest(); clearReview(); configure.mutate({ ...scope(), form }); }} type="button">
          {form.connection ? t("更新连接", "Update connection") : t("创建连接", "Create connection")}
        </button>
      </div>
      <small>{form.connection ? t(
        `正在编辑 ${form.connection.repository.full_name}；设置版本 ${form.connection.generation}。`,
        `Editing ${form.connection.repository.full_name}; settings version ${form.connection.generation}.`,
      ) : t("填写仓库与 GitHub App 信息创建连接，再使用设备登录授权。", "Enter the repository and GitHub App details to create a connection, then authorize it with device sign-in.")}</small>
      {connectionID && <div className="github-review-actions">
        <button disabled={pending} onClick={() => { startRequest(); reload.mutate(scope()); }} type="button">
          {t("重新载入最新设置", "Reload latest settings")}</button>
        {form.connection?.credential.kind === "github_app_device" && <button disabled={pending || !canSignIn}
          onClick={() => { startRequest(); setDevice(null); clearReview(); beginDevice.mutate(scope()); }} type="button">{t("设备登录", "Device sign-in")}</button>}
        <button disabled={pending || !canDisconnect} ref={disconnectButton}
          onClick={() => setDisconnectTarget(form.connection)} type="button">
          {t("删除本机凭据…", "Delete local credential…")}</button>
      </div>}
      {credential.isError && <ErrorState error={credential.error} />}
      {credentialCurrent && <small>{!credential.data?.credential.store_available ? t(
        "系统凭据库当前不可用。请连接支持系统凭据存储的服务，再点击“重新载入最新设置”核对。",
        "The system credential store is unavailable. Connect to a service with system credential storage, then select Reload latest settings to check again.")
        : credential.data.credential.configured ? t("本机凭据已配置。", "Local credential is configured.")
        : !form.connection?.enabled ? t(
          "此连接已停用。请从“GitHub 连接”选择已启用的连接，或选择“新建连接”重新配置。",
          "This connection is disabled. Select an enabled GitHub connection, or choose New connection to configure another.")
        : form.connection.credential.kind === "github_app_device" ? t(
          "请使用设备登录配置本机凭据。", "Use device sign-in to configure the local credential.")
        : t(
          `本机尚未配置 ${form.connection.credential.kind === "fine_grained_pat" ? "PAT" : "OAuth"} 凭据。请在“凭据引用”填写已保存且类型匹配的凭据名称，再点击“更新连接”；需要设备登录时，请选择“新建连接”配置 GitHub App。`,
          `The local ${form.connection.credential.kind === "fine_grained_pat" ? "PAT" : "OAuth"} credential is missing. Enter a stored credential name of the same type in Credential reference and select Update connection. For device sign-in, choose New connection to configure a GitHub App.`)}</small>}
      {device && <div className="github-review-device"><code>{device.user_code}</code>
        <a href={device.verification_uri} rel="noreferrer" target="_blank">github.com/login/device <ExternalLink size={12} /></a>
        <button disabled={pending || !canSignIn} onClick={() => {
          startRequest(); pollDevice.mutate({ ...scope(), sessionID: device.session_id });
        }} type="button">{t("检查授权", "Check authorization")}</button>
      </div>}
    </section>

    {connectionID && <section className="github-review-section">
      <h3>{t("查看 PR", "Inspect a PR")}</h3>
      <p>{t("填写 PR 编号，先检查访问条件，再获取最新快照。", "Enter a PR number, check access, then fetch the latest snapshot.")}</p>
      <div className="github-review-form"><input aria-label={t("PR 编号", "PR number")} min={1}
        onChange={(event) => { clearReview(); setPullRequest(Number(event.target.value)); }} type="number" value={pullRequest || ""} />
        <button disabled={pending || pullRequest < 1} onClick={() => {
          startRequest(); qualify.mutate({ ...scope(), number: pullRequest });
        }} type="button">{t("检查访问条件", "Check access")}</button>
        <button disabled={pending || pullRequest < 1} onClick={() => fetchRemote(pullRequest)} type="button">{t("抓取快照", "Fetch snapshot")}</button></div>
      {qualify.data && qualify.variables && isCurrent(qualify.variables) && qualify.variables.number === pullRequest &&
        <div className="github-review-diagnostics"><StatusBadge status={qualify.data.qualification.eligible ? "qualified" : "blocked"} />
        {qualify.data.qualification.diagnostics.map((item) => <small key={item.code}>{item.code}: {item.message}</small>)}</div>}
      {projection.isLoading && <LoadingState />}
      {projection.isError && <ErrorState error={projection.error} />}
      {projection.data?.standard_code_delivery && <div className="github-review-delivery-truth">
        <span><strong>{t("交付检查", "Delivery checks")}</strong>
          <code>{projection.data.standard_code_delivery.receipt_sha256}</code>
          <small>{projection.data.standard_code_delivery.diff.changed_count} {t("个文件", "files")} · {projection.data.standard_code_delivery.verifications.length} {t("条命令", "commands")}</small></span>
        <StatusBadge status={projection.data.standard_code_delivery.status} />
        {onOpenDelivery && <button className="compact-command" onClick={onOpenDelivery} type="button">
          {t("打开交付页", "Open delivery")}</button>}
      </div>}
      {latest && <><dl className="repository-reference github-review-stats">
        <KeyValue label="PR" value={`#${latest.identity.number} ${latest.title.text}`} />
        <KeyValue label="HEAD" value={shortID(latest.identity.head_sha)} />
        <KeyValue label={t("文件", "Files")} value={latest.files.length} />
        <KeyValue label={t("线程", "Threads")} value={latest.threads.length} />
        <KeyValue label="CI" value={`${latest.check_runs.length} / ${failedJobs.length} failed`} />
        <KeyValue label={t("抓取时间", "Fetched")} value={formatDate(latest.fetched_at)} />
      </dl><div className="github-review-actions"><button disabled={pending}
        onClick={() => { startRequest(); buildEvidence.mutate({ ...scope(), snapshotID: latest.id }); }} type="button">{t("绑定本地证据", "Bind local evidence")}</button></div></>}
      {failedJobs.map((job) => <article className="github-review-row" key={job.id}>
        <StatusBadge status={job.conclusion ?? job.status} /><strong>{job.name}</strong>
        <small>{job.failed_log.text || job.log_reason || t("无日志摘录", "No log excerpt")}</small>
      </article>)}
      {staleMappings.length > 0 && <details><summary>{t("非当前映射", "Non-current mappings")} · {staleMappings.length}</summary>
        {staleMappings.map((mapping) => <article className="github-review-row" key={mapping.comment_id}>
          <StatusBadge status={mapping.state} /><code>{mapping.path || mapping.comment_id}</code>
          <small>{mapping.reasons.join(" · ")}</small></article>)}</details>}
    </section>}

    {latest && <GitHubReviewThreads snapshot={latest} />}
    {latest && !connectionWriteEnabled && <section className="github-review-section">
      <h3>{t("审批后回写", "Approval-gated write-back")}</h3>
      <EmptyState>{t(
        "当前可查看 PR。需要评论、回复等远端操作时，在连接设置中明确允许逐次审批的写回。",
        "PR inspection is available. To comment, reply, or make other remote changes, explicitly allow per-call approved write-back in the connection settings.",
      )}</EmptyState>
    </section>}
    {latest && connectionWriteEnabled && <section className="github-review-section">
      <h3>{t("审批后回写", "Approval-gated write-back")}</h3>
      {snapshotRefreshRequired && !fetchSnapshot.isPending && <small>{t(
        "请成功刷新 PR 快照后再准备写回。", "Refresh the PR snapshot successfully before preparing a write.",
      )}</small>}
      <GitHubReviewWriteForm key={`${latest.id}:${writeDraftRevision}`} snapshot={latest} disabled={pending || !canWrite}
        onChange={clearReview} onPreview={(draft) => {
            if (pending || !canWrite || !githubWriteSupported(latest, draft.operation)) return;
            startRequest(); clearReview();
            reviewWrite.mutate({ ...scope(), snapshotID: latest.id, number: pullRequest, reviewRevision: reviewRevision.current,
              sourceBinding: { runID, connectionID, snapshotID: latest.id,
                connectionGeneration: form.connection!.generation, credentialName: form.connection!.credential.name,
                credentialKind: form.connection!.credential.kind, clientID: form.connection!.client_id ?? "", apiClient: client },
              spec: { ...draft, protocol_version: "github-review-write.v1",
                identity: latest.identity, credential: form.connection!.credential,
                capability_generation: latest.capability.generation,
                validation_summary: "Operator-reviewed Traverse Board evidence graph" } });
          }} />
      {review && <div className="github-review-approval"><ShieldCheck size={15} />
        <div className="github-review-preview-summary"><strong>{operationLabels[review.preview.operation]}</strong>
        {review.preview.target_id && <span> · {latest.threads.find((thread) => thread.id === review.preview.target_id)?.path ?? review.preview.target_id}</span>}
        {review.preview.review_event && <span>{review.preview.review_event}</span>}
        {review.preview.body_summary && <p>{review.preview.body_summary}</p>}
        {review.preview.reviewers?.length > 0 && <p>{review.preview.reviewers.join(", ")}</p>}</div>
        <code>{review.preview.approval_fingerprint}</code>
        <button onClick={onOpenApprovals} type="button">{t("打开审批", "Open approvals")}</button>
        <button disabled={pending || !canWrite || !githubWriteSupported(latest, review.preview.operation)} onClick={() => {
          startRequest(); executeWrite.mutate({ ...scope(), snapshotID: latest.id,
            number: pullRequest, reviewRevision: reviewRevision.current, sourceBinding: review.sourceBinding!, review });
        }} type="button">{t("执行已批准操作", "Execute approved write")}</button></div>}
      {projection.data?.writes.map((item) => <article className="github-review-row" key={item.id}>
        <StatusBadge status={item.status} /><strong>{item.preview.operation}</strong>
        <small>{item.receipt.result_url || item.error_code || item.preview.body_summary || item.id}</small>
      </article>)}
    </section>}
    <V2ConfirmDialog open={Boolean(disconnectTarget)} danger returnFocusRef={disconnectButton}
      title={t("删除 GitHub 本机凭据", "Delete local GitHub credential")}
      description={t(
        `将删除连接 ${disconnectTarget?.id ?? ""}（${disconnectTarget?.repository.full_name ?? ""}）当前使用的本机凭据。使用同一凭据引用的连接也会退出登录。连接设置和历史证据会保留。GitHub 端授权仍有效，如需撤销请在 GitHub 设置中操作。`,
        `Delete the local credential used by connection ${disconnectTarget?.id ?? ""} (${disconnectTarget?.repository.full_name ?? ""}). Connections sharing this credential reference will also be signed out. Connection settings and historical evidence remain. GitHub authorization stays active; revoke it in GitHub settings if needed.`,
      )}
      confirmLabel={t("删除本机凭据", "Delete local credential")}
      onCancel={() => setDisconnectTarget(null)}
      onConfirm={() => {
        if (disconnectTarget && disconnectConnectionChanged(disconnectTarget)) { rejectChangedDisconnect(); return; }
        if (!disconnectTarget || disconnectTarget.id !== connectionID || !canDisconnect || pending) return;
        startRequest();
        revision.current += 1;
        setDevice(null);
        clearReview();
        setDisconnectTarget(null);
        disconnect.mutate(scope());
      }} />
  </section>;
}
