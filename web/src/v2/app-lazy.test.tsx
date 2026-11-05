import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { useConnectionStore } from "../state/connection";
import { LocaleProvider } from "../lib/locale";
import { V2Workbench } from "./app";

const modules = vi.hoisted(() => {
  function gate() {
    let resolve!: () => void;
    const promise = new Promise<void>((ready) => { resolve = ready; });
    return { promise, resolve };
  }
  return { settings: gate(), history: gate(), run: gate(), session: gate(), automations: gate(),
    loads: { settings: 0, history: 0, run: 0, session: 0, automations: 0 } };
});

vi.mock("./components/settings", async () => {
  modules.loads.settings += 1;
  await modules.settings.promise;
  return { V2Settings: ({ section }: { section: string }) => <main><h1>设置模块 {section}</h1></main> };
});
vi.mock("./components/history-page", async () => {
  modules.loads.history += 1;
  await modules.history.promise;
  return { V2HistoryPage: ({ onOpenRecord }: { onOpenRecord: (kind: "run" | "session", id: string) => void }) =>
    <main><h1>历史页面模块</h1><button type="button" onClick={() => onOpenRecord("run", "run-saved")}>打开已保存执行</button>
      <button type="button" onClick={() => onOpenRecord("session", "session-saved")}>打开已保存会话</button></main> };
});
vi.mock("./components/run-details-page", async () => {
  modules.loads.run += 1;
  await modules.run.promise;
  return { V2RunDetailsPage: ({ resourceID }: { resourceID: string }) =>
    <main><h1>运行详情模块</h1><output aria-label="检查记录">{resourceID}</output></main> };
});

vi.mock("./components/session-details-page", async () => {
  modules.loads.session += 1;
  await modules.session.promise;
  return { V2SessionDetailsPage: ({ resourceID }: { resourceID: string }) => <main><h1>会话详情模块</h1><output aria-label="会话记录">{resourceID}</output></main> };
});
vi.mock("./components/automations-page", async () => {
  modules.loads.automations += 1;
  await modules.automations.promise;
  return { V2AutomationsPage: () => <main><h1>自动化模块</h1></main> };
});

afterEach(async () => {
  cleanup();
  await act(async () => {
    modules.settings.resolve();
    modules.history.resolve();
    modules.run.resolve();
    modules.session.resolve();
    modules.automations.resolve();
    await Promise.all([modules.settings.promise, modules.history.promise, modules.run.promise, modules.session.promise, modules.automations.promise]);
  });
  useConnectionStore.getState().disconnect();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
});

it("loads each advanced surface on first use while keeping drafts, focus, and later navigation", async () => {
  window.history.replaceState({}, "", "#/new");
  const workspace = { id: "workspace-lazy", name: "Lazy workspace", created_at: "2026-10-06T00:00:00Z" };
  const client = {
    hasThreadControl: true,
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
      page: { limit: 100 }, requestID: "request-lazy" })),
    createThread: vi.fn(), submitThreadTurn: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(),
  } as unknown as APIClient;
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <LocaleProvider><V2Workbench client={client} /></LocaleProvider>
  </QueryClientProvider>);
  const user = userEvent.setup();
  const composer = await screen.findByRole("textbox", { name: "开始新对话" });
  await waitFor(() => expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id));
  await user.type(composer, "切换页面后继续保留的需求");
  expect(modules.loads).toEqual({ settings: 0, history: 0, run: 0, session: 0, automations: 0 });

  await user.click(screen.getByRole("button", { name: "设置" }));
  expect(await screen.findByText("正在加载设置…")).toHaveAttribute("role", "status");
  const appearance = screen.getByRole("button", { name: "外观" });
  await user.click(appearance);
  expect(appearance).toHaveFocus();
  await act(async () => { modules.settings.resolve(); });
  expect(await screen.findByRole("heading", { name: "设置模块 appearance" })).toBeInTheDocument();
  expect(appearance).toHaveFocus();
  expect(modules.loads).toEqual({ settings: 1, history: 0, run: 0, session: 0, automations: 0 });
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("切换页面后继续保留的需求");

  const history = screen.getByRole("button", { name: "历史记录" });
  await user.click(history);
  expect(await screen.findByText("正在加载历史记录…")).toHaveAttribute("role", "status");
  expect(history).toHaveFocus();
  await act(async () => { modules.history.resolve(); });
  expect(await screen.findByRole("heading", { name: "历史页面模块" })).toBeInTheDocument();
  expect(history).toHaveFocus();
  expect(modules.loads).toEqual({ settings: 1, history: 1, run: 0, session: 0, automations: 0 });

  await user.click(screen.getByRole("button", { name: "打开已保存执行" }));
  expect(await screen.findByText("正在加载运行记录…")).toHaveAttribute("role", "status");
  await user.click(screen.getByRole("button", { name: "新对话" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("切换页面后继续保留的需求");
  await act(async () => { modules.run.resolve(); });
  expect(window.location.hash).toBe("#/new");
  expect(screen.queryByRole("heading", { name: "运行详情模块" })).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "历史记录" }));
  await user.click(await screen.findByRole("button", { name: "打开已保存执行" }));
  expect(await screen.findByLabelText("检查记录")).toHaveTextContent("run-saved");
  expect(window.location.hash).toBe("#/runs/run-saved");
  expect(modules.loads).toEqual({ settings: 1, history: 1, run: 1, session: 0, automations: 0 });

  await user.click(screen.getByRole("button", { name: "历史记录" }));
  await user.click(await screen.findByRole("button", { name: "打开已保存会话" }));
  expect(await screen.findByText("正在加载会话记录…")).toHaveAttribute("role", "status");
  await act(async () => { modules.session.resolve(); });
  expect(await screen.findByLabelText("会话记录")).toHaveTextContent("session-saved");
  expect(window.location.hash).toBe("#/sessions/session-saved");
  expect(modules.loads).toEqual({ settings: 1, history: 1, run: 1, session: 1, automations: 0 });

  await user.click(screen.getByRole("button", { name: "自动化" }));
  expect(await screen.findByText("正在加载自动化…")).toHaveAttribute("role", "status");
  await act(async () => { modules.automations.resolve(); });
  expect(await screen.findByRole("heading", { name: "自动化模块" })).toBeVisible();
  expect(window.location.hash).toBe("#/automations");
  expect(modules.loads).toEqual({ settings: 1, history: 1, run: 1, session: 1, automations: 1 });
  for (const mutation of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) {
    expect(mutation).not.toHaveBeenCalled();
  }
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
});
