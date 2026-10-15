import { isValidElement } from "react";
import { describe, expect, it } from "vitest";
import { v2ModelProviderPresets } from "./model-provider-presets";

describe("v2 model provider presets", () => {
  it("keeps the supported API providers in product order", () => {
    expect(v2ModelProviderPresets).toHaveLength(10);
    expect(v2ModelProviderPresets.map((preset) => preset.providerName)).toEqual([
      "Claude",
      "OpenAI",
      "DeepSeek",
      "Gemini",
      "Grok",
      "MiniMax",
      "MiMo",
      "Kimi",
      "Kimi for Coding",
      "OpenCode Go",
    ]);
    expect(v2ModelProviderPresets.every((preset) => isValidElement(preset.icon))).toBe(true);
  });

  it("uses exact official endpoints and supported transports", () => {
    const byID = new Map(v2ModelProviderPresets.map((preset) => [preset.id, preset]));
    expect(byID.get("official-anthropic")?.draft).toMatchObject({
      endpointURL: "https://api.anthropic.com/v1/messages",
      transport: "anthropic_messages",
      defaultModel: "claude-sonnet-5",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
    });
    expect(byID.get("official-openai")?.draft).toMatchObject({
      endpointURL: "https://api.openai.com/v1/responses",
      transport: "openai_responses",
      defaultModel: "gpt-6.1-sol",
      models: ["gpt-6.1-sol", "gpt-6-astra", "gpt-6-luna"],
      searchMode: "provider_native",
      nativeSearchDeclared: true,
    });
    expect(byID.get("official-deepseek")?.draft).toMatchObject({
      endpointURL: "https://api.deepseek.com/responses",
      transport: "openai_responses",
      models: ["deepseek-v4-flash", "deepseek-v4-pro"],
      searchMode: "web",
      nativeSearchDeclared: false,
      advancedConfig: { request_body: { reasoning: { effort: "none" } } },
    });
    expect(byID.get("official-google-gemini")?.draft).toMatchObject({
      endpointURL: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
      transport: "openai_chat_completions",
      defaultModel: "gemini-3.7-flash",
      searchMode: "web",
    });
    expect(byID.get("official-xai")?.draft).toMatchObject({
      endpointURL: "https://api.x.ai/v1/responses",
      transport: "openai_responses",
      defaultModel: "grok-4.6",
      searchMode: "provider_native",
      nativeSearchDeclared: true,
    });
    expect(byID.get("official-minimax")?.draft).toMatchObject({
      endpointURL: "https://api.minimax.io/v1/responses",
      defaultModel: "MiniMax-M3",
    });
    expect(byID.get("official-mimo")?.draft).toMatchObject({
      endpointURL: "https://api.xiaomimimo.com/v1/responses",
      models: ["mimo-v2.5-pro", "mimo-v2.5"],
    });
    expect(byID.get("opencode-go")?.draft).toMatchObject({
      endpointURL: "https://opencode.ai/zen/go/v1/responses",
      transport: "openai_responses",
      defaultModel: "gpt-5.6-luna",
    });
  });

  it("keeps Kimi platform and Kimi for Coding as separate credentials and routes", () => {
    const kimi = v2ModelProviderPresets.find((preset) => preset.id === "official-kimi");
    const coding = v2ModelProviderPresets.find((preset) => preset.id === "official-kimi-coding");

    expect(kimi).toMatchObject({ modelName: "kimi-k3" });
    expect(kimi?.draft).toMatchObject({
      endpointURL: "https://api.moonshot.ai/v1/chat/completions",
      models: ["kimi-k3"],
      searchMode: "web",
    });
    expect(coding).toMatchObject({ modelName: "kimi-for-coding" });
    expect(coding?.draft).toMatchObject({
      endpointURL: "https://api.kimi.com/coding/v1/chat/completions",
      models: ["kimi-for-coding", "k3", "k3-256k", "kimi-for-coding-highspeed"],
      searchMode: "web",
      advancedConfig: { request_headers: { "User-Agent": "Traverse-Board" } },
    });
    expect(kimi?.draft?.id).not.toBe(coding?.draft?.id);
  });

  it("offers an editable draft for every listed provider", () => {
    expect(v2ModelProviderPresets.every((preset) => preset.draft !== undefined)).toBe(true);
  });
});
