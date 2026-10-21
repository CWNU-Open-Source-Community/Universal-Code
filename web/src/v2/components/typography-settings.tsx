import { useId, useState } from "react";
import { defaultTypography, readTypography, saveTypography, typographyLimits, type TypographySettings } from "../../lib/typography";
import { useLocale } from "../../lib/locale";
import "./typography-settings.css";

export function TypographySettingsPanel() {
  const { t } = useLocale();
  const [settings, setSettings] = useState(readTypography);
  const [error, setError] = useState("");
  const id = useId();
  const update = (next: TypographySettings) => {
    try { setSettings(saveTypography(next)); setError(""); }
    catch { setError(t("字号未能保存，请重试。", "Font sizes could not be saved. Try again.")); }
  };
  const labels = {
    messageSize: [t("消息文字字号", "Message font size"), t("调整对话中消息文字的大小。", "Adjust message text in conversations.")],
    codeSize: [t("代码字号", "Code font size"), t("调整消息中代码块的大小。", "Adjust code blocks in messages.")],
  };
  const isDefault = settings.messageSize === defaultTypography.messageSize && settings.codeSize === defaultTypography.codeSize;
  return <section className="v2-settings-section v2-typography-settings" aria-labelledby={`${id}-heading`}>
    <h2 id={`${id}-heading`}>{t("阅读字号", "Reading font sizes")}</h2>
    <div className="v2-settings-card">
      {(["messageSize", "codeSize"] as const).map((key) => <div className="v2-setting-row" key={key}>
        <div><label htmlFor={`${id}-${key}`}>{labels[key][0]}</label><span id={`${id}-${key}-help`}>{labels[key][1]}</span></div>
        <div className="v2-setting-value v2-typography-control">
          <input id={`${id}-${key}`} type="range" min={typographyLimits[key].min} max={typographyLimits[key].max} step={1}
            aria-describedby={`${id}-${key}-help`} value={settings[key]}
            onChange={(event) => update({ ...settings, [key]: Number(event.currentTarget.value) })} />
          <output htmlFor={`${id}-${key}`}>{settings[key]} px</output>
        </div>
      </div>)}
      <div className="v2-setting-row">
        <div><strong>{t("默认字号", "Default font sizes")}</strong><span>{t("消息文字 14 px，代码块 13 px。设置保存在此设备。", "Message text 14 px, code blocks 13 px. Saved on this device.")}</span></div>
        <div className="v2-setting-value"><button disabled={isDefault} onClick={() => update({ ...defaultTypography })} type="button">
          {t("还原字号", "Reset font sizes")}</button></div>
      </div>
    </div>
    {error && <p className="v2-inline-error" role="alert">{error}</p>}
    <div className="v2-typography-preview" aria-label={t("字号预览", "Font size preview")}>
      <p>{t("选择适合阅读的字号。消息和代码会立即更新。", "Choose a comfortable reading size. Messages and code update immediately.")}</p>
      <pre><code>{'const message = "Hello";\nconsole.log(message);'}</code></pre>
    </div>
  </section>;
}
