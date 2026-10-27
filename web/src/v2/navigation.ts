import { useCallback, useEffect, useRef, useState } from "react";
import type { V2SettingsSection } from "./components/sidebar";
import { useFileEditLeave } from "../components/file-edit-drafts";

export type V2RunPane = "context" | "checkpoints" | "tools" | "ui-evidence";
export type V2Route = { kind: "initial" | "new" | "thread" | "invalid";
  threadID?: string; messageID?: string; section?: V2SettingsSection; view?: "inspector";
  tool?: "run" | "session" | "schedule"; resourceID?: string; pane?: V2RunPane };
const sections: string[] = ["general", "models", "permissions", "appearance", "inspector", "archived",
  "extensions", "skills", "advanced-models", "about", "shortcuts", "connections", "task-configuration", "environment", "configuration", "usage"];

// Fragments work with both the loopback UI and the existing Desktop asset
// server. History contains navigation identity only, never tokens or drafts.
export function readV2Route(hash: string, pathname = "/"): V2Route {
  if (!hash && (pathname === "/legacy" || pathname === "/legacy/")) return { kind: "new", view: "inspector" };
  if (!hash && pathname.startsWith("/legacy/")) {
    const legacy = /^\/legacy\/(threads|runs|sessions)\/([^/]+)\/?$/u.exec(pathname);
    if (!legacy) return { kind: "invalid" };
    let id: string;
    try { id = decodeURIComponent(legacy[2]); } catch { return { kind: "invalid" }; }
    if (!/^[a-zA-Z0-9_.-]{1,256}$/u.test(id)) return { kind: "invalid" };
    return legacy[1] === "threads" ? { kind: "thread", threadID: id, view: "inspector" }
      : { kind: "new", view: "inspector", tool: legacy[1] === "runs" ? "run" : "session", resourceID: id };
  }
  if (!hash || hash === "#") return { kind: "initial" };
  const message = /^#\/threads\/([a-zA-Z0-9_.-]{1,256})\/messages\/([a-zA-Z0-9_.-]{1,256})$/u.exec(hash);
  if (message) return { kind: "thread", threadID: message[1], messageID: message[2] };
  const match = /^#\/(new|threads\/([^/]+))(\/inspector(?:\/(schedule(?:\/[^/]+)?|runs\/[^/]+(?:\/(?:context|checkpoints|tools|ui-evidence))?|sessions\/[^/]+))?)?(?:\/settings\/([^/]+))?$/u.exec(hash);
  if (!match) return { kind: "invalid" };
  let threadID: string | undefined;
  try { threadID = match[2] ? decodeURIComponent(match[2]) : undefined; }
  catch { return { kind: "invalid" }; }
  if (threadID && !/^[a-zA-Z0-9_.-]{1,256}$/u.test(threadID)) return { kind: "invalid" };
  const section = (match[5] === "plugins" ? "extensions" : match[5] === "keyboard" ? "shortcuts"
    : match[5]) as V2SettingsSection | undefined;
  if (section && !sections.includes(section)) return { kind: "invalid" };
  const tool = match[4]?.startsWith("runs/") ? "run" : match[4]?.startsWith("sessions/") ? "session"
    : match[4]?.startsWith("schedule") ? "schedule" : undefined;
  let resourceID: string | undefined;
  try { resourceID = match[4]?.includes("/") ? decodeURIComponent(match[4]!.split("/")[1]) : undefined; }
  catch { return { kind: "invalid" }; }
  if (resourceID && !/^[a-zA-Z0-9_.-]{1,256}$/u.test(resourceID)) return { kind: "invalid" };
  const pane = tool === "run" ? match[4]?.split("/")[2] as V2RunPane | undefined : undefined;
  return { kind: threadID ? "thread" : "new", ...(threadID ? { threadID } : {}),
    ...(match[3] ? { view: "inspector" as const } : {}), ...(tool ? { tool } : {}),
    ...(resourceID ? { resourceID } : {}), ...(pane ? { pane } : {}), ...(section ? { section } : {}) };
}

function routeHash(route: V2Route) {
  const base = route.kind === "thread" ? `#/threads/${encodeURIComponent(route.threadID!)}` : "#/new";
  if (route.messageID && route.kind === "thread" && !route.section && !route.view) return `${base}/messages/${encodeURIComponent(route.messageID)}`;
  const tool = route.tool === "schedule" ? `/schedule${route.resourceID ? `/${encodeURIComponent(route.resourceID)}` : ""}` : route.tool && route.resourceID
    ? `/${route.tool}s/${encodeURIComponent(route.resourceID)}${route.tool === "run" && route.pane ? `/${route.pane}` : ""}` : "";
  return `${base}${route.view === "inspector" ? `/inspector${tool}` : ""}${route.section ? `/settings/${route.section}` : ""}`;
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
      const url = window.location.pathname.startsWith("/legacy") ? `/${window.location.search}${hash}` : hash;
      if (replace) window.history.replaceState(state, "", url);
      else window.history.pushState(state, "", url);
      accepted.current = { url: window.location.href, index, state };
      setLocation({ route, index });
      onAccepted?.();
    });
  }, [leave]);
  const back = () => {
    if (location.route.section) navigate({ ...location.route, section: undefined }, true);
    else if (location.index > 0) window.history.back();
    else if (location.route.view) navigate({ ...location.route, view: undefined, tool: undefined, resourceID: undefined, pane: undefined }, true);
  };
  return { route: location.route, navigate, back, canGoBack: location.index > 0 || Boolean(location.route.section || location.route.view) };
}
