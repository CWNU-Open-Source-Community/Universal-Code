import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { ProjectHeader } from "./project-header";

afterEach(cleanup);
beforeEach(() => localStorage.setItem("prayu.locale.v1", "zh-CN"));
const workspace = { id: "design", name: "页面设计", created_at: "2026-10-20T00:00:00Z" };
it("fetches full project details only when opened and exposes editing to keyboard users", async () => {
  const getWorkspaceProject = vi.fn().mockResolvedValue({ ...workspace, directory_path: "D:\\Projects\\design", thread_count: 140, active_thread_count: 2 });
  const onEdit = vi.fn();
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider>
    <ProjectHeader client={{ hasControl: true, getWorkspaceProject } as unknown as APIClient} workspace={workspace}
      label={workspace.name} expanded onToggle={vi.fn()} onEdit={onEdit} />
  </LocaleProvider></QueryClientProvider>);
  expect(getWorkspaceProject).not.toHaveBeenCalled();
  const user = userEvent.setup();
  const trigger = screen.getByRole("button", { name: "页面设计 项目信息" });
  trigger.focus();
  await user.keyboard("{Enter}");
  const card = screen.getByRole("dialog", { name: "页面设计 项目信息" });
  expect(await within(card).findByText("140 个聊天 · 2 个活跃聊天")).toBeInTheDocument();
  expect(within(card).getByText("D:\\Projects\\design")).toBeInTheDocument();
  await waitFor(() => expect(within(card).getByRole("button", { name: "编辑项目" })).toHaveFocus());
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  await user.keyboard("{Enter}");
  await user.click(screen.getByRole("button", { name: "编辑项目" }));
  expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: workspace.id }));
  expect(trigger).toHaveFocus();
});

it("opens on hover and keeps the interactive card open while the pointer enters it", async () => {
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider>
    <ProjectHeader workspace={workspace} label={workspace.name} expanded onToggle={vi.fn()} />
  </LocaleProvider></QueryClientProvider>);
  const user = userEvent.setup();
  await user.hover(screen.getByRole("button", { name: "页面设计" }));
  const card = await screen.findByRole("dialog", { name: "页面设计 项目信息" });
  await user.hover(card);
  expect(card).toHaveTextContent("连接操作令牌");
  await user.unhover(card);
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});
