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
    messageSize: t("消息文字字号", "Message font size"),
    codeSize: t("代码字号", "Code font size"),
  };
  const isDefault = settings.messageSize === defaultTypography.messageSize && settings.codeSize === defaultTypography.codeSize;
  return <section className="v2-settings-section v2-typography-settings" aria-labelledby={`${id}-heading`}>
    <header className="v2-typography-heading">
      <h2 id={`${id}-heading`} title={t("字号设置保存在此设备。", "Font sizes are saved on this device.")}>{t("阅读字号", "Reading font sizes")}</h2>
      <div className="v2-setting-value"><button disabled={isDefault} onClick={() => update({ ...defaultTypography })} type="button">
        {t("还原字号", "Reset font sizes")}</button></div>
    </header>
    <div className="v2-settings-card">
      {(["messageSize", "codeSize"] as const).map((key) => <div className="v2-setting-row" key={key}>
        <div><label htmlFor={`${id}-${key}`}>{labels[key]}</label></div>
        <div className="v2-setting-value v2-typography-control">
          <input id={`${id}-${key}`} type="range" min={typographyLimits[key].min} max={typographyLimits[key].max} step={1}
            value={settings[key]}
            onChange={(event) => update({ ...settings, [key]: Number(event.currentTarget.value) })} />
          <output htmlFor={`${id}-${key}`}>{settings[key]} px</output>
        </div>
      </div>)}
    </div>
    {error && <p className="v2-inline-error" role="alert">{error}</p>}
    <div className="v2-typography-preview" aria-label={t("字号预览", "Font size preview")}>
      <p>{t("你好，今天想做些什么？", "Hello, what would you like to work on today?")}</p>
      <pre><code>{'const message = "Hello";\nconsole.log(message);'}</code></pre>
    </div>
  </section>;
}
