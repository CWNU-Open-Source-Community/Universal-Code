import { useLocale, type LocaleText } from "../../lib/locale";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Ellipsis, FileDiff, Globe2, LoaderCircle, ShieldAlert, SquareTerminal } from "lucide-react";
import type { APIClient } from "../../api/client";
import { isRecord } from "../../api/client/validation";
import type { ApprovalDecisionControlRequestView, ApprovalPreviewView, ApprovalQueueItemView } from "../../api/types";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { ComputerUsePreview } from "../../components/computer-use-preview";
import { ApprovalDenialReason, approvalToolLabel, fileApprovalLabel, readApprovalPreview } from "../../components/approval-controls";
import { removeDecidedApproval, v2QueryKeys } from "../query-keys";

type Action = ApprovalDecisionControlRequestView["action"];
type Fields = ApprovalPreviewView["fields"];
const fieldLabels = (t: LocaleText): Record<string, string> => ({ command: t("命令", "Command"), executable: t("可执行文件", "Executable"),
  arguments: t("参数（按顺序）", "Arguments (in order)"), requested_backend: t("提案环境", "Proposed environment"), operation: t("操作", "Operation"),
  action: t("操作", "Action"), intent: t("命令请求", "Command request"), server: t("服务", "Server"), tool: t("工具", "Tool"),
  parameters: t("精确参数", "Exact parameters"), summary: t("影响", "Impact"), path: t("文件", "File"), destination_path: t("目标文件", "Destination file"),
  url: t("网址", "URL"), body_bytes: t("请求体大小（字节）", "Request body size (bytes)"), body_sha256: t("请求体 SHA-256", "Request body SHA-256"), expires_at: t("有效期至", "Expires at"),
  host: t("主机", "Host"), effect: t("操作类型", "Action type"), review_scope: t("用途与风险范围", "Purpose and risk scope"), bounded_review: t("有界审批说明", "Bounded approval details"),
  app_id: t("目标应用", "Target app"), app_path: t("应用路径", "App path"), window_id: t("目标窗口", "Target window"), tool_name: t("操作工具", "Tool"), authorization_scope: t("授权范围", "Permission scope"),
  working_directory: t("工作目录", "Working directory"), workspace_id: t("工作区", "Workspace"), purpose: t("用途", "Purpose"),
  network: t("网络声明", "Network policy"), credentials: t("凭据权限", "Credentials"), environment: t("环境变量", "Environment"),
  initial_stdin: t("初始输入", "Initial input"), stdin_policy: t("输入模式", "Input mode"), workspace_mode: t("工作区模式", "Workspace mode"),
  grant_ttl_seconds: t("原定有效秒数", "Original duration in seconds"), grant_max_uses: t("原定命令次数", "Original command limit"), grant_uses_remaining: t("剩余次数", "Uses remaining"), grant_expires_at: t("到期时间", "Expires at") });
const effectText = (t: LocaleText): Record<ApprovalPreviewView["effect"], string> => ({
  command_process: t("仅限当前任务的这批命令或标准输入，可能访问宿主文件和网络。输入或权限变化后需重新批准。", "These commands or standard input are limited to this run and may reach host files and the network. Changed input or permissions need fresh approval."),
  mcp_server_and_tool: t("授权启动或连接此服务、发现能力并调用此工具。外部影响仍待核实，请核对服务配置与参数。", "Allows starting or connecting to this server, discovering capabilities, and calling this tool. Check the configuration and arguments for external effects."),
  computer_use_app: t("按任务授权后，Agent 可继续点击、输入和滚动此应用，直至任务结束。", "A task grant lets the agent click, type and scroll in this app until the task ends."),
  dry_run: t("仅记录模拟执行，不启动进程。", "Records the simulation without starting a process."),
  record_git_approval: t("单次授权。执行时将再次核对仓库、权限和预览。", "One-time authorization. Execution rechecks the repository, permissions and preview."),
  file_review_required: t("在任务的「审阅改动」中查看差异并批准。", "Open Review changes in the task to inspect the diff and approve."),
  fetch_public_https: t("对话授权仅覆盖此主机的公开 HTTPS 读取。", "Conversation access covers public HTTPS reads from this host."),
  browser_sensitive_action: t("仅限当前页面的这一次操作。页面或权限变化后需重新批准。", "Applies to this action on the current page. Changed pages or permissions need fresh approval."),
  unavailable: t("此类操作暂不支持在这里批准，请回到对话查看对应的操作入口。", "This action cannot be approved here. Return to the conversation for its controls."),
});

