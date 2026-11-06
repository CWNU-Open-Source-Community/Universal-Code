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
  expect(screen.queryByRole("button", { name: "复制项目目录" })).not.toBeInTheDocument();
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
  expect(within(card).queryByRole("button", { name: "复制项目目录" })).not.toBeInTheDocument();
  await user.unhover(card);
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});

it.each([
  ["zh-CN", "复制项目目录", "已复制项目目录", "编辑项目", "页面设计 项目信息"],
  ["en-US", "Copy project directory", "Project directory copied", "Edit project", "页面设计 project details"],
])("copies the complete project path from the keyboard in %s", async (locale, copyLabel, copiedLabel, editLabel, detailsLabel) => {
  localStorage.setItem("prayu.locale.v1", locale);
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const path = "D:\\团队 项目\\Release 2026\\完整目录";
  const getWorkspaceProject = vi.fn().mockResolvedValue({ ...workspace, directory_path: path, thread_count: 2, active_thread_count: 1 });
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider>
    <ProjectHeader client={{ hasControl: true, getWorkspaceProject } as unknown as APIClient} workspace={workspace}
      label={workspace.name} expanded onToggle={vi.fn()} onEdit={vi.fn()} />
  </LocaleProvider></QueryClientProvider>);
  const trigger = screen.getByRole("button", { name: detailsLabel });
  trigger.focus();
  await user.keyboard("{Enter}");
  const copyButton = await screen.findByRole("button", { name: copyLabel });
  await waitFor(() => expect(screen.getByRole("button", { name: editLabel })).toHaveFocus());
  await user.keyboard("{Shift>}{Tab}{/Shift}");
  expect(copyButton).toHaveFocus();
  await user.keyboard("{Enter}");
  await waitFor(() => expect(within(copyButton).getByRole("status")).toHaveTextContent(copiedLabel));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(path);
  expect(await navigator.clipboard.readText()).toBe(path);
  expect(screen.getByText(path)).toBeVisible();
  expect(copyButton).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  await user.keyboard("{Enter}");
  expect(within(await screen.findByRole("button", { name: copyLabel })).getByRole("status")).toHaveTextContent(copyLabel);
});

it("reports clipboard rejection without success and lets the user retry", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockRejectedValueOnce(new DOMException("Write permission denied", "NotAllowedError"));
  const path = "D:\\Projects\\目录 with spaces";
  const getWorkspaceProject = vi.fn().mockResolvedValue({ ...workspace, directory_path: path, thread_count: 0, active_thread_count: 0 });
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider>
    <ProjectHeader client={{ hasControl: true, getWorkspaceProject } as unknown as APIClient} workspace={workspace}
      label={workspace.name} expanded onToggle={vi.fn()} />
  </LocaleProvider></QueryClientProvider>);
  await user.click(screen.getByRole("button", { name: "页面设计 项目信息" }));
  const copyButton = await screen.findByRole("button", { name: "复制项目目录" });
  await user.click(copyButton);
  expect(within(copyButton).getByRole("status")).toHaveTextContent("复制失败，请重试");
  expect(screen.queryByText("已复制项目目录")).not.toBeInTheDocument();
  expect(screen.getByText(path)).toBeVisible();
  await user.click(copyButton);
  expect(within(copyButton).getByRole("status")).toHaveTextContent("已复制项目目录");
  expect(writeText).toHaveBeenNthCalledWith(2, path);
});
