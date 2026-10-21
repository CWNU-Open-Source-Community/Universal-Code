import type { ThreadMessageControlView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { notifyTurnReply, readTurnNotificationMode, saveTurnNotificationMode, showTurnNotification } from "./turn-notifications";

const notifications: Array<{ title: string; options: NotificationOptions; onclick?: () => void; close: ReturnType<typeof vi.fn> }> = [];
class TestNotification {
  static permission: NotificationPermission = "granted";
  close = vi.fn();
  onclick?: () => void;
  constructor(readonly title: string, readonly options: NotificationOptions) { notifications.push(this); }
}
const reply = () => ({ version: "thread_message_submission.v1", replayed: false, execution_started: true,
  model_called: true, steering: { status: "committed" },
  thread: { id: "thread-one", title: "修复登录", composer_state: "ready" } }) as ThreadMessageControlView;

beforeEach(() => {
  localStorage.clear(); notifications.length = 0;
  TestNotification.permission = "granted";
  vi.stubGlobal("Notification", TestNotification);
  vi.spyOn(document, "hasFocus").mockReturnValue(true);
  vi.spyOn(window, "focus").mockImplementation(() => {});
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  window.history.replaceState(null, "", "#/threads/thread-one");
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("defaults off and never requests permissions while a reply arrives", () => {
  expect(readTurnNotificationMode()).toBe("off");
  notifyTurnReply(reply());
  expect(notifications).toHaveLength(0);
  localStorage.setItem("universal-code.turn-notifications.v1", "bad-value");
  expect(readTurnNotificationMode()).toBe("off");
});

it("notifies only when away, and opens the exact chat on click", () => {
  saveTurnNotificationMode("background");
  notifyTurnReply(reply());
  expect(notifications).toHaveLength(0);
  window.history.replaceState(null, "", "#/threads/thread-two");
  notifyTurnReply(reply());
  expect(notifications).toHaveLength(1);
  expect(notifications[0].options.body).toBe("修复登录");
  expect(notifications[0].options.tag).toBe("uc-reply:thread-one");
  notifications[0].onclick!();
  expect(window.location.hash).toBe("#/threads/thread-one");
  expect(notifications[0].close).toHaveBeenCalledOnce();
});

it("allows background windows and settings, and localizes notification text", () => {
  saveTurnNotificationMode("background");
  vi.mocked(document.hasFocus).mockReturnValue(false);
  notifyTurnReply(reply());
  expect(notifications).toHaveLength(1);
  vi.mocked(document.hasFocus).mockReturnValue(true);
  window.history.replaceState(null, "", "#/threads/thread-one/settings/general");
  localStorage.setItem("prayu.locale.v1", "en-US");
  notifyTurnReply(reply());
  expect(notifications[1].title).toBe("Universal-Code · Reply ready");
});

it("never labels queued, replayed, approval-waiting or non-model requests as replies", () => {
  saveTurnNotificationMode("always");
  const cases = [ { replayed: true }, { execution_started: false }, { model_called: false },
    { steering: { status: "pending" } }, { thread: { ...reply().thread, composer_state: "waiting_approval" } },
    { version: "session_message_submission.v1" } ];
  for (const patch of cases) notifyTurnReply({ ...reply(), ...patch } as ThreadMessageControlView);
  expect(notifications).toHaveLength(0);
  notifyTurnReply(reply());
  expect(notifications).toHaveLength(1);
});

it("handles unavailable or denied APIs and delivery errors without failing the turn", () => {
  saveTurnNotificationMode("always");
  TestNotification.permission = "denied";
  notifyTurnReply(reply());
  expect(notifications).toHaveLength(0);
  vi.stubGlobal("Notification", undefined);
  expect(showTurnNotification("test", "test")).toBe(false);
  vi.stubGlobal("Notification", class { static permission = "granted"; constructor() { throw new Error("OS rejected"); } });
  expect(() => notifyTurnReply(reply())).not.toThrow();
});

it("does not navigate a notification into a different connection", () => {
  saveTurnNotificationMode("always");
  notifyTurnReply(reply());
  window.history.replaceState(null, "", "#/new");
  const previous = useConnectionStore.getState().apiBaseURL;
  useConnectionStore.setState({ apiBaseURL: "http://different-server/api/v1" });
  notifications[0].onclick!();
  expect(window.location.hash).toBe("#/new");
  useConnectionStore.setState({ apiBaseURL: previous });
});

it("ignores an old server reply received after reconnecting elsewhere", () => {
  saveTurnNotificationMode("always");
  const original = useConnectionStore.getState();
  useConnectionStore.setState({ apiBaseURL: "http://different-server/api/v1" });
  notifyTurnReply(reply(), original);
  expect(notifications).toHaveLength(0);
  useConnectionStore.setState({ apiBaseURL: original.apiBaseURL });
});
