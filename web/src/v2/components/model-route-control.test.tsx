import type { ReactElement } from "react";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { chatPreferencesKey, saveChatPreferences } from "../chat-preferences";
import { V2ModelRouteControl, type V2ModelRouteCatalog, type V2ThreadModelRoute } from "./model-route-control";

const routeStyles = readFileSync("src/v2/styles.css", "utf8");

afterEach(() => localStorage.removeItem(chatPreferencesKey));

const catalog: V2ModelRouteCatalog = {
  protocol_version: "model_route_catalog.v1",
  generation: 7,
  routes: [
    { provider_id: "official-deepseek", provider_name: "DeepSeek", model: "deepseek-v4-flash",
	  definition_revision: 1,
      enabled: true, credential_status: "configured", qualification_status: "verified",
      harness_ready: true, selectable: true, unavailable_reason: "", default_for_routes: ["code"] },
    { provider_id: "official-deepseek", provider_name: "DeepSeek", model: "deepseek-v4-pro",
	  definition_revision: 1,
      enabled: true, credential_status: "configured", qualification_status: "verified",
      harness_ready: true, selectable: true, unavailable_reason: "", default_for_routes: [] },
    { provider_id: "official-openai", provider_name: "OpenAI", model: "gpt-5.6-terra",
	  definition_revision: 1,
      enabled: true, credential_status: "not_configured", qualification_status: "qualification_required",
      harness_ready: false, selectable: false, unavailable_reason: "credential_not_configured",
      default_for_routes: [] },
  ],
};

const current: V2ThreadModelRoute = {
  protocol_version: "thread_model_route.v1",
  thread_id: "thread-1",
  provider: "official-deepseek",
  model: "deepseek-v4-flash",
  source: "thread_preference",
  applies_to: "next_run",
  active_run_unchanged: true,
  replayed: false,
};

type TestRouteClient = APIClient & {
  availableModelRoutes: ReturnType<typeof vi.fn>;
  threadModelRoute: ReturnType<typeof vi.fn>;
  selectThreadModelRoute: ReturnType<typeof vi.fn>;
};

function routeClient(overrides: Record<string, unknown> = {}): TestRouteClient {
  return {
    hasModelControl: true,
    availableModelRoutes: vi.fn().mockResolvedValue(catalog),
    threadModelRoute: vi.fn().mockResolvedValue(current),
    selectThreadModelRoute: vi.fn().mockImplementation(async (_threadID: string, body: {
      action: "select" | "reset"; provider?: string; model?: string;
    }) => ({ ...current, provider: body.provider ?? "official-deepseek",
      model: body.model ?? "deepseek-v4-flash", source: body.action === "reset" ? "default" : "thread_preference",
      active_run_unchanged: true })),
    ...overrides,
  } as unknown as TestRouteClient;
}

function renderControl(client: TestRouteClient = routeClient(), options: {
  runActive?: boolean; onManageModels?: () => void;
} = {}) {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  const onManageModels = options.onManageModels ?? vi.fn();
  render(<QueryClientProvider client={queryClient}>
    <V2ModelRouteControl client={client} onManageModels={onManageModels}
      runActive={options.runActive} threadID="thread-1" />
  </QueryClientProvider>);
  return { client, onManageModels };
}

