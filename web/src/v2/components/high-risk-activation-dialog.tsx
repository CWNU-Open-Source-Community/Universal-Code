import { useId, useRef, type FormEvent, type RefObject } from "react";
import { createPortal } from "react-dom";
import {
  AlertTriangle,
  Bug,
  FolderOpen,
  Globe2,
  ShieldOff,
  SquareTerminal,
} from "lucide-react";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale, type LocaleText } from "../../lib/locale";

export type V2HighRiskProfile = "full_access" | "debug";
export type V2HighRiskActivationPhase = "idle" | "applying" | "confirming" | "restarting";

interface CapabilityCopy {
  title: string;
  description: string;
  icon: typeof FolderOpen;
}

interface ProfileCopy {
  title: string;
  introduction: string;
  capabilities: CapabilityCopy[];
  risk: string;
  boundary: string;
  details: string;
  confirmLabel: string;
}

const profileCopy = (t: LocaleText): Record<V2HighRiskProfile, ProfileCopy> => ({
  full_access: {
    title: t("要开启完全访问权限吗？", "Enable full access?"),
    introduction: t("当前任务将能够在当前系统用户权限范围内，无需逐次批准地运行命令、使用互联网、控制浏览器，并在此设备的任意位置创建和编辑文件。包括但不限于：", "This task will be able to run commands, use the internet, control browsers, and create or edit files anywhere on this device with your system user's permissions, without approval for each action. This includes:"),
    capabilities: [
      {
        title: t("文件和文件夹", "Files and folders"),
        description: t("读取、创建、修改、上传或删除此设备上任意位置的文件", "Read, create, edit, upload or delete files anywhere on this device"),
        icon: FolderOpen,
      },
      {
        title: t("终端命令", "Terminal commands"),
        description: t("运行未沙箱化的按次命令、安装软件和更改系统设置", "Run unsandboxed commands, install software and change system settings"),
        icon: SquareTerminal,
      },
      {
        title: t("互联网、浏览器和已连接的应用", "Internet, browsers and connected apps"),
        description: t("访问网站、发送数据并使用已启用的集成；完整 CDP 默认开启，可单独关闭", "Visit websites, send data and use enabled integrations. Full CDP is on by default and can be disabled separately"),
        icon: Globe2,
      },
    ],
    risk: t("这可能导致敏感数据丢失或泄露，也可能放大提示注入带来的风险。", "This may cause sensitive data loss or disclosure and increase the risks of prompt injection."),
    boundary: t("启用范围为当前任务，应用保持运行。请先暂停执行并等待资源释放完成，再确认启用。降低权限后会立即阻断新调用，既有进程在安全边界终止。", "This applies to the current task while the app stays open. Pause execution and wait for resources to be released before enabling it. Reducing permissions blocks new calls immediately and stops existing processes at a safe boundary."),
    details: t("完全访问包含宿主文件、网络和未沙箱化的按次命令。完整 CDP 随完全访问默认开启，可在权限页单独关闭或经风险确认后重新开启。持久终端、后台进程和限时终端输入需另外启用调试运行时。", "Full access includes host files, networking and unsandboxed commands. Full CDP is enabled by default; disable it on the permissions page or confirm the risk to re-enable it. Persistent terminals, background processes and time-limited terminal input also require the debug runtime."),
    confirmLabel: t("启用完全访问", "Enable full access"),
  },
  debug: {
    title: t("要开启调试模式吗？", "Enable debug mode?"),
    introduction: t("调试运行时为持久终端和后台进程提供支持。使用这些能力还需在当前任务选择本地执行、Debug 交互，并确认完全访问权限。仅应在你信任当前工作区及其内容时使用。包括：", "The debug runtime supports persistent terminals and background processes. To use them, select local execution and Debug interaction for this task and confirm full access. Use this only when you trust the workspace and its contents. This includes:"),
    capabilities: [
      {
        title: t("独立的任务权限", "Permissions for each task"),
        description: t("完全访问仍需逐个任务明确确认；网络范围、浏览器与其他集成保留各自的检查", "Full access still requires confirmation for each task. Network scope, browsers and integrations retain their own checks"),
        icon: ShieldOff,
      },
      {
        title: t("持久终端和后台进程", "Persistent terminals and background processes"),
        description: t("在对话执行期间保留终端会话和后台进程", "Keep terminal sessions and background processes during conversation execution"),
        icon: SquareTerminal,
      },
      {
        title: t("终端输入与调试控制", "Terminal input and debug controls"),
        description: t("在单独的限时授权内向持久终端输入并执行调试操作", "Send input to persistent terminals and debug within a separately authorized time window"),
        icon: Bug,
      },
    ],
    risk: t("终端和后台进程可能在界面操作结束后继续运行，凭证、环境变量和调试数据也可能暴露。", "Terminals and background processes may continue after UI actions finish. Credentials, environment variables and debug data may be exposed."),
    boundary: t("确认后，系统会显示桌面警告并重启同一个应用，准备持久终端、后台进程和限时终端输入。任务已保存的权限会保留；重启后请在当前任务重新确认完全访问，并选择本地执行与 Debug 交互。", "After confirmation, a desktop warning appears and this app restarts to prepare persistent terminals, background processes and time-limited terminal input. Saved task permissions remain. After restart, reconfirm full access and select local execution with Debug interaction for this task."),
    details: t("调试运行时使用当前系统用户权限。限时终端输入需要单独授权，批量验证和界面取证按各自能力配置启用。完全访问可在当前执行暂停且资源释放完成后直接开启；降低权限会立即阻断新调用。", "The debug runtime uses your system user's permissions. Time-limited terminal input requires separate authorization. Batch verification and UI evidence depend on their capability settings. Enable full access after pausing execution and releasing resources; reducing permissions blocks new calls immediately."),
    confirmLabel: t("启用并重启", "Enable and restart"),
  },
});

