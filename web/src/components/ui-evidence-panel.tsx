import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Ban,
  Camera,
  Download,
  LoaderCircle,
  RefreshCw,
  ShieldAlert,
} from "lucide-react";
import { APIRequestError, type APIClient } from "../api/client";
import type {
  UIEvidenceArtifactMetadata,
  UIEvidenceAttempt,
  UIEvidenceStartView,
} from "../api/types";
import { formatBytes, formatDate, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import { UIEvidencePreparation } from "./ui-evidence-preparation";
import { useVerificationRecord } from "./verification-recovery";
import { uiEvidenceDraftKey, uiEvidenceIntentKey, validUIEvidenceIntent, type UIEvidenceIntent } from "./ui-evidence-recovery";
import type { V2RecoveryStore } from "../v2/recovery-storage";
import { VerificationSettingsPanel } from "./verification-settings-panel";

interface UIEvidenceSubmission {
  request: UIEvidenceStartView; runID: string; client: APIClient;
  journal: { store: V2RecoveryStore | null; save: (intent: UIEvidenceIntent | null) => void; remove: () => void };
}

export function UIEvidencePanel({ client, runID, threadID }: {
  client: APIClient;
  runID: string;
  threadID?: string;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [selection, setSelection] = useState({ runID, attemptID: "" });
  const [preparationGeneration, setPreparationGeneration] = useState(0);

  const [localError, setLocalError] = useState("");
  const [preview, setPreview] = useState<{ attemptID: string; artifactID: string; url: string } | null>(null);
  const journal = useVerificationRecord<UIEvidenceIntent | null>(uiEvidenceIntentKey(runID), null,
    (value): value is UIEvidenceIntent | null => validUIEvidenceIntent(value, runID));
  const intent = journal.value?.state === "pending" ? { ...journal.value, state: "unknown" as const } : journal.value;
  const currentAttempt = useRef("");
  const currentScope = useRef({ runID, client, store: journal.store });
  currentScope.current = { runID, client, store: journal.store };
  const attempts = useQuery({
    queryKey: ["run", runID, "ui-evidence"],
    queryFn: ({ signal }) => client.uiEvidence(runID, signal),
    enabled: Boolean(runID),
    refetchInterval: (query) => query.state.data?.some((attempt) => attempt.status === "running")
      ? 1_500 : false,
  });
  const selectedID = selection.runID === runID ? selection.attemptID : "";
  const activeID = selectedID || attempts.data?.[0]?.manifest.attempt_id || "";
  currentAttempt.current = activeID;
  useEffect(() => () => { if (preview) URL.revokeObjectURL(preview.url); }, [preview]);
  const bundle = useQuery({
    queryKey: ["ui-evidence", activeID],
    queryFn: ({ signal }) => client.uiEvidenceBundle(activeID, signal),
    enabled: Boolean(activeID),
    refetchInterval: (query) => query.state.data?.attempt.status === "running" ? 1_500 : false,
  });

  useEffect(() => {
    if (selectedID && attempts.data && !attempts.data.some(
      (attempt) => attempt.manifest.attempt_id === selectedID)) {
      setSelection({ runID, attemptID: "" });
    }
  }, [attempts.data, selectedID, runID]);

  const refresh = async (attemptID?: string) => {
    await queryClient.invalidateQueries({ queryKey: ["run", runID, "ui-evidence"] });
    if (attemptID) {
      await queryClient.invalidateQueries({ queryKey: ["ui-evidence", attemptID] });
    }
  };
  const start = useMutation({
    mutationFn: (submission: UIEvidenceSubmission) => submission.client.startUIEvidence(submission.runID, submission.request),
    onSuccess: async (attempt, submission) => {
      let cleared = false;
      try {
        submission.journal.store!.assertReadable(uiEvidenceDraftKey(submission.runID));
        submission.journal.store!.remove(uiEvidenceDraftKey(submission.runID));
        submission.journal.remove(); cleared = true;
      } catch (failure) {
        if (currentScope.current.store === submission.journal.store && currentScope.current.runID === submission.runID) setLocalError(humanError(failure));
      }
      if (cleared && currentScope.current.runID === submission.runID && currentScope.current.client === submission.client && currentScope.current.store === submission.journal.store) {
        setPreparationGeneration((generation) => generation + 1);
        setSelection({ runID: submission.runID, attemptID: attempt.manifest.attempt_id }); setLocalError("");
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["run", submission.runID, "ui-evidence"] }),
        queryClient.invalidateQueries({ queryKey: ["ui-evidence", attempt.manifest.attempt_id] }),
      ]);
    },
    onError: (error, submission) => {
      try { submission.journal.save({ version: 1, runID: submission.runID, request: submission.request,
        state: error instanceof APIRequestError && error.status < 500 ? "rejected" : "unknown" }); }
      catch (failure) { if (currentScope.current.store === submission.journal.store && currentScope.current.runID === submission.runID) setLocalError(humanError(failure)); }
    },
  });
  const submit = (request: UIEvidenceStartView) => {
    if (!client.hasUIEvidence || start.isPending ||
      (intent?.state === "unknown" && intent.request.operation_key !== request.operation_key)) return;
    try {
      // An interrupted start is always restored as unknown, never re-dispatched
      // by mounting the panel. Persist its exact identity before the API call.
      journal.save({ version: 1, runID, request, state: "unknown" });
      setLocalError(""); start.mutate({ request, runID, client, journal });
    } catch (failure) { setLocalError(humanError(failure)); }
  };
  const cancel = useMutation({
    mutationFn: (attemptID: string) => client.cancelUIEvidence(attemptID),
    onSuccess: async (attempt) => refresh(attempt.manifest.attempt_id),
  });

  const download = async (artifact: UIEvidenceArtifactMetadata) => {
    setLocalError("");
    try {
      const content = await client.downloadUIEvidenceArtifact(artifact.attempt_id, artifact);
      const objectURL = URL.createObjectURL(content);
      const anchor = document.createElement("a");
      anchor.href = objectURL;
      anchor.download = artifactFilename(artifact);
      anchor.rel = "noopener";
      anchor.click();
      URL.revokeObjectURL(objectURL);
    } catch (caught) {
      setLocalError(humanError(caught));
    }
  };
  const previewScreenshot = async (artifact: UIEvidenceArtifactMetadata) => {
    setLocalError("");
    const scope = currentScope.current;
    try {
      const content = await client.downloadUIEvidenceArtifact(artifact.attempt_id, artifact);
      if (currentAttempt.current !== artifact.attempt_id || currentScope.current.runID !== scope.runID || currentScope.current.client !== scope.client) return;
      setPreview({ attemptID: artifact.attempt_id, artifactID: artifact.id, url: URL.createObjectURL(content) });
    } catch (caught) { setLocalError(humanError(caught)); }
  };

  const current = bundle.data?.attempt;
  const mutationError = start.error || cancel.error;
  const historyUnavailable = !client.hasUIEvidence && attempts.isError &&
    attempts.error instanceof APIRequestError && attempts.error.status === 404 &&
    attempts.error.code === "NOT_FOUND";
  return <div className="ui-evidence-panel">
    <header className="operator-list-header">
      <div><Camera aria-hidden="true" size={16} />
        <h2>{t("真实浏览器 UI 证据", "Real-browser UI evidence")}</h2></div>
      <button aria-label={t("刷新 UI 证据", "Refresh UI evidence")} className="icon-button"
        disabled={attempts.isFetching} onClick={() => void refresh(activeID)} type="button">
        <RefreshCw aria-hidden="true" className={attempts.isFetching ? "spin" : ""} size={15} />
      </button>
    </header>
    <div className="ui-evidence-boundary" role="note">
      <ShieldAlert aria-hidden="true" size={16} />
      <span><strong>{t("查看浏览器检查结果", "Inspect browser check results")}</strong>
        {t("截图和下载内容供你核对页面表现。以验证状态判断结果：「通过」表示检查完成通过，「未运行」表示等待验证。进程与凭据操作须单独授权。",
          "Use screenshots and downloads to inspect the page. Follow the verification status: Passed means the checks passed; Not run awaits verification. Process and credential actions require separate authorization.")}</span>
    </div>
    {!client.hasUIEvidence && <div className="inline-warning" role="note">
      <p>{client.uiEvidenceUnavailableReason === "missing_control_credential"
        ? t("先刷新列表以查看历史证据。连接控制凭证后可启动或取消浏览器验证。",
          "Refresh the list to inspect historical evidence. Connect a control credential to start or cancel browser verification.")
        : client.uiEvidenceUnavailableReason === "ui_evidence_disabled"
          ? t("在下方验证设置中启用浏览器验证，保存后重启应用。",
            "Enable browser verification in the settings below, save, and restart the application.")
          : client.uiEvidenceUnavailableReason === "run_execution_disabled"
            ? t("使用桌面默认启动方式并连接控制凭证，恢复浏览器验证所需的任务执行能力。",
              "Use the desktop's default startup and connect a control credential to restore task execution for browser verification.")
            : client.uiEvidenceUnavailableReason === "browser_cdp_control_disabled"
              ? t("使用桌面默认启动方式并重新连接当前控制服务，准备浏览器控制能力。",
                "Use the desktop's default startup and reconnect the current control service to prepare browser control.")
              : t("先刷新列表以查看历史记录。在下方验证设置中保存启用偏好并重启应用。",
                "Refresh the list to inspect historical records. Save your verification preferences below and restart the application.")}</p>
      <VerificationSettingsPanel client={client} />
      <details><summary>{t("高级：启动参数与独立 CLI", "Advanced: startup options and standalone CLI")}</summary>
      <p>{t("仅在手工启动 Windows Desktop 时，连接控制凭证并明确启用以下能力：",
        "When starting Windows Desktop manually, connect a control credential and explicitly enable these capabilities:")}
        {" "}<code>--enable-permission-control --enable-danger-full-access --enable-run-execution --enable-browser-cdp-control --enable-ui-evidence</code>
        {t("。独立 CLI 提供历史证据读取与导出；浏览器启动和取消请使用 Windows Desktop。",
          ". Use the standalone CLI to read and export historical evidence, and Windows Desktop to start or cancel a browser.")}
      </p></details>
    </div>}

    {attempts.isLoading && <LoadingState label={t("正在加载 UI 证据", "Loading UI evidence")} />}
    {attempts.isError && (historyUnavailable
      ? <p className="inline-warning" role="status">{t(
        "历史证据状态待确认。请核对所选执行，或连接支持 UI 取证的 Windows Desktop 后读取。",
        "Historical evidence needs confirmation. Check the selected execution, or read it through a Windows Desktop connection that supports UI evidence.",
      )}</p>
      : <ErrorState error={attempts.error} />)}
    {attempts.isSuccess && attempts.data.length === 0 && <EmptyState>{client.hasUIEvidence
      ? t("还没有浏览器验证。展开下方启动表单，填写应用启动方式并核对步骤后开始。", "No browser verification yet. Expand the launch form below, enter the application launch settings and review its steps to begin.")
      : t("还没有浏览器验证。请在上方验证设置中保存启用偏好，重启后连接当前服务。", "No browser verification yet. Save your verification preferences above, then restart and connect to the current service.")}</EmptyState>}

    {attempts.data && attempts.data.length > 0 && <div className="ui-evidence-layout">
      <section className="ui-evidence-attempts" aria-label={t("UI 证据 Attempts", "UI evidence attempts")}>
        {attempts.data.map((attempt) => <AttemptButton attempt={attempt}
          key={attempt.manifest.attempt_id}
          onSelect={() => setSelection({ runID, attemptID: attempt.manifest.attempt_id })}
          selected={attempt.manifest.attempt_id === activeID} />)}
      </section>
      <section className="ui-evidence-detail" aria-live="polite">
        {bundle.isLoading && <LoadingState label={t("正在加载 Attempt", "Loading attempt")} />}
        {bundle.isError && <ErrorState error={bundle.error} />}
        {bundle.data && <AttemptDetail attempt={bundle.data.attempt}
          artifacts={bundle.data.artifacts} onDownload={download} onPreview={previewScreenshot} steps={bundle.data.steps} />}
        {preview?.attemptID === activeID && <figure><img className="ui-evidence-image-preview" src={preview.url}
          alt={t(`验证截图 ${preview.artifactID}`, `Verification screenshot ${preview.artifactID}`)} /><figcaption>{t("已核对内容哈希的浏览器截图", "Browser screenshot with verified content hash")}</figcaption></figure>}
      </section>
    </div>}

    {current?.status === "running" && client.hasUIEvidence && <button className="command-button danger"
      disabled={cancel.isPending} onClick={() => cancel.mutate(current.manifest.attempt_id)} type="button">
      {cancel.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
        <Ban aria-hidden="true" size={14} />}
      {t("取消并清理 Attempt", "Cancel and clean up attempt")}
    </button>}

    {journal.error && <p className="inline-warning" role="status">{journal.error}</p>}
    {intent?.state === "unknown" && <div role="status" className="projection-placeholder">
      <p>{t("启动结果待确认。核对原请求以读取或恢复同一次验证。", "The start outcome needs confirmation. Check the original request to read or recover the same verification.")}</p>
      <details><summary>{t("原请求记录", "Original request")}</summary><pre>{JSON.stringify(intent.request, null, 2)}</pre></details>
      <button className="command-button" disabled={!client.hasUIEvidence || start.isPending} type="button"
        onClick={() => submit(intent.request)}>{t("核对原请求", "Check original request")}</button>
    </div>}
    <UIEvidencePreparation key={`${runID}:${preparationGeneration}`} client={client} runID={runID} threadID={threadID} pending={start.isPending} unresolved={intent?.state === "unknown"} onStart={submit} />
    {(localError || mutationError) && <div className="inline-warning" role="alert">
      {localError || humanError(mutationError)}</div>}
  </div>;
}

