import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { readTurnNotificationMode } from "../turn-notifications";
import { V2NotificationSettings } from "./notification-settings";

const requestPermission = vi.fn();
beforeEach(() => {
  localStorage.clear(); requestPermission.mockReset();
  vi.stubGlobal("Notification", class { static permission = "default"; static requestPermission = requestPermission; });
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("requests permission only after clicking Allow and persists the chosen mode", async () => {
  requestPermission.mockResolvedValue("granted");
  render(<LocaleProvider><V2NotificationSettings /></LocaleProvider>);
  expect(requestPermission).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("回复提醒方式"), { target: { value: "background" } });
  expect(readTurnNotificationMode()).toBe("background");
  expect(requestPermission).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "允许通知" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "发送测试通知" })).toBeVisible());
  expect(requestPermission).toHaveBeenCalledOnce();
});

it("explains denied or unsupported notification delivery", async () => {
  requestPermission.mockResolvedValue("denied");
  const view = render(<LocaleProvider><V2NotificationSettings /></LocaleProvider>);
  fireEvent.click(screen.getByRole("button", { name: "允许通知" }));
  await waitFor(() => expect(screen.getByText(/通知已被阻止/)).toBeVisible());
  view.unmount();
  vi.stubGlobal("Notification", undefined);
  render(<LocaleProvider><V2NotificationSettings /></LocaleProvider>);
  expect(screen.getByLabelText("回复提醒方式")).toBeDisabled();
  expect(screen.getByText("当前窗口不支持系统通知。")).toBeVisible();
});

it("shows a storage failure without presenting the preference as saved", () => {
  render(<LocaleProvider><V2NotificationSettings /></LocaleProvider>);
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
  fireEvent.change(screen.getByLabelText("回复提醒方式"), { target: { value: "always" } });
  expect(screen.getByRole("alert")).toHaveTextContent("提醒偏好未能保存");
  expect(screen.getByLabelText("回复提醒方式")).toHaveValue("off");
});
