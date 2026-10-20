import type { V2ModelProviderPreset } from "./model-settings";
import type { V2ProviderDraftPreset } from "./provider-settings";
import type { LocaleText } from "../../lib/locale";

export interface V2ConfiguredModelProviderPreset extends V2ModelProviderPreset {
  /** Each preset opens a supported API provider draft. */
  draft: V2ProviderDraftPreset;
}

type BrandIconProps = {
  path: string;
  viewBox?: string;
};

function BrandIcon({ path, viewBox = "0 0 24 24" }: BrandIconProps) {
  return <svg aria-hidden="true" focusable="false" viewBox={viewBox}>
    <path d={path} fill="currentColor" />
  </svg>;
}

function GrokIcon() {
  return <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24">
    <path d="M5 4.5 19 19.5M19 4.5 5 19.5" fill="none" stroke="currentColor"
      strokeLinecap="round" strokeWidth="2.2" />
    <path d="M13.3 7.1A6 6 0 1 0 17.7 17" fill="none" stroke="currentColor"
      strokeLinecap="round" strokeWidth="1.7" />
  </svg>;
}

const icons = {
  anthropic: <BrandIcon path="M17.3041 3.541h-3.6718l6.696 16.918H24Zm-10.6082 0L0 20.459h3.7442l1.3693-3.5527h7.0052l1.3693 3.5528h3.7442L10.5363 3.5409Zm-.3712 10.2232 2.2914-5.9456 2.2914 5.9456Z" />,
  openai: <BrandIcon path="M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1686a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4944zm-9.6607-4.1254a4.4708 4.4708 0 0 1-.5346-3.0137l.142.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1408-1.6464zM2.3408 7.8956a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1685a.0757.0757 0 0 1-.071 0l-4.8303-2.7865A4.504 4.504 0 0 1 2.3408 7.872zm16.5963 3.8558L13.1038 8.364 15.1192 7.2a.0757.0757 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.407-.667zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.409 9.2297V6.8974a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1638a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.5974 1.4997-2.6067-1.4997Z" />,
  deepseek: <BrandIcon path="M23.748 4.651c-.254-.124-.364.113-.512.233-.051.04-.094.09-.137.137-.372.397-.806.657-1.373.626-.829-.046-1.537.214-2.163.848-.133-.782-.575-1.248-1.247-1.548-.352-.155-.708-.311-.955-.65-.172-.24-.219-.509-.305-.774-.055-.16-.11-.323-.293-.35-.2-.031-.278.136-.356.276-.313.572-.434 1.202-.422 1.84.027 1.436.633 2.58 1.838 3.393.137.094.172.187.129.323-.082.28-.18.553-.266.833-.055.179-.137.218-.328.14a5.5 5.5 0 0 1-1.737-1.179c-.857-.828-1.631-1.743-2.597-2.46a12 12 0 0 0-.689-.47c-.985-.957.13-1.743.387-1.836.27-.098.094-.433-.778-.428-.872.003-1.67.295-2.687.685a3 3 0 0 1-.465.136 9.6 9.6 0 0 0-2.883-.101c-1.885.21-3.39 1.1-4.497 2.622C.082 8.776-.231 10.854.152 13.02c.403 2.284 1.568 4.175 3.36 5.653 1.857 1.533 3.997 2.284 6.438 2.14 1.482-.085 3.132-.284 4.994-1.86.47.234.962.328 1.78.398.629.058 1.235-.031 1.705-.129.735-.155.684-.836.418-.961-2.155-1.004-1.682-.595-2.112-.926 1.095-1.295 2.768-3.598 3.284-6.733.05-.346.115-.834.108-1.114-.004-.171.035-.238.23-.257a4.2 4.2 0 0 0 1.545-.475c1.397-.763 1.96-2.016 2.093-3.517.02-.23-.004-.467-.247-.588M11.58 18.168c-2.088-1.642-3.101-2.183-3.52-2.16-.39.024-.32.472-.234.763.09.288.207.487.371.74.114.167.192.416-.113.603-.673.416-1.842-.14-1.897-.168-1.361-.801-2.5-1.86-3.301-3.306-.775-1.393-1.225-2.888-1.299-4.482-.02-.385.094-.522.477-.592a4.7 4.7 0 0 1 1.53-.038c2.131.311 3.946 1.264 5.467 2.774.868.86 1.525 1.887 2.202 2.89.72 1.066 1.494 2.082 2.48 2.915.348.291.626.513.892.677-.802.09-2.14.109-3.055-.615zm1.001-6.44a.306.306 0 0 1 .415-.287.3.3 0 0 1 .113.074.3.3 0 0 1 .086.214c0 .17-.136.307-.308.307a.303.303 0 0 1-.306-.307m3.11 1.596c-.2.081-.4.151-.591.16a1.25 1.25 0 0 1-.798-.254c-.274-.23-.47-.358-.551-.758a1.7 1.7 0 0 1 .015-.588c.07-.327-.007-.537-.238-.727-.188-.156-.426-.199-.689-.199a.6.6 0 0 1-.254-.078.253.253 0 0 1-.114-.358 1 1 0 0 1 .192-.21c.356-.202.767-.136 1.146.016.352.144.618.408 1.001.782.392.451.462.576.685.915.176.264.336.536.446.848.066.194-.02.353-.25.45" />,
  gemini: <BrandIcon path="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81" />,
  minimax: <BrandIcon path="M11.43 3.92a.86.86 0 1 0-1.718 0v14.236a1.999 1.999 0 0 1-3.997 0V9.022a.86.86 0 1 0-1.718 0v3.87a1.999 1.999 0 0 1-3.997 0V11.49a.57.57 0 0 1 1.139 0v1.404a.86.86 0 0 0 1.719 0V9.022a1.999 1.999 0 0 1 3.997 0v9.134a.86.86 0 0 0 1.719 0V3.92a1.998 1.998 0 1 1 3.996 0v11.788a.57.57 0 1 1-1.139 0zm10.572 3.105a2 2 0 0 0-1.999 1.997v7.63a.86.86 0 0 1-1.718 0V3.923a1.999 1.999 0 0 0-3.997 0v16.16a.86.86 0 0 1-1.719 0V18.08a.57.57 0 1 0-1.138 0v2a1.998 1.998 0 0 0 3.996 0V3.92a.86.86 0 0 1 1.719 0v12.73a1.999 1.999 0 0 0 3.996 0V9.023a.86.86 0 1 1 1.72 0v6.686a.57.57 0 0 0 1.138 0V9.022a2 2 0 0 0-1.998-1.997" />,
  xiaomi: <BrandIcon path="M12 0C8.016 0 4.756.255 2.493 2.516.23 4.776 0 8.033 0 12.012c0 3.98.23 7.235 2.494 9.497C4.757 23.77 8.017 24 12 24c3.983 0 7.243-.23 9.506-2.491C23.77 19.247 24 15.99 24 12.012c0-3.984-.233-7.243-2.502-9.504C19.234.252 15.978 0 12 0zM4.906 7.405h5.624c1.47 0 3.007.068 3.764.827.746.746.827 2.233.83 3.676v4.54a.15.15 0 0 1-.152.147h-1.947a.15.15 0 0 1-.152-.148V11.83c-.002-.806-.048-1.634-.464-2.051-.358-.36-1.026-.441-1.72-.458H7.158a.15.15 0 0 0-.151.147v6.98a.15.15 0 0 1-.152.148H4.906a.15.15 0 0 1-.15-.148V7.554a.15.15 0 0 1 .15-.149zm12.131 0h1.949a.15.15 0 0 1 .15.15v8.892a.15.15 0 0 1-.15.148h-1.949a.15.15 0 0 1-.151-.148V7.554a.15.15 0 0 1 .151-.149zM8.92 10.948h2.046c.083 0 .15.066.15.147v5.352a.15.15 0 0 1-.15.148H8.92a.15.15 0 0 1-.152-.148v-5.352a.15.15 0 0 1 .152-.147Z" />,
  kimi: <BrandIcon path="M21.765.351C22.998.351 24 1.353 24 2.586S22.998 4.82 21.765 4.82h-1.974c-.15 0-.26-.12-.26-.26V2.586A2.237 2.237 0 0 1 21.765.35M9.41 13.388l8.447-8.377c.16-.16.07-.471-.14-.471h-4.55s-.1.02-.14.06l-9.099 9.029c-.14.14-.35.02-.35-.21V4.81c0-.15-.1-.27-.221-.27H.22c-.12 0-.22.12-.22.27v18.57c0 .15.1.27.22.27h3.137c.12 0 .22-.12.22-.27v-3.79c0-.08.03-.16.08-.21l2.826-2.796c.07-.07.16-.08.241-.03l7.546 5.551a8.9 8.9 0 0 0 4.018 1.493c.12.01.23-.11.23-.27V19.76c0-.14-.08-.25-.19-.26a5.8 5.8 0 0 1-2.355-.942l-6.533-4.73c-.14-.09-.15-.32-.03-.441" />,
  opencode: <BrandIcon path="M22 24H2V0h20zM17 4.8H7v14.4h10z" />,
} as const;

