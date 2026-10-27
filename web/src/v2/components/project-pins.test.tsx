import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ThreadView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2Sidebar } from "./sidebar";

const projects = [
  { id: "project-a", name: "页面设计", created_at: "2026-10-22T00:00:00Z" },
  { id: "project-b", name: "接口实现", created_at: "2026-10-22T00:00:00Z" },
];
const thread: ThreadView = { id: "chat-a", protocol_version: "thread.v1", workspace_id: "project-a", project_workspace_id: "project-a",
  mission_id: "mission-a", title: "页面讨论", status: "active", active_run_id: "run-a", last_run_id: "run-a", version: 1,
  composer_state: "ready", created_at: "2026-10-22T00:00:00Z", updated_at: "2026-10-22T00:00:00Z" };
const threads = [thread, { ...thread, id: "chat-b", title: "接口讨论", project_workspace_id: "project-b" },
  { ...thread, id: "chat-local", title: "本地讨论", project_workspace_id: "" },
  { ...thread, id: "chat-pin", title: "置顶讨论", pinned: true }];
const onVisibleChatOrder = vi.fn();
function page(scopeID = "store-a", baseURL = "/api/v1") {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <LocaleProvider><V2RecoveryProvider client={{ baseURL }} scopeID={scopeID}>
      <V2Sidebar workspaces={projects} threads={threads} selectedThreadID={thread.id} searchOpen={false}
        onSearchOpen={vi.fn()} onNewConversation={vi.fn()} onOpenModels={vi.fn()} onSelectThread={vi.fn()}
        onOpenSettings={vi.fn()} onArchive={vi.fn()} onNewInProject={vi.fn()} onVisibleChatOrder={onVisibleChatOrder} />
    </V2RecoveryProvider></LocaleProvider>
  </QueryClientProvider>;
}
function projectOrder() {
  return screen.getAllByRole("region", { name: /^项目 / }).map((element) => element.getAttribute("aria-label"));
}
beforeEach(() => { localStorage.clear(); localStorage.setItem("prayu.locale.v1", "zh-CN"); vi.clearAllMocks(); });
afterEach(cleanup);

it("pins and unpins a project from the keyboard and keeps chat navigation in rendered order", async () => {
  const user = userEvent.setup();
  render(page());
  expect(onVisibleChatOrder).toHaveBeenLastCalledWith(["chat-pin", "chat-a", "chat-b", "chat-local"]);
  await user.click(screen.getByRole("button", { name: "接口实现 项目信息" }));
  await user.tab();
  const pin = screen.getByRole("button", { name: "置顶项目" });
  expect(pin).toHaveFocus();
  await user.keyboard("{Enter}");
  expect(projectOrder()).toEqual(["项目 接口实现", "项目 页面设计", "项目 本地聊天"]);
  expect(onVisibleChatOrder).toHaveBeenLastCalledWith(["chat-pin", "chat-b", "chat-a", "chat-local"]);
  expect(within(screen.getByRole("region", { name: "项目 接口实现" })).getByRole("button", { name: "接口讨论" })).toBeVisible();
  expect(screen.getByRole("button", { name: "取消置顶项目" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "取消置顶项目" })).toHaveFocus();
  await user.keyboard("{Enter}");
  expect(projectOrder()).toEqual(["项目 页面设计", "项目 接口实现", "项目 本地聊天"]);
  expect(onVisibleChatOrder).toHaveBeenLastCalledWith(["chat-pin", "chat-a", "chat-b", "chat-local"]);
});

it("restores project pins only for the same API and data store", async () => {
  const user = userEvent.setup();
  const first = render(page());
  await user.click(screen.getByRole("button", { name: "接口实现 项目信息" }));
  await user.click(screen.getByRole("button", { name: "置顶项目" }));
  first.unmount();
  const restored = render(page());
  expect(projectOrder()[0]).toBe("项目 接口实现");
  restored.rerender(page("store-b"));
  expect(projectOrder()[0]).toBe("项目 页面设计");
  restored.rerender(page("store-a", "/other-api"));
  expect(projectOrder()[0]).toBe("项目 页面设计");
  restored.rerender(page());
  expect(projectOrder()[0]).toBe("项目 接口实现");
});

it("keeps pin focus after opening details that were already visible on hover", async () => {
  const user = userEvent.setup();
  render(page());
  const details = screen.getByRole("button", { name: "接口实现 项目信息" });
  await user.hover(details);
  await screen.findByRole("dialog", { name: "接口实现 项目信息" });
  await user.click(details);
  await user.tab();
  expect(screen.getByRole("button", { name: "置顶项目" })).toHaveFocus();
  await user.keyboard("{Enter}");
  expect(screen.getByRole("button", { name: "取消置顶项目" })).toHaveFocus();
});

it("ignores invalid stored preference values and offers the action in English", async () => {
  localStorage.setItem("prayu.locale.v1", "en-US");
  const key = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent("/api/v1")}:store-a:pinned-projects`;
  localStorage.setItem(key, JSON.stringify({ version: "v2_recovery.v1", value: { invalid: true } }));
  const user = userEvent.setup();
  render(page());
  expect(screen.getAllByRole("region", { name: /^Project / })[0]).toHaveAccessibleName("Project 页面设计");
  await user.click(screen.getByRole("button", { name: "接口实现 project details" }));
  await user.click(screen.getByRole("button", { name: "Pin project" }));
  expect(screen.getByRole("button", { name: "Unpin project" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getAllByRole("region", { name: /^Project / })[0]).toHaveAccessibleName("Project 接口实现");
});
