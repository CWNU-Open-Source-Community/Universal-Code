import { useCallback, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { ArrowUpRight, CalendarClock, Sparkles, Wand2, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type {
  PageResult,
  ThreadDetailView,
  ThreadTranscriptItemView,
  ThreadView,
} from "../../api/types";
import { useLocale } from "../../lib/locale";
import type { AutomationDraftPrefill } from "../automation-nl-schedule";
import {
  classifyExtensionCatalog,
  contextualIntentDetected,
  dismissRecommendation,
  evaluateProactiveToolRecommendations,
  PLUGIN_TOOL_ROLES,
  type SpecificPluginToolRole,
} from "../plugin-recommendations";
import { v2QueryKeys } from "../query-keys";
import "./plugin-recommendation-banner.css";

type CachedThreadSnapshot = {
  detail: ThreadDetailView | undefined;
  transcript: InfiniteData<PageResult<ThreadTranscriptItemView>, string> | undefined;
};

export function V2PluginRecommendationBanner({
  client,
  threadID,
  thread,
  draft = "",
  recentMessagesOverride,
  failureHintsOverride,
  onConfigureRole,
  onApplyDraftHint,
  onOpenAutomations,
}: {
  client: APIClient;
  threadID: string;
  thread?: Pick<ThreadView, "id" | "title" | "workspace_id" | "active_run_id" | "last_run_id">;
  draft?: string;
  recentMessagesOverride?: string[];
  failureHintsOverride?: string[];
  onConfigureRole: (role: SpecificPluginToolRole) => void;
  onApplyDraftHint?: (hint: string) => void;
  onOpenAutomations?: (prefill?: AutomationDraftPrefill) => void;
}) {
  const { locale, t } = useLocale();
  const isZh = locale.startsWith("zh");
  const queryClient = useQueryClient();
  const [dismissRevision, setDismissRevision] = useState(0);

  const snapshotRef = useRef<CachedThreadSnapshot>({
    detail: undefined,
    transcript: undefined,
  });

  const subscribe = useCallback(
    (onStoreChange: () => void) =>
      queryClient.getQueryCache().subscribe((event) => {
        if (event.type !== "updated" && event.type !== "removed") return;
        const key = event.query?.queryKey;
        if (!Array.isArray(key) || key[0] !== "v2" || key[1] !== "thread") return;
        queueMicrotask(onStoreChange);
      }),
    [queryClient],
  );

  const getSnapshot = useCallback((): CachedThreadSnapshot => {
    if (!threadID) {
      if (snapshotRef.current.detail || snapshotRef.current.transcript) {
        snapshotRef.current = { detail: undefined, transcript: undefined };
      }
      return snapshotRef.current;
    }
    const detail = queryClient.getQueryData<ThreadDetailView>(v2QueryKeys.thread(threadID));
    const transcript = queryClient.getQueryData<
      InfiniteData<PageResult<ThreadTranscriptItemView>, string>
    >(v2QueryKeys.transcript(threadID));
    if (
      snapshotRef.current.detail !== detail ||
      snapshotRef.current.transcript !== transcript
    ) {
      snapshotRef.current = { detail, transcript };
    }
    return snapshotRef.current;
  }, [queryClient, threadID]);

  const cached = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

  const extractedContext = useMemo(() => {
    const detail = cached.detail;
    const threadTitle = detail?.thread?.title || thread?.title || "";
    const missionGoal = detail?.mission?.goal || "";

    const transcriptItems =
      cached.transcript?.pages?.flatMap((page) => page.items ?? []) ?? [];
    const recentMessages =
      recentMessagesOverride ??
      transcriptItems
        .slice(-8)
        .map((item) => [item.title, item.detail].filter(Boolean).join(" "))
        .filter(Boolean);

    const failureHints =
      failureHintsOverride ??
      [
        detail?.recovery ? `${detail.recovery.error_code} ${detail.recovery.detail}` : "",
        ...transcriptItems
          .slice(-8)
          .flatMap((item) => [
            item.activity_summary?.status === "failed"
              ? `${item.tool_name ?? ""} ${item.activity_summary.command}`
              : "",
          ]),
      ].filter(Boolean);

    return {
      threadTitle,
      missionGoal,
      recentMessages,
      failureHints,
    };
  }, [cached.detail, cached.transcript, thread?.title, recentMessagesOverride, failureHintsOverride]);

  const hasIntent = useMemo(
    () =>
      Boolean(threadID) &&
      contextualIntentDetected({
        ...extractedContext,
        draft,
      }),
    [threadID, extractedContext, draft],
  );

  const activeRunID =
    cached.detail?.active_run?.id ||
    cached.detail?.last_run?.id ||
    thread?.active_run_id ||
    thread?.last_run_id ||
    "";
  const effectiveWorkspaceID =
    cached.detail?.thread?.workspace_id || thread?.workspace_id || "";

  const extensionsQuery = useQuery({
    queryKey: ["extensions", activeRunID, effectiveWorkspaceID],
    enabled: hasIntent && typeof client.extensionInventory === "function",
    queryFn: ({ signal }) =>
      effectiveWorkspaceID
        ? client.extensionInventory(activeRunID, signal, effectiveWorkspaceID)
        : client.extensionInventory(activeRunID, signal),
    staleTime: 30_000,
  });

  const skillsQuery = useQuery({
    queryKey: ["skill-package-registry", false],
    enabled:
      hasIntent &&
      Boolean(client.hasSkillInstallation) &&
      typeof client.listSkillPackages === "function",
    queryFn: ({ signal }) => client.listSkillPackages({ includeRemoved: false }, signal),
    staleTime: 30_000,
  });

  const catalog = useMemo(
    () =>
      classifyExtensionCatalog({
        mcpServers: extensionsQuery.data?.mcp_servers,
        plugins: extensionsQuery.data?.plugins,
        skills: skillsQuery.data?.items,
      }),
    [extensionsQuery.data, skillsQuery.data],
  );

  const recommendations = useMemo(() => {
    if (!threadID || !hasIntent) return [];
    // Wait until extensionInventory finishes if the client provides it, to avoid flashing a recommendation for an already-enabled tool
    if (typeof client.extensionInventory === "function" && extensionsQuery.isLoading) {
      return [];
    }
    return evaluateProactiveToolRecommendations({
      threadID,
      ...extractedContext,
      draft,
      catalog,
    });
  }, [
    threadID,
    hasIntent,
    client.extensionInventory,
    extensionsQuery.isLoading,
    extractedContext,
    draft,
    catalog,
    dismissRevision,
  ]);

  const topRecommendation = recommendations[0];
  if (!topRecommendation) return null;

  const roleDef = PLUGIN_TOOL_ROLES.find((item) => item.id === topRecommendation.role);
  const roleLabel = roleDef ? (isZh ? roleDef.label.zh : roleDef.label.en) : topRecommendation.role;

  const handleDismiss = () => {
    dismissRecommendation(threadID, topRecommendation.id);
    setDismissRevision((rev) => rev + 1);
  };

  const handleApply = () => {
    if (topRecommendation.actionKind === "automation" && onOpenAutomations) {
      dismissRecommendation(threadID, topRecommendation.id);
      setDismissRevision((rev) => rev + 1);
      onOpenAutomations({
        title: extractedContext.threadTitle || undefined,
        prompt:
          draft.trim() ||
          extractedContext.missionGoal ||
          extractedContext.recentMessages.at(-1) ||
          (isZh ? topRecommendation.draftHint.zh : topRecommendation.draftHint.en),
        scheduleExpression: topRecommendation.suggestedSchedule,
      });
      return;
    }
    if (onApplyDraftHint) {
      onApplyDraftHint(isZh ? topRecommendation.draftHint.zh : topRecommendation.draftHint.en);
    }
    dismissRecommendation(threadID, topRecommendation.id);
    setDismissRevision((rev) => rev + 1);
  };

  return (
    <aside
      aria-label={t("智能体工具推荐", "Agent tool recommendation")}
      className="v2-plugin-rec-banner"
    >
      <div className="v2-plugin-rec-content">
        <span aria-hidden="true" className="v2-plugin-rec-icon">
          <Sparkles size={15} />
        </span>
        <div className="v2-plugin-rec-body">
          <div className="v2-plugin-rec-head">
            <span className="v2-plugin-rec-eyebrow">
              {t("智能体工具推荐", "Agent Tool Recommendation")}
            </span>
            <span className="v2-plugin-rec-role-badge">{roleLabel}</span>
          </div>
          <strong className="v2-plugin-rec-title">
            {isZh ? topRecommendation.title.zh : topRecommendation.title.en}
          </strong>
          <p className="v2-plugin-rec-reason">
            {isZh ? topRecommendation.reason.zh : topRecommendation.reason.en}
          </p>
        </div>
      </div>

      <div className="v2-plugin-rec-actions">
        {topRecommendation.actionKind === "automation" ? (
          <>
            <button
              className="v2-plugin-rec-btn is-primary"
              onClick={handleApply}
              type="button"
            >
              <CalendarClock aria-hidden="true" size={13} />
              {t("立即套用", "Apply")}
            </button>
            <button
              className="v2-plugin-rec-btn"
              onClick={() => onConfigureRole(topRecommendation.role)}
              type="button"
            >
              <ArrowUpRight aria-hidden="true" size={13} />
              {t("前往启用插件", "Configure in Plugins")}
            </button>
          </>
        ) : (
          <>
            <button
              className="v2-plugin-rec-btn is-primary"
              onClick={() => onConfigureRole(topRecommendation.role)}
              type="button"
            >
              <ArrowUpRight aria-hidden="true" size={13} />
              {t("前往启用插件", "Configure in Plugins")}
            </button>
            {onApplyDraftHint && (
              <button className="v2-plugin-rec-btn" onClick={handleApply} type="button">
                <Wand2 aria-hidden="true" size={13} />
                {t("立即套用", "Apply")}
              </button>
            )}
          </>
        )}
        <button
          aria-label={t("忽略此工具推荐", "Dismiss tool recommendation")}
          className="v2-plugin-rec-dismiss"
          onClick={handleDismiss}
          type="button"
        >
          <X aria-hidden="true" size={14} />
        </button>
      </div>
    </aside>
  );
}
