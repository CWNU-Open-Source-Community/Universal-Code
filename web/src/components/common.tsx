import { localUIMessage } from "../lib/local-ui-message";
import type { ReactNode } from "react";
import { ChevronDown, LoaderCircle } from "lucide-react";
import { useLocale } from "../lib/locale";

const chineseStatuses: Record<string, string> = {
  created: "已创建", preparing: "准备中", running: "运行中", paused: "已暂停",
  completed: "已完成", failed: "失败", cancelled: "已取消", pending: "待处理",
  prepared: "已准备", committed: "已提交", approved: "已批准", denied: "已拒绝",
  proposed: "待审阅", applied: "已应用", active: "活动", blocked: "受阻",
  waiting: "等待中", "waiting-approval": "等待审批", passed: "通过",
  success: "通过", failure: "失败", neutral: "无结论", "action-required": "需要处理",
  partial: "部分完成", stale: "已过期", "not-run": "未运行",
  interrupted: "已中断", "timed-out": "已超时",
  unknown: "未知", available: "可用", unavailable: "不可用", qualified: "已验证",
  configured: "已配置", "not-configured": "未配置",
  "qualification-required": "需要验证", "trusted-builtin": "内置可信",
  incompatible: "不兼容", reachable: "可连接", unreachable: "不可连接",
  "metadata-only": "仅元数据", truncated: "已截断", current: "当前",
  redacted: "已脱敏", "read-only": "只读", added: "新增", modified: "修改",
  deleted: "删除", renamed: "重命名", regular: "普通文件", executable: "可执行文件",
  untracked: "未跟踪", unmodified: "无变化",
  idle: "空闲", queued: "已排队", starting: "启动中", started: "已开始",
  stopping: "正在停止", "in-progress": "进行中", killed: "已终止", skipped: "已跳过",
  selected: "已选定", none: "无", observed: "已观察", unobserved: "未观察", conflict: "有冲突",
  fetched: "已读取", create: "创建", modify: "修改", replace: "替换", delete: "删除", move: "移动",
  plan: "规划", deliver: "交付", "metadata-confirmed": "元数据已确认", "metadata-disputed": "元数据有争议",
};

function normalizedStatus(status: string): string {
  const normalized = status.toLowerCase().replaceAll("_", "-");
  if (normalized === "success") return "passed";
  if (normalized === "failure") return "failed";
  // Review decisions are stored as verbs; the badge represents their outcome.
  return normalized === "approve" ? "approved" : normalized === "deny" ? "denied" : normalized;
}

export function StatusLabel({ status }: { status: string }) {
  const { t } = useLocale();
  const normalized = normalizedStatus(status);
  const display = status === "approve" || status === "deny" ? normalized : status.replaceAll("_", " ");
  return <>{t(chineseStatuses[normalized] ?? display, display)}</>;
}

export function StatusBadge({ status, label }: { status: string; label?: string }) {
  const normalized = normalizedStatus(status);
  return <span className={`status-badge status-${normalized}`}>
    {label ?? <StatusLabel status={status} />}
  </span>;
}

export function LoadingState({ label }: { label?: string }) {
  const { t } = useLocale();
  return (
    <div className="state-message" role="status">
      <LoaderCircle aria-hidden="true" className="spin" size={18} />
      <span>{label ?? t("加载中", "Loading")}</span>
    </div>
  );
}

export function ErrorState({ error }: { error: unknown }) {
  const { t } = useLocale();
  const message = error instanceof Error ? localUIMessage(error.message, t) : t("请求失败", "Request failed");
  return <div className="state-message state-error" role="alert">{message}</div>;
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="empty-state">{children}</div>;
}

export function LoadMoreButton({
  hasNextPage,
  isFetching,
  onClick,
}: {
  hasNextPage: boolean;
  isFetching: boolean;
  onClick: () => void;
}) {
  const { t } = useLocale();
  if (!hasNextPage) {
    return null;
  }
  return (
    <button className="load-more" disabled={isFetching} onClick={onClick} type="button">
      {isFetching ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> : <ChevronDown aria-hidden="true" size={15} />}
      {t("加载更多", "Load more")}
    </button>
  );
}

export function KeyValue({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="key-value">
      <dt>{label}</dt>
      <dd>{value || "-"}</dd>
    </div>
  );
}