function AttemptButton({ attempt, onSelect, selected }: {
  attempt: UIEvidenceAttempt;
  onSelect: () => void;
  selected: boolean;
}) {
  const { t } = useLocale();
  return <button aria-pressed={selected} className={selected ? "selected" : ""}
    onClick={onSelect} type="button">
    <span><strong>{attempt.manifest.route}</strong>
      <small>{shortID(attempt.manifest.attempt_id)} · {formatDate(attempt.created_at)}</small></span>
    <StatusBadge status={attempt.status}
      label={attempt.status === "not_run" ? t("未运行", "Not run") : undefined} />
  </button>;
}

function AttemptDetail({ artifacts, attempt, onDownload, onPreview, steps }: {
  artifacts: UIEvidenceArtifactMetadata[];
  attempt: UIEvidenceAttempt;
  onDownload: (artifact: UIEvidenceArtifactMetadata) => void;
  onPreview: (artifact: UIEvidenceArtifactMetadata) => void;
  steps: Array<{
    step_id: string;
    sequence: number;
    kind: string;
    status: string;
    failure_stage: string;
    message?: string;
    completed_at: string;
  }>;
}) {
  const { t } = useLocale();
  const manifest = attempt.manifest;
  const cleanupComplete = Object.values(attempt.cleanup).every(Boolean);
  const sourceLabel = `${shortID(manifest.source.commit)}${manifest.source.dirty ? " + dirty" : ""}`;
  return <>
    <header><div><strong>{manifest.route}</strong><code>{manifest.attempt_id}</code></div>
      <StatusBadge status={attempt.status}
        label={attempt.status === "not_run" ? t("未运行", "Not run") : undefined} /></header>
    <dl className="ui-evidence-facts">
      <div><dt>{t("源码", "Source")}</dt><dd>{sourceLabel}</dd></div>
      <div><dt>{t("Dirty digest", "Dirty digest")}</dt><dd><code>{manifest.source.dirty_digest}</code></dd></div>
      <div><dt>{t("浏览器", "Browser")}</dt><dd>{manifest.browser.product} {manifest.browser.version}</dd></div>
      <div><dt>{t("驱动", "Driver")}</dt><dd><code>{manifest.browser.driver_protocol}</code></dd></div>
      <div><dt>URL / route</dt><dd><code>{manifest.url}</code> · <code>{manifest.route}</code></dd></div>
      <div><dt>{t("视口", "Viewport")}</dt><dd>{manifest.environment.viewport.width} × {manifest.environment.viewport.height} @ {manifest.environment.viewport.dpr}x</dd></div>
      <div><dt>{t("呈现环境", "Presentation")}</dt><dd>{manifest.environment.locale} · {manifest.environment.theme} · {manifest.environment.reduced_motion ? "reduced motion" : "full motion"}</dd></div>
      <div><dt>Fixture / seed</dt><dd>{manifest.fixture.name} · <code>{manifest.fixture.seed}</code></dd></div>
      <div><dt>{t("页面状态", "Page state")}</dt><dd><code>{manifest.fixture.page_state}</code></dd></div>
      <div><dt>{t("产物", "Artifacts")}</dt><dd>{attempt.artifact_count} · {formatBytes(attempt.artifact_bytes)}</dd></div>
      <div><dt>{t("清理", "Cleanup")}</dt><dd>{cleanupComplete ? t("完整", "complete") : t("不完整", "incomplete")}</dd></div>
      <div><dt>{t("失败阶段", "Failure stage")}</dt><dd>{attempt.failure_stage}</dd></div>
    </dl>
    {attempt.failure_message && <div className="ui-evidence-failure" role="alert">
      <strong>{attempt.failure_code}</strong><span>{attempt.failure_message}</span></div>}
    <div className="ui-evidence-diagnostics">
      {Object.entries(attempt.diagnostics).map(([key, value]) => <span key={key}>
        {key.replaceAll("_", " ")} <strong>{value}</strong></span>)}
    </div>
    <details><summary>{t("精确源码、命令与清单绑定", "Exact source, recipe, and manifest binding")}</summary>
      <pre>{JSON.stringify({ source: manifest.source, build: manifest.build,
        start: manifest.start, readiness: manifest.readiness, fixture: manifest.fixture,
        capture: manifest.capture, failure_policy: manifest.failure_policy,
        authority: manifest.authority, fingerprint: manifest.fingerprint }, null, 2)}</pre>
    </details>
    <section className="ui-evidence-steps">
      <h3>{t("步骤收据", "Step receipts")}</h3>
      {steps.length === 0 ? <p>{t("尚无已执行步骤。", "No step has executed.")}</p> : steps.map((step) =>
        <div key={`${step.sequence}:${step.step_id}`}><span><strong>{step.sequence}. {step.step_id}</strong>
          <small>{step.kind} · {formatDate(step.completed_at)}</small></span>
          <StatusBadge status={step.status} />{step.failure_stage !== "none" &&
            <small>{step.failure_stage}{step.message ? ` · ${step.message}` : ""}</small>}</div>)}
    </section>
    <section className="ui-evidence-artifacts">
      <h3>{t("截图与检查记录", "Screenshots and check records")}</h3>
      {artifacts.length === 0 ? <p>{t("尚无产物。", "No artifacts.")}</p> : artifacts.map((artifact) =>
        <div key={artifact.id}><span><strong>{artifact.kind}</strong>
          <code>{artifact.sha256}</code><small>{artifact.mime} · {formatBytes(artifact.bytes)} · {artifact.step_id} · {formatDate(artifact.created_at)} · {artifact.retention_policy} · {artifact.redacted ? t("已脱敏", "redacted") : t("未标记脱敏", "not marked redacted")}</small></span>
          {artifact.mime === "image/png" && <button type="button" className="compact-command" onClick={() => onPreview(artifact)}>{t("查看截图", "View screenshot")}</button>}
          <button aria-label={t(`下载检查记录 ${artifact.id}`, `Download check record ${artifact.id}`)}
            className="icon-button" onClick={() => void onDownload(artifact)} type="button">
            <Download aria-hidden="true" size={14} /></button></div>)}
    </section>
  </>;
}

function artifactFilename(artifact: UIEvidenceArtifactMetadata): string {
  const extension = artifact.mime === "image/png" ? "png" :
    artifact.mime === "application/json" ? "json" : "txt";
  const safeID = artifact.id.replace(/[^a-zA-Z0-9._-]/gu, "-");
  return `untrusted-${artifact.kind}-${safeID}.${extension}`;
}

function humanError(value: unknown): string {
  return value instanceof Error ? value.message : String(value || "Unknown error");
}
