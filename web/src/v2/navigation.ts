import { useCallback, useEffect, useRef, useState } from "react";
import type { V2SettingsSection } from "./components/sidebar";
import { useFileEditLeave } from "../components/file-edit-drafts";

export type V2RunPane = "context" | "checkpoints" | "tools" | "ui-evidence";
export type V2Route = { kind: "initial" | "new" | "thread" | "history" | "run" | "session" | "automations" | "invalid";
  threadID?: string; messageID?: string; section?: V2SettingsSection;
  panel?: "records"; resourceID?: string; pane?: V2RunPane };
const sections: string[] = ["general", "models", "permissions", "appearance", "records", "archived",
  "extensions", "skills", "advanced-models", "about", "shortcuts", "connections", "task-configuration", "environment", "configuration", "usage"];

// Fragments work with both the loopback UI and the existing Desktop asset
// server. History contains navigation identity only, never tokens or drafts.
export function readV2Route(hash: string, pathname = "/"): V2Route {
  if (/^\/legacy(?:\/|$)/u.test(pathname)) return { kind: "invalid" };
  if (!hash || hash === "#") return { kind: "initial" };
  const settings = /\/settings\/([^/]+)$/u.exec(hash);
  const path = settings ? hash.slice(0, settings.index) : hash;
  const section = (settings?.[1] === "plugins" ? "extensions" : settings?.[1] === "keyboard" ? "shortcuts"
    : settings?.[1]) as V2SettingsSection | undefined;
  if (section && !sections.includes(section)) return { kind: "invalid" };
  const parts = path.split("/");
  if (parts.shift() !== "#") return { kind: "invalid" };
  let threadID: string | undefined;
  try {
    if (parts[0] === "threads") {
      threadID = decodeURIComponent(parts[1] ?? "");
      if (!/^[a-zA-Z0-9_.-]{1,256}$/u.test(threadID)) return { kind: "invalid" };
      parts.splice(0, 2);
    }
    let route: V2Route;
    if (threadID && !parts.length) route = { kind: "thread", threadID };
    else if (threadID && parts[0] === "records" && parts.length === 1)
      route = { kind: "thread", threadID, panel: "records" };
    else if (threadID && parts[0] === "messages" && parts.length === 2) {
      const messageID = decodeURIComponent(parts[1]);
      if (!/^[a-zA-Z0-9_.-]{1,256}$/u.test(messageID)) return { kind: "invalid" };
      route = { kind: "thread", threadID, messageID };
    } else if (!threadID && parts[0] === "new" && parts.length === 1) route = { kind: "new" };
    else if (!threadID && parts[0] === "history" && parts.length === 1) route = { kind: "history" };
    else if ((parts[0] === "runs" && (parts.length === 2 || parts.length === 3)) ||
      (parts[0] === "sessions" && parts.length === 2)) {
      const resourceID = decodeURIComponent(parts[1]);
      if (!/^[a-zA-Z0-9_.-]{1,256}$/u.test(resourceID)) return { kind: "invalid" };
      const pane = parts[2] as V2RunPane | undefined;
      if (parts.length === 3 && !["context", "checkpoints", "tools", "ui-evidence"].includes(pane ?? "")) return { kind: "invalid" };
      route = { kind: parts[0] === "runs" ? "run" : "session", resourceID,
        ...(threadID ? { threadID } : {}), ...(pane ? { pane } : {}) };
    } else if (parts[0] === "automations" && (parts.length === 1 || (parts.length === 3 && parts[1] === "runs"))) {
      const resourceID = parts[2] ? decodeURIComponent(parts[2]) : undefined;
      if (parts.length === 3 && (!resourceID || !/^[a-zA-Z0-9_.-]{1,256}$/u.test(resourceID))) return { kind: "invalid" };
      route = { kind: "automations", ...(threadID ? { threadID } : {}), ...(resourceID ? { resourceID } : {}) };
    } else return { kind: "invalid" };
    return { ...route, ...(section ? { section } : {}) };
  } catch { return { kind: "invalid" }; }
}