export function V2HighRiskActivationDialog({ open, profile, phase = "idle", error,
  returnFocusRef, onCancel, onConfirm }: {
  open: boolean;
  profile: V2HighRiskProfile | null;
  phase?: V2HighRiskActivationPhase;
  error?: string;
  returnFocusRef?: RefObject<HTMLElement | null>;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useLocale();
  const titleID = useId();
  const descriptionID = useId();
  const boundaryID = useId();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const busy = phase !== "idle";
  const dialogRef = useModalFocusTrap<HTMLElement>(open, onCancel, busy, cancelRef, {
    isolateBackground: true,
    returnFocusRef,
  });
  if (!open || !profile) return null;

  const copy = profileCopy(t)[profile];
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    // Portal events still bubble through the React owner tree.
    event.stopPropagation();
    if (!busy) onConfirm();
  };
  const dialog = <div className="v2-overlay v2-high-risk-overlay" role="presentation"
    onMouseDown={(event) => {
      if (event.target === event.currentTarget && !busy) onCancel();
    }}>
    <section aria-busy={busy} aria-describedby={`${descriptionID} ${boundaryID}`}
      aria-labelledby={titleID} aria-modal="true" className="v2-high-risk-dialog"
      ref={dialogRef} role="dialog" tabIndex={-1}>
      <form onSubmit={submit}>
        <div className="v2-high-risk-content">
          <header className="v2-high-risk-heading">
            <AlertTriangle aria-hidden="true" size={22} />
            <h2 id={titleID}>{copy.title}</h2>
          </header>
          <p className="v2-high-risk-introduction" id={descriptionID}>{copy.introduction}</p>
          <div className="v2-high-risk-capabilities">
            {copy.capabilities.map(({ title, description, icon: Icon }) =>
              <div className="v2-high-risk-capability" key={title}>
                <Icon aria-hidden="true" size={25} />
                <div><strong>{title}</strong><span>{description}</span></div>
              </div>)}
          </div>
          <div className="v2-high-risk-scope" role="note">
            <strong><AlertTriangle aria-hidden="true" size={15} />{t("影响范围", "Scope")}</strong>
            <p id={boundaryID}>{copy.boundary}</p>
          </div>
          <div className="v2-high-risk-warning">
            <p>{copy.risk}</p>
            <details><summary>{t("了解运行边界", "Execution details")}</summary>
              <p>{copy.details}</p>
            </details>
          </div>
          {error && <p className="v2-high-risk-error" role="alert">{error}</p>}
        </div>
        <footer className="v2-high-risk-actions">
          {busy && <span aria-live="polite" role="status">
            {phase === "restarting" ? t("正在重启…", "Restarting…") : phase === "applying"
              ? t("正在应用…", "Applying…") : t("等待系统确认…", "Waiting for system confirmation…")}
          </span>}
          <div>
            <button disabled={busy} onClick={onCancel} ref={cancelRef} type="button">{t("取消", "Cancel")}</button>
            <button className="danger" disabled={busy} type="submit">
              <AlertTriangle aria-hidden="true" size={15} />
              {phase === "restarting" ? t("正在重启…", "Restarting…") : phase === "confirming"
                ? t("等待确认…", "Waiting for confirmation…") : phase === "applying" ? t("正在应用…", "Applying…") : copy.confirmLabel}
            </button>
          </div>
        </footer>
      </form>
    </section>
  </div>;

  return createPortal(dialog, document.body);
}
