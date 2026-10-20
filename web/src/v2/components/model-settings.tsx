import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowLeft, Settings2, UserRound } from "lucide-react";
import type { APIClient } from "../../api/client";
import { V2ProviderSettings } from "./provider-settings";
import { V2ChatGPTAccountSettings, useChatGPTAuthStatus, type V2ReadyModelSource } from "./chatgpt-account-settings";
import { useLocale, type LocaleText } from "../../lib/locale";
import "./settings.css";

export type V2ModelProviderPreset = {
  id: string;
  providerName: string;
  modelName: string;
  icon: ReactNode;
  /** True only after the OS credential store reports a configured secret. */
  credentialConfigured?: boolean;
};

export type V2ModelSettingsProps = {
  client: APIClient;
  presets: readonly V2ModelProviderPreset[];
  prepareForDraft?: boolean;
  onReady?: (source: V2ReadyModelSource) => void;
  onSelectPreset: (preset: V2ModelProviderPreset, trigger: HTMLButtonElement) => void;
};

function setupStateLabel(preset: V2ModelProviderPreset, t: LocaleText): string {
  return preset.credentialConfigured ? t("已保存 API Key", "API key saved") : t("尚未保存 API Key", "No API key saved");
}

export function V2ModelSettings({ client, presets, onSelectPreset, prepareForDraft, onReady }: V2ModelSettingsProps) {
  const { t } = useLocale();
  const chatGPT = useChatGPTAuthStatus(client);
  const [view, setView] = useState<"catalog" | "custom" | "chatgpt">("catalog");
  const chatGPTTriggerRef = useRef<HTMLButtonElement>(null);
  const customTriggerRef = useRef<HTMLButtonElement>(null);
  const backButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (view !== "custom") return;
    const frame = requestAnimationFrame(() => backButtonRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [view]);

  const returnToCatalog = () => {
    setView("catalog");
    requestAnimationFrame(() => (view === "chatgpt" ? chatGPTTriggerRef : customTriggerRef).current?.focus());
  };

  if (view === "chatgpt") {
    return <V2ChatGPTAccountSettings client={client} query={chatGPT} onExit={returnToCatalog}
      prepareForDraft={prepareForDraft} onReady={onReady} />;
  }

  if (view === "custom") {
    return <>
      <button aria-label={t("返回模型选择", "Back to model selection")} className="v2-model-custom-back"
        onClick={returnToCatalog} ref={backButtonRef} type="button">
        <ArrowLeft aria-hidden="true" size={16} />{t("返回模型", "Back to models")}</button>
      <V2ProviderSettings client={client} />
    </>;
  }

  const chatGPTState = !client.hasChatGPTAuth ? t("账户登录未启用", "Account sign-in disabled")
    : chatGPT.isError ? t("账户状态读取失败", "Could not read account status")
      : chatGPT.data?.login?.state === "waiting" ? t("等待登录", "Waiting for sign-in")
        : chatGPT.data?.accounts.some((account) => account.state === "connected") ? t("账户已连接", "Account connected")
          : chatGPT.data?.available === false ? t("凭据库不可用", "Credential store unavailable") : t("使用 ChatGPT 账户登录", "Sign in with a ChatGPT account");

  return <section aria-labelledby="v2-model-settings-title">
    <header className="v2-model-catalog-heading">
      <h1 id="v2-model-settings-title">{t("模型", "Models")}</h1>
      <p>{t("选择供应商，保存连接信息并完成模型检查。兼容接口可从自定义配置接入。", "Choose a provider, save its connection and check the model. Use custom configuration for compatible endpoints.")}</p>
    </header>
    <ul aria-label={t("模型供应商", "Model providers")} className="v2-model-catalog" role="list">
      <li>
        <button aria-label={t("打开自定义模型配置", "Open custom model configuration")} className="v2-model-card is-custom"
          onClick={() => setView("custom")} ref={customTriggerRef} type="button">
          <span aria-hidden="true" className="v2-model-card-icon"><Settings2 /></span>
          <span className="v2-model-card-copy"><strong>{t("自定义配置", "Custom configuration")}</strong>
            <span>{t("自定义模型", "Custom models")}</span></span>
        </button>
      </li>
      <li>
        <button aria-label={t(`ChatGPT，订阅账户，${chatGPTState}`, `ChatGPT, subscription account, ${chatGPTState}`)} className="v2-model-card"
          onClick={() => setView("chatgpt")} ref={chatGPTTriggerRef} type="button">
          <span aria-hidden="true" className="v2-model-card-icon"><UserRound /></span>
          <span className="v2-model-card-copy"><strong>ChatGPT</strong>
            <span>{t("订阅账户", "Subscription account")}</span><span>{chatGPTState}</span></span>
        </button>
      </li>
      {presets.map((preset) => {
        const setupState = setupStateLabel(preset, t);
        return <li key={preset.id}>
          <button aria-label={[preset.providerName, preset.modelName, setupState].join(t("，", ", "))}
            className="v2-model-card" onClick={(event) => onSelectPreset(preset, event.currentTarget)}
            data-model-provider-id={preset.id} type="button">
            <span aria-hidden="true" className="v2-model-card-icon">{preset.icon}</span>
            <span className="v2-model-card-copy"><strong>{preset.providerName}</strong>
              <span>{preset.modelName}</span><span>{setupState}</span></span>
          </button>
        </li>;
      })}
    </ul>
  </section>;
}