function routeHash(route: V2Route) {
  const thread = route.threadID ? `#/threads/${encodeURIComponent(route.threadID)}` : "#";
  const resource = route.resourceID ? encodeURIComponent(route.resourceID) : "";
  const base = route.kind === "thread" ? `${thread}${route.messageID ? `/messages/${encodeURIComponent(route.messageID)}`
    : route.panel === "records" ? "/records" : ""}`
    : route.kind === "history" ? "#/history"
      : route.kind === "run" ? `${thread}/runs/${resource}${route.pane ? `/${route.pane}` : ""}`
        : route.kind === "session" ? `${thread}/sessions/${resource}`
          : route.kind === "automations" ? `${thread}/automations${resource ? `/runs/${resource}` : ""}` : "#/new";
  return `${base}${route.section ? `/settings/${route.section}` : ""}`;
}

function historyIndex() {
  const index: unknown = window.history.state?.v2NavigationIndex;
  return typeof index === "number" && Number.isSafeInteger(index) && index >= 0 ? index : 0;
}

export function useV2Navigation() {
  const leave = useFileEditLeave();
  const [location, setLocation] = useState(() => ({ route: readV2Route(window.location.hash, window.location.pathname), index: historyIndex() }));
  const accepted = useRef({ url: window.location.href, index: historyIndex(), state: window.history.state });
  useEffect(() => {
    // Hash links and the browser's own Back/Forward controls also leave editors.
    // Number hash-created entries so cancellation can restore the original entry.
    window.history.replaceState({ ...window.history.state, v2NavigationIndex: historyIndex() }, "");
    accepted.current.state = window.history.state;
    const sync = () => {
      const previous = accepted.current;
      if (window.location.href === previous.url) return;
      if (!Number.isSafeInteger(window.history.state?.v2NavigationIndex)) {
        window.history.replaceState({ ...window.history.state, v2NavigationIndex: previous.index + 1 }, "");
      }
      const next = { url: window.location.href, index: historyIndex(), state: window.history.state };
      const route = readV2Route(window.location.hash, window.location.pathname);
      leave(() => {
        accepted.current = next;
        setLocation({ route, index: next.index });
      }, () => {
        const delta = previous.index - next.index;
        if (delta) window.history.go(delta);
        else window.history.replaceState(previous.state, "", previous.url);
      });
    };
    window.addEventListener("popstate", sync);
    window.addEventListener("hashchange", sync);
    return () => { window.removeEventListener("popstate", sync); window.removeEventListener("hashchange", sync); };
  }, [leave]);
  const navigate = useCallback((route: V2Route, replace = false, onAccepted?: () => void) => {
    const hash = routeHash(route);
    if (hash === window.location.hash) { onAccepted?.(); return; }
    leave(() => {
      const index = historyIndex() + (replace ? 0 : 1);
      const state = { ...window.history.state, v2NavigationIndex: index };
      if (replace) window.history.replaceState(state, "", hash);
      else window.history.pushState(state, "", hash);
      accepted.current = { url: window.location.href, index, state };
      setLocation({ route, index });
      onAccepted?.();
    });
  }, [leave]);
  const back = () => {
    const route = location.route;
    if (route.section) navigate({ ...route, section: undefined }, true);
    else if (route.kind === "run" || route.kind === "session")
      navigate(route.threadID ? { kind: "thread", threadID: route.threadID, panel: "records" } : { kind: "history" }, true);
    else if (route.kind === "automations")
      navigate(route.threadID ? { kind: "thread", threadID: route.threadID } : { kind: "new" }, true);
    else if (route.panel === "records") navigate({ kind: "thread", threadID: route.threadID }, true);
    else if (location.index > 0) window.history.back();
    else if (route.kind === "history") navigate({ kind: "new" }, true);
  };
  return { route: location.route, navigate, back, canGoBack: location.index > 0 || Boolean(location.route.section ||
    location.route.panel || ["history", "run", "session", "automations"].includes(location.route.kind)) };
}