export const localizedModelProviderPresets = (t: LocaleText): readonly V2ConfiguredModelProviderPreset[] => [
  {
    id: "official-anthropic",
    providerName: "Claude",
    modelName: "claude-sonnet-5",
    icon: icons.anthropic,
    draft: {
      id: "official-anthropic",
      displayName: t("Claude 官方 API", "Official Claude API"),
      note: t("Anthropic 官方 Messages API；默认使用服务方原生搜索，首次搜索会核实可用性。API Key 仅保存到操作系统凭据库。", "Official Anthropic Messages API. Uses provider-native search by default and verifies availability on first search. API keys are stored only in OS credentials."),
      websiteURL: "https://www.anthropic.com/",
      endpointURL: "https://api.anthropic.com/v1/messages",
      transport: "anthropic_messages",
      models: ["claude-sonnet-5", "claude-opus-5", "claude-haiku-4-5"],
      defaultModel: "claude-sonnet-5",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
      advancedConfig: {},
    },
  },
  {
    id: "official-openai",
    providerName: "OpenAI",
    modelName: "gpt-6.1-sol",
    icon: icons.openai,
    draft: {
      id: "official-openai",
      displayName: t("OpenAI 官方 API", "Official OpenAI API"),
      note: t("OpenAI 官方 Responses API；支持由 Harness 探测并启用原生 Web Search。", "Official OpenAI Responses API. Harness can detect and enable native Web Search."),
      websiteURL: "https://openai.com/",
      endpointURL: "https://api.openai.com/v1/responses",
      transport: "openai_responses",
      models: ["gpt-6.1-sol", "gpt-6-astra", "gpt-6-luna"],
      defaultModel: "gpt-6.1-sol",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
      advancedConfig: {},
    },
  },
  {
    id: "official-deepseek",
    providerName: "DeepSeek",
    modelName: "deepseek-v4-flash",
    icon: icons.deepseek,
    draft: {
      id: "official-deepseek",
      displayName: t("DeepSeek 官方 API", "Official DeepSeek API"),
      note: t("DeepSeek 官方 Responses API；默认关闭私有推理以保证无状态多工具续传，高级 JSON 可显式调整。", "Official DeepSeek Responses API. Private reasoning is disabled by default for stateless multi-tool continuation. Override it in advanced JSON."),
      websiteURL: "https://www.deepseek.com/",
      endpointURL: "https://api.deepseek.com/responses",
      transport: "openai_responses",
      models: ["deepseek-v4-flash", "deepseek-v4-pro"],
      defaultModel: "deepseek-v4-flash",
      searchMode: "web",
      nativeSearchDeclared: false,
      advancedConfig: { request_body: { reasoning: { effort: "none" } } },
    },
  },
  {
    id: "official-google-gemini",
    providerName: "Gemini",
    modelName: "gemini-3.7-flash",
    icon: icons.gemini,
    draft: {
      id: "official-google-gemini",
      displayName: t("Google Gemini 官方 API", "Official Google Gemini API"),
      note: t("Google 官方 OpenAI 兼容接口（Beta）；高级 JSON 可继续覆盖兼容参数。", "Google's official OpenAI-compatible endpoint (Beta). Override compatibility parameters in advanced JSON."),
      websiteURL: "https://ai.google.dev/",
      endpointURL: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
      transport: "openai_chat_completions",
      models: ["gemini-3.7-flash"],
      defaultModel: "gemini-3.7-flash",
      searchMode: "web",
      nativeSearchDeclared: false,
      advancedConfig: {},
    },
  },
  {
    id: "official-xai",
    providerName: "Grok",
    modelName: "grok-4.6",
    icon: <GrokIcon />,
    draft: {
      id: "official-xai",
      displayName: t("xAI Grok 官方 API", "Official xAI Grok API"),
      note: t("xAI 官方 Responses API；支持由 Harness 探测并启用原生搜索。", "Official xAI Responses API. Harness can detect and enable native search."),
      websiteURL: "https://x.ai/",
      endpointURL: "https://api.x.ai/v1/responses",
      transport: "openai_responses",
      models: ["grok-4.6"],
      defaultModel: "grok-4.6",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
      advancedConfig: {},
    },
  },
  {
    id: "official-minimax",
    providerName: "MiniMax",
    modelName: "MiniMax-M3",
    icon: icons.minimax,
    draft: {
      id: "official-minimax",
      displayName: t("MiniMax 官方 API", "Official MiniMax API"),
      note: t("MiniMax 官方 Responses API。", "Official MiniMax Responses API."),
      websiteURL: "https://www.minimax.io/",
      endpointURL: "https://api.minimax.io/v1/responses",
      transport: "openai_responses",
      models: ["MiniMax-M3"],
      defaultModel: "MiniMax-M3",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
      advancedConfig: {},
    },
  },
  {
    id: "official-mimo",
    providerName: "MiMo",
    modelName: "mimo-v2.5-pro",
    icon: icons.xiaomi,
    draft: {
      id: "official-mimo",
      displayName: t("Xiaomi MiMo 官方 API", "Official Xiaomi MiMo API"),
      note: t("MiMo 按量付费 Responses API；请使用此服务的专用凭据。Token Plan 请单独配置对应端点和凭据。", "MiMo pay-as-you-go Responses API. Use credentials for this service. Configure the Token Plan endpoint and credentials separately."),
      websiteURL: "https://mimo.xiaomi.com/",
      endpointURL: "https://api.xiaomimimo.com/v1/responses",
      transport: "openai_responses",
      models: ["mimo-v2.5-pro", "mimo-v2.5"],
      defaultModel: "mimo-v2.5-pro",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
      advancedConfig: {},
    },
  },
  {
    id: "official-kimi",
    providerName: "Kimi",
    modelName: "kimi-k3",
    icon: icons.kimi,
    draft: {
      id: "official-kimi",
      displayName: t("Kimi 官方 API", "Official Kimi API"),
      note: t("Moonshot 普通开放平台 API；与 Kimi for Coding 的会员 Key、域名和配额分开。", "Moonshot's standard platform API. Its keys, endpoint and quota are separate from Kimi for Coding."),
      websiteURL: "https://platform.moonshot.ai/",
      endpointURL: "https://api.moonshot.ai/v1/chat/completions",
      transport: "openai_chat_completions",
      models: ["kimi-k3"],
      defaultModel: "kimi-k3",
      searchMode: "web",
      nativeSearchDeclared: false,
      advancedConfig: {},
    },
  },
  {
    id: "official-kimi-coding",
    providerName: "Kimi for Coding",
    modelName: "kimi-for-coding",
    icon: icons.kimi,
    draft: {
      id: "official-kimi-coding",
      displayName: "Kimi for Coding",
      note: t("Kimi Code 会员接口；使用专属会员 Key，并保留真实客户端身份。", "Kimi Code subscription endpoint. Use its subscription key and preserve the real client identity."),
      websiteURL: "https://www.kimi.com/code",
      endpointURL: "https://api.kimi.com/coding/v1/chat/completions",
      transport: "openai_chat_completions",
      models: ["kimi-for-coding", "k3", "k3-256k", "kimi-for-coding-highspeed"],
      defaultModel: "kimi-for-coding",
      searchMode: "web",
      nativeSearchDeclared: false,
      advancedConfig: { request_headers: { "User-Agent": "Traverse-Board" } },
    },
  },
  {
    id: "opencode-go",
    providerName: "OpenCode Go",
    modelName: "gpt-5.6-luna",
    icon: icons.opencode,
    draft: {
      id: "opencode-go",
      displayName: "OpenCode Go",
      note: t("OpenCode Go 订阅网关；请使用 Go API Key。OpenCode Zen 请单独配置对应路由。", "OpenCode Go subscription gateway. Use a Go API key. Configure OpenCode Zen routes separately."),
      websiteURL: "https://opencode.ai/",
      endpointURL: "https://opencode.ai/zen/go/v1/responses",
      transport: "openai_responses",
      models: ["gpt-5.6-luna", "grok-4.6"],
      defaultModel: "gpt-5.6-luna",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
      advancedConfig: {},
    },
  },
] as const;

export const v2ModelProviderPresets = localizedModelProviderPresets((chinese) => chinese);
