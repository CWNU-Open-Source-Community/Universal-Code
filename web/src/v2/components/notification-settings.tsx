import { useEffect, useState } from "react";
import { useLocale } from "../../lib/locale";
import { readTurnNotificationMode, saveTurnNotificationMode, showTurnNotification,
  turnNotificationPermission, type TurnNotificationMode } from "../turn-notifications";

export function V2NotificationSettings() {
  const { t } = useLocale();
  const [mode, setMode] = useState(readTurnNotificationMode);
  const [permission, setPermission] = useState(turnNotificationPermission);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const refresh = () => { setMode(readTurnNotificationMode()); setPermission(turnNotificationPermission()); };
    window.addEventListener("focus", refresh);
    window.addEventListener("storage", refresh);
    return () => { window.removeEventListener("focus", refresh); window.removeEventListener("storage", refresh); };
  }, []);
  const save = (next: TurnNotificationMode) => {
    try { saveTurnNotificationMode(next); setMode(next); setError(""); }
    catch { setError(t("提醒偏好未能保存，请检查本机存储后重试。", "Could not save notification preferences. Check local storage and try again.")); }
  };
  const request = async () => {
    setPending(true); setError("");
    try { setPermission(await Notification.requestPermission()); }
    catch { setError(t("无法请求通知权限，请检查浏览器或系统设置。", "Could not request notification permission. Check your browser or system settings.")); }
    finally { setPending(false); }
  };
  return <section className="v2-settings-section"><h2>{t("通知", "Notifications")}</h2>
    <div className="v2-settings-card">
      <div className="v2-setting-row"><div><strong>{t("回复提醒", "Reply notifications")}</strong>
        <span>{t("仅限本窗口发送的消息，窗口需保持打开。", "Only for messages sent from this window. Keep the window open.")}</span></div>
        <div className="v2-setting-value"><select aria-label={t("回复提醒方式", "Reply notification mode")} value={mode}
          disabled={permission === "unsupported"} onChange={(event) => save(event.target.value as TurnNotificationMode)}>
          <option value="off">{t("关闭", "Off")}</option>
          <option value="background">{t("离开当前聊天时", "When away from the chat")}</option>
          <option value="always">{t("始终提醒", "Always")}</option>
        </select></div>
      </div>
      <div className="v2-setting-row"><div><strong>{t("系统通知", "System notifications")}</strong>{permission !== "default" && <span>
        {permission === "unsupported" ? t("当前窗口不支持系统通知。", "System notifications are unavailable in this window.")
          : permission === "denied" ? t("通知已被阻止，请在浏览器或系统设置中允许。", "Notifications are blocked. Allow them in your browser or system settings.")
          : t("已允许", "Allowed")}
      </span>}</div><div className="v2-setting-value">
        {permission === "default" && <button className="v2-setting-link" type="button" disabled={pending} onClick={() => void request()}>
          {pending ? t("等待允许…", "Waiting…") : t("允许通知", "Allow notifications")}</button>}
        {permission === "granted" && <button className="v2-setting-link" type="button" onClick={() => {
          const shown = showTurnNotification("Universal-Code", t("通知已准备就绪。", "Notifications are ready."));
          setError(shown ? "" : t("通知未能发送，请检查系统设置。", "Could not send the notification. Check system settings."));
        }}>{t("发送测试通知", "Send test notification")}</button>}
      </div></div>
    </div>
    {error && <p role="alert">{error}</p>}
  </section>;
}
