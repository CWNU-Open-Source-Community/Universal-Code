/// <reference types="node" />

import { readFileSync } from "node:fs";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Circle, Hexagon } from "lucide-react";
import type { APIClient } from "../../api/client";
import { V2ModelSettings, type V2ModelProviderPreset } from "./model-settings";

const modelStyles = readFileSync("src/v2/components/settings.css", "utf8");
const modelCardRule = /\.v2-model-card\s*\{(?<body>[\s\S]*?)\}/u.exec(modelStyles)?.groups?.body ?? "";
const modelCardIconRule = /\.v2-model-card-icon\s*\{(?<body>[\s\S]*?)\}/u.exec(modelStyles)?.groups?.body ?? "";

const presets: V2ModelProviderPreset[] = [
  {
    id: "openai",
    providerName: "OpenAI",
    modelName: "GPT-5",
    icon: <Circle data-testid="openai-mark" />,
  },
  {
    id: "anthropic",
    providerName: "Anthropic",
    modelName: "Claude Opus",
    icon: <Hexagon data-testid="anthropic-mark" />,
    credentialConfigured: true,
  },
];

function client(): APIClient {
  return {
    hasProviderDefinitions: true,
    hasProviderCredentials: true,
    providerDefinitions: vi.fn().mockResolvedValue({
      version: "provider_definition_collection.v1", revision: 0, providers: [],
    }),
    providerCredentialStatuses: vi.fn().mockResolvedValue({
      protocol_version: "provider_credential.v1", items: [],
    }),
  } as unknown as APIClient;
}

function renderSettings(onSelectPreset = vi.fn()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={queryClient}>
    <V2ModelSettings client={client()} onSelectPreset={onSelectPreset} presets={presets} />
  </QueryClientProvider>);
  return { ...view, onSelectPreset };
}

describe("V2 model settings", () => {
  it("keeps custom configuration first and renders concise two-column provider cards", () => {
    renderSettings();

    const list = screen.getByRole("list", { name: "模型供应商" });
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(4);
    expect(within(items[0]).getByRole("button", { name: "打开自定义模型配置" }))
      .toHaveTextContent("自定义配置自定义模型");
    expect(within(items[2]).getByRole("button", {
      name: "OpenAI，GPT-5，尚未保存 API Key",
    })).toHaveTextContent("OpenAIGPT-5");
    expect(within(items[3]).getByRole("button", {
      name: "Anthropic，Claude Opus，已保存 API Key",
    })).toHaveTextContent("AnthropicClaude Opus");
    expect(within(items[2]).getByText("尚未保存 API Key")).toBeVisible();
    expect(within(items[3]).getByText("已保存 API Key")).toBeVisible();
    expect(within(items[1]).getByRole("button", { name: /ChatGPT，订阅账户/u }))
      .toHaveTextContent("ChatGPT订阅账户");
    expect(screen.queryByText("需要 API Key")).not.toBeInTheDocument();
    expect(screen.queryByText("已接入")).not.toBeInTheDocument();
    const openAIButton = screen.getByRole("button", { name: /OpenAI，GPT-5/u });
    expect(openAIButton).not.toHaveAttribute("title");
    expect(openAIButton).toHaveAttribute("data-model-provider-id", "openai");

    expect(document.querySelector("style[data-v2-model-settings]")).toBeNull();
    expect(list.querySelector("[style]")).toBeNull();
    expect(modelStyles).toContain("grid-template-columns: repeat(2, minmax(0, 1fr))");
    expect(modelStyles).toContain("@media (max-width: 680px)");
    expect(modelStyles).toContain("grid-template-columns: minmax(0, 1fr)");
  });

  it("exposes white glass icon wells as decoration without duplicating accessible names", () => {
    renderSettings();

    const openAIButton = screen.getByRole("button", { name: /OpenAI，GPT-5/u });
    const icon = within(openAIButton).getByTestId("openai-mark");
    expect(icon.closest(".v2-model-card-icon")).toHaveAttribute("aria-hidden", "true");
    expect(modelStyles).toContain("backdrop-filter: blur(18px)");
    expect(modelStyles).toContain("prefers-reduced-transparency: reduce");
    expect(modelStyles).toContain("background: var(--v2-control)");
  });

  it("clips the composited icon well to the card's inner left radius", () => {
    renderSettings();

    expect(modelCardRule).toContain("-webkit-appearance: none");
    expect(modelCardRule).toContain("appearance: none");
    expect(modelCardRule).toContain("background-clip: padding-box");
    expect(modelCardRule).toContain("overflow: hidden");
    expect(modelCardIconRule).toContain("border-radius: 15px 0 0 15px");
    expect(modelCardIconRule).toContain("overflow: hidden");
  });

  it("falls back to solid system colors for contrast preferences without motion transforms", () => {
    renderSettings();

    expect(modelStyles).toContain("@media (prefers-contrast: more)");
    expect(modelStyles).toContain("@media (forced-colors: active)");
    expect(modelStyles).toContain("background: Canvas");
    expect(modelStyles).toContain("border: 2px solid ButtonText");
    expect(modelStyles).toContain("background: Highlight");
    expect(modelStyles).toContain("color: HighlightText");
    expect(modelStyles).toContain("forced-color-adjust: auto");
    expect(modelStyles).toContain("transform: none !important");
  });

  it("activates a preset with the keyboard and passes its native button trigger", async () => {
    const user = userEvent.setup();
    const onSelectPreset = vi.fn();
    renderSettings(onSelectPreset);

    await user.tab();
    expect(screen.getByRole("button", { name: "打开自定义模型配置" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: /ChatGPT，订阅账户/u })).toHaveFocus();
    await user.tab();
    const openAIButton = screen.getByRole("button", { name: /OpenAI，GPT-5/u });
    expect(openAIButton).toHaveFocus();
    await user.keyboard("{Enter}");

    expect(onSelectPreset).toHaveBeenCalledTimes(1);
    expect(onSelectPreset).toHaveBeenCalledWith(presets[0], openAIButton);
  });

  it("reuses the existing custom Provider settings and restores focus on return", async () => {
    const user = userEvent.setup();
    renderSettings();
    const customButton = screen.getByRole("button", { name: "打开自定义模型配置" });

    await user.click(customButton);
    expect(await screen.findByRole("heading", { name: "自定义配置" })).toBeInTheDocument();
    const backButton = screen.getByRole("button", { name: "返回模型选择" });
    await waitFor(() => expect(backButton).toHaveFocus());

    await user.keyboard("{Enter}");
    expect(screen.getByRole("heading", { name: "模型" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "打开自定义模型配置" }))
      .toHaveFocus());
  });
});
