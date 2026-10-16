import { useMemo, useState, type SetStateAction } from "react";
import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { UIEvidenceStartView } from "../api/types";
import { useLocale } from "../lib/locale";
import { ErrorState } from "./common";
import { useVerificationRecord } from "./verification-recovery";
import { emptyUIEvidenceDraft, uiEvidenceDraftKey, validUIEvidenceDraft, validUIEvidenceRequest,
  type UIEvidenceDraft } from "./ui-evidence-recovery";

type StepKind = UIEvidenceStartView["steps"][number]["step"]["kind"];
const emptyFixtureSHA256 = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a";

function localPageURL(value: string): URL | null {
  try {
    const url = new URL(value);
    return url.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname) &&
      url.port && !url.username && !url.password && !url.search && !url.hash ? url : null;
  } catch { return null; }
}

export function UIEvidencePreparation({ client, runID, threadID, pending, unresolved, onStart }: {
  client: APIClient; runID: string; threadID?: string; pending: boolean; unresolved: boolean;
  onStart: (request: UIEvidenceStartView) => void;
}) {
  const { t } = useLocale();
  const draft = useVerificationRecord(uiEvidenceDraftKey(runID), emptyUIEvidenceDraft(runID),
    (value): value is UIEvidenceDraft => validUIEvidenceDraft(value, runID));
  const { serviceID, name, command, directory, url, viewport, theme, steps, advanced } = draft.value;
  const field = <K extends keyof UIEvidenceDraft,>(key: K) => (update: SetStateAction<UIEvidenceDraft[K]>) =>
    draft.edit((current) => ({ ...current, [key]: typeof update === "function"
      ? (update as (previous: UIEvidenceDraft[K]) => UIEvidenceDraft[K])(current[key]) : update }));
  const setServiceID = field("serviceID"), setName = field("name"), setCommand = field("command");
  const setDirectory = field("directory"), setURL = field("url"), setViewport = field("viewport"), setTheme = field("theme");
  const setSteps = field("steps"), setAdvanced = field("advanced");
  const prepared = useMemo(() => {
    if (!draft.value.prepared) return null;
    try { const value: unknown = JSON.parse(advanced); return validUIEvidenceRequest(value) ? value : null; }
    catch { return null; }
  }, [draft.value.prepared, advanced]);
  const setPrepared = (value: UIEvidenceStartView | null) => draft.edit((current) => ({ ...current,
    prepared: Boolean(value), ...(value ? { advanced: JSON.stringify(value, null, 2) } : {}) }));
  const [review, setReview] = useState({ store: draft.store, key: draft.key, checked: false });
  const reviewed = review.store === draft.store && review.key === draft.key && review.checked;
  const setReviewed = (checked: boolean) => setReview({ store: draft.store, key: draft.key, checked });
  const [error, setError] = useState("");
  const [preparing, setPreparing] = useState(false);
  const locked = pending || unresolved || !client.hasUIEvidence;
  const services = useQuery({ queryKey: ["run", runID, "ui-evidence-services", threadID],
    queryFn: ({ signal }) => client.listThreadApplicationServices(threadID!, signal),
    enabled: Boolean(threadID && typeof client.listThreadApplicationServices === "function"), retry: false });
  const selected = services.data?.services.find((service) => service.job_id === serviceID && service.run_id === runID);
  const service = useQuery({ queryKey: ["run", runID, "ui-evidence-service", threadID, serviceID],
    queryFn: ({ signal }) => client.getThreadApplicationService(threadID!, serviceID, signal),
    enabled: Boolean(selected), retry: false });
  const source = useQuery({ queryKey: ["run", runID, "ui-evidence-service-command", threadID, selected?.source_call_id],
    queryFn: async ({ signal }) => {
      const value = await client.threadActivityDetail(threadID!, selected!.source_call_id!, signal);
      if (value.run_id !== runID || value.activity_ref !== selected!.source_call_id) throw new Error(t("启动记录来源已变化，请重新选择服务。", "The launch record source changed. Select the service again."));
      return value;
    }, enabled: Boolean(selected?.source_call_id), retry: false });
  const invalidate = () => { setPrepared(null); setReviewed(false); setError(""); };
  const useService = () => {
    const commands = source.data?.tools.flatMap((tool) => tool.name === "command_runtime" && tool.detail.kind === "command" ? tool.detail.command.commands : []) ?? [];
    const candidate = service.data?.candidate_urls.find((entry) => localPageURL(entry.url));
    if (commands.length !== 1 || !candidate || service.data?.service.run_id !== runID || service.data.service.job_id !== serviceID) {
      setError(t("该记录未提供唯一启动命令和本机地址。请按项目启动方式填写下方表单。", "The record does not provide one launch command and a local address. Fill in the project's launch settings below.")); return;
    }
    invalidate(); setName(t("所选应用服务", "Selected application service"));
    setCommand(commands[0]!.command); setDirectory(commands[0]!.working_directory); setURL(candidate.url);
  };
  const prepare = async () => {
    setError(""); setPreparing(true); setReviewed(false);
    try {
      if (!name.trim() || !command.trim()) throw new Error(t("填写应用名称和启动命令。", "Enter the application name and launch command."));
      const target = localPageURL(url);
      if (!target) {
        throw new Error(t("填写带端口的本机 HTTP 页面地址，例如 http://127.0.0.1:端口/页面。", "Enter a local HTTP page address with a port, such as http://127.0.0.1:port/page."));
      }
      if (!directory.trim() || directory.startsWith("/") || /^[a-z]:/iu.test(directory) || directory.split(/[\\/]/u).includes("..")) throw new Error(t("工作目录需位于当前项目内。", "The working directory must be inside the current project."));
      const runtimeSteps: UIEvidenceStartView["steps"] = [{ step: { id: "navigate", kind: "navigate", capture_after: true } }];
      for (const step of steps) {
        if (step.kind !== "capture" && !step.selector.trim()) throw new Error(t("为交互和断言步骤填写页面选择器。", "Enter a page selector for interaction and assertion steps."));
        const input = step.kind === "type" ? step.input : undefined;
        const inputHash = input === undefined ? undefined : Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(input))), (byte) => byte.toString(16).padStart(2, "0")).join("");
        runtimeSteps.push({ step: { id: step.id, kind: step.kind, capture_after: true,
          ...(step.kind !== "capture" ? { selector: step.selector } : {}), ...(inputHash ? { input_sha256: inputHash } : {}) }, ...(input !== undefined ? { input } : {}) });
      }
      const request: UIEvidenceStartView = {
        operation_key: `desktop-ui-evidence-${crypto.randomUUID()}`,
        start: { version: "command-runtime.v2", profile: "powershell", script: command,
          working_directory: directory, environment: [], stdin_policy: "closed", close_initial_stdin: true,
          timeout_milliseconds: 1_800_000, output: { inline_bytes: 16_384, artifact_bytes: 262_144 },
          network: "disabled", credentials: "none", purpose: `UI evidence: ${name.trim()}` },
        readiness: { url: target.href, method: "GET", expected_status: [200], timeout_milliseconds: 60_000, interval_milliseconds: 250 },
        url: target.href, route: target.pathname, browser: { product: "edge", channel: "stable" },
        environment: { viewport: viewport === "mobile" ? { width: 390, height: 844, dpr: 1 } : { width: 1440, height: 900, dpr: 1 }, locale: "zh-CN", theme, reduced_motion: viewport === "mobile" },
        fixture: { name: name.trim(), seed: "ui-evidence-v1", page_state: "{}", data_sha256: emptyFixtureSHA256, deterministic: true, synthetic: true },
        steps: runtimeSteps,
        capture: { screenshot: true, dom: true, accessibility: true, console: true, network: true, performance: true, video: false, mask_selectors: [] },
        failure_policy: { fail_on_console_error: true, fail_on_page_error: true, fail_on_request_error: true, fail_on_http_status: true },
      };
      setPrepared(request); setAdvanced(JSON.stringify(request, null, 2));
    } catch (caught) { setPrepared(null); setError(caught instanceof Error ? caught.message : String(caught)); }
    finally { setPreparing(false); }
  };
  const kinds: Array<[StepKind, string]> = [["click", t("点击", "Click")], ["type", t("输入测试文字", "Type test text")], ["assert_present", t("确认元素存在", "Assert present")], ["assert_absent", t("确认元素消失", "Assert absent")], ["capture", t("截图", "Capture")]];
  return <details className="ui-evidence-launch"><summary>{t("准备并启动浏览器验证", "Prepare and start browser verification")}</summary>
    {draft.error && <div className="inline-warning" role="status"><p>{draft.error}</p>
      {draft.store && <button type="button" className="compact-command" disabled={pending || unresolved || preparing}
        onClick={() => { try { draft.discard(); setReviewed(false); setError(""); } catch (failure) { setError(String(failure)); } }}>{t("放弃未保存草稿", "Discard unsaved draft")}</button>}
    </div>}
    <p>{t("选择应用服务的启动记录，或填写项目启动方式。验证会启动本次专用服务和浏览器，完成后清理；请选用测试数据与空闲端口。", "Choose an application service's launch record or enter the project's launch settings. Verification starts its own service and browser and cleans them up afterward. Use test data and a free port.")}</p>
    <details><summary>{t("准备本次执行权限", "Prepare execution authority")}</summary><p>{t("在任务设置选择 Code、Local 和 Deliver，并在任务权限中激活 Full 与 restricted 浏览器控制。启动时会核对当前根执行租约。", "Choose Code, Local, and Deliver in task settings, then activate Full and restricted browser control in task permissions. Starting verifies the current root execution lease.")}</p></details>
    <fieldset disabled={locked || preparing}>
      {threadID && <><label>{t("应用服务", "Application service")}<select value={serviceID} onChange={(event) => { setServiceID(event.target.value); invalidate(); }}>
        <option value="">{t("手动填写项目启动方式", "Enter project launch settings")}</option>
        {services.data?.services.filter((entry) => entry.run_id === runID).map((entry) => <option key={entry.job_id} value={entry.job_id}>{entry.state} · {entry.job_id}</option>)}
      </select></label>
      {services.isError && <ErrorState error={services.error} />}
      {selected && <><button type="button" className="compact-command" disabled={!source.data || !service.data} onClick={useService}>{t("使用所选启动记录", "Use selected launch record")}</button>
        <p>{!selected.source_call_id
          ? t("该服务没有可复用的启动记录，请按项目启动方式填写下方表单。", "This service has no reusable launch record. Fill in the project's launch settings below.")
          : t("读取启动记录后核对下方命令，并把地址改为本次专用空闲端口。", "Read the launch record, review the command below and change the address to a free port dedicated to this verification.")}</p>
        {(source.isError || service.isError) && <ErrorState error={source.error || service.error} />}</>}
      {services.data?.services.filter((entry) => entry.run_id === runID).length === 0 && <p>{t("当前执行尚无应用服务记录。可填写项目启动命令，或回任务要求启动应用后再读取。", "This execution has no application service records yet. Enter its launch command or ask the task to start the application, then read the records again.")}</p>}</>}
      <label>{t("应用名称", "Application name")}<input maxLength={256} value={name} onChange={(event) => { setName(event.target.value); invalidate(); }} /></label>
      <label>{t("启动命令（PowerShell）", "Launch command (PowerShell)")}<textarea rows={3} maxLength={65536} value={command} onChange={(event) => { setCommand(event.target.value); invalidate(); }} /></label>
      <label>{t("项目内工作目录", "Working directory in project")}<input maxLength={4096} value={directory} onChange={(event) => { setDirectory(event.target.value); invalidate(); }} /></label>
      <label>{t("本次页面地址", "Page address for this verification")}<input type="url" maxLength={4096} value={url} onChange={(event) => { setURL(event.target.value); invalidate(); }} /></label>
      <label>{t("页面尺寸", "Page size")}<select value={viewport} onChange={(event) => { setViewport(event.target.value as "desktop" | "mobile"); invalidate(); }}><option value="desktop">1440 × 900</option><option value="mobile">390 × 844</option></select></label>
      <label>{t("主题", "Theme")}<select value={theme} onChange={(event) => { setTheme(event.target.value as "light" | "dark"); invalidate(); }}><option value="light">{t("浅色", "Light")}</option><option value="dark">{t("深色", "Dark")}</option></select></label>
      <p>{t("先打开页面并收集截图、结构、无障碍与错误记录。按需要添加后续检查。", "The first step opens the page and collects screenshots, structure, accessibility, and errors. Add further checks as needed.")}</p>
      {steps.map((step, index) => <div key={step.id} className="ui-evidence-draft-step">
        <label>{t(`步骤 ${index + 2}`, `Step ${index + 2}`)}<select value={step.kind} onChange={(event) => { setSteps((current) => current.map((entry) => entry.id === step.id ? { ...entry, kind: event.target.value as StepKind } : entry)); invalidate(); }}>{kinds.map(([kind, label]) => <option key={kind} value={kind}>{label}</option>)}</select></label>
        {step.kind !== "capture" && <label>{t("页面选择器", "Page selector")}<input value={step.selector} onChange={(event) => { setSteps((current) => current.map((entry) => entry.id === step.id ? { ...entry, selector: event.target.value } : entry)); invalidate(); }} /></label>}
        {step.kind === "type" && <label>{t("测试文字", "Test text")}<input value={step.input} onChange={(event) => { setSteps((current) => current.map((entry) => entry.id === step.id ? { ...entry, input: event.target.value } : entry)); invalidate(); }} /></label>}
        <button type="button" onClick={() => { setSteps((current) => current.filter((entry) => entry.id !== step.id)); invalidate(); }}>{t("移除步骤", "Remove step")}</button>
      </div>)}
      <button type="button" disabled={steps.length >= 63} onClick={() => { setSteps((current) => [...current, { id: `step-${crypto.randomUUID()}`, kind: "assert_present", selector: "", input: "" }]); invalidate(); }}>{t("添加检查步骤", "Add check step")}</button>
      <button type="button" className="command-button" onClick={() => void prepare()}>{preparing ? t("正在生成清单", "Preparing manifest") : t("预览验证清单", "Preview verification manifest")}</button>
    </fieldset>
    {prepared && <><p>{t("本次会执行下列启动命令和浏览器步骤，并保存截图与检查结果。", "This verification runs the launch command and browser steps below and saves screenshots and check results.")}</p>
      <pre aria-label={t("本次精确验证清单", "Exact verification manifest")}>{JSON.stringify(prepared, null, 2)}</pre>
      <details><summary>{t("高级：编辑完整 JSON", "Advanced: edit complete JSON")}</summary>
        <textarea aria-label={t("精确 UI 证据启动 JSON", "Exact UI evidence launch JSON")} disabled={locked} value={advanced} onChange={(event) => { setAdvanced(event.target.value); setReviewed(false); setPrepared(null); }} />
      </details></>}
    {!prepared && advanced && <details open><summary>{t("高级：编辑完整 JSON", "Advanced: edit complete JSON")}</summary><textarea aria-label={t("精确 UI 证据启动 JSON", "Exact UI evidence launch JSON")} disabled={locked} value={advanced} onChange={(event) => { setAdvanced(event.target.value); setReviewed(false); }} />
      <button type="button" disabled={locked} onClick={() => { try { const parsed: unknown = JSON.parse(advanced); if (!validUIEvidenceRequest(parsed)) throw new Error(t("填写完整且符合执行边界的启动清单。", "Enter a complete launch manifest within the execution boundaries.")); setPrepared(parsed); setReviewed(false); setError(""); } catch (caught) { setError(String(caught)); } }}>{t("预览编辑后的清单", "Preview edited manifest")}</button></details>}
    <label><input type="checkbox" checked={reviewed} disabled={locked || !prepared} onChange={(event) => setReviewed(event.target.checked)} />{t("我已核对命令、端口与检查步骤；应用使用可复现的测试数据，输入与页面不含秘密或个人数据。", "I reviewed the command, port, and steps. The application uses reproducible test data, and inputs and pages contain no secrets or personal data.")}</label>
    <button type="button" className="command-button" disabled={locked || !prepared || !reviewed || Boolean(draft.error)} onClick={() => { if (prepared && reviewed && !draft.error) onStart(prepared); }}>{t("启动真实浏览器验证", "Start real-browser verification")}</button>
    {error && <p role="alert">{error}</p>}
  </details>;
}
