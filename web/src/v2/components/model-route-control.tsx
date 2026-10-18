import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronLeft, ChevronRight, CircleAlert,
  LoaderCircle, RotateCcw, Settings } from "lucide-react";
import type { APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import type { AvailableModelRouteCollectionView, AvailableModelRouteView,
  ThreadModelRouteView } from "../../api/types";

export type V2AvailableModelRoute = AvailableModelRouteView;
export type V2ModelRouteCatalog = AvailableModelRouteCollectionView;
export type V2ThreadModelRoute = ThreadModelRouteView;

export type V2PendingModelRoute = { provider: string; model: string };

const knownProviderNames: Record<string, string> = {
  "official-anthropic": "Claude",
  "official-openai": "OpenAI",
  "official-deepseek": "DeepSeek",
  "official-google-gemini": "Gemini",
  "official-xai": "Grok",
  "official-minimax": "MiniMax",
  "official-mimo": "MiMo",
  "official-kimi": "Kimi",
  "official-kimi-coding": "Kimi for Coding",
  "opencode-go": "OpenCode Go",
  "github-copilot": "GitHub Copilot",
};

const modelVariantNames: Record<string, string> = {
  flash: "Flash", pro: "Pro", chat: "Chat", reasoner: "Reasoner", coder: "Coder",
  thinking: "Thinking", instruct: "Instruct", mini: "Mini", nano: "Nano",
  terra: "Terra", sol: "Sol", luna: "Luna", astra: "Astra", codex: "Codex",
};

function modelDisplayName(model: string): string {
  const basename = model.split("/").filter(Boolean).at(-1) || model;
  const variants = (parts: string[]) => parts.map((part) => modelVariantNames[part.toLowerCase()] ?? part).join(" ");
  const deepseek = /^deepseek-([vr]\d+(?:\.\d+)?)((?:-[a-z0-9.]+)*)$/iu.exec(basename);
  if (deepseek) return ["DeepSeek", deepseek[1].toUpperCase(), variants(deepseek[2].split("-").filter(Boolean))].filter(Boolean).join(" ");
  const gpt = /^gpt-(\d+(?:\.\d+)?o?)((?:-[a-z0-9.]+)*)$/iu.exec(basename);
  if (gpt) return [`GPT-${gpt[1]}`, variants(gpt[2].split("-").filter(Boolean))].filter(Boolean).join(" ");
  const claude = /^claude-(sonnet|opus|haiku)-(\d{1,2})(?:-(\d{1,2}))?((?:-[a-z0-9.]+)*)$/iu.exec(basename);
  if (claude) {
    const suffix = claude[4].split("-").filter(Boolean);
    // Only the known Claude snapshot position may omit a valid calendar date.
    // Keep all remaining variant tokens, including unrecognized/custom ones.
    if (/^20\d{6}$/u.test(suffix[0] ?? "")) {
      const stamp = suffix[0];
      const year = Number(stamp.slice(0, 4)), month = Number(stamp.slice(4, 6)), day = Number(stamp.slice(6, 8));
      const date = new Date(Date.UTC(year, month - 1, day));
      if (date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day) suffix.shift();
    }
    const family = claude[1][0].toUpperCase() + claude[1].slice(1).toLowerCase();
    return ["Claude", family, `${claude[2]}${claude[3] ? `.${claude[3]}` : ""}`, variants(suffix)].filter(Boolean).join(" ");
  }
  return basename;
}

function unavailableReason(route: V2AvailableModelRoute, t: ReturnType<typeof useLocale>["t"]): string {
  const reason = route.unavailable_reason.trim();
  const reasons: Record<string, string> = {
    provider_disabled: t("供应商已停用", "Provider disabled"),
    credential_not_configured: t("API Key 尚未配置", "API key not configured"),
    invalid_configuration: t("供应商配置无效，请检查端点和高级 JSON", "Invalid provider configuration. Check the endpoint and advanced JSON."),
    provider_unavailable: t("供应商当前不可用", "Provider currently unavailable"),
    harness_qualification_required: t("Harness 尚未通过能力验证", "Harness capability verification pending"),
    not_configured: t("尚未完成 Harness 能力验证", "Harness capability verification incomplete"),
    protocol_mismatch: t("API 协议与 Harness 不兼容", "API protocol is incompatible with the harness"),
    auth_failed: t("API Key 验证失败", "API key verification failed"),
    network_failed: t("无法连接模型供应商", "Unable to connect to model provider"),
    rate_limit: t("供应商正在限流，请稍后重试", "Provider rate limit reached. Try again later."),
    capacity: t("供应商额度或容量不足，请检查账单或服务状态", "Provider quota or capacity is insufficient. Check billing or service status."),
    model_unsupported: t("供应商不支持该模型", "Provider does not support this model"),
    unavailable: t("Harness 可用性尚未确认", "Harness availability is unconfirmed"),
  };
  if (reason) return reasons[reason] ?? t("当前路由不可用，请检查供应商配置", "Current route unavailable. Check provider configuration.");
  if (!route.enabled) return t("供应商已停用", "Provider disabled");
  if (!["configured", "not_required"].includes(route.credential_status)) return t("API Key 尚未配置", "API key not configured");
  if (!route.harness_ready) return t("Harness 尚未通过能力验证", "Harness capability verification pending");
  if (!["available", "trusted_builtin", "verified"].includes(route.qualification_status)) {
    return reasons[route.qualification_status] ?? t("当前模型尚未完成能力验证", "Model capability verification incomplete");
  }
  return t("当前路由不可用", "Current route unavailable");
}

function focusMenuItem(root: HTMLElement | null, position: "first" | "last") {
  const items = [...(root?.querySelectorAll<HTMLButtonElement>(
    '[role^="menuitem"]:not(:disabled)',
  ) ?? [])];
  const target = position === "first" ? items[0] : items.at(-1);
  target?.focus();
}

function moveMenuFocus(event: ReactKeyboardEvent<HTMLElement>) {
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
  const menu = event.currentTarget;
  const items = [...menu.querySelectorAll<HTMLButtonElement>('[role^="menuitem"]:not(:disabled)')];
  if (!items.length) return;
  event.preventDefault();
  if (event.key === "Home") return items[0]?.focus();
  if (event.key === "End") return items.at(-1)?.focus();
  const current = items.indexOf(document.activeElement as HTMLButtonElement);
  const delta = event.key === "ArrowDown" ? 1 : -1;
  const next = current < 0 ? 0 : (current + delta + items.length) % items.length;
  items[next]?.focus();
}

export function V2ModelRouteControl({ client, threadID, pendingRoute, runActive = false,
  onManageModels, onPendingRouteChange }: {
  client: APIClient;
  threadID: string;
  pendingRoute?: V2PendingModelRoute | null;
  runActive?: boolean;
  onManageModels: (prepareForDraft?: boolean) => void;
  onPendingRouteChange?: (route: V2PendingModelRoute | null) => void;
}) {
  const { t } = useLocale();
  const catalogAvailable = Boolean(client.hasModelControl);
  const routeControlAvailable = catalogAvailable && (threadID
    ? true
    : typeof onPendingRouteChange === "function");
  const queryClient = useQueryClient();
  const [level, setLevel] = useState<"closed" | "settings" | "models">("closed");
  const [nextRunNotice, setNextRunNotice] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const settingsMenuRef = useRef<HTMLDivElement>(null);
  const modelsMenuRef = useRef<HTMLDivElement>(null);
  const pendingFocusOriginRef = useRef<HTMLElement | null>(null);
  const routeKey = ["v2", "thread", threadID, "model-route"] as const;
  const catalogKey = ["v2", "models", "available-routes"] as const;

  const closeMenu = () => {
    // Restore before removing the focused menu item. A later animation frame
    // could steal focus from a composer the operator has already returned to.
    // Async route completion must likewise respect focus outside this control.
    const active = document.activeElement;
    const origin = pendingFocusOriginRef.current;
    // Cache/mutation notification may re-enable the original item before this
    // callback. Its recorded focus ownership survives that DOM state change.
    const pendingMenuLostFocus = active === document.body && origin && rootRef.current?.contains(origin);
    pendingFocusOriginRef.current = null;
    if (rootRef.current?.contains(active) || pendingMenuLostFocus) triggerRef.current?.focus();
    setLevel("closed");
  };

  const routeQuery = useQuery({
    queryKey: routeKey,
    queryFn: ({ signal }) => client.threadModelRoute(threadID, signal),
    enabled: routeControlAvailable && Boolean(threadID),
    staleTime: 15_000,
  });
  const catalogQuery = useQuery({
    queryKey: catalogKey,
    queryFn: ({ signal }) => client.availableModelRoutes(signal),
    enabled: routeControlAvailable && level !== "closed",
    staleTime: 30_000,
  });
  const mutation = useMutation({
    mutationFn: (intent: { action: "select"; route: V2AvailableModelRoute } |
      { action: "reset" }) => client.selectThreadModelRoute(threadID, intent.action === "reset"
      ? { version: "thread_model_route_control.v1", action: "reset",
        operation_key: globalThis.crypto.randomUUID(), requested_by: "desktop-ui" }
      : { version: "thread_model_route_control.v1", action: "select",
        provider: intent.route.provider_id, model: intent.route.model,
        operation_key: globalThis.crypto.randomUUID(), requested_by: "desktop-ui" }),
    onSuccess: (route) => {
      queryClient.setQueryData(routeKey, route);
      setNextRunNotice(runActive && route.applies_to === "next_run");
      closeMenu();
    },
    onError: () => { pendingFocusOriginRef.current = null; },
  });

  useEffect(() => {
    if (!runActive) setNextRunNotice(false);
  }, [runActive]);

  useEffect(() => {
    if (level === "closed") return;
    const outside = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) {
        pendingFocusOriginRef.current = null;
        setLevel("closed");
      }
    };
    const keyboard = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      closeMenu();
    };
    const focusOutside = (event: FocusEvent) => {
      if (event.target !== document.body && !rootRef.current?.contains(event.target as Node)) {
        pendingFocusOriginRef.current = null;
      }
    };
    document.addEventListener("mousedown", outside);
    document.addEventListener("focusin", focusOutside);
    window.addEventListener("keydown", keyboard);
    return () => {
      document.removeEventListener("mousedown", outside);
      document.removeEventListener("focusin", focusOutside);
      window.removeEventListener("keydown", keyboard);
    };
  }, [level]);

  useLayoutEffect(() => {
    // Focus while the newly opened menu is committed, before the operator can
    // move elsewhere. A delayed frame could override newer keyboard/input focus.
    if (level === "settings") focusMenuItem(settingsMenuRef.current, "first");
    if (level === "models") focusMenuItem(modelsMenuRef.current, "first");
  }, [level]);

  const groups = useMemo(() => {
    const grouped = new Map<string, { name: string; routes: V2AvailableModelRoute[] }>();
    for (const route of catalogQuery.data?.routes ?? []) {
      const existing = grouped.get(route.provider_id);
      if (existing) existing.routes.push(route);
      else grouped.set(route.provider_id, { name: route.provider_name || route.provider_id, routes: [route] });
    }
    return [...grouped.entries()].map(([id, value]) => ({ id, ...value }));
  }, [catalogQuery.data?.routes]);
  const hasSelectableRoutes = Boolean(catalogQuery.data?.routes.some((route) => route.selectable));
  const current = threadID ? routeQuery.data : pendingRoute ? {
    protocol_version: "thread_model_route.v1" as const,
    thread_id: "",
    provider: pendingRoute.provider,
    model: pendingRoute.model,
    source: "composer_override",
    applies_to: "next_run" as const,
  } : undefined;
  const label = current?.model || (routeQuery.isLoading ? t("正在读取模型", "Loading model") : t("模型", "Model"));
  const providerLabel = current ? catalogQuery.data?.routes.find((route) =>
    route.provider_id === current.provider)?.provider_name ||
    knownProviderNames[current.provider] || current.provider : "";
  const triggerLabel = modelDisplayName(label);
  const routeDescription = t(`${providerLabel || "默认"} · ${label}；推理强度：随模型`,
    `${providerLabel || "Default"} · ${label}; Reasoning effort: model default`);
  const selectRoute = (route: V2AvailableModelRoute) => {
    if (!threadID) {
      onPendingRouteChange?.({ provider: route.provider_id, model: route.model });
      closeMenu();
      return;
    }
    pendingFocusOriginRef.current = rootRef.current?.contains(document.activeElement)
      ? document.activeElement as HTMLElement : null;
    mutation.mutate({ action: "select", route });
  };
  const resetRoute = () => {
    if (!threadID) {
      onPendingRouteChange?.(null);
      closeMenu();
      return;
    }
    pendingFocusOriginRef.current = rootRef.current?.contains(document.activeElement)
      ? document.activeElement as HTMLElement : null;
    mutation.mutate({ action: "reset" });
  };
  const closeForNavigation = () => {
    pendingFocusOriginRef.current = null;
    setLevel("closed");
    onManageModels(!threadID && catalogQuery.isSuccess && !hasSelectableRoutes);
  };

  return <div className="v2-model-route-control" ref={rootRef}>
    <button aria-expanded={level !== "closed"} aria-haspopup="menu"
      aria-label={t(`模型路由，当前 ${routeDescription}`, `Model route, current ${routeDescription}`)} className="v2-model-route-trigger"
      disabled={!routeControlAvailable} onClick={() => setLevel((currentLevel) =>
        currentLevel === "closed" ? "settings" : "closed")} ref={triggerRef}
      title={routeControlAvailable ? routeDescription : t("当前启动未开放模型路由控制", "Model route control is unavailable in this launch")}
      type="button">
      {(routeQuery.isFetching || mutation.isPending) && <LoaderCircle aria-hidden="true" className="spin" size={14} />}
      <span>{triggerLabel}</span>{nextRunNotice && <em>{t("下一轮", "Next turn")}</em>}
      <ChevronDown aria-hidden="true" size={14} />
    </button>

    {level === "settings" && <div aria-label={t("模型与响应设置", "Model and response settings")}
      className="v2-model-route-popover v2-model-route-settings" onKeyDown={moveMenuFocus}
      ref={settingsMenuRef} role="menu">
      {current && <dl className="v2-model-route-current" aria-label={t("当前模型完整路由", "Full current model route")}>
        <div><dt>{t("供应商", "Provider")}</dt><dd>{providerLabel}{providerLabel !== current.provider ? `（${current.provider}）` : ""}</dd></div>
        <div><dt>{t("模型 ID", "Model ID")}</dt><dd><code>{current.model}</code></dd></div>
      </dl>}
      <button onClick={() => setLevel("models")} onKeyDown={(event) => {
        if (event.key === "ArrowRight") { event.preventDefault(); setLevel("models"); }
      }} role="menuitem" type="button">
        <span>{t("模型", "Model")}</span><strong title={routeDescription}>{triggerLabel}</strong><ChevronRight aria-hidden="true" size={15} />
      </button>
      <button disabled role="menuitem" title={t("当前供应商尚未声明 reasoning_effort", "The current provider has not declared reasoning_effort")} type="button">
        <span>{t("推理强度", "Reasoning effort")}</span><strong>{t("随模型", "Model default")}</strong><ChevronRight aria-hidden="true" size={15} />
      </button>
      <button disabled role="menuitem" title={t("速度由当前模型路由决定", "Speed is determined by the current model route")} type="button">
        <span>{t("速度", "Speed")}</span><strong>{t("自动", "Automatic")}</strong><ChevronRight aria-hidden="true" size={15} />
      </button>
      <i aria-hidden="true" />
      <button disabled={mutation.isPending || !current || current.source === "default"}
        onClick={resetRoute} role="menuitem" type="button">
        <RotateCcw aria-hidden="true" size={15} /><span>{t("重置为默认设置", "Reset to defaults")}</span>
      </button>
      {nextRunNotice && <p className="v2-model-route-notice">{t("当前执行继续使用原模型，下一轮使用所选模型。", "The current execution keeps its model. The selected model will be used in the next turn.")}</p>}
      {mutation.isError && <p className="v2-model-route-error" role="alert">
        <CircleAlert aria-hidden="true" size={14} />{mutation.error instanceof Error
          ? mutation.error.message : t("模型路由更新失败", "Failed to update model route")}</p>}
    </div>}

    {level === "models" && <div aria-label={t("选择模型路由", "Select model route")}
      className="v2-model-route-popover v2-model-route-list" onKeyDown={(event) => {
        if (event.key === "ArrowLeft") {
          event.preventDefault();
          setLevel("settings");
          return;
        }
        moveMenuFocus(event);
      }} ref={modelsMenuRef} role="menu">
      <header><button aria-label={t("返回模型设置", "Back to model settings")} onClick={() => setLevel("settings")}
        role="menuitem" type="button"><ChevronLeft aria-hidden="true" size={16} /></button>
        <strong>{t("模型", "Model")}</strong></header>
      <div className="v2-model-route-scroll">
        {catalogQuery.isLoading && <p className="v2-model-route-empty">
          <LoaderCircle aria-hidden="true" className="spin" size={15} />{t("正在读取可用模型…", "Loading available models…")}</p>}
        {catalogQuery.isError && <p className="v2-model-route-error" role="alert">
          <CircleAlert aria-hidden="true" size={14} />{t("模型列表暂时不可用", "Model list temporarily unavailable")}</p>}
        {!catalogQuery.isLoading && !catalogQuery.isError && groups.length === 0 &&
          <p className="v2-model-route-empty">{t("还没有可用模型。请先配置供应商和 API Key。", "No models available yet. Configure a provider and API key first.")}</p>}
        {groups.map((group) => <section aria-label={group.name} key={group.id}>
          <h3>{group.name}</h3>
          {group.routes.map((route) => {
            const selected = current?.provider === route.provider_id && current.model === route.model;
            const reason = route.selectable ? "" : unavailableReason(route, t);
            return <button aria-checked={selected} disabled={!route.selectable || mutation.isPending}
              key={`${route.provider_id}\u0000${route.model}`}
              onClick={() => selectRoute(route)}
              role="menuitemradio" title={reason || `${group.name} · ${route.model}`} type="button">
              <span><strong>{route.model}</strong>{reason && <small>{reason}</small>}</span>
              {selected && <Check aria-hidden="true" size={16} />}
            </button>;
          })}
        </section>)}
      </div>
      <footer>{mutation.isError && <span className="v2-model-route-error" role="alert">
          <CircleAlert aria-hidden="true" size={14} />{mutation.error instanceof Error
            ? mutation.error.message : t("模型路由更新失败", "Failed to update model route")}</span>}
        <button onClick={closeForNavigation} role="menuitem" type="button">
        <Settings aria-hidden="true" size={15} /><span>{hasSelectableRoutes ? t("管理模型供应商…", "Manage model providers…") : t("添加模型供应商", "Add model provider")}</span>
      </button></footer>
    </div>}
  </div>;
}
