import type { SessionMessageControlView, ThreadMessageControlView } from "../api/types";
import { readPrayuLocale } from "../lib/locale";
import { useConnectionStore } from "../state/connection";
import { readV2Route } from "./navigation";

export type TurnNotificationMode = "off" | "background" | "always";
const preferenceKey = "universal-code.turn-notifications.v1";

export function readTurnNotificationMode(): TurnNotificationMode {
  try {
    const value = localStorage.getItem(preferenceKey);
    return value === "background" || value === "always" ? value : "off";
  } catch { return "off"; }
}

export function saveTurnNotificationMode(mode: TurnNotificationMode) {
  localStorage.setItem(preferenceKey, mode);
}

export function turnNotificationPermission(): NotificationPermission | "unsupported" {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

export function showTurnNotification(title: string, body: string, threadID?: string): boolean {
  if (turnNotificationPermission() !== "granted") return false;
  const connection = useConnectionStore.getState();
  try {
    const notification = new Notification(title, { body, ...(threadID ? { tag: `uc-reply:${threadID}` } : {}) });
    notification.onclick = () => {
      notification.close();
      window.focus();
      const current = useConnectionStore.getState();
      // A notification from a previous server must not open an unrelated chat.
      if (threadID && current.health?.data_store_id === connection.health?.data_store_id &&
        current.apiBaseURL === connection.apiBaseURL && current.token === connection.token) {
        window.location.hash = `#/threads/${encodeURIComponent(threadID)}`;
      }
    };
    return true;
  } catch { return false; }
}

// This runs only for a fresh reply received by this window. Queued inputs,
// steering acknowledgements, approval waits and recovery replays are not replies.
export function notifyTurnReply(result: ThreadMessageControlView | SessionMessageControlView,
  connection = useConnectionStore.getState()) {
  const mode = readTurnNotificationMode();
  if (mode === "off") return;
  const current = useConnectionStore.getState();
  if (current.health?.data_store_id !== connection.health?.data_store_id ||
    current.apiBaseURL !== connection.apiBaseURL || current.token !== connection.token) return;
  if (result.version !== "thread_message_submission.v1" || result.replayed ||
    !result.execution_started || !result.model_called || result.steering.status !== "committed" ||
    result.thread.composer_state === "waiting_approval") return;
  const route = readV2Route(window.location.hash, window.location.pathname);
  if (mode === "background" && document.visibilityState === "visible" && document.hasFocus() &&
    route.threadID === result.thread.id && !route.section && !route.view) return;
  const english = readPrayuLocale() === "en-US";
  showTurnNotification(english ? "Universal-Code · Reply ready" : "Universal-Code · 新回复已就绪",
    result.thread.title, result.thread.id);
}
