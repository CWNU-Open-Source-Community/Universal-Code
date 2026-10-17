import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ProjectHooksView } from "../../api/project-hooks";
import { LocaleProvider } from "../../lib/locale";
import { ProjectHooks } from "./project-hooks";

function hookView(overrides: Partial<ProjectHooksView> = {}): ProjectHooksView {
  return { version: "project-hooks.v1", thread_id: "thread-1", workspace_id: "workspace-1", fingerprint: "a".repeat(64), trusted: false,
    revision: 0, matches_task: true, state: "needs_review", message: "首次使用前请审阅这组项目动作。",
    hooks: [{ event: "after_edit", command_id: "format.go", target: "已应用的 .go 文件" }, { event: "before_finish", command_id: "test.npm", target: "." }], ...overrides };
}
function setup(initial = hookView(), control = true) {
  let current = initial;
  const client = { baseURL: "/api/v1", hasControl: control,
    get: vi.fn().mockImplementation(() => Promise.resolve(current)),
    postControl: vi.fn().mockImplementation((path: string) => {
      const trusted = path.endsWith("/trust");
      current = { ...current, trusted, state: trusted ? "trusted" : "disabled", revision: current.revision + 1 };
      return Promise.resolve(current);
    }) };
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = (threadID = "thread-1") => <LocaleProvider><QueryClientProvider client={queries}>
    <ProjectHooks client={client as unknown as APIClient} threadID={threadID} />
  </QueryClientProvider></LocaleProvider>;
  return { client, queries, ui, setCurrent: (value: ProjectHooksView) => { current = value; } };
}

beforeEach(() => localStorage.clear());

it("reviews fixed actions once and allows disabling the trusted configuration", async () => {
  const { client, ui } = setup();
  render(ui());
  expect(await screen.findByText("format.go")).toBeVisible();
  expect(screen.getByText("编辑后格式化")).toBeVisible();
  expect(screen.getByText("结束前测试")).toBeVisible();
  expect(screen.getByText("已应用的 .go 文件")).toBeVisible();
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "信任当前配置" }));
  await waitFor(() => expect(client.postControl).toHaveBeenCalledWith("/threads/thread-1/project-hooks/trust",
    { version: "project-hooks.v1", expected_fingerprint: "a".repeat(64), expected_revision: 0 }, expect.any(String)));
  await waitFor(() => expect(screen.getByRole("button", { name: "停用自动动作" })).toBeEnabled());
  expect(screen.queryByRole("button", { name: "信任当前配置" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "停用自动动作" }));
  expect(await screen.findByRole("button", { name: "重新信任当前配置" })).toBeInTheDocument();
  expect(client.postControl.mock.calls[1][1]).toEqual({ version: "project-hooks.v1", expected_fingerprint: "a".repeat(64), expected_revision: 1 });
});

it("reuses existing trust without another confirmation", async () => {
  const { client, ui } = setup(hookView({ trusted: true, state: "trusted", revision: 4 }));
  render(ui());
  await screen.findByRole("button", { name: "停用自动动作" });
  expect(screen.queryByRole("button", { name: /信任.*配置/ })).not.toBeInTheDocument();
  expect(client.postControl).not.toHaveBeenCalled();
});

it("shows changed configuration, its target and the next-task scope before explicit confirmation", async () => {
  const { client, ui } = setup(hookView({ state: "changed", fingerprint: "b".repeat(64), revision: 5, matches_task: false }));
  render(ui());
  const trust = await screen.findByRole("button", { name: "信任变更后的配置" });
  expect(screen.getByText(/信任将在新任务中生效/)).toBeVisible();
  expect(client.postControl).not.toHaveBeenCalled();
  fireEvent.click(trust);
  await waitFor(() => expect(client.postControl.mock.calls[0][1]).toEqual({ version: "project-hooks.v1", expected_fingerprint: "b".repeat(64), expected_revision: 5 }));
});

it("refreshes after a configuration conflict and waits for a new click", async () => {
  const { client, ui, setCurrent } = setup();
  client.postControl.mockImplementationOnce(() => {
    setCurrent(hookView({ state: "changed", fingerprint: "c".repeat(64), revision: 6 }));
    return Promise.reject(new APIRequestError("配置已变化，请刷新后审阅。", "CONFLICT", 409));
  });
  render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "信任当前配置" }));
  await screen.findByRole("button", { name: "信任变更后的配置" });
  expect(screen.getByText("配置已变化，请刷新后审阅。")).toBeVisible();
  expect(client.postControl).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "信任变更后的配置" }));
  await waitFor(() => expect(client.postControl).toHaveBeenCalledTimes(2));
  expect(client.postControl.mock.calls[1][1].expected_fingerprint).toBe("c".repeat(64));
});

it("keeps a late review response in its original task", async () => {
  const { client, ui, setCurrent } = setup();
  let finish!: (value: ProjectHooksView) => void;
  client.postControl.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const { rerender } = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "信任当前配置" }));
  await waitFor(() => expect(client.postControl).toHaveBeenCalledTimes(1));
  setCurrent(hookView({ thread_id: "thread-2", fingerprint: "d".repeat(64), state: "changed", revision: 8 }));
  rerender(ui("thread-2"));
  await screen.findByRole("button", { name: "信任变更后的配置" });
  await act(async () => finish(hookView({ trusted: true, state: "trusted", revision: 1 })));
  expect(screen.getByRole("button", { name: "信任变更后的配置" })).toBeVisible();
});

it("shows unconfigured and read-only states without granting trust", async () => {
  const { ui } = setup(hookView({ fingerprint: "", hooks: [], state: "not_configured" }), false);
  const { unmount } = render(ui());
  expect(await screen.findByText("尚未配置")).toBeVisible();
  expect(screen.queryByRole("button", { name: /信任/ })).not.toBeInTheDocument();
  unmount();
  const readonly = setup(hookView(), false);
  render(readonly.ui());
  expect(await screen.findByRole("button", { name: "信任当前配置" })).toBeDisabled();
  expect(readonly.client.postControl).not.toHaveBeenCalled();
});