describe("V2ModelRouteControl", () => {
  it.each(["ctrlKey", "metaKey"])("opens the model list with %s without selecting a route or changing the draft", async (modifier) => {
    const client = routeClient();
    const onManageModels = vi.fn();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ModelRouteControl client={client} threadID="thread-1" onManageModels={onManageModels} />
      <textarea aria-label="下一条消息" defaultValue="保留这份草稿" />
    </QueryClientProvider>);
    const composer = screen.getByRole("textbox", { name: "下一条消息" });
    act(() => composer.focus());
    const event = new KeyboardEvent("keydown", { key: "M", code: "KeyM", [modifier]: true,
      shiftKey: true, bubbles: true, cancelable: true });
    fireEvent(composer, event);
    expect(event.defaultPrevented).toBe(true);
    const models = screen.getByRole("menu", { name: "选择模型路由" });
    expect(screen.queryByRole("menu", { name: "模型与响应设置" })).not.toBeInTheDocument();
    expect(within(models).getByRole("menuitem", { name: "返回模型设置" })).toHaveFocus();
    expect(await within(models).findByRole("menuitemradio", { name: "deepseek-v4-flash" })).toHaveAttribute("aria-checked", "true");
    expect(within(models).getByRole("menuitemradio", { name: /^gpt-5\.6-terra/u })).toBeDisabled();
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
    expect(onManageModels).not.toHaveBeenCalled();
    expect(composer).toHaveValue("保留这份草稿");
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /模型路由/u })).toHaveFocus();
  });

  it("uses a remapped model shortcut immediately and leaves an unassigned shortcut available", () => {
    renderControl();
    const key = { key: "m", code: "KeyM", ctrlKey: true, shiftKey: true, cancelable: true };
    act(() => saveChatPreferences({ shortcuts: { modelPicker: "Mod+Shift+J" } }));
    fireEvent.keyDown(window, key);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    fireEvent.keyDown(window, { ...key, key: "j", code: "KeyJ" });
    expect(screen.getByRole("menu", { name: "选择模型路由" })).toBeInTheDocument();
    fireEvent.keyDown(window, { key: "Escape" });
    act(() => saveChatPreferences({ shortcuts: { modelPicker: "" } }));
    const event = new KeyboardEvent("keydown", key);
    fireEvent(window, event);
    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("does not open over another menu, a dialog, recording, editors, terminals or composition", () => {
    const { client } = renderControl();
    const key = { key: "m", code: "KeyM", ctrlKey: true, shiftKey: true };
    for (const className of ["monaco-editor", "xterm"]) {
      const editor = document.createElement("input"); editor.className = className; document.body.append(editor);
      fireEvent.keyDown(editor, key);
      editor.remove();
    }
    for (const role of ["dialog", "menu", "listbox"]) {
      const popup = document.createElement("div"); popup.setAttribute("role", role); document.body.append(popup);
      fireEvent.keyDown(window, key);
      popup.remove();
    }
    const recorder = document.createElement("button"); recorder.dataset.shortcutRecorder = "true"; document.body.append(recorder);
    fireEvent.keyDown(recorder, key);
    recorder.remove();
    const projectMenu = document.createElement("details");
    projectMenu.className = "v2-project-section-menu"; projectMenu.open = true; document.body.append(projectMenu);
    fireEvent.keyDown(window, key);
    projectMenu.remove();
    fireEvent.keyDown(window, { ...key, isComposing: true });
    fireEvent.keyDown(window, { ...key, keyCode: 229 });
    fireEvent.keyDown(window, { ...key, repeat: true });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(client.availableModelRoutes).not.toHaveBeenCalled();
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  });

  it("leaves the shortcut untouched when model control is unavailable", () => {
    const { client } = renderControl(routeClient({ hasModelControl: false }));
    const event = new KeyboardEvent("keydown", { key: "m", code: "KeyM", ctrlKey: true,
      shiftKey: true, cancelable: true });
    fireEvent(window, event);
    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(client.availableModelRoutes).not.toHaveBeenCalled();
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  });

  it("keeps only the model basename on the toolbar while retaining the full route and reasoning detail", async () => {
    const user = userEvent.setup();
    const client = routeClient({ threadModelRoute: vi.fn().mockResolvedValue({ ...current,
      provider: "custom-provider", model: "organization/model-release-2026" }) });
    renderControl(client);
    const trigger = await screen.findByRole("button", {
      name: "模型路由，当前 custom-provider · organization/model-release-2026；推理强度：随模型",
    });
    expect(trigger).toHaveTextContent(/^model-release-2026$/u);
    expect(trigger.querySelector(".lucide-zap")).toBeNull();
    expect(trigger).toHaveAttribute("title", "custom-provider · organization/model-release-2026；推理强度：随模型");
    expect(client.availableModelRoutes).not.toHaveBeenCalled();
    await user.click(trigger);
    const identity = screen.getByLabelText("当前模型完整路由");
    expect(within(identity).getByText("供应商")).toBeVisible();
    expect(within(identity).getByText("custom-provider")).toBeVisible();
    expect(within(identity).getByText("模型 ID")).toBeVisible();
    expect(within(identity).getByText("organization/model-release-2026")).toBeVisible();
    expect(screen.getByRole("menuitem", { name: /^模型/u })).toHaveTextContent("model-release-2026");
    expect(screen.getByRole("menuitem", { name: /^推理强度/u })).toBeDisabled();
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  });

  it.each([
    ["deepseek-v4-flash", "DeepSeek V4 Flash"],
    ["claude-sonnet-4-5-20250929", "Claude Sonnet 4.5"],
    ["gpt-5.6-terra", "GPT-5.6 Terra"],
    ["claude-sonnet-4-5-20250929-thinking", "Claude Sonnet 4.5 Thinking"],
    ["deepseek-v4-coder-thinking", "DeepSeek V4 Coder Thinking"],
    ["claude-sonnet-4-5-20250230-thinking", "Claude Sonnet 4.5 20250230 Thinking"],
  ])("shows a readable %s while keeping the exact full route visible and unchanged", async (model, friendly) => {
    const rawModel = `team/${model}`;
    const client = routeClient({ threadModelRoute: vi.fn().mockResolvedValue({ ...current,
      provider: "custom-provider", model: rawModel }) });
    renderControl(client); const user = userEvent.setup();
    const description = `custom-provider · ${rawModel}；推理强度：随模型`;
    const trigger = await screen.findByRole("button", { name: `模型路由，当前 ${description}` });
    expect(trigger).toHaveTextContent(friendly);
    expect(trigger).toHaveAttribute("title", description);
    await user.click(trigger);
    expect(within(screen.getByLabelText("当前模型完整路由")).getByText(rawModel)).toBeVisible();
    expect(screen.getByRole("menuitem", { name: /^模型/u })).toHaveTextContent(friendly);
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  });

  it("does not steal focus from typing after a pending model selection closes its menu", async () => {
    const frames = new Map<number, FrameRequestCallback>();
    let nextFrame = 0;
    vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((callback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation((id) => { frames.delete(id); });
    const flushFrames = () => {
      const pending = [...frames.values()];
      frames.clear();
      for (const callback of pending) callback(performance.now());
    };
    const client = routeClient();
    const onPendingRouteChange = vi.fn();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(<QueryClientProvider client={queryClient}>
      <V2ModelRouteControl client={client} threadID="" onManageModels={vi.fn()}
        onPendingRouteChange={onPendingRouteChange} />
      <textarea aria-label="首条消息" />
    </QueryClientProvider>);
    await user.click(screen.getByRole("button", { name: /模型路由/ }));
    act(flushFrames);
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    act(flushFrames);
    await user.click(await screen.findByRole("menuitemradio", { name: /deepseek-v4-pro/ }));
    expect(onPendingRouteChange).toHaveBeenCalledWith({ provider: "official-deepseek", model: "deepseek-v4-pro" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    const composer = screen.getByRole("textbox", { name: "首条消息" });
    await user.click(composer);
    act(flushFrames);
    await user.keyboard("first message");
    expect(composer).toHaveFocus();
    expect(composer).toHaveValue("first message");
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  });

  it("keeps a newer composer focus when an in-flight model change finishes", async () => {
    let resolve!: (value: V2ThreadModelRoute) => void;
    const pending = new Promise<V2ThreadModelRoute>((done) => { resolve = done; });
    const client = routeClient({ selectThreadModelRoute: vi.fn(() => pending) });
    const frames = new Map<number, FrameRequestCallback>();
    let nextFrame = 0;
    vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((callback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation((id) => { frames.delete(id); });
    const flushFrames = () => {
      const callbacks = [...frames.values()];
      frames.clear();
      for (const callback of callbacks) callback(performance.now());
    };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(<QueryClientProvider client={queryClient}>
      <V2ModelRouteControl client={client} threadID="thread-1" onManageModels={vi.fn()} />
      <textarea aria-label="下一条消息" />
    </QueryClientProvider>);
    await user.click(await screen.findByRole("button", { name: /模型路由/ }));
    act(flushFrames);
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    act(flushFrames);
    await user.click(await screen.findByRole("menuitemradio", { name: /deepseek-v4-pro/ }));
    const composer = screen.getByRole("textbox", { name: "下一条消息" });
    await user.click(composer);
    await act(async () => { resolve({ ...current, model: "deepseek-v4-pro" }); await pending; });
    await waitFor(() => expect(screen.getByRole("button", { name: /模型路由/ })).toHaveTextContent("DeepSeek V4 Pro"));
    act(flushFrames);
    await user.keyboard("keep this draft");
    expect(composer).toHaveFocus();
    expect(composer).toHaveValue("keep this draft");
    expect(client.selectThreadModelRoute).toHaveBeenCalledTimes(1);
  });

  it.each([false, true])("returns focus after the pending menu item loses focus (re-enabled before completion: %s)", async (reenabled) => {
    let resolve!: (value: V2ThreadModelRoute) => void;
    const pending = new Promise<V2ThreadModelRoute>((done) => { resolve = done; });
    const client = routeClient({ selectThreadModelRoute: vi.fn(() => pending) });
    const user = userEvent.setup();
    renderControl(client);
    const trigger = await screen.findByRole("button", { name: /模型路由/ });
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    const selected = await screen.findByRole("menuitemradio", { name: /deepseek-v4-pro/ });
    await user.click(selected);
    await waitFor(() => expect(selected).toBeDisabled());
    // Browsers may blur a focused button when it becomes disabled. Keep the
    // real pending mutation and emulate that focus event, without a timer.
    // jsdom keeps disabled controls focused even after blur(), so explicitly
    // reproduce the browser's body-focus state while the button stays disabled.
    document.body.tabIndex = -1;
    document.body.focus();
    document.body.removeAttribute("tabindex");
    expect(document.body).toHaveFocus();
    // Success/cache notification may enable the item before closeMenu runs.
    // Restoration must depend on the original focus owner, not this DOM flag.
    if (reenabled) selected.removeAttribute("disabled");
    await act(async () => { resolve({ ...current, model: "deepseek-v4-pro" }); await pending; });
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(trigger).toHaveTextContent("DeepSeek V4 Pro");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(client.selectThreadModelRoute).toHaveBeenCalledTimes(1);
  });

  it("does not let a queued menu-opening frame reclaim focus already moved to the composer", async () => {
    const frames: FrameRequestCallback[] = [];
    vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((callback) => { frames.push(callback); return frames.length; });
    vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation(() => undefined);
    const user = userEvent.setup(); const client = routeClient();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}>
      <V2ModelRouteControl client={client} threadID="thread-1" onManageModels={vi.fn()} />
      <textarea aria-label="下一条消息" />
    </QueryClientProvider>);
    await user.click(await screen.findByRole("button", { name: /模型路由/ }));
    const composer = screen.getByRole("textbox", { name: "下一条消息" });
    act(() => composer.focus());
    act(() => { for (const frame of frames.splice(0)) frame(performance.now()); });
    expect(composer).toHaveFocus();
    await user.keyboard("keep this draft");
    expect(composer).toHaveValue("keep this draft");
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  });

  it("retires a pending menu focus owner when keyboard or programmatic focus leaves for the composer", async () => {
    let resolve!: (value: V2ThreadModelRoute) => void;
    const pending = new Promise<V2ThreadModelRoute>((done) => { resolve = done; });
    const client = routeClient({ selectThreadModelRoute: vi.fn(() => pending) });
    const user = userEvent.setup();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ModelRouteControl client={client} threadID="thread-1" onManageModels={vi.fn()} />
      <textarea aria-label="下一条消息" />
    </QueryClientProvider>);
    const trigger = await screen.findByRole("button", { name: /模型路由/ });
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    await user.click(await screen.findByRole("menuitemradio", { name: /deepseek-v4-pro/ }));
    const composer = screen.getByRole("textbox", { name: "下一条消息" });
    // Move without mousedown, then blur. Body focus must not revive the old
    // menu owner after the operator has already moved outside the control.
    act(() => { composer.focus(); composer.blur(); });
    expect(document.body).toHaveFocus();
    await act(async () => { resolve({ ...current, model: "deepseek-v4-pro" }); await pending; });
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(document.body).toHaveFocus();
    expect(trigger).not.toHaveFocus();
    expect(trigger).toHaveTextContent("DeepSeek V4 Pro");
  });

  it("opens an anchored two-level menu and groups selectable and unavailable routes", async () => {
    const user = userEvent.setup();
    const controls = renderControl();
    const trigger = await screen.findByRole("button", {
      name: "模型路由，当前 DeepSeek · deepseek-v4-flash；推理强度：随模型",
    });

    expect(controls.client.availableModelRoutes).not.toHaveBeenCalled();
    await user.click(trigger);
    const settings = screen.getByRole("menu", { name: "模型与响应设置" });
    expect(within(screen.getByLabelText("当前模型完整路由")).getByText("DeepSeek（official-deepseek）")).toBeVisible();
    expect(within(settings).getByRole("menuitem", { name: /推理强度/ })).toBeDisabled();
    expect(within(settings).getByRole("menuitem", { name: /速度/ })).toBeDisabled();

    const modelRow = within(settings).getByRole("menuitem", { name: /^模型/ });
    modelRow.focus();
    await user.keyboard("{ArrowRight}");
    const routes = await screen.findByRole("menu", { name: "选择模型路由" });
    expect(controls.client.availableModelRoutes).toHaveBeenCalledTimes(1);
    expect(within(routes).getByRole("region", { name: "DeepSeek" })).toBeInTheDocument();
    const unavailable = within(routes).getByRole("menuitemradio", { name: /gpt-5.6-terra/ });
    expect(unavailable).toBeDisabled();
    expect(unavailable).toHaveAttribute("title", "API Key 尚未配置");
    expect(within(routes).getByRole("menuitemradio", { name: /deepseek-v4-flash/ }))
      .toHaveAttribute("aria-checked", "true");
  });

  it("selects a provider plus model for the next Run without mutating the active Run", async () => {
    const user = userEvent.setup();
    const controls = renderControl(routeClient(), { runActive: true });
    const trigger = await screen.findByRole("button", { name: /模型路由/ });
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    await user.click(await screen.findByRole("menuitemradio", { name: /deepseek-v4-pro/ }));

    await waitFor(() => expect(controls.client.selectThreadModelRoute).toHaveBeenCalledWith(
      "thread-1", expect.objectContaining({
        version: "thread_model_route_control.v1",
        action: "select",
        provider: "official-deepseek",
        model: "deepseek-v4-pro",
        requested_by: "desktop-ui",
      }),
    ));
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(trigger).toHaveTextContent("DeepSeek V4 Pro下一轮");
    expect(trigger).not.toHaveTextContent("DeepSeek ·");
    expect(trigger).toHaveAttribute("title", "DeepSeek · deepseek-v4-pro；推理强度：随模型");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("resets the thread override through the explicit reset action", async () => {
    const user = userEvent.setup();
    const controls = renderControl();
    await user.click(await screen.findByRole("button", { name: /模型路由/ }));
    await user.click(screen.getByRole("menuitem", { name: "重置为默认设置" }));

    await waitFor(() => expect(controls.client.selectThreadModelRoute).toHaveBeenCalledWith(
      "thread-1", expect.objectContaining({ action: "reset", requested_by: "desktop-ui" }),
    ));
    const body = vi.mocked(controls.client.selectThreadModelRoute).mock.calls[0]?.[1] as Record<string, unknown>;
    expect(body).not.toHaveProperty("provider");
    expect(body).not.toHaveProperty("model");
  });

  it("shows the backend's safe selection error instead of hiding the cause", async () => {
    const user = userEvent.setup();
    const client = routeClient({
      selectThreadModelRoute: vi.fn().mockRejectedValue(
        new Error("Harness 尚未完成该模型的能力验证"),
      ),
    });
    renderControl(client);
    await user.click(await screen.findByRole("button", { name: /模型路由/ }));
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    await user.click(await screen.findByRole("menuitemradio", { name: /deepseek-v4-pro/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Harness 尚未完成该模型的能力验证");
  });

  it("closes on Escape, restores focus, and keeps management in the model settings module", async () => {
    const user = userEvent.setup();
    const onManageModels = vi.fn();
    renderControl(routeClient(), { onManageModels });
    const trigger = await screen.findByRole("button", { name: /模型路由/ });

    await user.click(trigger);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());

    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    await user.click(await screen.findByRole("menuitem", { name: "管理模型供应商…" }));
    expect(onManageModels).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("dismisses on an outside pointer and supports arrow-key traversal", async () => {
    const user = userEvent.setup();
    renderControl();
    const trigger = await screen.findByRole("button", { name: /模型路由/ });
    await user.click(trigger);
    const first = screen.getByRole("menuitem", { name: /^模型/ });
    await waitFor(() => expect(first).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("menuitem", { name: "重置为默认设置" })).toHaveFocus();

    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("keeps the anchored material legible under reduced transparency and contrast modes", () => {
    expect(routeStyles).toContain(".v2-model-route-popover");
    expect(routeStyles).toContain("@media (prefers-reduced-transparency: reduce)");
    expect(routeStyles).toContain("@media (prefers-contrast: more)");
    expect(routeStyles).toContain("@media (forced-colors: active)");
    expect(routeStyles).toContain("backdrop-filter: none");
    expect(routeStyles).toContain("background: Canvas");
  });
});
/// <reference types="node" />

import { readFileSync } from "node:fs";

it("switches the open model menus while retaining route identities and requiring an explicit selection", async () => {
  const client = routeClient();
  const onManageModels = vi.fn();
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  function LanguageControl() {
    const { locale, setLocale } = useLocale();
    return <select aria-label="Test language" value={locale}
      onChange={(event) => setLocale(event.target.value as "zh-CN" | "en-US")}>
      <option value="zh-CN">中文</option><option value="en-US">English</option>
    </select>;
  }
  render(<QueryClientProvider client={queries}><LanguageControl />
    <V2ModelRouteControl client={client} onManageModels={onManageModels} threadID="thread-1" />
  </QueryClientProvider>);
  const user = userEvent.setup();
  const trigger = await screen.findByRole("button", { name: /模型路由.*deepseek-v4-flash/u });
  await user.click(trigger);
  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "en-US" } });
  expect(screen.getByRole("button", { name: /Model route, current.*deepseek-v4-flash/u })).toBe(trigger);
  expect(trigger).toHaveTextContent(/^DeepSeek V4 Flash$/u);
  expect(screen.getByRole("menu", { name: "Model and response settings" })).toBeInTheDocument();
  const identity = screen.getByLabelText("Full current model route");
  expect(within(identity).getByText("Model ID")).toBeInTheDocument();
  expect(within(identity).getByText("deepseek-v4-flash")).toBeInTheDocument();
  expect(within(identity).getByText("official-deepseek", { exact: false })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "Reset to defaults" })).toBeInTheDocument();
  await user.click(screen.getByRole("menuitem", { name: "ModelDeepSeek V4 Flash" }));
  const models = screen.getByRole("menu", { name: "Select model route" });
  const selected = await within(models).findByRole("menuitemradio", { name: "deepseek-v4-flash" });
  expect(selected).toHaveAttribute("aria-checked", "true");
  const unavailable = within(models).getByRole("menuitemradio", { name: /^gpt-5\.6-terra/u });
  expect(unavailable).toBeDisabled();
  expect(within(unavailable).getByText("API key not configured")).toBeInTheDocument();
  expect(models.textContent).not.toMatch(/[\u3400-\u9fff]/u);
  expect(client.selectThreadModelRoute).not.toHaveBeenCalled();
  expect(onManageModels).not.toHaveBeenCalled();

  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "zh-CN" } });
  expect(screen.getByRole("menu", { name: "选择模型路由" })).toBe(models);
  expect(unavailable).toBeDisabled();
  expect(within(unavailable).getByText("API Key 尚未配置")).toBeInTheDocument();
  expect(selected).toHaveAttribute("aria-checked", "true");
  expect(client.selectThreadModelRoute).not.toHaveBeenCalled();

  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "en-US" } });
  await user.click(screen.getByRole("menuitemradio", { name: "deepseek-v4-pro" }));
  await waitFor(() => expect(client.selectThreadModelRoute).toHaveBeenCalledWith("thread-1",
    expect.objectContaining({ action: "select", provider: "official-deepseek", model: "deepseek-v4-pro" })));
  queries.clear();
});

function render(ui: ReactElement) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider });
}
