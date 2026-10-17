import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { useConnectionStore } from "../state/connection";
import { V2Workbench } from "./app";

const settingsModule = vi.hoisted(() => {
  let reject!: (error: Error) => void;
  let resolve!: () => void;
  const promise = new Promise<void>((ready, fail) => { resolve = ready; reject = fail; });
  return { promise, resolve, reject };
});

vi.mock("./components/settings", async () => {
  await settingsModule.promise;
  return { V2Settings: () => <main>设置模块</main> };
});
vi.mock("./components/inspector-home", () => ({
  V2InspectorHome: () => <main><h1>Inspector 首页模块</h1></main>,
}));

afterEach(async () => {
  cleanup();
  await act(async () => {
    settingsModule.resolve();
    await settingsModule.promise.catch(() => undefined);
  });
  vi.restoreAllMocks();
  useConnectionStore.getState().disconnect();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
});

it("keeps navigation and the draft available when an advanced module fails to load", async () => {
  // React reports a caught lazy error to the console even when its boundary recovers.
  vi.spyOn(console, "error").mockImplementation(() => undefined);
  window.history.replaceState({}, "", "#/new");
  const workspace = { id: "workspace-load-error", name: "Load error workspace", created_at: "2026-10-06T00:00:00Z" };
  const client = {
    hasThreadControl: true,
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
      page: { limit: 100 }, requestID: "request-load-error" })),
    createThread: vi.fn(), submitThreadTurn: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(),
  } as unknown as APIClient;
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2Workbench client={client} />
  </QueryClientProvider>);
  const user = userEvent.setup();
  const composer = await screen.findByRole("textbox", { name: "开始新对话" });
  await waitFor(() => expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id));
  await user.type(composer, "模块失败后继续编辑的需求");

  await user.click(screen.getByRole("button", { name: "设置" }));
  expect(await screen.findByText("正在加载设置…")).toHaveAttribute("role", "status");
  await act(async () => { settingsModule.reject(new Error("fixture chunk load failure")); });
  expect(await screen.findByRole("alert")).toHaveTextContent("设置加载失败。");
  expect(screen.getByRole("button", { name: "返回应用" })).toBeEnabled();

  await user.click(screen.getByRole("button", { name: "返回应用" }));
  const restoredComposer = await screen.findByRole("textbox", { name: "开始新对话" });
  expect(restoredComposer).toHaveValue("模块失败后继续编辑的需求");
  expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id);
  await user.type(restoredComposer, "，还能继续修改");
  await user.click(screen.getByRole("button", { name: "观察与记录" }));
  expect(await screen.findByRole("heading", { name: "Inspector 首页模块" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "观察与记录" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("模块失败后继续编辑的需求，还能继续修改");
  expect(window.location.hash).toBe("#/new");
  for (const mutation of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) {
    expect(mutation).not.toHaveBeenCalled();
  }
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
