import { useState, type CSSProperties } from "react";
import { AlertCircle, Bot, CheckCircle2, ChevronRight, Hand, LoaderCircle, MousePointer2, OctagonX } from "lucide-react";
import { useLocale } from "../../lib/locale";
import type { BrowserCursorPoint, BrowserOperationStep } from "../use-auto-browser-dock";
import "./browser-collab-bar.css";

export function V2BrowserStepTrail({ steps }: { steps: readonly BrowserOperationStep[] }) {
  const { t } = useLocale();
  if (!steps.length) return null;

  return (
    <div className="v2-browser-collab-steps-strip" aria-label={t("浏览器操作步骤", "Browser operation steps")}>
      <ol className="v2-browser-step-trail" role="list">
        {steps.map((step, idx) => (
          <li className="v2-browser-step-item" data-step-status={step.status} key={step.id}>
            <span className="v2-browser-step-pill">
              {step.status === "running" ? (
                <LoaderCircle aria-hidden="true" className="v2-browser-step-spinner" size={13} />
              ) : step.status === "completed" ? (
                <CheckCircle2 aria-hidden="true" className="v2-browser-step-check" size={13} />
              ) : (
                <AlertCircle aria-hidden="true" size={13} />
              )}
              <span>{t(`步骤 ${step.index}: ${step.label}`, `Step ${step.index}: ${step.label}`)}</span>
            </span>
            {idx < steps.length - 1 && (
              <ChevronRight aria-hidden="true" className="v2-browser-step-arrow" size={13} />
            )}
          </li>
        ))}
      </ol>
    </div>
  );
}

export function V2BrowserCursorHud({
  cursor,
  manualTakeover = false,
}: {
  cursor: BrowserCursorPoint | null;
  manualTakeover?: boolean;
}) {
  const { t } = useLocale();
  if (!cursor || manualTakeover) return null;

  const actionLabel =
    cursor.actionKind === "click"
      ? t("点击", "Click")
      : cursor.actionKind === "type"
        ? t("填写", "Type")
        : cursor.actionKind === "navigate"
          ? t("访问", "Open")
          : cursor.actionKind === "scroll"
            ? t("滚动", "Scroll")
            : t("移动", "Move");

  return (
    <div
      aria-label={t("智能体实时光标", "Agent live cursor")}
      className="v2-browser-cursor-hud"
      data-action-kind={cursor.actionKind}
      data-testid="v2-browser-cursor-hud"
    >
      <div className="v2-browser-cursor-stage-anchor">
        <div
          className="v2-browser-cursor-node"
          style={{
            "--v2-cursor-pct-x": `${cursor.percentX.toFixed(1)}%`,
            "--v2-cursor-pct-y": `${cursor.percentY.toFixed(1)}%`,
          } as CSSProperties}
        >
          {cursor.ripple && (
            <span
              aria-hidden="true"
              className="v2-browser-cursor-ripple"
              data-testid="v2-browser-cursor-ripple"
              key={cursor.sequenceKey}
            />
          )}
          <MousePointer2 aria-hidden="true" className="v2-browser-cursor-arrow" size={18} />
          <span className="v2-browser-cursor-badge">
            <span>{cursor.targetLabel ? `${actionLabel} · ${cursor.targetLabel}` : actionLabel}</span>
            <span className="v2-browser-cursor-coords">({cursor.x}, {cursor.y})</span>
          </span>
        </div>
      </div>
    </div>
  );
}

export interface V2BrowserCollabBarProps {
  agentOperating: boolean;
  manualTakeover: boolean;
  steps?: readonly BrowserOperationStep[];
  onStopAndTakeOver: () => Promise<void> | void;
  onResumeAgent?: () => void;
}

export function V2BrowserCollabBar({
  agentOperating,
  manualTakeover,
  steps = [],
  onStopAndTakeOver,
  onResumeAgent,
}: V2BrowserCollabBarProps) {
  const { t } = useLocale();
  const [stopping, setStopping] = useState(false);

  const activeStep = steps.find((step) => step.status === "running") ?? steps.at(-1);
  const mode = manualTakeover ? "takeover" : agentOperating ? "agent" : "idle";

  const handleStopClick = async () => {
    if (stopping) return;
    setStopping(true);
    try {
      await onStopAndTakeOver();
    } finally {
      setStopping(false);
    }
  };

  return (
    <footer
      aria-label={t("浏览器协同控制栏", "Browser collaboration controls")}
      className="v2-browser-collab-bar"
      data-mode={mode}
    >
      <div className="v2-browser-collab-status" role="status" aria-live="polite">
        <span aria-hidden="true" className="v2-browser-collab-dot" />
        {manualTakeover ? (
          <>
            <Hand aria-hidden="true" size={15} />
            <span>{t("已由人工接管 · 您可直接操作页面", "Manual Takeover Active · You can interact with the page directly")}</span>
          </>
        ) : agentOperating ? (
          <>
            <Bot aria-hidden="true" size={15} />
            <span>
              {activeStep
                ? t(
                    `智能体正在操作网页 · 步骤 ${activeStep.index}: ${activeStep.label}`,
                    `Agent operating browser · Step ${activeStep.index}: ${activeStep.label}`,
                  )
                : t("智能体正在操作网页 · 可随时停止并接管", "Agent operating browser · Stop & take over anytime")}
            </span>
          </>
        ) : (
          <>
            <CheckCircle2 aria-hidden="true" size={15} />
            <span>{t("浏览器协同就绪 · 您可直接操作页面", "Browser collaboration ready · You can interact directly")}</span>
          </>
        )}
      </div>

      <div className="v2-browser-collab-actions">
        {manualTakeover ? (
          onResumeAgent && (
            <button
              className="v2-browser-collab-resume-btn"
              onClick={onResumeAgent}
              type="button"
            >
              <Bot aria-hidden="true" size={14} />
              <span>{t("交还智能体继续", "Resume with Agent")}</span>
            </button>
          )
        ) : (
          <button
            className="v2-browser-collab-stop-btn"
            disabled={stopping}
            onClick={() => void handleStopClick()}
            type="button"
          >
            {stopping ? (
              <LoaderCircle aria-hidden="true" className="v2-browser-step-spinner" size={14} />
            ) : (
              <OctagonX aria-hidden="true" size={14} />
            )}
            <span>
              {stopping
                ? t("正在停止并接管…", "Stopping & taking over…")
                : t("停止并人工接管", "Stop & Take Over")}
            </span>
          </button>
        )}
      </div>
    </footer>
  );
}