// Keep invisible direction overrides and control characters from changing how a reviewed value reads.
function visibleText(value: string) {
  return value.replace(/[\u0000-\u0008\u000b-\u000c\u000e-\u001f\u007f-\u009f\u061c\u200b-\u200f\u202a-\u202e\u2060-\u206f\ufeff]|\r(?!\n)/gu,
    (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`);
}

function ApprovalFields({ fields }: { fields: Fields }) {
  const { t } = useLocale();
  const labels = fieldLabels(t);
  if (!fields.length) return null;
  return <dl className="v2-approval-facts">{fields.map((field, index) => <div key={`${field.name}:${index}`}>
    <dt>{Object.hasOwn(labels, field.name) ? labels[field.name] : visibleText(field.name)}</dt><dd><pre>{visibleText(field.value)}</pre></dd>
  </div>)}</dl>;
}

function readJSON(value: string): unknown {
  try { return JSON.parse(value); } catch { return undefined; }
}

function argumentLine(executable: string, args: unknown) {
  if (!executable || !Array.isArray(args) || !args.every((arg): arg is string => typeof arg === "string")) return undefined;
  // Native processes receive literal argv. Quote empty, spaced and special tokens independently.
  return [executable, ...args].map((arg) => /^[\w./:@%=,+-]+$/u.test(arg) ? arg : JSON.stringify(arg)).join(" ");
}

type ReviewedCommand = { line: string; shell?: "PowerShell" | "Bash"; purpose?: string; facts?: Fields };
type CommandPreview = { commands: ReviewedCommand[]; consumed: string[]; action?: "run" | "start"; failurePolicy?: string };

function commandPreview(tool: string, fields: Fields): CommandPreview | undefined {
  const get = (name: string) => fields.find((field) => field.name === name)?.value;
  if (tool === "shell") {
    const line = get("command");
    return line ? { commands: [{ line }], consumed: ["command"] } : undefined;
  }
  if (tool === "script_process") {
    const line = argumentLine(get("executable") ?? "", readJSON(get("arguments") ?? ""));
    return line ? { commands: [{ line }], consumed: ["executable", "arguments"] } : undefined;
  }
  if (tool !== "command_runtime") return undefined;
  const payload = readJSON(get("intent") ?? "");
  if (!isRecord(payload) || payload.version !== "command-runtime.v2" || (payload.action !== "run" && payload.action !== "start") ||
    (get("action") !== undefined && get("action") !== payload.action) || !Array.isArray(payload.commands) || !payload.commands.length) return undefined;
  const commands: ReviewedCommand[] = [];
  for (const command of payload.commands) {
    if (!isRecord(command)) return undefined;
    let line: string | undefined;
    let shell: ReviewedCommand["shell"];
    if ((command.profile === "powershell" || command.profile === "bash") && typeof command.script === "string" &&
      command.executable === undefined && command.arguments === undefined) {
      line = command.script;
      shell = command.profile === "powershell" ? "PowerShell" : "Bash";
    }
    if (command.profile === "process" && typeof command.executable === "string" && command.script === undefined) {
      line = argumentLine(command.executable, command.arguments === undefined ? [] : command.arguments);
    }
    if (!line) return undefined;
    const facts: Fields = [];
    for (const [name, defaultValue] of Object.entries({ working_directory: ".", network: "disabled", credentials: "none",
      stdin_policy: "closed", initial_stdin: "", workspace_mode: "exclusive", environment: [] })) {
      const value = command[name];
      if (value !== undefined && value !== defaultValue && !(Array.isArray(value) && value.length === 0)) {
        facts.push({ name, value: typeof value === "string" ? value : JSON.stringify(value) });
      }
    }
    commands.push({ line, shell, purpose: typeof command.purpose === "string" ? command.purpose.trim() : undefined, facts });
  }
  return { commands, consumed: ["intent", "action"], action: payload.action, failurePolicy: typeof payload.failure_policy === "string" ? payload.failure_policy : undefined };
}

function commandQuestion(t: LocaleText, preview: CommandPreview | undefined, action?: string) {
  if (action === "write_stdin") return t("允许向这个进程发送输入？", "Allow sending input to this process?");
  if (preview && preview.commands.length > 1) {
    return t(`允许按顺序运行这 ${preview.commands.length} 条命令？`, `Allow running these ${preview.commands.length} commands in order?`);
  }
  const purpose = preview?.commands[0]?.purpose;
  if (purpose && purpose.length <= 360 && visibleText(purpose) === purpose && !/[\r\n\t\u2028\u2029]/u.test(purpose) && /[?？]$/u.test(purpose)) return purpose;
  return action === "start" ? t("允许在后台启动这个进程？", "Allow starting this process in the background?")
    : t("允许运行这条命令？", "Allow running this command?");
}

export function V2ApprovalCards({ client, runID, threadID, onReviewFile }: {
  client: APIClient; runID: string; threadID: string;
  onReviewFile?: (target: FileEditReviewTarget, trigger: HTMLButtonElement) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<{ approvalID: string; message: string }>();
  const query = useQuery({
    queryKey: v2QueryKeys.approvals(runID),
    queryFn: ({ signal }) => client.approvalQueue(runID, signal),
    enabled: Boolean(runID),
    refetchInterval: 2_000,
  });
  const items = query.data?.items;
  useEffect(() => {
    if (notice && items?.length && !items.some((item) => item.id === notice.approvalID)) setNotice(undefined);
  }, [items, notice]);
  const decided = (approvalID: string, message: string) => {
    removeDecidedApproval(queryClient, runID, (item) => item.id === approvalID);
    setNotice({ approvalID, message });
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.approvals(runID) });
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.transcript(threadID) });
  };
  return <>
    {notice && items?.length === 0 && <p className="v2-notice" role="status">{notice.message}</p>}
    {query.isLoading && <p className="v2-notice" role="status">{t("正在读取待审批操作…", "Loading pending approvals…")}</p>}
    {query.isError && <div className="v2-notice tone-warning" role="alert">
      {t("无法读取待审批操作，请重试后再作决定。", "Could not load pending approvals. Retry before deciding.")} {query.error instanceof Error && query.error.message}
      <button onClick={() => void query.refetch()} type="button">{t("重试审批队列", "Retry approval queue")}</button>
    </div>}
    {Boolean(items?.length) && <section aria-label={t("待处理审批", "Pending approvals")} className="v2-approval-stack">
      {!client.hasApprovalControl && <p className="v2-notice" role="status">
        {t("当前连接可以查看审批。需要批准、拒绝或恢复时，请使用具有审批控制权限的连接。", "This connection can view approvals. Use a connection with approval controls to approve, deny or resume.")}</p>}
      {items?.map((item) => <ApprovalCard client={client} item={item} key={item.id}
        notice={notice?.approvalID === item.id ? notice.message : undefined}
        onDecided={(message) => decided(item.id, message)} runID={runID} onReviewFile={onReviewFile} />)}
      {query.data?.truncated && <p role="status">{t("待审批操作较多；处理后会继续显示其余操作。", "More approvals are waiting. Remaining actions appear as you process these.")}</p>}
    </section>}
  </>;
}

function ApprovalCard({ client, item, runID, notice, onDecided, onReviewFile }: {
  client: APIClient; item: ApprovalQueueItemView; runID: string;
  notice?: string; onDecided: (message: string) => void;
  onReviewFile?: (target: FileEditReviewTarget, trigger: HTMLButtonElement) => void;
}) {
  const { t } = useLocale();
  const headingID = useId();
  const targetID = useId();
  const menuID = useId();
  const primary = useRef<HTMLButtonElement>(null);
  const deny = useRef<HTMLButtonElement>(null);
  const menuTrigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const focusLastOption = useRef(false);
  const detailsPanel = useRef<HTMLDivElement>(null);
  const limitsPanel = useRef<HTMLFieldSetElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [limitsOpen, setLimitsOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [requestedTTL, setGrantTTL] = useState(120);
  const [requestedUses, setGrantUses] = useState(2);
  const operationKeys = useRef(new Map<string, string>());
  const decisionPending = useRef(false);
  const preview = useQuery({
    queryKey: ["v2", "approval-preview", runID, item.id, item.version],
    queryFn: ({ signal }) => readApprovalPreview(client, item, runID, signal, t),
    retry: false,
  });
  const webFetch = item.tool_name === "web_fetch";
  const browserRequest = item.tool_name === "agent_browser_sensitive" && item.proposal_id.startsWith("browser-network-");
  const computerUse = item.tool_name === "mcp_tool_call" && preview.data?.effect === "computer_use_app";
  const fileReview = preview.data?.effect === "file_review_required";
  const fileTool = fileApprovalLabel(item.tool_name, t);
  const terminal = item.tool_name === "command_runtime" || item.tool_name === "shell" || item.tool_name === "script_process";
  const fields = preview.data?.fields ?? [];
  const fieldValue = (key: string) => fields.find(({ name }) => name === key)?.value;
  const filePath = fileReview ? fieldValue("path") : undefined;
  const destination = fileReview ? fieldValue("destination_path") : undefined;
  const webHost = webFetch ? fieldValue("host") || item.exact_target : undefined;
  const webURL = webFetch ? fieldValue("url") : undefined;
  const command = terminal ? commandPreview(item.tool_name, fields) : undefined;
  const firstShell = command?.commands[0]?.shell;
  const commonShell = firstShell && command?.commands.every(({ shell }) => shell === firstShell) ? firstShell : undefined;
  const requestDetails = browserRequest ? fields.filter(({ name }) => name === "body_sha256" || name === "expires_at") : [];
  const boundedApproval = client.hasApprovalControl && item.tool_name === "command_runtime" && item.allowed_actions.includes("approve_for_run");
  const grantFields = boundedApproval ? fields.filter(({ name }) => name === "review_scope" || name === "bounded_review" || name.startsWith("grant_")) : [];
  const visibleFields = fields.filter((field) => {
    if (fileReview && (field.name === "path" || field.name === "destination_path" || (field.name === "operation" && `${field.value}_file` === item.tool_name))) return false;
    if (webFetch && (field.name === "host" || field.name === "url")) return false;
    if (command && (command.consumed.includes(field.name) || field.name === "summary" || field.name === "review_scope")) return false;
    return !requestDetails.includes(field) && !grantFields.includes(field);
  });
  const recovering = item.status === "approved" || item.status === "denied";
  const existingTTL = Number(preview.data?.fields.find((field) => field.name === "grant_ttl_seconds")?.value);
  const existingUses = Number(preview.data?.fields.find((field) => field.name === "grant_max_uses")?.value);
  const existingGrant = Number.isInteger(existingTTL) && existingTTL >= 1 && existingTTL <= 900 &&
    Number.isInteger(existingUses) && existingUses >= 1 && existingUses <= 8;
  const grantTTL = existingGrant ? existingTTL : requestedTTL;
  const grantUses = existingGrant ? existingUses : requestedUses;
  const mutation = useMutation({
    mutationFn: (action: Action) => {
      const denialReason = action === "deny" ? reason.trim() : "";
      const intent = `${item.id}:${action}:${denialReason}:${action === "approve_for_run" ? `${grantTTL}:${grantUses}` : ""}`;
      let key = operationKeys.current.get(intent);
      if (!key) {
        key = `v2-approval-${globalThis.crypto.randomUUID()}`;
        operationKeys.current.set(intent, key);
      }
      return client.decideApproval(runID, item.id, { version: "approval_control.v1", action,
        ...(denialReason ? { reason: denialReason } : {}),
        ...(action === "approve_for_run" ? { grant_ttl_seconds: grantTTL, grant_max_uses: grantUses } : {}) }, key);
    },
    onSuccess: (result, action) => {
      if (browserRequest) {
        onDecided(action === "deny" ? t("已拒绝这次网页请求。", "This web request was denied.") : t("已批准这次网页请求。", "This web request was approved."));
        return;
      }
      const continuation = result.continuation;
      const next = ["agent_browser_sensitive", "mcp_tool_call", "command_runtime"].includes(item.tool_name) ? continuation?.state === "queued"
        ? t("Agent 已接收继续处理的请求。", "The agent received the request to continue.")
        : continuation?.state === "completed" || continuation?.state === "waiting_approval"
          ? t("Agent 已继续处理，可在工作记录中查看结果。", "The agent continued. Check the work log for results.")
          : t("决定已保存，后续执行尚未完成；请查看工作记录后继续。", "The decision is saved, but execution is incomplete. Check the work log before continuing.")
        : webFetch ? result.retry_scheduled
        ? t("后台已安排继续处理，可在工作记录中查看结果。", "Processing is scheduled to continue. Check the work log for results.")
        : t("读取尚未恢复；可以发送消息让 Agent 继续。", "The read has not resumed. Send a message to ask the agent to continue.")
        : preview.data?.effect === "dry_run" && action !== "deny"
          ? t("模拟执行已记录，没有启动真实进程。", "Simulated execution recorded; no real process was started.")
          : action !== "deny" ? t("批准已记录，操作等待执行；可在工作记录中查看进度。", "Approval recorded. The action awaits execution; follow progress in the work log.") : t("此提案已关闭。", "This proposal is closed.");
      onDecided(`${action === "deny" ? t("已拒绝。", "Denied. ") : t("已批准。", "Approved. ")}${next}`);
    },
    onError: () => { void preview.refetch(); },
    onSettled: () => { decisionPending.current = false; },
  });
  const decide = (action: Action) => {
    if (decisionPending.current) return;
    decisionPending.current = true;
    mutation.mutate(action);
  };
  const canApprove = client.hasApprovalControl && preview.isSuccess && !preview.isFetching && preview.data.source_current &&
    !preview.data.truncated && !mutation.isPending;
  const dryRun = preview.data?.effect === "dry_run";
  const source = terminal ? t("终端", "Terminal") : fileTool ?? (webFetch ? t("网页读取", "Web read")
    : browserRequest ? t("网页请求", "Web request") : computerUse ? t("电脑操作", "Computer use")
    : item.tool_name === "mcp_tool_call" ? "MCP" : item.tool_name === "git.advanced" ? "Git" : approvalToolLabel(item.tool_name, t));
  const fileQuestions: Record<string, string> = {
    create_file: t("创建前审阅这个文件？", "Review this file before creating it?"),
    replace_file: t("应用前审阅这次文件修改？", "Review these file changes before applying them?"),
    move_file: t("移动前审阅这个文件？", "Review this file before moving it?"),
    delete_file: t("删除前审阅这个文件？", "Review this file before deleting it?"),
  };
  const title = recovering ? webFetch ? t("恢复上次网页读取", "Resume previous web read") : t("恢复上次审批决定", "Resume previous approval decision")
    : fileTool ? fileQuestions[item.tool_name] : browserRequest ? t("允许发送这次网页请求？", "Allow this web request?")
      : webFetch ? webHost ? t(`允许读取 ${visibleText(webHost)} 吗？`, `Allow reading ${visibleText(webHost)}?`) : t("允许读取这个网站？", "Allow reading this website?")
      : computerUse ? t("允许操作这个应用？", "Allow controlling this app?") : dryRun ? t("批准这次模拟执行？", "Approve this simulated execution?")
      : terminal ? commandQuestion(t, command, command?.action ?? fieldValue("action"))
      : item.tool_name === "mcp_tool_call" ? t("允许调用这个 MCP 工具？", "Allow this MCP tool call?")
      : item.tool_name === "git.advanced" ? t("允许这次 Git 操作？", "Allow this Git operation?")
      : t("允许执行这项操作？", "Allow this action?");
  const Icon = terminal ? SquareTerminal : webFetch || item.tool_name === "agent_browser_sensitive" ? Globe2 : fileTool ? FileDiff : ShieldAlert;
  const canDeny = client.hasApprovalControl && !recovering && item.allowed_actions.includes("deny");
  const alternative = !client.hasApprovalControl || recovering ? undefined
    : webFetch && item.allowed_actions.includes("approve_for_thread") ? { action: "approve_for_thread",
      label: webHost ? t(`本对话允许读取 ${visibleText(webHost)}`, `Allow reads from ${visibleText(webHost)} for this conversation`) : t("本对话允许", "Allow for this conversation") } as const
    : computerUse && item.allowed_actions.includes("approve_for_run_app") ? { action: "approve_for_run_app", label: t("本任务允许操作该应用", "Allow this app for this task") } as const
    : boundedApproval ? { action: "approve_for_run", label: t("本次执行的限时、限次审批", "Time and usage limits for this run") } as const : undefined;
  const replayAction = item.allowed_actions.find((action): action is "approve_once" | "approve_for_thread" | "deny" =>
    item.status === "denied" ? action === "deny" : action === "approve_once" || (webFetch && action === "approve_for_thread"));
  const primaryAction = fileTool || fileReview ? (onReviewFile && preview.isSuccess && fileReview ? "review" : undefined)
    : !client.hasApprovalControl ? undefined : recovering ? replayAction
    : item.allowed_actions.includes("approve_once") ? "approve_once" : alternative?.action;
  const primaryLabel = primaryAction === "review" ? t("审阅差异", "Review diff") : recovering ? t("继续恢复", "Resume")
    : primaryAction === alternative?.action && alternative ? alternative.label : computerUse ? t("本次允许", "Allow once")
    : dryRun ? t("批准模拟一次", "Approve simulation once") : webFetch || terminal ? t("允许一次", "Allow once") : t("批准一次", "Approve once");
  const primaryDisabled = primaryAction === "review" ? preview.isFetching : primaryAction === "deny" ? mutation.isPending : !canApprove;
  const menuOption = primaryAction && alternative?.action !== primaryAction ? alternative : undefined;
  const scope = preview.data && (computerUse && !item.allowed_actions.includes("approve_for_run_app")
    ? t("本次允许仅批准当前操作；后续需要授权的操作将再次询问。", "Allow once approves only this action. Later actions requiring permission will ask again.")
    : webFetch && !item.allowed_actions.includes("approve_for_thread") ? t("仅限此网址的一次公开 HTTPS 读取。", "One public HTTPS read of this URL.")
    : effectText(t)[preview.data.effect]);
  const compactScope = Boolean(preview.data && (command || webFetch || dryRun));
  const detailFields = fields.filter((field) => requestDetails.includes(field) || ((command || boundedApproval) && field.name === "review_scope") ||
    (command && (field.name === "intent" || field.name === "arguments" || field.name === "summary")));
  const singlePurpose = command?.commands.length === 1 && command.commands[0].purpose;
  if (singlePurpose && singlePurpose !== title) detailFields.unshift({ name: "purpose", value: singlePurpose });
  if (preview.data?.working_directory === "." && !fileReview) detailFields.push({ name: "working_directory", value: "." });
  if (preview.data?.workspace_id) detailFields.push({ name: "workspace_id", value: preview.data.workspace_id });
  const detailLabel = t("请求详情", "Request details");
  const menuLabel = t("审批选项", "Approval options");
  const hasDetails = detailFields.length > 0 || compactScope || canDeny;
  const actionable = Boolean(primaryAction || canDeny);

  useEffect(() => {
    if (!menuOpen) return;
    const options = menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)");
    options?.[focusLastOption.current ? options.length - 1 : 0]?.focus();
    const outside = (event: PointerEvent) => {
      if (!menuTrigger.current?.contains(event.target as Node) && !menu.current?.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [menuOpen]);
  useEffect(() => { if (detailsOpen) detailsPanel.current?.focus(); }, [detailsOpen]);
  useEffect(() => { if (limitsOpen) limitsPanel.current?.focus(); }, [limitsOpen]);

  const closeMenu = () => { setMenuOpen(false); menuTrigger.current?.focus(); };
  const closeLimits = () => { setLimitsOpen(false); (menuTrigger.current ?? primary.current)?.focus(); };
  function handleKey(event: KeyboardEvent<HTMLElement>) {
    if (event.defaultPrevented || event.nativeEvent.isComposing || event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    if (event.key === "Escape" && menuOpen) { event.preventDefault(); event.stopPropagation(); closeMenu(); return; }
    if (event.key === "Escape" && limitsOpen) { event.preventDefault(); event.stopPropagation(); closeLimits(); return; }
    if (event.key === "Escape" && detailsOpen) { event.preventDefault(); event.stopPropagation(); setDetailsOpen(false); menuTrigger.current?.focus(); return; }
    const target = event.target;
    if (!(target instanceof HTMLElement) || target.closest("input, textarea, select, [contenteditable=true], details")) return;
    const button = event.key === "Enter" && target === event.currentTarget ? primary.current : event.key === "Escape" ? deny.current : null;
    if (button && !button.disabled) { event.preventDefault(); event.stopPropagation(); button.click(); }
  }
  function activate(action: Action | "review", trigger: HTMLButtonElement) {
    if (action === "review") {
      onReviewFile?.({ runID, editID: item.proposal_id, workspaceID: item.workspace_id }, trigger);
    } else if (action === "approve_for_run") {
      setDetailsOpen(false);
      setLimitsOpen(true);
    } else {
      decide(action);
    }
  }
  const details = <div aria-label={detailLabel} className="v2-approval-details-body" ref={detailsPanel} role="region" tabIndex={-1}>
    {compactScope && scope && <p>{scope}</p>}
    <ApprovalFields fields={detailFields} />
    {canDeny && <ApprovalDenialReason disabled={mutation.isPending} onChange={setReason} value={reason}
      label={browserRequest ? t("网页请求的拒绝原因", "Reason for denying the web request") : t(`${item.tool_name} 的拒绝原因`, `Reason for denying ${item.tool_name}`)} />}
  </div>;
  return <article aria-labelledby={filePath ? `${headingID} ${targetID}` : headingID} className="v2-approval-card"
    onKeyDown={handleKey} tabIndex={actionable ? 0 : undefined}>
    <header className="v2-approval-source"><Icon aria-hidden="true" size={16} /><span>{source}{commonShell && ` · ${commonShell}`}</span></header>
    <h3 className="v2-approval-question" id={headingID}>{title}</h3>
    {recovering && <p>{item.status === "approved" ? t("已允许，等待恢复", "Allowed; waiting to resume") : t("已拒绝，等待恢复", "Denied; waiting to resume")}</p>}
    {preview.isLoading && <p role="status">{t("正在读取这次操作的精确预览…", "Loading the exact action preview…")}</p>}
    {preview.isError && <div role="alert"><p>{t("操作预览读取失败，请重新读取后确认批准范围。", "Could not load the action preview. Reload it before confirming the approval scope.")}</p>
      {preview.error instanceof Error && <p>{preview.error.message}</p>}
      <button onClick={() => void preview.refetch()} type="button">{t("重试操作预览", "Retry action preview")}</button></div>}
    {preview.data && <>
      {(filePath || destination) && <div className="v2-approval-target" id={targetID}>
        {filePath && <code>{visibleText(filePath)}</code>}{destination && <><span aria-label={t("移至", "Move to")}> → </span><code>{visibleText(destination)}</code></>}
      </div>}
      {webURL && <div className="v2-approval-target"><code>{visibleText(webURL)}</code></div>}
      {command?.commands.map(({ line, shell, purpose, facts }, index) => <div className="v2-approval-command-entry" key={index}>
        {shell && !commonShell && <span className="v2-approval-source">{shell}</span>}
        <pre className="v2-approval-command" dir="ltr"><code>{visibleText(line)}</code></pre>
        {purpose && command.commands.length > 1 && <p>{visibleText(purpose)}</p>}
        {facts && <ApprovalFields fields={facts} />}
      </div>)}
      {command && command.commands.length > 1 && command.failurePolicy &&
        <p>{t("批量命令失败策略", "Batch failure policy")} <code>{visibleText(command.failurePolicy)}</code></p>}
      {command?.action === "start" && singlePurpose === title && <p>{t("在后台启动进程", "Starts a background process")}</p>}
      {preview.data.working_directory && preview.data.working_directory !== "." &&
        <ApprovalFields fields={[{ name: "working_directory", value: preview.data.working_directory }]} />}
      {computerUse ? <ComputerUsePreview preview={preview.data} /> : <ApprovalFields fields={visibleFields} />}
      {!compactScope && !browserRequest && !(fileReview && onReviewFile) && <p>{scope}</p>}
      {preview.data.redacted && <p>{t("敏感内容已脱敏，请按可见内容核对操作。", "Sensitive content is redacted. Review the action using the visible information.")}</p>}
      {(!preview.data.source_current || preview.data.truncated) && <p role="alert">
        {preview.data.truncated ? t("预览超过显示上限，需读取完整内容后再批准。", "The preview exceeds the display limit. Read the full content before approving.") : t("操作已变化或审批已处理，请刷新最新状态。", "The action changed or was already decided. Refresh its status.")}
        <button onClick={() => void preview.refetch()} type="button">{t("刷新操作预览", "Refresh action preview")}</button></p>}
    </>}
    {recovering && <p>{t("上次决定已经保存。点击“继续恢复”会沿用原决定和授权范围完成后续处理。", "The previous decision is saved. Resume uses the same decision and permission scope to finish processing.")}</p>}
    {hasDetails && (actionable ? detailsOpen && details : <details className="v2-approval-details">
      <summary aria-label={detailLabel} title={detailLabel}><Ellipsis aria-hidden="true" size={16} /></summary>{details}
    </details>)}
    {boundedApproval && limitsOpen && <fieldset aria-label={t("授权限额", "Grant limits")} className="v2-approval-grant"
      disabled={mutation.isPending} ref={limitsPanel} tabIndex={-1}>
      <legend>{t("本次执行的限时、限次审批", "Time and usage limits for this run")}</legend>
      <ApprovalFields fields={grantFields.filter(({ name }) => !existingGrant || (name !== "grant_ttl_seconds" && name !== "grant_max_uses"))} />
      {!grantFields.some(({ name }) => name === "bounded_review") && <p>{t("每条新命令仍需确认。", "Each new command still requires confirmation.")}</p>}
      <label>{t("有效秒数", "Duration in seconds")}<input aria-label={t("有界审批有效秒数", "Bounded approval duration in seconds")} type="number" min={1} max={900} value={Number.isNaN(grantTTL) ? "" : grantTTL} disabled={existingGrant}
        onChange={(event) => setGrantTTL(event.target.valueAsNumber)} /></label>
      <label>{t("命令次数", "Command limit")}<input aria-label={t("有界审批命令次数", "Bounded approval command limit")} type="number" min={1} max={8} value={Number.isNaN(grantUses) ? "" : grantUses} disabled={existingGrant}
        onChange={(event) => setGrantUses(event.target.valueAsNumber)} /></label>
      <button className="secondary" onClick={closeLimits} type="button">{t("取消", "Cancel")}</button>
      <button className="secondary" type="button" disabled={!canApprove || !Number.isInteger(grantTTL) || grantTTL < 1 || grantTTL > 900 || !Number.isInteger(grantUses) || grantUses < 1 || grantUses > 8}
        onClick={() => decide("approve_for_run")}>{t("确认本条命令并计入有界审批", "Confirm command within bounded approval")}</button>
    </fieldset>}
    <footer className="v2-approval-actions">
      {canDeny && <button aria-keyshortcuts="Escape" className="secondary v2-approval-deny" disabled={mutation.isPending} ref={deny}
        onClick={() => decide("deny")} type="button">{t("拒绝", "Deny")}{!mutation.isPending && <kbd aria-hidden="true">Esc</kbd>}</button>}
      {actionable && (primaryAction || hasDetails) && <div className="v2-approval-primary-group">
        {primaryAction && <button aria-keyshortcuts="Enter" className="primary" disabled={primaryDisabled} ref={primary} type="button"
          onClick={(event) => activate(primaryAction, event.currentTarget)}>
          {mutation.isPending && mutation.variables === primaryAction && <LoaderCircle aria-hidden="true" className="spin" size={14} />}
          {primaryLabel}{!primaryDisabled && <kbd aria-hidden="true">⏎</kbd>}
        </button>}
        {(menuOption || hasDetails) && <button aria-controls={menuOpen ? menuID : undefined} aria-expanded={menuOpen} aria-haspopup="menu"
          aria-label={menuLabel} className="v2-approval-menu-trigger" disabled={!hasDetails && !canApprove} ref={menuTrigger} title={menuLabel} type="button"
          onClick={() => { focusLastOption.current = false; setMenuOpen((open) => !open); }} onKeyDown={(event) => {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault(); focusLastOption.current = event.key === "ArrowUp"; setMenuOpen(true);
            }
          }}><ChevronDown aria-hidden="true" size={14} /></button>}
        {menuOpen && <div aria-label={menuLabel} className="v2-approval-menu" id={menuID} ref={menu} role="menu"
          onKeyDown={(event) => {
            if (event.key === "Tab") { closeMenu(); return; }
            if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
            event.preventDefault();
            const options = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
            const current = options.indexOf(document.activeElement as HTMLButtonElement);
            const next = event.key === "Home" ? 0 : event.key === "End" ? options.length - 1
              : (current + (event.key === "ArrowDown" ? 1 : -1) + options.length) % options.length;
            options[next]?.focus();
          }}
          onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget) && event.relatedTarget !== menuTrigger.current) setMenuOpen(false); }}>
          {menuOption && <button disabled={!canApprove} role="menuitem" tabIndex={-1} type="button"
            onClick={(event) => { closeMenu(); activate(menuOption.action, event.currentTarget); }}>{menuOption.label}</button>}
          {hasDetails && <button role="menuitem" tabIndex={-1} type="button" onClick={() => {
            closeMenu(); setLimitsOpen(false); setDetailsOpen((open) => !open);
          }}>{detailsOpen ? t("收起请求详情", "Hide request details") : detailLabel}</button>}
        </div>}
      </div>}
    </footer>
    {notice && <p className="v2-notice" role="status">{notice}</p>}
    {mutation.isError && <p className="v2-inline-error" role="alert">{t("决定未确认，可以重试。", "The decision is unconfirmed. You can retry. ")}{mutation.error instanceof Error ? mutation.error.message : t("审批失败", "Approval failed")}</p>}
  </article>;
}
