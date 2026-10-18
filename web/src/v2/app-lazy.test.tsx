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
  return { settings: gate(), home: gate(), tools: gate(), loads: { settings: 0, home: 0, tools: 0 } };
});

vi.mock("./components/settings", async () => {
  modules.loads.settings += 1;
  await modules.settings.promise;
  return { V2Settings: ({ section }: { section: string }) => <main><h1>设置模块 {section}</h1></main> };
});
vi.mock("./components/inspector-home", async () => {
  modules.loads.home += 1;
  await modules.home.promise;
  return { V2InspectorHome: ({ onOpenTool }: { onOpenTool: (tool: "run", id: string) => void }) =>
    <main><h1>Inspector 首页模块</h1><button type="button" onClick={() => onOpenTool("run", "run-saved")}>打开已保存执行</button></main> };
});
vi.mock("./components/inspector-tools", async () => {
  modules.loads.tools += 1;
  await modules.tools.promise;
  return { V2InspectorTools: ({ resourceID }: { resourceID: string }) =>
    <main><h1>Inspector 工具模块</h1><output aria-label="检查记录">{resourceID}</output></main> };
});

afterEach(async () => {
  cleanup();
  await act(async () => {
    modules.settings.resolve();
    modules.home.resolve();
    modules.tools.resolve();
    await Promise.all([modules.settings.promise, modules.home.promise, modules.tools.promise]);
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
  expect(modules.loads).toEqual({ settings: 0, home: 0, tools: 0 });

  await user.click(screen.getByRole("button", { name: "设置" }));
  expect(await screen.findByText("正在加载设置…")).toHaveAttribute("role", "status");
  const appearance = screen.getByRole("button", { name: "外观" });
  await user.click(appearance);
  expect(appearance).toHaveFocus();
  await act(async () => { modules.settings.resolve(); });
  expect(await screen.findByRole("heading", { name: "设置模块 appearance" })).toBeInTheDocument();
  expect(appearance).toHaveFocus();
  expect(modules.loads).toEqual({ settings: 1, home: 0, tools: 0 });
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("切换页面后继续保留的需求");

  const inspector = screen.getByRole("button", { name: "观察与记录" });
  await user.click(inspector);
  expect(await screen.findByText("正在加载 Inspector…")).toHaveAttribute("role", "status");
  expect(inspector).toHaveFocus();
  await act(async () => { modules.home.resolve(); });
  expect(await screen.findByRole("heading", { name: "Inspector 首页模块" })).toBeInTheDocument();
  expect(inspector).toHaveFocus();
  expect(modules.loads).toEqual({ settings: 1, home: 1, tools: 0 });

  await user.click(screen.getByRole("button", { name: "打开已保存执行" }));
  expect(await screen.findByText("正在加载检查工具…")).toHaveAttribute("role", "status");
  await user.click(screen.getByRole("button", { name: "观察与记录" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("切换页面后继续保留的需求");
  await act(async () => { modules.tools.resolve(); });
  expect(window.location.hash).toBe("#/new");
  expect(screen.queryByRole("heading", { name: "Inspector 工具模块" })).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "观察与记录" }));
  await user.click(await screen.findByRole("button", { name: "打开已保存执行" }));
  expect(await screen.findByLabelText("检查记录")).toHaveTextContent("run-saved");
  expect(window.location.hash).toBe("#/new/inspector/runs/run-saved");
  expect(modules.loads).toEqual({ settings: 1, home: 1, tools: 1 });
  for (const mutation of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) {
    expect(mutation).not.toHaveBeenCalled();
  }
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
});
