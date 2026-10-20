import { useId, useRef, useState, type MouseEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { Bug, LoaderCircle, RotateCw, ShieldCheck } from "lucide-react";
import {
  desktopCurrentRiskProfile,
  desktopDebugRestartEnabled,
  desktopErrorMessage,
  restartDesktopInDebugMode,
  type DesktopRuntimeRiskProfile,
} from "../../lib/desktop-bridge";
import {
  V2HighRiskActivationDialog,
} from "./high-risk-activation-dialog";

import { useLocale, type LocaleText } from "../../lib/locale";

const sessionCopy = (t: LocaleText): Record<DesktopRuntimeRiskProfile, {
  label: string;
  detail: string;
}> => ({
  safe: {
    label: t("标准运行时", "Standard runtime"),
    detail: t("完全访问可在当前任务中即时确认；持久终端和后台调试运行时未开启。", "Full access can be confirmed for the current task. Persistent terminal and background debug runtimes are not enabled."),
  },
  debug: {
    label: t("调试已开启", "Debug enabled"),
    detail: t("本次应用会话已初始化持久终端、后台进程和限时终端输入运行时。", "Persistent terminals, background processes and time-limited terminal input are initialized for this app session."),
  },
});

function actionState(current: DesktopRuntimeRiskProfile | null,
  restartEnabled: boolean, t: LocaleText): { disabled: boolean; label: string } {
  if (current === null) return { disabled: true, label: t("桌面会话不可用", "Desktop session unavailable") };
  if (current === "debug") return { disabled: true, label: t("调试运行时已开启", "Debug runtime enabled") };
  if (!restartEnabled) return { disabled: true, label: t("重启入口不可用", "Restart unavailable") };
  return { disabled: false, label: t("启用调试模式并重启", "Enable debug mode and restart") };
}

export function V2RuntimeCapabilityControl() {
  const { t } = useLocale();
  const titleID = useId();
  const [open, setOpen] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const returnFocusRef = useRef<HTMLButtonElement>(null);
  const current = desktopCurrentRiskProfile();
  const restartEnabled = desktopDebugRestartEnabled();
  const mutation = useMutation({
    mutationFn: () => restartDesktopInDebugMode(),
    onSuccess: (result) => {
      if (result.status === "cancelled") {
        setOpen(false);
        return;
      }
      setRestarting(true);
    },
  });
  const beginActivation = (event: MouseEvent<HTMLButtonElement>) => {
    returnFocusRef.current = event.currentTarget;
    mutation.reset();
    setRestarting(false);
    setOpen(true);
  };
  const close = () => {
    if (mutation.isPending || restarting) return;
    mutation.reset();
    setOpen(false);
  };
  const phase = restarting ? "restarting" : mutation.isPending ? "confirming" : "idle";
  const status = current ? sessionCopy(t)[current] : {
    label: t("无法读取", "Unavailable"),
    detail: t("当前页面没有可验证的桌面运行时能力信息。", "Verified desktop runtime capability information is unavailable on this page."),
  };

  return <section aria-labelledby={titleID}
    className="v2-settings-card v2-runtime-capability-card">
    <header>
      <div><span className="v2-runtime-icon"><ShieldCheck aria-hidden="true" size={18} /></span>
        <div><h2 id={titleID}>{t("调试运行时", "Debug runtime")}</h2><p>{t("持久终端与后台进程", "Persistent terminals and background processes")}</p></div></div>
      <span className={`v2-runtime-status is-${current ?? "unavailable"}`}>{status.label}</span>
    </header>
    <div className="v2-runtime-capability-body">
      <p className="v2-runtime-session-detail">{status.detail}</p>
      <div aria-label={t("调试运行时能力", "Debug runtime capabilities")} className="v2-runtime-actions" role="group">
        {(() => {
          const action = actionState(current, restartEnabled, t);
          return <article>
            <span><Bug aria-hidden="true" size={18} /></span>
            <div><strong>{t("调试模式", "Debug mode")}</strong><p>
              {t("提供持久终端、后台进程和限时终端输入支持；实际使用还需在当前任务选择本地执行、Debug 交互并确认完全访问。", "Supports persistent terminals, background processes and time-limited terminal input. Select local execution and Debug interaction and confirm full access for the task before use.")}</p></div>
            <button aria-label={action.label} disabled={action.disabled || mutation.isPending || restarting}
              onClick={beginActivation} type="button">
              {mutation.isPending
                ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
                : !action.disabled ? <RotateCw aria-hidden="true" size={14} /> : null}
              {action.label}
            </button>
          </article>;
        })()}
      </div>
      <p className="v2-runtime-boundary">
        {t("开启调试模式会重启应用，准备持久终端和后台进程。重启后任务权限仍保留，请在当前任务重新确认完全访问。\r\n        普通启动使用标准运行时；在当前任务开启完全访问时，先暂停执行并等待资源释放完成即可。", "Enabling debug mode restarts the app to prepare persistent terminals and background processes. Task permissions remain; reconfirm full access after restart. Normal startup uses the standard runtime. Pause task execution and wait for resources to be released before enabling full access.")}</p>
    </div>
    <V2HighRiskActivationDialog error={mutation.isError
      ? desktopErrorMessage(mutation.error) : undefined}
      onCancel={close} onConfirm={() => mutation.mutate()}
      open={open} phase={phase} profile="debug"
      returnFocusRef={returnFocusRef} />
  </section>;
}
